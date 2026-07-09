package mavlinkobserver

import (
	"strings"
	"time"

	"github.com/bluenviron/gomavlib/v4"
	"github.com/bluenviron/gomavlib/v4/pkg/dialects/common"
	"github.com/bluenviron/gomavlib/v4/pkg/message"
)

func DecodeEventFrame(event *gomavlib.EventFrame, observedAt time.Time) (Observation, bool) {
	if event == nil {
		return Observation{}, false
	}

	return DecodeMessage(event.SystemID(), event.ComponentID(), event.Message(), observedAt)
}

func DecodeMessage(systemID uint8, componentID uint8, msg message.Message, observedAt time.Time) (Observation, bool) {
	if msg == nil {
		return Observation{}, false
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	base := Observation{
		ObservedAt:  observedAt.UTC(),
		SystemID:    systemID,
		ComponentID: componentID,
		MessageID:   msg.GetID(),
	}

	switch m := msg.(type) {
	case *common.MessageHeartbeat:
		base.Kind = ObservationHeartbeat
		base.Heartbeat = &HeartbeatObservation{
			CustomMode:     m.CustomMode,
			Type:           uint8(m.Type),
			Autopilot:      uint8(m.Autopilot),
			BaseMode:       uint8(m.BaseMode),
			SystemStatus:   uint8(m.SystemStatus),
			MAVLinkVersion: m.MavlinkVersion,
		}
		return base, true
	case *common.MessageSysStatus:
		base.Kind = ObservationSystemStatus
		base.SystemStatus = &SystemStatusObservation{
			VoltageBatteryMV:        m.VoltageBattery,
			CurrentBatteryCA:        m.CurrentBattery,
			BatteryRemainingPercent: m.BatteryRemaining,
			DropRateComm:            m.DropRateComm,
			ErrorsComm:              m.ErrorsComm,
		}
		return base, true
	case *common.MessageBatteryStatus:
		base.Kind = ObservationBatteryStatus
		base.BatteryStatus = batteryStatusObservation(m)
		return base, true
	case *common.MessageGlobalPositionInt:
		base.Kind = ObservationGlobalPositionInt
		base.Position = globalPositionObservation(m)
		return base, true
	case *common.MessageGpsRawInt:
		base.Kind = ObservationGPSRawInt
		base.GPS = gpsRawObservation(m)
		return base, true
	case *common.MessageStatustext:
		base.Kind = ObservationStatusText
		base.StatusText = &StatusTextObservation{
			Severity: uint8(m.Severity),
			Text:     strings.TrimSpace(strings.TrimRight(m.Text, "\x00")),
		}
		return base, true
	case *common.MessageCommandAck:
		base.Kind = ObservationCommandAck
		base.CommandAck = &CommandAckObservation{
			Command:         uint16(m.Command),
			Result:          uint8(m.Result),
			Progress:        uint8Ptr(m.Progress),
			ResultParam2:    &m.ResultParam2,
			TargetSystem:    uint8Ptr(m.TargetSystem),
			TargetComponent: uint8Ptr(m.TargetComponent),
		}
		return base, true
	case *common.MessageMissionCurrent:
		total := m.Total
		missionState := uint8(m.MissionState)
		base.Kind = ObservationMissionCurrent
		base.MissionCurrent = &MissionCurrentObservation{
			Sequence:     m.Seq,
			Total:        &total,
			MissionState: &missionState,
			MissionMode:  &m.MissionMode,
		}
		return base, true
	default:
		return Observation{}, false
	}
}

func batteryStatusObservation(message *common.MessageBatteryStatus) *BatteryStatusObservation {
	voltages := make([]uint16, 0, len(message.Voltages))
	for _, voltage := range message.Voltages {
		if voltage != 0xffff {
			voltages = append(voltages, voltage)
		}
	}

	var temperature *float64
	if message.Temperature != 0x7fff {
		value := float64(message.Temperature) / 100
		temperature = &value
	}

	return &BatteryStatusObservation{
		ID:                      message.Id,
		Function:                uint8(message.BatteryFunction),
		Type:                    uint8(message.Type),
		TemperatureCelsius:      temperature,
		VoltagesMV:              voltages,
		CurrentBatteryCA:        message.CurrentBattery,
		BatteryRemainingPercent: message.BatteryRemaining,
	}
}

func globalPositionObservation(message *common.MessageGlobalPositionInt) *GlobalPositionObservation {
	var heading *float64
	if message.Hdg != 0xffff {
		value := float64(message.Hdg) / 100
		heading = &value
	}

	return &GlobalPositionObservation{
		LatitudeDeg:       float64(message.Lat) / 1e7,
		LongitudeDeg:      float64(message.Lon) / 1e7,
		AltitudeMSLM:      float64(message.Alt) / 1000,
		RelativeAltitudeM: float64(message.RelativeAlt) / 1000,
		VelocityXMPS:      float64(message.Vx) / 100,
		VelocityYMPS:      float64(message.Vy) / 100,
		VelocityZMPS:      float64(message.Vz) / 100,
		HeadingDeg:        heading,
	}
}

func gpsRawObservation(message *common.MessageGpsRawInt) *GPSRawObservation {
	var courseOverGround *float64
	if message.Cog != 0xffff {
		value := float64(message.Cog) / 100
		courseOverGround = &value
	}

	return &GPSRawObservation{
		FixType:             uint8(message.FixType),
		LatitudeDeg:         float64(message.Lat) / 1e7,
		LongitudeDeg:        float64(message.Lon) / 1e7,
		AltitudeMSLM:        float64(message.Alt) / 1000,
		EPH:                 message.Eph,
		EPV:                 message.Epv,
		GroundSpeedMPS:      float64(message.Vel) / 100,
		CourseOverGroundDeg: courseOverGround,
		SatellitesVisible:   message.SatellitesVisible,
	}
}
