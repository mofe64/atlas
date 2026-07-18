mod commands;
mod database;
mod ground_station;
mod video;

use std::{net::SocketAddr, sync::Arc};

use commands::{
    apply_mission_terrain_profile, archive_drone, cancel_vehicle_command, control_mission_run,
    create_incident, create_mission, fleet_snapshot, generate_mission_plan,
    ground_station_snapshot, history_overview, incident_detail, incident_list, mission_detail,
    mission_list, mission_plan, mission_run_detail, mission_run_history, mission_templates,
    perception_frame_subscription_renew, perception_frame_subscription_start,
    perception_frame_subscription_stop, perception_snapshot, prepare_incident_response,
    request_vehicle_command, restore_drone, runtime_info, update_incident, update_mission,
    upload_mission, vehicle_command_detail, vehicle_command_history, vehicle_event_history,
    vehicle_operations_snapshot, vehicle_telemetry_chart_series, vehicle_telemetry_history,
    video_stream_frame, video_stream_snapshot, video_stream_start, video_stream_stop,
};
use database::LocalDatabase;
use tauri::Manager;

pub(crate) struct AppState {
    database: Arc<LocalDatabase>,
    listen_address: String,
    command_router: ground_station::CommandRouter,
    perception: ground_station::PerceptionStore,
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
            let video = video::VideoManager::from_environment().map_err(std::io::Error::other)?;
            tauri::async_runtime::spawn(async move {
                if let Err(error) = ground_station::serve(
                    socket_address,
                    server_database,
                    server_command_router,
                    server_perception,
                )
                .await
                {
                    eprintln!("{error}");
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
            if let Some(window) = app.get_webview_window("main") {
                let shutdown_video = video.clone();
                window.on_window_event(move |event| {
                    if matches!(event, tauri::WindowEvent::Destroyed) {
                        let _ = shutdown_video.stop(None);
                    }
                });
            }
            app.manage(AppState {
                database,
                listen_address,
                command_router,
                perception,
                video,
            });
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            runtime_info,
            perception_snapshot,
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
            prepare_incident_response,
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
