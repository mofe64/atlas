# Atlas Spatial Runtime

`atlas-spatial-runtime` is the vendor-neutral onboard RGB-D boundary for Atlas.
It owns the selected depth camera, normalizes its color/depth/calibration
streams, exposes bounded health over a Unix socket, and supports deterministic
record/replay fixtures. It does not authorize or command aircraft movement.

The stable logical source is `front-depth`. Hardware provenance remains
explicit through the configured provider (`depthai`, later `realsense`) and
device identifier, but Atlas consumers do not depend on vendor topic names.

## Runtime contract

The first slice publishes:

```text
/atlas/spatial/color/image_raw
/atlas/spatial/color/camera_info
/atlas/spatial/aligned_depth/image_rect
/atlas/spatial/aligned_depth/camera_info
```

Aligned depth is `32FC1` in metres. The health service listens at
`/run/atlas-agent/spatial.sock` by default. Send one newline-terminated JSON
request:

```json
{"protocolVersion":"1","type":"probe"}
```

The one-line JSON response identifies the logical source, provider provenance,
frame freshness, measured rates, synchronization skew, and calibration hash.

## Development

Build inside a ROS 2 Jazzy environment:

```sh
cd atlas-spatial-runtime/ros2_ws
source /opt/ros/jazzy/setup.sh
colcon build --symlink-install
source install/setup.sh
ros2 launch atlas_spatial_runtime spatial_runtime.launch.py provider:=synthetic
```

Then probe it:

```sh
atlas-spatial-probe --socket /run/atlas-agent/spatial.sock
```

The synthetic provider is the CI and replay-development path. The DepthAI
provider is isolated in `launch/providers/depthai.launch.py`; no generic Atlas
node imports the DepthAI SDK or vendor topic names.

## Pi deployment

The release container is built from `packaging/Dockerfile`. The Pi runs the
release-versioned image through `atlas-spatial-runtime.service`. The setup
helper first accepts an already installed or packaged image. Until a release
registry/image archive is configured, the interactive `atlas-setup` flow uses
`atlas-spatial-setup --build-local` as the one-click commissioning fallback and
builds the bundled context on the Pi.

See [`docs/spatial-runtime.md`](../docs/spatial-runtime.md) for the deployment,
failure-boundary, provider-swap, and next-slice mental model.
