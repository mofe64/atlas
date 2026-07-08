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

	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
	pb "github.com/sunnyside/atlas/atlas-agent/internal/transport/vehicleagentchannelpb/atlas"
	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	commandTypeArm            = "arm"
	commandTypeTakeoff        = "takeoff"
	commandTypeReturnToLaunch = "return_to_launch"
	commandTypeLand           = "land"

	commandStateVehicleAgentReceived = "vehicle_agent_received"
	commandStateSentToVehicle        = "sent_to_vehicle"
	commandStateVehicleAcked         = "vehicle_acked"
	commandStateVehicleRejected      = "vehicle_rejected"
	commandStateFailed               = "failed"

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

var errUnsupportedCommand = errors.New("unsupported command type")

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

// commandOutcome remembers the final state sent for a command during this
// process lifetime so duplicate deliveries can replay the same result.
type commandOutcome struct {
	state         string
	resultMessage string
}

// missionExecutionOutcome remembers the final state sent for a mission action
// during this process lifetime so duplicate deliveries are idempotent.
type missionExecutionOutcome struct {
	state         string
	resultMessage string
}

// outboundQueues separates message priorities. Command and mission status are
// critical, heartbeats are useful liveness signals, and telemetry is allowed to
// drop old samples under backpressure.
type outboundQueues struct {
	critical  chan *pb.VehicleAgentToBackend
	heartbeat chan *pb.VehicleAgentToBackend
	telemetry chan *pb.VehicleAgentToBackend
}

// newOutboundQueues sizes each queue according to how much loss or delay Atlas
// can tolerate for that message class.
func newOutboundQueues() outboundQueues {
	return outboundQueues{
		critical:  make(chan *pb.VehicleAgentToBackend, 16),
		heartbeat: make(chan *pb.VehicleAgentToBackend, 2),
		telemetry: make(chan *pb.VehicleAgentToBackend, 1),
	}
}

// Run keeps the gRPC channel alive until ctx is cancelled. Each disconnect
// returns from connectOnce and is retried with bounded exponential backoff.
func Run(ctx context.Context, logger *slog.Logger, cfg Config, gateway vehicle.Gateway, telemetrySource telemetry.Source) {
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

	for ctx.Err() == nil {
		err := connectOnce(ctx, logger, cfg, gateway, telemetrySource)
		if ctx.Err() != nil {
			return
		}

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
func connectOnce(ctx context.Context, logger *slog.Logger, cfg Config, gateway vehicle.Gateway, telemetrySource telemetry.Source) error {
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

	go sendLoop(sendCtx, stream, outbound, errs)
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

	go sendHeartbeats(sendCtx, outbound, cfg)
	if telemetrySource != nil {
		go sendTelemetrySnapshots(sendCtx, logger, outbound, cfg, telemetrySource)
	}

	// TODO: Persist completed command IDs locally. If the agent process crashes
	// after executing a command but before reporting the final state, this
	// in-memory guard can forget the command and allow duplicate execution after
	// redelivery.
	processedCommands := make(map[string]commandOutcome)
	processedMissionExecutions := make(map[string]missionExecutionOutcome)

	// The main session loop consumes backend-to-agent work. Command and mission
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
			command := msg.GetCommand()
			if command != nil {
				// Backend redelivery can happen after lease expiry or reconnect.
				// Replaying the prior result avoids executing the same vehicle
				// command twice within this process lifetime.
				if outcome, ok := processedCommands[command.GetCommandId()]; ok {
					logger.Warn("duplicate gRPC command received; replaying prior result", "command_id", command.GetCommandId(), "type", command.GetCommandType())
					if !sendCommandStatus(ctx, outbound, cfg.VehicleAgentID, command.GetCommandId(), commandStateVehicleAgentReceived, "") {
						return ctx.Err()
					}
					if !sendCommandStatus(ctx, outbound, cfg.VehicleAgentID, command.GetCommandId(), outcome.state, outcome.resultMessage) {
						return ctx.Err()
					}
					continue
				}

				outcome, err := handleCommand(ctx, logger, outbound, cfg, gateway, command)
				if err != nil {
					return err
				}
				processedCommands[command.GetCommandId()] = outcome
				continue
			}

			missionExecution := msg.GetMissionExecution()
			if missionExecution == nil {
				continue
			}

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
		}
	}
}

// handleCommand translates one backend command envelope into a vehicle gateway
// call and reports each significant state transition back to the backend.
func handleCommand(ctx context.Context, logger *slog.Logger, outbound outboundQueues, cfg Config, gateway vehicle.Gateway, command *pb.CommandEnvelope) (commandOutcome, error) {
	logger.Info(
		"gRPC command received",
		"command_id", command.GetCommandId(),
		"type", command.GetCommandType(),
		"drone_id", command.GetDroneId(),
		"requested_by", command.GetRequestedBy(),
	)

	if !sendCommandStatus(ctx, outbound, cfg.VehicleAgentID, command.GetCommandId(), commandStateVehicleAgentReceived, "") {
		return commandOutcome{}, ctx.Err()
	}

	// "sent_to_vehicle" means the agent is about to call PX4/MAVSDK through
	// the Gateway abstraction. It does not yet mean the vehicle accepted it.
	if !sendCommandStatus(ctx, outbound, cfg.VehicleAgentID, command.GetCommandId(), commandStateSentToVehicle, "") {
		return commandOutcome{}, ctx.Err()
	}

	timeout := cfg.CommandTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Vehicle command calls can hang on real transports, so each command gets a
	// bounded execution context independent of the longer stream context.
	if err := executeVehicleCommand(commandCtx, gateway, command.GetCommandType()); err != nil {
		state := commandStateVehicleRejected
		if errors.Is(err, errUnsupportedCommand) {
			state = commandStateFailed
		}

		if !sendCommandStatus(ctx, outbound, cfg.VehicleAgentID, command.GetCommandId(), state, err.Error()) {
			return commandOutcome{}, ctx.Err()
		}
		logger.Error("gRPC command execution failed", "command_id", command.GetCommandId(), "type", command.GetCommandType(), "error", err)
		return commandOutcome{state: state, resultMessage: err.Error()}, nil
	}

	resultMessage := "accepted by vehicle"
	if !sendCommandStatus(ctx, outbound, cfg.VehicleAgentID, command.GetCommandId(), commandStateVehicleAcked, resultMessage) {
		return commandOutcome{}, ctx.Err()
	}

	logger.Info("gRPC command acknowledged by vehicle", "command_id", command.GetCommandId(), "type", command.GetCommandType())
	return commandOutcome{state: commandStateVehicleAcked, resultMessage: resultMessage}, nil
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

// executeVehicleCommand maps Atlas command names to concrete vehicle gateway
// operations.
func executeVehicleCommand(ctx context.Context, gateway vehicle.Gateway, commandType string) error {
	switch commandType {
	case commandTypeArm:
		return gateway.Arm(ctx)
	case commandTypeTakeoff:
		return gateway.Takeoff(ctx)
	case commandTypeReturnToLaunch:
		return gateway.ReturnToLaunch(ctx)
	case commandTypeLand:
		return gateway.Land(ctx)
	default:
		return fmt.Errorf("%w: %s", errUnsupportedCommand, commandType)
	}
}

// sendLoop is the only goroutine that writes to the gRPC stream. Keeping one
// writer avoids concurrent Send calls on the same client stream.
func sendLoop(ctx context.Context, stream pb.VehicleAgentChannelService_ConnectClient, outbound outboundQueues, errs chan<- error) {
	for {
		msg, ok := nextOutboundMessage(ctx, outbound)
		if !ok {
			return
		}

		if err := stream.Send(msg); err != nil {
			errs <- err
			return
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
	case <-ctx.Done():
		return nil, false
	case msg := <-outbound.critical:
		return msg, true
	case msg := <-outbound.heartbeat:
		return msg, true
	case msg := <-outbound.telemetry:
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
func sendHeartbeats(ctx context.Context, outbound outboundQueues, cfg Config) {
	interval := cfg.HeartbeatInterval
	if interval == 0 {
		interval = 5 * time.Second
	}

	sendHeartbeat(ctx, outbound, cfg)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sendHeartbeat(ctx, outbound, cfg)
		}
	}
}

// sendHeartbeat enqueues one heartbeat message if the heartbeat queue has room.
func sendHeartbeat(ctx context.Context, outbound outboundQueues, cfg Config) {
	if !enqueueHeartbeat(ctx, outbound, &pb.VehicleAgentToBackend{
		VehicleAgentId: cfg.VehicleAgentID,
		Payload: &pb.VehicleAgentToBackend_Heartbeat{
			Heartbeat: &pb.Heartbeat{
				VehicleAgentVersion: cfg.VehicleAgentVersion,
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

// sendCommandStatus reports command lifecycle updates to the backend services.
func sendCommandStatus(ctx context.Context, outbound outboundQueues, agentID string, commandID string, state string, resultMessage string) bool {
	return enqueueCritical(ctx, outbound, &pb.VehicleAgentToBackend{
		VehicleAgentId: agentID,
		Payload: &pb.VehicleAgentToBackend_CommandStatus{
			CommandStatus: &pb.CommandStatus{
				CommandId:     commandID,
				State:         state,
				ResultMessage: resultMessage,
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
// context is cancelled. These messages are part of command/mission correctness.
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

// nextBackoff doubles reconnect delay until the configured maximum.
func nextBackoff(current time.Duration, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}

	return next
}
