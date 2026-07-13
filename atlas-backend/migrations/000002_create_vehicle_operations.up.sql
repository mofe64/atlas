-- Vehicle operations are organization-owned. Composite keys on (organization_id,
-- id) let relationship tables enforce tenant consistency in PostgreSQL rather
-- than relying only on service code.
ALTER TABLE users
    ADD CONSTRAINT users_organization_id_id_key UNIQUE (organization_id, id);

CREATE TABLE drones (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    name VARCHAR(120) NOT NULL,
    flight_controller_uid VARCHAR(200),
    serial_number VARCHAR(200) NOT NULL DEFAULT '',
    vehicle_type VARCHAR(24) NOT NULL DEFAULT 'unknown'
        CHECK (vehicle_type IN ('unknown', 'multicopter', 'fixed_wing', 'vtol', 'rover')),
    status VARCHAR(24) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'maintenance', 'disabled', 'archived')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at TIMESTAMPTZ,
    UNIQUE (organization_id, id)
);

CREATE UNIQUE INDEX drones_flight_controller_uid_key
    ON drones (flight_controller_uid)
    WHERE flight_controller_uid IS NOT NULL;
CREATE INDEX drones_organization_status_idx ON drones (organization_id, status, name);

CREATE TABLE vehicle_agents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    installation_id VARCHAR(128) NOT NULL,
    public_key BYTEA NOT NULL CHECK (octet_length(public_key) = 32),
    status VARCHAR(24) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'suspended', 'revoked', 'rotation_required')),
    agent_version VARCHAR(64) NOT NULL,
    protocol_version VARCHAR(32) NOT NULL,
    device_profile JSONB NOT NULL DEFAULT '{}'::jsonb,
    capabilities JSONB NOT NULL DEFAULT '[]'::jsonb,
    enrollment_request_id VARCHAR(128) NOT NULL,
    enrolled_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, installation_id),
    UNIQUE (organization_id, enrollment_request_id),
    UNIQUE (public_key)
);

CREATE TABLE vehicle_agent_bindings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    vehicle_agent_id UUID NOT NULL,
    drone_id UUID NOT NULL,
    status VARCHAR(16) NOT NULL
        CHECK (status IN ('pending', 'active', 'suspended', 'ended')),
    flight_controller_transport VARCHAR(32) NOT NULL DEFAULT '',
    endpoint_description VARCHAR(300) NOT NULL DEFAULT '',
    baud_rate INTEGER CHECK (baud_rate IS NULL OR baud_rate > 0),
    mavlink_system_id SMALLINT CHECK (mavlink_system_id IS NULL OR mavlink_system_id BETWEEN 1 AND 255),
    mavlink_component_id SMALLINT CHECK (mavlink_component_id IS NULL OR mavlink_component_id BETWEEN 1 AND 255),
    observed_flight_controller_uid VARCHAR(200) NOT NULL DEFAULT '',
    bound_at TIMESTAMPTZ NOT NULL,
    verified_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ,
    end_reason VARCHAR(300) NOT NULL DEFAULT '',
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, vehicle_agent_id)
        REFERENCES vehicle_agents (organization_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (organization_id, drone_id)
        REFERENCES drones (organization_id, id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX vehicle_agent_bindings_one_current_per_agent
    ON vehicle_agent_bindings (vehicle_agent_id)
    WHERE status IN ('pending', 'active', 'suspended');
CREATE UNIQUE INDEX vehicle_agent_bindings_one_current_per_drone
    ON vehicle_agent_bindings (drone_id)
    WHERE status IN ('pending', 'active', 'suspended');
CREATE INDEX vehicle_agent_bindings_drone_history_idx
    ON vehicle_agent_bindings (organization_id, drone_id, bound_at DESC);

CREATE TABLE communication_links (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    vehicle_agent_binding_id UUID NOT NULL,
    session_instance_id VARCHAR(128) NOT NULL,
    transport VARCHAR(32) NOT NULL,
    roles JSONB NOT NULL DEFAULT '[]'::jsonb,
    status VARCHAR(16) NOT NULL DEFAULT 'connecting'
        CHECK (status IN ('connecting', 'healthy', 'degraded', 'stale', 'closed', 'rejected')),
    remote_address VARCHAR(300) NOT NULL DEFAULT '',
    command_eligible BOOLEAN NOT NULL DEFAULT false,
    opened_at TIMESTAMPTZ NOT NULL,
    first_heartbeat_at TIMESTAMPTZ,
    last_heartbeat_at TIMESTAMPTZ,
    latency_ms DOUBLE PRECISION CHECK (latency_ms IS NULL OR latency_ms >= 0),
    packet_loss_estimate DOUBLE PRECISION
        CHECK (packet_loss_estimate IS NULL OR packet_loss_estimate BETWEEN 0 AND 1),
    rx_bytes_per_second DOUBLE PRECISION CHECK (rx_bytes_per_second IS NULL OR rx_bytes_per_second >= 0),
    tx_bytes_per_second DOUBLE PRECISION CHECK (tx_bytes_per_second IS NULL OR tx_bytes_per_second >= 0),
    closed_at TIMESTAMPTZ,
    close_reason VARCHAR(300) NOT NULL DEFAULT '',
    UNIQUE (organization_id, id),
    UNIQUE (vehicle_agent_binding_id, session_instance_id),
    FOREIGN KEY (organization_id, vehicle_agent_binding_id)
        REFERENCES vehicle_agent_bindings (organization_id, id) ON DELETE RESTRICT
);

CREATE INDEX communication_links_binding_opened_idx
    ON communication_links (organization_id, vehicle_agent_binding_id, opened_at DESC);
CREATE INDEX communication_links_open_health_idx
    ON communication_links (status, last_heartbeat_at)
    WHERE closed_at IS NULL;

CREATE TABLE vehicle_agent_enrollment_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    created_by_user_id UUID NOT NULL,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    scoped_drone_id UUID,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    enrollment_request_id VARCHAR(128),
    enrolled_vehicle_agent_id UUID,
    enrolled_vehicle_agent_binding_id UUID,
    CHECK (expires_at > created_at),
    CHECK (
        (used_at IS NULL AND enrollment_request_id IS NULL AND enrolled_vehicle_agent_id IS NULL
            AND enrolled_vehicle_agent_binding_id IS NULL)
        OR
        (used_at IS NOT NULL AND enrollment_request_id IS NOT NULL AND enrolled_vehicle_agent_id IS NOT NULL
            AND enrolled_vehicle_agent_binding_id IS NOT NULL)
    ),
    FOREIGN KEY (organization_id, created_by_user_id)
        REFERENCES users (organization_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (organization_id, scoped_drone_id)
        REFERENCES drones (organization_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (organization_id, enrolled_vehicle_agent_id)
        REFERENCES vehicle_agents (organization_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (organization_id, enrolled_vehicle_agent_binding_id)
        REFERENCES vehicle_agent_bindings (organization_id, id) ON DELETE RESTRICT
);

CREATE INDEX vehicle_agent_enrollment_tokens_expiry_idx
    ON vehicle_agent_enrollment_tokens (expires_at)
    WHERE used_at IS NULL;
