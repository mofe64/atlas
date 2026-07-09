package services

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

// VehicleActionService coordinates operator-requested vehicle actions and delivery to the active vehicle agent.
type VehicleActionService struct {
	txManager repository.TxManager
	repos     repository.Repositories
}

// NewVehicleActionService builds the vehicle action workflow service used by HTTP handlers and the agent channel.
func NewVehicleActionService(txManager repository.TxManager, repos repository.Repositories) *VehicleActionService {
	return &VehicleActionService{txManager: txManager, repos: repos}
}

// RequestVehicleAction creates an operator-requested vehicle action after selecting the active agent and applying policy checks.
func (s *VehicleActionService) RequestVehicleAction(ctx context.Context, input repository.RequestVehicleActionInput, now time.Time) (models.VehicleAction, error) {
	var action models.VehicleAction
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		action, err = s.issueVehicleAction(ctx, repos, input, now)
		return err
	})
	return action, err
}

// NextVehicleActionForVehicleAgent leases the next deliverable vehicle action for the gRPC channel to push to an agent.
func (s *VehicleActionService) NextVehicleActionForVehicleAgent(ctx context.Context, agentID string, now time.Time) (models.VehicleAction, bool, error) {
	var action models.VehicleAction
	ok := false
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		action, ok, err = s.nextVehicleActionForVehicleAgent(ctx, repos, agentID, now)
		return err
	})
	return action, ok, err
}

// ClaimVehicleActionForVehicleAgent lets an agent explicitly claim a known vehicle action before executing it.
func (s *VehicleActionService) ClaimVehicleActionForVehicleAgent(ctx context.Context, agentID string, vehicleActionID string, now time.Time) (models.VehicleAction, error) {
	var action models.VehicleAction
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		action, err = s.claimVehicleActionForVehicleAgent(ctx, repos, agentID, vehicleActionID, now)
		return err
	})
	return action, err
}

// UpdateVehicleActionStatus records the vehicle agent's vehicle action execution status report.
func (s *VehicleActionService) UpdateVehicleActionStatus(ctx context.Context, input repository.UpdateVehicleActionStatusInput, now time.Time) (models.VehicleAction, error) {
	var action models.VehicleAction
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		action, err = s.updateVehicleActionStatus(ctx, repos, input, now)
		return err
	})
	return action, err
}

// GetVehicleActionByID fetches a vehicle action for API reads such as detail screens or status refreshes.
func (s *VehicleActionService) GetVehicleActionByID(ctx context.Context, vehicleActionID string) (models.VehicleAction, bool) {
	return s.repos.VehicleActions.GetVehicleActionByID(ctx, vehicleActionID)
}

// ListVehicleActionsForDrone returns recent vehicle action history for a drone in fleet and mission views.
func (s *VehicleActionService) ListVehicleActionsForDrone(ctx context.Context, droneID string, limit int) ([]models.VehicleAction, error) {
	return s.repos.VehicleActions.ListVehicleActionsForDrone(ctx, droneID, limit)
}

// ListVehicleActionEvents returns the durable lifecycle timeline for one vehicle action.
func (s *VehicleActionService) ListVehicleActionEvents(ctx context.Context, vehicleActionID string) ([]models.VehicleActionEvent, error) {
	return s.repos.VehicleActions.ListVehicleActionEvents(ctx, vehicleActionID)
}

// SweepTimedOutVehicleActions closes vehicle actions that are too old to safely
// continue. This prevents stale authorized or half-executed commands from being
// delivered later after the operational context has changed.
func (s *VehicleActionService) SweepTimedOutVehicleActions(ctx context.Context, now time.Time) (int, error) {
	changed := 0
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		changed, err = s.sweepTimedOutVehicleActions(ctx, repos, now)
		return err
	})
	return changed, err
}

// issueVehicleAction performs the transactional vehicle action authorization and audit-event write.
func (s *VehicleActionService) issueVehicleAction(ctx context.Context, repos repository.Repositories, input repository.RequestVehicleActionInput, now time.Time) (models.VehicleAction, error) {
	if !repos.Drones.DroneExists(ctx, input.DroneID) {
		return models.VehicleAction{}, repository.ErrDroneNotFound
	}

	if input.IdempotencyKey != "" {
		existing, ok, err := repos.VehicleActions.GetVehicleActionByIdempotencyKeyForUpdate(ctx, input.DroneID, input.RequestedBy, input.IdempotencyKey)
		if err != nil || ok {
			return existing, err
		}
	}

	// VehicleActions target the active agent for the drone; service code owns that
	// routing decision while postgres only selects the current active row.
	agent, ok, err := repos.VehicleAgents.GetActiveVehicleAgentForDrone(ctx, input.DroneID)
	if err != nil {
		return models.VehicleAction{}, err
	}
	if !ok {
		return models.VehicleAction{}, repository.ErrVehicleAgentNotFound
	}

	connection, hasConnection, err := repos.DroneVehicleAgentConnections.LatestActiveDroneVehicleAgentConnectionForAgent(ctx, agent.ID)
	if err != nil {
		return models.VehicleAction{}, err
	}
	var commandLink models.CommunicationLink
	hasCommandLink := false
	if hasConnection {
		commandLink, hasCommandLink, err = repos.CommunicationLinks.GetCommunicationLinkForDroneVehicleAgentConnection(ctx, connection.ID)
		if err != nil {
			return models.VehicleAction{}, err
		}
	}

	telemetry, _ := repos.Telemetry.GetTelemetryForDrone(ctx, input.DroneID)
	vehicleActionID, err := repos.VehicleActions.GenerateVehicleActionID(ctx)
	if err != nil {
		return models.VehicleAction{}, err
	}
	action := domain.AuthorizeVehicleAction(vehicleActionID, input.DroneID, input.Type, input.RequestedBy, agent, telemetry, now)
	action.IdempotencyKey = input.IdempotencyKey
	if action.State == models.VehicleActionStateAuthorized {
		if hasConnection {
			action.TargetDroneVehicleAgentConnectionID = connection.ID
		}
		if reason, failed := domain.CommandCommunicationLinkPolicyFailure(hasConnection, hasCommandLink, commandLink); failed {
			action.State = models.VehicleActionStateRejectedByPolicy
			action.PolicyReason = reason
		}
	}
	if action.State == models.VehicleActionStateAuthorized {
		action.AuthorizedAt = now
	}
	if err := repos.VehicleActions.InsertVehicleAction(ctx, action); err != nil {
		return models.VehicleAction{}, err
	}
	return action, repos.VehicleActions.InsertVehicleActionEvent(ctx, action, "requested", "backend", action.PolicyReason, now)
}

// nextVehicleActionForVehicleAgent chooses the oldest deliverable vehicle action and moves it into the sent lease state.
func (s *VehicleActionService) nextVehicleActionForVehicleAgent(ctx context.Context, repos repository.Repositories, agentID string, now time.Time) (models.VehicleAction, bool, error) {
	if !repos.VehicleAgents.VehicleAgentExists(ctx, agentID) {
		return models.VehicleAction{}, false, repository.ErrVehicleAgentNotFound
	}

	action, ok, err := s.oldestDeliverableVehicleActionForVehicleAgent(ctx, repos, agentID, now)
	if err != nil || !ok {
		return action, ok, err
	}

	action = domain.MarkVehicleActionSentToVehicleAgent(action, now)
	if err := repos.VehicleActions.UpdateVehicleAction(ctx, action); err != nil {
		return models.VehicleAction{}, false, err
	}
	if err := repos.VehicleActions.InsertVehicleActionEvent(ctx, action, "sent_to_vehicle_agent", "backend", "vehicle action sent to agent", now); err != nil {
		return models.VehicleAction{}, false, err
	}
	return action, true, nil
}

// claimVehicleActionForVehicleAgent validates vehicle action ownership before leasing a specific action to an agent.
func (s *VehicleActionService) claimVehicleActionForVehicleAgent(ctx context.Context, repos repository.Repositories, agentID string, vehicleActionID string, now time.Time) (models.VehicleAction, error) {
	if !repos.VehicleAgents.VehicleAgentExists(ctx, agentID) {
		return models.VehicleAction{}, repository.ErrVehicleAgentNotFound
	}

	action, ok, err := repos.VehicleActions.GetVehicleActionByIDForUpdate(ctx, vehicleActionID)
	if err != nil {
		return models.VehicleAction{}, err
	}
	if !ok {
		return models.VehicleAction{}, repository.ErrVehicleActionNotFound
	}
	if action.VehicleAgentID != agentID {
		return models.VehicleAction{}, repository.ErrVehicleActionNotAssignedToVehicleAgent
	}
	if !domain.VehicleActionDeliverable(action, now) {
		return models.VehicleAction{}, repository.ErrInvalidVehicleActionTransition
	}

	action = domain.MarkVehicleActionSentToVehicleAgent(action, now)
	if err := repos.VehicleActions.UpdateVehicleAction(ctx, action); err != nil {
		return models.VehicleAction{}, err
	}
	return action, repos.VehicleActions.InsertVehicleActionEvent(ctx, action, "sent_to_vehicle_agent", "backend", "vehicle action sent to agent", now)
}

// updateVehicleActionStatus applies an agent-reported state transition and writes the matching vehicle action event.
func (s *VehicleActionService) updateVehicleActionStatus(ctx context.Context, repos repository.Repositories, input repository.UpdateVehicleActionStatusInput, now time.Time) (models.VehicleAction, error) {
	action, ok, err := repos.VehicleActions.GetVehicleActionByIDForUpdate(ctx, input.VehicleActionID)
	if err != nil {
		return models.VehicleAction{}, err
	}
	if !ok {
		return models.VehicleAction{}, repository.ErrVehicleActionNotFound
	}
	if action.VehicleAgentID != input.VehicleAgentID {
		return models.VehicleAction{}, repository.ErrVehicleActionNotAssignedToVehicleAgent
	}
	if !domain.IsVehicleAgentReportedVehicleActionState(input.State) {
		return models.VehicleAction{}, repository.ErrInvalidVehicleActionState
	}
	if !domain.CanApplyVehicleAgentReportedVehicleActionState(action.State, input.State) {
		return models.VehicleAction{}, repository.ErrInvalidVehicleActionTransition
	}
	if input.State == models.VehicleActionStateVehicleAcked && input.AckCorrelationID == "" {
		return models.VehicleAction{}, repository.ErrVehicleActionAckCorrelationMismatch
	}
	if domain.VehicleActionAckCorrelationMismatch(action, input.AckCorrelationID) {
		return models.VehicleAction{}, repository.ErrVehicleActionAckCorrelationMismatch
	}

	var confirmationBaseline models.TelemetrySnapshot
	if input.State == models.VehicleActionStateVehicleAcked {
		confirmationBaseline, _ = repos.Telemetry.GetTelemetryForDrone(ctx, action.DroneID)
	}
	action = domain.ApplyVehicleAgentReportedVehicleActionState(action, input.State, input.ResultMessage, input.AckCorrelationID, input.RawAckCode, confirmationBaseline, now)
	if input.State == models.VehicleActionStateVehicleAcked {
		if err := s.supersedeOlderAckedVehicleActions(ctx, repos, action, now); err != nil {
			return models.VehicleAction{}, err
		}
	}

	if err := repos.VehicleActions.UpdateVehicleAction(ctx, action); err != nil {
		return models.VehicleAction{}, err
	}
	source := "agent"
	if len(input.Evidence) > 0 {
		source = "agent:mavlink_observer"
	}
	return action, repos.VehicleActions.InsertVehicleActionEventWithEvidence(ctx, action, string(input.State), source, input.ResultMessage, input.Evidence, now)
}

// supersedeOlderAckedVehicleActions prevents stale acknowledged vehicle actions of the same type from remaining operationally active.
func (s *VehicleActionService) supersedeOlderAckedVehicleActions(ctx context.Context, repos repository.Repositories, newer models.VehicleAction, now time.Time) error {
	actions, err := repos.VehicleActions.ListVehicleActionsForUpdate(ctx, repository.VehicleActionFilter{
		DroneID:         newer.DroneID,
		Type:            newer.Type,
		ExceptID:        newer.ID,
		States:          []models.VehicleActionState{models.VehicleActionStateVehicleAcked},
		RequestedBefore: newer.RequestedAt,
		Order:           repository.VehicleActionOrderRequestedAsc,
	})
	if err != nil {
		return err
	}
	for _, action := range actions {
		action = domain.MarkVehicleActionSupersededBy(action, newer, now)
		if err := repos.VehicleActions.UpdateVehicleAction(ctx, action); err != nil {
			return err
		}
		if err := repos.VehicleActions.InsertVehicleActionEvent(ctx, action, string(action.State), "backend", action.ResultMessage, now); err != nil {
			return err
		}
	}
	return nil
}

// oldestDeliverableVehicleActionForVehicleAgent finds either a fresh authorized vehicle action or an expired sent lease to retry.
func (s *VehicleActionService) oldestDeliverableVehicleActionForVehicleAgent(ctx context.Context, repos repository.Repositories, agentID string, now time.Time) (models.VehicleAction, bool, error) {
	candidates, err := repos.VehicleActions.ListVehicleActionsForUpdate(ctx, repository.VehicleActionFilter{
		VehicleAgentID: agentID,
		States:         []models.VehicleActionState{models.VehicleActionStateAuthorized},
		Order:          repository.VehicleActionOrderRequestedAsc,
		Limit:          1,
	})
	if err != nil {
		return models.VehicleAction{}, false, err
	}
	expiredLeasedVehicleActions, err := repos.VehicleActions.ListVehicleActionsForUpdate(ctx, repository.VehicleActionFilter{
		VehicleAgentID:       agentID,
		States:               []models.VehicleActionState{models.VehicleActionStateSentToVehicleAgent},
		LeaseUntilAtOrBefore: now,
		Order:                repository.VehicleActionOrderRequestedAsc,
		Limit:                1,
	})
	if err != nil {
		return models.VehicleAction{}, false, err
	}
	candidates = append(candidates, expiredLeasedVehicleActions...)

	var oldest models.VehicleAction
	for _, candidate := range candidates {
		if !domain.VehicleActionDeliverable(candidate, now) {
			continue
		}
		if oldest.ID == "" || vehicleActionRequestedBefore(candidate, oldest) {
			oldest = candidate
		}
	}
	if oldest.ID == "" {
		return models.VehicleAction{}, false, nil
	}
	return oldest, true, nil
}

// vehicleActionRequestedBefore provides stable vehicle action ordering when multiple candidates share the same request time.
func vehicleActionRequestedBefore(a models.VehicleAction, b models.VehicleAction) bool {
	return a.RequestedAt.Before(b.RequestedAt) || (a.RequestedAt.Equal(b.RequestedAt) && a.ID < b.ID)
}

func (s *VehicleActionService) sweepTimedOutVehicleActions(ctx context.Context, repos repository.Repositories, now time.Time) (int, error) {
	total := 0
	authorized, err := repos.VehicleActions.ListVehicleActionsForUpdate(ctx, repository.VehicleActionFilter{
		States:        []models.VehicleActionState{models.VehicleActionStateAuthorized},
		UpdatedBefore: now.Add(-models.VehicleActionAuthorizationTimeout),
		Order:         repository.VehicleActionOrderRequestedAsc,
	})
	if err != nil {
		return 0, err
	}
	if changed, err := s.timeoutVehicleActions(ctx, repos, authorized, "vehicle action was not delivered before authorization timeout", now); err != nil {
		return 0, err
	} else {
		total += changed
	}

	expiredLeases, err := repos.VehicleActions.ListVehicleActionsForUpdate(ctx, repository.VehicleActionFilter{
		States:               []models.VehicleActionState{models.VehicleActionStateSentToVehicleAgent},
		LeaseUntilAtOrBefore: now,
		Order:                repository.VehicleActionOrderRequestedAsc,
	})
	if err != nil {
		return 0, err
	}
	for _, action := range expiredLeases {
		if action.DeliveryAttempt < models.VehicleActionMaxDeliveryAttempts {
			continue
		}
		action = domain.MarkVehicleActionTimedOut(action, "vehicle action delivery lease expired after max attempts", now)
		if err := repos.VehicleActions.UpdateVehicleAction(ctx, action); err != nil {
			return 0, err
		}
		if err := repos.VehicleActions.InsertVehicleActionEvent(ctx, action, string(models.VehicleActionEventTimedOut), "backend", action.ResultMessage, now); err != nil {
			return 0, err
		}
		total++
	}

	inFlight, err := repos.VehicleActions.ListVehicleActionsForUpdate(ctx, repository.VehicleActionFilter{
		States: []models.VehicleActionState{
			models.VehicleActionStateVehicleAgentReceived,
			models.VehicleActionStateSentToVehicle,
		},
		UpdatedBefore: now.Add(-models.VehicleActionExecutionTimeout),
		Order:         repository.VehicleActionOrderRequestedAsc,
	})
	if err != nil {
		return 0, err
	}
	if changed, err := s.timeoutVehicleActions(ctx, repos, inFlight, "vehicle action execution timed out", now); err != nil {
		return 0, err
	} else {
		total += changed
	}

	acked, err := repos.VehicleActions.ListVehicleActionsForUpdate(ctx, repository.VehicleActionFilter{
		States:             []models.VehicleActionState{models.VehicleActionStateVehicleAcked},
		VehicleAckedBefore: now.Add(-models.VehicleActionObservationTimeout),
		Order:              repository.VehicleActionOrderRequestedAsc,
	})
	if err != nil {
		return 0, err
	}
	for _, action := range acked {
		action = domain.MarkVehicleActionAckedButNotObserved(action, now)
		if err := repos.VehicleActions.UpdateVehicleAction(ctx, action); err != nil {
			return 0, err
		}
		if err := repos.VehicleActions.InsertVehicleActionEvent(ctx, action, string(models.VehicleActionEventAckedButNotObserved), "backend", action.ResultMessage, now); err != nil {
			return 0, err
		}
		total++
	}

	return total, nil
}

func (s *VehicleActionService) timeoutVehicleActions(ctx context.Context, repos repository.Repositories, actions []models.VehicleAction, resultMessage string, now time.Time) (int, error) {
	for _, action := range actions {
		action = domain.MarkVehicleActionTimedOut(action, resultMessage, now)
		if err := repos.VehicleActions.UpdateVehicleAction(ctx, action); err != nil {
			return 0, err
		}
		if err := repos.VehicleActions.InsertVehicleActionEvent(ctx, action, string(models.VehicleActionEventTimedOut), "backend", action.ResultMessage, now); err != nil {
			return 0, err
		}
	}
	return len(actions), nil
}
