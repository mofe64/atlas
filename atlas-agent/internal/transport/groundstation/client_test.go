package groundstation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
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

type evidenceCommandExecutor struct{}
type fakeMissionExecutor struct{ updates chan vehicle.MissionUpdate }
type fakeAircraftFollowExecutor struct {
	updates chan vehicle.AircraftFollowUpdate
}
type deadlineCommandExecutor struct{}

type recordingPerceptionGroundStation struct {
	pb.UnimplementedGroundStationServiceServer
	messages chan *pb.AgentPerception
}

func (fakeCommandExecutor) Execute(context.Context, string, string, string) (vehicle.CommandResult, error) {
	return vehicle.CommandResult{Code: "RESULT_SUCCESS", Message: "success"}, nil
}

func (evidenceCommandExecutor) Execute(context.Context, string, string, string) (vehicle.CommandResult, error) {
	return vehicle.CommandResult{Code: "TRACK_GEOLOCATION_ESTIMATED", Message: "estimated", EvidenceJSON: `{"schemaVersion":1}`}, nil
}
func (evidenceCommandExecutor) Capabilities() []string {
	return []string{"command:geolocate_selected_track"}
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

func (executor fakeMissionExecutor) Reconcile(ctx context.Context, reconciliation vehicle.MissionReconciliation) {
	executor.updates <- vehicle.MissionUpdate{
		EventID:     "reconciled-1",
		OperationID: reconciliation.ReconciliationID,
		RunID:       reconciliation.RunID,
		Type:        "reconciliation_accepted",
		State:       reconciliation.State,
		ObservedAt:  time.Now().UTC(),
	}
}
func (executor fakeMissionExecutor) Updates() <-chan vehicle.MissionUpdate { return executor.updates }
func (fakeMissionExecutor) Capabilities() []string                         { return []string{"mission:upload"} }
func (executor fakeAircraftFollowExecutor) Apply(context.Context, vehicle.AircraftFollowOperation) {
}
func (fakeAircraftFollowExecutor) GroundLinkLost() {}
func (executor fakeAircraftFollowExecutor) Updates() <-chan vehicle.AircraftFollowUpdate {
	return executor.updates
}
func (fakeAircraftFollowExecutor) Capabilities() []string {
	return []string{"aircraft_follow:standoff:v1:unverified"}
}

func TestAircraftFollowOperationPreservesReviewedEnvelopeAndExactTrack(t *testing.T) {
	request := &pb.AircraftFollowControlRequest{
		OperationId: "operation-1", FollowSessionId: "follow-1", DroneId: "drone-1",
		Action: pb.AircraftFollowControlAction_AIRCRAFT_FOLLOW_CONTROL_ACTION_RENEW,
		Envelope: &pb.AircraftFollowEnvelope{
			StandoffM: 40, AltitudeRelativeM: 30, MinimumAltitudeRelativeM: 20,
			MaximumAltitudeRelativeM: 45, MaximumGroundSpeedMS: 8,
			MaximumAccelerationMS2: 1.5, MaximumDurationMs: 300_000,
			BoundaryCenterLatitude: 51.5, BoundaryCenterLongitude: -0.14,
			BoundaryRadiusM: 500, MinimumBatteryPercent: 30,
			MinimumTrackConfidence: 0.7, MaximumGeolocationUncertaintyM: 20,
			MaximumVelocityUncertaintyMS: 5,
		},
		Target: &pb.AircraftFollowTargetState{
			GeolocationId: "geo-1", SelectionId: "selection-1", SourceId: "a8-main",
			TrackSessionId: "track-session-1", TrackId: "track-1", ObservedAtUnixMs: 1_000,
			Latitude: 51.5002, Longitude: -0.14, AltitudeAmslM: 80,
			VelocityNorthMS: 1.2, VelocityEastMS: 0.1,
			HorizontalUncertaintyM: 4, VelocityUncertaintyMS: 0.8,
			TrackConfidence: 0.92, LifecycleState: "ACTIVE", MotionStatus: "FILTERED",
		},
		OperatorLeaseExpiresAtUnixMs: 4_000,
		ValidationReference:          "sitl-hil-flight/accepted-1",
	}
	operation, err := aircraftFollowOperation(request)
	if err != nil {
		t.Fatalf("translate follow operation: %v", err)
	}
	if operation.Action != "renew" || operation.SessionID != "follow-1" || operation.DroneID != "drone-1" {
		t.Fatalf("operation identity = %#v", operation)
	}
	if operation.Envelope.StandoffM != 40 || operation.Envelope.MaximumAccelerationMPS2 != 1.5 || operation.Envelope.MaximumDuration != 300*time.Second {
		t.Fatalf("reviewed envelope = %#v", operation.Envelope)
	}
	if operation.Target.SelectionID != "selection-1" || operation.Target.TrackSessionID != "track-session-1" || operation.Target.TrackID != "track-1" || operation.Target.MotionStatus != "FILTERED" {
		t.Fatalf("exact target = %#v", operation.Target)
	}
	if operation.ValidationReference != "sitl-hil-flight/accepted-1" {
		t.Fatalf("validation reference = %q", operation.ValidationReference)
	}
}

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
		followExecutor := fakeAircraftFollowExecutor{updates: make(chan vehicle.AircraftFollowUpdate)}
		done <- connect(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), config.Config{
			GroundStationAddress: listener.Addr().String(),
			DroneName:            "Test Drone",
			AgentVersion:         "test",
			ProtocolVersion:      "1",
			HeartbeatInterval:    10 * time.Millisecond,
			VehicleType:          "multicopter",
		}, identity.Identity{InstallationID: "agent-1", DroneID: "drone-1"}, telemetryUpdates, statusTexts, perception.Outputs{}, fakeCommandExecutor{}, missionExecutor, followExecutor, newFrameDemand())
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
			PerceptionProvider: "deepstream", TrackerAlgorithm: "byte_track_cmc",
		}, identity.Identity{InstallationID: "agent-1", DroneID: "drone-1"}, "session-1", perception.Outputs{Frames: frames, Health: health}, demand)
	}()

	registration := <-recorder.messages
	if got := registration.GetRegistration(); got == nil || got.GetProvider() != "deepstream" || got.GetInstallationId() != "agent-1" {
		t.Fatalf("perception registration = %#v", got)
	} else if !slices.Contains(got.GetCapabilities(), "tracker:byte_track_cmc:atlas:v1") || !slices.Contains(got.GetCapabilities(), "camera_motion:sparse_optical_flow:v1") || !slices.Contains(got.GetCapabilities(), "reid:disabled") {
		t.Fatalf("perception capabilities = %#v", got.GetCapabilities())
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

func TestPerceptionHealthCarriesTrackingSessionEvidence(t *testing.T) {
	message := perceptionHealthMessage("session-1", "drone-1", perception.Health{
		SourceID: "a8-main", Provider: "hailo", ObservedAt: time.Now().UTC(),
		Tracking: &perception.TrackingHealth{
			Algorithm: perception.TrackerAlgorithmByteTrackCMC, State: "ACTIVE",
			SessionID: "tracking-session-1", LastResetReason: perception.TrackingResetActivated,
			ResetCount: 2, CameraMotionState: "ACTIVE", CameraMotionMethod: "SPARSE_OPTICAL_FLOW",
			CameraMotionConfidence: 0.92,
		},
	})
	tracking := message.GetHealth().GetTracking()
	if tracking.GetAlgorithm() != "BYTE_TRACK_CMC" || tracking.GetSessionId() != "tracking-session-1" || tracking.GetResetCount() != 2 || tracking.GetCameraMotionState() != "ACTIVE" || tracking.GetReIdEnabled() {
		t.Fatalf("tracking health = %#v", tracking)
	}
}

func TestPerceptionTrackLifecycleMessageCarriesDurableSummary(t *testing.T) {
	observedAt := time.Now().UTC().Truncate(time.Millisecond)
	predicted := perception.BoundingBox{X: 0.12, Y: 0.2, Width: 0.1, Height: 0.2}
	message := perceptionTrackUpdateMessage("session-1", "drone-1", perception.TrackUpdateBatch{
		SourceID: "a8-main", StreamEpoch: "epoch-1", TrackSessionID: "track-session-1",
		TrackerType: perception.TrackerAlgorithmByteTrackCMC, ObservedAt: observedAt,
		SessionStarted: true, CurrentVisible: 1, UniqueConfirmed: 4,
		Tracks: []perception.TrackSnapshot{{
			TrackID: "atlas:track-session-1:1", TrackSessionID: "track-session-1",
			TrackerType:    perception.TrackerAlgorithmByteTrackCMC,
			LifecycleState: perception.TrackLifecycleTemporarilyOccluded,
			Revision:       3, AgeFrames: 4, ObservationCount: 3,
			FirstObservedAt: observedAt.Add(-time.Second), LastObservedAt: observedAt.Add(-100 * time.Millisecond),
			LatestConfirmedBox:        perception.BoundingBox{X: 0.1, Y: 0.2, Width: 0.1, Height: 0.2},
			LatestDetectionConfidence: 0.9, PredictedBox: &predicted, PredictionConfidence: 0.72,
			ClassID: 0, ClassLabel: "person", UpdateReason: perception.TrackUpdateStateChanged,
		}},
		RuleCounts: []perception.TrackRuleCount{{
			RuleID: "gate", RuleRevision: 2, RuleType: perception.CountingRuleLine, LineForward: 3, LineReverse: 1,
		}},
		CountEvents: []perception.TrackCountEvent{{
			EventID: "event-1", RuleID: "gate", RuleRevision: 2,
			TrackSessionID: "track-session-1", TrackID: "atlas:track-session-1:1",
			EventType: perception.TrackCountLineForward, ObservedAt: observedAt,
			Anchor: perception.NormalizedPoint{X: .5, Y: .4},
		}},
	})
	batch := message.GetTrackUpdates()
	if batch.GetTrackSessionId() != "track-session-1" || !batch.GetSessionStarted() || batch.GetTrackerType() != "BYTE_TRACK_CMC" || len(batch.GetTracks()) != 1 {
		t.Fatalf("track batch = %#v", batch)
	}
	track := batch.GetTracks()[0]
	if track.GetLifecycleState() != "TEMPORARILY_OCCLUDED" || track.GetRevision() != 3 || track.GetPredictedBox() == nil || track.GetPredictionConfidence() != 0.72 || track.GetFirstObservedAtUnixMs() != observedAt.Add(-time.Second).UnixMilli() {
		t.Fatalf("track snapshot = %#v", track)
	}
	if batch.GetCurrentVisible() != 1 || batch.GetUniqueConfirmed() != 4 || len(batch.GetRuleCounts()) != 1 || batch.GetRuleCounts()[0].GetLineForward() != 3 || len(batch.GetCountEvents()) != 1 || batch.GetCountEvents()[0].GetAnchor().GetX() != .5 {
		t.Fatalf("track count payload = %#v", batch)
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

func TestExecuteCommandRetainsStructuredEvidenceForNative(t *testing.T) {
	updates := make(chan commandExecutionUpdate, 1)
	command := &pb.VehicleCommandRequest{
		CommandId:        "command-evidence",
		DeadlineAtUnixMs: time.Now().Add(time.Second).UnixMilli(),
	}
	executeCommand(context.Background(), command, "geolocate_selected_track", evidenceCommandExecutor{}, updates)
	update := <-updates
	if update.updateType != pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_SUCCEEDED || update.evidenceJSON != `{"schemaVersion":1}` {
		t.Fatalf("evidence update = %#v", update)
	}
}
