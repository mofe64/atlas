package domain

import (
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

func TestAuthorizeVehicleActionUsesAgentAndTelemetryPolicy(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	onlineAgent := models.VehicleAgent{ID: "agent-1", LastHeartbeatAt: now}
	freshTelemetry := models.TelemetrySnapshot{ReceivedAt: now}

	tests := []struct {
		name       string
		agent      models.VehicleAgent
		telemetry  models.TelemetrySnapshot
		wantState  models.VehicleActionState
		wantReason string
	}{
		{
			name:      "authorized when agent is online and telemetry is fresh",
			agent:     onlineAgent,
			telemetry: freshTelemetry,
			wantState: models.VehicleActionStateAuthorized,
		},
		{
			name:       "rejected when agent is not online",
			agent:      models.VehicleAgent{ID: "agent-1"},
			telemetry:  freshTelemetry,
			wantState:  models.VehicleActionStateRejectedByPolicy,
			wantReason: "agent must be online",
		},
		{
			name:       "rejected when telemetry is stale",
			agent:      onlineAgent,
			telemetry:  models.TelemetrySnapshot{ReceivedAt: now.Add(-(models.TelemetryFreshWindow + time.Second))},
			wantState:  models.VehicleActionStateRejectedByPolicy,
			wantReason: "telemetry must be fresh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := AuthorizeVehicleAction("cmd-1", "drone-1", models.VehicleActionTypeArm, "operator", tt.agent, tt.telemetry, now)
			if command.State != tt.wantState {
				t.Fatalf("expected state %q, got %q", tt.wantState, command.State)
			}
			if command.PolicyReason != tt.wantReason {
				t.Fatalf("expected policy reason %q, got %q", tt.wantReason, command.PolicyReason)
			}
		})
	}
}

func TestCommandCommunicationLinkPolicyFailure(t *testing.T) {
	connectedCommandLink := models.CommunicationLink{
		Status:          models.CommunicationLinkStatusConnected,
		CommandEligible: true,
		Roles:           []models.CommunicationLinkRole{models.CommunicationLinkRoleCommand},
	}

	tests := []struct {
		name          string
		hasConnection bool
		hasLink       bool
		link          models.CommunicationLink
		wantFailed    bool
		wantReason    string
	}{
		{
			name:          "passes with active connected command link",
			hasConnection: true,
			hasLink:       true,
			link:          connectedCommandLink,
			wantFailed:    false,
		},
		{
			name:          "fails without connection",
			hasConnection: false,
			hasLink:       false,
			wantFailed:    true,
			wantReason:    "active drone vehicle agent connection is required",
		},
		{
			name:          "fails without link",
			hasConnection: true,
			hasLink:       false,
			wantFailed:    true,
			wantReason:    "active communication link is required",
		},
		{
			name:          "fails when link is not connected",
			hasConnection: true,
			hasLink:       true,
			link:          models.CommunicationLink{Status: models.CommunicationLinkStatusLost, CommandEligible: true, Roles: []models.CommunicationLinkRole{models.CommunicationLinkRoleCommand}},
			wantFailed:    true,
			wantReason:    "communication link must be connected",
		},
		{
			name:          "fails without command role",
			hasConnection: true,
			hasLink:       true,
			link:          models.CommunicationLink{Status: models.CommunicationLinkStatusConnected, CommandEligible: true, Roles: []models.CommunicationLinkRole{models.CommunicationLinkRoleTelemetry}},
			wantFailed:    true,
			wantReason:    "communication link must include command role",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, failed := CommandCommunicationLinkPolicyFailure(tt.hasConnection, tt.hasLink, tt.link)
			if failed != tt.wantFailed {
				t.Fatalf("expected failed=%v, got %v", tt.wantFailed, failed)
			}
			if reason != tt.wantReason {
				t.Fatalf("expected reason %q, got %q", tt.wantReason, reason)
			}
		})
	}
}

func TestApplyVehicleAgentReportedVehicleActionStateStoresAckBaselineAndClearsLease(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	command := models.VehicleAction{
		State:      models.VehicleActionStateSentToVehicle,
		LeaseUntil: now.Add(time.Minute),
	}
	baseline := models.TelemetrySnapshot{Armed: false, ReceivedAt: now}

	got := ApplyVehicleAgentReportedVehicleActionState(command, models.VehicleActionStateVehicleAcked, "accepted", "ack-1", "MAV_RESULT_ACCEPTED", baseline, now)

	if got.State != models.VehicleActionStateVehicleAcked {
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
	if got.AckCorrelationID != "ack-1" {
		t.Fatalf("expected ACK correlation id to be stored")
	}
	if got.RawAckCode != "MAV_RESULT_ACCEPTED" {
		t.Fatalf("expected raw ACK code to be stored")
	}
}

func TestTelemetryConfirmsVehicleActionOnlyAfterVehicleAckObservation(t *testing.T) {
	ackedAt := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	command := models.VehicleAction{
		Type:                 models.VehicleActionTypeArm,
		VehicleAckedAt:       ackedAt,
		ConfirmationBaseline: models.TelemetrySnapshot{Armed: false},
	}

	if TelemetryConfirmsVehicleAction(command, models.TelemetrySnapshot{Armed: true, ObservedAt: ackedAt.Add(-time.Second)}) {
		t.Fatalf("expected pre-ack telemetry to be ignored")
	}
	if !TelemetryConfirmsVehicleAction(command, models.TelemetrySnapshot{Armed: true, ObservedAt: ackedAt.Add(time.Second)}) {
		t.Fatalf("expected post-ack armed telemetry to confirm command")
	}
}
