// Package groundstation maintains the agent-initiated session to Atlas Native.
package groundstation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/config"
	"github.com/sunnyside/atlas/atlas-agent/internal/identity"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
	pb "github.com/sunnyside/atlas/atlas-agent/internal/transport/groundstationpb"
	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	minimumRetry = time.Second
	maximumRetry = 30 * time.Second
)

func Run(ctx context.Context, logger *slog.Logger, cfg config.Config, localIdentity identity.Identity, telemetryUpdates <-chan telemetry.Snapshot, statusTexts <-chan telemetry.StatusTextEvent, perceptionOutputs perception.Outputs, executor CommandExecutor, missionExecutor MissionExecutor) {
	if logger == nil {
		logger = slog.Default()
	}
	frameDemand := newFrameDemand()
	backoff := minimumRetry
	for ctx.Err() == nil {
		err := connect(ctx, logger, cfg, localIdentity, telemetryUpdates, statusTexts, perceptionOutputs, executor, missionExecutor, frameDemand)
		if ctx.Err() != nil {
			return
		}
		logger.Warn("ground-station session ended; reconnecting", "error", err, "retry_after", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > maximumRetry {
			backoff = maximumRetry
		}
	}
}

type CommandExecutor interface {
	Execute(context.Context, string, string, string) (vehicle.CommandResult, error)
	Capabilities() []string
}

type MissionExecutor interface {
	Execute(context.Context, vehicle.MissionOperation)
	Updates() <-chan vehicle.MissionUpdate
	Capabilities() []string
}

func connect(ctx context.Context, logger *slog.Logger, cfg config.Config, localIdentity identity.Identity, telemetryUpdates <-chan telemetry.Snapshot, statusTexts <-chan telemetry.StatusTextEvent, perceptionOutputs perception.Outputs, executor CommandExecutor, missionExecutor MissionExecutor, frameDemand *frameDemand) error {
	connection, err := grpc.NewClient(cfg.GroundStationAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("create ground-station client: %w", err)
	}
	defer connection.Close()

	client := pb.NewGroundStationServiceClient(connection)
	stream, err := client.OpenSession(ctx)
	if err != nil {
		return fmt.Errorf("open ground-station session: %w", err)
	}
	sessionID := identity.NewID()
	now := time.Now().UTC()
	capabilities := append(executor.Capabilities(), missionExecutor.Capabilities()...)
	if cfg.PerceptionEnabled() {
		capabilities = append(capabilities, "perception:object_detection:v1", "perception:health:v1", "perception:frame_subscription:v1")
	}
	if err := stream.Send(&pb.AgentToGroundStation{
		SessionId: sessionID,
		Payload:   &pb.AgentToGroundStation_Registration{Registration: registration(cfg, localIdentity, sessionID, now, capabilities)},
	}); err != nil {
		return fmt.Errorf("send agent registration: %w", err)
	}

	response, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receive registration response: %w", err)
	}
	accepted := response.GetRegistrationAccepted()
	if accepted == nil {
		return errors.New("ground station did not accept registration")
	}
	logger.Info(
		"registered with Atlas Native",
		"ground_station", cfg.GroundStationAddress,
		"session_id", sessionID,
		"agent_id", accepted.GetAgentId(),
		"drone_id", accepted.GetDroneId(),
		"binding_id", accepted.GetBindingId(),
		"communication_link_id", accepted.GetCommunicationLinkId(),
	)
	perceptionContext, cancelPerception := context.WithCancel(ctx)
	defer cancelPerception()
	if cfg.PerceptionEnabled() {
		go runPerception(perceptionContext, logger, client, cfg, localIdentity, sessionID, perceptionOutputs, frameDemand)
	}

	receiveErrors := make(chan error, 1)
	commandRequests := make(chan *pb.VehicleCommandRequest, 4)
	commandResults := make(chan commandExecutionUpdate, 8)
	cancellations := make(chan *pb.VehicleCommandCancellation, 4)
	missionOperations := make(chan *pb.MissionOperationRequest, 4)
	go func() {
		for {
			message, err := stream.Recv()
			if err != nil {
				receiveErrors <- err
				return
			}
			switch payload := message.GetPayload().(type) {
			case *pb.GroundStationToAgent_CommandRequest:
				commandRequests <- payload.CommandRequest
			case *pb.GroundStationToAgent_CommandCancellation:
				cancellations <- payload.CommandCancellation
			case *pb.GroundStationToAgent_MissionOperationRequest:
				missionOperations <- payload.MissionOperationRequest
			}
		}
	}()

	interval := cfg.HeartbeatInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	updates := telemetryUpdates
	events := statusTexts
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-receiveErrors:
			return err
		case command := <-commandRequests:
			logger.Info("vehicle command received", "command_id", command.GetCommandId(), "command_type", command.GetCommandType().String())
			if err := handleCommand(ctx, stream, sessionID, localIdentity.DroneID, command, executor, commandResults); err != nil {
				return err
			}
		case update := <-commandResults:
			logger.Info("vehicle command completed", "command_id", update.commandID, "result", update.updateType.String(), "result_code", update.resultCode)
			if err := sendCommandUpdate(stream, sessionID, update.commandID, update.updateType, update.resultCode, update.message); err != nil {
				return err
			}
		case cancellation := <-cancellations:
			if err := sendCommandUpdate(stream, sessionID, cancellation.GetCommandId(), pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_CANCELLATION_REJECTED, "NOT_CANCELLABLE", "Vehicle and payload commands cannot be cancelled after delivery"); err != nil {
				return err
			}
		case operation := <-missionOperations:
			if operation.GetDroneId() != localIdentity.DroneID || operation.GetMissionRunId() == "" {
				if err := sendMissionUpdate(stream, sessionID, vehicle.MissionUpdate{EventID: identity.NewID(), OperationID: operation.GetOperationId(), RunID: operation.GetMissionRunId(), Type: "operation_failed", State: "FAILED", ObservedAt: time.Now().UTC(), ErrorCode: "INVALID_TARGET", Message: "Mission operation does not target this drone"}); err != nil {
					return err
				}
				continue
			}
			if time.Now().UTC().UnixMilli() > operation.GetDeadlineAtUnixMs() {
				if err := sendMissionUpdate(stream, sessionID, vehicle.MissionUpdate{EventID: identity.NewID(), OperationID: operation.GetOperationId(), RunID: operation.GetMissionRunId(), Type: "operation_failed", State: "FAILED", ObservedAt: time.Now().UTC(), ErrorCode: "DEADLINE_EXCEEDED", Message: "Mission operation expired before execution"}); err != nil {
					return err
				}
				continue
			}
			operationType := missionOperationTypeName(operation.GetOperationType())
			if operationType == "" {
				if err := sendMissionUpdate(stream, sessionID, vehicle.MissionUpdate{EventID: identity.NewID(), OperationID: operation.GetOperationId(), RunID: operation.GetMissionRunId(), Type: "operation_failed", State: "FAILED", ObservedAt: time.Now().UTC(), ErrorCode: "UNSUPPORTED_OPERATION", Message: "Atlas Agent does not support this mission operation"}); err != nil {
					return err
				}
				continue
			}
			go missionExecutor.Execute(ctx, vehicle.MissionOperation{OperationID: operation.GetOperationId(), RunID: operation.GetMissionRunId(), Type: operationType, MissionPlanJSON: operation.GetMissionPlanJson()})
		case update := <-missionExecutor.Updates():
			frameDemand.setMissionState(update.RunID, update.State)
			if err := sendMissionUpdate(stream, sessionID, update); err != nil {
				return err
			}
		case snapshot, ok := <-updates:
			if !ok {
				updates = nil
				continue
			}
			if err := stream.Send(&pb.AgentToGroundStation{
				SessionId: sessionID,
				Payload:   &pb.AgentToGroundStation_Telemetry{Telemetry: telemetryMessage(snapshot)},
			}); err != nil {
				return fmt.Errorf("send aircraft telemetry: %w", err)
			}
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if err := stream.Send(&pb.AgentToGroundStation{
				SessionId: sessionID,
				Payload: &pb.AgentToGroundStation_StatusText{StatusText: &pb.AgentStatusText{
					ObservedAtUnixMs: event.ObservedAt.UTC().UnixMilli(),
					Source:           event.Source,
					Severity:         event.Severity,
					Text:             event.Text,
				}},
			}); err != nil {
				return fmt.Errorf("send PX4 status text: %w", err)
			}
		case observedAt := <-ticker.C:
			if err := stream.Send(&pb.AgentToGroundStation{
				SessionId: sessionID,
				Payload: &pb.AgentToGroundStation_Heartbeat{Heartbeat: &pb.AgentHeartbeat{
					ObservedAtUnixMs: observedAt.UTC().UnixMilli(),
				}},
			}); err != nil {
				return fmt.Errorf("send agent heartbeat: %w", err)
			}
		}
	}
}

func sendMissionUpdate(stream grpc.BidiStreamingClient[pb.AgentToGroundStation, pb.GroundStationToAgent], sessionID string, update vehicle.MissionUpdate) error {
	return stream.Send(&pb.AgentToGroundStation{
		SessionId: sessionID,
		Payload: &pb.AgentToGroundStation_MissionRunUpdate{MissionRunUpdate: &pb.MissionRunUpdate{
			EventId:          update.EventID,
			OperationId:      update.OperationID,
			MissionRunId:     update.RunID,
			UpdateType:       missionUpdateType(update.Type),
			RunState:         update.State,
			ObservedAtUnixMs: update.ObservedAt.UnixMilli(),
			ProgressPercent:  update.Progress,
			CurrentWaypoint:  update.CurrentWaypoint,
			TotalWaypoints:   update.TotalWaypoints,
			ErrorCode:        update.ErrorCode,
			Message:          update.Message,
			EvidenceJson:     update.EvidenceJSON,
			ActionSequence:   update.ActionSequence,
			ActionType:       update.ActionType,
			ActionState:      missionActionState(update.ActionState),
			ActionAttempt:    update.ActionAttempt,
			FailurePolicy:    update.FailurePolicy,
		}},
	})
}

type commandExecutionUpdate struct {
	commandID  string
	updateType pb.VehicleCommandUpdateType
	resultCode string
	message    string
}

func handleCommand(ctx context.Context, stream grpc.BidiStreamingClient[pb.AgentToGroundStation, pb.GroundStationToAgent], sessionID, droneID string, command *pb.VehicleCommandRequest, executor CommandExecutor, results chan<- commandExecutionUpdate) error {
	if command.GetCommandId() == "" || command.GetDroneId() != droneID {
		return sendCommandUpdate(stream, sessionID, command.GetCommandId(), pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_REJECTED, "INVALID_TARGET", "Command does not target this drone")
	}
	if time.Now().UTC().UnixMilli() > command.GetDeadlineAtUnixMs() {
		return sendCommandUpdate(stream, sessionID, command.GetCommandId(), pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_TIMED_OUT, "DEADLINE_EXCEEDED", "Command expired before execution")
	}
	commandType := commandTypeName(command.GetCommandType())
	if commandType == "" {
		return sendCommandUpdate(stream, sessionID, command.GetCommandId(), pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_REJECTED, "UNSUPPORTED_COMMAND", "Atlas Agent does not support this command")
	}
	if err := sendCommandUpdate(stream, sessionID, command.GetCommandId(), pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_ACCEPTED, "", "Command accepted by Atlas Agent"); err != nil {
		return err
	}
	if err := sendCommandUpdate(stream, sessionID, command.GetCommandId(), pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_EXECUTING, "", "Executing command through MAVSDK"); err != nil {
		return err
	}
	go executeCommand(ctx, command, commandType, executor, results)
	return nil
}

func executeCommand(ctx context.Context, command *pb.VehicleCommandRequest, commandType string, executor CommandExecutor, updates chan<- commandExecutionUpdate) {
	commandContext, cancel := context.WithDeadline(ctx, time.UnixMilli(command.GetDeadlineAtUnixMs()))
	defer cancel()
	result, err := executor.Execute(commandContext, command.GetCommandId(), commandType, command.GetParametersJson())
	update := commandExecutionUpdate{commandID: command.GetCommandId(), resultCode: result.Code}
	switch {
	case err == nil:
		update.updateType = pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_SUCCEEDED
		update.message = result.Message
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(commandContext.Err(), context.DeadlineExceeded):
		update.updateType = pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_TIMED_OUT
		update.resultCode = "MAVSDK_DEADLINE_EXCEEDED"
		update.message = "MAVSDK did not acknowledge the command before its deadline"
	default:
		update.updateType = pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_FAILED
		update.message = err.Error()
	}
	select {
	case updates <- update:
	case <-ctx.Done():
	}
}

func sendCommandUpdate(stream grpc.BidiStreamingClient[pb.AgentToGroundStation, pb.GroundStationToAgent], sessionID, commandID string, updateType pb.VehicleCommandUpdateType, resultCode, message string) error {
	return stream.Send(&pb.AgentToGroundStation{
		SessionId: sessionID,
		Payload: &pb.AgentToGroundStation_CommandUpdate{CommandUpdate: &pb.VehicleCommandUpdate{
			EventId:          identity.NewID(),
			CommandId:        commandID,
			UpdateType:       updateType,
			ObservedAtUnixMs: time.Now().UTC().UnixMilli(),
			ResultCode:       resultCode,
			Message:          message,
		}},
	})
}

func commandTypeName(commandType pb.VehicleCommandType) string {
	switch commandType {
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_HOLD:
		return "hold"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_RETURN_TO_LAUNCH:
		return "return_to_launch"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_LAND:
		return "land"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_GIMBAL_SET_ANGLES:
		return "gimbal_set_angles"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_GIMBAL_SET_RATES:
		return "gimbal_set_rates"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_GIMBAL_CENTER:
		return "gimbal_center"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_PAYLOAD_CONTROL_BEGIN:
		return "payload_control_begin"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_PAYLOAD_CONTROL_RENEW:
		return "payload_control_renew"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_PAYLOAD_CONTROL_END:
		return "payload_control_end"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_GIMBAL_SET_ROI:
		return "gimbal_set_roi"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_CAMERA_SET_ZOOM:
		return "camera_set_zoom"
	default:
		return ""
	}
}

func missionOperationTypeName(operationType pb.MissionOperationType) string {
	switch operationType {
	case pb.MissionOperationType_MISSION_OPERATION_TYPE_UPLOAD:
		return "upload"
	case pb.MissionOperationType_MISSION_OPERATION_TYPE_START:
		return "start"
	case pb.MissionOperationType_MISSION_OPERATION_TYPE_PAUSE:
		return "pause"
	case pb.MissionOperationType_MISSION_OPERATION_TYPE_RESUME:
		return "resume"
	case pb.MissionOperationType_MISSION_OPERATION_TYPE_CANCEL:
		return "cancel"
	case pb.MissionOperationType_MISSION_OPERATION_TYPE_RETURN_TO_LAUNCH:
		return "return_to_launch"
	default:
		return ""
	}
}

func missionUpdateType(value string) pb.MissionRunUpdateType {
	switch value {
	case "operation_accepted":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_OPERATION_ACCEPTED
	case "upload_progress":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_UPLOAD_PROGRESS
	case "uploaded":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_UPLOADED
	case "started":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_STARTED
	case "progress":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_PROGRESS
	case "paused":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_PAUSED
	case "resumed":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_RESUMED
	case "completed":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_COMPLETED
	case "cancelled":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_CANCELLED
	case "rtl_started":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_RTL_STARTED
	case "operation_failed":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_OPERATION_FAILED
	case "arming":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_ARMING
	case "armed":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_ARMED
	case "payload_manual_started":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_PAYLOAD_MANUAL_STARTED
	case "payload_mission_restored":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_PAYLOAD_MISSION_RESTORED
	case "payload_restore_failed":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_PAYLOAD_RESTORE_FAILED
	case "action_state_changed":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_ACTION_STATE_CHANGED
	default:
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_UNSPECIFIED
	}
}

func missionActionState(value string) pb.MissionActionState {
	switch value {
	case "REQUESTED":
		return pb.MissionActionState_MISSION_ACTION_STATE_REQUESTED
	case "RUNNING":
		return pb.MissionActionState_MISSION_ACTION_STATE_RUNNING
	case "RETRYING":
		return pb.MissionActionState_MISSION_ACTION_STATE_RETRYING
	case "SUCCEEDED":
		return pb.MissionActionState_MISSION_ACTION_STATE_SUCCEEDED
	case "FAILED":
		return pb.MissionActionState_MISSION_ACTION_STATE_FAILED
	case "POLICY_APPLIED":
		return pb.MissionActionState_MISSION_ACTION_STATE_POLICY_APPLIED
	default:
		return pb.MissionActionState_MISSION_ACTION_STATE_UNSPECIFIED
	}
}

func registration(cfg config.Config, localIdentity identity.Identity, requestID string, observedAt time.Time, commandCapabilities []string) *pb.AgentRegistration {
	hostname, _ := os.Hostname()
	hardwareID := machineID()
	return &pb.AgentRegistration{
		RegistrationRequestId: requestID,
		InstallationId:        localIdentity.InstallationID,
		AgentVersion:          cfg.AgentVersion,
		ProtocolVersion:       cfg.ProtocolVersion,
		Device: &pb.DeviceProfile{
			DeviceName:       hostname,
			Hostname:         hostname,
			OperatingSystem:  runtime.GOOS,
			Architecture:     runtime.GOARCH,
			HardwareId:       hardwareID,
			HardwareIdSource: machineIDSource(hardwareID),
			TotalMemoryBytes: totalMemoryBytes(),
		},
		Drone: &pb.DroneProfile{
			DroneId:             localIdentity.DroneID,
			Name:                cfg.DroneName,
			FlightControllerUid: cfg.FlightControllerUID,
			SerialNumber:        cfg.FlightControllerSerial,
			VehicleType:         cfg.VehicleType,
		},
		FlightController: &pb.FlightControllerAttachment{
			Transport:           cfg.FlightControllerTransport,
			EndpointDescription: cfg.FlightControllerEndpoint,
			BaudRate:            cfg.FlightControllerBaudRate,
			MavlinkSystemId:     cfg.MAVLinkSystemID,
			MavlinkComponentId:  cfg.MAVLinkComponentID,
		},
		Capabilities:     append([]string{"registration", "heartbeat", "telemetry", "status_text"}, commandCapabilities...),
		ObservedAtUnixMs: observedAt.UnixMilli(),
	}
}

func telemetryMessage(snapshot telemetry.Snapshot) *pb.AircraftTelemetry {
	message := &pb.AircraftTelemetry{
		ObservedAtUnixMs:  snapshot.ObservedAt.UTC().UnixMilli(),
		Source:            snapshot.Source,
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
		SatellitesVisible: snapshot.SatellitesVisible,
		HomePositionSet:   snapshot.HomePositionSet,
		AbsoluteAltitudeM: snapshot.AbsoluteAltitudeM,
		TerrainAltitudeM:  snapshot.TerrainAltitudeM,
		BottomClearanceM:  snapshot.BottomClearanceM,
		VelocityNorthMps:  snapshot.VelocityNorthMPS,
		VelocityEastMps:   snapshot.VelocityEastMPS,
		VelocityDownMps:   snapshot.VelocityDownMPS,
		ClimbRateMps:      snapshot.ClimbRateMPS,
		LandedState:       snapshot.LandedState,
	}
	for _, battery := range snapshot.Batteries {
		message.Batteries = append(message.Batteries, &pb.BatteryTelemetry{
			Id:               battery.ID,
			Function:         battery.Function,
			RemainingPercent: battery.RemainingPercent,
			VoltageV:         battery.VoltageV,
			CurrentA:         battery.CurrentA,
			TemperatureC:     battery.TemperatureC,
			ConsumedAh:       battery.ConsumedAH,
			TimeRemainingS:   battery.TimeRemainingS,
		})
	}
	if health := snapshot.Health; health != nil {
		message.Health = &pb.VehicleHealth{
			GyrometerCalibrationOk:     health.GyrometerCalibrationOK,
			AccelerometerCalibrationOk: health.AccelerometerCalibrationOK,
			MagnetometerCalibrationOk:  health.MagnetometerCalibrationOK,
			LocalPositionOk:            health.LocalPositionOK,
			GlobalPositionOk:           health.GlobalPositionOK,
			HomePositionOk:             health.HomePositionOK,
			Armable:                    health.Armable,
		}
	}
	if rc := snapshot.RCStatus; rc != nil {
		message.RcStatus = &pb.RcStatus{
			Available:             rc.Available,
			WasAvailableOnce:      rc.WasAvailableOnce,
			SignalStrengthPercent: rc.SignalStrengthPercent,
		}
	}
	if home := snapshot.HomePosition; home != nil {
		message.HomePosition = &pb.HomePosition{
			Latitude:          home.Latitude,
			Longitude:         home.Longitude,
			AbsoluteAltitudeM: home.AbsoluteAltitudeM,
			RelativeAltitudeM: home.RelativeAltitudeM,
		}
	}
	if quality := snapshot.GPSQuality; quality != nil {
		message.GpsQuality = &pb.GpsQuality{
			Hdop:                   quality.HDOP,
			Vdop:                   quality.VDOP,
			HorizontalUncertaintyM: quality.HorizontalUncertaintyM,
			VerticalUncertaintyM:   quality.VerticalUncertaintyM,
			VelocityUncertaintyMps: quality.VelocityUncertaintyMPS,
			CourseOverGroundDeg:    quality.CourseOverGroundDegrees,
		}
	}
	return message
}

func machineID() string {
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if value, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(value))
		}
	}
	return ""
}

func machineIDSource(machineID string) string {
	if machineID == "" {
		return ""
	}
	return "linux_machine_id"
}

func totalMemoryBytes() uint64 {
	contents, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return parseTotalMemoryBytes(string(contents))
}

func parseTotalMemoryBytes(meminfo string) uint64 {
	for line := range strings.SplitSeq(meminfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		kilobytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kilobytes * 1024
	}
	return 0
}
