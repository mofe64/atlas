use std::{pin::Pin, sync::Arc};

use tokio::sync::mpsc;
use tokio_stream::{wrappers::ReceiverStream, Stream};
use tonic::{Request, Response, Status, Streaming};

use crate::database::LocalDatabase;

use super::{
    command_router::CommandRouter,
    indoor::IndoorExploreStore,
    proto::pb::{
        agent_to_ground_station, ground_station_to_agent, AgentToGroundStation,
        AircraftFollowSessionUpdateType, GroundStationToAgent, MissionActionState,
        MissionRunUpdateType, VehicleCommandUpdateType,
    },
    registration, status_text, telemetry, unix_time_ms,
};

pub(super) type SessionResponseStream =
    Pin<Box<dyn Stream<Item = Result<GroundStationToAgent, Status>> + Send + 'static>>;

#[derive(Default)]
struct SessionState {
    session_id: Option<String>,
    drone_id: Option<String>,
    communication_link_id: Option<String>,
    pending_delivery: bool,
}

impl SessionState {
    fn message_id_matches(&self, received: &str) -> bool {
        self.session_id
            .as_deref()
            .is_none_or(|expected| expected == received)
    }

    fn registration_allowed(&self) -> bool {
        self.session_id.is_none()
    }

    fn activate(&mut self, session_id: String, drone_id: String, communication_link_id: String) {
        self.session_id = Some(session_id);
        self.drone_id = Some(drone_id);
        self.communication_link_id = Some(communication_link_id);
        self.pending_delivery = true;
    }

    fn active_id(&self) -> Option<&str> {
        self.session_id.as_deref()
    }
}

pub(super) async fn open(
    database: Arc<LocalDatabase>,
    command_router: CommandRouter,
    indoor: IndoorExploreStore,
    request: Request<Streaming<AgentToGroundStation>>,
) -> Result<Response<SessionResponseStream>, Status> {
    let remote_address = request
        .remote_addr()
        .map(|address| address.to_string())
        .unwrap_or_default();
    let mut inbound = request.into_inner();
    let (outbound, responses) = mpsc::channel(8);

    tokio::spawn(async move {
        let mut state = SessionState::default();
        loop {
            let message = match inbound.message().await {
                Ok(Some(message)) => message,
                Ok(None) => break,
                Err(error) => {
                    eprintln!("Atlas agent session receive failed: {error}");
                    break;
                }
            };

            if !state.message_id_matches(&message.session_id) {
                let _ = outbound
                    .send(Err(Status::invalid_argument(
                        "session id changed after registration",
                    )))
                    .await;
                break;
            }

            let result = match message.payload {
                Some(agent_to_ground_station::Payload::Registration(registration_message)) => {
                    if !state.registration_allowed() {
                        Err(Status::failed_precondition(
                            "registration may only be sent once per session",
                        ))
                    } else {
                        match registration::register(
                            &database,
                            &message.session_id,
                            &remote_address,
                            registration_message,
                        ) {
                            Ok(accepted) => {
                                command_router
                                    .register(
                                        accepted.drone_id.clone(),
                                        message.session_id.clone(),
                                        accepted.communication_link_id.clone(),
                                        outbound.clone(),
                                    )
                                    .await;
                                state.activate(
                                    message.session_id,
                                    accepted.drone_id.clone(),
                                    accepted.communication_link_id.clone(),
                                );
                                Ok(Some(GroundStationToAgent {
                                    payload: Some(
                                        ground_station_to_agent::Payload::RegistrationAccepted(
                                            accepted,
                                        ),
                                    ),
                                }))
                            }
                            Err(error) => Err(error),
                        }
                    }
                }
                Some(agent_to_ground_station::Payload::Heartbeat(_)) => match state.active_id() {
                    Some(session_id) => database
                        .record_heartbeat(session_id, unix_time_ms())
                        .map(|()| None)
                        .map_err(Status::internal),
                    None => Err(Status::failed_precondition(
                        "registration must be the first session message",
                    )),
                },
                Some(agent_to_ground_station::Payload::Telemetry(message)) => {
                    match state.active_id() {
                        Some(session_id) => match telemetry::input(message, unix_time_ms()) {
                            Ok(input) => database
                                .record_telemetry(session_id, &input)
                                .map(|()| None)
                                .map_err(Status::internal),
                            Err(error) => Err(error),
                        },
                        None => Err(Status::failed_precondition(
                            "registration must be the first session message",
                        )),
                    }
                }
                Some(agent_to_ground_station::Payload::StatusText(message)) => {
                    match state.active_id() {
                        Some(session_id) => match status_text::input(message, unix_time_ms()) {
                            Ok(input) => database
                                .record_status_event(session_id, &input)
                                .map(|()| None)
                                .map_err(Status::internal),
                            Err(error) => Err(error),
                        },
                        None => Err(Status::failed_precondition(
                            "registration must be the first session message",
                        )),
                    }
                }
                Some(agent_to_ground_station::Payload::CommandUpdate(message)) => {
                    match (state.active_id(), state.communication_link_id.as_deref()) {
                        (Some(_), Some(link_id)) => {
                            let (event_type, next_state) =
                                command_update_state(message.update_type());
                            database
                                .apply_vehicle_command_update(
                                    &crate::database::VehicleCommandUpdateInput {
                                        event_id: message.event_id,
                                        command_id: message.command_id,
                                        event_type: event_type.to_string(),
                                        next_state: next_state.map(str::to_string),
                                        communication_link_id: link_id.to_string(),
                                        occurred_at_unix_ms: message.observed_at_unix_ms,
                                        progress_percent: message.progress_percent,
                                        result_code: message.result_code,
                                        message: message.message,
                                        evidence_json: (!message.evidence_json.is_empty())
                                            .then_some(message.evidence_json),
                                    },
                                )
                                .map(|_| None)
                                .map_err(Status::failed_precondition)
                        }
                        _ => Err(Status::failed_precondition(
                            "registration must be the first session message",
                        )),
                    }
                }
                Some(agent_to_ground_station::Payload::MissionRunUpdate(message)) => {
                    match (state.active_id(), state.drone_id.as_deref()) {
                        (Some(_), Some(drone_id)) => {
                            match database.mission_run(&message.mission_run_id) {
                                Err(error) => Err(Status::failed_precondition(error)),
                                Ok(run) if run.drone_id != drone_id => {
                                    Err(Status::permission_denied(
                                        "mission update does not target this agent drone",
                                    ))
                                }
                                Ok(_)
                                    if message.update_type()
                                        == MissionRunUpdateType::ActionStateChanged =>
                                {
                                    match (
                                        message.action_sequence,
                                        mission_action_state(message.action_state()),
                                    ) {
                                        (Some(action_sequence), Some(action_state)) => database
                                            .apply_mission_action_update(
                                                &crate::database::MissionActionUpdateInput {
                                                    event_id: message.event_id,
                                                    mission_run_id: message.mission_run_id,
                                                    action_sequence,
                                                    action_type: message.action_type,
                                                    state: action_state.to_string(),
                                                    attempt: message.action_attempt,
                                                    failure_policy: message.failure_policy,
                                                    occurred_at_unix_ms: message
                                                        .observed_at_unix_ms,
                                                    error_code: message.error_code,
                                                    message: message.message,
                                                    evidence_json: (!message
                                                        .evidence_json
                                                        .is_empty())
                                                    .then_some(message.evidence_json),
                                                },
                                            )
                                            .map(|_| None)
                                            .map_err(Status::failed_precondition),
                                        (None, _) => Err(Status::invalid_argument(
                                            "mission action update is missing action_sequence",
                                        )),
                                        (_, None) => Err(Status::invalid_argument(
                                            "mission action update has an unspecified state",
                                        )),
                                    }
                                }
                                Ok(_) => {
                                    let event_type =
                                        mission_update_event(message.update_type()).to_string();
                                    database
                                        .apply_mission_run_update(
                                            &crate::database::MissionRunUpdateInput {
                                                event_id: message.event_id,
                                                operation_id: message.operation_id,
                                                mission_run_id: message.mission_run_id,
                                                event_type,
                                                run_state: message.run_state,
                                                occurred_at_unix_ms: message.observed_at_unix_ms,
                                                progress_percent: message.progress_percent,
                                                current_waypoint: message.current_waypoint,
                                                total_waypoints: message.total_waypoints,
                                                error_code: message.error_code,
                                                message: message.message,
                                                evidence_json: (!message.evidence_json.is_empty())
                                                    .then_some(message.evidence_json),
                                            },
                                        )
                                        .map(|_| None)
                                        .map_err(Status::failed_precondition)
                                }
                            }
                        }
                        _ => Err(Status::failed_precondition(
                            "registration must be the first session message",
                        )),
                    }
                }
                Some(agent_to_ground_station::Payload::AircraftFollowSessionUpdate(message)) => {
                    match (state.active_id(), state.drone_id.as_deref()) {
                        (Some(_), Some(drone_id)) => {
                            match database.aircraft_follow_session(&message.follow_session_id) {
                                Err(error) => Err(Status::failed_precondition(error)),
                                Ok(session) if session.drone_id != drone_id => {
                                    Err(Status::permission_denied(
                                        "aircraft follow update does not target this agent drone",
                                    ))
                                }
                                Ok(_) => match aircraft_follow_state(message.update_type()) {
                                    Some(next_state) => database
                                        .apply_aircraft_follow_agent_update(
                                            &crate::database::AircraftFollowAgentUpdateInput {
                                                event_id: message.event_id,
                                                operation_id: message.operation_id,
                                                session_id: message.follow_session_id,
                                                state: next_state.to_string(),
                                                observed_at_unix_ms: message.observed_at_unix_ms,
                                                reason_code: message.reason_code,
                                                message: message.message,
                                                evidence_json: message.evidence_json,
                                            },
                                        )
                                        .map(|_| None)
                                        .map_err(Status::failed_precondition),
                                    None => Err(Status::invalid_argument(
                                        "aircraft follow update has an unspecified state",
                                    )),
                                },
                            }
                        }
                        _ => Err(Status::failed_precondition(
                            "registration must be the first session message",
                        )),
                    }
                }
                Some(agent_to_ground_station::Payload::IndoorExploreMissionUpdate(message)) => {
                    match (state.active_id(), state.drone_id.as_deref()) {
                        (Some(_), Some(drone_id)) => indoor
                            .apply_agent_update(drone_id, message)
                            .map(|()| None)
                            .map_err(Status::failed_precondition),
                        _ => Err(Status::failed_precondition(
                            "registration must be the first session message",
                        )),
                    }
                }
                None => Err(Status::invalid_argument("session message has no payload")),
            };

            match result {
                Ok(Some(response)) => {
                    if outbound.send(Ok(response)).await.is_err() {
                        break;
                    }
                    if state.pending_delivery {
                        state.pending_delivery = false;
                        if let Some(drone_id) = state.drone_id.as_deref() {
                            if let Err(error) = command_router
                                .deliver_mission_reconciliation(&database, drone_id)
                                .await
                            {
                                eprintln!("Deliver Atlas mission reconciliation failed: {error}");
                            }
                            if let Err(error) =
                                command_router.deliver_pending(&database, drone_id).await
                            {
                                eprintln!("Deliver pending Atlas commands failed: {error}");
                            }
                        }
                    }
                }
                Ok(None) => {}
                Err(error) => {
                    let _ = outbound.send(Err(error)).await;
                    break;
                }
            }
        }

        if let Some(session_id) = state.session_id {
            if let Some(drone_id) = state.drone_id.as_deref() {
                command_router.unregister(drone_id, &session_id).await;
                indoor.agent_session_lost(drone_id, unix_time_ms());
                if let Err(error) = database.degrade_aircraft_follow_for_drone(
                    drone_id,
                    "AGENT_SESSION_LOST",
                    "Atlas Agent session ended; onboard follow control must enter Hold",
                    unix_time_ms(),
                ) {
                    eprintln!("Degrade aircraft follow after Agent session loss failed: {error}");
                }
            }
            if let Err(error) =
                database.close_session(&session_id, "agent stream ended", unix_time_ms())
            {
                eprintln!("Close Atlas agent session failed: {error}");
            }
        }
    });

    Ok(Response::new(Box::pin(ReceiverStream::new(responses))))
}

fn aircraft_follow_state(update: AircraftFollowSessionUpdateType) -> Option<&'static str> {
    match update {
        AircraftFollowSessionUpdateType::Validating => Some("VALIDATING"),
        AircraftFollowSessionUpdateType::Acquiring => Some("ACQUIRING"),
        AircraftFollowSessionUpdateType::Following => Some("FOLLOWING"),
        AircraftFollowSessionUpdateType::DegradedHold
        | AircraftFollowSessionUpdateType::Rejected => Some("DEGRADED_HOLD"),
        AircraftFollowSessionUpdateType::Ended => Some("ENDED"),
        AircraftFollowSessionUpdateType::Unspecified => None,
    }
}

fn mission_update_event(update: MissionRunUpdateType) -> &'static str {
    match update {
        MissionRunUpdateType::OperationAccepted => "operation_accepted",
        MissionRunUpdateType::UploadProgress => "upload_progress",
        MissionRunUpdateType::Uploaded => "uploaded",
        MissionRunUpdateType::Started => "started",
        MissionRunUpdateType::Progress => "progress",
        MissionRunUpdateType::Paused => "paused",
        MissionRunUpdateType::Resumed => "resumed",
        MissionRunUpdateType::Completed => "completed",
        MissionRunUpdateType::Cancelled => "cancelled",
        MissionRunUpdateType::RtlStarted => "rtl_started",
        MissionRunUpdateType::OperationFailed => "operation_failed",
        MissionRunUpdateType::Arming => "arming",
        MissionRunUpdateType::Armed => "armed",
        MissionRunUpdateType::PayloadManualStarted => "payload_manual_started",
        MissionRunUpdateType::PayloadMissionRestored => "payload_mission_restored",
        MissionRunUpdateType::PayloadRestoreFailed => "payload_restore_failed",
        MissionRunUpdateType::ActionStateChanged => "action_state_changed",
        MissionRunUpdateType::ReconciliationAccepted => "reconciliation_accepted",
        MissionRunUpdateType::ReconciliationFailed => "reconciliation_failed",
        MissionRunUpdateType::Unspecified => "unspecified",
    }
}

fn mission_action_state(state: MissionActionState) -> Option<&'static str> {
    match state {
        MissionActionState::Requested => Some("REQUESTED"),
        MissionActionState::Running => Some("RUNNING"),
        MissionActionState::Retrying => Some("RETRYING"),
        MissionActionState::Succeeded => Some("SUCCEEDED"),
        MissionActionState::Failed => Some("FAILED"),
        MissionActionState::PolicyApplied => Some("POLICY_APPLIED"),
        MissionActionState::Unspecified => None,
    }
}

fn command_update_state(update: VehicleCommandUpdateType) -> (&'static str, Option<&'static str>) {
    match update {
        VehicleCommandUpdateType::Accepted => ("accepted", Some("accepted")),
        VehicleCommandUpdateType::Rejected => ("rejected", Some("rejected")),
        VehicleCommandUpdateType::Executing => ("executing", Some("executing")),
        VehicleCommandUpdateType::Progress => ("progress", None),
        VehicleCommandUpdateType::Succeeded => ("succeeded", Some("succeeded")),
        VehicleCommandUpdateType::Failed => ("failed", Some("failed")),
        VehicleCommandUpdateType::TimedOut => ("timed_out", Some("timed_out")),
        VehicleCommandUpdateType::Cancelled => ("cancelled", Some("cancelled")),
        VehicleCommandUpdateType::CancellationRejected => ("cancellation_rejected", None),
        VehicleCommandUpdateType::Unspecified => ("unspecified", None),
    }
}
