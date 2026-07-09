package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

type TelemetryFeedRepository struct {
	exec DBExecutor
}

func NewTelemetryFeedRepository(db *sql.DB) *TelemetryFeedRepository {
	return newTelemetryFeedRepository(db)
}

func newTelemetryFeedRepository(exec DBExecutor) *TelemetryFeedRepository {
	return &TelemetryFeedRepository{exec: exec}
}

func (r *TelemetryFeedRepository) GenerateTelemetryFeedID(ctx context.Context) (string, error) {
	return newUUIDv7()
}

func (r *TelemetryFeedRepository) InsertTelemetryFeed(ctx context.Context, feed models.TelemetryFeed) error {
	return writeTelemetryFeed(ctx, r.exec, feed, `
		INSERT INTO telemetry_feeds (
		  id, drone_id, source_type, source_id, communication_link_id, status, priority,
		  freshness, last_telemetry_at, last_sequence, message_rate_hz, fields_available,
		  started_at, ended_at, last_error
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`)
}

func (r *TelemetryFeedRepository) UpdateTelemetryFeed(ctx context.Context, feed models.TelemetryFeed) error {
	return writeTelemetryFeed(ctx, r.exec, feed, `
		UPDATE telemetry_feeds SET
		  drone_id = $2,
		  source_type = $3,
		  source_id = $4,
		  communication_link_id = $5,
		  status = $6,
		  priority = $7,
		  freshness = $8,
		  last_telemetry_at = $9,
		  last_sequence = $10,
		  message_rate_hz = $11,
		  fields_available = $12,
		  started_at = $13,
		  ended_at = $14,
		  last_error = $15
		WHERE id = $1
	`)
}

func (r *TelemetryFeedRepository) GetTelemetryFeedByID(ctx context.Context, feedID string) (models.TelemetryFeed, bool, error) {
	feed, err := scanTelemetryFeed(r.exec.QueryRowContext(ctx, telemetryFeedSelectSQL+`
		WHERE id = $1
	`, feedID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.TelemetryFeed{}, false, nil
	}
	if err != nil {
		return models.TelemetryFeed{}, false, err
	}
	return feed, true, nil
}

func (r *TelemetryFeedRepository) GetTelemetryFeedBySource(ctx context.Context, droneID string, sourceType models.TelemetrySourceType, sourceID string, communicationLinkID string) (models.TelemetryFeed, bool, error) {
	feed, err := scanTelemetryFeed(r.exec.QueryRowContext(ctx, telemetryFeedSelectSQL+`
		WHERE drone_id = $1
		  AND source_type = $2
		  AND source_id = $3
		  AND COALESCE(communication_link_id, '') = $4
		  AND ended_at IS NULL
		ORDER BY started_at DESC, id DESC
		LIMIT 1
	`, droneID, string(sourceType), sourceID, communicationLinkID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.TelemetryFeed{}, false, nil
	}
	if err != nil {
		return models.TelemetryFeed{}, false, err
	}
	return feed, true, nil
}

func (r *TelemetryFeedRepository) ListTelemetryFeedsForDrone(ctx context.Context, droneID string) ([]models.TelemetryFeed, error) {
	if !rowExists(ctx, r.exec, `SELECT 1 FROM drones WHERE id = $1`, droneID) {
		return nil, repository.ErrDroneNotFound
	}

	rows, err := r.exec.QueryContext(ctx, telemetryFeedSelectSQL+`
		WHERE drone_id = $1
		ORDER BY ended_at IS NULL DESC, priority ASC, last_telemetry_at DESC NULLS LAST, started_at DESC, id DESC
	`, droneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	feeds := []models.TelemetryFeed{}
	for rows.Next() {
		feed, err := scanTelemetryFeed(rows)
		if err != nil {
			return nil, err
		}
		feeds = append(feeds, feed)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return feeds, nil
}

const telemetryFeedSelectSQL = `
	SELECT id, drone_id, source_type, source_id, communication_link_id, status, priority,
	       freshness, last_telemetry_at, last_sequence, message_rate_hz, fields_available,
	       started_at, ended_at, last_error
	FROM telemetry_feeds
`

func writeTelemetryFeed(ctx context.Context, q DBExecutor, feed models.TelemetryFeed, query string) error {
	fieldsAvailable, err := json.Marshal(feed.FieldsAvailable)
	if err != nil {
		return err
	}

	_, err = q.ExecContext(ctx, query,
		feed.ID,
		feed.DroneID,
		string(feed.SourceType),
		feed.SourceID,
		nullString(feed.CommunicationLinkID),
		string(feed.Status),
		feed.Priority,
		string(feed.Freshness),
		nullTime(feed.LastTelemetryAt),
		nullInt64(feed.LastSequence),
		nullFloat64(feed.MessageRateHz),
		fieldsAvailable,
		feed.StartedAt,
		nullTime(feed.EndedAt),
		feed.LastError,
	)
	return err
}

func scanTelemetryFeed(row rowScanner) (models.TelemetryFeed, error) {
	var feed models.TelemetryFeed
	var sourceType, status, freshness string
	var communicationLinkID sql.NullString
	var lastTelemetryAt, endedAt sql.NullTime
	var lastSequence sql.NullInt64
	var messageRateHz sql.NullFloat64
	var rawFields []byte
	err := row.Scan(
		&feed.ID,
		&feed.DroneID,
		&sourceType,
		&feed.SourceID,
		&communicationLinkID,
		&status,
		&feed.Priority,
		&freshness,
		&lastTelemetryAt,
		&lastSequence,
		&messageRateHz,
		&rawFields,
		&feed.StartedAt,
		&endedAt,
		&feed.LastError,
	)
	if err != nil {
		return models.TelemetryFeed{}, err
	}

	feed.SourceType = models.TelemetrySourceType(sourceType)
	feed.CommunicationLinkID = communicationLinkID.String
	feed.Status = models.TelemetryFeedStatus(status)
	feed.Freshness = models.TelemetryState(freshness)
	feed.LastTelemetryAt = timeFromNull(lastTelemetryAt)
	feed.LastSequence = lastSequence.Int64
	feed.MessageRateHz = messageRateHz.Float64
	feed.EndedAt = timeFromNull(endedAt)
	if len(rawFields) > 0 {
		_ = json.Unmarshal(rawFields, &feed.FieldsAvailable)
	}
	return feed, nil
}
