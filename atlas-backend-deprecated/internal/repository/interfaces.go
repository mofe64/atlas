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
	VehicleAgents                VehicleAgentRepository
	Drones                       DroneRepository
	DroneVehicleAgentConnections DroneVehicleAgentConnectionRepository
	CommunicationLinks           CommunicationLinkRepository
	TelemetryFeeds               TelemetryFeedRepository
	Telemetry                    TelemetryRepository
	TelemetrySamples             TelemetrySampleRepository
	VehicleActions               VehicleActionRepository
	Missions                     MissionRepository
	MissionExecutions            MissionExecutionRepository
}

// DroneVehicleAgentConnectionRepository stores concrete backend-to-agent stream
// lifecycles. Services use it to make reconnects and disconnects auditable
// without coupling vehicle action routing directly to gRPC internals.
type DroneVehicleAgentConnectionRepository interface {
	GenerateDroneVehicleAgentConnectionID(ctx context.Context) (string, error)
	InsertDroneVehicleAgentConnection(ctx context.Context, connection models.DroneVehicleAgentConnection) error
	UpdateDroneVehicleAgentConnectionHeartbeat(ctx context.Context, connectionID string, now time.Time) (models.DroneVehicleAgentConnection, error)
	EndDroneVehicleAgentConnection(ctx context.Context, connectionID string, status models.DroneVehicleAgentConnectionStatus, endedReason string, now time.Time) (models.DroneVehicleAgentConnection, error)
	GetDroneVehicleAgentConnectionByID(ctx context.Context, connectionID string) (models.DroneVehicleAgentConnection, bool, error)
	LatestActiveDroneVehicleAgentConnectionForAgent(ctx context.Context, agentID string) (models.DroneVehicleAgentConnection, bool, error)
}

// CommunicationLinkRepository stores runtime connectivity paths and their
// health. A link can point at a vehicle-agent connection, a ground-bridge
// connection, or another future source, but it remains separate from telemetry
// samples, video feeds, and vehicle action requests.
type CommunicationLinkRepository interface {
	GenerateCommunicationLinkID(ctx context.Context) (string, error)
	InsertCommunicationLink(ctx context.Context, link models.CommunicationLink) error
	UpdateCommunicationLink(ctx context.Context, link models.CommunicationLink) error
	TouchCommunicationLinksForDroneVehicleAgentConnection(ctx context.Context, connectionID string, now time.Time) error
	EndCommunicationLinksForDroneVehicleAgentConnection(ctx context.Context, connectionID string, status models.CommunicationLinkStatus, endedReason string, now time.Time) error
	GetCommunicationLinkByID(ctx context.Context, linkID string) (models.CommunicationLink, bool, error)
	GetCommunicationLinkForDroneVehicleAgentConnection(ctx context.Context, connectionID string) (models.CommunicationLink, bool, error)
	GetOpenCommunicationLinkByLocalEndpoint(ctx context.Context, droneID string, linkType models.CommunicationLinkType, transport string, endpointDescription string) (models.CommunicationLink, bool, error)
	ListCommunicationLinksForDrone(ctx context.Context, droneID string) ([]models.CommunicationLink, error)
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

// TelemetryFeedRepository stores telemetry producer state for feed selection and diagnostics.
type TelemetryFeedRepository interface {
	GenerateTelemetryFeedID(ctx context.Context) (string, error)
	InsertTelemetryFeed(ctx context.Context, feed models.TelemetryFeed) error
	UpdateTelemetryFeed(ctx context.Context, feed models.TelemetryFeed) error
	GetTelemetryFeedByID(ctx context.Context, feedID string) (models.TelemetryFeed, bool, error)
	GetTelemetryFeedBySource(ctx context.Context, droneID string, sourceType models.TelemetrySourceType, sourceID string, communicationLinkID string) (models.TelemetryFeed, bool, error)
	ListTelemetryFeedsForDrone(ctx context.Context, droneID string) ([]models.TelemetryFeed, error)
}

// TelemetrySampleRepository stores historical telemetry evidence keyed by the feed that produced it.
type TelemetrySampleRepository interface {
	GenerateTelemetrySampleID(ctx context.Context) (string, error)
	InsertTelemetrySample(ctx context.Context, sample models.TelemetrySample) error
	LatestTelemetrySampleForFeed(ctx context.Context, feedID string) (models.TelemetrySample, bool, error)
}

// VehicleActionRepository defines vehicle action persistence operations shared by backend services.
type VehicleActionRepository interface {
	GenerateVehicleActionID(ctx context.Context) (string, error)
	InsertVehicleAction(ctx context.Context, action models.VehicleAction) error
	UpdateVehicleAction(ctx context.Context, action models.VehicleAction) error
	InsertVehicleActionEvent(ctx context.Context, action models.VehicleAction, eventType string, source string, message string, now time.Time) error
	InsertVehicleActionEventWithEvidence(ctx context.Context, action models.VehicleAction, eventType string, source string, message string, evidence map[string]any, now time.Time) error
	GetVehicleActionByID(ctx context.Context, vehicleActionID string) (models.VehicleAction, bool)
	GetVehicleActionByIDForUpdate(ctx context.Context, vehicleActionID string) (models.VehicleAction, bool, error)
	GetVehicleActionByIdempotencyKeyForUpdate(ctx context.Context, droneID string, requestedBy string, idempotencyKey string) (models.VehicleAction, bool, error)
	ListVehicleActionsForUpdate(ctx context.Context, filter VehicleActionFilter) ([]models.VehicleAction, error)
	ListVehicleActionsForDrone(ctx context.Context, droneID string, limit int) ([]models.VehicleAction, error)
	ListVehicleActionEvents(ctx context.Context, vehicleActionID string) ([]models.VehicleActionEvent, error)
}

// MissionRepository defines mission definition persistence operations shared by backend services.
type MissionRepository interface {
	GenerateMissionID(ctx context.Context) (string, error)
	GenerateMissionVersionID(ctx context.Context) (string, error)
	InsertMission(ctx context.Context, mission models.Mission) error
	InsertMissionWaypoint(ctx context.Context, missionID string, waypoint models.MissionWaypoint) error
	InsertMissionVersion(ctx context.Context, version models.MissionVersion) error
	InsertMissionVersionWaypoint(ctx context.Context, missionVersionID string, waypoint models.MissionWaypoint) error
	SetMissionCurrentVersion(ctx context.Context, missionID string, missionVersionID string, updatedAt time.Time) error
	ListMissionsForDrone(ctx context.Context, droneID string) ([]models.Mission, error)
	GetMissionByID(ctx context.Context, missionID string) (models.Mission, bool)
	GetMissionVersionByID(ctx context.Context, missionVersionID string) (models.MissionVersion, bool, error)
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
