use rusqlite::{params, Connection, OptionalExtension};
use serde::{Deserialize, Serialize};

use super::{
    alerts::reconcile_telemetry_alerts,
    events::{insert_status_event, prune_status_events},
    LocalDatabase, StatusEventInput,
};

const ACTIVE_SNAPSHOT_INTERVAL_MS: i64 = 5_000;
const IDLE_SNAPSHOT_INTERVAL_MS: i64 = 30_000;
pub(super) const SNAPSHOT_RETENTION_MS: i64 = 7 * 24 * 60 * 60 * 1_000;

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TelemetryInput {
    pub source: String,
    pub agent_observed_at_unix_ms: i64,
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

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct BatteryTelemetry {
    pub id: u32,
    pub function: String,
    pub remaining_percent: Option<f64>,
    pub voltage_v: Option<f64>,
    pub current_a: Option<f64>,
    pub temperature_c: Option<f64>,
    pub consumed_ah: Option<f64>,
    pub time_remaining_s: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct VehicleHealth {
    pub gyrometer_calibration_ok: bool,
    pub accelerometer_calibration_ok: bool,
    pub magnetometer_calibration_ok: bool,
    pub local_position_ok: bool,
    pub global_position_ok: bool,
    pub home_position_ok: bool,
    pub armable: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct RcStatus {
    pub available: bool,
    pub was_available_once: bool,
    pub signal_strength_percent: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct HomePosition {
    pub latitude: Option<f64>,
    pub longitude: Option<f64>,
    pub absolute_altitude_m: Option<f64>,
    pub relative_altitude_m: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct GpsQuality {
    pub hdop: Option<f64>,
    pub vdop: Option<f64>,
    pub horizontal_uncertainty_m: Option<f64>,
    pub vertical_uncertainty_m: Option<f64>,
    pub velocity_uncertainty_mps: Option<f64>,
    pub course_over_ground_deg: Option<f64>,
}

impl LocalDatabase {
    pub(crate) fn record_telemetry(
        &self,
        session_id: &str,
        input: &TelemetryInput,
    ) -> Result<(), String> {
        let batteries_json = serde_json::to_string(&input.batteries)
            .map_err(|error| format!("encode battery telemetry: {error}"))?;
        let health_json = encode_optional_json(input.health.as_ref(), "vehicle health")?;
        let rc_status_json = encode_optional_json(input.rc_status.as_ref(), "RC status")?;
        let home_position_json =
            encode_optional_json(input.home_position.as_ref(), "home position")?;
        let gps_quality_json = encode_optional_json(input.gps_quality.as_ref(), "GPS quality")?;

        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let transaction = connection
            .transaction()
            .map_err(|error| format!("begin vehicle telemetry transaction: {error}"))?;
        let association: Option<(String, String)> = transaction
            .query_row(
                r#"
                SELECT b.drone_id, l.id
                FROM communication_links l
                JOIN vehicle_agent_bindings b ON b.id = l.vehicle_agent_binding_id
                WHERE l.session_instance_id = ?1 AND l.ended_at_unix_ms IS NULL
                "#,
                [session_id],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .optional()
            .map_err(|error| format!("resolve telemetry session: {error}"))?;
        let (drone_id, communication_link_id) =
            association.ok_or_else(|| "communication link is not open".to_string())?;
        let previous_state = transaction
            .query_row(
                r#"
                SELECT armed, in_air, flight_mode, landed_state
                FROM vehicle_telemetry_current
                WHERE drone_id = ?1
                "#,
                [&drone_id],
                |row| {
                    Ok(PreviousState {
                        armed: row.get(0)?,
                        in_air: row.get(1)?,
                        flight_mode: row.get(2)?,
                        landed_state: row.get(3)?,
                    })
                },
            )
            .optional()
            .map_err(|error| format!("read previous vehicle telemetry state: {error}"))?;
        let latest_snapshot_at: Option<i64> = transaction
            .query_row(
                "SELECT max(received_at_unix_ms) FROM vehicle_telemetry_snapshots WHERE drone_id = ?1",
                [&drone_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("read latest vehicle telemetry snapshot time: {error}"))?;
        let capture_reason =
            snapshot_capture_reason(previous_state.as_ref(), latest_snapshot_at, input);
        let derived_events = derive_status_events(previous_state.as_ref(), input)?;

        transaction
            .execute(
                r#"
                INSERT INTO vehicle_telemetry_current (
                    drone_id, source, agent_observed_at_unix_ms, received_at_unix_ms,
                    battery_percent, relative_altitude_m, flight_mode, armed, in_air,
                    latitude, longitude, heading_deg, ground_speed_mps, gps_fix,
                    satellites_visible, home_position_set, batteries_json,
                    health_json, absolute_altitude_m, terrain_altitude_m,
                    bottom_clearance_m, velocity_north_mps, velocity_east_mps,
                    velocity_down_mps, climb_rate_mps, landed_state,
                    rc_status_json, home_position_json, gps_quality_json
                ) VALUES (
                    ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9,
                    ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17,
                    ?18, ?19, ?20, ?21, ?22, ?23, ?24, ?25,
                    ?26, ?27, ?28, ?29
                )
                ON CONFLICT(drone_id) DO UPDATE SET
                    source = excluded.source,
                    agent_observed_at_unix_ms = excluded.agent_observed_at_unix_ms,
                    received_at_unix_ms = excluded.received_at_unix_ms,
                    battery_percent = excluded.battery_percent,
                    relative_altitude_m = excluded.relative_altitude_m,
                    flight_mode = excluded.flight_mode,
                    armed = excluded.armed,
                    in_air = excluded.in_air,
                    latitude = excluded.latitude,
                    longitude = excluded.longitude,
                    heading_deg = excluded.heading_deg,
                    ground_speed_mps = excluded.ground_speed_mps,
                    gps_fix = excluded.gps_fix,
                    satellites_visible = excluded.satellites_visible,
                    home_position_set = excluded.home_position_set,
                    batteries_json = excluded.batteries_json,
                    health_json = excluded.health_json,
                    absolute_altitude_m = excluded.absolute_altitude_m,
                    terrain_altitude_m = excluded.terrain_altitude_m,
                    bottom_clearance_m = excluded.bottom_clearance_m,
                    velocity_north_mps = excluded.velocity_north_mps,
                    velocity_east_mps = excluded.velocity_east_mps,
                    velocity_down_mps = excluded.velocity_down_mps,
                    climb_rate_mps = excluded.climb_rate_mps,
                    landed_state = excluded.landed_state,
                    rc_status_json = excluded.rc_status_json,
                    home_position_json = excluded.home_position_json,
                    gps_quality_json = excluded.gps_quality_json
                "#,
                params![
                    &drone_id,
                    &input.source,
                    input.agent_observed_at_unix_ms,
                    input.received_at_unix_ms,
                    input.battery_percent,
                    input.relative_altitude_m,
                    &input.flight_mode,
                    input.armed,
                    input.in_air,
                    input.latitude,
                    input.longitude,
                    input.heading_deg,
                    input.ground_speed_mps,
                    &input.gps_fix,
                    input.satellites_visible,
                    input.home_position_set,
                    &batteries_json,
                    &health_json,
                    input.absolute_altitude_m,
                    input.terrain_altitude_m,
                    input.bottom_clearance_m,
                    input.velocity_north_mps,
                    input.velocity_east_mps,
                    input.velocity_down_mps,
                    input.climb_rate_mps,
                    &input.landed_state,
                    &rc_status_json,
                    &home_position_json,
                    &gps_quality_json,
                ],
            )
            .map_err(|error| format!("store latest vehicle telemetry: {error}"))?;

        if let Some(capture_reason) = capture_reason {
            transaction
                .execute(
                    r#"
                    INSERT INTO vehicle_telemetry_snapshots (
                        id, drone_id, communication_link_id, capture_reason,
                        source, agent_observed_at_unix_ms, received_at_unix_ms,
                        battery_percent, relative_altitude_m, flight_mode, armed,
                        in_air, latitude, longitude, heading_deg, ground_speed_mps,
                        gps_fix, satellites_visible, home_position_set,
                        batteries_json, health_json, absolute_altitude_m,
                        terrain_altitude_m, bottom_clearance_m,
                        velocity_north_mps, velocity_east_mps, velocity_down_mps,
                        climb_rate_mps, landed_state, rc_status_json,
                        home_position_json, gps_quality_json
                    )
                    SELECT lower(hex(randomblob(16))), drone_id, ?2, ?3,
                           source, agent_observed_at_unix_ms, received_at_unix_ms,
                           battery_percent, relative_altitude_m, flight_mode, armed,
                           in_air, latitude, longitude, heading_deg, ground_speed_mps,
                           gps_fix, satellites_visible, home_position_set,
                           batteries_json, health_json, absolute_altitude_m,
                           terrain_altitude_m, bottom_clearance_m,
                           velocity_north_mps, velocity_east_mps, velocity_down_mps,
                           climb_rate_mps, landed_state, rc_status_json,
                           home_position_json, gps_quality_json
                    FROM vehicle_telemetry_current
                    WHERE drone_id = ?1
                    "#,
                    params![&drone_id, &communication_link_id, capture_reason],
                )
                .map_err(|error| format!("store vehicle telemetry snapshot: {error}"))?;
            prune_expired_snapshots(&transaction, input.received_at_unix_ms)?;
        }
        for event in &derived_events {
            insert_status_event(&transaction, &drone_id, &communication_link_id, event)?;
        }
        if !derived_events.is_empty() {
            prune_status_events(&transaction, &drone_id)?;
        }
        reconcile_telemetry_alerts(&transaction, &drone_id, input)?;

        transaction
            .commit()
            .map_err(|error| format!("commit vehicle telemetry: {error}"))
    }
}

pub(super) fn prune_expired_snapshots(
    connection: &Connection,
    now_unix_ms: i64,
) -> Result<usize, String> {
    let cutoff = now_unix_ms.saturating_sub(SNAPSHOT_RETENTION_MS);
    connection
        .execute(
            "DELETE FROM vehicle_telemetry_snapshots WHERE received_at_unix_ms < ?1",
            [cutoff],
        )
        .map_err(|error| format!("prune expired vehicle telemetry snapshots: {error}"))
}

struct PreviousState {
    armed: Option<bool>,
    in_air: Option<bool>,
    flight_mode: Option<String>,
    landed_state: Option<String>,
}

fn snapshot_capture_reason(
    previous: Option<&PreviousState>,
    latest_snapshot_at: Option<i64>,
    input: &TelemetryInput,
) -> Option<&'static str> {
    let Some(latest_snapshot_at) = latest_snapshot_at else {
        return Some("initial");
    };
    if previous.is_some_and(|previous| state_changed(previous, input)) {
        return Some("state_transition");
    }
    let interval = if vehicle_is_active(previous, input) {
        ACTIVE_SNAPSHOT_INTERVAL_MS
    } else {
        IDLE_SNAPSHOT_INTERVAL_MS
    };
    (input.received_at_unix_ms.saturating_sub(latest_snapshot_at) >= interval).then_some("interval")
}

fn state_changed(previous: &PreviousState, input: &TelemetryInput) -> bool {
    changed(&previous.armed, &input.armed)
        || changed(&previous.in_air, &input.in_air)
        || changed(&previous.flight_mode, &input.flight_mode)
        || changed(&previous.landed_state, &input.landed_state)
}

fn derive_status_events(
    previous: Option<&PreviousState>,
    input: &TelemetryInput,
) -> Result<Vec<StatusEventInput>, String> {
    let Some(previous) = previous else {
        return Ok(Vec::new());
    };
    let mut events = Vec::new();
    if let (Some(before), Some(after)) = (previous.armed, input.armed) {
        if before != after {
            events.push(derived_event(
                if after {
                    "vehicle_armed"
                } else {
                    "vehicle_disarmed"
                },
                if after {
                    "Vehicle armed"
                } else {
                    "Vehicle disarmed"
                },
                "INFO",
                serde_json::json!({ "previous": before, "current": after }),
                input,
            )?);
        }
    }
    if let (Some(before), Some(after)) = (previous.in_air, input.in_air) {
        if before != after {
            events.push(derived_event(
                if after {
                    "takeoff_detected"
                } else {
                    "landing_detected"
                },
                if after {
                    "Takeoff detected"
                } else {
                    "Landing detected"
                },
                "INFO",
                serde_json::json!({ "previous": before, "current": after }),
                input,
            )?);
        }
    }
    if let (Some(before), Some(after)) = (
        previous.flight_mode.as_deref(),
        input.flight_mode.as_deref(),
    ) {
        if before != after {
            events.push(derived_event(
                "flight_mode_changed",
                &format!("Flight mode changed from {before} to {after}"),
                "NOTICE",
                serde_json::json!({ "previous": before, "current": after }),
                input,
            )?);
        }
    }
    if let (Some(before), Some(after)) = (
        previous.landed_state.as_deref(),
        input.landed_state.as_deref(),
    ) {
        if before != after {
            events.push(derived_event(
                "landed_state_changed",
                &format!("Landed state changed from {before} to {after}"),
                "NOTICE",
                serde_json::json!({ "previous": before, "current": after }),
                input,
            )?);
        }
    }
    Ok(events)
}

fn derived_event(
    event_type: &str,
    message: &str,
    severity: &str,
    details: serde_json::Value,
    input: &TelemetryInput,
) -> Result<StatusEventInput, String> {
    Ok(StatusEventInput {
        origin: "atlas_native".to_string(),
        event_type: event_type.to_string(),
        code: Some(event_type.to_string()),
        details_json: Some(
            serde_json::to_string(&details)
                .map_err(|error| format!("encode derived vehicle event details: {error}"))?,
        ),
        source: "telemetry_transition".to_string(),
        severity: severity.to_string(),
        message: message.to_string(),
        agent_observed_at_unix_ms: input.agent_observed_at_unix_ms,
        received_at_unix_ms: input.received_at_unix_ms,
    })
}

fn changed<T: PartialEq>(previous: &Option<T>, current: &Option<T>) -> bool {
    matches!((previous, current), (Some(previous), Some(current)) if previous != current)
}

fn vehicle_is_active(previous: Option<&PreviousState>, input: &TelemetryInput) -> bool {
    input
        .armed
        .or_else(|| previous.and_then(|state| state.armed))
        == Some(true)
        || input
            .in_air
            .or_else(|| previous.and_then(|state| state.in_air))
            == Some(true)
        || input
            .landed_state
            .as_deref()
            .or_else(|| previous.and_then(|state| state.landed_state.as_deref()))
            .is_some_and(|state| matches!(state, "TAKING_OFF" | "IN_AIR" | "LANDING"))
}

fn encode_optional_json<T: Serialize>(
    value: Option<&T>,
    label: &str,
) -> Result<Option<String>, String> {
    value
        .map(serde_json::to_string)
        .transpose()
        .map_err(|error| format!("encode {label}: {error}"))
}
