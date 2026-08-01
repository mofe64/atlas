use rusqlite::{params, OptionalExtension, Transaction};
use serde::{Deserialize, Serialize};

use super::LocalDatabase;

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct CaptureEvidenceStillInput {
    pub drone_id: String,
    pub incident_id: Option<String>,
    pub mission_id: Option<String>,
    pub mission_run_id: Option<String>,
    pub selection_id: Option<String>,
    #[serde(default = "default_operator")]
    pub actor: String,
}

#[derive(Debug, Clone)]
pub(crate) struct CreateEvidenceStillAssetInput {
    pub source_id: String,
    pub drone_id: String,
    pub incident_id: Option<String>,
    pub mission_id: Option<String>,
    pub mission_run_id: Option<String>,
    pub selection_id: Option<String>,
    pub captured_at_unix_ms: i64,
    pub actor: String,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct QueueEvidenceEventClipInput {
    pub evidence_marker_annotation_id: String,
    #[serde(default = "default_pre_roll_seconds")]
    pub pre_roll_seconds: u64,
    #[serde(default = "default_post_roll_seconds")]
    pub post_roll_seconds: u64,
    #[serde(default = "default_operator")]
    pub actor: String,
}

#[derive(Debug, Clone, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub(crate) struct EvidenceAssetListInput {
    pub asset_type: Option<String>,
    pub drone_id: Option<String>,
    pub limit: Option<usize>,
}

#[derive(Debug, Clone)]
pub(crate) struct EvidenceAssetFileInput {
    pub relative_path: String,
    pub thumbnail_relative_path: String,
    pub mime_type: String,
    pub thumbnail_mime_type: String,
    pub byte_length: u64,
    pub sha256: String,
    pub thumbnail_byte_length: u64,
    pub thumbnail_sha256: String,
    pub source_started_at_unix_ms: i64,
    pub source_ended_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct EvidenceAssetSnapshot {
    pub id: String,
    pub asset_type: String,
    pub status: String,
    pub source_id: String,
    pub drone_id: String,
    pub drone_name: String,
    pub incident_id: Option<String>,
    pub mission_id: Option<String>,
    pub mission_run_id: Option<String>,
    pub recording_session_id: Option<String>,
    pub selection_id: Option<String>,
    pub track_session_id: Option<String>,
    pub track_id: Option<String>,
    pub track_class_label: Option<String>,
    pub evidence_marker_annotation_id: Option<String>,
    pub captured_at_unix_ms: i64,
    pub source_started_at_unix_ms: Option<i64>,
    pub source_ended_at_unix_ms: Option<i64>,
    pub requested_start_at_unix_ms: Option<i64>,
    pub requested_end_at_unix_ms: Option<i64>,
    pub relative_path: String,
    pub thumbnail_relative_path: String,
    pub mime_type: String,
    pub thumbnail_mime_type: String,
    pub byte_length: u64,
    pub sha256: String,
    pub thumbnail_byte_length: u64,
    pub thumbnail_sha256: String,
    pub created_by: String,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
    pub error_message: String,
}

struct AssetContext {
    incident_id: Option<String>,
    mission_id: Option<String>,
    mission_run_id: Option<String>,
    track_session_id: Option<String>,
    track_id: Option<String>,
}

struct ClipMarkerContext {
    selection_id: String,
    track_session_id: String,
    track_id: String,
    source_id: String,
    drone_id: String,
    incident_id: Option<String>,
    mission_id: Option<String>,
    mission_run_id: Option<String>,
    marked_at_unix_ms: i64,
}

impl LocalDatabase {
    pub(crate) fn create_evidence_still_asset(
        &self,
        input: &CreateEvidenceStillAssetInput,
    ) -> Result<EvidenceAssetSnapshot, String> {
        validate_actor_and_identity(&input.drone_id, &input.actor)?;
        let mut connection = self.connection.lock().map_err(lock_error)?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin still capture: {error}"))?;
        let context = resolve_asset_context(
            &tx,
            &input.drone_id,
            input.incident_id.as_deref(),
            input.mission_id.as_deref(),
            input.mission_run_id.as_deref(),
            input.selection_id.as_deref(),
        )?;
        let id = generate_id(&tx)?;
        tx.execute(
            r#"
            INSERT INTO evidence_assets (
                id, asset_type, status, source_id, drone_id, incident_id, mission_id,
                mission_run_id, selection_id, track_session_id, track_id,
                captured_at_unix_ms, requested_start_at_unix_ms, requested_end_at_unix_ms,
                created_by, created_at_unix_ms, updated_at_unix_ms
            ) VALUES (?1, 'STILL', 'PENDING', ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9,
                      ?10, ?10, ?10, ?11, ?10, ?10)
            "#,
            params![
                id,
                input.source_id,
                input.drone_id,
                context.incident_id,
                context.mission_id,
                context.mission_run_id,
                input.selection_id,
                context.track_session_id,
                context.track_id,
                input.captured_at_unix_ms,
                input.actor.trim(),
            ],
        )
        .map_err(|error| format!("insert still capture: {error}"))?;
        tx.commit()
            .map_err(|error| format!("commit still capture: {error}"))?;
        drop(connection);
        self.evidence_asset(&id)
    }

    pub(crate) fn queue_evidence_event_clip(
        &self,
        input: &QueueEvidenceEventClipInput,
        now: i64,
    ) -> Result<EvidenceAssetSnapshot, String> {
        validate_actor_and_identity(&input.evidence_marker_annotation_id, &input.actor)?;
        if input.pre_roll_seconds > 120
            || input.post_roll_seconds > 120
            || input.pre_roll_seconds + input.post_roll_seconds == 0
        {
            return Err("event clip pre/post roll must total 1 to 240 seconds, with each side at most 120 seconds".into());
        }
        let mut connection = self.connection.lock().map_err(lock_error)?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin event clip capture: {error}"))?;
        let marker = tx
            .query_row(
                r#"
                SELECT annotations.selection_id, annotations.track_session_id, annotations.track_id,
                       sessions.source_id, sessions.drone_id, sessions.incident_id,
                       sessions.mission_id, sessions.mission_run_id, annotations.marked_at_unix_ms
                FROM perception_track_annotations annotations
                JOIN evidence_recording_sessions sessions
                  ON sessions.id = annotations.evidence_recording_session_id
                WHERE annotations.id = ?1 AND annotations.annotation_type = 'EVIDENCE_MARKER'
                "#,
                [input.evidence_marker_annotation_id.trim()],
                |row| {
                    Ok(ClipMarkerContext {
                        selection_id: row.get(0)?,
                        track_session_id: row.get(1)?,
                        track_id: row.get(2)?,
                        source_id: row.get(3)?,
                        drone_id: row.get(4)?,
                        incident_id: row.get(5)?,
                        mission_id: row.get(6)?,
                        mission_run_id: row.get(7)?,
                        marked_at_unix_ms: row.get(8)?,
                    })
                },
            )
            .optional()
            .map_err(|error| format!("read event marker for clip: {error}"))?
            .ok_or_else(|| "event clip requires a retained track marker".to_string())?;
        let recording_session_id: String = tx
            .query_row(
                "SELECT evidence_recording_session_id FROM perception_track_annotations WHERE id = ?1",
                [input.evidence_marker_annotation_id.trim()],
                |row| row.get(0),
            )
            .map_err(|error| format!("read marker recording session: {error}"))?;
        let duplicate: Option<String> = tx
            .query_row(
                "SELECT id FROM evidence_assets WHERE evidence_marker_annotation_id = ?1 LIMIT 1",
                [input.evidence_marker_annotation_id.trim()],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("check existing marker clip: {error}"))?;
        if let Some(id) = duplicate {
            drop(tx);
            drop(connection);
            return self.evidence_asset(&id);
        }
        let id = generate_id(&tx)?;
        let requested_start = marker
            .marked_at_unix_ms
            .saturating_sub((input.pre_roll_seconds as i64) * 1_000);
        let requested_end = marker
            .marked_at_unix_ms
            .saturating_add((input.post_roll_seconds as i64) * 1_000);
        tx.execute(
            r#"
            INSERT INTO evidence_assets (
                id, asset_type, status, source_id, drone_id, incident_id, mission_id,
                mission_run_id, recording_session_id, selection_id, track_session_id,
                track_id, evidence_marker_annotation_id, captured_at_unix_ms,
                requested_start_at_unix_ms, requested_end_at_unix_ms,
                created_by, created_at_unix_ms, updated_at_unix_ms
            ) VALUES (?1, 'EVENT_CLIP', 'PENDING', ?2, ?3, ?4, ?5, ?6, ?7, ?8,
                      ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?16)
            "#,
            params![
                id,
                marker.source_id,
                marker.drone_id,
                marker.incident_id,
                marker.mission_id,
                marker.mission_run_id,
                recording_session_id,
                marker.selection_id,
                marker.track_session_id,
                marker.track_id,
                input.evidence_marker_annotation_id.trim(),
                marker.marked_at_unix_ms,
                requested_start,
                requested_end,
                input.actor.trim(),
                now,
            ],
        )
        .map_err(|error| format!("insert event clip capture: {error}"))?;
        tx.commit()
            .map_err(|error| format!("commit event clip capture: {error}"))?;
        drop(connection);
        self.evidence_asset(&id)
    }

    pub(crate) fn complete_evidence_asset(
        &self,
        asset_id: &str,
        files: &EvidenceAssetFileInput,
        now: i64,
    ) -> Result<EvidenceAssetSnapshot, String> {
        validate_file_manifest(files)?;
        let connection = self.connection.lock().map_err(lock_error)?;
        let changed = connection
            .execute(
                r#"
                UPDATE evidence_assets
                SET status = 'READY', relative_path = ?2, thumbnail_relative_path = ?3,
                    mime_type = ?4, thumbnail_mime_type = ?5, byte_length = ?6,
                    sha256 = ?7, thumbnail_byte_length = ?8, thumbnail_sha256 = ?9,
                    source_started_at_unix_ms = ?10, source_ended_at_unix_ms = ?11,
                    error_message = '', updated_at_unix_ms = ?12
                WHERE id = ?1 AND status = 'PENDING'
                "#,
                params![
                    asset_id,
                    files.relative_path,
                    files.thumbnail_relative_path,
                    files.mime_type,
                    files.thumbnail_mime_type,
                    files.byte_length as i64,
                    files.sha256.to_lowercase(),
                    files.thumbnail_byte_length as i64,
                    files.thumbnail_sha256.to_lowercase(),
                    files.source_started_at_unix_ms,
                    files.source_ended_at_unix_ms,
                    now,
                ],
            )
            .map_err(|error| format!("publish capture: {error}"))?;
        if changed != 1 {
            return Err("capture is not pending publication".into());
        }
        drop(connection);
        self.evidence_asset(asset_id)
    }

    pub(crate) fn fail_evidence_asset(
        &self,
        asset_id: &str,
        message: &str,
        now: i64,
    ) -> Result<(), String> {
        let connection = self.connection.lock().map_err(lock_error)?;
        connection
            .execute(
                "UPDATE evidence_assets SET status = 'FAILED', error_message = ?2, updated_at_unix_ms = ?3 WHERE id = ?1 AND status = 'PENDING'",
                params![asset_id, message.trim(), now],
            )
            .map_err(|error| format!("fail capture: {error}"))?;
        Ok(())
    }

    pub(crate) fn evidence_assets(
        &self,
        input: &EvidenceAssetListInput,
    ) -> Result<Vec<EvidenceAssetSnapshot>, String> {
        if input
            .asset_type
            .as_deref()
            .is_some_and(|value| !matches!(value, "STILL" | "EVENT_CLIP"))
        {
            return Err("capture type filter is invalid".into());
        }
        let connection = self.connection.lock().map_err(lock_error)?;
        let mut statement = connection
            .prepare(
                r#"
                SELECT id FROM evidence_assets
                WHERE (?1 IS NULL OR asset_type = ?1)
                  AND (?2 IS NULL OR drone_id = ?2)
                ORDER BY captured_at_unix_ms DESC, rowid DESC
                LIMIT ?3
                "#,
            )
            .map_err(|error| format!("prepare capture browser: {error}"))?;
        let limit = input.limit.unwrap_or(250).clamp(1, 1_000) as i64;
        let ids = statement
            .query_map(params![input.asset_type, input.drone_id, limit], |row| {
                row.get::<_, String>(0)
            })
            .map_err(|error| format!("query capture browser: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read capture browser: {error}"))?;
        ids.iter().map(|id| read_asset(&connection, id)).collect()
    }

    pub(crate) fn evidence_asset(&self, asset_id: &str) -> Result<EvidenceAssetSnapshot, String> {
        let connection = self.connection.lock().map_err(lock_error)?;
        read_asset(&connection, asset_id)
    }

    pub(crate) fn pending_event_clip_assets(
        &self,
        recording_session_id: Option<&str>,
    ) -> Result<Vec<EvidenceAssetSnapshot>, String> {
        let connection = self.connection.lock().map_err(lock_error)?;
        let mut statement = connection.prepare(
            "SELECT id FROM evidence_assets WHERE asset_type = 'EVENT_CLIP' AND status = 'PENDING' AND (?1 IS NULL OR recording_session_id = ?1) ORDER BY captured_at_unix_ms",
        ).map_err(|error| format!("prepare pending event clips: {error}"))?;
        let ids = statement
            .query_map([recording_session_id], |row| row.get::<_, String>(0))
            .map_err(|error| format!("query pending event clips: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read pending event clips: {error}"))?;
        ids.iter().map(|id| read_asset(&connection, id)).collect()
    }

    pub(crate) fn pending_evidence_assets(&self) -> Result<Vec<EvidenceAssetSnapshot>, String> {
        let connection = self.connection.lock().map_err(lock_error)?;
        let mut statement = connection
            .prepare(
                "SELECT id FROM evidence_assets WHERE status = 'PENDING' ORDER BY created_at_unix_ms",
            )
            .map_err(|error| format!("prepare pending captures: {error}"))?;
        let ids = statement
            .query_map([], |row| row.get::<_, String>(0))
            .map_err(|error| format!("query pending captures: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read pending captures: {error}"))?;
        ids.iter().map(|id| read_asset(&connection, id)).collect()
    }
}

fn read_asset(
    connection: &rusqlite::Connection,
    asset_id: &str,
) -> Result<EvidenceAssetSnapshot, String> {
    connection
        .query_row(
            r#"
            SELECT assets.id, assets.asset_type, assets.status, assets.source_id,
                   assets.drone_id, drones.name, assets.incident_id, assets.mission_id,
                   assets.mission_run_id, assets.recording_session_id, assets.selection_id,
                   assets.track_session_id, assets.track_id,
                   COALESCE(selections.snapshot_class_label, tracks.class_label),
                   assets.evidence_marker_annotation_id, assets.captured_at_unix_ms,
                   assets.source_started_at_unix_ms, assets.source_ended_at_unix_ms,
                   assets.requested_start_at_unix_ms, assets.requested_end_at_unix_ms,
                   assets.relative_path, assets.thumbnail_relative_path, assets.mime_type,
                   assets.thumbnail_mime_type, assets.byte_length, assets.sha256,
                   assets.thumbnail_byte_length, assets.thumbnail_sha256, assets.created_by,
                   assets.created_at_unix_ms, assets.updated_at_unix_ms, assets.error_message
            FROM evidence_assets assets
            JOIN drones ON drones.id = assets.drone_id
            LEFT JOIN perception_track_selections selections ON selections.id = assets.selection_id
            LEFT JOIN perception_tracks tracks ON tracks.id = assets.track_id
            WHERE assets.id = ?1
            "#,
            [asset_id],
            |row| {
                Ok(EvidenceAssetSnapshot {
                    id: row.get(0)?,
                    asset_type: row.get(1)?,
                    status: row.get(2)?,
                    source_id: row.get(3)?,
                    drone_id: row.get(4)?,
                    drone_name: row.get(5)?,
                    incident_id: row.get(6)?,
                    mission_id: row.get(7)?,
                    mission_run_id: row.get(8)?,
                    recording_session_id: row.get(9)?,
                    selection_id: row.get(10)?,
                    track_session_id: row.get(11)?,
                    track_id: row.get(12)?,
                    track_class_label: row.get(13)?,
                    evidence_marker_annotation_id: row.get(14)?,
                    captured_at_unix_ms: row.get(15)?,
                    source_started_at_unix_ms: row.get(16)?,
                    source_ended_at_unix_ms: row.get(17)?,
                    requested_start_at_unix_ms: row.get(18)?,
                    requested_end_at_unix_ms: row.get(19)?,
                    relative_path: row.get(20)?,
                    thumbnail_relative_path: row.get(21)?,
                    mime_type: row.get(22)?,
                    thumbnail_mime_type: row.get(23)?,
                    byte_length: row.get::<_, i64>(24)?.max(0) as u64,
                    sha256: row.get(25)?,
                    thumbnail_byte_length: row.get::<_, i64>(26)?.max(0) as u64,
                    thumbnail_sha256: row.get(27)?,
                    created_by: row.get(28)?,
                    created_at_unix_ms: row.get(29)?,
                    updated_at_unix_ms: row.get(30)?,
                    error_message: row.get(31)?,
                })
            },
        )
        .optional()
        .map_err(|error| format!("read capture: {error}"))?
        .ok_or_else(|| format!("capture {asset_id} was not found"))
}

fn resolve_asset_context(
    tx: &Transaction<'_>,
    drone_id: &str,
    incident_id: Option<&str>,
    mission_id: Option<&str>,
    mission_run_id: Option<&str>,
    selection_id: Option<&str>,
) -> Result<AssetContext, String> {
    let drone_exists: bool = tx
        .query_row(
            "SELECT EXISTS(SELECT 1 FROM drones WHERE id = ?1)",
            [drone_id],
            |row| row.get(0),
        )
        .map_err(|error| format!("validate capture aircraft: {error}"))?;
    if !drone_exists {
        return Err(format!("capture aircraft {drone_id} was not found"));
    }
    let mut resolved_mission = mission_id.map(str::to_string);
    if let Some(run_id) = mission_run_id {
        let (run_mission, run_drone): (String, String) = tx
            .query_row(
                "SELECT mission_id, drone_id FROM mission_runs WHERE id = ?1",
                [run_id],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .optional()
            .map_err(|error| format!("validate capture mission run: {error}"))?
            .ok_or_else(|| format!("capture mission run {run_id} was not found"))?;
        if run_drone != drone_id {
            return Err("capture mission run belongs to another aircraft".into());
        }
        if resolved_mission
            .as_deref()
            .is_some_and(|id| id != run_mission)
        {
            return Err("capture mission does not match the mission run".into());
        }
        resolved_mission = Some(run_mission);
    } else if let Some(mission_id) = resolved_mission.as_deref() {
        let exists: bool = tx
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM missions WHERE id = ?1)",
                [mission_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("validate capture mission: {error}"))?;
        if !exists {
            return Err(format!("capture mission {mission_id} was not found"));
        }
    }
    let linked_incident: Option<String> = if let Some(run_id) = mission_run_id {
        tx.query_row(
            "SELECT incident_id FROM incident_assignments WHERE mission_run_id = ?1",
            [run_id],
            |row| row.get(0),
        )
        .optional()
        .map_err(|error| format!("resolve capture incident from run: {error}"))?
    } else {
        None
    };
    if incident_id.is_some_and(|id| {
        linked_incident
            .as_deref()
            .is_some_and(|linked| linked != id)
    }) {
        return Err("capture incident does not match the mission run assignment".into());
    }
    if let Some(id) = incident_id {
        let exists: bool = tx
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM incidents WHERE id = ?1)",
                [id],
                |row| row.get(0),
            )
            .map_err(|error| format!("validate capture incident: {error}"))?;
        if !exists {
            return Err(format!("capture incident {id} was not found"));
        }
    }
    let (track_session_id, track_id) = if let Some(selection_id) = selection_id {
        let selected: (String, String, String) = tx
            .query_row(
                "SELECT drone_id, track_session_id, track_id FROM perception_track_selections WHERE id = ?1 AND status <> 'CLEARED'",
                [selection_id],
                |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
            )
            .optional()
            .map_err(|error| format!("validate capture track selection: {error}"))?
            .ok_or_else(|| "capture track selection is unavailable or cleared".to_string())?;
        if selected.0 != drone_id {
            return Err("capture track selection belongs to another aircraft".into());
        }
        (Some(selected.1), Some(selected.2))
    } else {
        (None, None)
    };
    Ok(AssetContext {
        incident_id: incident_id.map(str::to_string).or(linked_incident),
        mission_id: resolved_mission,
        mission_run_id: mission_run_id.map(str::to_string),
        track_session_id,
        track_id,
    })
}

fn validate_file_manifest(files: &EvidenceAssetFileInput) -> Result<(), String> {
    let valid_hash =
        |value: &str| value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit());
    if files.relative_path.trim().is_empty()
        || files.thumbnail_relative_path.trim().is_empty()
        || files.mime_type.trim().is_empty()
        || files.thumbnail_mime_type.trim().is_empty()
        || files.byte_length == 0
        || files.thumbnail_byte_length == 0
        || !valid_hash(&files.sha256)
        || !valid_hash(&files.thumbnail_sha256)
        || files.source_ended_at_unix_ms < files.source_started_at_unix_ms
    {
        return Err("capture file manifest is invalid".into());
    }
    Ok(())
}

fn validate_actor_and_identity(identity: &str, actor: &str) -> Result<(), String> {
    if identity.trim().is_empty()
        || actor.trim().is_empty()
        || identity.len() > 256
        || actor.len() > 128
    {
        return Err("capture identity and actor are required".into());
    }
    Ok(())
}

fn generate_id(tx: &Transaction<'_>) -> Result<String, String> {
    tx.query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate capture id: {error}"))
}

fn lock_error<T>(_: T) -> String {
    "local SQLite mutex was poisoned".to_string()
}

fn default_operator() -> String {
    "operator".into()
}

fn default_pre_roll_seconds() -> u64 {
    10
}

fn default_post_roll_seconds() -> u64 {
    10
}
