package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/backend"
	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
)

func TestNextBackoffCapsAtMax(t *testing.T) {
	if got := nextBackoff(time.Second, 30*time.Second); got != 2*time.Second {
		t.Fatalf("expected 2s, got %s", got)
	}

	if got := nextBackoff(20*time.Second, 30*time.Second); got != 30*time.Second {
		t.Fatalf("expected cap at 30s, got %s", got)
	}
}

func TestExecuteVehicleCommandRoutesToGatewayMethod(t *testing.T) {
	tests := map[string]struct {
		commandType string
		wantMethod  string
	}{
		"arm": {
			commandType: backend.CommandTypeArm,
			wantMethod:  "arm",
		},
		"takeoff": {
			commandType: backend.CommandTypeTakeoff,
			wantMethod:  "takeoff",
		},
		"return to launch": {
			commandType: backend.CommandTypeReturnToLaunch,
			wantMethod:  "return_to_launch",
		},
		"land": {
			commandType: backend.CommandTypeLand,
			wantMethod:  "land",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			gateway := &fakeGateway{}
			err := executeVehicleCommand(context.Background(), gateway, backend.Command{Type: tt.commandType})
			if err != nil {
				t.Fatalf("execute command: %v", err)
			}

			if gateway.called != tt.wantMethod {
				t.Fatalf("expected %q, got %q", tt.wantMethod, gateway.called)
			}
		})
	}
}

func TestExecuteVehicleCommandRejectsUnsupportedCommand(t *testing.T) {
	err := executeVehicleCommand(context.Background(), &fakeGateway{}, backend.Command{Type: "orbit"})
	if !errors.Is(err, errUnsupportedCommand) {
		t.Fatalf("expected unsupported command error, got %v", err)
	}
}

type fakeGateway struct {
	called string
}

func (g *fakeGateway) Telemetry(context.Context) (<-chan vehicle.TelemetryEvent, error) {
	return make(chan vehicle.TelemetryEvent), nil
}

func (g *fakeGateway) Arm(context.Context) error {
	g.called = "arm"
	return nil
}

func (g *fakeGateway) Takeoff(context.Context) error {
	g.called = "takeoff"
	return nil
}

func (g *fakeGateway) ReturnToLaunch(context.Context) error {
	g.called = "return_to_launch"
	return nil
}

func (g *fakeGateway) Land(context.Context) error {
	g.called = "land"
	return nil
}

func (g *fakeGateway) UploadMission(context.Context, vehicle.MissionPlan) error {
	g.called = "upload_mission"
	return nil
}

func (g *fakeGateway) PrepareMissionStart(context.Context) error {
	g.called = "prepare_mission_start"
	return nil
}

func (g *fakeGateway) StartMission(context.Context) error {
	g.called = "start_mission"
	return nil
}

func (g *fakeGateway) MissionProgress(context.Context) (<-chan vehicle.MissionProgressEvent, error) {
	ch := make(chan vehicle.MissionProgressEvent)
	close(ch)
	return ch, nil
}
