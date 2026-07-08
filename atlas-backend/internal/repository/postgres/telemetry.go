package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
)

type TelemetryRepository struct {
	exec DBExecutor
}

func NewTelemetryRepository(db *sql.DB) *TelemetryRepository {
	return newTelemetryRepository(db)
}

func newTelemetryRepository(exec DBExecutor) *TelemetryRepository {
	return &TelemetryRepository{exec: exec}
}

func (r *TelemetryRepository) UpsertLatestTelemetry(ctx context.Context, snapshot models.TelemetrySnapshot, now time.Time) (models.TelemetrySnapshot, error) {
	snapshot.ReceivedAt = now
	if _, err := r.exec.ExecContext(ctx, `
		INSERT INTO telemetry_latest (
		  drone_id, vehicle_agent_id, observed_at, received_at, battery_percent, relative_altitude_m,
		  flight_mode, armed, in_air, latitude, longitude, heading_deg, ground_speed_mps,
		  gps_fix, satellites_visible, home_position_set, source
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (drone_id) DO UPDATE SET
		  vehicle_agent_id = EXCLUDED.vehicle_agent_id,
		  observed_at = EXCLUDED.observed_at,
		  received_at = EXCLUDED.received_at,
		  battery_percent = EXCLUDED.battery_percent,
		  relative_altitude_m = EXCLUDED.relative_altitude_m,
		  flight_mode = EXCLUDED.flight_mode,
		  armed = EXCLUDED.armed,
		  in_air = EXCLUDED.in_air,
		  latitude = EXCLUDED.latitude,
		  longitude = EXCLUDED.longitude,
		  heading_deg = EXCLUDED.heading_deg,
		  ground_speed_mps = EXCLUDED.ground_speed_mps,
		  gps_fix = EXCLUDED.gps_fix,
		  satellites_visible = EXCLUDED.satellites_visible,
		  home_position_set = EXCLUDED.home_position_set,
		  source = EXCLUDED.source
	`, snapshot.DroneID, snapshot.VehicleAgentID, snapshot.ObservedAt, snapshot.ReceivedAt, snapshot.BatteryPercent,
		snapshot.RelativeAltitudeM, snapshot.FlightMode, snapshot.Armed, snapshot.InAir, snapshot.Latitude,
		snapshot.Longitude, snapshot.HeadingDeg, snapshot.GroundSpeedMPS, snapshot.GPSFix,
		snapshot.SatellitesVisible, snapshot.HomePositionSet, snapshot.Source); err != nil {
		return models.TelemetrySnapshot{}, err
	}

	return snapshot, nil
}

func (r *TelemetryRepository) GetTelemetryForDrone(ctx context.Context, droneID string) (models.TelemetrySnapshot, bool) {
	return getTelemetryForDrone(ctx, r.exec, droneID)
}

func getTelemetryForDrone(ctx context.Context, q DBExecutor, droneID string) (models.TelemetrySnapshot, bool) {
	var snapshot models.TelemetrySnapshot
	err := q.QueryRowContext(ctx, `
		SELECT drone_id, vehicle_agent_id, observed_at, received_at, battery_percent, relative_altitude_m,
		       flight_mode, armed, in_air, latitude, longitude, heading_deg, ground_speed_mps,
		       gps_fix, satellites_visible, home_position_set, source
		FROM telemetry_latest WHERE drone_id = $1
	`, droneID).Scan(
		&snapshot.DroneID,
		&snapshot.VehicleAgentID,
		&snapshot.ObservedAt,
		&snapshot.ReceivedAt,
		&snapshot.BatteryPercent,
		&snapshot.RelativeAltitudeM,
		&snapshot.FlightMode,
		&snapshot.Armed,
		&snapshot.InAir,
		&snapshot.Latitude,
		&snapshot.Longitude,
		&snapshot.HeadingDeg,
		&snapshot.GroundSpeedMPS,
		&snapshot.GPSFix,
		&snapshot.SatellitesVisible,
		&snapshot.HomePositionSet,
		&snapshot.Source,
	)
	return snapshot, err == nil
}
