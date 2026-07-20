use std::sync::atomic::{AtomicU64, Ordering};

use rusqlite::{params, OptionalExtension, Transaction};
use serde::{Deserialize, Serialize};

use super::LocalDatabase;

static PERCEPTION_OPERATION_ID: AtomicU64 = AtomicU64::new(0);

#[derive(Debug, Clone, Deserialize, Serialize)]
pub(crate) struct CountingPoint {
    pub x: f64,
    pub y: f64,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct UpsertCountingRuleInput {
    pub id: Option<String>,
    pub drone_id: String,
    pub source_id: String,
    pub label: String,
    pub rule_type: String,
    pub points: Vec<CountingPoint>,
    #[serde(default)]
    pub class_ids: Vec<i32>,
    #[serde(default = "default_true")]
    pub enabled: bool,
    #[serde(default = "default_operator")]
    pub actor: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct CountingRuleSnapshot {
    pub id: String,
    pub drone_id: String,
    pub source_id: String,
    pub label: String,
    pub rule_type: String,
    pub revision: u64,
    pub points: Vec<CountingPoint>,
    pub class_ids: Vec<i32>,
    pub enabled: bool,
    pub updated_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct RuleCountSnapshot {
    pub rule_id: String,
    pub label: String,
    pub rule_type: String,
    pub rule_revision: u64,
    pub line_forward: u64,
    pub line_reverse: u64,
    pub polygon_entries: u64,
    pub polygon_exits: u64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct PerceptionCountSnapshot {
    pub source_id: String,
    pub track_session_id: Option<String>,
    pub current_visible_count: u64,
    pub unique_session_tracks: u64,
    pub mission_id: Option<String>,
    pub mission_run_id: Option<String>,
    pub unique_mission_tracks: u64,
    pub rule_counts: Vec<RuleCountSnapshot>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct SelectTrackInput {
    pub drone_id: String,
    pub track_session_id: String,
    pub track_id: String,
    #[serde(default = "default_operator")]
    pub actor: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TrackSelectionSnapshot {
    pub selection_id: String,
    pub drone_id: String,
    pub track_session_id: String,
    pub track_id: String,
    pub status: String,
    pub selected_by: String,
    pub selected_at_unix_ms: i64,
    pub last_state_change_at_unix_ms: i64,
    pub result_reason: String,
    pub lifecycle_state: String,
    pub age_frames: u64,
    pub observation_count: u64,
    pub last_observed_at_unix_ms: i64,
    pub confidence: f64,
    pub prediction_confidence: f64,
    pub class_label: String,
    pub annotation_count: u64,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TrackAnnotationInput {
    pub selection_id: String,
    pub annotation_type: String,
    #[serde(default)]
    pub body: String,
    pub evidence_recording_session_id: Option<String>,
    #[serde(default = "default_operator")]
    pub actor: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TrackAnnotationSnapshot {
    pub id: String,
    pub selection_id: String,
    pub track_session_id: String,
    pub track_id: String,
    pub annotation_type: String,
    pub body: String,
    pub evidence_recording_session_id: Option<String>,
    pub marked_at_unix_ms: i64,
    pub created_by: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TrackSampleSnapshot {
    pub revision: u64,
    pub sample_reason: String,
    pub lifecycle_state: String,
    pub observed_at_unix_ms: i64,
    pub box_x: f64,
    pub box_y: f64,
    pub box_width: f64,
    pub box_height: f64,
    pub detection_confidence: f64,
    pub predicted_box_x: Option<f64>,
    pub predicted_box_y: Option<f64>,
    pub predicted_box_width: Option<f64>,
    pub predicted_box_height: Option<f64>,
    pub prediction_confidence: f64,
}

impl LocalDatabase {
    pub(crate) fn upsert_perception_counting_rule(
        &self,
        input: &UpsertCountingRuleInput,
        now: i64,
    ) -> Result<CountingRuleSnapshot, String> {
        validate_counting_rule(input)?;
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin counting rule update: {error}"))?;
        let id = input
            .id
            .as_deref()
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .map(str::to_string)
            .unwrap_or_else(|| new_operation_id("count-rule", now));
        let existing = tx
            .query_row(
                "SELECT drone_id, source_id, revision, created_at_unix_ms FROM perception_counting_rules WHERE id = ?1",
                [&id],
                |row| Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?, row.get::<_, i64>(2)?, row.get::<_, i64>(3)?)),
            )
            .optional()
            .map_err(|error| format!("read counting rule: {error}"))?;
        if let Some((drone_id, source_id, _, _)) = &existing {
            if drone_id != input.drone_id.trim() || source_id != input.source_id.trim() {
                return Err("counting rule cannot move between an aircraft or source".into());
            }
        }
        let drone_exists = tx
            .query_row(
                "SELECT 1 FROM drones WHERE id = ?1",
                [input.drone_id.trim()],
                |_| Ok(()),
            )
            .optional()
            .map_err(|error| format!("validate counting rule aircraft: {error}"))?
            .is_some();
        if !drone_exists {
            return Err("counting rule aircraft does not exist".into());
        }
        let revision = existing
            .as_ref()
            .map(|value| value.2.max(0) as u64 + 1)
            .unwrap_or(1);
        let created_at = existing.as_ref().map(|value| value.3).unwrap_or(now);
        tx.execute(
            r#"
            INSERT INTO perception_counting_rules (
                id, drone_id, source_id, label, rule_type, revision,
                points_json, class_ids_json, enabled, created_by,
                created_at_unix_ms, updated_at_unix_ms
            ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)
            ON CONFLICT(id) DO UPDATE SET
                label = excluded.label, rule_type = excluded.rule_type,
                revision = excluded.revision, points_json = excluded.points_json,
                class_ids_json = excluded.class_ids_json, enabled = excluded.enabled,
                updated_at_unix_ms = excluded.updated_at_unix_ms
            "#,
            params![
                id,
                input.drone_id.trim(),
                input.source_id.trim(),
                input.label.trim(),
                input.rule_type,
                revision as i64,
                serde_json::to_string(&input.points)
                    .map_err(|error| format!("encode counting rule points: {error}"))?,
                serde_json::to_string(&input.class_ids)
                    .map_err(|error| format!("encode counting rule classes: {error}"))?,
                input.enabled,
                input.actor.trim(),
                created_at,
                now,
            ],
        )
        .map_err(|error| format!("upsert counting rule: {error}"))?;
        tx.commit()
            .map_err(|error| format!("commit counting rule update: {error}"))?;
        drop(connection);
        self.perception_counting_rule(&id)
    }

    pub(crate) fn perception_counting_rules(
        &self,
        drone_id: &str,
        source_id: Option<&str>,
        enabled_only: bool,
    ) -> Result<Vec<CountingRuleSnapshot>, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare(
                r#"
                SELECT id, drone_id, source_id, label, rule_type, revision,
                       points_json, class_ids_json, enabled, updated_at_unix_ms
                FROM perception_counting_rules
                WHERE drone_id = ?1 AND (?2 IS NULL OR source_id = ?2)
                  AND (NOT ?3 OR enabled = 1)
                ORDER BY source_id, label, id
                "#,
            )
            .map_err(|error| format!("prepare counting rules: {error}"))?;
        let records = statement
            .query_map(
                params![drone_id, source_id, enabled_only],
                counting_rule_row,
            )
            .map_err(|error| format!("query counting rules: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read counting rules: {error}"))?;
        Ok(records)
    }

    pub(crate) fn perception_counts(
        &self,
        drone_id: &str,
        source_id: &str,
        mission_run_id: Option<&str>,
    ) -> Result<PerceptionCountSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let session = connection
            .query_row(
                r#"
                SELECT id, current_visible_count, unique_confirmed_count,
                       mission_id, mission_run_id
                FROM perception_track_sessions
                WHERE drone_id = ?1 AND source_id = ?2
                ORDER BY (ended_at_unix_ms IS NULL) DESC, last_update_at_unix_ms DESC
                LIMIT 1
                "#,
                params![drone_id, source_id],
                |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        row.get::<_, i64>(1)?,
                        row.get::<_, i64>(2)?,
                        row.get::<_, Option<String>>(3)?,
                        row.get::<_, Option<String>>(4)?,
                    ))
                },
            )
            .optional()
            .map_err(|error| format!("read current track counts: {error}"))?;
        let requested_run = mission_run_id
            .map(str::to_string)
            .or_else(|| session.as_ref().and_then(|value| value.4.clone()));
        let mission = requested_run.as_ref().and_then(|run_id| {
            connection
                .query_row(
                    "SELECT mission_id FROM mission_runs WHERE id = ?1 AND drone_id = ?2",
                    params![run_id, drone_id],
                    |row| row.get::<_, String>(0),
                )
                .optional()
                .ok()
                .flatten()
        });
        let unique_mission_tracks = requested_run
            .as_ref()
            .map(|run_id| {
                connection
                    .query_row(
                        "SELECT count(*) FROM perception_mission_tracks WHERE mission_run_id = ?1",
                        [run_id],
                        |row| row.get::<_, i64>(0),
                    )
                    .unwrap_or(0)
                    .max(0) as u64
            })
            .unwrap_or(0);
        let mut rule_counts = Vec::new();
        if let Some((session_id, _, _, _, _)) = &session {
            let mut statement = connection
                .prepare(
                    r#"
                    SELECT counts.counting_rule_id, rules.label, counts.rule_type,
                           counts.rule_revision, counts.line_forward,
                           counts.line_reverse, counts.polygon_entries,
                           counts.polygon_exits
                    FROM perception_track_rule_counts counts
                    JOIN perception_counting_rules rules ON rules.id = counts.counting_rule_id
                    WHERE counts.track_session_id = ?1
                    ORDER BY rules.label, counts.counting_rule_id
                    "#,
                )
                .map_err(|error| format!("prepare track rule counts: {error}"))?;
            rule_counts = statement
                .query_map([session_id], |row| {
                    Ok(RuleCountSnapshot {
                        rule_id: row.get(0)?,
                        label: row.get(1)?,
                        rule_type: row.get(2)?,
                        rule_revision: row.get::<_, i64>(3)?.max(0) as u64,
                        line_forward: row.get::<_, i64>(4)?.max(0) as u64,
                        line_reverse: row.get::<_, i64>(5)?.max(0) as u64,
                        polygon_entries: row.get::<_, i64>(6)?.max(0) as u64,
                        polygon_exits: row.get::<_, i64>(7)?.max(0) as u64,
                    })
                })
                .map_err(|error| format!("query track rule counts: {error}"))?
                .collect::<Result<Vec<_>, _>>()
                .map_err(|error| format!("read track rule counts: {error}"))?;
        }
        Ok(PerceptionCountSnapshot {
            source_id: source_id.to_string(),
            track_session_id: session.as_ref().map(|value| value.0.clone()),
            current_visible_count: session
                .as_ref()
                .map(|value| value.1.max(0) as u64)
                .unwrap_or(0),
            unique_session_tracks: session
                .as_ref()
                .map(|value| value.2.max(0) as u64)
                .unwrap_or(0),
            mission_id: mission,
            mission_run_id: requested_run,
            unique_mission_tracks,
            rule_counts,
        })
    }

    pub(crate) fn select_perception_track(
        &self,
        input: &SelectTrackInput,
        now: i64,
    ) -> Result<TrackSelectionSnapshot, String> {
        validate_selection_input(input)?;
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin track selection: {error}"))?;
        let track_snapshot = tx
            .query_row(
                r#"
                SELECT tracks.lifecycle_state, tracks.age_frames,
                       tracks.observation_count, tracks.last_observed_at_unix_ms,
                       tracks.latest_detection_confidence,
                       tracks.prediction_confidence, tracks.class_label
                FROM perception_tracks tracks
                JOIN perception_track_sessions sessions ON sessions.id = tracks.track_session_id
                WHERE tracks.id = ?1 AND tracks.track_session_id = ?2
                  AND tracks.drone_id = ?3 AND sessions.ended_at_unix_ms IS NULL
                "#,
                params![input.track_id, input.track_session_id, input.drone_id],
                |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        row.get::<_, i64>(1)?,
                        row.get::<_, i64>(2)?,
                        row.get::<_, i64>(3)?,
                        row.get::<_, f64>(4)?,
                        row.get::<_, f64>(5)?,
                        row.get::<_, String>(6)?,
                    ))
                },
            )
            .optional()
            .map_err(|error| format!("validate selectable track: {error}"))?
            .ok_or_else(|| {
                "track does not belong to the active aircraft tracking session".to_string()
            })?;
        if track_snapshot.0 != "ACTIVE" {
            return Err("only a currently visible confirmed track can be selected".into());
        }
        if let Some(existing_id) = tx
            .query_row(
                r#"
                SELECT id FROM perception_track_selections
                WHERE drone_id = ?1 AND track_session_id = ?2 AND track_id = ?3
                  AND status IN ('SELECTED', 'OCCLUDED')
                "#,
                params![input.drone_id, input.track_session_id, input.track_id],
                |row| row.get::<_, String>(0),
            )
            .optional()
            .map_err(|error| format!("read existing track selection: {error}"))?
        {
            drop(tx);
            drop(connection);
            return self
                .perception_track_selection(&input.drone_id)?
                .filter(|selection| selection.selection_id == existing_id)
                .ok_or_else(|| "existing selection disappeared".to_string());
        }
        clear_active_selection(&tx, &input.drone_id, "REPLACED", &input.actor, now)?;
        let selection_id = new_operation_id("track-selection", now);
        tx.execute(
            r#"
            INSERT INTO perception_track_selections (
                id, drone_id, track_session_id, track_id, status, selected_by,
                selected_at_unix_ms, last_state_change_at_unix_ms,
                cleared_at_unix_ms, result_reason, snapshot_lifecycle_state,
                snapshot_age_frames, snapshot_observation_count,
                snapshot_last_observed_at_unix_ms,
                snapshot_detection_confidence, snapshot_prediction_confidence,
                snapshot_class_label, created_at_unix_ms, updated_at_unix_ms
            ) VALUES (
                ?1, ?2, ?3, ?4, 'SELECTED', ?5, ?6, ?6, NULL, '',
                ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?6, ?6
            )
            "#,
            params![
                selection_id,
                input.drone_id,
                input.track_session_id,
                input.track_id,
                input.actor.trim(),
                now,
                track_snapshot.0,
                track_snapshot.1,
                track_snapshot.2,
                track_snapshot.3,
                track_snapshot.4,
                track_snapshot.5,
                track_snapshot.6,
            ],
        )
        .map_err(|error| format!("create track selection: {error}"))?;
        append_selection_event(
            &tx,
            &selection_id,
            "SELECTED",
            "ACTIVE",
            &input.actor,
            "",
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit track selection: {error}"))?;
        drop(connection);
        self.perception_track_selection(&input.drone_id)?
            .ok_or_else(|| "created selection was not readable".to_string())
    }

    pub(crate) fn clear_perception_track_selection(
        &self,
        drone_id: &str,
        actor: &str,
        now: i64,
    ) -> Result<(), String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin clearing track selection: {error}"))?;
        clear_latest_selection(&tx, drone_id, actor, now)?;
        tx.commit()
            .map_err(|error| format!("commit cleared track selection: {error}"))
    }

    pub(crate) fn perception_track_selection(
        &self,
        drone_id: &str,
    ) -> Result<Option<TrackSelectionSnapshot>, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        connection
            .query_row(
                r#"
                SELECT selections.id, selections.drone_id,
                       selections.track_session_id, selections.track_id,
                       selections.status, selections.selected_by,
                       selections.selected_at_unix_ms,
                       selections.last_state_change_at_unix_ms,
                       selections.result_reason,
                       selections.snapshot_lifecycle_state,
                       selections.snapshot_age_frames,
                       selections.snapshot_observation_count,
                       selections.snapshot_last_observed_at_unix_ms,
                       selections.snapshot_detection_confidence,
                       selections.snapshot_prediction_confidence,
                       selections.snapshot_class_label,
                       (SELECT count(*) FROM perception_track_annotations annotations
                         WHERE annotations.selection_id = selections.id)
                FROM perception_track_selections selections
                WHERE selections.drone_id = ?1 AND selections.status <> 'CLEARED'
                ORDER BY selections.selected_at_unix_ms DESC, selections.id DESC
                LIMIT 1
                "#,
                [drone_id],
                |row| {
                    Ok(TrackSelectionSnapshot {
                        selection_id: row.get(0)?,
                        drone_id: row.get(1)?,
                        track_session_id: row.get(2)?,
                        track_id: row.get(3)?,
                        status: row.get(4)?,
                        selected_by: row.get(5)?,
                        selected_at_unix_ms: row.get(6)?,
                        last_state_change_at_unix_ms: row.get(7)?,
                        result_reason: row.get(8)?,
                        lifecycle_state: row.get(9)?,
                        age_frames: row.get::<_, i64>(10)?.max(0) as u64,
                        observation_count: row.get::<_, i64>(11)?.max(0) as u64,
                        last_observed_at_unix_ms: row.get(12)?,
                        confidence: row.get(13)?,
                        prediction_confidence: row.get(14)?,
                        class_label: row.get(15)?,
                        annotation_count: row.get::<_, i64>(16)?.max(0) as u64,
                    })
                },
            )
            .optional()
            .map_err(|error| format!("read track selection: {error}"))
    }

    pub(crate) fn annotate_perception_track(
        &self,
        input: &TrackAnnotationInput,
        now: i64,
    ) -> Result<TrackAnnotationSnapshot, String> {
        if input.selection_id.trim().is_empty()
            || !matches!(input.annotation_type.as_str(), "NOTE" | "EVIDENCE_MARKER")
            || input.actor.trim().is_empty()
            || (input.annotation_type == "NOTE" && input.body.trim().is_empty())
            || input.body.len() > 2_000
            || (input.annotation_type == "EVIDENCE_MARKER"
                && input.evidence_recording_session_id.is_none())
        {
            return Err("track annotation is invalid".into());
        }
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin track annotation: {error}"))?;
        let (track_session_id, track_id, lifecycle_state, drone_id) = tx
            .query_row(
                r#"
                SELECT selections.track_session_id, selections.track_id,
                       selections.snapshot_lifecycle_state, selections.drone_id
                FROM perception_track_selections selections
                WHERE selections.id = ?1 AND selections.status <> 'CLEARED'
                "#,
                [input.selection_id.trim()],
                |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        row.get::<_, String>(1)?,
                        row.get::<_, String>(2)?,
                        row.get::<_, String>(3)?,
                    ))
                },
            )
            .optional()
            .map_err(|error| format!("read annotation selection: {error}"))?
            .ok_or_else(|| "track selection is unavailable or cleared".to_string())?;
        if let Some(recording_id) = input.evidence_recording_session_id.as_deref() {
            let valid = tx
                .query_row(
                    "SELECT 1 FROM evidence_recording_sessions WHERE id = ?1 AND drone_id = ?2 AND status = 'RUNNING'",
                    params![recording_id, drone_id],
                    |_| Ok(()),
                )
                .optional()
                .map_err(|error| format!("validate track evidence recording: {error}"))?
                .is_some();
            if !valid {
                return Err("evidence marker requires the aircraft's active recording".into());
            }
        }
        let id = new_operation_id("track-annotation", now);
        tx.execute(
            r#"
            INSERT INTO perception_track_annotations (
                id, selection_id, track_session_id, track_id, annotation_type,
                body, evidence_recording_session_id, marked_at_unix_ms,
                created_by, created_at_unix_ms
            ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?8)
            "#,
            params![
                id,
                input.selection_id.trim(),
                track_session_id,
                track_id,
                input.annotation_type,
                input.body.trim(),
                input.evidence_recording_session_id,
                now,
                input.actor.trim(),
            ],
        )
        .map_err(|error| format!("insert track annotation: {error}"))?;
        append_selection_event(
            &tx,
            input.selection_id.trim(),
            if input.annotation_type == "NOTE" {
                "ANNOTATED"
            } else {
                "EVIDENCE_MARKED"
            },
            &lifecycle_state,
            &input.actor,
            input.body.trim(),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit track annotation: {error}"))?;
        Ok(TrackAnnotationSnapshot {
            id,
            selection_id: input.selection_id.trim().to_string(),
            track_session_id,
            track_id,
            annotation_type: input.annotation_type.clone(),
            body: input.body.trim().to_string(),
            evidence_recording_session_id: input.evidence_recording_session_id.clone(),
            marked_at_unix_ms: now,
            created_by: input.actor.trim().to_string(),
        })
    }

    pub(crate) fn perception_track_samples(
        &self,
        track_session_id: &str,
        track_id: &str,
        limit: usize,
    ) -> Result<Vec<TrackSampleSnapshot>, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare(
                r#"
                SELECT samples.revision, samples.sample_reason,
                       samples.lifecycle_state, samples.observed_at_unix_ms,
                       samples.box_x, samples.box_y, samples.box_width,
                       samples.box_height, samples.detection_confidence,
                       samples.predicted_box_x, samples.predicted_box_y,
                       samples.predicted_box_width, samples.predicted_box_height,
                       samples.prediction_confidence
                FROM perception_track_samples samples
                JOIN perception_tracks tracks ON tracks.id = samples.track_id
                WHERE tracks.track_session_id = ?1 AND tracks.id = ?2
                ORDER BY samples.revision DESC
                LIMIT ?3
                "#,
            )
            .map_err(|error| format!("prepare track samples: {error}"))?;
        let samples = statement
            .query_map(
                params![track_session_id, track_id, limit.clamp(1, 240) as i64],
                |row| {
                    Ok(TrackSampleSnapshot {
                        revision: row.get::<_, i64>(0)?.max(0) as u64,
                        sample_reason: row.get(1)?,
                        lifecycle_state: row.get(2)?,
                        observed_at_unix_ms: row.get(3)?,
                        box_x: row.get(4)?,
                        box_y: row.get(5)?,
                        box_width: row.get(6)?,
                        box_height: row.get(7)?,
                        detection_confidence: row.get(8)?,
                        predicted_box_x: row.get(9)?,
                        predicted_box_y: row.get(10)?,
                        predicted_box_width: row.get(11)?,
                        predicted_box_height: row.get(12)?,
                        prediction_confidence: row.get(13)?,
                    })
                },
            )
            .map_err(|error| format!("query track samples: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read track samples: {error}"))?;
        Ok(samples)
    }

    fn perception_counting_rule(&self, id: &str) -> Result<CountingRuleSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        connection
            .query_row(
                r#"
                SELECT id, drone_id, source_id, label, rule_type, revision,
                       points_json, class_ids_json, enabled, updated_at_unix_ms
                FROM perception_counting_rules WHERE id = ?1
                "#,
                [id],
                counting_rule_row,
            )
            .map_err(|error| format!("read counting rule: {error}"))
    }
}

pub(crate) fn reconcile_track_selection(
    tx: &Transaction<'_>,
    track_session_id: &str,
    track_id: &str,
    lifecycle_state: &str,
    closure_reason: &str,
    observed_at_unix_ms: i64,
) -> Result<(), String> {
    let selection = tx
        .query_row(
            r#"
            SELECT id, status FROM perception_track_selections
            WHERE track_session_id = ?1 AND track_id = ?2
              AND status IN ('SELECTED', 'OCCLUDED')
            "#,
            params![track_session_id, track_id],
            |row| Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?)),
        )
        .optional()
        .map_err(|error| format!("read lifecycle track selection: {error}"))?;
    let Some((selection_id, current_status)) = selection else {
        return Ok(());
    };
    tx.execute(
        r#"
        UPDATE perception_track_selections
        SET snapshot_lifecycle_state = tracks.lifecycle_state,
            snapshot_age_frames = tracks.age_frames,
            snapshot_observation_count = tracks.observation_count,
            snapshot_last_observed_at_unix_ms = tracks.last_observed_at_unix_ms,
            snapshot_detection_confidence = tracks.latest_detection_confidence,
            snapshot_prediction_confidence = tracks.prediction_confidence,
            snapshot_class_label = tracks.class_label,
            updated_at_unix_ms = ?4
        FROM perception_tracks tracks
        WHERE perception_track_selections.id = ?1
          AND tracks.id = ?2 AND tracks.track_session_id = ?3
        "#,
        params![
            selection_id,
            track_id,
            track_session_id,
            observed_at_unix_ms
        ],
    )
    .map_err(|error| format!("refresh selected track snapshot: {error}"))?;
    let (status, event_type, reason) = match lifecycle_state {
        "ACTIVE" if current_status == "OCCLUDED" => ("SELECTED", "REACQUIRED", ""),
        "TEMPORARILY_OCCLUDED" if current_status == "SELECTED" => ("OCCLUDED", "OCCLUDED", ""),
        "LOST" => ("LOST", "LOST", "TRACK_LOST"),
        "CLOSED" => ("CLOSED", "CLOSED", closure_reason),
        _ => return Ok(()),
    };
    tx.execute(
        r#"
        UPDATE perception_track_selections
        SET status = ?2, last_state_change_at_unix_ms = ?3,
            result_reason = ?4, updated_at_unix_ms = ?3
        WHERE id = ?1
        "#,
        params![selection_id, status, observed_at_unix_ms, reason],
    )
    .map_err(|error| format!("update lifecycle track selection: {error}"))?;
    append_selection_event(
        tx,
        &selection_id,
        event_type,
        lifecycle_state,
        "atlas-agent",
        reason,
        observed_at_unix_ms,
    )
}

fn clear_active_selection(
    tx: &Transaction<'_>,
    drone_id: &str,
    event_type: &str,
    actor: &str,
    now: i64,
) -> Result<(), String> {
    let selected = tx
        .query_row(
            r#"
            SELECT selections.id, selections.snapshot_lifecycle_state
            FROM perception_track_selections selections
            WHERE selections.drone_id = ?1
              AND selections.status IN ('SELECTED', 'OCCLUDED')
            "#,
            [drone_id],
            |row| Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?)),
        )
        .optional()
        .map_err(|error| format!("read active track selection: {error}"))?;
    let Some((selection_id, lifecycle_state)) = selected else {
        return Ok(());
    };
    tx.execute(
        r#"
        UPDATE perception_track_selections
        SET status = 'CLEARED', cleared_at_unix_ms = ?2,
            last_state_change_at_unix_ms = ?2, result_reason = ?3,
            updated_at_unix_ms = ?2
        WHERE id = ?1
        "#,
        params![selection_id, now, event_type],
    )
    .map_err(|error| format!("clear active track selection: {error}"))?;
    append_selection_event(
        tx,
        &selection_id,
        event_type,
        &lifecycle_state,
        actor,
        event_type,
        now,
    )
}

fn clear_latest_selection(
    tx: &Transaction<'_>,
    drone_id: &str,
    actor: &str,
    now: i64,
) -> Result<(), String> {
    let selected = tx
        .query_row(
            r#"
            SELECT selections.id, selections.snapshot_lifecycle_state
            FROM perception_track_selections selections
            WHERE selections.drone_id = ?1 AND selections.status <> 'CLEARED'
            ORDER BY selections.selected_at_unix_ms DESC, selections.id DESC
            LIMIT 1
            "#,
            [drone_id],
            |row| Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?)),
        )
        .optional()
        .map_err(|error| format!("read latest track selection: {error}"))?;
    let Some((selection_id, lifecycle_state)) = selected else {
        return Ok(());
    };
    tx.execute(
        r#"
        UPDATE perception_track_selections
        SET status = 'CLEARED', cleared_at_unix_ms = ?2,
            last_state_change_at_unix_ms = ?2, result_reason = 'CLEARED',
            updated_at_unix_ms = ?2
        WHERE id = ?1
        "#,
        params![selection_id, now],
    )
    .map_err(|error| format!("clear latest track selection: {error}"))?;
    append_selection_event(
        tx,
        &selection_id,
        "CLEARED",
        &lifecycle_state,
        actor,
        "CLEARED",
        now,
    )
}

fn append_selection_event(
    tx: &Transaction<'_>,
    selection_id: &str,
    event_type: &str,
    lifecycle_state: &str,
    actor: &str,
    reason: &str,
    occurred_at: i64,
) -> Result<(), String> {
    let sequence = tx
        .query_row(
            "SELECT COALESCE(max(sequence), 0) + 1 FROM perception_track_selection_events WHERE selection_id = ?1",
            [selection_id],
            |row| row.get::<_, i64>(0),
        )
        .map_err(|error| format!("allocate track selection event sequence: {error}"))?;
    tx.execute(
        r#"
        INSERT INTO perception_track_selection_events (
            id, selection_id, sequence, event_type, lifecycle_state,
            actor, reason, details_json, occurred_at_unix_ms
        ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, '{}', ?8)
        "#,
        params![
            format!("{selection_id}:{sequence}"),
            selection_id,
            sequence,
            event_type,
            lifecycle_state,
            actor.trim(),
            reason,
            occurred_at,
        ],
    )
    .map_err(|error| format!("append track selection event: {error}"))?;
    Ok(())
}

fn counting_rule_row(row: &rusqlite::Row<'_>) -> rusqlite::Result<CountingRuleSnapshot> {
    let points_json = row.get::<_, String>(6)?;
    let class_ids_json = row.get::<_, String>(7)?;
    Ok(CountingRuleSnapshot {
        id: row.get(0)?,
        drone_id: row.get(1)?,
        source_id: row.get(2)?,
        label: row.get(3)?,
        rule_type: row.get(4)?,
        revision: row.get::<_, i64>(5)?.max(0) as u64,
        points: serde_json::from_str(&points_json).unwrap_or_default(),
        class_ids: serde_json::from_str(&class_ids_json).unwrap_or_default(),
        enabled: row.get(8)?,
        updated_at_unix_ms: row.get(9)?,
    })
}

fn validate_counting_rule(input: &UpsertCountingRuleInput) -> Result<(), String> {
    if input.drone_id.trim().is_empty()
        || input.source_id.trim().is_empty()
        || input.label.trim().is_empty()
        || input.label.len() > 120
        || input.actor.trim().is_empty()
        || !matches!(input.rule_type.as_str(), "LINE" | "POLYGON")
        || (input.rule_type == "LINE" && input.points.len() != 2)
        || (input.rule_type == "POLYGON" && !(3..=32).contains(&input.points.len()))
        || input.points.iter().any(|point| {
            !point.x.is_finite()
                || !point.y.is_finite()
                || !(0.0..=1.0).contains(&point.x)
                || !(0.0..=1.0).contains(&point.y)
        })
    {
        return Err("perception counting rule is invalid".into());
    }
    let area = input
        .points
        .iter()
        .enumerate()
        .map(|(index, point)| {
            let next = &input.points[(index + 1) % input.points.len()];
            point.x * next.y - next.x * point.y
        })
        .sum::<f64>()
        / 2.0;
    if (input.rule_type == "LINE"
        && (input.points[0].x - input.points[1].x).abs() < 1e-9
        && (input.points[0].y - input.points[1].y).abs() < 1e-9)
        || (input.rule_type == "POLYGON" && area.abs() < 1e-6)
    {
        return Err("perception counting rule geometry is degenerate".into());
    }
    Ok(())
}

fn validate_selection_input(input: &SelectTrackInput) -> Result<(), String> {
    if input.drone_id.trim().is_empty()
        || input.track_session_id.trim().is_empty()
        || input.track_id.trim().is_empty()
        || !input
            .track_id
            .starts_with(&format!("atlas:{}:", input.track_session_id))
        || input.actor.trim().is_empty()
    {
        return Err("track selection identity and actor are required".into());
    }
    Ok(())
}

fn new_operation_id(prefix: &str, now: i64) -> String {
    format!(
        "{prefix}:{now}:{}",
        PERCEPTION_OPERATION_ID.fetch_add(1, Ordering::Relaxed) + 1
    )
}

fn default_true() -> bool {
    true
}

fn default_operator() -> String {
    "operator".to_string()
}
