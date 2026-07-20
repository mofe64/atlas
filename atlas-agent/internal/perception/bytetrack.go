package perception

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const byteTrackProtocolVersion = "v1"

// ByteTrackConfig configures the official FoundationVision ByteTrack worker.
// The defaults are the defaults published by the upstream C++ deployment.
type ByteTrackConfig struct {
	WorkerPath                    string
	RequestTimeout                time.Duration
	FrameRate                     int
	TrackThreshold                float64
	HighThreshold                 float64
	MatchThreshold                float64
	TrackBufferFrames             int
	CameraMotionEnabled           bool
	CameraMotionMinimumConfidence float64
}

func DefaultByteTrackConfig() ByteTrackConfig {
	return ByteTrackConfig{
		WorkerPath:                    "atlas-bytetrack-worker",
		RequestTimeout:                250 * time.Millisecond,
		FrameRate:                     30,
		TrackThreshold:                0.50,
		HighThreshold:                 0.60,
		MatchThreshold:                0.80,
		TrackBufferFrames:             30,
		CameraMotionMinimumConfidence: 0.25,
	}
}

func DefaultByteTrackCMCConfig() ByteTrackConfig {
	config := DefaultByteTrackConfig()
	config.CameraMotionEnabled = true
	return config
}

func (config ByteTrackConfig) Validate() error {
	if strings.TrimSpace(config.WorkerPath) == "" {
		return errors.New("ByteTrack worker path is required")
	}
	if config.RequestTimeout < 10*time.Millisecond || config.RequestTimeout > 5*time.Second {
		return errors.New("ByteTrack request timeout must be between 10ms and 5s")
	}
	if config.FrameRate < 1 || config.FrameRate > 240 {
		return errors.New("ByteTrack frame rate must be between 1 and 240")
	}
	if config.TrackBufferFrames < 1 || config.TrackBufferFrames > 300 {
		return errors.New("ByteTrack track buffer must be between 1 and 300 frames")
	}
	if !finiteUnit(config.TrackThreshold) || !finiteUnit(config.HighThreshold) || config.TrackThreshold >= config.HighThreshold {
		return errors.New("ByteTrack thresholds require 0 <= track < high <= 1")
	}
	if !finiteUnit(config.MatchThreshold) {
		return errors.New("ByteTrack match threshold must be between 0 and 1")
	}
	if !finiteUnit(config.CameraMotionMinimumConfidence) {
		return errors.New("ByteTrack camera-motion minimum confidence must be between 0 and 1")
	}
	return nil
}

// ByteTrackBackend is a supervised adapter around the MIT-licensed
// FoundationVision ByteTrack core. The worker owns only algorithm-local state;
// TrackingStage remains authoritative for continuity and Atlas IDs.
type ByteTrackBackend struct {
	mu            sync.Mutex
	config        ByteTrackConfig
	resolvedPath  string
	process       *byteTrackProcess
	nextRequestID uint64
}

type byteTrackProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
}

type byteTrackScanResult struct {
	line string
	err  error
}

func NewByteTrackBackend(config ByteTrackConfig) (*ByteTrackBackend, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	resolvedPath, err := exec.LookPath(config.WorkerPath)
	if err != nil {
		return nil, fmt.Errorf("find FoundationVision ByteTrack worker %q: %w", config.WorkerPath, err)
	}
	return &ByteTrackBackend{config: config, resolvedPath: resolvedPath}, nil
}

func (backend *ByteTrackBackend) Algorithm() TrackerAlgorithm {
	if backend.config.CameraMotionEnabled {
		return TrackerAlgorithmByteTrackCMC
	}
	return TrackerAlgorithmByteTrack
}

func (backend *ByteTrackBackend) CameraMotionCompensationEnabled() bool {
	return backend.config.CameraMotionEnabled
}

func (backend *ByteTrackBackend) CameraMotionMinimumConfidence() float64 {
	if !backend.config.CameraMotionEnabled {
		return 0
	}
	return backend.config.CameraMotionMinimumConfidence
}

func (backend *ByteTrackBackend) ReIDEnabled() bool { return false }

func (backend *ByteTrackBackend) Track(frame TrackerFrame) ([]TrackAssociation, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if frame.ImageWidth == 0 || frame.ImageHeight == 0 {
		return nil, errors.New("ByteTrack image dimensions must be positive")
	}
	if len(frame.Detections) > 1000 {
		return nil, errors.New("ByteTrack accepts at most 1000 detections per frame")
	}

	requestID := backend.newRequestIDLocked()
	operation := "track"
	if backend.config.CameraMotionEnabled {
		operation = "track_cmc"
	}
	fields := []string{
		byteTrackProtocolVersion,
		operation,
		requestID,
		strconv.FormatUint(uint64(frame.ImageWidth), 10),
		strconv.FormatUint(uint64(frame.ImageHeight), 10),
		strconv.Itoa(len(frame.Detections)),
	}
	if backend.config.CameraMotionEnabled {
		fields = append(fields, byteTrackCameraMotionField(
			frame.CameraMotion, backend.config.CameraMotionMinimumConfidence,
		))
	}
	for index, detection := range frame.Detections {
		if err := detection.Validate(); err != nil {
			return nil, fmt.Errorf("ByteTrack detection %d: %w", index, err)
		}
		if detection.BoundingBox.Width <= 0 || detection.BoundingBox.Height <= 0 {
			return nil, fmt.Errorf("ByteTrack detection %d: bounding box dimensions must be positive", index)
		}
		fields = append(fields, strings.Join([]string{
			strconv.Itoa(index),
			strconv.FormatInt(int64(detection.ClassID), 10),
			formatByteTrackFloat(detection.Confidence),
			formatByteTrackFloat(detection.BoundingBox.X * float64(frame.ImageWidth)),
			formatByteTrackFloat(detection.BoundingBox.Y * float64(frame.ImageHeight)),
			formatByteTrackFloat(detection.BoundingBox.Width * float64(frame.ImageWidth)),
			formatByteTrackFloat(detection.BoundingBox.Height * float64(frame.ImageHeight)),
		}, ","))
	}

	response, err := backend.requestLocked(strings.Join(fields, "\t"), requestID)
	if err != nil {
		return nil, err
	}
	return parseByteTrackAssociations(response, requestID, frame.Detections)
}

func (backend *ByteTrackBackend) Reset() error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.process == nil {
		return nil
	}
	requestID := backend.newRequestIDLocked()
	response, err := backend.requestLocked(strings.Join([]string{byteTrackProtocolVersion, "reset", requestID}, "\t"), requestID)
	if err != nil {
		return err
	}
	fields := strings.Split(response, "\t")
	if len(fields) != 3 || fields[0] != byteTrackProtocolVersion || fields[1] != "reset_ok" || fields[2] != requestID {
		backend.stopLocked(true)
		return errors.New("ByteTrack worker returned an invalid reset response")
	}
	return nil
}

func (backend *ByteTrackBackend) Close() error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.stopLocked(false)
}

func (backend *ByteTrackBackend) newRequestIDLocked() string {
	backend.nextRequestID++
	return strconv.FormatUint(backend.nextRequestID, 10)
}

func (backend *ByteTrackBackend) requestLocked(request, requestID string) (string, error) {
	if backend.process == nil {
		if err := backend.startLocked(); err != nil {
			return "", err
		}
	}
	if _, err := io.WriteString(backend.process.stdin, request+"\n"); err != nil {
		backend.stopLocked(true)
		return "", fmt.Errorf("write ByteTrack worker request: %w", err)
	}

	resultChannel := make(chan byteTrackScanResult, 1)
	process := backend.process
	go func() {
		if process.scanner.Scan() {
			resultChannel <- byteTrackScanResult{line: process.scanner.Text()}
			return
		}
		err := process.scanner.Err()
		if err == nil {
			err = io.EOF
		}
		resultChannel <- byteTrackScanResult{err: err}
	}()

	timer := time.NewTimer(backend.config.RequestTimeout)
	defer timer.Stop()
	select {
	case result := <-resultChannel:
		if result.err != nil {
			backend.stopLocked(true)
			return "", fmt.Errorf("read ByteTrack worker response: %w", result.err)
		}
		fields := strings.Split(result.line, "\t")
		if len(fields) >= 4 && fields[0] == byteTrackProtocolVersion && fields[1] == "error" && fields[2] == requestID {
			return "", errors.New("ByteTrack worker: " + fields[3])
		}
		return result.line, nil
	case <-timer.C:
		backend.stopLocked(true)
		return "", fmt.Errorf("ByteTrack worker request %s timed out after %s", requestID, backend.config.RequestTimeout)
	}
}

func (backend *ByteTrackBackend) startLocked() error {
	arguments := []string{
		"--frame-rate", strconv.Itoa(backend.config.FrameRate),
		"--track-buffer", strconv.Itoa(backend.config.TrackBufferFrames),
		"--track-threshold", formatByteTrackFloat(backend.config.TrackThreshold),
		"--high-threshold", formatByteTrackFloat(backend.config.HighThreshold),
		"--match-threshold", formatByteTrackFloat(backend.config.MatchThreshold),
	}
	command := exec.Command(backend.resolvedPath, arguments...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("open ByteTrack worker stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("open ByteTrack worker stdout: %w", err)
	}
	process := &byteTrackProcess{command: command, stdin: stdin}
	process.scanner = bufio.NewScanner(stdout)
	process.scanner.Buffer(make([]byte, 4096), 1024*1024)
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		stdin.Close()
		return fmt.Errorf("start ByteTrack worker: %w", err)
	}
	backend.process = process
	return nil
}

func (backend *ByteTrackBackend) stopLocked(force bool) error {
	process := backend.process
	backend.process = nil
	if process == nil {
		return nil
	}
	_ = process.stdin.Close()
	if force && process.command.Process != nil {
		_ = process.command.Process.Kill()
	}
	err := process.command.Wait()
	if force || errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stop ByteTrack worker: %w", err)
	}
	return nil
}

func parseByteTrackAssociations(response, requestID string, detections []Detection) ([]TrackAssociation, error) {
	fields := strings.Split(response, "\t")
	if len(fields) < 4 || fields[0] != byteTrackProtocolVersion || fields[1] != "result" || fields[2] != requestID {
		return nil, errors.New("ByteTrack worker returned an invalid track response")
	}
	count, err := strconv.Atoi(fields[3])
	if err != nil || count < 0 || count != len(fields)-4 {
		return nil, errors.New("ByteTrack worker returned an invalid association count")
	}
	associations := make([]TrackAssociation, 0, count)
	for _, field := range fields[4:] {
		parts := strings.Split(field, ",")
		if len(parts) != 3 {
			return nil, errors.New("ByteTrack worker returned a malformed association")
		}
		detectionIndex, detectionErr := strconv.Atoi(parts[0])
		classID, classErr := strconv.ParseInt(parts[1], 10, 32)
		trackID, trackErr := strconv.ParseUint(parts[2], 10, 64)
		if detectionErr != nil || classErr != nil || trackErr != nil || trackID == 0 ||
			detectionIndex < 0 || detectionIndex >= len(detections) || int32(classID) != detections[detectionIndex].ClassID {
			return nil, errors.New("ByteTrack worker returned an invalid association")
		}
		associations = append(associations, TrackAssociation{
			DetectionIndex: detectionIndex,
			TrackKey:       strconv.FormatInt(classID, 10) + ":" + strconv.FormatUint(trackID, 10),
		})
	}
	return associations, nil
}

func formatByteTrackFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func byteTrackCameraMotionField(motion *CameraMotionEstimate, minimumConfidence float64) string {
	if motion == nil || motion.Confidence < minimumConfidence || len(motion.Homography) != 9 {
		return "none"
	}
	homography := motion.Homography
	values := make([]string, len(homography))
	for index, value := range homography {
		values[index] = formatByteTrackFloat(value)
	}
	return strings.Join(values, ",")
}
