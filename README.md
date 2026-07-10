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

### SITL MAVLink fanout

By default, `scripts/start-sitl.sh` runs MAVProxy as the local MAVLink fanout
process. This matches the Atlas architecture: PX4 produces one MAVLink stream,
then a router fans it out to each consumer instead of making tools compete for
the same endpoint.

```sh
scripts/start-sitl.sh
```

The default topology is:

```text
PX4 SITL -> MAVProxy
  -> udp:127.0.0.1:14541  mavsdk_server / Atlas Agent
  -> udp:127.0.0.1:14552  Atlas Raw MAVLink Observer
  -> udp:127.0.0.1:14553  QGroundControl
```

QGroundControl is not the main reason for MAVProxy mode; it is one consumer on
the same fanout path as MAVSDK and the Atlas raw observer.

In QGroundControl:

1. Open Application Settings.
2. Open Comm Links.
3. Add a UDP link named `Atlas SITL`.
4. Set the local/listening port to `14553`.
5. Enable Auto Connect on Start if desired.
6. Save and connect the link.

Override or disable the QGC route with:

```sh
scripts/start-sitl.sh --qgc-out udp:127.0.0.1:14554
scripts/start-sitl.sh --qgc-out none
```

Direct mode is available only as an explicit fallback for narrow debugging:

```sh
scripts/start-sitl.sh --mavlink-router none
```

In direct mode, MAVProxy does not start, there is no QGC fanout on UDP `14553`,
and Atlas components connect directly to PX4 endpoints. Do not use direct mode
for normal SITL verification because it skips the routing layer used by the
intended architecture.

Backend:

```sh
cd atlas-backend
go run ./cmd/atlas-backend
```

### Native Non-SITL Backend Tunnel For Onboard Pi

Use this when the backend runs on a different computer from the onboard Pi.
The onboard agent connects to the backend over raw gRPC, so the tunnel must
expose TCP port `9090`; an HTTP-only tunnel URL is not enough.

Atlas uses ngrok TCP for this development path because it gives the Pi a plain
`host:port` endpoint that can be passed directly to `install-onboard-pi.sh`.
Only Postgres runs in Docker; the backend and ngrok run natively on the ground
machine so local RTSP/WebRTC/gRPC networking does not cross Docker's bridge.

```sh
export NGROK_AUTHTOKEN=your_ngrok_token
scripts/start-native-onboard-backend-tunnel.sh
```

By default the native backend reads the Pi RTSP stream from
`rtsp://192.168.144.168:8554/atlas` over UDP and uses a bounded WebRTC RTP
queue:

```sh
ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT=udp
ATLAS_LOCAL_VIDEO_RTP_BUFFER_SIZE=256
```

Set `ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT=tcp` only if UDP is blocked or unstable
between the ground machine and the Pi.

The script starts:

```text
Docker Postgres -> Docker migrations -> native atlas-backend -> native ngrok TCP tunnel
```

It then prints a command like:

```sh
atlas-agent/scripts/install-onboard-pi.sh --ground-grpc 1.tcp.ngrok.io:12345
```

For a stable TCP endpoint, reserve an ngrok TCP address and pass it as:

```sh
export NGROK_TCP_URL=tcp://1.tcp.ngrok.io:12345
scripts/start-native-onboard-backend-tunnel.sh
```

The backend connects to Docker Postgres through `127.0.0.1:5432`, which avoids
Docker bridge networking for the backend, RTSP, WebRTC, and ngrok.

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



Start PX4 SITL and `mavsdk_server`, then run the agent:

```sh
ATLAS_MAVSDK_GRPC_ADDR=127.0.0.1:50051
ATLAS_PX4_SYSTEM_ADDRESS=udpin://0.0.0.0:14540
ATLAS_MAVLINK_OBSERVER_ENDPOINT=udp-server://0.0.0.0:14550
```

`ATLAS_PX4_SYSTEM_ADDRESS` is the MAVSDK connection source. The read-only
MAVLink observer uses `ATLAS_MAVLINK_OBSERVER_ENDPOINT`; use UDP for SITL and a
serial or router-fed UDP endpoint for hardware.
# atlas
