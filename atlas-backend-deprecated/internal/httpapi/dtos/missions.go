package dtos

type CreateMissionRequest struct {
	Name             string                   `json:"name"`
	CompletionAction string                   `json:"completionAction,omitempty"`
	Waypoints        []MissionWaypointRequest `json:"waypoints"`
}

type MissionWaypointRequest struct {
	Latitude          float64  `json:"latitude"`
	Longitude         float64  `json:"longitude"`
	RelativeAltitudeM float64  `json:"relativeAltitudeM"`
	SpeedMPS          *float64 `json:"speedMPS,omitempty"`
	LoiterTimeS       *float64 `json:"loiterTimeS,omitempty"`
}

type MissionResponse struct {
	ID               string                    `json:"id"`
	DroneID          string                    `json:"droneId"`
	CurrentVersionID string                    `json:"currentVersionId,omitempty"`
	Name             string                    `json:"name"`
	CreatedBy        string                    `json:"createdBy"`
	CreatedAt        string                    `json:"createdAt"`
	UpdatedAt        string                    `json:"updatedAt"`
	CompletionAction string                    `json:"completionAction"`
	ValidationStatus string                    `json:"validationStatus"`
	ValidationErrors []MissionValidationError  `json:"validationErrors,omitempty"`
	Waypoints        []MissionWaypointResponse `json:"waypoints"`
}

type MissionWaypointResponse struct {
	Sequence          int      `json:"sequence"`
	Latitude          float64  `json:"latitude"`
	Longitude         float64  `json:"longitude"`
	RelativeAltitudeM float64  `json:"relativeAltitudeM"`
	SpeedMPS          *float64 `json:"speedMPS,omitempty"`
	LoiterTimeS       *float64 `json:"loiterTimeS,omitempty"`
}

type MissionValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type MissionExecutionResponse struct {
	ID                 string `json:"id"`
	MissionID          string `json:"missionId"`
	MissionVersionID   string `json:"missionVersionId"`
	DroneID            string `json:"droneId"`
	VehicleAgentID     string `json:"vehicleAgentId"`
	RequestedBy        string `json:"requestedBy"`
	UploadRequestedBy  string `json:"uploadRequestedBy,omitempty"`
	StartRequestedBy   string `json:"startRequestedBy,omitempty"`
	State              string `json:"state"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
	LastSentAt         string `json:"lastSentAt,omitempty"`
	LeaseUntil         string `json:"leaseUntil,omitempty"`
	UploadRequestedAt  string `json:"uploadRequestedAt,omitempty"`
	UploadedAt         string `json:"uploadedAt,omitempty"`
	StartRequestedAt   string `json:"startRequestedAt,omitempty"`
	StartedAt          string `json:"startedAt,omitempty"`
	CompletedAt        string `json:"completedAt,omitempty"`
	HoldAt             string `json:"holdAt,omitempty"`
	FailedAt           string `json:"failedAt,omitempty"`
	CurrentMissionItem int    `json:"currentMissionItem,omitempty"`
	TotalMissionItems  int    `json:"totalMissionItems,omitempty"`
	ProgressUpdatedAt  string `json:"progressUpdatedAt,omitempty"`
	DeliveryAttempt    int    `json:"deliveryAttempt"`
	ResultMessage      string `json:"resultMessage,omitempty"`
}

type MissionExecutionEventResponse struct {
	ID                 string `json:"id"`
	ExecutionID        string `json:"executionId"`
	MissionID          string `json:"missionId"`
	MissionVersionID   string `json:"missionVersionId,omitempty"`
	DroneID            string `json:"droneId"`
	VehicleAgentID     string `json:"vehicleAgentId"`
	Type               string `json:"type"`
	State              string `json:"state"`
	Message            string `json:"message"`
	CurrentMissionItem int    `json:"currentMissionItem,omitempty"`
	TotalMissionItems  int    `json:"totalMissionItems,omitempty"`
	Source             string `json:"source"`
	CreatedAt          string `json:"createdAt"`
}

type MissionDetailResponse struct {
	Mission    MissionResponse            `json:"mission"`
	Executions []MissionExecutionResponse `json:"executions"`
}

type MissionStreamEventResponse struct {
	Type   string                `json:"type"`
	Detail MissionDetailResponse `json:"detail"`
}
