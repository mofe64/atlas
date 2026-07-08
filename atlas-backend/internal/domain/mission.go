package domain

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

const (
	MinimumMissionBatteryPercent float64 = 20
	MinimumMissionAltitudeM      float64 = 1
	MaximumMissionAltitudeM      float64 = 120
	MaximumMissionWaypoints              = 100
)

type MissionValidationContext struct {
	Agent          models.VehicleAgent
	HasActiveAgent bool
	Telemetry      models.TelemetrySnapshot
	Now            time.Time
}

// BuildMission normalizes operator input into the persisted mission model before
// validation decides whether the mission remains validated or is rejected.
func BuildMission(id string, droneID string, name string, createdBy string, waypoints []models.MissionWaypoint, completionAction models.MissionCompletionAction, now time.Time) models.Mission {
	return models.Mission{
		ID:               id,
		DroneID:          droneID,
		Name:             strings.TrimSpace(name),
		CreatedBy:        createdBy,
		CreatedAt:        now,
		UpdatedAt:        now,
		Waypoints:        waypoints,
		CompletionAction: NormalizeMissionCompletionAction(completionAction),
		ValidationStatus: models.MissionValidationStatusValidated,
	}
}

func NormalizeMissionCompletionAction(action models.MissionCompletionAction) models.MissionCompletionAction {
	if strings.TrimSpace(string(action)) == "" {
		return models.MissionCompletionActionReturnToLaunch
	}

	return action
}

func ValidMissionCompletionAction(action models.MissionCompletionAction) bool {
	switch action {
	case models.MissionCompletionActionHold,
		models.MissionCompletionActionReturnToLaunch,
		models.MissionCompletionActionLand:
		return true
	default:
		return false
	}
}

// ValidateMission is deterministic for the supplied validation context. Database
// lookups happen before this function so the same inputs always produce the same errors.
func ValidateMission(mission models.Mission, validation MissionValidationContext) []models.MissionValidationError {
	var validationErrors []models.MissionValidationError

	if !validation.HasActiveAgent {
		validationErrors = append(validationErrors, MissionValidationError("agent", "drone must have an active registered agent"))
	} else if models.VehicleAgentStatusFromHeartbeat(validation.Agent.LastHeartbeatAt, validation.Now) != models.VehicleAgentStatusOnline {
		validationErrors = append(validationErrors, MissionValidationError("agent", "agent must be online"))
	}

	telemetry := validation.Telemetry
	if models.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, validation.Now) != models.TelemetryStateFresh {
		validationErrors = append(validationErrors, MissionValidationError("telemetry", "telemetry must be fresh"))
	}
	if !telemetry.HomePositionSet {
		validationErrors = append(validationErrors, MissionValidationError("homePositionSet", "home position must be set"))
	}
	if !GPSFixUsableForMission(telemetry.GPSFix) {
		validationErrors = append(validationErrors, MissionValidationError("gpsFix", "GPS fix must be usable"))
	}
	if telemetry.BatteryPercent < MinimumMissionBatteryPercent {
		validationErrors = append(validationErrors, MissionValidationError("batteryPercent", fmt.Sprintf("battery must be at least %.0f%%", MinimumMissionBatteryPercent)))
	}
	if strings.TrimSpace(mission.Name) == "" {
		validationErrors = append(validationErrors, MissionValidationError("name", "mission name is required"))
	}
	if len(mission.Waypoints) == 0 {
		validationErrors = append(validationErrors, MissionValidationError("waypoints", "mission must include at least one waypoint"))
	}
	if len(mission.Waypoints) > MaximumMissionWaypoints {
		validationErrors = append(validationErrors, MissionValidationError("waypoints", fmt.Sprintf("mission cannot include more than %d waypoints", MaximumMissionWaypoints)))
	}
	if !ValidMissionCompletionAction(mission.CompletionAction) {
		validationErrors = append(validationErrors, MissionValidationError("completionAction", "completion action must be hold, return_to_launch, or land"))
	}
	for _, waypoint := range mission.Waypoints {
		fieldPrefix := fmt.Sprintf("waypoints[%d]", waypoint.Sequence-1)
		if !ValidLatitude(waypoint.Latitude) {
			validationErrors = append(validationErrors, MissionValidationError(fieldPrefix+".latitude", "latitude must be between -90 and 90"))
		}
		if !ValidLongitude(waypoint.Longitude) {
			validationErrors = append(validationErrors, MissionValidationError(fieldPrefix+".longitude", "longitude must be between -180 and 180"))
		}
		if waypoint.RelativeAltitudeM < MinimumMissionAltitudeM || waypoint.RelativeAltitudeM > MaximumMissionAltitudeM {
			validationErrors = append(validationErrors, MissionValidationError(fieldPrefix+".relativeAltitudeM", fmt.Sprintf("relative altitude must be between %.0f and %.0f meters", MinimumMissionAltitudeM, MaximumMissionAltitudeM)))
		}
		if waypoint.SpeedMPS != nil && *waypoint.SpeedMPS <= 0 {
			validationErrors = append(validationErrors, MissionValidationError(fieldPrefix+".speedMPS", "speed must be greater than 0 when provided"))
		}
		if waypoint.LoiterTimeS != nil && *waypoint.LoiterTimeS < 0 {
			validationErrors = append(validationErrors, MissionValidationError(fieldPrefix+".loiterTimeS", "loiter time cannot be negative when provided"))
		}
	}
	return validationErrors
}

func MissionValidationError(field string, message string) models.MissionValidationError {
	return models.MissionValidationError{
		Field:   field,
		Message: message,
	}
}

// MissionStartPreconditionFailure checks the live conditions required at start
// time, separate from mission-definition validation performed at creation time.
func MissionStartPreconditionFailure(agent models.VehicleAgent, hasActiveAgent bool, telemetry models.TelemetrySnapshot, now time.Time) (string, bool) {
	if !hasActiveAgent {
		return "drone has no active registered agent", true
	}
	if models.VehicleAgentStatusFromHeartbeat(agent.LastHeartbeatAt, now) != models.VehicleAgentStatusOnline {
		return "agent must be online before mission start", true
	}
	if models.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now) != models.TelemetryStateFresh {
		return "fresh telemetry is required before mission start", true
	}
	return "", false
}

func GPSFixUsableForMission(fix string) bool {
	normalized := strings.ToLower(strings.TrimSpace(fix))
	return strings.Contains(normalized, "3d") ||
		strings.Contains(normalized, "rtk") ||
		strings.Contains(normalized, "dgps")
}

func ValidLatitude(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= -90 && value <= 90
}

func ValidLongitude(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= -180 && value <= 180
}

func MissionExecutionEventType(state models.MissionExecutionState, currentMissionItem int, totalMissionItems int) string {
	if state == models.MissionExecutionStateActive &&
		(currentMissionItem > 0 || totalMissionItems > 0) {
		return "progress"
	}

	return string(state)
}

func MissionExecutionSnapshotRank(state models.MissionExecutionState) int {
	switch state {
	case models.MissionExecutionStateStartRequested,
		models.MissionExecutionStateActive,
		models.MissionExecutionStateHold,
		models.MissionExecutionStatePausedOrHold,
		models.MissionExecutionStateRTLRequested:
		return 2
	default:
		return 1
	}
}

func IsOperationalMissionExecutionState(state models.MissionExecutionState) bool {
	return missionExecutionStateIn(state, OperationalMissionExecutionStates())
}

func OperationalMissionExecutionStates() []models.MissionExecutionState {
	return []models.MissionExecutionState{
		models.MissionExecutionStateUploadRequested,
		models.MissionExecutionStateUploading,
		models.MissionExecutionStateStartRequested,
		models.MissionExecutionStateActive,
		models.MissionExecutionStateHold,
		models.MissionExecutionStatePausedOrHold,
		models.MissionExecutionStateRTLRequested,
	}
}

func IsAbortableMissionExecutionState(state models.MissionExecutionState) bool {
	return missionExecutionStateIn(state, AbortableMissionExecutionStates())
}

func AbortableMissionExecutionStates() []models.MissionExecutionState {
	return []models.MissionExecutionState{
		models.MissionExecutionStateStartRequested,
		models.MissionExecutionStateActive,
		models.MissionExecutionStateHold,
		models.MissionExecutionStatePausedOrHold,
	}
}

func DeliverableMissionExecutionStates() []models.MissionExecutionState {
	return []models.MissionExecutionState{
		models.MissionExecutionStateUploadRequested,
		models.MissionExecutionStateStartRequested,
		models.MissionExecutionStateRTLRequested,
	}
}

func missionExecutionStateIn(state models.MissionExecutionState, candidates []models.MissionExecutionState) bool {
	for _, candidate := range candidates {
		if state == candidate {
			return true
		}
	}
	return false
}

// MissionExecutionDeliverable allows one delivery attempt while a lease is
// active, then permits redelivery after the lease expires.
func MissionExecutionDeliverable(execution models.MissionExecution, now time.Time) bool {
	if execution.State == models.MissionExecutionStateUploadRequested ||
		execution.State == models.MissionExecutionStateStartRequested ||
		execution.State == models.MissionExecutionStateRTLRequested {
		if execution.LeaseUntil.IsZero() {
			return execution.LastSentAt.IsZero()
		}

		return !execution.LeaseUntil.After(now)
	}

	return false
}

func MarkMissionExecutionSent(execution models.MissionExecution, now time.Time) models.MissionExecution {
	execution.UpdatedAt = now
	execution.LastSentAt = now
	execution.LeaseUntil = now.Add(models.MissionExecutionDeliveryLeaseDuration)
	execution.DeliveryAttempt++
	return execution
}

func ResetMissionExecutionDelivery(execution models.MissionExecution) models.MissionExecution {
	execution.LastSentAt = time.Time{}
	execution.LeaseUntil = time.Time{}
	execution.DeliveryAttempt = 0
	return execution
}

func MarkMissionExecutionUploaded(execution models.MissionExecution, resultMessage string, now time.Time) models.MissionExecution {
	execution.State = models.MissionExecutionStateUploadedToVehicle
	execution.UpdatedAt = now
	execution.UploadedAt = now
	execution.LeaseUntil = time.Time{}
	execution.ResultMessage = resultMessage
	return execution
}

func MarkMissionStartRequested(execution models.MissionExecution, requestedBy string, now time.Time) models.MissionExecution {
	execution.State = models.MissionExecutionStateStartRequested
	execution.RequestedBy = requestedBy
	execution.StartRequestedBy = requestedBy
	execution.UpdatedAt = now
	execution.StartRequestedAt = now
	return ResetMissionExecutionDelivery(execution)
}

func MarkMissionAbortRequested(execution models.MissionExecution, requestedBy string, now time.Time) models.MissionExecution {
	execution.State = models.MissionExecutionStateRTLRequested
	execution.RequestedBy = requestedBy
	execution.UpdatedAt = now
	execution = ResetMissionExecutionDelivery(execution)
	execution.ResultMessage = "abort requested; returning to launch"
	return execution
}

// CanApplyAgentReportedMissionExecutionState captures the accepted state machine
// for status updates sent by the agent.
func CanApplyAgentReportedMissionExecutionState(current models.MissionExecutionState, next models.MissionExecutionState) bool {
	switch current {
	case models.MissionExecutionStateUploadRequested:
		return next == models.MissionExecutionStateUploading ||
			next == models.MissionExecutionStateUploadedToVehicle ||
			next == models.MissionExecutionStateUploadFailed ||
			next == models.MissionExecutionStateFailed
	case models.MissionExecutionStateUploading:
		return next == models.MissionExecutionStateUploadedToVehicle ||
			next == models.MissionExecutionStateUploadFailed ||
			next == models.MissionExecutionStateFailed
	case models.MissionExecutionStateStartRequested:
		return next == models.MissionExecutionStateActive ||
			next == models.MissionExecutionStateCompleted ||
			next == models.MissionExecutionStateHold ||
			next == models.MissionExecutionStateFailed
	case models.MissionExecutionStateActive:
		return next == models.MissionExecutionStateActive ||
			next == models.MissionExecutionStateCompleted ||
			next == models.MissionExecutionStateHold ||
			next == models.MissionExecutionStateAborted ||
			next == models.MissionExecutionStateFailed ||
			next == models.MissionExecutionStatePausedOrHold ||
			next == models.MissionExecutionStateRTLRequested
	case models.MissionExecutionStateCompleted:
		return next == models.MissionExecutionStateHold
	case models.MissionExecutionStatePausedOrHold:
		return next == models.MissionExecutionStateActive ||
			next == models.MissionExecutionStateCompleted ||
			next == models.MissionExecutionStateHold ||
			next == models.MissionExecutionStateAborted ||
			next == models.MissionExecutionStateFailed
	case models.MissionExecutionStateRTLRequested:
		return next == models.MissionExecutionStateRTLRequested ||
			next == models.MissionExecutionStateCompleted ||
			next == models.MissionExecutionStateHold ||
			next == models.MissionExecutionStateAborted ||
			next == models.MissionExecutionStateFailed
	default:
		return false
	}
}

// ApplyAgentReportedMissionExecutionState applies timestamps and progress fields
// after the caller has validated the state transition.
func ApplyAgentReportedMissionExecutionState(execution models.MissionExecution, state models.MissionExecutionState, resultMessage string, currentMissionItem int, totalMissionItems int, now time.Time) models.MissionExecution {
	execution.State = state
	execution.ResultMessage = resultMessage
	execution.UpdatedAt = now
	execution.LeaseUntil = time.Time{}
	if currentMissionItem > 0 || totalMissionItems > 0 {
		execution.CurrentMissionItem = currentMissionItem
		execution.TotalMissionItems = totalMissionItems
		execution.ProgressUpdatedAt = now
	}
	switch state {
	case models.MissionExecutionStateUploadedToVehicle:
		execution.UploadedAt = now
	case models.MissionExecutionStateActive:
		if execution.StartedAt.IsZero() {
			execution.StartedAt = now
		}
	case models.MissionExecutionStateCompleted:
		if execution.CompletedAt.IsZero() {
			execution.CompletedAt = now
		}
	case models.MissionExecutionStateHold:
		if execution.CompletedAt.IsZero() {
			execution.CompletedAt = now
		}
		if execution.HoldAt.IsZero() {
			execution.HoldAt = now
		}
	case models.MissionExecutionStateUploadFailed,
		models.MissionExecutionStateFailed,
		models.MissionExecutionStateAborted:
		execution.FailedAt = now
	}
	return execution
}

func MarkMissionExecutionAbortedByTelemetry(execution models.MissionExecution, now time.Time) models.MissionExecution {
	execution.State = models.MissionExecutionStateAborted
	execution.UpdatedAt = now
	execution.FailedAt = now
	execution.ResultMessage = "abort complete; vehicle returned and is no longer in air"
	return execution
}
