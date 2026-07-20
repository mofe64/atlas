use std::{env, fs, path::PathBuf};

use serde::{Deserialize, Serialize};
use serde_json::Value;

use super::missions::MissionWaypoint;

const EARTH_RADIUS_M: f64 = 6_371_000.0;
const MAX_BUILDING_FEATURES: usize = 100_000;

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct KnownBuildingProvenance {
    pub provider: String,
    pub product: String,
    pub dataset_id: String,
    pub schema_version: String,
    pub release: String,
    pub retrieved_at_unix_ms: i64,
    pub coverage_bbox: [f64; 4],
}

#[derive(Debug, Clone)]
pub(crate) struct KnownBuildingDataset {
    pub provenance: KnownBuildingProvenance,
    buildings: Vec<KnownBuilding>,
}

#[derive(Debug, Clone)]
struct KnownBuilding {
    feature_id: String,
    polygons: Vec<Vec<GeoPoint>>,
    absolute_base_meters: Option<f64>,
    absolute_top_meters: Option<f64>,
    relative_top_meters: Option<f64>,
    height_source: String,
    height_confidence: Option<String>,
    evidence_date: Option<String>,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct GeoPoint {
    pub latitude: f64,
    pub longitude: f64,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct RouteAssessmentStart {
    pub latitude: f64,
    pub longitude: f64,
    pub relative_altitude_meters: f64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct KnownBuildingAssessment {
    pub status: String,
    pub statement: String,
    pub checked_feature_count: usize,
    pub horizontal_clearance_meters: f64,
    pub vertical_clearance_meters: f64,
    pub home_absolute_altitude_meters: Option<f64>,
    pub route_start: Option<RouteAssessmentStart>,
    pub route_segment_count: usize,
    pub intersection_count: usize,
    pub unknown_height_count: usize,
    pub coverage_complete: bool,
    pub override_required: bool,
    pub override_reason: Option<String>,
    pub provenance: Option<KnownBuildingProvenance>,
    pub issues: Vec<KnownBuildingIssue>,
    pub limitations: Vec<String>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct KnownBuildingIssue {
    pub feature_id: String,
    pub result: String,
    pub route_segment_indexes: Vec<usize>,
    pub route_point_indexes: Vec<usize>,
    pub absolute_base_meters: Option<f64>,
    pub absolute_top_meters: Option<f64>,
    pub relative_top_meters: Option<f64>,
    pub height_source: String,
    pub height_confidence: Option<String>,
    pub evidence_date: Option<String>,
    pub footprint: Vec<GeoPoint>,
}

impl KnownBuildingDataset {
    pub(crate) fn from_environment() -> Result<Option<Self>, String> {
        let Some(raw_path) = env::var_os("ATLAS_KNOWN_BUILDINGS_GEOJSON") else {
            return Ok(None);
        };
        if raw_path.is_empty() {
            return Ok(None);
        }
        let path = PathBuf::from(raw_path);
        if !path.is_absolute() {
            return Err("ATLAS_KNOWN_BUILDINGS_GEOJSON must be an absolute path".to_string());
        }
        let bytes = fs::read(&path)
            .map_err(|error| format!("read configured known-building GeoJSON: {error}"))?;
        Self::from_geojson_slice(&bytes)
            .map(Some)
            .map_err(|error| format!("parse {}: {error}", path.display()))
    }

    pub(crate) fn from_geojson_slice(bytes: &[u8]) -> Result<Self, String> {
        let root: Value = serde_json::from_slice(bytes)
            .map_err(|error| format!("known-building data is not valid JSON: {error}"))?;
        if root.get("type").and_then(Value::as_str) != Some("FeatureCollection") {
            return Err("known-building GeoJSON must be a FeatureCollection".to_string());
        }
        let provenance: KnownBuildingProvenance = serde_json::from_value(
            root.get("atlasProvenance")
                .cloned()
                .ok_or_else(|| "known-building GeoJSON requires atlasProvenance".to_string())?,
        )
        .map_err(|error| format!("known-building atlasProvenance is invalid: {error}"))?;
        validate_provenance(&provenance)?;
        let features = root
            .get("features")
            .and_then(Value::as_array)
            .ok_or_else(|| "known-building GeoJSON requires a features array".to_string())?;
        if features.len() > MAX_BUILDING_FEATURES {
            return Err(format!(
                "known-building dataset contains more than {MAX_BUILDING_FEATURES} features"
            ));
        }
        let mut buildings = Vec::with_capacity(features.len());
        for (index, feature) in features.iter().enumerate() {
            buildings.push(parse_building(feature, index)?);
        }
        Ok(Self {
            provenance,
            buildings,
        })
    }
}

pub(crate) fn assess_known_buildings(
    dataset: Option<&KnownBuildingDataset>,
    start: Option<RouteAssessmentStart>,
    waypoints: &[MissionWaypoint],
    home_absolute_altitude_meters: Option<f64>,
    horizontal_clearance_meters: f64,
    vertical_clearance_meters: f64,
    override_reason: Option<&str>,
) -> Result<KnownBuildingAssessment, String> {
    if !(0.0..=100.0).contains(&horizontal_clearance_meters) {
        return Err("building horizontal clearance must be between 0 and 100 metres".into());
    }
    if !(0.0..=100.0).contains(&vertical_clearance_meters) {
        return Err("building vertical clearance must be between 0 and 100 metres".into());
    }
    let override_reason = override_reason
        .map(str::trim)
        .filter(|value| !value.is_empty());
    if override_reason.is_some_and(|reason| reason.chars().count() > 500) {
        return Err("known-building override reason must be 500 characters or fewer".into());
    }
    let mut route = Vec::with_capacity(waypoints.len() + usize::from(start.is_some()));
    if let Some(start) = start {
        route.push(RoutePoint {
            point: GeoPoint {
                latitude: start.latitude,
                longitude: start.longitude,
            },
            relative_altitude_meters: start.relative_altitude_meters,
        });
    }
    route.extend(waypoints.iter().map(|waypoint| RoutePoint {
        point: GeoPoint {
            latitude: waypoint.latitude,
            longitude: waypoint.longitude,
        },
        relative_altitude_meters: waypoint.altitude_meters,
    }));
    let route_segment_count = route.len().saturating_sub(1);
    let mut limitations = vec![
        "This checks only the configured known-building dataset; wires, cranes, vegetation, temporary objects, vehicles and unmapped structures are not covered.".to_string(),
        "This is not obstacle avoidance and does not establish that the route is safe or obstacle-free.".to_string(),
    ];
    if start.is_none() {
        limitations.push(
            "Aircraft departure position was unavailable; the first waypoint was assessed as a point, but the route into it was not assessed. Re-prepare when live position is available."
                .to_string(),
        );
    }
    let Some(dataset) = dataset else {
        return Ok(KnownBuildingAssessment {
            status: "DATA_UNAVAILABLE".into(),
            statement: "Known-building dataset unavailable; route assessment incomplete.".into(),
            checked_feature_count: 0,
            horizontal_clearance_meters,
            vertical_clearance_meters,
            home_absolute_altitude_meters,
            route_start: start,
            route_segment_count,
            intersection_count: 0,
            unknown_height_count: 0,
            coverage_complete: false,
            override_required: true,
            override_reason: override_reason.map(str::to_string),
            provenance: None,
            issues: vec![],
            limitations,
        });
    };

    let coverage_complete = route.iter().all(|point| {
        bbox_contains(
            dataset.provenance.coverage_bbox,
            point.point,
            horizontal_clearance_meters,
        )
    });
    let mut issues = Vec::new();
    let mut intersection_count = 0;
    let mut unknown_height_count = 0;
    for building in &dataset.buildings {
        let segment_indexes = route
            .windows(2)
            .enumerate()
            .filter_map(|(index, segment)| {
                building
                    .polygons
                    .iter()
                    .any(|polygon| {
                        segment_near_polygon(
                            segment[0].point,
                            segment[1].point,
                            polygon,
                            horizontal_clearance_meters,
                        )
                    })
                    .then_some(index)
            })
            .collect::<Vec<_>>();
        let point_indexes = if route.len() == 1
            && building.polygons.iter().any(|polygon| {
                segment_near_polygon(
                    route[0].point,
                    route[0].point,
                    polygon,
                    horizontal_clearance_meters,
                )
            }) {
            vec![0]
        } else {
            Vec::new()
        };
        if segment_indexes.is_empty() && point_indexes.is_empty() {
            continue;
        }
        let absolute_top = building.absolute_top_meters.or_else(|| {
            building
                .absolute_base_meters
                .zip(building.relative_top_meters)
                .map(|(base, relative)| base + relative)
        });
        let result = if let (Some(home), Some(top)) = (home_absolute_altitude_meters, absolute_top)
        {
            let intersects = segment_indexes.iter().any(|index| {
                let segment = &route[*index..=*index + 1];
                let minimum_route_altitude = segment
                    .iter()
                    .map(|point| home + point.relative_altitude_meters)
                    .fold(f64::INFINITY, f64::min);
                minimum_route_altitude <= top + vertical_clearance_meters
            }) || point_indexes.iter().any(|index| {
                home + route[*index].relative_altitude_meters <= top + vertical_clearance_meters
            });
            if !intersects {
                continue;
            }
            intersection_count += 1;
            "INTERSECTION"
        } else {
            unknown_height_count += 1;
            "HEIGHT_OR_DATUM_UNKNOWN"
        };
        issues.push(KnownBuildingIssue {
            feature_id: building.feature_id.clone(),
            result: result.into(),
            route_segment_indexes: segment_indexes,
            route_point_indexes: point_indexes,
            absolute_base_meters: building.absolute_base_meters,
            absolute_top_meters: absolute_top,
            relative_top_meters: building.relative_top_meters,
            height_source: building.height_source.clone(),
            height_confidence: building.height_confidence.clone(),
            evidence_date: building.evidence_date.clone(),
            footprint: building.polygons.first().cloned().unwrap_or_default(),
        });
    }
    let (status, statement) = if intersection_count > 0 {
        (
            "INTERSECTIONS",
            "Route intersects one or more known building clearance volumes.",
        )
    } else if unknown_height_count > 0
        || !coverage_complete
        || home_absolute_altitude_meters.is_none()
        || start.is_none()
    {
        (
            "INCOMPLETE",
            "Known-building assessment incomplete because coverage, height, or altitude-datum evidence is unavailable.",
        )
    } else {
        (
            "CLEAR_OF_CHECKED_VOLUMES",
            "Route is clear of the known building volumes checked from the identified dataset.",
        )
    };
    Ok(KnownBuildingAssessment {
        status: status.into(),
        statement: statement.into(),
        checked_feature_count: dataset.buildings.len(),
        horizontal_clearance_meters,
        vertical_clearance_meters,
        home_absolute_altitude_meters,
        route_start: start,
        route_segment_count,
        intersection_count,
        unknown_height_count,
        coverage_complete,
        override_required: status != "CLEAR_OF_CHECKED_VOLUMES",
        override_reason: override_reason.map(str::to_string),
        provenance: Some(dataset.provenance.clone()),
        issues,
        limitations,
    })
}

#[derive(Clone, Copy)]
struct RoutePoint {
    point: GeoPoint,
    relative_altitude_meters: f64,
}

fn validate_provenance(value: &KnownBuildingProvenance) -> Result<(), String> {
    if value.provider.trim().is_empty()
        || value.product.trim().is_empty()
        || value.dataset_id.trim().is_empty()
        || value.schema_version.trim().is_empty()
        || value.release.trim().is_empty()
        || value.retrieved_at_unix_ms <= 0
    {
        return Err(
            "known-building provenance identity, release, schema and retrieval time are required"
                .into(),
        );
    }
    let [west, south, east, north] = value.coverage_bbox;
    if !west.is_finite()
        || !south.is_finite()
        || !east.is_finite()
        || !north.is_finite()
        || west < -180.0
        || east > 180.0
        || south < -90.0
        || north > 90.0
        || west >= east
        || south >= north
    {
        return Err(
            "known-building provenance coverageBbox must be [west, south, east, north] in WGS84"
                .into(),
        );
    }
    Ok(())
}

fn parse_building(feature: &Value, index: usize) -> Result<KnownBuilding, String> {
    let properties = feature
        .get("properties")
        .and_then(Value::as_object)
        .ok_or_else(|| format!("known-building feature {index} requires properties"))?;
    let feature_id = feature
        .get("id")
        .and_then(Value::as_str)
        .or_else(|| property_string(properties, &["osid", "OS_TOPO_TOID", "toid"]))
        .ok_or_else(|| format!("known-building feature {index} requires an OS feature identifier"))?
        .to_string();
    let geometry = feature
        .get("geometry")
        .ok_or_else(|| format!("known-building feature {feature_id} requires geometry"))?;
    let polygons = parse_polygons(geometry, &feature_id)?;
    let absolute_base_meters = property_number(
        properties,
        &["height_absolutemin_m", "AbsHMin", "absoluteheightminimum"],
    );
    let absolute_top_meters = property_number(
        properties,
        &["height_absolutemax_m", "AbsHMax", "absoluteheightmaximum"],
    );
    let relative_top_meters = property_number(
        properties,
        &["height_relativemax_m", "RelHMax", "relativeheightmaximum"],
    );
    for (label, value) in [
        ("absolute base", absolute_base_meters),
        ("absolute top", absolute_top_meters),
        ("relative top", relative_top_meters),
    ] {
        if value.is_some_and(|height| !height.is_finite() || !(-500.0..=10_000.0).contains(&height))
        {
            return Err(format!(
                "known-building feature {feature_id} has invalid {label} height"
            ));
        }
    }
    let height_source = if properties.contains_key("height_absolutemax_m") {
        "OS_NGD_BUILDINGS_HEIGHT"
    } else if properties.contains_key("AbsHMax") {
        "OS_BUILDING_HEIGHT_ATTRIBUTE"
    } else {
        "HEIGHT_UNAVAILABLE"
    };
    Ok(KnownBuilding {
        feature_id,
        polygons,
        absolute_base_meters,
        absolute_top_meters,
        relative_top_meters,
        height_source: height_source.into(),
        height_confidence: property_string(properties, &["height_confidencelevel", "BHA_Conf"])
            .map(str::to_string),
        evidence_date: property_string(
            properties,
            &[
                "height_evidencedate",
                "BHA_ProcessDate",
                "height_updatedate",
            ],
        )
        .map(str::to_string),
    })
}

fn parse_polygons(geometry: &Value, feature_id: &str) -> Result<Vec<Vec<GeoPoint>>, String> {
    let geometry_type = geometry.get("type").and_then(Value::as_str).unwrap_or("");
    let coordinates = geometry
        .get("coordinates")
        .and_then(Value::as_array)
        .ok_or_else(|| format!("known-building feature {feature_id} requires coordinates"))?;
    let raw_polygons: Vec<&Value> = match geometry_type {
        "Polygon" => vec![geometry.get("coordinates").expect("coordinates checked")],
        "MultiPolygon" => coordinates.iter().collect(),
        _ => {
            return Err(format!(
                "known-building feature {feature_id} must use Polygon or MultiPolygon geometry"
            ))
        }
    };
    let mut polygons = Vec::new();
    for raw_polygon in raw_polygons {
        let outer_ring = raw_polygon
            .as_array()
            .and_then(|rings| rings.first())
            .and_then(Value::as_array)
            .ok_or_else(|| format!("known-building feature {feature_id} has no outer ring"))?;
        if outer_ring.len() < 4 {
            return Err(format!(
                "known-building feature {feature_id} outer ring must contain at least four coordinates"
            ));
        }
        let mut polygon = Vec::with_capacity(outer_ring.len());
        for coordinate in outer_ring {
            let values = coordinate.as_array().ok_or_else(|| {
                format!("known-building feature {feature_id} contains an invalid coordinate")
            })?;
            let longitude = values.first().and_then(Value::as_f64).ok_or_else(|| {
                format!("known-building feature {feature_id} longitude is invalid")
            })?;
            let latitude = values.get(1).and_then(Value::as_f64).ok_or_else(|| {
                format!("known-building feature {feature_id} latitude is invalid")
            })?;
            if !(-180.0..=180.0).contains(&longitude) || !(-90.0..=90.0).contains(&latitude) {
                return Err(format!(
                    "known-building feature {feature_id} coordinate is outside WGS84 bounds"
                ));
            }
            polygon.push(GeoPoint {
                latitude,
                longitude,
            });
        }
        polygons.push(polygon);
    }
    Ok(polygons)
}

fn property_number(properties: &serde_json::Map<String, Value>, names: &[&str]) -> Option<f64> {
    names.iter().find_map(|name| {
        properties.get(*name).and_then(|value| {
            value
                .as_f64()
                .or_else(|| value.as_str().and_then(|raw| raw.parse::<f64>().ok()))
        })
    })
}

fn property_string<'a>(
    properties: &'a serde_json::Map<String, Value>,
    names: &[&str],
) -> Option<&'a str> {
    names
        .iter()
        .find_map(|name| properties.get(*name).and_then(Value::as_str))
}

fn bbox_contains(bbox: [f64; 4], point: GeoPoint, margin_meters: f64) -> bool {
    let latitude_margin = (margin_meters / EARTH_RADIUS_M).to_degrees();
    let longitude_margin = latitude_margin / point.latitude.to_radians().cos().abs().max(0.01);
    point.longitude >= bbox[0] + longitude_margin
        && point.latitude >= bbox[1] + latitude_margin
        && point.longitude <= bbox[2] - longitude_margin
        && point.latitude <= bbox[3] - latitude_margin
}

fn segment_near_polygon(
    start: GeoPoint,
    end: GeoPoint,
    polygon: &[GeoPoint],
    margin_meters: f64,
) -> bool {
    let origin = polygon[0];
    let start = to_local(origin, start);
    let end = to_local(origin, end);
    let local_polygon = polygon
        .iter()
        .map(|point| to_local(origin, *point))
        .collect::<Vec<_>>();
    point_in_polygon(start, &local_polygon)
        || point_in_polygon(end, &local_polygon)
        || local_polygon.windows(2).any(|edge| {
            segments_intersect(start, end, edge[0], edge[1])
                || segment_distance(start, end, edge[0], edge[1]) <= margin_meters
        })
}

fn to_local(origin: GeoPoint, point: GeoPoint) -> (f64, f64) {
    let north = (point.latitude - origin.latitude).to_radians() * EARTH_RADIUS_M;
    let east = (point.longitude - origin.longitude).to_radians()
        * EARTH_RADIUS_M
        * origin.latitude.to_radians().cos();
    (east, north)
}

fn point_in_polygon(point: (f64, f64), polygon: &[(f64, f64)]) -> bool {
    let mut inside = false;
    for edge in polygon.windows(2) {
        let (a, b) = (edge[0], edge[1]);
        if ((a.1 > point.1) != (b.1 > point.1))
            && point.0 < (b.0 - a.0) * (point.1 - a.1) / (b.1 - a.1) + a.0
        {
            inside = !inside;
        }
    }
    inside
}

fn segments_intersect(a: (f64, f64), b: (f64, f64), c: (f64, f64), d: (f64, f64)) -> bool {
    let cross = |p: (f64, f64), q: (f64, f64), r: (f64, f64)| {
        (q.0 - p.0) * (r.1 - p.1) - (q.1 - p.1) * (r.0 - p.0)
    };
    let ab_c = cross(a, b, c);
    let ab_d = cross(a, b, d);
    let cd_a = cross(c, d, a);
    let cd_b = cross(c, d, b);
    ab_c * ab_d <= 0.0 && cd_a * cd_b <= 0.0
}

fn segment_distance(a: (f64, f64), b: (f64, f64), c: (f64, f64), d: (f64, f64)) -> f64 {
    [
        point_segment_distance(a, c, d),
        point_segment_distance(b, c, d),
        point_segment_distance(c, a, b),
        point_segment_distance(d, a, b),
    ]
    .into_iter()
    .fold(f64::INFINITY, f64::min)
}

fn point_segment_distance(point: (f64, f64), a: (f64, f64), b: (f64, f64)) -> f64 {
    let vector = (b.0 - a.0, b.1 - a.1);
    let length_squared = vector.0 * vector.0 + vector.1 * vector.1;
    if length_squared <= f64::EPSILON {
        return ((point.0 - a.0).powi(2) + (point.1 - a.1).powi(2)).sqrt();
    }
    let projection = (((point.0 - a.0) * vector.0 + (point.1 - a.1) * vector.1) / length_squared)
        .clamp(0.0, 1.0);
    let nearest = (a.0 + projection * vector.0, a.1 + projection * vector.1);
    ((point.0 - nearest.0).powi(2) + (point.1 - nearest.1).powi(2)).sqrt()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fixture() -> KnownBuildingDataset {
        KnownBuildingDataset::from_geojson_slice(
            br#"{
              "type":"FeatureCollection",
              "atlasProvenance":{"provider":"Ordnance Survey","product":"OS NGD Buildings","datasetId":"bld-fts-building-4","schemaVersion":"4.0","release":"2026-04","retrievedAtUnixMs":1775000000000,"coverageBbox":[-0.2,50.9,0.1,51.2]},
              "features":[{"type":"Feature","id":"os-building-1","geometry":{"type":"Polygon","coordinates":[[[-0.1001,50.9999],[-0.0999,50.9999],[-0.0999,51.0001],[-0.1001,51.0001],[-0.1001,50.9999]]]},"properties":{"height_absolutemin_m":20,"height_absolutemax_m":40,"height_confidencelevel":"High"}}]
            }"#,
        )
        .expect("parse known building fixture")
    }

    #[test]
    fn assessment_reports_intersection_without_claiming_obstacle_clearance() {
        let assessment = assess_known_buildings(
            Some(&fixture()),
            None,
            &[
                MissionWaypoint {
                    sequence: 0,
                    latitude: 51.0,
                    longitude: -0.101,
                    altitude_meters: 15.0,
                    speed_mps: Some(5.0),
                    heading_degrees: None,
                    hold_seconds: None,
                    action_refs: vec![],
                },
                MissionWaypoint {
                    sequence: 1,
                    latitude: 51.0,
                    longitude: -0.099,
                    altitude_meters: 15.0,
                    speed_mps: Some(5.0),
                    heading_degrees: None,
                    hold_seconds: None,
                    action_refs: vec![],
                },
            ],
            Some(20.0),
            5.0,
            5.0,
            None,
        )
        .expect("assess route");
        assert_eq!(assessment.status, "INTERSECTIONS");
        assert_eq!(assessment.intersection_count, 1);
        assert!(!assessment
            .statement
            .to_lowercase()
            .contains("obstacle-free"));
    }

    #[test]
    fn one_waypoint_without_departure_still_checks_the_waypoint_footprint() {
        let waypoint = MissionWaypoint {
            sequence: 0,
            latitude: 51.0,
            longitude: -0.1,
            altitude_meters: 15.0,
            speed_mps: Some(5.0),
            heading_degrees: None,
            hold_seconds: None,
            action_refs: vec![],
        };
        let assessment = assess_known_buildings(
            Some(&fixture()),
            None,
            &[waypoint],
            Some(20.0),
            5.0,
            5.0,
            None,
        )
        .expect("assess a one-waypoint response");

        assert_eq!(assessment.status, "INTERSECTIONS");
        assert_eq!(assessment.route_segment_count, 0);
        assert_eq!(assessment.intersection_count, 1);
        assert_eq!(assessment.issues[0].route_point_indexes, vec![0]);
        assert!(assessment.issues[0].route_segment_indexes.is_empty());
    }

    #[test]
    fn missing_height_remains_unknown() {
        let bytes = br#"{"type":"FeatureCollection","atlasProvenance":{"provider":"Ordnance Survey","product":"OS NGD Buildings + BHA","datasetId":"fixture","schemaVersion":"4.0","release":"2026-04","retrievedAtUnixMs":1775000000000,"coverageBbox":[-0.2,50.9,0.1,51.2]},"features":[{"type":"Feature","id":"unknown-height","geometry":{"type":"Polygon","coordinates":[[[-0.1001,50.9999],[-0.0999,50.9999],[-0.0999,51.0001],[-0.1001,51.0001],[-0.1001,50.9999]]]},"properties":{}}]}"#;
        let dataset = KnownBuildingDataset::from_geojson_slice(bytes).expect("parse fixture");
        let waypoints = [MissionWaypoint {
            sequence: 0,
            latitude: 51.0,
            longitude: -0.1,
            altitude_meters: 80.0,
            speed_mps: Some(5.0),
            heading_degrees: None,
            hold_seconds: None,
            action_refs: vec![],
        }];
        let assessment = assess_known_buildings(
            Some(&dataset),
            Some(RouteAssessmentStart {
                latitude: 51.0,
                longitude: -0.101,
                relative_altitude_meters: 0.0,
            }),
            &waypoints,
            Some(20.0),
            5.0,
            5.0,
            None,
        )
        .expect("assess unknown height");
        assert_eq!(assessment.status, "INCOMPLETE");
        assert_eq!(assessment.unknown_height_count, 1);
        assert_eq!(assessment.issues[0].result, "HEIGHT_OR_DATUM_UNKNOWN");
    }
}
