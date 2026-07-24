package vehicle

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type indoorHoldRecorder struct {
	calls int
	err   error
}

func (h *indoorHoldRecorder) Execute(_ context.Context, _, commandType, _ string) (CommandResult, error) {
	if commandType != "hold" {
		return CommandResult{}, errors.New("unexpected command")
	}
	h.calls++
	return CommandResult{Code: "RESULT_SUCCESS"}, h.err
}

func TestIndoorExploreContractAcceptsOnlyThreeAltitudesAndStaysInHold(t *testing.T) {
	for _, altitude := range []float64{0.5, 1.0, 2.0} {
		t.Run(fmt.Sprintf("%.1f_m", altitude), func(t *testing.T) {
			hold := &indoorHoldRecorder{}
			controller, err := NewIndoorExploreController(hold)
			if err != nil {
				t.Fatalf("new controller: %v", err)
			}
			operation := IndoorExploreOperation{
				OperationID: "operation-1",
				MissionID:   "mission-1",
				DroneID:     "drone-1",
				Action:      "start",
				AltitudeM:   altitude,
			}
			controller.Apply(context.Background(), operation)
			starting := <-controller.Updates()
			holding := <-controller.Updates()
			if starting.State != "STARTING" || holding.State != "HOLDING" {
				t.Fatalf("states = %s, %s", starting.State, holding.State)
			}
			if holding.ErrorCode != "LOCAL_NAVIGATION_NOT_COMMISSIONED" || hold.calls != 1 {
				t.Fatalf("holding = %#v, hold calls = %d", holding, hold.calls)
			}
		})
	}

	hold := &indoorHoldRecorder{}
	controller, _ := NewIndoorExploreController(hold)
	controller.Apply(context.Background(), IndoorExploreOperation{
		OperationID: "operation-invalid",
		MissionID:   "mission-invalid",
		DroneID:     "drone-1",
		Action:      "start",
		AltitudeM:   1.5,
	})
	failed := <-controller.Updates()
	if failed.State != "FAILED" || failed.ErrorCode != "INVALID_INDOOR_MISSION" || hold.calls != 0 {
		t.Fatalf("invalid altitude result = %#v, hold calls = %d", failed, hold.calls)
	}
}

func TestIndoorExploreAbortHoldsWithoutClaimingReturn(t *testing.T) {
	hold := &indoorHoldRecorder{}
	controller, _ := NewIndoorExploreController(hold)
	controller.Apply(context.Background(), IndoorExploreOperation{
		OperationID: "start-1",
		MissionID:   "mission-1",
		DroneID:     "drone-1",
		Action:      "start",
		AltitudeM:   1,
	})
	<-controller.Updates()
	<-controller.Updates()

	controller.Apply(context.Background(), IndoorExploreOperation{
		OperationID: "abort-1",
		MissionID:   "mission-1",
		DroneID:     "drone-1",
		Action:      "abort_and_return",
	})
	update := <-controller.Updates()
	if update.State != "HOLDING" || update.ErrorCode != "RETURN_CONTROLLER_NOT_COMMISSIONED" {
		t.Fatalf("abort update = %#v", update)
	}
	if hold.calls != 2 {
		t.Fatalf("hold calls = %d, want 2", hold.calls)
	}
}

func TestIndoorExploreHoldFailureIsExplicit(t *testing.T) {
	hold := &indoorHoldRecorder{err: errors.New("hold unavailable")}
	controller, _ := NewIndoorExploreController(hold)
	controller.Apply(context.Background(), IndoorExploreOperation{
		OperationID: "start-1",
		MissionID:   "mission-1",
		DroneID:     "drone-1",
		Action:      "start",
		AltitudeM:   2,
	})
	<-controller.Updates()
	failed := <-controller.Updates()
	if failed.State != "FAILED" || failed.ErrorCode != "START_HOLD_FAILED" {
		t.Fatalf("failed update = %#v", failed)
	}
}

func TestGroundLinkLossReplacesPendingProgressWithOneSafeState(t *testing.T) {
	hold := &indoorHoldRecorder{}
	controller, _ := NewIndoorExploreController(hold)
	controller.Apply(context.Background(), IndoorExploreOperation{
		OperationID: "start-1",
		MissionID:   "mission-1",
		DroneID:     "drone-1",
		Action:      "start",
		AltitudeM:   0.5,
	})

	controller.GroundLinkLost()
	update := <-controller.Updates()
	if update.State != "HOLDING" || update.ErrorCode != "GROUND_LINK_LOST" {
		t.Fatalf("ground-link update = %#v", update)
	}
	select {
	case unexpected := <-controller.Updates():
		t.Fatalf("unexpected stale update after ground-link loss: %#v", unexpected)
	default:
	}
	if hold.calls != 2 {
		t.Fatalf("hold calls = %d, want Start Hold plus ground-link Hold", hold.calls)
	}
}
