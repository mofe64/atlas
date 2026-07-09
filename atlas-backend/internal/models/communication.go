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

// DroneVehicleAgentConnection is one runtime backend-to-agent session for a
// drone. The vehicle agent identity is long-lived, while this record is
// intentionally per connection so reconnects, drops, and command-routing
// decisions can be audited without inheriting state from an older stream.
type DroneVehicleAgentConnection struct {
	ID                  string
	VehicleAgentID      string
	DroneID             string
	ConnectionID        string
	Transport           string
	RemoteAddress       string
	WireGuardPeerID     string
	Status              DroneVehicleAgentConnectionStatus
	StartedAt           time.Time
	LastHeartbeatAt     time.Time
	EndedAt             time.Time
	EndedReason         string
	VehicleAgentVersion string
	Capabilities        []string
}

// DroneVehicleAgentConnectionStatus describes the lifecycle of one
// backend-to-agent connection, not the overall drone or agent identity.
type DroneVehicleAgentConnectionStatus string

const (
	DroneVehicleAgentConnectionConnecting   DroneVehicleAgentConnectionStatus = "CONNECTING"
	DroneVehicleAgentConnectionConnected    DroneVehicleAgentConnectionStatus = "CONNECTED"
	DroneVehicleAgentConnectionDegraded     DroneVehicleAgentConnectionStatus = "DEGRADED"
	DroneVehicleAgentConnectionStale        DroneVehicleAgentConnectionStatus = "STALE"
	DroneVehicleAgentConnectionDisconnected DroneVehicleAgentConnectionStatus = "DISCONNECTED"
	DroneVehicleAgentConnectionRejected     DroneVehicleAgentConnectionStatus = "REJECTED"
	DroneVehicleAgentConnectionRevoked      DroneVehicleAgentConnectionStatus = "REVOKED"
)

// CommunicationLink is the observed network path for a drone. It lets Atlas
// describe shared path health independently from the feeds or sessions that use
// the path; for example, a degraded ground-unit data link can affect both video
// and telemetry feeds that reference the same link.
type CommunicationLink struct {
	ID                            string
	DroneID                       string
	VehicleAgentID                string
	DroneVehicleAgentConnectionID string
	LinkType                      CommunicationLinkType
	Roles                         []CommunicationLinkRole
	Status                        CommunicationLinkStatus
	Transport                     string
	EndpointDescription           string
	CommandEligible               bool
	LatencyMs                     float64
	PacketLossEstimate            float64
	RxBytesPerSec                 float64
	TxBytesPerSec                 float64
	LastSeenAt                    time.Time
	CreatedAt                     time.Time
	EndedAt                       time.Time
	EndedReason                   string
}

type CommunicationLinkType string

const (
	CommunicationLinkVehicleAgentGRPC   CommunicationLinkType = "VEHICLE_AGENT_GRPC"
	CommunicationLinkGroundUnitDataLink CommunicationLinkType = "GROUND_UNIT_DATA_LINK"
	CommunicationLinkUnknown            CommunicationLinkType = "UNKNOWN"
)

type CommunicationLinkRole string

const (
	CommunicationLinkRoleTelemetry     CommunicationLinkRole = "TELEMETRY"
	CommunicationLinkRoleCommand       CommunicationLinkRole = "COMMAND"
	CommunicationLinkRoleVideo         CommunicationLinkRole = "VIDEO"
	CommunicationLinkRoleGimbalControl CommunicationLinkRole = "GIMBAL_CONTROL"
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
