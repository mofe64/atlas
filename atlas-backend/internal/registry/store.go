package registry

import (
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
)

type Store interface {
	RegisterAgent(input RegisterAgentInput, now time.Time) domain.Agent
	RecordHeartbeat(input HeartbeatInput, now time.Time) (domain.Agent, error)
	ListDrones(now time.Time) []DroneSnapshot
	RecordTelemetry(snapshot domain.TelemetrySnapshot, now time.Time) (domain.TelemetrySnapshot, error)
	RecordCommandChannelConnected(agentID string, now time.Time) (domain.Agent, error)
	RecordCommandChannelDisconnected(agentID string, now time.Time) (domain.Agent, error)
	RequestCommand(input RequestCommandInput, now time.Time) (domain.OperatorCommand, error)
	NextCommandForAgent(agentID string, now time.Time) (domain.OperatorCommand, bool, error)
	ClaimCommandForAgent(agentID string, commandID string, now time.Time) (domain.OperatorCommand, error)
	UpdateCommandStatus(input UpdateCommandStatusInput, now time.Time) (domain.OperatorCommand, error)
	CommandByID(commandID string) (domain.OperatorCommand, bool)
	ListCommandsForDrone(droneID string, limit int) ([]domain.OperatorCommand, error)
	CreateMission(input CreateMissionInput, now time.Time) (domain.Mission, error)
	ListMissionsForDrone(droneID string) ([]domain.Mission, error)
	MissionByID(missionID string) (domain.Mission, bool)
	RequestMissionUpload(input RequestMissionUploadInput, now time.Time) (domain.MissionExecution, error)
	RecordMissionExecutionUploaded(executionID string, resultMessage string, now time.Time) (domain.MissionExecution, error)
	RequestMissionStart(input RequestMissionStartInput, now time.Time) (domain.MissionExecution, error)
	RequestMissionAbort(input RequestMissionAbortInput, now time.Time) (domain.MissionExecution, error)
	ListMissionExecutions(missionID string) ([]domain.MissionExecution, error)
	ListMissionExecutionEvents(missionID string) ([]domain.MissionExecutionEvent, error)
	NextMissionExecutionForAgent(agentID string, now time.Time) (domain.MissionExecution, bool, error)
	ClaimMissionExecutionForAgent(agentID string, executionID string, now time.Time) (domain.MissionExecution, error)
	UpdateMissionExecutionStatus(input UpdateMissionExecutionStatusInput, now time.Time) (domain.MissionExecution, error)
}
