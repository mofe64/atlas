use rusqlite::{params, OptionalExtension, Transaction};
use serde::Serialize;
use serde_json::Value;

use super::{incidents::mark_incident_response_on_scene, LocalDatabase, MissionRunSnapshot};

const RUNTIME_ACTION_TYPES: &[&str] = &["HOLD_AT_ARRIVAL", "POINT_GIMBAL_AT_INCIDENT"];

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct MissionActionExecutionSnapshot {
    pub id: String,
    pub mission_run_id: String,
    pub mission_plan_id: String,
    pub action_sequence: u32,
    pub action_type: String,
    pub state: String,
    pub attempt: u32,
    pub max_attempts: u32,
    pub failure_policy: String,
    pub requested_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
    pub started_at_unix_ms: Option<i64>,
    pub completed_at_unix_ms: Option<i64>,
    pub error_code: String,
    pub error_message: String,
    pub evidence_json: Option<String>,
    pub events: Vec<MissionActionExecutionEventSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct MissionActionExecutionEventSnapshot {
    pub id: String,
    pub sequence: u32,
    pub state: String,
    pub attempt: u32,
    pub source: String,
    pub occurred_at_unix_ms: i64,
    pub error_code: String,
    pub message: String,
    pub evidence_json: Option<String>,
}

#[derive(Debug)]
pub(crate) struct MissionActionUpdateInput {
    pub event_id: String,
    pub mission_run_id: String,
    pub action_sequence: u32,
    pub action_type: String,
    pub state: String,
    pub attempt: u32,
    pub failure_policy: String,
    pub occurred_at_unix_ms: i64,
    pub error_code: String,
    pub message: String,
    pub evidence_json: Option<String>,
}

impl LocalDatabase {
    pub(crate) fn apply_mission_action_update(
        &self,
        input: &MissionActionUpdateInput,
    ) -> Result<MissionRunSnapshot, String> {
        validate_action_state(&input.state)?;
        if input.event_id.trim().is_empty() {
            return Err("mission action eventId is required".to_string());
        }
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin mission action update: {error}"))?;

        let duplicate: Option<String> = tx
            .query_row(
                r#"
                SELECT execution.mission_run_id
                FROM mission_action_execution_events event
                JOIN mission_action_executions execution
                  ON execution.id = event.mission_action_execution_id
                WHERE event.id = ?1
                "#,
                [&input.event_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("check mission action event idempotency: {error}"))?;
        if let Some(existing_run_id) = duplicate {
            if existing_run_id != input.mission_run_id {
                return Err("mission action event id is already used by another run".to_string());
            }
            tx.commit()
                .map_err(|error| format!("commit duplicate mission action event: {error}"))?;
            drop(connection);
            return self.mission_run(&input.mission_run_id);
        }

        let execution: (String, String, String, u32, u32, String) = tx
            .query_row(
                "SELECT id, action_type, state, attempt, max_attempts, failure_policy FROM mission_action_executions WHERE mission_run_id = ?1 AND action_sequence = ?2",
                params![input.mission_run_id, input.action_sequence],
                |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?, row.get(4)?, row.get(5)?)),
            )
            .optional()
            .map_err(|error| format!("read mission action execution: {error}"))?
            .ok_or_else(|| {
                format!(
                    "mission run {} has no durable action at sequence {}",
                    input.mission_run_id, input.action_sequence
                )
            })?;
        let (
            execution_id,
            action_type,
            current_state,
            current_attempt,
            max_attempts,
            failure_policy,
        ) = execution;
        if action_type != input.action_type {
            return Err(format!(
                "mission action type mismatch at sequence {}: expected {}, received {}",
                input.action_sequence, action_type, input.action_type
            ));
        }
        if failure_policy != input.failure_policy {
            return Err(format!(
                "mission action failure policy mismatch at sequence {}: expected {}, received {}",
                input.action_sequence, failure_policy, input.failure_policy
            ));
        }
        if input.attempt > max_attempts {
            return Err(format!(
                "mission action attempt {} exceeds reviewed maximum {}",
                input.attempt, max_attempts
            ));
        }
        validate_action_transition(&current_state, &input.state, current_attempt, input.attempt)?;

        let completed = matches!(input.state.as_str(), "SUCCEEDED" | "POLICY_APPLIED");
        let clear_error = input.state == "SUCCEEDED";
        tx.execute(
            r#"
            UPDATE mission_action_executions
            SET state = ?2,
                attempt = ?3,
                updated_at_unix_ms = ?4,
                started_at_unix_ms = CASE
                    WHEN ?2 IN ('RUNNING', 'RETRYING')
                    THEN COALESCE(started_at_unix_ms, ?4)
                    ELSE started_at_unix_ms
                END,
                completed_at_unix_ms = CASE
                    WHEN ?5 THEN COALESCE(completed_at_unix_ms, ?4)
                    ELSE completed_at_unix_ms
                END,
                error_code = CASE
                    WHEN ?6 <> '' THEN ?6
                    WHEN ?7 THEN ''
                    ELSE error_code
                END,
                error_message = CASE
                    WHEN ?6 <> '' THEN ?8
                    WHEN ?7 THEN ''
                    ELSE error_message
                END,
                evidence_json = COALESCE(?9, evidence_json)
            WHERE id = ?1
            "#,
            params![
                execution_id,
                input.state,
                input.attempt,
                input.occurred_at_unix_ms,
                completed,
                input.error_code,
                clear_error,
                input.message,
                input.evidence_json,
            ],
        )
        .map_err(|error| format!("update mission action execution: {error}"))?;
        insert_action_event(
            &tx,
            &input.event_id,
            &execution_id,
            &input.state,
            input.attempt,
            "atlas_agent",
            input.occurred_at_unix_ms,
            &input.error_code,
            &input.message,
            input.evidence_json.as_deref(),
        )?;

        if action_type == "HOLD_AT_ARRIVAL"
            && current_state != "SUCCEEDED"
            && input.state == "SUCCEEDED"
        {
            mark_incident_response_on_scene(
                &tx,
                &input.mission_run_id,
                &execution_id,
                input.action_sequence,
                input.occurred_at_unix_ms,
            )?;
        }

        tx.commit()
            .map_err(|error| format!("commit mission action update: {error}"))?;
        drop(connection);
        self.mission_run(&input.mission_run_id)
    }
}

pub(super) fn create_mission_action_executions(
    tx: &Transaction<'_>,
    mission_run_id: &str,
    mission_plan_id: &str,
    now: i64,
) -> Result<(), String> {
    let actions = {
        let mut statement = tx
            .prepare(
                "SELECT sequence, action_type, params_json FROM mission_actions WHERE mission_plan_id = ?1 ORDER BY sequence",
            )
            .map_err(|error| format!("prepare runtime mission actions: {error}"))?;
        let values = statement
            .query_map([mission_plan_id], |row| {
                Ok((
                    row.get::<_, u32>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, String>(2)?,
                ))
            })
            .map_err(|error| format!("query runtime mission actions: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read runtime mission actions: {error}"))?;
        values
    };

    for (action_sequence, action_type, params_json) in actions {
        if !RUNTIME_ACTION_TYPES.contains(&action_type.as_str()) {
            continue;
        }
        let values: Value = serde_json::from_str(&params_json)
            .map_err(|error| format!("decode runtime mission action parameters: {error}"))?;
        let max_attempts = values
            .get("maxAttempts")
            .and_then(Value::as_u64)
            .and_then(|value| u32::try_from(value).ok())
            .filter(|value| (1..=5).contains(value))
            .ok_or_else(|| format!("{action_type} requires maxAttempts between 1 and 5"))?;
        let failure_policy = values
            .get("failurePolicy")
            .and_then(Value::as_str)
            .ok_or_else(|| format!("{action_type} requires an explicit failurePolicy"))?;
        validate_failure_policy(failure_policy)?;

        let execution_id = generate_id(tx)?;
        tx.execute(
            "INSERT INTO mission_action_executions (id, mission_run_id, mission_plan_id, action_sequence, action_type, state, attempt, max_attempts, failure_policy, requested_at_unix_ms, updated_at_unix_ms) VALUES (?1, ?2, ?3, ?4, ?5, 'REQUESTED', 0, ?6, ?7, ?8, ?8)",
            params![execution_id, mission_run_id, mission_plan_id, action_sequence, action_type, max_attempts, failure_policy, now],
        )
        .map_err(|error| format!("insert mission action execution: {error}"))?;
        insert_action_event(
            tx,
            &generate_id(tx)?,
            &execution_id,
            "REQUESTED",
            0,
            "atlas_native",
            now,
            "",
            "Mission action requested from the reviewed immutable plan",
            None,
        )?;
    }
    Ok(())
}

pub(super) fn read_mission_action_executions(
    connection: &rusqlite::Connection,
    mission_run_id: &str,
) -> Result<Vec<MissionActionExecutionSnapshot>, String> {
    let mut statement = connection
        .prepare(
            "SELECT id, mission_plan_id, action_sequence, action_type, state, attempt, max_attempts, failure_policy, requested_at_unix_ms, updated_at_unix_ms, started_at_unix_ms, completed_at_unix_ms, error_code, error_message, evidence_json FROM mission_action_executions WHERE mission_run_id = ?1 ORDER BY action_sequence",
        )
        .map_err(|error| format!("prepare mission action executions: {error}"))?;
    let mut executions = statement
        .query_map([mission_run_id], |row| {
            Ok(MissionActionExecutionSnapshot {
                id: row.get(0)?,
                mission_run_id: mission_run_id.to_string(),
                mission_plan_id: row.get(1)?,
                action_sequence: row.get(2)?,
                action_type: row.get(3)?,
                state: row.get(4)?,
                attempt: row.get(5)?,
                max_attempts: row.get(6)?,
                failure_policy: row.get(7)?,
                requested_at_unix_ms: row.get(8)?,
                updated_at_unix_ms: row.get(9)?,
                started_at_unix_ms: row.get(10)?,
                completed_at_unix_ms: row.get(11)?,
                error_code: row.get(12)?,
                error_message: row.get(13)?,
                evidence_json: row.get(14)?,
                events: Vec::new(),
            })
        })
        .map_err(|error| format!("query mission action executions: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read mission action executions: {error}"))?;
    drop(statement);

    for execution in &mut executions {
        let mut events = connection
            .prepare(
                "SELECT id, sequence, state, attempt, source, occurred_at_unix_ms, error_code, message, evidence_json FROM mission_action_execution_events WHERE mission_action_execution_id = ?1 ORDER BY sequence",
            )
            .map_err(|error| format!("prepare mission action execution events: {error}"))?;
        execution.events = events
            .query_map([&execution.id], |row| {
                Ok(MissionActionExecutionEventSnapshot {
                    id: row.get(0)?,
                    sequence: row.get(1)?,
                    state: row.get(2)?,
                    attempt: row.get(3)?,
                    source: row.get(4)?,
                    occurred_at_unix_ms: row.get(5)?,
                    error_code: row.get(6)?,
                    message: row.get(7)?,
                    evidence_json: row.get(8)?,
                })
            })
            .map_err(|error| format!("query mission action execution events: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read mission action execution events: {error}"))?;
    }
    Ok(executions)
}

#[allow(clippy::too_many_arguments)]
fn insert_action_event(
    tx: &Transaction<'_>,
    id: &str,
    execution_id: &str,
    state: &str,
    attempt: u32,
    source: &str,
    occurred_at_unix_ms: i64,
    error_code: &str,
    message: &str,
    evidence_json: Option<&str>,
) -> Result<(), String> {
    let sequence: u32 = tx
        .query_row(
            "SELECT COALESCE(max(sequence), -1) + 1 FROM mission_action_execution_events WHERE mission_action_execution_id = ?1",
            [execution_id],
            |row| row.get(0),
        )
        .map_err(|error| format!("select next mission action event sequence: {error}"))?;
    tx.execute(
        "INSERT INTO mission_action_execution_events (id, mission_action_execution_id, sequence, state, attempt, source, occurred_at_unix_ms, error_code, message, evidence_json) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)",
        params![id, execution_id, sequence, state, attempt, source, occurred_at_unix_ms, error_code, message, evidence_json],
    )
    .map_err(|error| format!("insert mission action execution event: {error}"))?;
    Ok(())
}

fn validate_action_state(state: &str) -> Result<(), String> {
    matches!(
        state,
        "RUNNING" | "RETRYING" | "SUCCEEDED" | "FAILED" | "POLICY_APPLIED"
    )
    .then_some(())
    .ok_or_else(|| format!("unsupported mission action state {state}"))
}

fn validate_action_transition(
    current: &str,
    next: &str,
    current_attempt: u32,
    next_attempt: u32,
) -> Result<(), String> {
    if next_attempt < current_attempt {
        return Err(format!(
            "mission action attempt cannot move backwards from {current_attempt} to {next_attempt}"
        ));
    }
    let allowed = current == next
        || matches!(
            (current, next),
            ("REQUESTED", "RUNNING" | "FAILED")
                | ("RUNNING", "RETRYING" | "SUCCEEDED" | "FAILED")
                | ("RETRYING", "RUNNING" | "FAILED")
                | ("FAILED", "POLICY_APPLIED")
        );
    allowed
        .then_some(())
        .ok_or_else(|| format!("invalid mission action transition {current} -> {next}"))
}

fn validate_failure_policy(value: &str) -> Result<(), String> {
    matches!(value, "RETURN_TO_LAUNCH" | "OPERATOR_INTERVENTION")
        .then_some(())
        .ok_or_else(|| {
            "failurePolicy must be RETURN_TO_LAUNCH or OPERATOR_INTERVENTION".to_string()
        })
}

fn generate_id(tx: &Transaction<'_>) -> Result<String, String> {
    tx.query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate mission action identifier: {error}"))
}

#[cfg(test)]
mod tests {
    use super::{validate_action_transition, validate_failure_policy};

    #[test]
    fn action_lifecycle_rejects_success_without_acknowledged_execution() {
        assert!(validate_action_transition("REQUESTED", "SUCCEEDED", 0, 1).is_err());
        assert!(validate_action_transition("REQUESTED", "RUNNING", 0, 1).is_ok());
        assert!(validate_action_transition("RUNNING", "SUCCEEDED", 1, 1).is_ok());
    }

    #[test]
    fn action_failure_policy_is_explicit_and_bounded() {
        assert!(validate_failure_policy("RETURN_TO_LAUNCH").is_ok());
        assert!(validate_failure_policy("OPERATOR_INTERVENTION").is_ok());
        assert!(validate_failure_policy("").is_err());
        assert!(validate_failure_policy("SILENT_DEFAULT").is_err());
    }
}
