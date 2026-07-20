# Atlas Development Guide

## Goal

This guide takes a new developer from a clean checkout to a safe local workflow,
then explains where to make common changes and how to validate them.

Read the [architecture overview](architecture-overview.md) before changing
behavior across Native and Agent. Read the relevant behavioral guide before
changing a control path:

- [Mission types and flight patterns](mission-types-and-flight-patterns.md)
- [Incident dispatch](incident-dispatch.md)
- [Inference, tracking, geolocation, and follow](inference-tracking-and-follow.md)

## Prerequisites

Install only what your workflow needs:

| Workflow | Requirements |
| --- | --- |
| Native UI/Rust | Node.js 22.13.1, npm, Rust 1.97.0, Tauri platform dependencies |
| Native video | FFmpeg |
| Agent | Go 1.25 |
| Backend | Go 1.25 or Docker with Compose |
| Protocol generation | `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc` |
| Full SITL | PX4 checkout, PX4 Python environment, Gazebo, `mavsdk_server` |
| Agent package | Linux build host, Debian packaging tools, network access for pinned artifacts |

The repository README recommends Node `22.13.1`; `package.json` requires at
least `20.19.0`. Rust `1.97.0` is the toolchain validated against the current
Cargo lockfile; the older default `1.86.0` cannot resolve the current dependency
minimums.

Initialize the MAVSDK protobuf submodule:

```sh
git submodule update --init --recursive
```

## Fastest safe Native workflow

From the repository root:

```sh
cd atlas
nvm use 22.13.1
npm install
npm run tauri:dev:isolated
```

The isolated launcher sets an absolute database path under
`.atlas-run/native-dev/atlas.db`. It avoids modifying the normal installed
application database.

Use ordinary `npm run tauri dev` only when you intentionally want the platform
application-data database.

### Native-only expectations

Without Agent:

- The UI and local SQLite should start.
- The fleet may show previously registered local aircraft.
- No current aircraft link or telemetry will exist.
- Video can still work if the configured RTSP camera is reachable.

## Loopback Native-Agent smoke test

Terminal 1:

```sh
cd atlas
ATLAS_GROUND_STATION_LISTEN_ADDR=127.0.0.1:7443 \
  npm run tauri:dev:isolated
```

Terminal 2:

```sh
cd atlas-agent
ATLAS_GROUND_STATION_ADDR=127.0.0.1:7443 \
ATLAS_AGENT_STATE_DIR=/tmp/atlas-agent-dev \
go run ./cmd/atlas-agent
```

The Agent requires an absolute state directory. If `mavsdk_server` is not
reachable, the hardware-facing features will not be healthy. Use the full SITL
stack for telemetry and command testing.

## Full SITL

The supported launcher is:

```sh
scripts/start-sitl.sh
```

It starts:

```text
PX4 Gazebo camera -> local H.264 RTSP stream -> Atlas Native video
PX4 MAVLink        -> mavsdk_server -> Atlas Agent -> Atlas Native
```

Defaults:

- PX4 checkout: sibling `../PX4-Autopilot`.
- Gazebo model: `gz_x500_gimbal`.
- World: `baylands`.
- Native and Agent state: `.atlas-run/state/sitl/`.
- Logs: timestamped directory under `.atlas-run/logs/`.
- Native/Agent transport: loopback.
- Simulated gimbal video: `rtsp://127.0.0.1:8554/main.264`.

Inspect commands without starting processes:

```sh
scripts/start-sitl.sh --dry-run
```

Useful examples:

```sh
scripts/start-sitl.sh --px4-model gz_x500_gimbal --world baylands
scripts/start-sitl.sh --mavlink-router mavproxy --qgc-out udp:127.0.0.1:14550
scripts/start-sitl.sh --skip-px4 --skip-mavsdk
scripts/start-sitl.sh --skip-video
```

The launcher checks dependencies and ports, waits for services, records logs,
probes the RTSP endpoint for decodable H.264 before starting Native, and stops
managed child processes on exit.

### SITL video path

The default `gz_x500_gimbal` model publishes raw `gz.msgs.Image` frames on its
Gazebo Transport camera topic. `scripts/start-sitl.sh` compiles and starts the
repository's small GStreamer RTSP bridge, which converts those frames to H.264
and serves `/main.264` on loopback. The same launcher passes the resolved URL to
Atlas through `ATLAS_VIDEO_RTSP_URL`, so Map, Video, and Split modes exercise
the normal Native RTSP decoder rather than a SITL-only UI path.

Required development libraries must be visible through `pkg-config`:

- `gz-transport13` and `gz-msgs10`;
- `gstreamer-1.0`, `gstreamer-app-1.0`, and
  `gstreamer-rtsp-server-1.0`;
- one H.264 encoder element: `x264enc`, `vtenc_h264`, or `openh264enc`.

The common overrides are:

```sh
ATLAS_SITL_VIDEO_TOPIC=/world/baylands/model/x500_gimbal_0/link/camera_link/sensor/camera/image
ATLAS_SITL_VIDEO_PORT=8554
ATLAS_SITL_VIDEO_PATH=/main.264
ATLAS_SITL_VIDEO_BITRATE_KBPS=2500
ATLAS_VIDEO_SOURCE_ID=sitl-gimbal
```

Set `ATLAS_SITL_VIDEO_ENABLED=0` or pass `--skip-video` when attaching Atlas to
an external camera stream or when using a Gazebo model without a camera. If
`ATLAS_VIDEO_RTSP_URL` is explicitly set, Native uses that URL while the local
bridge remains available at the independently configured SITL URL.

### Incident-response flight acceptance

The expanded response-pattern acceptance owns the one simulated aircraft for
its whole run. Start only PX4, `mavsdk_server`, and the RTSP bridge in one
terminal:

```sh
scripts/start-sitl.sh --skip-native --skip-agent
```

Then run the serial Native-to-Agent acceptance matrix in a second terminal:

```sh
scripts/test-sitl-response-patterns.sh
```

The wrapper selects the ignored
`sitl_flies_response_patterns_with_continuous_video` test and always passes
`--test-threads=1`. It uses an isolated Native database and Agent state, then:

1. flies Hold at Staging and requires `PAUSED` / `STAGED` without an on-scene
   transition or incident-gimbal action;
2. returns to launch and waits for landed, disarmed, armable telemetry;
3. flies every generated Bounded Area Scan waypoint, checking the acknowledged
   Hold then Resume boundary, cumulative sequential MAVSDK route coverage, and
   one durable completion;
4. returns to launch before flying the 25-waypoint single-level Orbit, checking
   Hold, incident ROI, Resume, cumulative coverage of every waypoint, and
   completion;
5. pulls a new JPEG frame through Native's normal RTSP decoder before and while
   airborne in every case, then performs a final RTL cleanup.

Override endpoints when attaching the test to a non-default stack:

```sh
ATLAS_TEST_SITL_MAVSDK_ADDR=127.0.0.1:50052 \
ATLAS_TEST_SITL_RTSP_URL=rtsp://127.0.0.1:9554/main.264 \
scripts/test-sitl-response-patterns.sh
```

Do not run this matrix alongside another live SITL test or an interactive
Native/Agent pair. They would compete for mission, gimbal, and vehicle state on
the same simulated aircraft.

## Backend workflow

```sh
cd atlas-backend
cp .env.example .env
docker compose up --build
```

Verify:

```sh
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
```

Remember that this does not connect the Backend to Native or Agent.

## Common change workflows

### React UI change

Start with:

- [`atlas/src/App.tsx`](../atlas/src/App.tsx) for navigation/context.
- The relevant feature folder under [`atlas/src/`](../atlas/src/).
- [`atlas/src/operationsTypes.ts`](../atlas/src/operationsTypes.ts) or mission
  types for response shapes.

If the UI needs new data or behavior:

1. Add or change the Rust Tauri command.
2. Keep authoritative validation in Rust.
3. Update the TypeScript type.
4. Add loading, error, stale, and empty states.
5. Run the Native build and Rust tests.

### Native policy or persistence change

Start with:

- [`atlas/src-tauri/src/commands.rs`](../atlas/src-tauri/src/commands.rs) for
  the Tauri boundary and high-level operation orchestration.
- [`atlas/src-tauri/src/database/`](../atlas/src-tauri/src/database/) for
  state, SQL, and transitions.

When changing behavior:

1. Identify the invariant and owning table/state machine.
2. Add a new migration when schema changes.
3. Do not mutate historical command, plan, or run evidence in place.
4. Add targeted tests for allowed and rejected transitions.
5. Check that UI gating matches, but does not replace, Native policy.

### New vehicle command

A command usually crosses all of these:

1. Add enum/message semantics in
   [`ground_station.proto`](../proto/atlas/ground_station.proto).
2. Regenerate Agent Go protobuf code.
3. Add Native command type, parameter validation, policy, schema constraint if
   required, transition tests, and router mapping.
4. Add Agent type mapping and execution.
5. Advertise a capability when availability depends on discovered hardware.
6. Add UI and result handling.
7. Test deadline, wrong target, unsupported capability, duplicate event, and
   hardware failure behavior.

Do not bypass durable command records with a one-off direct stream message.

### Mission planner change

Mission definition and generation live in
[`atlas/src-tauri/src/database/missions.rs`](../atlas/src-tauri/src/database/missions.rs).
Use [Mission types and flight patterns](mission-types-and-flight-patterns.md) as
the current behavior contract.

Consider:

- Input validation and bounds.
- Whether a new generation creates a new immutable plan.
- Semantic action ordering.
- Agent translation support and warnings.
- UI map editing and TypeScript types.
- Upload/start safety implications.
- Existing runs that reference older plans.

### Mission execution change

The end-to-end path spans:

- Native command orchestration:
  [`commands.rs`](../atlas/src-tauri/src/commands.rs).
- Native run lifecycle:
  [`database/mission_runs.rs`](../atlas/src-tauri/src/database/mission_runs.rs).
- Shared protocol.
- Agent executor:
  [`atlas-agent/internal/vehicle/missions.go`](../atlas-agent/internal/vehicle/missions.go).
- Mission execution UI.

Define the allowed state transition first. Preserve the prior run state when a
control operation fails but the actual mission remains healthy.

### Incident response change

Start with [Incident dispatch](incident-dispatch.md), then trace the behavior
through:

- [`atlas/src/operations/`](../atlas/src/operations/) for intake, map review,
  suitability presentation, and dispatch workflow;
- [`atlas/src-tauri/src/database/incidents.rs`](../atlas/src-tauri/src/database/incidents.rs)
  for revision, suitability, preparation, reservation, and assignment policy;
- [`atlas/src-tauri/src/database/mission_actions.rs`](../atlas/src-tauri/src/database/mission_actions.rs)
  for durable arrival-action state;
- [`atlas-agent/internal/vehicle/missions.go`](../atlas-agent/internal/vehicle/missions.go)
  for waypoint-triggered execution and acknowledgements.

A new response pattern is cross-cutting. Add parameter validation, deterministic
geometry, capability requirements, preview/review representation, immutable
arrival policy, Agent translation, assignment-state behavior, and tests
together. Preserve the rule that preparation sends no aircraft command and
commits the plan/reservation/audit record atomically.

### Payload control change

Use the single
[`PayloadController`](../atlas-agent/internal/vehicle/payload.go). Do not create
a second gimbal/camera owner.

Check:

- Inspection versus mission context.
- Lease duration and expiry.
- Gimbal ownership release.
- Mission-intent restoration.
- UI cleanup and lost-focus behavior.
- Native telemetry and run policy.
- Hardware capability advertisement.

### Perception provider change

Keep provider-specific code outside the neutral types:

1. Produce validated frame and health envelopes from the provider.
2. Use protocol version `"1"`.
3. Write to the Agent-owned Unix socket.
4. Preserve latest-only behavior.
5. Keep video pixels and overlay rendering outside the Agent transport.
6. Add adapter and neutral-type tests.

### Tracking, geolocation, or follow change

Read
[Inference, tracking, geolocation, and follow](inference-tracking-and-follow.md)
before changing these paths. Keep their authorities separate:

- detector/provider adapters emit normalized observations;
- the Agent tracker owns session continuity and Atlas track IDs;
- selected-track geolocation binds exact track, frame timing, aircraft pose,
  gimbal pose, and the commissioned boresight model;
- Native performs iterative terrain refinement and motion filtering;
- camera follow owns only leased gimbal rates;
- Follow from standoff owns PX4 Offboard and must enter Hold when a runtime gate
  or lease fails.

Test discontinuity and failure cases explicitly: runtime reconnect, source or
stream-epoch change, stale/occluded/closed track, lease loss, bad timing,
uncertainty rejection, Offboard failure, and ground-stream loss. A nominal
moving target test alone does not validate a controller.

### Evidence or recording change

Evidence metadata is durable in SQLite while media bytes live under the
configured evidence root. Preserve the finalization invariant: a file is not a
valid asset merely because some bytes were written. Hashing, database state,
associations, and retention transitions must agree before Atlas reports success.

Start with [`atlas/src-tauri/src/recording.rs`](../atlas/src-tauri/src/recording.rs),
the evidence database modules, and [`atlas/src/evidence/`](../atlas/src/evidence/).
Test interrupted recording/finalization, low storage, missing media, trash and
restore, retention, and startup recovery in addition to the happy path.

### Protobuf change

After editing the source contract:

```sh
scripts/generate-ground-station-proto-go.sh
```

Review generated Go changes with the source `.proto`. Rust regenerates during
build.

Never renumber or reuse existing protobuf fields or enum values.

### MAVSDK service/schema change

MAVSDK is pinned as one contract:

- Server release and SHA-256.
- `third_party/mavsdk-proto` commit.
- Generated Go clients.
- `internal/mavsdkpb/schema.commit`.

Update
[`atlas-agent/packaging/mavsdk.env`](../atlas-agent/packaging/mavsdk.env),
checkout the matching submodule revision, then run:

```sh
scripts/generate-mavsdk-go.sh
```

The package build rejects mismatches.

### Backend API or schema change

For a new endpoint:

1. Define request/response transport shapes in `internal/httpapi`.
2. Put business and security rules in a service.
3. Use repository interfaces and `TxManager`.
4. Add a new numbered migration for schema changes.
5. Add handler, service, repository, and integration tests as appropriate.
6. Preserve organization scoping from the authenticated session.

Do not edit an already applied shared migration.

## Validation commands

Run checks from the relevant component.

### Native

```sh
cd atlas
npm run build
cargo test --manifest-path src-tauri/Cargo.toml
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
```

### Agent

```sh
cd atlas-agent
go test ./...
go vet ./...
```

### Backend

```sh
cd atlas-backend
go test ./...
go vet ./...
```

### Hailo adapter unit test

```sh
cd atlas-agent
python3 scripts/atlas_hailort_adapter_test.py
```

Hardware changes still require a field check for serial MAVLink, HM30 routing,
RTSP, Hailo, gimbal movement, camera zoom, and real PX4 safety behavior.

## Debugging map

| Symptom | First checks |
| --- | --- |
| Agent never appears in Fleet | Native listener address, Agent `ATLAS_GROUND_STATION_ADDR`, HM30 route, Native log, Agent registration log |
| Link is stale | Heartbeat age, Agent process health, radio/Ethernet continuity |
| No telemetry | `mavsdk_server`, `ATLAS_MAVSDK_GRPC_ADDR`, PX4 connection, Agent telemetry logs |
| Command button disabled | Connection, telemetry freshness, in-air/ground policy, Agent capabilities |
| Command stuck | Command event history, deadline, Agent log, MAVSDK result |
| Mission upload blocked | Ready plan, selected aircraft, first-waypoint distance, terrain-home match, unfinished run |
| Mission start blocked | Live telemetry, PX4 armability, global/home position, battery |
| Incident response cannot be prepared | Incident revision/status/location, selected pattern inputs, aircraft suitability blockers, active assignment/run, known-building override |
| Prepared response became stale | Incident revision changed; preview and prepare again rather than editing the immutable plan |
| Arrival state does not advance | Mission action execution/event history, trigger waypoint, Agent acknowledgement, retry/failure policy |
| Gimbal control unavailable | Capability list, discovered gimbal, payload context, lease owner, ground safety state |
| Video unavailable | FFmpeg path, RTSP URL/transport, camera reachability, decoder stderr |
| No overlays | Source-ID match, perception health, frame lease, alignment delta/tolerance, Hailo adapter |
| Selection disappeared | Tracker session reset reason, source/stream epoch, lifecycle (`LOST`/`CLOSED`), exact selection ID |
| Geolocation rejected | Track state/centering, frame timing, pose/gimbal history, boresight commissioning, depression/range, DEM source |
| Camera follow stopped | Exact track lifecycle/freshness, payload lease, measured gimbal state, angle limits, read/write result |
| Follow entered `DEGRADED_HOLD` | Durable exit reason, operator lease, target/telemetry age, battery, altitude/boundary, PX4 Offboard state |
| Evidence asset unavailable | Recorder session/segment state, evidence gap events, hash/finalization, retention/trash state, local bytes |
| Backend not ready | PostgreSQL health, migration service, database URL |

### Logs and diagnostics

SITL logs are under `.atlas-run/logs/`.

On packaged hardware:

```sh
sudo atlas-setup doctor
systemctl --no-pager --full status \
  atlas-mavsdk.service \
  atlas-agent.service \
  atlas-hailo-adapter.service
journalctl -u atlas-agent.service -f
journalctl -u atlas-hailo-adapter.service -f
```

## Database safety

Prefer isolated Native or SITL databases for development.

The root reset script is destructive:

```sh
scripts/reset-databases.sh
```

It deletes the normal Native database and the Backend Compose PostgreSQL volume,
requires confirmation, and refuses to run while Native is open. It does not
delete isolated `.atlas-run/` databases.

Never delete Agent identity state as routine troubleshooting. That changes the
installation and drone IDs presented to Native.

## First contribution suggestions

Good first changes have one clear owner:

- Improve a React empty/error/stale state without changing policy.
- Add a missing unit test for Native validation.
- Add a test around Agent configuration or perception validation.
- Improve a Backend handler/service test.
- Clarify a setup diagnostic.
- Add observability around an existing state transition.

Cross-cutting command, mission, payload, protocol, or schema changes are
appropriate after understanding the relevant state machine and running the full
component validation.

## Documentation update checklist

Update documentation when a change affects:

- Component ownership or boundaries.
- A state machine or safety gate.
- A new command, mission action, or capability.
- Environment configuration.
- Database schema or retention.
- Protocol compatibility.
- Deployment/service topology.
- A supported development, installation, or recovery procedure.

Keep future proposals in the feature-gap assessment or a dedicated design
document. Keep these architecture documents focused on shipped behavior.
