"""Bounded Unix-socket server for the local Spatial health contract."""

from __future__ import annotations

import json
import os
from pathlib import Path
import socket
import threading
from typing import Callable

from .health_contract import validate_probe_request


class HealthServer:
    def __init__(self, socket_path: str, snapshot: Callable[[], dict]) -> None:
        path = Path(socket_path)
        if not path.is_absolute():
            raise ValueError("spatial socket path must be absolute")
        self._path = path
        self._snapshot = snapshot
        self._stop = threading.Event()
        self._socket: socket.socket | None = None
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        self._path.parent.mkdir(parents=True, exist_ok=True)
        try:
            self._path.unlink()
        except FileNotFoundError:
            pass
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        server.bind(str(self._path))
        os.chmod(self._path, 0o660)
        server.listen(4)
        server.settimeout(0.25)
        self._socket = server
        self._thread = threading.Thread(
            target=self._serve, name="atlas-spatial-health", daemon=True
        )
        self._thread.start()

    def close(self) -> None:
        self._stop.set()
        if self._socket is not None:
            self._socket.close()
        if self._thread is not None:
            self._thread.join(timeout=2.0)
        try:
            self._path.unlink()
        except FileNotFoundError:
            pass

    def _serve(self) -> None:
        assert self._socket is not None
        while not self._stop.is_set():
            try:
                connection, _ = self._socket.accept()
            except TimeoutError:
                continue
            except OSError:
                if self._stop.is_set():
                    return
                raise
            with connection:
                connection.settimeout(1.0)
                try:
                    request = _read_line(connection, 4096)
                    validate_probe_request(request)
                    response = self._snapshot()
                except Exception as error:
                    response = {"ready": False, "status": "error", "lastError": str(error)}
                connection.sendall(
                    json.dumps(response, separators=(",", ":")).encode("utf-8") + b"\n"
                )


def _read_line(connection: socket.socket, limit: int) -> bytes:
    raw = b""
    while not raw.endswith(b"\n") and len(raw) <= limit:
        chunk = connection.recv(min(4096, limit + 1 - len(raw)))
        if not chunk:
            break
        raw += chunk
    if len(raw) > limit:
        raise ValueError("probe request exceeds 4096 bytes")
    if not raw.endswith(b"\n"):
        raise ValueError("probe request is incomplete")
    return raw[:-1]

