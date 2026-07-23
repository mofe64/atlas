# Atlas Agent Installation Guide

This guide covers both a clean installation and an upgrade of the packaged
Atlas Agent with systemd-managed MAVSDK, Hailo perception, and optional spatial
camera services.

## Supported onboard computer

- Raspberry Pi 5 running Ubuntu 24.04 arm64
- Raspberry Pi AI HAT+ with Hailo-8L
- SIYI A8 camera reachable over its onboard Ethernet network
- PX4 flight controller connected through a stable serial device
- Optional DepthAI USB depth camera connected directly to a Pi USB 3 port with
  a USB 3 cable. The validated OAK-D Lite installation does not require an
  external powered hub.
- Optional downward Holybro H-Flow connected through DroneCAN to PX4. Atlas
  setup does not configure or validate H-Flow; configure it for the installed
  PX4 release through QGroundControl and retain the separate firmware,
  parameter, estimator, and flight-acceptance evidence.

The commands use `0.1.17`, the prepared bounded-live-cloud release.
The currently accepted aircraft foundation remains `0.1.16` as of 22 July
2026. Retain that package, image identity, and transform backup until `0.1.17`
passes the validation section; source implementation is not aircraft acceptance.

## Camera transport contract

The supported A8 installation uses `ATLAS_CAMERA_TRANSPORT=siyi_udp`. In this
mode the Agent does not open MAVSDK Camera subscriptions, so `mavsdk_server`
continues to provide flight, mission, action, and gimbal services without
probing PX4 as though it were a camera. Use `mavsdk` only for a MAVLink camera,
or `hybrid` when both transports are intentionally installed.

An older configuration that does not contain `ATLAS_CAMERA_TRANSPORT` safely
defaults to `siyi_udp`. Re-running `sudo atlas-setup` writes the choice
explicitly into `/etc/atlas-agent/atlas-agent.env`.

## Initial installation overview

An initial installation has five stages:

1. Build and transfer the new Debian package.
2. Install the package on the onboard computer.
3. Configure the pinned Hailo profile or deliberately retain a compatible
   native Hailo runtime.
4. Run the interactive Atlas configuration.
5. Validate the complete installation.

## 1. Build the Atlas package

MAVSDK is pinned as one release contract in `packaging/mavsdk.env`: the
official server version, server asset checksum, and protobuf submodule commit
must move together. The build stops before producing a package when the
checked-out protobuf source or generated Go client does not match that pin.

Run these commands on a Linux development or build computer:

```sh
cd /path/to/sunnyside/atlas/atlas-agent

export ATLAS_RELEASE_VERSION=0.1.17
sudo apt install cmake libeigen3-dev g++
./packaging/build-deb.sh
```

The same build is supported on the Atlas development Mac used for current
releases. Install Go 1.25 (or enable Go's matching toolchain download), CMake,
`dpkg-deb`, GNU `sha256sum`, and Docker Desktop with Buildx. Before relying on
the Mac path, verify the required tools and that Docker is running:

```sh
for command in go git dpkg-deb curl file cmake sha256sum docker; do
  command -v "${command}"
done
docker info >/dev/null
docker buildx version
```

On a clean checkout, Buildx creates the Linux-arm64 ByteTrack worker. Retain
`dist/atlas-bytetrack-worker-linux-arm64` between releases to reuse the
validated worker and avoid rebuilding it unnecessarily. Never substitute a
macOS executable for that Linux-arm64 artifact.

The package build automatically finds a cached Linux-arm64 worker under
`dist/`, builds it natively on Linux arm64, or cross-builds it when
`aarch64-linux-gnu-g++` is installed. On macOS and other hosts it can also use
Docker Buildx with the Linux-arm64 worker build container. `ATLAS_BYTETRACK_WORKER_BIN`
remains an optional CI override, not a required input. Every discovered or
generated worker is checked for both the Linux-arm64 executable format and the
Atlas worker identity before it is placed in the package.

On an x86-64 Linux build host, install `g++-aarch64-linux-gnu` in addition to
CMake and the Eigen 3 headers. On the target Raspberry Pi or another Linux
arm64 host, the normal `g++` package is sufficient.

The package preserves FoundationVision's MIT license under
`/usr/share/doc/atlas-agent/third-party/bytetrack/`. Do not place a macOS or
x86-64 worker in an arm64 Debian package.

The build produces:

```text
dist/atlas-agent_0.1.17_arm64.deb
dist/atlas-agent_0.1.17_arm64.deb.sha256
```

For `0.1.17`, also build and archive the exact Linux-arm64 spatial image so the
Pi does not rebuild it from the network:

```sh
cd /path/to/sunnyside/atlas
export ATLAS_RELEASE_VERSION=0.1.17

docker buildx build \
  --platform linux/arm64 \
  --file atlas-spatial-runtime/packaging/Dockerfile \
  --build-arg "ATLAS_SPATIAL_VERSION=${ATLAS_RELEASE_VERSION}" \
  --tag "atlas-spatial-runtime:${ATLAS_RELEASE_VERSION}" \
  --load \
  atlas-spatial-runtime

docker run --rm --entrypoint /bin/bash \
  "atlas-spatial-runtime:${ATLAS_RELEASE_VERSION}" -lc '
    dpkg-query -W ros-jazzy-depthai-v3 ros-jazzy-depthai-ros-driver-v3
    strings /opt/ros/jazzy/lib/*/libdepthai_v3-core.so | grep -F ATLAS_DEPTHAI_VIO_IMU_ORDER
    strings /opt/ros/jazzy/lib/*/libdepthai_v3-core.so | grep -F ATLAS_DEPTHAI_VIO_IMAGE_BACKPRESSURE
    grep -F "i_fps: 20.0" /workspace/src/atlas_spatial_runtime/config/depthai_vio.yaml
    test -f /workspace/src/atlas_spatial_runtime/atlas_spatial_runtime/live_cloud.py
    test -f /workspace/src/atlas_spatial_runtime/atlas_spatial_runtime/live_cloud_node.py
    python3 -m pytest -q /workspace/src/atlas_spatial_runtime/test
  '

docker save "atlas-spatial-runtime:${ATLAS_RELEASE_VERSION}" \
  --output "atlas-agent/dist/atlas-spatial-runtime_${ATLAS_RELEASE_VERSION}_arm64.tar"
gzip -n -f "atlas-agent/dist/atlas-spatial-runtime_${ATLAS_RELEASE_VERSION}_arm64.tar"
sha256sum "atlas-agent/dist/atlas-spatial-runtime_${ATLAS_RELEASE_VERSION}_arm64.tar.gz" \
  > "atlas-agent/dist/atlas-spatial-runtime_${ATLAS_RELEASE_VERSION}_arm64.tar.gz.sha256"

PACKAGE_CHECK_DIR="$(mktemp -d)"
dpkg-deb -x \
  "atlas-agent/dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb" \
  "$PACKAGE_CHECK_DIR"
sha256sum "$PACKAGE_CHECK_DIR/usr/bin/atlas-agent" | awk '{print $1}' \
  > "atlas-agent/dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.binary.sha256"
```

Record the Debian, packaged Agent binary, runtime archive, and loaded image IDs
from these outputs. Do not reuse the accepted `0.1.16` identities for the new
release.

Transfer both release artifacts and their identity files to the onboard
computer:

```sh
cd /path/to/sunnyside/atlas/atlas-agent
export ATLAS_RELEASE_VERSION=0.1.17
export ATLAS_PI_HOST=<pi-user>@<pi-address>

scp \
  "dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb" \
  "dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb.sha256" \
  "dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.binary.sha256" \
  "dist/atlas-spatial-runtime_${ATLAS_RELEASE_VERSION}_arm64.tar.gz" \
  "dist/atlas-spatial-runtime_${ATLAS_RELEASE_VERSION}_arm64.tar.gz.sha256" \
  "${ATLAS_PI_HOST}:/tmp/"
```

Skip this section when the release package is already available on the onboard
computer.

## 2. Install the packaged Atlas Agent

On the onboard computer, verify and install the transferred package:

```sh
cd /tmp

export ATLAS_RELEASE_VERSION=0.1.17
sha256sum -c "atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb.sha256"
sha256sum -c "atlas-spatial-runtime_${ATLAS_RELEASE_VERSION}_arm64.tar.gz.sha256"
sudo docker load \
  --input "atlas-spatial-runtime_${ATLAS_RELEASE_VERSION}_arm64.tar.gz"
sudo apt install "./atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb"
```

Load and install only while the aircraft is landed, disarmed, and not
executing a mission. `docker load` must report the `0.1.17` tag; setup later
resolves it to an immutable `sha256:` image ID.

The package installs `atlas-agent`, `atlas-setup`, `atlas-hailo-setup`,
`atlas-spatial-setup`, the Hailo and spatial container build contexts, MAVSDK,
the compatible HEF model, USB permissions, host diagnostics, and the systemd
unit files. It does not enable the services until `atlas-setup` has written a
valid configuration.

## 3. Configure Hailo

### Recommended: pinned container profile

On a clean Ubuntu host, install the pinned Atlas profile with:

```sh
sudo atlas-hailo-setup
```

The command is interactive and will ask before changing kernel or container
components. It:

- removes host HailoRT/TAPPAS userspace packages;
- installs the pinned Hailo PCIe driver and firmware on the host;
- installs and enables Docker when required;
- builds the pinned HailoRT/TAPPAS container;
- records the immutable image ID in
  `/etc/atlas-agent/hailo-container.env`.

Exit status `3` means that the installation succeeded but the loaded driver or
device cannot be verified until after a reboot. When requested, reboot:

```sh
sudo reboot
```

Reconnect to the onboard computer and inspect the Hailo installation:

```sh
sudo atlas-hailo-setup status
```

When the command reports conflicting host HailoRT/TAPPAS packages and replacing
them with the Atlas container profile is intentional, rerun it with:

```sh
sudo atlas-hailo-setup --replace-existing
```

The replacement removes host Hailo userspace packages, but keeps the host-side
driver and firmware on the pinned compatibility profile.

### Keep an existing native Hailo runtime

If a compatible Hailo runtime is already installed and container migration is
not currently desired, skip `atlas-hailo-setup`. The interactive Atlas setup can
discover and continue using the native process runtime. Confirm it afterward
with `sudo atlas-setup doctor`.

## 4. Configure and start Atlas

On a clean onboard computer with no existing transform bundle, run the
interactive installer now:

```sh
sudo atlas-setup
```

When upgrading the accepted `0.1.16` aircraft, defer that command until after
the exact-hash transform migration later in this section. The existing spatial
service must be stopped before replacing its active bundle.

The installer will:

1. Verify Ubuntu, Raspberry Pi, A8 camera, Hailo, and USB depth-camera discovery.
2. List detected serial devices, preferring `/dev/serial/by-id/...` paths.
3. Ask for the TELEM2 device and baud rate.
4. Passively verify a checksum-valid MAVLink heartbeat when MAVSDK is not
   already running.
5. Ask for the Atlas Native ground-station address.
6. Offer to enable Hailo object detection when the runtime and model match.
7. Offer to enable the logical `front-depth` spatial camera when supported USB
   hardware is detected.
8. Show the final configuration and services before applying changes.

When spatial support is selected, setup checks for the release-versioned
container image. This first implementation builds the bundled ROS 2 Jazzy
context on the Pi when no preloaded image is available, so the first run needs
internet access to the Ubuntu/ROS package repositories and GitHub and can take
several minutes. Later runs reuse the image. The build deliberately stops if
the available DepthAI core and ROS driver are not the validated 3.6.1/3.2.1
pair.

A waiting OAK can enumerate as `03e7:2485`, expose the synthetic USB identity
`03e72485`, and report 480 Mb/s even while connected by USB 3. `atlas-setup`
does not treat that boot-state identity as the camera MXID. The runtime uploads
firmware, the camera re-enumerates, and `atlas-setup doctor` performs the
authoritative live USB transport and MXID check while the service owns the
device. Do not copy `03e72485` into `ATLAS_SPATIAL_DEVICE_ID`.

For the pinned Hailo profile, confirm that the installation plan shows
`hailo (container)` perception and these services:

```text
atlas-mavsdk.service
atlas-agent.service
atlas-hailo-adapter.service
atlas-spatial-runtime.service  # when the front spatial camera is enabled
```

The generated configuration is stored at:

```text
/etc/atlas-agent/atlas-agent.env
/etc/atlas-agent/spatial.env
```

The indoor-localization foundation also seeds
`/var/lib/atlas-agent/spatial/transforms.v1.json` once. Setup will not overwrite
this file after its OAK/H-Flow geometry has been physically commissioned. The
default Ariadne OAK entry is `configured_unverified`: it records the operator's
approximate forward/upright 0.15 m mounting description and the explicit
CAM_A/RDF-to-body-FRD axis convention. Do not mark it `verified` until the
reference point, offsets, level alignment, and motion signs are physically
confirmed.

`0.1.17` adds an explicit configured-unverified aligned optical-frame edge.
Setup deliberately preserves an existing bundle, so migrate only the exact
accepted `0.1.16` bundle and retain a rollback copy. Run after installing the
new package and before rerunning setup:

```sh
set -eu
sudo systemctl stop atlas-spatial-runtime.service
export PYTHONPATH=/usr/share/atlas-agent/spatial-runtime/ros2_ws/src/atlas_spatial_runtime
CURRENT_TRANSFORM_HASH="$(sudo -E python3 -c \
  'from atlas_spatial_runtime.transform_contract import load_transform_bundle; print(load_transform_bundle("/var/lib/atlas-agent/spatial/transforms.v1.json")["sha256"])')"
test "$CURRENT_TRANSFORM_HASH" = \
  "sha256:30a90b90711af18a0bd5de3c0a2800aeb057f2ba1f59925151cc7179cd3c9304"

sudo install -d -m 0700 /var/backups/atlas-agent
sudo cp --preserve=mode,ownership,timestamps \
  /var/lib/atlas-agent/spatial/transforms.v1.json \
  /var/backups/atlas-agent/transforms.v1.json.0.1.16
sudo install -m 0640 -o root -g atlas-agent \
  /usr/share/atlas-agent/spatial-runtime/ros2_ws/src/atlas_spatial_runtime/config/transforms.v1.json \
  /var/lib/atlas-agent/spatial/transforms.v1.json

NEW_TRANSFORM_HASH="$(sudo -E python3 -c \
  'from atlas_spatial_runtime.transform_contract import load_transform_bundle; print(load_transform_bundle("/var/lib/atlas-agent/spatial/transforms.v1.json")["sha256"])')"
test "$NEW_TRANSFORM_HASH" = \
  "sha256:62ed08cdbdeab32df4e8d61c91e034ec720f94e60f021f5e2a2891cbf8e0f517"

sudo atlas-setup
```

If the first hash check fails, stop: the aircraft has a different commissioned
bundle and it must be reviewed/merged rather than overwritten. This migration
does not promote body-to-OAK or H-Flow geometry to `verified`.

## 5. Validate the installation

Run the complete Atlas diagnostic:

```sh
sudo atlas-setup doctor
```

For container-backed Hailo, the doctor verifies:

- installed and loaded host driver versions;
- host firmware package and live device firmware versions;
- `/dev/hailo0` access from the container;
- container HailoRT and TAPPAS compatibility;
- Hailo GStreamer elements and Python bindings;
- packaged HEF parsing and Hailo-8/Hailo-8L compatibility;
- Atlas, MAVSDK, Hailo, camera, and flight-controller connectivity.
- spatial container/image state, USB device and USB 2/3 transport;
- fresh synchronized color/depth frames, metre depth encoding, BMI270 health,
  live VIO state, calibration identity, and transform provenance.

After deploying the package containing the navigation-state slice, inspect its
read-only PX4/H-Flow state separately:

```sh
atlas-navigation-probe
```

For the hardware-validated DepthAI profile, also verify the immutable image,
production security boundary, private libusb selection, and versioned health
contract:

```sh
grep '^ATLAS_SPATIAL_CONTAINER_IMAGE=' /etc/atlas-agent/spatial.env
lsusb -t

sudo docker inspect \
  --format 'image={{.Image}} privileged={{.HostConfig.Privileged}} readonly={{.HostConfig.ReadonlyRootfs}} capdrop={{json .HostConfig.CapDrop}}' \
  atlas-spatial-runtime

sudo docker exec atlas-spatial-runtime /bin/bash -lc '
  core="$(find /opt/ros/jazzy/lib -name libdepthai_v3-core.so -print -quit)"
  ldd "${core}" | grep libusb

  set +u
  . /opt/ros/jazzy/setup.sh
  . /opt/atlas-spatial-runtime/setup.sh
  set -u

  ros2 run atlas_spatial_runtime atlas-spatial-probe \
    --socket /run/atlas-agent/spatial.sock \
    --json
'
```

Acceptance requires all spatial doctor checks to pass, the live USB tree to
show the booted device at 5000 Mb/s, the DepthAI core to resolve
`/opt/atlas-depthai-libusb/lib/libusb-1.0.so.0`, and the probe to report fresh
color plus `32FC1` metre depth, calibration, `synchronized=true`, and
`ready=true`, `streams.imu.ready=true`, and live non-authoritative VIO when it is
enabled. The container must report `privileged=false`, `readonly=true`, and
`capdrop=["ALL"]`. A `degraded` VIO state with reason `body to OAK transform is
not verified` is expected until physical extrinsic commissioning; it is not a
camera or estimator outage.

The installed OAK/H-Flow baseline and the indoor feature it supports are
summarized in
[`docs/indoor-ops-plan.md`](../docs/indoor-ops-plan.md). The reproducible
H-Flow parameter procedure remains in
[`docs/h-flow-px4-setup-and-verification.md`](../docs/h-flow-px4-setup-and-verification.md).
`atlas-setup doctor` does not currently inspect H-Flow, its PX4 parameter set,
optical-flow/range quality, or EKF fusion state; a passing Atlas doctor must not
be presented as H-Flow or GPS-denied position-hold acceptance.

The raw runtime socket's device description is setup-time provenance and can
retain `usb2-or-unbooted` or the bootloader product name from pre-boot
discovery. Starting with release `0.1.10`, the packaged
`atlas-spatial-runtime-check` reconciles that description with the live host
sysfs state and reports the current transport and `speedMbps`. The live runtime
check and the separate `spatial USB camera` / `spatial USB transport` doctor
checks are authoritative for the current MXID and USB connection.

Inspect all service states:

```sh
systemctl --no-pager --full status \
  atlas-mavsdk.service \
  atlas-agent.service \
  atlas-hailo-adapter.service \
  atlas-spatial-runtime.service
```

Follow Atlas Agent logs:

```sh
journalctl -u atlas-agent.service -f
```

Follow Hailo adapter and inference logs:

```sh
journalctl -u atlas-hailo-adapter.service -f
```

## 6. Commission geolocation boresight alignment

Atlas cannot infer physical camera-to-gimbal alignment from telemetry. A new
installation therefore advertises `geolocation:boresight_alignment:unverified`
and every estimate records `UNVERIFIED`, even though the default conservative
static angular allowance is 10 degrees.

Commission the installed camera, gimbal, mount, and aircraft as one assembly:

1. Use a surveyed, visually unambiguous ground target and a terrain source with
   known vertical datum and accuracy.
2. Collect centred-target estimates across the intended pitch/yaw envelope,
   approach directions, representative ranges, zoom settings, temperatures,
   and both directions of gimbal travel so backlash is exercised.
3. Compare each synchronized observation ray with the surveyed target ray.
   Retain the raw observations, mount serials, software version, method, and
   computed angular residuals as an immutable commissioning artifact.
4. Choose a static angular bound no smaller than the accepted worst-case error
   plus the deployment's reviewed safety margin. Do not use the residual mean
   as the bound.
5. Only after that artifact passes the deployment's accuracy criteria, add its
   identifier and the accepted bound to `/etc/atlas-agent/atlas-agent.env`:

   ```text
   ATLAS_GEOLOCATION_BORESIGHT_ALIGNMENT_REFERENCE=commissioning/<artifact-id>
   ATLAS_GEOLOCATION_BORESIGHT_ANGULAR_UNCERTAINTY_DEG=<accepted-bound>
   ```

6. Restart Agent and confirm Native shows the reference, configured bound, and
   `VERIFIED` on a new estimate. Existing evidence remains unchanged.

This configuration records the reviewed physical claim; it does not perform or
replace the survey. Repeat commissioning after camera, gimbal, mount, damping,
or alignment-affecting firmware changes. Leave the reference empty when the
physical test has not been completed.

## 7. Accept Follow from standoff flight control

Follow from standoff controls aircraft translation through PX4 Offboard. A new
or upgraded installation must leave `ATLAS_AIRCRAFT_FOLLOW_ENABLED=false` until
the exact aircraft/controller/companion/radio/software combination passes the
deployment acceptance process. Boresight commissioning above is a prerequisite,
not a substitute for flight-control validation.

Build one immutable acceptance record that includes airframe/controller and
companion identities, PX4/MAVSDK/Agent/Native versions, reviewed test envelopes,
logs, expected/observed Hold transitions, anomalies, reviewer, and approval
date. At minimum:

1. In PX4 SITL, exercise start/acquire/follow/end plus operator-lease expiry,
   stale/lost target, malformed or identity-changing target updates, telemetry
   loss, low battery, position-health loss, altitude/geofence violation,
   maximum duration, ground-link loss, and PX4 Offboard loss. Confirm zero
   setpoint, Offboard stop, and explicit PX4 Hold for every degraded path.
2. In HIL on the intended controller and companion, repeat timing and
   communication-loss cases, Agent/Native termination/restart, HM30 packet loss,
   RC takeover, and independent PX4 failsafes. Verify no second setpoint owner
   can coexist and no renewal can expand the original envelope.
3. In a protected controlled-flight area with a safety pilot, begin with a
   stationary surveyed target and conservative speed/acceleration/boundary.
   Progress to bounded moving targets only after each prior case is reviewed.
   Exercise operator Stop, lease/link loss, track loss, and RC takeover in the
   approved sequence.
4. Compare actual separation, speed, acceleration, altitude, boundary, target
   error, and Hold response latency with the reviewed acceptance criteria.
   Reject the installation on unexplained excursions or missing evidence.

Only after the record is accepted may `/etc/atlas-agent/atlas-agent.env` contain:

```text
ATLAS_AIRCRAFT_FOLLOW_ENABLED=true
ATLAS_AIRCRAFT_FOLLOW_VALIDATION_REFERENCE=commissioning/follow/<artifact-id>
```

Restart Agent and confirm Native displays `VERIFIED` with both the follow and
boresight references before any operational request. Placeholder references do
not constitute acceptance. Disable and repeat validation after changes to PX4,
MAVSDK, Agent navigation control, airframe dynamics, companion/controller/radio
hardware, or other behavior that can invalidate the evidence.

## Upgrading or replacing an existing Atlas Agent installation

Use this runbook after changing Atlas Agent and deploying a new package to a
computer that already has the packaged Agent, `/etc/atlas-agent/atlas-agent.env`,
and the systemd services. Do not repeat the initial Hailo setup during a normal
Agent upgrade.

An upgrade briefly stops MAVSDK, Atlas Agent, and perception. Perform it only
while the aircraft is landed, disarmed, and not executing a mission or holding
manual payload control. Keep the previously installed package until the new
installation has passed validation.

If the change also modified the shared ground-station protobuf or command
contract, treat Native and Agent as one coordinated release. Update Atlas
Native while the onboard Agent is stopped, then install and start the matching
Agent package. Do not operate payload or mission controls while the two sides
are on different contract versions.

### 1. Choose the package version

A new Debian version is strongly recommended because package state, Atlas
Native, logs, and rollback artifacts can then distinguish the builds:

```sh
export ATLAS_RELEASE_VERSION=0.1.17
```

For a development replacement that deliberately keeps the installed version:

```sh
export ATLAS_RELEASE_VERSION=0.1.17
```

Debian and Atlas will report the same version before and after a same-version
replacement. The binary checksum generated below is therefore the authoritative
proof that the new build was installed. Before rebuilding the same version,
move or copy the previous `.deb` and `.sha256` to a separate rollback directory;
the build uses the same output names and will otherwise overwrite them.

### 2. Test and build on a Linux development computer

Run the Agent test suite, then build the arm64 Debian package:

```sh
cd /path/to/sunnyside/atlas/atlas-agent

go test ./...
./packaging/build-deb.sh
(
  cd dist
  sha256sum -c "atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb.sha256"
)
```

Record the packaged Agent binary checksum as well. This is required to verify a
same-version replacement and useful for every deployment:

```sh
PACKAGE_CHECK_DIR="$(mktemp -d)"
dpkg-deb -x \
  "dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb" \
  "$PACKAGE_CHECK_DIR"
sha256sum "$PACKAGE_CHECK_DIR/usr/bin/atlas-agent" \
  | awk '{print $1}' \
  > "dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.binary.sha256"
```

The deployment set is now:

```text
dist/atlas-agent_<version>_arm64.deb
dist/atlas-agent_<version>_arm64.deb.sha256
dist/atlas-agent_<version>_arm64.binary.sha256
```

### 3. Transfer and verify the package

From the development computer:

```sh
export ATLAS_PI_HOST=<pi-user>@<pi-address>

scp \
  "dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb" \
  "dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb.sha256" \
  "dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.binary.sha256" \
  "${ATLAS_PI_HOST}:/tmp/"
```

On the onboard computer:

```sh
cd /tmp
export ATLAS_RELEASE_VERSION=0.1.17  # Use the exact version selected for this build.
sha256sum -c "atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb.sha256"
```

Do not install a package whose checksum does not pass.

### 4. Capture the current state and a rollback backup

Run a pre-upgrade health check and record the installed package and binary:

```sh
sudo atlas-setup doctor
dpkg-query -W -f='${Package} ${Version}\n' atlas-agent
sha256sum /usr/bin/atlas-agent
```

Back up the configuration and stable Agent identity while the current package
is still installed:

```sh
sudo install -d -m 0700 /var/backups/atlas-agent
ATLAS_BACKUP="/var/backups/atlas-agent/pre-upgrade-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"
sudo tar -C / -czf "$ATLAS_BACKUP" etc/atlas-agent var/lib/atlas-agent
printf 'Atlas backup: %s\n' "$ATLAS_BACKUP"
```

`/var/lib/atlas-agent` contains the stable installation and drone identities.
An ordinary package upgrade does not delete or replace that directory. Treat
the backup as sensitive operational data and copy it off the onboard computer
if it is needed for disaster recovery.

### 5. Stop the onboard services

Stop the services explicitly so package replacement cannot occur during flight
or while the old process is handling a command:

```sh
sudo systemctl stop \
  atlas-spatial-runtime.service \
  atlas-hailo-adapter.service \
  atlas-agent.service \
  atlas-mavsdk.service
```

Confirm they are inactive before continuing:

```sh
systemctl is-active atlas-hailo-adapter.service || true
systemctl is-active atlas-spatial-runtime.service || true
systemctl is-active atlas-agent.service || true
systemctl is-active atlas-mavsdk.service || true
```

Do not remove the existing package first. Installing over it preserves the
configuration, stable identity, systemd enablement, and Debian package history.

### 6. Install a newer version

For the recommended version-bumped package:

```sh
sudo apt install "/tmp/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb"
```

The package replaces the Agent and setup binaries, MAVSDK payload, packaged
model, Hailo build context, and systemd units. Its post-install script reloads
systemd. Because this runbook stopped the services first, the configuration
step below is responsible for starting the configured units again.

### 7. Replace the existing package with the same version

Use this command instead of the previous section when the package version was
not changed:

```sh
sudo apt install --reinstall \
  "/tmp/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb"
```

The `--reinstall` flag is required: a normal `apt install` may decide that the
same version is already installed and leave the old binary in place. The
package and Native UI will still display the same release version, so complete
the binary-checksum comparison in the validation section.

### 8. Refresh configuration and start services

If the package changed the Hailo container/runtime files listed in the next
section, leave the services stopped, complete section 9 first, and then return
here.

The package preserves the existing environment file. Re-run Atlas setup so it
validates the newly installed payload, carries forward the discovered settings,
updates `ATLAS_AGENT_VERSION` from the package release manifest, reloads
systemd, and enables/starts the configured services:

```sh
sudo atlas-setup
```

Review the proposed serial device, baud rate, ground-station address, camera,
and perception mode before accepting it. For a scripted deployment, first
review a non-mutating plan and then apply the discovered configuration:

```sh
sudo atlas-setup --dry-run --non-interactive
sudo atlas-setup --non-interactive
```

Use the scripted form only when the dry-run shows the expected configuration.

### 9. Rebuild Hailo only when its packaged runtime changed

Skip this section when the change was limited to Agent Go code, protobufs,
configuration logic, or systemd units. The existing Hailo container and host
driver remain valid for those upgrades.

Rebuild the pinned Hailo runtime when the deployment changed any of these:

- `packaging/hailo/Dockerfile` or its pinned Hailo/TAPPAS versions;
- `atlas-hailort-adapter.py` or the container health check;
- the required Hailo driver or firmware versions;
- the Hailo container execution profile.

Run the packaged setup after installing the new `.deb`:

```sh
sudo atlas-hailo-setup
```

The onboard services should still be stopped at this point. When the command
exits successfully without requiring a reboot, return to section 8 so
`atlas-setup` selects the new immutable container image and starts the services.

Use `--replace-existing` only when the command reports a conflicting installed
Hailo profile and that replacement is intentional. Exit status `3` means the
new driver/runtime was installed but requires a reboot. After any requested
reboot, run:

```sh
sudo atlas-setup
sudo atlas-setup doctor
```

### 10. Validate the deployed build

Confirm the installed Debian version and release manifest:

```sh
dpkg-query -W -f='${Package} ${Version}\n' atlas-agent
grep '^ATLAS_RELEASE_VERSION=' /usr/share/atlas-agent/release.env
grep '^ATLAS_MAVSDK_' /usr/share/atlas-agent/release.env
grep '^ATLAS_AGENT_VERSION=' /etc/atlas-agent/atlas-agent.env
grep '^ATLAS_CAMERA_TRANSPORT=' /etc/atlas-agent/atlas-agent.env
```

Verify that `/usr/bin/atlas-agent` exactly matches the binary from the
transferred package. This is mandatory for a same-version replacement:

```sh
EXPECTED_AGENT_SHA256="$(cat "/tmp/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.binary.sha256")"
printf '%s  %s\n' "$EXPECTED_AGENT_SHA256" /usr/bin/atlas-agent \
  | sha256sum -c -
```

Verify that the installed MAVSDK server is the checksum-pinned binary paired
with the generated Agent client:

```sh
. /usr/share/atlas-agent/release.env
printf '%s  %s\n' "$ATLAS_MAVSDK_SHA256" \
  /usr/libexec/atlas-agent/mavsdk_server \
  | sha256sum -c -
```

Run the complete diagnostic and inspect the services:

```sh
sudo atlas-setup doctor
systemctl --no-pager --full status \
  atlas-mavsdk.service \
  atlas-agent.service \
  atlas-hailo-adapter.service \
  atlas-spatial-runtime.service
journalctl -u atlas-agent.service -n 200 --no-pager
```

For the current navigation foundation, confirm that the approximately 1 Hz PX4
local-position-health observation does not create a false stale transition and
that spatial RGB-D/IMU/VIO remains live. This check leaves the aircraft disarmed
and sends no movement command:

```sh
for sample in $(seq 1 20); do
  sudo atlas-navigation-probe --require-ready >/dev/null || {
    sudo atlas-navigation-probe
    exit 1
  }
  sleep 0.5
done

sudo /usr/libexec/atlas-agent/atlas-spatial-runtime-check --json
systemctl show \
  atlas-mavsdk.service \
  atlas-agent.service \
  atlas-spatial-runtime.service \
  -p NRestarts
sudo docker inspect --format '{{.Image}}' atlas-spatial-runtime
```

Every navigation probe must exit successfully. The spatial check must report
`ready=true`, `USB_TRANSPORT=usb3` in its flattened form or
`device.connection="usb3"` with `speedMbps=5000` in JSON, and the restart
counts must remain zero. When VIO is enabled, the JSON must report a fresh
live `vio` sample with `authoritative=false`, `mappingEnabled=true`,
`px4FusionEnabled=false`, and `movementAuthority=false`. `mappingEnabled`
means that the isolated visualization cloud is active; it does not grant
flight authority. Retain the immutable image SHA printed by `docker inspect`
in the commissioning record.

For `0.1.17`, prove the authority boundary and verify that both VIO and the
bounded live cloud are publishing:

```sh
sudo /usr/libexec/atlas-agent/atlas-spatial-runtime-check --json | \
python3 -c '
import json, sys
probe = json.load(sys.stdin)
vio = probe["vio"]
assert vio["authoritative"] is False
assert vio["mappingEnabled"] is True
assert vio["px4FusionEnabled"] is False
assert vio["movementAuthority"] is False
print(json.dumps(vio, sort_keys=True))
'

sudo docker exec atlas-spatial-runtime /bin/bash -lc '
  set +u
  . /opt/ros/jazzy/setup.sh
  . /opt/atlas-spatial-runtime/setup.sh
  set -u
  timeout 30 ros2 topic hz /atlas/spatial/vio/odometry
  timeout 15 ros2 topic echo /atlas/spatial/map/points --once --field width
'
```

The measured VIO output should be approximately the intended 20 Hz, allowing
normal scheduling jitter, and the cloud width must become non-zero while the
camera sees valid depth. A timeout exit after printing VIO samples is expected.
Do not interpret cadence or a populated cloud as localization accuracy. During
a bounded soak, retain provider, IMU, VIO, live-cloud, temperature, throttling,
restart, and backpressure evidence:

```sh
ATLAS_SOAK_START="$(date --iso-8601=seconds)"
for sample in $(seq 1 60); do
  sudo /usr/libexec/atlas-agent/atlas-spatial-runtime-check --json
  sleep 5
done
systemctl show atlas-spatial-runtime.service -p NRestarts
vcgencmd measure_temp
vcgencmd get_throttled
sudo journalctl -u atlas-spatial-runtime.service \
  --since "$ATLAS_SOAK_START" --no-pager | \
  grep -c ATLAS_DEPTHAI_VIO_IMAGE_BACKPRESSURE || true
```

Backpressure must remain bounded without RGB-D/VIO staleness, assertion,
provider stall, or restart, and should be materially lower than the retained
30 Hz baseline over a comparable interval. Zero drops are not required, and
fresh VIO output means dropping obsolete visual work is not estimator failure.

In Atlas Native, confirm that the same drone identity reconnects, telemetry is
fresh, and the expected Agent capabilities are present. When perception or
payload code changed, also verify the Live workspace, detection frame lease,
gimbal discovery, one small inspection movement, and safe control release while
the aircraft remains disarmed and on the ground.

### 11. Roll back a failed upgrade

Keep the aircraft grounded and stop the four services before rollback. Install
the retained previous package:

```sh
sudo systemctl stop \
  atlas-spatial-runtime.service \
  atlas-hailo-adapter.service \
  atlas-agent.service \
  atlas-mavsdk.service
```

When rolling back to an older Debian version:

```sh
export ATLAS_PREVIOUS_VERSION=0.1.16
sudo apt install --allow-downgrades \
  "/tmp/atlas-agent_${ATLAS_PREVIOUS_VERSION}_arm64.deb"
sudo install -m 0640 -o root -g atlas-agent \
  /var/backups/atlas-agent/transforms.v1.json.0.1.16 \
  /var/lib/atlas-agent/spatial/transforms.v1.json
sudo docker image inspect \
  sha256:06c9106ea2f59f2d5f879b28c7482e0221dff7128e19c152c63e68c1cbe65edc \
  >/dev/null
```

When both builds intentionally have the same version, install the separately
retained previous `.deb` with `--reinstall`:

```sh
sudo apt install --reinstall /path/to/rollback/atlas-agent_previous_arm64.deb
```

Then reapply the configuration from the restored package and validate it:

```sh
sudo atlas-setup
sudo atlas-setup doctor
sudo docker inspect --format '{{.Image}}' atlas-spatial-runtime
```

The restored runtime image must resolve to the accepted `0.1.16` image
`sha256:06c9106ea2f59f2d5f879b28c7482e0221dff7128e19c152c63e68c1cbe65edc`.
If that exact image was removed, reload the separately retained `0.1.16`
archive before running `atlas-setup`; do not relabel the `0.1.17` image as a
rollback.

If the failed deployment also rebuilt the Hailo container or changed its pinned
runtime, reinstall the previous package first and then run its packaged
`sudo atlas-hailo-setup` before `atlas-setup`. Restore the configuration/state
backup only when package rollback is insufficient; overwriting
`/var/lib/atlas-agent` can change operational identity if the wrong backup is
used.

## Troubleshooting

### `/dev/hailo0` is missing

```sh
lspci -Dnn | grep -i hailo
ls -l /dev/hailo0
sudo modprobe hailo_pci
sudo atlas-hailo-setup status
```

If the driver was just installed or replaced, reboot before investigating
further.

### The Hailo container service is inactive

```sh
sudo systemctl restart atlas-hailo-adapter.service
systemctl --no-pager --full status atlas-hailo-adapter.service
journalctl -u atlas-hailo-adapter.service -n 200 --no-pager
```

### The OAK firmware boots but RGB-D topics never become ready

The validated failure signature is a DepthAI log that reports firmware boot
success and then `X_LINK_DEVICE_NOT_FOUND`. That is a container USB
re-enumeration failure, not evidence that the camera or USB 3 cable is bad.
Confirm that the installed release selected the expected image and private
library:

```sh
dpkg-query -W -f='${Package} ${Version}\n' atlas-agent
grep '^ATLAS_SPATIAL_CONTAINER_IMAGE=' /usr/share/atlas-agent/release.env
grep '^ATLAS_SPATIAL_CONTAINER_IMAGE=' /etc/atlas-agent/spatial.env

sudo docker exec atlas-spatial-runtime /bin/bash -lc '
  core="$(find /opt/ros/jazzy/lib -name libdepthai_v3-core.so -print -quit)"
  ldd "${core}" | grep libusb
'

sudo journalctl -u atlas-spatial-runtime.service -n 200 --no-pager
```

The linkage must resolve `/opt/atlas-depthai-libusb/lib/libusb-1.0.so.0`.
Installing `udev` in the runtime image or forcing the driver to USB 2 did not
fix this failure and is not the supported recovery. Install a release that
contains the pinned private-lib workaround, run `sudo atlas-setup` so its
release-versioned image is built and pinned, and validate USB 3 while the
runtime is active.

### Atlas is not configured

If `atlas-setup doctor` reports that Atlas is not configured, run:

```sh
sudo atlas-setup
```

### Re-run diagnostics after a correction

```sh
sudo atlas-hailo-setup status
sudo atlas-setup doctor
```
