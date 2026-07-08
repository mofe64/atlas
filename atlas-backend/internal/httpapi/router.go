package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/sunnyside/atlas/atlas-backend/internal/httpapi/dtos"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
	svc "github.com/sunnyside/atlas/atlas-backend/internal/services"
)

const serviceName = "atlas-backend"

const droneStreamInterval = time.Second

var droneStreamUpgrader = websocket.Upgrader{
	CheckOrigin: allowDroneStreamOrigin,
}

type CommandDispatcher interface {
	DispatchCommand(ctx context.Context, command models.CommandRequest) (models.CommandRequest, bool)
}

type MissionExecutionDispatcher interface {
	DispatchMissionExecution(ctx context.Context, execution models.MissionExecution) (models.MissionExecution, bool)
}

type Dependencies struct {
	VehicleAgents *svc.VehicleAgentService
	Telemetry     *svc.TelemetryService
	Commands      *svc.CommandService
	Missions      *svc.MissionService
	Fleet         *svc.FleetService
}

func NewRouter(deps Dependencies) http.Handler {
	return NewRouterWithCommandDispatcher(deps, nil)
}

func NewRouterWithCommandDispatcher(deps Dependencies, dispatcher CommandDispatcher) http.Handler {
	return NewRouterWithDispatchers(deps, dispatcher, nil)
}

func NewRouterWithDispatchers(deps Dependencies, commandDispatcher CommandDispatcher, missionDispatcher MissionExecutionDispatcher) http.Handler {
	router := chi.NewRouter()

	router.Get("/healthz", healthz)
	router.Get("/version", version)
	router.Route("/api", func(router chi.Router) {
		router.Post("/vehicle-agents/register", registerVehicleAgent(deps.VehicleAgents))
		router.Post("/vehicle-agents/{vehicleAgentID}/heartbeat", heartbeat(deps.VehicleAgents))
		router.Post("/vehicle-agents/{vehicleAgentID}/telemetry", recordTelemetry(deps.Telemetry))
		router.Post("/vehicle-agents/{vehicleAgentID}/commands/{commandID}/status", updateCommandStatus(deps.Commands))
		router.Get("/drones", listDrones(deps.Fleet))
		router.Get("/drones/stream", streamDrones(deps.Fleet))
		router.Get("/drones/{droneID}/missions", listMissionsForDrone(deps.Missions))
		router.Post("/drones/{droneID}/missions", createMission(deps.Missions))
		router.Get("/missions/{missionID}", getMission(deps.Fleet))
		router.Get("/missions/{missionID}/executions", listMissionExecutions(deps.Missions))
		router.Get("/missions/{missionID}/events", listMissionExecutionEvents(deps.Missions))
		router.Get("/missions/{missionID}/stream", streamMission(deps.Missions, deps.Fleet))
		router.Post("/missions/{missionID}/upload", requestMissionUpload(deps.Missions, missionDispatcher))
		router.Post("/missions/{missionID}/start", requestMissionStart(deps.Missions, missionDispatcher))
		router.Post("/missions/{missionID}/abort", requestMissionAbort(deps.Missions, missionDispatcher))
		router.Get("/drones/{droneID}/commands", listCommandsForDrone(deps.Commands))
		router.Post("/drones/{droneID}/commands/arm", issueCommand(deps.Commands, commandDispatcher, models.CommandTypeArm))
		router.Post("/drones/{droneID}/commands/takeoff", issueCommand(deps.Commands, commandDispatcher, models.CommandTypeTakeoff))
		router.Post("/drones/{droneID}/commands/return-to-launch", issueCommand(deps.Commands, commandDispatcher, models.CommandTypeReturnToLaunch))
		router.Post("/drones/{droneID}/commands/land", issueCommand(deps.Commands, commandDispatcher, models.CommandTypeLand))
	})

	return router
}

func healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, dtos.HealthResponse{
		Service: serviceName,
		Status:  "ok",
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

func version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, dtos.VersionResponse{
		Service: serviceName,
		Version: "0.1.0-dev",
	})
}

func registerVehicleAgent(repo *svc.VehicleAgentService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req dtos.RegisterVehicleAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
			return
		}

		if err := validateRegisterVehicleAgentRequest(req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		agent, err := repo.RegisterVehicleAgent(r.Context(), repository.RegisterVehicleAgentInput{
			VehicleAgentID:      req.VehicleAgentID,
			DroneID:             req.DroneID,
			DroneName:           req.DroneName,
			VehicleAgentVersion: req.VehicleAgentVersion,
		}, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to register vehicle agent")
			return
		}

		writeJSON(w, http.StatusOK, dtos.RegisterVehicleAgentResponse{
			VehicleAgentID:           agent.ID,
			DroneID:                  agent.DroneID,
			Status:                   "registered",
			HeartbeatIntervalSeconds: int(models.HeartbeatInterval.Seconds()),
		})
	}
}

func heartbeat(repo *svc.VehicleAgentService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "vehicleAgentID")

		var req dtos.HeartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
			return
		}

		if strings.TrimSpace(agentID) == "" {
			writeError(w, http.StatusBadRequest, "vehicleAgentId is required")
			return
		}

		if strings.TrimSpace(req.VehicleAgentVersion) == "" {
			writeError(w, http.StatusBadRequest, "vehicleAgentVersion is required")
			return
		}

		now := time.Now().UTC()
		agent, err := repo.RecordVehicleAgentHeartbeat(r.Context(), repository.VehicleAgentHeartbeatInput{
			VehicleAgentID:      agentID,
			VehicleAgentVersion: req.VehicleAgentVersion,
		}, now)
		if err != nil {
			if errors.Is(err, repository.ErrVehicleAgentNotFound) {
				writeError(w, http.StatusNotFound, "vehicle agent is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to record heartbeat")
			return
		}

		writeJSON(w, http.StatusOK, dtos.HeartbeatResponse{
			VehicleAgentID:     agent.ID,
			DroneID:            agent.DroneID,
			Status:             "online",
			LastHeartbeatAt:    rfc3339UTC(agent.LastHeartbeatAt),
			NextHeartbeatAfter: int(models.HeartbeatInterval.Seconds()),
		})
	}
}

func recordTelemetry(repo *svc.TelemetryService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "vehicleAgentID")
		if strings.TrimSpace(agentID) == "" {
			writeError(w, http.StatusBadRequest, "vehicleAgentId is required")
			return
		}

		var req dtos.TelemetryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
			return
		}

		snapshot, err := telemetryRequestToSnapshot(agentID, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		now := time.Now().UTC()
		snapshot, err = repo.RecordTelemetry(r.Context(), snapshot, now)
		if err != nil {
			if errors.Is(err, repository.ErrVehicleAgentNotFound) {
				writeError(w, http.StatusNotFound, "vehicle agent is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to record telemetry")
			return
		}

		writeJSON(w, http.StatusOK, dtos.TelemetryResponse{
			DroneID:        snapshot.DroneID,
			VehicleAgentID: snapshot.VehicleAgentID,
			TelemetryState: string(models.TelemetryStateFresh),
			ReceivedAt:     rfc3339UTC(snapshot.ReceivedAt),
		})
	}
}

func listDrones(fleet *svc.FleetService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, droneResponses(fleet.ListDrones(r.Context(), time.Now().UTC(), 8)))
	}
}

func streamDrones(fleet *svc.FleetService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := droneStreamUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		if err := writeDroneStreamSnapshot(r.Context(), conn, fleet); err != nil {
			return
		}

		ticker := time.NewTicker(droneStreamInterval)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if err := writeDroneStreamSnapshot(r.Context(), conn, fleet); err != nil {
					return
				}
			}
		}
	}
}

func createMission(repo *svc.MissionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		droneID := chi.URLParam(r, "droneID")
		if strings.TrimSpace(droneID) == "" {
			writeError(w, http.StatusBadRequest, "droneId is required")
			return
		}

		var req dtos.CreateMissionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
			return
		}

		mission, err := repo.CreateMission(r.Context(), repository.CreateMissionInput{
			DroneID:          droneID,
			Name:             req.Name,
			CreatedBy:        requestedBy(r),
			Waypoints:        missionWaypointInputs(req.Waypoints),
			CompletionAction: models.MissionCompletionAction(req.CompletionAction),
		}, time.Now().UTC())
		if err != nil {
			if errors.Is(err, repository.ErrDroneNotFound) {
				writeError(w, http.StatusNotFound, "drone is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to create mission")
			return
		}

		status := http.StatusCreated
		if mission.ValidationStatus == models.MissionValidationStatusRejected {
			status = http.StatusConflict
		}

		writeJSON(w, status, missionToResponse(mission))
	}
}

func listMissionsForDrone(repo *svc.MissionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		droneID := chi.URLParam(r, "droneID")
		if strings.TrimSpace(droneID) == "" {
			writeError(w, http.StatusBadRequest, "droneId is required")
			return
		}

		missions, err := repo.ListMissionsForDrone(r.Context(), droneID)
		if err != nil {
			if errors.Is(err, repository.ErrDroneNotFound) {
				writeError(w, http.StatusNotFound, "drone is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to list missions")
			return
		}

		res := make([]dtos.MissionResponse, 0, len(missions))
		for _, mission := range missions {
			res = append(res, missionToResponse(mission))
		}

		writeJSON(w, http.StatusOK, res)
	}
}

func getMission(fleet *svc.FleetService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		detail, err := missionDetail(r.Context(), fleet, missionID)
		if err != nil {
			if errors.Is(err, repository.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to fetch mission")
			return
		}

		writeJSON(w, http.StatusOK, detail)
	}
}

func listMissionExecutions(repo *svc.MissionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		executions, err := repo.ListMissionExecutions(r.Context(), missionID)
		if err != nil {
			if errors.Is(err, repository.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to list mission executions")
			return
		}

		writeJSON(w, http.StatusOK, missionExecutionResponses(executions))
	}
}

func listMissionExecutionEvents(repo *svc.MissionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		events, err := repo.ListMissionExecutionEvents(r.Context(), missionID)
		if err != nil {
			if errors.Is(err, repository.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to list mission execution events")
			return
		}

		writeJSON(w, http.StatusOK, missionExecutionEventResponses(events))
	}
}

func streamMission(missions *svc.MissionService, fleet *svc.FleetService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		if _, ok := missions.GetMissionByID(r.Context(), missionID); !ok {
			writeError(w, http.StatusNotFound, "mission was not found")
			return
		}

		conn, err := droneStreamUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		detail, err := missionDetail(r.Context(), fleet, missionID)
		if err != nil {
			return
		}
		lastSignature := missionDetailSignature(detail)

		if err := writeMissionStreamEvent(conn, "mission_snapshot", detail); err != nil {
			return
		}

		ticker := time.NewTicker(droneStreamInterval)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				detail, err := missionDetail(r.Context(), fleet, missionID)
				if err != nil {
					return
				}
				signature := missionDetailSignature(detail)
				if signature == lastSignature {
					continue
				}
				lastSignature = signature

				if err := writeMissionStreamEvent(conn, "mission_updated", detail); err != nil {
					return
				}
			}
		}
	}
}

func requestMissionUpload(repo *svc.MissionService, dispatcher MissionExecutionDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		execution, err := repo.RequestMissionUpload(r.Context(), repository.RequestMissionUploadInput{
			MissionID:   missionID,
			RequestedBy: requestedBy(r),
		}, time.Now().UTC())
		if err != nil {
			if errors.Is(err, repository.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			if errors.Is(err, repository.ErrMissionNotValidated) {
				writeError(w, http.StatusConflict, "mission must be validated before upload")
				return
			}

			if errors.Is(err, repository.ErrVehicleAgentNotFound) {
				writeError(w, http.StatusConflict, "drone has no registered vehicle agent")
				return
			}

			if errors.Is(err, repository.ErrDroneMissionActive) {
				writeError(w, http.StatusConflict, "drone already has an active mission execution")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to request mission upload")
			return
		}

		// Dispatch happens after the service transaction commits; network delivery
		// must never keep a database transaction open.
		if dispatcher != nil {
			if dispatched, ok := dispatcher.DispatchMissionExecution(r.Context(), execution); ok {
				execution = dispatched
			}
		}

		writeJSON(w, http.StatusAccepted, missionExecutionToResponse(execution))
	}
}

func requestMissionStart(repo *svc.MissionService, dispatcher MissionExecutionDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		execution, err := repo.RequestMissionStart(r.Context(), repository.RequestMissionStartInput{
			MissionID:   missionID,
			RequestedBy: requestedBy(r),
		}, time.Now().UTC())
		if err != nil {
			var preconditionErr repository.MissionStartPreconditionError
			if errors.As(err, &preconditionErr) {
				writeError(w, http.StatusConflict, preconditionErr.Error())
				return
			}

			if errors.Is(err, repository.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			if errors.Is(err, repository.ErrInvalidMissionExecutionState) {
				writeError(w, http.StatusConflict, "mission must be uploaded to vehicle before start")
				return
			}

			if errors.Is(err, repository.ErrDroneMissionActive) {
				writeError(w, http.StatusConflict, "drone already has an active mission execution")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to request mission start")
			return
		}

		// Dispatch happens after the service transaction commits; network delivery
		// must never keep a database transaction open.
		if dispatcher != nil {
			if dispatched, ok := dispatcher.DispatchMissionExecution(r.Context(), execution); ok {
				execution = dispatched
			}
		}

		writeJSON(w, http.StatusAccepted, missionExecutionToResponse(execution))
	}
}

func requestMissionAbort(repo *svc.MissionService, dispatcher MissionExecutionDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		execution, err := repo.RequestMissionAbort(r.Context(), repository.RequestMissionAbortInput{
			MissionID:   missionID,
			RequestedBy: requestedBy(r),
		}, time.Now().UTC())
		if err != nil {
			if errors.Is(err, repository.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			if errors.Is(err, repository.ErrInvalidMissionExecutionState) {
				writeError(w, http.StatusConflict, "mission must be active before aborting to RTL")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to request mission abort")
			return
		}

		// Dispatch happens after the service transaction commits; network delivery
		// must never keep a database transaction open.
		if dispatcher != nil {
			if dispatched, ok := dispatcher.DispatchMissionExecution(r.Context(), execution); ok {
				execution = dispatched
			}
		}

		writeJSON(w, http.StatusAccepted, missionExecutionToResponse(execution))
	}
}

func listCommandsForDrone(repo *svc.CommandService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		droneID := chi.URLParam(r, "droneID")
		if strings.TrimSpace(droneID) == "" {
			writeError(w, http.StatusBadRequest, "droneId is required")
			return
		}

		limit := 10
		if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
			parsed, err := strconv.Atoi(rawLimit)
			if err != nil || parsed < 1 || parsed > 100 {
				writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
				return
			}
			limit = parsed
		}

		commands, err := repo.ListCommandsForDrone(r.Context(), droneID, limit)
		if err != nil {
			if errors.Is(err, repository.ErrDroneNotFound) {
				writeError(w, http.StatusNotFound, "drone is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to list commands")
			return
		}

		res := make([]dtos.CommandResponse, 0, len(commands))
		for _, command := range commands {
			res = append(res, commandToResponse(command))
		}

		writeJSON(w, http.StatusOK, res)
	}
}

func issueCommand(repo *svc.CommandService, dispatcher CommandDispatcher, commandType models.CommandType) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		droneID := chi.URLParam(r, "droneID")
		if strings.TrimSpace(droneID) == "" {
			writeError(w, http.StatusBadRequest, "droneId is required")
			return
		}

		command, err := repo.IssueCommand(r.Context(), repository.RequestCommandInput{
			DroneID:     droneID,
			Type:        commandType,
			RequestedBy: requestedBy(r),
		}, time.Now().UTC())
		if err != nil {
			if errors.Is(err, repository.ErrDroneNotFound) {
				writeError(w, http.StatusNotFound, "drone is not registered")
				return
			}

			if errors.Is(err, repository.ErrVehicleAgentNotFound) {
				writeError(w, http.StatusConflict, "drone has no registered vehicle agent")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to issue command")
			return
		}

		status := http.StatusAccepted
		if command.State == models.CommandStateRejectedByPolicy {
			status = http.StatusConflict
		} else if dispatcher != nil {
			// Dispatch happens after the service transaction commits; network delivery
			// must never keep a database transaction open.
			if dispatched, ok := dispatcher.DispatchCommand(r.Context(), command); ok {
				command = dispatched
			}
		}

		writeJSON(w, status, commandToResponse(command))
	}
}

func updateCommandStatus(repo *svc.CommandService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "vehicleAgentID")
		if strings.TrimSpace(agentID) == "" {
			writeError(w, http.StatusBadRequest, "vehicleAgentId is required")
			return
		}

		commandID := chi.URLParam(r, "commandID")
		if strings.TrimSpace(commandID) == "" {
			writeError(w, http.StatusBadRequest, "commandId is required")
			return
		}

		var req dtos.CommandStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
			return
		}

		if strings.TrimSpace(req.State) == "" {
			writeError(w, http.StatusBadRequest, "state is required")
			return
		}

		command, err := repo.UpdateCommandStatus(r.Context(), repository.UpdateCommandStatusInput{
			VehicleAgentID: agentID,
			CommandID:      commandID,
			State:          models.CommandState(req.State),
			ResultMessage:  strings.TrimSpace(req.ResultMessage),
		}, time.Now().UTC())
		if err != nil {
			if errors.Is(err, repository.ErrCommandNotFound) {
				writeError(w, http.StatusNotFound, "command was not found")
				return
			}

			if errors.Is(err, repository.ErrCommandNotAssignedToVehicleAgent) {
				writeError(w, http.StatusForbidden, "command is not assigned to this vehicle agent")
				return
			}

			if errors.Is(err, repository.ErrInvalidCommandState) {
				writeError(w, http.StatusBadRequest, "state cannot be reported by a vehicle agent")
				return
			}

			if errors.Is(err, repository.ErrInvalidCommandTransition) {
				writeError(w, http.StatusConflict, "command cannot transition to requested state")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to update command status")
			return
		}

		writeJSON(w, http.StatusOK, commandToResponse(command))
	}
}

func writeDroneStreamSnapshot(ctx context.Context, conn *websocket.Conn, fleet *svc.FleetService) error {
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	return conn.WriteJSON(droneResponses(fleet.ListDrones(ctx, time.Now().UTC(), 8)))
}

func writeMissionStreamEvent(conn *websocket.Conn, eventType string, detail dtos.MissionDetailResponse) error {
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	return conn.WriteJSON(dtos.MissionStreamEventResponse{
		Type:   eventType,
		Detail: detail,
	})
}

func missionDetailSignature(detail dtos.MissionDetailResponse) string {
	payload, err := json.Marshal(detail)
	if err != nil {
		return ""
	}

	return string(payload)
}

func droneResponses(drones []svc.FleetDrone) []dtos.DroneResponse {
	res := make([]dtos.DroneResponse, 0, len(drones))

	for _, drone := range drones {
		snapshot := drone.Snapshot
		item := dtos.DroneResponse{
			ID:             snapshot.ID,
			Name:           snapshot.Name,
			VehicleAgentID: snapshot.VehicleAgentID,
			Status:         string(snapshot.Status),
			LastSeenAt:     rfc3339UTC(snapshot.LastSeenAt),
			CommandChannel: commandChannelToResponse(snapshot.CommandChannel),
			Commands:       commandResponses(drone.Commands),
		}

		if !snapshot.LastHeartbeatAt.IsZero() {
			item.LastHeartbeatAt = rfc3339UTC(snapshot.LastHeartbeatAt)
		}

		if !snapshot.Telemetry.ReceivedAt.IsZero() {
			item.Telemetry = &dtos.TelemetrySnapshotResponse{
				State:             string(snapshot.TelemetryState),
				ObservedAt:        rfc3339UTC(snapshot.Telemetry.ObservedAt),
				ReceivedAt:        rfc3339UTC(snapshot.Telemetry.ReceivedAt),
				BatteryPercent:    snapshot.Telemetry.BatteryPercent,
				RelativeAltitudeM: snapshot.Telemetry.RelativeAltitudeM,
				FlightMode:        snapshot.Telemetry.FlightMode,
				Armed:             snapshot.Telemetry.Armed,
				InAir:             snapshot.Telemetry.InAir,
				Latitude:          snapshot.Telemetry.Latitude,
				Longitude:         snapshot.Telemetry.Longitude,
				HeadingDeg:        snapshot.Telemetry.HeadingDeg,
				GroundSpeedMPS:    snapshot.Telemetry.GroundSpeedMPS,
				GPSFix:            snapshot.Telemetry.GPSFix,
				SatellitesVisible: snapshot.Telemetry.SatellitesVisible,
				HomePositionSet:   snapshot.Telemetry.HomePositionSet,
				Source:            snapshot.Telemetry.Source,
			}
		}

		if snapshot.LatestMissionExecution.ID != "" {
			execution := missionExecutionToResponse(snapshot.LatestMissionExecution)
			item.MissionExecution = &execution
		}

		res = append(res, item)
	}

	return res
}

func missionDetail(ctx context.Context, fleet *svc.FleetService, missionID string) (dtos.MissionDetailResponse, error) {
	detail, err := fleet.MissionDetail(ctx, missionID)
	if err != nil {
		return dtos.MissionDetailResponse{}, err
	}

	return dtos.MissionDetailResponse{
		Mission:    missionToResponse(detail.Mission),
		Executions: missionExecutionResponses(detail.Executions),
	}, nil
}

func missionExecutionResponses(executions []models.MissionExecution) []dtos.MissionExecutionResponse {
	res := make([]dtos.MissionExecutionResponse, 0, len(executions))
	for _, execution := range executions {
		res = append(res, missionExecutionToResponse(execution))
	}

	return res
}

func missionExecutionEventResponses(events []models.MissionExecutionEvent) []dtos.MissionExecutionEventResponse {
	res := make([]dtos.MissionExecutionEventResponse, 0, len(events))
	for _, event := range events {
		res = append(res, missionExecutionEventToResponse(event))
	}

	return res
}

func commandResponses(commands []models.CommandRequest) []dtos.CommandResponse {
	res := make([]dtos.CommandResponse, 0, len(commands))
	for _, command := range commands {
		res = append(res, commandToResponse(command))
	}

	return res
}

func commandChannelToResponse(channel repository.CommandChannelSnapshot) dtos.CommandChannelResponse {
	res := dtos.CommandChannelResponse{
		State: string(channel.State),
	}

	if !channel.ConnectedAt.IsZero() {
		res.ConnectedAt = rfc3339UTC(channel.ConnectedAt)
	}

	if !channel.LastDisconnectedAt.IsZero() {
		res.LastDisconnectedAt = rfc3339UTC(channel.LastDisconnectedAt)
	}

	return res
}

func telemetryRequestToSnapshot(agentID string, req dtos.TelemetryRequest) (models.TelemetrySnapshot, error) {
	observedAt, err := time.Parse(time.RFC3339Nano, req.ObservedAt)
	if err != nil {
		return models.TelemetrySnapshot{}, errors.New("observedAt must be an RFC3339 timestamp")
	}

	if req.BatteryPercent < 0 || req.BatteryPercent > 100 {
		return models.TelemetrySnapshot{}, errors.New("batteryPercent must be between 0 and 100")
	}

	if req.Latitude < -90 || req.Latitude > 90 {
		return models.TelemetrySnapshot{}, errors.New("latitude must be between -90 and 90")
	}

	if req.Longitude < -180 || req.Longitude > 180 {
		return models.TelemetrySnapshot{}, errors.New("longitude must be between -180 and 180")
	}

	if strings.TrimSpace(req.FlightMode) == "" {
		return models.TelemetrySnapshot{}, errors.New("flightMode is required")
	}

	if strings.TrimSpace(req.GPSFix) == "" {
		return models.TelemetrySnapshot{}, errors.New("gpsFix is required")
	}

	if strings.TrimSpace(req.Source) == "" {
		return models.TelemetrySnapshot{}, errors.New("source is required")
	}

	return models.TelemetrySnapshot{
		VehicleAgentID:    agentID,
		ObservedAt:        observedAt.UTC(),
		BatteryPercent:    req.BatteryPercent,
		RelativeAltitudeM: req.RelativeAltitudeM,
		FlightMode:        req.FlightMode,
		Armed:             req.Armed,
		InAir:             req.InAir,
		Latitude:          req.Latitude,
		Longitude:         req.Longitude,
		HeadingDeg:        req.HeadingDeg,
		GroundSpeedMPS:    req.GroundSpeedMPS,
		GPSFix:            req.GPSFix,
		SatellitesVisible: req.SatellitesVisible,
		HomePositionSet:   req.HomePositionSet,
		Source:            req.Source,
	}, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func rfc3339UTC(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func missionWaypointInputs(waypoints []dtos.MissionWaypointRequest) []repository.MissionWaypointInput {
	inputs := make([]repository.MissionWaypointInput, 0, len(waypoints))
	for _, waypoint := range waypoints {
		inputs = append(inputs, repository.MissionWaypointInput{
			Latitude:          waypoint.Latitude,
			Longitude:         waypoint.Longitude,
			RelativeAltitudeM: waypoint.RelativeAltitudeM,
			SpeedMPS:          waypoint.SpeedMPS,
			LoiterTimeS:       waypoint.LoiterTimeS,
		})
	}

	return inputs
}

func missionToResponse(mission models.Mission) dtos.MissionResponse {
	res := dtos.MissionResponse{
		ID:               mission.ID,
		DroneID:          mission.DroneID,
		Name:             mission.Name,
		CreatedBy:        mission.CreatedBy,
		CreatedAt:        rfc3339UTC(mission.CreatedAt),
		UpdatedAt:        rfc3339UTC(mission.UpdatedAt),
		CompletionAction: string(mission.CompletionAction),
		ValidationStatus: string(mission.ValidationStatus),
		Waypoints:        make([]dtos.MissionWaypointResponse, 0, len(mission.Waypoints)),
		ValidationErrors: make([]dtos.MissionValidationError, 0, len(mission.ValidationErrors)),
	}

	for _, waypoint := range mission.Waypoints {
		res.Waypoints = append(res.Waypoints, dtos.MissionWaypointResponse{
			Sequence:          waypoint.Sequence,
			Latitude:          waypoint.Latitude,
			Longitude:         waypoint.Longitude,
			RelativeAltitudeM: waypoint.RelativeAltitudeM,
			SpeedMPS:          waypoint.SpeedMPS,
			LoiterTimeS:       waypoint.LoiterTimeS,
		})
	}

	for _, validationError := range mission.ValidationErrors {
		res.ValidationErrors = append(res.ValidationErrors, dtos.MissionValidationError{
			Field:   validationError.Field,
			Message: validationError.Message,
		})
	}

	return res
}

func missionExecutionToResponse(execution models.MissionExecution) dtos.MissionExecutionResponse {
	res := dtos.MissionExecutionResponse{
		ID:                 execution.ID,
		MissionID:          execution.MissionID,
		DroneID:            execution.DroneID,
		VehicleAgentID:     execution.VehicleAgentID,
		RequestedBy:        execution.RequestedBy,
		UploadRequestedBy:  execution.UploadRequestedBy,
		StartRequestedBy:   execution.StartRequestedBy,
		State:              string(execution.State),
		CreatedAt:          rfc3339UTC(execution.CreatedAt),
		UpdatedAt:          rfc3339UTC(execution.UpdatedAt),
		CurrentMissionItem: execution.CurrentMissionItem,
		TotalMissionItems:  execution.TotalMissionItems,
		DeliveryAttempt:    execution.DeliveryAttempt,
		ResultMessage:      execution.ResultMessage,
	}

	if !execution.LastSentAt.IsZero() {
		res.LastSentAt = rfc3339UTC(execution.LastSentAt)
	}

	if !execution.LeaseUntil.IsZero() {
		res.LeaseUntil = rfc3339UTC(execution.LeaseUntil)
	}

	if !execution.UploadRequestedAt.IsZero() {
		res.UploadRequestedAt = rfc3339UTC(execution.UploadRequestedAt)
	}

	if !execution.UploadedAt.IsZero() {
		res.UploadedAt = rfc3339UTC(execution.UploadedAt)
	}

	if !execution.StartRequestedAt.IsZero() {
		res.StartRequestedAt = rfc3339UTC(execution.StartRequestedAt)
	}

	if !execution.StartedAt.IsZero() {
		res.StartedAt = rfc3339UTC(execution.StartedAt)
	}

	if !execution.CompletedAt.IsZero() {
		res.CompletedAt = rfc3339UTC(execution.CompletedAt)
	}

	if !execution.HoldAt.IsZero() {
		res.HoldAt = rfc3339UTC(execution.HoldAt)
	}

	if !execution.FailedAt.IsZero() {
		res.FailedAt = rfc3339UTC(execution.FailedAt)
	}

	if !execution.ProgressUpdatedAt.IsZero() {
		res.ProgressUpdatedAt = rfc3339UTC(execution.ProgressUpdatedAt)
	}

	return res
}

func missionExecutionEventToResponse(event models.MissionExecutionEvent) dtos.MissionExecutionEventResponse {
	return dtos.MissionExecutionEventResponse{
		ID:                 event.ID,
		ExecutionID:        event.ExecutionID,
		MissionID:          event.MissionID,
		DroneID:            event.DroneID,
		VehicleAgentID:     event.VehicleAgentID,
		Type:               event.Type,
		State:              string(event.State),
		Message:            event.Message,
		CurrentMissionItem: event.CurrentMissionItem,
		TotalMissionItems:  event.TotalMissionItems,
		Source:             event.Source,
		CreatedAt:          rfc3339UTC(event.CreatedAt),
	}
}

func commandToResponse(command models.CommandRequest) dtos.CommandResponse {
	res := dtos.CommandResponse{
		ID:                 command.ID,
		DroneID:            command.DroneID,
		VehicleAgentID:     command.VehicleAgentID,
		Type:               string(command.Type),
		State:              string(command.State),
		RequestedBy:        command.RequestedBy,
		RequestedAt:        rfc3339UTC(command.RequestedAt),
		UpdatedAt:          rfc3339UTC(command.UpdatedAt),
		DeliveryAttempt:    command.DeliveryAttempt,
		PolicyReason:       command.PolicyReason,
		ResultMessage:      command.ResultMessage,
		TelemetryState:     string(command.TelemetryState),
		VehicleAgentStatus: string(command.VehicleAgentStatus),
	}

	if !command.LastSentAt.IsZero() {
		res.LastSentAt = rfc3339UTC(command.LastSentAt)
	}

	if !command.LeaseUntil.IsZero() {
		res.LeaseUntil = rfc3339UTC(command.LeaseUntil)
	}

	if !command.VehicleAckedAt.IsZero() {
		res.VehicleAckedAt = rfc3339UTC(command.VehicleAckedAt)
	}

	return res
}

func requestedBy(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("X-Atlas-Operator-ID"))
	if value == "" {
		return "development-operator"
	}

	return value
}

func allowDroneStreamOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	originURL, err := url.Parse(origin)
	if err != nil {
		return false
	}

	originHost := originURL.Hostname()
	requestHost := hostWithoutPort(r.Host)
	if strings.EqualFold(originHost, requestHost) {
		return true
	}

	return isLocalhost(originHost) && isLocalhost(requestHost)
}

func hostWithoutPort(host string) string {
	value := strings.TrimSpace(host)
	if value == "" {
		return ""
	}

	hostname, _, err := net.SplitHostPort(value)
	if err == nil {
		return strings.Trim(hostname, "[]")
	}

	return strings.Trim(value, "[]")
}

func isLocalhost(host string) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func validateRegisterVehicleAgentRequest(req dtos.RegisterVehicleAgentRequest) error {
	if strings.TrimSpace(req.VehicleAgentID) == "" {
		return errors.New("vehicleAgentId is required")
	}

	if strings.TrimSpace(req.DroneID) == "" {
		return errors.New("droneId is required")
	}

	if strings.TrimSpace(req.DroneName) == "" {
		return errors.New("droneName is required")
	}

	if strings.TrimSpace(req.VehicleAgentVersion) == "" {
		return errors.New("vehicleAgentVersion is required")
	}

	return nil
}
