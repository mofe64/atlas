"""Provider-neutral depth frame types."""

from __future__ import annotations

from dataclasses import dataclass
import math
import time
from typing import Protocol

import numpy as np


@dataclass(frozen=True)
class CameraCalibration:
    frame_id: str
    width: int
    height: int
    fx: float
    fy: float
    cx: float
    cy: float
    distortion_model: str = "none"

    def validate(self) -> "CameraCalibration":
        if not self.frame_id:
            raise ValueError("camera frame_id is required")
        if self.width <= 0 or self.height <= 0:
            raise ValueError("camera dimensions must be positive")
        if not all(math.isfinite(value) for value in (self.fx, self.fy, self.cx, self.cy)):
            raise ValueError("camera intrinsics must be finite")
        if self.fx <= 0 or self.fy <= 0:
            raise ValueError("camera focal lengths must be positive")
        if not 0 <= self.cx < self.width or not 0 <= self.cy < self.height:
            raise ValueError("camera principal point is outside the image")
        return self


@dataclass(frozen=True)
class DepthFrame:
    """One calibrated depth image in the provider's native millimetre format."""

    depth_mm: np.ndarray
    capture_monotonic_ns: int
    arrival_monotonic_ns: int
    calibration: CameraCalibration

    def validate(self) -> "DepthFrame":
        self.calibration.validate()
        depth = np.asarray(self.depth_mm)
        if depth.dtype != np.uint16:
            raise ValueError("depth image must use uint16 millimetres")
        expected = (self.calibration.height, self.calibration.width)
        if depth.shape != expected:
            raise ValueError(
                f"depth dimensions {depth.shape} do not match calibration {expected}"
            )
        if self.capture_monotonic_ns <= 0 or self.arrival_monotonic_ns <= 0:
            raise ValueError("depth timestamps must be positive")
        return self


@dataclass(frozen=True)
class ProviderInfo:
    provider: str
    device_id: str
    model: str
    connection: str


class DepthProvider(Protocol):
    @property
    def info(self) -> ProviderInfo: ...

    def start(self) -> None: ...

    def try_read(self) -> DepthFrame | None: ...

    def close(self) -> None: ...


def host_arrival_ns() -> int:
    return time.monotonic_ns()

