package vehicle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	actionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/action"
	offboardpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/offboard"
	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const earthRadiusM = 6_378_137.0

type AircraftFollowEnvelope struct {
	StandoffM                      float64
	AltitudeRelativeM              float64
	MinimumAltitudeRelativeM       float64
	MaximumAltitudeRelativeM       float64
	MaximumGroundSpeedMPS          float64
	MaximumAccelerationMPS2        float64
	MaximumDuration                time.Duration
	BoundaryCenterLatitude         float64
	BoundaryCenterLongitude        float64
	BoundaryRadiusM                float64
	MinimumBatteryPercent          float64
	MinimumTrackConfidence         float64
	MaximumGeolocationUncertaintyM float64
	MaximumVelocityUncertaintyMPS  float64
}

type AircraftFollowTarget struct {
	GeolocationID          string
	SelectionID            string
	SourceID               string
	TrackSessionID         string
	TrackID                string
	ObservedAt             time.Time
	Latitude               float64
	Longitude              float64
	AltitudeAMSLM          float64
	VelocityNorthMPS       float64
	VelocityEastMPS        float64
	HorizontalUncertaintyM float64
	VelocityUncertaintyMPS float64
	TrackConfidence        float64
	LifecycleState         string
	MotionStatus           string
}

type AircraftFollowOperation struct {
	OperationID         string
	SessionID           string
	DroneID             string
	Action              string
	Envelope            AircraftFollowEnvelope
	Target              AircraftFollowTarget
	LeaseExpiresAt      time.Time
	ReasonCode          string
	Reason              string
	ValidationReference string
}

type AircraftFollowUpdate struct {
	EventID      string
	OperationID  string
	SessionID    string
	State        string
	ObservedAt   time.Time
	ReasonCode   string
	Message      string
	EvidenceJSON string
}

type AircraftFollowControllerConfig struct {
	Enabled             bool
	ValidationReference string
	UpdateInterval      time.Duration
	TargetFreshness     time.Duration
	TelemetryFreshness  time.Duration
}

func DefaultAircraftFollowControllerConfig() AircraftFollowControllerConfig {
	return AircraftFollowControllerConfig{
		UpdateInterval:     100 * time.Millisecond,
		TargetFreshness:    2500 * time.Millisecond,
		TelemetryFreshness: 1500 * time.Millisecond,
	}
}

type aircraftFollowVehicle interface {
	SetVelocityNED(context.Context, followVelocitySetpoint) error
	StartOffboard(context.Context) error
	OffboardActive(context.Context) (bool, error)
	StopOffboard(context.Context) error
	Hold(context.Context) error
	Close() error
}

type followVelocitySetpoint struct {
	NorthMPS float64
	EastMPS  float64
	DownMPS  float64
	YawDeg   float64
}

type followStopRequest struct {
	state      string
	reasonCode string
	reason     string
	operation  string
}

type activeAircraftFollow struct {
	id                     string
	operationID            string
	droneID                string
	envelope               AircraftFollowEnvelope
	target                 AircraftFollowTarget
	leaseExpiresAt         time.Time
	startedAt              time.Time
	observationRadialNorth float64
	observationRadialEast  float64
	lastSetpoint           followVelocitySetpoint
	lastSetpointAt         time.Time
	stop                   chan followStopRequest
}

// AircraftFollowController is the Agent-owned, short-lease navigation loop.
// Native authorizes target identity and envelope; this controller independently
// enforces freshness, flight state, battery, boundary, Offboard activity and
// acceleration limits before every setpoint.
type AircraftFollowController struct {
	logger  *slog.Logger
	config  AircraftFollowControllerConfig
	vehicle aircraftFollowVehicle
	latest  func() (telemetry.Snapshot, bool)
	updates chan AircraftFollowUpdate

	mu     sync.Mutex
	active *activeAircraftFollow
}

func NewAircraftFollowController(address string, logger *slog.Logger, config AircraftFollowControllerConfig, latest func() (telemetry.Snapshot, bool)) (*AircraftFollowController, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if latest == nil {
		return nil, errors.New("aircraft follow requires a latest telemetry provider")
	}
	vehicle, err := newMAVSDKAircraftFollowVehicle(address)
	if err != nil {
		return nil, err
	}
	return newAircraftFollowControllerWithVehicle(logger, config, latest, vehicle)
}

func newAircraftFollowControllerWithVehicle(logger *slog.Logger, config AircraftFollowControllerConfig, latest func() (telemetry.Snapshot, bool), vehicle aircraftFollowVehicle) (*AircraftFollowController, error) {
	if config.UpdateInterval <= 0 {
		config.UpdateInterval = 100 * time.Millisecond
	}
	if config.TargetFreshness < config.UpdateInterval {
		return nil, errors.New("aircraft follow target freshness must exceed the update interval")
	}
	if config.TelemetryFreshness < config.UpdateInterval {
		return nil, errors.New("aircraft follow telemetry freshness must exceed the update interval")
	}
	if config.Enabled && config.ValidationReference == "" {
		return nil, errors.New("enabled aircraft follow controller requires validation evidence")
	}
	return &AircraftFollowController{
		logger: logger, config: config, latest: latest, vehicle: vehicle,
		updates: make(chan AircraftFollowUpdate, 32),
	}, nil
}

func (c *AircraftFollowController) Capabilities() []string {
	if !c.config.Enabled {
		return []string{"aircraft_follow:standoff:v1:unverified"}
	}
	return []string{
		"aircraft_follow:standoff:v1:verified",
		"aircraft_follow:validation:" + c.config.ValidationReference,
	}
}

func (c *AircraftFollowController) Updates() <-chan AircraftFollowUpdate { return c.updates }

func (c *AircraftFollowController) Apply(ctx context.Context, operation AircraftFollowOperation) {
	switch operation.Action {
	case "start":
		c.start(ctx, operation)
	case "renew":
		c.renew(operation)
	case "hold":
		c.requestStop(operation.SessionID, followStopRequest{state: "DEGRADED_HOLD", reasonCode: operation.ReasonCode, reason: operation.Reason, operation: operation.OperationID})
	case "end":
		c.requestStop(operation.SessionID, followStopRequest{state: "ENDED", reasonCode: defaultString(operation.ReasonCode, "OPERATOR_STOP"), reason: defaultString(operation.Reason, "Operator ended Follow from standoff"), operation: operation.OperationID})
	default:
		c.emit(operation, "DEGRADED_HOLD", "UNSUPPORTED_FOLLOW_ACTION", "Agent rejected an unsupported aircraft follow action", "{}")
	}
}

func (c *AircraftFollowController) GroundLinkLost() {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active != nil {
		c.requestStop(active.id, followStopRequest{state: "DEGRADED_HOLD", reasonCode: "GROUND_LINK_LOST", reason: "Ground-station stream ended; onboard controller entered Hold", operation: active.operationID})
	}
}

func (c *AircraftFollowController) Close() error {
	c.GroundLinkLost()
	timer := time.NewTimer(750 * time.Millisecond)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer timer.Stop()
	defer ticker.Stop()
	waiting := true
	for waiting {
		c.mu.Lock()
		active := c.active
		c.mu.Unlock()
		if active == nil {
			break
		}
		select {
		case <-timer.C:
			waiting = false
		case <-ticker.C:
		}
	}
	return c.vehicle.Close()
}

func (c *AircraftFollowController) start(ctx context.Context, operation AircraftFollowOperation) {
	if !c.config.Enabled {
		c.emit(operation, "DEGRADED_HOLD", "FOLLOW_CONTROL_UNVERIFIED", "Aircraft follow control is installed but not commissioned", "{}")
		return
	}
	if operation.ValidationReference != c.config.ValidationReference {
		c.emit(operation, "DEGRADED_HOLD", "VALIDATION_REFERENCE_MISMATCH", "Native authorization does not match the commissioned Agent validation reference", "{}")
		return
	}
	if err := validateAircraftFollowOperation(operation, time.Now().UTC(), c.config.TargetFreshness); err != nil {
		c.emit(operation, "DEGRADED_HOLD", "FOLLOW_AUTHORIZATION_INVALID", err.Error(), "{}")
		return
	}
	snapshot, ready := c.latest()
	if err := validateFollowTelemetry(snapshot, ready, operation.Envelope, time.Now().UTC(), c.config.TelemetryFreshness); err != nil {
		c.emit(operation, "DEGRADED_HOLD", "AIRCRAFT_NOT_READY", err.Error(), "{}")
		return
	}
	c.mu.Lock()
	if c.active != nil {
		c.mu.Unlock()
		c.emit(operation, "DEGRADED_HOLD", "FOLLOW_AUTHORITY_BUSY", "Another aircraft follow session already owns Offboard authority", "{}")
		return
	}
	radialNorth, radialEast := localOffsetM(operation.Target.Latitude, operation.Target.Longitude, *snapshot.Latitude, *snapshot.Longitude)
	radialNorm := math.Hypot(radialNorth, radialEast)
	if radialNorm < 1 {
		radialNorth, radialEast = -operation.Target.VelocityNorthMPS, -operation.Target.VelocityEastMPS
		radialNorm = math.Hypot(radialNorth, radialEast)
	}
	if radialNorm < 0.1 {
		heading := valueOr(snapshot.HeadingDeg, 0) * math.Pi / 180
		radialNorth, radialEast = -math.Cos(heading), -math.Sin(heading)
		radialNorm = 1
	}
	active := &activeAircraftFollow{
		id: operation.SessionID, operationID: operation.OperationID,
		droneID:  operation.DroneID,
		envelope: operation.Envelope, target: operation.Target,
		leaseExpiresAt: operation.LeaseExpiresAt, startedAt: time.Now().UTC(),
		observationRadialNorth: radialNorth / radialNorm,
		observationRadialEast:  radialEast / radialNorm,
		stop:                   make(chan followStopRequest, 1),
	}
	c.active = active
	c.mu.Unlock()
	c.emit(operation, "ACQUIRING", "", "Agent accepted the exact track and is acquiring bounded Offboard authority", "{}")
	go c.run(ctx, active)
}

func (c *AircraftFollowController) renew(operation AircraftFollowOperation) {
	c.mu.Lock()
	active := c.active
	if active == nil || active.id != operation.SessionID {
		c.mu.Unlock()
		c.emit(operation, "DEGRADED_HOLD", "FOLLOW_SESSION_NOT_ACTIVE", "Agent has no matching active follow authority", "{}")
		return
	}
	validatedOperation := operation
	validatedOperation.Envelope = active.envelope
	if operation.DroneID != active.droneID || operation.ValidationReference != c.config.ValidationReference {
		c.mu.Unlock()
		c.requestStop(operation.SessionID, followStopRequest{state: "DEGRADED_HOLD", reasonCode: "FOLLOW_AUTHORIZATION_CHANGED", reason: "Follow renewal changed the authorized aircraft or commissioning reference", operation: operation.OperationID})
		return
	}
	if err := validateAircraftFollowOperation(validatedOperation, time.Now().UTC(), c.config.TargetFreshness); err != nil {
		c.mu.Unlock()
		c.requestStop(operation.SessionID, followStopRequest{state: "DEGRADED_HOLD", reasonCode: "TARGET_UPDATE_INVALID", reason: err.Error(), operation: operation.OperationID})
		return
	}
	if operation.Target.SelectionID != active.target.SelectionID || operation.Target.TrackSessionID != active.target.TrackSessionID || operation.Target.TrackID != active.target.TrackID || operation.Target.SourceID != active.target.SourceID {
		c.mu.Unlock()
		c.requestStop(operation.SessionID, followStopRequest{state: "DEGRADED_HOLD", reasonCode: "TRACK_IDENTITY_CHANGED", reason: "Target update did not match the exact authorized track", operation: operation.OperationID})
		return
	}
	active.target = operation.Target
	active.leaseExpiresAt = operation.LeaseExpiresAt
	c.mu.Unlock()
}

func (c *AircraftFollowController) requestStop(sessionID string, request followStopRequest) {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active == nil || active.id != sessionID {
		operation := AircraftFollowOperation{SessionID: sessionID, OperationID: request.operation}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = c.vehicle.StopOffboard(ctx)
		_ = c.vehicle.Hold(ctx)
		cancel()
		c.emit(operation, request.state, request.reasonCode, request.reason, "{}")
		return
	}
	select {
	case active.stop <- request:
	default:
	}
}

func (c *AircraftFollowController) run(ctx context.Context, active *activeAircraftFollow) {
	if err := c.enterOffboard(ctx); err != nil {
		c.finish(active, followStopRequest{state: "DEGRADED_HOLD", reasonCode: "OFFBOARD_START_FAILED", reason: err.Error(), operation: active.operationID})
		return
	}
	c.emit(AircraftFollowOperation{SessionID: active.id, OperationID: active.operationID}, "FOLLOWING", "", "PX4 Offboard active inside the reviewed standoff envelope", "{}")
	ticker := time.NewTicker(c.config.UpdateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.finish(active, followStopRequest{state: "DEGRADED_HOLD", reasonCode: "GROUND_LINK_LOST", reason: "Ground-station stream ended; onboard controller entered Hold", operation: active.operationID})
			return
		case request := <-active.stop:
			c.finish(active, request)
			return
		case now := <-ticker.C:
			if request := c.watchdog(active, now.UTC()); request != nil {
				c.finish(active, *request)
				return
			}
			setpoint, err := c.nextSetpoint(active, now.UTC())
			if err != nil {
				c.finish(active, followStopRequest{state: "DEGRADED_HOLD", reasonCode: "SETPOINT_REJECTED", reason: err.Error(), operation: active.operationID})
				return
			}
			commandContext, cancel := context.WithTimeout(ctx, c.config.UpdateInterval)
			err = c.vehicle.SetVelocityNED(commandContext, setpoint)
			cancel()
			if err != nil {
				c.finish(active, followStopRequest{state: "DEGRADED_HOLD", reasonCode: "OFFBOARD_SETPOINT_FAILED", reason: err.Error(), operation: active.operationID})
				return
			}
			active.lastSetpoint, active.lastSetpointAt = setpoint, now.UTC()
		}
	}
}

func (c *AircraftFollowController) enterOffboard(ctx context.Context) error {
	commandContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := c.vehicle.SetVelocityNED(commandContext, followVelocitySetpoint{}); err != nil {
		return fmt.Errorf("publish initial zero-velocity Offboard setpoint: %w", err)
	}
	if err := c.vehicle.StartOffboard(commandContext); err != nil {
		return fmt.Errorf("start PX4 Offboard: %w", err)
	}
	return nil
}

func (c *AircraftFollowController) watchdog(active *activeAircraftFollow, now time.Time) *followStopRequest {
	c.mu.Lock()
	target := active.target
	leaseExpiresAt := active.leaseExpiresAt
	c.mu.Unlock()
	fail := func(code, reason string) *followStopRequest {
		return &followStopRequest{state: "DEGRADED_HOLD", reasonCode: code, reason: reason, operation: active.operationID}
	}
	if !now.Before(leaseExpiresAt) {
		return fail("OPERATOR_LEASE_EXPIRED", "Operator supervision lease expired")
	}
	if now.Sub(target.ObservedAt) > c.config.TargetFreshness {
		return fail("GEOLOCATION_STALE", "Validated world-space target state became stale")
	}
	if now.Sub(active.startedAt) >= active.envelope.MaximumDuration {
		return fail("MAXIMUM_DURATION_REACHED", "Operator-reviewed maximum follow duration was reached")
	}
	snapshot, ready := c.latest()
	if err := validateFollowTelemetry(snapshot, ready, active.envelope, now, c.config.TelemetryFreshness); err != nil {
		return fail("AIRCRAFT_STATE_DEGRADED", err.Error())
	}
	if distanceM(active.envelope.BoundaryCenterLatitude, active.envelope.BoundaryCenterLongitude, target.Latitude, target.Longitude) > active.envelope.BoundaryRadiusM {
		return fail("FOLLOW_GEOFENCE_VIOLATION", "Target crossed the operator-reviewed follow boundary")
	}
	checkContext, cancel := context.WithTimeout(context.Background(), c.config.UpdateInterval)
	offboardActive, err := c.vehicle.OffboardActive(checkContext)
	cancel()
	if err != nil || !offboardActive {
		return fail("PX4_OFFBOARD_LOSS", "PX4 no longer reports active Offboard control")
	}
	return nil
}

func (c *AircraftFollowController) nextSetpoint(active *activeAircraftFollow, now time.Time) (followVelocitySetpoint, error) {
	c.mu.Lock()
	target := active.target
	c.mu.Unlock()
	snapshot, ready := c.latest()
	if err := validateFollowTelemetry(snapshot, ready, active.envelope, now, c.config.TelemetryFreshness); err != nil {
		return followVelocitySetpoint{}, err
	}
	predictionSeconds := math.Max(0, math.Min(now.Sub(target.ObservedAt).Seconds(), c.config.TargetFreshness.Seconds()))
	predictedLatitude, predictedLongitude := offsetCoordinate(target.Latitude, target.Longitude, target.VelocityNorthMPS*predictionSeconds, target.VelocityEastMPS*predictionSeconds)
	desiredLatitude, desiredLongitude := offsetCoordinate(predictedLatitude, predictedLongitude, active.observationRadialNorth*active.envelope.StandoffM, active.observationRadialEast*active.envelope.StandoffM)
	if distanceM(active.envelope.BoundaryCenterLatitude, active.envelope.BoundaryCenterLongitude, desiredLatitude, desiredLongitude) > active.envelope.BoundaryRadiusM {
		return followVelocitySetpoint{}, errors.New("desired observation point crosses the operator-reviewed follow geofence")
	}
	errorNorth, errorEast := localOffsetM(*snapshot.Latitude, *snapshot.Longitude, desiredLatitude, desiredLongitude)
	north := target.VelocityNorthMPS + errorNorth*0.25
	east := target.VelocityEastMPS + errorEast*0.25
	north, east = clampVector(north, east, active.envelope.MaximumGroundSpeedMPS)
	deltaSeconds := c.config.UpdateInterval.Seconds()
	if !active.lastSetpointAt.IsZero() {
		deltaSeconds = math.Max(0.01, now.Sub(active.lastSetpointAt).Seconds())
	}
	maximumDelta := active.envelope.MaximumAccelerationMPS2 * deltaSeconds
	deltaNorth, deltaEast := clampVector(north-active.lastSetpoint.NorthMPS, east-active.lastSetpoint.EastMPS, maximumDelta)
	north = active.lastSetpoint.NorthMPS + deltaNorth
	east = active.lastSetpoint.EastMPS + deltaEast
	down := math.Max(-1, math.Min(1, (*snapshot.RelativeAltitudeM-active.envelope.AltitudeRelativeM)*0.25))
	yawNorth, yawEast := localOffsetM(*snapshot.Latitude, *snapshot.Longitude, predictedLatitude, predictedLongitude)
	yaw := math.Mod(math.Atan2(yawEast, yawNorth)*180/math.Pi+360, 360)
	return followVelocitySetpoint{NorthMPS: north, EastMPS: east, DownMPS: down, YawDeg: yaw}, nil
}

func (c *AircraftFollowController) finish(active *activeAircraftFollow, request followStopRequest) {
	stopContext, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = c.vehicle.SetVelocityNED(stopContext, followVelocitySetpoint{YawDeg: active.lastSetpoint.YawDeg})
	stopError := c.vehicle.StopOffboard(stopContext)
	holdError := c.vehicle.Hold(stopContext)
	cancel()
	c.mu.Lock()
	if c.active == active {
		c.active = nil
	}
	c.mu.Unlock()
	evidence := fmt.Sprintf(`{"offboardStopError":%q,"holdError":%q}`, errorString(stopError), errorString(holdError))
	c.emit(AircraftFollowOperation{SessionID: active.id, OperationID: request.operation}, request.state, request.reasonCode, request.reason, evidence)
}

func (c *AircraftFollowController) emit(operation AircraftFollowOperation, state, reasonCode, message, evidence string) {
	update := AircraftFollowUpdate{
		EventID:     fmt.Sprintf("follow-%d", time.Now().UTC().UnixNano()),
		OperationID: operation.OperationID, SessionID: operation.SessionID,
		State: state, ObservedAt: time.Now().UTC(), ReasonCode: reasonCode,
		Message: message, EvidenceJSON: evidence,
	}
	select {
	case c.updates <- update:
	default:
		c.logger.Error("aircraft follow update queue full", "session_id", operation.SessionID, "state", state)
	}
}

func validateAircraftFollowOperation(operation AircraftFollowOperation, now time.Time, targetFreshness time.Duration) error {
	if operation.OperationID == "" || operation.SessionID == "" || operation.DroneID == "" {
		return errors.New("aircraft follow operation identity is incomplete")
	}
	if operation.LeaseExpiresAt.Before(now) || operation.LeaseExpiresAt.Sub(now) > 5*time.Second {
		return errors.New("aircraft follow operator lease is expired or exceeds five seconds")
	}
	if err := validateAircraftFollowEnvelope(operation.Envelope); err != nil {
		return err
	}
	target := operation.Target
	if target.GeolocationID == "" || target.SelectionID == "" || target.TrackSessionID == "" || target.TrackID == "" || target.SourceID == "" {
		return errors.New("aircraft follow target identity is incomplete")
	}
	if target.LifecycleState != "ACTIVE" || target.MotionStatus != "FILTERED" {
		return errors.New("aircraft follow requires an active track with filtered world-space motion")
	}
	if now.Sub(target.ObservedAt) > targetFreshness || target.ObservedAt.After(now.Add(250*time.Millisecond)) {
		return errors.New("aircraft follow target observation is stale or future-dated")
	}
	if !validCoordinate(target.Latitude, target.Longitude) || !finite(target.AltitudeAMSLM) || !finite(target.VelocityNorthMPS) || !finite(target.VelocityEastMPS) {
		return errors.New("aircraft follow target contains invalid world-space values")
	}
	if target.TrackConfidence < operation.Envelope.MinimumTrackConfidence || target.HorizontalUncertaintyM > operation.Envelope.MaximumGeolocationUncertaintyM || target.VelocityUncertaintyMPS > operation.Envelope.MaximumVelocityUncertaintyMPS {
		return errors.New("aircraft follow target quality is outside the reviewed envelope")
	}
	if distanceM(operation.Envelope.BoundaryCenterLatitude, operation.Envelope.BoundaryCenterLongitude, target.Latitude, target.Longitude) > operation.Envelope.BoundaryRadiusM {
		return errors.New("aircraft follow target lies outside the reviewed geographic boundary")
	}
	return nil
}

func validateAircraftFollowEnvelope(envelope AircraftFollowEnvelope) error {
	if !between(envelope.StandoffM, 10, 500) || !between(envelope.AltitudeRelativeM, 5, 120) || !between(envelope.MinimumAltitudeRelativeM, 5, 120) || !between(envelope.MaximumAltitudeRelativeM, 5, 120) || envelope.MinimumAltitudeRelativeM > envelope.AltitudeRelativeM || envelope.AltitudeRelativeM > envelope.MaximumAltitudeRelativeM {
		return errors.New("aircraft follow altitude or standoff envelope is invalid")
	}
	if !between(envelope.MaximumGroundSpeedMPS, 0.5, 15) || !between(envelope.MaximumAccelerationMPS2, 0.1, 5) || envelope.MaximumDuration < 10*time.Second || envelope.MaximumDuration > 30*time.Minute {
		return errors.New("aircraft follow speed, acceleration, or duration envelope is invalid")
	}
	if !validCoordinate(envelope.BoundaryCenterLatitude, envelope.BoundaryCenterLongitude) || !between(envelope.BoundaryRadiusM, 25, 5000) || !between(envelope.MinimumBatteryPercent, 15, 100) || !between(envelope.MinimumTrackConfidence, 0.5, 1) || !between(envelope.MaximumGeolocationUncertaintyM, 1, 100) || !between(envelope.MaximumVelocityUncertaintyMPS, 0.1, 25) {
		return errors.New("aircraft follow quality, battery, or geographic envelope is invalid")
	}
	return nil
}

func validateFollowTelemetry(snapshot telemetry.Snapshot, ready bool, envelope AircraftFollowEnvelope, now time.Time, freshness time.Duration) error {
	if !ready || snapshot.ObservedAt.IsZero() || now.Sub(snapshot.ObservedAt) > freshness {
		return errors.New("aircraft telemetry is unavailable or stale")
	}
	if snapshot.Armed == nil || !*snapshot.Armed || snapshot.InAir == nil || !*snapshot.InAir {
		return errors.New("aircraft is not armed and in flight")
	}
	if snapshot.Health == nil || !snapshot.Health.LocalPositionOK || !snapshot.Health.GlobalPositionOK {
		return errors.New("PX4 local or global position health is unavailable")
	}
	if snapshot.Latitude == nil || snapshot.Longitude == nil || !validCoordinate(*snapshot.Latitude, *snapshot.Longitude) || snapshot.RelativeAltitudeM == nil {
		return errors.New("aircraft position or relative altitude is unavailable")
	}
	if snapshot.BatteryPercent == nil || *snapshot.BatteryPercent < envelope.MinimumBatteryPercent {
		return errors.New("aircraft battery is below the reviewed reserve")
	}
	if *snapshot.RelativeAltitudeM < envelope.MinimumAltitudeRelativeM || *snapshot.RelativeAltitudeM > envelope.MaximumAltitudeRelativeM {
		return errors.New("aircraft altitude left the reviewed follow band")
	}
	if distanceM(envelope.BoundaryCenterLatitude, envelope.BoundaryCenterLongitude, *snapshot.Latitude, *snapshot.Longitude) > envelope.BoundaryRadiusM {
		return errors.New("aircraft crossed the reviewed follow geofence")
	}
	return nil
}

type mavsdkAircraftFollowVehicle struct {
	connection *grpc.ClientConn
	offboard   offboardpb.OffboardServiceClient
	action     actionpb.ActionServiceClient
}

func newMAVSDKAircraftFollowVehicle(address string) (*mavsdkAircraftFollowVehicle, error) {
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("create MAVSDK aircraft follow client: %w", err)
	}
	return &mavsdkAircraftFollowVehicle{
		connection: connection,
		offboard:   offboardpb.NewOffboardServiceClient(connection),
		action:     actionpb.NewActionServiceClient(connection),
	}, nil
}

func (v *mavsdkAircraftFollowVehicle) SetVelocityNED(ctx context.Context, setpoint followVelocitySetpoint) error {
	response, err := v.offboard.SetVelocityNed(ctx, &offboardpb.SetVelocityNedRequest{VelocityNedYaw: &offboardpb.VelocityNedYaw{
		NorthMS: float32(setpoint.NorthMPS), EastMS: float32(setpoint.EastMPS), DownMS: float32(setpoint.DownMPS), YawDeg: float32(setpoint.YawDeg),
	}})
	return offboardResult(response.GetOffboardResult(), err)
}

func (v *mavsdkAircraftFollowVehicle) StartOffboard(ctx context.Context) error {
	response, err := v.offboard.Start(ctx, &offboardpb.StartRequest{})
	return offboardResult(response.GetOffboardResult(), err)
}

func (v *mavsdkAircraftFollowVehicle) OffboardActive(ctx context.Context) (bool, error) {
	response, err := v.offboard.IsActive(ctx, &offboardpb.IsActiveRequest{})
	if err != nil {
		return false, err
	}
	return response.GetIsActive(), nil
}

func (v *mavsdkAircraftFollowVehicle) StopOffboard(ctx context.Context) error {
	response, err := v.offboard.Stop(ctx, &offboardpb.StopRequest{})
	return offboardResult(response.GetOffboardResult(), err)
}

func (v *mavsdkAircraftFollowVehicle) Hold(ctx context.Context) error {
	response, err := v.action.Hold(ctx, &actionpb.HoldRequest{})
	if err != nil {
		return err
	}
	result := response.GetActionResult()
	if result == nil || result.GetResult() != actionpb.ActionResult_RESULT_SUCCESS {
		return fmt.Errorf("PX4 Hold failed: %s", result.GetResultStr())
	}
	return nil
}

func (v *mavsdkAircraftFollowVehicle) Close() error { return v.connection.Close() }

func offboardResult(result *offboardpb.OffboardResult, err error) error {
	if err != nil {
		return err
	}
	if result == nil || result.GetResult() != offboardpb.OffboardResult_RESULT_SUCCESS {
		return fmt.Errorf("MAVSDK Offboard failed: %s", result.GetResultStr())
	}
	return nil
}

func localOffsetM(latitudeA, longitudeA, latitudeB, longitudeB float64) (float64, float64) {
	meanLatitude := (latitudeA + latitudeB) * 0.5 * math.Pi / 180
	north := (latitudeB - latitudeA) * math.Pi / 180 * earthRadiusM
	east := (longitudeB - longitudeA) * math.Pi / 180 * earthRadiusM * math.Cos(meanLatitude)
	return north, east
}

func offsetCoordinate(latitude, longitude, northM, eastM float64) (float64, float64) {
	latitudeRadians := latitude * math.Pi / 180
	nextLatitude := latitudeRadians + northM/earthRadiusM
	longitudeScale := earthRadiusM * math.Cos(latitudeRadians)
	if math.Abs(longitudeScale) < 1e-6 {
		return latitude, longitude
	}
	return nextLatitude * 180 / math.Pi, longitude + eastM/longitudeScale*180/math.Pi
}

func distanceM(latitudeA, longitudeA, latitudeB, longitudeB float64) float64 {
	north, east := localOffsetM(latitudeA, longitudeA, latitudeB, longitudeB)
	return math.Hypot(north, east)
}

func clampVector(north, east, maximum float64) (float64, float64) {
	norm := math.Hypot(north, east)
	if norm <= maximum || norm == 0 {
		return north, east
	}
	scale := maximum / norm
	return north * scale, east * scale
}

func between(value, minimum, maximum float64) bool {
	return finite(value) && value >= minimum && value <= maximum
}
func validCoordinate(latitude, longitude float64) bool {
	return between(latitude, -90, 90) && between(longitude, -180, 180)
}
func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func valueOr(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}
func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
