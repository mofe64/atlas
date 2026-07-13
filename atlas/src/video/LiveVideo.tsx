import { useEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import "./LiveVideo.css";

type LiveVideoProps = {
  nativeAvailable: boolean;
  droneId?: string;
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
    health?: {
      accelerator: string;
      inferenceReady: boolean;
      inferenceFps: number;
      droppedFrames: number;
      lastError: string;
    };
  }>;
};

type ParsedVideoFrame = { header: VideoFrameHeader; jpeg: Blob };

const packetMagic = [0x41, 0x54, 0x56, 0x31];

export function LiveVideo({ nativeAvailable, droneId }: LiveVideoProps) {
  const videoCanvasRef = useRef<HTMLCanvasElement>(null);
  const overlayCanvasRef = useRef<HTMLCanvasElement>(null);
  const overlayEnabledRef = useRef(true);
  const [overlayEnabled, setOverlayEnabled] = useState(true);
  const [stream, setStream] = useState<VideoStreamSnapshot>();
  const [perception, setPerception] = useState<PerceptionSnapshot>();
  const [detectionCount, setDetectionCount] = useState(0);
  const [alignmentDeltaMs, setAlignmentDeltaMs] = useState<number>();
  const [error, setError] = useState<string>();

  useEffect(() => {
    overlayEnabledRef.current = overlayEnabled;
    if (!overlayEnabled) {
      const overlay = overlayCanvasRef.current;
      overlay?.getContext("2d")?.clearRect(0, 0, overlay.width, overlay.height);
    }
  }, [overlayEnabled]);

  useEffect(() => {
    if (!nativeAvailable || !droneId) {
      setStream(undefined);
      setPerception(undefined);
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
          await renderFrame(videoCanvasRef.current, overlayCanvasRef.current, frame, overlayEnabledRef.current);
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
        const [nextStream, nextPerception] = await Promise.all([
          invoke<VideoStreamSnapshot>("video_stream_snapshot"),
          invoke<PerceptionSnapshot | null>("perception_snapshot", { droneId }),
        ]);
        if (!active) return;
        setStream(nextStream);
        setPerception(nextPerception ?? undefined);
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

  const sourceHealth = perception?.sources.find((source) => source.sourceId === stream?.sourceId)?.health;
  const playing = stream?.status === "playing";

  return (
    <section className="live-video" aria-label="Live camera stream">
      <div className="live-video__viewport">
        <canvas ref={videoCanvasRef} className="live-video__clean" aria-label="Clean RTSP video" />
        <canvas ref={overlayCanvasRef} className="live-video__overlay" aria-label="Object-detection overlay" />
        {!playing && (
          <div className="live-video__empty">
            <span className={stream?.status === "error" ? "live-video__signal live-video__signal--error" : "live-video__signal"} />
            <strong>{stream?.status === "error" ? "Video unavailable" : "Connecting to aircraft camera"}</strong>
            <p>{error || "Atlas Native is opening the clean RTSP stream."}</p>
          </div>
        )}
        <div className="live-video__topline">
          <span className={playing ? "live-video__live live-video__live--active" : "live-video__live"}>{playing ? "LIVE" : stream?.status?.toUpperCase() || "IDLE"}</span>
          <span>{stream?.sourceId || "A8 MAIN"}</span>
          <span>{stream ? `${stream.width}×${stream.height} · ${stream.targetFramesPerSecond} FPS` : "Waiting for decoder"}</span>
        </div>
        <div className="live-video__reticle" aria-hidden="true"><span /><span /></div>
      </div>

      <footer className="live-video__controls">
        <div className="live-video__mode" role="group" aria-label="Video display mode">
          <button type="button" className={!overlayEnabled ? "live-video__mode-active" : ""} onClick={() => setOverlayEnabled(false)}>Clean feed</button>
          <button type="button" className={overlayEnabled ? "live-video__mode-active" : ""} onClick={() => setOverlayEnabled(true)}>Detection overlay</button>
        </div>
        <div className="live-video__metrics" aria-live="polite">
          <VideoMetric label="Provider" value={perception?.provider?.toUpperCase() || "OFFLINE"} />
          <VideoMetric label="Accelerator" value={sourceHealth?.accelerator || "—"} />
          <VideoMetric label="Inference" value={sourceHealth?.inferenceReady ? `${sourceHealth.inferenceFps.toFixed(1)} FPS` : "NOT READY"} />
          <VideoMetric label="Detections" value={overlayEnabled ? String(detectionCount) : "HIDDEN"} />
          <VideoMetric label="Alignment" value={alignmentDeltaMs == null ? "NO MATCH" : `${signed(alignmentDeltaMs)} MS`} />
          <VideoMetric label="Playout" value={stream ? `${stream.playoutDelayMs} MS` : "—"} />
        </div>
      </footer>
    </section>
  );
}

function VideoMetric({ label, value }: { label: string; value: string }) {
  return <span><small>{label}</small><strong>{value}</strong></span>;
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

async function renderFrame(videoCanvas: HTMLCanvasElement | null, overlayCanvas: HTMLCanvasElement | null, frame: ParsedVideoFrame, drawOverlay: boolean) {
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
      drawDetections(overlayContext, frame.header.overlay.frame.detections, overlayCanvas.width, overlayCanvas.height);
    }
  } finally {
    bitmap.close();
  }
}

function drawDetections(context: CanvasRenderingContext2D, detections: PerceptionDetection[], width: number, height: number) {
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
    const colour = detectionColour(detection.classId);
    context.strokeStyle = colour;
    context.strokeRect(x, y, boxWidth, boxHeight);
    const label = `${detection.classLabel.toUpperCase()} ${Math.round(detection.confidence * 100)}%${detection.trackId ? ` · ${detection.trackId}` : ""}`;
    const textWidth = context.measureText(label).width;
    const labelHeight = fontSize + 8;
    const labelY = y >= labelHeight ? y - labelHeight : y;
    context.fillStyle = "rgba(8, 15, 18, .86)";
    context.fillRect(x, labelY, textWidth + 12, labelHeight);
    context.fillStyle = colour;
    context.fillText(label, x + 6, labelY + 4);
  }
}

function detectionColour(classId: number) {
  const colours = ["#78abc1", "#e0ae45", "#65a576", "#cf6a78", "#9b80aa", "#d17a4c"];
  return colours[Math.abs(classId) % colours.length];
}

function clamp(value: number) { return Math.max(0, Math.min(1, Number.isFinite(value) ? value : 0)); }
function signed(value: number) { return value > 0 ? `+${value}` : String(value); }
function wait(milliseconds: number) { return new Promise((resolve) => window.setTimeout(resolve, milliseconds)); }
function messageFrom(reason: unknown) { return reason instanceof Error ? reason.message : String(reason); }
