use rusqlite::{params, OptionalExtension, Transaction};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use super::LocalDatabase;

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct StartEvidenceRecordingInput {
    pub drone_id: String,
    pub incident_id: Option<String>,
    pub mission_id: Option<String>,
    pub mission_run_id: Option<String>,
}

#[derive(Debug, Clone)]
pub(crate) struct CreateEvidenceRecordingInput {
    pub source_id: String,
    pub evidence_root: String,
    pub drone_id: String,
    pub incident_id: Option<String>,
    pub mission_id: Option<String>,
    pub mission_run_id: Option<String>,
    pub segment_duration_seconds: u64,
    pub warning_free_bytes: u64,
    pub stop_free_bytes: u64,
    pub requested_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct EvidenceRecordingSessionSnapshot {
    pub id: String,
    pub source_id: String,
    pub status: String,
    pub evidence_root: String,
    pub drone_id: String,
    pub incident_id: Option<String>,
    pub mission_id: Option<String>,
    pub mission_run_id: Option<String>,
    pub requested_by: String,
    pub segment_duration_seconds: u64,
    pub warning_free_bytes: u64,
    pub stop_free_bytes: u64,
    pub requested_at_unix_ms: i64,
    pub started_at_unix_ms: Option<i64>,
    pub stopped_at_unix_ms: Option<i64>,
    pub finalized_segment_count: u64,
    pub total_bytes: u64,
    pub last_segment_ended_at_unix_ms: Option<i64>,
    pub stop_reason: String,
    pub error_code: String,
    pub error_message: String,
    pub segments: Vec<EvidenceRecordingSegmentSnapshot>,
    pub gaps: Vec<EvidenceGapEventSnapshot>,
    pub events: Vec<EvidenceRecordingEventSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct EvidenceRecordingSegmentSnapshot {
    pub id: String,
    pub sequence: u64,
    pub state: String,
    pub temporary_relative_path: String,
    pub final_relative_path: String,
    pub source_started_at_unix_ms: i64,
    pub source_ended_at_unix_ms: i64,
    pub byte_length: u64,
    pub sha256: String,
    pub finalized_at_unix_ms: Option<i64>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct EvidenceGapEventSnapshot {
    pub id: String,
    pub sequence: u64,
    pub cause: String,
    pub gap_started_at_unix_ms: i64,
    pub gap_ended_at_unix_ms: Option<i64>,
    pub details: Value,
    pub occurred_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct EvidenceRecordingEventSnapshot {
    pub id: String,
    pub sequence: u64,
    pub event_type: String,
    pub state: String,
    pub actor: String,
    pub message: String,
    pub evidence: Value,
    pub occurred_at_unix_ms: i64,
}

#[derive(Debug, Clone)]
pub(crate) struct SegmentFinalizationInput {
    pub recording_session_id: String,
    pub sequence: u64,
    pub temporary_relative_path: String,
    pub final_relative_path: String,
    pub source_started_at_unix_ms: i64,
    pub source_ended_at_unix_ms: i64,
    pub byte_length: u64,
    pub sha256: String,
    pub observed_at_unix_ms: i64,
}

struct ValidatedRecordingAssociations {
    incident_id: Option<String>,
    mission_id: Option<String>,
    mission_run_id: Option<String>,
}

struct RecordingStateTransition<'a> {
    session_id: &'a str,
    expected_state: &'a str,
    next_state: &'a str,
    event_type: &'a str,
    message: &'a str,
    error_code: &'a str,
    stop_reason: &'a str,
    occurred_at_unix_ms: i64,
}

struct RecordingEventInput<'a> {
    session_id: &'a str,
    event_type: &'a str,
    state: &'a str,
    actor: &'a str,
    message: &'a str,
    evidence: &'a Value,
    occurred_at_unix_ms: i64,
}

impl LocalDatabase {
    pub(crate) fn create_evidence_recording(
        &self,
        input: &CreateEvidenceRecordingInput,
    ) -> Result<EvidenceRecordingSessionSnapshot, String> {
        validate_create_input(input)?;
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence recording request: {error}"))?;
        let associations = validate_recording_associations(
            &tx,
            &input.drone_id,
            input.incident_id.as_deref(),
            input.mission_id.as_deref(),
            input.mission_run_id.as_deref(),
        )?;
        let session_id = generate_id(&tx)?;
        tx.execute(
            r#"
            INSERT INTO evidence_recording_sessions (
                id, source_id, status, evidence_root, drone_id, incident_id,
                mission_id, mission_run_id, requested_by, segment_duration_seconds,
                warning_free_bytes, stop_free_bytes, requested_at_unix_ms,
                created_at_unix_ms, updated_at_unix_ms
            ) VALUES (?1, ?2, 'REQUESTED', ?3, ?4, ?5, ?6, ?7,
                      'local_operator', ?8, ?9, ?10, ?11, ?11, ?11)
            "#,
            params![
                session_id,
                input.source_id,
                input.evidence_root,
                input.drone_id,
                associations.incident_id,
                associations.mission_id,
                associations.mission_run_id,
                input.segment_duration_seconds as i64,
                input.warning_free_bytes as i64,
                input.stop_free_bytes as i64,
                input.requested_at_unix_ms,
            ],
        )
        .map_err(|error| {
            if is_unique_constraint(&error) {
                format!(
                    "video source {} already has an active evidence recording",
                    input.source_id
                )
            } else {
                format!("insert evidence recording request: {error}")
            }
        })?;
        insert_recording_event(
            &tx,
            &RecordingEventInput {
                session_id: &session_id,
                event_type: "REQUESTED",
                state: "REQUESTED",
                actor: "local_operator",
                message: "Local source recording requested",
                evidence: &json!({
                    "sourceId": input.source_id,
                    "segmentDurationSeconds": input.segment_duration_seconds,
                    "warningFreeBytes": input.warning_free_bytes,
                    "stopFreeBytes": input.stop_free_bytes,
                }),
                occurred_at_unix_ms: input.requested_at_unix_ms,
            },
        )?;
        tx.commit()
            .map_err(|error| format!("commit evidence recording request: {error}"))?;
        drop(connection);
        self.evidence_recording_session(&session_id)
    }

    pub(crate) fn mark_evidence_recording_running(
        &self,
        session_id: &str,
        started_at_unix_ms: i64,
    ) -> Result<(), String> {
        self.update_recording_state(
            &RecordingStateTransition {
                session_id,
                expected_state: "REQUESTED",
                next_state: "RUNNING",
                event_type: "RUNNING",
                message: "Local recorder confirmed source capture into a temporary segment",
                error_code: "",
                stop_reason: "",
                occurred_at_unix_ms: started_at_unix_ms,
            },
            false,
        )
    }

    pub(crate) fn request_evidence_recording_stop(
        &self,
        session_id: &str,
        requested_at_unix_ms: i64,
    ) -> Result<EvidenceRecordingSessionSnapshot, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence recording stop request: {error}"))?;
        let state: String = tx
            .query_row(
                "SELECT status FROM evidence_recording_sessions WHERE id = ?1",
                [session_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("read evidence recording before stop: {error}"))?
            .ok_or_else(|| format!("evidence recording {session_id} was not found"))?;
        if !matches!(state.as_str(), "REQUESTED" | "RUNNING") {
            return Err(format!("evidence recording is already {state}"));
        }
        insert_recording_event(
            &tx,
            &RecordingEventInput {
                session_id,
                event_type: "STOP_REQUESTED",
                state: &state,
                actor: "local_operator",
                message: "Operator requested a graceful evidence recording stop",
                evidence: &json!({ "graceful": true }),
                occurred_at_unix_ms: requested_at_unix_ms,
            },
        )?;
        tx.commit()
            .map_err(|error| format!("commit evidence recording stop request: {error}"))?;
        drop(connection);
        self.evidence_recording_session(session_id)
    }

    pub(crate) fn begin_evidence_segment_finalization(
        &self,
        input: &SegmentFinalizationInput,
    ) -> Result<bool, String> {
        if input.sha256.len() != 64 || !input.sha256.bytes().all(|byte| byte.is_ascii_hexdigit()) {
            return Err("evidence segment SHA-256 must contain 64 hexadecimal characters".into());
        }
        if input.source_ended_at_unix_ms < input.source_started_at_unix_ms {
            return Err("evidence segment end cannot precede its start".into());
        }
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence segment finalization: {error}"))?;
        let existing: Option<(String, String, i64)> = tx
            .query_row(
                "SELECT state, sha256, byte_length FROM evidence_recording_segments WHERE recording_session_id = ?1 AND sequence = ?2",
                params![input.recording_session_id, input.sequence as i64],
                |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
            )
            .optional()
            .map_err(|error| format!("read existing evidence segment manifest: {error}"))?;
        if let Some((state, checksum, byte_length)) = existing {
            if checksum != input.sha256 || byte_length != input.byte_length as i64 {
                return Err(format!(
                    "evidence segment {} manifest conflicts with the finalized bytes",
                    input.sequence
                ));
            }
            return Ok(state == "FINALIZING");
        }
        let session_state: String = tx
            .query_row(
                "SELECT status FROM evidence_recording_sessions WHERE id = ?1",
                [&input.recording_session_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("read evidence recording for segment: {error}"))?
            .ok_or_else(|| {
                format!(
                    "evidence recording {} was not found",
                    input.recording_session_id
                )
            })?;
        if session_state == "SUCCEEDED" {
            return Err("cannot add a segment to a completed evidence recording".into());
        }
        tx.execute(
            r#"
            INSERT INTO evidence_recording_segments (
                id, recording_session_id, sequence, state, temporary_relative_path,
                final_relative_path, source_started_at_unix_ms,
                source_ended_at_unix_ms, byte_length, sha256,
                created_at_unix_ms, updated_at_unix_ms
            ) VALUES (?1, ?2, ?3, 'FINALIZING', ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?10)
            "#,
            params![
                generate_id(&tx)?,
                input.recording_session_id,
                input.sequence as i64,
                input.temporary_relative_path,
                input.final_relative_path,
                input.source_started_at_unix_ms,
                input.source_ended_at_unix_ms,
                input.byte_length as i64,
                input.sha256.to_lowercase(),
                input.observed_at_unix_ms,
            ],
        )
        .map_err(|error| format!("insert finalizing evidence segment manifest: {error}"))?;
        tx.commit()
            .map_err(|error| format!("commit finalizing evidence segment manifest: {error}"))?;
        Ok(true)
    }

    pub(crate) fn complete_evidence_segment_finalization(
        &self,
        session_id: &str,
        sequence: u64,
        finalized_at_unix_ms: i64,
    ) -> Result<(), String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin verified evidence segment update: {error}"))?;
        let segment: (String, i64, i64, String) = tx
            .query_row(
                "SELECT state, byte_length, source_ended_at_unix_ms, sha256 FROM evidence_recording_segments WHERE recording_session_id = ?1 AND sequence = ?2",
                params![session_id, sequence as i64],
                |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?)),
            )
            .optional()
            .map_err(|error| format!("read finalizing evidence segment: {error}"))?
            .ok_or_else(|| format!("evidence segment {sequence} was not found"))?;
        if segment.0 == "LOCAL_VERIFIED" {
            return Ok(());
        }
        let changed = tx
            .execute(
                "UPDATE evidence_recording_segments SET state = 'LOCAL_VERIFIED', finalized_at_unix_ms = ?3, updated_at_unix_ms = ?3 WHERE recording_session_id = ?1 AND sequence = ?2 AND state = 'FINALIZING'",
                params![session_id, sequence as i64, finalized_at_unix_ms],
            )
            .map_err(|error| format!("mark evidence segment verified: {error}"))?;
        if changed != 1 {
            return Err(format!("evidence segment {sequence} could not be verified"));
        }
        tx.execute(
            r#"
            UPDATE evidence_recording_sessions
            SET finalized_segment_count = finalized_segment_count + 1,
                total_bytes = total_bytes + ?2,
                last_segment_ended_at_unix_ms = CASE
                    WHEN last_segment_ended_at_unix_ms IS NULL OR last_segment_ended_at_unix_ms < ?3
                    THEN ?3 ELSE last_segment_ended_at_unix_ms END,
                updated_at_unix_ms = ?4
            WHERE id = ?1
            "#,
            params![session_id, segment.1, segment.2, finalized_at_unix_ms],
        )
        .map_err(|error| format!("update evidence recording totals: {error}"))?;
        insert_recording_event(
            &tx,
            &RecordingEventInput {
                session_id,
                event_type: "SEGMENT_FINALIZED",
                state: "RUNNING",
                actor: "atlas_native_recorder",
                message: &format!("Evidence segment {sequence} finalized and checksum verified"),
                evidence: &json!({
                    "segmentSequence": sequence,
                    "byteLength": segment.1,
                    "sha256": segment.3,
                }),
                occurred_at_unix_ms: finalized_at_unix_ms,
            },
        )?;
        tx.commit()
            .map_err(|error| format!("commit verified evidence segment: {error}"))
    }

    pub(crate) fn record_evidence_gap(
        &self,
        session_id: &str,
        cause: &str,
        gap_started_at_unix_ms: i64,
        gap_ended_at_unix_ms: Option<i64>,
        details: &Value,
        occurred_at_unix_ms: i64,
    ) -> Result<EvidenceGapEventSnapshot, String> {
        if cause.trim().is_empty() {
            return Err("evidence gap cause cannot be empty".into());
        }
        if gap_ended_at_unix_ms.is_some_and(|end| end < gap_started_at_unix_ms) {
            return Err("evidence gap end cannot precede its start".into());
        }
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence gap event: {error}"))?;
        let sequence = next_sequence(&tx, "evidence_gap_events", session_id)?;
        let id = generate_id(&tx)?;
        let details_json = serde_json::to_string(details)
            .map_err(|error| format!("serialize evidence gap details: {error}"))?;
        tx.execute(
            "INSERT INTO evidence_gap_events (id, recording_session_id, sequence, cause, gap_started_at_unix_ms, gap_ended_at_unix_ms, details_json, occurred_at_unix_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
            params![id, session_id, sequence as i64, cause, gap_started_at_unix_ms, gap_ended_at_unix_ms, details_json, occurred_at_unix_ms],
        )
        .map_err(|error| format!("insert evidence gap event: {error}"))?;
        let state: String = tx
            .query_row(
                "SELECT status FROM evidence_recording_sessions WHERE id = ?1",
                [session_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("read recording state for evidence gap: {error}"))?;
        insert_recording_event(
            &tx,
            &RecordingEventInput {
                session_id,
                event_type: "EVIDENCE_GAP",
                state: &state,
                actor: "atlas_native_recorder",
                message: &format!("Evidence gap recorded: {cause}"),
                evidence: &json!({
                    "gapSequence": sequence,
                    "cause": cause,
                    "gapStartedAtUnixMs": gap_started_at_unix_ms,
                    "gapEndedAtUnixMs": gap_ended_at_unix_ms,
                    "details": details,
                }),
                occurred_at_unix_ms,
            },
        )?;
        tx.commit()
            .map_err(|error| format!("commit evidence gap event: {error}"))?;
        Ok(EvidenceGapEventSnapshot {
            id,
            sequence,
            cause: cause.to_string(),
            gap_started_at_unix_ms,
            gap_ended_at_unix_ms,
            details: details.clone(),
            occurred_at_unix_ms,
        })
    }

    pub(crate) fn succeed_evidence_recording(
        &self,
        session_id: &str,
        stop_reason: &str,
        stopped_at_unix_ms: i64,
    ) -> Result<(), String> {
        self.update_recording_state(
            &RecordingStateTransition {
                session_id,
                expected_state: "RUNNING",
                next_state: "SUCCEEDED",
                event_type: "SUCCEEDED",
                message: "Evidence recording stopped; every published segment is checksum verified",
                error_code: "",
                stop_reason,
                occurred_at_unix_ms: stopped_at_unix_ms,
            },
            true,
        )
    }

    pub(crate) fn fail_evidence_recording(
        &self,
        session_id: &str,
        error_code: &str,
        error_message: &str,
        stop_reason: &str,
        stopped_at_unix_ms: i64,
    ) -> Result<(), String> {
        let current = self.evidence_recording_session(session_id)?;
        if matches!(current.status.as_str(), "SUCCEEDED" | "FAILED") {
            return Ok(());
        }
        self.update_recording_state(
            &RecordingStateTransition {
                session_id,
                expected_state: &current.status,
                next_state: "FAILED",
                event_type: "FAILED",
                message: error_message,
                error_code,
                stop_reason,
                occurred_at_unix_ms: stopped_at_unix_ms,
            },
            false,
        )
    }

    pub(crate) fn evidence_recording_session(
        &self,
        session_id: &str,
    ) -> Result<EvidenceRecordingSessionSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        read_recording_session(&connection, session_id)
    }

    pub(crate) fn evidence_recording_for_context(
        &self,
        drone_id: Option<&str>,
        incident_id: Option<&str>,
        mission_id: Option<&str>,
        mission_run_id: Option<&str>,
    ) -> Result<Option<EvidenceRecordingSessionSnapshot>, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let id: Option<String> = connection
            .query_row(
                r#"
                SELECT id FROM evidence_recording_sessions
                WHERE (?1 IS NULL OR drone_id = ?1)
                  AND (?2 IS NULL OR incident_id = ?2)
                  AND (?3 IS NULL OR mission_id = ?3)
                  AND (?4 IS NULL OR mission_run_id = ?4)
                ORDER BY CASE status WHEN 'RUNNING' THEN 0 WHEN 'REQUESTED' THEN 1 ELSE 2 END,
                         requested_at_unix_ms DESC, rowid DESC
                LIMIT 1
                "#,
                params![drone_id, incident_id, mission_id, mission_run_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("query evidence recording context: {error}"))?;
        id.map(|id| read_recording_session(&connection, &id))
            .transpose()
    }

    pub(crate) fn unfinished_evidence_recordings(
        &self,
    ) -> Result<Vec<EvidenceRecordingSessionSnapshot>, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare("SELECT id FROM evidence_recording_sessions WHERE status IN ('REQUESTED', 'RUNNING') ORDER BY requested_at_unix_ms")
            .map_err(|error| format!("prepare unfinished evidence recordings: {error}"))?;
        let ids = statement
            .query_map([], |row| row.get::<_, String>(0))
            .map_err(|error| format!("query unfinished evidence recordings: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read unfinished evidence recordings: {error}"))?;
        ids.iter()
            .map(|id| read_recording_session(&connection, id))
            .collect()
    }

    fn update_recording_state(
        &self,
        transition: &RecordingStateTransition<'_>,
        require_all_segments_verified: bool,
    ) -> Result<(), String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence recording transition: {error}"))?;
        if require_all_segments_verified {
            let finalizing_count: i64 = tx
                .query_row(
                    "SELECT count(*) FROM evidence_recording_segments WHERE recording_session_id = ?1 AND state = 'FINALIZING'",
                    [transition.session_id],
                    |row| row.get(0),
                )
                .map_err(|error| format!("verify evidence segments before success: {error}"))?;
            if finalizing_count > 0 {
                return Err(format!(
                    "evidence recording cannot succeed while {finalizing_count} segment(s) remain FINALIZING"
                ));
            }
        }
        let changed = tx
            .execute(
                r#"
                UPDATE evidence_recording_sessions
                SET status = ?3,
                    started_at_unix_ms = CASE WHEN ?3 = 'RUNNING' THEN ?4 ELSE started_at_unix_ms END,
                    stopped_at_unix_ms = CASE WHEN ?3 IN ('SUCCEEDED', 'FAILED') THEN ?4 ELSE stopped_at_unix_ms END,
                    stop_reason = CASE WHEN ?3 IN ('SUCCEEDED', 'FAILED') THEN ?5 ELSE stop_reason END,
                    error_code = CASE WHEN ?3 = 'FAILED' THEN ?6 ELSE error_code END,
                    error_message = CASE WHEN ?3 = 'FAILED' THEN ?7 ELSE error_message END,
                    updated_at_unix_ms = ?4
                WHERE id = ?1 AND status = ?2
                "#,
                params![
                    transition.session_id,
                    transition.expected_state,
                    transition.next_state,
                    transition.occurred_at_unix_ms,
                    transition.stop_reason,
                    transition.error_code,
                    transition.message,
                ],
            )
            .map_err(|error| format!("transition evidence recording: {error}"))?;
        if changed != 1 {
            let actual: Option<String> = tx
                .query_row(
                    "SELECT status FROM evidence_recording_sessions WHERE id = ?1",
                    [transition.session_id],
                    |row| row.get(0),
                )
                .optional()
                .map_err(|error| format!("read failed evidence transition: {error}"))?;
            return Err(match actual {
                Some(actual) => format!(
                    "evidence recording transition requires {}, current state is {actual}",
                    transition.expected_state
                ),
                None => format!("evidence recording {} was not found", transition.session_id),
            });
        }
        insert_recording_event(
            &tx,
            &RecordingEventInput {
                session_id: transition.session_id,
                event_type: transition.event_type,
                state: transition.next_state,
                actor: "atlas_native_recorder",
                message: transition.message,
                evidence: &json!({ "errorCode": transition.error_code, "stopReason": transition.stop_reason }),
                occurred_at_unix_ms: transition.occurred_at_unix_ms,
            },
        )?;
        tx.commit()
            .map_err(|error| format!("commit evidence recording transition: {error}"))
    }
}

fn validate_create_input(input: &CreateEvidenceRecordingInput) -> Result<(), String> {
    if input.source_id.trim().is_empty() || input.source_id.len() > 128 {
        return Err("evidence recording source id must contain 1 to 128 characters".into());
    }
    if input.drone_id.trim().is_empty() {
        return Err("evidence recording requires an aircraft".into());
    }
    if !(2..=600).contains(&input.segment_duration_seconds) {
        return Err("evidence segment duration must be between 2 and 600 seconds".into());
    }
    if input.warning_free_bytes < input.stop_free_bytes {
        return Err("evidence disk warning threshold cannot be below the stop threshold".into());
    }
    if input.warning_free_bytes > i64::MAX as u64 || input.stop_free_bytes > i64::MAX as u64 {
        return Err("evidence disk thresholds exceed SQLite integer limits".into());
    }
    Ok(())
}

fn validate_recording_associations(
    tx: &Transaction<'_>,
    drone_id: &str,
    incident_id: Option<&str>,
    mission_id: Option<&str>,
    mission_run_id: Option<&str>,
) -> Result<ValidatedRecordingAssociations, String> {
    let aircraft_exists: bool = tx
        .query_row(
            "SELECT EXISTS(SELECT 1 FROM drones WHERE id = ?1 AND status = 'active')",
            [drone_id],
            |row| row.get(0),
        )
        .map_err(|error| format!("validate evidence aircraft: {error}"))?;
    if !aircraft_exists {
        return Err("evidence recording aircraft is not active or does not exist".into());
    }

    let mut resolved_mission = mission_id.map(str::to_string);
    let mut resolved_run = mission_run_id.map(str::to_string);
    if let Some(run_id) = mission_run_id {
        let run: Option<(String, String)> = tx
            .query_row(
                "SELECT mission_id, drone_id FROM mission_runs WHERE id = ?1",
                [run_id],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .optional()
            .map_err(|error| format!("validate evidence mission run: {error}"))?;
        let Some((run_mission_id, run_drone_id)) = run else {
            return Err(format!("evidence mission run {run_id} was not found"));
        };
        if run_drone_id != drone_id {
            return Err("evidence mission run belongs to a different aircraft".into());
        }
        if mission_id.is_some_and(|candidate| candidate != run_mission_id) {
            return Err("evidence mission and mission run do not match".into());
        }
        resolved_mission = Some(run_mission_id);
    }

    if let Some(mission_id) = resolved_mission.as_deref() {
        let exists: bool = tx
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM missions WHERE id = ?1)",
                [mission_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("validate evidence mission: {error}"))?;
        if !exists {
            return Err(format!("evidence mission {mission_id} was not found"));
        }
    }

    let linked_incident = if let Some(run_id) = resolved_run.as_deref() {
        tx.query_row(
            "SELECT incident_id FROM incident_assignments WHERE mission_run_id = ?1",
            [run_id],
            |row| row.get::<_, String>(0),
        )
        .optional()
        .map_err(|error| format!("read evidence run incident association: {error}"))?
    } else if let Some(mission_id) = resolved_mission.as_deref() {
        tx.query_row(
            "SELECT incident_id FROM incident_assignments WHERE mission_id = ?1 AND drone_id = ?2 ORDER BY assigned_at_unix_ms DESC LIMIT 1",
            params![mission_id, drone_id],
            |row| row.get::<_, String>(0),
        )
        .optional()
        .map_err(|error| format!("read evidence mission incident association: {error}"))?
    } else {
        None
    };

    if let Some(incident_id) = incident_id {
        let exists: bool = tx
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM incidents WHERE id = ?1)",
                [incident_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("validate evidence incident: {error}"))?;
        if !exists {
            return Err(format!("evidence incident {incident_id} was not found"));
        }
        if resolved_mission.is_some() || resolved_run.is_some() {
            if linked_incident
                .as_deref()
                .is_some_and(|linked| linked != incident_id)
            {
                return Err("evidence incident does not match the mission assignment".into());
            }
            let linked: bool = tx
                .query_row(
                    r#"
                    SELECT EXISTS(
                        SELECT 1 FROM incident_assignments
                        WHERE incident_id = ?1 AND drone_id = ?2
                          AND (?3 IS NULL OR mission_id = ?3)
                          AND (?4 IS NULL OR mission_run_id = ?4)
                    )
                    "#,
                    params![incident_id, drone_id, resolved_mission, resolved_run],
                    |row| row.get(0),
                )
                .map_err(|error| format!("validate evidence incident assignment: {error}"))?;
            if !linked {
                return Err(
                    "evidence incident, aircraft, mission, and run associations are not linked"
                        .into(),
                );
            }
        }
    }

    let resolved_incident = incident_id.map(str::to_string).or(linked_incident);
    Ok(ValidatedRecordingAssociations {
        incident_id: resolved_incident,
        mission_id: resolved_mission.take(),
        mission_run_id: resolved_run.take(),
    })
}

fn read_recording_session(
    connection: &rusqlite::Connection,
    session_id: &str,
) -> Result<EvidenceRecordingSessionSnapshot, String> {
    let mut snapshot = connection
        .query_row(
            r#"
            SELECT id, source_id, status, evidence_root, drone_id, incident_id,
                   mission_id, mission_run_id, requested_by, segment_duration_seconds,
                   warning_free_bytes, stop_free_bytes, requested_at_unix_ms,
                   started_at_unix_ms, stopped_at_unix_ms, finalized_segment_count,
                   total_bytes, last_segment_ended_at_unix_ms, stop_reason,
                   error_code, error_message
            FROM evidence_recording_sessions WHERE id = ?1
            "#,
            [session_id],
            |row| {
                Ok(EvidenceRecordingSessionSnapshot {
                    id: row.get(0)?,
                    source_id: row.get(1)?,
                    status: row.get(2)?,
                    evidence_root: row.get(3)?,
                    drone_id: row.get(4)?,
                    incident_id: row.get(5)?,
                    mission_id: row.get(6)?,
                    mission_run_id: row.get(7)?,
                    requested_by: row.get(8)?,
                    segment_duration_seconds: row.get::<_, i64>(9)? as u64,
                    warning_free_bytes: row.get::<_, i64>(10)? as u64,
                    stop_free_bytes: row.get::<_, i64>(11)? as u64,
                    requested_at_unix_ms: row.get(12)?,
                    started_at_unix_ms: row.get(13)?,
                    stopped_at_unix_ms: row.get(14)?,
                    finalized_segment_count: row.get::<_, i64>(15)? as u64,
                    total_bytes: row.get::<_, i64>(16)? as u64,
                    last_segment_ended_at_unix_ms: row.get(17)?,
                    stop_reason: row.get(18)?,
                    error_code: row.get(19)?,
                    error_message: row.get(20)?,
                    segments: Vec::new(),
                    gaps: Vec::new(),
                    events: Vec::new(),
                })
            },
        )
        .optional()
        .map_err(|error| format!("read evidence recording session: {error}"))?
        .ok_or_else(|| format!("evidence recording {session_id} was not found"))?;
    snapshot.segments = read_segments(connection, session_id)?;
    snapshot.gaps = read_gaps(connection, session_id)?;
    snapshot.events = read_events(connection, session_id)?;
    Ok(snapshot)
}

fn read_segments(
    connection: &rusqlite::Connection,
    session_id: &str,
) -> Result<Vec<EvidenceRecordingSegmentSnapshot>, String> {
    let mut statement = connection
        .prepare(
            "SELECT id, sequence, state, temporary_relative_path, final_relative_path, source_started_at_unix_ms, source_ended_at_unix_ms, byte_length, sha256, finalized_at_unix_ms FROM evidence_recording_segments WHERE recording_session_id = ?1 ORDER BY sequence",
        )
        .map_err(|error| format!("prepare evidence segments: {error}"))?;
    let rows = statement
        .query_map([session_id], |row| {
            Ok(EvidenceRecordingSegmentSnapshot {
                id: row.get(0)?,
                sequence: row.get::<_, i64>(1)? as u64,
                state: row.get(2)?,
                temporary_relative_path: row.get(3)?,
                final_relative_path: row.get(4)?,
                source_started_at_unix_ms: row.get(5)?,
                source_ended_at_unix_ms: row.get(6)?,
                byte_length: row.get::<_, i64>(7)? as u64,
                sha256: row.get(8)?,
                finalized_at_unix_ms: row.get(9)?,
            })
        })
        .map_err(|error| format!("query evidence segments: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read evidence segments: {error}"))?;
    Ok(rows)
}

fn read_gaps(
    connection: &rusqlite::Connection,
    session_id: &str,
) -> Result<Vec<EvidenceGapEventSnapshot>, String> {
    let mut statement = connection
        .prepare("SELECT id, sequence, cause, gap_started_at_unix_ms, gap_ended_at_unix_ms, details_json, occurred_at_unix_ms FROM evidence_gap_events WHERE recording_session_id = ?1 ORDER BY sequence")
        .map_err(|error| format!("prepare evidence gaps: {error}"))?;
    let rows = statement
        .query_map([session_id], |row| {
            let details_json: String = row.get(5)?;
            Ok(EvidenceGapEventSnapshot {
                id: row.get(0)?,
                sequence: row.get::<_, i64>(1)? as u64,
                cause: row.get(2)?,
                gap_started_at_unix_ms: row.get(3)?,
                gap_ended_at_unix_ms: row.get(4)?,
                details: serde_json::from_str(&details_json).unwrap_or_else(|_| json!({})),
                occurred_at_unix_ms: row.get(6)?,
            })
        })
        .map_err(|error| format!("query evidence gaps: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read evidence gaps: {error}"))?;
    Ok(rows)
}

fn read_events(
    connection: &rusqlite::Connection,
    session_id: &str,
) -> Result<Vec<EvidenceRecordingEventSnapshot>, String> {
    let mut statement = connection
        .prepare("SELECT id, sequence, event_type, state, actor, message, evidence_json, occurred_at_unix_ms FROM evidence_recording_events WHERE recording_session_id = ?1 ORDER BY sequence")
        .map_err(|error| format!("prepare evidence recording events: {error}"))?;
    let rows = statement
        .query_map([session_id], |row| {
            let evidence_json: String = row.get(6)?;
            Ok(EvidenceRecordingEventSnapshot {
                id: row.get(0)?,
                sequence: row.get::<_, i64>(1)? as u64,
                event_type: row.get(2)?,
                state: row.get(3)?,
                actor: row.get(4)?,
                message: row.get(5)?,
                evidence: serde_json::from_str(&evidence_json).unwrap_or_else(|_| json!({})),
                occurred_at_unix_ms: row.get(7)?,
            })
        })
        .map_err(|error| format!("query evidence recording events: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read evidence recording events: {error}"))?;
    Ok(rows)
}

fn insert_recording_event(
    tx: &Transaction<'_>,
    input: &RecordingEventInput<'_>,
) -> Result<(), String> {
    let sequence = next_sequence(tx, "evidence_recording_events", input.session_id)?;
    let evidence_json = serde_json::to_string(input.evidence)
        .map_err(|error| format!("serialize evidence recording event: {error}"))?;
    tx.execute(
        "INSERT INTO evidence_recording_events (id, recording_session_id, sequence, event_type, state, actor, message, evidence_json, occurred_at_unix_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)",
        params![generate_id(tx)?, input.session_id, sequence as i64, input.event_type, input.state, input.actor, input.message, evidence_json, input.occurred_at_unix_ms],
    )
    .map_err(|error| format!("insert evidence recording event: {error}"))?;
    Ok(())
}

fn next_sequence(tx: &Transaction<'_>, table: &str, session_id: &str) -> Result<u64, String> {
    let sql = format!(
        "SELECT COALESCE(MAX(sequence), 0) + 1 FROM {table} WHERE recording_session_id = ?1"
    );
    let sequence: i64 = tx
        .query_row(&sql, [session_id], |row| row.get(0))
        .map_err(|error| format!("allocate {table} sequence: {error}"))?;
    Ok(sequence.max(0) as u64)
}

fn generate_id(tx: &Transaction<'_>) -> Result<String, String> {
    tx.query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate evidence record id: {error}"))
}

fn is_unique_constraint(error: &rusqlite::Error) -> bool {
    matches!(
        error,
        rusqlite::Error::SqliteFailure(code, _)
            if matches!(code.code, rusqlite::ErrorCode::ConstraintViolation)
    )
}

#[cfg(test)]
pub(crate) fn test_create_recording_input(
    drone_id: &str,
    evidence_root: &str,
    requested_at_unix_ms: i64,
) -> CreateEvidenceRecordingInput {
    CreateEvidenceRecordingInput {
        source_id: "test-source".into(),
        evidence_root: evidence_root.into(),
        drone_id: drone_id.into(),
        incident_id: None,
        mission_id: None,
        mission_run_id: None,
        segment_duration_seconds: 10,
        warning_free_bytes: 1_000,
        stop_free_bytes: 500,
        requested_at_unix_ms,
    }
}
