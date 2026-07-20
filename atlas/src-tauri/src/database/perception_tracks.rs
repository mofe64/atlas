use rusqlite::{params, OptionalExtension};
use serde::Serialize;

use super::{perception_operations::reconcile_track_selection, LocalDatabase};

#[derive(Debug, Clone)]
pub(crate) struct TrackBoundingBoxInput {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

#[derive(Debug, Clone)]
pub(crate) struct PerceptionTrackUpdateInput {
    pub track_id: String,
    pub track_session_id: String,
    pub tracker_type: String,
    pub lifecycle_state: String,
    pub revision: u64,
    pub age_frames: u64,
    pub observation_count: u64,
    pub first_observed_at_unix_ms: i64,
    pub last_observed_at_unix_ms: i64,
    pub latest_confirmed_box: TrackBoundingBoxInput,
    pub latest_detection_confidence: f64,
    pub predicted_box: Option<TrackBoundingBoxInput>,
    pub prediction_confidence: f64,
    pub closed_at_unix_ms: Option<i64>,
    pub closure_reason: String,
    pub class_id: i32,
    pub class_label: String,
    pub update_reason: String,
}

#[derive(Debug, Clone)]
pub(crate) struct PerceptionTrackBatchInput {
    pub source_id: String,
    pub stream_epoch: String,
    pub track_session_id: String,
    pub tracker_type: String,
    pub observed_at_unix_ms: i64,
    pub session_started: bool,
    pub session_ended: bool,
    pub session_end_reason: String,
    pub current_visible: u64,
    pub unique_confirmed: u64,
    pub tracks: Vec<PerceptionTrackUpdateInput>,
    pub rule_counts: Vec<TrackRuleCountInput>,
    pub count_events: Vec<TrackCountEventInput>,
}

#[derive(Debug, Clone)]
pub(crate) struct TrackRuleCountInput {
    pub rule_id: String,
    pub rule_revision: u64,
    pub rule_type: String,
    pub line_forward: u64,
    pub line_reverse: u64,
    pub polygon_entries: u64,
    pub polygon_exits: u64,
}

#[derive(Debug, Clone)]
pub(crate) struct TrackCountEventInput {
    pub event_id: String,
    pub rule_id: String,
    pub rule_revision: u64,
    pub track_session_id: String,
    pub track_id: String,
    pub event_type: String,
    pub observed_at_unix_ms: i64,
    pub anchor_x: f64,
    pub anchor_y: f64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct PerceptionTrackRecordSnapshot {
    pub track_id: String,
    pub track_session_id: String,
    pub source_id: String,
    pub stream_epoch: String,
    pub tracker_type: String,
    pub lifecycle_state: String,
    pub revision: u64,
    pub age_frames: u64,
    pub observation_count: u64,
    pub first_observed_at_unix_ms: i64,
    pub last_observed_at_unix_ms: i64,
    pub latest_confirmed_box: TrackBoundingBoxSnapshot,
    pub latest_detection_confidence: f64,
    pub predicted_box: Option<TrackBoundingBoxSnapshot>,
    pub prediction_confidence: f64,
    pub closed_at_unix_ms: Option<i64>,
    pub closure_reason: String,
    pub class_id: i32,
    pub class_label: String,
    pub event_count: u64,
    pub sample_count: u64,
}

#[derive(Debug, Clone, Serialize)]
pub(crate) struct TrackBoundingBoxSnapshot {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

impl LocalDatabase {
    pub(crate) fn perception_tracks(
        &self,
        drone_id: &str,
        include_closed: bool,
        limit: usize,
    ) -> Result<Vec<PerceptionTrackRecordSnapshot>, String> {
        if drone_id.trim().is_empty() {
            return Err("drone id is required for perception track history".into());
        }
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare(
                r#"
                SELECT tracks.id, tracks.track_session_id,
                       sessions.source_id, sessions.stream_epoch,
                       tracks.tracker_type, tracks.lifecycle_state,
                       tracks.revision, tracks.age_frames,
                       tracks.observation_count,
                       tracks.first_observed_at_unix_ms,
                       tracks.last_observed_at_unix_ms,
                       tracks.latest_box_x, tracks.latest_box_y,
                       tracks.latest_box_width, tracks.latest_box_height,
                       tracks.latest_detection_confidence,
                       tracks.predicted_box_x, tracks.predicted_box_y,
                       tracks.predicted_box_width, tracks.predicted_box_height,
                       tracks.prediction_confidence,
                       tracks.closed_at_unix_ms, tracks.closure_reason,
                       tracks.class_id, tracks.class_label,
                       (SELECT count(*) FROM perception_track_events events
                         WHERE events.track_id = tracks.id),
                       (SELECT count(*) FROM perception_track_samples samples
                         WHERE samples.track_id = tracks.id)
                FROM perception_tracks tracks
                JOIN perception_track_sessions sessions
                  ON sessions.id = tracks.track_session_id
                WHERE tracks.drone_id = ?1
                  AND (?2 OR tracks.lifecycle_state <> 'CLOSED')
                ORDER BY tracks.last_observed_at_unix_ms DESC, tracks.id DESC
                LIMIT ?3
                "#,
            )
            .map_err(|error| format!("prepare perception track history: {error}"))?;
        let records = statement
            .query_map(
                params![drone_id, include_closed, limit.clamp(1, 1_000) as i64],
                |row| {
                    let predicted_x = row.get::<_, Option<f64>>(16)?;
                    let predicted_y = row.get::<_, Option<f64>>(17)?;
                    let predicted_width = row.get::<_, Option<f64>>(18)?;
                    let predicted_height = row.get::<_, Option<f64>>(19)?;
                    let predicted_box =
                        match (predicted_x, predicted_y, predicted_width, predicted_height) {
                            (Some(x), Some(y), Some(width), Some(height)) => {
                                Some(TrackBoundingBoxSnapshot {
                                    x,
                                    y,
                                    width,
                                    height,
                                })
                            }
                            _ => None,
                        };
                    Ok(PerceptionTrackRecordSnapshot {
                        track_id: row.get(0)?,
                        track_session_id: row.get(1)?,
                        source_id: row.get(2)?,
                        stream_epoch: row.get(3)?,
                        tracker_type: row.get(4)?,
                        lifecycle_state: row.get(5)?,
                        revision: row.get::<_, i64>(6)?.max(0) as u64,
                        age_frames: row.get::<_, i64>(7)?.max(0) as u64,
                        observation_count: row.get::<_, i64>(8)?.max(0) as u64,
                        first_observed_at_unix_ms: row.get(9)?,
                        last_observed_at_unix_ms: row.get(10)?,
                        latest_confirmed_box: TrackBoundingBoxSnapshot {
                            x: row.get(11)?,
                            y: row.get(12)?,
                            width: row.get(13)?,
                            height: row.get(14)?,
                        },
                        latest_detection_confidence: row.get(15)?,
                        predicted_box,
                        prediction_confidence: row.get(20)?,
                        closed_at_unix_ms: row.get(21)?,
                        closure_reason: row.get(22)?,
                        class_id: row.get(23)?,
                        class_label: row.get(24)?,
                        event_count: row.get::<_, i64>(25)?.max(0) as u64,
                        sample_count: row.get::<_, i64>(26)?.max(0) as u64,
                    })
                },
            )
            .map_err(|error| format!("query perception track history: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read perception track history: {error}"))?;
        Ok(records)
    }

    pub(crate) fn record_perception_track_batch(
        &self,
        agent_session_id: &str,
        drone_id: &str,
        perception_stream_id: &str,
        batch: &PerceptionTrackBatchInput,
        received_at_unix_ms: i64,
    ) -> Result<(), String> {
        validate_batch(batch)?;
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin perception track update: {error}"))?;
        let communication_link_id = tx
            .query_row(
                r#"
                SELECT links.id
                FROM communication_links links
                JOIN vehicle_agent_bindings bindings
                  ON bindings.id = links.vehicle_agent_binding_id
                WHERE links.session_instance_id = ?1 AND bindings.drone_id = ?2
                "#,
                params![agent_session_id, drone_id],
                |row| row.get::<_, String>(0),
            )
            .optional()
            .map_err(|error| format!("resolve perception track communication link: {error}"))?
            .ok_or_else(|| {
                "perception track update does not match a registered communication link".to_string()
            })?;

        let existing_session = tx
            .query_row(
                "SELECT drone_id, source_id, stream_epoch, tracker_type FROM perception_track_sessions WHERE id = ?1",
                [&batch.track_session_id],
                |row| Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?, row.get::<_, String>(2)?, row.get::<_, String>(3)?)),
            )
            .optional()
            .map_err(|error| format!("read existing perception track session: {error}"))?;
        let starting_session = existing_session.is_none() || batch.session_started;
        if let Some((existing_drone, existing_source, existing_epoch, existing_tracker)) =
            existing_session
        {
            if existing_drone != drone_id
                || existing_source != batch.source_id
                || existing_epoch != batch.stream_epoch
                || existing_tracker != batch.tracker_type
            {
                return Err("perception track session identity changed".to_string());
            }
        }
        if starting_session {
            reconcile_superseded_track_sessions(
                &tx,
                drone_id,
                &batch.source_id,
                &batch.track_session_id,
                batch.observed_at_unix_ms,
                received_at_unix_ms,
            )?;
        }

        let mission_context = tx
            .query_row(
                r#"
                SELECT mission_id, id
                FROM mission_runs
                WHERE drone_id = ?1 AND status IN ('RUNNING', 'PAUSED')
                ORDER BY COALESCE(started_at_unix_ms, updated_at_unix_ms) DESC
                LIMIT 1
                "#,
                [drone_id],
                |row| Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?)),
            )
            .optional()
            .map_err(|error| format!("resolve perception track mission context: {error}"))?;

        let started_at = batch
            .tracks
            .iter()
            .map(|track| track.first_observed_at_unix_ms)
            .min()
            .unwrap_or(batch.observed_at_unix_ms)
            .min(batch.observed_at_unix_ms);
        let ended_at = batch.session_ended.then_some(batch.observed_at_unix_ms);
        tx.execute(
            r#"
            INSERT INTO perception_track_sessions (
                id, drone_id, communication_link_id, perception_stream_id,
                source_id, stream_epoch, tracker_type, started_at_unix_ms,
                last_update_at_unix_ms, ended_at_unix_ms, end_reason,
                created_at_unix_ms, updated_at_unix_ms, mission_id,
                mission_run_id, current_visible_count, unique_confirmed_count
            ) VALUES (
                ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?12,
                ?13, ?14, ?15, ?16
            )
            ON CONFLICT(id) DO UPDATE SET
                last_update_at_unix_ms = max(perception_track_sessions.last_update_at_unix_ms, excluded.last_update_at_unix_ms),
                ended_at_unix_ms = COALESCE(excluded.ended_at_unix_ms, perception_track_sessions.ended_at_unix_ms),
                end_reason = CASE WHEN excluded.ended_at_unix_ms IS NOT NULL THEN excluded.end_reason ELSE perception_track_sessions.end_reason END,
                current_visible_count = excluded.current_visible_count,
                unique_confirmed_count = max(perception_track_sessions.unique_confirmed_count, excluded.unique_confirmed_count),
                updated_at_unix_ms = excluded.updated_at_unix_ms
            "#,
            params![
                batch.track_session_id,
                drone_id,
                communication_link_id,
                perception_stream_id,
                batch.source_id,
                batch.stream_epoch,
                batch.tracker_type,
                started_at,
                batch.observed_at_unix_ms,
                ended_at,
                batch.session_end_reason,
                received_at_unix_ms,
                mission_context.as_ref().map(|value| value.0.as_str()),
                mission_context.as_ref().map(|value| value.1.as_str()),
                counter_i64(batch.current_visible, "current visible track count")?,
                counter_i64(batch.unique_confirmed, "unique confirmed track count")?,
            ],
        )
        .map_err(|error| format!("upsert perception track session: {error}"))?;

        for track in &batch.tracks {
            validate_track(batch, track)?;
            let existing = tx
                .query_row(
                    "SELECT track_session_id, revision, lifecycle_state FROM perception_tracks WHERE id = ?1",
                    [&track.track_id],
                    |row| Ok((row.get::<_, String>(0)?, row.get::<_, i64>(1)?, row.get::<_, String>(2)?)),
                )
                .optional()
                .map_err(|error| format!("read existing perception track: {error}"))?;
            if let Some((existing_session, existing_revision, _)) = &existing {
                if existing_session != &track.track_session_id {
                    return Err("perception track id moved between sessions".to_string());
                }
                if *existing_revision >= counter_i64(track.revision, "track revision")? {
                    continue;
                }
            }
            let previous_state = existing.as_ref().map(|(_, _, state)| state.clone());
            let predicted = track.predicted_box.as_ref();
            tx.execute(
                r#"
                INSERT INTO perception_tracks (
                    id, track_session_id, drone_id, class_id, class_label,
                    tracker_type, lifecycle_state, revision, age_frames,
                    observation_count, first_observed_at_unix_ms,
                    last_observed_at_unix_ms, latest_box_x, latest_box_y,
                    latest_box_width, latest_box_height,
                    latest_detection_confidence, predicted_box_x,
                    predicted_box_y, predicted_box_width, predicted_box_height,
                    prediction_confidence, closed_at_unix_ms, closure_reason,
                    last_update_reason, received_at_unix_ms,
                    created_at_unix_ms, updated_at_unix_ms
                ) VALUES (
                    ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10,
                    ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18, ?19, ?20,
                    ?21, ?22, ?23, ?24, ?25, ?26, ?26, ?26
                )
                ON CONFLICT(id) DO UPDATE SET
                    lifecycle_state = excluded.lifecycle_state,
                    revision = excluded.revision,
                    age_frames = excluded.age_frames,
                    observation_count = excluded.observation_count,
                    last_observed_at_unix_ms = excluded.last_observed_at_unix_ms,
                    latest_box_x = excluded.latest_box_x,
                    latest_box_y = excluded.latest_box_y,
                    latest_box_width = excluded.latest_box_width,
                    latest_box_height = excluded.latest_box_height,
                    latest_detection_confidence = excluded.latest_detection_confidence,
                    predicted_box_x = excluded.predicted_box_x,
                    predicted_box_y = excluded.predicted_box_y,
                    predicted_box_width = excluded.predicted_box_width,
                    predicted_box_height = excluded.predicted_box_height,
                    prediction_confidence = excluded.prediction_confidence,
                    closed_at_unix_ms = excluded.closed_at_unix_ms,
                    closure_reason = excluded.closure_reason,
                    last_update_reason = excluded.last_update_reason,
                    received_at_unix_ms = excluded.received_at_unix_ms,
                    updated_at_unix_ms = excluded.updated_at_unix_ms
                WHERE excluded.revision > perception_tracks.revision
                "#,
                params![
                    track.track_id,
                    track.track_session_id,
                    drone_id,
                    track.class_id,
                    track.class_label,
                    track.tracker_type,
                    track.lifecycle_state,
                    counter_i64(track.revision, "track revision")?,
                    counter_i64(track.age_frames, "track age")?,
                    counter_i64(track.observation_count, "track observation count")?,
                    track.first_observed_at_unix_ms,
                    track.last_observed_at_unix_ms,
                    track.latest_confirmed_box.x,
                    track.latest_confirmed_box.y,
                    track.latest_confirmed_box.width,
                    track.latest_confirmed_box.height,
                    track.latest_detection_confidence,
                    predicted.map(|value| value.x),
                    predicted.map(|value| value.y),
                    predicted.map(|value| value.width),
                    predicted.map(|value| value.height),
                    track.prediction_confidence,
                    track.closed_at_unix_ms,
                    track.closure_reason,
                    track.update_reason,
                    received_at_unix_ms,
                ],
            )
            .map_err(|error| format!("upsert perception track: {error}"))?;

            reconcile_track_selection(
                &tx,
                &track.track_session_id,
                &track.track_id,
                &track.lifecycle_state,
                &track.closure_reason,
                batch.observed_at_unix_ms,
            )?;

            let sample_id = format!("{}:{}", track.track_id, track.revision);
            tx.execute(
                r#"
                INSERT OR IGNORE INTO perception_track_samples (
                    id, track_id, revision, sample_reason, lifecycle_state,
                    observed_at_unix_ms, box_x, box_y, box_width, box_height,
                    detection_confidence, predicted_box_x, predicted_box_y,
                    predicted_box_width, predicted_box_height,
                    prediction_confidence, received_at_unix_ms
                ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10,
                          ?11, ?12, ?13, ?14, ?15, ?16, ?17)
                "#,
                params![
                    sample_id,
                    track.track_id,
                    counter_i64(track.revision, "track revision")?,
                    track.update_reason,
                    track.lifecycle_state,
                    batch.observed_at_unix_ms,
                    track.latest_confirmed_box.x,
                    track.latest_confirmed_box.y,
                    track.latest_confirmed_box.width,
                    track.latest_confirmed_box.height,
                    track.latest_detection_confidence,
                    predicted.map(|value| value.x),
                    predicted.map(|value| value.y),
                    predicted.map(|value| value.width),
                    predicted.map(|value| value.height),
                    track.prediction_confidence,
                    received_at_unix_ms,
                ],
            )
            .map_err(|error| format!("insert perception track sample: {error}"))?;

            if track.update_reason != "PERIODIC"
                || previous_state.as_deref() != Some(track.lifecycle_state.as_str())
            {
                let event_id = format!("{}:{}", track.track_id, track.revision);
                tx.execute(
                    r#"
                    INSERT OR IGNORE INTO perception_track_events (
                        id, track_id, revision, event_type, previous_state,
                        lifecycle_state, reason, occurred_at_unix_ms,
                        received_at_unix_ms
                    ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)
                    "#,
                    params![
                        event_id,
                        track.track_id,
                        counter_i64(track.revision, "track revision")?,
                        track.update_reason,
                        previous_state,
                        track.lifecycle_state,
                        if track.update_reason == "CLOSED" {
                            track.closure_reason.as_str()
                        } else {
                            track.update_reason.as_str()
                        },
                        batch.observed_at_unix_ms,
                        received_at_unix_ms,
                    ],
                )
                .map_err(|error| format!("insert perception track event: {error}"))?;
            }
            let is_confirmed_track = matches!(
                track.lifecycle_state.as_str(),
                "ACTIVE" | "TEMPORARILY_OCCLUDED" | "LOST"
            ) || (track.lifecycle_state == "CLOSED"
                && previous_state
                    .as_deref()
                    .is_some_and(|state| state != "TENTATIVE"));
            if is_confirmed_track {
                if let Some((mission_id, mission_run_id)) = &mission_context {
                    tx.execute(
                        r#"
                        INSERT INTO perception_mission_tracks (
                            mission_run_id, mission_id, track_session_id, track_id,
                            first_observed_at_unix_ms, last_observed_at_unix_ms
                        ) VALUES (?1, ?2, ?3, ?4, ?5, ?6)
                        ON CONFLICT(mission_run_id, track_id) DO UPDATE SET
                            first_observed_at_unix_ms = min(first_observed_at_unix_ms, excluded.first_observed_at_unix_ms),
                            last_observed_at_unix_ms = max(last_observed_at_unix_ms, excluded.last_observed_at_unix_ms)
                        "#,
                        params![
                            mission_run_id,
                            mission_id,
                            track.track_session_id,
                            track.track_id,
                            track.first_observed_at_unix_ms,
                            track.last_observed_at_unix_ms,
                        ],
                    )
                    .map_err(|error| {
                        format!("associate perception track with mission: {error}")
                    })?;
                }
            }
        }

        for count in &batch.rule_counts {
            validate_rule_count(count)?;
            validate_counting_rule_identity(
                &tx,
                drone_id,
                &batch.source_id,
                &count.rule_id,
                &count.rule_type,
            )?;
            tx.execute(
                r#"
                INSERT INTO perception_track_rule_counts (
                    track_session_id, counting_rule_id, rule_revision,
                    rule_type, line_forward, line_reverse, polygon_entries,
                    polygon_exits, updated_at_unix_ms
                ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)
                ON CONFLICT(track_session_id, counting_rule_id, rule_revision)
                DO UPDATE SET
                    line_forward = max(line_forward, excluded.line_forward),
                    line_reverse = max(line_reverse, excluded.line_reverse),
                    polygon_entries = max(polygon_entries, excluded.polygon_entries),
                    polygon_exits = max(polygon_exits, excluded.polygon_exits),
                    updated_at_unix_ms = excluded.updated_at_unix_ms
                "#,
                params![
                    batch.track_session_id,
                    count.rule_id,
                    counter_i64(count.rule_revision, "count rule revision")?,
                    count.rule_type,
                    counter_i64(count.line_forward, "line forward count")?,
                    counter_i64(count.line_reverse, "line reverse count")?,
                    counter_i64(count.polygon_entries, "polygon entry count")?,
                    counter_i64(count.polygon_exits, "polygon exit count")?,
                    received_at_unix_ms,
                ],
            )
            .map_err(|error| format!("upsert perception track rule count: {error}"))?;
        }

        for event in &batch.count_events {
            validate_count_event(batch, event)?;
            let rule_type = match event.event_type.as_str() {
                "LINE_FORWARD" | "LINE_REVERSE" => "LINE",
                "POLYGON_ENTRY" | "POLYGON_EXIT" => "POLYGON",
                _ => return Err("perception count event type is invalid".into()),
            };
            validate_counting_rule_identity(
                &tx,
                drone_id,
                &batch.source_id,
                &event.rule_id,
                rule_type,
            )?;
            tx.execute(
                r#"
                INSERT OR IGNORE INTO perception_count_events (
                    id, counting_rule_id, rule_revision, track_session_id,
                    track_id, mission_id, mission_run_id, event_type,
                    observed_at_unix_ms, anchor_x, anchor_y,
                    received_at_unix_ms
                ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)
                "#,
                params![
                    event.event_id,
                    event.rule_id,
                    counter_i64(event.rule_revision, "count event rule revision")?,
                    event.track_session_id,
                    event.track_id,
                    mission_context.as_ref().map(|value| value.0.as_str()),
                    mission_context.as_ref().map(|value| value.1.as_str()),
                    event.event_type,
                    event.observed_at_unix_ms,
                    event.anchor_x,
                    event.anchor_y,
                    received_at_unix_ms,
                ],
            )
            .map_err(|error| format!("insert perception count event: {error}"))?;
        }

        tx.commit()
            .map_err(|error| format!("commit perception track update: {error}"))
    }
}

fn validate_batch(batch: &PerceptionTrackBatchInput) -> Result<(), String> {
    if batch.source_id.trim().is_empty()
        || batch.stream_epoch.trim().is_empty()
        || batch.track_session_id.trim().is_empty()
        || !valid_tracker(&batch.tracker_type)
        || batch.observed_at_unix_ms <= 0
    {
        return Err("perception track batch identity, tracker, and time are required".into());
    }
    if batch.session_ended != !batch.session_end_reason.trim().is_empty() {
        return Err("ended perception track sessions require exactly one end reason".into());
    }
    if batch.tracks.len() > 1_000 {
        return Err("perception track batch exceeds 1000 updates".into());
    }
    if batch.rule_counts.len() > 64
        || batch.count_events.len() > 1_000
        || batch.current_visible > batch.unique_confirmed
    {
        return Err("perception track count batch is invalid".into());
    }
    Ok(())
}

fn reconcile_superseded_track_sessions(
    tx: &rusqlite::Transaction<'_>,
    drone_id: &str,
    source_id: &str,
    current_session_id: &str,
    observed_at_unix_ms: i64,
    received_at_unix_ms: i64,
) -> Result<(), String> {
    tx.execute(
        r#"
        INSERT OR IGNORE INTO perception_track_events (
            id, track_id, revision, event_type, previous_state,
            lifecycle_state, reason, occurred_at_unix_ms, received_at_unix_ms
        )
        SELECT tracks.id || ':' || CAST(tracks.revision + 1 AS TEXT),
               tracks.id, tracks.revision + 1, 'CLOSED', tracks.lifecycle_state,
               'CLOSED', 'SESSION_SUPERSEDED',
               max(tracks.last_observed_at_unix_ms, ?4), ?5
        FROM perception_tracks tracks
        JOIN perception_track_sessions sessions ON sessions.id = tracks.track_session_id
        WHERE sessions.drone_id = ?1 AND sessions.source_id = ?2
          AND sessions.id <> ?3 AND sessions.ended_at_unix_ms IS NULL
          AND tracks.lifecycle_state <> 'CLOSED'
        "#,
        params![
            drone_id,
            source_id,
            current_session_id,
            observed_at_unix_ms,
            received_at_unix_ms,
        ],
    )
    .map_err(|error| format!("record superseded perception track closure events: {error}"))?;
    tx.execute(
        r#"
        INSERT OR IGNORE INTO perception_track_samples (
            id, track_id, revision, sample_reason, lifecycle_state,
            observed_at_unix_ms, box_x, box_y, box_width, box_height,
            detection_confidence, predicted_box_x, predicted_box_y,
            predicted_box_width, predicted_box_height,
            prediction_confidence, received_at_unix_ms
        )
        SELECT tracks.id || ':' || CAST(tracks.revision + 1 AS TEXT),
               tracks.id, tracks.revision + 1, 'CLOSED', 'CLOSED',
               max(tracks.last_observed_at_unix_ms, ?4),
               tracks.latest_box_x, tracks.latest_box_y,
               tracks.latest_box_width, tracks.latest_box_height,
               tracks.latest_detection_confidence, NULL, NULL, NULL, NULL, 0, ?5
        FROM perception_tracks tracks
        JOIN perception_track_sessions sessions ON sessions.id = tracks.track_session_id
        WHERE sessions.drone_id = ?1 AND sessions.source_id = ?2
          AND sessions.id <> ?3 AND sessions.ended_at_unix_ms IS NULL
          AND tracks.lifecycle_state <> 'CLOSED'
        "#,
        params![
            drone_id,
            source_id,
            current_session_id,
            observed_at_unix_ms,
            received_at_unix_ms,
        ],
    )
    .map_err(|error| format!("record superseded perception track samples: {error}"))?;
    tx.execute(
        r#"
        UPDATE perception_tracks
        SET lifecycle_state = 'CLOSED', revision = revision + 1,
            age_frames = age_frames, predicted_box_x = NULL,
            predicted_box_y = NULL, predicted_box_width = NULL,
            predicted_box_height = NULL, prediction_confidence = 0,
            closed_at_unix_ms = max(last_observed_at_unix_ms, ?4),
            closure_reason = 'SESSION_SUPERSEDED',
            last_update_reason = 'CLOSED', received_at_unix_ms = ?5,
            updated_at_unix_ms = ?5
        WHERE track_session_id IN (
            SELECT id FROM perception_track_sessions
            WHERE drone_id = ?1 AND source_id = ?2 AND id <> ?3
              AND ended_at_unix_ms IS NULL
        ) AND lifecycle_state <> 'CLOSED'
        "#,
        params![
            drone_id,
            source_id,
            current_session_id,
            observed_at_unix_ms,
            received_at_unix_ms,
        ],
    )
    .map_err(|error| format!("close superseded perception tracks: {error}"))?;
    tx.execute(
        r#"
        UPDATE perception_track_sessions
        SET last_update_at_unix_ms = max(last_update_at_unix_ms, ?4),
            ended_at_unix_ms = max(
                started_at_unix_ms,
                last_update_at_unix_ms,
                ?4
            ),
            end_reason = 'SESSION_SUPERSEDED',
            current_visible_count = 0,
            updated_at_unix_ms = ?5
        WHERE drone_id = ?1 AND source_id = ?2 AND id <> ?3
          AND ended_at_unix_ms IS NULL
        "#,
        params![
            drone_id,
            source_id,
            current_session_id,
            observed_at_unix_ms,
            received_at_unix_ms,
        ],
    )
    .map_err(|error| format!("close superseded perception track sessions: {error}"))?;
    let selections = {
        let mut statement = tx
            .prepare(
                r#"
                SELECT selections.track_session_id, selections.track_id,
                       tracks.closed_at_unix_ms
                FROM perception_track_selections selections
                JOIN perception_tracks tracks ON tracks.id = selections.track_id
                WHERE selections.status IN ('SELECTED', 'OCCLUDED')
                  AND tracks.closure_reason = 'SESSION_SUPERSEDED'
                  AND tracks.track_session_id <> ?1
                "#,
            )
            .map_err(|error| format!("prepare superseded track selections: {error}"))?;
        let records = statement
            .query_map([current_session_id], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, i64>(2)?,
                ))
            })
            .map_err(|error| format!("query superseded track selections: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read superseded track selections: {error}"))?;
        records
    };
    for (track_session_id, track_id, closed_at) in selections {
        reconcile_track_selection(
            tx,
            &track_session_id,
            &track_id,
            "CLOSED",
            "SESSION_SUPERSEDED",
            closed_at,
        )?;
    }
    Ok(())
}

fn validate_track(
    batch: &PerceptionTrackBatchInput,
    track: &PerceptionTrackUpdateInput,
) -> Result<(), String> {
    if track.track_id.trim().is_empty()
        || track.track_session_id != batch.track_session_id
        || track.tracker_type != batch.tracker_type
        || track.class_label.trim().is_empty()
        || !matches!(
            track.lifecycle_state.as_str(),
            "TENTATIVE" | "ACTIVE" | "TEMPORARILY_OCCLUDED" | "LOST" | "CLOSED"
        )
        || !matches!(
            track.update_reason.as_str(),
            "CREATED" | "STATE_CHANGED" | "REACQUIRED" | "PERIODIC" | "CLOSED"
        )
        || track.revision == 0
        || track.age_frames == 0
        || track.observation_count == 0
        || track.age_frames < track.observation_count
        || track.first_observed_at_unix_ms <= 0
        || track.last_observed_at_unix_ms < track.first_observed_at_unix_ms
        || !valid_box(&track.latest_confirmed_box)
        || !finite_unit(track.latest_detection_confidence)
        || !finite_unit(track.prediction_confidence)
    {
        return Err("perception track update is invalid".into());
    }
    if track
        .predicted_box
        .as_ref()
        .is_some_and(|value| !valid_box(value))
        || (track.predicted_box.is_none() && track.prediction_confidence != 0.0)
    {
        return Err("perception track prediction is invalid".into());
    }
    let closed = track.lifecycle_state == "CLOSED";
    if closed != track.closed_at_unix_ms.is_some()
        || closed != !track.closure_reason.trim().is_empty()
        || (closed && track.update_reason != "CLOSED")
        || track
            .closed_at_unix_ms
            .is_some_and(|value| value < track.last_observed_at_unix_ms)
    {
        return Err("perception track closure is invalid".into());
    }
    Ok(())
}

fn validate_rule_count(count: &TrackRuleCountInput) -> Result<(), String> {
    if count.rule_id.trim().is_empty()
        || count.rule_revision == 0
        || !matches!(count.rule_type.as_str(), "LINE" | "POLYGON")
        || (count.rule_type == "LINE" && (count.polygon_entries != 0 || count.polygon_exits != 0))
        || (count.rule_type == "POLYGON" && (count.line_forward != 0 || count.line_reverse != 0))
    {
        return Err("perception track rule count is invalid".into());
    }
    Ok(())
}

fn validate_count_event(
    batch: &PerceptionTrackBatchInput,
    event: &TrackCountEventInput,
) -> Result<(), String> {
    if event.event_id.trim().is_empty()
        || event.rule_id.trim().is_empty()
        || event.rule_revision == 0
        || event.track_session_id != batch.track_session_id
        || !event
            .track_id
            .starts_with(&format!("atlas:{}:", batch.track_session_id))
        || event.observed_at_unix_ms <= 0
        || event.observed_at_unix_ms > batch.observed_at_unix_ms
        || !finite_unit(event.anchor_x)
        || !finite_unit(event.anchor_y)
    {
        return Err("perception count event is invalid".into());
    }
    Ok(())
}

fn validate_counting_rule_identity(
    tx: &rusqlite::Transaction<'_>,
    drone_id: &str,
    source_id: &str,
    rule_id: &str,
    rule_type: &str,
) -> Result<(), String> {
    let matches = tx
        .query_row(
            r#"
            SELECT 1 FROM perception_counting_rules
            WHERE id = ?1 AND drone_id = ?2 AND source_id = ?3
              AND rule_type = ?4
            "#,
            params![rule_id, drone_id, source_id, rule_type],
            |_| Ok(()),
        )
        .optional()
        .map_err(|error| format!("validate perception counting rule: {error}"))?
        .is_some();
    if !matches {
        return Err("perception count update references an unknown counting rule".into());
    }
    Ok(())
}

fn valid_tracker(value: &str) -> bool {
    matches!(value, "BYTE_TRACK" | "BYTE_TRACK_CMC")
}

fn valid_box(value: &TrackBoundingBoxInput) -> bool {
    finite_unit(value.x)
        && finite_unit(value.y)
        && finite_unit(value.width)
        && finite_unit(value.height)
        && value.x + value.width <= 1.0 + 1e-9
        && value.y + value.height <= 1.0 + 1e-9
}

fn finite_unit(value: f64) -> bool {
    value.is_finite() && (0.0..=1.0).contains(&value)
}

fn counter_i64(value: u64, field: &str) -> Result<i64, String> {
    i64::try_from(value).map_err(|_| format!("{field} exceeds SQLite integer range"))
}
