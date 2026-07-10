# Atlas Backend

Atlas Backend is the policy and product layer for Atlas.

In the first skeleton it exposes only basic process endpoints:

- `GET /healthz`
- `GET /version`
- `POST /api/vehicle-agents/register`
- `POST /api/vehicle-agents/{vehicleAgentId}/heartbeat`
- `POST /api/vehicle-agents/{vehicleAgentId}/telemetry`
- `GET /api/drones`
- `GET /api/drones/stream`
- `GET /api/drones/{droneId}/perception/events?limit=25`
- `GET /api/drones/{droneId}/perception/status`
- `POST /api/drones/{droneId}/actions/{action}`

It also exposes a gRPC backend-vehicle-agent channel on `ATLAS_VEHICLE_AGENT_GRPC_ADDR`.
The vehicle agent opens this outbound stream, sends heartbeat and telemetry messages over
it, and the backend pushes authorized vehicle actions over it when the vehicle agent is
connected.

The same gRPC stream accepts onboard perception metadata:

- `PerceptionEvent` stores compact detection metadata in `perception_events`.
- `PerceptionHealth` updates live inference status for the drone.
- Video frames stay on RTSP/WebRTC and are not stored by these APIs.

`GET /api/drones` and `GET /api/drones/stream` expose these as separate
operator-facing health signals:

- `status` is derived from heartbeat age.
- `telemetry.state` is derived from latest telemetry freshness.
- `commandChannel.state` shows whether the vehicle-agent gRPC stream is connected.

Vehicle action delivery uses a short lease. When the backend sends an action to a
vehicle agent, it records `sent_to_vehicle_agent`, increments the delivery attempt, and sets a
lease deadline. The vehicle agent clears that lease by reporting `vehicle_agent_received`. If the
lease expires first, the action becomes eligible for redelivery.

Run locally:

```sh
go run ./cmd/atlas-backend
```

Use `ATLAS_BACKEND_ADDR` to change the listen address:

```sh
ATLAS_BACKEND_ADDR=:8081 go run ./cmd/atlas-backend
```

Use `ATLAS_VEHICLE_AGENT_GRPC_ADDR` to change the vehicle-agent gRPC listen address:

```sh
ATLAS_VEHICLE_AGENT_GRPC_ADDR=:9091 go run ./cmd/atlas-backend
```

Ground-machine HM30 defaults:

```sh
ATLAS_VEHICLE_AGENT_GRPC_ADDR=:9090 \
ATLAS_LOCAL_INPUTS_ENABLED=true \
ATLAS_LOCAL_VIDEO_RTSP_URL=rtsp://192.168.144.168:8554/atlas \
ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT=udp \
ATLAS_LOCAL_VIDEO_RTP_BUFFER_SIZE=256 \
go run ./cmd/atlas-backend
```

The local video relay is optimized for live operator preview. The backend pulls
the Pi RTSP stream over UDP by default and uses a bounded RTP queue before
writing to WebRTC. If RTSP control connects but no UDP RTP packets arrive, the
backend automatically retries the Pi RTSP stream over TCP so the browser does
not stay stuck waiting for its first decoded frame. Set
`ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT=tcp` only when you want to force TCP from the
start.

Register a local development agent:

```sh
curl -X POST http://127.0.0.1:8080/api/vehicle-agents/register \
  -H 'Content-Type: application/json' \
  -d '{
    "vehicleAgentId": "agent-001",
    "droneId": "drone-001",
    "droneName": "Training Quad 1",
    "vehicleAgentVersion": "0.1.0-dev"
  }'
```

Send a heartbeat:

```sh
curl -X POST http://127.0.0.1:8080/api/vehicle-agents/agent-001/heartbeat \
  -H 'Content-Type: application/json' \
  -d '{"vehicleAgentVersion": "0.1.0-dev"}'
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
