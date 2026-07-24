import json
from pathlib import Path
import struct
import unittest

from sensor_msgs.msg import CameraInfo, Image

from atlas_spatial_runtime.depth_normalizer import (
    NORMALIZED_ALIGNED_DEPTH_FRAME_ID,
    normalize_camera_info,
    normalize_depth,
)


class DepthNormalizerTest(unittest.TestCase):
    def test_normalized_frame_is_declared_by_transform_contract(self):
        config_path = Path(__file__).parents[1] / "config" / "transforms.v1.json"
        bundle = json.loads(config_path.read_text(encoding="utf-8"))

        self.assertTrue(
            any(
                transform["childFrame"] == NORMALIZED_ALIGNED_DEPTH_FRAME_ID
                for transform in bundle["transforms"]
            )
        )

    def test_depth_payload_and_frame_are_normalized_together(self):
        source = Image()
        source.header.stamp.sec = 17
        source.header.frame_id = "camera_color_optical_frame"
        source.width = 2
        source.height = 1
        source.encoding = "16UC1"
        source.is_bigendian = False
        source.step = 4
        source.data = struct.pack("<HH", 1_000, 2_500)

        output = normalize_depth(source, NORMALIZED_ALIGNED_DEPTH_FRAME_ID)

        self.assertEqual(output.header.stamp.sec, 17)
        self.assertEqual(output.header.frame_id, "oak_rgb_camera_optical_frame")
        self.assertEqual(output.encoding, "32FC1")
        self.assertEqual(output.step, 8)
        self.assertEqual(struct.unpack("<ff", bytes(output.data)), (1.0, 2.5))
        self.assertEqual(source.header.frame_id, "camera_color_optical_frame")

    def test_camera_calibration_uses_the_same_normalized_frame(self):
        source = CameraInfo()
        source.header.stamp.nanosec = 42
        source.header.frame_id = "camera_color_optical_frame"
        source.width = 640
        source.height = 400
        source.p[0] = 500.0

        output = normalize_camera_info(source, NORMALIZED_ALIGNED_DEPTH_FRAME_ID)

        self.assertEqual(output.header.stamp.nanosec, 42)
        self.assertEqual(output.header.frame_id, "oak_rgb_camera_optical_frame")
        self.assertEqual(output.width, 640)
        self.assertEqual(output.height, 400)
        self.assertEqual(output.p[0], 500.0)
        self.assertEqual(source.header.frame_id, "camera_color_optical_frame")

    def test_non_millimetre_provider_depth_is_rejected(self):
        source = Image()
        source.encoding = "32FC1"

        with self.assertRaisesRegex(ValueError, "must be 16UC1"):
            normalize_depth(source, NORMALIZED_ALIGNED_DEPTH_FRAME_ID)


if __name__ == "__main__":
    unittest.main()
