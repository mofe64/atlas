// Package vehicleagentchannel implements the backend side of the long-lived
// gRPC stream used by onboard vehicle agents.
package vehicleagentchannel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
	svc "github.com/sunnyside/atlas/atlas-backend/internal/services"
	pb "github.com/sunnyside/atlas/atlas-backend/internal/transport/vehicleagentchannelpb/atlas"
)

type Hub struct {
	mu          sync.RWMutex
	connections map[string]*connection
	deps        Dependencies
	logger      *slog.Logger
}

// Dependencies are the application services the transport layer calls after it
// has decoded protobuf messages from the vehicle-agent stream.
type Dependencies struct {
	VehicleAgents *svc.VehicleAgentService
	Telemetry     *svc.TelemetryService
	Commands      *svc.CommandService
	Missions      *svc.MissionService
}

// Server implements the generated VehicleAgentChannelServiceServer interface.
// It is intentionally thin: protocol handling stays here, while business rules
// stay in services.
type Server struct {
	pb.UnimplementedVehicleAgentChannelServiceServer

	hub *Hub
}

// connection represents one active backend-to-agent send queue for a vehicle
// agent. The Hub keeps only the latest connection per agent id.
type connection struct {
	agentID string
	send    chan *pb.BackendToVehicleAgent
	done    chan struct{}
	once    sync.Once
}

// NewHub creates the in-memory connection registry used by HTTP handlers and
// the gRPC server to find connected vehicle agents.
func NewHub(deps Dependencies, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}

	return &Hub{
		connections: make(map[string]*connection),
		deps:        deps,
		logger:      logger,
	}
}

// NewServer binds the gRPC server implementation to the shared connection hub.
func NewServer(hub *Hub) *Server {
	return &Server{hub: hub}
}

// DispatchCommand is called by the HTTP API after an operator creates a command.
// If the target vehicle agent is connected, the command is claimed and queued
// for immediate stream delivery.
func (h *Hub) DispatchCommand(ctx context.Context, command models.CommandRequest) (models.CommandRequest, bool) {
	if h.connectionForVehicleAgent(command.VehicleAgentID) == nil {
		return models.CommandRequest{}, false
	}

	claimed, err := h.deps.Commands.ClaimCommandForVehicleAgent(ctx, command.VehicleAgentID, command.ID, time.Now().UTC())
	if err != nil {
		h.logger.Warn("command could not be claimed for gRPC delivery", "command_request_id", command.ID, "vehicle_agent_id", command.VehicleAgentID, "error", err)
		return models.CommandRequest{}, false
	}

	if !h.enqueueCommand(ctx, claimed) {
		h.logger.Warn("connected vehicle-agent command queue is unavailable", "command_request_id", command.ID, "vehicle_agent_id", command.VehicleAgentID)
	}

	return claimed, true
}

// DispatchMissionExecution is called by the HTTP API after an operator requests
// mission upload, mission start, or mission abort. It mirrors command dispatch:
// claim first, then enqueue only if the agent still has a live stream.
func (h *Hub) DispatchMissionExecution(ctx context.Context, execution models.MissionExecution) (models.MissionExecution, bool) {
	if h.connectionForVehicleAgent(execution.VehicleAgentID) == nil {
		return models.MissionExecution{}, false
	}

	claimed, err := h.deps.Missions.ClaimMissionExecutionForVehicleAgent(ctx, execution.VehicleAgentID, execution.ID, time.Now().UTC())
	if err != nil {
		h.logger.Warn("mission execution could not be claimed for gRPC delivery", "execution_id", execution.ID, "vehicle_agent_id", execution.VehicleAgentID, "error", err)
		return models.MissionExecution{}, false
	}

	if !h.enqueueMissionExecution(ctx, claimed) {
		h.logger.Warn("connected vehicle-agent mission execution queue is unavailable", "execution_id", execution.ID, "vehicle_agent_id", execution.VehicleAgentID)
	}

	return claimed, true
}

// DispatchPendingForVehicleAgent drains already-authorized work after an agent
// connects. This is what lets Atlas recover command and mission delivery after
// an agent restart or temporary network drop.
func (h *Hub) DispatchPendingForVehicleAgent(ctx context.Context, agentID string) {
	for ctx.Err() == nil {
		command, ok, err := h.deps.Commands.NextCommandForVehicleAgent(ctx, agentID, time.Now().UTC())
		if err != nil {
			h.logger.Warn("pending command lookup failed", "vehicle_agent_id", agentID, "error", err)
			return
		}

		if !ok {
			break
		}

		if !h.enqueueCommand(ctx, command) {
			h.logger.Warn("pending command could not be enqueued", "command_request_id", command.ID, "vehicle_agent_id", agentID)
			return
		}
	}

	for ctx.Err() == nil {
		execution, ok, err := h.deps.Missions.NextMissionExecutionForVehicleAgent(ctx, agentID, time.Now().UTC())
		if err != nil {
			h.logger.Warn("pending mission execution lookup failed", "vehicle_agent_id", agentID, "error", err)
			return
		}

		if !ok {
			return
		}

		if !h.enqueueMissionExecution(ctx, execution) {
			h.logger.Warn("pending mission execution could not be enqueued", "execution_id", execution.ID, "vehicle_agent_id", agentID)
			return
		}
	}
}

// Connect owns one bidirectional gRPC stream from a vehicle agent. The first
// message must be a hello so the backend can register the agent before it
// accepts status, heartbeat, telemetry, command, or mission traffic.
func (s *Server) Connect(stream pb.VehicleAgentChannelService_ConnectServer) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return err
	}

	agentID := first.GetVehicleAgentId()
	hello := first.GetHello()
	if agentID == "" || hello == nil {
		return errors.New("vehicle-agent channel must start with hello")
	}

	// Registering on hello makes reconnects idempotent and keeps the active
	// agent record aligned with the stream that can receive commands.
	if _, err := s.hub.deps.VehicleAgents.RegisterVehicleAgent(ctx, repository.RegisterVehicleAgentInput{
		VehicleAgentID:      agentID,
		DroneID:             hello.GetDroneId(),
		DroneName:           hello.GetDroneName(),
		VehicleAgentVersion: hello.GetVehicleAgentVersion(),
	}, time.Now().UTC()); err != nil {
		s.hub.logger.Warn("vehicle-agent gRPC registration failed", "vehicle_agent_id", agentID, "error", err)
		return err
	}

	conn := newConnection(agentID)
	s.hub.register(conn)
	defer s.hub.unregister(agentID, conn)
	if _, err := s.hub.deps.VehicleAgents.RecordCommandChannelConnected(ctx, agentID, time.Now().UTC()); err != nil {
		s.hub.logger.Warn("failed to record connected vehicle-agent gRPC channel", "vehicle_agent_id", agentID, "error", err)
	}

	s.hub.logger.Info(
		"vehicle-agent gRPC channel connected",
		"vehicle_agent_id", agentID,
		"drone_id", hello.GetDroneId(),
		"drone_name", hello.GetDroneName(),
		"vehicle_agent_version", hello.GetVehicleAgentVersion(),
	)

	errs := make(chan error, 1)
	go func() {
		errs <- s.receive(stream, agentID)
	}()

	// A newly connected agent may already have work waiting in the database.
	// Delivery is queued outside the registration transaction.
	go s.hub.DispatchPendingForVehicleAgent(ctx, agentID)

	// This select loop is the stream bridge: receive side errors terminate the
	// stream, while queued backend messages are sent to the connected agent.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-conn.done:
			return nil
		case err := <-errs:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case msg := <-conn.send:
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

// receive handles agent-to-backend messages on the stream. Each protobuf oneof
// branch is translated into one service call so transport parsing stays out of
// the application layer.
func (s *Server) receive(stream pb.VehicleAgentChannelService_ConnectServer, agentID string) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}

		// The stream is associated with the agent id from the hello message.
		// Mismatched ids are ignored so one stream cannot mutate another agent's
		// command, telemetry, or mission state.
		if msg.GetVehicleAgentId() != agentID {
			s.hub.logger.Warn("ignoring vehicle-agent channel message with mismatched vehicle agent id", "connected_vehicle_agent_id", agentID, "message_vehicle_agent_id", msg.GetVehicleAgentId())
			continue
		}

		status := msg.GetCommandStatus()
		if status != nil {
			s.recordCommandStatus(stream.Context(), agentID, status)
			continue
		}

		missionStatus := msg.GetMissionExecutionStatus()
		if missionStatus != nil {
			s.recordMissionExecutionStatus(stream.Context(), agentID, missionStatus)
			continue
		}

		heartbeat := msg.GetHeartbeat()
		if heartbeat != nil {
			s.recordHeartbeat(stream.Context(), agentID, heartbeat)
			continue
		}

		telemetry := msg.GetTelemetry()
		if telemetry != nil {
			s.recordTelemetry(stream.Context(), agentID, telemetry)
			continue
		}
	}
}

// recordCommandStatus applies the command state reported by the agent after it
// receives or executes a vehicle command.
func (s *Server) recordCommandStatus(ctx context.Context, agentID string, status *pb.CommandStatus) {
	_, err := s.hub.deps.Commands.UpdateCommandStatus(ctx, repository.UpdateCommandStatusInput{
		VehicleAgentID: agentID,
		CommandID:      status.GetCommandId(),
		State:          models.CommandState(status.GetState()),
		ResultMessage:  status.GetResultMessage(),
	}, time.Now().UTC())
	if err != nil {
		s.hub.logger.Warn("vehicle-agent command status rejected", "vehicle_agent_id", agentID, "command_request_id", status.GetCommandId(), "state", status.GetState(), "error", err)
		return
	}

	s.hub.logger.Info("vehicle-agent command status accepted", "vehicle_agent_id", agentID, "command_request_id", status.GetCommandId(), "state", status.GetState())
}

// recordMissionExecutionStatus applies mission progress or completion reported
// by the agent after mission upload, start, progress, or abort work.
func (s *Server) recordMissionExecutionStatus(ctx context.Context, agentID string, status *pb.MissionExecutionStatus) {
	_, err := s.hub.deps.Missions.UpdateMissionExecutionStatus(ctx, repository.UpdateMissionExecutionStatusInput{
		VehicleAgentID:     agentID,
		ExecutionID:        status.GetExecutionId(),
		State:              models.MissionExecutionState(status.GetState()),
		ResultMessage:      status.GetResultMessage(),
		CurrentMissionItem: int(status.GetCurrentMissionItem()),
		TotalMissionItems:  int(status.GetTotalMissionItems()),
	}, time.Now().UTC())
	if err != nil {
		s.hub.logger.Warn("vehicle-agent mission execution status rejected", "vehicle_agent_id", agentID, "execution_id", status.GetExecutionId(), "state", status.GetState(), "error", err)
		return
	}

	s.hub.logger.Info("vehicle-agent mission execution status accepted", "vehicle_agent_id", agentID, "execution_id", status.GetExecutionId(), "state", status.GetState(), "current_mission_item", status.GetCurrentMissionItem(), "total_mission_items", status.GetTotalMissionItems())
}

// recordHeartbeat refreshes vehicle-agent liveness. The services layer also
// updates the owning drone's last-seen timestamp.
func (s *Server) recordHeartbeat(ctx context.Context, agentID string, heartbeat *pb.Heartbeat) {
	agent, err := s.hub.deps.VehicleAgents.RecordVehicleAgentHeartbeat(ctx, repository.VehicleAgentHeartbeatInput{
		VehicleAgentID:      agentID,
		VehicleAgentVersion: heartbeat.GetVehicleAgentVersion(),
	}, time.Now().UTC())
	if err != nil {
		s.hub.logger.Warn("vehicle-agent gRPC heartbeat rejected", "vehicle_agent_id", agentID, "error", err)
		return
	}

	s.hub.logger.Info("vehicle-agent gRPC heartbeat accepted", "vehicle_agent_id", agent.ID, "drone_id", agent.DroneID)
}

// recordTelemetry validates and stores the latest aircraft state reported by
// the agent. Telemetry can also confirm commands or settle mission aborts.
func (s *Server) recordTelemetry(ctx context.Context, agentID string, telemetry *pb.Telemetry) {
	snapshot, err := telemetryToSnapshot(agentID, telemetry)
	if err != nil {
		s.hub.logger.Warn("vehicle-agent gRPC telemetry rejected", "vehicle_agent_id", agentID, "error", err)
		return
	}

	recorded, err := s.hub.deps.Telemetry.RecordTelemetry(ctx, snapshot, time.Now().UTC())
	if err != nil {
		s.hub.logger.Warn("vehicle-agent gRPC telemetry rejected", "vehicle_agent_id", agentID, "error", err)
		return
	}

	s.hub.logger.Info("vehicle-agent gRPC telemetry accepted", "vehicle_agent_id", agentID, "drone_id", recorded.DroneID, "source", recorded.Source)
}

// telemetryToSnapshot converts the protobuf wire message into the domain model
// accepted by TelemetryService, while enforcing basic transport-level validation.
func telemetryToSnapshot(agentID string, telemetry *pb.Telemetry) (models.TelemetrySnapshot, error) {
	observedAt, err := time.Parse(time.RFC3339Nano, telemetry.GetObservedAt())
	if err != nil {
		return models.TelemetrySnapshot{}, errors.New("observed_at must be an RFC3339 timestamp")
	}

	if telemetry.GetBatteryPercent() < 0 || telemetry.GetBatteryPercent() > 100 {
		return models.TelemetrySnapshot{}, errors.New("battery_percent must be between 0 and 100")
	}

	if telemetry.GetLatitude() < -90 || telemetry.GetLatitude() > 90 {
		return models.TelemetrySnapshot{}, errors.New("latitude must be between -90 and 90")
	}

	if telemetry.GetLongitude() < -180 || telemetry.GetLongitude() > 180 {
		return models.TelemetrySnapshot{}, errors.New("longitude must be between -180 and 180")
	}

	if strings.TrimSpace(telemetry.GetFlightMode()) == "" {
		return models.TelemetrySnapshot{}, errors.New("flight_mode is required")
	}

	if strings.TrimSpace(telemetry.GetGpsFix()) == "" {
		return models.TelemetrySnapshot{}, errors.New("gps_fix is required")
	}

	if strings.TrimSpace(telemetry.GetSource()) == "" {
		return models.TelemetrySnapshot{}, errors.New("source is required")
	}

	return models.TelemetrySnapshot{
		VehicleAgentID:    agentID,
		ObservedAt:        observedAt.UTC(),
		BatteryPercent:    telemetry.GetBatteryPercent(),
		RelativeAltitudeM: telemetry.GetRelativeAltitudeM(),
		FlightMode:        telemetry.GetFlightMode(),
		Armed:             telemetry.GetArmed(),
		InAir:             telemetry.GetInAir(),
		Latitude:          telemetry.GetLatitude(),
		Longitude:         telemetry.GetLongitude(),
		HeadingDeg:        telemetry.GetHeadingDeg(),
		GroundSpeedMPS:    telemetry.GetGroundSpeedMps(),
		GPSFix:            telemetry.GetGpsFix(),
		SatellitesVisible: int(telemetry.GetSatellitesVisible()),
		HomePositionSet:   telemetry.GetHomePositionSet(),
		Source:            telemetry.GetSource(),
	}, nil
}

// register makes this stream the active delivery path for the agent. If an
// older stream exists, it is closed so commands do not go to two places.
func (h *Hub) register(conn *connection) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if existing := h.connections[conn.agentID]; existing != nil {
		existing.close()
	}

	h.connections[conn.agentID] = conn
}

// connectionForVehicleAgent returns the current live stream, if any, for an
// agent. Callers use nil to mean "fall back to persisted pending delivery".
func (h *Hub) connectionForVehicleAgent(agentID string) *connection {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.connections[agentID]
}

// enqueueCommand translates a persisted command request into the protobuf
// envelope consumed by the onboard agent.
func (h *Hub) enqueueCommand(ctx context.Context, command models.CommandRequest) bool {
	conn := h.connectionForVehicleAgent(command.VehicleAgentID)
	if conn == nil {
		return false
	}

	msg := &pb.BackendToVehicleAgent{
		Payload: &pb.BackendToVehicleAgent_Command{
			Command: &pb.CommandEnvelope{
				CommandId:   command.ID,
				DroneId:     command.DroneID,
				CommandType: string(command.Type),
				RequestedBy: command.RequestedBy,
			},
		},
	}

	return conn.enqueue(ctx, msg)
}

// enqueueMissionExecution translates a mission execution into a protobuf
// instruction. The mission definition is loaded here because the agent needs
// the full waypoint payload to upload it to the vehicle.
func (h *Hub) enqueueMissionExecution(ctx context.Context, execution models.MissionExecution) bool {
	conn := h.connectionForVehicleAgent(execution.VehicleAgentID)
	if conn == nil {
		return false
	}

	mission, ok := h.deps.Missions.GetMissionByID(ctx, execution.MissionID)
	if !ok {
		h.logger.Warn("mission execution references missing mission", "execution_id", execution.ID, "mission_id", execution.MissionID)
		return false
	}

	msg := &pb.BackendToVehicleAgent{
		Payload: &pb.BackendToVehicleAgent_MissionExecution{
			MissionExecution: &pb.MissionExecutionEnvelope{
				ExecutionId:      execution.ID,
				MissionId:        execution.MissionID,
				DroneId:          execution.DroneID,
				Action:           missionExecutionAction(execution.State),
				RequestedBy:      execution.RequestedBy,
				Waypoints:        missionWaypointsToProto(mission.Waypoints),
				CompletionAction: string(mission.CompletionAction),
			},
		},
	}

	return conn.enqueue(ctx, msg)
}

// missionExecutionAction maps backend mission states to the small action
// vocabulary understood by the agent.
func missionExecutionAction(state models.MissionExecutionState) string {
	switch state {
	case models.MissionExecutionStateUploadRequested:
		return "upload"
	case models.MissionExecutionStateStartRequested:
		return "start"
	case models.MissionExecutionStateRTLRequested:
		return "return_to_launch"
	default:
		return string(state)
	}
}

// missionWaypointsToProto converts stored mission waypoints into the protobuf
// shape sent over the gRPC stream.
func missionWaypointsToProto(waypoints []models.MissionWaypoint) []*pb.MissionWaypoint {
	res := make([]*pb.MissionWaypoint, 0, len(waypoints))
	for _, waypoint := range waypoints {
		item := &pb.MissionWaypoint{
			Sequence:          int32(waypoint.Sequence),
			Latitude:          waypoint.Latitude,
			Longitude:         waypoint.Longitude,
			RelativeAltitudeM: waypoint.RelativeAltitudeM,
		}
		if waypoint.SpeedMPS != nil {
			item.SpeedMps = waypoint.SpeedMPS
		}
		if waypoint.LoiterTimeS != nil {
			item.LoiterTimeS = waypoint.LoiterTimeS
		}
		res = append(res, item)
	}

	return res
}

// unregister removes a stream only if it is still the active stream for the
// agent. This protects a new reconnect from being removed by an older stream's
// deferred cleanup.
func (h *Hub) unregister(agentID string, conn *connection) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.connections[agentID] == conn {
		delete(h.connections, agentID)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := h.deps.VehicleAgents.RecordCommandChannelDisconnected(ctx, agentID, time.Now().UTC()); err != nil {
			h.logger.Warn("failed to record disconnected vehicle-agent gRPC channel", "vehicle_agent_id", agentID, "error", err)
		}
	}

	conn.close()
	h.logger.Info("vehicle-agent gRPC channel disconnected", "vehicle_agent_id", agentID)
}

// newConnection creates the bounded send queue used by the Connect loop.
func newConnection(agentID string) *connection {
	return &connection{
		agentID: agentID,
		send:    make(chan *pb.BackendToVehicleAgent, 16),
		done:    make(chan struct{}),
	}
}

// enqueue writes a backend-to-agent message unless the request context or the
// connection has already closed.
func (c *connection) enqueue(ctx context.Context, msg *pb.BackendToVehicleAgent) bool {
	select {
	case <-ctx.Done():
		return false
	case <-c.done:
		return false
	case c.send <- msg:
		return true
	}
}

// close broadcasts connection shutdown exactly once.
func (c *connection) close() {
	c.once.Do(func() {
		close(c.done)
	})
}
