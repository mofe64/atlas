use std::{
    fs,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use rusqlite::Connection;

use super::{
    explicit_database_path, BatteryTelemetry, GpsQuality, HomePosition, LocalDatabase, RcStatus,
    RegistrationInput, StatusEventInput, TelemetryHistoryQuery, TelemetryInput,
    VehicleCommandUpdateInput, VehicleHealth,
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
    assert_eq!(version, 12);
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
    assert_eq!(version, 12);
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
