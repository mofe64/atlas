package dtos

type DroneResponse struct {
	ID               string                       `json:"id"`
	Name             string                       `json:"name"`
	VehicleAgentID   string                       `json:"vehicleAgentId"`
	Status           string                       `json:"status"`
	LastSeenAt       string                       `json:"lastSeenAt"`
	LastHeartbeatAt  string                       `json:"lastHeartbeatAt,omitempty"`
	MAVLinkObserver  map[string]any               `json:"mavlinkObserver,omitempty"`
	BackendChannel   map[string]any               `json:"backendChannel,omitempty"`
	Telemetry        *TelemetrySnapshotResponse   `json:"telemetry,omitempty"`
	CommandChannel   CommandChannelResponse       `json:"commandChannel"`
	Communication    CommunicationSummaryResponse `json:"communication"`
	VehicleActions   []VehicleActionResponse      `json:"vehicleActions"`
	MissionExecution *MissionExecutionResponse    `json:"missionExecution,omitempty"`
}
