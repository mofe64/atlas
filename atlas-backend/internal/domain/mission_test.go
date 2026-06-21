package domain

import "testing"

func TestMissionValidationStatusUsesStableAPIValues(t *testing.T) {
	tests := map[MissionValidationStatus]string{
		MissionValidationStatusNotValidated: "not_validated",
		MissionValidationStatusValidated:    "validated",
		MissionValidationStatusRejected:     "rejected",
	}

	for status, want := range tests {
		if string(status) != want {
			t.Fatalf("expected %q, got %q", want, status)
		}
	}
}

func TestMissionCompletionActionUsesStableAPIValues(t *testing.T) {
	tests := map[MissionCompletionAction]string{
		MissionCompletionActionHold:           "hold",
		MissionCompletionActionReturnToLaunch: "return_to_launch",
		MissionCompletionActionLand:           "land",
	}

	for action, want := range tests {
		if string(action) != want {
			t.Fatalf("expected %q, got %q", want, action)
		}
	}
}

func TestMissionExecutionStateUsesStableAPIValues(t *testing.T) {
	tests := map[MissionExecutionState]string{
		MissionExecutionStateUnknown:           "unknown",
		MissionExecutionStateCreated:           "created",
		MissionExecutionStateUploadRequested:   "upload_requested",
		MissionExecutionStateUploading:         "uploading",
		MissionExecutionStateUploadFailed:      "upload_failed",
		MissionExecutionStateUploadedToVehicle: "uploaded_to_vehicle",
		MissionExecutionStateStartRequested:    "start_requested",
		MissionExecutionStateActive:            "active",
		MissionExecutionStateHold:              "hold",
		MissionExecutionStatePausedOrHold:      "paused_or_hold",
		MissionExecutionStateRTLRequested:      "rtl_requested",
		MissionExecutionStateCompleted:         "completed",
		MissionExecutionStateAborted:           "aborted",
		MissionExecutionStateFailed:            "failed",
	}

	for state, want := range tests {
		if string(state) != want {
			t.Fatalf("expected %q, got %q", want, state)
		}
	}
}
