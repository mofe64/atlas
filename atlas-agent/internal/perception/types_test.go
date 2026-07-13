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

func TestPublishLatestDropsStaleValue(t *testing.T) {
	values := make(chan int, 1)
	publishLatest(values, 1)
	publishLatest(values, 2)
	if got := <-values; got != 2 {
		t.Fatalf("latest value = %d, want 2", got)
	}
}
