# H-Flow PX4 Setup and Verification

**Document type:** Operational setup and verification procedure

**Last updated:** 25 July 2026

## Purpose and boundary

Use this procedure to install a Holybro H-Flow. The procedure configures PX4 to
use optical-flow and range data. It also verifies that the data reaches the PX4
estimator.

H-Flow connects directly to PX4 over DroneCAN. PX4 owns its validation, sensor
fusion, and any resulting estimator state. Atlas Agent does not mirror that
state into a separate navigation layer.

This procedure does not authorize autonomous flight. If the aircraft will use
the sensor in flight, complete a low and supervised acceptance flight. Keep the
normal PX4 and RC failsafes active.

## Safety

**Warning:** An unexpected motor command can cause injury. Before installation
or a bench check:

- Disarm the aircraft.
- Remove the propellers.
- Disconnect the battery before you change CAN wiring or termination.
- Stop mission, Offboard, and autonomous-movement processes.
- Secure the aircraft before you apply power on the bench.

**Warning:** Do not weaken a PX4 safeguard or estimator threshold to get a
healthy indication. The changed value can permit unsafe flight behavior.

## Before changing the aircraft

In QGroundControl:

1. Open **Analyze Tools > MAVLink Console** and run:

   ```text
   ver all
   uavcan status
   ```

2. Select **Vehicle Setup > Parameters > Tools > Save to file**.

3. Confirm that QGroundControl writes the parameter file.

Use the parameter file to recover from an incorrect value. Atlas does not
require a checksum, manifest, evidence bundle, or versioned release archive for
this file.

## Install the sensor

### Orientation

Mount the H-Flow with an unobstructed downward view. For the Holybro default
orientation, point the board connectors toward the rear. Set
`SENS_FLOW_ROT=0`.

If the physical orientation is different, select the applicable PX4 rotation.
Verify the mount before you use a value from another aircraft.

### Body-frame offsets

Measure from the aircraft center of gravity to the optical-flow focal point and
rangefinder origin in PX4 body axes:

- X is positive forward;
- Y is positive right;
- Z is positive down.

The flow and range origins may differ even though they share one housing.
Configure their offsets independently.

### CAN wiring

Connect the sensor to the specified DroneCAN bus with a Pixhawk-compatible CAN
cable. Terminate only the two physical ends of the bus.

Before you apply power, examine:

- connector seating;
- cable routing; and
- strain relief.

The Ariadne installation uses CAN1. Verify the wiring and termination if a
different aircraft uses another port or topology.

## Configure PX4

Available parameters can depend on the active subscriptions. Apply the settings
in the specified order. Reboot when QGroundControl tells you to reboot.

### Enable DroneCAN and H-Flow subscriptions

| Parameter | Baseline | Purpose |
| --- | ---: | --- |
| `UAVCAN_ENABLE` | `2` | Enable DroneCAN sensors and dynamic node allocation. Use `3` only when DroneCAN ESC output is also required. |
| `UAVCAN_SUB_FLOW` | `1` | Subscribe to optical flow. |
| `UAVCAN_SUB_RNG` | `1` | Subscribe to range data. |

1. Set `UAVCAN_ENABLE`.
2. Reboot PX4.
3. Set `UAVCAN_SUB_FLOW=1`.
4. Set `UAVCAN_SUB_RNG=1`.
5. Reboot PX4.

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

Keep the DroneCAN range limits and optical-flow height limits consistent. These
values describe sensor capability. They do not define the approved flight
envelope.

### Enable estimator aiding

| Parameter | Ariadne baseline | Purpose |
| --- | ---: | --- |
| `EKF2_OF_CTRL` | `1` | Enable optical-flow aiding. |
| `EKF2_RNG_CTRL` | `1` | Enable range-height aiding. |
| `EKF2_RNG_A_HMAX` | `10 m` | Maximum height for conditional range aiding. |
| `EKF2_RNG_QLTY_T` | `0.2 s` | Range-quality hysteresis. |
| `EKF2_OF_QMIN` | `1` | Minimum in-flight flow quality. |
| `EKF2_OF_QMIN_GND` | `0` | Minimum on-ground flow quality. |

Do not disable GNSS for this procedure. You can verify sensor delivery and
estimator fusion while GNSS is enabled.

### Apply aircraft geometry

| Parameter | Value source |
| --- | --- |
| `SENS_FLOW_ROT` | Physical sensor orientation |
| `EKF2_OF_POS_X/Y/Z` | Measured center-of-gravity-to-flow-origin offset |
| `EKF2_RNG_POS_X/Y/Z` | Measured center-of-gravity-to-range-origin offset |

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

These values apply only to Ariadne. Measure the values for a different
airframe.

After you save the parameters:

1. Reboot PX4.
2. Refresh the QGroundControl parameter view.
3. Confirm that QGroundControl shows the saved values.

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

Confirm these results:

- timestamps are fresh;
- publication rates agree with the configured rates;
- range is nonzero and changes with height;
- quality is valid for the test surface;
- device IDs are stable; and
- sensor error counts do not increase.

Lift the aircraft vertically by hand. Confirm that the reported range changes
in the correct direction.

### 3. Orientation and signs

Hold the disarmed aircraft level above a textured surface. Move it in each
direction in the table.

| Vehicle translation | Expected integrated flow |
| --- | --- |
| Forward | `+Y` |
| Backward | `-Y` |
| Right | `-X` |
| Left | `+X` |

If a sign is incorrect, correct the physical orientation or
`SENS_FLOW_ROT`. Do not change an offset sign to compensate for incorrect
orientation.

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

Capture a ULog if the console check is inconclusive or you must diagnose a
flight anomaly. A routine installation does not require a hashed log archive.

## Finish and, when required, flight-check

Save the final parameter set in QGroundControl. Keep the current configuration
file with the aircraft maintenance material. Atlas does not require a separate
hash, evidence ledger, or completion table.

If H-Flow will be used for in-flight aiding, perform a low, manually supervised
hover in a clear area with:

- a safety pilot holding direct RC authority;
- conservative speed, height, duration, and battery limits;
- a textured, adequately lit surface within sensor range; and
- predefined Hold/land/takeover actions for invalid flow, range, or position.

Confirm stable position and height behavior. Confirm safe degradation when the
aiding source becomes unavailable.

This flight check validates PX4 behavior. It does not authorize Atlas obstacle
avoidance.

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
