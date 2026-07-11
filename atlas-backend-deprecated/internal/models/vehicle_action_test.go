package models

import "testing"

func TestVehicleActionTypesUseStableAPIValues(t *testing.T) {
	tests := map[VehicleActionType]string{
		VehicleActionTypeArm:            "arm",
		VehicleActionTypeTakeoff:        "takeoff",
		VehicleActionTypeReturnToLaunch: "return_to_launch",
		VehicleActionTypeLand:           "land",
	}

	for actionType, want := range tests {
		if string(actionType) != want {
			t.Fatalf("expected %q, got %q", want, actionType)
		}
	}
}
