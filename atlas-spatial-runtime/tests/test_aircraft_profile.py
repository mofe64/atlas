import json
from pathlib import Path
import tempfile
import unittest

from atlas_spatial_runtime.aircraft_profile import load_aircraft_profile


class AircraftProfileTests(unittest.TestCase):
    def test_tracked_ariadne_profile_contains_camera_identity_and_offset(self):
        path = Path(__file__).resolve().parents[2] / "aircraft-profiles/ariadne.json"
        profile = load_aircraft_profile(path)
        self.assertEqual(profile.profile_id, "ariadne")
        self.assertEqual(profile.depth_camera.device_id, "19443010F122147E00")
        self.assertEqual(profile.depth_camera.translation_m, (0.155, 0.01, 0.005))

    def test_profile_rejects_unknown_fields_and_invalid_offset(self):
        source = Path(__file__).resolve().parents[2] / "aircraft-profiles/ariadne.json"
        raw = json.loads(source.read_text(encoding="utf-8"))
        raw["payloads"]["depthCamera"]["offsetToBody"]["rotationWXYZ"] = {
            "w": 0,
            "x": 0,
            "y": 0,
            "z": 0,
        }
        with tempfile.NamedTemporaryFile("w", suffix=".json") as file:
            json.dump(raw, file)
            file.flush()
            with self.assertRaisesRegex(ValueError, "normalized"):
                load_aircraft_profile(Path(file.name))

        raw = json.loads(source.read_text(encoding="utf-8"))
        raw["unexpected"] = True
        with tempfile.NamedTemporaryFile("w", suffix=".json") as file:
            json.dump(raw, file)
            file.flush()
            with self.assertRaisesRegex(ValueError, "unknown fields"):
                load_aircraft_profile(Path(file.name))


if __name__ == "__main__":
    unittest.main()
