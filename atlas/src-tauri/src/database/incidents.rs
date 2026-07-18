use rusqlite::{params, Connection, OptionalExtension, Transaction};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use super::{
    missions::create_ready_mission_in_transaction, unix_time_ms, CreateMissionInput, LocalDatabase,
    MissionPlanSnapshot, MissionSnapshot,
};

const MANUAL_SOURCE_TYPE: &str = "MANUAL";
const ATLAS_NATIVE_SOURCE: &str = "ATLAS_NATIVE";
const MAX_INCIDENT_TYPE_LENGTH: usize = 80;
const MAX_SUMMARY_LENGTH: usize = 200;
const MAX_DESCRIPTION_LENGTH: usize = 4_000;
const MAX_LOCATION_LABEL_LENGTH: usize = 500;

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct CreateIncidentInput {
    pub incident_type: String,
    pub priority: String,
    pub summary: String,
    #[serde(default)]
    pub description: String,
    pub latitude: Option<f64>,
    pub longitude: Option<f64>,
    #[serde(default)]
    pub address: String,
    #[serde(default)]
    pub area: String,
    pub occurred_at_unix_ms: Option<i64>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct UpdateIncidentInput {
    pub expected_revision: u32,
    pub incident_type: String,
    pub priority: String,
    pub status: String,
    pub summary: String,
    #[serde(default)]
    pub description: String,
    pub latitude: Option<f64>,
    pub longitude: Option<f64>,
    #[serde(default)]
    pub address: String,
    #[serde(default)]
    pub area: String,
    pub occurred_at_unix_ms: Option<i64>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct IncidentSnapshot {
    pub id: String,
    pub source_type: String,
    pub source_system: String,
    pub external_id: Option<String>,
    pub incident_type: String,
    pub priority: String,
    pub status: String,
    pub summary: String,
    pub description: String,
    pub latitude: Option<f64>,
    pub longitude: Option<f64>,
    pub address: String,
    pub area: String,
    pub occurred_at_unix_ms: Option<i64>,
    pub received_at_unix_ms: i64,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
    pub revision: u32,
    pub location_revision: u32,
    pub source_payload: Option<Value>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct IncidentEventSnapshot {
    pub id: String,
    pub incident_id: String,
    pub sequence: u32,
    pub event_type: String,
    pub state: String,
    pub source: String,
    pub message: String,
    pub details: Value,
    pub occurred_at_unix_ms: i64,
    pub received_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct IncidentDetailSnapshot {
    pub incident: IncidentSnapshot,
    pub events: Vec<IncidentEventSnapshot>,
    pub assignments: Vec<IncidentAssignmentSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct IncidentAssignmentSnapshot {
    pub id: String,
    pub incident_id: String,
    pub drone_id: String,
    pub drone_name: String,
    pub mission_id: Option<String>,
    pub mission_name: Option<String>,
    pub generated_plan_id: Option<String>,
    pub mission_run_id: Option<String>,
    pub operator_id: Option<String>,
    pub status: String,
    pub assigned_at_unix_ms: i64,
    pub on_scene_at_unix_ms: Option<i64>,
    pub ended_at_unix_ms: Option<i64>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct PrepareIncidentResponseInput {
    pub expected_incident_revision: u32,
    pub drone_id: String,
    pub staging_latitude: f64,
    pub staging_longitude: f64,
    pub altitude_meters: f64,
    pub speed_mps: f64,
    pub arrival_failure_policy: String,
    #[serde(default)]
    pub point_gimbal_at_incident: bool,
    pub incident_target_altitude_amsl_meters: Option<f64>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct PreparedIncidentResponse {
    pub incident: IncidentSnapshot,
    pub assignment: IncidentAssignmentSnapshot,
    pub mission: MissionSnapshot,
    pub plan: MissionPlanSnapshot,
}

#[derive(Debug, Clone, PartialEq)]
struct IncidentValues {
    incident_type: String,
    priority: String,
    status: String,
    summary: String,
    description: String,
    latitude: Option<f64>,
    longitude: Option<f64>,
    address: String,
    area: String,
    occurred_at_unix_ms: Option<i64>,
}

impl LocalDatabase {
    pub(crate) fn prepare_incident_response(
        &self,
        incident_id: &str,
        input: &PrepareIncidentResponseInput,
    ) -> Result<PreparedIncidentResponse, String> {
        if input.expected_incident_revision == 0 {
            return Err("expectedIncidentRevision must be at least 1".to_string());
        }
        let drone_id = input.drone_id.trim();
        if drone_id.is_empty() {
            return Err("droneId is required".to_string());
        }
        let failure_policy = input.arrival_failure_policy.trim().to_ascii_uppercase();
        if !matches!(
            failure_policy.as_str(),
            "RETURN_TO_LAUNCH" | "OPERATOR_INTERVENTION"
        ) {
            return Err(
                "arrivalFailurePolicy must be RETURN_TO_LAUNCH or OPERATOR_INTERVENTION"
                    .to_string(),
            );
        }
        let target_altitude = if input.point_gimbal_at_incident {
            let value = input.incident_target_altitude_amsl_meters.ok_or_else(|| {
                "incidentTargetAltitudeAmslMeters is required when pointGimbalAtIncident is enabled"
                    .to_string()
            })?;
            if !value.is_finite() || !(-500.0..=9_000.0).contains(&value) {
                return Err(
                    "incidentTargetAltitudeAmslMeters must be between -500 and 9000".to_string(),
                );
            }
            Some(value)
        } else {
            None
        };

        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin incident response preparation: {error}"))?;
        let incident = read_incident(&tx, incident_id)?;
        if incident.revision != input.expected_incident_revision {
            return Err(format!(
                "incident changed since response review began: expected revision {}, current revision {}; reload and retry",
                input.expected_incident_revision, incident.revision
            ));
        }
        if !matches!(incident.status.as_str(), "OPEN" | "ACTIVE") {
            return Err(format!(
                "incident response cannot be prepared while the incident is {}",
                incident.status
            ));
        }
        let (incident_latitude, incident_longitude) = incident
            .latitude
            .zip(incident.longitude)
            .ok_or_else(|| "incident response requires a reviewed incident location".to_string())?;

        let aircraft: Option<(String, String)> = tx
            .query_row(
                "SELECT name, status FROM drones WHERE id = ?1",
                [drone_id],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .optional()
            .map_err(|error| format!("validate incident response aircraft: {error}"))?;
        let (drone_name, drone_status) =
            aircraft.ok_or_else(|| format!("aircraft {drone_id} is not registered"))?;
        if drone_status != "active" {
            return Err(format!(
                "aircraft {drone_id} is not operationally active; current lifecycle is {drone_status}"
            ));
        }
        let unfinished_run: bool = tx
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM mission_runs WHERE drone_id = ?1 AND completed_at_unix_ms IS NULL)",
                [drone_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("check aircraft mission availability: {error}"))?;
        if unfinished_run {
            return Err("selected aircraft already has an unfinished mission run".to_string());
        }
        let active_assignment: Option<String> = tx
            .query_row(
                "SELECT incident_id FROM incident_assignments WHERE drone_id = ?1 AND ended_at_unix_ms IS NULL",
                [drone_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("check aircraft incident assignment: {error}"))?;
        if let Some(assigned_incident_id) = active_assignment {
            return Err(format!(
                "selected aircraft already has an active incident assignment for {assigned_incident_id}"
            ));
        }

        let now = unix_time_ms();
        let mut arrival_actions = vec![json!({
            "actionType": "HOLD_AT_ARRIVAL",
            "failurePolicy": failure_policy,
            "maxAttempts": 3,
        })];
        if let Some(altitude_amsl_meters) = target_altitude {
            arrival_actions.push(json!({
                "actionType": "POINT_GIMBAL_AT_INCIDENT",
                "failurePolicy": failure_policy,
                "maxAttempts": 3,
                "latitude": incident_latitude,
                "longitude": incident_longitude,
                "altitudeAmslMeters": altitude_amsl_meters,
            }));
        }
        let mission_input = CreateMissionInput {
            template_type: "WAYPOINT".to_string(),
            name: response_mission_name(&incident.summary),
            description: format!(
                "Operator-reviewed staging response for incident {} at revision {}.",
                incident.id, incident.revision
            ),
            selected_pattern: Some("DIRECT_WAYPOINTS".to_string()),
            params: json!({
                "waypoints": [{
                    "latitude": input.staging_latitude,
                    "longitude": input.staging_longitude,
                    "altitudeMeters": input.altitude_meters,
                    "speedMps": input.speed_mps,
                }],
                "defaultAltitudeMeters": input.altitude_meters,
                "defaultSpeedMps": input.speed_mps,
                "altitudeMode": "HOME_RELATIVE",
                "returnToLaunch": false,
                "arrivalActions": arrival_actions,
                "incidentResponse": {
                    "incidentId": incident.id,
                    "incidentRevision": incident.revision,
                    "locationRevision": incident.location_revision,
                    "incidentLatitude": incident_latitude,
                    "incidentLongitude": incident_longitude,
                    "stagingLatitude": input.staging_latitude,
                    "stagingLongitude": input.staging_longitude,
                    "altitudeMeters": input.altitude_meters,
                    "speedMps": input.speed_mps,
                    "arrivalFailurePolicy": failure_policy,
                    "pointGimbalAtIncident": input.point_gimbal_at_incident,
                    "incidentTargetAltitudeAmslMeters": target_altitude,
                    "reviewedAtUnixMs": now,
                },
            }),
        };
        let (mission, plan) = create_ready_mission_in_transaction(&tx, &mission_input, now)?;
        let assignment_id = generate_id(&tx)?;
        tx.execute(
            "INSERT INTO incident_assignments (id, incident_id, drone_id, mission_id, mission_run_id, operator_id, status, assigned_at_unix_ms, ended_at_unix_ms) VALUES (?1, ?2, ?3, ?4, NULL, NULL, 'PREPARED', ?5, NULL)",
            params![assignment_id, incident.id, drone_id, mission.id, now],
        )
        .map_err(|error| format!("insert incident response assignment: {error}"))?;
        insert_incident_event(
            &tx,
            &incident.id,
            "response_prepared",
            &incident.status,
            "Incident response plan prepared for operator confirmation",
            &json!({
                "assignmentId": assignment_id,
                "droneId": drone_id,
                "missionId": mission.id,
                "planId": plan.id,
                "incidentRevision": incident.revision,
                "locationRevision": incident.location_revision,
                "stagingLatitude": input.staging_latitude,
                "stagingLongitude": input.staging_longitude,
                "altitudeMeters": input.altitude_meters,
                "speedMps": input.speed_mps,
                "arrivalFailurePolicy": failure_policy,
                "pointGimbalAtIncident": input.point_gimbal_at_incident,
                "incidentTargetAltitudeAmslMeters": target_altitude,
            }),
            now,
        )?;
        let assignment = IncidentAssignmentSnapshot {
            id: assignment_id,
            incident_id: incident.id.clone(),
            drone_id: drone_id.to_string(),
            drone_name,
            mission_id: Some(mission.id.clone()),
            mission_name: Some(mission.name.clone()),
            generated_plan_id: mission.generated_plan_id.clone(),
            mission_run_id: None,
            operator_id: None,
            status: "PREPARED".to_string(),
            assigned_at_unix_ms: now,
            on_scene_at_unix_ms: None,
            ended_at_unix_ms: None,
        };
        tx.commit()
            .map_err(|error| format!("commit incident response preparation: {error}"))?;
        Ok(PreparedIncidentResponse {
            incident,
            assignment,
            mission,
            plan,
        })
    }

    pub(crate) fn create_incident(
        &self,
        input: &CreateIncidentInput,
    ) -> Result<IncidentDetailSnapshot, String> {
        let values = validate_incident_values(
            &input.incident_type,
            &input.priority,
            "OPEN",
            &input.summary,
            &input.description,
            input.latitude,
            input.longitude,
            &input.address,
            &input.area,
            input.occurred_at_unix_ms,
        )?;
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin incident creation: {error}"))?;
        let id = generate_id(&tx)?;
        let now = unix_time_ms();
        tx.execute(
            "INSERT INTO incidents (id, source_type, source_system, external_id, incident_type, priority, status, summary, description, latitude, longitude, address, area, occurred_at_unix_ms, received_at_unix_ms, created_at_unix_ms, updated_at_unix_ms, revision, location_revision, source_payload_json) VALUES (?1, ?2, ?3, NULL, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?14, ?14, 1, 1, NULL)",
            params![
                id,
                MANUAL_SOURCE_TYPE,
                ATLAS_NATIVE_SOURCE,
                values.incident_type,
                values.priority,
                values.status,
                values.summary,
                values.description,
                values.latitude,
                values.longitude,
                values.address,
                values.area,
                values.occurred_at_unix_ms,
                now,
            ],
        )
        .map_err(|error| format!("insert incident: {error}"))?;
        insert_incident_event(
            &tx,
            &id,
            "created",
            "OPEN",
            "Incident created manually in Atlas Native",
            &json!({
                "revision": 1,
                "locationRevision": 1,
                "sourceType": MANUAL_SOURCE_TYPE,
                "sourceSystem": ATLAS_NATIVE_SOURCE,
            }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit incident creation: {error}"))?;
        drop(connection);
        self.incident(&id)
    }

    pub(crate) fn update_incident(
        &self,
        incident_id: &str,
        input: &UpdateIncidentInput,
    ) -> Result<IncidentDetailSnapshot, String> {
        if input.expected_revision == 0 {
            return Err("expectedRevision must be at least 1".to_string());
        }
        let values = validate_incident_values(
            &input.incident_type,
            &input.priority,
            &input.status,
            &input.summary,
            &input.description,
            input.latitude,
            input.longitude,
            &input.address,
            &input.area,
            input.occurred_at_unix_ms,
        )?;
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin incident update: {error}"))?;
        let current = read_incident(&tx, incident_id)?;
        if current.revision != input.expected_revision {
            return Err(format!(
                "incident changed since it was loaded: expected revision {}, current revision {}; reload and retry",
                input.expected_revision, current.revision
            ));
        }
        let current_values = IncidentValues::from(&current);
        if current_values == values {
            drop(tx);
            drop(connection);
            return self.incident(incident_id);
        }

        let changed_fields = changed_fields(&current_values, &values);
        let location_changed = current_values.latitude != values.latitude
            || current_values.longitude != values.longitude;
        let revision = current.revision + 1;
        let location_revision = current.location_revision + u32::from(location_changed);
        let status_changed = current.status != values.status;
        let now = unix_time_ms();
        let closing_incident = matches!(values.status.as_str(), "RESOLVED" | "CANCELLED")
            && !matches!(current.status.as_str(), "RESOLVED" | "CANCELLED");
        if closing_incident {
            let unfinished_response_run: bool = tx
                .query_row(
                    r#"
                    SELECT EXISTS(
                        SELECT 1
                        FROM incident_assignments a
                        JOIN mission_runs r ON r.id = a.mission_run_id
                        WHERE a.incident_id = ?1
                          AND a.ended_at_unix_ms IS NULL
                          AND r.completed_at_unix_ms IS NULL
                    )
                    "#,
                    [incident_id],
                    |row| row.get(0),
                )
                .map_err(|error| format!("check incident response run before closure: {error}"))?;
            if unfinished_response_run {
                return Err(
                    "incident cannot be closed while its response mission run is unfinished; cancel or complete the mission first"
                        .to_string(),
                );
            }
        }
        tx.execute(
            "UPDATE incidents SET incident_type = ?2, priority = ?3, status = ?4, summary = ?5, description = ?6, latitude = ?7, longitude = ?8, address = ?9, area = ?10, occurred_at_unix_ms = ?11, updated_at_unix_ms = ?12, revision = ?13, location_revision = ?14 WHERE id = ?1 AND revision = ?15",
            params![
                incident_id,
                values.incident_type,
                values.priority,
                values.status,
                values.summary,
                values.description,
                values.latitude,
                values.longitude,
                values.address,
                values.area,
                values.occurred_at_unix_ms,
                now,
                revision,
                location_revision,
                input.expected_revision,
            ],
        )
        .map_err(|error| format!("update incident: {error}"))?;
        let stale_assignments = tx
            .execute(
                "UPDATE incident_assignments SET status = 'STALE', ended_at_unix_ms = ?2 WHERE incident_id = ?1 AND mission_run_id IS NULL AND ended_at_unix_ms IS NULL",
                params![incident_id, now],
            )
            .map_err(|error| format!("expire stale prepared incident responses: {error}"))?;
        let event_type = if status_changed && changed_fields.len() == 1 {
            "status_changed"
        } else {
            "updated"
        };
        let message = if event_type == "status_changed" {
            format!(
                "Incident status changed from {} to {}",
                current.status, values.status
            )
        } else {
            "Incident details updated".to_string()
        };
        insert_incident_event(
            &tx,
            incident_id,
            event_type,
            &values.status,
            &message,
            &json!({
                "previousRevision": current.revision,
                "revision": revision,
                "previousLocationRevision": current.location_revision,
                "locationRevision": location_revision,
                "changedFields": changed_fields,
                "stalePreparedAssignments": stale_assignments,
            }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit incident update: {error}"))?;
        drop(connection);
        self.incident(incident_id)
    }

    pub(crate) fn incident(&self, incident_id: &str) -> Result<IncidentDetailSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let incident = read_incident(&connection, incident_id)?;
        let events = read_incident_events(&connection, incident_id)?;
        let assignments = read_incident_assignments(&connection, incident_id)?;
        Ok(IncidentDetailSnapshot {
            incident,
            events,
            assignments,
        })
    }

    pub(crate) fn incidents(
        &self,
        include_closed: bool,
        limit: usize,
    ) -> Result<Vec<IncidentSnapshot>, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare(
                r#"
                SELECT id FROM incidents
                WHERE ?1 OR status IN ('OPEN', 'ACTIVE')
                ORDER BY
                    CASE status WHEN 'OPEN' THEN 0 WHEN 'ACTIVE' THEN 1 ELSE 2 END,
                    CASE priority WHEN 'CRITICAL' THEN 0 WHEN 'HIGH' THEN 1
                                  WHEN 'MEDIUM' THEN 2 ELSE 3 END,
                    updated_at_unix_ms DESC,
                    id DESC
                LIMIT ?2
                "#,
            )
            .map_err(|error| format!("prepare incidents: {error}"))?;
        let ids = statement
            .query_map(params![include_closed, limit.clamp(1, 500) as i64], |row| {
                row.get::<_, String>(0)
            })
            .map_err(|error| format!("query incidents: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read incident identifiers: {error}"))?;
        ids.iter()
            .map(|id| read_incident(&connection, id))
            .collect()
    }
}

fn response_mission_name(summary: &str) -> String {
    format!("Response · {summary}").chars().take(120).collect()
}

impl From<&IncidentSnapshot> for IncidentValues {
    fn from(incident: &IncidentSnapshot) -> Self {
        Self {
            incident_type: incident.incident_type.clone(),
            priority: incident.priority.clone(),
            status: incident.status.clone(),
            summary: incident.summary.clone(),
            description: incident.description.clone(),
            latitude: incident.latitude,
            longitude: incident.longitude,
            address: incident.address.clone(),
            area: incident.area.clone(),
            occurred_at_unix_ms: incident.occurred_at_unix_ms,
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn validate_incident_values(
    incident_type: &str,
    priority: &str,
    status: &str,
    summary: &str,
    description: &str,
    latitude: Option<f64>,
    longitude: Option<f64>,
    address: &str,
    area: &str,
    occurred_at_unix_ms: Option<i64>,
) -> Result<IncidentValues, String> {
    let incident_type = required_text(incident_type, "incidentType", MAX_INCIDENT_TYPE_LENGTH)?;
    let summary = required_text(summary, "summary", MAX_SUMMARY_LENGTH)?;
    let description = optional_text(description, "description", MAX_DESCRIPTION_LENGTH)?;
    let address = optional_text(address, "address", MAX_LOCATION_LABEL_LENGTH)?;
    let area = optional_text(area, "area", MAX_LOCATION_LABEL_LENGTH)?;
    let priority = priority.trim().to_ascii_uppercase();
    if !matches!(priority.as_str(), "LOW" | "MEDIUM" | "HIGH" | "CRITICAL") {
        return Err("priority must be LOW, MEDIUM, HIGH, or CRITICAL".to_string());
    }
    let status = status.trim().to_ascii_uppercase();
    if !matches!(
        status.as_str(),
        "OPEN" | "ACTIVE" | "RESOLVED" | "CANCELLED"
    ) {
        return Err("status must be OPEN, ACTIVE, RESOLVED, or CANCELLED".to_string());
    }
    match (latitude, longitude) {
        (Some(latitude), Some(longitude)) => {
            if !latitude.is_finite() || !(-90.0..=90.0).contains(&latitude) {
                return Err("latitude must be a finite value between -90 and 90".to_string());
            }
            if !longitude.is_finite() || !(-180.0..=180.0).contains(&longitude) {
                return Err("longitude must be a finite value between -180 and 180".to_string());
            }
        }
        (None, None) => {}
        _ => return Err("latitude and longitude must be provided together".to_string()),
    }
    if occurred_at_unix_ms.is_some_and(|value| value <= 0) {
        return Err("occurredAtUnixMs must be greater than zero".to_string());
    }
    Ok(IncidentValues {
        incident_type,
        priority,
        status,
        summary,
        description,
        latitude,
        longitude,
        address,
        area,
        occurred_at_unix_ms,
    })
}

fn required_text(value: &str, field: &str, maximum: usize) -> Result<String, String> {
    let value = value.trim();
    if value.is_empty() {
        return Err(format!("{field} is required"));
    }
    bounded_text(value, field, maximum)
}

fn optional_text(value: &str, field: &str, maximum: usize) -> Result<String, String> {
    bounded_text(value.trim(), field, maximum)
}

fn bounded_text(value: &str, field: &str, maximum: usize) -> Result<String, String> {
    if value.chars().count() > maximum {
        return Err(format!("{field} must be {maximum} characters or fewer"));
    }
    Ok(value.to_string())
}

fn changed_fields(current: &IncidentValues, next: &IncidentValues) -> Vec<&'static str> {
    let mut fields = Vec::new();
    if current.incident_type != next.incident_type {
        fields.push("incidentType");
    }
    if current.priority != next.priority {
        fields.push("priority");
    }
    if current.status != next.status {
        fields.push("status");
    }
    if current.summary != next.summary {
        fields.push("summary");
    }
    if current.description != next.description {
        fields.push("description");
    }
    if current.latitude != next.latitude {
        fields.push("latitude");
    }
    if current.longitude != next.longitude {
        fields.push("longitude");
    }
    if current.address != next.address {
        fields.push("address");
    }
    if current.area != next.area {
        fields.push("area");
    }
    if current.occurred_at_unix_ms != next.occurred_at_unix_ms {
        fields.push("occurredAtUnixMs");
    }
    fields
}

fn read_incident(connection: &Connection, incident_id: &str) -> Result<IncidentSnapshot, String> {
    connection
        .query_row(
            "SELECT id, source_type, source_system, external_id, incident_type, priority, status, summary, description, latitude, longitude, address, area, occurred_at_unix_ms, received_at_unix_ms, created_at_unix_ms, updated_at_unix_ms, revision, location_revision, source_payload_json FROM incidents WHERE id = ?1",
            [incident_id],
            |row| {
                let source_payload_json: Option<String> = row.get(19)?;
                Ok(IncidentSnapshot {
                    id: row.get(0)?,
                    source_type: row.get(1)?,
                    source_system: row.get(2)?,
                    external_id: row.get(3)?,
                    incident_type: row.get(4)?,
                    priority: row.get(5)?,
                    status: row.get(6)?,
                    summary: row.get(7)?,
                    description: row.get(8)?,
                    latitude: row.get(9)?,
                    longitude: row.get(10)?,
                    address: row.get(11)?,
                    area: row.get(12)?,
                    occurred_at_unix_ms: row.get(13)?,
                    received_at_unix_ms: row.get(14)?,
                    created_at_unix_ms: row.get(15)?,
                    updated_at_unix_ms: row.get(16)?,
                    revision: row.get(17)?,
                    location_revision: row.get(18)?,
                    source_payload: source_payload_json
                        .and_then(|value| serde_json::from_str(&value).ok()),
                })
            },
        )
        .optional()
        .map_err(|error| format!("read incident: {error}"))?
        .ok_or_else(|| format!("incident {incident_id} was not found"))
}

fn read_incident_events(
    connection: &Connection,
    incident_id: &str,
) -> Result<Vec<IncidentEventSnapshot>, String> {
    let mut statement = connection
        .prepare("SELECT id, incident_id, sequence, event_type, state, source, message, details_json, occurred_at_unix_ms, received_at_unix_ms FROM incident_events WHERE incident_id = ?1 ORDER BY sequence")
        .map_err(|error| format!("prepare incident events: {error}"))?;
    let events = statement
        .query_map([incident_id], |row| {
            let details_json: String = row.get(7)?;
            Ok(IncidentEventSnapshot {
                id: row.get(0)?,
                incident_id: row.get(1)?,
                sequence: row.get(2)?,
                event_type: row.get(3)?,
                state: row.get(4)?,
                source: row.get(5)?,
                message: row.get(6)?,
                details: serde_json::from_str(&details_json).unwrap_or(Value::Null),
                occurred_at_unix_ms: row.get(8)?,
                received_at_unix_ms: row.get(9)?,
            })
        })
        .map_err(|error| format!("query incident events: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read incident events: {error}"))?;
    Ok(events)
}

fn read_incident_assignments(
    connection: &Connection,
    incident_id: &str,
) -> Result<Vec<IncidentAssignmentSnapshot>, String> {
    let mut statement = connection
        .prepare(
            r#"
            SELECT a.id, a.incident_id, a.drone_id, d.name, a.mission_id, m.name,
                   m.generated_plan_id, a.mission_run_id, a.operator_id, a.status,
                   a.assigned_at_unix_ms, a.on_scene_at_unix_ms, a.ended_at_unix_ms
            FROM incident_assignments a
            JOIN drones d ON d.id = a.drone_id
            LEFT JOIN missions m ON m.id = a.mission_id
            WHERE a.incident_id = ?1
            ORDER BY a.assigned_at_unix_ms DESC, a.id DESC
            "#,
        )
        .map_err(|error| format!("prepare incident assignments: {error}"))?;
    let assignments = statement
        .query_map([incident_id], |row| {
            Ok(IncidentAssignmentSnapshot {
                id: row.get(0)?,
                incident_id: row.get(1)?,
                drone_id: row.get(2)?,
                drone_name: row.get(3)?,
                mission_id: row.get(4)?,
                mission_name: row.get(5)?,
                generated_plan_id: row.get(6)?,
                mission_run_id: row.get(7)?,
                operator_id: row.get(8)?,
                status: row.get(9)?,
                assigned_at_unix_ms: row.get(10)?,
                on_scene_at_unix_ms: row.get(11)?,
                ended_at_unix_ms: row.get(12)?,
            })
        })
        .map_err(|error| format!("query incident assignments: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read incident assignments: {error}"))?;
    Ok(assignments)
}

pub(super) struct ActiveIncidentAssignment {
    pub(super) id: String,
    pub(super) incident_id: String,
}

struct IncidentAssignmentUploadContext {
    id: String,
    incident_id: String,
    assigned_drone_id: String,
    mission_run_id: Option<String>,
    ended_at_unix_ms: Option<i64>,
    incident_status: String,
    incident_revision: u32,
    mission_params_json: String,
}

pub(super) fn validate_incident_assignment_upload(
    tx: &Transaction<'_>,
    mission_id: &str,
    drone_id: &str,
) -> Result<Option<ActiveIncidentAssignment>, String> {
    let assignment: Option<IncidentAssignmentUploadContext> = tx
        .query_row(
            r#"
            SELECT a.id, a.incident_id, a.drone_id, a.mission_run_id,
                   a.ended_at_unix_ms, i.status, i.revision, m.params_json
            FROM incident_assignments a
            JOIN incidents i ON i.id = a.incident_id
            JOIN missions m ON m.id = a.mission_id
            WHERE a.mission_id = ?1
            "#,
            [mission_id],
            |row| {
                Ok(IncidentAssignmentUploadContext {
                    id: row.get(0)?,
                    incident_id: row.get(1)?,
                    assigned_drone_id: row.get(2)?,
                    mission_run_id: row.get(3)?,
                    ended_at_unix_ms: row.get(4)?,
                    incident_status: row.get(5)?,
                    incident_revision: row.get(6)?,
                    mission_params_json: row.get(7)?,
                })
            },
        )
        .optional()
        .map_err(|error| format!("read incident assignment for mission upload: {error}"))?;
    let Some(assignment) = assignment else {
        return Ok(None);
    };
    if assignment.assigned_drone_id != drone_id {
        return Err(format!(
            "incident response plan is assigned to aircraft {}; select that aircraft or prepare a new response",
            assignment.assigned_drone_id
        ));
    }
    if assignment.ended_at_unix_ms.is_some() {
        return Err(
            "incident response assignment has ended; prepare a new response before upload"
                .to_string(),
        );
    }
    if assignment.mission_run_id.is_some() {
        return Err("incident response assignment already has a mission run".to_string());
    }
    if !matches!(assignment.incident_status.as_str(), "OPEN" | "ACTIVE") {
        return Err(format!(
            "incident response upload is blocked because the incident is {}",
            assignment.incident_status
        ));
    }
    let mission_params: Value = serde_json::from_str(&assignment.mission_params_json)
        .map_err(|error| format!("decode incident response mission evidence: {error}"))?;
    let prepared_revision = mission_params
        .get("incidentResponse")
        .and_then(|value| value.get("incidentRevision"))
        .and_then(Value::as_u64)
        .and_then(|value| u32::try_from(value).ok())
        .ok_or_else(|| "incident response mission is missing revision evidence".to_string())?;
    if prepared_revision != assignment.incident_revision {
        return Err(format!(
            "incident changed after response preparation: prepared revision {prepared_revision}, current revision {}; prepare a new response",
            assignment.incident_revision
        ));
    }
    Ok(Some(ActiveIncidentAssignment {
        id: assignment.id,
        incident_id: assignment.incident_id,
    }))
}

pub(super) fn link_incident_assignment_run(
    tx: &Transaction<'_>,
    assignment: &ActiveIncidentAssignment,
    mission_run_id: &str,
    now: i64,
) -> Result<(), String> {
    let changed = tx
        .execute(
            "UPDATE incident_assignments SET mission_run_id = ?2, status = 'UPLOADING' WHERE id = ?1 AND mission_run_id IS NULL AND ended_at_unix_ms IS NULL",
            params![assignment.id, mission_run_id],
        )
        .map_err(|error| format!("link incident assignment mission run: {error}"))?;
    if changed != 1 {
        return Err(
            "incident response assignment changed during upload; reload and retry".to_string(),
        );
    }
    let incident_state: String = tx
        .query_row(
            "SELECT status FROM incidents WHERE id = ?1",
            [&assignment.incident_id],
            |row| row.get(0),
        )
        .map_err(|error| format!("read incident state for response upload: {error}"))?;
    insert_incident_event(
        tx,
        &assignment.incident_id,
        "response_upload_requested",
        &incident_state,
        "Incident response mission upload requested",
        &json!({
            "assignmentId": assignment.id,
            "missionRunId": mission_run_id,
            "runState": "UPLOADING",
        }),
        now,
    )
}

pub(super) fn mark_incident_response_on_scene(
    tx: &Transaction<'_>,
    mission_run_id: &str,
    action_execution_id: &str,
    action_sequence: u32,
    now: i64,
) -> Result<(), String> {
    let assignment: Option<(String, String, String, Option<i64>)> = tx
        .query_row(
            r#"
            SELECT a.id, a.incident_id, i.status, a.on_scene_at_unix_ms
            FROM incident_assignments a
            JOIN incidents i ON i.id = a.incident_id
            WHERE a.mission_run_id = ?1 AND a.ended_at_unix_ms IS NULL
            "#,
            [mission_run_id],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?)),
        )
        .optional()
        .map_err(|error| {
            format!("read incident assignment for arrival acknowledgement: {error}")
        })?;
    let Some((assignment_id, incident_id, incident_status, on_scene_at)) = assignment else {
        return Ok(());
    };
    if on_scene_at.is_some() {
        return Ok(());
    }
    let changed = tx
        .execute(
            "UPDATE incident_assignments SET status = 'ON_SCENE', on_scene_at_unix_ms = ?2 WHERE id = ?1 AND on_scene_at_unix_ms IS NULL AND ended_at_unix_ms IS NULL",
            params![assignment_id, now],
        )
        .map_err(|error| format!("mark incident response on scene: {error}"))?;
    if changed != 1 {
        return Err(
            "incident response assignment changed during arrival acknowledgement".to_string(),
        );
    }
    insert_incident_event(
        tx,
        &incident_id,
        "response_on_scene",
        &incident_status,
        "Aircraft Hold at the incident staging point was acknowledged",
        &json!({
            "assignmentId": assignment_id,
            "missionRunId": mission_run_id,
            "actionExecutionId": action_execution_id,
            "actionSequence": action_sequence,
            "actionType": "HOLD_AT_ARRIVAL",
            "acknowledged": true,
        }),
        now,
    )
}

pub(super) fn sync_incident_assignment_run_state(
    tx: &Transaction<'_>,
    mission_run_id: &str,
    run_state: &str,
    terminal: bool,
    now: i64,
) -> Result<(), String> {
    let assignment: Option<(String, String, String, String, Option<i64>)> = tx
        .query_row(
            r#"
            SELECT a.id, a.incident_id, a.status, i.status, a.on_scene_at_unix_ms
            FROM incident_assignments a
            JOIN incidents i ON i.id = a.incident_id
            WHERE a.mission_run_id = ?1 AND a.ended_at_unix_ms IS NULL
            "#,
            [mission_run_id],
            |row| {
                Ok((
                    row.get(0)?,
                    row.get(1)?,
                    row.get(2)?,
                    row.get(3)?,
                    row.get(4)?,
                ))
            },
        )
        .optional()
        .map_err(|error| format!("read incident assignment mission state: {error}"))?;
    let Some((assignment_id, incident_id, assignment_status, incident_status, on_scene_at)) =
        assignment
    else {
        return Ok(());
    };
    let assignment_run_state =
        if on_scene_at.is_some() && !terminal && matches!(run_state, "RUNNING" | "PAUSED") {
            "ON_SCENE"
        } else {
            run_state
        };
    if assignment_status == assignment_run_state {
        return Ok(());
    }
    tx.execute(
        "UPDATE incident_assignments SET status = ?2, ended_at_unix_ms = CASE WHEN ?3 THEN ?4 ELSE ended_at_unix_ms END WHERE id = ?1",
        params![assignment_id, assignment_run_state, terminal, now],
    )
    .map_err(|error| format!("update incident assignment mission state: {error}"))?;
    let event_type = format!("response_{}", assignment_run_state.to_ascii_lowercase());
    insert_incident_event(
        tx,
        &incident_id,
        &event_type,
        &incident_status,
        &format!("Incident response mission changed to {assignment_run_state}"),
        &json!({
            "assignmentId": assignment_id,
            "missionRunId": mission_run_id,
            "runState": run_state,
            "assignmentState": assignment_run_state,
            "terminal": terminal,
        }),
        now,
    )
}

pub(super) fn insert_incident_event(
    tx: &Transaction<'_>,
    incident_id: &str,
    event_type: &str,
    state: &str,
    message: &str,
    details: &Value,
    now: i64,
) -> Result<(), String> {
    let sequence: u32 = tx
        .query_row(
            "SELECT COALESCE(max(sequence), -1) + 1 FROM incident_events WHERE incident_id = ?1",
            [incident_id],
            |row| row.get(0),
        )
        .map_err(|error| format!("calculate incident event sequence: {error}"))?;
    tx.execute(
        "INSERT INTO incident_events (id, incident_id, sequence, event_type, state, source, message, details_json, occurred_at_unix_ms, received_at_unix_ms) VALUES (?1, ?2, ?3, ?4, ?5, 'atlas_native', ?6, ?7, ?8, ?8)",
        params![
            generate_id(tx)?,
            incident_id,
            sequence,
            event_type,
            state,
            message,
            details.to_string(),
            now,
        ],
    )
    .map_err(|error| format!("insert incident event: {error}"))?;
    Ok(())
}

fn generate_id(tx: &Transaction<'_>) -> Result<String, String> {
    tx.query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate incident identifier: {error}"))
}
