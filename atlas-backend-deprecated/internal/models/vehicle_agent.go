package models

import "time"

type Organization struct {
	ID         string
	Name       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt time.Time
}

type User struct {
	ID             string
	OrganizationID string
	Email          string
	DisplayName    string
	Status         UserStatus
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     time.Time
}

type UserStatus string

const (
	UserStatusActive    UserStatus = "ACTIVE"
	UserStatusSuspended UserStatus = "SUSPENDED"
	UserStatusArchived  UserStatus = "ARCHIVED"
)

type Operator struct {
	ID             string
	UserID         string
	OrganizationID string
	DisplayName    string
	Role           OperatorRole
	Status         OperatorStatus
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     time.Time
}

type OperatorRole string

const (
	OperatorRoleObserver    OperatorRole = "OBSERVER"
	OperatorRoleOperator    OperatorRole = "OPERATOR"
	OperatorRoleFlightAdmin OperatorRole = "FLIGHT_ADMIN"
	OperatorRoleSystemAdmin OperatorRole = "SYSTEM_ADMIN"
)

type OperatorStatus string

const (
	OperatorStatusActive    OperatorStatus = "ACTIVE"
	OperatorStatusSuspended OperatorStatus = "SUSPENDED"
	OperatorStatusArchived  OperatorStatus = "ARCHIVED"
)

type Drone struct {
	ID             string
	OrganizationID string
	Name           string
	SerialNumber   string
	VehicleType    VehicleType
	PX4SystemID    int
	Status         DroneStatus
	HomePosition   GeoPosition
	CurrentState   DroneCurrentState
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     time.Time
	LastSeenAt     time.Time
}

type VehicleType string

const (
	VehicleTypeUnknown     VehicleType = "UNKNOWN"
	VehicleTypeMulticopter VehicleType = "MULTICOPTER"
	VehicleTypeFixedWing   VehicleType = "FIXED_WING"
	VehicleTypeVTOL        VehicleType = "VTOL"
	VehicleTypeRover       VehicleType = "ROVER"
)

type DroneStatus string

const (
	DroneStatusUnknown     DroneStatus = "UNKNOWN"
	DroneStatusRegistered  DroneStatus = "REGISTERED"
	DroneStatusAvailable   DroneStatus = "AVAILABLE"
	DroneStatusConnected   DroneStatus = "CONNECTED"
	DroneStatusStale       DroneStatus = "STALE"
	DroneStatusLost        DroneStatus = "LOST"
	DroneStatusInMission   DroneStatus = "IN_MISSION"
	DroneStatusMaintenance DroneStatus = "MAINTENANCE"
	DroneStatusDisabled    DroneStatus = "DISABLED"
	DroneStatusArchived    DroneStatus = "ARCHIVED"
)

type GeoPosition struct {
	Latitude  float64
	Longitude float64
	AltitudeM float64
}

type DroneCurrentState struct {
	Armed       bool
	InAir       bool
	FlightMode  string
	LastKnownAt time.Time
}

type CompanionDevice struct {
	ID                  string
	DroneID             string
	DeviceName          string
	HardwareType        string
	OSVersion           string
	VehicleAgentVersion string
	Hostname            string
	NetworkInterfaces   map[string]NetworkInterface
	LastSeenAt          time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type NetworkInterface struct {
	Name       string
	MACAddress string
	IPAddress  string
	LinkType   CommunicationLinkType
}

type VehicleAgent struct {
	ID                               string
	DroneID                          string
	CompanionDeviceID                string
	Version                          string
	VehicleAgentVersion              string
	IdentityStatus                   DeviceIdentityStatus
	RegisteredAt                     time.Time
	LastSeenAt                       time.Time
	RevokedAt                        time.Time
	LastHeartbeatAt                  time.Time
	CommandChannelState              CommandChannelState
	CommandChannelConnectedAt        time.Time
	CommandChannelLastDisconnectedAt time.Time
	MAVLinkObserverDiagnostics       map[string]any
	BackendChannelHealth             map[string]any
}

type DeviceIdentityStatus string

const (
	DeviceIdentityPendingRegistration DeviceIdentityStatus = "PENDING_REGISTRATION"
	DeviceIdentityActive              DeviceIdentityStatus = "ACTIVE"
	DeviceIdentitySuspended           DeviceIdentityStatus = "SUSPENDED"
	DeviceIdentityRevoked             DeviceIdentityStatus = "REVOKED"
	DeviceIdentityRotationRequired    DeviceIdentityStatus = "ROTATION_REQUIRED"
)

type VehicleAgentStatus string

const (
	VehicleAgentStatusRegistered VehicleAgentStatus = "registered"
	VehicleAgentStatusOnline     VehicleAgentStatus = "online"
	VehicleAgentStatusStale      VehicleAgentStatus = "stale"
	VehicleAgentStatusOffline    VehicleAgentStatus = "offline"
)

type CommandChannelState string

const (
	CommandChannelDisconnected CommandChannelState = "disconnected"
	CommandChannelConnected    CommandChannelState = "connected"
)

const (
	HeartbeatInterval = 5 * time.Second
	OnlineWindow      = 15 * time.Second
	StaleWindow       = 60 * time.Second
)

func VehicleAgentStatusFromHeartbeat(lastHeartbeatAt time.Time, now time.Time) VehicleAgentStatus {
	if lastHeartbeatAt.IsZero() {
		return VehicleAgentStatusRegistered
	}

	age := now.Sub(lastHeartbeatAt)
	if age <= OnlineWindow {
		return VehicleAgentStatusOnline
	}

	if age <= StaleWindow {
		return VehicleAgentStatusStale
	}

	return VehicleAgentStatusOffline
}
