"""Depth projection and direct sensor-to-body geometry.

There is deliberately no transform graph or persistent map here. An obstacle
consumer projects one fresh depth frame and applies one configured camera-to-
body extrinsic.
"""

from __future__ import annotations

from dataclasses import dataclass
import math

import numpy as np


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
        if not 0 <= self.cx < self.width or not 0 <= self.cy < self.height:
            raise ValueError("camera principal point is outside the image")
        return self


@dataclass(frozen=True)
class SensorExtrinsic:
    """A direct transform from sensor-frame points to aircraft body FRD."""

    rotation_wxyz: tuple[float, float, float, float]
    translation_m: tuple[float, float, float]

    def apply(self, points: np.ndarray) -> np.ndarray:
        points = _points_array(points)
        rotation = _rotation_matrix(_normalized_quaternion(self.rotation_wxyz))
        translation = np.asarray(self.translation_m, dtype=np.float64)
        if translation.shape != (3,) or not np.isfinite(translation).all():
            raise ValueError("extrinsic translation must contain three finite values")
        return points @ rotation.T + translation


def project_depth_millimetres(
    depth_mm: np.ndarray,
    camera: CameraModel,
    *,
    pixel_stride: int,
    depth_min_m: float,
    depth_max_m: float,
) -> np.ndarray:
    """Project sampled native depth without converting the full frame."""

    if pixel_stride <= 0:
        raise ValueError("pixel_stride must be positive")
    source = np.asarray(depth_mm)
    if source.dtype != np.uint16:
        raise ValueError("native depth must use uint16 millimetres")
    sampled_mm = source[::pixel_stride, ::pixel_stride]
    sampled_m = sampled_mm.astype(np.float64) * 0.001
    sampled_m[sampled_mm == 0] = np.nan
    return _project_sampled(
        sampled_m,
        camera,
        pixel_stride=pixel_stride,
        depth_min_m=depth_min_m,
        depth_max_m=depth_max_m,
        original_shape=source.shape,
    )


def project_depth(
    depth_m: np.ndarray,
    camera: CameraModel,
    *,
    pixel_stride: int,
    depth_min_m: float,
    depth_max_m: float,
) -> np.ndarray:
    """Project a float depth array supplied by a metres-based consumer."""

    if pixel_stride <= 0:
        raise ValueError("pixel_stride must be positive")
    source = np.asarray(depth_m)
    sampled = source[::pixel_stride, ::pixel_stride].astype(np.float64, copy=False)
    return _project_sampled(
        sampled,
        camera,
        pixel_stride=pixel_stride,
        depth_min_m=depth_min_m,
        depth_max_m=depth_max_m,
        original_shape=source.shape,
    )


def _project_sampled(
    sampled_m: np.ndarray,
    camera: CameraModel,
    *,
    pixel_stride: int,
    depth_min_m: float,
    depth_max_m: float,
    original_shape: tuple[int, ...],
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
    if original_shape != (camera.height, camera.width):
        raise ValueError("depth dimensions do not match camera calibration")
    rows = np.arange(0, camera.height, pixel_stride, dtype=np.float64)[:, None]
    columns = np.arange(0, camera.width, pixel_stride, dtype=np.float64)[None, :]
    valid = (
        np.isfinite(sampled_m)
        & (sampled_m >= depth_min_m)
        & (sampled_m <= depth_max_m)
    )
    if not valid.any():
        return np.empty((0, 3), dtype=np.float64)
    z = sampled_m[valid]
    x = np.broadcast_to((columns - camera.cx) / camera.fx, sampled_m.shape)[valid] * z
    y = np.broadcast_to((rows - camera.cy) / camera.fy, sampled_m.shape)[valid] * z
    return np.column_stack((x, y, z))


def _points_array(points: np.ndarray) -> np.ndarray:
    result = np.asarray(points, dtype=np.float64)
    if result.size == 0:
        return np.empty((0, 3), dtype=np.float64)
    if result.ndim != 2 or result.shape[1] != 3:
        raise ValueError("points must have shape (N, 3)")
    return result


def _normalized_quaternion(
    value: tuple[float, float, float, float],
) -> tuple[float, float, float, float]:
    if len(value) != 4 or not all(math.isfinite(float(component)) for component in value):
        raise ValueError("extrinsic quaternion is invalid")
    norm = math.sqrt(sum(float(component) ** 2 for component in value))
    if norm < 1e-9 or abs(norm - 1.0) > 1e-3:
        raise ValueError("extrinsic quaternion is not normalized")
    return tuple(float(component) / norm for component in value)  # type: ignore[return-value]


def _rotation_matrix(value: tuple[float, float, float, float]) -> np.ndarray:
    w, x, y, z = value
    return np.asarray(
        [
            [1 - 2 * (y * y + z * z), 2 * (x * y - w * z), 2 * (x * z + w * y)],
            [2 * (x * y + w * z), 1 - 2 * (x * x + z * z), 2 * (y * z - w * x)],
            [2 * (x * z - w * y), 2 * (y * z + w * x), 1 - 2 * (x * x + y * y)],
        ],
        dtype=np.float64,
    )
