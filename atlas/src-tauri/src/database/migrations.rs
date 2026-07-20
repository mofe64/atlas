use rusqlite::Connection;

const MINIMUM_SAFE_SQLITE_VERSION: (u32, u32, u32) = (3, 51, 3);

pub(super) fn run(connection: &Connection) -> Result<(), String> {
    let current_version: u32 = connection
        .query_row("PRAGMA user_version", [], |row| row.get(0))
        .map_err(|error| format!("read local database schema version: {error}"))?;
    if current_version > 24 {
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
    if current_version < 12 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                CREATE TABLE drone_lifecycle_events (
                    id TEXT PRIMARY KEY,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    event_type TEXT NOT NULL
                        CHECK (event_type IN ('archived', 'restored',
                                              'archived_reconnect_rejected')),
                    reason TEXT NOT NULL DEFAULT '',
                    occurred_at_unix_ms INTEGER NOT NULL,
                    details_json TEXT
                );
                CREATE INDEX drone_lifecycle_events_drone_occurred
                    ON drone_lifecycle_events(drone_id, occurred_at_unix_ms DESC);

                PRAGMA user_version = 12;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 12: {error}"))?;
    }
    if current_version < 13 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                CREATE TABLE incidents (
                    id TEXT PRIMARY KEY,
                    source_type TEXT NOT NULL,
                    source_system TEXT NOT NULL,
                    external_id TEXT,
                    incident_type TEXT NOT NULL,
                    priority TEXT NOT NULL
                        CHECK (priority IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
                    status TEXT NOT NULL
                        CHECK (status IN ('OPEN', 'ACTIVE', 'RESOLVED', 'CANCELLED')),
                    summary TEXT NOT NULL,
                    description TEXT NOT NULL DEFAULT '',
                    latitude REAL,
                    longitude REAL,
                    address TEXT NOT NULL DEFAULT '',
                    area TEXT NOT NULL DEFAULT '',
                    occurred_at_unix_ms INTEGER,
                    received_at_unix_ms INTEGER NOT NULL,
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
                    location_revision INTEGER NOT NULL DEFAULT 1
                        CHECK (location_revision >= 1),
                    source_payload_json TEXT,
                    CHECK ((latitude IS NULL AND longitude IS NULL)
                        OR (latitude IS NOT NULL AND longitude IS NOT NULL)),
                    CHECK (latitude IS NULL OR (latitude >= -90 AND latitude <= 90)),
                    CHECK (longitude IS NULL OR (longitude >= -180 AND longitude <= 180))
                );
                CREATE UNIQUE INDEX incidents_external_identity
                    ON incidents(source_system, external_id)
                    WHERE external_id IS NOT NULL;
                CREATE INDEX incidents_status_priority_updated
                    ON incidents(status, priority, updated_at_unix_ms DESC);

                CREATE TABLE incident_events (
                    id TEXT PRIMARY KEY,
                    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE RESTRICT,
                    sequence INTEGER NOT NULL,
                    event_type TEXT NOT NULL,
                    state TEXT NOT NULL,
                    source TEXT NOT NULL,
                    message TEXT NOT NULL DEFAULT '',
                    details_json TEXT NOT NULL DEFAULT '{}',
                    occurred_at_unix_ms INTEGER NOT NULL,
                    received_at_unix_ms INTEGER NOT NULL,
                    UNIQUE(incident_id, sequence)
                );
                CREATE INDEX incident_events_incident_sequence
                    ON incident_events(incident_id, sequence);

                CREATE TABLE incident_assignments (
                    id TEXT PRIMARY KEY,
                    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE RESTRICT,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    mission_id TEXT REFERENCES missions(id) ON DELETE RESTRICT,
                    mission_run_id TEXT REFERENCES mission_runs(id) ON DELETE RESTRICT,
                    operator_id TEXT,
                    status TEXT NOT NULL,
                    assigned_at_unix_ms INTEGER NOT NULL,
                    ended_at_unix_ms INTEGER
                );
                CREATE INDEX incident_assignments_incident_assigned
                    ON incident_assignments(incident_id, assigned_at_unix_ms DESC);
                CREATE UNIQUE INDEX incident_assignments_one_active_per_drone
                    ON incident_assignments(drone_id)
                    WHERE ended_at_unix_ms IS NULL;
                CREATE UNIQUE INDEX incident_assignments_mission
                    ON incident_assignments(mission_id)
                    WHERE mission_id IS NOT NULL;
                CREATE UNIQUE INDEX incident_assignments_mission_run
                    ON incident_assignments(mission_run_id)
                    WHERE mission_run_id IS NOT NULL;

                PRAGMA user_version = 13;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 13: {error}"))?;
    }
    if current_version < 14 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                ALTER TABLE incident_assignments ADD COLUMN on_scene_at_unix_ms INTEGER;

                CREATE TABLE mission_action_executions (
                    id TEXT PRIMARY KEY,
                    mission_run_id TEXT NOT NULL REFERENCES mission_runs(id) ON DELETE CASCADE,
                    mission_plan_id TEXT NOT NULL REFERENCES mission_plans(id) ON DELETE RESTRICT,
                    action_sequence INTEGER NOT NULL,
                    action_type TEXT NOT NULL,
                    state TEXT NOT NULL
                        CHECK (state IN ('REQUESTED', 'RUNNING', 'RETRYING',
                                         'SUCCEEDED', 'FAILED', 'POLICY_APPLIED')),
                    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
                    max_attempts INTEGER NOT NULL CHECK (max_attempts >= 1),
                    failure_policy TEXT NOT NULL
                        CHECK (failure_policy IN ('RETURN_TO_LAUNCH', 'OPERATOR_INTERVENTION')),
                    requested_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    started_at_unix_ms INTEGER,
                    completed_at_unix_ms INTEGER,
                    error_code TEXT NOT NULL DEFAULT '',
                    error_message TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT,
                    UNIQUE(mission_run_id, action_sequence),
                    FOREIGN KEY (mission_plan_id, action_sequence)
                        REFERENCES mission_actions(mission_plan_id, sequence) ON DELETE RESTRICT
                );
                CREATE INDEX mission_action_executions_run_sequence
                    ON mission_action_executions(mission_run_id, action_sequence);

                CREATE TABLE mission_action_execution_events (
                    id TEXT PRIMARY KEY,
                    mission_action_execution_id TEXT NOT NULL
                        REFERENCES mission_action_executions(id) ON DELETE CASCADE,
                    sequence INTEGER NOT NULL,
                    state TEXT NOT NULL
                        CHECK (state IN ('REQUESTED', 'RUNNING', 'RETRYING',
                                         'SUCCEEDED', 'FAILED', 'POLICY_APPLIED')),
                    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
                    source TEXT NOT NULL,
                    occurred_at_unix_ms INTEGER NOT NULL,
                    error_code TEXT NOT NULL DEFAULT '',
                    message TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT,
                    UNIQUE(mission_action_execution_id, sequence)
                );
                CREATE INDEX mission_action_execution_events_action_sequence
                    ON mission_action_execution_events(mission_action_execution_id, sequence);

                PRAGMA user_version = 14;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 14: {error}"))?;
    }
    if current_version < 15 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                CREATE TABLE mission_action_executions_v15 (
                    id TEXT PRIMARY KEY,
                    mission_run_id TEXT NOT NULL REFERENCES mission_runs(id) ON DELETE CASCADE,
                    mission_plan_id TEXT NOT NULL REFERENCES mission_plans(id) ON DELETE RESTRICT,
                    action_sequence INTEGER NOT NULL,
                    action_type TEXT NOT NULL,
                    state TEXT NOT NULL
                        CHECK (state IN ('REQUESTED', 'RUNNING', 'RETRYING',
                                         'SUCCEEDED', 'FAILED', 'POLICY_APPLIED')),
                    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
                    max_attempts INTEGER NOT NULL CHECK (max_attempts >= 1),
                    failure_policy TEXT NOT NULL
                        CHECK (failure_policy IN ('RETURN_TO_LAUNCH',
                                                  'OPERATOR_INTERVENTION',
                                                  'SKIP_OPTIONAL_AND_NOTIFY')),
                    timeout_ms INTEGER NOT NULL CHECK (timeout_ms >= 1000),
                    retry_initial_delay_ms INTEGER NOT NULL CHECK (retry_initial_delay_ms >= 0),
                    retry_backoff_multiplier REAL NOT NULL CHECK (retry_backoff_multiplier >= 1),
                    attempt_deadline_at_unix_ms INTEGER,
                    next_attempt_at_unix_ms INTEGER,
                    requested_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    started_at_unix_ms INTEGER,
                    completed_at_unix_ms INTEGER,
                    error_code TEXT NOT NULL DEFAULT '',
                    error_message TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT,
                    UNIQUE(mission_run_id, action_sequence),
                    FOREIGN KEY (mission_plan_id, action_sequence)
                        REFERENCES mission_actions(mission_plan_id, sequence) ON DELETE RESTRICT
                );
                INSERT INTO mission_action_executions_v15 (
                    id, mission_run_id, mission_plan_id, action_sequence, action_type,
                    state, attempt, max_attempts, failure_policy, timeout_ms,
                    retry_initial_delay_ms, retry_backoff_multiplier,
                    requested_at_unix_ms, updated_at_unix_ms, started_at_unix_ms,
                    completed_at_unix_ms, error_code, error_message, evidence_json
                )
                SELECT id, mission_run_id, mission_plan_id, action_sequence, action_type,
                       state, attempt, max_attempts, failure_policy, 20000, 500, 1.0,
                       requested_at_unix_ms, updated_at_unix_ms, started_at_unix_ms,
                       completed_at_unix_ms, error_code, error_message, evidence_json
                FROM mission_action_executions;

                CREATE TABLE mission_action_execution_events_v15 (
                    id TEXT PRIMARY KEY,
                    mission_action_execution_id TEXT NOT NULL
                        REFERENCES mission_action_executions_v15(id) ON DELETE CASCADE,
                    sequence INTEGER NOT NULL,
                    state TEXT NOT NULL
                        CHECK (state IN ('REQUESTED', 'RUNNING', 'RETRYING',
                                         'SUCCEEDED', 'FAILED', 'POLICY_APPLIED')),
                    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
                    source TEXT NOT NULL,
                    occurred_at_unix_ms INTEGER NOT NULL,
                    error_code TEXT NOT NULL DEFAULT '',
                    message TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT,
                    UNIQUE(mission_action_execution_id, sequence)
                );
                INSERT INTO mission_action_execution_events_v15
                    SELECT * FROM mission_action_execution_events;

                DROP TABLE mission_action_execution_events;
                DROP TABLE mission_action_executions;
                ALTER TABLE mission_action_executions_v15
                    RENAME TO mission_action_executions;
                ALTER TABLE mission_action_execution_events_v15
                    RENAME TO mission_action_execution_events;
                CREATE INDEX mission_action_executions_run_sequence
                    ON mission_action_executions(mission_run_id, action_sequence);
                CREATE INDEX mission_action_execution_events_action_sequence
                    ON mission_action_execution_events(mission_action_execution_id, sequence);

                PRAGMA user_version = 15;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 15: {error}"))?;
    }
    if current_version < 16 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                CREATE TABLE operational_alerts (
                    id TEXT PRIMARY KEY,
                    dedupe_key TEXT NOT NULL,
                    alert_type TEXT NOT NULL,
                    severity TEXT NOT NULL
                        CHECK (severity IN ('INFO', 'WARNING', 'CRITICAL')),
                    source TEXT NOT NULL,
                    state TEXT NOT NULL
                        CHECK (state IN ('ACTIVE', 'ACKNOWLEDGED', 'RESOLVED', 'EXPIRED')),
                    drone_id TEXT REFERENCES drones(id) ON DELETE RESTRICT,
                    incident_id TEXT REFERENCES incidents(id) ON DELETE RESTRICT,
                    mission_run_id TEXT REFERENCES mission_runs(id) ON DELETE RESTRICT,
                    title TEXT NOT NULL,
                    recommended_action TEXT NOT NULL,
                    evidence_json TEXT NOT NULL DEFAULT '{}',
                    first_seen_at_unix_ms INTEGER NOT NULL,
                    last_seen_at_unix_ms INTEGER NOT NULL,
                    observation_count INTEGER NOT NULL DEFAULT 1
                        CHECK (observation_count >= 1),
                    acknowledged_at_unix_ms INTEGER,
                    acknowledged_by TEXT,
                    resolved_at_unix_ms INTEGER,
                    resolution_reason TEXT,
                    expired_at_unix_ms INTEGER,
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL
                );
                CREATE UNIQUE INDEX operational_alerts_one_unresolved_dedupe_key
                    ON operational_alerts(dedupe_key)
                    WHERE state IN ('ACTIVE', 'ACKNOWLEDGED');
                CREATE INDEX operational_alerts_state_severity_last_seen
                    ON operational_alerts(state, severity, last_seen_at_unix_ms DESC);
                CREATE INDEX operational_alerts_type_state
                    ON operational_alerts(alert_type, state);
                CREATE INDEX operational_alerts_drone_last_seen
                    ON operational_alerts(drone_id, last_seen_at_unix_ms DESC);
                CREATE INDEX operational_alerts_incident_last_seen
                    ON operational_alerts(incident_id, last_seen_at_unix_ms DESC);
                CREATE INDEX operational_alerts_run_last_seen
                    ON operational_alerts(mission_run_id, last_seen_at_unix_ms DESC);

                CREATE TABLE operational_alert_events (
                    id TEXT PRIMARY KEY,
                    operational_alert_id TEXT NOT NULL
                        REFERENCES operational_alerts(id) ON DELETE CASCADE,
                    sequence INTEGER NOT NULL,
                    event_type TEXT NOT NULL
                        CHECK (event_type IN ('ACTIVATED', 'SEVERITY_CHANGED',
                                              'ACKNOWLEDGED', 'RESOLVED', 'EXPIRED')),
                    state TEXT NOT NULL
                        CHECK (state IN ('ACTIVE', 'ACKNOWLEDGED', 'RESOLVED', 'EXPIRED')),
                    actor TEXT NOT NULL,
                    message TEXT NOT NULL,
                    evidence_json TEXT NOT NULL DEFAULT '{}',
                    occurred_at_unix_ms INTEGER NOT NULL,
                    UNIQUE(operational_alert_id, sequence)
                );
                CREATE INDEX operational_alert_events_alert_sequence
                    ON operational_alert_events(operational_alert_id, sequence);

                PRAGMA user_version = 16;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 16: {error}"))?;
    }
    if current_version < 17 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                CREATE TABLE evidence_recording_sessions (
                    id TEXT PRIMARY KEY,
                    source_id TEXT NOT NULL,
                    status TEXT NOT NULL
                        CHECK (status IN ('REQUESTED', 'RUNNING', 'SUCCEEDED', 'FAILED')),
                    evidence_root TEXT NOT NULL,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    incident_id TEXT REFERENCES incidents(id) ON DELETE RESTRICT,
                    mission_id TEXT REFERENCES missions(id) ON DELETE RESTRICT,
                    mission_run_id TEXT REFERENCES mission_runs(id) ON DELETE RESTRICT,
                    requested_by TEXT NOT NULL,
                    segment_duration_seconds INTEGER NOT NULL
                        CHECK (segment_duration_seconds BETWEEN 2 AND 600),
                    warning_free_bytes INTEGER NOT NULL CHECK (warning_free_bytes >= 0),
                    stop_free_bytes INTEGER NOT NULL CHECK (stop_free_bytes >= 0),
                    requested_at_unix_ms INTEGER NOT NULL,
                    started_at_unix_ms INTEGER,
                    stopped_at_unix_ms INTEGER,
                    finalized_segment_count INTEGER NOT NULL DEFAULT 0
                        CHECK (finalized_segment_count >= 0),
                    total_bytes INTEGER NOT NULL DEFAULT 0 CHECK (total_bytes >= 0),
                    last_segment_ended_at_unix_ms INTEGER,
                    stop_reason TEXT NOT NULL DEFAULT '',
                    error_code TEXT NOT NULL DEFAULT '',
                    error_message TEXT NOT NULL DEFAULT '',
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL
                );
                CREATE UNIQUE INDEX evidence_recording_one_active_source
                    ON evidence_recording_sessions(source_id)
                    WHERE status IN ('REQUESTED', 'RUNNING');
                CREATE INDEX evidence_recording_sessions_drone_requested
                    ON evidence_recording_sessions(drone_id, requested_at_unix_ms DESC);
                CREATE INDEX evidence_recording_sessions_incident_requested
                    ON evidence_recording_sessions(incident_id, requested_at_unix_ms DESC);
                CREATE INDEX evidence_recording_sessions_run_requested
                    ON evidence_recording_sessions(mission_run_id, requested_at_unix_ms DESC);

                CREATE TABLE evidence_recording_segments (
                    id TEXT PRIMARY KEY,
                    recording_session_id TEXT NOT NULL
                        REFERENCES evidence_recording_sessions(id) ON DELETE RESTRICT,
                    sequence INTEGER NOT NULL CHECK (sequence >= 0),
                    state TEXT NOT NULL CHECK (state IN ('FINALIZING', 'LOCAL_VERIFIED')),
                    temporary_relative_path TEXT NOT NULL,
                    final_relative_path TEXT NOT NULL,
                    source_started_at_unix_ms INTEGER NOT NULL,
                    source_ended_at_unix_ms INTEGER NOT NULL,
                    byte_length INTEGER NOT NULL CHECK (byte_length >= 0),
                    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
                    finalized_at_unix_ms INTEGER,
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    UNIQUE(recording_session_id, sequence),
                    CHECK (source_ended_at_unix_ms >= source_started_at_unix_ms)
                );
                CREATE INDEX evidence_recording_segments_session_sequence
                    ON evidence_recording_segments(recording_session_id, sequence);

                CREATE TABLE evidence_gap_events (
                    id TEXT PRIMARY KEY,
                    recording_session_id TEXT NOT NULL
                        REFERENCES evidence_recording_sessions(id) ON DELETE RESTRICT,
                    sequence INTEGER NOT NULL CHECK (sequence >= 1),
                    cause TEXT NOT NULL,
                    gap_started_at_unix_ms INTEGER NOT NULL,
                    gap_ended_at_unix_ms INTEGER,
                    details_json TEXT NOT NULL DEFAULT '{}',
                    occurred_at_unix_ms INTEGER NOT NULL,
                    UNIQUE(recording_session_id, sequence),
                    CHECK (gap_ended_at_unix_ms IS NULL OR gap_ended_at_unix_ms >= gap_started_at_unix_ms)
                );
                CREATE INDEX evidence_gap_events_session_sequence
                    ON evidence_gap_events(recording_session_id, sequence);

                CREATE TABLE evidence_recording_events (
                    id TEXT PRIMARY KEY,
                    recording_session_id TEXT NOT NULL
                        REFERENCES evidence_recording_sessions(id) ON DELETE RESTRICT,
                    sequence INTEGER NOT NULL CHECK (sequence >= 1),
                    event_type TEXT NOT NULL CHECK (event_type IN (
                        'REQUESTED', 'RUNNING', 'STOP_REQUESTED', 'SEGMENT_FINALIZED',
                        'EVIDENCE_GAP', 'SUCCEEDED', 'FAILED'
                    )),
                    state TEXT NOT NULL
                        CHECK (state IN ('REQUESTED', 'RUNNING', 'SUCCEEDED', 'FAILED')),
                    actor TEXT NOT NULL,
                    message TEXT NOT NULL,
                    evidence_json TEXT NOT NULL DEFAULT '{}',
                    occurred_at_unix_ms INTEGER NOT NULL,
                    UNIQUE(recording_session_id, sequence)
                );
                CREATE INDEX evidence_recording_events_session_sequence
                    ON evidence_recording_events(recording_session_id, sequence);

                PRAGMA user_version = 17;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 17: {error}"))?;
    }
    if current_version < 18 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                CREATE TABLE perception_track_sessions (
                    id TEXT PRIMARY KEY,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    communication_link_id TEXT NOT NULL
                        REFERENCES communication_links(id) ON DELETE RESTRICT,
                    perception_stream_id TEXT NOT NULL,
                    source_id TEXT NOT NULL,
                    stream_epoch TEXT NOT NULL,
                    tracker_type TEXT NOT NULL
                        CHECK (tracker_type IN ('BYTE_TRACK', 'BYTE_TRACK_CMC')),
                    started_at_unix_ms INTEGER NOT NULL,
                    last_update_at_unix_ms INTEGER NOT NULL,
                    ended_at_unix_ms INTEGER,
                    end_reason TEXT NOT NULL DEFAULT '',
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    CHECK (last_update_at_unix_ms >= started_at_unix_ms),
                    CHECK (
                        (ended_at_unix_ms IS NULL AND end_reason = '') OR
                        (ended_at_unix_ms IS NOT NULL AND end_reason <> '' AND ended_at_unix_ms >= started_at_unix_ms)
                    )
                );
                CREATE INDEX perception_track_sessions_drone_started
                    ON perception_track_sessions(drone_id, started_at_unix_ms DESC);
                CREATE INDEX perception_track_sessions_source_started
                    ON perception_track_sessions(source_id, started_at_unix_ms DESC);

                CREATE TABLE perception_tracks (
                    id TEXT PRIMARY KEY,
                    track_session_id TEXT NOT NULL
                        REFERENCES perception_track_sessions(id) ON DELETE RESTRICT,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    class_id INTEGER NOT NULL,
                    class_label TEXT NOT NULL,
                    tracker_type TEXT NOT NULL
                        CHECK (tracker_type IN ('BYTE_TRACK', 'BYTE_TRACK_CMC')),
                    lifecycle_state TEXT NOT NULL CHECK (lifecycle_state IN (
                        'TENTATIVE', 'ACTIVE', 'TEMPORARILY_OCCLUDED', 'LOST', 'CLOSED'
                    )),
                    revision INTEGER NOT NULL CHECK (revision >= 1),
                    age_frames INTEGER NOT NULL CHECK (age_frames >= 1),
                    observation_count INTEGER NOT NULL CHECK (observation_count >= 1),
                    first_observed_at_unix_ms INTEGER NOT NULL,
                    last_observed_at_unix_ms INTEGER NOT NULL,
                    latest_box_x REAL NOT NULL CHECK (latest_box_x BETWEEN 0 AND 1),
                    latest_box_y REAL NOT NULL CHECK (latest_box_y BETWEEN 0 AND 1),
                    latest_box_width REAL NOT NULL CHECK (latest_box_width BETWEEN 0 AND 1),
                    latest_box_height REAL NOT NULL CHECK (latest_box_height BETWEEN 0 AND 1),
                    latest_detection_confidence REAL NOT NULL
                        CHECK (latest_detection_confidence BETWEEN 0 AND 1),
                    predicted_box_x REAL,
                    predicted_box_y REAL,
                    predicted_box_width REAL,
                    predicted_box_height REAL,
                    prediction_confidence REAL NOT NULL
                        CHECK (prediction_confidence BETWEEN 0 AND 1),
                    closed_at_unix_ms INTEGER,
                    closure_reason TEXT NOT NULL DEFAULT '',
                    last_update_reason TEXT NOT NULL CHECK (last_update_reason IN (
                        'CREATED', 'STATE_CHANGED', 'REACQUIRED', 'PERIODIC', 'CLOSED'
                    )),
                    received_at_unix_ms INTEGER NOT NULL,
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    CHECK (age_frames >= observation_count),
                    CHECK (last_observed_at_unix_ms >= first_observed_at_unix_ms),
                    CHECK (latest_box_x + latest_box_width <= 1.000000001),
                    CHECK (latest_box_y + latest_box_height <= 1.000000001),
                    CHECK (
                        (predicted_box_x IS NULL AND predicted_box_y IS NULL AND
                         predicted_box_width IS NULL AND predicted_box_height IS NULL AND
                         prediction_confidence = 0) OR
                        (predicted_box_x BETWEEN 0 AND 1 AND predicted_box_y BETWEEN 0 AND 1 AND
                         predicted_box_width BETWEEN 0 AND 1 AND predicted_box_height BETWEEN 0 AND 1 AND
                         predicted_box_x + predicted_box_width <= 1.000000001 AND
                         predicted_box_y + predicted_box_height <= 1.000000001)
                    ),
                    CHECK (
                        (lifecycle_state = 'CLOSED' AND closed_at_unix_ms IS NOT NULL AND
                         closed_at_unix_ms >= last_observed_at_unix_ms AND closure_reason <> '') OR
                        (lifecycle_state <> 'CLOSED' AND closed_at_unix_ms IS NULL AND closure_reason = '')
                    )
                );
                CREATE INDEX perception_tracks_session_state
                    ON perception_tracks(track_session_id, lifecycle_state, last_observed_at_unix_ms DESC);
                CREATE INDEX perception_tracks_drone_state
                    ON perception_tracks(drone_id, lifecycle_state, last_observed_at_unix_ms DESC);

                CREATE TABLE perception_track_events (
                    id TEXT PRIMARY KEY,
                    track_id TEXT NOT NULL REFERENCES perception_tracks(id) ON DELETE RESTRICT,
                    revision INTEGER NOT NULL CHECK (revision >= 1),
                    event_type TEXT NOT NULL CHECK (event_type IN (
                        'CREATED', 'STATE_CHANGED', 'REACQUIRED', 'CLOSED'
                    )),
                    previous_state TEXT,
                    lifecycle_state TEXT NOT NULL CHECK (lifecycle_state IN (
                        'TENTATIVE', 'ACTIVE', 'TEMPORARILY_OCCLUDED', 'LOST', 'CLOSED'
                    )),
                    reason TEXT NOT NULL,
                    occurred_at_unix_ms INTEGER NOT NULL,
                    received_at_unix_ms INTEGER NOT NULL,
                    UNIQUE(track_id, revision)
                );
                CREATE INDEX perception_track_events_track_revision
                    ON perception_track_events(track_id, revision);

                CREATE TABLE perception_track_samples (
                    id TEXT PRIMARY KEY,
                    track_id TEXT NOT NULL REFERENCES perception_tracks(id) ON DELETE RESTRICT,
                    revision INTEGER NOT NULL CHECK (revision >= 1),
                    sample_reason TEXT NOT NULL CHECK (sample_reason IN (
                        'CREATED', 'STATE_CHANGED', 'REACQUIRED', 'PERIODIC', 'CLOSED'
                    )),
                    lifecycle_state TEXT NOT NULL,
                    observed_at_unix_ms INTEGER NOT NULL,
                    box_x REAL NOT NULL,
                    box_y REAL NOT NULL,
                    box_width REAL NOT NULL,
                    box_height REAL NOT NULL,
                    detection_confidence REAL NOT NULL,
                    predicted_box_x REAL,
                    predicted_box_y REAL,
                    predicted_box_width REAL,
                    predicted_box_height REAL,
                    prediction_confidence REAL NOT NULL,
                    received_at_unix_ms INTEGER NOT NULL,
                    UNIQUE(track_id, revision)
                );
                CREATE INDEX perception_track_samples_track_revision
                    ON perception_track_samples(track_id, revision DESC);

                PRAGMA user_version = 18;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 18: {error}"))?;
    }
    if current_version < 19 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                ALTER TABLE perception_track_sessions
                    ADD COLUMN mission_id TEXT REFERENCES missions(id) ON DELETE RESTRICT;
                ALTER TABLE perception_track_sessions
                    ADD COLUMN mission_run_id TEXT REFERENCES mission_runs(id) ON DELETE RESTRICT;
                ALTER TABLE perception_track_sessions
                    ADD COLUMN current_visible_count INTEGER NOT NULL DEFAULT 0
                        CHECK (current_visible_count >= 0);
                ALTER TABLE perception_track_sessions
                    ADD COLUMN unique_confirmed_count INTEGER NOT NULL DEFAULT 0
                        CHECK (unique_confirmed_count >= 0);
                CREATE INDEX perception_track_sessions_mission_run
                    ON perception_track_sessions(mission_run_id, started_at_unix_ms DESC);

                CREATE TABLE perception_mission_tracks (
                    mission_run_id TEXT NOT NULL
                        REFERENCES mission_runs(id) ON DELETE RESTRICT,
                    mission_id TEXT NOT NULL REFERENCES missions(id) ON DELETE RESTRICT,
                    track_session_id TEXT NOT NULL
                        REFERENCES perception_track_sessions(id) ON DELETE RESTRICT,
                    track_id TEXT NOT NULL REFERENCES perception_tracks(id) ON DELETE RESTRICT,
                    first_observed_at_unix_ms INTEGER NOT NULL,
                    last_observed_at_unix_ms INTEGER NOT NULL,
                    PRIMARY KEY (mission_run_id, track_id),
                    CHECK (last_observed_at_unix_ms >= first_observed_at_unix_ms)
                );
                CREATE INDEX perception_mission_tracks_mission
                    ON perception_mission_tracks(mission_id, mission_run_id);

                CREATE TABLE perception_counting_rules (
                    id TEXT PRIMARY KEY,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    source_id TEXT NOT NULL,
                    label TEXT NOT NULL,
                    rule_type TEXT NOT NULL CHECK (rule_type IN ('LINE', 'POLYGON')),
                    revision INTEGER NOT NULL CHECK (revision >= 1),
                    points_json TEXT NOT NULL,
                    class_ids_json TEXT NOT NULL DEFAULT '[]',
                    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
                    created_by TEXT NOT NULL,
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL
                );
                CREATE INDEX perception_counting_rules_drone_source
                    ON perception_counting_rules(drone_id, source_id, enabled, label);

                CREATE TABLE perception_count_events (
                    id TEXT PRIMARY KEY,
                    counting_rule_id TEXT NOT NULL
                        REFERENCES perception_counting_rules(id) ON DELETE RESTRICT,
                    rule_revision INTEGER NOT NULL CHECK (rule_revision >= 1),
                    track_session_id TEXT NOT NULL
                        REFERENCES perception_track_sessions(id) ON DELETE RESTRICT,
                    track_id TEXT NOT NULL REFERENCES perception_tracks(id) ON DELETE RESTRICT,
                    mission_id TEXT REFERENCES missions(id) ON DELETE RESTRICT,
                    mission_run_id TEXT REFERENCES mission_runs(id) ON DELETE RESTRICT,
                    event_type TEXT NOT NULL CHECK (event_type IN (
                        'LINE_FORWARD', 'LINE_REVERSE', 'POLYGON_ENTRY', 'POLYGON_EXIT'
                    )),
                    observed_at_unix_ms INTEGER NOT NULL,
                    anchor_x REAL NOT NULL CHECK (anchor_x BETWEEN 0 AND 1),
                    anchor_y REAL NOT NULL CHECK (anchor_y BETWEEN 0 AND 1),
                    received_at_unix_ms INTEGER NOT NULL
                );
                CREATE INDEX perception_count_events_session_rule
                    ON perception_count_events(track_session_id, counting_rule_id, rule_revision, observed_at_unix_ms);
                CREATE INDEX perception_count_events_mission_rule
                    ON perception_count_events(mission_run_id, counting_rule_id, observed_at_unix_ms);

                CREATE TABLE perception_track_rule_counts (
                    track_session_id TEXT NOT NULL
                        REFERENCES perception_track_sessions(id) ON DELETE RESTRICT,
                    counting_rule_id TEXT NOT NULL
                        REFERENCES perception_counting_rules(id) ON DELETE RESTRICT,
                    rule_revision INTEGER NOT NULL CHECK (rule_revision >= 1),
                    rule_type TEXT NOT NULL CHECK (rule_type IN ('LINE', 'POLYGON')),
                    line_forward INTEGER NOT NULL DEFAULT 0 CHECK (line_forward >= 0),
                    line_reverse INTEGER NOT NULL DEFAULT 0 CHECK (line_reverse >= 0),
                    polygon_entries INTEGER NOT NULL DEFAULT 0 CHECK (polygon_entries >= 0),
                    polygon_exits INTEGER NOT NULL DEFAULT 0 CHECK (polygon_exits >= 0),
                    updated_at_unix_ms INTEGER NOT NULL,
                    PRIMARY KEY (track_session_id, counting_rule_id, rule_revision)
                );
                CREATE INDEX perception_track_rule_counts_rule_session
                    ON perception_track_rule_counts(counting_rule_id, track_session_id);

                CREATE TABLE perception_track_selections (
                    id TEXT PRIMARY KEY,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    track_session_id TEXT NOT NULL
                        REFERENCES perception_track_sessions(id) ON DELETE RESTRICT,
                    track_id TEXT NOT NULL REFERENCES perception_tracks(id) ON DELETE RESTRICT,
                    status TEXT NOT NULL CHECK (status IN (
                        'SELECTED', 'OCCLUDED', 'LOST', 'CLOSED', 'CLEARED'
                    )),
                    selected_by TEXT NOT NULL,
                    selected_at_unix_ms INTEGER NOT NULL,
                    last_state_change_at_unix_ms INTEGER NOT NULL,
                    cleared_at_unix_ms INTEGER,
                    result_reason TEXT NOT NULL DEFAULT '',
                    snapshot_lifecycle_state TEXT NOT NULL CHECK (snapshot_lifecycle_state IN (
                        'TENTATIVE', 'ACTIVE', 'TEMPORARILY_OCCLUDED', 'LOST', 'CLOSED'
                    )),
                    snapshot_age_frames INTEGER NOT NULL CHECK (snapshot_age_frames >= 0),
                    snapshot_observation_count INTEGER NOT NULL
                        CHECK (snapshot_observation_count >= 0),
                    snapshot_last_observed_at_unix_ms INTEGER NOT NULL,
                    snapshot_detection_confidence REAL NOT NULL
                        CHECK (snapshot_detection_confidence BETWEEN 0 AND 1),
                    snapshot_prediction_confidence REAL NOT NULL
                        CHECK (snapshot_prediction_confidence BETWEEN 0 AND 1),
                    snapshot_class_label TEXT NOT NULL,
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    CHECK (
                        (status = 'CLEARED' AND cleared_at_unix_ms IS NOT NULL) OR
                        (status <> 'CLEARED' AND cleared_at_unix_ms IS NULL)
                    )
                );
                CREATE UNIQUE INDEX perception_track_one_active_selection
                    ON perception_track_selections(drone_id)
                    WHERE status IN ('SELECTED', 'OCCLUDED');
                CREATE INDEX perception_track_selections_drone_selected
                    ON perception_track_selections(drone_id, selected_at_unix_ms DESC);

                CREATE TABLE perception_track_selection_events (
                    id TEXT PRIMARY KEY,
                    selection_id TEXT NOT NULL
                        REFERENCES perception_track_selections(id) ON DELETE RESTRICT,
                    sequence INTEGER NOT NULL CHECK (sequence >= 1),
                    event_type TEXT NOT NULL CHECK (event_type IN (
                        'SELECTED', 'OCCLUDED', 'REACQUIRED', 'LOST', 'CLOSED',
                        'CLEARED', 'REPLACED', 'ANNOTATED', 'EVIDENCE_MARKED'
                    )),
                    lifecycle_state TEXT NOT NULL,
                    actor TEXT NOT NULL,
                    reason TEXT NOT NULL DEFAULT '',
                    details_json TEXT NOT NULL DEFAULT '{}',
                    occurred_at_unix_ms INTEGER NOT NULL,
                    UNIQUE(selection_id, sequence)
                );
                CREATE INDEX perception_track_selection_events_selection_sequence
                    ON perception_track_selection_events(selection_id, sequence);

                CREATE TABLE perception_track_annotations (
                    id TEXT PRIMARY KEY,
                    selection_id TEXT NOT NULL
                        REFERENCES perception_track_selections(id) ON DELETE RESTRICT,
                    track_session_id TEXT NOT NULL
                        REFERENCES perception_track_sessions(id) ON DELETE RESTRICT,
                    track_id TEXT NOT NULL REFERENCES perception_tracks(id) ON DELETE RESTRICT,
                    annotation_type TEXT NOT NULL CHECK (annotation_type IN ('NOTE', 'EVIDENCE_MARKER')),
                    body TEXT NOT NULL DEFAULT '',
                    evidence_recording_session_id TEXT
                        REFERENCES evidence_recording_sessions(id) ON DELETE RESTRICT,
                    marked_at_unix_ms INTEGER NOT NULL,
                    created_by TEXT NOT NULL,
                    created_at_unix_ms INTEGER NOT NULL,
                    CHECK (
                        annotation_type <> 'EVIDENCE_MARKER' OR
                        evidence_recording_session_id IS NOT NULL
                    )
                );
                CREATE INDEX perception_track_annotations_track_marked
                    ON perception_track_annotations(track_id, marked_at_unix_ms DESC);

                PRAGMA user_version = 19;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 19: {error}"))?;
    }
    if current_version < 20 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                DROP INDEX vehicle_command_events_command_sequence;
                DROP INDEX vehicle_commands_drone_created;
                DROP INDEX vehicle_commands_deliverable;
                ALTER TABLE vehicle_command_events RENAME TO vehicle_command_events_v19;
                ALTER TABLE vehicle_commands RENAME TO vehicle_commands_v19;

                CREATE TABLE vehicle_commands (
                    id TEXT PRIMARY KEY,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
                    command_type TEXT NOT NULL
                        CHECK (command_type IN ('hold', 'return_to_launch', 'land',
                                               'gimbal_set_angles', 'gimbal_set_rates',
                                               'gimbal_center', 'gimbal_set_roi',
                                               'camera_set_zoom', 'payload_control_begin',
                                               'payload_control_renew', 'payload_control_end',
                                               'gimbal_follow_start', 'gimbal_follow_stop')),
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
                INSERT INTO vehicle_commands SELECT * FROM vehicle_commands_v19;
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
                INSERT INTO vehicle_command_events SELECT * FROM vehicle_command_events_v19;
                CREATE INDEX vehicle_command_events_command_sequence
                    ON vehicle_command_events(vehicle_command_id, sequence);

                DROP TABLE vehicle_command_events_v19;
                DROP TABLE vehicle_commands_v19;
                PRAGMA user_version = 20;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 20: {error}"))?;
    }
    if current_version < 21 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                DROP INDEX vehicle_command_events_command_sequence;
                DROP INDEX vehicle_commands_drone_created;
                DROP INDEX vehicle_commands_deliverable;
                ALTER TABLE vehicle_command_events RENAME TO vehicle_command_events_v20;
                ALTER TABLE vehicle_commands RENAME TO vehicle_commands_v20;

                CREATE TABLE vehicle_commands (
                    id TEXT PRIMARY KEY,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
                    command_type TEXT NOT NULL
                        CHECK (command_type IN ('hold', 'return_to_launch', 'land',
                                               'gimbal_set_angles', 'gimbal_set_rates',
                                               'gimbal_center', 'gimbal_set_roi',
                                               'camera_set_zoom', 'payload_control_begin',
                                               'payload_control_renew', 'payload_control_end',
                                               'gimbal_follow_start', 'gimbal_follow_stop',
                                               'geolocate_selected_track')),
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
                INSERT INTO vehicle_commands SELECT * FROM vehicle_commands_v20;
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
                INSERT INTO vehicle_command_events SELECT * FROM vehicle_command_events_v20;
                CREATE INDEX vehicle_command_events_command_sequence
                    ON vehicle_command_events(vehicle_command_id, sequence);

                DROP TABLE vehicle_command_events_v20;
                DROP TABLE vehicle_commands_v20;

                CREATE TABLE perception_track_geolocations (
                    id TEXT PRIMARY KEY,
                    command_id TEXT NOT NULL UNIQUE
                        REFERENCES vehicle_commands(id) ON DELETE RESTRICT,
                    selection_id TEXT NOT NULL
                        REFERENCES perception_track_selections(id) ON DELETE RESTRICT,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    track_session_id TEXT NOT NULL
                        REFERENCES perception_track_sessions(id) ON DELETE RESTRICT,
                    track_id TEXT NOT NULL REFERENCES perception_tracks(id) ON DELETE RESTRICT,
                    source_id TEXT NOT NULL,
                    status TEXT NOT NULL CHECK (status IN ('REQUESTED', 'SUCCEEDED', 'REJECTED')),
                    requested_by TEXT NOT NULL,
                    requested_at_unix_ms INTEGER NOT NULL,
                    resolved_at_unix_ms INTEGER,
                    aim_point TEXT NOT NULL CHECK (aim_point IN ('GROUND_CONTACT', 'TARGET_CENTER')),
                    assumed_aim_point_height_m REAL NOT NULL,
                    assumed_aim_point_height_uncertainty_m REAL NOT NULL,
                    ground_altitude_amsl_m REAL NOT NULL,
                    ground_altitude_uncertainty_m REAL NOT NULL CHECK (ground_altitude_uncertainty_m >= 0),
                    ground_altitude_source TEXT NOT NULL,
                    ground_altitude_source_version TEXT NOT NULL,
                    ground_altitude_resolved_at_unix_ms INTEGER NOT NULL,
                    latitude REAL,
                    longitude REAL,
                    altitude_amsl_m REAL,
                    horizontal_uncertainty_m REAL,
                    method TEXT NOT NULL DEFAULT '',
                    frame_observed_at_unix_ms INTEGER,
                    rejection_code TEXT NOT NULL DEFAULT '',
                    rejection_reason TEXT NOT NULL DEFAULT '',
                    evidence_json TEXT,
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    CHECK (
                        (status = 'REQUESTED' AND resolved_at_unix_ms IS NULL AND
                         latitude IS NULL AND longitude IS NULL AND rejection_code = '' AND rejection_reason = '') OR
                        (status = 'SUCCEEDED' AND resolved_at_unix_ms IS NOT NULL AND
                         latitude BETWEEN -90 AND 90 AND longitude BETWEEN -180 AND 180 AND
                         altitude_amsl_m IS NOT NULL AND horizontal_uncertainty_m >= 0 AND
                         method <> '' AND rejection_code = '' AND rejection_reason = '') OR
                        (status = 'REJECTED' AND resolved_at_unix_ms IS NOT NULL AND
                         latitude IS NULL AND longitude IS NULL AND rejection_code <> '' AND rejection_reason <> '')
                    )
                );
                CREATE INDEX perception_track_geolocations_track_requested
                    ON perception_track_geolocations(track_session_id, track_id, requested_at_unix_ms DESC);
                CREATE INDEX perception_track_geolocations_drone_requested
                    ON perception_track_geolocations(drone_id, requested_at_unix_ms DESC);

                PRAGMA user_version = 21;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 21: {error}"))?;
    }
    if current_version < 22 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                ALTER TABLE perception_track_geolocations ADD COLUMN initial_latitude REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN initial_longitude REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN initial_altitude_amsl_m REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN initial_horizontal_uncertainty_m REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN initial_method TEXT NOT NULL DEFAULT '';
                ALTER TABLE perception_track_geolocations ADD COLUMN refinement_status TEXT NOT NULL DEFAULT 'NOT_REQUESTED';
                ALTER TABLE perception_track_geolocations ADD COLUMN terrain_source TEXT NOT NULL DEFAULT '';
                ALTER TABLE perception_track_geolocations ADD COLUMN terrain_source_version TEXT NOT NULL DEFAULT '';
                ALTER TABLE perception_track_geolocations ADD COLUMN terrain_vertical_uncertainty_m REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN terrain_iteration_count INTEGER NOT NULL DEFAULT 0;
                ALTER TABLE perception_track_geolocations ADD COLUMN terrain_residual_m REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN terrain_samples_json TEXT;
                ALTER TABLE perception_track_geolocations ADD COLUMN range_source TEXT NOT NULL DEFAULT 'BORESIGHT_INTERSECTION';
                ALTER TABLE perception_track_geolocations ADD COLUMN filtered_latitude REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN filtered_longitude REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN target_velocity_north_mps REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN target_velocity_east_mps REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN target_speed_mps REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN target_direction_deg REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN target_velocity_uncertainty_mps REAL;
                ALTER TABLE perception_track_geolocations ADD COLUMN motion_status TEXT NOT NULL DEFAULT 'INSUFFICIENT_HISTORY';

                UPDATE perception_track_geolocations
                SET initial_latitude = latitude,
                    initial_longitude = longitude,
                    initial_altitude_amsl_m = altitude_amsl_m,
                    initial_horizontal_uncertainty_m = horizontal_uncertainty_m,
                    initial_method = method
                WHERE status = 'SUCCEEDED';

                CREATE INDEX perception_track_geolocations_operational_latest
                    ON perception_track_geolocations(status, track_id, frame_observed_at_unix_ms DESC);

                PRAGMA user_version = 22;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 22: {error}"))?;
    }
    if current_version < 23 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                CREATE TABLE evidence_retention_policy (
                    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
                    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
                    default_retention_days INTEGER NOT NULL DEFAULT 30
                        CHECK (default_retention_days BETWEEN 1 AND 3650),
                    extended_retention_days INTEGER NOT NULL DEFAULT 365
                        CHECK (extended_retention_days BETWEEN 1 AND 3650),
                    trash_grace_days INTEGER NOT NULL DEFAULT 7
                        CHECK (trash_grace_days BETWEEN 1 AND 365),
                    updated_by TEXT NOT NULL DEFAULT 'atlas_default',
                    updated_at_unix_ms INTEGER NOT NULL
                );
                INSERT INTO evidence_retention_policy (
                    singleton_id, enabled, default_retention_days,
                    extended_retention_days, trash_grace_days,
                    updated_by, updated_at_unix_ms
                ) VALUES (1, 1, 30, 365, 7, 'atlas_default', 0);

                CREATE TABLE evidence_assets (
                    id TEXT PRIMARY KEY,
                    asset_type TEXT NOT NULL CHECK (asset_type IN ('STILL', 'EVENT_CLIP')),
                    status TEXT NOT NULL CHECK (status IN ('PENDING', 'READY', 'FAILED', 'TRASHED', 'PURGING', 'PURGED')),
                    review_state TEXT NOT NULL DEFAULT 'UNREVIEWED'
                        CHECK (review_state IN ('UNREVIEWED', 'RELEVANT', 'NOT_RELEVANT', 'NEEDS_FOLLOW_UP')),
                    retention_class TEXT NOT NULL DEFAULT 'STANDARD'
                        CHECK (retention_class IN ('STANDARD', 'EXTENDED', 'LEGAL_HOLD')),
                    source_id TEXT NOT NULL,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    incident_id TEXT REFERENCES incidents(id) ON DELETE RESTRICT,
                    mission_id TEXT REFERENCES missions(id) ON DELETE RESTRICT,
                    mission_run_id TEXT REFERENCES mission_runs(id) ON DELETE RESTRICT,
                    recording_session_id TEXT
                        REFERENCES evidence_recording_sessions(id) ON DELETE RESTRICT,
                    selection_id TEXT
                        REFERENCES perception_track_selections(id) ON DELETE RESTRICT,
                    track_session_id TEXT
                        REFERENCES perception_track_sessions(id) ON DELETE RESTRICT,
                    track_id TEXT REFERENCES perception_tracks(id) ON DELETE RESTRICT,
                    evidence_marker_annotation_id TEXT
                        REFERENCES perception_track_annotations(id) ON DELETE RESTRICT,
                    captured_at_unix_ms INTEGER NOT NULL,
                    source_started_at_unix_ms INTEGER,
                    source_ended_at_unix_ms INTEGER,
                    requested_start_at_unix_ms INTEGER,
                    requested_end_at_unix_ms INTEGER,
                    relative_path TEXT NOT NULL DEFAULT '',
                    thumbnail_relative_path TEXT NOT NULL DEFAULT '',
                    mime_type TEXT NOT NULL DEFAULT '',
                    thumbnail_mime_type TEXT NOT NULL DEFAULT '',
                    byte_length INTEGER NOT NULL DEFAULT 0 CHECK (byte_length >= 0),
                    sha256 TEXT NOT NULL DEFAULT '',
                    thumbnail_byte_length INTEGER NOT NULL DEFAULT 0
                        CHECK (thumbnail_byte_length >= 0),
                    thumbnail_sha256 TEXT NOT NULL DEFAULT '',
                    created_by TEXT NOT NULL,
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    retain_until_unix_ms INTEGER,
                    trashed_at_unix_ms INTEGER,
                    purge_after_unix_ms INTEGER,
                    delete_reason TEXT NOT NULL DEFAULT '',
                    purged_at_unix_ms INTEGER,
                    error_message TEXT NOT NULL DEFAULT '',
                    CHECK (source_ended_at_unix_ms IS NULL OR source_started_at_unix_ms IS NULL OR
                           source_ended_at_unix_ms >= source_started_at_unix_ms),
                    CHECK (requested_end_at_unix_ms IS NULL OR requested_start_at_unix_ms IS NULL OR
                           requested_end_at_unix_ms >= requested_start_at_unix_ms),
                    CHECK (retention_class = 'LEGAL_HOLD' OR retain_until_unix_ms IS NOT NULL),
                    CHECK (retention_class <> 'LEGAL_HOLD' OR retain_until_unix_ms IS NULL),
                    CHECK (status <> 'READY' OR
                           (relative_path <> '' AND thumbnail_relative_path <> '' AND
                            byte_length > 0 AND length(sha256) = 64 AND
                            thumbnail_byte_length > 0 AND length(thumbnail_sha256) = 64)),
                    CHECK (asset_type <> 'EVENT_CLIP' OR
                           (recording_session_id IS NOT NULL AND evidence_marker_annotation_id IS NOT NULL AND
                            requested_start_at_unix_ms IS NOT NULL AND requested_end_at_unix_ms IS NOT NULL))
                );
                CREATE INDEX evidence_assets_browse
                    ON evidence_assets(status, captured_at_unix_ms DESC);
                CREATE INDEX evidence_assets_drone_captured
                    ON evidence_assets(drone_id, captured_at_unix_ms DESC);
                CREATE INDEX evidence_assets_track_captured
                    ON evidence_assets(track_session_id, track_id, captured_at_unix_ms DESC);
                CREATE INDEX evidence_assets_recording_pending
                    ON evidence_assets(recording_session_id, status, captured_at_unix_ms);
                CREATE INDEX evidence_assets_retention_due
                    ON evidence_assets(status, retention_class, retain_until_unix_ms, purge_after_unix_ms);

                CREATE TABLE evidence_asset_annotations (
                    id TEXT PRIMARY KEY,
                    evidence_asset_id TEXT NOT NULL
                        REFERENCES evidence_assets(id) ON DELETE RESTRICT,
                    annotation_type TEXT NOT NULL CHECK (annotation_type IN ('NOTE', 'TAG')),
                    body TEXT NOT NULL,
                    created_by TEXT NOT NULL,
                    created_at_unix_ms INTEGER NOT NULL
                );
                CREATE INDEX evidence_asset_annotations_asset_created
                    ON evidence_asset_annotations(evidence_asset_id, created_at_unix_ms);

                CREATE TABLE evidence_asset_events (
                    id TEXT PRIMARY KEY,
                    evidence_asset_id TEXT NOT NULL
                        REFERENCES evidence_assets(id) ON DELETE RESTRICT,
                    sequence INTEGER NOT NULL CHECK (sequence >= 1),
                    event_type TEXT NOT NULL CHECK (event_type IN (
                        'CREATED', 'READY', 'FAILED', 'REVIEWED', 'ANNOTATED',
                        'RETENTION_CHANGED', 'TRASHED', 'RESTORED', 'PURGED'
                    )),
                    actor TEXT NOT NULL,
                    message TEXT NOT NULL,
                    details_json TEXT NOT NULL DEFAULT '{}',
                    occurred_at_unix_ms INTEGER NOT NULL,
                    UNIQUE(evidence_asset_id, sequence)
                );
                CREATE INDEX evidence_asset_events_asset_sequence
                    ON evidence_asset_events(evidence_asset_id, sequence);

                PRAGMA user_version = 23;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 23: {error}"))?;
    }
    if current_version < 24 {
        connection
            .execute_batch(
                r#"
                BEGIN IMMEDIATE;

                CREATE TABLE aircraft_follow_sessions (
                    id TEXT PRIMARY KEY,
                    drone_id TEXT NOT NULL REFERENCES drones(id) ON DELETE RESTRICT,
                    selection_id TEXT NOT NULL
                        REFERENCES perception_track_selections(id) ON DELETE RESTRICT,
                    track_session_id TEXT NOT NULL
                        REFERENCES perception_track_sessions(id) ON DELETE RESTRICT,
                    track_id TEXT NOT NULL REFERENCES perception_tracks(id) ON DELETE RESTRICT,
                    source_id TEXT NOT NULL,
                    state TEXT NOT NULL CHECK (state IN (
                        'REQUESTED', 'VALIDATING', 'ACQUIRING', 'FOLLOWING',
                        'DEGRADED_HOLD', 'ENDED'
                    )),
                    requested_by TEXT NOT NULL,
                    reviewed_by TEXT NOT NULL,
                    operator_review_note TEXT NOT NULL,
                    requested_at_unix_ms INTEGER NOT NULL,
                    authorized_at_unix_ms INTEGER,
                    started_at_unix_ms INTEGER,
                    ended_at_unix_ms INTEGER,
                    standoff_m REAL NOT NULL CHECK (standoff_m BETWEEN 10 AND 500),
                    altitude_relative_m REAL NOT NULL CHECK (altitude_relative_m BETWEEN 5 AND 120),
                    minimum_altitude_relative_m REAL NOT NULL CHECK (minimum_altitude_relative_m BETWEEN 5 AND 120),
                    maximum_altitude_relative_m REAL NOT NULL CHECK (maximum_altitude_relative_m BETWEEN 5 AND 120),
                    maximum_ground_speed_mps REAL NOT NULL CHECK (maximum_ground_speed_mps BETWEEN 0.5 AND 15),
                    maximum_acceleration_mps2 REAL NOT NULL CHECK (maximum_acceleration_mps2 BETWEEN 0.1 AND 5),
                    maximum_duration_ms INTEGER NOT NULL CHECK (maximum_duration_ms BETWEEN 10000 AND 1800000),
                    boundary_center_latitude REAL NOT NULL CHECK (boundary_center_latitude BETWEEN -90 AND 90),
                    boundary_center_longitude REAL NOT NULL CHECK (boundary_center_longitude BETWEEN -180 AND 180),
                    boundary_radius_m REAL NOT NULL CHECK (boundary_radius_m BETWEEN 25 AND 5000),
                    minimum_battery_percent REAL NOT NULL CHECK (minimum_battery_percent BETWEEN 15 AND 100),
                    minimum_track_confidence REAL NOT NULL CHECK (minimum_track_confidence BETWEEN 0.5 AND 1),
                    maximum_geolocation_uncertainty_m REAL NOT NULL
                        CHECK (maximum_geolocation_uncertainty_m BETWEEN 1 AND 100),
                    maximum_velocity_uncertainty_mps REAL NOT NULL
                        CHECK (maximum_velocity_uncertainty_mps BETWEEN 0.1 AND 25),
                    latest_geolocation_id TEXT NOT NULL
                        REFERENCES perception_track_geolocations(id) ON DELETE RESTRICT,
                    latest_target_observed_at_unix_ms INTEGER NOT NULL,
                    operator_lease_expires_at_unix_ms INTEGER,
                    last_agent_update_at_unix_ms INTEGER,
                    validation_reference TEXT NOT NULL,
                    boresight_reference TEXT NOT NULL,
                    boresight_error_bound_deg REAL NOT NULL
                        CHECK (boresight_error_bound_deg > 0 AND boresight_error_bound_deg <= 45),
                    exit_reason_code TEXT NOT NULL DEFAULT '',
                    exit_reason TEXT NOT NULL DEFAULT '',
                    created_at_unix_ms INTEGER NOT NULL,
                    updated_at_unix_ms INTEGER NOT NULL,
                    CHECK (minimum_altitude_relative_m <= altitude_relative_m AND
                           altitude_relative_m <= maximum_altitude_relative_m),
                    CHECK (state <> 'ENDED' OR ended_at_unix_ms IS NOT NULL)
                );
                CREATE UNIQUE INDEX aircraft_follow_sessions_one_active_drone
                    ON aircraft_follow_sessions(drone_id)
                    WHERE state <> 'ENDED';
                CREATE INDEX aircraft_follow_sessions_track_requested
                    ON aircraft_follow_sessions(track_session_id, track_id, requested_at_unix_ms DESC);
                CREATE INDEX aircraft_follow_sessions_state_updated
                    ON aircraft_follow_sessions(state, updated_at_unix_ms);

                CREATE TABLE aircraft_follow_target_updates (
                    id TEXT PRIMARY KEY,
                    aircraft_follow_session_id TEXT NOT NULL
                        REFERENCES aircraft_follow_sessions(id) ON DELETE RESTRICT,
                    geolocation_id TEXT NOT NULL
                        REFERENCES perception_track_geolocations(id) ON DELETE RESTRICT,
                    observed_at_unix_ms INTEGER NOT NULL,
                    latitude REAL NOT NULL CHECK (latitude BETWEEN -90 AND 90),
                    longitude REAL NOT NULL CHECK (longitude BETWEEN -180 AND 180),
                    altitude_amsl_m REAL NOT NULL,
                    velocity_north_mps REAL NOT NULL,
                    velocity_east_mps REAL NOT NULL,
                    horizontal_uncertainty_m REAL NOT NULL CHECK (horizontal_uncertainty_m >= 0),
                    velocity_uncertainty_mps REAL NOT NULL CHECK (velocity_uncertainty_mps >= 0),
                    track_confidence REAL NOT NULL CHECK (track_confidence BETWEEN 0 AND 1),
                    lifecycle_state TEXT NOT NULL,
                    motion_status TEXT NOT NULL,
                    accepted_at_unix_ms INTEGER NOT NULL,
                    UNIQUE(aircraft_follow_session_id, geolocation_id)
                );
                CREATE INDEX aircraft_follow_target_updates_session_observed
                    ON aircraft_follow_target_updates(aircraft_follow_session_id, observed_at_unix_ms DESC);

                CREATE TABLE aircraft_follow_events (
                    id TEXT PRIMARY KEY,
                    aircraft_follow_session_id TEXT NOT NULL
                        REFERENCES aircraft_follow_sessions(id) ON DELETE RESTRICT,
                    sequence INTEGER NOT NULL CHECK (sequence >= 1),
                    event_type TEXT NOT NULL,
                    state TEXT NOT NULL,
                    source TEXT NOT NULL CHECK (source IN (
                        'atlas_native', 'atlas_agent', 'operator', 'watchdog'
                    )),
                    operation_id TEXT NOT NULL DEFAULT '',
                    reason_code TEXT NOT NULL DEFAULT '',
                    message TEXT NOT NULL,
                    evidence_json TEXT NOT NULL DEFAULT '{}',
                    occurred_at_unix_ms INTEGER NOT NULL,
                    UNIQUE(aircraft_follow_session_id, sequence)
                );
                CREATE INDEX aircraft_follow_events_session_sequence
                    ON aircraft_follow_events(aircraft_follow_session_id, sequence);

                PRAGMA user_version = 24;
                COMMIT;
                "#,
            )
            .map_err(|error| format!("apply local database migration 24: {error}"))?;
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
