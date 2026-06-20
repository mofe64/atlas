package registry

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
)

var ErrAgentNotFound = errors.New("agent not found")

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

type DroneSnapshot struct {
	ID              string
	Name            string
	AgentID         string
	Status          domain.AgentStatus
	LastSeenAt      time.Time
	LastHeartbeatAt time.Time
	Telemetry       domain.TelemetrySnapshot
	TelemetryState  domain.TelemetryState
}

type MemoryRegistry struct {
	mu        sync.RWMutex
	drones    map[string]domain.Drone
	agents    map[string]domain.Agent
	telemetry map[string]domain.TelemetrySnapshot
}

func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{
		drones:    make(map[string]domain.Drone),
		agents:    make(map[string]domain.Agent),
		telemetry: make(map[string]domain.TelemetrySnapshot),
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
			ID:              drone.ID,
			Name:            drone.Name,
			AgentID:         agent.ID,
			Status:          domain.StatusFromHeartbeat(agent.LastHeartbeatAt, now),
			LastSeenAt:      drone.LastSeenAt,
			LastHeartbeatAt: agent.LastHeartbeatAt,
			Telemetry:       telemetry,
			TelemetryState:  domain.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now),
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

	return snapshot, nil
}

func (r *MemoryRegistry) agentForDroneLocked(droneID string) domain.Agent {
	for _, agent := range r.agents {
		if agent.DroneID == droneID {
			return agent
		}
	}

	return domain.Agent{}
}
