package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

type PerceptionRepository struct {
	exec DBExecutor
}

type perceptionDetectionRecord struct {
	Class      string     `json:"class"`
	Confidence float64    `json:"confidence"`
	BBox       [4]float64 `json:"bbox"`
}

func NewPerceptionRepository(db *sql.DB) *PerceptionRepository {
	return newPerceptionRepository(db)
}

func newPerceptionRepository(exec DBExecutor) *PerceptionRepository {
	return &PerceptionRepository{exec: exec}
}

func (r *PerceptionRepository) GeneratePerceptionEventID(ctx context.Context) (string, error) {
	return newUUIDv7()
}

func (r *PerceptionRepository) InsertPerceptionEvent(ctx context.Context, event models.PerceptionEvent) error {
	rawDetections, err := marshalPerceptionDetections(event.Detections)
	if err != nil {
		return err
	}

	_, err = r.exec.ExecContext(ctx, `
		INSERT INTO perception_events (
		  id, drone_id, camera_device_id, video_source_id, observed_at, frame_id,
		  model_name, model_version, inference_latency_ms, detections, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		event.ID,
		event.DroneID,
		event.CameraDeviceID,
		event.VideoSourceID,
		event.ObservedAt,
		event.FrameID,
		event.ModelName,
		event.ModelVersion,
		event.InferenceLatencyMS,
		rawDetections,
		event.CreatedAt,
	)
	return err
}

func (r *PerceptionRepository) ListPerceptionEventsForDrone(ctx context.Context, droneID string, limit int) ([]models.PerceptionEvent, error) {
	rows, err := r.exec.QueryContext(ctx, `
		SELECT id, drone_id, camera_device_id, video_source_id, observed_at, frame_id,
		       model_name, model_version, inference_latency_ms, detections, created_at
		FROM perception_events
		WHERE drone_id = $1
		ORDER BY observed_at DESC, id DESC
		LIMIT $2
	`, droneID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []models.PerceptionEvent
	for rows.Next() {
		event, err := scanPerceptionEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (r *PerceptionRepository) LatestPerceptionEventForDrone(ctx context.Context, droneID string) (models.PerceptionEvent, bool, error) {
	event, err := scanPerceptionEvent(r.exec.QueryRowContext(ctx, `
		SELECT id, drone_id, camera_device_id, video_source_id, observed_at, frame_id,
		       model_name, model_version, inference_latency_ms, detections, created_at
		FROM perception_events
		WHERE drone_id = $1
		ORDER BY observed_at DESC, id DESC
		LIMIT 1
	`, droneID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.PerceptionEvent{}, false, nil
	}
	if err != nil {
		return models.PerceptionEvent{}, false, err
	}
	return event, true, nil
}

func scanPerceptionEvent(row rowScanner) (models.PerceptionEvent, error) {
	var event models.PerceptionEvent
	var rawDetections []byte
	err := row.Scan(
		&event.ID,
		&event.DroneID,
		&event.CameraDeviceID,
		&event.VideoSourceID,
		&event.ObservedAt,
		&event.FrameID,
		&event.ModelName,
		&event.ModelVersion,
		&event.InferenceLatencyMS,
		&rawDetections,
		&event.CreatedAt,
	)
	if err != nil {
		return models.PerceptionEvent{}, err
	}
	event.Detections = unmarshalPerceptionDetections(rawDetections)
	return event, nil
}

func marshalPerceptionDetections(detections []models.PerceptionDetection) ([]byte, error) {
	records := make([]perceptionDetectionRecord, 0, len(detections))
	for _, detection := range detections {
		records = append(records, perceptionDetectionRecord{
			Class:      detection.ClassName,
			Confidence: detection.Confidence,
			BBox:       detection.BBox,
		})
	}
	return json.Marshal(records)
}

func unmarshalPerceptionDetections(raw []byte) []models.PerceptionDetection {
	if len(raw) == 0 {
		return nil
	}

	var records []perceptionDetectionRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil
	}

	detections := make([]models.PerceptionDetection, 0, len(records))
	for _, record := range records {
		detections = append(detections, models.PerceptionDetection{
			ClassName:  record.Class,
			Confidence: record.Confidence,
			BBox:       record.BBox,
		})
	}
	return detections
}
