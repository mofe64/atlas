package vehicleagentchannel

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
	postgresrepo "github.com/sunnyside/atlas/atlas-backend/internal/repository/postgres"
	svc "github.com/sunnyside/atlas/atlas-backend/internal/services"
	"github.com/sunnyside/atlas/atlas-backend/internal/testutil"
	pb "github.com/sunnyside/atlas/atlas-backend/internal/transport/vehicleagentchannelpb/atlas"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestAgentChannelDispatchesCommandAndReceivesStatus(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	hub := NewHub(repo.dependencies(), slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewVehicleAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_Hello{
			Hello: &pb.VehicleAgentHello{
				DroneId:             "drone-001",
				VehicleAgentVersion: "0.1.0",
			},
		},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	waitFor(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return hub.connections["agent-001"] != nil
	})

	command, err := repo.IssueCommand(context.Background(), repository.RequestCommandInput{
		DroneID:     "drone-001",
		Type:        models.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok := hub.DispatchCommand(ctx, command); !ok {
		t.Fatal("expected command dispatch over connected stream")
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive command: %v", err)
	}

	if msg.GetCommand().GetCommandId() != command.ID {
		t.Fatalf("expected command %q, got %q", command.ID, msg.GetCommand().GetCommandId())
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_CommandStatus{
			CommandStatus: &pb.CommandStatus{
				CommandId: command.ID,
				State:     string(models.CommandStateVehicleAgentReceived),
			},
		},
	}); err != nil {
		t.Fatalf("send vehicle_agent_received: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_CommandStatus{
			CommandStatus: &pb.CommandStatus{
				CommandId: command.ID,
				State:     string(models.CommandStateVehicleAcked),
			},
		},
	}); err != nil {
		t.Fatalf("send vehicle_acked: %v", err)
	}

	waitFor(t, func() bool {
		updated, ok := repo.GetCommandByID(context.Background(), command.ID)
		return ok && updated.State == models.CommandStateVehicleAcked
	})
}

func TestAgentChannelRedeliversExpiredCommandOnConnect(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	now := time.Now().UTC().Add(-2 * models.CommandDeliveryLeaseDuration)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.IssueCommand(context.Background(), repository.RequestCommandInput{
		DroneID:     "drone-001",
		Type:        models.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	first, ok, err := repo.NextCommandForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second))
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if !ok {
		t.Fatal("expected first delivery")
	}

	if !first.LeaseUntil.Before(time.Now().UTC()) {
		t.Fatalf("expected expired lease, got %s", first.LeaseUntil)
	}

	hub := NewHub(repo.dependencies(), slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewVehicleAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_Hello{
			Hello: &pb.VehicleAgentHello{
				DroneId:             "drone-001",
				VehicleAgentVersion: "0.1.0",
			},
		},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive redelivered command: %v", err)
	}

	if msg.GetCommand().GetCommandId() != command.ID {
		t.Fatalf("expected redelivered command %q, got %q", command.ID, msg.GetCommand().GetCommandId())
	}

	redelivered, ok := repo.GetCommandByID(context.Background(), command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if redelivered.DeliveryAttempt != 2 {
		t.Fatalf("expected second delivery attempt, got %d", redelivered.DeliveryAttempt)
	}
}

func TestAgentChannelDispatchesMissionExecutionAndReceivesStatus(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, repo, now)

	hub := NewHub(repo.dependencies(), slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewVehicleAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_Hello{
			Hello: &pb.VehicleAgentHello{
				DroneId:             "drone-001",
				VehicleAgentVersion: "0.1.0",
			},
		},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	waitFor(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return hub.connections["agent-001"] != nil
	})

	execution, err := repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "operator-001",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, ok := hub.DispatchMissionExecution(ctx, execution); !ok {
		t.Fatal("expected mission execution dispatch over connected stream")
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive mission execution: %v", err)
	}

	envelope := msg.GetMissionExecution()
	if envelope.GetExecutionId() != execution.ID {
		t.Fatalf("expected execution %q, got %q", execution.ID, envelope.GetExecutionId())
	}

	if envelope.GetAction() != "upload" {
		t.Fatalf("expected upload action, got %q", envelope.GetAction())
	}

	if len(envelope.GetWaypoints()) != 1 {
		t.Fatalf("expected one waypoint, got %d", len(envelope.GetWaypoints()))
	}

	if envelope.GetCompletionAction() != string(models.MissionCompletionActionReturnToLaunch) {
		t.Fatalf("expected return_to_launch completion action, got %q", envelope.GetCompletionAction())
	}

	if envelope.GetWaypoints()[0].LoiterTimeS == nil || envelope.GetWaypoints()[0].GetLoiterTimeS() != 8 {
		t.Fatalf("expected waypoint loiter time 8, got %v", envelope.GetWaypoints()[0].LoiterTimeS)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_MissionExecutionStatus{
			MissionExecutionStatus: &pb.MissionExecutionStatus{
				ExecutionId: execution.ID,
				State:       string(models.MissionExecutionStateUploading),
			},
		},
	}); err != nil {
		t.Fatalf("send uploading: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_MissionExecutionStatus{
			MissionExecutionStatus: &pb.MissionExecutionStatus{
				ExecutionId:   execution.ID,
				State:         string(models.MissionExecutionStateUploadedToVehicle),
				ResultMessage: "uploaded to vehicle",
			},
		},
	}); err != nil {
		t.Fatalf("send uploaded_to_vehicle: %v", err)
	}

	waitFor(t, func() bool {
		executions, err := repo.ListMissionExecutions(context.Background(), mission.ID)
		return err == nil &&
			len(executions) == 1 &&
			executions[0].State == models.MissionExecutionStateUploadedToVehicle
	})
}

func TestAgentChannelDispatchesMissionStartAfterUploadDelivery(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, repo, now)

	hub := NewHub(repo.dependencies(), slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewVehicleAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_Hello{
			Hello: &pb.VehicleAgentHello{
				DroneId:             "drone-001",
				VehicleAgentVersion: "0.1.0",
			},
		},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	waitFor(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return hub.connections["agent-001"] != nil
	})

	execution, err := repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "upload-operator",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, ok := hub.DispatchMissionExecution(ctx, execution); !ok {
		t.Fatal("expected upload dispatch over connected stream")
	}

	uploadMsg, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive upload mission execution: %v", err)
	}
	if uploadMsg.GetMissionExecution().GetAction() != "upload" {
		t.Fatalf("expected upload action, got %q", uploadMsg.GetMissionExecution().GetAction())
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_MissionExecutionStatus{
			MissionExecutionStatus: &pb.MissionExecutionStatus{
				ExecutionId:   execution.ID,
				State:         string(models.MissionExecutionStateUploadedToVehicle),
				ResultMessage: "uploaded to vehicle",
			},
		},
	}); err != nil {
		t.Fatalf("send uploaded_to_vehicle: %v", err)
	}

	waitFor(t, func() bool {
		executions, err := repo.ListMissionExecutions(context.Background(), mission.ID)
		return err == nil &&
			len(executions) == 1 &&
			executions[0].State == models.MissionExecutionStateUploadedToVehicle
	})

	started, err := repo.RequestMissionStart(context.Background(), repository.RequestMissionStartInput{
		MissionID:   mission.ID,
		RequestedBy: "start-operator",
	}, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("request mission start: %v", err)
	}

	dispatched, ok := hub.DispatchMissionExecution(ctx, started)
	if !ok {
		t.Fatal("expected start dispatch over connected stream")
	}
	if dispatched.LastSentAt.IsZero() {
		t.Fatal("expected start dispatch to create a delivery lease")
	}

	startMsg, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive start mission execution: %v", err)
	}

	envelope := startMsg.GetMissionExecution()
	if envelope.GetExecutionId() != execution.ID {
		t.Fatalf("expected execution %q, got %q", execution.ID, envelope.GetExecutionId())
	}
	if envelope.GetAction() != "start" {
		t.Fatalf("expected start action, got %q", envelope.GetAction())
	}
	if envelope.GetRequestedBy() != "start-operator" {
		t.Fatalf("expected start requester, got %q", envelope.GetRequestedBy())
	}
}

func TestAgentChannelDeliversPendingMissionExecutionOnConnect(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	now := time.Now().UTC().Add(-2 * models.MissionExecutionDeliveryLeaseDuration)
	mission := createValidatedMission(t, repo, now)

	execution, err := repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	hub := NewHub(repo.dependencies(), slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewVehicleAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_Hello{
			Hello: &pb.VehicleAgentHello{
				DroneId:             "drone-001",
				VehicleAgentVersion: "0.1.0",
			},
		},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive pending mission execution: %v", err)
	}

	if msg.GetMissionExecution().GetExecutionId() != execution.ID {
		t.Fatalf("expected pending execution %q, got %#v", execution.ID, msg)
	}
}

func TestAgentChannelHelloRegistersAgentAndHeartbeatUpdatesStatus(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	hub := NewHub(repo.dependencies(), slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewVehicleAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_Hello{
			Hello: &pb.VehicleAgentHello{
				DroneId:             "drone-001",
				DroneName:           "Training Quad 1",
				VehicleAgentVersion: "0.1.0",
			},
		},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_Heartbeat{
			Heartbeat: &pb.Heartbeat{
				VehicleAgentVersion: "0.1.1",
			},
		},
	}); err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}

	waitFor(t, func() bool {
		drones := repo.ListDrones(context.Background(), time.Now().UTC())
		return len(drones) == 1 &&
			drones[0].ID == "drone-001" &&
			drones[0].Status == models.VehicleAgentStatusOnline &&
			drones[0].CommandChannel.State == models.CommandChannelConnected
	})

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close stream send: %v", err)
	}

	waitFor(t, func() bool {
		drones := repo.ListDrones(context.Background(), time.Now().UTC())
		return len(drones) == 1 &&
			drones[0].CommandChannel.State == models.CommandChannelDisconnected &&
			!drones[0].CommandChannel.LastDisconnectedAt.IsZero()
	})
}

func TestAgentChannelTelemetryUpdatesFleetSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	hub := NewHub(repo.dependencies(), slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewVehicleAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_Hello{
			Hello: &pb.VehicleAgentHello{
				DroneId:             "drone-001",
				DroneName:           "Training Quad 1",
				VehicleAgentVersion: "0.1.0",
			},
		},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_Telemetry{
			Telemetry: &pb.Telemetry{
				ObservedAt:        time.Now().UTC().Format(time.RFC3339Nano),
				BatteryPercent:    82,
				RelativeAltitudeM: 12.5,
				FlightMode:        "HOLD",
				Armed:             true,
				InAir:             false,
				Latitude:          51.5074,
				Longitude:         -0.1278,
				HeadingDeg:        91,
				GpsFix:            "3D",
				SatellitesVisible: 14,
				HomePositionSet:   true,
				Source:            "px4",
			},
		},
	}); err != nil {
		t.Fatalf("send telemetry: %v", err)
	}

	waitFor(t, func() bool {
		drones := repo.ListDrones(context.Background(), time.Now().UTC())
		return len(drones) == 1 &&
			drones[0].TelemetryState == models.TelemetryStateFresh &&
			drones[0].Telemetry.BatteryPercent == 82 &&
			drones[0].Telemetry.Source == "px4"
	})
}

func TestAgentChannelDispatchFallsBackWhenAgentDisconnected(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.IssueCommand(context.Background(), repository.RequestCommandInput{
		DroneID:     "drone-001",
		Type:        models.CommandTypeLand,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	hub := NewHub(repo.dependencies(), slog.Default())
	if _, ok := hub.DispatchCommand(context.Background(), command); ok {
		t.Fatal("expected dispatch to fail without connected vehicle-agent")
	}

	stored, ok := repo.GetCommandByID(context.Background(), command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if stored.State != models.CommandStateAuthorized {
		t.Fatalf("expected command to remain authorized for polling fallback, got %q", stored.State)
	}
}

func newTestServer(t *testing.T, hub *Hub) (*grpc.Server, func(context.Context, string) (net.Conn, error)) {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pb.RegisterVehicleAgentChannelServiceServer(server, NewServer(hub))

	go func() {
		if err := server.Serve(listener); err != nil {
			t.Logf("test gRPC server stopped: %v", err)
		}
	}()

	return server, func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}
}

func registerReadyVehicleAgent(t *testing.T, repo *testRepository, now time.Time) {
	t.Helper()

	repo.RegisterVehicleAgent(context.Background(), repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.1.0",
	}, now)

	if _, err := repo.RecordVehicleAgentHeartbeat(context.Background(), repository.VehicleAgentHeartbeatInput{
		VehicleAgentID:      "agent-001",
		VehicleAgentVersion: "0.1.0",
	}, now); err != nil {
		t.Fatalf("record heartbeat: %v", err)
	}

	if _, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID:  "agent-001",
		ObservedAt:      now,
		BatteryPercent:  82,
		FlightMode:      "HOLD",
		GPSFix:          "3D",
		HomePositionSet: true,
		Source:          "px4",
	}, now); err != nil {
		t.Fatalf("record telemetry: %v", err)
	}
}

func createValidatedMission(t *testing.T, repo *testRepository, now time.Time) models.Mission {
	t.Helper()

	registerReadyVehicleAgent(t, repo, now)

	loiterTime := 8.0
	mission, err := repo.CreateMission(context.Background(), repository.CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "Training loop",
		CreatedBy: "operator-001",
		Waypoints: []repository.MissionWaypointInput{
			{Latitude: 51.5074, Longitude: -0.1278, RelativeAltitudeM: 30, LoiterTimeS: &loiterTime},
		},
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	if mission.ValidationStatus != models.MissionValidationStatusValidated {
		t.Fatalf("expected validated mission, got %q with errors %#v", mission.ValidationStatus, mission.ValidationErrors)
	}

	return mission
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("condition was not met before deadline")
}

type testRepository struct {
	*svc.VehicleAgentService
	*svc.TelemetryService
	*svc.CommandService
	*svc.MissionService
	repos repository.Repositories
	deps  Dependencies
}

func (r *testRepository) dependencies() Dependencies {
	return r.deps
}

func (r *testRepository) ListDrones(ctx context.Context, now time.Time) []repository.DroneSnapshot {
	return r.repos.Drones.ListDrones(ctx, now)
}

func newTestRepository(t *testing.T) *testRepository {
	t.Helper()

	db, err := database.OpenPostgres(context.Background(), testutil.DatabaseURL(t))
	if err != nil {
		t.Fatalf("open postgres test db: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close postgres test db: %v", err)
		}
	})

	txManager := postgresrepo.NewTxManager(db)
	repos := txManager.Repositories()
	appServices := svc.New(svc.Dependencies{
		TxManager:    txManager,
		Repositories: repos,
	})

	return &testRepository{
		VehicleAgentService: appServices.VehicleAgents,
		TelemetryService:    appServices.Telemetry,
		CommandService:      appServices.Commands,
		MissionService:      appServices.Missions,
		repos:               repos,
		deps: Dependencies{
			VehicleAgents: appServices.VehicleAgents,
			Telemetry:     appServices.Telemetry,
			Commands:      appServices.Commands,
			Missions:      appServices.Missions,
		},
	}
}
