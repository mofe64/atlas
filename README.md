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

Full PX4 SITL stack:

```sh
scripts/start-sitl.sh
```

This starts PX4 SITL, `mavsdk_server`, Atlas Backend, Atlas Agent, and Atlas UI
in order. Logs are written under `.atlas-run/logs/`, and Ctrl-C stops the
managed processes.

If your PX4 checkout is not beside this repo, point the script at it:

```sh
ATLAS_PX4_DIR=/path/to/PX4-Autopilot scripts/start-sitl.sh
```

Useful development variants:

```sh
scripts/start-sitl.sh --skip-px4
scripts/start-sitl.sh --skip-px4 --skip-mavsdk
scripts/start-sitl.sh --dry-run
```

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
