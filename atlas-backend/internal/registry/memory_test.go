package registry

import (
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
)

func TestRegisterAgentUpsertsDroneAndAgent(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	later := now.Add(2 * time.Second)
	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.1",
	}, later)

	drones := reg.ListDrones(later)
	if len(drones) != 1 {
		t.Fatalf("expected one drone, got %d", len(drones))
	}

	if drones[0].Status != domain.AgentStatusRegistered {
		t.Fatalf("expected registered status, got %q", drones[0].Status)
	}
}

func TestRecordHeartbeatUpdatesDerivedStatus(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	if _, err := reg.RecordHeartbeat(HeartbeatInput{
		AgentID:      "agent-001",
		AgentVersion: "0.1.0",
	}, now); err != nil {
		t.Fatalf("record heartbeat: %v", err)
	}

	drones := reg.ListDrones(now.Add(10 * time.Second))
	if drones[0].Status != domain.AgentStatusOnline {
		t.Fatalf("expected online status, got %q", drones[0].Status)
	}

	drones = reg.ListDrones(now.Add(30 * time.Second))
	if drones[0].Status != domain.AgentStatusStale {
		t.Fatalf("expected stale status, got %q", drones[0].Status)
	}

	drones = reg.ListDrones(now.Add(90 * time.Second))
	if drones[0].Status != domain.AgentStatusOffline {
		t.Fatalf("expected offline status, got %q", drones[0].Status)
	}
}

func TestRecordTelemetryStoresLatestSnapshot(t *testing.T) {
	reg := NewMemoryRegistry()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	reg.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad 1",
		AgentVersion: "0.1.0",
	}, now)

	snapshot, err := reg.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:           "agent-001",
		ObservedAt:        now,
		BatteryPercent:    82,
		RelativeAltitudeM: 12.5,
		FlightMode:        "HOLD",
		GPSFix:            "3D",
		Source:            "px4",
	}, now)
	if err != nil {
		t.Fatalf("record telemetry: %v", err)
	}

	if snapshot.DroneID != "drone-001" {
		t.Fatalf("expected drone-001, got %q", snapshot.DroneID)
	}

	drones := reg.ListDrones(now)
	if drones[0].TelemetryState != domain.TelemetryStateFresh {
		t.Fatalf("expected fresh telemetry, got %q", drones[0].TelemetryState)
	}

	if drones[0].Telemetry.BatteryPercent != 82 {
		t.Fatalf("expected battery 82, got %f", drones[0].Telemetry.BatteryPercent)
	}
}
