package dtos

type CommandChannelResponse struct {
	State              string `json:"state"`
	ConnectedAt        string `json:"connectedAt,omitempty"`
	LastDisconnectedAt string `json:"lastDisconnectedAt,omitempty"`
}

type CommandResponse struct {
	ID                 string `json:"id"`
	DroneID            string `json:"droneId"`
	VehicleAgentID     string `json:"vehicleAgentId"`
	Type               string `json:"type"`
	State              string `json:"state"`
	RequestedBy        string `json:"requestedBy"`
	RequestedAt        string `json:"requestedAt"`
	UpdatedAt          string `json:"updatedAt"`
	LastSentAt         string `json:"lastSentAt,omitempty"`
	LeaseUntil         string `json:"leaseUntil,omitempty"`
	VehicleAckedAt     string `json:"vehicleAckedAt,omitempty"`
	DeliveryAttempt    int    `json:"deliveryAttempt"`
	PolicyReason       string `json:"policyReason,omitempty"`
	ResultMessage      string `json:"resultMessage,omitempty"`
	TelemetryState     string `json:"telemetryState"`
	VehicleAgentStatus string `json:"vehicleAgentStatus"`
}

type CommandStatusRequest struct {
	State         string `json:"state"`
	ResultMessage string `json:"resultMessage"`
}
