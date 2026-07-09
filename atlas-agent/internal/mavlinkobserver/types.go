// Package mavlinkobserver decodes selected read-only MAVLink messages.
//
// The observer is intentionally not a vehicle command gateway. It consumes raw
// MAVLink bytes and emits evidence that other agent modules can use for
// diagnostics, telemetry cross-checks, and command ACK correlation.
package mavlinkobserver

import "time"

const (
	mavlinkV1Magic byte = 0xfe
	mavlinkV2Magic byte = 0xfd
)

const (
	MessageIDHeartbeat         uint32 = 0
	MessageIDSystemStatus      uint32 = 1
	MessageIDGPSRawInt         uint32 = 24
	MessageIDGlobalPositionInt uint32 = 33
	MessageIDMissionCurrent    uint32 = 42
	MessageIDCommandAck        uint32 = 77
	MessageIDBatteryStatus     uint32 = 147
	MessageIDStatusText        uint32 = 253
)

type Frame struct {
	Version       int
	Sequence      uint8
	SystemID      uint8
	ComponentID   uint8
	MessageID     uint32
	Payload       []byte
	Checksum      uint16
	IncompatFlags uint8
	CompatFlags   uint8
	Signature     []byte
}

type ObservationKind string

const (
	ObservationHeartbeat         ObservationKind = "HEARTBEAT"
	ObservationSystemStatus      ObservationKind = "SYS_STATUS"
	ObservationBatteryStatus     ObservationKind = "BATTERY_STATUS"
	ObservationGlobalPositionInt ObservationKind = "GLOBAL_POSITION_INT"
	ObservationGPSRawInt         ObservationKind = "GPS_RAW_INT"
	ObservationStatusText        ObservationKind = "STATUSTEXT"
	ObservationCommandAck        ObservationKind = "COMMAND_ACK"
	ObservationMissionCurrent    ObservationKind = "MISSION_CURRENT"
)

type Observation struct {
	Kind        ObservationKind
	ObservedAt  time.Time
	SystemID    uint8
	ComponentID uint8
	MessageID   uint32

	Heartbeat      *HeartbeatObservation
	SystemStatus   *SystemStatusObservation
	BatteryStatus  *BatteryStatusObservation
	Position       *GlobalPositionObservation
	GPS            *GPSRawObservation
	StatusText     *StatusTextObservation
	CommandAck     *CommandAckObservation
	MissionCurrent *MissionCurrentObservation
}

type HeartbeatObservation struct {
	CustomMode     uint32
	Type           uint8
	Autopilot      uint8
	BaseMode       uint8
	SystemStatus   uint8
	MAVLinkVersion uint8
}

type SystemStatusObservation struct {
	VoltageBatteryMV        uint16
	CurrentBatteryCA        int16
	BatteryRemainingPercent int8
	DropRateComm            uint16
	ErrorsComm              uint16
}

type BatteryStatusObservation struct {
	ID                      uint8
	Function                uint8
	Type                    uint8
	TemperatureCelsius      *float64
	VoltagesMV              []uint16
	CurrentBatteryCA        int16
	BatteryRemainingPercent int8
}

type GlobalPositionObservation struct {
	LatitudeDeg       float64
	LongitudeDeg      float64
	AltitudeMSLM      float64
	RelativeAltitudeM float64
	VelocityXMPS      float64
	VelocityYMPS      float64
	VelocityZMPS      float64
	HeadingDeg        *float64
}

type GPSRawObservation struct {
	FixType             uint8
	LatitudeDeg         float64
	LongitudeDeg        float64
	AltitudeMSLM        float64
	EPH                 uint16
	EPV                 uint16
	GroundSpeedMPS      float64
	CourseOverGroundDeg *float64
	SatellitesVisible   uint8
}

type StatusTextObservation struct {
	Severity uint8
	Text     string
}

type CommandAckObservation struct {
	Command         uint16
	Result          uint8
	Progress        *uint8
	ResultParam2    *int32
	TargetSystem    *uint8
	TargetComponent *uint8
}

type MissionCurrentObservation struct {
	Sequence     uint16
	Total        *uint16
	MissionState *uint8
	MissionMode  *uint8
}
