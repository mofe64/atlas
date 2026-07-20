package perception

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestByteTrackBackendUsesWorkerAndPreservesClassIsolation(t *testing.T) {
	backend := newTestByteTrackBackend(t, "normal", 2*time.Second)
	defer backend.Close()

	frame := TrackerFrame{
		ImageWidth: 1280, ImageHeight: 720,
		Detections: []Detection{
			{ClassID: 0, ClassLabel: "person", Confidence: 0.91, BoundingBox: BoundingBox{X: 0.1, Y: 0.2, Width: 0.1, Height: 0.3}},
			{ClassID: 2, ClassLabel: "car", Confidence: 0.87, BoundingBox: BoundingBox{X: 0.5, Y: 0.4, Width: 0.2, Height: 0.2}},
		},
	}
	associations, err := backend.Track(frame)
	if err != nil {
		t.Fatal(err)
	}
	want := []TrackAssociation{{DetectionIndex: 0, TrackKey: "0:17"}, {DetectionIndex: 1, TrackKey: "2:17"}}
	if fmt.Sprint(associations) != fmt.Sprint(want) {
		t.Fatalf("associations = %#v, want %#v", associations, want)
	}
	if backend.CameraMotionCompensationEnabled() || backend.ReIDEnabled() {
		t.Fatal("official ByteTrack backend must report CMC and ReID disabled")
	}
	if err := backend.Reset(); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
}

func TestByteTrackCMCBackendUsesWorkerAndReportsFeatures(t *testing.T) {
	backend := newTestByteTrackBackendWithCMC(t, "require_cmc", 2*time.Second)
	defer backend.Close()

	associations, err := backend.Track(TrackerFrame{
		ImageWidth: 640, ImageHeight: 480,
		CameraMotion: &CameraMotionEstimate{
			Method: "SPARSE_OPTICAL_FLOW", Confidence: 0.9,
			Homography: []float64{1, 0, 0.25, 0, 1, 0, 0, 0, 1},
		},
		Detections: []Detection{{
			ClassID: 0, ClassLabel: "person", Confidence: 0.9,
			BoundingBox: BoundingBox{X: 0.35, Y: 0.2, Width: 0.1, Height: 0.25},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(associations) != 1 || associations[0].TrackKey != "0:17" {
		t.Fatalf("CMC associations = %#v", associations)
	}
	if backend.Algorithm() != TrackerAlgorithmByteTrackCMC || !backend.CameraMotionCompensationEnabled() || backend.CameraMotionMinimumConfidence() != 0.25 || backend.ReIDEnabled() {
		t.Fatalf("CMC backend features are incorrect: algorithm=%s enabled=%v minimum=%v reid=%v",
			backend.Algorithm(), backend.CameraMotionCompensationEnabled(), backend.CameraMotionMinimumConfidence(), backend.ReIDEnabled())
	}
}

func TestByteTrackCMCFallsBackToIdentityBelowConfidenceThreshold(t *testing.T) {
	motion := &CameraMotionEstimate{Confidence: 0.24, Homography: []float64{1, 0, 0.25, 0, 1, 0, 0, 0, 1}}
	if got := byteTrackCameraMotionField(motion, 0.25); got != "none" {
		t.Fatalf("low-confidence motion = %q", got)
	}
}

func TestByteTrackCMCStageReportsCameraMotionHealth(t *testing.T) {
	backend := newTestByteTrackBackendWithCMC(t, "normal", 2*time.Second)
	defer backend.Close()
	stage := NewTrackingStage(backend, 0)
	frame := trackerTestFrame("frame-1", time.Second, "")
	stage.Process(frame)
	health := stage.EnrichHealth(Health{}).Tracking
	if health.CameraMotionState != "DEGRADED" || health.ReIDEnabled {
		t.Fatalf("tracking feature health without CMC = %#v", health)
	}
	frame.FrameID = "frame-2"
	frame.SourcePTSNS = int64(2 * time.Second)
	frame.ObservedAt = time.Unix(2, 0).UTC()
	frame.CameraMotion = &CameraMotionEstimate{
		Method: "SPARSE_OPTICAL_FLOW", Confidence: 0.9,
		Homography: []float64{1, 0, 0.01, 0, 1, 0, 0, 0, 1},
	}
	stage.Process(frame)
	health = stage.EnrichHealth(Health{}).Tracking
	if health.Algorithm != TrackerAlgorithmByteTrackCMC || health.CameraMotionState != "ACTIVE" || health.CameraMotionMethod != "SPARSE_OPTICAL_FLOW" || health.CameraMotionConfidence != 0.9 || health.ReIDEnabled {
		t.Fatalf("tracking feature health with CMC = %#v", health)
	}
}

func TestByteTrackBackendRejectsWrongClassFromWorker(t *testing.T) {
	backend := newTestByteTrackBackend(t, "wrong_class", 2*time.Second)
	defer backend.Close()
	_, err := backend.Track(TrackerFrame{
		ImageWidth: 640, ImageHeight: 480,
		Detections: []Detection{{ClassID: 0, ClassLabel: "person", Confidence: 0.9, BoundingBox: BoundingBox{X: 0.1, Y: 0.1, Width: 0.2, Height: 0.3}}},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid association") {
		t.Fatalf("Track() error = %v, want invalid association", err)
	}
}

func TestByteTrackBackendBoundsWorkerLatency(t *testing.T) {
	backend := newTestByteTrackBackend(t, "timeout", 20*time.Millisecond)
	defer backend.Close()
	started := time.Now()
	_, err := backend.Track(TrackerFrame{ImageWidth: 640, ImageHeight: 480})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Track() error = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded request took %s", elapsed)
	}
}

func TestFoundationVisionByteTrackWorkerIntegration(t *testing.T) {
	workerPath := os.Getenv("ATLAS_BYTETRACK_WORKER")
	if workerPath == "" {
		t.Skip("ATLAS_BYTETRACK_WORKER is not set")
	}
	config := DefaultByteTrackConfig()
	config.WorkerPath = workerPath
	config.RequestTimeout = 2 * time.Second
	backend, err := NewByteTrackBackend(config)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	frame := TrackerFrame{
		ImageWidth: 640, ImageHeight: 480,
		Detections: []Detection{{
			ClassID: 0, ClassLabel: "person", Confidence: 0.9,
			BoundingBox: BoundingBox{X: 0.15, Y: 0.2, Width: 0.1, Height: 0.25},
		}},
	}
	first, err := backend.Track(frame)
	if err != nil {
		t.Fatal(err)
	}
	frame.Detections[0].BoundingBox.X += 0.005
	second, err := backend.Track(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].TrackKey != second[0].TrackKey {
		t.Fatalf("official worker did not preserve association: first=%v second=%v", first, second)
	}
	if err := backend.Reset(); err != nil {
		t.Fatal(err)
	}
	afterReset, err := backend.Track(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterReset) != 1 {
		t.Fatalf("official worker did not restart after reset: %v", afterReset)
	}
}

func TestFoundationVisionByteTrackCMCWorkerIntegration(t *testing.T) {
	workerPath := os.Getenv("ATLAS_BYTETRACK_WORKER")
	if workerPath == "" {
		t.Skip("ATLAS_BYTETRACK_WORKER is not set")
	}
	config := DefaultByteTrackCMCConfig()
	config.WorkerPath = workerPath
	config.RequestTimeout = 2 * time.Second
	backend, err := NewByteTrackBackend(config)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	frame := TrackerFrame{
		ImageWidth: 640, ImageHeight: 480,
		Detections: []Detection{{
			ClassID: 0, ClassLabel: "person", Confidence: 0.9,
			BoundingBox: BoundingBox{X: 0.10, Y: 0.20, Width: 0.10, Height: 0.25},
		}},
	}
	first, err := backend.Track(frame)
	if err != nil {
		t.Fatal(err)
	}
	frame.Detections[0].BoundingBox.X = 0.35
	frame.CameraMotion = &CameraMotionEstimate{
		Method: "SPARSE_OPTICAL_FLOW", Confidence: 0.9,
		Homography: []float64{1, 0, 0.25, 0, 1, 0, 0, 0, 1},
	}
	second, err := backend.Track(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].TrackKey != second[0].TrackKey {
		t.Fatalf("CMC worker did not preserve translated association: first=%v second=%v", first, second)
	}
}

func TestByteTrackWorkerHelper(t *testing.T) {
	if os.Getenv("ATLAS_TEST_BYTETRACK_HELPER") != "1" {
		return
	}
	mode := os.Getenv("ATLAS_TEST_BYTETRACK_MODE")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 3 {
			os.Exit(2)
		}
		requestID := fields[2]
		switch fields[1] {
		case "reset":
			fmt.Printf("v1\treset_ok\t%s\n", requestID)
		case "track", "track_cmc":
			if mode == "timeout" {
				time.Sleep(2 * time.Second)
			}
			detectionStart := 6
			if fields[1] == "track_cmc" {
				detectionStart = 7
			}
			if mode == "require_cmc" && (fields[1] != "track_cmc" || len(fields) < 7 || fields[6] != "1,0,0.25,0,1,0,0,0,1") {
				os.Exit(3)
			}
			if mode == "wrong_class" {
				fmt.Printf("v1\tresult\t%s\t1\t0,9,17\n", requestID)
				continue
			}
			if len(fields) == detectionStart {
				fmt.Printf("v1\tresult\t%s\t0\n", requestID)
				continue
			}
			responses := make([]string, 0, len(fields)-detectionStart)
			for _, detection := range fields[detectionStart:] {
				parts := strings.Split(detection, ",")
				responses = append(responses, parts[0]+","+parts[1]+",17")
			}
			fmt.Printf("v1\tresult\t%s\t%d\t%s\n", requestID, len(responses), strings.Join(responses, "\t"))
		default:
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func newTestByteTrackBackend(t *testing.T, mode string, timeout time.Duration) *ByteTrackBackend {
	return newTestByteTrackBackendConfigured(t, mode, timeout, false)
}

func newTestByteTrackBackendWithCMC(t *testing.T, mode string, timeout time.Duration) *ByteTrackBackend {
	return newTestByteTrackBackendConfigured(t, mode, timeout, true)
}

func newTestByteTrackBackendConfigured(t *testing.T, mode string, timeout time.Duration, cameraMotionEnabled bool) *ByteTrackBackend {
	t.Helper()
	wrapperPath := filepath.Join(t.TempDir(), "fake-bytetrack-worker")
	executable := strings.ReplaceAll(os.Args[0], "'", "'\\''")
	wrapper := "#!/bin/sh\nATLAS_TEST_BYTETRACK_HELPER=1 ATLAS_TEST_BYTETRACK_MODE='" + mode + "' exec '" + executable + "' -test.run=TestByteTrackWorkerHelper --\n"
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	config := DefaultByteTrackConfig()
	config.WorkerPath = wrapperPath
	config.RequestTimeout = timeout
	config.CameraMotionEnabled = cameraMotionEnabled
	backend, err := NewByteTrackBackend(config)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}
