use rusqlite::{params, OptionalExtension, Row, Transaction};
use serde::Serialize;

use super::LocalDatabase;

const MAX_STATUS_EVENTS_PER_DRONE: usize = 200;

#[derive(Debug, Clone)]
pub(crate) struct StatusEventInput {
    pub origin: String,
    pub event_type: String,
    pub code: Option<String>,
    pub details_json: Option<String>,
    pub source: String,
    pub severity: String,
    pub message: String,
    pub agent_observed_at_unix_ms: i64,
    pub received_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct StatusEventSnapshot {
    pub id: String,
    pub source: String,
    pub origin: String,
    pub event_type: String,
    pub code: Option<String>,
    pub details_json: Option<String>,
    pub severity: String,
    pub message: String,
    pub observed_at_unix_ms: i64,
    pub received_at_unix_ms: i64,
}

impl LocalDatabase {
    pub(crate) fn record_status_event(
        &self,
        session_id: &str,
        input: &StatusEventInput,
    ) -> Result<(), String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let transaction = connection
            .transaction()
            .map_err(|error| format!("begin vehicle status event transaction: {error}"))?;
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
            .map_err(|error| format!("resolve vehicle status event session: {error}"))?;
        let (drone_id, communication_link_id) =
            association.ok_or_else(|| "communication link is not open".to_string())?;

        insert_status_event(&transaction, &drone_id, &communication_link_id, input)?;
        prune_status_events(&transaction, &drone_id)?;
        transaction
            .commit()
            .map_err(|error| format!("commit vehicle status event: {error}"))
    }

    pub(crate) fn vehicle_event_history(
        &self,
        drone_id: &str,
        from_received_at_unix_ms: Option<i64>,
        to_received_at_unix_ms: Option<i64>,
        limit: usize,
    ) -> Result<Vec<StatusEventSnapshot>, String> {
        if drone_id.trim().is_empty() {
            return Err("vehicle event history drone id is required".to_string());
        }
        if from_received_at_unix_ms
            .zip(to_received_at_unix_ms)
            .is_some_and(|(from, to)| from > to)
        {
            return Err(
                "vehicle event history start time must not be after its end time".to_string(),
            );
        }
        let limit = match limit {
            0 => 100,
            requested => requested.min(MAX_STATUS_EVENTS_PER_DRONE),
        };
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare(
                r#"
                SELECT id, source, severity, message,
                       agent_observed_at_unix_ms, received_at_unix_ms,
                       origin, event_type, code, details_json
                FROM vehicle_status_events
                WHERE drone_id = ?1
                  AND (?2 IS NULL OR received_at_unix_ms >= ?2)
                  AND (?3 IS NULL OR received_at_unix_ms <= ?3)
                ORDER BY received_at_unix_ms DESC,
                         agent_observed_at_unix_ms DESC,
                         rowid DESC
                LIMIT ?4
                "#,
            )
            .map_err(|error| format!("prepare vehicle event history query: {error}"))?;
        let events = statement
            .query_map(
                params![
                    drone_id,
                    from_received_at_unix_ms,
                    to_received_at_unix_ms,
                    limit as i64
                ],
                status_event_snapshot,
            )
            .map_err(|error| format!("query vehicle event history: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read vehicle event history: {error}"))?;
        Ok(events)
    }
}

pub(super) fn status_event_snapshot(row: &Row<'_>) -> rusqlite::Result<StatusEventSnapshot> {
    Ok(StatusEventSnapshot {
        id: row.get(0)?,
        source: row.get(1)?,
        severity: row.get(2)?,
        message: row.get(3)?,
        observed_at_unix_ms: row.get(4)?,
        received_at_unix_ms: row.get(5)?,
        origin: row.get(6)?,
        event_type: row.get(7)?,
        code: row.get(8)?,
        details_json: row.get(9)?,
    })
}

pub(super) fn insert_status_event(
    transaction: &Transaction<'_>,
    drone_id: &str,
    communication_link_id: &str,
    input: &StatusEventInput,
) -> Result<(), String> {
    validate_status_event(input)?;
    transaction
        .execute(
            r#"
            INSERT INTO vehicle_status_events (
                id, drone_id, communication_link_id, source, severity, message,
                agent_observed_at_unix_ms, received_at_unix_ms, origin,
                event_type, code, details_json
            ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)
            "#,
            params![
                generate_id(transaction)?,
                drone_id,
                communication_link_id,
                input.source,
                input.severity,
                input.message,
                input.agent_observed_at_unix_ms,
                input.received_at_unix_ms,
                input.origin,
                input.event_type,
                input.code,
                input.details_json,
            ],
        )
        .map_err(|error| format!("store vehicle status event: {error}"))?;
    Ok(())
}

fn validate_status_event(input: &StatusEventInput) -> Result<(), String> {
    if !matches!(
        input.origin.as_str(),
        "px4" | "atlas_agent" | "atlas_native"
    ) {
        return Err("vehicle status event origin is not recognized".to_string());
    }
    if input.event_type.trim().is_empty()
        || input.source.trim().is_empty()
        || input.severity.trim().is_empty()
        || input.message.trim().is_empty()
        || input.agent_observed_at_unix_ms <= 0
        || input.received_at_unix_ms <= 0
    {
        return Err(
            "vehicle status event type, source, severity, message, and timestamps are required"
                .to_string(),
        );
    }
    if input
        .code
        .as_deref()
        .is_some_and(|code| code.trim().is_empty())
    {
        return Err("vehicle status event code must not be empty".to_string());
    }
    if let Some(details) = input.details_json.as_deref() {
        serde_json::from_str::<serde_json::Value>(details)
            .map_err(|error| format!("vehicle status event details must be valid JSON: {error}"))?;
    }
    Ok(())
}

pub(super) fn prune_status_events(
    transaction: &Transaction<'_>,
    drone_id: &str,
) -> Result<(), String> {
    transaction
        .execute(
            r#"
            DELETE FROM vehicle_status_events
            WHERE drone_id = ?1 AND id NOT IN (
                SELECT id
                FROM vehicle_status_events
                WHERE drone_id = ?1
                ORDER BY received_at_unix_ms DESC,
                         agent_observed_at_unix_ms DESC,
                         rowid DESC
                LIMIT ?2
            )
            "#,
            params![drone_id, MAX_STATUS_EVENTS_PER_DRONE as i64],
        )
        .map_err(|error| format!("prune vehicle status event history: {error}"))?;
    Ok(())
}

fn generate_id(transaction: &Transaction<'_>) -> Result<String, String> {
    transaction
        .query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate vehicle status event identifier: {error}"))
}
