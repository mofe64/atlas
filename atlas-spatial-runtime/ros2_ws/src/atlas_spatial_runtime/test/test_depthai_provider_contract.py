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

    def test_vio_parameter_file_enables_live_non_authoritative_inputs_and_disables_tf(self):
        config = yaml.safe_load((PACKAGE_ROOT / "config" / "depthai_vio.yaml").read_text(encoding="utf-8"))
        parameters = config["/**"]["ros__parameters"]

        self.assertTrue(parameters["pipeline_gen"]["i_enable_imu"])
        self.assertTrue(parameters["pipeline_gen"]["i_enable_vio"])
        self.assertTrue(parameters["vio"]["i_publish_topic"])
        self.assertFalse(parameters["vio"]["i_publish_tf"])
        self.assertEqual(parameters["vio"]["i_fps"], 20.0)
        self.assertEqual(parameters["vio"]["i_imu_update_rate"], 400)
        self.assertEqual(parameters["vio"]["i_frame_id"], "vio_odom")
        self.assertEqual(parameters["vio"]["i_child_frame_id"], "oak_mount")

    def test_parameter_file_substitutes_selected_device_id(self):
        context = LaunchContext()
        context.launch_configurations["device_id"] = "19443010F122147E00"
        parameter_file = ParameterFile(PACKAGE_ROOT / "config" / "depthai_vio.yaml", allow_substs=True)

        evaluated_path = parameter_file.evaluate(context)
        try:
            config = yaml.safe_load(evaluated_path.read_text(encoding="utf-8"))
            self.assertEqual(
                config["/**"]["ros__parameters"]["driver"]["i_device_id"],
                "19443010F122147E00",
            )
        finally:
            parameter_file.cleanup()

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

        self.assertIn('"params_file": params_file', source)
        self.assertIn('"publish_tf_from_calibration": "false"', source)
        self.assertIn("UnlessCondition(vio_enabled)", source)
        self.assertIn("IfCondition(vio_enabled)", source)
        self.assertIn('default_value="true"', source)
        self.assertNotIn('"pipeline_gen.i_enable_vio"', source)
        self.assertNotIn('"driver.i_device_id"', source)


if __name__ == "__main__":
    unittest.main()
