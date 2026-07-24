from pathlib import Path
import unittest

import yaml
from launch import LaunchContext
from launch_ros.descriptions import ParameterFile


PACKAGE_ROOT = Path(__file__).resolve().parents[1]


class DepthAIProviderContractTests(unittest.TestCase):
    def test_stable_parameter_file_keeps_rgbd_and_imu_without_basalt(self):
        config = yaml.safe_load((PACKAGE_ROOT / "config" / "depthai_rgbd_imu.yaml").read_text(encoding="utf-8"))
        parameters = config["/**"]["ros__parameters"]

        self.assertTrue(parameters["pipeline_gen"]["i_enable_imu"])
        self.assertFalse(parameters["pipeline_gen"]["i_enable_vio"])
        self.assertEqual(parameters["pipeline_gen"]["i_pipeline_type"], "rgbd")
        self.assertNotIn("stereo", parameters)
        self.assertTrue(parameters["depth"]["i_aligned"])
        self.assertEqual(parameters["depth"]["i_board_socket_id"], 0)
        self.assertTrue(parameters["depth"]["i_synced"])
        self.assertEqual(parameters["depth"]["i_depth_preset"], "DEFAULT")
        self.assertFalse(parameters["depth"]["i_reverse_stereo_socket_order"])
        self.assertTrue(parameters["depth"]["i_left_rect_publish_topic"])
        self.assertTrue(parameters["depth"]["i_right_rect_publish_topic"])
        self.assertTrue(parameters["rgb"]["i_synced"])

    def test_external_vio_is_bounded_and_uses_stable_atlas_frames(self):
        config = yaml.safe_load(
            (PACKAGE_ROOT / "config" / "rtabmap_vio.yaml").read_text(encoding="utf-8")
        )
        parameters = config["/**"]["ros__parameters"]

        self.assertEqual(parameters["frame_id"], "oak_mount")
        self.assertEqual(parameters["odom_frame_id"], "vio_odom")
        self.assertFalse(parameters["publish_tf"])
        self.assertTrue(parameters["wait_imu_to_init"])
        self.assertTrue(parameters["always_process_most_recent_frame"])
        self.assertEqual(parameters["expected_update_rate"], 20.0)
        self.assertEqual(parameters["max_update_rate"], 20.0)
        self.assertLessEqual(parameters["topic_queue_size"], 2)
        self.assertLessEqual(parameters["sync_queue_size"], 5)
        self.assertTrue(parameters["publish_null_when_lost"])
        self.assertEqual(parameters["Odom/ResetCountdown"], "0")
        self.assertEqual(parameters["Stereo/MaxDisparity"], "128")
        self.assertEqual(parameters["Vis/FeatureType"], "2")

    def test_stable_parameter_file_substitutes_selected_device_id(self):
        context = LaunchContext()
        context.launch_configurations["device_id"] = "19443010F122147E00"
        parameter_file = ParameterFile(PACKAGE_ROOT / "config" / "depthai_rgbd_imu.yaml", allow_substs=True)

        evaluated_path = parameter_file.evaluate(context)
        try:
            config = yaml.safe_load(evaluated_path.read_text(encoding="utf-8"))
            self.assertEqual(
                config["/**"]["ros__parameters"]["driver"]["i_device_id"],
                "19443010F122147E00",
            )
        finally:
            parameter_file.cleanup()

    def test_provider_uses_supported_parameter_file_boundary(self):
        source = (PACKAGE_ROOT / "launch" / "providers" / "depthai.launch.py").read_text(encoding="utf-8")

        self.assertIn('"params_file": stable_params_file', source)
        self.assertIn('"publish_tf_from_calibration": "true"', source)
        self.assertIn('"parent_frame": "oak_mount"', source)
        self.assertIn('"depth_module.depth_profile": "640x400x20"', source)
        self.assertIn('"depth_module.infra_profile": "640x400x20"', source)
        self.assertIn('"rgb_camera.color_profile": "640x400x20"', source)
        self.assertIn('"enable_infra1": "true"', source)
        self.assertIn('"enable_infra2": "true"', source)
        self.assertIn("IfCondition(vio_enabled)", source)
        self.assertIn('default_value="true"', source)
        self.assertNotIn('"pipeline_gen.i_enable_vio"', source)
        self.assertNotIn('"driver.i_device_id"', source)
        self.assertNotIn("depthai_vio.yaml", source)
        self.assertIn('package="imu_filter_madgwick"', source)
        self.assertIn('package="rtabmap_odom"', source)
        self.assertIn('executable="stereo_odometry"', source)
        self.assertNotIn('executable="rgbd_odometry"', source)
        self.assertIn("TimerAction(period=8.0, actions=[external_vio])", source)
        self.assertIn('executable="atlas-spatial-imu-timestamp-gate"', source)
        self.assertIn(
            '("imu/data_raw", "/atlas/spatial/provider/imu/data_monotonic")',
            source,
        )
        self.assertIn(
            '("odom", "/atlas/spatial/vio/odometry")',
            source,
        )
        self.assertIn(
            '("left/image_rect", "/atlas/spatial/provider/left/image_rect")',
            source,
        )
        self.assertIn(
            '("right/image_rect", "/atlas/spatial/provider/right/image_rect")',
            source,
        )
        self.assertIn(
            'src="/camera/camera/infra1/image_rect_raw",\n'
            '            dst="/atlas/spatial/provider/right/image_rect"',
            source,
        )
        self.assertIn(
            'src="/camera/camera/infra2/image_rect_raw",\n'
            '            dst="/atlas/spatial/provider/left/image_rect"',
            source,
        )
        self.assertIn(
            'dst="/atlas/spatial/provider/right/camera_info_depthai"',
            source,
        )
        self.assertIn(
            'dst="/atlas/spatial/provider/left/camera_info_depthai"',
            source,
        )
        self.assertIn(
            'executable="atlas-spatial-stereo-camera-info"',
            source,
        )
        self.assertIn(
            'on_exit=Shutdown(reason="spatial stereo CameraInfo normalizer exited")',
            source,
        )
        self.assertIn(
            'dst="/atlas/spatial/provider/imu/data_raw"',
            source,
        )
        self.assertIn(
            'dst="/atlas/spatial/provider/aligned_depth/camera_info"',
            source,
        )
        self.assertNotIn(
            'dst="/atlas/spatial/aligned_depth/camera_info"',
            source,
        )
        self.assertIn(
            'on_exit=Shutdown(reason="spatial IMU timestamp gate exited")',
            source,
        )
        self.assertIn(
            'on_exit=Shutdown(reason="spatial IMU orientation filter exited")',
            source,
        )
        self.assertIn(
            'on_exit=Shutdown(reason="spatial stereo-inertial odometry exited")',
            source,
        )

    def test_runtime_supervises_both_complete_cloud_processes(self):
        source = (PACKAGE_ROOT / "launch" / "spatial_runtime.launch.py").read_text(
            encoding="utf-8"
        )

        self.assertIn(
            'on_exit=Shutdown(reason="spatial live-cloud builder exited")',
            source,
        )
        self.assertIn(
            'on_exit=Shutdown(reason="spatial complete-cloud stream exited")',
            source,
        )
        self.assertIn('"vio_required": vio_enabled', source)
        self.assertIn('"vio_stale_exit_after_ms": 5000.0', source)

    def test_health_observes_provider_imu_before_the_timestamp_gate(self):
        health_source = (
            PACKAGE_ROOT / "atlas_spatial_runtime" / "health_node.py"
        ).read_text(encoding="utf-8")
        synthetic_source = (
            PACKAGE_ROOT / "atlas_spatial_runtime" / "synthetic_provider.py"
        ).read_text(encoding="utf-8")

        self.assertIn('"/atlas/spatial/provider/imu/data_raw"', health_source)
        self.assertIn('"/atlas/spatial/provider/imu/data_raw"', synthetic_source)


if __name__ == "__main__":
    unittest.main()
