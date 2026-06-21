package domain

import "testing"

func TestCommandTypesUseStableAPIValues(t *testing.T) {
	tests := map[CommandType]string{
		CommandTypeArm:            "arm",
		CommandTypeTakeoff:        "takeoff",
		CommandTypeReturnToLaunch: "return_to_launch",
		CommandTypeLand:           "land",
	}

	for commandType, want := range tests {
		if string(commandType) != want {
			t.Fatalf("expected %q, got %q", want, commandType)
		}
	}
}
