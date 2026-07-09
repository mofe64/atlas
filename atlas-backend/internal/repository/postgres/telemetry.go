package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
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
	rawSystemHealth, err := json.Marshal(snapshot.SystemHealth)
	if err != nil {
		return models.TelemetrySnapshot{}, err
	}
	if _, err := r.exec.ExecContext(ctx, `
		INSERT INTO telemetry_latest (
		  drone_id, vehicle_agent_id, active_telemetry_feed_id, source_communication_link_id,
		  observed_at, received_at, battery_percent, relative_altitude_m, altitude_msl,
		  flight_mode, armed, in_air, latitude, longitude, roll_deg, pitch_deg, heading_deg,
		  velocity_north_mps, velocity_east_mps, velocity_down_mps, ground_speed_mps,
		  gps_fix, satellites_visible, home_position_set, mission_current_item, mission_total_items,
		  system_health, source
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17,
		          $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28)
		ON CONFLICT (drone_id) DO UPDATE SET
		  vehicle_agent_id = EXCLUDED.vehicle_agent_id,
		  active_telemetry_feed_id = EXCLUDED.active_telemetry_feed_id,
		  source_communication_link_id = EXCLUDED.source_communication_link_id,
		  observed_at = EXCLUDED.observed_at,
		  received_at = EXCLUDED.received_at,
		  battery_percent = EXCLUDED.battery_percent,
		  relative_altitude_m = EXCLUDED.relative_altitude_m,
		  altitude_msl = EXCLUDED.altitude_msl,
		  flight_mode = EXCLUDED.flight_mode,
		  armed = EXCLUDED.armed,
		  in_air = EXCLUDED.in_air,
		  latitude = EXCLUDED.latitude,
		  longitude = EXCLUDED.longitude,
		  roll_deg = EXCLUDED.roll_deg,
		  pitch_deg = EXCLUDED.pitch_deg,
		  heading_deg = EXCLUDED.heading_deg,
		  velocity_north_mps = EXCLUDED.velocity_north_mps,
		  velocity_east_mps = EXCLUDED.velocity_east_mps,
		  velocity_down_mps = EXCLUDED.velocity_down_mps,
		  ground_speed_mps = EXCLUDED.ground_speed_mps,
		  gps_fix = EXCLUDED.gps_fix,
		  satellites_visible = EXCLUDED.satellites_visible,
		  home_position_set = EXCLUDED.home_position_set,
		  mission_current_item = EXCLUDED.mission_current_item,
		  mission_total_items = EXCLUDED.mission_total_items,
		  system_health = EXCLUDED.system_health,
		  source = EXCLUDED.source
	`,
		snapshot.DroneID,
		nullString(snapshot.VehicleAgentID),
		nullString(snapshot.ActiveTelemetryFeedID),
		nullString(snapshot.SourceCommunicationLinkID),
		snapshot.ObservedAt,
		snapshot.ReceivedAt,
		snapshot.BatteryPercent,
		snapshot.RelativeAltitudeM,
		snapshot.AltitudeMSL,
		snapshot.FlightMode,
		snapshot.Armed,
		snapshot.InAir,
		snapshot.Latitude,
		snapshot.Longitude,
		snapshot.RollDeg,
		snapshot.PitchDeg,
		snapshot.HeadingDeg,
		snapshot.VelocityNorthMPS,
		snapshot.VelocityEastMPS,
		snapshot.VelocityDownMPS,
		snapshot.GroundSpeedMPS,
		snapshot.GPSFix,
		snapshot.SatellitesVisible,
		snapshot.HomePositionSet,
		snapshot.MissionCurrentItem,
		snapshot.MissionTotalItems,
		rawSystemHealth,
		snapshot.Source,
	); err != nil {
		return models.TelemetrySnapshot{}, err
	}

	return snapshot, nil
}

func (r *TelemetryRepository) GetTelemetryForDrone(ctx context.Context, droneID string) (models.TelemetrySnapshot, bool) {
	return getTelemetryForDrone(ctx, r.exec, droneID)
}

func getTelemetryForDrone(ctx context.Context, q DBExecutor, droneID string) (models.TelemetrySnapshot, bool) {
	var snapshot models.TelemetrySnapshot
	var vehicleAgentID, activeTelemetryFeedID, sourceCommunicationLinkID sql.NullString
	var rawSystemHealth []byte
	err := q.QueryRowContext(ctx, `
		SELECT drone_id, vehicle_agent_id, active_telemetry_feed_id, source_communication_link_id,
		       observed_at, received_at, battery_percent, relative_altitude_m, altitude_msl,
		       flight_mode, armed, in_air, latitude, longitude, roll_deg, pitch_deg, heading_deg,
		       velocity_north_mps, velocity_east_mps, velocity_down_mps, ground_speed_mps,
		       gps_fix, satellites_visible, home_position_set, mission_current_item, mission_total_items,
		       system_health, source
		FROM telemetry_latest WHERE drone_id = $1
	`, droneID).Scan(
		&snapshot.DroneID,
		&vehicleAgentID,
		&activeTelemetryFeedID,
		&sourceCommunicationLinkID,
		&snapshot.ObservedAt,
		&snapshot.ReceivedAt,
		&snapshot.BatteryPercent,
		&snapshot.RelativeAltitudeM,
		&snapshot.AltitudeMSL,
		&snapshot.FlightMode,
		&snapshot.Armed,
		&snapshot.InAir,
		&snapshot.Latitude,
		&snapshot.Longitude,
		&snapshot.RollDeg,
		&snapshot.PitchDeg,
		&snapshot.HeadingDeg,
		&snapshot.VelocityNorthMPS,
		&snapshot.VelocityEastMPS,
		&snapshot.VelocityDownMPS,
		&snapshot.GroundSpeedMPS,
		&snapshot.GPSFix,
		&snapshot.SatellitesVisible,
		&snapshot.HomePositionSet,
		&snapshot.MissionCurrentItem,
		&snapshot.MissionTotalItems,
		&rawSystemHealth,
		&snapshot.Source,
	)
	if err != nil {
		return models.TelemetrySnapshot{}, false
	}

	snapshot.VehicleAgentID = vehicleAgentID.String
	snapshot.ActiveTelemetryFeedID = activeTelemetryFeedID.String
	snapshot.SourceCommunicationLinkID = sourceCommunicationLinkID.String
	if len(rawSystemHealth) > 0 {
		_ = json.Unmarshal(rawSystemHealth, &snapshot.SystemHealth)
	}
	return snapshot, true
}
