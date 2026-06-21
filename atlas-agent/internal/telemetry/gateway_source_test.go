package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
)

type stubGateway struct {
	events chan vehicle.TelemetryEvent
}

func (g *stubGateway) Telemetry(ctx context.Context) (<-chan vehicle.TelemetryEvent, error) {
	return g.events, nil
}

func (g *stubGateway) Arm(ctx context.Context) error {
	return nil
}

func (g *stubGateway) Takeoff(ctx context.Context) error {
	return nil
}

func (g *stubGateway) ReturnToLaunch(ctx context.Context) error {
	return nil
}

func (g *stubGateway) Land(ctx context.Context) error {
	return nil
}

func (g *stubGateway) UploadMission(ctx context.Context, mission vehicle.MissionPlan) error {
	return nil
}

func (g *stubGateway) PrepareMissionStart(ctx context.Context) error {
	return nil
}

func (g *stubGateway) StartMission(ctx context.Context) error {
	return nil
}

func (g *stubGateway) MissionProgress(ctx context.Context) (<-chan vehicle.MissionProgressEvent, error) {
	ch := make(chan vehicle.MissionProgressEvent)
	close(ch)
	return ch, nil
}

func TestGatewaySourceReadUsesLatestVehicleEvent(t *testing.T) {
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	gateway := &stubGateway{events: make(chan vehicle.TelemetryEvent, 2)}
	gateway.events <- vehicle.TelemetryEvent{
		ObservedAt:        now,
		BatteryPercent:    82,
		RelativeAltitudeM: 12.5,
		FlightMode:        "HOLD",
		Latitude:          51.5074,
		Longitude:         -0.1278,
		GPSFix:            "3D",
		SatellitesVisible: 14,
		HomePositionSet:   true,
		Source:            "px4",
	}

	source, err := NewGatewaySource(context.Background(), "px4", gateway)
	if err != nil {
		t.Fatalf("new gateway source: %v", err)
	}

	snapshot, err := source.Read(now.Add(time.Second))
	if err != nil {
		t.Fatalf("read telemetry: %v", err)
	}

	if snapshot.Source != "px4" {
		t.Fatalf("expected px4 source, got %q", snapshot.Source)
	}

	if snapshot.BatteryPercent != 82 {
		t.Fatalf("expected battery 82, got %f", snapshot.BatteryPercent)
	}
}

func TestGatewaySourceReadRejectsStaleTelemetry(t *testing.T) {
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	gateway := &stubGateway{events: make(chan vehicle.TelemetryEvent, 1)}
	gateway.events <- vehicle.TelemetryEvent{
		ObservedAt: now,
		Source:     "px4",
	}

	source, err := NewGatewaySource(context.Background(), "px4", gateway)
	if err != nil {
		t.Fatalf("new gateway source: %v", err)
	}

	_, err = source.Read(now.Add(telemetryReadMaxAge + time.Second))
	if err == nil {
		t.Fatal("expected stale telemetry error")
	}
}
