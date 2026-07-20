# Atlas Native Ground Station

Atlas is a local-first Tauri v2 ground station. React renders the operator
interface while Rust owns the agent-facing gRPC server, embedded SQLite,
vehicle policy, mission execution records, and video/perception alignment.

There is currently no backend dependency, organization, user login, enrollment
token, or operator authentication.

For the system-wide newcomer path, component boundaries, state machines, and
code map, start with
[`../docs/README.md`](../docs/README.md). The Native-specific architecture is
documented in [`../docs/atlas-native.md`](../docs/atlas-native.md).

## Runtime topology

```text
React operations shell
    -> typed Tauri command
        -> Rust ground-station runtime
            -> supervised native FFmpeg RTSP decoder
                -> bounded delayed clean-frame buffer
                -> frame-aligned optional canvas overlay
            <- agent-initiated gRPC session over HM30
                <- onboard Go agent

Rust -> SQLite: drones, agents, bindings, communication-link sessions,
               latest aircraft telemetry, bounded PX4 status history
```

The native app listens on the HM30 ground address `192.168.144.50:7443` by
default. It does not expose the unauthenticated agent service on every network
interface.

## Run it

Native app:

```sh
nvm use 22.13.1
npm install
npm run tauri:dev:isolated
```

The isolated command stores development state at
`../.atlas-run/native-dev/atlas.db`, which is ignored by Git and cannot mutate
the normal installed application's database. An explicitly supplied absolute
`ATLAS_SQLITE_PATH` still takes precedence. Use `npm run tauri dev` only when
you deliberately want the platform application-data database.

Agent on the same machine for a loopback smoke test:

```sh
ATLAS_GROUND_STATION_ADDR=127.0.0.1:7443 \
ATLAS_AGENT_STATE_DIR=/tmp/atlas-agent-dev \
go run ../atlas-agent/cmd/atlas-agent
```

Real HM30 agent configuration:

```sh
ATLAS_GROUND_STATION_ADDR=192.168.144.50:7443
```

The mission execution camera view is decoded by the Rust host, not by the
WebView. Install FFmpeg on the operator computer or point Atlas at a bundled
binary, then configure the clean camera stream before startup:

```sh
ATLAS_VIDEO_RTSP_URL=rtsp://192.168.144.25:8554/main.264 \
ATLAS_VIDEO_DECODER_PATH=ffmpeg \
npm run tauri:dev:isolated
```

Video configuration:

| Variable | Default | Purpose |
| --- | --- | --- |
| `ATLAS_VIDEO_RTSP_URL` | `rtsp://192.168.144.25:8554/main.264` | Clean stream decoded by Native |
| `ATLAS_VIDEO_DECODER_PATH` | `ffmpeg` | FFmpeg executable or bundled sidecar path |
| `ATLAS_VIDEO_RTSP_TRANSPORT` | `tcp` | Native decoder RTSP transport (`tcp` or `udp`) |
| `ATLAS_VIDEO_SOURCE_ID` | `a8-main` | Must match the onboard perception source ID |
| `ATLAS_VIDEO_WIDTH` / `ATLAS_VIDEO_HEIGHT` | `1280` / `720` | Native display-frame size |
| `ATLAS_VIDEO_FPS` | `15` | Maximum frames delivered to the WebView |
| `ATLAS_VIDEO_JPEG_QUALITY` | `5` | FFmpeg MJPEG quality, where lower is higher quality |
| `ATLAS_VIDEO_PLAYOUT_DELAY_MS` | `350` | Bounded delay that lets detections catch their video frame |
| `ATLAS_VIDEO_ALIGNMENT_TOLERANCE_MS` | `180` | Maximum timing difference accepted for an overlay |
| `ATLAS_VIDEO_OVERLAY_OFFSET_MS` | `0` | Calibration offset for asymmetric RTSP/gRPC latency |

Local evidence recording reads the same source RTSP directly through a separate
FFmpeg process; it never records WebView canvases and does not use the A8
MicroSD. Evidence storage configuration:

| Variable | Default | Purpose |
| --- | --- | --- |
| `ATLAS_EVIDENCE_ROOT` | `<Atlas app data>/evidence` | Absolute local root containing recording `objects/`, reviewable `assets/`, recoverable `trash/`, and `temporary/` staging |
| `ATLAS_EVIDENCE_SEGMENT_SECONDS` | `30` | Closed-source segment target, from 2 to 600 seconds |
| `ATLAS_EVIDENCE_WARNING_FREE_BYTES` | `5368709120` | Free-space threshold that raises a warning |
| `ATLAS_EVIDENCE_STOP_FREE_BYTES` | `2147483648` | Reserved free-space boundary that safely stops/refuses recording |

Every published segment is first written below `temporary/`, hashed with
SHA-256, entered into SQLite as `FINALIZING`, atomically renamed below
`objects/`, verified again, and only then marked `LOCAL_VERIFIED`. SQLite
rejects successful session completion while any segment remains `FINALIZING`,
and recorder setup failures after `REQUESTED` become durable failed sessions so
the source reservation is released.

The Evidence workspace treats stills and event clips as first-class assets.
Stills are captured from the latest clean Native-decoded frame; when a track is
selected, its session-scoped track identity is retained on the asset. A track
evidence marker queues a pre/post-roll event clip and Atlas publishes it only
after verified recording segments cover the requested window. Both media types
receive a generated JPEG thumbnail and local integrity metadata before their
state changes from `PENDING` to `READY`.

Review state, notes, tags, and retention changes are append-only events. The
default retention policy keeps standard assets for 30 days, extended assets for
365 days, and recoverable trash for 7 days; operators can edit those intervals.
`LEGAL_HOLD` removes the expiry and blocks deletion. Deletion first moves the
asset directory atomically from `assets/` to `trash/`; only the hourly policy
worker can purge it after the recorded grace deadline. External export packages,
export manifests, and export checksum workflows are intentionally not included
in this slice.

Expanded incident-response planning can assess a local, reproducible
known-building snapshot without depending on internet availability:

| Variable | Default | Purpose |
| --- | --- | --- |
| `ATLAS_KNOWN_BUILDINGS_GEOJSON` | unset | Absolute path to an OS-derived GeoJSON `FeatureCollection`; unset means assessment status is `DATA_UNAVAILABLE` and preparation requires an operator override reason |

The collection must include an `atlasProvenance` object alongside `features`:

```json
{
  "type": "FeatureCollection",
  "atlasProvenance": {
    "provider": "Ordnance Survey",
    "product": "OS NGD Buildings + Building Height Attribute",
    "datasetId": "local-export-2026-07",
    "schemaVersion": "4.0",
    "release": "2026-07",
    "retrievedAtUnixMs": 1784332800000,
    "coverageBbox": [-0.20, 50.90, 0.10, 51.20]
  },
  "features": []
}
```

Polygon and MultiPolygon features are supported. Atlas reads current OS NGD
building fields such as `height_absolutemin_m`, `height_absolutemax_m`,
`height_relativemax_m`, `height_confidencelevel`, and `height_evidencedate`.
It also accepts joined legacy Building Height Attribute fields `AbsHMin`,
`AbsHMax`, `RelHMax`, `BHA_Conf`, and `BHA_ProcessDate`, using a feature `id`,
`osid`, `OS_TOPO_TOID`, or `toid` as evidence identity. A missing height,
aircraft home altitude, or route coverage remains explicit incomplete evidence.
The assessment checks only known building volumes in this snapshot; it is not
obstacle avoidance and never establishes an obstacle-free or safe route.
The immutable assessment records its departure point and home-altitude datum;
expanded-response upload requires fresh matching departure evidence or forces a
new review. A lone waypoint is checked as a point even when no departure was
available during preparation.

Native sends each clean JPEG and its matched detection metadata as one binary
IPC packet. A dedicated video canvas always receives unannotated pixels while a
second transparent canvas owns the boxes; “Clean feed” hides only that overlay.
Matching uses Native receive time minus
the Hailo adapter's measured inference latency, so the operator and aircraft do
not need perfectly synchronized wall clocks. The playout buffer and perception
history are bounded and latest-biased to prevent a slow WebView from building a
live-video backlog.

Override the native listener when needed:

```sh
ATLAS_GROUND_STATION_LISTEN_ADDR=127.0.0.1:7443 npm run tauri:dev:isolated
```

## Initial registration flow

1. The Rust host starts its gRPC server.
2. The Go agent loads or creates stable installation and drone IDs.
3. The agent opens a bidirectional session and sends registration first.
4. SQLite upserts the drone and agent, creates or reuses their active binding,
    closes any superseded communication link, and creates the new link.
   Archived drone identities reject registration until an operator restores
   them; each rejected attempt is recorded as a lifecycle event.
5. Rust returns the local agent, drone, binding, and communication-link IDs.
6. The agent sends a heartbeat every five seconds until the stream ends.
7. The agent samples its MAVSDK subscriptions and sends the latest telemetry
   snapshot without queueing stale samples.
8. PX4 status text is sent as a separate event stream so warnings and failsafe
   messages are retained instead of being overwritten by the next snapshot.

## Aircraft operations workspace

Selecting an aircraft opens one persistent workspace with five sections:

- **Overview** shows connection, readiness, power, position, and recent PX4
  information.
- **Live** shows the clean A8 feed, optional frame-aligned detection overlays,
  perception health, and leased inspection gimbal/zoom control.
- **Missions** shows current and previous runs and links into the dedicated
  planning/execution workspace.
- **History** shows aircraft-scoped telemetry charts and operational events.
- **Settings** exposes lifecycle state, archive, and restore.

Opening Live observes only. Physical payload controls require an acknowledged
inspection lease and fresh telemetry that explicitly reports both disarmed and
on-ground. Leaving Live, switching aircraft, losing renewal, or ending control
causes the Agent to stop angular rates and release gimbal ownership.

Archive is a reversible lifecycle transition, not deletion. An archived drone
is removed from operational fleet counts and mission selectors, while commands,
missions, telemetry, events, agents, and ended bindings remain queryable. Agent
reconnects are rejected and audited until an operator restores the drone.

The protocol is defined in `../proto/atlas/ground_station.proto`. The current
slice contains registration, heartbeat, telemetry, PX4 status events, durable
Hold/RTL/Land commands, and acknowledged gimbal angle/rate/centre commands.
Mission plans can be uploaded and controlled through the same agent-initiated
session, with agent/MAVSDK progress written back to durable mission runs.

The Operations response selector is backed by a Native suitability assessment,
not UI-only ordering. It excludes reserved or unfinished-run aircraft and
applies link, telemetry, position, PX4 health, battery, and response-capability
checks. Eligible aircraft are ranked by estimated arrival and battery, with
blockers and considerations returned for operator review. Upload and start
repeat their own authoritative checks because fleet state may change after a
recommendation is displayed.

Incident-response plans also carry an operator-reviewed arrival action chain.
Native creates durable action executions with requested, running, retrying,
succeeded, failed, and policy-applied states when the run is created. The
supported actions are `HOLD_AT_ARRIVAL`, optional
`POINT_GIMBAL_AT_INCIDENT`, and `RESUME_AFTER_ARRIVAL` for continuing patterns.
Area Scan and Orbit trigger this chain after their first generated waypoint and
resume only after acknowledgement. Reaching a waypoint is progress; the
assignment becomes `ON_SCENE` only for an observation pattern after Agent
reports an acknowledged arrival Hold. `HOLD_AT_STAGING` instead carries a
Hold-only `waitForOperatorDecision` action: Agent leaves the run `PAUSED` and
Native leaves the assignment `STAGED`, without setting the incident as on
scene or targeting the incident with the gimbal. Each plan explicitly selects
either Return to Launch or operator intervention as its exhausted-retry policy.

The separate **Follow** workspace authorizes bounded aircraft translation from
a validated world-space selected track. It requires a terrain-converged
coordinate, filtered target velocity, accepted uncertainty, fresh in-flight
telemetry, a reviewed standoff/altitude/speed/duration/geographic envelope, and
both boresight and aircraft-follow commissioning evidence. Native persists the
state machine and renews a four-second operator lease only while new exact-track
world state remains valid. Agent owns the PX4 Offboard setpoint loop and enters
explicit Hold on lease, target, link, telemetry, battery, position, altitude,
geofence, duration, or Offboard loss. This authority is independent of the
image-space gimbal-follow control; enabling one never enables the other.

Real translation is disabled by default. An Agent remains visibly
`UNVERIFIED` until configuration references accepted aircraft-follow validation
and physical camera/gimbal boresight alignment evidence. Code and simulated
controller tests do not by themselves satisfy HIL or controlled-flight
acceptance.

See [Incident dispatch](../docs/incident-dispatch.md) for the complete response
workflow and pattern semantics, and
[Inference, tracking, geolocation, and follow](../docs/inference-tracking-and-follow.md)
for the distinction between camera and aircraft follow.

## Embedded SQLite

The database is stored in Tauri's platform-specific application-data directory
as `atlas.db`. Startup enables foreign keys, full synchronous writes, WAL mode,
and a five-second busy timeout.

Development and SITL workflows can isolate the database with an absolute path:

```sh
ATLAS_SQLITE_PATH=/absolute/path/to/atlas-sitl.db npm run tauri:dev:isolated
```

The normal platform application-data location remains the default when this
variable is absent.

For ordinary native development, prefer the repeatable isolated shortcut:

```sh
npm run tauri:dev:isolated
```

The current schema version is 24. It contains durable records for:

- aircraft/Agent identity, bindings, communication links, lifecycle, telemetry,
  PX4 events, and command history;
- reusable mission definitions, immutable plans/items/actions, mission runs,
  and run events;
- incidents, revisioned audit events, aircraft assignments, arrival-action
  executions, and operational alerts;
- evidence recording sessions/segments/gaps, reviewable assets, annotations,
  events, and retention policy;
- perception sessions/tracks/events/samples, selections, annotations, counting,
  and selected-track geolocation;
- aircraft-follow sessions, target updates, and lifecycle events.

[`src-tauri/src/database/migrations.rs`](src-tauri/src/database/migrations.rs)
is the authoritative table and migration definition. A database newer than the
running binary is rejected rather than interpreted optimistically.

Mission definitions and executions are separate on purpose. Regenerating a plan
adds a new plan row. Every upload creates a new run rather than mutating an
earlier flight record.

## Mission planning

The Missions workspace implements three template/pattern pairs:

- `WAYPOINT` → `DIRECT_WAYPOINTS`
- `AREA_SCAN` → `LAWN_MOWER`
- `ROUTE_SCAN` → `ROUTE_FOLLOW`

Camera and gimbal behavior is part of each template rather than a separate
post-planning command. The v1 defaults are:

- Waypoint: `FORWARD_OBLIQUE`, pitch `-35°`, yaw follows aircraft heading.
- Area scan: `DOWNWARD_SCAN`, pitch `-90°`, yaw follows the scan-lane direction.
- Route scan: `FORWARD_OBLIQUE`, pitch `-40°`, yaw follows route bearing.

Operators can override these defaults for the whole definition. Waypoint
definitions can also override the view at an individual point, including a
`LOOK_AT_POINT` target. Generated plans emit semantic camera intent plus gimbal
and zoom actions before navigation, with point-scoped overrides on the relevant
waypoint. Agent translates supported vehicle yaw, gimbal, zoom, and recording
behavior. `SET_CAMERA_MODE` itself remains semantic intent rather than a promise
that every payload exposes an identical physical mode.

The complete geometry algorithms, action order, terrain model, and run-state
contract are documented in
[Mission types and flight patterns](../docs/mission-types-and-flight-patterns.md).

The native command boundary accepts `create_mission`, `update_mission`,
`mission_list`, `mission_detail`, `generate_mission_plan`, `mission_plan`,
`apply_mission_terrain_profile`, `upload_mission`, `control_mission_run`,
`mission_run_detail`, and `mission_run_history`. For example, the payload passed
to `create_mission` is:

```json
{
  "input": {
    "templateType": "AREA_SCAN",
    "name": "Park person search",
    "selectedPattern": "LAWN_MOWER",
    "params": {
      "areaPolygon": [
        { "latitude": 51.0001, "longitude": -0.1001 },
        { "latitude": 51.0001, "longitude": -0.1010 },
        { "latitude": 51.0008, "longitude": -0.1010 }
      ],
      "altitudeMeters": 35,
      "laneSpacingMeters": 25,
      "cameraMode": "DOWNWARD_SCAN",
      "gimbal": {
        "pitchDegrees": -90,
        "yawMode": "FOLLOW_LANE_DIRECTION",
        "stabilization": true
      },
      "returnToLaunch": true
    }
  }
}
```

Planning validates coordinates and 2–120 m altitude / 0.5–15 m/s speed bounds
before writing a definition. The lawn-mower planner clips scan lines in a local
tangent plane; this is suitable for small local sites but is not GIS-grade.

Every template can use either a fixed home-relative altitude or a v1
`TERRAIN_CLEARANCE` profile. Terrain-aware planning is deliberately two-pass:
Native first generates the route geometry, the webview samples the configured
raster DEM at the centre and both edges of the flight corridor, and Native then
validates those samples and persists a second immutable plan. The final relative
altitude at each station is based on terrain height above the profiled home,
requested ground clearance, and the operator safety margin. A backwards/forwards
envelope raises earlier or later stations when the configured climb or descent
rate would otherwise be exceeded. Missing DEM tiles, stale geometry, incomplete
corridor samples, and relative-ceiling breaches fail planning rather than
silently falling back to a flat altitude.

This is preflight route profiling, not PX4 live terrain following or obstacle
avoidance. A terrain-aware plan is tied to its sampled home; upload is blocked
when the selected aircraft's home is more than 30 m from that reference. The
plan metadata retains the DEM identity, home elevation, full sample evidence,
clearance settings, and a compact terrain/aircraft altitude series for operator
review and later audit.

### Mission upload and execution

Upload serializes the selected immutable `MissionPlan` and sends it to the
target Atlas Agent. The agent translates Atlas waypoints, speed, hold, vehicle
yaw, gimbal pitch/yaw, recording, land, and RTL-after-mission intent into the
MAVSDK Mission API. Upload progress and mission-item progress return over the
same bidirectional gRPC session.

Run states are `UPLOADING`, `READY`, `RUNNING`, `PAUSED`, `COMPLETED`, `FAILED`,
`CANCELLED`, and `RTL`. Start, pause, resume, cancel, and RTL requests create
append-only run events before delivery. Agent responses are idempotent by event
ID. One drone may have only one unfinished run, while a completed definition can
be uploaded again to create a new history record.

Before a run is created, Native compares the first generated waypoint with the
selected aircraft's reported home position, falling back to its current position.
Upload is blocked when no valid reference is available or when the waypoint is
more than 5 km away. This check runs before `mission_runs` insertion, so a
prevented deployment does not create false execution history.

Cancel pauses and clears the PX4 mission, leaving the aircraft in HOLD. RTL ends
the Atlas run after PX4 accepts Return-to-Launch; the operator must continue
monitoring until landing and disarm. `Arm & start mission` first requires a
connected aircraft with live telemetry, PX4 armable health, global and home
position readiness, and at least 15% battery when battery data is reported. The
agent first acknowledges any required `START_PERCEPTION` action from a fresh
onboard inference frame, then arms and starts the already-uploaded mission. If
perception cannot start, the aircraft remains unarmed. If mission start fails
after arming, Agent commands HOLD and releases the mission perception claim.
Resume never repeats the arm or perception-start step. `STOP_PERCEPTION` is
acknowledged at normal completion, while cancel and RTL always perform bounded
claim cleanup without delaying the safer flight action.

The execution workspace keeps the planned route, a fixed home marker, a moving
aircraft marker, and the flown telemetry trail on one map. Live marker movement
is independent of MapLibre style/source loading. On re-entry, the trail is
rebuilt from the run's persisted telemetry snapshots and then merged with new
live points, so navigating away does not erase the flight path.

### Operational mission map

Mission geometry is authored directly on a MapLibre map rather than through a
JSON field. Map clicks append waypoints, polygon vertices, or route points;
numbered vertices can be dragged or edited as precise coordinates. The map
renders the operator definition in green and the native-generated plan in orange.
When the planner opens, it centres on the previously selected connected aircraft
or the first connected aircraft. Live position is preferred and reported home is
the fallback; London is used only when neither is available. The planner displays
the active reference and warns immediately when the first point is outside the
5 km deployment boundary.
Saving a generated plan opens a separate execution workspace rather than adding
flight controls below the editor. That workspace renders the full route, current
mission item, completed/current legs, live aircraft heading and position, and an
aircraft trail merged from persisted snapshots and one-second native telemetry
updates. It also keeps
preflight readiness, arm/start/pause/resume/cancel/RTL controls, progress, durable
events, and this mission's execution history visible around the map. Saved
definitions can either reopen execution or return to the structured editor.
Updating one clears its current-plan pointer and generates a new immutable plan
while retaining previous plans for audit history.

The development default uses OpenStreetMap raster tiles with visible attribution:

```sh
VITE_ATLAS_MAP_TILE_URL=https://tile.openstreetmap.org/{z}/{x}/{y}.png npm run tauri:dev:isolated
```

`VITE_ATLAS_MAP_TILE_URL` is a build-time tile template and can point at an Atlas-
managed or commercial raster provider. When changing hosts, add that HTTPS origin
to the Tauri `connect-src` and `img-src` CSP entries. The public OpenStreetMap
service is best-effort and must not be used for bulk download or offline prefetch.

The map, geographic gimbal ROI, and terrain-aware planner share one raster DEM.
The default is the public Mapzen Terrain Tiles dataset listed in the AWS Open
Data Registry, using Terrarium encoding at zoom 12:

```sh
VITE_ATLAS_TERRAIN_TILE_URL=https://s3.amazonaws.com/elevation-tiles-prod/terrarium/{z}/{x}/{y}.png
VITE_ATLAS_TERRAIN_ENCODING=terrarium
VITE_ATLAS_TERRAIN_ZOOM=12
VITE_ATLAS_TERRAIN_VERTICAL_UNCERTAINTY_METERS=10
```

Map clicks used for geographic ROI now resolve DEM ground AMSL; the operator
enters only the target height above that ground point. When DEM lookup fails,
Atlas exposes an explicit manual AMSL fallback. Custom DEM origins must also be
allowed in Tauri's `connect-src` and `img-src` policy. Raster DEM uncertainty,
surface changes, vegetation, structures, and tile availability still require an
operational safety margin and operator review.

Selected-track geolocation uses this same configured DEM automatically. For
the calibration-free MVP it samples ground AMSL at the aircraft position and
projects the centred track onto that horizontal plane. If the DEM is
temporarily unavailable, Atlas falls back to the autopilot home-altitude plane
derived from absolute minus relative altitude with a conservative 25-metre
vertical uncertainty. Both paths store their source and assumptions; neither
is a claim of target-area terrain intersection or surveyed accuracy.

The current and historical telemetry rows include battery and power detail,
preflight health, altitudes, NED velocity and climb rate, landed state, RC
status, home position, and GPS quality. Current telemetry is updated for every
validated agent sample. Historical snapshots are captured every five seconds
while armed or airborne, every 30 seconds while idle, and immediately on armed,
in-air, flight-mode, or landed-state transitions. Status text is stored
separately because it is event history rather than sampled state.

Historical telemetry is retained for seven rolling days using Atlas Native's
receipt timestamp. Expired rows are removed at startup and when a new snapshot
is captured; SQLite reuses the freed pages for later data. History queries use
time ranges and stable cursor pagination, with pages capped at 500 rows.

Vehicle status events include their origin, stable event type, optional code,
and structured details. Atlas Native derives events for armed, in-air,
flight-mode, and landed-state transitions. Raw PX4 status text and derived
events remain in one chronological event stream.

The database is the native application's local operational source of truth. It
contains no authentication or backend-sync models.

## Project map

- `src/App.tsx`: local operations shell, workspace navigation, and link status.
- `src/fleet/`: fleet list plus aircraft Overview, Live, Missions, History, and
  Settings workspaces, including archive/restore.
- `src/history/`: detailed seven-day flight-history workspace with server-
  downsampled telemetry charts, a flight-mode ribbon, and vehicle-event timeline.
- `src/missions/`: MapLibre planning/tracking map, structured mission editor,
  dedicated execution workspace, live aircraft track, run controls, and reports.
- `src/video/`: clean-video canvas, aligned perception overlay, and renewable
  detection subscription lifecycle.
- `src-tauri/src/ground_station/`: protobuf boundary, tonic server, session state
  machine, registration mapping, telemetry and status-event mapping, and
  transport tests.
- `src-tauri/src/database/`: SQLite setup, migrations, registration, link
  lifecycle, telemetry persistence, projections, and repository tests.
- `src-tauri/src/commands.rs`: Tauri IPC commands and response DTOs.
- `src-tauri/src/lib.rs`: application composition and runtime startup only.
- `../proto/atlas/ground_station.proto`: shared Go/Rust protocol.

The `vehicle_telemetry_history` Tauri command exposes time-bounded, cursor-
paginated raw snapshot pages for detailed inspection and future export. It
returns 100 rows by default and accepts at most 500 rows per page.

The history workspace uses `vehicle_telemetry_chart_series` for bucketed chart
data and `vehicle_event_history` for the selected period's typed events. Chart
queries are capped at 1,200 points so a seven-day view does not load every raw
snapshot into the webview.

## Validate

```sh
npm run build
cargo test --manifest-path src-tauri/Cargo.toml
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
```
