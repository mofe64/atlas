package navigation

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"
)

const healthyEstimatorFlags = requiredEstimatorFlags

func TestPlaneTransitionsUnavailableReadyDegradedAndStale(t *testing.T) {
	config := DefaultConfig()
	plane, err := NewPlane(config)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_800_000_000, 0)
	if got := plane.Latest(base); got.Status != StatusUnavailable || got.Ready {
		t.Fatalf("empty state = %#v", got)
	}
	observeHealthyState(plane, base, 0)
	if got := plane.Latest(base.Add(100 * time.Millisecond)); got.Status != StatusReady || !got.Ready {
		t.Fatalf("healthy state = %#v", got)
	}
	plane.ObserveOpticalFlow(1_200_000, base.Add(200*time.Millisecond), OpticalFlow{Quality: 0, DistanceM: 1.2})
	if got := plane.Latest(base.Add(250 * time.Millisecond)); got.Status != StatusDegraded || got.Ready || got.Components["opticalFlow"].Status != StatusDegraded {
		t.Fatalf("low-quality flow state = %#v", got)
	}
	if got := plane.Latest(base.Add(2 * time.Second)); got.Status != StatusStale || got.Ready {
		t.Fatalf("stale state = %#v", got)
	}
}

func TestEstimatorResetCreatesBoundedDegradedWindow(t *testing.T) {
	config := DefaultConfig()
	config.LocalPositionStaleAfter = 10 * time.Second
	config.LocalPositionHealthStaleAfter = 10 * time.Second
	config.OdometryStaleAfter = 10 * time.Second
	config.EstimatorStaleAfter = 10 * time.Second
	config.OpticalFlowStaleAfter = 10 * time.Second
	config.RangeStaleAfter = 10 * time.Second
	plane, _ := NewPlane(config)
	base := time.Unix(1_800_000_000, 0)
	observeHealthyState(plane, base, 3)
	plane.ObserveOdometry(1_100_000, base.Add(100*time.Millisecond), Odometry{Attitude: Quaternion{W: 1}, ResetCounter: 4, Quality: 100})
	got := plane.Latest(base.Add(200 * time.Millisecond))
	if got.Status != StatusDegraded || got.LastEstimatorReset == nil || got.LastEstimatorReset.PreviousCounter != 3 || got.LastEstimatorReset.CurrentCounter != 4 {
		t.Fatalf("reset state = %#v", got)
	}
	if got := plane.Latest(base.Add(3 * time.Second)); got.Status != StatusReady {
		t.Fatalf("post-reset state = %#v", got)
	}
}

func TestOneSecondHealthCadenceDoesNotMakeFreshPositionStale(t *testing.T) {
	config := DefaultConfig()
	plane, err := NewPlane(config)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_800_000_000, 0)
	observeHealthyState(plane, base, 0)
	refreshNavigationMeasurements(plane, base.Add(time.Second), 2_000_000, 0)

	if got := plane.Latest(base.Add(1200 * time.Millisecond)); got.Status != StatusReady || !got.Ready {
		t.Fatalf("one-second health cadence produced false stale state = %#v", got)
	}

	refreshNavigationMeasurements(plane, base.Add(2500*time.Millisecond), 3_500_000, 0)
	got := plane.Latest(base.Add(2600 * time.Millisecond))
	if got.Status != StatusStale || got.Components["localPosition"].Reason != "PX4 local-position health is stale" {
		t.Fatalf("missing health heartbeat did not become stale = %#v", got)
	}
}

func TestSampleAtUsesOnlyStateAvailableAtCaptureTime(t *testing.T) {
	config := DefaultConfig()
	config.HistoryDuration = time.Minute
	plane, _ := NewPlane(config)
	base := time.Unix(1_800_000_000, 0)
	observeHealthyState(plane, base, 0)
	plane.ObserveLocalPosition(1_100_000, base.Add(100*time.Millisecond), Vector3{X: 1}, Vector3{})
	plane.ObserveLocalPosition(1_200_000, base.Add(200*time.Millisecond), Vector3{X: 2}, Vector3{})
	sample, err := plane.SampleAt(base.Add(150*time.Millisecond).UnixNano(), 200*time.Millisecond.Nanoseconds())
	if err != nil {
		t.Fatal(err)
	}
	if !sample.WithinTolerance || sample.State.LocalPosition == nil || sample.State.LocalPosition.Position.X != 1 || sample.SampleUnixNS > sample.CaptureUnixNS {
		t.Fatalf("capture-time sample = %#v", sample)
	}
	if sample.State.Odometry == nil || sample.State.Odometry.FrameID != 18 || sample.State.Odometry.ChildFrameID != 8 || sample.State.Odometry.ResetCounter != 0 || sample.State.Odometry.Time.ClockEpoch != 0 {
		t.Fatalf("capture-time epoch/frame metadata = %#v", sample.State.Odometry)
	}
}

func TestPX4ClockResetStartsNewAlignmentEpoch(t *testing.T) {
	aligner := clockAligner{}
	base := time.Unix(1_800_000_000, 0)
	first := aligner.align(8_000_000, base)
	second := aligner.align(100_000, base.Add(time.Second))
	if first.ClockEpoch != 0 || second.ClockEpoch != 1 {
		t.Fatalf("clock epochs = %d, %d", first.ClockEpoch, second.ClockEpoch)
	}
	if second.AlignedUnixNS != base.Add(time.Second).UnixNano() {
		t.Fatalf("realigned timestamp = %d", second.AlignedUnixNS)
	}
}

func TestSensorLossAndEstimatorInnovationCannotRemainReady(t *testing.T) {
	plane, _ := NewPlane(DefaultConfig())
	base := time.Unix(1_800_000_000, 0)
	observeHealthyState(plane, base, 0)
	plane.ObserveRange(1_100_000, base.Add(100*time.Millisecond), Range{MinimumM: 0.08, MaximumM: 30, CurrentM: -1})
	if got := plane.Latest(base.Add(150 * time.Millisecond)); got.Status != StatusDegraded || got.Components["range"].Status != StatusDegraded {
		t.Fatalf("invalid range state = %#v", got)
	}
	plane.ObserveEstimator(1_200_000, base.Add(200*time.Millisecond), EstimatorStatus{Flags: healthyEstimatorFlags, VelocityTestRatio: 1.1})
	if got := plane.Latest(base.Add(250 * time.Millisecond)); got.Status != StatusDegraded || got.Components["estimator"].Status != StatusDegraded {
		t.Fatalf("innovation failure state = %#v", got)
	}
	plane.SetConnected(false, base.Add(300*time.Millisecond))
	if got := plane.Latest(base.Add(350 * time.Millisecond)); got.Status != StatusUnavailable || got.Ready {
		t.Fatalf("connection-loss state = %#v", got)
	}
}

func TestSocketServesBoundedCaptureTimeSample(t *testing.T) {
	plane, _ := NewPlane(DefaultConfig())
	base := time.Now().UTC()
	observeHealthyState(plane, base, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	temporary, err := os.CreateTemp("/tmp", "atlas-navigation-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	path := temporary.Name()
	_ = temporary.Close()
	_ = os.Remove(path)
	t.Cleanup(func() { _ = os.Remove(path) })
	if err := StartSocketServer(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), path, plane); err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"protocolVersion": ProtocolVersion, "type": "sampleAt", "captureUnixNs": base.UnixNano(), "maxSkewNs": time.Second.Nanoseconds()}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Type   string  `json:"type"`
		Sample *Sample `json:"sample"`
	}
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&response); err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if response.Type != "sample" || response.Sample == nil || !response.Sample.WithinTolerance {
		t.Fatalf("socket response = %#v", response)
	}
}

func observeHealthyState(plane *Plane, base time.Time, resetCounter uint8) {
	plane.SetConnected(true, base)
	plane.SetLocalPositionValid(true, base)
	refreshNavigationMeasurements(plane, base, 1_000_000, resetCounter)
}

func refreshNavigationMeasurements(plane *Plane, observedAt time.Time, sourceUS uint64, resetCounter uint8) {
	plane.ObserveLocalPosition(sourceUS, observedAt, Vector3{}, Vector3{})
	plane.ObserveOdometry(sourceUS, observedAt, Odometry{FrameID: 18, ChildFrameID: 8, Attitude: Quaternion{W: 1}, ResetCounter: resetCounter, Quality: 100})
	plane.ObserveEstimator(sourceUS, observedAt, EstimatorStatus{Flags: healthyEstimatorFlags, VelocityTestRatio: 0.1, HorizontalPosTestRatio: 0.1, VerticalPosTestRatio: 0.1, HeightAGLTestRatio: 0.1})
	plane.ObserveOpticalFlow(sourceUS, observedAt, OpticalFlow{IntegrationTimeUS: 14_285, Quality: 100, DistanceM: 1.2})
	plane.ObserveRange(sourceUS, observedAt, Range{MinimumM: 0.08, MaximumM: 30, CurrentM: 1.2, SignalQuality: 100})
}
