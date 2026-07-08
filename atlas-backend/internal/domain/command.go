package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

// AuthorizeCommand builds the initial command state from the active agent and
// latest telemetry observed for the target drone.
func AuthorizeCommand(id string, droneID string, commandType models.CommandType, requestedBy string, agent models.VehicleAgent, telemetry models.TelemetrySnapshot, now time.Time) models.CommandRequest {
	agentStatus := models.VehicleAgentStatusFromHeartbeat(agent.LastHeartbeatAt, now)
	telemetryState := models.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now)

	command := models.CommandRequest{
		ID:                 id,
		DroneID:            droneID,
		VehicleAgentID:     agent.ID,
		Type:               commandType,
		State:              models.CommandStateAuthorized,
		RequestedBy:        requestedBy,
		RequestedAt:        now,
		UpdatedAt:          now,
		TelemetryState:     telemetryState,
		VehicleAgentStatus: agentStatus,
	}

	if agentStatus != models.VehicleAgentStatusOnline {
		command.State = models.CommandStateRejectedByPolicy
		command.PolicyReason = "agent must be online"
	} else if telemetryState != models.TelemetryStateFresh {
		command.State = models.CommandStateRejectedByPolicy
		command.PolicyReason = "telemetry must be fresh"
	}

	return command
}

// IsVehicleAgentReportedCommandState rejects backend-only states from agent status APIs.
func IsVehicleAgentReportedCommandState(state models.CommandState) bool {
	switch state {
	case models.CommandStateVehicleAgentReceived,
		models.CommandStateSentToVehicle,
		models.CommandStateVehicleAcked,
		models.CommandStateVehicleRejected,
		models.CommandStateTimedOut,
		models.CommandStateFailed:
		return true
	default:
		return false
	}
}

// CommandDeliverable treats an expired sent-to-agent lease as available for
// redelivery. The database query may pre-filter candidates, but this rule is the
// source of truth for explicit claim paths.
func CommandDeliverable(command models.CommandRequest, now time.Time) bool {
	if command.State == models.CommandStateAuthorized {
		return true
	}

	return command.State == models.CommandStateSentToVehicleAgent &&
		!command.LeaseUntil.IsZero() &&
		!command.LeaseUntil.After(now)
}

func MarkCommandSentToVehicleAgent(command models.CommandRequest, now time.Time) models.CommandRequest {
	command.State = models.CommandStateSentToVehicleAgent
	command.UpdatedAt = now
	command.LastSentAt = now
	command.LeaseUntil = now.Add(models.CommandDeliveryLeaseDuration)
	command.DeliveryAttempt++
	return command
}

func CanApplyVehicleAgentReportedCommandState(current models.CommandState, next models.CommandState) bool {
	switch current {
	case models.CommandStateSentToVehicleAgent:
		return next == models.CommandStateVehicleAgentReceived ||
			next == models.CommandStateSentToVehicle ||
			isTerminalAgentCommandState(next)
	case models.CommandStateVehicleAgentReceived:
		return next == models.CommandStateSentToVehicle ||
			isTerminalAgentCommandState(next)
	case models.CommandStateSentToVehicle:
		return isTerminalAgentCommandState(next)
	default:
		return false
	}
}

// ApplyVehicleAgentReportedCommandState records the agent's state report. Validation is
// intentionally separate so callers can map invalid states and invalid
// transitions to distinct API errors.
func ApplyVehicleAgentReportedCommandState(command models.CommandRequest, state models.CommandState, resultMessage string, confirmationBaseline models.TelemetrySnapshot, now time.Time) models.CommandRequest {
	command.State = state
	command.ResultMessage = resultMessage
	command.UpdatedAt = now
	command.LeaseUntil = time.Time{}
	if state == models.CommandStateVehicleAcked {
		command.VehicleAckedAt = now
		command.ConfirmationBaseline = confirmationBaseline
	}
	return command
}

func MarkCommandSupersededBy(command models.CommandRequest, newer models.CommandRequest, now time.Time) models.CommandRequest {
	command.State = models.CommandStateFailed
	command.UpdatedAt = now
	command.ResultMessage = fmt.Sprintf("superseded by newer %s command", newer.Type)
	return command
}

// TelemetryConfirmsCommand only accepts telemetry observed after the vehicle
// acknowledgement, preventing old snapshots from confirming a newly acked command.
func TelemetryConfirmsCommand(command models.CommandRequest, snapshot models.TelemetrySnapshot) bool {
	if !telemetryObservedAfterVehicleAck(command, snapshot) {
		return false
	}

	baseline := command.ConfirmationBaseline
	switch command.Type {
	case models.CommandTypeArm:
		return !baseline.Armed && snapshot.Armed
	case models.CommandTypeTakeoff:
		return !baseline.InAir && snapshot.InAir
	case models.CommandTypeLand:
		return (!flightModeIsLand(baseline.FlightMode) && flightModeIsLand(snapshot.FlightMode)) ||
			(baseline.InAir && !snapshot.InAir) ||
			(baseline.Armed && !snapshot.Armed)
	case models.CommandTypeReturnToLaunch:
		return !flightModeIsReturn(baseline.FlightMode) && flightModeIsReturn(snapshot.FlightMode)
	default:
		return false
	}
}

func MarkCommandTelemetryConfirmed(command models.CommandRequest, now time.Time) models.CommandRequest {
	command.State = models.CommandStateTelemetryConfirmed
	command.UpdatedAt = now
	command.ResultMessage = "confirmed by telemetry"
	return command
}

func telemetryObservedAfterVehicleAck(command models.CommandRequest, snapshot models.TelemetrySnapshot) bool {
	if command.VehicleAckedAt.IsZero() {
		return true
	}

	observedAt := snapshot.ObservedAt
	if observedAt.IsZero() {
		observedAt = snapshot.ReceivedAt
	}

	return !observedAt.Before(command.VehicleAckedAt)
}

func isTerminalAgentCommandState(state models.CommandState) bool {
	switch state {
	case models.CommandStateVehicleAcked,
		models.CommandStateVehicleRejected,
		models.CommandStateTimedOut,
		models.CommandStateFailed:
		return true
	default:
		return false
	}
}

func flightModeIsReturn(mode string) bool {
	normalized := strings.ToLower(mode)
	return strings.Contains(normalized, "return") || strings.Contains(normalized, "rtl")
}

func flightModeIsLand(mode string) bool {
	normalized := strings.ToLower(mode)
	return strings.Contains(normalized, "land")
}
