package models

import "time"

type MissionValidationStatus string

const (
	MissionValidationStatusNotValidated MissionValidationStatus = "not_validated"
	MissionValidationStatusValidated    MissionValidationStatus = "validated"
	MissionValidationStatusRejected     MissionValidationStatus = "rejected"
	MissionValidationStatusDraft        MissionValidationStatus = "DRAFT"
	MissionValidationStatusInvalid      MissionValidationStatus = "INVALID"
	MissionValidationStatusSuperseded   MissionValidationStatus = "SUPERSEDED"
)

type MissionCompletionAction string

const (
	MissionCompletionActionHold           MissionCompletionAction = "hold"
	MissionCompletionActionReturnToLaunch MissionCompletionAction = "return_to_launch"
	MissionCompletionActionLand           MissionCompletionAction = "land"
)

type MissionExecutionState string

const (
	MissionExecutionStateUnknown              MissionExecutionState = "unknown"
	MissionExecutionStateCreated              MissionExecutionState = "created"
	MissionExecutionStateValidating           MissionExecutionState = "validating"
	MissionExecutionStateValidated            MissionExecutionState = "validated"
	MissionExecutionStateRejectedByValidation MissionExecutionState = "rejected_by_validation"
	MissionExecutionStateUploadRequested      MissionExecutionState = "upload_requested"
	MissionExecutionStateUploading            MissionExecutionState = "uploading"
	MissionExecutionStateUploadFailed         MissionExecutionState = "upload_failed"
	MissionExecutionStateUploadedToVehicle    MissionExecutionState = "uploaded_to_vehicle"
	MissionExecutionStateStartRequested       MissionExecutionState = "start_requested"
	MissionExecutionStateActive               MissionExecutionState = "active"
	MissionExecutionStatePaused               MissionExecutionState = "paused"
	MissionExecutionStateHold                 MissionExecutionState = "hold"
	MissionExecutionStatePausedOrHold         MissionExecutionState = "paused_or_hold"
	MissionExecutionStateRTLRequested         MissionExecutionState = "rtl_requested"
	MissionExecutionStateCompleted            MissionExecutionState = "completed"
	MissionExecutionStateAbortRequested       MissionExecutionState = "abort_requested"
	MissionExecutionStateAborted              MissionExecutionState = "aborted"
	MissionExecutionStateFailed               MissionExecutionState = "failed"
)

const MissionExecutionDeliveryLeaseDuration = 10 * time.Second

type Mission struct {
	ID                  string
	OrganizationID      string
	DroneID             string
	Name                string
	Description         string
	CreatedBy           string
	CreatedByOperatorID string
	CurrentVersionID    string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ArchivedAt          time.Time
	Waypoints           []MissionWaypoint
	CompletionAction    MissionCompletionAction
	ValidationStatus    MissionValidationStatus
	ValidationErrors    []MissionValidationError
}

type MissionVersion struct {
	ID                  string
	MissionID           string
	VersionNumber       int
	Waypoints           []MissionWaypoint
	AltitudePolicy      MissionAltitudePolicy
	SpeedPolicy         MissionSpeedPolicy
	GeofencePolicy      MissionGeofencePolicy
	RTLPolicy           MissionRTLPolicy
	ValidationStatus    MissionValidationStatus
	ValidationErrors    []MissionValidationError
	CreatedByOperatorID string
	CreatedAt           time.Time
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

type MissionAltitudePolicy struct {
	MinimumRelativeAltitudeM float64
	MaximumRelativeAltitudeM float64
}

type MissionSpeedPolicy struct {
	DefaultSpeedMPS float64
	MaximumSpeedMPS float64
}

type MissionGeofencePolicy struct {
	Enabled bool
}

type MissionRTLPolicy struct {
	CompletionAction MissionCompletionAction
}

type MissionExecution struct {
	ID                    string
	MissionID             string
	MissionVersionID      string
	DroneID               string
	VehicleAgentID        string
	RequestedBy           string
	RequestedByOperatorID string
	UploadRequestedBy     string
	StartRequestedBy      string
	State                 MissionExecutionState
	CreatedAt             time.Time
	UpdatedAt             time.Time
	LastSentAt            time.Time
	LeaseUntil            time.Time
	UploadRequestedAt     time.Time
	UploadedAt            time.Time
	StartRequestedAt      time.Time
	StartedAt             time.Time
	CompletedAt           time.Time
	AbortedAt             time.Time
	HoldAt                time.Time
	FailedAt              time.Time
	FailureReason         string
	CurrentMissionItem    int
	TotalMissionItems     int
	ProgressUpdatedAt     time.Time
	DeliveryAttempt       int
	ResultMessage         string
}

type MissionExecutionEvent struct {
	ID                 string
	ExecutionID        string
	MissionID          string
	MissionVersionID   string
	DroneID            string
	VehicleAgentID     string
	Type               string
	State              MissionExecutionState
	Message            string
	CurrentMissionItem int
	TotalMissionItems  int
	Source             string
	CreatedAt          time.Time
}
