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

type VehicleActionRepository struct {
	exec DBExecutor
}

func NewVehicleActionRepository(db *sql.DB) *VehicleActionRepository {
	return newVehicleActionRepository(db)
}

func newVehicleActionRepository(exec DBExecutor) *VehicleActionRepository {
	return &VehicleActionRepository{exec: exec}
}

func (r *VehicleActionRepository) GenerateVehicleActionID(ctx context.Context) (string, error) {
	return newUUIDv7()
}

func (r *VehicleActionRepository) InsertVehicleAction(ctx context.Context, action models.VehicleAction) error {
	return writeVehicleAction(ctx, r.exec, action, `
		INSERT INTO vehicle_actions (
		  id, drone_id, vehicle_agent_id, mission_execution_id, type, payload, state, requested_by,
		  requested_by_operator_id, target_drone_vehicle_agent_connection_id, delivery_target,
		  requires_confirmation, requested_at, authorized_at, sent_to_vehicle_agent_at, updated_at,
		  last_sent_at, lease_until, vehicle_acked_at, completed_at, failed_at, failure_reason,
		  idempotency_key, ack_correlation_id, raw_ack_code, confirmation_baseline, delivery_attempt,
		  policy_reason, result_message, telemetry_state, vehicle_agent_status
		) VALUES (
		  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		  $11, $12, $13, $14, $15, $16, $17, $18, $19, $20,
		  $21, $22, $23, $24, $25, $26, $27, $28, $29, $30,
		  $31
		)
	`)
}

func (r *VehicleActionRepository) UpdateVehicleAction(ctx context.Context, action models.VehicleAction) error {
	return writeVehicleAction(ctx, r.exec, action, `
		UPDATE vehicle_actions SET
		  drone_id = $2,
		  vehicle_agent_id = $3,
		  mission_execution_id = $4,
		  type = $5,
		  payload = $6,
		  state = $7,
		  requested_by = $8,
		  requested_by_operator_id = $9,
		  target_drone_vehicle_agent_connection_id = $10,
		  delivery_target = $11,
		  requires_confirmation = $12,
		  requested_at = $13,
		  authorized_at = $14,
		  sent_to_vehicle_agent_at = $15,
		  updated_at = $16,
		  last_sent_at = $17,
		  lease_until = $18,
		  vehicle_acked_at = $19,
		  completed_at = $20,
		  failed_at = $21,
		  failure_reason = $22,
		  idempotency_key = $23,
		  ack_correlation_id = $24,
		  raw_ack_code = $25,
		  confirmation_baseline = $26,
		  delivery_attempt = $27,
		  policy_reason = $28,
		  result_message = $29,
		  telemetry_state = $30,
		  vehicle_agent_status = $31
		WHERE id = $1
	`)
}

func (r *VehicleActionRepository) InsertVehicleActionEvent(ctx context.Context, action models.VehicleAction, eventType string, source string, message string, now time.Time) error {
	return r.InsertVehicleActionEventWithEvidence(ctx, action, eventType, source, message, nil, now)
}

func (r *VehicleActionRepository) InsertVehicleActionEventWithEvidence(ctx context.Context, action models.VehicleAction, eventType string, source string, message string, evidence map[string]any, now time.Time) error {
	eventID, err := newUUIDv7()
	if err != nil {
		return err
	}
	evidenceRaw, err := json.Marshal(emptyMapIfNil(evidence))
	if err != nil {
		return err
	}
	_, err = r.exec.ExecContext(ctx, `
		INSERT INTO vehicle_action_events (
		  id, vehicle_action_id, drone_id, vehicle_agent_id, event_type, state, message, source, raw_ack_code, evidence, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, eventID, action.ID, action.DroneID, action.VehicleAgentID, eventType, string(action.State), message, source, action.RawAckCode, evidenceRaw, now)
	return err
}

func (r *VehicleActionRepository) ListVehicleActionEvents(ctx context.Context, vehicleActionID string) ([]models.VehicleActionEvent, error) {
	if !rowExists(ctx, r.exec, `SELECT 1 FROM vehicle_actions WHERE id = $1`, vehicleActionID) {
		return nil, repository.ErrVehicleActionNotFound
	}

	rows, err := r.exec.QueryContext(ctx, `
		SELECT id, vehicle_action_id, drone_id, vehicle_agent_id, event_type, state, source,
		       message, raw_ack_code, evidence, telemetry_snapshot_id, created_at
		FROM vehicle_action_events
		WHERE vehicle_action_id = $1
		ORDER BY created_at, id
	`, vehicleActionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVehicleActionEvents(rows)
}

func (r *VehicleActionRepository) GetVehicleActionByIDForUpdate(ctx context.Context, vehicleActionID string) (models.VehicleAction, bool, error) {
	action, err := scanVehicleAction(r.exec.QueryRowContext(ctx, vehicleActionSelectSQL+`WHERE id = $1`+forUpdate(), vehicleActionID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.VehicleAction{}, false, nil
	}
	if err != nil {
		return models.VehicleAction{}, false, err
	}
	return action, true, nil
}

func (r *VehicleActionRepository) GetVehicleActionByIdempotencyKeyForUpdate(ctx context.Context, droneID string, requestedBy string, idempotencyKey string) (models.VehicleAction, bool, error) {
	action, err := scanVehicleAction(r.exec.QueryRowContext(ctx, vehicleActionSelectSQL+`
		WHERE drone_id = $1 AND requested_by = $2 AND idempotency_key = $3
	`+forUpdate(), droneID, requestedBy, idempotencyKey))
	if errors.Is(err, sql.ErrNoRows) {
		return models.VehicleAction{}, false, nil
	}
	if err != nil {
		return models.VehicleAction{}, false, err
	}
	return action, true, nil
}

func (r *VehicleActionRepository) ListVehicleActionsForUpdate(ctx context.Context, filter repository.VehicleActionFilter) ([]models.VehicleAction, error) {
	query, args := vehicleActionListQuery(filter)
	query += forUpdate()
	rows, err := r.exec.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVehicleActions(rows)
}

// GetVehicleActionByID fetches a vehicle action for read paths that only need existence as a boolean.
func (r *VehicleActionRepository) GetVehicleActionByID(ctx context.Context, vehicleActionID string) (models.VehicleAction, bool) {
	action, err := scanVehicleAction(r.exec.QueryRowContext(ctx, vehicleActionSelectSQL+`WHERE id = $1`, vehicleActionID))
	return action, err == nil
}

// ListVehicleActionsForDrone returns recent vehicle action history for a drone.
func (r *VehicleActionRepository) ListVehicleActionsForDrone(ctx context.Context, droneID string, limit int) ([]models.VehicleAction, error) {
	if !rowExists(ctx, r.exec, `SELECT 1 FROM drones WHERE id = $1`, droneID) {
		return nil, repository.ErrDroneNotFound
	}

	query, args := vehicleActionListQuery(repository.VehicleActionFilter{
		DroneID: droneID,
		Order:   repository.VehicleActionOrderRequestedDesc,
		Limit:   limit,
	})
	rows, err := r.exec.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVehicleActions(rows)
}

const vehicleActionSelectSQL = `
	SELECT id, drone_id, vehicle_agent_id, mission_execution_id, type, payload, state, requested_by,
	       requested_by_operator_id, target_drone_vehicle_agent_connection_id, delivery_target,
	       requires_confirmation, requested_at, authorized_at, sent_to_vehicle_agent_at, updated_at,
	       last_sent_at, lease_until, vehicle_acked_at, completed_at, failed_at, failure_reason,
	       idempotency_key, ack_correlation_id, raw_ack_code, confirmation_baseline, delivery_attempt,
	       policy_reason, result_message, telemetry_state, vehicle_agent_status
	FROM vehicle_actions
`

func vehicleActionListQuery(filter repository.VehicleActionFilter) (string, []any) {
	query := vehicleActionSelectSQL
	where, args := vehicleActionWhere(filter)
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY " + vehicleActionOrderSQL(filter.Order)
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return query, args
}

func vehicleActionWhere(filter repository.VehicleActionFilter) ([]string, []any) {
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
	if !filter.UpdatedBefore.IsZero() {
		where = append(where, "updated_at < "+addArg(filter.UpdatedBefore))
	}
	if !filter.VehicleAckedBefore.IsZero() {
		where = append(where, "vehicle_acked_at IS NOT NULL AND vehicle_acked_at < "+addArg(filter.VehicleAckedBefore))
	}
	if !filter.LeaseUntilAtOrBefore.IsZero() {
		where = append(where, "lease_until IS NOT NULL AND lease_until <= "+addArg(filter.LeaseUntilAtOrBefore))
	}
	return where, args
}

func vehicleActionOrderSQL(order repository.VehicleActionOrder) string {
	switch order {
	case repository.VehicleActionOrderRequestedAsc:
		return "requested_at, id"
	case repository.VehicleActionOrderRequestedDesc:
		return "requested_at DESC, id DESC"
	default:
		return "requested_at DESC, id DESC"
	}
}

func scanVehicleAction(row rowScanner) (models.VehicleAction, error) {
	var action models.VehicleAction
	var actionType, state, deliveryTarget, telemetryState, agentStatus string
	var missionExecutionID, requestedByOperatorID, targetConnectionID sql.NullString
	var authorizedAt, sentToVehicleAgentAt, lastSentAt, leaseUntil, vehicleAckedAt, completedAt, failedAt sql.NullTime
	var payloadRaw, baselineRaw []byte
	err := row.Scan(
		&action.ID,
		&action.DroneID,
		&action.VehicleAgentID,
		&missionExecutionID,
		&actionType,
		&payloadRaw,
		&state,
		&action.RequestedBy,
		&requestedByOperatorID,
		&targetConnectionID,
		&deliveryTarget,
		&action.RequiresConfirmation,
		&action.RequestedAt,
		&authorizedAt,
		&sentToVehicleAgentAt,
		&action.UpdatedAt,
		&lastSentAt,
		&leaseUntil,
		&vehicleAckedAt,
		&completedAt,
		&failedAt,
		&action.FailureReason,
		&action.IdempotencyKey,
		&action.AckCorrelationID,
		&action.RawAckCode,
		&baselineRaw,
		&action.DeliveryAttempt,
		&action.PolicyReason,
		&action.ResultMessage,
		&telemetryState,
		&agentStatus,
	)
	if err != nil {
		return models.VehicleAction{}, err
	}
	action.MissionExecutionID = missionExecutionID.String
	action.RequestedByOperatorID = requestedByOperatorID.String
	action.TargetDroneVehicleAgentConnectionID = targetConnectionID.String
	action.Type = models.VehicleActionType(actionType)
	action.State = models.VehicleActionState(state)
	action.DeliveryTarget = models.VehicleActionDeliveryTarget(deliveryTarget)
	action.AuthorizedAt = timeFromNull(authorizedAt)
	action.SentToVehicleAgentAt = timeFromNull(sentToVehicleAgentAt)
	action.LastSentAt = timeFromNull(lastSentAt)
	action.LeaseUntil = timeFromNull(leaseUntil)
	action.VehicleAckedAt = timeFromNull(vehicleAckedAt)
	action.CompletedAt = timeFromNull(completedAt)
	action.FailedAt = timeFromNull(failedAt)
	action.TelemetryState = models.TelemetryState(telemetryState)
	action.VehicleAgentStatus = models.VehicleAgentStatus(agentStatus)
	if len(payloadRaw) > 0 {
		_ = json.Unmarshal(payloadRaw, &action.Payload)
	}
	if len(baselineRaw) > 0 {
		_ = json.Unmarshal(baselineRaw, &action.ConfirmationBaseline)
	}
	return action, nil
}

func scanVehicleActions(rows *sql.Rows) ([]models.VehicleAction, error) {
	var actions []models.VehicleAction
	for rows.Next() {
		action, err := scanVehicleAction(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

func scanVehicleActionEvents(rows *sql.Rows) ([]models.VehicleActionEvent, error) {
	var events []models.VehicleActionEvent
	for rows.Next() {
		event, err := scanVehicleActionEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func scanVehicleActionEvent(row rowScanner) (models.VehicleActionEvent, error) {
	var event models.VehicleActionEvent
	var eventType, state string
	var telemetrySnapshotID sql.NullString
	var evidenceRaw []byte
	err := row.Scan(
		&event.ID,
		&event.VehicleActionID,
		&event.DroneID,
		&event.VehicleAgentID,
		&eventType,
		&state,
		&event.Source,
		&event.Message,
		&event.RawAckCode,
		&evidenceRaw,
		&telemetrySnapshotID,
		&event.CreatedAt,
	)
	if err != nil {
		return models.VehicleActionEvent{}, err
	}
	event.EventType = models.VehicleActionEventType(eventType)
	event.State = models.VehicleActionState(state)
	event.TelemetrySnapshotID = telemetrySnapshotID.String
	event.Evidence = map[string]any{}
	if len(evidenceRaw) > 0 {
		_ = json.Unmarshal(evidenceRaw, &event.Evidence)
	}
	return event, nil
}

func emptyMapIfNil(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func writeVehicleAction(ctx context.Context, q DBExecutor, action models.VehicleAction, query string) error {
	payload := []byte("{}")
	if action.Payload != nil {
		var err error
		payload, err = json.Marshal(action.Payload)
		if err != nil {
			return err
		}
	}
	baseline, err := json.Marshal(action.ConfirmationBaseline)
	if err != nil {
		return err
	}
	if action.ConfirmationBaseline.ReceivedAt.IsZero() && action.ConfirmationBaseline.ObservedAt.IsZero() {
		baseline = nil
	}
	deliveryTarget := action.DeliveryTarget
	if deliveryTarget == "" {
		deliveryTarget = models.VehicleActionDeliveryTargetVehicleAgent
	}
	_, err = q.ExecContext(ctx, query,
		action.ID,
		action.DroneID,
		action.VehicleAgentID,
		nullString(action.MissionExecutionID),
		string(action.Type),
		payload,
		string(action.State),
		action.RequestedBy,
		nullString(action.RequestedByOperatorID),
		nullString(action.TargetDroneVehicleAgentConnectionID),
		string(deliveryTarget),
		action.RequiresConfirmation,
		action.RequestedAt,
		nullTime(action.AuthorizedAt),
		nullTime(action.SentToVehicleAgentAt),
		action.UpdatedAt,
		nullTime(action.LastSentAt),
		nullTime(action.LeaseUntil),
		nullTime(action.VehicleAckedAt),
		nullTime(action.CompletedAt),
		nullTime(action.FailedAt),
		action.FailureReason,
		action.IdempotencyKey,
		action.AckCorrelationID,
		action.RawAckCode,
		baseline,
		action.DeliveryAttempt,
		action.PolicyReason,
		action.ResultMessage,
		string(action.TelemetryState),
		string(action.VehicleAgentStatus),
	)
	return err
}
