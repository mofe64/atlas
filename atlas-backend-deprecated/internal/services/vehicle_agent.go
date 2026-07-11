package services

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

// VehicleAgentService manages onboard agent registration, heartbeats, and command-channel state.
type VehicleAgentService struct {
	txManager repository.TxManager
}

// NewVehicleAgentService builds the workflow service used when a drone companion agent connects to Atlas.
func NewVehicleAgentService(txManager repository.TxManager) *VehicleAgentService {
	return &VehicleAgentService{txManager: txManager}
}

// RegisterVehicleAgent records a companion agent as the active command-capable process for a drone.
func (s *VehicleAgentService) RegisterVehicleAgent(ctx context.Context, input repository.RegisterVehicleAgentInput, now time.Time) (models.VehicleAgent, error) {
	var agent models.VehicleAgent
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		agent, err = s.registerVehicleAgent(ctx, repos, input, now)
		return err
	})
	return agent, err
}

// RecordVehicleAgentHeartbeat updates liveness for an agent and its drone so operators see current fleet status.
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

// registerVehicleAgent performs the transactional registration steps shared by agent connection workflows.
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

// setCommandChannelState records whether Atlas currently has a live command stream to the onboard agent.
func (s *VehicleAgentService) setCommandChannelState(ctx context.Context, agentID string, state models.CommandChannelState, now time.Time) (models.VehicleAgent, error) {
	var agent models.VehicleAgent
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		agent, err = repos.VehicleAgents.SetCommandChannelState(ctx, agentID, state, now)
		return err
	})
	return agent, err
}

// RecordCommandChannelConnected marks an agent ready to receive commands over the backend-agent stream.
func (s *VehicleAgentService) RecordCommandChannelConnected(ctx context.Context, agentID string, now time.Time) (models.VehicleAgent, error) {
	return s.setCommandChannelState(ctx, agentID, models.CommandChannelConnected, now)
}

// RecordCommandChannelDisconnected marks an agent unavailable for command delivery after the stream drops.
func (s *VehicleAgentService) RecordCommandChannelDisconnected(ctx context.Context, agentID string, now time.Time) (models.VehicleAgent, error) {
	return s.setCommandChannelState(ctx, agentID, models.CommandChannelDisconnected, now)
}
