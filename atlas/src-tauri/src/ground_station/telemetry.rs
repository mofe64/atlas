use tonic::Status;

use crate::database::{
    BatteryTelemetry, GpsQuality, HomePosition, RcStatus, TelemetryInput, VehicleHealth,
};

use super::proto::pb;

#[allow(clippy::result_large_err)]
pub(super) fn input(
    telemetry: pb::AircraftTelemetry,
    received_at_unix_ms: i64,
) -> Result<TelemetryInput, Status> {
    if telemetry.observed_at_unix_ms <= 0 || telemetry.source.trim().is_empty() {
        return Err(Status::invalid_argument(
            "telemetry observation time and source are required",
        ));
    }
    validate_range(telemetry.battery_percent, 0.0, 100.0, "battery percent")?;
    validate_finite(telemetry.relative_altitude_m, "relative altitude")?;
    validate_range(telemetry.latitude, -90.0, 90.0, "latitude")?;
    validate_range(telemetry.longitude, -180.0, 180.0, "longitude")?;
    validate_range(telemetry.heading_deg, 0.0, 360.0, "heading")?;
    validate_range(telemetry.ground_speed_mps, 0.0, f64::MAX, "ground speed")?;
    validate_finite(telemetry.absolute_altitude_m, "absolute altitude")?;
    validate_finite(telemetry.terrain_altitude_m, "terrain altitude")?;
    validate_range(
        telemetry.bottom_clearance_m,
        0.0,
        f64::MAX,
        "bottom clearance",
    )?;
    validate_finite(telemetry.velocity_north_mps, "north velocity")?;
    validate_finite(telemetry.velocity_east_mps, "east velocity")?;
    validate_finite(telemetry.velocity_down_mps, "down velocity")?;
    validate_finite(telemetry.climb_rate_mps, "climb rate")?;

    let batteries = telemetry
        .batteries
        .into_iter()
        .map(battery)
        .collect::<Result<Vec<_>, _>>()?;
    let health = telemetry.health.map(vehicle_health);
    let rc_status = telemetry.rc_status.map(rc_status).transpose()?;
    let home_position = telemetry.home_position.map(home_position).transpose()?;
    let gps_quality = telemetry.gps_quality.map(gps_quality).transpose()?;

    Ok(TelemetryInput {
        source: telemetry.source,
        agent_observed_at_unix_ms: telemetry.observed_at_unix_ms,
        received_at_unix_ms,
        battery_percent: telemetry.battery_percent,
        relative_altitude_m: telemetry.relative_altitude_m,
        flight_mode: telemetry.flight_mode,
        armed: telemetry.armed,
        in_air: telemetry.in_air,
        latitude: telemetry.latitude,
        longitude: telemetry.longitude,
        heading_deg: telemetry.heading_deg,
        ground_speed_mps: telemetry.ground_speed_mps,
        gps_fix: telemetry.gps_fix,
        satellites_visible: telemetry.satellites_visible,
        home_position_set: telemetry.home_position_set,
        batteries,
        health,
        absolute_altitude_m: telemetry.absolute_altitude_m,
        terrain_altitude_m: telemetry.terrain_altitude_m,
        bottom_clearance_m: telemetry.bottom_clearance_m,
        velocity_north_mps: telemetry.velocity_north_mps,
        velocity_east_mps: telemetry.velocity_east_mps,
        velocity_down_mps: telemetry.velocity_down_mps,
        climb_rate_mps: telemetry.climb_rate_mps,
        landed_state: nonempty(telemetry.landed_state),
        rc_status,
        home_position,
        gps_quality,
    })
}

#[allow(clippy::result_large_err)]
fn battery(value: pb::BatteryTelemetry) -> Result<BatteryTelemetry, Status> {
    validate_range(
        value.remaining_percent,
        0.0,
        100.0,
        "battery remaining percent",
    )?;
    validate_range(value.voltage_v, 0.0, f64::MAX, "battery voltage")?;
    validate_finite(value.current_a, "battery current")?;
    validate_finite(value.temperature_c, "battery temperature")?;
    validate_range(
        value.consumed_ah,
        0.0,
        f64::MAX,
        "battery consumed capacity",
    )?;
    validate_range(
        value.time_remaining_s,
        0.0,
        f64::MAX,
        "battery remaining time",
    )?;
    Ok(BatteryTelemetry {
        id: value.id,
        function: value.function,
        remaining_percent: value.remaining_percent,
        voltage_v: value.voltage_v,
        current_a: value.current_a,
        temperature_c: value.temperature_c,
        consumed_ah: value.consumed_ah,
        time_remaining_s: value.time_remaining_s,
    })
}

fn vehicle_health(value: pb::VehicleHealth) -> VehicleHealth {
    VehicleHealth {
        gyrometer_calibration_ok: value.gyrometer_calibration_ok,
        accelerometer_calibration_ok: value.accelerometer_calibration_ok,
        magnetometer_calibration_ok: value.magnetometer_calibration_ok,
        local_position_ok: value.local_position_ok,
        global_position_ok: value.global_position_ok,
        home_position_ok: value.home_position_ok,
        armable: value.armable,
    }
}

#[allow(clippy::result_large_err)]
fn rc_status(value: pb::RcStatus) -> Result<RcStatus, Status> {
    validate_range(
        value.signal_strength_percent,
        0.0,
        100.0,
        "RC signal strength",
    )?;
    Ok(RcStatus {
        available: value.available,
        was_available_once: value.was_available_once,
        signal_strength_percent: value.signal_strength_percent,
    })
}

#[allow(clippy::result_large_err)]
fn home_position(value: pb::HomePosition) -> Result<HomePosition, Status> {
    validate_range(value.latitude, -90.0, 90.0, "home latitude")?;
    validate_range(value.longitude, -180.0, 180.0, "home longitude")?;
    validate_finite(value.absolute_altitude_m, "home absolute altitude")?;
    validate_finite(value.relative_altitude_m, "home relative altitude")?;
    Ok(HomePosition {
        latitude: value.latitude,
        longitude: value.longitude,
        absolute_altitude_m: value.absolute_altitude_m,
        relative_altitude_m: value.relative_altitude_m,
    })
}

#[allow(clippy::result_large_err)]
fn gps_quality(value: pb::GpsQuality) -> Result<GpsQuality, Status> {
    validate_range(value.hdop, 0.0, f64::MAX, "GPS HDOP")?;
    validate_range(value.vdop, 0.0, f64::MAX, "GPS VDOP")?;
    validate_range(
        value.horizontal_uncertainty_m,
        0.0,
        f64::MAX,
        "GPS horizontal uncertainty",
    )?;
    validate_range(
        value.vertical_uncertainty_m,
        0.0,
        f64::MAX,
        "GPS vertical uncertainty",
    )?;
    validate_range(
        value.velocity_uncertainty_mps,
        0.0,
        f64::MAX,
        "GPS velocity uncertainty",
    )?;
    validate_range(
        value.course_over_ground_deg,
        0.0,
        360.0,
        "GPS course over ground",
    )?;
    Ok(GpsQuality {
        hdop: value.hdop,
        vdop: value.vdop,
        horizontal_uncertainty_m: value.horizontal_uncertainty_m,
        vertical_uncertainty_m: value.vertical_uncertainty_m,
        velocity_uncertainty_mps: value.velocity_uncertainty_mps,
        course_over_ground_deg: value.course_over_ground_deg,
    })
}

fn nonempty(value: Option<String>) -> Option<String> {
    value.filter(|value| !value.trim().is_empty())
}

#[allow(clippy::result_large_err)]
fn validate_finite(value: Option<f64>, label: &str) -> Result<(), Status> {
    if value.is_some_and(|value| !value.is_finite()) {
        return Err(Status::invalid_argument(format!(
            "telemetry {label} must be finite"
        )));
    }
    Ok(())
}

#[allow(clippy::result_large_err)]
fn validate_range(
    value: Option<f64>,
    minimum: f64,
    maximum: f64,
    label: &str,
) -> Result<(), Status> {
    validate_finite(value, label)?;
    if value.is_some_and(|value| value < minimum || value > maximum) {
        return Err(Status::invalid_argument(format!(
            "telemetry {label} is outside its valid range"
        )));
    }
    Ok(())
}
