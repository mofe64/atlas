# Atlas Agent Installation Guide

This guide covers both a clean installation and an upgrade of the packaged
Atlas Agent with systemd-managed MAVSDK and Hailo perception services.

## Supported onboard computer

- Raspberry Pi 5 running Ubuntu 24.04 arm64
- Raspberry Pi AI HAT+ with Hailo-8L
- SIYI A8 camera reachable over its onboard Ethernet network
- PX4 flight controller connected through a stable serial device

The commands use `0.1.0` as an example release. Replace that value with the
release being deployed.

## Initial installation overview

An initial installation has five stages:

1. Build and transfer the new Debian package.
2. Install the package on the onboard computer.
3. Configure the pinned Hailo profile or deliberately retain a compatible
   native Hailo runtime.
4. Run the interactive Atlas configuration.
5. Validate the complete installation.

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

## 2. Install the packaged Atlas Agent

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
export ATLAS_RELEASE_VERSION=0.1.1
```

For a development replacement that deliberately keeps the installed version:

```sh
export ATLAS_RELEASE_VERSION=0.1.0
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
sha256sum -c "dist/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.deb.sha256"
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
export ATLAS_RELEASE_VERSION=0.1.1  # Use the exact version selected for this build.
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
  atlas-hailo-adapter.service \
  atlas-agent.service \
  atlas-mavsdk.service
```

Confirm they are inactive before continuing:

```sh
systemctl is-active atlas-hailo-adapter.service || true
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
grep '^ATLAS_AGENT_VERSION=' /etc/atlas-agent/atlas-agent.env
```

Verify that `/usr/bin/atlas-agent` exactly matches the binary from the
transferred package. This is mandatory for a same-version replacement:

```sh
EXPECTED_AGENT_SHA256="$(cat "/tmp/atlas-agent_${ATLAS_RELEASE_VERSION}_arm64.binary.sha256")"
printf '%s  %s\n' "$EXPECTED_AGENT_SHA256" /usr/bin/atlas-agent \
  | sha256sum -c -
```

Run the complete diagnostic and inspect the services:

```sh
sudo atlas-setup doctor
systemctl --no-pager --full status \
  atlas-mavsdk.service \
  atlas-agent.service \
  atlas-hailo-adapter.service
journalctl -u atlas-agent.service -n 200 --no-pager
```

In Atlas Native, confirm that the same drone identity reconnects, telemetry is
fresh, and the expected Agent capabilities are present. When perception or
payload code changed, also verify the Live workspace, detection frame lease,
gimbal discovery, one small inspection movement, and safe control release while
the aircraft remains disarmed and on the ground.

### 11. Roll back a failed upgrade

Keep the aircraft grounded and stop the three services before rollback. Install
the retained previous package:

```sh
sudo systemctl stop \
  atlas-hailo-adapter.service \
  atlas-agent.service \
  atlas-mavsdk.service
```

When rolling back to an older Debian version:

```sh
export ATLAS_PREVIOUS_VERSION=0.1.0
sudo apt install --allow-downgrades \
  "/tmp/atlas-agent_${ATLAS_PREVIOUS_VERSION}_arm64.deb"
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
```

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
