# Atlas Obstacle Avoidance TODO

**Status:** Obstacle observations and avoidance control are unimplemented and
unauthorized for flight.

Spatial currently owns fresh calibrated depth and local diagnostics. It does
not expose obstacle observations, Atlas Agent has no Spatial obstacle consumer,
and no component has obstacle-avoidance movement authority.

## 1. Define the obstacle-observation contract

The contract must describe bounded, short-lived evidence of nearby obstacles,
not a persistent map.

- [ ] Choose compact points, angular sectors, or another bounded
      representation.
- [ ] Define capture time, maximum age, and explicit expiry.
- [ ] Define sensor-frame and body-FRD fields and the transform boundary.
- [ ] Define units, valid distance range, invalid values, and quality semantics.
- [ ] Define horizontal and vertical partitioning and the clearance envelope.
- [ ] Define stale, missing, malformed, and incompatible-observation behaviour.
- [ ] Add focused tests for bounds, units, frames, timestamps, and expiry.

The contract is complete when Agent can decide whether an observation is usable
without knowing that the current provider is DepthAI or the aircraft is
Ariadne.

## 2. Implement fresh obstacle extraction

Implement this bounded path:

```text
fresh depth frame
  → sample relevant pixels
  → project into the camera frame
  → apply the configured camera-to-body FRD offset
  → reduce to bounded observations
  → attach capture time and expiry
  → deliver the latest observations to Agent
```

- [ ] Consume only a fresh calibrated `uint16` millimetre depth frame.
- [ ] Reject invalid calibration, dimensions, depth values, offsets, and stale
      frames.
- [ ] Implement projection as part of the extractor, with explicit CPU and
      memory bounds.
- [ ] Load the selected aircraft-profile mounting offset and transform results
      into body FRD.
- [ ] Reduce the result into the selected compact representation.
- [ ] Publish through a bounded latest-value boundary; never queue old
      observations behind new ones.
- [ ] Add an Agent consumer that validates observations before any controller
      can use them.
- [ ] Add focused synthetic tests for clear space, frontal obstacles, invalid
      pixels, range limits, transform direction, expiry, and restart.

Do not introduce SLAM, VIO, persistent occupancy, accumulated clouds, map
epochs, ROS, Docker, or a Native visualization dependency.

## 3. Design controller authority and perform acceptance

Spatial observes; it never commands movement. Before enabling avoidance,
define:

- [ ] Which Agent component owns avoidance movement requests.
- [ ] How avoidance interacts with missions, manual control, and Follow.
- [ ] Whether it modifies setpoints, requests Hold, or uses another explicit
      PX4 boundary.
- [ ] Hold/stop behaviour when observations expire, become invalid, or stop.
- [ ] Speed, acceleration, clearance, direction, altitude, and operating
      envelopes.
- [ ] Controller ownership, watchdog, shutdown, and restart behaviour.
- [ ] Precedence between mission, Follow, operator, PX4 failsafe, and avoidance
      intent.
- [ ] Operator-visible status and audit events that do not put Native in the
      control loop.

Acceptance must proceed in order:

- [ ] Software and simulation checks for expiry, bounds, conflicts, and
      fail-closed transitions.
- [ ] Bench sensor checks with clear scenes, obstacles, and degraded input.
- [ ] Grounded-aircraft checks for mounting offset, timing, restarts, and
      resource use.
- [ ] HIL checks for PX4 interaction, controller ownership, and watchdogs.
- [ ] Constrained-flight acceptance with reviewed limits and an independent
      safety operator.

No avoidance movement may be enabled before the installed aircraft completes
the required acceptance.
