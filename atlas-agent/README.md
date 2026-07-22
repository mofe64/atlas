# Atlas Agent

Atlas Agent is the supported Go 1.25 onboard runtime. It initiates and maintains
the direct HM30/local-network session to Atlas Native; it does not connect to
the Atlas Backend.

For the full system mental model and code-linked onboarding path, start with
[`../docs/README.md`](../docs/README.md). Agent internals are documented in
[`../docs/atlas-agent.md`](../docs/atlas-agent.md), and the shared transport is
documented in
[`../docs/native-agent-protocol.md`](../docs/native-agent-protocol.md).

## Current flow

```text
load or create stable installation + drone ids
    -> connect to Atlas Native
        -> send registration as the first gRPC message
            -> receive local agent/drone/binding/link ids
                -> send heartbeat every five seconds
                -> sample latest MAVSDK telemetry once per second
                -> forward PX4 status text as discrete events
                -> discover MAVSDK gimbals and the configured camera transport
                -> run the supervised HailoRT/TAPPAS object-detection adapter
                    -> remain READY with inference inactive until an explicit claim
                    -> publish normalized frame metadata over a protected Unix socket
                    -> stream health continuously and detection frames on renewable demand
                -> supervise an independent, optional spatial-camera container
                    -> normalize provider RGB-D to the logical front-depth contract
                    -> expose synchronized-frame and calibration health locally
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

Perception health is always forwarded, including the intentional `INACTIVE`
state. The Hailo pipeline starts in `READY` and enters `PLAYING` only while an
explicit claim exists. Live views use renewable 3–30 second claims; missions
use durable run-scoped claims created by acknowledged `START_PERCEPTION` and
released by `STOP_PERCEPTION` or terminal cleanup. Claims are reference-counted,
so one view closing cannot stop inference required by another view or mission.

Every normalized detection frame then crosses an Atlas-owned tracking stage.
Provider `trackId` values are preserved only as upstream provenance; Native sees
an ID only when an Atlas tracker backend assigns it. The foundation stage owns
session IDs, resets on source/model/stream/timestamp discontinuities, exposes
tracking health, and degrades to untracked detections on backend failure.
`byte_track` is the production default and selects the original MIT-licensed
FoundationVision ByteTrack C++
deployment core, pinned at commit
`d1bf0191adff59bc8fcfeaa0b33d3d1642552a99`. Agent supervises it as a bounded
worker process and still owns continuity resets and operator-visible IDs.
ByteTrack is class-isolated and has ReID disabled. `byte_track_cmc` uses the
same worker and association algorithm, but warps the predicted Kalman state
with Atlas's confidence-gated sparse-optical-flow camera transform before IoU
matching. Missing or low-confidence motion falls back to identity and is
reported as degraded CMC health without stopping detections.

Agent also owns the bounded
`TENTATIVE -> ACTIVE -> TEMPORARILY_OCCLUDED -> LOST -> CLOSED` lifecycle,
high-frequency active history, decaying image-space prediction, current-visible
and session-unique totals, and source-specific line/polygon rule evaluation.
Counts are produced only by confirmed observations and reset with the tracker
session. Native supplies the revisioned rule set and persists only state
changes, periodic/significant samples, count events, and operator actions; it
does not persist every box.

Camera zoom uses the explicit `ATLAS_CAMERA_TRANSPORT` policy. The default
`siyi_udp` mode communicates only through the SIYI A8 Mini UDP SDK and never
activates MAVSDK Camera discovery. `mavsdk` selects a MAVLink camera, while
`hybrid` deliberately enables both and permits SIYI fallback. The A8 Mini is
treated as a fixed-focus camera: Atlas does not advertise or issue
autofocus/focus commands.

Mission operations accept an immutable Atlas plan, translate navigation to
MAVSDK Mission items, and support upload, start, pause, resume, cancel-to-hold,
and Return-to-Launch. Incident-response plans retain a separate acknowledged
arrival-action chain. Hold at Staging and one-point Offset Observe trigger at
their final waypoint. Bounded Area Scan and Bounded Orbit trigger after generated
waypoint zero, acknowledge arrival, and then Resume through the remaining
pattern. Agent executes `HOLD_AT_ARRIVAL` first, optional
`POINT_GIMBAL_AT_INCIDENT` second, and `RESUME_AFTER_ARRIVAL` last when the
reviewed pattern continues. It reports every
running/retrying/succeeded/failed/policy-applied transition to Native. Exhausted
retries use the reviewed plan policy: request Return to Launch or preserve the
run for operator intervention. Staging is the special operator-decision mode:
successful Hold pauses the run and waits for explicit Resume, RTL, or Cancel.

Mission progress is streamed back to Atlas Native. Camera and gimbal actions
remain in the Agent payload plan instead of being embedded in PX4 mission
items, preventing automatic waypoint setpoints from racing a manual override.
RTL-after-completion is configured before upload. An initial Start runs native
preflight gates, commands MAVSDK Action Arm, then enters PX4 mission mode;
Resume only resumes a paused mission.
Required mission perception is acknowledged from a fresh post-activation frame
before Agent arms. If perception cannot start, the mission remains unarmed and
the durable action applies its reviewed operator-intervention policy. If mission
start fails after arming, the agent commands HOLD and releases the mission's
perception claim. Stop failure is reported but cannot block cancellation, RTL,
or normal terminal cleanup.

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

## Package and install on Raspberry Pi 5

See [INSTALLATION.md](INSTALLATION.md) for the complete build, clean-install,
upgrade, same-version replacement, rollback, and verification procedures.

The supported onboard profile is Ubuntu 24.04 arm64 on Raspberry Pi 5 with a
Raspberry Pi AI HAT+ (Hailo-8L by default). Build the Debian package on a Linux
build machine with Go 1.25 and Debian packaging tools:

```sh
cd atlas-agent
ATLAS_RELEASE_VERSION=0.1.8 packaging/build-deb.sh
```

The package builder cross-compiles `atlas-agent` and `atlas-setup`, downloads
the pinned official MAVSDK arm64 binary, verifies its SHA-256, and packages the
metadata-only Hailo adapter and a checksum-pinned Hailo-8L HEF. To build for a
26 TOPS Hailo-8 AI HAT+, provide a compatible HEF and identify its target:

```sh
ATLAS_RELEASE_VERSION=0.1.8 \
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
already has a compatible working Hailo installation, do not replace it
automatically: skip `atlas-hailo-setup` and run the interactive
`sudo atlas-setup`. Its discovery checks can select the native runtime; after
configuration, confirm it with `sudo atlas-setup doctor`. Use
`atlas-hailo-setup --replace-existing` only when deliberately moving to the
pinned container profile. That operation removes host HailoRT/TAPPAS userspace
packages, but retains/reinstalls the pinned host driver and firmware; changing
a loaded PCIe driver requires a reboot.

With no arguments, `atlas-setup` is interactive. It verifies Ubuntu, the Pi,
the A8 camera, Hailo, and an optional USB depth camera; lists stable `/dev/serial/by-id` devices; and passively
listens for a checksum-valid MAVLink heartbeat before configuring TELEM2. It
then writes `/etc/atlas-agent/atlas-agent.env`, writes the independent
`/etc/atlas-agent/spatial.env` contract, and enables:

```text
atlas-mavsdk.service -> atlas-agent.service
                            |
                            +-> atlas-hailo-adapter.service (container mode only)

atlas-spatial-runtime.service (optional; independent of flight services)
```

In native/process mode the Hailo adapter remains supervised by `atlas-agent`.
In container mode systemd supervises `atlas-hailo-adapter.service`, and the
Agent owns the protected perception socket. Native decodes the A8 RTSP stream
directly, while the Agent talks to the local `mavsdk_server` gRPC endpoint.

The installed H-Flow is configured through QGroundControl and fused by PX4; it
is not owned by the spatial camera container. `atlas-setup doctor` does not yet
validate H-Flow firmware, PX4 parameters, flow/range quality, or EKF fusion.
Accepted OAK identifiers and the remaining H-Flow/endurance evidence are listed
in
[`docs/indoor-navigation-commissioning.md`](../docs/indoor-navigation-commissioning.md).

Run the field diagnostic at any time:

```sh
sudo atlas-setup doctor
sudo atlas-hailo-setup status
journalctl -u atlas-hailo-adapter.service -f
journalctl -u atlas-spatial-runtime.service -f
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
| `ATLAS_CAMERA_TRANSPORT` | `siyi_udp` | Camera control policy: `siyi_udp`, `mavsdk`, or explicitly dual `hybrid` |
| `ATLAS_SIYI_CAMERA_ADDR` | `192.168.144.25:37260` | SIYI A8 Mini UDP SDK endpoint used when the selected transport includes SIYI |
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
| `ATLAS_TRACKER_ALGORITHM` | `byte_track` | Atlas tracker: production-default `byte_track`, aerial candidate `byte_track_cmc`, or explicit `disabled` |
| `ATLAS_TRACKER_MAX_TIMESTAMP_GAP` | `2s` | Discontinuity gap that resets the tracker (`100ms`–`30s`) |
| `ATLAS_TRACKER_CMC_MIN_CONFIDENCE` | `0.25` | Minimum camera-motion estimate confidence applied by `byte_track_cmc` |
| `ATLAS_TRACKER_CMC_MAX_DIMENSION` / `ATLAS_TRACKER_CMC_MAX_FEATURES` | `320` / `300` | Bounded sparse-optical-flow workload in the Hailo adapter |
| `ATLAS_TRACKER_CONFIRMATION_OBSERVATIONS` | `2` | Atlas observations required to promote a lifecycle from `TENTATIVE` to `ACTIVE` (`1`–`10`) |
| `ATLAS_TRACKER_PREDICTION_HORIZON` | `750ms` | Maximum bounded image-space prediction interval (`100ms`–`10s`) |
| `ATLAS_TRACKER_LOST_AFTER` / `ATLAS_TRACKER_CLOSE_AFTER` | `1s` / `3s` | Time since the last confirmed observation before `LOST`, then terminal `CLOSED` |
| `ATLAS_TRACKER_LIFECYCLE_SNAPSHOT_INTERVAL` | `1s` | Periodic durable-summary cadence; state transitions are always sent immediately |
| `ATLAS_TRACKER_HISTORY_OBSERVATIONS` | `60` | Maximum high-frequency observations retained onboard per active track (`2`–`600`) |
| `ATLAS_GEOLOCATION_BORESIGHT_ANGULAR_UNCERTAINTY_DEG` | `10` | Static camera/gimbal boresight angular error bound included in every coordinate (`0.1`–`44.9` degrees) |
| `ATLAS_GEOLOCATION_BORESIGHT_ALIGNMENT_REFERENCE` | empty | Commissioning artifact or record that physically justifies the configured bound; empty evidence is explicitly `UNVERIFIED` |
| `ATLAS_AIRCRAFT_FOLLOW_ENABLED` | `false` | Enables the PX4 Offboard Follow-from-standoff controller only after commissioning evidence is configured |
| `ATLAS_AIRCRAFT_FOLLOW_VALIDATION_REFERENCE` | empty | Accepted simulation/HIL/controlled-flight record for this installation; required when aircraft follow is enabled |
| `ATLAS_BYTETRACK_WORKER_PATH` | `atlas-bytetrack-worker` | Original FoundationVision ByteTrack worker executable; packaged installs use `/usr/libexec/atlas-agent/atlas-bytetrack-worker` |
| `ATLAS_BYTETRACK_REQUEST_TIMEOUT` | `250ms` | Per-frame worker deadline (`10ms`–`5s`); timeout degrades to untracked detections and resets the association session |
| `ATLAS_BYTETRACK_FRAME_RATE` / `ATLAS_BYTETRACK_BUFFER_FRAMES` | `30` / `30` | Upstream frame-rate scaling and lost-track buffer |
| `ATLAS_BYTETRACK_TRACK_THRESHOLD` / `ATLAS_BYTETRACK_HIGH_THRESHOLD` | `0.50` / `0.60` | Upstream second-stage and new-track score thresholds |
| `ATLAS_BYTETRACK_MATCH_THRESHOLD` | `0.80` | Upstream first-stage IoU assignment cost threshold |
| `ATLAS_GIMBAL_FOLLOW_UPDATE_INTERVAL` / `ATLAS_GIMBAL_FOLLOW_TRACK_FRESHNESS` | `100ms` / `350ms` | Onboard controller cadence and maximum confirmed-observation age before holding |
| `ATLAS_GIMBAL_FOLLOW_HOLD_TIMEOUT` | `2s` | Maximum continuous temporary-loss hold before the follow session stops |
| `ATLAS_GIMBAL_FOLLOW_DEADBAND` | `0.025` | Normalized image-centre error ignored to prevent gimbal hunting |
| `ATLAS_GIMBAL_FOLLOW_PITCH_GAIN` / `ATLAS_GIMBAL_FOLLOW_YAW_GAIN` | `60` / `80` | Image error to angular-rate proportional gains |
| `ATLAS_GIMBAL_FOLLOW_MAX_PITCH_RATE` / `ATLAS_GIMBAL_FOLLOW_MAX_YAW_RATE` | `20` / `30` | Maximum commanded rates in degrees per second |
| `ATLAS_GIMBAL_FOLLOW_MAX_PITCH_ACCELERATION` / `ATLAS_GIMBAL_FOLLOW_MAX_YAW_ACCELERATION` | `60` / `90` | Maximum ordinary rate change in degrees per second squared |
| `ATLAS_GIMBAL_FOLLOW_MIN_PITCH` / `ATLAS_GIMBAL_FOLLOW_MAX_PITCH` | `-90` / `30` | Installation-calibrated physical pitch envelope |
| `ATLAS_GIMBAL_FOLLOW_MIN_YAW` / `ATLAS_GIMBAL_FOLLOW_MAX_YAW` | `-180` / `180` | Installation-calibrated aircraft-relative yaw envelope |
| `ATLAS_GIMBAL_FOLLOW_LIMIT_MARGIN` | `2` | Angular margin reserved inside each physical stop |
| `ATLAS_VIDEO_SOURCE_ID` | `a8-main` | Identity shared with the native decoder for overlay matching |
| `ATLAS_HAILO_ACCELERATOR` | `hailo-8l` | Accelerator identity reported in health |

The calibration-free geolocation foundation uses a centred-boresight method.
The caller declares whether the centred point is ground contact or target
centre; target centre also requires the assumed height of that aim point above
ground and its uncertainty. Agent combines the frame-time-correlated aircraft
pose with measured gimbal attitude
relative to North, produces world-NED and aircraft-body-FRD directions, and
intersects the ray with a caller-supplied horizontal plane.

The default safety envelope requires the declared point to be within 0.04 of
image centre on each normalized axis, frame-time uncertainty no greater than
500 ms, at least 20 degrees of depression, and no more than 3 km ground range.
The reported error radius includes aircraft horizontal/vertical quality,
timing and velocity, ground/aim-point-height uncertainty, a conservative
10-degree static boresight bound, measured gimbal motion during the timing
window, and a 1-metre camera-to-navigation-origin allowance. The method rejects off-centre
points, or an uncertainty cone that reaches the horizon, rather than guessing
lens geometry, and it records the flat-plane and camera/gimbal alignment
assumptions in every result.

The 10-degree value is an uncalibrated operational bound, not a measured camera
accuracy claim, and must be confirmed or increased during payload acceptance.

Follow from standoff is installed fail-closed. By default the Agent advertises
`aircraft_follow:standoff:v1:unverified` and refuses translation. Setting
`ATLAS_AIRCRAFT_FOLLOW_ENABLED=true` is valid only when both the aircraft-follow
validation reference and physical boresight-alignment reference are non-empty.
That configuration records the evidence identity; it is not a substitute for
performing the referenced simulation, HIL, and controlled-flight acceptance.

Native exposes this estimator only for the exact current operator selection.
`geolocate_selected_track` carries `selectionId`, `sourceId`,
`trackSessionId`, and `trackId` together with automatically resolved ground
altitude, uncertainty, source/version, and the MVP target-centre-height
assumption. Native first uses its configured DEM at the aircraft position and
may fall back to an explicitly labelled autopilot home-altitude plane. Agent uses
the selected track's retained pre-inference timing anchor and returns either a
schema-versioned coordinate estimate or a stable rejection code plus reason.
The command is observational: it does not acquire the payload lease or move the
gimbal. Native schema 21 persists both successful coordinates and rejected
attempts against the selection and session-scoped track.

Perception providers are separate runtime processes. They translate native
runtime output into the versioned Atlas contract before connecting to the Unix
socket. Live frames use a latest-value buffer, so stale detections are discarded
instead of delaying current state. The current concrete adapter is HailoRT via
the Hailo GStreamer/TAPPAS elements; Jetson adapters remain future work.
When GStreamer 1.22 or newer and the RTSP sender provide a reconstructable
absolute sender clock, the Hailo adapter attaches the RTCP/NTP capture time to
the frame. Older runtimes or cameras without usable sender timing continue with
the conservative pre-inference pipeline-ingress estimate. Atlas rejects
implausibly old or future sender timestamps instead of treating clock presence
as proof of synchronization.

For native development, install the adapter on a computer where HailoRT, TAPPAS
Core, PyGObject, and their Python bindings are already available:

```sh
sudo install -m 0755 scripts/atlas-hailort-adapter.py /usr/local/bin/atlas-hailort-adapter
```

Build the FoundationVision ByteTrack worker on the target architecture
with CMake and Eigen 3 development headers:

```sh
./scripts/build-bytetrack-worker.sh /tmp/atlas-bytetrack-worker
ATLAS_TRACKER_ALGORITHM=byte_track \
ATLAS_BYTETRACK_WORKER_PATH=/tmp/atlas-bytetrack-worker \
go run ./cmd/atlas-agent
```

Manual worker construction is only needed for source-based development. The
Debian package builder automatically finds or creates the Linux-arm64 worker
and validates its executable format before packaging it.

To exercise both ByteTrack modes against ignored local recordings in
`sampleVids/`, generate detector stimuli and replay them through the exact
worker-backed tracking stage:

```sh
python3 scripts/atlas-sample-video-detections.py \
  ../sampleVids/33039-395456502.mp4 \
  --output /tmp/atlas-sample-detections.ndjson \
  --stride 5 --max-frames 100 --max-width 1280
go run ./cmd/atlas-tracker-replay \
  -algorithm byte_track_cmc \
  -worker-path /tmp/atlas-bytetrack-worker \
  -input /tmp/atlas-sample-detections.ndjson \
  -output /tmp/atlas-sample-tracks.ndjson
```

This harness uses OpenCV HOG detections so it validates replay, camera-motion,
association, and reset behavior only. It is not Hailo/YOLO accuracy evidence.

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
