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


def _finite(values: tuple[float, ...]) -> bool:
    return all(math.isfinite(value) for value in values)


@dataclass
class ImuHealthState:
    """Bounded health window for the normalized BMI270 stream."""

    enabled: bool = True
    clock_reset_threshold_ns: int = 1_000_000_000
    arrivals_ns: deque[int] = field(default_factory=lambda: deque(maxlen=400))
    captures_ns: deque[int] = field(default_factory=lambda: deque(maxlen=400))
    last_frame_id: str = ""
    clock_epoch: int = 0
    reset_count: int = 0
    invalid_samples: int = 0
    last_invalid_arrival_ns: int = 0
    out_of_order_samples: int = 0
    duplicate_timestamp_samples: int = 0
    last_timestamp_anomaly_arrival_ns: int = 0

    def observe(
        self,
        capture_ns: int,
        arrival_ns: int,
        frame_id: str,
        angular_velocity: tuple[float, float, float],
        linear_acceleration: tuple[float, float, float],
    ) -> None:
        if capture_ns <= 0 or arrival_ns <= 0 or not frame_id or not _finite(angular_velocity + linear_acceleration):
            self.invalid_samples += 1
            self.last_invalid_arrival_ns = arrival_ns
            return
        if self.captures_ns and capture_ns <= self.captures_ns[-1]:
            regression_ns = self.captures_ns[-1] - capture_ns
            if regression_ns >= self.clock_reset_threshold_ns:
                self.clock_epoch += 1
                self.reset_count += 1
                self.captures_ns.clear()
                self.arrivals_ns.clear()
                self.last_timestamp_anomaly_arrival_ns = 0
            else:
                if regression_ns == 0:
                    self.duplicate_timestamp_samples += 1
                else:
                    self.out_of_order_samples += 1
                self.last_timestamp_anomaly_arrival_ns = arrival_ns
                self.arrivals_ns.append(arrival_ns)
                self.last_frame_id = frame_id
                return
        self.captures_ns.append(capture_ns)
        self.arrivals_ns.append(arrival_ns)
        self.last_frame_id = frame_id

    def snapshot(
        self,
        now_ns: int | None = None,
        stale_after_ms: float = 250.0,
        minimum_rate_hz: float = 50.0,
    ) -> dict:
        now_ns = now_ns or time.monotonic_ns()
        counters = {
            "clockEpoch": self.clock_epoch,
            "resetCount": self.reset_count,
            "invalidSamples": self.invalid_samples,
            "outOfOrderSamples": self.out_of_order_samples,
            "duplicateTimestampSamples": self.duplicate_timestamp_samples,
            "lastAcceptedCaptureNs": self.captures_ns[-1] if self.captures_ns else None,
        }
        if not self.enabled or not self.arrivals_ns:
            return {
                "status": "unavailable",
                "ready": False,
                "reason": "IMU stream has not been observed",
                "rateHz": 0.0,
                **counters,
            }
        age_ms = (now_ns - self.arrivals_ns[-1]) / 1_000_000.0
        rate_hz = 0.0
        if len(self.arrivals_ns) >= 2 and self.arrivals_ns[-1] > self.arrivals_ns[0]:
            rate_hz = (len(self.arrivals_ns) - 1) * 1_000_000_000.0 / (
                self.arrivals_ns[-1] - self.arrivals_ns[0]
            )
        status, reason = "ready", ""
        if age_ms > stale_after_ms:
            status, reason = "stale", "IMU samples exceeded the freshness limit"
        elif self.last_invalid_arrival_ns and now_ns - self.last_invalid_arrival_ns <= 1_000_000_000:
            status, reason = "degraded", "a recent IMU sample was invalid"
        elif self.last_timestamp_anomaly_arrival_ns and now_ns - self.last_timestamp_anomaly_arrival_ns <= 1_000_000_000:
            status, reason = "degraded", "a recent IMU timestamp was duplicate or out of order"
        elif len(self.arrivals_ns) >= 10 and rate_hz < minimum_rate_hz:
            status, reason = "degraded", "IMU rate is below the configured minimum"
        return {
            "status": status,
            "ready": status == "ready",
            "reason": reason,
            "ageMs": age_ms,
            "rateHz": rate_hz,
            "frameId": self.last_frame_id,
            **counters,
            "timestampProvenance": "DepthAI device-derived ROS header stamp aligned by the driver; raw device tick is not exposed",
        }


@dataclass
class VioHealthState:
    """Direct health for live VIO, without comparing it to the PX4 estimator."""

    enabled: bool = True
    required_initial_samples: int = 10
    sample_count: int = 0
    last_arrival_ns: int = 0
    last_capture_ns: int = 0
    frame_id: str = ""
    child_frame_id: str = ""
    estimator_epoch: int = 0
    invalid_samples: int = 0
    last_invalid_arrival_ns: int = 0

    def observe(
        self,
        capture_ns: int,
        arrival_ns: int,
        values: tuple[float, ...],
        frame_id: str,
        child_frame_id: str,
    ) -> None:
        quaternion_norm = (
            math.sqrt(sum(component * component for component in values[3:7]))
            if len(values) == 7 and _finite(values)
            else 0.0
        )
        if (
            capture_ns <= 0
            or arrival_ns <= 0
            or not frame_id
            or not child_frame_id
            or len(values) != 7
            or not _finite(values)
            or quaternion_norm < 1e-9
            or abs(quaternion_norm - 1.0) > 1e-3
        ):
            self.invalid_samples += 1
            self.last_invalid_arrival_ns = arrival_ns
            return
        if self.last_capture_ns and (
            capture_ns <= self.last_capture_ns
            or frame_id != self.frame_id
            or child_frame_id != self.child_frame_id
        ):
            self.estimator_epoch += 1
            self.sample_count = 0
        self.last_capture_ns = capture_ns
        self.last_arrival_ns = arrival_ns
        self.frame_id = frame_id
        self.child_frame_id = child_frame_id
        self.sample_count += 1

    def tracking_live(
        self,
        now_ns: int | None = None,
        stale_after_ms: float = 1000.0,
    ) -> bool:
        """Return whether the newest VIO observation is valid and fresh."""
        now_ns = now_ns or time.monotonic_ns()
        if not self.enabled or not self.last_arrival_ns or stale_after_ms <= 0:
            return False
        if self.last_invalid_arrival_ns > self.last_arrival_ns:
            return False
        return now_ns - self.last_arrival_ns <= int(stale_after_ms * 1_000_000)

    def snapshot(
        self,
        transform_status: str | None,
        now_ns: int | None = None,
        stale_after_ms: float = 500.0,
    ) -> dict:
        now_ns = now_ns or time.monotonic_ns()
        base = {
            "estimatorMode": "live",
            "authoritative": False,
            "px4FusionEnabled": False,
            "mappingEnabled": True,
            "movementAuthority": False,
            "estimatorEpoch": self.estimator_epoch,
            "invalidSamples": self.invalid_samples,
            "frameId": self.frame_id,
            "childFrameId": self.child_frame_id,
            "transformStatus": transform_status or "unmeasured",
        }
        if not self.enabled:
            return {**base, "status": "unavailable", "ready": False, "reason": "VIO is disabled"}
        if self.last_invalid_arrival_ns > self.last_arrival_ns:
            age_ms = max(
                0.0,
                (now_ns - self.last_invalid_arrival_ns) / 1_000_000.0,
            )
            if age_ms > stale_after_ms:
                return {
                    **base,
                    "status": "stale",
                    "ready": False,
                    "reason": "VIO invalid-pose output exceeded the freshness limit",
                    "ageMs": age_ms,
                    "sampleCount": self.sample_count,
                }
            return {
                **base,
                "status": "degraded",
                "ready": False,
                "reason": "VIO is publishing invalid poses because visual tracking is lost",
                "ageMs": age_ms,
                "sampleCount": self.sample_count,
            }
        if not self.last_arrival_ns:
            return {**base, "status": "unavailable", "ready": False, "reason": "VIO output has not been observed"}
        age_ms = (now_ns - self.last_arrival_ns) / 1_000_000.0
        if age_ms > stale_after_ms:
            return {**base, "status": "stale", "ready": False, "reason": "VIO output exceeded the freshness limit", "ageMs": age_ms}
        if self.sample_count < self.required_initial_samples:
            return {
                **base,
                "status": "initializing",
                "ready": False,
                "reason": "VIO has not produced enough consecutive samples",
                "ageMs": age_ms,
                "sampleCount": self.sample_count,
            }
        reason = ""
        if transform_status not in ("verified", "configured_unverified"):
            reason = "camera-to-aircraft transform is unavailable"
        elif self.last_invalid_arrival_ns and now_ns - self.last_invalid_arrival_ns <= 1_000_000_000:
            reason = "a recent VIO sample was invalid"
        elif transform_status != "verified":
            reason = "camera-to-aircraft transform is configured but not physically verified"
        if reason:
            return {**base, "status": "degraded", "ready": False, "reason": reason, "ageMs": age_ms, "sampleCount": self.sample_count}
        return {**base, "status": "ready", "ready": True, "reason": "", "ageMs": age_ms, "sampleCount": self.sample_count}


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
    imu_required: bool = True
    calibration_digest: str = ""
    last_error: str = ""
    started_monotonic_ns: int = field(default_factory=time.monotonic_ns)
    color: StreamWindow = field(default_factory=StreamWindow)
    depth: StreamWindow = field(default_factory=StreamWindow)
    imu: ImuHealthState = field(default_factory=ImuHealthState)

    def set_calibration(self, values: Iterable[float]) -> None:
        self.calibration_digest = calibration_hash(values)

    def provider_stream_ages_ms(self, now_ns: Optional[int] = None) -> dict[str, Optional[float]]:
        """Return arrival ages used to supervise the camera process boundary."""
        now_ns = now_ns or time.monotonic_ns()
        streams = {"color": self.color.arrivals_ns, "depth": self.depth.arrivals_ns}
        if self.imu_required:
            streams["imu"] = self.imu.arrivals_ns
        return {
            name: (now_ns - arrivals[-1]) / 1_000_000.0 if arrivals else None
            for name, arrivals in streams.items()
        }

    def provider_streams_live(self, now_ns: Optional[int] = None, stale_after_ms: float = 1000.0) -> bool:
        ages = self.provider_stream_ages_ms(now_ns)
        return bool(ages) and all(
            age is not None and _finite_non_negative(age) and age <= stale_after_ms
            for age in ages.values()
        )

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
        imu = self.imu.snapshot(now_ns=now_ns)
        ready = streams_ready and fresh and synchronized and not self.last_error and bool(self.calibration_digest) and (not self.imu_required or imu["ready"])
        status = "ready" if ready else "degraded"
        if self.imu_required and imu["status"] in ("unavailable", "stale"):
            status = imu["status"]
        elif streams_ready and not fresh:
            status = "stale"
        return {
            "protocolVersion": PROTOCOL_VERSION,
            "status": status,
            "ready": ready,
            "provider": self.provider,
            "sourceId": self.source_id,
            "device": {
                "id": self.device_id,
                "model": self.model,
                "connection": self.usb_transport,
            },
            "capabilities": {"color": True, "depth": True, "imu": bool(self.imu.arrivals_ns)},
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
                "imu": imu,
            },
            "lastError": self.last_error,
        }


def validate_probe_request(raw: bytes) -> None:
    if len(raw) > 4096:
        raise ValueError("probe request exceeds 4096 bytes")
    request = json.loads(raw.decode("utf-8"))
    if request.get("protocolVersion") != PROTOCOL_VERSION or request.get("type") != "probe":
        raise ValueError("unsupported spatial probe request")
