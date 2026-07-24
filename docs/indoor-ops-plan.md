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
- Live, non-authoritative odometry on `/atlas/spatial/vio/odometry`.
- An approximately forward-facing, upright mount about `0.15 m` ahead of the
  aircraft centre. That measurement is approximate, so the existing transform
  remains `configured_unverified` rather than pretending to be precise.
- The working tree caps external global-shutter stereo odometry input at
  20 Hz. The accepted `0.1.16` Basalt image used 30 Hz visual input.

The accepted `0.1.16` aircraft runtime uses the custom DepthAI
`3.6.1+atlas2` build. It remains the known-working rollback while the current
source qualifies the preferred end state: the standard upstream package. The
custom build contains two small mitigations for failures seen on the real
Pi/OAK combination:

- IMU samples are delivered to Basalt in strictly increasing timestamp order,
  preventing the estimator assertion that previously killed the camera
  component.
- The VIO image queue is non-blocking and keeps the latest frame, preventing a
  slower estimator from stalling the shared RGB-D stream.

These patches are not a mapping framework. They keep the current accepted
camera/VIO source running, but Atlas must not assume they are universally
required by OAK-D Lite or make a permanent private DepthAI fork part of the
product without the comparison below.

### In progress: standard DepthAI qualification

Atlas now defaults its normal image build to the unmodified ROS Jazzy DepthAI
package. Integrated Basalt is disabled: DepthAI owns RGB-D, rectified
global-shutter mono, and raw-IMU transport; Madgwick supplies orientation; and
RTAB-Map owns bounded stereo-inertial odometry. An Atlas-owned input gate drops
duplicate and short out-of-order IMU stamps before Madgwick; a true clock reset
restarts the complete provider boundary. DepthAI itself remains unmodified.
The container mounts the host udev database read-only to give standard libusb
the host device state without granting privileged access.

A grounded Pi comparison on 2026-07-23 isolated the firmware re-enumeration
failure to the container network namespace. With `/dev/bus/usb` and
`/run/udev` mounted but `--network none`, standard libusb discovered the
unbooted OAK, uploaded firmware, and then failed with
`X_LINK_DEVICE_NOT_FOUND`. The same immutable `0.1.18` image, user, mounts,
capability set, and read-only filesystem succeeded with `--network host`:
the OAK reappeared, RGB-D/IMU reached the external odometry pipeline, and the
Atlas health and cloud sockets listened. The production runner therefore
shares the host network namespace for udev/netlink hotplug delivery while
retaining non-root execution, no capabilities, a read-only root filesystem,
and USB character-device access only. `ROS_LOCALHOST_ONLY=1` keeps ROS traffic
host-local. This proves the standard-package re-enumeration path, but the full
runtime is not accepted for flight until the remaining grounded qualification
below passes.

Finish the controlled qualification on the same grounded Pi, OAK, cable, USB
port, and configuration:

1. the new standard DepthAI plus external-odometry image inside the production
   container using the required host network namespace;
2. the accepted `3.6.1+atlas2` container as the rollback reference.

For both variants, retain repeated cold-start and service-restart results, OAK
firmware re-enumeration, USB 3 identity, uninterrupted RGB-D, raw BMI270
timestamp ordering, odometry cadence, deliberate estimator overload, resource
use, and restart counts. Any future direct-host experiment must preserve the
same stable Atlas topics, health socket, transform bundle, non-root account,
and independence from Agent/MAVSDK.

The standard package has now passed the re-enumeration boundary after
correcting the container's udev/netlink integration, so retain the container
and finish repeated cold-start, restart, sustained-stream, odometry-quality,
resource, and Native cloud qualification. Direct-on-Pi DepthAI remains future
work for comparing operational simplicity and isolation; it is no longer
needed to explain this failure. Removal of the retained private package and
patched build stages remains a separately approved cleanup after the standard
path passes the complete grounded/aircraft acceptance. If later qualification
exposes a separate upstream failure, report its minimal reproducer and retain
only the smallest mitigation demonstrably required. This investigation can
run in parallel with Native visualization work, but it must finish before the
indoor movement feature is released for aircraft use.

The first packaged `0.1.19` restart test also exposed a separate service
lifecycle bug: restarting `atlas-agent.service` removed its systemd-managed
`/run/atlas-agent` directory while the independent spatial processes still had
health and cloud sockets bound there. The kernel listeners survived, but their
filesystem names disappeared and new clients received `ENOENT`. The Agent unit
now preserves the shared runtime directory across both stops and restarts;
each socket owner remains responsible for replacing its own stale socket on
startup. Qualification must explicitly restart Agent while the spatial runtime
continues and prove that both spatial socket paths remain reachable.

The same `0.1.19` qualification then showed healthy synchronized RGB-D/IMU and
odometry but no `/atlas/spatial/map/points` message. Health reported the exact
unchanged legacy transform hash
`sha256:30a90b90711af18a0bd5de3c0a2800aeb057f2ba1f59925151cc7179cd3c9304`,
whose v2 bundle predates the `oak_rgb_camera_optical_frame` edge required to
project aligned depth into `oak_mount`. Setup had correctly avoided
overwriting arbitrary existing geometry, but made the known seed migration
too easy to miss. Setup now canonicalizes an existing bundle, automatically
migrates only that exact legacy hash, saves the original beside it, and
preserves every modified or commissioned bundle.

After the guarded migration on the grounded Pi, the packaged `0.1.19` runtime
reported synchronized `640x400` colour and metre depth, a healthy approximately
224 Hz raw IMU with zero timestamp anomalies, the booted OAK at USB 3
5000 Mb/s, and the expected v3 transform hash
`sha256:62ed08cdbdeab32df4e8d61c91e034ec720f94e60f021f5e2a2891cbf8e0f517`.
The live-cloud topic then emitted a complete 3,702-point snapshot. This passes
the real-device DepthAI-to-ROS cloud boundary; Agent-to-Native delivery,
tracking quality during deliberate grounded motion, repeated lifecycle tests,
and aircraft acceptance remain.

The first Agent-to-Native attempt reached Native's spatial gRPC service but was
rejected with `spatial pose quaternion must be normalized`. The complete cloud
was valid; the independently sampled, optional odometry pose was not. The
runtime stream boundary now omits pose whenever VIO has no valid finite unit
quaternion and clears any previously cached pose rather than attaching stale
metadata. Atlas Agent independently normalizes a near-unit pose and discards
invalid optional pose metadata without discarding the complete map-frame
cloud. Native remains strict, so malformed orientation data is never presented
as authoritative. The repeated `unsupported perception runtime protocol "1"`
messages observed in the same log belong to the separate Hailo perception
adapter and are not part of the spatial cloud protocol.

The following `0.1.20` grounded run proved that the effective DepthAI profile
already aligned depth to CAM_A and gave RGB and stereo equal device timestamps,
but RTAB-Map remained at quality zero with repeated insufficient-inlier
failures. The first process start did not produce a map because only null
odometry poses were produced.
The provider profile now explicitly preserves alignment/synchronization and
sets Luxonis's RTAB-Map `DEFAULT` preset on the driver's RealSense-compatible
`depth` namespace instead of leaving the observed implicit `FAST_ACCURACY`
preset active. VIO health now rejects null/non-unit pose quaternions,
does not count them as valid samples, and reports visual tracking loss instead
of hiding it behind the separate unverified-extrinsics warning. Qualification
must still prove sustained nonzero odometry quality and a continuously
updating map in a controlled textured scene; the inlier threshold remains
unchanged.

The active `DEFAULT` hotfix then produced a 516-point map; a clean runtime
restart produced a new 670-point map. Both maps froze while the provider kept
delivering fresh RGB-D and approximately 241 Hz IMU and RTAB-Map reported
quality zero continuously. A direct OAK input probe found 1,000 ORB features
per sampled RGB frame, strong still-frame sharpness and contrast, 54.9% valid
depth, and valid depth at 860 of those features. This rules out Native
transport, a stale pre-existing map, and simple lack of still-frame texture as
the primary cause. It does not validate geometry during movement: the OAK-D
Lite's IMX214 colour sensor is rolling-shutter, and the effective RGB-D rate on
the Pi was only approximately 3–6 Hz. The standard runtime now publishes the
rectified OV7251 global-shutter mono pair at 20 Hz and uses standard RTAB-Map
`stereo_odometry`; aligned RGB-D remains a separate input for cloud geometry.

RTAB-Map automatic odometry reset is disabled because a silent reset would
change the coordinate frame underneath the pose buffer and accumulated cloud.
Null odometry makes tracking loss observable. After the startup grace, five
seconds of sustained missing, stale, or invalid odometry terminates the whole
spatial runtime so systemd restarts the estimator, cloud, pose buffer, and
stream epoch as one coordinate boundary.

### Qualified grounded: release 0.1.25

Release `0.1.25` passed the controlled disarmed movement and lifecycle
qualification on the aircraft on 2026-07-24. The Pi ran package `0.1.25` from
immutable image
`sha256:28edec1c5ef969d6ed5eb2e49f972ab318a3f6cbabae158e0f057ff41c313670`;
the standard RTAB-Map `stereo_odometry` process was present and
`rgbd_odometry` was absent.

During an exact 35.013-second slow hand carry through a textured room:

- rectified left and right mono published at `19.901 Hz` and `19.817 Hz`;
- 99.4872% of stereo frames paired, with `-0.026918 ms` median skew and a
  positive `0.0747062841 m` right-camera baseline;
- filtered IMU published at `223.68 Hz`, all recorded sensor headers were
  monotonic, and all nine required static transforms were present;
- all 135 movement-window odometry samples were valid, with zero tracking-loss
  or zero-inlier samples and 25–652 inliers;
- tracked distance advanced by `6.424 m`, 65 keyframes were added, and the
  local odometry map grew from 896 to its configured 2,000-feature bound;
- the ROS map produced 30 messages across 33.040 seconds with ten capture-time
  advances and no invalid odometry messages;
- the complete-cloud stream produced 29 frames across 34.185 seconds, advanced
  its sequence 28 times, retained valid poses, and reported no error; and
- the spatial service restart count remained `6` before and after the
  movement window.

The ROS and Native-facing clouds had already reached the configured
100,000-point bound, so a changing point count was no longer a valid freshness
test. Freshness was instead proven by nondecreasing capture times with repeated
advancement, strictly advancing complete-cloud sequences, added keyframes, and
local-map growth. After the movement window ended and the aircraft was set
down, visual tracking later became invalid and the configured health boundary
performed one whole-runtime restart. That post-window restart recovered to a
ready, synchronized service and is not counted as a movement-window failure.

The same release then passed the required lifecycle checks. A manual spatial
restart re-enumerated the OAK at USB 3 / 5000 Mb/s, returned ready synchronized
health with valid odometry, retained the v3 transform hash, and started the
same immutable image with standard stereo odometry. Restarting
`atlas-agent.service` did not restart the spatial container and preserved both
spatial socket paths and their exact inodes. The preceding battery replacement
also created a new kernel boot ID, proving a real cold start; the misleading
old `who -b` time came from the Pi clock being corrected after boot.

The initial Native message, “CLOUD NOT AVAILABLE YET,” was a separate ground
link outage: the Mac had no active `192.168.144.50` Ethernet interface and
Native was not listening on port 7443. Once the HM30 Ethernet path was restored
and Native started, Agent registered successfully. In a synchronized
eight-second end-to-end window, the runtime produced 13 advancing complete
100,000-point snapshots while Native TCP-acknowledged 4.43 MB of the 4.50 MB
sent. This distinguishes a Native transport outage from a frozen ROS map.

This evidence passes the grounded standard-DepthAI spatial-runtime gate without
lowering the odometry inlier threshold or restoring patched Basalt. Retain
`0.1.16` as the operational rollback and keep the commissioned transforms
marked `configured_unverified`; Indoor Explore still has no aircraft movement
authority until its navigation and flight gates are implemented.
The retained evidence is in
`.scratch/pi-evidence-0.1.25-replay-movement-20260724T121316Z` and
`.scratch/pi-evidence-0.1.25-lifecycle-qualification-20260724T122443Z`;
the 632 MB MCAP remains on the Pi under
`/home/mofe/atlas-0.1.25-replay-movement-20260724T121316Z/rosbag`.

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

### Stage 3 contract qualified on 0.1.26, hold-only

The Native/Agent Indoor Explore contract is implemented, packaged, and
qualified on the disarmed aircraft in release `0.1.26`. The matched release
was built from clean commit
`df1649764694ab7855ea5a93d6c88ef3fb9f6789`, transferred over the normal Wi-Fi
route to `mofe@ariadne-robot`, verified on the Pi, and installed from immutable
spatial image
`sha256:bb62bef5f359690a77d3b3f511e39f41180714b0699a9cbda851f913c6bf50a8`.
Setup preserved the aircraft identity and v3 transforms and persisted that
exact image ID rather than the mutable `0.1.26` tag.

The protobuf carries dedicated `START` and `ABORT_AND_RETURN` operations and
the explicit `starting`, `taking_off`, `exploring`, `returning`, `complete`,
`holding`, and `failed` states. Atlas Native exposes exactly `0.5 m`, `1 m`,
and `2 m`; arbitrary altitude input is not present.

This slice deliberately has no aircraft movement authority. Agent advertises
`indoor_explore:contract:v1` together with
`indoor_explore:movement_authority:false`. Start records the contract identity
and selected altitude, invokes the existing acknowledged PX4 Hold action, and
reports `holding` with `LOCAL_NAVIGATION_NOT_COMMISSIONED`. Abort invokes Hold
again and reports `holding` with `RETURN_CONTROLLER_NOT_COMMISSIONED`. A main
session loss also invokes Hold onboard. The implementation never reports
takeoff, exploration, return, or completion before those controllers exist.

Native keeps the current contract snapshot in memory, validates Agent state
transitions and immutable altitude, blocks a second non-terminal mission, and
will recover an unknown post-restart mission only from a safe `holding` or
`failed` Agent report. The Indoor controls remain disabled when the connected
Agent does not advertise contract v1.

Local validation passed before packaging:

- Agent vehicle, main-session transport, and executable tests;
- Native's complete 98-test library suite: 95 passed and the three
  flight-dependent SITL tests remained intentionally ignored;
- a real localhost gRPC round trip from Native Start delivery through
  Agent-format `starting`/`holding` updates into the Native store; and
- the TypeScript/Vite production build using Node `24.14.0`.

Live grounded qualification then passed:

- Native displayed a complete 100,000-point cloud and exposed only the three
  fixed altitude choices.
- A `1 m` Start transitioned from `starting` to `holding` with
  `LOCAL_NAVIGATION_NOT_COMMISSIONED`; Abort remained available and returned
  `holding` with `RETURN_CONTROLLER_NOT_COMMISSIONED`.
- Restarting Agent left Native in the explicit fail-safe
  `holding / AGENT_SESSION_LOST` state. The aircraft stayed disarmed and on the
  ground.
- Restarting the spatial runtime replaced cloud epoch `301eb15b…6c49a` with
  `59e2bbcf…dc72e`; the new complete-cloud sequence advanced from 10 to 21
  instead of retaining a frozen pre-restart snapshot.
- A fresh `0.5 m` mission recovered after a Native restart only as
  `holding / GROUND_LINK_LOST`, proving the restricted recovery rule. A final
  Abort returned `holding / RETURN_CONTROLLER_NOT_COMMISSIONED`.
- All four services were active with zero automatic restarts after the
  qualification.

The first full doctor probe after the manual spatial restart briefly sampled
the separate RGB-versus-aligned-depth health window at approximately
`8.41/9.54 Hz` and `99.993 ms` skew. This did not coincide with a stereo/VIO
failure: a direct 20-second ROS probe measured rectified mono at
`20.002/19.885 Hz`, approximately `-0.028 ms` stereo skew, valid CameraInfo and
TF, filtered IMU at `215.56 Hz`, valid odometry, and standard
`stereo_odometry` quality of 260–594 with no zero-quality samples in the
retained log. Thirty consecutive health probes then reported ready with zero
RGB-D skew, and the final full doctor passed. This is retained as a transient
health-window incident rather than hidden or used to lower a threshold.

The physical cold start also exposed a separate Agent navigation-clock defect
in installed release `0.1.26`: if the Pi wall clock steps forward after boot,
the minimum-offset aligner can leave PX4 samples appearing stale until Agent
restarts. The subsequent Agent restart restored millisecond-scale alignment
and ready navigation.

The next-release source now preserves Go's monotonic receive timestamp,
compares monotonic elapsed time with wall-clock elapsed time, and starts a new
alignment epoch when those clocks differ by more than `250 ms`, below the
shortest `500 ms` navigation freshness deadline. Regression tests reproduce
both the observed `+68 s` wall-clock step and a sub-second step, while a
separate five-second delivery-delay test proves an old packet is not
incorrectly made fresh. Setup now also renders the package manifest version as
`ATLAS_AGENT_VERSION`. These fixes pass the complete Agent test suite, vet, and
targeted race detection locally, but they are not installed on the Pi yet.
Stage 3 remains safe because movement authority is false; stage 4 remains
blocked until a matched release is deployed and a physical cold start proves
ready navigation without an Agent restart.

Evidence is retained under
`.scratch/pi-evidence-0.1.26-stage3-qualification-20260724T150532Z`.

## What still needs to be implemented

Implementation checkpoint (24 July 2026): stages 1 and 2 now provide a bounded
live ROS cloud built from aligned depth, camera intrinsics, and capture-time
VIO poses. It publishes `/atlas/spatial/map/points`, resets rather than mixing
VIO coordinate epochs, and caps the accumulated voxel store. A separate Unix
socket and gRPC stream now carry complete latest-only cloud snapshots into an
in-memory Native store, and the Indoor workspace renders them with React Three
Fiber beside the existing camera view. Grounded Pi/OAK and Native end-to-end
acceptance passed on `0.1.25`; the matched `0.1.26` aircraft release qualifies
the explicit hold-only Native/Agent contract described above. Flight-enabling
acceptance and stages 4–6 remain. The clock/setup follow-up described above is
implemented locally but still requires matched packaging and cold-start
acceptance before stage 4. The first Pi `0.1.17` deployment exposed two release
blockers corrected for `0.1.18`: the stream node no longer shadows
`rclpy.Node` client state, and the
DepthAI provider normalizes the observed vendor frame name to the Atlas-owned
aligned-depth frame used by the transform bundle. Essential cloud processes
now terminate the launch when they exit so systemd cannot mask this class of
failure as an active service.

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
3. **Indoor mission contract (qualified on the aircraft, hold-only):** the
   three altitude choices, explicit mission states, Start, Abort-and-return,
   and safe Agent/Native restart recovery now cross the live Native/Agent
   boundary. Movement remains disabled.
4. **Local navigation:** add the occupancy slice, obstacle inflation, frontier
   selection, grid path, yaw-first movement, and replanning.
5. **Return behavior:** retain the start/breadcrumb path and use it for the
   abort return.
6. **Concurrent payload operation:** keep the current A8 video,
   detection/tracking, and gimbal control alive while Indoor Explore owns
   aircraft movement.

The standard DepthAI container path is now the qualified deployment direction.
A direct-host DepthAI comparison remains optional future work for operational
simplicity; it is not a prerequisite for stage 4 and must not displace the
accepted standard container or the retained `0.1.16` rollback.

## Explicitly unnecessary for this feature

Do not make the following prerequisites for Indoor Explore:

- recording/replay sessions or evidence capture;
- offline PLY generation, deterministic artifact hashes, or tamper checks;
- PX4/VIO comparison reports;
- Foxglove or RViz as the user-facing point-cloud view;
- a split native/runtime Docker release flow, remote BuildKit cache, or CI
  release pipeline;
- RTAB-Map SLAM, Nav2, OctoMap, loop closure, or a general 3D autonomy stack;
- PX4 external-vision fusion in the first version;
- production certification, fleet-scale orchestration, or exhaustive
  acceptance paperwork.

The normal local build is sufficient. The standard ROS/DepthAI/RTAB-Map package
layer appears before Atlas source, so normal Atlas Python, ROS launch, Agent,
or Native changes should reuse it. The retained patched DepthAI stages are not
part of a normal build and exist only for the qualification rollback.
