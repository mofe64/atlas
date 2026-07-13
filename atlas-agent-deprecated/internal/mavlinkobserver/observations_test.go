package mavlinkobserver

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestObserverDecodesHeartbeat(t *testing.T) {
	payload := make([]byte, 9)
	binary.LittleEndian.PutUint32(payload[0:4], 1234)
	payload[4] = 2
	payload[5] = 12
	payload[6] = 81
	payload[7] = 4
	payload[8] = 3

	observation := decodeOnlyObservation(t, mavlinkV1Frame(1, 42, 1, MessageIDHeartbeat, payload))
	if observation.Kind != ObservationHeartbeat {
		t.Fatalf("expected heartbeat observation, got %q", observation.Kind)
	}
	if observation.SystemID != 42 || observation.ComponentID != 1 {
		t.Fatalf("unexpected component identity: %#v", observation)
	}
	if observation.Heartbeat.CustomMode != 1234 {
		t.Fatalf("expected custom mode 1234, got %d", observation.Heartbeat.CustomMode)
	}
	if observation.Heartbeat.Type != 2 || observation.Heartbeat.Autopilot != 12 {
		t.Fatalf("unexpected heartbeat fields: %#v", observation.Heartbeat)
	}
}

func TestObserverDecodesCommandAck(t *testing.T) {
	payload := make([]byte, 10)
	binary.LittleEndian.PutUint16(payload[0:2], 400)
	payload[2] = 0
	payload[3] = 100
	binary.LittleEndian.PutUint32(payload[4:8], 7)
	payload[8] = 1
	payload[9] = 1

	observation := decodeOnlyObservation(t, mavlinkV2Frame(2, 1, 1, MessageIDCommandAck, payload, false))
	if observation.Kind != ObservationCommandAck {
		t.Fatalf("expected command ack observation, got %q", observation.Kind)
	}
	if observation.CommandAck.Command != 400 {
		t.Fatalf("expected MAV_CMD_COMPONENT_ARM_DISARM ack, got %d", observation.CommandAck.Command)
	}
	if observation.CommandAck.Result != 0 {
		t.Fatalf("expected accepted result, got %d", observation.CommandAck.Result)
	}
	if observation.CommandAck.Progress == nil || *observation.CommandAck.Progress != 100 {
		t.Fatalf("expected 100 progress, got %v", observation.CommandAck.Progress)
	}
	if observation.CommandAck.TargetSystem == nil || *observation.CommandAck.TargetSystem != 1 {
		t.Fatalf("expected target system 1, got %v", observation.CommandAck.TargetSystem)
	}
}

func TestObserverDecodesGlobalPosition(t *testing.T) {
	payload := make([]byte, 28)
	lat := int32(515074000)
	lon := int32(-1278000)
	velocityY := int16(-100)
	binary.LittleEndian.PutUint32(payload[4:8], uint32(lat))
	binary.LittleEndian.PutUint32(payload[8:12], uint32(lon))
	binary.LittleEndian.PutUint32(payload[12:16], uint32(int32(105000)))
	binary.LittleEndian.PutUint32(payload[16:20], uint32(int32(30000)))
	binary.LittleEndian.PutUint16(payload[20:22], uint16(int16(250)))
	binary.LittleEndian.PutUint16(payload[22:24], uint16(velocityY))
	binary.LittleEndian.PutUint16(payload[24:26], uint16(int16(0)))
	binary.LittleEndian.PutUint16(payload[26:28], 9123)

	observation := decodeOnlyObservation(t, mavlinkV2Frame(3, 1, 1, MessageIDGlobalPositionInt, payload, false))
	if observation.Kind != ObservationGlobalPositionInt {
		t.Fatalf("expected global position observation, got %q", observation.Kind)
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

func TestObserverDecodesMissionCurrentAndStatusText(t *testing.T) {
	missionPayload := make([]byte, 6)
	binary.LittleEndian.PutUint16(missionPayload[0:2], 4)
	binary.LittleEndian.PutUint16(missionPayload[2:4], 12)
	missionPayload[4] = 3
	missionPayload[5] = 1

	statusPayload := make([]byte, 51)
	statusPayload[0] = 6
	copy(statusPayload[1:], "EKF using GPS")

	var observer Observer
	data := append(
		mavlinkV2Frame(1, 1, 1, MessageIDMissionCurrent, missionPayload, false),
		mavlinkV2Frame(2, 1, 1, MessageIDStatusText, statusPayload, false)...,
	)
	observations, err := observer.Push(data, time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("push observations: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("expected two observations, got %d", len(observations))
	}

	if observations[0].MissionCurrent.Sequence != 4 {
		t.Fatalf("expected mission current 4, got %d", observations[0].MissionCurrent.Sequence)
	}
	if observations[0].MissionCurrent.Total == nil || *observations[0].MissionCurrent.Total != 12 {
		t.Fatalf("expected mission total 12, got %v", observations[0].MissionCurrent.Total)
	}
	if !strings.Contains(observations[1].StatusText.Text, "EKF") {
		t.Fatalf("expected status text, got %q", observations[1].StatusText.Text)
	}
}

func decodeOnlyObservation(t *testing.T, data []byte) Observation {
	t.Helper()

	var observer Observer
	observations, err := observer.Push(data, time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("push observation: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("expected one observation, got %d", len(observations))
	}
	return observations[0]
}
