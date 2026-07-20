package perception

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFrameValidateAcceptsNeutralDetection(t *testing.T) {
	frame := Frame{
		SourceID: "a8-main", StreamEpoch: "epoch-1", FrameID: "frame-1",
		ObservedAt: time.Now().UTC(), ImageWidth: 1920, ImageHeight: 1080,
		Model:              ModelIdentity{Name: "atlas-objects", Version: "1"},
		InferenceLatencyMS: 12.5,
		Detections: []Detection{{
			TrackID: "track-1", ClassID: 0, ClassLabel: "person", Confidence: 0.93,
			BoundingBox:   BoundingBox{X: 0.1, Y: 0.2, Width: 0.3, Height: 0.4},
			AttributesRaw: json.RawMessage(`{"colour":"blue"}`),
		}},
	}
	if err := frame.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestFrameValidateRejectsBoxOutsideFrame(t *testing.T) {
	frame := Frame{
		SourceID: "a8-main", StreamEpoch: "epoch-1", FrameID: "frame-1",
		ObservedAt: time.Now().UTC(), ImageWidth: 640, ImageHeight: 640,
		Model: ModelIdentity{Name: "atlas-objects", Version: "1"},
		Detections: []Detection{{
			ClassLabel: "vehicle", Confidence: 0.8,
			BoundingBox: BoundingBox{X: 0.8, Y: 0.1, Width: 0.3, Height: 0.2},
		}},
	}
	if err := frame.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want out-of-frame validation error")
	}
}

func TestHealthValidateDistinguishesInactiveFromFailedRuntime(t *testing.T) {
	health := Health{SourceID: "a8-main", Provider: "hailo", ActivationState: "INACTIVE", ObservedAt: time.Now().UTC()}
	if err := health.Validate(); err != nil {
		t.Fatalf("intentionally inactive health must remain valid: %v", err)
	}
	health.ActivationState = "STOPPED_MAYBE"
	if err := health.Validate(); err == nil {
		t.Fatal("invalid activation state was accepted")
	}
}

func TestFrameValidateAcceptsCameraMotionHomography(t *testing.T) {
	frame := Frame{
		SourceID: "a8-main", StreamEpoch: "epoch-1", FrameID: "frame-1",
		ObservedAt: time.Now().UTC(), ImageWidth: 640, ImageHeight: 640,
		Model: ModelIdentity{Name: "atlas-objects", Version: "1"},
		CameraMotion: &CameraMotionEstimate{
			Method: "ECC", Confidence: 0.8,
			Homography: []float64{1, 0, 0.01, 0, 1, -0.02, 0, 0, 1},
		},
	}
	if err := frame.Validate(); err != nil {
		t.Fatalf("valid camera motion was rejected: %v", err)
	}
	frame.CameraMotion.Homography = frame.CameraMotion.Homography[:8]
	if err := frame.Validate(); err == nil {
		t.Fatal("short camera motion homography was accepted")
	}
}

func TestTrackingHealthRequiresSessionForActiveTracker(t *testing.T) {
	health := Health{
		SourceID: "a8-main", Provider: "hailo", ObservedAt: time.Now().UTC(),
		Tracking: &TrackingHealth{Algorithm: TrackerAlgorithmByteTrackCMC, State: "ACTIVE", CameraMotionState: "DISABLED"},
	}
	if err := health.Validate(); err == nil {
		t.Fatal("active tracking health without a session was accepted")
	}
	health.Tracking.SessionID = "session-1"
	if err := health.Validate(); err != nil {
		t.Fatalf("valid tracking health was rejected: %v", err)
	}
}

func TestPublishLatestDropsStaleValue(t *testing.T) {
	values := make(chan int, 1)
	publishLatest(values, 1)
	publishLatest(values, 2)
	if got := <-values; got != 2 {
		t.Fatalf("latest value = %d, want 2", got)
	}
}
