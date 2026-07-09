package repository

import (
	"errors"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

var (
	ErrVehicleAgentNotFound                      = errors.New("vehicle agent not found")
	ErrDroneNotFound                             = errors.New("drone not found")
	ErrVehicleActionNotFound                     = errors.New("vehicle action not found")
	ErrVehicleActionNotAssignedToVehicleAgent    = errors.New("vehicle action not assigned to vehicle agent")
	ErrInvalidVehicleActionState                 = errors.New("invalid vehicle action state")
	ErrInvalidVehicleActionTransition            = errors.New("invalid vehicle action transition")
	ErrVehicleActionAckCorrelationMismatch       = errors.New("vehicle action ack correlation mismatch")
	ErrMissionNotFound                           = errors.New("mission not found")
	ErrMissionVersionNotFound                    = errors.New("mission version not found")
	ErrMissionNotValidated                       = errors.New("mission is not validated")
	ErrMissionExecutionNotFound                  = errors.New("mission execution not found")
	ErrMissionExecutionNotAssignedToVehicleAgent = errors.New("mission execution not assigned to vehicle agent")
	ErrInvalidMissionExecutionState              = errors.New("invalid mission execution state")
	ErrDroneMissionActive                        = errors.New("drone has an active mission execution")
	ErrDroneVehicleAgentConnectionNotFound       = errors.New("drone vehicle agent connection not found")
	ErrCommunicationLinkNotFound                 = errors.New("communication link not found")
)

const (
	MinimumMissionBatteryPercent = domain.MinimumMissionBatteryPercent
	MinimumMissionAltitudeM      = domain.MinimumMissionAltitudeM
	MaximumMissionAltitudeM      = domain.MaximumMissionAltitudeM
	MaximumMissionWaypoints      = domain.MaximumMissionWaypoints
)

type RegisterVehicleAgentInput struct {
	VehicleAgentID      string
	DroneID             string
	DroneName           string
	VehicleAgentVersion string
}

type VehicleAgentHeartbeatInput struct {
	VehicleAgentID             string
	VehicleAgentVersion        string
	MAVLinkObserverDiagnostics map[string]any
	BackendChannelHealth       map[string]any
}

type RecordLocalTelemetryInput struct {
	DroneID             string
	SourceID            string
	Source              string
	Transport           string
	EndpointDescription string
	Roles               []models.CommunicationLinkRole
	Snapshot            models.TelemetrySnapshot
}

// OpenDroneVehicleAgentConnectionInput describes the stream metadata known
// when a vehicle agent opens its backend gRPC channel. LinkType is optional;
// when omitted, the service records the link as the standard Atlas
// VEHICLE_AGENT_GRPC network path.
type OpenDroneVehicleAgentConnectionInput struct {
	VehicleAgentID      string
	DroneID             string
	VehicleAgentVersion string
	RemoteAddress       string
	LinkType            models.CommunicationLinkType
}

// CloseDroneVehicleAgentConnectionInput lets the hub close every historical
// stream record while only marking the agent disconnected when the closing
// stream is still the active stream for that agent.
type CloseDroneVehicleAgentConnectionInput struct {
	EndedReason                  string
	MarkVehicleAgentDisconnected bool
}

type RequestVehicleActionInput struct {
	DroneID        string
	Type           models.VehicleActionType
	RequestedBy    string
	IdempotencyKey string
}

type VehicleActionOrder string

const (
	VehicleActionOrderRequestedAsc  VehicleActionOrder = "requested_asc"
	VehicleActionOrderRequestedDesc VehicleActionOrder = "requested_desc"
)

// VehicleActionFilter describes storage-level vehicle action predicates. Workflow concepts
// such as "deliverable" or "superseded" belong in services/domain code.
type VehicleActionFilter struct {
	ID                   string
	DroneID              string
	VehicleAgentID       string
	ExceptID             string
	Type                 models.VehicleActionType
	States               []models.VehicleActionState
	RequestedBefore      time.Time
	UpdatedBefore        time.Time
	VehicleAckedBefore   time.Time
	LeaseUntilAtOrBefore time.Time
	Order                VehicleActionOrder
	Limit                int
}

type CreateMissionInput struct {
	DroneID          string
	Name             string
	CreatedBy        string
	Waypoints        []MissionWaypointInput
	CompletionAction models.MissionCompletionAction
}

type MissionWaypointInput struct {
	Latitude          float64
	Longitude         float64
	RelativeAltitudeM float64
	SpeedMPS          *float64
	LoiterTimeS       *float64
}

type RequestMissionUploadInput struct {
	MissionID   string
	RequestedBy string
}

type RequestMissionStartInput struct {
	MissionID   string
	RequestedBy string
}

type RequestMissionAbortInput struct {
	MissionID   string
	RequestedBy string
}

type MissionExecutionOrder string

const (
	MissionExecutionOrderCreatedDesc MissionExecutionOrder = "created_desc"
	MissionExecutionOrderUpdatedAsc  MissionExecutionOrder = "updated_asc"
	MissionExecutionOrderUpdatedDesc MissionExecutionOrder = "updated_desc"
)

// MissionExecutionFilter describes simple persistence predicates. Business
// concepts such as "operational" or "abortable" belong in services/domain code.
type MissionExecutionFilter struct {
	ID             string
	MissionID      string
	DroneID        string
	VehicleAgentID string
	ExceptID       string
	States         []models.MissionExecutionState
	Order          MissionExecutionOrder
	Limit          int
}

type UpdateMissionExecutionStatusInput struct {
	VehicleAgentID     string
	ExecutionID        string
	State              models.MissionExecutionState
	ResultMessage      string
	CurrentMissionItem int
	TotalMissionItems  int
}

type UpdateVehicleActionStatusInput struct {
	VehicleAgentID   string
	VehicleActionID  string
	State            models.VehicleActionState
	ResultMessage    string
	AckCorrelationID string
	RawAckCode       string
	Evidence         map[string]any
}

type DroneSnapshot struct {
	ID                     string
	Name                   string
	VehicleAgentID         string
	Status                 models.VehicleAgentStatus
	LastSeenAt             time.Time
	LastHeartbeatAt        time.Time
	MAVLinkObserver        map[string]any
	BackendChannelHealth   map[string]any
	Telemetry              models.TelemetrySnapshot
	TelemetryState         models.TelemetryState
	CommandChannel         CommandChannelSnapshot
	LatestMissionExecution models.MissionExecution
}

type CommandChannelSnapshot struct {
	State              models.CommandChannelState
	ConnectedAt        time.Time
	LastDisconnectedAt time.Time
}

type MissionStartPreconditionError struct {
	Reason string
}

func (e MissionStartPreconditionError) Error() string {
	return e.Reason
}
