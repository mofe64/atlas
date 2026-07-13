use rusqlite::Connection;

const MINIMUM_SAFE_SQLITE_VERSION: (u32, u32, u32) = (3, 51, 3);

pub(super) fn run(connection: &Connection) -> Result<(), String> {
    let current_version: u32 = connection
        .query_row("PRAGMA user_version", [], |row| row.get(0))
        .map_err(|error| format!("read local database schema version: {error}"))?;
    if current_version > 11 {
        return Err(format!(
            "local database schema version {current_version} is newer than this Atlas build"
        ));
    }
    if current_version < 2 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;
                DROP TABLE IF EXISTS cached_identity;

                CREATE TABLE IF NOT EXISTS drones (
                    id TEXT PRIMARY KEY,
                    name TEXT NOT NULL,
                    flight_controller_uid TEXT UNIQUE,
                    serial_number TEXT NOT NULL DEFAULT '',
                    vehicle_type TEXT NOT NULL DEFAULT 'unknown'
                        CHECK (vehicle_type IN ('unknown', 'multicopter', 'fixed_wing', 'vtol', 'rover')),
                    status TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'maintenance', 'disabled', 'archived')),
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL
                );

                CREATE TABLE IF NOT EXISTS vehicle_agents (
                    id TEXT PRIMARY KEY,
                    installation_id TEXT NOT NULL UNIQUE,
                    agent_version TEXT NOT NULL,
                    protocol_version TEXT NOT NULL,
                    device_profile_json TEXT NOT NULL DEFAULT '{}',
                    capabilities_json TEXT NOT NULL DEFAULT '[]',
                    registered_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL
                );

                CREATE TABLE IF NOT EXISTS vehicle_agent_bindings (
                    id TEXT PRIMARY KEY,
                    vehicle_agent_id TEXT NOT NULL REFERENCES vehicle_agents(id) ON DELETE RESTRICT,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    status TEXT NOT NULL CHECK (status IN ('active', 'suspended', 'ended')),
                    flight_controller_transport TEXT NOT NULL DEFAULT '',
                    endpoint_description TEXT NOT NULL DEFAULT '',
                    baud_rate INTEGER,
                    mavlink_system_id INTEGER,
                    mavlink_component_id INTEGER,
                    observed_flight_controller_uid TEXT NOT NULL DEFAULT '',
                    bound_at_unix_ms INTEGER NOT NULL,
                    ended_at_unix_ms INTEGER,
                    end_reason TEXT NOT NULL DEFAULT ''
                );
                CREATE UNIQUE INDEX IF NOT EXISTS vehicle_agent_bindings_one_current_agent
                    ON vehicle_agent_bindings(vehicle_agent_id) WHERE status IN ('active', 'suspended');
                CREATE UNIQUE INDEX IF NOT EXISTS vehicle_agent_bindings_one_current_drone
                    ON vehicle_agent_bindings(drone_id) WHERE status IN ('active', 'suspended');

                CREATE TABLE IF NOT EXISTS communication_links (
                    id TEXT PRIMARY KEY,
                    vehicle_agent_binding_id TEXT NOT NULL REFERENCES vehicle_agent_bindings(id) ON DELETE RESTRICT,
                    session_instance_id TEXT NOT NULL UNIQUE,
                    transport TEXT NOT NULL,
                    roles_json TEXT NOT NULL DEFAULT '[]',
                    status TEXT NOT NULL CHECK (status IN ('connected', 'stale', 'disconnected')),
                    remote_address TEXT NOT NULL DEFAULT '',
                    started_at_unix_ms INTEGER NOT NULL,
                    last_heartbeat_at_unix_ms INTEGER,
                    ended_at_unix_ms INTEGER,
                    ended_reason TEXT NOT NULL DEFAULT ''
                );
                CREATE INDEX IF NOT EXISTS communication_links_binding_started
                    ON communication_links(vehicle_agent_binding_id, started_at_unix_ms DESC);

                PRAGMA user_version = 2;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 2: {error}"))?;
    }
    if current_version < 3 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;
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
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 3: {error}"))?;
    }
    if current_version < 4 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;
                ALTER TABLE aircraft_telemetry_current
                    ADD COLUMN batteries_json TEXT NOT NULL DEFAULT '[]';
                ALTER TABLE aircraft_telemetry_current ADD COLUMN health_json TEXT;
                ALTER TABLE aircraft_telemetry_current ADD COLUMN absolute_altitude_m REAL;
                ALTER TABLE aircraft_telemetry_current ADD COLUMN terrain_altitude_m REAL;
                ALTER TABLE aircraft_telemetry_current ADD COLUMN bottom_clearance_m REAL;
                ALTER TABLE aircraft_telemetry_current ADD COLUMN velocity_north_mps REAL;
                ALTER TABLE aircraft_telemetry_current ADD COLUMN velocity_east_mps REAL;
                ALTER TABLE aircraft_telemetry_current ADD COLUMN velocity_down_mps REAL;
                ALTER TABLE aircraft_telemetry_current ADD COLUMN climb_rate_mps REAL;
                ALTER TABLE aircraft_telemetry_current ADD COLUMN landed_state TEXT;
                ALTER TABLE aircraft_telemetry_current ADD COLUMN rc_status_json TEXT;
                ALTER TABLE aircraft_telemetry_current ADD COLUMN home_position_json TEXT;
                ALTER TABLE aircraft_telemetry_current ADD COLUMN gps_quality_json TEXT;

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

                PRAGMA user_version = 4;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 4: {error}"))?;
    }
    if current_version < 5 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                ALTER TABLE aircraft_telemetry_current
                    RENAME TO vehicle_telemetry_current;
                ALTER TABLE aircraft_status_events
                    RENAME TO vehicle_status_events;

                DROP INDEX aircraft_status_events_drone_received;
                CREATE INDEX vehicle_status_events_drone_received
                    ON vehicle_status_events(
                        drone_id,
                        received_at_unix_ms DESC,
                        agent_observed_at_unix_ms DESC
                    );

                CREATE TABLE vehicle_telemetry_snapshots (
                    id TEXT PRIMARY KEY,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
                    communication_link_id TEXT NOT NULL
                        REFERENCES communication_links(id) ON DELETE RESTRICT,
                    capture_reason TEXT NOT NULL
                        CHECK (capture_reason IN ('initial', 'interval', 'state_transition')),
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
                CREATE INDEX vehicle_telemetry_snapshots_drone_received
                    ON vehicle_telemetry_snapshots(
                        drone_id,
                        received_at_unix_ms DESC,
                        agent_observed_at_unix_ms DESC
                    );
                CREATE INDEX vehicle_telemetry_snapshots_link_received
                    ON vehicle_telemetry_snapshots(
                        communication_link_id,
                        received_at_unix_ms DESC
                    );

                PRAGMA user_version = 5;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 5: {error}"))?;
    }
    if current_version < 6 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                ALTER TABLE vehicle_status_events
                    ADD COLUMN origin TEXT NOT NULL DEFAULT 'px4'
                    CHECK (origin IN ('px4', 'atlas_agent', 'atlas_native'));
                ALTER TABLE vehicle_status_events
                    ADD COLUMN event_type TEXT NOT NULL DEFAULT 'status_text';
                ALTER TABLE vehicle_status_events ADD COLUMN code TEXT;
                ALTER TABLE vehicle_status_events ADD COLUMN details_json TEXT;

                DROP INDEX vehicle_telemetry_snapshots_drone_received;
                CREATE INDEX vehicle_telemetry_snapshots_drone_received
                    ON vehicle_telemetry_snapshots(
                        drone_id,
                        received_at_unix_ms DESC,
                        id DESC
                    );
                CREATE INDEX vehicle_telemetry_snapshots_received
                    ON vehicle_telemetry_snapshots(received_at_unix_ms);

                PRAGMA user_version = 6;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 6: {error}"))?;
    }
    if current_version < 7 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                CREATE TABLE vehicle_commands (
                    id TEXT PRIMARY KEY,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
                    command_type TEXT NOT NULL
                        CHECK (command_type IN ('hold', 'return_to_launch', 'land')),
                    parameters_json TEXT NOT NULL DEFAULT '{}',
                    status TEXT NOT NULL
                        CHECK (status IN ('created', 'sent', 'accepted', 'rejected',
                                         'executing', 'succeeded', 'failed', 'timed_out',
                                         'cancelled')),
                    idempotency_key TEXT NOT NULL UNIQUE,
                    created_at_unix_ms INTEGER NOT NULL,
                    deadline_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    accepted_at_unix_ms INTEGER,
                    started_at_unix_ms INTEGER,
                    completed_at_unix_ms INTEGER,
                    delivery_attempts INTEGER NOT NULL DEFAULT 0,
                    last_communication_link_id TEXT
                        REFERENCES communication_links(id) ON DELETE RESTRICT,
                    result_code TEXT NOT NULL DEFAULT '',
                    result_message TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT
                );
                CREATE INDEX vehicle_commands_drone_created
                    ON vehicle_commands(drone_id, created_at_unix_ms DESC);
                CREATE INDEX vehicle_commands_deliverable
                    ON vehicle_commands(status, deadline_at_unix_ms);

                CREATE TABLE vehicle_command_events (
                    id TEXT PRIMARY KEY,
                    vehicle_command_id TEXT NOT NULL
                        REFERENCES vehicle_commands(id) ON DELETE CASCADE,
                    sequence INTEGER NOT NULL,
                    event_type TEXT NOT NULL,
                    state TEXT NOT NULL,
                    communication_link_id TEXT
                        REFERENCES communication_links(id) ON DELETE RESTRICT,
                    source TEXT NOT NULL
                        CHECK (source IN ('atlas_native', 'atlas_agent', 'mavsdk', 'telemetry')),
                    occurred_at_unix_ms INTEGER NOT NULL,
                    progress_percent REAL,
                    result_code TEXT NOT NULL DEFAULT '',
                    message TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT,
                    UNIQUE(vehicle_command_id, sequence)
                );
                CREATE INDEX vehicle_command_events_command_sequence
                    ON vehicle_command_events(vehicle_command_id, sequence);

                PRAGMA user_version = 7;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 7: {error}"))?;
    }
    if current_version < 8 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                -- Definitions and generated plans are intentionally separate from
                -- executions. Reusing a definition creates a new mission_run later.
                CREATE TABLE missions (
                    id TEXT PRIMARY KEY,
                    template_id TEXT NOT NULL,
                    template_type TEXT NOT NULL
                        CHECK (template_type IN ('WAYPOINT', 'AREA_SCAN', 'ROUTE_SCAN')),
                    name TEXT NOT NULL,
                    description TEXT NOT NULL DEFAULT '',
                    status TEXT NOT NULL
                        CHECK (status IN ('DRAFT', 'PLANNED', 'READY', 'RUNNING', 'PAUSED',
                                          'COMPLETED', 'FAILED', 'CANCELLED')),
                    params_json TEXT NOT NULL,
                    selected_pattern TEXT NOT NULL
                        CHECK (selected_pattern IN ('DIRECT_WAYPOINTS', 'LAWN_MOWER', 'ROUTE_FOLLOW')),
                    generated_plan_id TEXT,
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL
                );
                CREATE INDEX missions_updated ON missions(updated_at_unix_ms DESC);

                CREATE TABLE mission_plans (
                    id TEXT PRIMARY KEY,
                    mission_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
                    template_type TEXT NOT NULL,
                    pattern_type TEXT NOT NULL,
                    status TEXT NOT NULL CHECK (status IN ('PLANNED', 'READY', 'INVALID')),
                    metadata_json TEXT NOT NULL DEFAULT '{}',
                    validation_warnings_json TEXT NOT NULL DEFAULT '[]',
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL
                );
                CREATE INDEX mission_plans_mission_created
                    ON mission_plans(mission_id, created_at_unix_ms DESC);

                CREATE TABLE mission_items (
                    mission_plan_id TEXT NOT NULL REFERENCES mission_plans(id) ON DELETE CASCADE,
                    sequence INTEGER NOT NULL,
                    latitude REAL NOT NULL,
                    longitude REAL NOT NULL,
                    altitude_meters REAL NOT NULL,
                    speed_mps REAL,
                    heading_degrees REAL,
                    hold_seconds REAL,
                    action_refs_json TEXT NOT NULL DEFAULT '[]',
                    PRIMARY KEY (mission_plan_id, sequence)
                );

                CREATE TABLE mission_actions (
                    mission_plan_id TEXT NOT NULL REFERENCES mission_plans(id) ON DELETE CASCADE,
                    sequence INTEGER NOT NULL,
                    action_type TEXT NOT NULL,
                    params_json TEXT NOT NULL DEFAULT '{}',
                    PRIMARY KEY (mission_plan_id, sequence)
                );

                CREATE TABLE mission_runs (
                    id TEXT PRIMARY KEY,
                    mission_id TEXT NOT NULL REFERENCES missions(id) ON DELETE RESTRICT,
                    mission_plan_id TEXT NOT NULL REFERENCES mission_plans(id) ON DELETE RESTRICT,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    status TEXT NOT NULL,
                    current_waypoint INTEGER,
                    created_at_unix_ms INTEGER NOT NULL,
                    started_at_unix_ms INTEGER,
                    completed_at_unix_ms INTEGER,
                    error_code TEXT NOT NULL DEFAULT '',
                    error_message TEXT NOT NULL DEFAULT ''
                );
                CREATE INDEX mission_runs_mission_created
                    ON mission_runs(mission_id, created_at_unix_ms DESC);

                CREATE TABLE mission_run_events (
                    id TEXT PRIMARY KEY,
                    mission_run_id TEXT NOT NULL REFERENCES mission_runs(id) ON DELETE CASCADE,
                    sequence INTEGER NOT NULL,
                    event_type TEXT NOT NULL,
                    state TEXT NOT NULL,
                    occurred_at_unix_ms INTEGER NOT NULL,
                    current_waypoint INTEGER,
                    message TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT,
                    UNIQUE(mission_run_id, sequence)
                );

                PRAGMA user_version = 8;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 8: {error}"))?;
    }
    if current_version < 9 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                DROP INDEX vehicle_command_events_command_sequence;
                DROP INDEX vehicle_commands_drone_created;
                DROP INDEX vehicle_commands_deliverable;
                ALTER TABLE vehicle_command_events RENAME TO vehicle_command_events_v7;
                ALTER TABLE vehicle_commands RENAME TO vehicle_commands_v7;

                CREATE TABLE vehicle_commands (
                    id TEXT PRIMARY KEY,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
                    command_type TEXT NOT NULL
                        CHECK (command_type IN ('hold', 'return_to_launch', 'land',
                                               'gimbal_set_angles', 'gimbal_set_rates',
                                               'gimbal_center')),
                    parameters_json TEXT NOT NULL DEFAULT '{}',
                    status TEXT NOT NULL
                        CHECK (status IN ('created', 'sent', 'accepted', 'rejected',
                                         'executing', 'succeeded', 'failed', 'timed_out',
                                         'cancelled')),
                    idempotency_key TEXT NOT NULL UNIQUE,
                    created_at_unix_ms INTEGER NOT NULL,
                    deadline_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    accepted_at_unix_ms INTEGER,
                    started_at_unix_ms INTEGER,
                    completed_at_unix_ms INTEGER,
                    delivery_attempts INTEGER NOT NULL DEFAULT 0,
                    last_communication_link_id TEXT
                        REFERENCES communication_links(id) ON DELETE RESTRICT,
                    result_code TEXT NOT NULL DEFAULT '',
                    result_message TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT
                );
                INSERT INTO vehicle_commands SELECT * FROM vehicle_commands_v7;
                CREATE INDEX vehicle_commands_drone_created
                    ON vehicle_commands(drone_id, created_at_unix_ms DESC);
                CREATE INDEX vehicle_commands_deliverable
                    ON vehicle_commands(status, deadline_at_unix_ms);

                CREATE TABLE vehicle_command_events (
                    id TEXT PRIMARY KEY,
                    vehicle_command_id TEXT NOT NULL
                        REFERENCES vehicle_commands(id) ON DELETE CASCADE,
                    sequence INTEGER NOT NULL,
                    event_type TEXT NOT NULL,
                    state TEXT NOT NULL,
                    communication_link_id TEXT
                        REFERENCES communication_links(id) ON DELETE RESTRICT,
                    source TEXT NOT NULL
                        CHECK (source IN ('atlas_native', 'atlas_agent', 'mavsdk', 'telemetry')),
                    occurred_at_unix_ms INTEGER NOT NULL,
                    progress_percent REAL,
                    result_code TEXT NOT NULL DEFAULT '',
                    message TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT,
                    UNIQUE(vehicle_command_id, sequence)
                );
                INSERT INTO vehicle_command_events SELECT * FROM vehicle_command_events_v7;
                CREATE INDEX vehicle_command_events_command_sequence
                    ON vehicle_command_events(vehicle_command_id, sequence);

                DROP TABLE vehicle_command_events_v7;
                DROP TABLE vehicle_commands_v7;
                PRAGMA user_version = 9;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 9: {error}"))?;
    }
    if current_version < 10 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                ALTER TABLE mission_runs ADD COLUMN progress_percent REAL NOT NULL DEFAULT 0;
                ALTER TABLE mission_runs ADD COLUMN upload_progress_percent REAL NOT NULL DEFAULT 0;
                ALTER TABLE mission_runs ADD COLUMN total_waypoints INTEGER NOT NULL DEFAULT 0;
                ALTER TABLE mission_runs ADD COLUMN updated_at_unix_ms INTEGER NOT NULL DEFAULT 0;
                ALTER TABLE mission_runs ADD COLUMN uploaded_at_unix_ms INTEGER;
                ALTER TABLE mission_runs ADD COLUMN paused_at_unix_ms INTEGER;

                ALTER TABLE mission_run_events ADD COLUMN operation_id TEXT;
                ALTER TABLE mission_run_events ADD COLUMN source TEXT NOT NULL DEFAULT 'atlas_native';
                ALTER TABLE mission_run_events ADD COLUMN progress_percent REAL;
                ALTER TABLE mission_run_events ADD COLUMN total_waypoints INTEGER;
                ALTER TABLE mission_run_events ADD COLUMN error_code TEXT NOT NULL DEFAULT '';

                UPDATE mission_runs
                SET updated_at_unix_ms = COALESCE(completed_at_unix_ms, started_at_unix_ms, created_at_unix_ms);

                CREATE INDEX mission_runs_drone_created
                    ON mission_runs(drone_id, created_at_unix_ms DESC);
                CREATE UNIQUE INDEX mission_runs_one_active_per_drone
                    ON mission_runs(drone_id)
                    WHERE completed_at_unix_ms IS NULL;
                CREATE INDEX mission_run_events_run_sequence
                    ON mission_run_events(mission_run_id, sequence);

                PRAGMA user_version = 10;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 10: {error}"))?;
    }
    if current_version < 11 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                DROP INDEX vehicle_command_events_command_sequence;
                DROP INDEX vehicle_commands_drone_created;
                DROP INDEX vehicle_commands_deliverable;
                ALTER TABLE vehicle_command_events RENAME TO vehicle_command_events_v10;
                ALTER TABLE vehicle_commands RENAME TO vehicle_commands_v10;

                CREATE TABLE vehicle_commands (
                    id TEXT PRIMARY KEY,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
                    command_type TEXT NOT NULL
                        CHECK (command_type IN ('hold', 'return_to_launch', 'land',
                                               'gimbal_set_angles', 'gimbal_set_rates',
                                               'gimbal_center', 'gimbal_set_roi',
                                               'camera_set_zoom', 'payload_control_begin',
                                               'payload_control_renew', 'payload_control_end')),
                    parameters_json TEXT NOT NULL DEFAULT '{}',
                    status TEXT NOT NULL
                        CHECK (status IN ('created', 'sent', 'accepted', 'rejected',
                                         'executing', 'succeeded', 'failed', 'timed_out',
                                         'cancelled')),
                    idempotency_key TEXT NOT NULL UNIQUE,
                    created_at_unix_ms INTEGER NOT NULL,
                    deadline_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    accepted_at_unix_ms INTEGER,
                    started_at_unix_ms INTEGER,
                    completed_at_unix_ms INTEGER,
                    delivery_attempts INTEGER NOT NULL DEFAULT 0,
                    last_communication_link_id TEXT
                        REFERENCES communication_links(id) ON DELETE RESTRICT,
                    result_code TEXT NOT NULL DEFAULT '',
                    result_message TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT
                );
                INSERT INTO vehicle_commands SELECT * FROM vehicle_commands_v10;
                CREATE INDEX vehicle_commands_drone_created
                    ON vehicle_commands(drone_id, created_at_unix_ms DESC);
                CREATE INDEX vehicle_commands_deliverable
                    ON vehicle_commands(status, deadline_at_unix_ms);

                CREATE TABLE vehicle_command_events (
                    id TEXT PRIMARY KEY,
                    vehicle_command_id TEXT NOT NULL
                        REFERENCES vehicle_commands(id) ON DELETE CASCADE,
                    sequence INTEGER NOT NULL,
                    event_type TEXT NOT NULL,
                    state TEXT NOT NULL,
                    communication_link_id TEXT
                        REFERENCES communication_links(id) ON DELETE RESTRICT,
                    source TEXT NOT NULL
                        CHECK (source IN ('atlas_native', 'atlas_agent', 'mavsdk', 'telemetry')),
                    occurred_at_unix_ms INTEGER NOT NULL,
                    progress_percent REAL,
                    result_code TEXT NOT NULL DEFAULT '',
                    message TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT,
                    UNIQUE(vehicle_command_id, sequence)
                );
                INSERT INTO vehicle_command_events SELECT * FROM vehicle_command_events_v10;
                CREATE INDEX vehicle_command_events_command_sequence
                    ON vehicle_command_events(vehicle_command_id, sequence);

                DROP TABLE vehicle_command_events_v10;
                DROP TABLE vehicle_commands_v10;
                PRAGMA user_version = 11;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 11: {error}"))?;
    }
    Ok(())
}

pub(super) fn ensure_safe_sqlite_version(version: &str) -> Result<(), String> {
    let components = version
        .split('.')
        .take(3)
        .map(|part| part.parse::<u32>())
        .collect::<Result<Vec<_>, _>>()
        .map_err(|_| format!("SQLite reported an invalid version: {version}"))?;
    if components.len() != 3
        || (components[0], components[1], components[2]) < MINIMUM_SAFE_SQLITE_VERSION
    {
        return Err(format!(
            "SQLite {version} is too old for safe multi-connection WAL use; require 3.51.3 or newer"
        ));
    }
    Ok(())
}
