package dtos

type CommandChannelResponse struct {
	State              string `json:"state"`
	ConnectedAt        string `json:"connectedAt,omitempty"`
	LastDisconnectedAt string `json:"lastDisconnectedAt,omitempty"`
}

type VehicleActionResponse struct {
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
	IdempotencyKey     string `json:"idempotencyKey,omitempty"`
	AckCorrelationID   string `json:"ackCorrelationId,omitempty"`
	RawAckCode         string `json:"rawAckCode,omitempty"`
	PolicyReason       string `json:"policyReason,omitempty"`
	ResultMessage      string `json:"resultMessage,omitempty"`
	TelemetryState     string `json:"telemetryState"`
	VehicleAgentStatus string `json:"vehicleAgentStatus"`
}

type VehicleActionStatusRequest struct {
	State            string         `json:"state"`
	ResultMessage    string         `json:"resultMessage"`
	AckCorrelationID string         `json:"ackCorrelationId"`
	RawAckCode       string         `json:"rawAckCode"`
	Evidence         map[string]any `json:"evidence,omitempty"`
}

type VehicleActionEventResponse struct {
	ID                  string         `json:"id"`
	VehicleActionID     string         `json:"vehicleActionId"`
	DroneID             string         `json:"droneId"`
	VehicleAgentID      string         `json:"vehicleAgentId"`
	Type                string         `json:"type"`
	State               string         `json:"state"`
	Message             string         `json:"message"`
	Source              string         `json:"source"`
	RawAckCode          string         `json:"rawAckCode,omitempty"`
	Evidence            map[string]any `json:"evidence,omitempty"`
	TelemetrySnapshotID string         `json:"telemetrySnapshotId,omitempty"`
	CreatedAt           string         `json:"createdAt"`
}
