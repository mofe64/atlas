from importlib.machinery import SourceFileLoader
from importlib.util import module_from_spec, spec_from_loader
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("atlas-spatial-runtime-check")
LOADER = SourceFileLoader("atlas_spatial_runtime_check", str(SCRIPT))
SPEC = spec_from_loader(LOADER.name, LOADER)
runtime_check = module_from_spec(SPEC)
LOADER.exec_module(runtime_check)


class RuntimeCheckTests(unittest.TestCase):
    def test_discovers_depthai_without_product_specific_configuration(self):
        with tempfile.TemporaryDirectory() as temporary:
            device = Path(temporary) / "bus" / "usb" / "devices" / "1-1"
            device.mkdir(parents=True)
            (device / "idVendor").write_text("03e7\n", encoding="utf-8")
            (device / "serial").write_text("device-123\n", encoding="utf-8")
            (device / "product").write_text("OAK-D Lite\n", encoding="utf-8")
            (device / "speed").write_text("5000\n", encoding="utf-8")

            result = runtime_check.discover(Path(temporary))

        self.assertTrue(result["DEVICE_PRESENT"])
        self.assertEqual(result["PROVIDER"], "depthai")
        self.assertEqual(result["DEVICE_ID"], "device-123")
        self.assertEqual(result["USB_TRANSPORT"], "usb3")

    def test_configured_device_id_is_not_silently_replaced(self):
        with tempfile.TemporaryDirectory() as temporary:
            device = Path(temporary) / "bus" / "usb" / "devices" / "1-1"
            device.mkdir(parents=True)
            (device / "idVendor").write_text("03e7\n", encoding="utf-8")
            (device / "serial").write_text("another-device\n", encoding="utf-8")

            result = runtime_check.discover(Path(temporary), "configured-device")

        self.assertFalse(result["DEVICE_PRESENT"])
        self.assertTrue(result["OTHER_DEVICE_PRESENT"])
        self.assertEqual(result["DEVICE_ID"], "configured-device")

    def test_bootloader_usb_identity_is_not_used_as_depthai_device_id(self):
        with tempfile.TemporaryDirectory() as temporary:
            device = Path(temporary) / "bus" / "usb" / "devices" / "4-1"
            device.mkdir(parents=True)
            (device / "idVendor").write_text("03e7\n", encoding="utf-8")
            (device / "idProduct").write_text("2485\n", encoding="utf-8")
            (device / "serial").write_text("03e72485\n", encoding="utf-8")
            (device / "product").write_text("Movidius MyriadX\n", encoding="utf-8")
            (device / "speed").write_text("480\n", encoding="utf-8")

            result = runtime_check.discover(Path(temporary))

        self.assertTrue(result["DEVICE_PRESENT"])
        self.assertEqual(result["DEVICE_ID"], "")
        self.assertEqual(result["USB_IDENTITY"], "03e72485")
        self.assertEqual(result["USB_TRANSPORT"], "usb2-or-unbooted")

    def test_existing_bootloader_identity_is_healed_on_rediscovery(self):
        with tempfile.TemporaryDirectory() as temporary:
            device = Path(temporary) / "bus" / "usb" / "devices" / "4-1"
            device.mkdir(parents=True)
            (device / "idVendor").write_text("03e7\n", encoding="utf-8")
            (device / "idProduct").write_text("2485\n", encoding="utf-8")
            (device / "serial").write_text("03e72485\n", encoding="utf-8")

            result = runtime_check.discover(Path(temporary), "03e72485")

        self.assertTrue(result["DEVICE_PRESENT"])
        self.assertEqual(result["DEVICE_ID"], "")

    def test_probe_uses_versioned_contract_and_flattens_nested_streams(self):
        response_payload = {
            "protocolVersion": "1",
            "ready": True,
            "status": "ready",
            "provider": "depthai",
            "sourceId": "front-depth",
            "device": {"id": "device-123", "model": "camera", "connection": "usb3"},
            "streams": {
                "color": {"fps": 15.0}, "depth": {"fps": 14.9},
                "imu": {"status": "ready", "rateHz": 100.0, "ageMs": 5.0},
            },
            "vio": {
                "status": "initializing",
                "reason": "warming up",
                "mappingEnabled": True,
                "px4FusionEnabled": False,
                "movementAuthority": False,
            },
            "transformBundle": {"sha256": "sha256:test", "bodyToOakStatus": "unmeasured", "bodyToHFlowStatus": "configured_unverified"},
            "syncSkewMs": 1.2,
            "calibrationHash": "sha256:abc",
            "lastError": "",
        }

        class FakeSocket:
            def __init__(self):
                self.sent = b""
                self.response = json.dumps(response_payload).encode("utf-8") + b"\n"

            def __enter__(self):
                return self

            def __exit__(self, *_):
                return False

            def settimeout(self, _):
                pass

            def connect(self, _):
                pass

            def sendall(self, value):
                self.sent += value

            def recv(self, _):
                response, self.response = self.response, b""
                return response

        connection = FakeSocket()
        with patch.object(runtime_check.socket, "socket", return_value=connection):
            response = runtime_check.probe("/run/atlas-agent/spatial.sock", 1.0)

        self.assertEqual(json.loads(connection.sent), {"protocolVersion": "1", "type": "probe"})
        flattened = runtime_check._flat_probe(response)
        self.assertTrue(flattened["READY"])
        self.assertEqual(flattened["DEVICE_ID"], "device-123")
        self.assertEqual(flattened["COLOR_FPS"], 15.0)
        self.assertEqual(flattened["DEPTH_FPS"], 14.9)
        self.assertEqual(flattened["IMU_STATUS"], "ready")
        self.assertEqual(flattened["VIO_STATUS"], "initializing")
        self.assertTrue(flattened["VIO_MAPPING_ENABLED"])
        self.assertFalse(flattened["VIO_PX4_FUSION_ENABLED"])
        self.assertFalse(flattened["VIO_MOVEMENT_AUTHORITY"])
        self.assertEqual(flattened["BODY_TO_HFLOW_STATUS"], "configured_unverified")

    def test_live_probe_replaces_depthai_boot_transport_with_current_sysfs_speed(self):
        payload = {
            "protocolVersion": "1",
            "ready": True,
            "provider": "depthai",
            "device": {
                "id": "",
                "model": "Movidius MyriadX",
                "connection": "usb2-or-unbooted",
            },
        }
        with tempfile.TemporaryDirectory() as temporary:
            device = Path(temporary) / "bus" / "usb" / "devices" / "5-1"
            device.mkdir(parents=True)
            (device / "idVendor").write_text("03e7\n", encoding="utf-8")
            (device / "idProduct").write_text("f63b\n", encoding="utf-8")
            (device / "serial").write_text("19443010F122147E00\n", encoding="utf-8")
            (device / "product").write_text("Movidius MyriadX\n", encoding="utf-8")
            (device / "speed").write_text("5000\n", encoding="utf-8")

            reconciled = runtime_check.reconcile_live_usb(payload, Path(temporary))

        flattened = runtime_check._flat_probe(reconciled)
        self.assertEqual(flattened["USB_TRANSPORT"], "usb3")
        self.assertEqual(flattened["USB_SPEED_MBPS"], 5000)
        self.assertEqual(flattened["DEVICE_ID"], "19443010F122147E00")

    def test_live_probe_does_not_replace_non_depthai_transport(self):
        payload = {
            "protocolVersion": "1",
            "provider": "synthetic",
            "device": {"connection": "virtual"},
        }
        with tempfile.TemporaryDirectory() as temporary:
            reconciled = runtime_check.reconcile_live_usb(payload, Path(temporary))
        self.assertIs(reconciled, payload)


if __name__ == "__main__":
    unittest.main()
