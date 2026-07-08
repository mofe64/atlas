package models

import "time"

type CommandType string

const (
	CommandTypeArm            CommandType = "arm"
	CommandTypeDisarm         CommandType = "disarm"
	CommandTypeTakeoff        CommandType = "takeoff"
	CommandTypeReturnToLaunch CommandType = "return_to_launch"
	CommandTypeLand           CommandType = "land"
	CommandTypeHold           CommandType = "hold"
	CommandTypeUploadMission  CommandType = "upload_mission"
	CommandTypeStartMission   CommandType = "start_mission"
	CommandTypeAbortMission   CommandType = "abort_mission"
	CommandTypeSetFlightMode  CommandType = "set_flight_mode"
	CommandTypeGimbalControl  CommandType = "gimbal_control"
	CommandTypeCameraAction   CommandType = "camera_action"
)

type CommandState string

const (
	CommandStateRequested            CommandState = "requested"
	CommandStateAuthorized           CommandState = "authorized"
	CommandStateRejectedByPolicy     CommandState = "rejected_by_policy"
	CommandStateSentToVehicleAgent   CommandState = "sent_to_vehicle_agent"
	CommandStateVehicleAgentReceived CommandState = "vehicle_agent_received"
	CommandStateSentToVehicle        CommandState = "sent_to_vehicle"
	CommandStateVehicleAcked         CommandState = "vehicle_acked"
	CommandStateVehicleRejected      CommandState = "vehicle_rejected"
	CommandStateTelemetryConfirmed   CommandState = "telemetry_confirmed"
	CommandStateTimedOut             CommandState = "timed_out"
	CommandStateFailed               CommandState = "failed"
	CommandStateCancelledByOperator  CommandState = "cancelled_by_operator"
	CommandStateSuperseded           CommandState = "superseded"
)

const CommandDeliveryLeaseDuration = 10 * time.Second

type CommandRequest struct {
	ID                      string
	DroneID                 string
	VehicleAgentID          string
	MissionExecutionID      string
	Type                    CommandType
	Payload                 map[string]any
	State                   CommandState
	RequestedBy             string
	RequestedByOperatorID   string
	ControlSessionID        string
	TargetDroneConnectionID string
	DeliveryTarget          CommandDeliveryTarget
	RequiresConfirmation    bool
	RequestedAt             time.Time
	AuthorizedAt            time.Time
	SentToVehicleAgentAt    time.Time
	UpdatedAt               time.Time
	LastSentAt              time.Time
	LeaseUntil              time.Time
	VehicleAckedAt          time.Time
	CompletedAt             time.Time
	FailedAt                time.Time
	FailureReason           string
	IdempotencyKey          string
	ConfirmationBaseline    TelemetrySnapshot
	DeliveryAttempt         int
	PolicyReason            string
	ResultMessage           string
	TelemetryState          TelemetryState
	VehicleAgentStatus      VehicleAgentStatus
}

type CommandDeliveryTarget string

const (
	CommandDeliveryTargetVehicleAgent CommandDeliveryTarget = "VEHICLE_AGENT"
)

type CommandEvent struct {
	ID                  string
	CommandRequestID    string
	DroneID             string
	VehicleAgentID      string
	EventType           CommandEventType
	State               CommandState
	Source              string
	Message             string
	RawAckCode          string
	TelemetrySnapshotID string
	CreatedAt           time.Time
}

type CommandEventType string

const (
	CommandEventRequested                       CommandEventType = "REQUESTED"
	CommandEventAuthorized                      CommandEventType = "AUTHORIZED"
	CommandEventRejectedByPolicy                CommandEventType = "REJECTED_BY_POLICY"
	CommandEventRejectedStaleTelemetry          CommandEventType = "REJECTED_STALE_TELEMETRY"
	CommandEventRejectedNoControlSession        CommandEventType = "REJECTED_NO_CONTROL_SESSION"
	CommandEventRejectedNoDroneConnection       CommandEventType = "REJECTED_NO_DRONE_CONNECTION"
	CommandEventRejectedVehicleAgentUnavailable CommandEventType = "REJECTED_VEHICLE_AGENT_UNAVAILABLE"
	CommandEventDroneConnectionSelected         CommandEventType = "DRONE_CONNECTION_SELECTED"
	CommandEventSentToVehicleAgent              CommandEventType = "SENT_TO_VEHICLE_AGENT"
	CommandEventVehicleAgentReceived            CommandEventType = "VEHICLE_AGENT_RECEIVED"
	CommandEventSentToVehicle                   CommandEventType = "SENT_TO_VEHICLE"
	CommandEventVehicleAcked                    CommandEventType = "VEHICLE_ACKED"
	CommandEventVehicleRejected                 CommandEventType = "VEHICLE_REJECTED"
	CommandEventObservedInTelemetry             CommandEventType = "OBSERVED_IN_TELEMETRY"
	CommandEventAckedButNotObserved             CommandEventType = "ACKED_BUT_NOT_OBSERVED"
	CommandEventTimedOut                        CommandEventType = "TIMED_OUT"
	CommandEventFailedVehicleAgentError         CommandEventType = "FAILED_VEHICLE_AGENT_ERROR"
	CommandEventFailedVehicleAgentLinkLoss      CommandEventType = "FAILED_VEHICLE_AGENT_LINK_LOSS"
	CommandEventFailedUnknown                   CommandEventType = "FAILED_UNKNOWN"
	CommandEventCancelledByOperator             CommandEventType = "CANCELLED_BY_OPERATOR"
	CommandEventSuperseded                      CommandEventType = "SUPERSEDED"
)

type ControlSession struct {
	ID                      string
	DroneID                 string
	RequestedByOperatorID   string
	ApprovedByOperatorID    string
	State                   ControlSessionState
	AllowedCommandSet       []CommandType
	ActiveDroneConnectionID string
	StartedAt               time.Time
	ExpiresAt               time.Time
	EndedAt                 time.Time
	EndedReason             string
}

type ControlSessionState string

const (
	ControlSessionNotControlledByAtlas       ControlSessionState = "NOT_CONTROLLED_BY_ATLAS"
	ControlSessionAtlasObserving             ControlSessionState = "ATLAS_OBSERVING"
	ControlSessionRequested                  ControlSessionState = "ATLAS_CONTROL_SESSION_REQUESTED"
	ControlSessionActive                     ControlSessionState = "ATLAS_CONTROL_SESSION_ACTIVE"
	ControlSessionSuspendedStaleTelemetry    ControlSessionState = "SUSPENDED_STALE_TELEMETRY"
	ControlSessionSuspendedLinkLoss          ControlSessionState = "SUSPENDED_LINK_LOSS"
	ControlSessionSuspendedAuthorityConflict ControlSessionState = "SUSPENDED_AUTHORITY_CONFLICT"
	ControlSessionEndedByOperator            ControlSessionState = "ENDED_BY_OPERATOR"
	ControlSessionEndedByTimeout             ControlSessionState = "ENDED_BY_TIMEOUT"
	ControlSessionEndedByManualOverride      ControlSessionState = "ENDED_BY_MANUAL_OVERRIDE"
	ControlSessionAuthorityUnknown           ControlSessionState = "CONTROL_AUTHORITY_UNKNOWN"
)
