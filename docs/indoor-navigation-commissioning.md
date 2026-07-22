# Indoor Navigation Sensor Commissioning

**Status:** Evidence register and acceptance runbook; not a navigation-readiness
claim  
**Last updated:** 22 July 2026

## Purpose and authority boundary

This document records the installed OAK-D Lite and Holybro H-Flow baseline for
the bounded 2.5D indoor-navigation roadmap. It separates four states that must
not be conflated:

1. Hardware is physically installed.
2. A driver or PX4 parameter set is configured.
3. Sensor and estimator behavior has measured commissioning evidence.
4. Atlas is authorized to use that state for mapping or aircraft movement.

Only the OAK RGB-D contract has completed its current sensor acceptance. H-Flow
is installed and configured through QGroundControl, but its retained firmware,
parameter, estimator, GPS-denied flight, and failure-response evidence remains
incomplete. Neither device currently authorizes indoor autonomous movement.

## Evidence status vocabulary

| Status | Meaning |
| --- | --- |
| `ACCEPTED` | The named behavior has retained, repeatable acceptance evidence. |
| `CONFIGURED` | Hardware and initial software configuration are reported complete, but acceptance evidence is incomplete. |
| `PENDING` | The required artifact or test has not been captured. |
| `NOT_IMPLEMENTED` | Atlas has no current software path for the capability. |

## Accepted Atlas and OAK baseline

The following values were captured during the 21 July 2026 production
deployment to `ariadne-robot`:

| Evidence | Recorded value | Status |
| --- | --- | --- |
| Atlas Agent package | `0.1.8` arm64 | `ACCEPTED` |
| Atlas source revision | `0fa37ec` (`atlas spatial fix for luxonis deps`) | `ACCEPTED` |
| Spatial container image | `sha256:c50073b6765af9c0aba7ada297f71f5d936baddece937d1b636df5bc19572512` | `ACCEPTED` |
| OAK model | 2021 OAK-D Lite / runtime model `OAK-D-LITE` | `ACCEPTED` |
| OAK MXID | `19443010F122147E00` | `ACCEPTED` |
| USB transport | Direct Raspberry Pi 5 USB 3, `UsbSpeed.SUPER`, 5000 Mb/s | `ACCEPTED` |
| Camera inventory | Three camera sockets plus discovered BMI270 | `ACCEPTED` as hardware inventory |
| Spatial calibration hash | `sha256:66d9961eaba0ba63bdcc225d7aae453cf6a4c0efcb9df2582b1f91ad69bea8de` | `ACCEPTED` for contract-v1 RGB-D calibration identity |
| Live stream | 640x400 `bgr8` color plus aligned `32FC1` metre depth | `ACCEPTED` |
| Observed runtime rate | Approximately 16-21 fps in final doctor/probe samples | `ACCEPTED` as deployment observation, not a fleet-wide minimum |
| Reported RGB/depth skew | 0.0 ms in final production probe | `ACCEPTED` for the recorded run |
| DepthAI packages | Core 3.6.1 and ROS driver 3.2.1 | `ACCEPTED` |
| Runtime confinement | Read-only root filesystem, unprivileged, all Linux capabilities dropped | `ACCEPTED` |
| Spatial health | `ready=true`, fresh synchronized streams, calibration present, no spatial doctor failure | `ACCEPTED` |

The BMI270 inventory does not make VIO accepted. Spatial contract v1 does not
publish IMU samples and correctly reports `capabilities.imu=false`.

## H-Flow and PX4 commissioning state

The repeatable installation, configuration, and verification procedure is
documented in
[H-Flow PX4 Setup and Verification](h-flow-px4-setup-and-verification.md). This
document remains the evidence register for the installed aircraft.

The installation/configuration facts were confirmed by the operator on 21 July
2026. PX4 identity, DroneCAN status, and the corrected parameter export were
captured on 22 July 2026.

| Evidence | Recorded value | Status |
| --- | --- | --- |
| H-Flow physical installation | Installed downward on the aircraft | `CONFIGURED` |
| DroneCAN/PX4 setup | Required H-Flow flow/range and estimator parameters configured through QGroundControl | `CONFIGURED` |
| PX4 firmware identity | PX4 FMU V6C, release 1.17.0 stable, Git `d6f12ad1c4f70ad3230afd7d86e971421e02fef4`, NuttX 11.0.0, build 13 May 2026 18:50:30 | `ACCEPTED` as retained identity |
| H-Flow firmware identity | Not yet captured in this repository or commissioning record | `PENDING` |
| Complete PX4 parameter export | `params_after_h-flow_valid_params_fix_22_07_26.params`, SHA-256 `76c1eaff4c55f9248b04fe85344638b22b0c10b20ff0620dcbaa9e29cb9ff951` | `ACCEPTED` as configuration artifact |
| Mount orientation and body-frame X/Y/Z offsets | Configured rotation `SENS_FLOW_ROT=0`; optical-flow offset X `+0.045 m`, Y `-0.050 m`, Z `0 m`; range offset X/Y/Z `0 m` | `CONFIGURED`; physical measurement and motion-sign validation pending |
| Live flow/range message evidence | Clean 72.646 s ULog `log_145_2026-7-22-09-30-46.ulg`, SHA-256 `6e4a7eb30bdb581b4e4a6dcfd400c5d79c6d91f82cfb37f40d1ac4feb43011bb` | `ACCEPTED` as disarmed bench/hand-motion evidence |
| EKF optical-flow/range fusion and innovation evidence | Both EKF instances report optical-flow and range-height fusion; detailed observations below | `ACCEPTED` as disarmed estimator evidence; flight acceptance pending |
| GPS-denied position-hold drift and height acceptance | Not yet run or retained | `PENDING` |
| Sensor-loss, estimator-degradation, Hold, and manual-takeover acceptance | Not yet run or retained | `PENDING` |
| Atlas H-Flow discovery and estimator-health reporting | No current implementation | `NOT_IMPLEMENTED` |

QGroundControl configuration closes the setup step, not the estimator or flight
acceptance step. H-Flow supplies optical-flow velocity and distance aiding; it
does not independently provide a complete indoor pose or reliable yaw source.

### Recorded PX4 and DroneCAN identity

The retained PX4 console evidence reports:

| Field | Value |
| --- | --- |
| Hardware | `PX4_FMU_V6C`, type `V6C000002`, revision `0x002` |
| PX4 | Release 1.17.0, stable branch, Git `d6f12ad1c4f70ad3230afd7d86e971421e02fef4` |
| OS | NuttX 11.0.0, Git `fb2fadf6f599c1406f052db013efd00a2518e72c` |
| Toolchain | GNU GCC 13.2.1 20231009 |
| PX4 GUID | `000600000000383236313533511400360039` |

The same console session discovered H-Flow as DroneCAN node 125 in `OK` /
`OPERAT` state and mapped both `uavcan_flow` and `uavcan_rangefinder` to sensor
instance 0. CAN1 reported zero hardware and I/O errors. CAN2 reported no
received frames and accumulating errors; that interface must be confirmed as
intentionally unused or diagnosed before the CAN evidence is considered
closed.

### Corrected H-Flow parameter baseline

The corrected export records the installed PX4-side capability and fusion
limits:

| Parameter | Recorded value | Meaning in this commissioning baseline |
| --- | ---: | --- |
| `UAVCAN_SUB_FLOW` | `1` | Subscribe to DroneCAN optical flow. |
| `UAVCAN_SUB_RNG` | `1` | Subscribe to DroneCAN range. |
| `EKF2_OF_CTRL` | `1` | Enable optical-flow aiding. |
| `EKF2_RNG_CTRL` | `1` | Enable range-height aiding. |
| `UAVCAN_RNG_MIN` / `SENS_FLOW_MINHGT` | `0.08 m` | Sensor-reported and estimator minimum usable height are aligned. |
| `UAVCAN_RNG_MAX` / `SENS_FLOW_MAXHGT` | `30 m` | Sensor-reported and estimator maximum usable height are aligned. This is a capability ceiling, not an approved indoor flight ceiling. |
| `SENS_FLOW_MAXR` | `7.4 rad/s` | H-Flow maximum angular-flow capability supplied to PX4; it is not a commanded vehicle rate. |
| `SENS_FLOW_RATE` | `70 Hz` | Configured sensor publication rate. ULog topic rates may be lower because PX4 logging profiles down-sample topics. |
| `EKF2_OF_QMIN` / `EKF2_OF_QMIN_GND` | `1 / 0` | Minimum optical-flow quality thresholds in flight / on ground; the retained log must be checked against these values. |
| `SENS_FLOW_ROT` | `0` | Configured sensor rotation; still requires controlled X/Y motion-sign validation. |
| `EKF2_OF_POS_X/Y/Z` | `+0.045 / -0.050 / 0 m` | Configured optical-flow sensor location in PX4 body axes. |
| `EKF2_RNG_POS_X/Y/Z` | `0 / 0 / 0 m` | Configured range origin; physical measurement remains to be confirmed. |

These values make PX4's sensor model internally consistent with the H-Flow
capability values. They do not enlarge the operator-approved height, speed, or
lighting envelope and do not prove axis signs or estimator performance under
motion.

### Disarmed H-Flow ULog evidence — 22 July 2026

The completed QGroundControl download was hashed only after QGroundControl
closed its write handle and two consecutive hashes matched. PyULog 1.2.3 then
parsed the frozen 3,071,809-byte file without corruption and reported no ULog
dropouts. Its PX4 Git hash and system GUID match the console identity above.

| Observation | Result | Interpretation |
| --- | --- | --- |
| Raw optical flow | 72 logged samples over 71.013 s; device `8682755`; quality min/median/max `79 / 112 / 120`; sensor error count remained `0` | Live flow and quality are present. The roughly 1 Hz ULog rate is a logging rate, not the configured `SENS_FLOW_RATE=70 Hz`. |
| Raw range | 72 logged samples over 71.006 s; device `9010435`; orientation `25`; `0.156-0.712 m`; signal quality `100` throughout | Live range followed the hand-lift sequence and stayed within the configured `0.08-30 m` capability limits. |
| Optical-flow fusion | 145 samples per EKF instance; aid-source `fused=1` throughout; maximum logged test ratio `0.295` | Both estimator instances used flow, and sampled test ratios remained below the `1.0` rejection boundary. |
| Optical-flow rejection flag | Both estimator status instances briefly reported X/Y rejection near `42.04 s`; the lower-rate aid-source samples did not capture a rejected sample | Retain this transient. The run must not be summarized as “zero rejection,” and dynamic acceptance should explain whether the event is repeatable. |
| Range-height fusion | 145 samples per EKF instance; `fused=1`, no sampled rejection; maximum test ratio `0.00481` | Range-height aiding was healthy in this disarmed run. |
| Local position and resets | `xy_valid=1`, `z_valid=1`, bottom distance valid throughout; X/Y/Z reset counters did not increment | The estimator remained locally valid without an in-window reset while the aircraft was moved by hand. |
| Heading readiness | `heading_good_for_control=0` throughout | This log does not accept yaw/heading readiness for position-hold flight. |
| Vehicle state | Disarmed and `cs_in_air=0` throughout; `cs_vehicle_at_rest` changed during hand movement | This is not flight or position-hold evidence. Absolute local-position change during a hand-carried sequence is not drift. |
| CAN1 | `+34,298` RX frames, `+2,379` TX frames, zero I/O-error increase | The active H-Flow bus remained clean during the recording. |
| CAN2 | zero RX frames, `+2,379` TX frames, `+2,503` I/O errors | Confirm the interface is intentionally unused/unterminated or diagnose it; do not attribute these errors to CAN1/H-Flow. |

The raw range and local-position traces show that the aircraft was lifted and
moved, but the log has no labels for deliberate body +X, -X, +Y, and -Y motion.
It therefore cannot prove `SENS_FLOW_ROT=0` or the X/Y sign convention. It also
contains no armed hover, GPS-denied position hold, sensor disconnect, Hold, or
manual-takeover event.

The retained parameter export must make the configured state reviewable. At a
minimum, review the installed PX4 release's DroneCAN enablement and flow/range
subscriptions, optical-flow fusion control, range-aid controls, sensor height
limits, maximum flow rate, quality thresholds, and the measured optical-flow
and rangefinder body offsets. Exact parameter availability and semantics must
come from the installed PX4 release; the current reference procedures are the
[Holybro H-Flow setup guide](https://docs.holybro.com/peripherals/h-flow-dronecan/setup-guide)
and [PX4 optical-flow guide](https://docs.px4.io/main/en/sensor/optical_flow).
These references are not evidence of the values installed on this aircraft.

## Required H-Flow evidence capture

Before Atlas represents H-Flow/PX4 as navigation-ready, retain one immutable
commissioning bundle containing:

1. Aircraft, flight-controller, H-Flow, and mounting identities.
2. PX4 and H-Flow firmware versions.
3. A complete QGroundControl PX4 parameter export, not only screenshots or a
   list of parameters believed to have changed.
4. Mount orientation and measured body-frame sensor offsets.
5. QGroundControl/MAVLink or PX4-log evidence for optical flow, distance,
   quality, fusion state, estimator innovations, resets, and local-position
   validity.
6. The room envelope: lighting, floor texture, height range, speed, duration,
   battery reserve, and magnetic conditions.
7. Measured horizontal drift, height error, yaw behavior, dropouts, innovation
   ratios, and recovery behavior.
8. The exact pass/fail thresholds approved for GPS-denied translation.

Do not disable or degrade independent flight safeguards merely to make an
indoor test pass. Initial flight validation must use an independent safety pilot
with direct RC authority, propeller guards, and a controlled area without
people.

## Spatial endurance acceptance state

The production OAK runtime passed live RGB-D acceptance, including firmware
re-enumeration into USB 3 and the final production doctor/probe. The prior
acceptance did **not** include a timed soak, a physical disconnect/reconnect
cycle, or a cold-reboot endurance cycle. These remain open and must not be
reported as passed.

### Timed soak

Run with the aircraft disarmed and secured on the bench. Retain:

- package version, source revision, image SHA, OAK MXID, and calibration hash;
- start/end time and ambient/compute thermal observations;
- periodic probe output and `systemctl show` restart counts;
- `journalctl` output covering the complete run;
- live USB speed before and after the run;
- minimum/maximum observed color and depth rate, frame age, and sync skew.

Acceptance requires the service to remain active, the device to remain at USB
3, every post-warm-up probe to report fresh synchronized calibrated RGB-D, no
unexpected container restart, and no `X_LINK_DEVICE_NOT_FOUND`, depth-encoding,
or calibration-loss failure.

Useful observations are:

```sh
sudo atlas-setup doctor
sudo /usr/libexec/atlas-agent/atlas-spatial-runtime-check --discover
sudo /usr/libexec/atlas-agent/atlas-spatial-runtime-check
systemctl show atlas-spatial-runtime.service -p ActiveState -p NRestarts
journalctl -u atlas-spatial-runtime.service --since "YYYY-MM-DD HH:MM:SS" --no-pager
lsusb -t
```

### Physical camera disconnect and recovery

Prerequisites: aircraft disarmed, propellers removed, no active mission or
Offboard controller, and operator physically present.

1. Capture the ready probe, USB tree, service state, and restart count.
2. Disconnect the OAK USB cable and confirm spatial health becomes unavailable
   or degraded without interrupting `atlas-agent.service` or
   `atlas-mavsdk.service`.
3. Reconnect the same camera to the accepted USB 3 port and retain the complete
   spatial journal across firmware re-enumeration.
4. Confirm whether automatic service recovery returns `ready=true`. A manual
   service restart may be tested separately, but it must not be recorded as
   automatic recovery.
5. Confirm the same live MXID, USB 3 speed, calibration hash, RGB-D encodings,
   synchronization, and container-confinement properties.

### Cold reboot

After a controlled aircraft-computer reboot, repeat the production doctor,
probe, USB, immutable-image, private-libusb linkage, and confinement checks.
Record boot-to-ready duration and any service restart. This is separate from the
disconnect/reconnect result.

## Readiness gates for the next implementation slice

The next mapping implementation may begin when:

- the OAK baseline above remains reproducible;
- timed soak and disconnect/reboot outcomes are recorded, including failures;
- the complete H-Flow/PX4 firmware and parameter bundle is retained;
- live flow/range and estimator behavior is measured;
- camera-to-body and H-Flow-to-body transforms are versioned;
- synchronized RGB-D recording/replay and capture-time PX4 odometry contracts
  are defined.

Autonomous movement remains gated beyond those items by mapping, explicit
unknown-space handling, localization confidence, stopping-envelope validation,
an immutable operator-approved envelope, and single-writer setpoint authority.
