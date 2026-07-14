# Atlas Agent Installation Guide

This guide migrates an onboard Raspberry Pi from the deprecated Atlas Agent
stack to the packaged Atlas Agent with systemd-managed MAVSDK and Hailo
perception services.

## Supported onboard computer

- Raspberry Pi 5 running Ubuntu 24.04 arm64
- Raspberry Pi AI HAT+ with Hailo-8L
- SIYI A8 camera reachable over its onboard Ethernet network
- PX4 flight controller connected through a stable serial device

The commands use `0.1.0` as an example release. Replace that value with the
release being deployed.

## Migration overview

The migration has a short period where no Atlas service is running:

1. Build and transfer the new Debian package.
2. Remove the deprecated services and files.
3. Install the new package.
4. Move Hailo userspace into the pinned container profile.
5. Run the interactive Atlas configuration.
6. Validate the complete installation.

The deprecated cleanup preserves the existing camera network configuration and
Hailo installation by default. Do not use `--remove-eth0-config` during this
migration because Atlas still needs the A8 network.

## 1. Build the Atlas package

Run these commands on a Linux development or build computer:

```sh
cd /path/to/sunnyside/atlas/atlas-agent

export ATLAS_RELEASE_VERSION=0.1.0
./packaging/build-deb.sh
```

The build produces:

```text
dist/atlas-agent_0.1.0_arm64.deb
dist/atlas-agent_0.1.0_arm64.deb.sha256
```

Transfer both files to the onboard computer:

```sh
export ATLAS_RELEASE_VERSION=0.1.0
export ATLAS_PI_HOST=<pi-user>@<pi-address>

scp \
  "dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb" \
  "dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb.sha256" \
  "${ATLAS_PI_HOST}:/tmp/"
```

Skip this section when the release package is already available on the onboard
computer.

## 2. Remove the deprecated Atlas stack

Run these commands on the onboard computer from the deprecated checkout:

```sh
cd /path/to/sunnyside/atlas/atlas-agent-deprecated

./scripts/cleanup-onboard-pi.sh --dry-run
./scripts/cleanup-onboard-pi.sh --yes
```

The default cleanup removes:

- deprecated Atlas, video, MAVSDK, MAVLink Router, and MediaMTX services;
- deprecated binaries and Hailo model copies under `/opt/atlas`;
- deprecated configuration, state, and logs.

It preserves:

- Hailo driver, firmware, HailoRT, and TAPPAS packages;
- FFmpeg and GStreamer packages;
- the A8 `eth0`/netplan configuration;
- downloaded Hailo package caches.

Do not use `--purge-agent-packages` for a normal migration. Package removal can
affect tools or libraries used by other software on the Pi.

Optionally confirm that the deprecated-only services are no longer active:

```sh
systemctl is-active atlas-video-agent.service || true
systemctl is-active atlas-mediamtx.service || true
systemctl is-active atlas-mavlink-router.service || true
```

Inactive, disabled, or not-found results are expected.

## 3. Install the packaged Atlas Agent

On the onboard computer, verify and install the transferred package:

```sh
cd /tmp

export ATLAS_RELEASE_VERSION=0.1.0
sha256sum -c "atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb.sha256"
sudo apt install "./atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb"
```

The package installs `atlas-agent`, `atlas-setup`, `atlas-hailo-setup`, the
pinned Hailo container build context, MAVSDK, the compatible HEF model, and the
systemd unit files. It does not enable the services until `atlas-setup` has
written a valid configuration.

## 4. Configure Hailo

### Recommended migration: pinned container profile

The deprecated stack normally leaves HailoRT and TAPPAS installed on the host.
Migrate them into the Atlas container with:

```sh
sudo atlas-hailo-setup --replace-existing
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

### Clean Ubuntu host

When no Hailo packages have previously been installed, omit the replacement
flag:

```sh
sudo atlas-hailo-setup
```

### Keep an existing native Hailo runtime

If the deprecated Hailo runtime is known to work and container migration is not
currently desired, skip `atlas-hailo-setup`. The interactive Atlas setup can
discover and continue using the native process runtime. Confirm it afterward
with `sudo atlas-setup doctor`.

## 5. Configure and start Atlas

Run the interactive installer on the onboard computer:

```sh
sudo atlas-setup
```

The installer will:

1. Verify Ubuntu, Raspberry Pi, camera, and Hailo discovery.
2. List detected serial devices, preferring `/dev/serial/by-id/...` paths.
3. Ask for the TELEM2 device and baud rate.
4. Passively verify a checksum-valid MAVLink heartbeat when MAVSDK is not
   already running.
5. Ask for the Atlas Native ground-station address.
6. Offer to enable Hailo object detection when the runtime and model match.
7. Show the final configuration and services before applying changes.

For the pinned Hailo profile, confirm that the installation plan shows
`hailo (container)` perception and these services:

```text
atlas-mavsdk.service
atlas-agent.service
atlas-hailo-adapter.service
```

The generated configuration is stored at:

```text
/etc/atlas-agent/atlas-agent.env
```

## 6. Validate the installation

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

Inspect all service states:

```sh
systemctl --no-pager --full status \
  atlas-mavsdk.service \
  atlas-agent.service \
  atlas-hailo-adapter.service
```

Follow Atlas Agent logs:

```sh
journalctl -u atlas-agent.service -f
```

Follow Hailo adapter and inference logs:

```sh
journalctl -u atlas-hailo-adapter.service -f
```

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

