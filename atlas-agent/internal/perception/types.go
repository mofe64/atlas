// Package perception defines the accelerator-neutral boundary between an
// onboard inference runtime and Atlas Agent.
package perception

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const RuntimeProtocolVersion = "1"

type ModelIdentity struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	ArtifactHash string `json:"artifactHash,omitempty"`
}

type BoundingBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type Detection struct {
	TrackID       string          `json:"trackId,omitempty"`
	ClassID       int32           `json:"classId"`
	ClassLabel    string          `json:"classLabel"`
	Confidence    float64         `json:"confidence"`
	BoundingBox   BoundingBox     `json:"boundingBox"`
	AttributesRaw json.RawMessage `json:"attributes,omitempty"`
}

type Frame struct {
	SourceID           string        `json:"sourceId"`
	StreamEpoch        string        `json:"streamEpoch"`
	FrameID            string        `json:"frameId"`
	ObservedAt         time.Time     `json:"observedAt"`
	SourcePTSNS        int64         `json:"sourcePtsNs"`
	ImageWidth         uint32        `json:"imageWidth"`
	ImageHeight        uint32        `json:"imageHeight"`
	Model              ModelIdentity `json:"model"`
	InferenceLatencyMS float64       `json:"inferenceLatencyMs"`
	Detections         []Detection   `json:"detections"`
}

type Health struct {
	SourceID         string        `json:"sourceId"`
	Provider         string        `json:"provider"`
	Accelerator      string        `json:"accelerator,omitempty"`
	InputConnected   bool          `json:"inputConnected"`
	InferenceReady   bool          `json:"inferenceReady"`
	OutputPublishing bool          `json:"outputPublishing"`
	InputFPS         float64       `json:"inputFps"`
	InferenceFPS     float64       `json:"inferenceFps"`
	DroppedFrames    uint64        `json:"droppedFrames"`
	LastFrameAt      time.Time     `json:"lastFrameAt,omitempty"`
	LastDetectionAt  time.Time     `json:"lastDetectionAt,omitempty"`
	LastError        string        `json:"lastError,omitempty"`
	Model            ModelIdentity `json:"model"`
	ObservedAt       time.Time     `json:"observedAt"`
}

type Outputs struct {
	Frames <-chan Frame
	Health <-chan Health
}

func (frame Frame) Validate() error {
	if strings.TrimSpace(frame.SourceID) == "" || strings.TrimSpace(frame.StreamEpoch) == "" || strings.TrimSpace(frame.FrameID) == "" {
		return errors.New("sourceId, streamEpoch, and frameId are required")
	}
	if frame.ObservedAt.IsZero() {
		return errors.New("observedAt is required")
	}
	if frame.ImageWidth == 0 || frame.ImageHeight == 0 {
		return errors.New("imageWidth and imageHeight must be positive")
	}
	if strings.TrimSpace(frame.Model.Name) == "" || strings.TrimSpace(frame.Model.Version) == "" {
		return errors.New("model name and version are required")
	}
	if !finiteNonNegative(frame.InferenceLatencyMS) {
		return errors.New("inferenceLatencyMs must be finite and non-negative")
	}
	for index, detection := range frame.Detections {
		if err := detection.Validate(); err != nil {
			return fmt.Errorf("detection %d: %w", index, err)
		}
	}
	return nil
}

func (detection Detection) Validate() error {
	if strings.TrimSpace(detection.ClassLabel) == "" {
		return errors.New("classLabel is required")
	}
	if !finiteUnit(detection.Confidence) {
		return errors.New("confidence must be between 0 and 1")
	}
	box := detection.BoundingBox
	if !finiteUnit(box.X) || !finiteUnit(box.Y) || !finiteUnit(box.Width) || !finiteUnit(box.Height) {
		return errors.New("boundingBox values must be between 0 and 1")
	}
	const coordinateTolerance = 1e-9
	if box.X+box.Width > 1+coordinateTolerance || box.Y+box.Height > 1+coordinateTolerance {
		return errors.New("boundingBox must remain inside the normalized frame")
	}
	if len(detection.AttributesRaw) > 0 && !json.Valid(detection.AttributesRaw) {
		return errors.New("attributes must contain valid JSON")
	}
	return nil
}

func (health Health) Validate() error {
	if strings.TrimSpace(health.SourceID) == "" || strings.TrimSpace(health.Provider) == "" {
		return errors.New("sourceId and provider are required")
	}
	if health.ObservedAt.IsZero() {
		return errors.New("observedAt is required")
	}
	if !finiteNonNegative(health.InputFPS) || !finiteNonNegative(health.InferenceFPS) {
		return errors.New("inputFps and inferenceFps must be finite and non-negative")
	}
	modelNamePresent := strings.TrimSpace(health.Model.Name) != ""
	modelVersionPresent := strings.TrimSpace(health.Model.Version) != ""
	if modelNamePresent != modelVersionPresent {
		return errors.New("model name and version must either both be present or both be absent")
	}
	return nil
}

func finiteUnit(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
