#!/usr/bin/env python3
"""HailoRT/TAPPAS object-detection adapter for Atlas Agent.

The adapter consumes the clean A8 RTSP stream, runs Hailo inference, and sends
only normalized metadata to Atlas Agent's protected Unix socket. It never draws
on or republishes the video; the native Atlas renderer owns the optional overlay.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import queue
import signal
import socket
import sys
import threading
import time
import uuid
from collections import deque
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


RUNTIME_PROTOCOL_VERSION = "1"
DEFAULT_POSTPROCESS_SO = "/usr/lib/aarch64-linux-gnu/hailo/tappas/post_processes/libyolo_hailortpp_post.so"


def environment(name: str, fallback: str = "") -> str:
    value = os.environ.get(name, fallback).strip()
    # atlas-setup emits a systemd-compatible EnvironmentFile with double-quoted
    # values. Docker's --env-file keeps those quotes, so decode the same escaped
    # string format before validating paths and adapter options.
    if len(value) >= 2 and value.startswith('"') and value.endswith('"'):
        try:
            decoded = json.loads(value)
        except json.JSONDecodeError:
            return value
        if isinstance(decoded, str):
            return decoded.strip()
    return value


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def bounded_float(value: Any, minimum: float, maximum: float, fallback: float = 0.0) -> float:
    try:
        parsed = float(value)
    except (TypeError, ValueError):
        return fallback
    if not math.isfinite(parsed):
        return fallback
    return max(minimum, min(maximum, parsed))


def optional_call(value: Any, name: str, fallback: Any = None) -> Any:
    method = getattr(value, name, None)
    if not callable(method):
        return fallback
    try:
        return method()
    except Exception:
        return fallback


def detection_payload(detection: Any, hailo_module: Any) -> dict[str, Any]:
    class_id = int(optional_call(detection, "get_class_id", -1))
    label = str(optional_call(detection, "get_label", "") or f"class_{class_id}").strip()
    confidence = bounded_float(optional_call(detection, "get_confidence", 0.0), 0.0, 1.0)
    box = optional_call(detection, "get_bbox")
    x = bounded_float(optional_call(box, "xmin", 0.0), 0.0, 1.0)
    y = bounded_float(optional_call(box, "ymin", 0.0), 0.0, 1.0)
    width = bounded_float(optional_call(box, "width", 0.0), 0.0, 1.0 - x)
    height = bounded_float(optional_call(box, "height", 0.0), 0.0, 1.0 - y)
    track_id = ""
    unique_type = getattr(hailo_module, "HAILO_UNIQUE_ID", None)
    if unique_type is not None:
        unique_ids = optional_call(detection, "get_objects_typed", [])
        try:
            unique_ids = detection.get_objects_typed(unique_type)
        except Exception:
            pass
        if unique_ids:
            identifier = optional_call(unique_ids[0], "get_id")
            if identifier is not None:
                track_id = str(identifier)
    return {
        "trackId": track_id,
        "classId": class_id,
        "classLabel": label,
        "confidence": confidence,
        "boundingBox": {"x": x, "y": y, "width": width, "height": height},
    }


@dataclass(frozen=True)
class AdapterConfig:
    socket_path: str
    input_url: str
    input_codec: str
    input_transport: str
    input_latency_ms: int
    model_path: str
    model_name: str
    model_version: str
    model_hash: str
    postprocess_so: str
    postprocess_function: str
    postprocess_config: str
    source_id: str
    accelerator: str
    width: int
    height: int

    @classmethod
    def from_environment(cls, socket_override: str | None = None) -> "AdapterConfig":
        socket_path = socket_override or environment("ATLAS_PERCEPTION_SOCKET_PATH")
        model_path = environment("ATLAS_PERCEPTION_MODEL_PATH")
        postprocess_so = environment("ATLAS_PERCEPTION_POSTPROCESS_SO", DEFAULT_POSTPROCESS_SO)
        input_codec = environment("ATLAS_A8_RTP_CODEC", "auto").lower()
        input_transport = environment("ATLAS_A8_RTSP_TRANSPORT", "tcp").lower()
        if not socket_path or not Path(socket_path).is_absolute():
            raise ValueError("ATLAS_PERCEPTION_SOCKET_PATH must be an absolute path")
        if not model_path or not Path(model_path).is_file():
            raise ValueError("ATLAS_PERCEPTION_MODEL_PATH must name an existing HEF file")
        if not Path(postprocess_so).is_file():
            raise ValueError("ATLAS_PERCEPTION_POSTPROCESS_SO must name an existing TAPPAS postprocess library")
        if input_codec not in {"auto", "h264", "h265"}:
            raise ValueError("ATLAS_A8_RTP_CODEC must be auto, h264, or h265")
        if input_transport not in {"tcp", "udp"}:
            raise ValueError("ATLAS_A8_RTSP_TRANSPORT must be tcp or udp")
        postprocess_config = environment("ATLAS_PERCEPTION_POSTPROCESS_CONFIG")
        if postprocess_config and not Path(postprocess_config).is_file():
            raise ValueError("ATLAS_PERCEPTION_POSTPROCESS_CONFIG does not exist")
        model_hash = environment("ATLAS_PERCEPTION_MODEL_HASH") or sha256_file(Path(model_path))
        return cls(
            socket_path=socket_path,
            input_url=environment("ATLAS_A8_RTSP_URL", "rtsp://192.168.144.25:8554/main.264"),
            input_codec=input_codec,
            input_transport=input_transport,
            input_latency_ms=bounded_integer("ATLAS_A8_RTSP_LATENCY_MS", 75, 0, 2_000),
            model_path=model_path,
            model_name=environment("ATLAS_PERCEPTION_MODEL_NAME", Path(model_path).stem),
            model_version=environment("ATLAS_PERCEPTION_MODEL_VERSION", "1"),
            model_hash=f"sha256:{model_hash.removeprefix('sha256:')}",
            postprocess_so=postprocess_so,
            postprocess_function=environment("ATLAS_PERCEPTION_POSTPROCESS_FUNCTION", "filter"),
            postprocess_config=postprocess_config,
            source_id=environment("ATLAS_VIDEO_SOURCE_ID", "a8-main"),
            accelerator=environment("ATLAS_HAILO_ACCELERATOR", "hailo-8l"),
            width=bounded_integer("ATLAS_PERCEPTION_WIDTH", 640, 64, 4096),
            height=bounded_integer("ATLAS_PERCEPTION_HEIGHT", 640, 64, 4096),
        )


def bounded_integer(name: str, fallback: int, minimum: int, maximum: int) -> int:
    raw = environment(name)
    try:
        value = int(raw) if raw else fallback
    except ValueError as error:
        raise ValueError(f"{name} must be an integer") from error
    if value < minimum or value > maximum:
        raise ValueError(f"{name} must be between {minimum} and {maximum}")
    return value


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while chunk := handle.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def gst_quote(value: str) -> str:
    return json.dumps(value)


def build_pipeline(config: AdapterConfig) -> str:
    if config.input_codec == "h264":
        decode = "rtph264depay ! h264parse ! avdec_h264"
    elif config.input_codec == "h265":
        decode = "rtph265depay ! h265parse ! avdec_h265"
    else:
        decode = "application/x-rtp,media=video ! decodebin"
    hailofilter = (
        f"hailofilter so-path={gst_quote(config.postprocess_so)} "
        f"function-name={gst_quote(config.postprocess_function)} qos=false"
    )
    if config.postprocess_config:
        hailofilter += f" config-path={gst_quote(config.postprocess_config)}"
    return " ".join(
        [
            "rtspsrc",
            f"location={gst_quote(config.input_url)}",
            f"protocols={config.input_transport}",
            f"latency={config.input_latency_ms}",
            "drop-on-latency=true do-retransmission=false",
            "!",
            decode,
            "! queue leaky=downstream max-size-buffers=2 max-size-bytes=0 max-size-time=0",
            "! videoconvert ! videoscale",
            f"! video/x-raw,format=RGB,width={config.width},height={config.height}",
            "! queue leaky=downstream max-size-buffers=1 max-size-bytes=0 max-size-time=0",
            "! identity name=atlas_pre_infer silent=true",
            f"! hailonet hef-path={gst_quote(config.model_path)} batch-size=1 force-writable=true",
            f"! {hailofilter}",
            "! identity name=atlas_detection_output silent=true",
            "! fakesink sync=false qos=false",
        ]
    )


class RuntimePublisher:
    def __init__(self, socket_path: str) -> None:
        self.socket_path = socket_path
        self.condition = threading.Condition()
        self.pending_frame: dict[str, Any] | None = None
        self.pending_health: dict[str, Any] | None = None
        self.stopping = False
        self.connected = False
        self.dropped_frames = 0
        self.thread = threading.Thread(target=self._run, name="atlas-perception-publisher", daemon=True)

    def start(self) -> None:
        self.thread.start()

    def stop(self) -> None:
        with self.condition:
            self.stopping = True
            self.condition.notify_all()
        self.thread.join(timeout=2)

    def publish_frame(self, frame: dict[str, Any]) -> None:
        with self.condition:
            if self.pending_frame is not None:
                self.dropped_frames += 1
            self.pending_frame = {"protocolVersion": RUNTIME_PROTOCOL_VERSION, "type": "frame", "frame": frame}
            self.condition.notify()

    def publish_health(self, health: dict[str, Any]) -> None:
        with self.condition:
            self.pending_health = {"protocolVersion": RUNTIME_PROTOCOL_VERSION, "type": "health", "health": health}
            self.condition.notify()

    def _take(self) -> dict[str, Any] | None:
        with self.condition:
            self.condition.wait_for(lambda: self.stopping or self.pending_health is not None or self.pending_frame is not None, timeout=1)
            if self.stopping:
                return None
            if self.pending_health is not None:
                message, self.pending_health = self.pending_health, None
                return message
            message, self.pending_frame = self.pending_frame, None
            return message

    def _run(self) -> None:
        connection: socket.socket | None = None
        while not self.stopping:
            message = self._take()
            if message is None:
                continue
            while not self.stopping:
                try:
                    if connection is None:
                        connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                        connection.connect(self.socket_path)
                        self.connected = True
                    connection.sendall(json.dumps(message, separators=(",", ":")).encode("utf-8") + b"\n")
                    break
                except OSError:
                    self.connected = False
                    if connection is not None:
                        connection.close()
                    connection = None
                    time.sleep(0.5)
        if connection is not None:
            connection.close()


class AdapterState:
    def __init__(self, config: AdapterConfig, publisher: RuntimePublisher) -> None:
        self.config = config
        self.publisher = publisher
        self.stream_epoch = str(uuid.uuid4())
        self.frame_count = 0
        self.frame_times: deque[float] = deque(maxlen=120)
        self.inference_times: deque[float] = deque(maxlen=120)
        self.inflight: dict[int, int] = {}
        self.last_frame_at = ""
        self.last_frame_monotonic = 0.0
        self.last_detection_at = ""
        self.last_error = ""
        self.pipeline_playing = False
        self.model = {
            "name": config.model_name,
            "version": config.model_version,
            "artifactHash": config.model_hash,
        }

    def rate(self, values: deque[float]) -> float:
        if len(values) < 2:
            return 0.0
        duration = values[-1] - values[0]
        return 0.0 if duration <= 0 else (len(values) - 1) / duration

    def health(self) -> dict[str, Any]:
        now = utc_now()
        health: dict[str, Any] = {
            "sourceId": self.config.source_id,
            "provider": "hailo",
            "accelerator": self.config.accelerator,
            "inputConnected": self.last_frame_monotonic > 0 and time.monotonic() - self.last_frame_monotonic < 3.0,
            "inferenceReady": self.pipeline_playing and self.last_frame_monotonic > 0 and time.monotonic() - self.last_frame_monotonic < 3.0,
            "outputPublishing": self.publisher.connected,
            "inputFps": self.rate(self.frame_times),
            "inferenceFps": self.rate(self.inference_times),
            "droppedFrames": self.publisher.dropped_frames,
            "lastError": self.last_error,
            "model": self.model,
            "observedAt": now,
        }
        if self.last_frame_at:
            health["lastFrameAt"] = self.last_frame_at
        if self.last_detection_at:
            health["lastDetectionAt"] = self.last_detection_at
        return health


def run_adapter(config: AdapterConfig, dry_run: bool = False) -> int:
    pipeline_description = build_pipeline(config)
    if dry_run:
        print(pipeline_description)
        return 0
    try:
        import gi

        gi.require_version("Gst", "1.0")
        from gi.repository import GLib, Gst
        import hailo
    except (ImportError, ValueError) as error:
        raise RuntimeError(
            "Hailo adapter requires system PyGObject, GStreamer, HailoRT, and TAPPAS Python bindings"
        ) from error

    Gst.init(None)
    publisher = RuntimePublisher(config.socket_path)
    state = AdapterState(config, publisher)
    publisher.start()
    pipeline = Gst.parse_launch(pipeline_description)
    pre_infer = pipeline.get_by_name("atlas_pre_infer")
    detection_output = pipeline.get_by_name("atlas_detection_output")
    if pre_infer is None or detection_output is None:
        raise RuntimeError("Hailo pipeline did not create Atlas probe elements")

    def pre_infer_probe(_pad: Any, info: Any) -> Any:
        buffer = info.get_buffer()
        if buffer is not None:
            pts = int(buffer.pts)
            state.inflight[pts] = time.monotonic_ns()
            if len(state.inflight) > 256:
                state.inflight.pop(next(iter(state.inflight)))
        return Gst.PadProbeReturn.OK

    def detection_probe(pad: Any, info: Any) -> Any:
        buffer = info.get_buffer()
        if buffer is None:
            return Gst.PadProbeReturn.OK
        completed_ns = time.monotonic_ns()
        pts = int(buffer.pts)
        started_ns = state.inflight.pop(pts, completed_ns)
        inference_latency_ms = max(0.0, (completed_ns - started_ns) / 1_000_000)
        observed_at = utc_now()
        try:
            roi = hailo.get_roi_from_buffer(buffer)
            detections = [
                detection_payload(detection, hailo)
                for detection in roi.get_objects_typed(hailo.HAILO_DETECTION)
            ]
            caps = pad.get_current_caps()
            structure = caps.get_structure(0) if caps and caps.get_size() else None
            width = structure.get_value("width") if structure and structure.has_field("width") else config.width
            height = structure.get_value("height") if structure and structure.has_field("height") else config.height
            state.frame_count += 1
            state.frame_times.append(time.monotonic())
            state.inference_times.append(time.monotonic())
            state.last_frame_at = observed_at
            state.last_frame_monotonic = time.monotonic()
            if detections:
                state.last_detection_at = observed_at
            frame = {
                "sourceId": config.source_id,
                "streamEpoch": state.stream_epoch,
                "frameId": str(state.frame_count),
                "observedAt": observed_at,
                "sourcePtsNs": 0 if pts == int(Gst.CLOCK_TIME_NONE) else pts,
                "imageWidth": int(width),
                "imageHeight": int(height),
                "model": state.model,
                "inferenceLatencyMs": inference_latency_ms,
                "detections": detections,
            }
            publisher.publish_frame(frame)
            state.last_error = ""
        except Exception as error:
            state.last_error = f"extract Hailo detections: {error}"
        return Gst.PadProbeReturn.OK

    pre_infer.get_static_pad("src").add_probe(Gst.PadProbeType.BUFFER, pre_infer_probe)
    detection_output.get_static_pad("src").add_probe(Gst.PadProbeType.BUFFER, detection_probe)

    loop = GLib.MainLoop()

    def publish_health() -> bool:
        publisher.publish_health(state.health())
        return True

    def bus_message(_bus: Any, message: Any) -> None:
        if message.type == Gst.MessageType.ERROR:
            error, debug = message.parse_error()
            state.last_error = f"GStreamer: {error}" + (f" ({debug})" if debug else "")
            publisher.publish_health(state.health())
            loop.quit()
        elif message.type == Gst.MessageType.EOS:
            state.last_error = "RTSP inference pipeline reached end of stream"
            publisher.publish_health(state.health())
            loop.quit()

    bus = pipeline.get_bus()
    bus.add_signal_watch()
    bus.connect("message", bus_message)
    GLib.timeout_add_seconds(1, publish_health)

    def stop(_signum: int, _frame: Any) -> None:
        loop.quit()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    result = pipeline.set_state(Gst.State.PLAYING)
    if result == Gst.StateChangeReturn.FAILURE:
        publisher.stop()
        raise RuntimeError("Hailo GStreamer pipeline failed to enter PLAYING state")
    state.pipeline_playing = True
    publisher.publish_health(state.health())
    try:
        loop.run()
    finally:
        state.pipeline_playing = False
        pipeline.set_state(Gst.State.NULL)
        publisher.publish_health(state.health())
        publisher.stop()
    return 0 if not state.last_error else 1


def main() -> int:
    parser = argparse.ArgumentParser(description="Publish HailoRT detections to Atlas Agent")
    parser.add_argument("--socket", help="override ATLAS_PERCEPTION_SOCKET_PATH")
    parser.add_argument("--dry-run", action="store_true", help="validate configuration and print the pipeline")
    args = parser.parse_args()
    try:
        return run_adapter(AdapterConfig.from_environment(args.socket), args.dry_run)
    except (ValueError, RuntimeError) as error:
        print(f"atlas-hailort-adapter: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
