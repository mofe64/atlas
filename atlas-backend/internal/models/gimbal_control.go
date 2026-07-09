package models

type GimbalControlCommand struct {
	DroneID           string
	PitchRateDegS     float64
	YawRateDegS       float64
	TargetSystemID    uint8
	TargetComponentID uint8
	GimbalDeviceID    uint8
	RequestedBy       string
}
