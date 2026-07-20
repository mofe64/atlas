import importlib.util
import os
import pathlib
import sys
import unittest
from unittest import mock

import cv2
import numpy as np


SCRIPT = pathlib.Path(__file__).with_name("atlas-hailort-adapter.py")
SPEC = importlib.util.spec_from_file_location("atlas_hailort_adapter", SCRIPT)
ADAPTER = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
sys.modules[SPEC.name] = ADAPTER
SPEC.loader.exec_module(ADAPTER)


class FakeBox:
    def xmin(self): return -0.1
    def ymin(self): return 0.2
    def width(self): return 0.6
    def height(self): return 1.2


class FakeUniqueID:
    def get_id(self): return 17


class FakeDetection:
    def get_class_id(self): return 3
    def get_label(self): return "vehicle"
    def get_confidence(self): return 0.91
    def get_bbox(self): return FakeBox()
    def get_objects_typed(self, _kind): return [FakeUniqueID()]


class FakeHailo:
    HAILO_UNIQUE_ID = object()


class FakeCaps:
    def __init__(self, name):
        self.name = name


class FakeGst:
    CLOCK_TIME_NONE = (1 << 64) - 1

    class Caps:
        @staticmethod
        def from_string(name):
            return FakeCaps(name)


class FakeReferenceMeta:
    def __init__(self, timestamp):
        self.timestamp = timestamp


class FakeReferenceBuffer:
    def __init__(self, values):
        self.values = values

    def get_reference_timestamp_meta(self, caps):
        value = self.values.get(caps.name)
        return None if value is None else FakeReferenceMeta(value)


class FakeRTSPSource:
    def __init__(self, supported=True):
        self.supported = supported
        self.values = {}

    def find_property(self, name):
        return object() if self.supported and name == "add-reference-timestamp-meta" else None

    def set_property(self, name, value):
        self.values[name] = value


class FakePipeline:
    def __init__(self, source):
        self.source = source

    def get_by_name(self, name):
        return self.source if name == "atlas_rtsp_source" else None


class HailoAdapterTests(unittest.TestCase):
    def test_environment_decodes_atlas_setup_values_preserved_by_docker(self):
        with mock.patch.dict(
            os.environ,
            {"ATLAS_PERCEPTION_SOCKET_PATH": '"/run/atlas-agent/perception.sock"'},
        ):
            self.assertEqual(
                ADAPTER.environment("ATLAS_PERCEPTION_SOCKET_PATH"),
                "/run/atlas-agent/perception.sock",
            )

    def test_detection_payload_is_normalized_and_tracks_identity(self):
        payload = ADAPTER.detection_payload(FakeDetection(), FakeHailo)
        self.assertEqual(payload["trackId"], "17")
        self.assertEqual(payload["classLabel"], "vehicle")
        self.assertEqual(payload["confidence"], 0.91)
        self.assertEqual(payload["boundingBox"], {"x": 0.0, "y": 0.2, "width": 0.6, "height": 0.8})

    def test_gstreamer_values_are_quoted(self):
        self.assertEqual(ADAPTER.gst_quote("rtsp://camera/a b"), '"rtsp://camera/a b"')

    def test_pipeline_extracts_metadata_without_burning_an_overlay(self):
        config = ADAPTER.AdapterConfig(
            socket_path="/tmp/atlas.sock",
            input_url="rtsp://camera/main",
            input_codec="h264",
            input_transport="tcp",
            input_latency_ms=75,
            model_path="/models/objects.hef",
            model_name="objects",
            model_version="1",
            model_hash="sha256:test",
            postprocess_so="/lib/libyolo.so",
            postprocess_function="filter",
            postprocess_config="",
            source_id="a8-main",
            accelerator="hailo-8l",
            width=640,
            height=640,
        )
        pipeline = ADAPTER.build_pipeline(config)
        self.assertIn("hailonet", pipeline)
        self.assertIn("atlas_detection_output", pipeline)
        self.assertIn("name=atlas_rtsp_source", pipeline)
        self.assertNotIn("hailooverlay", pipeline)

    def test_source_reference_timestamp_is_enabled_only_when_supported(self):
        source = FakeRTSPSource(supported=True)
        self.assertTrue(ADAPTER.enable_source_reference_timestamps(FakePipeline(source)))
        self.assertEqual(source.values, {"add-reference-timestamp-meta": True})
        unsupported = FakeRTSPSource(supported=False)
        self.assertFalse(ADAPTER.enable_source_reference_timestamps(FakePipeline(unsupported)))
        self.assertEqual(unsupported.values, {})

    def test_source_reference_timestamp_converts_recognized_epochs(self):
        unix_ns = 1_700_000_000_123_456_789
        ntp_buffer = FakeReferenceBuffer({
            "timestamp/x-ntp": unix_ns + ADAPTER.NTP_UNIX_EPOCH_OFFSET_NS,
        })
        self.assertEqual(ADAPTER.source_capture_unix_ns(ntp_buffer, FakeGst), unix_ns)
        unix_buffer = FakeReferenceBuffer({"timestamp/x-unix": unix_ns})
        self.assertEqual(ADAPTER.source_capture_unix_ns(unix_buffer, FakeGst), unix_ns)
        invalid = FakeReferenceBuffer({"timestamp/x-ntp": FakeGst.CLOCK_TIME_NONE})
        self.assertIsNone(ADAPTER.source_capture_unix_ns(invalid, FakeGst))

    def test_sparse_optical_flow_reports_normalized_previous_to_current_motion(self):
        first = np.zeros((240, 320, 3), dtype=np.uint8)
        rng = np.random.default_rng(42)
        for x, y in rng.integers([20, 20], [300, 220], size=(80, 2)):
            cv2.circle(first, (int(x), int(y)), 2, (255, 255, 255), -1)
        shift_x, shift_y = 12, 7
        second = cv2.warpAffine(
            first,
            np.float32([[1, 0, shift_x], [0, 1, shift_y]]),
            (first.shape[1], first.shape[0]),
        )
        estimator = ADAPTER.CameraMotionEstimator(cv2, np, max_dimension=320, max_features=300)
        self.assertIsNone(estimator.estimate(first))
        motion = estimator.estimate(second)
        self.assertIsNotNone(motion)
        self.assertEqual(motion["method"], "SPARSE_OPTICAL_FLOW")
        self.assertAlmostEqual(motion["homography"][2], shift_x / first.shape[1], delta=0.01)
        self.assertAlmostEqual(motion["homography"][5], shift_y / first.shape[0], delta=0.01)
        self.assertGreater(motion["confidence"], 0.25)
        estimator.reset()
        self.assertIsNone(estimator.estimate(second))


if __name__ == "__main__":
    unittest.main()
