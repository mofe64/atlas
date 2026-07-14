package groundstation

import (
	"testing"
	"time"

	pb "github.com/sunnyside/atlas/atlas-agent/internal/transport/groundstationpb"
)

func TestFrameDemandUsesIndependentRenewableSubscriptions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	demand := newFrameDemand()
	start := func(id string) *pb.PerceptionFrameSubscription {
		return &pb.PerceptionFrameSubscription{
			SubscriptionId:  id,
			Purpose:         "live_view",
			Action:          pb.PerceptionFrameSubscriptionAction_PERCEPTION_FRAME_SUBSCRIPTION_ACTION_START_OR_RENEW,
			LeaseDurationMs: 7_000,
		}
	}
	if demand.active(now) {
		t.Fatal("frame demand was active without a consumer")
	}
	if err := demand.applySubscription(start("view-1"), now); err != nil {
		t.Fatalf("start first subscription: %v", err)
	}
	if err := demand.applySubscription(start("view-2"), now); err != nil {
		t.Fatalf("start second subscription: %v", err)
	}
	if err := demand.applySubscription(&pb.PerceptionFrameSubscription{
		SubscriptionId: "view-1",
		Purpose:        "live_view",
		Action:         pb.PerceptionFrameSubscriptionAction_PERCEPTION_FRAME_SUBSCRIPTION_ACTION_STOP,
	}, now); err != nil {
		t.Fatalf("stop first subscription: %v", err)
	}
	if !demand.active(now.Add(6 * time.Second)) {
		t.Fatal("stopping one view disabled another active view")
	}
	if demand.active(now.Add(8 * time.Second)) {
		t.Fatal("expired subscriptions continued frame demand")
	}
}

func TestFrameDemandIncludesActiveMissionState(t *testing.T) {
	demand := newFrameDemand()
	now := time.Now()
	demand.setMissionState("run-1", "RUNNING")
	if !demand.active(now) {
		t.Fatal("running mission did not request perception frames")
	}
	demand.setMissionState("run-1", "PAUSED")
	if !demand.active(now) {
		t.Fatal("paused mission did not retain perception frames")
	}
	demand.setMissionState("run-1", "COMPLETED")
	if demand.active(now) {
		t.Fatal("terminal mission retained perception frames")
	}
}

func TestFrameDemandRejectsInvalidLease(t *testing.T) {
	demand := newFrameDemand()
	err := demand.applySubscription(&pb.PerceptionFrameSubscription{
		SubscriptionId:  "view-1",
		Purpose:         "live_view",
		Action:          pb.PerceptionFrameSubscriptionAction_PERCEPTION_FRAME_SUBSCRIPTION_ACTION_START_OR_RENEW,
		LeaseDurationMs: 1_000,
	}, time.Now())
	if err == nil {
		t.Fatal("invalid short frame lease was accepted")
	}
}
