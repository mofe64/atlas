package repository

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

// TxManager owns transaction lifecycles for service workflows.
//
// Implementations must pass repositories bound to one transaction into fn, commit
// when fn succeeds, and roll back when fn returns an error or panics. The
// callback repositories are scoped to that callback and must not be stored.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context, repos Repositories) error) error
}

// Repositories groups the persistence ports used by services.
//
// The same aggregate shape is used for root DB-backed repositories and for the
// transaction-bound repository set passed by TxManager.WithinTx.
type Repositories struct {
	VehicleAgents     VehicleAgentRepository
	Drones            DroneRepository
	Telemetry         TelemetryRepository
	Commands          CommandRepository
	Missions          MissionRepository
	MissionExecutions MissionExecutionRepository
}

// VehicleAgentRepository defines vehicle-agent persistence operations shared by backend services.
type VehicleAgentRepository interface {
	UpsertVehicleAgentRegistration(ctx context.Context, agent models.VehicleAgent) error
	RevokeActiveVehicleAgentsForDrone(ctx context.Context, droneID string, exceptAgentID string, now time.Time) error
	UpdateVehicleAgentHeartbeat(ctx context.Context, input VehicleAgentHeartbeatInput, now time.Time) (models.VehicleAgent, error)
	SetCommandChannelState(ctx context.Context, agentID string, state models.CommandChannelState, now time.Time) (models.VehicleAgent, error)
	GetVehicleAgentByID(ctx context.Context, agentID string) (models.VehicleAgent, bool, error)
	VehicleAgentExists(ctx context.Context, agentID string) bool
	GetActiveVehicleAgentForDrone(ctx context.Context, droneID string) (models.VehicleAgent, bool, error)
}

// DroneRepository defines drone read operations shared by backend services.
type DroneRepository interface {
	UpsertDroneRegistration(ctx context.Context, droneID string, droneName string, now time.Time) error
	DroneExists(ctx context.Context, droneID string) bool
	UpdateDroneLastSeen(ctx context.Context, droneID string, now time.Time) error
	ListDrones(ctx context.Context, now time.Time) []DroneSnapshot
}

// TelemetryRepository defines telemetry persistence operations shared by backend services.
type TelemetryRepository interface {
	UpsertLatestTelemetry(ctx context.Context, snapshot models.TelemetrySnapshot, now time.Time) (models.TelemetrySnapshot, error)
	GetTelemetryForDrone(ctx context.Context, droneID string) (models.TelemetrySnapshot, bool)
}

// CommandRepository defines command persistence operations shared by backend services.
type CommandRepository interface {
	GenerateCommandID(ctx context.Context) (string, error)
	InsertCommand(ctx context.Context, command models.CommandRequest) error
	UpdateCommand(ctx context.Context, command models.CommandRequest) error
	InsertCommandEvent(ctx context.Context, command models.CommandRequest, eventType string, source string, message string, now time.Time) error
	GetCommandByID(ctx context.Context, commandID string) (models.CommandRequest, bool)
	GetCommandByIDForUpdate(ctx context.Context, commandID string) (models.CommandRequest, bool, error)
	ListCommandsForUpdate(ctx context.Context, filter CommandFilter) ([]models.CommandRequest, error)
	ListCommandsForDrone(ctx context.Context, droneID string, limit int) ([]models.CommandRequest, error)
}

// MissionRepository defines mission definition persistence operations shared by backend services.
type MissionRepository interface {
	GenerateMissionID(ctx context.Context) (string, error)
	InsertMission(ctx context.Context, mission models.Mission) error
	InsertMissionWaypoint(ctx context.Context, missionID string, waypoint models.MissionWaypoint) error
	ListMissionsForDrone(ctx context.Context, droneID string) ([]models.Mission, error)
	GetMissionByID(ctx context.Context, missionID string) (models.Mission, bool)
}

// MissionExecutionRepository defines mission execution persistence operations shared by backend services.
type MissionExecutionRepository interface {
	GenerateMissionExecutionID(ctx context.Context) (string, error)
	InsertMissionExecution(ctx context.Context, execution models.MissionExecution) error
	UpdateMissionExecution(ctx context.Context, execution models.MissionExecution) error
	InsertMissionExecutionEvent(ctx context.Context, execution models.MissionExecution, eventType string, source string, message string, now time.Time) error
	LockMissionExecution(ctx context.Context, executionID string) (models.MissionExecution, bool, error)
	ListMissionExecutionsForUpdate(ctx context.Context, filter MissionExecutionFilter) ([]models.MissionExecution, error)
	ListMissionExecutions(ctx context.Context, missionID string) ([]models.MissionExecution, error)
	ListMissionExecutionEvents(ctx context.Context, missionID string) ([]models.MissionExecutionEvent, error)
}
