import os
from pathlib import Path
import unittest
from unittest.mock import patch

import numpy as np

from atlas_spatial_runtime.runtime import build_provider


class RuntimeTests(unittest.TestCase):
    def profile_environment(self) -> dict[str, str]:
        path = Path(__file__).resolve().parents[2] / "aircraft-profiles/ariadne.json"
        return {
            "ATLAS_AIRCRAFT_PROFILE_ID": "ariadne",
            "ATLAS_AIRCRAFT_PROFILE_PATH": str(path),
        }

    def test_synthetic_provider_uses_native_frame_contract(self):
        environment = self.profile_environment() | {
            "ATLAS_SPATIAL_PROVIDER": "synthetic",
            "ATLAS_SPATIAL_WIDTH": "16",
            "ATLAS_SPATIAL_HEIGHT": "12",
            "ATLAS_SPATIAL_FPS": "20",
            "ATLAS_SPATIAL_FRAME_ID": "test_optical",
        }
        with patch.dict(os.environ, environment, clear=False):
            provider = build_provider()
        provider.start()
        frame = provider.try_read()
        self.assertIsNotNone(frame)
        self.assertEqual(frame.depth_mm.dtype, np.uint16)
        self.assertEqual(frame.depth_mm.shape, (12, 16))
        self.assertEqual(frame.calibration.frame_id, "test_optical")
        frame.validate()

    def test_unknown_provider_fails_before_service_start(self):
        with patch.dict(
            os.environ,
            self.profile_environment() | {
                "ATLAS_SPATIAL_PROVIDER": "unknown",
                "ATLAS_SPATIAL_FRAME_ID": "test_optical",
            },
            clear=False,
        ):
            with self.assertRaisesRegex(ValueError, "unsupported"):
                build_provider()


if __name__ == "__main__":
    unittest.main()
