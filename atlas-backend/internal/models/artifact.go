package models

import "time"

type ArtifactSync struct {
	ID                 string
	DroneID            string
	VehicleAgentID     string
	SourceDeviceID     string
	MissionExecutionID string
	ArtifactType       ArtifactType
	StorageLocation    string
	SyncStatus         ArtifactSyncStatus
	Priority           int
	BytesTotal         int64
	BytesUploaded      int64
	StartedAt          time.Time
	CompletedAt        time.Time
	FailedAt           time.Time
	FailureReason      string
	CreatedAt          time.Time
}

type ArtifactType string

const (
	ArtifactTypeFlightLog        ArtifactType = "FLIGHT_LOG"
	ArtifactTypePhoto            ArtifactType = "PHOTO"
	ArtifactTypeRecordedVideo    ArtifactType = "RECORDED_VIDEO"
	ArtifactTypePerceptionOutput ArtifactType = "PERCEPTION_OUTPUT"
	ArtifactTypeDebugBundle      ArtifactType = "DEBUG_BUNDLE"
	ArtifactTypeMissionEvidence  ArtifactType = "MISSION_EVIDENCE"
	ArtifactTypeInspectionData   ArtifactType = "INSPECTION_DATA"
	ArtifactTypeOther            ArtifactType = "OTHER"
)

type ArtifactSyncStatus string

const (
	ArtifactSyncPending   ArtifactSyncStatus = "PENDING"
	ArtifactSyncUploading ArtifactSyncStatus = "UPLOADING"
	ArtifactSyncPaused    ArtifactSyncStatus = "PAUSED"
	ArtifactSyncCompleted ArtifactSyncStatus = "COMPLETED"
	ArtifactSyncFailed    ArtifactSyncStatus = "FAILED"
	ArtifactSyncCancelled ArtifactSyncStatus = "CANCELLED"
)

type ArtifactMetadata struct {
	ID                 string
	ArtifactSyncID     string
	DroneID            string
	MissionExecutionID string
	ArtifactType       ArtifactType
	FileName           string
	ContentType        string
	StorageLocation    string
	ChecksumSHA256     string
	Bytes              int64
	Metadata           map[string]any
	CreatedAt          time.Time
}

type AuditEvent struct {
	ID             string
	OrganizationID string
	DroneID        string
	OperatorID     string
	EventType      string
	EntityType     string
	EntityID       string
	Message        string
	Metadata       map[string]any
	CreatedAt      time.Time
}

type DeviceCredential struct {
	ID             string
	DeviceType     DeviceCredentialDeviceType
	DeviceID       string
	CredentialType DeviceCredentialType
	Status         DeviceCredentialStatus
	IssuedAt       time.Time
	ExpiresAt      time.Time
	RotatedAt      time.Time
	RevokedAt      time.Time
	LastUsedAt     time.Time
}

type DeviceCredentialDeviceType string

const (
	DeviceCredentialDeviceAgent DeviceCredentialDeviceType = "AGENT"
)

type DeviceCredentialType string

const (
	DeviceCredentialToken          DeviceCredentialType = "DEVICE_TOKEN"
	DeviceCredentialMTLSCert       DeviceCredentialType = "MTLS_CERTIFICATE"
	DeviceCredentialWireGuardPeer  DeviceCredentialType = "WIREGUARD_PEER"
	DeviceCredentialAPIKey         DeviceCredentialType = "API_KEY"
	DeviceCredentialLocalOnlyToken DeviceCredentialType = "LOCAL_ONLY_TOKEN"
)

type DeviceCredentialStatus string

const (
	DeviceCredentialActive           DeviceCredentialStatus = "ACTIVE"
	DeviceCredentialSuspended        DeviceCredentialStatus = "SUSPENDED"
	DeviceCredentialRevoked          DeviceCredentialStatus = "REVOKED"
	DeviceCredentialRotationRequired DeviceCredentialStatus = "ROTATION_REQUIRED"
	DeviceCredentialExpired          DeviceCredentialStatus = "EXPIRED"
)
