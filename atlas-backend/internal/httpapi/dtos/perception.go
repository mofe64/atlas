package dtos

type PerceptionEventResponse struct {
	ID                 string                        `json:"id"`
	DroneID            string                        `json:"droneId"`
	SourceID           string                        `json:"sourceId"`
	ObservedAt         string                        `json:"observedAt"`
	FrameID            string                        `json:"frameId,omitempty"`
	ModelName          string                        `json:"modelName"`
	ModelVersion       string                        `json:"modelVersion,omitempty"`
	InferenceLatencyMS float64                       `json:"inferenceLatencyMs"`
	Detections         []PerceptionDetectionResponse `json:"detections"`
	CreatedAt          string                        `json:"createdAt"`
}

type PerceptionDetectionResponse struct {
	Class      string     `json:"class"`
	Confidence float64    `json:"confidence"`
	BBox       [4]float64 `json:"bbox"`
}

type PerceptionStatusResponse struct {
	DroneID          string                        `json:"droneId"`
	SourceID         string                        `json:"sourceId,omitempty"`
	InputConnected   bool                          `json:"inputConnected"`
	OutputPublishing bool                          `json:"outputPublishing"`
	ModelLoaded      bool                          `json:"modelLoaded"`
	Accelerator      string                        `json:"accelerator"`
	FPS              float64                       `json:"fps"`
	DroppedFrames    uint64                        `json:"droppedFrames"`
	LastFrameAt      string                        `json:"lastFrameAt,omitempty"`
	LastDetectionAt  string                        `json:"lastDetectionAt,omitempty"`
	LastError        string                        `json:"lastError,omitempty"`
	ModelName        string                        `json:"modelName,omitempty"`
	ModelVersion     string                        `json:"modelVersion,omitempty"`
	UpdatedAt        string                        `json:"updatedAt,omitempty"`
	ActiveCounts     map[string]int                `json:"activeCounts"`
	LatestDetections []PerceptionDetectionResponse `json:"latestDetections"`
	LatestEvent      *PerceptionEventResponse      `json:"latestEvent,omitempty"`
}
