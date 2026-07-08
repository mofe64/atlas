package agentchannel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
	pb "github.com/sunnyside/atlas/atlas-agent/internal/vehicleagentchannelpb/atlas"
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

type commandOutcome struct {
	state         string
	resultMessage string
}

type missionExecutionOutcome struct {
	state         string
	resultMessage string
}

type outboundQueues struct {
	critical  chan *pb.VehicleAgentToBackend
	heartbeat chan *pb.VehicleAgentToBackend
	telemetry chan *pb.VehicleAgentToBackend
}

func newOutboundQueues() outboundQueues {
	return outboundQueues{
		critical:  make(chan *pb.VehicleAgentToBackend, 16),
		heartbeat: make(chan *pb.VehicleAgentToBackend, 2),
		telemetry: make(chan *pb.VehicleAgentToBackend, 1),
	}
}

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

	if !sendCommandStatus(ctx, outbound, cfg.VehicleAgentID, command.GetCommandId(), commandStateSentToVehicle, "") {
		return commandOutcome{}, ctx.Err()
	}

	timeout := cfg.CommandTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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

func startMissionWorkflow(ctx context.Context, gateway vehicle.Gateway) error {
	if err := gateway.PrepareMissionStart(ctx); err != nil {
		return err
	}

	if err := gateway.StartMission(ctx); err != nil {
		return fmt.Errorf("start mission workflow: %w", err)
	}

	return nil
}

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

func missionExecutionProcessingKey(execution *pb.MissionExecutionEnvelope) string {
	return execution.GetExecutionId() + ":" + execution.GetAction()
}

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

func sendMissionExecutionStatus(ctx context.Context, outbound outboundQueues, agentID string, executionID string, state string, resultMessage string) bool {
	return sendMissionExecutionStatusWithProgress(ctx, outbound, agentID, executionID, state, resultMessage, vehicle.MissionProgressEvent{})
}

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

func enqueueCritical(ctx context.Context, outbound outboundQueues, msg *pb.VehicleAgentToBackend) bool {
	select {
	case <-ctx.Done():
		return false
	case outbound.critical <- msg:
		return true
	}
}

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

func nextBackoff(current time.Duration, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}

	return next
}
