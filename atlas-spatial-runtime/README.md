# Atlas Spatial Runtime

`atlas-spatial-runtime` is the vendor-neutral onboard RGB-D boundary for Atlas.
It owns the selected depth camera, publishes normalized live sensor topics,
tracks bounded health, and builds the bounded VIO-local cloud used by Indoor
Explore. It contains no PX4 setpoint or movement API.

## Runtime contract

The live boundary publishes:

```text
/atlas/spatial/color/image_raw
/atlas/spatial/color/camera_info
/atlas/spatial/aligned_depth/image_rect
/atlas/spatial/aligned_depth/camera_info
/atlas/spatial/imu/data
/atlas/spatial/vio/odometry
/atlas/spatial/map/points
```

Aligned depth is `32FC1` in metres. `/atlas/spatial/map/points` is a bounded,
voxel-downsampled `PointCloud2` in the current VIO-local frame. A VIO timestamp
regression or frame change resets the cloud rather than mixing coordinate
epochs.

The health service listens on `/run/atlas-agent/spatial.sock` by default. Send:

```json
{"protocolVersion":"1","type":"probe"}
```

The response reports source/provider identity, RGB-D/IMU freshness and rates,
calibration and transform identity, USB transport, and direct VIO health. VIO
is used for live mapping but remains non-authoritative: PX4 VIO fusion and
spatial-runtime movement authority are always disabled.

## Why DepthAI is patched

The stock ROS Jazzy DepthAI 3.6.1 package fails on the deployed Raspberry Pi 5
and 2021 OAK-D Lite in three observed ways:

1. Its Ubuntu udev-backed libusb can lose the OAK while firmware changes the
   USB identity. Atlas uses DepthAI's pinned libusb revision with the netlink
   backend so the container follows the device through re-enumeration.
2. BMI270 packets can reach Basalt with duplicate or regressive timestamps.
   Basalt requires strictly increasing IMU time and otherwise aborts the
   camera component. Atlas serializes callbacks, sorts each packet batch, and
   drops late samples without inventing timestamps.
3. Basalt's bounded visual queue uses a blocking push. When the Pi cannot keep
   up, that blocks the shared RGB-D pipeline. Atlas makes the queue
   non-blocking and latest-frame-wins, discarding obsolete estimator work while
   keeping RGB-D live.

These are narrow patches to the validated `3.6.1-2noble+atlas2` core, not a
forked mapping stack. The image build checks source hashes, runs both queue
tests, marks the Debian revision with `+atlas2`, verifies both patch markers in
the built library, and confirms the private libusb is actually linked.

## Development

Inside ROS 2 Jazzy:

```sh
cd atlas-spatial-runtime/ros2_ws
source /opt/ros/jazzy/setup.sh
colcon build --symlink-install
source install/setup.sh
ros2 launch atlas_spatial_runtime spatial_runtime.launch.py provider:=synthetic
```

The synthetic provider publishes RGB-D, IMU, and VIO with the same stable
topic/frame contract as the DepthAI provider. It should produce a live cloud on
`/atlas/spatial/map/points`.

The packaged transform bundle is seeded once at
`/var/lib/atlas-agent/spatial/transforms.v1.json`. The Ariadne OAK mount remains
`configured_unverified`: approximately 0.15 m forward, upright, and centred.
Setup never overwrites a commissioned replacement.

## Pi deployment

The normal image is built from `packaging/Dockerfile`. The expensive pinned
DepthAI/Basalt/libusb stages appear before Atlas source is copied, so ordinary
Python or launch changes reuse Docker's layer cache without a separate native
image or release flow.

The Pi runs the image through `atlas-spatial-runtime.service`. See
[`docs/spatial-runtime.md`](../docs/spatial-runtime.md) for the system boundary
and [`docs/indoor-ops-plan.md`](../docs/indoor-ops-plan.md) for feature order.
