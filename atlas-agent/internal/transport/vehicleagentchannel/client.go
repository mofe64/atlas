// Package vehicleagentchannel implements the onboard agent side of the
// long-lived gRPC stream to the Atlas backend.
package vehicleagentchannel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/comms"
	"github.com/sunnyside/atlas/atlas-agent/internal/mavlinkobserver"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
	pb "github.com/sunnyside/atlas/atlas-agent/internal/transport/vehicleagentchannelpb/atlas"
	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	vehicleActionTypeArm            = "arm"
	vehicleActionTypeTakeoff        = "takeoff"
	vehicleActionTypeReturnToLaunch = "return_to_launch"
	vehicleActionTypeLand           = "land"

	vehicleActionStateVehicleAgentReceived = "vehicle_agent_received"
	vehicleActionStateSentToVehicle        = "sent_to_vehicle"
	vehicleActionStateVehicleAcked         = "vehicle_acked"
	vehicleActionStateVehicleRejected      = "vehicle_rejected"
	vehicleActionStateFailed               = "failed"

	mavCmdNavReturnToLaunch      uint16 = 20
	mavCmdNavLand                uint16 = 21
	mavCmdNavTakeoff             uint16 = 22
	mavCmdComponentArmDisarm     uint16 = 400
	commandAckObservationSlack          = 500 * time.Millisecond
	commandAckObservationTimeout        = 2 * time.Second

	missionActionUpload = "upload"
	missionActionStart  = "start"
	missionActionRTL    = "return_to_launch"

	missionExecutionStateUploading         = "uploading"
	missionExecutionStateUploadedToVehicle = "uploaded_to_vehicle"
	missionExecutionStateUploadFailed      = "upload_failed"
	missionExecutionStateActive            = "active"
	missionExecutionStateRTLRequested      = "rtl_requested"
	missionExecutionStateCompleted         = "completed"
	missionExecutionStateHold              = "hold"
	missionExecutionStateFailed            = "failed"
)

var errUnsupportedVehicleAction = errors.New("unsupported vehicle action type")

// Config contains the identity, timing, and retry settings needed to keep the
// vehicle-agent channel connected to the backend.
type Config struct {
	Addr                string
	VehicleAgentID      string
	DroneID             string
	DroneName           string
	VehicleAgentVersion string
	HeartbeatInterval   time.Duration
	TelemetryInterval   time.Duration
	CommandTimeout      time.Duration
	RetryMin            time.Duration
	RetryMax            time.Duration
}

type MAVLinkObserver interface {
	WaitForCommandAck(context.Context, mavlinkobserver.CommandAckMatch) (mavlinkobserver.CommandAckEvidence, bool)
	SnapshotDiagnostics() mavlinkobserver.Diagnostics
	PreferredCommandAckSource() (uint8, uint8, bool)
}

type GimbalController interface {
	SendGimbalControl(context.Context, mavlinkobserver.GimbalControlCommand) error
}

// vehicleActionOutcome remembers the final state sent for a vehicle action during this
// process lifetime so duplicate deliveries can replay the same result.
type vehicleActionOutcome struct {
	state         string
	resultMessage string
	rawAckCode    string
	rawAck        *pb.RawMavlinkCommandAckEvidence
}

// missionExecutionOutcome remembers the final state sent for a mission action
// during this process lifetime so duplicate deliveries are idempotent.
type missionExecutionOutcome struct {
	state         string
	resultMessage string
}

// outboundQueues separates message priorities. Vehicle action and mission status are
// critical, heartbeats are useful liveness signals, and telemetry is allowed to
// drop old samples under backpressure.
type outboundQueues struct {
	critical   chan *pb.VehicleAgentToBackend
	heartbeat  chan *pb.VehicleAgentToBackend
	telemetry  chan *pb.VehicleAgentToBackend
	perception chan *pb.VehicleAgentToBackend
}

// newOutboundQueues sizes each queue according to how much loss or delay Atlas
// can tolerate for that message class.
func newOutboundQueues() outboundQueues {
	return outboundQueues{
		critical:   make(chan *pb.VehicleAgentToBackend, 16),
		heartbeat:  make(chan *pb.VehicleAgentToBackend, 2),
		telemetry:  make(chan *pb.VehicleAgentToBackend, 1),
		perception: make(chan *pb.VehicleAgentToBackend, 8),
	}
}

// Run keeps the gRPC channel alive until ctx is cancelled. Each disconnect
// returns from connectOnce and is retried with bounded exponential backoff.
func Run(ctx context.Context, logger *slog.Logger, cfg Config, gateway vehicle.Gateway, telemetrySource telemetry.Source, observer MAVLinkObserver, gimbalController GimbalController, perceptionSource perception.Source) {
	if logger == nil {
		logger = slog.Default()
	}

	backoff := cfg.RetryMin
	if backoff == 0 {
		backoff = time.Second
	}
	if cfg.RetryMax == 0 {
		cfg.RetryMax = 30 * time.Second
	}
	channelHealth := comms.NewBackendChannelManager(cfg.Addr)

	for ctx.Err() == nil {
		channelHealth.MarkConnecting(time.Now().UTC())
		err := connectOnce(ctx, logger, cfg, gateway, telemetrySource, observer, gimbalController, perceptionSource, channelHealth)
		if ctx.Err() != nil {
			return
		}

		channelHealth.MarkDisconnected(time.Now().UTC(), err)
		logger.Warn("vehicle-agent gRPC channel disconnected; retrying", "error", err, "retry_after", backoff.String())
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		backoff = nextBackoff(backoff, cfg.RetryMax)
	}
}

// connectOnce establishes one stream session, sends the required hello message,
// starts sender/receiver goroutines, and processes backend instructions until
// the stream fails or the context is cancelled.
func connectOnce(ctx context.Context, logger *slog.Logger, cfg Config, gateway vehicle.Gateway, telemetrySource telemetry.Source, observer MAVLinkObserver, gimbalController GimbalController, perceptionSource perception.Source, channelHealth *comms.BackendChannelManager) error {
	conn, err := grpc.NewClient(cfg.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("create vehicle-agent channel client: %w", err)
	}
	defer conn.Close()

	client := pb.NewVehicleAgentChannelServiceClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		return fmt.Errorf("connect vehicle-agent channel: %w", err)
	}

	sendCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	outbound := newOutboundQueues()
	errs := make(chan error, 2)
	inbound := make(chan *pb.BackendToVehicleAgent, 16)

	if channelHealth != nil {
		channelHealth.MarkConnected(time.Now().UTC())
	}

	go sendLoop(sendCtx, stream, outbound, errs, channelHealth)
	go receiveLoop(stream, inbound, errs)

	// The backend requires hello as the first message so it can register this
	// agent and associate the stream with one drone before accepting updates.
	if !enqueueCritical(sendCtx, outbound, &pb.VehicleAgentToBackend{
		VehicleAgentId: cfg.VehicleAgentID,
		Payload: &pb.VehicleAgentToBackend_Hello{
			Hello: &pb.VehicleAgentHello{
				DroneId:             cfg.DroneID,
				VehicleAgentVersion: cfg.VehicleAgentVersion,
				DroneName:           cfg.DroneName,
			},
		},
	}) {
		return sendCtx.Err()
	}

	logger.Info("vehicle-agent gRPC channel connected", "addr", cfg.Addr, "vehicle_agent_id", cfg.VehicleAgentID)

	go sendHeartbeats(sendCtx, outbound, cfg, observer, channelHealth)
	if telemetrySource != nil {
		go sendTelemetrySnapshots(sendCtx, logger, outbound, cfg, telemetrySource)
	}
	if perceptionSource != nil {
		go sendPerceptionMetadata(sendCtx, logger, outbound, cfg, perceptionSource)
	}

	// TODO: Persist completed vehicle action IDs locally. If the agent process crashes
	// after executing an action but before reporting the final state, this
	// in-memory guard can forget the action and allow duplicate execution after
	// redelivery.
	processedVehicleActions := make(map[string]vehicleActionOutcome)
	processedMissionExecutions := make(map[string]missionExecutionOutcome)

	// The main session loop consumes backend-to-agent work. Vehicle action and mission
	// handling runs synchronously here so each item reports a clear ordered
	// status sequence back to the backend.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errs:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case msg := <-inbound:
			action := msg.GetVehicleAction()
			if action != nil {
				// Backend redelivery can happen after lease expiry or reconnect.
				// Replaying the prior result avoids executing the same vehicle
				// action twice within this process lifetime.
				if outcome, ok := processedVehicleActions[action.GetVehicleActionId()]; ok {
					logger.Warn("duplicate gRPC vehicle action received; replaying prior result", "vehicle_action_id", action.GetVehicleActionId(), "type", action.GetActionType())
					if !sendVehicleActionStatus(ctx, outbound, cfg.VehicleAgentID, action.GetVehicleActionId(), vehicleActionStateVehicleAgentReceived, "", action.GetAckCorrelationId(), "", nil) {
						return ctx.Err()
					}
					if !sendVehicleActionStatus(ctx, outbound, cfg.VehicleAgentID, action.GetVehicleActionId(), outcome.state, outcome.resultMessage, action.GetAckCorrelationId(), outcome.rawAckCode, outcome.rawAck) {
						return ctx.Err()
					}
					continue
				}

				outcome, err := handleVehicleAction(ctx, logger, outbound, cfg, gateway, observer, action)
				if err != nil {
					return err
				}
				processedVehicleActions[action.GetVehicleActionId()] = outcome
				continue
			}

			missionExecution := msg.GetMissionExecution()
			if missionExecution != nil {
				missionExecutionKey := missionExecutionProcessingKey(missionExecution)
				if outcome, ok := processedMissionExecutions[missionExecutionKey]; ok {
					// Upload/start/RTL are separate actions on the same execution id,
					// so the idempotency key includes the action as well as the id.
					logger.Warn("duplicate gRPC mission execution received; replaying prior result", "execution_id", missionExecution.GetExecutionId(), "action", missionExecution.GetAction())
					if !sendMissionExecutionStatus(ctx, outbound, cfg.VehicleAgentID, missionExecution.GetExecutionId(), outcome.state, outcome.resultMessage) {
						return ctx.Err()
					}
					continue
				}

				outcome, err := handleMissionExecution(ctx, logger, outbound, cfg, gateway, missionExecution)
				if err != nil {
					return err
				}
				processedMissionExecutions[missionExecutionKey] = outcome
				continue
			}

			gimbalControl := msg.GetGimbalControl()
			if gimbalControl != nil {
				handleGimbalControl(ctx, logger, gimbalController, gimbalControl)
				continue
			}
		}
	}
}

func handleGimbalControl(ctx context.Context, logger *slog.Logger, controller GimbalController, command *pb.GimbalControlCommand) {
	if logger == nil {
		logger = slog.Default()
	}
	if controller == nil {
		logger.Warn("gimbal control command dropped; no MAVLink gimbal controller configured", "drone_id", command.GetDroneId())
		return
	}

	err := controller.SendGimbalControl(ctx, mavlinkobserver.GimbalControlCommand{
		PitchRateDegS:     command.GetPitchRateDegS(),
		YawRateDegS:       command.GetYawRateDegS(),
		TargetSystemID:    uint8(command.GetTargetSystemId()),
		TargetComponentID: uint8(command.GetTargetComponentId()),
		GimbalDeviceID:    uint8(command.GetGimbalDeviceId()),
	})
	if err != nil {
		logger.Warn("gimbal control command failed", "drone_id", command.GetDroneId(), "error", err)
		return
	}

	logger.Debug(
		"gimbal control command sent",
		"drone_id", command.GetDroneId(),
		"pitch_rate_deg_s", command.GetPitchRateDegS(),
		"yaw_rate_deg_s", command.GetYawRateDegS(),
	)
}

// handleVehicleAction translates one backend vehicle action envelope into a
// vehicle gateway call and reports each significant state transition back to the backend.
func handleVehicleAction(ctx context.Context, logger *slog.Logger, outbound outboundQueues, cfg Config, gateway vehicle.Gateway, observer MAVLinkObserver, action *pb.VehicleActionEnvelope) (vehicleActionOutcome, error) {
	logger.Info(
		"gRPC vehicle action received",
		"vehicle_action_id", action.GetVehicleActionId(),
		"type", action.GetActionType(),
		"drone_id", action.GetDroneId(),
		"requested_by", action.GetRequestedBy(),
	)

	if !sendVehicleActionStatus(ctx, outbound, cfg.VehicleAgentID, action.GetVehicleActionId(), vehicleActionStateVehicleAgentReceived, "", action.GetAckCorrelationId(), "", nil) {
		return vehicleActionOutcome{}, ctx.Err()
	}

	// "sent_to_vehicle" means the agent is about to call PX4/MAVSDK through
	// the Gateway abstraction. It does not yet mean the vehicle accepted it.
	if !sendVehicleActionStatus(ctx, outbound, cfg.VehicleAgentID, action.GetVehicleActionId(), vehicleActionStateSentToVehicle, "", action.GetAckCorrelationId(), "", nil) {
		return vehicleActionOutcome{}, ctx.Err()
	}

	timeout := cfg.CommandTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	actionCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mavlinkCommand, hasMAVLinkCommand := mavlinkCommandForVehicleAction(action.GetActionType())
	sentAt := time.Now().UTC()

	// Vehicle calls can hang on real transports, so each vehicle action gets a
	// bounded execution context independent of the longer stream context.
	if err := executeVehicleAction(actionCtx, gateway, action.GetActionType()); err != nil {
		state := vehicleActionStateVehicleRejected
		if errors.Is(err, errUnsupportedVehicleAction) {
			state = vehicleActionStateFailed
		}
		rawAck, rawAckCode := observedCommandAck(ctx, logger, cfg, observer, action, mavlinkCommand, hasMAVLinkCommand, sentAt)
		resultMessage := err.Error()
		if rawAck != nil && rawAck.GetResult() != 0 {
			resultMessage = fmt.Sprintf("vehicle rejected command: %s", rawAck.GetResultLabel())
		}

		if !sendVehicleActionStatus(ctx, outbound, cfg.VehicleAgentID, action.GetVehicleActionId(), state, resultMessage, action.GetAckCorrelationId(), rawAckCode, rawAck) {
			return vehicleActionOutcome{}, ctx.Err()
		}
		logger.Error("gRPC vehicle action execution failed", "vehicle_action_id", action.GetVehicleActionId(), "type", action.GetActionType(), "error", err)
		return vehicleActionOutcome{state: state, resultMessage: resultMessage, rawAckCode: rawAckCode, rawAck: rawAck}, nil
	}

	rawAck, rawAckCode := observedCommandAck(ctx, logger, cfg, observer, action, mavlinkCommand, hasMAVLinkCommand, sentAt)
	if rawAck != nil && rawAck.GetResult() != 0 {
		resultMessage := fmt.Sprintf("vehicle rejected command: %s", rawAck.GetResultLabel())
		if !sendVehicleActionStatus(ctx, outbound, cfg.VehicleAgentID, action.GetVehicleActionId(), vehicleActionStateVehicleRejected, resultMessage, action.GetAckCorrelationId(), rawAckCode, rawAck) {
			return vehicleActionOutcome{}, ctx.Err()
		}

		logger.Warn("gRPC vehicle action rejected by raw MAVLink ACK", "vehicle_action_id", action.GetVehicleActionId(), "type", action.GetActionType(), "raw_ack_code", rawAckCode)
		return vehicleActionOutcome{state: vehicleActionStateVehicleRejected, resultMessage: resultMessage, rawAckCode: rawAckCode, rawAck: rawAck}, nil
	}

	resultMessage := "accepted by vehicle"
	if !sendVehicleActionStatus(ctx, outbound, cfg.VehicleAgentID, action.GetVehicleActionId(), vehicleActionStateVehicleAcked, resultMessage, action.GetAckCorrelationId(), rawAckCode, rawAck) {
		return vehicleActionOutcome{}, ctx.Err()
	}

	logger.Info("gRPC vehicle action acknowledged by vehicle", "vehicle_action_id", action.GetVehicleActionId(), "type", action.GetActionType())
	return vehicleActionOutcome{state: vehicleActionStateVehicleAcked, resultMessage: resultMessage, rawAckCode: rawAckCode, rawAck: rawAck}, nil
}

// handleMissionExecution translates a backend mission action into vehicle
// gateway calls. Upload, start, and RTL have different state progressions, so
// they are handled as separate action branches.
func handleMissionExecution(ctx context.Context, logger *slog.Logger, outbound outboundQueues, cfg Config, gateway vehicle.Gateway, execution *pb.MissionExecutionEnvelope) (missionExecutionOutcome, error) {
	if logger == nil {
		logger = slog.Default()
	}

	logger.Info(
		"gRPC mission execution received",
		"execution_id", execution.GetExecutionId(),
		"mission_id", execution.GetMissionId(),
		"action", execution.GetAction(),
		"completion_action", execution.GetCompletionAction(),
		"drone_id", execution.GetDroneId(),
		"requested_by", execution.GetRequestedBy(),
		"waypoint_count", len(execution.GetWaypoints()),
	)

	switch execution.GetAction() {
	case missionActionUpload:
		// Upload sends the full waypoint payload to the vehicle but does not
		// start flying. The operator must request start separately.
		if !sendMissionExecutionStatus(ctx, outbound, cfg.VehicleAgentID, execution.GetExecutionId(), missionExecutionStateUploading, "") {
			return missionExecutionOutcome{}, ctx.Err()
		}

		if err := gateway.UploadMission(ctx, missionEnvelopeToPlan(execution)); err != nil {
			resultMessage := err.Error()
			if !sendMissionExecutionStatus(ctx, outbound, cfg.VehicleAgentID, execution.GetExecutionId(), missionExecutionStateUploadFailed, resultMessage) {
				return missionExecutionOutcome{}, ctx.Err()
			}

			logger.Error("gRPC mission upload failed", "execution_id", execution.GetExecutionId(), "mission_id", execution.GetMissionId(), "error", err)
			return missionExecutionOutcome{state: missionExecutionStateUploadFailed, resultMessage: resultMessage}, nil
		}

		resultMessage := "uploaded to vehicle"
		if !sendMissionExecutionStatus(ctx, outbound, cfg.VehicleAgentID, execution.GetExecutionId(), missionExecutionStateUploadedToVehicle, resultMessage) {
			return missionExecutionOutcome{}, ctx.Err()
		}

		logger.Info("gRPC mission uploaded to vehicle", "execution_id", execution.GetExecutionId(), "mission_id", execution.GetMissionId())
		return missionExecutionOutcome{state: missionExecutionStateUploadedToVehicle, resultMessage: resultMessage}, nil
	case missionActionStart:
		// Start assumes upload already succeeded. Progress is monitored
		// asynchronously after the vehicle accepts the start command.
		if err := startMissionWorkflow(ctx, gateway); err != nil {
			resultMessage := err.Error()
			if !sendMissionExecutionStatus(ctx, outbound, cfg.VehicleAgentID, execution.GetExecutionId(), missionExecutionStateFailed, resultMessage) {
				return missionExecutionOutcome{}, ctx.Err()
			}

			logger.Error("gRPC mission start failed", "execution_id", execution.GetExecutionId(), "mission_id", execution.GetMissionId(), "error", err)
			return missionExecutionOutcome{state: missionExecutionStateFailed, resultMessage: resultMessage}, nil
		}

		resultMessage := "mission started"
		if !sendMissionExecutionStatus(ctx, outbound, cfg.VehicleAgentID, execution.GetExecutionId(), missionExecutionStateActive, resultMessage) {
			return missionExecutionOutcome{}, ctx.Err()
		}

		go monitorMissionProgress(ctx, logger, outbound, cfg.VehicleAgentID, gateway, execution)

		logger.Info("gRPC mission started", "execution_id", execution.GetExecutionId(), "mission_id", execution.GetMissionId())
		return missionExecutionOutcome{state: missionExecutionStateActive, resultMessage: resultMessage}, nil
	case missionActionRTL:
		// Abort is represented as return-to-launch. Telemetry on the backend
		// later settles the execution once the aircraft is no longer airborne.
		if err := gateway.ReturnToLaunch(ctx); err != nil {
			resultMessage := err.Error()
			if !sendMissionExecutionStatus(ctx, outbound, cfg.VehicleAgentID, execution.GetExecutionId(), missionExecutionStateFailed, resultMessage) {
				return missionExecutionOutcome{}, ctx.Err()
			}

			logger.Error("gRPC mission RTL failed", "execution_id", execution.GetExecutionId(), "mission_id", execution.GetMissionId(), "error", err)
			return missionExecutionOutcome{state: missionExecutionStateFailed, resultMessage: resultMessage}, nil
		}

		resultMessage := "RTL accepted by vehicle; mission abort in progress"
		if !sendMissionExecutionStatus(ctx, outbound, cfg.VehicleAgentID, execution.GetExecutionId(), missionExecutionStateRTLRequested, resultMessage) {
			return missionExecutionOutcome{}, ctx.Err()
		}

		logger.Info("gRPC mission RTL accepted by vehicle", "execution_id", execution.GetExecutionId(), "mission_id", execution.GetMissionId())
		return missionExecutionOutcome{state: missionExecutionStateRTLRequested, resultMessage: resultMessage}, nil
	default:
		resultMessage := fmt.Sprintf("unsupported mission execution action: %s", execution.GetAction())
		if !sendMissionExecutionStatus(ctx, outbound, cfg.VehicleAgentID, execution.GetExecutionId(), missionExecutionStateFailed, resultMessage) {
			return missionExecutionOutcome{}, ctx.Err()
		}
		return missionExecutionOutcome{state: missionExecutionStateFailed, resultMessage: resultMessage}, nil
	}
}

// startMissionWorkflow groups the pre-start setup and actual mission start call
// so mission start handling reads as one application workflow.
func startMissionWorkflow(ctx context.Context, gateway vehicle.Gateway) error {
	if err := gateway.PrepareMissionStart(ctx); err != nil {
		return err
	}

	if err := gateway.StartMission(ctx); err != nil {
		return fmt.Errorf("start mission workflow: %w", err)
	}

	return nil
}

// monitorMissionProgress streams mission progress updates back to the backend
// until the gateway reports completion, the progress channel closes, or the
// connection context is cancelled.
func monitorMissionProgress(ctx context.Context, logger *slog.Logger, outbound outboundQueues, agentID string, gateway vehicle.Gateway, execution *pb.MissionExecutionEnvelope) {
	progressCh, err := gateway.MissionProgress(ctx)
	if err != nil {
		logger.Warn("mission progress subscription failed", "execution_id", execution.GetExecutionId(), "mission_id", execution.GetMissionId(), "error", err)
		return
	}

	lastCurrent := -1
	lastTotal := -1
	for {
		select {
		case <-ctx.Done():
			return
		case progress, ok := <-progressCh:
			if !ok {
				return
			}

			// Avoid sending duplicate progress rows when the vehicle repeats the
			// same mission item count.
			if progress.Current != lastCurrent || progress.Total != lastTotal {
				message := fmt.Sprintf("mission progress %d/%d", progress.Current, progress.Total)
				if !sendMissionExecutionStatusWithProgress(ctx, outbound, agentID, execution.GetExecutionId(), missionExecutionStateActive, message, progress) {
					return
				}
				lastCurrent = progress.Current
				lastTotal = progress.Total
			}

			if progress.Finished {
				message := fmt.Sprintf("mission completed %d/%d", progress.Current, progress.Total)
				if !sendMissionExecutionStatusWithProgress(ctx, outbound, agentID, execution.GetExecutionId(), missionExecutionStateCompleted, message, progress) {
					return
				}

				// Some missions intentionally hold at the final waypoint rather
				// than moving into a generic completed state only.
				if execution.GetCompletionAction() == string(vehicle.MissionCompletionActionHold) {
					if !sendMissionExecutionStatusWithProgress(ctx, outbound, agentID, execution.GetExecutionId(), missionExecutionStateHold, "mission complete; holding at final waypoint", progress) {
						return
					}
				}
				return
			}
		}
	}
}

// missionEnvelopeToPlan converts the backend protobuf payload into the vehicle
// gateway mission type used by the MAVSDK-facing layer.
func missionEnvelopeToPlan(execution *pb.MissionExecutionEnvelope) vehicle.MissionPlan {
	waypoints := execution.GetWaypoints()
	plan := vehicle.MissionPlan{
		Waypoints:        make([]vehicle.MissionWaypoint, 0, len(waypoints)),
		CompletionAction: vehicle.MissionCompletionAction(execution.GetCompletionAction()),
	}

	for _, waypoint := range waypoints {
		plan.Waypoints = append(plan.Waypoints, vehicle.MissionWaypoint{
			Sequence:          int(waypoint.GetSequence()),
			Latitude:          waypoint.GetLatitude(),
			Longitude:         waypoint.GetLongitude(),
			RelativeAltitudeM: waypoint.GetRelativeAltitudeM(),
			SpeedMPS:          waypoint.SpeedMps,
			LoiterTimeS:       waypoint.LoiterTimeS,
		})
	}

	return plan
}

// missionExecutionProcessingKey distinguishes separate actions on the same
// mission execution, such as upload followed later by start.
func missionExecutionProcessingKey(execution *pb.MissionExecutionEnvelope) string {
	return execution.GetExecutionId() + ":" + execution.GetAction()
}

// executeVehicleAction maps Atlas vehicle action names to concrete vehicle
// gateway operations.
func executeVehicleAction(ctx context.Context, gateway vehicle.Gateway, actionType string) error {
	switch actionType {
	case vehicleActionTypeArm:
		return gateway.Arm(ctx)
	case vehicleActionTypeTakeoff:
		return gateway.Takeoff(ctx)
	case vehicleActionTypeReturnToLaunch:
		return gateway.ReturnToLaunch(ctx)
	case vehicleActionTypeLand:
		return gateway.Land(ctx)
	default:
		return fmt.Errorf("%w: %s", errUnsupportedVehicleAction, actionType)
	}
}

func mavlinkCommandForVehicleAction(actionType string) (uint16, bool) {
	switch actionType {
	case vehicleActionTypeArm:
		return mavCmdComponentArmDisarm, true
	case vehicleActionTypeTakeoff:
		return mavCmdNavTakeoff, true
	case vehicleActionTypeReturnToLaunch:
		return mavCmdNavReturnToLaunch, true
	case vehicleActionTypeLand:
		return mavCmdNavLand, true
	default:
		return 0, false
	}
}

func observedCommandAck(ctx context.Context, logger *slog.Logger, cfg Config, observer MAVLinkObserver, action *pb.VehicleActionEnvelope, command uint16, hasCommand bool, sentAt time.Time) (*pb.RawMavlinkCommandAckEvidence, string) {
	if observer == nil || action == nil || !hasCommand {
		return nil, ""
	}
	if logger == nil {
		logger = slog.Default()
	}

	match := mavlinkobserver.CommandAckMatch{
		ActionID:           action.GetVehicleActionId(),
		ActionType:         action.GetActionType(),
		Command:            command,
		EarliestObservedAt: sentAt.Add(-commandAckObservationSlack),
		FinalOnly:          true,
	}
	if sourceSystemID, sourceComponentID, ok := observer.PreferredCommandAckSource(); ok {
		match.SourceSystemID = sourceSystemID
		match.SourceComponentID = sourceComponentID
	}

	waitTimeout := commandAckObservationTimeout
	if cfg.CommandTimeout > 0 && cfg.CommandTimeout < waitTimeout {
		waitTimeout = cfg.CommandTimeout
	}
	ackCtx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()

	evidence, ok := observer.WaitForCommandAck(ackCtx, match)
	if !ok {
		logger.Warn(
			"raw MAVLink COMMAND_ACK not observed for vehicle action",
			"vehicle_action_id", action.GetVehicleActionId(),
			"type", action.GetActionType(),
			"command", command,
		)
		return nil, ""
	}

	rawAck := commandAckEvidenceToProto(evidence)
	return rawAck, evidence.RawAckCode()
}

func commandAckEvidenceToProto(evidence mavlinkobserver.CommandAckEvidence) *pb.RawMavlinkCommandAckEvidence {
	res := &pb.RawMavlinkCommandAckEvidence{
		ObservedAt:        evidence.ObservedAt.UTC().Format(time.RFC3339Nano),
		SourceSystemId:    uint32(evidence.SourceSystemID),
		SourceComponentId: uint32(evidence.SourceComponentID),
		Command:           uint32(evidence.Command),
		Result:            uint32(evidence.Result),
		ResultLabel:       evidence.RawAckCode(),
		MatchStatus:       evidence.MatchStatus,
	}
	if evidence.Progress != nil {
		res.Progress = uint32(*evidence.Progress)
		res.HasProgress = true
	}
	if evidence.ResultParam2 != nil {
		res.ResultParam2 = *evidence.ResultParam2
		res.HasResultParam2 = true
	}
	if evidence.TargetSystem != nil {
		res.TargetSystem = uint32(*evidence.TargetSystem)
		res.HasTargetSystem = true
	}
	if evidence.TargetComponent != nil {
		res.TargetComponent = uint32(*evidence.TargetComponent)
		res.HasTargetComponent = true
	}
	return res
}

func mavlinkObserverDiagnosticsToProto(observer MAVLinkObserver) *pb.MavlinkObserverDiagnostics {
	if observer == nil {
		return nil
	}

	diagnostics := observer.SnapshotDiagnostics()
	components := make([]*pb.MavlinkComponent, 0, len(diagnostics.Components))
	for _, component := range diagnostics.Components {
		components = append(components, &pb.MavlinkComponent{
			SystemId:    uint32(component.SystemID),
			ComponentId: uint32(component.ComponentID),
			FirstSeenAt: formatOptionalTime(component.FirstSeenAt),
			LastSeenAt:  formatOptionalTime(component.LastSeenAt),
			PacketCount: component.PacketCount,
		})
	}

	return &pb.MavlinkObserverDiagnostics{
		Connected:             diagnostics.Connected,
		PacketsSeen:           diagnostics.PacketsSeen,
		LastPacketAt:          formatOptionalTime(diagnostics.LastPacketAt),
		LastHeartbeatAt:       formatOptionalTime(diagnostics.LastHeartbeatAt),
		LastCommandAckAt:      formatOptionalTime(diagnostics.LastCommandAckAt),
		LastCommandAckCommand: uint32(diagnostics.LastCommandAckCommand),
		LastCommandAckResult:  uint32(diagnostics.LastCommandAckResult),
		ComponentCount:        uint32(len(diagnostics.Components)),
		Components:            components,
	}
}

func backendChannelHealthToProto(manager *comms.BackendChannelManager) *pb.BackendChannelHealth {
	if manager == nil {
		return nil
	}

	snapshot := manager.Snapshot()
	return &pb.BackendChannelHealth{
		State:                snapshot.State,
		ReconnectCount:       snapshot.ReconnectCount,
		ConnectedAt:          formatOptionalTime(snapshot.ConnectedAt),
		LastDisconnectedAt:   formatOptionalTime(snapshot.LastDisconnectedAt),
		LastSuccessfulSendAt: formatOptionalTime(snapshot.LastSuccessfulSendAt),
		LastHeartbeatSentAt:  formatOptionalTime(snapshot.LastHeartbeatSentAt),
		LastError:            snapshot.LastError,
		BackendAddress:       snapshot.BackendAddress,
		WeakLink:             snapshot.WeakLink,
		WeakLinkReason:       snapshot.WeakLinkReason,
	}
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// sendLoop is the only goroutine that writes to the gRPC stream. Keeping one
// writer avoids concurrent Send calls on the same client stream.
func sendLoop(ctx context.Context, stream pb.VehicleAgentChannelService_ConnectClient, outbound outboundQueues, errs chan<- error, channelHealth *comms.BackendChannelManager) {
	for {
		msg, ok := nextOutboundMessage(ctx, outbound)
		if !ok {
			return
		}

		if err := stream.Send(msg); err != nil {
			if channelHealth != nil {
				channelHealth.RecordSendFailure(time.Now().UTC(), err)
			}
			errs <- err
			return
		}
		if channelHealth != nil {
			channelHealth.RecordSend(time.Now().UTC(), msg.GetHeartbeat() != nil)
		}
	}
}

// nextOutboundMessage chooses the next agent-to-backend message by priority.
// Critical status beats heartbeat, and heartbeat beats telemetry.
func nextOutboundMessage(ctx context.Context, outbound outboundQueues) (*pb.VehicleAgentToBackend, bool) {
	select {
	case <-ctx.Done():
		return nil, false
	default:
	}

	select {
	case msg := <-outbound.critical:
		return msg, true
	default:
	}

	select {
	case msg := <-outbound.heartbeat:
		return msg, true
	default:
	}

	select {
	case msg := <-outbound.telemetry:
		return msg, true
	default:
	}

	select {
	case msg := <-outbound.perception:
		return msg, true
	default:
	}

	select {
	case <-ctx.Done():
		return nil, false
	case msg := <-outbound.critical:
		return msg, true
	case msg := <-outbound.heartbeat:
		return msg, true
	case msg := <-outbound.telemetry:
		return msg, true
	case msg := <-outbound.perception:
		return msg, true
	}
}

// receiveLoop is the only goroutine that reads backend-to-agent messages from
// the stream and forwards them to the session loop.
func receiveLoop(stream pb.VehicleAgentChannelService_ConnectClient, inbound chan<- *pb.BackendToVehicleAgent, errs chan<- error) {
	for {
		msg, err := stream.Recv()
		if err != nil {
			errs <- err
			return
		}
		inbound <- msg
	}
}

// sendHeartbeats reports agent liveness at the configured interval.
func sendHeartbeats(ctx context.Context, outbound outboundQueues, cfg Config, observer MAVLinkObserver, channelHealth *comms.BackendChannelManager) {
	interval := cfg.HeartbeatInterval
	if interval == 0 {
		interval = 5 * time.Second
	}

	sendHeartbeat(ctx, outbound, cfg, observer, channelHealth)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sendHeartbeat(ctx, outbound, cfg, observer, channelHealth)
		}
	}
}

// sendHeartbeat enqueues one heartbeat message if the heartbeat queue has room.
func sendHeartbeat(ctx context.Context, outbound outboundQueues, cfg Config, observer MAVLinkObserver, channelHealth *comms.BackendChannelManager) {
	if !enqueueHeartbeat(ctx, outbound, &pb.VehicleAgentToBackend{
		VehicleAgentId: cfg.VehicleAgentID,
		Payload: &pb.VehicleAgentToBackend_Heartbeat{
			Heartbeat: &pb.Heartbeat{
				VehicleAgentVersion: cfg.VehicleAgentVersion,
				MavlinkObserver:     mavlinkObserverDiagnosticsToProto(observer),
				BackendChannel:      backendChannelHealthToProto(channelHealth),
			},
		},
	}) {
		return
	}
}

// sendTelemetrySnapshots periodically samples the telemetry source and enqueues
// the latest snapshot for backend fleet views.
func sendTelemetrySnapshots(ctx context.Context, logger *slog.Logger, outbound outboundQueues, cfg Config, source telemetry.Source) {
	interval := cfg.TelemetryInterval
	if interval == 0 {
		interval = 2 * time.Second
	}

	sendTelemetrySnapshot(ctx, logger, outbound, cfg, source)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sendTelemetrySnapshot(ctx, logger, outbound, cfg, source)
		}
	}
}

// sendTelemetrySnapshot reads one telemetry sample and serializes it into the
// protobuf wire format expected by the backend channel server.
func sendTelemetrySnapshot(ctx context.Context, logger *slog.Logger, outbound outboundQueues, cfg Config, source telemetry.Source) {
	snapshot, err := source.Read(time.Now().UTC())
	if err != nil {
		logger.Error("telemetry read failed", "source", source.Name(), "error", err)
		return
	}

	if !enqueueTelemetry(ctx, outbound, &pb.VehicleAgentToBackend{
		VehicleAgentId: cfg.VehicleAgentID,
		Payload: &pb.VehicleAgentToBackend_Telemetry{
			Telemetry: &pb.Telemetry{
				ObservedAt:        snapshot.ObservedAt.UTC().Format(time.RFC3339Nano),
				BatteryPercent:    snapshot.BatteryPercent,
				RelativeAltitudeM: snapshot.RelativeAltitudeM,
				FlightMode:        snapshot.FlightMode,
				Armed:             snapshot.Armed,
				InAir:             snapshot.InAir,
				Latitude:          snapshot.Latitude,
				Longitude:         snapshot.Longitude,
				HeadingDeg:        snapshot.HeadingDeg,
				GroundSpeedMps:    snapshot.GroundSpeedMPS,
				GpsFix:            snapshot.GPSFix,
				SatellitesVisible: int32(snapshot.SatellitesVisible),
				HomePositionSet:   snapshot.HomePositionSet,
				Source:            snapshot.Source,
			},
		},
	}) {
		logger.Debug("telemetry snapshot dropped due to outbound backpressure", "source", source.Name())
	}
}

// sendPerceptionMetadata forwards local inference metadata over the existing
// vehicle-agent stream. Keeping this on the same stream avoids a second process
// competing for command-channel ownership on the backend.
func sendPerceptionMetadata(ctx context.Context, logger *slog.Logger, outbound outboundQueues, cfg Config, source perception.Source) {
	events, health, errs := source.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				logger.Warn("perception metadata source error", "source", source.Name(), "error", err)
			}
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event.DroneID == "" {
				event.DroneID = cfg.DroneID
			}
			if !sendPerceptionEvent(ctx, outbound, cfg.VehicleAgentID, event) {
				logger.Debug("perception event dropped due to outbound backpressure", "source", source.Name())
			}
		case item, ok := <-health:
			if !ok {
				health = nil
				continue
			}
			if item.DroneID == "" {
				item.DroneID = cfg.DroneID
			}
			if !sendPerceptionHealth(ctx, outbound, cfg.VehicleAgentID, item) {
				logger.Debug("perception health dropped due to outbound backpressure", "source", source.Name())
			}
		}

		if events == nil && health == nil && errs == nil {
			return
		}
	}
}

func sendPerceptionEvent(ctx context.Context, outbound outboundQueues, agentID string, event perception.Event) bool {
	detections := make([]*pb.PerceptionDetection, 0, len(event.Detections))
	for _, detection := range event.Detections {
		detections = append(detections, &pb.PerceptionDetection{
			ClassName:  detection.ClassName,
			Confidence: detection.Confidence,
			Bbox: &pb.NormalizedBBox{
				X: detection.BBox[0],
				Y: detection.BBox[1],
				W: detection.BBox[2],
				H: detection.BBox[3],
			},
		})
	}

	return enqueuePerception(ctx, outbound, &pb.VehicleAgentToBackend{
		VehicleAgentId: agentID,
		Payload: &pb.VehicleAgentToBackend_PerceptionEvent{
			PerceptionEvent: &pb.PerceptionEvent{
				DroneId:            event.DroneID,
				SourceId:           event.SourceID,
				ObservedAt:         formatOptionalTime(event.ObservedAt),
				FrameId:            event.FrameID,
				ModelName:          event.ModelName,
				ModelVersion:       event.ModelVersion,
				InferenceLatencyMs: event.InferenceLatencyMS,
				Detections:         detections,
			},
		},
	})
}

func sendPerceptionHealth(ctx context.Context, outbound outboundQueues, agentID string, health perception.Health) bool {
	return enqueuePerception(ctx, outbound, &pb.VehicleAgentToBackend{
		VehicleAgentId: agentID,
		Payload: &pb.VehicleAgentToBackend_PerceptionHealth{
			PerceptionHealth: &pb.PerceptionHealth{
				DroneId:          health.DroneID,
				SourceId:         health.SourceID,
				InputConnected:   health.InputConnected,
				OutputPublishing: health.OutputPublishing,
				ModelLoaded:      health.ModelLoaded,
				Accelerator:      health.Accelerator,
				Fps:              health.FPS,
				DroppedFrames:    health.DroppedFrames,
				LastFrameAt:      formatOptionalTime(health.LastFrameAt),
				LastDetectionAt:  formatOptionalTime(health.LastDetectionAt),
				LastError:        health.LastError,
				ModelName:        health.ModelName,
				ModelVersion:     health.ModelVersion,
			},
		},
	})
}

// sendVehicleActionStatus reports vehicle action lifecycle updates to the backend services.
func sendVehicleActionStatus(ctx context.Context, outbound outboundQueues, agentID string, vehicleActionID string, state string, resultMessage string, ackCorrelationID string, rawAckCode string, rawAck *pb.RawMavlinkCommandAckEvidence) bool {
	return enqueueCritical(ctx, outbound, &pb.VehicleAgentToBackend{
		VehicleAgentId: agentID,
		Payload: &pb.VehicleAgentToBackend_VehicleActionStatus{
			VehicleActionStatus: &pb.VehicleActionStatus{
				VehicleActionId:      vehicleActionID,
				State:                state,
				ResultMessage:        resultMessage,
				AckCorrelationId:     ackCorrelationID,
				RawAckCode:           rawAckCode,
				RawMavlinkCommandAck: rawAck,
			},
		},
	})
}

// sendMissionExecutionStatus reports mission lifecycle updates without progress
// counters.
func sendMissionExecutionStatus(ctx context.Context, outbound outboundQueues, agentID string, executionID string, state string, resultMessage string) bool {
	return sendMissionExecutionStatusWithProgress(ctx, outbound, agentID, executionID, state, resultMessage, vehicle.MissionProgressEvent{})
}

// sendMissionExecutionStatusWithProgress reports mission lifecycle updates and,
// when available, the current mission item counters.
func sendMissionExecutionStatusWithProgress(ctx context.Context, outbound outboundQueues, agentID string, executionID string, state string, resultMessage string, progress vehicle.MissionProgressEvent) bool {
	return enqueueCritical(ctx, outbound, &pb.VehicleAgentToBackend{
		VehicleAgentId: agentID,
		Payload: &pb.VehicleAgentToBackend_MissionExecutionStatus{
			MissionExecutionStatus: &pb.MissionExecutionStatus{
				ExecutionId:        executionID,
				State:              state,
				ResultMessage:      resultMessage,
				CurrentMissionItem: int32(progress.Current),
				TotalMissionItems:  int32(progress.Total),
			},
		},
	})
}

// enqueueCritical blocks until a critical status message is queued or the
// context is cancelled. These messages are part of vehicle action/mission correctness.
func enqueueCritical(ctx context.Context, outbound outboundQueues, msg *pb.VehicleAgentToBackend) bool {
	select {
	case <-ctx.Done():
		return false
	case outbound.critical <- msg:
		return true
	}
}

// enqueueHeartbeat is best-effort. If heartbeats are already backed up, the
// channel is unhealthy enough that another heartbeat does not need to queue.
func enqueueHeartbeat(ctx context.Context, outbound outboundQueues, msg *pb.VehicleAgentToBackend) bool {
	select {
	case <-ctx.Done():
		return false
	case outbound.heartbeat <- msg:
		return true
	default:
		return false
	}
}

// enqueueTelemetry keeps the newest telemetry sample and drops an older queued
// sample when necessary. For fleet views, latest state is more useful than stale
// historical samples.
func enqueueTelemetry(ctx context.Context, outbound outboundQueues, msg *pb.VehicleAgentToBackend) bool {
	select {
	case <-ctx.Done():
		return false
	case outbound.telemetry <- msg:
		return true
	default:
	}

	select {
	case <-outbound.telemetry:
	default:
	}

	select {
	case <-ctx.Done():
		return false
	case outbound.telemetry <- msg:
		return true
	default:
		return false
	}
}

// enqueuePerception keeps the most recent bounded batch of perception messages.
// Detections are advisory and must not delay command status under weak links.
func enqueuePerception(ctx context.Context, outbound outboundQueues, msg *pb.VehicleAgentToBackend) bool {
	select {
	case <-ctx.Done():
		return false
	case outbound.perception <- msg:
		return true
	default:
	}

	select {
	case <-outbound.perception:
	default:
	}

	select {
	case <-ctx.Done():
		return false
	case outbound.perception <- msg:
		return true
	default:
		return false
	}
}

// nextBackoff doubles reconnect delay until the configured maximum.
func nextBackoff(current time.Duration, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}

	return next
}
