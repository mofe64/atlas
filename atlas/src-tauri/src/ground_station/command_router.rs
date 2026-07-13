use std::{collections::HashMap, sync::Arc};

use tokio::sync::{mpsc, RwLock};
use tonic::Status;

use crate::database::{LocalDatabase, VehicleCommandSnapshot};

use super::{
    proto::pb::{
        ground_station_to_agent, GroundStationToAgent, MissionOperationRequest,
        MissionOperationType, VehicleCommandCancellation, VehicleCommandRequest,
        VehicleCommandType,
    },
    unix_time_ms,
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
