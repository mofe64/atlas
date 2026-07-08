package models

import "time"

type VideoFeed struct {
	ID                  string
	DroneID             string
	CameraDeviceID      string
	SourceType          VideoFeedSourceType
	SourceID            string
	CommunicationLinkID string
	Status              VideoFeedStatus
	TransportIn         string
	TransportOut        string
	Codec               string
	Resolution          string
	BitrateKbps         float64
	FrameRate           float64
	LatencyMs           float64
	LastFrameAt         time.Time
	StartedAt           time.Time
	EndedAt             time.Time
	EndedReason         string
}

type VideoFeedSourceType string

const (
	VideoFeedSourceOnboardCamera    VideoFeedSourceType = "ONBOARD_CAMERA"
	VideoFeedSourceA8Camera         VideoFeedSourceType = "A8_CAMERA"
	VideoFeedSourceGroundBridgeHM30 VideoFeedSourceType = "GROUND_BRIDGE_HM30"
	VideoFeedSourceTestPattern      VideoFeedSourceType = "TEST_PATTERN"
	VideoFeedSourceFileReplay       VideoFeedSourceType = "FILE_REPLAY"
	VideoFeedSourceUnknown          VideoFeedSourceType = "UNKNOWN"
)

type VideoFeedStatus string

const (
	VideoFeedStatusStarting VideoFeedStatus = "STARTING"
	VideoFeedStatusActive   VideoFeedStatus = "ACTIVE"
	VideoFeedStatusDegraded VideoFeedStatus = "DEGRADED"
	VideoFeedStatusStale    VideoFeedStatus = "STALE"
	VideoFeedStatusLost     VideoFeedStatus = "LOST"
	VideoFeedStatusStopped  VideoFeedStatus = "STOPPED"
	VideoFeedStatusFailed   VideoFeedStatus = "FAILED"
)

type CameraDevice struct {
	ID                string
	DroneID           string
	Name              string
	CameraType        CameraType
	SourceDescription string
	Status            DeviceRuntimeStatus
	ActiveVideoFeedID string
	LastSeenAt        time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type CameraType string

const (
	CameraTypeSIYIA8  CameraType = "SIYI_A8"
	CameraTypeUSB     CameraType = "USB_CAMERA"
	CameraTypeCSI     CameraType = "CSI_CAMERA"
	CameraTypeRTSP    CameraType = "RTSP_CAMERA"
	CameraTypeTest    CameraType = "TEST_CAMERA"
	CameraTypeUnknown CameraType = "UNKNOWN"
)

type GimbalDevice struct {
	ID            string
	DroneID       string
	Name          string
	GimbalType    GimbalType
	Status        DeviceRuntimeStatus
	PitchDeg      float64
	YawDeg        float64
	RollDeg       float64
	ControlSource GimbalControlSource
	LastSeenAt    time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type GimbalType string

const (
	GimbalTypeSIYIA8  GimbalType = "SIYI_A8"
	GimbalTypeUnknown GimbalType = "UNKNOWN"
)

type DeviceRuntimeStatus string

const (
	DeviceRuntimeStatusUnknown  DeviceRuntimeStatus = "UNKNOWN"
	DeviceRuntimeStatusActive   DeviceRuntimeStatus = "ACTIVE"
	DeviceRuntimeStatusDegraded DeviceRuntimeStatus = "DEGRADED"
	DeviceRuntimeStatusLost     DeviceRuntimeStatus = "LOST"
	DeviceRuntimeStatusDisabled DeviceRuntimeStatus = "DISABLED"
)

type GimbalControlSource string

const (
	GimbalControlSourceAtlasUI        GimbalControlSource = "ATLAS_UI"
	GimbalControlSourceHM30           GimbalControlSource = "HM30_CONTROLLER"
	GimbalControlSourceQGC            GimbalControlSource = "QGROUND_CONTROL"
	GimbalControlSourceOnboardLogic   GimbalControlSource = "ONBOARD_LOGIC"
	GimbalControlSourceManualOperator GimbalControlSource = "MANUAL_OPERATOR"
	GimbalControlSourceUnknown        GimbalControlSource = "UNKNOWN"
)

type PerceptionEvent struct {
	ID             string
	DroneID        string
	CameraDeviceID string
	VideoFeedID    string
	Timestamp      time.Time
	FrameID        string
	ModelName      string
	ModelVersion   string
	Detections     []PerceptionDetection
	CreatedAt      time.Time
}

type PerceptionDetection struct {
	Class      string
	Confidence float64
	BBox       [4]float64
}
