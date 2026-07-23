"""Bounded framing and latest-only handoff for complete spatial snapshots."""

from __future__ import annotations

from dataclasses import dataclass
import json
import math
import struct
import threading


PROTOCOL_VERSION = "1"
MAXIMUM_POINTS = 100_000
BYTES_PER_POINT = 12
MAXIMUM_HEADER_BYTES = 16 * 1024


@dataclass(frozen=True)
class SpatialFrame:
    header: dict
    xyz_f32_le: bytes


class LatestSpatialFrame:
    """A one-slot mailbox: publishing replaces stale complete snapshots."""

    def __init__(self) -> None:
        self._condition = threading.Condition()
        self._revision = 0
        self._frame: SpatialFrame | None = None

    def publish(self, frame: SpatialFrame) -> int:
        validate_frame(frame)
        with self._condition:
            self._revision += 1
            self._frame = frame
            self._condition.notify_all()
            return self._revision

    def wait_after(
        self, revision: int, timeout: float | None = None
    ) -> tuple[int, SpatialFrame] | None:
        with self._condition:
            self._condition.wait_for(
                lambda: self._revision > revision and self._frame is not None,
                timeout=timeout,
            )
            if self._revision <= revision or self._frame is None:
                return None
            return self._revision, self._frame


def validate_frame(frame: SpatialFrame) -> None:
    point_count = frame.header.get("pointCount")
    if not isinstance(point_count, int) or not 0 < point_count <= MAXIMUM_POINTS:
        raise ValueError(f"pointCount must be between 1 and {MAXIMUM_POINTS}")
    if len(frame.xyz_f32_le) != point_count * BYTES_PER_POINT:
        raise ValueError("XYZ payload must contain exactly 12 bytes per point")
    for key in ("sourceId", "streamEpoch", "frameId"):
        value = frame.header.get(key)
        if not isinstance(value, str) or not value.strip():
            raise ValueError(f"{key} is required")
    if frame.header.get("protocolVersion") != PROTOCOL_VERSION:
        raise ValueError("unsupported spatial stream protocol")
    voxel_size = frame.header.get("voxelSizeM")
    if not isinstance(voxel_size, (int, float)) or not math.isfinite(voxel_size) or voxel_size <= 0:
        raise ValueError("voxelSizeM must be finite and positive")


def encode_frame(frame: SpatialFrame) -> bytes:
    validate_frame(frame)
    header = dict(frame.header)
    header["xyzByteLength"] = len(frame.xyz_f32_le)
    encoded_header = json.dumps(
        header, allow_nan=False, separators=(",", ":"), sort_keys=True
    ).encode("utf-8")
    if len(encoded_header) > MAXIMUM_HEADER_BYTES:
        raise ValueError("spatial frame header exceeds the bounded limit")
    return struct.pack(">I", len(encoded_header)) + encoded_header + frame.xyz_f32_le
