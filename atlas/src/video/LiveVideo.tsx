import { useEffect, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent } from "react";
import { invoke } from "@tauri-apps/api/core";
import type { OperationalAlert } from "../alerts/OperationalAlerts";
import { sampleTerrainElevation, terrainSource, terrainSourceVersion } from "../missions/terrain";
import type { FleetAircraft } from "../operationsTypes";
import "./LiveVideo.css";

type LiveVideoProps = {
  nativeAvailable: boolean;
  droneId?: string;
  aircraft?: FleetAircraft;
  highestAlert?: OperationalAlert;
  recordingPlanned?: boolean;
  recordingContext?: {
    incidentId?: string;
    missionId?: string;
    missionRunId?: string;
  };
  compact?: boolean;
};

type EvidenceRecordingSession = {
  id: string;
  sourceId: string;
  status: "REQUESTED" | "RUNNING" | "SUCCEEDED" | "FAILED";
  droneId: string;
  incidentId?: string;
  missionId?: string;
  missionRunId?: string;
  requestedAtUnixMs: number;
  startedAtUnixMs?: number;
  stoppedAtUnixMs?: number;
  finalizedSegmentCount: number;
  totalBytes: number;
  stopReason: string;
  errorCode: string;
  errorMessage: string;
  gaps: Array<{ id: string; cause: string; gapStartedAtUnixMs: number; gapEndedAtUnixMs?: number }>;
};

type EvidenceRecordingStatus = {
  configured: boolean;
  sourceId: string;
  evidenceRoot: string;
  segmentDurationSeconds: number;
  availableBytes?: number;
  warningFreeBytes: number;
  stopFreeBytes: number;
  diskState: "READY" | "WARNING" | "STOP" | "UNKNOWN";
  session?: EvidenceRecordingSession;
};

type EvidenceAsset = {
  id: string;
  assetType: "STILL" | "EVENT_CLIP";
  status: "PENDING" | "READY" | "FAILED" | "TRASHED" | "PURGING" | "PURGED";
  trackId?: string;
};

type TrackAnnotation = {
  id: string;
  annotationType: "NOTE" | "EVIDENCE_MARKER";
};

type VideoStreamSnapshot = {
  status: "stopped" | "connecting" | "playing" | "error";
  droneId?: string;
  sourceId: string;
  width: number;
  height: number;
  targetFramesPerSecond: number;
  playoutDelayMs: number;
  alignmentToleranceMs: number;
  overlayOffsetMs: number;
  startedAtUnixMs?: number;
  lastFrameAtUnixMs?: number;
  latestSequence: number;
  droppedFrames: number;
  lastError?: string;
};

type BoundingBox = { x: number; y: number; width: number; height: number };

type PerceptionDetection = {
  trackId: string;
  classId: number;
  classLabel: string;
  confidence: number;
  boundingBox: BoundingBox;
  attributes?: unknown;
};

type PerceptionFrame = {
  streamEpoch: string;
  frameId: string;
  observedAtUnixMs: number;
  receivedAtUnixMs: number;
  sourcePtsNs: number;
  imageWidth: number;
  imageHeight: number;
  model: { name: string; version: string; artifactHash: string };
  inferenceLatencyMs: number;
  detections: PerceptionDetection[];
};

type TrackLifecycle = {
  trackId: string;
  trackSessionId: string;
  trackerType: "BYTE_TRACK" | "BYTE_TRACK_CMC";
  lifecycleState: "TENTATIVE" | "ACTIVE" | "TEMPORARILY_OCCLUDED" | "LOST" | "CLOSED";
  revision: number;
  ageFrames: number;
  observationCount: number;
  firstObservedAtUnixMs: number;
  lastObservedAtUnixMs: number;
  latestConfirmedBox: BoundingBox;
  latestDetectionConfidence: number;
  predictedBox?: BoundingBox;
  predictionConfidence: number;
  closedAtUnixMs?: number;
  closureReason: string;
  classId: number;
  classLabel: string;
  updateReason: "CREATED" | "STATE_CHANGED" | "REACQUIRED" | "PERIODIC" | "CLOSED";
};

type CountingPoint = { x: number; y: number };

type CountingRule = {
  id: string;
  droneId: string;
  sourceId: string;
  label: string;
  ruleType: "LINE" | "POLYGON";
  revision: number;
  points: CountingPoint[];
  classIds: number[];
  enabled: boolean;
  updatedAtUnixMs: number;
};

type RuleCount = {
  ruleId: string;
  label: string;
  ruleType: "LINE" | "POLYGON";
  ruleRevision: number;
  lineForward: number;
  lineReverse: number;
  polygonEntries: number;
  polygonExits: number;
};

type PerceptionCounts = {
  sourceId: string;
  trackSessionId?: string;
  currentVisibleCount: number;
  uniqueSessionTracks: number;
  missionId?: string;
  missionRunId?: string;
  uniqueMissionTracks: number;
  ruleCounts: RuleCount[];
};

type TrackSelection = {
  selectionId: string;
  droneId: string;
  trackSessionId: string;
  trackId: string;
  status: "SELECTED" | "OCCLUDED" | "LOST" | "CLOSED";
  selectedBy: string;
  selectedAtUnixMs: number;
  lastStateChangeAtUnixMs: number;
  resultReason: string;
  lifecycleState: TrackLifecycle["lifecycleState"];
  ageFrames: number;
  observationCount: number;
  lastObservedAtUnixMs: number;
  confidence: number;
  predictionConfidence: number;
  classLabel: string;
  annotationCount: number;
};

type TrackSample = {
  revision: number;
  sampleReason: TrackLifecycle["updateReason"];
  lifecycleState: TrackLifecycle["lifecycleState"];
  observedAtUnixMs: number;
  detectionConfidence: number;
  predictionConfidence: number;
};

type TrackGeolocation = {
  id: string;
  commandId: string;
  selectionId: string;
  status: "REQUESTED" | "SUCCEEDED" | "REJECTED";
  requestedAtUnixMs: number;
  resolvedAtUnixMs?: number;
  aimPoint: "GROUND_CONTACT" | "TARGET_CENTER";
  assumedAimPointHeightM: number;
  assumedAimPointHeightUncertaintyM: number;
  groundAltitudeAmslM: number;
  groundAltitudeUncertaintyM: number;
  groundAltitudeSource: string;
  groundAltitudeSourceVersion: string;
  latitude?: number;
  longitude?: number;
  altitudeAmslM?: number;
  horizontalUncertaintyM?: number;
  method: string;
  frameObservedAtUnixMs?: number;
  initialLatitude?: number;
  initialLongitude?: number;
  initialAltitudeAmslM?: number;
  initialHorizontalUncertaintyM?: number;
  initialMethod: string;
  refinementStatus: "NOT_REQUESTED" | "CONVERGED" | "MAX_ITERATIONS";
  terrainSource: string;
  terrainSourceVersion: string;
  terrainVerticalUncertaintyM?: number;
  terrainIterationCount: number;
  terrainResidualM?: number;
  rangeSource: string;
  filteredLatitude?: number;
  filteredLongitude?: number;
  targetVelocityNorthMps?: number;
  targetVelocityEastMps?: number;
  targetSpeedMps?: number;
  targetDirectionDeg?: number;
  targetVelocityUncertaintyMps?: number;
  motionStatus: string;
  rejectionCode: string;
  rejectionReason: string;
  evidence?: unknown;
};

type CommandReceipt = {
  id: string;
  status: string;
  resultCode: string;
  resultMessage: string;
  deadlineAtUnixMs: number;
};

type CameraFollowState = {
  state: "starting" | "following" | "holding" | "stopping" | "stopped" | "error";
  controlSessionId: string;
  sourceId: string;
  trackSessionId: string;
  trackId: string;
  controlContext: { kind: "inspection" } | { kind: "mission_override"; missionRunId: string };
  message: string;
};

type CountingRuleDraft = {
  ruleType: "LINE" | "POLYGON";
  label: string;
  points: CountingPoint[];
};

type VideoFrameHeader = {
  sequence: number;
  receivedAtUnixMs: number;
  width: number;
  height: number;
  mimeType: string;
  overlay?: { alignmentDeltaMs: number; frame: PerceptionFrame };
};

type PerceptionSnapshot = {
  status: string;
  provider: string;
  sources: Array<{
    sourceId: string;
    trackSession?: {
      trackSessionId: string;
      trackerType: "BYTE_TRACK" | "BYTE_TRACK_CMC";
      streamEpoch: string;
      startedAtUnixMs: number;
      lastUpdateAtUnixMs: number;
      endedAtUnixMs?: number;
      endReason: string;
      currentVisibleCount: number;
      uniqueConfirmedCount: number;
      ruleCounts: RuleCount[];
    };
    tracks: TrackLifecycle[];
    health?: {
      accelerator: string;
      activationState: "ACTIVE" | "INACTIVE" | "FAILED";
      inferenceReady: boolean;
      inferenceFps: number;
      droppedFrames: number;
      lastError: string;
      tracking?: TrackingHealth;
    };
  }>;
};

type ParsedVideoFrame = { header: VideoFrameHeader; jpeg: Blob };
type FrameSubscriptionState = "idle" | "requesting" | "active" | "waiting";

type TrackingHealth = {
  algorithm: "DISABLED" | "BYTE_TRACK" | "BYTE_TRACK_CMC";
  state: "DISABLED" | "READY" | "ACTIVE" | "DEGRADED";
  sessionId: string;
  lastResetReason: string;
  resetCount: number;
  lastError: string;
  cameraMotionState: "DISABLED" | "WAITING" | "ACTIVE" | "DEGRADED";
  cameraMotionMethod: string;
  cameraMotionConfidence: number;
  reIdEnabled: boolean;
};

const packetMagic = [0x41, 0x54, 0x56, 0x31];
const frameSubscriptionLeaseMs = 12_000;
const payloadLeaseDurationMs = 7_000;
const mvpTargetCenterHeightMeters = 0.9;
const mvpTargetCenterHeightUncertaintyMeters = 0.9;
const mvpHomePlaneUncertaintyMeters = 25;
const mvpTerrainLookupTimeoutMs = 3_000;
const terrainRefinementConvergenceMeters = 0.75;
const terrainRefinementMaximumIterations = 6;
const terminalCommandStates = new Set(["succeeded", "failed", "rejected", "timed_out", "cancelled"]);

export function LiveVideo({
  nativeAvailable,
  droneId,
  aircraft,
  highestAlert,
  recordingPlanned = false,
  recordingContext,
  compact = false,
}: LiveVideoProps) {
  const videoCanvasRef = useRef<HTMLCanvasElement>(null);
  const overlayCanvasRef = useRef<HTMLCanvasElement>(null);
  const overlayEnabledRef = useRef(true);
  const latestOverlayFrameRef = useRef<PerceptionFrame | undefined>(undefined);
  const selectionRef = useRef<TrackSelection | undefined>(undefined);
  const countingRulesRef = useRef<CountingRule[]>([]);
  const countingRuleDraftRef = useRef<CountingRuleDraft | undefined>(undefined);
  const missionRunIdRef = useRef<string | undefined>(recordingContext?.missionRunId);
  const cameraFollowRef = useRef<CameraFollowState | undefined>(undefined);
  const [overlayEnabled, setOverlayEnabled] = useState(true);
  const [perceptionRequested, setPerceptionRequested] = useState(false);
  const [stream, setStream] = useState<VideoStreamSnapshot>();
  const [perception, setPerception] = useState<PerceptionSnapshot>();
  const [counts, setCounts] = useState<PerceptionCounts>();
  const [countingRules, setCountingRules] = useState<CountingRule[]>([]);
  const [countingRuleDraft, setCountingRuleDraft] = useState<CountingRuleDraft>();
  const [countingRulePending, setCountingRulePending] = useState(false);
  const [selection, setSelection] = useState<TrackSelection>();
  const [trackSamples, setTrackSamples] = useState<TrackSample[]>([]);
  const [trackGeolocations, setTrackGeolocations] = useState<TrackGeolocation[]>([]);
  const [geolocationPending, setGeolocationPending] = useState(false);
  const [selectionPending, setSelectionPending] = useState(false);
  const [trackNote, setTrackNote] = useState("");
  const [trackActionError, setTrackActionError] = useState<string>();
  const [cameraFollow, setCameraFollow] = useState<CameraFollowState>();
  const [detectionCount, setDetectionCount] = useState(0);
  const [alignmentDeltaMs, setAlignmentDeltaMs] = useState<number>();
  const [error, setError] = useState<string>();
  const [frameSubscriptionState, setFrameSubscriptionState] = useState<FrameSubscriptionState>("idle");
  const [hudReduced, setHudReduced] = useState(false);
  const [recording, setRecording] = useState<EvidenceRecordingStatus>();
  const [recordingPending, setRecordingPending] = useState<"start" | "stop">();
  const [recordingError, setRecordingError] = useState<string>();
  const [evidenceAssetPending, setEvidenceAssetPending] = useState<"still" | "clip">();
  const [evidenceAssetMessage, setEvidenceAssetMessage] = useState<string>();

  useEffect(() => {
    overlayEnabledRef.current = overlayEnabled;
    if (!overlayEnabled) {
      const overlay = overlayCanvasRef.current;
      overlay?.getContext("2d")?.clearRect(0, 0, overlay.width, overlay.height);
    }
  }, [overlayEnabled]);

  useEffect(() => {
    selectionRef.current = selection;
  }, [selection]);

  useEffect(() => {
    cameraFollowRef.current = cameraFollow;
  }, [cameraFollow]);

  useEffect(() => {
    countingRulesRef.current = countingRules;
  }, [countingRules]);

  useEffect(() => {
    countingRuleDraftRef.current = countingRuleDraft;
  }, [countingRuleDraft]);

  useEffect(() => {
    missionRunIdRef.current = recordingContext?.missionRunId;
  }, [recordingContext?.missionRunId]);

  useEffect(() => {
    if (!nativeAvailable || !droneId) {
      setStream(undefined);
      setPerception(undefined);
      setCounts(undefined);
      setCountingRules([]);
      setSelection(undefined);
      setTrackSamples([]);
      setTrackGeolocations([]);
      setCameraFollow(undefined);
      return;
    }
    let active = true;
    let lastSequence = 0;
    let lastStatsUpdate = 0;
    async function readFrames() {
      while (active) {
        try {
          const packet = await invoke<ArrayBuffer>("video_stream_frame", {
            droneId,
            afterSequence: lastSequence,
          });
          if (!active) return;
          if (packet.byteLength === 0) {
            await wait(20);
            continue;
          }
          const frame = parseVideoFramePacket(packet);
          if (frame.header.sequence <= lastSequence) continue;
          lastSequence = frame.header.sequence;
          latestOverlayFrameRef.current = frame.header.overlay?.frame;
          await renderFrame(
            videoCanvasRef.current,
            overlayCanvasRef.current,
            frame,
            overlayEnabledRef.current,
            selectionRef.current,
            countingRulesRef.current,
            countingRuleDraftRef.current,
          );
          const now = performance.now();
          if (now - lastStatsUpdate > 200) {
            lastStatsUpdate = now;
            setDetectionCount(frame.header.overlay?.frame.detections.length ?? 0);
            setAlignmentDeltaMs(frame.header.overlay?.alignmentDeltaMs);
          }
        } catch (reason) {
          if (active) setError(messageFrom(reason));
          await wait(250);
        }
      }
    }

    async function refreshStatus() {
      try {
        const nextStream = await invoke<VideoStreamSnapshot>("video_stream_snapshot");
        const [nextPerception, nextCounts, nextRules, nextSelection] = await Promise.all([
          invoke<PerceptionSnapshot | null>("perception_snapshot", { droneId }),
          invoke<PerceptionCounts>("perception_counts", {
            droneId,
            sourceId: nextStream.sourceId,
            missionRunId: missionRunIdRef.current ?? null,
          }),
          invoke<CountingRule[]>("perception_counting_rules", {
            droneId,
            sourceId: nextStream.sourceId,
          }),
          invoke<TrackSelection | null>("perception_track_selection", { droneId }),
        ]);
        if (!active) return;
        setStream(nextStream);
        setPerception(nextPerception ?? undefined);
        setCounts(nextCounts);
        setCountingRules(nextRules);
        setSelection(nextSelection ?? undefined);
        if (nextSelection) {
          const [samples, geolocations] = await Promise.all([
            invoke<TrackSample[]>("perception_track_samples", {
              trackSessionId: nextSelection.trackSessionId,
              trackId: nextSelection.trackId,
              limit: 12,
            }),
            invoke<TrackGeolocation[]>("perception_track_geolocations", {
              trackSessionId: nextSelection.trackSessionId,
              trackId: nextSelection.trackId,
              limit: 8,
            }),
          ]);
          if (active) {
            setTrackSamples(samples);
            setTrackGeolocations(geolocations);
          }
        } else {
          setTrackSamples([]);
          setTrackGeolocations([]);
        }
        setError(nextStream.lastError);
      } catch (reason) {
        if (active) setError(messageFrom(reason));
      }
    }

    void invoke<VideoStreamSnapshot>("video_stream_start", { droneId })
      .then((snapshot) => {
        if (!active) return;
        setStream(snapshot);
        setError(undefined);
        void readFrames();
        void refreshStatus();
      })
      .catch((reason) => {
        if (active) setError(messageFrom(reason));
      });
    const statusInterval = window.setInterval(() => void refreshStatus(), 1_000);

    return () => {
      active = false;
      window.clearInterval(statusInterval);
      void invoke("video_stream_stop", { droneId }).catch(() => undefined);
    };
  }, [droneId, nativeAvailable]);

  useEffect(() => {
    const follow = cameraFollow;
    if (!follow || !["following", "holding"].includes(follow.state)) return;
    if (!selection || selection.trackSessionId !== follow.trackSessionId || selection.trackId !== follow.trackId) {
      void stopCameraFollow("The selected track changed. Camera follow stopped.");
      return;
    }
    if (selection.status === "OCCLUDED") {
      setCameraFollow((current) => current ? { ...current, state: "holding", message: "Target temporarily occluded · holding the current gimbal angle" } : current);
    } else if (selection.status === "SELECTED") {
      setCameraFollow((current) => current ? { ...current, state: "following", message: "Onboard image-space controller active" } : current);
    } else {
      void stopCameraFollow(`Track ${selection.status.toLowerCase()}. Camera follow stopped without selecting a replacement.`);
    }
  }, [selection?.trackSessionId, selection?.trackId, selection?.status]);

  useEffect(() => {
    const follow = cameraFollow;
    if (!follow || !["following", "holding"].includes(follow.state) || !stream?.sourceId || stream.sourceId === follow.sourceId) return;
    void stopCameraFollow("Camera source changed. Camera follow stopped.");
  }, [stream?.sourceId]);

  useEffect(() => {
    if (!droneId || !cameraFollow || !["following", "holding"].includes(cameraFollow.state)) return;
    const interval = window.setInterval(() => {
      const follow = cameraFollowRef.current;
      if (!follow || !["following", "holding"].includes(follow.state)) return;
      void dispatchVehicleCommand(droneId, "payload_control_renew", {
        ...followCommandIdentity(follow),
        leaseDurationMs: payloadLeaseDurationMs,
      }).catch((reason) => {
        setCameraFollow((current) => current ? {
          ...current,
          state: "error",
          message: `Payload lease renewal failed. The Agent will stop the gimbal automatically: ${messageFrom(reason)}`,
        } : current);
      });
    }, 3_000);
    return () => window.clearInterval(interval);
  }, [droneId, cameraFollow?.controlSessionId, cameraFollow?.state]);

  useEffect(() => {
    return () => {
      const follow = cameraFollowRef.current;
      if (!droneId || !follow || ["stopped", "error"].includes(follow.state)) return;
      void endCameraFollowCommands(droneId, follow);
    };
  }, [droneId]);

  useEffect(() => {
    setPerceptionRequested(false);
    setFrameSubscriptionState("idle");
  }, [droneId]);

  useEffect(() => {
    if (!nativeAvailable || !droneId || !perceptionRequested) {
      return;
    }
    let active = true;
    const subscriptionId = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;

    async function updateFrameSubscription(command: "perception_frame_subscription_start" | "perception_frame_subscription_renew") {
      try {
        await invoke(command, {
          droneId,
          subscriptionId,
          purpose: "live_view",
          leaseDurationMs: frameSubscriptionLeaseMs,
        });
        if (active) setFrameSubscriptionState("waiting");
      } catch (reason) {
        if (active) {
          setFrameSubscriptionState("waiting");
          setError(messageFrom(reason));
        }
      }
    }

    setFrameSubscriptionState("requesting");
    void updateFrameSubscription("perception_frame_subscription_start");
    const subscriptionInterval = window.setInterval(
      () => void updateFrameSubscription("perception_frame_subscription_renew"),
      5_000,
    );
    return () => {
      active = false;
      window.clearInterval(subscriptionInterval);
      setFrameSubscriptionState("idle");
      void invoke("perception_frame_subscription_stop", {
        droneId,
        subscriptionId,
        purpose: "live_view",
      }).catch(() => undefined);
    };
  }, [droneId, nativeAvailable, perceptionRequested]);

  useEffect(() => {
    if (!nativeAvailable || !droneId) {
      setRecording(undefined);
      setRecordingError(undefined);
      return;
    }
    let active = true;
    async function refreshRecording() {
      try {
        const snapshot = await invoke<EvidenceRecordingStatus>("evidence_recording_status", {
          droneId,
          incidentId: recordingContext?.incidentId ?? null,
          missionId: recordingContext?.missionId ?? null,
          missionRunId: recordingContext?.missionRunId ?? null,
        });
        if (!active) return;
        setRecording(snapshot);
        setRecordingError(undefined);
      } catch (reason) {
        if (active) setRecordingError(messageFrom(reason));
      }
    }
    void refreshRecording();
    const interval = window.setInterval(refreshRecording, 1_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [droneId, nativeAvailable, recordingContext?.incidentId, recordingContext?.missionId, recordingContext?.missionRunId]);

  async function startRecording() {
    if (!droneId || recordingPending) return;
    setRecordingPending("start");
    setRecordingError(undefined);
    try {
      const session = await invoke<EvidenceRecordingSession>("start_evidence_recording", {
        input: {
          droneId,
          incidentId: recordingContext?.incidentId ?? null,
          missionId: recordingContext?.missionId ?? null,
          missionRunId: recordingContext?.missionRunId ?? null,
        },
      });
      setRecording((current) => current ? { ...current, session } : current);
    } catch (reason) {
      setRecordingError(messageFrom(reason));
    } finally {
      setRecordingPending(undefined);
    }
  }

  async function stopRecording() {
    const session = recording?.session;
    if (!session || !["REQUESTED", "RUNNING"].includes(session.status) || recordingPending) return;
    setRecordingPending("stop");
    setRecordingError(undefined);
    try {
      const updated = await invoke<EvidenceRecordingSession>("stop_evidence_recording", {
        recordingSessionId: session.id,
      });
      setRecording((current) => current ? { ...current, session: updated } : current);
    } catch (reason) {
      setRecordingError(messageFrom(reason));
    } finally {
      setRecordingPending(undefined);
    }
  }

  async function selectTrack(track: TrackLifecycle) {
    if (!droneId || selectionPending || track.lifecycleState !== "ACTIVE") return;
    setSelectionPending(true);
    setTrackActionError(undefined);
    try {
      const selected = await invoke<TrackSelection>("select_perception_track", {
        input: {
          droneId,
          trackSessionId: track.trackSessionId,
          trackId: track.trackId,
          actor: "operator",
        },
      });
      setSelection(selected);
      setTrackSamples([]);
      setTrackGeolocations([]);
    } catch (reason) {
      setTrackActionError(messageFrom(reason));
    } finally {
      setSelectionPending(false);
    }
  }

  async function clearTrackSelection() {
    if (!droneId || selectionPending) return;
    setSelectionPending(true);
    setTrackActionError(undefined);
    try {
      if (cameraFollow && !["stopped", "error"].includes(cameraFollow.state)) {
        await stopCameraFollow("Camera follow stopped by operator.");
      }
      await invoke("clear_perception_track_selection", { droneId, actor: "operator" });
      setSelection(undefined);
      setTrackSamples([]);
      setTrackGeolocations([]);
      setTrackNote("");
    } catch (reason) {
      setTrackActionError(messageFrom(reason));
    } finally {
      setSelectionPending(false);
    }
  }

  async function startCameraFollow() {
    if (!droneId || !selection || selection.status !== "SELECTED" || !stream?.sourceId || cameraFollow?.state === "starting") return;
    if (cameraFollow?.state === "error") {
      await endCameraFollowCommands(droneId, cameraFollow).catch(() => undefined);
    }
    const controlContext = cameraFollowControlContext(recordingContext?.missionRunId, aircraft);
    if (!controlContext) {
      setTrackActionError("Camera follow requires an active mission, or a disarmed on-ground inspection context.");
      return;
    }
    const gimbalId = capabilityNumber(aircraft?.agentCapabilities ?? [], /^gimbal:id:(\d+)$/);
    const controlSessionId = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
    const next: CameraFollowState = {
      state: "starting", controlSessionId, sourceId: stream.sourceId,
      trackSessionId: selection.trackSessionId, trackId: selection.trackId,
      controlContext, message: "Taking payload control…",
    };
    setCameraFollow(next);
    setTrackActionError(undefined);
    let leaseAcquired = false;
    try {
      await dispatchVehicleCommand(droneId, "payload_control_begin", {
        ...followCommandIdentity(next, gimbalId),
        leaseDurationMs: payloadLeaseDurationMs,
      });
      leaseAcquired = true;
      await dispatchVehicleCommand(droneId, "gimbal_follow_start", {
        ...followCommandIdentity(next, gimbalId),
        sourceId: next.sourceId,
        trackSessionId: next.trackSessionId,
        trackId: next.trackId,
      });
      setCameraFollow({ ...next, state: "following", message: "Onboard image-space controller active" });
    } catch (reason) {
      if (leaseAcquired) {
        await dispatchVehicleCommand(droneId, "payload_control_end", followCommandIdentity(next, gimbalId)).catch(() => undefined);
      }
      setCameraFollow({ ...next, state: "error", message: messageFrom(reason) });
    }
  }

  async function stopCameraFollow(message = "Camera follow stopped by operator.") {
    const follow = cameraFollowRef.current ?? cameraFollow;
    if (!droneId || !follow || ["stopping", "stopped"].includes(follow.state)) return;
    setCameraFollow({ ...follow, state: "stopping", message: "Stopping gimbal movement and releasing payload control…" });
    try {
      await endCameraFollowCommands(droneId, follow);
      setCameraFollow({ ...follow, state: "stopped", message });
    } catch (reason) {
      setCameraFollow({
        ...follow,
        state: "error",
        message: `Stop was requested but could not be confirmed. The short payload lease remains the fail-safe: ${messageFrom(reason)}`,
      });
    }
  }

  async function annotateTrack(annotationType: "NOTE" | "EVIDENCE_MARKER") {
    if (!selection || selectionPending) return;
    setSelectionPending(true);
    setTrackActionError(undefined);
    if (annotationType === "EVIDENCE_MARKER") {
      setEvidenceAssetPending("clip");
      setEvidenceAssetMessage(undefined);
    }
    try {
      const annotation = await invoke<TrackAnnotation>("annotate_perception_track", {
        input: {
          selectionId: selection.selectionId,
          annotationType,
          body: annotationType === "NOTE" ? trackNote.trim() : "Operator evidence marker",
          evidenceRecordingSessionId: annotationType === "EVIDENCE_MARKER" ? recordingSession?.id ?? null : null,
          actor: "operator",
        },
      });
      setSelection((current) => current ? { ...current, annotationCount: current.annotationCount + 1 } : current);
      if (annotationType === "EVIDENCE_MARKER") {
        const clip = await invoke<EvidenceAsset>("queue_evidence_event_clip", {
          input: {
            evidenceMarkerAnnotationId: annotation.id,
            preRollSeconds: 10,
            postRollSeconds: 10,
            actor: "operator",
          },
        });
        setEvidenceAssetMessage(
          clip.status === "READY"
            ? "Track event clip is ready in Evidence."
            : "Track event marked. The clip will publish after verified post-roll arrives.",
        );
      }
      if (annotationType === "NOTE") setTrackNote("");
    } catch (reason) {
      setTrackActionError(messageFrom(reason));
    } finally {
      setSelectionPending(false);
      if (annotationType === "EVIDENCE_MARKER") setEvidenceAssetPending(undefined);
    }
  }

  async function captureStill() {
    if (!droneId || !playing || evidenceAssetPending) return;
    setEvidenceAssetPending("still");
    setEvidenceAssetMessage(undefined);
    setRecordingError(undefined);
    try {
      const asset = await invoke<EvidenceAsset>("capture_evidence_still", {
        input: {
          droneId,
          incidentId: recordingContext?.incidentId ?? null,
          missionId: recordingContext?.missionId ?? null,
          missionRunId: recordingContext?.missionRunId ?? null,
          selectionId: selection?.selectionId ?? null,
          actor: "operator",
        },
      });
      setEvidenceAssetMessage(asset.trackId ? "Track-linked still saved to Evidence." : "Still saved to Evidence.");
    } catch (reason) {
      setRecordingError(messageFrom(reason));
    } finally {
      setEvidenceAssetPending(undefined);
    }
  }

  async function geolocateSelectedTrack() {
    if (!droneId || !selection || selection.status !== "SELECTED" || !stream?.sourceId || geolocationPending) return;
    setGeolocationPending(true);
    setTrackActionError(undefined);
    try {
      const ground = await resolveAutomaticGroundAltitude(aircraft);
      const receipt = await dispatchVehicleCommand(droneId, "geolocate_selected_track", {
        selectionId: selection.selectionId,
        sourceId: stream.sourceId,
        trackSessionId: selection.trackSessionId,
        trackId: selection.trackId,
        gimbalId: capabilityNumber(aircraft?.agentCapabilities ?? [], /^gimbal:id:(\d+)$/),
        aimPoint: "TARGET_CENTER",
        groundAltitudeAmslMeters: ground.altitudeAmslMeters,
        groundAltitudeUncertaintyMeters: ground.uncertaintyMeters,
        groundAltitudeSource: ground.source,
        groundAltitudeSourceVersion: ground.sourceVersion,
        groundAltitudeResolvedAtUnixMs: ground.resolvedAtUnixMs,
        assumedAimPointHeightMeters: mvpTargetCenterHeightMeters,
        assumedAimPointHeightUncertaintyMeters: mvpTargetCenterHeightUncertaintyMeters,
        requestedBy: "operator",
      });
      const geolocations = await invoke<TrackGeolocation[]>("perception_track_geolocations", {
        trackSessionId: selection.trackSessionId,
        trackId: selection.trackId,
        limit: 8,
      });
      const initial = geolocations.find((candidate) => candidate.commandId === receipt.id);
      if (!initial || initial.status !== "SUCCEEDED") {
        throw new Error("The successful Agent response did not produce a durable coordinate.");
      }
      try {
        const refined = await refineTrackGeolocation(initial);
        setTrackGeolocations([refined, ...geolocations.filter((candidate) => candidate.id !== refined.id)]);
      } catch (reason) {
        setTrackGeolocations(geolocations);
        setTrackActionError(`Initial coordinate saved; target-area terrain refinement was unavailable. ${messageFrom(reason)}`);
      }
    } catch (reason) {
      setTrackActionError(messageFrom(reason));
    } finally {
      try {
        const geolocations = await invoke<TrackGeolocation[]>("perception_track_geolocations", {
          trackSessionId: selection.trackSessionId,
          trackId: selection.trackId,
          limit: 8,
        });
        setTrackGeolocations(geolocations);
      } catch (reason) {
        setTrackActionError(messageFrom(reason));
      }
      setGeolocationPending(false);
    }
  }

  async function persistCountingRule(draft: CountingRuleDraft) {
    if (!droneId || !stream?.sourceId || countingRulePending) return;
    setCountingRulePending(true);
    setTrackActionError(undefined);
    try {
      const saved = await invoke<CountingRule>("upsert_perception_counting_rule", {
        input: {
          id: null,
          droneId,
          sourceId: stream.sourceId,
          label: draft.label,
          ruleType: draft.ruleType,
          points: draft.points,
          classIds: [],
          enabled: true,
          actor: "operator",
        },
      });
      setCountingRules((current) => [...current, saved]);
      setCountingRuleDraft(undefined);
    } catch (reason) {
      setTrackActionError(messageFrom(reason));
    } finally {
      setCountingRulePending(false);
    }
  }

  async function disableCountingRule(rule: CountingRule) {
    if (countingRulePending) return;
    setCountingRulePending(true);
    setTrackActionError(undefined);
    try {
      const updated = await invoke<CountingRule>("upsert_perception_counting_rule", {
        input: {
          id: rule.id,
          droneId: rule.droneId,
          sourceId: rule.sourceId,
          label: rule.label,
          ruleType: rule.ruleType,
          points: rule.points,
          classIds: rule.classIds,
          enabled: false,
          actor: "operator",
        },
      });
      setCountingRules((current) => current.map((candidate) => candidate.id === updated.id ? updated : candidate));
    } catch (reason) {
      setTrackActionError(messageFrom(reason));
    } finally {
      setCountingRulePending(false);
    }
  }

  function handleOverlayClick(event: ReactMouseEvent<HTMLCanvasElement>) {
    const bounds = event.currentTarget.getBoundingClientRect();
    if (bounds.width <= 0 || bounds.height <= 0) return;
    const point = {
      x: clamp((event.clientX - bounds.left) / bounds.width),
      y: clamp((event.clientY - bounds.top) / bounds.height),
    };
    const draft = countingRuleDraftRef.current;
    if (draft) {
      const next = { ...draft, points: [...draft.points, point] };
      setCountingRuleDraft(next);
      if (next.ruleType === "LINE" && next.points.length === 2) {
        void persistCountingRule(next);
      }
      return;
    }
    const visible = latestOverlayFrameRef.current?.detections
      .filter((detection) => detection.trackId && pointInsideBox(point, detection.boundingBox))
      .sort((left, right) => boxArea(left.boundingBox) - boxArea(right.boundingBox))[0];
    if (!visible) return;
    const source = perception?.sources.find((candidate) => candidate.sourceId === stream?.sourceId);
    const track = source?.tracks.find((candidate) =>
      candidate.trackId === visible.trackId &&
      candidate.trackSessionId === source.trackSession?.trackSessionId &&
      candidate.lifecycleState === "ACTIVE"
    );
    if (track) void selectTrack(track);
  }

  const sourceHealth = perception?.sources.find((source) => source.sourceId === stream?.sourceId)?.health;
  const trackingSource = perception?.sources.find((source) => source.sourceId === stream?.sourceId);
  const visibleTracks = trackingSource?.tracks
    .filter((track) => track.lifecycleState === "ACTIVE")
    .sort((left, right) => right.latestDetectionConfidence - left.latestDetectionConfidence) ?? [];
  const perceptionActive = sourceHealth?.inferenceReady === true;
  const perceptionOwnedElsewhere = perceptionActive && !perceptionRequested;

  useEffect(() => {
    if (!perceptionRequested) return;
    setFrameSubscriptionState(sourceHealth?.inferenceReady ? "active" : "waiting");
  }, [perceptionRequested, sourceHealth?.inferenceReady]);

  const playing = stream?.status === "playing";
  const telemetry = aircraft?.telemetry;
  const telemetryStale = Boolean(droneId) && (aircraft?.connectionStatus !== "connected" || telemetry?.status !== "live");
  const alertGlyph = highestAlert?.severity === "CRITICAL" ? "▲" : highestAlert ? "!" : "✓";
  const recordingSession = recording?.session;
  const recordingOwnedByAircraft = !recordingSession || recordingSession.droneId === droneId;
  const recordingActive = Boolean(recordingSession && ["REQUESTED", "RUNNING"].includes(recordingSession.status));
  const recordingLabel = evidenceRecordingLabel(recordingSession, recordingPlanned, recordingOwnedByAircraft);
  const followSupported = aircraft?.agentCapabilities?.includes("gimbal:track_follow") === true;
  const geolocationSupported = aircraft?.agentCapabilities?.includes("geolocation:selected_track_boresight") === true;
  const latestTrackGeolocation = trackGeolocations[0];
  const latestBoresightAlignment = geolocationAlignment(latestTrackGeolocation);
  const followContext = cameraFollowControlContext(recordingContext?.missionRunId, aircraft);
  const followRunning = Boolean(cameraFollow && ["starting", "following", "holding", "stopping"].includes(cameraFollow.state));
  const canStartFollow = followSupported
    && aircraft?.connectionStatus === "connected"
    && selection?.status === "SELECTED"
    && Boolean(stream?.sourceId)
    && Boolean(followContext);
  const followGuidance = !followSupported
    ? "The connected Agent does not advertise onboard track following."
    : !followContext
      ? "Requires an active mission, or disarmed on-ground inspection control."
      : selection?.status !== "SELECTED"
        ? "Select a visible confirmed track first."
        : "Atlas follows only this exact session-scoped track and never switches IDs automatically.";

  return (
    <section className={`live-video${compact ? " live-video--compact" : ""}${hudReduced ? " live-video--hud-reduced" : ""}`} aria-label="Live camera stream">
      <div className={`live-video__viewport${telemetryStale ? " live-video__viewport--telemetry-stale" : ""}`}>
        <canvas ref={videoCanvasRef} className="live-video__clean" aria-label="Clean RTSP video" />
        <canvas
          ref={overlayCanvasRef}
          className={`live-video__overlay${countingRuleDraft ? " live-video__overlay--drawing" : ""}`}
          aria-label={countingRuleDraft ? `Place ${countingRuleDraft.ruleType.toLowerCase()} count-rule points` : "Select a confirmed tracked object"}
          onClick={handleOverlayClick}
        />
        {!playing && (
          <div className="live-video__empty">
            <span className={stream?.status === "error" ? "live-video__signal live-video__signal--error" : "live-video__signal"} />
            <strong>{!droneId ? "No response aircraft assigned" : stream?.status === "error" ? "Video unavailable" : "Connecting to aircraft camera"}</strong>
            <p>{!droneId ? "Assign an aircraft to open its continuous clean video and optional perception controls." : error || "Atlas Native is opening the clean RTSP stream."}</p>
          </div>
        )}
        <div className="live-video__topline">
          <span className={playing ? "live-video__live live-video__live--active" : "live-video__live"}>{playing ? "LIVE" : stream?.status?.toUpperCase() || "IDLE"}</span>
          <span>{stream?.sourceId || "A8 MAIN"}</span>
          <span className="live-video__recording">REC · {recordingLabel}</span>
          <span>{stream ? `${stream.width}×${stream.height} · ${stream.targetFramesPerSecond} FPS` : "Waiting for decoder"}</span>
        </div>
        {telemetryStale && (
          <div className="live-video__stale-banner" role="status">
            <span aria-hidden="true">!</span>
            STALE TELEMETRY · VERIFY AIRCRAFT STATE
          </div>
        )}
        <div className="live-video__hud" aria-label="Aircraft flight HUD">
          <div className="live-video__hud-primary">
            <HudMetric label="Aircraft" value={telemetry?.armed == null ? "STATE UNKNOWN" : telemetry.armed ? "ARMED" : "DISARMED"} important={telemetry?.armed === true} />
            <HudMetric label="Flight" value={telemetry?.inAir == null ? "UNKNOWN" : telemetry.inAir ? "IN AIR" : "ON GROUND"} important={telemetry?.inAir === true} />
            <HudMetric label="Mode" value={telemetry?.flightMode || "UNKNOWN"} />
            <HudMetric label="Battery" value={formatHudMetric(telemetry?.batteryPercent, "%", 0)} />
            <HudMetric label="Rel altitude" value={formatHudMetric(telemetry?.relativeAltitudeM, " M", 1)} />
            <HudMetric label="Ground speed" value={formatHudMetric(telemetry?.groundSpeedMps, " M/S", 1)} />
            <HudMetric label="Heading" value={formatHudMetric(telemetry?.headingDeg, "°", 0)} />
            <HudMetric label="GPS" value={gpsLabel(aircraft)} />
            <HudMetric label="Link" value={linkLabel(aircraft)} important={telemetryStale} />
          </div>
          <div className={`live-video__safety-alert${highestAlert ? ` live-video__safety-alert--${highestAlert.severity.toLowerCase()}` : ""}`}>
            <span aria-hidden="true">{alertGlyph}</span>
            <div>
              <small>{highestAlert ? `${highestAlert.severity} SAFETY ALERT` : "SAFETY STATUS"}</small>
              <strong>{highestAlert?.title || "No active safety alerts"}</strong>
            </div>
          </div>
        </div>
        <div className="live-video__reticle" aria-hidden="true"><span /><span /></div>
      </div>

      <footer className="live-video__controls">
        <div className="live-video__mode" role="group" aria-label="Video display mode">
          <button type="button" className={!overlayEnabled ? "live-video__mode-active" : ""} onClick={() => setOverlayEnabled(false)}>Clean feed</button>
          <button type="button" className={overlayEnabled ? "live-video__mode-active" : ""} onClick={() => setOverlayEnabled(true)}>Detection overlay</button>
          <button
            type="button"
            className={perceptionRequested ? "live-video__mode-active" : ""}
            aria-pressed={perceptionRequested}
            disabled={!nativeAvailable || !droneId || perceptionOwnedElsewhere}
            onClick={() => setPerceptionRequested((current) => !current)}
          >
            {perceptionRequested ? "Stop perception" : perceptionOwnedElsewhere ? "Perception active" : "Start perception"}
          </button>
          <button type="button" className={hudReduced ? "live-video__mode-active" : ""} aria-pressed={hudReduced} onClick={() => setHudReduced((current) => !current)}>{hudReduced ? "Expand flight HUD" : "Reduce flight HUD"}</button>
        </div>
        <section className="track-operations" aria-label="Track counts and operator selection">
          <div className="track-operations__counts">
            <div className="track-operations__count-summary">
              <span><small>Visible now</small><strong>{counts?.currentVisibleCount ?? trackingSource?.trackSession?.currentVisibleCount ?? 0}</strong></span>
              <span><small>Session unique</small><strong>{counts?.uniqueSessionTracks ?? trackingSource?.trackSession?.uniqueConfirmedCount ?? 0}</strong></span>
              <span><small>Mission unique</small><strong>{counts?.missionRunId ? counts.uniqueMissionTracks : "—"}</strong></span>
            </div>
            <div className="track-operations__rule-totals" aria-label="Configured crossing counts">
              {countingRules.some((rule) => rule.enabled) ? countingRules.filter((rule) => rule.enabled).map((rule) => {
                const total = counts?.ruleCounts.find((candidate) => candidate.ruleId === rule.id && candidate.ruleRevision === rule.revision);
                return (
                  <span key={`${rule.id}:${rule.revision}`}>
                    <small>{rule.label}</small>
                    <strong>{rule.ruleType === "LINE" ? `${total?.lineForward ?? 0} → · ${total?.lineReverse ?? 0} ←` : `${total?.polygonEntries ?? 0} IN · ${total?.polygonExits ?? 0} OUT`}</strong>
                    <button type="button" disabled={countingRulePending} onClick={() => void disableCountingRule(rule)}>Disable</button>
                  </span>
                );
              }) : <p>No count boundaries configured for this source.</p>}
            </div>
            <div className="track-operations__rule-actions">
              {!countingRuleDraft ? (
                <>
                  <button type="button" onClick={() => { setOverlayEnabled(true); setCountingRuleDraft({ ruleType: "LINE", label: `Line ${countingRules.length + 1}`, points: [] }); }}>Draw count line</button>
                  <button type="button" onClick={() => { setOverlayEnabled(true); setCountingRuleDraft({ ruleType: "POLYGON", label: `Zone ${countingRules.length + 1}`, points: [] }); }}>Draw count zone</button>
                </>
              ) : (
                <div className="track-operations__rule-draft" role="status">
                  <label>
                    Rule label
                    <input
                      value={countingRuleDraft.label}
                      maxLength={120}
                      onChange={(event) => setCountingRuleDraft((current) => current ? { ...current, label: event.target.value } : current)}
                    />
                  </label>
                  <span>{countingRuleDraft.ruleType === "LINE" ? `${countingRuleDraft.points.length}/2 points` : `${countingRuleDraft.points.length} points · 3 minimum`}</span>
                  {countingRuleDraft.ruleType === "POLYGON" && (
                    <button type="button" disabled={countingRuleDraft.points.length < 3 || countingRulePending || !countingRuleDraft.label.trim()} onClick={() => void persistCountingRule(countingRuleDraft)}>Finish zone</button>
                  )}
                  <button type="button" className="track-operations__quiet-action" onClick={() => setCountingRuleDraft(undefined)}>Cancel</button>
                </div>
              )}
            </div>
          </div>
          <div className={`track-selection${selection ? ` track-selection--${selection.status.toLowerCase()}` : ""}`} aria-live="polite">
            {selection ? (
              <>
                <header className="track-selection__header">
                  <div>
                    <small>{selection.classLabel} · {shortTrackId(selection.trackId)}</small>
                    <strong>{selection.status === "SELECTED" ? "TRACK SELECTED" : selection.status}</strong>
                  </div>
                  <button type="button" onClick={() => void clearTrackSelection()} disabled={selectionPending}>Clear</button>
                </header>
                <div className="track-selection__facts">
                  <span><small>Lifecycle</small><strong>{selection.lifecycleState.replace(/_/g, " ")}</strong></span>
                  <span><small>Age</small><strong>{selection.ageFrames} frames</strong></span>
                  <span><small>Confidence</small><strong>{Math.round((selection.status === "OCCLUDED" ? selection.predictionConfidence : selection.confidence) * 100)}%</strong></span>
                  <span><small>Last observed</small><strong>{formatTrackTime(selection.lastObservedAtUnixMs)}</strong></span>
                </div>
                <div className={`track-follow track-follow--${cameraFollow?.state ?? "idle"}`}>
                  <div>
                    <small>Onboard gimbal control</small>
                    <strong>{cameraFollow ? cameraFollow.state.replace(/_/g, " ").toUpperCase() : "READY TO FOLLOW"}</strong>
                    <span>{cameraFollow?.message ?? followGuidance}</span>
                  </div>
                  {followRunning ? (
                    <button type="button" className="track-follow__stop" disabled={cameraFollow?.state === "stopping"} onClick={() => void stopCameraFollow()}>
                      {cameraFollow?.state === "stopping" ? "Stopping…" : "Stop camera follow"}
                    </button>
                  ) : (
                    <button type="button" disabled={!canStartFollow} title={canStartFollow ? undefined : followGuidance} onClick={() => void startCameraFollow()}>
                      {cameraFollow?.state === "error" ? "Retry camera follow" : "Follow with camera"}
                    </button>
                  )}
                </div>
                <div className="track-geolocation">
                  <header>
                    <div>
                      <small>Frame-aligned terrain intersection</small>
                      <strong>{latestTrackGeolocation ? latestTrackGeolocation.status : "NOT ESTIMATED"}</strong>
                    </div>
                    {latestTrackGeolocation?.status === "SUCCEEDED" && latestTrackGeolocation.latitude != null && latestTrackGeolocation.longitude != null && (
                      <span>
                        {latestTrackGeolocation.latitude.toFixed(6)}, {latestTrackGeolocation.longitude.toFixed(6)} · ±{latestTrackGeolocation.horizontalUncertaintyM?.toFixed(1) ?? "?"} m
                        {latestTrackGeolocation.refinementStatus !== "NOT_REQUESTED" && ` · terrain ${latestTrackGeolocation.refinementStatus.replace(/_/g, " ").toLowerCase()}`}
                      </span>
                    )}
                    {latestTrackGeolocation?.status === "REJECTED" && (
                      <span>{latestTrackGeolocation.rejectionCode.replace(/_/g, " ")} · {latestTrackGeolocation.rejectionReason}</span>
                    )}
                  </header>
                  <button
                    type="button"
                    disabled={!geolocationSupported || selection.status !== "SELECTED" || geolocationPending}
                    title={geolocationSupported ? "Atlas resolves the ground plane automatically and uses the latest centred selected-track observation." : "The connected Agent does not advertise selected-track geolocation."}
                    onClick={() => void geolocateSelectedTrack()}
                  >
                    {geolocationPending ? "Estimating…" : "Estimate coordinates"}
                  </button>
                  <p>
                    Atlas first bounds the observation ray with an aircraft-origin plane, then samples the configured DEM at the estimated target and iterates on that same ray. Observation time, terrain provenance, target-centre assumption, residual, and uncertainty remain attached to the result.
                    {latestTrackGeolocation?.terrainSource
                      ? ` Latest terrain: ${latestTrackGeolocation.terrainSource} · ${latestTrackGeolocation.terrainIterationCount} iteration${latestTrackGeolocation.terrainIterationCount === 1 ? "" : "s"} · ${latestTrackGeolocation.terrainResidualM?.toFixed(1) ?? "?"} m residual.`
                      : latestTrackGeolocation ? ` Initial plane: ${latestTrackGeolocation.groundAltitudeAmslM.toFixed(1)} m AMSL ±${latestTrackGeolocation.groundAltitudeUncertaintyM.toFixed(1)} m · ${latestTrackGeolocation.groundAltitudeSource}.` : ""}
                    {latestBoresightAlignment && ` Boresight alignment: ${latestBoresightAlignment}.`}
                  </p>
                </div>
                {selection.resultReason && <p className="track-selection__result">{selection.resultReason.replace(/_/g, " ")}</p>}
                <div className="track-selection__history" aria-label="Recent significant track samples">
                  {trackSamples.slice(0, 4).map((sample) => (
                    <span key={sample.revision}><small>r{sample.revision}</small>{sample.lifecycleState.replace(/_/g, " ")} · {Math.round(sample.detectionConfidence * 100)}%</span>
                  ))}
                </div>
                <form className="track-selection__annotation" onSubmit={(event) => { event.preventDefault(); void annotateTrack("NOTE"); }}>
                  <label>
                    Track note
                    <input value={trackNote} maxLength={2000} onChange={(event) => setTrackNote(event.target.value)} placeholder="Operator observation" />
                  </label>
                  <button type="submit" disabled={!trackNote.trim() || selectionPending}>Add note</button>
                  <button type="button" disabled={recordingSession?.status !== "RUNNING" || selectionPending} onClick={() => void annotateTrack("EVIDENCE_MARKER")}>
                    {evidenceAssetPending === "clip" ? "Queueing clip…" : "Mark + event clip"}
                  </button>
                </form>
              </>
            ) : (
              <>
                <header className="track-selection__header">
                  <div><small>Operator target</small><strong>NO TRACK SELECTED</strong></div>
                </header>
                <p className="track-selection__empty">Choose a confirmed box in the video or use the track list. Atlas will preserve only that exact session-scoped identity.</p>
                <div className="track-selection__available" aria-label="Visible confirmed tracks">
                  {visibleTracks.slice(0, 4).map((track) => (
                    <button key={`${track.trackSessionId}:${track.trackId}`} type="button" disabled={selectionPending} onClick={() => void selectTrack(track)}>
                      <span>{track.classLabel}</span><strong>{Math.round(track.latestDetectionConfidence * 100)}%</strong><small>{shortTrackId(track.trackId)}</small>
                    </button>
                  ))}
                  {!visibleTracks.length && <span>No confirmed visible tracks</span>}
                </div>
              </>
            )}
            {trackActionError && <p className="track-selection__error" role="alert">{trackActionError}</p>}
          </div>
        </section>
        <div className={`evidence-recorder evidence-recorder--${(recordingSession?.status ?? recording?.diskState ?? "idle").toLowerCase()}`} aria-live="polite">
          <div className="evidence-recorder__identity">
            <span aria-hidden="true">{recordingSession?.status === "RUNNING" ? "●" : recordingSession?.status === "FAILED" || recording?.diskState === "STOP" ? "!" : "○"}</span>
            <div>
              <small>Atlas local evidence · source RTSP</small>
              <strong>{recordingLabel}</strong>
            </div>
          </div>
          <div className="evidence-recorder__facts">
            <span><small>Verified</small><strong>{recordingSession?.finalizedSegmentCount ?? 0} segments</strong></span>
            <span><small>Evidence</small><strong>{formatBytes(recordingSession?.totalBytes)}</strong></span>
            <span><small>Disk</small><strong>{recording?.availableBytes == null ? "UNKNOWN" : `${formatBytes(recording.availableBytes)} · ${recording.diskState}`}</strong></span>
            <span><small>Gaps</small><strong>{recordingSession?.gaps.length ?? 0}</strong></span>
          </div>
          <div className="evidence-recorder__actions">
            <button type="button" onClick={() => void captureStill()} disabled={!nativeAvailable || !droneId || !playing || evidenceAssetPending != null}>
              {evidenceAssetPending === "still" ? "Saving still…" : selection ? "Capture track still" : "Capture still"}
            </button>
            <button type="button" onClick={() => void startRecording()} disabled={!nativeAvailable || !droneId || recordingActive || recordingPending != null || recording?.diskState === "STOP"}>
              {recordingPending === "start" ? "Requesting…" : "Start evidence"}
            </button>
            <button type="button" className="evidence-recorder__stop" onClick={() => void stopRecording()} disabled={!recordingActive || !recordingOwnedByAircraft || recordingPending != null}>
              {recordingPending === "stop" ? "Finalizing…" : "Stop + verify"}
            </button>
          </div>
          {(recordingError || recordingSession?.errorMessage || !recordingOwnedByAircraft) && (
            <p className="evidence-recorder__message" role="status">
              {!recordingOwnedByAircraft ? `Source reserved by ${recordingSession?.droneId}. Open that aircraft context to stop it.` : recordingError || recordingSession?.errorMessage}
            </p>
          )}
          {evidenceAssetMessage && <p className="evidence-recorder__message evidence-recorder__message--success" role="status">{evidenceAssetMessage}</p>}
        </div>
        <div className="live-video__metrics" aria-live="polite">
          <VideoMetric label="Provider" value={perception?.provider?.toUpperCase() || "OFFLINE"} />
          <VideoMetric label="Accelerator" value={sourceHealth?.accelerator || "—"} />
          <VideoMetric label="Inference" value={sourceHealth?.inferenceReady ? `${sourceHealth.inferenceFps.toFixed(1)} FPS` : sourceHealth?.activationState || "INACTIVE"} />
          <VideoMetric label="Perception" value={perceptionOwnedElsewhere ? "MISSION / SHARED" : frameSubscriptionLabel(frameSubscriptionState)} />
          <VideoMetric label="Tracking" value={trackingLabel(sourceHealth?.tracking)} />
          <VideoMetric label="Detections" value={overlayEnabled ? String(detectionCount) : "HIDDEN"} />
          <VideoMetric label="Alignment" value={alignmentDeltaMs == null ? "NO MATCH" : `${signed(alignmentDeltaMs)} MS`} />
          <VideoMetric label="Playout" value={stream ? `${stream.playoutDelayMs} MS` : "—"} />
        </div>
      </footer>
    </section>
  );
}

function HudMetric({ label, value, important = false }: { label: string; value: string; important?: boolean }) {
  return <span className={important ? "live-video__hud-metric live-video__hud-metric--important" : "live-video__hud-metric"}><small>{label}</small><strong>{value}</strong></span>;
}

function trackingLabel(tracking?: TrackingHealth) {
  if (!tracking) return "UNKNOWN";
  if (tracking.algorithm === "DISABLED") return "DISABLED";
  const cmc = tracking.cameraMotionState === "ACTIVE" ? "CMC" : `CMC ${tracking.cameraMotionState}`;
  return `${tracking.algorithm.replace("_", "-")} · ${tracking.state} · ${cmc}`;
}

function formatHudMetric(value: number | null | undefined, suffix: string, decimals: number) {
  return value == null || !Number.isFinite(value) ? "UNKNOWN" : `${value.toFixed(decimals)}${suffix}`;
}

function gpsLabel(aircraft?: FleetAircraft) {
  const telemetry = aircraft?.telemetry;
  if (aircraft?.connectionStatus !== "connected" || telemetry?.status !== "live") return "STALE";
  if (telemetry.health?.globalPositionOk === false) return "NO GLOBAL FIX";
  const fix = telemetry.gpsFix?.replace(/_/g, " ") || (telemetry.health?.globalPositionOk ? "POSITION OK" : "UNKNOWN");
  return telemetry.satellitesVisible == null ? fix : `${fix} · ${telemetry.satellitesVisible} SAT`;
}

function linkLabel(aircraft?: FleetAircraft) {
  if (!aircraft) return "NO AIRCRAFT";
  if (aircraft.connectionStatus !== "connected") return aircraft.connectionStatus.toUpperCase();
  if (!aircraft.lastHeartbeatAtUnixMs) return "AGE UNKNOWN";
  const ageSeconds = Math.max(0, Math.round((Date.now() - aircraft.lastHeartbeatAtUnixMs) / 1_000));
  return `${ageSeconds} S AGO`;
}

function VideoMetric({ label, value }: { label: string; value: string }) {
  return <span><small>{label}</small><strong>{value}</strong></span>;
}

function evidenceRecordingLabel(session: EvidenceRecordingSession | undefined, planned: boolean, ownedByAircraft: boolean) {
  if (session && !ownedByAircraft && ["REQUESTED", "RUNNING"].includes(session.status)) return "BUSY · OTHER AIRCRAFT";
  if (session?.status === "REQUESTED") return "REQUESTED · WAITING FOR SOURCE BYTES";
  if (session?.status === "RUNNING") return `RUNNING · ${session.finalizedSegmentCount} VERIFIED`;
  if (session?.status === "SUCCEEDED") return `STOPPED · ${session.finalizedSegmentCount} VERIFIED`;
  if (session?.status === "FAILED") return `FAILED · ${session.gaps.length} ${session.gaps.length === 1 ? "GAP" : "GAPS"}`;
  return planned ? "PLANNED · READY TO START" : "NOT REQUESTED";
}

function formatBytes(value: number | undefined) {
  if (value == null || !Number.isFinite(value)) return "0 B";
  if (value >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(1)} GB`;
  if (value >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${Math.max(0, Math.round(value))} B`;
}

export function parseVideoFramePacket(payload: ArrayBuffer | Uint8Array): ParsedVideoFrame {
  const bytes = payload instanceof Uint8Array ? payload : new Uint8Array(payload);
  if (bytes.byteLength < 8 || packetMagic.some((value, index) => bytes[index] !== value)) {
    throw new Error("Atlas received an invalid native video frame packet.");
  }
  const headerLength = new DataView(bytes.buffer, bytes.byteOffset + 4, 4).getUint32(0, true);
  const jpegOffset = 8 + headerLength;
  if (headerLength === 0 || jpegOffset >= bytes.byteLength) {
    throw new Error("Atlas received a truncated native video frame packet.");
  }
  const header = JSON.parse(new TextDecoder().decode(bytes.subarray(8, jpegOffset))) as VideoFrameHeader;
  if (!Number.isSafeInteger(header.sequence) || header.sequence <= 0 || header.width <= 0 || header.height <= 0) {
    throw new Error("Atlas received invalid native video frame metadata.");
  }
  return {
    header,
    jpeg: new Blob([bytes.slice(jpegOffset)], { type: header.mimeType || "image/jpeg" }),
  };
}

async function renderFrame(
  videoCanvas: HTMLCanvasElement | null,
  overlayCanvas: HTMLCanvasElement | null,
  frame: ParsedVideoFrame,
  drawOverlay: boolean,
  selection?: TrackSelection,
  countingRules: CountingRule[] = [],
  countingRuleDraft?: CountingRuleDraft,
) {
  if (!videoCanvas || !overlayCanvas) return;
  const bitmap = await createImageBitmap(frame.jpeg);
  try {
    if (videoCanvas.width !== frame.header.width || videoCanvas.height !== frame.header.height) {
      videoCanvas.width = frame.header.width;
      videoCanvas.height = frame.header.height;
      overlayCanvas.width = frame.header.width;
      overlayCanvas.height = frame.header.height;
    }
    const videoContext = videoCanvas.getContext("2d", { alpha: false });
    const overlayContext = overlayCanvas.getContext("2d");
    if (!videoContext || !overlayContext) throw new Error("Atlas could not create the native video canvas.");
    videoContext.drawImage(bitmap, 0, 0, videoCanvas.width, videoCanvas.height);
    overlayContext.clearRect(0, 0, overlayCanvas.width, overlayCanvas.height);
    if (drawOverlay && frame.header.overlay) {
      drawDetections(overlayContext, frame.header.overlay.frame.detections, overlayCanvas.width, overlayCanvas.height, selection);
      drawCountingRules(overlayContext, countingRules, countingRuleDraft, overlayCanvas.width, overlayCanvas.height);
    }
  } finally {
    bitmap.close();
  }
}

function drawDetections(context: CanvasRenderingContext2D, detections: PerceptionDetection[], width: number, height: number, selection?: TrackSelection) {
  const lineWidth = Math.max(2, Math.round(height / 360));
  const fontSize = Math.max(12, Math.round(height / 45));
  context.lineWidth = lineWidth;
  context.font = `700 ${fontSize}px ui-monospace, SFMono-Regular, Menlo, monospace`;
  context.textBaseline = "top";
  for (const detection of detections) {
    const box = detection.boundingBox;
    const x = clamp(box.x) * width;
    const y = clamp(box.y) * height;
    const boxWidth = Math.max(0, Math.min(1 - clamp(box.x), box.width)) * width;
    const boxHeight = Math.max(0, Math.min(1 - clamp(box.y), box.height)) * height;
    if (boxWidth < 1 || boxHeight < 1) continue;
    const selected = selection?.trackId === detection.trackId && selection.status === "SELECTED";
    const colour = selected ? "#f2c455" : detectionColour(detection.classId);
    context.strokeStyle = colour;
    context.lineWidth = selected ? lineWidth * 2 : lineWidth;
    context.strokeRect(x, y, boxWidth, boxHeight);
    const label = `${selected ? "SELECTED · " : ""}${detection.classLabel.toUpperCase()} ${Math.round(detection.confidence * 100)}%${detection.trackId ? ` · ${shortTrackId(detection.trackId)}` : ""}`;
    const textWidth = context.measureText(label).width;
    const labelHeight = fontSize + 8;
    const labelY = y >= labelHeight ? y - labelHeight : y;
    context.fillStyle = "rgba(8, 15, 18, .86)";
    context.fillRect(x, labelY, textWidth + 12, labelHeight);
    context.fillStyle = colour;
    context.fillText(label, x + 6, labelY + 4);
  }
}

function drawCountingRules(
  context: CanvasRenderingContext2D,
  rules: CountingRule[],
  draft: CountingRuleDraft | undefined,
  width: number,
  height: number,
) {
  context.save();
  context.lineWidth = Math.max(2, Math.round(height / 420));
  context.font = `700 ${Math.max(11, Math.round(height / 55))}px ui-monospace, SFMono-Regular, Menlo, monospace`;
  for (const rule of rules.filter((candidate) => candidate.enabled)) {
    drawCountingGeometry(context, rule.points, rule.ruleType, rule.label, width, height, "rgba(242, 196, 85, .88)");
  }
  if (draft) {
    drawCountingGeometry(context, draft.points, draft.ruleType, `${draft.label} · DRAWING`, width, height, "rgba(235, 240, 231, .95)");
  }
  context.restore();
}

function drawCountingGeometry(
  context: CanvasRenderingContext2D,
  points: CountingPoint[],
  ruleType: "LINE" | "POLYGON",
  label: string,
  width: number,
  height: number,
  colour: string,
) {
  if (!points.length) return;
  context.beginPath();
  context.moveTo(points[0].x * width, points[0].y * height);
  for (const point of points.slice(1)) context.lineTo(point.x * width, point.y * height);
  if (ruleType === "POLYGON" && points.length >= 3) context.closePath();
  context.strokeStyle = colour;
  context.setLineDash(ruleType === "POLYGON" ? [10, 7] : []);
  context.stroke();
  context.setLineDash([]);
  for (const point of points) {
    context.fillStyle = colour;
    context.fillRect(point.x * width - 3, point.y * height - 3, 6, 6);
  }
  context.fillStyle = "rgba(8, 15, 18, .84)";
  const textWidth = context.measureText(label.toUpperCase()).width;
  const labelX = points[0].x * width;
  const labelY = Math.max(0, points[0].y * height - 22);
  context.fillRect(labelX, labelY, textWidth + 12, 19);
  context.fillStyle = colour;
  context.fillText(label.toUpperCase(), labelX + 6, labelY + 3);
}

function detectionColour(classId: number) {
  const colours = ["#78abc1", "#e0ae45", "#65a576", "#cf6a78", "#9b80aa", "#d17a4c"];
  return colours[Math.abs(classId) % colours.length];
}

function pointInsideBox(point: CountingPoint, box: BoundingBox) {
  return point.x >= box.x && point.x <= box.x + box.width && point.y >= box.y && point.y <= box.y + box.height;
}

function boxArea(box: BoundingBox) { return Math.max(0, box.width) * Math.max(0, box.height); }
function shortTrackId(trackId: string) {
  const parts = trackId.split(":");
  return parts.length ? `#${parts[parts.length - 1]}` : trackId;
}
function formatTrackTime(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "UNKNOWN";
  const age = Math.max(0, Date.now() - value);
  return age < 1_000 ? "NOW" : `${Math.round(age / 1_000)} S AGO`;
}

function cameraFollowControlContext(missionRunId: string | undefined, aircraft: FleetAircraft | undefined): CameraFollowState["controlContext"] | undefined {
  if (missionRunId) return { kind: "mission_override", missionRunId };
  if (aircraft?.telemetry?.status === "live" && aircraft.telemetry.armed === false && aircraft.telemetry.inAir === false) {
    return { kind: "inspection" };
  }
  return undefined;
}

function followCommandIdentity(follow: CameraFollowState, gimbalId = 0) {
  return {
    controlContext: follow.controlContext,
    controlSessionId: follow.controlSessionId,
    gimbalId,
    cameraComponentId: 0,
  };
}

async function endCameraFollowCommands(droneId: string, follow: CameraFollowState) {
  let stopError: unknown;
  try {
    await dispatchVehicleCommand(droneId, "gimbal_follow_stop", followCommandIdentity(follow));
  } catch (reason) {
    stopError = reason;
  }
  try {
    await dispatchVehicleCommand(droneId, "payload_control_end", followCommandIdentity(follow));
  } catch (reason) {
    if (!stopError) stopError = reason;
  }
  if (stopError) throw stopError;
}

async function dispatchVehicleCommand(droneId: string, commandType: string, parameters: Record<string, unknown>) {
  const initial = await invoke<CommandReceipt>("request_vehicle_command", {
    droneId,
    commandType,
    parametersJson: JSON.stringify(parameters),
    timeoutMs: 15_000,
  });
  let current = initial;
  while (!terminalCommandStates.has(current.status) && Date.now() <= initial.deadlineAtUnixMs + 1_500) {
    await wait(200);
    current = await invoke<CommandReceipt>("vehicle_command_detail", { commandId: initial.id });
  }
  if (!terminalCommandStates.has(current.status)) {
    throw new Error(`${commandType.replace(/_/g, " ")} command did not reach a terminal state before its deadline.`);
  }
  if (current.status !== "succeeded") {
    throw new Error(current.resultMessage || current.resultCode || `${commandType.replace(/_/g, " ")} command ${current.status}`);
  }
  return current;
}

type AutomaticGroundAltitude = {
  altitudeAmslMeters: number;
  uncertaintyMeters: number;
  source: string;
  sourceVersion: string;
  resolvedAtUnixMs: number;
};

async function resolveAutomaticGroundAltitude(aircraft?: FleetAircraft): Promise<AutomaticGroundAltitude> {
  const telemetry = aircraft?.telemetry;
  if (!telemetry || telemetry.status !== "live" || Date.now() - telemetry.receivedAtUnixMs > 10_000) {
    throw new Error("Automatic ground altitude requires fresh aircraft telemetry.");
  }

  const latitude = telemetry.latitude;
  const longitude = telemetry.longitude;
  let terrainFailure = "aircraft position is unavailable";
  if (finiteCoordinate(latitude, -90, 90) && finiteCoordinate(longitude, -180, 180)) {
    const source = terrainSource();
    try {
      const altitudeAmslMeters = await promiseWithTimeout(
        sampleTerrainElevation(latitude, longitude, source),
        mvpTerrainLookupTimeoutMs,
        "terrain lookup timed out",
      );
      return {
        altitudeAmslMeters,
        uncertaintyMeters: source.verticalUncertaintyMeters,
        source: `Automatic DEM · ${source.displayName}`.slice(0, 240),
        sourceVersion: `aircraft-origin-plane-v1:${terrainSourceVersion(source)}`.slice(0, 240),
        resolvedAtUnixMs: Date.now(),
      };
    } catch (reason) {
      terrainFailure = messageFrom(reason);
    }
  }

  const absoluteAltitude = telemetry.absoluteAltitudeM;
  const relativeAltitude = telemetry.relativeAltitudeM;
  if (finiteCoordinate(absoluteAltitude, -500, 20_000) && finiteCoordinate(relativeAltitude, -1_000, 20_000)) {
    const altitudeAmslMeters = absoluteAltitude - relativeAltitude;
    if (altitudeAmslMeters >= -500 && altitudeAmslMeters <= 9_000) {
      return {
        altitudeAmslMeters,
        uncertaintyMeters: mvpHomePlaneUncertaintyMeters,
        source: "Automatic autopilot home-altitude plane",
        sourceVersion: "absolute-minus-relative-altitude-v1",
        resolvedAtUnixMs: telemetry.receivedAtUnixMs,
      };
    }
  }
  throw new Error(`Automatic ground altitude is unavailable. DEM lookup failed (${terrainFailure}) and aircraft absolute/relative altitude cannot provide the MVP fallback.`);
}

type TerrainRay = {
  originLatitude: number;
  originLongitude: number;
  originAltitudeM: number;
  north: number;
  east: number;
  down: number;
};

async function refineTrackGeolocation(initial: TrackGeolocation): Promise<TrackGeolocation> {
  if (!finiteCoordinate(initial.latitude, -90, 90) || !finiteCoordinate(initial.longitude, -180, 180)) {
    throw new Error("The initial geolocation has no valid coordinate to sample.");
  }
  const evidence = asRecord(initial.evidence);
  const estimate = asRecord(evidence?.estimate);
  const origin = asRecord(estimate?.origin);
  const direction = asRecord(estimate?.worldDirectionNed);
  const ray: TerrainRay = {
    originLatitude: requiredFinite(origin?.latitudeDeg, "origin latitude"),
    originLongitude: requiredFinite(origin?.longitudeDeg, "origin longitude"),
    originAltitudeM: requiredFinite(origin?.altitudeMeters, "origin altitude"),
    north: requiredFinite(direction?.x, "ray north component"),
    east: requiredFinite(direction?.y, "ray east component"),
    down: requiredFinite(direction?.z, "ray down component"),
  };
  const norm = Math.hypot(ray.north, ray.east, ray.down);
  if (norm < 0.9 || norm > 1.1 || ray.down <= 0) {
    throw new Error("Agent evidence does not contain a valid downward world-space observation ray.");
  }

  const source = terrainSource();
  const samples: Array<{ latitude: number; longitude: number; altitudeAmslM: number }> = [];
  let coordinate = { latitude: initial.latitude, longitude: initial.longitude };
  for (let iteration = 0; iteration < terrainRefinementMaximumIterations; iteration += 1) {
    const altitudeAmslM = await promiseWithTimeout(
      sampleTerrainElevation(coordinate.latitude, coordinate.longitude, source),
      mvpTerrainLookupTimeoutMs,
      `target terrain lookup ${iteration + 1} timed out`,
    );
    samples.push({ ...coordinate, altitudeAmslM });
    const next = terrainRayIntersection(ray, altitudeAmslM + initial.assumedAimPointHeightM);
    const residual = horizontalDistanceMeters(coordinate, next);
    coordinate = next;
    if (residual <= terrainRefinementConvergenceMeters) break;
  }
  return invoke<TrackGeolocation>("refine_perception_track_geolocation", {
    input: {
      geolocationId: initial.id,
      terrainSource: `Automatic target-area DEM · ${source.displayName}`.slice(0, 240),
      terrainSourceVersion: `target-ray-iterative-v1:${terrainSourceVersion(source)}`.slice(0, 240),
      terrainVerticalUncertaintyM: source.verticalUncertaintyMeters,
      convergenceThresholdM: terrainRefinementConvergenceMeters,
      samples,
    },
  });
}

function terrainRayIntersection(ray: TerrainRay, altitudeM: number) {
  const verticalDrop = ray.originAltitudeM - altitudeM;
  if (!Number.isFinite(verticalDrop) || verticalDrop <= 0) {
    throw new Error("Target terrain is not below the observation origin.");
  }
  const slantRangeM = verticalDrop / ray.down;
  const groundRangeM = slantRangeM * Math.hypot(ray.north, ray.east);
  if (!Number.isFinite(slantRangeM) || slantRangeM <= 0 || groundRangeM > 3_000) {
    throw new Error("Target terrain intersection exceeds the bounded 3 km ground range.");
  }
  return offsetWgs84(
    ray.originLatitude,
    ray.originLongitude,
    altitudeM,
    slantRangeM * ray.north,
    slantRangeM * ray.east,
  );
}

function offsetWgs84(latitudeDeg: number, longitudeDeg: number, altitudeM: number, northM: number, eastM: number) {
  const semiMajorM = 6_378_137;
  const eccentricitySquared = 6.69437999014e-3;
  const latitude = latitudeDeg * Math.PI / 180;
  const sinLatitude = Math.sin(latitude);
  const denominator = Math.sqrt(1 - eccentricitySquared * sinLatitude * sinLatitude);
  const primeVerticalRadius = semiMajorM / denominator;
  const meridianRadius = semiMajorM * (1 - eccentricitySquared) / denominator ** 3;
  const nextLatitude = latitude + northM / (meridianRadius + altitudeM);
  const longitudeScale = (primeVerticalRadius + altitudeM) * Math.cos(latitude);
  if (Math.abs(longitudeScale) < 1e-6) throw new Error("Terrain intersection is undefined at the pole.");
  return {
    latitude: nextLatitude * 180 / Math.PI,
    longitude: (longitudeDeg * Math.PI / 180 + eastM / longitudeScale) * 180 / Math.PI,
  };
}

function horizontalDistanceMeters(
  left: { latitude: number; longitude: number },
  right: { latitude: number; longitude: number },
) {
  const earthRadiusM = 6_378_137;
  const meanLatitude = (left.latitude + right.latitude) * 0.5 * Math.PI / 180;
  const northM = (right.latitude - left.latitude) * Math.PI / 180 * earthRadiusM;
  const eastM = (right.longitude - left.longitude) * Math.PI / 180 * earthRadiusM * Math.cos(meanLatitude);
  return Math.hypot(northM, eastM);
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

function geolocationAlignment(geolocation?: TrackGeolocation) {
  const evidence = asRecord(geolocation?.evidence);
  const estimate = asRecord(evidence?.estimate);
  const alignment = asRecord(estimate?.boresightAlignment);
  const status = typeof alignment?.status === "string" ? alignment.status : undefined;
  if (!status) return undefined;
  const bound = Number(alignment?.errorBoundDeg);
  const reference = typeof alignment?.reference === "string" ? alignment.reference : "";
  return `${status}${Number.isFinite(bound) ? ` · ±${bound.toFixed(1)}°` : ""}${reference ? ` · ${reference}` : " · field verification required"}`;
}

function requiredFinite(value: unknown, label: string) {
  const number = Number(value);
  if (!Number.isFinite(number)) throw new Error(`Agent evidence is missing ${label}.`);
  return number;
}

function finiteCoordinate(value: number | null | undefined, minimum: number, maximum: number): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= minimum && value <= maximum;
}

async function promiseWithTimeout<T>(pending: Promise<T>, timeoutMs: number, timeoutMessage: string): Promise<T> {
  let timeout: number | undefined;
  try {
    return await Promise.race([
      pending,
      new Promise<T>((_, reject) => {
        timeout = window.setTimeout(() => reject(new Error(timeoutMessage)), timeoutMs);
      }),
    ]);
  } finally {
    if (timeout !== undefined) window.clearTimeout(timeout);
  }
}

function capabilityNumber(capabilities: string[], pattern: RegExp) {
  for (const capability of capabilities) {
    const match = pattern.exec(capability);
    if (match) return Number(match[1]);
  }
  return 0;
}

function clamp(value: number) { return Math.max(0, Math.min(1, Number.isFinite(value) ? value : 0)); }
function signed(value: number) { return value > 0 ? `+${value}` : String(value); }
function frameSubscriptionLabel(state: FrameSubscriptionState) {
  if (state === "active") return "LEASED";
  if (state === "waiting") return "WAITING";
  if (state === "requesting") return "REQUESTING";
  return "OFF";
}
function wait(milliseconds: number) { return new Promise((resolve) => window.setTimeout(resolve, milliseconds)); }
function messageFrom(reason: unknown) { return reason instanceof Error ? reason.message : String(reason); }
