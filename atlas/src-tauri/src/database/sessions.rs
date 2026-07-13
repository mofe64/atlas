use rusqlite::params;

use super::LocalDatabase;

impl LocalDatabase {
    pub(crate) fn active_session_matches(
        &self,
        session_id: &str,
        installation_id: &str,
        drone_id: &str,
    ) -> Result<bool, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        connection
            .query_row(
                r#"
                SELECT EXISTS (
                    SELECT 1
                    FROM communication_links l
                    JOIN vehicle_agent_bindings b ON b.id = l.vehicle_agent_binding_id
                    JOIN vehicle_agents a ON a.id = b.vehicle_agent_id
                    WHERE l.session_instance_id = ?1
                      AND a.installation_id = ?2
                      AND b.drone_id = ?3
                      AND l.ended_at_unix_ms IS NULL
                      AND l.status = 'connected'
                )
                "#,
                params![session_id, installation_id, drone_id],
                |row| row.get::<_, bool>(0),
            )
            .map_err(|error| format!("validate active agent session: {error}"))
    }

    pub(crate) fn record_heartbeat(
        &self,
        session_id: &str,
        observed_at_unix_ms: i64,
    ) -> Result<(), String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let changed = connection
            .execute(
                "UPDATE communication_links SET status = 'connected', last_heartbeat_at_unix_ms = ?2 WHERE session_instance_id = ?1 AND ended_at_unix_ms IS NULL",
                params![session_id, observed_at_unix_ms],
            )
            .map_err(|error| format!("record communication-link heartbeat: {error}"))?;
        if changed != 1 {
            return Err("communication link is not open".to_string());
        }
        Ok(())
    }

    pub(crate) fn close_session(
        &self,
        session_id: &str,
        reason: &str,
        ended_at_unix_ms: i64,
    ) -> Result<(), String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        connection
            .execute(
                "UPDATE communication_links SET status = 'disconnected', ended_at_unix_ms = ?2, ended_reason = ?3 WHERE session_instance_id = ?1 AND ended_at_unix_ms IS NULL",
                params![session_id, ended_at_unix_ms, reason],
            )
            .map_err(|error| format!("close communication link: {error}"))?;
        Ok(())
    }
}
