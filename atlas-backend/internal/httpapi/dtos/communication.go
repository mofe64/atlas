package dtos

type CommunicationSummaryResponse struct {
	CommandLinkStatus     string `json:"commandLinkStatus"`
	ActiveCommandLinkID   string `json:"activeCommandLinkId,omitempty"`
	ActiveTelemetryLinkID string `json:"activeTelemetryLinkId,omitempty"`
	ActiveLinkCount       int    `json:"activeLinkCount"`
	DegradedLinkCount     int    `json:"degradedLinkCount"`
	LostLinkCount         int    `json:"lostLinkCount"`
}

type CommunicationLinkResponse struct {
	ID                            string   `json:"id"`
	DroneID                       string   `json:"droneId"`
	VehicleAgentID                string   `json:"vehicleAgentId,omitempty"`
	DroneVehicleAgentConnectionID string   `json:"droneVehicleAgentConnectionId,omitempty"`
	LinkType                      string   `json:"linkType"`
	Roles                         []string `json:"roles"`
	Status                        string   `json:"status"`
	Transport                     string   `json:"transport"`
	EndpointDescription           string   `json:"endpointDescription"`
	CommandEligible               bool     `json:"commandEligible"`
	LatencyMs                     float64  `json:"latencyMs,omitempty"`
	PacketLossEstimate            float64  `json:"packetLossEstimate,omitempty"`
	RxBytesPerSec                 float64  `json:"rxBytesPerSec,omitempty"`
	TxBytesPerSec                 float64  `json:"txBytesPerSec,omitempty"`
	LastSeenAt                    string   `json:"lastSeenAt,omitempty"`
	CreatedAt                     string   `json:"createdAt"`
	EndedAt                       string   `json:"endedAt,omitempty"`
	EndedReason                   string   `json:"endedReason,omitempty"`
}
