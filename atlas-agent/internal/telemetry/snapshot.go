// Package telemetry defines flight-controller telemetry independently of any
// transport or MAVSDK-generated types.
package telemetry

import "time"

// Snapshot is the latest coherent set of values observed from the flight
// controller. Pointer fields distinguish an unreported value from a real zero.
type Snapshot struct {
	ObservedAt        time.Time
	Source            string
	BatteryPercent    *float64
	RelativeAltitudeM *float64
	FlightMode        *string
	Armed             *bool
	InAir             *bool
	Latitude          *float64
	Longitude         *float64
	HeadingDeg        *float64
	GroundSpeedMPS    *float64
	GPSFix            *string
	SatellitesVisible *uint32
	HomePositionSet   *bool
	Batteries         []Battery
	Health            *VehicleHealth
	AbsoluteAltitudeM *float64
	TerrainAltitudeM  *float64
	BottomClearanceM  *float64
	VelocityNorthMPS  *float64
	VelocityEastMPS   *float64
	VelocityDownMPS   *float64
	ClimbRateMPS      *float64
	LandedState       *string
	RCStatus          *RCStatus
	HomePosition      *HomePosition
	GPSQuality        *GPSQuality
}

type Battery struct {
	ID               uint32
	Function         string
	RemainingPercent *float64
	VoltageV         *float64
	CurrentA         *float64
	TemperatureC     *float64
	ConsumedAH       *float64
	TimeRemainingS   *float64
}

type VehicleHealth struct {
	GyrometerCalibrationOK     bool
	AccelerometerCalibrationOK bool
	MagnetometerCalibrationOK  bool
	LocalPositionOK            bool
	GlobalPositionOK           bool
	HomePositionOK             bool
	Armable                    bool
}

type RCStatus struct {
	Available             bool
	WasAvailableOnce      bool
	SignalStrengthPercent *float64
}

type HomePosition struct {
	Latitude          *float64
	Longitude         *float64
	AbsoluteAltitudeM *float64
	RelativeAltitudeM *float64
}

type GPSQuality struct {
	HDOP                    *float64
	VDOP                    *float64
	HorizontalUncertaintyM  *float64
	VerticalUncertaintyM    *float64
	VelocityUncertaintyMPS  *float64
	CourseOverGroundDegrees *float64
}

type StatusTextEvent struct {
	ObservedAt time.Time
	Source     string
	Severity   string
	Text       string
}
