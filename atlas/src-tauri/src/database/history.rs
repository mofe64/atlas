use rusqlite::params;
use serde::{Deserialize, Serialize};

use super::{unix_time_ms, LocalDatabase, TelemetryInput};

const DEFAULT_HISTORY_PAGE_SIZE: usize = 100;
const MAXIMUM_HISTORY_PAGE_SIZE: usize = 500;
const DEFAULT_CHART_POINTS: usize = 600;
const MAXIMUM_CHART_POINTS: usize = 1_200;

#[derive(Debug, Clone)]
pub(crate) struct TelemetryHistoryQuery {
    pub drone_id: String,
    pub from_received_at_unix_ms: Option<i64>,
    pub to_received_at_unix_ms: Option<i64>,
    pub before: Option<TelemetryHistoryCursor>,
    pub limit: usize,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TelemetryHistoryCursor {
    pub received_at_unix_ms: i64,
    pub id: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TelemetryHistorySnapshot {
    pub id: String,
    pub drone_id: String,
    pub communication_link_id: String,
    pub capture_reason: String,
    pub telemetry: TelemetryInput,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TelemetryHistoryPage {
    pub snapshots: Vec<TelemetryHistorySnapshot>,
    pub next_cursor: Option<TelemetryHistoryCursor>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TelemetryChartPoint {
    pub received_at_unix_ms: i64,
    pub battery_percent: Option<f64>,
    pub relative_altitude_m: Option<f64>,
    pub bottom_clearance_m: Option<f64>,
    pub ground_speed_mps: Option<f64>,
    pub climb_rate_mps: Option<f64>,
    pub rc_signal_strength_percent: Option<f64>,
    pub gps_hdop: Option<f64>,
    pub flight_mode: Option<String>,
    pub armed: Option<bool>,
    pub in_air: Option<bool>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TelemetryChartSeries {
    pub from_received_at_unix_ms: i64,
    pub to_received_at_unix_ms: i64,
    pub bucket_width_ms: i64,
    pub points: Vec<TelemetryChartPoint>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct HistoryOverview {
    pub generated_at_unix_ms: i64,
    pub retention_days: u32,
    pub drones: Vec<DroneHistorySummary>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct DroneHistorySummary {
    pub drone_id: String,
    pub drone_name: String,
    pub vehicle_type: String,
    pub snapshot_count: i64,
    pub event_count: i64,
    pub first_snapshot_at_unix_ms: Option<i64>,
    pub last_snapshot_at_unix_ms: Option<i64>,
    pub last_event_at_unix_ms: Option<i64>,
    pub latest_flight_mode: Option<String>,
    pub latest_battery_percent: Option<f64>,
    pub latest_in_air: Option<bool>,
}

impl LocalDatabase {
    pub(crate) fn history_overview(&self) -> Result<HistoryOverview, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare(
                r#"
                WITH snapshot_summary AS (
                    SELECT drone_id,
                           count(*) AS snapshot_count,
                           min(received_at_unix_ms) AS first_snapshot_at_unix_ms,
                           max(received_at_unix_ms) AS last_snapshot_at_unix_ms
                    FROM vehicle_telemetry_snapshots
                    GROUP BY drone_id
                ),
                latest_snapshot AS (
                    SELECT drone_id, flight_mode, battery_percent, in_air
                    FROM (
                        SELECT drone_id, flight_mode, battery_percent, in_air,
                               row_number() OVER (
                                   PARTITION BY drone_id
                                   ORDER BY received_at_unix_ms DESC, id DESC
                               ) AS snapshot_rank
                        FROM vehicle_telemetry_snapshots
                    )
                    WHERE snapshot_rank = 1
                ),
                event_summary AS (
                    SELECT drone_id,
                           count(*) AS event_count,
                           max(received_at_unix_ms) AS last_event_at_unix_ms
                    FROM vehicle_status_events
                    GROUP BY drone_id
                )
                SELECT d.id, d.name, d.vehicle_type,
                       coalesce(s.snapshot_count, 0), coalesce(e.event_count, 0),
                       s.first_snapshot_at_unix_ms, s.last_snapshot_at_unix_ms,
                       e.last_event_at_unix_ms, l.flight_mode,
                       l.battery_percent, l.in_air
                FROM drones d
                LEFT JOIN snapshot_summary s ON s.drone_id = d.id
                LEFT JOIN latest_snapshot l ON l.drone_id = d.id
                LEFT JOIN event_summary e ON e.drone_id = d.id
                ORDER BY coalesce(s.last_snapshot_at_unix_ms, e.last_event_at_unix_ms,
                                  d.updated_at_unix_ms) DESC,
                         d.name ASC,
                         d.id ASC
                "#,
            )
            .map_err(|error| format!("prepare history overview: {error}"))?;
        let drones = statement
            .query_map([], |row| {
                Ok(DroneHistorySummary {
                    drone_id: row.get(0)?,
                    drone_name: row.get(1)?,
                    vehicle_type: row.get(2)?,
                    snapshot_count: row.get(3)?,
                    event_count: row.get(4)?,
                    first_snapshot_at_unix_ms: row.get(5)?,
                    last_snapshot_at_unix_ms: row.get(6)?,
                    last_event_at_unix_ms: row.get(7)?,
                    latest_flight_mode: row.get(8)?,
                    latest_battery_percent: row.get(9)?,
                    latest_in_air: row.get(10)?,
                })
            })
            .map_err(|error| format!("query history overview: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read history overview: {error}"))?;

        Ok(HistoryOverview {
            generated_at_unix_ms: unix_time_ms(),
            retention_days: 7,
            drones,
        })
    }

    pub(crate) fn telemetry_history(
        &self,
        query: &TelemetryHistoryQuery,
    ) -> Result<TelemetryHistoryPage, String> {
        validate_query(query)?;
        let page_size = match query.limit {
            0 => DEFAULT_HISTORY_PAGE_SIZE,
            requested => requested.min(MAXIMUM_HISTORY_PAGE_SIZE),
        };
        let cursor_received_at = query
            .before
            .as_ref()
            .map(|cursor| cursor.received_at_unix_ms);
        let cursor_id = query.before.as_ref().map(|cursor| cursor.id.as_str());
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare(
                r#"
                SELECT id, drone_id, communication_link_id, capture_reason,
                       source, agent_observed_at_unix_ms, received_at_unix_ms,
                       battery_percent, relative_altitude_m, flight_mode, armed,
                       in_air, latitude, longitude, heading_deg, ground_speed_mps,
                       gps_fix, satellites_visible, home_position_set,
                       batteries_json, health_json, absolute_altitude_m,
                       terrain_altitude_m, bottom_clearance_m,
                       velocity_north_mps, velocity_east_mps, velocity_down_mps,
                       climb_rate_mps, landed_state, rc_status_json,
                       home_position_json, gps_quality_json
                FROM vehicle_telemetry_snapshots
                WHERE drone_id = ?1
                  AND (?2 IS NULL OR received_at_unix_ms >= ?2)
                  AND (?3 IS NULL OR received_at_unix_ms <= ?3)
                  AND (
                    ?4 IS NULL
                    OR received_at_unix_ms < ?4
                    OR (received_at_unix_ms = ?4 AND id < ?5)
                  )
                ORDER BY received_at_unix_ms DESC, id DESC
                LIMIT ?6
                "#,
            )
            .map_err(|error| format!("prepare vehicle telemetry history query: {error}"))?;
        let rows = statement
            .query_map(
                params![
                    &query.drone_id,
                    query.from_received_at_unix_ms,
                    query.to_received_at_unix_ms,
                    cursor_received_at,
                    cursor_id,
                    (page_size + 1) as i64,
                ],
                |row| {
                    Ok(RawTelemetryHistorySnapshot {
                        id: row.get(0)?,
                        drone_id: row.get(1)?,
                        communication_link_id: row.get(2)?,
                        capture_reason: row.get(3)?,
                        source: row.get(4)?,
                        agent_observed_at_unix_ms: row.get(5)?,
                        received_at_unix_ms: row.get(6)?,
                        battery_percent: row.get(7)?,
                        relative_altitude_m: row.get(8)?,
                        flight_mode: row.get(9)?,
                        armed: row.get(10)?,
                        in_air: row.get(11)?,
                        latitude: row.get(12)?,
                        longitude: row.get(13)?,
                        heading_deg: row.get(14)?,
                        ground_speed_mps: row.get(15)?,
                        gps_fix: row.get(16)?,
                        satellites_visible: row.get(17)?,
                        home_position_set: row.get(18)?,
                        batteries_json: row.get(19)?,
                        health_json: row.get(20)?,
                        absolute_altitude_m: row.get(21)?,
                        terrain_altitude_m: row.get(22)?,
                        bottom_clearance_m: row.get(23)?,
                        velocity_north_mps: row.get(24)?,
                        velocity_east_mps: row.get(25)?,
                        velocity_down_mps: row.get(26)?,
                        climb_rate_mps: row.get(27)?,
                        landed_state: row.get(28)?,
                        rc_status_json: row.get(29)?,
                        home_position_json: row.get(30)?,
                        gps_quality_json: row.get(31)?,
                    })
                },
            )
            .map_err(|error| format!("query vehicle telemetry history: {error}"))?;
        let mut snapshots = rows
            .map(|row| {
                row.map_err(|error| format!("read vehicle telemetry history row: {error}"))?
                    .decode()
            })
            .collect::<Result<Vec<_>, String>>()?;
        let has_more = snapshots.len() > page_size;
        snapshots.truncate(page_size);
        let next_cursor = has_more.then(|| {
            let last = snapshots
                .last()
                .expect("a history page with more rows cannot be empty");
            TelemetryHistoryCursor {
                received_at_unix_ms: last.telemetry.received_at_unix_ms,
                id: last.id.clone(),
            }
        });
        Ok(TelemetryHistoryPage {
            snapshots,
            next_cursor,
        })
    }

    pub(crate) fn telemetry_chart_series(
        &self,
        drone_id: &str,
        from_received_at_unix_ms: i64,
        to_received_at_unix_ms: i64,
        maximum_points: usize,
    ) -> Result<TelemetryChartSeries, String> {
        if drone_id.trim().is_empty() {
            return Err("telemetry chart drone id is required".to_string());
        }
        if from_received_at_unix_ms >= to_received_at_unix_ms {
            return Err("telemetry chart start time must be before its end time".to_string());
        }
        let maximum_points = match maximum_points {
            0 => DEFAULT_CHART_POINTS,
            requested => requested.clamp(2, MAXIMUM_CHART_POINTS),
        };
        let span_ms = to_received_at_unix_ms
            .saturating_sub(from_received_at_unix_ms)
            .saturating_add(1);
        let bucket_width_ms = span_ms
            .saturating_add(maximum_points as i64 - 1)
            .checked_div(maximum_points as i64)
            .unwrap_or(1)
            .max(1);
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare(
                r#"
                WITH ranked AS (
                    SELECT received_at_unix_ms, battery_percent,
                           relative_altitude_m, bottom_clearance_m,
                           ground_speed_mps, climb_rate_mps,
                           CAST(json_extract(rc_status_json, '$.signalStrengthPercent') AS REAL)
                               AS rc_signal_strength_percent,
                           CAST(json_extract(gps_quality_json, '$.hdop') AS REAL)
                               AS gps_hdop,
                           flight_mode, armed, in_air,
                           row_number() OVER (
                               PARTITION BY ((received_at_unix_ms - ?2) / ?4)
                               ORDER BY received_at_unix_ms DESC, id DESC
                           ) AS bucket_rank
                    FROM vehicle_telemetry_snapshots
                    WHERE drone_id = ?1
                      AND received_at_unix_ms >= ?2
                      AND received_at_unix_ms <= ?3
                )
                SELECT received_at_unix_ms, battery_percent,
                       relative_altitude_m, bottom_clearance_m,
                       ground_speed_mps, climb_rate_mps,
                       rc_signal_strength_percent, gps_hdop,
                       flight_mode, armed, in_air
                FROM ranked
                WHERE bucket_rank = 1
                ORDER BY received_at_unix_ms
                "#,
            )
            .map_err(|error| format!("prepare telemetry chart series query: {error}"))?;
        let points = statement
            .query_map(
                params![
                    drone_id,
                    from_received_at_unix_ms,
                    to_received_at_unix_ms,
                    bucket_width_ms
                ],
                |row| {
                    Ok(TelemetryChartPoint {
                        received_at_unix_ms: row.get(0)?,
                        battery_percent: row.get(1)?,
                        relative_altitude_m: row.get(2)?,
                        bottom_clearance_m: row.get(3)?,
                        ground_speed_mps: row.get(4)?,
                        climb_rate_mps: row.get(5)?,
                        rc_signal_strength_percent: row.get(6)?,
                        gps_hdop: row.get(7)?,
                        flight_mode: row.get(8)?,
                        armed: row.get(9)?,
                        in_air: row.get(10)?,
                    })
                },
            )
            .map_err(|error| format!("query telemetry chart series: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read telemetry chart series: {error}"))?;
        Ok(TelemetryChartSeries {
            from_received_at_unix_ms,
            to_received_at_unix_ms,
            bucket_width_ms,
            points,
        })
    }
}

fn validate_query(query: &TelemetryHistoryQuery) -> Result<(), String> {
    if query.drone_id.trim().is_empty() {
        return Err("telemetry history drone id is required".to_string());
    }
    if query
        .from_received_at_unix_ms
        .zip(query.to_received_at_unix_ms)
        .is_some_and(|(from, to)| from > to)
    {
        return Err("telemetry history start time must not be after its end time".to_string());
    }
    if query
        .before
        .as_ref()
        .is_some_and(|cursor| cursor.id.trim().is_empty())
    {
        return Err("telemetry history cursor id is required".to_string());
    }
    Ok(())
}

struct RawTelemetryHistorySnapshot {
    id: String,
    drone_id: String,
    communication_link_id: String,
    capture_reason: String,
    source: String,
    agent_observed_at_unix_ms: i64,
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

impl RawTelemetryHistorySnapshot {
    fn decode(self) -> Result<TelemetryHistorySnapshot, String> {
        Ok(TelemetryHistorySnapshot {
            id: self.id,
            drone_id: self.drone_id,
            communication_link_id: self.communication_link_id,
            capture_reason: self.capture_reason,
            telemetry: TelemetryInput {
                source: self.source,
                agent_observed_at_unix_ms: self.agent_observed_at_unix_ms,
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
                batteries: decode_json(&self.batteries_json, "historical battery telemetry")?,
                health: decode_optional_json(
                    self.health_json.as_deref(),
                    "historical vehicle health",
                )?,
                absolute_altitude_m: self.absolute_altitude_m,
                terrain_altitude_m: self.terrain_altitude_m,
                bottom_clearance_m: self.bottom_clearance_m,
                velocity_north_mps: self.velocity_north_mps,
                velocity_east_mps: self.velocity_east_mps,
                velocity_down_mps: self.velocity_down_mps,
                climb_rate_mps: self.climb_rate_mps,
                landed_state: self.landed_state,
                rc_status: decode_optional_json(
                    self.rc_status_json.as_deref(),
                    "historical RC status",
                )?,
                home_position: decode_optional_json(
                    self.home_position_json.as_deref(),
                    "historical home position",
                )?,
                gps_quality: decode_optional_json(
                    self.gps_quality_json.as_deref(),
                    "historical GPS quality",
                )?,
            },
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
