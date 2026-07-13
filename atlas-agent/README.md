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
                    -> stream detections and health independently from vehicle commands
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

The payload controller is the sole owner of automatic mission view and temporary
operator override. A manual session claims primary MAVLink Gimbal v2 control and
must renew a short lease. Ending the session, losing the UI, or allowing the
lease to expire restores the gimbal and zoom for the mission's current waypoint;
it does not replay the view from the waypoint where manual control began.

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

## Package and install on Raspberry Pi 5

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
sudo atlas-setup
```

With no arguments, `atlas-setup` is interactive. It verifies Ubuntu, the Pi,
the camera, and Hailo; lists stable `/dev/serial/by-id` devices; and passively
listens for a checksum-valid MAVLink heartbeat before configuring TELEM2. It
then writes `/etc/atlas-agent/atlas-agent.env` and enables only:

```text
atlas-mavsdk.service -> atlas-agent.service
```

The Hailo adapter is supervised by `atlas-agent`; it is not a third service.
MediaMTX and MAVLink Router are not installed by this topology.

When deprecated units are still present under `/etc/systemd/system`, the
interactive wizard shows each one and asks permission to stop and archive it
under `/var/lib/atlas-agent/legacy-units`. This prevents an old locally written
unit from shadowing the packaged unit in `/usr/lib/systemd/system`.

Run the field diagnostic at any time:

```sh
sudo atlas-setup doctor
```

For fleet automation, the discovery defaults can be applied without prompts:

```sh
sudo atlas-setup install --non-interactive
```

`--dry-run` prints the generated configuration and system actions without
changing the computer.

### Ubuntu 24.04 and Hailo

Atlas verifies all of the following before enabling perception:

- the Hailo PCIe device;
- `hailortcli fw-control identify`;
- the `hailonet` and `hailofilter` GStreamer elements;
- the system Python `gi` and `hailo` bindings;
- a HEF matching the detected Hailo-8 or Hailo-8L accelerator;
- the configured TAPPAS postprocess library.

Raspberry Pi's turnkey `hailo-all` setup is published for Raspberry Pi OS.
Ubuntu 24.04 therefore needs a compatible HailoRT/TAPPAS package source or
preinstalled runtime. If that source exposes `hailo-all`, the wizard offers to
install it. Otherwise setup pauses with the missing prerequisites instead of
transplanting Raspberry Pi OS packages or patching vendor DKMS source. The rest
of Atlas can be installed without perception only after explicit confirmation.

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

Install the adapter on the onboard computer after HailoRT, TAPPAS Core,
PyGObject, and their Python bindings are available:

```sh
sudo install -m 0755 scripts/atlas-hailort-adapter.py /usr/local/bin/atlas-hailort-adapter
```

Then enable it alongside Atlas Agent:

```sh
ATLAS_PERCEPTION_PROVIDER=hailo \
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
