"""Host USB discovery plus the provider-neutral Spatial health probe."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import sys

from .probe import probe


DEPTHAI_VENDOR_ID = "03e7"


def _read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8").strip()
    except (OSError, UnicodeError):
        return ""


def discover(sysfs_root: Path, preferred_device_id: str = "") -> dict[str, object]:
    found: list[dict[str, object]] = []
    for candidate in sorted((sysfs_root / "bus" / "usb" / "devices").glob("*")):
        vendor_id = _read(candidate / "idVendor").lower()
        if vendor_id != DEPTHAI_VENDOR_ID:
            continue
        product_id = _read(candidate / "idProduct").lower()
        usb_identity = _read(candidate / "serial")
        device_id = usb_identity
        if usb_identity.lower() == vendor_id + product_id:
            device_id = ""
        try:
            speed_mbps = int(float(_read(candidate / "speed")))
        except ValueError:
            speed_mbps = 0
        if speed_mbps >= 5000:
            transport = "usb3"
        elif speed_mbps >= 480:
            transport = "usb2-or-unbooted"
        elif speed_mbps > 0:
            transport = "low-speed"
        else:
            transport = "unknown"
        found.append(
            {
                "DEVICE_PRESENT": True,
                "PROVIDER": "depthai",
                "DEVICE_ID": device_id,
                "USB_IDENTITY": usb_identity,
                "MODEL": _read(candidate / "product") or "DepthAI camera",
                "USB_TRANSPORT": transport,
                "USB_SPEED_MBPS": speed_mbps,
            }
        )
    if preferred_device_id:
        for device in found:
            if device["DEVICE_ID"] == preferred_device_id or (
                not device["DEVICE_ID"]
                and device["USB_IDENTITY"] == preferred_device_id
            ):
                return device
        return {
            "DEVICE_PRESENT": False,
            "OTHER_DEVICE_PRESENT": bool(found),
            "PROVIDER": "depthai",
            "DEVICE_ID": preferred_device_id,
            "USB_IDENTITY": "",
            "MODEL": "",
            "USB_TRANSPORT": "unknown",
            "USB_SPEED_MBPS": 0,
        }
    if found:
        return found[0]
    return {
        "DEVICE_PRESENT": False,
        "OTHER_DEVICE_PRESENT": False,
        "PROVIDER": "",
        "DEVICE_ID": "",
        "USB_IDENTITY": "",
        "MODEL": "",
        "USB_TRANSPORT": "unknown",
        "USB_SPEED_MBPS": 0,
    }


def reconcile_live_usb(
    payload: dict[str, object], sysfs_root: Path, preferred_device_id: str = ""
) -> dict[str, object]:
    if payload.get("provider") != "depthai":
        return payload
    existing_device = payload.get("device")
    if not isinstance(existing_device, dict):
        existing_device = {}
    selected_id = preferred_device_id or str(existing_device.get("id", ""))
    observed = discover(sysfs_root, selected_id)
    if not bool(observed.get("DEVICE_PRESENT")):
        return payload
    result = dict(payload)
    device = dict(existing_device)
    device["connection"] = observed.get("USB_TRANSPORT", "unknown")
    device["speedMbps"] = observed.get("USB_SPEED_MBPS", 0)
    if not device.get("id") and observed.get("DEVICE_ID"):
        device["id"] = observed["DEVICE_ID"]
    if not device.get("model") and observed.get("MODEL"):
        device["model"] = observed["MODEL"]
    result["device"] = device
    return result


def flat_probe(payload: dict[str, object]) -> dict[str, object]:
    streams = payload.get("streams") if isinstance(payload.get("streams"), dict) else {}
    depth = streams.get("depth") if isinstance(streams.get("depth"), dict) else {}
    calibration = payload.get("calibration") if isinstance(payload.get("calibration"), dict) else {}
    capabilities = payload.get("capabilities") if isinstance(payload.get("capabilities"), dict) else {}
    device = payload.get("device") if isinstance(payload.get("device"), dict) else {}
    profile = payload.get("aircraftProfile") if isinstance(payload.get("aircraftProfile"), dict) else {}
    return {
        "READY": bool(payload.get("ready")),
        "STATUS": payload.get("status", "unknown"),
        "PROTOCOL_VERSION": payload.get("protocolVersion", ""),
        "PROVIDER": payload.get("provider", ""),
        "SOURCE_ID": payload.get("sourceId", ""),
        "DEVICE_ID": device.get("id", ""),
        "MODEL": device.get("model", ""),
        "AIRCRAFT_PROFILE_ID": profile.get("id", ""),
        "USB_TRANSPORT": device.get("connection", "unknown"),
        "USB_SPEED_MBPS": device.get("speedMbps", 0),
        "DEPTH_FPS": depth.get("fps", 0),
        "DEPTH_FRAME_ID": depth.get("frameId", ""),
        "DEPTH_ENCODING": depth.get("encoding", ""),
        "DEPTH_UNIT": depth.get("unit", ""),
        "CALIBRATION_VALID": bool(calibration.get("valid")),
        "CALIBRATION_FRAME_ID": calibration.get("frameId", ""),
        "CALIBRATION_REASON": calibration.get("reason", ""),
        "OBSTACLE_OBSERVATIONS": bool(capabilities.get("obstacleObservations")),
        "LAST_ERROR": payload.get("lastError", ""),
    }


def _print_values(values: dict[str, object]) -> None:
    for key, value in values.items():
        rendered = str(value).lower() if isinstance(value, bool) else str(value)
        print(f"{key}={rendered.replace(chr(10), ' ').replace(chr(13), ' ')}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--discover", action="store_true")
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--socket", default="/run/atlas-agent/spatial.sock")
    parser.add_argument("--timeout", type=float, default=2.0)
    parser.add_argument("--sysfs-root", default=os.environ.get("ATLAS_SYSFS_ROOT", "/sys"))
    parser.add_argument("--device-id", default="")
    arguments = parser.parse_args()
    try:
        if arguments.discover:
            values = discover(Path(arguments.sysfs_root), arguments.device_id)
        else:
            values = reconcile_live_usb(
                probe(arguments.socket, arguments.timeout),
                Path(arguments.sysfs_root),
                arguments.device_id,
            )
        if arguments.json:
            print(json.dumps(values, separators=(",", ":")))
        else:
            _print_values(values if arguments.discover else flat_probe(values))
        return 0 if arguments.discover or bool(values.get("ready")) else 1
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as error:
        values = {"READY": False, "STATUS": "error", "LAST_ERROR": str(error)}
        if arguments.json:
            print(json.dumps({"ready": False, "status": "error", "lastError": str(error)}))
        else:
            _print_values(values)
        return 1


if __name__ == "__main__":
    sys.exit(main())
