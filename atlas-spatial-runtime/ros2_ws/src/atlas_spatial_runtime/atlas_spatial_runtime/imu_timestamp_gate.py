"""Protect stateful IMU consumers from non-monotonic provider timestamps."""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
import time

import rclpy
from rclpy.node import Node
from rclpy.qos import qos_profile_sensor_data
from sensor_msgs.msg import Imu


class TimestampAction(Enum):
    ACCEPT = "accept"
    DROP = "drop"
    RESTART = "restart"


@dataclass
class MonotonicTimestampGate:
    """Classify IMU stamps without changing the provider's clock."""

    clock_reset_threshold_ns: int = 1_000_000_000
    last_accepted_ns: int = 0
    invalid_samples: int = 0
    duplicate_samples: int = 0
    out_of_order_samples: int = 0
    clock_resets: int = 0

    def observe(self, capture_ns: int) -> TimestampAction:
        if capture_ns <= 0:
            self.invalid_samples += 1
            return TimestampAction.DROP
        if not self.last_accepted_ns or capture_ns > self.last_accepted_ns:
            self.last_accepted_ns = capture_ns
            return TimestampAction.ACCEPT

        regression_ns = self.last_accepted_ns - capture_ns
        if regression_ns >= self.clock_reset_threshold_ns:
            self.clock_resets += 1
            return TimestampAction.RESTART
        if regression_ns == 0:
            self.duplicate_samples += 1
        else:
            self.out_of_order_samples += 1
        return TimestampAction.DROP


class ImuTimestampGateNode(Node):
    """Publish only strictly increasing IMU samples to stateful filters."""

    def __init__(self) -> None:
        super().__init__("atlas_spatial_imu_timestamp_gate")
        self.declare_parameter("clock_reset_threshold_ms", 1000.0)
        threshold_ms = float(self.get_parameter("clock_reset_threshold_ms").value)
        if threshold_ms <= 0:
            raise ValueError("clock_reset_threshold_ms must be positive")
        self._gate = MonotonicTimestampGate(
            clock_reset_threshold_ns=int(threshold_ms * 1_000_000)
        )
        self._last_drop_log_ns = 0
        self._publisher = self.create_publisher(
            Imu,
            "/atlas/spatial/provider/imu/data_monotonic",
            qos_profile_sensor_data,
        )
        self.create_subscription(
            Imu,
            "/atlas/spatial/provider/imu/data_raw",
            self._handle_imu,
            qos_profile_sensor_data,
        )

    def _handle_imu(self, message: Imu) -> None:
        capture_ns = (
            int(message.header.stamp.sec) * 1_000_000_000
            + int(message.header.stamp.nanosec)
        )
        action = self._gate.observe(capture_ns)
        if action is TimestampAction.ACCEPT:
            self._publisher.publish(message)
            return
        if action is TimestampAction.RESTART:
            raise RuntimeError(
                "DepthAI IMU clock reset detected; restarting the complete spatial "
                "provider boundary so all estimator state is reset"
            )

        now_ns = time.monotonic_ns()
        if now_ns - self._last_drop_log_ns >= 5_000_000_000:
            self._last_drop_log_ns = now_ns
            self.get_logger().warning(
                "dropped invalid or non-monotonic DepthAI IMU sample "
                f"(invalid={self._gate.invalid_samples}, "
                f"duplicate={self._gate.duplicate_samples}, "
                f"out_of_order={self._gate.out_of_order_samples})"
            )


def main() -> None:
    rclpy.init()
    node = ImuTimestampGateNode()
    try:
        rclpy.spin(node)
    except KeyboardInterrupt:
        pass
    finally:
        node.destroy_node()
        if rclpy.ok():
            rclpy.shutdown()
