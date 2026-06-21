package registry

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
)

var (
	ErrAgentNotFound                = errors.New("agent not found")
	ErrDroneNotFound                = errors.New("drone not found")
	ErrCommandNotFound              = errors.New("command not found")
	ErrCommandNotAssigned           = errors.New("command not assigned to agent")
	ErrInvalidCommandState          = errors.New("invalid command state")
	ErrInvalidCommandTransition     = errors.New("invalid command transition")
	ErrMissionNotFound              = errors.New("mission not found")
	ErrMissionNotValidated          = errors.New("mission is not validated")
	ErrMissionExecutionNotFound     = errors.New("mission execution not found")
	ErrMissionExecutionNotAssigned  = errors.New("mission execution not assigned to agent")
	ErrInvalidMissionExecutionState = errors.New("invalid mission execution state")
	ErrDroneMissionActive           = errors.New("drone has an active mission execution")
)

const (
	MinimumMissionBatteryPercent float64 = 20
	MinimumMissionAltitudeM      float64 = 1
	MaximumMissionAltitudeM      float64 = 120
	MaximumMissionWaypoints              = 100
)

type RegisterAgentInput struct {
	AgentID      string
	DroneID      string
	DroneName    string
	AgentVersion string
}

type HeartbeatInput struct {
	AgentID      string
	AgentVersion string
}

type RequestCommandInput struct {
	DroneID     string
	Type        domain.CommandType
	RequestedBy string
}

type CreateMissionInput struct {
	DroneID          string
	Name             string
	CreatedBy        string
	Waypoints        []MissionWaypointInput
	CompletionAction domain.MissionCompletionAction
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

type UpdateMissionExecutionStatusInput struct {
	AgentID            string
	ExecutionID        string
	State              domain.MissionExecutionState
	ResultMessage      string
	CurrentMissionItem int
	TotalMissionItems  int
}

type UpdateCommandStatusInput struct {
	AgentID       string
	CommandID     string
	State         domain.CommandState
	ResultMessage string
}

type DroneSnapshot struct {
	ID                     string
	Name                   string
	AgentID                string
	Status                 domain.AgentStatus
	LastSeenAt             time.Time
	LastHeartbeatAt        time.Time
	Telemetry              domain.TelemetrySnapshot
	TelemetryState         domain.TelemetryState
	CommandChannel         CommandChannelSnapshot
	LatestMissionExecution domain.MissionExecution
}

type CommandChannelSnapshot struct {
	State              domain.CommandChannelState
	ConnectedAt        time.Time
	LastDisconnectedAt time.Time
}

type MemoryRegistry struct {
	mu               sync.RWMutex
	drones           map[string]domain.Drone
	agents           map[string]domain.Agent
	telemetry        map[string]domain.TelemetrySnapshot
	commands         map[string]domain.OperatorCommand
	missions         map[string]domain.Mission
	executions       map[string]domain.MissionExecution
	missionEvents    map[string]domain.MissionExecutionEvent
	nextCommandSeq   int64
	nextMissionSeq   int64
	nextExecutionSeq int64
	nextEventSeq     int64
}

type MissionStartPreconditionError struct {
	Reason string
}

func (e MissionStartPreconditionError) Error() string {
	return e.Reason
}

func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{
		drones:        make(map[string]domain.Drone),
		agents:        make(map[string]domain.Agent),
		telemetry:     make(map[string]domain.TelemetrySnapshot),
		commands:      make(map[string]domain.OperatorCommand),
		missions:      make(map[string]domain.Mission),
		executions:    make(map[string]domain.MissionExecution),
		missionEvents: make(map[string]domain.MissionExecutionEvent),
	}
}

func (r *MemoryRegistry) RegisterAgent(input RegisterAgentInput, now time.Time) domain.Agent {
	r.mu.Lock()
	defer r.mu.Unlock()

	drone := r.drones[input.DroneID]
	drone.ID = input.DroneID
	drone.Name = input.DroneName
	drone.LastSeenAt = now
	r.drones[input.DroneID] = drone

	agent := r.agents[input.AgentID]
	agent.ID = input.AgentID
	agent.DroneID = input.DroneID
	agent.Version = input.AgentVersion
	if agent.CommandChannelState == "" {
		agent.CommandChannelState = domain.CommandChannelDisconnected
	}
	if agent.RegisteredAt.IsZero() {
		agent.RegisteredAt = now
	}
	r.agents[input.AgentID] = agent

	return agent
}

func (r *MemoryRegistry) RecordHeartbeat(input HeartbeatInput, now time.Time) (domain.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent, ok := r.agents[input.AgentID]
	if !ok {
		return domain.Agent{}, ErrAgentNotFound
	}

	agent.Version = input.AgentVersion
	agent.LastHeartbeatAt = now
	r.agents[input.AgentID] = agent

	drone := r.drones[agent.DroneID]
	drone.LastSeenAt = now
	r.drones[agent.DroneID] = drone

	return agent, nil
}

func (r *MemoryRegistry) ListDrones(now time.Time) []DroneSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	snapshots := make([]DroneSnapshot, 0, len(r.drones))
	for _, drone := range r.drones {
		agent := r.agentForDroneLocked(drone.ID)
		telemetry := r.telemetry[drone.ID]
		snapshots = append(snapshots, DroneSnapshot{
			ID:                     drone.ID,
			Name:                   drone.Name,
			AgentID:                agent.ID,
			Status:                 domain.StatusFromHeartbeat(agent.LastHeartbeatAt, now),
			LastSeenAt:             drone.LastSeenAt,
			LastHeartbeatAt:        agent.LastHeartbeatAt,
			Telemetry:              telemetry,
			TelemetryState:         domain.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now),
			CommandChannel:         commandChannelSnapshot(agent),
			LatestMissionExecution: r.latestMissionExecutionForDroneLocked(drone.ID),
		})
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].ID < snapshots[j].ID
	})

	return snapshots
}

func (r *MemoryRegistry) RecordTelemetry(snapshot domain.TelemetrySnapshot, now time.Time) (domain.TelemetrySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent, ok := r.agents[snapshot.AgentID]
	if !ok {
		return domain.TelemetrySnapshot{}, ErrAgentNotFound
	}

	snapshot.DroneID = agent.DroneID
	snapshot.ReceivedAt = now
	r.telemetry[agent.DroneID] = snapshot

	drone := r.drones[agent.DroneID]
	drone.LastSeenAt = now
	r.drones[agent.DroneID] = drone

	r.confirmCommandsFromTelemetryLocked(agent.DroneID, snapshot, now)

	return snapshot, nil
}

func (r *MemoryRegistry) RecordCommandChannelConnected(agentID string, now time.Time) (domain.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent, ok := r.agents[agentID]
	if !ok {
		return domain.Agent{}, ErrAgentNotFound
	}

	agent.CommandChannelState = domain.CommandChannelConnected
	agent.CommandChannelConnectedAt = now
	r.agents[agentID] = agent

	return agent, nil
}

func (r *MemoryRegistry) RecordCommandChannelDisconnected(agentID string, now time.Time) (domain.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent, ok := r.agents[agentID]
	if !ok {
		return domain.Agent{}, ErrAgentNotFound
	}

	agent.CommandChannelState = domain.CommandChannelDisconnected
	agent.CommandChannelLastDisconnectedAt = now
	r.agents[agentID] = agent

	return agent, nil
}

func (r *MemoryRegistry) RequestCommand(input RequestCommandInput, now time.Time) (domain.OperatorCommand, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.drones[input.DroneID]; !ok {
		return domain.OperatorCommand{}, ErrDroneNotFound
	}

	agent := r.agentForDroneLocked(input.DroneID)
	if agent.ID == "" {
		return domain.OperatorCommand{}, ErrAgentNotFound
	}

	telemetry := r.telemetry[input.DroneID]
	agentStatus := domain.StatusFromHeartbeat(agent.LastHeartbeatAt, now)
	telemetryState := domain.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now)

	r.nextCommandSeq++
	command := domain.OperatorCommand{
		ID:             fmt.Sprintf("cmd-%06d", r.nextCommandSeq),
		DroneID:        input.DroneID,
		AgentID:        agent.ID,
		Type:           input.Type,
		State:          domain.CommandStateAuthorized,
		RequestedBy:    input.RequestedBy,
		RequestedAt:    now,
		UpdatedAt:      now,
		TelemetryState: telemetryState,
		AgentStatus:    agentStatus,
	}

	if agentStatus != domain.AgentStatusOnline {
		command.State = domain.CommandStateRejectedByPolicy
		command.PolicyReason = "agent must be online"
	} else if telemetryState != domain.TelemetryStateFresh {
		command.State = domain.CommandStateRejectedByPolicy
		command.PolicyReason = "telemetry must be fresh"
	}

	r.commands[command.ID] = command

	return command, nil
}

func (r *MemoryRegistry) CreateMission(input CreateMissionInput, now time.Time) (domain.Mission, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.drones[input.DroneID]; !ok {
		return domain.Mission{}, ErrDroneNotFound
	}

	r.nextMissionSeq++
	mission := domain.Mission{
		ID:               fmt.Sprintf("msn-%06d", r.nextMissionSeq),
		DroneID:          input.DroneID,
		Name:             strings.TrimSpace(input.Name),
		CreatedBy:        input.CreatedBy,
		CreatedAt:        now,
		UpdatedAt:        now,
		CompletionAction: normalizeMissionCompletionAction(input.CompletionAction),
		ValidationStatus: domain.MissionValidationStatusValidated,
	}

	for i, waypoint := range input.Waypoints {
		mission.Waypoints = append(mission.Waypoints, domain.MissionWaypoint{
			Sequence:          i + 1,
			Latitude:          waypoint.Latitude,
			Longitude:         waypoint.Longitude,
			RelativeAltitudeM: waypoint.RelativeAltitudeM,
			SpeedMPS:          waypoint.SpeedMPS,
			LoiterTimeS:       waypoint.LoiterTimeS,
		})
	}

	mission.ValidationErrors = r.validateMissionLocked(mission, now)
	if len(mission.ValidationErrors) > 0 {
		mission.ValidationStatus = domain.MissionValidationStatusRejected
	}

	r.missions[mission.ID] = mission

	return mission, nil
}

func (r *MemoryRegistry) ListMissionsForDrone(droneID string) ([]domain.Mission, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, ok := r.drones[droneID]; !ok {
		return nil, ErrDroneNotFound
	}

	missions := make([]domain.Mission, 0)
	for _, mission := range r.missions {
		if mission.DroneID == droneID {
			missions = append(missions, mission)
		}
	}

	sort.Slice(missions, func(i, j int) bool {
		if missions[i].CreatedAt.Equal(missions[j].CreatedAt) {
			return missions[i].ID > missions[j].ID
		}

		return missions[i].CreatedAt.After(missions[j].CreatedAt)
	})

	return missions, nil
}

func (r *MemoryRegistry) RequestMissionUpload(input RequestMissionUploadInput, now time.Time) (domain.MissionExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	mission, ok := r.missions[input.MissionID]
	if !ok {
		return domain.MissionExecution{}, ErrMissionNotFound
	}

	if mission.ValidationStatus != domain.MissionValidationStatusValidated {
		return domain.MissionExecution{}, ErrMissionNotValidated
	}

	agent := r.agentForDroneLocked(mission.DroneID)
	if agent.ID == "" {
		return domain.MissionExecution{}, ErrAgentNotFound
	}

	if active := r.operationalMissionExecutionForDroneLocked(mission.DroneID, ""); active.ID != "" {
		return domain.MissionExecution{}, ErrDroneMissionActive
	}

	r.nextExecutionSeq++
	execution := domain.MissionExecution{
		ID:                fmt.Sprintf("mex-%06d", r.nextExecutionSeq),
		MissionID:         mission.ID,
		DroneID:           mission.DroneID,
		AgentID:           agent.ID,
		RequestedBy:       input.RequestedBy,
		UploadRequestedBy: input.RequestedBy,
		State:             domain.MissionExecutionStateUploadRequested,
		CreatedAt:         now,
		UpdatedAt:         now,
		UploadRequestedAt: now,
	}
	r.executions[execution.ID] = execution
	r.appendMissionExecutionEventLocked("upload_requested", "backend", execution, "mission upload requested", now)

	return execution, nil
}

func (r *MemoryRegistry) RecordMissionExecutionUploaded(executionID string, resultMessage string, now time.Time) (domain.MissionExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	execution, ok := r.executions[executionID]
	if !ok {
		return domain.MissionExecution{}, ErrMissionExecutionNotFound
	}

	if execution.State != domain.MissionExecutionStateUploadRequested &&
		execution.State != domain.MissionExecutionStateUploading {
		return domain.MissionExecution{}, ErrInvalidMissionExecutionState
	}

	execution.State = domain.MissionExecutionStateUploadedToVehicle
	execution.UpdatedAt = now
	execution.UploadedAt = now
	execution.LeaseUntil = time.Time{}
	execution.ResultMessage = resultMessage
	r.executions[execution.ID] = execution
	r.appendMissionExecutionEventLocked("uploaded_to_vehicle", "backend", execution, resultMessage, now)

	return execution, nil
}

func (r *MemoryRegistry) RequestMissionStart(input RequestMissionStartInput, now time.Time) (domain.MissionExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	mission, ok := r.missions[input.MissionID]
	if !ok {
		return domain.MissionExecution{}, ErrMissionNotFound
	}

	execution, ok := r.latestMissionExecutionInStateLocked(input.MissionID, domain.MissionExecutionStateUploadedToVehicle)
	if !ok {
		return domain.MissionExecution{}, ErrInvalidMissionExecutionState
	}

	if active := r.operationalMissionExecutionForDroneLocked(mission.DroneID, execution.ID); active.ID != "" {
		return domain.MissionExecution{}, ErrDroneMissionActive
	}

	if err := r.validateMissionStartPreconditionsLocked(mission, now); err != nil {
		return domain.MissionExecution{}, err
	}

	execution.State = domain.MissionExecutionStateStartRequested
	execution.RequestedBy = input.RequestedBy
	execution.StartRequestedBy = input.RequestedBy
	execution.UpdatedAt = now
	execution.StartRequestedAt = now
	r.executions[execution.ID] = execution
	r.appendMissionExecutionEventLocked("start_requested", "backend", execution, "mission start requested", now)

	return execution, nil
}

func (r *MemoryRegistry) RequestMissionAbort(input RequestMissionAbortInput, now time.Time) (domain.MissionExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	mission, ok := r.missions[input.MissionID]
	if !ok {
		return domain.MissionExecution{}, ErrMissionNotFound
	}

	execution := r.abortableMissionExecutionForMissionLocked(mission.ID)
	if execution.ID == "" {
		return domain.MissionExecution{}, ErrInvalidMissionExecutionState
	}

	execution.State = domain.MissionExecutionStateRTLRequested
	execution.RequestedBy = input.RequestedBy
	execution.UpdatedAt = now
	execution.LeaseUntil = time.Time{}
	execution.ResultMessage = "abort requested; returning to launch"
	r.executions[execution.ID] = execution
	r.appendMissionExecutionEventLocked("rtl_requested", "backend", execution, execution.ResultMessage, now)

	return execution, nil
}

func (r *MemoryRegistry) ListMissionExecutions(missionID string) ([]domain.MissionExecution, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, ok := r.missions[missionID]; !ok {
		return nil, ErrMissionNotFound
	}

	executions := make([]domain.MissionExecution, 0)
	for _, execution := range r.executions {
		if execution.MissionID == missionID {
			executions = append(executions, execution)
		}
	}

	sort.Slice(executions, func(i, j int) bool {
		if executions[i].CreatedAt.Equal(executions[j].CreatedAt) {
			return executions[i].ID > executions[j].ID
		}

		return executions[i].CreatedAt.After(executions[j].CreatedAt)
	})

	return executions, nil
}

func (r *MemoryRegistry) ListMissionExecutionEvents(missionID string) ([]domain.MissionExecutionEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, ok := r.missions[missionID]; !ok {
		return nil, ErrMissionNotFound
	}

	events := make([]domain.MissionExecutionEvent, 0)
	for _, event := range r.missionEvents {
		if event.MissionID == missionID {
			events = append(events, event)
		}
	}

	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].ID < events[j].ID
		}

		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})

	return events, nil
}

func (r *MemoryRegistry) NextMissionExecutionForAgent(agentID string, now time.Time) (domain.MissionExecution, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.agents[agentID]; !ok {
		return domain.MissionExecution{}, false, ErrAgentNotFound
	}

	var pending []domain.MissionExecution
	for _, execution := range r.executions {
		if execution.AgentID == agentID && isMissionExecutionDeliverable(execution, now) {
			pending = append(pending, execution)
		}
	}

	if len(pending) == 0 {
		return domain.MissionExecution{}, false, nil
	}

	sort.Slice(pending, func(i, j int) bool {
		if pending[i].UpdatedAt.Equal(pending[j].UpdatedAt) {
			return pending[i].ID < pending[j].ID
		}

		return pending[i].UpdatedAt.Before(pending[j].UpdatedAt)
	})

	execution := markMissionExecutionSent(pending[0], now)
	r.executions[execution.ID] = execution
	r.appendMissionExecutionEventLocked("sent_to_agent", "backend", execution, "mission execution sent to agent", now)

	return execution, true, nil
}

func (r *MemoryRegistry) ClaimMissionExecutionForAgent(agentID string, executionID string, now time.Time) (domain.MissionExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.agents[agentID]; !ok {
		return domain.MissionExecution{}, ErrAgentNotFound
	}

	execution, ok := r.executions[executionID]
	if !ok {
		return domain.MissionExecution{}, ErrMissionExecutionNotFound
	}

	if execution.AgentID != agentID {
		return domain.MissionExecution{}, ErrMissionExecutionNotAssigned
	}

	if !isMissionExecutionDeliverable(execution, now) {
		return domain.MissionExecution{}, ErrInvalidMissionExecutionState
	}

	execution = markMissionExecutionSent(execution, now)
	r.executions[execution.ID] = execution
	r.appendMissionExecutionEventLocked("sent_to_agent", "backend", execution, "mission execution sent to agent", now)

	return execution, nil
}

func (r *MemoryRegistry) UpdateMissionExecutionStatus(input UpdateMissionExecutionStatusInput, now time.Time) (domain.MissionExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	execution, ok := r.executions[input.ExecutionID]
	if !ok {
		return domain.MissionExecution{}, ErrMissionExecutionNotFound
	}

	if execution.AgentID != input.AgentID {
		return domain.MissionExecution{}, ErrMissionExecutionNotAssigned
	}

	if !canApplyAgentReportedMissionExecutionState(execution.State, input.State) {
		return domain.MissionExecution{}, ErrInvalidMissionExecutionState
	}

	execution.State = input.State
	execution.ResultMessage = input.ResultMessage
	execution.UpdatedAt = now
	execution.LeaseUntil = time.Time{}
	if input.CurrentMissionItem > 0 || input.TotalMissionItems > 0 {
		execution.CurrentMissionItem = input.CurrentMissionItem
		execution.TotalMissionItems = input.TotalMissionItems
		execution.ProgressUpdatedAt = now
	}
	switch input.State {
	case domain.MissionExecutionStateUploadedToVehicle:
		execution.UploadedAt = now
	case domain.MissionExecutionStateActive:
		if execution.StartedAt.IsZero() {
			execution.StartedAt = now
		}
	case domain.MissionExecutionStateCompleted:
		if execution.CompletedAt.IsZero() {
			execution.CompletedAt = now
		}
	case domain.MissionExecutionStateHold:
		if execution.CompletedAt.IsZero() {
			execution.CompletedAt = now
		}
		if execution.HoldAt.IsZero() {
			execution.HoldAt = now
		}
	case domain.MissionExecutionStateUploadFailed,
		domain.MissionExecutionStateFailed,
		domain.MissionExecutionStateAborted:
		execution.FailedAt = now
	}
	r.executions[execution.ID] = execution
	r.appendMissionExecutionEventLocked(missionExecutionEventType(input), "agent", execution, input.ResultMessage, now)

	return execution, nil
}

func (r *MemoryRegistry) NextCommandForAgent(agentID string, now time.Time) (domain.OperatorCommand, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.agents[agentID]; !ok {
		return domain.OperatorCommand{}, false, ErrAgentNotFound
	}

	var pending []domain.OperatorCommand
	for _, command := range r.commands {
		if command.AgentID == agentID && isCommandDeliverable(command, now) {
			pending = append(pending, command)
		}
	}

	if len(pending) == 0 {
		return domain.OperatorCommand{}, false, nil
	}

	sort.Slice(pending, func(i, j int) bool {
		if pending[i].RequestedAt.Equal(pending[j].RequestedAt) {
			return pending[i].ID < pending[j].ID
		}

		return pending[i].RequestedAt.Before(pending[j].RequestedAt)
	})

	command := markCommandSentToAgent(pending[0], now)
	r.commands[command.ID] = command

	return command, true, nil
}

func (r *MemoryRegistry) ClaimCommandForAgent(agentID string, commandID string, now time.Time) (domain.OperatorCommand, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.agents[agentID]; !ok {
		return domain.OperatorCommand{}, ErrAgentNotFound
	}

	command, ok := r.commands[commandID]
	if !ok {
		return domain.OperatorCommand{}, ErrCommandNotFound
	}

	if command.AgentID != agentID {
		return domain.OperatorCommand{}, ErrCommandNotAssigned
	}

	if !isCommandDeliverable(command, now) {
		return domain.OperatorCommand{}, ErrInvalidCommandTransition
	}

	command = markCommandSentToAgent(command, now)
	r.commands[command.ID] = command

	return command, nil
}

func (r *MemoryRegistry) UpdateCommandStatus(input UpdateCommandStatusInput, now time.Time) (domain.OperatorCommand, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	command, ok := r.commands[input.CommandID]
	if !ok {
		return domain.OperatorCommand{}, ErrCommandNotFound
	}

	if command.AgentID != input.AgentID {
		return domain.OperatorCommand{}, ErrCommandNotAssigned
	}

	if !isAgentReportedCommandState(input.State) {
		return domain.OperatorCommand{}, ErrInvalidCommandState
	}

	if !canApplyAgentReportedCommandState(command.State, input.State) {
		return domain.OperatorCommand{}, ErrInvalidCommandTransition
	}

	command.State = input.State
	command.ResultMessage = input.ResultMessage
	command.UpdatedAt = now
	command.LeaseUntil = time.Time{}
	if input.State == domain.CommandStateVehicleAcked {
		command.VehicleAckedAt = now
		command.ConfirmationBaseline = r.telemetry[command.DroneID]
		r.supersedeOlderAckedCommandsLocked(command, now)
	}
	r.commands[command.ID] = command

	return command, nil
}

func (r *MemoryRegistry) CommandByID(commandID string) (domain.OperatorCommand, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	command, ok := r.commands[commandID]
	return command, ok
}

func (r *MemoryRegistry) MissionByID(missionID string) (domain.Mission, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	mission, ok := r.missions[missionID]
	return mission, ok
}

func (r *MemoryRegistry) ListCommandsForDrone(droneID string, limit int) ([]domain.OperatorCommand, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, ok := r.drones[droneID]; !ok {
		return nil, ErrDroneNotFound
	}

	commands := make([]domain.OperatorCommand, 0)
	for _, command := range r.commands {
		if command.DroneID == droneID {
			commands = append(commands, command)
		}
	}

	sort.Slice(commands, func(i, j int) bool {
		if commands[i].RequestedAt.Equal(commands[j].RequestedAt) {
			return commands[i].ID > commands[j].ID
		}

		return commands[i].RequestedAt.After(commands[j].RequestedAt)
	})

	if limit > 0 && len(commands) > limit {
		commands = commands[:limit]
	}

	return commands, nil
}

func (r *MemoryRegistry) validateMissionLocked(mission domain.Mission, now time.Time) []domain.MissionValidationError {
	var validationErrors []domain.MissionValidationError

	agent := r.agentForDroneLocked(mission.DroneID)
	if agent.ID == "" {
		validationErrors = append(validationErrors, missionValidationError("agent", "drone must have a registered agent"))
	} else if domain.StatusFromHeartbeat(agent.LastHeartbeatAt, now) != domain.AgentStatusOnline {
		validationErrors = append(validationErrors, missionValidationError("agent", "agent must be online"))
	}

	telemetry := r.telemetry[mission.DroneID]
	if domain.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now) != domain.TelemetryStateFresh {
		validationErrors = append(validationErrors, missionValidationError("telemetry", "telemetry must be fresh"))
	}

	if !telemetry.HomePositionSet {
		validationErrors = append(validationErrors, missionValidationError("homePositionSet", "home position must be set"))
	}

	if !gpsFixUsableForMission(telemetry.GPSFix) {
		validationErrors = append(validationErrors, missionValidationError("gpsFix", "GPS fix must be usable"))
	}

	if telemetry.BatteryPercent < MinimumMissionBatteryPercent {
		validationErrors = append(validationErrors, missionValidationError("batteryPercent", fmt.Sprintf("battery must be at least %.0f%%", MinimumMissionBatteryPercent)))
	}

	if strings.TrimSpace(mission.Name) == "" {
		validationErrors = append(validationErrors, missionValidationError("name", "mission name is required"))
	}

	if len(mission.Waypoints) == 0 {
		validationErrors = append(validationErrors, missionValidationError("waypoints", "mission must include at least one waypoint"))
	}

	if len(mission.Waypoints) > MaximumMissionWaypoints {
		validationErrors = append(validationErrors, missionValidationError("waypoints", fmt.Sprintf("mission cannot include more than %d waypoints", MaximumMissionWaypoints)))
	}

	if !validMissionCompletionAction(mission.CompletionAction) {
		validationErrors = append(validationErrors, missionValidationError("completionAction", "completion action must be hold, return_to_launch, or land"))
	}

	for _, waypoint := range mission.Waypoints {
		fieldPrefix := fmt.Sprintf("waypoints[%d]", waypoint.Sequence-1)
		if !validLatitude(waypoint.Latitude) {
			validationErrors = append(validationErrors, missionValidationError(fieldPrefix+".latitude", "latitude must be between -90 and 90"))
		}

		if !validLongitude(waypoint.Longitude) {
			validationErrors = append(validationErrors, missionValidationError(fieldPrefix+".longitude", "longitude must be between -180 and 180"))
		}

		if waypoint.RelativeAltitudeM < MinimumMissionAltitudeM || waypoint.RelativeAltitudeM > MaximumMissionAltitudeM {
			validationErrors = append(validationErrors, missionValidationError(fieldPrefix+".relativeAltitudeM", fmt.Sprintf("relative altitude must be between %.0f and %.0f meters", MinimumMissionAltitudeM, MaximumMissionAltitudeM)))
		}

		if waypoint.SpeedMPS != nil && *waypoint.SpeedMPS <= 0 {
			validationErrors = append(validationErrors, missionValidationError(fieldPrefix+".speedMPS", "speed must be greater than 0 when provided"))
		}

		if waypoint.LoiterTimeS != nil && *waypoint.LoiterTimeS < 0 {
			validationErrors = append(validationErrors, missionValidationError(fieldPrefix+".loiterTimeS", "loiter time cannot be negative when provided"))
		}
	}

	return validationErrors
}

func (r *MemoryRegistry) validateMissionStartPreconditionsLocked(mission domain.Mission, now time.Time) error {
	agent := r.agentForDroneLocked(mission.DroneID)
	if agent.ID == "" {
		return missionStartPreconditionError("drone has no registered agent")
	}

	if domain.StatusFromHeartbeat(agent.LastHeartbeatAt, now) != domain.AgentStatusOnline {
		return missionStartPreconditionError("agent must be online before mission start")
	}

	telemetry := r.telemetry[mission.DroneID]
	if domain.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now) != domain.TelemetryStateFresh {
		return missionStartPreconditionError("fresh telemetry is required before mission start")
	}

	return nil
}

func missionStartPreconditionError(reason string) MissionStartPreconditionError {
	return MissionStartPreconditionError{
		Reason: fmt.Sprintf("%s; required sequence: upload -> start mission launch workflow", reason),
	}
}

func (r *MemoryRegistry) latestMissionExecutionInStateLocked(missionID string, state domain.MissionExecutionState) (domain.MissionExecution, bool) {
	var latest domain.MissionExecution
	for _, execution := range r.executions {
		if execution.MissionID != missionID || execution.State != state {
			continue
		}

		if latest.ID == "" ||
			execution.CreatedAt.After(latest.CreatedAt) ||
			(execution.CreatedAt.Equal(latest.CreatedAt) && execution.ID > latest.ID) {
			latest = execution
		}
	}

	return latest, latest.ID != ""
}

func (r *MemoryRegistry) latestMissionExecutionForDroneLocked(droneID string) domain.MissionExecution {
	var latest domain.MissionExecution
	for _, execution := range r.executions {
		if execution.DroneID != droneID {
			continue
		}

		if latest.ID == "" ||
			missionExecutionSnapshotRank(execution.State) > missionExecutionSnapshotRank(latest.State) ||
			(missionExecutionSnapshotRank(execution.State) == missionExecutionSnapshotRank(latest.State) && execution.UpdatedAt.After(latest.UpdatedAt)) ||
			(missionExecutionSnapshotRank(execution.State) == missionExecutionSnapshotRank(latest.State) && execution.UpdatedAt.Equal(latest.UpdatedAt) && execution.ID > latest.ID) {
			latest = execution
		}
	}

	return latest
}

func (r *MemoryRegistry) operationalMissionExecutionForDroneLocked(droneID string, exceptExecutionID string) domain.MissionExecution {
	var latest domain.MissionExecution
	for _, execution := range r.executions {
		if execution.DroneID != droneID || execution.ID == exceptExecutionID || !isOperationalMissionExecutionState(execution.State) {
			continue
		}

		if latest.ID == "" ||
			execution.UpdatedAt.After(latest.UpdatedAt) ||
			(execution.UpdatedAt.Equal(latest.UpdatedAt) && execution.ID > latest.ID) {
			latest = execution
		}
	}

	return latest
}

func (r *MemoryRegistry) abortableMissionExecutionForMissionLocked(missionID string) domain.MissionExecution {
	var latest domain.MissionExecution
	for _, execution := range r.executions {
		if execution.MissionID != missionID || !isAbortableMissionExecutionState(execution.State) {
			continue
		}

		if latest.ID == "" ||
			execution.UpdatedAt.After(latest.UpdatedAt) ||
			(execution.UpdatedAt.Equal(latest.UpdatedAt) && execution.ID > latest.ID) {
			latest = execution
		}
	}

	return latest
}

func (r *MemoryRegistry) appendMissionExecutionEventLocked(eventType string, source string, execution domain.MissionExecution, message string, now time.Time) {
	r.nextEventSeq++
	event := domain.MissionExecutionEvent{
		ID:                 fmt.Sprintf("mev-%06d", r.nextEventSeq),
		ExecutionID:        execution.ID,
		MissionID:          execution.MissionID,
		DroneID:            execution.DroneID,
		AgentID:            execution.AgentID,
		Type:               eventType,
		State:              execution.State,
		Message:            message,
		CurrentMissionItem: execution.CurrentMissionItem,
		TotalMissionItems:  execution.TotalMissionItems,
		Source:             source,
		CreatedAt:          now,
	}
	r.missionEvents[event.ID] = event
}

func missionExecutionEventType(input UpdateMissionExecutionStatusInput) string {
	if input.State == domain.MissionExecutionStateActive &&
		(input.CurrentMissionItem > 0 || input.TotalMissionItems > 0) {
		return "progress"
	}

	return string(input.State)
}

func missionExecutionSnapshotRank(state domain.MissionExecutionState) int {
	switch state {
	case domain.MissionExecutionStateStartRequested,
		domain.MissionExecutionStateActive,
		domain.MissionExecutionStateHold,
		domain.MissionExecutionStatePausedOrHold,
		domain.MissionExecutionStateRTLRequested:
		return 2
	default:
		return 1
	}
}

func isOperationalMissionExecutionState(state domain.MissionExecutionState) bool {
	switch state {
	case domain.MissionExecutionStateUploadRequested,
		domain.MissionExecutionStateUploading,
		domain.MissionExecutionStateStartRequested,
		domain.MissionExecutionStateActive,
		domain.MissionExecutionStateHold,
		domain.MissionExecutionStatePausedOrHold,
		domain.MissionExecutionStateRTLRequested:
		return true
	default:
		return false
	}
}

func isAbortableMissionExecutionState(state domain.MissionExecutionState) bool {
	switch state {
	case domain.MissionExecutionStateStartRequested,
		domain.MissionExecutionStateActive,
		domain.MissionExecutionStateHold,
		domain.MissionExecutionStatePausedOrHold:
		return true
	default:
		return false
	}
}

func missionValidationError(field string, message string) domain.MissionValidationError {
	return domain.MissionValidationError{
		Field:   field,
		Message: message,
	}
}

func gpsFixUsableForMission(fix string) bool {
	normalized := strings.ToLower(strings.TrimSpace(fix))
	return strings.Contains(normalized, "3d") ||
		strings.Contains(normalized, "rtk") ||
		strings.Contains(normalized, "dgps")
}

func normalizeMissionCompletionAction(action domain.MissionCompletionAction) domain.MissionCompletionAction {
	if strings.TrimSpace(string(action)) == "" {
		return domain.MissionCompletionActionReturnToLaunch
	}

	return action
}

func validMissionCompletionAction(action domain.MissionCompletionAction) bool {
	switch action {
	case domain.MissionCompletionActionHold,
		domain.MissionCompletionActionReturnToLaunch,
		domain.MissionCompletionActionLand:
		return true
	default:
		return false
	}
}

func validLatitude(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= -90 && value <= 90
}

func validLongitude(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= -180 && value <= 180
}

func (r *MemoryRegistry) agentForDroneLocked(droneID string) domain.Agent {
	for _, agent := range r.agents {
		if agent.DroneID == droneID {
			return agent
		}
	}

	return domain.Agent{}
}

func commandChannelSnapshot(agent domain.Agent) CommandChannelSnapshot {
	state := agent.CommandChannelState
	if state == "" {
		state = domain.CommandChannelDisconnected
	}

	return CommandChannelSnapshot{
		State:              state,
		ConnectedAt:        agent.CommandChannelConnectedAt,
		LastDisconnectedAt: agent.CommandChannelLastDisconnectedAt,
	}
}

func isAgentReportedCommandState(state domain.CommandState) bool {
	switch state {
	case domain.CommandStateAgentReceived,
		domain.CommandStateSentToVehicle,
		domain.CommandStateVehicleAcked,
		domain.CommandStateVehicleRejected,
		domain.CommandStateTimedOut,
		domain.CommandStateFailed:
		return true
	default:
		return false
	}
}

func isCommandDeliverable(command domain.OperatorCommand, now time.Time) bool {
	if command.State == domain.CommandStateAuthorized {
		return true
	}

	return command.State == domain.CommandStateSentToAgent &&
		!command.LeaseUntil.IsZero() &&
		!command.LeaseUntil.After(now)
}

func isMissionExecutionDeliverable(execution domain.MissionExecution, now time.Time) bool {
	if execution.State == domain.MissionExecutionStateUploadRequested ||
		execution.State == domain.MissionExecutionStateStartRequested ||
		execution.State == domain.MissionExecutionStateRTLRequested {
		if execution.LeaseUntil.IsZero() {
			return execution.LastSentAt.IsZero()
		}

		return !execution.LeaseUntil.After(now)
	}

	return false
}

func markCommandSentToAgent(command domain.OperatorCommand, now time.Time) domain.OperatorCommand {
	command.State = domain.CommandStateSentToAgent
	command.UpdatedAt = now
	command.LastSentAt = now
	command.LeaseUntil = now.Add(domain.CommandDeliveryLeaseDuration)
	command.DeliveryAttempt++
	return command
}

func markMissionExecutionSent(execution domain.MissionExecution, now time.Time) domain.MissionExecution {
	execution.UpdatedAt = now
	execution.LastSentAt = now
	execution.LeaseUntil = now.Add(domain.MissionExecutionDeliveryLeaseDuration)
	execution.DeliveryAttempt++
	return execution
}

func canApplyAgentReportedCommandState(current domain.CommandState, next domain.CommandState) bool {
	switch current {
	case domain.CommandStateSentToAgent:
		return next == domain.CommandStateAgentReceived ||
			next == domain.CommandStateSentToVehicle ||
			isTerminalAgentCommandState(next)
	case domain.CommandStateAgentReceived:
		return next == domain.CommandStateSentToVehicle ||
			isTerminalAgentCommandState(next)
	case domain.CommandStateSentToVehicle:
		return isTerminalAgentCommandState(next)
	default:
		return false
	}
}

func canApplyAgentReportedMissionExecutionState(current domain.MissionExecutionState, next domain.MissionExecutionState) bool {
	switch current {
	case domain.MissionExecutionStateUploadRequested:
		return next == domain.MissionExecutionStateUploading ||
			next == domain.MissionExecutionStateUploadedToVehicle ||
			next == domain.MissionExecutionStateUploadFailed ||
			next == domain.MissionExecutionStateFailed
	case domain.MissionExecutionStateUploading:
		return next == domain.MissionExecutionStateUploadedToVehicle ||
			next == domain.MissionExecutionStateUploadFailed ||
			next == domain.MissionExecutionStateFailed
	case domain.MissionExecutionStateStartRequested:
		return next == domain.MissionExecutionStateActive ||
			next == domain.MissionExecutionStateCompleted ||
			next == domain.MissionExecutionStateHold ||
			next == domain.MissionExecutionStateFailed
	case domain.MissionExecutionStateActive:
		return next == domain.MissionExecutionStateActive ||
			next == domain.MissionExecutionStateCompleted ||
			next == domain.MissionExecutionStateHold ||
			next == domain.MissionExecutionStateAborted ||
			next == domain.MissionExecutionStateFailed ||
			next == domain.MissionExecutionStatePausedOrHold ||
			next == domain.MissionExecutionStateRTLRequested
	case domain.MissionExecutionStateCompleted:
		return next == domain.MissionExecutionStateHold
	case domain.MissionExecutionStatePausedOrHold:
		return next == domain.MissionExecutionStateActive ||
			next == domain.MissionExecutionStateCompleted ||
			next == domain.MissionExecutionStateHold ||
			next == domain.MissionExecutionStateAborted ||
			next == domain.MissionExecutionStateFailed
	case domain.MissionExecutionStateRTLRequested:
		return next == domain.MissionExecutionStateRTLRequested ||
			next == domain.MissionExecutionStateCompleted ||
			next == domain.MissionExecutionStateHold ||
			next == domain.MissionExecutionStateAborted ||
			next == domain.MissionExecutionStateFailed
	default:
		return false
	}
}

func (r *MemoryRegistry) confirmCommandsFromTelemetryLocked(droneID string, snapshot domain.TelemetrySnapshot, now time.Time) {
	for id, command := range r.commands {
		if command.DroneID != droneID || command.State != domain.CommandStateVehicleAcked {
			continue
		}

		if !telemetryConfirmsCommand(command, snapshot) {
			continue
		}

		command.State = domain.CommandStateTelemetryConfirmed
		command.UpdatedAt = now
		command.ResultMessage = "confirmed by telemetry"
		r.commands[id] = command
	}
}

func (r *MemoryRegistry) supersedeOlderAckedCommandsLocked(newer domain.OperatorCommand, now time.Time) {
	for id, command := range r.commands {
		if command.ID == newer.ID ||
			command.DroneID != newer.DroneID ||
			command.Type != newer.Type ||
			command.State != domain.CommandStateVehicleAcked ||
			!command.RequestedAt.Before(newer.RequestedAt) {
			continue
		}

		command.State = domain.CommandStateFailed
		command.UpdatedAt = now
		command.ResultMessage = fmt.Sprintf("superseded by newer %s command", newer.Type)
		r.commands[id] = command
	}
}

func telemetryConfirmsCommand(command domain.OperatorCommand, snapshot domain.TelemetrySnapshot) bool {
	// Phase 2 confirmation is transition-based rather than true MAVLink correlation.
	// This is maintainable for now, but it cannot prove causality if an external
	// or manual action causes the same telemetry transition after a command ACK.
	// Upgrade this to MAVLink-level command/ACK correlation when the Vehicle
	// Gateway starts handling lower-level MAVLink command IDs directly.
	if !telemetryObservedAfterVehicleAck(command, snapshot) {
		return false
	}

	baseline := command.ConfirmationBaseline
	switch command.Type {
	case domain.CommandTypeArm:
		return !baseline.Armed && snapshot.Armed
	case domain.CommandTypeTakeoff:
		return !baseline.InAir && snapshot.InAir
	case domain.CommandTypeLand:
		return (!flightModeIsLand(baseline.FlightMode) && flightModeIsLand(snapshot.FlightMode)) ||
			(baseline.InAir && !snapshot.InAir) ||
			(baseline.Armed && !snapshot.Armed)
	case domain.CommandTypeReturnToLaunch:
		return !flightModeIsReturn(baseline.FlightMode) && flightModeIsReturn(snapshot.FlightMode)
	default:
		return false
	}
}

func telemetryObservedAfterVehicleAck(command domain.OperatorCommand, snapshot domain.TelemetrySnapshot) bool {
	if command.VehicleAckedAt.IsZero() {
		return true
	}

	observedAt := snapshot.ObservedAt
	if observedAt.IsZero() {
		observedAt = snapshot.ReceivedAt
	}

	return !observedAt.Before(command.VehicleAckedAt)
}

func flightModeIsReturn(mode string) bool {
	normalized := strings.ToLower(mode)
	return strings.Contains(normalized, "return") || strings.Contains(normalized, "rtl")
}

func flightModeIsLand(mode string) bool {
	normalized := strings.ToLower(mode)
	return strings.Contains(normalized, "land")
}

func isTerminalAgentCommandState(state domain.CommandState) bool {
	switch state {
	case domain.CommandStateVehicleAcked,
		domain.CommandStateVehicleRejected,
		domain.CommandStateTimedOut,
		domain.CommandStateFailed:
		return true
	default:
		return false
	}
}
