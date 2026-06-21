package domain

import "time"

type CommandType string

const (
	CommandTypeArm            CommandType = "arm"
	CommandTypeTakeoff        CommandType = "takeoff"
	CommandTypeReturnToLaunch CommandType = "return_to_launch"
	CommandTypeLand           CommandType = "land"
)

type CommandState string

const (
	CommandStateRequested          CommandState = "requested"
	CommandStateAuthorized         CommandState = "authorized"
	CommandStateRejectedByPolicy   CommandState = "rejected_by_policy"
	CommandStateSentToAgent        CommandState = "sent_to_agent"
	CommandStateAgentReceived      CommandState = "agent_received"
	CommandStateSentToVehicle      CommandState = "sent_to_vehicle"
	CommandStateVehicleAcked       CommandState = "vehicle_acked"
	CommandStateVehicleRejected    CommandState = "vehicle_rejected"
	CommandStateTelemetryConfirmed CommandState = "telemetry_confirmed"
	CommandStateTimedOut           CommandState = "timed_out"
	CommandStateFailed             CommandState = "failed"
)

const CommandDeliveryLeaseDuration = 10 * time.Second

type OperatorCommand struct {
	ID                   string
	DroneID              string
	AgentID              string
	Type                 CommandType
	State                CommandState
	RequestedBy          string
	RequestedAt          time.Time
	UpdatedAt            time.Time
	LastSentAt           time.Time
	LeaseUntil           time.Time
	VehicleAckedAt       time.Time
	ConfirmationBaseline TelemetrySnapshot
	DeliveryAttempt      int
	PolicyReason         string
	ResultMessage        string
	TelemetryState       TelemetryState
	AgentStatus          AgentStatus
}
