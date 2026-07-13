import importlib.util
import pathlib
import sys
import unittest


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


class HailoAdapterTests(unittest.TestCase):
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
        self.assertNotIn("hailooverlay", pipeline)


if __name__ == "__main__":
    unittest.main()
