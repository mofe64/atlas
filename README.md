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

## Hardware Onboard MVP Runbook

Use this runbook for the real, non-SITL Atlas setup:

```text
Pixhawk 6C TELEM2 -> USB serial -> Raspberry Pi 5 / Atlas Agent
A8 camera RTSP -> Raspberry Pi 5 / GStreamer -> MediaMTX RTSP
Raspberry Pi 5 / Atlas Agent -> ngrok TCP -> native Atlas Backend on ground machine
Ground machine Atlas Backend -> browser UI over localhost HTTP/WebRTC
Docker Postgres -> native Atlas Backend
```

### Prerequisites

Ground machine:

- Docker with Docker Compose.
- Go 1.25.x for `atlas-backend` and `atlas-agent`.
- Node.js `22.13.1` through `nvm` for `atlas-ui`.
- `ngrok` CLI plus an auth token that supports TCP tunnels.
- Network reachability to the Pi RTSP endpoint, normally
  `rtsp://192.168.144.168:8554/atlas`.

Onboard Pi:

- Raspberry Pi 5 running Ubuntu 24.04 arm64 for the current hardware path.
- Pixhawk 6C TELEM2 connected through a USB serial adapter.
- Pixhawk TELEM2 baud configured to match the installer baud, currently
  `921600` for `SER_TEL2_BAUD`.
- A8/HM30 camera available at `rtsp://192.168.144.25:8554/main.264`.
- Network access from the Pi to `https://archive.raspberrypi.com` so the
  installer can download the public Hailo `.deb` packages into `~/hailo-debs`.

### Ports And Endpoints

| Purpose | Default |
| --- | --- |
| Backend HTTP | `http://127.0.0.1:8080` |
| Backend vehicle-agent gRPC | `127.0.0.1:9090` |
| ngrok tunnel | TCP tunnel to backend gRPC port `9090` |
| Docker Postgres | `127.0.0.1:5432` |
| Pi processed RTSP | `rtsp://192.168.144.168:8554/atlas` |
| Pi local MediaMTX RTSP | `rtsp://127.0.0.1:8554/atlas` |
| A8 camera input | `rtsp://192.168.144.25:8554/main.264` |
| MAVSDK server on Pi | `127.0.0.1:50051` |
| MAVLink Router output to MAVSDK | `127.0.0.1:14540` |
| Raw MAVLink observer | `udp-server://0.0.0.0:14550` |

### 1. Start The Ground Backend And Tunnel

Run this from the repo root on the ground machine:

```sh
export NGROK_AUTHTOKEN=your_ngrok_token
scripts/start-native-onboard-backend-tunnel.sh
```

The script starts Docker Postgres, applies migrations, runs `atlas-backend`
natively, then starts an ngrok TCP tunnel to backend gRPC port `9090`. Keep this
terminal open. The script prints a Pi installer command containing the current
ngrok `host:port`.

Optional low-latency video defaults:

```sh
export ATLAS_LOCAL_VIDEO_RTSP_URL=rtsp://192.168.144.168:8554/atlas
export ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT=udp
export ATLAS_LOCAL_VIDEO_RTP_BUFFER_SIZE=256
```

Use `ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT=tcp` only if UDP is blocked or unstable
between the ground machine and the Pi.

### 2. Identify The Pixhawk USB Serial Device

Run this on the Pi before installing:

```sh
for dev in /dev/serial/by-id/* /dev/ttyUSB* /dev/ttyACM*; do
  [ -e "$dev" ] && printf '%s -> %s\n' "$dev" "$(readlink -f "$dev")"
done
```

Use the stable `/dev/serial/by-id/...` path in the installer. The known
Prolific adapter path from the current setup is:

```text
/dev/serial/by-id/usb-Prolific_Technology_Inc._USB-Serial_Controller_EHDSb2A5414-if00-port0
```

### 3. Prepare Hailo Packages On Ubuntu

Ubuntu's default apt repositories do not ship the Raspberry Pi AI HAT+ Hailo
stack. On Ubuntu, the installer automatically creates `~/hailo-debs`, downloads
the matching Hailo package set from the public Raspberry Pi archive, then
installs those local `.deb` files. Raspberry Pi's AI software docs list the
Hailo package families and version-matching requirement:
https://www.raspberrypi.com/documentation/computers/ai.html

The default public package suite is `bookworm`, because `trixie` Hailo packages
require newer Python/OpenCV/libc packages than Ubuntu 24.04 provides. Use
`--hailo-deb-source none` and `--hailo-deb-dir /path/to/debs` only when using an
internal mirror or predownloaded package set.

The Ubuntu path intentionally uses a pinned package subset instead of the latest
`bookworm` HailoRT package. `hailort` `4.19+` conflicts with
`hailo-tappas-core` `3.29.1`, while newer TAPPAS packages depend on Raspberry Pi
OS Python/OpenCV packages that Ubuntu 24.04 does not provide.

### 4. Install The Onboard AI Stack On The Pi

Run the installer in Hailo mode:

```sh
atlas-agent/scripts/install-onboard-pi.sh \
  --ground-grpc <ngrok-host:port> \
  --video-pipeline-mode hailo \
  --hailo-hardware ai-hat-plus \
  --mavlink-device /dev/serial/by-id/usb-Prolific_Technology_Inc._USB-Serial_Controller_EHDSb2A5414-if00-port0 \
  --mavlink-baud 921600
```

If the Pi needs the local HM30/SIYI Ethernet address, add `--configure-eth0`.
That writes `/etc/netplan/99-siyi-eth0-local.yaml` with
`192.168.144.168/24`; apply it manually with:

```sh
sudo netplan try
```

Verify Hailo before starting the stack:

```sh
hailortcli fw-control identify
gst-inspect-1.0 hailonet hailooverlay
```

### 5. Start And Verify The Pi Services

```sh
atlas-agent/scripts/start-onboard-stack.sh
sleep 15
systemctl is-active atlas-mediamtx atlas-mavlink-router atlas-mavsdk atlas-agent atlas-video-agent
atlas-agent/scripts/status-onboard-stack.sh
```

The expected `systemctl is-active` output is five `active` lines.

Verify the processed RTSP stream on the Pi:

```sh
ffprobe -rtsp_transport tcp \
  -v error \
  -select_streams v:0 \
  -show_entries stream=codec_name,width,height \
  -of default=noprint_wrappers=1 \
  rtsp://127.0.0.1:8554/atlas
```

Expected output is H.264 video, normally `640x640`:

```text
codec_name=h264
width=640
height=640
```

### 6. Verify The Ground Backend And UI

On the ground machine:

```sh
curl -s http://127.0.0.1:8080/healthz
curl -s http://127.0.0.1:8080/api/local-video/status
```

Start the UI:

```sh
cd atlas-ui
nvm use
npm install
npm run dev
```

Open the Vite URL printed by `npm run dev`. The Fleet card should show the Pi
agent online, and the Local video panel should transition to `streaming` after
connecting.

### Troubleshooting

Hailo pipeline fails with `no element "hailonet"`:

- The Hailo GStreamer plugin is not installed or not visible to GStreamer.
- Confirm the installer downloaded Hailo packages into `~/hailo-debs`.
- Confirm the installer is using the default `bookworm` Hailo package suite on
  Ubuntu 24.04.
- If apt reports `hailort : Breaks: hailo-tappas-core (< 3.30.0)`, update this
  repo and rerun the installer so it selects the pinned Ubuntu-compatible
  HailoRT/TAPPAS pair.
- If `hailo-dkms` fails with
  `no previous prototype for 'hailo_pcie_is_device_ready_for_boot'`, update this
  repo and rerun the installer. The Ubuntu path patches the Hailo DKMS source
  before rebuilding it against the Raspberry Pi `6.8.*-raspi` kernel.
- Check `hailortcli fw-control identify` and
  `gst-inspect-1.0 hailonet hailooverlay`.

RTSP returns `404 Not Found` for `/atlas`:

- MediaMTX is running, but the video agent is not publishing the `/atlas`
  stream.
- Run `atlas-agent/scripts/status-onboard-stack.sh`.
- Check `atlas-video-agent` logs for GStreamer errors.

Browser says streaming but video is black or very delayed:

- Confirm the backend is running natively, not behind Docker bridge networking.
- Keep `ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT=udp` for the ground backend to Pi RTSP
  leg unless UDP is blocked.
- Pi-side latency knobs live in `~/.config/atlas-agent/onboard.env`:

```sh
ATLAS_A8_RTSP_LATENCY_MS=50
ATLAS_A8_RTSP_TRANSPORT=tcp
ATLAS_VIDEO_KEY_INT_MAX=15
```

After changing them:

```sh
sudo systemctl restart atlas-video-agent
```

MAVLink telemetry is missing:

- Confirm the Pixhawk USB serial path with the serial-device command above.
- Confirm `--mavlink-baud` matches Pixhawk `SER_TEL2_BAUD`.
- Confirm `atlas-mavlink-router`, `atlas-mavsdk`, and `atlas-agent` are active.

ngrok tunnel does not publish an endpoint:

- Confirm `ngrok` is installed and authenticated.
- Confirm the account supports TCP tunnels.
- Confirm local backend gRPC port `9090` is free before starting the native
  tunnel script.

### Stop And Cleanup

Stop the Pi services:

```sh
atlas-agent/scripts/start-onboard-stack.sh --stop
```

Stop the native backend and ngrok with `Ctrl-C` in the tunnel script terminal.
Docker Postgres is intentionally left running so local data survives restarts.

Cleanup the Pi Atlas setup while preserving FFmpeg/media dependencies and
MediaMTX:

```sh
atlas-agent/scripts/cleanup-onboard-pi.sh --dry-run
atlas-agent/scripts/cleanup-onboard-pi.sh --yes
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
