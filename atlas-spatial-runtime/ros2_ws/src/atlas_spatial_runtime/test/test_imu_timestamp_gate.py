import unittest

from atlas_spatial_runtime.imu_timestamp_gate import (
    MonotonicTimestampGate,
    TimestampAction,
)


class MonotonicTimestampGateTests(unittest.TestCase):
    def test_accepts_strictly_increasing_positive_timestamps(self):
        gate = MonotonicTimestampGate()

        self.assertIs(gate.observe(1_000_000_000), TimestampAction.ACCEPT)
        self.assertIs(gate.observe(1_002_500_000), TimestampAction.ACCEPT)
        self.assertEqual(gate.last_accepted_ns, 1_002_500_000)

    def test_drops_invalid_duplicate_and_short_regression_without_retiming(self):
        gate = MonotonicTimestampGate()

        self.assertIs(gate.observe(0), TimestampAction.DROP)
        self.assertIs(gate.observe(2_000_000_000), TimestampAction.ACCEPT)
        self.assertIs(gate.observe(2_000_000_000), TimestampAction.DROP)
        self.assertIs(gate.observe(1_996_100_000), TimestampAction.DROP)

        self.assertEqual(gate.invalid_samples, 1)
        self.assertEqual(gate.duplicate_samples, 1)
        self.assertEqual(gate.out_of_order_samples, 1)
        self.assertEqual(gate.last_accepted_ns, 2_000_000_000)

    def test_clock_epoch_change_requires_complete_provider_restart(self):
        gate = MonotonicTimestampGate(clock_reset_threshold_ns=1_000_000_000)

        self.assertIs(gate.observe(5_000_000_000), TimestampAction.ACCEPT)
        self.assertIs(gate.observe(3_999_999_999), TimestampAction.RESTART)

        self.assertEqual(gate.clock_resets, 1)
        self.assertEqual(gate.last_accepted_ns, 5_000_000_000)


if __name__ == "__main__":
    unittest.main()
