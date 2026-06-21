package registry

import (
	"context"
	"database/sql"
	"fmt"
)

func applySQLiteSchema(ctx context.Context, db *sql.DB) error {
	for _, statement := range sqliteSchemaStatements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply sqlite schema: %w", err)
		}
	}
	return nil
}

var sqliteSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS atlas_sequences (
		name text PRIMARY KEY,
		value integer NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS drones (
		id text PRIMARY KEY,
		name text NOT NULL,
		last_seen_at timestamp NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS agents (
		id text PRIMARY KEY,
		drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
		version text NOT NULL,
		registered_at timestamp NOT NULL,
		last_heartbeat_at timestamp,
		command_channel_state text NOT NULL,
		command_channel_connected_at timestamp,
		command_channel_last_disconnected_at timestamp
	)`,
	`CREATE INDEX IF NOT EXISTS agents_drone_id_idx ON agents (drone_id)`,
	`CREATE TABLE IF NOT EXISTS telemetry_latest (
		drone_id text PRIMARY KEY REFERENCES drones(id) ON DELETE CASCADE,
		agent_id text NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		observed_at timestamp NOT NULL,
		received_at timestamp NOT NULL,
		battery_percent real NOT NULL,
		relative_altitude_m real NOT NULL,
		flight_mode text NOT NULL,
		armed boolean NOT NULL,
		in_air boolean NOT NULL,
		latitude real NOT NULL,
		longitude real NOT NULL,
		heading_deg real NOT NULL,
		ground_speed_mps real NOT NULL,
		gps_fix text NOT NULL,
		satellites_visible integer NOT NULL,
		home_position_set boolean NOT NULL,
		source text NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS telemetry_latest_agent_id_idx ON telemetry_latest (agent_id)`,
	`CREATE TABLE IF NOT EXISTS operator_commands (
		id text PRIMARY KEY,
		drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
		agent_id text NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		type text NOT NULL,
		state text NOT NULL,
		requested_by text NOT NULL,
		requested_at timestamp NOT NULL,
		updated_at timestamp NOT NULL,
		last_sent_at timestamp,
		lease_until timestamp,
		vehicle_acked_at timestamp,
		confirmation_baseline text,
		delivery_attempt integer NOT NULL DEFAULT 0,
		policy_reason text NOT NULL DEFAULT '',
		result_message text NOT NULL DEFAULT '',
		telemetry_state text NOT NULL,
		agent_status text NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS operator_commands_drone_requested_idx ON operator_commands (drone_id, requested_at DESC, id DESC)`,
	`CREATE INDEX IF NOT EXISTS operator_commands_agent_delivery_idx ON operator_commands (agent_id, state, lease_until, requested_at)`,
	`CREATE TABLE IF NOT EXISTS command_events (
		id text PRIMARY KEY,
		command_id text NOT NULL REFERENCES operator_commands(id) ON DELETE CASCADE,
		drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
		agent_id text NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		event_type text NOT NULL,
		state text NOT NULL,
		message text NOT NULL DEFAULT '',
		source text NOT NULL,
		created_at timestamp NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS command_events_command_created_idx ON command_events (command_id, created_at, id)`,
	`CREATE INDEX IF NOT EXISTS command_events_drone_created_idx ON command_events (drone_id, created_at DESC, id DESC)`,
	`CREATE TABLE IF NOT EXISTS missions (
		id text PRIMARY KEY,
		drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
		name text NOT NULL,
		created_by text NOT NULL,
		created_at timestamp NOT NULL,
		updated_at timestamp NOT NULL,
		completion_action text NOT NULL,
		validation_status text NOT NULL,
		validation_errors text NOT NULL DEFAULT '[]'
	)`,
	`CREATE INDEX IF NOT EXISTS missions_drone_created_idx ON missions (drone_id, created_at DESC, id DESC)`,
	`CREATE TABLE IF NOT EXISTS mission_waypoints (
		mission_id text NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
		sequence integer NOT NULL,
		latitude real NOT NULL,
		longitude real NOT NULL,
		relative_altitude_m real NOT NULL,
		speed_mps real,
		loiter_time_s real,
		PRIMARY KEY (mission_id, sequence)
	)`,
	`CREATE TABLE IF NOT EXISTS mission_executions (
		id text PRIMARY KEY,
		mission_id text NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
		drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
		agent_id text NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		requested_by text NOT NULL,
		upload_requested_by text NOT NULL DEFAULT '',
		start_requested_by text NOT NULL DEFAULT '',
		state text NOT NULL,
		created_at timestamp NOT NULL,
		updated_at timestamp NOT NULL,
		last_sent_at timestamp,
		lease_until timestamp,
		upload_requested_at timestamp,
		uploaded_at timestamp,
		start_requested_at timestamp,
		started_at timestamp,
		completed_at timestamp,
		hold_at timestamp,
		failed_at timestamp,
		current_mission_item integer NOT NULL DEFAULT 0,
		total_mission_items integer NOT NULL DEFAULT 0,
		progress_updated_at timestamp,
		delivery_attempt integer NOT NULL DEFAULT 0,
		result_message text NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS mission_executions_mission_created_idx ON mission_executions (mission_id, created_at DESC, id DESC)`,
	`CREATE INDEX IF NOT EXISTS mission_executions_drone_state_updated_idx ON mission_executions (drone_id, state, updated_at DESC, id DESC)`,
	`CREATE INDEX IF NOT EXISTS mission_executions_agent_delivery_idx ON mission_executions (agent_id, state, lease_until, updated_at)`,
	`CREATE TABLE IF NOT EXISTS mission_execution_events (
		id text PRIMARY KEY,
		execution_id text NOT NULL REFERENCES mission_executions(id) ON DELETE CASCADE,
		mission_id text NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
		drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
		agent_id text NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		event_type text NOT NULL,
		state text NOT NULL,
		message text NOT NULL DEFAULT '',
		current_mission_item integer NOT NULL DEFAULT 0,
		total_mission_items integer NOT NULL DEFAULT 0,
		source text NOT NULL,
		created_at timestamp NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS mission_execution_events_mission_created_idx ON mission_execution_events (mission_id, created_at, id)`,
	`CREATE INDEX IF NOT EXISTS mission_execution_events_execution_created_idx ON mission_execution_events (execution_id, created_at, id)`,
	`CREATE INDEX IF NOT EXISTS mission_execution_events_drone_created_idx ON mission_execution_events (drone_id, created_at DESC, id DESC)`,
}
