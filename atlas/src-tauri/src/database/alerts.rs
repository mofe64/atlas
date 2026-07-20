use rusqlite::{params, OptionalExtension, Transaction};
use serde::Serialize;
use serde_json::{json, Value};

use super::{
    unix_time_ms, LocalDatabase, MissionActionUpdateInput, MissionRunUpdateInput, TelemetryInput,
};

pub(super) const RESOLVED_ALERT_RETENTION_MS: i64 = 30 * 24 * 60 * 60 * 1_000;
const TELEMETRY_STALE_AFTER_MS: i64 = 5_000;
const TELEMETRY_LOST_AFTER_MS: i64 = 15_000;
const BATTERY_ALERT_THRESHOLD_PERCENT: f64 = 20.0;
const BATTERY_CRITICAL_THRESHOLD_PERCENT: f64 = 10.0;
const BATTERY_RECOVERY_THRESHOLD_PERCENT: f64 = 22.0;

#[derive(Debug, Clone)]
pub(crate) struct AlertObservation {
    pub dedupe_key: String,
    pub alert_type: String,
    pub severity: String,
    pub source: String,
    pub drone_id: Option<String>,
    pub incident_id: Option<String>,
    pub mission_run_id: Option<String>,
    pub title: String,
    pub recommended_action: String,
    pub evidence: Value,
    pub observed_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct OperationalAlertSnapshot {
    pub id: String,
    pub dedupe_key: String,
    pub alert_type: String,
    pub severity: String,
    pub source: String,
    pub state: String,
    pub drone_id: Option<String>,
    pub incident_id: Option<String>,
    pub mission_run_id: Option<String>,
    pub title: String,
    pub recommended_action: String,
    pub evidence: Value,
    pub first_seen_at_unix_ms: i64,
    pub last_seen_at_unix_ms: i64,
    pub observation_count: u64,
    pub acknowledged_at_unix_ms: Option<i64>,
    pub acknowledged_by: Option<String>,
    pub resolved_at_unix_ms: Option<i64>,
    pub resolution_reason: Option<String>,
    pub expired_at_unix_ms: Option<i64>,
    pub events: Vec<OperationalAlertEventSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct OperationalAlertEventSnapshot {
    pub id: String,
    pub sequence: u32,
    pub event_type: String,
    pub state: String,
    pub actor: String,
    pub message: String,
    pub evidence: Value,
    pub occurred_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct OperationalAlertListSnapshot {
    pub generated_at_unix_ms: i64,
    pub active_count: usize,
    pub unacknowledged_count: usize,
    pub alerts: Vec<OperationalAlertSnapshot>,
}

impl LocalDatabase {
    pub(crate) fn observe_operational_alert(
        &self,
        observation: &AlertObservation,
    ) -> Result<OperationalAlertSnapshot, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin operational alert observation: {error}"))?;
        let id = observe_alert(&tx, observation)?;
        tx.commit()
            .map_err(|error| format!("commit operational alert observation: {error}"))?;
        drop(connection);
        self.operational_alert(&id)
    }

    pub(crate) fn resolve_operational_alert(
        &self,
        dedupe_key: &str,
        reason: &str,
        evidence: &Value,
        resolved_at_unix_ms: i64,
    ) -> Result<bool, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin operational alert resolution: {error}"))?;
        let resolved = resolve_alert(&tx, dedupe_key, reason, evidence, resolved_at_unix_ms)?;
        tx.commit()
            .map_err(|error| format!("commit operational alert resolution: {error}"))?;
        Ok(resolved)
    }

    pub(crate) fn resolve_operational_alerts_by_type(
        &self,
        alert_type: &str,
        reason: &str,
        evidence: &Value,
        resolved_at_unix_ms: i64,
    ) -> Result<usize, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin operational alert type resolution: {error}"))?;
        let keys = unresolved_keys(&tx, "alert_type = ?1", alert_type)?;
        for key in &keys {
            resolve_alert(&tx, key, reason, evidence, resolved_at_unix_ms)?;
        }
        tx.commit()
            .map_err(|error| format!("commit operational alert type resolution: {error}"))?;
        Ok(keys.len())
    }

    pub(crate) fn acknowledge_operational_alert(
        &self,
        alert_id: &str,
        acknowledged_at_unix_ms: i64,
    ) -> Result<OperationalAlertSnapshot, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin operational alert acknowledgement: {error}"))?;
        let state: String = tx
            .query_row(
                "SELECT state FROM operational_alerts WHERE id = ?1",
                [alert_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("read operational alert before acknowledgement: {error}"))?
            .ok_or_else(|| format!("operational alert {alert_id} was not found"))?;
        match state.as_str() {
            "ACTIVE" => {
                tx.execute(
                    "UPDATE operational_alerts SET state = 'ACKNOWLEDGED', acknowledged_at_unix_ms = ?2, acknowledged_by = 'local_operator', updated_at_unix_ms = ?2 WHERE id = ?1 AND state = 'ACTIVE'",
                    params![alert_id, acknowledged_at_unix_ms],
                )
                .map_err(|error| format!("acknowledge operational alert: {error}"))?;
                insert_alert_event(
                    &tx,
                    alert_id,
                    "ACKNOWLEDGED",
                    "ACKNOWLEDGED",
                    "local_operator",
                    "Operator acknowledged the alert; the underlying condition remains active",
                    &json!({ "conditionResolved": false }),
                    acknowledged_at_unix_ms,
                )?;
            }
            "ACKNOWLEDGED" => {}
            "RESOLVED" | "EXPIRED" => {
                return Err(format!(
                    "operational alert is {state}; acknowledgement is only valid while the condition is active"
                ));
            }
            _ => return Err(format!("operational alert has unsupported state {state}")),
        }
        tx.commit()
            .map_err(|error| format!("commit operational alert acknowledgement: {error}"))?;
        drop(connection);
        self.operational_alert(alert_id)
    }

    pub(crate) fn operational_alerts(
        &self,
        include_history: bool,
        limit: usize,
    ) -> Result<OperationalAlertListSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare(
                r#"
                SELECT id
                FROM operational_alerts
                WHERE ?1 OR state IN ('ACTIVE', 'ACKNOWLEDGED')
                ORDER BY
                    CASE state WHEN 'ACTIVE' THEN 0 WHEN 'ACKNOWLEDGED' THEN 1
                               WHEN 'RESOLVED' THEN 2 ELSE 3 END,
                    CASE severity WHEN 'CRITICAL' THEN 0 WHEN 'WARNING' THEN 1 ELSE 2 END,
                    last_seen_at_unix_ms DESC,
                    id DESC
                LIMIT ?2
                "#,
            )
            .map_err(|error| format!("prepare operational alert list: {error}"))?;
        let ids = statement
            .query_map(
                params![include_history, limit.clamp(1, 500) as i64],
                |row| row.get::<_, String>(0),
            )
            .map_err(|error| format!("query operational alert list: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read operational alert ids: {error}"))?;
        drop(statement);
        let alerts = ids
            .iter()
            .map(|id| read_alert(&connection, id))
            .collect::<Result<Vec<_>, _>>()?;
        let (active_count, unacknowledged_count): (i64, i64) = connection
            .query_row(
                r#"
                SELECT count(*),
                       COALESCE(sum(CASE WHEN state = 'ACTIVE' THEN 1 ELSE 0 END), 0)
                FROM operational_alerts
                WHERE state IN ('ACTIVE', 'ACKNOWLEDGED')
                "#,
                [],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .map_err(|error| format!("count current operational alerts: {error}"))?;
        Ok(OperationalAlertListSnapshot {
            generated_at_unix_ms: unix_time_ms(),
            active_count: active_count.max(0) as usize,
            unacknowledged_count: unacknowledged_count.max(0) as usize,
            alerts,
        })
    }

    pub(crate) fn operational_alert(
        &self,
        alert_id: &str,
    ) -> Result<OperationalAlertSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        read_alert(&connection, alert_id)
    }

    pub(crate) fn refresh_time_based_alerts(&self, now: i64) -> Result<(), String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin time-based alert refresh: {error}"))?;
        let aircraft = {
            let mut statement = tx
                .prepare(
                    r#"
                    SELECT d.id,
                           (SELECT l.status FROM communication_links l
                            JOIN vehicle_agent_bindings b ON b.id = l.vehicle_agent_binding_id
                            WHERE b.drone_id = d.id
                            ORDER BY l.started_at_unix_ms DESC, l.rowid DESC LIMIT 1),
                           (SELECT l.started_at_unix_ms FROM communication_links l
                            JOIN vehicle_agent_bindings b ON b.id = l.vehicle_agent_binding_id
                            WHERE b.drone_id = d.id
                            ORDER BY l.started_at_unix_ms DESC, l.rowid DESC LIMIT 1),
                           t.received_at_unix_ms
                    FROM drones d
                    LEFT JOIN vehicle_telemetry_current t ON t.drone_id = d.id
                    WHERE d.status = 'active'
                    "#,
                )
                .map_err(|error| format!("prepare time-based aircraft alerts: {error}"))?;
            let rows = statement
                .query_map([], |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        row.get::<_, Option<String>>(1)?,
                        row.get::<_, Option<i64>>(2)?,
                        row.get::<_, Option<i64>>(3)?,
                    ))
                })
                .map_err(|error| format!("query time-based aircraft alerts: {error}"))?
                .collect::<Result<Vec<_>, _>>()
                .map_err(|error| format!("read time-based aircraft alerts: {error}"))?;
            rows
        };
        for (drone_id, link_status, link_started_at, telemetry_received_at) in aircraft {
            let agent_key = format!("agent_disconnected:{drone_id}");
            if link_started_at.is_some() && link_status.as_deref() == Some("disconnected") {
                observe_alert(
                    &tx,
                    &AlertObservation {
                        dedupe_key: agent_key,
                        alert_type: "AGENT_DISCONNECTED".into(),
                        severity: "CRITICAL".into(),
                        source: "agent".into(),
                        drone_id: Some(drone_id.clone()),
                        incident_id: None,
                        mission_run_id: None,
                        title: "Atlas Agent disconnected".into(),
                        recommended_action: "Restore the Agent link before issuing aircraft commands; use the aircraft's current autonomous safety mode meanwhile.".into(),
                        evidence: json!({ "linkStatus": link_status }),
                        observed_at_unix_ms: now,
                    },
                )?;
            } else if link_status.as_deref() == Some("connected") {
                resolve_alert(
                    &tx,
                    &agent_key,
                    "Atlas Agent registered and the communication link recovered",
                    &json!({ "linkStatus": "connected" }),
                    now,
                )?;
            }

            let telemetry_key = format!("telemetry_unavailable:{drone_id}");
            let telemetry_age = telemetry_received_at.map(|received| now.saturating_sub(received));
            let waiting_for_initial = telemetry_received_at.is_none()
                && link_started_at
                    .is_some_and(|started| now.saturating_sub(started) > TELEMETRY_STALE_AFTER_MS);
            if waiting_for_initial
                || telemetry_age.is_some_and(|age| age > TELEMETRY_STALE_AFTER_MS)
            {
                let lost = waiting_for_initial
                    || telemetry_age.is_some_and(|age| age > TELEMETRY_LOST_AFTER_MS)
                    || link_status.as_deref() == Some("disconnected");
                observe_alert(
                    &tx,
                    &AlertObservation {
                        dedupe_key: telemetry_key,
                        alert_type: "TELEMETRY_UNAVAILABLE".into(),
                        severity: if lost { "CRITICAL" } else { "WARNING" }.into(),
                        source: "telemetry".into(),
                        drone_id: Some(drone_id.clone()),
                        incident_id: None,
                        mission_run_id: None,
                        title: if lost { "Aircraft telemetry lost" } else { "Aircraft telemetry stale" }.into(),
                        recommended_action: "Confirm the Agent and MAVSDK links, avoid relying on old flight data, and recover command/telemetry connectivity.".into(),
                        evidence: json!({
                            "lastTelemetryAtUnixMs": telemetry_received_at,
                            "ageMs": telemetry_age,
                            "condition": if lost { "lost" } else { "stale" },
                        }),
                        observed_at_unix_ms: now,
                    },
                )?;
            } else if telemetry_received_at.is_some() {
                resolve_alert(
                    &tx,
                    &telemetry_key,
                    "Fresh aircraft telemetry was received",
                    &json!({ "lastTelemetryAtUnixMs": telemetry_received_at }),
                    now,
                )?;
            }
        }
        expire_resolved_alerts(&tx, now.saturating_sub(RESOLVED_ALERT_RETENTION_MS), now)?;
        tx.commit()
            .map_err(|error| format!("commit time-based alert refresh: {error}"))
    }

    #[cfg(test)]
    pub(crate) fn expire_resolved_operational_alerts(
        &self,
        resolved_before_unix_ms: i64,
        expired_at_unix_ms: i64,
    ) -> Result<usize, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin operational alert expiry: {error}"))?;
        let expired = expire_resolved_alerts(&tx, resolved_before_unix_ms, expired_at_unix_ms)?;
        tx.commit()
            .map_err(|error| format!("commit operational alert expiry: {error}"))?;
        Ok(expired)
    }
}

pub(super) fn reconcile_telemetry_alerts(
    tx: &Transaction<'_>,
    drone_id: &str,
    input: &TelemetryInput,
) -> Result<(), String> {
    let now = input.received_at_unix_ms;
    resolve_alert(
        tx,
        &format!("telemetry_unavailable:{drone_id}"),
        "Fresh aircraft telemetry was received",
        &json!({ "receivedAtUnixMs": now, "source": input.source }),
        now,
    )?;

    let battery = input
        .batteries
        .iter()
        .filter_map(|battery| battery.remaining_percent)
        .chain(input.battery_percent)
        .filter(|value| value.is_finite() && (0.0..=100.0).contains(value))
        .reduce(f64::min);
    let battery_key = format!("battery_below_threshold:{drone_id}");
    if let Some(percent) = battery {
        if percent < BATTERY_ALERT_THRESHOLD_PERCENT {
            let critical = percent <= BATTERY_CRITICAL_THRESHOLD_PERCENT;
            observe_alert(
                tx,
                &AlertObservation {
                    dedupe_key: battery_key,
                    alert_type: "BATTERY_BELOW_THRESHOLD".into(),
                    severity: if critical { "CRITICAL" } else { "WARNING" }.into(),
                    source: "telemetry".into(),
                    drone_id: Some(drone_id.to_string()),
                    incident_id: None,
                    mission_run_id: None,
                    title: format!("Aircraft battery low ({percent:.0}%)"),
                    recommended_action: if critical {
                        "Prioritize immediate recovery using the reviewed flight contingency and monitor landing progress."
                    } else {
                        "Review mission endurance and prepare to return or land before the critical threshold."
                    }
                    .into(),
                    evidence: json!({
                        "remainingPercent": percent,
                        "alertThresholdPercent": BATTERY_ALERT_THRESHOLD_PERCENT,
                        "criticalThresholdPercent": BATTERY_CRITICAL_THRESHOLD_PERCENT,
                        "recoveryThresholdPercent": BATTERY_RECOVERY_THRESHOLD_PERCENT,
                    }),
                    observed_at_unix_ms: now,
                },
            )?;
        } else if percent >= BATTERY_RECOVERY_THRESHOLD_PERCENT {
            resolve_alert(
                tx,
                &battery_key,
                "Battery recovered above the alert hysteresis threshold",
                &json!({ "remainingPercent": percent }),
                now,
            )?;
        }
    }

    if let Some(health) = input.health.as_ref() {
        reconcile_position_alert(
            tx,
            drone_id,
            "global_position_unavailable",
            "GLOBAL_POSITION_UNAVAILABLE",
            "Global position unavailable",
            health.global_position_ok && input.latitude.is_some() && input.longitude.is_some(),
            input.in_air == Some(true),
            "Verify GPS/estimator health and do not start or continue position-dependent operations until global position recovers.",
            json!({
                "healthOk": health.global_position_ok,
                "latitudeAvailable": input.latitude.is_some(),
                "longitudeAvailable": input.longitude.is_some(),
            }),
            now,
        )?;
        reconcile_position_alert(
            tx,
            drone_id,
            "home_position_unavailable",
            "HOME_POSITION_UNAVAILABLE",
            "Home position unavailable",
            health.home_position_ok && input.home_position_set == Some(true),
            input.in_air == Some(true),
            "Confirm home-position establishment before relying on RTL or starting a response mission.",
            json!({
                "healthOk": health.home_position_ok,
                "homePositionSet": input.home_position_set,
            }),
            now,
        )?;
    }
    Ok(())
}

pub(super) fn observe_incident_revision_alerts(
    tx: &Transaction<'_>,
    incident_id: &str,
    current_revision: u32,
    changed_fields: &[&str],
    observed_at_unix_ms: i64,
) -> Result<(), String> {
    let assignments = {
        let mut statement = tx
            .prepare(
                r#"
                SELECT a.id, a.drone_id, a.mission_run_id, a.status,
                       m.id, m.params_json, r.status
                FROM incident_assignments a
                JOIN missions m ON m.id = a.mission_id
                LEFT JOIN mission_runs r ON r.id = a.mission_run_id
                WHERE a.incident_id = ?1
                "#,
            )
            .map_err(|error| format!("prepare incident revision alert assignments: {error}"))?;
        let rows = statement
            .query_map([incident_id], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, Option<String>>(2)?,
                    row.get::<_, String>(3)?,
                    row.get::<_, String>(4)?,
                    row.get::<_, String>(5)?,
                    row.get::<_, Option<String>>(6)?,
                ))
            })
            .map_err(|error| format!("query incident revision alert assignments: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read incident revision alert assignments: {error}"))?;
        rows
    };
    for (
        assignment_id,
        drone_id,
        mission_run_id,
        assignment_state,
        mission_id,
        params_json,
        run_state,
    ) in assignments
    {
        let params: Value = serde_json::from_str(&params_json)
            .map_err(|error| format!("decode incident response revision evidence: {error}"))?;
        let planned_revision = params
            .get("incidentResponse")
            .and_then(|response| response.get("incidentRevision"))
            .and_then(Value::as_u64)
            .and_then(|value| u32::try_from(value).ok());
        if planned_revision.is_none_or(|planned| planned >= current_revision) {
            continue;
        }
        let active_run = run_state
            .as_deref()
            .is_some_and(|state| !matches!(state, "COMPLETED" | "FAILED" | "CANCELLED" | "RTL"));
        observe_alert(
            tx,
            &AlertObservation {
                dedupe_key: format!("incident_revision_changed:{assignment_id}"),
                alert_type: "INCIDENT_REVISION_CHANGED".into(),
                severity: if active_run { "CRITICAL" } else { "WARNING" }.into(),
                source: "incident".into(),
                drone_id: Some(drone_id),
                incident_id: Some(incident_id.to_string()),
                mission_run_id,
                title: "Incident changed after response planning".into(),
                recommended_action: if active_run {
                    "Review the changed incident against the active response immediately; cancel or continue only through an explicit operator decision."
                } else {
                    "Review the incident changes and prepare a new response plan before deployment."
                }
                .into(),
                evidence: json!({
                    "assignmentId": assignment_id,
                    "missionId": mission_id,
                    "assignmentState": assignment_state,
                    "runState": run_state,
                    "plannedIncidentRevision": planned_revision,
                    "currentIncidentRevision": current_revision,
                    "changedFields": changed_fields,
                }),
                observed_at_unix_ms,
            },
        )?;
    }
    Ok(())
}

pub(super) fn resolve_incident_revision_alerts(
    tx: &Transaction<'_>,
    incident_id: &str,
    reason: &str,
    resolved_at_unix_ms: i64,
) -> Result<usize, String> {
    let keys = unresolved_keys(
        tx,
        "alert_type = 'INCIDENT_REVISION_CHANGED' AND incident_id = ?1",
        incident_id,
    )?;
    for key in &keys {
        resolve_alert(
            tx,
            key,
            reason,
            &json!({ "incidentId": incident_id }),
            resolved_at_unix_ms,
        )?;
    }
    Ok(keys.len())
}

pub(super) fn reconcile_mission_action_alert(
    tx: &Transaction<'_>,
    execution_id: &str,
    input: &MissionActionUpdateInput,
) -> Result<(), String> {
    let association: (String, Option<String>) = tx
        .query_row(
            r#"
            SELECT r.drone_id, a.incident_id
            FROM mission_runs r
            LEFT JOIN incident_assignments a ON a.mission_run_id = r.id
            WHERE r.id = ?1
            "#,
            [&input.mission_run_id],
            |row| Ok((row.get(0)?, row.get(1)?)),
        )
        .map_err(|error| format!("read mission action alert association: {error}"))?;
    let dedupe_key = format!("arrival_action_attention:{execution_id}");
    match input.state.as_str() {
        "RETRYING" => {
            observe_alert(
                tx,
                &AlertObservation {
                    dedupe_key,
                    alert_type: "ARRIVAL_ACTION_RETRYING".into(),
                    severity: "WARNING".into(),
                    source: "mission_action".into(),
                    drone_id: Some(association.0),
                    incident_id: association.1,
                    mission_run_id: Some(input.mission_run_id.clone()),
                    title: format!("{} is retrying", action_label(&input.action_type)),
                    recommended_action: "Monitor the next reviewed attempt and be ready to apply the plan's explicit failure policy.".into(),
                    evidence: json!({
                        "actionSequence": input.action_sequence,
                        "actionType": input.action_type,
                        "attempt": input.attempt,
                        "failurePolicy": input.failure_policy,
                        "errorCode": input.error_code,
                        "message": input.message,
                    }),
                    observed_at_unix_ms: input.occurred_at_unix_ms,
                },
            )?;
        }
        "FAILED" => {
            observe_alert(
                tx,
                &AlertObservation {
                    dedupe_key,
                    alert_type: "ARRIVAL_ACTION_FAILED".into(),
                    severity: "CRITICAL".into(),
                    source: "mission_action".into(),
                    drone_id: Some(association.0),
                    incident_id: association.1,
                    mission_run_id: Some(input.mission_run_id.clone()),
                    title: format!("{} failed", action_label(&input.action_type)),
                    recommended_action: "Review the failure evidence and confirm that the reviewed contingency is applied or take explicit operator control.".into(),
                    evidence: json!({
                        "actionSequence": input.action_sequence,
                        "actionType": input.action_type,
                        "attempt": input.attempt,
                        "failurePolicy": input.failure_policy,
                        "errorCode": input.error_code,
                        "message": input.message,
                    }),
                    observed_at_unix_ms: input.occurred_at_unix_ms,
                },
            )?;
        }
        "POLICY_APPLIED" if input.failure_policy == "OPERATOR_INTERVENTION" => {
            observe_alert(
                tx,
                &AlertObservation {
                    dedupe_key,
                    alert_type: "ARRIVAL_ACTION_AWAITING_INTERVENTION".into(),
                    severity: "CRITICAL".into(),
                    source: "mission_action".into(),
                    drone_id: Some(association.0),
                    incident_id: association.1,
                    mission_run_id: Some(input.mission_run_id.clone()),
                    title: format!(
                        "{} requires operator intervention",
                        action_label(&input.action_type)
                    ),
                    recommended_action: "Take an explicit reviewed action for the aircraft; acknowledging this alert does not release the intervention requirement.".into(),
                    evidence: json!({
                        "actionSequence": input.action_sequence,
                        "actionType": input.action_type,
                        "attempt": input.attempt,
                        "failurePolicy": input.failure_policy,
                        "errorCode": input.error_code,
                        "message": input.message,
                    }),
                    observed_at_unix_ms: input.occurred_at_unix_ms,
                },
            )?;
        }
        "SUCCEEDED" | "POLICY_APPLIED" => {
            resolve_alert(
                tx,
                &dedupe_key,
                if input.state == "SUCCEEDED" {
                    "The arrival action was acknowledged successfully"
                } else {
                    "The reviewed automatic or optional-action policy was applied"
                },
                &json!({
                    "actionSequence": input.action_sequence,
                    "actionType": input.action_type,
                    "attempt": input.attempt,
                    "failurePolicy": input.failure_policy,
                    "state": input.state,
                }),
                input.occurred_at_unix_ms,
            )?;
        }
        _ => {}
    }
    Ok(())
}

pub(super) fn reconcile_mission_run_alerts(
    tx: &Transaction<'_>,
    input: &MissionRunUpdateInput,
    terminal: bool,
) -> Result<(), String> {
    let translation_key = format!("mission_translation_warning:{}", input.mission_run_id);
    if input.event_type == "uploaded" {
        let warnings = input
            .evidence_json
            .as_deref()
            .and_then(|evidence| serde_json::from_str::<Value>(evidence).ok())
            .and_then(|evidence| evidence.get("translationWarnings").cloned())
            .and_then(|warnings| warnings.as_array().cloned())
            .unwrap_or_default();
        if warnings.is_empty() {
            resolve_alert(
                tx,
                &translation_key,
                "Mission translation completed without warnings",
                &json!({ "translationWarnings": [] }),
                input.occurred_at_unix_ms,
            )?;
        } else {
            let association: (String, Option<String>) = tx
                .query_row(
                    r#"
                    SELECT r.drone_id, a.incident_id
                    FROM mission_runs r
                    LEFT JOIN incident_assignments a ON a.mission_run_id = r.id
                    WHERE r.id = ?1
                    "#,
                    [&input.mission_run_id],
                    |row| Ok((row.get(0)?, row.get(1)?)),
                )
                .map_err(|error| format!("read mission translation alert association: {error}"))?;
            observe_alert(
                tx,
                &AlertObservation {
                    dedupe_key: translation_key.clone(),
                    alert_type: "MISSION_TRANSLATION_WARNING".into(),
                    severity: "WARNING".into(),
                    source: "mission_translation".into(),
                    drone_id: Some(association.0),
                    incident_id: association.1,
                    mission_run_id: Some(input.mission_run_id.clone()),
                    title: "Mission translation produced warnings".into(),
                    recommended_action: "Review the translation warnings and confirm the resulting PX4 and payload behavior before starting the mission.".into(),
                    evidence: json!({ "translationWarnings": warnings }),
                    observed_at_unix_ms: input.occurred_at_unix_ms,
                },
            )?;
        }
    }
    if terminal {
        resolve_alert(
            tx,
            &translation_key,
            "The mission run reached a terminal state",
            &json!({ "runState": input.run_state }),
            input.occurred_at_unix_ms,
        )?;
        let keys = unresolved_keys(
            tx,
            "source = 'mission_action' AND mission_run_id = ?1",
            &input.mission_run_id,
        )?;
        for key in keys {
            resolve_alert(
                tx,
                &key,
                "The mission run reached a terminal state and no longer awaits arrival-action handling",
                &json!({ "runState": input.run_state }),
                input.occurred_at_unix_ms,
            )?;
        }
    }
    Ok(())
}

fn action_label(action_type: &str) -> &'static str {
    match action_type {
        "HOLD_AT_ARRIVAL" => "Arrival Hold",
        "POINT_GIMBAL_AT_INCIDENT" => "Incident gimbal action",
        "RESUME_AFTER_ARRIVAL" => "Post-arrival mission resume",
        _ => "Mission action",
    }
}

fn unresolved_keys(
    tx: &Transaction<'_>,
    predicate: &str,
    association_id: &str,
) -> Result<Vec<String>, String> {
    let sql = format!(
        "SELECT dedupe_key FROM operational_alerts WHERE state IN ('ACTIVE', 'ACKNOWLEDGED') AND {predicate}"
    );
    let mut statement = tx
        .prepare(&sql)
        .map_err(|error| format!("prepare associated operational alerts: {error}"))?;
    let keys = statement
        .query_map([association_id], |row| row.get::<_, String>(0))
        .map_err(|error| format!("query associated operational alerts: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read associated operational alerts: {error}"))?;
    Ok(keys)
}

#[allow(clippy::too_many_arguments)]
fn reconcile_position_alert(
    tx: &Transaction<'_>,
    drone_id: &str,
    key_prefix: &str,
    alert_type: &str,
    title: &str,
    available: bool,
    in_air: bool,
    recommended_action: &str,
    evidence: Value,
    now: i64,
) -> Result<(), String> {
    let dedupe_key = format!("{key_prefix}:{drone_id}");
    if available {
        resolve_alert(
            tx,
            &dedupe_key,
            &format!("{} recovered", title.to_ascii_lowercase()),
            &evidence,
            now,
        )?;
    } else {
        observe_alert(
            tx,
            &AlertObservation {
                dedupe_key,
                alert_type: alert_type.into(),
                severity: if in_air { "CRITICAL" } else { "WARNING" }.into(),
                source: "telemetry".into(),
                drone_id: Some(drone_id.to_string()),
                incident_id: None,
                mission_run_id: None,
                title: title.into(),
                recommended_action: recommended_action.into(),
                evidence,
                observed_at_unix_ms: now,
            },
        )?;
    }
    Ok(())
}

pub(super) fn observe_alert(
    tx: &Transaction<'_>,
    observation: &AlertObservation,
) -> Result<String, String> {
    validate_observation(observation)?;
    let evidence_json = serde_json::to_string(&observation.evidence)
        .map_err(|error| format!("encode operational alert evidence: {error}"))?;
    let existing: Option<(String, String, String)> = tx
        .query_row(
            "SELECT id, severity, state FROM operational_alerts WHERE dedupe_key = ?1 AND state IN ('ACTIVE', 'ACKNOWLEDGED')",
            [&observation.dedupe_key],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
        )
        .optional()
        .map_err(|error| format!("find unresolved operational alert: {error}"))?;
    if let Some((id, previous_severity, state)) = existing {
        let severity_increased =
            severity_rank(&observation.severity) > severity_rank(&previous_severity);
        let requires_new_acknowledgement = state == "ACKNOWLEDGED" && severity_increased;
        let next_state = if requires_new_acknowledgement {
            "ACTIVE"
        } else {
            state.as_str()
        };
        tx.execute(
            r#"
            UPDATE operational_alerts
            SET alert_type = ?2,
                severity = ?3,
                source = ?4,
                drone_id = COALESCE(?5, drone_id),
                incident_id = COALESCE(?6, incident_id),
                mission_run_id = COALESCE(?7, mission_run_id),
                title = ?8,
                recommended_action = ?9,
                evidence_json = ?10,
                last_seen_at_unix_ms = max(last_seen_at_unix_ms, ?11),
                observation_count = observation_count + 1,
                updated_at_unix_ms = max(updated_at_unix_ms, ?11),
                state = CASE WHEN ?12 THEN 'ACTIVE' ELSE state END,
                acknowledged_at_unix_ms = CASE WHEN ?12 THEN NULL ELSE acknowledged_at_unix_ms END,
                acknowledged_by = CASE WHEN ?12 THEN NULL ELSE acknowledged_by END
            WHERE id = ?1
            "#,
            params![
                id,
                observation.alert_type,
                observation.severity,
                observation.source,
                observation.drone_id,
                observation.incident_id,
                observation.mission_run_id,
                observation.title,
                observation.recommended_action,
                evidence_json,
                observation.observed_at_unix_ms,
                requires_new_acknowledgement,
            ],
        )
        .map_err(|error| format!("update duplicate operational alert: {error}"))?;
        if previous_severity != observation.severity {
            let message = if requires_new_acknowledgement {
                format!(
                    "Alert severity increased from {previous_severity} to {}; operator acknowledgement is required again",
                    observation.severity
                )
            } else {
                format!(
                    "Alert severity changed from {previous_severity} to {}",
                    observation.severity
                )
            };
            insert_alert_event(
                tx,
                &id,
                "SEVERITY_CHANGED",
                next_state,
                &observation.source,
                &message,
                &observation.evidence,
                observation.observed_at_unix_ms,
            )?;
        }
        return Ok(id);
    }

    let id = generate_id(tx)?;
    tx.execute(
        r#"
        INSERT INTO operational_alerts (
            id, dedupe_key, alert_type, severity, source, state,
            drone_id, incident_id, mission_run_id, title, recommended_action,
            evidence_json, first_seen_at_unix_ms, last_seen_at_unix_ms,
            observation_count, created_at_unix_ms, updated_at_unix_ms
        ) VALUES (?1, ?2, ?3, ?4, ?5, 'ACTIVE', ?6, ?7, ?8, ?9, ?10,
                  ?11, ?12, ?12, 1, ?12, ?12)
        "#,
        params![
            id,
            observation.dedupe_key,
            observation.alert_type,
            observation.severity,
            observation.source,
            observation.drone_id,
            observation.incident_id,
            observation.mission_run_id,
            observation.title,
            observation.recommended_action,
            evidence_json,
            observation.observed_at_unix_ms,
        ],
    )
    .map_err(|error| format!("insert operational alert: {error}"))?;
    insert_alert_event(
        tx,
        &id,
        "ACTIVATED",
        "ACTIVE",
        &observation.source,
        "Operational condition activated this alert",
        &observation.evidence,
        observation.observed_at_unix_ms,
    )?;
    Ok(id)
}

pub(super) fn resolve_alert(
    tx: &Transaction<'_>,
    dedupe_key: &str,
    reason: &str,
    evidence: &Value,
    resolved_at_unix_ms: i64,
) -> Result<bool, String> {
    let alert_id: Option<String> = tx
        .query_row(
            "SELECT id FROM operational_alerts WHERE dedupe_key = ?1 AND state IN ('ACTIVE', 'ACKNOWLEDGED')",
            [dedupe_key],
            |row| row.get(0),
        )
        .optional()
        .map_err(|error| format!("find operational alert to resolve: {error}"))?;
    let Some(alert_id) = alert_id else {
        return Ok(false);
    };
    let evidence_json = serde_json::to_string(evidence)
        .map_err(|error| format!("encode operational alert resolution evidence: {error}"))?;
    tx.execute(
        "UPDATE operational_alerts SET state = 'RESOLVED', evidence_json = ?2, resolved_at_unix_ms = ?3, resolution_reason = ?4, updated_at_unix_ms = ?3 WHERE id = ?1 AND state IN ('ACTIVE', 'ACKNOWLEDGED')",
        params![alert_id, evidence_json, resolved_at_unix_ms, reason],
    )
    .map_err(|error| format!("resolve operational alert: {error}"))?;
    insert_alert_event(
        tx,
        &alert_id,
        "RESOLVED",
        "RESOLVED",
        "atlas_native",
        reason,
        evidence,
        resolved_at_unix_ms,
    )?;
    Ok(true)
}

fn expire_resolved_alerts(
    tx: &Transaction<'_>,
    resolved_before_unix_ms: i64,
    expired_at_unix_ms: i64,
) -> Result<usize, String> {
    let ids = {
        let mut statement = tx
            .prepare(
                "SELECT id FROM operational_alerts WHERE state = 'RESOLVED' AND resolved_at_unix_ms <= ?1",
            )
            .map_err(|error| format!("prepare resolved operational alerts for expiry: {error}"))?;
        let rows = statement
            .query_map([resolved_before_unix_ms], |row| row.get::<_, String>(0))
            .map_err(|error| format!("query resolved operational alerts for expiry: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read resolved operational alerts for expiry: {error}"))?;
        rows
    };
    for id in &ids {
        tx.execute(
            "UPDATE operational_alerts SET state = 'EXPIRED', expired_at_unix_ms = ?2, updated_at_unix_ms = ?2 WHERE id = ?1 AND state = 'RESOLVED'",
            params![id, expired_at_unix_ms],
        )
        .map_err(|error| format!("expire operational alert: {error}"))?;
        insert_alert_event(
            tx,
            id,
            "EXPIRED",
            "EXPIRED",
            "atlas_native",
            "Resolved alert aged out of the active operational history window",
            &json!({ "resolvedBeforeUnixMs": resolved_before_unix_ms }),
            expired_at_unix_ms,
        )?;
    }
    Ok(ids.len())
}

#[allow(clippy::too_many_arguments)]
fn insert_alert_event(
    tx: &Transaction<'_>,
    alert_id: &str,
    event_type: &str,
    state: &str,
    actor: &str,
    message: &str,
    evidence: &Value,
    occurred_at_unix_ms: i64,
) -> Result<(), String> {
    let sequence: u32 = tx
        .query_row(
            "SELECT COALESCE(max(sequence), -1) + 1 FROM operational_alert_events WHERE operational_alert_id = ?1",
            [alert_id],
            |row| row.get(0),
        )
        .map_err(|error| format!("select next operational alert event sequence: {error}"))?;
    let evidence_json = serde_json::to_string(evidence)
        .map_err(|error| format!("encode operational alert event evidence: {error}"))?;
    tx.execute(
        "INSERT INTO operational_alert_events (id, operational_alert_id, sequence, event_type, state, actor, message, evidence_json, occurred_at_unix_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)",
        params![generate_id(tx)?, alert_id, sequence, event_type, state, actor, message, evidence_json, occurred_at_unix_ms],
    )
    .map_err(|error| format!("insert operational alert event: {error}"))?;
    Ok(())
}

fn read_alert(
    connection: &rusqlite::Connection,
    alert_id: &str,
) -> Result<OperationalAlertSnapshot, String> {
    let mut alert = connection
        .query_row(
            r#"
            SELECT id, dedupe_key, alert_type, severity, source, state,
                   drone_id, incident_id, mission_run_id, title, recommended_action,
                   evidence_json, first_seen_at_unix_ms, last_seen_at_unix_ms,
                   observation_count, acknowledged_at_unix_ms, acknowledged_by,
                   resolved_at_unix_ms, resolution_reason, expired_at_unix_ms
            FROM operational_alerts WHERE id = ?1
            "#,
            [alert_id],
            |row| {
                let evidence_json: String = row.get(11)?;
                Ok(OperationalAlertSnapshot {
                    id: row.get(0)?,
                    dedupe_key: row.get(1)?,
                    alert_type: row.get(2)?,
                    severity: row.get(3)?,
                    source: row.get(4)?,
                    state: row.get(5)?,
                    drone_id: row.get(6)?,
                    incident_id: row.get(7)?,
                    mission_run_id: row.get(8)?,
                    title: row.get(9)?,
                    recommended_action: row.get(10)?,
                    evidence: serde_json::from_str(&evidence_json).unwrap_or_else(|_| json!({})),
                    first_seen_at_unix_ms: row.get(12)?,
                    last_seen_at_unix_ms: row.get(13)?,
                    observation_count: row.get::<_, i64>(14)?.max(0) as u64,
                    acknowledged_at_unix_ms: row.get(15)?,
                    acknowledged_by: row.get(16)?,
                    resolved_at_unix_ms: row.get(17)?,
                    resolution_reason: row.get(18)?,
                    expired_at_unix_ms: row.get(19)?,
                    events: Vec::new(),
                })
            },
        )
        .optional()
        .map_err(|error| format!("read operational alert: {error}"))?
        .ok_or_else(|| format!("operational alert {alert_id} was not found"))?;
    let mut statement = connection
        .prepare(
            "SELECT id, sequence, event_type, state, actor, message, evidence_json, occurred_at_unix_ms FROM operational_alert_events WHERE operational_alert_id = ?1 ORDER BY sequence",
        )
        .map_err(|error| format!("prepare operational alert events: {error}"))?;
    alert.events = statement
        .query_map([alert_id], |row| {
            let evidence_json: String = row.get(6)?;
            Ok(OperationalAlertEventSnapshot {
                id: row.get(0)?,
                sequence: row.get(1)?,
                event_type: row.get(2)?,
                state: row.get(3)?,
                actor: row.get(4)?,
                message: row.get(5)?,
                evidence: serde_json::from_str(&evidence_json).unwrap_or_else(|_| json!({})),
                occurred_at_unix_ms: row.get(7)?,
            })
        })
        .map_err(|error| format!("query operational alert events: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read operational alert events: {error}"))?;
    Ok(alert)
}

fn validate_observation(observation: &AlertObservation) -> Result<(), String> {
    for (label, value, maximum) in [
        ("dedupeKey", observation.dedupe_key.as_str(), 500),
        ("alertType", observation.alert_type.as_str(), 120),
        ("source", observation.source.as_str(), 120),
        ("title", observation.title.as_str(), 500),
        (
            "recommendedAction",
            observation.recommended_action.as_str(),
            2_000,
        ),
    ] {
        if value.trim().is_empty() || value.chars().count() > maximum {
            return Err(format!(
                "operational alert {label} must contain 1 to {maximum} characters"
            ));
        }
    }
    if !matches!(
        observation.severity.as_str(),
        "INFO" | "WARNING" | "CRITICAL"
    ) {
        return Err("operational alert severity must be INFO, WARNING, or CRITICAL".into());
    }
    if observation.observed_at_unix_ms <= 0 {
        return Err("operational alert observedAtUnixMs must be greater than zero".into());
    }
    Ok(())
}

fn severity_rank(severity: &str) -> u8 {
    match severity {
        "CRITICAL" => 2,
        "WARNING" => 1,
        _ => 0,
    }
}

fn generate_id(tx: &Transaction<'_>) -> Result<String, String> {
    tx.query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate operational alert identifier: {error}"))
}
