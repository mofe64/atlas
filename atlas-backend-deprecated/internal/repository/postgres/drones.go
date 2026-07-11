package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

type DroneRepository struct {
	exec DBExecutor
}

func NewDroneRepository(db *sql.DB) *DroneRepository {
	return newDroneRepository(db)
}

func newDroneRepository(exec DBExecutor) *DroneRepository {
	return &DroneRepository{exec: exec}
}

func (r *DroneRepository) UpsertDroneRegistration(ctx context.Context, droneID string, droneName string, now time.Time) error {
	_, err := r.exec.ExecContext(ctx, `
		INSERT INTO drones (id, name, last_seen_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, last_seen_at = EXCLUDED.last_seen_at
	`, droneID, droneName, now)
	return err
}

func (r *DroneRepository) DroneExists(ctx context.Context, droneID string) bool {
	return rowExists(ctx, r.exec, `SELECT 1 FROM drones WHERE id = $1`, droneID)
}

func (r *DroneRepository) UpdateDroneLastSeen(ctx context.Context, droneID string, now time.Time) error {
	_, err := r.exec.ExecContext(ctx, `UPDATE drones SET last_seen_at = $2 WHERE id = $1`, droneID, now)
	return err
}

func (r *DroneRepository) ListDrones(ctx context.Context, now time.Time) []repository.DroneSnapshot {
	rows, err := r.exec.QueryContext(ctx, `
		SELECT id, name, last_seen_at FROM drones ORDER BY id
	`)
	if err != nil {
		return nil
	}

	var drones []models.Drone
	for rows.Next() {
		var drone models.Drone
		if err := rows.Scan(&drone.ID, &drone.Name, &drone.LastSeenAt); err != nil {
			rows.Close()
			return nil
		}
		drones = append(drones, drone)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil
	}
	rows.Close()

	var snapshots []repository.DroneSnapshot
	for _, drone := range drones {
		// Fleet snapshots use the active vehicle agent to summarize liveness and command-channel state.
		agent, ok, err := getActiveVehicleAgentForDrone(ctx, r.exec, drone.ID)
		if err != nil {
			return nil
		}
		if !ok {
			agent = models.VehicleAgent{}
		}
		telemetry, _ := getTelemetryForDrone(ctx, r.exec, drone.ID)
		snapshots = append(snapshots, repository.DroneSnapshot{
			ID:                     drone.ID,
			Name:                   drone.Name,
			VehicleAgentID:         agent.ID,
			Status:                 models.VehicleAgentStatusFromHeartbeat(agent.LastHeartbeatAt, now),
			LastSeenAt:             drone.LastSeenAt,
			LastHeartbeatAt:        agent.LastHeartbeatAt,
			MAVLinkObserver:        agent.MAVLinkObserverDiagnostics,
			BackendChannelHealth:   agent.BackendChannelHealth,
			Telemetry:              telemetry,
			TelemetryState:         models.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now),
			CommandChannel:         generateCommandChannelSnapshot(agent),
			LatestMissionExecution: latestMissionExecutionForDrone(ctx, r.exec, drone.ID),
		})
	}

	return snapshots
}

func generateCommandChannelSnapshot(agent models.VehicleAgent) repository.CommandChannelSnapshot {
	state := agent.CommandChannelState
	if state == "" {
		state = models.CommandChannelDisconnected
	}

	return repository.CommandChannelSnapshot{
		State:              state,
		ConnectedAt:        agent.CommandChannelConnectedAt,
		LastDisconnectedAt: agent.CommandChannelLastDisconnectedAt,
	}
}
