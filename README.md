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

The first goal is a stable backend-agent-UI loop before connecting Atlas to PX4 SITL:

```text
Agent starts
  -> registers with backend
  -> sends heartbeat every few seconds
  -> backend stores last heartbeat
  -> UI shows drone online/stale/offline
```

## Phase 1 Goal

The next goal is live vehicle state:

```text
Telemetry source
  -> atlas-agent
  -> atlas-backend
  -> atlas-ui
```

The first Phase 1 implementation uses simulated telemetry from `atlas-agent`.
PX4 SITL telemetry can replace that source once the API shape is proven.

Select the current source with:

```sh
ATLAS_TELEMETRY_SOURCE=simulated
```
# atlas
