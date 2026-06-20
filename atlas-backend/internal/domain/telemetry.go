package domain

import "time"

type TelemetryState string

const (
	TelemetryStateUnknown TelemetryState = "unknown"
	TelemetryStateFresh   TelemetryState = "fresh"
	TelemetryStateStale   TelemetryState = "stale"
	TelemetryStateLost    TelemetryState = "lost"
)

const (
	TelemetryFreshWindow = 5 * time.Second
	TelemetryStaleWindow = 20 * time.Second
)

type TelemetrySnapshot struct {
	DroneID           string
	AgentID           string
	ObservedAt        time.Time
	ReceivedAt        time.Time
	BatteryPercent    float64
	RelativeAltitudeM float64
	FlightMode        string
	Armed             bool
	InAir             bool
	Latitude          float64
	Longitude         float64
	HeadingDeg        float64
	GPSFix            string
	SatellitesVisible int
	HomePositionSet   bool
	Source            string
}

func TelemetryStateFromReceivedAt(receivedAt time.Time, now time.Time) TelemetryState {
	if receivedAt.IsZero() {
		return TelemetryStateUnknown
	}

	age := now.Sub(receivedAt)
	if age <= TelemetryFreshWindow {
		return TelemetryStateFresh
	}

	if age <= TelemetryStaleWindow {
		return TelemetryStateStale
	}

	return TelemetryStateLost
}
