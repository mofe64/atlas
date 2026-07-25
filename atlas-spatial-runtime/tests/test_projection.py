import unittest

import numpy as np

from atlas_spatial_runtime.projection import (
    CameraModel,
    SensorExtrinsic,
    project_depth,
    project_depth_millimetres,
)


class ProjectionTests(unittest.TestCase):
    def test_native_depth_projects_without_full_frame_float_contract(self):
        camera = CameraModel("camera_optical", 2, 2, 1.0, 1.0, 0.0, 0.0)
        points = project_depth_millimetres(
            np.asarray([[1000, 2000], [0, 500]], dtype=np.uint16),
            camera,
            pixel_stride=1,
            depth_min_m=0.8,
            depth_max_m=1.5,
        )
        np.testing.assert_allclose(points, np.asarray([[0.0, 0.0, 1.0]]))

    def test_metres_projection_remains_available_at_consumer_edge(self):
        camera = CameraModel("camera_optical", 2, 2, 1.0, 1.0, 0.0, 0.0)
        points = project_depth(
            np.asarray([[1.0, 2.0], [0.0, np.nan]], dtype=np.float32),
            camera,
            pixel_stride=1,
            depth_min_m=0.2,
            depth_max_m=1.5,
        )
        np.testing.assert_allclose(points, np.asarray([[0.0, 0.0, 1.0]]))

    def test_ariadne_extrinsic_maps_camera_axes_into_body_frd(self):
        transform = SensorExtrinsic(
            rotation_wxyz=(0.5, 0.5, 0.5, 0.5),
            translation_m=(0.155, 0.01, 0.005),
        )
        body_points = transform.apply(
            np.asarray(
                [
                    [1.0, 0.0, 0.0],
                    [0.0, 1.0, 0.0],
                    [0.0, 0.0, 1.0],
                ]
            )
        )
        np.testing.assert_allclose(
            body_points,
            np.asarray(
                [
                    [0.155, 1.01, 0.005],
                    [0.155, 0.01, 1.005],
                    [1.155, 0.01, 0.005],
                ]
            ),
        )

    def test_extrinsic_rejects_non_unit_quaternion(self):
        transform = SensorExtrinsic(
            rotation_wxyz=(1.0, 1.0, 0.0, 0.0),
            translation_m=(0.0, 0.0, 0.0),
        )
        with self.assertRaisesRegex(ValueError, "not normalized"):
            transform.apply(np.asarray([[0.0, 0.0, 1.0]]))

    def test_projection_rejects_invalid_sampling_stride(self):
        camera = CameraModel("camera_optical", 1, 1, 1.0, 1.0, 0.0, 0.0)
        with self.assertRaisesRegex(ValueError, "pixel_stride"):
            project_depth_millimetres(
                np.asarray([[1000]], dtype=np.uint16),
                camera,
                pixel_stride=0,
                depth_min_m=0.2,
                depth_max_m=2.0,
            )


if __name__ == "__main__":
    unittest.main()
