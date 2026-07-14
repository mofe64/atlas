use rusqlite::{params, OptionalExtension};

use super::{unix_time_ms, LocalDatabase, OperationsSnapshot};

const FRESH_LINK_WINDOW_MS: i64 = 15_000;

impl LocalDatabase {
    /// Archives a disconnected aircraft without deleting any operational
    /// evidence. Bindings and stale/open links are ended in the same
    /// transaction as the lifecycle transition.
    pub(crate) fn archive_drone(
        &self,
        drone_id: &str,
        reason: &str,
    ) -> Result<OperationsSnapshot, String> {
        if drone_id.trim().is_empty() {
            return Err("drone id is required".to_string());
        }
        let now = unix_time_ms();
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin aircraft archive: {error}"))?;
        let status = tx
            .query_row(
                "SELECT status FROM drones WHERE id = ?1",
                [drone_id],
                |row| row.get::<_, String>(0),
            )
            .optional()
            .map_err(|error| format!("read aircraft lifecycle: {error}"))?
            .ok_or_else(|| format!("aircraft {drone_id} is not registered"))?;

        if status == "archived" {
            drop(tx);
            drop(connection);
            return self.operations_snapshot_for(Some(drone_id));
        }

        let fresh_link_exists = tx
            .query_row(
                r#"
                SELECT EXISTS (
                    SELECT 1
                    FROM communication_links l
                    JOIN vehicle_agent_bindings b
                      ON b.id = l.vehicle_agent_binding_id
                    WHERE b.drone_id = ?1
                      AND l.ended_at_unix_ms IS NULL
                      AND l.status = 'connected'
                      AND COALESCE(l.last_heartbeat_at_unix_ms,
                                   l.started_at_unix_ms) >= ?2
                )
                "#,
                params![drone_id, now - FRESH_LINK_WINDOW_MS],
                |row| row.get::<_, bool>(0),
            )
            .map_err(|error| format!("check aircraft communication link: {error}"))?;
        if fresh_link_exists {
            return Err(
                "disconnect the aircraft and wait for its ground link to close before archiving"
                    .to_string(),
            );
        }

        tx.execute(
            r#"
            UPDATE communication_links
            SET status = 'disconnected', ended_at_unix_ms = ?2,
                ended_reason = 'aircraft archived'
            WHERE ended_at_unix_ms IS NULL
              AND vehicle_agent_binding_id IN (
                  SELECT id FROM vehicle_agent_bindings WHERE drone_id = ?1
              )
            "#,
            params![drone_id, now],
        )
        .map_err(|error| format!("close archived aircraft links: {error}"))?;
        tx.execute(
            r#"
            UPDATE vehicle_agent_bindings
            SET status = 'ended', ended_at_unix_ms = ?2,
                end_reason = 'aircraft archived'
            WHERE drone_id = ?1 AND status IN ('active', 'suspended')
            "#,
            params![drone_id, now],
        )
        .map_err(|error| format!("end archived aircraft bindings: {error}"))?;
        tx.execute(
            "UPDATE drones SET status = 'archived', updated_at_unix_ms = ?2 WHERE id = ?1",
            params![drone_id, now],
        )
        .map_err(|error| format!("archive aircraft: {error}"))?;
        let event_id = generate_id(&tx)?;
        tx.execute(
            r#"
            INSERT INTO drone_lifecycle_events (
                id, drone_id, event_type, reason, occurred_at_unix_ms
            ) VALUES (?1, ?2, 'archived', ?3, ?4)
            "#,
            params![event_id, drone_id, reason.trim(), now],
        )
        .map_err(|error| format!("record aircraft archive: {error}"))?;
        tx.commit()
            .map_err(|error| format!("commit aircraft archive: {error}"))?;
        drop(connection);
        self.operations_snapshot_for(Some(drone_id))
    }

    /// Restoring changes only lifecycle eligibility. A future agent
    /// registration must establish a new binding and communication link.
    pub(crate) fn restore_drone(&self, drone_id: &str) -> Result<OperationsSnapshot, String> {
        if drone_id.trim().is_empty() {
            return Err("drone id is required".to_string());
        }
        let now = unix_time_ms();
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin aircraft restore: {error}"))?;
        let status = tx
            .query_row(
                "SELECT status FROM drones WHERE id = ?1",
                [drone_id],
                |row| row.get::<_, String>(0),
            )
            .optional()
            .map_err(|error| format!("read aircraft lifecycle: {error}"))?
            .ok_or_else(|| format!("aircraft {drone_id} is not registered"))?;

        if status != "archived" {
            drop(tx);
            drop(connection);
            return self.operations_snapshot_for(Some(drone_id));
        }

        tx.execute(
            "UPDATE drones SET status = 'active', updated_at_unix_ms = ?2 WHERE id = ?1",
            params![drone_id, now],
        )
        .map_err(|error| format!("restore aircraft: {error}"))?;
        let event_id = generate_id(&tx)?;
        tx.execute(
            r#"
            INSERT INTO drone_lifecycle_events (
                id, drone_id, event_type, reason, occurred_at_unix_ms
            ) VALUES (?1, ?2, 'restored', 'operator restored aircraft', ?3)
            "#,
            params![event_id, drone_id, now],
        )
        .map_err(|error| format!("record aircraft restore: {error}"))?;
        tx.commit()
            .map_err(|error| format!("commit aircraft restore: {error}"))?;
        drop(connection);
        self.operations_snapshot_for(Some(drone_id))
    }
}

fn generate_id(tx: &rusqlite::Transaction<'_>) -> Result<String, String> {
    tx.query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate aircraft lifecycle event id: {error}"))
}
