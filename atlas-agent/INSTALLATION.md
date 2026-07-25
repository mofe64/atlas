# Atlas Agent Installation Guide

This guide installs or updates Atlas Agent on the supported Raspberry Pi. Keep
the aircraft landed and disarmed throughout package and service changes.
It is the canonical document for onboard networking, package installation,
service operation, updates, and deployment troubleshooting. Agent architecture
is documented in [`../docs/atlas-agent.md`](../docs/atlas-agent.md).

## Supported onboard computer

- Raspberry Pi 5 running Ubuntu 24.04 arm64
- Raspberry Pi AI HAT+ with Hailo-8L
- SIYI A8 camera on the onboard Ethernet network
- PX4 connected through a stable serial device
- Optional DepthAI camera on a direct USB 3 connection
- Optional H-Flow connected to PX4 through DroneCAN

Atlas setup does not commission H-Flow, geolocation boresight, or aircraft
Follow. Use their dedicated setup and acceptance procedures.

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

The Pi connects to Atlas Native at `192.168.144.50:7443`; it does not connect
to the HM30 Ground management address. Use subnet mask `255.255.255.0`, keep
every address unique, and leave the gateway empty on the dedicated HM30
interfaces. The Pi should continue using Wi-Fi for its default internet route.

If the ground computer intentionally uses another address, configure Native's
`ATLAS_GROUND_STATION_LISTEN_ADDR` and Agent's
`ATLAS_GROUND_STATION_ADDR` to the same address and port.

## Build and transfer the latest packages

Validate Agent source before packaging:

```sh
cd /path/to/sunnyside/atlas/atlas-agent
go test ./...
```

Build the Agent package and the independently versioned Spatial package:

```sh
cd /path/to/sunnyside/atlas/atlas-agent
./packaging/release.sh build 0.1.29

cd /path/to/sunnyside/atlas/atlas-spatial-runtime
./packaging/release.sh build 0.1.0
```

The Spatial build command runs its provider-neutral source tests before
packaging. The two outputs are:

```text
atlas-agent/dist/atlas-agent_0.1.29_arm64.deb
atlas-spatial-runtime/dist/atlas-spatial-runtime_0.1.0_arm64.deb
```

Transfer both selected packages to the Pi:

```sh
cd /path/to/sunnyside/atlas
./scripts/transfer-onboard-release.sh \
  0.1.29 0.1.0 mofe@ariadne-robot
```

Each component also has its own `packaging/release.sh transfer` command when
only one package changed. Each `dist` directory retains only its latest
package. The release paths create no Spatial image archive, Atlas checksum
bundle, cross-component manifest, or rollback set.

## Install on the Pi

On the landed and disarmed Pi:

```sh
cd /tmp
sudo apt install \
  ./atlas-agent_0.1.29_arm64.deb \
  ./atlas-spatial-runtime_0.1.0_arm64.deb

sudo atlas-setup
```

The Pi needs neither a repository checkout nor PyPI access. The Spatial package
contains its Linux-arm64 DepthAI and NumPy runtime. Skip that package only on
an aircraft that will not enable `front-depth`. Agent and Spatial remain
independent packages: installing an Agent `.deb` never replaces camera code,
and installing Spatial never replaces Agent.

The interactive setup:

1. Selects and validates one installed aircraft profile.
2. Verifies the Pi/Ubuntu platform and discovers PX4 serial, camera, Hailo, and
   optional depth hardware.
3. Asks for the TELEM2 path, baud rate, and Native ground-station address.
4. Selects the A8 transport and optional perception runtime.
5. Offers the logical `front-depth` depth provider when supported hardware is
   present.
6. Shows the configuration and services before applying changes.

For a first non-interactive setup, select the profile explicitly:

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

Setup copies the selected profile to
`/etc/atlas-agent/aircraft-profile.json`; Agent and Spatial both load that
active copy. A profile contains only its id, the depth-camera device id, and
the camera-to-body mounting translation and rotation. It contains no runtime
capabilities, calibration catalogue, safety thresholds, hashes, or controller
authorization. A different aircraft must provide and verify its own physical
measurements.

## Hailo profile

For the pinned container-backed Hailo profile:

```sh
sudo atlas-hailo-setup
```

If an existing host userspace installation is detected, setup stops. Replace
it only when that migration is deliberate:

```sh
sudo atlas-hailo-setup --replace-existing
```

Ordinary Agent updates do not require rerunning `atlas-hailo-setup`.

The default A8 camera contract is:

```text
ATLAS_CAMERA_TRANSPORT=siyi_udp
```

This keeps MAVSDK Camera discovery disabled for a payload controlled through
the SIYI UDP SDK. Use `mavsdk` or `hybrid` only for an intentionally different
installation.

## Spatial runtime

Spatial is a native Python/systemd service installed from its own Debian
package. It uses DepthAI directly and owns its private runtime under
`/opt/atlas-spatial-runtime`, udev rule, service unit, source tests, and
diagnostic commands. It requires neither ROS nor a Spatial Docker image.

Update Spatial only when its source or DepthAI dependency changed:

```sh
cd /path/to/sunnyside/atlas/atlas-spatial-runtime
./packaging/release.sh build 0.1.1
./packaging/release.sh transfer 0.1.1 mofe@ariadne-robot

# On the landed, disarmed Pi:
sudo apt install /tmp/atlas-spatial-runtime_0.1.1_arm64.deb
sudo atlas-setup
sudo atlas-setup doctor
```

Package installation replaces the one private runtime under
`/opt/atlas-spatial-runtime`; no old environments, images, or Spatial rollback
artifacts are retained. The first package installation also removes the exact
legacy `/opt/atlas-spatial-runtime/venv` path used by the retired checkout
installer. An Agent-only release does not trigger this update.

After `sudo atlas-setup doctor` reports Spatial ready, remove the former
Spatial container and image once:

```sh
sudo docker rm -f atlas-spatial-runtime 2>/dev/null || true
sudo docker image rm atlas-spatial-runtime:local
```

Do not uninstall Docker when the selected Hailo profile still uses its
container-backed adapter. Docker is removed from Spatial ownership, not
implicitly from unrelated onboard services.

A waiting DepthAI device can report `03e7:2485`, an artificial `03e72485`
identity, and 480 Mb/s before firmware upload. Do not configure that value as
the camera MXID. The live doctor check reconciles the booted camera with sysfs.

The selected services are:

```text
atlas-mavsdk.service
atlas-agent.service
atlas-hailo-adapter.service       # when container perception is enabled
atlas-spatial-runtime.service     # when front-depth is enabled
```

Spatial is independent from Agent/MAVSDK. A depth-provider failure must not
stop ordinary telemetry or commands.

## Validate the deployment

Run:

```sh
dpkg-query -W -f='${Package} ${Version}\n' \
  atlas-agent atlas-spatial-runtime
sudo atlas-setup doctor
```

For spatial, doctor checks:

- independently installed runtime, diagnostic command, and service state;
- configured camera discovery and live USB transport;
- fresh native `16UC1` depth in millimetres with an explicit metre scale;
- a non-empty depth frame ID;
- valid calibration with matching frame ID and dimensions; and
- current provider errors.

Colour, pose estimation, mapping, transform digests, and cloud streaming are
not part of this readiness contract. A passing check does not commission
obstacle avoidance or repeat the source test suite.

Inspect services when needed:

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

Update only while landed, disarmed, and outside a mission:

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

Install only the component packages that changed, but install multiple changed
packages in one `apt` transaction. Keep only the Agent and Spatial package
versions used by the Pi. Atlas provides no rollback workflow: correct a failed
update with a new source change and package. This policy accepts longer
recovery in exchange for much lower release and storage complexity.

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

The doctor output includes the concrete Spatial diagnostic, including USB
discovery, depth frame/encoding, calibration validity and frame, or the
runtime's `lastError`. Keep the aircraft grounded while diagnosing acquisition
or calibration failures.

## Related runbooks

- [Spatial runtime boundary](../docs/spatial-runtime.md)
- [H-Flow PX4 setup](../docs/h-flow-px4-setup-and-verification.md)
- [Inference, geolocation, and Follow](../docs/inference-tracking-and-follow.md)
- [Indoor-navigation decommission audit](../docs/indoor-navigation-decommission-audit.md)
