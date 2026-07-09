package perception

import "time"

type Detection struct {
	ClassName  string
	Confidence float64
	BBox       [4]float64
}

type Event struct {
	DroneID            string
	SourceID           string
	ObservedAt         time.Time
	FrameID            string
	ModelName          string
	ModelVersion       string
	InferenceLatencyMS float64
	Detections         []Detection
}

type Health struct {
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
}

type Source interface {
	Name() string
	Subscribe() (<-chan Event, <-chan Health, <-chan error)
}
