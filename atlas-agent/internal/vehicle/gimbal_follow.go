package vehicle

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	gimbalpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/gimbal"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
)

// GimbalFollowConfig contains the safety envelope for image-space following.
// Angles are aircraft-forward gimbal angles; rates and accelerations are in
// degrees per second and degrees per second squared.
type GimbalFollowConfig struct {
	UpdateInterval       time.Duration
	TrackFreshness       time.Duration
	HoldTimeout          time.Duration
	Deadband             float64
	PitchGain            float64
	YawGain              float64
	MaxPitchRate         float64
	MaxYawRate           float64
	MaxPitchAcceleration float64
	MaxYawAcceleration   float64
	MinPitch             float64
	MaxPitch             float64
	MinYaw               float64
	MaxYaw               float64
	LimitMargin          float64
}

func DefaultGimbalFollowConfig() GimbalFollowConfig {
	return GimbalFollowConfig{
		UpdateInterval:       100 * time.Millisecond,
		TrackFreshness:       350 * time.Millisecond,
		HoldTimeout:          2 * time.Second,
		Deadband:             0.025,
		PitchGain:            60,
		YawGain:              80,
		MaxPitchRate:         20,
		MaxYawRate:           30,
		MaxPitchAcceleration: 60,
		MaxYawAcceleration:   90,
		MinPitch:             -90,
		MaxPitch:             30,
		MinYaw:               -180,
		MaxYaw:               180,
		LimitMargin:          2,
	}
}

func (config GimbalFollowConfig) Validate() error {
	if config.UpdateInterval < 50*time.Millisecond || config.UpdateInterval > 500*time.Millisecond {
		return errors.New("gimbal follow update interval must be between 50ms and 500ms")
	}
	if config.TrackFreshness < config.UpdateInterval || config.TrackFreshness > 2*time.Second {
		return errors.New("gimbal follow track freshness must be at least the update interval and at most 2s")
	}
	if config.HoldTimeout < config.TrackFreshness || config.HoldTimeout > 10*time.Second {
		return errors.New("gimbal follow hold timeout must be at least track freshness and at most 10s")
	}
	if !finiteBetween(config.Deadband, 0, 0.25) ||
		!finiteBetween(config.PitchGain, 1, 360) || !finiteBetween(config.YawGain, 1, 360) ||
		!finiteBetween(config.MaxPitchRate, 1, 90) || !finiteBetween(config.MaxYawRate, 1, 90) ||
		!finiteBetween(config.MaxPitchAcceleration, 1, 360) || !finiteBetween(config.MaxYawAcceleration, 1, 360) {
		return errors.New("gimbal follow gains, rates, accelerations, or deadband are outside the safety envelope")
	}
	if !finiteBetween(config.MinPitch, -180, 180) || !finiteBetween(config.MaxPitch, -180, 180) || config.MinPitch >= config.MaxPitch ||
		!finiteBetween(config.MinYaw, -360, 360) || !finiteBetween(config.MaxYaw, -360, 360) || config.MinYaw >= config.MaxYaw {
		return errors.New("gimbal follow physical angle limits are invalid")
	}
	if !finiteBetween(config.LimitMargin, 0, 15) || config.LimitMargin*2 >= config.MaxPitch-config.MinPitch || config.LimitMargin*2 >= config.MaxYaw-config.MinYaw {
		return errors.New("gimbal follow limit margin does not fit inside the physical angle limits")
	}
	return nil
}

type gimbalFollowSession struct {
	id               string
	controlSessionID string
	sourceID         string
	trackSessionID   string
	trackID          string
	gimbalID         int32
	cancel           context.CancelFunc
	lastPitchRate    float64
	lastYawRate      float64
	lastCommandAt    time.Time
	holdSince        time.Time
}

// ConfigureTrackFollowing connects the payload authority to the Atlas-owned
// tracking stage. A nil source keeps the feature unavailable and prevents the
// Agent from advertising it.
func (p *PayloadController) ConfigureTrackFollowing(source perception.TrackFollowSource, config GimbalFollowConfig) error {
	if source != nil {
		if err := config.Validate(); err != nil {
			return err
		}
	}
	p.commandMu.Lock()
	defer p.commandMu.Unlock()
	p.cancelTrackFollowLocked("TRACK_SOURCE_RECONFIGURED")
	p.mu.Lock()
	p.followSource = source
	p.followConfig = config
	p.mu.Unlock()
	return nil
}

func (p *PayloadController) startTrackFollow(ctx context.Context, input payloadCommand) (CommandResult, error) {
	if err := p.validateManual(input); err != nil {
		return CommandResult{Code: "PAYLOAD_CONTROL_REQUIRED", Message: err.Error()}, err
	}
	p.mu.Lock()
	source := p.followSource
	config := p.followConfig
	current := p.follow
	gimbalID := p.commandGimbalIDLocked(input)
	p.mu.Unlock()
	if source == nil {
		err := errors.New("track following is unavailable because no Atlas tracking source is active")
		return CommandResult{Code: "TRACK_FOLLOW_UNAVAILABLE", Message: err.Error()}, err
	}
	if strings.TrimSpace(input.SourceID) == "" || strings.TrimSpace(input.TrackSessionID) == "" || strings.TrimSpace(input.TrackID) == "" {
		err := errors.New("sourceId, trackSessionId, and trackId are required")
		return CommandResult{Code: "INVALID_TRACK_SELECTION", Message: err.Error()}, err
	}
	if gimbalID <= 0 {
		err := errors.New("no discovered gimbal is available for track following")
		return CommandResult{Code: "GIMBAL_UNAVAILABLE", Message: err.Error()}, err
	}
	if current != nil {
		if current.controlSessionID == input.ControlSessionID && current.sourceID == input.SourceID && current.trackSessionID == input.TrackSessionID && current.trackID == input.TrackID {
			return CommandResult{Code: "GIMBAL_FOLLOW_ACTIVE", Message: "Camera is already following the selected track"}, nil
		}
		err := errors.New("another operator-selected track is already controlling the gimbal")
		return CommandResult{Code: "GIMBAL_FOLLOW_ACTIVE", Message: err.Error()}, err
	}
	observation, ok := source.TrackForFollow(input.TrackSessionID, input.TrackID)
	if !ok || observation.SourceID != input.SourceID {
		err := errors.New("the selected session-scoped track is no longer available on this camera source")
		return CommandResult{Code: "TRACK_NOT_AVAILABLE", Message: err.Error()}, err
	}
	if observation.LifecycleState != perception.TrackLifecycleActive {
		err := fmt.Errorf("track following requires an ACTIVE track, got %s", observation.LifecycleState)
		return CommandResult{Code: "TRACK_NOT_CONFIRMED", Message: err.Error()}, err
	}
	if trackObservationStale(observation.LastObservedAt, time.Now(), config.TrackFreshness) {
		err := errors.New("the selected track observation is stale")
		return CommandResult{Code: "TRACK_STALE", Message: err.Error()}, err
	}
	if _, _, err := p.measuredGimbalAngles(ctx, gimbalID); err != nil {
		return CommandResult{Code: "GIMBAL_ATTITUDE_UNAVAILABLE", Message: err.Error()}, err
	}
	if err := p.commandFollowRates(ctx, gimbalID, 0, 0); err != nil {
		return CommandResult{Code: "GIMBAL_COMMAND_FAULT", Message: err.Error()}, err
	}
	followContext, cancel := context.WithCancel(context.Background())
	follow := &gimbalFollowSession{
		id:               input.ControlSessionID + ":" + input.TrackSessionID + ":" + input.TrackID,
		controlSessionID: input.ControlSessionID,
		sourceID:         input.SourceID, trackSessionID: input.TrackSessionID, trackID: input.TrackID,
		gimbalID: gimbalID, cancel: cancel, lastCommandAt: time.Now(),
	}
	p.mu.Lock()
	p.follow = follow
	if p.manual != nil && p.manual.id == input.ControlSessionID {
		p.manual.followExpected = true
		p.manual.followStopReason = ""
	}
	p.mu.Unlock()
	go p.runTrackFollow(followContext, follow.id)
	return CommandResult{Code: "GIMBAL_FOLLOW_STARTED", Message: "Camera is following the selected track"}, nil
}

func (p *PayloadController) stopTrackFollow(ctx context.Context, input payloadCommand) (CommandResult, error) {
	if err := p.validateManual(input); err != nil {
		return CommandResult{Code: "PAYLOAD_CONTROL_REQUIRED", Message: err.Error()}, err
	}
	p.mu.Lock()
	follow := p.follow
	p.mu.Unlock()
	if follow == nil {
		return CommandResult{Code: "GIMBAL_FOLLOW_STOPPED", Message: "Camera follow is already stopped"}, nil
	}
	if follow.controlSessionID != input.ControlSessionID {
		err := errors.New("camera follow belongs to a different payload-control session")
		return CommandResult{Code: "PAYLOAD_CONTROL_REQUIRED", Message: err.Error()}, err
	}
	p.cancelTrackFollowLocked("OPERATOR_REQUEST")
	if err := p.commandFollowRates(ctx, follow.gimbalID, 0, 0); err != nil {
		return CommandResult{Code: "GIMBAL_FOLLOW_STOP_FAILED", Message: err.Error()}, err
	}
	return CommandResult{Code: "GIMBAL_FOLLOW_STOPPED", Message: "Camera follow stopped at the current safe angle"}, nil
}

func (p *PayloadController) runTrackFollow(ctx context.Context, followID string) {
	p.mu.Lock()
	interval := p.followConfig.UpdateInterval
	p.mu.Unlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			p.stepTrackFollow(followID, now)
		}
	}
}

func (p *PayloadController) stepTrackFollow(followID string, now time.Time) {
	p.commandMu.Lock()
	defer p.commandMu.Unlock()
	p.mu.Lock()
	follow := p.follow
	source := p.followSource
	config := p.followConfig
	manualActive := follow != nil && p.manual != nil && p.manual.id == follow.controlSessionID && now.Before(p.manual.expiresAt)
	if follow == nil || follow.id != followID {
		p.mu.Unlock()
		return
	}
	copy := *follow
	p.mu.Unlock()
	if !manualActive {
		p.stopTrackFollowForSafetyLocked(copy, "PAYLOAD_LEASE_LOST", true)
		return
	}
	if source == nil {
		p.stopTrackFollowForSafetyLocked(copy, "TRACK_SOURCE_UNAVAILABLE", true)
		return
	}
	observation, ok := source.TrackForFollow(copy.trackSessionID, copy.trackID)
	if !ok || observation.SourceID != copy.sourceID {
		p.stopTrackFollowForSafetyLocked(copy, "TRACK_OR_SOURCE_CHANGED", true)
		return
	}
	switch observation.LifecycleState {
	case perception.TrackLifecycleTentative, perception.TrackLifecycleLost, perception.TrackLifecycleClosed:
		p.stopTrackFollowForSafetyLocked(copy, "TRACK_"+string(observation.LifecycleState), true)
		return
	case perception.TrackLifecycleTemporarilyOccluded:
		p.holdTrackFollowLocked(copy, "TRACK_TEMPORARILY_OCCLUDED", now, config.HoldTimeout)
		return
	case perception.TrackLifecycleActive:
		if trackObservationStale(observation.LastObservedAt, now, config.TrackFreshness) {
			p.holdTrackFollowLocked(copy, "TRACK_STALE", now, config.HoldTimeout)
			return
		}
	default:
		p.stopTrackFollowForSafetyLocked(copy, "TRACK_STATE_INVALID", true)
		return
	}
	pitch, yaw, err := p.measuredGimbalAnglesWithTimeout(copy.gimbalID)
	if err != nil {
		p.stopTrackFollowForSafetyLocked(copy, "GIMBAL_ATTITUDE_FAULT", true)
		return
	}
	targetX := observation.LatestConfirmedBox.X + observation.LatestConfirmedBox.Width/2
	targetY := observation.LatestConfirmedBox.Y + observation.LatestConfirmedBox.Height/2
	errorX := targetX - 0.5
	errorY := targetY - 0.5
	desiredYaw := proportionalRate(errorX, config.YawGain, config.Deadband, config.MaxYawRate)
	desiredPitch := proportionalRate(-errorY, config.PitchGain, config.Deadband, config.MaxPitchRate)
	dt := now.Sub(copy.lastCommandAt).Seconds()
	if dt <= 0 || dt > config.UpdateInterval.Seconds()*2 {
		dt = config.UpdateInterval.Seconds()
	}
	pitchRate := slewRate(copy.lastPitchRate, desiredPitch, config.MaxPitchAcceleration*dt)
	yawRate := slewRate(copy.lastYawRate, desiredYaw, config.MaxYawAcceleration*dt)
	pitchRate = protectPhysicalLimit(pitchRate, pitch, config.MinPitch, config.MaxPitch, config.LimitMargin, config.MaxPitchAcceleration)
	yawRate = protectPhysicalLimit(yawRate, yaw, config.MinYaw, config.MaxYaw, config.LimitMargin, config.MaxYawAcceleration)
	if err := p.commandFollowRatesWithTimeout(copy.gimbalID, pitchRate, yawRate); err != nil {
		p.stopTrackFollowForSafetyLocked(copy, "GIMBAL_COMMAND_FAULT", false)
		return
	}
	p.mu.Lock()
	if p.follow != nil && p.follow.id == followID {
		p.follow.lastPitchRate = pitchRate
		p.follow.lastYawRate = yawRate
		p.follow.lastCommandAt = now
		p.follow.holdSince = time.Time{}
	}
	p.mu.Unlock()
}

func (p *PayloadController) holdTrackFollowLocked(follow gimbalFollowSession, reason string, now time.Time, timeout time.Duration) {
	holdSince := follow.holdSince
	if holdSince.IsZero() {
		holdSince = now
		p.mu.Lock()
		if p.follow != nil && p.follow.id == follow.id {
			p.follow.holdSince = holdSince
		}
		p.mu.Unlock()
	}
	if now.Sub(holdSince) >= timeout {
		p.stopTrackFollowForSafetyLocked(follow, "TRACK_HOLD_TIMEOUT", true)
		return
	}
	if follow.lastPitchRate == 0 && follow.lastYawRate == 0 {
		return
	}
	if err := p.commandFollowRatesWithTimeout(follow.gimbalID, 0, 0); err != nil {
		p.stopTrackFollowForSafetyLocked(follow, "GIMBAL_COMMAND_FAULT", false)
		return
	}
	p.mu.Lock()
	if p.follow != nil && p.follow.id == follow.id {
		p.follow.lastPitchRate = 0
		p.follow.lastYawRate = 0
		p.follow.lastCommandAt = time.Now()
	}
	p.mu.Unlock()
	p.logger.Info("gimbal follow holding current angle", "reason", reason, "track_session_id", follow.trackSessionID, "track_id", follow.trackID)
}

func (p *PayloadController) stopTrackFollowForSafetyLocked(follow gimbalFollowSession, reason string, commandStop bool) {
	p.cancelTrackFollowLocked(reason)
	if commandStop {
		if err := p.commandFollowRatesWithTimeout(follow.gimbalID, 0, 0); err != nil {
			p.logger.Error("stop gimbal follow rates", "reason", reason, "error", err)
		}
	}
}

// cancelTrackFollowLocked requires commandMu. It changes controller state but
// leaves release/restoration to the owning payload session lifecycle.
func (p *PayloadController) cancelTrackFollowLocked(reason string) {
	p.mu.Lock()
	follow := p.follow
	if follow != nil {
		p.follow = nil
		follow.cancel()
		if p.manual != nil && p.manual.id == follow.controlSessionID && p.manual.followExpected {
			p.manual.followStopReason = reason
		}
	}
	p.mu.Unlock()
	if follow != nil {
		p.logger.Info("gimbal follow stopped", "reason", reason, "track_session_id", follow.trackSessionID, "track_id", follow.trackID)
	}
}

func (p *PayloadController) measuredGimbalAnglesWithTimeout(gimbalID int32) (float64, float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	return p.measuredGimbalAngles(ctx, gimbalID)
}

func (p *PayloadController) measuredGimbalAngles(ctx context.Context, gimbalID int32) (float64, float64, error) {
	response, err := p.gimbal.GetAttitude(ctx, &gimbalpb.GetAttitudeRequest{GimbalId: gimbalID})
	if _, resultErr := gimbalResponse(response.GetGimbalResult(), err); resultErr != nil {
		return 0, 0, fmt.Errorf("read measured gimbal attitude: %w", resultErr)
	}
	attitude := response.GetAttitude()
	if attitude == nil || attitude.GetEulerAngleForward() == nil || attitude.GetGimbalId() != gimbalID {
		return 0, 0, errors.New("measured gimbal attitude did not match the controlled gimbal")
	}
	angles := attitude.GetEulerAngleForward()
	return float64(angles.GetPitchDeg()), float64(angles.GetYawDeg()), nil
}

func (p *PayloadController) commandFollowRatesWithTimeout(gimbalID int32, pitchRate, yawRate float64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	return p.commandFollowRates(ctx, gimbalID, pitchRate, yawRate)
}

func (p *PayloadController) commandFollowRates(ctx context.Context, gimbalID int32, pitchRate, yawRate float64) error {
	response, err := p.gimbal.SetAngularRates(ctx, &gimbalpb.SetAngularRatesRequest{
		GimbalId: gimbalID, RollRateDegS: 0,
		PitchRateDegS: float32(pitchRate), YawRateDegS: float32(yawRate),
		GimbalMode: gimbalpb.GimbalMode_GIMBAL_MODE_YAW_FOLLOW,
		SendMode:   gimbalpb.SendMode_SEND_MODE_ONCE,
	})
	if _, resultErr := gimbalResponse(response.GetGimbalResult(), err); resultErr != nil {
		return fmt.Errorf("command gimbal follow rates: %w", resultErr)
	}
	return nil
}

func (p *PayloadController) commandGimbalIDLocked(input payloadCommand) int32 {
	if input.GimbalID > 0 {
		return input.GimbalID
	}
	if p.manual != nil && p.manual.gimbalID > 0 {
		return p.manual.gimbalID
	}
	return p.primaryGimbalIDLocked()
}

func trackObservationStale(observedAt, now time.Time, freshness time.Duration) bool {
	if observedAt.IsZero() {
		return true
	}
	age := now.Sub(observedAt)
	return age > freshness
}

func proportionalRate(error, gain, deadband, maximum float64) float64 {
	if math.Abs(error) <= deadband {
		return 0
	}
	return math.Max(-maximum, math.Min(maximum, error*gain))
}

func slewRate(current, target, maximumDelta float64) float64 {
	return current + math.Max(-maximumDelta, math.Min(maximumDelta, target-current))
}

func protectPhysicalLimit(rate, angle, minimum, maximum, margin, acceleration float64) float64 {
	var remaining float64
	if rate > 0 {
		remaining = maximum - margin - angle
	} else if rate < 0 {
		remaining = angle - (minimum + margin)
	} else {
		return 0
	}
	if remaining <= 0 {
		return 0
	}
	safeRate := math.Sqrt(2 * acceleration * remaining)
	if math.Abs(rate) > safeRate {
		return math.Copysign(safeRate, rate)
	}
	return rate
}

func finiteBetween(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}
