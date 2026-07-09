package dtos

type TelemetryRequest struct {
	ObservedAt        string  `json:"observedAt"`
	BatteryPercent    float64 `json:"batteryPercent"`
	RelativeAltitudeM float64 `json:"relativeAltitudeM"`
	FlightMode        string  `json:"flightMode"`
	Armed             bool    `json:"armed"`
	InAir             bool    `json:"inAir"`
	Latitude          float64 `json:"latitude"`
	Longitude         float64 `json:"longitude"`
	HeadingDeg        float64 `json:"headingDeg"`
	GroundSpeedMPS    float64 `json:"groundSpeedMPS"`
	GPSFix            string  `json:"gpsFix"`
	SatellitesVisible int     `json:"satellitesVisible"`
	HomePositionSet   bool    `json:"homePositionSet"`
	Source            string  `json:"source"`
}

type TelemetryResponse struct {
	DroneID                   string `json:"droneId"`
	VehicleAgentID            string `json:"vehicleAgentId"`
	ActiveTelemetryFeedID     string `json:"activeTelemetryFeedId,omitempty"`
	SourceCommunicationLinkID string `json:"sourceCommunicationLinkId,omitempty"`
	TelemetryState            string `json:"telemetryState"`
	ReceivedAt                string `json:"receivedAt"`
}

type TelemetrySnapshotResponse struct {
	State                     string  `json:"state"`
	ActiveTelemetryFeedID     string  `json:"activeTelemetryFeedId,omitempty"`
	SourceCommunicationLinkID string  `json:"sourceCommunicationLinkId,omitempty"`
	ObservedAt                string  `json:"observedAt"`
	ReceivedAt                string  `json:"receivedAt"`
	BatteryPercent            float64 `json:"batteryPercent"`
	RelativeAltitudeM         float64 `json:"relativeAltitudeM"`
	FlightMode                string  `json:"flightMode"`
	Armed                     bool    `json:"armed"`
	InAir                     bool    `json:"inAir"`
	Latitude                  float64 `json:"latitude"`
	Longitude                 float64 `json:"longitude"`
	HeadingDeg                float64 `json:"headingDeg"`
	GroundSpeedMPS            float64 `json:"groundSpeedMPS"`
	GPSFix                    string  `json:"gpsFix"`
	SatellitesVisible         int     `json:"satellitesVisible"`
	HomePositionSet           bool    `json:"homePositionSet"`
	Source                    string  `json:"source"`
}

type TelemetryFeedResponse struct {
	ID                  string                           `json:"id"`
	DroneID             string                           `json:"droneId"`
	SourceType          string                           `json:"sourceType"`
	SourceID            string                           `json:"sourceId"`
	CommunicationLinkID string                           `json:"communicationLinkId,omitempty"`
	Status              string                           `json:"status"`
	Priority            int                              `json:"priority"`
	Freshness           string                           `json:"freshness"`
	LastTelemetryAt     string                           `json:"lastTelemetryAt,omitempty"`
	LastSequence        int64                            `json:"lastSequence,omitempty"`
	MessageRateHz       float64                          `json:"messageRateHz,omitempty"`
	FieldsAvailable     TelemetryFieldsAvailableResponse `json:"fieldsAvailable"`
	StartedAt           string                           `json:"startedAt"`
	EndedAt             string                           `json:"endedAt,omitempty"`
	LastError           string                           `json:"lastError,omitempty"`
}

type TelemetryFieldsAvailableResponse struct {
	Position        bool `json:"position"`
	Altitude        bool `json:"altitude"`
	Heading         bool `json:"heading"`
	Attitude        bool `json:"attitude"`
	Velocity        bool `json:"velocity"`
	Battery         bool `json:"battery"`
	Armed           bool `json:"armed"`
	FlightMode      bool `json:"flightMode"`
	GPSHealth       bool `json:"gpsHealth"`
	HomePosition    bool `json:"homePosition"`
	MissionProgress bool `json:"missionProgress"`
	SystemHealth    bool `json:"systemHealth"`
}
