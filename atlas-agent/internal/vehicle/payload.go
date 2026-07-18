package vehicle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	camerapb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/camera"
	gimbalpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/gimbal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	minimumPayloadLease = 3 * time.Second
	maximumPayloadLease = 15 * time.Second
	inspectionControl   = "inspection"
	missionOverride     = "mission_override"
)

type payloadIntent struct {
	gimbal *gimbalIntent
	zoom   *float32
}

type payloadMissionPlan struct {
	global    payloadIntent
	waypoints map[uint32]payloadIntent
}

type PayloadEvent struct {
	RunID           string
	Type            string
	State           string
	CurrentWaypoint uint32
	ErrorCode       string
	Message         string
}

type manualPayloadSession struct {
	id              string
	kind            string
	runID           string
	gimbalID        int32
	cameraID        int32
	expiresAt       time.Time
	expirationTimer *time.Timer
}

// PayloadController is the single authority for mission and operator payload
// setpoints. PX4 remains responsible for navigation; gimbal/camera ownership
// stays here so a manual override cannot race a waypoint-carried setpoint.
type PayloadController struct {
	connection *grpc.ClientConn
	logger     *slog.Logger
	gimbal     gimbalpb.GimbalServiceClient
	camera     camerapb.CameraServiceClient
	siyi       *SIYICamera

	commandMu           sync.Mutex
	mu                  sync.Mutex
	gimbalIDs           []int32
	cameraIDs           []int32
	mavsdkCameraEnabled bool
	siyiAvailable       bool
	runID               string
	runState            string
	plan                payloadMissionPlan
	waypoint            uint32
	manual              *manualPayloadSession
	eventSink           func(PayloadEvent)
}

// ConfigureCameraTransports explicitly selects the camera drivers that may
// perform discovery and zoom commands. A disabled MAVSDK camera transport must
// never open CameraService streams because doing so activates MAVLink camera
// polling in mavsdk_server. The A8 Mini is fixed-focus, so its SIYI adapter
// deliberately exposes zoom only.
func (p *PayloadController) ConfigureCameraTransports(mavsdkEnabled bool, siyiAddress string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mavsdkCameraEnabled = mavsdkEnabled
	if !mavsdkEnabled {
		p.cameraIDs = nil
	}
	if siyiAddress == "" {
		p.siyi = nil
		p.siyiAvailable = false
		return
	}
	p.siyi = NewSIYICamera(siyiAddress)
	p.siyiAvailable = false
}

func NewPayloadController(address string, logger *slog.Logger) (*PayloadController, error) {
	if logger == nil {
		logger = slog.Default()
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("create MAVSDK payload client: %w", err)
	}
	return &PayloadController{
		connection: connection,
		logger:     logger,
		gimbal:     gimbalpb.NewGimbalServiceClient(connection),
		camera:     camerapb.NewCameraServiceClient(connection),
	}, nil
}

func (p *PayloadController) Close() error {
	p.mu.Lock()
	if p.manual != nil && p.manual.expirationTimer != nil {
		p.manual.expirationTimer.Stop()
	}
	p.manual = nil
	p.mu.Unlock()
	return p.connection.Close()
}

func (p *PayloadController) SetEventSink(sink func(PayloadEvent)) {
	p.mu.Lock()
	p.eventSink = sink
	p.mu.Unlock()
}

func (p *PayloadController) ConfigureMission(runID string, plan payloadMissionPlan) {
	p.commandMu.Lock()
	defer p.commandMu.Unlock()
	p.mu.Lock()
	if p.manual != nil && p.manual.kind == missionOverride {
		if p.manual.expirationTimer != nil {
			p.manual.expirationTimer.Stop()
		}
		p.manual = nil
	}
	p.runID = runID
	p.runState = "READY"
	p.plan = plan
	p.waypoint = 0
	p.mu.Unlock()
}

func (p *PayloadController) ActivateMission(ctx context.Context, runID, state string) error {
	p.commandMu.Lock()
	defer p.commandMu.Unlock()
	p.mu.Lock()
	if p.runID != runID {
		p.mu.Unlock()
		return errors.New("payload mission plan is not configured for this run")
	}
	manual := p.manual
	if manual != nil && manual.kind == inspectionControl {
		p.mu.Unlock()
		return errors.New("end inspection payload control before starting the mission")
	}
	p.runState = state
	p.mu.Unlock()
	if manual != nil {
		return nil
	}
	return p.restoreMissionIntent(ctx)
}

func (p *PayloadController) SetMissionState(runID, state string) {
	p.mu.Lock()
	if p.runID == runID {
		p.runState = state
	}
	p.mu.Unlock()
}

func (p *PayloadController) MissionProgress(ctx context.Context, runID string, waypoint uint32) {
	p.commandMu.Lock()
	defer p.commandMu.Unlock()
	p.mu.Lock()
	if p.runID != runID || p.waypoint == waypoint {
		p.mu.Unlock()
		return
	}
	p.waypoint = waypoint
	manual := p.manual != nil
	p.mu.Unlock()
	if manual {
		return
	}
	if err := p.restoreMissionIntent(ctx); err != nil {
		p.emit(PayloadEvent{RunID: runID, Type: "payload_restore_failed", State: p.state(), CurrentWaypoint: waypoint, ErrorCode: "MISSION_PAYLOAD_SETPOINT_FAILED", Message: err.Error()})
	}
}

func (p *PayloadController) EndMission(ctx context.Context, runID string) {
	p.commandMu.Lock()
	defer p.commandMu.Unlock()
	p.mu.Lock()
	if p.runID != runID {
		p.mu.Unlock()
		return
	}
	manual := p.manual
	clearManual := manual != nil && manual.kind == missionOverride && manual.runID == runID
	if clearManual && manual.expirationTimer != nil {
		manual.expirationTimer.Stop()
	}
	gimbalID := p.primaryGimbalIDLocked()
	if clearManual {
		p.manual = nil
	}
	p.runState = ""
	p.runID = ""
	p.plan = payloadMissionPlan{}
	p.mu.Unlock()
	if manual == nil || clearManual {
		stopContext, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		_ = p.stopAndReleaseGimbal(stopContext, gimbalID)
	}
}

// PointAtLocation executes an acknowledged mission-owned geographic ROI
// setpoint. It deliberately bypasses the manual-override lease while retaining
// the same single payload authority and refusing to race an active operator.
func (p *PayloadController) PointAtLocation(ctx context.Context, runID string, latitude, longitude float64, altitudeAMSL float32) error {
	p.commandMu.Lock()
	defer p.commandMu.Unlock()
	p.mu.Lock()
	if p.runID != runID || !matchesActiveMissionState(p.runState) {
		p.mu.Unlock()
		return errors.New("incident gimbal action requires the active mission run")
	}
	if p.manual != nil {
		p.mu.Unlock()
		return errors.New("incident gimbal action is blocked by active operator payload control")
	}
	gimbalID := p.primaryGimbalIDLocked()
	p.mu.Unlock()
	if gimbalID <= 0 {
		return errors.New("no discovered gimbal is available for the incident target")
	}
	if _, err := p.takeGimbalControl(ctx, gimbalID); err != nil {
		return fmt.Errorf("acquire gimbal control for incident target: %w", err)
	}
	response, err := p.gimbal.SetRoiLocation(ctx, &gimbalpb.SetRoiLocationRequest{
		GimbalId:     gimbalID,
		LatitudeDeg:  latitude,
		LongitudeDeg: longitude,
		AltitudeM:    altitudeAMSL,
	})
	if _, err = gimbalResponse(response.GetGimbalResult(), err); err != nil {
		return fmt.Errorf("point gimbal at incident: %w", err)
	}
	return nil
}

func (p *PayloadController) Execute(ctx context.Context, commandType, parametersJSON string) (CommandResult, error) {
	p.commandMu.Lock()
	defer p.commandMu.Unlock()
	var input payloadCommand
	if err := json.Unmarshal([]byte(parametersJSON), &input); err != nil {
		return CommandResult{}, fmt.Errorf("decode payload command: %w", err)
	}
	switch commandType {
	case "payload_control_begin":
		return p.beginManual(ctx, input)
	case "payload_control_renew":
		return p.renewManual(input)
	case "payload_control_end":
		return p.endManual(ctx, input)
	}
	if err := p.validateManual(input); err != nil {
		return CommandResult{Code: "PAYLOAD_CONTROL_REQUIRED", Message: err.Error()}, err
	}
	switch commandType {
	case "gimbal_set_angles":
		return p.setAngles(ctx, input)
	case "gimbal_set_rates":
		return p.setRates(ctx, input)
	case "gimbal_center":
		input.PitchDegrees = 0
		input.YawDegrees = 0
		input.YawFrame = "AIRCRAFT_RELATIVE"
		return p.setAngles(ctx, input)
	case "gimbal_set_roi":
		return p.setROI(ctx, input)
	case "camera_set_zoom":
		return p.setZoom(ctx, input)
	default:
		return CommandResult{}, fmt.Errorf("unsupported payload command %q", commandType)
	}
}

type payloadCommand struct {
	ControlContext            payloadControlContext `json:"controlContext"`
	ControlSessionID          string                `json:"controlSessionId"`
	LeaseDurationMS           int64                 `json:"leaseDurationMs"`
	GimbalID                  int32                 `json:"gimbalId"`
	CameraComponentID         int32                 `json:"cameraComponentId"`
	PitchDegrees              float32               `json:"pitchDegrees"`
	YawDegrees                float32               `json:"yawDegrees"`
	YawFrame                  string                `json:"yawFrame"`
	PitchRateDegreesPerSecond float32               `json:"pitchRateDegreesPerSecond"`
	YawRateDegreesPerSecond   float32               `json:"yawRateDegreesPerSecond"`
	Latitude                  float64               `json:"latitude"`
	Longitude                 float64               `json:"longitude"`
	AltitudeAmslMeters        float32               `json:"altitudeAmslMeters"`
	ZoomPercent               float32               `json:"zoomPercent"`
}

type payloadControlContext struct {
	Kind         string `json:"kind"`
	MissionRunID string `json:"missionRunId"`
}

func (p *PayloadController) beginManual(ctx context.Context, input payloadCommand) (CommandResult, error) {
	kind, runID, err := input.controlIdentity()
	if err != nil {
		return CommandResult{Code: "INVALID_CONTROL_CONTEXT", Message: err.Error()}, err
	}
	lease, err := payloadLease(input.LeaseDurationMS)
	if err != nil {
		return CommandResult{Code: "INVALID_PAYLOAD_LEASE", Message: err.Error()}, err
	}
	p.mu.Lock()
	if kind == missionOverride && (p.runID != runID || !matchesActiveMissionState(p.runState)) {
		p.mu.Unlock()
		err := errors.New("manual payload control requires the active RUNNING or PAUSED mission")
		return CommandResult{Code: "MISSION_NOT_ACTIVE", Message: err.Error()}, err
	}
	if kind == inspectionControl && matchesActiveMissionState(p.runState) {
		p.mu.Unlock()
		err := errors.New("inspection payload control is unavailable during an active mission")
		return CommandResult{Code: "MISSION_ACTIVE", Message: err.Error()}, err
	}
	if p.manual != nil && p.manual.id != input.ControlSessionID {
		p.mu.Unlock()
		err := errors.New("another operator payload-control session is active")
		return CommandResult{Code: "PAYLOAD_ALREADY_CONTROLLED", Message: err.Error()}, err
	}
	gimbalID := input.GimbalID
	if gimbalID == 0 {
		gimbalID = p.primaryGimbalIDLocked()
	}
	cameraID := input.CameraComponentID
	if cameraID == 0 {
		cameraID = p.primaryCameraIDLocked()
	}
	p.mu.Unlock()
	if gimbalID > 0 {
		if result, err := p.takeGimbalControl(ctx, gimbalID); err != nil {
			return result, err
		}
	}
	p.mu.Lock()
	p.manual = &manualPayloadSession{id: input.ControlSessionID, kind: kind, runID: runID, gimbalID: gimbalID, cameraID: cameraID, expiresAt: time.Now().Add(lease)}
	p.scheduleExpirationLocked(lease)
	waypoint := p.waypoint
	state := p.runState
	p.mu.Unlock()
	if kind == missionOverride {
		p.emit(PayloadEvent{RunID: runID, Type: "payload_manual_started", State: state, CurrentWaypoint: waypoint, Message: "Operator took manual payload control"})
	}
	message := "Inspection payload control active"
	if kind == missionOverride {
		message = "Mission payload override active"
	}
	return CommandResult{Code: "PAYLOAD_MANUAL_ACTIVE", Message: message}, nil
}

func (p *PayloadController) renewManual(input payloadCommand) (CommandResult, error) {
	lease, err := payloadLease(input.LeaseDurationMS)
	if err != nil {
		return CommandResult{Code: "INVALID_PAYLOAD_LEASE", Message: err.Error()}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.validateManualLocked(input); err != nil {
		return CommandResult{Code: "PAYLOAD_CONTROL_REQUIRED", Message: err.Error()}, err
	}
	p.manual.expiresAt = time.Now().Add(lease)
	p.scheduleExpirationLocked(lease)
	return CommandResult{Code: "PAYLOAD_LEASE_RENEWED", Message: "Manual payload control lease renewed"}, nil
}

func (p *PayloadController) endManual(ctx context.Context, input payloadCommand) (CommandResult, error) {
	p.mu.Lock()
	if err := p.validateManualLocked(input); err != nil {
		p.mu.Unlock()
		return CommandResult{Code: "PAYLOAD_CONTROL_REQUIRED", Message: err.Error()}, err
	}
	if p.manual.expirationTimer != nil {
		p.manual.expirationTimer.Stop()
	}
	manual := p.manual
	p.manual = nil
	runID := p.runID
	state := p.runState
	waypoint := p.waypoint
	p.mu.Unlock()
	if manual.kind == inspectionControl {
		if err := p.stopAndReleaseGimbal(ctx, manual.gimbalID); err != nil {
			return CommandResult{Code: "PAYLOAD_RELEASE_FAILED", Message: err.Error()}, err
		}
		return CommandResult{Code: "PAYLOAD_INSPECTION_RELEASED", Message: "Inspection control released safely"}, nil
	}
	if err := p.restoreMissionIntent(ctx); err != nil {
		p.emit(PayloadEvent{RunID: runID, Type: "payload_restore_failed", State: state, CurrentWaypoint: waypoint, ErrorCode: "PAYLOAD_RESTORE_FAILED", Message: err.Error()})
		return CommandResult{Code: "PAYLOAD_RESTORE_FAILED", Message: err.Error()}, err
	}
	p.emit(PayloadEvent{RunID: runID, Type: "payload_mission_restored", State: state, CurrentWaypoint: waypoint, Message: "Mission payload view restored"})
	return CommandResult{Code: "PAYLOAD_MISSION_RESTORED", Message: "Mission payload view restored"}, nil
}

func (p *PayloadController) expireManual(sessionID string) {
	p.commandMu.Lock()
	defer p.commandMu.Unlock()
	p.mu.Lock()
	if p.manual == nil || p.manual.id != sessionID || time.Now().Before(p.manual.expiresAt) {
		p.mu.Unlock()
		return
	}
	manual := p.manual
	runID := manual.runID
	state := p.runState
	waypoint := p.waypoint
	p.manual = nil
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if manual.kind == inspectionControl {
		if err := p.stopAndReleaseGimbal(ctx, manual.gimbalID); err != nil {
			p.logger.Error("release inspection payload control after lease expiry", "error", err)
		}
		return
	}
	if err := p.restoreMissionIntent(ctx); err != nil {
		p.logger.Error("restore mission payload after manual lease expiry", "mission_run_id", runID, "error", err)
		p.emit(PayloadEvent{RunID: runID, Type: "payload_restore_failed", State: state, CurrentWaypoint: waypoint, ErrorCode: "PAYLOAD_LEASE_RESTORE_FAILED", Message: err.Error()})
		return
	}
	p.emit(PayloadEvent{RunID: runID, Type: "payload_mission_restored", State: state, CurrentWaypoint: waypoint, Message: "Manual payload lease expired; mission view restored automatically"})
}

func (p *PayloadController) scheduleExpirationLocked(lease time.Duration) {
	if p.manual.expirationTimer != nil {
		p.manual.expirationTimer.Stop()
	}
	sessionID := p.manual.id
	p.manual.expirationTimer = time.AfterFunc(lease, func() { p.expireManual(sessionID) })
}

func (p *PayloadController) validateManual(input payloadCommand) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.validateManualLocked(input)
}

func (p *PayloadController) validateManualLocked(input payloadCommand) error {
	kind, runID, err := input.controlIdentity()
	if err != nil {
		return err
	}
	if p.manual == nil || p.manual.id != input.ControlSessionID || p.manual.kind != kind || p.manual.runID != runID {
		return errors.New("manual payload-control session is not active")
	}
	if time.Now().After(p.manual.expiresAt) {
		return errors.New("manual payload-control lease expired")
	}
	return nil
}

func (input payloadCommand) controlIdentity() (string, string, error) {
	if input.ControlSessionID == "" {
		return "", "", errors.New("controlSessionId is required")
	}
	switch input.ControlContext.Kind {
	case inspectionControl:
		if input.ControlContext.MissionRunID != "" {
			return "", "", errors.New("inspection control cannot contain missionRunId")
		}
		return inspectionControl, "", nil
	case missionOverride:
		if input.ControlContext.MissionRunID == "" {
			return "", "", errors.New("mission override requires missionRunId")
		}
		return missionOverride, input.ControlContext.MissionRunID, nil
	default:
		return "", "", errors.New("controlContext.kind must be inspection or mission_override")
	}
}

func (p *PayloadController) stopAndReleaseGimbal(ctx context.Context, gimbalID int32) error {
	if gimbalID <= 0 {
		return nil
	}
	rates, err := p.gimbal.SetAngularRates(ctx, &gimbalpb.SetAngularRatesRequest{GimbalId: gimbalID, SendMode: gimbalpb.SendMode_SEND_MODE_ONCE})
	if _, err = gimbalResponse(rates.GetGimbalResult(), err); err != nil {
		return fmt.Errorf("stop manual gimbal rate: %w", err)
	}
	release, err := p.gimbal.ReleaseControl(ctx, &gimbalpb.ReleaseControlRequest{GimbalId: gimbalID})
	if _, err = gimbalResponse(release.GetGimbalResult(), err); err != nil {
		return fmt.Errorf("release manual gimbal control: %w", err)
	}
	return nil
}

func (p *PayloadController) setAngles(ctx context.Context, input payloadCommand) (CommandResult, error) {
	mode := gimbalpb.GimbalMode_GIMBAL_MODE_YAW_FOLLOW
	if input.YawFrame == "NORTH_LOCKED" {
		mode = gimbalpb.GimbalMode_GIMBAL_MODE_YAW_LOCK
	}
	response, err := p.gimbal.SetAngles(ctx, &gimbalpb.SetAnglesRequest{GimbalId: p.commandGimbalID(input), RollDeg: 0, PitchDeg: input.PitchDegrees, YawDeg: input.YawDegrees, GimbalMode: mode, SendMode: gimbalpb.SendMode_SEND_MODE_ONCE})
	return gimbalResponse(response.GetGimbalResult(), err)
}

func (p *PayloadController) setRates(ctx context.Context, input payloadCommand) (CommandResult, error) {
	mode := gimbalpb.GimbalMode_GIMBAL_MODE_YAW_FOLLOW
	if input.YawFrame == "NORTH_LOCKED" {
		mode = gimbalpb.GimbalMode_GIMBAL_MODE_YAW_LOCK
	}
	response, err := p.gimbal.SetAngularRates(ctx, &gimbalpb.SetAngularRatesRequest{GimbalId: p.commandGimbalID(input), RollRateDegS: 0, PitchRateDegS: input.PitchRateDegreesPerSecond, YawRateDegS: input.YawRateDegreesPerSecond, GimbalMode: mode, SendMode: gimbalpb.SendMode_SEND_MODE_ONCE})
	return gimbalResponse(response.GetGimbalResult(), err)
}

func (p *PayloadController) setROI(ctx context.Context, input payloadCommand) (CommandResult, error) {
	response, err := p.gimbal.SetRoiLocation(ctx, &gimbalpb.SetRoiLocationRequest{GimbalId: p.commandGimbalID(input), LatitudeDeg: input.Latitude, LongitudeDeg: input.Longitude, AltitudeM: input.AltitudeAmslMeters})
	return gimbalResponse(response.GetGimbalResult(), err)
}

func (p *PayloadController) setZoom(ctx context.Context, input payloadCommand) (CommandResult, error) {
	cameraID := p.commandCameraID(input)
	p.mu.Lock()
	mavsdkEnabled := p.mavsdkCameraEnabled
	siyi := p.siyi
	siyiAvailable := p.siyiAvailable
	p.mu.Unlock()
	if mavsdkEnabled && cameraID > 0 {
		result, err := p.setMAVSDKZoom(ctx, cameraID, input.ZoomPercent)
		if err == nil {
			return result, nil
		}
		if result.Code != "" && siyiAvailable {
			p.logger.Warn("MAVSDK camera zoom failed; trying SIYI fallback", "camera_component_id", cameraID, "result", result.Code, "error", err)
		}
	}
	if siyiAvailable && siyi != nil {
		if err := siyi.SetZoom(ctx, input.ZoomPercent); err != nil {
			return CommandResult{Code: "SIYI_ZOOM_FAILED", Message: err.Error()}, err
		}
		return CommandResult{Code: "SIYI_ZOOM_SET", Message: "A8 Mini zoom set"}, nil
	}
	err := errors.New("no zoom-capable camera is available")
	return CommandResult{Code: "CAMERA_UNAVAILABLE", Message: err.Error()}, err
}

func (p *PayloadController) restoreMissionIntent(ctx context.Context) error {
	p.mu.Lock()
	if p.runID == "" {
		p.mu.Unlock()
		return nil
	}
	intent := p.plan.global
	if override, ok := p.plan.waypoints[p.waypoint]; ok {
		if override.gimbal != nil {
			intent.gimbal = override.gimbal
		}
		if override.zoom != nil {
			intent.zoom = override.zoom
		}
	}
	gimbalID := p.primaryGimbalIDLocked()
	cameraID := p.primaryCameraIDLocked()
	mavsdkEnabled := p.mavsdkCameraEnabled
	siyi := p.siyi
	siyiAvailable := p.siyiAvailable
	p.mu.Unlock()
	if gimbalID > 0 && intent.gimbal != nil && !float32IsNaN(intent.gimbal.pitch) {
		if _, err := p.takeGimbalControl(ctx, gimbalID); err != nil {
			return err
		}
		response, err := p.gimbal.SetAngularRates(ctx, &gimbalpb.SetAngularRatesRequest{GimbalId: gimbalID, SendMode: gimbalpb.SendMode_SEND_MODE_ONCE})
		if _, err = gimbalResponse(response.GetGimbalResult(), err); err != nil {
			return fmt.Errorf("stop manual gimbal rate: %w", err)
		}
		mode := gimbalpb.GimbalMode_GIMBAL_MODE_YAW_FOLLOW
		responseAngles, err := p.gimbal.SetAngles(ctx, &gimbalpb.SetAnglesRequest{GimbalId: gimbalID, PitchDeg: intent.gimbal.pitch, YawDeg: intent.gimbal.yaw, GimbalMode: mode, SendMode: gimbalpb.SendMode_SEND_MODE_ONCE})
		if _, err = gimbalResponse(responseAngles.GetGimbalResult(), err); err != nil {
			return fmt.Errorf("restore mission gimbal orientation: %w", err)
		}
	}
	if intent.zoom != nil && (mavsdkEnabled && cameraID > 0 || siyiAvailable) {
		if mavsdkEnabled && cameraID > 0 {
			if _, err := p.setMAVSDKZoom(ctx, cameraID, *intent.zoom); err == nil {
				return nil
			} else if !siyiAvailable {
				return fmt.Errorf("restore mission camera zoom: %w", err)
			}
		}
		if siyi != nil && siyiAvailable {
			if err := siyi.SetZoom(ctx, *intent.zoom); err != nil {
				return fmt.Errorf("restore mission A8 Mini zoom: %w", err)
			}
		}
	}
	return nil
}

func (p *PayloadController) setMAVSDKZoom(ctx context.Context, cameraID int32, zoomPercent float32) (CommandResult, error) {
	response, err := p.camera.ZoomRange(ctx, &camerapb.ZoomRangeRequest{ComponentId: cameraID, Range: zoomPercent})
	return cameraResponse(response.GetCameraResult(), err)
}

func (p *PayloadController) commandGimbalID(input payloadCommand) int32 {
	if input.GimbalID > 0 {
		return input.GimbalID
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.manual != nil && p.manual.gimbalID > 0 {
		return p.manual.gimbalID
	}
	return p.primaryGimbalIDLocked()
}

func (p *PayloadController) commandCameraID(input payloadCommand) int32 {
	if input.CameraComponentID > 0 {
		return input.CameraComponentID
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.manual != nil && p.manual.cameraID > 0 {
		return p.manual.cameraID
	}
	return p.primaryCameraIDLocked()
}

func (p *PayloadController) takeGimbalControl(ctx context.Context, gimbalID int32) (CommandResult, error) {
	response, err := p.gimbal.TakeControl(ctx, &gimbalpb.TakeControlRequest{GimbalId: gimbalID, ControlMode: gimbalpb.ControlMode_CONTROL_MODE_PRIMARY})
	return gimbalResponse(response.GetGimbalResult(), err)
}

func (p *PayloadController) DiscoverGimbals(ctx context.Context) ([]int32, error) {
	stream, err := p.gimbal.SubscribeGimbalList(ctx, &gimbalpb.SubscribeGimbalListRequest{})
	if err != nil {
		return nil, err
	}
	response, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	items := response.GetGimbalList().GetGimbals()
	ids := make([]int32, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.GetGimbalId())
	}
	p.mu.Lock()
	p.gimbalIDs = append([]int32(nil), ids...)
	p.mu.Unlock()
	return ids, nil
}

func (p *PayloadController) DiscoverCameras(ctx context.Context) ([]int32, error) {
	p.mu.Lock()
	mavsdkEnabled := p.mavsdkCameraEnabled
	siyi := p.siyi
	p.siyiAvailable = false
	p.mu.Unlock()
	if !mavsdkEnabled && siyi == nil {
		return nil, errors.New("no camera transport is configured")
	}

	var ids []int32
	var mavsdkErr error
	if mavsdkEnabled {
		ids, mavsdkErr = p.discoverMAVSDKCameras(ctx)
	}
	p.mu.Lock()
	p.cameraIDs = append([]int32(nil), ids...)
	p.mu.Unlock()

	var siyiErr error
	if siyi != nil {
		siyiContext, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		siyiErr = siyi.Discover(siyiContext)
		cancel()
		p.mu.Lock()
		p.siyiAvailable = siyiErr == nil
		p.mu.Unlock()
	}
	if len(ids) > 0 || siyiErr == nil && siyi != nil {
		return ids, nil
	}
	if mavsdkEnabled && mavsdkErr != nil && siyiErr != nil {
		return nil, fmt.Errorf("MAVSDK camera discovery: %v; SIYI camera discovery: %w", mavsdkErr, siyiErr)
	}
	if mavsdkEnabled && mavsdkErr != nil {
		return nil, mavsdkErr
	}
	if siyiErr != nil {
		return nil, siyiErr
	}
	return ids, nil
}

func (p *PayloadController) discoverMAVSDKCameras(ctx context.Context) ([]int32, error) {
	mavsdkContext, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	stream, err := p.camera.SubscribeCameraList(mavsdkContext, &camerapb.SubscribeCameraListRequest{})
	if err != nil {
		return nil, err
	}
	response, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	items := response.GetCameraList().GetCameras()
	ids := make([]int32, 0, len(items))
	for _, item := range items {
		if item.GetComponentId() > 0 {
			ids = append(ids, item.GetComponentId())
		}
	}
	return ids, nil
}

func (p *PayloadController) Capabilities() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	capabilities := []string{}
	if len(p.gimbalIDs) > 0 {
		capabilities = append(capabilities, "gimbal:detected", "gimbal:roi", "payload:manual_override", "command:gimbal_set_angles", "command:gimbal_set_rates", "command:gimbal_center", "command:gimbal_set_roi")
		for _, id := range p.gimbalIDs {
			capabilities = append(capabilities, fmt.Sprintf("gimbal:id:%d", id))
		}
	}
	if len(p.cameraIDs) > 0 || p.siyiAvailable {
		capabilities = append(capabilities, "camera:detected", "camera:zoom:range", "command:camera_set_zoom")
		for _, id := range p.cameraIDs {
			capabilities = append(capabilities, fmt.Sprintf("camera:component_id:%d", id))
		}
		if len(p.cameraIDs) > 0 {
			capabilities = append(capabilities, "camera:transport:mavsdk")
		}
		if p.siyiAvailable {
			capabilities = append(capabilities, "camera:transport:siyi_udp")
		}
	}
	return capabilities
}

func (p *PayloadController) primaryGimbalIDLocked() int32 {
	if len(p.gimbalIDs) == 0 {
		return 0
	}
	return p.gimbalIDs[0]
}

func (p *PayloadController) primaryCameraIDLocked() int32 {
	if len(p.cameraIDs) == 0 {
		return 0
	}
	return p.cameraIDs[0]
}

func (p *PayloadController) state() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runState
}

func (p *PayloadController) emit(event PayloadEvent) {
	p.mu.Lock()
	sink := p.eventSink
	p.mu.Unlock()
	if sink != nil {
		sink(event)
	}
}

func payloadLease(milliseconds int64) (time.Duration, error) {
	lease := time.Duration(milliseconds) * time.Millisecond
	if lease < minimumPayloadLease || lease > maximumPayloadLease {
		return 0, fmt.Errorf("payload lease must be between %s and %s", minimumPayloadLease, maximumPayloadLease)
	}
	return lease, nil
}

func matchesActiveMissionState(state string) bool {
	return state == "RUNNING" || state == "PAUSED"
}

func gimbalResponse(result *gimbalpb.GimbalResult, err error) (CommandResult, error) {
	if err != nil {
		return CommandResult{}, err
	}
	if result == nil {
		return CommandResult{}, errors.New("MAVSDK gimbal response did not include a result")
	}
	commandResult := CommandResult{Code: result.GetResult().String(), Message: result.GetResultStr()}
	if commandResult.Message == "" {
		commandResult.Message = commandResult.Code
	}
	if result.GetResult() != gimbalpb.GimbalResult_RESULT_SUCCESS {
		return commandResult, errors.New(commandResult.Message)
	}
	return commandResult, nil
}

func cameraResponse(result *camerapb.CameraResult, err error) (CommandResult, error) {
	if err != nil {
		return CommandResult{}, err
	}
	if result == nil {
		return CommandResult{}, errors.New("MAVSDK camera response did not include a result")
	}
	commandResult := CommandResult{Code: result.GetResult().String(), Message: result.GetResultStr()}
	if commandResult.Message == "" {
		commandResult.Message = commandResult.Code
	}
	if result.GetResult() != camerapb.CameraResult_RESULT_SUCCESS {
		return commandResult, errors.New(commandResult.Message)
	}
	return commandResult, nil
}

func missionPayloadPlan(actions []atlasAction) payloadMissionPlan {
	defaultZoom := float32(0)
	defaultGimbal := gimbalIntent{pitch: -35, yaw: 0, yawMode: "FOLLOW_DRONE_HEADING"}
	plan := payloadMissionPlan{global: payloadIntent{gimbal: &defaultGimbal, zoom: &defaultZoom}, waypoints: map[uint32]payloadIntent{}}
	for _, action := range actions {
		intent := plan.global
		waypoint, waypointSpecific := numberParam(action.Params, "waypointSequence")
		if waypointSpecific {
			intent = plan.waypoints[uint32(waypoint)]
		}
		switch action.Type {
		case "SET_GIMBAL_ORIENTATION":
			gimbal := parseGimbalIntent(action.Params)
			intent.gimbal = &gimbal
		case "SET_CAMERA_ZOOM":
			if value, ok := numberParam(action.Params, "zoomPercent"); ok && !math.IsNaN(value) {
				zoom := float32(math.Max(0, math.Min(100, value)))
				intent.zoom = &zoom
			}
		default:
			continue
		}
		if waypointSpecific {
			plan.waypoints[uint32(waypoint)] = intent
		} else {
			plan.global = intent
		}
	}
	return plan
}
