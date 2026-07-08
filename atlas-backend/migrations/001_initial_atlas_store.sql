CREATE TABLE IF NOT EXISTS organizations (
  id text PRIMARY KEY,
  name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz
);

CREATE TABLE IF NOT EXISTS users (
  id text PRIMARY KEY,
  organization_id text REFERENCES organizations(id) ON DELETE SET NULL,
  email text NOT NULL,
  display_name text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'ACTIVE',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz,
  UNIQUE (organization_id, email)
);

CREATE TABLE IF NOT EXISTS operators (
  id text PRIMARY KEY,
  user_id text REFERENCES users(id) ON DELETE SET NULL,
  organization_id text REFERENCES organizations(id) ON DELETE SET NULL,
  display_name text NOT NULL,
  role text NOT NULL DEFAULT 'OPERATOR',
  status text NOT NULL DEFAULT 'ACTIVE',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz
);

CREATE TABLE IF NOT EXISTS drones (
  id text PRIMARY KEY,
  organization_id text REFERENCES organizations(id) ON DELETE SET NULL,
  name text NOT NULL,
  serial_number text NOT NULL DEFAULT '',
  vehicle_type text NOT NULL DEFAULT 'UNKNOWN',
  px4_system_id integer,
  status text NOT NULL DEFAULT 'REGISTERED',
  home_position jsonb,
  current_state jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz,
  last_seen_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS drones_organization_status_idx
  ON drones (organization_id, status, name);

CREATE TABLE IF NOT EXISTS companion_devices (
  id text PRIMARY KEY,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  device_name text NOT NULL,
  hardware_type text NOT NULL DEFAULT '',
  os_version text NOT NULL DEFAULT '',
  vehicle_agent_version text NOT NULL DEFAULT '',
  hostname text NOT NULL DEFAULT '',
  network_interfaces jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_seen_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS companion_devices_drone_id_idx
  ON companion_devices (drone_id);

CREATE TABLE IF NOT EXISTS vehicle_agents (
  id text PRIMARY KEY,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  companion_device_id text REFERENCES companion_devices(id) ON DELETE SET NULL,
  version text NOT NULL,
  vehicle_agent_version text NOT NULL DEFAULT '',
  identity_status text NOT NULL DEFAULT 'ACTIVE',
  registered_at timestamptz NOT NULL,
  last_seen_at timestamptz,
  revoked_at timestamptz,
  last_heartbeat_at timestamptz,
  command_channel_state text NOT NULL,
  command_channel_connected_at timestamptz,
  command_channel_last_disconnected_at timestamptz
);

CREATE INDEX IF NOT EXISTS vehicle_agents_drone_id_idx
  ON vehicle_agents (drone_id);

CREATE UNIQUE INDEX IF NOT EXISTS vehicle_agents_one_active_per_drone_idx
  ON vehicle_agents (drone_id)
  WHERE identity_status = 'ACTIVE';

CREATE INDEX IF NOT EXISTS vehicle_agents_companion_device_id_idx
  ON vehicle_agents (companion_device_id);

CREATE TABLE IF NOT EXISTS drone_connections (
  id text PRIMARY KEY,
  vehicle_agent_id text NOT NULL REFERENCES vehicle_agents(id) ON DELETE CASCADE,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  connection_id text NOT NULL,
  transport text NOT NULL DEFAULT '',
  remote_address text NOT NULL DEFAULT '',
  wire_guard_peer_id text NOT NULL DEFAULT '',
  status text NOT NULL,
  started_at timestamptz NOT NULL,
  last_heartbeat_at timestamptz,
  ended_at timestamptz,
  ended_reason text NOT NULL DEFAULT '',
  vehicle_agent_version text NOT NULL DEFAULT '',
  capabilities jsonb NOT NULL DEFAULT '[]'::jsonb
);

CREATE INDEX IF NOT EXISTS drone_connections_drone_status_idx
  ON drone_connections (drone_id, status, started_at DESC);

CREATE INDEX IF NOT EXISTS drone_connections_vehicle_agent_status_idx
  ON drone_connections (vehicle_agent_id, status, started_at DESC);

CREATE TABLE IF NOT EXISTS ground_bridges (
  id text PRIMARY KEY,
  organization_id text REFERENCES organizations(id) ON DELETE SET NULL,
  name text NOT NULL,
  device_type text NOT NULL DEFAULT '',
  bridge_version text NOT NULL DEFAULT '',
  identity_status text NOT NULL DEFAULT 'ACTIVE',
  registered_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz,
  revoked_at timestamptz
);

CREATE TABLE IF NOT EXISTS ground_bridge_connections (
  id text PRIMARY KEY,
  ground_bridge_id text NOT NULL REFERENCES ground_bridges(id) ON DELETE CASCADE,
  organization_id text REFERENCES organizations(id) ON DELETE SET NULL,
  status text NOT NULL,
  mode text NOT NULL,
  started_at timestamptz NOT NULL,
  last_heartbeat_at timestamptz,
  ended_at timestamptz,
  ended_reason text NOT NULL DEFAULT '',
  internet_available boolean NOT NULL DEFAULT false,
  local_only boolean NOT NULL DEFAULT false,
  capabilities jsonb NOT NULL DEFAULT '[]'::jsonb
);

CREATE INDEX IF NOT EXISTS ground_bridge_connections_bridge_status_idx
  ON ground_bridge_connections (ground_bridge_id, status, started_at DESC);

CREATE TABLE IF NOT EXISTS communication_links (
  id text PRIMARY KEY,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  vehicle_agent_id text REFERENCES vehicle_agents(id) ON DELETE SET NULL,
  ground_bridge_id text REFERENCES ground_bridges(id) ON DELETE SET NULL,
  drone_connection_id text REFERENCES drone_connections(id) ON DELETE SET NULL,
  ground_bridge_connection_id text REFERENCES ground_bridge_connections(id) ON DELETE SET NULL,
  link_type text NOT NULL,
  roles jsonb NOT NULL DEFAULT '[]'::jsonb,
  status text NOT NULL DEFAULT 'UNKNOWN',
  transport text NOT NULL DEFAULT '',
  endpoint_description text NOT NULL DEFAULT '',
  command_eligible boolean NOT NULL DEFAULT false,
  latency_ms double precision,
  packet_loss_estimate double precision,
  rx_bytes_per_sec double precision,
  tx_bytes_per_sec double precision,
  last_seen_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  ended_at timestamptz,
  ended_reason text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS communication_links_drone_status_idx
  ON communication_links (drone_id, status, link_type);

CREATE INDEX IF NOT EXISTS communication_links_drone_connection_idx
  ON communication_links (drone_connection_id);

CREATE TABLE IF NOT EXISTS telemetry_feeds (
  id text PRIMARY KEY,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  source_type text NOT NULL,
  source_id text NOT NULL DEFAULT '',
  communication_link_id text REFERENCES communication_links(id) ON DELETE SET NULL,
  status text NOT NULL DEFAULT 'UNKNOWN',
  priority integer NOT NULL DEFAULT 100,
  freshness text NOT NULL DEFAULT 'unknown',
  last_telemetry_at timestamptz,
  last_sequence bigint,
  message_rate_hz double precision,
  fields_available jsonb NOT NULL DEFAULT '{}'::jsonb,
  started_at timestamptz NOT NULL DEFAULT now(),
  ended_at timestamptz,
  last_error text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS telemetry_feeds_drone_priority_idx
  ON telemetry_feeds (drone_id, priority, started_at DESC);

CREATE TABLE IF NOT EXISTS telemetry_latest (
  drone_id text PRIMARY KEY REFERENCES drones(id) ON DELETE CASCADE,
  vehicle_agent_id text NOT NULL REFERENCES vehicle_agents(id) ON DELETE CASCADE,
  active_telemetry_feed_id text REFERENCES telemetry_feeds(id) ON DELETE SET NULL,
  source_communication_link_id text REFERENCES communication_links(id) ON DELETE SET NULL,
  observed_at timestamptz NOT NULL,
  received_at timestamptz NOT NULL,
  battery_percent double precision NOT NULL,
  relative_altitude_m double precision NOT NULL,
  altitude_msl double precision NOT NULL DEFAULT 0,
  flight_mode text NOT NULL,
  armed boolean NOT NULL,
  in_air boolean NOT NULL,
  latitude double precision NOT NULL,
  longitude double precision NOT NULL,
  roll_deg double precision NOT NULL DEFAULT 0,
  pitch_deg double precision NOT NULL DEFAULT 0,
  heading_deg double precision NOT NULL,
  velocity_north_mps double precision NOT NULL DEFAULT 0,
  velocity_east_mps double precision NOT NULL DEFAULT 0,
  velocity_down_mps double precision NOT NULL DEFAULT 0,
  ground_speed_mps double precision NOT NULL,
  gps_fix text NOT NULL,
  satellites_visible integer NOT NULL,
  home_position_set boolean NOT NULL,
  mission_current_item integer NOT NULL DEFAULT 0,
  mission_total_items integer NOT NULL DEFAULT 0,
  system_health jsonb NOT NULL DEFAULT '{}'::jsonb,
  source text NOT NULL
);

CREATE INDEX IF NOT EXISTS telemetry_latest_vehicle_agent_id_idx
  ON telemetry_latest (vehicle_agent_id);

CREATE INDEX IF NOT EXISTS telemetry_latest_feed_id_idx
  ON telemetry_latest (active_telemetry_feed_id);

CREATE TABLE IF NOT EXISTS missions (
  id text PRIMARY KEY,
  organization_id text REFERENCES organizations(id) ON DELETE SET NULL,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  created_by text NOT NULL,
  created_by_operator_id text REFERENCES operators(id) ON DELETE SET NULL,
  current_version_id text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  archived_at timestamptz,
  completion_action text NOT NULL,
  validation_status text NOT NULL,
  validation_errors jsonb NOT NULL DEFAULT '[]'::jsonb
);

CREATE INDEX IF NOT EXISTS missions_drone_created_idx
  ON missions (drone_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS missions_organization_created_idx
  ON missions (organization_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS mission_versions (
  id text PRIMARY KEY,
  mission_id text NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
  version_number integer NOT NULL,
  waypoints jsonb NOT NULL DEFAULT '[]'::jsonb,
  altitude_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
  speed_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
  geofence_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
  rtl_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
  validation_status text NOT NULL DEFAULT 'DRAFT',
  validation_errors jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_by_operator_id text REFERENCES operators(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (mission_id, version_number)
);

CREATE TABLE IF NOT EXISTS mission_waypoints (
  mission_id text NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
  sequence integer NOT NULL,
  latitude double precision NOT NULL,
  longitude double precision NOT NULL,
  relative_altitude_m double precision NOT NULL,
  speed_mps double precision,
  loiter_time_s double precision,
  PRIMARY KEY (mission_id, sequence)
);

CREATE TABLE IF NOT EXISTS mission_version_waypoints (
  mission_version_id text NOT NULL REFERENCES mission_versions(id) ON DELETE CASCADE,
  sequence integer NOT NULL,
  latitude double precision NOT NULL,
  longitude double precision NOT NULL,
  relative_altitude_m double precision NOT NULL,
  speed_mps double precision,
  loiter_time_s double precision,
  PRIMARY KEY (mission_version_id, sequence)
);

CREATE TABLE IF NOT EXISTS control_sessions (
  id text PRIMARY KEY,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  requested_by_operator_id text REFERENCES operators(id) ON DELETE SET NULL,
  approved_by_operator_id text REFERENCES operators(id) ON DELETE SET NULL,
  state text NOT NULL,
  allowed_command_set jsonb NOT NULL DEFAULT '[]'::jsonb,
  active_drone_connection_id text REFERENCES drone_connections(id) ON DELETE SET NULL,
  started_at timestamptz,
  expires_at timestamptz,
  ended_at timestamptz,
  ended_reason text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS control_sessions_drone_state_idx
  ON control_sessions (drone_id, state, started_at DESC);

CREATE TABLE IF NOT EXISTS mission_executions (
  id text PRIMARY KEY,
  mission_id text NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
  mission_version_id text REFERENCES mission_versions(id) ON DELETE SET NULL,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  vehicle_agent_id text NOT NULL REFERENCES vehicle_agents(id) ON DELETE CASCADE,
  requested_by text NOT NULL,
  requested_by_operator_id text REFERENCES operators(id) ON DELETE SET NULL,
  upload_requested_by text NOT NULL DEFAULT '',
  start_requested_by text NOT NULL DEFAULT '',
  state text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  last_sent_at timestamptz,
  lease_until timestamptz,
  upload_requested_at timestamptz,
  uploaded_at timestamptz,
  start_requested_at timestamptz,
  started_at timestamptz,
  completed_at timestamptz,
  aborted_at timestamptz,
  hold_at timestamptz,
  failed_at timestamptz,
  failure_reason text NOT NULL DEFAULT '',
  current_mission_item integer NOT NULL DEFAULT 0,
  total_mission_items integer NOT NULL DEFAULT 0,
  progress_updated_at timestamptz,
  progress jsonb NOT NULL DEFAULT '{}'::jsonb,
  delivery_attempt integer NOT NULL DEFAULT 0,
  result_message text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS mission_executions_mission_created_idx
  ON mission_executions (mission_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS mission_executions_drone_state_updated_idx
  ON mission_executions (drone_id, state, updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS mission_executions_vehicle_agent_delivery_idx
  ON mission_executions (vehicle_agent_id, state, lease_until, updated_at);

CREATE TABLE IF NOT EXISTS command_requests (
  id text PRIMARY KEY,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  vehicle_agent_id text NOT NULL REFERENCES vehicle_agents(id) ON DELETE CASCADE,
  mission_execution_id text REFERENCES mission_executions(id) ON DELETE SET NULL,
  type text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  state text NOT NULL,
  requested_by text NOT NULL,
  requested_by_operator_id text REFERENCES operators(id) ON DELETE SET NULL,
  control_session_id text REFERENCES control_sessions(id) ON DELETE SET NULL,
  target_drone_connection_id text REFERENCES drone_connections(id) ON DELETE SET NULL,
  delivery_target text NOT NULL DEFAULT 'VEHICLE_AGENT',
  requires_confirmation boolean NOT NULL DEFAULT false,
  requested_at timestamptz NOT NULL,
  authorized_at timestamptz,
  sent_to_vehicle_agent_at timestamptz,
  updated_at timestamptz NOT NULL,
  last_sent_at timestamptz,
  lease_until timestamptz,
  vehicle_acked_at timestamptz,
  completed_at timestamptz,
  failed_at timestamptz,
  failure_reason text NOT NULL DEFAULT '',
  idempotency_key text NOT NULL DEFAULT '',
  confirmation_baseline jsonb,
  delivery_attempt integer NOT NULL DEFAULT 0,
  policy_reason text NOT NULL DEFAULT '',
  result_message text NOT NULL DEFAULT '',
  telemetry_state text NOT NULL,
  vehicle_agent_status text NOT NULL
);

CREATE INDEX IF NOT EXISTS command_requests_drone_requested_idx
  ON command_requests (drone_id, requested_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS command_requests_agent_delivery_idx
  ON command_requests (vehicle_agent_id, state, lease_until, requested_at);

CREATE INDEX IF NOT EXISTS command_requests_mission_execution_idx
  ON command_requests (mission_execution_id, requested_at DESC);

CREATE INDEX IF NOT EXISTS command_requests_connection_idx
  ON command_requests (target_drone_connection_id, requested_at DESC);

CREATE TABLE IF NOT EXISTS command_events (
  id text PRIMARY KEY,
  command_request_id text NOT NULL REFERENCES command_requests(id) ON DELETE CASCADE,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  vehicle_agent_id text NOT NULL REFERENCES vehicle_agents(id) ON DELETE CASCADE,
  event_type text NOT NULL,
  state text NOT NULL,
  message text NOT NULL DEFAULT '',
  source text NOT NULL,
  raw_ack_code text NOT NULL DEFAULT '',
  telemetry_snapshot_id text,
  created_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS command_events_request_created_idx
  ON command_events (command_request_id, created_at, id);

CREATE INDEX IF NOT EXISTS command_events_drone_created_idx
  ON command_events (drone_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS mission_execution_events (
  id text PRIMARY KEY,
  execution_id text NOT NULL REFERENCES mission_executions(id) ON DELETE CASCADE,
  mission_id text NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
  mission_version_id text REFERENCES mission_versions(id) ON DELETE SET NULL,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  vehicle_agent_id text NOT NULL REFERENCES vehicle_agents(id) ON DELETE CASCADE,
  event_type text NOT NULL,
  state text NOT NULL,
  message text NOT NULL DEFAULT '',
  current_mission_item integer NOT NULL DEFAULT 0,
  total_mission_items integer NOT NULL DEFAULT 0,
  source text NOT NULL,
  created_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS mission_execution_events_mission_created_idx
  ON mission_execution_events (mission_id, created_at, id);

CREATE INDEX IF NOT EXISTS mission_execution_events_execution_created_idx
  ON mission_execution_events (execution_id, created_at, id);

CREATE INDEX IF NOT EXISTS mission_execution_events_drone_created_idx
  ON mission_execution_events (drone_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS telemetry_samples (
  id text PRIMARY KEY,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  mission_execution_id text REFERENCES mission_executions(id) ON DELETE SET NULL,
  telemetry_feed_id text REFERENCES telemetry_feeds(id) ON DELETE SET NULL,
  sampled_at timestamptz NOT NULL,
  snapshot jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS telemetry_samples_drone_sampled_idx
  ON telemetry_samples (drone_id, sampled_at DESC);

CREATE INDEX IF NOT EXISTS telemetry_samples_mission_sampled_idx
  ON telemetry_samples (mission_execution_id, sampled_at);

CREATE TABLE IF NOT EXISTS link_health_samples (
  id text PRIMARY KEY,
  communication_link_id text NOT NULL REFERENCES communication_links(id) ON DELETE CASCADE,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  sampled_at timestamptz NOT NULL,
  status text NOT NULL,
  latency_ms double precision,
  packet_loss_estimate double precision,
  rx_bytes_per_sec double precision,
  tx_bytes_per_sec double precision,
  quality text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS link_health_samples_link_sampled_idx
  ON link_health_samples (communication_link_id, sampled_at DESC);

CREATE TABLE IF NOT EXISTS camera_devices (
  id text PRIMARY KEY,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  name text NOT NULL,
  camera_type text NOT NULL DEFAULT 'UNKNOWN',
  source_description text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'UNKNOWN',
  active_video_feed_id text,
  last_seen_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS gimbal_devices (
  id text PRIMARY KEY,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  name text NOT NULL,
  gimbal_type text NOT NULL DEFAULT 'UNKNOWN',
  status text NOT NULL DEFAULT 'UNKNOWN',
  pitch_deg double precision NOT NULL DEFAULT 0,
  yaw_deg double precision NOT NULL DEFAULT 0,
  roll_deg double precision NOT NULL DEFAULT 0,
  control_source text NOT NULL DEFAULT 'UNKNOWN',
  last_seen_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS video_feeds (
  id text PRIMARY KEY,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  camera_device_id text REFERENCES camera_devices(id) ON DELETE SET NULL,
  source_type text NOT NULL,
  source_id text NOT NULL DEFAULT '',
  communication_link_id text REFERENCES communication_links(id) ON DELETE SET NULL,
  status text NOT NULL,
  transport_in text NOT NULL DEFAULT '',
  transport_out text NOT NULL DEFAULT '',
  codec text NOT NULL DEFAULT '',
  resolution text NOT NULL DEFAULT '',
  bitrate_kbps double precision,
  frame_rate double precision,
  latency_ms double precision,
  last_frame_at timestamptz,
  started_at timestamptz NOT NULL DEFAULT now(),
  ended_at timestamptz,
  ended_reason text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS video_feeds_drone_status_idx
  ON video_feeds (drone_id, status, started_at DESC);

CREATE TABLE IF NOT EXISTS perception_events (
  id text PRIMARY KEY,
  drone_id text NOT NULL REFERENCES drones(id) ON DELETE CASCADE,
  camera_device_id text REFERENCES camera_devices(id) ON DELETE SET NULL,
  video_feed_id text REFERENCES video_feeds(id) ON DELETE SET NULL,
  observed_at timestamptz NOT NULL,
  frame_id text NOT NULL DEFAULT '',
  model_name text NOT NULL DEFAULT '',
  model_version text NOT NULL DEFAULT '',
  detections jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS perception_events_drone_observed_idx
  ON perception_events (drone_id, observed_at DESC);

CREATE TABLE IF NOT EXISTS artifact_syncs (
  id text PRIMARY KEY,
  drone_id text REFERENCES drones(id) ON DELETE SET NULL,
  vehicle_agent_id text REFERENCES vehicle_agents(id) ON DELETE SET NULL,
  ground_bridge_id text REFERENCES ground_bridges(id) ON DELETE SET NULL,
  source_device_id text NOT NULL DEFAULT '',
  mission_execution_id text REFERENCES mission_executions(id) ON DELETE SET NULL,
  artifact_type text NOT NULL,
  storage_location text NOT NULL DEFAULT '',
  sync_status text NOT NULL,
  priority integer NOT NULL DEFAULT 100,
  bytes_total bigint NOT NULL DEFAULT 0,
  bytes_uploaded bigint NOT NULL DEFAULT 0,
  started_at timestamptz,
  completed_at timestamptz,
  failed_at timestamptz,
  failure_reason text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS artifact_syncs_drone_status_idx
  ON artifact_syncs (drone_id, sync_status, created_at DESC);

CREATE TABLE IF NOT EXISTS artifact_metadata (
  id text PRIMARY KEY,
  artifact_sync_id text REFERENCES artifact_syncs(id) ON DELETE SET NULL,
  drone_id text REFERENCES drones(id) ON DELETE SET NULL,
  mission_execution_id text REFERENCES mission_executions(id) ON DELETE SET NULL,
  artifact_type text NOT NULL,
  file_name text NOT NULL DEFAULT '',
  content_type text NOT NULL DEFAULT '',
  storage_location text NOT NULL,
  checksum_sha256 text NOT NULL DEFAULT '',
  bytes bigint NOT NULL DEFAULT 0,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS artifact_metadata_drone_created_idx
  ON artifact_metadata (drone_id, created_at DESC);

CREATE TABLE IF NOT EXISTS audit_events (
  id text PRIMARY KEY,
  organization_id text REFERENCES organizations(id) ON DELETE SET NULL,
  drone_id text REFERENCES drones(id) ON DELETE SET NULL,
  operator_id text REFERENCES operators(id) ON DELETE SET NULL,
  event_type text NOT NULL,
  entity_type text NOT NULL DEFAULT '',
  entity_id text NOT NULL DEFAULT '',
  message text NOT NULL DEFAULT '',
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_events_org_created_idx
  ON audit_events (organization_id, created_at DESC);

CREATE INDEX IF NOT EXISTS audit_events_drone_created_idx
  ON audit_events (drone_id, created_at DESC);

CREATE TABLE IF NOT EXISTS device_credentials (
  id text PRIMARY KEY,
  device_type text NOT NULL,
  device_id text NOT NULL,
  credential_type text NOT NULL,
  status text NOT NULL,
  credential_hash text NOT NULL DEFAULT '',
  public_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  issued_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz,
  rotated_at timestamptz,
  revoked_at timestamptz,
  last_used_at timestamptz
);

CREATE INDEX IF NOT EXISTS device_credentials_device_status_idx
  ON device_credentials (device_type, device_id, status);
