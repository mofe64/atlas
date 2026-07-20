# Inference, tracking, geolocation, and follow modes

Atlas's perception stack detects objects in clean aircraft video, associates
detections over time, optionally estimates a selected target's ground position,
and exposes two very different follow controllers. This guide explains the data
flow and the safety boundaries between those stages.

The most important distinction is:

- **camera follow** moves the gimbal to keep a selected image-space track near
  the frame centre;
- **Follow from standoff** moves the aircraft using a filtered geographic target
  estimate.

Neither mode silently enables the other. They have separate control authority,
prerequisites, watchdogs, and failure behavior.

## End-to-end data flow

```text
aircraft RTSP stream
   |                         clean video path
   |-------------------------------> Atlas Native decoder/display/recording
   |
   +-> onboard inference adapter
          |  detections + frame metadata over local Unix socket
          v
       Atlas Agent
          |-- tracker assigns Atlas-owned track IDs
          |-- optional camera-motion compensation
          |-- track lifecycle and counting
          |-- exact-selection camera-follow controller
          |-- selected-track geolocation using pose/gimbal history
          |
          +------------ gRPC metadata/events ------------> Atlas Native
                                                            |-- live overlays
                                                            |-- durable summaries
                                                            |-- DEM refinement
                                                            |-- motion filtering
                                                            |-- evidence capture
                                                            +-- aircraft-follow session
```

Video and inference metadata intentionally take separate paths. The Hailo
pipeline does not draw boxes into or re-encode the stream. Native decodes the
clean RTSP stream independently, so live overlays, recordings, and exported
evidence are not coupled to a vendor-specific annotated video feed.

## Inference activation and ownership

The onboard adapter starts `READY` but `INACTIVE`. Work begins only when an
activation claim exists. Claims have an owner and a lease, and the adapter runs
the union of the classes requested by active claims.

Two common owners are:

- a live-view lease, normally renewed every 5 seconds with a 12-second lifetime;
- a mission's durable `START_PERCEPTION` action, which must be acknowledged
  before the mission arms when perception is required.

Native accepts live-view lease durations from 3 to 30 seconds. If the UI stops
renewing a lease, the claim expires instead of leaving expensive inference
running forever. A view that requests no class filter receives all available
classes; filtered claims are combined so one consumer cannot accidentally hide
classes needed by another.

The adapter uses a configured Hailo HEF and TAPPAS YOLO postprocessor. Atlas does
not hard-code a particular YOLO model/version into its public contract. The
normalized detection/frame messages are provider-independent; another adapter
can be introduced if it preserves validation, timing, source identity, and
lifecycle semantics.

## Detection frames and validation

Each frame carries source/session identity, capture timing, dimensions, model
metadata, and normalized detections. Atlas validates normalized bounding boxes,
confidence, class identity, timestamps, and frame/source continuity before the
data enters tracking and control.

Normalized coordinates make the transport independent of display resolution,
but control still depends on correct source dimensions and timing. A box from a
different stream epoch or an old frame must never be treated as the current
location of a selected target.

## Tracking algorithms

Set `ATLAS_TRACKER_ALGORITHM` to choose the current tracker mode.

### `disabled`

Detections continue to flow, but Atlas does not assign an Atlas `track_id`. Use
this for detector diagnostics where temporal identity is deliberately excluded.
Features that require an exact selected track cannot operate.

### `byte_track` (default production mode)

Atlas uses the pinned FoundationVision ByteTrack C++ implementation. The main
ideas are:

- a Kalman filter predicts where an existing track should appear next;
- matching is isolated by object class, so a vehicle detection cannot be joined
  to a person track;
- high-confidence detections are associated first;
- lower-confidence detections get a second association pass, which can recover
  objects that were partly obscured or briefly detected with lower confidence;
- LAPJV solves the assignment between predicted tracks and detections;
- unmatched tracks are retained for bounded recovery before closure.

Appearance re-identification (ReID) is disabled. Atlas therefore does not claim
that a person or vehicle leaving the scene and returning later has retained the
same identity based on appearance.

### `byte_track_cmc`

This mode uses the same tracker plus camera-motion compensation (CMC). Sparse
optical flow estimates apparent camera motion from the exact pre-inference RGB
frame, allowing the association step to compensate for a moving camera.

CMC is confidence-gated. If the image estimate is missing or unreliable, Atlas
uses the identity transform, reports degraded quality, and continues tracking.
It does not apply an untrusted transform merely to keep the mode nominally
active.

## Track identity and lifecycle

The Agent owns operator-visible IDs in the form:

```text
atlas:<session>:<counter>
```

Any upstream tracker ID is retained only as provenance. It cannot become the
operator identity because an inference runtime can restart, reuse counters, or
change algorithms.

Default lifecycle timing is:

| Transition or behavior | Default |
| --- | --- |
| `TENTATIVE` to `ACTIVE` | 2 observations |
| Prediction horizon | up to 750 ms |
| `LOST` | after 1 second without a matched observation |
| `CLOSED` | after 3 seconds without recovery |
| Track summary publication | every 1 second |
| Retained in-memory history | up to 60 samples per track |

`TEMPORARILY_OCCLUDED` represents a bounded interval in which the track is still
eligible for prediction but lacks a current matched observation. Predicted
positions are normalized image-space estimates with decaying confidence.

Atlas resets the tracking session when continuity is no longer trustworthy,
including activation/deactivation, inference runtime reconnect, source or stream
epoch change, model or image-dimension change, timestamp regression, a gap over
2 seconds, or tracker failure. A reset creates new IDs; selection and control do
not jump old intent onto a new association.

## Selection is exact, not best-effort

A selection names both the perception session and Atlas track ID. Atlas may
follow the same track through its bounded occlusion lifecycle, but it never
transfers selection to the nearest box or revives it across a new tracking
session.

This invariant is central to safe control. "Probably the same object" is useful
for analytics but is not sufficient authority to move a gimbal or aircraft.

## Counting

Line and polygon counts are session-scoped. Atlas counts only the centre of a
confirmed, observed bounding box crossing the configured geometry. Predicted
samples never increment counts. A continuity gap resets the relevant geometry
state so reconnects or seeks cannot manufacture crossings.

Native persists durable session summaries, lifecycle events, important samples,
counts, and selections. It does not store every box from every frame as an
unbounded historical dataset. The live frame history is deliberately bounded
(currently 240 frames / roughly 10 seconds) for UI correlation and evidence
workflows.

## Selected-track geolocation

Geolocation converts one selected image-space observation into a ground
coordinate. It is intentionally strict because a plausible-looking coordinate
can be more dangerous than no coordinate.

The Agent accepts only the exact selected `ACTIVE` track. The current model uses
a centred-boresight approximation: the bounding-box centre must be within 0.04
normalized units of frame centre on each axis. This limitation keeps the ray
model consistent with the commissioned physical boresight reference; it is not
a general camera intrinsic/extrinsic projection for arbitrary pixels.

### Inputs

The calculation aligns:

- retained frame capture timing;
- aircraft position and attitude history;
- measured gimbal orientation history;
- the commissioned boresight relationship;
- an initial ground plane or target-centre height.

It casts a downward ray and rejects geometry outside its validity envelope,
including more than 3 km range, less than 20° depression, or more than 500 ms
timing uncertainty. The result includes error/uncertainty information; callers
must not treat the latitude/longitude alone as exact truth.

### Terrain refinement

Native samples the DEM at the initial coordinate and sends the updated target
height back through the geolocation calculation. It repeats target-area samples
for up to five refinement iterations after the initial estimate and stops when
coordinate movement is at most 0.75 metres. The ray is recomputed each time;
Native does not merely replace the altitude field on an old coordinate.

This iterative loop matters on sloped terrain: changing assumed ground height
changes where the ray intersects the ground, which can in turn change the DEM
height at the new coordinate.

## Motion filtering

Follow from standoff requires motion, not just a sequence of noisy points. Native
therefore filters consecutive finalized coordinates for the same selected track.

The filter:

1. requires elapsed time between 0.2 and 30 seconds, otherwise initializes or
   resets history;
2. predicts the next position from the prior filtered velocity;
3. compares the observed coordinate with that prediction;
4. rejects residuals implying more than 150 m/s as outliers;
5. chooses a position gain from 0.2–0.8 based on uncertainty;
6. chooses a velocity gain from 0.1–0.6 based on elapsed time and uncertainty.

Results report one of these states:

| Status | Meaning |
| --- | --- |
| `INSUFFICIENT_HISTORY` | A position exists, but there is not enough valid history for motion. |
| `TIME_GAP_RESET` | The timing interval broke filter continuity. |
| `OUTLIER_REJECTED` | The new observation was inconsistent with the motion envelope. |
| `BELOW_UNCERTAINTY` | Apparent displacement was not meaningful relative to uncertainty. |
| `FILTERED` | A usable filtered position and velocity estimate is available. |

Only `FILTERED` motion is eligible to start aircraft follow. This prevents a
single geolocation point or jitter below measurement uncertainty from being
interpreted as target velocity.

## Camera follow: gimbal control in image space

Camera follow keeps the selected object near the image centre by commanding
gimbal angular rates. It does not geolocate the target and does not move the
aircraft.

### Prerequisites and authority

Camera follow requires:

- a manual payload-control lease;
- an exact selected `ACTIVE` track in the current source/session;
- measured gimbal orientation;
- an available gimbal implementation.

The payload lease prevents mission automatic intent and manual/follow commands
from fighting for the gimbal. When follow stops and the lease is released, the
payload controller can restore the current mission waypoint's automatic gimbal
and zoom intent.

### Controller logic

At the default 10 Hz update rate, Atlas measures bounding-box-centre error from
frame centre and applies proportional pitch/yaw control:

- normalized deadband: 0.025;
- pitch gain: 60;
- yaw gain: 80;
- pitch-rate cap: 20°/s;
- yaw-rate cap: 30°/s;
- pitch acceleration cap: 60°/s²;
- yaw acceleration cap: 90°/s²;
- physical pitch range: -90° to +30°;
- physical yaw range: -180° to +180°;
- soft angle margin: 2°.

Small errors inside the deadband produce no motion. Rate and acceleration limits
avoid abrupt commands, and the angle margin slows/stops motion before a physical
limit.

### Loss behavior

When a frame is older than 350 ms or the track becomes
`TEMPORARILY_OCCLUDED`, Atlas sends zero rate and holds the current angle. If
that condition lasts 2 seconds, follow stops. A tentative/lost/closed track,
source/session change, payload lease loss, or gimbal read/write fault stops
immediately. The controller never selects a replacement track.

## Follow from standoff: aircraft control in geographic space

Follow from standoff commands PX4 Offboard velocity so the aircraft follows a
moving target from a reviewed horizontal distance and altitude. It is a separate
vehicle-control authority and does not use bounding-box error.

The feature is unverified and disabled by default with
`ATLAS_AIRCRAFT_FOLLOW_ENABLED=false`. Enabling the software flag is not enough
to claim readiness: the aircraft must advertise a commissioned follow validation
reference and physical boresight reference, and the operator must review the
envelope for that airframe/site.

### Session lifecycle

Native persists:

```text
REQUESTED -> VALIDATING -> ACQUIRING -> FOLLOWING
                                      |
                                      +-> DEGRADED_HOLD

explicit operator end -> ENDED
```

The UI normally grants a 4-second operator lease and renews it every second;
Native accepts 1–5 seconds and runs a 250 ms watchdog. The Agent has an
independent watchdog. Losing the desktop or the network therefore does not rely
on the UI being alive to stop Offboard control.

### Start prerequisites

Native requires, among other checks:

- no unfinished mission run on the aircraft;
- the exact current selection;
- an `ACTIVE` track;
- a successfully converged geolocation;
- motion-filter status `FILTERED`;
- target age no more than 5 seconds at start;
- commissioned follow and boresight capabilities;
- armed, airborne aircraft with healthy local/global state;
- fresh telemetry, sufficient battery, and reviewed altitude/boundary envelope.

The Agent applies tighter runtime freshness where appropriate (including
telemetry no older than 1.5 seconds and target age no more than 2.5 seconds).

### Reviewed envelope

The current accepted ranges are:

| Parameter | Range |
| --- | --- |
| Standoff distance | 10–500 m |
| Altitude/floor/ceiling inputs | 5–120 m |
| Maximum horizontal speed | 0.5–15 m/s |
| Maximum horizontal acceleration | 0.1–5 m/s² |
| Session duration | 10–1,800 s |
| Boundary radius | 25–5,000 m |
| Battery reserve | 15–100% |
| Minimum track confidence | 0.5–1.0 |
| Maximum geolocation uncertainty | 1–100 m |
| Maximum velocity uncertainty | 0.1–25 m/s |
| Operator review note | 8–500 characters |

These schema bounds make bad configuration harder; they do not choose a safe
value for a particular aircraft or operation.

### Controller logic

The Agent controller runs at 10 Hz and fixes one observation side when the
session starts:

1. Prefer the radial direction from target to aircraft.
2. If that is unavailable, use the direction opposite target velocity.
3. If neither is usable, fall back opposite the aircraft's current heading.

Keeping the radial side fixed avoids the commanded point flipping across the
target as estimates change.

For each step the controller predicts the target through a bounded age, then
computes:

```text
desired aircraft position = predicted target + fixed radial × standoff

commanded horizontal velocity =
  target velocity feed-forward
  + 0.25 × position error
```

It clamps speed and acceleration to the reviewed envelope. Altitude uses a
proportional down-axis correction capped at ±1 m/s, and yaw faces the target.

### Runtime gates and failure behavior

Every control step rechecks the operator lease, target freshness, maximum
duration, aircraft telemetry, armed/in-air state, health, battery reserve,
altitude band, aircraft/target/desired-point boundary, and Offboard state.

On any failure—or if the ground command stream disappears—the Agent commands
zero velocity, stops Offboard, commands PX4 Hold, and records a durable reason.
It never automatically chooses RTL or Land. Those actions require explicit
operator intent or a separately reviewed policy.

## Comparing the two follow modes

| Property | Camera follow | Follow from standoff |
| --- | --- | --- |
| Moves | Gimbal | Aircraft |
| Control space | Normalized image error | Geographic target position/velocity |
| Needs geolocation | No | Yes, converged and fresh |
| Needs motion filter | No | Yes, `FILTERED` |
| Main authority | Payload lease | PX4 Offboard + operator lease |
| Occlusion response | Zero gimbal rate, then stop | Target freshness/controller gates lead to Hold |
| Failure terminal behavior | Stop gimbal follow | Stop Offboard and PX4 Hold |
| Automatic replacement target | Never | Never |
| Automatic RTL/Land | No | No |

## Evidence and retention boundary

Atlas Native can preserve a verified still or bounded event clip around an
important perception moment and records provenance linking the media to the
aircraft, source, session, track, and timing context. Local segmented recording
and evidence retention are separate from the short live overlay buffer.

This does not mean every detection frame is archived indefinitely. The bounded
ephemeral history supports correlation; explicit evidence workflows create
durable media and metadata. Export and remote replication remain distinct
product concerns.

## Where to make changes

| Concern | Primary implementation |
| --- | --- |
| Hailo inference adapter and Unix-socket protocol | [`atlas-agent/internal/perception/`](../atlas-agent/internal/perception/) |
| Tracker selection, identity, lifecycle, counting | [`atlas-agent/internal/perception/`](../atlas-agent/internal/perception/) |
| Camera/gimbal follow controller | [`atlas-agent/internal/vehicle/gimbal_follow.go`](../atlas-agent/internal/vehicle/gimbal_follow.go) |
| Selected-track geolocation | [`atlas-agent/internal/vehicle/track_geolocation.go`](../atlas-agent/internal/vehicle/track_geolocation.go) and [`proto/atlas/ground_station.proto`](../proto/atlas/ground_station.proto) |
| Native frame/session persistence | [`perception_tracks.rs`](../atlas/src-tauri/src/database/perception_tracks.rs), [`perception_operations.rs`](../atlas/src-tauri/src/database/perception_operations.rs), and related stores |
| DEM refinement and motion filter | [`atlas/src/follow/trackGeolocation.ts`](../atlas/src/follow/trackGeolocation.ts) and Native perception commands/storage |
| Aircraft-follow Native state/watchdog | [`atlas/src-tauri/src/database/aircraft_follow.rs`](../atlas/src-tauri/src/database/aircraft_follow.rs) and Native command handling |
| Aircraft-follow Agent controller | [`atlas-agent/internal/vehicle/aircraft_follow.go`](../atlas-agent/internal/vehicle/aircraft_follow.go) |
| Operator follow UI | [`atlas/src/follow/FollowPage.tsx`](../atlas/src/follow/FollowPage.tsx) |
| Evidence recording and retention | Native evidence/video database and recorder modules |

Changes to a threshold or state transition must be made at the authority that
enforces it. UI validation improves feedback but cannot replace Native or Agent
runtime gates. For control features, add tests for loss of input and loss of
lease—not only the nominal tracking path.
