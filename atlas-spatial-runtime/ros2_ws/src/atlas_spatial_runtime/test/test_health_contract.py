import json
from pathlib import Path
import unittest

from atlas_spatial_runtime.depth_contract import millimetres_to_float_metres
from atlas_spatial_runtime.health_contract import (
    ImuHealthState,
    SpatialHealthState,
    VioHealthState,
    calibration_hash,
    validate_probe_request,
)
from atlas_spatial_runtime.transform_contract import CONVENTIONS, bundle_hash, validate_transform_bundle


def transform_bundle(status="verified"):
    transform = {
        "parentFrame": "body_frd", "childFrame": "oak_mount", "status": status,
        "translationM": {"x": 0.0, "y": 0.0, "z": 0.0},
        "rotationWXYZ": {"w": 1.0, "x": 0.0, "y": 0.0, "z": 0.0},
        "provenance": {"method": "test fixture"},
    }
    if status == "unmeasured":
        transform["translationM"] = None
        transform["rotationWXYZ"] = None
    return {
        "schema": "atlas.transform-bundle/v1", "bundleId": "test", "aircraftId": "test-aircraft",
        "createdAt": "2026-07-22T00:00:00Z", "conventions": dict(CONVENTIONS),
        "frames": {"body_frd": "test parent", "oak_mount": "test child"}, "transforms": [transform],
    }


class HealthContractTests(unittest.TestCase):
    def test_ready_snapshot_requires_fresh_synchronized_calibrated_streams(self):
        state = SpatialHealthState(provider="synthetic", source_id="front-depth")
        state.set_calibration([64.0, 48.0, 50.0])
        state.color.observe(1_000_000_000, 2_000_000_000, 64, 48, "rgb8")
        state.depth.observe(1_002_000_000, 2_001_000_000, 64, 48, "32FC1")
        for index in range(20):
            state.imu.observe(1_000_000_000 + index * 10_000_000, 1_800_000_000 + index * 10_000_000, "oak_imu_frame", (0.0, 0.0, 0.0), (0.0, 0.0, 9.8))
        snapshot = state.snapshot(now_ns=2_010_000_000)
        self.assertTrue(snapshot["ready"])
        self.assertTrue(snapshot["synchronized"])
        self.assertEqual(snapshot["streams"]["depth"]["unit"], "metre")
        self.assertEqual(snapshot["streams"]["imu"]["status"], "ready")

    def test_stale_or_misaligned_stream_is_degraded(self):
        state = SpatialHealthState(provider="synthetic", source_id="front-depth")
        state.set_calibration([1.0])
        state.color.observe(1_000_000_000, 1_000_000_000, 64, 48, "rgb8")
        state.depth.observe(1_100_000_000, 1_000_000_000, 64, 48, "32FC1")
        self.assertFalse(state.snapshot(now_ns=3_000_000_000)["ready"])

    def test_provider_liveness_requires_fresh_rgbd_and_required_imu(self):
        state = SpatialHealthState(provider="synthetic", source_id="front-depth")
        now_ns = 2_000_000_000
        self.assertFalse(state.provider_streams_live(now_ns=now_ns))
        state.color.observe(1_000_000_000, 1_900_000_000, 64, 48, "rgb8")
        state.depth.observe(1_000_000_000, 1_900_000_000, 64, 48, "32FC1")
        self.assertFalse(state.provider_streams_live(now_ns=now_ns))
        state.imu.observe(1_000_000_000, 1_900_000_000, "imu", (0.0, 0.0, 0.0), (0.0, 0.0, 9.8))
        self.assertTrue(state.provider_streams_live(now_ns=now_ns))
        self.assertFalse(state.provider_streams_live(now_ns=3_100_000_000))

    def test_calibration_hash_is_stable_and_probe_is_bounded(self):
        self.assertEqual(calibration_hash([1.0, 2.0]), calibration_hash([1.0, 2.0]))
        validate_probe_request(json.dumps({"protocolVersion": "1", "type": "probe"}).encode())
        with self.assertRaises(ValueError):
            validate_probe_request(json.dumps({"protocolVersion": "2", "type": "probe"}).encode())
        with self.assertRaises(ValueError):
            validate_probe_request(b"x" * 4097)

    def test_provider_depth_is_normalized_to_float_metres(self):
        import numpy

        source = numpy.array([0, 500, 2000, 65535], dtype=numpy.uint16)
        raw, _ = millimetres_to_float_metres(source.tobytes(), 2, 2, 4, False)
        converted = numpy.frombuffer(raw, dtype=numpy.float32)
        self.assertTrue(numpy.isnan(converted[0]))
        self.assertAlmostEqual(float(converted[1]), 0.5)
        self.assertAlmostEqual(float(converted[2]), 2.0)
        self.assertAlmostEqual(float(converted[3]), 65.535, places=3)

    def test_imu_health_distinguishes_unavailable_degraded_and_stale(self):
        state = SpatialHealthState(provider="synthetic", source_id="front-depth")
        self.assertEqual(state.imu.snapshot(now_ns=1_000_000_000)["status"], "unavailable")
        for index in range(10):
            state.imu.observe(100 + index, 1_000_000_000 + index * 100_000_000, "imu", (0.0, 0.0, 0.0), (0.0, 0.0, 9.8))
        self.assertEqual(state.imu.snapshot(now_ns=1_910_000_000)["status"], "degraded")
        self.assertEqual(state.imu.snapshot(now_ns=2_500_000_000)["status"], "stale")

    def test_imu_health_recovers_after_the_invalid_sample_window(self):
        imu = ImuHealthState()
        for index in range(20):
            imu.observe(1_000_000_000 + index * 10_000_000, 2_000_000_000 + index * 10_000_000, "imu", (0.0, 0.0, 0.0), (0.0, 0.0, 9.8))
        imu.observe(0, 2_200_000_000, "", (0.0, 0.0, 0.0), (0.0, 0.0, 9.8))
        self.assertEqual(imu.snapshot(now_ns=2_210_000_000)["status"], "degraded")
        imu.observe(2_300_000_000, 3_300_000_000, "imu", (0.0, 0.0, 0.0), (0.0, 0.0, 9.8))
        self.assertEqual(imu.snapshot(now_ns=3_300_000_001, minimum_rate_hz=0.0)["status"], "ready")

    def test_imu_health_distinguishes_late_samples_from_device_clock_resets(self):
        imu = ImuHealthState(clock_reset_threshold_ns=1_000_000_000)
        values = ((0.0, 0.0, 0.0), (0.0, 0.0, 9.8))
        imu.observe(43_530_058, 2_000_000_000, "imu", *values)

        # This mirrors the regression that crashed Basalt: it is only 3.9 ms
        # late, so it is an ordering anomaly rather than a clock epoch reset.
        imu.observe(39_631_391, 2_004_000_000, "imu", *values)
        imu.observe(43_530_058, 2_008_000_000, "imu", *values)
        anomaly = imu.snapshot(now_ns=2_009_000_000, minimum_rate_hz=0.0)
        self.assertEqual(anomaly["status"], "degraded")
        self.assertEqual(anomaly["outOfOrderSamples"], 1)
        self.assertEqual(anomaly["duplicateTimestampSamples"], 1)
        self.assertEqual(anomaly["resetCount"], 0)
        self.assertEqual(list(imu.captures_ns), [43_530_058])

        # A large rollback is consistent with a real device-clock restart and
        # deliberately starts a new capture epoch.
        imu.observe(2_000_000_000, 4_000_000_000, "imu", *values)
        imu.observe(100_000_000, 4_004_000_000, "imu", *values)
        reset = imu.snapshot(now_ns=4_004_000_001, minimum_rate_hz=0.0)
        self.assertEqual(reset["clockEpoch"], 1)
        self.assertEqual(reset["resetCount"], 1)
        self.assertEqual(list(imu.captures_ns), [100_000_000])

    def test_vio_health_has_non_authoritative_five_state_contract(self):
        state = VioHealthState(required_initial_samples=2)
        self.assertEqual(state.snapshot("verified", now_ns=1)["status"], "unavailable")
        state.observe(100, 1_000, (0.0, 0.0, 0.0, 1.0, 0.0, 0.0, 0.0), "vio_odom", "oak_mount")
        self.assertEqual(state.snapshot("verified", now_ns=1_100)["status"], "initializing")
        state.observe(200, 2_000, (0.1, 0.0, 0.0, 1.0, 0.0, 0.0, 0.0), "vio_odom", "oak_mount")
        ready = state.snapshot("verified", now_ns=2_100)
        self.assertEqual(ready["status"], "ready")
        self.assertEqual(ready["estimatorMode"], "live")
        self.assertFalse(ready["authoritative"])
        self.assertFalse(ready["movementAuthority"])
        self.assertTrue(ready["mappingEnabled"])
        self.assertEqual(state.snapshot("configured_unverified", now_ns=2_100)["status"], "degraded")
        self.assertEqual(state.snapshot("verified", now_ns=1_000_000_000)["status"], "stale")

    def test_vio_health_reports_tracking_lost_for_null_odometry_pose(self):
        state = VioHealthState(required_initial_samples=1)
        state.observe(
            100,
            1_000_000_000,
            (0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0),
            "vio_odom",
            "oak_mount",
        )

        snapshot = state.snapshot("configured_unverified", now_ns=1_010_000_000)

        self.assertEqual(snapshot["status"], "degraded")
        self.assertFalse(snapshot["ready"])
        self.assertEqual(
            snapshot["reason"],
            "VIO is publishing invalid poses because visual tracking is lost",
        )
        self.assertEqual(snapshot["sampleCount"], 0)
        self.assertEqual(snapshot["invalidSamples"], 1)
        self.assertFalse(
            state.tracking_live(
                now_ns=1_010_000_000,
                stale_after_ms=500.0,
            )
        )

    def test_vio_tracking_live_follows_the_newest_observation_and_freshness(self):
        state = VioHealthState(required_initial_samples=1)
        state.observe(
            100,
            1_000_000_000,
            (0.0, 0.0, 0.0, 1.0, 0.0, 0.0, 0.0),
            "vio_odom",
            "oak_mount",
        )
        self.assertTrue(
            state.tracking_live(
                now_ns=1_100_000_000,
                stale_after_ms=500.0,
            )
        )
        self.assertFalse(
            state.tracking_live(
                now_ns=1_600_000_000,
                stale_after_ms=500.0,
            )
        )

        state.observe(
            200,
            1_700_000_000,
            (0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0),
            "vio_odom",
            "oak_mount",
        )
        self.assertFalse(
            state.tracking_live(
                now_ns=1_710_000_000,
                stale_after_ms=500.0,
            )
        )
        lost = state.snapshot(
            "configured_unverified",
            now_ns=1_710_000_000,
            stale_after_ms=500.0,
        )
        self.assertEqual(lost["status"], "degraded")
        self.assertEqual(
            lost["reason"],
            "VIO is publishing invalid poses because visual tracking is lost",
        )

        state.observe(
            300,
            1_800_000_000,
            (0.1, 0.0, 0.0, 1.0, 0.0, 0.0, 0.0),
            "vio_odom",
            "oak_mount",
        )
        self.assertTrue(
            state.tracking_live(
                now_ns=1_810_000_000,
                stale_after_ms=500.0,
            )
        )

    def test_vio_timestamp_or_frame_change_starts_a_new_epoch(self):
        for capture_ns, frame_id, child_frame_id in (
            (900, "vio_odom", "oak_mount"),
            (1_100, "new_vio_odom", "oak_mount"),
            (1_100, "vio_odom", "new_oak_mount"),
        ):
            with self.subTest(capture_ns=capture_ns, frame_id=frame_id, child_frame_id=child_frame_id):
                state = VioHealthState(required_initial_samples=1)
                state.observe(
                    1_000,
                    1_000,
                    (0.0, 0.0, 0.0, 1.0, 0.0, 0.0, 0.0),
                    "vio_odom",
                    "oak_mount",
                )
                state.observe(
                    capture_ns,
                    2_000,
                    (0.1, 0.0, 0.0, 1.0, 0.0, 0.0, 0.0),
                    frame_id,
                    child_frame_id,
                )
                self.assertEqual(state.estimator_epoch, 1)

    def test_transform_contract_rejects_invented_unmeasured_geometry_and_hashes_canonically(self):
        bundle = transform_bundle("unmeasured")
        validated = validate_transform_bundle(bundle)
        self.assertEqual(validated["sha256"], bundle_hash(bundle))
        bundle["transforms"][0]["translationM"] = {"x": 0.0, "y": 0.0, "z": 0.0}
        with self.assertRaisesRegex(ValueError, "invented geometry"):
            validate_transform_bundle(bundle)

    def test_transform_contract_rejects_ambiguous_coordinate_semantics(self):
        bundle = transform_bundle()
        bundle["conventions"]["rotation"] = "unspecified"
        with self.assertRaisesRegex(ValueError, "ambiguous"):
            validate_transform_bundle(bundle)

    def test_ariadne_preliminary_oak_mount_maps_rdf_axes_into_body_frd(self):
        config_path = Path(__file__).parents[1] / "config" / "transforms.v1.json"
        bundle = validate_transform_bundle(json.loads(config_path.read_text()))
        oak = next(transform for transform in bundle["transforms"] if transform["childFrame"] == "oak_mount")
        self.assertEqual(oak["status"], "configured_unverified")
        self.assertEqual(oak["translationM"], {"x": 0.15, "y": 0.0, "z": 0.0})
        self.assertEqual(oak["rotationWXYZ"], {"w": 0.5, "x": 0.5, "y": 0.5, "z": 0.5})

        # q=(0.5, 0.5, 0.5, 0.5) cyclically maps OAK RDF basis axes:
        # right -> body right, down -> body down, forward -> body forward.
        self.assertEqual(_rotate_wxyz(oak["rotationWXYZ"], (1.0, 0.0, 0.0)), (0.0, 1.0, 0.0))
        self.assertEqual(_rotate_wxyz(oak["rotationWXYZ"], (0.0, 1.0, 0.0)), (0.0, 0.0, 1.0))
        self.assertEqual(_rotate_wxyz(oak["rotationWXYZ"], (0.0, 0.0, 1.0)), (1.0, 0.0, 0.0))

        optical = next(
            transform
            for transform in bundle["transforms"]
            if transform["parentFrame"] == "oak_mount"
            and transform["childFrame"] == "oak_rgb_camera_optical_frame"
        )
        self.assertEqual(optical["status"], "configured_unverified")
        self.assertEqual(optical["translationM"], {"x": 0.0, "y": 0.0, "z": 0.0})
        self.assertEqual(optical["rotationWXYZ"], {"w": 1.0, "x": 0.0, "y": 0.0, "z": 0.0})


def _rotate_wxyz(rotation, vector):
    w, x, y, z = (rotation[key] for key in ("w", "x", "y", "z"))
    vx, vy, vz = vector
    tx, ty, tz = 2 * (y * vz - z * vy), 2 * (z * vx - x * vz), 2 * (x * vy - y * vx)
    return (
        vx + w * tx + y * tz - z * ty,
        vy + w * ty + z * tx - x * tz,
        vz + w * tz + x * ty - y * tx,
    )


if __name__ == "__main__":
    unittest.main()
