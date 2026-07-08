package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

type CommandRepository struct {
	exec DBExecutor
}

func NewCommandRepository(db *sql.DB) *CommandRepository {
	return newCommandRepository(db)
}

func newCommandRepository(exec DBExecutor) *CommandRepository {
	return &CommandRepository{exec: exec}
}

func (r *CommandRepository) GenerateCommandID(ctx context.Context) (string, error) {
	return newUUIDv7()
}

func (r *CommandRepository) InsertCommand(ctx context.Context, command models.CommandRequest) error {
	return writeCommand(ctx, r.exec, command, `
		INSERT INTO command_requests (
		  id, drone_id, vehicle_agent_id, type, state, requested_by, requested_at, updated_at,
		  last_sent_at, lease_until, vehicle_acked_at, confirmation_baseline, delivery_attempt,
		  policy_reason, result_message, telemetry_state, vehicle_agent_status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`)
}

func (r *CommandRepository) UpdateCommand(ctx context.Context, command models.CommandRequest) error {
	return writeCommand(ctx, r.exec, command, `
		UPDATE command_requests SET
		  drone_id = $2, vehicle_agent_id = $3, type = $4, state = $5, requested_by = $6,
		  requested_at = $7, updated_at = $8, last_sent_at = $9, lease_until = $10,
		  vehicle_acked_at = $11, confirmation_baseline = $12, delivery_attempt = $13,
		  policy_reason = $14, result_message = $15, telemetry_state = $16, vehicle_agent_status = $17
		WHERE id = $1
	`)
}

func (r *CommandRepository) InsertCommandEvent(ctx context.Context, command models.CommandRequest, eventType string, source string, message string, now time.Time) error {
	eventID, err := newUUIDv7()
	if err != nil {
		return err
	}
	_, err = r.exec.ExecContext(ctx, `
		INSERT INTO command_events (
		  id, command_request_id, drone_id, vehicle_agent_id, event_type, state, message, source, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, eventID, command.ID, command.DroneID, command.VehicleAgentID, eventType, string(command.State), message, source, now)
	return err
}

func (r *CommandRepository) GetCommandByIDForUpdate(ctx context.Context, commandID string) (models.CommandRequest, bool, error) {
	command, err := scanCommand(r.exec.QueryRowContext(ctx, commandSelectSQL+`WHERE id = $1`+forUpdate(), commandID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.CommandRequest{}, false, nil
	}
	if err != nil {
		return models.CommandRequest{}, false, err
	}
	return command, true, nil
}

func (r *CommandRepository) ListCommandsForUpdate(ctx context.Context, filter repository.CommandFilter) ([]models.CommandRequest, error) {
	query, args := commandListQuery(filter)
	query += forUpdate()
	rows, err := r.exec.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCommands(rows)
}

// GetCommandByID fetches a command for read paths that only need existence as a boolean.
func (r *CommandRepository) GetCommandByID(ctx context.Context, commandID string) (models.CommandRequest, bool) {
	command, err := scanCommand(r.exec.QueryRowContext(ctx, commandSelectSQL+`WHERE id = $1`, commandID))
	return command, err == nil
}

// ListCommandsForDrone returns recent command history for a drone.
func (r *CommandRepository) ListCommandsForDrone(ctx context.Context, droneID string, limit int) ([]models.CommandRequest, error) {
	if !rowExists(ctx, r.exec, `SELECT 1 FROM drones WHERE id = $1`, droneID) {
		return nil, repository.ErrDroneNotFound
	}

	query, args := commandListQuery(repository.CommandFilter{
		DroneID: droneID,
		Order:   repository.CommandOrderRequestedDesc,
		Limit:   limit,
	})
	rows, err := r.exec.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCommands(rows)
}

const commandSelectSQL = `
	SELECT id, drone_id, vehicle_agent_id, type, state, requested_by, requested_at, updated_at,
	       last_sent_at, lease_until, vehicle_acked_at, confirmation_baseline,
	       delivery_attempt, policy_reason, result_message, telemetry_state, vehicle_agent_status
	FROM command_requests
`

func commandListQuery(filter repository.CommandFilter) (string, []any) {
	query := commandSelectSQL
	where, args := commandWhere(filter)
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY " + commandOrderSQL(filter.Order)
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return query, args
}

func commandWhere(filter repository.CommandFilter) ([]string, []any) {
	var where []string
	var args []any
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}

	if filter.ID != "" {
		where = append(where, "id = "+addArg(filter.ID))
	}
	if filter.DroneID != "" {
		where = append(where, "drone_id = "+addArg(filter.DroneID))
	}
	if filter.VehicleAgentID != "" {
		where = append(where, "vehicle_agent_id = "+addArg(filter.VehicleAgentID))
	}
	if filter.ExceptID != "" {
		where = append(where, "id <> "+addArg(filter.ExceptID))
	}
	if filter.Type != "" {
		where = append(where, "type = "+addArg(string(filter.Type)))
	}
	if len(filter.States) > 0 {
		placeholders := make([]string, 0, len(filter.States))
		for _, state := range filter.States {
			placeholders = append(placeholders, addArg(string(state)))
		}
		where = append(where, "state IN ("+strings.Join(placeholders, ", ")+")")
	}
	if !filter.RequestedBefore.IsZero() {
		where = append(where, "requested_at < "+addArg(filter.RequestedBefore))
	}
	if !filter.LeaseUntilAtOrBefore.IsZero() {
		where = append(where, "lease_until IS NOT NULL AND lease_until <= "+addArg(filter.LeaseUntilAtOrBefore))
	}
	return where, args
}

func commandOrderSQL(order repository.CommandOrder) string {
	switch order {
	case repository.CommandOrderRequestedAsc:
		return "requested_at, id"
	case repository.CommandOrderRequestedDesc:
		return "requested_at DESC, id DESC"
	default:
		return "requested_at DESC, id DESC"
	}
}

func scanCommand(row rowScanner) (models.CommandRequest, error) {
	var command models.CommandRequest
	var commandType, state, telemetryState, agentStatus string
	var lastSentAt, leaseUntil, vehicleAckedAt sql.NullTime
	var baselineRaw []byte
	err := row.Scan(
		&command.ID,
		&command.DroneID,
		&command.VehicleAgentID,
		&commandType,
		&state,
		&command.RequestedBy,
		&command.RequestedAt,
		&command.UpdatedAt,
		&lastSentAt,
		&leaseUntil,
		&vehicleAckedAt,
		&baselineRaw,
		&command.DeliveryAttempt,
		&command.PolicyReason,
		&command.ResultMessage,
		&telemetryState,
		&agentStatus,
	)
	if err != nil {
		return models.CommandRequest{}, err
	}
	command.Type = models.CommandType(commandType)
	command.State = models.CommandState(state)
	command.LastSentAt = timeFromNull(lastSentAt)
	command.LeaseUntil = timeFromNull(leaseUntil)
	command.VehicleAckedAt = timeFromNull(vehicleAckedAt)
	command.TelemetryState = models.TelemetryState(telemetryState)
	command.VehicleAgentStatus = models.VehicleAgentStatus(agentStatus)
	if len(baselineRaw) > 0 {
		_ = json.Unmarshal(baselineRaw, &command.ConfirmationBaseline)
	}
	return command, nil
}

func scanCommands(rows *sql.Rows) ([]models.CommandRequest, error) {
	var commands []models.CommandRequest
	for rows.Next() {
		command, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, rows.Err()
}

func writeCommand(ctx context.Context, q DBExecutor, command models.CommandRequest, query string) error {
	baseline, err := json.Marshal(command.ConfirmationBaseline)
	if err != nil {
		return err
	}
	if command.ConfirmationBaseline.ReceivedAt.IsZero() && command.ConfirmationBaseline.ObservedAt.IsZero() {
		baseline = nil
	}
	_, err = q.ExecContext(ctx, query,
		command.ID,
		command.DroneID,
		command.VehicleAgentID,
		string(command.Type),
		string(command.State),
		command.RequestedBy,
		command.RequestedAt,
		command.UpdatedAt,
		nullTime(command.LastSentAt),
		nullTime(command.LeaseUntil),
		nullTime(command.VehicleAckedAt),
		baseline,
		command.DeliveryAttempt,
		command.PolicyReason,
		command.ResultMessage,
		string(command.TelemetryState),
		string(command.VehicleAgentStatus),
	)
	return err
}
