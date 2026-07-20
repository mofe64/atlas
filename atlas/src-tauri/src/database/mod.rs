mod aircraft_follow;
mod alerts;
mod commands;
mod drones;
mod events;
mod evidence_assets;
mod history;
mod incidents;
mod known_buildings;
mod migrations;
mod mission_actions;
mod mission_runs;
mod missions;
mod perception_geolocations;
mod perception_operations;
mod perception_tracks;
mod recordings;
mod registration;
mod sessions;
mod snapshots;
mod telemetry;

pub(crate) use events::{StatusEventInput, StatusEventSnapshot};
pub(crate) use evidence_assets::{
    AnnotateEvidenceAssetInput, CaptureEvidenceStillInput, CreateEvidenceStillAssetInput,
    EvidenceAssetFileInput, EvidenceAssetListInput, EvidenceAssetSnapshot,
    EvidenceRetentionPolicySnapshot, QueueEvidenceEventClipInput, RestoreEvidenceAssetInput,
    ReviewEvidenceAssetInput, TrashEvidenceAssetInput, UpdateEvidenceAssetRetentionInput,
    UpdateEvidenceRetentionPolicyInput,
};
pub(crate) use history::{
    HistoryOverview, TelemetryChartSeries, TelemetryHistoryCursor, TelemetryHistoryPage,
    TelemetryHistoryQuery,
};
#[cfg(test)]
pub(crate) use incidents::PrepareIncidentResponseInput;
pub(crate) use incidents::{
    AbandonPreparedResponseInput, CreateIncidentInput, ExpandedPrepareIncidentResponseInput,
    IncidentDetailSnapshot, IncidentResponseAircraftSuitabilityInput,
    IncidentResponseAircraftSuitabilitySnapshot, IncidentResponsePlanPreview, IncidentSnapshot,
    PreparedIncidentResponse, UpdateIncidentInput,
};
pub(crate) use mission_actions::{MissionActionExecutionSnapshot, MissionActionUpdateInput};
pub(crate) use mission_runs::{MissionRunSnapshot, MissionRunUpdateInput};
pub(crate) use missions::{
    mission_plan_json_for_agent, templates as mission_templates, ApplyTerrainProfileInput,
    CreateMissionInput, MissionAction, MissionPlanSnapshot, MissionSnapshot, MissionTemplate,
    MissionWaypoint,
};
#[cfg(test)]
pub(crate) use perception_geolocations::TerrainSampleInput;
pub(crate) use perception_geolocations::{
    OperationalTrackGeolocationSnapshot, TerrainRefinementInput, TrackGeolocationSnapshot,
};
#[cfg(test)]
pub(crate) use perception_operations::CountingPoint;
pub(crate) use perception_operations::{
    CountingRuleSnapshot, PerceptionCountSnapshot, SelectTrackInput, TrackAnnotationInput,
    TrackAnnotationSnapshot, TrackSampleSnapshot, TrackSelectionSnapshot, UpsertCountingRuleInput,
};
pub(crate) use perception_tracks::{
    PerceptionTrackBatchInput, PerceptionTrackRecordSnapshot, PerceptionTrackUpdateInput,
    TrackBoundingBoxInput, TrackCountEventInput, TrackRuleCountInput,
};
pub(crate) use recordings::{
    CreateEvidenceRecordingInput, EvidenceRecordingSegmentSnapshot,
    EvidenceRecordingSessionSnapshot, SegmentFinalizationInput, StartEvidenceRecordingInput,
};
pub(crate) use registration::{RegisteredSession, RegistrationInput, ARCHIVED_REGISTRATION_ERROR};
pub(crate) use snapshots::AircraftTelemetrySnapshot;
pub(crate) use snapshots::{FleetSnapshot, OperationsSnapshot};
pub(crate) use telemetry::{
    BatteryTelemetry, GpsQuality, HomePosition, RcStatus, TelemetryInput, VehicleHealth,
};

use std::{
    fs,
    path::PathBuf,
    sync::Mutex,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use rusqlite::Connection;
use tauri::{AppHandle, Manager};

/// LocalDatabase is the native application's source of truth for directly
/// connected drones. It contains no users, organizations, authentication
/// tokens, or operator sessions.
pub(crate) struct LocalDatabase {
    connection: Mutex<Connection>,
    pub(crate) sqlite_version: String,
    pub(crate) journal_mode: String,
    pub(crate) path: PathBuf,
}

impl LocalDatabase {
    pub(crate) fn open(app: &AppHandle) -> Result<Self, String> {
        if let Some(configured_path) = std::env::var_os("ATLAS_SQLITE_PATH") {
            let path = explicit_database_path(PathBuf::from(configured_path))?;
            if let Some(directory) = path.parent() {
                fs::create_dir_all(directory)
                    .map_err(|error| format!("create configured SQLite directory: {error}"))?;
            }
            return Self::open_path(path);
        }

        let directory = app
            .path()
            .app_data_dir()
            .map_err(|error| format!("resolve Atlas application-data directory: {error}"))?;
        fs::create_dir_all(&directory)
            .map_err(|error| format!("create Atlas application-data directory: {error}"))?;
        Self::open_path(directory.join("atlas.db"))
    }

    pub(crate) fn open_path(path: PathBuf) -> Result<Self, String> {
        let connection = Connection::open(&path)
            .map_err(|error| format!("open local SQLite database: {error}"))?;
        connection
            .busy_timeout(Duration::from_secs(5))
            .map_err(|error| format!("configure SQLite busy timeout: {error}"))?;
        connection
            .pragma_update(None, "foreign_keys", "ON")
            .map_err(|error| format!("enable SQLite foreign keys: {error}"))?;
        connection
            .pragma_update(None, "synchronous", "FULL")
            .map_err(|error| format!("configure SQLite durability: {error}"))?;

        let journal_mode: String = connection
            .query_row("PRAGMA journal_mode = WAL", [], |row| row.get(0))
            .map_err(|error| format!("enable SQLite WAL mode: {error}"))?;
        if !journal_mode.eq_ignore_ascii_case("wal") {
            return Err(format!(
                "SQLite refused WAL mode and selected {journal_mode:?} instead"
            ));
        }

        let sqlite_version: String = connection
            .query_row("SELECT sqlite_version()", [], |row| row.get(0))
            .map_err(|error| format!("read SQLite version: {error}"))?;
        migrations::ensure_safe_sqlite_version(&sqlite_version)?;
        migrations::run(&connection)?;
        telemetry::prune_expired_snapshots(&connection, unix_time_ms())?;

        Ok(Self {
            connection: Mutex::new(connection),
            sqlite_version,
            journal_mode,
            path,
        })
    }
}

fn explicit_database_path(path: PathBuf) -> Result<PathBuf, String> {
    if !path.is_absolute() {
        return Err("ATLAS_SQLITE_PATH must be an absolute path".to_string());
    }
    Ok(path)
}

pub(crate) fn unix_time_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as i64
}

#[cfg(test)]
mod tests;
pub(crate) use aircraft_follow::{
    AircraftFollowAgentUpdateInput, AircraftFollowSessionSnapshot,
    CreateAircraftFollowSessionInput, EndAircraftFollowSessionInput,
    RenewAircraftFollowSessionInput,
};
pub(crate) use alerts::{AlertObservation, OperationalAlertListSnapshot, OperationalAlertSnapshot};
pub(crate) use commands::{VehicleCommandSnapshot, VehicleCommandUpdateInput};
