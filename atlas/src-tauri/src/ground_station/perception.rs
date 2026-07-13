// Tonic's stream contract uses `Status` directly. Keeping that type through
// validation helpers avoids boxing and immediately unboxing every stream error.
#![allow(clippy::result_large_err)]

use std::{
    collections::{HashMap, VecDeque},
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
        agent_perception, ground_station_perception, AgentPerception, GroundStationPerception,
        NormalizedBoundingBox, PerceptionDetection, PerceptionFrame, PerceptionHealth,
        PerceptionModelIdentity, PerceptionStreamAccepted,
    },
    unix_time_ms,
};

const PERCEPTION_PROTOCOL_VERSION: &str = "1";
const MAX_DETECTIONS_PER_FRAME: usize = 1_000;
const MAX_ATTRIBUTES_BYTES: usize = 64 * 1024;
const STALE_AFTER_MS: i64 = 3_000;
const PERCEPTION_HISTORY_MAX_FRAMES: usize = 240;
const PERCEPTION_HISTORY_MAX_AGE_MS: i64 = 10_000;

pub(super) type PerceptionResponseStream =
    Pin<Box<dyn Stream<Item = Result<GroundStationPerception, Status>> + Send + 'static>>;

#[derive(Clone, Default)]
pub(crate) struct PerceptionStore {
    streams: Arc<RwLock<HashMap<String, StreamState>>>,
}

#[derive(Clone)]
struct StreamState {
    session_id: String,
    stream_id: String,
    drone_id: String,
    provider: String,
    status: String,
    connected_at_unix_ms: i64,
    last_received_at_unix_ms: i64,
    sources: HashMap<String, PerceptionSourceSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct PerceptionSnapshot {
    pub status: String,
    pub session_id: String,
    pub stream_id: String,
    pub drone_id: String,
    pub provider: String,
    pub connected_at_unix_ms: i64,
    pub last_received_at_unix_ms: i64,
    pub sources: Vec<PerceptionSourceSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct PerceptionSourceSnapshot {
    source_id: String,
    latest_frame: Option<PerceptionFrameSnapshot>,
    health: Option<PerceptionHealthSnapshot>,
    #[serde(skip)]
    recent_frames: VecDeque<PerceptionFrameSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct PerceptionFrameSnapshot {
    stream_epoch: String,
    frame_id: String,
    observed_at_unix_ms: i64,
    received_at_unix_ms: i64,
    source_pts_ns: i64,
    image_width: u32,
    image_height: u32,
    model: ModelSnapshot,
    inference_latency_ms: f64,
    detections: Vec<DetectionSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct AlignedPerceptionFrameSnapshot {
    alignment_delta_ms: i64,
    frame: PerceptionFrameSnapshot,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct DetectionSnapshot {
    track_id: String,
    class_id: i32,
    class_label: String,
    confidence: f64,
    bounding_box: BoundingBoxSnapshot,
    attributes: Option<serde_json::Value>,
}

#[derive(Debug, Clone, Serialize)]
struct BoundingBoxSnapshot {
    x: f64,
    y: f64,
    width: f64,
    height: f64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct ModelSnapshot {
    name: String,
    version: String,
    artifact_hash: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct PerceptionHealthSnapshot {
    provider: String,
    accelerator: String,
    input_connected: bool,
    inference_ready: bool,
    output_publishing: bool,
    input_fps: f64,
    inference_fps: f64,
    dropped_frames: u64,
    last_frame_at_unix_ms: i64,
    last_detection_at_unix_ms: i64,
    last_error: String,
    model: Option<ModelSnapshot>,
    observed_at_unix_ms: i64,
    received_at_unix_ms: i64,
}

impl PerceptionStore {
    fn register(
        &self,
        session_id: &str,
        drone_id: &str,
        stream_id: &str,
        provider: &str,
        now: i64,
    ) -> Result<(), Status> {
        let mut streams = self
            .streams
            .write()
            .map_err(|_| Status::internal("perception state lock was poisoned"))?;
        streams.insert(
            drone_id.to_string(),
            StreamState {
                session_id: session_id.to_string(),
                stream_id: stream_id.to_string(),
                drone_id: drone_id.to_string(),
                provider: provider.to_string(),
                status: "connected".to_string(),
                connected_at_unix_ms: now,
                last_received_at_unix_ms: now,
                sources: HashMap::new(),
            },
        );
        Ok(())
    }

    fn record_frame(
        &self,
        drone_id: &str,
        stream_id: &str,
        frame: PerceptionFrame,
        received_at: i64,
    ) -> Result<(), Status> {
        let snapshot = frame_snapshot(frame, received_at)?;
        let source_id = snapshot.0;
        let mut streams = self
            .streams
            .write()
            .map_err(|_| Status::internal("perception state lock was poisoned"))?;
        let state = active_stream(&mut streams, drone_id, stream_id)?;
        let source =
            state
                .sources
                .entry(source_id.clone())
                .or_insert_with(|| PerceptionSourceSnapshot {
                    source_id,
                    latest_frame: None,
                    health: None,
                    recent_frames: VecDeque::new(),
                });
        source.latest_frame = Some(snapshot.1.clone());
        source.recent_frames.push_back(snapshot.1);
        while source.recent_frames.len() > PERCEPTION_HISTORY_MAX_FRAMES
            || source.recent_frames.front().is_some_and(|frame| {
                received_at - frame.received_at_unix_ms > PERCEPTION_HISTORY_MAX_AGE_MS
            })
        {
            source.recent_frames.pop_front();
        }
        state.last_received_at_unix_ms = received_at;
        state.status = "connected".to_string();
        Ok(())
    }

    fn record_health(
        &self,
        drone_id: &str,
        stream_id: &str,
        health: PerceptionHealth,
        received_at: i64,
    ) -> Result<(), Status> {
        let provider = health.provider.trim().to_string();
        let snapshot = health_snapshot(health, received_at)?;
        let source_id = snapshot.0;
        let mut streams = self
            .streams
            .write()
            .map_err(|_| Status::internal("perception state lock was poisoned"))?;
        let state = active_stream(&mut streams, drone_id, stream_id)?;
        if provider != state.provider {
            return Err(Status::invalid_argument(
                "perception health provider does not match stream registration",
            ));
        }
        let source =
            state
                .sources
                .entry(source_id.clone())
                .or_insert_with(|| PerceptionSourceSnapshot {
                    source_id,
                    latest_frame: None,
                    health: None,
                    recent_frames: VecDeque::new(),
                });
        source.health = Some(snapshot.1);
        state.last_received_at_unix_ms = received_at;
        state.status = "connected".to_string();
        Ok(())
    }

    fn close(&self, drone_id: &str, stream_id: &str, now: i64) {
        if let Ok(mut streams) = self.streams.write() {
            if let Some(state) = streams.get_mut(drone_id) {
                if state.stream_id == stream_id {
                    state.status = "disconnected".to_string();
                    state.last_received_at_unix_ms = now;
                }
            }
        }
    }

    pub(crate) fn snapshot(&self, drone_id: Option<&str>) -> Option<PerceptionSnapshot> {
        let streams = self.streams.read().ok()?;
        let state = match drone_id {
            Some(drone_id) => streams.get(drone_id),
            None => streams
                .values()
                .max_by_key(|state| state.last_received_at_unix_ms),
        }?;
        let mut sources = state.sources.values().cloned().collect::<Vec<_>>();
        sources.sort_by(|left, right| left.source_id.cmp(&right.source_id));
        let status = if state.status == "connected"
            && unix_time_ms() - state.last_received_at_unix_ms > STALE_AFTER_MS
        {
            "stale".to_string()
        } else {
            state.status.clone()
        };
        Some(PerceptionSnapshot {
            status,
            session_id: state.session_id.clone(),
            stream_id: state.stream_id.clone(),
            drone_id: state.drone_id.clone(),
            provider: state.provider.clone(),
            connected_at_unix_ms: state.connected_at_unix_ms,
            last_received_at_unix_ms: state.last_received_at_unix_ms,
            sources,
        })
    }

    /// Matches a delayed native video frame to recent perception using only
    /// ground-station clock values. Subtracting measured inference latency
    /// avoids requiring synchronized clocks between the aircraft and operator.
    pub(crate) fn aligned_frame(
        &self,
        drone_id: &str,
        source_id: &str,
        video_received_at_unix_ms: i64,
        overlay_offset_ms: i64,
        tolerance_ms: i64,
    ) -> Option<AlignedPerceptionFrameSnapshot> {
        let streams = self.streams.read().ok()?;
        let state = streams.get(drone_id)?;
        let source = state.sources.get(source_id)?;
        source
            .recent_frames
            .iter()
            .filter_map(|frame| {
                let inferred_capture_at = frame.received_at_unix_ms
                    - frame.inference_latency_ms.round() as i64
                    + overlay_offset_ms;
                let delta = inferred_capture_at - video_received_at_unix_ms;
                (delta.abs() <= tolerance_ms).then(|| AlignedPerceptionFrameSnapshot {
                    alignment_delta_ms: delta,
                    frame: frame.clone(),
                })
            })
            .min_by_key(|candidate| candidate.alignment_delta_ms.abs())
    }
}

pub(super) async fn open(
    database: Arc<LocalDatabase>,
    store: PerceptionStore,
    request: Request<Streaming<AgentPerception>>,
) -> Result<Response<PerceptionResponseStream>, Status> {
    let inbound = request.into_inner();
    let (outbound, responses) = mpsc::channel(2);
    tokio::spawn(async move {
        if let Err(error) = run_stream(database, store, inbound, outbound.clone()).await {
            let _ = outbound.send(Err(error)).await;
        }
    });

    // Return the response stream before waiting for registration. Bidirectional
    // gRPC clients cannot send their first payload until the RPC is open.
    Ok(Response::new(Box::pin(ReceiverStream::new(responses))))
}

async fn run_stream(
    database: Arc<LocalDatabase>,
    store: PerceptionStore,
    mut inbound: Streaming<AgentPerception>,
    outbound: mpsc::Sender<Result<GroundStationPerception, Status>>,
) -> Result<(), Status> {
    let first = inbound
        .message()
        .await?
        .ok_or_else(|| Status::invalid_argument("perception registration is required"))?;
    let registration = match first.payload {
        Some(agent_perception::Payload::Registration(registration)) => registration,
        _ => {
            return Err(Status::failed_precondition(
                "perception registration must be the first stream message",
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
            "perception stream does not match an active agent session",
        ));
    }

    let now = unix_time_ms();
    store.register(
        &first.session_id,
        &first.drone_id,
        &registration.stream_id,
        &registration.provider,
        now,
    )?;
    let result = run_registered_stream(
        &store,
        &first.session_id,
        &first.drone_id,
        &registration.stream_id,
        &mut inbound,
        &outbound,
        GroundStationPerception {
            payload: Some(ground_station_perception::Payload::StreamAccepted(
                PerceptionStreamAccepted {
                    stream_id: registration.stream_id.clone(),
                    accepted_at_unix_ms: now,
                },
            )),
        },
    )
    .await;
    store.close(&first.drone_id, &registration.stream_id, unix_time_ms());
    result
}

async fn run_registered_stream(
    store: &PerceptionStore,
    session_id: &str,
    drone_id: &str,
    stream_id: &str,
    inbound: &mut Streaming<AgentPerception>,
    outbound: &mpsc::Sender<Result<GroundStationPerception, Status>>,
    accepted: GroundStationPerception,
) -> Result<(), Status> {
    outbound
        .send(Ok(accepted))
        .await
        .map_err(|_| Status::cancelled("perception response stream closed"))?;

    loop {
        let message = match inbound.message().await? {
            Some(message) => message,
            None => return Ok(()),
        };
        if message.session_id != session_id || message.drone_id != drone_id {
            return Err(Status::invalid_argument(
                "perception session or drone id changed after registration",
            ));
        }
        match message.payload {
            Some(agent_perception::Payload::Frame(frame)) => {
                store.record_frame(drone_id, stream_id, frame, unix_time_ms())?
            }
            Some(agent_perception::Payload::Health(health)) => {
                store.record_health(drone_id, stream_id, health, unix_time_ms())?
            }
            Some(agent_perception::Payload::Registration(_)) => {
                return Err(Status::failed_precondition(
                    "perception stream is already registered",
                ));
            }
            None => {
                return Err(Status::invalid_argument(
                    "perception stream message has no payload",
                ));
            }
        }
    }
}

fn validate_registration(
    session_id: &str,
    drone_id: &str,
    registration: &super::proto::pb::PerceptionStreamRegistration,
) -> Result<(), Status> {
    if session_id.trim().is_empty()
        || drone_id.trim().is_empty()
        || registration.stream_id.trim().is_empty()
        || registration.installation_id.trim().is_empty()
        || registration.provider.trim().is_empty()
    {
        return Err(Status::invalid_argument(
            "session, drone, stream, installation, and provider are required",
        ));
    }
    if registration.protocol_version != PERCEPTION_PROTOCOL_VERSION {
        return Err(Status::failed_precondition(
            "unsupported perception protocol version",
        ));
    }
    Ok(())
}

fn active_stream<'a>(
    streams: &'a mut HashMap<String, StreamState>,
    drone_id: &str,
    stream_id: &str,
) -> Result<&'a mut StreamState, Status> {
    let state = streams
        .get_mut(drone_id)
        .ok_or_else(|| Status::failed_precondition("perception stream is not registered"))?;
    if state.stream_id != stream_id || state.status == "disconnected" {
        return Err(Status::failed_precondition(
            "perception stream is no longer active",
        ));
    }
    Ok(state)
}

fn frame_snapshot(
    frame: PerceptionFrame,
    received_at: i64,
) -> Result<(String, PerceptionFrameSnapshot), Status> {
    if frame.source_id.trim().is_empty()
        || frame.stream_epoch.trim().is_empty()
        || frame.frame_id.trim().is_empty()
        || frame.observed_at_unix_ms <= 0
        || frame.image_width == 0
        || frame.image_height == 0
    {
        return Err(Status::invalid_argument(
            "perception frame identity, time, and dimensions are required",
        ));
    }
    if frame.detections.len() > MAX_DETECTIONS_PER_FRAME
        || !finite_non_negative(frame.inference_latency_ms)
    {
        return Err(Status::invalid_argument(
            "perception frame exceeds detection or latency limits",
        ));
    }
    let model = model_snapshot(
        frame
            .model
            .ok_or_else(|| Status::invalid_argument("perception frame model is required"))?,
    )?;
    let detections = frame
        .detections
        .into_iter()
        .map(detection_snapshot)
        .collect::<Result<Vec<_>, _>>()?;
    let source_id = frame.source_id;
    Ok((
        source_id,
        PerceptionFrameSnapshot {
            stream_epoch: frame.stream_epoch,
            frame_id: frame.frame_id,
            observed_at_unix_ms: frame.observed_at_unix_ms,
            received_at_unix_ms: received_at,
            source_pts_ns: frame.source_pts_ns,
            image_width: frame.image_width,
            image_height: frame.image_height,
            model,
            inference_latency_ms: frame.inference_latency_ms,
            detections,
        },
    ))
}

fn detection_snapshot(detection: PerceptionDetection) -> Result<DetectionSnapshot, Status> {
    if detection.class_label.trim().is_empty() || !finite_unit(detection.confidence) {
        return Err(Status::invalid_argument(
            "detection class label and unit confidence are required",
        ));
    }
    let bounding_box = bounding_box_snapshot(
        detection
            .bounding_box
            .ok_or_else(|| Status::invalid_argument("detection bounding box is required"))?,
    )?;
    if detection.attributes_json.len() > MAX_ATTRIBUTES_BYTES {
        return Err(Status::invalid_argument(
            "detection attributes exceed the maximum size",
        ));
    }
    let attributes = if detection.attributes_json.trim().is_empty() {
        None
    } else {
        Some(
            serde_json::from_str(&detection.attributes_json)
                .map_err(|_| Status::invalid_argument("detection attributes must be valid JSON"))?,
        )
    };
    Ok(DetectionSnapshot {
        track_id: detection.track_id,
        class_id: detection.class_id,
        class_label: detection.class_label,
        confidence: detection.confidence,
        bounding_box,
        attributes,
    })
}

fn bounding_box_snapshot(box_value: NormalizedBoundingBox) -> Result<BoundingBoxSnapshot, Status> {
    if !finite_unit(box_value.x)
        || !finite_unit(box_value.y)
        || !finite_unit(box_value.width)
        || !finite_unit(box_value.height)
        || box_value.x + box_value.width > 1.0 + 1e-9
        || box_value.y + box_value.height > 1.0 + 1e-9
    {
        return Err(Status::invalid_argument(
            "detection bounding box must remain inside the normalized frame",
        ));
    }
    Ok(BoundingBoxSnapshot {
        x: box_value.x,
        y: box_value.y,
        width: box_value.width,
        height: box_value.height,
    })
}

fn health_snapshot(
    health: PerceptionHealth,
    received_at: i64,
) -> Result<(String, PerceptionHealthSnapshot), Status> {
    if health.source_id.trim().is_empty()
        || health.provider.trim().is_empty()
        || health.observed_at_unix_ms <= 0
        || !finite_non_negative(health.input_fps)
        || !finite_non_negative(health.inference_fps)
    {
        return Err(Status::invalid_argument(
            "perception health identity, time, and rates are invalid",
        ));
    }
    let model = health.model.map(model_snapshot).transpose()?;
    let source_id = health.source_id;
    Ok((
        source_id,
        PerceptionHealthSnapshot {
            provider: health.provider,
            accelerator: health.accelerator,
            input_connected: health.input_connected,
            inference_ready: health.inference_ready,
            output_publishing: health.output_publishing,
            input_fps: health.input_fps,
            inference_fps: health.inference_fps,
            dropped_frames: health.dropped_frames,
            last_frame_at_unix_ms: health.last_frame_at_unix_ms,
            last_detection_at_unix_ms: health.last_detection_at_unix_ms,
            last_error: health.last_error,
            model,
            observed_at_unix_ms: health.observed_at_unix_ms,
            received_at_unix_ms: received_at,
        },
    ))
}

fn model_snapshot(model: PerceptionModelIdentity) -> Result<ModelSnapshot, Status> {
    if model.name.trim().is_empty() || model.version.trim().is_empty() {
        return Err(Status::invalid_argument(
            "perception model name and version are required",
        ));
    }
    Ok(ModelSnapshot {
        name: model.name,
        version: model.version,
        artifact_hash: model.artifact_hash,
    })
}

fn finite_unit(value: f64) -> bool {
    value.is_finite() && (0.0..=1.0).contains(&value)
}

fn finite_non_negative(value: f64) -> bool {
    value.is_finite() && value >= 0.0
}

#[cfg(test)]
mod alignment_tests {
    use super::*;

    #[test]
    fn alignment_uses_native_receive_time_and_inference_latency() {
        let store = PerceptionStore::default();
        store
            .register("session-1", "drone-1", "stream-1", "hailo", 1_000)
            .expect("register perception");
        store
            .record_frame(
                "drone-1",
                "stream-1",
                PerceptionFrame {
                    source_id: "a8-main".into(),
                    stream_epoch: "epoch-1".into(),
                    frame_id: "frame-1".into(),
                    observed_at_unix_ms: 900,
                    source_pts_ns: 42_000,
                    image_width: 1_920,
                    image_height: 1_080,
                    model: Some(PerceptionModelIdentity {
                        name: "atlas-objects".into(),
                        version: "1".into(),
                        artifact_hash: String::new(),
                    }),
                    inference_latency_ms: 50.0,
                    detections: Vec::new(),
                },
                1_200,
            )
            .expect("record perception frame");

        let aligned = store
            .aligned_frame("drone-1", "a8-main", 1_155, 0, 10)
            .expect("aligned frame");
        assert_eq!(aligned.alignment_delta_ms, -5);
        assert_eq!(aligned.frame.frame_id, "frame-1");
        assert!(store
            .aligned_frame("drone-1", "a8-main", 1_300, 0, 10)
            .is_none());
    }
}
