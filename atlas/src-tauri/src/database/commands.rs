use rusqlite::{params, OptionalExtension, Transaction};
use serde::Serialize;

use super::{perception_geolocations, LocalDatabase};

const DEFAULT_COMMAND_TIMEOUT_MS: i64 = 15_000;
const MAX_COMMAND_TIMEOUT_MS: i64 = 120_000;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum PayloadControlContext<'a> {
    Inspection,
    MissionOverride(&'a str),
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct VehicleCommandSnapshot {
    pub id: String,
    pub drone_id: String,
    pub command_type: String,
    pub parameters_json: String,
    pub status: String,
    pub idempotency_key: String,
    pub created_at_unix_ms: i64,
    pub deadline_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
    pub accepted_at_unix_ms: Option<i64>,
    pub started_at_unix_ms: Option<i64>,
    pub completed_at_unix_ms: Option<i64>,
    pub delivery_attempts: u32,
    pub last_communication_link_id: Option<String>,
    pub result_code: String,
    pub result_message: String,
    pub evidence_json: Option<String>,
    pub events: Vec<VehicleCommandEventSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct VehicleCommandEventSnapshot {
    pub id: String,
    pub sequence: u32,
    pub event_type: String,
    pub state: String,
    pub communication_link_id: Option<String>,
    pub source: String,
    pub occurred_at_unix_ms: i64,
    pub progress_percent: Option<f64>,
    pub result_code: String,
    pub message: String,
    pub evidence_json: Option<String>,
}

pub(crate) struct VehicleCommandUpdateInput {
    pub event_id: String,
    pub command_id: String,
    pub event_type: String,
    pub next_state: Option<String>,
    pub communication_link_id: String,
    pub occurred_at_unix_ms: i64,
    pub progress_percent: Option<f64>,
    pub result_code: String,
    pub message: String,
    pub evidence_json: Option<String>,
}

impl LocalDatabase {
    pub(crate) fn create_vehicle_command(
        &self,
        drone_id: &str,
        command_type: &str,
        parameters_json: &str,
        timeout_ms: Option<i64>,
        now: i64,
    ) -> Result<VehicleCommandSnapshot, String> {
        validate_command_type(command_type)?;
        let parameters = serde_json::from_str::<serde_json::Value>(parameters_json)
            .map_err(|error| format!("command parameters must be valid JSON: {error}"))?;
        validate_command_parameters(command_type, &parameters)?;
        let timeout_ms = timeout_ms
            .unwrap_or(DEFAULT_COMMAND_TIMEOUT_MS)
            .clamp(1_000, MAX_COMMAND_TIMEOUT_MS);
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        validate_command_policy(&connection, drone_id, command_type, &parameters, now)?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin vehicle command creation: {error}"))?;
        let id = generate_id(&tx)?;
        tx.execute(
            r#"
            INSERT INTO vehicle_commands (
                id, drone_id, command_type, parameters_json, status,
                idempotency_key, created_at_unix_ms, deadline_at_unix_ms,
                updated_at_unix_ms
            ) VALUES (?1, ?2, ?3, ?4, 'created', ?1, ?5, ?6, ?5)
            "#,
            params![
                id,
                drone_id,
                command_type,
                parameters_json,
                now,
                now + timeout_ms
            ],
        )
        .map_err(|error| format!("insert vehicle command: {error}"))?;
        if command_type == "geolocate_selected_track" {
            perception_geolocations::insert_request(&tx, &id, drone_id, &parameters, now)?;
        }
        insert_event(
            &tx,
            &generate_id(&tx)?,
            &id,
            "created",
            "created",
            None,
            "atlas_native",
            now,
            None,
            "",
            "Command created",
            None,
        )?;
        tx.commit()
            .map_err(|error| format!("commit vehicle command creation: {error}"))?;
        drop(connection);
        self.vehicle_command(&id)
    }

    pub(crate) fn mark_vehicle_command_sent(
        &self,
        command_id: &str,
        communication_link_id: &str,
        now: i64,
    ) -> Result<VehicleCommandSnapshot, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin vehicle command delivery: {error}"))?;
        let current = command_status(&tx, command_id)?;
        if !matches!(current.as_str(), "created" | "sent") {
            return Err(format!("command in {current} state cannot be delivered"));
        }
        tx.execute(
            r#"
            UPDATE vehicle_commands
            SET status = 'sent', updated_at_unix_ms = ?2,
                delivery_attempts = delivery_attempts + 1,
                last_communication_link_id = ?3
            WHERE id = ?1
            "#,
            params![command_id, now, communication_link_id],
        )
        .map_err(|error| format!("mark vehicle command sent: {error}"))?;
        insert_event(
            &tx,
            &generate_id(&tx)?,
            command_id,
            "sent",
            "sent",
            Some(communication_link_id),
            "atlas_native",
            now,
            None,
            "",
            "Command sent to Atlas Agent",
            None,
        )?;
        tx.commit()
            .map_err(|error| format!("commit vehicle command delivery: {error}"))?;
        drop(connection);
        self.vehicle_command(command_id)
    }

    pub(crate) fn apply_vehicle_command_update(
        &self,
        input: &VehicleCommandUpdateInput,
    ) -> Result<VehicleCommandSnapshot, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin vehicle command update: {error}"))?;
        let duplicate: bool = tx
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM vehicle_command_events WHERE id = ?1)",
                [&input.event_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("check command update idempotency: {error}"))?;
        if duplicate {
            drop(tx);
            drop(connection);
            return self.vehicle_command(&input.command_id);
        }
        let current = command_status(&tx, &input.command_id)?;
        if is_terminal(&current) {
            drop(tx);
            drop(connection);
            return self.vehicle_command(&input.command_id);
        }
        let state = input.next_state.as_deref().unwrap_or(&current);
        if let Some(next) = input.next_state.as_deref() {
            validate_transition(&current, next)?;
            let terminal = is_terminal(next);
            tx.execute(
                r#"
                UPDATE vehicle_commands
                SET status = ?2, updated_at_unix_ms = ?3,
                    accepted_at_unix_ms = CASE WHEN ?2 = 'accepted' THEN COALESCE(accepted_at_unix_ms, ?3) ELSE accepted_at_unix_ms END,
                    started_at_unix_ms = CASE WHEN ?2 = 'executing' THEN COALESCE(started_at_unix_ms, ?3) ELSE started_at_unix_ms END,
                    completed_at_unix_ms = CASE WHEN ?4 THEN ?3 ELSE completed_at_unix_ms END,
                    result_code = CASE WHEN ?5 = '' THEN result_code ELSE ?5 END,
                    result_message = CASE WHEN ?6 = '' THEN result_message ELSE ?6 END,
                    evidence_json = COALESCE(?7, evidence_json)
                WHERE id = ?1
                "#,
                params![
                    input.command_id,
                    next,
                    input.occurred_at_unix_ms,
                    terminal,
                    input.result_code,
                    input.message,
                    input.evidence_json,
                ],
            )
            .map_err(|error| format!("update vehicle command state: {error}"))?;
            if terminal {
                perception_geolocations::resolve_from_command_update(
                    &tx,
                    &input.command_id,
                    next,
                    &input.result_code,
                    &input.message,
                    input.evidence_json.as_deref(),
                    input.occurred_at_unix_ms,
                )?;
            }
        }
        insert_event(
            &tx,
            &input.event_id,
            &input.command_id,
            &input.event_type,
            state,
            Some(&input.communication_link_id),
            "atlas_agent",
            input.occurred_at_unix_ms,
            input.progress_percent,
            &input.result_code,
            &input.message,
            input.evidence_json.as_deref(),
        )?;
        tx.commit()
            .map_err(|error| format!("commit vehicle command update: {error}"))?;
        drop(connection);
        self.vehicle_command(&input.command_id)
    }

    pub(crate) fn record_vehicle_command_cancellation_requested(
        &self,
        command_id: &str,
        now: i64,
    ) -> Result<VehicleCommandSnapshot, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin command cancellation: {error}"))?;
        let current = command_status(&tx, command_id)?;
        if is_terminal(&current) {
            return Err(format!("command in {current} state cannot be cancelled"));
        }
        insert_event(
            &tx,
            &generate_id(&tx)?,
            command_id,
            "cancellation_requested",
            &current,
            None,
            "atlas_native",
            now,
            None,
            "",
            "Cancellation requested",
            None,
        )?;
        tx.commit()
            .map_err(|error| format!("commit command cancellation: {error}"))?;
        drop(connection);
        self.vehicle_command(command_id)
    }

    pub(crate) fn deliverable_vehicle_commands(
        &self,
        drone_id: &str,
        now: i64,
    ) -> Result<Vec<VehicleCommandSnapshot>, String> {
        self.expire_vehicle_commands(now)?;
        let ids = {
            let connection = self
                .connection
                .lock()
                .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
            let mut statement = connection.prepare("SELECT id FROM vehicle_commands WHERE drone_id = ?1 AND status IN ('created', 'sent') AND deadline_at_unix_ms >= ?2 ORDER BY created_at_unix_ms").map_err(|error| format!("prepare deliverable commands: {error}"))?;
            let rows = statement
                .query_map(params![drone_id, now], |row| row.get::<_, String>(0))
                .map_err(|error| format!("query deliverable commands: {error}"))?
                .collect::<Result<Vec<_>, _>>()
                .map_err(|error| format!("read deliverable commands: {error}"))?;
            rows
        };
        ids.iter().map(|id| self.vehicle_command(id)).collect()
    }

    pub(crate) fn vehicle_command(
        &self,
        command_id: &str,
    ) -> Result<VehicleCommandSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        read_command(&connection, command_id)
    }

    pub(crate) fn vehicle_command_history(
        &self,
        drone_id: &str,
        limit: usize,
    ) -> Result<Vec<VehicleCommandSnapshot>, String> {
        self.expire_vehicle_commands(super::unix_time_ms())?;
        let ids = {
            let connection = self
                .connection
                .lock()
                .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
            let mut statement = connection.prepare("SELECT id FROM vehicle_commands WHERE drone_id = ?1 ORDER BY created_at_unix_ms DESC LIMIT ?2").map_err(|error| format!("prepare vehicle command history: {error}"))?;
            let rows = statement
                .query_map(params![drone_id, limit.clamp(1, 200) as i64], |row| {
                    row.get::<_, String>(0)
                })
                .map_err(|error| format!("query vehicle command history: {error}"))?
                .collect::<Result<Vec<_>, _>>()
                .map_err(|error| format!("read vehicle command history: {error}"))?;
            rows
        };
        ids.iter().map(|id| self.vehicle_command(id)).collect()
    }

    pub(crate) fn expire_vehicle_commands(&self, now: i64) -> Result<(), String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin command expiration: {error}"))?;
        let ids = {
            let mut statement = tx
                .prepare("SELECT id, status FROM vehicle_commands WHERE status IN ('created', 'sent', 'accepted', 'executing') AND deadline_at_unix_ms < ?1")
                .map_err(|error| format!("prepare expired commands: {error}"))?;
            let rows = statement
                .query_map([now], |row| {
                    Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
                })
                .map_err(|error| format!("query expired commands: {error}"))?
                .collect::<Result<Vec<_>, _>>()
                .map_err(|error| format!("read expired commands: {error}"))?;
            rows
        };
        for (id, previous_state) in ids {
            let (result_code, message) =
                if matches!(previous_state.as_str(), "accepted" | "executing") {
                    (
                        "COMMAND_EXECUTION_DEADLINE_EXCEEDED",
                        "Agent or MAVSDK did not complete the command before its deadline",
                    )
                } else {
                    (
                        "DELIVERY_DEADLINE_EXCEEDED",
                        "Command expired before agent acceptance",
                    )
                };
            tx.execute(
                "UPDATE vehicle_commands SET status = 'timed_out', updated_at_unix_ms = ?2, completed_at_unix_ms = ?2, result_code = ?3, result_message = ?4 WHERE id = ?1",
                params![id, now, result_code, message],
            )
            .map_err(|error| format!("expire vehicle command: {error}"))?;
            perception_geolocations::resolve_from_command_update(
                &tx,
                &id,
                "timed_out",
                result_code,
                message,
                None,
                now,
            )?;
            insert_event(
                &tx,
                &generate_id(&tx)?,
                &id,
                "timed_out",
                "timed_out",
                None,
                "atlas_native",
                now,
                None,
                result_code,
                message,
                None,
            )?;
        }
        tx.commit()
            .map_err(|error| format!("commit command expiration: {error}"))
    }
}

fn read_command(
    connection: &rusqlite::Connection,
    id: &str,
) -> Result<VehicleCommandSnapshot, String> {
    let mut command = connection.query_row("SELECT id, drone_id, command_type, parameters_json, status, idempotency_key, created_at_unix_ms, deadline_at_unix_ms, updated_at_unix_ms, accepted_at_unix_ms, started_at_unix_ms, completed_at_unix_ms, delivery_attempts, last_communication_link_id, result_code, result_message, evidence_json FROM vehicle_commands WHERE id = ?1", [id], |row| Ok(VehicleCommandSnapshot { id: row.get(0)?, drone_id: row.get(1)?, command_type: row.get(2)?, parameters_json: row.get(3)?, status: row.get(4)?, idempotency_key: row.get(5)?, created_at_unix_ms: row.get(6)?, deadline_at_unix_ms: row.get(7)?, updated_at_unix_ms: row.get(8)?, accepted_at_unix_ms: row.get(9)?, started_at_unix_ms: row.get(10)?, completed_at_unix_ms: row.get(11)?, delivery_attempts: row.get(12)?, last_communication_link_id: row.get(13)?, result_code: row.get(14)?, result_message: row.get(15)?, evidence_json: row.get(16)?, events: Vec::new() })).optional().map_err(|error| format!("read vehicle command: {error}"))?.ok_or_else(|| format!("vehicle command {id} was not found"))?;
    let mut statement = connection.prepare("SELECT id, sequence, event_type, state, communication_link_id, source, occurred_at_unix_ms, progress_percent, result_code, message, evidence_json FROM vehicle_command_events WHERE vehicle_command_id = ?1 ORDER BY sequence").map_err(|error| format!("prepare vehicle command events: {error}"))?;
    command.events = statement
        .query_map([id], |row| {
            Ok(VehicleCommandEventSnapshot {
                id: row.get(0)?,
                sequence: row.get(1)?,
                event_type: row.get(2)?,
                state: row.get(3)?,
                communication_link_id: row.get(4)?,
                source: row.get(5)?,
                occurred_at_unix_ms: row.get(6)?,
                progress_percent: row.get(7)?,
                result_code: row.get(8)?,
                message: row.get(9)?,
                evidence_json: row.get(10)?,
            })
        })
        .map_err(|error| format!("query vehicle command events: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read vehicle command events: {error}"))?;
    Ok(command)
}

#[allow(clippy::too_many_arguments)]
fn insert_event(
    tx: &Transaction<'_>,
    id: &str,
    command_id: &str,
    event_type: &str,
    state: &str,
    link_id: Option<&str>,
    source: &str,
    occurred_at: i64,
    progress: Option<f64>,
    result_code: &str,
    message: &str,
    evidence: Option<&str>,
) -> Result<(), String> {
    let sequence: i64 = tx.query_row("SELECT COALESCE(max(sequence), -1) + 1 FROM vehicle_command_events WHERE vehicle_command_id = ?1", [command_id], |row| row.get(0)).map_err(|error| format!("select next command event sequence: {error}"))?;
    tx.execute("INSERT INTO vehicle_command_events (id, vehicle_command_id, sequence, event_type, state, communication_link_id, source, occurred_at_unix_ms, progress_percent, result_code, message, evidence_json) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)", params![id, command_id, sequence, event_type, state, link_id, source, occurred_at, progress, result_code, message, evidence]).map_err(|error| format!("insert vehicle command event: {error}"))?;
    Ok(())
}

fn command_status(tx: &Transaction<'_>, id: &str) -> Result<String, String> {
    tx.query_row(
        "SELECT status FROM vehicle_commands WHERE id = ?1",
        [id],
        |row| row.get(0),
    )
    .optional()
    .map_err(|error| format!("read vehicle command state: {error}"))?
    .ok_or_else(|| format!("vehicle command {id} was not found"))
}

fn validate_command_type(value: &str) -> Result<(), String> {
    match value {
        "hold"
        | "return_to_launch"
        | "land"
        | "gimbal_set_angles"
        | "gimbal_set_rates"
        | "gimbal_center"
        | "gimbal_set_roi"
        | "camera_set_zoom"
        | "payload_control_begin"
        | "payload_control_renew"
        | "payload_control_end"
        | "gimbal_follow_start"
        | "gimbal_follow_stop"
        | "geolocate_selected_track" => Ok(()),
        _ => Err("unsupported vehicle command type".to_string()),
    }
}

fn validate_command_parameters(
    command_type: &str,
    value: &serde_json::Value,
) -> Result<(), String> {
    let number = |key: &str| {
        value
            .get(key)
            .and_then(serde_json::Value::as_f64)
            .ok_or_else(|| format!("{key} must be a number"))
    };
    let gimbal_id = value
        .get("gimbalId")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or(0);
    if gimbal_id < 0 || gimbal_id > i32::MAX as i64 {
        return Err("gimbalId must be a non-negative integer".into());
    }
    let frame = value
        .get("yawFrame")
        .and_then(serde_json::Value::as_str)
        .unwrap_or("AIRCRAFT_RELATIVE");
    if !matches!(frame, "AIRCRAFT_RELATIVE" | "NORTH_LOCKED") {
        return Err("yawFrame must be AIRCRAFT_RELATIVE or NORTH_LOCKED".into());
    }
    let payload_command = command_type.starts_with("gimbal_")
        || command_type.starts_with("camera_")
        || command_type.starts_with("payload_control_");
    if payload_command {
        payload_control_context(value)?;
        if value
            .get("controlSessionId")
            .and_then(serde_json::Value::as_str)
            .is_none_or(str::is_empty)
        {
            return Err("controlSessionId must be a non-empty string".to_string());
        }
    }
    match command_type {
        "payload_control_begin" | "payload_control_renew" => {
            let lease = value
                .get("leaseDurationMs")
                .and_then(serde_json::Value::as_i64)
                .ok_or_else(|| "leaseDurationMs must be an integer".to_string())?;
            if !(3_000..=15_000).contains(&lease) {
                return Err("leaseDurationMs must be between 3000 and 15000".into());
            }
        }
        "gimbal_set_angles" => {
            let pitch = number("pitchDegrees")?;
            let yaw = number("yawDegrees")?;
            if !(-90.0..=30.0).contains(&pitch) {
                return Err("pitchDegrees must be between -90 and 30".into());
            }
            if !(-180.0..=180.0).contains(&yaw) {
                return Err("yawDegrees must be between -180 and 180".into());
            }
        }
        "gimbal_set_rates" => {
            let pitch = number("pitchRateDegreesPerSecond")?;
            let yaw = number("yawRateDegreesPerSecond")?;
            if !(-90.0..=90.0).contains(&pitch) || !(-90.0..=90.0).contains(&yaw) {
                return Err("gimbal rates must be between -90 and 90 degrees per second".into());
            }
        }
        "gimbal_follow_start" => {
            for key in ["sourceId", "trackSessionId", "trackId"] {
                if value
                    .get(key)
                    .and_then(serde_json::Value::as_str)
                    .is_none_or(str::is_empty)
                {
                    return Err(format!("{key} must be a non-empty string"));
                }
            }
        }
        "geolocate_selected_track" => {
            for key in [
                "selectionId",
                "sourceId",
                "trackSessionId",
                "trackId",
                "groundAltitudeSource",
                "groundAltitudeSourceVersion",
                "requestedBy",
            ] {
                let value = value
                    .get(key)
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or_default();
                if value.trim().is_empty() || value.len() > 240 {
                    return Err(format!(
                        "{key} must be a non-empty string of at most 240 characters"
                    ));
                }
            }
            let aim_point = value
                .get("aimPoint")
                .and_then(serde_json::Value::as_str)
                .unwrap_or_default();
            if !matches!(aim_point, "GROUND_CONTACT" | "TARGET_CENTER") {
                return Err("aimPoint must be GROUND_CONTACT or TARGET_CENTER".into());
            }
            let ground_altitude = number("groundAltitudeAmslMeters")?;
            let ground_uncertainty = number("groundAltitudeUncertaintyMeters")?;
            if !(-500.0..=9_000.0).contains(&ground_altitude) {
                return Err("groundAltitudeAmslMeters must be between -500 and 9000".into());
            }
            if !(0.0..=100.0).contains(&ground_uncertainty) {
                return Err("groundAltitudeUncertaintyMeters must be between 0 and 100".into());
            }
            let resolved_at = value
                .get("groundAltitudeResolvedAtUnixMs")
                .and_then(serde_json::Value::as_i64)
                .unwrap_or_default();
            if resolved_at <= 0 {
                return Err("groundAltitudeResolvedAtUnixMs must be a positive integer".into());
            }
            let aim_height = number("assumedAimPointHeightMeters")?;
            let aim_height_uncertainty = number("assumedAimPointHeightUncertaintyMeters")?;
            if aim_point == "GROUND_CONTACT" {
                if aim_height != 0.0 || aim_height_uncertainty != 0.0 {
                    return Err("GROUND_CONTACT cannot include an assumed aim-point height".into());
                }
            } else if !(0.0..=100.0).contains(&aim_height)
                || aim_height == 0.0
                || !(0.0..=100.0).contains(&aim_height_uncertainty)
                || aim_height_uncertainty == 0.0
            {
                return Err("TARGET_CENTER requires positive aim-point height and uncertainty at or below 100 metres".into());
            }
        }
        "gimbal_set_roi" => {
            let latitude = number("latitude")?;
            let longitude = number("longitude")?;
            let altitude = number("altitudeAmslMeters")?;
            if !(-90.0..=90.0).contains(&latitude) {
                return Err("latitude must be between -90 and 90".into());
            }
            if !(-180.0..=180.0).contains(&longitude) {
                return Err("longitude must be between -180 and 180".into());
            }
            if !(-500.0..=20_000.0).contains(&altitude) {
                return Err("altitudeAmslMeters must be between -500 and 20000".into());
            }
        }
        "camera_set_zoom" => {
            let zoom = number("zoomPercent")?;
            if !(0.0..=100.0).contains(&zoom) {
                return Err("zoomPercent must be between 0 and 100".into());
            }
            let camera_id = value
                .get("cameraComponentId")
                .and_then(serde_json::Value::as_i64)
                .unwrap_or(0);
            if camera_id < 0 || camera_id > i32::MAX as i64 {
                return Err("cameraComponentId must be a non-negative integer".into());
            }
        }
        _ => {}
    }
    Ok(())
}

fn validate_command_policy(
    connection: &rusqlite::Connection,
    drone_id: &str,
    command_type: &str,
    parameters: &serde_json::Value,
    now: i64,
) -> Result<(), String> {
    let link_ready: bool = connection
        .query_row(
            r#"
            SELECT EXISTS(
                SELECT 1
                FROM communication_links l
                JOIN vehicle_agent_bindings b ON b.id = l.vehicle_agent_binding_id
                JOIN drones d ON d.id = b.drone_id
                WHERE b.drone_id = ?1
                  AND b.status = 'active'
                  AND d.status = 'active'
                  AND l.status = 'connected'
                  AND l.ended_at_unix_ms IS NULL
                  AND l.last_heartbeat_at_unix_ms >= ?2
            )
            "#,
            params![drone_id, now - 15_000],
            |row| row.get(0),
        )
        .map_err(|error| format!("check command communication link: {error}"))?;
    if !link_ready {
        return Err("a fresh connected communication link is required".to_string());
    }
    if command_type == "geolocate_selected_track" {
        let selection_id = parameters["selectionId"].as_str().unwrap_or_default();
        let source_id = parameters["sourceId"].as_str().unwrap_or_default();
        let track_session_id = parameters["trackSessionId"].as_str().unwrap_or_default();
        let track_id = parameters["trackId"].as_str().unwrap_or_default();
        let selected: bool = connection
            .query_row(
                r#"
                SELECT EXISTS(
                    SELECT 1
                    FROM perception_track_selections selections
                    JOIN perception_tracks tracks ON tracks.id = selections.track_id
                    JOIN perception_track_sessions sessions ON sessions.id = selections.track_session_id
                    WHERE selections.id = ?1
                      AND selections.drone_id = ?2
                      AND selections.status = 'SELECTED'
                      AND selections.track_session_id = ?3
                      AND selections.track_id = ?4
                      AND sessions.source_id = ?5
                      AND tracks.lifecycle_state = 'ACTIVE'
                )
                "#,
                params![selection_id, drone_id, track_session_id, track_id, source_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("check selected-track geolocation policy: {error}"))?;
        if !selected {
            return Err(
                "geolocation requires the exact currently selected ACTIVE track, selection, and source"
                    .into(),
            );
        }
        let resolved_at = parameters["groundAltitudeResolvedAtUnixMs"]
            .as_i64()
            .unwrap_or_default();
        if resolved_at > now + 300_000 {
            return Err("ground altitude provenance cannot be dated in the future".into());
        }
    }
    if command_type.starts_with("gimbal_")
        || command_type.starts_with("camera_")
        || command_type.starts_with("payload_control_")
    {
        match payload_control_context(parameters)? {
            PayloadControlContext::MissionOverride(run_id) => {
                let run_ready: bool = connection
                    .query_row(
                        "SELECT EXISTS(SELECT 1 FROM mission_runs WHERE id = ?1 AND drone_id = ?2 AND status IN ('RUNNING', 'PAUSED'))",
                        params![run_id, drone_id],
                        |row| row.get(0),
                    )
                    .map_err(|error| format!("check payload mission policy: {error}"))?;
                if !run_ready {
                    return Err(
                        "mission override requires this drone's RUNNING or PAUSED mission run"
                            .into(),
                    );
                }
            }
            PayloadControlContext::Inspection => {
                let telemetry = connection
                    .query_row(
                        "SELECT received_at_unix_ms, armed, in_air FROM vehicle_telemetry_current WHERE drone_id = ?1",
                        [drone_id],
                        |row| {
                            Ok((
                                row.get::<_, i64>(0)?,
                                row.get::<_, Option<bool>>(1)?,
                                row.get::<_, Option<bool>>(2)?,
                            ))
                        },
                    )
                    .optional()
                    .map_err(|error| format!("read inspection telemetry policy state: {error}"))?
                    .ok_or_else(|| {
                        "live telemetry is required before taking inspection control".to_string()
                    })?;
                if now - telemetry.0 > 5_000 {
                    return Err(
                        "live telemetry is required before taking inspection control".into(),
                    );
                }
                if telemetry.1 != Some(false) || telemetry.2 != Some(false) {
                    return Err(
                        "inspection control requires the aircraft to be explicitly disarmed and on the ground"
                            .into(),
                    );
                }
            }
        }
        if command_type == "gimbal_follow_start" {
            let source_id = parameters["sourceId"].as_str().unwrap_or_default();
            let track_session_id = parameters["trackSessionId"].as_str().unwrap_or_default();
            let track_id = parameters["trackId"].as_str().unwrap_or_default();
            let selected: bool = connection
                .query_row(
                    r#"
                    SELECT EXISTS(
                        SELECT 1
                        FROM perception_track_selections selections
                        JOIN perception_tracks tracks ON tracks.id = selections.track_id
                        JOIN perception_track_sessions sessions ON sessions.id = selections.track_session_id
                        WHERE selections.drone_id = ?1
                          AND selections.status = 'SELECTED'
                          AND selections.track_session_id = ?2
                          AND selections.track_id = ?3
                          AND sessions.source_id = ?4
                          AND tracks.lifecycle_state = 'ACTIVE'
                    )
                    "#,
                    params![drone_id, track_session_id, track_id, source_id],
                    |row| row.get(0),
                )
                .map_err(|error| format!("check operator-selected track policy: {error}"))?;
            if !selected {
                return Err(
                    "gimbal follow requires the exact currently selected ACTIVE track and source"
                        .into(),
                );
            }
        }
        return Ok(());
    }
    let telemetry = connection
        .query_row(
            r#"
            SELECT received_at_unix_ms, in_air, home_position_set,
                   CAST(json_extract(health_json, '$.globalPositionOk') AS INTEGER),
                   CAST(json_extract(health_json, '$.homePositionOk') AS INTEGER)
            FROM vehicle_telemetry_current
            WHERE drone_id = ?1
            "#,
            [drone_id],
            |row| {
                Ok((
                    row.get::<_, i64>(0)?,
                    row.get::<_, Option<bool>>(1)?,
                    row.get::<_, Option<bool>>(2)?,
                    row.get::<_, Option<bool>>(3)?,
                    row.get::<_, Option<bool>>(4)?,
                ))
            },
        )
        .optional()
        .map_err(|error| format!("read command telemetry policy state: {error}"))?
        .ok_or_else(|| "current telemetry is required".to_string())?;
    if now - telemetry.0 > 5_000 {
        return Err("telemetry must be fresh before issuing a command".to_string());
    }
    if telemetry.1 != Some(true) {
        return Err(format!("{command_type} requires the drone to be in air"));
    }
    if command_type == "return_to_launch"
        && (telemetry.2 != Some(true) || telemetry.3 != Some(true) || telemetry.4 != Some(true))
    {
        return Err("return_to_launch requires GPS and home position readiness".to_string());
    }
    Ok(())
}

fn payload_control_context(value: &serde_json::Value) -> Result<PayloadControlContext<'_>, String> {
    let context = value
        .get("controlContext")
        .and_then(serde_json::Value::as_object)
        .ok_or_else(|| "controlContext must be an object".to_string())?;
    match context
        .get("kind")
        .and_then(serde_json::Value::as_str)
        .unwrap_or_default()
    {
        "inspection" => {
            if context.get("missionRunId").is_some() {
                return Err("inspection controlContext cannot contain missionRunId".into());
            }
            Ok(PayloadControlContext::Inspection)
        }
        "mission_override" => {
            let run_id = context
                .get("missionRunId")
                .and_then(serde_json::Value::as_str)
                .filter(|value| !value.is_empty())
                .ok_or_else(|| {
                    "mission_override controlContext requires a non-empty missionRunId".to_string()
                })?;
            Ok(PayloadControlContext::MissionOverride(run_id))
        }
        _ => Err("controlContext.kind must be inspection or mission_override".into()),
    }
}

fn validate_transition(current: &str, next: &str) -> Result<(), String> {
    let allowed = match current {
        "created" | "sent" => matches!(
            next,
            "accepted" | "rejected" | "failed" | "timed_out" | "cancelled"
        ),
        "accepted" => matches!(next, "executing" | "failed" | "timed_out" | "cancelled"),
        "executing" => matches!(next, "succeeded" | "failed" | "timed_out" | "cancelled"),
        _ => false,
    };
    if allowed {
        Ok(())
    } else {
        Err(format!(
            "invalid vehicle command transition {current} -> {next}"
        ))
    }
}

fn is_terminal(state: &str) -> bool {
    matches!(
        state,
        "succeeded" | "failed" | "rejected" | "timed_out" | "cancelled"
    )
}

fn generate_id(tx: &Transaction<'_>) -> Result<String, String> {
    tx.query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate command identifier: {error}"))
}

#[cfg(test)]
mod parameter_tests {
    use super::validate_command_parameters;
    use serde_json::json;

    #[test]
    fn gimbal_yaw_frame_is_explicit_and_angles_are_bounded() {
        assert!(validate_command_parameters(
            "gimbal_set_angles",
            &json!({"controlContext":{"kind":"mission_override","missionRunId":"run-1"},"controlSessionId":"session-1","pitchDegrees": -45, "yawDegrees": 30, "yawFrame": "AIRCRAFT_RELATIVE"})
        )
        .is_ok());
        assert!(validate_command_parameters(
            "gimbal_set_angles",
            &json!({"controlContext":{"kind":"mission_override","missionRunId":"run-1"},"controlSessionId":"session-1","pitchDegrees": -100, "yawDegrees": 30})
        )
        .is_err());
        assert!(validate_command_parameters(
            "gimbal_set_angles",
            &json!({"controlContext":{"kind":"mission_override","missionRunId":"run-1"},"controlSessionId":"session-1","pitchDegrees": -45, "yawDegrees": 30, "yawFrame": "AIRCRAFT_YAW"})
        )
        .is_err());
    }

    #[test]
    fn payload_roi_zoom_and_lease_are_bounded() {
        let identity = json!({"controlContext":{"kind":"mission_override","missionRunId":"run-1"},"controlSessionId":"session-1"});
        let mut begin = identity.clone();
        begin["leaseDurationMs"] = json!(7000);
        assert!(validate_command_parameters("payload_control_begin", &begin).is_ok());
        begin["leaseDurationMs"] = json!(1000);
        assert!(validate_command_parameters("payload_control_begin", &begin).is_err());

        let mut roi = identity.clone();
        roi["latitude"] = json!(51.0);
        roi["longitude"] = json!(-0.1);
        roi["altitudeAmslMeters"] = json!(35);
        assert!(validate_command_parameters("gimbal_set_roi", &roi).is_ok());

        let mut zoom = identity;
        zoom["zoomPercent"] = json!(100);
        assert!(validate_command_parameters("camera_set_zoom", &zoom).is_ok());
        zoom["zoomPercent"] = json!(101);
        assert!(validate_command_parameters("camera_set_zoom", &zoom).is_err());
    }

    #[test]
    fn payload_context_is_a_strict_discriminated_union() {
        let inspection = json!({
            "controlContext":{"kind":"inspection"},
            "controlSessionId":"session-1",
            "leaseDurationMs":7000
        });
        assert!(validate_command_parameters("payload_control_begin", &inspection).is_ok());

        let inspection_with_run = json!({
            "controlContext":{"kind":"inspection","missionRunId":"run-1"},
            "controlSessionId":"session-1",
            "leaseDurationMs":7000
        });
        assert!(
            validate_command_parameters("payload_control_begin", &inspection_with_run).is_err()
        );

        let missing_run = json!({
            "controlContext":{"kind":"mission_override"},
            "controlSessionId":"session-1",
            "leaseDurationMs":7000
        });
        assert!(validate_command_parameters("payload_control_begin", &missing_run).is_err());
    }

    #[test]
    fn gimbal_follow_requires_an_exact_track_identity() {
        let valid = json!({
            "controlContext":{"kind":"mission_override","missionRunId":"run-1"},
            "controlSessionId":"follow-1",
            "sourceId":"camera-main",
            "trackSessionId":"track-session-1",
            "trackId":"atlas:track-session-1:1"
        });
        assert!(validate_command_parameters("gimbal_follow_start", &valid).is_ok());
        let mut missing_track = valid;
        missing_track["trackId"] = json!("");
        assert!(validate_command_parameters("gimbal_follow_start", &missing_track).is_err());
    }
}
