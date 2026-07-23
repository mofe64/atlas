import unittest

import numpy as np

from atlas_spatial_runtime.live_cloud import (
    BoundedVoxelCloud,
    CameraModel,
    PoseBuffer,
    PoseSample,
    pose_transform,
    project_depth,
    resolve_transform,
)


def pose(stamp, *, frame="vio_odom", child="oak_mount", position=(0.0, 0.0, 0.0)):
    return PoseSample(stamp, frame, child, position, (1.0, 0.0, 0.0, 0.0))


class LiveCloudTests(unittest.TestCase):
    def test_depth_projects_with_camera_intrinsics_and_range_filter(self):
        camera = CameraModel("camera_optical", 2, 2, 1.0, 1.0, 0.0, 0.0)
        points = project_depth(
            np.asarray([[1.0, 2.0], [0.0, np.nan]], dtype=np.float32),
            camera,
            pixel_stride=1,
            depth_min_m=0.2,
            depth_max_m=1.5,
        )
        np.testing.assert_allclose(points, np.asarray([[0.0, 0.0, 1.0]]))

    def test_configured_optical_transform_and_vio_pose_produce_local_points(self):
        bundle = {
            "transforms": [
                {
                    "parentFrame": "oak_mount",
                    "childFrame": "camera_optical",
                    "status": "configured_unverified",
                    "translationM": {"x": 1.0, "y": 0.0, "z": 0.0},
                    "rotationWXYZ": {"w": 1.0, "x": 0.0, "y": 0.0, "z": 0.0},
                }
            ]
        }
        optical_to_child = resolve_transform(bundle, "camera_optical", "oak_mount")
        child_points = optical_to_child.apply(np.asarray([[0.0, 0.0, 2.0]]))
        local_points = pose_transform(pose(10, position=(0.0, 2.0, 0.0))).apply(child_points)
        np.testing.assert_allclose(local_points, np.asarray([[1.0, 2.0, 2.0]]))

    def test_configured_transform_rotation_uses_child_to_parent_semantics(self):
        root_half = 2 ** -0.5
        bundle = {
            "transforms": [
                {
                    "parentFrame": "oak_mount",
                    "childFrame": "camera_optical",
                    "status": "configured_unverified",
                    "translationM": {"x": 0.0, "y": 0.0, "z": 0.0},
                    "rotationWXYZ": {"w": root_half, "x": 0.0, "y": 0.0, "z": root_half},
                }
            ]
        }
        transform = resolve_transform(bundle, "camera_optical", "oak_mount")
        np.testing.assert_allclose(
            transform.apply(np.asarray([[1.0, 0.0, 0.0]])),
            np.asarray([[0.0, 1.0, 0.0]]),
            atol=1e-7,
        )

    def test_pose_matching_is_bounded_and_epoch_changes_are_explicit(self):
        poses = PoseBuffer(capacity=3)
        self.assertFalse(poses.add(pose(100)))
        self.assertFalse(poses.add(pose(200)))
        self.assertEqual(poses.nearest(170, 40), pose(200))
        self.assertIsNone(poses.nearest(300, 50))
        self.assertTrue(poses.add(pose(50)))
        self.assertEqual(poses.latest_capture_ns, 50)
        self.assertTrue(poses.add(pose(60, frame="new_vio_odom")))

    def test_voxel_cloud_is_downsampled_and_strictly_bounded(self):
        cloud = BoundedVoxelCloud(voxel_size_m=1.0, maximum_points=2)
        cloud.integrate(np.asarray([[0.1, 0.1, 0.1], [0.2, 0.2, 0.2], [1.1, 0.0, 0.0]]))
        self.assertEqual(len(cloud), 2)
        cloud.integrate(np.asarray([[2.1, 0.0, 0.0]]))
        self.assertEqual(len(cloud), 2)
        np.testing.assert_allclose(
            cloud.snapshot(),
            np.asarray([[1.1, 0.0, 0.0], [2.1, 0.0, 0.0]], dtype=np.float32),
        )

    def test_recently_observed_voxel_is_retained_when_cloud_is_full(self):
        cloud = BoundedVoxelCloud(voxel_size_m=1.0, maximum_points=2)
        cloud.integrate(np.asarray([[0.1, 0.0, 0.0], [1.1, 0.0, 0.0]]))
        cloud.integrate(np.asarray([[0.2, 0.0, 0.0], [2.1, 0.0, 0.0]]))
        np.testing.assert_allclose(
            cloud.snapshot(),
            np.asarray([[0.2, 0.0, 0.0], [2.1, 0.0, 0.0]], dtype=np.float32),
        )

    def test_unmeasured_or_missing_transform_is_not_used(self):
        bundle = {
            "transforms": [
                {
                    "parentFrame": "oak_mount",
                    "childFrame": "camera_optical",
                    "status": "unmeasured",
                }
            ]
        }
        with self.assertRaisesRegex(ValueError, "no configured transform"):
            resolve_transform(bundle, "camera_optical", "oak_mount")


if __name__ == "__main__":
    unittest.main()
