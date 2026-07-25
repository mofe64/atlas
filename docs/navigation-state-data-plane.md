# PX4 and H-Flow In-Process Health

Atlas Agent maintains a read-only, bounded view of PX4 local position,
odometry, estimator status, H-Flow optical flow, and range data. PX4 owns
sensor fusion, vehicle state estimation, stabilization, flight modes, and
failsafes.

Agent normalizes the latest MAVLink observations into timestamp-aligned state
with freshness, reset, and component health. This state remains inside the
MAVSDK telemetry adapter. There is no navigation socket, probe binary, local
protocol version, or Native capability advertisement.

Readiness is fixed code, not a configurable policy:

- top-level `status`, `ready`, and `reasons` describe the MAVSDK/PX4
  connection plus local position, odometry, and estimator health;
- `hflowStatus`, `hflowReady`, and `hflowReasons` describe fresh valid optical
  flow and range observations received through MAVSDK's direct MAVLink stream;
- `components` retains the status, age, and reason for all five observations.

Missing or degraded H-Flow therefore does not make generic PX4 navigation
unready. H-Flow readiness still requires an active PX4 connection because its
observations arrive through that connection. Generic estimator readiness does
not require the height-above-ground estimator flag or HAGL innovation ratio;
those range-dependent signals remain observable but are not universal PX4
requirements.

This state has no setpoint or movement-authority API, and no current production
controller consumes its aggregate readiness fields. The observations and
health calculation remain in process because the planned obstacle controller
is the next intended consumer. That controller must define its minimal inputs,
freshness limits, and fail-safe behavior before the internal boundary is
exposed again.

H-Flow is a PX4 installation concern, not a spatial-runtime prerequisite.
Loss of H-Flow must not make a separate depth camera unhealthy, and loss of the
depth camera must not rewrite PX4 estimator health.

The retired local socket briefly had v1 and v2 shapes. Neither version remains
in source or packaging.

See [H-Flow PX4 setup and verification](h-flow-px4-setup-and-verification.md)
for the installed parameter baseline and required flight acceptance.
