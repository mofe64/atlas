// Tonic's stream contract uses `Status` directly. Keeping that type through
// validation helpers avoids boxing and immediately unboxing every stream error.
#![allow(clippy::result_large_err)]

use std::{
    collections::{BTreeMap, HashMap, VecDeque},
    pin::Pin,
    sync::{Arc, RwLock},
};

use serde::Serialize;
use tokio::sync::mpsc;
use tokio_stream::{wrappers::ReceiverStream, Stream};
use tonic::{Request, Response, Status, Streaming};

use crate::database::{
    CountingRuleSnapshot, LocalDatabase, PerceptionTrackBatchInput, PerceptionTrackUpdateInput,
    TrackBoundingBoxInput, TrackCountEventInput, TrackRuleCountInput,
};

use super::{
    proto::pb::{
        agent_perception, ground_station_perception, AgentPerception, GroundStationPerception,
        NormalizedBoundingBox, NormalizedPoint, PerceptionCountingRule, PerceptionCountingRuleSet,
        PerceptionDetection, PerceptionFrame, PerceptionFrameSubscription,
        PerceptionFrameSubscriptionAction, PerceptionHealth, PerceptionModelIdentity,
        PerceptionStreamAccepted, PerceptionTrackCountEvent as ProtoTrackCountEvent,
        PerceptionTrackRuleCount as ProtoTrackRuleCount,
        PerceptionTrackSnapshot as ProtoPerceptionTrackSnapshot, PerceptionTrackUpdateBatch,
        PerceptionTrackingHealth,
    },
    unix_time_ms,
};

const PERCEPTION_PROTOCOL_VERSION: &str = "1";
const MAX_DETECTIONS_PER_FRAME: usize = 1_000;
const MAX_ATTRIBUTES_BYTES: usize = 64 * 1024;
const STALE_AFTER_MS: i64 = 3_000;
const PERCEPTION_HISTORY_MAX_FRAMES: usize = 240;
const PERCEPTION_HISTORY_MAX_AGE_MS: i64 = 10_000;
const MAX_RECENT_CLOSED_TRACKS: usize = 100;
const MINIMUM_FRAME_SUBSCRIPTION_LEASE_MS: i64 = 3_000;
const MAXIMUM_FRAME_SUBSCRIPTION_LEASE_MS: i64 = 30_000;

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
    outbound: mpsc::Sender<Result<GroundStationPerception, Status>>,
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

impl PerceptionSnapshot {
    pub(crate) fn operational_availability(&self) -> (bool, serde_json::Value) {
        let health = self
            .sources
            .iter()
            .filter_map(|source| source.health.as_ref())
            .collect::<Vec<_>>();
        let available = self.status == "connected"
            && !health.is_empty()
            && health.iter().any(|source| {
                source.output_publishing
                    && source.activation_state != "FAILED"
                    && source.last_error.trim().is_empty()
            });
        let sources = self
            .sources
            .iter()
            .map(|source| {
                serde_json::json!({
                    "sourceId": source.source_id,
                    "health": source.health.as_ref().map(|health| serde_json::json!({
                        "inputConnected": health.input_connected,
                        "inferenceReady": health.inference_ready,
                        "activationState": health.activation_state,
                        "outputPublishing": health.output_publishing,
                        "lastError": health.last_error,
                        "receivedAtUnixMs": health.received_at_unix_ms,
                    })),
                })
            })
            .collect::<Vec<_>>();
        (
            available,
            serde_json::json!({
                "status": self.status,
                "provider": self.provider,
                "lastReceivedAtUnixMs": self.last_received_at_unix_ms,
                "sources": sources,
            }),
        )
    }
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct PerceptionSourceSnapshot {
    source_id: String,
    latest_frame: Option<PerceptionFrameSnapshot>,
    health: Option<PerceptionHealthSnapshot>,
    track_session: Option<TrackSessionSnapshot>,
    tracks: Vec<TrackLifecycleSnapshot>,
    #[serde(skip)]
    recent_frames: VecDeque<PerceptionFrameSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct TrackSessionSnapshot {
    track_session_id: String,
    tracker_type: String,
    stream_epoch: String,
    started_at_unix_ms: i64,
    last_update_at_unix_ms: i64,
    ended_at_unix_ms: Option<i64>,
    end_reason: String,
    current_visible_count: u64,
    unique_confirmed_count: u64,
    rule_counts: Vec<TrackRuleCountSnapshot>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct TrackRuleCountSnapshot {
    rule_id: String,
    rule_revision: u64,
    rule_type: String,
    line_forward: u64,
    line_reverse: u64,
    polygon_entries: u64,
    polygon_exits: u64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct TrackLifecycleSnapshot {
    track_id: String,
    track_session_id: String,
    tracker_type: String,
    lifecycle_state: String,
    revision: u64,
    age_frames: u64,
    observation_count: u64,
    first_observed_at_unix_ms: i64,
    last_observed_at_unix_ms: i64,
    latest_confirmed_box: BoundingBoxSnapshot,
    latest_detection_confidence: f64,
    predicted_box: Option<BoundingBoxSnapshot>,
    prediction_confidence: f64,
    closed_at_unix_ms: Option<i64>,
    closure_reason: String,
    class_id: i32,
    class_label: String,
    update_reason: String,
}

struct ValidatedTrackBatch {
    source_id: String,
    session: TrackSessionSnapshot,
    session_started: bool,
    session_ended: bool,
    tracks: Vec<TrackLifecycleSnapshot>,
    database: PerceptionTrackBatchInput,
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
    activation_state: String,
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
    tracking: Option<TrackingHealthSnapshot>,
    observed_at_unix_ms: i64,
    received_at_unix_ms: i64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct TrackingHealthSnapshot {
    algorithm: String,
    state: String,
    session_id: String,
    last_reset_reason: String,
    reset_count: u64,
    last_error: String,
    camera_motion_state: String,
    camera_motion_method: String,
    camera_motion_confidence: f64,
    re_id_enabled: bool,
}

impl PerceptionStore {
    fn register(
        &self,
        session_id: &str,
        drone_id: &str,
        stream_id: &str,
        provider: &str,
        now: i64,
        outbound: mpsc::Sender<Result<GroundStationPerception, Status>>,
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
                outbound,
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
                    track_session: None,
                    tracks: Vec::new(),
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
                    track_session: None,
                    tracks: Vec::new(),
                    recent_frames: VecDeque::new(),
                });
        source.health = Some(snapshot.1);
        state.last_received_at_unix_ms = received_at;
        state.status = "connected".to_string();
        Ok(())
    }

    fn record_track_batch(
        &self,
        drone_id: &str,
        stream_id: &str,
        batch: ValidatedTrackBatch,
        received_at: i64,
    ) -> Result<(), Status> {
        let mut streams = self
            .streams
            .write()
            .map_err(|_| Status::internal("perception state lock was poisoned"))?;
        let state = active_stream(&mut streams, drone_id, stream_id)?;
        let source = state
            .sources
            .entry(batch.source_id.clone())
            .or_insert_with(|| PerceptionSourceSnapshot {
                source_id: batch.source_id.clone(),
                latest_frame: None,
                health: None,
                track_session: None,
                tracks: Vec::new(),
                recent_frames: VecDeque::new(),
            });
        if batch.session_started
            || source
                .track_session
                .as_ref()
                .is_none_or(|current| current.track_session_id != batch.session.track_session_id)
        {
            source.track_session = Some(batch.session.clone());
        } else if let Some(current) = source.track_session.as_mut() {
            current.last_update_at_unix_ms = current
                .last_update_at_unix_ms
                .max(batch.session.last_update_at_unix_ms);
            current.current_visible_count = batch.session.current_visible_count;
            current.unique_confirmed_count = current
                .unique_confirmed_count
                .max(batch.session.unique_confirmed_count);
            current.rule_counts = batch.session.rule_counts.clone();
            if batch.session_ended {
                current.ended_at_unix_ms = batch.session.ended_at_unix_ms;
                current.end_reason = batch.session.end_reason.clone();
            }
        }
        for track in batch.tracks {
            match source
                .tracks
                .iter_mut()
                .find(|existing| existing.track_id == track.track_id)
            {
                Some(existing) if existing.revision >= track.revision => {}
                Some(existing) => *existing = track,
                None => source.tracks.push(track),
            }
        }
        while source
            .tracks
            .iter()
            .filter(|track| track.lifecycle_state == "CLOSED")
            .count()
            > MAX_RECENT_CLOSED_TRACKS
        {
            if let Some(index) = source
                .tracks
                .iter()
                .position(|track| track.lifecycle_state == "CLOSED")
            {
                source.tracks.remove(index);
            }
        }
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

    pub(crate) async fn start_or_renew_frame_subscription(
        &self,
        drone_id: &str,
        subscription_id: &str,
        purpose: &str,
        lease_duration_ms: i64,
    ) -> Result<(), String> {
        if !(MINIMUM_FRAME_SUBSCRIPTION_LEASE_MS..=MAXIMUM_FRAME_SUBSCRIPTION_LEASE_MS)
            .contains(&lease_duration_ms)
        {
            return Err("perception frame lease must be between 3000 and 30000 ms".into());
        }
        self.send_frame_subscription(
            drone_id,
            subscription_id,
            purpose,
            PerceptionFrameSubscriptionAction::StartOrRenew,
            lease_duration_ms,
        )
        .await
    }

    pub(crate) async fn stop_frame_subscription(
        &self,
        drone_id: &str,
        subscription_id: &str,
        purpose: &str,
    ) -> Result<(), String> {
        self.send_frame_subscription(
            drone_id,
            subscription_id,
            purpose,
            PerceptionFrameSubscriptionAction::Stop,
            0,
        )
        .await
    }

    async fn send_frame_subscription(
        &self,
        drone_id: &str,
        subscription_id: &str,
        purpose: &str,
        action: PerceptionFrameSubscriptionAction,
        lease_duration_ms: i64,
    ) -> Result<(), String> {
        if subscription_id.trim().is_empty() {
            return Err("perception subscription id is required".into());
        }
        if purpose != "live_view" {
            return Err("unsupported perception subscription purpose".into());
        }
        let outbound = {
            let streams = self
                .streams
                .read()
                .map_err(|_| "perception state lock was poisoned".to_string())?;
            let stream = streams
                .get(drone_id)
                .filter(|stream| stream.status == "connected")
                .ok_or_else(|| "aircraft perception stream is not connected".to_string())?;
            stream.outbound.clone()
        };
        outbound
            .send(Ok(GroundStationPerception {
                payload: Some(ground_station_perception::Payload::FrameSubscription(
                    PerceptionFrameSubscription {
                        subscription_id: subscription_id.to_string(),
                        purpose: purpose.to_string(),
                        action: action as i32,
                        lease_duration_ms,
                    },
                )),
            }))
            .await
            .map_err(|_| "aircraft perception response stream is closed".to_string())
    }

    pub(crate) async fn send_counting_rules(
        &self,
        drone_id: &str,
        source_id: &str,
        rules: &[CountingRuleSnapshot],
    ) -> Result<(), String> {
        let outbound = {
            let streams = self
                .streams
                .read()
                .map_err(|_| "perception state lock was poisoned".to_string())?;
            let stream = streams
                .get(drone_id)
                .filter(|stream| stream.status == "connected")
                .ok_or_else(|| "aircraft perception stream is not connected".to_string())?;
            stream.outbound.clone()
        };
        outbound
            .send(Ok(counting_rule_message(source_id, rules)))
            .await
            .map_err(|_| "aircraft perception response stream is closed".to_string())
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
        outbound.clone(),
    )?;
    let accepted = GroundStationPerception {
        payload: Some(ground_station_perception::Payload::StreamAccepted(
            PerceptionStreamAccepted {
                stream_id: registration.stream_id.clone(),
                accepted_at_unix_ms: now,
            },
        )),
    };
    let result = match outbound.send(Ok(accepted)).await {
        Ok(()) => {
            let all_rules = database
                .perception_counting_rules(&first.drone_id, None, false)
                .map_err(Status::internal)?;
            let mut rules_by_source = BTreeMap::<String, Vec<CountingRuleSnapshot>>::new();
            for rule in all_rules {
                let source_id = rule.source_id.clone();
                let rules = rules_by_source.entry(source_id).or_default();
                if rule.enabled {
                    rules.push(rule);
                }
            }
            for (source_id, rules) in rules_by_source {
                outbound
                    .send(Ok(counting_rule_message(&source_id, &rules)))
                    .await
                    .map_err(|_| Status::cancelled("perception response stream closed"))?;
            }
            run_registered_stream(
                &database,
                &store,
                &first.session_id,
                &first.drone_id,
                &registration.stream_id,
                &mut inbound,
            )
            .await
        }
        Err(_) => Err(Status::cancelled("perception response stream closed")),
    };
    store.close(&first.drone_id, &registration.stream_id, unix_time_ms());
    result
}

fn counting_rule_message(
    source_id: &str,
    rules: &[CountingRuleSnapshot],
) -> GroundStationPerception {
    GroundStationPerception {
        payload: Some(ground_station_perception::Payload::CountingRules(
            PerceptionCountingRuleSet {
                source_id: source_id.to_string(),
                rules: rules
                    .iter()
                    .map(|rule| PerceptionCountingRule {
                        rule_id: rule.id.clone(),
                        label: rule.label.clone(),
                        rule_type: rule.rule_type.clone(),
                        revision: rule.revision,
                        points: rule
                            .points
                            .iter()
                            .map(|point| NormalizedPoint {
                                x: point.x,
                                y: point.y,
                            })
                            .collect(),
                        class_ids: rule.class_ids.clone(),
                    })
                    .collect(),
            },
        )),
    }
}

async fn run_registered_stream(
    database: &LocalDatabase,
    store: &PerceptionStore,
    session_id: &str,
    drone_id: &str,
    stream_id: &str,
    inbound: &mut Streaming<AgentPerception>,
) -> Result<(), Status> {
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
            Some(agent_perception::Payload::TrackUpdates(batch)) => {
                let received_at = unix_time_ms();
                let validated = track_batch_snapshot(batch)?;
                database
                    .record_perception_track_batch(
                        session_id,
                        drone_id,
                        stream_id,
                        &validated.database,
                        received_at,
                    )
                    .map_err(Status::internal)?;
                store.record_track_batch(drone_id, stream_id, validated, received_at)?;
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

fn track_batch_snapshot(batch: PerceptionTrackUpdateBatch) -> Result<ValidatedTrackBatch, Status> {
    if batch.source_id.trim().is_empty()
        || batch.stream_epoch.trim().is_empty()
        || batch.track_session_id.trim().is_empty()
        || !matches!(batch.tracker_type.as_str(), "BYTE_TRACK" | "BYTE_TRACK_CMC")
        || batch.observed_at_unix_ms <= 0
        || batch.tracks.len() > MAX_DETECTIONS_PER_FRAME
        || batch.rule_counts.len() > 64
        || batch.count_events.len() > MAX_DETECTIONS_PER_FRAME
        || batch.current_visible > batch.unique_confirmed
        || batch.session_ended != !batch.session_end_reason.trim().is_empty()
    {
        return Err(Status::invalid_argument(
            "perception track batch identity, time, tracker, or ending is invalid",
        ));
    }
    let tracks = batch
        .tracks
        .into_iter()
        .map(|track| {
            track_lifecycle_snapshot(
                track,
                &batch.track_session_id,
                &batch.tracker_type,
                batch.observed_at_unix_ms,
            )
        })
        .collect::<Result<Vec<_>, _>>()?;
    let rule_counts = batch
        .rule_counts
        .into_iter()
        .map(track_rule_count_snapshot)
        .collect::<Result<Vec<_>, _>>()?;
    let count_events = batch
        .count_events
        .into_iter()
        .map(|event| {
            track_count_event_input(event, &batch.track_session_id, batch.observed_at_unix_ms)
        })
        .collect::<Result<Vec<_>, _>>()?;
    let started_at_unix_ms = tracks
        .iter()
        .map(|track| track.first_observed_at_unix_ms)
        .min()
        .unwrap_or(batch.observed_at_unix_ms)
        .min(batch.observed_at_unix_ms);
    let session = TrackSessionSnapshot {
        track_session_id: batch.track_session_id.clone(),
        tracker_type: batch.tracker_type.clone(),
        stream_epoch: batch.stream_epoch.clone(),
        started_at_unix_ms,
        last_update_at_unix_ms: batch.observed_at_unix_ms,
        ended_at_unix_ms: batch.session_ended.then_some(batch.observed_at_unix_ms),
        end_reason: batch.session_end_reason.clone(),
        current_visible_count: batch.current_visible,
        unique_confirmed_count: batch.unique_confirmed,
        rule_counts: rule_counts.clone(),
    };
    let database = PerceptionTrackBatchInput {
        source_id: batch.source_id.clone(),
        stream_epoch: batch.stream_epoch,
        track_session_id: batch.track_session_id,
        tracker_type: batch.tracker_type,
        observed_at_unix_ms: batch.observed_at_unix_ms,
        session_started: batch.session_started,
        session_ended: batch.session_ended,
        session_end_reason: batch.session_end_reason,
        current_visible: batch.current_visible,
        unique_confirmed: batch.unique_confirmed,
        tracks: tracks.iter().map(database_track_input).collect(),
        rule_counts: rule_counts.iter().map(database_rule_count_input).collect(),
        count_events,
    };
    Ok(ValidatedTrackBatch {
        source_id: batch.source_id,
        session,
        session_started: batch.session_started,
        session_ended: batch.session_ended,
        tracks,
        database,
    })
}

fn track_lifecycle_snapshot(
    track: ProtoPerceptionTrackSnapshot,
    track_session_id: &str,
    tracker_type: &str,
    batch_observed_at_unix_ms: i64,
) -> Result<TrackLifecycleSnapshot, Status> {
    if track.track_id.trim().is_empty()
        || !track
            .track_id
            .starts_with(&format!("atlas:{track_session_id}:"))
        || track.track_session_id != track_session_id
        || track.tracker_type != tracker_type
        || track.class_label.trim().is_empty()
        || !matches!(
            track.lifecycle_state.as_str(),
            "TENTATIVE" | "ACTIVE" | "TEMPORARILY_OCCLUDED" | "LOST" | "CLOSED"
        )
        || !matches!(
            track.update_reason.as_str(),
            "CREATED" | "STATE_CHANGED" | "REACQUIRED" | "PERIODIC" | "CLOSED"
        )
        || track.revision == 0
        || track.age_frames == 0
        || track.observation_count == 0
        || track.age_frames < track.observation_count
        || track.first_observed_at_unix_ms <= 0
        || track.last_observed_at_unix_ms < track.first_observed_at_unix_ms
        || batch_observed_at_unix_ms < track.last_observed_at_unix_ms
        || !finite_unit(track.latest_detection_confidence)
        || !finite_unit(track.prediction_confidence)
    {
        return Err(Status::invalid_argument(
            "perception track lifecycle fields are invalid",
        ));
    }
    let latest_confirmed_box = bounding_box_snapshot(
        track
            .latest_confirmed_box
            .ok_or_else(|| Status::invalid_argument("track confirmed box is required"))?,
    )?;
    let predicted_box = track.predicted_box.map(bounding_box_snapshot).transpose()?;
    if predicted_box.is_none() && track.prediction_confidence != 0.0 {
        return Err(Status::invalid_argument(
            "track prediction confidence requires a predicted box",
        ));
    }
    let closed_at_unix_ms = (track.closed_at_unix_ms > 0).then_some(track.closed_at_unix_ms);
    let closed = track.lifecycle_state == "CLOSED";
    if closed != closed_at_unix_ms.is_some()
        || closed != !track.closure_reason.trim().is_empty()
        || (closed && track.update_reason != "CLOSED")
        || closed_at_unix_ms.is_some_and(|closed_at| {
            closed_at < track.last_observed_at_unix_ms || closed_at > batch_observed_at_unix_ms
        })
    {
        return Err(Status::invalid_argument(
            "perception track closure fields are invalid",
        ));
    }
    Ok(TrackLifecycleSnapshot {
        track_id: track.track_id,
        track_session_id: track.track_session_id,
        tracker_type: track.tracker_type,
        lifecycle_state: track.lifecycle_state,
        revision: track.revision,
        age_frames: track.age_frames,
        observation_count: track.observation_count,
        first_observed_at_unix_ms: track.first_observed_at_unix_ms,
        last_observed_at_unix_ms: track.last_observed_at_unix_ms,
        latest_confirmed_box,
        latest_detection_confidence: track.latest_detection_confidence,
        predicted_box,
        prediction_confidence: track.prediction_confidence,
        closed_at_unix_ms,
        closure_reason: track.closure_reason,
        class_id: track.class_id,
        class_label: track.class_label,
        update_reason: track.update_reason,
    })
}

fn database_track_input(track: &TrackLifecycleSnapshot) -> PerceptionTrackUpdateInput {
    PerceptionTrackUpdateInput {
        track_id: track.track_id.clone(),
        track_session_id: track.track_session_id.clone(),
        tracker_type: track.tracker_type.clone(),
        lifecycle_state: track.lifecycle_state.clone(),
        revision: track.revision,
        age_frames: track.age_frames,
        observation_count: track.observation_count,
        first_observed_at_unix_ms: track.first_observed_at_unix_ms,
        last_observed_at_unix_ms: track.last_observed_at_unix_ms,
        latest_confirmed_box: database_bounding_box(&track.latest_confirmed_box),
        latest_detection_confidence: track.latest_detection_confidence,
        predicted_box: track.predicted_box.as_ref().map(database_bounding_box),
        prediction_confidence: track.prediction_confidence,
        closed_at_unix_ms: track.closed_at_unix_ms,
        closure_reason: track.closure_reason.clone(),
        class_id: track.class_id,
        class_label: track.class_label.clone(),
        update_reason: track.update_reason.clone(),
    }
}

fn track_rule_count_snapshot(count: ProtoTrackRuleCount) -> Result<TrackRuleCountSnapshot, Status> {
    if count.rule_id.trim().is_empty()
        || count.rule_revision == 0
        || !matches!(count.rule_type.as_str(), "LINE" | "POLYGON")
        || (count.rule_type == "LINE" && (count.polygon_entries != 0 || count.polygon_exits != 0))
        || (count.rule_type == "POLYGON" && (count.line_forward != 0 || count.line_reverse != 0))
    {
        return Err(Status::invalid_argument(
            "perception track rule count is invalid",
        ));
    }
    Ok(TrackRuleCountSnapshot {
        rule_id: count.rule_id,
        rule_revision: count.rule_revision,
        rule_type: count.rule_type,
        line_forward: count.line_forward,
        line_reverse: count.line_reverse,
        polygon_entries: count.polygon_entries,
        polygon_exits: count.polygon_exits,
    })
}

fn database_rule_count_input(count: &TrackRuleCountSnapshot) -> TrackRuleCountInput {
    TrackRuleCountInput {
        rule_id: count.rule_id.clone(),
        rule_revision: count.rule_revision,
        rule_type: count.rule_type.clone(),
        line_forward: count.line_forward,
        line_reverse: count.line_reverse,
        polygon_entries: count.polygon_entries,
        polygon_exits: count.polygon_exits,
    }
}

fn track_count_event_input(
    event: ProtoTrackCountEvent,
    track_session_id: &str,
    batch_observed_at_unix_ms: i64,
) -> Result<TrackCountEventInput, Status> {
    let anchor = event
        .anchor
        .ok_or_else(|| Status::invalid_argument("count event anchor is required"))?;
    if event.event_id.trim().is_empty()
        || event.rule_id.trim().is_empty()
        || event.rule_revision == 0
        || event.track_session_id != track_session_id
        || !event
            .track_id
            .starts_with(&format!("atlas:{track_session_id}:"))
        || !matches!(
            event.event_type.as_str(),
            "LINE_FORWARD" | "LINE_REVERSE" | "POLYGON_ENTRY" | "POLYGON_EXIT"
        )
        || event.observed_at_unix_ms <= 0
        || event.observed_at_unix_ms > batch_observed_at_unix_ms
        || !finite_unit(anchor.x)
        || !finite_unit(anchor.y)
    {
        return Err(Status::invalid_argument(
            "perception track count event is invalid",
        ));
    }
    Ok(TrackCountEventInput {
        event_id: event.event_id,
        rule_id: event.rule_id,
        rule_revision: event.rule_revision,
        track_session_id: event.track_session_id,
        track_id: event.track_id,
        event_type: event.event_type,
        observed_at_unix_ms: event.observed_at_unix_ms,
        anchor_x: anchor.x,
        anchor_y: anchor.y,
    })
}

fn database_bounding_box(value: &BoundingBoxSnapshot) -> TrackBoundingBoxInput {
    TrackBoundingBoxInput {
        x: value.x,
        y: value.y,
        width: value.width,
        height: value.height,
    }
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
    let tracking = health.tracking.map(tracking_health_snapshot).transpose()?;
    let source_id = health.source_id;
    Ok((
        source_id,
        PerceptionHealthSnapshot {
            provider: health.provider,
            accelerator: health.accelerator,
            activation_state: if health.activation_state.is_empty() {
                if health.inference_ready {
                    "ACTIVE"
                } else {
                    "INACTIVE"
                }
                .to_string()
            } else {
                health.activation_state
            },
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
            tracking,
            observed_at_unix_ms: health.observed_at_unix_ms,
            received_at_unix_ms: received_at,
        },
    ))
}

fn tracking_health_snapshot(
    tracking: PerceptionTrackingHealth,
) -> Result<TrackingHealthSnapshot, Status> {
    let camera_motion_state = if tracking.camera_motion_state.is_empty() {
        "DISABLED".to_string()
    } else {
        tracking.camera_motion_state.clone()
    };
    if !matches!(
        tracking.algorithm.as_str(),
        "DISABLED" | "BYTE_TRACK" | "BYTE_TRACK_CMC"
    ) || !matches!(
        tracking.state.as_str(),
        "DISABLED" | "READY" | "ACTIVE" | "DEGRADED"
    ) || !matches!(
        camera_motion_state.as_str(),
        "DISABLED" | "WAITING" | "ACTIVE" | "DEGRADED"
    ) || !finite_unit(tracking.camera_motion_confidence)
        || (tracking.state == "ACTIVE" && tracking.session_id.trim().is_empty())
        || (camera_motion_state == "ACTIVE" && tracking.camera_motion_method.trim().is_empty())
    {
        return Err(Status::invalid_argument(
            "perception tracking health is invalid",
        ));
    }
    Ok(TrackingHealthSnapshot {
        algorithm: tracking.algorithm,
        state: tracking.state,
        session_id: tracking.session_id,
        last_reset_reason: tracking.last_reset_reason,
        reset_count: tracking.reset_count,
        last_error: tracking.last_error,
        camera_motion_state,
        camera_motion_method: tracking.camera_motion_method,
        camera_motion_confidence: tracking.camera_motion_confidence,
        re_id_enabled: tracking.re_id_enabled,
    })
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
    fn active_tracking_health_requires_a_temporary_session() {
        let invalid = PerceptionTrackingHealth {
            algorithm: "BYTE_TRACK_CMC".into(),
            state: "ACTIVE".into(),
            session_id: String::new(),
            last_reset_reason: String::new(),
            reset_count: 0,
            last_error: String::new(),
            camera_motion_state: "DISABLED".into(),
            camera_motion_method: String::new(),
            camera_motion_confidence: 0.0,
            re_id_enabled: false,
        };
        assert!(tracking_health_snapshot(invalid).is_err());
    }

    #[test]
    fn lifecycle_batch_is_validated_and_exposed_independently_of_frames() {
        let store = PerceptionStore::default();
        let (outbound, _responses) = mpsc::channel(2);
        store
            .register("session-1", "drone-1", "stream-1", "hailo", 1_000, outbound)
            .expect("register perception");
        let validated = track_batch_snapshot(PerceptionTrackUpdateBatch {
            source_id: "a8-main".into(),
            stream_epoch: "epoch-1".into(),
            track_session_id: "track-session-1".into(),
            tracker_type: "BYTE_TRACK".into(),
            observed_at_unix_ms: 1_100,
            session_started: true,
            session_ended: false,
            session_end_reason: String::new(),
            current_visible: 1,
            unique_confirmed: 1,
            rule_counts: Vec::new(),
            count_events: Vec::new(),
            tracks: vec![ProtoPerceptionTrackSnapshot {
                track_id: "atlas:track-session-1:1".into(),
                track_session_id: "track-session-1".into(),
                tracker_type: "BYTE_TRACK".into(),
                lifecycle_state: "ACTIVE".into(),
                revision: 1,
                age_frames: 2,
                observation_count: 2,
                first_observed_at_unix_ms: 1_100,
                last_observed_at_unix_ms: 1_100,
                latest_confirmed_box: Some(NormalizedBoundingBox {
                    x: 0.1,
                    y: 0.2,
                    width: 0.1,
                    height: 0.2,
                }),
                latest_detection_confidence: 0.9,
                predicted_box: None,
                prediction_confidence: 0.0,
                closed_at_unix_ms: 0,
                closure_reason: String::new(),
                class_id: 0,
                class_label: "person".into(),
                update_reason: "CREATED".into(),
            }],
        })
        .expect("validate lifecycle batch");
        store
            .record_track_batch("drone-1", "stream-1", validated, 1_101)
            .expect("record lifecycle batch");
        let snapshot = store
            .snapshot(Some("drone-1"))
            .expect("perception snapshot");
        assert_eq!(snapshot.sources.len(), 1);
        assert_eq!(snapshot.sources[0].tracks.len(), 1);
        assert_eq!(snapshot.sources[0].tracks[0].lifecycle_state, "ACTIVE");
        assert_eq!(
            snapshot.sources[0]
                .track_session
                .as_ref()
                .expect("track session")
                .track_session_id,
            "track-session-1"
        );
        assert_eq!(
            snapshot.sources[0]
                .track_session
                .as_ref()
                .expect("track session")
                .current_visible_count,
            1
        );
        assert!(snapshot.sources[0].latest_frame.is_none());
    }

    #[test]
    fn operational_availability_requires_connected_healthy_publishing_source() {
        let store = PerceptionStore::default();
        let now = unix_time_ms();
        let (outbound, _responses) = mpsc::channel(2);
        store
            .register("session-1", "drone-1", "stream-1", "hailo", now, outbound)
            .expect("register perception");
        let before_health = store
            .snapshot(Some("drone-1"))
            .expect("perception snapshot");
        assert!(!before_health.operational_availability().0);

        store
            .record_health(
                "drone-1",
                "stream-1",
                PerceptionHealth {
                    source_id: "a8-main".into(),
                    provider: "hailo".into(),
                    accelerator: "hailo-8".into(),
                    activation_state: "ACTIVE".into(),
                    input_connected: true,
                    inference_ready: true,
                    output_publishing: true,
                    input_fps: 30.0,
                    inference_fps: 30.0,
                    dropped_frames: 0,
                    last_frame_at_unix_ms: now + 1,
                    last_detection_at_unix_ms: now + 1,
                    last_error: String::new(),
                    model: Some(PerceptionModelIdentity {
                        name: "atlas-objects".into(),
                        version: "1".into(),
                        artifact_hash: String::new(),
                    }),
                    observed_at_unix_ms: now + 1,
                    tracking: None,
                },
                now + 2,
            )
            .expect("record healthy perception source");
        let healthy = store.snapshot(Some("drone-1")).expect("healthy snapshot");
        let (available, evidence) = healthy.operational_availability();
        assert!(available);
        assert_eq!(evidence["sources"][0]["health"]["inferenceReady"], true);

        store
            .record_health(
                "drone-1",
                "stream-1",
                PerceptionHealth {
                    source_id: "a8-main".into(),
                    provider: "hailo".into(),
                    accelerator: "hailo-8".into(),
                    activation_state: "INACTIVE".into(),
                    input_connected: false,
                    inference_ready: false,
                    output_publishing: true,
                    input_fps: 0.0,
                    inference_fps: 0.0,
                    dropped_frames: 0,
                    last_frame_at_unix_ms: now + 1,
                    last_detection_at_unix_ms: now + 1,
                    last_error: String::new(),
                    model: Some(PerceptionModelIdentity {
                        name: "atlas-objects".into(),
                        version: "1".into(),
                        artifact_hash: String::new(),
                    }),
                    observed_at_unix_ms: now + 3,
                    tracking: None,
                },
                now + 4,
            )
            .expect("record intentionally inactive perception source");
        assert!(
            store
                .snapshot(Some("drone-1"))
                .expect("inactive snapshot")
                .operational_availability()
                .0,
            "an intentionally inactive but connected runtime is operationally available"
        );

        store.close("drone-1", "stream-1", now + 5);
        assert!(
            !store
                .snapshot(Some("drone-1"))
                .expect("closed perception snapshot")
                .operational_availability()
                .0
        );
    }

    #[test]
    fn alignment_uses_native_receive_time_and_inference_latency() {
        let store = PerceptionStore::default();
        let (outbound, _responses) = mpsc::channel(2);
        store
            .register("session-1", "drone-1", "stream-1", "hailo", 1_000, outbound)
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

    #[tokio::test]
    async fn frame_subscription_is_delivered_to_the_matching_agent_stream() {
        let store = PerceptionStore::default();
        let (outbound, mut responses) = mpsc::channel(2);
        store
            .register("session-1", "drone-1", "stream-1", "hailo", 1_000, outbound)
            .expect("register perception");
        store
            .start_or_renew_frame_subscription("drone-1", "view-1", "live_view", 12_000)
            .await
            .expect("start frame subscription");
        let response = responses
            .recv()
            .await
            .expect("subscription response")
            .expect("valid subscription response");
        let subscription = response
            .payload
            .and_then(|payload| match payload {
                ground_station_perception::Payload::FrameSubscription(value) => Some(value),
                _ => None,
            })
            .expect("frame subscription payload");
        assert_eq!(subscription.subscription_id, "view-1");
        assert_eq!(subscription.lease_duration_ms, 12_000);
        assert_eq!(
            subscription.action(),
            PerceptionFrameSubscriptionAction::StartOrRenew
        );
    }

    #[tokio::test]
    async fn counting_rules_are_delivered_as_a_complete_source_set() {
        let store = PerceptionStore::default();
        let (outbound, mut responses) = mpsc::channel(2);
        store
            .register("session-1", "drone-1", "stream-1", "hailo", 1_000, outbound)
            .expect("register perception");
        store
            .send_counting_rules(
                "drone-1",
                "a8-main",
                &[CountingRuleSnapshot {
                    id: "gate".into(),
                    drone_id: "drone-1".into(),
                    source_id: "a8-main".into(),
                    label: "Gate".into(),
                    rule_type: "LINE".into(),
                    revision: 2,
                    points: vec![
                        crate::database::CountingPoint { x: 0.5, y: 0.0 },
                        crate::database::CountingPoint { x: 0.5, y: 1.0 },
                    ],
                    class_ids: vec![0],
                    enabled: true,
                    updated_at_unix_ms: 1_000,
                }],
            )
            .await
            .expect("send counting rules");
        let response = responses
            .recv()
            .await
            .expect("counting response")
            .expect("valid counting response");
        let rules = response
            .payload
            .and_then(|payload| match payload {
                ground_station_perception::Payload::CountingRules(value) => Some(value),
                _ => None,
            })
            .expect("counting rules payload");
        assert_eq!(rules.source_id, "a8-main");
        assert_eq!(rules.rules.len(), 1);
        assert_eq!(rules.rules[0].revision, 2);
        assert_eq!(rules.rules[0].points.len(), 2);
    }
}
