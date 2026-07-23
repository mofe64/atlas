# PX4 and H-Flow Navigation State

Atlas Agent maintains a read-only, bounded view of PX4 local position,
odometry, estimator status, H-Flow optical flow, and range data. This state is
the flight-control pose foundation for Indoor Explore; it contains no setpoint
or movement-authority API.

PX4 owns H-Flow fusion and vehicle stabilization. Agent normalizes the MAVLink
observations into capture-aligned state with freshness, reset, and component
health. The current aircraft-local diagnostic socket supports `latest` and
bounded `sampleAt` queries and is exercised by `atlas-navigation-probe`.

The future Indoor Explore controller will consume the navigation plane inside
Agent, record the PX4-local start point, and remain in Hold whenever local
position or estimator health becomes unavailable. Spatial VIO is used to place
depth in its local map; it is not injected into PX4.

See [H-Flow PX4 setup and verification](h-flow-px4-setup-and-verification.md)
for the installed parameter baseline and required GPS-denied flight acceptance.
