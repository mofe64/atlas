package services

import (
	"context"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

func TestVehicleAgentServiceRegisterVehicleAgentUsesTxManagerRepositories(t *testing.T) {
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	expected := models.VehicleAgent{ID: "agent-001", DroneID: "drone-001"}
	agents := &fakeVehicleAgentRepository{registerAgent: expected}
	drones := &fakeDroneRepository{}
	txManager := &recordingTxManager{
		repos: repository.Repositories{VehicleAgents: agents, Drones: drones},
	}

	service := NewVehicleAgentService(txManager)
	agent, err := service.RegisterVehicleAgent(context.Background(), repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad",
		VehicleAgentVersion: "0.1.0",
	}, now)
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}

	if !txManager.called {
		t.Fatal("expected service to enter TxManager")
	}
	if !agents.registerCalled {
		t.Fatal("expected service to upsert transaction-bound agent repository")
	}
	if !agents.revokeCalled {
		t.Fatal("expected service to revoke previous active agents")
	}
	if !drones.upsertCalled {
		t.Fatal("expected service to upsert transaction-bound drone repository")
	}
	if agent.ID != expected.ID || agent.DroneID != expected.DroneID {
		t.Fatalf("expected %#v, got %#v", expected, agent)
	}
}

type recordingTxManager struct {
	called bool
	repos  repository.Repositories
}

func (m *recordingTxManager) WithinTx(ctx context.Context, fn func(context.Context, repository.Repositories) error) error {
	m.called = true
	return fn(ctx, m.repos)
}

type fakeVehicleAgentRepository struct {
	registerCalled bool
	revokeCalled   bool
	registerAgent  models.VehicleAgent
}

func (r *fakeVehicleAgentRepository) UpsertVehicleAgentRegistration(context.Context, models.VehicleAgent) error {
	r.registerCalled = true
	return nil
}

func (r *fakeVehicleAgentRepository) RevokeActiveVehicleAgentsForDrone(context.Context, string, string, time.Time) error {
	r.revokeCalled = true
	return nil
}

func (r *fakeVehicleAgentRepository) UpdateVehicleAgentHeartbeat(context.Context, repository.VehicleAgentHeartbeatInput, time.Time) (models.VehicleAgent, error) {
	return models.VehicleAgent{}, nil
}

func (r *fakeVehicleAgentRepository) SetCommandChannelState(context.Context, string, models.CommandChannelState, time.Time) (models.VehicleAgent, error) {
	return models.VehicleAgent{}, nil
}

func (r *fakeVehicleAgentRepository) GetVehicleAgentByID(context.Context, string) (models.VehicleAgent, bool, error) {
	return r.registerAgent, true, nil
}

func (r *fakeVehicleAgentRepository) VehicleAgentExists(context.Context, string) bool {
	return false
}

func (r *fakeVehicleAgentRepository) GetActiveVehicleAgentForDrone(context.Context, string) (models.VehicleAgent, bool, error) {
	return models.VehicleAgent{}, false, nil
}

type fakeDroneRepository struct {
	upsertCalled bool
}

func (r *fakeDroneRepository) UpsertDroneRegistration(context.Context, string, string, time.Time) error {
	r.upsertCalled = true
	return nil
}

func (r *fakeDroneRepository) DroneExists(context.Context, string) bool {
	return false
}

func (r *fakeDroneRepository) UpdateDroneLastSeen(context.Context, string, time.Time) error {
	return nil
}

func (r *fakeDroneRepository) ListDrones(context.Context, time.Time) []repository.DroneSnapshot {
	return nil
}
