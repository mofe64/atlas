package vehicle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/identity"
	actionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/action"
	missionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/mission"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type MissionOperation struct {
	OperationID     string
	RunID           string
	Type            string
	MissionPlanJSON string
}

type MissionUpdate struct {
	EventID         string
	OperationID     string
	RunID           string
	Type            string
	State           string
	ObservedAt      time.Time
	Progress        *float64
	CurrentWaypoint *uint32
	TotalWaypoints  *uint32
	ErrorCode       string
	Message         string
	EvidenceJSON    string
}

type MissionExecutor struct {
	connection    *grpc.ClientConn
	logger        *slog.Logger
	mission       missionpb.MissionServiceClient
	action        actionpb.ActionServiceClient
	payload       *PayloadController
	ownsPayload   bool
	updates       chan MissionUpdate
	operationMu   sync.Mutex
	mu            sync.Mutex
	uploadedRunID string
	activeRunID   string
	state         string
	watchCancel   context.CancelFunc
}

func NewMissionExecutor(address string, logger *slog.Logger, sharedPayload ...*PayloadController) (*MissionExecutor, error) {
	if logger == nil {
		logger = slog.Default()
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("create MAVSDK mission client: %w", err)
	}
	var payload *PayloadController
	ownsPayload := false
	if len(sharedPayload) > 0 && sharedPayload[0] != nil {
		payload = sharedPayload[0]
	} else {
		payload, err = NewPayloadController(address, logger)
		if err != nil {
			_ = connection.Close()
			return nil, err
		}
		ownsPayload = true
	}
	executor := &MissionExecutor{
		connection:  connection,
		logger:      logger,
		mission:     missionpb.NewMissionServiceClient(connection),
		action:      actionpb.NewActionServiceClient(connection),
		payload:     payload,
		ownsPayload: ownsPayload,
		updates:     make(chan MissionUpdate, 64),
	}
	payload.SetEventSink(executor.emitPayloadEvent)
	return executor, nil
}

func (e *MissionExecutor) Close() error {
	e.mu.Lock()
	if e.watchCancel != nil {
		e.watchCancel()
	}
	e.mu.Unlock()
	err := e.connection.Close()
	if e.ownsPayload {
		if payloadErr := e.payload.Close(); err == nil {
			err = payloadErr
		}
	}
	return err
}

func (e *MissionExecutor) Updates() <-chan MissionUpdate { return e.updates }

func (e *MissionExecutor) Capabilities() []string {
	return []string{"mission:upload", "mission:start", "mission:auto_arm", "mission:pause", "mission:resume", "mission:cancel", "mission:return_to_launch", "mission:progress"}
}

func (e *MissionExecutor) Execute(ctx context.Context, operation MissionOperation) {
	e.operationMu.Lock()
	defer e.operationMu.Unlock()
	e.logger.Info("mission operation received", "operation_id", operation.OperationID, "mission_run_id", operation.RunID, "operation", operation.Type)
	if operation.Type == "upload" {
		e.mu.Lock()
		e.uploadedRunID = operation.RunID
		e.activeRunID = ""
		e.state = "UPLOADING"
		e.mu.Unlock()
	}
	e.emit(ctx, operation, "operation_accepted", e.currentState(operation.Type), nil, nil, nil, "", "Mission operation accepted", "")
	timeout := 25 * time.Second
	if operation.Type == "upload" {
		timeout = 120 * time.Second
	}
	operationContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var err error
	switch operation.Type {
	case "upload":
		err = e.upload(operationContext, operation)
	case "start":
		err = e.start(operationContext, ctx, operation, false)
	case "pause":
		err = e.pause(operationContext, operation)
	case "resume":
		err = e.start(operationContext, ctx, operation, true)
	case "cancel":
		err = e.cancel(operationContext, operation)
	case "return_to_launch":
		err = e.returnToLaunch(operationContext, operation)
	default:
		err = fmt.Errorf("unsupported mission operation %q", operation.Type)
	}
	if err != nil {
		state := e.currentState(operation.Type)
		if operation.Type == "upload" || operation.Type == "start" {
			state = "FAILED"
			e.setState(operation.RunID, state)
		}
		e.emit(ctx, operation, "operation_failed", state, nil, nil, nil, missionErrorCode(err), err.Error(), "")
		e.logger.Error("mission operation failed", "operation_id", operation.OperationID, "mission_run_id", operation.RunID, "operation", operation.Type, "state", state, "error", err)
	} else {
		e.logger.Info("mission operation completed", "operation_id", operation.OperationID, "mission_run_id", operation.RunID, "operation", operation.Type, "state", e.currentState(operation.Type))
	}
}

func (e *MissionExecutor) upload(ctx context.Context, operation MissionOperation) error {
	translated, err := translateMissionPlan(operation.MissionPlanJSON)
	if err != nil {
		return fmt.Errorf("translate Atlas mission plan: %w", err)
	}
	e.stopWatcher()
	if e.payload != nil {
		e.payload.ConfigureMission(operation.RunID, translated.payload)
	}
	response, err := e.mission.SetReturnToLaunchAfterMission(ctx, &missionpb.SetReturnToLaunchAfterMissionRequest{Enable: translated.returnToLaunch})
	if err != nil {
		return fmt.Errorf("configure mission RTL: %w", err)
	}
	if err := missionResultError(response.GetMissionResult()); err != nil {
		return fmt.Errorf("configure mission RTL: %w", err)
	}
	stream, err := e.mission.SubscribeUploadMissionWithProgress(ctx, &missionpb.SubscribeUploadMissionWithProgressRequest{MissionPlan: translated.plan})
	if err != nil {
		return fmt.Errorf("begin MAVSDK mission upload: %w", err)
	}
	completed := false
	for {
		response, receiveErr := stream.Recv()
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			return fmt.Errorf("receive MAVSDK mission upload progress: %w", receiveErr)
		}
		if progress := response.GetProgressData(); progress != nil && !float32IsNaN(progress.GetProgress()) {
			value := math.Max(0, math.Min(100, float64(progress.GetProgress())*100))
			e.emit(ctx, operation, "upload_progress", "UPLOADING", &value, nil, uint32Pointer(uint32(len(translated.plan.GetMissionItems()))), "", "Uploading mission to flight controller", "")
		}
		result := response.GetMissionResult()
		if result == nil || result.GetResult() == missionpb.MissionResult_RESULT_NEXT {
			continue
		}
		if err := missionResultError(result); err != nil {
			return fmt.Errorf("upload mission: %w", err)
		}
		completed = true
		break
	}
	if !completed {
		return errors.New("MAVSDK mission upload ended without a completion result")
	}
	e.mu.Lock()
	e.uploadedRunID = operation.RunID
	e.activeRunID = ""
	e.state = "READY"
	e.mu.Unlock()
	evidence, _ := json.Marshal(map[string]any{"translationWarnings": translated.warnings})
	progress := 100.0
	e.emit(ctx, operation, "uploaded", "READY", &progress, nil, uint32Pointer(uint32(len(translated.plan.GetMissionItems()))), "", "Mission uploaded and verified by MAVSDK", string(evidence))
	return nil
}

func (e *MissionExecutor) start(ctx, watcherContext context.Context, operation MissionOperation, resume bool) error {
	e.mu.Lock()
	valid := e.uploadedRunID == operation.RunID && (!resume || e.state == "PAUSED")
	e.mu.Unlock()
	if !valid {
		if resume {
			return errors.New("mission is not paused and ready to resume")
		}
		return errors.New("mission has not been uploaded for this run")
	}
	if !resume {
		e.emit(ctx, operation, "arming", "READY", nil, nil, nil, "", "Running PX4 preflight checks and arming aircraft", "")
		armResponse, err := e.action.Arm(ctx, &actionpb.ArmRequest{})
		if err != nil {
			return fmt.Errorf("arm aircraft before mission start: %w", err)
		}
		if err := actionResultError(armResponse.GetActionResult()); err != nil {
			return fmt.Errorf("arm aircraft before mission start: %w", err)
		}
		e.emit(ctx, operation, "armed", "READY", nil, nil, nil, "", "Aircraft armed; requesting PX4 mission mode", "")
		e.logger.Info("aircraft armed for mission", "operation_id", operation.OperationID, "mission_run_id", operation.RunID)
	}
	response, err := e.mission.StartMission(ctx, &missionpb.StartMissionRequest{})
	if err != nil {
		if !resume {
			e.holdAfterFailedStart(ctx, operation)
		}
		return fmt.Errorf("start MAVSDK mission: %w", err)
	}
	if err := missionResultError(response.GetMissionResult()); err != nil {
		if !resume {
			e.holdAfterFailedStart(ctx, operation)
		}
		return fmt.Errorf("start mission: %w", err)
	}
	e.mu.Lock()
	e.activeRunID = operation.RunID
	e.state = "RUNNING"
	e.mu.Unlock()
	if e.payload != nil {
		if payloadErr := e.payload.ActivateMission(ctx, operation.RunID, "RUNNING"); payloadErr != nil {
			e.emitPayloadEvent(PayloadEvent{RunID: operation.RunID, Type: "payload_restore_failed", State: "RUNNING", ErrorCode: "MISSION_PAYLOAD_ACTIVATION_FAILED", Message: payloadErr.Error()})
		}
	}
	updateType := "started"
	message := "Mission started"
	if resume {
		updateType = "resumed"
		message = "Mission resumed"
	}
	e.emit(ctx, operation, updateType, "RUNNING", nil, nil, nil, "", message, "")
	e.startWatcher(watcherContext, operation.RunID)
	return nil
}

func (e *MissionExecutor) holdAfterFailedStart(ctx context.Context, operation MissionOperation) {
	response, err := e.action.Hold(ctx, &actionpb.HoldRequest{})
	if err != nil {
		e.logger.Error("mission start failed and hold request failed", "operation_id", operation.OperationID, "mission_run_id", operation.RunID, "error", err)
		return
	}
	if resultErr := actionResultError(response.GetActionResult()); resultErr != nil {
		e.logger.Error("mission start failed and PX4 rejected hold", "operation_id", operation.OperationID, "mission_run_id", operation.RunID, "error", resultErr)
		return
	}
	e.logger.Warn("mission start failed after arming; aircraft placed in hold", "operation_id", operation.OperationID, "mission_run_id", operation.RunID)
}

func (e *MissionExecutor) pause(ctx context.Context, operation MissionOperation) error {
	if !e.runIs(operation.RunID, "RUNNING") {
		return errors.New("mission is not running")
	}
	response, err := e.mission.PauseMission(ctx, &missionpb.PauseMissionRequest{})
	if err != nil {
		return fmt.Errorf("pause MAVSDK mission: %w", err)
	}
	if err := missionResultError(response.GetMissionResult()); err != nil {
		return fmt.Errorf("pause mission: %w", err)
	}
	e.setState(operation.RunID, "PAUSED")
	if e.payload != nil {
		e.payload.SetMissionState(operation.RunID, "PAUSED")
	}
	e.emit(ctx, operation, "paused", "PAUSED", nil, nil, nil, "", "Mission paused; aircraft holding", "")
	return nil
}

func (e *MissionExecutor) cancel(ctx context.Context, operation MissionOperation) error {
	if !e.runMatches(operation.RunID) {
		return errors.New("mission run is not loaded on this vehicle")
	}
	if e.currentState(operation.Type) == "RUNNING" {
		response, err := e.mission.PauseMission(ctx, &missionpb.PauseMissionRequest{})
		if err != nil {
			return fmt.Errorf("hold before mission cancellation: %w", err)
		}
		if err := missionResultError(response.GetMissionResult()); err != nil {
			return fmt.Errorf("hold before mission cancellation: %w", err)
		}
	}
	response, err := e.mission.ClearMission(ctx, &missionpb.ClearMissionRequest{})
	if err != nil {
		return fmt.Errorf("clear MAVSDK mission: %w", err)
	}
	if err := missionResultError(response.GetMissionResult()); err != nil {
		return fmt.Errorf("clear mission: %w", err)
	}
	e.stopWatcher()
	e.setState(operation.RunID, "CANCELLED")
	if e.payload != nil {
		e.payload.EndMission(ctx, operation.RunID)
	}
	e.emit(ctx, operation, "cancelled", "CANCELLED", nil, nil, nil, "", "Mission cancelled and cleared; aircraft remains in hold", "")
	return nil
}

func (e *MissionExecutor) returnToLaunch(ctx context.Context, operation MissionOperation) error {
	if !e.runMatches(operation.RunID) {
		return errors.New("mission run is not active on this vehicle")
	}
	response, err := e.action.ReturnToLaunch(ctx, &actionpb.ReturnToLaunchRequest{})
	if err != nil {
		return fmt.Errorf("request return to launch: %w", err)
	}
	if err := actionResultError(response.GetActionResult()); err != nil {
		return fmt.Errorf("return to launch: %w", err)
	}
	e.stopWatcher()
	e.setState(operation.RunID, "RTL")
	if e.payload != nil {
		e.payload.EndMission(ctx, operation.RunID)
	}
	e.emit(ctx, operation, "rtl_started", "RTL", nil, nil, nil, "", "Return to launch accepted; mission execution ended", "")
	return nil
}

func (e *MissionExecutor) startWatcher(ctx context.Context, runID string) {
	e.mu.Lock()
	if e.watchCancel != nil {
		e.mu.Unlock()
		return
	}
	watchContext, cancel := context.WithCancel(ctx)
	e.watchCancel = cancel
	e.mu.Unlock()
	go func() {
		stream, err := e.mission.SubscribeMissionProgress(watchContext, &missionpb.SubscribeMissionProgressRequest{})
		if err != nil {
			e.emitForRun(watchContext, runID, "operation_failed", e.currentState("progress"), nil, nil, nil, "PROGRESS_SUBSCRIPTION_FAILED", err.Error())
			return
		}
		for {
			response, receiveErr := stream.Recv()
			if receiveErr != nil {
				if watchContext.Err() == nil {
					code := "PROGRESS_STREAM_FAILED"
					message := receiveErr.Error()
					if errors.Is(receiveErr, io.EOF) {
						code = "PROGRESS_STREAM_ENDED"
						message = "MAVSDK mission progress stream ended before completion"
					}
					e.emitForRun(watchContext, runID, "operation_failed", e.currentState("progress"), nil, nil, nil, code, message)
				}
				return
			}
			progress := response.GetMissionProgress()
			if progress == nil || progress.GetCurrent() < 0 || progress.GetTotal() < 0 {
				continue
			}
			current := uint32(progress.GetCurrent())
			total := uint32(progress.GetTotal())
			percent := 0.0
			if total > 0 {
				percent = math.Min(100, float64(current)/float64(total)*100)
			}
			if current >= total && total > 0 {
				e.setState(runID, "COMPLETED")
				if e.payload != nil {
					e.payload.EndMission(watchContext, runID)
				}
				e.emitForRun(watchContext, runID, "completed", "COMPLETED", &percent, &current, &total, "", "Mission completed")
				e.stopWatcherFromWatcher()
				return
			}
			if e.payload != nil {
				e.payload.MissionProgress(watchContext, runID, current)
			}
			e.emitForRun(watchContext, runID, "progress", e.currentState("progress"), &percent, &current, &total, "", "Mission progress updated")
		}
	}()
}

func (e *MissionExecutor) stopWatcher() {
	e.mu.Lock()
	if e.watchCancel != nil {
		e.watchCancel()
		e.watchCancel = nil
	}
	e.mu.Unlock()
}

func (e *MissionExecutor) stopWatcherFromWatcher() {
	e.mu.Lock()
	e.watchCancel = nil
	e.mu.Unlock()
}

func (e *MissionExecutor) runMatches(runID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.uploadedRunID == runID
}

func (e *MissionExecutor) runIs(runID, state string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.uploadedRunID == runID && e.state == state
}

func (e *MissionExecutor) setState(runID, state string) {
	e.mu.Lock()
	if e.uploadedRunID == "" {
		e.uploadedRunID = runID
	}
	e.activeRunID = runID
	e.state = state
	e.mu.Unlock()
}

func (e *MissionExecutor) currentState(operation string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state != "" {
		return e.state
	}
	if operation == "upload" {
		return "UPLOADING"
	}
	return "READY"
}

func (e *MissionExecutor) emit(ctx context.Context, operation MissionOperation, updateType, state string, progress *float64, current, total *uint32, errorCode, message, evidence string) {
	e.emitUpdate(ctx, MissionUpdate{EventID: identity.NewID(), OperationID: operation.OperationID, RunID: operation.RunID, Type: updateType, State: state, ObservedAt: time.Now().UTC(), Progress: progress, CurrentWaypoint: current, TotalWaypoints: total, ErrorCode: errorCode, Message: message, EvidenceJSON: evidence})
}

func (e *MissionExecutor) emitForRun(ctx context.Context, runID, updateType, state string, progress *float64, current, total *uint32, errorCode, message string) {
	e.emitUpdate(ctx, MissionUpdate{EventID: identity.NewID(), RunID: runID, Type: updateType, State: state, ObservedAt: time.Now().UTC(), Progress: progress, CurrentWaypoint: current, TotalWaypoints: total, ErrorCode: errorCode, Message: message})
}

func (e *MissionExecutor) emitUpdate(ctx context.Context, update MissionUpdate) {
	if update.Type != "progress" && update.Type != "upload_progress" {
		e.logger.Info("mission lifecycle update", "mission_run_id", update.RunID, "operation_id", update.OperationID, "event", update.Type, "state", update.State, "error_code", update.ErrorCode, "message", update.Message)
	}
	select {
	case e.updates <- update:
	case <-ctx.Done():
	}
}

func (e *MissionExecutor) emitPayloadEvent(event PayloadEvent) {
	if event.RunID == "" || event.State == "" {
		return
	}
	e.emitUpdate(context.Background(), MissionUpdate{
		EventID:         identity.NewID(),
		RunID:           event.RunID,
		Type:            event.Type,
		State:           event.State,
		ObservedAt:      time.Now().UTC(),
		CurrentWaypoint: uint32Pointer(event.CurrentWaypoint),
		ErrorCode:       event.ErrorCode,
		Message:         event.Message,
	})
}

type atlasMissionPlan struct {
	GeneratedWaypoints []atlasWaypoint `json:"generatedWaypoints"`
	Actions            []atlasAction   `json:"actions"`
}

type atlasWaypoint struct {
	Sequence       uint32   `json:"sequence"`
	Latitude       float64  `json:"latitude"`
	Longitude      float64  `json:"longitude"`
	AltitudeMeters float64  `json:"altitudeMeters"`
	SpeedMPS       *float64 `json:"speedMps"`
	HeadingDegrees *float64 `json:"headingDegrees"`
	HoldSeconds    *float64 `json:"holdSeconds"`
}

type atlasAction struct {
	Type   string         `json:"actionType"`
	Params map[string]any `json:"params"`
}

type translatedMission struct {
	plan           *missionpb.MissionPlan
	payload        payloadMissionPlan
	returnToLaunch bool
	warnings       []string
}

type gimbalIntent struct {
	pitch   float32
	yaw     float32
	yawMode string
	target  *geoPoint
}

type geoPoint struct{ latitude, longitude float64 }

func translateMissionPlan(value string) (translatedMission, error) {
	var source atlasMissionPlan
	if err := json.Unmarshal([]byte(value), &source); err != nil {
		return translatedMission{}, err
	}
	if len(source.GeneratedWaypoints) == 0 {
		return translatedMission{}, errors.New("mission plan contains no waypoints")
	}
	globalSpeed := float32(math.NaN())
	globalGimbal := gimbalIntent{pitch: float32(math.NaN()), yaw: 0, yawMode: "FOLLOW_DRONE_HEADING"}
	waypointGimbals := map[uint32]gimbalIntent{}
	startRecording := false
	stopRecording := false
	land := false
	returnToLaunch := false
	warnings := []string{}
	for _, action := range source.Actions {
		switch action.Type {
		case "SET_SPEED":
			if value, ok := numberParam(action.Params, "speedMps"); ok {
				globalSpeed = float32(value)
			}
		case "SET_GIMBAL_ORIENTATION":
			intent := parseGimbalIntent(action.Params)
			if sequence, ok := numberParam(action.Params, "waypointSequence"); ok {
				waypointGimbals[uint32(sequence)] = intent
			} else {
				globalGimbal = intent
			}
		case "START_RECORDING":
			startRecording = true
		case "STOP_RECORDING":
			stopRecording = true
		case "RETURN_TO_LAUNCH":
			returnToLaunch = true
		case "LAND":
			land = true
		case "START_PERCEPTION", "STOP_PERCEPTION":
			warnings = appendUnique(warnings, "Perception actions remain onboard-agent structured events and are not executed by MAVSDK Mission v1")
		}
	}
	items := make([]*missionpb.MissionItem, 0, len(source.GeneratedWaypoints))
	for index, waypoint := range source.GeneratedWaypoints {
		speed := globalSpeed
		if waypoint.SpeedMPS != nil {
			speed = float32(*waypoint.SpeedMPS)
		}
		intent := globalGimbal
		if override, exists := waypointGimbals[waypoint.Sequence]; exists {
			intent = override
		}
		yaw := float32(math.NaN())
		if waypoint.HeadingDegrees != nil {
			yaw = float32(*waypoint.HeadingDegrees)
		} else if intent.yawMode == "LOOK_AT_POINT" && intent.target != nil {
			yaw = float32(initialBearing(waypoint.Latitude, waypoint.Longitude, intent.target.latitude, intent.target.longitude))
		} else if index+1 < len(source.GeneratedWaypoints) && intent.yawMode != "FIXED_ANGLE" {
			next := source.GeneratedWaypoints[index+1]
			yaw = float32(initialBearing(waypoint.Latitude, waypoint.Longitude, next.Latitude, next.Longitude))
		}
		loiter := float32(math.NaN())
		if waypoint.HoldSeconds != nil {
			loiter = float32(*waypoint.HoldSeconds)
		}
		item := &missionpb.MissionItem{
			LatitudeDeg:          waypoint.Latitude,
			LongitudeDeg:         waypoint.Longitude,
			RelativeAltitudeM:    float32(waypoint.AltitudeMeters),
			SpeedMS:              speed,
			IsFlyThrough:         waypoint.HoldSeconds == nil || *waypoint.HoldSeconds <= 0,
			GimbalPitchDeg:       float32(math.NaN()),
			GimbalYawDeg:         float32(math.NaN()),
			LoiterTimeS:          loiter,
			AcceptanceRadiusM:    float32(math.NaN()),
			YawDeg:               yaw,
			CameraPhotoIntervalS: 1,
		}
		items = append(items, item)
	}
	if startRecording {
		items[0].CameraAction = missionpb.MissionItem_CAMERA_ACTION_START_VIDEO
	}
	if stopRecording && len(items) > 1 {
		items[len(items)-1].CameraAction = missionpb.MissionItem_CAMERA_ACTION_STOP_VIDEO
	} else if stopRecording {
		warnings = appendUnique(warnings, "A one-waypoint MAVSDK mission cannot encode both start and stop video actions")
	}
	if land {
		items[len(items)-1].VehicleAction = missionpb.MissionItem_VEHICLE_ACTION_LAND
	}
	return translatedMission{plan: &missionpb.MissionPlan{MissionItems: items}, payload: missionPayloadPlan(source.Actions), returnToLaunch: returnToLaunch, warnings: warnings}, nil
}

func parseGimbalIntent(params map[string]any) gimbalIntent {
	intent := gimbalIntent{pitch: float32(math.NaN()), yaw: 0, yawMode: stringParam(params, "yawMode")}
	if value, ok := numberParam(params, "pitchDegrees"); ok {
		intent.pitch = float32(value)
	}
	if value, ok := numberParam(params, "yawDegrees"); ok && intent.yawMode == "FIXED_ANGLE" {
		intent.yaw = float32(value)
	}
	if target, ok := params["target"].(map[string]any); ok {
		latitude, latitudeOK := numberParam(target, "latitude")
		longitude, longitudeOK := numberParam(target, "longitude")
		if latitudeOK && longitudeOK {
			intent.target = &geoPoint{latitude: latitude, longitude: longitude}
		}
	}
	return intent
}

func numberParam(params map[string]any, key string) (float64, bool) {
	value, ok := params[key].(float64)
	return value, ok
}

func stringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return value
}

func initialBearing(latitude, longitude, targetLatitude, targetLongitude float64) float64 {
	lat1, lat2 := latitude*math.Pi/180, targetLatitude*math.Pi/180
	deltaLongitude := (targetLongitude - longitude) * math.Pi / 180
	y := math.Sin(deltaLongitude) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(deltaLongitude)
	return math.Mod(math.Atan2(y, x)*180/math.Pi+360, 360)
}

func missionResultError(result *missionpb.MissionResult) error {
	if result == nil {
		return errors.New("MAVSDK response did not include a mission result")
	}
	if result.GetResult() == missionpb.MissionResult_RESULT_SUCCESS {
		return nil
	}
	message := result.GetResultStr()
	if message == "" {
		message = result.GetResult().String()
	}
	return fmt.Errorf("%s: %s", result.GetResult().String(), message)
}

func actionResultError(result *actionpb.ActionResult) error {
	if result == nil {
		return errors.New("MAVSDK response did not include an action result")
	}
	if result.GetResult() == actionpb.ActionResult_RESULT_SUCCESS {
		return nil
	}
	message := result.GetResultStr()
	if message == "" {
		message = result.GetResult().String()
	}
	return fmt.Errorf("%s: %s", result.GetResult().String(), message)
}

func missionErrorCode(err error) string {
	if err == nil {
		return ""
	}
	return "MISSION_OPERATION_FAILED"
}

func float32IsNaN(value float32) bool { return math.IsNaN(float64(value)) }

func uint32Pointer(value uint32) *uint32 { return &value }

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}
