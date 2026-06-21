# Atlas Backend

Atlas Backend is the policy and product layer for Atlas.

In the first skeleton it exposes only basic process endpoints:

- `GET /healthz`
- `GET /version`
- `POST /api/agents/register`
- `POST /api/agents/{agentId}/heartbeat`
- `POST /api/agents/{agentId}/telemetry`
- `GET /api/drones`
- `GET /api/drones/stream`
- `POST /api/drones/{droneId}/commands/{command}`

It also exposes a gRPC backend-agent channel on `ATLAS_AGENT_GRPC_ADDR`.
The agent opens this outbound stream, sends heartbeat and telemetry messages over
it, and the backend pushes authorized commands over it when the agent is
connected.

`GET /api/drones` and `GET /api/drones/stream` expose these as separate
operator-facing health signals:

- `status` is derived from heartbeat age.
- `telemetry.state` is derived from latest telemetry freshness.
- `commandChannel.state` shows whether the agent gRPC stream is connected.

Command delivery uses a short lease. When the backend sends a command to an
agent, it records `sent_to_agent`, increments the delivery attempt, and sets a
lease deadline. The agent clears that lease by reporting `agent_received`. If the
lease expires first, the command becomes eligible for redelivery.

Run locally:

```sh
go run ./cmd/atlas-backend
```

Use `ATLAS_BACKEND_ADDR` to change the listen address:

```sh
ATLAS_BACKEND_ADDR=:8081 go run ./cmd/atlas-backend
```

Use `ATLAS_AGENT_GRPC_ADDR` to change the agent gRPC listen address:

```sh
ATLAS_AGENT_GRPC_ADDR=:9091 go run ./cmd/atlas-backend
```

Register a local development agent:

```sh
curl -X POST http://127.0.0.1:8080/api/agents/register \
  -H 'Content-Type: application/json' \
  -d '{
    "agentId": "agent-001",
    "droneId": "drone-001",
    "droneName": "Training Quad 1",
    "agentVersion": "0.1.0-dev"
  }'
```

Send a heartbeat:

```sh
curl -X POST http://127.0.0.1:8080/api/agents/agent-001/heartbeat \
  -H 'Content-Type: application/json' \
  -d '{"agentVersion": "0.1.0-dev"}'
```

Status is derived from heartbeat age:

```text
no heartbeat      registered
<= 15 seconds     online
<= 60 seconds     stale
> 60 seconds      offline
```

Telemetry freshness is derived separately:

```text
no telemetry      unknown
<= 5 seconds      fresh
<= 20 seconds     stale
> 20 seconds      lost
```
