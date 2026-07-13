package vehicleagentchannel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/comms"
	"github.com/sunnyside/atlas/atlas-agent/internal/mavlinkobserver"
	pb "github.com/sunnyside/atlas/atlas-agent/internal/transport/vehicleagentchannelpb/atlas"
	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
)

func TestEnqueueTelemetryKeepsLatestSnapshot(t *testing.T) {
	outbound := newOutboundQueues()

	if !enqueueTelemetry(context.Background(), outbound, testTelemetryMessage("old")) {
		t.Fatal("expected first telemetry enqueue")
	}

	if !enqueueTelemetry(context.Background(), outbound, testTelemetryMessage("new")) {
		t.Fatal("expected latest telemetry enqueue")
	}

	msg := <-outbound.telemetry
	if msg.GetVehicleAgentId() != "new" {
		t.Fatalf("expected latest telemetry message, got %q", msg.GetVehicleAgentId())
	}

	if len(outbound.telemetry) != 0 {
		t.Fatalf("expected one telemetry message to be retained, got %d queued", len(outbound.telemetry)+1)
	}
}

func TestTelemetryBackpressureDoesNotBlockCriticalMessages(t *testing.T) {
	outbound := newOutboundQueues()

	if !enqueueTelemetry(context.Background(), outbound, testTelemetryMessage("telemetry")) {
		t.Fatal("expected telemetry enqueue")
	}

	for i := 0; i < cap(outbound.heartbeat); i++ {
		if !enqueueHeartbeat(context.Background(), outbound, testHeartbeatMessage("heartbeat")) {
			t.Fatalf("expected heartbeat enqueue %d", i)
		}
	}

	if !enqueueCritical(context.Background(), outbound, testVehicleActionStatusMessage("critical")) {
		t.Fatal("expected critical vehicle action status enqueue")
	}

	msg, ok := nextOutboundMessage(context.Background(), outbound)
	if !ok {
		t.Fatal("expected outbound message")
	}

	if msg.GetVehicleAgentId() != "critical" {
		t.Fatalf("expected critical message first, got %q", msg.GetVehicleAgentId())
	}
}

func TestNextOutboundMessagePrioritizesCriticalThenHeartbeatThenTelemetry(t *testing.T) {
	outbound := newOutboundQueues()

	if !enqueueTelemetry(context.Background(), outbound, testTelemetryMessage("telemetry")) {
		t.Fatal("expected telemetry enqueue")
	}
	if !enqueueHeartbeat(context.Background(), outbound, testHeartbeatMessage("heartbeat")) {
		t.Fatal("expected heartbeat enqueue")
	}
	if !enqueueCritical(context.Background(), outbound, testVehicleActionStatusMessage("critical")) {
		t.Fatal("expected critical enqueue")
	}

	assertNextAgentID(t, outbound, "critical")
	assertNextAgentID(t, outbound, "heartbeat")
	assertNextAgentID(t, outbound, "telemetry")
}

func TestMissionExecutionStatusUsesCriticalQueue(t *testing.T) {
	outbound := newOutboundQueues()

	if !sendMissionExecutionStatus(context.Background(), outbound, "critical", "mex-000001", missionExecutionStateUploading, "") {
		t.Fatal("expected mission execution status enqueue")
	}

	msg, ok := nextOutboundMessage(context.Background(), outbound)
	if !ok {
		t.Fatal("expected outbound message")
	}

	status := msg.GetMissionExecutionStatus()
	if status.GetExecutionId() != "mex-000001" {
		t.Fatalf("expected mission execution status, got %#v", msg)
	}

	if status.GetState() != missionExecutionStateUploading {
		t.Fatalf("expected uploading status, got %q", status.GetState())
	}
}

func TestHandleGimbalControlCallsController(t *testing.T) {
	controller := &fakeGimbalController{}

	handleGimbalControl(context.Background(), nil, controller, &pb.GimbalControlCommand{
		DroneId:           "drone-001",
		PitchRateDegS:     20,
		YawRateDegS:       -15,
		TargetSystemId:    1,
		TargetComponentId: 154,
		GimbalDeviceId:    1,
	})

	if !controller.called {
		t.Fatal("expected gimbal controller to be called")
	}
	if controller.command.PitchRateDegS != 20 {
		t.Fatalf("expected pitch rate 20, got %f", controller.command.PitchRateDegS)
	}
	if controller.command.YawRateDegS != -15 {
		t.Fatalf("expected yaw rate -15, got %f", controller.command.YawRateDegS)
	}
	if controller.command.TargetComponentID != 154 {
		t.Fatalf("expected target component 154, got %d", controller.command.TargetComponentID)
	}
}

func TestHandleMissionUploadCallsGatewayAndReportsUploaded(t *testing.T) {
	outbound := newOutboundQueues()
	gateway := &fakeGateway{}

	speed := 6.5
	loiterTime := 12.0
	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{VehicleAgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
		ExecutionId:      "mex-000001",
		MissionId:        "msn-000001",
		DroneId:          "drone-001",
		Action:           missionActionUpload,
		CompletionAction: string(vehicle.MissionCompletionActionLand),
		Waypoints: []*pb.MissionWaypoint{
			{
				Sequence:          1,
				Latitude:          51.5074,
				Longitude:         -0.1278,
				RelativeAltitudeM: 30,
				SpeedMps:          &speed,
				LoiterTimeS:       &loiterTime,
			},
		},
	})
	if err != nil {
		t.Fatalf("handle mission execution: %v", err)
	}

	if outcome.state != missionExecutionStateUploadedToVehicle {
		t.Fatalf("expected uploaded_to_vehicle outcome, got %q", outcome.state)
	}

	if len(gateway.uploadedMissions) != 1 {
		t.Fatalf("expected one uploaded mission, got %d", len(gateway.uploadedMissions))
	}

	if got := gateway.uploadedMissions[0].Waypoints[0].SpeedMPS; got == nil || *got != speed {
		t.Fatalf("expected waypoint speed %f, got %v", speed, got)
	}

	if got := gateway.uploadedMissions[0].CompletionAction; got != vehicle.MissionCompletionActionLand {
		t.Fatalf("expected land completion action, got %q", got)
	}

	if got := gateway.uploadedMissions[0].Waypoints[0].LoiterTimeS; got == nil || *got != loiterTime {
		t.Fatalf("expected waypoint loiter time %f, got %v", loiterTime, got)
	}

	first, ok := nextOutboundMessage(context.Background(), outbound)
	if !ok {
		t.Fatal("expected uploading status")
	}

	if first.GetMissionExecutionStatus().GetState() != missionExecutionStateUploading {
		t.Fatalf("expected uploading status, got %q", first.GetMissionExecutionStatus().GetState())
	}

	second, ok := nextOutboundMessage(context.Background(), outbound)
	if !ok {
		t.Fatal("expected uploaded_to_vehicle status")
	}

	if second.GetMissionExecutionStatus().GetState() != missionExecutionStateUploadedToVehicle {
		t.Fatalf("expected uploaded_to_vehicle status, got %q", second.GetMissionExecutionStatus().GetState())
	}
}

func TestHandleMissionUploadReportsGatewayFailure(t *testing.T) {
	outbound := newOutboundQueues()
	gateway := &fakeGateway{uploadErr: errors.New("upload rejected")}

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{VehicleAgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
		ExecutionId: "mex-000001",
		MissionId:   "msn-000001",
		DroneId:     "drone-001",
		Action:      missionActionUpload,
	})
	if err != nil {
		t.Fatalf("handle mission execution: %v", err)
	}

	if outcome.state != missionExecutionStateUploadFailed {
		t.Fatalf("expected upload_failed outcome, got %q", outcome.state)
	}

	_, _ = nextOutboundMessage(context.Background(), outbound)
	failed, ok := nextOutboundMessage(context.Background(), outbound)
	if !ok {
		t.Fatal("expected failure status")
	}

	if failed.GetMissionExecutionStatus().GetState() != missionExecutionStateUploadFailed {
		t.Fatalf("expected upload_failed status, got %q", failed.GetMissionExecutionStatus().GetState())
	}
}

func TestHandleMissionStartCallsGatewayAndReportsActive(t *testing.T) {
	outbound := newOutboundQueues()
	gateway := &fakeGateway{}

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{VehicleAgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
		ExecutionId: "mex-000001",
		MissionId:   "msn-000001",
		DroneId:     "drone-001",
		Action:      missionActionStart,
	})
	if err != nil {
		t.Fatalf("handle mission execution: %v", err)
	}

	if outcome.state != missionExecutionStateActive {
		t.Fatalf("expected active outcome, got %q", outcome.state)
	}

	if !equalStrings(gateway.calls, []string{"arm", "takeoff", "start_mission"}) {
		t.Fatalf("expected arm/takeoff/start workflow, got %v", gateway.calls)
	}

	if gateway.startMissionCalls != 1 {
		t.Fatalf("expected one start mission call, got %d", gateway.startMissionCalls)
	}

	msg, ok := nextOutboundMessage(context.Background(), outbound)
	if !ok {
		t.Fatal("expected active status")
	}

	if msg.GetMissionExecutionStatus().GetState() != missionExecutionStateActive {
		t.Fatalf("expected active status, got %q", msg.GetMissionExecutionStatus().GetState())
	}
}

func TestHandleMissionRTLCallsGatewayAndReportsRTLRequested(t *testing.T) {
	outbound := newOutboundQueues()
	gateway := &fakeGateway{}

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{VehicleAgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
		ExecutionId: "mex-000001",
		MissionId:   "msn-000001",
		DroneId:     "drone-001",
		Action:      missionActionRTL,
	})
	if err != nil {
		t.Fatalf("handle mission execution: %v", err)
	}

	if outcome.state != missionExecutionStateRTLRequested {
		t.Fatalf("expected rtl_requested outcome, got %q", outcome.state)
	}

	if !equalStrings(gateway.calls, []string{"return_to_launch"}) {
		t.Fatalf("expected RTL gateway call, got %v", gateway.calls)
	}

	msg := nextMissionStatus(t, outbound)
	if msg.GetState() != missionExecutionStateRTLRequested {
		t.Fatalf("expected rtl_requested status, got %q", msg.GetState())
	}
}

func TestHandleMissionStartStopsWhenArmFails(t *testing.T) {
	outbound := newOutboundQueues()
	gateway := &fakeGateway{armErr: errors.New("arm rejected")}

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{VehicleAgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
		ExecutionId: "mex-000001",
		MissionId:   "msn-000001",
		DroneId:     "drone-001",
		Action:      missionActionStart,
	})
	if err != nil {
		t.Fatalf("handle mission execution: %v", err)
	}

	if outcome.state != missionExecutionStateFailed {
		t.Fatalf("expected failed outcome, got %q", outcome.state)
	}

	if !equalStrings(gateway.calls, []string{"arm"}) {
		t.Fatalf("expected workflow to stop after arm failure, got %v", gateway.calls)
	}

	msg := nextMissionStatus(t, outbound)
	status := msg.GetState()
	if status != missionExecutionStateFailed {
		t.Fatalf("expected failed status, got %q", status)
	}
}

func TestHandleMissionStartStopsWhenTakeoffFails(t *testing.T) {
	outbound := newOutboundQueues()
	gateway := &fakeGateway{takeoffErr: errors.New("takeoff rejected")}

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{VehicleAgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
		ExecutionId: "mex-000001",
		MissionId:   "msn-000001",
		DroneId:     "drone-001",
		Action:      missionActionStart,
	})
	if err != nil {
		t.Fatalf("handle mission execution: %v", err)
	}

	if outcome.state != missionExecutionStateFailed {
		t.Fatalf("expected failed outcome, got %q", outcome.state)
	}

	if !equalStrings(gateway.calls, []string{"arm", "takeoff"}) {
		t.Fatalf("expected workflow to stop after takeoff failure, got %v", gateway.calls)
	}

	msg := nextMissionStatus(t, outbound)
	status := msg.GetState()
	if status != missionExecutionStateFailed {
		t.Fatalf("expected failed status, got %q", status)
	}
}

func TestHandleMissionStartReportsProgressCompletionAndHold(t *testing.T) {
	outbound := newOutboundQueues()
	progress := make(chan vehicle.MissionProgressEvent, 2)
	progress <- vehicle.MissionProgressEvent{Current: 1, Total: 2}
	progress <- vehicle.MissionProgressEvent{Current: 2, Total: 2, Finished: true}
	close(progress)
	gateway := &fakeGateway{progress: progress}

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{VehicleAgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
		ExecutionId:      "mex-000001",
		MissionId:        "msn-000001",
		DroneId:          "drone-001",
		Action:           missionActionStart,
		CompletionAction: string(vehicle.MissionCompletionActionHold),
	})
	if err != nil {
		t.Fatalf("handle mission execution: %v", err)
	}

	if outcome.state != missionExecutionStateActive {
		t.Fatalf("expected active outcome, got %q", outcome.state)
	}

	initial := nextMissionStatus(t, outbound)
	if initial.GetState() != missionExecutionStateActive {
		t.Fatalf("expected initial active status, got %q", initial.GetState())
	}

	active := nextMissionStatus(t, outbound)
	if active.GetState() != missionExecutionStateActive {
		t.Fatalf("expected progress active status, got %q", active.GetState())
	}
	if active.GetCurrentMissionItem() != 1 || active.GetTotalMissionItems() != 2 {
		t.Fatalf("expected progress 1/2, got %d/%d", active.GetCurrentMissionItem(), active.GetTotalMissionItems())
	}

	completed := nextMissionStatus(t, outbound)
	if completed.GetState() != missionExecutionStateActive {
		t.Fatalf("expected final progress active status, got %q", completed.GetState())
	}
	if completed.GetCurrentMissionItem() != 2 || completed.GetTotalMissionItems() != 2 {
		t.Fatalf("expected final progress 2/2, got %d/%d", completed.GetCurrentMissionItem(), completed.GetTotalMissionItems())
	}

	final := nextMissionStatus(t, outbound)
	if final.GetState() != missionExecutionStateCompleted {
		t.Fatalf("expected completed status, got %q", final.GetState())
	}

	hold := nextMissionStatus(t, outbound)
	if hold.GetState() != missionExecutionStateHold {
		t.Fatalf("expected hold status, got %q", hold.GetState())
	}
	if hold.GetCurrentMissionItem() != 2 || hold.GetTotalMissionItems() != 2 {
		t.Fatalf("expected hold progress 2/2, got %d/%d", hold.GetCurrentMissionItem(), hold.GetTotalMissionItems())
	}
}

func TestMissionExecutionProcessingKeyIncludesAction(t *testing.T) {
	uploadKey := missionExecutionProcessingKey(&pb.MissionExecutionEnvelope{
		ExecutionId: "mex-000001",
		Action:      missionActionUpload,
	})
	startKey := missionExecutionProcessingKey(&pb.MissionExecutionEnvelope{
		ExecutionId: "mex-000001",
		Action:      missionActionStart,
	})

	if uploadKey == startKey {
		t.Fatalf("expected upload and start to use different duplicate keys, got %q", uploadKey)
	}
}

func TestExecuteVehicleActionRoutesToGatewayMethod(t *testing.T) {
	tests := []struct {
		name       string
		actionType string
		wantCalls  []string
	}{
		{name: "arm", actionType: vehicleActionTypeArm, wantCalls: []string{"arm"}},
		{name: "takeoff", actionType: vehicleActionTypeTakeoff, wantCalls: []string{"takeoff"}},
		{name: "return to launch", actionType: vehicleActionTypeReturnToLaunch, wantCalls: []string{"return_to_launch"}},
		{name: "land", actionType: vehicleActionTypeLand, wantCalls: []string{"land"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gateway := &fakeGateway{}

			if err := executeVehicleAction(context.Background(), gateway, tt.actionType); err != nil {
				t.Fatalf("execute vehicle action: %v", err)
			}

			if !equalStrings(gateway.calls, tt.wantCalls) {
				t.Fatalf("expected gateway calls %v, got %v", tt.wantCalls, gateway.calls)
			}
		})
	}
}

func TestExecuteVehicleActionRejectsUnsupportedAction(t *testing.T) {
	err := executeVehicleAction(context.Background(), &fakeGateway{}, "orbit")
	if !errors.Is(err, errUnsupportedVehicleAction) {
		t.Fatalf("expected unsupported vehicle action error, got %v", err)
	}
}

func TestHeartbeatDropsWhenHeartbeatQueueIsFull(t *testing.T) {
	outbound := newOutboundQueues()

	for i := 0; i < cap(outbound.heartbeat); i++ {
		if !enqueueHeartbeat(context.Background(), outbound, testHeartbeatMessage("heartbeat")) {
			t.Fatalf("expected heartbeat enqueue %d", i)
		}
	}

	if enqueueHeartbeat(context.Background(), outbound, testHeartbeatMessage("dropped")) {
		t.Fatal("expected heartbeat to drop when heartbeat queue is full")
	}
}

func TestSendHeartbeatIncludesBackendChannelHealth(t *testing.T) {
	outbound := newOutboundQueues()
	manager := comms.NewBackendChannelManager("127.0.0.1:8080")
	manager.MarkConnected(time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC))

	sendHeartbeat(context.Background(), outbound, Config{
		VehicleAgentID:      "agent-001",
		VehicleAgentVersion: "v0.1.0",
	}, nil, manager)

	msg, ok := nextOutboundMessage(context.Background(), outbound)
	if !ok {
		t.Fatal("expected heartbeat message")
	}

	heartbeat := msg.GetHeartbeat()
	if heartbeat == nil {
		t.Fatalf("expected heartbeat payload, got %#v", msg)
	}

	health := heartbeat.GetBackendChannel()
	if health == nil {
		t.Fatal("expected backend channel health")
	}
	if health.GetState() != comms.StateConnected {
		t.Fatalf("expected connected state, got %q", health.GetState())
	}
	if health.GetBackendAddress() != "127.0.0.1:8080" {
		t.Fatalf("expected backend address, got %q", health.GetBackendAddress())
	}
	if health.GetWeakLink() {
		t.Fatal("expected connected channel not to be weak")
	}
}

func assertNextAgentID(t *testing.T, outbound outboundQueues, want string) {
	t.Helper()

	msg, ok := nextOutboundMessage(context.Background(), outbound)
	if !ok {
		t.Fatalf("expected message %q", want)
	}

	if msg.GetVehicleAgentId() != want {
		t.Fatalf("expected %q, got %q", want, msg.GetVehicleAgentId())
	}
}

func nextMissionStatus(t *testing.T, outbound outboundQueues) *pb.MissionExecutionStatus {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	msg, ok := nextOutboundMessage(ctx, outbound)
	if !ok {
		t.Fatal("expected mission execution status")
	}

	status := msg.GetMissionExecutionStatus()
	if status == nil {
		t.Fatalf("expected mission execution status, got %#v", msg)
	}

	return status
}

func testVehicleActionStatusMessage(agentID string) *pb.VehicleAgentToBackend {
	return &pb.VehicleAgentToBackend{
		VehicleAgentId: agentID,
		Payload: &pb.VehicleAgentToBackend_VehicleActionStatus{
			VehicleActionStatus: &pb.VehicleActionStatus{
				VehicleActionId: "act-000001",
				State:           vehicleActionStateVehicleAgentReceived,
			},
		},
	}
}

func testHeartbeatMessage(agentID string) *pb.VehicleAgentToBackend {
	return &pb.VehicleAgentToBackend{
		VehicleAgentId: agentID,
		Payload: &pb.VehicleAgentToBackend_Heartbeat{
			Heartbeat: &pb.Heartbeat{VehicleAgentVersion: "test"},
		},
	}
}

func testTelemetryMessage(agentID string) *pb.VehicleAgentToBackend {
	return &pb.VehicleAgentToBackend{
		VehicleAgentId: agentID,
		Payload: &pb.VehicleAgentToBackend_Telemetry{
			Telemetry: &pb.Telemetry{Source: "px4"},
		},
	}
}

type fakeGateway struct {
	uploadErr         error
	armErr            error
	takeoffErr        error
	startErr          error
	rtlErr            error
	progress          <-chan vehicle.MissionProgressEvent
	uploadedMissions  []vehicle.MissionPlan
	startMissionCalls int
	calls             []string
}

func (g *fakeGateway) Telemetry(ctx context.Context) (<-chan vehicle.TelemetryEvent, error) {
	ch := make(chan vehicle.TelemetryEvent)
	close(ch)
	return ch, nil
}

func (g *fakeGateway) Arm(ctx context.Context) error {
	g.calls = append(g.calls, "arm")
	return g.armErr
}

func (g *fakeGateway) Takeoff(ctx context.Context) error {
	g.calls = append(g.calls, "takeoff")
	return g.takeoffErr
}

func (g *fakeGateway) ReturnToLaunch(ctx context.Context) error {
	g.calls = append(g.calls, "return_to_launch")
	return g.rtlErr
}

func (g *fakeGateway) Land(ctx context.Context) error {
	g.calls = append(g.calls, "land")
	return nil
}

func (g *fakeGateway) UploadMission(ctx context.Context, mission vehicle.MissionPlan) error {
	g.uploadedMissions = append(g.uploadedMissions, mission)
	return g.uploadErr
}

func (g *fakeGateway) PrepareMissionStart(ctx context.Context) error {
	g.calls = append(g.calls, "arm")
	if g.armErr != nil {
		return g.armErr
	}

	g.calls = append(g.calls, "takeoff")
	if g.takeoffErr != nil {
		return g.takeoffErr
	}

	return nil
}

func (g *fakeGateway) StartMission(ctx context.Context) error {
	g.calls = append(g.calls, "start_mission")
	g.startMissionCalls++
	return g.startErr
}

func (g *fakeGateway) MissionProgress(ctx context.Context) (<-chan vehicle.MissionProgressEvent, error) {
	if g.progress != nil {
		return g.progress, nil
	}

	ch := make(chan vehicle.MissionProgressEvent)
	close(ch)
	return ch, nil
}

type fakeGimbalController struct {
	called  bool
	command mavlinkobserver.GimbalControlCommand
	err     error
}

func (f *fakeGimbalController) SendGimbalControl(_ context.Context, command mavlinkobserver.GimbalControlCommand) error {
	f.called = true
	f.command = command
	return f.err
}

func equalStrings(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}
