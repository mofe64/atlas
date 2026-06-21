package agentchannel

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	pb "github.com/sunnyside/atlas/atlas-backend/internal/agentchannelpb/atlas"
	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestAgentChannelDispatchesCommandAndReceivesStatus(t *testing.T) {
	ctx := context.Background()
	reg := registry.NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	hub := NewHub(reg, slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_Hello{
			Hello: &pb.AgentHello{
				DroneId:      "drone-001",
				AgentVersion: "0.1.0",
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

	command, err := reg.RequestCommand(registry.RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
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

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_CommandStatus{
			CommandStatus: &pb.CommandStatus{
				CommandId: command.ID,
				State:     string(domain.CommandStateAgentReceived),
			},
		},
	}); err != nil {
		t.Fatalf("send agent_received: %v", err)
	}

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_CommandStatus{
			CommandStatus: &pb.CommandStatus{
				CommandId: command.ID,
				State:     string(domain.CommandStateVehicleAcked),
			},
		},
	}); err != nil {
		t.Fatalf("send vehicle_acked: %v", err)
	}

	waitFor(t, func() bool {
		updated, ok := reg.CommandByID(command.ID)
		return ok && updated.State == domain.CommandStateVehicleAcked
	})
}

func TestAgentChannelRedeliversExpiredCommandOnConnect(t *testing.T) {
	ctx := context.Background()
	reg := registry.NewMemoryRegistry()
	now := time.Now().UTC().Add(-2 * domain.CommandDeliveryLeaseDuration)
	registerReadyAgent(t, reg, now)

	command, err := reg.RequestCommand(registry.RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	first, ok, err := reg.NextCommandForAgent("agent-001", now.Add(time.Second))
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if !ok {
		t.Fatal("expected first delivery")
	}

	if !first.LeaseUntil.Before(time.Now().UTC()) {
		t.Fatalf("expected expired lease, got %s", first.LeaseUntil)
	}

	hub := NewHub(reg, slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_Hello{
			Hello: &pb.AgentHello{
				DroneId:      "drone-001",
				AgentVersion: "0.1.0",
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

	redelivered, ok := reg.CommandByID(command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if redelivered.DeliveryAttempt != 2 {
		t.Fatalf("expected second delivery attempt, got %d", redelivered.DeliveryAttempt)
	}
}

func TestAgentChannelDispatchesMissionExecutionAndReceivesStatus(t *testing.T) {
	ctx := context.Background()
	reg := registry.NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, reg, now)

	hub := NewHub(reg, slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_Hello{
			Hello: &pb.AgentHello{
				DroneId:      "drone-001",
				AgentVersion: "0.1.0",
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

	execution, err := reg.RequestMissionUpload(registry.RequestMissionUploadInput{
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

	if envelope.GetCompletionAction() != string(domain.MissionCompletionActionReturnToLaunch) {
		t.Fatalf("expected return_to_launch completion action, got %q", envelope.GetCompletionAction())
	}

	if envelope.GetWaypoints()[0].LoiterTimeS == nil || envelope.GetWaypoints()[0].GetLoiterTimeS() != 8 {
		t.Fatalf("expected waypoint loiter time 8, got %v", envelope.GetWaypoints()[0].LoiterTimeS)
	}

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_MissionExecutionStatus{
			MissionExecutionStatus: &pb.MissionExecutionStatus{
				ExecutionId: execution.ID,
				State:       string(domain.MissionExecutionStateUploading),
			},
		},
	}); err != nil {
		t.Fatalf("send uploading: %v", err)
	}

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_MissionExecutionStatus{
			MissionExecutionStatus: &pb.MissionExecutionStatus{
				ExecutionId:   execution.ID,
				State:         string(domain.MissionExecutionStateUploadedToVehicle),
				ResultMessage: "uploaded to vehicle",
			},
		},
	}); err != nil {
		t.Fatalf("send uploaded_to_vehicle: %v", err)
	}

	waitFor(t, func() bool {
		executions, err := reg.ListMissionExecutions(mission.ID)
		return err == nil &&
			len(executions) == 1 &&
			executions[0].State == domain.MissionExecutionStateUploadedToVehicle
	})
}

func TestAgentChannelDeliversPendingMissionExecutionOnConnect(t *testing.T) {
	ctx := context.Background()
	reg := registry.NewMemoryRegistry()
	now := time.Now().UTC().Add(-2 * domain.MissionExecutionDeliveryLeaseDuration)
	mission := createValidatedMission(t, reg, now)

	execution, err := reg.RequestMissionUpload(registry.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	hub := NewHub(reg, slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_Hello{
			Hello: &pb.AgentHello{
				DroneId:      "drone-001",
				AgentVersion: "0.1.0",
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
	reg := registry.NewMemoryRegistry()
	hub := NewHub(reg, slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_Hello{
			Hello: &pb.AgentHello{
				DroneId:      "drone-001",
				DroneName:    "Training Quad 1",
				AgentVersion: "0.1.0",
			},
		},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_Heartbeat{
			Heartbeat: &pb.Heartbeat{
				AgentVersion: "0.1.1",
			},
		},
	}); err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}

	waitFor(t, func() bool {
		drones := reg.ListDrones(time.Now().UTC())
		return len(drones) == 1 &&
			drones[0].ID == "drone-001" &&
			drones[0].Status == domain.AgentStatusOnline &&
			drones[0].CommandChannel.State == domain.CommandChannelConnected
	})

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close stream send: %v", err)
	}

	waitFor(t, func() bool {
		drones := reg.ListDrones(time.Now().UTC())
		return len(drones) == 1 &&
			drones[0].CommandChannel.State == domain.CommandChannelDisconnected &&
			!drones[0].CommandChannel.LastDisconnectedAt.IsZero()
	})
}

func TestAgentChannelTelemetryUpdatesFleetSnapshot(t *testing.T) {
	ctx := context.Background()
	reg := registry.NewMemoryRegistry()
	hub := NewHub(reg, slog.Default())
	grpcServer, dialer := newTestServer(t, hub)
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer conn.Close()

	stream, err := pb.NewAgentChannelServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("connect stream: %v", err)
	}

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_Hello{
			Hello: &pb.AgentHello{
				DroneId:      "drone-001",
				DroneName:    "Training Quad 1",
				AgentVersion: "0.1.0",
			},
		},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	if err := stream.Send(&pb.AgentToBackend{
		AgentId: "agent-001",
		Payload: &pb.AgentToBackend_Telemetry{
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
		drones := reg.ListDrones(time.Now().UTC())
		return len(drones) == 1 &&
			drones[0].TelemetryState == domain.TelemetryStateFresh &&
			drones[0].Telemetry.BatteryPercent == 82 &&
			drones[0].Telemetry.Source == "px4"
	})
}

func TestAgentChannelDispatchFallsBackWhenAgentDisconnected(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	command, err := reg.RequestCommand(registry.RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeLand,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	hub := NewHub(reg, slog.Default())
	if _, ok := hub.DispatchCommand(context.Background(), command); ok {
		t.Fatal("expected dispatch to fail without connected agent")
	}

	stored, ok := reg.CommandByID(command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if stored.State != domain.CommandStateAuthorized {
		t.Fatalf("expected command to remain authorized for polling fallback, got %q", stored.State)
	}
}

func newTestServer(t *testing.T, hub *Hub) (*grpc.Server, func(context.Context, string) (net.Conn, error)) {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pb.RegisterAgentChannelServiceServer(server, NewServer(hub))

	go func() {
		if err := server.Serve(listener); err != nil {
			t.Logf("test gRPC server stopped: %v", err)
		}
	}()

	return server, func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}
}

func registerReadyAgent(t *testing.T, reg *registry.MemoryRegistry, now time.Time) {
	t.Helper()

	reg.RegisterAgent(registry.RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	if _, err := reg.RecordHeartbeat(registry.HeartbeatInput{
		AgentID:      "agent-001",
		AgentVersion: "0.1.0",
	}, now); err != nil {
		t.Fatalf("record heartbeat: %v", err)
	}

	if _, err := reg.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:         "agent-001",
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

func createValidatedMission(t *testing.T, reg *registry.MemoryRegistry, now time.Time) domain.Mission {
	t.Helper()

	registerReadyAgent(t, reg, now)

	loiterTime := 8.0
	mission, err := reg.CreateMission(registry.CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "Training loop",
		CreatedBy: "operator-001",
		Waypoints: []registry.MissionWaypointInput{
			{Latitude: 51.5074, Longitude: -0.1278, RelativeAltitudeM: 30, LoiterTimeS: &loiterTime},
		},
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	if mission.ValidationStatus != domain.MissionValidationStatusValidated {
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
