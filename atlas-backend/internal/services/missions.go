package services

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

// MissionService coordinates mission definitions and mission execution state transitions.
type MissionService struct {
	txManager repository.TxManager
	repos     repository.Repositories
}

// NewMissionService builds the mission workflow service used by HTTP handlers and agent delivery.
func NewMissionService(txManager repository.TxManager, repos repository.Repositories) *MissionService {
	return &MissionService{txManager: txManager, repos: repos}
}

// CreateMission stores a planned route for a drone and validates whether it is ready to execute.
func (s *MissionService) CreateMission(ctx context.Context, input repository.CreateMissionInput, now time.Time) (models.Mission, error) {
	var mission models.Mission
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		mission, err = s.createMission(ctx, repos, input, now)
		return err
	})
	return mission, err
}

// ListMissionsForDrone returns saved mission plans for a drone so operators can review or launch them.
func (s *MissionService) ListMissionsForDrone(ctx context.Context, droneID string) ([]models.Mission, error) {
	return s.repos.Missions.ListMissionsForDrone(ctx, droneID)
}

// GetMissionByID fetches a mission definition for detail views and execution requests.
func (s *MissionService) GetMissionByID(ctx context.Context, missionID string) (models.Mission, bool) {
	return s.repos.Missions.GetMissionByID(ctx, missionID)
}

// RequestMissionUpload creates an execution record that asks the active vehicle agent to upload a mission to the vehicle.
func (s *MissionService) RequestMissionUpload(ctx context.Context, input repository.RequestMissionUploadInput, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.requestMissionUpload(ctx, repos, input, now)
		return err
	})
	return execution, err
}

// RecordMissionExecutionUploaded marks a mission execution as uploaded after the agent confirms vehicle acceptance.
func (s *MissionService) RecordMissionExecutionUploaded(ctx context.Context, executionID string, resultMessage string, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.recordMissionExecutionUploaded(ctx, repos, executionID, resultMessage, now)
		return err
	})
	return execution, err
}

// RequestMissionStart asks the vehicle agent to start the latest uploaded execution after safety preconditions pass.
func (s *MissionService) RequestMissionStart(ctx context.Context, input repository.RequestMissionStartInput, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.requestMissionStart(ctx, repos, input, now)
		return err
	})
	return execution, err
}

// RequestMissionAbort requests return-to-launch for the current abortable mission execution.
func (s *MissionService) RequestMissionAbort(ctx context.Context, input repository.RequestMissionAbortInput, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.requestMissionAbort(ctx, repos, input, now)
		return err
	})
	return execution, err
}

// ListMissionExecutions returns the execution history for a mission plan.
func (s *MissionService) ListMissionExecutions(ctx context.Context, missionID string) ([]models.MissionExecution, error) {
	return s.repos.MissionExecutions.ListMissionExecutions(ctx, missionID)
}

// ListMissionExecutionEvents returns the audit timeline for mission execution progress and failures.
func (s *MissionService) ListMissionExecutionEvents(ctx context.Context, missionID string) ([]models.MissionExecutionEvent, error) {
	return s.repos.MissionExecutions.ListMissionExecutionEvents(ctx, missionID)
}

// NextMissionExecutionForVehicleAgent leases the next mission action that an agent should perform.
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

// ClaimMissionExecutionForVehicleAgent lets an agent explicitly claim a specific mission execution action.
func (s *MissionService) ClaimMissionExecutionForVehicleAgent(ctx context.Context, agentID string, executionID string, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.claimMissionExecutionForVehicleAgent(ctx, repos, agentID, executionID, now)
		return err
	})
	return execution, err
}

// UpdateMissionExecutionStatus records mission progress or completion reported by the vehicle agent.
func (s *MissionService) UpdateMissionExecutionStatus(ctx context.Context, input repository.UpdateMissionExecutionStatusInput, now time.Time) (models.MissionExecution, error) {
	var execution models.MissionExecution
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		execution, err = s.updateMissionExecutionStatus(ctx, repos, input, now)
		return err
	})
	return execution, err
}

// createMission builds and persists a mission definition with normalized waypoint sequence numbers.
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

// requestMissionUpload opens a mission execution only when the mission is validated and no drone mission is active.
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

// recordMissionExecutionUploaded applies the backend-side state transition after upload confirmation from the agent.
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

// requestMissionStart moves an uploaded mission into start-requested state for agent delivery.
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

// requestMissionAbort moves the current mission execution into RTL-requested state for agent delivery.
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

// nextMissionExecutionForVehicleAgent chooses the next deliverable mission action and marks it sent to the agent.
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

// claimMissionExecutionForVehicleAgent validates mission execution ownership before marking it sent to an agent.
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

// updateMissionExecutionStatus applies progress, success, or failure states reported by the agent.
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

// validateMissionStartPreconditions checks live agent and telemetry conditions before a mission can start.
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

// latestMissionExecutionForUpdate loads the most relevant locked execution matching a mission workflow filter.
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
