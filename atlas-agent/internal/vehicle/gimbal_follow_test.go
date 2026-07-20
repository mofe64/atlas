package vehicle

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	gimbalpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/gimbal"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
	"google.golang.org/grpc"
)

type mutableTrackFollowSource struct {
	mu          sync.Mutex
	observation perception.TrackFollowObservation
	available   bool
}

func (source *mutableTrackFollowSource) TrackForFollow(trackSessionID, trackID string) (perception.TrackFollowObservation, bool) {
	source.mu.Lock()
	defer source.mu.Unlock()
	if !source.available || source.observation.TrackSessionID != trackSessionID || source.observation.TrackID != trackID {
		return perception.TrackFollowObservation{}, false
	}
	return source.observation, true
}

func (source *mutableTrackFollowSource) update(state perception.TrackLifecycleState, available bool) {
	source.mu.Lock()
	source.observation.LifecycleState = state
	source.observation.LastObservedAt = time.Now()
	source.available = available
	source.mu.Unlock()
}

func TestGimbalFollowTracksHoldsAndStopsWithoutChangingIdentity(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	recorder := &recordingGimbalServer{
		control: make(chan *gimbalpb.TakeControlRequest, 2),
		rates:   make(chan *gimbalpb.SetAngularRatesRequest, 16),
	}
	gimbalpb.RegisterGimbalServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	controller, err := NewPayloadController(listener.Addr().String(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new payload controller: %v", err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	controller.gimbalIDs = []int32{1}
	source := &mutableTrackFollowSource{available: true, observation: perception.TrackFollowObservation{
		SourceID: "a8-main", TrackSessionID: "session-1", TrackID: "atlas:session-1:1",
		LifecycleState: perception.TrackLifecycleActive, LastObservedAt: time.Now(),
		LatestConfirmedBox: perception.BoundingBox{X: 0.75, Y: 0.05, Width: 0.1, Height: 0.1},
	}}
	config := DefaultGimbalFollowConfig()
	config.UpdateInterval = 50 * time.Millisecond
	config.TrackFreshness = 500 * time.Millisecond
	if err := controller.ConfigureTrackFollowing(source, config); err != nil {
		t.Fatalf("configure follow: %v", err)
	}
	if _, err := controller.Execute(context.Background(), "payload_control_begin", `{"controlContext":{"kind":"inspection"},"controlSessionId":"follow-control","leaseDurationMs":7000,"gimbalId":1}`); err != nil {
		t.Fatalf("begin control: %v", err)
	}
	if _, err := controller.Execute(context.Background(), "gimbal_follow_start", `{"controlContext":{"kind":"inspection"},"controlSessionId":"follow-control","gimbalId":1,"sourceId":"a8-main","trackSessionId":"session-1","trackId":"atlas:session-1:1"}`); err != nil {
		t.Fatalf("start follow: %v", err)
	}
	first := receiveNonZeroGimbalRate(t, recorder.rates)
	if first.GetPitchRateDegS() <= 0 || first.GetYawRateDegS() <= 0 {
		t.Fatalf("first image-space correction = %#v, want positive pitch and yaw", first)
	}
	if first.GetPitchRateDegS() > float32(config.MaxPitchAcceleration*config.UpdateInterval.Seconds()+0.01) || first.GetYawRateDegS() > float32(config.MaxYawAcceleration*config.UpdateInterval.Seconds()+0.01) {
		t.Fatalf("first correction exceeded acceleration limit: %#v", first)
	}

	source.update(perception.TrackLifecycleTemporarilyOccluded, true)
	hold := receiveZeroGimbalRate(t, recorder.rates)
	if hold.GetPitchRateDegS() != 0 || hold.GetYawRateDegS() != 0 {
		t.Fatalf("occlusion hold = %#v, want zero rates", hold)
	}
	controller.mu.Lock()
	if controller.follow == nil {
		controller.mu.Unlock()
		t.Fatal("temporary occlusion ended exact-track follow instead of holding")
	}
	controller.mu.Unlock()

	source.update(perception.TrackLifecycleActive, false)
	receiveZeroGimbalRate(t, recorder.rates)
	controller.mu.Lock()
	active := controller.follow != nil
	controller.mu.Unlock()
	if active {
		t.Fatal("follow remained active after the exact track/source became unavailable")
	}
	if _, err := controller.Execute(context.Background(), "payload_control_renew", `{"controlContext":{"kind":"inspection"},"controlSessionId":"follow-control","leaseDurationMs":7000,"gimbalId":1}`); err == nil {
		t.Fatal("lease renewal hid an autonomous gimbal-follow stop")
	}
	source.update(perception.TrackLifecycleTentative, true)
	if _, err := controller.Execute(context.Background(), "gimbal_follow_start", `{"controlContext":{"kind":"inspection"},"controlSessionId":"follow-control","gimbalId":1,"sourceId":"a8-main","trackSessionId":"session-1","trackId":"atlas:session-1:1"}`); err == nil {
		t.Fatal("tentative track was accepted for gimbal following")
	}
}

func TestGimbalFollowProtectsPhysicalLimits(t *testing.T) {
	if got := protectPhysicalLimit(10, 29, -90, 30, 2, 60); got != 0 {
		t.Fatalf("rate at upper pitch margin = %v, want zero", got)
	}
	if got := protectPhysicalLimit(-10, -89, -90, 30, 2, 60); got != 0 {
		t.Fatalf("rate at lower pitch margin = %v, want zero", got)
	}
	config := DefaultGimbalFollowConfig()
	if err := config.Validate(); err != nil {
		t.Fatalf("default follow config: %v", err)
	}
}

func receiveGimbalRate(t *testing.T, rates <-chan *gimbalpb.SetAngularRatesRequest) *gimbalpb.SetAngularRatesRequest {
	t.Helper()
	select {
	case rate := <-rates:
		return rate
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for gimbal rate")
		return nil
	}
}

func receiveNonZeroGimbalRate(t *testing.T, rates <-chan *gimbalpb.SetAngularRatesRequest) *gimbalpb.SetAngularRatesRequest {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case rate := <-rates:
			if rate.GetPitchRateDegS() != 0 || rate.GetYawRateDegS() != 0 {
				return rate
			}
		case <-deadline:
			t.Fatal("timed out waiting for non-zero gimbal correction")
			return nil
		}
	}
}

func receiveZeroGimbalRate(t *testing.T, rates <-chan *gimbalpb.SetAngularRatesRequest) *gimbalpb.SetAngularRatesRequest {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case rate := <-rates:
			if rate.GetPitchRateDegS() == 0 && rate.GetYawRateDegS() == 0 {
				return rate
			}
		case <-deadline:
			t.Fatal("timed out waiting for zero gimbal hold rate")
			return nil
		}
	}
}
