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
	ErrCommandNotFound                           = errors.New("command not found")
	ErrCommandNotAssignedToVehicleAgent          = errors.New("command not assigned to vehicle agent")
	ErrInvalidCommandState                       = errors.New("invalid command state")
	ErrInvalidCommandTransition                  = errors.New("invalid command transition")
	ErrMissionNotFound                           = errors.New("mission not found")
	ErrMissionNotValidated                       = errors.New("mission is not validated")
	ErrMissionExecutionNotFound                  = errors.New("mission execution not found")
	ErrMissionExecutionNotAssignedToVehicleAgent = errors.New("mission execution not assigned to vehicle agent")
	ErrInvalidMissionExecutionState              = errors.New("invalid mission execution state")
	ErrDroneMissionActive                        = errors.New("drone has an active mission execution")
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
	VehicleAgentID      string
	VehicleAgentVersion string
}

type RequestCommandInput struct {
	DroneID     string
	Type        models.CommandType
	RequestedBy string
}

type CommandOrder string

const (
	CommandOrderRequestedAsc  CommandOrder = "requested_asc"
	CommandOrderRequestedDesc CommandOrder = "requested_desc"
)

// CommandFilter describes storage-level command predicates. Workflow concepts
// such as "deliverable" or "superseded" belong in services/domain code.
type CommandFilter struct {
	ID                   string
	DroneID              string
	VehicleAgentID       string
	ExceptID             string
	Type                 models.CommandType
	States               []models.CommandState
	RequestedBefore      time.Time
	LeaseUntilAtOrBefore time.Time
	Order                CommandOrder
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

type UpdateCommandStatusInput struct {
	VehicleAgentID string
	CommandID      string
	State          models.CommandState
	ResultMessage  string
}

type DroneSnapshot struct {
	ID                     string
	Name                   string
	VehicleAgentID         string
	Status                 models.VehicleAgentStatus
	LastSeenAt             time.Time
	LastHeartbeatAt        time.Time
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
