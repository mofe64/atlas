package groundstation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/config"
	"github.com/sunnyside/atlas/atlas-agent/internal/identity"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
	pb "github.com/sunnyside/atlas/atlas-agent/internal/transport/groundstationpb"
	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
	"google.golang.org/grpc"
)

type recordingGroundStation struct {
	pb.UnimplementedGroundStationServiceServer
	registration   chan *pb.AgentToGroundStation
	heartbeat      chan *pb.AgentToGroundStation
	telemetry      chan *pb.AgentToGroundStation
	statusText     chan *pb.AgentToGroundStation
	commandUpdates chan *pb.VehicleCommandUpdate
	missionUpdates chan *pb.MissionRunUpdate
}

type fakeCommandExecutor struct{}
type fakeMissionExecutor struct{ updates chan vehicle.MissionUpdate }
type deadlineCommandExecutor struct{}

type recordingPerceptionGroundStation struct {
	pb.UnimplementedGroundStationServiceServer
	messages chan *pb.AgentPerception
}

func (fakeCommandExecutor) Execute(context.Context, string, string, string) (vehicle.CommandResult, error) {
	return vehicle.CommandResult{Code: "RESULT_SUCCESS", Message: "success"}, nil
}
func (fakeCommandExecutor) Capabilities() []string { return []string{"command:hold"} }
func (deadlineCommandExecutor) Execute(ctx context.Context, _, _, _ string) (vehicle.CommandResult, error) {
	<-ctx.Done()
	return vehicle.CommandResult{}, ctx.Err()
}
func (deadlineCommandExecutor) Capabilities() []string { return []string{"command:hold"} }
func (executor fakeMissionExecutor) Execute(ctx context.Context, operation vehicle.MissionOperation) {
	progress := 100.0
	for _, update := range []vehicle.MissionUpdate{
		{EventID: "mission-accepted", OperationID: operation.OperationID, RunID: operation.RunID, Type: "operation_accepted", State: "UPLOADING", ObservedAt: time.Now().UTC(), Message: "accepted"},
		{EventID: "mission-uploaded", OperationID: operation.OperationID, RunID: operation.RunID, Type: "uploaded", State: "READY", ObservedAt: time.Now().UTC(), Progress: &progress, Message: "uploaded"},
	} {
		select {
		case executor.updates <- update:
		case <-ctx.Done():
			return
		}
	}
}
func (executor fakeMissionExecutor) Updates() <-chan vehicle.MissionUpdate { return executor.updates }
func (fakeMissionExecutor) Capabilities() []string                         { return []string{"mission:upload"} }

func (s *recordingGroundStation) OpenSession(stream pb.GroundStationService_OpenSessionServer) error {
	registration, err := stream.Recv()
	if err != nil {
		return err
	}
	s.registration <- registration
	if err := stream.Send(&pb.GroundStationToAgent{
		Payload: &pb.GroundStationToAgent_RegistrationAccepted{
			RegistrationAccepted: &pb.RegistrationAccepted{
				AgentId:             registration.GetRegistration().GetInstallationId(),
				DroneId:             registration.GetRegistration().GetDrone().GetDroneId(),
				BindingId:           "binding-1",
				CommunicationLinkId: "link-1",
			},
		},
	}); err != nil {
		return err
	}
	for range 3 {
		message, err := stream.Recv()
		if err != nil {
			return err
		}
		switch {
		case message.GetHeartbeat() != nil:
			s.heartbeat <- message
		case message.GetTelemetry() != nil:
			s.telemetry <- message
		case message.GetStatusText() != nil:
			s.statusText <- message
		default:
			return errors.New("unexpected session message")
		}
	}
	if err := stream.Send(&pb.GroundStationToAgent{
		Payload: &pb.GroundStationToAgent_CommandRequest{CommandRequest: &pb.VehicleCommandRequest{
			CommandId: "command-1", DroneId: "drone-1",
			CommandType:      pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_HOLD,
			DeadlineAtUnixMs: time.Now().Add(time.Second).UnixMilli(),
		}},
	}); err != nil {
		return err
	}
	for received := 0; received < 3; {
		message, err := stream.Recv()
		if err != nil {
			return err
		}
		if message.GetCommandUpdate() != nil {
			s.commandUpdates <- message.GetCommandUpdate()
			received++
		}
	}
	if err := stream.Send(&pb.GroundStationToAgent{
		Payload: &pb.GroundStationToAgent_MissionOperationRequest{MissionOperationRequest: &pb.MissionOperationRequest{
			OperationId: "operation-1", MissionRunId: "run-1", DroneId: "drone-1",
			OperationType:    pb.MissionOperationType_MISSION_OPERATION_TYPE_UPLOAD,
			MissionPlanJson:  `{"generatedWaypoints":[{"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25}],"actions":[]}`,
			DeadlineAtUnixMs: time.Now().Add(time.Second).UnixMilli(),
		}},
	}); err != nil {
		return err
	}
	for range 2 {
		message, err := stream.Recv()
		if err != nil {
			return err
		}
		if message.GetMissionRunUpdate() == nil {
			return errors.New("expected mission run update")
		}
		s.missionUpdates <- message.GetMissionRunUpdate()
	}
	return nil
}

func (s *recordingPerceptionGroundStation) OpenPerceptionStream(stream pb.GroundStationService_OpenPerceptionStreamServer) error {
	registration, err := stream.Recv()
	if err != nil {
		return err
	}
	s.messages <- registration
	if err := stream.Send(&pb.GroundStationPerception{
		Payload: &pb.GroundStationPerception_StreamAccepted{StreamAccepted: &pb.PerceptionStreamAccepted{
			StreamId:         registration.GetRegistration().GetStreamId(),
			AcceptedAtUnixMs: time.Now().UTC().UnixMilli(),
		}},
	}); err != nil {
		return err
	}
	for range 2 {
		message, err := stream.Recv()
		if err != nil {
			return err
		}
		s.messages <- message
	}
	return nil
}

func TestConnectRegistersAndSendsHeartbeat(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	recorder := &recordingGroundStation{
		registration:   make(chan *pb.AgentToGroundStation, 1),
		heartbeat:      make(chan *pb.AgentToGroundStation, 1),
		telemetry:      make(chan *pb.AgentToGroundStation, 1),
		statusText:     make(chan *pb.AgentToGroundStation, 1),
		commandUpdates: make(chan *pb.VehicleCommandUpdate, 3),
		missionUpdates: make(chan *pb.MissionRunUpdate, 2),
	}
	pb.RegisterGroundStationServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		battery := 82.5
		voltage := 22.8
		mode := "HOLD"
		telemetryUpdates := make(chan telemetry.Snapshot, 1)
		statusTexts := make(chan telemetry.StatusTextEvent, 1)
		telemetryUpdates <- telemetry.Snapshot{
			ObservedAt:     time.Now().UTC(),
			Source:         "mavsdk",
			BatteryPercent: &battery,
			FlightMode:     &mode,
			Batteries: []telemetry.Battery{{
				ID: 0, Function: "ALL", RemainingPercent: &battery, VoltageV: &voltage,
			}},
			Health: &telemetry.VehicleHealth{Armable: true},
		}
		statusTexts <- telemetry.StatusTextEvent{
			ObservedAt: time.Now().UTC(), Source: "mavsdk", Severity: "WARNING", Text: "Battery temperature high",
		}
		missionExecutor := fakeMissionExecutor{updates: make(chan vehicle.MissionUpdate, 2)}
		done <- connect(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), config.Config{
			GroundStationAddress: listener.Addr().String(),
			DroneName:            "Test Drone",
			AgentVersion:         "test",
			ProtocolVersion:      "1",
			HeartbeatInterval:    10 * time.Millisecond,
			VehicleType:          "multicopter",
		}, identity.Identity{InstallationID: "agent-1", DroneID: "drone-1"}, telemetryUpdates, statusTexts, perception.Outputs{}, fakeCommandExecutor{}, missionExecutor, newFrameDemand())
	}()

	select {
	case registration := <-recorder.registration:
		if registration.GetRegistration().GetInstallationId() != "agent-1" || registration.GetSessionId() == "" {
			t.Fatalf("registration = %#v", registration)
		}
	case <-ctx.Done():
		t.Fatal("registration was not received")
	}
	select {
	case heartbeat := <-recorder.heartbeat:
		if heartbeat.GetHeartbeat() == nil {
			t.Fatalf("heartbeat = %#v", heartbeat)
		}
	case <-ctx.Done():
		t.Fatal("heartbeat was not received")
	}
	select {
	case message := <-recorder.telemetry:
		if got := message.GetTelemetry(); got == nil || got.BatteryPercent == nil || got.GetBatteryPercent() != 82.5 || got.GetFlightMode() != "HOLD" || len(got.GetBatteries()) != 1 || got.GetBatteries()[0].GetVoltageV() != 22.8 || !got.GetHealth().GetArmable() {
			t.Fatalf("telemetry = %#v", got)
		}
	case <-ctx.Done():
		t.Fatal("telemetry was not received")
	}
	select {
	case message := <-recorder.statusText:
		if got := message.GetStatusText(); got == nil || got.GetSeverity() != "WARNING" || got.GetText() != "Battery temperature high" {
			t.Fatalf("status text = %#v", got)
		}
	case <-ctx.Done():
		t.Fatal("status text was not received")
	}
	wantUpdates := []pb.VehicleCommandUpdateType{
		pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_ACCEPTED,
		pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_EXECUTING,
		pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_SUCCEEDED,
	}
	for _, want := range wantUpdates {
		select {
		case update := <-recorder.commandUpdates:
			if update.GetCommandId() != "command-1" || update.GetUpdateType() != want {
				t.Fatalf("command update = %#v, want %s", update, want)
			}
		case <-ctx.Done():
			t.Fatalf("command update %s was not received", want)
		}
	}
	wantMissionUpdates := []pb.MissionRunUpdateType{
		pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_OPERATION_ACCEPTED,
		pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_UPLOADED,
	}
	for _, want := range wantMissionUpdates {
		select {
		case update := <-recorder.missionUpdates:
			if update.GetMissionRunId() != "run-1" || update.GetUpdateType() != want {
				t.Fatalf("mission update = %#v, want %s", update, want)
			}
		case <-ctx.Done():
			t.Fatalf("mission update %s was not received", want)
		}
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("client did not observe server stream closure")
	}
}

func TestParseTotalMemoryBytes(t *testing.T) {
	const meminfo = "MemTotal:       16384256 kB\nMemFree:         123456 kB\n"
	if got, want := parseTotalMemoryBytes(meminfo), uint64(16384256*1024); got != want {
		t.Fatalf("parseTotalMemoryBytes() = %d, want %d", got, want)
	}
}

func TestPerceptionUsesIndependentHardwareNeutralStream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	recorder := &recordingPerceptionGroundStation{messages: make(chan *pb.AgentPerception, 3)}
	pb.RegisterGroundStationServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer connection.Close()
	frames := make(chan perception.Frame, 1)
	health := make(chan perception.Health, 1)
	now := time.Now().UTC()
	frames <- perception.Frame{
		SourceID: "a8-main", StreamEpoch: "epoch-1", FrameID: "frame-42",
		ObservedAt: now, SourcePTSNS: 42_000, ImageWidth: 1920, ImageHeight: 1080,
		Model:              perception.ModelIdentity{Name: "atlas-objects", Version: "1", ArtifactHash: "sha256:test"},
		InferenceLatencyMS: 8.5,
		Detections: []perception.Detection{{
			TrackID: "track-7", ClassID: 0, ClassLabel: "person", Confidence: 0.91,
			BoundingBox: perception.BoundingBox{X: 0.1, Y: 0.2, Width: 0.3, Height: 0.4},
		}},
	}
	health <- perception.Health{
		SourceID: "a8-main", Provider: "deepstream", Accelerator: "jetson-orin",
		InputConnected: true, InferenceReady: true, OutputPublishing: true,
		InputFPS: 30, InferenceFPS: 20, LastFrameAt: now, ObservedAt: now,
		Model: perception.ModelIdentity{Name: "atlas-objects", Version: "1"},
	}
	done := make(chan error, 1)
	demand := newFrameDemand()
	if err := demand.applySubscription(&pb.PerceptionFrameSubscription{
		SubscriptionId:  "view-1",
		Purpose:         "live_view",
		Action:          pb.PerceptionFrameSubscriptionAction_PERCEPTION_FRAME_SUBSCRIPTION_ACTION_START_OR_RENEW,
		LeaseDurationMs: 7_000,
	}, now); err != nil {
		t.Fatalf("start frame demand: %v", err)
	}
	go func() {
		done <- streamPerception(ctx, pb.NewGroundStationServiceClient(connection), config.Config{
			PerceptionProvider: "deepstream",
		}, identity.Identity{InstallationID: "agent-1", DroneID: "drone-1"}, "session-1", perception.Outputs{Frames: frames, Health: health}, demand)
	}()

	registration := <-recorder.messages
	if got := registration.GetRegistration(); got == nil || got.GetProvider() != "deepstream" || got.GetInstallationId() != "agent-1" {
		t.Fatalf("perception registration = %#v", got)
	}
	seenFrame := false
	seenHealth := false
	for range 2 {
		message := <-recorder.messages
		if frame := message.GetFrame(); frame != nil {
			seenFrame = frame.GetFrameId() == "frame-42" && frame.GetDetections()[0].GetClassLabel() == "person"
		}
		if update := message.GetHealth(); update != nil {
			seenHealth = update.GetProvider() == "deepstream" && update.GetInferenceReady()
		}
	}
	if !seenFrame || !seenHealth {
		t.Fatalf("seen frame=%v health=%v", seenFrame, seenHealth)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("perception stream did not observe server closure")
	}
}

func TestPerceptionHealthOmitsUnknownModel(t *testing.T) {
	message := perceptionHealthMessage("session-1", "drone-1", perception.Health{
		SourceID: "a8-main", Provider: "hailo", ObservedAt: time.Now().UTC(),
	})
	if message.GetHealth().GetModel() != nil {
		t.Fatalf("health model = %#v, want nil", message.GetHealth().GetModel())
	}
}

func TestExecuteCommandReportsMAVSDKDeadline(t *testing.T) {
	updates := make(chan commandExecutionUpdate, 1)
	command := &pb.VehicleCommandRequest{
		CommandId:        "command-timeout",
		DeadlineAtUnixMs: time.Now().Add(20 * time.Millisecond).UnixMilli(),
	}
	executeCommand(context.Background(), command, "hold", deadlineCommandExecutor{}, updates)
	update := <-updates
	if update.updateType != pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_TIMED_OUT {
		t.Fatalf("update type = %s, want timed out", update.updateType)
	}
	if update.resultCode != "MAVSDK_DEADLINE_EXCEEDED" {
		t.Fatalf("result code = %q", update.resultCode)
	}
}
