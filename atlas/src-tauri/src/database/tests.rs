use std::{
    fs,
    path::{Path, PathBuf},
    sync::{
        atomic::{AtomicU64, Ordering},
        Arc,
    },
    time::{SystemTime, UNIX_EPOCH},
};

use rusqlite::Connection;

use super::{
    explicit_database_path, AbandonPreparedResponseInput, AircraftFollowAgentUpdateInput,
    AlertObservation, AnnotateEvidenceAssetInput, BatteryTelemetry, CaptureEvidenceStillInput,
    CountingPoint, CreateAircraftFollowSessionInput, CreateEvidenceStillAssetInput,
    CreateIncidentInput, CreateMissionInput, EvidenceAssetFileInput,
    ExpandedPrepareIncidentResponseInput, GpsQuality, HomePosition,
    IncidentResponseAircraftSuitabilityInput, LocalDatabase, MissionActionUpdateInput,
    MissionRunUpdateInput, PerceptionTrackBatchInput, PerceptionTrackUpdateInput,
    PrepareIncidentResponseInput, QueueEvidenceEventClipInput, RcStatus, RegistrationInput,
    RestoreEvidenceAssetInput, ReviewEvidenceAssetInput, SegmentFinalizationInput,
    SelectTrackInput, StartEvidenceRecordingInput, StatusEventInput, TelemetryHistoryQuery,
    TelemetryInput, TerrainRefinementInput, TerrainSampleInput, TrackAnnotationInput,
    TrackBoundingBoxInput, TrackCountEventInput, TrackRuleCountInput, TrashEvidenceAssetInput,
    UpdateEvidenceAssetRetentionInput, UpdateEvidenceRetentionPolicyInput, UpdateIncidentInput,
    UpsertCountingRuleInput, VehicleCommandUpdateInput, VehicleHealth,
};
use super::{incidents::IncidentResponseGeometryInput, known_buildings::KnownBuildingDataset};

static TEST_DATABASE_COUNTER: AtomicU64 = AtomicU64::new(0);

#[test]
fn explicit_sqlite_path_must_be_absolute() {
    assert_eq!(
        explicit_database_path(PathBuf::from("state/atlas.db"))
            .expect_err("relative path must fail"),
        "ATLAS_SQLITE_PATH must be an absolute path"
    );

    let absolute = std::env::temp_dir().join("atlas-explicit-path.db");
    assert_eq!(
        explicit_database_path(absolute.clone()).expect("absolute path must pass"),
        absolute
    );
}

#[test]
fn migration_replaces_auth_cache_with_vehicle_operations_schema() {
    let (database, path) = test_database();
    assert_eq!(database.journal_mode.to_lowercase(), "wal");
    let connection = database.connection.lock().expect("lock database");
    let version: u32 = connection
        .query_row("PRAGMA user_version", [], |row| row.get(0))
        .expect("read schema version");
    assert_eq!(version, 24);
    let cached_identity_tables: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'cached_identity'",
            [],
            |row| row.get(0),
        )
        .expect("inspect auth table");
    assert_eq!(cached_identity_tables, 0);
    let command_tables: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('vehicle_commands', 'vehicle_command_events')",
            [],
            |row| row.get(0),
        )
        .expect("inspect command lifecycle tables");
    assert_eq!(command_tables, 2);
    let incident_and_action_tables: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('incidents', 'incident_events', 'incident_assignments', 'mission_action_executions', 'mission_action_execution_events')",
            [],
            |row| row.get(0),
        )
        .expect("inspect incident operation tables");
    assert_eq!(incident_and_action_tables, 5);
    let alert_tables: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('operational_alerts', 'operational_alert_events')",
            [],
            |row| row.get(0),
        )
        .expect("inspect operational alert tables");
    assert_eq!(alert_tables, 2);
    let evidence_tables: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('evidence_recording_sessions', 'evidence_recording_segments', 'evidence_gap_events', 'evidence_recording_events', 'evidence_retention_policy', 'evidence_assets', 'evidence_asset_annotations', 'evidence_asset_events')",
            [],
            |row| row.get(0),
        )
        .expect("inspect evidence recording tables");
    assert_eq!(evidence_tables, 8);
    let perception_track_tables: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('perception_track_sessions', 'perception_tracks', 'perception_track_events', 'perception_track_samples')",
            [],
            |row| row.get(0),
        )
        .expect("inspect perception track tables");
    assert_eq!(perception_track_tables, 4);
    let perception_operation_tables: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('perception_mission_tracks', 'perception_counting_rules', 'perception_count_events', 'perception_track_rule_counts', 'perception_track_selections', 'perception_track_selection_events', 'perception_track_annotations')",
            [],
            |row| row.get(0),
        )
        .expect("inspect perception operations tables");
    assert_eq!(perception_operation_tables, 7);
    let aircraft_follow_tables: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('aircraft_follow_sessions', 'aircraft_follow_target_updates', 'aircraft_follow_events')",
            [],
            |row| row.get(0),
        )
        .expect("inspect aircraft follow tables");
    assert_eq!(aircraft_follow_tables, 3);
    drop(connection);
    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn migration_upgrades_existing_v3_telemetry_database_to_vehicle_tables() {
    let path = test_database_path();
    let connection = Connection::open(&path).expect("create version 3 database");
    connection
        .execute_batch(
            r#"
            PRAGMA foreign_keys = ON;
            CREATE TABLE drones (id TEXT PRIMARY KEY);
            CREATE TABLE communication_links (id TEXT PRIMARY KEY);
            CREATE TABLE aircraft_telemetry_current (
                drone_id TEXT PRIMARY KEY REFERENCES drones(id) ON DELETE CASCADE,
                source TEXT NOT NULL,
                agent_observed_at_unix_ms INTEGER NOT NULL,
                received_at_unix_ms INTEGER NOT NULL,
                battery_percent REAL,
                relative_altitude_m REAL,
                flight_mode TEXT,
                armed INTEGER,
                in_air INTEGER,
                latitude REAL,
                longitude REAL,
                heading_deg REAL,
                ground_speed_mps REAL,
                gps_fix TEXT,
                satellites_visible INTEGER,
                home_position_set INTEGER
            );
            PRAGMA user_version = 3;
            "#,
        )
        .expect("create version 3 schema");
    drop(connection);

    let database = LocalDatabase::open_path(path.clone()).expect("upgrade version 3 database");
    let connection = database.connection.lock().expect("lock upgraded database");
    let version: u32 = connection
        .query_row("PRAGMA user_version", [], |row| row.get(0))
        .expect("read upgraded schema version");
    assert_eq!(version, 24);
    let new_columns: i64 = connection
        .query_row(
            "SELECT count(*) FROM pragma_table_info('vehicle_telemetry_current') WHERE name IN ('batteries_json', 'health_json', 'rc_status_json', 'gps_quality_json')",
            [],
            |row| row.get(0),
        )
        .expect("inspect upgraded telemetry columns");
    assert_eq!(new_columns, 4);
    let status_event_table: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'vehicle_status_events'",
            [],
            |row| row.get(0),
        )
        .expect("inspect status event table");
    assert_eq!(status_event_table, 1);
    let typed_event_columns: i64 = connection
        .query_row(
            "SELECT count(*) FROM pragma_table_info('vehicle_status_events') WHERE name IN ('origin', 'event_type', 'code', 'details_json')",
            [],
            |row| row.get(0),
        )
        .expect("inspect typed vehicle event columns");
    assert_eq!(typed_event_columns, 4);
    let telemetry_snapshot_table: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'vehicle_telemetry_snapshots'",
            [],
            |row| row.get(0),
        )
        .expect("inspect telemetry snapshot table");
    assert_eq!(telemetry_snapshot_table, 1);
    let old_telemetry_tables: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('aircraft_telemetry_current', 'aircraft_status_events')",
            [],
            |row| row.get(0),
        )
        .expect("inspect removed aircraft telemetry table names");
    assert_eq!(old_telemetry_tables, 0);
    drop(connection);
    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn command_deadline_expires_agent_accepted_and_executing_states() {
    let (database, path) = test_database();
    {
        let connection = database.connection.lock().expect("lock database");
        connection
            .execute(
                "INSERT INTO drones (id, name, vehicle_type, status, created_at_unix_ms, updated_at_unix_ms) VALUES ('drone-timeout', 'Timeout Drone', 'multicopter', 'active', 1, 1)",
                [],
            )
            .expect("insert timeout drone");
        for (id, status) in [
            ("accepted-timeout", "accepted"),
            ("executing-timeout", "executing"),
        ] {
            connection
                .execute(
                    "INSERT INTO vehicle_commands (id, drone_id, command_type, parameters_json, status, idempotency_key, created_at_unix_ms, deadline_at_unix_ms, updated_at_unix_ms) VALUES (?1, 'drone-timeout', 'gimbal_center', '{}', ?2, ?1, 1, 100, 1)",
                    (id, status),
                )
                .expect("insert expiring command");
        }
    }

    database
        .expire_vehicle_commands(101)
        .expect("expire commands");

    for id in ["accepted-timeout", "executing-timeout"] {
        let command = database
            .vehicle_command(id)
            .expect("read timed out command");
        assert_eq!(command.status, "timed_out");
        assert_eq!(command.result_code, "COMMAND_EXECUTION_DEADLINE_EXCEEDED");
        assert_eq!(command.events.len(), 1);
        assert_eq!(command.events[0].event_type, "timed_out");
    }

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn migration_renames_v4_tables_without_losing_current_state_or_events() {
    let path = test_database_path();
    let connection = Connection::open(&path).expect("create version 4 database");
    connection
        .execute_batch(
            r#"
            PRAGMA foreign_keys = ON;
            CREATE TABLE drones (id TEXT PRIMARY KEY);
            CREATE TABLE communication_links (id TEXT PRIMARY KEY);
            CREATE TABLE aircraft_telemetry_current (
                drone_id TEXT PRIMARY KEY REFERENCES drones(id) ON DELETE CASCADE,
                source TEXT NOT NULL,
                agent_observed_at_unix_ms INTEGER NOT NULL,
                received_at_unix_ms INTEGER NOT NULL,
                battery_percent REAL,
                relative_altitude_m REAL,
                flight_mode TEXT,
                armed INTEGER,
                in_air INTEGER,
                latitude REAL,
                longitude REAL,
                heading_deg REAL,
                ground_speed_mps REAL,
                gps_fix TEXT,
                satellites_visible INTEGER,
                home_position_set INTEGER,
                batteries_json TEXT NOT NULL DEFAULT '[]',
                health_json TEXT,
                absolute_altitude_m REAL,
                terrain_altitude_m REAL,
                bottom_clearance_m REAL,
                velocity_north_mps REAL,
                velocity_east_mps REAL,
                velocity_down_mps REAL,
                climb_rate_mps REAL,
                landed_state TEXT,
                rc_status_json TEXT,
                home_position_json TEXT,
                gps_quality_json TEXT
            );
            CREATE TABLE aircraft_status_events (
                id TEXT PRIMARY KEY,
                drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
                communication_link_id TEXT NOT NULL
                    REFERENCES communication_links(id) ON DELETE RESTRICT,
                source TEXT NOT NULL,
                severity TEXT NOT NULL,
                message TEXT NOT NULL,
                agent_observed_at_unix_ms INTEGER NOT NULL,
                received_at_unix_ms INTEGER NOT NULL
            );
            CREATE INDEX aircraft_status_events_drone_received
                ON aircraft_status_events(
                    drone_id,
                    received_at_unix_ms DESC,
                    agent_observed_at_unix_ms DESC
                );
            INSERT INTO drones (id) VALUES ('drone-1');
            INSERT INTO communication_links (id) VALUES ('link-1');
            INSERT INTO aircraft_telemetry_current (
                drone_id, source, agent_observed_at_unix_ms, received_at_unix_ms,
                battery_percent
            ) VALUES ('drone-1', 'mavsdk', 1000, 1100, 82.5);
            INSERT INTO aircraft_status_events (
                id, drone_id, communication_link_id, source, severity, message,
                agent_observed_at_unix_ms, received_at_unix_ms
            ) VALUES (
                'event-1', 'drone-1', 'link-1', 'mavsdk', 'WARNING',
                'Battery temperature high', 1000, 1100
            );
            PRAGMA user_version = 4;
            "#,
        )
        .expect("create version 4 schema and data");
    drop(connection);

    let database = LocalDatabase::open_path(path.clone()).expect("upgrade version 4 database");
    let connection = database.connection.lock().expect("lock upgraded database");
    let battery_percent: f64 = connection
        .query_row(
            "SELECT battery_percent FROM vehicle_telemetry_current WHERE drone_id = 'drone-1'",
            [],
            |row| row.get(0),
        )
        .expect("read renamed current telemetry");
    assert_eq!(battery_percent, 82.5);
    let message: String = connection
        .query_row(
            "SELECT message FROM vehicle_status_events WHERE id = 'event-1'",
            [],
            |row| row.get(0),
        )
        .expect("read renamed status event");
    assert_eq!(message, "Battery temperature high");
    let event_identity: (String, String) = connection
        .query_row(
            "SELECT origin, event_type FROM vehicle_status_events WHERE id = 'event-1'",
            [],
            |row| Ok((row.get(0)?, row.get(1)?)),
        )
        .expect("read migrated event identity");
    assert_eq!(event_identity, ("px4".into(), "status_text".into()));
    drop(connection);
    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn registration_creates_graph_and_tracks_link_lifecycle() {
    let (database, path) = test_database();
    let now = unix_time_ms();
    let input = RegistrationInput {
        session_id: "session-1".into(),
        installation_id: "agent-1".into(),
        agent_version: "0.1.0".into(),
        protocol_version: "1".into(),
        device_profile_json: r#"{"hostname":"pi"}"#.into(),
        capabilities_json: r#"["registration","heartbeat"]"#.into(),
        drone_id: "drone-1".into(),
        drone_name: "Atlas One".into(),
        flight_controller_uid: "fc-1".into(),
        serial_number: "serial-1".into(),
        vehicle_type: "multicopter".into(),
        flight_controller_transport: "serial".into(),
        endpoint_description: "/dev/serial0".into(),
        baud_rate: 921_600,
        mavlink_system_id: 1,
        mavlink_component_id: 1,
        remote_address: "192.168.144.168:50000".into(),
        observed_at_unix_ms: now,
    };
    let registered = database.register_agent(&input).expect("register agent");
    assert_eq!(registered.agent_id, "agent-1");

    database
        .record_heartbeat("session-1", now + 1000)
        .expect("record heartbeat");
    let telemetry_input = TelemetryInput {
        source: "mavsdk".into(),
        agent_observed_at_unix_ms: now + 900,
        received_at_unix_ms: now + 1000,
        battery_percent: Some(82.5),
        relative_altitude_m: Some(12.4),
        flight_mode: Some("HOLD".into()),
        armed: Some(true),
        in_air: Some(true),
        latitude: Some(51.5074),
        longitude: Some(-0.1278),
        heading_deg: Some(175.0),
        ground_speed_mps: Some(4.5),
        gps_fix: Some("3D".into()),
        satellites_visible: Some(14),
        home_position_set: Some(true),
        batteries: vec![BatteryTelemetry {
            id: 0,
            function: "ALL".into(),
            remaining_percent: Some(82.5),
            voltage_v: Some(22.8),
            current_a: Some(5.2),
            temperature_c: Some(31.0),
            consumed_ah: Some(1.4),
            time_remaining_s: Some(1_200.0),
        }],
        health: Some(VehicleHealth {
            gyrometer_calibration_ok: true,
            accelerometer_calibration_ok: true,
            magnetometer_calibration_ok: true,
            local_position_ok: true,
            global_position_ok: true,
            home_position_ok: true,
            armable: true,
        }),
        absolute_altitude_m: Some(45.3),
        terrain_altitude_m: Some(32.9),
        bottom_clearance_m: Some(12.3),
        velocity_north_mps: Some(4.0),
        velocity_east_mps: Some(2.0),
        velocity_down_mps: Some(-0.5),
        climb_rate_mps: Some(0.5),
        landed_state: Some("IN_AIR".into()),
        rc_status: Some(RcStatus {
            available: true,
            was_available_once: true,
            signal_strength_percent: Some(76.0),
        }),
        home_position: Some(HomePosition {
            latitude: Some(51.507),
            longitude: Some(-0.128),
            absolute_altitude_m: Some(32.9),
            relative_altitude_m: Some(0.0),
        }),
        gps_quality: Some(GpsQuality {
            hdop: Some(0.8),
            vdop: Some(1.2),
            horizontal_uncertainty_m: Some(0.4),
            vertical_uncertainty_m: Some(0.7),
            velocity_uncertainty_mps: Some(0.1),
            course_over_ground_deg: Some(26.6),
        }),
    };
    database
        .record_telemetry("session-1", &telemetry_input)
        .expect("record telemetry");
    database
        .record_status_event(
            "session-1",
            &StatusEventInput {
                origin: "px4".into(),
                event_type: "status_text".into(),
                code: None,
                details_json: None,
                source: "mavsdk".into(),
                severity: "WARNING".into(),
                message: "Battery temperature high".into(),
                agent_observed_at_unix_ms: now + 950,
                received_at_unix_ms: now + 1000,
            },
        )
        .expect("record status event");
    let connected = database
        .operations_snapshot()
        .expect("read connected snapshot");
    assert_eq!(connected.connection_status, "connected");
    assert_eq!(connected.vehicle_type.as_deref(), Some("multicopter"));
    assert_eq!(connected.vehicle_status.as_deref(), Some("active"));
    assert_eq!(connected.last_heartbeat_at_unix_ms, Some(now + 1000));
    let telemetry = connected.telemetry.expect("current telemetry");
    assert_eq!(telemetry.status, "live");
    assert_eq!(telemetry.battery_percent, Some(82.5));
    assert_eq!(telemetry.flight_mode.as_deref(), Some("HOLD"));
    assert_eq!(telemetry.batteries[0].voltage_v, Some(22.8));
    assert!(telemetry.health.expect("vehicle health").armable);
    assert!(telemetry.rc_status.expect("RC status").available);
    assert_eq!(connected.status_events.len(), 1);
    assert_eq!(connected.status_events[0].severity, "WARNING");
    assert_eq!(connected.status_events[0].origin, "px4");
    assert_eq!(connected.status_events[0].event_type, "status_text");

    let selected = database
        .operations_snapshot_for(Some("drone-1"))
        .expect("read selected aircraft snapshot");
    assert_eq!(selected.drone_name.as_deref(), Some("Atlas One"));
    let fleet = database
        .fleet_snapshot(false)
        .expect("read local fleet snapshot");
    assert_eq!(fleet.aircraft.len(), 1);
    assert_eq!(fleet.aircraft[0].drone_id.as_deref(), Some("drone-1"));
    let history_overview = database.history_overview().expect("read history overview");
    assert_eq!(history_overview.retention_days, 7);
    assert_eq!(history_overview.drones.len(), 1);
    assert_eq!(history_overview.drones[0].drone_name, "Atlas One");
    assert_eq!(history_overview.drones[0].snapshot_count, 1);
    assert_eq!(history_overview.drones[0].event_count, 1);
    assert_eq!(
        history_overview.drones[0].latest_flight_mode.as_deref(),
        Some("HOLD")
    );

    let command = database
        .create_vehicle_command("drone-1", "hold", "{}", Some(15_000), now + 1_000)
        .expect("create hold command");
    let command = database
        .mark_vehicle_command_sent(&command.id, &registered.communication_link_id, now + 1_100)
        .expect("mark hold command sent");
    assert_eq!(command.status, "sent");
    for (event_id, event_type, state, observed_at) in [
        ("command-accepted", "accepted", "accepted", now + 1_200),
        ("command-executing", "executing", "executing", now + 1_300),
        ("command-succeeded", "succeeded", "succeeded", now + 1_400),
    ] {
        database
            .apply_vehicle_command_update(&VehicleCommandUpdateInput {
                event_id: event_id.into(),
                command_id: command.id.clone(),
                event_type: event_type.into(),
                next_state: Some(state.into()),
                communication_link_id: registered.communication_link_id.clone(),
                occurred_at_unix_ms: observed_at,
                progress_percent: None,
                result_code: if state == "succeeded" {
                    "RESULT_SUCCESS".into()
                } else {
                    String::new()
                },
                message: format!("Command {event_type}"),
                evidence_json: None,
            })
            .expect("apply command lifecycle update");
    }
    let completed_command = database
        .vehicle_command(&command.id)
        .expect("read completed command");
    assert_eq!(completed_command.status, "succeeded");
    assert_eq!(completed_command.delivery_attempts, 1);
    assert_eq!(completed_command.events.len(), 5);

    let initial_snapshot_count: i64 = database
        .connection
        .lock()
        .expect("lock database")
        .query_row(
            "SELECT count(*) FROM vehicle_telemetry_snapshots",
            [],
            |row| row.get(0),
        )
        .expect("count initial telemetry snapshots");
    assert_eq!(initial_snapshot_count, 1);

    let mut too_soon = telemetry_input.clone();
    too_soon.agent_observed_at_unix_ms = now + 3_900;
    too_soon.received_at_unix_ms = now + 4_000;
    database
        .record_telemetry("session-1", &too_soon)
        .expect("record current telemetry without interval snapshot");
    let snapshot_count: i64 = database
        .connection
        .lock()
        .expect("lock database")
        .query_row(
            "SELECT count(*) FROM vehicle_telemetry_snapshots",
            [],
            |row| row.get(0),
        )
        .expect("count telemetry snapshots before interval");
    assert_eq!(snapshot_count, 1);

    let mut transition = too_soon.clone();
    transition.agent_observed_at_unix_ms = now + 4_000;
    transition.received_at_unix_ms = now + 4_100;
    transition.flight_mode = Some("MISSION".into());
    database
        .record_telemetry("session-1", &transition)
        .expect("record state-transition telemetry snapshot");
    let mut interval = transition.clone();
    interval.agent_observed_at_unix_ms = now + 9_000;
    interval.received_at_unix_ms = now + 9_100;
    database
        .record_telemetry("session-1", &interval)
        .expect("record active interval telemetry snapshot");
    let mut idle_transition = interval.clone();
    idle_transition.agent_observed_at_unix_ms = now + 9_100;
    idle_transition.received_at_unix_ms = now + 9_200;
    idle_transition.armed = Some(false);
    idle_transition.in_air = Some(false);
    idle_transition.landed_state = Some("ON_GROUND".into());
    database
        .record_telemetry("session-1", &idle_transition)
        .expect("record idle state-transition telemetry snapshot");
    let mut idle_too_soon = idle_transition.clone();
    idle_too_soon.agent_observed_at_unix_ms = now + 29_900;
    idle_too_soon.received_at_unix_ms = now + 30_000;
    database
        .record_telemetry("session-1", &idle_too_soon)
        .expect("record current idle telemetry before snapshot interval");
    let mut idle_interval = idle_transition.clone();
    idle_interval.agent_observed_at_unix_ms = now + 39_100;
    idle_interval.received_at_unix_ms = now + 39_200;
    database
        .record_telemetry("session-1", &idle_interval)
        .expect("record idle interval telemetry snapshot");
    let capture_reasons: Vec<String> = {
        let connection = database.connection.lock().expect("lock database");
        let mut statement = connection
            .prepare(
                "SELECT capture_reason FROM vehicle_telemetry_snapshots ORDER BY received_at_unix_ms",
            )
            .expect("prepare telemetry snapshot reasons");
        statement
            .query_map([], |row| row.get(0))
            .expect("read telemetry snapshot reasons")
            .collect::<Result<Vec<_>, _>>()
            .expect("decode telemetry snapshot reasons")
    };
    assert_eq!(
        capture_reasons,
        vec![
            "initial",
            "state_transition",
            "interval",
            "state_transition",
            "interval"
        ]
    );

    let derived_event_types: Vec<String> = {
        let connection = database.connection.lock().expect("lock database");
        let mut statement = connection
            .prepare(
                "SELECT event_type FROM vehicle_status_events WHERE origin = 'atlas_native' ORDER BY received_at_unix_ms, rowid",
            )
            .expect("prepare derived vehicle event types");
        statement
            .query_map([], |row| row.get(0))
            .expect("read derived vehicle event types")
            .collect::<Result<Vec<_>, _>>()
            .expect("decode derived vehicle event types")
    };
    assert_eq!(
        derived_event_types,
        vec![
            "flight_mode_changed",
            "vehicle_disarmed",
            "landing_detected",
            "landed_state_changed"
        ]
    );
    let flight_mode_details: String = database
        .connection
        .lock()
        .expect("lock database")
        .query_row(
            "SELECT details_json FROM vehicle_status_events WHERE event_type = 'flight_mode_changed'",
            [],
            |row| row.get(0),
        )
        .expect("read flight-mode event details");
    let flight_mode_details: serde_json::Value =
        serde_json::from_str(&flight_mode_details).expect("decode flight-mode event details");
    assert_eq!(flight_mode_details["previous"], "HOLD");
    assert_eq!(flight_mode_details["current"], "MISSION");
    let derived_history = database
        .vehicle_event_history("drone-1", Some(now), Some(now + 40_000), 2)
        .expect("read bounded vehicle event history");
    assert_eq!(derived_history.len(), 2);
    assert_eq!(derived_history[0].origin, "atlas_native");

    let first_history_page = database
        .telemetry_history(&TelemetryHistoryQuery {
            drone_id: "drone-1".into(),
            from_received_at_unix_ms: None,
            to_received_at_unix_ms: None,
            before: None,
            limit: 2,
        })
        .expect("read first telemetry history page");
    assert_eq!(first_history_page.snapshots.len(), 2);
    assert_eq!(
        first_history_page.snapshots[0]
            .telemetry
            .received_at_unix_ms,
        now + 39_200
    );
    let second_history_page = database
        .telemetry_history(&TelemetryHistoryQuery {
            drone_id: "drone-1".into(),
            from_received_at_unix_ms: None,
            to_received_at_unix_ms: None,
            before: first_history_page.next_cursor.clone(),
            limit: 2,
        })
        .expect("read second telemetry history page");
    assert_eq!(second_history_page.snapshots.len(), 2);
    let third_history_page = database
        .telemetry_history(&TelemetryHistoryQuery {
            drone_id: "drone-1".into(),
            from_received_at_unix_ms: None,
            to_received_at_unix_ms: None,
            before: second_history_page.next_cursor.clone(),
            limit: 2,
        })
        .expect("read third telemetry history page");
    assert_eq!(third_history_page.snapshots.len(), 1);
    assert!(third_history_page.next_cursor.is_none());
    let transition_history = database
        .telemetry_history(&TelemetryHistoryQuery {
            drone_id: "drone-1".into(),
            from_received_at_unix_ms: Some(now + 9_100),
            to_received_at_unix_ms: Some(now + 9_200),
            before: None,
            limit: 10,
        })
        .expect("read time-bounded telemetry history");
    assert_eq!(transition_history.snapshots.len(), 2);
    assert!(database
        .telemetry_history(&TelemetryHistoryQuery {
            drone_id: "drone-1".into(),
            from_received_at_unix_ms: Some(now + 2),
            to_received_at_unix_ms: Some(now + 1),
            before: None,
            limit: 10,
        })
        .is_err());
    let chart_series = database
        .telemetry_chart_series("drone-1", now, now + 40_000, 600)
        .expect("read telemetry chart series");
    assert_eq!(chart_series.points.len(), 5);
    assert_eq!(
        chart_series.points[0].rc_signal_strength_percent,
        Some(76.0)
    );
    let downsampled = database
        .telemetry_chart_series("drone-1", now, now + 40_000, 2)
        .expect("read downsampled telemetry chart series");
    assert!(downsampled.points.len() <= 2);

    {
        let connection = database.connection.lock().expect("lock database");
        connection
            .execute(
                r#"
                INSERT INTO vehicle_telemetry_snapshots
                SELECT 'expired-snapshot', drone_id, communication_link_id,
                       capture_reason, source,
                       agent_observed_at_unix_ms - ?1,
                       received_at_unix_ms - ?1,
                       battery_percent, relative_altitude_m, flight_mode, armed,
                       in_air, latitude, longitude, heading_deg, ground_speed_mps,
                       gps_fix, satellites_visible, home_position_set,
                       batteries_json, health_json, absolute_altitude_m,
                       terrain_altitude_m, bottom_clearance_m,
                       velocity_north_mps, velocity_east_mps, velocity_down_mps,
                       climb_rate_mps, landed_state, rc_status_json,
                       home_position_json, gps_quality_json
                FROM vehicle_telemetry_snapshots
                ORDER BY received_at_unix_ms DESC
                LIMIT 1
                "#,
                [super::telemetry::SNAPSHOT_RETENTION_MS + 1],
            )
            .expect("insert expired telemetry snapshot fixture");
    }
    let mut retention_trigger = idle_interval.clone();
    retention_trigger.agent_observed_at_unix_ms = now + 69_900;
    retention_trigger.received_at_unix_ms = now + 70_000;
    database
        .record_telemetry("session-1", &retention_trigger)
        .expect("capture telemetry and apply retention");
    let expired_snapshot_count: i64 = database
        .connection
        .lock()
        .expect("lock database")
        .query_row(
            "SELECT count(*) FROM vehicle_telemetry_snapshots WHERE id = 'expired-snapshot'",
            [],
            |row| row.get(0),
        )
        .expect("count expired telemetry snapshot");
    assert_eq!(expired_snapshot_count, 0);

    for index in 0..205 {
        database
            .record_status_event(
                "session-1",
                &StatusEventInput {
                    origin: "px4".into(),
                    event_type: "status_text".into(),
                    code: None,
                    details_json: None,
                    source: "mavsdk".into(),
                    severity: "INFO".into(),
                    message: format!("Event {index}"),
                    agent_observed_at_unix_ms: now + 1_100 + index,
                    received_at_unix_ms: now + 1_100 + index,
                },
            )
            .expect("record bounded status event");
    }
    let retained_event_count: i64 = database
        .connection
        .lock()
        .expect("lock database")
        .query_row("SELECT count(*) FROM vehicle_status_events", [], |row| {
            row.get(0)
        })
        .expect("count retained status events");
    assert_eq!(retained_event_count, 200);
    assert_eq!(
        database
            .operations_snapshot()
            .expect("snapshot bounded status events")
            .status_events
            .len(),
        20
    );

    database
        .close_session("session-1", "test complete", now + 71_000)
        .expect("close session");
    let disconnected = database
        .operations_snapshot()
        .expect("read disconnected snapshot");
    assert_eq!(disconnected.connection_status, "disconnected");
    assert_eq!(
        disconnected.telemetry.expect("retained telemetry").status,
        "stale"
    );

    database
        .connection
        .lock()
        .expect("lock database")
        .execute(
            r#"
            UPDATE vehicle_telemetry_snapshots
            SET received_at_unix_ms = ?1
            WHERE id = (
                SELECT id FROM vehicle_telemetry_snapshots
                ORDER BY received_at_unix_ms
                LIMIT 1
            )
            "#,
            [unix_time_ms() - super::telemetry::SNAPSHOT_RETENTION_MS - 1],
        )
        .expect("age telemetry snapshot for startup retention");
    drop(database);
    let reopened = LocalDatabase::open_path(path.clone()).expect("reopen retained database");
    let expired_after_startup: i64 = reopened
        .connection
        .lock()
        .expect("lock reopened database")
        .query_row(
            "SELECT count(*) FROM vehicle_telemetry_snapshots WHERE received_at_unix_ms < ?1",
            [unix_time_ms() - super::telemetry::SNAPSHOT_RETENTION_MS],
            |row| row.get(0),
        )
        .expect("count expired snapshots after startup retention");
    assert_eq!(expired_after_startup, 0);
    drop(reopened);
    remove_sqlite_files(&path);
}

#[test]
fn perception_track_lifecycle_is_revisioned_durable_and_idempotent() {
    let (database, path) = test_database();
    let now = unix_time_ms();
    database
        .register_agent(&alert_registration_input("track-agent-session", now))
        .expect("register track source agent");

    let mut created = perception_track_batch("track-session-1", now + 100);
    database
        .record_perception_track_batch(
            "track-agent-session",
            "alert-source-drone",
            "perception-stream-1",
            &created,
            now + 110,
        )
        .expect("persist tentative track");

    created.session_started = false;
    created.observed_at_unix_ms = now + 200;
    created.tracks[0].revision = 2;
    created.tracks[0].age_frames = 2;
    created.tracks[0].observation_count = 2;
    created.tracks[0].last_observed_at_unix_ms = now + 200;
    created.tracks[0].lifecycle_state = "ACTIVE".into();
    created.tracks[0].update_reason = "STATE_CHANGED".into();
    database
        .record_perception_track_batch(
            "track-agent-session",
            "alert-source-drone",
            "perception-stream-1",
            &created,
            now + 210,
        )
        .expect("persist active track");

    created.observed_at_unix_ms = now + 1_200;
    created.tracks[0].revision = 3;
    created.tracks[0].age_frames = 32;
    created.tracks[0].observation_count = 32;
    created.tracks[0].last_observed_at_unix_ms = now + 1_200;
    created.tracks[0].update_reason = "PERIODIC".into();
    for _ in 0..2 {
        database
            .record_perception_track_batch(
                "track-agent-session",
                "alert-source-drone",
                "perception-stream-1",
                &created,
                now + 1_210,
            )
            .expect("periodic update is idempotent");
    }

    created.observed_at_unix_ms = now + 1_500;
    created.session_ended = true;
    created.session_end_reason = "PERCEPTION_DEACTIVATED".into();
    created.tracks[0].revision = 4;
    created.tracks[0].age_frames = 40;
    created.tracks[0].lifecycle_state = "CLOSED".into();
    created.tracks[0].closed_at_unix_ms = Some(now + 1_500);
    created.tracks[0].closure_reason = "PERCEPTION_DEACTIVATED".into();
    created.tracks[0].update_reason = "CLOSED".into();
    database
        .record_perception_track_batch(
            "track-agent-session",
            "alert-source-drone",
            "perception-stream-1",
            &created,
            now + 1_510,
        )
        .expect("persist closed track");

    let connection = database.connection.lock().expect("lock database");
    let track: (String, i64, i64, String, Option<i64>) = connection
        .query_row(
            "SELECT lifecycle_state, revision, observation_count, closure_reason, closed_at_unix_ms FROM perception_tracks WHERE id = 'atlas:track-session-1:1'",
            [],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?, row.get(4)?)),
        )
        .expect("read durable perception track");
    assert_eq!(
        track,
        (
            "CLOSED".into(),
            4,
            32,
            "PERCEPTION_DEACTIVATED".into(),
            Some(now + 1_500)
        )
    );
    let event_count: i64 = connection
        .query_row(
            "SELECT count(*) FROM perception_track_events WHERE track_id = 'atlas:track-session-1:1'",
            [],
            |row| row.get(0),
        )
        .expect("count lifecycle events");
    let sample_count: i64 = connection
        .query_row(
            "SELECT count(*) FROM perception_track_samples WHERE track_id = 'atlas:track-session-1:1'",
            [],
            |row| row.get(0),
        )
        .expect("count important track samples");
    assert_eq!(
        event_count, 3,
        "periodic snapshots are not lifecycle events"
    );
    assert_eq!(sample_count, 4, "duplicate revisions are idempotent");
    let session_end: (Option<i64>, String) = connection
        .query_row(
            "SELECT ended_at_unix_ms, end_reason FROM perception_track_sessions WHERE id = 'track-session-1'",
            [],
            |row| Ok((row.get(0)?, row.get(1)?)),
        )
        .expect("read durable track session");
    assert_eq!(
        session_end,
        (Some(now + 1_500), "PERCEPTION_DEACTIVATED".into())
    );
    drop(connection);

    let history = database
        .perception_tracks("alert-source-drone", true, 10)
        .expect("query durable track history");
    assert_eq!(history.len(), 1);
    assert_eq!(history[0].track_id, "atlas:track-session-1:1");
    assert_eq!(history[0].lifecycle_state, "CLOSED");
    assert_eq!(history[0].event_count, 3);
    assert_eq!(history[0].sample_count, 4);
    assert!(database
        .perception_tracks("alert-source-drone", false, 10)
        .expect("query open track history")
        .is_empty());
    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn aircraft_follow_requires_commissioned_world_state_and_holds_on_lease_loss() {
    let (database, path) = test_database();
    let now = unix_time_ms();
    let mut registration = alert_registration_input("follow-agent-session", now);
    registration.capabilities_json = serde_json::json!([
        "registration",
        "heartbeat",
        "telemetry",
        "geolocation:boresight_alignment:verified",
        "aircraft_follow:standoff:v1:verified",
        "aircraft_follow:validation:sitl-hil-flight/accepted-1"
    ])
    .to_string();
    database
        .register_agent(&registration)
        .expect("register commissioned follow Agent");
    let mut telemetry = dispatch_ready_telemetry(now + 10, 51.5, -0.14, 82.0);
    telemetry.relative_altitude_m = Some(30.0);
    telemetry.absolute_altitude_m = Some(80.0);
    telemetry.armed = Some(true);
    telemetry.in_air = Some(true);
    telemetry.landed_state = Some("IN_AIR".into());
    database
        .record_telemetry("follow-agent-session", &telemetry)
        .expect("record ready in-flight telemetry");

    let mut batch = perception_track_batch("follow-track-session", now + 20);
    batch.tracks[0].lifecycle_state = "ACTIVE".into();
    batch.tracks[0].revision = 2;
    batch.tracks[0].age_frames = 4;
    batch.tracks[0].observation_count = 4;
    batch.tracks[0].latest_detection_confidence = 0.92;
    batch.tracks[0].update_reason = "STATE_CHANGED".into();
    database
        .record_perception_track_batch(
            "follow-agent-session",
            "alert-source-drone",
            "follow-perception-stream",
            &batch,
            now + 30,
        )
        .expect("persist active follow track");
    let selection = database
        .select_perception_track(
            &SelectTrackInput {
                drone_id: "alert-source-drone".into(),
                track_session_id: batch.track_session_id.clone(),
                track_id: batch.tracks[0].track_id.clone(),
                actor: "operator".into(),
            },
            now + 40,
        )
        .expect("select exact follow track");

    let evidence = serde_json::json!({
        "estimate": {
            "boresightAlignment": {
                "status": "VERIFIED",
                "reference": "commissioning/a8-gimbal-2026-07-20",
                "errorBoundDeg": 2.5
            }
        }
    });
    let geolocation_id = "follow-geolocation-2";
    {
        let mut connection = database.connection.lock().expect("lock follow fixture");
        let tx = connection.transaction().expect("begin follow fixture");
        tx.execute(
            r#"
            INSERT INTO vehicle_commands (
                id, drone_id, command_type, parameters_json, status, idempotency_key,
                created_at_unix_ms, deadline_at_unix_ms, updated_at_unix_ms,
                accepted_at_unix_ms, started_at_unix_ms, completed_at_unix_ms,
                result_code, result_message
            ) VALUES ('follow-geolocation-command-2', 'alert-source-drone',
                'geolocate_selected_track', '{}', 'succeeded',
                'follow-geolocation-command-2', ?1, ?2, ?2, ?1, ?1, ?2,
                'TRACK_GEOLOCATION_ESTIMATED', 'fixture')
            "#,
            rusqlite::params![now - 400, now - 300],
        )
        .expect("insert follow geolocation command");
        tx.execute(
            r#"
            INSERT INTO perception_track_geolocations (
                id, command_id, selection_id, drone_id, track_session_id, track_id,
                source_id, status, requested_by, requested_at_unix_ms,
                resolved_at_unix_ms, aim_point, assumed_aim_point_height_m,
                assumed_aim_point_height_uncertainty_m, ground_altitude_amsl_m,
                ground_altitude_uncertainty_m, ground_altitude_source,
                ground_altitude_source_version, ground_altitude_resolved_at_unix_ms,
                latitude, longitude, altitude_amsl_m, horizontal_uncertainty_m,
                method, frame_observed_at_unix_ms, rejection_code, rejection_reason,
                evidence_json, created_at_unix_ms, updated_at_unix_ms,
                initial_latitude, initial_longitude, initial_altitude_amsl_m,
                initial_horizontal_uncertainty_m, initial_method, refinement_status,
                terrain_source, terrain_source_version, terrain_vertical_uncertainty_m,
                terrain_iteration_count, terrain_residual_m, range_source,
                filtered_latitude, filtered_longitude, target_velocity_north_mps,
                target_velocity_east_mps, target_speed_mps, target_direction_deg,
                target_velocity_uncertainty_mps, motion_status
            ) VALUES (
                ?1, 'follow-geolocation-command-2', ?2, 'alert-source-drone', ?3, ?4,
                'a8-main', 'SUCCEEDED', 'operator', ?5, ?6, 'TARGET_CENTER',
                0.9, 0.9, 31.2, 1.5, 'test terrain', 'test-v1', ?5,
                51.5002, -0.14, 32.1, 4.0, 'BORESIGHT_TERRAIN_ITERATIVE', ?7,
                '', '', ?8, ?5, ?6, 51.5002, -0.14, 32.1, 6.0,
                'BORESIGHT_GROUND_PLANE', 'CONVERGED', 'test terrain', 'test-v1',
                1.5, 2, 0.2, 'BORESIGHT_INTERSECTION', 51.5002, -0.14,
                1.2, 0.1, 1.204, 4.8, 0.8, 'FILTERED'
            )
            "#,
            rusqlite::params![
                geolocation_id,
                selection.selection_id,
                batch.track_session_id,
                batch.tracks[0].track_id,
                now - 400,
                now - 300,
                now - 200,
                evidence.to_string(),
            ],
        )
        .expect("insert converged filtered target state");
        tx.commit().expect("commit follow fixture");
    }

    let requested = database
        .create_aircraft_follow_session(
            &CreateAircraftFollowSessionInput {
                drone_id: "alert-source-drone".into(),
                geolocation_id: geolocation_id.into(),
                requested_by: "operator".into(),
                reviewed_by: "operator".into(),
                operator_review_note: "Reviewed standoff and geographic envelope".into(),
                standoff_m: 40.0,
                altitude_relative_m: 30.0,
                minimum_altitude_relative_m: 20.0,
                maximum_altitude_relative_m: 40.0,
                maximum_ground_speed_mps: 8.0,
                maximum_acceleration_mps2: 1.5,
                maximum_duration_seconds: 300,
                boundary_center_latitude: 51.5,
                boundary_center_longitude: -0.14,
                boundary_radius_m: 500.0,
                minimum_battery_percent: 30.0,
                minimum_track_confidence: 0.7,
                maximum_geolocation_uncertainty_m: 15.0,
                maximum_velocity_uncertainty_mps: 3.0,
            },
            now,
        )
        .expect("create reviewed follow session");
    assert_eq!(requested.state, "REQUESTED");
    assert_eq!(requested.boresight_error_bound_deg, 2.5);
    let aircraft = database
        .operations_snapshot_for(Some("alert-source-drone"))
        .expect("read follow aircraft watchdog fixture");
    let mut unavailable = aircraft.clone();
    unavailable.telemetry = None;
    assert_eq!(
        super::aircraft_follow::watchdog_aircraft_reason(&unavailable, &requested, now)
            .map(|reason| reason.0),
        Some("TELEMETRY_UNAVAILABLE")
    );
    let mut altitude_unavailable = aircraft.clone();
    altitude_unavailable
        .telemetry
        .as_mut()
        .expect("follow telemetry")
        .relative_altitude_m = None;
    assert_eq!(
        super::aircraft_follow::watchdog_aircraft_reason(&altitude_unavailable, &requested, now,)
            .map(|reason| reason.0),
        Some("ALTITUDE_UNAVAILABLE")
    );
    let mut position_unavailable = aircraft;
    position_unavailable
        .telemetry
        .as_mut()
        .expect("follow telemetry")
        .latitude = None;
    assert_eq!(
        super::aircraft_follow::watchdog_aircraft_reason(&position_unavailable, &requested, now,)
            .map(|reason| reason.0),
        Some("POSITION_UNAVAILABLE")
    );
    let authorized = database
        .authorize_aircraft_follow_session(&requested.id, 3_000, now + 50)
        .expect("authorize follow session");
    assert_eq!(authorized.state, "VALIDATING");
    for (index, state) in ["ACQUIRING", "FOLLOWING"].iter().enumerate() {
        database
            .apply_aircraft_follow_agent_update(&AircraftFollowAgentUpdateInput {
                event_id: format!("follow-agent-{state}"),
                operation_id: "follow-start-operation".into(),
                session_id: requested.id.clone(),
                state: (*state).into(),
                observed_at_unix_ms: now + 60 + index as i64,
                reason_code: String::new(),
                message: format!("Agent {state}"),
                evidence_json: "{}".into(),
            })
            .expect("advance follow Agent state");
    }
    let actions = database
        .aircraft_follow_watchdog_actions(now + 3_051)
        .expect("run follow watchdog");
    assert_eq!(actions.len(), 1);
    assert_eq!(actions[0].reason_code, "OPERATOR_LEASE_EXPIRED");
    assert_eq!(actions[0].session.state, "DEGRADED_HOLD");
    let ended = database
        .apply_aircraft_follow_agent_update(&AircraftFollowAgentUpdateInput {
            event_id: "follow-agent-ended".into(),
            operation_id: "follow-end-operation".into(),
            session_id: requested.id,
            state: "ENDED".into(),
            observed_at_unix_ms: now + 3_060,
            reason_code: "OPERATOR_STOP".into(),
            message: "Operator ended held follow session".into(),
            evidence_json: "{}".into(),
        })
        .expect("end held follow session");
    assert_eq!(ended.state, "ENDED");
    assert!(ended
        .events
        .iter()
        .any(|event| event.reason_code == "OPERATOR_LEASE_EXPIRED"));
    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn new_track_session_reconciles_an_unclosed_previous_session() {
    let (database, path) = test_database();
    let now = unix_time_ms();
    database
        .register_agent(&alert_registration_input("track-reconnect-session", now))
        .expect("register track source agent");
    let previous = perception_track_batch("old-track-session", now + 100);
    database
        .record_perception_track_batch(
            "track-reconnect-session",
            "alert-source-drone",
            "perception-stream-1",
            &previous,
            now + 110,
        )
        .expect("persist old track session");
    // A source timestamp can regress across a stream reset. Reconciliation must
    // still close the old record no earlier than its final observation.
    let replacement = perception_track_batch("new-track-session", now + 50);
    database
        .record_perception_track_batch(
            "track-reconnect-session",
            "alert-source-drone",
            "perception-stream-2",
            &replacement,
            now + 510,
        )
        .expect("start replacement track session");

    let connection = database.connection.lock().expect("lock database");
    let old_track: (String, i64, String) = connection
        .query_row(
            "SELECT lifecycle_state, revision, closure_reason FROM perception_tracks WHERE id = 'atlas:old-track-session:1'",
            [],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
        )
        .expect("read reconciled track");
    assert_eq!(old_track, ("CLOSED".into(), 2, "SESSION_SUPERSEDED".into()));
    let old_session: (Option<i64>, String) = connection
        .query_row(
            "SELECT ended_at_unix_ms, end_reason FROM perception_track_sessions WHERE id = 'old-track-session'",
            [],
            |row| Ok((row.get(0)?, row.get(1)?)),
        )
        .expect("read reconciled session");
    assert_eq!(old_session, (Some(now + 100), "SESSION_SUPERSEDED".into()));
    drop(connection);
    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn counting_and_operator_selection_remain_session_scoped_and_explicit() {
    let (database, path) = test_database();
    let now = unix_time_ms();
    database
        .register_agent(&alert_registration_input("track-operations-session", now))
        .expect("register track operations agent");
    let rule = database
        .upsert_perception_counting_rule(
            &UpsertCountingRuleInput {
                id: Some("count-rule-gate".into()),
                drone_id: "alert-source-drone".into(),
                source_id: "a8-main".into(),
                label: "Gate".into(),
                rule_type: "LINE".into(),
                points: vec![
                    CountingPoint { x: 0.5, y: 0.0 },
                    CountingPoint { x: 0.5, y: 1.0 },
                ],
                class_ids: vec![0],
                enabled: true,
                actor: "operator".into(),
            },
            now + 10,
        )
        .expect("create counting rule");
    assert_eq!(rule.revision, 1);

    let mission = database
        .create_mission(&CreateMissionInput {
            template_type: "WAYPOINT".into(),
            name: "Track counting mission".into(),
            description: String::new(),
            selected_pattern: None,
            params: serde_json::json!({
                "defaultAltitudeMeters": 25,
                "waypoints": [{"latitude": 51.501, "longitude": -0.141}]
            }),
        })
        .expect("create track counting mission");
    let plan = database
        .plan_mission(&mission.id)
        .expect("plan track counting mission");
    let run = database
        .create_mission_run(&mission.id, "alert-source-drone", Some(&plan.id))
        .expect("create track counting mission run");
    database
        .connection
        .lock()
        .expect("lock mission run")
        .execute(
            "UPDATE mission_runs SET status = 'RUNNING', started_at_unix_ms = ?2, updated_at_unix_ms = ?2 WHERE id = ?1",
            rusqlite::params![run.run.id, now + 50],
        )
        .expect("start track counting mission run");

    let mut batch = perception_track_batch("track-session-operations", now + 100);
    batch.current_visible = 1;
    batch.unique_confirmed = 1;
    batch.tracks[0].lifecycle_state = "ACTIVE".into();
    batch.tracks[0].age_frames = 2;
    batch.tracks[0].observation_count = 2;
    batch.tracks[0].update_reason = "STATE_CHANGED".into();
    batch.rule_counts = vec![TrackRuleCountInput {
        rule_id: rule.id.clone(),
        rule_revision: rule.revision,
        rule_type: "LINE".into(),
        line_forward: 0,
        line_reverse: 1,
        polygon_entries: 0,
        polygon_exits: 0,
    }];
    batch.count_events = vec![TrackCountEventInput {
        event_id: "count-event-1".into(),
        rule_id: rule.id.clone(),
        rule_revision: rule.revision,
        track_session_id: batch.track_session_id.clone(),
        track_id: batch.tracks[0].track_id.clone(),
        event_type: "LINE_REVERSE".into(),
        observed_at_unix_ms: now + 100,
        anchor_x: 0.6,
        anchor_y: 0.4,
    }];
    let mut tentative = batch.tracks[0].clone();
    tentative.track_id = "atlas:track-session-operations:tentative".into();
    tentative.lifecycle_state = "TENTATIVE".into();
    tentative.revision = 1;
    tentative.age_frames = 1;
    tentative.observation_count = 1;
    tentative.update_reason = "CREATED".into();
    batch.tracks.push(tentative);
    database
        .record_perception_track_batch(
            "track-operations-session",
            "alert-source-drone",
            "perception-stream-operations",
            &batch,
            now + 110,
        )
        .expect("persist active counted track");

    let counts = database
        .perception_counts("alert-source-drone", "a8-main", None)
        .expect("read perception counts");
    assert_eq!(counts.current_visible_count, 1);
    assert_eq!(counts.unique_session_tracks, 1);
    assert_eq!(counts.mission_run_id.as_deref(), Some(run.run.id.as_str()));
    assert_eq!(counts.unique_mission_tracks, 1);
    assert_eq!(counts.rule_counts.len(), 1);
    assert_eq!(counts.rule_counts[0].line_reverse, 1);
    let rejected = database
        .select_perception_track(
            &SelectTrackInput {
                drone_id: "alert-source-drone".into(),
                track_session_id: batch.track_session_id.clone(),
                track_id: "atlas:track-session-operations:tentative".into(),
                actor: "operator".into(),
            },
            now + 115,
        )
        .expect_err("tentative track must not be operator selectable");
    assert!(rejected.contains("visible confirmed"));

    let selection = database
        .select_perception_track(
            &SelectTrackInput {
                drone_id: "alert-source-drone".into(),
                track_session_id: batch.track_session_id.clone(),
                track_id: batch.tracks[0].track_id.clone(),
                actor: "operator".into(),
            },
            now + 120,
        )
        .expect("select active track");
    assert_eq!(selection.status, "SELECTED");

    database
        .record_telemetry(
            "track-operations-session",
            &alert_telemetry(now + 121, 30.0, false),
        )
        .expect("record in-air telemetry for selected-track geolocation policy");

    let follow_parameters = serde_json::json!({
        "controlContext": {"kind": "mission_override", "missionRunId": run.run.id},
        "controlSessionId": "follow-control-1",
        "gimbalId": 1,
        "sourceId": "a8-main",
        "trackSessionId": batch.track_session_id,
        "trackId": batch.tracks[0].track_id
    });
    database
        .create_vehicle_command(
            "alert-source-drone",
            "gimbal_follow_start",
            &follow_parameters.to_string(),
            None,
            now + 121,
        )
        .expect("create command for exact active operator selection");
    let mut wrong_track = follow_parameters;
    wrong_track["trackId"] = serde_json::json!("atlas:track-session-operations:other");
    let rejected_follow = database
        .create_vehicle_command(
            "alert-source-drone",
            "gimbal_follow_start",
            &wrong_track.to_string(),
            None,
            now + 122,
        )
        .expect_err("unselected track must not control the gimbal");
    assert!(rejected_follow.contains("exact currently selected ACTIVE track"));

    let geolocation_parameters = serde_json::json!({
        "selectionId": selection.selection_id,
        "sourceId": "a8-main",
        "trackSessionId": batch.track_session_id,
        "trackId": batch.tracks[0].track_id,
        "gimbalId": 1,
        "aimPoint": "TARGET_CENTER",
        "groundAltitudeAmslMeters": 31.2,
        "groundAltitudeUncertaintyMeters": 1.5,
        "groundAltitudeSource": "Automatic DEM · Mapzen Terrain Tiles on AWS",
        "groundAltitudeSourceVersion": "aircraft-origin-plane-v1:mapzen-aws-terrain-tiles:terrarium:z12:px256",
        "groundAltitudeResolvedAtUnixMs": now + 122,
        "assumedAimPointHeightMeters": 0.9,
        "assumedAimPointHeightUncertaintyMeters": 0.9,
        "requestedBy": "operator"
    });
    let mut wrong_geolocation_identity = geolocation_parameters.clone();
    wrong_geolocation_identity["selectionId"] = serde_json::json!("different-selection");
    let rejected_geolocation_request = database
        .create_vehicle_command(
            "alert-source-drone",
            "geolocate_selected_track",
            &wrong_geolocation_identity.to_string(),
            None,
            now + 122,
        )
        .expect_err("geolocation must not accept a different selection identity");
    assert!(rejected_geolocation_request.contains("exact currently selected ACTIVE track"));
    let geolocation_command = database
        .create_vehicle_command(
            "alert-source-drone",
            "geolocate_selected_track",
            &geolocation_parameters.to_string(),
            None,
            now + 123,
        )
        .expect("create selected-track geolocation request");
    let pending_geolocations = database
        .perception_track_geolocations(
            geolocation_parameters["trackSessionId"].as_str().unwrap(),
            geolocation_parameters["trackId"].as_str().unwrap(),
            10,
        )
        .expect("read pending selected-track geolocation");
    assert_eq!(pending_geolocations[0].status, "REQUESTED");
    assert_eq!(pending_geolocations[0].command_id, geolocation_command.id);

    let link_id: String = database
        .connection
        .lock()
        .expect("lock geolocation command link")
        .query_row(
            "SELECT id FROM communication_links WHERE status = 'connected' ORDER BY started_at_unix_ms DESC LIMIT 1",
            [],
            |row| row.get(0),
        )
        .expect("read geolocation command link");
    for (index, state) in ["accepted", "executing"].iter().enumerate() {
        database
            .apply_vehicle_command_update(&VehicleCommandUpdateInput {
                event_id: format!("geolocation-{state}"),
                command_id: geolocation_command.id.clone(),
                event_type: (*state).into(),
                next_state: Some((*state).into()),
                communication_link_id: link_id.clone(),
                occurred_at_unix_ms: now + 124 + index as i64,
                progress_percent: None,
                result_code: String::new(),
                message: format!("geolocation {state}"),
                evidence_json: None,
            })
            .expect("advance selected-track geolocation command");
    }
    let evidence = serde_json::json!({
        "schemaVersion": 1,
        "status": "SUCCEEDED",
        "selectionId": geolocation_parameters["selectionId"],
        "sourceId": geolocation_parameters["sourceId"],
        "trackSessionId": geolocation_parameters["trackSessionId"],
        "trackId": geolocation_parameters["trackId"],
        "estimatedAtUnixMs": now + 126,
        "estimate": {
            "method": "BORESIGHT_GROUND_PLANE",
            "frameTime": {"observedAtUnixNs": (now + 120) * 1_000_000},
            "origin": {"latitudeDeg": 51.5, "longitudeDeg": -0.140456, "altitudeMeters": 45.8},
            "worldDirectionNed": {"x": 0.7071067812, "y": 0.0, "z": 0.7071067812},
            "intersection": {"latitudeDeg": 51.500123, "longitudeDeg": -0.140456, "altitudeMeters": 32.1},
            "uncertainty": {"horizontalRadiusMeters": 18.4}
        }
    });
    database
        .apply_vehicle_command_update(&VehicleCommandUpdateInput {
            event_id: "geolocation-succeeded".into(),
            command_id: geolocation_command.id.clone(),
            event_type: "succeeded".into(),
            next_state: Some("succeeded".into()),
            communication_link_id: link_id.clone(),
            occurred_at_unix_ms: now + 126,
            progress_percent: None,
            result_code: "TRACK_GEOLOCATION_ESTIMATED".into(),
            message: "Selected-track boresight coordinate estimated".into(),
            evidence_json: Some(evidence.to_string()),
        })
        .expect("persist selected-track geolocation result");
    let completed_geolocations = database
        .perception_track_geolocations(
            geolocation_parameters["trackSessionId"].as_str().unwrap(),
            geolocation_parameters["trackId"].as_str().unwrap(),
            10,
        )
        .expect("read completed selected-track geolocation");
    assert_eq!(completed_geolocations[0].status, "SUCCEEDED");
    assert_eq!(completed_geolocations[0].latitude, Some(51.500123));
    assert_eq!(
        completed_geolocations[0].horizontal_uncertainty_m,
        Some(18.4)
    );
    assert_eq!(
        completed_geolocations[0].ground_altitude_source,
        "Automatic DEM · Mapzen Terrain Tiles on AWS"
    );
    let refined = database
        .refine_perception_track_geolocation(
            &TerrainRefinementInput {
                geolocation_id: completed_geolocations[0].id.clone(),
                terrain_source: "Automatic target-area DEM · test terrain".into(),
                terrain_source_version: "target-ray-iterative-v1:test".into(),
                terrain_vertical_uncertainty_m: 1.5,
                convergence_threshold_m: 0.75,
                samples: vec![TerrainSampleInput {
                    latitude: 51.500123,
                    longitude: -0.140456,
                    altitude_amsl_m: 31.2,
                }],
            },
            now + 127,
        )
        .expect("refine persisted coordinate against target terrain");
    assert_eq!(refined.method, "BORESIGHT_TERRAIN_ITERATIVE");
    assert_eq!(refined.refinement_status, "CONVERGED");
    assert_eq!(refined.terrain_iteration_count, 1);
    assert_eq!(refined.motion_status, "INSUFFICIENT_HISTORY");
    assert_eq!(refined.initial_latitude, Some(51.500123));
    let operational_targets = database
        .operational_track_geolocations(10)
        .expect("read latest operational track target");
    assert_eq!(operational_targets.len(), 1);
    assert_eq!(operational_targets[0].geolocation.id, refined.id);
    assert_eq!(operational_targets[0].lifecycle_state, "ACTIVE");

    let rejected_command = database
        .create_vehicle_command(
            "alert-source-drone",
            "geolocate_selected_track",
            &geolocation_parameters.to_string(),
            None,
            now + 128,
        )
        .expect("create geolocation request that Agent will reject");
    for (index, state) in ["accepted", "executing"].iter().enumerate() {
        database
            .apply_vehicle_command_update(&VehicleCommandUpdateInput {
                event_id: format!("rejected-geolocation-{state}"),
                command_id: rejected_command.id.clone(),
                event_type: (*state).into(),
                next_state: Some((*state).into()),
                communication_link_id: link_id.clone(),
                occurred_at_unix_ms: now + 128 + index as i64,
                progress_percent: None,
                result_code: String::new(),
                message: format!("geolocation {state}"),
                evidence_json: None,
            })
            .expect("advance rejected geolocation command");
    }
    database
        .apply_vehicle_command_update(&VehicleCommandUpdateInput {
            event_id: "geolocation-rejected".into(),
            command_id: rejected_command.id,
            event_type: "failed".into(),
            next_state: Some("failed".into()),
            communication_link_id: link_id,
            occurred_at_unix_ms: now + 130,
            progress_percent: None,
            result_code: "GEOLOCATION_ESTIMATE_REJECTED".into(),
            message: "aim point is not centred within normalized tolerance 0.040".into(),
            evidence_json: Some(
                serde_json::json!({
                    "schemaVersion": 1,
                    "status": "REJECTED",
                    "selectionId": geolocation_parameters["selectionId"],
                    "sourceId": geolocation_parameters["sourceId"],
                    "trackSessionId": geolocation_parameters["trackSessionId"],
                    "trackId": geolocation_parameters["trackId"],
                    "rejectionCode": "GEOLOCATION_ESTIMATE_REJECTED",
                    "rejectionReason": "aim point is not centred within normalized tolerance 0.040"
                })
                .to_string(),
            ),
        })
        .expect("persist explicit selected-track geolocation rejection");
    let rejected_geolocations = database
        .perception_track_geolocations(
            geolocation_parameters["trackSessionId"].as_str().unwrap(),
            geolocation_parameters["trackId"].as_str().unwrap(),
            10,
        )
        .expect("read rejected selected-track geolocation");
    assert_eq!(rejected_geolocations[0].status, "REJECTED");
    assert_eq!(
        rejected_geolocations[0].rejection_code,
        "GEOLOCATION_ESTIMATE_REJECTED"
    );
    assert!(rejected_geolocations[0]
        .rejection_reason
        .contains("not centred"));

    batch.session_started = false;
    batch.current_visible = 0;
    batch.count_events.clear();
    batch.tracks[0].revision = 2;
    batch.tracks[0].age_frames = 3;
    batch.tracks[0].lifecycle_state = "TEMPORARILY_OCCLUDED".into();
    batch.tracks[0].update_reason = "STATE_CHANGED".into();
    batch.observed_at_unix_ms = now + 200;
    database
        .record_perception_track_batch(
            "track-operations-session",
            "alert-source-drone",
            "perception-stream-operations",
            &batch,
            now + 210,
        )
        .expect("persist selected short occlusion");
    assert_eq!(
        database
            .perception_track_selection("alert-source-drone")
            .expect("read occluded selection")
            .expect("selection retained")
            .status,
        "OCCLUDED"
    );

    batch.current_visible = 1;
    batch.tracks[0].revision = 3;
    batch.tracks[0].age_frames = 4;
    batch.tracks[0].observation_count = 3;
    batch.tracks[0].last_observed_at_unix_ms = now + 300;
    batch.tracks[0].lifecycle_state = "ACTIVE".into();
    batch.tracks[0].update_reason = "REACQUIRED".into();
    batch.observed_at_unix_ms = now + 300;
    database
        .record_perception_track_batch(
            "track-operations-session",
            "alert-source-drone",
            "perception-stream-operations",
            &batch,
            now + 310,
        )
        .expect("persist selected reacquisition");
    assert_eq!(
        database
            .perception_track_selection("alert-source-drone")
            .expect("read reacquired selection")
            .expect("selection retained")
            .status,
        "SELECTED"
    );

    database
        .annotate_perception_track(
            &TrackAnnotationInput {
                selection_id: selection.selection_id.clone(),
                annotation_type: "NOTE".into(),
                body: "Red jacket near the gate".into(),
                evidence_recording_session_id: None,
                actor: "operator".into(),
            },
            now + 320,
        )
        .expect("annotate selected track");
    let evidence_root = path.with_extension("track-operation-evidence");
    let recording_input = super::recordings::test_create_recording_input(
        "alert-source-drone",
        &evidence_root.to_string_lossy(),
        now + 321,
    );
    let recording = database
        .create_evidence_recording(&recording_input)
        .expect("request track evidence recording");
    database
        .mark_evidence_recording_running(&recording.id, now + 322)
        .expect("start track evidence recording");
    let evidence_marker = database
        .annotate_perception_track(
            &TrackAnnotationInput {
                selection_id: selection.selection_id.clone(),
                annotation_type: "EVIDENCE_MARKER".into(),
                body: "Operator evidence marker".into(),
                evidence_recording_session_id: Some(recording.id),
                actor: "operator".into(),
            },
            now + 323,
        )
        .expect("mark selected track evidence");
    let queued_clip = database
        .queue_evidence_event_clip(
            &QueueEvidenceEventClipInput {
                evidence_marker_annotation_id: evidence_marker.id.clone(),
                pre_roll_seconds: 8,
                post_roll_seconds: 12,
                actor: "operator".into(),
            },
            now + 324,
        )
        .expect("queue marker-linked event clip");
    assert_eq!(queued_clip.status, "PENDING");
    assert_eq!(queued_clip.track_id, Some(selection.track_id.clone()));
    assert_eq!(
        queued_clip.requested_start_at_unix_ms,
        Some(now + 323 - 8_000)
    );
    assert_eq!(
        queued_clip.requested_end_at_unix_ms,
        Some(now + 323 + 12_000)
    );
    let duplicate_clip = database
        .queue_evidence_event_clip(
            &QueueEvidenceEventClipInput {
                evidence_marker_annotation_id: evidence_marker.id,
                pre_roll_seconds: 1,
                post_roll_seconds: 1,
                actor: "operator".into(),
            },
            now + 325,
        )
        .expect("reuse the marker's durable clip request");
    assert_eq!(duplicate_clip.id, queued_clip.id);

    batch.current_visible = 0;
    batch.tracks[0].revision = 4;
    batch.tracks[0].age_frames = 12;
    batch.tracks[0].lifecycle_state = "LOST".into();
    batch.tracks[0].update_reason = "STATE_CHANGED".into();
    batch.observed_at_unix_ms = now + 1_300;
    database
        .record_perception_track_batch(
            "track-operations-session",
            "alert-source-drone",
            "perception-stream-operations",
            &batch,
            now + 1_310,
        )
        .expect("persist selected lost result");
    let lost = database
        .perception_track_selection("alert-source-drone")
        .expect("read lost selection")
        .expect("lost result remains explicit");
    assert_eq!(lost.status, "LOST");
    assert_eq!(lost.lifecycle_state, "LOST");
    assert_eq!(lost.annotation_count, 2);
    let lost_last_observed = lost.last_observed_at_unix_ms;

    batch.current_visible = 1;
    batch.tracks[0].revision = 5;
    batch.tracks[0].age_frames = 13;
    batch.tracks[0].observation_count = 4;
    batch.tracks[0].last_observed_at_unix_ms = now + 1_400;
    batch.tracks[0].lifecycle_state = "ACTIVE".into();
    batch.tracks[0].update_reason = "REACQUIRED".into();
    batch.observed_at_unix_ms = now + 1_400;
    database
        .record_perception_track_batch(
            "track-operations-session",
            "alert-source-drone",
            "perception-stream-operations",
            &batch,
            now + 1_410,
        )
        .expect("persist backend reacquisition after lost result");
    let terminal = database
        .perception_track_selection("alert-source-drone")
        .expect("read terminal selection")
        .expect("terminal result retained");
    assert_eq!(terminal.status, "LOST");
    assert_eq!(terminal.lifecycle_state, "LOST");
    assert_eq!(terminal.last_observed_at_unix_ms, lost_last_observed);

    let samples = database
        .perception_track_samples(&batch.track_session_id, &batch.tracks[0].track_id, 3)
        .expect("read bounded track samples");
    assert_eq!(samples.len(), 3);
    assert!(samples[0].revision > samples[1].revision);

    database
        .clear_perception_track_selection("alert-source-drone", "operator", now + 1_500)
        .expect("clear terminal selection result");
    assert!(database
        .perception_track_selection("alert-source-drone")
        .expect("read cleared selection")
        .is_none());
    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn archive_is_safe_idempotent_and_blocks_registration_until_restore() {
    let (database, path) = test_database();
    let now = unix_time_ms();
    let input = RegistrationInput {
        session_id: "archive-session-1".into(),
        installation_id: "archive-agent-1".into(),
        agent_version: "0.1.0".into(),
        protocol_version: "1".into(),
        device_profile_json: r#"{"hostname":"pi"}"#.into(),
        capabilities_json: r#"["registration","heartbeat"]"#.into(),
        drone_id: "archive-drone-1".into(),
        drone_name: "Atlas Archive".into(),
        flight_controller_uid: "archive-fc-1".into(),
        serial_number: "archive-serial-1".into(),
        vehicle_type: "multicopter".into(),
        flight_controller_transport: "serial".into(),
        endpoint_description: "/dev/serial0".into(),
        baud_rate: 921_600,
        mavlink_system_id: 1,
        mavlink_component_id: 1,
        remote_address: "192.168.144.168:50000".into(),
        observed_at_unix_ms: now,
    };
    database.register_agent(&input).expect("register aircraft");

    let error = database
        .archive_drone(&input.drone_id, "retired from service")
        .expect_err("freshly connected aircraft must not archive");
    assert!(error.contains("disconnect the aircraft"));

    database
        .close_session(&input.session_id, "operator disconnected", now + 1)
        .expect("close aircraft session");
    let archived = database
        .archive_drone(&input.drone_id, "retired from service")
        .expect("archive disconnected aircraft");
    assert_eq!(archived.vehicle_status.as_deref(), Some("archived"));
    assert_eq!(archived.connection_status, "disconnected");
    database
        .archive_drone(&input.drone_id, "duplicate request")
        .expect("archive is idempotent");
    assert!(database
        .fleet_snapshot(false)
        .expect("read operational fleet")
        .aircraft
        .is_empty());
    assert_eq!(
        database
            .fleet_snapshot(true)
            .expect("read fleet including archived")
            .aircraft
            .len(),
        1
    );

    let mut reconnect = input.clone();
    reconnect.session_id = "archive-session-rejected".into();
    reconnect.observed_at_unix_ms = now + 2;
    let error = database
        .register_agent(&reconnect)
        .expect_err("archived aircraft registration must fail");
    assert!(error.starts_with(super::ARCHIVED_REGISTRATION_ERROR));
    let current_binding_count: i64 = database
        .connection
        .lock()
        .expect("lock database")
        .query_row(
            "SELECT count(*) FROM vehicle_agent_bindings WHERE drone_id = ?1 AND status IN ('active', 'suspended')",
            [&input.drone_id],
            |row| row.get(0),
        )
        .expect("count current bindings");
    assert_eq!(current_binding_count, 0);

    let restored = database
        .restore_drone(&input.drone_id)
        .expect("restore aircraft");
    assert_eq!(restored.vehicle_status.as_deref(), Some("active"));
    assert_eq!(restored.connection_status, "disconnected");
    database
        .restore_drone(&input.drone_id)
        .expect("restore is idempotent");

    reconnect.session_id = "archive-session-restored".into();
    reconnect.observed_at_unix_ms = now + 3;
    database
        .register_agent(&reconnect)
        .expect("restored aircraft can register");
    let lifecycle_counts: (i64, i64, i64) = database
        .connection
        .lock()
        .expect("lock database")
        .query_row(
            r#"
            SELECT
                sum(event_type = 'archived'),
                sum(event_type = 'restored'),
                sum(event_type = 'archived_reconnect_rejected')
            FROM drone_lifecycle_events WHERE drone_id = ?1
            "#,
            [&input.drone_id],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
        )
        .expect("read lifecycle event counts");
    assert_eq!(lifecycle_counts, (1, 1, 1));

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn manual_incident_creation_sets_source_identity_and_initial_event() {
    let (database, path) = test_database();
    let created = database
        .create_incident(&sample_incident_input())
        .expect("create manual incident");

    assert_eq!(created.incident.source_type, "MANUAL");
    assert_eq!(created.incident.source_system, "ATLAS_NATIVE");
    assert_eq!(created.incident.external_id, None);
    assert_eq!(created.incident.priority, "HIGH");
    assert_eq!(created.incident.status, "OPEN");
    assert_eq!(created.incident.revision, 1);
    assert_eq!(created.incident.location_revision, 1);
    assert_eq!(created.incident.source_payload, None);
    assert_eq!(created.events.len(), 1);
    assert_eq!(created.events[0].sequence, 0);
    assert_eq!(created.events[0].event_type, "created");
    assert_eq!(created.events[0].state, "OPEN");
    assert_eq!(created.events[0].source, "atlas_native");
    assert_eq!(created.events[0].details["revision"], 1);

    let open_incidents = database.incidents(false, 100).expect("list open incidents");
    assert_eq!(open_incidents.len(), 1);
    assert_eq!(open_incidents[0].id, created.incident.id);

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn manual_incident_survives_native_restart_without_backend_or_internet() {
    let path = test_database_path();
    let incident_id = {
        let database = LocalDatabase::open_path(path.clone()).expect("open local database");
        database
            .create_incident(&sample_incident_input())
            .expect("create local manual incident")
            .incident
            .id
    };

    let reopened = LocalDatabase::open_path(path.clone()).expect("reopen native database");
    let detail = reopened
        .incident(&incident_id)
        .expect("manual incident must survive restart");
    assert_eq!(detail.incident.source_type, "MANUAL");
    assert_eq!(detail.incident.revision, 1);
    assert_eq!(detail.events.len(), 1);
    assert_eq!(detail.events[0].event_type, "created");

    drop(reopened);
    remove_sqlite_files(&path);
}

#[test]
fn incident_updates_atomically_append_events_and_track_location_revision() {
    let (database, path) = test_database();
    let created = database
        .create_incident(&sample_incident_input())
        .expect("create incident");
    let moved = database
        .update_incident(
            &created.incident.id,
            &UpdateIncidentInput {
                expected_revision: 1,
                incident_type: "Missing person".into(),
                priority: "critical".into(),
                status: "active".into(),
                summary: "Search requested near the north trail".into(),
                description: "Last observed beside the wooded trail.".into(),
                latitude: Some(51.501),
                longitude: Some(-0.142),
                address: "North trail entrance".into(),
                area: "North sector".into(),
                occurred_at_unix_ms: Some(1_700_000_000_000),
            },
        )
        .expect("update incident location");

    assert_eq!(moved.incident.revision, 2);
    assert_eq!(moved.incident.location_revision, 2);
    assert_eq!(moved.incident.status, "ACTIVE");
    assert_eq!(moved.incident.priority, "CRITICAL");
    assert_eq!(moved.events.len(), 2);
    assert_eq!(moved.events[1].sequence, 1);
    assert_eq!(moved.events[1].event_type, "updated");
    assert_eq!(moved.events[1].details["previousRevision"], 1);
    assert_eq!(moved.events[1].details["locationRevision"], 2);
    assert!(moved.events[1].details["changedFields"]
        .as_array()
        .expect("changed fields array")
        .iter()
        .any(|field| field == "latitude"));

    let resolved = database
        .update_incident(
            &created.incident.id,
            &UpdateIncidentInput {
                expected_revision: 2,
                incident_type: moved.incident.incident_type.clone(),
                priority: moved.incident.priority.clone(),
                status: "RESOLVED".into(),
                summary: moved.incident.summary.clone(),
                description: moved.incident.description.clone(),
                latitude: moved.incident.latitude,
                longitude: moved.incident.longitude,
                address: moved.incident.address.clone(),
                area: moved.incident.area.clone(),
                occurred_at_unix_ms: moved.incident.occurred_at_unix_ms,
            },
        )
        .expect("resolve incident");
    assert_eq!(resolved.incident.revision, 3);
    assert_eq!(resolved.incident.location_revision, 2);
    assert_eq!(resolved.events.len(), 3);
    assert_eq!(resolved.events[2].event_type, "status_changed");
    assert_eq!(resolved.events[2].state, "RESOLVED");

    assert!(database
        .incidents(false, 100)
        .expect("list active incidents")
        .is_empty());
    assert_eq!(
        database
            .incidents(true, 100)
            .expect("list all incidents")
            .len(),
        1
    );

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn incident_update_rejects_stale_revision_without_partial_changes() {
    let (database, path) = test_database();
    let created = database
        .create_incident(&sample_incident_input())
        .expect("create incident");
    let input = UpdateIncidentInput {
        expected_revision: 1,
        incident_type: created.incident.incident_type.clone(),
        priority: created.incident.priority.clone(),
        status: "ACTIVE".into(),
        summary: created.incident.summary.clone(),
        description: created.incident.description.clone(),
        latitude: created.incident.latitude,
        longitude: created.incident.longitude,
        address: created.incident.address.clone(),
        area: created.incident.area.clone(),
        occurred_at_unix_ms: created.incident.occurred_at_unix_ms,
    };
    database
        .update_incident(&created.incident.id, &input)
        .expect("activate incident");

    let error = database
        .update_incident(
            &created.incident.id,
            &UpdateIncidentInput {
                summary: "Stale summary must not be written".into(),
                ..input
            },
        )
        .expect_err("stale update must fail");
    assert!(
        error.contains("expected revision 1, current revision 2"),
        "{error}"
    );

    let unchanged = database
        .incident(&created.incident.id)
        .expect("read incident after rejected update");
    assert_eq!(unchanged.incident.revision, 2);
    assert_eq!(unchanged.incident.summary, created.incident.summary);
    assert_eq!(unchanged.events.len(), 2);

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn incident_validation_requires_complete_coordinates_before_writing() {
    let (database, path) = test_database();
    let error = database
        .create_incident(&CreateIncidentInput {
            longitude: None,
            ..sample_incident_input()
        })
        .expect_err("partial coordinates must fail");
    assert_eq!(error, "latitude and longitude must be provided together");
    assert!(database
        .incidents(true, 100)
        .expect("list incidents after rejected create")
        .is_empty());

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn incident_response_preparation_atomically_creates_plan_assignment_and_event() {
    let (database, path) = test_database();
    insert_test_aircraft(&database, "response-drone-1", "Response One", "active");
    insert_test_aircraft(&database, "response-drone-2", "Response Two", "active");
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create incident");

    let prepared = database
        .prepare_incident_response(
            &incident.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                drone_id: "response-drone-1".into(),
                staging_latitude: 51.501,
                staging_longitude: -0.141,
                altitude_meters: 35.0,
                speed_mps: 6.0,
                arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
                point_gimbal_at_incident: true,
                incident_target_altitude_amsl_meters: Some(42.0),
            },
        )
        .expect("prepare incident response");

    assert_eq!(prepared.incident.id, incident.incident.id);
    assert_eq!(prepared.assignment.status, "PREPARED");
    assert_eq!(prepared.assignment.drone_id, "response-drone-1");
    assert_eq!(prepared.mission.status, "READY");
    assert_eq!(
        prepared.mission.generated_plan_id,
        Some(prepared.plan.id.clone())
    );
    assert_eq!(prepared.plan.status, "READY");
    assert_eq!(prepared.plan.generated_waypoints.len(), 1);
    assert_eq!(prepared.plan.generated_waypoints[0].latitude, 51.501);
    assert_eq!(prepared.plan.generated_waypoints[0].altitude_meters, 35.0);
    assert_eq!(prepared.plan.generated_waypoints[0].speed_mps, Some(6.0));
    let runtime_actions: Vec<_> = prepared
        .plan
        .actions
        .iter()
        .filter(|action| {
            matches!(
                action.action_type.as_str(),
                "HOLD_AT_ARRIVAL" | "POINT_GIMBAL_AT_INCIDENT"
            )
        })
        .collect();
    assert_eq!(runtime_actions.len(), 2);
    assert_eq!(runtime_actions[0].action_type, "HOLD_AT_ARRIVAL");
    assert_eq!(runtime_actions[0].params["maxAttempts"], 3);
    assert_eq!(runtime_actions[0].params["timeoutMs"], 20_000);
    assert_eq!(runtime_actions[0].params["retryInitialDelayMs"], 2_000);
    assert_eq!(runtime_actions[0].params["retryBackoffMultiplier"], 2.0);
    assert_eq!(
        runtime_actions[0].params["failurePolicy"],
        "RETURN_TO_LAUNCH"
    );
    assert_eq!(runtime_actions[1].action_type, "POINT_GIMBAL_AT_INCIDENT");
    assert_eq!(
        runtime_actions[1].params["failurePolicy"],
        "SKIP_OPTIONAL_AND_NOTIFY"
    );
    assert_eq!(
        prepared.plan.metadata["incidentResponse"]["incidentRevision"],
        incident.incident.revision
    );

    let detail = database
        .incident(&incident.incident.id)
        .expect("read prepared incident");
    assert_eq!(detail.assignments.len(), 1);
    assert_eq!(
        detail.events.last().unwrap().event_type,
        "response_prepared"
    );

    let wrong_aircraft_error = database
        .create_mission_run(
            &prepared.mission.id,
            "response-drone-2",
            Some(&prepared.plan.id),
        )
        .expect_err("response plan must remain bound to reviewed aircraft");
    assert!(wrong_aircraft_error.contains("assigned to aircraft response-drone-1"));

    let dispatch = database
        .create_mission_run(
            &prepared.mission.id,
            "response-drone-1",
            Some(&prepared.plan.id),
        )
        .expect("create assigned response run");
    assert_eq!(dispatch.run.actions.len(), 2);
    assert!(dispatch
        .run
        .actions
        .iter()
        .all(|action| action.state == "REQUESTED" && action.events.len() == 1));
    assert!(dispatch.run.actions.iter().all(|action| {
        action.timeout_ms == 20_000
            && action.retry_initial_delay_ms == 2_000
            && action.retry_backoff_multiplier == 2.0
            && action.attempt_deadline_at_unix_ms.is_none()
            && action.next_attempt_at_unix_ms.is_none()
    }));
    let reconciliation = database
        .mission_reconciliation("response-drone-1")
        .expect("build durable Agent reconciliation")
        .expect("unfinished response must reconcile");
    assert_eq!(reconciliation.run.id, dispatch.run.id);
    assert_eq!(reconciliation.run.status, "UPLOADING");
    assert!(reconciliation.mission_plan_json.contains("HOLD_AT_ARRIVAL"));
    let linked = database
        .incident(&incident.incident.id)
        .expect("read linked incident");
    assert_eq!(linked.assignments[0].status, "UPLOADING");
    assert_eq!(
        linked.assignments[0].mission_run_id.as_deref(),
        Some(dispatch.run.id.as_str())
    );
    let close_error = database
        .update_incident(
            &incident.incident.id,
            &UpdateIncidentInput {
                expected_revision: incident.incident.revision,
                incident_type: incident.incident.incident_type.clone(),
                priority: incident.incident.priority.clone(),
                status: "RESOLVED".into(),
                summary: incident.incident.summary.clone(),
                description: incident.incident.description.clone(),
                latitude: incident.incident.latitude,
                longitude: incident.incident.longitude,
                address: incident.incident.address.clone(),
                area: incident.incident.area.clone(),
                occurred_at_unix_ms: incident.incident.occurred_at_unix_ms,
            },
        )
        .expect_err("unfinished response run must block incident closure");
    assert!(close_error.contains("response mission run is unfinished"));

    database
        .apply_mission_run_update(&MissionRunUpdateInput {
            event_id: "response-upload-failed".into(),
            operation_id: dispatch.operation_id,
            mission_run_id: dispatch.run.id,
            event_type: "delivery_failed".into(),
            run_state: "FAILED".into(),
            occurred_at_unix_ms: unix_time_ms(),
            progress_percent: None,
            current_waypoint: None,
            total_waypoints: Some(1),
            error_code: "TEST_FAILURE".into(),
            message: "Forced upload failure".into(),
            evidence_json: None,
        })
        .expect("finish response run");
    let finished = database
        .incident(&incident.incident.id)
        .expect("read finished assignment");
    assert_eq!(finished.assignments[0].status, "FAILED");
    assert!(finished.assignments[0].ended_at_unix_ms.is_some());

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn offset_observe_prepares_hold_gimbal_and_reviewed_geometry_atomically() {
    let (database, path) = test_database();
    insert_test_aircraft(
        &database,
        "offset-observe-drone",
        "Offset Observe",
        "active",
    );
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create offset incident");
    let input = ExpandedPrepareIncidentResponseInput {
        expected_incident_revision: incident.incident.revision,
        drone_id: "offset-observe-drone".into(),
        geometry: IncidentResponseGeometryInput::OffsetObserve {
            observation_latitude: 51.501,
            observation_longitude: -0.141,
            altitude_meters: 35.0,
            speed_mps: 6.0,
        },
        arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
        incident_target_altitude_amsl_meters: Some(42.0),
        building_horizontal_clearance_meters: 10.0,
        building_vertical_clearance_meters: 5.0,
        known_building_override_reason: Some(
            "No local OS extract is configured in this test environment".into(),
        ),
    };
    let prepared = database
        .prepare_expanded_incident_response_with_dataset(&incident.incident.id, &input, None)
        .expect("prepare offset observe");
    assert_eq!(prepared.plan.generated_waypoints.len(), 1);
    assert_eq!(
        prepared.plan.metadata["incidentResponse"]["responsePattern"],
        "OFFSET_OBSERVE"
    );
    assert_eq!(
        prepared.plan.metadata["incidentResponse"]["knownBuildingAssessment"]["status"],
        "DATA_UNAVAILABLE"
    );
    let action_types = prepared
        .plan
        .actions
        .iter()
        .map(|action| action.action_type.as_str())
        .collect::<Vec<_>>();
    assert!(action_types.contains(&"HOLD_AT_ARRIVAL"));
    assert!(action_types.contains(&"POINT_GIMBAL_AT_INCIDENT"));
    assert!(action_types.contains(&"SET_GIMBAL_ORIENTATION"));
    let detail = database
        .incident(&incident.incident.id)
        .expect("read linked offset response");
    assert_eq!(detail.assignments.len(), 1);
    assert_eq!(detail.assignments[0].mission_id, Some(prepared.mission.id));

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn hold_at_staging_pauses_without_claiming_the_aircraft_is_on_scene() {
    let (database, path) = test_database();
    insert_test_aircraft(&database, "staging-drone", "Staging Aircraft", "active");
    let now = unix_time_ms();
    database
        .connection
        .lock()
        .expect("lock staging telemetry")
        .execute(
            "INSERT INTO vehicle_telemetry_current (drone_id, source, agent_observed_at_unix_ms, received_at_unix_ms, relative_altitude_m, latitude, longitude, absolute_altitude_m, home_position_json) VALUES ('staging-drone', 'test', ?1, ?1, 0, 51.5, -0.142, 20, ?2)",
            rusqlite::params![now, r#"{"absoluteAltitudeM":20}"#],
        )
        .expect("insert staging telemetry");
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create staging incident");
    let prepared = database
        .prepare_expanded_incident_response_with_dataset(
            &incident.incident.id,
            &ExpandedPrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                drone_id: "staging-drone".into(),
                geometry: IncidentResponseGeometryInput::HoldAtStaging {
                    staging_latitude: 51.501,
                    staging_longitude: -0.141,
                    altitude_meters: 35.0,
                    speed_mps: 6.0,
                },
                arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
                incident_target_altitude_amsl_meters: None,
                building_horizontal_clearance_meters: 10.0,
                building_vertical_clearance_meters: 5.0,
                known_building_override_reason: Some(
                    "No local OS extract is configured in this test environment".into(),
                ),
            },
            None,
        )
        .expect("prepare Hold at staging");
    assert_eq!(
        prepared.plan.metadata["incidentResponse"]["responsePattern"],
        "HOLD_AT_STAGING"
    );
    let hold = prepared
        .plan
        .actions
        .iter()
        .find(|action| action.action_type == "HOLD_AT_ARRIVAL")
        .expect("staging plan requires Hold");
    assert_eq!(hold.params["waitForOperatorDecision"], true);
    assert!(!prepared
        .plan
        .actions
        .iter()
        .any(|action| action.action_type == "POINT_GIMBAL_AT_INCIDENT"));

    let dispatch = database
        .create_mission_run(
            &prepared.mission.id,
            "staging-drone",
            Some(&prepared.plan.id),
        )
        .expect("create staging run");
    let action_sequence = dispatch.run.actions[0].action_sequence;
    let ready = database
        .apply_mission_run_update(&MissionRunUpdateInput {
            event_id: "staging-uploaded".into(),
            operation_id: dispatch.operation_id,
            mission_run_id: dispatch.run.id.clone(),
            event_type: "uploaded".into(),
            run_state: "READY".into(),
            occurred_at_unix_ms: unix_time_ms(),
            progress_percent: Some(100.0),
            current_waypoint: None,
            total_waypoints: Some(1),
            error_code: String::new(),
            message: "Uploaded".into(),
            evidence_json: None,
        })
        .expect("mark staging run ready");
    let start = database
        .record_mission_operation_requested(&ready.id, "start")
        .expect("request staging start");
    database
        .apply_mission_run_update(&MissionRunUpdateInput {
            event_id: "staging-started".into(),
            operation_id: start.operation_id,
            mission_run_id: ready.id.clone(),
            event_type: "started".into(),
            run_state: "RUNNING".into(),
            occurred_at_unix_ms: unix_time_ms(),
            progress_percent: Some(0.0),
            current_waypoint: Some(0),
            total_waypoints: Some(1),
            error_code: String::new(),
            message: "Started".into(),
            evidence_json: None,
        })
        .expect("start staging run");
    database
        .apply_mission_action_update(&MissionActionUpdateInput {
            event_id: "staging-hold-running".into(),
            mission_run_id: ready.id.clone(),
            action_sequence,
            action_type: "HOLD_AT_ARRIVAL".into(),
            state: "RUNNING".into(),
            attempt: 1,
            failure_policy: "RETURN_TO_LAUNCH".into(),
            occurred_at_unix_ms: unix_time_ms(),
            error_code: String::new(),
            message: "Executing staging Hold".into(),
            evidence_json: None,
        })
        .expect("start staging Hold");
    database
        .apply_mission_action_update(&MissionActionUpdateInput {
            event_id: "staging-hold-succeeded".into(),
            mission_run_id: ready.id.clone(),
            action_sequence,
            action_type: "HOLD_AT_ARRIVAL".into(),
            state: "SUCCEEDED".into(),
            attempt: 1,
            failure_policy: "RETURN_TO_LAUNCH".into(),
            occurred_at_unix_ms: unix_time_ms(),
            error_code: String::new(),
            message: "Staging Hold acknowledged".into(),
            evidence_json: Some(r#"{"acknowledged":true}"#.into()),
        })
        .expect("acknowledge staging Hold");
    database
        .apply_mission_run_update(&MissionRunUpdateInput {
            event_id: "staging-paused".into(),
            operation_id: String::new(),
            mission_run_id: ready.id,
            event_type: "paused".into(),
            run_state: "PAUSED".into(),
            occurred_at_unix_ms: unix_time_ms(),
            progress_percent: Some(100.0),
            current_waypoint: Some(1),
            total_waypoints: Some(1),
            error_code: String::new(),
            message: "Awaiting operator decision".into(),
            evidence_json: None,
        })
        .expect("pause staged run");

    let staged = database
        .incident(&incident.incident.id)
        .expect("read staged response");
    assert_eq!(staged.assignments[0].status, "STAGED");
    assert_eq!(staged.assignments[0].on_scene_at_unix_ms, None);
    assert!(staged
        .events
        .iter()
        .any(|event| event.event_type == "response_staged"));
    assert!(!staged
        .events
        .iter()
        .any(|event| event.event_type == "response_on_scene"));

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn aircraft_suitability_excludes_reserved_aircraft_and_ranks_dispatch_ready_candidates() {
    let (database, path) = test_database();
    let now = unix_time_ms();
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create suitability incident");
    for (drone_id, session_id, latitude, longitude, battery) in [
        ("near-ready", "near-session", 51.5002, -0.1402, 78.0),
        ("far-ready", "far-session", 51.5500, -0.2000, 64.0),
        ("reserved", "reserved-session", 51.5001, -0.1401, 92.0),
    ] {
        let registration = suitability_registration_input(drone_id, session_id, now);
        database
            .register_agent(&registration)
            .expect("register suitability aircraft");
        database
            .record_heartbeat(session_id, now + 100)
            .expect("record suitability heartbeat");
        database
            .record_telemetry(
                session_id,
                &dispatch_ready_telemetry(now + 100, latitude, longitude, battery),
            )
            .expect("record suitability telemetry");
    }
    database
        .connection
        .lock()
        .expect("lock suitability reservation")
        .execute(
            "INSERT INTO incident_assignments (id, incident_id, drone_id, status, assigned_at_unix_ms) VALUES ('reserved-assignment', ?1, 'reserved', 'PREPARED', ?2)",
            rusqlite::params![incident.incident.id, now + 100],
        )
        .expect("reserve otherwise suitable aircraft");

    let suitability = database
        .incident_response_aircraft_suitability_at(
            &incident.incident.id,
            &IncidentResponseAircraftSuitabilityInput {
                response_pattern: "HOLD_AT_STAGING".into(),
                target_latitude: 51.5,
                target_longitude: -0.14,
                speed_mps: 5.0,
            },
            now + 200,
        )
        .expect("assess aircraft suitability");
    assert_eq!(suitability[0].drone_id, "near-ready");
    assert!(suitability[0].available);
    assert!(suitability[0].recommended);
    assert_eq!(suitability[1].drone_id, "far-ready");
    assert!(suitability[1].available);
    let reserved = suitability
        .iter()
        .find(|candidate| candidate.drone_id == "reserved")
        .expect("reserved aircraft remains visible with an explanation");
    assert!(!reserved.available);
    assert!(reserved
        .blockers
        .iter()
        .any(|reason| reason.code == "ACTIVE_INCIDENT_ASSIGNMENT"));

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn bounded_area_scan_reuses_lawn_mower_and_preserves_polygon_spacing_and_distance() {
    let (database, path) = test_database();
    insert_test_aircraft(&database, "area-response-drone", "Area Response", "active");
    let now = unix_time_ms();
    database
        .connection
        .lock()
        .expect("lock area response telemetry")
        .execute(
            "INSERT INTO vehicle_telemetry_current (drone_id, source, agent_observed_at_unix_ms, received_at_unix_ms, relative_altitude_m, latitude, longitude, absolute_altitude_m, home_position_json) VALUES ('area-response-drone', 'test', ?1, ?1, 0, 51.5, -0.142, 20, ?2)",
            rusqlite::params![now, r#"{"absoluteAltitudeM":20}"#],
        )
        .expect("insert area response telemetry");
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create area incident");
    let polygon = vec![
        super::known_buildings::GeoPoint {
            latitude: 51.5,
            longitude: -0.142,
        },
        super::known_buildings::GeoPoint {
            latitude: 51.5,
            longitude: -0.140,
        },
        super::known_buildings::GeoPoint {
            latitude: 51.502,
            longitude: -0.140,
        },
        super::known_buildings::GeoPoint {
            latitude: 51.502,
            longitude: -0.142,
        },
    ];
    let input = ExpandedPrepareIncidentResponseInput {
        expected_incident_revision: incident.incident.revision,
        drone_id: "area-response-drone".into(),
        geometry: IncidentResponseGeometryInput::BoundedAreaScan {
            area_polygon: polygon.clone(),
            altitude_meters: 45.0,
            speed_mps: 5.0,
            lane_spacing_meters: 20.0,
            sweep_angle_degrees: 15.0,
        },
        arrival_failure_policy: "OPERATOR_INTERVENTION".into(),
        incident_target_altitude_amsl_meters: None,
        building_horizontal_clearance_meters: 10.0,
        building_vertical_clearance_meters: 5.0,
        known_building_override_reason: Some("Local OS extract unavailable for test".into()),
    };
    let prepared = database
        .prepare_expanded_incident_response_with_dataset(&incident.incident.id, &input, None)
        .expect("prepare area response");
    assert_eq!(prepared.mission.template_type, "AREA_SCAN");
    assert_eq!(prepared.plan.pattern_type, "LAWN_MOWER");
    assert!(prepared.plan.generated_waypoints.len() > 4);
    assert_eq!(prepared.plan.metadata["laneSpacingMeters"], 20.0);
    assert_eq!(prepared.plan.metadata["sweepAngleDegrees"], 15.0);
    assert!(prepared.plan.metadata["estimatedDistanceMeters"]
        .as_f64()
        .is_some_and(|distance| distance > 0.0));
    assert_eq!(
        prepared.plan.metadata["incidentResponse"]["reviewedGeometry"]["areaPolygon"]
            .as_array()
            .map(Vec::len),
        Some(polygon.len())
    );
    let arrival_actions = prepared
        .plan
        .actions
        .iter()
        .filter(|action| {
            matches!(
                action.action_type.as_str(),
                "HOLD_AT_ARRIVAL" | "RESUME_AFTER_ARRIVAL"
            )
        })
        .collect::<Vec<_>>();
    assert_eq!(arrival_actions.len(), 2);
    assert_eq!(arrival_actions[0].action_type, "HOLD_AT_ARRIVAL");
    assert_eq!(arrival_actions[1].action_type, "RESUME_AFTER_ARRIVAL");
    assert!(arrival_actions
        .iter()
        .all(|action| action.params["triggerAfterWaypointSequence"] == 0));
    let dispatch = database
        .create_mission_run(
            &prepared.mission.id,
            "area-response-drone",
            Some(&prepared.plan.id),
        )
        .expect("create area response run with durable arrival phase");
    assert_eq!(dispatch.run.actions.len(), 2);
    assert_eq!(dispatch.run.actions[0].action_type, "HOLD_AT_ARRIVAL");
    assert_eq!(dispatch.run.actions[1].action_type, "RESUME_AFTER_ARRIVAL");

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn bounded_orbit_is_single_level_explicit_and_rejects_stepped_altitude_for_now() {
    let (database, path) = test_database();
    insert_test_aircraft(
        &database,
        "orbit-response-drone",
        "Orbit Response",
        "active",
    );
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create orbit incident");
    let mut input = ExpandedPrepareIncidentResponseInput {
        expected_incident_revision: incident.incident.revision,
        drone_id: "orbit-response-drone".into(),
        geometry: IncidentResponseGeometryInput::BoundedOrbit {
            center_latitude: 51.501,
            center_longitude: -0.141,
            radius_meters: 40.0,
            altitude_levels_meters: vec![50.0],
            speed_mps: 5.0,
            laps_per_level: 1,
            direction: "CLOCKWISE".into(),
            max_vertical_rate_mps: 1.5,
        },
        arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
        incident_target_altitude_amsl_meters: Some(42.0),
        building_horizontal_clearance_meters: 10.0,
        building_vertical_clearance_meters: 5.0,
        known_building_override_reason: Some("Local OS extract unavailable for test".into()),
    };
    let preview = database
        .preview_expanded_incident_response_with_dataset(&incident.incident.id, &input, None)
        .expect("preview single-level orbit");
    assert_eq!(preview.generated_waypoints.len(), 25);
    assert_eq!(
        preview.metadata["incidentResponse"]["orbit"]["radiusMeters"],
        40.0
    );
    assert_eq!(
        preview.metadata["incidentResponse"]["orbit"]["transitionCount"],
        0
    );
    assert_eq!(
        preview.metadata["incidentResponse"]["orbit"]["transition"],
        "NONE_SINGLE_LEVEL"
    );
    let arrival_types = preview
        .actions
        .iter()
        .filter_map(|action| {
            matches!(
                action.action_type.as_str(),
                "HOLD_AT_ARRIVAL" | "POINT_GIMBAL_AT_INCIDENT" | "RESUME_AFTER_ARRIVAL"
            )
            .then_some(action.action_type.as_str())
        })
        .collect::<Vec<_>>();
    assert_eq!(
        arrival_types,
        vec![
            "HOLD_AT_ARRIVAL",
            "POINT_GIMBAL_AT_INCIDENT",
            "RESUME_AFTER_ARRIVAL"
        ]
    );

    if let IncidentResponseGeometryInput::BoundedOrbit {
        altitude_levels_meters,
        ..
    } = &mut input.geometry
    {
        *altitude_levels_meters = vec![40.0, 55.0];
    }
    let error = database
        .preview_expanded_incident_response_with_dataset(&incident.incident.id, &input, None)
        .expect_err("stepped orbit must remain gated");
    assert!(error.contains("exactly one altitude level"));

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn known_building_intersection_requires_and_persists_operator_override() {
    let (database, path) = test_database();
    insert_test_aircraft(
        &database,
        "building-response-drone",
        "Building Response",
        "active",
    );
    database
        .connection
        .lock()
        .expect("lock telemetry")
        .execute(
            "INSERT INTO vehicle_telemetry_current (drone_id, source, agent_observed_at_unix_ms, received_at_unix_ms, relative_altitude_m, latitude, longitude, absolute_altitude_m, home_position_json) VALUES (?1, 'test', 1, 1, 0, 51.5, -0.142, 20, ?2)",
            rusqlite::params!["building-response-drone", r#"{"absoluteAltitudeM":20}"#],
        )
        .expect("insert response telemetry");
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create building incident");
    let dataset = KnownBuildingDataset::from_geojson_slice(br#"{
      "type":"FeatureCollection",
      "atlasProvenance":{"provider":"Ordnance Survey","product":"OS NGD Buildings + Building Height Attribute","datasetId":"bld-fts-building-4:test","schemaVersion":"4.0","release":"2026-04","retrievedAtUnixMs":1775000000000,"coverageBbox":[-0.15,51.49,-0.13,51.51]},
      "features":[{"type":"Feature","id":"os-building-crossing","geometry":{"type":"Polygon","coordinates":[[[-0.1411,51.4999],[-0.1409,51.4999],[-0.1409,51.5001],[-0.1411,51.5001],[-0.1411,51.4999]]]},"properties":{"height_absolutemin_m":20,"height_absolutemax_m":40,"height_confidencelevel":"High","height_evidencedate":"2026-03-01"}}]
    }"#).expect("parse OS fixture");
    let mut input = ExpandedPrepareIncidentResponseInput {
        expected_incident_revision: incident.incident.revision,
        drone_id: "building-response-drone".into(),
        geometry: IncidentResponseGeometryInput::OffsetObserve {
            observation_latitude: 51.5,
            observation_longitude: -0.140,
            altitude_meters: 15.0,
            speed_mps: 5.0,
        },
        arrival_failure_policy: "OPERATOR_INTERVENTION".into(),
        incident_target_altitude_amsl_meters: Some(42.0),
        building_horizontal_clearance_meters: 5.0,
        building_vertical_clearance_meters: 5.0,
        known_building_override_reason: None,
    };
    let preview = database
        .preview_expanded_incident_response_with_dataset(
            &incident.incident.id,
            &input,
            Some(&dataset),
        )
        .expect("preview building intersection");
    assert_eq!(preview.known_building_assessment.status, "INTERSECTIONS");
    assert_eq!(preview.known_building_assessment.intersection_count, 1);
    let error = database
        .prepare_expanded_incident_response_with_dataset(
            &incident.incident.id,
            &input,
            Some(&dataset),
        )
        .expect_err("intersection requires override");
    assert!(error.contains("explicit override reason"));
    assert!(database
        .incident(&incident.incident.id)
        .expect("read incident after blocked response")
        .assignments
        .is_empty());

    input.known_building_override_reason = Some(
        "Operator accepts the mapped-building warning for this supervised validation flight".into(),
    );
    let prepared = database
        .prepare_expanded_incident_response_with_dataset(
            &incident.incident.id,
            &input,
            Some(&dataset),
        )
        .expect("prepare overridden building intersection");
    let assessment = &prepared.plan.metadata["incidentResponse"]["knownBuildingAssessment"];
    assert_eq!(assessment["status"], "INTERSECTIONS");
    assert_eq!(assessment["issues"][0]["featureId"], "os-building-crossing");
    assert_eq!(
        assessment["provenance"]["datasetId"],
        "bld-fts-building-4:test"
    );
    assert!(assessment["overrideReason"]
        .as_str()
        .is_some_and(|reason| reason.contains("supervised validation")));

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn expanded_response_upload_is_bound_to_the_assessed_departure() {
    let (database, path) = test_database();
    let drone_id = "departure-bound-drone";
    insert_test_aircraft(&database, drone_id, "Departure Bound", "active");
    let now = unix_time_ms();
    database
        .connection
        .lock()
        .expect("lock departure telemetry")
        .execute(
            "INSERT INTO vehicle_telemetry_current (drone_id, source, agent_observed_at_unix_ms, received_at_unix_ms, relative_altitude_m, latitude, longitude, absolute_altitude_m, home_position_json) VALUES (?1, 'test', ?2, ?2, 0, 51.5, -0.142, 20, ?3)",
            rusqlite::params![drone_id, now, r#"{"absoluteAltitudeM":20}"#],
        )
        .expect("insert assessed departure telemetry");
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create departure-bound incident");
    let prepared = database
        .prepare_expanded_incident_response_with_dataset(
            &incident.incident.id,
            &ExpandedPrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                drone_id: drone_id.into(),
                geometry: IncidentResponseGeometryInput::OffsetObserve {
                    observation_latitude: 51.501,
                    observation_longitude: -0.141,
                    altitude_meters: 35.0,
                    speed_mps: 6.0,
                },
                arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
                incident_target_altitude_amsl_meters: Some(42.0),
                building_horizontal_clearance_meters: 10.0,
                building_vertical_clearance_meters: 5.0,
                known_building_override_reason: Some(
                    "No configured building dataset in this test".into(),
                ),
            },
            None,
        )
        .expect("prepare departure-bound response");
    assert_eq!(
        prepared.plan.metadata["incidentResponse"]["knownBuildingAssessment"]["routeStart"]
            ["latitude"],
        51.5
    );

    database
        .connection
        .lock()
        .expect("lock changed departure telemetry")
        .execute(
            "UPDATE vehicle_telemetry_current SET latitude = 51.501, longitude = -0.142, received_at_unix_ms = ?2 WHERE drone_id = ?1",
            rusqlite::params![drone_id, unix_time_ms()],
        )
        .expect("move aircraft departure");
    let error = database
        .create_mission_run(&prepared.mission.id, drone_id, Some(&prepared.plan.id))
        .expect_err("movement after assessment must block upload");
    assert!(error.contains("departure changed after known-building assessment"));

    database
        .connection
        .lock()
        .expect("lock restored departure telemetry")
        .execute(
            "UPDATE vehicle_telemetry_current SET latitude = 51.5, longitude = -0.142, received_at_unix_ms = ?2 WHERE drone_id = ?1",
            rusqlite::params![drone_id, unix_time_ms()],
        )
        .expect("restore assessed departure");
    database
        .create_mission_run(&prepared.mission.id, drone_id, Some(&prepared.plan.id))
        .expect("unchanged fresh departure may upload");

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn incident_edit_expires_prepared_response_and_releases_aircraft() {
    let (database, path) = test_database();
    insert_test_aircraft(
        &database,
        "stale-response-drone",
        "Stale Response",
        "active",
    );
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create incident");
    let prepared = database
        .prepare_incident_response(
            &incident.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                drone_id: "stale-response-drone".into(),
                staging_latitude: 51.501,
                staging_longitude: -0.141,
                altitude_meters: 35.0,
                speed_mps: 6.0,
                arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
                point_gimbal_at_incident: false,
                incident_target_altitude_amsl_meters: None,
            },
        )
        .expect("prepare incident response");

    let revised = database
        .update_incident(
            &incident.incident.id,
            &UpdateIncidentInput {
                expected_revision: incident.incident.revision,
                incident_type: incident.incident.incident_type.clone(),
                priority: incident.incident.priority.clone(),
                status: incident.incident.status.clone(),
                summary: "Updated incident context".into(),
                description: incident.incident.description.clone(),
                latitude: Some(51.505),
                longitude: incident.incident.longitude,
                address: incident.incident.address.clone(),
                area: incident.incident.area.clone(),
                occurred_at_unix_ms: incident.incident.occurred_at_unix_ms,
            },
        )
        .expect("revise incident");
    assert_eq!(revised.assignments[0].status, "STALE");
    assert_eq!(revised.incident.location_revision, 2);
    assert!(revised.assignments[0].ended_at_unix_ms.is_some());
    assert_eq!(
        revised.events.last().unwrap().details["stalePreparedAssignments"],
        1
    );
    let revision_alert = database
        .operational_alerts(false, 100)
        .expect("read changed-incident alert")
        .alerts
        .into_iter()
        .find(|alert| alert.alert_type == "INCIDENT_REVISION_CHANGED")
        .expect("incident edit after planning must alert");
    assert_eq!(revision_alert.state, "ACTIVE");
    assert_eq!(
        revision_alert.incident_id.as_deref(),
        Some(incident.incident.id.as_str())
    );
    assert_eq!(
        revision_alert.drone_id.as_deref(),
        Some("stale-response-drone")
    );
    assert_eq!(revision_alert.evidence["plannedIncidentRevision"], 1);
    assert_eq!(revision_alert.evidence["currentIncidentRevision"], 2);

    let upload_error = database
        .create_mission_run(
            &prepared.mission.id,
            "stale-response-drone",
            Some(&prepared.plan.id),
        )
        .expect_err("expired response must not upload");
    assert!(upload_error.contains("assignment has ended"));

    database
        .prepare_incident_response(
            &incident.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: revised.incident.revision,
                drone_id: "stale-response-drone".into(),
                staging_latitude: 51.502,
                staging_longitude: -0.142,
                altitude_meters: 35.0,
                speed_mps: 6.0,
                arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
                point_gimbal_at_incident: false,
                incident_target_altitude_amsl_meters: None,
            },
        )
        .expect("released aircraft can receive a replacement response");
    let recovered_revision_alert = database
        .operational_alert(&revision_alert.id)
        .expect("read resolved changed-incident alert");
    assert_eq!(recovered_revision_alert.state, "RESOLVED");
    assert!(recovered_revision_alert
        .resolution_reason
        .as_deref()
        .is_some_and(|reason| reason.contains("replacement response")));

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn operator_can_auditably_abandon_only_an_unstarted_prepared_response() {
    let (database, path) = test_database();
    insert_test_aircraft(
        &database,
        "released-response-drone",
        "Released Response",
        "active",
    );
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create incident");
    let prepared = database
        .prepare_incident_response(
            &incident.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                drone_id: "released-response-drone".into(),
                staging_latitude: 51.501,
                staging_longitude: -0.141,
                altitude_meters: 35.0,
                speed_mps: 6.0,
                arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
                point_gimbal_at_incident: false,
                incident_target_altitude_amsl_meters: None,
            },
        )
        .expect("prepare response");
    let abandoned = database
        .abandon_prepared_response(
            &incident.incident.id,
            &prepared.assignment.id,
            &AbandonPreparedResponseInput {
                expected_incident_revision: incident.incident.revision,
                reason: "Weather changed before deployment".into(),
            },
        )
        .expect("abandon prepared response");
    assert_eq!(abandoned.assignments[0].status, "ABANDONED");
    assert!(abandoned.assignments[0].ended_at_unix_ms.is_some());
    assert_eq!(
        abandoned.events.last().unwrap().event_type,
        "response_abandoned"
    );
    assert_eq!(
        abandoned.events.last().unwrap().details["reason"],
        "Weather changed before deployment"
    );
    assert_eq!(
        abandoned.events.last().unwrap().details["aircraftReservationReleased"],
        true
    );
    let upload_error = database
        .create_mission_run(
            &prepared.mission.id,
            "released-response-drone",
            Some(&prepared.plan.id),
        )
        .expect_err("abandoned response cannot upload");
    assert!(upload_error.contains("assignment has ended"));

    let replacement = database
        .prepare_incident_response(
            &incident.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                drone_id: "released-response-drone".into(),
                staging_latitude: 51.502,
                staging_longitude: -0.142,
                altitude_meters: 30.0,
                speed_mps: 5.0,
                arrival_failure_policy: "OPERATOR_INTERVENTION".into(),
                point_gimbal_at_incident: false,
                incident_target_altitude_amsl_meters: None,
            },
        )
        .expect("released aircraft accepts a new response");
    database
        .create_mission_run(
            &replacement.mission.id,
            "released-response-drone",
            Some(&replacement.plan.id),
        )
        .expect("start replacement upload");
    let active_error = database
        .abandon_prepared_response(
            &incident.incident.id,
            &replacement.assignment.id,
            &AbandonPreparedResponseInput {
                expected_incident_revision: incident.incident.revision,
                reason: "Too late".into(),
            },
        )
        .expect_err("run-linked response must use mission controls");
    assert!(active_error.contains("mission upload has begun"));

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn incident_response_rejects_stale_or_invalid_context_without_partial_writes() {
    let (database, path) = test_database();
    insert_test_aircraft(&database, "response-drone", "Response", "active");
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create incident");
    let input = PrepareIncidentResponseInput {
        expected_incident_revision: incident.incident.revision + 1,
        drone_id: "response-drone".into(),
        staging_latitude: 51.501,
        staging_longitude: -0.141,
        altitude_meters: 35.0,
        speed_mps: 6.0,
        arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
        point_gimbal_at_incident: false,
        incident_target_altitude_amsl_meters: None,
    };

    let stale_error = database
        .prepare_incident_response(&incident.incident.id, &input)
        .expect_err("stale incident revision must fail");
    assert!(stale_error.contains("expected revision 2, current revision 1"));

    let invalid_error = database
        .prepare_incident_response(
            &incident.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                altitude_meters: 121.0,
                ..input
            },
        )
        .expect_err("unsafe altitude must fail");
    assert!(
        invalid_error.contains("altitudeMeters must be between 2 and 120"),
        "{invalid_error}"
    );

    let connection = database.connection.lock().expect("lock database");
    let counts: (i64, i64, i64, i64) = connection
        .query_row(
            "SELECT (SELECT count(*) FROM missions), (SELECT count(*) FROM mission_plans), (SELECT count(*) FROM incident_assignments), (SELECT count(*) FROM incident_events)",
            [],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?)),
        )
        .expect("read response table counts");
    assert_eq!(counts, (0, 0, 0, 1));
    drop(connection);

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn incident_response_rolls_back_mission_and_plan_when_assignment_insert_fails() {
    let (database, path) = test_database();
    insert_test_aircraft(&database, "rollback-drone", "Rollback", "active");
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create incident");
    database
        .connection
        .lock()
        .expect("lock database")
        .execute_batch(
            "CREATE TRIGGER reject_test_assignment BEFORE INSERT ON incident_assignments BEGIN SELECT RAISE(ABORT, 'forced assignment failure'); END;",
        )
        .expect("install failure trigger");

    let error = database
        .prepare_incident_response(
            &incident.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                drone_id: "rollback-drone".into(),
                staging_latitude: 51.501,
                staging_longitude: -0.141,
                altitude_meters: 35.0,
                speed_mps: 6.0,
                arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
                point_gimbal_at_incident: false,
                incident_target_altitude_amsl_meters: None,
            },
        )
        .expect_err("assignment failure must abort response preparation");
    assert!(error.contains("forced assignment failure"));

    let connection = database.connection.lock().expect("lock database");
    let counts: (i64, i64, i64, i64, i64) = connection
        .query_row(
            "SELECT (SELECT count(*) FROM missions), (SELECT count(*) FROM mission_plans), (SELECT count(*) FROM mission_items), (SELECT count(*) FROM incident_assignments), (SELECT count(*) FROM incident_events)",
            [],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?, row.get(4)?)),
        )
        .expect("read rolled-back response counts");
    assert_eq!(counts, (0, 0, 0, 0, 1));
    drop(connection);

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn arrival_hold_lifecycle_requires_acknowledgement_before_on_scene() {
    let (database, path) = test_database();
    let (incident_id, run_id, action_sequence) =
        prepare_running_response(&database, "arrival-drone", "RETURN_TO_LAUNCH");

    let before_hold = database
        .incident(&incident_id)
        .expect("read arrived response");
    assert_eq!(before_hold.assignments[0].status, "RUNNING");
    assert_eq!(before_hold.assignments[0].on_scene_at_unix_ms, None);
    assert!(!before_hold
        .events
        .iter()
        .any(|event| event.event_type == "response_on_scene"));

    for (event_id, state, attempt, error_code, message) in [
        ("hold-running-1", "RUNNING", 1, "", "Executing Hold"),
        (
            "hold-retrying-1",
            "RETRYING",
            1,
            "ARRIVAL_HOLD_FAILED",
            "Hold rejected; retrying",
        ),
        ("hold-running-2", "RUNNING", 2, "", "Retrying Hold"),
        ("hold-succeeded-2", "SUCCEEDED", 2, "", "Hold acknowledged"),
    ] {
        let occurred_at = unix_time_ms();
        let updated = database
            .apply_mission_action_update(&MissionActionUpdateInput {
                event_id: event_id.into(),
                mission_run_id: run_id.clone(),
                action_sequence,
                action_type: "HOLD_AT_ARRIVAL".into(),
                state: state.into(),
                attempt,
                failure_policy: "RETURN_TO_LAUNCH".into(),
                occurred_at_unix_ms: occurred_at,
                error_code: error_code.into(),
                message: message.into(),
                evidence_json: (state == "SUCCEEDED").then_some(r#"{"acknowledged":true}"#.into()),
            })
            .expect("persist Hold action update");
        let action = &updated.actions[0];
        match state {
            "RUNNING" => {
                assert_eq!(
                    action.attempt_deadline_at_unix_ms,
                    Some(occurred_at + 20_000)
                );
                assert_eq!(action.next_attempt_at_unix_ms, None);
            }
            "RETRYING" => {
                assert_eq!(action.attempt_deadline_at_unix_ms, None);
                assert_eq!(action.next_attempt_at_unix_ms, Some(occurred_at + 2_000));
            }
            "SUCCEEDED" => {
                assert_eq!(action.attempt_deadline_at_unix_ms, None);
                assert_eq!(action.next_attempt_at_unix_ms, None);
            }
            _ => {}
        }
    }

    let retry_alert = database
        .operational_alerts(true, 100)
        .expect("read recovered arrival-action alert")
        .alerts
        .into_iter()
        .find(|alert| alert.source == "mission_action")
        .expect("Hold retry must create a durable alert episode");
    assert_eq!(retry_alert.alert_type, "ARRIVAL_ACTION_RETRYING");
    assert_eq!(retry_alert.state, "RESOLVED");
    assert!(retry_alert
        .events
        .iter()
        .any(|event| event.event_type == "RESOLVED"));

    let on_scene = database
        .incident(&incident_id)
        .expect("read acknowledged response");
    assert_eq!(on_scene.assignments[0].status, "ON_SCENE");
    assert!(on_scene.assignments[0].on_scene_at_unix_ms.is_some());
    let on_scene_event = on_scene
        .events
        .iter()
        .find(|event| event.event_type == "response_on_scene")
        .expect("durable on-scene event");
    assert_eq!(on_scene_event.details["acknowledged"], true);

    let run = database
        .mission_run(&run_id)
        .expect("read action lifecycle");
    assert_eq!(run.status, "RUNNING");
    assert_eq!(run.actions[0].state, "SUCCEEDED");
    assert_eq!(run.actions[0].attempt, 2);
    assert_eq!(
        run.actions[0]
            .events
            .iter()
            .map(|event| event.state.as_str())
            .collect::<Vec<_>>(),
        vec!["REQUESTED", "RUNNING", "RETRYING", "RUNNING", "SUCCEEDED"]
    );

    database
        .apply_mission_run_update(&MissionRunUpdateInput {
            event_id: "arrival-completed-after-hold".into(),
            operation_id: String::new(),
            mission_run_id: run_id,
            event_type: "completed".into(),
            run_state: "COMPLETED".into(),
            occurred_at_unix_ms: unix_time_ms(),
            progress_percent: Some(100.0),
            current_waypoint: Some(1),
            total_waypoints: Some(1),
            error_code: String::new(),
            message: "Arrival actions completed".into(),
            evidence_json: None,
        })
        .expect("complete response after Hold acknowledgement");
    let completed = database
        .incident(&incident_id)
        .expect("read completed response");
    assert_eq!(completed.assignments[0].status, "COMPLETED");
    assert!(completed.assignments[0].ended_at_unix_ms.is_some());
    assert!(completed.assignments[0].on_scene_at_unix_ms.is_some());

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn aircraft_rejects_conflicting_incident_assignments_and_mission_runs() {
    let (database, path) = test_database();
    insert_test_aircraft(&database, "conflict-drone", "Conflict Test", "active");
    let first = database
        .create_incident(&sample_incident_input())
        .expect("create first incident");
    let prepared = database
        .prepare_incident_response(
            &first.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: first.incident.revision,
                drone_id: "conflict-drone".into(),
                staging_latitude: 51.501,
                staging_longitude: -0.141,
                altitude_meters: 35.0,
                speed_mps: 6.0,
                arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
                point_gimbal_at_incident: false,
                incident_target_altitude_amsl_meters: None,
            },
        )
        .expect("prepare first assignment");
    let second = database
        .create_incident(&CreateIncidentInput {
            summary: "Second incident for conflict test".into(),
            ..sample_incident_input()
        })
        .expect("create second incident");
    let assignment_error = database
        .prepare_incident_response(
            &second.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: second.incident.revision,
                drone_id: "conflict-drone".into(),
                staging_latitude: 51.502,
                staging_longitude: -0.142,
                altitude_meters: 35.0,
                speed_mps: 6.0,
                arrival_failure_policy: "OPERATOR_INTERVENTION".into(),
                point_gimbal_at_incident: false,
                incident_target_altitude_amsl_meters: None,
            },
        )
        .expect_err("active assignment must block a second assignment");
    assert!(assignment_error.contains("active incident assignment"));

    let unrelated = database
        .create_mission(&CreateMissionInput {
            template_type: "WAYPOINT".into(),
            name: "Unrelated mission".into(),
            description: String::new(),
            selected_pattern: None,
            params: serde_json::json!({
                "defaultAltitudeMeters": 25,
                "waypoints": [{"latitude": 51.501, "longitude": -0.141}]
            }),
        })
        .expect("create unrelated mission");
    let unrelated_plan = database
        .plan_mission(&unrelated.id)
        .expect("plan unrelated mission");
    let mission_error = database
        .create_mission_run(&unrelated.id, "conflict-drone", Some(&unrelated_plan.id))
        .expect_err("active assignment must block unrelated run");
    assert!(mission_error.contains("active incident assignment"));

    let response_run = database
        .create_mission_run(
            &prepared.mission.id,
            "conflict-drone",
            Some(&prepared.plan.id),
        )
        .expect("assigned response may create its run");
    let second_run_error = database
        .create_mission_run(&unrelated.id, "conflict-drone", Some(&unrelated_plan.id))
        .expect_err("unfinished run must block another run");
    assert!(second_run_error.contains("unfinished mission run"));
    assert_eq!(response_run.run.drone_id, "conflict-drone");

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn mission_translation_warning_is_associated_and_resolves_with_the_run() {
    let (database, path) = test_database();
    insert_test_aircraft(
        &database,
        "translation-alert-drone",
        "Translation Alert",
        "active",
    );
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create incident for translation warning");
    let prepared = database
        .prepare_incident_response(
            &incident.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                drone_id: "translation-alert-drone".into(),
                staging_latitude: 51.501,
                staging_longitude: -0.141,
                altitude_meters: 35.0,
                speed_mps: 6.0,
                arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
                point_gimbal_at_incident: false,
                incident_target_altitude_amsl_meters: None,
            },
        )
        .expect("prepare translation-warning response");
    let dispatch = database
        .create_mission_run(
            &prepared.mission.id,
            "translation-alert-drone",
            Some(&prepared.plan.id),
        )
        .expect("create translation-warning run");
    database
        .apply_mission_run_update(&MissionRunUpdateInput {
            event_id: "translation-warning-uploaded".into(),
            operation_id: dispatch.operation_id,
            mission_run_id: dispatch.run.id.clone(),
            event_type: "uploaded".into(),
            run_state: "READY".into(),
            occurred_at_unix_ms: unix_time_ms(),
            progress_percent: Some(100.0),
            current_waypoint: None,
            total_waypoints: Some(1),
            error_code: String::new(),
            message: "Uploaded with translation warning".into(),
            evidence_json: Some(
                serde_json::json!({
                    "translationWarnings": ["Gimbal pitch is advisory on this vehicle"]
                })
                .to_string(),
            ),
        })
        .expect("record translated mission warning");
    let warning = database
        .operational_alerts(false, 100)
        .expect("read translation warning")
        .alerts
        .into_iter()
        .find(|alert| alert.alert_type == "MISSION_TRANSLATION_WARNING")
        .expect("translation warning must be operationally visible");
    assert_eq!(warning.state, "ACTIVE");
    assert_eq!(warning.severity, "WARNING");
    assert_eq!(warning.drone_id.as_deref(), Some("translation-alert-drone"));
    assert_eq!(
        warning.incident_id.as_deref(),
        Some(incident.incident.id.as_str())
    );
    assert_eq!(
        warning.mission_run_id.as_deref(),
        Some(dispatch.run.id.as_str())
    );
    assert_eq!(
        warning.evidence["translationWarnings"]
            .as_array()
            .unwrap()
            .len(),
        1
    );

    database
        .apply_mission_run_update(&MissionRunUpdateInput {
            event_id: "translation-warning-run-failed".into(),
            operation_id: String::new(),
            mission_run_id: dispatch.run.id,
            event_type: "execution_failed".into(),
            run_state: "FAILED".into(),
            occurred_at_unix_ms: unix_time_ms(),
            progress_percent: None,
            current_waypoint: None,
            total_waypoints: Some(1),
            error_code: "TEST_END".into(),
            message: "Finish translation warning test".into(),
            evidence_json: None,
        })
        .expect("terminate translation-warning run");
    assert_eq!(
        database
            .operational_alert(&warning.id)
            .expect("read resolved translation warning")
            .state,
        "RESOLVED"
    );

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn failed_arrival_action_persists_reviewed_policy_without_claiming_on_scene() {
    let (database, path) = test_database();
    let (incident_id, run_id, action_sequence) =
        prepare_running_response(&database, "policy-drone", "OPERATOR_INTERVENTION");

    for (event_id, state, attempt) in [
        ("policy-running-1", "RUNNING", 1),
        ("policy-retrying-1", "RETRYING", 1),
        ("policy-running-2", "RUNNING", 2),
        ("policy-retrying-2", "RETRYING", 2),
        ("policy-running-3", "RUNNING", 3),
        ("policy-failed-3", "FAILED", 3),
        ("policy-applied-3", "POLICY_APPLIED", 3),
    ] {
        let failed = matches!(state, "RETRYING" | "FAILED" | "POLICY_APPLIED");
        database
            .apply_mission_action_update(&MissionActionUpdateInput {
                event_id: event_id.into(),
                mission_run_id: run_id.clone(),
                action_sequence,
                action_type: "HOLD_AT_ARRIVAL".into(),
                state: state.into(),
                attempt,
                failure_policy: "OPERATOR_INTERVENTION".into(),
                occurred_at_unix_ms: unix_time_ms(),
                error_code: if failed { "ARRIVAL_HOLD_FAILED" } else { "" }.into(),
                message: if state == "POLICY_APPLIED" {
                    "Operator intervention required"
                } else {
                    "Hold was not acknowledged"
                }
                .into(),
                evidence_json: (state == "POLICY_APPLIED").then_some(
                    r#"{"policy":"OPERATOR_INTERVENTION","automaticVehicleCommand":false}"#.into(),
                ),
            })
            .expect("persist failed Hold policy lifecycle");
    }

    let run = database.mission_run(&run_id).expect("read failed action");
    assert_eq!(run.status, "RUNNING");
    assert_eq!(run.actions[0].state, "POLICY_APPLIED");
    assert_eq!(run.actions[0].failure_policy, "OPERATOR_INTERVENTION");
    let incident = database
        .incident(&incident_id)
        .expect("read incident without Hold acknowledgement");
    assert_eq!(incident.assignments[0].on_scene_at_unix_ms, None);
    assert!(!incident
        .events
        .iter()
        .any(|event| event.event_type == "response_on_scene"));

    let alerts = database
        .operational_alerts(true, 100)
        .expect("read arrival action alerts");
    let intervention = alerts
        .alerts
        .iter()
        .find(|alert| alert.alert_type == "ARRIVAL_ACTION_AWAITING_INTERVENTION")
        .expect("operator intervention must remain an active alert");
    assert_eq!(intervention.state, "ACTIVE");
    assert_eq!(intervention.severity, "CRITICAL");
    assert_eq!(intervention.drone_id.as_deref(), Some("policy-drone"));
    assert_eq!(
        intervention.incident_id.as_deref(),
        Some(incident_id.as_str())
    );
    assert_eq!(
        intervention.mission_run_id.as_deref(),
        Some(run_id.as_str())
    );
    assert_eq!(intervention.observation_count, 4);

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn operational_alert_lifecycle_deduplicates_without_conflating_acknowledgement_and_recovery() {
    let (database, path) = test_database();
    insert_test_aircraft(&database, "alert-drone", "Alert Test", "active");
    let now = unix_time_ms();
    let observation = AlertObservation {
        dedupe_key: "test_condition:alert-drone".into(),
        alert_type: "TEST_CONDITION".into(),
        severity: "WARNING".into(),
        source: "test_source".into(),
        drone_id: Some("alert-drone".into()),
        incident_id: None,
        mission_run_id: None,
        title: "Test operational condition".into(),
        recommended_action: "Inspect the durable alert lifecycle.".into(),
        evidence: serde_json::json!({ "sample": 1 }),
        observed_at_unix_ms: now,
    };
    let first = database
        .observe_operational_alert(&observation)
        .expect("activate alert");
    let duplicate = database
        .observe_operational_alert(&AlertObservation {
            evidence: serde_json::json!({ "sample": 2 }),
            observed_at_unix_ms: now + 1,
            ..observation.clone()
        })
        .expect("update duplicate observation");
    assert_eq!(duplicate.id, first.id);
    assert_eq!(duplicate.observation_count, 2);
    assert_eq!(duplicate.first_seen_at_unix_ms, now);
    assert_eq!(duplicate.last_seen_at_unix_ms, now + 1);
    assert_eq!(duplicate.events.len(), 1);

    let acknowledged = database
        .acknowledge_operational_alert(&first.id, now + 2)
        .expect("acknowledge alert as seen");
    assert_eq!(acknowledged.state, "ACKNOWLEDGED");
    assert_eq!(acknowledged.resolved_at_unix_ms, None);
    assert_eq!(acknowledged.events[1].event_type, "ACKNOWLEDGED");
    assert_eq!(acknowledged.events[1].evidence["conditionResolved"], false);

    let escalated = database
        .observe_operational_alert(&AlertObservation {
            severity: "CRITICAL".into(),
            evidence: serde_json::json!({ "sample": 3 }),
            observed_at_unix_ms: now + 3,
            ..observation.clone()
        })
        .expect("escalate acknowledged alert without creating a second row");
    assert_eq!(escalated.id, first.id);
    assert_eq!(escalated.state, "ACTIVE");
    assert_eq!(escalated.severity, "CRITICAL");
    assert_eq!(escalated.observation_count, 3);
    assert_eq!(escalated.acknowledged_at_unix_ms, None);
    assert_eq!(escalated.events[2].event_type, "SEVERITY_CHANGED");
    assert!(escalated.events[2]
        .message
        .contains("acknowledgement is required again"));

    let other = database
        .observe_operational_alert(&AlertObservation {
            dedupe_key: "other_condition:alert-drone".into(),
            alert_type: "OTHER_CONDITION".into(),
            title: "Other condition".into(),
            observed_at_unix_ms: now + 3,
            ..observation
        })
        .expect("activate a second alert");
    let paginated = database
        .operational_alerts(false, 1)
        .expect("list a paginated alert result");
    assert_eq!(paginated.alerts.len(), 1);
    assert_eq!(paginated.active_count, 2);
    assert_eq!(paginated.unacknowledged_count, 2);

    assert!(database
        .resolve_operational_alert(
            "test_condition:alert-drone",
            "The test condition recovered",
            &serde_json::json!({ "recovered": true }),
            now + 4,
        )
        .expect("resolve acknowledged alert from source recovery"));
    assert!(database
        .resolve_operational_alert(
            "other_condition:alert-drone",
            "The other condition recovered",
            &serde_json::json!({ "recovered": true }),
            now + 4,
        )
        .expect("resolve second alert"));
    let resolved = database
        .operational_alert(&first.id)
        .expect("read resolved alert");
    assert_eq!(resolved.state, "RESOLVED");
    assert_eq!(resolved.acknowledged_at_unix_ms, None);
    assert_eq!(resolved.events.last().unwrap().event_type, "RESOLVED");
    assert!(database
        .acknowledge_operational_alert(&first.id, now + 5)
        .expect_err("resolved alerts cannot be acknowledged")
        .contains("only valid while the condition is active"));

    drop(database);
    let reopened = LocalDatabase::open_path(path.clone()).expect("reopen alert database");
    let persisted = reopened
        .operational_alert(&first.id)
        .expect("alert history must survive restart");
    assert_eq!(persisted.state, "RESOLVED");
    assert_eq!(persisted.observation_count, 3);
    assert_eq!(persisted.events.len(), 4);
    assert_eq!(
        reopened
            .expire_resolved_operational_alerts(now + 4, now + 5)
            .expect("expire old resolved alerts"),
        2
    );
    let expired = reopened
        .operational_alert(&first.id)
        .expect("expired alert history remains queryable");
    assert_eq!(expired.state, "EXPIRED");
    assert_eq!(expired.events.last().unwrap().event_type, "EXPIRED");
    assert_eq!(
        reopened
            .operational_alert(&other.id)
            .expect("second expired alert remains queryable")
            .state,
        "EXPIRED"
    );

    drop(reopened);
    remove_sqlite_files(&path);
}

#[test]
fn telemetry_and_agent_conditions_resolve_only_when_their_sources_recover() {
    let (database, path) = test_database();
    let now = unix_time_ms();
    let registration = alert_registration_input("alert-session-1", now);
    database
        .register_agent(&registration)
        .expect("register alert test aircraft");

    let low_unhealthy = alert_telemetry(now + 1, 9.0, false);
    database
        .record_telemetry(&registration.session_id, &low_unhealthy)
        .expect("record low-battery unhealthy-position telemetry");
    let current = database
        .operational_alerts(false, 100)
        .expect("read telemetry alerts");
    assert_eq!(current.active_count, 3);
    for expected in [
        "BATTERY_BELOW_THRESHOLD",
        "GLOBAL_POSITION_UNAVAILABLE",
        "HOME_POSITION_UNAVAILABLE",
    ] {
        let alert = current
            .alerts
            .iter()
            .find(|alert| alert.alert_type == expected)
            .unwrap_or_else(|| panic!("missing {expected} alert"));
        assert_eq!(alert.severity, "CRITICAL");
        assert_eq!(
            alert.drone_id.as_deref(),
            Some(registration.drone_id.as_str())
        );
    }
    let battery_id = current
        .alerts
        .iter()
        .find(|alert| alert.alert_type == "BATTERY_BELOW_THRESHOLD")
        .expect("battery alert")
        .id
        .clone();
    database
        .acknowledge_operational_alert(&battery_id, now + 2)
        .expect("acknowledge battery alert");
    database
        .record_telemetry(
            &registration.session_id,
            &alert_telemetry(now + 3, 9.0, false),
        )
        .expect("record duplicate telemetry condition");
    let battery = database
        .operational_alert(&battery_id)
        .expect("read acknowledged duplicate battery alert");
    assert_eq!(battery.state, "ACKNOWLEDGED");
    assert_eq!(battery.observation_count, 2);

    database
        .record_telemetry(
            &registration.session_id,
            &alert_telemetry(now + 4, 30.0, true),
        )
        .expect("record recovered telemetry conditions");
    assert_eq!(
        database
            .operational_alerts(false, 100)
            .expect("read current conditions after recovery")
            .active_count,
        0
    );

    database
        .refresh_time_based_alerts(now + 10_000)
        .expect("evaluate stale telemetry");
    let stale = database
        .operational_alerts(false, 100)
        .expect("read stale telemetry alert");
    assert_eq!(stale.active_count, 1);
    assert_eq!(stale.alerts[0].alert_type, "TELEMETRY_UNAVAILABLE");
    assert_eq!(stale.alerts[0].severity, "WARNING");
    let telemetry_alert_id = stale.alerts[0].id.clone();
    database
        .acknowledge_operational_alert(&telemetry_alert_id, now + 10_001)
        .expect("acknowledge stale telemetry");
    database
        .refresh_time_based_alerts(now + 20_000)
        .expect("escalate stale telemetry to lost");
    let lost = database
        .operational_alerts(false, 100)
        .expect("read lost telemetry alert");
    assert_eq!(lost.active_count, 1);
    assert_eq!(lost.unacknowledged_count, 1);
    assert_eq!(lost.alerts[0].id, telemetry_alert_id);
    assert_eq!(lost.alerts[0].state, "ACTIVE");
    assert_eq!(lost.alerts[0].alert_type, "TELEMETRY_UNAVAILABLE");
    assert_eq!(lost.alerts[0].severity, "CRITICAL");
    database
        .record_telemetry(
            &registration.session_id,
            &alert_telemetry(now + 20_001, 30.0, true),
        )
        .expect("recover telemetry stream");

    database
        .close_session(&registration.session_id, "test disconnect", now + 21_000)
        .expect("close Agent session");
    let disconnected = database
        .operational_alerts(false, 100)
        .expect("read Agent-disconnected alert");
    assert_eq!(disconnected.active_count, 1);
    assert_eq!(disconnected.alerts[0].alert_type, "AGENT_DISCONNECTED");
    let reconnect = alert_registration_input("alert-session-2", now + 22_000);
    database
        .register_agent(&reconnect)
        .expect("reconnect Agent");
    assert_eq!(
        database
            .operational_alerts(false, 100)
            .expect("read current alerts after Agent recovery")
            .active_count,
        0
    );
    let history = database
        .operational_alerts(true, 100)
        .expect("read durable alert history");
    assert!(history
        .alerts
        .iter()
        .any(|alert| { alert.alert_type == "AGENT_DISCONNECTED" && alert.state == "RESOLVED" }));
    assert!(history
        .alerts
        .iter()
        .any(|alert| { alert.alert_type == "TELEMETRY_UNAVAILABLE" && alert.state == "RESOLVED" }));

    drop(database);
    remove_sqlite_files(&path);
}

#[test]
fn evidence_recording_manifest_requires_checksum_before_verified_completion() {
    let (database, path) = test_database();
    insert_test_aircraft(&database, "evidence-drone", "Evidence Drone", "active");
    let evidence_root = path.with_extension("evidence");
    let now = unix_time_ms();
    let input = super::recordings::test_create_recording_input(
        "evidence-drone",
        &evidence_root.to_string_lossy(),
        now,
    );
    let requested = database
        .create_evidence_recording(&input)
        .expect("request evidence recording");
    assert_eq!(requested.status, "REQUESTED");
    assert_eq!(requested.events[0].event_type, "REQUESTED");
    let duplicate = database
        .create_evidence_recording(&input)
        .expect_err("one source may have only one active recording");
    assert!(duplicate.contains("already has an active evidence recording"));

    database
        .mark_evidence_recording_running(&requested.id, now + 1)
        .expect("confirm temporary capture");
    let invalid = database
        .begin_evidence_segment_finalization(&SegmentFinalizationInput {
            recording_session_id: requested.id.clone(),
            sequence: 0,
            temporary_relative_path: format!("temporary/{}/000000.partial.mp4", requested.id),
            final_relative_path: format!("objects/{}/000000.mp4", requested.id),
            source_started_at_unix_ms: now + 1,
            source_ended_at_unix_ms: now + 2_001,
            byte_length: 14,
            sha256: "not-a-checksum".into(),
            observed_at_unix_ms: now + 2_002,
        })
        .expect_err("invalid checksum must not enter the manifest");
    assert!(invalid.contains("SHA-256"));
    database
        .begin_evidence_segment_finalization(&SegmentFinalizationInput {
            recording_session_id: requested.id.clone(),
            sequence: 0,
            temporary_relative_path: format!("temporary/{}/000000.partial.mp4", requested.id),
            final_relative_path: format!("objects/{}/000000.mp4", requested.id),
            source_started_at_unix_ms: now + 1,
            source_ended_at_unix_ms: now + 2_001,
            byte_length: 14,
            sha256: "5e17f9e895f9f10ac4fa916dc40f30c6325f37ef6e679624f77748fe729ef9c3".into(),
            observed_at_unix_ms: now + 2_002,
        })
        .expect("record finalizing segment manifest");
    let finalizing = database
        .evidence_recording_session(&requested.id)
        .expect("read finalizing segment");
    assert_eq!(finalizing.segments[0].state, "FINALIZING");
    assert_eq!(finalizing.finalized_segment_count, 0);
    let premature_success = database
        .succeed_evidence_recording(&requested.id, "operator_stop", now + 2_003)
        .expect_err("a FINALIZING segment must block successful completion");
    assert!(premature_success.contains("remain FINALIZING"));
    assert_eq!(
        database
            .evidence_recording_session(&requested.id)
            .expect("read recording after blocked success")
            .status,
        "RUNNING"
    );

    database
        .complete_evidence_segment_finalization(&requested.id, 0, now + 2_003)
        .expect("complete verified segment");
    database
        .succeed_evidence_recording(&requested.id, "operator_stop", now + 2_004)
        .expect("finish evidence recording");
    let completed = database
        .evidence_recording_session(&requested.id)
        .expect("read completed evidence recording");
    assert_eq!(completed.status, "SUCCEEDED");
    assert_eq!(completed.finalized_segment_count, 1);
    assert_eq!(completed.total_bytes, 14);
    assert_eq!(completed.segments[0].state, "LOCAL_VERIFIED");
    assert_eq!(completed.segments[0].sha256.len(), 64);
    assert!(completed
        .events
        .iter()
        .any(|event| event.event_type == "SEGMENT_FINALIZED"));

    drop(database);
    remove_sqlite_files(&path);
    let _ = fs::remove_dir_all(evidence_root);
}

#[test]
fn evidence_assets_preserve_review_annotation_and_recoverable_retention_lifecycle() {
    let (database, path) = test_database();
    insert_test_aircraft(
        &database,
        "asset-evidence-drone",
        "Asset Evidence",
        "active",
    );
    let now = unix_time_ms();
    let policy = database
        .update_evidence_retention_policy(
            &UpdateEvidenceRetentionPolicyInput {
                enabled: true,
                default_retention_days: 1,
                extended_retention_days: 90,
                trash_grace_days: 3,
                actor: "test_operator".into(),
            },
            now,
        )
        .expect("configure test evidence retention");
    assert_eq!(policy.default_retention_days, 1);

    let pending = database
        .create_evidence_still_asset(&CreateEvidenceStillAssetInput {
            source_id: "test-source".into(),
            drone_id: "asset-evidence-drone".into(),
            incident_id: None,
            mission_id: None,
            mission_run_id: None,
            selection_id: None,
            captured_at_unix_ms: now,
            actor: "test_operator".into(),
        })
        .expect("create pending still asset");
    assert_eq!(pending.status, "PENDING");
    assert_eq!(pending.retain_until_unix_ms, Some(now + 86_400_000));

    let ready = database
        .complete_evidence_asset(
            &pending.id,
            &EvidenceAssetFileInput {
                relative_path: format!("assets/{}/original.jpg", pending.id),
                thumbnail_relative_path: format!("assets/{}/thumbnail.jpg", pending.id),
                mime_type: "image/jpeg".into(),
                thumbnail_mime_type: "image/jpeg".into(),
                byte_length: 12,
                sha256: "a".repeat(64),
                thumbnail_byte_length: 6,
                thumbnail_sha256: "b".repeat(64),
                source_started_at_unix_ms: now,
                source_ended_at_unix_ms: now,
            },
            now + 1,
        )
        .expect("publish still asset");
    assert_eq!(ready.status, "READY");
    assert_eq!(ready.events.last().unwrap().event_type, "READY");
    assert_eq!(
        database
            .retention_trash_candidates(now + 86_400_001)
            .expect("read due evidence")
            .len(),
        1
    );

    let reviewed = database
        .review_evidence_asset(
            &ReviewEvidenceAssetInput {
                asset_id: pending.id.clone(),
                review_state: "RELEVANT".into(),
                note: "Confirms the gate crossing".into(),
                actor: "reviewer".into(),
            },
            now + 2,
        )
        .expect("review evidence asset");
    assert_eq!(reviewed.review_state, "RELEVANT");
    assert_eq!(reviewed.annotations.len(), 1);

    let annotated = database
        .annotate_evidence_asset(
            &AnnotateEvidenceAssetInput {
                asset_id: pending.id.clone(),
                annotation_type: "TAG".into(),
                body: "gate-entry".into(),
                actor: "reviewer".into(),
            },
            now + 3,
        )
        .expect("tag evidence asset");
    assert_eq!(annotated.annotations.len(), 2);

    let held = database
        .update_evidence_asset_retention(
            &UpdateEvidenceAssetRetentionInput {
                asset_id: pending.id.clone(),
                retention_class: "LEGAL_HOLD".into(),
                actor: "reviewer".into(),
            },
            now + 4,
        )
        .expect("place evidence under legal hold");
    assert_eq!(held.retain_until_unix_ms, None);
    assert!(database
        .mark_evidence_asset_trashed(
            &pending.id,
            &format!("trash/{}/original.jpg", pending.id),
            &format!("trash/{}/thumbnail.jpg", pending.id),
            "cleanup",
            "reviewer",
            now + 5,
        )
        .expect_err("legal hold must block trash")
        .contains("legal hold"));

    database
        .update_evidence_asset_retention(
            &UpdateEvidenceAssetRetentionInput {
                asset_id: pending.id.clone(),
                retention_class: "STANDARD".into(),
                actor: "reviewer".into(),
            },
            now + 6,
        )
        .expect("release legal hold");
    let trashed = database
        .mark_evidence_asset_trashed(
            &pending.id,
            &format!("trash/{}/original.jpg", pending.id),
            &format!("trash/{}/thumbnail.jpg", pending.id),
            "retention elapsed",
            "atlas_retention_policy",
            now + 7,
        )
        .expect("move evidence to recoverable trash");
    assert_eq!(trashed.status, "TRASHED");
    assert_eq!(trashed.purge_after_unix_ms, Some(now + 7 + 3 * 86_400_000));
    let restored = database
        .mark_evidence_asset_restored(
            &pending.id,
            &format!("assets/{}/original.jpg", pending.id),
            &format!("assets/{}/thumbnail.jpg", pending.id),
            "reviewer",
            now + 8,
        )
        .expect("restore recoverable evidence");
    assert_eq!(restored.status, "READY");
    assert_eq!(restored.events.last().unwrap().event_type, "RESTORED");

    let trashed_again = database
        .mark_evidence_asset_trashed(
            &pending.id,
            &format!("trash/{}/original.jpg", pending.id),
            &format!("trash/{}/thumbnail.jpg", pending.id),
            "retention elapsed again",
            "atlas_retention_policy",
            now + 9,
        )
        .expect("trash restored evidence again");
    let purge_at = trashed_again.purge_after_unix_ms.unwrap();
    database
        .begin_evidence_asset_purge(&pending.id, purge_at)
        .expect("atomically claim eligible purge");
    assert_eq!(
        database
            .evidence_asset(&pending.id)
            .expect("read claimed purge")
            .status,
        "PURGING"
    );
    database
        .complete_evidence_asset_purge(&pending.id, "atlas_retention_policy", purge_at + 1)
        .expect("complete claimed purge");
    let purged = database
        .evidence_asset(&pending.id)
        .expect("read purged evidence record");
    assert_eq!(purged.status, "PURGED");
    assert_eq!(purged.events.last().unwrap().event_type, "PURGED");

    drop(database);
    remove_sqlite_files(&path);
}

#[cfg(unix)]
#[test]
fn still_capture_publishes_thumbnail_and_moves_media_through_recoverable_trash() {
    use std::os::unix::fs::PermissionsExt;

    let (database, path) = test_database();
    insert_test_aircraft(&database, "still-capture-drone", "Still Capture", "active");
    let evidence_root = path.with_extension("still-capture-evidence");
    let thumbnailer = path.with_extension("thumbnailer.sh");
    fs::write(
        &thumbnailer,
        b"#!/bin/sh\ninput=\"\"\nlast=\"\"\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = \"-i\" ]; then\n    shift\n    input=\"$1\"\n  fi\n  last=\"$1\"\n  shift\ndone\ncp \"$input\" \"$last\"\n",
    )
    .expect("write deterministic thumbnail fixture");
    fs::set_permissions(&thumbnailer, fs::Permissions::from_mode(0o755))
        .expect("make thumbnail fixture executable");
    let database = Arc::new(database);
    let recorder = crate::recording::EvidenceRecorder::for_test(
        Arc::clone(&database),
        crate::video::VideoSourceConfig {
            rtsp_url: "rtsp://camera/source".into(),
            rtsp_transport: "tcp".into(),
            decoder_path: thumbnailer.to_string_lossy().into_owned(),
            source_id: "still-source".into(),
        },
        evidence_root.clone(),
        1_000,
        500,
        Some(10_000),
    )
    .expect("create still evidence recorder");
    let jpeg = vec![0xff, 0xd8, 1, 2, 3, 0xff, 0xd9];
    let asset = recorder
        .capture_still(
            CaptureEvidenceStillInput {
                drone_id: "still-capture-drone".into(),
                incident_id: None,
                mission_id: None,
                mission_run_id: None,
                selection_id: None,
                actor: "operator".into(),
            },
            crate::video::CapturedVideoFrame {
                source_id: "still-source".into(),
                observed_at_unix_ms: unix_time_ms(),
                jpeg: jpeg.clone(),
            },
        )
        .expect("capture still evidence");
    assert_eq!(asset.status, "READY");
    assert_eq!(
        fs::read(evidence_root.join(&asset.relative_path)).expect("read published still"),
        jpeg
    );
    assert!(evidence_root.join(&asset.thumbnail_relative_path).is_file());

    let trashed = recorder
        .trash_asset(&TrashEvidenceAssetInput {
            asset_id: asset.id.clone(),
            reason: "operator cleanup".into(),
            actor: "operator".into(),
        })
        .expect("move still to recoverable trash");
    assert_eq!(trashed.status, "TRASHED");
    assert!(evidence_root.join("trash").join(&asset.id).is_dir());
    assert!(!evidence_root.join("assets").join(&asset.id).exists());

    let restored = recorder
        .restore_asset(&RestoreEvidenceAssetInput {
            asset_id: asset.id.clone(),
            actor: "operator".into(),
        })
        .expect("restore still from trash");
    assert_eq!(restored.status, "READY");
    assert!(evidence_root.join("assets").join(&asset.id).is_dir());

    recorder
        .trash_asset(&TrashEvidenceAssetInput {
            asset_id: asset.id.clone(),
            reason: "retention fixture".into(),
            actor: "operator".into(),
        })
        .expect("trash still for purge fixture");
    database
        .connection
        .lock()
        .expect("lock purge fixture database")
        .execute(
            "UPDATE evidence_assets SET purge_after_unix_ms = ?2 WHERE id = ?1",
            rusqlite::params![&asset.id, unix_time_ms() - 1],
        )
        .expect("make trash eligible for policy purge");
    recorder
        .apply_retention_policy()
        .expect("purge eligible recoverable trash");
    assert_eq!(
        database
            .evidence_asset(&asset.id)
            .expect("read purged still")
            .status,
        "PURGED"
    );
    assert!(!evidence_root.join("trash").join(&asset.id).exists());

    drop(recorder);
    drop(database);
    remove_sqlite_files(&path);
    let _ = fs::remove_dir_all(evidence_root);
    let _ = fs::remove_file(thumbnailer);
}

#[cfg(unix)]
#[test]
fn track_event_clip_waits_for_verified_segments_then_publishes_media() {
    use sha2::{Digest, Sha256};
    use std::os::unix::fs::PermissionsExt;

    let (database, path) = test_database();
    let now = unix_time_ms();
    database
        .register_agent(&alert_registration_input(
            "clip-agent-session",
            now - 30_000,
        ))
        .expect("register event clip aircraft");
    let mut batch = perception_track_batch("clip-track-session", now);
    batch.current_visible = 1;
    batch.unique_confirmed = 1;
    batch.tracks[0].lifecycle_state = "ACTIVE".into();
    batch.tracks[0].age_frames = 2;
    batch.tracks[0].observation_count = 2;
    batch.tracks[0].update_reason = "STATE_CHANGED".into();
    database
        .record_perception_track_batch(
            "clip-agent-session",
            "alert-source-drone",
            "clip-perception-stream",
            &batch,
            now + 1,
        )
        .expect("persist event clip track");
    let selection = database
        .select_perception_track(
            &SelectTrackInput {
                drone_id: "alert-source-drone".into(),
                track_session_id: batch.track_session_id.clone(),
                track_id: batch.tracks[0].track_id.clone(),
                actor: "operator".into(),
            },
            now + 2,
        )
        .expect("select event clip track");

    let evidence_root = path.with_extension("event-clip-evidence");
    let media_tool = path.with_extension("media-tool.sh");
    fs::write(
        &media_tool,
        b"#!/bin/sh\ninput=\"\"\nlast=\"\"\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = \"-i\" ]; then\n    shift\n    input=\"$1\"\n  fi\n  last=\"$1\"\n  shift\ndone\ncp \"$input\" \"$last\"\n",
    )
    .expect("write deterministic event clip fixture");
    fs::set_permissions(&media_tool, fs::Permissions::from_mode(0o755))
        .expect("make event clip fixture executable");
    let database = Arc::new(database);
    let recorder = crate::recording::EvidenceRecorder::for_test(
        Arc::clone(&database),
        crate::video::VideoSourceConfig {
            rtsp_url: "rtsp://camera/source".into(),
            rtsp_transport: "tcp".into(),
            decoder_path: media_tool.to_string_lossy().into_owned(),
            source_id: "test-source".into(),
        },
        evidence_root.clone(),
        1_000,
        500,
        Some(10_000),
    )
    .expect("create event clip recorder");
    let recording_input = super::recordings::test_create_recording_input(
        "alert-source-drone",
        &evidence_root.to_string_lossy(),
        now - 25_000,
    );
    let recording = database
        .create_evidence_recording(&recording_input)
        .expect("create event clip recording");
    database
        .mark_evidence_recording_running(&recording.id, now - 24_000)
        .expect("start event clip recording");
    let marker = database
        .annotate_perception_track(
            &TrackAnnotationInput {
                selection_id: selection.selection_id,
                annotation_type: "EVIDENCE_MARKER".into(),
                body: "Event clip fixture".into(),
                evidence_recording_session_id: Some(recording.id.clone()),
                actor: "operator".into(),
            },
            now + 3,
        )
        .expect("create event clip marker");
    let clip_request = QueueEvidenceEventClipInput {
        evidence_marker_annotation_id: marker.id,
        pre_roll_seconds: 10,
        post_roll_seconds: 10,
        actor: "operator".into(),
    };
    let pending_clip = recorder
        .queue_event_clip(clip_request.clone())
        .expect("queue event clip before segment coverage");
    assert_eq!(pending_clip.status, "PENDING");

    let segment_bytes = b"verified-segment-media";
    let segment_directory = evidence_root.join("objects").join(&recording.id);
    fs::create_dir_all(&segment_directory).expect("create verified segment directory");
    fs::write(segment_directory.join("000000.mp4"), segment_bytes)
        .expect("write verified segment fixture");
    let segment_sha256 = hex::encode(Sha256::digest(segment_bytes));
    database
        .begin_evidence_segment_finalization(&SegmentFinalizationInput {
            recording_session_id: recording.id.clone(),
            sequence: 0,
            temporary_relative_path: format!("temporary/{}/000000.partial.mp4", recording.id),
            final_relative_path: format!("objects/{}/000000.mp4", recording.id),
            source_started_at_unix_ms: now - 20_000,
            source_ended_at_unix_ms: now + 20_000,
            byte_length: segment_bytes.len() as u64,
            sha256: segment_sha256,
            observed_at_unix_ms: now + 4,
        })
        .expect("record event clip segment manifest");
    database
        .complete_evidence_segment_finalization(&recording.id, 0, now + 5)
        .expect("verify event clip segment");

    let clip = recorder
        .queue_event_clip(clip_request)
        .expect("publish track event clip from verified coverage");
    assert_eq!(clip.id, pending_clip.id);
    assert_eq!(clip.status, "READY");
    assert_eq!(clip.asset_type, "EVENT_CLIP");
    assert_eq!(clip.track_id, Some(batch.tracks[0].track_id.clone()));
    assert!(evidence_root.join(&clip.relative_path).is_file());
    assert!(evidence_root.join(&clip.thumbnail_relative_path).is_file());

    drop(recorder);
    drop(database);
    remove_sqlite_files(&path);
    let _ = fs::remove_dir_all(evidence_root);
    let _ = fs::remove_file(media_tool);
}

#[test]
fn evidence_recording_infers_incident_and_mission_from_the_run() {
    let (database, path) = test_database();
    let (incident_id, run_id, _) =
        prepare_running_response(&database, "evidence-response-drone", "RETURN_TO_LAUNCH");
    let evidence_root = path.with_extension("response-evidence");
    let mut input = super::recordings::test_create_recording_input(
        "evidence-response-drone",
        &evidence_root.to_string_lossy(),
        unix_time_ms(),
    );
    input.mission_run_id = Some(run_id.clone());
    let recording = database
        .create_evidence_recording(&input)
        .expect("request run-associated recording");
    assert_eq!(recording.incident_id.as_deref(), Some(incident_id.as_str()));
    assert_eq!(recording.mission_run_id.as_deref(), Some(run_id.as_str()));
    assert!(recording.mission_id.is_some());

    drop(database);
    remove_sqlite_files(&path);
    let _ = fs::remove_dir_all(evidence_root);
}

#[test]
fn recorder_restart_marks_open_partial_as_a_gap_not_valid_evidence() {
    let (database, path) = test_database();
    insert_test_aircraft(
        &database,
        "restart-evidence-drone",
        "Restart Evidence",
        "active",
    );
    let evidence_root = path.with_extension("restart-evidence");
    let now = unix_time_ms();
    let input = super::recordings::test_create_recording_input(
        "restart-evidence-drone",
        &evidence_root.to_string_lossy(),
        now,
    );
    let session = database
        .create_evidence_recording(&input)
        .expect("request interrupted recording");
    database
        .mark_evidence_recording_running(&session.id, now + 1)
        .expect("mark interrupted recording running");
    let temporary_directory = evidence_root.join("temporary").join(&session.id);
    fs::create_dir_all(&temporary_directory).expect("create interrupted temporary directory");
    let partial_path = temporary_directory.join("000000.partial.mp4");
    fs::write(&partial_path, b"unfinished-media").expect("write open partial segment");
    let database = Arc::new(database);
    let recorder = crate::recording::EvidenceRecorder::for_test(
        Arc::clone(&database),
        crate::video::VideoSourceConfig {
            rtsp_url: "rtsp://camera/source".into(),
            rtsp_transport: "tcp".into(),
            decoder_path: "ffmpeg".into(),
            source_id: "test-source".into(),
        },
        evidence_root.clone(),
        1_000,
        500,
        None,
    )
    .expect("recover interrupted recorder");
    let recovered = database
        .evidence_recording_session(&session.id)
        .expect("read recovered recording");
    assert_eq!(recovered.status, "FAILED");
    assert!(recovered.segments.is_empty());
    assert_eq!(recovered.gaps.len(), 1);
    assert_eq!(recovered.gaps[0].cause, "RECORDER_RESTART");
    assert!(
        partial_path.exists(),
        "partial bytes remain quarantined under temporary storage"
    );
    let alerts = database
        .operational_alerts(false, 100)
        .expect("read restart evidence alert");
    assert!(alerts.alerts.iter().any(|alert| {
        alert.alert_type == "EVIDENCE_RECORDING_GAP"
            && alert.mission_run_id.is_none()
            && alert.drone_id.as_deref() == Some("restart-evidence-drone")
    }));

    drop(recorder);
    drop(database);
    remove_sqlite_files(&path);
    let _ = fs::remove_dir_all(evidence_root);
}

#[test]
fn low_disk_guard_fails_before_spawn_and_emits_gap_and_alert() {
    let (database, path) = test_database();
    insert_test_aircraft(&database, "low-disk-drone", "Low Disk", "active");
    let evidence_root = path.with_extension("low-disk-evidence");
    fs::create_dir_all(&evidence_root).expect("create low-disk evidence root");
    let stop_threshold = 1;
    let database = Arc::new(database);
    let recorder = crate::recording::EvidenceRecorder::for_test(
        Arc::clone(&database),
        crate::video::VideoSourceConfig {
            rtsp_url: "rtsp://camera/source".into(),
            rtsp_transport: "tcp".into(),
            decoder_path: "recorder-must-not-spawn".into(),
            source_id: "low-disk-source".into(),
        },
        evidence_root.clone(),
        stop_threshold,
        stop_threshold,
        Some(0),
    )
    .expect("create low-disk recorder");
    let result = recorder
        .start(StartEvidenceRecordingInput {
            drone_id: "low-disk-drone".into(),
            incident_id: None,
            mission_id: None,
            mission_run_id: None,
        })
        .expect("low-disk refusal is a durable failed lifecycle");
    assert_eq!(result.status, "FAILED");
    assert_eq!(result.error_code, "LOW_DISK_SPACE");
    assert_eq!(result.gaps.len(), 1);
    assert_eq!(result.gaps[0].cause, "LOW_DISK_SPACE");
    assert!(result.segments.is_empty());
    let alerts = database
        .operational_alerts(false, 100)
        .expect("read low-disk alerts");
    assert!(alerts
        .alerts
        .iter()
        .any(|alert| alert.alert_type == "EVIDENCE_STORAGE_LOW" && alert.severity == "CRITICAL"));

    drop(recorder);
    drop(database);
    remove_sqlite_files(&path);
    let _ = fs::remove_dir_all(evidence_root);
}

#[test]
fn recorder_directory_setup_failure_is_terminal_and_releases_the_source() {
    let (database, path) = test_database();
    insert_test_aircraft(&database, "setup-failure-drone", "Setup Failure", "active");
    let evidence_root = path.with_extension("setup-failure-evidence");
    let database = Arc::new(database);
    let recorder = crate::recording::EvidenceRecorder::for_test(
        Arc::clone(&database),
        crate::video::VideoSourceConfig {
            rtsp_url: "rtsp://camera/source".into(),
            rtsp_transport: "tcp".into(),
            decoder_path: "recorder-must-not-spawn".into(),
            source_id: "setup-failure-source".into(),
        },
        evidence_root.clone(),
        1_000,
        500,
        Some(10_000),
    )
    .expect("create setup-failure recorder");
    let temporary_root = evidence_root.join("temporary");
    fs::remove_dir_all(&temporary_root).expect("remove temporary directory fixture");
    fs::write(&temporary_root, b"not-a-directory").expect("replace temporary root with file");
    let input = StartEvidenceRecordingInput {
        drone_id: "setup-failure-drone".into(),
        incident_id: None,
        mission_id: None,
        mission_run_id: None,
    };

    let first = recorder
        .start(input.clone())
        .expect("setup failure must be represented by a durable lifecycle");
    assert_eq!(first.status, "FAILED");
    assert_eq!(first.error_code, "RECORDER_SETUP_FAILED");
    assert_eq!(first.gaps.len(), 1);

    let second = recorder
        .start(input)
        .expect("a failed setup must not retain the active-source lock");
    assert_eq!(second.status, "FAILED");
    assert_eq!(second.error_code, "RECORDER_SETUP_FAILED");
    assert_ne!(second.id, first.id);

    drop(recorder);
    drop(database);
    remove_sqlite_files(&path);
    let _ = fs::remove_dir_all(evidence_root);
}

fn prepare_running_response(
    database: &LocalDatabase,
    drone_id: &str,
    failure_policy: &str,
) -> (String, String, u32) {
    insert_test_aircraft(database, drone_id, "Arrival Test", "active");
    let incident = database
        .create_incident(&sample_incident_input())
        .expect("create response incident");
    let prepared = database
        .prepare_incident_response(
            &incident.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                drone_id: drone_id.into(),
                staging_latitude: 51.501,
                staging_longitude: -0.141,
                altitude_meters: 35.0,
                speed_mps: 6.0,
                arrival_failure_policy: failure_policy.into(),
                point_gimbal_at_incident: false,
                incident_target_altitude_amsl_meters: None,
            },
        )
        .expect("prepare response with arrival Hold");
    let dispatch = database
        .create_mission_run(&prepared.mission.id, drone_id, Some(&prepared.plan.id))
        .expect("create response run");
    let action_sequence = dispatch.run.actions[0].action_sequence;
    let ready = database
        .apply_mission_run_update(&MissionRunUpdateInput {
            event_id: format!("{drone_id}-uploaded"),
            operation_id: dispatch.operation_id,
            mission_run_id: dispatch.run.id.clone(),
            event_type: "uploaded".into(),
            run_state: "READY".into(),
            occurred_at_unix_ms: unix_time_ms(),
            progress_percent: Some(100.0),
            current_waypoint: None,
            total_waypoints: Some(1),
            error_code: String::new(),
            message: "Uploaded".into(),
            evidence_json: None,
        })
        .expect("mark response uploaded");
    let start = database
        .record_mission_operation_requested(&ready.id, "start")
        .expect("request response start");
    database
        .apply_mission_run_update(&MissionRunUpdateInput {
            event_id: format!("{drone_id}-started"),
            operation_id: start.operation_id,
            mission_run_id: ready.id.clone(),
            event_type: "started".into(),
            run_state: "RUNNING".into(),
            occurred_at_unix_ms: unix_time_ms(),
            progress_percent: Some(0.0),
            current_waypoint: Some(0),
            total_waypoints: Some(1),
            error_code: String::new(),
            message: "Started".into(),
            evidence_json: None,
        })
        .expect("start response run");
    database
        .apply_mission_run_update(&MissionRunUpdateInput {
            event_id: format!("{drone_id}-final-waypoint"),
            operation_id: String::new(),
            mission_run_id: ready.id.clone(),
            event_type: "progress".into(),
            run_state: "RUNNING".into(),
            occurred_at_unix_ms: unix_time_ms(),
            progress_percent: Some(100.0),
            current_waypoint: Some(1),
            total_waypoints: Some(1),
            error_code: String::new(),
            message: "Final waypoint reached; awaiting Hold".into(),
            evidence_json: None,
        })
        .expect("record final waypoint without completion");
    (incident.incident.id, ready.id, action_sequence)
}

fn insert_test_aircraft(database: &LocalDatabase, id: &str, name: &str, status: &str) {
    database
        .connection
        .lock()
        .expect("lock database")
        .execute(
            "INSERT INTO drones (id, name, vehicle_type, status, created_at_unix_ms, updated_at_unix_ms) VALUES (?1, ?2, 'multicopter', ?3, 1, 1)",
            rusqlite::params![id, name, status],
        )
        .expect("insert test aircraft");
}

fn sample_incident_input() -> CreateIncidentInput {
    CreateIncidentInput {
        incident_type: "Missing person".into(),
        priority: "high".into(),
        summary: "Search requested near the south trail".into(),
        description: "Last observed beside the wooded trail.".into(),
        latitude: Some(51.5),
        longitude: Some(-0.14),
        address: "South trail entrance".into(),
        area: "South sector".into(),
        occurred_at_unix_ms: Some(1_700_000_000_000),
    }
}

fn alert_registration_input(session_id: &str, observed_at_unix_ms: i64) -> RegistrationInput {
    RegistrationInput {
        session_id: session_id.into(),
        installation_id: "alert-agent".into(),
        agent_version: "0.1.0".into(),
        protocol_version: "1".into(),
        device_profile_json: r#"{"hostname":"alert-test"}"#.into(),
        capabilities_json: r#"["registration","heartbeat","telemetry"]"#.into(),
        drone_id: "alert-source-drone".into(),
        drone_name: "Alert Source Test".into(),
        flight_controller_uid: "alert-source-fc".into(),
        serial_number: "alert-source-serial".into(),
        vehicle_type: "multicopter".into(),
        flight_controller_transport: "serial".into(),
        endpoint_description: "/dev/alert-test".into(),
        baud_rate: 921_600,
        mavlink_system_id: 1,
        mavlink_component_id: 1,
        remote_address: "127.0.0.1:47000".into(),
        observed_at_unix_ms,
    }
}

fn perception_track_batch(
    track_session_id: &str,
    observed_at_unix_ms: i64,
) -> PerceptionTrackBatchInput {
    PerceptionTrackBatchInput {
        source_id: "a8-main".into(),
        stream_epoch: "epoch-1".into(),
        track_session_id: track_session_id.into(),
        tracker_type: "BYTE_TRACK".into(),
        observed_at_unix_ms,
        session_started: true,
        session_ended: false,
        session_end_reason: String::new(),
        current_visible: 0,
        unique_confirmed: 0,
        tracks: vec![PerceptionTrackUpdateInput {
            track_id: format!("atlas:{track_session_id}:1"),
            track_session_id: track_session_id.into(),
            tracker_type: "BYTE_TRACK".into(),
            lifecycle_state: "TENTATIVE".into(),
            revision: 1,
            age_frames: 1,
            observation_count: 1,
            first_observed_at_unix_ms: observed_at_unix_ms,
            last_observed_at_unix_ms: observed_at_unix_ms,
            latest_confirmed_box: TrackBoundingBoxInput {
                x: 0.1,
                y: 0.2,
                width: 0.1,
                height: 0.2,
            },
            latest_detection_confidence: 0.9,
            predicted_box: None,
            prediction_confidence: 0.0,
            closed_at_unix_ms: None,
            closure_reason: String::new(),
            class_id: 0,
            class_label: "person".into(),
            update_reason: "CREATED".into(),
        }],
        rule_counts: Vec::new(),
        count_events: Vec::new(),
    }
}

fn suitability_registration_input(
    drone_id: &str,
    session_id: &str,
    observed_at_unix_ms: i64,
) -> RegistrationInput {
    RegistrationInput {
        session_id: session_id.into(),
        installation_id: format!("{drone_id}-agent"),
        agent_version: "0.1.0".into(),
        protocol_version: "1".into(),
        device_profile_json: r#"{"hostname":"suitability-test"}"#.into(),
        capabilities_json: r#"["mission:upload","mission:start","mission:action:hold_at_arrival"]"#
            .into(),
        drone_id: drone_id.into(),
        drone_name: drone_id.replace('-', " "),
        flight_controller_uid: format!("{drone_id}-fc"),
        serial_number: format!("{drone_id}-serial"),
        vehicle_type: "multicopter".into(),
        flight_controller_transport: "serial".into(),
        endpoint_description: format!("/dev/{drone_id}"),
        baud_rate: 921_600,
        mavlink_system_id: 1,
        mavlink_component_id: 1,
        remote_address: "127.0.0.1:47000".into(),
        observed_at_unix_ms,
    }
}

fn dispatch_ready_telemetry(
    received_at_unix_ms: i64,
    latitude: f64,
    longitude: f64,
    battery_percent: f64,
) -> TelemetryInput {
    TelemetryInput {
        source: "mavsdk".into(),
        agent_observed_at_unix_ms: received_at_unix_ms,
        received_at_unix_ms,
        battery_percent: Some(battery_percent),
        relative_altitude_m: Some(0.0),
        flight_mode: Some("HOLD".into()),
        armed: Some(false),
        in_air: Some(false),
        latitude: Some(latitude),
        longitude: Some(longitude),
        heading_deg: Some(180.0),
        ground_speed_mps: Some(0.0),
        gps_fix: Some("3D".into()),
        satellites_visible: Some(14),
        home_position_set: Some(true),
        batteries: Vec::new(),
        health: Some(VehicleHealth {
            gyrometer_calibration_ok: true,
            accelerometer_calibration_ok: true,
            magnetometer_calibration_ok: true,
            local_position_ok: true,
            global_position_ok: true,
            home_position_ok: true,
            armable: true,
        }),
        absolute_altitude_m: Some(32.0),
        terrain_altitude_m: None,
        bottom_clearance_m: None,
        velocity_north_mps: Some(0.0),
        velocity_east_mps: Some(0.0),
        velocity_down_mps: Some(0.0),
        climb_rate_mps: Some(0.0),
        landed_state: Some("ON_GROUND".into()),
        rc_status: None,
        home_position: Some(HomePosition {
            latitude: Some(latitude),
            longitude: Some(longitude),
            absolute_altitude_m: Some(32.0),
            relative_altitude_m: Some(0.0),
        }),
        gps_quality: None,
    }
}

fn alert_telemetry(
    received_at_unix_ms: i64,
    battery_percent: f64,
    position_available: bool,
) -> TelemetryInput {
    TelemetryInput {
        source: "mavsdk".into(),
        agent_observed_at_unix_ms: received_at_unix_ms,
        received_at_unix_ms,
        battery_percent: Some(battery_percent),
        relative_altitude_m: Some(if position_available { 0.0 } else { 25.0 }),
        flight_mode: Some("HOLD".into()),
        armed: Some(!position_available),
        in_air: Some(!position_available),
        latitude: position_available.then_some(51.5074),
        longitude: position_available.then_some(-0.1278),
        heading_deg: Some(180.0),
        ground_speed_mps: Some(0.0),
        gps_fix: position_available.then(|| "3D".into()),
        satellites_visible: position_available.then_some(14),
        home_position_set: Some(position_available),
        batteries: Vec::new(),
        health: Some(VehicleHealth {
            gyrometer_calibration_ok: true,
            accelerometer_calibration_ok: true,
            magnetometer_calibration_ok: true,
            local_position_ok: position_available,
            global_position_ok: position_available,
            home_position_ok: position_available,
            armable: position_available,
        }),
        absolute_altitude_m: position_available.then_some(32.0),
        terrain_altitude_m: None,
        bottom_clearance_m: None,
        velocity_north_mps: Some(0.0),
        velocity_east_mps: Some(0.0),
        velocity_down_mps: Some(0.0),
        climb_rate_mps: Some(0.0),
        landed_state: Some(if position_available {
            "ON_GROUND".into()
        } else {
            "IN_AIR".into()
        }),
        rc_status: None,
        home_position: None,
        gps_quality: None,
    }
}

fn test_database() -> (LocalDatabase, PathBuf) {
    let path = test_database_path();
    let database = LocalDatabase::open_path(path.clone()).expect("database should open");
    (database, path)
}

fn test_database_path() -> PathBuf {
    let unique = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("clock should be after epoch")
        .as_nanos();
    let counter = TEST_DATABASE_COUNTER.fetch_add(1, Ordering::Relaxed);
    let path = std::env::temp_dir().join(format!(
        "atlas-test-{}-{unique}-{counter}.db",
        std::process::id()
    ));
    path
}

fn unix_time_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as i64
}

fn remove_sqlite_files(path: &Path) {
    for candidate in [
        path.to_path_buf(),
        PathBuf::from(format!("{}-wal", path.display())),
        PathBuf::from(format!("{}-shm", path.display())),
    ] {
        let _ = fs::remove_file(candidate);
    }
}
