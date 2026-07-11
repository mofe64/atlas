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

func TestBackendChannelHealthToMap(t *testing.T) {
	raw := backendChannelHealthToMap(&pb.BackendChannelHealth{
		State:                "connected",
		ReconnectCount:       3,
		ConnectedAt:          "2026-07-09T10:00:00Z",
		LastDisconnectedAt:   "2026-07-09T09:59:00Z",
		LastSuccessfulSendAt: "2026-07-09T10:00:05Z",
		LastHeartbeatSentAt:  "2026-07-09T10:00:05Z",
		LastError:            "",
		BackendAddress:       "127.0.0.1:8080",
		WeakLink:             false,
		WeakLinkReason:       "",
	})

	if raw["state"] != "connected" {
		t.Fatalf("expected connected state, got %#v", raw["state"])
	}
	if raw["reconnectCount"] != uint64(3) {
		t.Fatalf("expected reconnect count, got %#v", raw["reconnectCount"])
	}
	if raw["backendAddress"] != "127.0.0.1:8080" {
		t.Fatalf("expected backend address, got %#v", raw["backendAddress"])
	}
	if raw["weakLink"] != false {
		t.Fatalf("expected weak link false, got %#v", raw["weakLink"])
	}
}

func TestDispatchGimbalControlEnqueuesTransientCommandForConnectedDrone(t *testing.T) {
	hub := NewHub(Dependencies{}, slog.Default())
	conn := newConnection("agent-001", "drone-001", "connection-001", "link-001")
	hub.register(conn)

	ok := hub.DispatchGimbalControl(context.Background(), models.GimbalControlCommand{
		DroneID:           "drone-001",
		PitchRateDegS:     25,
		YawRateDegS:       -10,
		TargetSystemID:    1,
		TargetComponentID: 154,
		GimbalDeviceID:    1,
	})
	if !ok {
		t.Fatal("expected gimbal control dispatch over connected stream")
	}

	select {
	case msg := <-conn.send:
		command := msg.GetGimbalControl()
		if command == nil {
			t.Fatalf("expected gimbal control message, got %#v", msg)
		}
		if command.GetDroneId() != "drone-001" {
			t.Fatalf("expected drone-001, got %q", command.GetDroneId())
		}
		if command.GetPitchRateDegS() != 25 {
			t.Fatalf("expected pitch rate 25, got %f", command.GetPitchRateDegS())
		}
		if command.GetYawRateDegS() != -10 {
			t.Fatalf("expected yaw rate -10, got %f", command.GetYawRateDegS())
		}
		if command.GetTargetComponentId() != 154 {
			t.Fatalf("expected target component 154, got %d", command.GetTargetComponentId())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for gimbal control message")
	}
}

func TestDispatchGimbalControlRejectsDisconnectedDrone(t *testing.T) {
	hub := NewHub(Dependencies{}, slog.Default())

	ok := hub.DispatchGimbalControl(context.Background(), models.GimbalControlCommand{
		DroneID:       "drone-001",
		PitchRateDegS: 25,
	})
	if ok {
		t.Fatal("expected dispatch to reject gimbal control without a connected agent")
	}
}

func TestAgentChannelDispatchesCommandAndReceivesStatus(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	now := time.Now().UTC()
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

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok := hub.DispatchVehicleAction(ctx, command); !ok {
		t.Fatal("expected command dispatch over connected stream")
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive command: %v", err)
	}

	if msg.GetVehicleAction().GetVehicleActionId() != command.ID {
		t.Fatalf("expected vehicle action %q, got %q", command.ID, msg.GetVehicleAction().GetVehicleActionId())
	}
	if msg.GetVehicleAction().GetAckCorrelationId() != command.AckCorrelationID {
		t.Fatalf("expected ACK correlation id %q, got %q", command.AckCorrelationID, msg.GetVehicleAction().GetAckCorrelationId())
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_VehicleActionStatus{
			VehicleActionStatus: &pb.VehicleActionStatus{
				VehicleActionId: command.ID,
				State:           string(models.VehicleActionStateVehicleAgentReceived),
			},
		},
	}); err != nil {
		t.Fatalf("send vehicle_agent_received: %v", err)
	}

	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-001",
		Payload: &pb.VehicleAgentToBackend_VehicleActionStatus{
			VehicleActionStatus: &pb.VehicleActionStatus{
				VehicleActionId:  command.ID,
				State:            string(models.VehicleActionStateVehicleAcked),
				AckCorrelationId: command.AckCorrelationID,
				RawAckCode:       "MAV_RESULT_ACCEPTED",
				RawMavlinkCommandAck: &pb.RawMavlinkCommandAckEvidence{
					ObservedAt:        time.Now().UTC().Format(time.RFC3339Nano),
					SourceSystemId:    1,
					SourceComponentId: 1,
					Command:           400,
					Result:            0,
					ResultLabel:       "MAV_RESULT_ACCEPTED",
					MatchStatus:       "matched_command_time_active_action_source",
				},
			},
		},
	}); err != nil {
		t.Fatalf("send vehicle_acked: %v", err)
	}

	waitFor(t, func() bool {
		updated, ok := repo.GetVehicleActionByID(context.Background(), command.ID)
		return ok && updated.State == models.VehicleActionStateVehicleAcked
	})

	events, err := repo.ListVehicleActionEvents(context.Background(), command.ID)
	if err != nil {
		t.Fatalf("list vehicle action events: %v", err)
	}
	var hasRawAckEvidence bool
	for _, event := range events {
		if event.RawAckCode == "MAV_RESULT_ACCEPTED" && event.Evidence["rawMavlinkCommandAck"] != nil {
			hasRawAckEvidence = true
		}
	}
	if !hasRawAckEvidence {
		t.Fatalf("expected raw MAVLink ACK evidence event, got %#v", events)
	}
}

func TestAgentChannelRecordsConnectionAndCommunicationLinkLifecycle(t *testing.T) {
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
		VehicleAgentId: "agent-conn-001",
		Payload: &pb.VehicleAgentToBackend_Hello{
			Hello: &pb.VehicleAgentHello{
				DroneId:             "drone-conn-001",
				DroneName:           "Connection Test Quad",
				VehicleAgentVersion: "0.1.0",
			},
		},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	var connectionRecord models.DroneVehicleAgentConnection
	waitFor(t, func() bool {
		var ok bool
		var err error
		connectionRecord, ok, err = repo.repos.DroneVehicleAgentConnections.LatestActiveDroneVehicleAgentConnectionForAgent(ctx, "agent-conn-001")
		return err == nil && ok
	})

	if connectionRecord.DroneID != "drone-conn-001" {
		t.Fatalf("expected connection for drone-conn-001, got %q", connectionRecord.DroneID)
	}
	if connectionRecord.Transport != "GRPC" {
		t.Fatalf("expected GRPC transport, got %q", connectionRecord.Transport)
	}

	linkRecord, ok, err := repo.repos.CommunicationLinks.GetCommunicationLinkForDroneVehicleAgentConnection(ctx, connectionRecord.ID)
	if err != nil {
		t.Fatalf("get communication link: %v", err)
	}
	if !ok {
		t.Fatal("expected communication link for connection")
	}
	if linkRecord.Status != models.CommunicationLinkStatusConnected {
		t.Fatalf("expected connected link, got %q", linkRecord.Status)
	}
	if linkRecord.LinkType != models.CommunicationLinkVehicleAgentGRPC {
		t.Fatalf("expected vehicle-agent gRPC link, got %q", linkRecord.LinkType)
	}
	if !linkRecord.CommandEligible {
		t.Fatal("expected vehicle-agent gRPC link to be command eligible")
	}
	for _, role := range []models.CommunicationLinkRole{
		models.CommunicationLinkRoleTelemetry,
		models.CommunicationLinkRoleCommand,
		models.CommunicationLinkRoleVideo,
		models.CommunicationLinkRoleGimbalControl,
	} {
		if !hasCommunicationLinkRole(linkRecord.Roles, role) {
			t.Fatalf("expected vehicle-agent gRPC link role %q in %#v", role, linkRecord.Roles)
		}
	}

	heartbeatSentAt := time.Now().UTC()
	if err := stream.Send(&pb.VehicleAgentToBackend{
		VehicleAgentId: "agent-conn-001",
		Payload: &pb.VehicleAgentToBackend_Heartbeat{
			Heartbeat: &pb.Heartbeat{
				VehicleAgentVersion: "0.1.1",
			},
		},
	}); err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}

	waitFor(t, func() bool {
		updated, ok, err := repo.repos.DroneVehicleAgentConnections.GetDroneVehicleAgentConnectionByID(ctx, connectionRecord.ID)
		return err == nil && ok && !updated.LastHeartbeatAt.Before(heartbeatSentAt)
	})

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close stream: %v", err)
	}

	waitFor(t, func() bool {
		closed, ok, err := repo.repos.DroneVehicleAgentConnections.GetDroneVehicleAgentConnectionByID(ctx, connectionRecord.ID)
		if err != nil || !ok || closed.Status != models.DroneVehicleAgentConnectionDisconnected || closed.EndedAt.IsZero() {
			return false
		}
		closedLink, ok, err := repo.repos.CommunicationLinks.GetCommunicationLinkForDroneVehicleAgentConnection(ctx, connectionRecord.ID)
		return err == nil && ok && closedLink.Status == models.CommunicationLinkStatusLost && !closedLink.EndedAt.IsZero()
	})
}

func TestAgentChannelRedeliversExpiredCommandOnConnect(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	now := time.Now().UTC().Add(-2 * models.VehicleActionDeliveryLeaseDuration)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	first, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second))
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

	if msg.GetVehicleAction().GetVehicleActionId() != command.ID {
		t.Fatalf("expected redelivered vehicle action %q, got %q", command.ID, msg.GetVehicleAction().GetVehicleActionId())
	}

	redelivered, ok := repo.GetVehicleActionByID(context.Background(), command.ID)
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

func TestAgentChannelDispatchLeavesCommandAuthorizedWhenAgentDisconnected(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeLand,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	hub := NewHub(repo.dependencies(), slog.Default())
	if _, ok := hub.DispatchVehicleAction(context.Background(), command); ok {
		t.Fatal("expected dispatch to fail without connected vehicle-agent")
	}

	stored, ok := repo.GetVehicleActionByID(context.Background(), command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if stored.State != models.VehicleActionStateAuthorized {
		t.Fatalf("expected command to remain authorized for gRPC reconnect delivery, got %q", stored.State)
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

	if _, _, err := repo.VehicleAgentConnectionService.OpenDroneVehicleAgentConnection(context.Background(), repository.OpenDroneVehicleAgentConnectionInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		VehicleAgentVersion: "0.1.0",
		RemoteAddress:       "127.0.0.1:50051",
	}, now); err != nil {
		t.Fatalf("open communication link: %v", err)
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

func hasCommunicationLinkRole(roles []models.CommunicationLinkRole, expected models.CommunicationLinkRole) bool {
	for _, role := range roles {
		if role == expected {
			return true
		}
	}
	return false
}

type testRepository struct {
	*svc.VehicleAgentService
	*svc.VehicleAgentConnectionService
	*svc.TelemetryService
	*svc.VehicleActionService
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
		VehicleAgentService:           appServices.VehicleAgents,
		VehicleAgentConnectionService: appServices.VehicleAgentConnections,
		TelemetryService:              appServices.Telemetry,
		VehicleActionService:          appServices.VehicleActions,
		MissionService:                appServices.Missions,
		repos:                         repos,
		deps: Dependencies{
			VehicleAgents:           appServices.VehicleAgents,
			VehicleAgentConnections: appServices.VehicleAgentConnections,
			Telemetry:               appServices.Telemetry,
			VehicleActions:          appServices.VehicleActions,
			Missions:                appServices.Missions,
		},
	}
}
