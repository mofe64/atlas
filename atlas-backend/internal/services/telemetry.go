package services

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

// TelemetryService records latest vehicle telemetry and settles workflows that telemetry can confirm.
type TelemetryService struct {
	txManager repository.TxManager
}

// NewTelemetryService builds the workflow service used by agent telemetry ingestion.
func NewTelemetryService(txManager repository.TxManager) *TelemetryService {
	return &TelemetryService{txManager: txManager}
}

// RecordTelemetry stores the latest telemetry snapshot and applies any command or mission state changes it proves.
func (s *TelemetryService) RecordTelemetry(ctx context.Context, snapshot models.TelemetrySnapshot, now time.Time) (models.TelemetrySnapshot, error) {
	var recorded models.TelemetrySnapshot
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		recorded, err = s.recordTelemetry(ctx, repos, snapshot, now)
		return err
	})
	return recorded, err
}

// recordTelemetry enriches agent telemetry with drone identity and runs telemetry-driven workflow updates in one transaction.
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

// confirmCommandsFromTelemetry closes commands that were acknowledged by the vehicle and later proven by observed state.
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

// settleMissionExecutionsFromTelemetry marks RTL abort requests complete once telemetry shows the aircraft is no longer airborne.
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
