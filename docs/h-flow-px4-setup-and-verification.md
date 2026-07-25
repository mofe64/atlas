# H-Flow PX4 Setup and Verification

**Status:** Practical setup and bench-verification runbook
**Last updated:** 25 July 2026

## Purpose and boundary

This runbook covers installing a Holybro H-Flow, configuring PX4 to consume its
optical-flow and range data, and confirming that the data reaches the PX4
estimator.

H-Flow connects directly to PX4 over DroneCAN. PX4 owns its validation, sensor
fusion, and any resulting estimator state. Atlas Agent does not mirror that
state into a separate navigation layer.

This procedure does not authorize autonomous flight. If the aircraft will use
flow or range aiding in flight, finish with a low, supervised acceptance flight
under the normal PX4 and RC failsafes.

## Safety

For installation and bench checks:

- disarm the aircraft and remove the propellers;
- disconnect the battery before changing CAN wiring or termination;
- keep mission, Offboard, and autonomous movement processes stopped; and
- secure the aircraft whenever it is powered on the bench.

Do not weaken PX4 safeguards or estimator thresholds merely to make the sensor
appear healthy.

## Before changing the aircraft

In QGroundControl:

1. Open **Analyze Tools > MAVLink Console** and run:

   ```text
   ver all
   uavcan status
   ```

2. Save the current parameters from **Vehicle Setup > Parameters > Tools > Save
   to file**.

The parameter file is a practical recovery reference if a value is entered
incorrectly. It does not need a checksum, manifest, evidence bundle, or
versioned Atlas release archive.

## Install the sensor

### Orientation

Mount the H-Flow facing downward with an unobstructed view. With the Holybro
default orientation, the board connectors point toward the rear and
`SENS_FLOW_ROT=0`.

If the physical orientation differs, use the matching PX4 rotation. Do not copy
the rotation from another aircraft without checking the mount.

### Body-frame offsets

Measure from the aircraft centre of gravity to the optical-flow focal point and
rangefinder origin in PX4 body axes:

- X is positive forward;
- Y is positive right;
- Z is positive down.

The flow and range origins may differ even though they share one housing.
Configure their offsets independently.

### CAN wiring

Connect the sensor to the intended DroneCAN bus with a Pixhawk-compatible CAN
cable. Terminate only the two physical ends of the bus. Check connector seating,
cable routing, and strain relief before applying power.

The Ariadne installation uses CAN1. A different port or topology must be
checked on its own merits.

## Configure PX4

Parameter availability can depend on the active subscriptions. Apply the
settings in this order and reboot when QGroundControl requests it.

### Enable DroneCAN and H-Flow subscriptions

| Parameter | Baseline | Purpose |
| --- | ---: | --- |
| `UAVCAN_ENABLE` | `2` | Enable DroneCAN sensors and dynamic node allocation. Use `3` only when DroneCAN ESC output is also required. |
| `UAVCAN_SUB_FLOW` | `1` | Subscribe to optical flow. |
| `UAVCAN_SUB_RNG` | `1` | Subscribe to range data. |

Set `UAVCAN_ENABLE`, reboot, enable both subscriptions, and reboot again.

### Describe the sensor

| Parameter | Ariadne baseline | Purpose |
| --- | ---: | --- |
| `UAVCAN_RNG_MIN` | `0.08 m` | Minimum range capability. |
| `UAVCAN_RNG_MAX` | `30 m` | Maximum range capability. |
| `SENS_FLOW_MINHGT` | `0.08 m` | Minimum height at which PX4 uses this flow model. |
| `SENS_FLOW_MAXHGT` | `30 m` | Sensor capability, not an approved flight ceiling. |
| `SENS_FLOW_MAXR` | `7.4 rad/s` | Maximum angular-flow rate for the sensor model. |
| `SENS_FLOW_RATE` | `70 Hz` | Sensor publication rate. |
| `SENS_FLOW_SCALE` | `1.0` | Initial scale; change only after a measured rotation check. |

Keep the DroneCAN range and optical-flow height limits consistent. These values
describe the device; the operational flight envelope should be more
conservative.

### Enable estimator aiding

| Parameter | Ariadne baseline | Purpose |
| --- | ---: | --- |
| `EKF2_OF_CTRL` | `1` | Enable optical-flow aiding. |
| `EKF2_RNG_CTRL` | `1` | Enable range-height aiding. |
| `EKF2_RNG_A_HMAX` | `10 m` | Maximum height for conditional range aiding. |
| `EKF2_RNG_QLTY_T` | `0.2 s` | Range-quality hysteresis. |
| `EKF2_OF_QMIN` | `1` | Minimum in-flight flow quality. |
| `EKF2_OF_QMIN_GND` | `0` | Minimum on-ground flow quality. |

Do not disable GNSS as part of ordinary H-Flow setup. Sensor delivery and
estimator fusion can be verified with GNSS still enabled.

### Apply aircraft geometry

| Parameter | Value source |
| --- | --- |
| `SENS_FLOW_ROT` | Physical sensor orientation |
| `EKF2_OF_POS_X/Y/Z` | Measured centre-of-gravity-to-flow-origin offset |
| `EKF2_RNG_POS_X/Y/Z` | Measured centre-of-gravity-to-range-origin offset |

The current Ariadne values are:

```text
SENS_FLOW_ROT=0
EKF2_OF_POS_X=+0.045 m
EKF2_OF_POS_Y=-0.050 m
EKF2_OF_POS_Z=0 m
EKF2_RNG_POS_X=0 m
EKF2_RNG_POS_Y=0 m
EKF2_RNG_POS_Z=0 m
```

They are an Ariadne reference, not a template for another airframe. After
saving the parameters, reboot PX4 and refresh the QGroundControl parameter
view.

## Verify the installation

### 1. DroneCAN discovery

Run:

```text
uavcan status
```

Check that:

- the expected H-Flow node is online with health `OK` and mode `OPERAT`;
- `uavcan_flow` and `uavcan_rangefinder` map to sensor instances;
- the active CAN interface is receiving frames; and
- error counters on that interface do not continually increase.

An unused CAN interface can show no traffic without indicating a fault on the
active bus.

### 2. Raw flow and range

Run:

```text
listener sensor_optical_flow -n 5
listener distance_sensor -n 5
uorb top
```

Check for fresh timestamps, plausible publication rates, non-zero range,
reasonable quality, stable device IDs, and no increasing sensor error count.
Lift the aircraft vertically by hand and confirm that range changes in the
expected direction.

### 3. Orientation and signs

Hold the disarmed aircraft level over a textured surface and move it forward,
backward, right, and left.

| Vehicle translation | Expected integrated flow |
| --- | --- |
| Forward | `+Y` |
| Backward | `-Y` |
| Right | `-X` |
| Left | `+X` |

If the signs are wrong, correct the physical orientation or `SENS_FLOW_ROT`.
Do not compensate by inventing offset signs.

### 4. Estimator health

Run bounded samples:

```text
listener estimator_aid_src_optical_flow -n 5
listener estimator_aid_src_rng_hgt -n 5
listener estimator_status_flags -n 5
listener estimator_event_flags -n 5
listener vehicle_local_position -n 5
```

Check that:

- optical flow and range height are fused when measurements are valid;
- innovations and test ratios are not persistently rejected;
- rejection or fault flags are not sustained;
- expected position and bottom-distance validity flags are true; and
- reset counters do not increase unexpectedly.

When multiple EKF instances are enabled, check each one. Heading readiness is a
separate condition and must not be inferred from healthy flow or range data.

Capture a ULog only when a console check is inconclusive or a flight anomaly
needs diagnosis. Routine installation does not require a hashed log archive.

## Finish and, when required, flight-check

Save the final current parameter set in QGroundControl. Keep that one current
configuration file with the aircraft maintenance material; Atlas does not
require a separate hash, evidence ledger, or per-aircraft completion table.

If H-Flow will be used for in-flight aiding, perform a low, manually supervised
hover in a clear area with:

- a safety pilot holding direct RC authority;
- conservative speed, height, duration, and battery limits;
- a textured, adequately lit surface within sensor range; and
- predefined Hold/land/takeover actions for invalid flow, range, or position.

Confirm stable position/height behaviour and safe degradation when the aiding
source becomes unavailable. This check validates PX4 behaviour; it does not
commission Atlas obstacle avoidance.

## References

- [Holybro H-Flow setup guide](https://docs.holybro.com/peripherals/h-flow-dronecan/setup-guide)
- [PX4 optical flow](https://docs.px4.io/main/en/sensor/optical_flow)
- [PX4 DroneCAN configuration](https://docs.px4.io/main/en/dronecan/)
- [PX4 EKF2 optical-flow guidance](https://docs.px4.io/main/en/advanced_config/tuning_the_ecl_ekf)
- [PX4 uORB listener](https://docs.px4.io/main/en/middleware/uorb)
- [QGroundControl parameter save/load](https://docs.qgroundcontrol.com/master/en/qgc-user-guide/setup_view/parameters.html)

Installed PX4 parameter metadata and the installed H-Flow firmware are
authoritative for a particular aircraft. If they disagree with this runbook,
stop and review the applicable hardware and PX4 documentation before changing
the aircraft.
