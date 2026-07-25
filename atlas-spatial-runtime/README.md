# Atlas Spatial Runtime

`atlas-spatial-runtime` is a small native service that owns one depth camera.
It acquires fresh calibrated depth, exposes bounded local health, and retains
the direct projection math needed by a future obstacle extractor.

It has no ROS dependency, Docker image, map, pose estimator, transform graph,
flight command, Agent stream, or Native UI contract.

## Data boundary

The provider returns one in-process `DepthFrame`:

| Field | Contract |
| --- | --- |
| Depth | `uint16` millimetres; zero means invalid |
| Timestamp | Capture time on the host monotonic clock |
| Frame | `oak_rgb_camera_optical_frame` by default |
| Calibration | Intrinsics for the exact aligned depth dimensions |

The runtime keeps the camera's compact native depth representation. Consumers
convert or project only the pixels they use; the service no longer expands
every frame into a ROS `32FC1` image.

`depthai` is the physical OAK provider. It runs stereo depth on the device,
aligns depth to the RGB optical frame, keeps only the newest queued frame, and
reads the aligned intrinsics from DepthAI frame metadata. `synthetic` implements
the same provider interface for software tests.

## Health contract

The Unix socket defaults to `/run/atlas-agent/spatial.sock`. Send:

```json
{"protocolVersion":"2","type":"probe"}
```

Readiness requires a fresh `16UC1` millimetre depth frame and matching valid
intrinsics. The response explicitly reports `scaleToMetres: 0.001`. It does not
claim obstacle observations yet.

The operator-facing health command on the Pi is:

```sh
sudo atlas-setup doctor
sudo journalctl -u atlas-spatial-runtime.service -n 200 --no-pager
```

`atlas-setup doctor` invokes the private Spatial diagnostics and includes their
specific failure in its combined report. The package keeps
`atlas-spatial-check` and `atlas-spatial-probe` under
`/opt/atlas-spatial-runtime/bin` as implementation details; they are not
installed as routine `/usr/bin` operator commands.

## Development

Run source tests directly:

```sh
./scripts/test-source.sh
```

Run a local synthetic process after installing the package into a virtual
environment:

```sh
python3 -m venv .venv
.venv/bin/pip install -e .
ATLAS_SPATIAL_PROVIDER=synthetic \
ATLAS_SPATIAL_SOCKET_PATH=/tmp/atlas-spatial.sock \
.venv/bin/atlas-spatial-runtime
```

DepthAI is an optional development dependency:

```sh
.venv/bin/pip install -e '.[depthai]'
```

## Package and Pi installation

Build and transfer Spatial from the development checkout:

```sh
./packaging/release.sh build 0.1.0
./packaging/release.sh transfer 0.1.0 mofe@ariadne-robot
```

The build command runs `scripts/test-source.sh`, resolves the declared
Linux-arm64 DepthAI/NumPy runtime, and creates:

```text
dist/atlas-spatial-runtime_0.1.0_arm64.deb
```

Install it on the landed, disarmed Pi together with the independently built
Agent package, then configure both:

```sh
cd /tmp
sudo apt install \
  ./atlas-agent_0.1.29_arm64.deb \
  ./atlas-spatial-runtime_0.1.0_arm64.deb
sudo atlas-setup
sudo atlas-setup doctor
```

The package is self-contained and does not access PyPI on the Pi. It installs
one private runtime at `/opt/atlas-spatial-runtime`, the udev rule, systemd
unit, and private diagnostic commands. Reinstalling or upgrading replaces that
runtime; it does not retain old Spatial artifacts or images. A source checkout
is not required on the aircraft.

Atlas Agent remains a separate package. `atlas-setup` writes
`/etc/atlas-agent/spatial.env` and enables the already-installed Spatial
service, but its Debian package does not contain or build Spatial.

Spatial loads the selected profile from
`/etc/atlas-agent/aircraft-profile.json`. The profile contains only its id,
the depth-camera device id, and the direct sensor-to-body mounting offset. The
current Ariadne source profile is
[`../aircraft-profiles/ariadne.json`](../aircraft-profiles/ariadne.json). See
[`../docs/spatial-runtime.md`](../docs/spatial-runtime.md) for the system
boundary and failure model.
