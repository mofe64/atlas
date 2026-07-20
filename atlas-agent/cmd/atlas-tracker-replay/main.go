// Command atlas-tracker-replay runs a ByteTrack worker over newline-delimited
// Atlas perception frames. It is a deterministic validation harness, not a
// replacement for the live runtime path.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
)

func main() {
	inputPath := flag.String("input", "-", "input Atlas Frame NDJSON file, or - for stdin")
	outputPath := flag.String("output", "-", "output tracked Frame NDJSON file, or - for stdout")
	algorithm := flag.String("algorithm", "byte_track", "tracker algorithm: byte_track or byte_track_cmc")
	workerPath := flag.String("worker-path", "atlas-bytetrack-worker", "FoundationVision ByteTrack worker executable")
	requestTimeout := flag.Duration("request-timeout", 250*time.Millisecond, "per-frame worker deadline")
	cmcMinimumConfidence := flag.Float64("cmc-min-confidence", 0.25, "minimum camera-motion confidence for byte_track_cmc")
	maxTimestampGap := flag.Duration("max-timestamp-gap", 2*time.Second, "continuity gap that resets the tracking session")
	flag.Parse()

	if err := run(*inputPath, *outputPath, *algorithm, *workerPath, *requestTimeout, *cmcMinimumConfidence, *maxTimestampGap); err != nil {
		fmt.Fprintln(os.Stderr, "atlas-tracker-replay:", err)
		os.Exit(1)
	}
}

func run(inputPath, outputPath, algorithm, workerPath string, requestTimeout time.Duration, cmcMinimumConfidence float64, maxTimestampGap time.Duration) error {
	input, closeInput, err := openReplayInput(inputPath)
	if err != nil {
		return err
	}
	defer closeInput()
	output, closeOutput, err := openReplayOutput(outputPath)
	if err != nil {
		return err
	}
	defer closeOutput()

	trackerConfig := perception.DefaultByteTrackConfig()
	switch algorithm {
	case "byte_track":
	case "byte_track_cmc":
		trackerConfig.CameraMotionEnabled = true
	default:
		return fmt.Errorf("unsupported tracker algorithm %q", algorithm)
	}
	trackerConfig.WorkerPath = workerPath
	trackerConfig.RequestTimeout = requestTimeout
	trackerConfig.CameraMotionMinimumConfidence = cmcMinimumConfidence
	backend, err := perception.NewByteTrackBackend(trackerConfig)
	if err != nil {
		return fmt.Errorf("configure %s: %w", algorithm, err)
	}
	defer backend.Close()
	stage := perception.NewTrackingStage(backend, maxTimestampGap)
	decoder := bufio.NewScanner(input)
	decoder.Buffer(make([]byte, 64*1024), 8*1024*1024)
	encoder := json.NewEncoder(output)
	uniqueTracks := make(map[string]struct{})
	trackingStarted := time.Now()
	frames := 0
	detections := 0
	associations := 0
	for decoder.Scan() {
		frames++
		var frame perception.Frame
		if err := json.Unmarshal(decoder.Bytes(), &frame); err != nil {
			return fmt.Errorf("decode frame %d: %w", frames, err)
		}
		if err := frame.Validate(); err != nil {
			return fmt.Errorf("validate frame %d: %w", frames, err)
		}
		tracked := stage.Process(frame)
		detections += len(tracked.Detections)
		for _, detection := range tracked.Detections {
			if detection.TrackID != "" {
				associations++
				uniqueTracks[detection.TrackID] = struct{}{}
			}
		}
		if err := encoder.Encode(tracked); err != nil {
			return fmt.Errorf("encode frame %d: %w", frames, err)
		}
	}
	if err := decoder.Err(); err != nil {
		return fmt.Errorf("read replay input: %w", err)
	}
	trackingElapsed := time.Since(trackingStarted)
	replayFPS := 0.0
	if trackingElapsed > 0 {
		replayFPS = float64(frames) / trackingElapsed.Seconds()
	}
	health := stage.EnrichHealth(perception.Health{})
	state := "UNKNOWN"
	resets := uint64(0)
	if health.Tracking != nil {
		state = health.Tracking.State
		resets = health.Tracking.ResetCount
	}
	fmt.Fprintf(os.Stderr, "tracker=%s frames=%d detections=%d associations=%d unique_tracks=%d resets=%d state=%s elapsed=%s replay_fps=%.1f\n",
		algorithm, frames, detections, associations, len(uniqueTracks), resets, state, trackingElapsed.Round(time.Microsecond), replayFPS)
	return nil
}

func openReplayInput(path string) (io.Reader, func(), error) {
	if path == "-" {
		return os.Stdin, func() {}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open input: %w", err)
	}
	return file, func() { _ = file.Close() }, nil
}

func openReplayOutput(path string) (io.Writer, func(), error) {
	if path == "-" {
		return os.Stdout, func() {}, nil
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open output: %w", err)
	}
	return file, func() { _ = file.Close() }, nil
}
