package mavlinkobserver

import (
	"bytes"
	"encoding/binary"
	"strings"
	"time"
)

func DecodeFrame(frame Frame, observedAt time.Time) (Observation, bool) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	base := Observation{
		ObservedAt:  observedAt.UTC(),
		SystemID:    frame.SystemID,
		ComponentID: frame.ComponentID,
		MessageID:   frame.MessageID,
	}

	switch frame.MessageID {
	case MessageIDHeartbeat:
		return decodeHeartbeat(base, frame.Payload)
	case MessageIDSystemStatus:
		return decodeSystemStatus(base, frame.Payload)
	case MessageIDBatteryStatus:
		return decodeBatteryStatus(base, frame.Payload)
	case MessageIDGlobalPositionInt:
		return decodeGlobalPositionInt(base, frame.Payload)
	case MessageIDGPSRawInt:
		return decodeGPSRawInt(base, frame.Payload)
	case MessageIDStatusText:
		return decodeStatusText(base, frame.Payload)
	case MessageIDCommandAck:
		return decodeCommandAck(base, frame.Payload)
	case MessageIDMissionCurrent:
		return decodeMissionCurrent(base, frame.Payload)
	default:
		return Observation{}, false
	}
}

func decodeHeartbeat(base Observation, payload []byte) (Observation, bool) {
	if len(payload) < 9 {
		return Observation{}, false
	}
	base.Kind = ObservationHeartbeat
	base.Heartbeat = &HeartbeatObservation{
		CustomMode:     binary.LittleEndian.Uint32(payload[0:4]),
		Type:           payload[4],
		Autopilot:      payload[5],
		BaseMode:       payload[6],
		SystemStatus:   payload[7],
		MAVLinkVersion: payload[8],
	}
	return base, true
}

func decodeSystemStatus(base Observation, payload []byte) (Observation, bool) {
	if len(payload) < 31 {
		return Observation{}, false
	}
	base.Kind = ObservationSystemStatus
	base.SystemStatus = &SystemStatusObservation{
		VoltageBatteryMV:        binary.LittleEndian.Uint16(payload[14:16]),
		CurrentBatteryCA:        int16(binary.LittleEndian.Uint16(payload[16:18])),
		DropRateComm:            binary.LittleEndian.Uint16(payload[18:20]),
		ErrorsComm:              binary.LittleEndian.Uint16(payload[20:22]),
		BatteryRemainingPercent: int8(payload[30]),
	}
	return base, true
}

func decodeBatteryStatus(base Observation, payload []byte) (Observation, bool) {
	if len(payload) < 36 {
		return Observation{}, false
	}

	voltages := make([]uint16, 0, 10)
	for offset := 0; offset < 20; offset += 2 {
		voltage := binary.LittleEndian.Uint16(payload[offset : offset+2])
		if voltage != 0xffff {
			voltages = append(voltages, voltage)
		}
	}

	temperature := int16(binary.LittleEndian.Uint16(payload[34:36]))
	var temperatureCelsius *float64
	if temperature != 0x7fff {
		value := float64(temperature) / 100
		temperatureCelsius = &value
	}

	base.Kind = ObservationBatteryStatus
	base.BatteryStatus = &BatteryStatusObservation{
		CurrentBatteryCA:        int16(binary.LittleEndian.Uint16(payload[20:22])),
		BatteryRemainingPercent: int8(payload[30]),
		ID:                      payload[31],
		Function:                payload[32],
		Type:                    payload[33],
		TemperatureCelsius:      temperatureCelsius,
		VoltagesMV:              voltages,
	}
	return base, true
}

func decodeGlobalPositionInt(base Observation, payload []byte) (Observation, bool) {
	if len(payload) < 28 {
		return Observation{}, false
	}

	headingRaw := binary.LittleEndian.Uint16(payload[26:28])
	var heading *float64
	if headingRaw != 0xffff {
		value := float64(headingRaw) / 100
		heading = &value
	}

	base.Kind = ObservationGlobalPositionInt
	base.Position = &GlobalPositionObservation{
		LatitudeDeg:       float64(int32(binary.LittleEndian.Uint32(payload[4:8]))) / 1e7,
		LongitudeDeg:      float64(int32(binary.LittleEndian.Uint32(payload[8:12]))) / 1e7,
		AltitudeMSLM:      float64(int32(binary.LittleEndian.Uint32(payload[12:16]))) / 1000,
		RelativeAltitudeM: float64(int32(binary.LittleEndian.Uint32(payload[16:20]))) / 1000,
		VelocityXMPS:      float64(int16(binary.LittleEndian.Uint16(payload[20:22]))) / 100,
		VelocityYMPS:      float64(int16(binary.LittleEndian.Uint16(payload[22:24]))) / 100,
		VelocityZMPS:      float64(int16(binary.LittleEndian.Uint16(payload[24:26]))) / 100,
		HeadingDeg:        heading,
	}
	return base, true
}

func decodeGPSRawInt(base Observation, payload []byte) (Observation, bool) {
	if len(payload) < 30 {
		return Observation{}, false
	}

	cogRaw := binary.LittleEndian.Uint16(payload[26:28])
	var courseOverGround *float64
	if cogRaw != 0xffff {
		value := float64(cogRaw) / 100
		courseOverGround = &value
	}

	base.Kind = ObservationGPSRawInt
	base.GPS = &GPSRawObservation{
		LatitudeDeg:         float64(int32(binary.LittleEndian.Uint32(payload[8:12]))) / 1e7,
		LongitudeDeg:        float64(int32(binary.LittleEndian.Uint32(payload[12:16]))) / 1e7,
		AltitudeMSLM:        float64(int32(binary.LittleEndian.Uint32(payload[16:20]))) / 1000,
		EPH:                 binary.LittleEndian.Uint16(payload[20:22]),
		EPV:                 binary.LittleEndian.Uint16(payload[22:24]),
		GroundSpeedMPS:      float64(binary.LittleEndian.Uint16(payload[24:26])) / 100,
		CourseOverGroundDeg: courseOverGround,
		FixType:             payload[28],
		SatellitesVisible:   payload[29],
	}
	return base, true
}

func decodeStatusText(base Observation, payload []byte) (Observation, bool) {
	if len(payload) < 51 {
		return Observation{}, false
	}
	text := payload[1:51]
	if nul := bytes.IndexByte(text, 0); nul >= 0 {
		text = text[:nul]
	}

	base.Kind = ObservationStatusText
	base.StatusText = &StatusTextObservation{
		Severity: payload[0],
		Text:     strings.TrimSpace(string(text)),
	}
	return base, true
}

func decodeCommandAck(base Observation, payload []byte) (Observation, bool) {
	if len(payload) < 3 {
		return Observation{}, false
	}

	ack := &CommandAckObservation{
		Command: binary.LittleEndian.Uint16(payload[0:2]),
		Result:  payload[2],
	}
	if len(payload) >= 4 {
		ack.Progress = uint8Ptr(payload[3])
	}
	if len(payload) >= 8 {
		value := int32(binary.LittleEndian.Uint32(payload[4:8]))
		ack.ResultParam2 = &value
	}
	if len(payload) >= 9 {
		ack.TargetSystem = uint8Ptr(payload[8])
	}
	if len(payload) >= 10 {
		ack.TargetComponent = uint8Ptr(payload[9])
	}

	base.Kind = ObservationCommandAck
	base.CommandAck = ack
	return base, true
}

func decodeMissionCurrent(base Observation, payload []byte) (Observation, bool) {
	if len(payload) < 2 {
		return Observation{}, false
	}

	current := &MissionCurrentObservation{
		Sequence: binary.LittleEndian.Uint16(payload[0:2]),
	}
	if len(payload) >= 4 {
		value := binary.LittleEndian.Uint16(payload[2:4])
		current.Total = &value
	}
	if len(payload) >= 5 {
		current.MissionState = uint8Ptr(payload[4])
	}
	if len(payload) >= 6 {
		current.MissionMode = uint8Ptr(payload[5])
	}

	base.Kind = ObservationMissionCurrent
	base.MissionCurrent = current
	return base, true
}

func uint8Ptr(value uint8) *uint8 {
	return &value
}
