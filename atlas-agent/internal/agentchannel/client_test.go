package agentchannel

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/sunnyside/atlas/atlas-agent/internal/agentchannelpb/atlas"
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
	if msg.GetAgentId() != "new" {
		t.Fatalf("expected latest telemetry message, got %q", msg.GetAgentId())
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

	if !enqueueCritical(context.Background(), outbound, testCommandStatusMessage("critical")) {
		t.Fatal("expected critical command status enqueue")
	}

	msg, ok := nextOutboundMessage(context.Background(), outbound)
	if !ok {
		t.Fatal("expected outbound message")
	}

	if msg.GetAgentId() != "critical" {
		t.Fatalf("expected critical message first, got %q", msg.GetAgentId())
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
	if !enqueueCritical(context.Background(), outbound, testCommandStatusMessage("critical")) {
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

func TestHandleMissionUploadCallsGatewayAndReportsUploaded(t *testing.T) {
	outbound := newOutboundQueues()
	gateway := &fakeGateway{}

	speed := 6.5
	loiterTime := 12.0
	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{AgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
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

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{AgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
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

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{AgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
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

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{AgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
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

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{AgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
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

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{AgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
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

	outcome, err := handleMissionExecution(context.Background(), nil, outbound, Config{AgentID: "agent-001"}, gateway, &pb.MissionExecutionEnvelope{
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

func assertNextAgentID(t *testing.T, outbound outboundQueues, want string) {
	t.Helper()

	msg, ok := nextOutboundMessage(context.Background(), outbound)
	if !ok {
		t.Fatalf("expected message %q", want)
	}

	if msg.GetAgentId() != want {
		t.Fatalf("expected %q, got %q", want, msg.GetAgentId())
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

func testCommandStatusMessage(agentID string) *pb.AgentToBackend {
	return &pb.AgentToBackend{
		AgentId: agentID,
		Payload: &pb.AgentToBackend_CommandStatus{
			CommandStatus: &pb.CommandStatus{
				CommandId: "cmd-000001",
				State:     commandStateAgentReceived,
			},
		},
	}
}

func testHeartbeatMessage(agentID string) *pb.AgentToBackend {
	return &pb.AgentToBackend{
		AgentId: agentID,
		Payload: &pb.AgentToBackend_Heartbeat{
			Heartbeat: &pb.Heartbeat{AgentVersion: "test"},
		},
	}
}

func testTelemetryMessage(agentID string) *pb.AgentToBackend {
	return &pb.AgentToBackend{
		AgentId: agentID,
		Payload: &pb.AgentToBackend_Telemetry{
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
