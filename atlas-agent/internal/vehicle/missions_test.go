package vehicle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	actionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/action"
	missionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/mission"
	telemetrypb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/telemetry"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
	"google.golang.org/grpc"
)

type recordingActionClient struct {
	actionpb.ActionServiceClient
	order       *[]string
	armResult   actionpb.ActionResult_Result
	holdResults []actionpb.ActionResult_Result
	holdCalls   int
	rtlResult   actionpb.ActionResult_Result
}

// sitlFaultActionClient keeps the aircraft and successful policy calls on the
// real MAVSDK/PX4 path while making Hold rejection deterministic. PX4 does not
// offer a stable switch for rejecting exactly N Hold calls, so acceptance tests
// inject only those result codes at the client boundary.
type sitlFaultActionClient struct {
	actionpb.ActionServiceClient
	holdFailuresRemaining int
	holdCalls             int
	forwardedHoldCalls    int
	rtlCalls              int
}

func (client *sitlFaultActionClient) Hold(ctx context.Context, request *actionpb.HoldRequest, options ...grpc.CallOption) (*actionpb.HoldResponse, error) {
	client.holdCalls++
	if client.holdFailuresRemaining > 0 {
		client.holdFailuresRemaining--
		return &actionpb.HoldResponse{ActionResult: &actionpb.ActionResult{Result: actionpb.ActionResult_RESULT_COMMAND_DENIED}}, nil
	}
	client.forwardedHoldCalls++
	return client.ActionServiceClient.Hold(ctx, request, options...)
}

func (client *sitlFaultActionClient) ReturnToLaunch(ctx context.Context, request *actionpb.ReturnToLaunchRequest, options ...grpc.CallOption) (*actionpb.ReturnToLaunchResponse, error) {
	client.rtlCalls++
	return client.ActionServiceClient.ReturnToLaunch(ctx, request, options...)
}

func (client *recordingActionClient) Arm(context.Context, *actionpb.ArmRequest, ...grpc.CallOption) (*actionpb.ArmResponse, error) {
	*client.order = append(*client.order, "arm")
	return &actionpb.ArmResponse{ActionResult: &actionpb.ActionResult{Result: client.armResult}}, nil
}

func (client *recordingActionClient) Hold(context.Context, *actionpb.HoldRequest, ...grpc.CallOption) (*actionpb.HoldResponse, error) {
	if client.order != nil {
		*client.order = append(*client.order, "hold")
	}
	result := actionpb.ActionResult_RESULT_SUCCESS
	if client.holdCalls < len(client.holdResults) {
		result = client.holdResults[client.holdCalls]
	}
	client.holdCalls++
	return &actionpb.HoldResponse{ActionResult: &actionpb.ActionResult{Result: result}}, nil
}

func (client *recordingActionClient) ReturnToLaunch(context.Context, *actionpb.ReturnToLaunchRequest, ...grpc.CallOption) (*actionpb.ReturnToLaunchResponse, error) {
	if client.order != nil {
		*client.order = append(*client.order, "rtl")
	}
	result := client.rtlResult
	if result == actionpb.ActionResult_RESULT_UNKNOWN {
		result = actionpb.ActionResult_RESULT_SUCCESS
	}
	return &actionpb.ReturnToLaunchResponse{ActionResult: &actionpb.ActionResult{Result: result}}, nil
}

type recordingMissionClient struct {
	missionpb.MissionServiceClient
	order        *[]string
	downloadPlan *missionpb.MissionPlan
}

type recordingPerceptionControl struct {
	order      *[]string
	acquireErr error
	releaseErr error
	releases   int
}

func (control *recordingPerceptionControl) Acquire(_ context.Context, claim perception.Claim) (perception.ActivationEvidence, error) {
	if control.order != nil {
		*control.order = append(*control.order, "perception-start")
	}
	if control.acquireErr != nil {
		return perception.ActivationEvidence{}, control.acquireErr
	}
	return perception.ActivationEvidence{ClaimID: claim.ID, Owner: claim.Owner, State: "ACTIVE", SourceID: "a8-main", StreamEpoch: "epoch-1", LastFrameID: "frame-1", ObservedAt: time.Now().UTC()}, nil
}

func (control *recordingPerceptionControl) Release(_ context.Context, claimID string) (perception.ActivationEvidence, error) {
	control.releases++
	if control.releaseErr != nil {
		return perception.ActivationEvidence{}, control.releaseErr
	}
	return perception.ActivationEvidence{ClaimID: claimID, State: "INACTIVE", ObservedAt: time.Now().UTC()}, nil
}

func TestMissionPerceptionIsAcknowledgedBeforeArming(t *testing.T) {
	order := []string{}
	startAction := &runtimeMissionAction{
		sequence: 1, actionType: "START_PERCEPTION", maxAttempts: 1,
		failurePolicy: "OPERATOR_INTERVENTION", timeout: time.Second, retryBackoffMultiplier: 1,
		detectionClasses: []string{"person"},
	}
	executor := &MissionExecutor{
		logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:                &recordingActionClient{order: &order, armResult: actionpb.ActionResult_RESULT_SUCCESS},
		mission:               &recordingMissionClient{order: &order},
		perceptionControl:     &recordingPerceptionControl{order: &order},
		startPerception:       startAction,
		stopPerceptionHandled: true,
		updates:               make(chan MissionUpdate, 16),
		uploadedRunID:         "run-perception",
		state:                 "READY",
		watchCancel:           func() {},
	}

	if err := executor.start(context.Background(), context.Background(), MissionOperation{OperationID: "operation-perception", RunID: "run-perception", Type: "start"}, false); err != nil {
		t.Fatalf("start perception mission: %v", err)
	}
	want := []string{"perception-start", "arm", "start"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("required perception must precede arming: got %v want %v", order, want)
	}
}

func TestMissionPerceptionFailureBlocksArming(t *testing.T) {
	order := []string{}
	executor := &MissionExecutor{
		logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:                &recordingActionClient{order: &order, armResult: actionpb.ActionResult_RESULT_SUCCESS},
		mission:               &recordingMissionClient{order: &order},
		perceptionControl:     &recordingPerceptionControl{order: &order, acquireErr: errors.New("runtime unavailable")},
		startPerception:       &runtimeMissionAction{sequence: 1, actionType: "START_PERCEPTION", maxAttempts: 1, failurePolicy: "OPERATOR_INTERVENTION", timeout: time.Second, retryBackoffMultiplier: 1},
		stopPerceptionHandled: true,
		updates:               make(chan MissionUpdate, 16), uploadedRunID: "run-perception", state: "READY", watchCancel: func() {},
	}

	if err := executor.start(context.Background(), context.Background(), MissionOperation{OperationID: "operation-perception", RunID: "run-perception", Type: "start"}, false); err == nil {
		t.Fatal("expected unavailable perception to block mission start")
	}
	if fmt.Sprint(order) != fmt.Sprint([]string{"perception-start"}) {
		t.Fatalf("aircraft must not arm after perception failure, got %v", order)
	}
}

func TestPerceptionStopFailureDoesNotBlockMissionCompletion(t *testing.T) {
	control := &recordingPerceptionControl{releaseErr: errors.New("adapter did not stop")}
	executor := &MissionExecutor{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), perceptionControl: control,
		stopPerception:         &runtimeMissionAction{sequence: 9, actionType: "STOP_PERCEPTION", maxAttempts: 1, failurePolicy: "SKIP_OPTIONAL_AND_NOTIFY", timeout: time.Second, retryBackoffMultiplier: 1},
		startPerceptionHandled: true, updates: make(chan MissionUpdate, 16), uploadedRunID: "run-perception", activeRunID: "run-perception", state: "RUNNING", watchCancel: func() {},
	}

	executor.completeRun(context.Background(), "run-perception", 100, 1, 1, "Mission completed")
	if executor.currentState("progress") != "COMPLETED" {
		t.Fatalf("stop failure blocked completion: state=%s", executor.currentState("progress"))
	}
	foundPolicy := false
	for len(executor.updates) > 0 {
		update := <-executor.updates
		if update.ActionType == "STOP_PERCEPTION" && update.ActionState == "POLICY_APPLIED" {
			foundPolicy = true
		}
	}
	if !foundPolicy {
		t.Fatal("stop failure did not emit its reviewed skip-and-notify policy")
	}
}

func (client *recordingMissionClient) StartMission(context.Context, *missionpb.StartMissionRequest, ...grpc.CallOption) (*missionpb.StartMissionResponse, error) {
	*client.order = append(*client.order, "start")
	return &missionpb.StartMissionResponse{MissionResult: &missionpb.MissionResult{Result: missionpb.MissionResult_RESULT_SUCCESS}}, nil
}

func (client *recordingMissionClient) DownloadMission(context.Context, *missionpb.DownloadMissionRequest, ...grpc.CallOption) (*missionpb.DownloadMissionResponse, error) {
	return &missionpb.DownloadMissionResponse{
		MissionResult: &missionpb.MissionResult{Result: missionpb.MissionResult_RESULT_SUCCESS},
		MissionPlan:   client.downloadPlan,
	}, nil
}

func TestMissionStartArmsBeforeStartingMission(t *testing.T) {
	order := []string{}
	executor := &MissionExecutor{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:        &recordingActionClient{order: &order, armResult: actionpb.ActionResult_RESULT_SUCCESS},
		mission:       &recordingMissionClient{order: &order},
		updates:       make(chan MissionUpdate, 8),
		uploadedRunID: "run-1",
		state:         "READY",
		watchCancel:   func() {},
	}

	err := executor.start(context.Background(), context.Background(), MissionOperation{
		OperationID: "operation-1",
		RunID:       "run-1",
		Type:        "start",
	}, false)
	if err != nil {
		t.Fatalf("start mission: %v", err)
	}
	if len(order) != 2 || order[0] != "arm" || order[1] != "start" {
		t.Fatalf("expected arm then start, got %v", order)
	}

	gotTypes := []string{}
	for len(executor.updates) > 0 {
		gotTypes = append(gotTypes, (<-executor.updates).Type)
	}
	wantTypes := []string{"arming", "armed", "started"}
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("expected lifecycle %v, got %v", wantTypes, gotTypes)
	}
	for index := range wantTypes {
		if gotTypes[index] != wantTypes[index] {
			t.Fatalf("expected lifecycle %v, got %v", wantTypes, gotTypes)
		}
	}
}

func TestMissionStartStopsWhenArmingIsRejected(t *testing.T) {
	order := []string{}
	executor := &MissionExecutor{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:        &recordingActionClient{order: &order, armResult: actionpb.ActionResult_RESULT_COMMAND_DENIED},
		mission:       &recordingMissionClient{order: &order},
		updates:       make(chan MissionUpdate, 8),
		uploadedRunID: "run-1",
		state:         "READY",
		watchCancel:   func() {},
	}

	err := executor.start(context.Background(), context.Background(), MissionOperation{
		OperationID: "operation-1",
		RunID:       "run-1",
		Type:        "start",
	}, false)
	if err == nil {
		t.Fatal("expected rejected arm to stop mission start")
	}
	if len(order) != 1 || order[0] != "arm" {
		t.Fatalf("mission start must not be sent after rejected arm, got %v", order)
	}
}

func TestArrivalHoldRetriesAndCompletesOnlyAfterAcknowledgement(t *testing.T) {
	actions := &recordingActionClient{holdResults: []actionpb.ActionResult_Result{
		actionpb.ActionResult_RESULT_COMMAND_DENIED,
		actionpb.ActionResult_RESULT_SUCCESS,
	}}
	executor := &MissionExecutor{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:        actions,
		updates:       make(chan MissionUpdate, 16),
		uploadedRunID: "arrival-run",
		state:         "RUNNING",
	}
	arrival := runtimeMissionAction{
		sequence:               7,
		actionType:             "HOLD_AT_ARRIVAL",
		maxAttempts:            3,
		failurePolicy:          "RETURN_TO_LAUNCH",
		timeout:                time.Second,
		retryBackoffMultiplier: 1,
	}

	outcome := executor.executeArrivalActions(context.Background(), "arrival-run", []runtimeMissionAction{arrival})
	if !outcome.completed || outcome.policyRunState != "" {
		t.Fatalf("expected acknowledged Hold to complete arrival actions, got %#v", outcome)
	}
	if actions.holdCalls != 2 {
		t.Fatalf("expected two Hold attempts, got %d", actions.holdCalls)
	}
	states := []string{}
	for len(executor.updates) > 0 {
		update := <-executor.updates
		if update.Type == "action_state_changed" {
			states = append(states, update.ActionState)
		}
	}
	want := []string{"RUNNING", "RETRYING", "RUNNING", "SUCCEEDED"}
	if len(states) != len(want) {
		t.Fatalf("expected action states %v, got %v", want, states)
	}
	for index := range want {
		if states[index] != want[index] {
			t.Fatalf("expected action states %v, got %v", want, states)
		}
	}
}

func TestStagingHoldPausesForAnExplicitOperatorDecision(t *testing.T) {
	executor := &MissionExecutor{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:        &recordingActionClient{},
		updates:       make(chan MissionUpdate, 8),
		uploadedRunID: "staging-run",
		activeRunID:   "staging-run",
		state:         "RUNNING",
		watchCancel:   func() {},
	}
	arrival := runtimeMissionAction{
		sequence:                2,
		actionType:              "HOLD_AT_ARRIVAL",
		maxAttempts:             1,
		failurePolicy:           "OPERATOR_INTERVENTION",
		timeout:                 time.Second,
		retryBackoffMultiplier:  1,
		waitForOperatorDecision: true,
	}

	outcome := executor.executeArrivalActions(context.Background(), "staging-run", []runtimeMissionAction{arrival})
	if !outcome.completed || !outcome.waitForOperatorDecision {
		t.Fatalf("expected staging Hold to require an operator decision, got %#v", outcome)
	}
	executor.pauseForOperatorDecision(context.Background(), "staging-run", 100, 1, 1)
	if !executor.runIs("staging-run", "PAUSED") {
		t.Fatalf("staging run state = %q, want PAUSED", executor.currentState("progress"))
	}
	if executor.watchCancel != nil {
		t.Fatal("staging wait must stop the mission progress watcher")
	}
	updates := drainMissionUpdates(executor)
	last := updates[len(updates)-1]
	if last.Type != "paused" || last.State != "PAUSED" || !strings.Contains(last.Message, "operator decision") {
		t.Fatalf("staging pause update = %#v", last)
	}
}

func TestArrivalHoldFailureAppliesReviewedRTLPolicy(t *testing.T) {
	order := []string{}
	actions := &recordingActionClient{
		order: &order,
		holdResults: []actionpb.ActionResult_Result{
			actionpb.ActionResult_RESULT_COMMAND_DENIED,
			actionpb.ActionResult_RESULT_COMMAND_DENIED,
			actionpb.ActionResult_RESULT_COMMAND_DENIED,
		},
		rtlResult: actionpb.ActionResult_RESULT_SUCCESS,
	}
	executor := &MissionExecutor{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:        actions,
		updates:       make(chan MissionUpdate, 24),
		uploadedRunID: "policy-run",
		state:         "RUNNING",
	}
	arrival := runtimeMissionAction{
		sequence:               9,
		actionType:             "HOLD_AT_ARRIVAL",
		maxAttempts:            3,
		failurePolicy:          "RETURN_TO_LAUNCH",
		timeout:                time.Second,
		retryBackoffMultiplier: 1,
	}

	outcome := executor.executeArrivalActions(context.Background(), "policy-run", []runtimeMissionAction{arrival})
	if outcome.completed || outcome.policyRunState != "RTL" {
		t.Fatalf("expected reviewed RTL policy, got %#v", outcome)
	}
	if len(order) != 4 || order[3] != "rtl" {
		t.Fatalf("expected three Hold attempts followed by RTL, got %v", order)
	}
	states := []string{}
	for len(executor.updates) > 0 {
		update := <-executor.updates
		if update.Type == "action_state_changed" {
			states = append(states, update.ActionState)
		}
	}
	if states[len(states)-2] != "FAILED" || states[len(states)-1] != "POLICY_APPLIED" {
		t.Fatalf("expected failed then policy applied, got %v", states)
	}
}

func TestOptionalGimbalFailureAppliesReviewedSkipPolicy(t *testing.T) {
	executor := &MissionExecutor{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		updates: make(chan MissionUpdate, 4),
		state:   "RUNNING",
	}
	action := runtimeMissionAction{
		sequence:      10,
		actionType:    "POINT_GIMBAL_AT_INCIDENT",
		maxAttempts:   3,
		failurePolicy: "SKIP_OPTIONAL_AND_NOTIFY",
	}
	outcome := executor.applyArrivalFailurePolicy(context.Background(), "optional-run", action, context.DeadlineExceeded)
	if !outcome.skipOptional || outcome.policyRunState != "" {
		t.Fatalf("expected optional action to be skipped without changing flight state, got %#v", outcome)
	}
	update := <-executor.updates
	if update.ActionState != "POLICY_APPLIED" || update.FailurePolicy != "SKIP_OPTIONAL_AND_NOTIFY" {
		t.Fatalf("expected durable skip policy update, got %#v", update)
	}
}

func TestRestartDoesNotRepeatAnExhaustedRunningAction(t *testing.T) {
	actions := &recordingActionClient{}
	executor := &MissionExecutor{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:  actions,
		updates: make(chan MissionUpdate, 8),
		state:   "RUNNING",
	}
	past := time.Now().Add(-time.Second)
	action := runtimeMissionAction{
		sequence:               7,
		actionType:             "HOLD_AT_ARRIVAL",
		maxAttempts:            1,
		failurePolicy:          "OPERATOR_INTERVENTION",
		timeout:                time.Second,
		retryBackoffMultiplier: 1,
		durableState:           "RUNNING",
		attempt:                1,
		attemptDeadlineAt:      &past,
	}
	outcome := executor.executeArrivalActions(context.Background(), "exhausted-run", []runtimeMissionAction{action})
	if actions.holdCalls != 0 {
		t.Fatalf("restart must not repeat an exhausted action, got %d Hold calls", actions.holdCalls)
	}
	if outcome.completed || outcome.policyRunState != "" || outcome.errorCode != "ARRIVAL_HOLD_FAILED" {
		t.Fatalf("expected durable operator-intervention outcome, got %#v", outcome)
	}
}

func TestRestartDoesNotRepeatSucceededAction(t *testing.T) {
	actions := &recordingActionClient{}
	executor := &MissionExecutor{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:  actions,
		updates: make(chan MissionUpdate, 4),
		state:   "RUNNING",
	}
	action := runtimeMissionAction{
		sequence:      7,
		actionType:    "HOLD_AT_ARRIVAL",
		maxAttempts:   3,
		failurePolicy: "RETURN_TO_LAUNCH",
		durableState:  "SUCCEEDED",
		attempt:       1,
	}

	outcome := executor.executeArrivalActions(context.Background(), "completed-action-run", []runtimeMissionAction{action})
	if !outcome.completed || actions.holdCalls != 0 {
		t.Fatalf("a durable success must complete without replay, calls=%d outcome=%#v", actions.holdCalls, outcome)
	}
	if len(executor.updates) != 0 {
		t.Fatalf("a durable success must not emit another action transition, got %d", len(executor.updates))
	}
}

func TestRestartResumesOnlyFromDurableRetryCheckpoint(t *testing.T) {
	actions := &recordingActionClient{holdResults: []actionpb.ActionResult_Result{actionpb.ActionResult_RESULT_SUCCESS}}
	executor := &MissionExecutor{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:  actions,
		updates: make(chan MissionUpdate, 8),
		state:   "RUNNING",
	}
	past := time.Now().Add(-time.Second)
	action := runtimeMissionAction{
		sequence:               8,
		actionType:             "HOLD_AT_ARRIVAL",
		maxAttempts:            3,
		failurePolicy:          "RETURN_TO_LAUNCH",
		timeout:                time.Second,
		retryBackoffMultiplier: 2,
		durableState:           "RETRYING",
		attempt:                1,
		nextAttemptAt:          &past,
	}
	outcome := executor.executeArrivalActions(context.Background(), "retry-run", []runtimeMissionAction{action})
	if !outcome.completed || actions.holdCalls != 1 {
		t.Fatalf("expected exactly one permitted attempt-two Hold, calls=%d outcome=%#v", actions.holdCalls, outcome)
	}
	updates := []MissionUpdate{<-executor.updates, <-executor.updates}
	if updates[0].ActionState != "RUNNING" || updates[0].ActionAttempt != 2 || updates[1].ActionState != "SUCCEEDED" {
		t.Fatalf("expected retry checkpoint to resume at attempt two, got %#v", updates)
	}
}

func TestResumeAfterArrivalRequiresMAVSDKAcknowledgement(t *testing.T) {
	order := []string{}
	executor := &MissionExecutor{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		mission:       &recordingMissionClient{order: &order},
		updates:       make(chan MissionUpdate, 4),
		uploadedRunID: "pattern-run",
		state:         "RUNNING",
	}
	action := runtimeMissionAction{
		sequence:               8,
		actionType:             "RESUME_AFTER_ARRIVAL",
		maxAttempts:            1,
		failurePolicy:          "OPERATOR_INTERVENTION",
		timeout:                time.Second,
		retryBackoffMultiplier: 1,
	}

	outcome := executor.executeArrivalActions(context.Background(), "pattern-run", []runtimeMissionAction{action})
	if !outcome.completed || len(order) != 1 || order[0] != "start" {
		t.Fatalf("expected one acknowledged mission resume, order=%v outcome=%#v", order, outcome)
	}
	assertActionStates(t, drainMissionUpdates(executor), "RESUME_AFTER_ARRIVAL", "RUNNING", "SUCCEEDED")
}

func TestArrivalActionsBecomeDueAtReviewedWaypointBeforeCompletion(t *testing.T) {
	actions := []runtimeMissionAction{{triggerAfterWaypoint: 0}}
	if arrivalActionsDue(actions, 0, 4) {
		t.Fatal("arrival actions must not run before the reviewed waypoint is reached")
	}
	if !arrivalActionsDue(actions, 1, 4) {
		t.Fatal("arrival actions must run immediately after the reviewed waypoint")
	}
	if !arrivalActionsDue(actions, 4, 4) {
		t.Fatal("a delayed progress observation must still trigger pending arrival actions")
	}
}

func TestSITLMissionUploadArmsAndStarts(t *testing.T) {
	address := os.Getenv("ATLAS_TEST_SITL_MAVSDK_ADDR")
	if address == "" {
		t.Skip("set ATLAS_TEST_SITL_MAVSDK_ADDR to run against PX4 SITL")
	}
	executor, err := NewMissionExecutor(address, slog.Default())
	if err != nil {
		t.Fatalf("connect mission executor: %v", err)
	}
	defer executor.Close()

	operation := MissionOperation{
		OperationID: "sitl-upload",
		RunID:       "sitl-auto-arm-run",
		Type:        "upload",
		MissionPlanJSON: `{
			"generatedWaypoints":[
				{"sequence":0,"latitude":37.41908,"longitude":-121.99320,"altitudeMeters":20,"speedMps":4},
				{"sequence":1,"latitude":37.41918,"longitude":-121.99305,"altitudeMeters":20,"speedMps":4}
			],
			"actions":[{"sequence":0,"actionType":"RETURN_TO_LAUNCH","params":{}}]
		}`,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := executor.upload(ctx, operation); err != nil {
		t.Fatalf("upload SITL mission: %v", err)
	}

	operation.OperationID = "sitl-start"
	operation.Type = "start"
	if err := executor.start(ctx, ctx, operation, false); err != nil {
		t.Fatalf("arm and start SITL mission: %v", err)
	}

	telemetry := telemetrypb.NewTelemetryServiceClient(executor.connection)
	armedStream, err := telemetry.SubscribeArmed(ctx, &telemetrypb.SubscribeArmedRequest{})
	if err != nil {
		t.Fatalf("subscribe SITL armed state: %v", err)
	}
	armed, err := armedStream.Recv()
	if err != nil || !armed.GetIsArmed() {
		t.Fatalf("expected Atlas to arm SITL aircraft, armed=%v error=%v", armed.GetIsArmed(), err)
	}
	inAirStream, err := telemetry.SubscribeInAir(ctx, &telemetrypb.SubscribeInAirRequest{})
	if err != nil {
		t.Fatalf("subscribe SITL in-air state: %v", err)
	}
	for {
		inAir, receiveErr := inAirStream.Recv()
		if receiveErr != nil {
			t.Fatalf("wait for SITL takeoff: %v", receiveErr)
		}
		if inAir.GetIsInAir() {
			break
		}
	}

	operation.OperationID = "sitl-rtl"
	operation.Type = "return_to_launch"
	if err := executor.returnToLaunch(ctx, operation); err != nil {
		t.Fatalf("return SITL aircraft to launch: %v", err)
	}
}

func sitlArrivalPlan(failurePolicy string, includeIncidentGimbal bool) string {
	gimbalAction := ""
	if includeIncidentGimbal {
		gimbalAction = `,{
			"sequence":1,
			"actionType":"POINT_GIMBAL_AT_INCIDENT",
			"params":{"maxAttempts":3,"failurePolicy":"SKIP_OPTIONAL_AND_NOTIFY","timeoutMs":20000,"retryInitialDelayMs":500,"retryBackoffMultiplier":2,"latitude":37.41225,"longitude":-121.99880,"altitudeAmslMeters":20}
		}`
	}
	return fmt.Sprintf(`{
		"generatedWaypoints":[
			{"sequence":0,"latitude":37.41225,"longitude":-121.99880,"altitudeMeters":20,"speedMps":4}
		],
		"actions":[{
			"sequence":0,
			"actionType":"HOLD_AT_ARRIVAL",
			"params":{"maxAttempts":3,"failurePolicy":%q,"timeoutMs":20000,"retryInitialDelayMs":500,"retryBackoffMultiplier":2}
		}%s]
	}`, failurePolicy, gimbalAction)
}

func runSITLArrivalScenario(t *testing.T, executor *MissionExecutor, runID, planJSON, terminalType string) []MissionUpdate {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	operation := MissionOperation{
		OperationID:     runID + "-upload",
		RunID:           runID,
		Type:            "upload",
		MissionPlanJSON: planJSON,
	}
	if err := executor.upload(ctx, operation); err != nil {
		t.Fatalf("upload SITL arrival mission: %v", err)
	}
	operation.OperationID = runID + "-start"
	operation.Type = "start"
	if err := executor.start(ctx, ctx, operation, false); err != nil {
		t.Fatalf("arm and start SITL arrival mission: %v", err)
	}

	updates := make([]MissionUpdate, 0, 16)
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("wait for SITL %s terminal update: %v", terminalType, ctx.Err())
		case update := <-executor.Updates():
			updates = append(updates, update)
			if update.Type == terminalType {
				return updates
			}
		}
	}
}

func actionStates(updates []MissionUpdate, actionType string) []string {
	states := make([]string, 0, 8)
	for _, update := range updates {
		if update.Type == "action_state_changed" && update.ActionType == actionType {
			states = append(states, update.ActionState)
		}
	}
	return states
}

func assertActionStates(t *testing.T, updates []MissionUpdate, actionType string, expected ...string) {
	t.Helper()
	actual := actionStates(updates, actionType)
	if len(actual) != len(expected) {
		t.Fatalf("%s states = %v, want %v", actionType, actual, expected)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("%s states = %v, want %v", actionType, actual, expected)
		}
	}
}

func drainMissionUpdates(executor *MissionExecutor) []MissionUpdate {
	updates := make([]MissionUpdate, 0, len(executor.updates))
	for len(executor.updates) > 0 {
		updates = append(updates, <-executor.updates)
	}
	return updates
}

func returnSITLToLaunch(t *testing.T, executor *MissionExecutor, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := executor.returnToLaunch(ctx, MissionOperation{
		OperationID: runID + "-cleanup-rtl",
		RunID:       runID,
		Type:        "return_to_launch",
	}); err != nil {
		t.Fatalf("return SITL aircraft to launch: %v", err)
	}
	waitForSITLGrounded(t, ctx, executor.connection)
}

func waitForSITLGrounded(t *testing.T, ctx context.Context, connection *grpc.ClientConn) {
	t.Helper()
	telemetry := telemetrypb.NewTelemetryServiceClient(connection)
	inAirStream, err := telemetry.SubscribeInAir(ctx, &telemetrypb.SubscribeInAirRequest{})
	if err != nil {
		t.Fatalf("subscribe SITL in-air state: %v", err)
	}
	for {
		inAir, receiveErr := inAirStream.Recv()
		if receiveErr != nil {
			t.Fatalf("wait for SITL landing: %v", receiveErr)
		}
		if !inAir.GetIsInAir() {
			break
		}
	}
	armedStream, err := telemetry.SubscribeArmed(ctx, &telemetrypb.SubscribeArmedRequest{})
	if err != nil {
		t.Fatalf("subscribe SITL armed state: %v", err)
	}
	for {
		armed, receiveErr := armedStream.Recv()
		if receiveErr != nil {
			t.Fatalf("wait for SITL disarm: %v", receiveErr)
		}
		if !armed.GetIsArmed() {
			return
		}
	}
}

func discoverSITLGimbal(t *testing.T, ctx context.Context, payload *PayloadController) {
	t.Helper()
	for ctx.Err() == nil {
		ids, err := payload.DiscoverGimbals(ctx)
		if err == nil && len(ids) > 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("discover SITL gimbal: %v", ctx.Err())
}

func TestSITLBatch6ArrivalActionAcceptance(t *testing.T) {
	address := os.Getenv("ATLAS_TEST_SITL_MAVSDK_ADDR")
	if address == "" {
		t.Skip("set ATLAS_TEST_SITL_MAVSDK_ADDR to run the Batch 6 PX4 SITL acceptance matrix")
	}
	executor, err := NewMissionExecutor(address, slog.Default())
	if err != nil {
		t.Fatalf("connect mission executor: %v", err)
	}
	defer executor.Close()
	discoveryContext, cancelDiscovery := context.WithTimeout(context.Background(), 10*time.Second)
	discoverSITLGimbal(t, discoveryContext, executor.payload)
	cancelDiscovery()
	realActionClient := executor.action

	successClient := &sitlFaultActionClient{ActionServiceClient: realActionClient}
	executor.action = successClient
	updates := runSITLArrivalScenario(t, executor, "sitl-batch6-arrival", sitlArrivalPlan("RETURN_TO_LAUNCH", false), "completed")
	assertActionStates(t, updates, "HOLD_AT_ARRIVAL", "RUNNING", "SUCCEEDED")
	if successClient.holdCalls != 1 || successClient.forwardedHoldCalls != 1 {
		t.Fatalf("Hold calls=%d forwarded=%d, want one real PX4 acknowledgement", successClient.holdCalls, successClient.forwardedHoldCalls)
	}

	holdAction := runtimeMissionAction{
		sequence:               0,
		actionType:             "HOLD_AT_ARRIVAL",
		maxAttempts:            3,
		timeout:                20 * time.Second,
		retryInitialDelay:      500 * time.Millisecond,
		retryBackoffMultiplier: 2,
	}
	executor.setState("sitl-batch6-arrival", "RUNNING")
	retryClient := &sitlFaultActionClient{ActionServiceClient: realActionClient, holdFailuresRemaining: 1}
	executor.action = retryClient
	retryAction := holdAction
	retryAction.failurePolicy = "RETURN_TO_LAUNCH"
	if outcome := executor.executeArrivalActions(context.Background(), "sitl-batch6-arrival", []runtimeMissionAction{retryAction}); !outcome.completed {
		t.Fatalf("Hold retry did not complete: %#v", outcome)
	}
	assertActionStates(t, drainMissionUpdates(executor), "HOLD_AT_ARRIVAL", "RUNNING", "RETRYING", "RUNNING", "SUCCEEDED")
	if retryClient.holdCalls != 2 || retryClient.forwardedHoldCalls != 1 {
		t.Fatalf("Hold calls=%d forwarded=%d, want one rejection followed by one real PX4 acknowledgement", retryClient.holdCalls, retryClient.forwardedHoldCalls)
	}

	executor.setState("sitl-batch6-arrival", "RUNNING")
	operatorClient := &sitlFaultActionClient{ActionServiceClient: realActionClient, holdFailuresRemaining: 3}
	executor.action = operatorClient
	operatorAction := holdAction
	operatorAction.failurePolicy = "OPERATOR_INTERVENTION"
	operatorOutcome := executor.executeArrivalActions(context.Background(), "sitl-batch6-arrival", []runtimeMissionAction{operatorAction})
	if operatorOutcome.completed || operatorOutcome.policyRunState != "" || operatorClient.rtlCalls != 0 {
		t.Fatalf("operator intervention outcome=%#v automatic RTL calls=%d", operatorOutcome, operatorClient.rtlCalls)
	}
	assertActionStates(t, drainMissionUpdates(executor), "HOLD_AT_ARRIVAL", "RUNNING", "RETRYING", "RUNNING", "RETRYING", "RUNNING", "FAILED", "POLICY_APPLIED")

	executor.setState("sitl-batch6-arrival", "RUNNING")
	executor.payload.ConfigureMission("sitl-batch6-arrival", payloadMissionPlan{waypoints: map[uint32]payloadIntent{}})
	gimbalContext, cancelGimbal := context.WithTimeout(context.Background(), 20*time.Second)
	if err := executor.payload.ActivateMission(gimbalContext, "sitl-batch6-arrival", "RUNNING"); err != nil {
		cancelGimbal()
		t.Fatalf("reactivate mission payload for incident gimbal acceptance: %v", err)
	}
	executor.action = realActionClient
	gimbalAction := runtimeMissionAction{
		sequence:               1,
		actionType:             "POINT_GIMBAL_AT_INCIDENT",
		maxAttempts:            3,
		failurePolicy:          "SKIP_OPTIONAL_AND_NOTIFY",
		timeout:                20 * time.Second,
		retryInitialDelay:      500 * time.Millisecond,
		retryBackoffMultiplier: 2,
		latitude:               37.41225,
		longitude:              -121.99880,
		altitudeAMSL:           20,
	}
	gimbalOutcome := executor.executeArrivalActions(gimbalContext, "sitl-batch6-arrival", []runtimeMissionAction{gimbalAction})
	cancelGimbal()
	if !gimbalOutcome.completed {
		t.Fatalf("optional incident gimbal action did not succeed: %#v", gimbalOutcome)
	}
	assertActionStates(t, drainMissionUpdates(executor), "POINT_GIMBAL_AT_INCIDENT", "RUNNING", "SUCCEEDED")

	executor.setState("sitl-batch6-arrival", "RUNNING")
	rtlClient := &sitlFaultActionClient{ActionServiceClient: realActionClient, holdFailuresRemaining: 3}
	executor.action = rtlClient
	rtlAction := holdAction
	rtlAction.failurePolicy = "RETURN_TO_LAUNCH"
	rtlOutcome := executor.executeArrivalActions(context.Background(), "sitl-batch6-arrival", []runtimeMissionAction{rtlAction})
	if rtlOutcome.completed || rtlOutcome.policyRunState != "RTL" {
		t.Fatalf("exhausted Hold did not apply RTL: %#v", rtlOutcome)
	}
	assertActionStates(t, drainMissionUpdates(executor), "HOLD_AT_ARRIVAL", "RUNNING", "RETRYING", "RUNNING", "RETRYING", "RUNNING", "FAILED", "POLICY_APPLIED")
	if rtlClient.holdCalls != 3 || rtlClient.forwardedHoldCalls != 0 || rtlClient.rtlCalls != 1 {
		t.Fatalf("Hold calls=%d forwarded=%d RTL calls=%d, want three rejections then one real PX4 RTL", rtlClient.holdCalls, rtlClient.forwardedHoldCalls, rtlClient.rtlCalls)
	}
	executor.payload.EndMission(context.Background(), "sitl-batch6-arrival")
	groundContext, cancelGround := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelGround()
	waitForSITLGrounded(t, groundContext, executor.connection)
}

func TestSITLAgentRestartReconcilesOnboardMissionAndActionCheckpoint(t *testing.T) {
	address := os.Getenv("ATLAS_TEST_SITL_MAVSDK_ADDR")
	if address == "" {
		t.Skip("set ATLAS_TEST_SITL_MAVSDK_ADDR to run live Agent restart reconciliation")
	}
	const runID = "sitl-reconciled-run"
	planJSON := sitlArrivalPlan("RETURN_TO_LAUNCH", false)
	firstExecutor, err := NewMissionExecutor(address, slog.Default())
	if err != nil {
		t.Fatalf("connect first mission executor: %v", err)
	}
	operationContext, cancelOperation := context.WithTimeout(context.Background(), 60*time.Second)
	operation := MissionOperation{
		OperationID:     runID + "-upload",
		RunID:           runID,
		Type:            "upload",
		MissionPlanJSON: planJSON,
	}
	if err := firstExecutor.upload(operationContext, operation); err != nil {
		cancelOperation()
		firstExecutor.Close()
		t.Fatalf("upload mission before Agent restart: %v", err)
	}
	operation.OperationID = runID + "-start"
	operation.Type = "start"
	if err := firstExecutor.start(operationContext, operationContext, operation, false); err != nil {
		cancelOperation()
		firstExecutor.Close()
		t.Fatalf("start mission before Agent restart: %v", err)
	}
	cancelOperation()
	if err := firstExecutor.Close(); err != nil {
		t.Fatalf("close first Agent executor: %v", err)
	}

	restartedExecutor, err := NewMissionExecutor(address, slog.Default())
	if err != nil {
		t.Fatalf("connect restarted mission executor: %v", err)
	}
	defer restartedExecutor.Close()
	actions := &sitlFaultActionClient{ActionServiceClient: restartedExecutor.action}
	restartedExecutor.action = actions
	currentWaypoint := uint32(0)
	reconciliationContext, cancelReconciliation := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancelReconciliation()
	restartedExecutor.Reconcile(reconciliationContext, MissionReconciliation{
		ReconciliationID: "sitl-reconcile-after-agent-restart",
		RunID:            runID,
		State:            "RUNNING",
		MissionPlanJSON:  planJSON,
		CurrentWaypoint:  &currentWaypoint,
		TotalWaypoints:   1,
		Actions: []MissionActionCheckpoint{{
			Sequence:   0,
			ActionType: "HOLD_AT_ARRIVAL",
			State:      "REQUESTED",
		}},
	})

	reconciliationAccepted := false
	holdSucceeded := false
	for {
		select {
		case <-reconciliationContext.Done():
			t.Fatalf("wait for live Agent reconciliation: %v", reconciliationContext.Err())
		case update := <-restartedExecutor.Updates():
			switch update.Type {
			case "reconciliation_failed":
				t.Fatalf("live Agent reconciliation failed: %s: %s", update.ErrorCode, update.Message)
			case "reconciliation_accepted":
				reconciliationAccepted = true
				if update.CurrentWaypoint == nil || *update.CurrentWaypoint != currentWaypoint || update.TotalWaypoints == nil || *update.TotalWaypoints != 1 {
					t.Fatalf("reconciled progress = current %v total %v, want 0/1", update.CurrentWaypoint, update.TotalWaypoints)
				}
			case "action_state_changed":
				if update.ActionType == "HOLD_AT_ARRIVAL" && update.ActionState == "SUCCEEDED" {
					holdSucceeded = true
				}
			case "completed":
				if !reconciliationAccepted || !holdSucceeded {
					t.Fatalf("mission completed before reconciliation and Hold acknowledgement: accepted=%t hold=%t", reconciliationAccepted, holdSucceeded)
				}
				if actions.holdCalls != 1 || actions.forwardedHoldCalls != 1 {
					t.Fatalf("restarted Agent Hold calls=%d forwarded=%d, want one durable requested attempt", actions.holdCalls, actions.forwardedHoldCalls)
				}
				returnSITLToLaunch(t, restartedExecutor, runID)
				return
			}
		}
	}
}

func TestTranslateMissionPlanMapsFlightAndGimbalIntent(t *testing.T) {
	translated, err := translateMissionPlan(`{
        "generatedWaypoints": [
          {"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25,"speedMps":4},
          {"sequence":1,"latitude":51.0005,"longitude":-0.1005,"altitudeMeters":25,"holdSeconds":10}
        ],
        "actions": [
          {"sequence":0,"actionType":"SET_GIMBAL_ORIENTATION","params":{"pitchDegrees":-35,"yawMode":"FOLLOW_DRONE_HEADING"}},
          {"sequence":1,"actionType":"START_RECORDING","params":{}},
          {"sequence":2,"actionType":"NAVIGATE_TO","params":{"waypointSequence":0}},
          {"sequence":3,"actionType":"NAVIGATE_TO","params":{"waypointSequence":1}},
          {"sequence":4,"actionType":"SET_GIMBAL_ORIENTATION","params":{"waypointSequence":1,"pitchDegrees":-45,"yawMode":"LOOK_AT_POINT","target":{"latitude":51.001,"longitude":-0.101}}},
          {"sequence":5,"actionType":"STOP_RECORDING","params":{}},
          {"sequence":6,"actionType":"RETURN_TO_LAUNCH","params":{}}
        ]
      }`)
	if err != nil {
		t.Fatalf("translate mission: %v", err)
	}
	if !translated.returnToLaunch {
		t.Fatal("expected RTL-after-mission option")
	}
	items := translated.plan.GetMissionItems()
	if len(items) != 2 {
		t.Fatalf("got %d mission items", len(items))
	}
	if !math.IsNaN(float64(items[0].GetGimbalPitchDeg())) || !math.IsNaN(float64(items[0].GetGimbalYawDeg())) {
		t.Fatalf("PX4 mission item must not compete with Agent payload ownership: pitch=%v yaw=%v", items[0].GetGimbalPitchDeg(), items[0].GetGimbalYawDeg())
	}
	if translated.payload.global.gimbal == nil || translated.payload.global.gimbal.pitch != -35 {
		t.Fatalf("global payload intent was not retained: %#v", translated.payload.global.gimbal)
	}
	if translated.payload.waypoints[1].gimbal == nil || translated.payload.waypoints[1].gimbal.pitch != -45 || math.IsNaN(float64(items[1].GetYawDeg())) {
		t.Fatalf("waypoint payload intent was not mapped: %#v aircraft_yaw=%v", translated.payload.waypoints[1].gimbal, items[1].GetYawDeg())
	}
	if items[0].GetCameraAction() != missionpb.MissionItem_CAMERA_ACTION_START_VIDEO || items[1].GetCameraAction() != missionpb.MissionItem_CAMERA_ACTION_STOP_VIDEO {
		t.Fatalf("video actions were not mapped: %v %v", items[0].GetCameraAction(), items[1].GetCameraAction())
	}
	if items[0].GetIsFlyThrough() != true || items[1].GetIsFlyThrough() != false || items[1].GetLoiterTimeS() != 10 {
		t.Fatal("hold/fly-through behavior was not mapped")
	}
}

func TestTranslateMissionPlanRetainsAcknowledgedPerceptionActions(t *testing.T) {
	translated, err := translateMissionPlan(`{
        "generatedWaypoints":[{"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25}],
        "actions":[
          {"sequence":0,"actionType":"START_PERCEPTION","params":{"detectionClasses":["person"],"maxAttempts":3,"failurePolicy":"OPERATOR_INTERVENTION","timeoutMs":20000,"retryInitialDelayMs":2000,"retryBackoffMultiplier":2}},
          {"sequence":1,"actionType":"STOP_PERCEPTION","params":{"maxAttempts":2,"failurePolicy":"SKIP_OPTIONAL_AND_NOTIFY","timeoutMs":10000,"retryInitialDelayMs":1000,"retryBackoffMultiplier":2}}
        ]
      }`)
	if err != nil {
		t.Fatalf("translate mission: %v", err)
	}
	if len(translated.warnings) != 0 {
		t.Fatalf("perception actions must not produce translation warnings, got %v", translated.warnings)
	}
	if translated.startPerception == nil || translated.stopPerception == nil {
		t.Fatalf("perception actions were not retained: start=%#v stop=%#v", translated.startPerception, translated.stopPerception)
	}
	if len(translated.startPerception.detectionClasses) != 1 || translated.startPerception.detectionClasses[0] != "person" {
		t.Fatalf("detection profile was not retained: %#v", translated.startPerception.detectionClasses)
	}
}

func TestTranslateMissionPlanRetainsReviewedArrivalActions(t *testing.T) {
	translated, err := translateMissionPlan(`{
        "generatedWaypoints":[{"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25}],
        "actions":[
          {"sequence":5,"actionType":"HOLD_AT_ARRIVAL","params":{"maxAttempts":3,"failurePolicy":"RETURN_TO_LAUNCH","timeoutMs":20000,"retryInitialDelayMs":2000,"retryBackoffMultiplier":2}},
          {"sequence":6,"actionType":"POINT_GIMBAL_AT_INCIDENT","params":{"maxAttempts":3,"failurePolicy":"SKIP_OPTIONAL_AND_NOTIFY","timeoutMs":20000,"retryInitialDelayMs":2000,"retryBackoffMultiplier":2,"latitude":51.001,"longitude":-0.101,"altitudeAmslMeters":42}}
        ]
      }`)
	if err != nil {
		t.Fatalf("translate arrival actions: %v", err)
	}
	if len(translated.arrivalActions) != 2 {
		t.Fatalf("expected two acknowledged arrival actions, got %#v", translated.arrivalActions)
	}
	if translated.arrivalActions[0].sequence != 5 || translated.arrivalActions[0].actionType != "HOLD_AT_ARRIVAL" {
		t.Fatalf("Hold action identity was not retained: %#v", translated.arrivalActions[0])
	}
	if translated.arrivalActions[1].altitudeAMSL != 42 {
		t.Fatalf("gimbal target evidence was not retained: %#v", translated.arrivalActions[1])
	}
	if translated.arrivalActions[0].timeout != 20*time.Second || translated.arrivalActions[0].retryInitialDelay != 2*time.Second || translated.arrivalActions[0].retryBackoffMultiplier != 2 {
		t.Fatalf("reviewed action timing was not retained: %#v", translated.arrivalActions[0])
	}
	if translated.arrivalActions[1].failurePolicy != "SKIP_OPTIONAL_AND_NOTIFY" {
		t.Fatalf("optional failure policy was not retained: %#v", translated.arrivalActions[1])
	}
}

func TestTranslateMissionPlanRetainsHoldAtStagingWaitSemantics(t *testing.T) {
	translated, err := translateMissionPlan(`{
        "generatedWaypoints":[{"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25}],
        "actions":[
          {"sequence":1,"actionType":"HOLD_AT_ARRIVAL","params":{"triggerAfterWaypointSequence":0,"maxAttempts":3,"failurePolicy":"RETURN_TO_LAUNCH","timeoutMs":20000,"retryInitialDelayMs":2000,"retryBackoffMultiplier":2,"waitForOperatorDecision":true}}
        ]
      }`)
	if err != nil {
		t.Fatalf("translate staging Hold: %v", err)
	}
	if len(translated.arrivalActions) != 1 || !translated.arrivalActions[0].waitForOperatorDecision {
		t.Fatalf("staging wait semantics were not retained: %#v", translated.arrivalActions)
	}

	_, err = translateMissionPlan(`{
        "generatedWaypoints":[{"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25}],
        "actions":[
          {"sequence":1,"actionType":"HOLD_AT_ARRIVAL","params":{"triggerAfterWaypointSequence":0,"maxAttempts":3,"failurePolicy":"RETURN_TO_LAUNCH","timeoutMs":20000,"retryInitialDelayMs":2000,"retryBackoffMultiplier":2,"waitForOperatorDecision":true}},
          {"sequence":2,"actionType":"POINT_GIMBAL_AT_INCIDENT","params":{"triggerAfterWaypointSequence":0,"maxAttempts":3,"failurePolicy":"SKIP_OPTIONAL_AND_NOTIFY","timeoutMs":20000,"retryInitialDelayMs":2000,"retryBackoffMultiplier":2,"latitude":51.001,"longitude":-0.101,"altitudeAmslMeters":42}}
        ]
      }`)
	if err == nil || !strings.Contains(err.Error(), "Hold-only") {
		t.Fatalf("expected staging wait with a gimbal action to be rejected, got %v", err)
	}
}

func TestTranslateMissionPlanAcceptsDurableMidMissionArrivalPhase(t *testing.T) {
	translated, err := translateMissionPlan(`{
        "generatedWaypoints": [
          {"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25},
          {"sequence":1,"latitude":51.0005,"longitude":-0.1005,"altitudeMeters":25},
          {"sequence":2,"latitude":51.001,"longitude":-0.101,"altitudeMeters":25}
        ],
        "actions": [
          {"sequence":3,"actionType":"HOLD_AT_ARRIVAL","params":{"triggerAfterWaypointSequence":0,"maxAttempts":3,"failurePolicy":"RETURN_TO_LAUNCH","timeoutMs":20000,"retryInitialDelayMs":2000,"retryBackoffMultiplier":2}},
          {"sequence":4,"actionType":"RESUME_AFTER_ARRIVAL","params":{"triggerAfterWaypointSequence":0,"maxAttempts":3,"failurePolicy":"RETURN_TO_LAUNCH","timeoutMs":20000,"retryInitialDelayMs":2000,"retryBackoffMultiplier":2}}
        ]
      }`)
	if err != nil {
		t.Fatalf("translate mid-mission arrival phase: %v", err)
	}
	if len(translated.arrivalActions) != 2 || translated.arrivalActions[0].triggerAfterWaypoint != 0 || translated.arrivalActions[1].actionType != "RESUME_AFTER_ARRIVAL" {
		t.Fatalf("arrival phase was not retained: %#v", translated.arrivalActions)
	}

	_, err = translateMissionPlan(`{
        "generatedWaypoints": [
          {"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25},
          {"sequence":1,"latitude":51.0005,"longitude":-0.1005,"altitudeMeters":25}
        ],
        "actions": [
          {"sequence":2,"actionType":"HOLD_AT_ARRIVAL","params":{"triggerAfterWaypointSequence":0,"maxAttempts":3,"failurePolicy":"RETURN_TO_LAUNCH","timeoutMs":20000,"retryInitialDelayMs":2000,"retryBackoffMultiplier":2}}
        ]
      }`)
	if err == nil || !strings.Contains(err.Error(), "requires RESUME_AFTER_ARRIVAL") {
		t.Fatalf("expected a non-final Hold without Resume to be rejected, got %v", err)
	}
}

func TestMissionReconciliationVerifiesOnboardPlanAndRestoresReadyRun(t *testing.T) {
	planJSON := `{"generatedWaypoints":[{"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25}],"actions":[]}`
	translated, err := translateMissionPlan(planJSON)
	if err != nil {
		t.Fatalf("translate reconciliation plan: %v", err)
	}
	executor := &MissionExecutor{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		mission: &recordingMissionClient{downloadPlan: translated.plan},
		updates: make(chan MissionUpdate, 4),
	}
	executor.Reconcile(context.Background(), MissionReconciliation{
		ReconciliationID: "reconcile-1",
		RunID:            "run-recovered",
		State:            "UPLOADING",
		MissionPlanJSON:  planJSON,
		TotalWaypoints:   1,
	})
	update := <-executor.updates
	if update.Type != "reconciliation_accepted" || update.State != "READY" {
		t.Fatalf("expected verified upload to recover as READY, got %#v", update)
	}
	if executor.uploadedRunID != "run-recovered" || executor.state != "READY" {
		t.Fatalf("executor did not restore Native run identity: run=%q state=%q", executor.uploadedRunID, executor.state)
	}
}

func TestMissionReconciliationRejectsDifferentOnboardMission(t *testing.T) {
	expectedJSON := `{"generatedWaypoints":[{"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25}],"actions":[]}`
	different, err := translateMissionPlan(`{"generatedWaypoints":[{"sequence":0,"latitude":52,"longitude":-0.1,"altitudeMeters":25}],"actions":[]}`)
	if err != nil {
		t.Fatalf("translate onboard plan: %v", err)
	}
	executor := &MissionExecutor{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		mission: &recordingMissionClient{downloadPlan: different.plan},
		updates: make(chan MissionUpdate, 4),
	}
	executor.Reconcile(context.Background(), MissionReconciliation{
		ReconciliationID: "reconcile-mismatch",
		RunID:            "run-mismatch",
		State:            "RUNNING",
		MissionPlanJSON:  expectedJSON,
		TotalWaypoints:   1,
	})
	update := <-executor.updates
	if update.Type != "reconciliation_failed" || update.ErrorCode != "RECONCILIATION_PLAN_MISMATCH" || update.State != "RUNNING" {
		t.Fatalf("expected explicit non-resuming mismatch, got %#v", update)
	}
	if executor.uploadedRunID != "" {
		t.Fatalf("mismatched onboard mission must not be associated with Native run, got %q", executor.uploadedRunID)
	}
}

func TestTranslateMissionPlanRejectsEmptyPlan(t *testing.T) {
	if _, err := translateMissionPlan(`{"generatedWaypoints":[],"actions":[]}`); err == nil {
		t.Fatal("expected empty plan to fail")
	}
}
