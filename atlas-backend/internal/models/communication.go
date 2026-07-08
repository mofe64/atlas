package models

import "time"

type CommunicationPlane string

const (
	CommunicationPlaneC2Telemetry        CommunicationPlane = "C2_TELEMETRY"
	CommunicationPlaneVideo              CommunicationPlane = "VIDEO"
	CommunicationPlaneMissionFleet       CommunicationPlane = "MISSION_FLEET"
	CommunicationPlaneArtifactSync       CommunicationPlane = "ARTIFACT_SYNC"
	CommunicationPlaneManualSafety       CommunicationPlane = "MANUAL_SAFETY"
	CommunicationPlaneDebugObservability CommunicationPlane = "DEBUG_OBSERVABILITY"
)

type DroneConnection struct {
	ID                  string
	VehicleAgentID      string
	DroneID             string
	ConnectionID        string
	Transport           string
	RemoteAddress       string
	WireGuardPeerID     string
	Status              DroneConnectionStatus
	StartedAt           time.Time
	LastHeartbeatAt     time.Time
	EndedAt             time.Time
	EndedReason         string
	VehicleAgentVersion string
	Capabilities        []string
}

type DroneConnectionStatus string

const (
	DroneConnectionConnecting   DroneConnectionStatus = "CONNECTING"
	DroneConnectionConnected    DroneConnectionStatus = "CONNECTED"
	DroneConnectionDegraded     DroneConnectionStatus = "DEGRADED"
	DroneConnectionStale        DroneConnectionStatus = "STALE"
	DroneConnectionDisconnected DroneConnectionStatus = "DISCONNECTED"
	DroneConnectionRejected     DroneConnectionStatus = "REJECTED"
	DroneConnectionRevoked      DroneConnectionStatus = "REVOKED"
)

type GroundBridge struct {
	ID             string
	OrganizationID string
	Name           string
	DeviceType     string
	BridgeVersion  string
	IdentityStatus DeviceIdentityStatus
	RegisteredAt   time.Time
	LastSeenAt     time.Time
	RevokedAt      time.Time
}

type GroundBridgeConnection struct {
	ID                string
	GroundBridgeID    string
	OrganizationID    string
	Status            GroundBridgeConnectionStatus
	Mode              GroundBridgeMode
	StartedAt         time.Time
	LastHeartbeatAt   time.Time
	EndedAt           time.Time
	EndedReason       string
	InternetAvailable bool
	LocalOnly         bool
	Capabilities      []string
}

type GroundBridgeConnectionStatus string

const (
	GroundBridgeConnectionConnecting   GroundBridgeConnectionStatus = "CONNECTING"
	GroundBridgeConnectionConnected    GroundBridgeConnectionStatus = "CONNECTED"
	GroundBridgeConnectionDegraded     GroundBridgeConnectionStatus = "DEGRADED"
	GroundBridgeConnectionStale        GroundBridgeConnectionStatus = "STALE"
	GroundBridgeConnectionDisconnected GroundBridgeConnectionStatus = "DISCONNECTED"
	GroundBridgeConnectionRejected     GroundBridgeConnectionStatus = "REJECTED"
	GroundBridgeConnectionRevoked      GroundBridgeConnectionStatus = "REVOKED"
)

type GroundBridgeMode string

const (
	GroundBridgeModeCloudConnected   GroundBridgeMode = "CLOUD_CONNECTED"
	GroundBridgeModeLocalOnly        GroundBridgeMode = "LOCAL_ONLY"
	GroundBridgeModeCloudRelay       GroundBridgeMode = "CLOUD_RELAY"
	GroundBridgeModeOfflineRecording GroundBridgeMode = "OFFLINE_RECORDING"
)

type CommunicationLink struct {
	ID                       string
	DroneID                  string
	VehicleAgentID           string
	GroundBridgeID           string
	DroneConnectionID        string
	GroundBridgeConnectionID string
	LinkType                 CommunicationLinkType
	Roles                    []CommunicationLinkRole
	Status                   CommunicationLinkStatus
	Transport                string
	EndpointDescription      string
	CommandEligible          bool
	LatencyMs                float64
	PacketLossEstimate       float64
	RxBytesPerSec            float64
	TxBytesPerSec            float64
	LastSeenAt               time.Time
	CreatedAt                time.Time
	EndedAt                  time.Time
	EndedReason              string
}

type CommunicationLinkType string

const (
	CommunicationLinkSITL          CommunicationLinkType = "SITL"
	CommunicationLinkUSBSerial     CommunicationLinkType = "USB_SERIAL"
	CommunicationLinkWiFi          CommunicationLinkType = "WIFI"
	CommunicationLinkLTEWireGuard  CommunicationLinkType = "LTE_WIREGUARD"
	CommunicationLinkSiKSerial     CommunicationLinkType = "SIK_SERIAL"
	CommunicationLinkHM30          CommunicationLinkType = "HM30"
	CommunicationLinkMK15          CommunicationLinkType = "MK15"
	CommunicationLinkMK32          CommunicationLinkType = "MK32"
	CommunicationLinkLocalEthernet CommunicationLinkType = "LOCAL_ETHERNET"
	CommunicationLinkDockWiFi      CommunicationLinkType = "DOCK_WIFI"
	CommunicationLinkDockEthernet  CommunicationLinkType = "DOCK_ETHERNET"
	CommunicationLinkSiteEthernet  CommunicationLinkType = "SITE_ETHERNET"
	CommunicationLinkManualRC      CommunicationLinkType = "MANUAL_RC"
	CommunicationLinkQGCExternal   CommunicationLinkType = "QGC_EXTERNAL"
	CommunicationLinkUnknown       CommunicationLinkType = "UNKNOWN"
)

type CommunicationLinkRole string

const (
	CommunicationLinkRoleTelemetry        CommunicationLinkRole = "TELEMETRY"
	CommunicationLinkRoleCommand          CommunicationLinkRole = "COMMAND"
	CommunicationLinkRoleVideo            CommunicationLinkRole = "VIDEO"
	CommunicationLinkRoleGimbalControl    CommunicationLinkRole = "GIMBAL_CONTROL"
	CommunicationLinkRoleGimbalObserver   CommunicationLinkRole = "GIMBAL_OBSERVER"
	CommunicationLinkRoleManualRC         CommunicationLinkRole = "MANUAL_RC"
	CommunicationLinkRoleBackup           CommunicationLinkRole = "BACKUP"
	CommunicationLinkRoleObserver         CommunicationLinkRole = "OBSERVER"
	CommunicationLinkRoleDebug            CommunicationLinkRole = "DEBUG"
	CommunicationLinkRoleCloudBackend     CommunicationLinkRole = "CLOUD_BACKEND"
	CommunicationLinkRoleQGCCompatibility CommunicationLinkRole = "QGC_COMPATIBILITY"
)

type CommunicationLinkStatus string

const (
	CommunicationLinkStatusUnknown    CommunicationLinkStatus = "UNKNOWN"
	CommunicationLinkStatusConnected  CommunicationLinkStatus = "CONNECTED"
	CommunicationLinkStatusDegraded   CommunicationLinkStatus = "DEGRADED"
	CommunicationLinkStatusStale      CommunicationLinkStatus = "STALE"
	CommunicationLinkStatusLost       CommunicationLinkStatus = "LOST"
	CommunicationLinkStatusDisabled   CommunicationLinkStatus = "DISABLED"
	CommunicationLinkStatusConflicted CommunicationLinkStatus = "CONFLICTED"
)

type LinkHealthSample struct {
	ID                  string
	CommunicationLinkID string
	DroneID             string
	SampledAt           time.Time
	Status              CommunicationLinkStatus
	LatencyMs           float64
	PacketLossEstimate  float64
	RxBytesPerSec       float64
	TxBytesPerSec       float64
	Quality             string
}
