use std::f64::consts::PI;

use rusqlite::{params, OptionalExtension, Row, Transaction};
use serde::{Deserialize, Serialize};

use super::LocalDatabase;

const FOLLOW_LEASE_MIN_MS: i64 = 1_000;
const FOLLOW_LEASE_MAX_MS: i64 = 5_000;
const FOLLOW_START_TARGET_MAX_AGE_MS: i64 = 5_000;
const FOLLOW_RUNTIME_TARGET_MAX_AGE_MS: i64 = 2_500;
const TELEMETRY_MAX_AGE_MS: i64 = 5_000;

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct CreateAircraftFollowSessionInput {
    pub drone_id: String,
    pub geolocation_id: String,
    pub requested_by: String,
    pub reviewed_by: String,
    pub operator_review_note: String,
    pub standoff_m: f64,
    pub altitude_relative_m: f64,
    pub minimum_altitude_relative_m: f64,
    pub maximum_altitude_relative_m: f64,
    pub maximum_ground_speed_mps: f64,
    pub maximum_acceleration_mps2: f64,
    pub maximum_duration_seconds: u32,
    pub boundary_center_latitude: f64,
    pub boundary_center_longitude: f64,
    pub boundary_radius_m: f64,
    pub minimum_battery_percent: f64,
    pub minimum_track_confidence: f64,
    pub maximum_geolocation_uncertainty_m: f64,
    pub maximum_velocity_uncertainty_mps: f64,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct RenewAircraftFollowSessionInput {
    pub session_id: String,
    pub geolocation_id: String,
    pub lease_duration_ms: i64,
    pub actor: String,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct EndAircraftFollowSessionInput {
    pub session_id: String,
    pub reason: String,
    pub actor: String,
}

#[derive(Debug, Clone)]
pub(crate) struct AircraftFollowAgentUpdateInput {
    pub event_id: String,
    pub operation_id: String,
    pub session_id: String,
    pub state: String,
    pub observed_at_unix_ms: i64,
    pub reason_code: String,
    pub message: String,
    pub evidence_json: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct AircraftFollowTargetSnapshot {
    pub geolocation_id: String,
    pub drone_id: String,
    pub selection_id: String,
    pub source_id: String,
    pub track_session_id: String,
    pub track_id: String,
    pub observed_at_unix_ms: i64,
    pub latitude: f64,
    pub longitude: f64,
    pub altitude_amsl_m: f64,
    pub velocity_north_mps: f64,
    pub velocity_east_mps: f64,
    pub horizontal_uncertainty_m: f64,
    pub velocity_uncertainty_mps: f64,
    pub track_confidence: f64,
    pub lifecycle_state: String,
    pub motion_status: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct AircraftFollowEventSnapshot {
    pub id: String,
    pub sequence: u32,
    pub event_type: String,
    pub state: String,
    pub source: String,
    pub operation_id: String,
    pub reason_code: String,
    pub message: String,
    pub evidence: serde_json::Value,
    pub occurred_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct AircraftFollowSessionSnapshot {
    pub id: String,
    pub drone_id: String,
    pub selection_id: String,
    pub track_session_id: String,
    pub track_id: String,
    pub source_id: String,
    pub state: String,
    pub requested_by: String,
    pub reviewed_by: String,
    pub operator_review_note: String,
    pub requested_at_unix_ms: i64,
    pub authorized_at_unix_ms: Option<i64>,
    pub started_at_unix_ms: Option<i64>,
    pub ended_at_unix_ms: Option<i64>,
    pub standoff_m: f64,
    pub altitude_relative_m: f64,
    pub minimum_altitude_relative_m: f64,
    pub maximum_altitude_relative_m: f64,
    pub maximum_ground_speed_mps: f64,
    pub maximum_acceleration_mps2: f64,
    pub maximum_duration_ms: i64,
    pub boundary_center_latitude: f64,
    pub boundary_center_longitude: f64,
    pub boundary_radius_m: f64,
    pub minimum_battery_percent: f64,
    pub minimum_track_confidence: f64,
    pub maximum_geolocation_uncertainty_m: f64,
    pub maximum_velocity_uncertainty_mps: f64,
    pub latest_geolocation_id: String,
    pub latest_target_observed_at_unix_ms: i64,
    pub operator_lease_expires_at_unix_ms: Option<i64>,
    pub last_agent_update_at_unix_ms: Option<i64>,
    pub validation_reference: String,
    pub boresight_reference: String,
    pub boresight_error_bound_deg: f64,
    pub exit_reason_code: String,
    pub exit_reason: String,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
    pub target: AircraftFollowTargetSnapshot,
    pub events: Vec<AircraftFollowEventSnapshot>,
}

#[derive(Debug, Clone)]
pub(crate) struct AircraftFollowWatchdogAction {
    pub session: AircraftFollowSessionSnapshot,
    pub reason_code: String,
    pub reason: String,
}

impl LocalDatabase {
    pub(crate) fn create_aircraft_follow_session(
        &self,
        input: &CreateAircraftFollowSessionInput,
        now: i64,
    ) -> Result<AircraftFollowSessionSnapshot, String> {
        validate_create_input(input)?;
        let aircraft = self.operations_snapshot_for(Some(&input.drone_id))?;
        let validation_reference = validate_aircraft_readiness(
            &aircraft,
            input.minimum_battery_percent,
            input.minimum_altitude_relative_m,
            input.maximum_altitude_relative_m,
            now,
        )?;
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin aircraft follow request: {error}"))?;
        let unfinished_mission: bool = tx
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM mission_runs WHERE drone_id = ?1 AND status NOT IN ('COMPLETED', 'FAILED', 'CANCELLED', 'RTL'))",
                [&input.drone_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("check active mission before aircraft follow: {error}"))?;
        if unfinished_mission {
            return Err(
                "Follow from standoff is blocked while the aircraft has an unfinished mission run"
                    .into(),
            );
        }
        let target = read_validated_target(&tx, &input.geolocation_id)?;
        validate_target_for_envelope(
            &target,
            &input.drone_id,
            input.minimum_track_confidence,
            input.maximum_geolocation_uncertainty_m,
            input.maximum_velocity_uncertainty_mps,
            input.boundary_center_latitude,
            input.boundary_center_longitude,
            input.boundary_radius_m,
            now,
            FOLLOW_START_TARGET_MAX_AGE_MS,
        )?;
        let (boresight_reference, boresight_error_bound_deg) =
            verified_boresight(&tx, &input.geolocation_id)?;
        let id = generate_id(&tx)?;
        tx.execute(
            r#"
            INSERT INTO aircraft_follow_sessions (
                id, drone_id, selection_id, track_session_id, track_id, source_id,
                state, requested_by, reviewed_by, operator_review_note,
                requested_at_unix_ms, standoff_m, altitude_relative_m,
                minimum_altitude_relative_m, maximum_altitude_relative_m,
                maximum_ground_speed_mps, maximum_acceleration_mps2,
                maximum_duration_ms, boundary_center_latitude,
                boundary_center_longitude, boundary_radius_m,
                minimum_battery_percent, minimum_track_confidence,
                maximum_geolocation_uncertainty_m,
                maximum_velocity_uncertainty_mps, latest_geolocation_id,
                latest_target_observed_at_unix_ms, validation_reference,
                boresight_reference, boresight_error_bound_deg,
                created_at_unix_ms, updated_at_unix_ms
            ) VALUES (
                ?1, ?2, ?3, ?4, ?5, ?6, 'REQUESTED', ?7, ?8, ?9, ?10,
                ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18, ?19, ?20, ?21,
                ?22, ?23, ?24, ?25, ?26, ?27, ?28, ?29, ?10, ?10
            )
            "#,
            params![
                id,
                input.drone_id,
                target.selection_id,
                target.track_session_id,
                target.track_id,
                target.source_id,
                input.requested_by.trim(),
                input.reviewed_by.trim(),
                input.operator_review_note.trim(),
                now,
                input.standoff_m,
                input.altitude_relative_m,
                input.minimum_altitude_relative_m,
                input.maximum_altitude_relative_m,
                input.maximum_ground_speed_mps,
                input.maximum_acceleration_mps2,
                input.maximum_duration_seconds as i64 * 1_000,
                input.boundary_center_latitude,
                input.boundary_center_longitude,
                input.boundary_radius_m,
                input.minimum_battery_percent,
                input.minimum_track_confidence,
                input.maximum_geolocation_uncertainty_m,
                input.maximum_velocity_uncertainty_mps,
                input.geolocation_id,
                target.observed_at_unix_ms,
                validation_reference,
                boresight_reference,
                boresight_error_bound_deg,
            ],
        )
        .map_err(|error| format!("insert aircraft follow session: {error}"))?;
        insert_target_update(&tx, &id, &target, now)?;
        insert_event(
            &tx,
            &id,
            "REQUESTED",
            "REQUESTED",
            "operator",
            "",
            "",
            "Operator reviewed and requested a bounded standoff follow",
            &serde_json::json!({
                "geolocationId": input.geolocation_id,
                "reviewNote": input.operator_review_note,
            }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit aircraft follow request: {error}"))?;
        drop(connection);
        self.aircraft_follow_session(&id)
    }

    pub(crate) fn authorize_aircraft_follow_session(
        &self,
        session_id: &str,
        lease_duration_ms: i64,
        now: i64,
    ) -> Result<AircraftFollowSessionSnapshot, String> {
        let lease_duration_ms = validate_lease(lease_duration_ms)?;
        let current = self.aircraft_follow_session(session_id)?;
        if current.state != "REQUESTED" {
            return Err(format!(
                "aircraft follow session in {} state cannot be authorized",
                current.state
            ));
        }
        let aircraft = self.operations_snapshot_for(Some(&current.drone_id))?;
        validate_aircraft_readiness(
            &aircraft,
            current.minimum_battery_percent,
            current.minimum_altitude_relative_m,
            current.maximum_altitude_relative_m,
            now,
        )?;
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin aircraft follow authorization: {error}"))?;
        let target = read_validated_target(&tx, &current.latest_geolocation_id)?;
        validate_target_for_session(&target, &current, now, FOLLOW_START_TARGET_MAX_AGE_MS)?;
        tx.execute(
            "UPDATE aircraft_follow_sessions SET state = 'VALIDATING', authorized_at_unix_ms = ?2, operator_lease_expires_at_unix_ms = ?3, updated_at_unix_ms = ?2 WHERE id = ?1 AND state = 'REQUESTED'",
            params![session_id, now, now + lease_duration_ms],
        )
        .map_err(|error| format!("authorize aircraft follow session: {error}"))?;
        insert_event(
            &tx,
            session_id,
            "AUTHORIZED",
            "VALIDATING",
            "atlas_native",
            &generate_id(&tx)?,
            "",
            "Native policy accepted the reviewed follow envelope",
            &serde_json::json!({ "leaseDurationMs": lease_duration_ms }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit aircraft follow authorization: {error}"))?;
        drop(connection);
        self.aircraft_follow_session(session_id)
    }

    pub(crate) fn renew_aircraft_follow_session(
        &self,
        input: &RenewAircraftFollowSessionInput,
        now: i64,
    ) -> Result<AircraftFollowSessionSnapshot, String> {
        let lease_duration_ms = validate_lease(input.lease_duration_ms)?;
        if input.actor.trim().is_empty() {
            return Err("aircraft follow lease renewal requires an actor".into());
        }
        let current = self.aircraft_follow_session(&input.session_id)?;
        if !matches!(
            current.state.as_str(),
            "VALIDATING" | "ACQUIRING" | "FOLLOWING"
        ) {
            return Err(format!(
                "aircraft follow session in {} state cannot be renewed",
                current.state
            ));
        }
        let aircraft = self.operations_snapshot_for(Some(&current.drone_id))?;
        validate_aircraft_readiness(
            &aircraft,
            current.minimum_battery_percent,
            current.minimum_altitude_relative_m,
            current.maximum_altitude_relative_m,
            now,
        )?;
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin aircraft follow renewal: {error}"))?;
        let target = read_validated_target(&tx, &input.geolocation_id)?;
        validate_target_for_session(&target, &current, now, FOLLOW_RUNTIME_TARGET_MAX_AGE_MS)?;
        insert_target_update(&tx, &input.session_id, &target, now)?;
        tx.execute(
            "UPDATE aircraft_follow_sessions SET latest_geolocation_id = ?2, latest_target_observed_at_unix_ms = ?3, operator_lease_expires_at_unix_ms = ?4, updated_at_unix_ms = ?5 WHERE id = ?1",
            params![
                input.session_id,
                input.geolocation_id,
                target.observed_at_unix_ms,
                now + lease_duration_ms,
                now
            ],
        )
        .map_err(|error| format!("renew aircraft follow target: {error}"))?;
        insert_event(
            &tx,
            &input.session_id,
            "LEASE_RENEWED",
            &current.state,
            "operator",
            &generate_id(&tx)?,
            "",
            "Operator supervision lease and validated target state renewed",
            &serde_json::json!({
                "geolocationId": input.geolocation_id,
                "leaseDurationMs": lease_duration_ms,
                "actor": input.actor,
            }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit aircraft follow renewal: {error}"))?;
        drop(connection);
        self.aircraft_follow_session(&input.session_id)
    }

    pub(crate) fn record_aircraft_follow_end_requested(
        &self,
        input: &EndAircraftFollowSessionInput,
        now: i64,
    ) -> Result<AircraftFollowSessionSnapshot, String> {
        if input.actor.trim().is_empty() || input.reason.trim().is_empty() {
            return Err("ending aircraft follow requires an actor and explicit reason".into());
        }
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin aircraft follow end request: {error}"))?;
        let state: String = tx
            .query_row(
                "SELECT state FROM aircraft_follow_sessions WHERE id = ?1",
                [&input.session_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("read aircraft follow session for end: {error}"))?
            .ok_or_else(|| format!("aircraft follow session {} was not found", input.session_id))?;
        if state == "ENDED" {
            return Err("aircraft follow session has already ended".into());
        }
        tx.execute(
            "UPDATE aircraft_follow_sessions SET operator_lease_expires_at_unix_ms = ?2, updated_at_unix_ms = ?2 WHERE id = ?1",
            params![input.session_id, now],
        )
        .map_err(|error| format!("expire aircraft follow lease on end request: {error}"))?;
        insert_event(
            &tx,
            &input.session_id,
            "END_REQUESTED",
            &state,
            "operator",
            &generate_id(&tx)?,
            "OPERATOR_STOP",
            input.reason.trim(),
            &serde_json::json!({ "actor": input.actor }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit aircraft follow end request: {error}"))?;
        drop(connection);
        self.aircraft_follow_session(&input.session_id)
    }

    pub(crate) fn apply_aircraft_follow_agent_update(
        &self,
        input: &AircraftFollowAgentUpdateInput,
    ) -> Result<AircraftFollowSessionSnapshot, String> {
        if !matches!(
            input.state.as_str(),
            "VALIDATING" | "ACQUIRING" | "FOLLOWING" | "DEGRADED_HOLD" | "ENDED"
        ) {
            return Err(format!("unsupported aircraft follow state {}", input.state));
        }
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin aircraft follow Agent update: {error}"))?;
        let duplicate: bool = tx
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM aircraft_follow_events WHERE id = ?1)",
                [&input.event_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("check aircraft follow update idempotency: {error}"))?;
        if duplicate {
            drop(tx);
            drop(connection);
            return self.aircraft_follow_session(&input.session_id);
        }
        let current: String = tx
            .query_row(
                "SELECT state FROM aircraft_follow_sessions WHERE id = ?1",
                [&input.session_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("read aircraft follow state: {error}"))?
            .ok_or_else(|| format!("aircraft follow session {} was not found", input.session_id))?;
        if current == "ENDED" {
            drop(tx);
            drop(connection);
            return self.aircraft_follow_session(&input.session_id);
        }
        validate_state_transition(&current, &input.state)?;
        tx.execute(
            r#"
            UPDATE aircraft_follow_sessions
            SET state = ?2, last_agent_update_at_unix_ms = ?3,
                started_at_unix_ms = CASE WHEN ?2 = 'FOLLOWING'
                    THEN COALESCE(started_at_unix_ms, ?3) ELSE started_at_unix_ms END,
                ended_at_unix_ms = CASE WHEN ?2 = 'ENDED' THEN ?3 ELSE ended_at_unix_ms END,
                exit_reason_code = CASE WHEN ?2 IN ('DEGRADED_HOLD', 'ENDED')
                    THEN ?4 ELSE exit_reason_code END,
                exit_reason = CASE WHEN ?2 IN ('DEGRADED_HOLD', 'ENDED')
                    THEN ?5 ELSE exit_reason END,
                updated_at_unix_ms = ?3
            WHERE id = ?1
            "#,
            params![
                input.session_id,
                input.state,
                input.observed_at_unix_ms,
                input.reason_code,
                input.message,
            ],
        )
        .map_err(|error| format!("apply aircraft follow Agent state: {error}"))?;
        insert_event_with_id(
            &tx,
            &input.event_id,
            &input.session_id,
            "AGENT_STATE",
            &input.state,
            "atlas_agent",
            &input.operation_id,
            &input.reason_code,
            &input.message,
            &serde_json::from_str(&input.evidence_json).unwrap_or_else(|_| serde_json::json!({})),
            input.observed_at_unix_ms,
        )?;
        tx.commit()
            .map_err(|error| format!("commit aircraft follow Agent update: {error}"))?;
        drop(connection);
        self.aircraft_follow_session(&input.session_id)
    }

    pub(crate) fn degrade_aircraft_follow_session(
        &self,
        session_id: &str,
        source: &str,
        reason_code: &str,
        reason: &str,
        now: i64,
    ) -> Result<AircraftFollowSessionSnapshot, String> {
        if !matches!(source, "atlas_native" | "watchdog") {
            return Err("invalid aircraft follow degradation source".into());
        }
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin aircraft follow degradation: {error}"))?;
        let current: String = tx
            .query_row(
                "SELECT state FROM aircraft_follow_sessions WHERE id = ?1",
                [session_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("read aircraft follow session for degradation: {error}"))?
            .ok_or_else(|| format!("aircraft follow session {session_id} was not found"))?;
        if matches!(current.as_str(), "DEGRADED_HOLD" | "ENDED") {
            drop(tx);
            drop(connection);
            return self.aircraft_follow_session(session_id);
        }
        tx.execute(
            "UPDATE aircraft_follow_sessions SET state = 'DEGRADED_HOLD', operator_lease_expires_at_unix_ms = ?2, exit_reason_code = ?3, exit_reason = ?4, updated_at_unix_ms = ?2 WHERE id = ?1",
            params![session_id, now, reason_code, reason],
        )
        .map_err(|error| format!("degrade aircraft follow session: {error}"))?;
        insert_event(
            &tx,
            session_id,
            "WATCHDOG_HOLD",
            "DEGRADED_HOLD",
            source,
            &generate_id(&tx)?,
            reason_code,
            reason,
            &serde_json::json!({}),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit aircraft follow degradation: {error}"))?;
        drop(connection);
        self.aircraft_follow_session(session_id)
    }

    pub(crate) fn degrade_aircraft_follow_for_drone(
        &self,
        drone_id: &str,
        reason_code: &str,
        reason: &str,
        now: i64,
    ) -> Result<Option<AircraftFollowSessionSnapshot>, String> {
        let session_id = {
            let connection = self
                .connection
                .lock()
                .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
            connection
                .query_row(
                    "SELECT id FROM aircraft_follow_sessions WHERE drone_id = ?1 AND state NOT IN ('DEGRADED_HOLD', 'ENDED') ORDER BY requested_at_unix_ms DESC LIMIT 1",
                    [drone_id],
                    |row| row.get::<_, String>(0),
                )
                .optional()
                .map_err(|error| format!("read active aircraft follow for drone: {error}"))?
        };
        session_id
            .map(|id| {
                self.degrade_aircraft_follow_session(&id, "watchdog", reason_code, reason, now)
            })
            .transpose()
    }

    pub(crate) fn aircraft_follow_watchdog_actions(
        &self,
        now: i64,
    ) -> Result<Vec<AircraftFollowWatchdogAction>, String> {
        let sessions = self.aircraft_follow_sessions(true, 50)?;
        let mut actions = Vec::new();
        for session in sessions.into_iter().filter(|session| {
            matches!(
                session.state.as_str(),
                "VALIDATING" | "ACQUIRING" | "FOLLOWING"
            )
        }) {
            let reason = if session
                .operator_lease_expires_at_unix_ms
                .is_none_or(|expires| expires <= now)
            {
                Some((
                    "OPERATOR_LEASE_EXPIRED",
                    "Operator supervision lease expired; aircraft translation was stopped",
                ))
            } else if now.saturating_sub(session.latest_target_observed_at_unix_ms)
                > FOLLOW_RUNTIME_TARGET_MAX_AGE_MS
            {
                Some((
                    "GEOLOCATION_STALE",
                    "Validated world-space target state became stale; aircraft translation was stopped",
                ))
            } else if now.saturating_sub(session.authorized_at_unix_ms.unwrap_or(now))
                >= session.maximum_duration_ms
            {
                Some((
                    "MAXIMUM_DURATION_REACHED",
                    "The operator-reviewed maximum follow duration was reached",
                ))
            } else {
                let aircraft = self.operations_snapshot_for(Some(&session.drone_id))?;
                watchdog_aircraft_reason(&aircraft, &session, now)
            };
            if let Some((code, message)) = reason {
                let degraded = self.degrade_aircraft_follow_session(
                    &session.id,
                    "watchdog",
                    code,
                    message,
                    now,
                )?;
                actions.push(AircraftFollowWatchdogAction {
                    session: degraded,
                    reason_code: code.to_string(),
                    reason: message.to_string(),
                });
            }
        }
        Ok(actions)
    }

    pub(crate) fn aircraft_follow_session(
        &self,
        session_id: &str,
    ) -> Result<AircraftFollowSessionSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        read_session(&connection, session_id)
    }

    pub(crate) fn aircraft_follow_sessions(
        &self,
        include_ended: bool,
        limit: usize,
    ) -> Result<Vec<AircraftFollowSessionSnapshot>, String> {
        let ids = {
            let connection = self
                .connection
                .lock()
                .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
            let mut statement = connection
                .prepare(
                    "SELECT id FROM aircraft_follow_sessions WHERE state <> 'ENDED' OR ?1 ORDER BY requested_at_unix_ms DESC LIMIT ?2",
                )
                .map_err(|error| format!("prepare aircraft follow sessions: {error}"))?;
            let ids = statement
                .query_map(params![include_ended, limit.clamp(1, 200) as i64], |row| {
                    row.get::<_, String>(0)
                })
                .map_err(|error| format!("query aircraft follow sessions: {error}"))?
                .collect::<Result<Vec<_>, _>>()
                .map_err(|error| format!("read aircraft follow session ids: {error}"))?;
            ids
        };
        ids.iter()
            .map(|id| self.aircraft_follow_session(id))
            .collect()
    }
}

fn validate_create_input(input: &CreateAircraftFollowSessionInput) -> Result<(), String> {
    if input.drone_id.trim().is_empty()
        || input.geolocation_id.trim().is_empty()
        || input.requested_by.trim().is_empty()
        || input.reviewed_by.trim().is_empty()
    {
        return Err("aircraft, geolocation, requester, and reviewer are required".into());
    }
    if input.operator_review_note.trim().len() < 8 || input.operator_review_note.len() > 500 {
        return Err("operator review note must contain between 8 and 500 characters".into());
    }
    bounded(input.standoff_m, 10.0, 500.0, "standoff")?;
    bounded(input.altitude_relative_m, 5.0, 120.0, "relative altitude")?;
    bounded(
        input.minimum_altitude_relative_m,
        5.0,
        120.0,
        "minimum relative altitude",
    )?;
    bounded(
        input.maximum_altitude_relative_m,
        5.0,
        120.0,
        "maximum relative altitude",
    )?;
    if input.minimum_altitude_relative_m > input.altitude_relative_m
        || input.altitude_relative_m > input.maximum_altitude_relative_m
    {
        return Err("reviewed altitude must remain inside the reviewed altitude band".into());
    }
    bounded(
        input.maximum_ground_speed_mps,
        0.5,
        15.0,
        "maximum groundspeed",
    )?;
    bounded(
        input.maximum_acceleration_mps2,
        0.1,
        5.0,
        "maximum acceleration",
    )?;
    if !(10..=1_800).contains(&input.maximum_duration_seconds) {
        return Err("maximum follow duration must be between 10 and 1800 seconds".into());
    }
    coordinate(
        input.boundary_center_latitude,
        input.boundary_center_longitude,
    )?;
    bounded(input.boundary_radius_m, 25.0, 5_000.0, "boundary radius")?;
    bounded(
        input.minimum_battery_percent,
        15.0,
        100.0,
        "minimum battery",
    )?;
    bounded(
        input.minimum_track_confidence,
        0.5,
        1.0,
        "minimum track confidence",
    )?;
    bounded(
        input.maximum_geolocation_uncertainty_m,
        1.0,
        100.0,
        "maximum geolocation uncertainty",
    )?;
    bounded(
        input.maximum_velocity_uncertainty_mps,
        0.1,
        25.0,
        "maximum velocity uncertainty",
    )
}

fn validate_lease(duration_ms: i64) -> Result<i64, String> {
    if !(FOLLOW_LEASE_MIN_MS..=FOLLOW_LEASE_MAX_MS).contains(&duration_ms) {
        return Err(format!(
            "aircraft follow supervision lease must be between {FOLLOW_LEASE_MIN_MS} and {FOLLOW_LEASE_MAX_MS} milliseconds"
        ));
    }
    Ok(duration_ms)
}

fn validate_aircraft_readiness(
    aircraft: &super::OperationsSnapshot,
    minimum_battery_percent: f64,
    minimum_altitude_relative_m: f64,
    maximum_altitude_relative_m: f64,
    now: i64,
) -> Result<String, String> {
    if aircraft.connection_status != "connected"
        || aircraft
            .last_heartbeat_at_unix_ms
            .is_none_or(|heartbeat| now.saturating_sub(heartbeat) > 15_000)
    {
        return Err("Follow from standoff requires a fresh connected Atlas Agent link".into());
    }
    if !aircraft
        .agent_capabilities
        .iter()
        .any(|capability| capability == "aircraft_follow:standoff:v1:verified")
    {
        return Err("Follow from standoff remains UNVERIFIED on this installation; record accepted simulation, HIL, and controlled-flight evidence before enabling aircraft translation".into());
    }
    if !aircraft
        .agent_capabilities
        .iter()
        .any(|capability| capability == "geolocation:boresight_alignment:verified")
    {
        return Err(
            "Follow from standoff requires VERIFIED camera/gimbal boresight alignment".into(),
        );
    }
    let validation_reference = aircraft
        .agent_capabilities
        .iter()
        .find_map(|capability| capability.strip_prefix("aircraft_follow:validation:"))
        .filter(|reference| !reference.trim().is_empty())
        .ok_or_else(|| {
            "verified follow capability did not include its physical validation reference"
                .to_string()
        })?;
    let telemetry = aircraft
        .telemetry
        .as_ref()
        .ok_or_else(|| "Follow from standoff requires aircraft telemetry".to_string())?;
    if telemetry.status != "live"
        || now.saturating_sub(telemetry.received_at_unix_ms) > TELEMETRY_MAX_AGE_MS
    {
        return Err("Follow from standoff requires telemetry no older than five seconds".into());
    }
    if telemetry.armed != Some(true) || telemetry.in_air != Some(true) {
        return Err("Follow from standoff requires an armed aircraft already in flight".into());
    }
    let health = telemetry
        .health
        .as_ref()
        .ok_or_else(|| "Follow from standoff requires reported position health".to_string())?;
    if !health.local_position_ok || !health.global_position_ok {
        return Err("Follow from standoff requires healthy local and global position".into());
    }
    let battery = telemetry
        .battery_percent
        .ok_or_else(|| "Follow from standoff requires a reported battery percentage".to_string())?;
    if battery < minimum_battery_percent {
        return Err(format!(
            "Follow from standoff requires at least {minimum_battery_percent:.0}% battery; aircraft reports {battery:.0}%"
        ));
    }
    let altitude = telemetry
        .relative_altitude_m
        .ok_or_else(|| "Follow from standoff requires relative altitude".to_string())?;
    if altitude < minimum_altitude_relative_m || altitude > maximum_altitude_relative_m {
        return Err(format!(
            "aircraft relative altitude {altitude:.1} m is outside the reviewed {minimum_altitude_relative_m:.1}–{maximum_altitude_relative_m:.1} m band"
        ));
    }
    Ok(validation_reference.to_string())
}

pub(super) fn watchdog_aircraft_reason<'a>(
    aircraft: &super::OperationsSnapshot,
    session: &AircraftFollowSessionSnapshot,
    now: i64,
) -> Option<(&'a str, &'a str)> {
    if aircraft.connection_status != "connected"
        || aircraft
            .last_heartbeat_at_unix_ms
            .is_none_or(|heartbeat| now.saturating_sub(heartbeat) > 15_000)
    {
        return Some((
            "GROUND_LINK_LOST",
            "Ground link became unavailable; the onboard lease watchdog stopped translation",
        ));
    }
    let Some(telemetry) = aircraft.telemetry.as_ref() else {
        return Some((
            "TELEMETRY_UNAVAILABLE",
            "Aircraft telemetry became unavailable; aircraft translation was stopped",
        ));
    };
    if telemetry.status != "live"
        || now.saturating_sub(telemetry.received_at_unix_ms) > TELEMETRY_MAX_AGE_MS
    {
        return Some((
            "TELEMETRY_STALE",
            "Aircraft telemetry became stale; aircraft translation was stopped",
        ));
    }
    if telemetry.in_air != Some(true) || telemetry.armed != Some(true) {
        return Some((
            "FLIGHT_STATE_INVALID",
            "Aircraft is no longer armed and in flight; follow control was stopped",
        ));
    }
    if telemetry
        .battery_percent
        .is_none_or(|battery| battery < session.minimum_battery_percent)
    {
        return Some((
            "BATTERY_RESERVE_REACHED",
            "Battery reached the operator-reviewed reserve; aircraft translation was stopped",
        ));
    }
    if telemetry
        .health
        .as_ref()
        .is_none_or(|health| !health.local_position_ok || !health.global_position_ok)
    {
        return Some((
            "POSITION_HEALTH_LOST",
            "PX4 local or global position health was lost; aircraft translation was stopped",
        ));
    }
    let Some(altitude) = telemetry.relative_altitude_m else {
        return Some((
            "ALTITUDE_UNAVAILABLE",
            "Aircraft relative altitude became unavailable; aircraft translation was stopped",
        ));
    };
    if altitude < session.minimum_altitude_relative_m
        || altitude > session.maximum_altitude_relative_m
    {
        return Some((
            "ALTITUDE_BAND_VIOLATION",
            "Aircraft left the operator-reviewed altitude band; translation was stopped",
        ));
    }
    let (Some(latitude), Some(longitude)) = (telemetry.latitude, telemetry.longitude) else {
        return Some((
            "POSITION_UNAVAILABLE",
            "Aircraft global position became unavailable; aircraft translation was stopped",
        ));
    };
    if distance_m(
        session.boundary_center_latitude,
        session.boundary_center_longitude,
        latitude,
        longitude,
    ) > session.boundary_radius_m
    {
        return Some((
            "FOLLOW_GEOFENCE_VIOLATION",
            "Aircraft crossed the operator-reviewed follow boundary; translation was stopped",
        ));
    }
    None
}

fn read_validated_target(
    tx: &Transaction<'_>,
    geolocation_id: &str,
) -> Result<AircraftFollowTargetSnapshot, String> {
    tx.query_row(
        r#"
        SELECT g.id, g.selection_id, g.source_id, g.track_session_id, g.track_id,
               g.frame_observed_at_unix_ms, COALESCE(g.filtered_latitude, g.latitude),
               COALESCE(g.filtered_longitude, g.longitude), g.altitude_amsl_m,
               g.target_velocity_north_mps, g.target_velocity_east_mps,
               g.horizontal_uncertainty_m, g.target_velocity_uncertainty_mps,
               t.latest_detection_confidence, t.lifecycle_state, g.motion_status,
               g.status, g.refinement_status, s.status, s.drone_id
        FROM perception_track_geolocations g
        JOIN perception_tracks t
          ON t.track_session_id = g.track_session_id AND t.id = g.track_id
        JOIN perception_track_selections s ON s.id = g.selection_id
        WHERE g.id = ?1
        "#,
        [geolocation_id],
        |row| {
            let status: String = row.get(16)?;
            let refinement: String = row.get(17)?;
            let selection_status: String = row.get(18)?;
            if status != "SUCCEEDED"
                || refinement != "CONVERGED"
                || selection_status != "SELECTED"
            {
                return Err(rusqlite::Error::InvalidQuery);
            }
            scan_target(row)
        },
    )
    .optional()
    .map_err(|error| {
        if matches!(error, rusqlite::Error::InvalidQuery) {
            "Follow from standoff requires a successful, converged coordinate for the exact active selection".to_string()
        } else {
            format!("read validated aircraft follow target: {error}")
        }
    })?
    .ok_or_else(|| format!("geolocation {geolocation_id} was not found"))
}

fn scan_target(row: &Row<'_>) -> rusqlite::Result<AircraftFollowTargetSnapshot> {
    Ok(AircraftFollowTargetSnapshot {
        geolocation_id: row.get(0)?,
        drone_id: row.get(19)?,
        selection_id: row.get(1)?,
        source_id: row.get(2)?,
        track_session_id: row.get(3)?,
        track_id: row.get(4)?,
        observed_at_unix_ms: row.get(5)?,
        latitude: row.get(6)?,
        longitude: row.get(7)?,
        altitude_amsl_m: row.get(8)?,
        velocity_north_mps: row.get(9)?,
        velocity_east_mps: row.get(10)?,
        horizontal_uncertainty_m: row.get(11)?,
        velocity_uncertainty_mps: row.get(12)?,
        track_confidence: row.get(13)?,
        lifecycle_state: row.get(14)?,
        motion_status: row.get(15)?,
    })
}

fn validate_target_for_session(
    target: &AircraftFollowTargetSnapshot,
    session: &AircraftFollowSessionSnapshot,
    now: i64,
    maximum_age_ms: i64,
) -> Result<(), String> {
    if target.selection_id != session.selection_id
        || target.track_session_id != session.track_session_id
        || target.track_id != session.track_id
        || target.source_id != session.source_id
    {
        return Err(
            "aircraft follow target update does not belong to the authorized exact track selection"
                .into(),
        );
    }
    validate_target_for_envelope(
        target,
        &session.drone_id,
        session.minimum_track_confidence,
        session.maximum_geolocation_uncertainty_m,
        session.maximum_velocity_uncertainty_mps,
        session.boundary_center_latitude,
        session.boundary_center_longitude,
        session.boundary_radius_m,
        now,
        maximum_age_ms,
    )
}

#[allow(clippy::too_many_arguments)]
fn validate_target_for_envelope(
    target: &AircraftFollowTargetSnapshot,
    expected_drone_id: &str,
    minimum_track_confidence: f64,
    maximum_geolocation_uncertainty_m: f64,
    maximum_velocity_uncertainty_mps: f64,
    boundary_center_latitude: f64,
    boundary_center_longitude: f64,
    boundary_radius_m: f64,
    now: i64,
    maximum_age_ms: i64,
) -> Result<(), String> {
    if target.drone_id != expected_drone_id {
        return Err(
            "validated target does not belong to the aircraft authorized for follow".into(),
        );
    }
    coordinate(target.latitude, target.longitude)?;
    if now.saturating_sub(target.observed_at_unix_ms) > maximum_age_ms {
        return Err("validated world-space target state is stale".into());
    }
    if target.lifecycle_state != "ACTIVE" {
        return Err(format!(
            "track lifecycle {} is not eligible for aircraft following",
            target.lifecycle_state
        ));
    }
    if target.motion_status != "FILTERED" {
        return Err("aircraft following requires a filtered world-space target velocity".into());
    }
    if target.track_confidence < minimum_track_confidence {
        return Err("track confidence is below the reviewed follow threshold".into());
    }
    if target.horizontal_uncertainty_m > maximum_geolocation_uncertainty_m {
        return Err("target geolocation uncertainty exceeds the reviewed follow threshold".into());
    }
    if target.velocity_uncertainty_mps > maximum_velocity_uncertainty_mps {
        return Err("target velocity uncertainty exceeds the reviewed follow threshold".into());
    }
    if distance_m(
        boundary_center_latitude,
        boundary_center_longitude,
        target.latitude,
        target.longitude,
    ) > boundary_radius_m
    {
        return Err("target lies outside the operator-reviewed follow boundary".into());
    }
    Ok(())
}

fn verified_boresight(tx: &Transaction<'_>, geolocation_id: &str) -> Result<(String, f64), String> {
    let evidence: String = tx
        .query_row(
            "SELECT evidence_json FROM perception_track_geolocations WHERE id = ?1",
            [geolocation_id],
            |row| row.get(0),
        )
        .map_err(|error| format!("read boresight evidence: {error}"))?;
    let evidence: serde_json::Value = serde_json::from_str(&evidence)
        .map_err(|error| format!("decode boresight evidence: {error}"))?;
    let alignment = &evidence["estimate"]["boresightAlignment"];
    let status = alignment["status"].as_str().unwrap_or_default();
    let reference = alignment["reference"].as_str().unwrap_or_default().trim();
    let bound = alignment["errorBoundDeg"]
        .as_f64()
        .filter(|value| value.is_finite() && *value > 0.0 && *value <= 45.0);
    if status != "VERIFIED" || reference.is_empty() || bound.is_none() {
        return Err("Follow from standoff requires a physical boresight test reference and accepted angular bound".into());
    }
    Ok((reference.to_string(), bound.expect("checked bound")))
}

fn insert_target_update(
    tx: &Transaction<'_>,
    session_id: &str,
    target: &AircraftFollowTargetSnapshot,
    now: i64,
) -> Result<(), String> {
    tx.execute(
        r#"
        INSERT OR IGNORE INTO aircraft_follow_target_updates (
            id, aircraft_follow_session_id, geolocation_id, observed_at_unix_ms,
            latitude, longitude, altitude_amsl_m, velocity_north_mps,
            velocity_east_mps, horizontal_uncertainty_m,
            velocity_uncertainty_mps, track_confidence, lifecycle_state,
            motion_status, accepted_at_unix_ms
        ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15)
        "#,
        params![
            generate_id(tx)?,
            session_id,
            target.geolocation_id,
            target.observed_at_unix_ms,
            target.latitude,
            target.longitude,
            target.altitude_amsl_m,
            target.velocity_north_mps,
            target.velocity_east_mps,
            target.horizontal_uncertainty_m,
            target.velocity_uncertainty_mps,
            target.track_confidence,
            target.lifecycle_state,
            target.motion_status,
            now,
        ],
    )
    .map_err(|error| format!("insert aircraft follow target update: {error}"))?;
    Ok(())
}

fn read_session(
    connection: &rusqlite::Connection,
    id: &str,
) -> Result<AircraftFollowSessionSnapshot, String> {
    let mut session = connection
        .query_row(
            r#"
            SELECT id, drone_id, selection_id, track_session_id, track_id, source_id,
                   state, requested_by, reviewed_by, operator_review_note,
                   requested_at_unix_ms, authorized_at_unix_ms, started_at_unix_ms,
                   ended_at_unix_ms, standoff_m, altitude_relative_m,
                   minimum_altitude_relative_m, maximum_altitude_relative_m,
                   maximum_ground_speed_mps, maximum_acceleration_mps2,
                   maximum_duration_ms, boundary_center_latitude,
                   boundary_center_longitude, boundary_radius_m,
                   minimum_battery_percent, minimum_track_confidence,
                   maximum_geolocation_uncertainty_m,
                   maximum_velocity_uncertainty_mps, latest_geolocation_id,
                   latest_target_observed_at_unix_ms,
                   operator_lease_expires_at_unix_ms, last_agent_update_at_unix_ms,
                   validation_reference, boresight_reference,
                   boresight_error_bound_deg, exit_reason_code, exit_reason,
                   created_at_unix_ms, updated_at_unix_ms
            FROM aircraft_follow_sessions WHERE id = ?1
            "#,
            [id],
            |row| {
                Ok(AircraftFollowSessionSnapshot {
                    id: row.get(0)?,
                    drone_id: row.get(1)?,
                    selection_id: row.get(2)?,
                    track_session_id: row.get(3)?,
                    track_id: row.get(4)?,
                    source_id: row.get(5)?,
                    state: row.get(6)?,
                    requested_by: row.get(7)?,
                    reviewed_by: row.get(8)?,
                    operator_review_note: row.get(9)?,
                    requested_at_unix_ms: row.get(10)?,
                    authorized_at_unix_ms: row.get(11)?,
                    started_at_unix_ms: row.get(12)?,
                    ended_at_unix_ms: row.get(13)?,
                    standoff_m: row.get(14)?,
                    altitude_relative_m: row.get(15)?,
                    minimum_altitude_relative_m: row.get(16)?,
                    maximum_altitude_relative_m: row.get(17)?,
                    maximum_ground_speed_mps: row.get(18)?,
                    maximum_acceleration_mps2: row.get(19)?,
                    maximum_duration_ms: row.get(20)?,
                    boundary_center_latitude: row.get(21)?,
                    boundary_center_longitude: row.get(22)?,
                    boundary_radius_m: row.get(23)?,
                    minimum_battery_percent: row.get(24)?,
                    minimum_track_confidence: row.get(25)?,
                    maximum_geolocation_uncertainty_m: row.get(26)?,
                    maximum_velocity_uncertainty_mps: row.get(27)?,
                    latest_geolocation_id: row.get(28)?,
                    latest_target_observed_at_unix_ms: row.get(29)?,
                    operator_lease_expires_at_unix_ms: row.get(30)?,
                    last_agent_update_at_unix_ms: row.get(31)?,
                    validation_reference: row.get(32)?,
                    boresight_reference: row.get(33)?,
                    boresight_error_bound_deg: row.get(34)?,
                    exit_reason_code: row.get(35)?,
                    exit_reason: row.get(36)?,
                    created_at_unix_ms: row.get(37)?,
                    updated_at_unix_ms: row.get(38)?,
                    target: AircraftFollowTargetSnapshot {
                        geolocation_id: String::new(),
                        drone_id: String::new(),
                        selection_id: String::new(),
                        source_id: String::new(),
                        track_session_id: String::new(),
                        track_id: String::new(),
                        observed_at_unix_ms: 0,
                        latitude: 0.0,
                        longitude: 0.0,
                        altitude_amsl_m: 0.0,
                        velocity_north_mps: 0.0,
                        velocity_east_mps: 0.0,
                        horizontal_uncertainty_m: 0.0,
                        velocity_uncertainty_mps: 0.0,
                        track_confidence: 0.0,
                        lifecycle_state: String::new(),
                        motion_status: String::new(),
                    },
                    events: Vec::new(),
                })
            },
        )
        .optional()
        .map_err(|error| format!("read aircraft follow session: {error}"))?
        .ok_or_else(|| format!("aircraft follow session {id} was not found"))?;
    session.target = connection
        .query_row(
            r#"
            SELECT geolocation_id, s.drone_id, s.selection_id, s.source_id, s.track_session_id,
                   s.track_id, u.observed_at_unix_ms, u.latitude, u.longitude,
                   u.altitude_amsl_m, u.velocity_north_mps, u.velocity_east_mps,
                   u.horizontal_uncertainty_m, u.velocity_uncertainty_mps,
                   u.track_confidence, u.lifecycle_state, u.motion_status
            FROM aircraft_follow_target_updates u
            JOIN aircraft_follow_sessions s ON s.id = u.aircraft_follow_session_id
            WHERE u.aircraft_follow_session_id = ?1
            ORDER BY u.observed_at_unix_ms DESC, u.accepted_at_unix_ms DESC LIMIT 1
            "#,
            [id],
            |row| {
                Ok(AircraftFollowTargetSnapshot {
                    geolocation_id: row.get(0)?,
                    drone_id: row.get(1)?,
                    selection_id: row.get(2)?,
                    source_id: row.get(3)?,
                    track_session_id: row.get(4)?,
                    track_id: row.get(5)?,
                    observed_at_unix_ms: row.get(6)?,
                    latitude: row.get(7)?,
                    longitude: row.get(8)?,
                    altitude_amsl_m: row.get(9)?,
                    velocity_north_mps: row.get(10)?,
                    velocity_east_mps: row.get(11)?,
                    horizontal_uncertainty_m: row.get(12)?,
                    velocity_uncertainty_mps: row.get(13)?,
                    track_confidence: row.get(14)?,
                    lifecycle_state: row.get(15)?,
                    motion_status: row.get(16)?,
                })
            },
        )
        .map_err(|error| format!("read latest aircraft follow target: {error}"))?;
    let mut events = connection
        .prepare(
            "SELECT id, sequence, event_type, state, source, operation_id, reason_code, message, evidence_json, occurred_at_unix_ms FROM aircraft_follow_events WHERE aircraft_follow_session_id = ?1 ORDER BY sequence",
        )
        .map_err(|error| format!("prepare aircraft follow events: {error}"))?;
    session.events = events
        .query_map([id], |row| {
            let evidence: String = row.get(8)?;
            Ok(AircraftFollowEventSnapshot {
                id: row.get(0)?,
                sequence: row.get(1)?,
                event_type: row.get(2)?,
                state: row.get(3)?,
                source: row.get(4)?,
                operation_id: row.get(5)?,
                reason_code: row.get(6)?,
                message: row.get(7)?,
                evidence: serde_json::from_str(&evidence).unwrap_or_else(|_| serde_json::json!({})),
                occurred_at_unix_ms: row.get(9)?,
            })
        })
        .map_err(|error| format!("query aircraft follow events: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read aircraft follow events: {error}"))?;
    Ok(session)
}

#[allow(clippy::too_many_arguments)]
fn insert_event(
    tx: &Transaction<'_>,
    session_id: &str,
    event_type: &str,
    state: &str,
    source: &str,
    operation_id: &str,
    reason_code: &str,
    message: &str,
    evidence: &serde_json::Value,
    occurred_at: i64,
) -> Result<(), String> {
    insert_event_with_id(
        tx,
        &generate_id(tx)?,
        session_id,
        event_type,
        state,
        source,
        operation_id,
        reason_code,
        message,
        evidence,
        occurred_at,
    )
}

#[allow(clippy::too_many_arguments)]
fn insert_event_with_id(
    tx: &Transaction<'_>,
    event_id: &str,
    session_id: &str,
    event_type: &str,
    state: &str,
    source: &str,
    operation_id: &str,
    reason_code: &str,
    message: &str,
    evidence: &serde_json::Value,
    occurred_at: i64,
) -> Result<(), String> {
    let sequence: i64 = tx
        .query_row(
            "SELECT COALESCE(MAX(sequence), 0) + 1 FROM aircraft_follow_events WHERE aircraft_follow_session_id = ?1",
            [session_id],
            |row| row.get(0),
        )
        .map_err(|error| format!("sequence aircraft follow event: {error}"))?;
    tx.execute(
        "INSERT INTO aircraft_follow_events (id, aircraft_follow_session_id, sequence, event_type, state, source, operation_id, reason_code, message, evidence_json, occurred_at_unix_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11)",
        params![
            event_id,
            session_id,
            sequence,
            event_type,
            state,
            source,
            operation_id,
            reason_code,
            message,
            evidence.to_string(),
            occurred_at,
        ],
    )
    .map_err(|error| format!("insert aircraft follow event: {error}"))?;
    Ok(())
}

fn validate_state_transition(current: &str, next: &str) -> Result<(), String> {
    let allowed = current == next
        || matches!(
            (current, next),
            ("VALIDATING", "ACQUIRING")
                | ("VALIDATING", "DEGRADED_HOLD")
                | ("VALIDATING", "ENDED")
                | ("ACQUIRING", "FOLLOWING")
                | ("ACQUIRING", "DEGRADED_HOLD")
                | ("ACQUIRING", "ENDED")
                | ("FOLLOWING", "DEGRADED_HOLD")
                | ("FOLLOWING", "ENDED")
                | ("DEGRADED_HOLD", "ENDED")
        );
    if allowed {
        Ok(())
    } else {
        Err(format!(
            "invalid aircraft follow transition {current} -> {next}"
        ))
    }
}

fn bounded(value: f64, minimum: f64, maximum: f64, label: &str) -> Result<(), String> {
    if !value.is_finite() || value < minimum || value > maximum {
        return Err(format!("{label} must be between {minimum} and {maximum}"));
    }
    Ok(())
}

fn coordinate(latitude: f64, longitude: f64) -> Result<(), String> {
    bounded(latitude, -90.0, 90.0, "latitude")?;
    bounded(longitude, -180.0, 180.0, "longitude")
}

fn distance_m(latitude_a: f64, longitude_a: f64, latitude_b: f64, longitude_b: f64) -> f64 {
    const EARTH_RADIUS_M: f64 = 6_378_137.0;
    let mean_latitude = (latitude_a + latitude_b) * 0.5 * PI / 180.0;
    let north_m = (latitude_b - latitude_a) * PI / 180.0 * EARTH_RADIUS_M;
    let east_m = (longitude_b - longitude_a) * PI / 180.0 * EARTH_RADIUS_M * mean_latitude.cos();
    north_m.hypot(east_m)
}

fn generate_id(tx: &Transaction<'_>) -> Result<String, String> {
    tx.query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate aircraft follow identifier: {error}"))
}
