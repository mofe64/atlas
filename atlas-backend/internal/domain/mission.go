package domain

import "time"

type MissionValidationStatus string

const (
	MissionValidationStatusNotValidated MissionValidationStatus = "not_validated"
	MissionValidationStatusValidated    MissionValidationStatus = "validated"
	MissionValidationStatusRejected     MissionValidationStatus = "rejected"
)

type MissionCompletionAction string

const (
	MissionCompletionActionHold           MissionCompletionAction = "hold"
	MissionCompletionActionReturnToLaunch MissionCompletionAction = "return_to_launch"
	MissionCompletionActionLand           MissionCompletionAction = "land"
)

type MissionExecutionState string

const (
	MissionExecutionStateUnknown           MissionExecutionState = "unknown"
	MissionExecutionStateCreated           MissionExecutionState = "created"
	MissionExecutionStateUploadRequested   MissionExecutionState = "upload_requested"
	MissionExecutionStateUploading         MissionExecutionState = "uploading"
	MissionExecutionStateUploadFailed      MissionExecutionState = "upload_failed"
	MissionExecutionStateUploadedToVehicle MissionExecutionState = "uploaded_to_vehicle"
	MissionExecutionStateStartRequested    MissionExecutionState = "start_requested"
	MissionExecutionStateActive            MissionExecutionState = "active"
	MissionExecutionStateHold              MissionExecutionState = "hold"
	MissionExecutionStatePausedOrHold      MissionExecutionState = "paused_or_hold"
	MissionExecutionStateRTLRequested      MissionExecutionState = "rtl_requested"
	MissionExecutionStateCompleted         MissionExecutionState = "completed"
	MissionExecutionStateAborted           MissionExecutionState = "aborted"
	MissionExecutionStateFailed            MissionExecutionState = "failed"
)

const MissionExecutionDeliveryLeaseDuration = 10 * time.Second

type Mission struct {
	ID               string
	DroneID          string
	Name             string
	CreatedBy        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Waypoints        []MissionWaypoint
	CompletionAction MissionCompletionAction
	ValidationStatus MissionValidationStatus
	ValidationErrors []MissionValidationError
}

type MissionWaypoint struct {
	Sequence          int
	Latitude          float64
	Longitude         float64
	RelativeAltitudeM float64
	SpeedMPS          *float64
	LoiterTimeS       *float64
}

type MissionValidationError struct {
	Field   string
	Message string
}

type MissionExecution struct {
	ID                 string
	MissionID          string
	DroneID            string
	AgentID            string
	RequestedBy        string
	UploadRequestedBy  string
	StartRequestedBy   string
	State              MissionExecutionState
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastSentAt         time.Time
	LeaseUntil         time.Time
	UploadRequestedAt  time.Time
	UploadedAt         time.Time
	StartRequestedAt   time.Time
	StartedAt          time.Time
	CompletedAt        time.Time
	HoldAt             time.Time
	FailedAt           time.Time
	CurrentMissionItem int
	TotalMissionItems  int
	ProgressUpdatedAt  time.Time
	DeliveryAttempt    int
	ResultMessage      string
}

type MissionExecutionEvent struct {
	ID                 string
	ExecutionID        string
	MissionID          string
	DroneID            string
	AgentID            string
	Type               string
	State              MissionExecutionState
	Message            string
	CurrentMissionItem int
	TotalMissionItems  int
	Source             string
	CreatedAt          time.Time
}
