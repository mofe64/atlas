"""ROS subscriptions and bounded Unix-socket health service."""

from __future__ import annotations

import json
import os
import socket
import stat
import threading
import time

import rclpy
from nav_msgs.msg import Odometry
from rclpy.node import Node
from rclpy.qos import qos_profile_sensor_data
from sensor_msgs.msg import CameraInfo, Image, Imu

from .health_contract import SpatialHealthState, VioHealthState, validate_probe_request
from .transform_contract import load_transform_bundle, transform_status


def _stamp_ns(message) -> int:
    return int(message.header.stamp.sec) * 1_000_000_000 + int(message.header.stamp.nanosec)


class SpatialHealthNode(Node):
    def __init__(self) -> None:
        super().__init__("atlas_spatial_health")
        self.declare_parameter("provider", "synthetic")
        self.declare_parameter("source_id", "front-depth")
        self.declare_parameter("device_id", "")
        self.declare_parameter("model", "")
        self.declare_parameter("usb_transport", "unknown")
        self.declare_parameter("imu_required", True)
        self.declare_parameter("vio_required", True)
        self.declare_parameter("socket_path", "/run/atlas-agent/spatial.sock")
        self.declare_parameter("stale_after_ms", 1000.0)
        self.declare_parameter("sync_tolerance_ms", 25.0)
        self.declare_parameter("transform_bundle_path", "")
        self.declare_parameter("provider_startup_grace_ms", 30000.0)
        self.declare_parameter("provider_stale_exit_after_ms", 5000.0)
        self.declare_parameter("vio_startup_grace_ms", 30000.0)
        self.declare_parameter("vio_stale_exit_after_ms", 5000.0)

        self.state = SpatialHealthState(
            provider=str(self.get_parameter("provider").value),
            source_id=str(self.get_parameter("source_id").value),
            device_id=str(self.get_parameter("device_id").value),
            model=str(self.get_parameter("model").value),
            usb_transport=str(self.get_parameter("usb_transport").value),
            imu_required=bool(self.get_parameter("imu_required").value),
        )
        self._lock = threading.Lock()
        self._stop = threading.Event()
        self._provider_failure_started_ns = None
        self._vio_failure_started_ns = None
        self._socket_path = str(self.get_parameter("socket_path").value)
        transform_path = str(self.get_parameter("transform_bundle_path").value)
        if not transform_path:
            raise ValueError("transform_bundle_path is required")
        self._transform_bundle = load_transform_bundle(transform_path)
        self._body_to_oak_status = transform_status(self._transform_bundle, "body_frd", "oak_mount")
        self._vio_state = VioHealthState()
        self._server = self._open_server(self._socket_path)

        self.create_subscription(Image, "/atlas/spatial/color/image_raw", self._color, qos_profile_sensor_data)
        self.create_subscription(Image, "/atlas/spatial/aligned_depth/image_rect", self._depth, qos_profile_sensor_data)
        self.create_subscription(CameraInfo, "/atlas/spatial/aligned_depth/camera_info", self._camera_info, qos_profile_sensor_data)
        # Health observes the provider-side stream so timestamp anomalies remain
        # visible even when the safety gate drops them before Madgwick.
        self.create_subscription(
            Imu,
            "/atlas/spatial/provider/imu/data_raw",
            self._imu,
            qos_profile_sensor_data,
        )
        self.create_subscription(Odometry, "/atlas/spatial/vio/odometry", self._vio, qos_profile_sensor_data)

        self._server_thread = threading.Thread(target=self._serve, name="atlas-spatial-health-socket", daemon=True)
        self._server_thread.start()
        self.create_timer(0.5, self._supervise_provider)

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

    def _imu(self, message: Imu) -> None:
        capture_ns = int(message.header.stamp.sec) * 1_000_000_000 + int(message.header.stamp.nanosec)
        with self._lock:
            self.state.imu.observe(
                capture_ns, time.monotonic_ns(), message.header.frame_id,
                (message.angular_velocity.x, message.angular_velocity.y, message.angular_velocity.z),
                (message.linear_acceleration.x, message.linear_acceleration.y, message.linear_acceleration.z),
            )

    def _vio(self, message: Odometry) -> None:
        pose = message.pose.pose
        with self._lock:
            self._vio_state.observe(
                _stamp_ns(message),
                time.monotonic_ns(),
                (
                    pose.position.x,
                    pose.position.y,
                    pose.position.z,
                    pose.orientation.w,
                    pose.orientation.x,
                    pose.orientation.y,
                    pose.orientation.z,
                ),
                message.header.frame_id,
                message.child_frame_id,
            )

    def _supervise_provider(self) -> None:
        """Terminate this required node when the provider stops delivering data.

        The launch description shuts down when this node exits. That makes the
        Docker process exit too, allowing systemd to restart the entire camera
        failure boundary instead of leaving a live container around dead RGB-D.
        """
        now_ns = time.monotonic_ns()
        startup_grace_ns = int(float(self.get_parameter("provider_startup_grace_ms").value) * 1_000_000)
        if now_ns - self.state.started_monotonic_ns < startup_grace_ns:
            return
        stale_after_ms = float(self.get_parameter("stale_after_ms").value)
        with self._lock:
            live = self.state.provider_streams_live(now_ns=now_ns, stale_after_ms=stale_after_ms)
            ages = self.state.provider_stream_ages_ms(now_ns=now_ns)
        if not live:
            if self._provider_failure_started_ns is None:
                self._provider_failure_started_ns = now_ns
                return
            exit_after_ns = int(
                float(self.get_parameter("provider_stale_exit_after_ms").value)
                * 1_000_000
            )
            if now_ns - self._provider_failure_started_ns < exit_after_ns:
                return
            self.get_logger().fatal(
                "provider streams remained unavailable or stale; shutting down the spatial failure boundary: "
                + json.dumps(
                    ages,
                    allow_nan=False,
                    separators=(",", ":"),
                    sort_keys=True,
                )
            )
            rclpy.shutdown()
            return
        self._provider_failure_started_ns = None
        self._supervise_vio(now_ns, stale_after_ms)

    def _supervise_vio(self, now_ns: int, stale_after_ms: float) -> None:
        """Restart the complete coordinate boundary after sustained VIO loss."""
        if not bool(self.get_parameter("vio_required").value):
            self._vio_failure_started_ns = None
            return
        startup_grace_ns = int(
            float(self.get_parameter("vio_startup_grace_ms").value) * 1_000_000
        )
        if now_ns - self.state.started_monotonic_ns < startup_grace_ns:
            return
        with self._lock:
            tracking_live = self._vio_state.tracking_live(
                now_ns=now_ns,
                stale_after_ms=stale_after_ms,
            )
            vio_snapshot = self._vio_state.snapshot(
                self._body_to_oak_status,
                now_ns=now_ns,
                stale_after_ms=stale_after_ms,
            )
        if tracking_live:
            self._vio_failure_started_ns = None
            return
        if self._vio_failure_started_ns is None:
            self._vio_failure_started_ns = now_ns
            return
        exit_after_ns = int(
            float(self.get_parameter("vio_stale_exit_after_ms").value) * 1_000_000
        )
        if now_ns - self._vio_failure_started_ns < exit_after_ns:
            return
        self.get_logger().fatal(
            "VIO remained unavailable, stale, or invalid; restarting the complete "
            "spatial coordinate boundary: "
            + json.dumps(
                vio_snapshot,
                allow_nan=False,
                separators=(",", ":"),
                sort_keys=True,
            )
        )
        rclpy.shutdown()

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
                        snapshot["vio"] = self._vio_state.snapshot(self._body_to_oak_status)
                        vio_available = snapshot["vio"]["status"] != "unavailable"
                        snapshot["capabilities"]["vio"] = vio_available
                        snapshot["transformBundle"] = {
                            "bundleId": self._transform_bundle["bundleId"],
                            "sha256": self._transform_bundle["sha256"],
                            "bodyToOakStatus": self._body_to_oak_status,
                            "bodyToHFlowStatus": transform_status(self._transform_bundle, "body_frd", "hflow_flow_frd"),
                        }
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
    except KeyboardInterrupt:
        pass
    finally:
        node.destroy_node()
        if rclpy.ok():
            rclpy.shutdown()
