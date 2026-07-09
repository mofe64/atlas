package models

import "time"

type PerceptionEvent struct {
	ID                 string
	DroneID            string
	CameraDeviceID     string
	VideoSourceID      string
	ObservedAt         time.Time
	FrameID            string
	ModelName          string
	ModelVersion       string
	InferenceLatencyMS float64
	Detections         []PerceptionDetection
	CreatedAt          time.Time
}

type PerceptionDetection struct {
	ClassName  string
	Confidence float64
	BBox       [4]float64
}

type PerceptionHealth struct {
	DroneID          string
	SourceID         string
	InputConnected   bool
	OutputPublishing bool
	ModelLoaded      bool
	Accelerator      string
	FPS              float64
	DroppedFrames    uint64
	LastFrameAt      time.Time
	LastDetectionAt  time.Time
	LastError        string
	ModelName        string
	ModelVersion     string
	UpdatedAt        time.Time
}
