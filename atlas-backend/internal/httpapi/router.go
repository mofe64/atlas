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
	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/registry"
)

const serviceName = "atlas-backend"

const droneStreamInterval = time.Second

var droneStreamUpgrader = websocket.Upgrader{
	CheckOrigin: allowDroneStreamOrigin,
}

type CommandDispatcher interface {
	DispatchCommand(ctx context.Context, command domain.OperatorCommand) (domain.OperatorCommand, bool)
}

type MissionExecutionDispatcher interface {
	DispatchMissionExecution(ctx context.Context, execution domain.MissionExecution) (domain.MissionExecution, bool)
}

type healthResponse struct {
	Service string `json:"service"`
	Status  string `json:"status"`
	Time    string `json:"time"`
}

type versionResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
}

type registerAgentRequest struct {
	AgentID      string `json:"agentId"`
	DroneID      string `json:"droneId"`
	DroneName    string `json:"droneName"`
	AgentVersion string `json:"agentVersion"`
}

type registerAgentResponse struct {
	AgentID                  string `json:"agentId"`
	DroneID                  string `json:"droneId"`
	Status                   string `json:"status"`
	HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
}

type heartbeatRequest struct {
	AgentVersion string `json:"agentVersion"`
}

type heartbeatResponse struct {
	AgentID            string `json:"agentId"`
	DroneID            string `json:"droneId"`
	Status             string `json:"status"`
	LastHeartbeatAt    string `json:"lastHeartbeatAt"`
	NextHeartbeatAfter int    `json:"nextHeartbeatAfterSeconds"`
}

type telemetryRequest struct {
	ObservedAt        string  `json:"observedAt"`
	BatteryPercent    float64 `json:"batteryPercent"`
	RelativeAltitudeM float64 `json:"relativeAltitudeM"`
	FlightMode        string  `json:"flightMode"`
	Armed             bool    `json:"armed"`
	InAir             bool    `json:"inAir"`
	Latitude          float64 `json:"latitude"`
	Longitude         float64 `json:"longitude"`
	HeadingDeg        float64 `json:"headingDeg"`
	GroundSpeedMPS    float64 `json:"groundSpeedMPS"`
	GPSFix            string  `json:"gpsFix"`
	SatellitesVisible int     `json:"satellitesVisible"`
	HomePositionSet   bool    `json:"homePositionSet"`
	Source            string  `json:"source"`
}

type telemetryResponse struct {
	DroneID        string `json:"droneId"`
	AgentID        string `json:"agentId"`
	TelemetryState string `json:"telemetryState"`
	ReceivedAt     string `json:"receivedAt"`
}

type telemetrySnapshotResponse struct {
	State             string  `json:"state"`
	ObservedAt        string  `json:"observedAt"`
	ReceivedAt        string  `json:"receivedAt"`
	BatteryPercent    float64 `json:"batteryPercent"`
	RelativeAltitudeM float64 `json:"relativeAltitudeM"`
	FlightMode        string  `json:"flightMode"`
	Armed             bool    `json:"armed"`
	InAir             bool    `json:"inAir"`
	Latitude          float64 `json:"latitude"`
	Longitude         float64 `json:"longitude"`
	HeadingDeg        float64 `json:"headingDeg"`
	GroundSpeedMPS    float64 `json:"groundSpeedMPS"`
	GPSFix            string  `json:"gpsFix"`
	SatellitesVisible int     `json:"satellitesVisible"`
	HomePositionSet   bool    `json:"homePositionSet"`
	Source            string  `json:"source"`
}

type commandChannelResponse struct {
	State              string `json:"state"`
	ConnectedAt        string `json:"connectedAt,omitempty"`
	LastDisconnectedAt string `json:"lastDisconnectedAt,omitempty"`
}

type commandResponse struct {
	ID              string `json:"id"`
	DroneID         string `json:"droneId"`
	AgentID         string `json:"agentId"`
	Type            string `json:"type"`
	State           string `json:"state"`
	RequestedBy     string `json:"requestedBy"`
	RequestedAt     string `json:"requestedAt"`
	UpdatedAt       string `json:"updatedAt"`
	LastSentAt      string `json:"lastSentAt,omitempty"`
	LeaseUntil      string `json:"leaseUntil,omitempty"`
	VehicleAckedAt  string `json:"vehicleAckedAt,omitempty"`
	DeliveryAttempt int    `json:"deliveryAttempt"`
	PolicyReason    string `json:"policyReason,omitempty"`
	ResultMessage   string `json:"resultMessage,omitempty"`
	TelemetryState  string `json:"telemetryState"`
	AgentStatus     string `json:"agentStatus"`
}

type createMissionRequest struct {
	Name             string                   `json:"name"`
	CompletionAction string                   `json:"completionAction,omitempty"`
	Waypoints        []missionWaypointRequest `json:"waypoints"`
}

type missionWaypointRequest struct {
	Latitude          float64  `json:"latitude"`
	Longitude         float64  `json:"longitude"`
	RelativeAltitudeM float64  `json:"relativeAltitudeM"`
	SpeedMPS          *float64 `json:"speedMPS,omitempty"`
	LoiterTimeS       *float64 `json:"loiterTimeS,omitempty"`
}

type missionResponse struct {
	ID               string                    `json:"id"`
	DroneID          string                    `json:"droneId"`
	Name             string                    `json:"name"`
	CreatedBy        string                    `json:"createdBy"`
	CreatedAt        string                    `json:"createdAt"`
	UpdatedAt        string                    `json:"updatedAt"`
	CompletionAction string                    `json:"completionAction"`
	ValidationStatus string                    `json:"validationStatus"`
	ValidationErrors []missionValidationError  `json:"validationErrors,omitempty"`
	Waypoints        []missionWaypointResponse `json:"waypoints"`
}

type missionWaypointResponse struct {
	Sequence          int      `json:"sequence"`
	Latitude          float64  `json:"latitude"`
	Longitude         float64  `json:"longitude"`
	RelativeAltitudeM float64  `json:"relativeAltitudeM"`
	SpeedMPS          *float64 `json:"speedMPS,omitempty"`
	LoiterTimeS       *float64 `json:"loiterTimeS,omitempty"`
}

type missionValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type missionExecutionResponse struct {
	ID                 string `json:"id"`
	MissionID          string `json:"missionId"`
	DroneID            string `json:"droneId"`
	AgentID            string `json:"agentId"`
	RequestedBy        string `json:"requestedBy"`
	UploadRequestedBy  string `json:"uploadRequestedBy,omitempty"`
	StartRequestedBy   string `json:"startRequestedBy,omitempty"`
	State              string `json:"state"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
	LastSentAt         string `json:"lastSentAt,omitempty"`
	LeaseUntil         string `json:"leaseUntil,omitempty"`
	UploadRequestedAt  string `json:"uploadRequestedAt,omitempty"`
	UploadedAt         string `json:"uploadedAt,omitempty"`
	StartRequestedAt   string `json:"startRequestedAt,omitempty"`
	StartedAt          string `json:"startedAt,omitempty"`
	CompletedAt        string `json:"completedAt,omitempty"`
	HoldAt             string `json:"holdAt,omitempty"`
	FailedAt           string `json:"failedAt,omitempty"`
	CurrentMissionItem int    `json:"currentMissionItem,omitempty"`
	TotalMissionItems  int    `json:"totalMissionItems,omitempty"`
	ProgressUpdatedAt  string `json:"progressUpdatedAt,omitempty"`
	DeliveryAttempt    int    `json:"deliveryAttempt"`
	ResultMessage      string `json:"resultMessage,omitempty"`
}

type missionExecutionEventResponse struct {
	ID                 string `json:"id"`
	ExecutionID        string `json:"executionId"`
	MissionID          string `json:"missionId"`
	DroneID            string `json:"droneId"`
	AgentID            string `json:"agentId"`
	Type               string `json:"type"`
	State              string `json:"state"`
	Message            string `json:"message"`
	CurrentMissionItem int    `json:"currentMissionItem,omitempty"`
	TotalMissionItems  int    `json:"totalMissionItems,omitempty"`
	Source             string `json:"source"`
	CreatedAt          string `json:"createdAt"`
}

type missionDetailResponse struct {
	Mission    missionResponse            `json:"mission"`
	Executions []missionExecutionResponse `json:"executions"`
}

type missionStreamEventResponse struct {
	Type   string                `json:"type"`
	Detail missionDetailResponse `json:"detail"`
}

type commandStatusRequest struct {
	State         string `json:"state"`
	ResultMessage string `json:"resultMessage"`
}

type droneResponse struct {
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	AgentID          string                     `json:"agentId"`
	Status           string                     `json:"status"`
	LastSeenAt       string                     `json:"lastSeenAt"`
	LastHeartbeatAt  string                     `json:"lastHeartbeatAt,omitempty"`
	Telemetry        *telemetrySnapshotResponse `json:"telemetry,omitempty"`
	CommandChannel   commandChannelResponse     `json:"commandChannel"`
	Commands         []commandResponse          `json:"commands"`
	MissionExecution *missionExecutionResponse  `json:"missionExecution,omitempty"`
}

func NewRouter() http.Handler {
	return NewRouterWithRegistry(registry.NewMemoryRegistry())
}

func NewRouterWithRegistry(reg registry.Store) http.Handler {
	return NewRouterWithRegistryAndCommandDispatcher(reg, nil)
}

func NewRouterWithRegistryAndCommandDispatcher(reg registry.Store, dispatcher CommandDispatcher) http.Handler {
	return NewRouterWithRegistryAndDispatchers(reg, dispatcher, nil)
}

func NewRouterWithRegistryAndDispatchers(reg registry.Store, commandDispatcher CommandDispatcher, missionDispatcher MissionExecutionDispatcher) http.Handler {
	router := chi.NewRouter()

	router.Get("/healthz", healthz)
	router.Get("/version", version)
	router.Route("/api", func(router chi.Router) {
		router.Post("/agents/register", registerAgent(reg))
		router.Post("/agents/{agentID}/heartbeat", heartbeat(reg))
		router.Post("/agents/{agentID}/telemetry", recordTelemetry(reg))
		router.Get("/agents/{agentID}/commands/next", nextCommandForAgent(reg))
		router.Post("/agents/{agentID}/commands/{commandID}/status", updateCommandStatus(reg))
		router.Get("/drones", listDrones(reg))
		router.Get("/drones/stream", streamDrones(reg))
		router.Get("/drones/{droneID}/missions", listMissionsForDrone(reg))
		router.Post("/drones/{droneID}/missions", createMission(reg))
		router.Get("/missions/{missionID}", getMission(reg))
		router.Get("/missions/{missionID}/executions", listMissionExecutions(reg))
		router.Get("/missions/{missionID}/events", listMissionExecutionEvents(reg))
		router.Get("/missions/{missionID}/stream", streamMission(reg))
		router.Post("/missions/{missionID}/upload", requestMissionUpload(reg, missionDispatcher))
		router.Post("/missions/{missionID}/start", requestMissionStart(reg, missionDispatcher))
		router.Post("/missions/{missionID}/abort", requestMissionAbort(reg, missionDispatcher))
		router.Get("/drones/{droneID}/commands", listCommandsForDrone(reg))
		router.Post("/drones/{droneID}/commands/arm", requestCommand(reg, commandDispatcher, domain.CommandTypeArm))
		router.Post("/drones/{droneID}/commands/takeoff", requestCommand(reg, commandDispatcher, domain.CommandTypeTakeoff))
		router.Post("/drones/{droneID}/commands/return-to-launch", requestCommand(reg, commandDispatcher, domain.CommandTypeReturnToLaunch))
		router.Post("/drones/{droneID}/commands/land", requestCommand(reg, commandDispatcher, domain.CommandTypeLand))
	})

	return router
}

func healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Service: serviceName,
		Status:  "ok",
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

func version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, versionResponse{
		Service: serviceName,
		Version: "0.1.0-dev",
	})
}

func registerAgent(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req registerAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
			return
		}

		if err := validateRegisterAgentRequest(req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		agent := reg.RegisterAgent(registry.RegisterAgentInput{
			AgentID:      req.AgentID,
			DroneID:      req.DroneID,
			DroneName:    req.DroneName,
			AgentVersion: req.AgentVersion,
		}, time.Now().UTC())

		writeJSON(w, http.StatusOK, registerAgentResponse{
			AgentID:                  agent.ID,
			DroneID:                  agent.DroneID,
			Status:                   "registered",
			HeartbeatIntervalSeconds: int(domain.HeartbeatInterval.Seconds()),
		})
	}
}

func heartbeat(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "agentID")

		var req heartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
			return
		}

		if strings.TrimSpace(agentID) == "" {
			writeError(w, http.StatusBadRequest, "agentId is required")
			return
		}

		if strings.TrimSpace(req.AgentVersion) == "" {
			writeError(w, http.StatusBadRequest, "agentVersion is required")
			return
		}

		now := time.Now().UTC()
		agent, err := reg.RecordHeartbeat(registry.HeartbeatInput{
			AgentID:      agentID,
			AgentVersion: req.AgentVersion,
		}, now)
		if err != nil {
			if errors.Is(err, registry.ErrAgentNotFound) {
				writeError(w, http.StatusNotFound, "agent is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to record heartbeat")
			return
		}

		writeJSON(w, http.StatusOK, heartbeatResponse{
			AgentID:            agent.ID,
			DroneID:            agent.DroneID,
			Status:             "online",
			LastHeartbeatAt:    agent.LastHeartbeatAt.Format(time.RFC3339),
			NextHeartbeatAfter: int(domain.HeartbeatInterval.Seconds()),
		})
	}
}

func recordTelemetry(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "agentID")
		if strings.TrimSpace(agentID) == "" {
			writeError(w, http.StatusBadRequest, "agentId is required")
			return
		}

		var req telemetryRequest
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
		snapshot, err = reg.RecordTelemetry(snapshot, now)
		if err != nil {
			if errors.Is(err, registry.ErrAgentNotFound) {
				writeError(w, http.StatusNotFound, "agent is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to record telemetry")
			return
		}

		writeJSON(w, http.StatusOK, telemetryResponse{
			DroneID:        snapshot.DroneID,
			AgentID:        snapshot.AgentID,
			TelemetryState: string(domain.TelemetryStateFresh),
			ReceivedAt:     snapshot.ReceivedAt.Format(time.RFC3339),
		})
	}
}

func listDrones(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, droneResponses(reg, time.Now().UTC()))
	}
}

func streamDrones(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := droneStreamUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		if err := writeDroneStreamSnapshot(conn, reg); err != nil {
			return
		}

		ticker := time.NewTicker(droneStreamInterval)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if err := writeDroneStreamSnapshot(conn, reg); err != nil {
					return
				}
			}
		}
	}
}

func createMission(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		droneID := chi.URLParam(r, "droneID")
		if strings.TrimSpace(droneID) == "" {
			writeError(w, http.StatusBadRequest, "droneId is required")
			return
		}

		var req createMissionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
			return
		}

		mission, err := reg.CreateMission(registry.CreateMissionInput{
			DroneID:          droneID,
			Name:             req.Name,
			CreatedBy:        requestedBy(r),
			Waypoints:        missionWaypointInputs(req.Waypoints),
			CompletionAction: domain.MissionCompletionAction(req.CompletionAction),
		}, time.Now().UTC())
		if err != nil {
			if errors.Is(err, registry.ErrDroneNotFound) {
				writeError(w, http.StatusNotFound, "drone is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to create mission")
			return
		}

		status := http.StatusCreated
		if mission.ValidationStatus == domain.MissionValidationStatusRejected {
			status = http.StatusConflict
		}

		writeJSON(w, status, missionToResponse(mission))
	}
}

func listMissionsForDrone(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		droneID := chi.URLParam(r, "droneID")
		if strings.TrimSpace(droneID) == "" {
			writeError(w, http.StatusBadRequest, "droneId is required")
			return
		}

		missions, err := reg.ListMissionsForDrone(droneID)
		if err != nil {
			if errors.Is(err, registry.ErrDroneNotFound) {
				writeError(w, http.StatusNotFound, "drone is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to list missions")
			return
		}

		res := make([]missionResponse, 0, len(missions))
		for _, mission := range missions {
			res = append(res, missionToResponse(mission))
		}

		writeJSON(w, http.StatusOK, res)
	}
}

func getMission(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		detail, err := missionDetail(reg, missionID)
		if err != nil {
			if errors.Is(err, registry.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to fetch mission")
			return
		}

		writeJSON(w, http.StatusOK, detail)
	}
}

func listMissionExecutions(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		executions, err := reg.ListMissionExecutions(missionID)
		if err != nil {
			if errors.Is(err, registry.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to list mission executions")
			return
		}

		writeJSON(w, http.StatusOK, missionExecutionResponses(executions))
	}
}

func listMissionExecutionEvents(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		events, err := reg.ListMissionExecutionEvents(missionID)
		if err != nil {
			if errors.Is(err, registry.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to list mission execution events")
			return
		}

		writeJSON(w, http.StatusOK, missionExecutionEventResponses(events))
	}
}

func streamMission(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		if _, ok := reg.MissionByID(missionID); !ok {
			writeError(w, http.StatusNotFound, "mission was not found")
			return
		}

		conn, err := droneStreamUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		detail, err := missionDetail(reg, missionID)
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
				detail, err := missionDetail(reg, missionID)
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

func requestMissionUpload(reg registry.Store, dispatcher MissionExecutionDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		execution, err := reg.RequestMissionUpload(registry.RequestMissionUploadInput{
			MissionID:   missionID,
			RequestedBy: requestedBy(r),
		}, time.Now().UTC())
		if err != nil {
			if errors.Is(err, registry.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			if errors.Is(err, registry.ErrMissionNotValidated) {
				writeError(w, http.StatusConflict, "mission must be validated before upload")
				return
			}

			if errors.Is(err, registry.ErrAgentNotFound) {
				writeError(w, http.StatusConflict, "drone has no registered agent")
				return
			}

			if errors.Is(err, registry.ErrDroneMissionActive) {
				writeError(w, http.StatusConflict, "drone already has an active mission execution")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to request mission upload")
			return
		}

		if dispatcher != nil {
			if dispatched, ok := dispatcher.DispatchMissionExecution(r.Context(), execution); ok {
				execution = dispatched
			}
		}

		writeJSON(w, http.StatusAccepted, missionExecutionToResponse(execution))
	}
}

func requestMissionStart(reg registry.Store, dispatcher MissionExecutionDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		execution, err := reg.RequestMissionStart(registry.RequestMissionStartInput{
			MissionID:   missionID,
			RequestedBy: requestedBy(r),
		}, time.Now().UTC())
		if err != nil {
			var preconditionErr registry.MissionStartPreconditionError
			if errors.As(err, &preconditionErr) {
				writeError(w, http.StatusConflict, preconditionErr.Error())
				return
			}

			if errors.Is(err, registry.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			if errors.Is(err, registry.ErrInvalidMissionExecutionState) {
				writeError(w, http.StatusConflict, "mission must be uploaded to vehicle before start")
				return
			}

			if errors.Is(err, registry.ErrDroneMissionActive) {
				writeError(w, http.StatusConflict, "drone already has an active mission execution")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to request mission start")
			return
		}

		if dispatcher != nil {
			if dispatched, ok := dispatcher.DispatchMissionExecution(r.Context(), execution); ok {
				execution = dispatched
			}
		}

		writeJSON(w, http.StatusAccepted, missionExecutionToResponse(execution))
	}
}

func requestMissionAbort(reg registry.Store, dispatcher MissionExecutionDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		missionID := chi.URLParam(r, "missionID")
		if strings.TrimSpace(missionID) == "" {
			writeError(w, http.StatusBadRequest, "missionId is required")
			return
		}

		execution, err := reg.RequestMissionAbort(registry.RequestMissionAbortInput{
			MissionID:   missionID,
			RequestedBy: requestedBy(r),
		}, time.Now().UTC())
		if err != nil {
			if errors.Is(err, registry.ErrMissionNotFound) {
				writeError(w, http.StatusNotFound, "mission was not found")
				return
			}

			if errors.Is(err, registry.ErrInvalidMissionExecutionState) {
				writeError(w, http.StatusConflict, "mission must be active before aborting to RTL")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to request mission abort")
			return
		}

		if dispatcher != nil {
			if dispatched, ok := dispatcher.DispatchMissionExecution(r.Context(), execution); ok {
				execution = dispatched
			}
		}

		writeJSON(w, http.StatusAccepted, missionExecutionToResponse(execution))
	}
}

func listCommandsForDrone(reg registry.Store) http.HandlerFunc {
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

		commands, err := reg.ListCommandsForDrone(droneID, limit)
		if err != nil {
			if errors.Is(err, registry.ErrDroneNotFound) {
				writeError(w, http.StatusNotFound, "drone is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to list commands")
			return
		}

		res := make([]commandResponse, 0, len(commands))
		for _, command := range commands {
			res = append(res, commandToResponse(command))
		}

		writeJSON(w, http.StatusOK, res)
	}
}

func requestCommand(reg registry.Store, dispatcher CommandDispatcher, commandType domain.CommandType) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		droneID := chi.URLParam(r, "droneID")
		if strings.TrimSpace(droneID) == "" {
			writeError(w, http.StatusBadRequest, "droneId is required")
			return
		}

		command, err := reg.RequestCommand(registry.RequestCommandInput{
			DroneID:     droneID,
			Type:        commandType,
			RequestedBy: requestedBy(r),
		}, time.Now().UTC())
		if err != nil {
			if errors.Is(err, registry.ErrDroneNotFound) {
				writeError(w, http.StatusNotFound, "drone is not registered")
				return
			}

			if errors.Is(err, registry.ErrAgentNotFound) {
				writeError(w, http.StatusConflict, "drone has no registered agent")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to request command")
			return
		}

		status := http.StatusAccepted
		if command.State == domain.CommandStateRejectedByPolicy {
			status = http.StatusConflict
		} else if dispatcher != nil {
			if dispatched, ok := dispatcher.DispatchCommand(r.Context(), command); ok {
				command = dispatched
			}
		}

		writeJSON(w, status, commandToResponse(command))
	}
}

func nextCommandForAgent(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "agentID")
		if strings.TrimSpace(agentID) == "" {
			writeError(w, http.StatusBadRequest, "agentId is required")
			return
		}

		command, ok, err := reg.NextCommandForAgent(agentID, time.Now().UTC())
		if err != nil {
			if errors.Is(err, registry.ErrAgentNotFound) {
				writeError(w, http.StatusNotFound, "agent is not registered")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to fetch next command")
			return
		}

		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		writeJSON(w, http.StatusOK, commandToResponse(command))
	}
}

func updateCommandStatus(reg registry.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "agentID")
		if strings.TrimSpace(agentID) == "" {
			writeError(w, http.StatusBadRequest, "agentId is required")
			return
		}

		commandID := chi.URLParam(r, "commandID")
		if strings.TrimSpace(commandID) == "" {
			writeError(w, http.StatusBadRequest, "commandId is required")
			return
		}

		var req commandStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON request body")
			return
		}

		if strings.TrimSpace(req.State) == "" {
			writeError(w, http.StatusBadRequest, "state is required")
			return
		}

		command, err := reg.UpdateCommandStatus(registry.UpdateCommandStatusInput{
			AgentID:       agentID,
			CommandID:     commandID,
			State:         domain.CommandState(req.State),
			ResultMessage: strings.TrimSpace(req.ResultMessage),
		}, time.Now().UTC())
		if err != nil {
			if errors.Is(err, registry.ErrCommandNotFound) {
				writeError(w, http.StatusNotFound, "command was not found")
				return
			}

			if errors.Is(err, registry.ErrCommandNotAssigned) {
				writeError(w, http.StatusForbidden, "command is not assigned to this agent")
				return
			}

			if errors.Is(err, registry.ErrInvalidCommandState) {
				writeError(w, http.StatusBadRequest, "state cannot be reported by an agent")
				return
			}

			if errors.Is(err, registry.ErrInvalidCommandTransition) {
				writeError(w, http.StatusConflict, "command cannot transition to requested state")
				return
			}

			writeError(w, http.StatusInternalServerError, "failed to update command status")
			return
		}

		writeJSON(w, http.StatusOK, commandToResponse(command))
	}
}

func writeDroneStreamSnapshot(conn *websocket.Conn, reg registry.Store) error {
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	return conn.WriteJSON(droneResponses(reg, time.Now().UTC()))
}

func writeMissionStreamEvent(conn *websocket.Conn, eventType string, detail missionDetailResponse) error {
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	return conn.WriteJSON(missionStreamEventResponse{
		Type:   eventType,
		Detail: detail,
	})
}

func missionDetailSignature(detail missionDetailResponse) string {
	payload, err := json.Marshal(detail)
	if err != nil {
		return ""
	}

	return string(payload)
}

func droneResponses(reg registry.Store, now time.Time) []droneResponse {
	snapshots := reg.ListDrones(now)
	res := make([]droneResponse, 0, len(snapshots))

	for _, drone := range snapshots {
		item := droneResponse{
			ID:             drone.ID,
			Name:           drone.Name,
			AgentID:        drone.AgentID,
			Status:         string(drone.Status),
			LastSeenAt:     drone.LastSeenAt.Format(time.RFC3339),
			CommandChannel: commandChannelToResponse(drone.CommandChannel),
			Commands:       commandResponsesForDrone(reg, drone.ID, 8),
		}

		if !drone.LastHeartbeatAt.IsZero() {
			item.LastHeartbeatAt = drone.LastHeartbeatAt.Format(time.RFC3339)
		}

		if !drone.Telemetry.ReceivedAt.IsZero() {
			item.Telemetry = &telemetrySnapshotResponse{
				State:             string(drone.TelemetryState),
				ObservedAt:        drone.Telemetry.ObservedAt.Format(time.RFC3339),
				ReceivedAt:        drone.Telemetry.ReceivedAt.Format(time.RFC3339),
				BatteryPercent:    drone.Telemetry.BatteryPercent,
				RelativeAltitudeM: drone.Telemetry.RelativeAltitudeM,
				FlightMode:        drone.Telemetry.FlightMode,
				Armed:             drone.Telemetry.Armed,
				InAir:             drone.Telemetry.InAir,
				Latitude:          drone.Telemetry.Latitude,
				Longitude:         drone.Telemetry.Longitude,
				HeadingDeg:        drone.Telemetry.HeadingDeg,
				GroundSpeedMPS:    drone.Telemetry.GroundSpeedMPS,
				GPSFix:            drone.Telemetry.GPSFix,
				SatellitesVisible: drone.Telemetry.SatellitesVisible,
				HomePositionSet:   drone.Telemetry.HomePositionSet,
				Source:            drone.Telemetry.Source,
			}
		}

		if drone.LatestMissionExecution.ID != "" {
			execution := missionExecutionToResponse(drone.LatestMissionExecution)
			item.MissionExecution = &execution
		}

		res = append(res, item)
	}

	return res
}

func missionDetail(reg registry.Store, missionID string) (missionDetailResponse, error) {
	mission, ok := reg.MissionByID(missionID)
	if !ok {
		return missionDetailResponse{}, registry.ErrMissionNotFound
	}

	executions, err := reg.ListMissionExecutions(missionID)
	if err != nil {
		return missionDetailResponse{}, err
	}

	return missionDetailResponse{
		Mission:    missionToResponse(mission),
		Executions: missionExecutionResponses(executions),
	}, nil
}

func missionExecutionResponses(executions []domain.MissionExecution) []missionExecutionResponse {
	res := make([]missionExecutionResponse, 0, len(executions))
	for _, execution := range executions {
		res = append(res, missionExecutionToResponse(execution))
	}

	return res
}

func missionExecutionEventResponses(events []domain.MissionExecutionEvent) []missionExecutionEventResponse {
	res := make([]missionExecutionEventResponse, 0, len(events))
	for _, event := range events {
		res = append(res, missionExecutionEventToResponse(event))
	}

	return res
}

func commandResponsesForDrone(reg registry.Store, droneID string, limit int) []commandResponse {
	commands, err := reg.ListCommandsForDrone(droneID, limit)
	if err != nil {
		return nil
	}

	res := make([]commandResponse, 0, len(commands))
	for _, command := range commands {
		res = append(res, commandToResponse(command))
	}

	return res
}

func commandChannelToResponse(channel registry.CommandChannelSnapshot) commandChannelResponse {
	res := commandChannelResponse{
		State: string(channel.State),
	}

	if !channel.ConnectedAt.IsZero() {
		res.ConnectedAt = channel.ConnectedAt.Format(time.RFC3339)
	}

	if !channel.LastDisconnectedAt.IsZero() {
		res.LastDisconnectedAt = channel.LastDisconnectedAt.Format(time.RFC3339)
	}

	return res
}

func telemetryRequestToSnapshot(agentID string, req telemetryRequest) (domain.TelemetrySnapshot, error) {
	observedAt, err := time.Parse(time.RFC3339Nano, req.ObservedAt)
	if err != nil {
		return domain.TelemetrySnapshot{}, errors.New("observedAt must be an RFC3339 timestamp")
	}

	if req.BatteryPercent < 0 || req.BatteryPercent > 100 {
		return domain.TelemetrySnapshot{}, errors.New("batteryPercent must be between 0 and 100")
	}

	if req.Latitude < -90 || req.Latitude > 90 {
		return domain.TelemetrySnapshot{}, errors.New("latitude must be between -90 and 90")
	}

	if req.Longitude < -180 || req.Longitude > 180 {
		return domain.TelemetrySnapshot{}, errors.New("longitude must be between -180 and 180")
	}

	if strings.TrimSpace(req.FlightMode) == "" {
		return domain.TelemetrySnapshot{}, errors.New("flightMode is required")
	}

	if strings.TrimSpace(req.GPSFix) == "" {
		return domain.TelemetrySnapshot{}, errors.New("gpsFix is required")
	}

	if strings.TrimSpace(req.Source) == "" {
		return domain.TelemetrySnapshot{}, errors.New("source is required")
	}

	return domain.TelemetrySnapshot{
		AgentID:           agentID,
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

func missionWaypointInputs(waypoints []missionWaypointRequest) []registry.MissionWaypointInput {
	inputs := make([]registry.MissionWaypointInput, 0, len(waypoints))
	for _, waypoint := range waypoints {
		inputs = append(inputs, registry.MissionWaypointInput{
			Latitude:          waypoint.Latitude,
			Longitude:         waypoint.Longitude,
			RelativeAltitudeM: waypoint.RelativeAltitudeM,
			SpeedMPS:          waypoint.SpeedMPS,
			LoiterTimeS:       waypoint.LoiterTimeS,
		})
	}

	return inputs
}

func missionToResponse(mission domain.Mission) missionResponse {
	res := missionResponse{
		ID:               mission.ID,
		DroneID:          mission.DroneID,
		Name:             mission.Name,
		CreatedBy:        mission.CreatedBy,
		CreatedAt:        mission.CreatedAt.Format(time.RFC3339),
		UpdatedAt:        mission.UpdatedAt.Format(time.RFC3339),
		CompletionAction: string(mission.CompletionAction),
		ValidationStatus: string(mission.ValidationStatus),
		Waypoints:        make([]missionWaypointResponse, 0, len(mission.Waypoints)),
		ValidationErrors: make([]missionValidationError, 0, len(mission.ValidationErrors)),
	}

	for _, waypoint := range mission.Waypoints {
		res.Waypoints = append(res.Waypoints, missionWaypointResponse{
			Sequence:          waypoint.Sequence,
			Latitude:          waypoint.Latitude,
			Longitude:         waypoint.Longitude,
			RelativeAltitudeM: waypoint.RelativeAltitudeM,
			SpeedMPS:          waypoint.SpeedMPS,
			LoiterTimeS:       waypoint.LoiterTimeS,
		})
	}

	for _, validationError := range mission.ValidationErrors {
		res.ValidationErrors = append(res.ValidationErrors, missionValidationError{
			Field:   validationError.Field,
			Message: validationError.Message,
		})
	}

	return res
}

func missionExecutionToResponse(execution domain.MissionExecution) missionExecutionResponse {
	res := missionExecutionResponse{
		ID:                 execution.ID,
		MissionID:          execution.MissionID,
		DroneID:            execution.DroneID,
		AgentID:            execution.AgentID,
		RequestedBy:        execution.RequestedBy,
		UploadRequestedBy:  execution.UploadRequestedBy,
		StartRequestedBy:   execution.StartRequestedBy,
		State:              string(execution.State),
		CreatedAt:          execution.CreatedAt.Format(time.RFC3339),
		UpdatedAt:          execution.UpdatedAt.Format(time.RFC3339),
		CurrentMissionItem: execution.CurrentMissionItem,
		TotalMissionItems:  execution.TotalMissionItems,
		DeliveryAttempt:    execution.DeliveryAttempt,
		ResultMessage:      execution.ResultMessage,
	}

	if !execution.LastSentAt.IsZero() {
		res.LastSentAt = execution.LastSentAt.Format(time.RFC3339)
	}

	if !execution.LeaseUntil.IsZero() {
		res.LeaseUntil = execution.LeaseUntil.Format(time.RFC3339)
	}

	if !execution.UploadRequestedAt.IsZero() {
		res.UploadRequestedAt = execution.UploadRequestedAt.Format(time.RFC3339)
	}

	if !execution.UploadedAt.IsZero() {
		res.UploadedAt = execution.UploadedAt.Format(time.RFC3339)
	}

	if !execution.StartRequestedAt.IsZero() {
		res.StartRequestedAt = execution.StartRequestedAt.Format(time.RFC3339)
	}

	if !execution.StartedAt.IsZero() {
		res.StartedAt = execution.StartedAt.Format(time.RFC3339)
	}

	if !execution.CompletedAt.IsZero() {
		res.CompletedAt = execution.CompletedAt.Format(time.RFC3339)
	}

	if !execution.HoldAt.IsZero() {
		res.HoldAt = execution.HoldAt.Format(time.RFC3339)
	}

	if !execution.FailedAt.IsZero() {
		res.FailedAt = execution.FailedAt.Format(time.RFC3339)
	}

	if !execution.ProgressUpdatedAt.IsZero() {
		res.ProgressUpdatedAt = execution.ProgressUpdatedAt.Format(time.RFC3339)
	}

	return res
}

func missionExecutionEventToResponse(event domain.MissionExecutionEvent) missionExecutionEventResponse {
	return missionExecutionEventResponse{
		ID:                 event.ID,
		ExecutionID:        event.ExecutionID,
		MissionID:          event.MissionID,
		DroneID:            event.DroneID,
		AgentID:            event.AgentID,
		Type:               event.Type,
		State:              string(event.State),
		Message:            event.Message,
		CurrentMissionItem: event.CurrentMissionItem,
		TotalMissionItems:  event.TotalMissionItems,
		Source:             event.Source,
		CreatedAt:          event.CreatedAt.Format(time.RFC3339),
	}
}

func commandToResponse(command domain.OperatorCommand) commandResponse {
	res := commandResponse{
		ID:              command.ID,
		DroneID:         command.DroneID,
		AgentID:         command.AgentID,
		Type:            string(command.Type),
		State:           string(command.State),
		RequestedBy:     command.RequestedBy,
		RequestedAt:     command.RequestedAt.Format(time.RFC3339),
		UpdatedAt:       command.UpdatedAt.Format(time.RFC3339),
		DeliveryAttempt: command.DeliveryAttempt,
		PolicyReason:    command.PolicyReason,
		ResultMessage:   command.ResultMessage,
		TelemetryState:  string(command.TelemetryState),
		AgentStatus:     string(command.AgentStatus),
	}

	if !command.LastSentAt.IsZero() {
		res.LastSentAt = command.LastSentAt.Format(time.RFC3339)
	}

	if !command.LeaseUntil.IsZero() {
		res.LeaseUntil = command.LeaseUntil.Format(time.RFC3339)
	}

	if !command.VehicleAckedAt.IsZero() {
		res.VehicleAckedAt = command.VehicleAckedAt.Format(time.RFC3339)
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

func validateRegisterAgentRequest(req registerAgentRequest) error {
	if strings.TrimSpace(req.AgentID) == "" {
		return errors.New("agentId is required")
	}

	if strings.TrimSpace(req.DroneID) == "" {
		return errors.New("droneId is required")
	}

	if strings.TrimSpace(req.DroneName) == "" {
		return errors.New("droneName is required")
	}

	if strings.TrimSpace(req.AgentVersion) == "" {
		return errors.New("agentVersion is required")
	}

	return nil
}
