package domain

import (
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

func TestValidateMissionUsesSafetyContextAndWaypointRules(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	mission := models.Mission{
		Name:             "",
		CompletionAction: models.MissionCompletionAction("invalid"),
		Waypoints: []models.MissionWaypoint{
			{
				Sequence:          1,
				Latitude:          91,
				Longitude:         181,
				RelativeAltitudeM: 0,
			},
		},
	}

	got := ValidateMission(mission, MissionValidationContext{
		HasActiveAgent: false,
		Telemetry: models.TelemetrySnapshot{
			ReceivedAt:      now.Add(-(models.TelemetryFreshWindow + time.Second)),
			BatteryPercent:  MinimumMissionBatteryPercent - 1,
			GPSFix:          "2d",
			HomePositionSet: false,
		},
		Now: now,
	})

	for _, field := range []string{
		"agent",
		"telemetry",
		"homePositionSet",
		"gpsFix",
		"batteryPercent",
		"name",
		"completionAction",
		"waypoints[0].latitude",
		"waypoints[0].longitude",
		"waypoints[0].relativeAltitudeM",
	} {
		if !hasValidationError(got, field) {
			t.Fatalf("expected validation error for %q in %#v", field, got)
		}
	}
}

func TestMissionStartPreconditionFailureRequiresOnlineAgentAndFreshTelemetry(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	agent := models.VehicleAgent{LastHeartbeatAt: now}
	telemetry := models.TelemetrySnapshot{ReceivedAt: now}

	if reason, failed := MissionStartPreconditionFailure(agent, true, telemetry, now); failed || reason != "" {
		t.Fatalf("expected start preconditions to pass, got failed=%v reason=%q", failed, reason)
	}
	if reason, failed := MissionStartPreconditionFailure(models.VehicleAgent{}, false, telemetry, now); !failed || reason != "drone has no active registered agent" {
		t.Fatalf("expected missing agent failure, got failed=%v reason=%q", failed, reason)
	}
	if reason, failed := MissionStartPreconditionFailure(agent, true, models.TelemetrySnapshot{}, now); !failed || reason != "fresh telemetry is required before mission start" {
		t.Fatalf("expected stale telemetry failure, got failed=%v reason=%q", failed, reason)
	}
}

func TestApplyAgentReportedMissionExecutionStateStampsProgressAndCompletion(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	execution := models.MissionExecution{
		State:      models.MissionExecutionStateActive,
		LeaseUntil: now.Add(time.Minute),
	}

	got := ApplyAgentReportedMissionExecutionState(execution, models.MissionExecutionStateCompleted, "done", 3, 4, now)

	if got.State != models.MissionExecutionStateCompleted {
		t.Fatalf("expected completed, got %q", got.State)
	}
	if !got.LeaseUntil.IsZero() {
		t.Fatalf("expected lease to be cleared")
	}
	if got.CurrentMissionItem != 3 || got.TotalMissionItems != 4 || got.ProgressUpdatedAt != now {
		t.Fatalf("expected progress fields to be stamped, got current=%d total=%d at=%v", got.CurrentMissionItem, got.TotalMissionItems, got.ProgressUpdatedAt)
	}
	if got.CompletedAt != now {
		t.Fatalf("expected completed timestamp %v, got %v", now, got.CompletedAt)
	}
}

func hasValidationError(errors []models.MissionValidationError, field string) bool {
	for _, err := range errors {
		if err.Field == field {
			return true
		}
	}
	return false
}
