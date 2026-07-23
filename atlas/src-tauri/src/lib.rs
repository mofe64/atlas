mod commands;
mod database;
mod ground_station;
mod recording;
mod video;

use std::{net::SocketAddr, sync::Arc};

use commands::{
    abandon_prepared_response, acknowledge_operational_alert, aircraft_follow_session,
    aircraft_follow_sessions, annotate_evidence_asset, annotate_perception_track,
    apply_mission_terrain_profile, archive_drone, cancel_vehicle_command, capture_evidence_still,
    clear_perception_track_selection, control_mission_run, create_aircraft_follow_session,
    create_incident, create_mission, end_aircraft_follow_session, evidence_asset,
    evidence_asset_content, evidence_assets, evidence_recording_status, evidence_retention_policy,
    fleet_snapshot, generate_mission_plan, ground_station_snapshot, history_overview,
    incident_detail, incident_list, incident_response_aircraft_suitability, mission_detail,
    mission_list, mission_plan, mission_run_detail, mission_run_history, mission_templates,
    operational_alerts, operational_track_geolocations, perception_counting_rules,
    perception_counts, perception_frame_subscription_renew, perception_frame_subscription_start,
    perception_frame_subscription_stop, perception_snapshot, perception_track_geolocations,
    perception_track_history, perception_track_samples, perception_track_selection,
    prepare_incident_response, preview_incident_response, queue_evidence_event_clip,
    refine_perception_track_geolocation, renew_aircraft_follow_session, request_vehicle_command,
    restore_drone, restore_evidence_asset, review_evidence_asset, runtime_info,
    select_perception_track, spatial_frame, spatial_snapshot, spatial_subscription_renew,
    spatial_subscription_start, spatial_subscription_stop, start_evidence_recording,
    stop_evidence_recording, trash_evidence_asset, update_evidence_asset_retention,
    update_evidence_retention_policy, update_incident, update_mission, upload_mission,
    upsert_perception_counting_rule, vehicle_command_detail, vehicle_command_history,
    vehicle_event_history, vehicle_operations_snapshot, vehicle_telemetry_chart_series,
    vehicle_telemetry_history, video_stream_frame, video_stream_snapshot, video_stream_start,
    video_stream_stop,
};
use database::LocalDatabase;
use tauri::Manager;

pub(crate) struct AppState {
    database: Arc<LocalDatabase>,
    listen_address: String,
    command_router: ground_station::CommandRouter,
    perception: ground_station::PerceptionStore,
    spatial: ground_station::SpatialStore,
    recording: recording::EvidenceRecorder,
    video: video::VideoManager,
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .setup(|app| {
            let listen_address = std::env::var("ATLAS_GROUND_STATION_LISTEN_ADDR")
                .unwrap_or_else(|_| "192.168.144.50:7443".to_string());
            let socket_address: SocketAddr = listen_address.parse().map_err(|error| {
                std::io::Error::other(format!("invalid ground-station listen address: {error}"))
            })?;
            let database =
                Arc::new(LocalDatabase::open(app.handle()).map_err(std::io::Error::other)?);

            println!(
                "Atlas local database ready: SQLite {}, journal={}, path={}",
                database.sqlite_version,
                database.journal_mode,
                database.path.display()
            );
            let server_database = Arc::clone(&database);
            let command_timeout_database = Arc::clone(&database);
            let command_router = ground_station::CommandRouter::default();
            let server_command_router = command_router.clone();
            let perception = ground_station::PerceptionStore::default();
            let server_perception = perception.clone();
            let spatial = ground_station::SpatialStore::default();
            let server_spatial = spatial.clone();
            let video = video::VideoManager::from_environment().map_err(std::io::Error::other)?;
            let recording = recording::EvidenceRecorder::from_environment(
                Arc::clone(&database),
                video.source_config(),
            )
            .map_err(std::io::Error::other)?;
            let retention_recording = recording.clone();
            let alert_database = Arc::clone(&database);
            let follow_database = Arc::clone(&database);
            let follow_router = command_router.clone();
            let alert_perception = perception.clone();
            let alert_video = video.clone();
            tauri::async_runtime::spawn(async move {
                if let Err(error) = ground_station::serve(
                    socket_address,
                    server_database,
                    server_command_router,
                    server_perception,
                    server_spatial,
                )
                .await
                {
                    eprintln!("{error}");
                }
            });
            tauri::async_runtime::spawn(async move {
                let mut interval = tokio::time::interval(std::time::Duration::from_secs(2));
                loop {
                    interval.tick().await;
                    let now = database::unix_time_ms();
                    if let Err(error) = alert_database.refresh_time_based_alerts(now) {
                        eprintln!("Refresh Atlas time-based alerts failed: {error}");
                    }
                    if let Err(error) = commands::refresh_media_operational_alerts(
                        &alert_database,
                        &alert_video,
                        &alert_perception,
                        now,
                    ) {
                        eprintln!("Refresh Atlas media alerts failed: {error}");
                    }
                }
            });
            tauri::async_runtime::spawn(async move {
                let mut interval = tokio::time::interval(std::time::Duration::from_secs(1));
                loop {
                    interval.tick().await;
                    if let Err(error) =
                        command_timeout_database.expire_vehicle_commands(database::unix_time_ms())
                    {
                        eprintln!("Expire Atlas vehicle commands failed: {error}");
                    }
                }
            });
            tauri::async_runtime::spawn(async move {
                let mut interval = tokio::time::interval(std::time::Duration::from_millis(250));
                loop {
                    interval.tick().await;
                    if let Err(error) = commands::run_aircraft_follow_watchdogs(
                        &follow_database,
                        &follow_router,
                        database::unix_time_ms(),
                    )
                    .await
                    {
                        eprintln!("Run Atlas aircraft follow watchdogs failed: {error}");
                    }
                }
            });
            tauri::async_runtime::spawn(async move {
                let mut interval = tokio::time::interval(std::time::Duration::from_secs(3_600));
                loop {
                    interval.tick().await;
                    if let Err(error) = retention_recording.apply_retention_policy() {
                        eprintln!("Apply Atlas evidence retention policy failed: {error}");
                    }
                }
            });
            if let Some(window) = app.get_webview_window("main") {
                let shutdown_video = video.clone();
                let shutdown_recording = recording.clone();
                window.on_window_event(move |event| {
                    if matches!(event, tauri::WindowEvent::Destroyed) {
                        shutdown_recording.shutdown();
                        let _ = shutdown_video.stop(None);
                    }
                });
            }
            app.manage(AppState {
                database,
                listen_address,
                command_router,
                perception,
                spatial,
                recording,
                video,
            });
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            runtime_info,
            evidence_recording_status,
            start_evidence_recording,
            stop_evidence_recording,
            capture_evidence_still,
            queue_evidence_event_clip,
            evidence_assets,
            evidence_asset,
            evidence_asset_content,
            review_evidence_asset,
            annotate_evidence_asset,
            update_evidence_asset_retention,
            trash_evidence_asset,
            restore_evidence_asset,
            evidence_retention_policy,
            update_evidence_retention_policy,
            create_aircraft_follow_session,
            renew_aircraft_follow_session,
            end_aircraft_follow_session,
            aircraft_follow_session,
            aircraft_follow_sessions,
            operational_alerts,
            acknowledge_operational_alert,
            perception_snapshot,
            spatial_snapshot,
            spatial_frame,
            spatial_subscription_start,
            spatial_subscription_renew,
            spatial_subscription_stop,
            perception_track_history,
            perception_counts,
            perception_counting_rules,
            upsert_perception_counting_rule,
            perception_track_selection,
            select_perception_track,
            clear_perception_track_selection,
            annotate_perception_track,
            perception_track_samples,
            perception_track_geolocations,
            operational_track_geolocations,
            refine_perception_track_geolocation,
            perception_frame_subscription_start,
            perception_frame_subscription_renew,
            perception_frame_subscription_stop,
            video_stream_start,
            video_stream_stop,
            video_stream_snapshot,
            video_stream_frame,
            fleet_snapshot,
            archive_drone,
            restore_drone,
            ground_station_snapshot,
            history_overview,
            incident_list,
            incident_detail,
            create_incident,
            update_incident,
            incident_response_aircraft_suitability,
            prepare_incident_response,
            preview_incident_response,
            abandon_prepared_response,
            mission_templates,
            mission_list,
            mission_detail,
            create_mission,
            update_mission,
            generate_mission_plan,
            apply_mission_terrain_profile,
            mission_plan,
            upload_mission,
            control_mission_run,
            mission_run_detail,
            mission_run_history,
            request_vehicle_command,
            vehicle_command_detail,
            vehicle_command_history,
            cancel_vehicle_command,
            vehicle_operations_snapshot,
            vehicle_telemetry_history,
            vehicle_telemetry_chart_series,
            vehicle_event_history
        ])
        .run(tauri::generate_context!())
        .expect("error while running Atlas");
}
