use std::{
    fs,
    net::TcpListener,
    path::PathBuf,
    process::{Child, Command, Stdio},
    sync::Arc,
    time::Duration,
};

use serde_json::json;
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;

use crate::{
    database::{
        CreateIncidentInput, ExpandedPrepareIncidentResponseInput, LocalDatabase,
        MissionRunSnapshot, PrepareIncidentResponseInput, PreparedIncidentResponse,
    },
    video::VideoManager,
};

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

    let incident = database
        .create_incident(&CreateIncidentInput {
            incident_type: "Missing person".into(),
            priority: "high".into(),
            summary: "Search requested near the south trail".into(),
            description: "Last observed beside the wooded trail.".into(),
            latitude: Some(51.5),
            longitude: Some(-0.14),
            address: "South trail entrance".into(),
            area: "South sector".into(),
            occurred_at_unix_ms: Some(1_700_000_000_000),
        })
        .expect("create response incident");
    let prepared = database
        .prepare_incident_response(
            &incident.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                drone_id: "drone-1".into(),
                staging_latitude: 51.501,
                staging_longitude: -0.141,
                altitude_meters: 35.0,
                speed_mps: 6.0,
                arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
                point_gimbal_at_incident: false,
                incident_target_altitude_amsl_meters: None,
            },
        )
        .expect("prepare response action");
    let dispatch = database
        .create_mission_run(&prepared.mission.id, "drone-1", Some(&prepared.plan.id))
        .expect("create response run");
    assert!(command_router
        .deliver_mission_reconciliation(&database, "drone-1")
        .await
        .expect("deliver durable mission reconciliation"));
    let reconciliation = responses
        .message()
        .await
        .expect("read reconciliation delivery")
        .expect("reconciliation delivery");
    let reconciliation = match reconciliation.payload {
        Some(ground_station_to_agent::Payload::MissionReconciliationRequest(request)) => request,
        other => panic!("expected mission reconciliation request, got {other:?}"),
    };
    assert_eq!(reconciliation.mission_run_id, dispatch.run.id);
    assert_eq!(reconciliation.run_state, "UPLOADING");
    assert_eq!(reconciliation.actions.len(), 1);
    assert_eq!(reconciliation.actions[0].state, "REQUESTED");
    assert!(reconciliation.mission_plan_json.contains("HOLD_AT_ARRIVAL"));
    outbound
        .send(AgentToGroundStation {
            session_id: "session-1".into(),
            payload: Some(agent_to_ground_station::Payload::MissionRunUpdate(
                pb::MissionRunUpdate {
                    event_id: "reconciliation-accepted-over-grpc".into(),
                    operation_id: reconciliation.reconciliation_id,
                    mission_run_id: dispatch.run.id.clone(),
                    update_type: pb::MissionRunUpdateType::ReconciliationAccepted as i32,
                    run_state: "READY".into(),
                    observed_at_unix_ms: unix_time_ms(),
                    message: "Onboard plan verified".into(),
                    ..Default::default()
                },
            )),
        })
        .await
        .expect("send reconciliation acknowledgement");
    tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            if database
                .mission_run(&dispatch.run.id)
                .expect("read reconciled run")
                .status
                == "READY"
            {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    })
    .await
    .expect("reconciliation should update the durable run");
    outbound
        .send(AgentToGroundStation {
            session_id: "session-1".into(),
            payload: Some(agent_to_ground_station::Payload::MissionRunUpdate(
                pb::MissionRunUpdate {
                    event_id: "hold-running-over-grpc".into(),
                    mission_run_id: dispatch.run.id.clone(),
                    update_type: pb::MissionRunUpdateType::ActionStateChanged as i32,
                    run_state: "UPLOADING".into(),
                    observed_at_unix_ms: unix_time_ms(),
                    action_sequence: Some(dispatch.run.actions[0].action_sequence),
                    action_type: "HOLD_AT_ARRIVAL".into(),
                    action_state: pb::MissionActionState::Running as i32,
                    action_attempt: 1,
                    failure_policy: "RETURN_TO_LAUNCH".into(),
                    message: "Executing reviewed arrival Hold".into(),
                    ..Default::default()
                },
            )),
        })
        .await
        .expect("send mission action state");
    tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            let run = database
                .mission_run(&dispatch.run.id)
                .expect("read response action");
            if run.actions[0].state == "RUNNING" {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    })
    .await
    .expect("mission action state should cross the gRPC boundary");

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
                activation_state: "ACTIVE".into(),
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
                tracking: Some(pb::PerceptionTrackingHealth {
                    algorithm: "BYTE_TRACK_CMC".into(),
                    state: "ACTIVE".into(),
                    session_id: "tracking-session-1".into(),
                    last_reset_reason: "PERCEPTION_ACTIVATED".into(),
                    reset_count: 1,
                    last_error: String::new(),
                    camera_motion_state: "ACTIVE".into(),
                    camera_motion_method: "SPARSE_OPTICAL_FLOW".into(),
                    camera_motion_confidence: 0.91,
                    re_id_enabled: false,
                }),
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
    assert_eq!(
        perception_json["sources"][0]["health"]["tracking"]["sessionId"],
        "tracking-session-1"
    );
    assert_eq!(
        perception_json["sources"][0]["health"]["tracking"]["cameraMotionState"],
        "ACTIVE"
    );
    assert_eq!(
        perception_json["sources"][0]["health"]["tracking"]["reIdEnabled"],
        false
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

fn spawn_sitl_agent(
    agent_binary: &std::path::Path,
    agent_directory: &std::path::Path,
    state_directory: &std::path::Path,
    ground_station_address: std::net::SocketAddr,
) -> ManagedChild {
    let mavsdk_address = std::env::var("ATLAS_TEST_SITL_MAVSDK_ADDR")
        .unwrap_or_else(|_| "127.0.0.1:50051".to_string());
    ManagedChild(
        Command::new(agent_binary)
            .current_dir(agent_directory)
            .env(
                "ATLAS_GROUND_STATION_ADDR",
                ground_station_address.to_string(),
            )
            .env("ATLAS_AGENT_STATE_DIR", state_directory)
            .env("ATLAS_MAVSDK_GRPC_ADDR", mavsdk_address)
            .env("ATLAS_DRONE_NAME", "Atlas SITL Acceptance Drone")
            .env("ATLAS_VEHICLE_TYPE", "multicopter")
            .env("ATLAS_CAMERA_TRANSPORT", "mavsdk")
            .env("ATLAS_FLIGHT_CONTROLLER_TRANSPORT", "udp")
            .env(
                "ATLAS_FLIGHT_CONTROLLER_ENDPOINT",
                "PX4 SITL via mavsdk_server",
            )
            .env("ATLAS_FLIGHT_CONTROLLER_BAUD_RATE", "0")
            .stdout(Stdio::inherit())
            .stderr(Stdio::inherit())
            .spawn()
            .expect("start Atlas Agent for reconciliation SITL"),
    )
}

async fn wait_for_mission_run_state(
    database: &LocalDatabase,
    run_id: &str,
    expected: &str,
    attempts: usize,
) -> crate::database::MissionRunSnapshot {
    for _ in 0..attempts {
        let run = database
            .mission_run(run_id)
            .expect("read SITL mission run state");
        if run.status == expected {
            return run;
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
    let run = database
        .mission_run(run_id)
        .expect("read timed out SITL mission run state");
    panic!(
        "mission run {} remained in {} instead of reaching {expected}: {} {}",
        run.id, run.status, run.error_code, run.error_message
    )
}

#[tokio::test]
#[ignore = "requires PX4 SITL and mavsdk_server on 127.0.0.1:50051"]
async fn sitl_reconciles_native_run_after_agent_process_restart() {
    let unique = unix_time_ms();
    let path = std::env::temp_dir().join(format!("atlas-sitl-reconcile-{unique}.db"));
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
    let agent_binary = state_directory.join("atlas-agent-sitl");
    let build_status = Command::new("go")
        .args(["build", "-o"])
        .arg(&agent_binary)
        .arg("./cmd/atlas-agent")
        .current_dir(&agent_directory)
        .status()
        .expect("build directly managed Atlas Agent binary");
    assert!(build_status.success(), "Atlas Agent build failed");
    let mut first_agent =
        spawn_sitl_agent(&agent_binary, &agent_directory, &state_directory, address);

    let mut drone_id = None;
    for _ in 0..160 {
        if let Some(registered) = database
            .operations_snapshot()
            .expect("read SITL registration")
            .drone_id
        {
            drone_id = Some(registered);
            break;
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
    let drone_id = drone_id.expect("Atlas Agent did not register within 40 seconds");
    let incident = database
        .create_incident(&CreateIncidentInput {
            incident_type: "SITL reconciliation".into(),
            priority: "high".into(),
            summary: "Verify a response survives Agent restart".into(),
            description: "Live PX4 reconciliation acceptance".into(),
            latitude: Some(37.413),
            longitude: Some(-121.9988),
            address: "Baylands SITL".into(),
            area: "Acceptance corridor".into(),
            occurred_at_unix_ms: Some(unix_time_ms()),
        })
        .expect("create SITL response incident");
    let prepared = database
        .prepare_incident_response(
            &incident.incident.id,
            &PrepareIncidentResponseInput {
                expected_incident_revision: incident.incident.revision,
                drone_id: drone_id.clone(),
                staging_latitude: 37.413,
                staging_longitude: -121.9988,
                altitude_meters: 20.0,
                speed_mps: 2.0,
                arrival_failure_policy: "RETURN_TO_LAUNCH".into(),
                point_gimbal_at_incident: false,
                incident_target_altitude_amsl_meters: None,
            },
        )
        .expect("prepare SITL response");
    let upload = database
        .create_mission_run(&prepared.mission.id, &drone_id, Some(&prepared.plan.id))
        .expect("create SITL response run");
    let mission_plan_json = crate::database::mission_plan_json_for_agent(prepared.plan)
        .expect("serialize SITL response plan");
    let requested_at = unix_time_ms();
    command_router
        .deliver_mission_operation(
            &drone_id,
            &upload.operation_id,
            &upload.run.id,
            "upload",
            Some(&mission_plan_json),
            requested_at,
            requested_at + 120_000,
        )
        .await
        .expect("deliver SITL mission upload");
    wait_for_mission_run_state(&database, &upload.run.id, "READY", 160).await;
    let start = database
        .record_mission_operation_requested(&upload.run.id, "start")
        .expect("request SITL mission start");
    let requested_at = unix_time_ms();
    command_router
        .deliver_mission_operation(
            &drone_id,
            &start.operation_id,
            &start.run.id,
            "start",
            None,
            requested_at,
            requested_at + 30_000,
        )
        .await
        .expect("deliver SITL mission start");
    let running = wait_for_mission_run_state(&database, &upload.run.id, "RUNNING", 120).await;
    assert_eq!(running.actions[0].state, "REQUESTED");

    first_agent
        .0
        .kill()
        .expect("stop first Atlas Agent process");
    first_agent
        .0
        .wait()
        .expect("wait for first Atlas Agent process");
    drop(first_agent);
    assert_eq!(
        database
            .mission_reconciliation(&drone_id)
            .expect("build restart checkpoint")
            .expect("unfinished run must reconcile")
            .run
            .id,
        upload.run.id
    );

    let _restarted_agent =
        spawn_sitl_agent(&agent_binary, &agent_directory, &state_directory, address);
    let mut reconciliation_seen = false;
    let mut completed = None;
    for _ in 0..600 {
        let run = database
            .mission_run(&upload.run.id)
            .expect("read reconciled SITL run");
        reconciliation_seen |= run
            .events
            .iter()
            .any(|event| event.event_type == "reconciliation_accepted");
        if run.status == "COMPLETED" {
            completed = Some(run);
            break;
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
    let completed =
        completed.expect("restarted Agent did not complete the PX4 mission in 150 seconds");
    assert!(
        reconciliation_seen,
        "Native never recorded reconciliation acceptance"
    );
    assert_eq!(completed.actions[0].state, "SUCCEEDED");
    assert_eq!(completed.actions[0].attempt, 1);
    assert_eq!(
        completed.actions[0]
            .events
            .iter()
            .map(|event| event.state.as_str())
            .collect::<Vec<_>>(),
        vec!["REQUESTED", "RUNNING", "SUCCEEDED"],
        "restart must execute only the attempt permitted by Native's durable checkpoint"
    );
    assert_eq!(
        completed
            .events
            .iter()
            .filter(|event| event.event_type == "completed")
            .count(),
        1,
        "the reconciled run must complete exactly once"
    );
    assert!(completed.current_waypoint.is_some());
    let rtl = execute_sitl_command(
        &database,
        &command_router,
        &drone_id,
        "return_to_launch",
        "{}",
    )
    .await;
    assert_eq!(rtl.status, "succeeded");

    server.abort();
    drop(database);
    remove_sqlite_files(&path);
    let _ = fs::remove_dir_all(&state_directory);
}

#[derive(Debug, Clone, Copy)]
struct SitlOrigin {
    latitude: f64,
    longitude: f64,
    absolute_altitude_meters: f64,
}

fn offset_sitl_coordinate(origin: SitlOrigin, north_meters: f64, east_meters: f64) -> (f64, f64) {
    const METERS_PER_LATITUDE_DEGREE: f64 = 111_111.0;
    let latitude = origin.latitude + north_meters / METERS_PER_LATITUDE_DEGREE;
    let longitude = origin.longitude
        + east_meters
            / (METERS_PER_LATITUDE_DEGREE * origin.latitude.to_radians().cos().abs().max(0.01));
    (latitude, longitude)
}

async fn wait_for_sitl_aircraft_ready(database: &LocalDatabase) -> (String, SitlOrigin) {
    for _ in 0..400 {
        let snapshot = database
            .operations_snapshot()
            .expect("read SITL aircraft readiness");
        if snapshot.connection_status == "connected" {
            if let (Some(drone_id), Some(telemetry)) = (snapshot.drone_id, snapshot.telemetry) {
                let home = telemetry.home_position.as_ref();
                let latitude = home
                    .and_then(|position| position.latitude)
                    .or(telemetry.latitude);
                let longitude = home
                    .and_then(|position| position.longitude)
                    .or(telemetry.longitude);
                let absolute_altitude_meters = home
                    .and_then(|position| position.absolute_altitude_m)
                    .or(telemetry.absolute_altitude_m)
                    .unwrap_or(0.0);
                let health_ready = telemetry.health.as_ref().is_some_and(|health| {
                    health.armable
                        && health.local_position_ok
                        && health.global_position_ok
                        && health.home_position_ok
                });
                if telemetry.armed == Some(false)
                    && telemetry.in_air == Some(false)
                    && health_ready
                    && latitude.is_some_and(f64::is_finite)
                    && longitude.is_some_and(f64::is_finite)
                {
                    return (
                        drone_id,
                        SitlOrigin {
                            latitude: latitude.expect("checked latitude"),
                            longitude: longitude.expect("checked longitude"),
                            absolute_altitude_meters,
                        },
                    );
                }
            }
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
    panic!("Atlas Agent did not report a connected, grounded, armable SITL aircraft in 100 seconds")
}

async fn wait_for_sitl_airborne(database: &LocalDatabase, drone_id: &str, run_id: &str) {
    for _ in 0..360 {
        let run = database
            .mission_run(run_id)
            .expect("read SITL run while waiting for takeoff");
        assert!(
            !matches!(run.status.as_str(), "FAILED" | "CANCELLED" | "RTL"),
            "mission {} entered {} before airborne telemetry: {} {}",
            run.id,
            run.status,
            run.error_code,
            run.error_message
        );
        if database
            .operations_snapshot_for(Some(drone_id))
            .expect("read SITL airborne telemetry")
            .telemetry
            .is_some_and(|telemetry| telemetry.in_air == Some(true))
        {
            return;
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
    panic!("SITL aircraft did not report in-air telemetry within 90 seconds")
}

async fn wait_for_sitl_grounded(database: &LocalDatabase, drone_id: &str) {
    for _ in 0..600 {
        if database
            .operations_snapshot_for(Some(drone_id))
            .expect("read SITL landing telemetry")
            .telemetry
            .is_some_and(|telemetry| {
                telemetry.in_air == Some(false) && telemetry.armed == Some(false)
            })
        {
            return;
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
    panic!("SITL aircraft did not land and disarm within 150 seconds")
}

fn video_packet_sequence(packet: &[u8]) -> u64 {
    assert!(packet.len() >= 12, "video packet is too short");
    assert_eq!(&packet[..4], b"ATV1", "video packet magic changed");
    let header_length = u32::from_le_bytes(
        packet[4..8]
            .try_into()
            .expect("video packet header length bytes"),
    ) as usize;
    let jpeg_offset = 8 + header_length;
    assert!(
        jpeg_offset < packet.len(),
        "video packet has no JPEG payload"
    );
    let header: serde_json::Value =
        serde_json::from_slice(&packet[8..jpeg_offset]).expect("decode video packet header");
    let jpeg = &packet[jpeg_offset..];
    assert!(
        jpeg.starts_with(&[0xff, 0xd8]) && jpeg.ends_with(&[0xff, 0xd9]),
        "video packet did not contain a complete JPEG frame"
    );
    header["sequence"].as_u64().expect("video packet sequence")
}

async fn pull_next_sitl_video_frame(
    video: &VideoManager,
    perception: &PerceptionStore,
    drone_id: &str,
    after_sequence: u64,
    phase: &str,
) -> u64 {
    for _ in 0..300 {
        let packet = video
            .frame_packet(perception, drone_id, after_sequence)
            .unwrap_or_else(|error| panic!("pull {phase} SITL video frame: {error}"));
        if !packet.is_empty() {
            let sequence = video_packet_sequence(&packet);
            assert!(
                sequence > after_sequence,
                "{phase} video frame did not advance beyond sequence {after_sequence}"
            );
            println!("SITL video {phase}: decoded frame sequence {sequence}");
            return sequence;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    let snapshot = video.snapshot().expect("read timed-out video state");
    panic!(
        "SITL video produced no new {phase} frame in 30 seconds: status={} last_frame={:?} error={:?}",
        snapshot.status, snapshot.last_frame_at_unix_ms, snapshot.last_error
    )
}

fn expanded_sitl_response_input(
    incident_revision: u32,
    drone_id: &str,
    geometry: serde_json::Value,
    incident_target_altitude_amsl_meters: Option<f64>,
) -> ExpandedPrepareIncidentResponseInput {
    serde_json::from_value(json!({
        "expectedIncidentRevision": incident_revision,
        "droneId": drone_id,
        "geometry": geometry,
        "arrivalFailurePolicy": "RETURN_TO_LAUNCH",
        "incidentTargetAltitudeAmslMeters": incident_target_altitude_amsl_meters,
        "buildingHorizontalClearanceMeters": 10.0,
        "buildingVerticalClearanceMeters": 5.0,
        "knownBuildingOverrideReason": "No licensed known-building dataset is configured for this isolated SITL acceptance run"
    }))
    .expect("build expanded SITL response input")
}

fn create_sitl_response_incident(
    database: &LocalDatabase,
    label: &str,
    latitude: f64,
    longitude: f64,
) -> crate::database::IncidentDetailSnapshot {
    database
        .create_incident(&CreateIncidentInput {
            incident_type: "SITL public-safety acceptance".into(),
            priority: "high".into(),
            summary: format!("{label} live flight acceptance"),
            description:
                "Generated response pattern flown through Native, Agent, MAVSDK, and PX4 SITL."
                    .into(),
            latitude: Some(latitude),
            longitude: Some(longitude),
            address: "PX4 SITL test range".into(),
            area: "Isolated acceptance volume".into(),
            occurred_at_unix_ms: Some(unix_time_ms()),
        })
        .unwrap_or_else(|error| panic!("create {label} SITL incident: {error}"))
}

async fn upload_sitl_response(
    database: &LocalDatabase,
    command_router: &super::CommandRouter,
    drone_id: &str,
    prepared: &PreparedIncidentResponse,
) -> MissionRunSnapshot {
    let upload = database
        .create_mission_run(&prepared.mission.id, drone_id, Some(&prepared.plan.id))
        .expect("create expanded SITL mission run");
    let plan_json = crate::database::mission_plan_json_for_agent(prepared.plan.clone())
        .expect("serialize expanded SITL mission plan");
    let requested_at = unix_time_ms();
    command_router
        .deliver_mission_operation(
            drone_id,
            &upload.operation_id,
            &upload.run.id,
            "upload",
            Some(&plan_json),
            requested_at,
            requested_at + 120_000,
        )
        .await
        .expect("deliver expanded SITL mission upload");
    wait_for_mission_run_state(database, &upload.run.id, "READY", 480).await
}

async fn start_sitl_response(
    database: &LocalDatabase,
    command_router: &super::CommandRouter,
    run_id: &str,
) {
    let start = database
        .record_mission_operation_requested(run_id, "start")
        .expect("request expanded SITL mission start");
    let requested_at = unix_time_ms();
    command_router
        .deliver_mission_operation(
            &start.run.drone_id,
            &start.operation_id,
            run_id,
            "start",
            None,
            requested_at,
            requested_at + 30_000,
        )
        .await
        .expect("deliver expanded SITL mission start");
}

fn assert_sitl_action_chain(run: &MissionRunSnapshot, expected_types: &[&str]) {
    assert_eq!(
        run.actions
            .iter()
            .map(|action| action.action_type.as_str())
            .collect::<Vec<_>>(),
        expected_types,
        "unexpected durable arrival-action chain"
    );
    for action in &run.actions {
        assert_eq!(
            action.state, "SUCCEEDED",
            "{} did not complete: {} {}",
            action.action_type, action.error_code, action.error_message
        );
        assert!(
            action.events.iter().any(|event| event.state == "RUNNING"),
            "{} never entered RUNNING",
            action.action_type
        );
        assert_eq!(
            action.events.last().map(|event| event.state.as_str()),
            Some("SUCCEEDED"),
            "{} did not end with an acknowledged success event",
            action.action_type
        );
    }
    for pair in run.actions.windows(2) {
        assert!(
            pair[0].completed_at_unix_ms.expect("completed action time")
                <= pair[1].started_at_unix_ms.expect("next action start time"),
            "{} and {} executed out of order",
            pair[0].action_type,
            pair[1].action_type
        );
    }
}

fn assert_sitl_completed_route(run: &MissionRunSnapshot, expected_waypoints: usize) {
    assert_eq!(run.status, "COMPLETED");
    assert_eq!(run.total_waypoints as usize, expected_waypoints);
    assert_eq!(run.current_waypoint, Some(run.total_waypoints));
    assert_eq!(run.progress_percent, 100.0);

    let resume_completed_at = run
        .actions
        .iter()
        .find(|action| action.action_type == "RESUME_AFTER_ARRIVAL")
        .and_then(|action| action.completed_at_unix_ms)
        .expect("completed Resume action");
    let arrival_progress = run
        .events
        .iter()
        .find(|event| {
            event.event_type == "progress"
                && event.current_waypoint.is_some_and(|current| current > 0)
        })
        .expect("cumulative progress beyond the first generated waypoint");
    let hold_started_at = run.actions[0]
        .started_at_unix_ms
        .expect("arrival Hold start time");
    assert!(
        arrival_progress.occurred_at_unix_ms <= hold_started_at,
        "waypoint 0 must trigger Hold before the operational pattern resumes"
    );

    // MAVSDK's `current` is a cumulative count of sequential mission items,
    // not a guaranteed callback for every integer. A transition from 5 to 7
    // therefore proves that generated waypoints 5 and 6 were both passed even
    // when the transient `current = 6` notification was coalesced.
    let cumulative_events = run
        .events
        .iter()
        .filter(|event| {
            matches!(event.event_type.as_str(), "progress" | "completed")
                && event.current_waypoint.is_some()
        })
        .collect::<Vec<_>>();
    let mut previous_current = 0;
    for event in &cumulative_events {
        let current = event.current_waypoint.expect("filtered current waypoint");
        assert!(
            current >= previous_current,
            "MAVSDK mission progress regressed from {previous_current} to {current}"
        );
        previous_current = current;
    }
    assert_eq!(
        previous_current, run.total_waypoints,
        "cumulative mission progress did not cover the generated route"
    );
    for waypoint_sequence in 0..run.total_waypoints {
        let covering_event = cumulative_events
            .iter()
            .find(|event| {
                event
                    .current_waypoint
                    .is_some_and(|current| current > waypoint_sequence)
            })
            .unwrap_or_else(|| panic!("generated waypoint {waypoint_sequence} was not passed"));
        if waypoint_sequence > 0 {
            assert!(
                covering_event.occurred_at_unix_ms >= resume_completed_at,
                "generated waypoint {waypoint_sequence} was passed before Resume"
            );
        }
    }
    assert_eq!(
        run.events
            .iter()
            .filter(|event| event.event_type == "completed")
            .count(),
        1,
        "mission must complete exactly once"
    );
}

async fn return_sitl_run_to_launch(
    database: &LocalDatabase,
    command_router: &super::CommandRouter,
    drone_id: &str,
    run_id: &str,
) {
    let run = database
        .mission_run(run_id)
        .expect("read SITL run before RTL cleanup");
    if matches!(run.status.as_str(), "RUNNING" | "PAUSED") {
        let rtl = database
            .record_mission_operation_requested(run_id, "return_to_launch")
            .expect("request mission RTL cleanup");
        let requested_at = unix_time_ms();
        command_router
            .deliver_mission_operation(
                drone_id,
                &rtl.operation_id,
                run_id,
                "return_to_launch",
                None,
                requested_at,
                requested_at + 30_000,
            )
            .await
            .expect("deliver mission RTL cleanup");
        wait_for_mission_run_state(database, run_id, "RTL", 240).await;
    } else {
        let rtl =
            execute_sitl_command(database, command_router, drone_id, "return_to_launch", "{}")
                .await;
        assert_eq!(
            rtl.status, "succeeded",
            "RTL cleanup failed: {} {}",
            rtl.result_code, rtl.result_message
        );
    }
    wait_for_sitl_grounded(database, drone_id).await;
    let (ready_drone_id, _) = wait_for_sitl_aircraft_ready(database).await;
    assert_eq!(ready_drone_id, drone_id);
}

#[tokio::test]
#[ignore = "requires PX4 gz_x500_gimbal SITL, mavsdk_server, and local RTSP video; run scripts/test-sitl-response-patterns.sh"]
async fn sitl_flies_response_patterns_with_continuous_video() {
    let rtsp_url = std::env::var("ATLAS_VIDEO_RTSP_URL").expect(
        "set ATLAS_VIDEO_RTSP_URL to the SITL stream, or use scripts/test-sitl-response-patterns.sh",
    );
    assert!(
        rtsp_url.starts_with("rtsp://") || rtsp_url.starts_with("rtsps://"),
        "SITL acceptance requires an RTSP video URL"
    );

    let unique = unix_time_ms();
    let path = std::env::temp_dir().join(format!("atlas-sitl-patterns-{unique}.db"));
    let state_directory = std::env::temp_dir().join(format!("atlas-sitl-patterns-{unique}"));
    fs::create_dir_all(&state_directory).expect("create pattern SITL Agent state directory");
    let database =
        Arc::new(LocalDatabase::open_path(path.clone()).expect("open pattern SITL database"));
    let probe = TcpListener::bind("127.0.0.1:0").expect("reserve pattern SITL ground-station port");
    let address = probe
        .local_addr()
        .expect("read pattern SITL ground-station address");
    drop(probe);
    let command_router = super::CommandRouter::default();
    let perception = PerceptionStore::default();
    let server = tokio::spawn(serve(
        address,
        Arc::clone(&database),
        command_router.clone(),
        perception.clone(),
    ));

    let agent_directory = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../atlas-agent");
    let agent_binary = state_directory.join("atlas-agent-sitl-patterns");
    let build_status = Command::new("go")
        .args(["build", "-o"])
        .arg(&agent_binary)
        .arg("./cmd/atlas-agent")
        .current_dir(&agent_directory)
        .status()
        .expect("build Atlas Agent for response-pattern SITL");
    assert!(build_status.success(), "Atlas Agent build failed");
    let _agent = spawn_sitl_agent(&agent_binary, &agent_directory, &state_directory, address);

    let (drone_id, origin) = wait_for_sitl_aircraft_ready(&database).await;
    let video = VideoManager::from_environment().expect("configure Native SITL video decoder");
    println!("SITL video source: {}", video.source_config().rtsp_url);
    video
        .start(&drone_id)
        .expect("start Native SITL video decoder");
    let mut video_sequence = 0;

    let incident_coordinate = offset_sitl_coordinate(origin, 28.0, 0.0);
    let staging_coordinate = offset_sitl_coordinate(origin, 18.0, -6.0);
    let staging_incident = create_sitl_response_incident(
        &database,
        "Hold at Staging",
        incident_coordinate.0,
        incident_coordinate.1,
    );
    let staging_input = expanded_sitl_response_input(
        staging_incident.incident.revision,
        &drone_id,
        json!({
            "responsePattern": "HOLD_AT_STAGING",
            "stagingLatitude": staging_coordinate.0,
            "stagingLongitude": staging_coordinate.1,
            "altitudeMeters": 12.0,
            "speedMps": 3.0
        }),
        None,
    );
    let staging_prepared = database
        .prepare_expanded_incident_response_with_dataset(
            &staging_incident.incident.id,
            &staging_input,
            None,
        )
        .expect("prepare Hold at Staging SITL response");
    assert_eq!(staging_prepared.plan.generated_waypoints.len(), 1);
    let staging_ready =
        upload_sitl_response(&database, &command_router, &drone_id, &staging_prepared).await;
    video_sequence = pull_next_sitl_video_frame(
        &video,
        &perception,
        &drone_id,
        video_sequence,
        "before Hold at Staging",
    )
    .await;
    start_sitl_response(&database, &command_router, &staging_ready.id).await;
    wait_for_sitl_airborne(&database, &drone_id, &staging_ready.id).await;
    video_sequence = pull_next_sitl_video_frame(
        &video,
        &perception,
        &drone_id,
        video_sequence,
        "during Hold at Staging",
    )
    .await;
    let staged = wait_for_mission_run_state(&database, &staging_ready.id, "PAUSED", 720).await;
    let staged_incident = database
        .incident(&staging_incident.incident.id)
        .expect("read staged SITL incident");
    return_sitl_run_to_launch(&database, &command_router, &drone_id, &staging_ready.id).await;
    assert_sitl_action_chain(&staged, &["HOLD_AT_ARRIVAL"]);
    assert_eq!(staged.current_waypoint, Some(1));
    assert_eq!(staged_incident.assignments[0].status, "STAGED");
    assert_eq!(staged_incident.assignments[0].on_scene_at_unix_ms, None);
    assert!(staged_incident
        .events
        .iter()
        .any(|event| event.event_type == "response_staged"));
    assert!(!staged_incident
        .events
        .iter()
        .any(|event| event.event_type == "response_on_scene"));
    assert!(!staged.actions.iter().any(|action| {
        action.action_type == "POINT_GIMBAL_AT_INCIDENT"
            || action.events.iter().any(|event| {
                event
                    .message
                    .to_ascii_lowercase()
                    .contains("gimbal at incident")
            })
    }));

    let area_center = offset_sitl_coordinate(origin, 28.0, 0.0);
    let area_polygon = [
        offset_sitl_coordinate(origin, 18.0, -12.0),
        offset_sitl_coordinate(origin, 18.0, 12.0),
        offset_sitl_coordinate(origin, 38.0, 12.0),
        offset_sitl_coordinate(origin, 38.0, -12.0),
    ];
    let area_incident =
        create_sitl_response_incident(&database, "Bounded Area Scan", area_center.0, area_center.1);
    let area_input = expanded_sitl_response_input(
        area_incident.incident.revision,
        &drone_id,
        json!({
            "responsePattern": "BOUNDED_AREA_SCAN",
            "areaPolygon": area_polygon
                .iter()
                .map(|(latitude, longitude)| json!({"latitude": latitude, "longitude": longitude}))
                .collect::<Vec<_>>(),
            "altitudeMeters": 12.0,
            "speedMps": 3.0,
            "laneSpacingMeters": 7.0,
            "sweepAngleDegrees": 0.0
        }),
        None,
    );
    let area_prepared = database
        .prepare_expanded_incident_response_with_dataset(
            &area_incident.incident.id,
            &area_input,
            None,
        )
        .expect("prepare Bounded Area Scan SITL response");
    let area_waypoints = area_prepared.plan.generated_waypoints.len();
    assert!(
        area_waypoints > 2,
        "Area Scan must contain a lawnmower route"
    );
    let area_ready =
        upload_sitl_response(&database, &command_router, &drone_id, &area_prepared).await;
    video_sequence = pull_next_sitl_video_frame(
        &video,
        &perception,
        &drone_id,
        video_sequence,
        "before Bounded Area Scan",
    )
    .await;
    start_sitl_response(&database, &command_router, &area_ready.id).await;
    wait_for_sitl_airborne(&database, &drone_id, &area_ready.id).await;
    video_sequence = pull_next_sitl_video_frame(
        &video,
        &perception,
        &drone_id,
        video_sequence,
        "during Bounded Area Scan",
    )
    .await;
    let area_completed =
        wait_for_mission_run_state(&database, &area_ready.id, "COMPLETED", 1_200).await;
    return_sitl_run_to_launch(&database, &command_router, &drone_id, &area_ready.id).await;
    assert_sitl_action_chain(
        &area_completed,
        &["HOLD_AT_ARRIVAL", "RESUME_AFTER_ARRIVAL"],
    );
    assert_sitl_completed_route(&area_completed, area_waypoints);

    let orbit_center = offset_sitl_coordinate(origin, 28.0, 0.0);
    let orbit_incident = create_sitl_response_incident(
        &database,
        "Single-level Bounded Orbit",
        orbit_center.0,
        orbit_center.1,
    );
    let orbit_input = expanded_sitl_response_input(
        orbit_incident.incident.revision,
        &drone_id,
        json!({
            "responsePattern": "BOUNDED_ORBIT",
            "centerLatitude": orbit_center.0,
            "centerLongitude": orbit_center.1,
            "radiusMeters": 10.0,
            "altitudeLevelsMeters": [14.0],
            "speedMps": 3.0,
            "lapsPerLevel": 1,
            "direction": "CLOCKWISE",
            "maxVerticalRateMps": 1.0
        }),
        Some(origin.absolute_altitude_meters),
    );
    let orbit_prepared = database
        .prepare_expanded_incident_response_with_dataset(
            &orbit_incident.incident.id,
            &orbit_input,
            None,
        )
        .expect("prepare single-level Bounded Orbit SITL response");
    let orbit_waypoints = orbit_prepared.plan.generated_waypoints.len();
    assert_eq!(orbit_waypoints, 25);
    let orbit_ready =
        upload_sitl_response(&database, &command_router, &drone_id, &orbit_prepared).await;
    video_sequence = pull_next_sitl_video_frame(
        &video,
        &perception,
        &drone_id,
        video_sequence,
        "before single-level Orbit",
    )
    .await;
    start_sitl_response(&database, &command_router, &orbit_ready.id).await;
    wait_for_sitl_airborne(&database, &drone_id, &orbit_ready.id).await;
    video_sequence = pull_next_sitl_video_frame(
        &video,
        &perception,
        &drone_id,
        video_sequence,
        "during single-level Orbit",
    )
    .await;
    let orbit_completed =
        wait_for_mission_run_state(&database, &orbit_ready.id, "COMPLETED", 1_200).await;
    return_sitl_run_to_launch(&database, &command_router, &drone_id, &orbit_ready.id).await;
    assert_sitl_action_chain(
        &orbit_completed,
        &[
            "HOLD_AT_ARRIVAL",
            "POINT_GIMBAL_AT_INCIDENT",
            "RESUME_AFTER_ARRIVAL",
        ],
    );
    assert_sitl_completed_route(&orbit_completed, orbit_waypoints);

    assert!(
        video_sequence > 0,
        "SITL video never produced a decoded frame"
    );
    video
        .stop(Some(&drone_id))
        .expect("stop Native SITL video decoder");
    server.abort();
    drop(database);
    remove_sqlite_files(&path);
    let _ = fs::remove_dir_all(&state_directory);
}
