use std::{collections::HashMap, sync::Arc};

use tokio::sync::{mpsc, RwLock};
use tonic::Status;

use crate::database::{AircraftFollowSessionSnapshot, LocalDatabase, VehicleCommandSnapshot};

use super::{
    proto::pb::{
        ground_station_to_agent, AircraftFollowControlAction, AircraftFollowControlRequest,
        AircraftFollowEnvelope, AircraftFollowTargetState, GroundStationToAgent,
        IndoorExploreControlRequest, MissionActionCheckpoint, MissionOperationRequest,
        MissionOperationType, MissionReconciliationRequest, VehicleCommandCancellation,
        VehicleCommandRequest, VehicleCommandType,
    },
    unix_time_ms, IndoorExploreControl,
};

type ResponseSender = mpsc::Sender<Result<GroundStationToAgent, Status>>;

#[derive(Clone)]
struct ActiveSession {
    session_id: String,
    communication_link_id: String,
    outbound: ResponseSender,
}

#[derive(Clone, Default)]
pub(crate) struct CommandRouter {
    sessions: Arc<RwLock<HashMap<String, ActiveSession>>>,
}

impl CommandRouter {
    pub(crate) async fn register(
        &self,
        drone_id: String,
        session_id: String,
        communication_link_id: String,
        outbound: ResponseSender,
    ) {
        self.sessions.write().await.insert(
            drone_id,
            ActiveSession {
                session_id,
                communication_link_id,
                outbound,
            },
        );
    }

    pub(crate) async fn unregister(&self, drone_id: &str, session_id: &str) {
        let mut sessions = self.sessions.write().await;
        if sessions
            .get(drone_id)
            .is_some_and(|session| session.session_id == session_id)
        {
            sessions.remove(drone_id);
        }
    }

    pub(crate) async fn deliver(
        &self,
        database: &LocalDatabase,
        command: &VehicleCommandSnapshot,
    ) -> Result<VehicleCommandSnapshot, String> {
        let session = self
            .sessions
            .read()
            .await
            .get(&command.drone_id)
            .cloned()
            .ok_or_else(|| "drone has no active Atlas Agent session".to_string())?;
        let delivered_at = unix_time_ms();
        let delivered = database.mark_vehicle_command_sent(
            &command.id,
            &session.communication_link_id,
            delivered_at,
        )?;
        session
            .outbound
            .send(Ok(GroundStationToAgent {
                payload: Some(ground_station_to_agent::Payload::CommandRequest(
                    VehicleCommandRequest {
                        command_id: command.id.clone(),
                        drone_id: command.drone_id.clone(),
                        command_type: command_type(&command.command_type) as i32,
                        parameters_json: command.parameters_json.clone(),
                        created_at_unix_ms: command.created_at_unix_ms,
                        delivered_at_unix_ms: delivered_at,
                        deadline_at_unix_ms: command.deadline_at_unix_ms,
                    },
                )),
            }))
            .await
            .map_err(|_| "Atlas Agent session closed before command delivery".to_string())?;
        Ok(delivered)
    }

    pub(crate) async fn deliver_pending(
        &self,
        database: &LocalDatabase,
        drone_id: &str,
    ) -> Result<(), String> {
        for command in database.deliverable_vehicle_commands(drone_id, unix_time_ms())? {
            self.deliver(database, &command).await?;
        }
        Ok(())
    }

    pub(crate) async fn deliver_mission_reconciliation(
        &self,
        database: &LocalDatabase,
        drone_id: &str,
    ) -> Result<bool, String> {
        let Some(snapshot) = database.mission_reconciliation(drone_id)? else {
            return Ok(false);
        };
        let session = self
            .sessions
            .read()
            .await
            .get(drone_id)
            .cloned()
            .ok_or_else(|| "drone has no active Atlas Agent session".to_string())?;
        let requested_at = unix_time_ms();
        let reconciliation_id = format!("reconcile-{drone_id}-{requested_at}");
        let actions = snapshot
            .run
            .actions
            .iter()
            .map(|action| MissionActionCheckpoint {
                action_sequence: action.action_sequence,
                action_type: action.action_type.clone(),
                state: action.state.clone(),
                attempt: action.attempt,
                attempt_deadline_at_unix_ms: action.attempt_deadline_at_unix_ms,
                next_attempt_at_unix_ms: action.next_attempt_at_unix_ms,
            })
            .collect();
        session
            .outbound
            .send(Ok(GroundStationToAgent {
                payload: Some(
                    ground_station_to_agent::Payload::MissionReconciliationRequest(
                        MissionReconciliationRequest {
                            reconciliation_id,
                            mission_run_id: snapshot.run.id,
                            drone_id: snapshot.run.drone_id,
                            run_state: snapshot.run.status,
                            mission_plan_json: snapshot.mission_plan_json,
                            current_waypoint: snapshot.run.current_waypoint,
                            total_waypoints: snapshot.run.total_waypoints,
                            actions,
                            requested_at_unix_ms: requested_at,
                            deadline_at_unix_ms: requested_at + 30_000,
                        },
                    ),
                ),
            }))
            .await
            .map_err(|_| {
                "Atlas Agent session closed before mission reconciliation delivery".to_string()
            })?;
        Ok(true)
    }

    pub(crate) async fn deliver_aircraft_follow_control(
        &self,
        follow: &AircraftFollowSessionSnapshot,
        operation_id: &str,
        action: &str,
        reason_code: &str,
        reason: &str,
    ) -> Result<(), String> {
        let session = self
            .sessions
            .read()
            .await
            .get(&follow.drone_id)
            .cloned()
            .ok_or_else(|| "drone has no active Atlas Agent session".to_string())?;
        let target = &follow.target;
        session
            .outbound
            .send(Ok(GroundStationToAgent {
                payload: Some(
                    ground_station_to_agent::Payload::AircraftFollowControlRequest(
                        AircraftFollowControlRequest {
                            operation_id: operation_id.to_string(),
                            follow_session_id: follow.id.clone(),
                            drone_id: follow.drone_id.clone(),
                            action: aircraft_follow_action(action) as i32,
                            envelope: Some(AircraftFollowEnvelope {
                                standoff_m: follow.standoff_m,
                                altitude_relative_m: follow.altitude_relative_m,
                                minimum_altitude_relative_m: follow.minimum_altitude_relative_m,
                                maximum_altitude_relative_m: follow.maximum_altitude_relative_m,
                                maximum_ground_speed_m_s: follow.maximum_ground_speed_mps,
                                maximum_acceleration_m_s2: follow.maximum_acceleration_mps2,
                                maximum_duration_ms: follow.maximum_duration_ms,
                                boundary_center_latitude: follow.boundary_center_latitude,
                                boundary_center_longitude: follow.boundary_center_longitude,
                                boundary_radius_m: follow.boundary_radius_m,
                                minimum_battery_percent: follow.minimum_battery_percent,
                                minimum_track_confidence: follow.minimum_track_confidence,
                                maximum_geolocation_uncertainty_m: follow
                                    .maximum_geolocation_uncertainty_m,
                                maximum_velocity_uncertainty_m_s: follow
                                    .maximum_velocity_uncertainty_mps,
                            }),
                            target: Some(AircraftFollowTargetState {
                                geolocation_id: target.geolocation_id.clone(),
                                selection_id: target.selection_id.clone(),
                                source_id: target.source_id.clone(),
                                track_session_id: target.track_session_id.clone(),
                                track_id: target.track_id.clone(),
                                observed_at_unix_ms: target.observed_at_unix_ms,
                                latitude: target.latitude,
                                longitude: target.longitude,
                                altitude_amsl_m: target.altitude_amsl_m,
                                velocity_north_m_s: target.velocity_north_mps,
                                velocity_east_m_s: target.velocity_east_mps,
                                horizontal_uncertainty_m: target.horizontal_uncertainty_m,
                                velocity_uncertainty_m_s: target.velocity_uncertainty_mps,
                                track_confidence: target.track_confidence,
                                lifecycle_state: target.lifecycle_state.clone(),
                                motion_status: target.motion_status.clone(),
                            }),
                            operator_lease_expires_at_unix_ms: follow
                                .operator_lease_expires_at_unix_ms
                                .unwrap_or_default(),
                            requested_at_unix_ms: unix_time_ms(),
                            reason_code: reason_code.to_string(),
                            reason: reason.to_string(),
                            validation_reference: follow.validation_reference.clone(),
                        },
                    ),
                ),
            }))
            .await
            .map_err(|_| {
                "Atlas Agent session closed before aircraft follow control delivery".to_string()
            })
    }

    pub(crate) async fn deliver_indoor_explore_control(
        &self,
        control: &IndoorExploreControl,
    ) -> Result<(), String> {
        let session = self
            .sessions
            .read()
            .await
            .get(&control.drone_id)
            .cloned()
            .ok_or_else(|| "drone has no active Atlas Agent session".to_string())?;
        session
            .outbound
            .send(Ok(GroundStationToAgent {
                payload: Some(
                    ground_station_to_agent::Payload::IndoorExploreControlRequest(
                        IndoorExploreControlRequest {
                            operation_id: control.operation_id.clone(),
                            mission_id: control.mission_id.clone(),
                            drone_id: control.drone_id.clone(),
                            action: control.action as i32,
                            altitude_m: control.altitude_m,
                            requested_at_unix_ms: control.requested_at_unix_ms,
                            deadline_at_unix_ms: control.deadline_at_unix_ms,
                            reason: control.reason.clone(),
                        },
                    ),
                ),
            }))
            .await
            .map_err(|_| {
                "Atlas Agent session closed before Indoor Explore control delivery".to_string()
            })
    }

    pub(crate) async fn cancel(
        &self,
        drone_id: &str,
        command_id: &str,
        requested_at: i64,
    ) -> Result<(), String> {
        let session = self
            .sessions
            .read()
            .await
            .get(drone_id)
            .cloned()
            .ok_or_else(|| "drone has no active Atlas Agent session".to_string())?;
        session
            .outbound
            .send(Ok(GroundStationToAgent {
                payload: Some(ground_station_to_agent::Payload::CommandCancellation(
                    VehicleCommandCancellation {
                        command_id: command_id.to_string(),
                        requested_at_unix_ms: requested_at,
                    },
                )),
            }))
            .await
            .map_err(|_| "Atlas Agent session closed before cancellation delivery".to_string())
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) async fn deliver_mission_operation(
        &self,
        drone_id: &str,
        operation_id: &str,
        run_id: &str,
        operation: &str,
        mission_plan_json: Option<&str>,
        requested_at: i64,
        deadline_at: i64,
    ) -> Result<(), String> {
        let session = self
            .sessions
            .read()
            .await
            .get(drone_id)
            .cloned()
            .ok_or_else(|| "drone has no active Atlas Agent session".to_string())?;
        session
            .outbound
            .send(Ok(GroundStationToAgent {
                payload: Some(ground_station_to_agent::Payload::MissionOperationRequest(
                    MissionOperationRequest {
                        operation_id: operation_id.to_string(),
                        mission_run_id: run_id.to_string(),
                        drone_id: drone_id.to_string(),
                        operation_type: mission_operation_type(operation) as i32,
                        mission_plan_json: mission_plan_json.unwrap_or_default().to_string(),
                        requested_at_unix_ms: requested_at,
                        deadline_at_unix_ms: deadline_at,
                    },
                )),
            }))
            .await
            .map_err(|_| "Atlas Agent session closed before mission operation delivery".to_string())
    }
}

fn command_type(value: &str) -> VehicleCommandType {
    match value {
        "hold" => VehicleCommandType::Hold,
        "return_to_launch" => VehicleCommandType::ReturnToLaunch,
        "land" => VehicleCommandType::Land,
        "gimbal_set_angles" => VehicleCommandType::GimbalSetAngles,
        "gimbal_set_rates" => VehicleCommandType::GimbalSetRates,
        "gimbal_center" => VehicleCommandType::GimbalCenter,
        "payload_control_begin" => VehicleCommandType::PayloadControlBegin,
        "payload_control_renew" => VehicleCommandType::PayloadControlRenew,
        "payload_control_end" => VehicleCommandType::PayloadControlEnd,
        "gimbal_set_roi" => VehicleCommandType::GimbalSetRoi,
        "camera_set_zoom" => VehicleCommandType::CameraSetZoom,
        "gimbal_follow_start" => VehicleCommandType::GimbalFollowStart,
        "gimbal_follow_stop" => VehicleCommandType::GimbalFollowStop,
        "geolocate_selected_track" => VehicleCommandType::GeolocateSelectedTrack,
        _ => VehicleCommandType::Unspecified,
    }
}

fn mission_operation_type(value: &str) -> MissionOperationType {
    match value {
        "upload" => MissionOperationType::Upload,
        "start" => MissionOperationType::Start,
        "pause" => MissionOperationType::Pause,
        "resume" => MissionOperationType::Resume,
        "cancel" => MissionOperationType::Cancel,
        "return_to_launch" => MissionOperationType::ReturnToLaunch,
        _ => MissionOperationType::Unspecified,
    }
}

fn aircraft_follow_action(value: &str) -> AircraftFollowControlAction {
    match value {
        "start" => AircraftFollowControlAction::Start,
        "renew" => AircraftFollowControlAction::Renew,
        "hold" => AircraftFollowControlAction::Hold,
        "end" => AircraftFollowControlAction::End,
        _ => AircraftFollowControlAction::Unspecified,
    }
}
