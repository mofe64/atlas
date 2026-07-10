# Atlas Agent

Atlas Agent is the onboard vehicle-agent service that will eventually run on the drone companion computer.

The first implementation only proves the backend-vehicle-agent loop:

1. Open the backend-vehicle-agent gRPC stream and register with a hello message.
2. Send heartbeat and telemetry messages over that same stream.
3. Receive authorized vehicle actions and mission actions over the stream.
4. Let the backend derive online/stale/offline state for the vehicle agent.

Phase 1 reads real PX4 SITL telemetry through `mavsdk_server` using generated
Go gRPC clients from `MAVSDK-Proto`. The agent does not generate simulated
telemetry.

PX4-specific code is kept behind the agent's Vehicle Gateway abstraction:

```text
atlas-agent/internal/vehicle
```

The rest of the agent should call the gateway interface for telemetry and
vehicle operations instead of importing generated MAVSDK protobuf packages directly.
The MAVSDK gateway currently implements telemetry plus arm, takeoff,
return-to-launch, and land actions.

Vehicle action delivery uses the gRPC backend-vehicle-agent stream. The vehicle agent
opens an outbound stream to the backend, the backend pushes authorized vehicle actions
over that stream, and the agent reports vehicle action lifecycle status back on the
same connection. Telemetry and heartbeat use the same stream, so the agent does
not run a separate HTTP polling loop.

The agent applies outbound backpressure by message importance:

- vehicle action status and hello messages use the critical queue and are not dropped;
- heartbeat messages use a small separate queue and may be skipped if the stream
  is badly backed up;
- telemetry keeps only the latest pending snapshot, so stale samples are dropped
  before they can delay vehicle action acknowledgements;
- perception metadata uses a bounded advisory queue, so detection bursts cannot
  block vehicle action or mission status.

If the backend is unavailable when the agent starts, the agent keeps retrying
the gRPC channel connection with capped exponential backoff.

If the stream fails later, the vehicle agent reconnects and sends hello again.
This lets the vehicle agent recover after a backend restart, network
interruption, or lost session.

Run locally:

```sh
go run ./cmd/atlas-agent
```

The agent module targets Go 1.25 because the live MAVLink observer uses
`gomavlib` v4.

Configuration:

```sh
ATLAS_VEHICLE_AGENT_ID=agent-001
ATLAS_DRONE_ID=drone-001
ATLAS_DRONE_NAME="Training Quad 1"
ATLAS_VEHICLE_AGENT_VERSION=0.1.0-dev
ATLAS_VEHICLE_AGENT_GRPC_ADDR=127.0.0.1:9090
ATLAS_MAVSDK_GRPC_ADDR=127.0.0.1:50051
ATLAS_PX4_SYSTEM_ADDRESS=udpin://0.0.0.0:14540
ATLAS_MAVLINK_OBSERVER_ENDPOINT=udp-server://0.0.0.0:14550
ATLAS_PERCEPTION_METADATA_PATH=~/.local/state/atlas-agent/perception/metadata.jsonl
```

Default runtime intervals:

```text
heartbeat:      5s
telemetry:      2s
command timeout: 15s
```

Onboard perception MVP:

- `scripts/atlas-video-agent.py` runs the Pi-side Hailo/GStreamer video pipeline.
- The raw A8 input defaults to `rtsp://192.168.144.25:8554/main.264`.
- The processed MediaMTX output defaults to `rtsp://127.0.0.1:8554/atlas`.
- The ground machine should read `rtsp://192.168.144.168:8554/atlas`.
- The video agent uses GStreamer's dynamic decoder path with
  `ATLAS_A8_RTP_CODEC=auto`; override with `ATLAS_A8_RTP_CODEC=h264` or `h265`
  only when you want to force a specific RTP depayloader.
- Use `ATLAS_VIDEO_PIPELINE_MODE=passthrough` to validate camera -> MediaMTX ->
  UI video without requiring the Hailo runtime. Use `hailo` for the inference
  pipeline; the installer then attempts to install the matching Hailo apt
  packages and fails if `hailonet` or `hailooverlay` are still unavailable.
- On Raspberry Pi OS, the Hailo install defaults to the AI Kit / AI HAT+
  package family (`hailo-all`). Use `--hailo-hardware ai-hat-plus-2` for
  AI HAT+ 2 (`hailo-h10-all`).
- On Ubuntu, the installer does not add Raspberry Pi OS repositories. If
  `--hailo-apt-packages` is provided, it installs those Ubuntu/Hailo apt package
  names. Otherwise, Ubuntu defaults to downloading the public Raspberry Pi Hailo
  `.deb` package set into `~/hailo-debs`, then installing those local files. Use
  the default `bookworm` package suite on Ubuntu 24.04; `trixie` requires newer
  Python/OpenCV/libc packages than Ubuntu 24.04 provides. Use
  `--hailo-deb-source none` and `--hailo-deb-dir /path/to/hailo-debs` only when
  using an internal mirror or predownloaded package set.
- The RTSP publish stage requires the `rtspclientsink` GStreamer element, installed
  by Ubuntu's `gstreamer1.0-rtsp` package.
- The video pipeline is tuned for operator preview latency, not archival
  completeness. `ATLAS_A8_RTSP_LATENCY_MS` defaults to `50`, the pipeline uses
  leaky one-buffer queues between receive/decode/inference/encode stages, and
  `ATLAS_VIDEO_KEY_INT_MAX` defaults to `15` so a dropped H.264 frame recovers
  quickly at the next keyframe.
- `ATLAS_A8_RTSP_TRANSPORT` defaults to `tcp` between the A8 camera and the Pi
  because that leg is usually a direct local camera link. If the camera/link
  accumulates latency, try `ATLAS_A8_RTSP_TRANSPORT=udp` and restart
  `atlas-video-agent`.
- Runtime health and compact detections are written as JSONL to `ATLAS_PERCEPTION_METADATA_PATH`.
- `atlas-agent` tails that JSONL file and forwards `PerceptionEvent` and `PerceptionHealth` on the existing vehicle-agent gRPC stream.

Run the video service dry-run locally:

```sh
ATLAS_PERCEPTION_MODEL_PATH=/opt/atlas/models/yolov6n.hef \
scripts/atlas-video-agent.py --dry-run
```

Raspberry Pi one-run setup:

```sh
scripts/install-onboard-pi.sh --dry-run --ground-grpc 192.168.144.50:9090
scripts/install-onboard-pi.sh --ground-grpc 192.168.144.50:9090 --configure-eth0
scripts/install-onboard-pi.sh --ground-grpc 192.168.144.50:9090 --video-pipeline-mode passthrough
scripts/install-onboard-pi.sh --ground-grpc 192.168.144.50:9090 --video-pipeline-mode hailo --hailo-hardware ai-kit
scripts/install-onboard-pi.sh --ground-grpc 192.168.144.50:9090 --video-pipeline-mode hailo --hailo-apt-packages "dkms hailo-dkms hailort hailo-tappas-core"
scripts/install-onboard-pi.sh --ground-grpc 192.168.144.50:9090 --video-pipeline-mode hailo --hailo-hardware ai-hat-plus
scripts/start-onboard-stack.sh
scripts/status-onboard-stack.sh
```

Existing installs can tune preview latency by editing
`~/.config/atlas-agent/onboard.env`:

```sh
ATLAS_A8_RTSP_LATENCY_MS=50
ATLAS_A8_RTSP_TRANSPORT=tcp
ATLAS_VIDEO_KEY_INT_MAX=15
```

Then restart the video service:

```sh
sudo systemctl restart atlas-video-agent
```

For the Raspberry Pi AI HAT+ hardware path on Ubuntu, run the installer with
the AI HAT+ selector and the stable USB serial path for the Pixhawk TELEM2
adapter:

```sh
scripts/install-onboard-pi.sh \
  --ground-grpc 0.tcp.eu.ngrok.io:14289 \
  --video-pipeline-mode hailo \
  --hailo-hardware ai-hat-plus \
  --mavlink-device /dev/serial/by-id/usb-Prolific_Technology_Inc._USB-Serial_Controller_EHDSb2A5414-if00-port0 \
  --mavlink-baud 921600
```

Replace `0.tcp.eu.ngrok.io:14289` with the current ngrok TCP endpoint printed
by the ground-machine backend tunnel script. On Ubuntu, `~/hailo-debs` must
be writable; the installer downloads a matching Hailo package set there. Verify
with:

```sh
hailortcli fw-control identify
gst-inspect-1.0 hailonet hailooverlay
```

On Ubuntu 24.04 arm64, `mavlink-router` may not exist in the enabled apt
repositories. The installer handles that by building `mavlink-routerd` from the
upstream source with Meson/Ninja and installing it under `/usr`.

Cleanup the Atlas agent setup while preserving FFmpeg/media dependencies and
MediaMTX:

```sh
scripts/cleanup-onboard-pi.sh --dry-run
scripts/cleanup-onboard-pi.sh --yes
```

Network config and package removal are opt-in because they can affect the rest
of the Pi:

```sh
scripts/cleanup-onboard-pi.sh --yes --remove-eth0-config
scripts/cleanup-onboard-pi.sh --yes --purge-agent-packages
```

Telemetry source:

```text
px4   PX4 telemetry through mavsdk_server gRPC
```

Raw MAVLink observer:

```text
atlas-agent/internal/mavlinkobserver
```

The observer is always enabled and is selected by
`ATLAS_MAVLINK_OBSERVER_ENDPOINT`. It uses `gomavlib` for live MAVLink parsing
and emits typed observations for heartbeat, system status, battery status, GPS,
global position, status text, mission current, and command ACK messages. It is
supplemental evidence beside MAVSDK telemetry; it is not a command gateway and
does not replace the `px4` telemetry source.

Supported observer endpoints:

```text
udp-server://0.0.0.0:14550              listen for MAVLink UDP datagrams
udp-client://127.0.0.1:14550            connect to a MAVLink UDP peer
serial:///dev/ttyUSB0?baud=57600        read a telemetry serial device
serial:///dev/ttyAMA0?baud=921600       read a high-speed companion link
```

For local SITL, the default UDP server endpoint is the right starting point.
For hardware, configure a serial endpoint only when the companion computer has a
dedicated telemetry tap, or point the observer at a MAVLink router UDP output.
Do not put the observer and MAVSDK in competition for the same exclusive serial
device.

For reliable `COMMAND_ACK` evidence, the observer must see the MAVLink path that
receives the vehicle's ACKs. In practice that means using a MAVLink router/fanout
when MAVSDK is also sending commands. A separate UDP listener can see heartbeat
and telemetry but may miss ACKs that are routed only back to MAVSDK's channel.

Raspberry Pi MAVLink Router setup:

```sh
scripts/setup-mavlink-router.sh --device /dev/serial0 --baud 921600
```

The script generates:

```text
~/.config/atlas-agent/mavlink-router/main.conf
~/.config/atlas-agent/mavlink-router/atlas-mavlink.env
```

It does not require systemd. To run the router in the foreground:

```sh
scripts/setup-mavlink-router.sh --start
```

If `mavlink-routerd` is not installed on a Debian/Raspberry Pi OS system, the
script can try to install it:

```sh
scripts/setup-mavlink-router.sh --install
```

Optional QGroundControl UDP output:

```sh
scripts/setup-mavlink-router.sh \
  --device /dev/serial0 \
  --baud 921600 \
  --qgc 192.168.1.20:14550
```

After setup, use the generated environment file before starting `mavsdk_server`
and `atlas-agent`:

```sh
source ~/.config/atlas-agent/mavlink-router/atlas-mavlink.env
mavsdk_server -p 50051 "$ATLAS_PX4_SYSTEM_ADDRESS"
go run ./cmd/atlas-agent
```

Or start the companion runtime stack with:

```sh
scripts/start-companion-agent.sh --backend-grpc 10.0.0.5:9090
```

This starts:

```text
mavlink-routerd -> mavsdk_server -> atlas-agent
```

It keeps the processes in the foreground, writes logs under
`~/.local/state/atlas-agent/logs`, and stops child processes on Ctrl-C. Use
`--dry-run` to inspect the exact commands before starting anything:

```sh
scripts/start-companion-agent.sh --dry-run --backend-grpc 10.0.0.5:9090
```

If you already run `mavlink-routerd` or `mavsdk_server` separately, skip those
parts:

```sh
scripts/start-companion-agent.sh --skip-router
scripts/start-companion-agent.sh --skip-router --skip-mavsdk
```

The script configures the Raspberry Pi side only. Pixhawk TELEM2 must still be
configured in PX4/QGroundControl with a matching MAVLink instance and baud rate.

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
ATLAS_MAVLINK_OBSERVER_ENDPOINT=udp-server://0.0.0.0:14550 \
go run ./cmd/atlas-agent
```
