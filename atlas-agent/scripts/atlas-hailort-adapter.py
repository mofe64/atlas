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


RUNTIME_PROTOCOL_VERSION = "3"
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
    cmc_max_dimension: int = 320
    cmc_max_features: int = 300
    tracker_algorithm: str = "byte_track"

    @classmethod
    def from_environment(cls, socket_override: str | None = None) -> "AdapterConfig":
        socket_path = socket_override or environment("ATLAS_PERCEPTION_SOCKET_PATH")
        model_path = environment("ATLAS_PERCEPTION_MODEL_PATH")
        postprocess_so = environment("ATLAS_PERCEPTION_POSTPROCESS_SO", DEFAULT_POSTPROCESS_SO)
        input_codec = environment("ATLAS_A8_RTP_CODEC", "auto").lower()
        input_transport = environment("ATLAS_A8_RTSP_TRANSPORT", "tcp").lower()
        tracker_algorithm = environment("ATLAS_TRACKER_ALGORITHM", "byte_track").lower()
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
        if tracker_algorithm not in {"disabled", "byte_track", "byte_track_cmc"}:
            raise ValueError("ATLAS_TRACKER_ALGORITHM must be disabled, byte_track, or byte_track_cmc")
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
            cmc_max_dimension=bounded_integer("ATLAS_TRACKER_CMC_MAX_DIMENSION", 320, 96, 1024),
            cmc_max_features=bounded_integer("ATLAS_TRACKER_CMC_MAX_FEATURES", 300, 32, 2_000),
            tracker_algorithm=tracker_algorithm,
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


NTP_UNIX_EPOCH_OFFSET_NS = 2_208_988_800 * 1_000_000_000


def enable_source_reference_timestamps(pipeline: Any) -> bool:
    """Enable reconstructed sender timestamps when GStreamer supports them.

    add-reference-timestamp-meta was introduced in GStreamer 1.22. Looking up
    the property dynamically keeps Atlas compatible with older Hailo images.
    """
    source = pipeline.get_by_name("atlas_rtsp_source")
    if source is None or source.find_property("add-reference-timestamp-meta") is None:
        return False
    try:
        source.set_property("add-reference-timestamp-meta", True)
        return True
    except Exception:
        return False


def source_capture_unix_ns(buffer: Any, gst_module: Any) -> int | None:
    """Return a recognized absolute source timestamp without guessing epochs."""
    getter = getattr(buffer, "get_reference_timestamp_meta", None)
    if getter is None:
        return None
    candidates = (
        ("timestamp/x-unix", 0),
        ("timestamp/x-ntp", NTP_UNIX_EPOCH_OFFSET_NS),
    )
    for caps_name, epoch_offset in candidates:
        try:
            meta = getter(gst_module.Caps.from_string(caps_name))
        except Exception:
            return None
        if meta is None:
            continue
        try:
            timestamp = int(meta.timestamp)
        except (AttributeError, TypeError, ValueError):
            return None
        if timestamp == int(gst_module.CLOCK_TIME_NONE) or timestamp <= epoch_offset:
            continue
        return timestamp - epoch_offset
    return None


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
            "name=atlas_rtsp_source",
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


class CameraMotionEstimator:
    """Sparse optical-flow global motion for the exact inference frame.

    The returned 3x3 matrix maps normalized points in the previous frame to
    normalized points in the current frame. Foreground association remains in
    Agent; this class only measures global image motion.
    """

    METHOD = "SPARSE_OPTICAL_FLOW"

    def __init__(self, cv2_module: Any, numpy_module: Any, max_dimension: int, max_features: int) -> None:
        self.cv2 = cv2_module
        self.np = numpy_module
        self.max_dimension = max_dimension
        self.max_features = max_features
        self.previous_gray: Any = None
        self.previous_points: Any = None

    def reset(self) -> None:
        self.previous_gray = None
        self.previous_points = None

    def estimate(self, rgb_frame: Any) -> dict[str, Any] | None:
        if rgb_frame is None or len(rgb_frame.shape) != 3 or rgb_frame.shape[2] != 3:
            raise ValueError("CMC requires an RGB frame")
        height, width = rgb_frame.shape[:2]
        scale = min(1.0, self.max_dimension / max(height, width))
        if scale < 1.0:
            resized = self.cv2.resize(
                rgb_frame,
                (max(1, round(width * scale)), max(1, round(height * scale))),
                interpolation=self.cv2.INTER_AREA,
            )
        else:
            resized = rgb_frame
        gray = self.cv2.cvtColor(resized, self.cv2.COLOR_RGB2GRAY)
        current_points = self.cv2.goodFeaturesToTrack(
            gray,
            maxCorners=self.max_features,
            qualityLevel=0.01,
            minDistance=3,
            blockSize=3,
            useHarrisDetector=False,
        )
        if self.previous_gray is None or self.previous_points is None or len(self.previous_points) < 6:
            self.previous_gray = gray.copy()
            self.previous_points = current_points
            return None
        tracked, status, _errors = self.cv2.calcOpticalFlowPyrLK(
            self.previous_gray,
            gray,
            self.previous_points,
            None,
            winSize=(21, 21),
            maxLevel=3,
            criteria=(self.cv2.TERM_CRITERIA_EPS | self.cv2.TERM_CRITERIA_COUNT, 30, 0.01),
        )
        previous_points = self.previous_points
        self.previous_gray = gray.copy()
        self.previous_points = current_points
        if tracked is None or status is None:
            return None
        mask = status.reshape(-1).astype(bool)
        previous = previous_points.reshape(-1, 2)[mask]
        current = tracked.reshape(-1, 2)[mask]
        finite = self.np.isfinite(previous).all(axis=1) & self.np.isfinite(current).all(axis=1)
        previous = previous[finite]
        current = current[finite]
        if len(previous) < 6:
            return None
        affine, inliers = self.cv2.estimateAffinePartial2D(
            previous,
            current,
            method=self.cv2.RANSAC,
            ransacReprojThreshold=3.0,
            maxIters=1_000,
            confidence=0.99,
            refineIters=10,
        )
        if affine is None or inliers is None or not self.np.isfinite(affine).all():
            return None
        motion_width = gray.shape[1]
        motion_height = gray.shape[0]
        determinant = float(self.np.linalg.det(affine[:, :2]))
        translation_x = float(affine[0, 2])
        translation_y = float(affine[1, 2])
        if abs(determinant) < 0.5 or abs(determinant) > 2.0:
            return None
        if abs(translation_x) > motion_width * 0.5 or abs(translation_y) > motion_height * 0.5:
            return None
        inlier_count = int(inliers.reshape(-1).sum())
        confidence = bounded_float(
            (inlier_count / len(previous)) * min(1.0, inlier_count / 30.0),
            0.0,
            1.0,
        )
        normalized = [
            float(affine[0, 0]),
            float(affine[0, 1]) * motion_height / motion_width,
            translation_x / motion_width,
            float(affine[1, 0]) * motion_width / motion_height,
            float(affine[1, 1]),
            translation_y / motion_height,
            0.0,
            0.0,
            1.0,
        ]
        return {"method": self.METHOD, "homography": normalized, "confidence": confidence}


def map_rgb_buffer(buffer: Any, pad: Any, gst_module: Any, numpy_module: Any, fallback_width: int, fallback_height: int) -> Any:
    caps = pad.get_current_caps()
    structure = caps.get_structure(0) if caps and caps.get_size() else None
    width = int(structure.get_value("width")) if structure and structure.has_field("width") else fallback_width
    height = int(structure.get_value("height")) if structure and structure.has_field("height") else fallback_height
    mapped, mapping = buffer.map(gst_module.MapFlags.READ)
    if not mapped:
        raise RuntimeError("could not map RGB inference buffer for CMC")
    try:
        raw = numpy_module.frombuffer(mapping.data, dtype=numpy_module.uint8)
        if height <= 0 or raw.size < width * height * 3:
            raise RuntimeError("RGB inference buffer is smaller than its negotiated caps")
        row_stride = raw.size // height
        if row_stride < width * 3:
            raise RuntimeError("RGB inference buffer row stride is invalid")
        return raw[: row_stride * height].reshape(height, row_stride)[:, : width * 3].reshape(height, width, 3).copy()
    finally:
        buffer.unmap(mapping)


class RuntimePublisher:
    def __init__(self, socket_path: str) -> None:
        self.socket_path = socket_path
        self.condition = threading.Condition()
        self.pending_frame: dict[str, Any] | None = None
        self.pending_health: dict[str, Any] | None = None
        self.pending_control: deque[dict[str, Any]] = deque()
        self.stopping = False
        self.connected = False
        self.connection: socket.socket | None = None
        self.command_handler: Any = None
        self.disconnect_handler: Any = None
        self.dropped_frames = 0
        self.thread = threading.Thread(target=self._run, name="atlas-perception-publisher", daemon=True)
        self.reader_thread = threading.Thread(target=self._read_commands, name="atlas-perception-control", daemon=True)

    def start(self) -> None:
        self.thread.start()
        self.reader_thread.start()

    def set_command_handler(self, handler: Any) -> None:
        self.command_handler = handler

    def set_disconnect_handler(self, handler: Any) -> None:
        self.disconnect_handler = handler

    def stop(self) -> None:
        with self.condition:
            self.stopping = True
            connection = self.connection
            self.connection = None
            self.connected = False
            self.condition.notify_all()
        if connection is not None:
            connection.close()
        self.thread.join(timeout=2)
        self.reader_thread.join(timeout=2)

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

    def publish_activation_result(self, request_id: str, state: str, source_id: str, error: str = "") -> None:
        with self.condition:
            self.pending_control.append({
                "protocolVersion": RUNTIME_PROTOCOL_VERSION,
                "type": "activation_result",
                "activationResult": {
                    "requestId": request_id,
                    "state": state,
                    "sourceId": source_id,
                    "observedAt": utc_now(),
                    "error": error,
                },
            })
            self.condition.notify()

    def _take(self) -> dict[str, Any] | None:
        with self.condition:
            self.condition.wait_for(lambda: self.stopping or self.pending_control or self.pending_health is not None or self.pending_frame is not None, timeout=1)
            if self.stopping:
                return None
            if self.pending_control:
                return self.pending_control.popleft()
            if self.pending_health is not None:
                message, self.pending_health = self.pending_health, None
                return message
            message, self.pending_frame = self.pending_frame, None
            return message

    def _run(self) -> None:
        while not self.stopping:
            message = self._take()
            if message is None:
                continue
            while not self.stopping:
                connection = None
                try:
                    connection = self._connection()
                    connection.sendall(json.dumps(message, separators=(",", ":")).encode("utf-8") + b"\n")
                    break
                except OSError:
                    self._disconnect(connection)
                    time.sleep(0.5)

    def _connection(self) -> socket.socket:
        with self.condition:
            if self.connection is not None:
                return self.connection
        connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        connection.connect(self.socket_path)
        with self.condition:
            if self.connection is None:
                self.connection = connection
                self.connected = True
                self.condition.notify_all()
                return connection
            current = self.connection
        connection.close()
        return current

    def _disconnect(self, connection: socket.socket | None) -> None:
        with self.condition:
            if connection is not None and self.connection is not connection:
                return
            current = self.connection
            self.connection = None
            self.connected = False
            self.condition.notify_all()
        if current is not None:
            current.close()
            if not self.stopping and self.disconnect_handler is not None:
                self.disconnect_handler()

    def _read_commands(self) -> None:
        buffered = b""
        while not self.stopping:
            with self.condition:
                self.condition.wait_for(lambda: self.stopping or self.connection is not None, timeout=1)
                if self.stopping:
                    return
                connection = self.connection
            if connection is None:
                continue
            try:
                chunk = connection.recv(64 * 1024)
                if not chunk:
                    raise OSError("Atlas Agent closed the runtime socket")
                buffered += chunk
                while b"\n" in buffered:
                    line, buffered = buffered.split(b"\n", 1)
                    if not line:
                        continue
                    command = json.loads(line)
                    if command.get("protocolVersion") != RUNTIME_PROTOCOL_VERSION:
                        raise ValueError("unsupported Atlas runtime control protocol")
                    if command.get("type") != "activation_request":
                        raise ValueError("unsupported Atlas runtime control message")
                    if self.command_handler is not None:
                        self.command_handler(command)
            except (OSError, ValueError, json.JSONDecodeError):
                buffered = b""
                self._disconnect(connection)
                time.sleep(0.1)


class AdapterState:
    def __init__(self, config: AdapterConfig, publisher: RuntimePublisher) -> None:
        self.config = config
        self.publisher = publisher
        self.stream_epoch = str(uuid.uuid4())
        self.frame_count = 0
        self.frame_times: deque[float] = deque(maxlen=120)
        self.inference_times: deque[float] = deque(maxlen=120)
        # PTS -> (pre-inference companion monotonic ns, companion unix ns,
        # optional reconstructed source-capture Unix ns).
        # Detection completion is deliberately not used as frame time.
        self.inflight: dict[int, tuple[int, int, int | None]] = {}
        self.camera_motion: dict[int, dict[str, Any]] = {}
        self.last_frame_at = ""
        self.last_frame_monotonic = 0.0
        self.last_detection_at = ""
        self.last_error = ""
        self.pipeline_playing = False
        self.source_reference_timestamp_supported = False
        self.source_reference_frames = 0
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
            "activationState": "FAILED" if self.last_error else ("ACTIVE" if self.pipeline_playing else "INACTIVE"),
            "accelerator": self.config.accelerator,
            "inputConnected": self.last_frame_monotonic > 0 and time.monotonic() - self.last_frame_monotonic < 3.0,
            "inferenceReady": self.pipeline_playing and self.last_frame_monotonic > 0 and time.monotonic() - self.last_frame_monotonic < 3.0,
            "outputPublishing": self.publisher.connected,
            "inputFps": self.rate(self.frame_times),
            "inferenceFps": self.rate(self.inference_times),
            "droppedFrames": self.publisher.dropped_frames,
            "sourceReferenceTimestampSupported": self.source_reference_timestamp_supported,
            "sourceReferenceFrames": self.source_reference_frames,
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
        import cv2
        import numpy as np
    except (ImportError, ValueError) as error:
        raise RuntimeError(
            "Hailo adapter requires system PyGObject, GStreamer, HailoRT, TAPPAS, OpenCV, and NumPy"
        ) from error

    Gst.init(None)
    publisher = RuntimePublisher(config.socket_path)
    state = AdapterState(config, publisher)
    motion_estimator = (
        CameraMotionEstimator(cv2, np, config.cmc_max_dimension, config.cmc_max_features)
        if config.tracker_algorithm == "byte_track_cmc"
        else None
    )
    pipeline = Gst.parse_launch(pipeline_description)
    state.source_reference_timestamp_supported = enable_source_reference_timestamps(pipeline)
    pre_infer = pipeline.get_by_name("atlas_pre_infer")
    detection_output = pipeline.get_by_name("atlas_detection_output")
    if pre_infer is None or detection_output is None:
        raise RuntimeError("Hailo pipeline did not create Atlas probe elements")

    def pre_infer_probe(pad: Any, info: Any) -> Any:
        buffer = info.get_buffer()
        if buffer is not None:
            pts = int(buffer.pts)
            if motion_estimator is not None:
                try:
                    rgb_frame = map_rgb_buffer(buffer, pad, Gst, np, config.width, config.height)
                    motion = motion_estimator.estimate(rgb_frame)
                    if motion is not None:
                        state.camera_motion[pts] = motion
                        if len(state.camera_motion) > 256:
                            state.camera_motion.pop(next(iter(state.camera_motion)))
                except Exception:
                    # CMC is observable downstream as DEGRADED. It must never stop
                    # detector inference or the clean RTSP path.
                    state.camera_motion.pop(pts, None)
            capture_unix_ns = source_capture_unix_ns(buffer, Gst)
            if capture_unix_ns is not None:
                state.source_reference_frames += 1
            state.inflight[pts] = (time.monotonic_ns(), time.time_ns(), capture_unix_ns)
            if len(state.inflight) > 256:
                state.inflight.pop(next(iter(state.inflight)))
        return Gst.PadProbeReturn.OK

    def detection_probe(pad: Any, info: Any) -> Any:
        buffer = info.get_buffer()
        if buffer is None:
            return Gst.PadProbeReturn.OK
        completed_ns = time.monotonic_ns()
        pts = int(buffer.pts)
        ingress_timing = state.inflight.pop(pts, None)
        if ingress_timing is None:
            state.last_error = "frame reached detection output without a pre-inference timing anchor"
            return Gst.PadProbeReturn.OK
        started_ns, ingress_unix_ns, capture_unix_ns = ingress_timing
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
            timing = {
                "sourcePtsPresent": pts != int(Gst.CLOCK_TIME_NONE),
                "pipelineIngressMonotonicNs": started_ns,
                "pipelineIngressUnixNs": ingress_unix_ns,
            }
            if capture_unix_ns is not None:
                timing["sourceCaptureUnixNs"] = capture_unix_ns
            frame = {
                "sourceId": config.source_id,
                "streamEpoch": state.stream_epoch,
                "frameId": str(state.frame_count),
                "observedAt": observed_at,
                "sourcePtsNs": 0 if pts == int(Gst.CLOCK_TIME_NONE) else pts,
                "timing": timing,
                "imageWidth": int(width),
                "imageHeight": int(height),
                "model": state.model,
                "inferenceLatencyMs": inference_latency_ms,
                "detections": detections,
            }
            camera_motion = state.camera_motion.pop(pts, None)
            if camera_motion is not None:
                frame["cameraMotion"] = camera_motion
            publisher.publish_frame(frame)
            state.last_error = ""
        except Exception as error:
            state.last_error = f"extract Hailo detections: {error}"
        return Gst.PadProbeReturn.OK

    pre_infer.get_static_pad("src").add_probe(Gst.PadProbeType.BUFFER, pre_infer_probe)
    detection_output.get_static_pad("src").add_probe(Gst.PadProbeType.BUFFER, detection_probe)

    loop = GLib.MainLoop()

    def apply_activation(command: dict[str, Any]) -> bool:
        request_id = str(command.get("requestId", "")).strip()
        desired_state = str(command.get("desiredState", "")).strip().upper()
        if not request_id or desired_state not in {"ACTIVE", "INACTIVE"}:
            publisher.publish_activation_result(request_id, "FAILED", config.source_id, "invalid activation request")
            return False
        target = Gst.State.PLAYING if desired_state == "ACTIVE" else Gst.State.READY
        result = pipeline.set_state(target)
        if result == Gst.StateChangeReturn.FAILURE:
            state.pipeline_playing = False
            state.last_error = f"GStreamer failed to enter {desired_state} state"
            publisher.publish_health(state.health())
            publisher.publish_activation_result(request_id, "FAILED", config.source_id, state.last_error)
            return False
        change_result, current_state, _pending_state = pipeline.get_state(10 * Gst.SECOND)
        if change_result == Gst.StateChangeReturn.FAILURE or current_state != target:
            state.pipeline_playing = False
            state.last_error = f"GStreamer did not reach {desired_state} state"
            publisher.publish_health(state.health())
            publisher.publish_activation_result(request_id, "FAILED", config.source_id, state.last_error)
            return False
        state.pipeline_playing = desired_state == "ACTIVE"
        if not state.pipeline_playing:
            state.inflight.clear()
            state.camera_motion.clear()
            if motion_estimator is not None:
                motion_estimator.reset()
        state.last_error = ""
        publisher.publish_health(state.health())
        publisher.publish_activation_result(request_id, desired_state, config.source_id)
        return False

    def receive_control(command: dict[str, Any]) -> None:
        GLib.idle_add(apply_activation, command)

    def fail_safe_inactive() -> None:
        GLib.idle_add(apply_activation, {
            "requestId": "agent-disconnected",
            "desiredState": "INACTIVE",
        })

    publisher.set_command_handler(receive_control)
    publisher.set_disconnect_handler(fail_safe_inactive)
    publisher.start()

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
    result = pipeline.set_state(Gst.State.READY)
    if result == Gst.StateChangeReturn.FAILURE:
        publisher.stop()
        raise RuntimeError("Hailo GStreamer pipeline failed to enter READY state")
    state.pipeline_playing = False
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
