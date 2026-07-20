# Incident dispatch

Incident dispatch turns a reported location into a reviewed, auditable response
mission for one suitable aircraft. It reuses Atlas's normal mission planner and
mission-run state machine, then adds incident lifecycle, aircraft suitability,
known-building assessment, assignment reservations, and durable arrival actions.

This guide describes the current implementation. It is not a roadmap and does
not imply autonomous dispatch: an operator reviews the incident, response
pattern, aircraft, route assessment, and failure policy before preparation and
again controls upload and start.

## System boundary

The current incident control path is local:

```text
operator
  |
  v
Native Operations workspace
  |-- SQLite: incident, event, preview, assignment, plan, run, action state
  |-- suitability + geometry + policy validation
  |
  v
Agent gRPC command stream
  |
  v
MAVSDK / PX4 + payload controller
```

The cloud backend is not in this command path. Incident records currently enter
through manual creation in Atlas Native with source `ATLAS_NATIVE`. The stored
source fields are intentionally source-neutral so a future trusted integration
can be added without changing the incident model, but no external emergency-
service feed is implied today.

## Core records and why they are separate

| Record | Purpose |
| --- | --- |
| Incident | The reported event, location, severity, notes, and lifecycle. |
| Incident event | Append-only audit history of meaningful incident and dispatch changes. |
| Preview | A deterministic proposed response for a specific incident revision and aircraft context. |
| Mission definition and plan | The response geometry converted into Atlas's normal editable intent and immutable executable plan. |
| Assignment | The reservation linking one incident, aircraft, plan, and response state. |
| Mission run | Upload and flight execution state. |
| Mission action execution | Durable acknowledgement/retry/policy state for arrival actions. |

Keeping these records separate prevents an important class of ambiguity. An
incident can change after a route was reviewed, an assignment can be prepared
without being uploaded, and a mission can remain historically valid even when
the incident is later resolved. Atlas records each fact instead of overwriting
one mutable "dispatch status" value.

## Incident lifecycle and revisions

Incidents use four states:

| State | Meaning |
| --- | --- |
| `OPEN` | Reported and available for response planning. |
| `ACTIVE` | Response work is underway. |
| `RESOLVED` | The incident is closed as resolved. |
| `CANCELLED` | The incident is closed without resolution. |

Each incident has a `revision` and a `locationRevision`. Normal edits increment
`revision`; coordinate changes also increment `locationRevision`. Updates use
optimistic concurrency, meaning the writer must name the revision it reviewed.
If another writer changed the incident first, Atlas rejects the stale update
instead of silently overwriting it.

Any incident update makes a prepared-but-not-uploaded assignment `STALE`.
Location-specific artifacts therefore cannot survive a change simply because
the change looked small. A run already in the air keeps its immutable plan and
raises operator-visible alerts; Atlas does not mutate an airborne route.

An incident cannot be resolved or cancelled while its response has an unfinished
mission run. The operator must first bring that aircraft/run into an explicit
terminal state.

## Dispatch workflow

### 1. Create or select an incident

The operator records a valid target coordinate, severity, description, and
other available context. Dispatch is supported only while the incident is
`OPEN` or `ACTIVE`.

### 2. Select a response pattern

The operator chooses one of the four patterns below and supplies its reviewed
parameters. Atlas rejects unsupported combinations rather than converting them
silently.

### 3. Evaluate aircraft suitability

Native evaluates the fleet against current reservations, capabilities, link
state, telemetry, PX4 health, home/global position, battery, and estimated
travel time. Suitability is both guidance for the operator and a preparation
gate; it is not the last safety check. Upload, start, and Agent execution each
revalidate the conditions relevant to their boundary.

### 4. Preview the response

Preview deterministically turns the incident, response parameters, and selected
aircraft context into a normal Atlas mission definition and generated plan. It
also performs the known-building assessment described below.

Preview sends no aircraft command and reserves no aircraft.

### 5. Review and prepare

The operator reviews geometry, altitude, speed, payload behavior, required
capabilities, arrival actions, and action-failure policy. A non-clear known-
building assessment requires an explicit override reason.

Preparation is one database transaction. It inserts the ready mission,
immutable plan, assignment in `PREPARED`, and incident audit event together.
If any part fails, none of the reservation is committed. Preparation still
sends no aircraft command.

### 6. Upload and start

Upload rechecks the assignment, incident revision, aircraft binding, departure
context, and normal mission constraints, then creates the mission run. Start
uses the standard PX4 health/arming/perception gates. Progress and arrival-action
acknowledgements update both the run and assignment.

### 7. Finish deliberately

The response pattern decides whether execution pauses for operator control or
continues to completion. Return-to-launch is never hidden in the four current
incident patterns. The operator chooses RTL, Land, Resume, or Cancel when that
is the reviewed next action.

## Response patterns

### Hold at staging

`HOLD_AT_STAGING` creates a one-point `WAYPOINT` / `DIRECT_WAYPOINTS` mission.
The payload uses a forward-oblique view with a -35° gimbal pitch and follows
aircraft heading.

After waypoint 0, the arrival chain commands Hold. A successful durable Hold
acknowledgement changes the mission run to `PAUSED` and the assignment to
`STAGED`. It does **not** mark the aircraft `ON_SCENE` and does not resume
automatically. The operator must explicitly choose Resume, RTL, Land, or Cancel.
Landing by itself does not close the mission run.

Use this pattern when the aircraft should wait at a reviewed staging location
before entering the incident area.

### Offset observe

`OFFSET_OBSERVE` creates a one-point `WAYPOINT` / `DIRECT_WAYPOINTS` mission at
a reviewed offset observation position. It uses `LOOK_AT_POINT` behavior and
aims the gimbal at the incident target. The pattern requires a reviewed absolute
target elevation and the relevant point-gimbal capabilities.

At the final waypoint the action chain commands Hold and then attempts the
optional gimbal region-of-interest action. A successful Hold acknowledgement
marks the assignment `ON_SCENE`. The chain has no Resume because it occurs at
the final point. When required actions and policy handling are complete, the
run completes while the aircraft remains in the reviewed hold behavior.

Use this pattern to observe a location from a safer stand-off position rather
than flying directly above it.

### Bounded area scan

`BOUNDED_AREA_SCAN` creates an `AREA_SCAN` / `LAWN_MOWER` mission inside an
operator-reviewed polygon. It uses the same lane-generation algorithm documented
in [Mission types and flight patterns](mission-types-and-flight-patterns.md) and
a downward payload view.

After generated waypoint 0, the arrival chain commands Hold and then Resume.
The acknowledged Hold marks the assignment `ON_SCENE`; Resume allows the
remaining lanes to run. Triggering after the first point, rather than the final
point, makes "on scene" mean that the aircraft reached and acknowledged the
survey area before it performed most of the scan.

Use this pattern for systematic coverage of a bounded incident area.

### Bounded orbit

`BOUNDED_ORBIT` generates a geodesic circle around the incident target as a
`WAYPOINT` / `DIRECT_WAYPOINTS` mission. The current implementation supports
exactly one altitude level. A multi-level orbit must not be presented in UI or
integration documentation as available.

Current bounds are:

- radius: 5–500 metres;
- laps: 1–10;
- direction: clockwise or counter-clockwise;
- altitude: 2–120 metres;
- speed: 0.5–15 metres per second;
- 24 points per lap, plus a closure point.

The vertical-rate field is validated between 0.2 and 5 metres per second for
schema consistency, but no inter-level transition is generated while the orbit
is single-level.

After the first waypoint, the action chain commands Hold, attempts gimbal ROI at
the incident, and commands Resume. The Hold acknowledgement marks the assignment
`ON_SCENE`; the aircraft then flies the remainder of the orbit while the camera
uses `LOOK_AT_POINT` intent.

Use this pattern for repeated observation around a target when the entire
reviewed circle is suitable for flight.

## Arrival actions and failure policy

Arrival actions are durable state machines, not fire-and-forget UI commands.
They use these execution states:

```text
REQUESTED -> RUNNING -> SUCCEEDED
                |
                +-> RETRYING -> RUNNING
                |
                +-> FAILED -> POLICY_APPLIED
```

The current retry policy permits up to three attempts, with a 20-second action
timeout, a 2-second initial delay, and a multiplier of 2. Restart reconciliation
uses the stored action and acknowledgement state so Native does not merely
assume an interrupted request succeeded.

Action-chain invariants are:

- Hold is first.
- Optional point-gimbal action, when present, follows Hold.
- Resume is last and is allowed only for a non-final trigger.
- Every action in a chain uses the same waypoint trigger.
- A required-action failure uses the operator-reviewed policy:
  `RETURN_TO_LAUNCH` or `OPERATOR_INTERVENTION`.
- Point-gimbal failure uses `SKIP_OPTIONAL_AND_NOTIFY`; losing an optional
  camera angle must not be disguised as successful execution, but it need not
  force the same response as losing Hold.

All four current response plans set automatic `returnToLaunch` to false. RTL can
still be selected as the required-action failure policy or issued explicitly by
the operator.

## Aircraft suitability

Suitability filters out an aircraft when any blocker is present, including:

- the aircraft lifecycle is not active;
- it already has an active incident assignment or unfinished mission run;
- the link is not freshly connected (15-second link threshold);
- telemetry is missing or older than 5 seconds;
- current position is invalid;
- PX4 health is unavailable;
- the aircraft is unarmed and not armable;
- global position or home is invalid;
- battery is missing or below 15%;
- the aircraft lacks a required commissioned capability.

A battery between 15% and 25% is surfaced as a consideration even when it is not
an absolute blocker.

Base capability requirements include mission upload/start and the Hold action.
Area scan and orbit also require Resume. Offset observe and orbit require point-
gimbal support plus the detected gimbal/ROI capabilities.

Atlas ranks available aircraft ahead of unavailable ones, then considers blocker
count, estimated travel time, battery, and finally stable name/ID ordering. The
first available aircraft is presented as recommended. Estimated arrival time is
straight-line distance divided by reviewed response speed; it is a ranking aid,
not a forecast that accounts for wind, airspace, route constraints, or take-off
time.

## Known-building assessment

Atlas can load optional known-building GeoJSON from
`ATLAS_KNOWN_BUILDINGS_GEOJSON`. Source provenance is required. The preview
checks the route from the aircraft's current position through every generated
waypoint against buffered known-building footprints and their vertical volumes,
using the reviewed home absolute-altitude datum.

The result is one of:

| Result | Meaning |
| --- | --- |
| `CLEAR_OF_CHECKED_VOLUMES` | No intersection was found in the supplied dataset and modeled volume. |
| `INTERSECTIONS` | The modeled route intersects at least one checked volume. |
| `INCOMPLETE` | Some required geometry or altitude context could not be assessed completely. |
| `DATA_UNAVAILABLE` | No usable known-building source was available. |

Only the first result is clear without override. Every other result requires an
explicit reason before preparation.

This is **not obstacle avoidance** and not a declaration that a route is safe.
It sees only the loaded dataset and modeled volumes. It does not account for
wires, cranes, vegetation, temporary structures, unmapped buildings, people,
airspace restrictions, or vehicle dynamics.

## Departure-context freshness

Known-building assessment begins at the reviewed aircraft departure context.
At upload Atlas requires that context still be current:

- telemetry age no more than 5 seconds;
- horizontal movement no more than 30 metres;
- relative-altitude change no more than 5 metres;
- home absolute-datum change no more than 5 metres.

If any threshold is exceeded, the operator must preview and prepare again. This
prevents a route assessed from one take-off context being reused after the
aircraft or home reference has materially moved.

## Assignment states and reservations

Preparation reserves the aircraft so two incidents cannot concurrently claim
the same vehicle. Atlas also blocks preparation when an unfinished normal
mission run exists.

Important assignment states include `PREPARED`, `UPLOADING`, `STAGED`,
`ON_SCENE`, terminal outcomes, and `STALE`. `STAGED` and `ON_SCENE` are based on
durable Hold acknowledgement, not merely on GPS proximity or the UI observing a
waypoint index.

A `PREPARED` assignment may be abandoned before upload. Abandoning releases the
reservation but retains the mission, plan, and audit history. Historical data is
kept because it explains what an operator reviewed and why it was not flown.

## Failure modes operators and developers should expect

- **Incident changed after review:** the prepared assignment becomes stale;
  re-preview and prepare.
- **Aircraft moved before upload:** departure-context validation fails;
  re-preview from the new context.
- **Capability disappeared or link became stale:** suitability/upload/start
  blocks; do not bypass the later gate because an earlier preview passed.
- **Required arrival action failed:** the reviewed RTL or operator-intervention
  policy is applied and recorded.
- **Optional gimbal action failed:** Atlas notifies and continues according to
  `SKIP_OPTIONAL_AND_NOTIFY`.
- **Native restarted during an action:** stored request/acknowledgement state is
  reconciled; do not manufacture success from elapsed time.
- **Incident edited while aircraft is airborne:** the plan remains immutable;
  Atlas alerts the operator instead of changing the route underneath PX4.

## Where to make changes

| Concern | Primary implementation |
| --- | --- |
| Operations workflow and review UI | [`atlas/src/operations/OperationsPage.tsx`](../atlas/src/operations/OperationsPage.tsx) |
| Map and geometry presentation | [`atlas/src/operations/OperationsMap.tsx`](../atlas/src/operations/OperationsMap.tsx) |
| Incident, preview, suitability, assignment, and audit persistence | [`atlas/src-tauri/src/database/incidents.rs`](../atlas/src-tauri/src/database/incidents.rs) |
| Generic pattern generation | [`atlas/src-tauri/src/database/missions.rs`](../atlas/src-tauri/src/database/missions.rs) |
| Durable arrival-action state machine | [`atlas/src-tauri/src/database/mission_actions.rs`](../atlas/src-tauri/src/database/mission_actions.rs) |
| Mission-run state and transitions | [`atlas/src-tauri/src/database/mission_runs.rs`](../atlas/src-tauri/src/database/mission_runs.rs) |
| Native command boundary | [`atlas/src-tauri/src/commands.rs`](../atlas/src-tauri/src/commands.rs) |
| Agent mission/action execution | [`atlas-agent/internal/vehicle/missions.go`](../atlas-agent/internal/vehicle/missions.go) |
| Native–Agent contract | [`proto/atlas/ground_station.proto`](../proto/atlas/ground_station.proto) |

When adding an incident source, preserve revision and audit semantics. When
adding a response pattern, implement the same concept across parameter
validation, deterministic generation, suitability capabilities, preview,
arrival actions, UI review, persistence, Agent translation, and tests. A new UI
option without those boundaries is not a complete response mode.
