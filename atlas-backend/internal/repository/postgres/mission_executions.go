package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

type MissionExecutionRepository struct {
	exec DBExecutor
}

func NewMissionExecutionRepository(db *sql.DB) *MissionExecutionRepository {
	return newMissionExecutionRepository(db)
}

func newMissionExecutionRepository(exec DBExecutor) *MissionExecutionRepository {
	return &MissionExecutionRepository{exec: exec}
}

func (r *MissionExecutionRepository) GenerateMissionExecutionID(ctx context.Context) (string, error) {
	return newUUIDv7()
}

func (r *MissionExecutionRepository) InsertMissionExecution(ctx context.Context, execution models.MissionExecution) error {
	return writeMissionExecution(ctx, r.exec, execution, `
		INSERT INTO mission_executions (
		  id, mission_id, drone_id, vehicle_agent_id, requested_by, upload_requested_by, start_requested_by,
		  state, created_at, updated_at, last_sent_at, lease_until, upload_requested_at, uploaded_at,
		  start_requested_at, started_at, completed_at, hold_at, failed_at, current_mission_item,
		  total_mission_items, progress_updated_at, delivery_attempt, result_message
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
	`)
}

func (r *MissionExecutionRepository) UpdateMissionExecution(ctx context.Context, execution models.MissionExecution) error {
	return writeMissionExecution(ctx, r.exec, execution, `
		UPDATE mission_executions SET
		  mission_id = $2, drone_id = $3, vehicle_agent_id = $4, requested_by = $5,
		  upload_requested_by = $6, start_requested_by = $7, state = $8,
		  created_at = $9, updated_at = $10, last_sent_at = $11, lease_until = $12,
		  upload_requested_at = $13, uploaded_at = $14, start_requested_at = $15,
		  started_at = $16, completed_at = $17, hold_at = $18, failed_at = $19,
		  current_mission_item = $20, total_mission_items = $21, progress_updated_at = $22,
		  delivery_attempt = $23, result_message = $24
		WHERE id = $1
	`)
}

func (r *MissionExecutionRepository) InsertMissionExecutionEvent(ctx context.Context, execution models.MissionExecution, eventType string, source string, message string, now time.Time) error {
	eventID, err := newUUIDv7()
	if err != nil {
		return err
	}
	_, err = r.exec.ExecContext(ctx, `
		INSERT INTO mission_execution_events (
		  id, execution_id, mission_id, mission_version_id, drone_id, vehicle_agent_id, event_type, state, message,
		  current_mission_item, total_mission_items, source, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, eventID, execution.ID, execution.MissionID, nullString(execution.MissionVersionID), execution.DroneID, execution.VehicleAgentID, eventType,
		string(execution.State), message, execution.CurrentMissionItem, execution.TotalMissionItems, source, now)
	return err
}

func (r *MissionExecutionRepository) LockMissionExecution(ctx context.Context, executionID string) (models.MissionExecution, bool, error) {
	execution, err := scanMissionExecution(r.exec.QueryRowContext(ctx, missionExecutionSelectSQL+`WHERE id = $1`+forUpdate(), executionID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.MissionExecution{}, false, nil
	}
	if err != nil {
		return models.MissionExecution{}, false, err
	}
	return execution, true, nil
}

func (r *MissionExecutionRepository) ListMissionExecutionsForUpdate(ctx context.Context, filter repository.MissionExecutionFilter) ([]models.MissionExecution, error) {
	query, args := missionExecutionListQuery(filter)
	query += forUpdate()
	rows, err := r.exec.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMissionExecutions(rows)
}

func (r *MissionExecutionRepository) ListMissionExecutions(ctx context.Context, missionID string) ([]models.MissionExecution, error) {
	if !rowExists(ctx, r.exec, `SELECT 1 FROM missions WHERE id = $1`, missionID) {
		return nil, repository.ErrMissionNotFound
	}
	rows, err := r.exec.QueryContext(ctx, missionExecutionSelectSQL+`
		WHERE mission_id = $1
		ORDER BY created_at DESC, id DESC
	`, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMissionExecutions(rows)
}

func (r *MissionExecutionRepository) ListMissionExecutionEvents(ctx context.Context, missionID string) ([]models.MissionExecutionEvent, error) {
	if !rowExists(ctx, r.exec, `SELECT 1 FROM missions WHERE id = $1`, missionID) {
		return nil, repository.ErrMissionNotFound
	}
	rows, err := r.exec.QueryContext(ctx, missionExecutionEventSelectSQL+`
		WHERE mission_id = $1
		ORDER BY created_at, id
	`, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMissionExecutionEvents(rows)
}

const missionExecutionSelectSQL = `
	SELECT id, mission_id, drone_id, vehicle_agent_id, requested_by, upload_requested_by,
	       start_requested_by, state, created_at, updated_at, last_sent_at, lease_until,
	       upload_requested_at, uploaded_at, start_requested_at, started_at, completed_at,
	       hold_at, failed_at, current_mission_item, total_mission_items, progress_updated_at,
	       delivery_attempt, result_message
	FROM mission_executions
`

const missionExecutionEventSelectSQL = `
	SELECT id, execution_id, mission_id, drone_id, vehicle_agent_id, event_type, state, message,
	       current_mission_item, total_mission_items, source, created_at
	FROM mission_execution_events
`

func missionExecutionListQuery(filter repository.MissionExecutionFilter) (string, []any) {
	query := missionExecutionSelectSQL
	where, args := missionExecutionWhere(filter)
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY " + missionExecutionOrderSQL(filter.Order)
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return query, args
}

func missionExecutionWhere(filter repository.MissionExecutionFilter) ([]string, []any) {
	var where []string
	var args []any
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}

	if filter.ID != "" {
		where = append(where, "id = "+addArg(filter.ID))
	}
	if filter.MissionID != "" {
		where = append(where, "mission_id = "+addArg(filter.MissionID))
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
	if len(filter.States) > 0 {
		placeholders := make([]string, 0, len(filter.States))
		for _, state := range filter.States {
			placeholders = append(placeholders, addArg(string(state)))
		}
		where = append(where, "state IN ("+strings.Join(placeholders, ", ")+")")
	}
	return where, args
}

func missionExecutionOrderSQL(order repository.MissionExecutionOrder) string {
	switch order {
	case repository.MissionExecutionOrderCreatedDesc:
		return "created_at DESC, id DESC"
	case repository.MissionExecutionOrderUpdatedAsc:
		return "updated_at, id"
	case repository.MissionExecutionOrderUpdatedDesc:
		return "updated_at DESC, id DESC"
	default:
		return "updated_at DESC, id DESC"
	}
}

func writeMissionExecution(ctx context.Context, q DBExecutor, execution models.MissionExecution, query string) error {
	_, err := q.ExecContext(ctx, query,
		execution.ID,
		execution.MissionID,
		execution.DroneID,
		execution.VehicleAgentID,
		execution.RequestedBy,
		execution.UploadRequestedBy,
		execution.StartRequestedBy,
		string(execution.State),
		execution.CreatedAt,
		execution.UpdatedAt,
		nullTime(execution.LastSentAt),
		nullTime(execution.LeaseUntil),
		nullTime(execution.UploadRequestedAt),
		nullTime(execution.UploadedAt),
		nullTime(execution.StartRequestedAt),
		nullTime(execution.StartedAt),
		nullTime(execution.CompletedAt),
		nullTime(execution.HoldAt),
		nullTime(execution.FailedAt),
		execution.CurrentMissionItem,
		execution.TotalMissionItems,
		nullTime(execution.ProgressUpdatedAt),
		execution.DeliveryAttempt,
		execution.ResultMessage,
	)
	return err
}

func scanMissionExecution(row rowScanner) (models.MissionExecution, error) {
	var execution models.MissionExecution
	var state string
	var lastSentAt, leaseUntil, uploadRequestedAt, uploadedAt, startRequestedAt, startedAt, completedAt, holdAt, failedAt, progressUpdatedAt sql.NullTime
	err := row.Scan(
		&execution.ID,
		&execution.MissionID,
		&execution.DroneID,
		&execution.VehicleAgentID,
		&execution.RequestedBy,
		&execution.UploadRequestedBy,
		&execution.StartRequestedBy,
		&state,
		&execution.CreatedAt,
		&execution.UpdatedAt,
		&lastSentAt,
		&leaseUntil,
		&uploadRequestedAt,
		&uploadedAt,
		&startRequestedAt,
		&startedAt,
		&completedAt,
		&holdAt,
		&failedAt,
		&execution.CurrentMissionItem,
		&execution.TotalMissionItems,
		&progressUpdatedAt,
		&execution.DeliveryAttempt,
		&execution.ResultMessage,
	)
	if err != nil {
		return models.MissionExecution{}, err
	}
	execution.State = models.MissionExecutionState(state)
	execution.LastSentAt = timeFromNull(lastSentAt)
	execution.LeaseUntil = timeFromNull(leaseUntil)
	execution.UploadRequestedAt = timeFromNull(uploadRequestedAt)
	execution.UploadedAt = timeFromNull(uploadedAt)
	execution.StartRequestedAt = timeFromNull(startRequestedAt)
	execution.StartedAt = timeFromNull(startedAt)
	execution.CompletedAt = timeFromNull(completedAt)
	execution.HoldAt = timeFromNull(holdAt)
	execution.FailedAt = timeFromNull(failedAt)
	execution.ProgressUpdatedAt = timeFromNull(progressUpdatedAt)
	return execution, nil
}

func scanMissionExecutions(rows *sql.Rows) ([]models.MissionExecution, error) {
	var executions []models.MissionExecution
	for rows.Next() {
		execution, err := scanMissionExecution(rows)
		if err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, rows.Err()
}

func scanMissionExecutionEvents(rows *sql.Rows) ([]models.MissionExecutionEvent, error) {
	var events []models.MissionExecutionEvent
	for rows.Next() {
		var event models.MissionExecutionEvent
		var state string
		if err := rows.Scan(
			&event.ID,
			&event.ExecutionID,
			&event.MissionID,
			&event.DroneID,
			&event.VehicleAgentID,
			&event.Type,
			&state,
			&event.Message,
			&event.CurrentMissionItem,
			&event.TotalMissionItems,
			&event.Source,
			&event.CreatedAt,
		); err != nil {
			return nil, err
		}
		event.State = models.MissionExecutionState(state)
		events = append(events, event)
	}
	return events, rows.Err()
}

func latestMissionExecutionForDrone(ctx context.Context, q DBExecutor, droneID string) models.MissionExecution {
	rows, err := q.QueryContext(ctx, missionExecutionSelectSQL+`
		WHERE drone_id = $1
	`, droneID)
	if err != nil {
		return models.MissionExecution{}
	}
	defer rows.Close()
	executions, err := scanMissionExecutions(rows)
	if err != nil {
		return models.MissionExecution{}
	}
	var latest models.MissionExecution
	for _, execution := range executions {
		if latest.ID == "" ||
			domain.MissionExecutionSnapshotRank(execution.State) > domain.MissionExecutionSnapshotRank(latest.State) ||
			(domain.MissionExecutionSnapshotRank(execution.State) == domain.MissionExecutionSnapshotRank(latest.State) && execution.UpdatedAt.After(latest.UpdatedAt)) ||
			(domain.MissionExecutionSnapshotRank(execution.State) == domain.MissionExecutionSnapshotRank(latest.State) && execution.UpdatedAt.Equal(latest.UpdatedAt) && execution.ID > latest.ID) {
			latest = execution
		}
	}
	return latest
}
