package services

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

// CommandService coordinates operator command requests and delivery to the active vehicle agent.
type CommandService struct {
	txManager repository.TxManager
	repos     repository.Repositories
}

// NewCommandService builds the command workflow service used by HTTP handlers and the agent channel.
func NewCommandService(txManager repository.TxManager, repos repository.Repositories) *CommandService {
	return &CommandService{txManager: txManager, repos: repos}
}

// IssueCommand creates an operator-requested command after selecting the active agent and applying policy checks.
func (s *CommandService) IssueCommand(ctx context.Context, input repository.RequestCommandInput, now time.Time) (models.CommandRequest, error) {
	var command models.CommandRequest
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		command, err = s.issueCommand(ctx, repos, input, now)
		return err
	})
	return command, err
}

// NextCommandForVehicleAgent leases the next deliverable command for the gRPC channel to push to an agent.
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

// ClaimCommandForVehicleAgent lets an agent explicitly claim a known command before executing it.
func (s *CommandService) ClaimCommandForVehicleAgent(ctx context.Context, agentID string, commandID string, now time.Time) (models.CommandRequest, error) {
	var command models.CommandRequest
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		command, err = s.claimCommandForVehicleAgent(ctx, repos, agentID, commandID, now)
		return err
	})
	return command, err
}

// UpdateCommandStatus records the vehicle agent's command execution status report.
func (s *CommandService) UpdateCommandStatus(ctx context.Context, input repository.UpdateCommandStatusInput, now time.Time) (models.CommandRequest, error) {
	var command models.CommandRequest
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		command, err = s.updateCommandStatus(ctx, repos, input, now)
		return err
	})
	return command, err
}

// GetCommandByID fetches a command for API reads such as command detail screens or status refreshes.
func (s *CommandService) GetCommandByID(ctx context.Context, commandID string) (models.CommandRequest, bool) {
	return s.repos.Commands.GetCommandByID(ctx, commandID)
}

// ListCommandsForDrone returns recent command history for a drone in fleet and mission views.
func (s *CommandService) ListCommandsForDrone(ctx context.Context, droneID string, limit int) ([]models.CommandRequest, error) {
	return s.repos.Commands.ListCommandsForDrone(ctx, droneID, limit)
}

// issueCommand performs the transactional command authorization and audit-event write.
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

// nextCommandForVehicleAgent chooses the oldest deliverable command and moves it into the sent lease state.
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

// claimCommandForVehicleAgent validates command ownership before leasing a specific command to an agent.
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

// updateCommandStatus applies an agent-reported state transition and writes the matching command event.
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

// supersedeOlderAckedCommands prevents stale acknowledged commands of the same type from remaining operationally active.
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

// oldestDeliverableCommandForVehicleAgent finds either a fresh authorized command or an expired sent lease to retry.
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

// commandRequestedBefore provides stable command ordering when multiple candidates share the same request time.
func commandRequestedBefore(a models.CommandRequest, b models.CommandRequest) bool {
	return a.RequestedAt.Before(b.RequestedAt) || (a.RequestedAt.Equal(b.RequestedAt) && a.ID < b.ID)
}
