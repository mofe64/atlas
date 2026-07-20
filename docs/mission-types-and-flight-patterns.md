# Mission types and flight patterns

This guide explains how Atlas turns an operator's intent into a reviewed,
immutable flight plan and how that plan is executed on an aircraft. It is the
canonical reference for the generic mission system. Incident response uses the
same planner and run machinery, but adds dispatch-specific review and arrival
actions; see [Incident dispatch](incident-dispatch.md).

## The short mental model

Atlas deliberately separates four things that are easy to confuse:

1. A **mission definition** is editable operator intent, such as a polygon to
   scan or a route to follow.
2. A **generated plan** is the exact, reviewed sequence of actions and
   waypoints produced from that definition. It is immutable.
3. A **mission run** binds one generated plan to one aircraft and records its
   execution state.
4. The **Agent** translates the plan into MAVSDK/PX4 operations and reports
   durable acknowledgements and progress back to Native.

Editing a definition never changes a plan that has already been reviewed or a
run that is already in progress. To fly changed geometry, generate and review a
new plan.

```text
editable definition
       |
       v
generate + validate geometry
       |
       v
immutable generated plan --upload--> aircraft mission storage
       |                                  |
       +-------- mission run <------------+
                         |
                         v
              start / pause / resume / cancel / RTL
```

## Supported generic mission types

| Mission type | Flight pattern | Operator supplies | Atlas generates | Typical use |
| --- | --- | --- | --- | --- |
| `WAYPOINT` | `DIRECT_WAYPOINTS` | One or more ordered points | The same ordered route, with defaults and per-point overrides resolved | Point inspection, transit, custom paths |
| `AREA_SCAN` | `LAWN_MOWER` | A polygon, sweep direction, and optional lane spacing | Alternating parallel lanes clipped to the polygon | Systematic area coverage |
| `ROUTE_SCAN` | `ROUTE_FOLLOW` | An ordered route and optional sample spacing | A densified route along the supplied centerline | Road, fence, pipeline, or corridor inspection |

The mission type describes the operator's intent. The flight pattern names the
algorithm used to turn that intent into waypoints. Atlas currently supports the
three pairings above; arbitrary combinations are rejected rather than guessed.

All generic missions share these base limits:

- altitude: 2–120 metres;
- speed: 0.5–15 metres per second;
- valid latitude/longitude coordinates;
- at least the minimum geometry required by the selected pattern.

These application limits do not replace site procedures, local aviation rules,
aircraft performance limits, or the pilot's responsibility to review the plan.

## Plan generation and common action order

After the flight pattern produces waypoints, Atlas wraps them in a common action
sequence. Optional actions are omitted when the definition does not request
them.

1. `TAKEOFF`
2. `SET_SPEED`
3. `SET_CAMERA_MODE`
4. `SET_CAMERA_ZOOM`
5. `SET_GIMBAL_ORIENTATION`
6. `START_RECORDING`
7. `START_PERCEPTION`
8. one `NAVIGATE_TO` action per generated waypoint, including any waypoint
   view overrides
9. `STOP_PERCEPTION`
10. `STOP_RECORDING`
11. any reviewed arrival actions
12. `RETURN_TO_LAUNCH`, if the mission explicitly requests it

The order is important. In particular, required perception is activated and
acknowledged before arming during mission start. A failed required activation
therefore prevents take-off instead of allowing a mission to fly without the
expected detection service.

`SET_CAMERA_MODE` records semantic plan intent. The physical payload controller
currently applies supported gimbal and zoom commands, while camera recording is
also translated into MAVSDK mission actions where the connected aircraft
supports them. Do not assume that every semantic camera mode has a one-to-one
hardware command on every payload.

## Direct waypoints

`DIRECT_WAYPOINTS` preserves the operator's point order. Each point may override
the mission's default altitude and speed and may supply heading, hold, camera,
zoom, or gimbal behavior.

### Heading selection

Atlas resolves yaw in this order:

1. An explicit waypoint heading wins.
2. A `LOOK_AT_POINT` camera target makes the aircraft face the initial bearing
   from the waypoint to that target.
3. Otherwise Atlas faces the next waypoint, unless the camera behavior is
   `FIXED_ANGLE`.

The distinction matters: aircraft yaw and gimbal aim are related but separate
controls. A look-at target can inform aircraft yaw, while the payload plan can
also aim the gimbal at the target.

### Holds

A waypoint hold is translated into the appropriate loiter/fly-through behavior
for the MAVSDK mission item. A zero hold means the aircraft may proceed through
the point; a positive hold asks it to remain there for the reviewed duration.

## Lawn-mower area scan

`LAWN_MOWER` converts a polygon into alternating parallel scan lanes.

### Geometry algorithm

1. Require at least three polygon vertices.
2. Build a local tangent plane whose origin is the first polygon point. This
   lets the planner work in metres instead of angular latitude/longitude units.
3. Rotate the polygon by the negative sweep heading so desired scan lanes are
   horizontal in the working coordinate system.
4. Move a horizontal scan line from the polygon's minimum to maximum `y`
   coordinate at the chosen lane spacing.
5. Intersect each scan line with polygon edges and pair the sorted intersections
   into inside-polygon lane segments.
6. Reverse every other segment so adjacent lanes form a continuous back-and-
   forth path.
7. Rotate the generated points back and convert them to latitude/longitude.

Alternating lane direction reduces unnecessary transit between lanes. The
polygon clipping step means concave polygons can produce more than one inside
segment on a scan line.

### Lane spacing

An explicit spacing must be between 1 and 500 metres. When it is omitted, Atlas
derives it from altitude and requested overlap:

```text
lane spacing = max(altitude × (1 - overlap / 100), 5 metres)
```

Overlap must be at least 0% and less than 90%. This is a simple planning model,
not a camera-calibration model: it does not calculate true ground sampling
distance from sensor size, focal length, or lens distortion. Operators must set
explicit spacing when survey-grade coverage is required.

### Geographic limitation

The local tangent-plane approximation is intended for bounded operational
areas. Very large polygons and geometry crossing the antimeridian require a
geodesic planner and are outside the current model. Atlas warns rather than
presenting the approximation as globally exact.

## Route-follow scan

`ROUTE_FOLLOW` flies along an ordered centerline. It requires at least two route
points.

For every route segment, Atlas computes its haversine distance. When sample
spacing is configured, it divides the segment into:

```text
division count = ceil(segment distance / sample spacing)
```

It then linearly interpolates latitude and longitude at those divisions. Sample
spacing must be between 1 and 10,000 metres. Without sample spacing, the original
route endpoints are used.

`corridorWidthMeters` describes the corridor used by planning and terrain
assessment. It does **not** make the aircraft weave across the corridor or
create parallel offset routes. The flight path remains the supplied centerline.

## Altitude modes and terrain profiling

Atlas supports two altitude interpretations:

- `HOME_RELATIVE`: waypoint altitude is relative to the reviewed home position.
- `TERRAIN_CLEARANCE`: Atlas precomputes a home-relative altitude profile that
  aims to preserve a requested clearance over sampled terrain.

Terrain clearance is preflight planning, not live terrain following. Atlas does
not continuously query a sensor or DEM and reshape the route in flight.

### Terrain-clearance calculation

The UI samples the digital elevation model (DEM) at home and at interpolated
route stations. For corridor missions it also samples relevant center and edge
locations. Native validates the sample geometry, source provenance, station
count, and maximum distance between a required sample and its station before it
accepts the profile.

For each station the initial home-relative altitude is:

```text
max(
  terrain elevation - home elevation
  + requested clearance
  + safety margin,
  2 metres
)
```

Atlas then applies two rate-limit passes:

- a backward pass raises earlier points where necessary to make the upcoming
  climb achievable within the configured climb-rate limit;
- a forward pass raises later points where necessary to make descent gradual
  enough for the configured descent-rate limit.

The planner raises the route; it does not silently violate clearance to satisfy
a rate limit. Generation fails when the resulting profile would exceed the
configured ceiling.

At upload, the current home position must remain within 30 metres of the home
position used to build the profile. A moved home datum invalidates the meaning
of every relative altitude, so the mission must be regenerated.

## Upload and execution lifecycle

### Upload checks

Native rejects an upload when, among other checks:

- the generated plan is not ready;
- the aircraft is not connected;
- the aircraft already has an unfinished mission run;
- the first waypoint is more than 5 km from the reported home position, falling
  back to current position when home is unavailable;
- terrain-profile provenance or geometry no longer matches the reviewed plan.

Upload creates a run and transfers the immutable plan. It does not start flight.

### Start checks

Starting a run requires fresh live telemetry and appropriate PX4 health. Global
position and home must be valid, and the aircraft must be armed or armable. A
reported battery below 15% blocks start. If a battery value is unavailable,
other mission and incident paths may apply stricter policy; callers should not
interpret missing telemetry as proof of safety.

On the Agent, required perception activation is acknowledged first. The Agent
then arms the aircraft and starts the MAVSDK mission. If start fails after
arming, it commands Hold and releases the perception claim.

### Run states and controls

Mission runs use these durable states:

| State | Meaning |
| --- | --- |
| `UPLOADING` | Native is transferring the plan to the Agent/aircraft. |
| `READY` | Upload succeeded; the run can be started. |
| `RUNNING` | The aircraft is executing the plan. |
| `PAUSED` | Execution is held, including reviewed staging behavior. |
| `COMPLETED` | The reviewed plan and required terminal actions succeeded. |
| `FAILED` | Execution or a required action failed. |
| `CANCELLED` | The operator cancelled the run. |
| `RTL` | The run was moved into return-to-launch handling. |

Pause commands PX4 Hold. Resume continues mission execution. Cancel clears the
mission and leaves the vehicle holding; it is not an implicit RTL. RTL is a
separate, explicit control.

## Payload behavior during a mission

The payload controller keeps the current waypoint's reviewed gimbal orientation
and zoom as its automatic intent. A manual operator command temporarily acquires
the payload lease. When that override ends, automatic intent is restored from
the current waypoint rather than from an old global default.

This lease boundary prevents the mission loop and an operator from issuing
competing payload commands at the same time. It also explains why camera follow,
manual gimbal control, and mission payload intent must report who currently owns
control.

## Safety and correctness invariants

- Definitions are editable; generated plans and active runs are historical
  records and are not edited in place.
- A plan is bound to one aircraft for a run.
- Only one unfinished mission run may control an aircraft.
- Mission pause/cancel and explicit RTL are distinct commands.
- Terrain profiles are tied to their sampled source and home datum.
- Semantic camera intent is not a promise that every payload exposes an
  identical hardware mode.
- Flight-pattern geometry is deterministic for the reviewed inputs.
- Generic planning does not perform obstacle avoidance.

## Where to make changes

| Concern | Primary implementation |
| --- | --- |
| Mission definitions, validation, and flight-pattern generation | [`atlas/src-tauri/src/database/missions.rs`](../atlas/src-tauri/src/database/missions.rs) |
| Terrain-profile validation and storage | [`atlas/src-tauri/src/database/missions.rs`](../atlas/src-tauri/src/database/missions.rs) and the mission-planning UI |
| Upload/start/pause/resume/cancel/RTL commands | [`atlas/src-tauri/src/commands.rs`](../atlas/src-tauri/src/commands.rs) and [`mission_runs.rs`](../atlas/src-tauri/src/database/mission_runs.rs) |
| Durable arrival actions | [`atlas/src-tauri/src/database/mission_actions.rs`](../atlas/src-tauri/src/database/mission_actions.rs) |
| Agent plan translation and execution | [`atlas-agent/internal/vehicle/missions.go`](../atlas-agent/internal/vehicle/missions.go) |
| Protocol messages and enums | [`proto/atlas/ground_station.proto`](../proto/atlas/ground_station.proto) |
| Mission UI | [`atlas/src/missions/`](../atlas/src/missions/) |

Before changing a flight pattern, update its validation and deterministic tests
alongside the generator. Before changing a run transition, check both Native's
persisted state machine and the Agent acknowledgement path; neither side alone
defines the complete behavior.
