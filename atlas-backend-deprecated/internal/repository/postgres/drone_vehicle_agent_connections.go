package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

// DroneVehicleAgentConnectionRepository persists one row per backend-to-agent
// stream. Keeping this repository narrow makes it clear that it is about the
// runtime session, not the long-lived vehicle agent identity.
type DroneVehicleAgentConnectionRepository struct {
	exec DBExecutor
}

func NewDroneVehicleAgentConnectionRepository(db *sql.DB) *DroneVehicleAgentConnectionRepository {
	return newDroneVehicleAgentConnectionRepository(db)
}

func newDroneVehicleAgentConnectionRepository(exec DBExecutor) *DroneVehicleAgentConnectionRepository {
	return &DroneVehicleAgentConnectionRepository{exec: exec}
}

func (r *DroneVehicleAgentConnectionRepository) GenerateDroneVehicleAgentConnectionID(ctx context.Context) (string, error) {
	return newUUIDv7()
}

func (r *DroneVehicleAgentConnectionRepository) InsertDroneVehicleAgentConnection(ctx context.Context, connection models.DroneVehicleAgentConnection) error {
	capabilities, err := json.Marshal(connection.Capabilities)
	if err != nil {
		return err
	}

	_, err = r.exec.ExecContext(ctx, `
		INSERT INTO drone_vehicle_agent_connections (
		  id, vehicle_agent_id, drone_id, connection_id, transport, remote_address,
		  wire_guard_peer_id, status, started_at, last_heartbeat_at, ended_at,
		  ended_reason, vehicle_agent_version, capabilities
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, connection.ID,
		connection.VehicleAgentID,
		connection.DroneID,
		connection.ConnectionID,
		connection.Transport,
		connection.RemoteAddress,
		connection.WireGuardPeerID,
		string(connection.Status),
		connection.StartedAt,
		nullTime(connection.LastHeartbeatAt),
		nullTime(connection.EndedAt),
		connection.EndedReason,
		connection.VehicleAgentVersion,
		capabilities,
	)
	return err
}

func (r *DroneVehicleAgentConnectionRepository) UpdateDroneVehicleAgentConnectionHeartbeat(ctx context.Context, connectionID string, now time.Time) (models.DroneVehicleAgentConnection, error) {
	connection, err := scanDroneVehicleAgentConnection(r.exec.QueryRowContext(ctx, droneVehicleAgentConnectionSelectSQL+`
		WHERE id = $1 AND ended_at IS NULL
	`, connectionID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.DroneVehicleAgentConnection{}, repository.ErrDroneVehicleAgentConnectionNotFound
	}
	if err != nil {
		return models.DroneVehicleAgentConnection{}, err
	}

	connection.LastHeartbeatAt = now
	if err := r.updateDroneVehicleAgentConnection(ctx, connection); err != nil {
		return models.DroneVehicleAgentConnection{}, err
	}
	return connection, nil
}

func (r *DroneVehicleAgentConnectionRepository) EndDroneVehicleAgentConnection(ctx context.Context, connectionID string, status models.DroneVehicleAgentConnectionStatus, endedReason string, now time.Time) (models.DroneVehicleAgentConnection, error) {
	connection, err := scanDroneVehicleAgentConnection(r.exec.QueryRowContext(ctx, droneVehicleAgentConnectionSelectSQL+`
		WHERE id = $1
	`, connectionID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.DroneVehicleAgentConnection{}, repository.ErrDroneVehicleAgentConnectionNotFound
	}
	if err != nil {
		return models.DroneVehicleAgentConnection{}, err
	}

	connection.Status = status
	connection.EndedAt = now
	connection.EndedReason = endedReason
	if err := r.updateDroneVehicleAgentConnection(ctx, connection); err != nil {
		return models.DroneVehicleAgentConnection{}, err
	}
	return connection, nil
}

func (r *DroneVehicleAgentConnectionRepository) GetDroneVehicleAgentConnectionByID(ctx context.Context, connectionID string) (models.DroneVehicleAgentConnection, bool, error) {
	connection, err := scanDroneVehicleAgentConnection(r.exec.QueryRowContext(ctx, droneVehicleAgentConnectionSelectSQL+`
		WHERE id = $1
	`, connectionID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.DroneVehicleAgentConnection{}, false, nil
	}
	if err != nil {
		return models.DroneVehicleAgentConnection{}, false, err
	}
	return connection, true, nil
}

func (r *DroneVehicleAgentConnectionRepository) LatestActiveDroneVehicleAgentConnectionForAgent(ctx context.Context, agentID string) (models.DroneVehicleAgentConnection, bool, error) {
	connection, err := scanDroneVehicleAgentConnection(r.exec.QueryRowContext(ctx, droneVehicleAgentConnectionSelectSQL+`
		WHERE vehicle_agent_id = $1
		  AND ended_at IS NULL
		  AND status IN ($2, $3, $4)
		ORDER BY started_at DESC, id DESC
		LIMIT 1
	`, agentID,
		string(models.DroneVehicleAgentConnectionConnected),
		string(models.DroneVehicleAgentConnectionDegraded),
		string(models.DroneVehicleAgentConnectionStale),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return models.DroneVehicleAgentConnection{}, false, nil
	}
	if err != nil {
		return models.DroneVehicleAgentConnection{}, false, err
	}
	return connection, true, nil
}

func (r *DroneVehicleAgentConnectionRepository) updateDroneVehicleAgentConnection(ctx context.Context, connection models.DroneVehicleAgentConnection) error {
	capabilities, err := json.Marshal(connection.Capabilities)
	if err != nil {
		return err
	}

	res, err := r.exec.ExecContext(ctx, `
		UPDATE drone_vehicle_agent_connections SET
		  vehicle_agent_id = $2,
		  drone_id = $3,
		  connection_id = $4,
		  transport = $5,
		  remote_address = $6,
		  wire_guard_peer_id = $7,
		  status = $8,
		  started_at = $9,
		  last_heartbeat_at = $10,
		  ended_at = $11,
		  ended_reason = $12,
		  vehicle_agent_version = $13,
		  capabilities = $14
		WHERE id = $1
	`, connection.ID,
		connection.VehicleAgentID,
		connection.DroneID,
		connection.ConnectionID,
		connection.Transport,
		connection.RemoteAddress,
		connection.WireGuardPeerID,
		string(connection.Status),
		connection.StartedAt,
		nullTime(connection.LastHeartbeatAt),
		nullTime(connection.EndedAt),
		connection.EndedReason,
		connection.VehicleAgentVersion,
		capabilities,
	)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return repository.ErrDroneVehicleAgentConnectionNotFound
	}
	return nil
}

const droneVehicleAgentConnectionSelectSQL = `
	SELECT id, vehicle_agent_id, drone_id, connection_id, transport, remote_address,
	       wire_guard_peer_id, status, started_at, last_heartbeat_at, ended_at,
	       ended_reason, vehicle_agent_version, capabilities
	FROM drone_vehicle_agent_connections
`

func scanDroneVehicleAgentConnection(row rowScanner) (models.DroneVehicleAgentConnection, error) {
	var connection models.DroneVehicleAgentConnection
	var status string
	var lastHeartbeatAt, endedAt sql.NullTime
	var rawCapabilities []byte
	err := row.Scan(
		&connection.ID,
		&connection.VehicleAgentID,
		&connection.DroneID,
		&connection.ConnectionID,
		&connection.Transport,
		&connection.RemoteAddress,
		&connection.WireGuardPeerID,
		&status,
		&connection.StartedAt,
		&lastHeartbeatAt,
		&endedAt,
		&connection.EndedReason,
		&connection.VehicleAgentVersion,
		&rawCapabilities,
	)
	if err != nil {
		return models.DroneVehicleAgentConnection{}, err
	}

	connection.Status = models.DroneVehicleAgentConnectionStatus(status)
	connection.LastHeartbeatAt = timeFromNull(lastHeartbeatAt)
	connection.EndedAt = timeFromNull(endedAt)
	if len(rawCapabilities) > 0 {
		_ = json.Unmarshal(rawCapabilities, &connection.Capabilities)
	}
	return connection, nil
}
