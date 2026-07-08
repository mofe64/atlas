# Atlas Agent

Atlas Agent is the onboard vehicle-agent service that will eventually run on the drone companion computer.

The first implementation only proves the backend-vehicle-agent loop:

1. Open the backend-vehicle-agent gRPC stream and register with a hello message.
2. Send heartbeat and telemetry messages over that same stream.
3. Receive authorized commands and mission actions over the stream.
4. Let the backend derive online/stale/offline state for the vehicle agent.

Phase 1 reads real PX4 SITL telemetry through `mavsdk_server` using generated
Go gRPC clients from `MAVSDK-Proto`. The agent does not generate simulated
telemetry.

PX4-specific code is kept behind the agent's Vehicle Gateway abstraction:

```text
atlas-agent/internal/vehicle
```

The rest of the agent should call the gateway interface for telemetry and
commands instead of importing generated MAVSDK protobuf packages directly.
The MAVSDK gateway currently implements telemetry plus arm, takeoff,
return-to-launch, and land actions.

Command delivery uses the gRPC backend-vehicle-agent stream. The vehicle agent
opens an outbound stream to the backend, the backend pushes authorized commands
over that stream, and the agent reports command lifecycle status back on the
same connection. Telemetry and heartbeat use the same stream, so the agent does
not run a separate HTTP polling loop.

The agent applies outbound backpressure by message importance:

- command status and hello messages use the critical queue and are not dropped;
- heartbeat messages use a small separate queue and may be skipped if the stream
  is badly backed up;
- telemetry keeps only the latest pending snapshot, so stale samples are dropped
  before they can delay command acknowledgements.

If the backend is unavailable when the agent starts, the agent keeps retrying
the gRPC channel connection with capped exponential backoff.

If the stream fails later, the vehicle agent reconnects and sends hello again.
This lets the vehicle agent recover after a backend restart, network
interruption, or lost session.

Run locally:

```sh
go run ./cmd/atlas-agent
```

Configuration:

```sh
ATLAS_VEHICLE_AGENT_ID=agent-001
ATLAS_DRONE_ID=drone-001
ATLAS_DRONE_NAME="Training Quad 1"
ATLAS_VEHICLE_AGENT_VERSION=0.1.0-dev
ATLAS_VEHICLE_AGENT_GRPC_ADDR=127.0.0.1:9090
ATLAS_MAVSDK_GRPC_ADDR=127.0.0.1:50051
ATLAS_PX4_SYSTEM_ADDRESS=udpin://0.0.0.0:14540
```

Default runtime intervals:

```text
heartbeat:      5s
telemetry:      2s
command timeout: 15s
```

Telemetry source:

```text
px4   PX4 telemetry through mavsdk_server gRPC
```

Generate Atlas backend-vehicle-agent gRPC clients after editing `proto/atlas/*.proto`:

```sh
../scripts/generate-atlas-proto-go.sh
```

Generate MAVSDK Go clients after updating `third_party/mavsdk-proto`:

```sh
../scripts/generate-mavsdk-go.sh
```

PX4 SITL telemetry run sequence:

From the repository root, the preferred Phase 3 development command is:

```sh
scripts/start-sitl.sh
```

It starts PX4 SITL, `mavsdk_server`, Atlas Backend, Atlas Agent, and Atlas UI.
Use `ATLAS_PX4_DIR=/path/to/PX4-Autopilot scripts/start-sitl.sh` if PX4 is not
checked out beside the Atlas repository.

Manual sequence:

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
