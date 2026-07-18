# Atlas

Atlas is a local-first drone operations system. The Tauri desktop application
is the ground station, the Go Agent runs on the aircraft, and the two processes
communicate directly over the HM30/local Ethernet link. The operational flight
path does not require the Atlas Backend or an internet connection.

## Supported topology

```text
PX4 flight controller
    -> mavsdk_server on the onboard computer
        -> Atlas Agent
            -> agent-initiated gRPC session over HM30/Ethernet
                -> Atlas Native ground station
                    -> embedded SQLite operational history

SIYI A8 clean RTSP stream -------------------------> Atlas Native/FFmpeg
SIYI A8 stream -> Hailo inference -> Atlas Agent --> perception metadata
```

This separation is intentional:

- **Atlas Native** owns the operator UI, safety policy, durable command and
  mission records, SQLite, RTSP decoding, and the ground-station gRPC server.
- **Atlas Agent** owns PX4/MAVSDK integration, physical gimbal and camera
  control, perception-runtime supervision, and the outbound Native session.
- **Atlas Backend** is a separate Go/Gin/PostgreSQL foundation for identity and
  future coordinated services. It is not on the current aircraft-control path.

## Repository map

| Path | Purpose |
| --- | --- |
| `atlas/` | React + Tauri v2 Native ground station |
| `atlas-agent/` | Go onboard Agent, setup tools, systemd units, and Debian packaging |
| `atlas-backend/` | Optional Go/Gin/PostgreSQL backend foundation |
| `proto/atlas/ground_station.proto` | Shared Native/Agent transport contract |
| `scripts/start-sitl.sh` | Complete local PX4 Gazebo development stack |
| `scripts/tauri-dev-isolated.sh` | Native development with a repository-local SQLite database |
| `scripts/reset-databases.sh` | Destructive reset for current local development databases |
| `scripts/archive/deprecated-stack/` | Unsupported historical scripts; never used by current workflows |
| `docs/aircraft-operations-implementation.md` | Shipped aircraft workspace contracts and safety invariants |
| `third_party/mavsdk-proto/` | Pinned MAVSDK protobuf submodule used for code generation |

## Developer documentation

Start with [`docs/README.md`](docs/README.md) for the newcomer reading path and
repository map. The detailed architecture set covers:

- [System architecture and component boundaries](docs/architecture-overview.md)
- [Atlas Native internals](docs/atlas-native.md)
- [Atlas Agent internals](docs/atlas-agent.md)
- [The Native-Agent protocol](docs/native-agent-protocol.md)
- [Aircraft operations, missions, commands, and safety](docs/aircraft-operations-implementation.md)
- [Video and perception](docs/video-perception.md)
- [The separate Atlas Backend](docs/atlas-backend.md)
- [Development, validation, and debugging workflows](docs/development-guide.md)

The architecture documents describe shipped behavior. Product proposals and
future direction remain separate in
[`docs/feature-gap-assessment.md`](docs/feature-gap-assessment.md).

## What the current application supports

- Stable local drone and Agent identities with direct registration.
- Fleet monitoring, readiness, telemetry, PX4 status events, and seven-day
  history.
- Aircraft workspaces for Overview, Live inspection, Missions, History, and
  Settings.
- Safe drone archive/restore with retained operational history and rejected
  reconnect auditing while archived.
- Clean A8 video decoded by Native, optional frame-aligned Hailo detections,
  and continuous perception health.
- Leased inspection gimbal/zoom control while the aircraft is freshly known to
  be disarmed and on the ground.
- Waypoint, area-scan, and route-scan planning; immutable plans; terrain
  profiling; upload; execution; mission-scoped payload override; and reports.
- Durable Hold, Return-to-Launch, Land, mission, gimbal, and zoom command
  lifecycles.

The detailed component contracts and configuration live in
[`atlas/README.md`](atlas/README.md) and
[`atlas-agent/README.md`](atlas-agent/README.md).

## Prerequisites

Install only what the workflow you are running needs:

- Node.js `22.13.1`, npm, Rust, and Tauri platform dependencies for Native.
- Go `1.25` for the Agent and Backend.
- FFmpeg for Native A8 video decoding.
- Docker with Compose for the optional Backend.
- `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` for protobuf generation.
- A PX4-Autopilot checkout, Gazebo dependencies, and `mavsdk_server` for SITL.

After cloning, initialize the pinned MAVSDK protobuf source:

```sh
git submodule update --init --recursive
```

## Run Atlas Native safely for development

```sh
cd atlas
nvm use 22.13.1
npm install
npm run tauri:dev:isolated
```

`tauri:dev:isolated` stores development state in
`.atlas-run/native-dev/atlas.db` at the repository root. It cannot mutate the
normal installed application's platform data unless you deliberately override
`ATLAS_SQLITE_PATH`.

For a loopback Agent smoke test, expose Native only on loopback:

```sh
cd atlas
ATLAS_GROUND_STATION_LISTEN_ADDR=127.0.0.1:7443 \
  npm run tauri:dev:isolated
```

Then run the current Agent in another terminal:

```sh
cd atlas-agent
ATLAS_GROUND_STATION_ADDR=127.0.0.1:7443 \
ATLAS_AGENT_STATE_DIR=/tmp/atlas-agent-dev \
go run ./cmd/atlas-agent
```

Telemetry and vehicle commands require a reachable `mavsdk_server`; the Agent
will otherwise continue reconnecting while reporting the unavailable runtime.

## Run the complete PX4 SITL stack

The launcher starts the supported development path:

```text
PX4 Gazebo -> mavsdk_server -> Atlas Agent -> Atlas Native
```

From the repository root:

```sh
scripts/start-sitl.sh
```

The default PX4 checkout is the sibling directory `../PX4-Autopilot`. Override
it or inspect the resolved commands before launch with:

```sh
ATLAS_PX4_DIR=/absolute/path/to/PX4-Autopilot \
  scripts/start-sitl.sh --dry-run
```

Useful options include:

```sh
scripts/start-sitl.sh --px4-model gz_x500_gimbal --world baylands
scripts/start-sitl.sh --mavlink-router mavproxy --qgc-out udp:127.0.0.1:14550
scripts/start-sitl.sh --skip-px4 --skip-mavsdk
```

The launcher stores Native and Agent state under `.atlas-run/state/sitl/`,
writes process logs under `.atlas-run/logs/`, and stops its child processes on
exit. Run `scripts/start-sitl.sh --help` for the full override list.

## Install or upgrade the onboard Agent

The supported hardware profile is Raspberry Pi 5 on Ubuntu 24.04 arm64 with a
Raspberry Pi AI HAT+ and SIYI A8 camera. Build, clean-install, upgrade,
same-version replacement, validation, and rollback are documented in
[`atlas-agent/INSTALLATION.md`](atlas-agent/INSTALLATION.md).

Native and Agent share `proto/atlas/ground_station.proto`. When that contract
changes, build and deploy them as one coordinated release; mixed versions may
reject payload or mission commands even when the gRPC stream connects.

## Run the optional Backend

The Backend is independently runnable and does not proxy the current Agent
session:

```sh
cd atlas-backend
cp .env.example .env
docker compose up --build
```

See [`atlas-backend/README.md`](atlas-backend/README.md) for its identity,
PostgreSQL, API, and transaction model.

## Generate protocol clients

Regenerate the shared Native/Agent Go protocol after changing
`proto/atlas/ground_station.proto`:

```sh
scripts/generate-ground-station-proto-go.sh
```

Regenerate the selected MAVSDK Go clients from the pinned submodule after
changing the service selection or MAVSDK revision:

```sh
scripts/generate-mavsdk-go.sh
```

Generated protobuf files are committed. Review their diff alongside the source
contract.

## Reset local development data

To delete both the optional Backend's Compose PostgreSQL volume and Native's
normal platform SQLite database:

```sh
scripts/reset-databases.sh
```

The script is destructive, requires confirmation, and refuses to continue while
Atlas Native is running. Isolated development and SITL databases under
`.atlas-run/` are separate; delete only the specific state directory when you
intend to reset one of those environments.

## Validate

Native:

```sh
cd atlas
npm run build
cargo test --manifest-path src-tauri/Cargo.toml
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
```

Agent:

```sh
cd atlas-agent
go test ./...
go vet ./...
```

Backend:

```sh
cd atlas-backend
go test ./...
go vet ./...
```

Hardware validation remains necessary for the HM30 link, PX4 serial path,
camera RTSP stream, Hailo runtime, physical gimbal movement, and zoom.

## Historical scripts

Scripts under `scripts/archive/deprecated-stack/` are retained only for source
history and recovery investigation. Their referenced applications and protocol
no longer exist in the working tree. Do not use them for installation,
development, or deployment.
