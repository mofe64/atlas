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
const maxGimbalRateDegS = 90

var droneStreamUpgrader = websocket.Upgrader{
	CheckOrigin: allowDroneStreamOrigin,
}

type VehicleActionDispatcher interface {
	DispatchVehicleAction(ctx context.Context, action models.VehicleAction) (models.VehicleAction, bool)
}

type MissionExecutionDispatcher interface {
	DispatchMissionExecution(ctx context.Context, execution models.MissionExecution) (models.MissionExecution, bool)
}

type GimbalControlDispatcher interface {
	DispatchGimbalControl(ctx context.Context, command models.GimbalControlCommand) bool
}

type Dependencies struct {
	VehicleAgents  *svc.VehicleAgentService
	Telemetry      *svc.TelemetryService
	VehicleActions *svc.VehicleActionService
	Missions       *svc.MissionService
	Fleet          *svc.FleetService
}

func NewRouter(deps Dependencies) http.Handler {
	return NewRouterWithVehicleActionDispatcher(deps, nil)
}

func NewRouterWithVehicleActionDispatcher(deps Dependencies, dispatcher VehicleActionDispatcher) http.Handler {
	return NewRouterWithDispatchers(deps, dispatcher, nil)
}

func NewRouterWithDispatchers(deps Dependencies, actionDispatcher VehicleActionDispatcher, missionDispatcher MissionExecutionDispatcher) http.Handler {
	return NewRouterWithGimbalControlDispatcher(deps, actionDispatcher, missionDispatcher, nil)
}

func NewRouterWithGimbalControlDispatcher(deps Dependencies, actionDispatcher VehicleActionDispatcher, missionDispatcher MissionExecutionDispatcher, gimbalDispatcher GimbalControlDispatcher) http.Handler {
	router := chi.NewRouter()

	router.Get("/healthz", healthz)
	router.Get("/version", version)
	router.Route("/api", func(router chi.Router) {
		router.Post("/vehicle-agents/register", registerVehicleAgent(deps.VehicleAgents))
		router.Post("/vehicle-agents/{vehicleAgentID}/heartbeat", heartbeat(deps.VehicleAgents))
		router.Post("/vehicle-agents/{vehicleAgentID}/telemetry", recordTelemetry(deps.Telemetry))
		router.Post("/vehicle-agents/{vehicleAgentID}/actions/{vehicleActionID}/status", updateVehicleActionStatus(deps.VehicleActions))
		router.Get("/drones", listDrones(deps.Fleet))
		router.Get("/drones/stream", streamDrones(deps.Fleet))
		router.Get("/drones/{droneID}/communication-links", listCommunicationLinksForDrone(deps.Fleet))
		router.Get("/drones/{droneID}/telemetry-feeds", listTelemetryFeedsForDrone(deps.Fleet))
		router.Get("/drones/{droneID}/missions", listMissionsForDrone(deps.Missions))
		router.Post("/drones/{droneID}/missions", createMission(deps.Missions))
		router.Get("/missions/{missionID}", getMission(deps.Fleet))
		router.Get("/missions/{missionID}/executions", listMissionExecutions(deps.Missions))
		router.Get("/missions/{missionID}/events", listMissionExecutionEvents(deps.Missions))
		router.Get("/missions/{missionID}/stream", streamMission(deps.Missions, deps.Fleet))
		router.Post("/missions/{missionID}/upload", requestMissionUpload(deps.Missions, missionDispatcher))
		router.Post("/missions/{missionID}/start", requestMissionStart(deps.Missions, missionDispatcher))
		router.Post("/missions/{missionID}/abort", requestMissionAbort(deps.Missions, missionDispatcher))
		router.Get("/drones/{droneID}/actions", listVehicleActionsForDrone(deps.VehicleActions))
		router.Get("/drones/{droneID}/actions/{vehicleActionID}/events", listVehicleActionEvents(deps.VehicleActions))
		router.Post("/drones/{droneID}/actions/arm", requestVehicleAction(deps.VehicleActions, actionDispatcher, models.VehicleActionTypeArm))
		router.Post("/drones/{droneID}/actions/takeoff", requestVehicleAction(deps.VehicleActions, actionDispatcher, models.VehicleActionTypeTakeoff))
		router.Post("/drones/{droneID}/actions/return-to-launch", requestVehicleAction(deps.VehicleActions, actionDispatcher, models.VehicleActionTypeReturnToLaunch))
		router.Post("/drones/{droneID}/actions/land", requestVehicleAction(deps.VehicleActions, actionDispatcher, models.VehicleActionTypeLand))
		router.Post("/drones/{droneID}/gimbal/control", sendGimbalControl(gimbalDispatcher))
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
			DroneID:                   snapshot.DroneID,
			VehicleAgentID:            snapshot.VehicleAgentID,
			ActiveTelemetryFeedID:     snapshot.ActiveTelemetryFeedID,
			SourceCommunicationLinkID: snapshot.SourceCommunicationLinkID,
			TelemetryState:            string(models.TelemetryStateFresh),
			ReceivedAt:                rfc3339UTC(snapshot.ReceivedAt),
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

func listCommunicationLinksForDrone(fleet *svc.FleetService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		droneID := chi.URLParam(r, "droneID")
		if strings.TrimSpace(droneID) == "" {
			writeError(w, http.StatusBadRequest, "droneId is required")
			return
		}

		links, err := fleet.ListCommunicationLinksForDrone(r.Context(), droneID)
		if err != nil {
			if errors.Is(err, repository.ErrDroneNotFound) {
				writeError(w, http.StatusNotFound, "drone is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to list communication links")
			return
		}

		writeJSON(w, http.StatusOK, communicationLinkResponses(links))
	}
}

func listTelemetryFeedsForDrone(fleet *svc.FleetService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		droneID := chi.URLParam(r, "droneID")
		if strings.TrimSpace(droneID) == "" {
			writeError(w, http.StatusBadRequest, "droneId is required")
			return
		}

		feeds, err := fleet.ListTelemetryFeedsForDrone(r.Context(), droneID)
		if err != nil {
			if errors.Is(err, repository.ErrDroneNotFound) {
				writeError(w, http.StatusNotFound, "drone is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to list telemetry feeds")
			return
		}

		writeJSON(w, http.StatusOK, telemetryFeedResponses(feeds))
	}
}

func sendGimbalControl(dispatcher GimbalControlDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dispatcher == nil {
			writeError(w, http.StatusServiceUnavailable, "gimbal control dispatcher is unavailable")
			return
		}

		droneID := strings.TrimSpace(chi.URLParam(r, "droneID"))
		if droneID == "" {
			writeError(w, http.StatusBadRequest, "drone id is required")
			return
		}

		var req dtos.GimbalControlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
			return
		}
		if err := validateGimbalControlRequest(req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		accepted := dispatcher.DispatchGimbalControl(r.Context(), models.GimbalControlCommand{
			DroneID:           droneID,
			PitchRateDegS:     req.PitchRateDegS,
			YawRateDegS:       req.YawRateDegS,
			TargetSystemID:    req.TargetSystemID,
			TargetComponentID: req.TargetComponentID,
			GimbalDeviceID:    req.GimbalDeviceID,
			RequestedBy:       requestedBy(r),
		})
		if !accepted {
			writeError(w, http.StatusConflict, "no connected agent is available for gimbal control")
			return
		}

		writeJSON(w, http.StatusAccepted, dtos.GimbalControlResponse{
			Accepted: true,
			State:    "sent",
		})
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

			if errors.Is(err, repository.ErrMissionVersionNotFound) {
				writeError(w, http.StatusConflict, "mission has no current version")
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

func listVehicleActionsForDrone(repo *svc.VehicleActionService) http.HandlerFunc {
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

		actions, err := repo.ListVehicleActionsForDrone(r.Context(), droneID, limit)
		if err != nil {
			if errors.Is(err, repository.ErrDroneNotFound) {
				writeError(w, http.StatusNotFound, "drone is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to list vehicle actions")
			return
		}

		res := make([]dtos.VehicleActionResponse, 0, len(actions))
		for _, action := range actions {
			res = append(res, vehicleActionToResponse(action))
		}

		writeJSON(w, http.StatusOK, res)
	}
}

func listVehicleActionEvents(repo *svc.VehicleActionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		droneID := chi.URLParam(r, "droneID")
		if strings.TrimSpace(droneID) == "" {
			writeError(w, http.StatusBadRequest, "droneId is required")
			return
		}

		vehicleActionID := chi.URLParam(r, "vehicleActionID")
		if strings.TrimSpace(vehicleActionID) == "" {
			writeError(w, http.StatusBadRequest, "vehicleActionId is required")
			return
		}

		action, ok := repo.GetVehicleActionByID(r.Context(), vehicleActionID)
		if !ok || action.DroneID != droneID {
			writeError(w, http.StatusNotFound, "vehicle action was not found for this drone")
			return
		}

		events, err := repo.ListVehicleActionEvents(r.Context(), vehicleActionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list vehicle action events")
			return
		}

		writeJSON(w, http.StatusOK, vehicleActionEventResponses(events))
	}
}

func requestVehicleAction(repo *svc.VehicleActionService, dispatcher VehicleActionDispatcher, actionType models.VehicleActionType) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		droneID := chi.URLParam(r, "droneID")
		if strings.TrimSpace(droneID) == "" {
			writeError(w, http.StatusBadRequest, "droneId is required")
			return
		}

		action, err := repo.RequestVehicleAction(r.Context(), repository.RequestVehicleActionInput{
			DroneID:        droneID,
			Type:           actionType,
			RequestedBy:    requestedBy(r),
			IdempotencyKey: idempotencyKey(r),
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

			writeError(w, http.StatusInternalServerError, "failed to request vehicle action")
			return
		}

		status := http.StatusAccepted
		if action.State == models.VehicleActionStateRejectedByPolicy {
			status = http.StatusConflict
		} else if action.State == models.VehicleActionStateAuthorized && dispatcher != nil {
			// Dispatch happens after the service transaction commits; network delivery
			// must never keep a database transaction open.
			if dispatched, ok := dispatcher.DispatchVehicleAction(r.Context(), action); ok {
				action = dispatched
			}
		}

		writeJSON(w, status, vehicleActionToResponse(action))
	}
}

func updateVehicleActionStatus(repo *svc.VehicleActionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "vehicleAgentID")
		if strings.TrimSpace(agentID) == "" {
			writeError(w, http.StatusBadRequest, "vehicleAgentId is required")
			return
		}

		vehicleActionID := chi.URLParam(r, "vehicleActionID")
		if strings.TrimSpace(vehicleActionID) == "" {
			writeError(w, http.StatusBadRequest, "vehicleActionId is required")
			return
		}

		var req dtos.VehicleActionStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
			return
		}

		if strings.TrimSpace(req.State) == "" {
			writeError(w, http.StatusBadRequest, "state is required")
			return
		}

		action, err := repo.UpdateVehicleActionStatus(r.Context(), repository.UpdateVehicleActionStatusInput{
			VehicleAgentID:   agentID,
			VehicleActionID:  vehicleActionID,
			State:            models.VehicleActionState(req.State),
			ResultMessage:    strings.TrimSpace(req.ResultMessage),
			AckCorrelationID: strings.TrimSpace(req.AckCorrelationID),
			RawAckCode:       strings.TrimSpace(req.RawAckCode),
			Evidence:         req.Evidence,
		}, time.Now().UTC())
		if err != nil {
			if errors.Is(err, repository.ErrVehicleActionNotFound) {
				writeError(w, http.StatusNotFound, "vehicle action was not found")
				return
			}

			if errors.Is(err, repository.ErrVehicleActionNotAssignedToVehicleAgent) {
				writeError(w, http.StatusForbidden, "vehicle action is not assigned to this vehicle agent")
				return
			}

			if errors.Is(err, repository.ErrInvalidVehicleActionState) {
				writeError(w, http.StatusBadRequest, "state cannot be reported by a vehicle agent")
				return
			}

			if errors.Is(err, repository.ErrInvalidVehicleActionTransition) {
				writeError(w, http.StatusConflict, "vehicle action cannot transition to requested state")
				return
			}

			if errors.Is(err, repository.ErrVehicleActionAckCorrelationMismatch) {
				writeError(w, http.StatusConflict, "vehicle action ack correlation id does not match")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to update vehicle action status")
			return
		}

		writeJSON(w, http.StatusOK, vehicleActionToResponse(action))
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
			ID:              snapshot.ID,
			Name:            snapshot.Name,
			VehicleAgentID:  snapshot.VehicleAgentID,
			Status:          string(snapshot.Status),
			LastSeenAt:      rfc3339UTC(snapshot.LastSeenAt),
			MAVLinkObserver: snapshot.MAVLinkObserver,
			BackendChannel:  snapshot.BackendChannelHealth,
			CommandChannel:  commandChannelToResponse(snapshot.CommandChannel),
			Communication:   communicationSummaryFromLinks(drone.CommunicationLinks),
			VehicleActions:  vehicleActionResponses(drone.VehicleActions),
		}

		if !snapshot.LastHeartbeatAt.IsZero() {
			item.LastHeartbeatAt = rfc3339UTC(snapshot.LastHeartbeatAt)
		}

		if !snapshot.Telemetry.ReceivedAt.IsZero() {
			item.Telemetry = &dtos.TelemetrySnapshotResponse{
				State:                     string(snapshot.TelemetryState),
				ActiveTelemetryFeedID:     snapshot.Telemetry.ActiveTelemetryFeedID,
				SourceCommunicationLinkID: snapshot.Telemetry.SourceCommunicationLinkID,
				ObservedAt:                rfc3339UTC(snapshot.Telemetry.ObservedAt),
				ReceivedAt:                rfc3339UTC(snapshot.Telemetry.ReceivedAt),
				BatteryPercent:            snapshot.Telemetry.BatteryPercent,
				RelativeAltitudeM:         snapshot.Telemetry.RelativeAltitudeM,
				FlightMode:                snapshot.Telemetry.FlightMode,
				Armed:                     snapshot.Telemetry.Armed,
				InAir:                     snapshot.Telemetry.InAir,
				Latitude:                  snapshot.Telemetry.Latitude,
				Longitude:                 snapshot.Telemetry.Longitude,
				HeadingDeg:                snapshot.Telemetry.HeadingDeg,
				GroundSpeedMPS:            snapshot.Telemetry.GroundSpeedMPS,
				GPSFix:                    snapshot.Telemetry.GPSFix,
				SatellitesVisible:         snapshot.Telemetry.SatellitesVisible,
				HomePositionSet:           snapshot.Telemetry.HomePositionSet,
				Source:                    snapshot.Telemetry.Source,
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

func vehicleActionResponses(actions []models.VehicleAction) []dtos.VehicleActionResponse {
	res := make([]dtos.VehicleActionResponse, 0, len(actions))
	for _, action := range actions {
		res = append(res, vehicleActionToResponse(action))
	}

	return res
}

func vehicleActionEventResponses(events []models.VehicleActionEvent) []dtos.VehicleActionEventResponse {
	res := make([]dtos.VehicleActionEventResponse, 0, len(events))
	for _, event := range events {
		res = append(res, vehicleActionEventToResponse(event))
	}

	return res
}

func telemetryFeedResponses(feeds []models.TelemetryFeed) []dtos.TelemetryFeedResponse {
	res := make([]dtos.TelemetryFeedResponse, 0, len(feeds))
	for _, feed := range feeds {
		res = append(res, telemetryFeedToResponse(feed))
	}
	return res
}

func telemetryFeedToResponse(feed models.TelemetryFeed) dtos.TelemetryFeedResponse {
	res := dtos.TelemetryFeedResponse{
		ID:                  feed.ID,
		DroneID:             feed.DroneID,
		SourceType:          string(feed.SourceType),
		SourceID:            feed.SourceID,
		CommunicationLinkID: feed.CommunicationLinkID,
		Status:              string(feed.Status),
		Priority:            feed.Priority,
		Freshness:           string(feed.Freshness),
		LastSequence:        feed.LastSequence,
		MessageRateHz:       feed.MessageRateHz,
		FieldsAvailable:     telemetryFieldsAvailableToResponse(feed.FieldsAvailable),
		StartedAt:           rfc3339UTC(feed.StartedAt),
		LastError:           feed.LastError,
	}
	if !feed.LastTelemetryAt.IsZero() {
		res.LastTelemetryAt = rfc3339UTC(feed.LastTelemetryAt)
	}
	if !feed.EndedAt.IsZero() {
		res.EndedAt = rfc3339UTC(feed.EndedAt)
	}
	return res
}

func telemetryFieldsAvailableToResponse(fields models.TelemetryFieldsAvailable) dtos.TelemetryFieldsAvailableResponse {
	return dtos.TelemetryFieldsAvailableResponse{
		Position:        fields.Position,
		Altitude:        fields.Altitude,
		Heading:         fields.Heading,
		Attitude:        fields.Attitude,
		Velocity:        fields.Velocity,
		Battery:         fields.Battery,
		Armed:           fields.Armed,
		FlightMode:      fields.FlightMode,
		GPSHealth:       fields.GPSHealth,
		HomePosition:    fields.HomePosition,
		MissionProgress: fields.MissionProgress,
		SystemHealth:    fields.SystemHealth,
	}
}

func communicationLinkResponses(links []models.CommunicationLink) []dtos.CommunicationLinkResponse {
	res := make([]dtos.CommunicationLinkResponse, 0, len(links))
	for _, link := range links {
		res = append(res, communicationLinkToResponse(link))
	}
	return res
}

func communicationLinkToResponse(link models.CommunicationLink) dtos.CommunicationLinkResponse {
	res := dtos.CommunicationLinkResponse{
		ID:                            link.ID,
		DroneID:                       link.DroneID,
		VehicleAgentID:                link.VehicleAgentID,
		DroneVehicleAgentConnectionID: link.DroneVehicleAgentConnectionID,
		LinkType:                      string(link.LinkType),
		Roles:                         communicationRoleResponses(link.Roles),
		Status:                        string(link.Status),
		Transport:                     link.Transport,
		EndpointDescription:           link.EndpointDescription,
		CommandEligible:               link.CommandEligible,
		LatencyMs:                     link.LatencyMs,
		PacketLossEstimate:            link.PacketLossEstimate,
		RxBytesPerSec:                 link.RxBytesPerSec,
		TxBytesPerSec:                 link.TxBytesPerSec,
		CreatedAt:                     rfc3339UTC(link.CreatedAt),
		EndedReason:                   link.EndedReason,
	}
	if !link.LastSeenAt.IsZero() {
		res.LastSeenAt = rfc3339UTC(link.LastSeenAt)
	}
	if !link.EndedAt.IsZero() {
		res.EndedAt = rfc3339UTC(link.EndedAt)
	}
	return res
}

func communicationSummaryFromLinks(links []models.CommunicationLink) dtos.CommunicationSummaryResponse {
	res := dtos.CommunicationSummaryResponse{
		CommandLinkStatus: string(models.CommunicationLinkStatusUnknown),
	}

	for _, link := range links {
		if communicationLinkActive(link) {
			res.ActiveLinkCount++
		}
		switch link.Status {
		case models.CommunicationLinkStatusDegraded:
			res.DegradedLinkCount++
		case models.CommunicationLinkStatusLost:
			res.LostLinkCount++
		}

		if res.ActiveTelemetryLinkID == "" &&
			communicationLinkActive(link) &&
			communicationLinkHasRole(link, models.CommunicationLinkRoleTelemetry) {
			res.ActiveTelemetryLinkID = link.ID
		}

		if res.ActiveCommandLinkID == "" && communicationLinkCanCarryCommand(link) {
			res.ActiveCommandLinkID = link.ID
			res.CommandLinkStatus = string(link.Status)
		}
	}

	if res.ActiveCommandLinkID == "" {
		res.CommandLinkStatus = communicationFallbackCommandStatus(links)
	}

	return res
}

func communicationFallbackCommandStatus(links []models.CommunicationLink) string {
	for _, link := range links {
		if link.CommandEligible && communicationLinkHasRole(link, models.CommunicationLinkRoleCommand) {
			return string(link.Status)
		}
	}
	return string(models.CommunicationLinkStatusUnknown)
}

func communicationRoleResponses(roles []models.CommunicationLinkRole) []string {
	res := make([]string, 0, len(roles))
	for _, role := range roles {
		res = append(res, string(role))
	}
	return res
}

func communicationLinkCanCarryCommand(link models.CommunicationLink) bool {
	return communicationLinkActive(link) &&
		link.CommandEligible &&
		link.Status == models.CommunicationLinkStatusConnected &&
		communicationLinkHasRole(link, models.CommunicationLinkRoleCommand)
}

func communicationLinkActive(link models.CommunicationLink) bool {
	return link.EndedAt.IsZero() &&
		link.Status != models.CommunicationLinkStatusLost &&
		link.Status != models.CommunicationLinkStatusDisabled
}

func communicationLinkHasRole(link models.CommunicationLink, role models.CommunicationLinkRole) bool {
	for _, candidate := range link.Roles {
		if candidate == role {
			return true
		}
	}
	return false
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
		CurrentVersionID: mission.CurrentVersionID,
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
		MissionVersionID:   execution.MissionVersionID,
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
		MissionVersionID:   event.MissionVersionID,
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

func vehicleActionEventToResponse(event models.VehicleActionEvent) dtos.VehicleActionEventResponse {
	return dtos.VehicleActionEventResponse{
		ID:                  event.ID,
		VehicleActionID:     event.VehicleActionID,
		DroneID:             event.DroneID,
		VehicleAgentID:      event.VehicleAgentID,
		Type:                string(event.EventType),
		State:               string(event.State),
		Message:             event.Message,
		Source:              event.Source,
		RawAckCode:          event.RawAckCode,
		Evidence:            event.Evidence,
		TelemetrySnapshotID: event.TelemetrySnapshotID,
		CreatedAt:           rfc3339UTC(event.CreatedAt),
	}
}

func vehicleActionToResponse(action models.VehicleAction) dtos.VehicleActionResponse {
	res := dtos.VehicleActionResponse{
		ID:                 action.ID,
		DroneID:            action.DroneID,
		VehicleAgentID:     action.VehicleAgentID,
		Type:               string(action.Type),
		State:              string(action.State),
		RequestedBy:        action.RequestedBy,
		RequestedAt:        rfc3339UTC(action.RequestedAt),
		UpdatedAt:          rfc3339UTC(action.UpdatedAt),
		DeliveryAttempt:    action.DeliveryAttempt,
		IdempotencyKey:     action.IdempotencyKey,
		AckCorrelationID:   action.AckCorrelationID,
		RawAckCode:         action.RawAckCode,
		PolicyReason:       action.PolicyReason,
		ResultMessage:      action.ResultMessage,
		TelemetryState:     string(action.TelemetryState),
		VehicleAgentStatus: string(action.VehicleAgentStatus),
	}

	if !action.LastSentAt.IsZero() {
		res.LastSentAt = rfc3339UTC(action.LastSentAt)
	}

	if !action.LeaseUntil.IsZero() {
		res.LeaseUntil = rfc3339UTC(action.LeaseUntil)
	}

	if !action.VehicleAckedAt.IsZero() {
		res.VehicleAckedAt = rfc3339UTC(action.VehicleAckedAt)
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

func idempotencyKey(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Idempotency-Key"))
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

func validateGimbalControlRequest(req dtos.GimbalControlRequest) error {
	if req.PitchRateDegS < -maxGimbalRateDegS || req.PitchRateDegS > maxGimbalRateDegS {
		return errors.New("pitchRateDegS is out of range")
	}
	if req.YawRateDegS < -maxGimbalRateDegS || req.YawRateDegS > maxGimbalRateDegS {
		return errors.New("yawRateDegS is out of range")
	}
	return nil
}
