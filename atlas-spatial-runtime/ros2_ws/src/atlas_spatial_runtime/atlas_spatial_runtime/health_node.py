"""ROS subscriptions and bounded Unix-socket health service."""

from __future__ import annotations

import json
import os
import socket
import stat
import threading
import time

import rclpy
from rclpy.node import Node
from rclpy.qos import qos_profile_sensor_data
from sensor_msgs.msg import CameraInfo, Image

from .health_contract import SpatialHealthState, validate_probe_request


def _stamp_ns(message: Image) -> int:
    return int(message.header.stamp.sec) * 1_000_000_000 + int(message.header.stamp.nanosec)


class SpatialHealthNode(Node):
    def __init__(self) -> None:
        super().__init__("atlas_spatial_health")
        self.declare_parameter("provider", "synthetic")
        self.declare_parameter("source_id", "front-depth")
        self.declare_parameter("device_id", "")
        self.declare_parameter("model", "")
        self.declare_parameter("usb_transport", "unknown")
        self.declare_parameter("imu_available", False)
        self.declare_parameter("socket_path", "/run/atlas-agent/spatial.sock")
        self.declare_parameter("stale_after_ms", 1000.0)
        self.declare_parameter("sync_tolerance_ms", 25.0)

        self.state = SpatialHealthState(
            provider=str(self.get_parameter("provider").value),
            source_id=str(self.get_parameter("source_id").value),
            device_id=str(self.get_parameter("device_id").value),
            model=str(self.get_parameter("model").value),
            usb_transport=str(self.get_parameter("usb_transport").value),
            imu_available=bool(self.get_parameter("imu_available").value),
        )
        self._lock = threading.Lock()
        self._stop = threading.Event()
        self._socket_path = str(self.get_parameter("socket_path").value)
        self._server = self._open_server(self._socket_path)

        self.create_subscription(Image, "/atlas/spatial/color/image_raw", self._color, qos_profile_sensor_data)
        self.create_subscription(Image, "/atlas/spatial/aligned_depth/image_rect", self._depth, qos_profile_sensor_data)
        self.create_subscription(CameraInfo, "/atlas/spatial/aligned_depth/camera_info", self._camera_info, qos_profile_sensor_data)

        self._server_thread = threading.Thread(target=self._serve, name="atlas-spatial-health-socket", daemon=True)
        self._server_thread.start()

    @staticmethod
    def _open_server(socket_path: str) -> socket.socket:
        if not os.path.isabs(socket_path):
            raise ValueError("spatial socket path must be absolute")
        parent = os.path.dirname(socket_path)
        os.makedirs(parent, mode=0o750, exist_ok=True)
        try:
            existing = os.lstat(socket_path)
        except FileNotFoundError:
            existing = None
        if existing is not None:
            if not stat.S_ISSOCK(existing.st_mode):
                raise RuntimeError(f"refusing to replace non-socket path: {socket_path}")
            os.unlink(socket_path)
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            server.bind(socket_path)
            os.chmod(socket_path, 0o660)
            server.listen(4)
            server.settimeout(0.5)
        except Exception:
            server.close()
            raise
        return server

    def _color(self, message: Image) -> None:
        with self._lock:
            self.state.color.observe(_stamp_ns(message), time.monotonic_ns(), message.width, message.height, message.encoding)

    def _depth(self, message: Image) -> None:
        with self._lock:
            if message.encoding != "32FC1":
                self.state.last_error = f"aligned depth encoding must be 32FC1, got {message.encoding}"
                return
            self.state.depth.observe(_stamp_ns(message), time.monotonic_ns(), message.width, message.height, message.encoding)

    def _camera_info(self, message: CameraInfo) -> None:
        values = [float(message.width), float(message.height), *message.k, *message.d, *message.r, *message.p]
        with self._lock:
            self.state.set_calibration(values)

    def _serve(self) -> None:
        server = self._server
        while not self._stop.is_set():
            try:
                connection, _ = server.accept()
            except socket.timeout:
                continue
            except OSError:
                break
            with connection:
                connection.settimeout(1.0)
                try:
                    raw = b""
                    while not raw.endswith(b"\n") and len(raw) <= 4096:
                        chunk = connection.recv(1024)
                        if not chunk:
                            break
                        raw += chunk
                    validate_probe_request(raw.strip())
                    with self._lock:
                        snapshot = self.state.snapshot(
                            stale_after_ms=float(self.get_parameter("stale_after_ms").value),
                            sync_tolerance_ms=float(self.get_parameter("sync_tolerance_ms").value),
                        )
                    response = snapshot
                except Exception as error:  # response remains bounded and contains no traceback
                    response = {"protocolVersion": "1", "ready": False, "status": "error", "lastError": str(error)[:500]}
                try:
                    connection.sendall(json.dumps(response, separators=(",", ":")).encode("utf-8") + b"\n")
                except OSError:
                    # A diagnostic client may time out or disconnect. That is
                    # not a reason to lose the health server thread.
                    continue

    def destroy_node(self) -> bool:
        self._stop.set()
        if self._server is not None:
            self._server.close()
        self._server_thread.join(timeout=1.0)
        try:
            existing = os.lstat(self._socket_path)
            if stat.S_ISSOCK(existing.st_mode):
                os.unlink(self._socket_path)
        except FileNotFoundError:
            pass
        return super().destroy_node()


def main() -> None:
    rclpy.init()
    node = SpatialHealthNode()
    try:
        rclpy.spin(node)
    finally:
        node.destroy_node()
        rclpy.shutdown()
