package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

type TelemetrySampleRepository struct {
	exec DBExecutor
}

func NewTelemetrySampleRepository(db *sql.DB) *TelemetrySampleRepository {
	return newTelemetrySampleRepository(db)
}

func newTelemetrySampleRepository(exec DBExecutor) *TelemetrySampleRepository {
	return &TelemetrySampleRepository{exec: exec}
}

func (r *TelemetrySampleRepository) GenerateTelemetrySampleID(ctx context.Context) (string, error) {
	return newUUIDv7()
}

func (r *TelemetrySampleRepository) InsertTelemetrySample(ctx context.Context, sample models.TelemetrySample) error {
	rawSnapshot, err := json.Marshal(sample.Snapshot)
	if err != nil {
		return err
	}

	_, err = r.exec.ExecContext(ctx, `
		INSERT INTO telemetry_samples (
		  id, drone_id, mission_execution_id, telemetry_feed_id, sampled_at, snapshot
		) VALUES ($1, $2, $3, $4, $5, $6)
	`,
		sample.ID,
		sample.DroneID,
		nullString(sample.MissionExecutionID),
		nullString(sample.TelemetryFeedID),
		sample.Timestamp,
		rawSnapshot,
	)
	return err
}

func (r *TelemetrySampleRepository) LatestTelemetrySampleForFeed(ctx context.Context, feedID string) (models.TelemetrySample, bool, error) {
	sample, err := scanTelemetrySample(r.exec.QueryRowContext(ctx, `
		SELECT id, drone_id, mission_execution_id, telemetry_feed_id, sampled_at, snapshot
		FROM telemetry_samples
		WHERE telemetry_feed_id = $1
		ORDER BY sampled_at DESC, id DESC
		LIMIT 1
	`, feedID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.TelemetrySample{}, false, nil
	}
	if err != nil {
		return models.TelemetrySample{}, false, err
	}
	return sample, true, nil
}

func scanTelemetrySample(row rowScanner) (models.TelemetrySample, error) {
	var sample models.TelemetrySample
	var missionExecutionID, telemetryFeedID sql.NullString
	var rawSnapshot []byte
	err := row.Scan(
		&sample.ID,
		&sample.DroneID,
		&missionExecutionID,
		&telemetryFeedID,
		&sample.Timestamp,
		&rawSnapshot,
	)
	if err != nil {
		return models.TelemetrySample{}, err
	}

	sample.MissionExecutionID = missionExecutionID.String
	sample.TelemetryFeedID = telemetryFeedID.String
	if len(rawSnapshot) > 0 {
		_ = json.Unmarshal(rawSnapshot, &sample.Snapshot)
	}
	return sample, nil
}
