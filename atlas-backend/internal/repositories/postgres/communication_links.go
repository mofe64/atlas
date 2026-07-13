package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

type CommunicationLinkRepository struct {
	tx database.TxExecutor
}

var _ repositories.CommunicationLinkRepository = (*CommunicationLinkRepository)(nil)

func NewCommunicationLinkRepository(tx database.TxExecutor) *CommunicationLinkRepository {
	return &CommunicationLinkRepository{tx: tx}
}

func (r *CommunicationLinkRepository) Create(ctx context.Context, input models.NewCommunicationLink) (models.CommunicationLink, error) {
	roles, err := json.Marshal(input.Roles)
	if err != nil {
		return models.CommunicationLink{}, err
	}
	var link models.CommunicationLink
	err = scanCommunicationLink(r.tx.QueryRow(ctx, `
		INSERT INTO communication_links (
			organization_id, vehicle_agent_binding_id, session_instance_id,
			transport, roles, remote_address, command_eligible, opened_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, organization_id, vehicle_agent_binding_id, session_instance_id,
		          transport, roles, status, remote_address, command_eligible,
		          opened_at, first_heartbeat_at, last_heartbeat_at,
		          latency_ms, packet_loss_estimate, rx_bytes_per_second,
		          tx_bytes_per_second, closed_at, close_reason
	`, input.OrganizationID, input.VehicleAgentBindingID, input.SessionInstanceID,
		input.Transport, roles, input.RemoteAddress, input.CommandEligible, input.Now), &link)
	if err != nil {
		return models.CommunicationLink{}, mapDatabaseError("insert communication link", err)
	}
	return link, nil
}

func (r *CommunicationLinkRepository) FindByID(ctx context.Context, organizationID, linkID string) (models.CommunicationLink, error) {
	return r.find(ctx, "organization_id = $1 AND id = $2", organizationID, linkID)
}

func (r *CommunicationLinkRepository) FindBySessionInstanceID(ctx context.Context, organizationID, bindingID, sessionID string) (models.CommunicationLink, error) {
	return r.find(ctx, "organization_id = $1 AND vehicle_agent_binding_id = $2 AND session_instance_id = $3", organizationID, bindingID, sessionID)
}

func (r *CommunicationLinkRepository) RecordHeartbeat(ctx context.Context, organizationID, linkID string, health models.CommunicationLinkHealth, now time.Time) (models.CommunicationLink, error) {
	var link models.CommunicationLink
	err := scanCommunicationLink(r.tx.QueryRow(ctx, `
		UPDATE communication_links
		SET status = 'healthy',
		    first_heartbeat_at = COALESCE(first_heartbeat_at, $3),
		    last_heartbeat_at = $3,
		    latency_ms = $4,
		    packet_loss_estimate = $5,
		    rx_bytes_per_second = $6,
		    tx_bytes_per_second = $7
		WHERE organization_id = $1 AND id = $2 AND closed_at IS NULL
		RETURNING id, organization_id, vehicle_agent_binding_id, session_instance_id,
		          transport, roles, status, remote_address, command_eligible,
		          opened_at, first_heartbeat_at, last_heartbeat_at,
		          latency_ms, packet_loss_estimate, rx_bytes_per_second,
		          tx_bytes_per_second, closed_at, close_reason
	`, organizationID, linkID, now, health.LatencyMS, health.PacketLossEstimate,
		health.RXBytesPerSecond, health.TXBytesPerSecond), &link)
	if err != nil {
		return models.CommunicationLink{}, mapDatabaseError("record communication-link heartbeat", err)
	}
	return link, nil
}

func (r *CommunicationLinkRepository) Close(ctx context.Context, organizationID, linkID, reason string, now time.Time) (models.CommunicationLink, error) {
	link, err := r.FindByID(ctx, organizationID, linkID)
	if err != nil {
		return models.CommunicationLink{}, err
	}
	if link.ClosedAt != nil {
		return link, nil
	}
	err = scanCommunicationLink(r.tx.QueryRow(ctx, `
		UPDATE communication_links
		SET status = 'closed', closed_at = $3, close_reason = $4
		WHERE organization_id = $1 AND id = $2 AND closed_at IS NULL
		RETURNING id, organization_id, vehicle_agent_binding_id, session_instance_id,
		          transport, roles, status, remote_address, command_eligible,
		          opened_at, first_heartbeat_at, last_heartbeat_at,
		          latency_ms, packet_loss_estimate, rx_bytes_per_second,
		          tx_bytes_per_second, closed_at, close_reason
	`, organizationID, linkID, now, reason), &link)
	if err != nil {
		return models.CommunicationLink{}, mapDatabaseError("close communication link", err)
	}
	return link, nil
}

func (r *CommunicationLinkRepository) CloseOpenForBinding(ctx context.Context, organizationID, bindingID, reason string, now time.Time) error {
	_, err := r.tx.Exec(ctx, `
		UPDATE communication_links
		SET status = 'closed', closed_at = $3, close_reason = $4
		WHERE organization_id = $1 AND vehicle_agent_binding_id = $2 AND closed_at IS NULL
	`, organizationID, bindingID, now, reason)
	if err != nil {
		return mapDatabaseError("close communication links for binding", err)
	}
	return nil
}

func (r *CommunicationLinkRepository) FindCurrentCommandLink(ctx context.Context, organizationID, bindingID string) (models.CommunicationLink, error) {
	var link models.CommunicationLink
	err := scanCommunicationLink(r.tx.QueryRow(ctx, `
		SELECT l.id, l.organization_id, l.vehicle_agent_binding_id, l.session_instance_id,
		       l.transport, l.roles, l.status, l.remote_address, l.command_eligible,
		       l.opened_at, l.first_heartbeat_at, l.last_heartbeat_at,
		       l.latency_ms, l.packet_loss_estimate, l.rx_bytes_per_second,
		       l.tx_bytes_per_second, l.closed_at, l.close_reason
		FROM communication_links l
		JOIN vehicle_agent_bindings b
		  ON b.organization_id = l.organization_id AND b.id = l.vehicle_agent_binding_id
		WHERE l.organization_id = $1 AND l.vehicle_agent_binding_id = $2
		  AND b.status = 'active'
		  AND l.command_eligible = true AND l.status = 'healthy' AND l.closed_at IS NULL
		ORDER BY l.last_heartbeat_at DESC NULLS LAST, l.opened_at DESC
		LIMIT 1
	`, organizationID, bindingID), &link)
	if err != nil {
		return models.CommunicationLink{}, mapDatabaseError("find current command link", err)
	}
	return link, nil
}

func (r *CommunicationLinkRepository) find(ctx context.Context, predicate string, args ...any) (models.CommunicationLink, error) {
	var link models.CommunicationLink
	err := scanCommunicationLink(r.tx.QueryRow(ctx, communicationLinkSelect+` WHERE `+predicate, args...), &link)
	if err != nil {
		return models.CommunicationLink{}, mapDatabaseError("find communication link", err)
	}
	return link, nil
}

const communicationLinkSelect = `
	SELECT id, organization_id, vehicle_agent_binding_id, session_instance_id,
	       transport, roles, status, remote_address, command_eligible,
	       opened_at, first_heartbeat_at, last_heartbeat_at,
	       latency_ms, packet_loss_estimate, rx_bytes_per_second,
	       tx_bytes_per_second, closed_at, close_reason
	FROM communication_links
`

func scanCommunicationLink(row rowScanner, link *models.CommunicationLink) error {
	var roles []byte
	var latency, packetLoss, rxBytes, txBytes sql.NullFloat64
	if err := row.Scan(
		&link.ID,
		&link.OrganizationID,
		&link.VehicleAgentBindingID,
		&link.SessionInstanceID,
		&link.Transport,
		&roles,
		&link.Status,
		&link.RemoteAddress,
		&link.CommandEligible,
		&link.OpenedAt,
		&link.FirstHeartbeatAt,
		&link.LastHeartbeatAt,
		&latency,
		&packetLoss,
		&rxBytes,
		&txBytes,
		&link.ClosedAt,
		&link.CloseReason,
	); err != nil {
		return err
	}
	if err := json.Unmarshal(roles, &link.Roles); err != nil {
		return err
	}
	link.LatencyMS = optionalFloat64(latency)
	link.PacketLossEstimate = optionalFloat64(packetLoss)
	link.RXBytesPerSecond = optionalFloat64(rxBytes)
	link.TXBytesPerSecond = optionalFloat64(txBytes)
	return nil
}

func optionalFloat64(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}
