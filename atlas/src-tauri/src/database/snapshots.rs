use std::time::{SystemTime, UNIX_EPOCH};

use rusqlite::OptionalExtension;
use serde::Serialize;

use super::{
    events::status_event_snapshot, BatteryTelemetry, GpsQuality, HomePosition, LocalDatabase,
    RcStatus, StatusEventSnapshot, VehicleHealth,
};

#[derive(Debug, Clone, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct AircraftTelemetrySnapshot {
    pub status: String,
    pub source: String,
    pub observed_at_unix_ms: i64,
    pub received_at_unix_ms: i64,
    pub battery_percent: Option<f64>,
    pub relative_altitude_m: Option<f64>,
    pub flight_mode: Option<String>,
    pub armed: Option<bool>,
    pub in_air: Option<bool>,
    pub latitude: Option<f64>,
    pub longitude: Option<f64>,
    pub heading_deg: Option<f64>,
    pub ground_speed_mps: Option<f64>,
    pub gps_fix: Option<String>,
    pub satellites_visible: Option<u32>,
    pub home_position_set: Option<bool>,
    pub batteries: Vec<BatteryTelemetry>,
    pub health: Option<VehicleHealth>,
    pub absolute_altitude_m: Option<f64>,
    pub terrain_altitude_m: Option<f64>,
    pub bottom_clearance_m: Option<f64>,
    pub velocity_north_mps: Option<f64>,
    pub velocity_east_mps: Option<f64>,
    pub velocity_down_mps: Option<f64>,
    pub climb_rate_mps: Option<f64>,
    pub landed_state: Option<String>,
    pub rc_status: Option<RcStatus>,
    pub home_position: Option<HomePosition>,
    pub gps_quality: Option<GpsQuality>,
}

#[derive(Debug, Clone, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct OperationsSnapshot {
    pub connection_status: String,
    pub drone_id: Option<String>,
    pub drone_name: Option<String>,
    pub vehicle_type: Option<String>,
    pub vehicle_status: Option<String>,
    pub agent_id: Option<String>,
    pub agent_version: Option<String>,
    pub agent_capabilities: Vec<String>,
    pub binding_id: Option<String>,
    pub communication_link_id: Option<String>,
    pub session_id: Option<String>,
    pub remote_address: Option<String>,
    pub connected_at_unix_ms: Option<i64>,
    pub last_heartbeat_at_unix_ms: Option<i64>,
    pub telemetry: Option<AircraftTelemetrySnapshot>,
    pub status_events: Vec<StatusEventSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct FleetSnapshot {
    pub generated_at_unix_ms: i64,
    pub aircraft: Vec<OperationsSnapshot>,
}

impl LocalDatabase {
    pub(crate) fn operations_snapshot(&self) -> Result<OperationsSnapshot, String> {
        self.operations_snapshot_for(None)
    }

    pub(crate) fn operations_snapshot_for(
        &self,
        drone_id: Option<&str>,
    ) -> Result<OperationsSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut snapshot = connection
            .query_row(
                r#"
                SELECT l.status, d.id, d.name, d.vehicle_type, d.status,
                       a.id, a.agent_version, a.capabilities_json, b.id,
                       l.id, l.session_instance_id, l.remote_address,
                       l.started_at_unix_ms, l.last_heartbeat_at_unix_ms
                FROM communication_links l
                JOIN vehicle_agent_bindings b ON b.id = l.vehicle_agent_binding_id
                JOIN vehicle_agents a ON a.id = b.vehicle_agent_id
                JOIN drones d ON d.id = b.drone_id
                WHERE (?1 IS NULL OR d.id = ?1)
                ORDER BY l.started_at_unix_ms DESC, l.rowid DESC
                LIMIT 1
                "#,
                [drone_id],
                |row| {
                    Ok(OperationsSnapshot {
                        connection_status: row.get(0)?,
                        drone_id: row.get(1)?,
                        drone_name: row.get(2)?,
                        vehicle_type: row.get(3)?,
                        vehicle_status: row.get(4)?,
                        agent_id: row.get(5)?,
                        agent_version: row.get(6)?,
                        agent_capabilities: serde_json::from_str(&row.get::<_, String>(7)?)
                            .unwrap_or_default(),
                        binding_id: row.get(8)?,
                        communication_link_id: row.get(9)?,
                        session_id: row.get(10)?,
                        remote_address: row.get(11)?,
                        connected_at_unix_ms: row.get(12)?,
                        last_heartbeat_at_unix_ms: row.get(13)?,
                        telemetry: None,
                        status_events: Vec::new(),
                    })
                },
            )
            .optional()
            .map(|snapshot| {
                snapshot.unwrap_or_else(|| OperationsSnapshot {
                    connection_status: "disconnected".to_string(),
                    ..OperationsSnapshot::default()
                })
            })
            .map_err(|error| format!("read operations snapshot: {error}"))?;
        if snapshot.connection_status == "connected"
            && snapshot
                .last_heartbeat_at_unix_ms
                .is_some_and(|last_heartbeat| unix_time_ms() - last_heartbeat > 15_000)
        {
            snapshot.connection_status = "stale".to_string();
        }
        let connection_live = snapshot.connection_status == "connected";
        if let Some(drone_id) = snapshot.drone_id.as_deref() {
            let telemetry_row = connection
                .query_row(
                    r#"
                    SELECT source, agent_observed_at_unix_ms, received_at_unix_ms,
                           battery_percent, relative_altitude_m, flight_mode,
                           armed, in_air, latitude, longitude, heading_deg,
                           ground_speed_mps, gps_fix, satellites_visible,
                           home_position_set, batteries_json, health_json,
                           absolute_altitude_m, terrain_altitude_m,
                           bottom_clearance_m, velocity_north_mps,
                           velocity_east_mps, velocity_down_mps,
                           climb_rate_mps, landed_state, rc_status_json,
                           home_position_json, gps_quality_json
                    FROM vehicle_telemetry_current
                    WHERE drone_id = ?1
                    "#,
                    [drone_id],
                    |row| {
                        Ok(RawAircraftTelemetry {
                            source: row.get(0)?,
                            observed_at_unix_ms: row.get(1)?,
                            received_at_unix_ms: row.get(2)?,
                            battery_percent: row.get(3)?,
                            relative_altitude_m: row.get(4)?,
                            flight_mode: row.get(5)?,
                            armed: row.get(6)?,
                            in_air: row.get(7)?,
                            latitude: row.get(8)?,
                            longitude: row.get(9)?,
                            heading_deg: row.get(10)?,
                            ground_speed_mps: row.get(11)?,
                            gps_fix: row.get(12)?,
                            satellites_visible: row.get(13)?,
                            home_position_set: row.get(14)?,
                            batteries_json: row.get(15)?,
                            health_json: row.get(16)?,
                            absolute_altitude_m: row.get(17)?,
                            terrain_altitude_m: row.get(18)?,
                            bottom_clearance_m: row.get(19)?,
                            velocity_north_mps: row.get(20)?,
                            velocity_east_mps: row.get(21)?,
                            velocity_down_mps: row.get(22)?,
                            climb_rate_mps: row.get(23)?,
                            landed_state: row.get(24)?,
                            rc_status_json: row.get(25)?,
                            home_position_json: row.get(26)?,
                            gps_quality_json: row.get(27)?,
                        })
                    },
                )
                .optional()
                .map_err(|error| format!("read current aircraft telemetry: {error}"))?;
            snapshot.telemetry = telemetry_row
                .map(|row| row.into_snapshot(connection_live))
                .transpose()?;

            let mut statement = connection
                .prepare(
                    r#"
                    SELECT id, source, severity, message,
                           agent_observed_at_unix_ms, received_at_unix_ms,
                           origin, event_type, code, details_json
                    FROM vehicle_status_events
                    WHERE drone_id = ?1
                    ORDER BY received_at_unix_ms DESC,
                             agent_observed_at_unix_ms DESC,
                             rowid DESC
                    LIMIT 20
                    "#,
                )
                .map_err(|error| format!("prepare vehicle status event history: {error}"))?;
            snapshot.status_events = statement
                .query_map([drone_id], status_event_snapshot)
                .map_err(|error| format!("read vehicle status event history: {error}"))?
                .collect::<Result<Vec<_>, _>>()
                .map_err(|error| format!("decode vehicle status event history: {error}"))?;
        }
        Ok(snapshot)
    }

    pub(crate) fn fleet_snapshot(&self) -> Result<FleetSnapshot, String> {
        let drone_ids = {
            let connection = self
                .connection
                .lock()
                .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
            let mut statement = connection
                .prepare(
                    r#"
                    SELECT d.id
                    FROM drones d
                    LEFT JOIN vehicle_telemetry_current t ON t.drone_id = d.id
                    ORDER BY t.received_at_unix_ms DESC NULLS LAST,
                             d.updated_at_unix_ms DESC,
                             d.id ASC
                    "#,
                )
                .map_err(|error| format!("prepare local fleet: {error}"))?;
            let rows = statement
                .query_map([], |row| row.get::<_, String>(0))
                .map_err(|error| format!("read local fleet: {error}"))?
                .collect::<Result<Vec<_>, _>>()
                .map_err(|error| format!("decode local fleet: {error}"))?;
            rows
        };

        let aircraft = drone_ids
            .iter()
            .map(|drone_id| self.operations_snapshot_for(Some(drone_id)))
            .collect::<Result<Vec<_>, _>>()?;

        Ok(FleetSnapshot {
            generated_at_unix_ms: unix_time_ms(),
            aircraft,
        })
    }
}

struct RawAircraftTelemetry {
    source: String,
    observed_at_unix_ms: i64,
    received_at_unix_ms: i64,
    battery_percent: Option<f64>,
    relative_altitude_m: Option<f64>,
    flight_mode: Option<String>,
    armed: Option<bool>,
    in_air: Option<bool>,
    latitude: Option<f64>,
    longitude: Option<f64>,
    heading_deg: Option<f64>,
    ground_speed_mps: Option<f64>,
    gps_fix: Option<String>,
    satellites_visible: Option<u32>,
    home_position_set: Option<bool>,
    batteries_json: String,
    health_json: Option<String>,
    absolute_altitude_m: Option<f64>,
    terrain_altitude_m: Option<f64>,
    bottom_clearance_m: Option<f64>,
    velocity_north_mps: Option<f64>,
    velocity_east_mps: Option<f64>,
    velocity_down_mps: Option<f64>,
    climb_rate_mps: Option<f64>,
    landed_state: Option<String>,
    rc_status_json: Option<String>,
    home_position_json: Option<String>,
    gps_quality_json: Option<String>,
}

impl RawAircraftTelemetry {
    fn into_snapshot(self, connection_live: bool) -> Result<AircraftTelemetrySnapshot, String> {
        let live = connection_live && unix_time_ms() - self.received_at_unix_ms <= 5_000;
        Ok(AircraftTelemetrySnapshot {
            status: if live { "live" } else { "stale" }.to_string(),
            source: self.source,
            observed_at_unix_ms: self.observed_at_unix_ms,
            received_at_unix_ms: self.received_at_unix_ms,
            battery_percent: self.battery_percent,
            relative_altitude_m: self.relative_altitude_m,
            flight_mode: self.flight_mode,
            armed: self.armed,
            in_air: self.in_air,
            latitude: self.latitude,
            longitude: self.longitude,
            heading_deg: self.heading_deg,
            ground_speed_mps: self.ground_speed_mps,
            gps_fix: self.gps_fix,
            satellites_visible: self.satellites_visible,
            home_position_set: self.home_position_set,
            batteries: decode_json(&self.batteries_json, "battery telemetry")?,
            health: decode_optional_json(self.health_json.as_deref(), "vehicle health")?,
            absolute_altitude_m: self.absolute_altitude_m,
            terrain_altitude_m: self.terrain_altitude_m,
            bottom_clearance_m: self.bottom_clearance_m,
            velocity_north_mps: self.velocity_north_mps,
            velocity_east_mps: self.velocity_east_mps,
            velocity_down_mps: self.velocity_down_mps,
            climb_rate_mps: self.climb_rate_mps,
            landed_state: self.landed_state,
            rc_status: decode_optional_json(self.rc_status_json.as_deref(), "RC status")?,
            home_position: decode_optional_json(
                self.home_position_json.as_deref(),
                "home position",
            )?,
            gps_quality: decode_optional_json(self.gps_quality_json.as_deref(), "GPS quality")?,
        })
    }
}

fn decode_json<T: serde::de::DeserializeOwned>(value: &str, label: &str) -> Result<T, String> {
    serde_json::from_str(value).map_err(|error| format!("decode {label}: {error}"))
}

fn decode_optional_json<T: serde::de::DeserializeOwned>(
    value: Option<&str>,
    label: &str,
) -> Result<Option<T>, String> {
    value.map(|value| decode_json(value, label)).transpose()
}

fn unix_time_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as i64
}
