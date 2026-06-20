# Atlas

Atlas is a drone operations system around PX4-based drones.

PX4 remains the flight-control authority. Atlas provides the surrounding product layer:

- operator UI
- backend policy and audit
- onboard agent connectivity
- telemetry freshness
- mission and command workflows
- video and link health later

## Apps

```text
atlas-backend  Go backend service
atlas-agent    Go onboard agent prototype
atlas-ui       React, TypeScript, Tailwind frontend
```

## Local Development

Backend:

```sh
cd atlas-backend
go run ./cmd/atlas-backend
```

UI:

```sh
cd atlas-ui
npm install
npm run dev
```

Agent:

```sh
cd atlas-agent
go run ./cmd/atlas-agent
```

## Phase 0 Goal

The first goal is a stable backend-agent-UI loop connected to PX4 SITL telemetry:

```text
Agent starts
  -> registers with backend
  -> sends heartbeat every few seconds
  -> backend stores last heartbeat
  -> UI shows drone online/stale/offline
```

## Phase 1 Goal

The next goal is live vehicle state from PX4:

```text
PX4 SITL
  -> mavsdk_server
  -> atlas-agent
  -> atlas-backend
  -> atlas-ui
```

Start PX4 SITL and `mavsdk_server`, then run the agent:

```sh
ATLAS_MAVSDK_GRPC_ADDR=127.0.0.1:50051
ATLAS_PX4_SYSTEM_ADDRESS=udpin://0.0.0.0:14540
```
# atlas
