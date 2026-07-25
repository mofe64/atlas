"""Thread-safe, provider-neutral health state."""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass, field
import json
import math
import threading
import time

from . import PROTOCOL_VERSION
from .provider import CameraCalibration, DepthFrame, ProviderInfo


@dataclass
class StreamWindow:
    arrivals_ns: deque[int] = field(default_factory=lambda: deque(maxlen=120))
    captures_ns: deque[int] = field(default_factory=lambda: deque(maxlen=120))
    frame_id: str = ""
    width: int = 0
    height: int = 0

    def observe(self, frame: DepthFrame) -> None:
        self.captures_ns.append(frame.capture_monotonic_ns)
        self.arrivals_ns.append(frame.arrival_monotonic_ns)
        self.frame_id = frame.calibration.frame_id
        self.width = frame.calibration.width
        self.height = frame.calibration.height

    def fps(self) -> float:
        if len(self.arrivals_ns) < 2:
            return 0.0
        elapsed = self.arrivals_ns[-1] - self.arrivals_ns[0]
        if elapsed <= 0:
            return 0.0
        return (len(self.arrivals_ns) - 1) * 1_000_000_000.0 / elapsed


@dataclass
class SpatialHealthState:
    provider: str
    source_id: str
    device: ProviderInfo
    profile_id: str = ""
    last_error: str = ""
    started_monotonic_ns: int = field(default_factory=time.monotonic_ns)
    depth: StreamWindow = field(default_factory=StreamWindow)
    calibration: CameraCalibration | None = None
    _lock: threading.Lock = field(default_factory=threading.Lock, repr=False)

    def observe(self, frame: DepthFrame) -> None:
        frame.validate()
        with self._lock:
            self.depth.observe(frame)
            self.calibration = frame.calibration
            self.last_error = ""

    def set_device(self, device: ProviderInfo) -> None:
        with self._lock:
            self.device = device

    def set_error(self, message: str) -> None:
        with self._lock:
            self.last_error = message

    def last_arrival_ns(self) -> int | None:
        with self._lock:
            return self.depth.arrivals_ns[-1] if self.depth.arrivals_ns else None

    def provider_streams_live(
        self, now_ns: int | None = None, stale_after_ms: float = 1000.0
    ) -> bool:
        with self._lock:
            if not self.depth.arrivals_ns:
                return False
            age = ((now_ns or time.monotonic_ns()) - self.depth.arrivals_ns[-1]) / 1_000_000.0
            return math.isfinite(age) and 0 <= age <= stale_after_ms

    def snapshot(
        self, now_ns: int | None = None, stale_after_ms: float = 1000.0
    ) -> dict:
        with self._lock:
            now_ns = now_ns or time.monotonic_ns()
            observed = bool(self.depth.arrivals_ns)
            age_ms = (
                (now_ns - self.depth.arrivals_ns[-1]) / 1_000_000.0
                if observed
                else None
            )
            fresh = bool(
                age_ms is not None
                and math.isfinite(age_ms)
                and 0 <= age_ms <= stale_after_ms
            )
            calibration = self.calibration
            calibration_valid = bool(
                calibration
                and calibration.frame_id == self.depth.frame_id
                and calibration.width == self.depth.width
                and calibration.height == self.depth.height
            )
            ready = bool(observed and fresh and calibration_valid and not self.last_error)
            if not observed:
                status = "unavailable"
            elif not fresh:
                status = "stale"
            else:
                status = "ready" if ready else "degraded"
            return {
                "protocolVersion": PROTOCOL_VERSION,
                "status": status,
                "ready": ready,
                "provider": self.provider,
                "sourceId": self.source_id,
                "device": {
                    "id": self.device.device_id,
                    "model": self.device.model,
                    "connection": self.device.connection,
                },
                "aircraftProfile": {
                    "id": self.profile_id,
                },
                "capabilities": {
                    "depth": observed,
                    "obstacleObservations": False,
                },
                "calibration": _calibration_snapshot(calibration),
                "lastFrameAgeMs": age_ms,
                "streams": {
                    "depth": {
                        "frameId": self.depth.frame_id,
                        "width": self.depth.width,
                        "height": self.depth.height,
                        "encoding": "16UC1",
                        "unit": "millimetre",
                        "scaleToMetres": 0.001,
                        "fps": self.depth.fps(),
                    }
                },
                "lastError": self.last_error,
            }


def _calibration_snapshot(calibration: CameraCalibration | None) -> dict:
    if calibration is None:
        return {
            "valid": False,
            "reason": "aligned depth calibration has not been observed",
            "frameId": "",
            "width": 0,
            "height": 0,
            "distortionModel": "",
            "intrinsics": {"fx": 0.0, "fy": 0.0, "cx": 0.0, "cy": 0.0},
        }
    try:
        calibration.validate()
    except ValueError as error:
        valid = False
        reason = str(error)
    else:
        valid = True
        reason = ""
    return {
        "valid": valid,
        "reason": reason,
        "frameId": calibration.frame_id,
        "width": calibration.width,
        "height": calibration.height,
        "distortionModel": calibration.distortion_model,
        "intrinsics": {
            "fx": calibration.fx,
            "fy": calibration.fy,
            "cx": calibration.cx,
            "cy": calibration.cy,
        },
    }


def validate_probe_request(raw: bytes) -> None:
    if len(raw) > 4096:
        raise ValueError("probe request exceeds 4096 bytes")
    request = json.loads(raw.decode("utf-8"))
    if (
        request.get("protocolVersion") != PROTOCOL_VERSION
        or request.get("type") != "probe"
    ):
        raise ValueError("unsupported spatial probe request")
