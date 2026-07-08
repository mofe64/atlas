package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

type VehicleAgentRepository struct {
	exec DBExecutor
}

func NewVehicleAgentRepository(db *sql.DB) *VehicleAgentRepository {
	return newVehicleAgentRepository(db)
}

func newVehicleAgentRepository(exec DBExecutor) *VehicleAgentRepository {
	return &VehicleAgentRepository{exec: exec}
}

func (r *VehicleAgentRepository) RevokeActiveVehicleAgentsForDrone(ctx context.Context, droneID string, exceptAgentID string, now time.Time) error {
	_, err := r.exec.ExecContext(ctx, `
		UPDATE vehicle_agents
		SET identity_status = $3,
		    revoked_at = $4,
		    command_channel_state = $5,
		    command_channel_last_disconnected_at = $4
		WHERE drone_id = $1
		  AND id <> $2
		  AND identity_status = $6
	`, droneID, exceptAgentID, string(models.DeviceIdentityRevoked), now, string(models.CommandChannelDisconnected), string(models.DeviceIdentityActive))
	return err
}

func (r *VehicleAgentRepository) UpsertVehicleAgentRegistration(ctx context.Context, agent models.VehicleAgent) error {
	_, err := r.exec.ExecContext(ctx, `
		INSERT INTO vehicle_agents (
		  id, drone_id, version, vehicle_agent_version, identity_status, registered_at, last_seen_at, command_channel_state
		) VALUES ($1, $2, $3, $3, $4, $5, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
		  drone_id = EXCLUDED.drone_id,
		  version = EXCLUDED.version,
		  vehicle_agent_version = EXCLUDED.vehicle_agent_version,
		  identity_status = EXCLUDED.identity_status,
		  last_seen_at = EXCLUDED.last_seen_at,
		  revoked_at = NULL
	`, agent.ID, agent.DroneID, agent.VehicleAgentVersion, string(agent.IdentityStatus), agent.RegisteredAt, string(agent.CommandChannelState))
	return err
}

func (r *VehicleAgentRepository) UpdateVehicleAgentHeartbeat(ctx context.Context, input repository.VehicleAgentHeartbeatInput, now time.Time) (models.VehicleAgent, error) {
	var agent models.VehicleAgent
	res, err := r.exec.ExecContext(ctx, `
		UPDATE vehicle_agents SET version = $2, vehicle_agent_version = $2, last_seen_at = $3, last_heartbeat_at = $3 WHERE id = $1
	`, input.VehicleAgentID, input.VehicleAgentVersion, now)
	if err != nil {
		return models.VehicleAgent{}, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return models.VehicleAgent{}, repository.ErrVehicleAgentNotFound
	}

	agent, err = scanVehicleAgent(r.exec.QueryRowContext(ctx, vehicleAgentByIDSQL, input.VehicleAgentID))
	if err != nil {
		return models.VehicleAgent{}, err
	}

	return agent, nil
}

func (r *VehicleAgentRepository) SetCommandChannelState(ctx context.Context, agentID string, state models.CommandChannelState, now time.Time) (models.VehicleAgent, error) {
	return r.updateCommandChannel(ctx, agentID, state, now, state == models.CommandChannelConnected)
}

const vehicleAgentByIDSQL = `
	SELECT id, drone_id, companion_device_id, version, vehicle_agent_version, identity_status,
	       registered_at, last_seen_at, revoked_at, last_heartbeat_at,
	       command_channel_state, command_channel_connected_at, command_channel_last_disconnected_at
	FROM vehicle_agents WHERE id = $1
`

func scanVehicleAgent(row rowScanner) (models.VehicleAgent, error) {
	var agent models.VehicleAgent
	var companionDeviceID, agentVersion, identityStatus sql.NullString
	var lastSeenAt, revokedAt, lastHeartbeatAt, connectedAt, disconnectedAt sql.NullTime
	var channelState string
	err := row.Scan(
		&agent.ID,
		&agent.DroneID,
		&companionDeviceID,
		&agent.Version,
		&agentVersion,
		&identityStatus,
		&agent.RegisteredAt,
		&lastSeenAt,
		&revokedAt,
		&lastHeartbeatAt,
		&channelState,
		&connectedAt,
		&disconnectedAt,
	)
	if err != nil {
		return models.VehicleAgent{}, err
	}
	agent.CompanionDeviceID = companionDeviceID.String
	agent.VehicleAgentVersion = agentVersion.String
	if agent.VehicleAgentVersion == "" {
		agent.VehicleAgentVersion = agent.Version
	}
	agent.IdentityStatus = models.DeviceIdentityStatus(identityStatus.String)
	if agent.IdentityStatus == "" {
		agent.IdentityStatus = models.DeviceIdentityActive
	}
	agent.LastSeenAt = timeFromNull(lastSeenAt)
	agent.RevokedAt = timeFromNull(revokedAt)
	agent.LastHeartbeatAt = timeFromNull(lastHeartbeatAt)
	agent.CommandChannelState = models.CommandChannelState(channelState)
	agent.CommandChannelConnectedAt = timeFromNull(connectedAt)
	agent.CommandChannelLastDisconnectedAt = timeFromNull(disconnectedAt)
	return agent, nil
}

func (r *VehicleAgentRepository) updateCommandChannel(ctx context.Context, agentID string, state models.CommandChannelState, now time.Time, connected bool) (models.VehicleAgent, error) {
	var agent models.VehicleAgent
	var res sql.Result
	var err error
	if connected {
		res, err = r.exec.ExecContext(ctx, `
			UPDATE vehicle_agents SET command_channel_state = $2, command_channel_connected_at = $3 WHERE id = $1
		`, agentID, string(state), now)
	} else {
		res, err = r.exec.ExecContext(ctx, `
			UPDATE vehicle_agents SET command_channel_state = $2, command_channel_last_disconnected_at = $3 WHERE id = $1
		`, agentID, string(state), now)
	}
	if err != nil {
		return models.VehicleAgent{}, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return models.VehicleAgent{}, repository.ErrVehicleAgentNotFound
	}
	agent, err = scanVehicleAgent(r.exec.QueryRowContext(ctx, vehicleAgentByIDSQL, agentID))
	return agent, err
}

func (r *VehicleAgentRepository) GetVehicleAgentByID(ctx context.Context, agentID string) (models.VehicleAgent, bool, error) {
	agent, err := scanVehicleAgent(r.exec.QueryRowContext(ctx, vehicleAgentByIDSQL, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.VehicleAgent{}, false, nil
	}
	if err != nil {
		return models.VehicleAgent{}, false, err
	}
	return agent, true, nil
}

func (r *VehicleAgentRepository) VehicleAgentExists(ctx context.Context, agentID string) bool {
	return rowExists(ctx, r.exec, `SELECT 1 FROM vehicle_agents WHERE id = $1`, agentID)
}

func (r *VehicleAgentRepository) GetActiveVehicleAgentForDrone(ctx context.Context, droneID string) (models.VehicleAgent, bool, error) {
	return getActiveVehicleAgentForDrone(ctx, r.exec, droneID)
}

// getActiveVehicleAgentForDrone returns the current active vehicle agent assigned to a drone.
// Active means the vehicle agent is command-capable for this drone; ok is false when no active vehicle agent exists.
func getActiveVehicleAgentForDrone(ctx context.Context, q DBExecutor, droneID string) (models.VehicleAgent, bool, error) {
	agent, err := scanVehicleAgent(q.QueryRowContext(ctx, `
		SELECT id, drone_id, companion_device_id, version, vehicle_agent_version, identity_status,
		       registered_at, last_seen_at, revoked_at, last_heartbeat_at,
		       command_channel_state, command_channel_connected_at, command_channel_last_disconnected_at
		FROM vehicle_agents
		WHERE drone_id = $1 AND identity_status = $2
		ORDER BY registered_at DESC, id DESC
		LIMIT 1
	`, droneID, string(models.DeviceIdentityActive)))
	if errors.Is(err, sql.ErrNoRows) {
		return models.VehicleAgent{}, false, nil
	}
	if err != nil {
		return models.VehicleAgent{}, false, err
	}
	return agent, true, nil
}
