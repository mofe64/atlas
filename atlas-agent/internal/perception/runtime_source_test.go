package perception

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testRuntimeFrameTiming() *FrameTiming {
	return &FrameTiming{
		SourcePTSPresent: true, PipelineIngressMonotonicNS: 1_000_000_000,
		PipelineIngressUnixNS: 1_700_000_000_000_000_000,
	}
}

type recordingTimingObserver struct {
	observations chan FrameTiming
}

func (observer recordingTimingObserver) ObserveFrameTiming(_ string, _ string, _ int64, sourcePTSPresent bool, pipelineIngressMonotonicNS, pipelineIngressUnixNS, sourceCaptureUnixNS int64) {
	observer.observations <- FrameTiming{
		SourcePTSPresent: sourcePTSPresent, PipelineIngressMonotonicNS: pipelineIngressMonotonicNS,
		PipelineIngressUnixNS: pipelineIngressUnixNS, SourceCaptureUnixNS: sourceCaptureUnixNS,
	}
}

func TestRuntimeSourceTranslatesVersionedMessagesAndCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Darwin limits Unix-domain socket paths to roughly 104 bytes, while
	// testing.T.TempDir can exceed that before the file name is appended.
	tempDirectory, err := os.MkdirTemp("/tmp", "atlas-perception-")
	if err != nil {
		t.Fatalf("create short runtime directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDirectory) })
	socketPath := filepath.Join(tempDirectory, "runtime.sock")
	timingObserver := recordingTimingObserver{observations: make(chan FrameTiming, 1)}
	outputs, err := StartRuntimeSourceWithTrackerAndTiming(
		ctx,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		socketPath,
		NewDisabledTrackingStage(),
		timingObserver,
	)
	if err != nil {
		t.Fatalf("StartRuntimeSource() error = %v", err)
	}
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		cancel()
		t.Fatalf("dial runtime socket: %v", err)
	}
	now := time.Now().UTC()
	encoder := json.NewEncoder(connection)
	if err := encoder.Encode(runtimeEnvelope{
		ProtocolVersion: RuntimeProtocolVersion,
		Type:            "frame",
		Frame: &Frame{
			SourceID: "a8-main", StreamEpoch: "epoch-1", FrameID: "frame-1",
			ObservedAt: now, SourcePTSNS: 1, Timing: testRuntimeFrameTiming(), ImageWidth: 1920, ImageHeight: 1080,
			Model: ModelIdentity{Name: "atlas-objects", Version: "1"},
			Detections: []Detection{{
				ClassID: 0, ClassLabel: "person", Confidence: 0.9,
				BoundingBox: BoundingBox{X: 0.1, Y: 0.2, Width: 0.3, Height: 0.4},
			}},
		},
	}); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	if err := encoder.Encode(runtimeEnvelope{
		ProtocolVersion: RuntimeProtocolVersion,
		Type:            "health",
		Health: &Health{
			SourceID: "a8-main", Provider: "deepstream", Accelerator: "jetson-orin",
			InputConnected: true, InferenceReady: true, OutputPublishing: true,
			InputFPS: 30, InferenceFPS: 20, ObservedAt: now,
		},
	}); err != nil {
		t.Fatalf("write health: %v", err)
	}

	select {
	case frame := <-outputs.Frames:
		if frame.FrameID != "frame-1" || len(frame.Detections) != 0 {
			t.Fatalf("frame = %#v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime frame was not published")
	}
	select {
	case timing := <-timingObserver.observations:
		if !timing.SourcePTSPresent || timing.PipelineIngressMonotonicNS != 1_000_000_000 {
			t.Fatalf("timing observation = %#v", timing)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime frame timing was not observed")
	}
	select {
	case health := <-outputs.Health:
		if health.Provider != "deepstream" || health.Accelerator != "jetson-orin" {
			t.Fatalf("health = %#v", health)
		}
		if health.Tracking == nil || health.Tracking.Algorithm != TrackerAlgorithmDisabled || health.Tracking.State != "DISABLED" {
			t.Fatalf("tracking health = %#v", health.Tracking)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime health was not published")
	}

	connection.Close()
	cancel()
	for range 50 {
		if _, err := os.Lstat(socketPath); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runtime socket %q was not removed", socketPath)
}

func TestRuntimeActivationClaimsAcknowledgeFreshFramesAndReferenceCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tempDirectory, err := os.MkdirTemp("/tmp", "atlas-perception-control-")
	if err != nil {
		t.Fatalf("create short runtime directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDirectory) })
	trackerBackend := &fakeTrackerBackend{algorithm: TrackerAlgorithmByteTrack}
	tracker := NewTrackingStage(trackerBackend, time.Second)
	outputs, err := StartRuntimeSourceWithTracker(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(tempDirectory, "runtime.sock"), tracker)
	if err != nil {
		t.Fatalf("start runtime source: %v", err)
	}
	connection, err := net.Dial("unix", filepath.Join(tempDirectory, "runtime.sock"))
	if err != nil {
		t.Fatalf("dial runtime socket: %v", err)
	}
	defer connection.Close()

	commands := make(chan runtimeActivationRequest, 2)
	var writeMu sync.Mutex
	encode := func(value any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = json.NewEncoder(connection).Encode(value)
	}
	go func() {
		decoder := json.NewDecoder(connection)
		for {
			var request runtimeActivationRequest
			if err := decoder.Decode(&request); err != nil {
				return
			}
			commands <- request
			encode(runtimeEnvelope{
				ProtocolVersion:  AdapterProtocolVersion,
				Type:             "activation_result",
				ActivationResult: &runtimeActivationResult{RequestID: request.RequestID, State: request.DesiredState, SourceID: "a8-main", ObservedAt: time.Now().UTC()},
			})
			if request.DesiredState == "ACTIVE" {
				encode(runtimeEnvelope{
					ProtocolVersion: AdapterProtocolVersion,
					Type:            "frame",
					Frame:           &Frame{SourceID: "a8-main", StreamEpoch: "epoch-1", FrameID: "frame-1", ObservedAt: time.Now().UTC(), SourcePTSNS: 1, Timing: testRuntimeFrameTiming(), ImageWidth: 640, ImageHeight: 640, Model: ModelIdentity{Name: "objects", Version: "1"}},
				})
				go func() {
					time.Sleep(50 * time.Millisecond)
					encode(runtimeEnvelope{
						ProtocolVersion: AdapterProtocolVersion,
						Type:            "frame",
						Frame:           &Frame{SourceID: "a8-main", StreamEpoch: "epoch-1", FrameID: "frame-2", ObservedAt: time.Now().UTC(), SourcePTSNS: 2, Timing: testRuntimeFrameTiming(), ImageWidth: 640, ImageHeight: 640, Model: ModelIdentity{Name: "objects", Version: "1"}},
					})
				}()
			}
		}
	}()

	activationContext, activationCancel := context.WithTimeout(ctx, time.Second)
	defer activationCancel()
	evidence, err := outputs.Control.Acquire(activationContext, Claim{ID: "live_view:view-1", Owner: "live_view", LeaseDuration: 10 * time.Second})
	if err != nil {
		t.Fatalf("activate perception: %v", err)
	}
	if evidence.State != "ACTIVE" || evidence.StreamEpoch != "epoch-1" || evidence.LastFrameID != "frame-1" {
		t.Fatalf("activation evidence = %#v", evidence)
	}
	if request := <-commands; request.DesiredState != "ACTIVE" || len(request.ClaimIDs) != 1 {
		t.Fatalf("activation request = %#v", request)
	}
	if trackerBackend.resetCount != 1 {
		t.Fatalf("tracker resets after first activation = %d", trackerBackend.resetCount)
	}

	if _, err := outputs.Control.Acquire(activationContext, Claim{ID: "mission:run-1", Owner: "mission"}); err != nil {
		t.Fatalf("add mission claim: %v", err)
	}
	if trackerBackend.resetCount != 1 {
		t.Fatalf("tracker reset for shared claim = %d, want no additional reset", trackerBackend.resetCount)
	}
	if evidence, err := outputs.Control.Release(activationContext, "live_view:view-1"); err != nil || !evidence.RuntimeStillActive {
		t.Fatalf("release one of two claims: evidence=%#v error=%v", evidence, err)
	}
	if _, err := outputs.Control.Release(activationContext, "mission:run-1"); err != nil {
		t.Fatalf("release final claim: %v", err)
	}
	if health := tracker.EnrichHealth(Health{}).Tracking; trackerBackend.resetCount != 2 || health.LastResetReason != TrackingResetDeactivated || health.State != "READY" {
		t.Fatalf("tracker after deactivation = %#v backend resets=%d", health, trackerBackend.resetCount)
	}
	if request := <-commands; request.DesiredState != "INACTIVE" || len(request.ClaimIDs) != 0 {
		t.Fatalf("deactivation request = %#v", request)
	}
}

func TestRuntimeControllerFiltersToTheUnionOfActiveClaimProfiles(t *testing.T) {
	controller := newRuntimeController(context.Background())
	controller.claims["mission:run-1"] = activeClaim{claim: Claim{ID: "mission:run-1", Owner: "mission", DetectionClasses: []string{"person", "vehicle"}}}
	frame := controller.filterFrame(Frame{Detections: []Detection{
		{ClassLabel: "person"},
		{ClassLabel: "animal"},
		{ClassLabel: "vehicle"},
	}})
	if len(frame.Detections) != 2 || frame.Detections[0].ClassLabel != "person" || frame.Detections[1].ClassLabel != "vehicle" {
		t.Fatalf("filtered detections = %#v", frame.Detections)
	}
	controller.claims["live_view:view-1"] = activeClaim{claim: Claim{ID: "live_view:view-1", Owner: "live_view"}}
	if got := controller.filterFrame(Frame{Detections: []Detection{{ClassLabel: "animal"}}}); len(got.Detections) != 1 {
		t.Fatalf("unfiltered live-view detections = %#v", got.Detections)
	}
}
