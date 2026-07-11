package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
	svc "github.com/sunnyside/atlas/atlas-backend/internal/services"
	"github.com/sunnyside/atlas/atlas-backend/internal/testutil"
)

func TestRegisterVehicleAgentUpsertsDroneAndAgent(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	repo.RegisterVehicleAgent(context.Background(), repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.1.0",
	}, now)

	later := now.Add(2 * time.Second)
	repo.RegisterVehicleAgent(context.Background(), repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.1.1",
	}, later)

	drones := repo.ListDrones(context.Background(), later)
	if len(drones) != 1 {
		t.Fatalf("expected one drone, got %d", len(drones))
	}

	if drones[0].Status != models.VehicleAgentStatusRegistered {
		t.Fatalf("expected registered status, got %q", drones[0].Status)
	}
}

func TestRegisterVehicleAgentRevokesPreviousActiveAgentForDrone(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	if _, err := repo.RegisterVehicleAgent(ctx, repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.1.0",
	}, now); err != nil {
		t.Fatalf("register first agent: %v", err)
	}
	if _, err := repo.RecordCommandChannelConnected(ctx, "agent-001", now.Add(time.Second)); err != nil {
		t.Fatalf("record first agent command channel: %v", err)
	}

	replacementAt := now.Add(2 * time.Second)
	if _, err := repo.RegisterVehicleAgent(ctx, repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-002",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.2.0",
	}, replacementAt); err != nil {
		t.Fatalf("register replacement agent: %v", err)
	}

	oldAgent := vehicleAgentByID(t, repo, "agent-001")
	if oldAgent.IdentityStatus != models.DeviceIdentityRevoked {
		t.Fatalf("expected old agent to be revoked, got %q", oldAgent.IdentityStatus)
	}
	if !oldAgent.RevokedAt.Equal(replacementAt) {
		t.Fatalf("expected old agent revoked at %s, got %s", replacementAt, oldAgent.RevokedAt)
	}
	if oldAgent.CommandChannelState != models.CommandChannelDisconnected {
		t.Fatalf("expected old agent command channel disconnected, got %q", oldAgent.CommandChannelState)
	}

	newAgent := vehicleAgentByID(t, repo, "agent-002")
	if newAgent.IdentityStatus != models.DeviceIdentityActive {
		t.Fatalf("expected replacement agent active, got %q", newAgent.IdentityStatus)
	}

	selected, ok, err := repo.agents.GetActiveVehicleAgentForDrone(ctx, "drone-001")
	if err != nil {
		t.Fatalf("select active agent for drone: %v", err)
	}
	if !ok {
		t.Fatalf("expected active agent for drone")
	}
	if selected.ID != "agent-002" {
		t.Fatalf("expected replacement agent to be selected for drone, got %q", selected.ID)
	}

	drones := repo.ListDrones(ctx, replacementAt)
	if len(drones) != 1 {
		t.Fatalf("expected one drone, got %d", len(drones))
	}
	if drones[0].VehicleAgentID != "agent-002" {
		t.Fatalf("expected drone snapshot to use replacement agent, got %q", drones[0].VehicleAgentID)
	}
}

func TestTxManagerRollsBackCallbackError(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	rollbackErr := errors.New("rollback transaction")

	err := repo.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		if err := insertRegisteredAgent(ctx, repos, "agent-tx", "drone-tx", "Transaction Quad", now); err != nil {
			t.Fatalf("register transaction-bound agent: %v", err)
		}
		agent, ok, err := repos.VehicleAgents.GetVehicleAgentByID(ctx, "agent-tx")
		if err != nil {
			t.Fatalf("read transaction-bound agent: %v", err)
		}
		if !ok {
			t.Fatal("expected transaction-bound agent to be visible before rollback")
		}
		if agent.ID != "agent-tx" {
			t.Fatalf("expected transaction-bound agent to be visible before rollback, got %q", agent.ID)
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("expected rollback sentinel, got %v", err)
	}

	drones := repo.ListDrones(ctx, now)
	if len(drones) != 0 {
		t.Fatalf("expected rollback to discard transaction-bound register, got %d drones", len(drones))
	}
}

func TestRecordHeartbeatUpdatesDerivedStatus(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

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

	drones := repo.ListDrones(context.Background(), now.Add(10*time.Second))
	if drones[0].Status != models.VehicleAgentStatusOnline {
		t.Fatalf("expected online status, got %q", drones[0].Status)
	}

	drones = repo.ListDrones(context.Background(), now.Add(30*time.Second))
	if drones[0].Status != models.VehicleAgentStatusStale {
		t.Fatalf("expected stale status, got %q", drones[0].Status)
	}

	drones = repo.ListDrones(context.Background(), now.Add(90*time.Second))
	if drones[0].Status != models.VehicleAgentStatusOffline {
		t.Fatalf("expected offline status, got %q", drones[0].Status)
	}
}

func TestRecordTelemetryStoresLatestSnapshot(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	repo.RegisterVehicleAgent(context.Background(), repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.1.0",
	}, now)
	link := openReadyCommunicationLink(t, repo, now)

	snapshot, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID:    "agent-001",
		ObservedAt:        now,
		BatteryPercent:    82,
		RelativeAltitudeM: 12.5,
		FlightMode:        "HOLD",
		GPSFix:            "3D",
		Source:            "px4",
	}, now)
	if err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	if snapshot.DroneID != "drone-001" {
		t.Fatalf("expected drone-001, got %q", snapshot.DroneID)
	}
	if snapshot.ActiveTelemetryFeedID == "" {
		t.Fatal("expected active telemetry feed id")
	}
	if snapshot.SourceCommunicationLinkID != link.ID {
		t.Fatalf("expected source communication link %q, got %q", link.ID, snapshot.SourceCommunicationLinkID)
	}

	drones := repo.ListDrones(context.Background(), now)
	if drones[0].TelemetryState != models.TelemetryStateFresh {
		t.Fatalf("expected fresh telemetry, got %q", drones[0].TelemetryState)
	}

	if drones[0].Telemetry.BatteryPercent != 82 {
		t.Fatalf("expected battery 82, got %f", drones[0].Telemetry.BatteryPercent)
	}
	if drones[0].Telemetry.ActiveTelemetryFeedID != snapshot.ActiveTelemetryFeedID {
		t.Fatalf("expected latest telemetry feed %q, got %q", snapshot.ActiveTelemetryFeedID, drones[0].Telemetry.ActiveTelemetryFeedID)
	}
	if drones[0].Telemetry.SourceCommunicationLinkID != link.ID {
		t.Fatalf("expected latest source communication link %q, got %q", link.ID, drones[0].Telemetry.SourceCommunicationLinkID)
	}

	feed, ok, err := repo.TelemetryFeedRepository.GetTelemetryFeedByID(context.Background(), snapshot.ActiveTelemetryFeedID)
	if err != nil {
		t.Fatalf("get telemetry feed: %v", err)
	}
	if !ok {
		t.Fatal("expected telemetry feed")
	}
	if feed.SourceType != models.TelemetrySourceAgentDirect {
		t.Fatalf("expected agent-direct telemetry feed, got %q", feed.SourceType)
	}
	if feed.SourceID != "agent-001" {
		t.Fatalf("expected source agent-001, got %q", feed.SourceID)
	}
	if feed.CommunicationLinkID != link.ID {
		t.Fatalf("expected feed communication link %q, got %q", link.ID, feed.CommunicationLinkID)
	}

	sample, ok, err := repo.TelemetrySampleRepository.LatestTelemetrySampleForFeed(context.Background(), feed.ID)
	if err != nil {
		t.Fatalf("get latest telemetry sample: %v", err)
	}
	if !ok {
		t.Fatal("expected telemetry sample")
	}
	if sample.TelemetryFeedID != feed.ID {
		t.Fatalf("expected sample feed %q, got %q", feed.ID, sample.TelemetryFeedID)
	}
	if sample.Snapshot.ActiveTelemetryFeedID != feed.ID {
		t.Fatalf("expected sample snapshot feed %q, got %q", feed.ID, sample.Snapshot.ActiveTelemetryFeedID)
	}
}

func TestRecordLocalTelemetryDoesNotOverrideFreshAgentLatest(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	repo.RegisterVehicleAgent(context.Background(), repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.1.0",
	}, now)
	agentLink := openReadyCommunicationLink(t, repo, now)

	agentSnapshot, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID:    "agent-001",
		ObservedAt:        now,
		BatteryPercent:    82,
		RelativeAltitudeM: 12.5,
		FlightMode:        "HOLD",
		GPSFix:            "3D",
		Source:            "px4",
	}, now)
	if err != nil {
		t.Fatalf("record agent telemetry: %v", err)
	}

	localSnapshot, promoted, err := repo.telemetryService.RecordLocalTelemetry(context.Background(), repository.RecordLocalTelemetryInput{
		DroneID:             "drone-001",
		SourceID:            "hm30-local",
		Source:              "local:hm30-local",
		Transport:           "MAVLINK_UDP",
		EndpointDescription: "udp-server://0.0.0.0:14560",
		Snapshot: models.TelemetrySnapshot{
			ObservedAt:        now.Add(time.Second),
			BatteryPercent:    74,
			RelativeAltitudeM: 20,
			FlightMode:        "UNKNOWN",
			GPSFix:            "3D",
			Source:            "local:hm30-local",
		},
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("record local telemetry: %v", err)
	}
	if promoted {
		t.Fatal("expected fresh agent telemetry to remain selected")
	}
	if localSnapshot.ActiveTelemetryFeedID == "" {
		t.Fatal("expected local telemetry feed id")
	}

	latest, ok := repo.GetTelemetryForDrone(context.Background(), "drone-001")
	if !ok {
		t.Fatal("expected latest telemetry")
	}
	if latest.ActiveTelemetryFeedID != agentSnapshot.ActiveTelemetryFeedID {
		t.Fatalf("expected latest feed %q, got %q", agentSnapshot.ActiveTelemetryFeedID, latest.ActiveTelemetryFeedID)
	}
	if latest.SourceCommunicationLinkID != agentLink.ID {
		t.Fatalf("expected latest source link %q, got %q", agentLink.ID, latest.SourceCommunicationLinkID)
	}

	feeds, err := repo.TelemetryFeedRepository.ListTelemetryFeedsForDrone(context.Background(), "drone-001")
	if err != nil {
		t.Fatalf("list telemetry feeds: %v", err)
	}
	if len(feeds) != 2 {
		t.Fatalf("expected agent and local telemetry feeds, got %d", len(feeds))
	}
}

func TestRecordLocalTelemetryPromotesWhenAgentLatestIsStale(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	repo.RegisterVehicleAgent(context.Background(), repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.1.0",
	}, now)
	openReadyCommunicationLink(t, repo, now)

	if _, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID:    "agent-001",
		ObservedAt:        now,
		BatteryPercent:    82,
		RelativeAltitudeM: 12.5,
		FlightMode:        "HOLD",
		GPSFix:            "3D",
		Source:            "px4",
	}, now); err != nil {
		t.Fatalf("record agent telemetry: %v", err)
	}

	staleTime := now.Add(models.TelemetryFreshWindow + time.Second)
	localSnapshot, promoted, err := repo.telemetryService.RecordLocalTelemetry(context.Background(), repository.RecordLocalTelemetryInput{
		DroneID:             "drone-001",
		SourceID:            "hm30-local",
		Source:              "local:hm30-local",
		Transport:           "MAVLINK_UDP",
		EndpointDescription: "udp-server://0.0.0.0:14560",
		Snapshot: models.TelemetrySnapshot{
			ObservedAt:        staleTime,
			BatteryPercent:    74,
			RelativeAltitudeM: 20,
			FlightMode:        "UNKNOWN",
			GPSFix:            "3D",
			Source:            "local:hm30-local",
		},
	}, staleTime)
	if err != nil {
		t.Fatalf("record local telemetry: %v", err)
	}
	if !promoted {
		t.Fatal("expected stale agent telemetry to allow local fallback selection")
	}

	latest, ok := repo.GetTelemetryForDrone(context.Background(), "drone-001")
	if !ok {
		t.Fatal("expected latest telemetry")
	}
	if latest.ActiveTelemetryFeedID != localSnapshot.ActiveTelemetryFeedID {
		t.Fatalf("expected local feed %q, got %q", localSnapshot.ActiveTelemetryFeedID, latest.ActiveTelemetryFeedID)
	}
	if latest.VehicleAgentID != "" {
		t.Fatalf("expected local fallback latest to have no vehicle agent id, got %q", latest.VehicleAgentID)
	}
	if latest.Source != "local:hm30-local" {
		t.Fatalf("expected local source, got %q", latest.Source)
	}
}

func TestRecordCommandChannelStateAppearsInDroneSnapshot(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	repo.RegisterVehicleAgent(context.Background(), repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.1.0",
	}, now)

	drones := repo.ListDrones(context.Background(), now)
	if drones[0].CommandChannel.State != models.CommandChannelDisconnected {
		t.Fatalf("expected disconnected command channel, got %q", drones[0].CommandChannel.State)
	}

	connectedAt := now.Add(time.Second)
	if _, err := repo.RecordCommandChannelConnected(context.Background(), "agent-001", connectedAt); err != nil {
		t.Fatalf("record connected channel: %v", err)
	}

	drones = repo.ListDrones(context.Background(), connectedAt)
	if drones[0].CommandChannel.State != models.CommandChannelConnected {
		t.Fatalf("expected connected command channel, got %q", drones[0].CommandChannel.State)
	}

	if !drones[0].CommandChannel.ConnectedAt.Equal(connectedAt) {
		t.Fatalf("expected connected at %s, got %s", connectedAt, drones[0].CommandChannel.ConnectedAt)
	}

	disconnectedAt := connectedAt.Add(time.Second)
	if _, err := repo.RecordCommandChannelDisconnected(context.Background(), "agent-001", disconnectedAt); err != nil {
		t.Fatalf("record disconnected channel: %v", err)
	}

	drones = repo.ListDrones(context.Background(), disconnectedAt)
	if drones[0].CommandChannel.State != models.CommandChannelDisconnected {
		t.Fatalf("expected disconnected command channel, got %q", drones[0].CommandChannel.State)
	}

	if !drones[0].CommandChannel.LastDisconnectedAt.Equal(disconnectedAt) {
		t.Fatalf("expected disconnected at %s, got %s", disconnectedAt, drones[0].CommandChannel.LastDisconnectedAt)
	}
}

func TestRequestCommandAuthorizesWhenAgentOnlineAndTelemetryFresh(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

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
		VehicleAgentID: "agent-001",
		ObservedAt:     now,
		BatteryPercent: 82,
		FlightMode:     "HOLD",
		GPSFix:         "3D",
		Source:         "px4",
	}, now); err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	openReadyCommunicationLink(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if command.State != models.VehicleActionStateAuthorized {
		t.Fatalf("expected authorized command, got %q", command.State)
	}
}

func TestRequestCommandRejectsWhenTelemetryIsNotFresh(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

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

	openReadyCommunicationLink(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeTakeoff,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if command.State != models.VehicleActionStateRejectedByPolicy {
		t.Fatalf("expected rejected command, got %q", command.State)
	}

	if command.PolicyReason != "telemetry must be fresh" {
		t.Fatalf("expected telemetry policy reason, got %q", command.PolicyReason)
	}
}

func TestRequestVehicleActionReusesIdempotencyKey(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	input := repository.RequestVehicleActionInput{
		DroneID:        "drone-001",
		Type:           models.VehicleActionTypeArm,
		RequestedBy:    "operator-001",
		IdempotencyKey: "operator-001-arm-0001",
	}
	first, err := repo.RequestVehicleAction(context.Background(), input, now)
	if err != nil {
		t.Fatalf("request first vehicle action: %v", err)
	}
	second, err := repo.RequestVehicleAction(context.Background(), input, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request duplicate vehicle action: %v", err)
	}

	if second.ID != first.ID {
		t.Fatalf("expected duplicate request to return %q, got %q", first.ID, second.ID)
	}
	if second.IdempotencyKey != input.IdempotencyKey {
		t.Fatalf("expected idempotency key %q, got %q", input.IdempotencyKey, second.IdempotencyKey)
	}

	actions, err := repo.ListVehicleActionsForDrone(context.Background(), "drone-001", 10)
	if err != nil {
		t.Fatalf("list vehicle actions: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected one stored vehicle action, got %d", len(actions))
	}
}

func TestNextVehicleActionForVehicleAgentClaimsOldestAuthorizedCommand(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	first, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request first command: %v", err)
	}

	if _, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeLand,
		RequestedBy: "operator-001",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("request second command: %v", err)
	}

	claimed, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("next command: %v", err)
	}

	if !ok {
		t.Fatal("expected pending command")
	}

	if claimed.ID != first.ID {
		t.Fatalf("expected oldest command %q, got %q", first.ID, claimed.ID)
	}

	if claimed.State != models.VehicleActionStateSentToVehicleAgent {
		t.Fatalf("expected sent_to_vehicle_agent, got %q", claimed.State)
	}

	if claimed.DeliveryAttempt != 1 {
		t.Fatalf("expected first delivery attempt, got %d", claimed.DeliveryAttempt)
	}

	if claimed.LeaseUntil.IsZero() {
		t.Fatal("expected delivery lease deadline")
	}
}

func TestNextVehicleActionForVehicleAgentReturnsEmptyWhenNoAuthorizedCommand(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	_, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now)
	if err != nil {
		t.Fatalf("next command: %v", err)
	}

	if ok {
		t.Fatal("expected no pending command")
	}
}

func TestNextVehicleActionForVehicleAgentDoesNotRedeliverBeforeLeaseExpires(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	if _, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now); err != nil {
		t.Fatalf("request command: %v", err)
	}

	claimed, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second))
	if err != nil {
		t.Fatalf("next command: %v", err)
	}
	if !ok {
		t.Fatal("expected first delivery")
	}

	_, ok, err = repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", claimed.LeaseUntil.Add(-time.Millisecond))
	if err != nil {
		t.Fatalf("next command before lease expiry: %v", err)
	}
	if ok {
		t.Fatal("expected no redelivery before lease expiry")
	}
}

func TestNextVehicleActionForVehicleAgentRedeliversAfterLeaseExpires(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	if _, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now); err != nil {
		t.Fatalf("request command: %v", err)
	}

	first, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second))
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if !ok {
		t.Fatal("expected first delivery")
	}

	second, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", first.LeaseUntil)
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if !ok {
		t.Fatal("expected redelivery after lease expiry")
	}

	if second.ID != first.ID {
		t.Fatalf("expected same command redelivered, got %q", second.ID)
	}

	if second.DeliveryAttempt != 2 {
		t.Fatalf("expected second delivery attempt, got %d", second.DeliveryAttempt)
	}

	if !second.LeaseUntil.After(first.LeaseUntil) {
		t.Fatalf("expected renewed lease after %s, got %s", first.LeaseUntil, second.LeaseUntil)
	}
}

func TestSweepTimedOutVehicleActionsTimesOutStaleAuthorizedAction(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	count, err := repo.SweepTimedOutVehicleActions(context.Background(), now.Add(models.VehicleActionAuthorizationTimeout+time.Second))
	if err != nil {
		t.Fatalf("sweep timeouts: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one timed out vehicle action, got %d", count)
	}

	updated, ok := repo.GetVehicleActionByID(context.Background(), command.ID)
	if !ok {
		t.Fatal("expected stored vehicle action")
	}
	if updated.State != models.VehicleActionStateTimedOut {
		t.Fatalf("expected timed_out, got %q", updated.State)
	}
}

func TestUpdateVehicleActionStatusRecordsAgentResult(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	updated, err := repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
		VehicleAgentID:   "agent-001",
		VehicleActionID:  command.ID,
		State:            models.VehicleActionStateVehicleAcked,
		AckCorrelationID: command.AckCorrelationID,
		RawAckCode:       "MAV_RESULT_ACCEPTED",
		ResultMessage:    "accepted by vehicle",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if updated.State != models.VehicleActionStateVehicleAcked {
		t.Fatalf("expected vehicle_acked, got %q", updated.State)
	}

	if updated.ResultMessage != "accepted by vehicle" {
		t.Fatalf("expected result message, got %q", updated.ResultMessage)
	}

	if updated.RawAckCode != "MAV_RESULT_ACCEPTED" {
		t.Fatalf("expected raw ACK code, got %q", updated.RawAckCode)
	}

	expectedVehicleAckedAt := now.Add(time.Second)
	if !updated.VehicleAckedAt.Equal(expectedVehicleAckedAt) {
		t.Fatalf("expected vehicle acked at %s, got %s", expectedVehicleAckedAt, updated.VehicleAckedAt)
	}

	if updated.ConfirmationBaseline.Armed {
		t.Fatal("expected arm confirmation baseline to start disarmed")
	}
}

func TestUpdateVehicleActionStatusRejectsAckCorrelationMismatch(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	_, err = repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
		VehicleAgentID:   "agent-001",
		VehicleActionID:  command.ID,
		State:            models.VehicleActionStateVehicleAcked,
		AckCorrelationID: "wrong-ack",
	}, now.Add(2*time.Second))
	if err != repository.ErrVehicleActionAckCorrelationMismatch {
		t.Fatalf("expected ACK correlation mismatch, got %v", err)
	}
}

func TestSweepTimedOutVehicleActionsMarksAckedButNotObserved(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	if _, err := repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
		VehicleAgentID:   "agent-001",
		VehicleActionID:  command.ID,
		State:            models.VehicleActionStateVehicleAcked,
		AckCorrelationID: command.AckCorrelationID,
		ResultMessage:    "accepted by vehicle",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("update command status: %v", err)
	}

	count, err := repo.SweepTimedOutVehicleActions(context.Background(), now.Add(2*time.Second+models.VehicleActionObservationTimeout+time.Second))
	if err != nil {
		t.Fatalf("sweep timeouts: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one ACK-but-not-observed vehicle action, got %d", count)
	}

	updated, ok := repo.GetVehicleActionByID(context.Background(), command.ID)
	if !ok {
		t.Fatal("expected stored vehicle action")
	}
	if updated.State != models.VehicleActionStateAckedButNotObserved {
		t.Fatalf("expected acked_but_not_observed, got %q", updated.State)
	}
}

func TestRecordTelemetryConfirmsAckedArmCommand(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	if _, err := repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
		VehicleAgentID:   "agent-001",
		VehicleActionID:  command.ID,
		State:            models.VehicleActionStateVehicleAcked,
		AckCorrelationID: command.AckCorrelationID,
		ResultMessage:    "accepted by vehicle",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if _, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID: "agent-001",
		ObservedAt:     now.Add(3 * time.Second),
		BatteryPercent: 82,
		FlightMode:     "HOLD",
		Armed:          true,
		GPSFix:         "3D",
		Source:         "px4",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	updated, ok := repo.GetVehicleActionByID(context.Background(), command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if updated.State != models.VehicleActionStateTelemetryConfirmed {
		t.Fatalf("expected telemetry_confirmed, got %q", updated.State)
	}

	if updated.ResultMessage != "confirmed by telemetry" {
		t.Fatalf("expected telemetry confirmation result, got %q", updated.ResultMessage)
	}
}

func TestRecordTelemetryConfirmsAckedReturnToLaunchCommand(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeReturnToLaunch,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	if _, err := repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
		VehicleAgentID:   "agent-001",
		VehicleActionID:  command.ID,
		State:            models.VehicleActionStateVehicleAcked,
		AckCorrelationID: command.AckCorrelationID,
		ResultMessage:    "accepted by vehicle",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if _, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID: "agent-001",
		ObservedAt:     now.Add(3 * time.Second),
		BatteryPercent: 82,
		FlightMode:     "Return to Launch",
		GPSFix:         "3D",
		Source:         "px4",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	updated, ok := repo.GetVehicleActionByID(context.Background(), command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if updated.State != models.VehicleActionStateTelemetryConfirmed {
		t.Fatalf("expected telemetry_confirmed, got %q", updated.State)
	}
}

func TestRecordTelemetryConfirmsAckedLandCommand(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	if _, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID: "agent-001",
		ObservedAt:     now.Add(time.Second),
		BatteryPercent: 82,
		FlightMode:     "HOLD",
		Armed:          true,
		InAir:          true,
		GPSFix:         "3D",
		Source:         "px4",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("record in-air telemetry: %v", err)
	}

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeLand,
		RequestedBy: "operator-001",
	}, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(3*time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	if _, err := repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
		VehicleAgentID:   "agent-001",
		VehicleActionID:  command.ID,
		State:            models.VehicleActionStateVehicleAcked,
		AckCorrelationID: command.AckCorrelationID,
		ResultMessage:    "accepted by vehicle",
	}, now.Add(4*time.Second)); err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if _, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID: "agent-001",
		ObservedAt:     now.Add(5 * time.Second),
		BatteryPercent: 82,
		FlightMode:     "LAND",
		Armed:          true,
		InAir:          true,
		GPSFix:         "3D",
		Source:         "px4",
	}, now.Add(5*time.Second)); err != nil {
		t.Fatalf("record land telemetry: %v", err)
	}

	updated, ok := repo.GetVehicleActionByID(context.Background(), command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if updated.State != models.VehicleActionStateTelemetryConfirmed {
		t.Fatalf("expected telemetry_confirmed, got %q", updated.State)
	}
}

func TestRecordTelemetryDoesNotConfirmSupersededTakeoffCommand(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	first, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeTakeoff,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	if _, err := repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
		VehicleAgentID:   "agent-001",
		VehicleActionID:  first.ID,
		State:            models.VehicleActionStateVehicleAcked,
		AckCorrelationID: first.AckCorrelationID,
		ResultMessage:    "accepted by vehicle",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("update command status: %v", err)
	}

	second, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeTakeoff,
		RequestedBy: "operator-001",
	}, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("request second command: %v", err)
	}

	if _, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(4*time.Second)); err != nil {
		t.Fatalf("next second command: %v", err)
	} else if !ok {
		t.Fatal("expected second pending command")
	}

	if _, err := repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
		VehicleAgentID:   "agent-001",
		VehicleActionID:  second.ID,
		State:            models.VehicleActionStateVehicleAcked,
		AckCorrelationID: second.AckCorrelationID,
		ResultMessage:    "accepted by vehicle",
	}, now.Add(5*time.Second)); err != nil {
		t.Fatalf("update second command status: %v", err)
	}

	firstAfterSecondAck, ok := repo.GetVehicleActionByID(context.Background(), first.ID)
	if !ok {
		t.Fatal("expected first stored command")
	}

	if firstAfterSecondAck.State != models.VehicleActionStateFailed {
		t.Fatalf("expected first command to be superseded as failed, got %q", firstAfterSecondAck.State)
	}

	if _, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID: "agent-001",
		ObservedAt:     now.Add(6 * time.Second),
		BatteryPercent: 82,
		FlightMode:     "HOLD",
		InAir:          true,
		GPSFix:         "3D",
		Source:         "px4",
	}, now.Add(6*time.Second)); err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	updatedFirst, ok := repo.GetVehicleActionByID(context.Background(), first.ID)
	if !ok {
		t.Fatal("expected first stored command")
	}

	if updatedFirst.State != models.VehicleActionStateFailed {
		t.Fatalf("expected first command to stay failed, got %q", updatedFirst.State)
	}

	updatedSecond, ok := repo.GetVehicleActionByID(context.Background(), second.ID)
	if !ok {
		t.Fatal("expected second stored command")
	}

	if updatedSecond.State != models.VehicleActionStateTelemetryConfirmed {
		t.Fatalf("expected second command to confirm, got %q", updatedSecond.State)
	}
}

func TestRecordTelemetryDoesNotConfirmCommandBeforeVehicleAck(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	claimed, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second))
	if err != nil {
		t.Fatalf("next command: %v", err)
	}
	if !ok {
		t.Fatal("expected pending command")
	}

	if _, err := repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
		VehicleAgentID:  "agent-001",
		VehicleActionID: command.ID,
		State:           models.VehicleActionStateVehicleAgentReceived,
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if _, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID: "agent-001",
		ObservedAt:     now.Add(3 * time.Second),
		BatteryPercent: 82,
		FlightMode:     "HOLD",
		Armed:          true,
		GPSFix:         "3D",
		Source:         "px4",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	updated, ok := repo.GetVehicleActionByID(context.Background(), command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if updated.State != models.VehicleActionStateVehicleAgentReceived {
		t.Fatalf("expected command to remain vehicle_agent_received, got %q", updated.State)
	}

	if claimed.State != models.VehicleActionStateSentToVehicleAgent {
		t.Fatalf("expected claimed command state sent_to_vehicle_agent, got %q", claimed.State)
	}
}

func TestListVehicleActionsForDroneReturnsNewestFirstWithLimit(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	if _, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now); err != nil {
		t.Fatalf("request first command: %v", err)
	}

	second, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeLand,
		RequestedBy: "operator-001",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request second command: %v", err)
	}

	commands, err := repo.ListVehicleActionsForDrone(context.Background(), "drone-001", 1)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}

	if len(commands) != 1 {
		t.Fatalf("expected one command, got %d", len(commands))
	}

	if commands[0].ID != second.ID {
		t.Fatalf("expected newest command %q, got %q", second.ID, commands[0].ID)
	}
}

func TestUpdateVehicleActionStatusClearsDeliveryLeaseOnAck(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	claimed, ok, err := repo.NextVehicleActionForVehicleAgent(context.Background(), "agent-001", now.Add(time.Second))
	if err != nil {
		t.Fatalf("next command: %v", err)
	}
	if !ok {
		t.Fatal("expected pending command")
	}
	if claimed.LeaseUntil.IsZero() {
		t.Fatal("expected delivery lease")
	}

	updated, err := repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
		VehicleAgentID:  "agent-001",
		VehicleActionID: command.ID,
		State:           models.VehicleActionStateVehicleAgentReceived,
	}, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if updated.State != models.VehicleActionStateVehicleAgentReceived {
		t.Fatalf("expected vehicle_agent_received, got %q", updated.State)
	}

	if !updated.LeaseUntil.IsZero() {
		t.Fatalf("expected lease to be cleared after ACK, got %s", updated.LeaseUntil)
	}
}

func TestUpdateVehicleActionStatusRejectsResultBeforeCommandIsClaimed(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	_, err = repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
		VehicleAgentID:  "agent-001",
		VehicleActionID: command.ID,
		State:           models.VehicleActionStateVehicleAcked,
	}, now.Add(time.Second))
	if err != repository.ErrInvalidVehicleActionTransition {
		t.Fatalf("expected invalid transition error, got %v", err)
	}
}

func TestUpdateVehicleActionStatusRejectsNonAgentState(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	command, err := repo.RequestVehicleAction(context.Background(), repository.RequestVehicleActionInput{
		DroneID:     "drone-001",
		Type:        models.VehicleActionTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	for _, state := range []models.VehicleActionState{
		models.VehicleActionStateAuthorized,
		models.VehicleActionStateTelemetryConfirmed,
	} {
		_, err = repo.UpdateVehicleActionStatus(context.Background(), repository.UpdateVehicleActionStatusInput{
			VehicleAgentID:  "agent-001",
			VehicleActionID: command.ID,
			State:           state,
		}, now.Add(time.Second))
		if err != repository.ErrInvalidVehicleActionState {
			t.Fatalf("expected invalid state error for %q, got %v", state, err)
		}
	}
}

func TestCreateMissionValidatesAndStoresMission(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	speed := 6.5
	loiterTime := 12.0
	mission, err := repo.CreateMission(context.Background(), repository.CreateMissionInput{
		DroneID:          "drone-001",
		Name:             "Training loop",
		CreatedBy:        "operator-001",
		CompletionAction: models.MissionCompletionActionLand,
		Waypoints: []repository.MissionWaypointInput{
			{Latitude: 51.5074, Longitude: -0.1278, RelativeAltitudeM: 30, SpeedMPS: &speed, LoiterTimeS: &loiterTime},
			{Latitude: 51.5078, Longitude: -0.1282, RelativeAltitudeM: 35},
		},
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	if mission.ValidationStatus != models.MissionValidationStatusValidated {
		t.Fatalf("expected validated mission, got %q with errors %#v", mission.ValidationStatus, mission.ValidationErrors)
	}

	assertUUIDv7(t, mission.ID)

	if len(mission.Waypoints) != 2 {
		t.Fatalf("expected two waypoints, got %d", len(mission.Waypoints))
	}

	if mission.Waypoints[0].Sequence != 1 || mission.Waypoints[1].Sequence != 2 {
		t.Fatalf("expected waypoint sequence numbers, got %d and %d", mission.Waypoints[0].Sequence, mission.Waypoints[1].Sequence)
	}

	if mission.CompletionAction != models.MissionCompletionActionLand {
		t.Fatalf("expected land completion action, got %q", mission.CompletionAction)
	}

	if mission.Waypoints[0].LoiterTimeS == nil || *mission.Waypoints[0].LoiterTimeS != loiterTime {
		t.Fatalf("expected waypoint loiter time %f, got %v", loiterTime, mission.Waypoints[0].LoiterTimeS)
	}

	missions, err := repo.ListMissionsForDrone(context.Background(), "drone-001")
	if err != nil {
		t.Fatalf("list missions: %v", err)
	}

	if len(missions) != 1 {
		t.Fatalf("expected one mission, got %d", len(missions))
	}
}

func TestCreateMissionCreatesCurrentMissionVersion(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	speed := 5.5
	loiterTime := 8.0
	mission, err := repo.CreateMission(context.Background(), repository.CreateMissionInput{
		DroneID:          "drone-001",
		Name:             "Versioned route",
		CreatedBy:        "operator-001",
		CompletionAction: models.MissionCompletionActionHold,
		Waypoints: []repository.MissionWaypointInput{
			{Latitude: 51.5074, Longitude: -0.1278, RelativeAltitudeM: 30, SpeedMPS: &speed, LoiterTimeS: &loiterTime},
		},
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	if mission.CurrentVersionID == "" {
		t.Fatal("expected current mission version id")
	}

	stored, ok := repo.GetMissionByID(context.Background(), mission.ID)
	if !ok {
		t.Fatal("expected stored mission")
	}
	if stored.CurrentVersionID != mission.CurrentVersionID {
		t.Fatalf("expected stored current version %q, got %q", mission.CurrentVersionID, stored.CurrentVersionID)
	}

	version, ok, err := repo.GetMissionVersionByID(context.Background(), mission.CurrentVersionID)
	if err != nil {
		t.Fatalf("get mission version: %v", err)
	}
	if !ok {
		t.Fatal("expected mission version")
	}
	if version.MissionID != mission.ID {
		t.Fatalf("expected mission id %q, got %q", mission.ID, version.MissionID)
	}
	if version.VersionNumber != 1 {
		t.Fatalf("expected version number 1, got %d", version.VersionNumber)
	}
	if version.ValidationStatus != mission.ValidationStatus {
		t.Fatalf("expected validation status %q, got %q", mission.ValidationStatus, version.ValidationStatus)
	}
	if version.RTLPolicy.CompletionAction != models.MissionCompletionActionHold {
		t.Fatalf("expected hold completion action, got %q", version.RTLPolicy.CompletionAction)
	}
	if len(version.Waypoints) != 1 {
		t.Fatalf("expected one version waypoint, got %d", len(version.Waypoints))
	}
	if version.Waypoints[0].SpeedMPS == nil || *version.Waypoints[0].SpeedMPS != speed {
		t.Fatalf("expected version waypoint speed %f, got %v", speed, version.Waypoints[0].SpeedMPS)
	}
	if version.Waypoints[0].LoiterTimeS == nil || *version.Waypoints[0].LoiterTimeS != loiterTime {
		t.Fatalf("expected version waypoint loiter time %f, got %v", loiterTime, version.Waypoints[0].LoiterTimeS)
	}
}

func TestCreateMissionRejectsUnsafeMissionWithValidationErrors(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	repo.RegisterVehicleAgent(context.Background(), repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.1.0",
	}, now)

	mission, err := repo.CreateMission(context.Background(), repository.CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "",
		CreatedBy: "operator-001",
		Waypoints: []repository.MissionWaypointInput{
			{Latitude: 91, Longitude: -0.1278, RelativeAltitudeM: 0},
		},
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	if mission.ValidationStatus != models.MissionValidationStatusRejected {
		t.Fatalf("expected rejected mission, got %q", mission.ValidationStatus)
	}

	if len(mission.ValidationErrors) < 5 {
		t.Fatalf("expected accumulated validation errors, got %#v", mission.ValidationErrors)
	}

	assertMissionValidationError(t, mission.ValidationErrors, "agent")
	assertMissionValidationError(t, mission.ValidationErrors, "telemetry")
	assertMissionValidationError(t, mission.ValidationErrors, "homePositionSet")
	assertMissionValidationError(t, mission.ValidationErrors, "gpsFix")
	assertMissionValidationError(t, mission.ValidationErrors, "batteryPercent")
	assertMissionValidationError(t, mission.ValidationErrors, "name")
	assertMissionValidationError(t, mission.ValidationErrors, "waypoints[0].latitude")
	assertMissionValidationError(t, mission.ValidationErrors, "waypoints[0].relativeAltitudeM")
}

func TestCreateMissionDefaultsCompletionActionToReturnToLaunch(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	mission, err := repo.CreateMission(context.Background(), repository.CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "Training loop",
		CreatedBy: "operator-001",
		Waypoints: []repository.MissionWaypointInput{
			{Latitude: 51.5074, Longitude: -0.1278, RelativeAltitudeM: 30},
		},
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	if mission.CompletionAction != models.MissionCompletionActionReturnToLaunch {
		t.Fatalf("expected return_to_launch default, got %q", mission.CompletionAction)
	}
}

func TestCreateMissionRejectsInvalidExpandedMissionFields(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyVehicleAgent(t, repo, now)

	negativeLoiter := -1.0
	waypoints := make([]repository.MissionWaypointInput, repository.MaximumMissionWaypoints+1)
	for i := range waypoints {
		waypoints[i] = repository.MissionWaypointInput{Latitude: 51.5074, Longitude: -0.1278, RelativeAltitudeM: 30}
	}
	waypoints[0].LoiterTimeS = &negativeLoiter

	mission, err := repo.CreateMission(context.Background(), repository.CreateMissionInput{
		DroneID:          "drone-001",
		Name:             "Training loop",
		CreatedBy:        "operator-001",
		CompletionAction: models.MissionCompletionAction("orbit_forever"),
		Waypoints:        waypoints,
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	if mission.ValidationStatus != models.MissionValidationStatusRejected {
		t.Fatalf("expected rejected mission, got %q", mission.ValidationStatus)
	}

	assertMissionValidationError(t, mission.ValidationErrors, "waypoints")
	assertMissionValidationError(t, mission.ValidationErrors, "completionAction")
	assertMissionValidationError(t, mission.ValidationErrors, "waypoints[0].loiterTimeS")
}

func TestRequestMissionUploadCreatesExecutionForValidatedMission(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, repo, now)

	execution, err := repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "operator-001",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	assertUUIDv7(t, execution.ID)

	if execution.MissionID != mission.ID {
		t.Fatalf("expected mission %q, got %q", mission.ID, execution.MissionID)
	}

	if execution.MissionVersionID != mission.CurrentVersionID {
		t.Fatalf("expected mission version %q, got %q", mission.CurrentVersionID, execution.MissionVersionID)
	}

	if execution.State != models.MissionExecutionStateUploadRequested {
		t.Fatalf("expected upload_requested, got %q", execution.State)
	}

	if execution.VehicleAgentID != "agent-001" {
		t.Fatalf("expected agent-001, got %q", execution.VehicleAgentID)
	}

	if execution.UploadRequestedBy != "operator-001" {
		t.Fatalf("expected upload requester operator-001, got %q", execution.UploadRequestedBy)
	}
}

func TestRequestMissionUploadRejectsUnvalidatedMission(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	repo.RegisterVehicleAgent(context.Background(), repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.1.0",
	}, now)

	mission, err := repo.CreateMission(context.Background(), repository.CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "",
		CreatedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	_, err = repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "operator-001",
	}, now.Add(time.Second))
	if err != repository.ErrMissionNotValidated {
		t.Fatalf("expected mission not validated error, got %v", err)
	}
}

func TestRequestMissionStartRequiresUploadedExecution(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, repo, now)

	if _, err := repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "operator-001",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	_, err := repo.RequestMissionStart(context.Background(), repository.RequestMissionStartInput{
		MissionID:   mission.ID,
		RequestedBy: "operator-001",
	}, now.Add(2*time.Second))
	if err != repository.ErrInvalidMissionExecutionState {
		t.Fatalf("expected invalid execution state error, got %v", err)
	}
}

func TestRequestMissionStartMovesUploadedExecutionToStartRequested(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, repo, now)

	execution, err := repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "upload-operator",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, err := repo.RecordMissionExecutionUploaded(context.Background(), execution.ID, "uploaded to vehicle", now.Add(2*time.Second)); err != nil {
		t.Fatalf("record mission uploaded: %v", err)
	}

	started, err := repo.RequestMissionStart(context.Background(), repository.RequestMissionStartInput{
		MissionID:   mission.ID,
		RequestedBy: "start-operator",
	}, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("request mission start: %v", err)
	}

	if started.ID != execution.ID {
		t.Fatalf("expected execution %q, got %q", execution.ID, started.ID)
	}

	if started.State != models.MissionExecutionStateStartRequested {
		t.Fatalf("expected start_requested, got %q", started.State)
	}

	if started.RequestedBy != "start-operator" {
		t.Fatalf("expected start operator, got %q", started.RequestedBy)
	}

	if started.UploadRequestedBy != "upload-operator" {
		t.Fatalf("expected upload operator to be preserved, got %q", started.UploadRequestedBy)
	}

	if started.StartRequestedBy != "start-operator" {
		t.Fatalf("expected start requester, got %q", started.StartRequestedBy)
	}

	if started.StartRequestedAt.IsZero() {
		t.Fatal("expected start requested timestamp")
	}
}

func TestRequestMissionStartAllowsGroundedVehicleWhenTelemetryIsFresh(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, repo, now)

	execution, err := repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "upload-operator",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, err := repo.RecordMissionExecutionUploaded(context.Background(), execution.ID, "uploaded to vehicle", now.Add(2*time.Second)); err != nil {
		t.Fatalf("record mission uploaded: %v", err)
	}

	started, err := repo.RequestMissionStart(context.Background(), repository.RequestMissionStartInput{
		MissionID:   mission.ID,
		RequestedBy: "start-operator",
	}, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("request mission start: %v", err)
	}

	if started.State != models.MissionExecutionStateStartRequested {
		t.Fatalf("expected start_requested, got %q", started.State)
	}
}

func TestUpdateMissionExecutionStatusStoresProgressCompletionAndHold(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, repo, now)

	execution, err := repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "upload-operator",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, err := repo.RecordMissionExecutionUploaded(context.Background(), execution.ID, "uploaded to vehicle", now.Add(2*time.Second)); err != nil {
		t.Fatalf("record mission uploaded: %v", err)
	}
	recordAirborneTelemetry(t, repo, now.Add(2500*time.Millisecond))

	if _, err := repo.RequestMissionStart(context.Background(), repository.RequestMissionStartInput{
		MissionID:   mission.ID,
		RequestedBy: "start-operator",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("request mission start: %v", err)
	}

	active, err := repo.UpdateMissionExecutionStatus(context.Background(), repository.UpdateMissionExecutionStatusInput{
		VehicleAgentID:     "agent-001",
		ExecutionID:        execution.ID,
		State:              models.MissionExecutionStateActive,
		ResultMessage:      "mission progress 3/6",
		CurrentMissionItem: 3,
		TotalMissionItems:  6,
	}, now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("update mission active progress: %v", err)
	}

	if active.CurrentMissionItem != 3 || active.TotalMissionItems != 6 {
		t.Fatalf("expected progress 3/6, got %d/%d", active.CurrentMissionItem, active.TotalMissionItems)
	}

	if active.ProgressUpdatedAt.IsZero() {
		t.Fatal("expected progress updated timestamp")
	}

	completed, err := repo.UpdateMissionExecutionStatus(context.Background(), repository.UpdateMissionExecutionStatusInput{
		VehicleAgentID:     "agent-001",
		ExecutionID:        execution.ID,
		State:              models.MissionExecutionStateCompleted,
		ResultMessage:      "mission completed 6/6",
		CurrentMissionItem: 6,
		TotalMissionItems:  6,
	}, now.Add(5*time.Second))
	if err != nil {
		t.Fatalf("update mission completed: %v", err)
	}

	if completed.State != models.MissionExecutionStateCompleted || completed.CompletedAt.IsZero() {
		t.Fatalf("expected completed execution with timestamp, got %#v", completed)
	}

	hold, err := repo.UpdateMissionExecutionStatus(context.Background(), repository.UpdateMissionExecutionStatusInput{
		VehicleAgentID:     "agent-001",
		ExecutionID:        execution.ID,
		State:              models.MissionExecutionStateHold,
		ResultMessage:      "mission complete; holding at final waypoint",
		CurrentMissionItem: 6,
		TotalMissionItems:  6,
	}, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("update mission hold: %v", err)
	}

	if hold.State != models.MissionExecutionStateHold {
		t.Fatalf("expected hold state, got %q", hold.State)
	}

	if hold.HoldAt.IsZero() {
		t.Fatal("expected hold timestamp")
	}

	snapshots := repo.ListDrones(context.Background(), now.Add(7*time.Second))
	if len(snapshots) != 1 {
		t.Fatalf("expected one drone snapshot, got %d", len(snapshots))
	}

	if snapshots[0].LatestMissionExecution.ID != execution.ID {
		t.Fatalf("expected latest mission execution %q, got %q", execution.ID, snapshots[0].LatestMissionExecution.ID)
	}

	if snapshots[0].LatestMissionExecution.State != models.MissionExecutionStateHold {
		t.Fatalf("expected latest mission state hold, got %q", snapshots[0].LatestMissionExecution.State)
	}
}

func TestRequestMissionAbortRequestsRTLAndBlocksNewUpload(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, repo, now)

	execution, err := repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "upload-operator",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, err := repo.RecordMissionExecutionUploaded(context.Background(), execution.ID, "uploaded to vehicle", now.Add(2*time.Second)); err != nil {
		t.Fatalf("record mission uploaded: %v", err)
	}
	recordAirborneTelemetry(t, repo, now.Add(2500*time.Millisecond))

	if _, err := repo.RequestMissionStart(context.Background(), repository.RequestMissionStartInput{
		MissionID:   mission.ID,
		RequestedBy: "start-operator",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("request mission start: %v", err)
	}

	if _, err := repo.UpdateMissionExecutionStatus(context.Background(), repository.UpdateMissionExecutionStatusInput{
		VehicleAgentID: "agent-001",
		ExecutionID:    execution.ID,
		State:          models.MissionExecutionStateActive,
		ResultMessage:  "mission started",
	}, now.Add(4*time.Second)); err != nil {
		t.Fatalf("mark mission active: %v", err)
	}

	aborted, err := repo.RequestMissionAbort(context.Background(), repository.RequestMissionAbortInput{
		MissionID:   mission.ID,
		RequestedBy: "safety-operator",
	}, now.Add(5*time.Second))
	if err != nil {
		t.Fatalf("request mission abort: %v", err)
	}

	if aborted.ID != execution.ID {
		t.Fatalf("expected abort on execution %q, got %q", execution.ID, aborted.ID)
	}

	if aborted.State != models.MissionExecutionStateRTLRequested {
		t.Fatalf("expected rtl_requested, got %q", aborted.State)
	}

	claimed, err := repo.ClaimMissionExecutionForVehicleAgent(context.Background(), "agent-001", aborted.ID, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("claim abort execution: %v", err)
	}

	if claimed.State != models.MissionExecutionStateRTLRequested {
		t.Fatalf("expected claimed rtl_requested, got %q", claimed.State)
	}

	if _, err := repo.UpdateMissionExecutionStatus(context.Background(), repository.UpdateMissionExecutionStatusInput{
		VehicleAgentID: "agent-001",
		ExecutionID:    aborted.ID,
		State:          models.MissionExecutionStateRTLRequested,
		ResultMessage:  "RTL accepted by vehicle; mission abort in progress",
	}, now.Add(7*time.Second)); err != nil {
		t.Fatalf("ack abort execution: %v", err)
	}

	if redelivered, ok, err := repo.NextMissionExecutionForVehicleAgent(context.Background(), "agent-001", now.Add(8*time.Second)); err != nil {
		t.Fatalf("lookup pending abort execution: %v", err)
	} else if ok {
		t.Fatalf("expected acknowledged abort not to redeliver, got %#v", redelivered)
	}

	secondMission, err := repo.CreateMission(context.Background(), repository.CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "Second route",
		CreatedBy: "operator-001",
		Waypoints: []repository.MissionWaypointInput{
			{Latitude: 51.5075, Longitude: -0.1279, RelativeAltitudeM: 30},
		},
	}, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("create second mission: %v", err)
	}

	_, err = repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   secondMission.ID,
		RequestedBy: "operator-001",
	}, now.Add(7*time.Second))
	if err != repository.ErrDroneMissionActive {
		t.Fatalf("expected active mission error, got %v", err)
	}

	recordAirborneTelemetry(t, repo, now.Add(9*time.Second))

	_, err = repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   secondMission.ID,
		RequestedBy: "operator-001",
	}, now.Add(10*time.Second))
	if err != repository.ErrDroneMissionActive {
		t.Fatalf("expected active mission error while RTL is airborne, got %v", err)
	}

	recordGroundedTelemetry(t, repo, now.Add(11*time.Second))

	executions, err := repo.ListMissionExecutions(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("list mission executions: %v", err)
	}
	if len(executions) != 1 {
		t.Fatalf("expected one mission execution, got %d", len(executions))
	}
	if executions[0].State != models.MissionExecutionStateAborted {
		t.Fatalf("expected RTL mission to settle as aborted, got %q", executions[0].State)
	}

	if _, err := repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   secondMission.ID,
		RequestedBy: "operator-001",
	}, now.Add(12*time.Second)); err != nil {
		t.Fatalf("expected upload after RTL settles, got %v", err)
	}
}

func TestMissionExecutionEventsAreRecordedChronologically(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, repo, now)

	execution, err := repo.RequestMissionUpload(context.Background(), repository.RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "upload-operator",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, err := repo.ClaimMissionExecutionForVehicleAgent(context.Background(), "agent-001", execution.ID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("claim mission upload: %v", err)
	}

	if _, err := repo.UpdateMissionExecutionStatus(context.Background(), repository.UpdateMissionExecutionStatusInput{
		VehicleAgentID: "agent-001",
		ExecutionID:    execution.ID,
		State:          models.MissionExecutionStateUploading,
		ResultMessage:  "uploading",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("update mission uploading: %v", err)
	}

	if _, err := repo.UpdateMissionExecutionStatus(context.Background(), repository.UpdateMissionExecutionStatusInput{
		VehicleAgentID: "agent-001",
		ExecutionID:    execution.ID,
		State:          models.MissionExecutionStateUploadedToVehicle,
		ResultMessage:  "uploaded to vehicle",
	}, now.Add(4*time.Second)); err != nil {
		t.Fatalf("update mission uploaded: %v", err)
	}

	events, err := repo.ListMissionExecutionEvents(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("list mission execution events: %v", err)
	}

	assertMissionEventTypes(t, events, []string{
		"upload_requested",
		"sent_to_vehicle_agent",
		"uploading",
		"uploaded_to_vehicle",
	})

	for _, event := range events {
		if event.MissionVersionID != mission.CurrentVersionID {
			t.Fatalf("expected event mission version %q, got %q", mission.CurrentVersionID, event.MissionVersionID)
		}
	}
}

func assertMissionValidationError(t *testing.T, validationErrors []models.MissionValidationError, field string) {
	t.Helper()

	for _, validationError := range validationErrors {
		if validationError.Field == field {
			return
		}
	}

	t.Fatalf("expected validation error for %q in %#v", field, validationErrors)
}

func assertMissionEventTypes(t *testing.T, events []models.MissionExecutionEvent, want []string) {
	t.Helper()

	if len(events) != len(want) {
		t.Fatalf("expected %d events, got %d: %#v", len(want), len(events), events)
	}

	for i, event := range events {
		if event.Type != want[i] {
			t.Fatalf("event %d: expected type %q, got %q", i, want[i], event.Type)
		}
	}
}

func assertUUIDv7(t *testing.T, value string) {
	t.Helper()

	id, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("expected UUID, got %q: %v", value, err)
	}
	if id.Version() != 7 {
		t.Fatalf("expected UUIDv7, got %q with version %d", value, id.Version())
	}
}

func vehicleAgentByID(t *testing.T, repo *testRepository, agentID string) models.VehicleAgent {
	t.Helper()

	agent, err := scanVehicleAgent(repo.agents.exec.QueryRowContext(context.Background(), vehicleAgentByIDSQL, agentID))
	if err != nil {
		t.Fatalf("load agent %q: %v", agentID, err)
	}
	return agent
}

func createValidatedMission(t *testing.T, repo *testRepository, now time.Time) models.Mission {
	t.Helper()

	registerReadyVehicleAgent(t, repo, now)

	mission, err := repo.CreateMission(context.Background(), repository.CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "Training loop",
		CreatedBy: "operator-001",
		Waypoints: []repository.MissionWaypointInput{
			{Latitude: 51.5074, Longitude: -0.1278, RelativeAltitudeM: 30},
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

	openReadyCommunicationLink(t, repo, now)

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

func openReadyCommunicationLink(t *testing.T, repo *testRepository, now time.Time) models.CommunicationLink {
	t.Helper()

	_, link, err := repo.VehicleAgentConnectionService.OpenDroneVehicleAgentConnection(context.Background(), repository.OpenDroneVehicleAgentConnectionInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		VehicleAgentVersion: "0.1.0",
		RemoteAddress:       "127.0.0.1:50051",
	}, now)
	if err != nil {
		t.Fatalf("open communication link: %v", err)
	}
	return link
}

func recordAirborneTelemetry(t *testing.T, repo *testRepository, now time.Time) {
	t.Helper()

	if _, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID:  "agent-001",
		ObservedAt:      now,
		BatteryPercent:  82,
		FlightMode:      "TAKEOFF",
		Armed:           true,
		InAir:           true,
		GPSFix:          "3D",
		HomePositionSet: true,
		Source:          "px4",
	}, now); err != nil {
		t.Fatalf("record airborne telemetry: %v", err)
	}
}

func recordGroundedTelemetry(t *testing.T, repo *testRepository, now time.Time) {
	t.Helper()

	if _, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID:  "agent-001",
		ObservedAt:      now,
		BatteryPercent:  80,
		FlightMode:      "HOLD",
		Armed:           false,
		InAir:           false,
		GPSFix:          "3D",
		HomePositionSet: true,
		Source:          "px4",
	}, now); err != nil {
		t.Fatalf("record grounded telemetry: %v", err)
	}
}

type testRepository struct {
	txManager        *TxManager
	agents           *VehicleAgentRepository
	telemetryService *svc.TelemetryService

	*VehicleAgentRepository
	*DroneRepository
	*TelemetryRepository
	*TelemetryFeedRepository
	*TelemetrySampleRepository
	*svc.VehicleAgentService
	*svc.VehicleAgentConnectionService
	*svc.VehicleActionService
	*svc.MissionService
}

func (r *testRepository) RecordTelemetry(ctx context.Context, snapshot models.TelemetrySnapshot, now time.Time) (models.TelemetrySnapshot, error) {
	return r.telemetryService.RecordTelemetry(ctx, snapshot, now)
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

	txManager := NewTxManager(db)
	agents := newVehicleAgentRepository(db)
	telemetry := newTelemetryRepository(db)
	telemetryFeeds := newTelemetryFeedRepository(db)
	telemetrySamples := newTelemetrySampleRepository(db)
	drones := newDroneRepository(db)
	repos := txManager.Repositories()
	services := svc.New(svc.Dependencies{
		TxManager:    txManager,
		Repositories: repos,
	})

	return &testRepository{
		txManager:                     txManager,
		agents:                        agents,
		telemetryService:              services.Telemetry,
		VehicleAgentRepository:        agents,
		DroneRepository:               drones,
		TelemetryRepository:           telemetry,
		TelemetryFeedRepository:       telemetryFeeds,
		TelemetrySampleRepository:     telemetrySamples,
		VehicleAgentService:           services.VehicleAgents,
		VehicleAgentConnectionService: services.VehicleAgentConnections,
		VehicleActionService:          services.VehicleActions,
		MissionService:                services.Missions,
	}
}
