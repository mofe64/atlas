# Atlas Development Guide

## Goal

This guide takes a new developer from a clean checkout to a safe local workflow,
then explains where to make common changes and how to validate them.

Read the [architecture overview](architecture-overview.md) before changing
behavior across Native and Agent.

## Prerequisites

Install only what your workflow needs:

| Workflow | Requirements |
| --- | --- |
| Native UI/Rust | Node.js, npm, Rust, Tauri platform dependencies |
| Native video | FFmpeg |
| Agent | Go 1.25 |
| Backend | Go 1.25 or Docker with Compose |
| Protocol generation | `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc` |
| Full SITL | PX4 checkout, PX4 Python environment, Gazebo, `mavsdk_server` |
| Agent package | Linux build host, Debian packaging tools, network access for pinned artifacts |

The repository README recommends Node `22.13.1`; `package.json` requires at
least `20.19.0`.

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
PX4 Gazebo
    -> mavsdk_server
        -> Atlas Agent
            -> Atlas Native
```

Defaults:

- PX4 checkout: sibling `../PX4-Autopilot`.
- Gazebo model: `gz_x500_gimbal`.
- World: `baylands`.
- Native and Agent state: `.atlas-run/state/sitl/`.
- Logs: timestamped directory under `.atlas-run/logs/`.
- Native/Agent transport: loopback.

Inspect commands without starting processes:

```sh
scripts/start-sitl.sh --dry-run
```

Useful examples:

```sh
scripts/start-sitl.sh --px4-model gz_x500_gimbal --world baylands
scripts/start-sitl.sh --mavlink-router mavproxy --qgc-out udp:127.0.0.1:14550
scripts/start-sitl.sh --skip-px4 --skip-mavsdk
```

The launcher checks dependencies and ports, waits for services, records logs,
and stops managed child processes on exit.

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
| Gimbal control unavailable | Capability list, discovered gimbal, payload context, lease owner, ground safety state |
| Video unavailable | FFmpeg path, RTSP URL/transport, camera reachability, decoder stderr |
| No overlays | Source-ID match, perception health, frame lease, alignment delta/tolerance, Hailo adapter |
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

