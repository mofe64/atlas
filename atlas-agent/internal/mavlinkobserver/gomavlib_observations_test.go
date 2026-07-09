package mavlinkobserver

import (
	"testing"
	"time"

	"github.com/bluenviron/gomavlib/v4/pkg/dialects/common"
)

func TestDecodeMessageFromGomavlibCommandAck(t *testing.T) {
	observedAt := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	observation, ok := DecodeMessage(1, 1, &common.MessageCommandAck{
		Command:         common.MAV_CMD_COMPONENT_ARM_DISARM,
		Result:          common.MAV_RESULT_ACCEPTED,
		Progress:        100,
		ResultParam2:    7,
		TargetSystem:    250,
		TargetComponent: 191,
	}, observedAt)
	if !ok {
		t.Fatal("expected command ack observation")
	}

	if observation.Kind != ObservationCommandAck {
		t.Fatalf("expected command ack kind, got %q", observation.Kind)
	}
	if observation.ObservedAt != observedAt {
		t.Fatalf("expected observed_at %s, got %s", observedAt, observation.ObservedAt)
	}
	if observation.CommandAck.Command != 400 {
		t.Fatalf("expected MAV_CMD_COMPONENT_ARM_DISARM, got %d", observation.CommandAck.Command)
	}
	if observation.CommandAck.Result != 0 {
		t.Fatalf("expected MAV_RESULT_ACCEPTED, got %d", observation.CommandAck.Result)
	}
	if observation.CommandAck.Progress == nil || *observation.CommandAck.Progress != 100 {
		t.Fatalf("expected progress 100, got %v", observation.CommandAck.Progress)
	}
	if observation.CommandAck.TargetSystem == nil || *observation.CommandAck.TargetSystem != 250 {
		t.Fatalf("expected target system 250, got %v", observation.CommandAck.TargetSystem)
	}
	if observation.CommandAck.TargetComponent == nil || *observation.CommandAck.TargetComponent != 191 {
		t.Fatalf("expected target component 191, got %v", observation.CommandAck.TargetComponent)
	}
}

func TestDecodeMessageFromGomavlibGlobalPosition(t *testing.T) {
	observation, ok := DecodeMessage(1, 1, &common.MessageGlobalPositionInt{
		Lat:         515074000,
		Lon:         -1278000,
		Alt:         105000,
		RelativeAlt: 30000,
		Vx:          250,
		Vy:          -100,
		Vz:          0,
		Hdg:         9123,
	}, time.Time{})
	if !ok {
		t.Fatal("expected global position observation")
	}

	if observation.Kind != ObservationGlobalPositionInt {
		t.Fatalf("expected global position kind, got %q", observation.Kind)
	}
	if observation.Position.LatitudeDeg != 51.5074 {
		t.Fatalf("expected latitude 51.5074, got %f", observation.Position.LatitudeDeg)
	}
	if observation.Position.LongitudeDeg != -0.1278 {
		t.Fatalf("expected longitude -0.1278, got %f", observation.Position.LongitudeDeg)
	}
	if observation.Position.RelativeAltitudeM != 30 {
		t.Fatalf("expected relative altitude 30m, got %f", observation.Position.RelativeAltitudeM)
	}
	if observation.Position.HeadingDeg == nil || *observation.Position.HeadingDeg != 91.23 {
		t.Fatalf("expected heading 91.23, got %v", observation.Position.HeadingDeg)
	}
}

func TestDecodeMessageFromGomavlibHeartbeat(t *testing.T) {
	observation, ok := DecodeMessage(1, 1, &common.MessageHeartbeat{
		Type:           common.MAV_TYPE_QUADROTOR,
		Autopilot:      common.MAV_AUTOPILOT_PX4,
		BaseMode:       81,
		CustomMode:     1234,
		SystemStatus:   common.MAV_STATE_ACTIVE,
		MavlinkVersion: 3,
	}, time.Time{})
	if !ok {
		t.Fatal("expected heartbeat observation")
	}

	if observation.Kind != ObservationHeartbeat {
		t.Fatalf("expected heartbeat kind, got %q", observation.Kind)
	}
	if observation.Heartbeat.Type != 2 {
		t.Fatalf("expected quadrotor type, got %d", observation.Heartbeat.Type)
	}
	if observation.Heartbeat.Autopilot != 12 {
		t.Fatalf("expected PX4 autopilot, got %d", observation.Heartbeat.Autopilot)
	}
	if observation.Heartbeat.SystemStatus != 4 {
		t.Fatalf("expected active system status, got %d", observation.Heartbeat.SystemStatus)
	}
}
