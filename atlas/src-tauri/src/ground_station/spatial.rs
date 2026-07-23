#![allow(clippy::result_large_err)]

use std::{
    collections::HashMap,
    pin::Pin,
    sync::{Arc, RwLock},
};

use serde::Serialize;
use tokio::sync::mpsc;
use tokio_stream::{wrappers::ReceiverStream, Stream};
use tonic::{Request, Response, Status, Streaming};

use crate::database::LocalDatabase;

use super::{
    proto::pb::{
        agent_spatial, ground_station_spatial, AgentSpatial, GroundStationSpatial,
        SpatialCloudSnapshot, SpatialCloudSubscription, SpatialCloudSubscriptionAction,
        SpatialPose, SpatialStreamAccepted, SpatialStreamRegistration,
    },
    unix_time_ms,
};

const SPATIAL_PROTOCOL_VERSION: &str = "1";
const MAXIMUM_POINTS: usize = 100_000;
const BYTES_PER_POINT: usize = 12;
const STALE_AFTER_MS: i64 = 3_000;
const MINIMUM_LEASE_MS: i64 = 3_000;
const MAXIMUM_LEASE_MS: i64 = 30_000;
const FRAME_PACKET_MAGIC: &[u8; 4] = b"ATS1";

pub(super) type SpatialResponseStream =
    Pin<Box<dyn Stream<Item = Result<GroundStationSpatial, Status>> + Send + 'static>>;

#[derive(Clone, Default)]
pub(crate) struct SpatialStore {
    streams: Arc<RwLock<HashMap<String, StreamState>>>,
}

#[derive(Clone)]
struct StreamState {
    session_id: String,
    stream_id: String,
    drone_id: String,
    source_id: String,
    maximum_points: u32,
    status: String,
    connected_at_unix_ms: i64,
    last_received_at_unix_ms: i64,
    latest: Option<CloudFrame>,
    outbound: mpsc::Sender<Result<GroundStationSpatial, Status>>,
}

#[derive(Clone)]
struct CloudFrame {
    metadata: SpatialCloudMetadata,
    xyz_f32_le: Arc<Vec<u8>>,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct SpatialCloudMetadata {
    source_id: String,
    stream_epoch: String,
    sequence: u64,
    observed_at_unix_ms: i64,
    received_at_unix_ms: i64,
    capture_ns: i64,
    frame_id: String,
    voxel_size_m: f32,
    point_count: u32,
    pose: Option<SpatialPoseSnapshot>,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct SpatialPoseSnapshot {
    capture_ns: i64,
    frame_id: String,
    child_frame_id: String,
    x: f64,
    y: f64,
    z: f64,
    qw: f64,
    qx: f64,
    qy: f64,
    qz: f64,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct SpatialSnapshot {
    status: String,
    session_id: String,
    stream_id: String,
    drone_id: String,
    source_id: String,
    maximum_points: u32,
    connected_at_unix_ms: i64,
    last_received_at_unix_ms: i64,
    latest_cloud: Option<SpatialCloudMetadata>,
}

impl SpatialStore {
    fn register(
        &self,
        session_id: &str,
        drone_id: &str,
        registration: &SpatialStreamRegistration,
        now: i64,
        outbound: mpsc::Sender<Result<GroundStationSpatial, Status>>,
    ) -> Result<(), Status> {
        let mut streams = self
            .streams
            .write()
            .map_err(|_| Status::internal("spatial state lock was poisoned"))?;
        streams.insert(
            drone_id.to_string(),
            StreamState {
                session_id: session_id.to_string(),
                stream_id: registration.stream_id.clone(),
                drone_id: drone_id.to_string(),
                source_id: registration.source_id.clone(),
                maximum_points: registration.maximum_points,
                status: "connected".to_string(),
                connected_at_unix_ms: now,
                last_received_at_unix_ms: now,
                latest: None,
                outbound,
            },
        );
        Ok(())
    }

    fn record_cloud(
        &self,
        drone_id: &str,
        stream_id: &str,
        cloud: SpatialCloudSnapshot,
        received_at: i64,
    ) -> Result<(), Status> {
        let mut streams = self
            .streams
            .write()
            .map_err(|_| Status::internal("spatial state lock was poisoned"))?;
        let state = streams
            .get_mut(drone_id)
            .filter(|state| state.stream_id == stream_id && state.status == "connected")
            .ok_or_else(|| Status::failed_precondition("spatial stream is not active"))?;
        validate_cloud(state, &cloud)?;
        let pose = cloud.pose.as_ref().map(pose_snapshot).transpose()?;
        let metadata = SpatialCloudMetadata {
            source_id: cloud.source_id,
            stream_epoch: cloud.stream_epoch,
            sequence: cloud.sequence,
            observed_at_unix_ms: cloud.observed_at_unix_ms,
            received_at_unix_ms: received_at,
            capture_ns: cloud.capture_ns,
            frame_id: cloud.frame_id,
            voxel_size_m: cloud.voxel_size_m,
            point_count: cloud.point_count,
            pose,
        };
        state.latest = Some(CloudFrame {
            metadata,
            xyz_f32_le: Arc::new(cloud.xyz_f32_le),
        });
        state.last_received_at_unix_ms = received_at;
        Ok(())
    }

    fn close(&self, drone_id: &str, stream_id: &str, now: i64) {
        if let Ok(mut streams) = self.streams.write() {
            if let Some(state) = streams
                .get_mut(drone_id)
                .filter(|state| state.stream_id == stream_id)
            {
                state.status = "disconnected".to_string();
                state.last_received_at_unix_ms = now;
            }
        }
    }

    pub(crate) fn snapshot(&self, drone_id: Option<&str>) -> Option<SpatialSnapshot> {
        let streams = self.streams.read().ok()?;
        let state = match drone_id {
            Some(id) => streams.get(id),
            None => streams
                .values()
                .max_by_key(|state| state.last_received_at_unix_ms),
        }?;
        let status = if state.status == "connected"
            && unix_time_ms() - state.last_received_at_unix_ms > STALE_AFTER_MS
        {
            "stale".to_string()
        } else {
            state.status.clone()
        };
        Some(SpatialSnapshot {
            status,
            session_id: state.session_id.clone(),
            stream_id: state.stream_id.clone(),
            drone_id: state.drone_id.clone(),
            source_id: state.source_id.clone(),
            maximum_points: state.maximum_points,
            connected_at_unix_ms: state.connected_at_unix_ms,
            last_received_at_unix_ms: state.last_received_at_unix_ms,
            latest_cloud: state.latest.as_ref().map(|frame| frame.metadata.clone()),
        })
    }

    pub(crate) fn frame_packet(
        &self,
        drone_id: &str,
        after_stream_epoch: Option<&str>,
        after_sequence: u64,
    ) -> Result<Vec<u8>, String> {
        let frame = {
            let streams = self
                .streams
                .read()
                .map_err(|_| "spatial state lock was poisoned".to_string())?;
            let Some(state) = streams
                .get(drone_id)
                .filter(|state| state.status == "connected")
            else {
                return Ok(Vec::new());
            };
            let Some(frame) = state.latest.as_ref().filter(|frame| {
                after_stream_epoch != Some(frame.metadata.stream_epoch.as_str())
                    || frame.metadata.sequence > after_sequence
            }) else {
                return Ok(Vec::new());
            };
            frame.clone()
        };
        let header = serde_json::to_vec(&frame.metadata)
            .map_err(|error| format!("encode spatial frame metadata: {error}"))?;
        let header_length = u32::try_from(header.len())
            .map_err(|_| "spatial frame metadata is too large".to_string())?;
        let mut packet = Vec::with_capacity(8 + header.len() + frame.xyz_f32_le.len());
        packet.extend_from_slice(FRAME_PACKET_MAGIC);
        packet.extend_from_slice(&header_length.to_le_bytes());
        packet.extend_from_slice(&header);
        packet.extend_from_slice(frame.xyz_f32_le.as_slice());
        Ok(packet)
    }

    pub(crate) async fn start_or_renew_subscription(
        &self,
        drone_id: &str,
        subscription_id: &str,
        lease_duration_ms: i64,
    ) -> Result<(), String> {
        if !(MINIMUM_LEASE_MS..=MAXIMUM_LEASE_MS).contains(&lease_duration_ms) {
            return Err("spatial cloud lease must be between 3000 and 30000 ms".into());
        }
        self.send_subscription(
            drone_id,
            subscription_id,
            SpatialCloudSubscriptionAction::StartOrRenew,
            lease_duration_ms,
        )
        .await
    }

    pub(crate) async fn stop_subscription(
        &self,
        drone_id: &str,
        subscription_id: &str,
    ) -> Result<(), String> {
        self.send_subscription(
            drone_id,
            subscription_id,
            SpatialCloudSubscriptionAction::Stop,
            0,
        )
        .await
    }

    async fn send_subscription(
        &self,
        drone_id: &str,
        subscription_id: &str,
        action: SpatialCloudSubscriptionAction,
        lease_duration_ms: i64,
    ) -> Result<(), String> {
        if subscription_id.trim().is_empty() {
            return Err("spatial subscription id is required".into());
        }
        let outbound = {
            let streams = self
                .streams
                .read()
                .map_err(|_| "spatial state lock was poisoned".to_string())?;
            streams
                .get(drone_id)
                .filter(|state| state.status == "connected")
                .ok_or_else(|| "aircraft spatial stream is not connected".to_string())?
                .outbound
                .clone()
        };
        outbound
            .send(Ok(GroundStationSpatial {
                payload: Some(ground_station_spatial::Payload::CloudSubscription(
                    SpatialCloudSubscription {
                        subscription_id: subscription_id.to_string(),
                        purpose: "indoor_view".to_string(),
                        action: action as i32,
                        lease_duration_ms,
                    },
                )),
            }))
            .await
            .map_err(|_| "aircraft spatial response stream is closed".to_string())
    }
}

fn validate_cloud(state: &StreamState, cloud: &SpatialCloudSnapshot) -> Result<(), Status> {
    if cloud.source_id != state.source_id
        || cloud.stream_epoch.trim().is_empty()
        || cloud.frame_id.trim().is_empty()
    {
        return Err(Status::invalid_argument(
            "spatial cloud source, epoch, or coordinate frame is invalid",
        ));
    }
    if cloud.sequence == 0 || cloud.capture_ns <= 0 || cloud.observed_at_unix_ms <= 0 {
        return Err(Status::invalid_argument(
            "spatial cloud timestamps and sequence must be positive",
        ));
    }
    if cloud.point_count == 0
        || cloud.point_count > state.maximum_points
        || cloud.point_count as usize > MAXIMUM_POINTS
        || cloud.xyz_f32_le.len() != cloud.point_count as usize * BYTES_PER_POINT
    {
        return Err(Status::invalid_argument(
            "spatial cloud must contain the complete bounded XYZ payload",
        ));
    }
    if !cloud.voxel_size_m.is_finite() || cloud.voxel_size_m <= 0.0 {
        return Err(Status::invalid_argument(
            "spatial voxel size must be finite and positive",
        ));
    }
    if cloud
        .xyz_f32_le
        .chunks_exact(4)
        .any(|bytes| !f32::from_le_bytes(bytes.try_into().expect("four-byte chunk")).is_finite())
    {
        return Err(Status::invalid_argument(
            "spatial cloud contains a non-finite coordinate",
        ));
    }
    if let Some(previous) = state.latest.as_ref().map(|frame| &frame.metadata) {
        if previous.stream_epoch == cloud.stream_epoch && cloud.sequence <= previous.sequence {
            return Err(Status::invalid_argument(
                "spatial cloud sequence did not advance within the stream epoch",
            ));
        }
    }
    Ok(())
}

fn pose_snapshot(pose: &SpatialPose) -> Result<SpatialPoseSnapshot, Status> {
    let values = [pose.x, pose.y, pose.z, pose.qw, pose.qx, pose.qy, pose.qz];
    if pose.capture_ns <= 0
        || pose.frame_id.trim().is_empty()
        || pose.child_frame_id.trim().is_empty()
        || values.iter().any(|value| !value.is_finite())
    {
        return Err(Status::invalid_argument("spatial pose is invalid"));
    }
    let norm =
        (pose.qw * pose.qw + pose.qx * pose.qx + pose.qy * pose.qy + pose.qz * pose.qz).sqrt();
    if !(0.9..=1.1).contains(&norm) {
        return Err(Status::invalid_argument(
            "spatial pose quaternion must be normalized",
        ));
    }
    Ok(SpatialPoseSnapshot {
        capture_ns: pose.capture_ns,
        frame_id: pose.frame_id.clone(),
        child_frame_id: pose.child_frame_id.clone(),
        x: pose.x,
        y: pose.y,
        z: pose.z,
        qw: pose.qw,
        qx: pose.qx,
        qy: pose.qy,
        qz: pose.qz,
    })
}

pub(super) async fn open(
    database: Arc<LocalDatabase>,
    store: SpatialStore,
    request: Request<Streaming<AgentSpatial>>,
) -> Result<Response<SpatialResponseStream>, Status> {
    let inbound = request.into_inner();
    let (outbound, responses) = mpsc::channel(2);
    tokio::spawn(async move {
        if let Err(error) = run_stream(database, store, inbound, outbound.clone()).await {
            let _ = outbound.send(Err(error)).await;
        }
    });
    Ok(Response::new(Box::pin(ReceiverStream::new(responses))))
}

async fn run_stream(
    database: Arc<LocalDatabase>,
    store: SpatialStore,
    mut inbound: Streaming<AgentSpatial>,
    outbound: mpsc::Sender<Result<GroundStationSpatial, Status>>,
) -> Result<(), Status> {
    let first = inbound
        .message()
        .await?
        .ok_or_else(|| Status::invalid_argument("spatial registration is required"))?;
    let registration = match first.payload {
        Some(agent_spatial::Payload::Registration(registration)) => registration,
        _ => {
            return Err(Status::failed_precondition(
                "spatial registration must be the first stream message",
            ))
        }
    };
    validate_registration(&first.session_id, &first.drone_id, &registration)?;
    if !database
        .active_session_matches(
            &first.session_id,
            &registration.installation_id,
            &first.drone_id,
        )
        .map_err(Status::internal)?
    {
        return Err(Status::permission_denied(
            "spatial stream does not match an active agent session",
        ));
    }
    let now = unix_time_ms();
    store.register(
        &first.session_id,
        &first.drone_id,
        &registration,
        now,
        outbound.clone(),
    )?;
    outbound
        .send(Ok(GroundStationSpatial {
            payload: Some(ground_station_spatial::Payload::StreamAccepted(
                SpatialStreamAccepted {
                    stream_id: registration.stream_id.clone(),
                    accepted_at_unix_ms: now,
                },
            )),
        }))
        .await
        .map_err(|_| Status::cancelled("spatial response stream closed"))?;

    let result = run_registered_stream(
        &store,
        &first.session_id,
        &first.drone_id,
        &registration.stream_id,
        &mut inbound,
    )
    .await;
    store.close(&first.drone_id, &registration.stream_id, unix_time_ms());
    result
}

async fn run_registered_stream(
    store: &SpatialStore,
    session_id: &str,
    drone_id: &str,
    stream_id: &str,
    inbound: &mut Streaming<AgentSpatial>,
) -> Result<(), Status> {
    while let Some(message) = inbound.message().await? {
        if message.session_id != session_id || message.drone_id != drone_id {
            return Err(Status::invalid_argument(
                "spatial session or drone id changed after registration",
            ));
        }
        match message.payload {
            Some(agent_spatial::Payload::Cloud(cloud)) => {
                store.record_cloud(drone_id, stream_id, cloud, unix_time_ms())?
            }
            Some(agent_spatial::Payload::Registration(_)) => {
                return Err(Status::failed_precondition(
                    "spatial stream is already registered",
                ))
            }
            None => {
                return Err(Status::invalid_argument(
                    "spatial stream message has no payload",
                ))
            }
        }
    }
    Ok(())
}

fn validate_registration(
    session_id: &str,
    drone_id: &str,
    registration: &SpatialStreamRegistration,
) -> Result<(), Status> {
    if session_id.trim().is_empty()
        || drone_id.trim().is_empty()
        || registration.stream_id.trim().is_empty()
        || registration.installation_id.trim().is_empty()
        || registration.source_id.trim().is_empty()
        || registration.protocol_version != SPATIAL_PROTOCOL_VERSION
        || registration.maximum_points == 0
        || registration.maximum_points as usize > MAXIMUM_POINTS
    {
        return Err(Status::invalid_argument(
            "spatial stream registration is invalid or unsupported",
        ));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn registration() -> SpatialStreamRegistration {
        SpatialStreamRegistration {
            stream_id: "stream-1".into(),
            installation_id: "installation-1".into(),
            protocol_version: "1".into(),
            source_id: "front-depth".into(),
            maximum_points: 100_000,
            observed_at_unix_ms: 1,
        }
    }

    fn cloud(sequence: u64, point_count: u32) -> SpatialCloudSnapshot {
        SpatialCloudSnapshot {
            source_id: "front-depth".into(),
            stream_epoch: "epoch-1".into(),
            sequence,
            observed_at_unix_ms: 2,
            capture_ns: 3,
            frame_id: "vio_local".into(),
            voxel_size_m: 0.05,
            point_count,
            xyz_f32_le: vec![0; point_count as usize * BYTES_PER_POINT],
            pose: None,
        }
    }

    #[test]
    fn store_replaces_the_complete_snapshot_without_history() {
        let store = SpatialStore::default();
        let (outbound, _responses) = mpsc::channel(2);
        store
            .register("session-1", "drone-1", &registration(), 1, outbound)
            .unwrap();
        store
            .record_cloud("drone-1", "stream-1", cloud(1, 3), unix_time_ms())
            .unwrap();
        store
            .record_cloud("drone-1", "stream-1", cloud(2, 5), unix_time_ms())
            .unwrap();
        let snapshot = store.snapshot(Some("drone-1")).unwrap();
        assert_eq!(snapshot.latest_cloud.unwrap().point_count, 5);
        let packet = store.frame_packet("drone-1", Some("epoch-1"), 1).unwrap();
        assert_eq!(&packet[..4], FRAME_PACKET_MAGIC);
        assert_eq!(
            packet.len() - 8 - u32::from_le_bytes(packet[4..8].try_into().unwrap()) as usize,
            60
        );
    }

    #[test]
    fn store_rejects_partial_and_out_of_order_clouds() {
        let store = SpatialStore::default();
        let (outbound, _responses) = mpsc::channel(2);
        store
            .register("session-1", "drone-1", &registration(), 1, outbound)
            .unwrap();
        let mut partial = cloud(1, 3);
        partial.xyz_f32_le.truncate(12);
        assert!(store
            .record_cloud("drone-1", "stream-1", partial, 2)
            .is_err());
        store
            .record_cloud("drone-1", "stream-1", cloud(2, 3), 2)
            .unwrap();
        assert!(store
            .record_cloud("drone-1", "stream-1", cloud(2, 3), 3)
            .is_err());
    }

    #[test]
    fn maximum_cloud_remains_one_complete_native_frame() {
        let store = SpatialStore::default();
        let (outbound, _responses) = mpsc::channel(2);
        store
            .register("session-1", "drone-1", &registration(), 1, outbound)
            .unwrap();
        store
            .record_cloud(
                "drone-1",
                "stream-1",
                cloud(1, MAXIMUM_POINTS as u32),
                unix_time_ms(),
            )
            .unwrap();
        let packet = store.frame_packet("drone-1", None, 0).unwrap();
        let header_length = u32::from_le_bytes(packet[4..8].try_into().unwrap()) as usize;
        assert_eq!(packet.len() - 8 - header_length, 1_200_000);
    }

    #[test]
    fn a_new_coordinate_epoch_is_delivered_even_when_its_sequence_restarts() {
        let store = SpatialStore::default();
        let (outbound, _responses) = mpsc::channel(2);
        store
            .register("session-1", "drone-1", &registration(), 1, outbound)
            .unwrap();
        store
            .record_cloud("drone-1", "stream-1", cloud(10, 3), unix_time_ms())
            .unwrap();
        assert!(store
            .frame_packet("drone-1", Some("epoch-1"), 10)
            .unwrap()
            .is_empty());
        let mut reset = cloud(1, 4);
        reset.stream_epoch = "epoch-2".into();
        store
            .record_cloud("drone-1", "stream-1", reset, unix_time_ms())
            .unwrap();
        assert!(!store
            .frame_packet("drone-1", Some("epoch-1"), 10)
            .unwrap()
            .is_empty());
    }

    #[tokio::test]
    async fn subscription_is_delivered_to_the_matching_stream() {
        let store = SpatialStore::default();
        let (outbound, mut responses) = mpsc::channel(2);
        store
            .register("session-1", "drone-1", &registration(), 1, outbound)
            .unwrap();
        store
            .start_or_renew_subscription("drone-1", "indoor-1", 12_000)
            .await
            .unwrap();
        let response = responses.recv().await.unwrap().unwrap();
        let subscription = response
            .payload
            .and_then(|payload| match payload {
                ground_station_spatial::Payload::CloudSubscription(value) => Some(value),
                _ => None,
            })
            .expect("cloud subscription payload");
        assert_eq!(subscription.purpose, "indoor_view");
        assert_eq!(
            subscription.action(),
            SpatialCloudSubscriptionAction::StartOrRenew
        );
    }
}
