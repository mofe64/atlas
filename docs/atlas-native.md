# Atlas Native

## Role

Atlas Native is the local ground-station authority. It is one Tauri v2
application with two cooperating runtimes:

- A React 19 webview renders the operator experience.
- A Rust host owns policy, persistence, Agent transport, video decoding, and
  Native services.

The main process composition is
[`atlas/src-tauri/src/lib.rs`](../atlas/src-tauri/src/lib.rs). The main UI
composition is [`atlas/src/App.tsx`](../atlas/src/App.tsx).

## Internal layers

```mermaid
flowchart TB
    Pages["React pages and components"]
    Invoke["Tauri invoke calls"]
    Commands["Rust command boundary"]
    Services["Ground-station, video, and planning services"]
    DB[("Local SQLite")]
    Agent["Atlas Agent gRPC streams"]
    FFmpeg["FFmpeg child process"]

    Pages --> Invoke --> Commands
    Commands --> DB
    Commands --> Services
    Services --> DB
    Services <--> Agent
    Services --> FFmpeg
```

### React presentation layer

[`atlas/src/App.tsx`](../atlas/src/App.tsx) owns top-level navigation and the
selected aircraft/mission context. The primary workspaces are:

- Fleet and aircraft detail.
- Mission planning.
- Mission execution.
- History.

The app polls Native snapshots once per second. This is a deliberate simple
boundary at the current scale: React asks for a coherent snapshot instead of
reconstructing operational truth from many browser-side event handlers.

Important UI modules:

| Module | Responsibility |
| --- | --- |
| [`fleet/FleetPage.tsx`](../atlas/src/fleet/FleetPage.tsx) | Operational and archived aircraft list |
| [`missions/MissionPage.tsx`](../atlas/src/missions/MissionPage.tsx) | Mission definition, map geometry, plan generation, and terrain workflow |
| [`missions/MissionExecutionPage.tsx`](../atlas/src/missions/MissionExecutionPage.tsx) | Upload, start, progress, pause/resume, cancel, RTL, live map, and payload override |
| [`missions/MissionPayloadControl.tsx`](../atlas/src/missions/MissionPayloadControl.tsx) | Inspection and mission-scoped payload leases |
| [`history/HistoryPage.tsx`](../atlas/src/history/HistoryPage.tsx) | Seven-day local telemetry and event history |
| [`video/LiveVideo.tsx`](../atlas/src/video/LiveVideo.tsx) | Clean video canvas, optional detection canvas, perception lease, and health metrics |

TypeScript types mirror Rust response JSON using camelCase serialization. The
shared types are not generated, so any Tauri response change must update both
the Rust structure and TypeScript consumer.

### Tauri command boundary

[`atlas/src-tauri/src/commands.rs`](../atlas/src-tauri/src/commands.rs) is the
webview-to-host API. It exposes read commands, write commands, and asynchronous
delivery commands.

Read examples:

- `fleet_snapshot`
- `vehicle_operations_snapshot`
- `mission_list`
- `mission_run_detail`
- `vehicle_command_history`
- `vehicle_telemetry_chart_series`
- `perception_snapshot`
- `video_stream_snapshot`

Write/delivery examples:

- `archive_drone` and `restore_drone`
- `create_mission`, `update_mission`, and `generate_mission_plan`
- `upload_mission` and `control_mission_run`
- `request_vehicle_command`
- perception frame-subscription start/renew/stop
- video stream start/stop

The command boundary is where UI input becomes trusted Native input. Rust
performs the authoritative validation even when React already disables an
unsafe button.

## Startup and managed state

At startup Native creates one `AppState` containing:

```text
Arc<LocalDatabase>
ground-station listen address
CommandRouter
PerceptionStore
VideoManager
```

The gRPC server and command-timeout loop run on Tauri's async runtime. The video
manager owns child-process and frame-buffer state. When the main window is
destroyed, Native stops the decoder so FFmpeg is not orphaned.

The default Agent listener is `192.168.144.50:7443`. Override it with
`ATLAS_GROUND_STATION_LISTEN_ADDR`; use loopback for local development.

## Ground-station service

[`atlas/src-tauri/src/ground_station/server.rs`](../atlas/src-tauri/src/ground_station/server.rs)
hosts two bidirectional gRPC methods:

- `OpenSession` for registration, heartbeat, telemetry, PX4 events, commands,
  and mission operations.
- `OpenPerceptionStream` for health, detection frames, and frame-demand leases.

The split prevents high-rate perception metadata from delaying command
acknowledgements.

### Session processing

[`ground_station/session.rs`](../atlas/src-tauri/src/ground_station/session.rs)
enforces:

1. Registration must be first.
2. The session ID cannot change after registration.
3. Telemetry, status, command updates, and mission updates require an active
   registered session.
4. Mission updates must target the drone bound to that stream.
5. Stream termination closes the communication-link record and unregisters the
   in-memory route.

### Command router

[`ground_station/command_router.rs`](../atlas/src-tauri/src/ground_station/command_router.rs)
maps a drone ID to the active response stream. It delivers:

- Vehicle command requests.
- Command cancellation requests.
- Mission operation requests.

The router is intentionally in memory because it represents current network
reachability. SQLite remains authoritative for what was requested and what
happened. After registration, the router attempts to deliver eligible pending
commands.

### Perception store

[`ground_station/perception.rs`](../atlas/src-tauri/src/ground_station/perception.rs)
holds bounded live state per drone and source:

- Latest frame and health.
- Up to 240 recent frames, bounded to ten seconds.
- Connection and staleness state.
- The outbound channel used to start, renew, or stop frame subscriptions.

Perception is live state, not durable history.

## Local SQLite

[`database/mod.rs`](../atlas/src-tauri/src/database/mod.rs) opens the database,
enables foreign keys, uses `synchronous=FULL`, requires WAL mode, configures a
five-second busy timeout, validates the SQLite version, applies migrations, and
prunes expired telemetry snapshots.

The normal file is `atlas.db` in the platform application-data directory.
`ATLAS_SQLITE_PATH` accepts only an absolute path and is used by isolated
development and SITL.

Schema version 12 contains:

| Area | Tables |
| --- | --- |
| Identity and connectivity | `drones`, `vehicle_agents`, `vehicle_agent_bindings`, `communication_links` |
| Telemetry and events | `vehicle_telemetry_current`, `vehicle_telemetry_snapshots`, `vehicle_status_events` |
| Commands | `vehicle_commands`, `vehicle_command_events` |
| Missions | `missions`, `mission_plans`, `mission_items`, `mission_actions`, `mission_runs`, `mission_run_events` |
| Aircraft lifecycle | `drone_lifecycle_events` |

Migrations are embedded in
[`database/migrations.rs`](../atlas/src-tauri/src/database/migrations.rs). They
are forward-only at application startup. A database with a newer schema version
is rejected rather than guessed at.

### Current versus historical telemetry

Every accepted telemetry message replaces one current row. Historical snapshots
are sampled:

- On the first sample.
- Every five seconds while active.
- Every thirty seconds while idle.
- Immediately on armed, in-air, flight-mode, or landed-state transitions.

Snapshots are retained for seven days. Derived state-transition events are
stored alongside PX4 status text. This balances useful history with bounded
local storage.

### Freshness

Snapshot presentation derives freshness at read time:

- A link is stale after 15 seconds without a heartbeat.
- Telemetry is live only when the link is connected and the sample is no more
  than five seconds old.
- Perception is stale after three seconds without a message.

Database rows are not rewritten merely to mark them stale. Freshness is a
comparison between stored timestamps and the current clock.

## Mission planning

Native owns mission definitions and plan generation in
[`database/missions.rs`](../atlas/src-tauri/src/database/missions.rs).

Supported template/pattern pairs are:

| Template | Pattern |
| --- | --- |
| `WAYPOINT` | `DIRECT_WAYPOINTS` |
| `AREA_SCAN` | `LAWN_MOWER` |
| `ROUTE_SCAN` | `ROUTE_FOLLOW` |

Definitions contain editable parameters. Generating a plan inserts a new
immutable plan, item, and action set, then points the definition at the new plan.
Old plans remain available to old mission runs.

Terrain clearance is a two-stage process:

1. Rust generates route geometry.
2. React samples the configured DEM along the centre and corridor edges.
3. Rust validates the evidence, climb/descent envelope, home reference, and
   ceiling before persisting a second immutable plan.

The detailed terrain evidence remains in Native. Upload sends the operational
plan while removing the bulky profile-point evidence from the wire payload.

## Video manager

[`atlas/src-tauri/src/video.rs`](../atlas/src-tauri/src/video.rs) supervises an
FFmpeg child process that:

1. Opens the clean RTSP stream.
2. Scales and pads to the configured dimensions.
3. Limits output frame rate.
4. Emits MJPEG frames through stdout.

Native parses the image stream into a bounded frame deque. A requested frame is
held until the configured playout delay, matched to perception metadata, then
returned as one binary `ATV1` packet containing a JSON header and clean JPEG.

The webview always receives clean pixels. Detection boxes are drawn on a second
transparent canvas and can be hidden without changing the source frame.

## Extension rules

When adding Native behavior:

- Put operator presentation in React.
- Put authoritative validation and state transitions in Rust.
- Put durable operational state in SQLite.
- Keep in-memory state for live routing, bounded media, and cache-like data only.
- Expose a coherent Tauri command rather than letting the UI coordinate several
  partially applied writes.
- Preserve append-only lifecycle events for commands and mission runs.
- Test policy and state transitions in Rust; test parsing and interaction-heavy
  rendering in TypeScript where feasible.

