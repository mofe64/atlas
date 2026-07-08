package models

import "time"

type TelemetryState string

const (
	TelemetryStateUnknown    TelemetryState = "unknown"
	TelemetryStateFresh      TelemetryState = "fresh"
	TelemetryStateStale      TelemetryState = "stale"
	TelemetryStateLost       TelemetryState = "lost"
	TelemetryStateConflicted TelemetryState = "conflicted"
)

const (
	TelemetryFreshWindow = 5 * time.Second
	TelemetryStaleWindow = 20 * time.Second
)

type TelemetrySnapshot struct {
	DroneID                   string
	VehicleAgentID            string
	ActiveTelemetryFeedID     string
	SourceCommunicationLinkID string
	ObservedAt                time.Time
	ReceivedAt                time.Time
	BatteryPercent            float64
	RelativeAltitudeM         float64
	AltitudeMSL               float64
	FlightMode                string
	Armed                     bool
	InAir                     bool
	Latitude                  float64
	Longitude                 float64
	RollDeg                   float64
	PitchDeg                  float64
	HeadingDeg                float64
	VelocityNorthMPS          float64
	VelocityEastMPS           float64
	VelocityDownMPS           float64
	GroundSpeedMPS            float64
	GPSFix                    string
	SatellitesVisible         int
	HomePositionSet           bool
	MissionCurrentItem        int
	MissionTotalItems         int
	SystemHealth              SystemHealth
	Source                    string
}

type TelemetryFeed struct {
	ID                  string
	DroneID             string
	SourceType          TelemetrySourceType
	SourceID            string
	CommunicationLinkID string
	Status              TelemetryFeedStatus
	Priority            int
	Freshness           TelemetryState
	LastTelemetryAt     time.Time
	LastSequence        int64
	MessageRateHz       float64
	FieldsAvailable     TelemetryFieldsAvailable
	StartedAt           time.Time
	EndedAt             time.Time
	LastError           string
}

type TelemetrySourceType string

const (
	TelemetrySourceAgentDirect      TelemetrySourceType = "AGENT_DIRECT"
	TelemetrySourceGroundBridgeSiK  TelemetrySourceType = "GROUND_BRIDGE_SIK"
	TelemetrySourceGroundBridgeHM30 TelemetrySourceType = "GROUND_BRIDGE_HM30"
	TelemetrySourceQGCObserver      TelemetrySourceType = "QGC_OBSERVER"
	TelemetrySourceSITL             TelemetrySourceType = "SITL"
	TelemetrySourceUnknown          TelemetrySourceType = "UNKNOWN"
)

type TelemetryFeedStatus string

const (
	TelemetryFeedStatusUnknown    TelemetryFeedStatus = "UNKNOWN"
	TelemetryFeedStatusActive     TelemetryFeedStatus = "ACTIVE"
	TelemetryFeedStatusDegraded   TelemetryFeedStatus = "DEGRADED"
	TelemetryFeedStatusStale      TelemetryFeedStatus = "STALE"
	TelemetryFeedStatusLost       TelemetryFeedStatus = "LOST"
	TelemetryFeedStatusEnded      TelemetryFeedStatus = "ENDED"
	TelemetryFeedStatusConflicted TelemetryFeedStatus = "CONFLICTED"
)

type TelemetryFieldsAvailable struct {
	Position        bool
	Altitude        bool
	Heading         bool
	Attitude        bool
	Velocity        bool
	Battery         bool
	Armed           bool
	FlightMode      bool
	GPSHealth       bool
	HomePosition    bool
	MissionProgress bool
	SystemHealth    bool
}

type TelemetrySample struct {
	ID                 string
	DroneID            string
	MissionExecutionID string
	TelemetryFeedID    string
	Timestamp          time.Time
	Snapshot           TelemetrySnapshot
}

type SystemHealth struct {
	IsGlobalPositionOK bool
	IsHomePositionOK   bool
	IsArmable          bool
	BatteryWarning     string
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
