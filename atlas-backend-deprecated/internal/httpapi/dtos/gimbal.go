package dtos

type GimbalControlRequest struct {
	PitchRateDegS     float64 `json:"pitchRateDegS"`
	YawRateDegS       float64 `json:"yawRateDegS"`
	TargetSystemID    uint8   `json:"targetSystemId,omitempty"`
	TargetComponentID uint8   `json:"targetComponentId,omitempty"`
	GimbalDeviceID    uint8   `json:"gimbalDeviceId,omitempty"`
}

type GimbalControlResponse struct {
	Accepted bool   `json:"accepted"`
	State    string `json:"state"`
}
