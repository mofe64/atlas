import json
import struct
import threading
import time
import unittest

from atlas_spatial_runtime.spatial_stream import LatestSpatialFrame, SpatialFrame, encode_frame


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


if __name__ == "__main__":
    unittest.main()
