use rusqlite::{params, OptionalExtension, Transaction};
use serde::Serialize;

use super::{unix_time_ms, LocalDatabase};

const TERMINAL_STATES: &[&str] = &["COMPLETED", "FAILED", "CANCELLED", "RTL"];

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct MissionRunSnapshot {
    pub id: String,
    pub mission_id: String,
    pub mission_plan_id: String,
    pub mission_name: String,
    pub template_type: String,
    pub pattern_type: String,
    pub drone_id: String,
    pub drone_name: String,
    pub status: String,
    pub current_waypoint: Option<u32>,
    pub total_waypoints: u32,
    pub upload_progress_percent: f64,
    pub progress_percent: f64,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
    pub uploaded_at_unix_ms: Option<i64>,
    pub started_at_unix_ms: Option<i64>,
    pub paused_at_unix_ms: Option<i64>,
    pub completed_at_unix_ms: Option<i64>,
    pub error_code: String,
    pub error_message: String,
    pub events: Vec<MissionRunEventSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct MissionRunEventSnapshot {
    pub id: String,
    pub sequence: u32,
    pub operation_id: Option<String>,
    pub event_type: String,
    pub state: String,
    pub source: String,
    pub occurred_at_unix_ms: i64,
    pub current_waypoint: Option<u32>,
    pub total_waypoints: Option<u32>,
    pub progress_percent: Option<f64>,
    pub error_code: String,
    pub message: String,
    pub evidence_json: Option<String>,
}

#[derive(Debug)]
pub(crate) struct MissionRunUpdateInput {
    pub event_id: String,
    pub operation_id: String,
    pub mission_run_id: String,
    pub event_type: String,
    pub run_state: String,
    pub occurred_at_unix_ms: i64,
    pub progress_percent: Option<f64>,
    pub current_waypoint: Option<u32>,
    pub total_waypoints: Option<u32>,
    pub error_code: String,
    pub message: String,
    pub evidence_json: Option<String>,
}

#[derive(Debug)]
pub(crate) struct MissionOperationDispatch {
    pub run: MissionRunSnapshot,
    pub operation_id: String,
}

impl LocalDatabase {
    pub(crate) fn create_mission_run(
        &self,
        mission_id: &str,
        drone_id: &str,
        expected_plan_id: Option<&str>,
    ) -> Result<MissionOperationDispatch, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin mission run: {error}"))?;
        let plan_id: String = tx
            .query_row(
                "SELECT generated_plan_id FROM missions WHERE id = ?1 AND status = 'READY'",
                [mission_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("read ready mission plan: {error}"))?
            .ok_or_else(|| "mission must have a READY generated plan before upload".to_string())?;
        if expected_plan_id.is_some_and(|expected| expected != plan_id) {
            return Err(
                "mission plan changed during upload validation; review and retry".to_string(),
            );
        }
        let plan_status: String = tx
            .query_row(
                "SELECT status FROM mission_plans WHERE id = ?1",
                [&plan_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("read mission plan status: {error}"))?;
        if plan_status != "READY" {
            return Err("mission plan is not ready for upload".to_string());
        }
        let drone_exists: bool = tx
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM drones WHERE id = ?1 AND status = 'active')",
                [drone_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("validate mission drone: {error}"))?;
        if !drone_exists {
            return Err("mission target drone is not active or does not exist".to_string());
        }
        let active_run: bool = tx
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM mission_runs WHERE drone_id = ?1 AND completed_at_unix_ms IS NULL)",
                [drone_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("check active vehicle mission: {error}"))?;
        if active_run {
            return Err("drone already has an unfinished mission run".to_string());
        }
        let total_waypoints: u32 = tx
            .query_row(
                "SELECT count(*) FROM mission_items WHERE mission_plan_id = ?1",
                [&plan_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("count mission waypoints: {error}"))?;
        let run_id = generate_id(&tx)?;
        let operation_id = generate_id(&tx)?;
        let now = unix_time_ms();
        tx.execute(
            "INSERT INTO mission_runs (id, mission_id, mission_plan_id, drone_id, status, total_waypoints, created_at_unix_ms, updated_at_unix_ms) VALUES (?1, ?2, ?3, ?4, 'UPLOADING', ?5, ?6, ?6)",
            params![run_id, mission_id, plan_id, drone_id, total_waypoints, now],
        )
        .map_err(|error| format!("insert mission run: {error}"))?;
        insert_event(
            &tx,
            &generate_id(&tx)?,
            &run_id,
            Some(&operation_id),
            "upload_requested",
            "UPLOADING",
            "atlas_native",
            now,
            None,
            Some(total_waypoints),
            None,
            "",
            "Mission upload requested",
            None,
        )?;
        tx.commit()
            .map_err(|error| format!("commit mission run: {error}"))?;
        drop(connection);
        Ok(MissionOperationDispatch {
            run: self.mission_run(&run_id)?,
            operation_id,
        })
    }

    pub(crate) fn mission_run(&self, run_id: &str) -> Result<MissionRunSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        read_run(&connection, run_id)
    }

    pub(crate) fn mission_runs(
        &self,
        mission_id: Option<&str>,
        drone_id: Option<&str>,
        limit: usize,
    ) -> Result<Vec<MissionRunSnapshot>, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare("SELECT id FROM mission_runs WHERE (?1 IS NULL OR mission_id = ?1) AND (?2 IS NULL OR drone_id = ?2) ORDER BY created_at_unix_ms DESC, id DESC LIMIT ?3")
            .map_err(|error| format!("prepare mission runs: {error}"))?;
        let ids = statement
            .query_map(
                params![mission_id, drone_id, limit.clamp(1, 200) as i64],
                |row| row.get::<_, String>(0),
            )
            .map_err(|error| format!("query mission runs: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read mission run identifiers: {error}"))?;
        ids.iter().map(|id| read_run(&connection, id)).collect()
    }

    pub(crate) fn record_mission_operation_requested(
        &self,
        run_id: &str,
        operation: &str,
    ) -> Result<MissionOperationDispatch, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin mission control request: {error}"))?;
        let state: String = tx
            .query_row(
                "SELECT status FROM mission_runs WHERE id = ?1",
                [run_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("read mission run state: {error}"))?
            .ok_or_else(|| format!("mission run {run_id} was not found"))?;
        validate_operation(operation, &state)?;
        let operation_id = generate_id(&tx)?;
        let now = unix_time_ms();
        insert_event(
            &tx,
            &generate_id(&tx)?,
            run_id,
            Some(&operation_id),
            &format!("{operation}_requested"),
            &state,
            "atlas_native",
            now,
            None,
            None,
            None,
            "",
            &format!("Mission {} requested", operation.replace('_', " ")),
            None,
        )?;
        tx.execute(
            "UPDATE mission_runs SET updated_at_unix_ms = ?2 WHERE id = ?1",
            params![run_id, now],
        )
        .map_err(|error| format!("touch mission run: {error}"))?;
        tx.commit()
            .map_err(|error| format!("commit mission control request: {error}"))?;
        drop(connection);
        Ok(MissionOperationDispatch {
            run: self.mission_run(run_id)?,
            operation_id,
        })
    }

    pub(crate) fn record_mission_delivery_failure(
        &self,
        run_id: &str,
        operation_id: &str,
        operation: &str,
        message: &str,
    ) -> Result<MissionRunSnapshot, String> {
        let current = self.mission_run(run_id)?;
        let fatal = matches!(operation, "upload" | "start");
        self.apply_mission_run_update(&MissionRunUpdateInput {
            event_id: format!("delivery-{operation_id}"),
            operation_id: operation_id.to_string(),
            mission_run_id: run_id.to_string(),
            event_type: "delivery_failed".to_string(),
            run_state: if fatal { "FAILED" } else { &current.status }.to_string(),
            occurred_at_unix_ms: unix_time_ms(),
            progress_percent: None,
            current_waypoint: current.current_waypoint,
            total_waypoints: Some(current.total_waypoints),
            error_code: "AGENT_UNAVAILABLE".to_string(),
            message: message.to_string(),
            evidence_json: None,
        })
    }

    pub(crate) fn apply_mission_run_update(
        &self,
        input: &MissionRunUpdateInput,
    ) -> Result<MissionRunSnapshot, String> {
        validate_state(&input.run_state)?;
        if let Some(progress) = input.progress_percent {
            if !(0.0..=100.0).contains(&progress) || !progress.is_finite() {
                return Err("mission progress must be between 0 and 100".to_string());
            }
        }
        if let (Some(current), Some(total)) = (input.current_waypoint, input.total_waypoints) {
            if current > total {
                return Err("current waypoint cannot exceed total waypoints".to_string());
            }
        }
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin mission run update: {error}"))?;
        let existing: Option<String> = tx
            .query_row(
                "SELECT mission_run_id FROM mission_run_events WHERE id = ?1",
                [&input.event_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("check mission event idempotency: {error}"))?;
        if let Some(existing_run) = existing {
            if existing_run != input.mission_run_id {
                return Err("mission event id is already used by another run".to_string());
            }
            tx.commit()
                .map_err(|error| format!("commit duplicate mission event: {error}"))?;
            drop(connection);
            return self.mission_run(&input.mission_run_id);
        }
        let current_state: String = tx
            .query_row(
                "SELECT status FROM mission_runs WHERE id = ?1",
                [&input.mission_run_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("read mission run state: {error}"))?
            .ok_or_else(|| format!("mission run {} was not found", input.mission_run_id))?;
        validate_transition(&current_state, &input.run_state)?;
        let terminal = TERMINAL_STATES.contains(&input.run_state.as_str());
        let upload_progress = if input.event_type == "upload_progress" {
            input.progress_percent
        } else if input.run_state == "READY" {
            Some(100.0)
        } else {
            None
        };
        let execution_progress = if input.event_type == "progress" {
            input.progress_percent
        } else if input.run_state == "COMPLETED" {
            Some(100.0)
        } else {
            None
        };
        let clear_error = matches!(
            input.event_type.as_str(),
            "uploaded"
                | "started"
                | "progress"
                | "paused"
                | "resumed"
                | "completed"
                | "cancelled"
                | "rtl_started"
        );
        tx.execute(
            "UPDATE mission_runs SET status = ?2, current_waypoint = COALESCE(?3, current_waypoint), total_waypoints = COALESCE(?4, total_waypoints), upload_progress_percent = COALESCE(?5, upload_progress_percent), progress_percent = COALESCE(?6, progress_percent), updated_at_unix_ms = ?7, uploaded_at_unix_ms = CASE WHEN ?2 = 'READY' THEN COALESCE(uploaded_at_unix_ms, ?7) ELSE uploaded_at_unix_ms END, started_at_unix_ms = CASE WHEN ?2 = 'RUNNING' THEN COALESCE(started_at_unix_ms, ?7) ELSE started_at_unix_ms END, paused_at_unix_ms = CASE WHEN ?2 = 'PAUSED' THEN ?7 ELSE paused_at_unix_ms END, completed_at_unix_ms = CASE WHEN ?8 THEN COALESCE(completed_at_unix_ms, ?7) ELSE completed_at_unix_ms END, error_code = CASE WHEN ?9 <> '' THEN ?9 WHEN ?11 THEN '' ELSE error_code END, error_message = CASE WHEN ?9 <> '' THEN ?10 WHEN ?11 THEN '' ELSE error_message END WHERE id = ?1",
            params![input.mission_run_id, input.run_state, input.current_waypoint, input.total_waypoints, upload_progress, execution_progress, input.occurred_at_unix_ms, terminal, input.error_code, input.message, clear_error],
        )
        .map_err(|error| format!("update mission run: {error}"))?;
        insert_event(
            &tx,
            &input.event_id,
            &input.mission_run_id,
            (!input.operation_id.is_empty()).then_some(input.operation_id.as_str()),
            &input.event_type,
            &input.run_state,
            if input.event_id.starts_with("delivery-") {
                "atlas_native"
            } else {
                "atlas_agent"
            },
            input.occurred_at_unix_ms,
            input.current_waypoint,
            input.total_waypoints,
            input.progress_percent,
            &input.error_code,
            &input.message,
            input.evidence_json.as_deref(),
        )?;
        tx.commit()
            .map_err(|error| format!("commit mission run update: {error}"))?;
        drop(connection);
        self.mission_run(&input.mission_run_id)
    }
}

fn read_run(connection: &rusqlite::Connection, run_id: &str) -> Result<MissionRunSnapshot, String> {
    let mut run = connection
        .query_row(
            "SELECT r.id, r.mission_id, r.mission_plan_id, m.name, m.template_type, p.pattern_type, r.drone_id, d.name, r.status, r.current_waypoint, r.total_waypoints, r.upload_progress_percent, r.progress_percent, r.created_at_unix_ms, r.updated_at_unix_ms, r.uploaded_at_unix_ms, r.started_at_unix_ms, r.paused_at_unix_ms, r.completed_at_unix_ms, r.error_code, r.error_message FROM mission_runs r JOIN missions m ON m.id = r.mission_id JOIN mission_plans p ON p.id = r.mission_plan_id JOIN drones d ON d.id = r.drone_id WHERE r.id = ?1",
            [run_id],
            |row| {
                Ok(MissionRunSnapshot {
                    id: row.get(0)?,
                    mission_id: row.get(1)?,
                    mission_plan_id: row.get(2)?,
                    mission_name: row.get(3)?,
                    template_type: row.get(4)?,
                    pattern_type: row.get(5)?,
                    drone_id: row.get(6)?,
                    drone_name: row.get(7)?,
                    status: row.get(8)?,
                    current_waypoint: row.get(9)?,
                    total_waypoints: row.get(10)?,
                    upload_progress_percent: row.get(11)?,
                    progress_percent: row.get(12)?,
                    created_at_unix_ms: row.get(13)?,
                    updated_at_unix_ms: row.get(14)?,
                    uploaded_at_unix_ms: row.get(15)?,
                    started_at_unix_ms: row.get(16)?,
                    paused_at_unix_ms: row.get(17)?,
                    completed_at_unix_ms: row.get(18)?,
                    error_code: row.get(19)?,
                    error_message: row.get(20)?,
                    events: Vec::new(),
                })
            },
        )
        .optional()
        .map_err(|error| format!("read mission run: {error}"))?
        .ok_or_else(|| format!("mission run {run_id} was not found"))?;
    let mut statement = connection
        .prepare("SELECT id, sequence, operation_id, event_type, state, source, occurred_at_unix_ms, current_waypoint, total_waypoints, progress_percent, error_code, message, evidence_json FROM mission_run_events WHERE mission_run_id = ?1 ORDER BY sequence")
        .map_err(|error| format!("prepare mission run events: {error}"))?;
    run.events = statement
        .query_map([run_id], |row| {
            Ok(MissionRunEventSnapshot {
                id: row.get(0)?,
                sequence: row.get(1)?,
                operation_id: row.get(2)?,
                event_type: row.get(3)?,
                state: row.get(4)?,
                source: row.get(5)?,
                occurred_at_unix_ms: row.get(6)?,
                current_waypoint: row.get(7)?,
                total_waypoints: row.get(8)?,
                progress_percent: row.get(9)?,
                error_code: row.get(10)?,
                message: row.get(11)?,
                evidence_json: row.get(12)?,
            })
        })
        .map_err(|error| format!("query mission run events: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read mission run events: {error}"))?;
    Ok(run)
}

#[allow(clippy::too_many_arguments)]
fn insert_event(
    tx: &Transaction<'_>,
    id: &str,
    run_id: &str,
    operation_id: Option<&str>,
    event_type: &str,
    state: &str,
    source: &str,
    occurred_at: i64,
    current_waypoint: Option<u32>,
    total_waypoints: Option<u32>,
    progress_percent: Option<f64>,
    error_code: &str,
    message: &str,
    evidence_json: Option<&str>,
) -> Result<(), String> {
    let sequence: u32 = tx
        .query_row(
            "SELECT COALESCE(max(sequence), -1) + 1 FROM mission_run_events WHERE mission_run_id = ?1",
            [run_id],
            |row| row.get(0),
        )
        .map_err(|error| format!("select next mission event sequence: {error}"))?;
    tx.execute(
        "INSERT INTO mission_run_events (id, mission_run_id, sequence, event_type, state, occurred_at_unix_ms, current_waypoint, message, evidence_json, operation_id, source, progress_percent, total_waypoints, error_code) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14)",
        params![id, run_id, sequence, event_type, state, occurred_at, current_waypoint, message, evidence_json, operation_id, source, progress_percent, total_waypoints, error_code],
    )
    .map_err(|error| format!("insert mission run event: {error}"))?;
    Ok(())
}

fn generate_id(tx: &Transaction<'_>) -> Result<String, String> {
    tx.query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate mission lifecycle identifier: {error}"))
}

fn validate_operation(operation: &str, state: &str) -> Result<(), String> {
    let allowed = match operation {
        "start" => state == "READY",
        "pause" => state == "RUNNING",
        "resume" => state == "PAUSED",
        "cancel" => matches!(state, "READY" | "RUNNING" | "PAUSED"),
        "return_to_launch" => matches!(state, "RUNNING" | "PAUSED"),
        _ => return Err("unsupported mission operation".to_string()),
    };
    allowed
        .then_some(())
        .ok_or_else(|| format!("cannot {operation} a mission run in {state} state"))
}

fn validate_state(state: &str) -> Result<(), String> {
    matches!(
        state,
        "UPLOADING" | "READY" | "RUNNING" | "PAUSED" | "COMPLETED" | "FAILED" | "CANCELLED" | "RTL"
    )
    .then_some(())
    .ok_or_else(|| format!("unsupported mission run state {state}"))
}

fn validate_transition(current: &str, next: &str) -> Result<(), String> {
    let allowed = current == next
        || matches!(
            (current, next),
            ("UPLOADING", "READY" | "FAILED")
                | ("READY", "RUNNING" | "CANCELLED" | "FAILED")
                | (
                    "RUNNING",
                    "PAUSED" | "COMPLETED" | "CANCELLED" | "RTL" | "FAILED"
                )
                | ("PAUSED", "RUNNING" | "CANCELLED" | "RTL" | "FAILED")
        );
    allowed
        .then_some(())
        .ok_or_else(|| format!("invalid mission run transition {current} -> {next}"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::database::{CreateMissionInput, LocalDatabase};
    use serde_json::json;

    #[test]
    fn run_lifecycle_is_durable_and_reusable() {
        let path = std::env::temp_dir().join(format!(
            "atlas-mission-run-{}-{}.db",
            std::process::id(),
            unix_time_ms()
        ));
        let database = LocalDatabase::open_path(path.clone()).unwrap();
        {
            let connection = database.connection.lock().unwrap();
            connection.execute("INSERT INTO drones (id, name, vehicle_type, created_at_unix_ms, updated_at_unix_ms) VALUES ('drone-1', 'SITL One', 'multicopter', 1, 1)", []).unwrap();
        }
        let mission = database
            .create_mission(&CreateMissionInput {
                template_type: "WAYPOINT".into(),
                name: "Run lifecycle".into(),
                description: String::new(),
                selected_pattern: None,
                params: json!({"defaultAltitudeMeters": 25, "waypoints": [{"latitude": 51.0, "longitude": -0.1}]}),
            })
            .unwrap();
        let plan = database.plan_mission(&mission.id).unwrap();
        assert_eq!(
            database
                .create_mission_run(&mission.id, "drone-1", Some("stale-plan"))
                .unwrap_err(),
            "mission plan changed during upload validation; review and retry"
        );
        assert!(database
            .mission_runs(Some(&mission.id), None, 10)
            .unwrap()
            .is_empty());
        let created = database
            .create_mission_run(&mission.id, "drone-1", Some(&plan.id))
            .unwrap();
        assert_eq!(created.run.status, "UPLOADING");
        assert!(database
            .create_mission_run(&mission.id, "drone-1", None)
            .is_err());
        let ready = database
            .apply_mission_run_update(&MissionRunUpdateInput {
                event_id: "uploaded-1".into(),
                operation_id: created.operation_id,
                mission_run_id: created.run.id.clone(),
                event_type: "uploaded".into(),
                run_state: "READY".into(),
                occurred_at_unix_ms: 10,
                progress_percent: Some(100.0),
                current_waypoint: None,
                total_waypoints: Some(1),
                error_code: String::new(),
                message: "Uploaded".into(),
                evidence_json: None,
            })
            .unwrap();
        assert_eq!(ready.status, "READY");
        assert_eq!(ready.upload_progress_percent, 100.0);
        let start = database
            .record_mission_operation_requested(&ready.id, "start")
            .unwrap();
        let running = database
            .apply_mission_run_update(&MissionRunUpdateInput {
                event_id: "started-1".into(),
                operation_id: start.operation_id,
                mission_run_id: ready.id.clone(),
                event_type: "started".into(),
                run_state: "RUNNING".into(),
                occurred_at_unix_ms: 20,
                progress_percent: Some(0.0),
                current_waypoint: Some(0),
                total_waypoints: Some(1),
                error_code: String::new(),
                message: "Started".into(),
                evidence_json: None,
            })
            .unwrap();
        assert_eq!(running.status, "RUNNING");
        let completed = database
            .apply_mission_run_update(&MissionRunUpdateInput {
                event_id: "completed-1".into(),
                operation_id: String::new(),
                mission_run_id: ready.id.clone(),
                event_type: "completed".into(),
                run_state: "COMPLETED".into(),
                occurred_at_unix_ms: 30,
                progress_percent: Some(100.0),
                current_waypoint: Some(1),
                total_waypoints: Some(1),
                error_code: String::new(),
                message: "Completed".into(),
                evidence_json: None,
            })
            .unwrap();
        assert_eq!(completed.status, "COMPLETED");
        assert!(completed.completed_at_unix_ms.is_some());
        assert!(completed.events.len() >= 5);
        let second = database
            .create_mission_run(&mission.id, "drone-1", None)
            .unwrap();
        assert_ne!(second.run.id, completed.id);
        drop(database);
        let _ = std::fs::remove_file(path);
    }
}
