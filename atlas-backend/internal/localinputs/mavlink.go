package localinputs

import (
	"encoding/binary"
	"math"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

const (
	mavlinkV1Magic = 0xfe
	mavlinkV2Magic = 0xfd

	mavlinkMsgHeartbeat         = 0
	mavlinkMsgSysStatus         = 1
	mavlinkMsgGPSRawInt         = 24
	mavlinkMsgAttitude          = 30
	mavlinkMsgGlobalPositionInt = 33
	mavlinkMsgVFRHUD            = 74
	mavlinkMsgBatteryStatus     = 147

	mavModeFlagSafetyArmed = 128
)

type mavlinkFrame struct {
	messageID uint32
	payload   []byte
}

type telemetryAccumulator struct {
	observedAt        time.Time
	batteryPercent    float64
	hasBattery        bool
	relativeAltitudeM float64
	altitudeMSL       float64
	hasAltitude       bool
	flightMode        string
	armed             bool
	hasHeartbeat      bool
	latitude          float64
	longitude         float64
	hasPosition       bool
	rollDeg           float64
	pitchDeg          float64
	headingDeg        float64
	hasHeading        bool
	velocityNorthMPS  float64
	velocityEastMPS   float64
	velocityDownMPS   float64
	groundSpeedMPS    float64
	hasVelocity       bool
	gpsFix            string
	satellitesVisible int
	hasGPS            bool
}

func parseMAVLinkFrames(packet []byte) []mavlinkFrame {
	frames := []mavlinkFrame{}
	for offset := 0; offset < len(packet); {
		if packet[offset] != mavlinkV1Magic && packet[offset] != mavlinkV2Magic {
			offset++
			continue
		}

		frame, size, ok := parseMAVLinkFrame(packet[offset:])
		if !ok {
			offset++
			continue
		}
		frames = append(frames, frame)
		offset += size
	}
	return frames
}

func parseMAVLinkFrame(packet []byte) (mavlinkFrame, int, bool) {
	if len(packet) < 8 {
		return mavlinkFrame{}, 0, false
	}

	switch packet[0] {
	case mavlinkV1Magic:
		payloadLen := int(packet[1])
		size := 6 + payloadLen + 2
		if len(packet) < size {
			return mavlinkFrame{}, 0, false
		}
		payload := append([]byte(nil), packet[6:6+payloadLen]...)
		return mavlinkFrame{messageID: uint32(packet[5]), payload: payload}, size, true
	case mavlinkV2Magic:
		if len(packet) < 12 {
			return mavlinkFrame{}, 0, false
		}
		payloadLen := int(packet[1])
		size := 10 + payloadLen + 2
		if packet[2]&0x01 != 0 {
			size += 13
		}
		if len(packet) < size {
			return mavlinkFrame{}, 0, false
		}
		messageID := uint32(packet[7]) | uint32(packet[8])<<8 | uint32(packet[9])<<16
		payload := append([]byte(nil), packet[10:10+payloadLen]...)
		return mavlinkFrame{messageID: messageID, payload: payload}, size, true
	default:
		return mavlinkFrame{}, 0, false
	}
}

func (a *telemetryAccumulator) handleFrame(frame mavlinkFrame, now time.Time) {
	a.observedAt = now
	switch frame.messageID {
	case mavlinkMsgHeartbeat:
		a.handleHeartbeat(frame.payload)
	case mavlinkMsgSysStatus:
		a.handleSysStatus(frame.payload)
	case mavlinkMsgGPSRawInt:
		a.handleGPSRawInt(frame.payload)
	case mavlinkMsgAttitude:
		a.handleAttitude(frame.payload)
	case mavlinkMsgGlobalPositionInt:
		a.handleGlobalPositionInt(frame.payload)
	case mavlinkMsgVFRHUD:
		a.handleVFRHUD(frame.payload)
	case mavlinkMsgBatteryStatus:
		a.handleBatteryStatus(frame.payload)
	}
}

func (a *telemetryAccumulator) snapshot(source string) (models.TelemetrySnapshot, bool) {
	if !a.hasPosition && !a.hasHeartbeat && !a.hasGPS {
		return models.TelemetrySnapshot{}, false
	}

	flightMode := a.flightMode
	if flightMode == "" {
		flightMode = "UNKNOWN"
	}
	gpsFix := a.gpsFix
	if gpsFix == "" {
		gpsFix = "UNKNOWN"
	}
	battery := a.batteryPercent
	if !a.hasBattery {
		battery = 0
	}
	observedAt := a.observedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	return models.TelemetrySnapshot{
		ObservedAt:        observedAt,
		BatteryPercent:    battery,
		RelativeAltitudeM: a.relativeAltitudeM,
		AltitudeMSL:       a.altitudeMSL,
		FlightMode:        flightMode,
		Armed:             a.armed,
		InAir:             a.relativeAltitudeM > 0.5 || a.groundSpeedMPS > 1,
		Latitude:          a.latitude,
		Longitude:         a.longitude,
		RollDeg:           a.rollDeg,
		PitchDeg:          a.pitchDeg,
		HeadingDeg:        a.headingDeg,
		VelocityNorthMPS:  a.velocityNorthMPS,
		VelocityEastMPS:   a.velocityEastMPS,
		VelocityDownMPS:   a.velocityDownMPS,
		GroundSpeedMPS:    a.groundSpeedMPS,
		GPSFix:            gpsFix,
		SatellitesVisible: a.satellitesVisible,
		HomePositionSet:   false,
		Source:            source,
	}, true
}

func (a *telemetryAccumulator) handleHeartbeat(payload []byte) {
	if len(payload) < 9 {
		return
	}
	baseMode := payload[6]
	customMode := binary.LittleEndian.Uint32(payload[0:4])
	a.armed = baseMode&mavModeFlagSafetyArmed != 0
	a.flightMode = px4ModeLabel(customMode)
	a.hasHeartbeat = true
}

func (a *telemetryAccumulator) handleSysStatus(payload []byte) {
	if len(payload) < 31 {
		return
	}
	remaining := int8(payload[30])
	if remaining < 0 || remaining > 100 {
		return
	}
	a.batteryPercent = float64(remaining)
	a.hasBattery = true
}

func (a *telemetryAccumulator) handleGPSRawInt(payload []byte) {
	if len(payload) < 30 {
		return
	}
	a.latitude = float64(int32(binary.LittleEndian.Uint32(payload[8:12]))) / 1e7
	a.longitude = float64(int32(binary.LittleEndian.Uint32(payload[12:16]))) / 1e7
	a.altitudeMSL = float64(int32(binary.LittleEndian.Uint32(payload[16:20]))) / 1000
	a.gpsFix = gpsFixLabel(payload[28])
	a.satellitesVisible = int(payload[29])
	a.hasPosition = a.latitude != 0 || a.longitude != 0
	a.hasGPS = true
}

func (a *telemetryAccumulator) handleAttitude(payload []byte) {
	if len(payload) < 28 {
		return
	}
	a.rollDeg = radiansToDegrees(float32FromLE(payload[4:8]))
	a.pitchDeg = radiansToDegrees(float32FromLE(payload[8:12]))
	heading := radiansToDegrees(float32FromLE(payload[12:16]))
	if heading < 0 {
		heading += 360
	}
	a.headingDeg = heading
	a.hasHeading = true
}

func (a *telemetryAccumulator) handleGlobalPositionInt(payload []byte) {
	if len(payload) < 28 {
		return
	}
	a.latitude = float64(int32(binary.LittleEndian.Uint32(payload[4:8]))) / 1e7
	a.longitude = float64(int32(binary.LittleEndian.Uint32(payload[8:12]))) / 1e7
	a.altitudeMSL = float64(int32(binary.LittleEndian.Uint32(payload[12:16]))) / 1000
	a.relativeAltitudeM = float64(int32(binary.LittleEndian.Uint32(payload[16:20]))) / 1000
	a.velocityNorthMPS = float64(int16(binary.LittleEndian.Uint16(payload[20:22]))) / 100
	a.velocityEastMPS = float64(int16(binary.LittleEndian.Uint16(payload[22:24]))) / 100
	a.velocityDownMPS = float64(int16(binary.LittleEndian.Uint16(payload[24:26]))) / 100
	a.groundSpeedMPS = math.Hypot(a.velocityNorthMPS, a.velocityEastMPS)
	heading := binary.LittleEndian.Uint16(payload[26:28])
	if heading != 65535 {
		a.headingDeg = float64(heading) / 100
		a.hasHeading = true
	}
	a.hasPosition = a.latitude != 0 || a.longitude != 0
	a.hasAltitude = true
	a.hasVelocity = true
}

func (a *telemetryAccumulator) handleVFRHUD(payload []byte) {
	if len(payload) < 20 {
		return
	}
	a.groundSpeedMPS = float64(float32FromLE(payload[4:8]))
	a.altitudeMSL = float64(float32FromLE(payload[12:16]))
	heading := int16(binary.LittleEndian.Uint16(payload[16:18]))
	if heading >= 0 {
		a.headingDeg = float64(heading)
		a.hasHeading = true
	}
	a.hasVelocity = true
}

func (a *telemetryAccumulator) handleBatteryStatus(payload []byte) {
	if len(payload) < 36 {
		return
	}
	remaining := int8(payload[35])
	if remaining < 0 || remaining > 100 {
		return
	}
	a.batteryPercent = float64(remaining)
	a.hasBattery = true
}

func float32FromLE(raw []byte) float64 {
	return float64(math.Float32frombits(binary.LittleEndian.Uint32(raw)))
}

func radiansToDegrees(value float64) float64 {
	return value * 180 / math.Pi
}

func gpsFixLabel(fixType byte) string {
	switch fixType {
	case 0:
		return "NO_GPS"
	case 1:
		return "NO_FIX"
	case 2:
		return "2D"
	case 3:
		return "3D"
	case 4:
		return "DGPS"
	case 5:
		return "RTK_FLOAT"
	case 6:
		return "RTK_FIXED"
	default:
		return "UNKNOWN"
	}
}

func px4ModeLabel(customMode uint32) string {
	if customMode == 0 {
		return "UNKNOWN"
	}
	return "PX4_CUSTOM_" + uintToDecimal(customMode)
}

func uintToDecimal(value uint32) string {
	if value == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}
