"""Streams complete live-cloud snapshots to Atlas Agent over a Unix socket."""

from __future__ import annotations

import math
import os
import socket
import stat
import threading
import time
import uuid

import rclpy
from nav_msgs.msg import Odometry
from rclpy.node import Node
from rclpy.qos import qos_profile_sensor_data
from sensor_msgs.msg import PointCloud2, PointField

from .spatial_stream import LatestSpatialFrame, MAXIMUM_POINTS, SpatialFrame, encode_frame


def _stamp_ns(stamp) -> int:
    return int(stamp.sec) * 1_000_000_000 + int(stamp.nanosec)


def _validated_pose(message: Odometry) -> dict:
    capture_ns = _stamp_ns(message.header.stamp)
    frame_id = message.header.frame_id
    child_frame_id = message.child_frame_id
    pose = message.pose.pose
    position = [
        float(pose.position.x),
        float(pose.position.y),
        float(pose.position.z),
    ]
    orientation = [
        float(pose.orientation.w),
        float(pose.orientation.x),
        float(pose.orientation.y),
        float(pose.orientation.z),
    ]
    if capture_ns <= 0 or not frame_id.strip() or not child_frame_id.strip():
        raise ValueError("pose timestamp and frame IDs must be present")
    if not all(math.isfinite(value) for value in position + orientation):
        raise ValueError("pose position and quaternion must be finite")
    norm = math.sqrt(sum(component * component for component in orientation))
    if not 0.9 <= norm <= 1.1:
        raise ValueError("pose quaternion must be normalized")
    orientation = [component / norm for component in orientation]
    return {
        "captureNs": capture_ns,
        "frameId": frame_id,
        "childFrameId": child_frame_id,
        "position": position,
        "orientationWxyz": orientation,
    }


class SpatialStreamNode(Node):
    def __init__(self, *, parameter_overrides=None) -> None:
        super().__init__("atlas_spatial_stream", parameter_overrides=parameter_overrides)
        self.declare_parameter("source_id", "front-depth")
        self.declare_parameter("cloud_socket_path", "/run/atlas-agent/spatial-cloud.sock")
        self.declare_parameter("maximum_points", MAXIMUM_POINTS)
        self.declare_parameter("voxel_size_m", 0.05)

        self._source_id = str(self.get_parameter("source_id").value)
        self._maximum_points = int(self.get_parameter("maximum_points").value)
        self._voxel_size_m = float(self.get_parameter("voxel_size_m").value)
        if not 0 < self._maximum_points <= MAXIMUM_POINTS:
            raise ValueError(f"maximum_points must be between 1 and {MAXIMUM_POINTS}")
        if not self._source_id:
            raise ValueError("source_id is required")

        self._epoch = str(uuid.uuid4())
        self._sequence = 0
        self._latest_pose: dict | None = None
        self._invalid_pose_warning_active = False
        self._last_capture_ns = 0
        self._last_frame_id = ""
        self._mailbox = LatestSpatialFrame()
        self._stop = threading.Event()
        # rclpy.Node owns an internal `_clients` collection for ROS service
        # clients. Keep socket worker bookkeeping under an Atlas-specific name
        # so the executor never mistakes a Python thread for a ROS entity.
        self._socket_client_threads: set[threading.Thread] = set()
        self._socket_clients_lock = threading.Lock()
        self._socket_path = str(self.get_parameter("cloud_socket_path").value)
        self._server = self._open_server(self._socket_path)
        self._server_thread = threading.Thread(
            target=self._serve, name="atlas-spatial-cloud-socket", daemon=True
        )
        self._server_thread.start()

        self.create_subscription(
            PointCloud2,
            "/atlas/spatial/map/points",
            self._on_cloud,
            qos_profile_sensor_data,
        )
        self.create_subscription(
            Odometry,
            "/atlas/spatial/vio/odometry",
            self._on_odometry,
            qos_profile_sensor_data,
        )

    @staticmethod
    def _open_server(socket_path: str) -> socket.socket:
        if not os.path.isabs(socket_path):
            raise ValueError("spatial cloud socket path must be absolute")
        os.makedirs(os.path.dirname(socket_path), mode=0o750, exist_ok=True)
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
            server.listen(2)
            server.settimeout(0.5)
        except Exception:
            server.close()
            raise
        return server

    def _on_odometry(self, message: Odometry) -> None:
        try:
            self._latest_pose = _validated_pose(message)
        except ValueError as error:
            # The cloud is already expressed in its map frame, so pose is
            # optional metadata. Never let degraded VIO poison a valid cloud,
            # and never retain an older pose after odometry becomes invalid.
            self._latest_pose = None
            if not self._invalid_pose_warning_active:
                self.get_logger().warning(f"omitting invalid spatial pose: {error}")
            self._invalid_pose_warning_active = True
            return
        self._invalid_pose_warning_active = False

    def _on_cloud(self, message: PointCloud2) -> None:
        capture_ns = _stamp_ns(message.header.stamp)
        if capture_ns <= 0 or not message.header.frame_id:
            self.get_logger().warning("dropping cloud without timestamp or frame")
            return
        if capture_ns < self._last_capture_ns or (
            self._last_frame_id and message.header.frame_id != self._last_frame_id
        ):
            self._epoch = str(uuid.uuid4())
            self._sequence = 0
        self._last_capture_ns = capture_ns
        self._last_frame_id = message.header.frame_id

        try:
            payload = self._validated_xyz(message)
        except ValueError as error:
            self.get_logger().warning(f"dropping invalid complete cloud: {error}")
            return
        self._sequence += 1
        self._mailbox.publish(
            SpatialFrame(
                header={
                    "protocolVersion": "1",
                    "sourceId": self._source_id,
                    "streamEpoch": self._epoch,
                    "sequence": self._sequence,
                    "observedAtUnixMs": time.time_ns() // 1_000_000,
                    "captureNs": capture_ns,
                    "frameId": message.header.frame_id,
                    "voxelSizeM": self._voxel_size_m,
                    "pointCount": int(message.width) * int(message.height),
                    "pose": self._latest_pose,
                },
                xyz_f32_le=payload,
            )
        )

    def _validated_xyz(self, message: PointCloud2) -> bytes:
        point_count = int(message.width) * int(message.height)
        if not 0 < point_count <= self._maximum_points:
            raise ValueError("point count is outside the configured bound")
        expected_fields = [("x", 0), ("y", 4), ("z", 8)]
        actual_fields = [
            (field.name, int(field.offset))
            for field in message.fields
            if field.datatype == PointField.FLOAT32 and field.count == 1
        ]
        if message.is_bigendian or message.point_step != 12 or actual_fields != expected_fields:
            raise ValueError("cloud must be tightly packed little-endian XYZ float32")
        if message.row_step != message.width * 12:
            raise ValueError("cloud rows must not contain padding")
        payload = bytes(message.data)
        if len(payload) != point_count * 12:
            raise ValueError("cloud payload length does not match point count")
        return payload

    def _serve(self) -> None:
        while not self._stop.is_set():
            try:
                connection, _ = self._server.accept()
            except socket.timeout:
                continue
            except OSError:
                break
            worker = threading.Thread(
                target=self._stream_client,
                args=(connection,),
                name="atlas-spatial-cloud-client",
                daemon=True,
            )
            with self._socket_clients_lock:
                self._socket_client_threads.add(worker)
            worker.start()

    def _stream_client(self, connection: socket.socket) -> None:
        revision = 0
        try:
            connection.settimeout(2.0)
            while not self._stop.is_set():
                latest = self._mailbox.wait_after(revision, timeout=0.5)
                if latest is None:
                    continue
                revision, frame = latest
                connection.sendall(encode_frame(frame))
        except (BrokenPipeError, ConnectionError, socket.timeout, OSError):
            pass
        finally:
            connection.close()
            current = threading.current_thread()
            with self._socket_clients_lock:
                self._socket_client_threads.discard(current)

    def destroy_node(self) -> bool:
        self._stop.set()
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
    node = SpatialStreamNode()
    try:
        rclpy.spin(node)
    except KeyboardInterrupt:
        pass
    finally:
        node.destroy_node()
        if rclpy.ok():
            rclpy.shutdown()
