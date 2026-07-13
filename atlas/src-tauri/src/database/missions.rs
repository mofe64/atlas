use rusqlite::{params, OptionalExtension};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use super::{unix_time_ms, LocalDatabase};

const EARTH_RADIUS_M: f64 = 6_371_000.0;
const MIN_ALTITUDE_M: f64 = 2.0;
const MAX_ALTITUDE_M: f64 = 120.0;
const MIN_SPEED_MPS: f64 = 0.5;
const MAX_SPEED_MPS: f64 = 15.0;
const MAX_TERRAIN_STATIONS: usize = 5_000;
const MAX_TERRAIN_SAMPLES_PER_STATION: usize = 5;

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct MissionTemplate {
    pub id: &'static str,
    pub name: &'static str,
    pub template_type: &'static str,
    pub description: &'static str,
    pub supported_patterns: &'static [&'static str],
    pub default_pattern: &'static str,
    pub required_params: &'static [&'static str],
    pub optional_params: &'static [&'static str],
    pub default_params: Value,
    pub version: u32,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct CreateMissionInput {
    pub template_type: String,
    pub name: String,
    #[serde(default)]
    pub description: String,
    pub selected_pattern: Option<String>,
    pub params: Value,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct MissionSnapshot {
    pub id: String,
    pub template_id: String,
    pub template_type: String,
    pub name: String,
    pub description: String,
    pub status: String,
    pub params: Value,
    pub selected_pattern: String,
    pub generated_plan_id: Option<String>,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct MissionPlanSnapshot {
    pub id: String,
    pub mission_id: String,
    pub template_type: String,
    pub pattern_type: String,
    pub status: String,
    pub generated_waypoints: Vec<MissionWaypoint>,
    pub actions: Vec<MissionAction>,
    pub metadata: Value,
    pub validation_warnings: Vec<String>,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct MissionWaypoint {
    pub sequence: u32,
    pub latitude: f64,
    pub longitude: f64,
    pub altitude_meters: f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub speed_mps: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub heading_degrees: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub hold_seconds: Option<f64>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub action_refs: Vec<u32>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct MissionAction {
    pub sequence: u32,
    pub action_type: String,
    pub params: Value,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct ApplyTerrainProfileInput {
    base_plan_id: String,
    source: TerrainSourceEvidence,
    home: TerrainSample,
    stations: Vec<TerrainProfileStation>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct TerrainSourceEvidence {
    dataset_id: String,
    display_name: String,
    encoding: String,
    zoom: u8,
    tile_size: u16,
    sampled_at_unix_ms: i64,
}

#[derive(Debug, Clone, Copy, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct TerrainSample {
    latitude: f64,
    longitude: f64,
    elevation_meters: f64,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct TerrainProfileStation {
    latitude: f64,
    longitude: f64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    source_waypoint_sequence: Option<u32>,
    samples: Vec<TerrainSample>,
}

#[derive(Debug, Clone, Copy, Deserialize)]
#[serde(rename_all = "camelCase")]
struct TerrainPlanningSettings {
    safety_margin_meters: f64,
    sample_spacing_meters: f64,
    corridor_width_meters: f64,
    max_climb_rate_mps: f64,
    max_descent_rate_mps: f64,
    max_relative_altitude_meters: f64,
}

#[derive(Debug, Clone, Copy)]
struct ExpectedTerrainStation {
    coordinate: Coordinate,
    source_waypoint_sequence: Option<u32>,
    start_waypoint_index: usize,
    end_waypoint_index: usize,
    segment_fraction: f64,
}

#[derive(Debug, Clone, Copy, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Coordinate {
    latitude: f64,
    longitude: f64,
    #[serde(default)]
    altitude_meters: Option<f64>,
    #[serde(default)]
    speed_mps: Option<f64>,
    #[serde(default)]
    heading_degrees: Option<f64>,
    #[serde(default)]
    hold_seconds: Option<f64>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct GimbalTarget {
    latitude: f64,
    longitude: f64,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct GimbalOrientation {
    pitch_degrees: f64,
    yaw_mode: String,
    #[serde(default)]
    yaw_degrees: Option<f64>,
    #[serde(default = "default_true")]
    stabilization: bool,
    #[serde(default)]
    target: Option<GimbalTarget>,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct WaypointBehavior {
    #[serde(default)]
    camera_mode: Option<String>,
    #[serde(default)]
    gimbal: Option<GimbalOrientation>,
}

#[derive(Clone, Copy)]
struct ViewDefaults {
    camera_mode: &'static str,
    pitch_degrees: f64,
    yaw_mode: &'static str,
}

const WAYPOINT_VIEW_DEFAULTS: ViewDefaults = ViewDefaults {
    camera_mode: "FORWARD_OBLIQUE",
    pitch_degrees: -35.0,
    yaw_mode: "FOLLOW_DRONE_HEADING",
};
const AREA_VIEW_DEFAULTS: ViewDefaults = ViewDefaults {
    camera_mode: "DOWNWARD_SCAN",
    pitch_degrees: -90.0,
    yaw_mode: "FOLLOW_LANE_DIRECTION",
};
const ROUTE_VIEW_DEFAULTS: ViewDefaults = ViewDefaults {
    camera_mode: "FORWARD_OBLIQUE",
    pitch_degrees: -40.0,
    yaw_mode: "FOLLOW_ROUTE_BEARING",
};

#[derive(Debug)]
struct PlannedMission {
    waypoints: Vec<MissionWaypoint>,
    actions: Vec<MissionAction>,
    metadata: Value,
    warnings: Vec<String>,
}

pub(crate) fn templates() -> Vec<MissionTemplate> {
    vec![
        MissionTemplate {
            id: "waypoint-v1",
            name: "Waypoint mission",
            template_type: "WAYPOINT",
            description: "Fly an operator-defined sequence of points.",
            supported_patterns: &["DIRECT_WAYPOINTS"],
            default_pattern: "DIRECT_WAYPOINTS",
            required_params: &["waypoints"],
            optional_params: &[
                "defaultAltitudeMeters",
                "defaultSpeedMps",
                "takeoffAltitudeMeters",
                "returnToLaunch",
                "cameraMode",
                "gimbal",
                "zoomPercent",
                "detectionClasses",
                "altitudeMode",
                "terrain",
            ],
            default_params: json!({"cameraMode": "FORWARD_OBLIQUE", "gimbal": {"pitchDegrees": -35, "yawMode": "FOLLOW_DRONE_HEADING", "stabilization": true}, "zoomPercent": 0, "returnToLaunch": false, "detectionClasses": []}),
            version: 1,
        },
        MissionTemplate {
            id: "area-scan-v1",
            name: "Area scan",
            template_type: "AREA_SCAN",
            description: "Generate a back-and-forth coverage path for a polygon.",
            supported_patterns: &["LAWN_MOWER"],
            default_pattern: "LAWN_MOWER",
            required_params: &["areaPolygon", "altitudeMeters"],
            optional_params: &[
                "speedMps",
                "laneSpacingMeters",
                "overlapPercent",
                "sweepAngleDegrees",
                "cameraMode",
                "gimbal",
                "zoomPercent",
                "detectionClasses",
                "returnToLaunch",
                "recordVideo",
                "altitudeMode",
                "terrain",
            ],
            default_params: json!({"cameraMode": "DOWNWARD_SCAN", "gimbal": {"pitchDegrees": -90, "yawMode": "FOLLOW_LANE_DIRECTION", "stabilization": true}, "zoomPercent": 0, "returnToLaunch": false, "detectionClasses": []}),
            version: 1,
        },
        MissionTemplate {
            id: "route-scan-v1",
            name: "Route scan",
            template_type: "ROUTE_SCAN",
            description: "Follow and sample an operator-defined route or corridor.",
            supported_patterns: &["ROUTE_FOLLOW"],
            default_pattern: "ROUTE_FOLLOW",
            required_params: &["route", "altitudeMeters"],
            optional_params: &[
                "speedMps",
                "corridorWidthMeters",
                "sampleSpacingMeters",
                "cameraMode",
                "gimbal",
                "zoomPercent",
                "detectionClasses",
                "returnToLaunch",
                "recordVideo",
                "altitudeMode",
                "terrain",
            ],
            default_params: json!({"cameraMode": "FORWARD_OBLIQUE", "gimbal": {"pitchDegrees": -40, "yawMode": "FOLLOW_ROUTE_BEARING", "stabilization": true}, "zoomPercent": 0, "returnToLaunch": false, "detectionClasses": []}),
            version: 1,
        },
    ]
}

impl LocalDatabase {
    pub(crate) fn create_mission(
        &self,
        input: &CreateMissionInput,
    ) -> Result<MissionSnapshot, String> {
        let (template, name, pattern) = validate_mission_input(input)?;
        let now = unix_time_ms();
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let id: String = connection
            .query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
            .map_err(|error| format!("generate mission identifier: {error}"))?;
        connection.execute(
            "INSERT INTO missions (id, template_id, template_type, name, description, status, params_json, selected_pattern, created_at_unix_ms, updated_at_unix_ms) VALUES (?1, ?2, ?3, ?4, ?5, 'DRAFT', ?6, ?7, ?8, ?8)",
            params![id, template.id, template.template_type, name, input.description.trim(), input.params.to_string(), pattern, now],
        ).map_err(|error| format!("insert mission: {error}"))?;
        read_mission(&connection, &id)
    }

    pub(crate) fn update_mission(
        &self,
        mission_id: &str,
        input: &CreateMissionInput,
    ) -> Result<MissionSnapshot, String> {
        let (template, name, pattern) = validate_mission_input(input)?;
        let now = unix_time_ms();
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let unfinished_run: bool = connection
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM mission_runs WHERE mission_id = ?1 AND completed_at_unix_ms IS NULL)",
                [mission_id],
                |row| row.get(0),
            )
            .map_err(|error| format!("check mission execution lock: {error}"))?;
        if unfinished_run {
            return Err("mission definition cannot change while a run is unfinished".to_string());
        }
        let changed = connection.execute(
            "UPDATE missions SET template_id = ?2, template_type = ?3, name = ?4, description = ?5, status = 'DRAFT', params_json = ?6, selected_pattern = ?7, generated_plan_id = NULL, updated_at_unix_ms = ?8 WHERE id = ?1",
            params![mission_id, template.id, template.template_type, name, input.description.trim(), input.params.to_string(), pattern, now],
        ).map_err(|error| format!("update mission definition: {error}"))?;
        if changed == 0 {
            return Err(format!("mission {mission_id} was not found"));
        }
        read_mission(&connection, mission_id)
    }

    pub(crate) fn missions(&self) -> Result<Vec<MissionSnapshot>, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare("SELECT id FROM missions ORDER BY updated_at_unix_ms DESC, id DESC")
            .map_err(|error| format!("prepare missions: {error}"))?;
        let ids = statement
            .query_map([], |row| row.get::<_, String>(0))
            .map_err(|error| format!("query missions: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read mission identifiers: {error}"))?;
        ids.iter().map(|id| read_mission(&connection, id)).collect()
    }

    pub(crate) fn mission(&self, id: &str) -> Result<MissionSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        read_mission(&connection, id)
    }

    pub(crate) fn plan_mission(&self, mission_id: &str) -> Result<MissionPlanSnapshot, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mission = read_mission(&connection, mission_id)?;
        let planned = plan_mission(
            &mission.template_type,
            &mission.selected_pattern,
            &mission.params,
        )?;
        persist_planned_mission(&mut connection, &mission, &planned)?;
        drop(connection);
        self.mission_plan(mission_id)
    }

    pub(crate) fn apply_mission_terrain_profile(
        &self,
        mission_id: &str,
        input: &ApplyTerrainProfileInput,
    ) -> Result<MissionPlanSnapshot, String> {
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mission = read_mission(&connection, mission_id)?;
        if mission.generated_plan_id.as_deref() != Some(input.base_plan_id.as_str()) {
            return Err(
                "terrain profile is stale: regenerate it from the mission's current base plan"
                    .to_string(),
            );
        }
        if mission.params.get("altitudeMode").and_then(Value::as_str) != Some("TERRAIN_CLEARANCE") {
            return Err(
                "terrain profile can only be applied when altitudeMode is TERRAIN_CLEARANCE"
                    .to_string(),
            );
        }
        let base_plan = read_plan(&connection, &input.base_plan_id)?;
        if base_plan.mission_id != mission_id {
            return Err("terrain profile base plan belongs to a different mission".to_string());
        }
        let planned = apply_terrain_profile(&mission.params, &base_plan, input)?;
        persist_planned_mission(&mut connection, &mission, &planned)?;
        drop(connection);
        self.mission_plan(mission_id)
    }

    pub(crate) fn mission_plan(&self, mission_id: &str) -> Result<MissionPlanSnapshot, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let plan_id: String = connection
            .query_row(
                "SELECT generated_plan_id FROM missions WHERE id = ?1",
                [mission_id],
                |row| row.get(0),
            )
            .optional()
            .map_err(|error| format!("read generated plan reference: {error}"))?
            .ok_or_else(|| format!("mission {mission_id} has no generated plan"))?;
        read_plan(&connection, &plan_id)
    }
}

fn persist_planned_mission(
    connection: &mut rusqlite::Connection,
    mission: &MissionSnapshot,
    planned: &PlannedMission,
) -> Result<String, String> {
    let now = unix_time_ms();
    let tx = connection
        .transaction()
        .map_err(|error| format!("begin mission planning transaction: {error}"))?;
    let plan_id: String = tx
        .query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate plan identifier: {error}"))?;
    tx.execute(
        "INSERT INTO mission_plans (id, mission_id, template_type, pattern_type, status, metadata_json, validation_warnings_json, created_at_unix_ms, updated_at_unix_ms) VALUES (?1, ?2, ?3, ?4, 'READY', ?5, ?6, ?7, ?7)",
        params![plan_id, mission.id, mission.template_type, mission.selected_pattern, planned.metadata.to_string(), serde_json::to_string(&planned.warnings).unwrap_or_else(|_| "[]".into()), now],
    ).map_err(|error| format!("insert mission plan: {error}"))?;
    for waypoint in &planned.waypoints {
        tx.execute("INSERT INTO mission_items (mission_plan_id, sequence, latitude, longitude, altitude_meters, speed_mps, heading_degrees, hold_seconds, action_refs_json) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)", params![plan_id, waypoint.sequence, waypoint.latitude, waypoint.longitude, waypoint.altitude_meters, waypoint.speed_mps, waypoint.heading_degrees, waypoint.hold_seconds, serde_json::to_string(&waypoint.action_refs).unwrap_or_else(|_| "[]".into())]).map_err(|error| format!("insert mission waypoint: {error}"))?;
    }
    for action in &planned.actions {
        tx.execute("INSERT INTO mission_actions (mission_plan_id, sequence, action_type, params_json) VALUES (?1, ?2, ?3, ?4)", params![plan_id, action.sequence, action.action_type, action.params.to_string()]).map_err(|error| format!("insert mission action: {error}"))?;
    }
    tx.execute("UPDATE missions SET status = 'READY', generated_plan_id = ?2, updated_at_unix_ms = ?3 WHERE id = ?1", params![mission.id, plan_id, now]).map_err(|error| format!("link generated mission plan: {error}"))?;
    tx.commit()
        .map_err(|error| format!("commit mission plan: {error}"))?;
    Ok(plan_id)
}

fn read_mission(connection: &rusqlite::Connection, id: &str) -> Result<MissionSnapshot, String> {
    connection.query_row("SELECT id, template_id, template_type, name, description, status, params_json, selected_pattern, generated_plan_id, created_at_unix_ms, updated_at_unix_ms FROM missions WHERE id = ?1", [id], |row| {
        let params_json: String = row.get(6)?;
        Ok(MissionSnapshot { id: row.get(0)?, template_id: row.get(1)?, template_type: row.get(2)?, name: row.get(3)?, description: row.get(4)?, status: row.get(5)?, params: serde_json::from_str(&params_json).unwrap_or(Value::Null), selected_pattern: row.get(7)?, generated_plan_id: row.get(8)?, created_at_unix_ms: row.get(9)?, updated_at_unix_ms: row.get(10)? })
    }).optional().map_err(|error| format!("read mission: {error}"))?.ok_or_else(|| format!("mission {id} was not found"))
}

fn read_plan(connection: &rusqlite::Connection, id: &str) -> Result<MissionPlanSnapshot, String> {
    let (mission_id, template_type, pattern_type, status, metadata_json, warnings_json, created_at, updated_at): (String, String, String, String, String, String, i64, i64) = connection.query_row("SELECT mission_id, template_type, pattern_type, status, metadata_json, validation_warnings_json, created_at_unix_ms, updated_at_unix_ms FROM mission_plans WHERE id = ?1", [id], |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?, row.get(4)?, row.get(5)?, row.get(6)?, row.get(7)?))).map_err(|error| format!("read mission plan: {error}"))?;
    let mut item_statement = connection.prepare("SELECT sequence, latitude, longitude, altitude_meters, speed_mps, heading_degrees, hold_seconds, action_refs_json FROM mission_items WHERE mission_plan_id = ?1 ORDER BY sequence").map_err(|error| format!("prepare mission items: {error}"))?;
    let generated_waypoints = item_statement
        .query_map([id], |row| {
            let refs: String = row.get(7)?;
            Ok(MissionWaypoint {
                sequence: row.get(0)?,
                latitude: row.get(1)?,
                longitude: row.get(2)?,
                altitude_meters: row.get(3)?,
                speed_mps: row.get(4)?,
                heading_degrees: row.get(5)?,
                hold_seconds: row.get(6)?,
                action_refs: serde_json::from_str(&refs).unwrap_or_default(),
            })
        })
        .map_err(|error| format!("query mission items: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read mission items: {error}"))?;
    let mut action_statement = connection.prepare("SELECT sequence, action_type, params_json FROM mission_actions WHERE mission_plan_id = ?1 ORDER BY sequence").map_err(|error| format!("prepare mission actions: {error}"))?;
    let actions = action_statement
        .query_map([id], |row| {
            let params_json: String = row.get(2)?;
            Ok(MissionAction {
                sequence: row.get(0)?,
                action_type: row.get(1)?,
                params: serde_json::from_str(&params_json).unwrap_or(Value::Null),
            })
        })
        .map_err(|error| format!("query mission actions: {error}"))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| format!("read mission actions: {error}"))?;
    Ok(MissionPlanSnapshot {
        id: id.to_string(),
        mission_id,
        template_type,
        pattern_type,
        status,
        generated_waypoints,
        actions,
        metadata: serde_json::from_str(&metadata_json).unwrap_or(Value::Null),
        validation_warnings: serde_json::from_str(&warnings_json).unwrap_or_default(),
        created_at_unix_ms: created_at,
        updated_at_unix_ms: updated_at,
    })
}

fn template_for(template_type: &str) -> Result<MissionTemplate, String> {
    templates()
        .into_iter()
        .find(|template| template.template_type == template_type)
        .ok_or_else(|| format!("unsupported mission template {template_type}"))
}

fn validate_mission_input(
    input: &CreateMissionInput,
) -> Result<(MissionTemplate, String, String), String> {
    let template = template_for(&input.template_type)?;
    let name = input.name.trim();
    if name.is_empty() || name.len() > 120 {
        return Err("mission name must contain 1 to 120 characters".to_string());
    }
    let pattern = input
        .selected_pattern
        .as_deref()
        .unwrap_or(template.default_pattern);
    validate_pattern(&template, pattern)?;
    // Planning before persistence makes invalid input a side-effect-free failure.
    plan_mission(template.template_type, pattern, &input.params)?;
    Ok((template, name.to_string(), pattern.to_string()))
}

fn validate_pattern(template: &MissionTemplate, pattern: &str) -> Result<(), String> {
    if template.supported_patterns.contains(&pattern) {
        Ok(())
    } else {
        Err(format!(
            "pattern {pattern} is not supported by {}",
            template.template_type
        ))
    }
}

fn plan_mission(
    template_type: &str,
    pattern: &str,
    params: &Value,
) -> Result<PlannedMission, String> {
    let template = template_for(template_type)?;
    validate_pattern(&template, pattern)?;
    let planned = match pattern {
        "DIRECT_WAYPOINTS" => plan_direct(params),
        "LAWN_MOWER" => plan_lawn_mower(params),
        "ROUTE_FOLLOW" => plan_route(params),
        _ => Err(format!("unsupported mission pattern {pattern}")),
    }?;
    match params.get("altitudeMode") {
        None | Some(Value::Null) => {}
        Some(Value::String(value)) if value == "HOME_RELATIVE" => {}
        Some(Value::String(value)) if value == "TERRAIN_CLEARANCE" => {
            validate_terrain_settings(terrain_planning_settings(params)?, &planned.waypoints)?;
        }
        Some(Value::String(value)) => return Err(format!("unsupported altitudeMode {value}")),
        Some(_) => return Err("altitudeMode must be a string".to_string()),
    }
    Ok(planned)
}

fn plan_direct(params: &Value) -> Result<PlannedMission, String> {
    let points: Vec<Coordinate> = parse_coordinates(params, "waypoints")?;
    let waypoint_behaviors = parse_waypoint_behaviors(params)?;
    if points.is_empty() {
        return Err("waypoints must contain at least one point".into());
    }
    let default_altitude = optional_number(params, "defaultAltitudeMeters")?;
    let default_speed = optional_number(params, "defaultSpeedMps")?;
    if let Some(value) = default_altitude {
        validate_altitude(value)?;
    }
    if let Some(value) = default_speed {
        validate_speed(value)?;
    }
    let mut waypoints = Vec::with_capacity(points.len());
    for (index, point) in points.iter().enumerate() {
        validate_coordinate(*point)?;
        let altitude = point.altitude_meters.or(default_altitude).ok_or_else(|| format!("waypoints[{index}].altitudeMeters is required when defaultAltitudeMeters is absent"))?;
        validate_altitude(altitude)?;
        let speed = point.speed_mps.or(default_speed);
        if let Some(value) = speed {
            validate_speed(value)?;
        }
        waypoints.push(to_waypoint(index, *point, altitude, speed));
    }
    let actions = common_actions(
        params,
        &waypoints,
        optional_number(params, "takeoffAltitudeMeters")?,
        WAYPOINT_VIEW_DEFAULTS,
        Some(&waypoint_behaviors),
    )?;
    Ok(PlannedMission {
        metadata: distance_metadata(&waypoints),
        waypoints,
        actions,
        warnings: vec![],
    })
}

fn plan_lawn_mower(params: &Value) -> Result<PlannedMission, String> {
    let polygon: Vec<Coordinate> = parse_coordinates(params, "areaPolygon")?;
    if polygon.len() < 3 {
        return Err("areaPolygon must contain at least three points".into());
    }
    for point in &polygon {
        validate_coordinate(*point)?;
    }
    let altitude = required_number(params, "altitudeMeters")?;
    validate_altitude(altitude)?;
    let speed = optional_number(params, "speedMps")?;
    if let Some(value) = speed {
        validate_speed(value)?;
    }
    let overlap = optional_number(params, "overlapPercent")?.unwrap_or(30.0);
    if !(0.0..90.0).contains(&overlap) {
        return Err("overlapPercent must be at least 0 and less than 90".into());
    }
    let spacing = optional_number(params, "laneSpacingMeters")?
        .unwrap_or_else(|| (altitude * (1.0 - overlap / 100.0)).max(5.0));
    if !(1.0..=500.0).contains(&spacing) {
        return Err("laneSpacingMeters must be between 1 and 500".into());
    }
    let sweep = optional_number(params, "sweepAngleDegrees")?.unwrap_or(0.0);
    let origin = polygon[0];
    let angle = sweep.to_radians();
    let local: Vec<(f64, f64)> = polygon
        .iter()
        .map(|point| rotate(to_local(origin, *point), -angle))
        .collect();
    let min_y = local
        .iter()
        .map(|point| point.1)
        .fold(f64::INFINITY, f64::min);
    let max_y = local
        .iter()
        .map(|point| point.1)
        .fold(f64::NEG_INFINITY, f64::max);
    let mut generated = Vec::new();
    let mut lane = 0usize;
    let mut y = min_y;
    while y <= max_y + 0.001 {
        let mut intersections = horizontal_intersections(&local, y);
        intersections.sort_by(f64::total_cmp);
        for pair in intersections.chunks_exact(2) {
            let endpoints = if lane % 2 == 0 {
                [pair[0], pair[1]]
            } else {
                [pair[1], pair[0]]
            };
            for x in endpoints {
                let coordinate = from_local(origin, rotate((x, y), angle));
                generated.push(to_waypoint(generated.len(), coordinate, altitude, speed));
            }
            lane += 1;
        }
        y += spacing;
    }
    if generated.is_empty() {
        return Err("areaPolygon is too small for the requested lane spacing".into());
    }
    let actions = common_actions(params, &generated, Some(altitude), AREA_VIEW_DEFAULTS, None)?;
    let mut metadata = distance_metadata(&generated);
    metadata["laneSpacingMeters"] = json!(spacing);
    metadata["sweepAngleDegrees"] = json!(sweep);
    metadata["coverageMethod"] = json!("local tangent-plane scanline clipping");
    Ok(PlannedMission { waypoints: generated, actions, metadata, warnings: vec!["Coverage uses a local flat-earth approximation; review plans spanning large areas or crossing the antimeridian.".into()] })
}

fn plan_route(params: &Value) -> Result<PlannedMission, String> {
    let route: Vec<Coordinate> = parse_coordinates(params, "route")?;
    if route.len() < 2 {
        return Err("route must contain at least two points".into());
    }
    for point in &route {
        validate_coordinate(*point)?;
    }
    let altitude = required_number(params, "altitudeMeters")?;
    validate_altitude(altitude)?;
    let speed = optional_number(params, "speedMps")?;
    if let Some(value) = speed {
        validate_speed(value)?;
    }
    let spacing = optional_number(params, "sampleSpacingMeters")?;
    if let Some(value) = spacing {
        if !(1.0..=10_000.0).contains(&value) {
            return Err("sampleSpacingMeters must be between 1 and 10000".into());
        }
    }
    let mut sampled = vec![route[0]];
    for segment in route.windows(2) {
        let distance = haversine(segment[0], segment[1]);
        let divisions = spacing
            .map(|value| (distance / value).ceil() as usize)
            .unwrap_or(1)
            .max(1);
        for step in 1..=divisions {
            let fraction = step as f64 / divisions as f64;
            sampled.push(Coordinate {
                latitude: segment[0].latitude
                    + (segment[1].latitude - segment[0].latitude) * fraction,
                longitude: segment[0].longitude
                    + (segment[1].longitude - segment[0].longitude) * fraction,
                altitude_meters: None,
                speed_mps: None,
                heading_degrees: None,
                hold_seconds: None,
            });
        }
    }
    let waypoints: Vec<_> = sampled
        .iter()
        .enumerate()
        .map(|(index, point)| to_waypoint(index, *point, altitude, speed))
        .collect();
    let actions = common_actions(
        params,
        &waypoints,
        Some(altitude),
        ROUTE_VIEW_DEFAULTS,
        None,
    )?;
    let mut metadata = distance_metadata(&waypoints);
    if let Some(value) = spacing {
        metadata["sampleSpacingMeters"] = json!(value);
    }
    if let Some(value) = optional_number(params, "corridorWidthMeters")? {
        if value <= 0.0 {
            return Err("corridorWidthMeters must be greater than 0".into());
        }
        metadata["corridorWidthMeters"] = json!(value);
    }
    Ok(PlannedMission {
        waypoints,
        actions,
        metadata,
        warnings: vec![],
    })
}

fn apply_terrain_profile(
    params: &Value,
    base_plan: &MissionPlanSnapshot,
    input: &ApplyTerrainProfileInput,
) -> Result<PlannedMission, String> {
    let settings = terrain_planning_settings(params)?;
    validate_terrain_settings(settings, &base_plan.generated_waypoints)?;
    validate_terrain_source(&input.source)?;
    validate_terrain_sample(input.home, "home")?;
    if input.stations.is_empty() || input.stations.len() > MAX_TERRAIN_STATIONS {
        return Err(format!(
            "terrain profile must contain 1 to {MAX_TERRAIN_STATIONS} stations"
        ));
    }
    let expected = expected_terrain_stations(
        &base_plan.generated_waypoints,
        settings.sample_spacing_meters,
    );
    if input.stations.len() != expected.len() {
        return Err(format!(
            "terrain profile geometry mismatch: expected {} stations from the base plan, received {}",
            expected.len(),
            input.stations.len()
        ));
    }

    let mut station_terrain = Vec::with_capacity(expected.len());
    let mut sample_count = 0usize;
    for (index, (station, expected_station)) in
        input.stations.iter().zip(expected.iter()).enumerate()
    {
        let supplied = coordinate(station.latitude, station.longitude);
        validate_coordinate(supplied)?;
        if haversine(supplied, expected_station.coordinate) > 1.0 {
            return Err(format!(
                "terrain station {index} does not match the route generated by the native planner"
            ));
        }
        if station.source_waypoint_sequence != expected_station.source_waypoint_sequence {
            return Err(format!(
                "terrain station {index} has an invalid source waypoint reference"
            ));
        }
        if station.samples.is_empty() || station.samples.len() > MAX_TERRAIN_SAMPLES_PER_STATION {
            return Err(format!(
                "terrain station {index} must contain 1 to {MAX_TERRAIN_SAMPLES_PER_STATION} DEM samples"
            ));
        }
        let mut maximum_elevation = f64::NEG_INFINITY;
        for (sample_index, sample) in station.samples.iter().enumerate() {
            validate_terrain_sample(*sample, &format!("station {index} sample {sample_index}"))?;
            let distance = haversine(supplied, coordinate(sample.latitude, sample.longitude));
            if distance > settings.corridor_width_meters / 2.0 + 2.0 {
                return Err(format!(
                    "terrain station {index} contains a sample outside the configured corridor"
                ));
            }
            maximum_elevation = maximum_elevation.max(sample.elevation_meters);
        }
        for required_sample in
            required_corridor_samples(&expected, index, settings.corridor_width_meters)
        {
            if !station.samples.iter().any(|sample| {
                haversine(
                    required_sample,
                    coordinate(sample.latitude, sample.longitude),
                ) <= 2.0
            }) {
                return Err(format!(
                    "terrain station {index} is missing a required centreline or corridor-edge DEM sample"
                ));
            }
        }
        sample_count += station.samples.len();
        station_terrain.push(maximum_elevation);
    }

    let mut raw_altitudes = Vec::with_capacity(expected.len());
    for (index, station) in expected.iter().enumerate() {
        let start = &base_plan.generated_waypoints[station.start_waypoint_index];
        let end = &base_plan.generated_waypoints[station.end_waypoint_index];
        let clearance = start.altitude_meters
            + (end.altitude_meters - start.altitude_meters) * station.segment_fraction;
        let altitude = (station_terrain[index] - input.home.elevation_meters
            + clearance
            + settings.safety_margin_meters)
            .max(MIN_ALTITUDE_M);
        if altitude > settings.max_relative_altitude_meters {
            return Err(format!(
                "terrain at station {} requires {:.1} m relative altitude, above the configured {:.1} m ceiling",
                index + 1,
                altitude,
                settings.max_relative_altitude_meters
            ));
        }
        raw_altitudes.push(altitude);
    }

    let planned_altitudes = smooth_terrain_altitudes(
        &expected,
        &base_plan.generated_waypoints,
        &raw_altitudes,
        settings,
    )?;
    let mut waypoints = Vec::with_capacity(expected.len());
    for (index, station) in expected.iter().enumerate() {
        let source_waypoint = station.source_waypoint_sequence.and_then(|sequence| {
            base_plan
                .generated_waypoints
                .iter()
                .find(|waypoint| waypoint.sequence == sequence)
        });
        let segment_waypoint = &base_plan.generated_waypoints[station.end_waypoint_index];
        waypoints.push(MissionWaypoint {
            sequence: index as u32,
            latitude: station.coordinate.latitude,
            longitude: station.coordinate.longitude,
            altitude_meters: round_tenth(planned_altitudes[index]),
            speed_mps: segment_waypoint.speed_mps,
            heading_degrees: source_waypoint.and_then(|waypoint| waypoint.heading_degrees),
            hold_seconds: source_waypoint.and_then(|waypoint| waypoint.hold_seconds),
            action_refs: vec![],
        });
    }
    let actions = terrain_actions(&base_plan.actions, &waypoints, &expected)?;
    let minimum_terrain = station_terrain
        .iter()
        .copied()
        .fold(f64::INFINITY, f64::min);
    let maximum_terrain = station_terrain
        .iter()
        .copied()
        .fold(f64::NEG_INFINITY, f64::max);
    let minimum_altitude = planned_altitudes
        .iter()
        .copied()
        .fold(f64::INFINITY, f64::min);
    let maximum_altitude = planned_altitudes
        .iter()
        .copied()
        .fold(f64::NEG_INFINITY, f64::max);
    let minimum_clearance = base_plan
        .generated_waypoints
        .iter()
        .map(|waypoint| waypoint.altitude_meters)
        .fold(f64::INFINITY, f64::min);
    let profile_points: Vec<Value> = expected
        .iter()
        .enumerate()
        .map(|(index, _)| {
            json!({
                "sequence": index,
                "groundRelativeAltitudeMeters": round_tenth(station_terrain[index] - input.home.elevation_meters),
                "plannedRelativeAltitudeMeters": round_tenth(planned_altitudes[index]),
            })
        })
        .collect();
    let mut metadata = base_plan.metadata.clone();
    let generated_metadata = distance_metadata(&waypoints);
    metadata["waypointCount"] = generated_metadata["waypointCount"].clone();
    metadata["estimatedDistanceMeters"] = generated_metadata["estimatedDistanceMeters"].clone();
    metadata["altitudeMode"] = json!("TERRAIN_CLEARANCE");
    metadata["basePlanId"] = json!(input.base_plan_id);
    metadata["terrainProfile"] = json!({
        "datasetId": input.source.dataset_id,
        "displayName": input.source.display_name,
        "encoding": input.source.encoding,
        "zoom": input.source.zoom,
        "tileSize": input.source.tile_size,
        "sampledAtUnixMs": input.source.sampled_at_unix_ms,
        "homeLatitude": input.home.latitude,
        "homeLongitude": input.home.longitude,
        "homeElevationMeters": input.home.elevation_meters,
        "stationCount": expected.len(),
        "sampleCount": sample_count,
        "minimumTerrainElevationMeters": round_tenth(minimum_terrain),
        "maximumTerrainElevationMeters": round_tenth(maximum_terrain),
        "minimumRelativeAltitudeMeters": round_tenth(minimum_altitude),
        "maximumRelativeAltitudeMeters": round_tenth(maximum_altitude),
        "minimumClearanceMeters": round_tenth(minimum_clearance),
        "safetyMarginMeters": settings.safety_margin_meters,
        "sampleSpacingMeters": settings.sample_spacing_meters,
        "corridorWidthMeters": settings.corridor_width_meters,
        "maxClimbRateMps": settings.max_climb_rate_mps,
        "maxDescentRateMps": settings.max_descent_rate_mps,
        "maxRelativeAltitudeMeters": settings.max_relative_altitude_meters,
        "profilePoints": profile_points,
    });
    metadata["terrainProfileEvidence"] = serde_json::to_value(input)
        .map_err(|error| format!("serialize terrain evidence: {error}"))?;
    let mut warnings = base_plan.validation_warnings.clone();
    warnings.push("Terrain clearance is a precomputed DEM profile, not live terrain following. Replan after changing the route, home position, DEM source, or safety settings.".to_string());
    Ok(PlannedMission {
        waypoints,
        actions,
        metadata,
        warnings,
    })
}

fn terrain_planning_settings(params: &Value) -> Result<TerrainPlanningSettings, String> {
    let value = params
        .get("terrain")
        .ok_or_else(|| "terrain settings are required for TERRAIN_CLEARANCE".to_string())?;
    serde_json::from_value(value.clone())
        .map_err(|error| format!("terrain settings are invalid: {error}"))
}

fn validate_terrain_settings(
    settings: TerrainPlanningSettings,
    base_waypoints: &[MissionWaypoint],
) -> Result<(), String> {
    if !(0.0..=100.0).contains(&settings.safety_margin_meters) {
        return Err("terrain.safetyMarginMeters must be between 0 and 100".to_string());
    }
    if !(5.0..=200.0).contains(&settings.sample_spacing_meters) {
        return Err("terrain.sampleSpacingMeters must be between 5 and 200".to_string());
    }
    if !(0.0..=5_000.0).contains(&settings.corridor_width_meters) {
        return Err("terrain.corridorWidthMeters must be between 0 and 5000".to_string());
    }
    if !(0.2..=10.0).contains(&settings.max_climb_rate_mps)
        || !(0.2..=10.0).contains(&settings.max_descent_rate_mps)
    {
        return Err("terrain climb and descent rates must be between 0.2 and 10 m/s".to_string());
    }
    if !(20.0..=1_000.0).contains(&settings.max_relative_altitude_meters) {
        return Err("terrain.maxRelativeAltitudeMeters must be between 20 and 1000".to_string());
    }
    if base_waypoints
        .iter()
        .any(|waypoint| waypoint.altitude_meters + settings.safety_margin_meters > 120.0)
    {
        return Err(
            "requested ground clearance plus terrain safety margin must not exceed 120 metres"
                .to_string(),
        );
    }
    Ok(())
}

fn validate_terrain_source(source: &TerrainSourceEvidence) -> Result<(), String> {
    if source.dataset_id.trim().is_empty()
        || source.dataset_id.len() > 120
        || source.display_name.trim().is_empty()
        || source.display_name.len() > 160
    {
        return Err("terrain source identity is missing or too long".to_string());
    }
    if !matches!(source.encoding.as_str(), "terrarium" | "mapbox") {
        return Err("terrain source encoding must be terrarium or mapbox".to_string());
    }
    if source.zoom > 22 || !matches!(source.tile_size, 256 | 512) {
        return Err("terrain source zoom or tile size is unsupported".to_string());
    }
    if source.sampled_at_unix_ms <= 0 || source.sampled_at_unix_ms > unix_time_ms() + 300_000 {
        return Err("terrain source sampledAtUnixMs is invalid".to_string());
    }
    Ok(())
}

fn validate_terrain_sample(sample: TerrainSample, label: &str) -> Result<(), String> {
    validate_coordinate(coordinate(sample.latitude, sample.longitude))?;
    if !sample.elevation_meters.is_finite()
        || !(-500.0..=9_000.0).contains(&sample.elevation_meters)
    {
        return Err(format!(
            "terrain {label} elevation must be a finite value between -500 and 9000 metres AMSL"
        ));
    }
    Ok(())
}

fn expected_terrain_stations(
    waypoints: &[MissionWaypoint],
    spacing_meters: f64,
) -> Vec<ExpectedTerrainStation> {
    if waypoints.is_empty() {
        return vec![];
    }
    let mut stations = vec![ExpectedTerrainStation {
        coordinate: waypoint_coordinate(&waypoints[0]),
        source_waypoint_sequence: Some(waypoints[0].sequence),
        start_waypoint_index: 0,
        end_waypoint_index: 0,
        segment_fraction: 0.0,
    }];
    for end_index in 1..waypoints.len() {
        let start_index = end_index - 1;
        let start = waypoint_coordinate(&waypoints[start_index]);
        let end = waypoint_coordinate(&waypoints[end_index]);
        let divisions = ((haversine(start, end) / spacing_meters).ceil() as usize).max(1);
        for step in 1..=divisions {
            let fraction = step as f64 / divisions as f64;
            stations.push(ExpectedTerrainStation {
                coordinate: coordinate(
                    start.latitude + (end.latitude - start.latitude) * fraction,
                    start.longitude + (end.longitude - start.longitude) * fraction,
                ),
                source_waypoint_sequence: (step == divisions)
                    .then_some(waypoints[end_index].sequence),
                start_waypoint_index: start_index,
                end_waypoint_index: end_index,
                segment_fraction: fraction,
            });
        }
    }
    stations
}

fn required_corridor_samples(
    stations: &[ExpectedTerrainStation],
    index: usize,
    corridor_width_meters: f64,
) -> Vec<Coordinate> {
    let center = stations[index].coordinate;
    if corridor_width_meters <= 1.0 || stations.len() < 2 {
        return vec![center];
    }
    let bearing = if index == 0 {
        bearing_degrees(stations[0].coordinate, stations[1].coordinate)
    } else if index == stations.len() - 1 {
        bearing_degrees(stations[index - 1].coordinate, center)
    } else {
        bearing_degrees(
            stations[index - 1].coordinate,
            stations[index + 1].coordinate,
        )
    };
    let half_width = corridor_width_meters / 2.0;
    vec![
        offset_coordinate(center, bearing - 90.0, half_width),
        center,
        offset_coordinate(center, bearing + 90.0, half_width),
    ]
}

fn bearing_degrees(start: Coordinate, end: Coordinate) -> f64 {
    let latitude_1 = start.latitude.to_radians();
    let latitude_2 = end.latitude.to_radians();
    let longitude_delta = (end.longitude - start.longitude).to_radians();
    let y = longitude_delta.sin() * latitude_2.cos();
    let x = latitude_1.cos() * latitude_2.sin()
        - latitude_1.sin() * latitude_2.cos() * longitude_delta.cos();
    y.atan2(x).to_degrees().rem_euclid(360.0)
}

fn offset_coordinate(start: Coordinate, bearing_degrees: f64, distance_meters: f64) -> Coordinate {
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
    coordinate(next_latitude.to_degrees(), next_longitude.to_degrees())
}

fn smooth_terrain_altitudes(
    stations: &[ExpectedTerrainStation],
    base_waypoints: &[MissionWaypoint],
    raw_altitudes: &[f64],
    settings: TerrainPlanningSettings,
) -> Result<Vec<f64>, String> {
    let mut altitudes = raw_altitudes.to_vec();
    for index in (0..altitudes.len().saturating_sub(1)).rev() {
        let distance = haversine(stations[index].coordinate, stations[index + 1].coordinate);
        let speed = base_waypoints[stations[index + 1].end_waypoint_index]
            .speed_mps
            .unwrap_or(5.0)
            .max(MIN_SPEED_MPS);
        let climb_capacity = settings.max_climb_rate_mps * distance / speed;
        altitudes[index] = altitudes[index].max(altitudes[index + 1] - climb_capacity);
    }
    for index in 1..altitudes.len() {
        let distance = haversine(stations[index - 1].coordinate, stations[index].coordinate);
        let speed = base_waypoints[stations[index].end_waypoint_index]
            .speed_mps
            .unwrap_or(5.0)
            .max(MIN_SPEED_MPS);
        let descent_capacity = settings.max_descent_rate_mps * distance / speed;
        altitudes[index] = altitudes[index].max(altitudes[index - 1] - descent_capacity);
    }
    if let Some((index, altitude)) = altitudes
        .iter()
        .enumerate()
        .find(|(_, altitude)| **altitude > settings.max_relative_altitude_meters)
    {
        return Err(format!(
            "climb/descent smoothing requires {:.1} m at terrain station {}, above the configured {:.1} m ceiling",
            altitude,
            index + 1,
            settings.max_relative_altitude_meters
        ));
    }
    Ok(altitudes)
}

fn terrain_actions(
    base_actions: &[MissionAction],
    waypoints: &[MissionWaypoint],
    stations: &[ExpectedTerrainStation],
) -> Result<Vec<MissionAction>, String> {
    let first_navigation = base_actions
        .iter()
        .position(|action| action.action_type == "NAVIGATE_TO")
        .ok_or_else(|| "base plan has no navigation actions".to_string())?;
    let last_waypoint_action = base_actions
        .iter()
        .rposition(|action| {
            action.action_type == "NAVIGATE_TO"
                || action
                    .params
                    .get("waypointSequence")
                    .and_then(Value::as_u64)
                    .is_some()
        })
        .ok_or_else(|| "base plan has no waypoint action boundary".to_string())?;
    let mut actions = Vec::new();
    for action in &base_actions[..first_navigation] {
        let mut action = action.clone();
        if action.action_type == "TAKEOFF" {
            action.params["altitudeMeters"] = json!(waypoints[0].altitude_meters);
        }
        push_resequenced_action(&mut actions, action);
    }
    for (index, station) in stations.iter().enumerate() {
        push_resequenced_action(
            &mut actions,
            MissionAction {
                sequence: 0,
                action_type: "NAVIGATE_TO".to_string(),
                params: json!({"waypointSequence": index}),
            },
        );
        if let Some(source_sequence) = station.source_waypoint_sequence {
            for action in base_actions.iter().filter(|action| {
                action.action_type != "NAVIGATE_TO"
                    && action
                        .params
                        .get("waypointSequence")
                        .and_then(Value::as_u64)
                        == Some(source_sequence as u64)
            }) {
                let mut action = action.clone();
                action.params["waypointSequence"] = json!(index);
                push_resequenced_action(&mut actions, action);
            }
        }
    }
    for action in &base_actions[last_waypoint_action + 1..] {
        push_resequenced_action(&mut actions, action.clone());
    }
    Ok(actions)
}

fn push_resequenced_action(actions: &mut Vec<MissionAction>, mut action: MissionAction) {
    action.sequence = actions.len() as u32;
    actions.push(action);
}

fn coordinate(latitude: f64, longitude: f64) -> Coordinate {
    Coordinate {
        latitude,
        longitude,
        altitude_meters: None,
        speed_mps: None,
        heading_degrees: None,
        hold_seconds: None,
    }
}

fn waypoint_coordinate(waypoint: &MissionWaypoint) -> Coordinate {
    coordinate(waypoint.latitude, waypoint.longitude)
}

fn round_tenth(value: f64) -> f64 {
    (value * 10.0).round() / 10.0
}

fn common_actions(
    params: &Value,
    waypoints: &[MissionWaypoint],
    takeoff_altitude: Option<f64>,
    view_defaults: ViewDefaults,
    waypoint_behaviors: Option<&[WaypointBehavior]>,
) -> Result<Vec<MissionAction>, String> {
    if let Some(value) = takeoff_altitude {
        validate_altitude(value)?;
    }
    let mut actions = Vec::new();
    let mut push = |action_type: &str, values: Value| {
        let sequence = actions.len() as u32;
        actions.push(MissionAction {
            sequence,
            action_type: action_type.into(),
            params: values,
        });
    };
    if let Some(altitude) = takeoff_altitude {
        push("TAKEOFF", json!({"altitudeMeters": altitude}));
    }
    if let Some(speed) =
        optional_number(params, "speedMps")?.or(optional_number(params, "defaultSpeedMps")?)
    {
        push("SET_SPEED", json!({"speedMps": speed}));
    }
    let camera_mode = selected_camera_mode(params, view_defaults.camera_mode)?;
    validate_camera_mode(camera_mode)?;
    push("SET_CAMERA_MODE", json!({"cameraMode": camera_mode}));
    let zoom_percent = optional_number(params, "zoomPercent")?.unwrap_or(0.0);
    if !(0.0..=100.0).contains(&zoom_percent) {
        return Err("zoomPercent must be between 0 and 100".into());
    }
    push("SET_CAMERA_ZOOM", json!({"zoomPercent": zoom_percent}));
    let gimbal = mission_gimbal(params, view_defaults)?;
    push(
        "SET_GIMBAL_ORIENTATION",
        gimbal_action_params(&gimbal, None),
    );
    let record = params
        .get("recordVideo")
        .and_then(Value::as_bool)
        .unwrap_or(false);
    if record {
        push("START_RECORDING", json!({}));
    }
    let detections = params
        .get("detectionClasses")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    if !detections.is_empty() {
        push("START_PERCEPTION", json!({"detectionClasses": detections}));
    }
    for (index, waypoint) in waypoints.iter().enumerate() {
        push(
            "NAVIGATE_TO",
            json!({"waypointSequence": waypoint.sequence}),
        );
        if let Some(behavior) = waypoint_behaviors.and_then(|values| values.get(index)) {
            if let Some(mode) = behavior.camera_mode.as_deref() {
                validate_camera_mode(mode)?;
                push(
                    "SET_CAMERA_MODE",
                    json!({"cameraMode": mode, "waypointSequence": waypoint.sequence}),
                );
            }
            if let Some(orientation) = behavior.gimbal.as_ref() {
                validate_gimbal(orientation)?;
                push(
                    "SET_GIMBAL_ORIENTATION",
                    gimbal_action_params(orientation, Some(waypoint.sequence)),
                );
            }
        }
    }
    if !detections.is_empty() {
        push("STOP_PERCEPTION", json!({}));
    }
    if record {
        push("STOP_RECORDING", json!({}));
    }
    if params
        .get("returnToLaunch")
        .and_then(Value::as_bool)
        .unwrap_or(false)
    {
        push("RETURN_TO_LAUNCH", json!({}));
    }
    Ok(actions)
}

fn selected_camera_mode<'a>(params: &'a Value, default: &'a str) -> Result<&'a str, String> {
    match params.get("cameraMode") {
        None | Some(Value::Null) => Ok(default),
        Some(Value::String(value)) => Ok(value),
        Some(_) => Err("cameraMode must be a string".to_string()),
    }
}

fn mission_gimbal(params: &Value, defaults: ViewDefaults) -> Result<GimbalOrientation, String> {
    let orientation = match params.get("gimbal") {
        None | Some(Value::Null) => GimbalOrientation {
            pitch_degrees: defaults.pitch_degrees,
            yaw_mode: defaults.yaw_mode.to_string(),
            yaw_degrees: None,
            stabilization: true,
            target: None,
        },
        Some(value) => serde_json::from_value(value.clone())
            .map_err(|error| format!("gimbal parameters are invalid: {error}"))?,
    };
    validate_gimbal(&orientation)?;
    Ok(orientation)
}

fn parse_waypoint_behaviors(params: &Value) -> Result<Vec<WaypointBehavior>, String> {
    let values = params
        .get("waypoints")
        .ok_or_else(|| "waypoints is required".to_string())?
        .clone();
    serde_json::from_value(values)
        .map_err(|error| format!("waypoint camera/gimbal parameters are invalid: {error}"))
}

fn validate_camera_mode(mode: &str) -> Result<(), String> {
    if matches!(
        mode,
        "FORWARD_OBLIQUE"
            | "DOWNWARD_SCAN"
            | "DOWNWARD_OBLIQUE_SCAN"
            | "LOOK_AT_POINT"
            | "FIXED_ANGLE"
    ) {
        Ok(())
    } else {
        Err(format!("unsupported cameraMode {mode}"))
    }
}

fn validate_gimbal(orientation: &GimbalOrientation) -> Result<(), String> {
    if !(-90.0..=30.0).contains(&orientation.pitch_degrees) {
        return Err("gimbal.pitchDegrees must be between -90 and 30".to_string());
    }
    if !matches!(
        orientation.yaw_mode.as_str(),
        "FOLLOW_DRONE_HEADING"
            | "FOLLOW_LANE_DIRECTION"
            | "FOLLOW_ROUTE_BEARING"
            | "LOCKED_TO_ROUTE"
            | "LOOK_AT_POINT"
            | "FIXED_ANGLE"
    ) {
        return Err(format!(
            "unsupported gimbal.yawMode {}",
            orientation.yaw_mode
        ));
    }
    if let Some(yaw) = orientation.yaw_degrees {
        if !(-180.0..=180.0).contains(&yaw) {
            return Err("gimbal.yawDegrees must be between -180 and 180".to_string());
        }
    }
    if let Some(target) = orientation.target.as_ref() {
        if !(-90.0..=90.0).contains(&target.latitude)
            || !(-180.0..=180.0).contains(&target.longitude)
        {
            return Err("gimbal target must contain valid latitude and longitude".to_string());
        }
    }
    if orientation.yaw_mode == "LOOK_AT_POINT" && orientation.target.is_none() {
        return Err("LOOK_AT_POINT requires gimbal.target".to_string());
    }
    Ok(())
}

fn gimbal_action_params(orientation: &GimbalOrientation, waypoint_sequence: Option<u32>) -> Value {
    let mut value = serde_json::to_value(orientation).unwrap_or_else(|_| json!({}));
    if let (Some(sequence), Some(object)) = (waypoint_sequence, value.as_object_mut()) {
        object.insert("waypointSequence".to_string(), json!(sequence));
    }
    value
}

fn default_true() -> bool {
    true
}

fn parse_coordinates(params: &Value, key: &str) -> Result<Vec<Coordinate>, String> {
    let value = params
        .get(key)
        .ok_or_else(|| format!("{key} is required"))?
        .clone();
    serde_json::from_value(value)
        .map_err(|error| format!("{key} must be an array of coordinates: {error}"))
}
fn required_number(params: &Value, key: &str) -> Result<f64, String> {
    optional_number(params, key)?.ok_or_else(|| format!("{key} is required"))
}
fn optional_number(params: &Value, key: &str) -> Result<Option<f64>, String> {
    match params.get(key) {
        None | Some(Value::Null) => Ok(None),
        Some(value) => value
            .as_f64()
            .map(Some)
            .ok_or_else(|| format!("{key} must be a number")),
    }
}
fn validate_coordinate(point: Coordinate) -> Result<(), String> {
    if !(-90.0..=90.0).contains(&point.latitude) {
        return Err(format!("latitude {} is outside -90..90", point.latitude));
    }
    if !(-180.0..=180.0).contains(&point.longitude) {
        return Err(format!(
            "longitude {} is outside -180..180",
            point.longitude
        ));
    }
    Ok(())
}
fn validate_altitude(value: f64) -> Result<(), String> {
    if (MIN_ALTITUDE_M..=MAX_ALTITUDE_M).contains(&value) {
        Ok(())
    } else {
        Err(format!(
            "altitudeMeters must be between {MIN_ALTITUDE_M} and {MAX_ALTITUDE_M}"
        ))
    }
}
fn validate_speed(value: f64) -> Result<(), String> {
    if (MIN_SPEED_MPS..=MAX_SPEED_MPS).contains(&value) {
        Ok(())
    } else {
        Err(format!(
            "speed must be between {MIN_SPEED_MPS} and {MAX_SPEED_MPS} m/s"
        ))
    }
}
fn to_waypoint(
    index: usize,
    point: Coordinate,
    altitude: f64,
    speed: Option<f64>,
) -> MissionWaypoint {
    MissionWaypoint {
        sequence: index as u32,
        latitude: point.latitude,
        longitude: point.longitude,
        altitude_meters: altitude,
        speed_mps: speed,
        heading_degrees: point.heading_degrees,
        hold_seconds: point.hold_seconds,
        action_refs: vec![],
    }
}
fn haversine(a: Coordinate, b: Coordinate) -> f64 {
    let lat1 = a.latitude.to_radians();
    let lat2 = b.latitude.to_radians();
    let dlat = lat2 - lat1;
    let dlon = (b.longitude - a.longitude).to_radians();
    let h = (dlat / 2.0).sin().powi(2) + lat1.cos() * lat2.cos() * (dlon / 2.0).sin().powi(2);
    2.0 * EARTH_RADIUS_M * h.sqrt().asin()
}
fn distance_metadata(waypoints: &[MissionWaypoint]) -> Value {
    let distance: f64 = waypoints
        .windows(2)
        .map(|pair| {
            haversine(
                Coordinate {
                    latitude: pair[0].latitude,
                    longitude: pair[0].longitude,
                    altitude_meters: None,
                    speed_mps: None,
                    heading_degrees: None,
                    hold_seconds: None,
                },
                Coordinate {
                    latitude: pair[1].latitude,
                    longitude: pair[1].longitude,
                    altitude_meters: None,
                    speed_mps: None,
                    heading_degrees: None,
                    hold_seconds: None,
                },
            )
        })
        .sum();
    json!({"waypointCount": waypoints.len(), "estimatedDistanceMeters": distance})
}
fn to_local(origin: Coordinate, point: Coordinate) -> (f64, f64) {
    let north = (point.latitude - origin.latitude).to_radians() * EARTH_RADIUS_M;
    let east = (point.longitude - origin.longitude).to_radians()
        * EARTH_RADIUS_M
        * origin.latitude.to_radians().cos();
    (east, north)
}
fn from_local(origin: Coordinate, point: (f64, f64)) -> Coordinate {
    Coordinate {
        latitude: origin.latitude + (point.1 / EARTH_RADIUS_M).to_degrees(),
        longitude: origin.longitude
            + (point.0 / (EARTH_RADIUS_M * origin.latitude.to_radians().cos())).to_degrees(),
        altitude_meters: None,
        speed_mps: None,
        heading_degrees: None,
        hold_seconds: None,
    }
}
fn rotate(point: (f64, f64), angle: f64) -> (f64, f64) {
    (
        point.0 * angle.cos() - point.1 * angle.sin(),
        point.0 * angle.sin() + point.1 * angle.cos(),
    )
}
fn horizontal_intersections(polygon: &[(f64, f64)], y: f64) -> Vec<f64> {
    let mut values = Vec::new();
    for index in 0..polygon.len() {
        let a = polygon[index];
        let b = polygon[(index + 1) % polygon.len()];
        if (a.1 > y) != (b.1 > y) {
            values.push(a.0 + (y - a.1) * (b.0 - a.0) / (b.1 - a.1));
        }
    }
    values
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::{SystemTime, UNIX_EPOCH};

    #[test]
    fn direct_planner_applies_defaults_and_actions() {
        let plan = plan_mission("WAYPOINT", "DIRECT_WAYPOINTS", &json!({"defaultAltitudeMeters": 30, "defaultSpeedMps": 4, "detectionClasses": ["person"], "waypoints": [{"latitude": 51.0, "longitude": -0.1}]})).unwrap();
        assert_eq!(plan.waypoints[0].altitude_meters, 30.0);
        assert_eq!(plan.waypoints[0].speed_mps, Some(4.0));
        assert!(plan
            .actions
            .iter()
            .any(|action| action.action_type == "START_PERCEPTION"));
        let camera = plan
            .actions
            .iter()
            .find(|action| action.action_type == "SET_CAMERA_MODE")
            .unwrap();
        assert_eq!(camera.params["cameraMode"], "FORWARD_OBLIQUE");
        let gimbal = plan
            .actions
            .iter()
            .find(|action| action.action_type == "SET_GIMBAL_ORIENTATION")
            .unwrap();
        assert_eq!(gimbal.params["pitchDegrees"], -35.0);
        assert_eq!(gimbal.params["yawMode"], "FOLLOW_DRONE_HEADING");
    }

    #[test]
    fn direct_planner_rejects_empty_and_invalid_coordinates() {
        assert!(plan_mission("WAYPOINT", "DIRECT_WAYPOINTS", &json!({"waypoints": []})).is_err());
        assert!(plan_mission(
            "WAYPOINT",
            "DIRECT_WAYPOINTS",
            &json!({"waypoints": [{"latitude": 91, "longitude": 0, "altitudeMeters": 20}]})
        )
        .is_err());
    }

    #[test]
    fn lawn_mower_clips_and_alternates_lanes() {
        let plan = plan_mission("AREA_SCAN", "LAWN_MOWER", &json!({"altitudeMeters": 30, "laneSpacingMeters": 20, "cameraMode": "DOWNWARD_SCAN", "detectionClasses": ["person"], "areaPolygon": [{"latitude": 51.0, "longitude": -0.1}, {"latitude": 51.0, "longitude": -0.099}, {"latitude": 51.001, "longitude": -0.099}, {"latitude": 51.001, "longitude": -0.1}]})).unwrap();
        assert!(plan.waypoints.len() >= 4);
        assert!(plan.waypoints[0].longitude < plan.waypoints[1].longitude);
        assert!(plan.waypoints[2].longitude > plan.waypoints[3].longitude);
        assert!(plan
            .actions
            .iter()
            .any(|action| action.action_type == "SET_CAMERA_MODE"));
        let gimbal = plan
            .actions
            .iter()
            .find(|action| action.action_type == "SET_GIMBAL_ORIENTATION")
            .unwrap();
        assert_eq!(gimbal.params["pitchDegrees"], -90.0);
        assert_eq!(gimbal.params["yawMode"], "FOLLOW_LANE_DIRECTION");
    }

    #[test]
    fn route_planner_interpolates_and_preserves_corridor_metadata() {
        let plan = plan_mission("ROUTE_SCAN", "ROUTE_FOLLOW", &json!({"altitudeMeters": 40, "sampleSpacingMeters": 25, "corridorWidthMeters": 30, "route": [{"latitude": 51.0, "longitude": -0.1}, {"latitude": 51.001, "longitude": -0.1}]})).unwrap();
        assert!(plan.waypoints.len() > 2);
        assert_eq!(plan.metadata["corridorWidthMeters"], json!(30.0));
        let gimbal = plan
            .actions
            .iter()
            .find(|action| action.action_type == "SET_GIMBAL_ORIENTATION")
            .unwrap();
        assert_eq!(gimbal.params["pitchDegrees"], -40.0);
        assert_eq!(gimbal.params["yawMode"], "FOLLOW_ROUTE_BEARING");
    }

    #[test]
    fn terrain_profile_raises_route_over_corridor_high_point() {
        let params = json!({
            "altitudeMode": "TERRAIN_CLEARANCE",
            "altitudeMeters": 30,
            "speedMps": 5,
            "sampleSpacingMeters": 1000,
            "corridorWidthMeters": 20,
            "terrain": {
                "safetyMarginMeters": 10,
                "sampleSpacingMeters": 30,
                "corridorWidthMeters": 20,
                "maxClimbRateMps": 2,
                "maxDescentRateMps": 1.5,
                "maxRelativeAltitudeMeters": 500
            },
            "route": [
                {"latitude": 51.0, "longitude": -0.1},
                {"latitude": 51.0009, "longitude": -0.1}
            ]
        });
        let base = plan_mission("ROUTE_SCAN", "ROUTE_FOLLOW", &params).unwrap();
        let base_snapshot = plan_snapshot("base-plan", "ROUTE_SCAN", "ROUTE_FOLLOW", &base);
        let expected = expected_terrain_stations(&base.waypoints, 30.0);
        let high_index = expected.len() / 2;
        let stations = expected
            .iter()
            .enumerate()
            .map(|(index, station)| TerrainProfileStation {
                latitude: station.coordinate.latitude,
                longitude: station.coordinate.longitude,
                source_waypoint_sequence: station.source_waypoint_sequence,
                samples: required_corridor_samples(&expected, index, 20.0)
                    .into_iter()
                    .map(|coordinate| TerrainSample {
                        latitude: coordinate.latitude,
                        longitude: coordinate.longitude,
                        elevation_meters: if index == high_index { 140.0 } else { 100.0 },
                    })
                    .collect(),
            })
            .collect();
        let input = terrain_input("base-plan", stations, 100.0);
        let profiled = apply_terrain_profile(&params, &base_snapshot, &input).unwrap();

        assert_eq!(base.waypoints.len(), 2);
        assert!(profiled.waypoints.len() > base.waypoints.len());
        assert!(profiled
            .waypoints
            .iter()
            .any(|waypoint| waypoint.altitude_meters >= 80.0));
        assert_eq!(profiled.metadata["altitudeMode"], "TERRAIN_CLEARANCE");
        assert_eq!(
            profiled.metadata["terrainProfile"]["homeElevationMeters"],
            100.0
        );
        assert_eq!(
            profiled
                .actions
                .iter()
                .filter(|action| action.action_type == "NAVIGATE_TO")
                .count(),
            profiled.waypoints.len()
        );
    }

    #[test]
    fn terrain_profile_rejects_stale_geometry_and_relative_ceiling_breach() {
        let params = json!({
            "altitudeMode": "TERRAIN_CLEARANCE",
            "altitudeMeters": 30,
            "speedMps": 5,
            "terrain": {
                "safetyMarginMeters": 10,
                "sampleSpacingMeters": 30,
                "corridorWidthMeters": 20,
                "maxClimbRateMps": 2,
                "maxDescentRateMps": 1.5,
                "maxRelativeAltitudeMeters": 60
            },
            "route": [
                {"latitude": 51.0, "longitude": -0.1},
                {"latitude": 51.0003, "longitude": -0.1}
            ]
        });
        let base = plan_mission("ROUTE_SCAN", "ROUTE_FOLLOW", &params).unwrap();
        let base_snapshot = plan_snapshot("base-plan", "ROUTE_SCAN", "ROUTE_FOLLOW", &base);
        let expected = expected_terrain_stations(&base.waypoints, 30.0);
        let mut stations: Vec<_> = expected
            .iter()
            .enumerate()
            .map(|(index, station)| TerrainProfileStation {
                latitude: station.coordinate.latitude,
                longitude: station.coordinate.longitude,
                source_waypoint_sequence: station.source_waypoint_sequence,
                samples: required_corridor_samples(&expected, index, 20.0)
                    .into_iter()
                    .map(|coordinate| TerrainSample {
                        latitude: coordinate.latitude,
                        longitude: coordinate.longitude,
                        elevation_meters: 140.0,
                    })
                    .collect(),
            })
            .collect();
        let ceiling_error = apply_terrain_profile(
            &params,
            &base_snapshot,
            &terrain_input("base-plan", stations.clone(), 100.0),
        )
        .unwrap_err();
        assert!(ceiling_error.contains("above the configured"));

        stations[0].latitude += 0.001;
        stations[0].samples[0].latitude += 0.001;
        let geometry_error = apply_terrain_profile(
            &params,
            &base_snapshot,
            &terrain_input("base-plan", stations, 140.0),
        )
        .unwrap_err();
        assert!(geometry_error.contains("does not match the route"));
    }

    #[test]
    fn terrain_profile_is_persisted_as_a_new_immutable_plan() {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!("atlas-terrain-mission-{nonce}.db"));
        let database = LocalDatabase::open_path(path.clone()).unwrap();
        let params = json!({
            "altitudeMode": "TERRAIN_CLEARANCE",
            "altitudeMeters": 30,
            "speedMps": 5,
            "terrain": {
                "safetyMarginMeters": 10,
                "sampleSpacingMeters": 30,
                "corridorWidthMeters": 20,
                "maxClimbRateMps": 2,
                "maxDescentRateMps": 1.5,
                "maxRelativeAltitudeMeters": 500
            },
            "route": [
                {"latitude": 51.0, "longitude": -0.1},
                {"latitude": 51.0003, "longitude": -0.1}
            ]
        });
        let mission = database
            .create_mission(&CreateMissionInput {
                template_type: "ROUTE_SCAN".to_string(),
                name: "Terrain route".to_string(),
                description: String::new(),
                selected_pattern: None,
                params,
            })
            .unwrap();
        let base = database.plan_mission(&mission.id).unwrap();
        let expected = expected_terrain_stations(&base.generated_waypoints, 30.0);
        let stations = expected
            .iter()
            .enumerate()
            .map(|(index, station)| TerrainProfileStation {
                latitude: station.coordinate.latitude,
                longitude: station.coordinate.longitude,
                source_waypoint_sequence: station.source_waypoint_sequence,
                samples: required_corridor_samples(&expected, index, 20.0)
                    .into_iter()
                    .map(|coordinate| TerrainSample {
                        latitude: coordinate.latitude,
                        longitude: coordinate.longitude,
                        elevation_meters: 100.0,
                    })
                    .collect(),
            })
            .collect();
        let final_plan = database
            .apply_mission_terrain_profile(&mission.id, &terrain_input(&base.id, stations, 100.0))
            .unwrap();

        assert_ne!(base.id, final_plan.id);
        assert_eq!(final_plan.metadata["basePlanId"], base.id);
        assert_eq!(
            database.mission(&mission.id).unwrap().generated_plan_id,
            Some(final_plan.id.clone())
        );
        let connection = database.connection.lock().unwrap();
        let plan_count: i64 = connection
            .query_row(
                "SELECT count(*) FROM mission_plans WHERE mission_id = ?1",
                [&mission.id],
                |row| row.get(0),
            )
            .unwrap();
        assert_eq!(plan_count, 2);
        drop(connection);
        drop(database);
        let _ = std::fs::remove_file(&path);
        let _ = std::fs::remove_file(path.with_extension("db-wal"));
        let _ = std::fs::remove_file(path.with_extension("db-shm"));
    }

    fn terrain_input(
        base_plan_id: &str,
        stations: Vec<TerrainProfileStation>,
        home_elevation: f64,
    ) -> ApplyTerrainProfileInput {
        ApplyTerrainProfileInput {
            base_plan_id: base_plan_id.to_string(),
            source: TerrainSourceEvidence {
                dataset_id: "test-dem".to_string(),
                display_name: "Test DEM".to_string(),
                encoding: "terrarium".to_string(),
                zoom: 12,
                tile_size: 256,
                sampled_at_unix_ms: unix_time_ms(),
            },
            home: TerrainSample {
                latitude: 51.0,
                longitude: -0.1,
                elevation_meters: home_elevation,
            },
            stations,
        }
    }

    fn plan_snapshot(
        id: &str,
        template_type: &str,
        pattern_type: &str,
        plan: &PlannedMission,
    ) -> MissionPlanSnapshot {
        MissionPlanSnapshot {
            id: id.to_string(),
            mission_id: "mission".to_string(),
            template_type: template_type.to_string(),
            pattern_type: pattern_type.to_string(),
            status: "READY".to_string(),
            generated_waypoints: plan.waypoints.clone(),
            actions: plan.actions.clone(),
            metadata: plan.metadata.clone(),
            validation_warnings: plan.warnings.clone(),
            created_at_unix_ms: 1,
            updated_at_unix_ms: 1,
        }
    }

    #[test]
    fn waypoint_gimbal_override_is_emitted_after_navigation() {
        let plan = plan_mission(
            "WAYPOINT",
            "DIRECT_WAYPOINTS",
            &json!({
                "defaultAltitudeMeters": 25,
                "waypoints": [{
                    "latitude": 51.0,
                    "longitude": -0.1,
                    "holdSeconds": 10,
                    "cameraMode": "LOOK_AT_POINT",
                    "gimbal": {
                        "pitchDegrees": -45,
                        "yawMode": "LOOK_AT_POINT",
                        "stabilization": true,
                        "target": {"latitude": 51.0005, "longitude": -0.1005}
                    }
                }]
            }),
        )
        .unwrap();
        let navigation_index = plan
            .actions
            .iter()
            .position(|action| action.action_type == "NAVIGATE_TO")
            .unwrap();
        let override_index = plan
            .actions
            .iter()
            .position(|action| {
                action.action_type == "SET_GIMBAL_ORIENTATION"
                    && action.params.get("waypointSequence") == Some(&json!(0))
            })
            .unwrap();
        assert!(override_index > navigation_index);
        assert_eq!(
            plan.actions[override_index].params["target"]["latitude"],
            51.0005
        );
    }

    #[test]
    fn rejects_invalid_gimbal_behaviour() {
        assert!(plan_mission(
            "WAYPOINT",
            "DIRECT_WAYPOINTS",
            &json!({
                "defaultAltitudeMeters": 25,
                "gimbal": {"pitchDegrees": -100, "yawMode": "FOLLOW_DRONE_HEADING"},
                "waypoints": [{"latitude": 51.0, "longitude": -0.1}]
            })
        )
        .is_err());
        assert!(plan_mission(
            "WAYPOINT",
            "DIRECT_WAYPOINTS",
            &json!({
                "defaultAltitudeMeters": 25,
                "gimbal": {"pitchDegrees": -45, "yawMode": "LOOK_AT_POINT"},
                "waypoints": [{"latitude": 51.0, "longitude": -0.1}]
            })
        )
        .is_err());
    }

    #[test]
    fn rejects_unsupported_template_pattern_pairs() {
        assert!(plan_mission("AREA_SCAN", "DIRECT_WAYPOINTS", &json!({})).is_err());
        assert!(plan_mission("ROUTE_SCAN", "LAWN_MOWER", &json!({})).is_err());
    }

    #[test]
    fn persisted_definition_keeps_generated_plans_immutable() {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!("atlas-mission-{nonce}.db"));
        let database = LocalDatabase::open_path(path.clone()).unwrap();
        let mission = database
            .create_mission(&CreateMissionInput {
                template_type: "WAYPOINT".into(),
                name: "Reusable route".into(),
                description: String::new(),
                selected_pattern: None,
                params: json!({"defaultAltitudeMeters": 25, "waypoints": [{"latitude": 51.0, "longitude": -0.1}]}),
            })
            .unwrap();
        let first = database.plan_mission(&mission.id).unwrap();
        database
            .update_mission(
                &mission.id,
                &CreateMissionInput {
                    template_type: "WAYPOINT".into(),
                    name: "Reusable route, revised".into(),
                    description: String::new(),
                    selected_pattern: None,
                    params: json!({"defaultAltitudeMeters": 30, "waypoints": [{"latitude": 51.001, "longitude": -0.101}]}),
                },
            )
            .unwrap();
        let second = database.plan_mission(&mission.id).unwrap();
        assert_ne!(first.id, second.id);
        assert_eq!(
            database.mission(&mission.id).unwrap().generated_plan_id,
            Some(second.id.clone())
        );
        let connection = database.connection.lock().unwrap();
        let plan_count: i64 = connection
            .query_row(
                "SELECT count(*) FROM mission_plans WHERE mission_id = ?1",
                [&mission.id],
                |row| row.get(0),
            )
            .unwrap();
        assert_eq!(plan_count, 2);
        drop(connection);
        drop(database);
        let _ = std::fs::remove_file(&path);
        let _ = std::fs::remove_file(path.with_extension("db-wal"));
        let _ = std::fs::remove_file(path.with_extension("db-shm"));
    }
}
