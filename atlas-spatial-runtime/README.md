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

## Standard DepthAI boundary

The normal image installs the unmodified ROS Jazzy DepthAI packages. DepthAI
owns synchronized RGB-D, a rectified global-shutter mono pair, and raw BMI270
transport; its integrated Basalt VIO is disabled. Atlas runs standard
`imu_filter_madgwick` and RTAB-Map stereo odometry as separate processes and
publishes the result on the existing
non-authoritative `/atlas/spatial/vio/odometry` compatibility topic.
The camera profile explicitly aligns and synchronizes RGB/stereo and sets the
Luxonis RTAB-Map `DEFAULT` preset on the RealSense-compatible `depth`
namespace, so cloud projection receives useful registered depth rather than
silently retaining the driver's implicit `FAST_ACCURACY` profile. Visual
odometry independently uses the rectified mono pair.

This boundary removes the reasons Atlas previously patched DepthAI:

1. An Atlas-owned timestamp gate drops duplicate and short out-of-order raw
   IMU samples before Madgwick, without rewriting device time. A one-second
   clock regression terminates the provider boundary so Madgwick, odometry,
   and the camera restart in the same clock epoch.
2. RTAB-Map keeps only the most recent unprocessed stereo observation, so
   estimator overload cannot block the DepthAI camera component.
3. The container mounts the host udev database read-only alongside
   `/dev/bus/usb` and shares the host network namespace. Standard libusb's
   udev monitor depends on host netlink hotplug delivery: a grounded Pi test
   proved that `--network none` loses the booted OAK after firmware upload,
   while `--network host` re-discovers it and produces RGB-D/IMU input without
   a patched DepthAI or libusb. The container remains non-root, read-only,
   capability-free, and limited to the USB character-device class.

The accepted `0.1.16` image remains the operational rollback. Its
`3.6.1-2noble+atlas2` build stage and source files are retained but are not in
the normal image dependency graph; they must not be deleted until the standard
path passes aircraft tests and cleanup is explicitly approved.

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

The normal image is built from `packaging/Dockerfile` and defaults to the
`atlas-standard-depthai` base. The retained patched stages are skipped by
BuildKit during a normal build. Atlas source is copied only after the
third-party runtime layers, preserving cache reuse without a second release
flow.

The Pi runs the image through `atlas-spatial-runtime.service`. See
[`docs/spatial-runtime.md`](../docs/spatial-runtime.md) for the system boundary
and [`docs/indoor-ops-plan.md`](../docs/indoor-ops-plan.md) for feature order.
