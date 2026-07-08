package dtos

type DroneResponse struct {
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	VehicleAgentID   string                     `json:"vehicleAgentId"`
	Status           string                     `json:"status"`
	LastSeenAt       string                     `json:"lastSeenAt"`
	LastHeartbeatAt  string                     `json:"lastHeartbeatAt,omitempty"`
	Telemetry        *TelemetrySnapshotResponse `json:"telemetry,omitempty"`
	CommandChannel   CommandChannelResponse     `json:"commandChannel"`
	Commands         []CommandResponse          `json:"commands"`
	MissionExecution *MissionExecutionResponse  `json:"missionExecution,omitempty"`
}
