package dtos

type RegisterVehicleAgentRequest struct {
	VehicleAgentID      string `json:"vehicleAgentId"`
	DroneID             string `json:"droneId"`
	DroneName           string `json:"droneName"`
	VehicleAgentVersion string `json:"vehicleAgentVersion"`
}

type RegisterVehicleAgentResponse struct {
	VehicleAgentID           string `json:"vehicleAgentId"`
	DroneID                  string `json:"droneId"`
	Status                   string `json:"status"`
	HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
}

type HeartbeatRequest struct {
	VehicleAgentVersion string `json:"vehicleAgentVersion"`
}

type HeartbeatResponse struct {
	VehicleAgentID     string `json:"vehicleAgentId"`
	DroneID            string `json:"droneId"`
	Status             string `json:"status"`
	LastHeartbeatAt    string `json:"lastHeartbeatAt"`
	NextHeartbeatAfter int    `json:"nextHeartbeatAfterSeconds"`
}
