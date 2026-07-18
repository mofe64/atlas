use serde::Serialize;
use std::time::{SystemTime, UNIX_EPOCH};
use tauri::{ipc::Response, State};

use crate::{database, AppState};

const MAX_FIRST_WAYPOINT_DISTANCE_METERS: f64 = 5_000.0;
const MAX_TERRAIN_HOME_DRIFT_METERS: f64 = 30.0;

/// Values supplied by the compiled native host rather than the webview.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct RuntimeInfo {
    app_version: &'static str,
    target_arch: &'static str,
    target_os: &'static str,
}

#[tauri::command]
pub(crate) fn runtime_info() -> RuntimeInfo {
    RuntimeInfo {
        app_version: env!("CARGO_PKG_VERSION"),
        target_arch: std::env::consts::ARCH,
        target_os: std::env::consts::OS,
    }
}

#[tauri::command]
pub(crate) fn ground_station_snapshot(
    state: State<'_, AppState>,
) -> Result<GroundStationSnapshot, String> {
    Ok(GroundStationSnapshot {
        listen_address: state.listen_address.clone(),
        operations: state.database.operations_snapshot()?,
    })
}

/// Returns the latest structured detections and runtime health. The video
/// renderer can combine this snapshot with the original stream without
/// permanently drawing annotations into the source pixels.
#[tauri::command]
pub(crate) fn perception_snapshot(
    state: State<'_, AppState>,
    drone_id: Option<String>,
) -> Option<crate::ground_station::PerceptionSnapshot> {
    state.perception.snapshot(drone_id.as_deref())
}

#[tauri::command]
pub(crate) async fn perception_frame_subscription_start(
    state: State<'_, AppState>,
    drone_id: String,
    subscription_id: String,
    purpose: String,
    lease_duration_ms: i64,
) -> Result<(), String> {
    state
        .perception
        .start_or_renew_frame_subscription(&drone_id, &subscription_id, &purpose, lease_duration_ms)
        .await
}

#[tauri::command]
pub(crate) async fn perception_frame_subscription_renew(
    state: State<'_, AppState>,
    drone_id: String,
    subscription_id: String,
    purpose: String,
    lease_duration_ms: i64,
) -> Result<(), String> {
    state
        .perception
        .start_or_renew_frame_subscription(&drone_id, &subscription_id, &purpose, lease_duration_ms)
        .await
}

#[tauri::command]
pub(crate) async fn perception_frame_subscription_stop(
    state: State<'_, AppState>,
    drone_id: String,
    subscription_id: String,
    purpose: String,
) -> Result<(), String> {
    state
        .perception
        .stop_frame_subscription(&drone_id, &subscription_id, &purpose)
        .await
}

#[tauri::command]
pub(crate) fn video_stream_start(
    state: State<'_, AppState>,
    drone_id: String,
) -> Result<crate::video::VideoStreamSnapshot, String> {
    state.video.start(&drone_id)
}

#[tauri::command]
pub(crate) fn video_stream_stop(
    state: State<'_, AppState>,
    drone_id: Option<String>,
) -> Result<crate::video::VideoStreamSnapshot, String> {
    state.video.stop(drone_id.as_deref())
}

#[tauri::command]
pub(crate) fn video_stream_snapshot(
    state: State<'_, AppState>,
) -> Result<crate::video::VideoStreamSnapshot, String> {
    state.video.snapshot()
}

/// Returns a raw packet: `ATV1`, a little-endian JSON-header length, the JSON
/// header, then a clean JPEG frame. The header carries the matched perception
/// frame, keeping annotations out of the source pixels.
#[tauri::command]
pub(crate) fn video_stream_frame(
    state: State<'_, AppState>,
    drone_id: String,
    after_sequence: u64,
) -> Result<Response, String> {
    state
        .video
        .frame_packet(&state.perception, &drone_id, after_sequence)
        .map(Response::new)
}

#[tauri::command]
pub(crate) fn fleet_snapshot(
    state: State<'_, AppState>,
    include_archived: Option<bool>,
) -> Result<database::FleetSnapshot, String> {
    state
        .database
        .fleet_snapshot(include_archived.unwrap_or(false))
}

#[tauri::command]
pub(crate) fn archive_drone(
    state: State<'_, AppState>,
    drone_id: String,
    reason: Option<String>,
) -> Result<database::OperationsSnapshot, String> {
    state.database.archive_drone(
        &drone_id,
        reason.as_deref().unwrap_or("operator archived aircraft"),
    )
}

#[tauri::command]
pub(crate) fn restore_drone(
    state: State<'_, AppState>,
    drone_id: String,
) -> Result<database::OperationsSnapshot, String> {
    state.database.restore_drone(&drone_id)
}

#[tauri::command]
pub(crate) fn vehicle_operations_snapshot(
    state: State<'_, AppState>,
    drone_id: String,
) -> Result<GroundStationSnapshot, String> {
    Ok(GroundStationSnapshot {
        listen_address: state.listen_address.clone(),
        operations: state.database.operations_snapshot_for(Some(&drone_id))?,
    })
}

#[tauri::command]
pub(crate) fn history_overview(
    state: State<'_, AppState>,
) -> Result<database::HistoryOverview, String> {
    state.database.history_overview()
}

#[tauri::command]
pub(crate) fn incident_list(
    state: State<'_, AppState>,
    include_closed: Option<bool>,
    limit: Option<usize>,
) -> Result<Vec<database::IncidentSnapshot>, String> {
    state
        .database
        .incidents(include_closed.unwrap_or(false), limit.unwrap_or(100))
}

#[tauri::command]
pub(crate) fn incident_detail(
    state: State<'_, AppState>,
    incident_id: String,
) -> Result<database::IncidentDetailSnapshot, String> {
    state.database.incident(&incident_id)
}

#[tauri::command]
pub(crate) fn create_incident(
    state: State<'_, AppState>,
    input: database::CreateIncidentInput,
) -> Result<database::IncidentDetailSnapshot, String> {
    state.database.create_incident(&input)
}

#[tauri::command]
pub(crate) fn update_incident(
    state: State<'_, AppState>,
    incident_id: String,
    input: database::UpdateIncidentInput,
) -> Result<database::IncidentDetailSnapshot, String> {
    state.database.update_incident(&incident_id, &input)
}

#[tauri::command]
pub(crate) fn prepare_incident_response(
    state: State<'_, AppState>,
    incident_id: String,
    input: database::PrepareIncidentResponseInput,
) -> Result<database::PreparedIncidentResponse, String> {
    state
        .database
        .prepare_incident_response(&incident_id, &input)
}

#[tauri::command]
pub(crate) fn mission_templates() -> Vec<database::MissionTemplate> {
    database::mission_templates()
}

#[tauri::command]
pub(crate) fn mission_list(
    state: State<'_, AppState>,
) -> Result<Vec<database::MissionSnapshot>, String> {
    state.database.missions()
}

#[tauri::command]
pub(crate) fn mission_detail(
    state: State<'_, AppState>,
    mission_id: String,
) -> Result<database::MissionSnapshot, String> {
    state.database.mission(&mission_id)
}

#[tauri::command]
pub(crate) fn create_mission(
    state: State<'_, AppState>,
    input: database::CreateMissionInput,
) -> Result<database::MissionSnapshot, String> {
    state.database.create_mission(&input)
}

#[tauri::command]
pub(crate) fn update_mission(
    state: State<'_, AppState>,
    mission_id: String,
    input: database::CreateMissionInput,
) -> Result<database::MissionSnapshot, String> {
    state.database.update_mission(&mission_id, &input)
}

#[tauri::command]
pub(crate) fn generate_mission_plan(
    state: State<'_, AppState>,
    mission_id: String,
) -> Result<database::MissionPlanSnapshot, String> {
    state.database.plan_mission(&mission_id)
}

#[tauri::command]
pub(crate) fn apply_mission_terrain_profile(
    state: State<'_, AppState>,
    mission_id: String,
    input: database::ApplyTerrainProfileInput,
) -> Result<database::MissionPlanSnapshot, String> {
    state
        .database
        .apply_mission_terrain_profile(&mission_id, &input)
}

#[tauri::command]
pub(crate) fn mission_plan(
    state: State<'_, AppState>,
    mission_id: String,
) -> Result<database::MissionPlanSnapshot, String> {
    state.database.mission_plan(&mission_id)
}

#[tauri::command]
pub(crate) async fn upload_mission(
    state: State<'_, AppState>,
    mission_id: String,
    drone_id: String,
) -> Result<database::MissionRunSnapshot, String> {
    let mission = state.database.mission(&mission_id)?;
    let plan = state.database.mission_plan(&mission_id)?;
    validate_terrain_plan_ready(&mission, &plan)?;
    let snapshot = state.database.operations_snapshot_for(Some(&drone_id))?;
    validate_mission_upload_distance(&plan, &snapshot)?;
    let dispatch = state
        .database
        .create_mission_run(&mission_id, &drone_id, Some(&plan.id))?;
    let mut vehicle_plan = plan.clone();
    if let Some(metadata) = vehicle_plan.metadata.as_object_mut() {
        metadata.remove("terrainProfileEvidence");
        if let Some(profile) = metadata
            .get_mut("terrainProfile")
            .and_then(serde_json::Value::as_object_mut)
        {
            profile.remove("profilePoints");
        }
    }
    let plan_json = serde_json::to_string(&vehicle_plan)
        .map_err(|error| format!("serialize mission plan for upload: {error}"))?;
    let requested_at = unix_time_ms();
    match state
        .command_router
        .deliver_mission_operation(
            &drone_id,
            &dispatch.operation_id,
            &dispatch.run.id,
            "upload",
            Some(&plan_json),
            requested_at,
            requested_at + 120_000,
        )
        .await
    {
        Ok(()) => state.database.mission_run(&dispatch.run.id),
        Err(error) => state.database.record_mission_delivery_failure(
            &dispatch.run.id,
            &dispatch.operation_id,
            "upload",
            &error,
        ),
    }
}

fn validate_terrain_plan_ready(
    mission: &database::MissionSnapshot,
    plan: &database::MissionPlanSnapshot,
) -> Result<(), String> {
    if mission
        .params
        .get("altitudeMode")
        .and_then(serde_json::Value::as_str)
        == Some("TERRAIN_CLEARANCE")
        && plan
            .metadata
            .get("altitudeMode")
            .and_then(serde_json::Value::as_str)
            != Some("TERRAIN_CLEARANCE")
    {
        return Err("mission upload blocked: terrain-aware planning is incomplete; regenerate the DEM altitude profile".to_string());
    }
    Ok(())
}

fn validate_mission_upload_distance(
    plan: &database::MissionPlanSnapshot,
    snapshot: &database::OperationsSnapshot,
) -> Result<(), String> {
    if snapshot.connection_status != "connected" {
        return Err("mission upload blocked: target aircraft is not connected".to_string());
    }
    let first_waypoint = plan.generated_waypoints.first().ok_or_else(|| {
        "mission upload blocked: generated plan does not contain a first waypoint".to_string()
    })?;
    let telemetry = snapshot.telemetry.as_ref().ok_or_else(|| {
        "mission upload blocked: waiting for aircraft home or current position".to_string()
    })?;
    validate_terrain_profile_home(plan, telemetry)?;
    let reference = (telemetry.home_position_set == Some(true))
        .then_some(telemetry.home_position.as_ref())
        .flatten()
        .and_then(|home| {
            valid_position(home.latitude, home.longitude).map(|position| (position, "home"))
        })
        .or_else(|| {
            valid_position(telemetry.latitude, telemetry.longitude)
                .map(|position| (position, "current"))
        })
        .ok_or_else(|| {
            "mission upload blocked: waiting for aircraft home or current position".to_string()
        })?;
    let distance = distance_between(
        reference.0 .0,
        reference.0 .1,
        first_waypoint.latitude,
        first_waypoint.longitude,
    );
    if distance > MAX_FIRST_WAYPOINT_DISTANCE_METERS {
        return Err(format!(
            "mission upload blocked: first waypoint is {} from aircraft {} position; maximum allowed distance is 5.0 km",
            format_distance(distance), reference.1
        ));
    }
    Ok(())
}

fn validate_terrain_profile_home(
    plan: &database::MissionPlanSnapshot,
    telemetry: &database::AircraftTelemetrySnapshot,
) -> Result<(), String> {
    if plan
        .metadata
        .get("altitudeMode")
        .and_then(serde_json::Value::as_str)
        != Some("TERRAIN_CLEARANCE")
    {
        return Ok(());
    }
    let profile = plan
        .metadata
        .get("terrainProfile")
        .ok_or_else(|| "mission upload blocked: terrain profile evidence is missing".to_string())?;
    let planned_home = valid_position(
        profile
            .get("homeLatitude")
            .and_then(serde_json::Value::as_f64),
        profile
            .get("homeLongitude")
            .and_then(serde_json::Value::as_f64),
    )
    .ok_or_else(|| "mission upload blocked: terrain profile home is invalid".to_string())?;
    let reported_home = (telemetry.home_position_set == Some(true))
        .then_some(telemetry.home_position.as_ref())
        .flatten()
        .and_then(|home| valid_position(home.latitude, home.longitude))
        .ok_or_else(|| {
            "mission upload blocked: terrain-aware plans require a reported aircraft home position"
                .to_string()
        })?;
    let distance = distance_between(
        planned_home.0,
        planned_home.1,
        reported_home.0,
        reported_home.1,
    );
    if distance > MAX_TERRAIN_HOME_DRIFT_METERS {
        return Err(format!(
            "mission upload blocked: aircraft home moved {} from the home used for the terrain profile; regenerate the plan",
            format_distance(distance)
        ));
    }
    Ok(())
}

fn valid_position(latitude: Option<f64>, longitude: Option<f64>) -> Option<(f64, f64)> {
    match (latitude, longitude) {
        (Some(latitude), Some(longitude))
            if latitude.is_finite()
                && longitude.is_finite()
                && (-90.0..=90.0).contains(&latitude)
                && (-180.0..=180.0).contains(&longitude) =>
        {
            Some((latitude, longitude))
        }
        _ => None,
    }
}

fn distance_between(
    latitude: f64,
    longitude: f64,
    target_latitude: f64,
    target_longitude: f64,
) -> f64 {
    const EARTH_RADIUS_METERS: f64 = 6_371_000.0;
    let latitude_radians = latitude.to_radians();
    let target_latitude_radians = target_latitude.to_radians();
    let latitude_delta = (target_latitude - latitude).to_radians();
    let longitude_delta = (target_longitude - longitude).to_radians();
    let haversine = (latitude_delta / 2.0).sin().powi(2)
        + latitude_radians.cos()
            * target_latitude_radians.cos()
            * (longitude_delta / 2.0).sin().powi(2);
    EARTH_RADIUS_METERS * 2.0 * haversine.sqrt().atan2((1.0 - haversine).sqrt())
}

fn format_distance(distance_meters: f64) -> String {
    if distance_meters >= 1_000.0 {
        format!("{:.1} km", distance_meters / 1_000.0)
    } else {
        format!("{distance_meters:.0} m")
    }
}

#[tauri::command]
pub(crate) async fn control_mission_run(
    state: State<'_, AppState>,
    mission_run_id: String,
    operation: String,
) -> Result<database::MissionRunSnapshot, String> {
    if operation == "start" {
        let run = state.database.mission_run(&mission_run_id)?;
        let snapshot = state
            .database
            .operations_snapshot_for(Some(&run.drone_id))?;
        validate_mission_start(&snapshot)?;
    }
    let dispatch = state
        .database
        .record_mission_operation_requested(&mission_run_id, &operation)?;
    let requested_at = unix_time_ms();
    match state
        .command_router
        .deliver_mission_operation(
            &dispatch.run.drone_id,
            &dispatch.operation_id,
            &dispatch.run.id,
            &operation,
            None,
            requested_at,
            requested_at + 30_000,
        )
        .await
    {
        Ok(()) => state.database.mission_run(&dispatch.run.id),
        Err(error) => state.database.record_mission_delivery_failure(
            &dispatch.run.id,
            &dispatch.operation_id,
            &operation,
            &error,
        ),
    }
}

fn validate_mission_start(snapshot: &database::OperationsSnapshot) -> Result<(), String> {
    if snapshot.connection_status != "connected" {
        return Err("mission start blocked: target aircraft is not connected".to_string());
    }
    let telemetry = snapshot
        .telemetry
        .as_ref()
        .ok_or_else(|| "mission start blocked: waiting for aircraft telemetry".to_string())?;
    if telemetry.status != "live" {
        return Err("mission start blocked: aircraft telemetry is stale".to_string());
    }
    let health = telemetry
        .health
        .as_ref()
        .ok_or_else(|| "mission start blocked: waiting for PX4 preflight health".to_string())?;
    if telemetry.armed != Some(true) && !health.armable {
        return Err("mission start blocked: PX4 reports the aircraft is not armable".to_string());
    }
    if !health.global_position_ok {
        return Err("mission start blocked: global position is not ready".to_string());
    }
    if !health.home_position_ok || telemetry.home_position_set != Some(true) {
        return Err("mission start blocked: home position is not ready".to_string());
    }
    if telemetry
        .battery_percent
        .is_some_and(|battery| battery < 15.0)
    {
        return Err("mission start blocked: battery is below 15 percent".to_string());
    }
    Ok(())
}

#[cfg(test)]
mod mission_start_tests {
    use super::{validate_mission_start, validate_mission_upload_distance};
    use crate::database::{
        AircraftTelemetrySnapshot, HomePosition, MissionPlanSnapshot, MissionWaypoint,
        OperationsSnapshot, VehicleHealth,
    };
    use serde_json::json;

    fn ready_snapshot() -> OperationsSnapshot {
        OperationsSnapshot {
            connection_status: "connected".to_string(),
            telemetry: Some(AircraftTelemetrySnapshot {
                status: "live".to_string(),
                armed: Some(false),
                battery_percent: Some(85.0),
                home_position_set: Some(true),
                health: Some(VehicleHealth {
                    gyrometer_calibration_ok: true,
                    accelerometer_calibration_ok: true,
                    magnetometer_calibration_ok: true,
                    local_position_ok: true,
                    global_position_ok: true,
                    home_position_ok: true,
                    armable: true,
                }),
                ..AircraftTelemetrySnapshot::default()
            }),
            ..OperationsSnapshot::default()
        }
    }

    fn plan_at(latitude: f64, longitude: f64) -> MissionPlanSnapshot {
        MissionPlanSnapshot {
            id: "plan-1".to_string(),
            mission_id: "mission-1".to_string(),
            template_type: "WAYPOINT".to_string(),
            pattern_type: "DIRECT_WAYPOINTS".to_string(),
            status: "READY".to_string(),
            generated_waypoints: vec![MissionWaypoint {
                sequence: 0,
                latitude,
                longitude,
                altitude_meters: 20.0,
                speed_mps: Some(4.0),
                heading_degrees: None,
                hold_seconds: None,
                action_refs: Vec::new(),
            }],
            actions: Vec::new(),
            metadata: json!({}),
            validation_warnings: Vec::new(),
            created_at_unix_ms: 1,
            updated_at_unix_ms: 1,
        }
    }

    fn terrain_plan_at(
        latitude: f64,
        longitude: f64,
        home_latitude: f64,
        home_longitude: f64,
    ) -> MissionPlanSnapshot {
        let mut plan = plan_at(latitude, longitude);
        plan.metadata = json!({
            "altitudeMode": "TERRAIN_CLEARANCE",
            "terrainProfile": {
                "homeLatitude": home_latitude,
                "homeLongitude": home_longitude,
                "homeElevationMeters": 10.0
            }
        });
        plan
    }

    #[test]
    fn mission_start_allows_ready_aircraft() {
        assert!(validate_mission_start(&ready_snapshot()).is_ok());
    }

    #[test]
    fn mission_start_blocks_disconnected_aircraft() {
        let mut snapshot = ready_snapshot();
        snapshot.connection_status = "disconnected".to_string();
        assert_eq!(
            validate_mission_start(&snapshot).unwrap_err(),
            "mission start blocked: target aircraft is not connected"
        );
    }

    #[test]
    fn mission_start_blocks_unarmable_aircraft() {
        let mut snapshot = ready_snapshot();
        snapshot
            .telemetry
            .as_mut()
            .unwrap()
            .health
            .as_mut()
            .unwrap()
            .armable = false;
        assert_eq!(
            validate_mission_start(&snapshot).unwrap_err(),
            "mission start blocked: PX4 reports the aircraft is not armable"
        );
    }

    #[test]
    fn mission_upload_allows_first_waypoint_near_home() {
        let mut snapshot = ready_snapshot();
        snapshot.telemetry.as_mut().unwrap().home_position = Some(HomePosition {
            latitude: Some(37.41903),
            longitude: Some(-121.99332),
            absolute_altitude_m: None,
            relative_altitude_m: None,
        });
        assert!(
            validate_mission_upload_distance(&plan_at(37.41918, -121.99305), &snapshot).is_ok()
        );
    }

    #[test]
    fn mission_upload_blocks_first_waypoint_far_from_home() {
        let mut snapshot = ready_snapshot();
        let telemetry = snapshot.telemetry.as_mut().unwrap();
        telemetry.latitude = Some(51.5074);
        telemetry.longitude = Some(-0.1278);
        telemetry.home_position = Some(HomePosition {
            latitude: Some(37.41903),
            longitude: Some(-121.99332),
            absolute_altitude_m: None,
            relative_altitude_m: None,
        });
        let error =
            validate_mission_upload_distance(&plan_at(51.5074, -0.1278), &snapshot).unwrap_err();
        assert!(error.contains("from aircraft home position"), "{error}");
        assert!(
            error.contains("maximum allowed distance is 5.0 km"),
            "{error}"
        );
    }

    #[test]
    fn mission_upload_blocks_when_no_position_is_available() {
        let snapshot = ready_snapshot();
        assert_eq!(
            validate_mission_upload_distance(&plan_at(37.41918, -121.99305), &snapshot)
                .unwrap_err(),
            "mission upload blocked: waiting for aircraft home or current position"
        );
    }

    #[test]
    fn terrain_mission_upload_requires_the_profiled_home() {
        let mut snapshot = ready_snapshot();
        snapshot.telemetry.as_mut().unwrap().home_position = Some(HomePosition {
            latitude: Some(37.41903),
            longitude: Some(-121.99332),
            absolute_altitude_m: None,
            relative_altitude_m: None,
        });
        assert!(validate_mission_upload_distance(
            &terrain_plan_at(37.41918, -121.99305, 37.41903, -121.99332),
            &snapshot,
        )
        .is_ok());

        let error = validate_mission_upload_distance(
            &terrain_plan_at(37.41918, -121.99305, 37.42003, -121.99332),
            &snapshot,
        )
        .unwrap_err();
        assert!(error.contains("aircraft home moved"), "{error}");
        assert!(error.contains("regenerate the plan"), "{error}");
    }
}

#[tauri::command]
pub(crate) fn mission_run_detail(
    state: State<'_, AppState>,
    mission_run_id: String,
) -> Result<database::MissionRunSnapshot, String> {
    state.database.mission_run(&mission_run_id)
}

#[tauri::command]
pub(crate) fn mission_run_history(
    state: State<'_, AppState>,
    mission_id: Option<String>,
    drone_id: Option<String>,
    limit: Option<usize>,
) -> Result<Vec<database::MissionRunSnapshot>, String> {
    state.database.mission_runs(
        mission_id.as_deref(),
        drone_id.as_deref(),
        limit.unwrap_or(50),
    )
}

#[tauri::command]
pub(crate) async fn request_vehicle_command(
    state: State<'_, AppState>,
    drone_id: String,
    command_type: String,
    parameters_json: Option<String>,
    timeout_ms: Option<i64>,
) -> Result<database::VehicleCommandSnapshot, String> {
    let command = state.database.create_vehicle_command(
        &drone_id,
        &command_type,
        parameters_json.as_deref().unwrap_or("{}"),
        timeout_ms,
        unix_time_ms(),
    )?;
    match state
        .command_router
        .deliver(&state.database, &command)
        .await
    {
        Ok(delivered) => Ok(delivered),
        Err(_) => state.database.vehicle_command(&command.id),
    }
}

#[tauri::command]
pub(crate) fn vehicle_command_detail(
    state: State<'_, AppState>,
    command_id: String,
) -> Result<database::VehicleCommandSnapshot, String> {
    state.database.vehicle_command(&command_id)
}

#[tauri::command]
pub(crate) fn vehicle_command_history(
    state: State<'_, AppState>,
    drone_id: String,
    limit: Option<usize>,
) -> Result<Vec<database::VehicleCommandSnapshot>, String> {
    state
        .database
        .vehicle_command_history(&drone_id, limit.unwrap_or(50))
}

#[tauri::command]
pub(crate) async fn cancel_vehicle_command(
    state: State<'_, AppState>,
    command_id: String,
) -> Result<database::VehicleCommandSnapshot, String> {
    let now = unix_time_ms();
    let command = state
        .database
        .record_vehicle_command_cancellation_requested(&command_id, now)?;
    state
        .command_router
        .cancel(&command.drone_id, &command.id, now)
        .await?;
    Ok(command)
}

#[tauri::command]
pub(crate) fn vehicle_telemetry_history(
    state: State<'_, AppState>,
    drone_id: String,
    from_received_at_unix_ms: Option<i64>,
    to_received_at_unix_ms: Option<i64>,
    before: Option<database::TelemetryHistoryCursor>,
    limit: Option<usize>,
) -> Result<database::TelemetryHistoryPage, String> {
    state
        .database
        .telemetry_history(&database::TelemetryHistoryQuery {
            drone_id,
            from_received_at_unix_ms,
            to_received_at_unix_ms,
            before,
            limit: limit.unwrap_or(0),
        })
}

#[tauri::command]
pub(crate) fn vehicle_telemetry_chart_series(
    state: State<'_, AppState>,
    drone_id: String,
    from_received_at_unix_ms: i64,
    to_received_at_unix_ms: i64,
    maximum_points: Option<usize>,
) -> Result<database::TelemetryChartSeries, String> {
    state.database.telemetry_chart_series(
        &drone_id,
        from_received_at_unix_ms,
        to_received_at_unix_ms,
        maximum_points.unwrap_or(0),
    )
}

#[tauri::command]
pub(crate) fn vehicle_event_history(
    state: State<'_, AppState>,
    drone_id: String,
    from_received_at_unix_ms: Option<i64>,
    to_received_at_unix_ms: Option<i64>,
    limit: Option<usize>,
) -> Result<Vec<database::StatusEventSnapshot>, String> {
    state.database.vehicle_event_history(
        &drone_id,
        from_received_at_unix_ms,
        to_received_at_unix_ms,
        limit.unwrap_or(0),
    )
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct GroundStationSnapshot {
    listen_address: String,
    #[serde(flatten)]
    operations: database::OperationsSnapshot,
}

fn unix_time_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as i64
}

#[cfg(test)]
mod tests {
    use super::runtime_info;

    #[test]
    fn runtime_info_comes_from_the_compiled_host() {
        let info = runtime_info();

        assert!(!info.app_version.is_empty());
        assert!(!info.target_arch.is_empty());
        assert!(!info.target_os.is_empty());
    }
}
