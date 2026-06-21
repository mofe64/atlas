package agentchannel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	pb "github.com/sunnyside/atlas/atlas-backend/internal/agentchannelpb/atlas"
	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/registry"
)

type Hub struct {
	mu          sync.RWMutex
	connections map[string]*connection
	registry    registry.Store
	logger      *slog.Logger
}

type Server struct {
	pb.UnimplementedAgentChannelServiceServer

	hub *Hub
}

type connection struct {
	agentID string
	send    chan *pb.BackendToAgent
	done    chan struct{}
	once    sync.Once
}

func NewHub(reg registry.Store, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}

	return &Hub{
		connections: make(map[string]*connection),
		registry:    reg,
		logger:      logger,
	}
}

func NewServer(hub *Hub) *Server {
	return &Server{hub: hub}
}

func (h *Hub) DispatchCommand(ctx context.Context, command domain.OperatorCommand) (domain.OperatorCommand, bool) {
	if h.connectionForAgent(command.AgentID) == nil {
		return domain.OperatorCommand{}, false
	}

	claimed, err := h.registry.ClaimCommandForAgent(command.AgentID, command.ID, time.Now().UTC())
	if err != nil {
		h.logger.Warn("command could not be claimed for gRPC delivery", "command_id", command.ID, "agent_id", command.AgentID, "error", err)
		return domain.OperatorCommand{}, false
	}

	if !h.enqueueCommand(ctx, claimed) {
		h.logger.Warn("connected agent command queue is unavailable", "command_id", command.ID, "agent_id", command.AgentID)
	}

	return claimed, true
}

func (h *Hub) DispatchMissionExecution(ctx context.Context, execution domain.MissionExecution) (domain.MissionExecution, bool) {
	if h.connectionForAgent(execution.AgentID) == nil {
		return domain.MissionExecution{}, false
	}

	claimed, err := h.registry.ClaimMissionExecutionForAgent(execution.AgentID, execution.ID, time.Now().UTC())
	if err != nil {
		h.logger.Warn("mission execution could not be claimed for gRPC delivery", "execution_id", execution.ID, "agent_id", execution.AgentID, "error", err)
		return domain.MissionExecution{}, false
	}

	if !h.enqueueMissionExecution(ctx, claimed) {
		h.logger.Warn("connected agent mission execution queue is unavailable", "execution_id", execution.ID, "agent_id", execution.AgentID)
	}

	return claimed, true
}

func (h *Hub) DispatchPendingForAgent(ctx context.Context, agentID string) {
	for ctx.Err() == nil {
		command, ok, err := h.registry.NextCommandForAgent(agentID, time.Now().UTC())
		if err != nil {
			h.logger.Warn("pending command lookup failed", "agent_id", agentID, "error", err)
			return
		}

		if !ok {
			break
		}

		if !h.enqueueCommand(ctx, command) {
			h.logger.Warn("pending command could not be enqueued", "command_id", command.ID, "agent_id", agentID)
			return
		}
	}

	for ctx.Err() == nil {
		execution, ok, err := h.registry.NextMissionExecutionForAgent(agentID, time.Now().UTC())
		if err != nil {
			h.logger.Warn("pending mission execution lookup failed", "agent_id", agentID, "error", err)
			return
		}

		if !ok {
			return
		}

		if !h.enqueueMissionExecution(ctx, execution) {
			h.logger.Warn("pending mission execution could not be enqueued", "execution_id", execution.ID, "agent_id", agentID)
			return
		}
	}
}

func (s *Server) Connect(stream pb.AgentChannelService_ConnectServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}

	agentID := first.GetAgentId()
	hello := first.GetHello()
	if agentID == "" || hello == nil {
		return errors.New("agent channel must start with hello")
	}

	s.hub.registry.RegisterAgent(registry.RegisterAgentInput{
		AgentID:      agentID,
		DroneID:      hello.GetDroneId(),
		DroneName:    hello.GetDroneName(),
		AgentVersion: hello.GetAgentVersion(),
	}, time.Now().UTC())

	conn := newConnection(agentID)
	s.hub.register(conn)
	defer s.hub.unregister(agentID, conn)
	if _, err := s.hub.registry.RecordCommandChannelConnected(agentID, time.Now().UTC()); err != nil {
		s.hub.logger.Warn("failed to record connected agent gRPC channel", "agent_id", agentID, "error", err)
	}

	s.hub.logger.Info(
		"agent gRPC channel connected",
		"agent_id", agentID,
		"drone_id", hello.GetDroneId(),
		"drone_name", hello.GetDroneName(),
		"agent_version", hello.GetAgentVersion(),
	)

	errs := make(chan error, 1)
	go func() {
		errs <- s.receive(stream, agentID)
	}()

	go s.hub.DispatchPendingForAgent(stream.Context(), agentID)

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
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

func (s *Server) receive(stream pb.AgentChannelService_ConnectServer, agentID string) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}

		if msg.GetAgentId() != agentID {
			s.hub.logger.Warn("ignoring agent channel message with mismatched agent id", "connected_agent_id", agentID, "message_agent_id", msg.GetAgentId())
			continue
		}

		status := msg.GetCommandStatus()
		if status != nil {
			s.recordCommandStatus(agentID, status)
			continue
		}

		missionStatus := msg.GetMissionExecutionStatus()
		if missionStatus != nil {
			s.recordMissionExecutionStatus(agentID, missionStatus)
			continue
		}

		heartbeat := msg.GetHeartbeat()
		if heartbeat != nil {
			s.recordHeartbeat(agentID, heartbeat)
			continue
		}

		telemetry := msg.GetTelemetry()
		if telemetry != nil {
			s.recordTelemetry(agentID, telemetry)
			continue
		}
	}
}

func (s *Server) recordCommandStatus(agentID string, status *pb.CommandStatus) {
	_, err := s.hub.registry.UpdateCommandStatus(registry.UpdateCommandStatusInput{
		AgentID:       agentID,
		CommandID:     status.GetCommandId(),
		State:         domain.CommandState(status.GetState()),
		ResultMessage: status.GetResultMessage(),
	}, time.Now().UTC())
	if err != nil {
		s.hub.logger.Warn("agent command status rejected", "agent_id", agentID, "command_id", status.GetCommandId(), "state", status.GetState(), "error", err)
		return
	}

	s.hub.logger.Info("agent command status accepted", "agent_id", agentID, "command_id", status.GetCommandId(), "state", status.GetState())
}

func (s *Server) recordMissionExecutionStatus(agentID string, status *pb.MissionExecutionStatus) {
	_, err := s.hub.registry.UpdateMissionExecutionStatus(registry.UpdateMissionExecutionStatusInput{
		AgentID:            agentID,
		ExecutionID:        status.GetExecutionId(),
		State:              domain.MissionExecutionState(status.GetState()),
		ResultMessage:      status.GetResultMessage(),
		CurrentMissionItem: int(status.GetCurrentMissionItem()),
		TotalMissionItems:  int(status.GetTotalMissionItems()),
	}, time.Now().UTC())
	if err != nil {
		s.hub.logger.Warn("agent mission execution status rejected", "agent_id", agentID, "execution_id", status.GetExecutionId(), "state", status.GetState(), "error", err)
		return
	}

	s.hub.logger.Info("agent mission execution status accepted", "agent_id", agentID, "execution_id", status.GetExecutionId(), "state", status.GetState(), "current_mission_item", status.GetCurrentMissionItem(), "total_mission_items", status.GetTotalMissionItems())
}

func (s *Server) recordHeartbeat(agentID string, heartbeat *pb.Heartbeat) {
	agent, err := s.hub.registry.RecordHeartbeat(registry.HeartbeatInput{
		AgentID:      agentID,
		AgentVersion: heartbeat.GetAgentVersion(),
	}, time.Now().UTC())
	if err != nil {
		s.hub.logger.Warn("agent gRPC heartbeat rejected", "agent_id", agentID, "error", err)
		return
	}

	s.hub.logger.Info("agent gRPC heartbeat accepted", "agent_id", agent.ID, "drone_id", agent.DroneID)
}

func (s *Server) recordTelemetry(agentID string, telemetry *pb.Telemetry) {
	snapshot, err := telemetryToSnapshot(agentID, telemetry)
	if err != nil {
		s.hub.logger.Warn("agent gRPC telemetry rejected", "agent_id", agentID, "error", err)
		return
	}

	recorded, err := s.hub.registry.RecordTelemetry(snapshot, time.Now().UTC())
	if err != nil {
		s.hub.logger.Warn("agent gRPC telemetry rejected", "agent_id", agentID, "error", err)
		return
	}

	s.hub.logger.Info("agent gRPC telemetry accepted", "agent_id", agentID, "drone_id", recorded.DroneID, "source", recorded.Source)
}

func telemetryToSnapshot(agentID string, telemetry *pb.Telemetry) (domain.TelemetrySnapshot, error) {
	observedAt, err := time.Parse(time.RFC3339Nano, telemetry.GetObservedAt())
	if err != nil {
		return domain.TelemetrySnapshot{}, errors.New("observed_at must be an RFC3339 timestamp")
	}

	if telemetry.GetBatteryPercent() < 0 || telemetry.GetBatteryPercent() > 100 {
		return domain.TelemetrySnapshot{}, errors.New("battery_percent must be between 0 and 100")
	}

	if telemetry.GetLatitude() < -90 || telemetry.GetLatitude() > 90 {
		return domain.TelemetrySnapshot{}, errors.New("latitude must be between -90 and 90")
	}

	if telemetry.GetLongitude() < -180 || telemetry.GetLongitude() > 180 {
		return domain.TelemetrySnapshot{}, errors.New("longitude must be between -180 and 180")
	}

	if strings.TrimSpace(telemetry.GetFlightMode()) == "" {
		return domain.TelemetrySnapshot{}, errors.New("flight_mode is required")
	}

	if strings.TrimSpace(telemetry.GetGpsFix()) == "" {
		return domain.TelemetrySnapshot{}, errors.New("gps_fix is required")
	}

	if strings.TrimSpace(telemetry.GetSource()) == "" {
		return domain.TelemetrySnapshot{}, errors.New("source is required")
	}

	return domain.TelemetrySnapshot{
		AgentID:           agentID,
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

func (h *Hub) register(conn *connection) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if existing := h.connections[conn.agentID]; existing != nil {
		existing.close()
	}

	h.connections[conn.agentID] = conn
}

func (h *Hub) connectionForAgent(agentID string) *connection {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.connections[agentID]
}

func (h *Hub) enqueueCommand(ctx context.Context, command domain.OperatorCommand) bool {
	conn := h.connectionForAgent(command.AgentID)
	if conn == nil {
		return false
	}

	msg := &pb.BackendToAgent{
		Payload: &pb.BackendToAgent_Command{
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

func (h *Hub) enqueueMissionExecution(ctx context.Context, execution domain.MissionExecution) bool {
	conn := h.connectionForAgent(execution.AgentID)
	if conn == nil {
		return false
	}

	mission, ok := h.registry.MissionByID(execution.MissionID)
	if !ok {
		h.logger.Warn("mission execution references missing mission", "execution_id", execution.ID, "mission_id", execution.MissionID)
		return false
	}

	msg := &pb.BackendToAgent{
		Payload: &pb.BackendToAgent_MissionExecution{
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

func missionExecutionAction(state domain.MissionExecutionState) string {
	switch state {
	case domain.MissionExecutionStateUploadRequested:
		return "upload"
	case domain.MissionExecutionStateStartRequested:
		return "start"
	case domain.MissionExecutionStateRTLRequested:
		return "return_to_launch"
	default:
		return string(state)
	}
}

func missionWaypointsToProto(waypoints []domain.MissionWaypoint) []*pb.MissionWaypoint {
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

func (h *Hub) unregister(agentID string, conn *connection) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.connections[agentID] == conn {
		delete(h.connections, agentID)
		if _, err := h.registry.RecordCommandChannelDisconnected(agentID, time.Now().UTC()); err != nil {
			h.logger.Warn("failed to record disconnected agent gRPC channel", "agent_id", agentID, "error", err)
		}
	}

	conn.close()
	h.logger.Info("agent gRPC channel disconnected", "agent_id", agentID)
}

func newConnection(agentID string) *connection {
	return &connection{
		agentID: agentID,
		send:    make(chan *pb.BackendToAgent, 16),
		done:    make(chan struct{}),
	}
}

func (c *connection) enqueue(ctx context.Context, msg *pb.BackendToAgent) bool {
	select {
	case <-ctx.Done():
		return false
	case <-c.done:
		return false
	case c.send <- msg:
		return true
	}
}

func (c *connection) close() {
	c.once.Do(func() {
		close(c.done)
	})
}
