use std::{
    collections::HashSet,
    env,
    ffi::CString,
    fs::{self, File},
    io::{BufRead, BufReader, Read, Write},
    os::unix::ffi::OsStrExt,
    path::{Component, Path, PathBuf},
    process::{Child, Command, Stdio},
    sync::{Arc, Mutex},
    thread,
    time::Duration,
};

use serde::Serialize;
use serde_json::json;
use sha2::{Digest, Sha256};

use crate::{
    database::{
        unix_time_ms, AlertObservation, CaptureEvidenceStillInput, CreateEvidenceRecordingInput,
        CreateEvidenceStillAssetInput, EvidenceAssetFileInput, EvidenceAssetSnapshot,
        EvidenceRecordingSessionSnapshot, LocalDatabase, QueueEvidenceEventClipInput,
        RestoreEvidenceAssetInput, SegmentFinalizationInput, StartEvidenceRecordingInput,
        TrashEvidenceAssetInput,
    },
    video::{CapturedVideoFrame, VideoSourceConfig},
};

const DEFAULT_SEGMENT_SECONDS: u64 = 30;
const DEFAULT_WARNING_FREE_BYTES: u64 = 5 * 1024 * 1024 * 1024;
const DEFAULT_STOP_FREE_BYTES: u64 = 2 * 1024 * 1024 * 1024;
const GRACEFUL_STOP_TIMEOUT_MS: i64 = 5_000;
const TIMELINE_GAP_TOLERANCE_MS: i64 = 1_000;
const MIN_EVENT_CLIP_DURATION_MS: i64 = 100;

#[derive(Clone)]
pub(crate) struct EvidenceRecorder {
    config: Arc<EvidenceRecorderConfig>,
    database: Arc<LocalDatabase>,
    shared: Arc<RecorderShared>,
}

struct RecorderShared {
    state: Mutex<RecorderState>,
}

struct RecorderState {
    generation: u64,
    active_session_id: Option<String>,
    child: Option<Arc<Mutex<Option<Child>>>>,
    stop_requested_at_unix_ms: Option<i64>,
    last_process_message: Option<String>,
}

#[derive(Clone)]
struct EvidenceRecorderConfig {
    evidence_root: PathBuf,
    source: VideoSourceConfig,
    segment_duration_seconds: u64,
    warning_free_bytes: u64,
    stop_free_bytes: u64,
    available_bytes_override: Option<u64>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct EvidenceRecordingStatusSnapshot {
    pub configured: bool,
    pub source_id: String,
    pub evidence_root: String,
    pub segment_duration_seconds: u64,
    pub available_bytes: Option<u64>,
    pub warning_free_bytes: u64,
    pub stop_free_bytes: u64,
    pub disk_state: String,
    pub session: Option<EvidenceRecordingSessionSnapshot>,
}

#[derive(Debug, Clone)]
struct SegmentListEntry {
    sequence: u64,
    filename: String,
    start_seconds: f64,
    end_seconds: f64,
}

impl EvidenceRecorder {
    pub(crate) fn from_environment(
        database: Arc<LocalDatabase>,
        source: VideoSourceConfig,
    ) -> Result<Self, String> {
        let default_root = database
            .path
            .parent()
            .unwrap_or_else(|| Path::new("."))
            .join("evidence");
        let evidence_root = match env::var_os("ATLAS_EVIDENCE_ROOT") {
            Some(raw) if !raw.is_empty() => {
                let path = PathBuf::from(raw);
                if !path.is_absolute() {
                    return Err("ATLAS_EVIDENCE_ROOT must be an absolute path".into());
                }
                path
            }
            _ => default_root,
        };
        let segment_duration_seconds = environment_u64(
            "ATLAS_EVIDENCE_SEGMENT_SECONDS",
            DEFAULT_SEGMENT_SECONDS,
            2,
            600,
        )?;
        let warning_free_bytes = environment_u64(
            "ATLAS_EVIDENCE_WARNING_FREE_BYTES",
            DEFAULT_WARNING_FREE_BYTES,
            0,
            i64::MAX as u64,
        )?;
        let stop_free_bytes = environment_u64(
            "ATLAS_EVIDENCE_STOP_FREE_BYTES",
            DEFAULT_STOP_FREE_BYTES,
            0,
            i64::MAX as u64,
        )?;
        if warning_free_bytes < stop_free_bytes {
            return Err(
                "ATLAS_EVIDENCE_WARNING_FREE_BYTES cannot be below ATLAS_EVIDENCE_STOP_FREE_BYTES"
                    .into(),
            );
        }
        ensure_evidence_root(&evidence_root)?;
        let recorder = Self {
            config: Arc::new(EvidenceRecorderConfig {
                evidence_root,
                source,
                segment_duration_seconds,
                warning_free_bytes,
                stop_free_bytes,
                available_bytes_override: None,
            }),
            database,
            shared: Arc::new(RecorderShared {
                state: Mutex::new(RecorderState {
                    generation: 0,
                    active_session_id: None,
                    child: None,
                    stop_requested_at_unix_ms: None,
                    last_process_message: None,
                }),
            }),
        };
        recorder.recover_interrupted_purges()?;
        recorder.recover_pending_evidence_assets()?;
        recorder.recover_interrupted_recordings()?;
        recorder.finalize_pending_event_clips(None)?;
        recorder.apply_retention_policy()?;
        Ok(recorder)
    }

    pub(crate) fn start(
        &self,
        input: StartEvidenceRecordingInput,
    ) -> Result<EvidenceRecordingSessionSnapshot, String> {
        {
            let state = self.lock_state()?;
            if let Some(session_id) = state.active_session_id.as_deref() {
                return Err(format!(
                    "video source {} is already recording in session {session_id}",
                    self.config.source.source_id
                ));
            }
        }
        let now = unix_time_ms();
        let create = CreateEvidenceRecordingInput {
            source_id: self.config.source.source_id.clone(),
            evidence_root: self.config.evidence_root.to_string_lossy().into_owned(),
            drone_id: input.drone_id,
            incident_id: input.incident_id,
            mission_id: input.mission_id,
            mission_run_id: input.mission_run_id,
            segment_duration_seconds: self.config.segment_duration_seconds,
            warning_free_bytes: self.config.warning_free_bytes,
            stop_free_bytes: self.config.stop_free_bytes,
            requested_at_unix_ms: now,
        };
        let session = self.database.create_evidence_recording(&create)?;
        let available = match self.available_bytes() {
            Ok(bytes) => bytes,
            Err(error) => {
                self.fail_before_spawn(&session, "DISK_CHECK_FAILED", &error, now)?;
                return self.database.evidence_recording_session(&session.id);
            }
        };
        self.reconcile_storage_alert(&session, available, now)?;
        if available <= self.config.stop_free_bytes {
            let message = format!(
                "Evidence recording refused because only {} bytes are available",
                available
            );
            self.record_gap_and_fail(
                &session,
                "LOW_DISK_SPACE",
                "LOW_DISK_SPACE",
                &message,
                now,
                Some(now),
            )?;
            return self.database.evidence_recording_session(&session.id);
        }

        let temporary_directory = self.temporary_session_directory(&session.id);
        let object_directory = self.object_session_directory(&session.id);
        if let Err(error) = fs::create_dir_all(&temporary_directory) {
            let message = format!("create evidence temporary session directory: {error}");
            self.fail_before_spawn(&session, "RECORDER_SETUP_FAILED", &message, now)?;
            return self.database.evidence_recording_session(&session.id);
        }
        if let Err(error) = fs::create_dir_all(&object_directory) {
            let message = format!("create evidence object session directory: {error}");
            self.fail_before_spawn(&session, "RECORDER_SETUP_FAILED", &message, now)?;
            return self.database.evidence_recording_session(&session.id);
        }

        let mut child = match self.recorder_command(&session.id).spawn() {
            Ok(child) => child,
            Err(error) => {
                let message = format!(
                    "start source RTSP recorder {}: {error}",
                    self.config.source.decoder_path
                );
                self.fail_before_spawn(&session, "RECORDER_START_FAILED", &message, now)?;
                return self.database.evidence_recording_session(&session.id);
            }
        };
        let stderr = child.stderr.take();
        let child = Arc::new(Mutex::new(Some(child)));
        let generation = {
            let mut state = self.lock_state()?;
            state.generation = state.generation.wrapping_add(1);
            state.active_session_id = Some(session.id.clone());
            state.child = Some(Arc::clone(&child));
            state.stop_requested_at_unix_ms = None;
            state.last_process_message = None;
            state.generation
        };
        if let Some(stderr) = stderr {
            let shared = Arc::clone(&self.shared);
            if let Err(error) = thread::Builder::new()
                .name("atlas-evidence-recorder-errors".into())
                .spawn(move || read_recorder_errors(shared, generation, stderr))
            {
                let message = format!("start evidence recorder error reader: {error}");
                self.fail_after_process_spawn(
                    generation,
                    &session,
                    &child,
                    "RECORDER_MONITOR_START_FAILED",
                    &message,
                    now,
                )?;
                return self.database.evidence_recording_session(&session.id);
            }
        }
        let recorder = self.clone();
        let session_id = session.id.clone();
        let monitor_child = Arc::clone(&child);
        if let Err(error) = thread::Builder::new()
            .name("atlas-evidence-recorder".into())
            .spawn(move || recorder.run_recording(generation, &session_id, monitor_child))
        {
            let message = format!("start evidence recorder monitor: {error}");
            self.fail_after_process_spawn(
                generation,
                &session,
                &child,
                "RECORDER_MONITOR_START_FAILED",
                &message,
                now,
            )?;
            return self.database.evidence_recording_session(&session.id);
        }
        Ok(session)
    }

    pub(crate) fn stop(
        &self,
        session_id: &str,
    ) -> Result<EvidenceRecordingSessionSnapshot, String> {
        let now = unix_time_ms();
        let child = {
            let mut state = self.lock_state()?;
            if state.active_session_id.as_deref() != Some(session_id) {
                return Err(format!(
                    "evidence recording {session_id} is not owned by the active local recorder"
                ));
            }
            if state.stop_requested_at_unix_ms.is_none() {
                state.stop_requested_at_unix_ms = Some(now);
            }
            state.child.clone()
        };
        self.database
            .request_evidence_recording_stop(session_id, now)?;
        if let Some(child) = child {
            if let Ok(mut child) = child.lock() {
                if let Some(child) = child.as_mut() {
                    if let Some(stdin) = child.stdin.as_mut() {
                        let _ = stdin.write_all(b"q\n");
                        let _ = stdin.flush();
                    }
                }
            }
        }
        self.database.evidence_recording_session(session_id)
    }

    pub(crate) fn status(
        &self,
        drone_id: Option<&str>,
        incident_id: Option<&str>,
        mission_id: Option<&str>,
        mission_run_id: Option<&str>,
    ) -> Result<EvidenceRecordingStatusSnapshot, String> {
        let active_session_id = self.lock_state()?.active_session_id.clone();
        let session = if let Some(session_id) = active_session_id {
            Some(self.database.evidence_recording_session(&session_id)?)
        } else {
            self.database.evidence_recording_for_context(
                drone_id,
                incident_id,
                mission_id,
                mission_run_id,
            )?
        };
        let available_bytes = self.available_bytes().ok();
        let disk_state = available_bytes
            .map(|available| self.disk_state(available).to_string())
            .unwrap_or_else(|| "UNKNOWN".into());
        Ok(EvidenceRecordingStatusSnapshot {
            configured: true,
            source_id: self.config.source.source_id.clone(),
            evidence_root: self.config.evidence_root.to_string_lossy().into_owned(),
            segment_duration_seconds: self.config.segment_duration_seconds,
            available_bytes,
            warning_free_bytes: self.config.warning_free_bytes,
            stop_free_bytes: self.config.stop_free_bytes,
            disk_state,
            session,
        })
    }

    pub(crate) fn capture_still(
        &self,
        input: CaptureEvidenceStillInput,
        frame: CapturedVideoFrame,
    ) -> Result<EvidenceAssetSnapshot, String> {
        if input.drone_id.trim().is_empty() || frame.jpeg.is_empty() {
            return Err(
                "still capture requires an aircraft identity and decoded JPEG frame".into(),
            );
        }
        if self.available_bytes()? <= self.config.stop_free_bytes {
            return Err(
                "still capture is blocked at the configured evidence storage reserve".into(),
            );
        }
        let asset = self
            .database
            .create_evidence_still_asset(&CreateEvidenceStillAssetInput {
                source_id: frame.source_id.clone(),
                drone_id: input.drone_id,
                incident_id: input.incident_id,
                mission_id: input.mission_id,
                mission_run_id: input.mission_run_id,
                selection_id: input.selection_id,
                captured_at_unix_ms: frame.observed_at_unix_ms,
                actor: input.actor,
            })?;
        let result = self.publish_still(&asset, &frame);
        match result {
            Ok(files) => {
                match self
                    .database
                    .complete_evidence_asset(&asset.id, &files, unix_time_ms())
                {
                    Ok(ready) => Ok(ready),
                    Err(error) => {
                        let _ = fs::remove_dir_all(
                            self.config.evidence_root.join("assets").join(&asset.id),
                        );
                        let _ =
                            self.database
                                .fail_evidence_asset(&asset.id, &error, unix_time_ms());
                        Err(error)
                    }
                }
            }
            Err(error) => {
                let _ = self
                    .database
                    .fail_evidence_asset(&asset.id, &error, unix_time_ms());
                let _ = fs::remove_dir_all(self.temporary_asset_directory(&asset.id));
                Err(error)
            }
        }
    }

    pub(crate) fn queue_event_clip(
        &self,
        input: QueueEvidenceEventClipInput,
    ) -> Result<EvidenceAssetSnapshot, String> {
        let asset = self
            .database
            .queue_evidence_event_clip(&input, unix_time_ms())?;
        if let Some(session_id) = asset.recording_session_id.as_deref() {
            let _ = self.finalize_pending_event_clips(Some(session_id));
        }
        self.database.evidence_asset(&asset.id)
    }

    pub(crate) fn evidence_asset_bytes(
        &self,
        asset_id: &str,
        thumbnail: bool,
    ) -> Result<Vec<u8>, String> {
        let asset = self.database.evidence_asset(asset_id)?;
        if !matches!(asset.status.as_str(), "READY" | "TRASHED") {
            return Err(format!(
                "evidence media is unavailable while the asset is {}",
                asset.status
            ));
        }
        let relative = if thumbnail {
            &asset.thumbnail_relative_path
        } else {
            &asset.relative_path
        };
        let path = self.safe_evidence_path(relative)?;
        fs::read(&path).map_err(|error| format!("read evidence media {}: {error}", path.display()))
    }

    pub(crate) fn trash_asset(
        &self,
        input: &TrashEvidenceAssetInput,
    ) -> Result<EvidenceAssetSnapshot, String> {
        self.trash_asset_at(input, unix_time_ms())
    }

    pub(crate) fn restore_asset(
        &self,
        input: &RestoreEvidenceAssetInput,
    ) -> Result<EvidenceAssetSnapshot, String> {
        let asset = self.database.evidence_asset(&input.asset_id)?;
        if asset.status != "TRASHED" {
            return Err("only trashed evidence can be restored".into());
        }
        validate_asset_id(&asset.id)?;
        let source_directory = self.config.evidence_root.join("trash").join(&asset.id);
        let destination_directory = self.config.evidence_root.join("assets").join(&asset.id);
        if !source_directory.is_dir() {
            return Err("trashed evidence bytes are missing and cannot be restored".into());
        }
        if destination_directory.exists() {
            return Err("evidence restore destination already exists".into());
        }
        fs::rename(&source_directory, &destination_directory)
            .map_err(|error| format!("restore evidence media: {error}"))?;
        let relative_path = asset_relative_path("assets", &asset.id, &asset.relative_path)?;
        let thumbnail_relative_path =
            asset_relative_path("assets", &asset.id, &asset.thumbnail_relative_path)?;
        match self.database.mark_evidence_asset_restored(
            &asset.id,
            &relative_path,
            &thumbnail_relative_path,
            &input.actor,
            unix_time_ms(),
        ) {
            Ok(restored) => Ok(restored),
            Err(error) => {
                let _ = fs::rename(&destination_directory, &source_directory);
                Err(error)
            }
        }
    }

    pub(crate) fn apply_retention_policy(&self) -> Result<(), String> {
        let policy = self.database.evidence_retention_policy()?;
        if !policy.enabled {
            return Ok(());
        }
        let now = unix_time_ms();
        let mut failures = Vec::new();
        for asset in self.database.retention_trash_candidates(now)? {
            if let Err(error) = self.trash_asset_at(
                &TrashEvidenceAssetInput {
                    asset_id: asset.id,
                    reason: "Configured retention period elapsed".into(),
                    actor: "atlas_retention_policy".into(),
                },
                now,
            ) {
                failures.push(error);
            }
        }
        for asset in self.database.retention_purge_candidates(now)? {
            validate_asset_id(&asset.id)?;
            if let Err(error) = self.database.begin_evidence_asset_purge(&asset.id, now) {
                failures.push(error);
                continue;
            }
            let directory = self.config.evidence_root.join("trash").join(&asset.id);
            if directory.exists() {
                if let Err(error) = fs::remove_dir_all(&directory) {
                    let _ = self.database.cancel_evidence_asset_purge(&asset.id, now);
                    failures.push(format!("purge expired evidence media: {error}"));
                    continue;
                }
            }
            if let Err(error) = self.database.complete_evidence_asset_purge(
                &asset.id,
                "atlas_retention_policy",
                now,
            ) {
                failures.push(error);
            }
        }
        if !failures.is_empty() {
            return Err(failures.join("; "));
        }
        Ok(())
    }

    pub(crate) fn shutdown(&self) {
        let active = self
            .lock_state()
            .ok()
            .and_then(|state| state.active_session_id.clone());
        if let Some(session_id) = active {
            let _ = self.stop(&session_id);
            for _ in 0..30 {
                if self
                    .database
                    .evidence_recording_session(&session_id)
                    .is_ok_and(|session| matches!(session.status.as_str(), "SUCCEEDED" | "FAILED"))
                {
                    return;
                }
                thread::sleep(Duration::from_millis(200));
            }
            if let Ok(state) = self.lock_state() {
                if let Some(child) = state.child.as_ref() {
                    kill_process(child);
                }
            }
        }
    }

    fn run_recording(&self, generation: u64, session_id: &str, child: Arc<Mutex<Option<Child>>>) {
        let mut running_confirmed = false;
        let mut processed_manifest_sequences = HashSet::new();
        let mut terminal_failure: Option<(String, String, String)> = None;
        let mut last_disk_check = 0;

        loop {
            let now = unix_time_ms();
            if !running_confirmed && self.temporary_capture_started(session_id) {
                if let Err(error) = self
                    .database
                    .mark_evidence_recording_running(session_id, now)
                {
                    terminal_failure = Some((
                        "STATE_TRANSITION_FAILED".into(),
                        error,
                        "recorder_state_failure".into(),
                    ));
                    request_process_stop(&child);
                } else {
                    running_confirmed = true;
                }
            }

            if let Err(error) =
                self.finalize_manifest_segments(session_id, &mut processed_manifest_sequences)
            {
                if terminal_failure.is_none() {
                    terminal_failure = Some((
                        "SEGMENT_FINALIZATION_FAILED".into(),
                        error,
                        "segment_finalization_failed".into(),
                    ));
                    request_process_stop(&child);
                }
            }

            if now.saturating_sub(last_disk_check) >= 1_000 {
                last_disk_check = now;
                match self.available_bytes() {
                    Ok(available) => {
                        if let Ok(session) = self.database.evidence_recording_session(session_id) {
                            let _ = self.reconcile_storage_alert(&session, available, now);
                        }
                        if available <= self.config.stop_free_bytes && terminal_failure.is_none() {
                            terminal_failure = Some((
                                "LOW_DISK_SPACE".into(),
                                format!(
                                    "Evidence recording stopped safely at the configured reserve; {available} bytes remain"
                                ),
                                "low_disk_space".into(),
                            ));
                            request_process_stop(&child);
                        }
                    }
                    Err(error) if terminal_failure.is_none() => {
                        terminal_failure = Some((
                            "DISK_CHECK_FAILED".into(),
                            error,
                            "disk_check_failed".into(),
                        ));
                        request_process_stop(&child);
                    }
                    Err(_) => {}
                }
            }

            let stop_requested_at = self
                .lock_state()
                .ok()
                .filter(|state| state.generation == generation)
                .and_then(|state| state.stop_requested_at_unix_ms);
            if stop_requested_at
                .is_some_and(|requested| now.saturating_sub(requested) > GRACEFUL_STOP_TIMEOUT_MS)
            {
                kill_process(&child);
            }

            let exit = child
                .lock()
                .ok()
                .and_then(|mut child| child.as_mut().and_then(|child| child.try_wait().ok()))
                .flatten();
            if let Some(exit) = exit {
                if let Ok(mut child) = child.lock() {
                    if let Some(mut owned) = child.take() {
                        let _ = owned.wait();
                    }
                }
                if let Err(error) =
                    self.finalize_manifest_segments(session_id, &mut processed_manifest_sequences)
                {
                    if terminal_failure.is_none() {
                        terminal_failure = Some((
                            "SEGMENT_FINALIZATION_FAILED".into(),
                            error,
                            "segment_finalization_failed".into(),
                        ));
                    }
                }
                let now = unix_time_ms();
                if let Some((code, message, reason)) = terminal_failure.take() {
                    let session = self.database.evidence_recording_session(session_id).ok();
                    if let Some(session) = session.as_ref() {
                        let gap_start = session
                            .last_segment_ended_at_unix_ms
                            .or(session.started_at_unix_ms)
                            .unwrap_or(session.requested_at_unix_ms);
                        let _ = self.record_gap_and_fail(
                            session,
                            &reason.to_uppercase(),
                            &code,
                            &message,
                            gap_start,
                            Some(now),
                        );
                    }
                } else if stop_requested_at.is_some() && running_confirmed {
                    let _ =
                        self.database
                            .succeed_evidence_recording(session_id, "operator_stop", now);
                } else {
                    let message = self
                        .lock_state()
                        .ok()
                        .and_then(|state| state.last_process_message.clone())
                        .unwrap_or_else(|| format!("source RTSP recorder exited with {exit}"));
                    if let Ok(session) = self.database.evidence_recording_session(session_id) {
                        let gap_start = session
                            .last_segment_ended_at_unix_ms
                            .or(session.started_at_unix_ms)
                            .unwrap_or(session.requested_at_unix_ms);
                        let _ = self.record_gap_and_fail(
                            &session,
                            "SOURCE_INTERRUPTED",
                            "RECORDER_EXITED",
                            &message,
                            gap_start,
                            Some(now),
                        );
                    }
                }
                let _ = self.finalize_pending_event_clips(Some(session_id));
                self.clear_active(generation);
                return;
            }
            thread::sleep(Duration::from_millis(200));
        }
    }

    fn finalize_manifest_segments(
        &self,
        session_id: &str,
        processed: &mut HashSet<u64>,
    ) -> Result<(), String> {
        let initial = self.database.evidence_recording_session(session_id)?;
        for segment in &initial.segments {
            if segment.state == "LOCAL_VERIFIED" {
                processed.insert(segment.sequence);
            }
        }
        let entries = read_segment_list(&self.segment_list_path(session_id))?;
        for entry in entries {
            if processed.contains(&entry.sequence) {
                continue;
            }
            let session = self.database.evidence_recording_session(session_id)?;
            self.finalize_segment(&session, &entry)?;
            processed.insert(entry.sequence);
        }
        self.finalize_pending_event_clips(Some(session_id))?;
        Ok(())
    }

    fn finalize_segment(
        &self,
        session: &EvidenceRecordingSessionSnapshot,
        entry: &SegmentListEntry,
    ) -> Result<(), String> {
        let filename = Path::new(&entry.filename)
            .file_name()
            .and_then(|name| name.to_str())
            .ok_or_else(|| "evidence segment list contains an invalid filename".to_string())?;
        let temporary_relative_path = format!("temporary/{}/{filename}", session.id);
        let final_relative_path = format!("objects/{}/{:06}.mp4", session.id, entry.sequence);
        let temporary_path = self.config.evidence_root.join(&temporary_relative_path);
        let final_path = self.config.evidence_root.join(&final_relative_path);
        let manifest = session
            .segments
            .iter()
            .find(|segment| segment.sequence == entry.sequence);
        let (byte_length, sha256) = if let Some(manifest) = manifest {
            (manifest.byte_length, manifest.sha256.clone())
        } else {
            if !temporary_path.is_file() {
                let now = unix_time_ms();
                self.database.record_evidence_gap(
                    &session.id,
                    "SEGMENT_FILE_MISSING",
                    recording_timestamp(session, entry.start_seconds),
                    Some(recording_timestamp(session, entry.end_seconds)),
                    &json!({ "sequence": entry.sequence, "temporaryPath": temporary_relative_path }),
                    now,
                )?;
                return Err(format!(
                    "closed evidence segment {} is missing from temporary storage",
                    entry.sequence
                ));
            }
            checksum_file(&temporary_path)?
        };
        if let Some(previous) = session
            .segments
            .iter()
            .filter(|segment| {
                segment.state == "LOCAL_VERIFIED" && segment.sequence < entry.sequence
            })
            .max_by_key(|segment| segment.sequence)
        {
            let current_start = recording_timestamp(session, entry.start_seconds);
            if current_start.saturating_sub(previous.source_ended_at_unix_ms)
                > TIMELINE_GAP_TOLERANCE_MS
            {
                self.database.record_evidence_gap(
                    &session.id,
                    "SOURCE_TIMELINE_GAP",
                    previous.source_ended_at_unix_ms,
                    Some(current_start),
                    &json!({
                        "previousSequence": previous.sequence,
                        "nextSequence": entry.sequence,
                    }),
                    unix_time_ms(),
                )?;
            }
            if entry.sequence > previous.sequence + 1 {
                self.database.record_evidence_gap(
                    &session.id,
                    "SEGMENT_SEQUENCE_GAP",
                    previous.source_ended_at_unix_ms,
                    Some(current_start),
                    &json!({
                        "previousSequence": previous.sequence,
                        "nextSequence": entry.sequence,
                    }),
                    unix_time_ms(),
                )?;
            }
        }
        let finalization = SegmentFinalizationInput {
            recording_session_id: session.id.clone(),
            sequence: entry.sequence,
            temporary_relative_path,
            final_relative_path,
            source_started_at_unix_ms: recording_timestamp(session, entry.start_seconds),
            source_ended_at_unix_ms: recording_timestamp(session, entry.end_seconds),
            byte_length,
            sha256: sha256.clone(),
            observed_at_unix_ms: unix_time_ms(),
        };
        self.database
            .begin_evidence_segment_finalization(&finalization)?;
        fs::create_dir_all(final_path.parent().unwrap_or(&self.config.evidence_root))
            .map_err(|error| format!("create evidence object directory: {error}"))?;
        if final_path.is_file() {
            let (existing_bytes, existing_sha256) = checksum_file(&final_path)?;
            if existing_bytes != byte_length || existing_sha256 != sha256 {
                return Err(format!(
                    "published evidence segment {} does not match its manifest checksum",
                    entry.sequence
                ));
            }
        } else {
            File::open(&temporary_path)
                .and_then(|file| file.sync_all())
                .map_err(|error| format!("sync temporary evidence segment: {error}"))?;
            fs::rename(&temporary_path, &final_path)
                .map_err(|error| format!("atomically finalize evidence segment: {error}"))?;
        }
        let (verified_bytes, verified_sha256) = checksum_file(&final_path)?;
        if verified_bytes != byte_length || verified_sha256 != sha256 {
            return Err(format!(
                "evidence segment {} changed during atomic finalization",
                entry.sequence
            ));
        }
        self.database.complete_evidence_segment_finalization(
            &session.id,
            entry.sequence,
            unix_time_ms(),
        )
    }

    fn recover_interrupted_recordings(&self) -> Result<(), String> {
        for session in self.database.unfinished_evidence_recordings()? {
            let mut processed = HashSet::new();
            let _ = self.recover_finalizing_segments(&session, &mut processed);
            let _ = self.finalize_manifest_segments(&session.id, &mut processed);
            let refreshed = self.database.evidence_recording_session(&session.id)?;
            let temporary_count = fs::read_dir(self.temporary_session_directory(&session.id))
                .ok()
                .into_iter()
                .flatten()
                .flatten()
                .filter(|entry| {
                    entry
                        .path()
                        .file_name()
                        .and_then(|name| name.to_str())
                        .is_some_and(|name| name.ends_with(".partial.mp4"))
                })
                .count();
            let now = unix_time_ms();
            let gap_start = refreshed
                .last_segment_ended_at_unix_ms
                .or(refreshed.started_at_unix_ms)
                .unwrap_or(refreshed.requested_at_unix_ms);
            let message = "Atlas restarted while the local recorder owned this session; completed segments were recovered and any open partial remains invalid";
            self.database.record_evidence_gap(
                &session.id,
                "RECORDER_RESTART",
                gap_start,
                Some(now),
                &json!({ "unfinalizedPartialFiles": temporary_count }),
                now,
            )?;
            self.database.fail_evidence_recording(
                &session.id,
                "RECORDER_RESTART",
                message,
                "recorder_restart",
                now,
            )?;
            self.observe_recording_failure_alert(&refreshed, "CRITICAL", message, now)?;
        }
        Ok(())
    }

    fn recover_finalizing_segments(
        &self,
        session: &EvidenceRecordingSessionSnapshot,
        processed: &mut HashSet<u64>,
    ) -> Result<(), String> {
        for segment in &session.segments {
            if segment.state == "LOCAL_VERIFIED" {
                processed.insert(segment.sequence);
                continue;
            }
            let temporary_path = self
                .config
                .evidence_root
                .join(&segment.temporary_relative_path);
            let final_path = self.config.evidence_root.join(&segment.final_relative_path);
            let candidate = if final_path.is_file() {
                &final_path
            } else if temporary_path.is_file() {
                &temporary_path
            } else {
                self.database.record_evidence_gap(
                    &session.id,
                    "FINALIZING_SEGMENT_MISSING",
                    segment.source_started_at_unix_ms,
                    Some(segment.source_ended_at_unix_ms),
                    &json!({ "sequence": segment.sequence }),
                    unix_time_ms(),
                )?;
                continue;
            };
            let (byte_length, checksum) = checksum_file(candidate)?;
            if byte_length != segment.byte_length || checksum != segment.sha256 {
                return Err(format!(
                    "recovering segment {} failed checksum verification",
                    segment.sequence
                ));
            }
            if !final_path.is_file() {
                fs::create_dir_all(final_path.parent().unwrap_or(&self.config.evidence_root))
                    .map_err(|error| {
                        format!("create recovered evidence object directory: {error}")
                    })?;
                fs::rename(&temporary_path, &final_path)
                    .map_err(|error| format!("finalize recovered evidence segment: {error}"))?;
            }
            self.database.complete_evidence_segment_finalization(
                &session.id,
                segment.sequence,
                unix_time_ms(),
            )?;
            processed.insert(segment.sequence);
        }
        Ok(())
    }

    fn fail_before_spawn(
        &self,
        session: &EvidenceRecordingSessionSnapshot,
        code: &str,
        message: &str,
        now: i64,
    ) -> Result<(), String> {
        self.record_gap_and_fail(
            session,
            code,
            code,
            message,
            session.requested_at_unix_ms,
            Some(now),
        )
    }

    fn fail_after_process_spawn(
        &self,
        generation: u64,
        session: &EvidenceRecordingSessionSnapshot,
        child: &Arc<Mutex<Option<Child>>>,
        code: &str,
        message: &str,
        now: i64,
    ) -> Result<(), String> {
        kill_process(child);
        if let Ok(mut child) = child.lock() {
            if let Some(mut owned) = child.take() {
                let _ = owned.wait();
            }
        }
        self.clear_active(generation);
        self.fail_before_spawn(session, code, message, now)
    }

    fn record_gap_and_fail(
        &self,
        session: &EvidenceRecordingSessionSnapshot,
        gap_cause: &str,
        error_code: &str,
        message: &str,
        gap_started_at_unix_ms: i64,
        gap_ended_at_unix_ms: Option<i64>,
    ) -> Result<(), String> {
        let now = unix_time_ms();
        self.database.record_evidence_gap(
            &session.id,
            gap_cause,
            gap_started_at_unix_ms,
            gap_ended_at_unix_ms,
            &json!({ "errorCode": error_code, "message": message }),
            now,
        )?;
        self.database.fail_evidence_recording(
            &session.id,
            error_code,
            message,
            &gap_cause.to_lowercase(),
            now,
        )?;
        self.observe_recording_failure_alert(session, "CRITICAL", message, now)
    }

    fn observe_recording_failure_alert(
        &self,
        session: &EvidenceRecordingSessionSnapshot,
        severity: &str,
        message: &str,
        now: i64,
    ) -> Result<(), String> {
        self.database.observe_operational_alert(&AlertObservation {
            dedupe_key: format!("evidence_recording_gap:{}", session.id),
            alert_type: "EVIDENCE_RECORDING_GAP".into(),
            severity: severity.into(),
            source: "evidence_recorder".into(),
            drone_id: Some(session.drone_id.clone()),
            incident_id: session.incident_id.clone(),
            mission_run_id: session.mission_run_id.clone(),
            title: "Local evidence recording has a gap".into(),
            recommended_action: "Review the retained segment manifest and gap event, restore RTSP/storage health, then start a new recording session if evidence capture is still required.".into(),
            evidence: json!({ "recordingSessionId": session.id, "message": message }),
            observed_at_unix_ms: now,
        })?;
        Ok(())
    }

    fn reconcile_storage_alert(
        &self,
        session: &EvidenceRecordingSessionSnapshot,
        available: u64,
        now: i64,
    ) -> Result<(), String> {
        let key = format!("evidence_storage_low:{}", self.config.source.source_id);
        if available <= self.config.warning_free_bytes {
            let critical = available <= self.config.stop_free_bytes;
            self.database.observe_operational_alert(&AlertObservation {
                dedupe_key: key,
                alert_type: "EVIDENCE_STORAGE_LOW".into(),
                severity: if critical { "CRITICAL" } else { "WARNING" }.into(),
                source: "evidence_recorder".into(),
                drone_id: Some(session.drone_id.clone()),
                incident_id: session.incident_id.clone(),
                mission_run_id: session.mission_run_id.clone(),
                title: if critical {
                    "Evidence storage reserve reached"
                } else {
                    "Evidence storage is running low"
                }
                .into(),
                recommended_action: "Free space under the configured Atlas evidence root. Recording stops at the critical reserve so finalized evidence remains valid.".into(),
                evidence: json!({
                    "recordingSessionId": session.id,
                    "availableBytes": available,
                    "warningFreeBytes": self.config.warning_free_bytes,
                    "stopFreeBytes": self.config.stop_free_bytes,
                }),
                observed_at_unix_ms: now,
            })?;
        } else {
            self.database.resolve_operational_alert(
                &key,
                "Evidence storage recovered above the configured warning threshold",
                &json!({ "availableBytes": available }),
                now,
            )?;
        }
        Ok(())
    }

    fn recorder_command(&self, session_id: &str) -> Command {
        let temporary_directory = self.temporary_session_directory(session_id);
        let segment_list = temporary_directory.join("segments.csv.partial");
        let segment_pattern = temporary_directory.join("%06d.partial.mp4");
        let mut command = Command::new(&self.config.source.decoder_path);
        command
            .args([
                "-hide_banner",
                "-loglevel",
                "warning",
                "-rtsp_transport",
                &self.config.source.rtsp_transport,
                "-i",
                &self.config.source.rtsp_url,
                "-map",
                "0:v:0",
                "-an",
                "-sn",
                "-dn",
                "-c:v",
                "copy",
                "-f",
                "segment",
                "-segment_time",
                &self.config.segment_duration_seconds.to_string(),
                "-reset_timestamps",
                "1",
                "-segment_format",
                "mp4",
                "-segment_list_type",
                "csv",
                "-segment_list_flags",
                "+live",
                "-segment_list",
                &segment_list.to_string_lossy(),
                &segment_pattern.to_string_lossy(),
            ])
            .stdin(Stdio::piped())
            .stdout(Stdio::null())
            .stderr(Stdio::piped());
        command
    }

    fn temporary_capture_started(&self, session_id: &str) -> bool {
        fs::read_dir(self.temporary_session_directory(session_id))
            .ok()
            .into_iter()
            .flatten()
            .flatten()
            .any(|entry| {
                entry
                    .path()
                    .file_name()
                    .and_then(|name| name.to_str())
                    .is_some_and(|name| name.ends_with(".partial.mp4"))
                    && entry.metadata().is_ok_and(|metadata| metadata.len() > 0)
            })
    }

    fn disk_state(&self, available: u64) -> &'static str {
        if available <= self.config.stop_free_bytes {
            "STOP"
        } else if available <= self.config.warning_free_bytes {
            "WARNING"
        } else {
            "READY"
        }
    }

    fn temporary_session_directory(&self, session_id: &str) -> PathBuf {
        self.config.evidence_root.join("temporary").join(session_id)
    }

    fn object_session_directory(&self, session_id: &str) -> PathBuf {
        self.config.evidence_root.join("objects").join(session_id)
    }

    fn segment_list_path(&self, session_id: &str) -> PathBuf {
        self.temporary_session_directory(session_id)
            .join("segments.csv.partial")
    }

    fn clear_active(&self, generation: u64) {
        if let Ok(mut state) = self.shared.state.lock() {
            if state.generation == generation {
                state.active_session_id = None;
                state.child = None;
                state.stop_requested_at_unix_ms = None;
            }
        }
    }

    fn lock_state(&self) -> Result<std::sync::MutexGuard<'_, RecorderState>, String> {
        self.shared
            .state
            .lock()
            .map_err(|_| "evidence recorder state lock was poisoned".into())
    }

    fn available_bytes(&self) -> Result<u64, String> {
        self.config
            .available_bytes_override
            .map_or_else(|| available_disk_bytes(&self.config.evidence_root), Ok)
    }

    fn publish_still(
        &self,
        asset: &EvidenceAssetSnapshot,
        frame: &CapturedVideoFrame,
    ) -> Result<EvidenceAssetFileInput, String> {
        validate_asset_id(&asset.id)?;
        let temporary_directory = self.temporary_asset_directory(&asset.id);
        let final_directory = self.config.evidence_root.join("assets").join(&asset.id);
        fs::create_dir_all(&temporary_directory)
            .map_err(|error| format!("create temporary still directory: {error}"))?;
        let original = temporary_directory.join("original.jpg");
        let thumbnail = temporary_directory.join("thumbnail.jpg");
        write_synced(&original, &frame.jpeg)?;
        self.generate_thumbnail(&original, &thumbnail)?;
        let (byte_length, sha256) = checksum_file(&original)?;
        let (thumbnail_byte_length, thumbnail_sha256) = checksum_file(&thumbnail)?;
        publish_asset_directory(&temporary_directory, &final_directory)?;
        Ok(EvidenceAssetFileInput {
            relative_path: format!("assets/{}/original.jpg", asset.id),
            thumbnail_relative_path: format!("assets/{}/thumbnail.jpg", asset.id),
            mime_type: "image/jpeg".into(),
            thumbnail_mime_type: "image/jpeg".into(),
            byte_length,
            sha256,
            thumbnail_byte_length,
            thumbnail_sha256,
            source_started_at_unix_ms: frame.observed_at_unix_ms,
            source_ended_at_unix_ms: frame.observed_at_unix_ms,
        })
    }

    fn recover_interrupted_purges(&self) -> Result<(), String> {
        for asset in self.database.purging_evidence_assets()? {
            validate_asset_id(&asset.id)?;
            let directory = self.config.evidence_root.join("trash").join(&asset.id);
            if directory.exists() {
                fs::remove_dir_all(&directory)
                    .map_err(|error| format!("resume interrupted evidence purge: {error}"))?;
            }
            self.database.complete_evidence_asset_purge(
                &asset.id,
                "atlas_recovery",
                unix_time_ms(),
            )?;
        }
        Ok(())
    }

    fn recover_pending_evidence_assets(&self) -> Result<(), String> {
        for asset in self.database.pending_evidence_assets()? {
            validate_asset_id(&asset.id)?;
            let final_directory = self.config.evidence_root.join("assets").join(&asset.id);
            let temporary_directory = self.temporary_asset_directory(&asset.id);
            if final_directory.is_dir() {
                let (filename, mime_type) = if asset.asset_type == "STILL" {
                    ("original.jpg", "image/jpeg")
                } else {
                    ("original.mp4", "video/mp4")
                };
                let original = final_directory.join(filename);
                let thumbnail = final_directory.join("thumbnail.jpg");
                if original.is_file() && thumbnail.is_file() {
                    let (byte_length, sha256) = checksum_file(&original)?;
                    let (thumbnail_byte_length, thumbnail_sha256) = checksum_file(&thumbnail)?;
                    let source_started = asset
                        .source_started_at_unix_ms
                        .or(asset.requested_start_at_unix_ms)
                        .unwrap_or(asset.captured_at_unix_ms);
                    let source_ended = asset
                        .source_ended_at_unix_ms
                        .or(asset.requested_end_at_unix_ms)
                        .unwrap_or(asset.captured_at_unix_ms)
                        .max(source_started);
                    self.database.complete_evidence_asset(
                        &asset.id,
                        &EvidenceAssetFileInput {
                            relative_path: format!("assets/{}/{filename}", asset.id),
                            thumbnail_relative_path: format!("assets/{}/thumbnail.jpg", asset.id),
                            mime_type: mime_type.into(),
                            thumbnail_mime_type: "image/jpeg".into(),
                            byte_length,
                            sha256,
                            thumbnail_byte_length,
                            thumbnail_sha256,
                            source_started_at_unix_ms: source_started,
                            source_ended_at_unix_ms: source_ended,
                        },
                        unix_time_ms(),
                    )?;
                    continue;
                }
                self.database.fail_evidence_asset(
                    &asset.id,
                    "Atlas restarted after an incomplete asset publication",
                    unix_time_ms(),
                )?;
                fs::remove_dir_all(&final_directory)
                    .map_err(|error| format!("remove incomplete evidence publication: {error}"))?;
                continue;
            }
            if temporary_directory.exists() {
                fs::remove_dir_all(&temporary_directory).map_err(|error| {
                    format!("clear interrupted evidence asset staging: {error}")
                })?;
            }
            if asset.asset_type == "STILL" {
                self.database.fail_evidence_asset(
                    &asset.id,
                    "Atlas restarted before the captured still could be published",
                    unix_time_ms(),
                )?;
            }
        }
        Ok(())
    }

    fn finalize_pending_event_clips(
        &self,
        recording_session_id: Option<&str>,
    ) -> Result<(), String> {
        for asset in self
            .database
            .pending_event_clip_assets(recording_session_id)?
        {
            let Some(session_id) = asset.recording_session_id.as_deref() else {
                continue;
            };
            let session = self.database.evidence_recording_session(session_id)?;
            let requested_start = asset
                .requested_start_at_unix_ms
                .ok_or_else(|| "event clip is missing its requested start".to_string())?;
            let requested_end = asset
                .requested_end_at_unix_ms
                .ok_or_else(|| "event clip is missing its requested end".to_string())?;
            let verified = session
                .segments
                .iter()
                .filter(|segment| {
                    segment.state == "LOCAL_VERIFIED"
                        && segment.source_ended_at_unix_ms >= requested_start
                        && segment.source_started_at_unix_ms <= requested_end
                })
                .cloned()
                .collect::<Vec<_>>();
            let terminal = matches!(session.status.as_str(), "SUCCEEDED" | "FAILED");
            if verified.is_empty() {
                if terminal {
                    self.database.fail_evidence_asset(
                        &asset.id,
                        "No verified recording segment overlaps the event marker",
                        unix_time_ms(),
                    )?;
                }
                continue;
            }
            let coverage_end = verified
                .iter()
                .map(|segment| segment.source_ended_at_unix_ms)
                .max()
                .unwrap_or(requested_start);
            if coverage_end < requested_end && !terminal {
                continue;
            }
            let result = self.publish_event_clip(&asset, &verified, requested_start, requested_end);
            match result {
                Ok(files) => {
                    if let Err(error) =
                        self.database
                            .complete_evidence_asset(&asset.id, &files, unix_time_ms())
                    {
                        let _ = fs::remove_dir_all(
                            self.config.evidence_root.join("assets").join(&asset.id),
                        );
                        let _ =
                            self.database
                                .fail_evidence_asset(&asset.id, &error, unix_time_ms());
                    }
                }
                Err(error) => {
                    let _ = self
                        .database
                        .fail_evidence_asset(&asset.id, &error, unix_time_ms());
                    let _ = fs::remove_dir_all(self.temporary_asset_directory(&asset.id));
                }
            }
        }
        Ok(())
    }

    fn publish_event_clip(
        &self,
        asset: &EvidenceAssetSnapshot,
        segments: &[crate::database::EvidenceRecordingSegmentSnapshot],
        requested_start: i64,
        requested_end: i64,
    ) -> Result<EvidenceAssetFileInput, String> {
        validate_asset_id(&asset.id)?;
        let first = segments
            .first()
            .ok_or_else(|| "event clip requires at least one verified segment".to_string())?;
        let last = segments.last().unwrap_or(first);
        let actual_start = requested_start.max(first.source_started_at_unix_ms);
        let actual_end = requested_end.min(last.source_ended_at_unix_ms);
        if actual_end.saturating_sub(actual_start) < MIN_EVENT_CLIP_DURATION_MS {
            return Err("verified recording coverage is too short for an event clip".into());
        }
        let temporary_directory = self.temporary_asset_directory(&asset.id);
        let final_directory = self.config.evidence_root.join("assets").join(&asset.id);
        fs::create_dir_all(&temporary_directory)
            .map_err(|error| format!("create temporary event clip directory: {error}"))?;
        let concat_list = temporary_directory.join("segments.ffconcat");
        let clip = temporary_directory.join("original.mp4");
        let thumbnail = temporary_directory.join("thumbnail.jpg");
        let mut list = File::create(&concat_list)
            .map_err(|error| format!("create event clip segment list: {error}"))?;
        writeln!(list, "ffconcat version 1.0")
            .map_err(|error| format!("write event clip list header: {error}"))?;
        for segment in segments {
            let path = self.safe_evidence_path(&segment.final_relative_path)?;
            writeln!(list, "file '{}'", escape_ffconcat_path(&path))
                .map_err(|error| format!("write event clip segment list: {error}"))?;
        }
        list.flush()
            .map_err(|error| format!("flush event clip segment list: {error}"))?;
        list.sync_all()
            .map_err(|error| format!("sync event clip segment list: {error}"))?;
        let offset_seconds =
            (actual_start.saturating_sub(first.source_started_at_unix_ms)) as f64 / 1_000.0;
        let duration_seconds = (actual_end.saturating_sub(actual_start)) as f64 / 1_000.0;
        let output = Command::new(&self.config.source.decoder_path)
            .args([
                "-nostdin",
                "-y",
                "-hide_banner",
                "-loglevel",
                "error",
                "-f",
                "concat",
                "-safe",
                "0",
                "-i",
            ])
            .arg(&concat_list)
            .args([
                "-ss",
                &format!("{offset_seconds:.3}"),
                "-t",
                &format!("{duration_seconds:.3}"),
                "-map",
                "0:v:0",
                "-an",
                "-sn",
                "-dn",
                "-c:v",
                "copy",
                "-movflags",
                "+faststart",
            ])
            .arg(&clip)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::piped())
            .output()
            .map_err(|error| format!("start event clip generator: {error}"))?;
        if !output.status.success() {
            return Err(format!(
                "event clip generator failed: {}",
                String::from_utf8_lossy(&output.stderr).trim()
            ));
        }
        self.generate_thumbnail(&clip, &thumbnail)?;
        let (byte_length, sha256) = checksum_file(&clip)?;
        let (thumbnail_byte_length, thumbnail_sha256) = checksum_file(&thumbnail)?;
        fs::remove_file(&concat_list)
            .map_err(|error| format!("remove event clip segment list: {error}"))?;
        publish_asset_directory(&temporary_directory, &final_directory)?;
        Ok(EvidenceAssetFileInput {
            relative_path: format!("assets/{}/original.mp4", asset.id),
            thumbnail_relative_path: format!("assets/{}/thumbnail.jpg", asset.id),
            mime_type: "video/mp4".into(),
            thumbnail_mime_type: "image/jpeg".into(),
            byte_length,
            sha256,
            thumbnail_byte_length,
            thumbnail_sha256,
            source_started_at_unix_ms: actual_start,
            source_ended_at_unix_ms: actual_end,
        })
    }

    fn generate_thumbnail(&self, source: &Path, destination: &Path) -> Result<(), String> {
        let output = Command::new(&self.config.source.decoder_path)
            .args(["-nostdin", "-y", "-hide_banner", "-loglevel", "error", "-i"])
            .arg(source)
            .args(["-frames:v", "1", "-vf", "scale=480:-2", "-q:v", "4"])
            .arg(destination)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::piped())
            .output()
            .map_err(|error| format!("start evidence thumbnail generator: {error}"))?;
        if !output.status.success() || !destination.is_file() {
            return Err(format!(
                "evidence thumbnail generator failed: {}",
                String::from_utf8_lossy(&output.stderr).trim()
            ));
        }
        File::open(destination)
            .and_then(|file| file.sync_all())
            .map_err(|error| format!("sync evidence thumbnail: {error}"))
    }

    fn trash_asset_at(
        &self,
        input: &TrashEvidenceAssetInput,
        now: i64,
    ) -> Result<EvidenceAssetSnapshot, String> {
        let asset = self.database.evidence_asset(&input.asset_id)?;
        if asset.status != "READY" {
            return Err("only ready evidence can be moved to trash".into());
        }
        if asset.retention_class == "LEGAL_HOLD" {
            return Err("evidence under legal hold cannot be moved to trash".into());
        }
        validate_asset_id(&asset.id)?;
        let source_directory = self.config.evidence_root.join("assets").join(&asset.id);
        let destination_directory = self.config.evidence_root.join("trash").join(&asset.id);
        if !source_directory.is_dir() {
            return Err("evidence bytes are missing and cannot be moved to trash".into());
        }
        if destination_directory.exists() {
            return Err("evidence trash destination already exists".into());
        }
        fs::rename(&source_directory, &destination_directory)
            .map_err(|error| format!("move evidence to recoverable trash: {error}"))?;
        let relative_path = asset_relative_path("trash", &asset.id, &asset.relative_path)?;
        let thumbnail_relative_path =
            asset_relative_path("trash", &asset.id, &asset.thumbnail_relative_path)?;
        match self.database.mark_evidence_asset_trashed(
            &asset.id,
            &relative_path,
            &thumbnail_relative_path,
            &input.reason,
            &input.actor,
            now,
        ) {
            Ok(trashed) => Ok(trashed),
            Err(error) => {
                let _ = fs::rename(&destination_directory, &source_directory);
                Err(error)
            }
        }
    }

    fn temporary_asset_directory(&self, asset_id: &str) -> PathBuf {
        self.config
            .evidence_root
            .join("temporary")
            .join("assets")
            .join(asset_id)
    }

    fn safe_evidence_path(&self, relative: &str) -> Result<PathBuf, String> {
        let path = Path::new(relative);
        if path.as_os_str().is_empty()
            || path.is_absolute()
            || path.components().any(|component| {
                matches!(
                    component,
                    Component::ParentDir | Component::RootDir | Component::Prefix(_)
                )
            })
        {
            return Err("evidence asset path is outside the configured evidence root".into());
        }
        Ok(self.config.evidence_root.join(path))
    }

    #[cfg(test)]
    pub(crate) fn for_test(
        database: Arc<LocalDatabase>,
        source: VideoSourceConfig,
        evidence_root: PathBuf,
        warning_free_bytes: u64,
        stop_free_bytes: u64,
        available_bytes_override: Option<u64>,
    ) -> Result<Self, String> {
        ensure_evidence_root(&evidence_root)?;
        let recorder = Self {
            config: Arc::new(EvidenceRecorderConfig {
                evidence_root,
                source,
                segment_duration_seconds: 2,
                warning_free_bytes,
                stop_free_bytes,
                available_bytes_override,
            }),
            database,
            shared: Arc::new(RecorderShared {
                state: Mutex::new(RecorderState {
                    generation: 0,
                    active_session_id: None,
                    child: None,
                    stop_requested_at_unix_ms: None,
                    last_process_message: None,
                }),
            }),
        };
        recorder.recover_interrupted_purges()?;
        recorder.recover_pending_evidence_assets()?;
        recorder.recover_interrupted_recordings()?;
        recorder.finalize_pending_event_clips(None)?;
        Ok(recorder)
    }
}

fn ensure_evidence_root(root: &Path) -> Result<(), String> {
    fs::create_dir_all(root.join("objects"))
        .map_err(|error| format!("create evidence object root: {error}"))?;
    fs::create_dir_all(root.join("assets"))
        .map_err(|error| format!("create evidence asset root: {error}"))?;
    fs::create_dir_all(root.join("trash"))
        .map_err(|error| format!("create evidence trash root: {error}"))?;
    fs::create_dir_all(root.join("temporary"))
        .map_err(|error| format!("create evidence temporary root: {error}"))?;
    fs::create_dir_all(root.join("temporary").join("assets"))
        .map_err(|error| format!("create temporary evidence asset root: {error}"))?;
    let probe = root.join("temporary").join(".atlas-write-probe");
    fs::write(&probe, b"atlas")
        .map_err(|error| format!("verify evidence root is writable: {error}"))?;
    fs::remove_file(&probe)
        .map_err(|error| format!("remove evidence root write probe: {error}"))?;
    Ok(())
}

fn write_synced(path: &Path, bytes: &[u8]) -> Result<(), String> {
    let mut file = File::create(path)
        .map_err(|error| format!("create evidence media {}: {error}", path.display()))?;
    file.write_all(bytes)
        .map_err(|error| format!("write evidence media {}: {error}", path.display()))?;
    file.flush()
        .map_err(|error| format!("flush evidence media {}: {error}", path.display()))?;
    file.sync_all()
        .map_err(|error| format!("sync evidence media {}: {error}", path.display()))
}

fn publish_asset_directory(temporary: &Path, destination: &Path) -> Result<(), String> {
    if destination.exists() {
        return Err("evidence asset publication destination already exists".into());
    }
    let parent = destination
        .parent()
        .ok_or_else(|| "evidence asset destination has no parent".to_string())?;
    fs::create_dir_all(parent)
        .map_err(|error| format!("create evidence asset publication root: {error}"))?;
    File::open(temporary)
        .and_then(|directory| directory.sync_all())
        .map_err(|error| format!("sync temporary evidence asset directory: {error}"))?;
    fs::rename(temporary, destination)
        .map_err(|error| format!("atomically publish evidence asset: {error}"))
}

fn escape_ffconcat_path(path: &Path) -> String {
    path.to_string_lossy().replace('\'', "'\\''")
}

fn validate_asset_id(asset_id: &str) -> Result<(), String> {
    if asset_id.len() != 32 || !asset_id.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return Err("evidence asset id is invalid".into());
    }
    Ok(())
}

fn asset_relative_path(root: &str, asset_id: &str, previous: &str) -> Result<String, String> {
    validate_asset_id(asset_id)?;
    let filename = Path::new(previous)
        .file_name()
        .and_then(|value| value.to_str())
        .filter(|value| !value.is_empty())
        .ok_or_else(|| "evidence asset filename is invalid".to_string())?;
    Ok(format!("{root}/{asset_id}/{filename}"))
}

fn environment_u64(name: &str, fallback: u64, minimum: u64, maximum: u64) -> Result<u64, String> {
    let value = match env::var(name) {
        Ok(raw) if !raw.trim().is_empty() => raw
            .parse::<u64>()
            .map_err(|_| format!("{name} must be an unsigned integer"))?,
        _ => fallback,
    };
    if value < minimum || value > maximum {
        return Err(format!("{name} must be between {minimum} and {maximum}"));
    }
    Ok(value)
}

fn read_segment_list(path: &Path) -> Result<Vec<SegmentListEntry>, String> {
    let file = match File::open(path) {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(format!("open evidence segment list: {error}")),
    };
    let mut entries = Vec::new();
    for line in BufReader::new(file).lines() {
        let line = line.map_err(|error| format!("read evidence segment list: {error}"))?;
        let fields = line.split(',').map(str::trim).collect::<Vec<_>>();
        if fields.len() < 3 {
            continue;
        }
        let filename = fields[0].trim_matches('"').to_string();
        let Some(sequence) = Path::new(&filename)
            .file_name()
            .and_then(|name| name.to_str())
            .and_then(|name| name.split('.').next())
            .and_then(|value| value.parse::<u64>().ok())
        else {
            continue;
        };
        let start_seconds = fields[1]
            .trim_matches('"')
            .parse::<f64>()
            .map_err(|_| "evidence segment list contains an invalid start time".to_string())?;
        let end_seconds = fields[2]
            .trim_matches('"')
            .parse::<f64>()
            .map_err(|_| "evidence segment list contains an invalid end time".to_string())?;
        if !start_seconds.is_finite() || !end_seconds.is_finite() || end_seconds < start_seconds {
            return Err("evidence segment list contains an invalid time range".into());
        }
        entries.push(SegmentListEntry {
            sequence,
            filename,
            start_seconds,
            end_seconds,
        });
    }
    Ok(entries)
}

fn checksum_file(path: &Path) -> Result<(u64, String), String> {
    let mut file =
        File::open(path).map_err(|error| format!("open evidence segment for checksum: {error}"))?;
    let mut hasher = Sha256::new();
    let mut buffer = [0_u8; 128 * 1024];
    let mut byte_length = 0_u64;
    loop {
        let read = file
            .read(&mut buffer)
            .map_err(|error| format!("read evidence segment for checksum: {error}"))?;
        if read == 0 {
            break;
        }
        hasher.update(&buffer[..read]);
        byte_length = byte_length.saturating_add(read as u64);
    }
    Ok((byte_length, hex::encode(hasher.finalize())))
}

fn recording_timestamp(session: &EvidenceRecordingSessionSnapshot, seconds: f64) -> i64 {
    let origin = session
        .started_at_unix_ms
        .unwrap_or(session.requested_at_unix_ms);
    origin.saturating_add((seconds * 1_000.0).round() as i64)
}

fn request_process_stop(child: &Arc<Mutex<Option<Child>>>) {
    if let Ok(mut child) = child.lock() {
        if let Some(child) = child.as_mut() {
            if let Some(stdin) = child.stdin.as_mut() {
                let _ = stdin.write_all(b"q\n");
                let _ = stdin.flush();
            }
        }
    }
}

fn kill_process(child: &Arc<Mutex<Option<Child>>>) {
    if let Ok(mut child) = child.lock() {
        if let Some(child) = child.as_mut() {
            let _ = child.kill();
        }
    }
}

fn read_recorder_errors(shared: Arc<RecorderShared>, generation: u64, stderr: impl Read) {
    for line in BufReader::new(stderr).lines().map_while(Result::ok) {
        let message = line.trim();
        if message.is_empty() {
            continue;
        }
        if let Ok(mut state) = shared.state.lock() {
            if state.generation != generation {
                return;
            }
            state.last_process_message = Some(message.chars().take(2_000).collect());
        }
    }
}

#[cfg(unix)]
fn available_disk_bytes(path: &Path) -> Result<u64, String> {
    let path = CString::new(path.as_os_str().as_bytes())
        .map_err(|_| "evidence root contains an unsupported null byte".to_string())?;
    let mut stats = std::mem::MaybeUninit::<libc::statvfs>::uninit();
    // SAFETY: `path` is a valid nul-terminated filesystem path and `stats`
    // points to writable memory for the duration of the libc call.
    let result = unsafe { libc::statvfs(path.as_ptr(), stats.as_mut_ptr()) };
    if result != 0 {
        return Err(format!(
            "inspect evidence root free space: {}",
            std::io::Error::last_os_error()
        ));
    }
    // SAFETY: statvfs returned success and initialized the output structure.
    let stats = unsafe { stats.assume_init() };
    Ok((stats.f_bavail as u64).saturating_mul(stats.f_frsize))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn segment_list_only_exposes_closed_segments() {
        let root = std::env::temp_dir().join(format!("atlas-segment-list-{}", std::process::id()));
        let _ = fs::remove_dir_all(&root);
        fs::create_dir_all(&root).expect("create segment list fixture");
        let path = root.join("segments.csv.partial");
        fs::write(
            &path,
            "\"000000.partial.mp4\",0.000000,10.200000\n\"000001.partial.mp4\",10.200000,20.500000\n",
        )
        .expect("write segment list fixture");
        let entries = read_segment_list(&path).expect("parse segment list");
        assert_eq!(entries.len(), 2);
        assert_eq!(entries[1].sequence, 1);
        assert_eq!(entries[1].filename, "000001.partial.mp4");
        assert!((entries[1].start_seconds - 10.2).abs() < f64::EPSILON);
        fs::remove_dir_all(root).expect("remove segment list fixture");
    }

    #[test]
    fn checksum_is_stable_and_counts_exact_bytes() {
        let path =
            std::env::temp_dir().join(format!("atlas-evidence-checksum-{}", std::process::id()));
        fs::write(&path, b"atlas-evidence").expect("write checksum fixture");
        let (bytes, checksum) = checksum_file(&path).expect("checksum fixture");
        assert_eq!(bytes, 14);
        assert_eq!(
            checksum,
            "d082c12660943b7dc273bbe0ce7b570d093eeaddd749cd88eec427033299a804"
        );
        fs::remove_file(path).expect("remove checksum fixture");
    }
}
