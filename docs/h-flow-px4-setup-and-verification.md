# H-Flow PX4 Setup and Verification

**Status:** New-aircraft installation and commissioning runbook; not a flight
authorization  
**Last updated:** 22 July 2026

## Purpose

This runbook describes how Atlas installed, configured, and verified a Holybro
H-Flow on PX4, and how to repeat that work on another aircraft.

Use this document for the installation and parameter procedure. The current
aircraft baseline and the feature that depends on it are summarized in the
[Indoor Operations Plan](indoor-ops-plan.md).

The work has four separate gates:

1. The H-Flow is installed and electrically healthy.
2. PX4 is configured to receive and fuse its flow and range data.
3. Live sensor and estimator behavior is verified and retained.
4. Controlled GPS-denied flight and failure handling are accepted.

Passing an earlier gate never implies that a later gate has passed. In
particular, this procedure does not authorize autonomous indoor movement.

## Safety and authority boundary

Perform installation and bench tests with:

- the aircraft disarmed;
- propellers removed;
- the battery disconnected while changing wiring or termination;
- no mission, Offboard controller, or autonomous movement process active; and
- the aircraft physically secured whenever it is powered on the bench.

Do not disable independent flight safeguards merely to make optical-flow
testing pass. The first hover and sensor-loss tests require a safety pilot with
direct RC authority, propeller guards, a controlled area without people, and a
written flight envelope.

## Evidence to create for every aircraft

Before changing configuration, choose a stable aircraft identifier and an
evidence location. Retain at least:

| Artifact | Required content |
| --- | --- |
| Aircraft record | Aircraft ID, airframe, flight-controller model, date, operator, and installation photographs |
| PX4 identity | Complete `ver all` output |
| H-Flow identity | DroneCAN node name, node ID, hardware version, software version, unique ID/serial, and firmware provenance |
| Mount record | Orientation photographs and measured body-frame flow/range X/Y/Z offsets |
| Parameter baseline | Complete QGroundControl `.params` export and SHA-256 |
| CAN evidence | `uavcan status` before and after the bench test |
| Sensor evidence | Flow, quality, range, device IDs, update rates, and error counters |
| Estimator evidence | Flow/range fusion, innovations, test ratios, rejection flags, resets, and local-position validity |
| Flight evidence | Approved envelope, drift, height error, yaw behavior, dropouts, degradation, recovery, Hold, and takeover results |

Do not rely on screenshots alone when a complete text export or ULog is
available. Do not modify or re-encode a retained binary log; record its exact
hash after QGroundControl has finished downloading it.

## 1. Record the starting aircraft state

Connect QGroundControl and open **Analyze Tools > MAVLink Console**. Capture:

```text
ver all
uavcan status
```

`ver all` binds the configuration to the exact PX4 build, flight-controller
hardware, OS build, toolchain, and PX4 GUID. `uavcan status` records enabled CAN
interfaces, traffic/error counters, online nodes, and the flow/range sensor
mapping.

Also export the complete pre-change parameter set from **Vehicle Setup >
Parameters > Tools > Save to file**. This is the rollback and comparison
baseline.

## 2. Install the H-Flow

### Mount orientation

Mount the sensor downward with an unobstructed view of the floor. For the
Holybro default rotation, the board connectors point toward the rear of the
vehicle and `SENS_FLOW_ROT=0`.

If the connectors do not point rearward, select the rotation that matches the
physical installation. Do not use `0` simply because it worked on another
aircraft.

Retain photographs that show:

- the direction of the vehicle nose;
- the H-Flow connector direction;
- the flow lens and rangefinder field of view;
- nearby landing gear, wiring, or structure that could obstruct either sensor;
  and
- cable strain relief.

### Measure body-frame offsets

Measure from the aircraft centre of gravity to the optical-flow focal point and
rangefinder origin using PX4 body axes:

- X is positive forward;
- Y is positive right;
- Z is positive down.

Record optical-flow and range offsets separately. The sensors share one H-Flow
housing, but their configured offsets must still reflect the measured origins
and must not automatically be copied from the Ariadne values.

### CAN wiring and termination

Connect the H-Flow to the intended DroneCAN bus using a Pixhawk-compatible CAN
cable. Record the flight-controller port and every node on that bus.

The CAN network must have termination at its two physical ends. If the H-Flow
is an end node, configure its `CAN_TERMINATE`/termination setting according to
the actual topology. Do not enable termination on every node. After wiring,
inspect the cable routing, connector seating, and strain relief before applying
power.

The verified Ariadne installation uses CAN1. CAN2 is not part of the accepted
H-Flow path and must be assessed independently if enabled.

## 3. Configure PX4 through QGroundControl

Parameter availability can depend on subscriptions. Use this order and reboot
when QGroundControl or the parameter metadata requires it.

### Enable DroneCAN and sensor subscriptions

| Parameter | Baseline | Purpose |
| --- | ---: | --- |
| `UAVCAN_ENABLE` | `2` | Enable DroneCAN sensors and dynamic node allocation. Use `3` only when the aircraft also requires DroneCAN ESC output. |
| `UAVCAN_SUB_FLOW` | `1` | Subscribe to DroneCAN optical flow. |
| `UAVCAN_SUB_RNG` | `1` | Subscribe to DroneCAN rangefinder data. |

Set `UAVCAN_ENABLE`, reboot, enable both subscriptions, and reboot again. PX4
may not expose the flow-specific parameters until the flow subscription is
enabled.

### Set H-Flow capability values

| Parameter | Verified baseline | Purpose |
| --- | ---: | --- |
| `UAVCAN_RNG_MIN` | `0.08 m` | H-Flow minimum range capability. |
| `UAVCAN_RNG_MAX` | `30 m` | H-Flow maximum range capability. |
| `SENS_FLOW_MINHGT` | `0.08 m` | Minimum height at which PX4 uses this flow model. |
| `SENS_FLOW_MAXHGT` | `30 m` | Maximum sensor capability supplied to PX4. This is not an approved flight ceiling. |
| `SENS_FLOW_MAXR` | `7.4 rad/s` | Maximum angular-flow rate reported for the sensor model. This is not a commanded aircraft rate. |
| `SENS_FLOW_RATE` | `70 Hz` | Configured sensor publication rate. |
| `SENS_FLOW_SCALE` | `1.0` | Initial flow scale; change only from measured rotational/flight evidence. |

Keep the DroneCAN range limits and optical-flow height limits aligned. The
values describe sensor capability; the approved indoor operating envelope will
normally be much smaller.

### Enable estimator aiding

| Parameter | Verified baseline | Purpose |
| --- | ---: | --- |
| `EKF2_OF_CTRL` | `1` | Enable optical-flow aiding. |
| `EKF2_RNG_CTRL` | `1` | Enable range-height aiding. |
| `EKF2_RNG_A_HMAX` | `10 m` | Maximum height for conditional range aiding in this baseline. |
| `EKF2_RNG_QLTY_T` | `0.2 s` | Range-quality hysteresis time in this baseline. |
| `EKF2_OF_QMIN` | `1` | Minimum in-flight flow-quality value in this baseline. |
| `EKF2_OF_QMIN_GND` | `0` | Minimum on-ground flow quality in this baseline. |

Do not tune quality thresholds or innovation gates merely to suppress a failed
test. First establish whether the cause is lighting, texture, height, motion,
mounting, timing, vibration, or an incorrect sensor model.

Do not set `EKF2_GPS_CTRL=0` as part of routine installation. Deliberately
disabling GNSS is a separate, controlled test decision and not required to
prove that H-Flow data reaches the estimator.

### Apply the aircraft-specific geometry

| Parameter | Value source |
| --- | --- |
| `SENS_FLOW_ROT` | Physical connector/mount orientation |
| `EKF2_OF_POS_X/Y/Z` | Measured centre-of-gravity-to-flow-focal-point offset |
| `EKF2_RNG_POS_X/Y/Z` | Measured centre-of-gravity-to-range-origin offset |

The Ariadne values are a worked example, not a template:

```text
SENS_FLOW_ROT=0
EKF2_OF_POS_X=+0.045 m
EKF2_OF_POS_Y=-0.050 m
EKF2_OF_POS_Z=0 m
EKF2_RNG_POS_X=0 m
EKF2_RNG_POS_Y=0 m
EKF2_RNG_POS_Z=0 m
```

The Ariadne range offsets still require physical confirmation. A new aircraft
must use its own measurements.

After saving the parameters, reboot the flight controller and refresh the
QGroundControl parameter view.

## 4. Verify DroneCAN discovery and identity

Run:

```text
uavcan status
```

Pass the discovery check only when:

- the expected H-Flow node is online with health `OK` and mode `OPERAT`;
- `uavcan_flow` maps to an optical-flow sensor instance;
- `uavcan_rangefinder` maps to a rangefinder instance;
- the active CAN interface receives frames; and
- hardware, transfer, and I/O errors on the active H-Flow interface do not
  increase during the observation window.

Treat other CAN interfaces separately. Zero received frames plus increasing
errors on an unused interface does not prove that the active H-Flow bus is bad,
but the interface must be confirmed as intentionally unused or diagnosed.

### Capture the H-Flow firmware identity

`uavcan status` proves node presence and health; it does not by itself retain
the node's software/hardware version and unique ID.

For PX4 releases that publish `device_information`, capture all entries for the
H-Flow with:

```text
listener device_information
```

PX4 documents this asset-tracking topic for v1.18 and later. For the verified
PX4 1.17 baseline, use a DroneCAN monitor that requests
`uavcan.protocol.GetNodeInfo`, such as the cross-platform DroneCAN GUI Tool with
a compatible CAN adapter. Retain:

- node name and node ID;
- software major/minor and version-control commit, when supplied;
- hardware major/minor;
- the 16-byte unique ID; and
- a screenshot or text export tied to the aircraft evidence bundle.

Do not update the H-Flow firmware merely to discover its current identity.
Firmware update is a separate controlled change with its own before/after
evidence and rollback plan.

## 5. Verify raw sensor data

Use the QGroundControl MAVLink Console. PX4's `listener` prints a bounded number
of uORB messages:

```text
listener sensor_optical_flow -n 5
listener distance_sensor -n 5
uorb top
```

Also use **Analyze Tools > MAVLink Inspector** to inspect `DISTANCE_SENSOR` when
available. Holybro's setup check requires a non-zero `current_distance`.

Verify and record:

- stable flow and range device IDs;
- non-stale timestamps;
- optical-flow quality and error count;
- range in metres and range signal quality;
- plausible change when the aircraft is lifted vertically by hand; and
- plausible topic rates in `uorb top`.

The ULog recording rate may be lower than the sensor publication rate. Do not
mistake a down-sampled log topic for a 1 Hz sensor.

## 6. Verify orientation and motion signs

With the aircraft disarmed, propellers removed, level, and held at a usable
height above a textured floor, record a labelled sequence:

1. stationary;
2. forward along body +X;
3. backward along body -X;
4. right along body +Y;
5. left along body -Y; and
6. stationary again.

PX4's optical-flow contract is:

| Vehicle translation | Expected integrated flow |
| --- | --- |
| Forward | `+Y` |
| Backward | `-Y` |
| Right | `-X` |
| Left | `+X` |

These are angular image-flow measurements, not translational distances. If the
signs do not match, stop and correct the physical orientation or
`SENS_FLOW_ROT`; do not compensate by inventing offset signs.

Use a separate controlled rotation sequence only when validating
`SENS_FLOW_SCALE`. Pure rotational gyro and optical-flow integrals should agree
before the scale is changed.

## 7. Verify estimator fusion and local-position health

Capture bounded console samples and a ULog containing at least:

```text
listener estimator_aid_src_optical_flow -n 5
listener estimator_aid_src_rng_hgt -n 5
listener estimator_status_flags -n 5
listener estimator_event_flags -n 5
listener vehicle_local_position -n 5
```

Review both EKF instances when multiple estimators are enabled. Check:

- optical-flow and range-height aiding are active;
- `fused` is asserted while valid measurements are present;
- innovation values and test ratios remain bounded;
- rejection/fault flags are not sustained or unexplained;
- XY, Z, and bottom-distance validity remain true when expected;
- reset counters do not increment unexpectedly; and
- heading readiness is evaluated separately from flow/range readiness.

PX4 defines an innovation test ratio of `1.0` as the rejection boundary and
describes successful operation as normally remaining below `0.5`, with only
occasional higher spikes. A brief flag must still be retained and explained;
do not summarize a run as “zero rejection” if any estimator status sample says
otherwise.

### Ariadne disarmed benchmark

The retained Ariadne log is a comparison point, not a universal acceptance
threshold:

| Observation | Ariadne result |
| --- | --- |
| ULog | 72.646 s, clean parse, zero declared dropouts |
| Flow quality | `79 / 112 / 120` minimum/median/maximum; error count `0` |
| Range | `0.156-0.712 m`; signal quality `100` |
| Fusion | Flow and range-height fused on both EKF instances |
| Maximum sampled flow test ratio | `0.295` |
| Estimator status | Brief X/Y flow rejection near 42.04 s during hand movement |
| Local position | XY/Z/bottom distance valid; no in-window reset-counter increment |
| Heading | `heading_good_for_control=false` throughout |
| Vehicle state | Disarmed and not in air |

This benchmark accepted live disarmed sensor/estimator behavior only. It did
not accept rotation signs, heading, drift, position hold, or flight behavior.

## 8. Export and hash the final configuration

After the final reboot and verification:

1. Refresh all parameters in QGroundControl.
2. Use **Vehicle Setup > Parameters > Tools > Save to file**.
3. Confirm the header contains the expected PX4 version and Git revision.
4. Confirm the export contains the flight-controller component and every
   expected H-Flow-related parameter.
5. Compute and record a SHA-256 hash.

On macOS or Linux:

```sh
shasum -a 256 aircraft-hflow-final.params
```

A QGroundControl parameter file can contain multiple components. A PX4-only
export does not replace the separate H-Flow node firmware/identity record.

## 9. Capture and retain the ULog

Before the test, check the logger:

```text
logger status
```

If the logger process is running but not currently recording, capture a bounded
log around the labelled test:

```text
logger on
logger status
```

After completing the test:

```text
logger off
logger status
```

`logger on` overrides the normal armed-state trigger; it does not arm the
aircraft. Confirm that a new log started and stopped as intended.

Download the file through **Analyze Tools > Download Logs**. Wait for the
download to finish and for QGroundControl to close the file before hashing or
parsing it. A preallocated file can have its final size while QGroundControl is
still writing missing blocks.

Record:

```sh
shasum -a 256 log-file.ulg
```

Retain the raw ULog unchanged. Record the analysis tool and version separately
so results can be reproduced.

## 10. Flight acceptance remains a separate gate

Do not progress directly from a healthy bench log to an indoor waypoint. The
first GPS-denied flight stage must define and retain:

- the exact room boundary, floor texture, lighting, magnetic conditions, and
  usable height range;
- maximum horizontal/vertical speed and duration;
- horizontal drift, height error, yaw error, dropout, and innovation limits;
- battery reserve and abort criteria;
- safety-pilot and RC takeover roles;
- Hold behavior when flow, range, local position, or heading becomes invalid;
- post-test parameter and ULog hashes.

Start with a low, manually supervised hover/position-hold test. Autonomous
waypoint execution remains blocked until position-hold behavior, estimator
degradation, and operator takeover have separate acceptance evidence.

Atlas exposes H-Flow/PX4 estimator state through the Agent's protected,
read-only navigation socket. The spatial runtime deliberately does not compare
that state with VIO or fuse VIO into PX4. Initial H-Flow position-hold
acceptance therefore depends on PX4 estimator health and controlled flight
evidence, not on a separate VIO/PX4 comparison subsystem.

## Per-aircraft completion record

Copy this table into the commissioning evidence for each new aircraft:

| Field | Recorded value | Evidence reference | Status |
| --- | --- | --- | --- |
| Aircraft ID |  |  | `PENDING` |
| Flight-controller hardware |  |  | `PENDING` |
| PX4 release and full Git hash |  |  | `PENDING` |
| PX4 GUID |  |  | `PENDING` |
| H-Flow node name/ID |  |  | `PENDING` |
| H-Flow hardware/software version |  |  | `PENDING` |
| H-Flow unique ID |  |  | `PENDING` |
| CAN port and termination topology |  |  | `PENDING` |
| Mount orientation |  |  | `PENDING` |
| Optical-flow X/Y/Z offsets |  |  | `PENDING` |
| Range X/Y/Z offsets |  |  | `PENDING` |
| Final parameter export/hash |  |  | `PENDING` |
| DroneCAN discovery |  |  | `PENDING` |
| Direction/sign test |  |  | `PENDING` |
| Raw flow/range evidence |  |  | `PENDING` |
| Fusion/innovation/reset evidence |  |  | `PENDING` |
| GPS-denied position hold |  |  | `PENDING` |
| Sensor-loss/Hold/takeover |  |  | `PENDING` |
| Final acceptance authority/date |  |  | `PENDING` |

## Authoritative references

- [Holybro H-Flow setup guide](https://docs.holybro.com/peripherals/h-flow-dronecan/setup-guide)
- [Holybro H-Flow firmware](https://docs.holybro.com/peripherals/h-flow-dronecan/firmware)
- [PX4 optical flow](https://docs.px4.io/main/en/sensor/optical_flow)
- [PX4 DroneCAN configuration](https://docs.px4.io/main/en/dronecan/)
- [PX4 CAN wiring and `uavcan status`](https://docs.px4.io/main/en/uavcan/)
- [PX4 EKF2 optical-flow and estimator guidance](https://docs.px4.io/main/en/advanced_config/tuning_the_ecl_ekf)
- [PX4 uORB listener](https://docs.px4.io/main/en/middleware/uorb)
- [PX4 logging](https://docs.px4.io/main/en/dev_log/logging)
- [PX4 DroneCAN asset tracking](https://docs.px4.io/main/en/debug/asset_tracking)
- [DroneCAN GUI Tool](https://dronecan.github.io/GUI_Tool/Overview/)
- [DroneCAN `GetNodeInfo`](https://dronecan.github.io/Specification/6._Application_level_functions/)
- [QGroundControl parameter save/load](https://docs.qgroundcontrol.com/master/en/qgc-user-guide/setup_view/parameters.html)
- [QGroundControl Analyze Tools](https://docs.qgroundcontrol.com/master/en/qgc-user-guide/analyze_view/)

Installed PX4 parameter metadata and the installed H-Flow firmware remain
authoritative for a specific aircraft. If they disagree with this runbook,
stop, record the discrepancy, and review the applicable release before
changing the aircraft.
