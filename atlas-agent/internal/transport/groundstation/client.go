// Package groundstation maintains the agent-initiated session to Atlas Ground Station aka Atlas Native.
package groundstation

// ground-station client is the Atlas Agent’s communications hub.
// Its responsibilities are:
// 1. Maintain an outbound connection from the aircraft to Atlas Native gcs application.
// 2. Register the Agent and drone whenever a new connection starts.
// 3. Send telemetry, heartbeats, PX4 status messages, and operation results.
// 4. Receive vehicle commands, mission operations, reconciliation state, and aircraft-follow instructions.
// 5. Translate protobuf wire messages into Atlas Agent domain types.
// 6. Reconnect when the gcs connection (HM30/Ethernet) link disappears.
// 7. Trigger safe aircraft-follow Hold behavior when the ground link is lost.

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

// Run supervises the Agent's long-lived connection to Atlas GCS (Native).
//
// connect owns one complete session and blocks while that session is active.
// If the session fails, Run waits using bounded exponential backoff and then
// creates a new session. Cancellation of ctx stops both the current session
// and any pending reconnect delay.
func Run(ctx context.Context, logger *slog.Logger, cfg config.Config, localIdentity identity.Identity, telemetryUpdates <-chan telemetry.Snapshot, statusTexts <-chan telemetry.StatusTextEvent, perceptionOutputs perception.Outputs, executor CommandExecutor, missionExecutor MissionExecutor, followExecutor AircraftFollowExecutor) {
	// if logger is not provided, use the default logger
	if logger == nil {
		logger = slog.Default()
	}

	// create shared perception frame demand state
	// Shared by the main session and independent perception stream.
	// It records whether high-rate perception frames currently have a consumer.
	frameDemand := newFrameDemand()

	// set the initial backoff to the minimum retry time
	backoff := minimumRetry

	// below we are attempting to call connect function
	// and then waiting to see if the connection fails
	// and if it does we retry our connection with exponential backoff
	// we keep doing this until the passed in context is cancelled
	for ctx.Err() == nil {

		// This connect functionblocks for the lifetime of the registered gRPC session.
		// while the connection is healthy, execution remains inside the connect function
		// connect contains the event loop that handles
		// - Heartbeat ticks
		// - Telemetry channel values
		// - Status-text channel values
		// - Incoming gRPC requests
		// - Mission updates
		// - Command results
		// - Context cancellation
		// run will only resume when the connect event loop ends
		err := connect(ctx, logger, cfg, localIdentity, telemetryUpdates, statusTexts, perceptionOutputs, executor, missionExecutor, followExecutor, frameDemand)

		// connect can retrun for two broad reasons:
		// 1. unexpcted shutdown because ctx was cancelled
		// 2. unexpected session failure such as a broken network connection

		// A cancelled context (1) means the application is shutting down,
		// rather than experiencing a connection failure.
		// so we return immediately
		if ctx.Err() != nil {
			return
		}

		// for (2) we log the error and retry with exponential backoff
		logger.Warn("ground-station session ended; reconnecting", "error", err, "retry_after", backoff)

		// we use a timer here instead of time.Sleep because, time.sleep can't be interrupted, and if our context
		// is cancelled, we would still have to wait for the sleep to complete before returning
		// our timer allows us to interrupt the sleep if the context is cancelled
		timer := time.NewTimer(backoff)

		// the following select statement says
		// wait for eprteher application cancellaction via context or the timer expires
		// whichever happens first
		select {
		// if the context is cancelled, we stop the timer and return
		case <-ctx.Done():
			timer.Stop()
			return

		// retry delay has elapsed, so we conitnue after the select statement
		// our syntax <-timer.C eceives and discards the timer’s time.Time value.
		// If we needed the exact firing time, we could capture it eg.
		// case firedAt := <-timer.C:
		//     logger.Info("timer fired", "at", firedAt)
		case <-timer.C:
		}

		// we only get here if the timer expired
		// initially our backoff is min retry time (1 second)
		// after each failed session, we double the backoff time
		// and cap it at the maximum retry time (30 seconds)
		// once we reach the max retry time, any subsequent retry will be the same max retry time (30 seconds)
		// note (we do not reset the backoff after a successful session), so any subsequent retry will use
		// the current backoff time.
		backoff *= 2
		if backoff > maximumRetry {
			backoff = maximumRetry
		}
	}
}

// CommandExecutor handles one-shot operations such as Hold, Land, gimbal movement, and camera zoom
type CommandExecutor interface {
	Execute(context.Context, string, string, string) (vehicle.CommandResult, error)
	Capabilities() []string
}

// MissionExecutor handles longer-lived mission operations and exposes an update channel.
type MissionExecutor interface {
	Execute(context.Context, vehicle.MissionOperation)
	Reconcile(context.Context, vehicle.MissionReconciliation)
	Updates() <-chan vehicle.MissionUpdate
	Capabilities() []string
}

// AircraftFollowExecutor owns the high-rate onboard follow loop and exposes follow state changes.
type AircraftFollowExecutor interface {
	Apply(context.Context, vehicle.AircraftFollowOperation)
	GroundLinkLost()
	Updates() <-chan vehicle.AircraftFollowUpdate
	Capabilities() []string
}

func connect(ctx context.Context, logger *slog.Logger, cfg config.Config, localIdentity identity.Identity, telemetryUpdates <-chan telemetry.Snapshot, statusTexts <-chan telemetry.StatusTextEvent, perceptionOutputs perception.Outputs, executor CommandExecutor, missionExecutor MissionExecutor, followExecutor AircraftFollowExecutor, frameDemand *frameDemand) error {
	// phase 1: create grpc client and open session
	// create our grpc client to the ground station
	// no tls, too lazy to set up certs
	connection, err := grpc.NewClient(cfg.GroundStationAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("create ground-station client: %w", err)
	}
	defer connection.Close()

	// create our ground station service client
	client := pb.NewGroundStationServiceClient(connection)
	stream, err := client.OpenSession(ctx) // start bidirectional rpc
	// stream.Send(...) sends Agent messages.
	// stream.Recv() receives Native messages.
	if err != nil {
		return fmt.Errorf("open ground-station session: %w", err)
	}

	//phase 2: application level registration handshake
	// for atlas the rule is that registration must be the first message on every new OpenSession gRPC stream.
	// we create a new session id for every connection
	sessionID := identity.NewID()
	now := time.Now().UTC()
	// collect all the capabilities from our executors (mission, follow, command)
	capabilities := append(executor.Capabilities(), missionExecutor.Capabilities()...)
	capabilities = append(capabilities, followExecutor.Capabilities()...)
	// if perception is enabled, add the perception capabilities
	if cfg.PerceptionEnabled() {
		capabilities = append(capabilities, "perception:object_detection:v1", "perception:health:v1", "perception:frame_subscription:v1")
	}
	// send our registration message to the ground station
	if err := stream.Send(&pb.AgentToGroundStation{
		SessionId: sessionID,
		Payload:   &pb.AgentToGroundStation_Registration{Registration: registration(cfg, localIdentity, sessionID, now, capabilities)},
	}); err != nil {
		return fmt.Errorf("send agent registration: %w", err)
	}

	// the client blocks on the first recv call and expects a registration accepeted response
	// the ground station will
	// 	- Upsert the stable drone.
	// 	- Upsert the Agent installation.
	// 	- Create or reuse their binding.
	// 	- Create a new communication-link record.
	// 	- Register an in-memory route for requests to this drone.
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
	// phase 3: start secondary behavior

	// if anything causes the connect to return,
	// we need to make sure that the follow executor is notified that
	// our connection to the ground station has been lost
	// this will ensure that if the aircraft is currently in follow mode,
	// it will enter a safe degraded hold state
	// if the aircraft is not in follow mode, this is a no-op
	// this is a safety invariant, if the aircraft looses connection
	// to the ground station in follow mode, we essentially have lost control of the aircraft
	// so this ensures that if we loose connection in any instance, the aircraft is in a safe state
	defer followExecutor.GroundLinkLost()

	// create a new context from our existign context for the perception stream
	// we use a seprate stream for perception so that we don't clog our main grpc stream
	// which is responsible for the main mission and command execution
	// This prevents high-rate detection/perception traffic from delaying command acknowledgements or heartbeats.
	perceptionContext, cancelPerception := context.WithCancel(ctx)
	defer cancelPerception()
	// if perception is enabled, start the perception stream
	if cfg.PerceptionEnabled() {
		go runPerception(perceptionContext, logger, client, cfg, localIdentity, sessionID, perceptionOutputs, frameDemand)
	}

	// phase 4: Receive goroutine

	// create channels for the different types of messages we will receive from the ground station or process within our event loop
	receiveErrors := make(chan error, 1)
	commandRequests := make(chan *pb.VehicleCommandRequest, 4)
	commandResults := make(chan commandExecutionUpdate, 8)
	cancellations := make(chan *pb.VehicleCommandCancellation, 4)
	missionOperations := make(chan *pb.MissionOperationRequest, 4)
	missionReconciliations := make(chan *pb.MissionReconciliationRequest, 2)
	aircraftFollowRequests := make(chan *pb.AircraftFollowControlRequest, 8)

	// start the receive goroutine
	// this continually calls recv and seperates the different types of messages into their respective channels
	go func() {
		for {
			message, err := stream.Recv()
			if err != nil {
				// if we receive an error, we send it to the receiveErrors channel
				// and return, causing the receive goroutine to exit
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
			case *pb.GroundStationToAgent_MissionReconciliationRequest:
				missionReconciliations <- payload.MissionReconciliationRequest
			case *pb.GroundStationToAgent_AircraftFollowControlRequest:
				aircraftFollowRequests <- payload.AircraftFollowControlRequest
			}
		}
	}()

	interval := cfg.HeartbeatInterval
	if interval <= 0 {
		// if the heartbeat interval is not set, we use a default of 5 seconds
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// create local refs for the telemetry and status text channels
	updates := telemetryUpdates
	events := statusTexts

	// main event loop
	// acts as the agents receptionist and dispatcher
	// while the agent is connected, several things may happen at a time,
	// we can get a command from the found station, telem might arrive etc
	// our select lets one goroutine, wait for all those things simultaneously
	// long lived work is moved into goroutines, so we can quickly return to the select statement, and wait for the next event
	// the loop handles three broad classes of event:
	// - Lifecycle: shutdown and connection failure.
	// - Native-to-Agent control: commands, missions, reconciliation, follow.
	// - Agent-to-Native observations/results: telemetry, status, heartbeats, mission and command updates.
	// for any send err, which occurs in the lifetime of the main event loop, we exit the loop and return the error
	// this will cause the connect to return, and the cleanup process to begin
	// the run function will log the failure and then attempt to reconnect using our exponential backoff strategy
	for {
		select {
		// application shutdown via context
		case <-ctx.Done():
			return ctx.Err()
		// grpc recieve failure
		case err := <-receiveErrors:
			return err
		// vehicle command arrives
		case command := <-commandRequests:
			logger.Info("vehicle command received", "command_id", command.GetCommandId(), "command_type", command.GetCommandType().String())
			// handle command validates the command, validates the command is for the corretct drone, and hasn't had its deadline exceeded
			// then sends accepted , and executing updates back the ground station directly through the grpc stream
			// and then its starts the command execution in a new goroutine
			// we do not block on the command execution, because commands like land or RTL, take a while to complete,
			// and we want to allow the agent to continue to process other messages, and send updates to the ground station
			if err := handleCommand(ctx, stream, sessionID, localIdentity.DroneID, command, executor, commandResults); err != nil {
				return err
			}
		// vehicle command finishes
		// this is the other half of command exectution
		// multiple command goroutines may exist at the same time, but the concrete ActionExecutor
		// serializes their execution with a mutex, so aircraft and payload commands run one at a time
		// we route their final results back through the event loop so command goroutines do not
		// independently write to the main grpc stream and network output remains coordinated
		case update := <-commandResults:
			// the execution goroutine does not send directly to Native. Instead, it places a commandExecutionUpdate into commandResults.
			logger.Info("vehicle command completed", "command_id", update.commandID, "result", update.updateType.String(), "result_code", update.resultCode)
			// creats the protobud message, add unique event id and timestamp and calls stream.Send() to send it to the ground station
			if err := sendCommandUpdateWithEvidence(stream, sessionID, update.commandID, update.updateType, update.resultCode, update.message, update.evidenceJSON); err != nil {
				return err
			}

		// ground stattion asks to cancel a command, we reject it since we don't support cancellation once it has been delivered to the aircraft (px4 is already executing it)
		// this is legacy functionality,
		// ideally since we don't allow cancellation once it has been delivered,
		// we should not allow the ground station to request cancellation at all, once the message has been delivered to the aircraft
		// cancellation in that case becomes a ground station only operation for when the aircraft has not received a command, that means
		// we have the following todo:
		// - remove the cancellation channel and its semantics from the client and cancellation messages
		// - for messages that have already been delivered to the aircraft, we should not allow the ground station to request cancellation
		// - for messages that have not been delivered to the aircraft, the ground station can cancel it locally without involving the agent
		case cancellation := <-cancellations:
			if err := sendCommandUpdate(stream, sessionID, cancellation.GetCommandId(), pb.VehicleCommandUpdateType_VEHICLE_COMMAND_UPDATE_TYPE_CANCELLATION_REJECTED, "NOT_CANCELLABLE", "Vehicle and payload commands cannot be cancelled after delivery"); err != nil {
				return err
			}
		// mission operation arrives eg upload, start, pause, resume, cancel, rtl mission
		case operation := <-missionOperations:
			// we validate the operation is for the correct drone, and has a mission run id
			if operation.GetDroneId() != localIdentity.DroneID || operation.GetMissionRunId() == "" {
				if err := sendMissionUpdate(stream, sessionID, vehicle.MissionUpdate{EventID: identity.NewID(), OperationID: operation.GetOperationId(), RunID: operation.GetMissionRunId(), Type: "operation_failed", State: "FAILED", ObservedAt: time.Now().UTC(), ErrorCode: "INVALID_TARGET", Message: "Mission operation does not target this drone"}); err != nil {
					return err
				}
				continue
			}
			// if the operation's deadline has been exceeded, becuase it was submitted to the agent too late after its creation
			// then we reject the operation, because the agent must not execute stale intent
			if time.Now().UTC().UnixMilli() > operation.GetDeadlineAtUnixMs() {
				if err := sendMissionUpdate(stream, sessionID, vehicle.MissionUpdate{EventID: identity.NewID(), OperationID: operation.GetOperationId(), RunID: operation.GetMissionRunId(), Type: "operation_failed", State: "FAILED", ObservedAt: time.Now().UTC(), ErrorCode: "DEADLINE_EXCEEDED", Message: "Mission operation expired before execution"}); err != nil {
					return err
				}
				continue
			}
			// we validate the operation type is supported
			operationType := missionOperationTypeName(operation.GetOperationType())
			if operationType == "" {
				if err := sendMissionUpdate(stream, sessionID, vehicle.MissionUpdate{EventID: identity.NewID(), OperationID: operation.GetOperationId(), RunID: operation.GetMissionRunId(), Type: "operation_failed", State: "FAILED", ObservedAt: time.Now().UTC(), ErrorCode: "UNSUPPORTED_OPERATION", Message: "Atlas Agent does not support this mission operation"}); err != nil {
					return err
				}
				continue
			}
			// we begin the execution for the supplied mission operation in a new goroutine
			// the mission executor later published lifecycyle updates through the missionExecutor.Updates() channel
			// those updates are handled by another case in this event loop
			go missionExecutor.Execute(ctx, vehicle.MissionOperation{OperationID: operation.GetOperationId(), RunID: operation.GetMissionRunId(), Type: operationType, MissionPlanJSON: operation.GetMissionPlanJson()})

		// reconcilliation is all about revovering mission state after the agent reconnects
		// in a situation where the agent disconnects mid-mission, the ground station still has durable mission state, stored in local db,
		// so the ground station sends a reconcilliation request that basically says
		// "hey, my db says mission run x was running, it was at waypoint ym and these arrival actions had reached these specific states,please, check the aircraft and rebuild the local runtime state"
		case reconciliation := <-missionReconciliations:
			// we validate the reconciliation is for the correct drone, and has a mission run id
			if reconciliation.GetDroneId() != localIdentity.DroneID || reconciliation.GetMissionRunId() == "" {
				if err := sendMissionUpdate(stream, sessionID, vehicle.MissionUpdate{EventID: identity.NewID(), OperationID: reconciliation.GetReconciliationId(), RunID: reconciliation.GetMissionRunId(), Type: "reconciliation_failed", State: reconciliation.GetRunState(), ObservedAt: time.Now().UTC(), ErrorCode: "INVALID_TARGET", Message: "Mission reconciliation does not target this drone"}); err != nil {
					return err
				}
				continue
			}
			// validate the reconciliation deadline has not been exceeded
			if time.Now().UTC().UnixMilli() > reconciliation.GetDeadlineAtUnixMs() {
				if err := sendMissionUpdate(stream, sessionID, vehicle.MissionUpdate{EventID: identity.NewID(), OperationID: reconciliation.GetReconciliationId(), RunID: reconciliation.GetMissionRunId(), Type: "reconciliation_failed", State: reconciliation.GetRunState(), ObservedAt: time.Now().UTC(), ErrorCode: "DEADLINE_EXCEEDED", Message: "Mission reconciliation expired before execution"}); err != nil {
					return err
				}
				continue
			}
			// convert the provided protobuf actions into our agent internal mission action checkpoint struct
			checkpoints := make([]vehicle.MissionActionCheckpoint, 0, len(reconciliation.GetActions()))
			for _, checkpoint := range reconciliation.GetActions() {
				checkpoints = append(checkpoints, vehicle.MissionActionCheckpoint{
					Sequence:          checkpoint.GetActionSequence(),
					ActionType:        checkpoint.GetActionType(),
					State:             checkpoint.GetState(),
					Attempt:           checkpoint.GetAttempt(),
					AttemptDeadlineAt: unixMillisecondTime(checkpoint.AttemptDeadlineAtUnixMs),
					NextAttemptAt:     unixMillisecondTime(checkpoint.NextAttemptAtUnixMs),
				})
			}
			// begin the reconciliation in a new goroutine
			// our reconciliation does the following:
			// - Parses the ground station's immutable mission plan.
			// - Downloads the currently loaded mission from PX4 through MAVSDK.
			// - Compares the PX4 mission with the ground station's plan.
			// - Refuses automatic recovery if they do not match.
			// - Rebuilds runtime action states.
			// - Restores mission payload/perception ownership where appropriate.
			// - Restarts mission or recovery watchers.
			// - Emits either: reconciliation_accepted or reconciliation_failed
			go missionExecutor.Reconcile(ctx, vehicle.MissionReconciliation{
				ReconciliationID: reconciliation.GetReconciliationId(),
				RunID:            reconciliation.GetMissionRunId(),
				State:            reconciliation.GetRunState(),
				MissionPlanJSON:  reconciliation.GetMissionPlanJson(),
				CurrentWaypoint:  reconciliation.CurrentWaypoint,
				TotalWaypoints:   reconciliation.GetTotalWaypoints(),
				Actions:          checkpoints,
			})

		// aircraft follow request arrives eg start, hold, end, renew
		// aircraft follow is a dedicated navigation authority that temporarily owns PX4 Offboard movement
		// it navigates the aircraft relative to a specific target, but does not rewrite or replace the immutable mission plan
		// the incoming request contains
		// - Exact track identity.
		// - World-space target location and velocity.
		// - Uncertainty values.
		// - Operator lease expiry.
		// - Reviewed altitude, speed, battery, and boundary limits.
		case request := <-aircraftFollowRequests:
			if request.GetDroneId() != localIdentity.DroneID || request.GetFollowSessionId() == "" {
				if err := sendAircraftFollowUpdate(stream, sessionID, vehicle.AircraftFollowUpdate{
					EventID: identity.NewID(), OperationID: request.GetOperationId(), SessionID: request.GetFollowSessionId(),
					State: "DEGRADED_HOLD", ObservedAt: time.Now().UTC(), ReasonCode: "INVALID_TARGET",
					Message: "Aircraft follow request does not target this drone", EvidenceJSON: "{}",
				}); err != nil {
					return err
				}
				continue
			}
			// convert the incoming request into our agent internal aircraft follow operation struct
			operation, err := aircraftFollowOperation(request)
			if err != nil {
				if sendErr := sendAircraftFollowUpdate(stream, sessionID, vehicle.AircraftFollowUpdate{
					EventID: identity.NewID(), OperationID: request.GetOperationId(), SessionID: request.GetFollowSessionId(),
					State: "DEGRADED_HOLD", ObservedAt: time.Now().UTC(), ReasonCode: "INVALID_FOLLOW_REQUEST",
					Message: err.Error(), EvidenceJSON: "{}",
				}); sendErr != nil {
					return sendErr
				}
				continue
			}
			// our apply method does deeper safety checks and publishes a follow update for both success and failure
			// successful requests can emit ACQUIRING or FOLLOWING, while rejected or unsafe requests emit DEGRADED_HOLD
			followExecutor.Apply(ctx, operation)
		// follow update arrives eg acquiring, following, degraded_hold, ended
		// VALIDATING exists in the protocol, but the current controller validates synchronously and does not emit that state
		// this is the other half of aircraft follow execution
		case update := <-followExecutor.Updates():
			if err := sendAircraftFollowUpdate(stream, sessionID, update); err != nil {
				return err
			}
		// mission executor emits an update
		// this is the return path for both mission execution and reconciliation
		case update := <-missionExecutor.Updates():
			// here we update our frame demand to reflect the current mission state
			// this updated state will be used by the frame demand to determine if we need to send perception frames to the ground station
			// or if we can stop sending them
			frameDemand.setMissionState(update.RunID, update.State)
			if err := sendMissionUpdate(stream, sessionID, update); err != nil {
				return err
			}
		// aircraft telemetry arrives eg position, velocity, altitude, heading, battery, etc
		// roll, pitch, and yaw attitude are not included in the telemetry snapshot sent through this channel
		// our updates channel refs the channel produced by the mavsdk telemetry subsystem
		// our event loop here
		// 	- Receives a telemetry.Snapshot.
		// 	- Converts it into protobuf with telemetryMessage.
		// 	- Wraps it in AgentToGroundStation.
		// 	- Sends it to the ground station.
		// we added ok to this case statement, to handle the case where the updates channel is closed
		case snapshot, ok := <-updates:
			if !ok {
				// the update channel is managed externally by the mavsdk telemetry subsystem
				// when the telem subsystem closes the channel for any reason (usually when we shutdown the agent),
				// we need to handle this case defensively.
				// ideally, if an error occurs in the telem subsystem the channel remains open, and it will retry
				// howvever, when we the shutdown the agent, we have a race condition between the telem subsystem closing the channel
				// and the ctx.done() signal. if the ctx.done arrive first, the select statement exits as expected,
				// but if the telem subsystem closes the channel first, we might get the zero value snapshot and create a fast infinite loop
				// to avoid this, we check for the closed ok flag before accessing the snapshot. if it is closed, we set the updates channel to nil, and continue to the next iteration of the loop
				// since receiving from a nil channel blocks forever, inside our select statement, that means that this case is disabled.
				// if we don't do this, the closed channel will return a zero value snapshot repeatedly and create a fast infinite loop.
				updates = nil
				continue
			}
			// here we send the telemetry update to the ground station
			if err := stream.Send(&pb.AgentToGroundStation{
				SessionId: sessionID,
				Payload:   &pb.AgentToGroundStation_Telemetry{Telemetry: telemetryMessage(snapshot)},
			}); err != nil {
				return fmt.Errorf("send aircraft telemetry: %w", err)
			}
		// PX4 status-text event arrives eg mission progress, warnings, errors, etc
		// these are handled separately from telemetry because they are events, not latest-value state.
		// our event loop here
		// 	- Receives a telemetry.StatusTextEvent.
		// 	- Constructs the protobuf AgentStatusText message inline.
		// 	- Wraps it in AgentToGroundStation.
		// 	- Sends it to the ground station.
		// we do the same defensive ok check for the events channel as we did for the updates channel
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
		// heartbeat timer fires
		// our heartbeat ticker normally fires every 5 seconds
		// it shows that the registered Agent session is alive and can still send to Native
		// it does not prove that PX4, MAVSDK, telemetry, or the aircraft are healthy
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

// translates the internal vehicle.MissionUpdate into its protobuf equivalent.
// It preserves two distinct ideas:
// - UpdateType: what just happened, such as progress or operation failure.
// - RunState: the mission's state after that event, such as RUNNING or PAUSED.
// this distinction matters. A failed pause request can be an operation_failed event while the underlying run remains RUNNING.
// it also carries durable action details such as sequence, attempt, action state, failure policy, and evidence.
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

// converts an onboard follow-controller update into a protobuf message.
func sendAircraftFollowUpdate(stream grpc.BidiStreamingClient[pb.AgentToGroundStation, pb.GroundStationToAgent], sessionID string, update vehicle.AircraftFollowUpdate) error {
	return stream.Send(&pb.AgentToGroundStation{
		SessionId: sessionID,
		Payload: &pb.AgentToGroundStation_AircraftFollowSessionUpdate{AircraftFollowSessionUpdate: &pb.AircraftFollowSessionUpdate{
			EventId: update.EventID, OperationId: update.OperationID, FollowSessionId: update.SessionID,
			UpdateType: aircraftFollowUpdateType(update.State), ObservedAtUnixMs: update.ObservedAt.UnixMilli(),
			ReasonCode: update.ReasonCode, Message: update.Message, EvidenceJson: update.EvidenceJSON,
		}},
	})
}

// translates a protobuf follow request into vehicle.AircraftFollowOperation
// will reject requests that lack a valid action, target state, and reviewed safety envelope.
// The function copies the complete reviewed envelope and exact target identity. This prevents the onboard controller from silently switching to another nearby track.
// It performs structural validation only. Detailed safety validation such as fresh telemetry, battery, boundary, target freshness, Offboard state is performed by AircraftFollowController.
func aircraftFollowOperation(request *pb.AircraftFollowControlRequest) (vehicle.AircraftFollowOperation, error) {
	action := aircraftFollowActionName(request.GetAction())
	if action == "" {
		return vehicle.AircraftFollowOperation{}, errors.New("aircraft follow control action is unspecified")
	}
	envelope, target := request.GetEnvelope(), request.GetTarget()
	if envelope == nil || target == nil {
		return vehicle.AircraftFollowOperation{}, errors.New("aircraft follow control is missing envelope or target state")
	}
	return vehicle.AircraftFollowOperation{
		OperationID: request.GetOperationId(), SessionID: request.GetFollowSessionId(), DroneID: request.GetDroneId(), Action: action,
		Envelope: vehicle.AircraftFollowEnvelope{
			StandoffM: envelope.GetStandoffM(), AltitudeRelativeM: envelope.GetAltitudeRelativeM(),
			MinimumAltitudeRelativeM: envelope.GetMinimumAltitudeRelativeM(), MaximumAltitudeRelativeM: envelope.GetMaximumAltitudeRelativeM(),
			MaximumGroundSpeedMPS: envelope.GetMaximumGroundSpeedMS(), MaximumAccelerationMPS2: envelope.GetMaximumAccelerationMS2(),
			MaximumDuration:        time.Duration(envelope.GetMaximumDurationMs()) * time.Millisecond,
			BoundaryCenterLatitude: envelope.GetBoundaryCenterLatitude(), BoundaryCenterLongitude: envelope.GetBoundaryCenterLongitude(),
			BoundaryRadiusM: envelope.GetBoundaryRadiusM(), MinimumBatteryPercent: envelope.GetMinimumBatteryPercent(),
			MinimumTrackConfidence: envelope.GetMinimumTrackConfidence(), MaximumGeolocationUncertaintyM: envelope.GetMaximumGeolocationUncertaintyM(),
			MaximumVelocityUncertaintyMPS: envelope.GetMaximumVelocityUncertaintyMS(),
		},
		Target: vehicle.AircraftFollowTarget{
			GeolocationID: target.GetGeolocationId(), SelectionID: target.GetSelectionId(), SourceID: target.GetSourceId(),
			TrackSessionID: target.GetTrackSessionId(), TrackID: target.GetTrackId(), ObservedAt: time.UnixMilli(target.GetObservedAtUnixMs()).UTC(),
			Latitude: target.GetLatitude(), Longitude: target.GetLongitude(), AltitudeAMSLM: target.GetAltitudeAmslM(),
			VelocityNorthMPS: target.GetVelocityNorthMS(), VelocityEastMPS: target.GetVelocityEastMS(),
			HorizontalUncertaintyM: target.GetHorizontalUncertaintyM(), VelocityUncertaintyMPS: target.GetVelocityUncertaintyMS(),
			TrackConfidence: target.GetTrackConfidence(), LifecycleState: target.GetLifecycleState(), MotionStatus: target.GetMotionStatus(),
		},
		LeaseExpiresAt: time.UnixMilli(request.GetOperatorLeaseExpiresAtUnixMs()).UTC(), ReasonCode: request.GetReasonCode(),
		Reason: request.GetReason(),
	}, nil
}

func aircraftFollowActionName(action pb.AircraftFollowControlAction) string {
	switch action {
	case pb.AircraftFollowControlAction_AIRCRAFT_FOLLOW_CONTROL_ACTION_START:
		return "start"
	case pb.AircraftFollowControlAction_AIRCRAFT_FOLLOW_CONTROL_ACTION_RENEW:
		return "renew"
	case pb.AircraftFollowControlAction_AIRCRAFT_FOLLOW_CONTROL_ACTION_HOLD:
		return "hold"
	case pb.AircraftFollowControlAction_AIRCRAFT_FOLLOW_CONTROL_ACTION_END:
		return "end"
	default:
		return ""
	}
}

func aircraftFollowUpdateType(state string) pb.AircraftFollowSessionUpdateType {
	switch state {
	case "VALIDATING":
		return pb.AircraftFollowSessionUpdateType_AIRCRAFT_FOLLOW_SESSION_UPDATE_TYPE_VALIDATING
	case "ACQUIRING":
		return pb.AircraftFollowSessionUpdateType_AIRCRAFT_FOLLOW_SESSION_UPDATE_TYPE_ACQUIRING
	case "FOLLOWING":
		return pb.AircraftFollowSessionUpdateType_AIRCRAFT_FOLLOW_SESSION_UPDATE_TYPE_FOLLOWING
	case "DEGRADED_HOLD":
		return pb.AircraftFollowSessionUpdateType_AIRCRAFT_FOLLOW_SESSION_UPDATE_TYPE_DEGRADED_HOLD
	case "ENDED":
		return pb.AircraftFollowSessionUpdateType_AIRCRAFT_FOLLOW_SESSION_UPDATE_TYPE_ENDED
	default:
		return pb.AircraftFollowSessionUpdateType_AIRCRAFT_FOLLOW_SESSION_UPDATE_TYPE_UNSPECIFIED
	}
}

type commandExecutionUpdate struct {
	commandID    string
	updateType   pb.VehicleCommandUpdateType
	resultCode   string
	message      string
	evidenceJSON string
}

// validates and begins a vehicle command execution.
// will send accepted and executing command updates to ground station
// before actually executing the command through the executor.
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

// invokes the command executor which use mavsdk to execute the requested command
func executeCommand(ctx context.Context, command *pb.VehicleCommandRequest, commandType string, executor CommandExecutor, updates chan<- commandExecutionUpdate) {
	// create a execution context for the command based off the command request deadline
	commandContext, cancel := context.WithDeadline(ctx, time.UnixMilli(command.GetDeadlineAtUnixMs()))
	defer cancel()
	// execute the command through the executor
	result, err := executor.Execute(commandContext, command.GetCommandId(), commandType, command.GetParametersJson())
	// create a command execution update based off the result
	update := commandExecutionUpdate{commandID: command.GetCommandId(), resultCode: result.Code, evidenceJSON: result.EvidenceJSON}
	// use the returned error, to determine the update type and result code, and message
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
	// place the resolved update on the updates channel, or if the context is done, discard the update
	select {
	case updates <- update:
	case <-ctx.Done():
	}
}

// sends a command update to the ground station, does not include any evidence json
func sendCommandUpdate(stream grpc.BidiStreamingClient[pb.AgentToGroundStation, pb.GroundStationToAgent], sessionID, commandID string, updateType pb.VehicleCommandUpdateType, resultCode, message string) error {
	return sendCommandUpdateWithEvidence(stream, sessionID, commandID, updateType, resultCode, message, "")
}

// sends a command update to the ground station, includes evidence json
func sendCommandUpdateWithEvidence(stream grpc.BidiStreamingClient[pb.AgentToGroundStation, pb.GroundStationToAgent], sessionID, commandID string, updateType pb.VehicleCommandUpdateType, resultCode, message, evidenceJSON string) error {
	return stream.Send(&pb.AgentToGroundStation{
		SessionId: sessionID,
		Payload: &pb.AgentToGroundStation_CommandUpdate{CommandUpdate: &pb.VehicleCommandUpdate{
			EventId:          identity.NewID(),
			CommandId:        commandID,
			UpdateType:       updateType,
			ObservedAtUnixMs: time.Now().UTC().UnixMilli(),
			ResultCode:       resultCode,
			Message:          message,
			EvidenceJson:     evidenceJSON,
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
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_GIMBAL_FOLLOW_START:
		return "gimbal_follow_start"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_GIMBAL_FOLLOW_STOP:
		return "gimbal_follow_stop"
	case pb.VehicleCommandType_VEHICLE_COMMAND_TYPE_GEOLOCATE_SELECTED_TRACK:
		return "geolocate_selected_track"
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
	case "reconciliation_accepted":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_RECONCILIATION_ACCEPTED
	case "reconciliation_failed":
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_RECONCILIATION_FAILED
	default:
		return pb.MissionRunUpdateType_MISSION_RUN_UPDATE_TYPE_UNSPECIFIED
	}
}

func unixMillisecondTime(value *int64) *time.Time {
	if value == nil {
		return nil
	}
	parsed := time.UnixMilli(*value).UTC()
	return &parsed
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

// builds the registration payload. This is sent to the ground station when the agent first connects.
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

// converts a transport-independent telemetry.Snapshot into protobuf AircraftTelemetry.
// includes all the telemetry data from the snapshot.
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

// reads the machine ID from the system, this is used to identify the device.
// if the machine ID is not found, an empty string is returned.
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

// reads the total memory bytes from the system, this is used to estimate the device memory.
func totalMemoryBytes() uint64 {
	contents, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return parseTotalMemoryBytes(string(contents))
}

// parses the total memory bytes from the system, this is used to estimate the device memory.
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
