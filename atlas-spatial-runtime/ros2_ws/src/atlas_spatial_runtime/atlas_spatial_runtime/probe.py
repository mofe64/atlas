"""Probe a running Atlas spatial runtime through its stable local contract."""

from __future__ import annotations

import argparse
import json
import socket
import sys

from . import PROTOCOL_VERSION


def probe(socket_path: str, timeout: float) -> dict:
    request = json.dumps({"protocolVersion": PROTOCOL_VERSION, "type": "probe"}).encode("utf-8") + b"\n"
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
    if not raw.endswith(b"\n"):
        raise RuntimeError("spatial runtime returned an incomplete probe response")
    response = json.loads(raw)
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
        print(f"calibration_hash={response.get('calibrationHash', '')}")
        print(f"synchronized={str(bool(response.get('synchronized'))).lower()}")
        print(f"last_error={response.get('lastError', '')}")
    raise SystemExit(0 if response.get("ready") else 1)
