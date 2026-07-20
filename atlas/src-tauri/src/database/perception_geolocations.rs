use std::f64::consts::PI;

use rusqlite::{params, OptionalExtension, Row, Transaction};
use serde::{Deserialize, Serialize};

use super::LocalDatabase;

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TrackGeolocationSnapshot {
    pub id: String,
    pub command_id: String,
    pub selection_id: String,
    pub drone_id: String,
    pub track_session_id: String,
    pub track_id: String,
    pub source_id: String,
    pub status: String,
    pub requested_by: String,
    pub requested_at_unix_ms: i64,
    pub resolved_at_unix_ms: Option<i64>,
    pub aim_point: String,
    pub assumed_aim_point_height_m: f64,
    pub assumed_aim_point_height_uncertainty_m: f64,
    pub ground_altitude_amsl_m: f64,
    pub ground_altitude_uncertainty_m: f64,
    pub ground_altitude_source: String,
    pub ground_altitude_source_version: String,
    pub ground_altitude_resolved_at_unix_ms: i64,
    pub latitude: Option<f64>,
    pub longitude: Option<f64>,
    pub altitude_amsl_m: Option<f64>,
    pub horizontal_uncertainty_m: Option<f64>,
    pub method: String,
    pub frame_observed_at_unix_ms: Option<i64>,
    pub initial_latitude: Option<f64>,
    pub initial_longitude: Option<f64>,
    pub initial_altitude_amsl_m: Option<f64>,
    pub initial_horizontal_uncertainty_m: Option<f64>,
    pub initial_method: String,
    pub refinement_status: String,
    pub terrain_source: String,
    pub terrain_source_version: String,
    pub terrain_vertical_uncertainty_m: Option<f64>,
    pub terrain_iteration_count: u32,
    pub terrain_residual_m: Option<f64>,
    pub range_source: String,
    pub filtered_latitude: Option<f64>,
    pub filtered_longitude: Option<f64>,
    pub target_velocity_north_mps: Option<f64>,
    pub target_velocity_east_mps: Option<f64>,
    pub target_speed_mps: Option<f64>,
    pub target_direction_deg: Option<f64>,
    pub target_velocity_uncertainty_mps: Option<f64>,
    pub motion_status: String,
    pub rejection_code: String,
    pub rejection_reason: String,
    pub evidence: Option<serde_json::Value>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TerrainRefinementInput {
    pub geolocation_id: String,
    pub terrain_source: String,
    pub terrain_source_version: String,
    pub terrain_vertical_uncertainty_m: f64,
    pub convergence_threshold_m: f64,
    pub samples: Vec<TerrainSampleInput>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct TerrainSampleInput {
    pub latitude: f64,
    pub longitude: f64,
    pub altitude_amsl_m: f64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct OperationalTrackGeolocationSnapshot {
    pub geolocation: TrackGeolocationSnapshot,
    pub drone_name: String,
    pub class_label: String,
    pub lifecycle_state: String,
    pub observation_count: u64,
    pub selection_status: String,
    pub annotation_count: u64,
    pub evidence_marker_count: u64,
}

impl LocalDatabase {
    pub(crate) fn perception_track_geolocations(
        &self,
        track_session_id: &str,
        track_id: &str,
        limit: usize,
    ) -> Result<Vec<TrackGeolocationSnapshot>, String> {
        if track_session_id.trim().is_empty() || track_id.trim().is_empty() {
            return Err(
                "track session id and track id are required for geolocation history".into(),
            );
        }
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare(
                r#"
                SELECT id, command_id, selection_id, drone_id, track_session_id, track_id,
                       source_id, status, requested_by, requested_at_unix_ms,
                       resolved_at_unix_ms, aim_point, assumed_aim_point_height_m,
                       assumed_aim_point_height_uncertainty_m, ground_altitude_amsl_m,
                       ground_altitude_uncertainty_m, ground_altitude_source,
                       ground_altitude_source_version, ground_altitude_resolved_at_unix_ms,
                       latitude, longitude, altitude_amsl_m, horizontal_uncertainty_m,
                       method, frame_observed_at_unix_ms,
                       initial_latitude, initial_longitude, initial_altitude_amsl_m,
                       initial_horizontal_uncertainty_m, initial_method, refinement_status,
                       terrain_source, terrain_source_version, terrain_vertical_uncertainty_m,
                       terrain_iteration_count, terrain_residual_m, range_source,
                       filtered_latitude, filtered_longitude,
                       target_velocity_north_mps, target_velocity_east_mps,
                       target_speed_mps, target_direction_deg,
                       target_velocity_uncertainty_mps, motion_status,
                       rejection_code, rejection_reason, evidence_json
                FROM perception_track_geolocations
                WHERE track_session_id = ?1 AND track_id = ?2
                ORDER BY requested_at_unix_ms DESC, id DESC
                LIMIT ?3
                "#,
            )
            .map_err(|error| format!("prepare track geolocation history: {error}"))?;
        let records = statement
            .query_map(
                params![track_session_id, track_id, limit.clamp(1, 200) as i64],
                scan_geolocation,
            )
            .map_err(|error| format!("query track geolocation history: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read track geolocation history: {error}"))?;
        Ok(records)
    }

    pub(crate) fn operational_track_geolocations(
        &self,
        limit: usize,
    ) -> Result<Vec<OperationalTrackGeolocationSnapshot>, String> {
        let connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let mut statement = connection
            .prepare(
                r#"
                WITH ranked AS (
                    SELECT geolocations.*,
                           row_number() OVER (
                               PARTITION BY geolocations.track_session_id, geolocations.track_id
                               ORDER BY geolocations.frame_observed_at_unix_ms DESC,
                                        geolocations.resolved_at_unix_ms DESC,
                                        geolocations.id DESC
                           ) AS coordinate_rank
                    FROM perception_track_geolocations geolocations
                    WHERE geolocations.status = 'SUCCEEDED'
                )
                SELECT ranked.id, ranked.command_id, ranked.selection_id, ranked.drone_id,
                       ranked.track_session_id, ranked.track_id, ranked.source_id, ranked.status,
                       ranked.requested_by, ranked.requested_at_unix_ms, ranked.resolved_at_unix_ms,
                       ranked.aim_point, ranked.assumed_aim_point_height_m,
                       ranked.assumed_aim_point_height_uncertainty_m,
                       ranked.ground_altitude_amsl_m, ranked.ground_altitude_uncertainty_m,
                       ranked.ground_altitude_source, ranked.ground_altitude_source_version,
                       ranked.ground_altitude_resolved_at_unix_ms,
                       ranked.latitude, ranked.longitude, ranked.altitude_amsl_m,
                       ranked.horizontal_uncertainty_m, ranked.method,
                       ranked.frame_observed_at_unix_ms,
                       ranked.initial_latitude, ranked.initial_longitude,
                       ranked.initial_altitude_amsl_m, ranked.initial_horizontal_uncertainty_m,
                       ranked.initial_method, ranked.refinement_status,
                       ranked.terrain_source, ranked.terrain_source_version,
                       ranked.terrain_vertical_uncertainty_m, ranked.terrain_iteration_count,
                       ranked.terrain_residual_m, ranked.range_source,
                       ranked.filtered_latitude, ranked.filtered_longitude,
                       ranked.target_velocity_north_mps, ranked.target_velocity_east_mps,
                       ranked.target_speed_mps, ranked.target_direction_deg,
                       ranked.target_velocity_uncertainty_mps, ranked.motion_status,
                       ranked.rejection_code, ranked.rejection_reason, ranked.evidence_json,
                       drones.name, tracks.class_label, tracks.lifecycle_state,
                       tracks.observation_count,
                       COALESCE(selections.status, ''),
                       (SELECT count(*) FROM perception_track_annotations annotations
                        WHERE annotations.track_session_id = ranked.track_session_id
                          AND annotations.track_id = ranked.track_id),
                       (SELECT count(*) FROM perception_track_annotations annotations
                        WHERE annotations.track_session_id = ranked.track_session_id
                          AND annotations.track_id = ranked.track_id
                          AND annotations.annotation_type = 'EVIDENCE_MARKER')
                FROM ranked
                JOIN drones ON drones.id = ranked.drone_id
                JOIN perception_tracks tracks
                  ON tracks.track_session_id = ranked.track_session_id
                 AND tracks.id = ranked.track_id
                LEFT JOIN perception_track_selections selections ON selections.id = ranked.selection_id
                WHERE ranked.coordinate_rank = 1
                ORDER BY ranked.frame_observed_at_unix_ms DESC, ranked.resolved_at_unix_ms DESC
                LIMIT ?1
                "#,
            )
            .map_err(|error| format!("prepare operational track geolocations: {error}"))?;
        let records = statement
            .query_map([limit.clamp(1, 500) as i64], |row| {
                Ok(OperationalTrackGeolocationSnapshot {
                    geolocation: scan_geolocation(row)?,
                    drone_name: row.get(48)?,
                    class_label: row.get(49)?,
                    lifecycle_state: row.get(50)?,
                    observation_count: row.get::<_, i64>(51)? as u64,
                    selection_status: row.get(52)?,
                    annotation_count: row.get::<_, i64>(53)? as u64,
                    evidence_marker_count: row.get::<_, i64>(54)? as u64,
                })
            })
            .map_err(|error| format!("query operational track geolocations: {error}"))?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|error| format!("read operational track geolocations: {error}"))?;
        Ok(records)
    }
}

fn scan_geolocation(row: &Row<'_>) -> rusqlite::Result<TrackGeolocationSnapshot> {
    let evidence_json = row.get::<_, Option<String>>(47)?;
    Ok(TrackGeolocationSnapshot {
        id: row.get(0)?,
        command_id: row.get(1)?,
        selection_id: row.get(2)?,
        drone_id: row.get(3)?,
        track_session_id: row.get(4)?,
        track_id: row.get(5)?,
        source_id: row.get(6)?,
        status: row.get(7)?,
        requested_by: row.get(8)?,
        requested_at_unix_ms: row.get(9)?,
        resolved_at_unix_ms: row.get(10)?,
        aim_point: row.get(11)?,
        assumed_aim_point_height_m: row.get(12)?,
        assumed_aim_point_height_uncertainty_m: row.get(13)?,
        ground_altitude_amsl_m: row.get(14)?,
        ground_altitude_uncertainty_m: row.get(15)?,
        ground_altitude_source: row.get(16)?,
        ground_altitude_source_version: row.get(17)?,
        ground_altitude_resolved_at_unix_ms: row.get(18)?,
        latitude: row.get(19)?,
        longitude: row.get(20)?,
        altitude_amsl_m: row.get(21)?,
        horizontal_uncertainty_m: row.get(22)?,
        method: row.get(23)?,
        frame_observed_at_unix_ms: row.get(24)?,
        initial_latitude: row.get(25)?,
        initial_longitude: row.get(26)?,
        initial_altitude_amsl_m: row.get(27)?,
        initial_horizontal_uncertainty_m: row.get(28)?,
        initial_method: row.get(29)?,
        refinement_status: row.get(30)?,
        terrain_source: row.get(31)?,
        terrain_source_version: row.get(32)?,
        terrain_vertical_uncertainty_m: row.get(33)?,
        terrain_iteration_count: row.get(34)?,
        terrain_residual_m: row.get(35)?,
        range_source: row.get(36)?,
        filtered_latitude: row.get(37)?,
        filtered_longitude: row.get(38)?,
        target_velocity_north_mps: row.get(39)?,
        target_velocity_east_mps: row.get(40)?,
        target_speed_mps: row.get(41)?,
        target_direction_deg: row.get(42)?,
        target_velocity_uncertainty_mps: row.get(43)?,
        motion_status: row.get(44)?,
        rejection_code: row.get(45)?,
        rejection_reason: row.get(46)?,
        evidence: evidence_json.and_then(|value| serde_json::from_str(&value).ok()),
    })
}

impl LocalDatabase {
    pub(crate) fn refine_perception_track_geolocation(
        &self,
        input: &TerrainRefinementInput,
        now: i64,
    ) -> Result<TrackGeolocationSnapshot, String> {
        validate_refinement_input(input)?;
        let samples_json = serde_json::to_string(&input.samples)
            .map_err(|error| format!("encode terrain refinement samples: {error}"))?;
        let mut connection = self
            .connection
            .lock()
            .map_err(|_| "local SQLite mutex was poisoned".to_string())?;
        let tx = connection
            .transaction()
            .map_err(|error| format!("begin terrain refinement: {error}"))?;
        let request = tx
            .query_row(
                r#"
                SELECT track_session_id, track_id, assumed_aim_point_height_m,
                       ground_altitude_uncertainty_m, initial_latitude, initial_longitude,
                       initial_horizontal_uncertainty_m, frame_observed_at_unix_ms,
                       evidence_json
                FROM perception_track_geolocations
                WHERE id = ?1 AND status = 'SUCCEEDED'
                "#,
                [&input.geolocation_id],
                |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        row.get::<_, String>(1)?,
                        row.get::<_, f64>(2)?,
                        row.get::<_, f64>(3)?,
                        row.get::<_, Option<f64>>(4)?,
                        row.get::<_, Option<f64>>(5)?,
                        row.get::<_, Option<f64>>(6)?,
                        row.get::<_, Option<i64>>(7)?,
                        row.get::<_, Option<String>>(8)?,
                    ))
                },
            )
            .optional()
            .map_err(|error| format!("read geolocation for terrain refinement: {error}"))?
            .ok_or_else(|| {
                "only a successful persisted geolocation can be terrain-refined".to_string()
            })?;
        let (
            track_session_id,
            track_id,
            aim_height_m,
            original_ground_uncertainty_m,
            initial_latitude,
            initial_longitude,
            initial_uncertainty_m,
            frame_observed_at_unix_ms,
            evidence_json,
        ) = request;
        let initial_latitude =
            initial_latitude.ok_or_else(|| "geolocation has no initial latitude".to_string())?;
        let initial_longitude =
            initial_longitude.ok_or_else(|| "geolocation has no initial longitude".to_string())?;
        let initial_uncertainty_m = initial_uncertainty_m
            .ok_or_else(|| "geolocation has no initial uncertainty".to_string())?;
        let evidence = evidence_json
            .as_deref()
            .and_then(|value| serde_json::from_str::<serde_json::Value>(value).ok())
            .ok_or_else(|| "geolocation has no valid estimator evidence".to_string())?;
        let estimate = &evidence["estimate"];
        let ray = TerrainRay {
            origin_latitude: json_finite(estimate, &["origin", "latitudeDeg"])?,
            origin_longitude: json_finite(estimate, &["origin", "longitudeDeg"])?,
            origin_altitude_m: json_finite(estimate, &["origin", "altitudeMeters"])?,
            north: json_finite(estimate, &["worldDirectionNed", "x"])?,
            east: json_finite(estimate, &["worldDirectionNed", "y"])?,
            down: json_finite(estimate, &["worldDirectionNed", "z"])?,
        };
        ray.validate()?;

        let mut expected = GeodeticCoordinate {
            latitude: initial_latitude,
            longitude: initial_longitude,
        };
        let mut residual_m = f64::INFINITY;
        let mut final_coordinate = expected;
        for (index, sample) in input.samples.iter().enumerate() {
            let sample_gap_m = horizontal_distance_m(
                expected.latitude,
                expected.longitude,
                sample.latitude,
                sample.longitude,
            );
            if !sample_gap_m.is_finite()
                || sample_gap_m > input.convergence_threshold_m.max(1.0) * 3.0
            {
                return Err(format!(
                    "terrain sample {} is {:.2} m from the expected ray iteration coordinate",
                    index + 1,
                    sample_gap_m
                ));
            }
            final_coordinate = ray.intersection(sample.altitude_amsl_m + aim_height_m)?;
            residual_m = horizontal_distance_m(
                sample.latitude,
                sample.longitude,
                final_coordinate.latitude,
                final_coordinate.longitude,
            );
            expected = final_coordinate;
        }
        let refinement_status = if residual_m <= input.convergence_threshold_m {
            "CONVERGED"
        } else {
            "MAX_ITERATIONS"
        };
        let last_sample = input.samples.last().expect("validated non-empty samples");
        let horizontal_projection = ray.north.hypot(ray.east) / ray.down;
        let uncertainty_without_initial_ground = (initial_uncertainty_m
            - original_ground_uncertainty_m * horizontal_projection)
            .max(0.0);
        let refined_uncertainty_m = uncertainty_without_initial_ground
            + input.terrain_vertical_uncertainty_m * horizontal_projection
            + residual_m;
        tx.execute(
            r#"
            UPDATE perception_track_geolocations
            SET latitude = ?2, longitude = ?3, altitude_amsl_m = ?4,
                horizontal_uncertainty_m = ?5, method = 'BORESIGHT_TERRAIN_ITERATIVE',
                refinement_status = ?6, terrain_source = ?7,
                terrain_source_version = ?8, terrain_vertical_uncertainty_m = ?9,
                terrain_iteration_count = ?10, terrain_residual_m = ?11,
                terrain_samples_json = ?12, updated_at_unix_ms = ?13
            WHERE id = ?1 AND status = 'SUCCEEDED'
            "#,
            params![
                input.geolocation_id,
                final_coordinate.latitude,
                final_coordinate.longitude,
                last_sample.altitude_amsl_m + aim_height_m,
                refined_uncertainty_m,
                refinement_status,
                input.terrain_source,
                input.terrain_source_version,
                input.terrain_vertical_uncertainty_m,
                input.samples.len() as i64,
                residual_m,
                samples_json,
                now,
            ],
        )
        .map_err(|error| format!("persist terrain-refined geolocation: {error}"))?;

        update_motion_filter(
            &tx,
            &input.geolocation_id,
            &track_session_id,
            &track_id,
            frame_observed_at_unix_ms,
            final_coordinate.latitude,
            final_coordinate.longitude,
            refined_uncertainty_m,
        )?;
        tx.commit()
            .map_err(|error| format!("commit terrain-refined geolocation: {error}"))?;
        drop(connection);
        self.perception_track_geolocations(&track_session_id, &track_id, 200)?
            .into_iter()
            .find(|snapshot| snapshot.id == input.geolocation_id)
            .ok_or_else(|| "terrain-refined geolocation disappeared after commit".to_string())
    }
}

fn validate_refinement_input(input: &TerrainRefinementInput) -> Result<(), String> {
    if input.geolocation_id.trim().is_empty()
        || input.terrain_source.trim().is_empty()
        || input.terrain_source_version.trim().is_empty()
    {
        return Err("geolocation identity and terrain provenance are required".into());
    }
    if input.terrain_source.len() > 240 || input.terrain_source_version.len() > 240 {
        return Err("terrain provenance cannot exceed 240 characters".into());
    }
    if !input.terrain_vertical_uncertainty_m.is_finite()
        || !(0.0..=100.0).contains(&input.terrain_vertical_uncertainty_m)
    {
        return Err("terrain vertical uncertainty must be between 0 and 100 metres".into());
    }
    if !input.convergence_threshold_m.is_finite()
        || !(0.25..=10.0).contains(&input.convergence_threshold_m)
    {
        return Err("terrain convergence threshold must be between 0.25 and 10 metres".into());
    }
    if input.samples.is_empty() || input.samples.len() > 8 {
        return Err("terrain refinement requires between one and eight ordered samples".into());
    }
    for sample in &input.samples {
        if !valid_coordinate(sample.latitude, sample.longitude)
            || !sample.altitude_amsl_m.is_finite()
            || !(-500.0..=9_000.0).contains(&sample.altitude_amsl_m)
        {
            return Err("terrain refinement sample coordinate or altitude is invalid".into());
        }
    }
    Ok(())
}

#[derive(Clone, Copy)]
struct TerrainRay {
    origin_latitude: f64,
    origin_longitude: f64,
    origin_altitude_m: f64,
    north: f64,
    east: f64,
    down: f64,
}

#[derive(Clone, Copy)]
struct GeodeticCoordinate {
    latitude: f64,
    longitude: f64,
}

impl TerrainRay {
    fn validate(self) -> Result<(), String> {
        let norm = (self.north * self.north + self.east * self.east + self.down * self.down).sqrt();
        if !valid_coordinate(self.origin_latitude, self.origin_longitude)
            || !self.origin_altitude_m.is_finite()
            || !norm.is_finite()
            || !(0.9..=1.1).contains(&norm)
            || self.down <= 0.0
        {
            return Err("estimator evidence contains an invalid downward world-space ray".into());
        }
        Ok(())
    }

    fn intersection(self, altitude_m: f64) -> Result<GeodeticCoordinate, String> {
        let vertical_drop = self.origin_altitude_m - altitude_m;
        if !altitude_m.is_finite() || vertical_drop <= 0.0 {
            return Err("terrain intersection must remain below the observation origin".into());
        }
        let slant_range_m = vertical_drop / self.down;
        let ground_range_m = slant_range_m * self.north.hypot(self.east);
        if !slant_range_m.is_finite() || slant_range_m <= 0.0 || ground_range_m > 3_000.0 {
            return Err("terrain intersection exceeds the bounded 3 km ground range".into());
        }
        offset_geodetic(
            self.origin_latitude,
            self.origin_longitude,
            altitude_m,
            slant_range_m * self.north,
            slant_range_m * self.east,
        )
    }
}

fn json_finite(value: &serde_json::Value, path: &[&str]) -> Result<f64, String> {
    let mut current = value;
    for component in path {
        current = &current[*component];
    }
    current
        .as_f64()
        .filter(|number| number.is_finite())
        .ok_or_else(|| format!("estimator evidence is missing {}", path.join(".")))
}

fn update_motion_filter(
    tx: &Transaction<'_>,
    geolocation_id: &str,
    track_session_id: &str,
    track_id: &str,
    frame_observed_at_unix_ms: Option<i64>,
    latitude: f64,
    longitude: f64,
    uncertainty_m: f64,
) -> Result<(), String> {
    let Some(observed_at) = frame_observed_at_unix_ms else {
        return Ok(());
    };
    let previous = tx
        .query_row(
            r#"
            SELECT COALESCE(filtered_latitude, latitude),
                   COALESCE(filtered_longitude, longitude),
                   target_velocity_north_mps, target_velocity_east_mps,
                   horizontal_uncertainty_m, target_velocity_uncertainty_mps,
                   frame_observed_at_unix_ms
            FROM perception_track_geolocations
            WHERE track_session_id = ?1 AND track_id = ?2 AND id <> ?3
              AND status = 'SUCCEEDED'
              AND refinement_status IN ('CONVERGED', 'MAX_ITERATIONS')
              AND frame_observed_at_unix_ms < ?4
            ORDER BY frame_observed_at_unix_ms DESC, resolved_at_unix_ms DESC
            LIMIT 1
            "#,
            params![track_session_id, track_id, geolocation_id, observed_at],
            |row| {
                Ok((
                    row.get::<_, f64>(0)?,
                    row.get::<_, f64>(1)?,
                    row.get::<_, Option<f64>>(2)?,
                    row.get::<_, Option<f64>>(3)?,
                    row.get::<_, f64>(4)?,
                    row.get::<_, Option<f64>>(5)?,
                    row.get::<_, i64>(6)?,
                ))
            },
        )
        .optional()
        .map_err(|error| format!("read prior target motion state: {error}"))?;
    let Some((
        previous_latitude,
        previous_longitude,
        previous_north_mps,
        previous_east_mps,
        previous_uncertainty_m,
        previous_velocity_uncertainty_mps,
        previous_observed_at,
    )) = previous
    else {
        tx.execute(
            "UPDATE perception_track_geolocations SET filtered_latitude = ?2, filtered_longitude = ?3, motion_status = 'INSUFFICIENT_HISTORY' WHERE id = ?1",
            params![geolocation_id, latitude, longitude],
        )
        .map_err(|error| format!("seed target motion filter: {error}"))?;
        return Ok(());
    };
    let elapsed_s = (observed_at - previous_observed_at) as f64 / 1_000.0;
    if !(0.2..=30.0).contains(&elapsed_s) {
        tx.execute(
            "UPDATE perception_track_geolocations SET filtered_latitude = ?2, filtered_longitude = ?3, motion_status = 'TIME_GAP_RESET' WHERE id = ?1",
            params![geolocation_id, latitude, longitude],
        )
        .map_err(|error| format!("reset target motion filter: {error}"))?;
        return Ok(());
    }
    let previous_north_mps = previous_north_mps.unwrap_or(0.0);
    let previous_east_mps = previous_east_mps.unwrap_or(0.0);
    let predicted = offset_geodetic(
        previous_latitude,
        previous_longitude,
        0.0,
        previous_north_mps * elapsed_s,
        previous_east_mps * elapsed_s,
    )?;
    let (residual_north_m, residual_east_m) =
        local_offset_m(predicted.latitude, predicted.longitude, latitude, longitude);
    let residual_speed_mps = residual_north_m.hypot(residual_east_m) / elapsed_s;
    if residual_speed_mps > 150.0 {
        tx.execute(
            r#"
            UPDATE perception_track_geolocations
            SET filtered_latitude = ?2, filtered_longitude = ?3,
                target_velocity_north_mps = ?4, target_velocity_east_mps = ?5,
                target_speed_mps = ?6, target_direction_deg = ?7,
                target_velocity_uncertainty_mps = ?8, motion_status = 'OUTLIER_REJECTED'
            WHERE id = ?1
            "#,
            params![
                geolocation_id,
                predicted.latitude,
                predicted.longitude,
                previous_north_mps,
                previous_east_mps,
                previous_north_mps.hypot(previous_east_mps),
                direction_degrees(previous_north_mps, previous_east_mps),
                previous_velocity_uncertainty_mps,
            ],
        )
        .map_err(|error| format!("reject target motion outlier: {error}"))?;
        return Ok(());
    }
    let position_gain =
        (previous_uncertainty_m / (previous_uncertainty_m + uncertainty_m)).clamp(0.2, 0.8);
    let velocity_gain = (elapsed_s / (elapsed_s + 4.0)).clamp(0.1, 0.6);
    let filtered = offset_geodetic(
        predicted.latitude,
        predicted.longitude,
        0.0,
        residual_north_m * position_gain,
        residual_east_m * position_gain,
    )?;
    let north_mps = previous_north_mps + velocity_gain * residual_north_m / elapsed_s;
    let east_mps = previous_east_mps + velocity_gain * residual_east_m / elapsed_s;
    let speed_mps = north_mps.hypot(east_mps);
    let measurement_velocity_uncertainty_mps =
        previous_uncertainty_m.hypot(uncertainty_m) / elapsed_s;
    let velocity_uncertainty_mps = previous_velocity_uncertainty_mps
        .map(|prior| {
            (1.0 - velocity_gain) * prior + velocity_gain * measurement_velocity_uncertainty_mps
        })
        .unwrap_or(measurement_velocity_uncertainty_mps);
    let motion_status = if speed_mps > velocity_uncertainty_mps {
        "FILTERED"
    } else {
        "BELOW_UNCERTAINTY"
    };
    tx.execute(
        r#"
        UPDATE perception_track_geolocations
        SET filtered_latitude = ?2, filtered_longitude = ?3,
            target_velocity_north_mps = ?4, target_velocity_east_mps = ?5,
            target_speed_mps = ?6, target_direction_deg = ?7,
            target_velocity_uncertainty_mps = ?8, motion_status = ?9
        WHERE id = ?1
        "#,
        params![
            geolocation_id,
            filtered.latitude,
            filtered.longitude,
            north_mps,
            east_mps,
            speed_mps,
            direction_degrees(north_mps, east_mps),
            velocity_uncertainty_mps,
            motion_status,
        ],
    )
    .map_err(|error| format!("persist target motion filter: {error}"))?;
    Ok(())
}

fn direction_degrees(north_mps: f64, east_mps: f64) -> Option<f64> {
    let speed = north_mps.hypot(east_mps);
    (speed > 1e-6).then(|| (east_mps.atan2(north_mps) * 180.0 / PI + 360.0) % 360.0)
}

fn valid_coordinate(latitude: f64, longitude: f64) -> bool {
    latitude.is_finite()
        && longitude.is_finite()
        && (-90.0..=90.0).contains(&latitude)
        && (-180.0..=180.0).contains(&longitude)
}

fn horizontal_distance_m(
    latitude_a: f64,
    longitude_a: f64,
    latitude_b: f64,
    longitude_b: f64,
) -> f64 {
    let (north_m, east_m) = local_offset_m(latitude_a, longitude_a, latitude_b, longitude_b);
    north_m.hypot(east_m)
}

fn local_offset_m(
    latitude_a: f64,
    longitude_a: f64,
    latitude_b: f64,
    longitude_b: f64,
) -> (f64, f64) {
    const EARTH_RADIUS_M: f64 = 6_378_137.0;
    let mean_latitude = (latitude_a + latitude_b) * 0.5 * PI / 180.0;
    let north_m = (latitude_b - latitude_a) * PI / 180.0 * EARTH_RADIUS_M;
    let east_m = (longitude_b - longitude_a) * PI / 180.0 * EARTH_RADIUS_M * mean_latitude.cos();
    (north_m, east_m)
}

fn offset_geodetic(
    latitude_deg: f64,
    longitude_deg: f64,
    altitude_m: f64,
    north_m: f64,
    east_m: f64,
) -> Result<GeodeticCoordinate, String> {
    if !valid_coordinate(latitude_deg, longitude_deg)
        || !altitude_m.is_finite()
        || !north_m.is_finite()
        || !east_m.is_finite()
    {
        return Err("geodetic offset input is invalid".into());
    }
    const WGS84_SEMI_MAJOR_M: f64 = 6_378_137.0;
    const WGS84_ECCENTRICITY_SQUARED: f64 = 6.694_379_990_14e-3;
    let latitude = latitude_deg * PI / 180.0;
    let sin_latitude = latitude.sin();
    let denominator = (1.0 - WGS84_ECCENTRICITY_SQUARED * sin_latitude * sin_latitude).sqrt();
    let prime_vertical_radius = WGS84_SEMI_MAJOR_M / denominator;
    let meridian_radius =
        WGS84_SEMI_MAJOR_M * (1.0 - WGS84_ECCENTRICITY_SQUARED) / denominator.powi(3);
    let latitude_out = latitude + north_m / (meridian_radius + altitude_m);
    let longitude_scale = (prime_vertical_radius + altitude_m) * latitude.cos();
    if longitude_scale.abs() < 1e-6 {
        return Err("geodetic offset is undefined at the pole".into());
    }
    let longitude_out = longitude_deg * PI / 180.0 + east_m / longitude_scale;
    let result = GeodeticCoordinate {
        latitude: latitude_out * 180.0 / PI,
        longitude: longitude_out * 180.0 / PI,
    };
    if !valid_coordinate(result.latitude, result.longitude) {
        return Err("geodetic offset produced an invalid coordinate".into());
    }
    Ok(result)
}

#[cfg(test)]
mod tests {
    use rusqlite::{params, Connection};

    use super::{offset_geodetic, update_motion_filter};

    #[test]
    fn successive_world_coordinates_produce_filtered_speed_and_direction() {
        let mut connection = Connection::open_in_memory().expect("open motion filter database");
        connection
            .execute_batch(
                r#"
                CREATE TABLE perception_track_geolocations (
                    id TEXT PRIMARY KEY,
                    track_session_id TEXT NOT NULL,
                    track_id TEXT NOT NULL,
                    status TEXT NOT NULL,
                    refinement_status TEXT NOT NULL,
                    frame_observed_at_unix_ms INTEGER,
                    resolved_at_unix_ms INTEGER,
                    latitude REAL,
                    longitude REAL,
                    horizontal_uncertainty_m REAL,
                    filtered_latitude REAL,
                    filtered_longitude REAL,
                    target_velocity_north_mps REAL,
                    target_velocity_east_mps REAL,
                    target_speed_mps REAL,
                    target_direction_deg REAL,
                    target_velocity_uncertainty_mps REAL,
                    motion_status TEXT NOT NULL
                );
                "#,
            )
            .expect("create motion filter table");
        let start = (51.5, -0.14);
        let moved = offset_geodetic(start.0, start.1, 0.0, 10.0, 0.0).expect("offset target");
        connection
            .execute(
                "INSERT INTO perception_track_geolocations (id, track_session_id, track_id, status, refinement_status, frame_observed_at_unix_ms, resolved_at_unix_ms, latitude, longitude, horizontal_uncertainty_m, motion_status) VALUES ('first', 'session', 'track', 'SUCCEEDED', 'CONVERGED', 1000, 1001, ?1, ?2, 1, 'INSUFFICIENT_HISTORY')",
                params![start.0, start.1],
            )
            .expect("insert first coordinate");
        connection
            .execute(
                "INSERT INTO perception_track_geolocations (id, track_session_id, track_id, status, refinement_status, frame_observed_at_unix_ms, resolved_at_unix_ms, latitude, longitude, horizontal_uncertainty_m, motion_status) VALUES ('second', 'session', 'track', 'SUCCEEDED', 'CONVERGED', 3000, 3001, ?1, ?2, 1, 'INSUFFICIENT_HISTORY')",
                params![moved.latitude, moved.longitude],
            )
            .expect("insert second coordinate");
        let tx = connection.transaction().expect("begin motion update");
        update_motion_filter(
            &tx,
            "first",
            "session",
            "track",
            Some(1000),
            start.0,
            start.1,
            1.0,
        )
        .expect("seed motion filter");
        update_motion_filter(
            &tx,
            "second",
            "session",
            "track",
            Some(3000),
            moved.latitude,
            moved.longitude,
            1.0,
        )
        .expect("update motion filter");
        tx.commit().expect("commit motion update");
        let (speed, direction, status): (f64, f64, String) = connection
            .query_row(
                "SELECT target_speed_mps, target_direction_deg, motion_status FROM perception_track_geolocations WHERE id = 'second'",
                [],
                |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
            )
            .expect("read filtered motion");
        assert!((speed - 5.0 / 3.0).abs() < 0.05, "speed = {speed}");
        assert!(
            direction < 0.1 || direction > 359.9,
            "direction = {direction}"
        );
        assert_eq!(status, "FILTERED");
    }
}

pub(super) fn insert_request(
    tx: &Transaction<'_>,
    command_id: &str,
    drone_id: &str,
    parameters: &serde_json::Value,
    now: i64,
) -> Result<(), String> {
    let string = |key: &str| {
        parameters[key]
            .as_str()
            .ok_or_else(|| format!("{key} must be a string"))
    };
    let number = |key: &str| {
        parameters[key]
            .as_f64()
            .ok_or_else(|| format!("{key} must be a number"))
    };
    let integer = |key: &str| {
        parameters[key]
            .as_i64()
            .ok_or_else(|| format!("{key} must be an integer"))
    };
    let id: String = tx
        .query_row("SELECT lower(hex(randomblob(16)))", [], |row| row.get(0))
        .map_err(|error| format!("generate track geolocation identifier: {error}"))?;
    tx.execute(
        r#"
        INSERT INTO perception_track_geolocations (
            id, command_id, selection_id, drone_id, track_session_id, track_id,
            source_id, status, requested_by, requested_at_unix_ms, aim_point,
            assumed_aim_point_height_m, assumed_aim_point_height_uncertainty_m,
            ground_altitude_amsl_m, ground_altitude_uncertainty_m,
            ground_altitude_source, ground_altitude_source_version,
            ground_altitude_resolved_at_unix_ms, created_at_unix_ms, updated_at_unix_ms
        ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, 'REQUESTED', ?8, ?9, ?10,
                  ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?9, ?9)
        "#,
        params![
            id,
            command_id,
            string("selectionId")?,
            drone_id,
            string("trackSessionId")?,
            string("trackId")?,
            string("sourceId")?,
            string("requestedBy")?,
            now,
            string("aimPoint")?,
            number("assumedAimPointHeightMeters")?,
            number("assumedAimPointHeightUncertaintyMeters")?,
            number("groundAltitudeAmslMeters")?,
            number("groundAltitudeUncertaintyMeters")?,
            string("groundAltitudeSource")?,
            string("groundAltitudeSourceVersion")?,
            integer("groundAltitudeResolvedAtUnixMs")?,
        ],
    )
    .map_err(|error| format!("insert selected-track geolocation request: {error}"))?;
    Ok(())
}

pub(super) fn resolve_from_command_update(
    tx: &Transaction<'_>,
    command_id: &str,
    command_state: &str,
    result_code: &str,
    message: &str,
    evidence_json: Option<&str>,
    occurred_at_unix_ms: i64,
) -> Result<(), String> {
    let request = tx
        .query_row(
            "SELECT selection_id, source_id, track_session_id, track_id FROM perception_track_geolocations WHERE command_id = ?1 AND status = 'REQUESTED'",
            [command_id],
            |row| Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?, row.get::<_, String>(2)?, row.get::<_, String>(3)?)),
        )
        .optional()
        .map_err(|error| format!("read pending track geolocation request: {error}"))?;
    let Some((selection_id, source_id, track_session_id, track_id)) = request else {
        return Ok(());
    };
    if command_state != "succeeded" {
        let code = if result_code.trim().is_empty() {
            format!("GEOLOCATION_COMMAND_{}", command_state.to_ascii_uppercase())
        } else {
            result_code.to_string()
        };
        let reason = if message.trim().is_empty() {
            format!("selected-track geolocation command ended in {command_state}")
        } else {
            message.to_string()
        };
        return reject(
            tx,
            command_id,
            &code,
            &reason,
            evidence_json,
            occurred_at_unix_ms,
        );
    }

    let parsed = evidence_json
        .and_then(|value| serde_json::from_str::<serde_json::Value>(value).ok())
        .ok_or_else(|| {
            "successful geolocation command did not include valid JSON evidence".to_string()
        });
    let evidence = match parsed {
        Ok(value) => value,
        Err(reason) => {
            return reject(
                tx,
                command_id,
                "GEOLOCATION_INVALID_AGENT_EVIDENCE",
                &reason,
                evidence_json,
                occurred_at_unix_ms,
            )
        }
    };
    let identity_matches = evidence["schemaVersion"].as_i64() == Some(1)
        && evidence["selectionId"].as_str() == Some(selection_id.as_str())
        && evidence["sourceId"].as_str() == Some(source_id.as_str())
        && evidence["trackSessionId"].as_str() == Some(track_session_id.as_str())
        && evidence["trackId"].as_str() == Some(track_id.as_str());
    let estimate = &evidence["estimate"];
    let latitude = estimate["intersection"]["latitudeDeg"].as_f64();
    let longitude = estimate["intersection"]["longitudeDeg"].as_f64();
    let altitude = estimate["intersection"]["altitudeMeters"].as_f64();
    let uncertainty = estimate["uncertainty"]["horizontalRadiusMeters"].as_f64();
    let method = estimate["method"].as_str().unwrap_or_default();
    let frame_observed_at_unix_ms = estimate["frameTime"]["observedAtUnixNs"]
        .as_i64()
        .map(|value| value / 1_000_000);
    let valid = identity_matches
        && latitude.is_some_and(|value| value.is_finite() && (-90.0..=90.0).contains(&value))
        && longitude.is_some_and(|value| value.is_finite() && (-180.0..=180.0).contains(&value))
        && altitude.is_some_and(f64::is_finite)
        && uncertainty.is_some_and(|value| value.is_finite() && value >= 0.0)
        && !method.is_empty();
    if !valid {
        return reject(
            tx,
            command_id,
            "GEOLOCATION_INVALID_AGENT_EVIDENCE",
            "agent evidence identity or coordinate fields did not match the durable request",
            evidence_json,
            occurred_at_unix_ms,
        );
    }
    tx.execute(
        r#"
        UPDATE perception_track_geolocations
        SET status = 'SUCCEEDED', resolved_at_unix_ms = ?2,
            latitude = ?3, longitude = ?4, altitude_amsl_m = ?5,
            horizontal_uncertainty_m = ?6, method = ?7,
            initial_latitude = ?3, initial_longitude = ?4,
            initial_altitude_amsl_m = ?5,
            initial_horizontal_uncertainty_m = ?6,
            initial_method = ?7,
            frame_observed_at_unix_ms = ?8, evidence_json = ?9,
            updated_at_unix_ms = ?2
        WHERE command_id = ?1 AND status = 'REQUESTED'
        "#,
        params![
            command_id,
            occurred_at_unix_ms,
            latitude,
            longitude,
            altitude,
            uncertainty,
            method,
            frame_observed_at_unix_ms,
            evidence_json,
        ],
    )
    .map_err(|error| format!("persist selected-track geolocation result: {error}"))?;
    Ok(())
}

fn reject(
    tx: &Transaction<'_>,
    command_id: &str,
    code: &str,
    reason: &str,
    evidence_json: Option<&str>,
    occurred_at_unix_ms: i64,
) -> Result<(), String> {
    tx.execute(
        r#"
        UPDATE perception_track_geolocations
        SET status = 'REJECTED', resolved_at_unix_ms = ?2,
            rejection_code = ?3, rejection_reason = ?4,
            evidence_json = ?5, updated_at_unix_ms = ?2
        WHERE command_id = ?1 AND status = 'REQUESTED'
        "#,
        params![command_id, occurred_at_unix_ms, code, reason, evidence_json],
    )
    .map_err(|error| format!("persist selected-track geolocation rejection: {error}"))?;
    Ok(())
}
