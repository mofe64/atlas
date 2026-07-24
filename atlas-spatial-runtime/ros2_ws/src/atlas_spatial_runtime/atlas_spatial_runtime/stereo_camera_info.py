"""Normalize DepthAI's mono calibration into the ROS stereo convention."""

from __future__ import annotations

from copy import deepcopy
import math

import rclpy
from rclpy.node import Node
from rclpy.qos import qos_profile_sensor_data
from sensor_msgs.msg import CameraInfo


def derive_baseline_m(source: CameraInfo) -> float:
    """Return the unsigned calibrated baseline encoded in a projection matrix."""
    if source.width <= 0 or source.height <= 0 or not source.header.frame_id:
        raise ValueError("stereo CameraInfo dimensions and frame ID are required")
    focal_x = float(source.p[0])
    projection_tx = float(source.p[3])
    if not math.isfinite(focal_x) or focal_x <= 0:
        raise ValueError("stereo CameraInfo focal length must be finite and positive")
    if not math.isfinite(projection_tx) or abs(projection_tx) <= 1e-6:
        raise ValueError(
            "DepthAI first-camera projection must encode a nonzero stereo baseline"
        )
    baseline_m = abs(projection_tx / focal_x)
    if not math.isfinite(baseline_m) or baseline_m <= 0:
        raise ValueError("stereo baseline must be finite and positive")
    return baseline_m


def normalize_camera_info(
    source: CameraInfo, *, is_left: bool, baseline_m: float
) -> CameraInfo:
    """Return calibration for a physical ROS left or right rectified image."""
    if not math.isfinite(baseline_m) or baseline_m <= 0:
        raise ValueError("stereo baseline must be finite and positive")
    if source.width <= 0 or source.height <= 0 or not source.header.frame_id:
        raise ValueError("stereo CameraInfo dimensions and frame ID are required")
    focal_x = float(source.p[0])
    if not math.isfinite(focal_x) or focal_x <= 0:
        raise ValueError("stereo CameraInfo focal length must be finite and positive")

    output = deepcopy(source)
    # ROS/OpenCV defines the physical left projection as Tx=0 and the physical
    # right projection as Tx=-fx*baseline, so -Tx/fx is strictly positive.
    output.p[3] = 0.0 if is_left else -focal_x * baseline_m
    return output


class StereoCameraInfoNormalizer(Node):
    """Publish CameraInfo matching Atlas's swapped physical mono image routes."""

    def __init__(self) -> None:
        super().__init__("atlas_spatial_stereo_camera_info")
        self._baseline_m: float | None = None
        self._pending_right: CameraInfo | None = None
        self._left_publisher = self.create_publisher(
            CameraInfo,
            "/atlas/spatial/provider/left/camera_info",
            qos_profile_sensor_data,
        )
        self._right_publisher = self.create_publisher(
            CameraInfo,
            "/atlas/spatial/provider/right/camera_info",
            qos_profile_sensor_data,
        )
        self.create_subscription(
            CameraInfo,
            "/atlas/spatial/provider/left/camera_info_depthai",
            self._handle_left,
            qos_profile_sensor_data,
        )
        self.create_subscription(
            CameraInfo,
            "/atlas/spatial/provider/right/camera_info_depthai",
            self._handle_right,
            qos_profile_sensor_data,
        )

    def _handle_left(self, message: CameraInfo) -> None:
        candidate = derive_baseline_m(message)
        if self._baseline_m is None:
            self._baseline_m = candidate
            self.get_logger().info(
                f"normalized DepthAI stereo baseline: {candidate:.6f} m"
            )
        elif abs(candidate - self._baseline_m) > max(
            0.0001, self._baseline_m * 0.01
        ):
            raise RuntimeError(
                "DepthAI stereo baseline changed within one provider epoch"
            )

        self._left_publisher.publish(
            normalize_camera_info(
                message, is_left=True, baseline_m=self._baseline_m
            )
        )
        if self._pending_right is not None:
            self._right_publisher.publish(
                normalize_camera_info(
                    self._pending_right,
                    is_left=False,
                    baseline_m=self._baseline_m,
                )
            )
            self._pending_right = None

    def _handle_right(self, message: CameraInfo) -> None:
        if self._baseline_m is None:
            self._pending_right = deepcopy(message)
            return
        self._right_publisher.publish(
            normalize_camera_info(
                message, is_left=False, baseline_m=self._baseline_m
            )
        )


def main() -> None:
    rclpy.init()
    node = StereoCameraInfoNormalizer()
    try:
        rclpy.spin(node)
    except KeyboardInterrupt:
        pass
    finally:
        node.destroy_node()
        if rclpy.ok():
            rclpy.shutdown()
