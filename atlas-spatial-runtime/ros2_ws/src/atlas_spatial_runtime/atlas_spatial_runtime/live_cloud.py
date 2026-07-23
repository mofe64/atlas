"""Bounded live point-cloud geometry independent of ROS."""

from __future__ import annotations

from collections import OrderedDict, deque
from dataclasses import dataclass
import math
from typing import Iterable

import numpy as np


Vector3 = tuple[float, float, float]
Quaternion = tuple[float, float, float, float]


@dataclass(frozen=True)
class CameraModel:
    frame_id: str
    width: int
    height: int
    fx: float
    fy: float
    cx: float
    cy: float

    def validate(self) -> "CameraModel":
        if not self.frame_id:
            raise ValueError("camera frame_id is required")
        if self.width <= 0 or self.height <= 0:
            raise ValueError("camera dimensions must be positive")
        if not all(math.isfinite(value) for value in (self.fx, self.fy, self.cx, self.cy)):
            raise ValueError("camera intrinsics must be finite")
        if self.fx <= 0 or self.fy <= 0:
            raise ValueError("camera focal lengths must be positive")
        return self


@dataclass(frozen=True)
class PoseSample:
    capture_ns: int
    frame_id: str
    child_frame_id: str
    position_m: Vector3
    orientation_wxyz: Quaternion

    def validate(self) -> "PoseSample":
        if self.capture_ns <= 0:
            raise ValueError("pose capture timestamp must be positive")
        if not self.frame_id or not self.child_frame_id:
            raise ValueError("pose frame IDs are required")
        if not all(math.isfinite(value) for value in self.position_m):
            raise ValueError("pose position must be finite")
        _normalized_quaternion(self.orientation_wxyz, "pose")
        return self


@dataclass(frozen=True)
class RigidTransform:
    rotation_wxyz: Quaternion
    translation_m: Vector3

    def apply(self, points: np.ndarray) -> np.ndarray:
        points = _points_array(points)
        rotation = _rotation_matrix(_normalized_quaternion(self.rotation_wxyz, "transform"))
        translation = np.asarray(self.translation_m, dtype=np.float64)
        if translation.shape != (3,) or not np.isfinite(translation).all():
            raise ValueError("transform translation must contain three finite values")
        return points @ rotation.T + translation


class PoseBuffer:
    """Small monotonic VIO history used to match depth at capture time."""

    def __init__(self, capacity: int = 60) -> None:
        if capacity <= 1:
            raise ValueError("pose buffer capacity must be greater than one")
        self._samples: deque[PoseSample] = deque(maxlen=capacity)

    def add(self, sample: PoseSample) -> bool:
        """Add a pose and return whether the VIO coordinate epoch changed."""
        sample.validate()
        changed = bool(self._samples) and (
            sample.capture_ns <= self._samples[-1].capture_ns
            or sample.frame_id != self._samples[-1].frame_id
            or sample.child_frame_id != self._samples[-1].child_frame_id
        )
        if changed:
            self._samples.clear()
        self._samples.append(sample)
        return changed

    @property
    def latest_capture_ns(self) -> int | None:
        return self._samples[-1].capture_ns if self._samples else None

    def nearest(self, capture_ns: int, maximum_skew_ns: int) -> PoseSample | None:
        if capture_ns <= 0 or maximum_skew_ns <= 0:
            raise ValueError("capture timestamp and maximum skew must be positive")
        if not self._samples:
            return None
        sample = min(self._samples, key=lambda item: abs(item.capture_ns - capture_ns))
        if abs(sample.capture_ns - capture_ns) > maximum_skew_ns:
            return None
        return sample


class BoundedVoxelCloud:
    """Voxel-downsampled cloud with least-recently-observed eviction."""

    def __init__(self, voxel_size_m: float, maximum_points: int) -> None:
        if not math.isfinite(voxel_size_m) or voxel_size_m <= 0:
            raise ValueError("voxel_size_m must be positive")
        if maximum_points <= 0:
            raise ValueError("maximum_points must be positive")
        self.voxel_size_m = voxel_size_m
        self.maximum_points = maximum_points
        self._voxels: OrderedDict[tuple[int, int, int], np.ndarray] = OrderedDict()

    def clear(self) -> None:
        self._voxels.clear()

    def __len__(self) -> int:
        return len(self._voxels)

    def integrate(self, points: np.ndarray) -> None:
        points = _points_array(points)
        if points.size == 0:
            return
        finite = points[np.isfinite(points).all(axis=1)]
        keys = np.floor(finite / self.voxel_size_m).astype(np.int64)
        for key_values, point in zip(keys, finite, strict=True):
            key = tuple(int(value) for value in key_values)
            if key in self._voxels:
                self._voxels[key] = point.astype(np.float32, copy=True)
                self._voxels.move_to_end(key)
                continue
            if len(self._voxels) >= self.maximum_points:
                self._voxels.popitem(last=False)
            self._voxels[key] = point.astype(np.float32, copy=True)

    def snapshot(self) -> np.ndarray:
        if not self._voxels:
            return np.empty((0, 3), dtype=np.float32)
        return np.stack(tuple(self._voxels.values())).astype(np.float32, copy=False)


def project_depth(
    depth_m: np.ndarray,
    camera: CameraModel,
    *,
    pixel_stride: int,
    depth_min_m: float,
    depth_max_m: float,
) -> np.ndarray:
    camera.validate()
    if pixel_stride <= 0:
        raise ValueError("pixel_stride must be positive")
    if (
        not math.isfinite(depth_min_m)
        or not math.isfinite(depth_max_m)
        or depth_min_m <= 0
        or depth_max_m <= depth_min_m
    ):
        raise ValueError("depth range must be finite, positive, and increasing")
    depth = np.asarray(depth_m)
    if depth.shape != (camera.height, camera.width):
        raise ValueError("depth dimensions do not match camera calibration")
    sampled = depth[::pixel_stride, ::pixel_stride].astype(np.float64, copy=False)
    rows = np.arange(0, camera.height, pixel_stride, dtype=np.float64)[:, None]
    columns = np.arange(0, camera.width, pixel_stride, dtype=np.float64)[None, :]
    valid = np.isfinite(sampled) & (sampled >= depth_min_m) & (sampled <= depth_max_m)
    if not valid.any():
        return np.empty((0, 3), dtype=np.float64)
    z = sampled[valid]
    x = np.broadcast_to((columns - camera.cx) / camera.fx, sampled.shape)[valid] * z
    y = np.broadcast_to((rows - camera.cy) / camera.fy, sampled.shape)[valid] * z
    return np.column_stack((x, y, z))


def resolve_transform(bundle: dict, source_frame: str, target_frame: str) -> RigidTransform:
    """Resolve a configured transform from source-frame points to target frame."""
    if not source_frame or not target_frame:
        raise ValueError("source and target frame IDs are required")
    identity = RigidTransform((1.0, 0.0, 0.0, 0.0), (0.0, 0.0, 0.0))
    if source_frame == target_frame:
        return identity
    graph: dict[str, list[tuple[str, RigidTransform]]] = {}
    for item in bundle.get("transforms", ()):
        if item.get("status") == "unmeasured":
            continue
        parent, child = item.get("parentFrame"), item.get("childFrame")
        rotation, translation = item.get("rotationWXYZ"), item.get("translationM")
        if not isinstance(parent, str) or not isinstance(child, str):
            continue
        if not isinstance(rotation, dict) or not isinstance(translation, dict):
            continue
        forward = RigidTransform(
            tuple(float(rotation[key]) for key in ("w", "x", "y", "z")),
            tuple(float(translation[key]) for key in ("x", "y", "z")),
        )
        graph.setdefault(child, []).append((parent, forward))
        graph.setdefault(parent, []).append((child, _inverse(forward)))
    pending = deque([(source_frame, identity)])
    visited = {source_frame}
    while pending:
        frame, accumulated = pending.popleft()
        for next_frame, edge in graph.get(frame, ()):
            if next_frame in visited:
                continue
            composed = _compose(edge, accumulated)
            if next_frame == target_frame:
                return composed
            visited.add(next_frame)
            pending.append((next_frame, composed))
    raise ValueError(f"no configured transform from {source_frame} to {target_frame}")


def pose_transform(sample: PoseSample) -> RigidTransform:
    sample.validate()
    return RigidTransform(sample.orientation_wxyz, sample.position_m)


def _points_array(points: np.ndarray | Iterable[Vector3]) -> np.ndarray:
    result = np.asarray(points, dtype=np.float64)
    if result.size == 0:
        return np.empty((0, 3), dtype=np.float64)
    if result.ndim != 2 or result.shape[1] != 3:
        raise ValueError("points must have shape (N, 3)")
    return result


def _normalized_quaternion(value: Quaternion, name: str) -> Quaternion:
    if len(value) != 4 or not all(math.isfinite(float(component)) for component in value):
        raise ValueError(f"{name} quaternion is invalid")
    norm = math.sqrt(sum(float(component) ** 2 for component in value))
    if norm < 1e-9 or abs(norm - 1.0) > 1e-3:
        raise ValueError(f"{name} quaternion is not normalized")
    return tuple(float(component) / norm for component in value)  # type: ignore[return-value]


def _rotation_matrix(value: Quaternion) -> np.ndarray:
    w, x, y, z = value
    return np.asarray(
        [
            [1 - 2 * (y * y + z * z), 2 * (x * y - z * w), 2 * (x * z + y * w)],
            [2 * (x * y + z * w), 1 - 2 * (x * x + z * z), 2 * (y * z - x * w)],
            [2 * (x * z - y * w), 2 * (y * z + x * w), 1 - 2 * (x * x + y * y)],
        ],
        dtype=np.float64,
    )


def _compose(outer: RigidTransform, inner: RigidTransform) -> RigidTransform:
    rotation = _quaternion_multiply(outer.rotation_wxyz, inner.rotation_wxyz)
    translation = outer.apply(np.asarray([inner.translation_m], dtype=np.float64))[0]
    return RigidTransform(rotation, tuple(float(value) for value in translation))


def _inverse(transform: RigidTransform) -> RigidTransform:
    w, x, y, z = _normalized_quaternion(transform.rotation_wxyz, "transform")
    inverse_rotation = (w, -x, -y, -z)
    rotation = _rotation_matrix(inverse_rotation)
    translation = -(rotation @ np.asarray(transform.translation_m, dtype=np.float64))
    return RigidTransform(inverse_rotation, tuple(float(value) for value in translation))


def _quaternion_multiply(left: Quaternion, right: Quaternion) -> Quaternion:
    lw, lx, ly, lz = left
    rw, rx, ry, rz = right
    return (
        lw * rw - lx * rx - ly * ry - lz * rz,
        lw * rx + lx * rw + ly * rz - lz * ry,
        lw * ry - lx * rz + ly * rw + lz * rx,
        lw * rz + lx * ry - ly * rx + lz * rw,
    )
