import unittest

from sensor_msgs.msg import CameraInfo

from atlas_spatial_runtime.stereo_camera_info import (
    derive_baseline_m,
    normalize_camera_info,
)


def camera_info(*, frame_id: str, focal_x: float, projection_tx: float) -> CameraInfo:
    message = CameraInfo()
    message.header.frame_id = frame_id
    message.header.stamp.sec = 17
    message.width = 640
    message.height = 400
    message.p[0] = focal_x
    message.p[2] = 320.0
    message.p[3] = projection_tx
    message.p[5] = focal_x
    message.p[6] = 200.0
    message.p[10] = 1.0
    return message


class StereoCameraInfoTests(unittest.TestCase):
    def test_derives_unsigned_baseline_from_depthai_first_camera_projection(self):
        message = camera_info(
            frame_id="camera_infra2_optical_frame",
            focal_x=457.2,
            projection_tx=-34.15284,
        )

        self.assertAlmostEqual(derive_baseline_m(message), 0.0747, places=6)

    def test_physical_left_projection_is_zero_without_mutating_source(self):
        source = camera_info(
            frame_id="camera_infra2_optical_frame",
            focal_x=457.2,
            projection_tx=-34.15284,
        )

        output = normalize_camera_info(source, is_left=True, baseline_m=0.0747)

        self.assertEqual(output.header.frame_id, "camera_infra2_optical_frame")
        self.assertEqual(output.header.stamp.sec, 17)
        self.assertEqual(output.p[3], 0.0)
        self.assertEqual(source.p[3], -34.15284)

    def test_physical_right_projection_encodes_positive_ros_baseline(self):
        source = camera_info(
            frame_id="camera_infra1_optical_frame",
            focal_x=457.0,
            projection_tx=0.0,
        )

        output = normalize_camera_info(
            source, is_left=False, baseline_m=0.0747
        )

        self.assertAlmostEqual(-output.p[3] / output.p[0], 0.0747, places=7)
        self.assertLess(output.p[3], 0.0)
        self.assertEqual(source.p[3], 0.0)

    def test_missing_depthai_baseline_is_rejected(self):
        source = camera_info(
            frame_id="camera_infra2_optical_frame",
            focal_x=457.2,
            projection_tx=0.0,
        )

        with self.assertRaisesRegex(ValueError, "nonzero stereo baseline"):
            derive_baseline_m(source)


if __name__ == "__main__":
    unittest.main()
