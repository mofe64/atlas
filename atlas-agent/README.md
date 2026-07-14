# Atlas Agent

Atlas Agent is the new Go 1.25 onboard runtime. It initiates and maintains the
direct HM30/local-network session to Atlas Native; it does not connect to the
Atlas backend.

The previous PX4, MAVSDK, video, and backend-stream prototype remains in
`../atlas-agent-deprecated` while those capabilities are ported deliberately.

## Current flow

```text
load or create stable installation + drone ids
    -> connect to Atlas Native
        -> send registration as the first gRPC message
            -> receive local agent/drone/binding/link ids
                -> send heartbeat every five seconds
                -> sample latest MAVSDK telemetry once per second
                -> forward PX4 status text as discrete events
                -> discover MAVSDK gimbals and MAVSDK/SIYI camera zoom
                -> run the supervised HailoRT/TAPPAS object-detection adapter
                    -> publish normalized frame metadata over a protected Unix socket
                    -> stream health continuously and detection frames on renewable demand
                -> execute idempotent Hold, RTL, Land, and payload commands
                -> upload and control MAVSDK missions
                    -> report upload progress, current item, completion, and errors
                    -> apply template gimbal/zoom intent with mission-scoped manual override
                    -> reconnect with bounded backoff on failure
```

The current transport contains registration, heartbeat, read-only flight
telemetry, and PX4 status events. Telemetry includes multi-battery power data,
preflight health, altitude and NED velocity, landed and RC state, home position,
and GPS quality. The command transport supports the deliberately small
contingency set of Hold, Return to Launch, and Land. During an active mission,
the payload controller supports gimbal pitch/yaw angles, angular rates, centre,
geographic ROI, and camera zoom. Gimbal yaw explicitly chooses
aircraft-relative yaw-follow or north-locked yaw. Every request uses the same
accepted/executing/result lifecycle and deadline as other vehicle commands.

The payload controller is the sole owner of physical gimbal/camera control. A
manual session declares exactly one context and renews a short lease:

- `inspection` is aircraft-owned, allowed only while connected with fresh,
  explicitly disarmed/on-ground telemetry, and releases Gimbal v2 primary
  control when it ends or expires.
- `mission_override` belongs to one running/paused mission and restores the
  gimbal and zoom for that mission's current waypoint when it ends or expires;
  it does not replay the view from the waypoint where manual control began.

Mission activation is rejected while an inspection session owns the payload,
so autonomous mission intent cannot race manual inspection movement.

Perception health is always forwarded. Detection frames are forwarded only
while at least one renewable Native consumer lease is active or the current
mission is running/paused. Consumers have independent leases, so one view
closing cannot stop frames required by another view or by a mission.

Camera zoom prefers MAVSDK Camera when a camera component is discovered and
falls back to the SIYI A8 Mini UDP SDK. The A8 Mini is treated as a fixed-focus
camera: Atlas does not advertise or issue autofocus/focus commands.

Mission operations accept an immutable Atlas plan, translate navigation to
MAVSDK Mission items, and support upload, start, pause, resume, cancel-to-hold,
and Return-to-Launch. Mission progress is streamed back to Atlas Native. Camera
and gimbal actions remain in the Agent payload plan instead of being embedded in
PX4 mission items, preventing automatic waypoint setpoints from racing a manual
override. RTL-after-completion is configured before upload. An initial Start
runs native preflight gates, commands MAVSDK Action Arm, then enters PX4 mission
mode; Resume only resumes a paused mission.
If mission start fails after arming, the agent commands HOLD and reports the
failure. The v1 perception actions are reported as translation warnings rather
than silently claimed as executed.

`mavsdk_server` runs beside Atlas Agent and owns the MAVLink connection to PX4.
Atlas Agent consumes its local gRPC API; it does not access the serial device
directly in this slice.

## Run

Against Atlas Native on the HM30 ground address:

```sh
ATLAS_GROUND_STATION_ADDR=192.168.144.50:7443 \
go run ./cmd/atlas-agent
```

Start MAVSDK first for a serial flight-controller connection:

```sh
mavsdk_server -p 50051 serial:///dev/serial0:921600
```

For PX4 SITL:

```sh
mavsdk_server -p 50051 udpin://0.0.0.0:14540
```

Local loopback:

```sh
ATLAS_GROUND_STATION_ADDR=127.0.0.1:7443 \
ATLAS_AGENT_STATE_DIR=/tmp/atlas-agent-dev \
go run ./cmd/atlas-agent
```

## Understanding the HM30 network addresses

The HM30 link connects the drone-side Ethernet network to the ground-side
Ethernet network over radio. Think of it as a long Ethernet cable between the
air unit and the ground unit: devices on either side can communicate because
they are all on the `192.168.144.x` network.

```text
Drone side                                                  Ground side

A8 camera       Raspberry Pi       HM30 Air       HM30 Ground       Ground computer
.25             .168               .11    ))) radio ((( .12         .50:7443
     \____________ Ethernet hub __________/              \________ Ethernet ________/
```

The complete address map is:

| Device | Address | What it is used for |
| --- | --- | --- |
| HM30 Air unit | `192.168.144.11` | Management address for the air radio |
| HM30 Ground unit | `192.168.144.12` | Management address for the ground radio |
| SIYI A8 camera | `192.168.144.25` | RTSP video and camera control |
| Raspberry Pi `eth0` | `192.168.144.168` | Atlas Agent and onboard Hailo access |
| Ground computer Ethernet | `192.168.144.50` | Atlas Native; gRPC listens on port `7443` |

`192.168.144.50` is **not** an HM30 address. It is an address we chose and
manually assigned to the ground computer's Ethernet adapter. It is editable,
but keeping it makes setup easier because Atlas Native and Atlas Agent both use
`192.168.144.50:7443` as their default.

The HM30 units use `.11` and `.12` for the radios themselves. Atlas does not
send agent messages to `.12`; the Pi connects through the radio link to Atlas
Native at `.50:7443`. Similarly, the ground computer can request the A8 RTSP
stream from `.25`, and the radio link carries that traffic to the drone side.
No port forwarding or internet connection is required.

All addresses must be unique and use the `255.255.255.0` subnet mask. Leave the
router/gateway field empty on the Pi's HM30 Ethernet connection and on the
ground computer's HM30 Ethernet connection. The Pi should continue to use
Wi-Fi for its internet/default route; the HM30 Ethernet connection is only for
local `192.168.144.x` traffic.

On a macOS ground computer, find the Ethernet device with:

```sh
networksetup -listallhardwareports
```

Then check its address, replacing `en7` with the device shown for the Ethernet
adapter:

```sh
ipconfig getifaddr en7
```

If the result is `192.168.144.50`, use the default ground-station address
`192.168.144.50:7443` during `atlas-setup`. If a different unused
`192.168.144.x` address is intentionally assigned to the ground computer, set
Atlas Native's `ATLAS_GROUND_STATION_LISTEN_ADDR` and Atlas Agent's
`ATLAS_GROUND_STATION_ADDR` to that same address with port `7443`.

See the
[A8, HM30, Pi, and ground-computer network guide](../../docs/atlas_a8_hm30_pi_video_setup.md)
for wiring, configuration, routing, and troubleshooting details.

## Package and install on Raspberry Pi 5

See [INSTALLATION.md](INSTALLATION.md) for the complete build, deprecated-stack
cleanup, Hailo migration, interactive setup, and verification procedure.

The supported onboard profile is Ubuntu 24.04 arm64 on Raspberry Pi 5 with a
Raspberry Pi AI HAT+ (Hailo-8L by default). Build the Debian package on a Linux
build machine with Go 1.25 and Debian packaging tools:

```sh
cd atlas-agent
ATLAS_RELEASE_VERSION=0.1.0 packaging/build-deb.sh
```

The package builder cross-compiles `atlas-agent` and `atlas-setup`, downloads
the pinned official MAVSDK arm64 binary, verifies its SHA-256, and packages the
metadata-only Hailo adapter and a checksum-pinned Hailo-8L HEF. To build for a
26 TOPS Hailo-8 AI HAT+, provide a compatible HEF and identify its target:

```sh
ATLAS_RELEASE_VERSION=0.1.0 \
ATLAS_HEF_MODEL_PATH=/path/to/objects-h8.hef \
ATLAS_MODEL_ACCELERATOR=hailo-8 \
packaging/build-deb.sh
```

Copy `dist/atlas-agent_<version>_arm64.deb` to the onboard computer, then run:

```sh
sudo apt install ./atlas-agent_<version>_arm64.deb
sudo atlas-hailo-setup  # clean Ubuntu host only
# Reboot here if atlas-hailo-setup exits with status 3.
sudo atlas-setup
```

`atlas-hailo-setup` is needed on a clean Ubuntu installation. If the computer
already has a working Hailo installation from the deprecated Atlas Agent, do
not replace it automatically: skip `atlas-hailo-setup` and run the interactive
`sudo atlas-setup`. Its discovery checks can select the existing native runtime;
after configuration, confirm it with `sudo atlas-setup doctor`. Use
`atlas-hailo-setup --replace-existing` only when deliberately migrating to the
pinned container profile. That migration removes host HailoRT/TAPPAS packages,
but retains/reinstalls the pinned host driver and firmware; changing a loaded
PCIe driver requires a reboot.

With no arguments, `atlas-setup` is interactive. It verifies Ubuntu, the Pi,
the camera, and Hailo; lists stable `/dev/serial/by-id` devices; and passively
listens for a checksum-valid MAVLink heartbeat before configuring TELEM2. It
then writes `/etc/atlas-agent/atlas-agent.env` and enables:

```text
atlas-mavsdk.service -> atlas-agent.service
                            |
                            +-> atlas-hailo-adapter.service (container mode only)
```

In native/process mode the Hailo adapter remains supervised by `atlas-agent`.
In container mode systemd supervises `atlas-hailo-adapter.service`, and the
agent only owns the perception socket. MediaMTX and MAVLink Router are not
installed by this topology.

When deprecated units are still present under `/etc/systemd/system`, the
interactive wizard shows each one and asks permission to stop and archive it
under `/var/lib/atlas-agent/legacy-units`. This prevents an old locally written
unit from shadowing the packaged unit in `/usr/lib/systemd/system`.

Run the field diagnostic at any time:

```sh
sudo atlas-setup doctor
sudo atlas-hailo-setup status
journalctl -u atlas-hailo-adapter.service -f
```

For fleet automation, the discovery defaults can be applied without prompts:

```sh
sudo atlas-setup install --non-interactive
```

`--dry-run` prints the generated configuration and system actions without
changing the computer.

### Ubuntu 24.04 and Hailo

`atlas-hailo-setup` installs one pinned compatibility profile:

- Hailo PCIe driver and firmware `4.20.0` on the Ubuntu host;
- HailoRT `4.20.0-1` and TAPPAS Core `3.31.0+1-1` in a digest-pinned Debian
  arm64 container;
- a narrow, deterministic DKMS source patch needed by Ubuntu's Raspberry Pi
  kernel warning policy;
- an immutable local container image ID recorded in
  `/etc/atlas-agent/hailo-container.env`.

The profile uses checksum-pinned packages from Raspberry Pi's public archive.
Keeping kernel-facing driver/firmware on the host and Hailo userspace in the
container prevents Atlas's Python and GStreamer dependencies from becoming
host-global dependencies. Only the Hailo adapter is containerized. It receives
host networking for the A8 RTSP stream, `/dev/hailo0`, the Atlas runtime socket,
and the packaged model directory. It runs as the host `atlas-agent` UID plus the
Hailo device group, with a read-only root filesystem and all Linux capabilities
dropped.

`atlas-setup doctor` verifies all of the following before container perception
is considered healthy:

- installed and loaded host driver version;
- installed host firmware package and live device firmware version;
- `/dev/hailo0` availability inside the container;
- exact HailoRT/TAPPAS userspace compatibility with the host profile;
- `hailonet` and `hailofilter` GStreamer elements and Python `gi`/`hailo`
  bindings inside the container;
- successful HEF parsing and a Hailo-8 versus Hailo-8L accelerator match.

The container has host networking because it must reach the SIYI A8 network;
this is intentionally broader than a dedicated container bridge. It does not
mount the Docker socket or the rest of the host filesystem.

The default package contains a Hailo-8L model. Setup refuses to enable it on a
detected Hailo-8 accelerator; build the package with the matching HEF instead.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `ATLAS_AGENT_STATE_DIR` | OS config directory under `atlas-agent` | Stable local installation identity |
| `ATLAS_GROUND_STATION_ADDR` | `192.168.144.50:7443` | Native app gRPC listener |
| `ATLAS_DRONE_NAME` | `Atlas Drone` | Local display name |
| `ATLAS_AGENT_VERSION` | `0.1.0-dev` | Reported agent version |
| `ATLAS_FLIGHT_CONTROLLER_UID` | Empty | Observed controller identity |
| `ATLAS_FLIGHT_CONTROLLER_TRANSPORT` | `serial` | Agent-to-controller attachment |
| `ATLAS_FLIGHT_CONTROLLER_ENDPOINT` | `/dev/serial0` | Attachment endpoint description |
| `ATLAS_FLIGHT_CONTROLLER_BAUD_RATE` | `921600` | Serial baud rate |
| `ATLAS_MAVLINK_SYSTEM_ID` | `1` | Expected MAVLink system ID |
| `ATLAS_MAVLINK_COMPONENT_ID` | `1` | Expected MAVLink component ID |
| `ATLAS_MAVSDK_GRPC_ADDR` | `127.0.0.1:50051` | Local `mavsdk_server` gRPC address |
| `ATLAS_SIYI_CAMERA_ADDR` | `192.168.144.25:37260` | SIYI A8 Mini UDP SDK endpoint used for zoom discovery/fallback |
| `ATLAS_TELEMETRY_INTERVAL` | `1s` | Latest-snapshot publish interval (minimum `100ms`) |
| `ATLAS_PERCEPTION_PROVIDER` | `disabled` | Neutral runtime selector: `external`, `hailo`, `deepstream`, `tensorrt`, or `onnx` |
| `ATLAS_PERCEPTION_ADAPTER_MODE` | `process` | `process` lets `atlas-agent` launch the adapter; `container` delegates it to the Hailo systemd unit |
| `ATLAS_PERCEPTION_SOCKET_PATH` | Agent state directory under `perception/runtime.sock` | Protected Unix socket where the selected runtime publishes Atlas perception envelopes |
| `ATLAS_PERCEPTION_ADAPTER_PATH` | `atlas-hailort-adapter` | Hailo adapter executable supervised when the provider is `hailo` |
| `ATLAS_A8_RTSP_URL` | `rtsp://192.168.144.25:8554/main.264` | Clean A8 stream consumed by Hailo inference |
| `ATLAS_A8_RTP_CODEC` | `auto` | `auto`, `h264`, or `h265` depay/decode selection |
| `ATLAS_A8_RTSP_TRANSPORT` | `tcp` | RTSP transport used by the Hailo pipeline |
| `ATLAS_A8_RTSP_LATENCY_MS` | `75` | Hailo pipeline jitter-buffer latency |
| `ATLAS_PERCEPTION_MODEL_PATH` | Required for Hailo | Hailo HEF object-detection model |
| `ATLAS_PERCEPTION_MODEL_NAME` | HEF filename | Stable model identity sent to Native |
| `ATLAS_PERCEPTION_MODEL_VERSION` | `1` | Model version sent with every detection frame |
| `ATLAS_PERCEPTION_POSTPROCESS_SO` | TAPPAS YOLO postprocess library | Library that converts model tensors into Hailo detection metadata |
| `ATLAS_PERCEPTION_POSTPROCESS_FUNCTION` | `filter` | Postprocess entry point |
| `ATLAS_PERCEPTION_POSTPROCESS_CONFIG` | Empty | Optional postprocess labels/configuration file |
| `ATLAS_PERCEPTION_WIDTH` / `ATLAS_PERCEPTION_HEIGHT` | `640` / `640` | Hailo inference input size |
| `ATLAS_VIDEO_SOURCE_ID` | `a8-main` | Identity shared with the native decoder for overlay matching |
| `ATLAS_HAILO_ACCELERATOR` | `hailo-8l` | Accelerator identity reported in health |

Perception providers are separate runtime processes. They translate native
runtime output into the versioned Atlas contract before connecting to the Unix
socket. Live frames use a latest-value buffer, so stale detections are discarded
instead of delaying current state. The current concrete adapter is HailoRT via
the Hailo GStreamer/TAPPAS elements; Jetson adapters remain future work.

For native development, install the adapter on a computer where HailoRT, TAPPAS
Core, PyGObject, and their Python bindings are already available:

```sh
sudo install -m 0755 scripts/atlas-hailort-adapter.py /usr/local/bin/atlas-hailort-adapter
```

Then enable it alongside Atlas Agent:

```sh
ATLAS_PERCEPTION_PROVIDER=hailo \
ATLAS_PERCEPTION_ADAPTER_MODE=process \
ATLAS_PERCEPTION_MODEL_PATH=/opt/atlas/models/objects.hef \
go run ./cmd/atlas-agent
```

The Hailo pipeline contains no `hailooverlay` element. It consumes the camera
for inference and publishes only structured detections; Native independently
decodes the clean RTSP stream and decides whether to render those detections.

## Generate protobuf code

From the repository root:

```sh
scripts/generate-ground-station-proto-go.sh
scripts/generate-mavsdk-go.sh
```

Rust protobuf code is generated by the Tauri crate's `build.rs`.

## Validate

```sh
go test ./...
go vet ./...
python3 -m unittest discover -s scripts -p '*_test.py'
```
