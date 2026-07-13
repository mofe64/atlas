use serde_json::json;
use tonic::Status;

use crate::database::{LocalDatabase, RegisteredSession, RegistrationInput};

use super::{proto::pb, unix_time_ms};

#[allow(clippy::result_large_err)]
pub(super) fn register(
    database: &LocalDatabase,
    session_id: &str,
    remote_address: &str,
    registration: pb::AgentRegistration,
) -> Result<pb::RegistrationAccepted, Status> {
    if session_id.trim().is_empty()
        || registration.registration_request_id.trim().is_empty()
        || registration.installation_id.trim().is_empty()
    {
        return Err(Status::invalid_argument(
            "session, registration request, and installation ids are required",
        ));
    }
    let device = registration
        .device
        .ok_or_else(|| Status::invalid_argument("device profile is required"))?;
    let drone = registration
        .drone
        .ok_or_else(|| Status::invalid_argument("drone profile is required"))?;
    if drone.drone_id.trim().is_empty() || drone.name.trim().is_empty() {
        return Err(Status::invalid_argument("drone id and name are required"));
    }
    let flight_controller = registration
        .flight_controller
        .ok_or_else(|| Status::invalid_argument("flight-controller attachment is required"))?;
    let now = unix_time_ms();
    let registered: RegisteredSession = database
        .register_agent(&RegistrationInput {
            session_id: session_id.to_string(),
            installation_id: registration.installation_id,
            agent_version: registration.agent_version,
            protocol_version: registration.protocol_version,
            device_profile_json: json!({
                "deviceName": device.device_name,
                "hostname": device.hostname,
                "operatingSystem": device.operating_system,
                "architecture": device.architecture,
                "hardwareId": device.hardware_id,
                "hardwareIdSource": device.hardware_id_source,
                "totalMemoryBytes": device.total_memory_bytes,
            })
            .to_string(),
            capabilities_json: serde_json::to_string(&registration.capabilities)
                .map_err(|error| Status::internal(error.to_string()))?,
            drone_id: drone.drone_id,
            drone_name: drone.name,
            flight_controller_uid: drone.flight_controller_uid,
            serial_number: drone.serial_number,
            vehicle_type: drone.vehicle_type,
            flight_controller_transport: flight_controller.transport,
            endpoint_description: flight_controller.endpoint_description,
            baud_rate: flight_controller.baud_rate,
            mavlink_system_id: flight_controller.mavlink_system_id,
            mavlink_component_id: flight_controller.mavlink_component_id,
            remote_address: remote_address.to_string(),
            observed_at_unix_ms: now,
        })
        .map_err(Status::internal)?;

    Ok(pb::RegistrationAccepted {
        agent_id: registered.agent_id,
        drone_id: registered.drone_id,
        binding_id: registered.binding_id,
        communication_link_id: registered.communication_link_id,
        registered_at_unix_ms: now,
    })
}
