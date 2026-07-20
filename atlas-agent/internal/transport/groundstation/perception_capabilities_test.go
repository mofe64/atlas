package groundstation

import (
	"slices"
	"testing"

	"github.com/sunnyside/atlas/atlas-agent/internal/config"
)

func TestFoundationVisionByteTrackCapabilities(t *testing.T) {
	capabilities := perceptionCapabilities(config.Config{TrackerAlgorithm: "byte_track"})
	for _, expected := range []string{"tracker:byte_track:foundationvision:v1", "camera_motion:none", "reid:disabled", "track_lifecycle:v1", "track_counting:v1"} {
		if !slices.Contains(capabilities, expected) {
			t.Fatalf("capabilities %v do not contain %q", capabilities, expected)
		}
	}
	if slices.Contains(capabilities, "camera_motion:sparse_optical_flow:v1") {
		t.Fatalf("plain ByteTrack capabilities incorrectly advertise CMC: %v", capabilities)
	}
}

func TestByteTrackCMCCapabilities(t *testing.T) {
	capabilities := perceptionCapabilities(config.Config{TrackerAlgorithm: "byte_track_cmc"})
	for _, expected := range []string{"tracker:byte_track_cmc:atlas:v1", "camera_motion:sparse_optical_flow:v1", "reid:disabled"} {
		if !slices.Contains(capabilities, expected) {
			t.Fatalf("capabilities %v do not contain %q", capabilities, expected)
		}
	}
	if slices.Contains(capabilities, "camera_motion:none") {
		t.Fatalf("ByteTrack CMC capabilities incorrectly disable camera motion: %v", capabilities)
	}
}
