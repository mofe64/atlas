"""Deterministic provider used by source tests and service smoke tests."""

from __future__ import annotations

import time

import numpy as np

from .provider import CameraCalibration, DepthFrame, ProviderInfo, host_arrival_ns


class SyntheticProvider:
    def __init__(
        self,
        *,
        width: int,
        height: int,
        fps: float,
        frame_id: str,
    ) -> None:
        self._width = width
        self._height = height
        self._period_ns = int(1_000_000_000 / fps)
        self._frame_id = frame_id
        self._next_ns = 0
        self._info = ProviderInfo("synthetic", "synthetic", "synthetic", "memory")

    @property
    def info(self) -> ProviderInfo:
        return self._info

    def start(self) -> None:
        self._next_ns = time.monotonic_ns()

    def try_read(self) -> DepthFrame | None:
        now = time.monotonic_ns()
        if now < self._next_ns:
            return None
        self._next_ns = now + self._period_ns
        calibration = CameraCalibration(
            frame_id=self._frame_id,
            width=self._width,
            height=self._height,
            fx=float(self._width),
            fy=float(self._width),
            cx=(self._width - 1) / 2.0,
            cy=(self._height - 1) / 2.0,
        )
        return DepthFrame(
            depth_mm=np.full((self._height, self._width), 2000, dtype=np.uint16),
            capture_monotonic_ns=now,
            arrival_monotonic_ns=host_arrival_ns(),
            calibration=calibration,
        )

    def close(self) -> None:
        return

