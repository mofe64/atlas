package models

import "time"

type DroneStatus string

const (
	DroneStatusActive      DroneStatus = "active"
	DroneStatusMaintenance DroneStatus = "maintenance"
	DroneStatusDisabled    DroneStatus = "disabled"
	DroneStatusArchived    DroneStatus = "archived"
)

type VehicleType string

const (
	VehicleTypeUnknown     VehicleType = "unknown"
	VehicleTypeMulticopter VehicleType = "multicopter"
	VehicleTypeVTOL        VehicleType = "vtol"
)

// Drone is the organization-owned physical vehicle. Runtime connectivity is
// derived from active bindings and communication links rather than stored here.
type Drone struct {
	ID                  string
	OrganizationID      string
	Name                string
	FlightControllerUID string
	SerialNumber        string
	VehicleType         VehicleType
	Status              DroneStatus
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ArchivedAt          *time.Time
}

type VehicleAgentStatus string

const (
	VehicleAgentStatusActive           VehicleAgentStatus = "active"
	VehicleAgentStatusSuspended        VehicleAgentStatus = "suspended"
	VehicleAgentStatusRevoked          VehicleAgentStatus = "revoked"
	VehicleAgentStatusRotationRequired VehicleAgentStatus = "rotation_required"
)

// DeviceProfile contains slow-changing facts about the onboard computer. Live
// resource usage belongs in heartbeats and communication-link health instead.
type DeviceProfile struct {
	DisplayName      string `json:"displayName,omitempty"`
	Hostname         string `json:"hostname,omitempty"`
	Manufacturer     string `json:"manufacturer,omitempty"`
	Model            string `json:"model,omitempty"`
	HardwareID       string `json:"hardwareId,omitempty"`
	HardwareIDSource string `json:"hardwareIdSource,omitempty"`
	CPUArchitecture  string `json:"cpuArchitecture,omitempty"`
	CPUModel         string `json:"cpuModel,omitempty"`
	CPUCores         int    `json:"cpuCores,omitempty"`
	TotalMemoryBytes uint64 `json:"totalMemoryBytes,omitempty"`
	OSName           string `json:"osName,omitempty"`
	OSVersion        string `json:"osVersion,omitempty"`
	KernelVersion    string `json:"kernelVersion,omitempty"`
}

// VehicleAgent is one enrolled Atlas Agent installation. Its Ed25519 public
// key is the durable device identity that a future transport can authenticate.
type VehicleAgent struct {
	ID                  string
	OrganizationID      string
	InstallationID      string
	PublicKey           []byte
	Status              VehicleAgentStatus
	AgentVersion        string
	ProtocolVersion     string
	DeviceProfile       DeviceProfile
	Capabilities        []string
	EnrollmentRequestID string
	EnrolledAt          time.Time
	UpdatedAt           time.Time
	RevokedAt           *time.Time
}

type VehicleAgentBindingStatus string

const (
	VehicleAgentBindingPending   VehicleAgentBindingStatus = "pending"
	VehicleAgentBindingActive    VehicleAgentBindingStatus = "active"
	VehicleAgentBindingSuspended VehicleAgentBindingStatus = "suspended"
	VehicleAgentBindingEnded     VehicleAgentBindingStatus = "ended"
)

type FlightControllerAttachment struct {
	Transport           string
	EndpointDescription string
	BaudRate            int
	SystemID            int
	ComponentID         int
	ObservedUID         string
}

// VehicleAgentBinding is the temporal attachment between an installed agent
// and a physical drone. It deliberately contains no backend-network state.
type VehicleAgentBinding struct {
	ID             string
	OrganizationID string
	VehicleAgentID string
	DroneID        string
	Status         VehicleAgentBindingStatus
	Attachment     FlightControllerAttachment
	BoundAt        time.Time
	VerifiedAt     *time.Time
	EndedAt        *time.Time
	EndReason      string
}

type CommunicationLinkStatus string

const (
	CommunicationLinkConnecting CommunicationLinkStatus = "connecting"
	CommunicationLinkHealthy    CommunicationLinkStatus = "healthy"
	CommunicationLinkDegraded   CommunicationLinkStatus = "degraded"
	CommunicationLinkStale      CommunicationLinkStatus = "stale"
	CommunicationLinkClosed     CommunicationLinkStatus = "closed"
	CommunicationLinkRejected   CommunicationLinkStatus = "rejected"
)

type CommunicationLink struct {
	ID                    string
	OrganizationID        string
	VehicleAgentBindingID string
	SessionInstanceID     string
	Transport             string
	Roles                 []string
	Status                CommunicationLinkStatus
	RemoteAddress         string
	CommandEligible       bool
	OpenedAt              time.Time
	FirstHeartbeatAt      *time.Time
	LastHeartbeatAt       *time.Time
	LatencyMS             *float64
	PacketLossEstimate    *float64
	RXBytesPerSecond      *float64
	TXBytesPerSecond      *float64
	ClosedAt              *time.Time
	CloseReason           string
}

type EnrollmentToken struct {
	ID                     string
	OrganizationID         string
	CreatedByUserID        string
	TokenHash              []byte
	ScopedDroneID          string
	CreatedAt              time.Time
	ExpiresAt              time.Time
	UsedAt                 *time.Time
	EnrollmentRequestID    string
	EnrolledVehicleAgentID string
	EnrolledBindingID      string
}

type NewDrone struct {
	OrganizationID      string
	Name                string
	FlightControllerUID string
	SerialNumber        string
	VehicleType         VehicleType
	Now                 time.Time
}

type DroneIdentity struct {
	FlightControllerUID string
	SerialNumber        string
	VehicleType         VehicleType
	Now                 time.Time
}

type NewVehicleAgent struct {
	OrganizationID      string
	InstallationID      string
	PublicKey           []byte
	AgentVersion        string
	ProtocolVersion     string
	DeviceProfile       DeviceProfile
	Capabilities        []string
	EnrollmentRequestID string
	Now                 time.Time
}

type NewVehicleAgentBinding struct {
	OrganizationID string
	VehicleAgentID string
	DroneID        string
	Status         VehicleAgentBindingStatus
	Attachment     FlightControllerAttachment
	Now            time.Time
}

type NewCommunicationLink struct {
	OrganizationID        string
	VehicleAgentBindingID string
	SessionInstanceID     string
	Transport             string
	Roles                 []string
	RemoteAddress         string
	CommandEligible       bool
	Now                   time.Time
}

type NewEnrollmentToken struct {
	OrganizationID  string
	CreatedByUserID string
	TokenHash       []byte
	ScopedDroneID   string
	Now             time.Time
	ExpiresAt       time.Time
}

type CommunicationLinkHealth struct {
	LatencyMS          *float64
	PacketLossEstimate *float64
	RXBytesPerSecond   *float64
	TXBytesPerSecond   *float64
}
