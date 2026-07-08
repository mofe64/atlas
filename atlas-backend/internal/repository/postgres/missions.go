package postgres

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

type MissionRepository struct {
	exec DBExecutor
}

func NewMissionRepository(db *sql.DB) *MissionRepository {
	return newMissionRepository(db)
}

func newMissionRepository(exec DBExecutor) *MissionRepository {
	return &MissionRepository{exec: exec}
}

func (r *MissionRepository) GenerateMissionID(ctx context.Context) (string, error) {
	return newUUIDv7()
}

func (r *MissionRepository) InsertMission(ctx context.Context, mission models.Mission) error {
	rawErrors, err := json.Marshal(mission.ValidationErrors)
	if err != nil {
		return err
	}
	_, err = r.exec.ExecContext(ctx, `
		INSERT INTO missions (
		  id, drone_id, name, created_by, created_at, updated_at,
		  completion_action, validation_status, validation_errors
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, mission.ID, mission.DroneID, mission.Name, mission.CreatedBy, mission.CreatedAt, mission.UpdatedAt,
		string(mission.CompletionAction), string(mission.ValidationStatus), rawErrors)
	return err
}

func (r *MissionRepository) InsertMissionWaypoint(ctx context.Context, missionID string, waypoint models.MissionWaypoint) error {
	_, err := r.exec.ExecContext(ctx, `
		INSERT INTO mission_waypoints (
		  mission_id, sequence, latitude, longitude, relative_altitude_m, speed_mps, loiter_time_s
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, missionID, waypoint.Sequence, waypoint.Latitude, waypoint.Longitude, waypoint.RelativeAltitudeM,
		floatPtrValue(waypoint.SpeedMPS), floatPtrValue(waypoint.LoiterTimeS))
	return err
}

func (r *MissionRepository) ListMissionsForDrone(ctx context.Context, droneID string) ([]models.Mission, error) {
	if !rowExists(ctx, r.exec, `SELECT 1 FROM drones WHERE id = $1`, droneID) {
		return nil, repository.ErrDroneNotFound
	}
	rows, err := r.exec.QueryContext(ctx, missionSelectSQL+`
		WHERE drone_id = $1
		ORDER BY created_at DESC, id DESC
	`, droneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	missions, err := scanMissions(rows)
	if err != nil {
		return nil, err
	}
	for i := range missions {
		missions[i].Waypoints, err = r.listMissionWaypoints(ctx, missions[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return missions, nil
}

func (r *MissionRepository) GetMissionByID(ctx context.Context, missionID string) (models.Mission, bool) {
	mission, err := scanMission(r.exec.QueryRowContext(ctx, missionSelectSQL+`WHERE id = $1`, missionID))
	if err != nil {
		return models.Mission{}, false
	}
	waypoints, err := r.listMissionWaypoints(ctx, missionID)
	if err != nil {
		return models.Mission{}, false
	}
	mission.Waypoints = waypoints
	return mission, true
}

const missionSelectSQL = `
	SELECT id, drone_id, name, created_by, created_at, updated_at,
	       completion_action, validation_status, validation_errors
	FROM missions
`

func scanMission(row rowScanner) (models.Mission, error) {
	var mission models.Mission
	var completionAction, validationStatus string
	var rawErrors []byte
	err := row.Scan(
		&mission.ID,
		&mission.DroneID,
		&mission.Name,
		&mission.CreatedBy,
		&mission.CreatedAt,
		&mission.UpdatedAt,
		&completionAction,
		&validationStatus,
		&rawErrors,
	)
	if err != nil {
		return models.Mission{}, err
	}
	mission.CompletionAction = models.MissionCompletionAction(completionAction)
	mission.ValidationStatus = models.MissionValidationStatus(validationStatus)
	if len(rawErrors) > 0 {
		_ = json.Unmarshal(rawErrors, &mission.ValidationErrors)
	}
	return mission, nil
}

func scanMissions(rows *sql.Rows) ([]models.Mission, error) {
	var missions []models.Mission
	for rows.Next() {
		mission, err := scanMission(rows)
		if err != nil {
			return nil, err
		}
		missions = append(missions, mission)
	}
	return missions, rows.Err()
}

func (r *MissionRepository) listMissionWaypoints(ctx context.Context, missionID string) ([]models.MissionWaypoint, error) {
	rows, err := r.exec.QueryContext(ctx, `
		SELECT sequence, latitude, longitude, relative_altitude_m, speed_mps, loiter_time_s
		FROM mission_waypoints
		WHERE mission_id = $1
		ORDER BY sequence
	`, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var waypoints []models.MissionWaypoint
	for rows.Next() {
		var waypoint models.MissionWaypoint
		var speed, loiter sql.NullFloat64
		if err := rows.Scan(
			&waypoint.Sequence,
			&waypoint.Latitude,
			&waypoint.Longitude,
			&waypoint.RelativeAltitudeM,
			&speed,
			&loiter,
		); err != nil {
			return nil, err
		}
		if speed.Valid {
			waypoint.SpeedMPS = &speed.Float64
		}
		if loiter.Valid {
			waypoint.LoiterTimeS = &loiter.Float64
		}
		waypoints = append(waypoints, waypoint)
	}
	return waypoints, rows.Err()
}
