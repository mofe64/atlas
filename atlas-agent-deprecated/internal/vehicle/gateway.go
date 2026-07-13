package vehicle

import (
	"context"
	"time"
)

type TelemetryEvent struct {
	ObservedAt        time.Time
	BatteryPercent    float64
	RelativeAltitudeM float64
	FlightMode        string
	Armed             bool
	InAir             bool
	Latitude          float64
	Longitude         float64
	HeadingDeg        float64
	GroundSpeedMPS    float64
	GPSFix            string
	SatellitesVisible int
	HomePositionSet   bool
	Source            string
}

type MissionPlan struct {
	Waypoints        []MissionWaypoint
	CompletionAction MissionCompletionAction
}

type MissionProgressEvent struct {
	Current  int
	Total    int
	Finished bool
}

type MissionCompletionAction string

const (
	MissionCompletionActionHold           MissionCompletionAction = "hold"
	MissionCompletionActionReturnToLaunch MissionCompletionAction = "return_to_launch"
	MissionCompletionActionLand           MissionCompletionAction = "land"
)

type MissionWaypoint struct {
	Sequence          int
	Latitude          float64
	Longitude         float64
	RelativeAltitudeM float64
	SpeedMPS          *float64
	LoiterTimeS       *float64
}

type Gateway interface {
	Telemetry(ctx context.Context) (<-chan TelemetryEvent, error)
	Arm(ctx context.Context) error
	Takeoff(ctx context.Context) error
	ReturnToLaunch(ctx context.Context) error
	Land(ctx context.Context) error
	UploadMission(ctx context.Context, mission MissionPlan) error
	PrepareMissionStart(ctx context.Context) error
	StartMission(ctx context.Context) error
	MissionProgress(ctx context.Context) (<-chan MissionProgressEvent, error)
}
