package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/registry"
)

const serviceName = "atlas-backend"

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
	GPSFix            string  `json:"gpsFix"`
	SatellitesVisible int     `json:"satellitesVisible"`
	HomePositionSet   bool    `json:"homePositionSet"`
	Source            string  `json:"source"`
}

type droneResponse struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	AgentID         string                     `json:"agentId"`
	Status          string                     `json:"status"`
	LastSeenAt      string                     `json:"lastSeenAt"`
	LastHeartbeatAt string                     `json:"lastHeartbeatAt,omitempty"`
	Telemetry       *telemetrySnapshotResponse `json:"telemetry,omitempty"`
}

func NewRouter() http.Handler {
	return NewRouterWithRegistry(registry.NewMemoryRegistry())
}

func NewRouterWithRegistry(reg *registry.MemoryRegistry) http.Handler {
	router := chi.NewRouter()

	router.Get("/healthz", healthz)
	router.Get("/version", version)
	router.Route("/api", func(router chi.Router) {
		router.Post("/agents/register", registerAgent(reg))
		router.Post("/agents/{agentID}/heartbeat", heartbeat(reg))
		router.Post("/agents/{agentID}/telemetry", recordTelemetry(reg))
		router.Get("/drones", listDrones(reg))
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

func registerAgent(reg *registry.MemoryRegistry) http.HandlerFunc {
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

func heartbeat(reg *registry.MemoryRegistry) http.HandlerFunc {
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

func recordTelemetry(reg *registry.MemoryRegistry) http.HandlerFunc {
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

func listDrones(reg *registry.MemoryRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snapshots := reg.ListDrones(time.Now().UTC())
		res := make([]droneResponse, 0, len(snapshots))

		for _, drone := range snapshots {
			item := droneResponse{
				ID:         drone.ID,
				Name:       drone.Name,
				AgentID:    drone.AgentID,
				Status:     string(drone.Status),
				LastSeenAt: drone.LastSeenAt.Format(time.RFC3339),
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
					GPSFix:            drone.Telemetry.GPSFix,
					SatellitesVisible: drone.Telemetry.SatellitesVisible,
					HomePositionSet:   drone.Telemetry.HomePositionSet,
					Source:            drone.Telemetry.Source,
				}
			}

			res = append(res, item)
		}

		writeJSON(w, http.StatusOK, res)
	}
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
