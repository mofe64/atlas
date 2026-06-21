package registry

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
)

func TestSQLiteStorePersistsCoreRegistryState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atlas.db")
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	store, err := OpenSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	agent := store.RegisterAgent(RegisterAgentInput{
		AgentID:      "agent-001",
		DroneID:      "drone-001",
		DroneName:    "Training Quad",
		AgentVersion: "test",
	}, now)
	if agent.ID != "agent-001" {
		t.Fatalf("agent ID = %q, want agent-001", agent.ID)
	}
	if _, err := store.RecordHeartbeat(HeartbeatInput{AgentID: "agent-001", AgentVersion: "test"}, now.Add(time.Second)); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}
	if _, err := store.RecordTelemetry(domain.TelemetrySnapshot{
		AgentID:           "agent-001",
		ObservedAt:        now.Add(2 * time.Second),
		BatteryPercent:    82,
		RelativeAltitudeM: 12,
		FlightMode:        "Hold",
		Latitude:          47.397742,
		Longitude:         8.545594,
		HeadingDeg:        90,
		GroundSpeedMPS:    2,
		GPSFix:            "3d",
		SatellitesVisible: 12,
		HomePositionSet:   true,
		Source:            "test",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("RecordTelemetry: %v", err)
	}
	command, err := store.RequestCommand(RequestCommandInput{
		DroneID:     "drone-001",
		Type:        domain.CommandTypeArm,
		RequestedBy: "operator",
	}, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("RequestCommand: %v", err)
	}
	if command.ID != "cmd-000001" {
		t.Fatalf("command ID = %q, want cmd-000001", command.ID)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("reopen SQLite store: %v", err)
	}
	defer reopened.Close()

	drones := reopened.ListDrones(now.Add(3 * time.Second))
	if len(drones) != 1 {
		t.Fatalf("ListDrones returned %d drones, want 1", len(drones))
	}
	if drones[0].ID != "drone-001" || drones[0].Telemetry.DroneID != "drone-001" {
		t.Fatalf("unexpected persisted drone snapshot: %+v", drones[0])
	}
	if drones[0].Telemetry.BatteryPercent != 82 {
		t.Fatalf("battery = %v, want 82", drones[0].Telemetry.BatteryPercent)
	}
	persistedCommand, ok := reopened.CommandByID("cmd-000001")
	if !ok {
		t.Fatal("expected persisted command cmd-000001")
	}
	if persistedCommand.Type != domain.CommandTypeArm {
		t.Fatalf("command type = %q, want arm", persistedCommand.Type)
	}
}
