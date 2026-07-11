package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

// AuthorizeVehicleAction builds the initial vehicle action state from the active agent and
// latest telemetry observed for the target drone.
func AuthorizeVehicleAction(id string, droneID string, actionType models.VehicleActionType, requestedBy string, agent models.VehicleAgent, telemetry models.TelemetrySnapshot, now time.Time) models.VehicleAction {
	agentStatus := models.VehicleAgentStatusFromHeartbeat(agent.LastHeartbeatAt, now)
	telemetryState := models.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now)

	action := models.VehicleAction{
		ID:                 id,
		DroneID:            droneID,
		VehicleAgentID:     agent.ID,
		Type:               actionType,
		State:              models.VehicleActionStateAuthorized,
		RequestedBy:        requestedBy,
		DeliveryTarget:     models.VehicleActionDeliveryTargetVehicleAgent,
		AckCorrelationID:   id,
		RequestedAt:        now,
		UpdatedAt:          now,
		TelemetryState:     telemetryState,
		VehicleAgentStatus: agentStatus,
	}

	if agentStatus != models.VehicleAgentStatusOnline {
		action.State = models.VehicleActionStateRejectedByPolicy
		action.PolicyReason = "agent must be online"
	} else if telemetryState != models.TelemetryStateFresh {
		action.State = models.VehicleActionStateRejectedByPolicy
		action.PolicyReason = "telemetry must be fresh"
	}

	return action
}

// CommandCommunicationLinkPolicyFailure explains why the currently selected
// backend-to-agent path is not allowed to carry a vehicle action.
func CommandCommunicationLinkPolicyFailure(hasConnection bool, hasLink bool, link models.CommunicationLink) (string, bool) {
	if !hasConnection {
		return "active drone vehicle agent connection is required", true
	}
	if !hasLink {
		return "active communication link is required", true
	}
	if !link.EndedAt.IsZero() {
		return "communication link must be active", true
	}
	if !link.CommandEligible {
		return "communication link must be command eligible", true
	}
	if link.Status != models.CommunicationLinkStatusConnected {
		return "communication link must be connected", true
	}
	if !communicationLinkHasRole(link, models.CommunicationLinkRoleCommand) {
		return "communication link must include command role", true
	}

	return "", false
}

// IsVehicleAgentReportedVehicleActionState rejects backend-only states from agent status APIs.
func IsVehicleAgentReportedVehicleActionState(state models.VehicleActionState) bool {
	switch state {
	case models.VehicleActionStateVehicleAgentReceived,
		models.VehicleActionStateSentToVehicle,
		models.VehicleActionStateVehicleAcked,
		models.VehicleActionStateVehicleRejected,
		models.VehicleActionStateTimedOut,
		models.VehicleActionStateFailed:
		return true
	default:
		return false
	}
}

// VehicleActionDeliverable treats an expired sent-to-agent lease as available for
// redelivery. The database query may pre-filter candidates, but this rule is the
// source of truth for explicit claim paths.
func VehicleActionDeliverable(action models.VehicleAction, now time.Time) bool {
	if action.State == models.VehicleActionStateAuthorized {
		return action.UpdatedAt.IsZero() || !action.UpdatedAt.Before(now.Add(-models.VehicleActionAuthorizationTimeout))
	}

	return action.State == models.VehicleActionStateSentToVehicleAgent &&
		action.DeliveryAttempt < models.VehicleActionMaxDeliveryAttempts &&
		!action.LeaseUntil.IsZero() &&
		!action.LeaseUntil.After(now)
}

func MarkVehicleActionSentToVehicleAgent(action models.VehicleAction, now time.Time) models.VehicleAction {
	action.State = models.VehicleActionStateSentToVehicleAgent
	action.UpdatedAt = now
	if action.SentToVehicleAgentAt.IsZero() {
		action.SentToVehicleAgentAt = now
	}
	action.LastSentAt = now
	action.LeaseUntil = now.Add(models.VehicleActionDeliveryLeaseDuration)
	action.DeliveryAttempt++
	return action
}

func CanApplyVehicleAgentReportedVehicleActionState(current models.VehicleActionState, next models.VehicleActionState) bool {
	switch current {
	case models.VehicleActionStateSentToVehicleAgent:
		return next == models.VehicleActionStateVehicleAgentReceived ||
			next == models.VehicleActionStateSentToVehicle ||
			isTerminalAgentVehicleActionState(next)
	case models.VehicleActionStateVehicleAgentReceived:
		return next == models.VehicleActionStateSentToVehicle ||
			isTerminalAgentVehicleActionState(next)
	case models.VehicleActionStateSentToVehicle:
		return isTerminalAgentVehicleActionState(next)
	default:
		return false
	}
}

// ApplyVehicleAgentReportedVehicleActionState records the agent's state report. Validation is
// intentionally separate so callers can map invalid states and invalid
// transitions to distinct API errors.
func ApplyVehicleAgentReportedVehicleActionState(action models.VehicleAction, state models.VehicleActionState, resultMessage string, ackCorrelationID string, rawAckCode string, confirmationBaseline models.TelemetrySnapshot, now time.Time) models.VehicleAction {
	action.State = state
	action.ResultMessage = resultMessage
	if ackCorrelationID != "" {
		action.AckCorrelationID = ackCorrelationID
	}
	action.RawAckCode = rawAckCode
	action.UpdatedAt = now
	action.LeaseUntil = time.Time{}
	if state == models.VehicleActionStateVehicleAcked {
		action.VehicleAckedAt = now
		action.ConfirmationBaseline = confirmationBaseline
	}
	if state == models.VehicleActionStateVehicleRejected ||
		state == models.VehicleActionStateTimedOut ||
		state == models.VehicleActionStateFailed {
		action.FailedAt = now
		action.FailureReason = resultMessage
	}
	return action
}

func VehicleActionAckCorrelationMismatch(action models.VehicleAction, ackCorrelationID string) bool {
	return ackCorrelationID != "" && action.AckCorrelationID != "" && ackCorrelationID != action.AckCorrelationID
}

func MarkVehicleActionSupersededBy(action models.VehicleAction, newer models.VehicleAction, now time.Time) models.VehicleAction {
	action.State = models.VehicleActionStateFailed
	action.UpdatedAt = now
	action.ResultMessage = fmt.Sprintf("superseded by newer %s vehicle action", newer.Type)
	return action
}

func MarkVehicleActionTimedOut(action models.VehicleAction, resultMessage string, now time.Time) models.VehicleAction {
	action.State = models.VehicleActionStateTimedOut
	action.UpdatedAt = now
	action.FailedAt = now
	action.FailureReason = resultMessage
	action.ResultMessage = resultMessage
	action.LeaseUntil = time.Time{}
	return action
}

func MarkVehicleActionAckedButNotObserved(action models.VehicleAction, now time.Time) models.VehicleAction {
	action.State = models.VehicleActionStateAckedButNotObserved
	action.UpdatedAt = now
	action.FailedAt = now
	action.FailureReason = "vehicle acknowledged action but telemetry did not confirm it"
	action.ResultMessage = action.FailureReason
	return action
}

// TelemetryConfirmsVehicleAction only accepts telemetry observed after the vehicle
// acknowledgement, preventing old snapshots from confirming a newly acked vehicle action.
func TelemetryConfirmsVehicleAction(action models.VehicleAction, snapshot models.TelemetrySnapshot) bool {
	if !telemetryObservedAfterVehicleAck(action, snapshot) {
		return false
	}

	baseline := action.ConfirmationBaseline
	switch action.Type {
	case models.VehicleActionTypeArm:
		return !baseline.Armed && snapshot.Armed
	case models.VehicleActionTypeTakeoff:
		return !baseline.InAir && snapshot.InAir
	case models.VehicleActionTypeLand:
		return (!flightModeIsLand(baseline.FlightMode) && flightModeIsLand(snapshot.FlightMode)) ||
			(baseline.InAir && !snapshot.InAir) ||
			(baseline.Armed && !snapshot.Armed)
	case models.VehicleActionTypeReturnToLaunch:
		return !flightModeIsReturn(baseline.FlightMode) && flightModeIsReturn(snapshot.FlightMode)
	default:
		return false
	}
}

func MarkVehicleActionTelemetryConfirmed(action models.VehicleAction, now time.Time) models.VehicleAction {
	action.State = models.VehicleActionStateTelemetryConfirmed
	action.UpdatedAt = now
	action.CompletedAt = now
	action.ResultMessage = "confirmed by telemetry"
	return action
}

func telemetryObservedAfterVehicleAck(action models.VehicleAction, snapshot models.TelemetrySnapshot) bool {
	if action.VehicleAckedAt.IsZero() {
		return true
	}

	observedAt := snapshot.ObservedAt
	if observedAt.IsZero() {
		observedAt = snapshot.ReceivedAt
	}

	return !observedAt.Before(action.VehicleAckedAt)
}

func isTerminalAgentVehicleActionState(state models.VehicleActionState) bool {
	switch state {
	case models.VehicleActionStateVehicleAcked,
		models.VehicleActionStateVehicleRejected,
		models.VehicleActionStateAckedButNotObserved,
		models.VehicleActionStateTimedOut,
		models.VehicleActionStateFailed:
		return true
	default:
		return false
	}
}

func communicationLinkHasRole(link models.CommunicationLink, role models.CommunicationLinkRole) bool {
	for _, candidate := range link.Roles {
		if candidate == role {
			return true
		}
	}
	return false
}

func flightModeIsReturn(mode string) bool {
	normalized := strings.ToLower(mode)
	return strings.Contains(normalized, "return") || strings.Contains(normalized, "rtl")
}

func flightModeIsLand(mode string) bool {
	normalized := strings.ToLower(mode)
	return strings.Contains(normalized, "land")
}
