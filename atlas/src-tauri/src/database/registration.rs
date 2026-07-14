use rusqlite::{params, OptionalExtension, Transaction};

use super::LocalDatabase;

pub(crate) const ARCHIVED_REGISTRATION_ERROR: &str = "archived aircraft registration rejected";

#[derive(Debug, Clone)]
pub(crate) struct RegistrationInput {
    pub session_id: String,
    pub installation_id: String,
    pub agent_version: String,
    pub protocol_version: String,
    pub device_profile_json: String,
    pub capabilities_json: String,
    pub drone_id: String,
    pub drone_name: String,
    pub flight_controller_uid: String,
    pub serial_number: String,
    pub vehicle_type: String,
    pub flight_controller_transport: String,
    pub endpoint_description: String,
    pub baud_rate: u32,
    pub mavlink_system_id: u32,
    pub mavlink_component_id: u32,
    pub remote_address: String,
    pub observed_at_unix_ms: i64,
}

#[derive(Debug, Clone)]
pub(crate) struct RegisteredSession {
    pub agent_id: String,
    pub drone_id: String,
    pub binding_id: String,
    pub communication_link_id: String,
}

impl LocalDatabase {
    pub(crate) fn register_agent(
        &self,
        input: &RegistrationInput,
    ) -> Result<RegisteredSession, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin agent registration: {error}"))?;

        let existing_status = tx
            .query_row(
                "SELECT status FROM drones WHERE id = ?1",
                [&input.drone_id],
                |row| row.get::<_, String>(0),
            )
            .optional()
            .map_err(|error| format!("read aircraft lifecycle during registration: {error}"))?;
        if existing_status.as_deref() == Some("archived") {
            let event_id = generate_id(&tx)?;
            let details = serde_json::json!({
                "installationId": input.installation_id,
                "sessionId": input.session_id,
                "remoteAddress": input.remote_address,
            })
            .to_string();
            tx.execute(
                r#"
                INSERT INTO drone_lifecycle_events (
                    id, drone_id, event_type, reason,
                    occurred_at_unix_ms, details_json
                ) VALUES (?1, ?2, 'archived_reconnect_rejected',
                          'agent registration rejected while archived', ?3, ?4)
                "#,
                params![event_id, input.drone_id, input.observed_at_unix_ms, details],
            )
            .map_err(|error| format!("record archived reconnect rejection: {error}"))?;
            tx.commit()
                .map_err(|error| format!("commit archived reconnect rejection: {error}"))?;
            return Err(format!(
                "{ARCHIVED_REGISTRATION_ERROR}: restore {} before reconnecting it",
                input.drone_id
            ));
        }

        tx.execute(
            r#"
            INSERT INTO drones (
                id, name, flight_controller_uid, serial_number, vehicle_type,
                status, created_at_unix_ms, updated_at_unix_ms
            ) VALUES (?1, ?2, NULLIF(?3, ''), ?4, ?5, 'active', ?6, ?6)
            ON CONFLICT(id) DO UPDATE SET
                name = excluded.name,
                flight_controller_uid = COALESCE(drones.flight_controller_uid, excluded.flight_controller_uid),
                serial_number = CASE WHEN drones.serial_number = '' THEN excluded.serial_number ELSE drones.serial_number END,
                vehicle_type = CASE WHEN drones.vehicle_type = 'unknown' THEN excluded.vehicle_type ELSE drones.vehicle_type END,
                updated_at_unix_ms = excluded.updated_at_unix_ms
            "#,
            params![
                input.drone_id,
                input.drone_name,
                input.flight_controller_uid,
                input.serial_number,
                normalized_vehicle_type(&input.vehicle_type),
                input.observed_at_unix_ms,
            ],
        )
        .map_err(|error| format!("upsert local drone: {error}"))?;

        tx.execute(
            r#"
            INSERT INTO vehicle_agents (
                id, installation_id, agent_version, protocol_version,
                device_profile_json, capabilities_json, registered_at_unix_ms,
                updated_at_unix_ms
            ) VALUES (?1, ?1, ?2, ?3, ?4, ?5, ?6, ?6)
            ON CONFLICT(id) DO UPDATE SET
                agent_version = excluded.agent_version,
                protocol_version = excluded.protocol_version,
                device_profile_json = excluded.device_profile_json,
                capabilities_json = excluded.capabilities_json,
                updated_at_unix_ms = excluded.updated_at_unix_ms
            "#,
            params![
                input.installation_id,
                input.agent_version,
                input.protocol_version,
                input.device_profile_json,
                input.capabilities_json,
                input.observed_at_unix_ms,
            ],
        )
        .map_err(|error| format!("upsert local vehicle agent: {error}"))?;

        let binding_id = match active_binding_id(&tx, &input.installation_id, &input.drone_id)? {
            Some(binding_id) => binding_id,
            None => generate_id(&tx)?,
        };
        tx.execute(
            "UPDATE vehicle_agent_bindings SET status = 'ended', ended_at_unix_ms = ?3, end_reason = 'superseded by registration' WHERE status = 'active' AND (vehicle_agent_id = ?1 OR drone_id = ?2) AND id <> ?4",
            params![input.installation_id, input.drone_id, input.observed_at_unix_ms, binding_id],
        )
        .map_err(|error| format!("end superseded vehicle-agent bindings: {error}"))?;

        tx.execute(
            r#"
            INSERT INTO vehicle_agent_bindings (
                id, vehicle_agent_id, drone_id, status,
                flight_controller_transport, endpoint_description, baud_rate,
                mavlink_system_id, mavlink_component_id,
                observed_flight_controller_uid, bound_at_unix_ms
            ) VALUES (?1, ?2, ?3, 'active', ?4, ?5, NULLIF(?6, 0), NULLIF(?7, 0), NULLIF(?8, 0), ?9, ?10)
            ON CONFLICT(id) DO UPDATE SET
                status = 'active',
                flight_controller_transport = excluded.flight_controller_transport,
                endpoint_description = excluded.endpoint_description,
                baud_rate = excluded.baud_rate,
                mavlink_system_id = excluded.mavlink_system_id,
                mavlink_component_id = excluded.mavlink_component_id,
                observed_flight_controller_uid = excluded.observed_flight_controller_uid,
                ended_at_unix_ms = NULL,
                end_reason = ''
            "#,
            params![
                binding_id,
                input.installation_id,
                input.drone_id,
                input.flight_controller_transport,
                input.endpoint_description,
                input.baud_rate,
                input.mavlink_system_id,
                input.mavlink_component_id,
                input.flight_controller_uid,
                input.observed_at_unix_ms,
            ],
        )
        .map_err(|error| format!("upsert vehicle-agent binding: {error}"))?;

        tx.execute(
            "UPDATE communication_links SET status = 'disconnected', ended_at_unix_ms = ?2, ended_reason = 'superseded by reconnect' WHERE vehicle_agent_binding_id = ?1 AND ended_at_unix_ms IS NULL",
            params![binding_id, input.observed_at_unix_ms],
        )
        .map_err(|error| format!("close superseded communication links: {error}"))?;

        let link_id = generate_id(&tx)?;
        tx.execute(
            r#"
            INSERT INTO communication_links (
                id, vehicle_agent_binding_id, session_instance_id, transport,
                roles_json, status, remote_address, started_at_unix_ms,
                last_heartbeat_at_unix_ms
            ) VALUES (?1, ?2, ?3, 'grpc', '["registration","heartbeat","telemetry","status_text","command"]',
                      'connected', ?4, ?5, ?5)
            "#,
            params![
                link_id,
                binding_id,
                input.session_id,
                input.remote_address,
                input.observed_at_unix_ms,
            ],
        )
        .map_err(|error| format!("insert communication link: {error}"))?;

        tx.commit()
            .map_err(|error| format!("commit agent registration: {error}"))?;
        Ok(RegisteredSession {
            agent_id: input.installation_id.clone(),
            drone_id: input.drone_id.clone(),
            binding_id,
            communication_link_id: link_id,
        })
    }
}

fn active_binding_id(
    tx: &Transaction<'_>,
    agent_id: &str,
    drone_id: &str,
) -> Result<Option<String>, String> {
    tx.query_row(
        "SELECT id FROM vehicle_agent_bindings WHERE vehicle_agent_id = ?1 AND drone_id = ?2 AND status = 'active' LIMIT 1",
        params![agent_id, drone_id],
        |row| row.get(0),
    )
    .optional()
    .map_err(|error| format!("find active vehicle-agent binding: {error}"))
}

fn generate_id(tx: &Transaction<'_>) -> Result<String, String> {
    tx.query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate local identifier: {error}"))
}

fn normalized_vehicle_type(value: &str) -> &str {
    match value {
        "multicopter" | "fixed_wing" | "vtol" | "rover" => value,
        _ => "unknown",
    }
}
