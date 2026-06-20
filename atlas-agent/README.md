# Atlas Agent

Atlas Agent is the onboard service that will eventually run on the drone companion computer.

The first implementation only proves the backend-agent loop:

1. Register with Atlas Backend.
2. Send a heartbeat every five seconds.
3. Let the backend derive online/stale/offline state.

Phase 1 reads real PX4 SITL telemetry through `mavsdk_server` using generated
Go gRPC clients from `MAVSDK-Proto`. The agent does not generate simulated
telemetry.

If the backend is unavailable when the agent starts, the agent keeps retrying
registration with capped exponential backoff.

If a heartbeat fails later, the agent re-enters registration retry. This lets the
agent recover after a backend restart that clears the in-memory registry.

Run locally:

```sh
go run ./cmd/atlas-agent
```

Configuration:

```sh
ATLAS_BACKEND_URL=http://127.0.0.1:8080
ATLAS_AGENT_ID=agent-001
ATLAS_DRONE_ID=drone-001
ATLAS_DRONE_NAME="Training Quad 1"
ATLAS_AGENT_VERSION=0.1.0-dev
ATLAS_MAVSDK_GRPC_ADDR=127.0.0.1:50051
ATLAS_PX4_SYSTEM_ADDRESS=udpin://0.0.0.0:14540
```

Telemetry source:

```text
px4   PX4 telemetry through mavsdk_server gRPC
```

Generate MAVSDK Go clients after updating `third_party/mavsdk-proto`:

```sh
../scripts/generate-mavsdk-go.sh
```

PX4 SITL telemetry run sequence:

```sh
cd /Users/mofe/dev/sunnyside/PX4-Autopilot
source .venv/bin/activate
make px4_sitl gz_x500
```

In another terminal:

```sh
mavsdk_server -p 50051 udpin://0.0.0.0:14540
```

Then run the backend and start the agent with:

```sh
ATLAS_MAVSDK_GRPC_ADDR=127.0.0.1:50051 \
ATLAS_PX4_SYSTEM_ADDRESS=udpin://0.0.0.0:14540 \
go run ./cmd/atlas-agent
```
