use std::{
    fs,
    net::TcpListener,
    path::PathBuf,
    process::{Child, Command, Stdio},
    sync::Arc,
    time::Duration,
};

use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;

use crate::database::LocalDatabase;

use super::{
    proto::pb::{
        self, agent_perception, agent_to_ground_station, ground_station_perception,
        ground_station_to_agent, AgentPerception, AgentToGroundStation,
    },
    server::serve,
    unix_time_ms, PerceptionStore,
};

#[tokio::test]
async fn grpc_registration_creates_and_closes_local_link() {
    let path = std::env::temp_dir().join(format!("atlas-grpc-test-{}.db", unix_time_ms()));
    let database = Arc::new(LocalDatabase::open_path(path.clone()).expect("open database"));
    let probe = TcpListener::bind("127.0.0.1:0").expect("reserve loopback port");
    let address = probe.local_addr().expect("read loopback address");
    drop(probe);

    let command_router = super::CommandRouter::default();
    let perception = PerceptionStore::default();
    let server = tokio::spawn(serve(
        address,
        Arc::clone(&database),
        command_router.clone(),
        perception.clone(),
    ));
    let endpoint = format!("http://{address}");
    let mut client = loop {
        match pb::ground_station_service_client::GroundStationServiceClient::connect(
            endpoint.clone(),
        )
        .await
        {
            Ok(client) => break client,
            Err(_) => tokio::time::sleep(Duration::from_millis(10)).await,
        }
    };

    let (outbound, requests) = mpsc::channel(4);
    let response = client
        .open_session(ReceiverStream::new(requests))
        .await
        .expect("open session");
    let mut responses = response.into_inner();
    outbound
        .send(AgentToGroundStation {
            session_id: "session-1".into(),
            payload: Some(agent_to_ground_station::Payload::Registration(
                pb::AgentRegistration {
                    registration_request_id: "request-1".into(),
                    installation_id: "agent-1".into(),
                    agent_version: "0.1.0".into(),
                    protocol_version: "1".into(),
                    device: Some(pb::DeviceProfile {
                        hostname: "test-pi".into(),
                        ..Default::default()
                    }),
                    drone: Some(pb::DroneProfile {
                        drone_id: "drone-1".into(),
                        name: "Atlas One".into(),
                        vehicle_type: "multicopter".into(),
                        ..Default::default()
                    }),
                    flight_controller: Some(pb::FlightControllerAttachment {
                        transport: "serial".into(),
                        ..Default::default()
                    }),
                    capabilities: vec![
                        "registration".into(),
                        "heartbeat".into(),
                        "telemetry".into(),
                        "status_text".into(),
                        "command:hold".into(),
                        "command:return_to_launch".into(),
                        "command:land".into(),
                    ],
                    observed_at_unix_ms: unix_time_ms(),
                },
            )),
        })
        .await
        .expect("send registration");

    let accepted = responses
        .message()
        .await
        .expect("read registration response")
        .expect("registration response")
        .payload;
    assert!(matches!(
        accepted,
        Some(ground_station_to_agent::Payload::RegistrationAccepted(_))
    ));
    outbound
        .send(AgentToGroundStation {
            session_id: "session-1".into(),
            payload: Some(agent_to_ground_station::Payload::Heartbeat(
                pb::AgentHeartbeat {
                    observed_at_unix_ms: unix_time_ms(),
                },
            )),
        })
        .await
        .expect("send heartbeat");
    outbound
        .send(AgentToGroundStation {
            session_id: "session-1".into(),
            payload: Some(agent_to_ground_station::Payload::Telemetry(
                pb::AircraftTelemetry {
                    observed_at_unix_ms: unix_time_ms(),
                    source: "mavsdk".into(),
                    battery_percent: Some(72.5),
                    relative_altitude_m: Some(10.0),
                    flight_mode: Some("HOLD".into()),
                    armed: Some(true),
                    in_air: Some(true),
                    gps_fix: Some("3D".into()),
                    satellites_visible: Some(12),
                    batteries: vec![pb::BatteryTelemetry {
                        id: 0,
                        function: "ALL".into(),
                        remaining_percent: Some(72.5),
                        voltage_v: Some(22.8),
                        current_a: Some(5.2),
                        temperature_c: Some(31.0),
                        consumed_ah: Some(1.4),
                        time_remaining_s: Some(1_200.0),
                    }],
                    health: Some(pb::VehicleHealth {
                        gyrometer_calibration_ok: true,
                        accelerometer_calibration_ok: true,
                        magnetometer_calibration_ok: true,
                        local_position_ok: true,
                        global_position_ok: true,
                        home_position_ok: true,
                        armable: true,
                    }),
                    landed_state: Some("ON_GROUND".into()),
                    rc_status: Some(pb::RcStatus {
                        available: true,
                        was_available_once: true,
                        signal_strength_percent: Some(76.0),
                    }),
                    ..Default::default()
                },
            )),
        })
        .await
        .expect("send telemetry");
    outbound
        .send(AgentToGroundStation {
            session_id: "session-1".into(),
            payload: Some(agent_to_ground_station::Payload::StatusText(
                pb::AgentStatusText {
                    observed_at_unix_ms: unix_time_ms(),
                    source: "mavsdk".into(),
                    severity: "WARNING".into(),
                    text: "Battery temperature high".into(),
                },
            )),
        })
        .await
        .expect("send status text");
    tokio::time::sleep(Duration::from_millis(20)).await;
    let connected = database.operations_snapshot().expect("connected snapshot");
    assert_eq!(connected.connection_status, "connected");
    let telemetry = connected.telemetry.expect("telemetry snapshot");
    assert_eq!(telemetry.battery_percent, Some(72.5));
    assert_eq!(telemetry.flight_mode.as_deref(), Some("HOLD"));
    assert_eq!(telemetry.batteries[0].voltage_v, Some(22.8));
    assert!(telemetry.health.expect("vehicle health").armable);
    assert_eq!(connected.status_events.len(), 1);
    assert_eq!(connected.status_events[0].severity, "WARNING");

    let (perception_outbound, perception_requests) = mpsc::channel(4);
    let perception_response = client
        .open_perception_stream(ReceiverStream::new(perception_requests))
        .await
        .expect("open perception stream");
    let mut perception_responses = perception_response.into_inner();
    perception_outbound
        .send(AgentPerception {
            session_id: "session-1".into(),
            drone_id: "drone-1".into(),
            payload: Some(agent_perception::Payload::Registration(
                pb::PerceptionStreamRegistration {
                    stream_id: "perception-stream-1".into(),
                    installation_id: "agent-1".into(),
                    protocol_version: "1".into(),
                    provider: "deepstream".into(),
                    capabilities: vec!["object_detection:v1".into()],
                    observed_at_unix_ms: unix_time_ms(),
                },
            )),
        })
        .await
        .expect("send perception registration");
    let perception_accepted = perception_responses
        .message()
        .await
        .expect("read perception registration response")
        .expect("perception registration response")
        .payload;
    assert!(matches!(
        perception_accepted,
        Some(ground_station_perception::Payload::StreamAccepted(_))
    ));
    perception_outbound
        .send(AgentPerception {
            session_id: "session-1".into(),
            drone_id: "drone-1".into(),
            payload: Some(agent_perception::Payload::Frame(pb::PerceptionFrame {
                source_id: "a8-main".into(),
                stream_epoch: "epoch-1".into(),
                frame_id: "frame-42".into(),
                observed_at_unix_ms: unix_time_ms(),
                source_pts_ns: 42_000,
                image_width: 1_920,
                image_height: 1_080,
                model: Some(pb::PerceptionModelIdentity {
                    name: "atlas-objects".into(),
                    version: "1".into(),
                    artifact_hash: "sha256:test".into(),
                }),
                inference_latency_ms: 8.5,
                detections: vec![pb::PerceptionDetection {
                    track_id: "track-7".into(),
                    class_id: 0,
                    class_label: "person".into(),
                    confidence: 0.91,
                    bounding_box: Some(pb::NormalizedBoundingBox {
                        x: 0.1,
                        y: 0.2,
                        width: 0.3,
                        height: 0.4,
                    }),
                    attributes_json: r#"{"risk":"low"}"#.into(),
                }],
            })),
        })
        .await
        .expect("send perception frame");
    perception_outbound
        .send(AgentPerception {
            session_id: "session-1".into(),
            drone_id: "drone-1".into(),
            payload: Some(agent_perception::Payload::Health(pb::PerceptionHealth {
                source_id: "a8-main".into(),
                provider: "deepstream".into(),
                accelerator: "jetson-orin".into(),
                input_connected: true,
                inference_ready: true,
                output_publishing: true,
                input_fps: 30.0,
                inference_fps: 20.0,
                dropped_frames: 2,
                last_frame_at_unix_ms: unix_time_ms(),
                last_detection_at_unix_ms: unix_time_ms(),
                last_error: String::new(),
                model: Some(pb::PerceptionModelIdentity {
                    name: "atlas-objects".into(),
                    version: "1".into(),
                    artifact_hash: String::new(),
                }),
                observed_at_unix_ms: unix_time_ms(),
            })),
        })
        .await
        .expect("send perception health");
    tokio::time::sleep(Duration::from_millis(20)).await;
    let perception_json = serde_json::to_value(
        perception
            .snapshot(Some("drone-1"))
            .expect("perception snapshot"),
    )
    .expect("serialize perception snapshot");
    assert_eq!(perception_json["provider"], "deepstream");
    assert_eq!(perception_json["sources"][0]["sourceId"], "a8-main");
    assert_eq!(
        perception_json["sources"][0]["latestFrame"]["frameId"],
        "frame-42"
    );
    assert_eq!(
        perception_json["sources"][0]["latestFrame"]["detections"][0]["classLabel"],
        "person"
    );
    assert_eq!(
        perception_json["sources"][0]["health"]["accelerator"],
        "jetson-orin"
    );

    let command = database
        .create_vehicle_command("drone-1", "hold", "{}", Some(15_000), unix_time_ms())
        .expect("create command");
    command_router
        .deliver(&database, &command)
        .await
        .expect("deliver command");
    let delivered = responses
        .message()
        .await
        .expect("read command delivery")
        .expect("command delivery");
    let delivered_command = match delivered.payload {
        Some(ground_station_to_agent::Payload::CommandRequest(command)) => command,
        other => panic!("expected command request, got {other:?}"),
    };
    assert_eq!(delivered_command.command_id, command.id);
    for (event_id, update_type) in [
        ("accepted-1", pb::VehicleCommandUpdateType::Accepted),
        ("executing-1", pb::VehicleCommandUpdateType::Executing),
        ("succeeded-1", pb::VehicleCommandUpdateType::Succeeded),
    ] {
        outbound
            .send(AgentToGroundStation {
                session_id: "session-1".into(),
                payload: Some(agent_to_ground_station::Payload::CommandUpdate(
                    pb::VehicleCommandUpdate {
                        event_id: event_id.into(),
                        command_id: command.id.clone(),
                        update_type: update_type as i32,
                        observed_at_unix_ms: unix_time_ms(),
                        result_code: if update_type == pb::VehicleCommandUpdateType::Succeeded {
                            "RESULT_SUCCESS".into()
                        } else {
                            String::new()
                        },
                        ..Default::default()
                    },
                )),
            })
            .await
            .expect("send command update");
    }
    tokio::time::sleep(Duration::from_millis(20)).await;
    assert_eq!(
        database
            .vehicle_command(&command.id)
            .expect("read completed command")
            .status,
        "succeeded"
    );

    drop(outbound);
    for _ in 0..20 {
        if database
            .operations_snapshot()
            .expect("disconnected snapshot")
            .connection_status
            == "disconnected"
        {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    assert_eq!(
        database
            .operations_snapshot()
            .expect("final snapshot")
            .connection_status,
        "disconnected"
    );

    server.abort();
    drop(database);
    remove_sqlite_files(&path);
}

fn remove_sqlite_files(path: &std::path::Path) {
    for candidate in [
        path.to_path_buf(),
        PathBuf::from(format!("{}-wal", path.display())),
        PathBuf::from(format!("{}-shm", path.display())),
    ] {
        let _ = fs::remove_file(candidate);
    }
}

struct ManagedChild(Child);

impl Drop for ManagedChild {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

async fn execute_sitl_command(
    database: &LocalDatabase,
    command_router: &super::CommandRouter,
    drone_id: &str,
    command_type: &str,
    parameters_json: &str,
) -> crate::database::VehicleCommandSnapshot {
    let command = database
        .create_vehicle_command(
            drone_id,
            command_type,
            parameters_json,
            Some(20_000),
            unix_time_ms(),
        )
        .unwrap_or_else(|error| panic!("create {command_type} command: {error}"));
    command_router
        .deliver(database, &command)
        .await
        .unwrap_or_else(|error| panic!("deliver {command_type} command: {error}"));
    for _ in 0..300 {
        let current = database
            .vehicle_command(&command.id)
            .expect("read SITL command state");
        if matches!(
            current.status.as_str(),
            "succeeded" | "failed" | "rejected" | "timed_out"
        ) {
            return current;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    panic!("{command_type} did not complete within 30 seconds")
}

#[tokio::test]
#[ignore = "requires PX4 SITL and mavsdk_server on 127.0.0.1:50051"]
async fn sitl_executes_hold_rtl_and_land_through_native_lifecycle() {
    let unique = unix_time_ms();
    let path = std::env::temp_dir().join(format!("atlas-sitl-command-{unique}.db"));
    let state_directory = std::env::temp_dir().join(format!("atlas-sitl-agent-{unique}"));
    fs::create_dir_all(&state_directory).expect("create SITL agent state directory");
    let database = Arc::new(LocalDatabase::open_path(path.clone()).expect("open SITL database"));
    let probe = TcpListener::bind("127.0.0.1:0").expect("reserve SITL ground-station port");
    let address = probe
        .local_addr()
        .expect("read SITL ground-station address");
    drop(probe);
    let command_router = super::CommandRouter::default();
    let server = tokio::spawn(serve(
        address,
        Arc::clone(&database),
        command_router.clone(),
        PerceptionStore::default(),
    ));

    let agent_directory = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../atlas-agent");
    let agent = Command::new("go")
        .args(["run", "./cmd/atlas-agent"])
        .current_dir(&agent_directory)
        .env("ATLAS_GROUND_STATION_ADDR", address.to_string())
        .env("ATLAS_AGENT_STATE_DIR", &state_directory)
        .env("ATLAS_MAVSDK_GRPC_ADDR", "127.0.0.1:50051")
        .env("ATLAS_DRONE_NAME", "Atlas SITL Command Drone")
        .env("ATLAS_VEHICLE_TYPE", "multicopter")
        .stdout(Stdio::inherit())
        .stderr(Stdio::inherit())
        .spawn()
        .expect("start Atlas Agent for SITL");
    let _agent = ManagedChild(agent);

    let mut registered_drone_id = None;
    for _ in 0..160 {
        if let Some(drone_id) = database
            .operations_snapshot()
            .expect("read SITL registration")
            .drone_id
        {
            registered_drone_id = Some(drone_id);
            break;
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
    let drone_id = registered_drone_id.expect("Atlas Agent did not register within 40 seconds");

    let fixture_status = Command::new("go")
        .args(["run", "./cmd/atlas-sitl-fixture"])
        .current_dir(&agent_directory)
        .env("ATLAS_MAVSDK_GRPC_ADDR", "127.0.0.1:50051")
        .status()
        .expect("run SITL takeoff fixture");
    assert!(fixture_status.success(), "SITL takeoff fixture failed");

    let mut airborne = false;
    for _ in 0..120 {
        if database
            .operations_snapshot_for(Some(&drone_id))
            .expect("read SITL telemetry")
            .telemetry
            .is_some_and(|telemetry| telemetry.in_air == Some(true))
        {
            airborne = true;
            break;
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
    assert!(airborne, "SITL drone did not report in-air telemetry");

    for command_type in ["hold", "return_to_launch", "land"] {
        let completed =
            execute_sitl_command(&database, &command_router, &drone_id, command_type, "{}").await;
        assert_eq!(
            completed.status, "succeeded",
            "{command_type} result: {} {}",
            completed.result_code, completed.result_message
        );
        assert!(completed
            .events
            .iter()
            .any(|event| event.event_type == "accepted"));
        assert!(completed
            .events
            .iter()
            .any(|event| event.event_type == "executing"));
    }

    server.abort();
    drop(database);
    remove_sqlite_files(&path);
    let _ = fs::remove_dir_all(&state_directory);
}
