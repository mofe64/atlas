package registry

import (
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
)

func TestRegisterAgentUpsertsDroneAndAgent(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	later := now.Add(2 * time.Second)
	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.1",
	}, later)

	drones := reg.ListDrones(later)
	if len(drones) != 1 {
		t.Fatalf("expected one drone, got %d", len(drones))
	}

	if drones[0].Status != domain.AgentStatusRegistered {
		t.Fatalf("expected registered status, got %q", drones[0].Status)
	}
}

func TestRecordHeartbeatUpdatesDerivedStatus(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	if _, err := reg.RecordHeartbeat(HeartbeatInput{
		AgentID:      "agent-001",
		AgentVersion: "0.1.0",
	}, now); err != nil {
		t.Fatalf("record heartbeat: %v", err)
	}

	drones := reg.ListDrones(now.Add(10 * time.Second))
	if drones[0].Status != domain.AgentStatusOnline {
		t.Fatalf("expected online status, got %q", drones[0].Status)
	}

	drones = reg.ListDrones(now.Add(30 * time.Second))
	if drones[0].Status != domain.AgentStatusStale {
		t.Fatalf("expected stale status, got %q", drones[0].Status)
	}

	drones = reg.ListDrones(now.Add(90 * time.Second))
	if drones[0].Status != domain.AgentStatusOffline {
		t.Fatalf("expected offline status, got %q", drones[0].Status)
	}
}

func TestRecordTelemetryStoresLatestSnapshot(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	snapshot, err := reg.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:           "agent-001",
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

	drones := reg.ListDrones(now)
	if drones[0].TelemetryState != domain.TelemetryStateFresh {
		t.Fatalf("expected fresh telemetry, got %q", drones[0].TelemetryState)
	}

	if drones[0].Telemetry.BatteryPercent != 82 {
		t.Fatalf("expected battery 82, got %f", drones[0].Telemetry.BatteryPercent)
	}
}

func TestRecordCommandChannelStateAppearsInDroneSnapshot(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	drones := reg.ListDrones(now)
	if drones[0].CommandChannel.State != domain.CommandChannelDisconnected {
		t.Fatalf("expected disconnected command channel, got %q", drones[0].CommandChannel.State)
	}

	connectedAt := now.Add(time.Second)
	if _, err := reg.RecordCommandChannelConnected("agent-001", connectedAt); err != nil {
		t.Fatalf("record connected channel: %v", err)
	}

	drones = reg.ListDrones(connectedAt)
	if drones[0].CommandChannel.State != domain.CommandChannelConnected {
		t.Fatalf("expected connected command channel, got %q", drones[0].CommandChannel.State)
	}

	if !drones[0].CommandChannel.ConnectedAt.Equal(connectedAt) {
		t.Fatalf("expected connected at %s, got %s", connectedAt, drones[0].CommandChannel.ConnectedAt)
	}

	disconnectedAt := connectedAt.Add(time.Second)
	if _, err := reg.RecordCommandChannelDisconnected("agent-001", disconnectedAt); err != nil {
		t.Fatalf("record disconnected channel: %v", err)
	}

	drones = reg.ListDrones(disconnectedAt)
	if drones[0].CommandChannel.State != domain.CommandChannelDisconnected {
		t.Fatalf("expected disconnected command channel, got %q", drones[0].CommandChannel.State)
	}

	if !drones[0].CommandChannel.LastDisconnectedAt.Equal(disconnectedAt) {
		t.Fatalf("expected disconnected at %s, got %s", disconnectedAt, drones[0].CommandChannel.LastDisconnectedAt)
	}
}

func TestRequestCommandAuthorizesWhenAgentOnlineAndTelemetryFresh(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	if _, err := reg.RecordHeartbeat(HeartbeatInput{
		AgentID:      "agent-001",
		AgentVersion: "0.1.0",
	}, now); err != nil {
		t.Fatalf("record heartbeat: %v", err)
	}

	if _, err := reg.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:        "agent-001",
		ObservedAt:     now,
		BatteryPercent: 82,
		FlightMode:     "HOLD",
		GPSFix:         "3D",
		Source:         "px4",
	}, now); err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	command, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if command.State != domain.CommandStateAuthorized {
		t.Fatalf("expected authorized command, got %q", command.State)
	}
}

func TestRequestCommandRejectsWhenTelemetryIsNotFresh(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	if _, err := reg.RecordHeartbeat(HeartbeatInput{
		AgentID:      "agent-001",
		AgentVersion: "0.1.0",
	}, now); err != nil {
		t.Fatalf("record heartbeat: %v", err)
	}

	command, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeTakeoff,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if command.State != domain.CommandStateRejectedByPolicy {
		t.Fatalf("expected rejected command, got %q", command.State)
	}

	if command.PolicyReason != "telemetry must be fresh" {
		t.Fatalf("expected telemetry policy reason, got %q", command.PolicyReason)
	}
}

func TestNextCommandForAgentClaimsOldestAuthorizedCommand(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	first, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request first command: %v", err)
	}

	if _, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeLand,
		RequestedBy: "operator-001",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("request second command: %v", err)
	}

	claimed, ok, err := reg.NextCommandForAgent("agent-001", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("next command: %v", err)
	}

	if !ok {
		t.Fatal("expected pending command")
	}

	if claimed.ID != first.ID {
		t.Fatalf("expected oldest command %q, got %q", first.ID, claimed.ID)
	}

	if claimed.State != domain.CommandStateSentToAgent {
		t.Fatalf("expected sent_to_agent, got %q", claimed.State)
	}

	if claimed.DeliveryAttempt != 1 {
		t.Fatalf("expected first delivery attempt, got %d", claimed.DeliveryAttempt)
	}

	if claimed.LeaseUntil.IsZero() {
		t.Fatal("expected delivery lease deadline")
	}
}

func TestNextCommandForAgentReturnsEmptyWhenNoAuthorizedCommand(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	_, ok, err := reg.NextCommandForAgent("agent-001", now)
	if err != nil {
		t.Fatalf("next command: %v", err)
	}

	if ok {
		t.Fatal("expected no pending command")
	}
}

func TestNextCommandForAgentDoesNotRedeliverBeforeLeaseExpires(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	if _, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now); err != nil {
		t.Fatalf("request command: %v", err)
	}

	claimed, ok, err := reg.NextCommandForAgent("agent-001", now.Add(time.Second))
	if err != nil {
		t.Fatalf("next command: %v", err)
	}
	if !ok {
		t.Fatal("expected first delivery")
	}

	_, ok, err = reg.NextCommandForAgent("agent-001", claimed.LeaseUntil.Add(-time.Millisecond))
	if err != nil {
		t.Fatalf("next command before lease expiry: %v", err)
	}
	if ok {
		t.Fatal("expected no redelivery before lease expiry")
	}
}

func TestNextCommandForAgentRedeliversAfterLeaseExpires(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	if _, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now); err != nil {
		t.Fatalf("request command: %v", err)
	}

	first, ok, err := reg.NextCommandForAgent("agent-001", now.Add(time.Second))
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if !ok {
		t.Fatal("expected first delivery")
	}

	second, ok, err := reg.NextCommandForAgent("agent-001", first.LeaseUntil)
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

func TestUpdateCommandStatusRecordsAgentResult(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	command, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := reg.NextCommandForAgent("agent-001", now.Add(time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	updated, err := reg.UpdateCommandStatus(UpdateCommandStatusInput{
		AgentID:       "agent-001",
		CommandID:     command.ID,
		State:         domain.CommandStateVehicleAcked,
		ResultMessage: "accepted by vehicle",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if updated.State != domain.CommandStateVehicleAcked {
		t.Fatalf("expected vehicle_acked, got %q", updated.State)
	}

	if updated.ResultMessage != "accepted by vehicle" {
		t.Fatalf("expected result message, got %q", updated.ResultMessage)
	}

	expectedVehicleAckedAt := now.Add(time.Second)
	if !updated.VehicleAckedAt.Equal(expectedVehicleAckedAt) {
		t.Fatalf("expected vehicle acked at %s, got %s", expectedVehicleAckedAt, updated.VehicleAckedAt)
	}

	if updated.ConfirmationBaseline.Armed {
		t.Fatal("expected arm confirmation baseline to start disarmed")
	}
}

func TestRecordTelemetryConfirmsAckedArmCommand(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	command, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := reg.NextCommandForAgent("agent-001", now.Add(time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	if _, err := reg.UpdateCommandStatus(UpdateCommandStatusInput{
		AgentID:       "agent-001",
		CommandID:     command.ID,
		State:         domain.CommandStateVehicleAcked,
		ResultMessage: "accepted by vehicle",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if _, err := reg.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:        "agent-001",
		ObservedAt:     now.Add(3 * time.Second),
		BatteryPercent: 82,
		FlightMode:     "HOLD",
		Armed:          true,
		GPSFix:         "3D",
		Source:         "px4",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	updated, ok := reg.CommandByID(command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if updated.State != domain.CommandStateTelemetryConfirmed {
		t.Fatalf("expected telemetry_confirmed, got %q", updated.State)
	}

	if updated.ResultMessage != "confirmed by telemetry" {
		t.Fatalf("expected telemetry confirmation result, got %q", updated.ResultMessage)
	}
}

func TestRecordTelemetryConfirmsAckedReturnToLaunchCommand(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	command, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeReturnToLaunch,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := reg.NextCommandForAgent("agent-001", now.Add(time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	if _, err := reg.UpdateCommandStatus(UpdateCommandStatusInput{
		AgentID:       "agent-001",
		CommandID:     command.ID,
		State:         domain.CommandStateVehicleAcked,
		ResultMessage: "accepted by vehicle",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if _, err := reg.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:        "agent-001",
		ObservedAt:     now.Add(3 * time.Second),
		BatteryPercent: 82,
		FlightMode:     "Return to Launch",
		GPSFix:         "3D",
		Source:         "px4",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	updated, ok := reg.CommandByID(command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if updated.State != domain.CommandStateTelemetryConfirmed {
		t.Fatalf("expected telemetry_confirmed, got %q", updated.State)
	}
}

func TestRecordTelemetryConfirmsAckedLandCommand(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	if _, err := reg.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:        "agent-001",
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

	command, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeLand,
		RequestedBy: "operator-001",
	}, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := reg.NextCommandForAgent("agent-001", now.Add(3*time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	if _, err := reg.UpdateCommandStatus(UpdateCommandStatusInput{
		AgentID:       "agent-001",
		CommandID:     command.ID,
		State:         domain.CommandStateVehicleAcked,
		ResultMessage: "accepted by vehicle",
	}, now.Add(4*time.Second)); err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if _, err := reg.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:        "agent-001",
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

	updated, ok := reg.CommandByID(command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if updated.State != domain.CommandStateTelemetryConfirmed {
		t.Fatalf("expected telemetry_confirmed, got %q", updated.State)
	}
}

func TestRecordTelemetryDoesNotConfirmSupersededTakeoffCommand(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	first, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeTakeoff,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	if _, ok, err := reg.NextCommandForAgent("agent-001", now.Add(time.Second)); err != nil {
		t.Fatalf("next command: %v", err)
	} else if !ok {
		t.Fatal("expected pending command")
	}

	if _, err := reg.UpdateCommandStatus(UpdateCommandStatusInput{
		AgentID:       "agent-001",
		CommandID:     first.ID,
		State:         domain.CommandStateVehicleAcked,
		ResultMessage: "accepted by vehicle",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("update command status: %v", err)
	}

	second, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeTakeoff,
		RequestedBy: "operator-001",
	}, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("request second command: %v", err)
	}

	if _, ok, err := reg.NextCommandForAgent("agent-001", now.Add(4*time.Second)); err != nil {
		t.Fatalf("next second command: %v", err)
	} else if !ok {
		t.Fatal("expected second pending command")
	}

	if _, err := reg.UpdateCommandStatus(UpdateCommandStatusInput{
		AgentID:       "agent-001",
		CommandID:     second.ID,
		State:         domain.CommandStateVehicleAcked,
		ResultMessage: "accepted by vehicle",
	}, now.Add(5*time.Second)); err != nil {
		t.Fatalf("update second command status: %v", err)
	}

	firstAfterSecondAck, ok := reg.CommandByID(first.ID)
	if !ok {
		t.Fatal("expected first stored command")
	}

	if firstAfterSecondAck.State != domain.CommandStateFailed {
		t.Fatalf("expected first command to be superseded as failed, got %q", firstAfterSecondAck.State)
	}

	if _, err := reg.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:        "agent-001",
		ObservedAt:     now.Add(6 * time.Second),
		BatteryPercent: 82,
		FlightMode:     "HOLD",
		InAir:          true,
		GPSFix:         "3D",
		Source:         "px4",
	}, now.Add(6*time.Second)); err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	updatedFirst, ok := reg.CommandByID(first.ID)
	if !ok {
		t.Fatal("expected first stored command")
	}

	if updatedFirst.State != domain.CommandStateFailed {
		t.Fatalf("expected first command to stay failed, got %q", updatedFirst.State)
	}

	updatedSecond, ok := reg.CommandByID(second.ID)
	if !ok {
		t.Fatal("expected second stored command")
	}

	if updatedSecond.State != domain.CommandStateTelemetryConfirmed {
		t.Fatalf("expected second command to confirm, got %q", updatedSecond.State)
	}
}

func TestRecordTelemetryDoesNotConfirmCommandBeforeVehicleAck(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	command, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	claimed, ok, err := reg.NextCommandForAgent("agent-001", now.Add(time.Second))
	if err != nil {
		t.Fatalf("next command: %v", err)
	}
	if !ok {
		t.Fatal("expected pending command")
	}

	if _, err := reg.UpdateCommandStatus(UpdateCommandStatusInput{
		AgentID:   "agent-001",
		CommandID: command.ID,
		State:     domain.CommandStateAgentReceived,
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if _, err := reg.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:        "agent-001",
		ObservedAt:     now.Add(3 * time.Second),
		BatteryPercent: 82,
		FlightMode:     "HOLD",
		Armed:          true,
		GPSFix:         "3D",
		Source:         "px4",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	updated, ok := reg.CommandByID(command.ID)
	if !ok {
		t.Fatal("expected stored command")
	}

	if updated.State != domain.CommandStateAgentReceived {
		t.Fatalf("expected command to remain agent_received, got %q", updated.State)
	}

	if claimed.State != domain.CommandStateSentToAgent {
		t.Fatalf("expected claimed command state sent_to_agent, got %q", claimed.State)
	}
}

func TestListCommandsForDroneReturnsNewestFirstWithLimit(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	if _, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now); err != nil {
		t.Fatalf("request first command: %v", err)
	}

	second, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeLand,
		RequestedBy: "operator-001",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request second command: %v", err)
	}

	commands, err := reg.ListCommandsForDrone("drone-001", 1)
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

func TestUpdateCommandStatusClearsDeliveryLeaseOnAck(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	command, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	claimed, ok, err := reg.NextCommandForAgent("agent-001", now.Add(time.Second))
	if err != nil {
		t.Fatalf("next command: %v", err)
	}
	if !ok {
		t.Fatal("expected pending command")
	}
	if claimed.LeaseUntil.IsZero() {
		t.Fatal("expected delivery lease")
	}

	updated, err := reg.UpdateCommandStatus(UpdateCommandStatusInput{
		AgentID:   "agent-001",
		CommandID: command.ID,
		State:     domain.CommandStateAgentReceived,
	}, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("update command status: %v", err)
	}

	if updated.State != domain.CommandStateAgentReceived {
		t.Fatalf("expected agent_received, got %q", updated.State)
	}

	if !updated.LeaseUntil.IsZero() {
		t.Fatalf("expected lease to be cleared after ACK, got %s", updated.LeaseUntil)
	}
}

func TestUpdateCommandStatusRejectsResultBeforeCommandIsClaimed(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	command, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	_, err = reg.UpdateCommandStatus(UpdateCommandStatusInput{
		AgentID:   "agent-001",
		CommandID: command.ID,
		State:     domain.CommandStateVehicleAcked,
	}, now.Add(time.Second))
	if err != ErrInvalidCommandTransition {
		t.Fatalf("expected invalid transition error, got %v", err)
	}
}

func TestUpdateCommandStatusRejectsNonAgentState(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	command, err := reg.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("request command: %v", err)
	}

	for _, state := range []domain.CommandState{
		domain.CommandStateAuthorized,
		domain.CommandStateTelemetryConfirmed,
	} {
		_, err = reg.UpdateCommandStatus(UpdateCommandStatusInput{
			AgentID:   "agent-001",
			CommandID: command.ID,
			State:     state,
		}, now.Add(time.Second))
		if err != ErrInvalidCommandState {
			t.Fatalf("expected invalid state error for %q, got %v", state, err)
		}
	}
}

func TestCreateMissionValidatesAndStoresMission(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	speed := 6.5
	loiterTime := 12.0
	mission, err := reg.CreateMission(CreateMissionInput{
		DroneID:          "drone-001",
		Name:             "Training loop",
		CreatedBy:        "operator-001",
		CompletionAction: domain.MissionCompletionActionLand,
		Waypoints: []MissionWaypointInput{
			{Latitude: 51.5074, Longitude: -0.1278, RelativeAltitudeM: 30, SpeedMPS: &speed, LoiterTimeS: &loiterTime},
			{Latitude: 51.5078, Longitude: -0.1282, RelativeAltitudeM: 35},
		},
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	if mission.ValidationStatus != domain.MissionValidationStatusValidated {
		t.Fatalf("expected validated mission, got %q with errors %#v", mission.ValidationStatus, mission.ValidationErrors)
	}

	if mission.ID != "msn-000001" {
		t.Fatalf("expected first mission ID, got %q", mission.ID)
	}

	if len(mission.Waypoints) != 2 {
		t.Fatalf("expected two waypoints, got %d", len(mission.Waypoints))
	}

	if mission.Waypoints[0].Sequence != 1 || mission.Waypoints[1].Sequence != 2 {
		t.Fatalf("expected waypoint sequence numbers, got %d and %d", mission.Waypoints[0].Sequence, mission.Waypoints[1].Sequence)
	}

	if mission.CompletionAction != domain.MissionCompletionActionLand {
		t.Fatalf("expected land completion action, got %q", mission.CompletionAction)
	}

	if mission.Waypoints[0].LoiterTimeS == nil || *mission.Waypoints[0].LoiterTimeS != loiterTime {
		t.Fatalf("expected waypoint loiter time %f, got %v", loiterTime, mission.Waypoints[0].LoiterTimeS)
	}

	missions, err := reg.ListMissionsForDrone("drone-001")
	if err != nil {
		t.Fatalf("list missions: %v", err)
	}

	if len(missions) != 1 {
		t.Fatalf("expected one mission, got %d", len(missions))
	}
}

func TestCreateMissionRejectsUnsafeMissionWithValidationErrors(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	mission, err := reg.CreateMission(CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "",
		CreatedBy: "operator-001",
		Waypoints: []MissionWaypointInput{
			{Latitude: 91, Longitude: -0.1278, RelativeAltitudeM: 0},
		},
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	if mission.ValidationStatus != domain.MissionValidationStatusRejected {
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
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	mission, err := reg.CreateMission(CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "Training loop",
		CreatedBy: "operator-001",
		Waypoints: []MissionWaypointInput{
			{Latitude: 51.5074, Longitude: -0.1278, RelativeAltitudeM: 30},
		},
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	if mission.CompletionAction != domain.MissionCompletionActionReturnToLaunch {
		t.Fatalf("expected return_to_launch default, got %q", mission.CompletionAction)
	}
}

func TestCreateMissionRejectsInvalidExpandedMissionFields(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	registerReadyAgent(t, reg, now)

	negativeLoiter := -1.0
	waypoints := make([]MissionWaypointInput, MaximumMissionWaypoints+1)
	for i := range waypoints {
		waypoints[i] = MissionWaypointInput{Latitude: 51.5074, Longitude: -0.1278, RelativeAltitudeM: 30}
	}
	waypoints[0].LoiterTimeS = &negativeLoiter

	mission, err := reg.CreateMission(CreateMissionInput{
		DroneID:          "drone-001",
		Name:             "Training loop",
		CreatedBy:        "operator-001",
		CompletionAction: domain.MissionCompletionAction("orbit_forever"),
		Waypoints:        waypoints,
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	if mission.ValidationStatus != domain.MissionValidationStatusRejected {
		t.Fatalf("expected rejected mission, got %q", mission.ValidationStatus)
	}

	assertMissionValidationError(t, mission.ValidationErrors, "waypoints")
	assertMissionValidationError(t, mission.ValidationErrors, "completionAction")
	assertMissionValidationError(t, mission.ValidationErrors, "waypoints[0].loiterTimeS")
}

func TestRequestMissionUploadCreatesExecutionForValidatedMission(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, reg, now)

	execution, err := reg.RequestMissionUpload(RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "operator-001",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if execution.ID != "mex-000001" {
		t.Fatalf("expected first execution ID, got %q", execution.ID)
	}

	if execution.MissionID != mission.ID {
		t.Fatalf("expected mission %q, got %q", mission.ID, execution.MissionID)
	}

	if execution.State != domain.MissionExecutionStateUploadRequested {
		t.Fatalf("expected upload_requested, got %q", execution.State)
	}

	if execution.AgentID != "agent-001" {
		t.Fatalf("expected agent-001, got %q", execution.AgentID)
	}

	if execution.UploadRequestedBy != "operator-001" {
		t.Fatalf("expected upload requester operator-001, got %q", execution.UploadRequestedBy)
	}
}

func TestRequestMissionUploadRejectsUnvalidatedMission(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	mission, err := reg.CreateMission(CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "",
		CreatedBy: "operator-001",
	}, now)
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	_, err = reg.RequestMissionUpload(RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "operator-001",
	}, now.Add(time.Second))
	if err != ErrMissionNotValidated {
		t.Fatalf("expected mission not validated error, got %v", err)
	}
}

func TestRequestMissionStartRequiresUploadedExecution(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, reg, now)

	if _, err := reg.RequestMissionUpload(RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "operator-001",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	_, err := reg.RequestMissionStart(RequestMissionStartInput{
		MissionID:   mission.ID,
		RequestedBy: "operator-001",
	}, now.Add(2*time.Second))
	if err != ErrInvalidMissionExecutionState {
		t.Fatalf("expected invalid execution state error, got %v", err)
	}
}

func TestRequestMissionStartMovesUploadedExecutionToStartRequested(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, reg, now)

	execution, err := reg.RequestMissionUpload(RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "upload-operator",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, err := reg.RecordMissionExecutionUploaded(execution.ID, "uploaded to vehicle", now.Add(2*time.Second)); err != nil {
		t.Fatalf("record mission uploaded: %v", err)
	}

	started, err := reg.RequestMissionStart(RequestMissionStartInput{
		MissionID:   mission.ID,
		RequestedBy: "start-operator",
	}, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("request mission start: %v", err)
	}

	if started.ID != execution.ID {
		t.Fatalf("expected execution %q, got %q", execution.ID, started.ID)
	}

	if started.State != domain.MissionExecutionStateStartRequested {
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
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, reg, now)

	execution, err := reg.RequestMissionUpload(RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "upload-operator",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, err := reg.RecordMissionExecutionUploaded(execution.ID, "uploaded to vehicle", now.Add(2*time.Second)); err != nil {
		t.Fatalf("record mission uploaded: %v", err)
	}

	started, err := reg.RequestMissionStart(RequestMissionStartInput{
		MissionID:   mission.ID,
		RequestedBy: "start-operator",
	}, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("request mission start: %v", err)
	}

	if started.State != domain.MissionExecutionStateStartRequested {
		t.Fatalf("expected start_requested, got %q", started.State)
	}
}

func TestUpdateMissionExecutionStatusStoresProgressCompletionAndHold(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, reg, now)

	execution, err := reg.RequestMissionUpload(RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "upload-operator",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, err := reg.RecordMissionExecutionUploaded(execution.ID, "uploaded to vehicle", now.Add(2*time.Second)); err != nil {
		t.Fatalf("record mission uploaded: %v", err)
	}
	recordAirborneTelemetry(t, reg, now.Add(2500*time.Millisecond))

	if _, err := reg.RequestMissionStart(RequestMissionStartInput{
		MissionID:   mission.ID,
		RequestedBy: "start-operator",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("request mission start: %v", err)
	}

	active, err := reg.UpdateMissionExecutionStatus(UpdateMissionExecutionStatusInput{
		AgentID:            "agent-001",
		ExecutionID:        execution.ID,
		State:              domain.MissionExecutionStateActive,
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

	completed, err := reg.UpdateMissionExecutionStatus(UpdateMissionExecutionStatusInput{
		AgentID:            "agent-001",
		ExecutionID:        execution.ID,
		State:              domain.MissionExecutionStateCompleted,
		ResultMessage:      "mission completed 6/6",
		CurrentMissionItem: 6,
		TotalMissionItems:  6,
	}, now.Add(5*time.Second))
	if err != nil {
		t.Fatalf("update mission completed: %v", err)
	}

	if completed.State != domain.MissionExecutionStateCompleted || completed.CompletedAt.IsZero() {
		t.Fatalf("expected completed execution with timestamp, got %#v", completed)
	}

	hold, err := reg.UpdateMissionExecutionStatus(UpdateMissionExecutionStatusInput{
		AgentID:            "agent-001",
		ExecutionID:        execution.ID,
		State:              domain.MissionExecutionStateHold,
		ResultMessage:      "mission complete; holding at final waypoint",
		CurrentMissionItem: 6,
		TotalMissionItems:  6,
	}, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("update mission hold: %v", err)
	}

	if hold.State != domain.MissionExecutionStateHold {
		t.Fatalf("expected hold state, got %q", hold.State)
	}

	if hold.HoldAt.IsZero() {
		t.Fatal("expected hold timestamp")
	}

	snapshots := reg.ListDrones(now.Add(7 * time.Second))
	if len(snapshots) != 1 {
		t.Fatalf("expected one drone snapshot, got %d", len(snapshots))
	}

	if snapshots[0].LatestMissionExecution.ID != execution.ID {
		t.Fatalf("expected latest mission execution %q, got %q", execution.ID, snapshots[0].LatestMissionExecution.ID)
	}

	if snapshots[0].LatestMissionExecution.State != domain.MissionExecutionStateHold {
		t.Fatalf("expected latest mission state hold, got %q", snapshots[0].LatestMissionExecution.State)
	}
}

func TestRequestMissionAbortRequestsRTLAndBlocksNewUpload(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, reg, now)

	execution, err := reg.RequestMissionUpload(RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "upload-operator",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, err := reg.RecordMissionExecutionUploaded(execution.ID, "uploaded to vehicle", now.Add(2*time.Second)); err != nil {
		t.Fatalf("record mission uploaded: %v", err)
	}
	recordAirborneTelemetry(t, reg, now.Add(2500*time.Millisecond))

	if _, err := reg.RequestMissionStart(RequestMissionStartInput{
		MissionID:   mission.ID,
		RequestedBy: "start-operator",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("request mission start: %v", err)
	}

	if _, err := reg.UpdateMissionExecutionStatus(UpdateMissionExecutionStatusInput{
		AgentID:       "agent-001",
		ExecutionID:   execution.ID,
		State:         domain.MissionExecutionStateActive,
		ResultMessage: "mission started",
	}, now.Add(4*time.Second)); err != nil {
		t.Fatalf("mark mission active: %v", err)
	}

	aborted, err := reg.RequestMissionAbort(RequestMissionAbortInput{
		MissionID:   mission.ID,
		RequestedBy: "safety-operator",
	}, now.Add(5*time.Second))
	if err != nil {
		t.Fatalf("request mission abort: %v", err)
	}

	if aborted.ID != execution.ID {
		t.Fatalf("expected abort on execution %q, got %q", execution.ID, aborted.ID)
	}

	if aborted.State != domain.MissionExecutionStateRTLRequested {
		t.Fatalf("expected rtl_requested, got %q", aborted.State)
	}

	claimed, err := reg.ClaimMissionExecutionForAgent("agent-001", aborted.ID, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("claim abort execution: %v", err)
	}

	if claimed.State != domain.MissionExecutionStateRTLRequested {
		t.Fatalf("expected claimed rtl_requested, got %q", claimed.State)
	}

	if _, err := reg.UpdateMissionExecutionStatus(UpdateMissionExecutionStatusInput{
		AgentID:       "agent-001",
		ExecutionID:   aborted.ID,
		State:         domain.MissionExecutionStateRTLRequested,
		ResultMessage: "RTL accepted by vehicle; mission abort in progress",
	}, now.Add(7*time.Second)); err != nil {
		t.Fatalf("ack abort execution: %v", err)
	}

	if redelivered, ok, err := reg.NextMissionExecutionForAgent("agent-001", now.Add(8*time.Second)); err != nil {
		t.Fatalf("lookup pending abort execution: %v", err)
	} else if ok {
		t.Fatalf("expected acknowledged abort not to redeliver, got %#v", redelivered)
	}

	secondMission, err := reg.CreateMission(CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "Second route",
		CreatedBy: "operator-001",
		Waypoints: []MissionWaypointInput{
			{Latitude: 51.5075, Longitude: -0.1279, RelativeAltitudeM: 30},
		},
	}, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("create second mission: %v", err)
	}

	_, err = reg.RequestMissionUpload(RequestMissionUploadInput{
		MissionID:   secondMission.ID,
		RequestedBy: "operator-001",
	}, now.Add(7*time.Second))
	if err != ErrDroneMissionActive {
		t.Fatalf("expected active mission error, got %v", err)
	}
}

func TestMissionExecutionEventsAreRecordedChronologically(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	mission := createValidatedMission(t, reg, now)

	execution, err := reg.RequestMissionUpload(RequestMissionUploadInput{
		MissionID:   mission.ID,
		RequestedBy: "upload-operator",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("request mission upload: %v", err)
	}

	if _, err := reg.ClaimMissionExecutionForAgent("agent-001", execution.ID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("claim mission upload: %v", err)
	}

	if _, err := reg.UpdateMissionExecutionStatus(UpdateMissionExecutionStatusInput{
		AgentID:       "agent-001",
		ExecutionID:   execution.ID,
		State:         domain.MissionExecutionStateUploading,
		ResultMessage: "uploading",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("update mission uploading: %v", err)
	}

	if _, err := reg.UpdateMissionExecutionStatus(UpdateMissionExecutionStatusInput{
		AgentID:       "agent-001",
		ExecutionID:   execution.ID,
		State:         domain.MissionExecutionStateUploadedToVehicle,
		ResultMessage: "uploaded to vehicle",
	}, now.Add(4*time.Second)); err != nil {
		t.Fatalf("update mission uploaded: %v", err)
	}

	events, err := reg.ListMissionExecutionEvents(mission.ID)
	if err != nil {
		t.Fatalf("list mission execution events: %v", err)
	}

	assertMissionEventTypes(t, events, []string{
		"upload_requested",
		"sent_to_agent",
		"uploading",
		"uploaded_to_vehicle",
	})
}

func assertMissionValidationError(t *testing.T, validationErrors []domain.MissionValidationError, field string) {
	t.Helper()

	for _, validationError := range validationErrors {
		if validationError.Field == field {
			return
		}
	}

	t.Fatalf("expected validation error for %q in %#v", field, validationErrors)
}

func assertMissionEventTypes(t *testing.T, events []domain.MissionExecutionEvent, want []string) {
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

func createValidatedMission(t *testing.T, reg *MemoryRegistry, now time.Time) domain.Mission {
	t.Helper()

	registerReadyAgent(t, reg, now)

	mission, err := reg.CreateMission(CreateMissionInput{
		DroneID:   "drone-001",
		Name:      "Training loop",
		CreatedBy: "operator-001",
		Waypoints: []MissionWaypointInput{
			{Latitude: 51.5074, Longitude: -0.1278, RelativeAltitudeM: 30},
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

func registerReadyAgent(t *testing.T, reg *MemoryRegistry, now time.Time) {
	t.Helper()

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	if _, err := reg.RecordHeartbeat(HeartbeatInput{
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

func recordAirborneTelemetry(t *testing.T, reg *MemoryRegistry, now time.Time) {
	t.Helper()

	if _, err := reg.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:         "agent-001",
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
