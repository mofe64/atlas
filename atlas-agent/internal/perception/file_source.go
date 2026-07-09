package perception

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultPollInterval = 250 * time.Millisecond

type FileSource struct {
	ctx          context.Context
	path         string
	pollInterval time.Duration
}

type fileEnvelope struct {
	Type               string          `json:"type"`
	DroneID            string          `json:"droneId"`
	SourceID           string          `json:"sourceId"`
	ObservedAt         string          `json:"observedAt"`
	FrameID            string          `json:"frameId"`
	ModelName          string          `json:"modelName"`
	ModelVersion       string          `json:"modelVersion"`
	InferenceLatencyMS float64         `json:"inferenceLatencyMs"`
	Detections         []fileDetection `json:"detections"`
	InputConnected     bool            `json:"inputConnected"`
	OutputPublishing   bool            `json:"outputPublishing"`
	ModelLoaded        bool            `json:"modelLoaded"`
	Accelerator        string          `json:"accelerator"`
	FPS                float64         `json:"fps"`
	DroppedFrames      uint64          `json:"droppedFrames"`
	LastFrameAt        string          `json:"lastFrameAt"`
	LastDetectionAt    string          `json:"lastDetectionAt"`
	LastError          string          `json:"lastError"`
}

type fileDetection struct {
	Class      string     `json:"class"`
	ClassName  string     `json:"className"`
	Confidence float64    `json:"confidence"`
	BBox       [4]float64 `json:"bbox"`
}

func NewFileSource(ctx context.Context, path string) *FileSource {
	return &FileSource{
		ctx:          ctx,
		path:         expandPath(path),
		pollInterval: defaultPollInterval,
	}
}

func (s *FileSource) Name() string {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return "perception-file"
	}
	return "perception-file:" + s.path
}

func (s *FileSource) Subscribe() (<-chan Event, <-chan Health, <-chan error) {
	events := make(chan Event, 16)
	health := make(chan Health, 4)
	errs := make(chan error, 4)

	go func() {
		defer close(events)
		defer close(health)
		defer close(errs)
		s.run(events, health, errs)
	}()

	return events, health, errs
}

func (s *FileSource) run(events chan<- Event, health chan<- Health, errs chan<- error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return
	}

	for s.ctx.Err() == nil {
		if err := s.tail(events, health); err != nil && s.ctx.Err() == nil {
			select {
			case errs <- err:
			default:
			}
			sleepContext(s.ctx, time.Second)
		}
	}
}

func (s *FileSource) tail(events chan<- Event, health chan<- Health) error {
	file, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			sleepContext(s.ctx, time.Second)
			return nil
		}
		return fmt.Errorf("open perception metadata file: %w", err)
	}
	defer file.Close()

	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek perception metadata file: %w", err)
	}

	reader := bufio.NewReader(file)
	for s.ctx.Err() == nil {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				sleepContext(s.ctx, s.pollInterval)
				continue
			}
			return fmt.Errorf("read perception metadata file: %w", err)
		}
		if strings.TrimSpace(line) == "" {
			continue
		}

		envelope, err := parseEnvelope(line)
		if err != nil {
			return err
		}
		switch envelope.Type {
		case "event":
			event, err := envelope.toEvent()
			if err != nil {
				return err
			}
			select {
			case <-s.ctx.Done():
				return nil
			case events <- event:
			}
		case "health":
			item, err := envelope.toHealth()
			if err != nil {
				return err
			}
			select {
			case <-s.ctx.Done():
				return nil
			case health <- item:
			}
		default:
			return fmt.Errorf("unknown perception metadata type: %s", envelope.Type)
		}
	}
	return nil
}

func parseEnvelope(line string) (fileEnvelope, error) {
	var envelope fileEnvelope
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		return fileEnvelope{}, fmt.Errorf("parse perception metadata JSONL: %w", err)
	}
	envelope.Type = strings.ToLower(strings.TrimSpace(envelope.Type))
	if envelope.Type == "" {
		if len(envelope.Detections) > 0 || envelope.ObservedAt != "" || envelope.FrameID != "" {
			envelope.Type = "event"
		} else {
			envelope.Type = "health"
		}
	}
	return envelope, nil
}

func (e fileEnvelope) toEvent() (Event, error) {
	observedAt, err := parseRequiredTime(e.ObservedAt, "observedAt")
	if err != nil {
		return Event{}, err
	}
	detections := make([]Detection, 0, len(e.Detections))
	for _, detection := range e.Detections {
		className := detection.Class
		if className == "" {
			className = detection.ClassName
		}
		detections = append(detections, Detection{
			ClassName:  className,
			Confidence: detection.Confidence,
			BBox:       detection.BBox,
		})
	}
	return Event{
		DroneID:            e.DroneID,
		SourceID:           e.SourceID,
		ObservedAt:         observedAt,
		FrameID:            e.FrameID,
		ModelName:          e.ModelName,
		ModelVersion:       e.ModelVersion,
		InferenceLatencyMS: e.InferenceLatencyMS,
		Detections:         detections,
	}, nil
}

func (e fileEnvelope) toHealth() (Health, error) {
	lastFrameAt, err := parseOptionalTime(e.LastFrameAt, "lastFrameAt")
	if err != nil {
		return Health{}, err
	}
	lastDetectionAt, err := parseOptionalTime(e.LastDetectionAt, "lastDetectionAt")
	if err != nil {
		return Health{}, err
	}
	return Health{
		DroneID:          e.DroneID,
		SourceID:         e.SourceID,
		InputConnected:   e.InputConnected,
		OutputPublishing: e.OutputPublishing,
		ModelLoaded:      e.ModelLoaded,
		Accelerator:      e.Accelerator,
		FPS:              e.FPS,
		DroppedFrames:    e.DroppedFrames,
		LastFrameAt:      lastFrameAt,
		LastDetectionAt:  lastDetectionAt,
		LastError:        e.LastError,
		ModelName:        e.ModelName,
		ModelVersion:     e.ModelVersion,
	}, nil
}

func parseRequiredTime(value string, field string) (time.Time, error) {
	parsed, err := parseOptionalTime(value, field)
	if err != nil {
		return time.Time{}, err
	}
	if parsed.IsZero() {
		return time.Time{}, fmt.Errorf("%s is required", field)
	}
	return parsed, nil
}

func parseOptionalTime(value string, field string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339: %w", field, err)
	}
	return parsed.UTC(), nil
}

func expandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "~" {
		return path
	}
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
}

func sleepContext(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
