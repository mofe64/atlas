import tempfile
from pathlib import Path
import unittest

from atlas_spatial_runtime.diagnostics import discover, flat_probe, reconcile_live_usb


def write(path: Path, value: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(value, encoding="utf-8")


class DiagnosticsTests(unittest.TestCase):
    def test_discovers_depthai_usb3_device(self):
        with tempfile.TemporaryDirectory() as directory:
            device = Path(directory) / "bus/usb/devices/1-1"
            write(device / "idVendor", "03e7")
            write(device / "idProduct", "2485")
            write(device / "serial", "18443010")
            write(device / "product", "OAK-D Lite")
            write(device / "speed", "5000")
            result = discover(Path(directory))
            self.assertTrue(result["DEVICE_PRESENT"])
            self.assertEqual(result["DEVICE_ID"], "18443010")
            self.assertEqual(result["USB_TRANSPORT"], "usb3")

    def test_bootloader_identity_is_not_treated_as_camera_id(self):
        with tempfile.TemporaryDirectory() as directory:
            device = Path(directory) / "bus/usb/devices/1-1"
            write(device / "idVendor", "03e7")
            write(device / "idProduct", "2485")
            write(device / "serial", "03e72485")
            write(device / "speed", "480")
            result = discover(Path(directory))
            self.assertEqual(result["DEVICE_ID"], "")
            self.assertEqual(result["USB_TRANSPORT"], "usb2-or-unbooted")

    def test_flat_probe_contains_depth_only_contract(self):
        result = flat_probe(
            {
                "ready": True,
                "status": "ready",
                "protocolVersion": "2",
                "streams": {
                    "depth": {
                        "fps": 20,
                        "frameId": "oak_rgb_camera_optical_frame",
                        "encoding": "16UC1",
                        "unit": "millimetre",
                    }
                },
                "calibration": {"valid": True, "frameId": "oak_rgb_camera_optical_frame"},
                "capabilities": {"obstacleObservations": False},
                "aircraftProfile": {"id": "test-aircraft"},
                "device": {},
            }
        )
        self.assertEqual(result["DEPTH_ENCODING"], "16UC1")
        self.assertEqual(result["DEPTH_UNIT"], "millimetre")
        self.assertNotIn("COLOR_FPS", result)
        self.assertEqual(result["AIRCRAFT_PROFILE_ID"], "test-aircraft")

    def test_live_usb_metadata_replaces_boot_time_transport(self):
        with tempfile.TemporaryDirectory() as directory:
            device = Path(directory) / "bus/usb/devices/1-1"
            write(device / "idVendor", "03e7")
            write(device / "idProduct", "2485")
            write(device / "serial", "18443010")
            write(device / "product", "OAK-D Lite")
            write(device / "speed", "5000")
            payload = {
                "provider": "depthai",
                "device": {
                    "id": "18443010",
                    "model": "OAK-D Lite",
                    "connection": "usb2-or-unbooted",
                },
            }
            result = reconcile_live_usb(payload, Path(directory))
            self.assertEqual(result["device"]["connection"], "usb3")
            self.assertEqual(result["device"]["speedMbps"], 5000)


if __name__ == "__main__":
    unittest.main()
