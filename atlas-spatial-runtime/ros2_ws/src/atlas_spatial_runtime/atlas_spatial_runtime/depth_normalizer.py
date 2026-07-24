"""Normalize provider depth from uint16 millimetres to float32 metres."""

from __future__ import annotations

from copy import deepcopy

import rclpy
from rclpy.node import Node
from rclpy.qos import qos_profile_sensor_data
from sensor_msgs.msg import CameraInfo, Image

from .depth_contract import millimetres_to_float_metres


NORMALIZED_ALIGNED_DEPTH_FRAME_ID = "oak_rgb_camera_optical_frame"


def normalize_depth(source: Image, output_frame_id: str) -> Image:
    if source.encoding != "16UC1":
        raise ValueError(f"provider depth encoding must be 16UC1, got {source.encoding}")
    data, is_bigendian = millimetres_to_float_metres(
        bytes(source.data), source.width, source.height, source.step, bool(source.is_bigendian)
    )
    output = Image()
    output.header = deepcopy(source.header)
    output.header.frame_id = output_frame_id
    output.width = source.width
    output.height = source.height
    output.encoding = "32FC1"
    output.is_bigendian = is_bigendian
    output.step = source.width * 4
    output.data = data
    return output


def normalize_camera_info(source: CameraInfo, output_frame_id: str) -> CameraInfo:
    output = deepcopy(source)
    output.header.frame_id = output_frame_id
    return output


class DepthNormalizer(Node):
    def __init__(self) -> None:
        super().__init__("atlas_spatial_depth_normalizer")
        self.declare_parameter("output_frame_id", NORMALIZED_ALIGNED_DEPTH_FRAME_ID)
        self._output_frame_id = str(self.get_parameter("output_frame_id").value)
        if not self._output_frame_id:
            raise ValueError("normalized aligned-depth frame ID is required")
        self.depth_publisher = self.create_publisher(
            Image, "/atlas/spatial/aligned_depth/image_rect", qos_profile_sensor_data
        )
        self.camera_info_publisher = self.create_publisher(
            CameraInfo, "/atlas/spatial/aligned_depth/camera_info", qos_profile_sensor_data
        )
        self.create_subscription(Image, "/atlas/spatial/provider/depth_mm", self.convert, qos_profile_sensor_data)
        self.create_subscription(
            CameraInfo,
            "/atlas/spatial/provider/aligned_depth/camera_info",
            self.convert_camera_info,
            qos_profile_sensor_data,
        )

    def convert(self, source: Image) -> None:
        try:
            output = normalize_depth(source, self._output_frame_id)
        except ValueError as error:
            self.get_logger().error(str(error))
            return
        self.depth_publisher.publish(output)

    def convert_camera_info(self, source: CameraInfo) -> None:
        self.camera_info_publisher.publish(
            normalize_camera_info(source, self._output_frame_id)
        )


def main() -> None:
    rclpy.init()
    node = DepthNormalizer()
    try:
        rclpy.spin(node)
    except KeyboardInterrupt:
        pass
    finally:
        node.destroy_node()
        if rclpy.ok():
            rclpy.shutdown()
