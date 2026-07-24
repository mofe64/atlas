import json
import socket
import struct
import threading
import time
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

import rclpy
from nav_msgs.msg import Odometry
from rclpy.parameter import Parameter

from atlas_spatial_runtime.spatial_stream import LatestSpatialFrame, SpatialFrame, encode_frame
from atlas_spatial_runtime.spatial_stream_node import SpatialStreamNode, _validated_pose


def frame(sequence: int, points: int = 2) -> SpatialFrame:
    return SpatialFrame(
        header={
            "protocolVersion": "1",
            "sourceId": "front-depth",
            "streamEpoch": "epoch-1",
            "sequence": sequence,
            "frameId": "vio_local",
            "voxelSizeM": 0.05,
            "pointCount": points,
        },
        xyz_f32_le=b"\x00" * (points * 12),
    )


class SpatialStreamTest(unittest.TestCase):
    def test_pose_is_normalized_before_crossing_the_socket_contract(self):
        message = Odometry()
        message.header.stamp.sec = 1
        message.header.frame_id = "vio_local"
        message.child_frame_id = "oak_mount"
        message.pose.pose.orientation.w = 1.01

        pose = _validated_pose(message)

        self.assertEqual(pose["orientationWxyz"], [1.0, 0.0, 0.0, 0.0])

    def test_degraded_zero_quaternion_is_not_valid_pose_metadata(self):
        message = Odometry()
        message.header.stamp.sec = 1
        message.header.frame_id = "vio_local"
        message.child_frame_id = "oak_mount"
        message.pose.pose.orientation.w = 0.0
        message.pose.pose.orientation.x = 0.0
        message.pose.pose.orientation.y = 0.0
        message.pose.pose.orientation.z = 0.0

        with self.assertRaisesRegex(ValueError, "quaternion must be normalized"):
            _validated_pose(message)

    def test_frame_contains_one_bounded_header_and_the_complete_cloud(self):
        encoded = encode_frame(frame(7))
        header_size = struct.unpack(">I", encoded[:4])[0]
        header = json.loads(encoded[4 : 4 + header_size])
        self.assertEqual(header["sequence"], 7)
        self.assertEqual(header["xyzByteLength"], 24)
        self.assertEqual(len(encoded[4 + header_size :]), 24)

    def test_mailbox_replaces_stale_whole_frames(self):
        mailbox = LatestSpatialFrame()
        mailbox.publish(frame(1))
        mailbox.publish(frame(2, points=3))
        revision, latest = mailbox.wait_after(0, timeout=0.01)
        self.assertEqual(revision, 2)
        self.assertEqual(latest.header["sequence"], 2)
        self.assertEqual(len(latest.xyz_f32_le), 36)

    def test_waiter_is_woken_by_the_next_snapshot(self):
        mailbox = LatestSpatialFrame()
        result = []
        waiter = threading.Thread(target=lambda: result.append(mailbox.wait_after(0, timeout=1)))
        waiter.start()
        time.sleep(0.01)
        mailbox.publish(frame(1))
        waiter.join()
        self.assertEqual(result[0][0], 1)

    def test_payload_length_must_match_point_count(self):
        invalid = frame(1)
        invalid = SpatialFrame(invalid.header, b"\x00" * 12)
        with self.assertRaisesRegex(ValueError, "exactly 12 bytes"):
            encode_frame(invalid)

    def test_maximum_snapshot_is_one_complete_payload(self):
        encoded = encode_frame(frame(8, points=100_000))
        header_size = struct.unpack(">I", encoded[:4])[0]
        header = json.loads(encoded[4 : 4 + header_size])
        self.assertEqual(header["pointCount"], 100_000)
        self.assertEqual(header["xyzByteLength"], 1_200_000)
        self.assertEqual(len(encoded) - 4 - header_size, 1_200_000)

    def test_socket_worker_does_not_replace_rclpy_service_clients(self):
        owns_context = not rclpy.ok()
        if owns_context:
            rclpy.init()
        node = None
        connection = None
        try:
            with TemporaryDirectory() as directory:
                socket_path = Path(directory) / "spatial-cloud.sock"
                node = SpatialStreamNode(
                    parameter_overrides=[
                        Parameter("cloud_socket_path", value=str(socket_path))
                    ]
                )
                connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                connection.connect(str(socket_path))

                deadline = time.monotonic() + 1.0
                while (
                    not node._socket_client_threads
                    and time.monotonic() < deadline
                ):
                    time.sleep(0.01)

                self.assertTrue(node._socket_client_threads)
                self.assertEqual(list(node.clients), [])
                rclpy.spin_once(node, timeout_sec=0.01)
        finally:
            if connection is not None:
                connection.close()
            if node is not None:
                node.destroy_node()
            if owns_context:
                rclpy.shutdown()


if __name__ == "__main__":
    unittest.main()
