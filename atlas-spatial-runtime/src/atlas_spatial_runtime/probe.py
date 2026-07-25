"""Probe a running Atlas Spatial service."""

from __future__ import annotations

import argparse
import json
import socket
import sys

from . import PROTOCOL_VERSION


def probe(socket_path: str, timeout: float) -> dict:
    request = (
        json.dumps({"protocolVersion": PROTOCOL_VERSION, "type": "probe"}).encode()
        + b"\n"
    )
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
        connection.settimeout(timeout)
        connection.connect(socket_path)
        connection.sendall(request)
        raw = b""
        while not raw.endswith(b"\n") and len(raw) <= 64 * 1024:
            chunk = connection.recv(4096)
            if not chunk:
                break
            raw += chunk
    if len(raw) > 64 * 1024:
        raise RuntimeError("spatial runtime response exceeded 64 KiB")
    if not raw.endswith(b"\n"):
        raise RuntimeError("spatial runtime returned an incomplete response")
    response = json.loads(raw)
    if not isinstance(response, dict):
        raise RuntimeError("spatial runtime response must be an object")
    if response.get("protocolVersion") != PROTOCOL_VERSION:
        raise RuntimeError("spatial runtime protocol version mismatch")
    return response


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--socket", default="/run/atlas-agent/spatial.sock")
    parser.add_argument("--timeout", type=float, default=2.0)
    parser.add_argument("--json", action="store_true")
    arguments = parser.parse_args()
    try:
        response = probe(arguments.socket, arguments.timeout)
    except Exception as error:
        print(f"atlas-spatial-probe: {error}", file=sys.stderr)
        raise SystemExit(1)
    if arguments.json:
        print(json.dumps(response, sort_keys=True))
    else:
        print(f"status={response.get('status', 'unknown')}")
        print(f"ready={str(bool(response.get('ready'))).lower()}")
        print(f"provider={response.get('provider', '')}")
        print(f"source_id={response.get('sourceId', '')}")
        device = response.get("device") or {}
        print(f"device_id={device.get('id', '')}")
        print(f"model={device.get('model', '')}")
        print(f"usb_transport={device.get('connection', '')}")
        depth = (response.get("streams") or {}).get("depth") or {}
        calibration = response.get("calibration") or {}
        print(f"depth_frame_id={depth.get('frameId', '')}")
        print(f"calibration_valid={str(bool(calibration.get('valid'))).lower()}")
        print(f"calibration_frame_id={calibration.get('frameId', '')}")
        print(f"calibration_reason={calibration.get('reason', '')}")
        print(f"last_error={response.get('lastError', '')}")
    raise SystemExit(0 if response.get("ready") else 1)


if __name__ == "__main__":
    main()

