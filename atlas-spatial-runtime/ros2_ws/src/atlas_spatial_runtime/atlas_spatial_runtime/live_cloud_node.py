"""ROS node that accumulates aligned depth in the live VIO-local frame."""

from __future__ import annotations

from collections import deque

import numpy as np
import rclpy
from nav_msgs.msg import Odometry
from rclpy.node import Node
from rclpy.qos import qos_profile_sensor_data
from sensor_msgs.msg import CameraInfo, Image, PointCloud2, PointField

from .live_cloud import (
    BoundedVoxelCloud,
    CameraModel,
    PoseBuffer,
    PoseSample,
    pose_transform,
    project_depth,
    resolve_transform,
)
from .transform_contract import load_transform_bundle


def _stamp_ns(stamp) -> int:
    return int(stamp.sec) * 1_000_000_000 + int(stamp.nanosec)


class LiveCloudNode(Node):
    def __init__(self) -> None:
        super().__init__("atlas_live_cloud")
        self.declare_parameter("transform_bundle_path", "")
        self.declare_parameter("voxel_size_m", 0.05)
        self.declare_parameter("maximum_points", 100_000)
        self.declare_parameter("pixel_stride", 4)
        self.declare_parameter("depth_min_m", 0.2)
        self.declare_parameter("depth_max_m", 8.0)
        self.declare_parameter("maximum_pose_skew_ms", 100.0)
        self.declare_parameter("publish_hz", 2.0)

        transform_path = str(self.get_parameter("transform_bundle_path").value)
        if not transform_path:
            raise ValueError("transform_bundle_path is required")
        self._transforms = load_transform_bundle(transform_path)
        self._pixel_stride = int(self.get_parameter("pixel_stride").value)
        self._depth_min_m = float(self.get_parameter("depth_min_m").value)
        self._depth_max_m = float(self.get_parameter("depth_max_m").value)
        self._maximum_pose_skew_ns = int(
            float(self.get_parameter("maximum_pose_skew_ms").value) * 1_000_000
        )
        publish_hz = float(self.get_parameter("publish_hz").value)
        if publish_hz <= 0:
            raise ValueError("publish_hz must be positive")

        self._cloud = BoundedVoxelCloud(
            float(self.get_parameter("voxel_size_m").value),
            int(self.get_parameter("maximum_points").value),
        )
        self._poses = PoseBuffer()
        self._camera: CameraModel | None = None
        self._pending_depth: deque[Image] = deque(maxlen=8)
        self._output_frame_id = ""
        self._last_cloud_stamp = None
        self._warned: set[str] = set()

        self._publisher = self.create_publisher(
            PointCloud2, "/atlas/spatial/map/points", qos_profile_sensor_data
        )
        self.create_subscription(
            CameraInfo,
            "/atlas/spatial/aligned_depth/camera_info",
            self._on_camera_info,
            qos_profile_sensor_data,
        )
        self.create_subscription(
            Image,
            "/atlas/spatial/aligned_depth/image_rect",
            self._on_depth,
            qos_profile_sensor_data,
        )
        self.create_subscription(
            Odometry,
            "/atlas/spatial/vio/odometry",
            self._on_odometry,
            qos_profile_sensor_data,
        )
        self.create_timer(1.0 / publish_hz, self._publish)

    def _on_camera_info(self, message: CameraInfo) -> None:
        try:
            model = CameraModel(
                frame_id=message.header.frame_id,
                width=int(message.width),
                height=int(message.height),
                # image_rect is rectified, so its projection matrix is the
                # authoritative pinhole model (K describes the raw image).
                fx=float(message.p[0]),
                fy=float(message.p[5]),
                cx=float(message.p[2]),
                cy=float(message.p[6]),
            ).validate()
        except (IndexError, ValueError) as error:
            self._warn_once("camera_info", str(error))
            return
        if self._camera is not None and model != self._camera:
            self._reset_cloud("aligned-depth calibration changed")
        self._camera = model

    def _on_odometry(self, message: Odometry) -> None:
        pose = message.pose.pose
        try:
            changed = self._poses.add(
                PoseSample(
                    capture_ns=_stamp_ns(message.header.stamp),
                    frame_id=message.header.frame_id,
                    child_frame_id=message.child_frame_id,
                    position_m=(
                        float(pose.position.x),
                        float(pose.position.y),
                        float(pose.position.z),
                    ),
                    orientation_wxyz=(
                        float(pose.orientation.w),
                        float(pose.orientation.x),
                        float(pose.orientation.y),
                        float(pose.orientation.z),
                    ),
                )
            )
        except ValueError as error:
            self._warn_once("odometry", str(error))
            return
        if changed:
            self._reset_cloud("VIO timestamp or coordinate frame changed")
        self._drain_depth()

    def _on_depth(self, message: Image) -> None:
        if message.encoding != "32FC1":
            self._warn_once("depth_encoding", f"depth encoding must be 32FC1, got {message.encoding}")
            return
        self._pending_depth.append(message)
        self._drain_depth()

    def _drain_depth(self) -> None:
        latest_pose_ns = self._poses.latest_capture_ns
        if latest_pose_ns is None or self._camera is None:
            return
        while self._pending_depth and _stamp_ns(self._pending_depth[0].header.stamp) <= latest_pose_ns:
            depth = self._pending_depth.popleft()
            capture_ns = _stamp_ns(depth.header.stamp)
            if capture_ns <= 0:
                self._warn_once("depth_timestamp", "depth capture timestamp must be positive")
                continue
            pose = self._poses.nearest(capture_ns, self._maximum_pose_skew_ns)
            if pose is None:
                self._warn_once("pose_skew", "depth frame has no capture-time VIO pose within the allowed skew")
                continue
            if depth.header.frame_id != self._camera.frame_id:
                self._warn_once("depth_frame", "depth image and CameraInfo frame IDs do not match")
                continue
            try:
                depth_values = self._decode_depth(depth)
                optical_points = project_depth(
                    depth_values,
                    self._camera,
                    pixel_stride=self._pixel_stride,
                    depth_min_m=self._depth_min_m,
                    depth_max_m=self._depth_max_m,
                )
                optical_to_vio_child = resolve_transform(
                    self._transforms, self._camera.frame_id, pose.child_frame_id
                )
                child_points = optical_to_vio_child.apply(optical_points)
                local_points = pose_transform(pose).apply(child_points)
            except ValueError as error:
                self._warn_once("projection", str(error))
                continue
            if self._output_frame_id and self._output_frame_id != pose.frame_id:
                self._reset_cloud("VIO output frame changed")
            self._output_frame_id = pose.frame_id
            self._cloud.integrate(local_points)
            self._last_cloud_stamp = depth.header.stamp

    @staticmethod
    def _decode_depth(message: Image) -> np.ndarray:
        if message.width <= 0 or message.height <= 0 or message.step < message.width * 4:
            raise ValueError("depth image layout is invalid")
        payload = bytes(message.data)
        if len(payload) < message.step * message.height:
            raise ValueError("depth image payload is truncated")
        dtype = np.dtype(">f4" if message.is_bigendian else "<f4")
        return np.ndarray(
            shape=(message.height, message.width),
            dtype=dtype,
            buffer=payload,
            strides=(message.step, 4),
        )

    def _publish(self) -> None:
        if not self._output_frame_id or self._last_cloud_stamp is None or len(self._cloud) == 0:
            return
        points = self._cloud.snapshot().astype("<f4", copy=False)
        message = PointCloud2()
        message.header.stamp = self._last_cloud_stamp
        message.header.frame_id = self._output_frame_id
        message.height = 1
        message.width = len(points)
        message.fields = [
            PointField(name="x", offset=0, datatype=PointField.FLOAT32, count=1),
            PointField(name="y", offset=4, datatype=PointField.FLOAT32, count=1),
            PointField(name="z", offset=8, datatype=PointField.FLOAT32, count=1),
        ]
        message.is_bigendian = False
        message.point_step = 12
        message.row_step = message.point_step * message.width
        message.data = points.tobytes(order="C")
        message.is_dense = True
        self._publisher.publish(message)

    def _reset_cloud(self, reason: str) -> None:
        self._cloud.clear()
        self._pending_depth.clear()
        self._output_frame_id = ""
        self._last_cloud_stamp = None
        self.get_logger().warning(f"live cloud reset: {reason}")

    def _warn_once(self, key: str, message: str) -> None:
        if key in self._warned:
            return
        self._warned.add(key)
        self.get_logger().warning(message)


def main() -> None:
    rclpy.init()
    node = LiveCloudNode()
    try:
        rclpy.spin(node)
    except KeyboardInterrupt:
        pass
    finally:
        node.destroy_node()
        if rclpy.ok():
            rclpy.shutdown()


if __name__ == "__main__":
    main()
