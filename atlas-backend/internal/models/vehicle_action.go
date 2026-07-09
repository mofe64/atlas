package models

import "time"

type VehicleActionType string

const (
	VehicleActionTypeArm            VehicleActionType = "arm"
	VehicleActionTypeDisarm         VehicleActionType = "disarm"
	VehicleActionTypeTakeoff        VehicleActionType = "takeoff"
	VehicleActionTypeReturnToLaunch VehicleActionType = "return_to_launch"
	VehicleActionTypeLand           VehicleActionType = "land"
	VehicleActionTypeHold           VehicleActionType = "hold"
	VehicleActionTypeUploadMission  VehicleActionType = "upload_mission"
	VehicleActionTypeStartMission   VehicleActionType = "start_mission"
	VehicleActionTypeAbortMission   VehicleActionType = "abort_mission"
	VehicleActionTypeSetFlightMode  VehicleActionType = "set_flight_mode"
	VehicleActionTypeGimbalControl  VehicleActionType = "gimbal_control"
	VehicleActionTypeCameraAction   VehicleActionType = "camera_action"
)

type VehicleActionState string

const (
	VehicleActionStateRequested            VehicleActionState = "requested"
	VehicleActionStateAuthorized           VehicleActionState = "authorized"
	VehicleActionStateRejectedByPolicy     VehicleActionState = "rejected_by_policy"
	VehicleActionStateSentToVehicleAgent   VehicleActionState = "sent_to_vehicle_agent"
	VehicleActionStateVehicleAgentReceived VehicleActionState = "vehicle_agent_received"
	VehicleActionStateSentToVehicle        VehicleActionState = "sent_to_vehicle"
	VehicleActionStateVehicleAcked         VehicleActionState = "vehicle_acked"
	VehicleActionStateVehicleRejected      VehicleActionState = "vehicle_rejected"
	VehicleActionStateTelemetryConfirmed   VehicleActionState = "telemetry_confirmed"
	VehicleActionStateAckedButNotObserved  VehicleActionState = "acked_but_not_observed"
	VehicleActionStateTimedOut             VehicleActionState = "timed_out"
	VehicleActionStateFailed               VehicleActionState = "failed"
	VehicleActionStateCancelledByOperator  VehicleActionState = "cancelled_by_operator"
	VehicleActionStateSuperseded           VehicleActionState = "superseded"
)

const VehicleActionDeliveryLeaseDuration = 10 * time.Second
const VehicleActionAuthorizationTimeout = 30 * time.Second
const VehicleActionExecutionTimeout = 30 * time.Second
const VehicleActionObservationTimeout = 30 * time.Second
const VehicleActionMaxDeliveryAttempts = 3

type VehicleAction struct {
	ID                                  string
	DroneID                             string
	VehicleAgentID                      string
	MissionExecutionID                  string
	Type                                VehicleActionType
	Payload                             map[string]any
	State                               VehicleActionState
	RequestedBy                         string
	RequestedByOperatorID               string
	TargetDroneVehicleAgentConnectionID string
	DeliveryTarget                      VehicleActionDeliveryTarget
	RequiresConfirmation                bool
	RequestedAt                         time.Time
	AuthorizedAt                        time.Time
	SentToVehicleAgentAt                time.Time
	UpdatedAt                           time.Time
	LastSentAt                          time.Time
	LeaseUntil                          time.Time
	VehicleAckedAt                      time.Time
	CompletedAt                         time.Time
	FailedAt                            time.Time
	FailureReason                       string
	IdempotencyKey                      string
	AckCorrelationID                    string
	RawAckCode                          string
	ConfirmationBaseline                TelemetrySnapshot
	DeliveryAttempt                     int
	PolicyReason                        string
	ResultMessage                       string
	TelemetryState                      TelemetryState
	VehicleAgentStatus                  VehicleAgentStatus
}

type VehicleActionDeliveryTarget string

const (
	VehicleActionDeliveryTargetVehicleAgent VehicleActionDeliveryTarget = "VEHICLE_AGENT"
)

type VehicleActionEvent struct {
	ID                  string
	VehicleActionID     string
	DroneID             string
	VehicleAgentID      string
	EventType           VehicleActionEventType
	State               VehicleActionState
	Source              string
	Message             string
	RawAckCode          string
	Evidence            map[string]any
	TelemetrySnapshotID string
	CreatedAt           time.Time
}

type VehicleActionEventType string

const (
	VehicleActionEventRequested                             VehicleActionEventType = "REQUESTED"
	VehicleActionEventAuthorized                            VehicleActionEventType = "AUTHORIZED"
	VehicleActionEventRejectedByPolicy                      VehicleActionEventType = "REJECTED_BY_POLICY"
	VehicleActionEventRejectedStaleTelemetry                VehicleActionEventType = "REJECTED_STALE_TELEMETRY"
	VehicleActionEventRejectedNoDroneVehicleAgentConnection VehicleActionEventType = "REJECTED_NO_DRONE_VEHICLE_AGENT_CONNECTION"
	VehicleActionEventRejectedVehicleAgentUnavailable       VehicleActionEventType = "REJECTED_VEHICLE_AGENT_UNAVAILABLE"
	VehicleActionEventDroneVehicleAgentConnectionSelected   VehicleActionEventType = "DRONE_VEHICLE_AGENT_CONNECTION_SELECTED"
	VehicleActionEventSentToVehicleAgent                    VehicleActionEventType = "SENT_TO_VEHICLE_AGENT"
	VehicleActionEventVehicleAgentReceived                  VehicleActionEventType = "VEHICLE_AGENT_RECEIVED"
	VehicleActionEventSentToVehicle                         VehicleActionEventType = "SENT_TO_VEHICLE"
	VehicleActionEventVehicleAcked                          VehicleActionEventType = "VEHICLE_ACKED"
	VehicleActionEventVehicleRejected                       VehicleActionEventType = "VEHICLE_REJECTED"
	VehicleActionEventObservedInTelemetry                   VehicleActionEventType = "OBSERVED_IN_TELEMETRY"
	VehicleActionEventAckedButNotObserved                   VehicleActionEventType = "ACKED_BUT_NOT_OBSERVED"
	VehicleActionEventTimedOut                              VehicleActionEventType = "TIMED_OUT"
	VehicleActionEventFailedVehicleAgentError               VehicleActionEventType = "FAILED_VEHICLE_AGENT_ERROR"
	VehicleActionEventFailedVehicleAgentLinkLoss            VehicleActionEventType = "FAILED_VEHICLE_AGENT_LINK_LOSS"
	VehicleActionEventFailedUnknown                         VehicleActionEventType = "FAILED_UNKNOWN"
	VehicleActionEventCancelledByOperator                   VehicleActionEventType = "CANCELLED_BY_OPERATOR"
	VehicleActionEventSuperseded                            VehicleActionEventType = "SUPERSEDED"
)
