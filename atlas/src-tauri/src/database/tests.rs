use std::{
    fs,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use rusqlite::Connection;

use super::{
    explicit_database_path, BatteryTelemetry, CreateIncidentInput, CreateMissionInput, GpsQuality,
    HomePosition, LocalDatabase, MissionActionUpdateInput, MissionRunUpdateInput,
    PrepareIncidentResponseInput, RcStatus, RegistrationInput, StatusEventInput,
    TelemetryHistoryQuery, TelemetryInput, UpdateIncidentInput, VehicleCommandUpdateInput,
    VehicleHealth,
};

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
    assert_eq!(version, 14);
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
    assert_eq!(version, 14);
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
    assert_eq!(
        runtime_actions[0].params["failurePolicy"],
        "RETURN_TO_LAUNCH"
    );
    assert_eq!(runtime_actions[1].action_type, "POINT_GIMBAL_AT_INCIDENT");
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
        database
            .apply_mission_action_update(&MissionActionUpdateInput {
                event_id: event_id.into(),
                mission_run_id: run_id.clone(),
                action_sequence,
                action_type: "HOLD_AT_ARRIVAL".into(),
                state: state.into(),
                attempt,
                failure_policy: "RETURN_TO_LAUNCH".into(),
                occurred_at_unix_ms: unix_time_ms(),
                error_code: error_code.into(),
                message: message.into(),
                evidence_json: (state == "SUCCEEDED").then_some(r#"{"acknowledged":true}"#.into()),
            })
            .expect("persist Hold action update");
    }

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

    drop(database);
    remove_sqlite_files(&path);
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
