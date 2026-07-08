package domain

import (
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

func TestAuthorizeCommandUsesAgentAndTelemetryPolicy(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	onlineAgent := models.VehicleAgent{ID: "agent-1", LastHeartbeatAt: now}
	freshTelemetry := models.TelemetrySnapshot{ReceivedAt: now}

	tests := []struct {
		name       string
		agent      models.VehicleAgent
		telemetry  models.TelemetrySnapshot
		wantState  models.CommandState
		wantReason string
	}{
		{
			name:      "authorized when agent is online and telemetry is fresh",
			agent:     onlineAgent,
			telemetry: freshTelemetry,
			wantState: models.CommandStateAuthorized,
		},
		{
			name:       "rejected when agent is not online",
			agent:      models.VehicleAgent{ID: "agent-1"},
			telemetry:  freshTelemetry,
			wantState:  models.CommandStateRejectedByPolicy,
			wantReason: "agent must be online",
		},
		{
			name:       "rejected when telemetry is stale",
			agent:      onlineAgent,
			telemetry:  models.TelemetrySnapshot{ReceivedAt: now.Add(-(models.TelemetryFreshWindow + time.Second))},
			wantState:  models.CommandStateRejectedByPolicy,
			wantReason: "telemetry must be fresh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := AuthorizeCommand("cmd-1", "drone-1", models.CommandTypeArm, "operator", tt.agent, tt.telemetry, now)
			if command.State != tt.wantState {
				t.Fatalf("expected state %q, got %q", tt.wantState, command.State)
			}
			if command.PolicyReason != tt.wantReason {
				t.Fatalf("expected policy reason %q, got %q", tt.wantReason, command.PolicyReason)
			}
		})
	}
}

func TestApplyVehicleAgentReportedCommandStateStoresAckBaselineAndClearsLease(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	command := models.CommandRequest{
		State:      models.CommandStateSentToVehicle,
		LeaseUntil: now.Add(time.Minute),
	}
	baseline := models.TelemetrySnapshot{Armed: false, ReceivedAt: now}

	got := ApplyVehicleAgentReportedCommandState(command, models.CommandStateVehicleAcked, "accepted", baseline, now)

	if got.State != models.CommandStateVehicleAcked {
		t.Fatalf("expected vehicle acked, got %q", got.State)
	}
	if !got.LeaseUntil.IsZero() {
		t.Fatalf("expected lease to be cleared")
	}
	if got.VehicleAckedAt != now {
		t.Fatalf("expected vehicle ack timestamp %v, got %v", now, got.VehicleAckedAt)
	}
	if got.ConfirmationBaseline != baseline {
		t.Fatalf("expected confirmation baseline to be stored")
	}
}

func TestTelemetryConfirmsCommandOnlyAfterVehicleAckObservation(t *testing.T) {
	ackedAt := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	command := models.CommandRequest{
		Type:                 models.CommandTypeArm,
		VehicleAckedAt:       ackedAt,
		ConfirmationBaseline: models.TelemetrySnapshot{Armed: false},
	}

	if TelemetryConfirmsCommand(command, models.TelemetrySnapshot{Armed: true, ObservedAt: ackedAt.Add(-time.Second)}) {
		t.Fatalf("expected pre-ack telemetry to be ignored")
	}
	if !TelemetryConfirmsCommand(command, models.TelemetrySnapshot{Armed: true, ObservedAt: ackedAt.Add(time.Second)}) {
		t.Fatalf("expected post-ack armed telemetry to confirm command")
	}
}
