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
	DroneID        string `json:"droneId"`
	VehicleAgentID string `json:"vehicleAgentId"`
	TelemetryState string `json:"telemetryState"`
	ReceivedAt     string `json:"receivedAt"`
}

type TelemetrySnapshotResponse struct {
	State             string  `json:"state"`
	ObservedAt        string  `json:"observedAt"`
	ReceivedAt        string  `json:"receivedAt"`
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
