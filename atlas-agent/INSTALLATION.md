# Atlas Agent Installation Guide

Use this procedure to install or update Atlas Agent on the supported Raspberry
Pi. This document is the canonical procedure for onboard networking, package
installation, service operation, updates, and deployment troubleshooting.

**Warning:** Keep the aircraft landed, disarmed, and without propellers during
package and service changes. An unexpected motor command can cause injury.

For Agent architecture, see
[`../docs/atlas-agent.md`](../docs/atlas-agent.md).

## Supported onboard computer

- Raspberry Pi 5 running Ubuntu 24.04 arm64
- Raspberry Pi AI HAT+ with Hailo-8L
- SIYI A8 camera on the onboard Ethernet network
- PX4 connected through a stable serial device
- Optional DepthAI camera on a direct USB 3 connection
- Optional H-Flow connected to PX4 through DroneCAN

Atlas setup does not commission H-Flow, geolocation boresight, or Follow from
standoff. Use the applicable setup and acceptance procedure.

## HM30 network

The supported field network is a dedicated `192.168.144.0/24` Ethernet subnet
carried by the HM30 radio link:

| Device | Address | Purpose |
| --- | --- | --- |
| HM30 Air | `192.168.144.11` | Air-radio management |
| HM30 Ground | `192.168.144.12` | Ground-radio management |
| SIYI A8 camera | `192.168.144.25` | RTSP and SIYI UDP control |
| Raspberry Pi | `192.168.144.168` | Atlas Agent and onboard services |
| Ground computer | `192.168.144.50` | Atlas Native listener on port `7443` |

Configure the Pi to connect to Atlas Native at `192.168.144.50:7443`. Do not
use the HM30 Ground management address as the Atlas Native address.

Use subnet mask `255.255.255.0`. Give each device a unique address. Leave the
gateway empty on each dedicated HM30 interface. Keep Wi-Fi as the default
internet route for the Pi.

If the ground computer uses a different address, set both applicable
configuration values:

- Set `ATLAS_GROUND_STATION_LISTEN_ADDR` in Atlas Native.
- Set `ATLAS_GROUND_STATION_ADDR` in Atlas Agent.
- Use the same address and port in both values.

## Build and transfer the latest packages

From the Agent source directory, run the tests:

```sh
cd /path/to/sunnyside/atlas/atlas-agent
go test ./...
```

Build the Agent package:

```sh
cd /path/to/sunnyside/atlas/atlas-agent
./packaging/release.sh build 0.1.29
```

Build the independently versioned Spatial package:

```sh
cd /path/to/sunnyside/atlas/atlas-spatial-runtime
./packaging/release.sh build 0.1.0
```

The Spatial build runs the provider-neutral source tests before packaging.
Confirm that these package files exist:

```text
atlas-agent/dist/atlas-agent_0.1.29_arm64.deb
atlas-spatial-runtime/dist/atlas-spatial-runtime_0.1.0_arm64.deb
```

Transfer the selected packages to the Pi:

```sh
cd /path/to/sunnyside/atlas
./scripts/transfer-onboard-release.sh \
  0.1.29 0.1.0 mofe@ariadne-robot
```

If only one component changed, use its `packaging/release.sh transfer` command.
Each `dist` directory keeps only its latest package.

The release process does not create a Spatial image archive, an Atlas checksum
bundle, a cross-component manifest, or a rollback set.

## Install on the Pi

On the landed, disarmed, and propeller-free aircraft, install the packages:

```sh
cd /tmp
sudo apt install \
  ./atlas-agent_0.1.29_arm64.deb \
  ./atlas-spatial-runtime_0.1.0_arm64.deb

sudo atlas-setup
```

The Pi does not need a repository checkout or PyPI access. The Spatial package
contains its Linux-arm64 DepthAI and NumPy runtime.

Install the Spatial package only if the aircraft uses the depth camera. The
Agent and Spatial packages are independent. An Agent installation does not
replace Spatial code. A Spatial installation does not replace Agent code.

The interactive setup does these operations:

1. Selects and validates one installed aircraft profile.
2. Verifies the Pi/Ubuntu platform and discovers PX4 serial, camera, Hailo, and
   optional depth hardware.
3. Asks for the TELEM2 path, baud rate, and Native ground-station address.
4. Selects the A8 transport and optional perception runtime.
5. Offers the DepthAI provider when supported hardware is present.
6. Shows the configuration and services before applying changes.

For the first non-interactive setup, specify the aircraft profile:

```sh
sudo atlas-setup --non-interactive --aircraft-profile ariadne
```

Configuration is written to:

```text
/etc/atlas-agent/atlas-agent.env
/etc/atlas-agent/spatial.env
/etc/atlas-agent/aircraft-profile.json
```

The Agent package installs all tracked aircraft profiles under:

```text
/usr/share/atlas-agent/aircraft-profiles/
```

Setup validates the selected profile and copies it to
`/etc/atlas-agent/aircraft-profile.json`. Atlas Spatial Runtime loads this
active copy. Atlas Agent does not load the profile because it has no depth
consumer.

The profile contains:

- its ID;
- the depth-camera device ID; and
- the camera-to-body mounting translation and rotation.

The profile does not contain runtime capabilities, a calibration catalogue,
safety thresholds, hashes, or controller authorization. Measure and verify the
values for each different aircraft.

## Hailo profile

For the pinned container-backed Hailo profile:

```sh
sudo atlas-hailo-setup
```

If setup finds an existing host userspace installation, it stops. To approve
the replacement, run:

```sh
sudo atlas-hailo-setup --replace-existing
```

Do not run `atlas-hailo-setup` again for an ordinary Agent update.

The default A8 camera contract is:

```text
ATLAS_CAMERA_TRANSPORT=siyi_udp
```

This value disables MAVSDK Camera discovery for a SIYI UDP payload. Use
`mavsdk` or `hybrid` only when the installed camera configuration requires it.

## Spatial runtime

Spatial is a native Python/systemd service installed from its own Debian
package. It uses DepthAI directly and owns its private runtime under
`/opt/atlas-spatial-runtime`, udev rule, service unit, source tests, and
diagnostic commands. It requires neither ROS nor a Spatial Docker image.

If Spatial source or its DepthAI dependency changed, build and transfer a new
Spatial package:

```sh
cd /path/to/sunnyside/atlas/atlas-spatial-runtime
./packaging/release.sh build 0.1.1
./packaging/release.sh transfer 0.1.1 mofe@ariadne-robot

# On the landed, disarmed Pi:
sudo apt install /tmp/atlas-spatial-runtime_0.1.1_arm64.deb
sudo atlas-setup
sudo atlas-setup doctor
```

Package installation replaces the private runtime under
`/opt/atlas-spatial-runtime`. It does not keep old environments, images, or
Spatial rollback artifacts. The first package installation also removes the
legacy `/opt/atlas-spatial-runtime/venv` path. An Agent-only release does not
change Spatial.

If `sudo atlas-setup doctor` reports Spatial ready, remove the former Spatial
container and image:

```sh
sudo docker rm -f atlas-spatial-runtime 2>/dev/null || true
sudo docker image rm atlas-spatial-runtime:local
```

**Caution:** Do not uninstall Docker if the selected Hailo profile uses the
container adapter. The Spatial migration does not remove dependencies of other
onboard services.

A DepthAI device that waits for firmware can report `03e7:2485`, artificial
identity `03e72485`, and 480 Mb/s. Do not use `03e72485` as the camera MXID.
The live doctor check matches the booted camera to sysfs.

The selected services are:

```text
atlas-mavsdk.service
atlas-agent.service
atlas-hailo-adapter.service       # when container perception is enabled
atlas-spatial-runtime.service     # when depth acquisition is enabled
```

Spatial is independent from Agent/MAVSDK. A depth-provider failure must not
stop ordinary telemetry or commands.

## Validate the deployment

Run these deployment checks:

```sh
dpkg-query -W -f='${Package} ${Version}\n' \
  atlas-agent atlas-spatial-runtime
sudo atlas-setup doctor
```

For Spatial, `atlas-setup doctor` checks:

- independently installed runtime, diagnostic command, and service state;
- configured camera discovery and live USB transport;
- fresh native `16UC1` depth in millimeters with an explicit meter scale;
- a non-empty depth frame ID;
- valid calibration with matching frame ID and dimensions; and
- current provider errors.

This readiness contract does not include color, pose estimation, mapping,
transform digests, or cloud streaming. A successful check does not authorize
obstacle avoidance. It also does not repeat the source test suite.

If a check fails, inspect the services:

```sh
systemctl --no-pager --full status \
  atlas-mavsdk.service \
  atlas-agent.service \
  atlas-hailo-adapter.service \
  atlas-spatial-runtime.service

journalctl -u atlas-agent.service -n 200 --no-pager
journalctl -u atlas-spatial-runtime.service -n 200 --no-pager
```

## Update policy

**Warning:** Update the onboard software only when the aircraft is landed,
disarmed, without propellers, and outside a mission.

```sh
sudo systemctl stop \
  atlas-agent.service \
  atlas-mavsdk.service \
  atlas-hailo-adapter.service \
  atlas-spatial-runtime.service

sudo apt install \
  /tmp/atlas-agent_0.1.29_arm64.deb \
  /tmp/atlas-spatial-runtime_0.1.0_arm64.deb
sudo atlas-setup
sudo atlas-setup doctor
```

Install only the component packages that changed. Install multiple changed
packages in one `apt` transaction.

Keep only the Agent and Spatial package versions that the Pi uses. Atlas does
not provide a rollback workflow. Correct a failed update with a new source
change and package.

## Troubleshooting

### Atlas is not configured

```sh
sudo atlas-setup
sudo atlas-setup doctor
```

### Hailo service is inactive

```sh
sudo systemctl restart atlas-hailo-adapter.service
systemctl --no-pager --full status atlas-hailo-adapter.service
journalctl -u atlas-hailo-adapter.service -n 200 --no-pager
```

### The OAK boots but depth is not ready

```sh
lsusb -t
sudo atlas-setup doctor
sudo journalctl -u atlas-spatial-runtime.service -n 200 --no-pager
```

The doctor output identifies the failed Spatial check. It includes USB
discovery, depth encoding, calibration, frame, and the runtime `lastError`.

**Warning:** Keep the aircraft grounded while you diagnose an acquisition or
calibration failure.

## Related runbooks

- [Spatial runtime boundary](../docs/spatial-runtime.md)
- [H-Flow PX4 setup](../docs/h-flow-px4-setup-and-verification.md)
- [Inference, geolocation, and Follow](../docs/inference-tracking-and-follow.md)
