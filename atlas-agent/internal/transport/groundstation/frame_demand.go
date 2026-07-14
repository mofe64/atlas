package groundstation

import (
	"errors"
	"sync"
	"time"

	pb "github.com/sunnyside/atlas/atlas-agent/internal/transport/groundstationpb"
)

const (
	minimumFrameLease = 3 * time.Second
	maximumFrameLease = 30 * time.Second
)

// frameDemand combines short Native consumer leases with mission lifecycle
// demand. Runtime frames are always drained, but only forwarded while this
// state says a consumer needs them. Health bypasses this gate.
type frameDemand struct {
	mu            sync.Mutex
	subscriptions map[string]time.Time
	missionStates map[string]string
}

func newFrameDemand() *frameDemand {
	return &frameDemand{
		subscriptions: map[string]time.Time{},
		missionStates: map[string]string{},
	}
}

func (d *frameDemand) applySubscription(request *pb.PerceptionFrameSubscription, now time.Time) error {
	if request == nil || request.GetSubscriptionId() == "" {
		return errors.New("perception frame subscription id is required")
	}
	if request.GetPurpose() != "live_view" {
		return errors.New("unsupported perception frame subscription purpose")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch request.GetAction() {
	case pb.PerceptionFrameSubscriptionAction_PERCEPTION_FRAME_SUBSCRIPTION_ACTION_START_OR_RENEW:
		lease := time.Duration(request.GetLeaseDurationMs()) * time.Millisecond
		if lease < minimumFrameLease || lease > maximumFrameLease {
			return errors.New("perception frame lease must be between 3 and 30 seconds")
		}
		d.subscriptions[request.GetSubscriptionId()] = now.Add(lease)
	case pb.PerceptionFrameSubscriptionAction_PERCEPTION_FRAME_SUBSCRIPTION_ACTION_STOP:
		delete(d.subscriptions, request.GetSubscriptionId())
	default:
		return errors.New("perception frame subscription action is required")
	}
	return nil
}

func (d *frameDemand) setMissionState(runID, state string) {
	if runID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if state == "RUNNING" || state == "PAUSED" {
		d.missionStates[runID] = state
		return
	}
	delete(d.missionStates, runID)
}

func (d *frameDemand) active(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, expiry := range d.subscriptions {
		if !now.Before(expiry) {
			delete(d.subscriptions, id)
		}
	}
	return len(d.subscriptions) > 0 || len(d.missionStates) > 0
}

func (d *frameDemand) clearSubscriptions() {
	d.mu.Lock()
	d.subscriptions = map[string]time.Time{}
	d.mu.Unlock()
}
