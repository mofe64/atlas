package vehicle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/identity"
	actionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/action"
	missionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/mission"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type MissionOperation struct {
	OperationID     string
	RunID           string
	Type            string
	MissionPlanJSON string
}

type MissionReconciliation struct {
	ReconciliationID string
	RunID            string
	State            string
	MissionPlanJSON  string
	CurrentWaypoint  *uint32
	TotalWaypoints   uint32
	Actions          []MissionActionCheckpoint
}

type MissionActionCheckpoint struct {
	Sequence          uint32
	ActionType        string
	State             string
	Attempt           uint32
	AttemptDeadlineAt *time.Time
	NextAttemptAt     *time.Time
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
	ActionSequence  *uint32
	ActionType      string
	ActionState     string
	ActionAttempt   uint32
	FailurePolicy   string
}

type MissionExecutor struct {
	connection             *grpc.ClientConn
	logger                 *slog.Logger
	mission                missionpb.MissionServiceClient
	action                 actionpb.ActionServiceClient
	payload                *PayloadController
	ownsPayload            bool
	updates                chan MissionUpdate
	operationMu            sync.Mutex
	mu                     sync.Mutex
	uploadedRunID          string
	activeRunID            string
	state                  string
	watchCancel            context.CancelFunc
	arrivalActions         []runtimeMissionAction
	arrivalHandled         bool
	perceptionControl      perception.Control
	startPerception        *runtimeMissionAction
	stopPerception         *runtimeMissionAction
	startPerceptionHandled bool
	stopPerceptionHandled  bool
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

func (e *MissionExecutor) SetPerceptionControl(control perception.Control) {
	e.mu.Lock()
	e.perceptionControl = control
	e.mu.Unlock()
}

func (e *MissionExecutor) Capabilities() []string {
	return []string{"mission:upload", "mission:start", "mission:auto_arm", "mission:pause", "mission:resume", "mission:cancel", "mission:return_to_launch", "mission:progress", "mission:reconciliation:v1", "mission:actions:v1", "mission:action:start_perception", "mission:action:stop_perception", "mission:action:hold_at_arrival", "mission:action:point_gimbal_at_incident", "mission:action:resume_after_arrival"}
}

func (e *MissionExecutor) Reconcile(ctx context.Context, reconciliation MissionReconciliation) {
	e.operationMu.Lock()
	defer e.operationMu.Unlock()
	failed := func(state, code string, err error) {
		e.emitUpdate(ctx, MissionUpdate{
			EventID:      identity.NewID(),
			OperationID:  reconciliation.ReconciliationID,
			RunID:        reconciliation.RunID,
			Type:         "reconciliation_failed",
			State:        state,
			ObservedAt:   time.Now().UTC(),
			ErrorCode:    code,
			Message:      err.Error(),
			EvidenceJSON: `{"operatorInterventionRequired":true}`,
		})
	}
	if reconciliation.RunID == "" || !matchesRunState(reconciliation.State) {
		failed(reconciliation.State, "RECONCILIATION_INVALID", errors.New("Native supplied an invalid unfinished mission checkpoint"))
		return
	}
	translated, err := translateMissionPlan(reconciliation.MissionPlanJSON)
	if err != nil {
		failed(reconciliation.State, "RECONCILIATION_PLAN_INVALID", fmt.Errorf("decode immutable mission plan: %w", err))
		return
	}
	operationContext, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	downloaded, err := e.mission.DownloadMission(operationContext, &missionpb.DownloadMissionRequest{})
	if err != nil {
		failed(reconciliation.State, "RECONCILIATION_DOWNLOAD_FAILED", fmt.Errorf("query mission loaded on PX4: %w", err))
		return
	}
	if err := missionResultError(downloaded.GetMissionResult()); err != nil {
		failed(reconciliation.State, "RECONCILIATION_DOWNLOAD_REJECTED", fmt.Errorf("query mission loaded on PX4: %w", err))
		return
	}
	if !missionPlansEquivalent(translated.plan, downloaded.GetMissionPlan()) {
		failed(reconciliationFailureState(reconciliation.State), "RECONCILIATION_PLAN_MISMATCH", errors.New("PX4 loaded mission does not match the immutable Native plan; automatic execution was not resumed"))
		return
	}
	actions, err := actionsFromCheckpoints(translated.runtimeActions(), reconciliation.Actions)
	if err != nil {
		failed(reconciliation.State, "RECONCILIATION_ACTION_INVALID", err)
		return
	}
	startPerception, stopPerception, arrivalActions := splitRuntimeActions(actions)

	e.mu.Lock()
	if e.uploadedRunID != "" && e.uploadedRunID != reconciliation.RunID && e.watchCancel != nil {
		e.mu.Unlock()
		failed(reconciliation.State, "RECONCILIATION_RUN_CONFLICT", errors.New("Agent is already watching a different mission run"))
		return
	}
	alreadyWatching := e.uploadedRunID == reconciliation.RunID && e.watchCancel != nil
	reconciledState := reconciliation.State
	if reconciledState == "UPLOADING" {
		reconciledState = "READY"
	}
	if !alreadyWatching {
		e.uploadedRunID = reconciliation.RunID
		e.activeRunID = ""
		if reconciledState == "RUNNING" || reconciledState == "PAUSED" {
			e.activeRunID = reconciliation.RunID
		}
		e.state = reconciledState
		e.startPerception = startPerception
		e.stopPerception = stopPerception
		e.startPerceptionHandled = startPerception == nil || startPerception.durableState == "SUCCEEDED"
		e.stopPerceptionHandled = stopPerception == nil || stopPerception.durableState == "SUCCEEDED" || stopPerception.durableState == "POLICY_APPLIED"
		e.arrivalActions = arrivalActions
		e.arrivalHandled = arrivalActionChainComplete(arrivalActions)
	} else {
		reconciledState = e.state
	}
	e.mu.Unlock()

	if e.payload != nil && !alreadyWatching {
		e.payload.ConfigureMission(reconciliation.RunID, translated.payload)
		if reconciledState == "RUNNING" || reconciledState == "PAUSED" {
			if payloadErr := e.payload.ActivateMission(operationContext, reconciliation.RunID, reconciledState); payloadErr != nil {
				e.emitPayloadEvent(PayloadEvent{RunID: reconciliation.RunID, Type: "payload_restore_failed", State: reconciledState, ErrorCode: "MISSION_PAYLOAD_RECONCILIATION_FAILED", Message: payloadErr.Error()})
			}
		}
	}
	if !alreadyWatching && (reconciledState == "RUNNING" || reconciledState == "PAUSED") && startPerception != nil && startPerception.durableState == "SUCCEEDED" {
		e.mu.Lock()
		control := e.perceptionControl
		e.mu.Unlock()
		if control == nil {
			failed(reconciledState, "RECONCILIATION_PERCEPTION_UNAVAILABLE", errors.New("mission perception was active before restart but runtime control is unavailable"))
			return
		}
		if _, activationErr := control.Acquire(operationContext, perception.Claim{ID: missionPerceptionClaimID(reconciliation.RunID), Owner: "mission", SourceID: startPerception.sourceID, DetectionClasses: startPerception.detectionClasses}); activationErr != nil {
			failed(reconciledState, "RECONCILIATION_PERCEPTION_FAILED", fmt.Errorf("restore mission perception: %w", activationErr))
			return
		}
	}
	evidence, _ := json.Marshal(map[string]any{
		"onboardMissionVerified": true,
		"waypointCount":          len(translated.plan.GetMissionItems()),
		"alreadyWatching":        alreadyWatching,
	})
	e.emitUpdate(ctx, MissionUpdate{
		EventID:         identity.NewID(),
		OperationID:     reconciliation.ReconciliationID,
		RunID:           reconciliation.RunID,
		Type:            "reconciliation_accepted",
		State:           reconciledState,
		ObservedAt:      time.Now().UTC(),
		CurrentWaypoint: reconciliation.CurrentWaypoint,
		TotalWaypoints:  uint32Pointer(reconciliation.TotalWaypoints),
		Message:         "PX4 onboard mission matched the immutable plan; Agent execution state reconciled",
		EvidenceJSON:    string(evidence),
	})
	if !alreadyWatching && reconciledState == "RUNNING" {
		e.startWatcher(ctx, reconciliation.RunID)
	}
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
	} else if operation.Type == "start" {
		timeout = 90 * time.Second
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
	e.mu.Lock()
	e.arrivalActions = append([]runtimeMissionAction(nil), translated.arrivalActions...)
	e.arrivalHandled = false
	e.startPerception = cloneRuntimeAction(translated.startPerception)
	e.stopPerception = cloneRuntimeAction(translated.stopPerception)
	e.startPerceptionHandled = translated.startPerception == nil
	e.stopPerceptionHandled = translated.stopPerception == nil
	e.mu.Unlock()
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
		if err := e.activateMissionPerception(ctx, operation.RunID); err != nil {
			return fmt.Errorf("start required mission perception before arming: %w", err)
		}
		e.emit(ctx, operation, "arming", "READY", nil, nil, nil, "", "Running PX4 preflight checks and arming aircraft", "")
		armResponse, err := e.action.Arm(ctx, &actionpb.ArmRequest{})
		if err != nil {
			e.releaseMissionPerceptionForCleanup(ctx, operation.RunID)
			return fmt.Errorf("arm aircraft before mission start: %w", err)
		}
		if err := actionResultError(armResponse.GetActionResult()); err != nil {
			e.releaseMissionPerceptionForCleanup(ctx, operation.RunID)
			return fmt.Errorf("arm aircraft before mission start: %w", err)
		}
		e.emit(ctx, operation, "armed", "READY", nil, nil, nil, "", "Aircraft armed; requesting PX4 mission mode", "")
		e.logger.Info("aircraft armed for mission", "operation_id", operation.OperationID, "mission_run_id", operation.RunID)
	}
	response, err := e.mission.StartMission(ctx, &missionpb.StartMissionRequest{})
	if err != nil {
		if !resume {
			e.holdAfterFailedStart(ctx, operation)
			e.releaseMissionPerceptionForCleanup(ctx, operation.RunID)
		}
		return fmt.Errorf("start MAVSDK mission: %w", err)
	}
	if err := missionResultError(response.GetMissionResult()); err != nil {
		if !resume {
			e.holdAfterFailedStart(ctx, operation)
			e.releaseMissionPerceptionForCleanup(ctx, operation.RunID)
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

func (e *MissionExecutor) activateMissionPerception(ctx context.Context, runID string) error {
	e.mu.Lock()
	action := cloneRuntimeAction(e.startPerception)
	handled := e.startPerceptionHandled
	e.mu.Unlock()
	if action == nil || handled {
		return nil
	}
	outcome := e.executeArrivalActions(ctx, runID, []runtimeMissionAction{*action})
	if !outcome.completed {
		if outcome.message == "" {
			outcome.message = "required mission perception was not acknowledged"
		}
		return errors.New(outcome.message)
	}
	e.mu.Lock()
	if e.uploadedRunID == runID {
		e.startPerceptionHandled = true
	}
	e.mu.Unlock()
	return nil
}

// stopMissionPerception executes the reviewed terminal action when present,
// then always removes the mission claim. A failed inference shutdown must be
// visible, but it must never prevent cancellation, RTL, or run completion.
func (e *MissionExecutor) stopMissionPerception(ctx context.Context, runID string) {
	e.mu.Lock()
	action := cloneRuntimeAction(e.stopPerception)
	handled := e.stopPerceptionHandled
	e.mu.Unlock()
	if action != nil && !handled {
		outcome := e.executeArrivalActions(ctx, runID, []runtimeMissionAction{*action})
		if outcome.completed {
			e.mu.Lock()
			if e.uploadedRunID == runID {
				e.stopPerceptionHandled = true
			}
			e.mu.Unlock()
		}
	}
	e.releaseMissionPerceptionForCleanup(ctx, runID)
}

func (e *MissionExecutor) releaseMissionPerceptionForCleanup(ctx context.Context, runID string) {
	e.mu.Lock()
	control := e.perceptionControl
	e.mu.Unlock()
	if control == nil {
		return
	}
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := control.Release(cleanupContext, missionPerceptionClaimID(runID)); err != nil {
		e.logger.Warn("release mission perception claim during cleanup", "mission_run_id", runID, "error", err)
	}
}

func missionPerceptionClaimID(runID string) string { return "mission:" + runID }

func cloneRuntimeAction(action *runtimeMissionAction) *runtimeMissionAction {
	if action == nil {
		return nil
	}
	cloned := *action
	cloned.detectionClasses = append([]string(nil), action.detectionClasses...)
	return &cloned
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
	e.stopMissionPerception(ctx, operation.RunID)
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
	e.stopMissionPerception(ctx, operation.RunID)
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
			if watchContext.Err() == nil {
				e.emitForRun(watchContext, runID, "operation_failed", e.currentState("progress"), nil, nil, nil, "PROGRESS_SUBSCRIPTION_FAILED", err.Error())
			}
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
			actions, arrivalHandled := e.arrivalActionStateForRun(runID)
			if len(actions) > 0 && !arrivalHandled && arrivalActionsDue(actions, current, total) {
				message := "Arrival waypoint reached; awaiting acknowledged arrival actions"
				if current >= total {
					message = "Final waypoint reached; awaiting acknowledged arrival actions"
				}
				e.emitForRun(watchContext, runID, "progress", "RUNNING", &percent, &current, &total, "", message)
				outcome := e.executeArrivalActions(watchContext, runID, actions)
				switch {
				case outcome.completed:
					e.markArrivalActionsHandled(runID)
					if outcome.waitForOperatorDecision {
						e.pauseForOperatorDecision(watchContext, runID, percent, current, total)
						return
					}
					if current >= total {
						e.completeRun(watchContext, runID, percent, current, total, "Arrival Hold acknowledged; mission actions completed")
						return
					}
					if e.payload != nil {
						e.payload.MissionProgress(watchContext, runID, current)
					}
					e.emitForRun(watchContext, runID, "progress", "RUNNING", &percent, &current, &total, "", "Arrival actions acknowledged; operational pattern resumed")
					continue
				case outcome.policyRunState == "RTL":
					e.setState(runID, "RTL")
					if e.payload != nil {
						e.payload.EndMission(watchContext, runID)
					}
					e.emitForRun(watchContext, runID, "rtl_started", "RTL", &percent, &current, &total, outcome.errorCode, outcome.message)
					e.stopWatcherFromWatcher()
				default:
					e.emitForRun(watchContext, runID, "operation_failed", "RUNNING", &percent, &current, &total, outcome.errorCode, outcome.message)
					e.stopWatcherFromWatcher()
				}
				return
			}
			if current >= total && total > 0 {
				e.completeRun(watchContext, runID, percent, current, total, "Mission completed")
				return
			}
			if e.payload != nil {
				e.payload.MissionProgress(watchContext, runID, current)
			}
			e.emitForRun(watchContext, runID, "progress", e.currentState("progress"), &percent, &current, &total, "", "Mission progress updated")
		}
	}()
}

func (e *MissionExecutor) arrivalActionStateForRun(runID string) ([]runtimeMissionAction, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.uploadedRunID != runID {
		return nil, false
	}
	return append([]runtimeMissionAction(nil), e.arrivalActions...), e.arrivalHandled
}

func (e *MissionExecutor) markArrivalActionsHandled(runID string) {
	e.mu.Lock()
	if e.uploadedRunID == runID {
		e.arrivalHandled = true
	}
	e.mu.Unlock()
}

func (e *MissionExecutor) completeRun(ctx context.Context, runID string, percent float64, current, total uint32, message string) {
	e.stopMissionPerception(ctx, runID)
	e.setState(runID, "COMPLETED")
	if e.payload != nil {
		e.payload.EndMission(ctx, runID)
	}
	e.emitForRun(ctx, runID, "completed", "COMPLETED", &percent, &current, &total, "", message)
	e.stopWatcherFromWatcher()
}

func (e *MissionExecutor) pauseForOperatorDecision(ctx context.Context, runID string, percent float64, current, total uint32) {
	e.setState(runID, "PAUSED")
	if e.payload != nil {
		e.payload.SetMissionState(runID, "PAUSED")
	}
	e.emitForRun(ctx, runID, "paused", "PAUSED", &percent, &current, &total, "", "Staging Hold acknowledged; awaiting an explicit operator decision")
	e.stopWatcherFromWatcher()
}

func (e *MissionExecutor) executeArrivalActions(ctx context.Context, runID string, actions []runtimeMissionAction) arrivalActionOutcome {
	for _, action := range actions {
		switch action.durableState {
		case "SUCCEEDED":
			continue
		case "POLICY_APPLIED":
			if action.failurePolicy == "SKIP_OPTIONAL_AND_NOTIFY" {
				continue
			}
			return outcomeForAppliedPolicy(action)
		case "FAILED":
			outcome := e.applyArrivalFailurePolicy(ctx, runID, action, errors.New("durable action checkpoint is failed"))
			if outcome.skipOptional {
				continue
			}
			return outcome
		case "RUNNING":
			if !waitUntil(ctx, action.attemptDeadlineAt) {
				return arrivalActionOutcome{errorCode: "ARRIVAL_ACTION_INTERRUPTED", message: "Arrival action reconciliation was interrupted"}
			}
			if action.attempt >= action.maxAttempts {
				e.emitActionUpdate(ctx, runID, action, "FAILED", action.attempt, runtimeActionErrorCode(action.actionType), "Arrival action deadline elapsed after Agent restart; no reviewed retry remains", "")
				outcome := e.applyArrivalFailurePolicy(ctx, runID, action, context.DeadlineExceeded)
				if outcome.skipOptional {
					continue
				}
				return outcome
			}
			e.emitActionUpdate(ctx, runID, action, "RETRYING", action.attempt, runtimeActionErrorCode(action.actionType), "Arrival action outcome was not durable before Agent restart; waiting for the reviewed retry", "")
			if !waitForRetry(ctx, retryDelayForAttempt(action, action.attempt)) {
				return arrivalActionOutcome{errorCode: "ARRIVAL_ACTION_INTERRUPTED", message: "Arrival action retry was interrupted"}
			}
			action.attempt++
		case "RETRYING":
			if !waitUntil(ctx, action.nextAttemptAt) {
				return arrivalActionOutcome{errorCode: "ARRIVAL_ACTION_INTERRUPTED", message: "Arrival action retry was interrupted"}
			}
			action.attempt++
		default:
			action.attempt = 1
		}
		if action.attempt == 0 {
			action.attempt = 1
		}
		var lastErr error
		for attempt := action.attempt; attempt <= action.maxAttempts; attempt++ {
			e.emitActionUpdate(ctx, runID, action, "RUNNING", attempt, "", fmt.Sprintf("Executing %s through the acknowledged Agent runtime", actionLabel(action.actionType)), "")
			actionContext, cancel := context.WithTimeout(ctx, action.timeout)
			evidence, executeErr := e.executeRuntimeAction(actionContext, runID, action)
			lastErr = executeErr
			cancel()
			if lastErr == nil {
				if evidence == "" {
					evidence = `{"acknowledged":true}`
				}
				e.emitActionUpdate(ctx, runID, action, "SUCCEEDED", attempt, "", fmt.Sprintf("%s acknowledged", actionLabel(action.actionType)), evidence)
				break
			}
			errorCode := runtimeActionErrorCode(action.actionType)
			if attempt < action.maxAttempts {
				e.emitActionUpdate(ctx, runID, action, "RETRYING", attempt, errorCode, fmt.Sprintf("%s was not acknowledged; retrying", actionLabel(action.actionType)), "")
				if !waitForRetry(ctx, retryDelayForAttempt(action, attempt)) {
					lastErr = ctx.Err()
					e.emitActionUpdate(ctx, runID, action, "FAILED", attempt, errorCode, "Arrival action retry was interrupted", "")
					break
				}
				continue
			}
			e.emitActionUpdate(ctx, runID, action, "FAILED", attempt, errorCode, lastErr.Error(), "")
		}
		if lastErr != nil {
			outcome := e.applyArrivalFailurePolicy(ctx, runID, action, lastErr)
			if outcome.skipOptional {
				continue
			}
			return outcome
		}
	}
	waitForOperatorDecision := len(actions) == 1 && actions[0].waitForOperatorDecision
	return arrivalActionOutcome{completed: true, waitForOperatorDecision: waitForOperatorDecision}
}

func (e *MissionExecutor) executeRuntimeAction(ctx context.Context, runID string, action runtimeMissionAction) (string, error) {
	switch action.actionType {
	case "START_PERCEPTION":
		e.mu.Lock()
		control := e.perceptionControl
		e.mu.Unlock()
		if control == nil {
			return "", errors.New("perception runtime control is unavailable")
		}
		evidence, err := control.Acquire(ctx, perception.Claim{
			ID:               missionPerceptionClaimID(runID),
			Owner:            "mission",
			SourceID:         action.sourceID,
			DetectionClasses: action.detectionClasses,
		})
		if err != nil {
			return "", err
		}
		encoded, _ := json.Marshal(evidence)
		return string(encoded), nil
	case "STOP_PERCEPTION":
		e.mu.Lock()
		control := e.perceptionControl
		e.mu.Unlock()
		if control == nil {
			return "", errors.New("perception runtime control is unavailable")
		}
		evidence, err := control.Release(ctx, missionPerceptionClaimID(runID))
		if err != nil {
			return "", err
		}
		encoded, _ := json.Marshal(evidence)
		return string(encoded), nil
	case "HOLD_AT_ARRIVAL":
		response, err := e.action.Hold(ctx, &actionpb.HoldRequest{})
		if err != nil {
			return "", fmt.Errorf("request operational Hold: %w", err)
		}
		if err := actionResultError(response.GetActionResult()); err != nil {
			return "", fmt.Errorf("operational Hold rejected: %w", err)
		}
		return "", nil
	case "POINT_GIMBAL_AT_INCIDENT":
		if e.payload == nil {
			return "", errors.New("payload controller is unavailable")
		}
		return "", e.payload.PointAtLocation(ctx, runID, action.latitude, action.longitude, action.altitudeAMSL)
	case "RESUME_AFTER_ARRIVAL":
		response, err := e.mission.StartMission(ctx, &missionpb.StartMissionRequest{})
		if err != nil {
			return "", fmt.Errorf("resume mission after arrival: %w", err)
		}
		if err := missionResultError(response.GetMissionResult()); err != nil {
			return "", fmt.Errorf("resume mission after arrival: %w", err)
		}
		return "", nil
	default:
		return "", fmt.Errorf("unsupported runtime mission action %q", action.actionType)
	}
}

func (e *MissionExecutor) applyArrivalFailurePolicy(ctx context.Context, runID string, action runtimeMissionAction, actionErr error) arrivalActionOutcome {
	errorCode := runtimeActionErrorCode(action.actionType)
	switch action.failurePolicy {
	case "RETURN_TO_LAUNCH":
		policyContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		response, err := e.action.ReturnToLaunch(policyContext, &actionpb.ReturnToLaunchRequest{})
		if err == nil {
			err = actionResultError(response.GetActionResult())
		}
		if err != nil {
			return arrivalActionOutcome{
				errorCode: "ARRIVAL_FAILURE_POLICY_FAILED",
				message:   fmt.Sprintf("%s failed and reviewed RTL policy was not acknowledged: %v; operator intervention required", actionLabel(action.actionType), err),
			}
		}
		e.emitActionUpdate(ctx, runID, action, "POLICY_APPLIED", action.maxAttempts, errorCode, "Arrival action failed; reviewed Return to launch policy acknowledged", `{"policy":"RETURN_TO_LAUNCH","acknowledged":true}`)
		return arrivalActionOutcome{
			policyRunState: "RTL",
			errorCode:      errorCode,
			message:        fmt.Sprintf("%s failed; reviewed Return to launch policy acknowledged", actionLabel(action.actionType)),
		}
	case "OPERATOR_INTERVENTION":
		e.emitActionUpdate(ctx, runID, action, "POLICY_APPLIED", action.maxAttempts, errorCode, "Arrival action failed; reviewed operator intervention policy is active", `{"policy":"OPERATOR_INTERVENTION","automaticVehicleCommand":false}`)
		return arrivalActionOutcome{
			errorCode: errorCode,
			message:   fmt.Sprintf("%s failed after %d attempts: %v; operator intervention required", actionLabel(action.actionType), action.maxAttempts, actionErr),
		}
	case "SKIP_OPTIONAL_AND_NOTIFY":
		if action.actionType != "POINT_GIMBAL_AT_INCIDENT" && action.actionType != "STOP_PERCEPTION" {
			return arrivalActionOutcome{
				errorCode: "ARRIVAL_FAILURE_POLICY_INVALID",
				message:   "Skip policy was rejected for a required arrival action; operator intervention required",
			}
		}
		message := "Optional arrival action failed; reviewed skip-and-notify policy applied"
		if action.actionType == "STOP_PERCEPTION" {
			message = "Perception stop was not acknowledged; reviewed notify-and-continue-safe-termination policy applied"
		}
		e.emitActionUpdate(ctx, runID, action, "POLICY_APPLIED", action.maxAttempts, errorCode, message, `{"policy":"SKIP_OPTIONAL_AND_NOTIFY","operatorNotified":true}`)
		return arrivalActionOutcome{skipOptional: true}
	default:
		return arrivalActionOutcome{
			errorCode: "ARRIVAL_FAILURE_POLICY_INVALID",
			message:   "Arrival action failed without a supported reviewed policy; operator intervention required",
		}
	}
}

func (e *MissionExecutor) emitActionUpdate(ctx context.Context, runID string, action runtimeMissionAction, state string, attempt uint32, errorCode, message, evidence string) {
	sequence := action.sequence
	e.emitUpdate(ctx, MissionUpdate{
		EventID:        identity.NewID(),
		RunID:          runID,
		Type:           "action_state_changed",
		State:          e.currentState("arrival_action"),
		ObservedAt:     time.Now().UTC(),
		ErrorCode:      errorCode,
		Message:        message,
		EvidenceJSON:   evidence,
		ActionSequence: &sequence,
		ActionType:     action.actionType,
		ActionState:    state,
		ActionAttempt:  attempt,
		FailurePolicy:  action.failurePolicy,
	})
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func waitUntil(ctx context.Context, deadline *time.Time) bool {
	if deadline == nil {
		return ctx.Err() == nil
	}
	return waitForRetry(ctx, time.Until(*deadline))
}

func retryDelayForAttempt(action runtimeMissionAction, completedAttempt uint32) time.Duration {
	exponent := float64(completedAttempt)
	if exponent > 0 {
		exponent--
	}
	delay := float64(action.retryInitialDelay) * math.Pow(action.retryBackoffMultiplier, exponent)
	const maximumDuration = time.Duration(1<<63 - 1)
	if math.IsInf(delay, 0) || delay > float64(maximumDuration) {
		return maximumDuration
	}
	return time.Duration(delay)
}

func outcomeForAppliedPolicy(action runtimeMissionAction) arrivalActionOutcome {
	switch action.failurePolicy {
	case "RETURN_TO_LAUNCH":
		return arrivalActionOutcome{policyRunState: "RTL", errorCode: runtimeActionErrorCode(action.actionType), message: "Reviewed Return to launch policy was already applied before Agent restart"}
	case "OPERATOR_INTERVENTION":
		return arrivalActionOutcome{errorCode: runtimeActionErrorCode(action.actionType), message: "Reviewed operator intervention policy remains active after Agent restart"}
	default:
		return arrivalActionOutcome{errorCode: "ARRIVAL_FAILURE_POLICY_INVALID", message: "Durable action checkpoint contains an unsupported applied policy"}
	}
}

func runtimeActionErrorCode(actionType string) string {
	switch actionType {
	case "START_PERCEPTION":
		return "PERCEPTION_START_FAILED"
	case "STOP_PERCEPTION":
		return "PERCEPTION_STOP_FAILED"
	case "HOLD_AT_ARRIVAL":
		return "ARRIVAL_HOLD_FAILED"
	case "RESUME_AFTER_ARRIVAL":
		return "ARRIVAL_RESUME_FAILED"
	default:
		return "ARRIVAL_GIMBAL_FAILED"
	}
}

func actionLabel(actionType string) string {
	switch actionType {
	case "START_PERCEPTION":
		return "Start perception"
	case "STOP_PERCEPTION":
		return "Stop perception"
	case "HOLD_AT_ARRIVAL":
		return "Hold at arrival"
	case "RESUME_AFTER_ARRIVAL":
		return "Resume after arrival"
	default:
		return "Point gimbal at incident"
	}
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
	Sequence uint32         `json:"sequence"`
	Type     string         `json:"actionType"`
	Params   map[string]any `json:"params"`
}

type translatedMission struct {
	plan            *missionpb.MissionPlan
	payload         payloadMissionPlan
	returnToLaunch  bool
	warnings        []string
	arrivalActions  []runtimeMissionAction
	startPerception *runtimeMissionAction
	stopPerception  *runtimeMissionAction
}

func (mission translatedMission) runtimeActions() []runtimeMissionAction {
	actions := append([]runtimeMissionAction(nil), mission.arrivalActions...)
	if mission.startPerception != nil {
		actions = append(actions, *cloneRuntimeAction(mission.startPerception))
	}
	if mission.stopPerception != nil {
		actions = append(actions, *cloneRuntimeAction(mission.stopPerception))
	}
	sort.Slice(actions, func(left, right int) bool { return actions[left].sequence < actions[right].sequence })
	return actions
}

func splitRuntimeActions(actions []runtimeMissionAction) (*runtimeMissionAction, *runtimeMissionAction, []runtimeMissionAction) {
	var startPerception *runtimeMissionAction
	var stopPerception *runtimeMissionAction
	arrival := make([]runtimeMissionAction, 0, len(actions))
	for _, action := range actions {
		action := action
		switch action.actionType {
		case "START_PERCEPTION":
			startPerception = cloneRuntimeAction(&action)
		case "STOP_PERCEPTION":
			stopPerception = cloneRuntimeAction(&action)
		default:
			arrival = append(arrival, action)
		}
	}
	return startPerception, stopPerception, arrival
}

type runtimeMissionAction struct {
	sequence                uint32
	actionType              string
	maxAttempts             uint32
	failurePolicy           string
	timeout                 time.Duration
	retryInitialDelay       time.Duration
	retryBackoffMultiplier  float64
	durableState            string
	attempt                 uint32
	attemptDeadlineAt       *time.Time
	nextAttemptAt           *time.Time
	latitude                float64
	longitude               float64
	altitudeAMSL            float32
	triggerAfterWaypoint    uint32
	triggerExplicit         bool
	waitForOperatorDecision bool
	sourceID                string
	detectionClasses        []string
}

type arrivalActionOutcome struct {
	completed               bool
	waitForOperatorDecision bool
	skipOptional            bool
	policyRunState          string
	errorCode               string
	message                 string
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
	arrivalActions := []runtimeMissionAction{}
	var startPerception *runtimeMissionAction
	var stopPerception *runtimeMissionAction
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
		case "START_PERCEPTION":
			runtimeAction, err := parseRuntimeMissionAction(action)
			if err != nil {
				return translatedMission{}, err
			}
			if startPerception != nil {
				return translatedMission{}, errors.New("mission plan contains more than one START_PERCEPTION action")
			}
			startPerception = &runtimeAction
		case "STOP_PERCEPTION":
			runtimeAction, err := parseRuntimeMissionAction(action)
			if err != nil {
				return translatedMission{}, err
			}
			if stopPerception != nil {
				return translatedMission{}, errors.New("mission plan contains more than one STOP_PERCEPTION action")
			}
			stopPerception = &runtimeAction
		case "HOLD_AT_ARRIVAL", "POINT_GIMBAL_AT_INCIDENT", "RESUME_AFTER_ARRIVAL":
			runtimeAction, err := parseRuntimeMissionAction(action)
			if err != nil {
				return translatedMission{}, err
			}
			arrivalActions = append(arrivalActions, runtimeAction)
		}
	}
	if (startPerception == nil) != (stopPerception == nil) {
		return translatedMission{}, errors.New("mission perception requires one START_PERCEPTION and one STOP_PERCEPTION action")
	}
	if startPerception != nil && startPerception.sequence >= stopPerception.sequence {
		return translatedMission{}, errors.New("START_PERCEPTION must precede STOP_PERCEPTION")
	}
	if len(arrivalActions) > 0 {
		if arrivalActions[0].actionType != "HOLD_AT_ARRIVAL" {
			return translatedMission{}, errors.New("acknowledged arrival actions must begin with HOLD_AT_ARRIVAL")
		}
		if len(arrivalActions) > 3 {
			return translatedMission{}, errors.New("acknowledged arrival actions support Hold, one optional incident gimbal target, and one optional final Resume")
		}
		for index := range arrivalActions {
			if !arrivalActions[index].triggerExplicit {
				arrivalActions[index].triggerAfterWaypoint = uint32(len(source.GeneratedWaypoints) - 1)
			}
			if arrivalActions[index].triggerAfterWaypoint >= uint32(len(source.GeneratedWaypoints)) {
				return translatedMission{}, fmt.Errorf("arrival action sequence %d has an out-of-range triggerAfterWaypointSequence", arrivalActions[index].sequence)
			}
			if index > 0 && arrivalActions[index].triggerAfterWaypoint != arrivalActions[0].triggerAfterWaypoint {
				return translatedMission{}, errors.New("acknowledged arrival actions must share one triggerAfterWaypointSequence")
			}
		}
		lastType := arrivalActions[len(arrivalActions)-1].actionType
		for index, action := range arrivalActions[1:] {
			switch action.actionType {
			case "POINT_GIMBAL_AT_INCIDENT":
				if index != 0 {
					return translatedMission{}, errors.New("POINT_GIMBAL_AT_INCIDENT must immediately follow HOLD_AT_ARRIVAL")
				}
			case "RESUME_AFTER_ARRIVAL":
				if index+2 != len(arrivalActions) {
					return translatedMission{}, errors.New("RESUME_AFTER_ARRIVAL must be the final acknowledged arrival action")
				}
			default:
				return translatedMission{}, fmt.Errorf("unsupported acknowledged arrival action %q", action.actionType)
			}
		}
		triggerIsFinal := arrivalActions[0].triggerAfterWaypoint+1 == uint32(len(source.GeneratedWaypoints))
		if arrivalActions[0].waitForOperatorDecision {
			if len(arrivalActions) != 1 {
				return translatedMission{}, errors.New("waitForOperatorDecision requires a Hold-only arrival chain")
			}
			if !triggerIsFinal {
				return translatedMission{}, errors.New("waitForOperatorDecision requires a final-waypoint Hold")
			}
		}
		if lastType == "RESUME_AFTER_ARRIVAL" && triggerIsFinal {
			return translatedMission{}, errors.New("RESUME_AFTER_ARRIVAL requires at least one waypoint after the arrival trigger")
		}
		if lastType != "RESUME_AFTER_ARRIVAL" && !triggerIsFinal {
			return translatedMission{}, errors.New("a non-final arrival trigger requires RESUME_AFTER_ARRIVAL")
		}
		if returnToLaunch {
			return translatedMission{}, errors.New("MAVSDK RTL-after-mission cannot be combined with acknowledged arrival actions")
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
	return translatedMission{plan: &missionpb.MissionPlan{MissionItems: items}, payload: missionPayloadPlan(source.Actions), returnToLaunch: returnToLaunch, warnings: warnings, arrivalActions: arrivalActions, startPerception: startPerception, stopPerception: stopPerception}, nil
}

func parseRuntimeMissionAction(action atlasAction) (runtimeMissionAction, error) {
	failurePolicy := stringParam(action.Params, "failurePolicy")
	validFailurePolicy := failurePolicy == "RETURN_TO_LAUNCH" || failurePolicy == "OPERATOR_INTERVENTION" || ((action.Type == "POINT_GIMBAL_AT_INCIDENT" || action.Type == "STOP_PERCEPTION") && failurePolicy == "SKIP_OPTIONAL_AND_NOTIFY")
	if !validFailurePolicy {
		return runtimeMissionAction{}, fmt.Errorf("%s requires an explicit supported failurePolicy", action.Type)
	}
	attempts, ok := numberParam(action.Params, "maxAttempts")
	if !ok || attempts < 1 || attempts > 5 || attempts != math.Trunc(attempts) {
		return runtimeMissionAction{}, fmt.Errorf("%s maxAttempts must be an integer between 1 and 5", action.Type)
	}
	timeoutMS, timeoutOK := numberParam(action.Params, "timeoutMs")
	if !timeoutOK || timeoutMS < 1_000 || timeoutMS > 120_000 || timeoutMS != math.Trunc(timeoutMS) {
		return runtimeMissionAction{}, fmt.Errorf("%s timeoutMs must be an integer between 1000 and 120000", action.Type)
	}
	retryDelayMS, retryDelayOK := numberParam(action.Params, "retryInitialDelayMs")
	if !retryDelayOK || retryDelayMS < 0 || retryDelayMS > 60_000 || retryDelayMS != math.Trunc(retryDelayMS) {
		return runtimeMissionAction{}, fmt.Errorf("%s retryInitialDelayMs must be an integer between 0 and 60000", action.Type)
	}
	retryMultiplier, retryMultiplierOK := numberParam(action.Params, "retryBackoffMultiplier")
	if !retryMultiplierOK || math.IsNaN(retryMultiplier) || math.IsInf(retryMultiplier, 0) || retryMultiplier < 1 || retryMultiplier > 5 {
		return runtimeMissionAction{}, fmt.Errorf("%s retryBackoffMultiplier must be between 1 and 5", action.Type)
	}
	runtimeAction := runtimeMissionAction{
		sequence:               action.Sequence,
		actionType:             action.Type,
		maxAttempts:            uint32(attempts),
		failurePolicy:          failurePolicy,
		timeout:                time.Duration(timeoutMS) * time.Millisecond,
		retryInitialDelay:      time.Duration(retryDelayMS) * time.Millisecond,
		retryBackoffMultiplier: retryMultiplier,
		durableState:           "REQUESTED",
	}
	if waitForOperatorDecision, exists := action.Params["waitForOperatorDecision"]; exists {
		value, ok := waitForOperatorDecision.(bool)
		if !ok || action.Type != "HOLD_AT_ARRIVAL" {
			return runtimeMissionAction{}, fmt.Errorf("%s waitForOperatorDecision must be a boolean on HOLD_AT_ARRIVAL", action.Type)
		}
		runtimeAction.waitForOperatorDecision = value
	}
	if trigger, ok := numberParam(action.Params, "triggerAfterWaypointSequence"); ok {
		if trigger < 0 || trigger > math.MaxUint32 || trigger != math.Trunc(trigger) {
			return runtimeMissionAction{}, fmt.Errorf("%s triggerAfterWaypointSequence must be a non-negative integer", action.Type)
		}
		runtimeAction.triggerAfterWaypoint = uint32(trigger)
		runtimeAction.triggerExplicit = true
	}
	if action.Type == "POINT_GIMBAL_AT_INCIDENT" {
		latitude, latitudeOK := numberParam(action.Params, "latitude")
		longitude, longitudeOK := numberParam(action.Params, "longitude")
		altitude, altitudeOK := numberParam(action.Params, "altitudeAmslMeters")
		if !latitudeOK || !longitudeOK || !altitudeOK || math.IsNaN(latitude) || math.IsNaN(longitude) || math.IsNaN(altitude) || latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 || altitude < -500 || altitude > 9000 {
			return runtimeMissionAction{}, errors.New("POINT_GIMBAL_AT_INCIDENT requires valid latitude, longitude, and AMSL altitude")
		}
		runtimeAction.latitude = latitude
		runtimeAction.longitude = longitude
		runtimeAction.altitudeAMSL = float32(altitude)
	}
	if action.Type == "START_PERCEPTION" {
		runtimeAction.sourceID = stringParam(action.Params, "sourceId")
		if values, exists := action.Params["detectionClasses"]; exists {
			items, ok := values.([]any)
			if !ok {
				return runtimeMissionAction{}, errors.New("START_PERCEPTION detectionClasses must be an array")
			}
			for _, item := range items {
				value, ok := item.(string)
				if !ok || strings.TrimSpace(value) == "" {
					return runtimeMissionAction{}, errors.New("START_PERCEPTION detectionClasses must contain non-empty strings")
				}
				runtimeAction.detectionClasses = append(runtimeAction.detectionClasses, strings.TrimSpace(value))
			}
		}
	}
	return runtimeAction, nil
}

func arrivalActionsDue(actions []runtimeMissionAction, current, total uint32) bool {
	return len(actions) > 0 && total > 0 && current > actions[0].triggerAfterWaypoint
}

func arrivalActionChainComplete(actions []runtimeMissionAction) bool {
	if len(actions) == 0 {
		return true
	}
	for _, action := range actions {
		if action.durableState == "SUCCEEDED" {
			continue
		}
		if action.durableState == "POLICY_APPLIED" && (action.actionType == "POINT_GIMBAL_AT_INCIDENT" || action.actionType == "STOP_PERCEPTION") && action.failurePolicy == "SKIP_OPTIONAL_AND_NOTIFY" {
			continue
		}
		return false
	}
	return true
}

func actionsFromCheckpoints(actions []runtimeMissionAction, checkpoints []MissionActionCheckpoint) ([]runtimeMissionAction, error) {
	bySequence := make(map[uint32]MissionActionCheckpoint, len(checkpoints))
	for _, checkpoint := range checkpoints {
		bySequence[checkpoint.Sequence] = checkpoint
	}
	result := append([]runtimeMissionAction(nil), actions...)
	for index := range result {
		checkpoint, ok := bySequence[result[index].sequence]
		if !ok {
			return nil, fmt.Errorf("Native reconciliation omitted action sequence %d", result[index].sequence)
		}
		if checkpoint.ActionType != result[index].actionType {
			return nil, fmt.Errorf("Native action sequence %d changed type from %s to %s", result[index].sequence, result[index].actionType, checkpoint.ActionType)
		}
		if !matchesActionState(checkpoint.State) || checkpoint.Attempt > result[index].maxAttempts {
			return nil, fmt.Errorf("Native action sequence %d supplied an invalid durable checkpoint", result[index].sequence)
		}
		if (checkpoint.State == "REQUESTED" && checkpoint.Attempt != 0) ||
			(checkpoint.State != "REQUESTED" && checkpoint.Attempt == 0) ||
			(checkpoint.State == "RETRYING" && checkpoint.Attempt >= result[index].maxAttempts) {
			return nil, fmt.Errorf("Native action sequence %d supplied an impossible retry position", result[index].sequence)
		}
		result[index].durableState = checkpoint.State
		result[index].attempt = checkpoint.Attempt
		result[index].attemptDeadlineAt = checkpoint.AttemptDeadlineAt
		result[index].nextAttemptAt = checkpoint.NextAttemptAt
	}
	if len(checkpoints) != len(result) {
		return nil, errors.New("Native reconciliation action count does not match the immutable plan")
	}
	return result, nil
}

func matchesRunState(value string) bool {
	return value == "UPLOADING" || value == "READY" || value == "RUNNING" || value == "PAUSED"
}

func reconciliationFailureState(value string) string {
	if value == "UPLOADING" {
		return "FAILED"
	}
	return value
}

func matchesActionState(value string) bool {
	switch value {
	case "REQUESTED", "RUNNING", "RETRYING", "SUCCEEDED", "FAILED", "POLICY_APPLIED":
		return true
	default:
		return false
	}
}

func missionPlansEquivalent(expected, actual *missionpb.MissionPlan) bool {
	if expected == nil || actual == nil || len(expected.GetMissionItems()) != len(actual.GetMissionItems()) {
		return false
	}
	for index, expectedItem := range expected.GetMissionItems() {
		actualItem := actual.GetMissionItems()[index]
		if !near(expectedItem.GetLatitudeDeg(), actualItem.GetLatitudeDeg(), 1e-7) ||
			!near(expectedItem.GetLongitudeDeg(), actualItem.GetLongitudeDeg(), 1e-7) ||
			!near(float64(expectedItem.GetRelativeAltitudeM()), float64(actualItem.GetRelativeAltitudeM()), 0.05) ||
			!sameFloat32(expectedItem.GetSpeedMS(), actualItem.GetSpeedMS(), 0.05) ||
			expectedItem.GetIsFlyThrough() != actualItem.GetIsFlyThrough() ||
			expectedItem.GetCameraAction() != actualItem.GetCameraAction() ||
			expectedItem.GetVehicleAction() != actualItem.GetVehicleAction() {
			return false
		}
	}
	return true
}

func near(left, right, tolerance float64) bool {
	return math.Abs(left-right) <= tolerance
}

func sameFloat32(left, right float32, tolerance float64) bool {
	if math.IsNaN(float64(left)) && math.IsNaN(float64(right)) {
		return true
	}
	return near(float64(left), float64(right), tolerance)
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
