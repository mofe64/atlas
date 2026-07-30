# Atlas Agent

Atlas Agent is the Go runtime on the onboard computer. It starts a direct gRPC
session to Atlas Native across the HM30 or local Ethernet network. It integrates
MAVSDK, PX4, payload, perception, and diagnostic services.

Atlas Agent does not connect to Atlas Backend.

## Documentation ownership

This README is the developer entry point for the Agent source tree. Use the
following canonical documents for detailed information:

| Need | Canonical document |
| --- | --- |
| Agent responsibilities, control flow, state, and component boundaries | [Agent architecture](../docs/atlas-agent.md) |
| Pi build, installation, configuration, services, updates, and troubleshooting | [Installation guide](INSTALLATION.md) |
| HM30/Pi deployment topology | [Architecture overview](../docs/architecture-overview.md#current-deployment-topology) |
| Native-Agent gRPC messages and compatibility | [Native-Agent protocol](../docs/native-agent-protocol.md) |
| Missions and arrival actions | [Mission types and flight patterns](../docs/mission-types-and-flight-patterns.md) |
| Inference, tracking, geolocation, and follow | [Inference, tracking, geolocation, and follow](../docs/inference-tracking-and-follow.md) |
| Local development and SITL | [Development guide](../docs/development-guide.md) |

Configuration is parsed and validated in
[`internal/config/config.go`](internal/config/config.go). Packaged configuration
is written to `/etc/atlas-agent/atlas-agent.env` by `atlas-setup`; the
installation guide is the operator-facing configuration reference.

## Runtime boundary

```text
Atlas Native
    <- outbound Agent gRPC session over HM30/local Ethernet
Atlas Agent
    -> mavsdk_server -> PX4
    -> SIYI UDP or MAVSDK camera control
    -> provider-neutral perception Unix socket

Atlas Spatial Runtime
    -> independent depth-camera service and local health socket
```

The Agent owns aircraft hardware integration and execution. Native owns
operator policy and durable operational records. The Spatial Runtime is
independent: depth-camera failure must not stop Agent telemetry or commands.

## Run locally

Start `mavsdk_server` first. For a serial flight controller:

```sh
mavsdk_server -p 50051 serial:///dev/serial0:921600
```

For PX4 SITL:

```sh
mavsdk_server -p 50051 udpin://0.0.0.0:14540
```

Then run Agent against a local Native listener:

```sh
ATLAS_GROUND_STATION_ADDR=127.0.0.1:7443 \
ATLAS_AGENT_STATE_DIR=/tmp/atlas-agent-dev \
go run ./cmd/atlas-agent
```

The state directory must be absolute. Without a reachable `mavsdk_server`, the
process can start but aircraft-facing capabilities remain unhealthy.

## Source map

| Path | Responsibility |
| --- | --- |
| `cmd/atlas-agent/` | Process composition and lifecycle |
| `cmd/atlas-setup/` | Pi discovery, configuration, installation, and doctor |
| `internal/config/` | Environment parsing and validation |
| `internal/identity/` | Stable installation and aircraft identity |
| `internal/telemetry/` | MAVSDK telemetry normalization |
| `internal/vehicle/` | Actions, missions, gimbal, camera, and follow controllers |
| `internal/perception/` | Provider-neutral inference and tracking boundary |
| `internal/transport/groundstation/` | Reconnecting Native session |
| `packaging/` | Debian package and systemd units |

## Generate protocol code

From the repository root:

```sh
scripts/generate-ground-station-proto-go.sh
scripts/generate-mavsdk-go.sh
```

The shared Atlas protobuf is generated into committed Go code and generated
for Rust during the Tauri build. MAVSDK generation follows the pinned
`packaging/mavsdk.env` dependency contract.

## Validate source

From `atlas-agent/`:

```sh
go test ./...
go vet ./...
python3 -m unittest discover -s scripts -p '*_test.py'
```

These checks validate source. Packaging does not replace or rerun them against
the built Debian artifact.

## Package and install

Use [INSTALLATION.md](INSTALLATION.md). It is the only supported Pi
installation and update procedure. Agent and Spatial are independently built
Debian packages; install the selected pair in one `apt` transaction and run
`atlas-setup` once to configure both.
