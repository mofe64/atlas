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

// CommunicationLinkRepository stores the health of concrete communication
// paths. A link can carry several roles, so roles are persisted as JSON instead
// of forcing one row per role.
type CommunicationLinkRepository struct {
	exec DBExecutor
}

func NewCommunicationLinkRepository(db *sql.DB) *CommunicationLinkRepository {
	return newCommunicationLinkRepository(db)
}

func newCommunicationLinkRepository(exec DBExecutor) *CommunicationLinkRepository {
	return &CommunicationLinkRepository{exec: exec}
}

func (r *CommunicationLinkRepository) GenerateCommunicationLinkID(ctx context.Context) (string, error) {
	return newUUIDv7()
}

func (r *CommunicationLinkRepository) InsertCommunicationLink(ctx context.Context, link models.CommunicationLink) error {
	return writeCommunicationLink(ctx, r.exec, link, `
		INSERT INTO communication_links (
		  id, drone_id, vehicle_agent_id, drone_vehicle_agent_connection_id,
		  link_type, roles, status, transport, endpoint_description,
		  command_eligible, latency_ms, packet_loss_estimate, rx_bytes_per_sec, tx_bytes_per_sec,
		  last_seen_at, created_at, ended_at, ended_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
	`)
}

func (r *CommunicationLinkRepository) UpdateCommunicationLink(ctx context.Context, link models.CommunicationLink) error {
	return writeCommunicationLink(ctx, r.exec, link, `
		UPDATE communication_links SET
		  drone_id = $2,
		  vehicle_agent_id = $3,
		  drone_vehicle_agent_connection_id = $4,
		  link_type = $5,
		  roles = $6,
		  status = $7,
		  transport = $8,
		  endpoint_description = $9,
		  command_eligible = $10,
		  latency_ms = $11,
		  packet_loss_estimate = $12,
		  rx_bytes_per_sec = $13,
		  tx_bytes_per_sec = $14,
		  last_seen_at = $15,
		  created_at = $16,
		  ended_at = $17,
		  ended_reason = $18
		WHERE id = $1
	`)
}

func (r *CommunicationLinkRepository) TouchCommunicationLinksForDroneVehicleAgentConnection(ctx context.Context, connectionID string, now time.Time) error {
	_, err := r.exec.ExecContext(ctx, `
		UPDATE communication_links
		SET last_seen_at = $2
		WHERE drone_vehicle_agent_connection_id = $1
		  AND ended_at IS NULL
	`, connectionID, now)
	return err
}

func (r *CommunicationLinkRepository) EndCommunicationLinksForDroneVehicleAgentConnection(ctx context.Context, connectionID string, status models.CommunicationLinkStatus, endedReason string, now time.Time) error {
	_, err := r.exec.ExecContext(ctx, `
		UPDATE communication_links
		SET status = $2,
		    last_seen_at = $3,
		    ended_at = $3,
		    ended_reason = $4
		WHERE drone_vehicle_agent_connection_id = $1
		  AND ended_at IS NULL
	`, connectionID, string(status), now, endedReason)
	return err
}

func (r *CommunicationLinkRepository) GetCommunicationLinkByID(ctx context.Context, linkID string) (models.CommunicationLink, bool, error) {
	link, err := scanCommunicationLink(r.exec.QueryRowContext(ctx, communicationLinkSelectSQL+`
		WHERE id = $1
	`, linkID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.CommunicationLink{}, false, nil
	}
	if err != nil {
		return models.CommunicationLink{}, false, err
	}
	return link, true, nil
}

func (r *CommunicationLinkRepository) GetCommunicationLinkForDroneVehicleAgentConnection(ctx context.Context, connectionID string) (models.CommunicationLink, bool, error) {
	link, err := scanCommunicationLink(r.exec.QueryRowContext(ctx, communicationLinkSelectSQL+`
		WHERE drone_vehicle_agent_connection_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, connectionID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.CommunicationLink{}, false, nil
	}
	if err != nil {
		return models.CommunicationLink{}, false, err
	}
	return link, true, nil
}

func (r *CommunicationLinkRepository) GetOpenCommunicationLinkByLocalEndpoint(ctx context.Context, droneID string, linkType models.CommunicationLinkType, transport string, endpointDescription string) (models.CommunicationLink, bool, error) {
	link, err := scanCommunicationLink(r.exec.QueryRowContext(ctx, communicationLinkSelectSQL+`
		WHERE drone_id = $1
		  AND link_type = $2
		  AND transport = $3
		  AND endpoint_description = $4
		  AND vehicle_agent_id IS NULL
		  AND drone_vehicle_agent_connection_id IS NULL
		  AND ended_at IS NULL
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, droneID, string(linkType), transport, endpointDescription))
	if errors.Is(err, sql.ErrNoRows) {
		return models.CommunicationLink{}, false, nil
	}
	if err != nil {
		return models.CommunicationLink{}, false, err
	}
	return link, true, nil
}

func (r *CommunicationLinkRepository) ListCommunicationLinksForDrone(ctx context.Context, droneID string) ([]models.CommunicationLink, error) {
	if !rowExists(ctx, r.exec, `SELECT 1 FROM drones WHERE id = $1`, droneID) {
		return nil, repository.ErrDroneNotFound
	}

	rows, err := r.exec.QueryContext(ctx, communicationLinkSelectSQL+`
		WHERE drone_id = $1
		ORDER BY ended_at IS NULL DESC, last_seen_at DESC NULLS LAST, created_at DESC, id DESC
	`, droneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanCommunicationLinks(rows)
}

const communicationLinkSelectSQL = `
	SELECT id, drone_id, vehicle_agent_id, drone_vehicle_agent_connection_id,
	       link_type, roles, status, transport, endpoint_description,
	       command_eligible, latency_ms, packet_loss_estimate, rx_bytes_per_sec, tx_bytes_per_sec,
	       last_seen_at, created_at, ended_at, ended_reason
	FROM communication_links
`

func writeCommunicationLink(ctx context.Context, q DBExecutor, link models.CommunicationLink, query string) error {
	roles, err := marshalCommunicationLinkRoles(link.Roles)
	if err != nil {
		return err
	}

	_, err = q.ExecContext(ctx, query,
		link.ID,
		link.DroneID,
		nullString(link.VehicleAgentID),
		nullString(link.DroneVehicleAgentConnectionID),
		string(link.LinkType),
		roles,
		string(link.Status),
		link.Transport,
		link.EndpointDescription,
		link.CommandEligible,
		nullFloat64(link.LatencyMs),
		nullFloat64(link.PacketLossEstimate),
		nullFloat64(link.RxBytesPerSec),
		nullFloat64(link.TxBytesPerSec),
		nullTime(link.LastSeenAt),
		link.CreatedAt,
		nullTime(link.EndedAt),
		link.EndedReason,
	)
	return err
}

func scanCommunicationLink(row rowScanner) (models.CommunicationLink, error) {
	var link models.CommunicationLink
	var vehicleAgentID, droneVehicleAgentConnectionID sql.NullString
	var linkType, status string
	var latencyMs, packetLossEstimate, rxBytesPerSec, txBytesPerSec sql.NullFloat64
	var lastSeenAt, endedAt sql.NullTime
	var rawRoles []byte
	err := row.Scan(
		&link.ID,
		&link.DroneID,
		&vehicleAgentID,
		&droneVehicleAgentConnectionID,
		&linkType,
		&rawRoles,
		&status,
		&link.Transport,
		&link.EndpointDescription,
		&link.CommandEligible,
		&latencyMs,
		&packetLossEstimate,
		&rxBytesPerSec,
		&txBytesPerSec,
		&lastSeenAt,
		&link.CreatedAt,
		&endedAt,
		&link.EndedReason,
	)
	if err != nil {
		return models.CommunicationLink{}, err
	}

	link.VehicleAgentID = vehicleAgentID.String
	link.DroneVehicleAgentConnectionID = droneVehicleAgentConnectionID.String
	link.LinkType = models.CommunicationLinkType(linkType)
	link.Roles = unmarshalCommunicationLinkRoles(rawRoles)
	link.Status = models.CommunicationLinkStatus(status)
	link.LatencyMs = latencyMs.Float64
	link.PacketLossEstimate = packetLossEstimate.Float64
	link.RxBytesPerSec = rxBytesPerSec.Float64
	link.TxBytesPerSec = txBytesPerSec.Float64
	link.LastSeenAt = timeFromNull(lastSeenAt)
	link.EndedAt = timeFromNull(endedAt)
	return link, nil
}

func scanCommunicationLinks(rows *sql.Rows) ([]models.CommunicationLink, error) {
	links := []models.CommunicationLink{}
	for rows.Next() {
		link, err := scanCommunicationLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return links, nil
}

func marshalCommunicationLinkRoles(roles []models.CommunicationLinkRole) ([]byte, error) {
	values := make([]string, 0, len(roles))
	for _, role := range roles {
		values = append(values, string(role))
	}
	return json.Marshal(values)
}

func unmarshalCommunicationLinkRoles(raw []byte) []models.CommunicationLinkRole {
	var values []string
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil {
		return nil
	}

	roles := make([]models.CommunicationLinkRole, 0, len(values))
	for _, value := range values {
		roles = append(roles, models.CommunicationLinkRole(value))
	}
	return roles
}
