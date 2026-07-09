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

func (r *MissionRepository) GenerateMissionVersionID(ctx context.Context) (string, error) {
	return newUUIDv7()
}

func (r *MissionRepository) InsertMission(ctx context.Context, mission models.Mission) error {
	rawErrors, err := json.Marshal(mission.ValidationErrors)
	if err != nil {
		return err
	}
	_, err = r.exec.ExecContext(ctx, `
		INSERT INTO missions (
		  id, drone_id, name, description, created_by, created_by_operator_id, current_version_id, created_at, updated_at,
		  completion_action, validation_status, validation_errors
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, mission.ID, mission.DroneID, mission.Name, mission.Description, mission.CreatedBy, nullString(mission.CreatedByOperatorID),
		nullString(mission.CurrentVersionID), mission.CreatedAt, mission.UpdatedAt,
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

func (r *MissionRepository) InsertMissionVersion(ctx context.Context, version models.MissionVersion) error {
	rawWaypoints, err := json.Marshal(version.Waypoints)
	if err != nil {
		return err
	}
	rawAltitudePolicy, err := json.Marshal(version.AltitudePolicy)
	if err != nil {
		return err
	}
	rawSpeedPolicy, err := json.Marshal(version.SpeedPolicy)
	if err != nil {
		return err
	}
	rawGeofencePolicy, err := json.Marshal(version.GeofencePolicy)
	if err != nil {
		return err
	}
	rawRTLPolicy, err := json.Marshal(version.RTLPolicy)
	if err != nil {
		return err
	}
	rawErrors, err := json.Marshal(version.ValidationErrors)
	if err != nil {
		return err
	}

	_, err = r.exec.ExecContext(ctx, `
		INSERT INTO mission_versions (
		  id, mission_id, version_number, waypoints, altitude_policy, speed_policy, geofence_policy,
		  rtl_policy, validation_status, validation_errors, created_by_operator_id, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, version.ID, version.MissionID, version.VersionNumber, rawWaypoints, rawAltitudePolicy, rawSpeedPolicy,
		rawGeofencePolicy, rawRTLPolicy, string(version.ValidationStatus), rawErrors, nullString(version.CreatedByOperatorID),
		version.CreatedAt)
	return err
}

func (r *MissionRepository) InsertMissionVersionWaypoint(ctx context.Context, missionVersionID string, waypoint models.MissionWaypoint) error {
	_, err := r.exec.ExecContext(ctx, `
		INSERT INTO mission_version_waypoints (
		  mission_version_id, sequence, latitude, longitude, relative_altitude_m, speed_mps, loiter_time_s
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, missionVersionID, waypoint.Sequence, waypoint.Latitude, waypoint.Longitude, waypoint.RelativeAltitudeM,
		floatPtrValue(waypoint.SpeedMPS), floatPtrValue(waypoint.LoiterTimeS))
	return err
}

func (r *MissionRepository) SetMissionCurrentVersion(ctx context.Context, missionID string, missionVersionID string, updatedAt time.Time) error {
	_, err := r.exec.ExecContext(ctx, `
		UPDATE missions
		SET current_version_id = $2, updated_at = $3
		WHERE id = $1
	`, missionID, missionVersionID, updatedAt)
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
		missions[i], err = r.hydrateMissionDefinition(ctx, missions[i])
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
	mission, err = r.hydrateMissionDefinition(ctx, mission)
	if err != nil {
		return models.Mission{}, false
	}
	return mission, true
}

func (r *MissionRepository) GetMissionVersionByID(ctx context.Context, missionVersionID string) (models.MissionVersion, bool, error) {
	version, err := scanMissionVersion(r.exec.QueryRowContext(ctx, missionVersionSelectSQL+`
		WHERE id = $1
	`, missionVersionID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.MissionVersion{}, false, nil
	}
	if err != nil {
		return models.MissionVersion{}, false, err
	}

	waypoints, err := r.listMissionVersionWaypoints(ctx, version.ID)
	if err != nil {
		return models.MissionVersion{}, false, err
	}
	if len(waypoints) > 0 {
		version.Waypoints = waypoints
	}
	return version, true, nil
}

const missionSelectSQL = `
	SELECT id, organization_id, drone_id, name, description, created_by, created_by_operator_id,
	       current_version_id, created_at, updated_at, archived_at,
	       completion_action, validation_status, validation_errors
	FROM missions
`

func scanMission(row rowScanner) (models.Mission, error) {
	var mission models.Mission
	var completionAction, validationStatus string
	var rawErrors []byte
	var organizationID, createdByOperatorID, currentVersionID sql.NullString
	var archivedAt sql.NullTime
	err := row.Scan(
		&mission.ID,
		&organizationID,
		&mission.DroneID,
		&mission.Name,
		&mission.Description,
		&mission.CreatedBy,
		&createdByOperatorID,
		&currentVersionID,
		&mission.CreatedAt,
		&mission.UpdatedAt,
		&archivedAt,
		&completionAction,
		&validationStatus,
		&rawErrors,
	)
	if err != nil {
		return models.Mission{}, err
	}
	mission.OrganizationID = organizationID.String
	mission.CreatedByOperatorID = createdByOperatorID.String
	mission.CurrentVersionID = currentVersionID.String
	mission.ArchivedAt = timeFromNull(archivedAt)
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

const missionVersionSelectSQL = `
	SELECT id, mission_id, version_number, waypoints, altitude_policy, speed_policy, geofence_policy,
	       rtl_policy, validation_status, validation_errors, created_by_operator_id, created_at
	FROM mission_versions
`

func scanMissionVersion(row rowScanner) (models.MissionVersion, error) {
	var version models.MissionVersion
	var validationStatus string
	var rawWaypoints, rawAltitudePolicy, rawSpeedPolicy, rawGeofencePolicy, rawRTLPolicy, rawErrors []byte
	var createdByOperatorID sql.NullString
	err := row.Scan(
		&version.ID,
		&version.MissionID,
		&version.VersionNumber,
		&rawWaypoints,
		&rawAltitudePolicy,
		&rawSpeedPolicy,
		&rawGeofencePolicy,
		&rawRTLPolicy,
		&validationStatus,
		&rawErrors,
		&createdByOperatorID,
		&version.CreatedAt,
	)
	if err != nil {
		return models.MissionVersion{}, err
	}
	version.ValidationStatus = models.MissionValidationStatus(validationStatus)
	version.CreatedByOperatorID = createdByOperatorID.String
	if len(rawWaypoints) > 0 {
		_ = json.Unmarshal(rawWaypoints, &version.Waypoints)
	}
	if len(rawAltitudePolicy) > 0 {
		_ = json.Unmarshal(rawAltitudePolicy, &version.AltitudePolicy)
	}
	if len(rawSpeedPolicy) > 0 {
		_ = json.Unmarshal(rawSpeedPolicy, &version.SpeedPolicy)
	}
	if len(rawGeofencePolicy) > 0 {
		_ = json.Unmarshal(rawGeofencePolicy, &version.GeofencePolicy)
	}
	if len(rawRTLPolicy) > 0 {
		_ = json.Unmarshal(rawRTLPolicy, &version.RTLPolicy)
	}
	if len(rawErrors) > 0 {
		_ = json.Unmarshal(rawErrors, &version.ValidationErrors)
	}
	return version, nil
}

func (r *MissionRepository) hydrateMissionDefinition(ctx context.Context, mission models.Mission) (models.Mission, error) {
	if mission.CurrentVersionID == "" {
		waypoints, err := r.listMissionWaypoints(ctx, mission.ID)
		if err != nil {
			return models.Mission{}, err
		}
		mission.Waypoints = waypoints
		return mission, nil
	}

	version, ok, err := r.GetMissionVersionByID(ctx, mission.CurrentVersionID)
	if err != nil {
		return models.Mission{}, err
	}
	if !ok {
		return models.Mission{}, repository.ErrMissionVersionNotFound
	}
	mission.Waypoints = version.Waypoints
	mission.ValidationStatus = version.ValidationStatus
	mission.ValidationErrors = version.ValidationErrors
	if version.RTLPolicy.CompletionAction != "" {
		mission.CompletionAction = version.RTLPolicy.CompletionAction
	}
	return mission, nil
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

func (r *MissionRepository) listMissionVersionWaypoints(ctx context.Context, missionVersionID string) ([]models.MissionWaypoint, error) {
	rows, err := r.exec.QueryContext(ctx, `
		SELECT sequence, latitude, longitude, relative_altitude_m, speed_mps, loiter_time_s
		FROM mission_version_waypoints
		WHERE mission_version_id = $1
		ORDER BY sequence
	`, missionVersionID)
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
