"""Normalize provider depth from uint16 millimetres to float32 metres."""

from __future__ import annotations

import rclpy
from rclpy.node import Node
from rclpy.qos import qos_profile_sensor_data
from sensor_msgs.msg import Image

from .depth_contract import millimetres_to_float_metres


class DepthNormalizer(Node):
    def __init__(self) -> None:
        super().__init__("atlas_spatial_depth_normalizer")
        self.publisher = self.create_publisher(Image, "/atlas/spatial/aligned_depth/image_rect", qos_profile_sensor_data)
        self.create_subscription(Image, "/atlas/spatial/provider/depth_mm", self.convert, qos_profile_sensor_data)

    def convert(self, source: Image) -> None:
        if source.encoding != "16UC1":
            self.get_logger().error(f"provider depth encoding must be 16UC1, got {source.encoding}")
            return
        try:
            data, is_bigendian = millimetres_to_float_metres(
                bytes(source.data), source.width, source.height, source.step, bool(source.is_bigendian)
            )
        except ValueError as error:
            self.get_logger().error(str(error))
            return
        output = Image()
        output.header = source.header
        output.width = source.width
        output.height = source.height
        output.encoding = "32FC1"
        output.is_bigendian = is_bigendian
        output.step = source.width * 4
        output.data = data
        self.publisher.publish(output)


def main() -> None:
    rclpy.init()
    node = DepthNormalizer()
    try:
        rclpy.spin(node)
    finally:
        node.destroy_node()
        rclpy.shutdown()
