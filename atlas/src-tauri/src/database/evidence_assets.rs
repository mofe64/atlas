use rusqlite::{params, OptionalExtension, Transaction};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use super::LocalDatabase;

const DAY_MS: i64 = 86_400_000;

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
    pub review_state: Option<String>,
    pub drone_id: Option<String>,
    #[serde(default)]
    pub include_trashed: bool,
    pub limit: Option<usize>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct ReviewEvidenceAssetInput {
    pub asset_id: String,
    pub review_state: String,
    #[serde(default)]
    pub note: String,
    #[serde(default = "default_operator")]
    pub actor: String,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct AnnotateEvidenceAssetInput {
    pub asset_id: String,
    pub annotation_type: String,
    pub body: String,
    #[serde(default = "default_operator")]
    pub actor: String,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct UpdateEvidenceAssetRetentionInput {
    pub asset_id: String,
    pub retention_class: String,
    #[serde(default = "default_operator")]
    pub actor: String,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TrashEvidenceAssetInput {
    pub asset_id: String,
    pub reason: String,
    #[serde(default = "default_operator")]
    pub actor: String,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct RestoreEvidenceAssetInput {
    pub asset_id: String,
    #[serde(default = "default_operator")]
    pub actor: String,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct UpdateEvidenceRetentionPolicyInput {
    pub enabled: bool,
    pub default_retention_days: u64,
    pub extended_retention_days: u64,
    pub trash_grace_days: u64,
    #[serde(default = "default_operator")]
    pub actor: String,
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
pub(crate) struct EvidenceRetentionPolicySnapshot {
    pub enabled: bool,
    pub default_retention_days: u64,
    pub extended_retention_days: u64,
    pub trash_grace_days: u64,
    pub updated_by: String,
    pub updated_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct EvidenceAssetSnapshot {
    pub id: String,
    pub asset_type: String,
    pub status: String,
    pub review_state: String,
    pub retention_class: String,
    pub source_id: String,
    pub drone_id: String,
    pub incident_id: Option<String>,
    pub mission_id: Option<String>,
    pub mission_run_id: Option<String>,
    pub recording_session_id: Option<String>,
    pub selection_id: Option<String>,
    pub track_session_id: Option<String>,
    pub track_id: Option<String>,
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
    pub retain_until_unix_ms: Option<i64>,
    pub trashed_at_unix_ms: Option<i64>,
    pub purge_after_unix_ms: Option<i64>,
    pub delete_reason: String,
    pub purged_at_unix_ms: Option<i64>,
    pub error_message: String,
    pub annotations: Vec<EvidenceAssetAnnotationSnapshot>,
    pub events: Vec<EvidenceAssetEventSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct EvidenceAssetAnnotationSnapshot {
    pub id: String,
    pub annotation_type: String,
    pub body: String,
    pub created_by: String,
    pub created_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct EvidenceAssetEventSnapshot {
    pub id: String,
    pub sequence: u64,
    pub event_type: String,
    pub actor: String,
    pub message: String,
    pub details: Value,
    pub occurred_at_unix_ms: i64,
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
            .map_err(|error| format!("begin still evidence asset: {error}"))?;
        let context = resolve_asset_context(
            &tx,
            &input.drone_id,
            input.incident_id.as_deref(),
            input.mission_id.as_deref(),
            input.mission_run_id.as_deref(),
            input.selection_id.as_deref(),
        )?;
        let policy = read_retention_policy(&tx)?;
        let id = generate_id(&tx)?;
        let retain_until =
            retention_deadline(input.captured_at_unix_ms, policy.default_retention_days);
        tx.execute(
            r#"
            INSERT INTO evidence_assets (
                id, asset_type, status, source_id, drone_id, incident_id, mission_id,
                mission_run_id, selection_id, track_session_id, track_id,
                captured_at_unix_ms, requested_start_at_unix_ms, requested_end_at_unix_ms,
                created_by, created_at_unix_ms, updated_at_unix_ms, retain_until_unix_ms
            ) VALUES (?1, 'STILL', 'PENDING', ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9,
                      ?10, ?10, ?10, ?11, ?10, ?10, ?12)
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
                retain_until,
            ],
        )
        .map_err(|error| format!("insert still evidence asset: {error}"))?;
        insert_asset_event(
            &tx,
            &id,
            "CREATED",
            input.actor.trim(),
            "Still capture accepted",
            &json!({
                "capturedAtUnixMs": input.captured_at_unix_ms,
                "selectionId": input.selection_id,
            }),
            input.captured_at_unix_ms,
        )?;
        tx.commit()
            .map_err(|error| format!("commit still evidence asset: {error}"))?;
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
            .map_err(|error| format!("begin event clip asset: {error}"))?;
        let marker: ClipMarkerContext = tx
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
            .map_err(|error| format!("read evidence marker for event clip: {error}"))?
            .ok_or_else(|| "event clip requires a retained track evidence marker".to_string())?;
        let recording_session_id: String = tx.query_row(
            "SELECT evidence_recording_session_id FROM perception_track_annotations WHERE id = ?1",
            [input.evidence_marker_annotation_id.trim()], |row| row.get(0),
        ).map_err(|error| format!("read marker recording session: {error}"))?;
        let duplicate: Option<String> = tx.query_row(
            "SELECT id FROM evidence_assets WHERE evidence_marker_annotation_id = ?1 AND status <> 'PURGED' LIMIT 1",
            [input.evidence_marker_annotation_id.trim()], |row| row.get(0),
        ).optional().map_err(|error| format!("check existing marker clip: {error}"))?;
        if let Some(id) = duplicate {
            drop(tx);
            drop(connection);
            return self.evidence_asset(&id);
        }
        let policy = read_retention_policy(&tx)?;
        let id = generate_id(&tx)?;
        let requested_start = marker
            .marked_at_unix_ms
            .saturating_sub((input.pre_roll_seconds as i64) * 1_000);
        let requested_end = marker
            .marked_at_unix_ms
            .saturating_add((input.post_roll_seconds as i64) * 1_000);
        let retain_until = retention_deadline(now, policy.default_retention_days);
        tx.execute(
            r#"
            INSERT INTO evidence_assets (
                id, asset_type, status, source_id, drone_id, incident_id, mission_id,
                mission_run_id, recording_session_id, selection_id, track_session_id,
                track_id, evidence_marker_annotation_id, captured_at_unix_ms,
                requested_start_at_unix_ms, requested_end_at_unix_ms,
                created_by, created_at_unix_ms, updated_at_unix_ms, retain_until_unix_ms
            ) VALUES (?1, 'EVENT_CLIP', 'PENDING', ?2, ?3, ?4, ?5, ?6, ?7, ?8,
                      ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?16, ?17)
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
                retain_until
            ],
        )
        .map_err(|error| format!("insert event clip asset: {error}"))?;
        insert_asset_event(
            &tx,
            &id,
            "CREATED",
            input.actor.trim(),
            "Track event clip queued",
            &json!({
                "evidenceMarkerAnnotationId": input.evidence_marker_annotation_id,
                "preRollSeconds": input.pre_roll_seconds,
                "postRollSeconds": input.post_roll_seconds,
            }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit event clip asset: {error}"))?;
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
        let mut connection = self.connection.lock().map_err(lock_error)?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence asset publication: {error}"))?;
        let changed = tx
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
                    now
                ],
            )
            .map_err(|error| format!("publish evidence asset: {error}"))?;
        if changed != 1 {
            return Err("evidence asset is not pending publication".into());
        }
        insert_asset_event(
            &tx,
            asset_id,
            "READY",
            "atlas_native_evidence",
            "Media and thumbnail published with local integrity metadata",
            &json!({
                "byteLength": files.byte_length,
                "sha256": files.sha256,
                "thumbnailByteLength": files.thumbnail_byte_length,
                "thumbnailSha256": files.thumbnail_sha256,
            }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit evidence asset publication: {error}"))?;
        drop(connection);
        self.evidence_asset(asset_id)
    }

    pub(crate) fn fail_evidence_asset(
        &self,
        asset_id: &str,
        message: &str,
        now: i64,
    ) -> Result<(), String> {
        let message = message.trim();
        let mut connection = self.connection.lock().map_err(lock_error)?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence asset failure: {error}"))?;
        let changed = tx.execute(
            "UPDATE evidence_assets SET status = 'FAILED', error_message = ?2, updated_at_unix_ms = ?3 WHERE id = ?1 AND status = 'PENDING'",
            params![asset_id, message, now],
        ).map_err(|error| format!("fail evidence asset: {error}"))?;
        if changed == 1 {
            insert_asset_event(
                &tx,
                asset_id,
                "FAILED",
                "atlas_native_evidence",
                "Evidence asset generation failed",
                &json!({ "error": message }),
                now,
            )?;
        }
        tx.commit()
            .map_err(|error| format!("commit evidence asset failure: {error}"))
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
            return Err("evidence asset type filter is invalid".into());
        }
        if input
            .review_state
            .as_deref()
            .is_some_and(|value| !valid_review_state(value))
        {
            return Err("evidence review state filter is invalid".into());
        }
        let connection = self.connection.lock().map_err(lock_error)?;
        let mut statement = connection
            .prepare(
                r#"
            SELECT id FROM evidence_assets
            WHERE (?1 IS NULL OR asset_type = ?1)
              AND (?2 IS NULL OR review_state = ?2)
              AND (?3 IS NULL OR drone_id = ?3)
              AND (status <> 'PURGED')
              AND (?4 = 1 OR status NOT IN ('TRASHED', 'PURGING'))
            ORDER BY captured_at_unix_ms DESC, rowid DESC
            LIMIT ?5
            "#,
            )
            .map_err(|error| format!("prepare evidence asset browser: {error}"))?;
        let limit = input.limit.unwrap_or(250).clamp(1, 1_000) as i64;
        let ids = statement
            .query_map(
                params![
                    input.asset_type,
                    input.review_state,
                    input.drone_id,
                    input.include_trashed,
                    limit
                ],
                |row| row.get::<_, String>(0),
            )
            .map_err(|error| format!("query evidence asset browser: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read evidence asset browser: {error}"))?;
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
            .prepare("SELECT id FROM evidence_assets WHERE status = 'PENDING' ORDER BY created_at_unix_ms")
            .map_err(|error| format!("prepare pending evidence assets: {error}"))?;
        let ids = statement
            .query_map([], |row| row.get::<_, String>(0))
            .map_err(|error| format!("query pending evidence assets: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read pending evidence assets: {error}"))?;
        ids.iter().map(|id| read_asset(&connection, id)).collect()
    }

    pub(crate) fn review_evidence_asset(
        &self,
        input: &ReviewEvidenceAssetInput,
        now: i64,
    ) -> Result<EvidenceAssetSnapshot, String> {
        if !valid_review_state(&input.review_state)
            || input.actor.trim().is_empty()
            || input.note.len() > 2_000
        {
            return Err("evidence review is invalid".into());
        }
        let mut connection = self.connection.lock().map_err(lock_error)?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence review: {error}"))?;
        require_mutable_asset(&tx, &input.asset_id)?;
        tx.execute(
            "UPDATE evidence_assets SET review_state = ?2, updated_at_unix_ms = ?3 WHERE id = ?1",
            params![input.asset_id, input.review_state, now],
        )
        .map_err(|error| format!("update evidence review: {error}"))?;
        if !input.note.trim().is_empty() {
            insert_annotation(
                &tx,
                &input.asset_id,
                "NOTE",
                input.note.trim(),
                input.actor.trim(),
                now,
            )?;
        }
        insert_asset_event(
            &tx,
            &input.asset_id,
            "REVIEWED",
            input.actor.trim(),
            "Evidence review state changed",
            &json!({
                "reviewState": input.review_state,
                "note": input.note.trim(),
            }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit evidence review: {error}"))?;
        drop(connection);
        self.evidence_asset(&input.asset_id)
    }

    pub(crate) fn annotate_evidence_asset(
        &self,
        input: &AnnotateEvidenceAssetInput,
        now: i64,
    ) -> Result<EvidenceAssetSnapshot, String> {
        if !matches!(input.annotation_type.as_str(), "NOTE" | "TAG")
            || input.body.trim().is_empty()
            || input.body.len() > 2_000
            || input.actor.trim().is_empty()
        {
            return Err("evidence asset annotation is invalid".into());
        }
        let mut connection = self.connection.lock().map_err(lock_error)?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence annotation: {error}"))?;
        require_mutable_asset(&tx, &input.asset_id)?;
        insert_annotation(
            &tx,
            &input.asset_id,
            &input.annotation_type,
            input.body.trim(),
            input.actor.trim(),
            now,
        )?;
        tx.execute(
            "UPDATE evidence_assets SET updated_at_unix_ms = ?2 WHERE id = ?1",
            params![input.asset_id, now],
        )
        .map_err(|error| format!("touch annotated evidence asset: {error}"))?;
        insert_asset_event(
            &tx,
            &input.asset_id,
            "ANNOTATED",
            input.actor.trim(),
            "Asset-level annotation added",
            &json!({
                "annotationType": input.annotation_type,
                "body": input.body.trim(),
            }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit evidence annotation: {error}"))?;
        drop(connection);
        self.evidence_asset(&input.asset_id)
    }

    pub(crate) fn evidence_retention_policy(
        &self,
    ) -> Result<EvidenceRetentionPolicySnapshot, String> {
        let connection = self.connection.lock().map_err(lock_error)?;
        read_retention_policy(&connection)
    }

    pub(crate) fn update_evidence_retention_policy(
        &self,
        input: &UpdateEvidenceRetentionPolicyInput,
        now: i64,
    ) -> Result<EvidenceRetentionPolicySnapshot, String> {
        if input.actor.trim().is_empty()
            || !(1..=3650).contains(&input.default_retention_days)
            || !(1..=3650).contains(&input.extended_retention_days)
            || input.extended_retention_days < input.default_retention_days
            || !(1..=365).contains(&input.trash_grace_days)
        {
            return Err("evidence retention policy is invalid".into());
        }
        let connection = self.connection.lock().map_err(lock_error)?;
        connection.execute(
            "UPDATE evidence_retention_policy SET enabled = ?1, default_retention_days = ?2, extended_retention_days = ?3, trash_grace_days = ?4, updated_by = ?5, updated_at_unix_ms = ?6 WHERE singleton_id = 1",
            params![input.enabled, input.default_retention_days as i64, input.extended_retention_days as i64, input.trash_grace_days as i64, input.actor.trim(), now],
        ).map_err(|error| format!("update evidence retention policy: {error}"))?;
        read_retention_policy(&connection)
    }

    pub(crate) fn update_evidence_asset_retention(
        &self,
        input: &UpdateEvidenceAssetRetentionInput,
        now: i64,
    ) -> Result<EvidenceAssetSnapshot, String> {
        if !matches!(
            input.retention_class.as_str(),
            "STANDARD" | "EXTENDED" | "LEGAL_HOLD"
        ) || input.actor.trim().is_empty()
        {
            return Err("evidence retention class is invalid".into());
        }
        let mut connection = self.connection.lock().map_err(lock_error)?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin asset retention update: {error}"))?;
        let created_at: i64 = tx.query_row("SELECT created_at_unix_ms FROM evidence_assets WHERE id = ?1 AND status NOT IN ('TRASHED', 'PURGING', 'PURGED')", [&input.asset_id], |row| row.get(0))
            .optional().map_err(|error| format!("read evidence asset retention: {error}"))?
            .ok_or_else(|| "only active evidence assets can change retention".to_string())?;
        let policy = read_retention_policy(&tx)?;
        let retain_until = match input.retention_class.as_str() {
            "STANDARD" => Some(retention_deadline(
                created_at,
                policy.default_retention_days,
            )),
            "EXTENDED" => Some(retention_deadline(
                created_at,
                policy.extended_retention_days,
            )),
            _ => None,
        };
        tx.execute("UPDATE evidence_assets SET retention_class = ?2, retain_until_unix_ms = ?3, updated_at_unix_ms = ?4 WHERE id = ?1", params![input.asset_id, input.retention_class, retain_until, now])
            .map_err(|error| format!("update evidence asset retention: {error}"))?;
        insert_asset_event(
            &tx,
            &input.asset_id,
            "RETENTION_CHANGED",
            input.actor.trim(),
            "Evidence retention class changed",
            &json!({
                "retentionClass": input.retention_class,
                "retainUntilUnixMs": retain_until,
            }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit asset retention update: {error}"))?;
        drop(connection);
        self.evidence_asset(&input.asset_id)
    }

    pub(crate) fn retention_trash_candidates(
        &self,
        now: i64,
    ) -> Result<Vec<EvidenceAssetSnapshot>, String> {
        candidate_assets(
            self,
            "status = 'READY' AND retention_class <> 'LEGAL_HOLD' AND retain_until_unix_ms <= ?1",
            now,
        )
    }

    pub(crate) fn retention_purge_candidates(
        &self,
        now: i64,
    ) -> Result<Vec<EvidenceAssetSnapshot>, String> {
        candidate_assets(
            self,
            "status = 'TRASHED' AND retention_class <> 'LEGAL_HOLD' AND purge_after_unix_ms <= ?1",
            now,
        )
    }

    pub(crate) fn mark_evidence_asset_trashed(
        &self,
        asset_id: &str,
        relative_path: &str,
        thumbnail_relative_path: &str,
        reason: &str,
        actor: &str,
        now: i64,
    ) -> Result<EvidenceAssetSnapshot, String> {
        if reason.trim().is_empty() || actor.trim().is_empty() {
            return Err("trashing evidence requires an actor and reason".into());
        }
        let mut connection = self.connection.lock().map_err(lock_error)?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence trash transition: {error}"))?;
        let retention: String = tx.query_row("SELECT retention_class FROM evidence_assets WHERE id = ?1 AND status IN ('READY', 'FAILED')", [asset_id], |row| row.get(0))
            .optional().map_err(|error| format!("read evidence asset before trash: {error}"))?
            .ok_or_else(|| "only ready or failed evidence assets can be moved to trash".to_string())?;
        if retention == "LEGAL_HOLD" {
            return Err("evidence under legal hold cannot be moved to trash".into());
        }
        let policy = read_retention_policy(&tx)?;
        let purge_after = retention_deadline(now, policy.trash_grace_days);
        tx.execute(
            "UPDATE evidence_assets SET status = 'TRASHED', relative_path = ?2, thumbnail_relative_path = ?3, trashed_at_unix_ms = ?4, purge_after_unix_ms = ?5, delete_reason = ?6, updated_at_unix_ms = ?4 WHERE id = ?1",
            params![asset_id, relative_path, thumbnail_relative_path, now, purge_after, reason.trim()],
        ).map_err(|error| format!("mark evidence asset trashed: {error}"))?;
        insert_asset_event(
            &tx,
            asset_id,
            "TRASHED",
            actor.trim(),
            "Evidence moved to recoverable trash",
            &json!({
                "reason": reason.trim(), "purgeAfterUnixMs": purge_after,
            }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit evidence trash transition: {error}"))?;
        drop(connection);
        self.evidence_asset(asset_id)
    }

    pub(crate) fn mark_evidence_asset_restored(
        &self,
        asset_id: &str,
        relative_path: &str,
        thumbnail_relative_path: &str,
        actor: &str,
        now: i64,
    ) -> Result<EvidenceAssetSnapshot, String> {
        if actor.trim().is_empty() {
            return Err("restoring evidence requires an actor".into());
        }
        let mut connection = self.connection.lock().map_err(lock_error)?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence restore transition: {error}"))?;
        let changed = tx.execute(
            "UPDATE evidence_assets SET status = 'READY', relative_path = ?2, thumbnail_relative_path = ?3, trashed_at_unix_ms = NULL, purge_after_unix_ms = NULL, delete_reason = '', updated_at_unix_ms = ?4 WHERE id = ?1 AND status = 'TRASHED'",
            params![asset_id, relative_path, thumbnail_relative_path, now],
        ).map_err(|error| format!("restore evidence asset: {error}"))?;
        if changed != 1 {
            return Err("only trashed evidence can be restored".into());
        }
        insert_asset_event(
            &tx,
            asset_id,
            "RESTORED",
            actor.trim(),
            "Evidence restored from recoverable trash",
            &json!({}),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit evidence restore transition: {error}"))?;
        drop(connection);
        self.evidence_asset(asset_id)
    }

    pub(crate) fn begin_evidence_asset_purge(
        &self,
        asset_id: &str,
        now: i64,
    ) -> Result<(), String> {
        let connection = self.connection.lock().map_err(lock_error)?;
        let changed = connection.execute(
            "UPDATE evidence_assets SET status = 'PURGING', updated_at_unix_ms = ?2 WHERE id = ?1 AND status = 'TRASHED' AND retention_class <> 'LEGAL_HOLD' AND purge_after_unix_ms <= ?2",
            params![asset_id, now],
        ).map_err(|error| format!("claim evidence asset purge: {error}"))?;
        if changed != 1 {
            return Err("evidence asset is not eligible for purge".into());
        }
        Ok(())
    }

    pub(crate) fn complete_evidence_asset_purge(
        &self,
        asset_id: &str,
        actor: &str,
        now: i64,
    ) -> Result<(), String> {
        let mut connection = self.connection.lock().map_err(lock_error)?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin evidence purge completion: {error}"))?;
        let changed = tx.execute(
            "UPDATE evidence_assets SET status = 'PURGED', relative_path = '', thumbnail_relative_path = '', purged_at_unix_ms = ?2, updated_at_unix_ms = ?2 WHERE id = ?1 AND status = 'PURGING'",
            params![asset_id, now],
        ).map_err(|error| format!("complete evidence asset purge: {error}"))?;
        if changed != 1 {
            return Err("evidence asset purge was not claimed".into());
        }
        insert_asset_event(
            &tx,
            asset_id,
            "PURGED",
            actor,
            "Recoverable trash grace period elapsed; media bytes purged",
            &json!({}),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit evidence purge transition: {error}"))
    }

    pub(crate) fn cancel_evidence_asset_purge(
        &self,
        asset_id: &str,
        now: i64,
    ) -> Result<(), String> {
        let connection = self.connection.lock().map_err(lock_error)?;
        connection.execute(
            "UPDATE evidence_assets SET status = 'TRASHED', updated_at_unix_ms = ?2 WHERE id = ?1 AND status = 'PURGING'",
            params![asset_id, now],
        ).map_err(|error| format!("cancel evidence asset purge: {error}"))?;
        Ok(())
    }

    pub(crate) fn purging_evidence_assets(&self) -> Result<Vec<EvidenceAssetSnapshot>, String> {
        let connection = self.connection.lock().map_err(lock_error)?;
        let mut statement = connection
            .prepare("SELECT id FROM evidence_assets WHERE status = 'PURGING' ORDER BY updated_at_unix_ms")
            .map_err(|error| format!("prepare interrupted evidence purges: {error}"))?;
        let ids = statement
            .query_map([], |row| row.get::<_, String>(0))
            .map_err(|error| format!("query interrupted evidence purges: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read interrupted evidence purges: {error}"))?;
        ids.iter().map(|id| read_asset(&connection, id)).collect()
    }
}

fn read_asset(
    connection: &rusqlite::Connection,
    asset_id: &str,
) -> Result<EvidenceAssetSnapshot, String> {
    let mut asset = connection
        .query_row(
            r#"
        SELECT id, asset_type, status, review_state, retention_class, source_id,
               drone_id, incident_id, mission_id, mission_run_id, recording_session_id,
               selection_id, track_session_id, track_id, evidence_marker_annotation_id,
               captured_at_unix_ms, source_started_at_unix_ms, source_ended_at_unix_ms,
               requested_start_at_unix_ms, requested_end_at_unix_ms, relative_path,
               thumbnail_relative_path, mime_type, thumbnail_mime_type, byte_length,
               sha256, thumbnail_byte_length, thumbnail_sha256, created_by,
               created_at_unix_ms, updated_at_unix_ms, retain_until_unix_ms,
               trashed_at_unix_ms, purge_after_unix_ms, delete_reason,
               purged_at_unix_ms, error_message
        FROM evidence_assets WHERE id = ?1
        "#,
            [asset_id],
            |row| {
                Ok(EvidenceAssetSnapshot {
                    id: row.get(0)?,
                    asset_type: row.get(1)?,
                    status: row.get(2)?,
                    review_state: row.get(3)?,
                    retention_class: row.get(4)?,
                    source_id: row.get(5)?,
                    drone_id: row.get(6)?,
                    incident_id: row.get(7)?,
                    mission_id: row.get(8)?,
                    mission_run_id: row.get(9)?,
                    recording_session_id: row.get(10)?,
                    selection_id: row.get(11)?,
                    track_session_id: row.get(12)?,
                    track_id: row.get(13)?,
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
                    retain_until_unix_ms: row.get(31)?,
                    trashed_at_unix_ms: row.get(32)?,
                    purge_after_unix_ms: row.get(33)?,
                    delete_reason: row.get(34)?,
                    purged_at_unix_ms: row.get(35)?,
                    error_message: row.get(36)?,
                    annotations: Vec::new(),
                    events: Vec::new(),
                })
            },
        )
        .optional()
        .map_err(|error| format!("read evidence asset: {error}"))?
        .ok_or_else(|| format!("evidence asset {asset_id} was not found"))?;
    asset.annotations = read_annotations(connection, asset_id)?;
    asset.events = read_asset_events(connection, asset_id)?;
    Ok(asset)
}

fn read_annotations(
    connection: &rusqlite::Connection,
    asset_id: &str,
) -> Result<Vec<EvidenceAssetAnnotationSnapshot>, String> {
    let mut statement = connection.prepare("SELECT id, annotation_type, body, created_by, created_at_unix_ms FROM evidence_asset_annotations WHERE evidence_asset_id = ?1 ORDER BY created_at_unix_ms, rowid")
        .map_err(|error| format!("prepare evidence annotations: {error}"))?;
    let rows = statement
        .query_map([asset_id], |row| {
            Ok(EvidenceAssetAnnotationSnapshot {
                id: row.get(0)?,
                annotation_type: row.get(1)?,
                body: row.get(2)?,
                created_by: row.get(3)?,
                created_at_unix_ms: row.get(4)?,
            })
        })
        .map_err(|error| format!("query evidence annotations: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read evidence annotations: {error}"))?;
    Ok(rows)
}

fn read_asset_events(
    connection: &rusqlite::Connection,
    asset_id: &str,
) -> Result<Vec<EvidenceAssetEventSnapshot>, String> {
    let mut statement = connection.prepare("SELECT id, sequence, event_type, actor, message, details_json, occurred_at_unix_ms FROM evidence_asset_events WHERE evidence_asset_id = ?1 ORDER BY sequence")
        .map_err(|error| format!("prepare evidence asset events: {error}"))?;
    let rows = statement
        .query_map([asset_id], |row| {
            let details_json: String = row.get(5)?;
            Ok(EvidenceAssetEventSnapshot {
                id: row.get(0)?,
                sequence: row.get::<_, i64>(1)?.max(0) as u64,
                event_type: row.get(2)?,
                actor: row.get(3)?,
                message: row.get(4)?,
                details: serde_json::from_str(&details_json).unwrap_or_else(|_| json!({})),
                occurred_at_unix_ms: row.get(6)?,
            })
        })
        .map_err(|error| format!("query evidence asset events: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read evidence asset events: {error}"))?;
    Ok(rows)
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
        .map_err(|error| format!("validate evidence aircraft: {error}"))?;
    if !drone_exists {
        return Err(format!("evidence aircraft {drone_id} was not found"));
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
            .map_err(|error| format!("validate evidence mission run: {error}"))?
            .ok_or_else(|| format!("evidence mission run {run_id} was not found"))?;
        if run_drone != drone_id {
            return Err("evidence mission run belongs to another aircraft".into());
        }
        if resolved_mission
            .as_deref()
            .is_some_and(|id| id != run_mission)
        {
            return Err("evidence mission does not match the mission run".into());
        }
        resolved_mission = Some(run_mission);
    } else if let Some(mission_id) = resolved_mission.as_deref() {
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
    let linked_incident: Option<String> = if let Some(run_id) = mission_run_id {
        tx.query_row(
            "SELECT incident_id FROM incident_assignments WHERE mission_run_id = ?1",
            [run_id],
            |row| row.get(0),
        )
        .optional()
        .map_err(|error| format!("resolve evidence incident from run: {error}"))?
    } else {
        None
    };
    if incident_id.is_some_and(|id| {
        linked_incident
            .as_deref()
            .is_some_and(|linked| linked != id)
    }) {
        return Err("evidence incident does not match the mission run assignment".into());
    }
    if let Some(id) = incident_id {
        let exists: bool = tx
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM incidents WHERE id = ?1)",
                [id],
                |row| row.get(0),
            )
            .map_err(|error| format!("validate evidence incident: {error}"))?;
        if !exists {
            return Err(format!("evidence incident {id} was not found"));
        }
    }
    let (track_session_id, track_id) = if let Some(selection_id) = selection_id {
        let selected: (String, String, String) = tx.query_row(
            "SELECT drone_id, track_session_id, track_id FROM perception_track_selections WHERE id = ?1 AND status <> 'CLEARED'",
            [selection_id], |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
        ).optional().map_err(|error| format!("validate evidence track selection: {error}"))?
            .ok_or_else(|| "evidence track selection is unavailable or cleared".to_string())?;
        if selected.0 != drone_id {
            return Err("evidence track selection belongs to another aircraft".into());
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

fn read_retention_policy(
    connection: &rusqlite::Connection,
) -> Result<EvidenceRetentionPolicySnapshot, String> {
    connection.query_row(
        "SELECT enabled, default_retention_days, extended_retention_days, trash_grace_days, updated_by, updated_at_unix_ms FROM evidence_retention_policy WHERE singleton_id = 1",
        [], |row| Ok(EvidenceRetentionPolicySnapshot {
            enabled: row.get(0)?, default_retention_days: row.get::<_, i64>(1)?.max(1) as u64,
            extended_retention_days: row.get::<_, i64>(2)?.max(1) as u64, trash_grace_days: row.get::<_, i64>(3)?.max(1) as u64,
            updated_by: row.get(4)?, updated_at_unix_ms: row.get(5)?,
        }),
    ).map_err(|error| format!("read evidence retention policy: {error}"))
}

fn insert_annotation(
    tx: &Transaction<'_>,
    asset_id: &str,
    annotation_type: &str,
    body: &str,
    actor: &str,
    now: i64,
) -> Result<(), String> {
    tx.execute("INSERT INTO evidence_asset_annotations (id, evidence_asset_id, annotation_type, body, created_by, created_at_unix_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
        params![generate_id(tx)?, asset_id, annotation_type, body, actor, now])
        .map_err(|error| format!("insert evidence asset annotation: {error}"))?;
    Ok(())
}

fn insert_asset_event(
    tx: &Transaction<'_>,
    asset_id: &str,
    event_type: &str,
    actor: &str,
    message: &str,
    details: &Value,
    now: i64,
) -> Result<(), String> {
    let sequence: i64 = tx.query_row("SELECT COALESCE(MAX(sequence), 0) + 1 FROM evidence_asset_events WHERE evidence_asset_id = ?1", [asset_id], |row| row.get(0))
        .map_err(|error| format!("allocate evidence asset event sequence: {error}"))?;
    let details_json = serde_json::to_string(details)
        .map_err(|error| format!("serialize evidence asset event: {error}"))?;
    tx.execute("INSERT INTO evidence_asset_events (id, evidence_asset_id, sequence, event_type, actor, message, details_json, occurred_at_unix_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
        params![generate_id(tx)?, asset_id, sequence, event_type, actor, message, details_json, now])
        .map_err(|error| format!("insert evidence asset event: {error}"))?;
    Ok(())
}

fn require_mutable_asset(tx: &Transaction<'_>, asset_id: &str) -> Result<(), String> {
    let status: Option<String> = tx
        .query_row(
            "SELECT status FROM evidence_assets WHERE id = ?1",
            [asset_id],
            |row| row.get(0),
        )
        .optional()
        .map_err(|error| format!("read evidence asset state: {error}"))?;
    match status.as_deref() {
        Some("READY" | "PENDING" | "FAILED") => Ok(()),
        Some("TRASHED" | "PURGING") => {
            Err("restore evidence before changing its review record".into())
        }
        Some("PURGED") => Err("purged evidence cannot be changed".into()),
        _ => Err(format!("evidence asset {asset_id} was not found")),
    }
}

fn candidate_assets(
    database: &LocalDatabase,
    predicate: &str,
    now: i64,
) -> Result<Vec<EvidenceAssetSnapshot>, String> {
    let connection = database.connection.lock().map_err(lock_error)?;
    let sql =
        format!("SELECT id FROM evidence_assets WHERE {predicate} ORDER BY captured_at_unix_ms");
    let mut statement = connection
        .prepare(&sql)
        .map_err(|error| format!("prepare evidence retention candidates: {error}"))?;
    let ids = statement
        .query_map([now], |row| row.get::<_, String>(0))
        .map_err(|error| format!("query evidence retention candidates: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read evidence retention candidates: {error}"))?;
    ids.iter().map(|id| read_asset(&connection, id)).collect()
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
        return Err("evidence asset file manifest is invalid".into());
    }
    Ok(())
}

fn validate_actor_and_identity(identity: &str, actor: &str) -> Result<(), String> {
    if identity.trim().is_empty()
        || actor.trim().is_empty()
        || identity.len() > 256
        || actor.len() > 128
    {
        return Err("evidence asset identity and actor are required".into());
    }
    Ok(())
}

fn retention_deadline(start: i64, days: u64) -> i64 {
    start.saturating_add((days.min(i64::MAX as u64 / DAY_MS as u64) as i64).saturating_mul(DAY_MS))
}

fn valid_review_state(value: &str) -> bool {
    matches!(
        value,
        "UNREVIEWED" | "RELEVANT" | "NOT_RELEVANT" | "NEEDS_FOLLOW_UP"
    )
}

fn generate_id(tx: &Transaction<'_>) -> Result<String, String> {
    tx.query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate evidence asset id: {error}"))
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
