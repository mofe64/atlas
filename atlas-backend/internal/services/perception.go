package services

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

const defaultPerceptionEventLimit = 25
const maxPerceptionEventLimit = 100

var ErrPerceptionDroneMismatch = errors.New("perception event drone does not match vehicle agent")

// PerceptionService records compact perception metadata and tracks live
// inference health. Detection events are persisted; process health is volatile
// runtime state because no health table exists in the Atlas store.
type PerceptionService struct {
	txManager repository.TxManager
	repos     repository.Repositories

	mu             sync.RWMutex
	healthByDrone  map[string]models.PerceptionHealth
	healthBySource map[string]models.PerceptionHealth
}

// PerceptionStatus combines the latest runtime health with recent stored
// detection events for API consumers.
type PerceptionStatus struct {
	DroneID          string
	SourceID         string
	InputConnected   bool
	OutputPublishing bool
	ModelLoaded      bool
	Accelerator      string
	FPS              float64
	DroppedFrames    uint64
	LastFrameAt      time.Time
	LastDetectionAt  time.Time
	LastError        string
	ModelName        string
	ModelVersion     string
	UpdatedAt        time.Time
	ActiveCounts     map[string]int
	LatestDetections []models.PerceptionDetection
	LatestEvent      models.PerceptionEvent
}

// NewPerceptionService builds the workflow service for agent perception ingestion.
func NewPerceptionService(txManager repository.TxManager, repos repository.Repositories) *PerceptionService {
	return &PerceptionService{
		txManager:      txManager,
		repos:          repos,
		healthByDrone:  map[string]models.PerceptionHealth{},
		healthBySource: map[string]models.PerceptionHealth{},
	}
}

func (s *PerceptionService) RecordPerceptionEvent(ctx context.Context, vehicleAgentID string, event models.PerceptionEvent, now time.Time) (models.PerceptionEvent, error) {
	var recorded models.PerceptionEvent
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		agent, ok, err := repos.VehicleAgents.GetVehicleAgentByID(ctx, vehicleAgentID)
		if err != nil {
			return err
		}
		if !ok {
			return repository.ErrVehicleAgentNotFound
		}

		if strings.TrimSpace(event.DroneID) == "" {
			event.DroneID = agent.DroneID
		}
		if event.DroneID != agent.DroneID {
			return ErrPerceptionDroneMismatch
		}
		if event.ObservedAt.IsZero() {
			event.ObservedAt = now
		}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = now
		}
		id, err := repos.Perception.GeneratePerceptionEventID(ctx)
		if err != nil {
			return err
		}
		event.ID = id
		if err := repos.Perception.InsertPerceptionEvent(ctx, event); err != nil {
			return err
		}
		recorded = event
		return nil
	})
	return recorded, err
}

func (s *PerceptionService) RecordPerceptionHealth(ctx context.Context, vehicleAgentID string, health models.PerceptionHealth, now time.Time) error {
	var recorded models.PerceptionHealth
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		agent, ok, err := repos.VehicleAgents.GetVehicleAgentByID(ctx, vehicleAgentID)
		if err != nil {
			return err
		}
		if !ok {
			return repository.ErrVehicleAgentNotFound
		}
		if strings.TrimSpace(health.DroneID) == "" {
			health.DroneID = agent.DroneID
		}
		if health.DroneID != agent.DroneID {
			return ErrPerceptionDroneMismatch
		}
		if health.UpdatedAt.IsZero() {
			health.UpdatedAt = now
		}
		recorded = health
		return nil
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthByDrone[recorded.DroneID] = recorded
	if recorded.SourceID != "" {
		s.healthBySource[recorded.DroneID+"\x00"+recorded.SourceID] = recorded
	}
	return nil
}

func (s *PerceptionService) ListPerceptionEvents(ctx context.Context, droneID string, limit int) ([]models.PerceptionEvent, error) {
	if limit <= 0 {
		limit = defaultPerceptionEventLimit
	}
	if limit > maxPerceptionEventLimit {
		limit = maxPerceptionEventLimit
	}
	if !s.repos.Drones.DroneExists(ctx, droneID) {
		return nil, repository.ErrDroneNotFound
	}
	return s.repos.Perception.ListPerceptionEventsForDrone(ctx, droneID, limit)
}

func (s *PerceptionService) PerceptionStatus(ctx context.Context, droneID string) (PerceptionStatus, error) {
	if !s.repos.Drones.DroneExists(ctx, droneID) {
		return PerceptionStatus{}, repository.ErrDroneNotFound
	}

	status := PerceptionStatus{
		DroneID:       droneID,
		ActiveCounts:  map[string]int{},
		UpdatedAt:     time.Now().UTC(),
		Accelerator:   "unknown",
		ModelName:     "",
		ModelVersion:  "",
		LastError:     "",
		DroppedFrames: 0,
	}

	s.mu.RLock()
	if health, ok := s.healthByDrone[droneID]; ok {
		status.SourceID = health.SourceID
		status.InputConnected = health.InputConnected
		status.OutputPublishing = health.OutputPublishing
		status.ModelLoaded = health.ModelLoaded
		status.Accelerator = health.Accelerator
		status.FPS = health.FPS
		status.DroppedFrames = health.DroppedFrames
		status.LastFrameAt = health.LastFrameAt
		status.LastDetectionAt = health.LastDetectionAt
		status.LastError = health.LastError
		status.ModelName = health.ModelName
		status.ModelVersion = health.ModelVersion
		status.UpdatedAt = health.UpdatedAt
	}
	s.mu.RUnlock()

	event, ok, err := s.repos.Perception.LatestPerceptionEventForDrone(ctx, droneID)
	if err != nil {
		return PerceptionStatus{}, err
	}
	if ok {
		status.LatestEvent = event
		status.LatestDetections = event.Detections
		if status.SourceID == "" {
			status.SourceID = event.VideoSourceID
		}
		if status.ModelName == "" {
			status.ModelName = event.ModelName
		}
		if status.ModelVersion == "" {
			status.ModelVersion = event.ModelVersion
		}
		if status.LastDetectionAt.IsZero() && len(event.Detections) > 0 {
			status.LastDetectionAt = event.ObservedAt
		}
		for _, detection := range event.Detections {
			if detection.ClassName != "" {
				status.ActiveCounts[detection.ClassName]++
			}
		}
	}

	return status, nil
}

func PerceptionClassCounts(detections []models.PerceptionDetection) map[string]int {
	counts := map[string]int{}
	for _, detection := range detections {
		if detection.ClassName == "" {
			continue
		}
		counts[detection.ClassName]++
	}
	return counts
}

func SortedPerceptionClasses(counts map[string]int) []string {
	classes := make([]string, 0, len(counts))
	for className := range counts {
		classes = append(classes, className)
	}
	sort.Strings(classes)
	return classes
}
