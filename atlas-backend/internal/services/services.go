// Package services contains Atlas application workflows.
//
// Services own transaction boundaries through repository.TxManager. Repositories
// passed into a TxManager callback are transaction-scoped; private helpers should
// accept that repository set when they need to participate in an existing
// workflow transaction.
package services

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

type Dependencies struct {
	TxManager    repository.TxManager
	Repositories repository.Repositories
}

type Services struct {
	VehicleAgents *VehicleAgentService
	Telemetry     *TelemetryService
	Commands      *CommandService
	Missions      *MissionService
	Fleet         *FleetService
}

func New(deps Dependencies) Services {
	return Services{
		VehicleAgents: NewVehicleAgentService(deps.TxManager),
		Telemetry:     NewTelemetryService(deps.TxManager),
		Commands:      NewCommandService(deps.TxManager, deps.Repositories),
		Missions:      NewMissionService(deps.TxManager, deps.Repositories),
		Fleet:         NewFleetService(deps.Repositories),
	}
}

type VehicleAgentService struct {
	txManager repository.TxManager
}

func NewVehicleAgentService(txManager repository.TxManager) *VehicleAgentService {
	return &VehicleAgentService{txManager: txManager}
}

func (s *VehicleAgentService) RegisterVehicleAgent(ctx context.Context, input repository.RegisterVehicleAgentInput, now time.Time) (models.VehicleAgent, error) {
	var agent models.VehicleAgent
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		agent, err = s.registerVehicleAgent(ctx, repos, input, now)
		return err
	})
	return agent, err
}

func (s *VehicleAgentService) RecordVehicleAgentHeartbeat(ctx context.Context, input repository.VehicleAgentHeartbeatInput, now time.Time) (models.VehicleAgent, error) {
	var agent models.VehicleAgent
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		agent, err = repos.VehicleAgents.UpdateVehicleAgentHeartbeat(ctx, input, now)
		if err != nil {
			return err
		}
		return repos.Drones.UpdateDroneLastSeen(ctx, agent.DroneID, now)
	})
	return agent, err
}

func (s *VehicleAgentService) registerVehicleAgent(ctx context.Context, repos repository.Repositories, input repository.RegisterVehicleAgentInput, now time.Time) (models.VehicleAgent, error) {
	if err := repos.Drones.UpsertDroneRegistration(ctx, input.DroneID, input.DroneName, now); err != nil {
		return models.VehicleAgent{}, err
	}

	// Only one active vehicle agent should be command-capable for a drone at a time.
	// Registration therefore revokes any previous active vehicle agent in the same tx.
	if err := repos.VehicleAgents.RevokeActiveVehicleAgentsForDrone(ctx, input.DroneID, input.VehicleAgentID, now); err != nil {
		return models.VehicleAgent{}, err
	}

	agent := models.VehicleAgent{
		ID:                  input.VehicleAgentID,
		DroneID:             input.DroneID,
		Version:             input.VehicleAgentVersion,
		VehicleAgentVersion: input.VehicleAgentVersion,
		IdentityStatus:      models.DeviceIdentityActive,
		RegisteredAt:        now,
		LastSeenAt:          now,
		CommandChannelState: models.CommandChannelDisconnected,
	}
	if err := repos.VehicleAgents.UpsertVehicleAgentRegistration(ctx, agent); err != nil {
		return models.VehicleAgent{}, err
	}

	registered, ok, err := repos.VehicleAgents.GetVehicleAgentByID(ctx, input.VehicleAgentID)
	if err != nil {
		return models.VehicleAgent{}, err
	}
	if !ok {
		return models.VehicleAgent{}, repository.ErrVehicleAgentNotFound
	}
	return registered, nil
}

func (s *VehicleAgentService) setCommandChannelState(ctx context.Context, agentID string, state models.CommandChannelState, now time.Time) (models.VehicleAgent, error) {
	var agent models.VehicleAgent
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		agent, err = repos.VehicleAgents.SetCommandChannelState(ctx, agentID, state, now)
		return err
	})
	return agent, err
}

func (s *VehicleAgentService) RecordCommandChannelConnected(ctx context.Context, agentID string, now time.Time) (models.VehicleAgent, error) {
	return s.setCommandChannelState(ctx, agentID, models.CommandChannelConnected, now)
}

func (s *VehicleAgentService) RecordCommandChannelDisconnected(ctx context.Context, agentID string, now time.Time) (models.VehicleAgent, error) {
	return s.setCommandChannelState(ctx, agentID, models.CommandChannelDisconnected, now)
}

type TelemetryService struct {
	txManager repository.TxManager
}

func NewTelemetryService(txManager repository.TxManager) *TelemetryService {
	return &TelemetryService{txManager: txManager}
}

func (s *TelemetryService) RecordTelemetry(ctx context.Context, snapshot models.TelemetrySnapshot, now time.Time) (models.TelemetrySnapshot, error) {
	var recorded models.TelemetrySnapshot
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		recorded, err = s.recordTelemetry(ctx, repos, snapshot, now)
		return err
	})
	return recorded, err
}

func (s *TelemetryService) recordTelemetry(ctx context.Context, repos repository.Repositories, snapshot models.TelemetrySnapshot, now time.Time) (models.TelemetrySnapshot, error) {
	agent, ok, err := repos.VehicleAgents.GetVehicleAgentByID(ctx, snapshot.VehicleAgentID)
	if err != nil {
		return models.TelemetrySnapshot{}, err
	}
	if !ok {
		return models.TelemetrySnapshot{}, repository.ErrVehicleAgentNotFound
	}

	snapshot.DroneID = agent.DroneID
	recorded, err := repos.Telemetry.UpsertLatestTelemetry(ctx, snapshot, now)
	if err != nil {
		return models.TelemetrySnapshot{}, err
	}
	if err := repos.Drones.UpdateDroneLastSeen(ctx, recorded.DroneID, now); err != nil {
		return models.TelemetrySnapshot{}, err
	}
	if err := s.confirmCommandsFromTelemetry(ctx, repos, recorded, now); err != nil {
		return models.TelemetrySnapshot{}, err
	}
	if err := s.settleMissionExecutionsFromTelemetry(ctx, repos, recorded, now); err != nil {
		return models.TelemetrySnapshot{}, err
	}
	return recorded, nil
}

func (s *TelemetryService) confirmCommandsFromTelemetry(ctx context.Context, repos repository.Repositories, snapshot models.TelemetrySnapshot, now time.Time) error {
	commands, err := repos.Commands.ListCommandsForUpdate(ctx, repository.CommandFilter{
		DroneID: snapshot.DroneID,
		States:  []models.CommandState{models.CommandStateVehicleAcked},
		Order:   repository.CommandOrderRequestedAsc,
	})
	if err != nil {
		return err
	}
	for _, command := range commands {
		if !domain.TelemetryConfirmsCommand(command, snapshot) {
			continue
		}
		command = domain.MarkCommandTelemetryConfirmed(command, now)
		if err := repos.Commands.UpdateCommand(ctx, command); err != nil {
			return err
		}
		if err := repos.Commands.InsertCommandEvent(ctx, command, string(command.State), "backend", command.ResultMessage, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *TelemetryService) settleMissionExecutionsFromTelemetry(ctx context.Context, repos repository.Repositories, snapshot models.TelemetrySnapshot, now time.Time) error {
	if snapshot.InAir {
		return nil
	}

	executions, err := repos.MissionExecutions.ListMissionExecutionsForUpdate(ctx, repository.MissionExecutionFilter{
		DroneID: snapshot.DroneID,
		States:  []models.MissionExecutionState{models.MissionExecutionStateRTLRequested},
		Order:   repository.MissionExecutionOrderUpdatedAsc,
	})
	if err != nil {
		return err
	}
	for _, execution := range executions {
		execution = domain.MarkMissionExecutionAbortedByTelemetry(execution, now)
		if err := repos.MissionExecutions.UpdateMissionExecution(ctx, execution); err != nil {
			return err
		}
		if err := repos.MissionExecutions.InsertMissionExecutionEvent(ctx, execution, "aborted", "backend", execution.ResultMessage, now); err != nil {
			return err
		}
	}
	return nil
}

type CommandService struct {
	txManager repository.TxManager
	repos     repository.Repositories
}

func NewCommandService(txManager repository.TxManager, repos repository.Repositories) *CommandService {
	return &CommandService{txManager: txManager, repos: repos}
}

func (s *CommandService) IssueCommand(ctx context.Context, input repository.RequestCommandInput, now time.Time) (models.CommandRequest, error) {
	var command models.CommandRequest
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		command, err = s.issueCommand(ctx, repos, input, now)
		return err
	})
	return command, err
}

func (s *CommandService) NextCommandForVehicleAgent(ctx context.Context, agentID string, now time.Time) (models.CommandRequest, bool, error) {
	var command models.CommandRequest
	ok := false
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		command, ok, err = s.nextCommandForVehicleAgent(ctx, repos, agentID, now)
		return err
	})
	return command, ok, err
}

func (s *CommandService) ClaimCommandForVehicleAgent(ctx context.Context, agentID string, commandID string, now time.Time) (models.CommandRequest, error) {
	var command models.CommandRequest
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		command, err = s.claimCommandForVehicleAgent(ctx, repos, agentID, commandID, now)
		return err
	})
	return command, err
}

func (s *CommandService) UpdateCommandStatus(ctx context.Context, input repository.UpdateCommandStatusInput, now time.Time) (models.CommandRequest, error) {
	var command models.CommandRequest
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		command, err = s.updateCommandStatus(ctx, repos, input, now)
		return err
	})
	return command, err
}

func (s *CommandService) GetCommandByID(ctx context.Context, commandID string) (models.CommandRequest, bool) {
	return s.repos.Commands.GetCommandByID(ctx, commandID)
}

func (s *CommandService) ListCommandsForDrone(ctx context.Context, droneID string, limit int) ([]models.CommandRequest, error) {
	return s.repos.Commands.ListCommandsForDrone(ctx, droneID, limit)
}

func (s *CommandService) issueCommand(ctx context.Context, repos repository.Repositories, input repository.RequestCommandInput, now time.Time) (models.CommandRequest, error) {
	if !repos.Drones.DroneExists(ctx, input.DroneID) {
		return models.CommandRequest{}, repository.ErrDroneNotFound
	}

	// Commands target the active agent for the drone; service code owns that
	// routing decision while postgres only selects the current active row.
	agent, ok, err := repos.VehicleAgents.GetActiveVehicleAgentForDrone(ctx, input.DroneID)
	if err != nil {
		return models.CommandRequest{}, err
	}
	if !ok {
		return models.CommandRequest{}, repository.ErrVehicleAgentNotFound
	}

	telemetry, _ := repos.Telemetry.GetTelemetryForDrone(ctx, input.DroneID)
	commandID, err := repos.Commands.GenerateCommandID(ctx)
	if err != nil {
		return models.CommandRequest{}, err
	}
	command := domain.AuthorizeCommand(commandID, input.DroneID, input.Type, input.RequestedBy, agent, telemetry, now)
	if err := repos.Commands.InsertCommand(ctx, command); err != nil {
		return models.CommandRequest{}, err
	}
	return command, repos.Commands.InsertCommandEvent(ctx, command, "requested", "backend", command.PolicyReason, now)
}

func (s *CommandService) nextCommandForVehicleAgent(ctx context.Context, repos repository.Repositories, agentID string, now time.Time) (models.CommandRequest, bool, error) {
	if !repos.VehicleAgents.VehicleAgentExists(ctx, agentID) {
		return models.CommandRequest{}, false, repository.ErrVehicleAgentNotFound
	}

	command, ok, err := s.oldestDeliverableCommandForVehicleAgent(ctx, repos, agentID, now)
	if err != nil || !ok {
		return command, ok, err
	}

	command = domain.MarkCommandSentToVehicleAgent(command, now)
	if err := repos.Commands.UpdateCommand(ctx, command); err != nil {
		return models.CommandRequest{}, false, err
	}
	if err := repos.Commands.InsertCommandEvent(ctx, command, "sent_to_vehicle_agent", "backend", "command sent to agent", now); err != nil {
		return models.CommandRequest{}, false, err
	}
	return command, true, nil
}

func (s *CommandService) claimCommandForVehicleAgent(ctx context.Context, repos repository.Repositories, agentID string, commandID string, now time.Time) (models.CommandRequest, error) {
	if !repos.VehicleAgents.VehicleAgentExists(ctx, agentID) {
		return models.CommandRequest{}, repository.ErrVehicleAgentNotFound
	}

	command, ok, err := repos.Commands.GetCommandByIDForUpdate(ctx, commandID)
	if err != nil {
		return models.CommandRequest{}, err
	}
	if !ok {
		return models.CommandRequest{}, repository.ErrCommandNotFound
	}
	if command.VehicleAgentID != agentID {
		return models.CommandRequest{}, repository.ErrCommandNotAssignedToVehicleAgent
	}
	if !domain.CommandDeliverable(command, now) {
		return models.CommandRequest{}, repository.ErrInvalidCommandTransition
	}

	command = domain.MarkCommandSentToVehicleAgent(command, now)
	if err := repos.Commands.UpdateCommand(ctx, command); err != nil {
		return models.CommandRequest{}, err
	}
	return command, repos.Commands.InsertCommandEvent(ctx, command, "sent_to_vehicle_agent", "backend", "command sent to agent", now)
}

func (s *CommandService) updateCommandStatus(ctx context.Context, repos repository.Repositories, input repository.UpdateCommandStatusInput, now time.Time) (models.CommandRequest, error) {
	command, ok, err := repos.Commands.GetCommandByIDForUpdate(ctx, input.CommandID)
	if err != nil {
		return models.CommandRequest{}, err
	}
	if !ok {
		return models.CommandRequest{}, repository.ErrCommandNotFound
	}
	if command.VehicleAgentID != input.VehicleAgentID {
		return models.CommandRequest{}, repository.ErrCommandNotAssignedToVehicleAgent
	}
	if !domain.IsVehicleAgentReportedCommandState(input.State) {
		return models.CommandRequest{}, repository.ErrInvalidCommandState
	}
	if !domain.CanApplyVehicleAgentReportedCommandState(command.State, input.State) {
		return models.CommandRequest{}, repository.ErrInvalidCommandTransition
	}

	var confirmationBaseline models.TelemetrySnapshot
	if input.State == models.CommandStateVehicleAcked {
		confirmationBaseline, _ = repos.Telemetry.GetTelemetryForDrone(ctx, command.DroneID)
	}
	command = domain.ApplyVehicleAgentReportedCommandState(command, input.State, input.ResultMessage, confirmationBaseline, now)
	if input.State == models.CommandStateVehicleAcked {
		if err := s.supersedeOlderAckedCommands(ctx, repos, command, now); err != nil {
			return models.CommandRequest{}, err
		}
	}

	if err := repos.Commands.UpdateCommand(ctx, command); err != nil {
		return models.CommandRequest{}, err
	}
	return command, repos.Commands.InsertCommandEvent(ctx, command, string(input.State), "agent", input.ResultMessage, now)
}

func (s *CommandService) supersedeOlderAckedCommands(ctx context.Context, repos repository.Repositories, newer models.CommandRequest, now time.Time) error {
	commands, err := repos.Commands.ListCommandsForUpdate(ctx, repository.CommandFilter{
		DroneID:         newer.DroneID,
		Type:            newer.Type,
		ExceptID:        newer.ID,
		States:          []models.CommandState{models.CommandStateVehicleAcked},
		RequestedBefore: newer.RequestedAt,
		Order:           repository.CommandOrderRequestedAsc,
	})
	if err != nil {
		return err
	}
	for _, command := range commands {
		command = domain.MarkCommandSupersededBy(command, newer, now)
		if err := repos.Commands.UpdateCommand(ctx, command); err != nil {
			return err
		}
		if err := repos.Commands.InsertCommandEvent(ctx, command, string(command.State), "backend", command.ResultMessage, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *CommandService) oldestDeliverableCommandForVehicleAgent(ctx context.Context, repos repository.Repositories, agentID string, now time.Time) (models.CommandRequest, bool, error) {
	candidates, err := repos.Commands.ListCommandsForUpdate(ctx, repository.CommandFilter{
		VehicleAgentID: agentID,
		States:         []models.CommandState{models.CommandStateAuthorized},
		Order:          repository.CommandOrderRequestedAsc,
		Limit:          1,
	})
	if err != nil {
		return models.CommandRequest{}, false, err
	}
	expiredLeasedCommands, err := repos.Commands.ListCommandsForUpdate(ctx, repository.CommandFilter{
		VehicleAgentID:       agentID,
		States:               []models.CommandState{models.CommandStateSentToVehicleAgent},
		LeaseUntilAtOrBefore: now,
		Order:                repository.CommandOrderRequestedAsc,
		Limit:                1,
	})
	if err != nil {
		return models.CommandRequest{}, false, err
	}
	candidates = append(candidates, expiredLeasedCommands...)

	var oldest models.CommandRequest
	for _, candidate := range candidates {
		if !domain.CommandDeliverable(candidate, now) {
			continue
		}
		if oldest.ID == "" || commandRequestedBefore(candidate, oldest) {
			oldest = candidate
		}
	}
	if oldest.ID == "" {
		return models.CommandRequest{}, false, nil
	}
	return oldest, true, nil
}

func commandRequestedBefore(a models.CommandRequest, b models.CommandRequest) bool {
	return a.RequestedAt.Before(b.RequestedAt) || (a.RequestedAt.Equal(b.RequestedAt) && a.ID < b.ID)
}

type MissionService struct {
	txManager repository.TxManager
	repos     repository.Repositories
}

func NewMissionService(txManager repository.TxManager, repos repository.Repositories) *MissionService {
	return &MissionService{txManager: txManager, repos: repos}
}

func (s *MissionService) CreateMission(ctx context.Context, input repository.CreateMissionInput, now time.Time) (models.Mission, error) {
	var mission models.Mission
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		mission, err = s.createMission(ctx, repos, input, now)
		return err
	})
	return mission, err
}

func (s *MissionService) ListMissionsForDrone(ctx context.Context, droneID string) ([]models.Mission, error) {
	return s.repos.Missions.ListMissionsForDrone(ctx, droneID)
}

func (s *MissionService) GetMissionByID(ctx context.Context, missionID string) (models.Mission, bool) {
	return s.repos.Missions.GetMissionByID(ctx, missionID)
}

func (s *MissionService) RequestMissionUpload(ctx context.Context, input repository.RequestMissionUploadInput, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.requestMissionUpload(ctx, repos, input, now)
		return err
	})
	return execution, err
}

func (s *MissionService) RecordMissionExecutionUploaded(ctx context.Context, executionID string, resultMessage string, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.recordMissionExecutionUploaded(ctx, repos, executionID, resultMessage, now)
		return err
	})
	return execution, err
}

func (s *MissionService) RequestMissionStart(ctx context.Context, input repository.RequestMissionStartInput, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.requestMissionStart(ctx, repos, input, now)
		return err
	})
	return execution, err
}

func (s *MissionService) RequestMissionAbort(ctx context.Context, input repository.RequestMissionAbortInput, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.requestMissionAbort(ctx, repos, input, now)
		return err
	})
	return execution, err
}

func (s *MissionService) ListMissionExecutions(ctx context.Context, missionID string) ([]models.MissionExecution, error) {
	return s.repos.MissionExecutions.ListMissionExecutions(ctx, missionID)
}

func (s *MissionService) ListMissionExecutionEvents(ctx context.Context, missionID string) ([]models.MissionExecutionEvent, error) {
	return s.repos.MissionExecutions.ListMissionExecutionEvents(ctx, missionID)
}

func (s *MissionService) NextMissionExecutionForVehicleAgent(ctx context.Context, agentID string, now time.Time) (models.MissionExecution, bool, error) {
	var execution models.MissionExecution
	ok := false
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, ok, err = s.nextMissionExecutionForVehicleAgent(ctx, repos, agentID, now)
		return err
	})
	return execution, ok, err
}

func (s *MissionService) ClaimMissionExecutionForVehicleAgent(ctx context.Context, agentID string, executionID string, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.claimMissionExecutionForVehicleAgent(ctx, repos, agentID, executionID, now)
		return err
	})
	return execution, err
}

func (s *MissionService) UpdateMissionExecutionStatus(ctx context.Context, input repository.UpdateMissionExecutionStatusInput, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.updateMissionExecutionStatus(ctx, repos, input, now)
		return err
	})
	return execution, err
}

func (s *MissionService) createMission(ctx context.Context, repos repository.Repositories, input repository.CreateMissionInput, now time.Time) (models.Mission, error) {
	if !repos.Drones.DroneExists(ctx, input.DroneID) {
		return models.Mission{}, repository.ErrDroneNotFound
	}

	waypoints := make([]models.MissionWaypoint, 0, len(input.Waypoints))
	for i, waypoint := range input.Waypoints {
		waypoints = append(waypoints, models.MissionWaypoint{
			Sequence:          i + 1,
			Latitude:          waypoint.Latitude,
			Longitude:         waypoint.Longitude,
			RelativeAltitudeM: waypoint.RelativeAltitudeM,
			SpeedMPS:          waypoint.SpeedMPS,
			LoiterTimeS:       waypoint.LoiterTimeS,
		})
	}

	missionID, err := repos.Missions.GenerateMissionID(ctx)
	if err != nil {
		return models.Mission{}, err
	}
	mission := domain.BuildMission(missionID, input.DroneID, input.Name, input.CreatedBy, waypoints, input.CompletionAction, now)
	agent, ok, err := repos.VehicleAgents.GetActiveVehicleAgentForDrone(ctx, mission.DroneID)
	// Preserve the previous API behavior: active-agent lookup failures are
	// reported as mission validation failures instead of transport errors.
	hasActiveAgent := err == nil && ok
	telemetry, _ := repos.Telemetry.GetTelemetryForDrone(ctx, mission.DroneID)
	mission.ValidationErrors = domain.ValidateMission(mission, domain.MissionValidationContext{
		Agent:          agent,
		HasActiveAgent: hasActiveAgent,
		Telemetry:      telemetry,
		Now:            now,
	})
	if len(mission.ValidationErrors) > 0 {
		mission.ValidationStatus = models.MissionValidationStatusRejected
	}

	if err := repos.Missions.InsertMission(ctx, mission); err != nil {
		return models.Mission{}, err
	}
	for _, waypoint := range mission.Waypoints {
		if err := repos.Missions.InsertMissionWaypoint(ctx, mission.ID, waypoint); err != nil {
			return models.Mission{}, err
		}
	}
	return mission, nil
}

func (s *MissionService) requestMissionUpload(ctx context.Context, repos repository.Repositories, input repository.RequestMissionUploadInput, now time.Time) (models.MissionExecution, error) {
	mission, ok := repos.Missions.GetMissionByID(ctx, input.MissionID)
	if !ok {
		return models.MissionExecution{}, repository.ErrMissionNotFound
	}
	if mission.ValidationStatus != models.MissionValidationStatusValidated {
		return models.MissionExecution{}, repository.ErrMissionNotValidated
	}

	// Upload requests bind to the active agent that can receive the mission payload.
	agent, ok, err := repos.VehicleAgents.GetActiveVehicleAgentForDrone(ctx, mission.DroneID)
	if err != nil {
		return models.MissionExecution{}, err
	}
	if !ok {
		return models.MissionExecution{}, repository.ErrVehicleAgentNotFound
	}
	if _, ok, err := s.latestMissionExecutionForUpdate(ctx, repos, repository.MissionExecutionFilter{
		DroneID: mission.DroneID,
		States:  domain.OperationalMissionExecutionStates(),
		Order:   repository.MissionExecutionOrderUpdatedDesc,
		Limit:   1,
	}); err != nil {
		return models.MissionExecution{}, err
	} else if ok {
		return models.MissionExecution{}, repository.ErrDroneMissionActive
	}

	executionID, err := repos.MissionExecutions.GenerateMissionExecutionID(ctx)
	if err != nil {
		return models.MissionExecution{}, err
	}
	execution := models.MissionExecution{
		ID:                executionID,
		MissionID:         mission.ID,
		DroneID:           mission.DroneID,
		VehicleAgentID:    agent.ID,
		RequestedBy:       input.RequestedBy,
		UploadRequestedBy: input.RequestedBy,
		State:             models.MissionExecutionStateUploadRequested,
		CreatedAt:         now,
		UpdatedAt:         now,
		UploadRequestedAt: now,
	}
	if err := repos.MissionExecutions.InsertMissionExecution(ctx, execution); err != nil {
		return models.MissionExecution{}, err
	}
	return execution, repos.MissionExecutions.InsertMissionExecutionEvent(ctx, execution, "upload_requested", "backend", "mission upload requested", now)
}

func (s *MissionService) recordMissionExecutionUploaded(ctx context.Context, repos repository.Repositories, executionID string, resultMessage string, now time.Time) (models.MissionExecution, error) {
	execution, ok, err := repos.MissionExecutions.LockMissionExecution(ctx, executionID)
	if err != nil {
		return models.MissionExecution{}, err
	}
	if !ok {
		return models.MissionExecution{}, repository.ErrMissionExecutionNotFound
	}
	if execution.State != models.MissionExecutionStateUploadRequested &&
		execution.State != models.MissionExecutionStateUploading {
		return models.MissionExecution{}, repository.ErrInvalidMissionExecutionState
	}

	execution = domain.MarkMissionExecutionUploaded(execution, resultMessage, now)
	if err := repos.MissionExecutions.UpdateMissionExecution(ctx, execution); err != nil {
		return models.MissionExecution{}, err
	}
	return execution, repos.MissionExecutions.InsertMissionExecutionEvent(ctx, execution, "uploaded_to_vehicle", "backend", resultMessage, now)
}

func (s *MissionService) requestMissionStart(ctx context.Context, repos repository.Repositories, input repository.RequestMissionStartInput, now time.Time) (models.MissionExecution, error) {
	mission, ok := repos.Missions.GetMissionByID(ctx, input.MissionID)
	if !ok {
		return models.MissionExecution{}, repository.ErrMissionNotFound
	}

	execution, ok, err := s.latestMissionExecutionForUpdate(ctx, repos, repository.MissionExecutionFilter{
		MissionID: input.MissionID,
		States:    []models.MissionExecutionState{models.MissionExecutionStateUploadedToVehicle},
		Order:     repository.MissionExecutionOrderCreatedDesc,
		Limit:     1,
	})
	if err != nil {
		return models.MissionExecution{}, err
	}
	if !ok {
		return models.MissionExecution{}, repository.ErrInvalidMissionExecutionState
	}
	if _, ok, err := s.latestMissionExecutionForUpdate(ctx, repos, repository.MissionExecutionFilter{
		DroneID:  mission.DroneID,
		ExceptID: execution.ID,
		States:   domain.OperationalMissionExecutionStates(),
		Order:    repository.MissionExecutionOrderUpdatedDesc,
		Limit:    1,
	}); err != nil {
		return models.MissionExecution{}, err
	} else if ok {
		return models.MissionExecution{}, repository.ErrDroneMissionActive
	}
	if err := s.validateMissionStartPreconditions(ctx, repos, mission, now); err != nil {
		return models.MissionExecution{}, err
	}

	execution = domain.MarkMissionStartRequested(execution, input.RequestedBy, now)
	if err := repos.MissionExecutions.UpdateMissionExecution(ctx, execution); err != nil {
		return models.MissionExecution{}, err
	}
	return execution, repos.MissionExecutions.InsertMissionExecutionEvent(ctx, execution, "start_requested", "backend", "mission start requested", now)
}

func (s *MissionService) requestMissionAbort(ctx context.Context, repos repository.Repositories, input repository.RequestMissionAbortInput, now time.Time) (models.MissionExecution, error) {
	if _, ok := repos.Missions.GetMissionByID(ctx, input.MissionID); !ok {
		return models.MissionExecution{}, repository.ErrMissionNotFound
	}

	execution, ok, err := s.latestMissionExecutionForUpdate(ctx, repos, repository.MissionExecutionFilter{
		MissionID: input.MissionID,
		States:    domain.AbortableMissionExecutionStates(),
		Order:     repository.MissionExecutionOrderUpdatedDesc,
		Limit:     1,
	})
	if err != nil {
		return models.MissionExecution{}, err
	}
	if !ok {
		return models.MissionExecution{}, repository.ErrInvalidMissionExecutionState
	}

	execution = domain.MarkMissionAbortRequested(execution, input.RequestedBy, now)
	if err := repos.MissionExecutions.UpdateMissionExecution(ctx, execution); err != nil {
		return models.MissionExecution{}, err
	}
	return execution, repos.MissionExecutions.InsertMissionExecutionEvent(ctx, execution, "rtl_requested", "backend", execution.ResultMessage, now)
}

func (s *MissionService) nextMissionExecutionForVehicleAgent(ctx context.Context, repos repository.Repositories, agentID string, now time.Time) (models.MissionExecution, bool, error) {
	if !repos.VehicleAgents.VehicleAgentExists(ctx, agentID) {
		return models.MissionExecution{}, false, repository.ErrVehicleAgentNotFound
	}
	candidates, err := repos.MissionExecutions.ListMissionExecutionsForUpdate(ctx, repository.MissionExecutionFilter{
		VehicleAgentID: agentID,
		States:         domain.DeliverableMissionExecutionStates(),
		Order:          repository.MissionExecutionOrderUpdatedAsc,
	})
	if err != nil {
		return models.MissionExecution{}, false, err
	}

	var execution models.MissionExecution
	for _, candidate := range candidates {
		if domain.MissionExecutionDeliverable(candidate, now) {
			execution = candidate
			break
		}
	}
	if execution.ID == "" {
		return models.MissionExecution{}, false, nil
	}

	execution = domain.MarkMissionExecutionSent(execution, now)
	if err := repos.MissionExecutions.UpdateMissionExecution(ctx, execution); err != nil {
		return models.MissionExecution{}, false, err
	}
	if err := repos.MissionExecutions.InsertMissionExecutionEvent(ctx, execution, "sent_to_vehicle_agent", "backend", "mission execution sent to agent", now); err != nil {
		return models.MissionExecution{}, false, err
	}
	return execution, true, nil
}

func (s *MissionService) claimMissionExecutionForVehicleAgent(ctx context.Context, repos repository.Repositories, agentID string, executionID string, now time.Time) (models.MissionExecution, error) {
	if !repos.VehicleAgents.VehicleAgentExists(ctx, agentID) {
		return models.MissionExecution{}, repository.ErrVehicleAgentNotFound
	}
	execution, ok, err := repos.MissionExecutions.LockMissionExecution(ctx, executionID)
	if err != nil {
		return models.MissionExecution{}, err
	}
	if !ok {
		return models.MissionExecution{}, repository.ErrMissionExecutionNotFound
	}
	if execution.VehicleAgentID != agentID {
		return models.MissionExecution{}, repository.ErrMissionExecutionNotAssignedToVehicleAgent
	}
	if !domain.MissionExecutionDeliverable(execution, now) {
		return models.MissionExecution{}, repository.ErrInvalidMissionExecutionState
	}

	execution = domain.MarkMissionExecutionSent(execution, now)
	if err := repos.MissionExecutions.UpdateMissionExecution(ctx, execution); err != nil {
		return models.MissionExecution{}, err
	}
	return execution, repos.MissionExecutions.InsertMissionExecutionEvent(ctx, execution, "sent_to_vehicle_agent", "backend", "mission execution sent to agent", now)
}

func (s *MissionService) updateMissionExecutionStatus(ctx context.Context, repos repository.Repositories, input repository.UpdateMissionExecutionStatusInput, now time.Time) (models.MissionExecution, error) {
	execution, ok, err := repos.MissionExecutions.LockMissionExecution(ctx, input.ExecutionID)
	if err != nil {
		return models.MissionExecution{}, err
	}
	if !ok {
		return models.MissionExecution{}, repository.ErrMissionExecutionNotFound
	}
	if execution.VehicleAgentID != input.VehicleAgentID {
		return models.MissionExecution{}, repository.ErrMissionExecutionNotAssignedToVehicleAgent
	}
	if !domain.CanApplyAgentReportedMissionExecutionState(execution.State, input.State) {
		return models.MissionExecution{}, repository.ErrInvalidMissionExecutionState
	}

	execution = domain.ApplyAgentReportedMissionExecutionState(execution, input.State, input.ResultMessage, input.CurrentMissionItem, input.TotalMissionItems, now)
	if err := repos.MissionExecutions.UpdateMissionExecution(ctx, execution); err != nil {
		return models.MissionExecution{}, err
	}
	return execution, repos.MissionExecutions.InsertMissionExecutionEvent(ctx, execution, domain.MissionExecutionEventType(input.State, input.CurrentMissionItem, input.TotalMissionItems), "agent", input.ResultMessage, now)
}

func (s *MissionService) validateMissionStartPreconditions(ctx context.Context, repos repository.Repositories, mission models.Mission, now time.Time) error {
	agent, ok, err := repos.VehicleAgents.GetActiveVehicleAgentForDrone(ctx, mission.DroneID)
	if err != nil {
		return err
	}
	telemetry, _ := repos.Telemetry.GetTelemetryForDrone(ctx, mission.DroneID)
	if reason, failed := domain.MissionStartPreconditionFailure(agent, ok, telemetry, now); failed {
		return repository.MissionStartPreconditionError{Reason: reason}
	}
	return nil
}

func (s *MissionService) latestMissionExecutionForUpdate(ctx context.Context, repos repository.Repositories, filter repository.MissionExecutionFilter) (models.MissionExecution, bool, error) {
	executions, err := repos.MissionExecutions.ListMissionExecutionsForUpdate(ctx, filter)
	if err != nil {
		return models.MissionExecution{}, false, err
	}
	if len(executions) == 0 {
		return models.MissionExecution{}, false, nil
	}
	return executions[0], true, nil
}

type FleetService struct {
	repos repository.Repositories
}

type FleetDrone struct {
	Snapshot repository.DroneSnapshot
	Commands []models.CommandRequest
}

type MissionDetail struct {
	Mission    models.Mission
	Executions []models.MissionExecution
}

func NewFleetService(repos repository.Repositories) *FleetService {
	return &FleetService{repos: repos}
}

func (s *FleetService) ListDrones(ctx context.Context, now time.Time, commandLimit int) []FleetDrone {
	snapshots := s.repos.Drones.ListDrones(ctx, now)
	drones := make([]FleetDrone, 0, len(snapshots))
	for _, snapshot := range snapshots {
		commands, err := s.repos.Commands.ListCommandsForDrone(ctx, snapshot.ID, commandLimit)
		if err != nil {
			commands = nil
		}
		drones = append(drones, FleetDrone{Snapshot: snapshot, Commands: commands})
	}
	return drones
}

func (s *FleetService) MissionDetail(ctx context.Context, missionID string) (MissionDetail, error) {
	mission, ok := s.repos.Missions.GetMissionByID(ctx, missionID)
	if !ok {
		return MissionDetail{}, repository.ErrMissionNotFound
	}

	executions, err := s.repos.MissionExecutions.ListMissionExecutions(ctx, missionID)
	if err != nil {
		return MissionDetail{}, err
	}

	return MissionDetail{
		Mission:    mission,
		Executions: executions,
	}, nil
}
