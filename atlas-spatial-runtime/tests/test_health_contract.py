import json
import unittest

import numpy as np

from atlas_spatial_runtime.health_contract import (
    SpatialHealthState,
    validate_probe_request,
)
from atlas_spatial_runtime.provider import (
    CameraCalibration,
    DepthFrame,
    ProviderInfo,
)


def frame(
    *,
    capture_ns: int = 1_000_000_000,
    arrival_ns: int = 2_000_000_000,
    frame_id: str = "oak_rgb_camera_optical_frame",
) -> DepthFrame:
    return DepthFrame(
        depth_mm=np.ones((48, 64), dtype=np.uint16),
        capture_monotonic_ns=capture_ns,
        arrival_monotonic_ns=arrival_ns,
        calibration=CameraCalibration(
            frame_id=frame_id,
            width=64,
            height=48,
            fx=50.0,
            fy=50.0,
            cx=32.0,
            cy=24.0,
        ),
    )


class HealthContractTests(unittest.TestCase):
    def setUp(self):
        self.state = SpatialHealthState(
            provider="synthetic",
            source_id="front-depth",
            device=ProviderInfo("synthetic", "synthetic", "synthetic", "memory"),
            profile_id="test-aircraft",
        )

    def test_ready_requires_fresh_native_depth_and_matching_intrinsics(self):
        self.state.observe(frame())
        snapshot = self.state.snapshot(now_ns=2_010_000_000)
        self.assertTrue(snapshot["ready"])
        self.assertEqual(snapshot["protocolVersion"], "2")
        self.assertEqual(snapshot["streams"]["depth"]["encoding"], "16UC1")
        self.assertEqual(snapshot["streams"]["depth"]["unit"], "millimetre")
        self.assertEqual(snapshot["streams"]["depth"]["scaleToMetres"], 0.001)
        self.assertNotIn("color", snapshot["streams"])
        self.assertFalse(snapshot["capabilities"]["obstacleObservations"])
        self.assertEqual(snapshot["aircraftProfile"]["id"], "test-aircraft")

    def test_stale_depth_is_not_ready(self):
        self.state.observe(frame(arrival_ns=1_000_000_000))
        snapshot = self.state.snapshot(now_ns=3_000_000_000)
        self.assertEqual(snapshot["status"], "stale")
        self.assertFalse(snapshot["ready"])

    def test_frame_validation_rejects_calibration_mismatch(self):
        invalid = DepthFrame(
            depth_mm=np.ones((10, 10), dtype=np.uint16),
            capture_monotonic_ns=1,
            arrival_monotonic_ns=2,
            calibration=frame().calibration,
        )
        with self.assertRaisesRegex(ValueError, "do not match"):
            self.state.observe(invalid)

    def test_probe_is_versioned_and_bounded(self):
        validate_probe_request(
            json.dumps({"protocolVersion": "2", "type": "probe"}).encode()
        )
        with self.assertRaises(ValueError):
            validate_probe_request(
                json.dumps({"protocolVersion": "1", "type": "probe"}).encode()
            )
        with self.assertRaises(ValueError):
            validate_probe_request(b"x" * 4097)


if __name__ == "__main__":
    unittest.main()
