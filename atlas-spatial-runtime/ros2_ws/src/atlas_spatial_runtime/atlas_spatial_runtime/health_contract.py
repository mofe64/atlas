"""Pure-Python health state used by the ROS node and host-side tests."""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass, field
from hashlib import sha256
import json
import math
import time
from typing import Iterable, Optional

from . import PROTOCOL_VERSION


def _finite_non_negative(value: float) -> bool:
    return math.isfinite(value) and value >= 0


def calibration_hash(values: Iterable[float]) -> str:
    canonical = json.dumps(list(values), allow_nan=False, separators=(",", ":"))
    return "sha256:" + sha256(canonical.encode("utf-8")).hexdigest()


@dataclass
class StreamWindow:
    arrivals_ns: deque[int] = field(default_factory=lambda: deque(maxlen=120))
    captures_ns: deque[int] = field(default_factory=lambda: deque(maxlen=120))
    last_capture_ns: Optional[int] = None
    width: int = 0
    height: int = 0
    encoding: str = ""

    def observe(self, capture_ns: int, arrival_ns: int, width: int, height: int, encoding: str) -> None:
        if capture_ns <= 0 or arrival_ns <= 0 or width <= 0 or height <= 0:
            raise ValueError("stream samples require positive timestamps and dimensions")
        self.last_capture_ns = capture_ns
        self.captures_ns.append(capture_ns)
        self.arrivals_ns.append(arrival_ns)
        self.width = width
        self.height = height
        self.encoding = encoding

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
    device_id: str = ""
    model: str = ""
    usb_transport: str = "unknown"
    imu_available: bool = False
    calibration_digest: str = ""
    last_error: str = ""
    started_monotonic_ns: int = field(default_factory=time.monotonic_ns)
    color: StreamWindow = field(default_factory=StreamWindow)
    depth: StreamWindow = field(default_factory=StreamWindow)

    def set_calibration(self, values: Iterable[float]) -> None:
        self.calibration_digest = calibration_hash(values)

    def snapshot(
        self,
        now_ns: Optional[int] = None,
        stale_after_ms: float = 1000.0,
        sync_tolerance_ms: float = 25.0,
    ) -> dict:
        now_ns = now_ns or time.monotonic_ns()
        stream_ages_ms = [
            (now_ns - stream.arrivals_ns[-1]) / 1_000_000.0
            for stream in (self.color, self.depth)
            if stream.arrivals_ns
        ]
        last_frame_age_ms = max(stream_ages_ms) if len(stream_ages_ms) == 2 else None
        sync_skew_ms = None
        synchronized = False
        if self.color.captures_ns and self.depth.captures_ns:
            sync_skew_ms = min(
                abs(color_ns - depth_ns)
                for color_ns in list(self.color.captures_ns)[-10:]
                for depth_ns in list(self.depth.captures_ns)[-10:]
            ) / 1_000_000.0
            synchronized = sync_skew_ms <= sync_tolerance_ms
        streams_ready = bool(self.color.arrivals_ns and self.depth.arrivals_ns)
        fresh = (
            last_frame_age_ms is not None
            and all(_finite_non_negative(age) and age <= stale_after_ms for age in stream_ages_ms)
        )
        ready = streams_ready and fresh and synchronized and not self.last_error and bool(self.calibration_digest)
        return {
            "protocolVersion": PROTOCOL_VERSION,
            "status": "ready" if ready else "degraded",
            "ready": ready,
            "provider": self.provider,
            "sourceId": self.source_id,
            "device": {
                "id": self.device_id,
                "model": self.model,
                "connection": self.usb_transport,
            },
            "capabilities": {"color": True, "depth": True, "imu": self.imu_available},
            "calibrationHash": self.calibration_digest,
            "synchronized": synchronized,
            "syncSkewMs": sync_skew_ms,
            "lastFrameAgeMs": last_frame_age_ms,
            "streams": {
                "color": {
                    "width": self.color.width,
                    "height": self.color.height,
                    "encoding": self.color.encoding,
                    "fps": self.color.fps(),
                },
                "depth": {
                    "width": self.depth.width,
                    "height": self.depth.height,
                    "encoding": self.depth.encoding,
                    "unit": "metre",
                    "fps": self.depth.fps(),
                },
            },
            "lastError": self.last_error,
        }


def validate_probe_request(raw: bytes) -> None:
    if len(raw) > 4096:
        raise ValueError("probe request exceeds 4096 bytes")
    request = json.loads(raw.decode("utf-8"))
    if request.get("protocolVersion") != PROTOCOL_VERSION or request.get("type") != "probe":
        raise ValueError("unsupported spatial probe request")
