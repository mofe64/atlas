"""Deterministic RGB-D source for CI, replay development, and contract tests."""

from __future__ import annotations

from array import array

import rclpy
from rclpy.node import Node
from sensor_msgs.msg import CameraInfo, Image


class SyntheticProvider(Node):
    def __init__(self) -> None:
        super().__init__("atlas_spatial_synthetic_provider")
        self.declare_parameter("width", 64)
        self.declare_parameter("height", 48)
        self.declare_parameter("fps", 10.0)
        self.width = int(self.get_parameter("width").value)
        self.height = int(self.get_parameter("height").value)
        fps = float(self.get_parameter("fps").value)
        self.color = self.create_publisher(Image, "/atlas/spatial/color/image_raw", 10)
        self.color_info = self.create_publisher(CameraInfo, "/atlas/spatial/color/camera_info", 10)
        self.depth = self.create_publisher(Image, "/atlas/spatial/aligned_depth/image_rect", 10)
        self.depth_info = self.create_publisher(CameraInfo, "/atlas/spatial/aligned_depth/camera_info", 10)
        self.timer = self.create_timer(1.0 / fps, self.publish_bundle)

    def camera_info(self, stamp) -> CameraInfo:
        message = CameraInfo()
        message.header.stamp = stamp
        message.header.frame_id = "front_depth_color_optical_frame"
        message.width = self.width
        message.height = self.height
        focal = float(self.width)
        message.k = [focal, 0.0, self.width / 2.0, 0.0, focal, self.height / 2.0, 0.0, 0.0, 1.0]
        message.r = [1.0, 0.0, 0.0, 0.0, 1.0, 0.0, 0.0, 0.0, 1.0]
        message.p = [focal, 0.0, self.width / 2.0, 0.0, 0.0, focal, self.height / 2.0, 0.0, 0.0, 0.0, 1.0, 0.0]
        message.distortion_model = "plumb_bob"
        return message

    def publish_bundle(self) -> None:
        stamp = self.get_clock().now().to_msg()
        info = self.camera_info(stamp)

        color = Image()
        color.header = info.header
        color.width, color.height = self.width, self.height
        color.encoding = "rgb8"
        color.is_bigendian = False
        color.step = self.width * 3
        color.data = bytes([32, 96, 160]) * (self.width * self.height)

        depth = Image()
        depth.header = info.header
        depth.width, depth.height = self.width, self.height
        depth.encoding = "32FC1"
        depth.is_bigendian = False
        depth.step = self.width * 4
        depth.data = array("f", [2.0] * (self.width * self.height)).tobytes()

        self.color.publish(color)
        self.color_info.publish(info)
        self.depth.publish(depth)
        self.depth_info.publish(info)


def main() -> None:
    rclpy.init()
    node = SyntheticProvider()
    try:
        rclpy.spin(node)
    finally:
        node.destroy_node()
        rclpy.shutdown()
