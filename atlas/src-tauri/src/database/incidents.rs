use rusqlite::{params, Connection, OptionalExtension, Transaction};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use super::{
    alerts::{observe_incident_revision_alerts, resolve_alert, resolve_incident_revision_alerts},
    known_buildings::{
        assess_known_buildings, GeoPoint, KnownBuildingAssessment, KnownBuildingDataset,
        RouteAssessmentStart,
    },
    missions::{create_ready_mission_in_transaction, preview_ready_mission},
    unix_time_ms, CreateMissionInput, LocalDatabase, MissionAction, MissionPlanSnapshot,
    MissionSnapshot, MissionWaypoint, VehicleHealth,
};

const MANUAL_SOURCE_TYPE: &str = "MANUAL";
const ATLAS_NATIVE_SOURCE: &str = "ATLAS_NATIVE";
const MAX_INCIDENT_TYPE_LENGTH: usize = 80;
const MAX_SUMMARY_LENGTH: usize = 200;
const MAX_DESCRIPTION_LENGTH: usize = 4_000;
const MAX_LOCATION_LABEL_LENGTH: usize = 500;
const RESPONSE_DEPARTURE_MAX_AGE_MS: i64 = 5_000;
const RESPONSE_DEPARTURE_HORIZONTAL_TOLERANCE_METERS: f64 = 30.0;
const RESPONSE_DEPARTURE_ALTITUDE_TOLERANCE_METERS: f64 = 5.0;
const RESPONSE_SUITABILITY_LINK_MAX_AGE_MS: i64 = 15_000;
const RESPONSE_SUITABILITY_TELEMETRY_MAX_AGE_MS: i64 = 5_000;
const RESPONSE_SUITABILITY_MINIMUM_BATTERY_PERCENT: f64 = 15.0;
const RESPONSE_SUITABILITY_LOW_BATTERY_PERCENT: f64 = 25.0;

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

#[cfg(test)]
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

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct ExpandedPrepareIncidentResponseInput {
    pub expected_incident_revision: u32,
    pub drone_id: String,
    pub geometry: IncidentResponseGeometryInput,
    pub arrival_failure_policy: String,
    pub incident_target_altitude_amsl_meters: Option<f64>,
    #[serde(default = "default_building_horizontal_clearance")]
    pub building_horizontal_clearance_meters: f64,
    #[serde(default = "default_building_vertical_clearance")]
    pub building_vertical_clearance_meters: f64,
    pub known_building_override_reason: Option<String>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(
    tag = "responsePattern",
    rename_all = "SCREAMING_SNAKE_CASE",
    rename_all_fields = "camelCase"
)]
pub(crate) enum IncidentResponseGeometryInput {
    HoldAtStaging {
        staging_latitude: f64,
        staging_longitude: f64,
        altitude_meters: f64,
        speed_mps: f64,
    },
    OffsetObserve {
        observation_latitude: f64,
        observation_longitude: f64,
        altitude_meters: f64,
        speed_mps: f64,
    },
    BoundedAreaScan {
        area_polygon: Vec<GeoPoint>,
        altitude_meters: f64,
        speed_mps: f64,
        lane_spacing_meters: f64,
        sweep_angle_degrees: f64,
    },
    BoundedOrbit {
        center_latitude: f64,
        center_longitude: f64,
        radius_meters: f64,
        altitude_levels_meters: Vec<f64>,
        speed_mps: f64,
        laps_per_level: u32,
        direction: String,
        max_vertical_rate_mps: f64,
    },
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct IncidentResponseAircraftSuitabilityInput {
    pub response_pattern: String,
    pub target_latitude: f64,
    pub target_longitude: f64,
    pub speed_mps: f64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct AircraftSuitabilityReason {
    pub code: String,
    pub message: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct IncidentResponseAircraftSuitabilitySnapshot {
    pub drone_id: String,
    pub drone_name: String,
    pub available: bool,
    pub recommended: bool,
    pub connection_status: String,
    pub battery_percent: Option<f64>,
    pub telemetry_age_ms: Option<i64>,
    pub distance_meters: Option<f64>,
    pub estimated_arrival_seconds: Option<f64>,
    pub active_incident_id: Option<String>,
    pub unfinished_mission_run_id: Option<String>,
    pub blockers: Vec<AircraftSuitabilityReason>,
    pub considerations: Vec<AircraftSuitabilityReason>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct IncidentResponsePlanPreview {
    pub template_type: String,
    pub pattern_type: String,
    pub generated_waypoints: Vec<MissionWaypoint>,
    pub actions: Vec<MissionAction>,
    pub metadata: Value,
    pub validation_warnings: Vec<String>,
    pub known_building_assessment: KnownBuildingAssessment,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct AbandonPreparedResponseInput {
    pub expected_incident_revision: u32,
    pub reason: String,
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

struct PreparedAssignmentAbandonContext {
    incident_id: String,
    drone_id: String,
    mission_id: Option<String>,
    mission_run_id: Option<String>,
    status: String,
    ended_at_unix_ms: Option<i64>,
}

struct SuitabilityAircraftRow {
    drone_id: String,
    drone_name: String,
    lifecycle_status: String,
    telemetry_received_at_unix_ms: Option<i64>,
    battery_percent: Option<f64>,
    latitude: Option<f64>,
    longitude: Option<f64>,
    armed: Option<bool>,
    home_position_set: Option<bool>,
    health_json: Option<String>,
    link_status: Option<String>,
    link_started_at_unix_ms: Option<i64>,
    last_heartbeat_at_unix_ms: Option<i64>,
    link_ended_at_unix_ms: Option<i64>,
    capabilities_json: Option<String>,
    active_incident_id: Option<String>,
    unfinished_mission_run_id: Option<String>,
}

impl LocalDatabase {
    pub(crate) fn incident_response_aircraft_suitability(
        &self,
        incident_id: &str,
        input: &IncidentResponseAircraftSuitabilityInput,
    ) -> Result<Vec<IncidentResponseAircraftSuitabilitySnapshot>, String> {
        self.incident_response_aircraft_suitability_at(incident_id, input, unix_time_ms())
    }

    pub(super) fn incident_response_aircraft_suitability_at(
        &self,
        incident_id: &str,
        input: &IncidentResponseAircraftSuitabilityInput,
        now: i64,
    ) -> Result<Vec<IncidentResponseAircraftSuitabilitySnapshot>, String> {
        let pattern = input.response_pattern.trim().to_ascii_uppercase();
        if !matches!(
            pattern.as_str(),
            "HOLD_AT_STAGING" | "OFFSET_OBSERVE" | "BOUNDED_AREA_SCAN" | "BOUNDED_ORBIT"
        ) {
            return Err("responsePattern is not supported for aircraft suitability".to_string());
        }
        if !valid_response_coordinate(input.target_latitude, input.target_longitude) {
            return Err("aircraft suitability target must contain valid WGS84 coordinates".into());
        }
        if !input.speed_mps.is_finite() || !(0.5..=15.0).contains(&input.speed_mps) {
            return Err("aircraft suitability speedMps must be between 0.5 and 15".into());
        }

        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let incident = read_incident(&connection, incident_id)?;
        if !matches!(incident.status.as_str(), "OPEN" | "ACTIVE") {
            return Err(format!(
                "aircraft suitability is unavailable while the incident is {}",
                incident.status
            ));
        }

        let mut statement = connection
            .prepare(
                r#"
                SELECT d.id, d.name, d.status,
                       t.received_at_unix_ms, t.battery_percent,
                       t.latitude, t.longitude, t.armed,
                       t.home_position_set, t.health_json,
                       l.status, l.started_at_unix_ms,
                       l.last_heartbeat_at_unix_ms, l.ended_at_unix_ms,
                       a.capabilities_json,
                       ia.incident_id,
                       mr.id
                FROM drones d
                LEFT JOIN vehicle_telemetry_current t ON t.drone_id = d.id
                LEFT JOIN vehicle_agent_bindings b ON b.id = (
                    SELECT candidate.id
                    FROM vehicle_agent_bindings candidate
                    WHERE candidate.drone_id = d.id
                    ORDER BY CASE candidate.status
                                 WHEN 'active' THEN 0
                                 WHEN 'suspended' THEN 1
                                 ELSE 2
                             END,
                             candidate.bound_at_unix_ms DESC,
                             candidate.rowid DESC
                    LIMIT 1
                )
                LEFT JOIN vehicle_agents a ON a.id = b.vehicle_agent_id
                LEFT JOIN communication_links l ON l.id = (
                    SELECT candidate.id
                    FROM communication_links candidate
                    WHERE candidate.vehicle_agent_binding_id = b.id
                    ORDER BY candidate.started_at_unix_ms DESC,
                             candidate.rowid DESC
                    LIMIT 1
                )
                LEFT JOIN incident_assignments ia ON ia.id = (
                    SELECT candidate.id
                    FROM incident_assignments candidate
                    WHERE candidate.drone_id = d.id
                      AND candidate.ended_at_unix_ms IS NULL
                    ORDER BY candidate.assigned_at_unix_ms DESC,
                             candidate.rowid DESC
                    LIMIT 1
                )
                LEFT JOIN mission_runs mr ON mr.id = (
                    SELECT candidate.id
                    FROM mission_runs candidate
                    WHERE candidate.drone_id = d.id
                      AND candidate.completed_at_unix_ms IS NULL
                    ORDER BY candidate.created_at_unix_ms DESC,
                             candidate.rowid DESC
                    LIMIT 1
                )
                WHERE d.status != 'archived'
                ORDER BY d.name COLLATE NOCASE, d.id
                "#,
            )
            .map_err(|error| format!("prepare incident-response aircraft suitability: {error}"))?;
        let rows = statement
            .query_map([], |row| {
                Ok(SuitabilityAircraftRow {
                    drone_id: row.get(0)?,
                    drone_name: row.get(1)?,
                    lifecycle_status: row.get(2)?,
                    telemetry_received_at_unix_ms: row.get(3)?,
                    battery_percent: row.get(4)?,
                    latitude: row.get(5)?,
                    longitude: row.get(6)?,
                    armed: row.get(7)?,
                    home_position_set: row.get(8)?,
                    health_json: row.get(9)?,
                    link_status: row.get(10)?,
                    link_started_at_unix_ms: row.get(11)?,
                    last_heartbeat_at_unix_ms: row.get(12)?,
                    link_ended_at_unix_ms: row.get(13)?,
                    capabilities_json: row.get(14)?,
                    active_incident_id: row.get(15)?,
                    unfinished_mission_run_id: row.get(16)?,
                })
            })
            .map_err(|error| format!("read incident-response aircraft suitability: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("decode incident-response aircraft suitability: {error}"))?;

        let mut snapshots = rows
            .into_iter()
            .map(|row| aircraft_suitability_snapshot(row, &pattern, input, now))
            .collect::<Vec<_>>();
        snapshots.sort_by(|left, right| {
            right
                .available
                .cmp(&left.available)
                .then_with(|| left.blockers.len().cmp(&right.blockers.len()))
                .then_with(|| {
                    left.estimated_arrival_seconds
                        .unwrap_or(f64::INFINITY)
                        .total_cmp(&right.estimated_arrival_seconds.unwrap_or(f64::INFINITY))
                })
                .then_with(|| {
                    right
                        .battery_percent
                        .unwrap_or(f64::NEG_INFINITY)
                        .total_cmp(&left.battery_percent.unwrap_or(f64::NEG_INFINITY))
                })
                .then_with(|| left.drone_name.cmp(&right.drone_name))
                .then_with(|| left.drone_id.cmp(&right.drone_id))
        });
        if let Some(recommended) = snapshots.iter_mut().find(|candidate| candidate.available) {
            recommended.recommended = true;
        }
        Ok(snapshots)
    }

    #[cfg(test)]
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
            "triggerAfterWaypointSequence": 0,
            "failurePolicy": failure_policy,
            "maxAttempts": 3,
            "timeoutMs": 20_000,
            "retryInitialDelayMs": 2_000,
            "retryBackoffMultiplier": 2.0,
        })];
        if let Some(altitude_amsl_meters) = target_altitude {
            arrival_actions.push(json!({
                "actionType": "POINT_GIMBAL_AT_INCIDENT",
                "triggerAfterWaypointSequence": 0,
                "failurePolicy": "SKIP_OPTIONAL_AND_NOTIFY",
                "maxAttempts": 3,
                "timeoutMs": 20_000,
                "retryInitialDelayMs": 2_000,
                "retryBackoffMultiplier": 2.0,
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
                    "optionalActionFailurePolicy": "SKIP_OPTIONAL_AND_NOTIFY",
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
                "optionalActionFailurePolicy": "SKIP_OPTIONAL_AND_NOTIFY",
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
        resolve_incident_revision_alerts(
            &tx,
            &incident.id,
            "A replacement response was prepared against the current incident revision",
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit incident response preparation: {error}"))?;
        Ok(PreparedIncidentResponse {
            incident,
            assignment,
            mission,
            plan,
        })
    }

    pub(crate) fn preview_expanded_incident_response(
        &self,
        incident_id: &str,
        input: &ExpandedPrepareIncidentResponseInput,
    ) -> Result<IncidentResponsePlanPreview, String> {
        let dataset = KnownBuildingDataset::from_environment()?;
        self.preview_expanded_incident_response_with_dataset(incident_id, input, dataset.as_ref())
    }

    pub(crate) fn preview_expanded_incident_response_with_dataset(
        &self,
        incident_id: &str,
        input: &ExpandedPrepareIncidentResponseInput,
        dataset: Option<&KnownBuildingDataset>,
    ) -> Result<IncidentResponsePlanPreview, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let (_, _, _, preview) =
            expanded_response_preview(&connection, incident_id, input, dataset, unix_time_ms())?;
        Ok(preview)
    }

    pub(crate) fn prepare_expanded_incident_response(
        &self,
        incident_id: &str,
        input: &ExpandedPrepareIncidentResponseInput,
    ) -> Result<PreparedIncidentResponse, String> {
        let dataset = KnownBuildingDataset::from_environment()?;
        self.prepare_expanded_incident_response_with_dataset(incident_id, input, dataset.as_ref())
    }

    pub(crate) fn prepare_expanded_incident_response_with_dataset(
        &self,
        incident_id: &str,
        input: &ExpandedPrepareIncidentResponseInput,
        dataset: Option<&KnownBuildingDataset>,
    ) -> Result<PreparedIncidentResponse, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin expanded incident response preparation: {error}"))?;
        let now = unix_time_ms();
        let (incident, drone_name, mission_input, preview) =
            expanded_response_preview(&tx, incident_id, input, dataset, now)?;
        if preview.known_building_assessment.override_required
            && preview
                .known_building_assessment
                .override_reason
                .as_deref()
                .is_none_or(str::is_empty)
        {
            return Err(format!(
                "known-building assessment is {}; review the result and provide an explicit override reason before preparation",
                preview.known_building_assessment.status
            ));
        }
        let drone_id = input.drone_id.trim();
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
        let (mission, plan) = create_ready_mission_in_transaction(&tx, &mission_input, now)?;
        let assignment_id = generate_id(&tx)?;
        tx.execute(
            "INSERT INTO incident_assignments (id, incident_id, drone_id, mission_id, mission_run_id, operator_id, status, assigned_at_unix_ms, ended_at_unix_ms) VALUES (?1, ?2, ?3, ?4, NULL, NULL, 'PREPARED', ?5, NULL)",
            params![assignment_id, incident.id, drone_id, mission.id, now],
        )
        .map_err(|error| format!("insert expanded incident response assignment: {error}"))?;
        let response_evidence = mission_input
            .params
            .get("incidentResponse")
            .cloned()
            .unwrap_or(Value::Null);
        insert_incident_event(
            &tx,
            &incident.id,
            "response_prepared",
            &incident.status,
            "Expanded incident response plan prepared for operator confirmation",
            &json!({
                "assignmentId": assignment_id,
                "droneId": drone_id,
                "missionId": mission.id,
                "planId": plan.id,
                "incidentRevision": incident.revision,
                "locationRevision": incident.location_revision,
                "responseEvidence": response_evidence,
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
        resolve_incident_revision_alerts(
            &tx,
            &incident.id,
            "A replacement response was prepared against the current incident revision",
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit expanded incident response preparation: {error}"))?;
        Ok(PreparedIncidentResponse {
            incident,
            assignment,
            mission,
            plan,
        })
    }

    pub(crate) fn abandon_prepared_response(
        &self,
        incident_id: &str,
        assignment_id: &str,
        input: &AbandonPreparedResponseInput,
    ) -> Result<IncidentDetailSnapshot, String> {
        if input.expected_incident_revision == 0 {
            return Err("expectedIncidentRevision must be at least 1".to_string());
        }
        let reason = input.reason.trim();
        if reason.is_empty() {
            return Err(
                "an abandonment reason is required for the incident audit trail".to_string(),
            );
        }
        if reason.chars().count() > 500 {
            return Err("abandonment reason must be 500 characters or fewer".to_string());
        }

        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin prepared response abandonment: {error}"))?;
        let incident = read_incident(&tx, incident_id)?;
        if incident.revision != input.expected_incident_revision {
            return Err(format!(
                "incident changed before the prepared response was released: expected revision {}, current revision {}; reload and retry",
                input.expected_incident_revision, incident.revision
            ));
        }
        let assignment: Option<PreparedAssignmentAbandonContext> = tx
            .query_row(
                r#"
                SELECT incident_id, drone_id, mission_id, mission_run_id, status, ended_at_unix_ms
                FROM incident_assignments
                WHERE id = ?1
                "#,
                [assignment_id],
                |row| {
                    Ok(PreparedAssignmentAbandonContext {
                        incident_id: row.get(0)?,
                        drone_id: row.get(1)?,
                        mission_id: row.get(2)?,
                        mission_run_id: row.get(3)?,
                        status: row.get(4)?,
                        ended_at_unix_ms: row.get(5)?,
                    })
                },
            )
            .optional()
            .map_err(|error| format!("read prepared response assignment: {error}"))?;
        let assignment = assignment
            .ok_or_else(|| format!("incident assignment {assignment_id} was not found"))?;
        if assignment.incident_id != incident_id {
            return Err("prepared response assignment belongs to a different incident".to_string());
        }
        if assignment.status == "ABANDONED" && assignment.ended_at_unix_ms.is_some() {
            tx.commit()
                .map_err(|error| format!("commit repeated response abandonment: {error}"))?;
            drop(connection);
            return self.incident(incident_id);
        }
        if assignment.mission_run_id.is_some() {
            return Err(
                "prepared response can no longer be abandoned because mission upload has begun; use mission controls"
                    .to_string(),
            );
        }
        if assignment.status != "PREPARED" || assignment.ended_at_unix_ms.is_some() {
            return Err(format!(
                "only an active PREPARED response can be abandoned; assignment is {}",
                assignment.status
            ));
        }

        let now = unix_time_ms();
        let changed = tx
            .execute(
                "UPDATE incident_assignments SET status = 'ABANDONED', ended_at_unix_ms = ?2 WHERE id = ?1 AND status = 'PREPARED' AND mission_run_id IS NULL AND ended_at_unix_ms IS NULL",
                params![assignment_id, now],
            )
            .map_err(|error| format!("release prepared response assignment: {error}"))?;
        if changed != 1 {
            return Err(
                "prepared response changed while it was being released; reload and retry"
                    .to_string(),
            );
        }
        let plan_id: Option<String> = assignment
            .mission_id
            .as_deref()
            .map(|id| {
                tx.query_row(
                    "SELECT generated_plan_id FROM missions WHERE id = ?1",
                    [id],
                    |row| row.get(0),
                )
                .optional()
                .map_err(|error| format!("read abandoned response plan: {error}"))
            })
            .transpose()?
            .flatten();
        insert_incident_event(
            &tx,
            incident_id,
            "response_abandoned",
            &incident.status,
            "Prepared incident response abandoned before upload",
            &json!({
                "assignmentId": assignment_id,
                "droneId": assignment.drone_id,
                "missionId": assignment.mission_id,
                "planId": plan_id,
                "reason": reason,
                "aircraftReservationReleased": true,
            }),
            now,
        )?;
        resolve_alert(
            &tx,
            &format!("incident_revision_changed:{assignment_id}"),
            "The stale prepared response was abandoned",
            &json!({ "assignmentId": assignment_id, "incidentId": incident_id }),
            now,
        )?;
        tx.commit()
            .map_err(|error| format!("commit prepared response abandonment: {error}"))?;
        drop(connection);
        self.incident(incident_id)
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
        if closing_incident {
            resolve_incident_revision_alerts(
                &tx,
                incident_id,
                "The incident was closed and no replacement response is required",
                now,
            )?;
        } else {
            observe_incident_revision_alerts(&tx, incident_id, revision, &changed_fields, now)?;
        }
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

fn aircraft_suitability_snapshot(
    row: SuitabilityAircraftRow,
    pattern: &str,
    input: &IncidentResponseAircraftSuitabilityInput,
    now: i64,
) -> IncidentResponseAircraftSuitabilitySnapshot {
    let mut blockers = Vec::new();
    let mut considerations = Vec::new();
    let mut block = |code: &str, message: String| {
        blockers.push(AircraftSuitabilityReason {
            code: code.into(),
            message,
        });
    };
    let mut consider = |code: &str, message: String| {
        considerations.push(AircraftSuitabilityReason {
            code: code.into(),
            message,
        });
    };

    if row.lifecycle_status != "active" {
        block(
            "LIFECYCLE_UNAVAILABLE",
            format!("Aircraft lifecycle is {}", row.lifecycle_status),
        );
    }
    if let Some(assigned_incident_id) = row.active_incident_id.as_deref() {
        block(
            "ACTIVE_INCIDENT_ASSIGNMENT",
            format!(
                "Reserved by active incident {}",
                short_operational_reference(assigned_incident_id)
            ),
        );
    }
    if let Some(run_id) = row.unfinished_mission_run_id.as_deref() {
        block(
            "UNFINISHED_MISSION_RUN",
            format!(
                "Mission run {} is unfinished",
                short_operational_reference(run_id)
            ),
        );
    }

    let last_link_observation = row
        .last_heartbeat_at_unix_ms
        .or(row.link_started_at_unix_ms);
    let link_live = row.link_status.as_deref() == Some("connected")
        && row.link_ended_at_unix_ms.is_none()
        && last_link_observation.is_some_and(|observed_at| {
            now.saturating_sub(observed_at) <= RESPONSE_SUITABILITY_LINK_MAX_AGE_MS
        });
    let connection_status = if link_live {
        "connected"
    } else if row.link_ended_at_unix_ms.is_none() && row.link_status.is_some() {
        "stale"
    } else {
        "disconnected"
    }
    .to_string();
    if !link_live {
        block(
            "AIRCRAFT_NOT_CONNECTED",
            "Aircraft has no fresh Atlas Agent link".into(),
        );
    }

    let telemetry_age_ms = row
        .telemetry_received_at_unix_ms
        .map(|received_at| now.saturating_sub(received_at).max(0));
    match telemetry_age_ms {
        None => block(
            "TELEMETRY_UNAVAILABLE",
            "No aircraft telemetry has been received".into(),
        ),
        Some(age) if age > RESPONSE_SUITABILITY_TELEMETRY_MAX_AGE_MS => block(
            "TELEMETRY_STALE",
            format!(
                "Aircraft telemetry is {:.1} seconds old",
                age as f64 / 1_000.0
            ),
        ),
        Some(_) => {}
    }

    let position = row
        .latitude
        .zip(row.longitude)
        .filter(|(latitude, longitude)| valid_response_coordinate(*latitude, *longitude));
    if position.is_none() {
        block(
            "POSITION_UNAVAILABLE",
            "Current aircraft position is unavailable".into(),
        );
    }

    let health = row
        .health_json
        .as_deref()
        .and_then(|value| serde_json::from_str::<VehicleHealth>(value).ok());
    match health.as_ref() {
        None => block(
            "PREFLIGHT_HEALTH_UNAVAILABLE",
            "PX4 preflight health is unavailable".into(),
        ),
        Some(health) => {
            if row.armed != Some(true) && !health.armable {
                block(
                    "AIRCRAFT_NOT_ARMABLE",
                    "PX4 reports that the aircraft is not armable".into(),
                );
            }
            if !health.global_position_ok {
                block(
                    "GLOBAL_POSITION_UNAVAILABLE",
                    "PX4 global position is not ready".into(),
                );
            }
            if !health.home_position_ok || row.home_position_set != Some(true) {
                block(
                    "HOME_POSITION_UNAVAILABLE",
                    "PX4 home position is not ready".into(),
                );
            }
        }
    }

    match row.battery_percent {
        None => block("BATTERY_UNAVAILABLE", "Battery state is unavailable".into()),
        Some(percent) if percent < RESPONSE_SUITABILITY_MINIMUM_BATTERY_PERCENT => block(
            "BATTERY_BELOW_MINIMUM",
            format!(
                "Battery is {:.0}%, below the {:.0}% mission-start minimum",
                percent, RESPONSE_SUITABILITY_MINIMUM_BATTERY_PERCENT
            ),
        ),
        Some(percent) if percent < RESPONSE_SUITABILITY_LOW_BATTERY_PERCENT => consider(
            "LOW_BATTERY_MARGIN",
            format!("Battery margin is limited at {:.0}%", percent),
        ),
        Some(_) => {}
    }

    let capabilities = row
        .capabilities_json
        .as_deref()
        .and_then(|value| serde_json::from_str::<Vec<String>>(value).ok())
        .unwrap_or_default();
    let mut required_capabilities = vec![
        "mission:upload",
        "mission:start",
        "mission:action:hold_at_arrival",
    ];
    if matches!(pattern, "BOUNDED_AREA_SCAN" | "BOUNDED_ORBIT") {
        required_capabilities.push("mission:action:resume_after_arrival");
    }
    if matches!(pattern, "OFFSET_OBSERVE" | "BOUNDED_ORBIT") {
        required_capabilities.push("mission:action:point_gimbal_at_incident");
        required_capabilities.push("gimbal:detected");
        required_capabilities.push("gimbal:roi");
    }
    let missing_capabilities = required_capabilities
        .into_iter()
        .filter(|required| !capabilities.iter().any(|capability| capability == required))
        .collect::<Vec<_>>();
    if !missing_capabilities.is_empty() {
        block(
            "REQUIRED_CAPABILITY_UNAVAILABLE",
            format!(
                "Required capability is unavailable: {}",
                missing_capabilities.join(", ")
            ),
        );
    }

    let distance_meters = position.map(|(latitude, longitude)| {
        response_distance_meters(
            latitude,
            longitude,
            input.target_latitude,
            input.target_longitude,
        )
    });
    let estimated_arrival_seconds = distance_meters.map(|distance| distance / input.speed_mps);

    IncidentResponseAircraftSuitabilitySnapshot {
        drone_id: row.drone_id,
        drone_name: row.drone_name,
        available: blockers.is_empty(),
        recommended: false,
        connection_status,
        battery_percent: row.battery_percent,
        telemetry_age_ms,
        distance_meters,
        estimated_arrival_seconds,
        active_incident_id: row.active_incident_id,
        unfinished_mission_run_id: row.unfinished_mission_run_id,
        blockers,
        considerations,
    }
}

fn short_operational_reference(value: &str) -> String {
    value
        .chars()
        .take(8)
        .collect::<String>()
        .to_ascii_uppercase()
}

fn valid_response_coordinate(latitude: f64, longitude: f64) -> bool {
    latitude.is_finite()
        && longitude.is_finite()
        && (-90.0..=90.0).contains(&latitude)
        && (-180.0..=180.0).contains(&longitude)
}

fn default_building_horizontal_clearance() -> f64 {
    10.0
}

fn default_building_vertical_clearance() -> f64 {
    5.0
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct ResponseTelemetryHome {
    absolute_altitude_m: Option<f64>,
}

type ResponseTelemetryContext = (
    Option<f64>,
    Option<f64>,
    Option<f64>,
    Option<f64>,
    Option<String>,
);

type ResponseDepartureTelemetryContext = (
    Option<f64>,
    Option<f64>,
    Option<f64>,
    Option<f64>,
    Option<String>,
    i64,
);

fn expanded_response_preview(
    connection: &Connection,
    incident_id: &str,
    input: &ExpandedPrepareIncidentResponseInput,
    dataset: Option<&KnownBuildingDataset>,
    now: i64,
) -> Result<
    (
        IncidentSnapshot,
        String,
        CreateMissionInput,
        IncidentResponsePlanPreview,
    ),
    String,
> {
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
            "arrivalFailurePolicy must be RETURN_TO_LAUNCH or OPERATOR_INTERVENTION".to_string(),
        );
    }
    let incident = read_incident(connection, incident_id)?;
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
    let aircraft: Option<(String, String)> = connection
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
    let telemetry: Option<ResponseTelemetryContext> = connection
        .query_row(
            "SELECT latitude, longitude, relative_altitude_m, absolute_altitude_m, home_position_json FROM vehicle_telemetry_current WHERE drone_id = ?1",
            [drone_id],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?, row.get(4)?)),
        )
        .optional()
        .map_err(|error| format!("read aircraft context for known-building assessment: {error}"))?;
    let route_start = telemetry
        .as_ref()
        .and_then(|(latitude, longitude, relative, _, _)| {
            latitude
                .zip(*longitude)
                .map(|(latitude, longitude)| RouteAssessmentStart {
                    latitude,
                    longitude,
                    relative_altitude_meters: relative.unwrap_or(0.0),
                })
        });
    let home_absolute_altitude = telemetry
        .as_ref()
        .and_then(|(_, _, relative, absolute, home)| {
            home.as_deref()
                .and_then(|value| serde_json::from_str::<ResponseTelemetryHome>(value).ok())
                .and_then(|home| home.absolute_altitude_m)
                .or_else(|| {
                    absolute
                        .zip(*relative)
                        .map(|(absolute, relative)| absolute - relative)
                })
        });
    let mut mission_input = expanded_response_mission_input(
        &incident,
        input,
        &failure_policy,
        incident_latitude,
        incident_longitude,
        now,
    )?;
    let planned = preview_ready_mission(&mission_input)?;
    let assessment = assess_known_buildings(
        dataset,
        route_start,
        &planned.waypoints,
        home_absolute_altitude,
        input.building_horizontal_clearance_meters,
        input.building_vertical_clearance_meters,
        input.known_building_override_reason.as_deref(),
    )?;
    let assessment_value = serde_json::to_value(&assessment)
        .map_err(|error| format!("serialize known-building assessment: {error}"))?;
    mission_input.params["incidentResponse"]["knownBuildingAssessment"] = assessment_value;
    let mut final_planned = preview_ready_mission(&mission_input)?;
    final_planned.metadata["incidentResponse"] = mission_input.params["incidentResponse"].clone();
    final_planned.warnings.push(assessment.statement.clone());
    final_planned
        .warnings
        .extend(assessment.limitations.iter().cloned());
    let preview = IncidentResponsePlanPreview {
        template_type: mission_input.template_type.clone(),
        pattern_type: mission_input
            .selected_pattern
            .clone()
            .unwrap_or_else(|| "DIRECT_WAYPOINTS".into()),
        generated_waypoints: final_planned.waypoints,
        actions: final_planned.actions,
        metadata: final_planned.metadata,
        validation_warnings: final_planned.warnings,
        known_building_assessment: assessment,
    };
    Ok((incident, drone_name, mission_input, preview))
}

fn expanded_response_mission_input(
    incident: &IncidentSnapshot,
    input: &ExpandedPrepareIncidentResponseInput,
    failure_policy: &str,
    incident_latitude: f64,
    incident_longitude: f64,
    now: i64,
) -> Result<CreateMissionInput, String> {
    let hold = json!({
        "actionType": "HOLD_AT_ARRIVAL",
        "triggerAfterWaypointSequence": 0,
        "failurePolicy": failure_policy,
        "maxAttempts": 3,
        "timeoutMs": 20_000,
        "retryInitialDelayMs": 2_000,
        "retryBackoffMultiplier": 2.0,
    });
    let staging_hold = json!({
        "actionType": "HOLD_AT_ARRIVAL",
        "triggerAfterWaypointSequence": 0,
        "failurePolicy": failure_policy,
        "maxAttempts": 3,
        "timeoutMs": 20_000,
        "retryInitialDelayMs": 2_000,
        "retryBackoffMultiplier": 2.0,
        "waitForOperatorDecision": true,
    });
    let gimbal_target_altitude = |required: bool| -> Result<Option<f64>, String> {
        let value = input.incident_target_altitude_amsl_meters;
        if required && value.is_none() {
            return Err(
                "incidentTargetAltitudeAmslMeters is required for Offset Observe and Bounded Orbit"
                    .to_string(),
            );
        }
        if value.is_some_and(|value| !value.is_finite() || !(-500.0..=9_000.0).contains(&value)) {
            return Err(
                "incidentTargetAltitudeAmslMeters must be between -500 and 9000".to_string(),
            );
        }
        Ok(value)
    };
    let gimbal_action = |altitude: f64| {
        json!({
            "actionType": "POINT_GIMBAL_AT_INCIDENT",
            "triggerAfterWaypointSequence": 0,
            "failurePolicy": "SKIP_OPTIONAL_AND_NOTIFY",
            "maxAttempts": 3,
            "timeoutMs": 20_000,
            "retryInitialDelayMs": 2_000,
            "retryBackoffMultiplier": 2.0,
            "latitude": incident_latitude,
            "longitude": incident_longitude,
            "altitudeAmslMeters": altitude,
        })
    };
    let resume = json!({
        "actionType": "RESUME_AFTER_ARRIVAL",
        "triggerAfterWaypointSequence": 0,
        "failurePolicy": failure_policy,
        "maxAttempts": 3,
        "timeoutMs": 20_000,
        "retryInitialDelayMs": 2_000,
        "retryBackoffMultiplier": 2.0,
    });
    let geometry = serde_json::to_value(&input.geometry)
        .map_err(|error| format!("serialize reviewed response geometry: {error}"))?;
    let base_evidence = json!({
        "incidentId": incident.id,
        "incidentRevision": incident.revision,
        "locationRevision": incident.location_revision,
        "incidentLatitude": incident_latitude,
        "incidentLongitude": incident_longitude,
        "arrivalFailurePolicy": failure_policy,
        "optionalActionFailurePolicy": "SKIP_OPTIONAL_AND_NOTIFY",
        "reviewedGeometry": geometry,
        "buildingHorizontalClearanceMeters": input.building_horizontal_clearance_meters,
        "buildingVerticalClearanceMeters": input.building_vertical_clearance_meters,
        "knownBuildingOverrideReason": input.known_building_override_reason,
        "reviewedAtUnixMs": now,
    });
    let (template_type, selected_pattern, label, params) = match &input.geometry {
        IncidentResponseGeometryInput::HoldAtStaging {
            staging_latitude,
            staging_longitude,
            altitude_meters,
            speed_mps,
        } => (
            "WAYPOINT",
            "DIRECT_WAYPOINTS",
            "Hold at staging",
            json!({
                "waypoints": [{
                    "latitude": staging_latitude,
                    "longitude": staging_longitude,
                    "altitudeMeters": altitude_meters,
                    "speedMps": speed_mps,
                }],
                "defaultAltitudeMeters": altitude_meters,
                "defaultSpeedMps": speed_mps,
                "altitudeMode": "HOME_RELATIVE",
                "returnToLaunch": false,
                "cameraMode": "FORWARD_OBLIQUE",
                "gimbal": {"pitchDegrees": -35, "yawMode": "FOLLOW_DRONE_HEADING", "stabilization": true},
                "arrivalActions": [staging_hold],
                "incidentResponse": merge_response_evidence(base_evidence, "HOLD_AT_STAGING"),
            }),
        ),
        IncidentResponseGeometryInput::OffsetObserve {
            observation_latitude,
            observation_longitude,
            altitude_meters,
            speed_mps,
        } => {
            let target_altitude = gimbal_target_altitude(true)?.expect("required above");
            (
                "WAYPOINT",
                "DIRECT_WAYPOINTS",
                "Offset observe",
                json!({
                    "waypoints": [{
                        "latitude": observation_latitude,
                        "longitude": observation_longitude,
                        "altitudeMeters": altitude_meters,
                        "speedMps": speed_mps,
                    }],
                    "defaultAltitudeMeters": altitude_meters,
                    "defaultSpeedMps": speed_mps,
                    "altitudeMode": "HOME_RELATIVE",
                    "returnToLaunch": false,
                    "cameraMode": "LOOK_AT_POINT",
                    "gimbal": {"pitchDegrees": -35, "yawMode": "LOOK_AT_POINT", "stabilization": true, "target": {"latitude": incident_latitude, "longitude": incident_longitude}},
                    "arrivalActions": [hold, gimbal_action(target_altitude)],
                    "incidentResponse": merge_response_evidence(base_evidence, "OFFSET_OBSERVE"),
                }),
            )
        }
        IncidentResponseGeometryInput::BoundedAreaScan {
            area_polygon,
            altitude_meters,
            speed_mps,
            lane_spacing_meters,
            sweep_angle_degrees,
        } => (
            "AREA_SCAN",
            "LAWN_MOWER",
            "Bounded area scan",
            json!({
                "areaPolygon": area_polygon,
                "altitudeMeters": altitude_meters,
                "speedMps": speed_mps,
                "laneSpacingMeters": lane_spacing_meters,
                "sweepAngleDegrees": sweep_angle_degrees,
                "cameraMode": "DOWNWARD_SCAN",
                "gimbal": {"pitchDegrees": -90, "yawMode": "FOLLOW_LANE_DIRECTION", "stabilization": true},
                "altitudeMode": "HOME_RELATIVE",
                "returnToLaunch": false,
                "arrivalActions": [hold, resume],
                "incidentResponse": merge_response_evidence(base_evidence, "BOUNDED_AREA_SCAN"),
            }),
        ),
        IncidentResponseGeometryInput::BoundedOrbit {
            center_latitude,
            center_longitude,
            radius_meters,
            altitude_levels_meters,
            speed_mps,
            laps_per_level,
            direction,
            max_vertical_rate_mps,
        } => {
            if altitude_levels_meters.len() != 1 {
                return Err("the first Bounded Orbit implementation requires exactly one altitude level; stepped-altitude orbit remains disabled until single-level acceptance passes".to_string());
            }
            if !radius_meters.is_finite() || !(5.0..=500.0).contains(radius_meters) {
                return Err("orbit radius must be between 5 and 500 metres".to_string());
            }
            if !(1..=10).contains(laps_per_level) {
                return Err("orbit lapsPerLevel must be between 1 and 10".to_string());
            }
            if !max_vertical_rate_mps.is_finite() || !(0.2..=5.0).contains(max_vertical_rate_mps) {
                return Err("orbit maxVerticalRateMps must be between 0.2 and 5".to_string());
            }
            let direction = direction.trim().to_ascii_uppercase();
            if !matches!(direction.as_str(), "CLOCKWISE" | "COUNTERCLOCKWISE") {
                return Err("orbit direction must be CLOCKWISE or COUNTERCLOCKWISE".to_string());
            }
            let altitude = altitude_levels_meters[0];
            let target_altitude = gimbal_target_altitude(true)?.expect("required above");
            let waypoints = orbit_waypoints(
                *center_latitude,
                *center_longitude,
                *radius_meters,
                altitude,
                *speed_mps,
                *laps_per_level,
                &direction,
            )?;
            (
                "WAYPOINT",
                "DIRECT_WAYPOINTS",
                "Bounded orbit",
                json!({
                    "waypoints": waypoints,
                    "defaultAltitudeMeters": altitude,
                    "defaultSpeedMps": speed_mps,
                    "altitudeMode": "HOME_RELATIVE",
                    "returnToLaunch": false,
                    "cameraMode": "LOOK_AT_POINT",
                    "gimbal": {"pitchDegrees": -35, "yawMode": "LOOK_AT_POINT", "stabilization": true, "target": {"latitude": incident_latitude, "longitude": incident_longitude}},
                    "arrivalActions": [hold, gimbal_action(target_altitude), resume],
                    "incidentResponse": merge_response_evidence(
                        merge_json(base_evidence, json!({
                            "orbit": {
                                "centerLatitude": center_latitude,
                                "centerLongitude": center_longitude,
                                "radiusMeters": radius_meters,
                                "altitudeReference": "HOME_RELATIVE",
                                "altitudeLevelsMeters": altitude_levels_meters,
                                "lapsPerLevel": laps_per_level,
                                "direction": direction,
                                "transition": "NONE_SINGLE_LEVEL",
                                "transitionCount": 0,
                                "maxVerticalRateMps": max_vertical_rate_mps,
                                "pointsPerLap": 24,
                            }
                        })),
                        "BOUNDED_ORBIT",
                    ),
                }),
            )
        }
    };
    Ok(CreateMissionInput {
        template_type: template_type.into(),
        name: expanded_response_mission_name(label, &incident.summary),
        description: format!(
            "Operator-reviewed {label} response for incident {} at revision {}.",
            incident.id, incident.revision
        ),
        selected_pattern: Some(selected_pattern.into()),
        params,
    })
}

fn merge_response_evidence(mut base: Value, pattern: &str) -> Value {
    base["responsePattern"] = json!(pattern);
    base
}

fn merge_json(mut base: Value, extra: Value) -> Value {
    if let (Some(base), Some(extra)) = (base.as_object_mut(), extra.as_object()) {
        base.extend(extra.clone());
    }
    base
}

fn orbit_waypoints(
    center_latitude: f64,
    center_longitude: f64,
    radius_meters: f64,
    altitude_meters: f64,
    speed_mps: f64,
    laps: u32,
    direction: &str,
) -> Result<Vec<Value>, String> {
    if !(-90.0..=90.0).contains(&center_latitude) || !(-180.0..=180.0).contains(&center_longitude) {
        return Err("orbit center must contain valid WGS84 latitude and longitude".into());
    }
    if !altitude_meters.is_finite() || !(2.0..=120.0).contains(&altitude_meters) {
        return Err("orbit altitude level must be between 2 and 120 metres".into());
    }
    if !speed_mps.is_finite() || !(0.5..=15.0).contains(&speed_mps) {
        return Err("orbit speed must be between 0.5 and 15 m/s".into());
    }
    let mut waypoints = Vec::with_capacity((laps * 24 + 1) as usize);
    let sign = if direction == "CLOCKWISE" { 1.0 } else { -1.0 };
    for step in 0..=laps * 24 {
        let bearing = sign * step as f64 / 24.0 * 360.0;
        let coordinate = offset_response_coordinate(
            GeoPoint {
                latitude: center_latitude,
                longitude: center_longitude,
            },
            bearing,
            radius_meters,
        );
        waypoints.push(json!({
            "latitude": coordinate.latitude,
            "longitude": coordinate.longitude,
            "altitudeMeters": altitude_meters,
            "speedMps": speed_mps,
        }));
    }
    Ok(waypoints)
}

fn offset_response_coordinate(
    start: GeoPoint,
    bearing_degrees: f64,
    distance_meters: f64,
) -> GeoPoint {
    const EARTH_RADIUS_M: f64 = 6_371_000.0;
    let angular_distance = distance_meters / EARTH_RADIUS_M;
    let bearing = bearing_degrees.to_radians();
    let latitude = start.latitude.to_radians();
    let longitude = start.longitude.to_radians();
    let next_latitude = (latitude.sin() * angular_distance.cos()
        + latitude.cos() * angular_distance.sin() * bearing.cos())
    .asin();
    let next_longitude = longitude
        + (bearing.sin() * angular_distance.sin() * latitude.cos())
            .atan2(angular_distance.cos() - latitude.sin() * next_latitude.sin());
    GeoPoint {
        latitude: next_latitude.to_degrees(),
        longitude: next_longitude.to_degrees(),
    }
}

fn expanded_response_mission_name(label: &str, summary: &str) -> String {
    let suffix = summary.trim();
    let candidate = if suffix.is_empty() {
        format!("Incident {label}")
    } else {
        format!("{label} · {suffix}")
    };
    candidate.chars().take(120).collect()
}

#[cfg(test)]
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
    if let Some(assessment) = mission_params
        .get("incidentResponse")
        .and_then(|value| value.get("knownBuildingAssessment"))
    {
        validate_known_building_departure_context(tx, drone_id, assessment)?;
    }
    Ok(Some(ActiveIncidentAssignment {
        id: assignment.id,
        incident_id: assignment.incident_id,
    }))
}

fn validate_known_building_departure_context(
    tx: &Transaction<'_>,
    drone_id: &str,
    assessment: &Value,
) -> Result<(), String> {
    let assessed_start = assessment
        .get("routeStart")
        .cloned()
        .map(serde_json::from_value::<Option<RouteAssessmentStart>>)
        .transpose()
        .map_err(|error| format!("decode assessed incident-response departure: {error}"))?
        .flatten()
        .ok_or_else(|| {
            "incident response upload blocked: the known-building assessment has no aircraft departure position; prepare the response again with live telemetry"
                .to_string()
        })?;
    let assessed_home = assessment
        .get("homeAbsoluteAltitudeMeters")
        .and_then(Value::as_f64);
    let telemetry: Option<ResponseDepartureTelemetryContext> = tx
        .query_row(
            "SELECT latitude, longitude, relative_altitude_m, absolute_altitude_m, home_position_json, received_at_unix_ms FROM vehicle_telemetry_current WHERE drone_id = ?1",
            [drone_id],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?, row.get(4)?, row.get(5)?)),
        )
        .optional()
        .map_err(|error| format!("read current incident-response departure: {error}"))?;
    let (latitude, longitude, relative, absolute, home, received_at) = telemetry.ok_or_else(|| {
        "incident response upload blocked: current aircraft departure telemetry is unavailable; prepare the response again"
            .to_string()
    })?;
    let age = unix_time_ms().saturating_sub(received_at);
    if !(0..=RESPONSE_DEPARTURE_MAX_AGE_MS).contains(&age) {
        return Err(
            "incident response upload blocked: aircraft departure telemetry is stale; prepare the response again with live telemetry"
                .to_string(),
        );
    }
    let (latitude, longitude) = latitude
        .zip(longitude)
        .filter(|(latitude, longitude)| {
            latitude.is_finite()
                && longitude.is_finite()
                && (-90.0..=90.0).contains(latitude)
                && (-180.0..=180.0).contains(longitude)
        })
        .ok_or_else(|| {
            "incident response upload blocked: current aircraft departure position is invalid; prepare the response again"
                .to_string()
        })?;
    let horizontal_change = response_distance_meters(
        assessed_start.latitude,
        assessed_start.longitude,
        latitude,
        longitude,
    );
    let relative = relative.unwrap_or(0.0);
    let relative_change = (relative - assessed_start.relative_altitude_meters).abs();
    let current_home = response_home_absolute_altitude(Some(relative), absolute, home.as_deref());
    let home_changed = match (assessed_home, current_home) {
        (Some(assessed), Some(current)) => {
            (assessed - current).abs() > RESPONSE_DEPARTURE_ALTITUDE_TOLERANCE_METERS
        }
        (None, None) => false,
        _ => true,
    };
    if horizontal_change > RESPONSE_DEPARTURE_HORIZONTAL_TOLERANCE_METERS
        || relative_change > RESPONSE_DEPARTURE_ALTITUDE_TOLERANCE_METERS
        || home_changed
    {
        return Err(format!(
            "incident response upload blocked: aircraft departure changed after known-building assessment (horizontal {horizontal_change:.1} m, relative altitude {relative_change:.1} m, home datum changed {home_changed}); prepare the response again"
        ));
    }
    Ok(())
}

fn response_home_absolute_altitude(
    relative: Option<f64>,
    absolute: Option<f64>,
    home: Option<&str>,
) -> Option<f64> {
    home.and_then(|value| serde_json::from_str::<ResponseTelemetryHome>(value).ok())
        .and_then(|home| home.absolute_altitude_m)
        .or_else(|| {
            absolute
                .zip(relative)
                .map(|(absolute, relative)| absolute - relative)
        })
}

fn response_distance_meters(
    start_latitude: f64,
    start_longitude: f64,
    end_latitude: f64,
    end_longitude: f64,
) -> f64 {
    const EARTH_RADIUS_M: f64 = 6_371_000.0;
    let start_latitude = start_latitude.to_radians();
    let end_latitude = end_latitude.to_radians();
    let latitude_delta = end_latitude - start_latitude;
    let longitude_delta = (end_longitude - start_longitude).to_radians();
    let haversine = (latitude_delta / 2.0).sin().powi(2)
        + start_latitude.cos() * end_latitude.cos() * (longitude_delta / 2.0).sin().powi(2);
    2.0 * EARTH_RADIUS_M * haversine.sqrt().asin()
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

pub(super) fn mark_incident_response_arrival_acknowledged(
    tx: &Transaction<'_>,
    mission_run_id: &str,
    action_execution_id: &str,
    action_sequence: u32,
    now: i64,
) -> Result<(), String> {
    let assignment: Option<(String, String, String, Option<i64>, String)> = tx
        .query_row(
            r#"
            SELECT a.id, a.incident_id, i.status, a.on_scene_at_unix_ms, m.params_json
            FROM incident_assignments a
            JOIN incidents i ON i.id = a.incident_id
            JOIN missions m ON m.id = a.mission_id
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
        .map_err(|error| {
            format!("read incident assignment for arrival acknowledgement: {error}")
        })?;
    let Some((assignment_id, incident_id, incident_status, on_scene_at, mission_params_json)) =
        assignment
    else {
        return Ok(());
    };
    let mission_params: Value = serde_json::from_str(&mission_params_json)
        .map_err(|error| format!("decode incident response arrival evidence: {error}"))?;
    let response_pattern = mission_params
        .get("incidentResponse")
        .and_then(|value| value.get("responsePattern"))
        .and_then(Value::as_str)
        .unwrap_or("OFFSET_OBSERVE");
    let hold_at_staging = response_pattern == "HOLD_AT_STAGING";
    if (!hold_at_staging && on_scene_at.is_some())
        || (hold_at_staging
            && tx
                .query_row(
                    "SELECT status = 'STAGED' FROM incident_assignments WHERE id = ?1",
                    [&assignment_id],
                    |row| row.get::<_, bool>(0),
                )
                .map_err(|error| format!("read staged assignment state: {error}"))?)
    {
        return Ok(());
    }
    let (assignment_state, event_type, message) = if hold_at_staging {
        (
            "STAGED",
            "response_staged",
            "Aircraft Hold at the reviewed staging point was acknowledged",
        )
    } else {
        (
            "ON_SCENE",
            "response_on_scene",
            "Aircraft Hold at the incident observation point was acknowledged",
        )
    };
    let changed = if hold_at_staging {
        tx.execute(
            "UPDATE incident_assignments SET status = 'STAGED' WHERE id = ?1 AND ended_at_unix_ms IS NULL",
            [&assignment_id],
        )
        .map_err(|error| format!("mark incident response staged: {error}"))?
    } else {
        tx.execute(
            "UPDATE incident_assignments SET status = 'ON_SCENE', on_scene_at_unix_ms = ?2 WHERE id = ?1 AND on_scene_at_unix_ms IS NULL AND ended_at_unix_ms IS NULL",
            params![assignment_id, now],
        )
        .map_err(|error| format!("mark incident response on scene: {error}"))?
    };
    if changed != 1 {
        return Err(
            "incident response assignment changed during arrival acknowledgement".to_string(),
        );
    }
    insert_incident_event(
        tx,
        &incident_id,
        event_type,
        &incident_status,
        message,
        &json!({
            "assignmentId": assignment_id,
            "missionRunId": mission_run_id,
            "actionExecutionId": action_execution_id,
            "actionSequence": action_sequence,
            "actionType": "HOLD_AT_ARRIVAL",
            "responsePattern": response_pattern,
            "assignmentState": assignment_state,
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
    let assignment_run_state = if assignment_status == "STAGED"
        && !terminal
        && matches!(run_state, "RUNNING" | "PAUSED")
    {
        "STAGED"
    } else if on_scene_at.is_some() && !terminal && matches!(run_state, "RUNNING" | "PAUSED") {
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
