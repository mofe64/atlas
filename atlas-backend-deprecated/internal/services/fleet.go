package services

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

// FleetService assembles read models used by fleet and mission detail views.
type FleetService struct {
	repos repository.Repositories
}

// FleetDrone combines a drone snapshot with recent vehicle actions for the operator fleet overview.
type FleetDrone struct {
	Snapshot           repository.DroneSnapshot
	VehicleActions     []models.VehicleAction
	CommunicationLinks []models.CommunicationLink
}

// MissionDetail combines a mission definition with its execution history for detail screens.
type MissionDetail struct {
	Mission    models.Mission
	Executions []models.MissionExecution
}

// NewFleetService builds the read-side service for operator-facing fleet summaries.
func NewFleetService(repos repository.Repositories) *FleetService {
	return &FleetService{repos: repos}
}

// ListDrones returns the fleet overview with recent vehicle action context for each drone.
func (s *FleetService) ListDrones(ctx context.Context, now time.Time, actionLimit int) []FleetDrone {
	snapshots := s.repos.Drones.ListDrones(ctx, now)
	drones := make([]FleetDrone, 0, len(snapshots))
	for _, snapshot := range snapshots {
		actions, err := s.repos.VehicleActions.ListVehicleActionsForDrone(ctx, snapshot.ID, actionLimit)
		if err != nil {
			actions = nil
		}
		links, err := s.repos.CommunicationLinks.ListCommunicationLinksForDrone(ctx, snapshot.ID)
		if err != nil {
			links = nil
		}
		drones = append(drones, FleetDrone{
			Snapshot:           snapshot,
			VehicleActions:     actions,
			CommunicationLinks: links,
		})
	}
	return drones
}

// ListCommunicationLinksForDrone returns all observed communication paths for one drone.
func (s *FleetService) ListCommunicationLinksForDrone(ctx context.Context, droneID string) ([]models.CommunicationLink, error) {
	return s.repos.CommunicationLinks.ListCommunicationLinksForDrone(ctx, droneID)
}

// ListTelemetryFeedsForDrone returns the telemetry producers observed for one drone.
func (s *FleetService) ListTelemetryFeedsForDrone(ctx context.Context, droneID string) ([]models.TelemetryFeed, error) {
	return s.repos.TelemetryFeeds.ListTelemetryFeedsForDrone(ctx, droneID)
}

// MissionDetail returns the mission and execution history shown on mission detail pages.
func (s *FleetService) MissionDetail(ctx context.Context, missionID string) (MissionDetail, error) {
	mission, ok := s.repos.Missions.GetMissionByID(ctx, missionID)
	if !ok {
		return MissionDetail{}, repository.ErrMissionNotFound
	}

	executions, err := s.repos.MissionExecutions.ListMissionExecutions(ctx, missionID)
	if err != nil {
		return MissionDetail{}, err
	}

	return MissionDetail{
		Mission:    mission,
		Executions: executions,
	}, nil
}
