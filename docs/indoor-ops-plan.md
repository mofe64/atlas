# Indoor Operations Plan

**Status:** Single source of truth for the indoor mission  
**Scope:** Functional prototype on the current Ariadne aircraft and Atlas stack

## Goal

Atlas must let an operator start an **Indoor Explore** mission from Atlas
Native. The operator selects a flight altitude of **0.5 m, 1 m, or 2 m**. The
aircraft then explores the indoor space at that altitude, builds a point cloud,
and shows the cloud live in Atlas Native.

During the mission the aircraft must:

- avoid obstacles using the forward OAK-D Lite depth camera;
- keep the SIYI A8 gimbal video visible in Atlas Native;
- keep the existing detection and tracking pipeline running on that video;
- show its current position, travelled path, start point, and point cloud;
- accept an operator Abort command at any time; and
- on Abort, stop exploring and return to the recorded start point.

This is a focused prototype. It is not a production autonomy, certification,
fleet-management, or general-purpose robotics project.

## Operator experience in Atlas Native

The indoor mission screen needs only:

1. An **Indoor Explore** mission type.
2. An altitude selector containing exactly `0.5 m`, `1 m`, and `2 m`.
3. A **Start mission** control.
4. An **Abort and return** control that remains available throughout flight.
5. Mission state: `starting`, `taking_off`, `exploring`, `returning`,
   `complete`, `holding`, or `failed`.
6. A live 3D view containing the accumulated point cloud, aircraft position,
   start point, and travelled/return path.
7. The existing gimbal video view with the existing detection boxes, track IDs,
   and tracking controls.

Starting the mission records the aircraft's current PX4 local position as the
return point. The selected altitude is relative to that starting floor/height
reference; Atlas does not accept arbitrary altitude values in this mission.

## Minimal system design

```text
OAK-D Lite RGB-D + BMI270
    -> Spatial Runtime: live depth, VIO, bounded point cloud, obstacle cells
        -> Atlas Agent: exploration target, path, and one movement controller
            -> PX4: stabilization and local-position setpoints

H-Flow optical flow + range
    -> PX4 estimator: horizontal motion and height aiding

SIYI A8 video
    -> existing Hailo detection/tracking path
        -> Atlas Native video and overlays

Spatial Runtime + Agent mission state
    -> Atlas Native: live point cloud, path, aircraft pose, and controls
```

PX4 remains responsible for stabilizing the aircraft. H-Flow/PX4 local
position is the initial flight-control pose. OAK VIO places depth points in the
local map. At mission start Atlas records the relationship between those two
local frames so a path chosen in the map can be issued as PX4-local setpoints.
OAK VIO is not injected into PX4 in this first implementation.

Only one Atlas component may write movement setpoints. Existing mission,
Follow, or manual movement control must not run at the same time as Indoor
Explore.

## Mapping and exploration

The required mapping path is deliberately small:

1. Convert each aligned depth frame into local 3D points using the OAK camera
   calibration and the capture-time VIO pose.
2. Voxel-downsample and cap the accumulated cloud so memory use stays bounded.
3. Send the downsampled cloud and current aircraft pose to Atlas Native for the
   live 3D view.
4. Project nearby points through the selected flight-height band into a simple
   two-dimensional occupancy grid.
5. Inflate occupied cells by the aircraft radius plus a fixed clearance.
6. Select the nearest reachable boundary between known free space and unknown
   space as the next exploration target.
7. Find a path through known free cells with a simple grid search such as A*.
8. Turn the aircraft to face the path before translating, then move slowly and
   replan when fresh depth changes the free space.

No RTAB-Map, Nav2, OctoMap, global 3D planner, loop-closure system, or new SLAM
framework is required. The live VIO-local cloud plus a small occupancy grid is
enough for this prototype.

The OAK-D Lite looks forward. It cannot see the sides, rear, or space above the
aircraft. Indoor Explore must therefore scan by yawing, face the direction of
travel before moving, and never strafe or reverse into unobserved space.

## Obstacle behavior

Fresh depth in front of the aircraft is used both to mark occupied map cells
and as an immediate stopping boundary. When a new obstacle blocks the current
path, Atlas stops translation, keeps the aircraft in Hold while necessary,
chooses another reachable frontier, and continues only along known free space.

If Atlas cannot find a path, depth becomes unavailable, or local position is
lost, the aircraft remains in PX4 Hold. It must never continue blindly.

## Abort and return

Atlas retains the start point and a breadcrumb of the known-free path as the
aircraft explores.

On **Abort and return**:

1. Stop exploration and stop advancing the current path.
2. Command PX4 Hold.
3. Plan from the current position to the start through the known-free map,
   preferring the recorded breadcrumb where it remains clear.
4. Face each return segment before moving and keep applying the same forward
   obstacle checks.
5. Return to the start point and Hold there.

If the return path or local position is unavailable, the safe achievable
behavior is Hold and operator takeover; Atlas must report that it could not
complete the automatic return. “Abort” must never mean continuing the
exploration while a return is being worked out.

## Existing foundation to keep

### OAK-D Lite and Spatial Runtime

The current aircraft already has the necessary sensor foundation:

- A 2021 OAK-D Lite connected directly to the Raspberry Pi 5 over USB 3.
- Aligned `640x400` colour and metre-depth streams.
- A BMI270 IMU publishing at approximately 200 Hz.
- Live DepthAI/Basalt VIO on `/atlas/spatial/vio/odometry`.
- An approximately forward-facing, upright mount about `0.15 m` ahead of the
  aircraft centre. That measurement is approximate, so the existing transform
  remains `configured_unverified` rather than pretending to be precise.
- The working tree requests 20 Hz VIO visual input. The accepted `0.1.16`
  aircraft image used 30 Hz.

The currently accepted aircraft runtime uses the custom DepthAI
`3.6.1+atlas2` build. Retain it only as the known-working rollback while Atlas
qualifies the preferred end state: the standard upstream package. The custom
build contains two small mitigations for failures seen on the real Pi/OAK
combination:

- IMU samples are delivered to Basalt in strictly increasing timestamp order,
  preventing the estimator assertion that previously killed the camera
  component.
- The VIO image queue is non-blocking and keeps the latest frame, preventing a
  slower estimator from stalling the shared RGB-D stream.

These patches are not a mapping framework. They keep the current accepted
camera/VIO source running, but Atlas must not assume they are universally
required by OAK-D Lite or make a permanent private DepthAI fork part of the
product without the comparison below.

### Future work: standard DepthAI and direct-host evaluation

Atlas prefers the standard DepthAI implementation. The current evidence proves
that the custom build works in Atlas's restricted Pi container; it does not
prove that patching is the only correct solution. In particular, the USB
failure may be caused or amplified by the container/udev boundary, and the
final 20 Hz VIO configuration has not been qualified with the standard
package.

Run a controlled comparison on the same grounded Pi, OAK, cable, USB port, and
configuration:

1. standard DepthAI directly on the Pi under an independent systemd service;
2. standard DepthAI inside the current restricted container; and
3. the accepted `3.6.1+atlas2` container as the reference.

For each variant, retain repeated cold-start and service-restart results, OAK
firmware re-enumeration, USB 3 identity, uninterrupted RGB-D, raw BMI270
timestamp ordering, VIO cadence, deliberate estimator overload, resource use,
and restart counts. A direct-host service must preserve the same stable Atlas
topics, health socket, transform bundle, non-root account, and independence
from Agent/MAVSDK.

If standard DepthAI passes directly on the host, prefer that design and remove
the spatial container and private package in a separately approved cleanup. If
the standard package passes only after correcting container/udev integration,
keep container isolation but remove the private package. If neither standard
variant passes, report the minimal reproducer upstream and retain only the
smallest mitigation that remains necessary. This investigation can run in
parallel with Native visualization work, but it must finish before the indoor
movement feature is released for aircraft use.

### H-Flow and PX4

The Holybro H-Flow is mounted downward and connected to PX4 over DroneCAN. PX4
is configured to fuse its optical flow and range data. The current Ariadne
baseline is:

- `SENS_FLOW_ROT=0`
- flow offset `X=+0.045 m`, `Y=-0.050 m`, `Z=0 m`
- range offset `X=0 m`, `Y=0 m`, `Z=0 m`
- flow/range operating limits configured from `0.08 m` to `30 m`
- optical-flow publication configured at `70 Hz`
- `EKF2_OF_CTRL=1` and `EKF2_RNG_CTRL=1`

PX4 has already received and fused live H-Flow/range data while disarmed, and
Atlas Agent already exposes PX4 local position, odometry, flow, range, and
estimator health. The remaining practical gap is that GPS-denied position hold
has not yet been flown on this aircraft. This is a flight capability gap, not a
reason to repeat the completed OAK camera work.

### Atlas mission, video, and tracking paths

Atlas already has reusable foundations for mission commands and state,
acknowledged Hold/RTL/Land actions, exclusive movement authority, gimbal video,
Hailo detections, ByteTrack tracking, and Native video overlays. Indoor Explore
adds a new mission controller and point-cloud view; it should reuse those paths
instead of creating parallel command, video, or tracking systems.

## What still needs to be implemented

Implementation checkpoint (23 July 2026): stages 1 and 2 now provide a bounded live ROS
cloud built from aligned depth, camera intrinsics, and capture-time VIO poses.
It publishes `/atlas/spatial/map/points`, resets rather than mixing VIO
coordinate epochs, and caps the accumulated voxel store. A separate Unix
socket and gRPC stream now carry complete latest-only cloud snapshots into an
in-memory Native store, and the Indoor workspace renders them with React Three
Fiber beside the existing camera view. Pi/OAK end-to-end release acceptance
remains, and stage 3 is the next feature slice.

The transport does not downsample the current cloud. Every delivered update
contains all points currently held by the bounded map, up to 100,000 tightly
packed little-endian XYZ float32 triples. At the upper bound this is 1.2 MB per
snapshot, or about 2.4 MB/s at 2 Hz before gRPC framing. “Latest-only” means
that each asynchronous boundary has one replaceable whole-frame slot; a slow
consumer skips stale complete snapshots instead of accumulating a backlog or
receiving the map in pieces. Native renews a short view lease while the Indoor
workspace is mounted, so this bandwidth is not used when nobody is viewing
the cloud.

Build the feature in this order:

1. **Live cloud:** replace the offline PLY direction with a bounded live cloud
   built directly from depth and VIO.
2. **Native transport and view (implemented for live cloud and pose):** stream
   complete bounded cloud snapshots and current pose over an isolated spatial
   channel and render them in a React Three Fiber panel beside existing video.
   Path, start point, and mission state join this view with the mission
   contract rather than being fabricated before stage 3.
3. **Indoor mission contract:** add the three altitude choices, mission states,
   Start, and Abort-and-return messages between Native and Agent.
4. **Local navigation:** add the occupancy slice, obstacle inflation, frontier
   selection, grid path, yaw-first movement, and replanning.
5. **Return behavior:** retain the start/breadcrumb path and use it for the
   abort return.
6. **Concurrent payload operation:** keep the current A8 video,
   detection/tracking, and gimbal control alive while Indoor Explore owns
   aircraft movement.

In parallel with stages 2 and 3, run the standard-DepthAI/direct-host
evaluation above. It is not a prerequisite for implementing the Native view,
but its outcome controls the final spatial deployment and release packaging.

## Explicitly unnecessary for this feature

Do not make the following prerequisites for Indoor Explore:

- recording/replay sessions or evidence capture;
- offline PLY generation, deterministic artifact hashes, or tamper checks;
- PX4/VIO comparison reports;
- Foxglove or RViz as the user-facing point-cloud view;
- a split native/runtime Docker release flow, remote BuildKit cache, or CI
  release pipeline;
- RTAB-Map, Nav2, OctoMap, loop closure, or a general 3D autonomy stack;
- PX4 external-vision fusion in the first version;
- production certification, fleet-scale orchestration, or exhaustive
  acceptance paperwork.

The normal local build is sufficient. The heavy native DepthAI layer only
needs rebuilding when its pinned ROS/DepthAI/Basalt/libusb dependencies or the
two required DepthAI patches change. Normal Atlas Python, ROS launch, Agent, or
Native changes should reuse that layer.
