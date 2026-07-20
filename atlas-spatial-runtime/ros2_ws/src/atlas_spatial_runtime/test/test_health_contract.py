import json
import unittest

from atlas_spatial_runtime.depth_contract import millimetres_to_float_metres
from atlas_spatial_runtime.health_contract import SpatialHealthState, calibration_hash, validate_probe_request


class HealthContractTests(unittest.TestCase):
    def test_ready_snapshot_requires_fresh_synchronized_calibrated_streams(self):
        state = SpatialHealthState(provider="synthetic", source_id="front-depth")
        state.set_calibration([64.0, 48.0, 50.0])
        state.color.observe(1_000_000_000, 2_000_000_000, 64, 48, "rgb8")
        state.depth.observe(1_002_000_000, 2_001_000_000, 64, 48, "32FC1")
        snapshot = state.snapshot(now_ns=2_010_000_000)
        self.assertTrue(snapshot["ready"])
        self.assertTrue(snapshot["synchronized"])
        self.assertEqual(snapshot["streams"]["depth"]["unit"], "metre")

    def test_stale_or_misaligned_stream_is_degraded(self):
        state = SpatialHealthState(provider="synthetic", source_id="front-depth")
        state.set_calibration([1.0])
        state.color.observe(1_000_000_000, 1_000_000_000, 64, 48, "rgb8")
        state.depth.observe(1_100_000_000, 1_000_000_000, 64, 48, "32FC1")
        self.assertFalse(state.snapshot(now_ns=3_000_000_000)["ready"])

    def test_calibration_hash_is_stable_and_probe_is_bounded(self):
        self.assertEqual(calibration_hash([1.0, 2.0]), calibration_hash([1.0, 2.0]))
        validate_probe_request(json.dumps({"protocolVersion": "1", "type": "probe"}).encode())
        with self.assertRaises(ValueError):
            validate_probe_request(json.dumps({"protocolVersion": "2", "type": "probe"}).encode())
        with self.assertRaises(ValueError):
            validate_probe_request(b"x" * 4097)

    def test_provider_depth_is_normalized_to_float_metres(self):
        import numpy

        source = numpy.array([0, 500, 2000, 65535], dtype=numpy.uint16)
        raw, _ = millimetres_to_float_metres(source.tobytes(), 2, 2, 4, False)
        converted = numpy.frombuffer(raw, dtype=numpy.float32)
        self.assertTrue(numpy.isnan(converted[0]))
        self.assertAlmostEqual(float(converted[1]), 0.5)
        self.assertAlmostEqual(float(converted[2]), 2.0)
        self.assertAlmostEqual(float(converted[3]), 65.535, places=3)


if __name__ == "__main__":
    unittest.main()
