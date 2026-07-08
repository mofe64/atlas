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

// FleetDrone combines a drone snapshot with recent commands for the operator fleet overview.
type FleetDrone struct {
	Snapshot repository.DroneSnapshot
	Commands []models.CommandRequest
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

// ListDrones returns the fleet overview with recent command context for each drone.
func (s *FleetService) ListDrones(ctx context.Context, now time.Time, commandLimit int) []FleetDrone {
	snapshots := s.repos.Drones.ListDrones(ctx, now)
	drones := make([]FleetDrone, 0, len(snapshots))
	for _, snapshot := range snapshots {
		commands, err := s.repos.Commands.ListCommandsForDrone(ctx, snapshot.ID, commandLimit)
		if err != nil {
			commands = nil
		}
		drones = append(drones, FleetDrone{Snapshot: snapshot, Commands: commands})
	}
	return drones
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
