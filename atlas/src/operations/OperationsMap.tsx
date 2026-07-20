import { useEffect, useRef, useState } from "react";
import maplibregl, { type GeoJSONSource, type Map as MapLibreMap } from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import { atlasMapStyle, ATLAS_MAP_INITIAL_CENTER } from "../maps/atlasMapStyle";
import type { MissionPlan } from "../missions/missionTypes";
import type { FleetAircraft, IncidentSnapshot, OperationalTrackGeolocation } from "../operationsTypes";

type DraftLocation = { latitude: number; longitude: number };
type DraftResponseGeometry = {
  pattern: "HOLD_AT_STAGING" | "OFFSET_OBSERVE" | "BOUNDED_AREA_SCAN" | "BOUNDED_ORBIT";
  points: DraftLocation[];
  radiusMeters?: number;
};
type ResponsePlanGeometry = Pick<MissionPlan, "generatedWaypoints" | "metadata"> & { id?: string };

export type OperationsMapLayerVisibility = {
  incidents: boolean;
  aircraft: boolean;
  responseRoute: boolean;
  aircraftTrail: boolean;
  trackTargets: boolean;
};

export const defaultOperationsMapLayers: OperationsMapLayerVisibility = {
  incidents: true,
  aircraft: true,
  responseRoute: true,
  aircraftTrail: true,
  trackTargets: true,
};

type OperationsMapProps = {
  incidents: IncidentSnapshot[];
  aircraft: FleetAircraft[];
  selectedIncidentId?: string;
  draftLocation?: DraftLocation;
  draftResponseGeometry?: DraftResponseGeometry;
  selectingLocation?: boolean;
  responsePlan?: ResponsePlanGeometry;
  responseDroneId?: string;
  aircraftTrail?: Array<{ latitude: number; longitude: number }>;
  trackGeolocations?: OperationalTrackGeolocation[];
  layers?: OperationsMapLayerVisibility;
  onIncidentSelect: (incidentId: string) => void;
  onAircraftSelect?: (droneId: string) => void;
  onLocationSelect?: (location: DraftLocation) => void;
};

const INCIDENT_SOURCE = "operations-incidents";
const AIRCRAFT_SOURCE = "operations-aircraft";
const DRAFT_SOURCE = "operations-draft-location";
const RESPONSE_SOURCE = "operations-response-plan";
const TRAIL_SOURCE = "operations-aircraft-trail";
const TRACK_TARGET_SOURCE = "operations-track-targets";

export function OperationsMap({
  incidents,
  aircraft,
  selectedIncidentId,
  draftLocation,
  draftResponseGeometry,
  selectingLocation = false,
  responsePlan,
  responseDroneId,
  aircraftTrail = [],
  trackGeolocations = [],
  layers = defaultOperationsMapLayers,
  onIncidentSelect,
  onAircraftSelect,
  onLocationSelect,
}: OperationsMapProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MapLibreMap | null>(null);
  const labelMarkersRef = useRef<Map<string, maplibregl.Marker>>(new Map());
  const initialFitRef = useRef(false);
  const valuesRef = useRef({
    incidents,
    aircraft,
    selectedIncidentId,
    draftLocation,
    draftResponseGeometry,
    selectingLocation,
    responsePlan,
    responseDroneId,
    aircraftTrail,
    trackGeolocations,
    layers,
    onIncidentSelect,
    onAircraftSelect,
    onLocationSelect,
  });
  const [mapState, setMapState] = useState<"loading" | "ready" | "degraded">("loading");
  const focusedIncident = incidents.find((incident) => incident.id === selectedIncidentId);

  valuesRef.current = {
    incidents,
    aircraft,
    selectedIncidentId,
    draftLocation,
    draftResponseGeometry,
    selectingLocation,
    responsePlan,
    responseDroneId,
    aircraftTrail,
    trackGeolocations,
    layers,
    onIncidentSelect,
    onAircraftSelect,
    onLocationSelect,
  };

  useEffect(() => {
    if (!containerRef.current || mapRef.current) return;
    const map = new maplibregl.Map({
      container: containerRef.current,
      style: atlasMapStyle(),
      center: ATLAS_MAP_INITIAL_CENTER,
      zoom: 11,
      minZoom: 2,
      maxZoom: 19,
      attributionControl: false,
      cooperativeGestures: true,
    });
    mapRef.current = map;
    map.addControl(new maplibregl.NavigationControl({ visualizePitch: true }), "top-right");
    map.addControl(new maplibregl.ScaleControl({ maxWidth: 120, unit: "metric" }), "bottom-left");
    map.addControl(new maplibregl.AttributionControl({ compact: true }), "bottom-right");

    map.on("load", () => {
      addOperationsLayers(map);
      setMapState("ready");
      updateMapSources(map, valuesRef.current);
      applyLayerVisibility(map, valuesRef.current.layers ?? defaultOperationsMapLayers);
      fitOperationalContext(map, valuesRef.current.incidents, valuesRef.current.aircraft, valuesRef.current.trackGeolocations);
      updateOperationLabels(map, labelMarkersRef.current, valuesRef.current);
      initialFitRef.current =
        valuesRef.current.incidents.some(hasIncidentLocation) ||
        valuesRef.current.aircraft.some(hasAircraftLocation) ||
        valuesRef.current.trackGeolocations.some(hasTrackLocation);
    });
    map.on("error", (event) => {
      if (String(event.error?.message ?? event.error).toLowerCase().includes("tile")) {
        setMapState("degraded");
      }
    });
    map.on("zoomend", () => {
      updateOperationLabels(map, labelMarkersRef.current, valuesRef.current);
    });
    map.on("click", "operations-incident-points", (event) => {
      event.preventDefault();
      const id = event.features?.[0]?.properties?.id;
      if (typeof id === "string") valuesRef.current.onIncidentSelect(id);
    });
    map.on("click", "operations-incident-clusters", (event) => {
      event.preventDefault();
      const feature = event.features?.[0];
      const clusterId = Number(feature?.properties?.cluster_id);
      if (!feature || !Number.isFinite(clusterId) || feature.geometry.type !== "Point") return;
      const center = feature.geometry.coordinates as [number, number];
      const source = map.getSource(INCIDENT_SOURCE) as GeoJSONSource;
      void source.getClusterExpansionZoom(clusterId).then((zoom) => {
        map.easeTo({ center, zoom, duration: 520 });
      });
    });
    map.on("click", "operations-aircraft-points", (event) => {
      event.preventDefault();
      const id = event.features?.[0]?.properties?.id;
      if (typeof id === "string") valuesRef.current.onAircraftSelect?.(id);
    });
    map.on("click", "operations-track-target-points", (event) => {
      event.preventDefault();
      const id = event.features?.[0]?.properties?.id;
      const target = valuesRef.current.trackGeolocations.find((candidate) => candidate.geolocation.id === id);
      if (target) showTrackTargetPopup(map, target);
    });
    map.on("click", (event) => {
      if (event.defaultPrevented || !valuesRef.current.selectingLocation) return;
      valuesRef.current.onLocationSelect?.({
        latitude: event.lngLat.lat,
        longitude: event.lngLat.lng,
      });
    });
    for (const layer of ["operations-incident-points", "operations-incident-clusters", "operations-aircraft-points", "operations-track-target-points"]) {
      map.on("mouseenter", layer, () => { map.getCanvas().style.cursor = "pointer"; });
      map.on("mouseleave", layer, () => {
        map.getCanvas().style.cursor = valuesRef.current.selectingLocation ? "crosshair" : "";
      });
    }

    const observer = new ResizeObserver(() => map.resize());
    observer.observe(containerRef.current);
    return () => {
      observer.disconnect();
      clearOperationLabels(labelMarkersRef.current);
      map.remove();
      mapRef.current = null;
    };
  }, []);

  useEffect(() => {
    const map = mapRef.current;
    if (!map?.getSource(INCIDENT_SOURCE)) return;
    updateMapSources(map, valuesRef.current);
    updateOperationLabels(map, labelMarkersRef.current, valuesRef.current);
    map.getCanvas().style.cursor = selectingLocation ? "crosshair" : "";
    if (!initialFitRef.current && (incidents.some(hasIncidentLocation) || aircraft.some(hasAircraftLocation) || trackGeolocations.some(hasTrackLocation))) {
      fitOperationalContext(map, incidents, aircraft, trackGeolocations);
      initialFitRef.current = true;
    }
  }, [aircraft, aircraftTrail, draftLocation, draftResponseGeometry, incidents, responseDroneId, responsePlan, selectedIncidentId, selectingLocation, trackGeolocations]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map?.getLayer("operations-incident-points")) return;
    applyLayerVisibility(map, layers);
    updateOperationLabels(map, labelMarkersRef.current, valuesRef.current);
  }, [layers]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map || !responsePlan) return;
    fitResponsePlan(map, responsePlan, aircraft, responseDroneId);
  }, [responseDroneId, responsePlan?.id]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map || !focusedIncident || !hasIncidentLocation(focusedIncident)) return;
    map.easeTo({ center: [focusedIncident.longitude, focusedIncident.latitude], zoom: Math.max(map.getZoom(), 14), duration: 460 });
  }, [selectedIncidentId, focusedIncident?.latitude, focusedIncident?.longitude]);

  function fitAll() {
    const map = mapRef.current;
    if (map) fitOperationalContext(map, incidents, aircraft, trackGeolocations);
  }

  return (
    <section className="operations-map" aria-label="Fleet and incident operations map">
      <div ref={containerRef} className="operations-map__canvas" />
      <div className="operations-map__status" role="status">
        <span className={`map-status-dot map-status-dot--${mapState}`} />
        {mapState === "loading" ? "Loading operational map" : mapState === "degraded" ? "Map data degraded" : "Operational map live"}
      </div>
      <div className="operations-map__legend" aria-label="Map legend">
        {layers.incidents && <span><i className="operations-map__legend-incident" /> Incident</span>}
        {layers.aircraft && <span><i className="operations-map__legend-aircraft" /> Aircraft</span>}
        {layers.responseRoute && <span><i className="operations-map__legend-route" /> Route / reviewed bounds</span>}
        {layers.aircraftTrail && <span><i className="operations-map__legend-trail" /> Trail</span>}
        {layers.trackTargets && <span><i className="operations-map__legend-target" /> Track target</span>}
      </div>
      {selectingLocation && (
        <p className="operations-map__instruction">Add the next reviewed response coordinate</p>
      )}
      <button type="button" className="operations-map__fit" onClick={fitAll}>
        Fit operations
      </button>
    </section>
  );
}

function addOperationsLayers(map: MapLibreMap) {
  map.addSource(INCIDENT_SOURCE, {
    type: "geojson",
    data: emptyFeatureCollection(),
    cluster: true,
    clusterMaxZoom: 13,
    clusterRadius: 44,
  });
  map.addSource(AIRCRAFT_SOURCE, { type: "geojson", data: emptyFeatureCollection() });
  map.addSource(DRAFT_SOURCE, { type: "geojson", data: emptyFeatureCollection() });
  map.addSource(RESPONSE_SOURCE, { type: "geojson", data: emptyFeatureCollection() });
  map.addSource(TRAIL_SOURCE, { type: "geojson", data: emptyFeatureCollection() });
  map.addSource(TRACK_TARGET_SOURCE, { type: "geojson", data: emptyFeatureCollection() });

  map.addLayer({
    id: "operations-aircraft-trail",
    type: "line",
    source: TRAIL_SOURCE,
    paint: {
      "line-color": "#27799f",
      "line-width": 3,
      "line-opacity": 0.78,
    },
  });

  map.addLayer({
    id: "operations-track-target-uncertainty",
    type: "fill",
    source: TRACK_TARGET_SOURCE,
    filter: ["==", ["get", "kind"], "track-uncertainty"],
    paint: {
      "fill-color": [
        "match", ["get", "lifecycleState"],
        "ACTIVE", "#cf7a2a",
        "TEMPORARILY_OCCLUDED", "#d79a27",
        "LOST", "#7c8790",
        "#6d736e",
      ],
      "fill-opacity": 0.12,
      "fill-outline-color": "#8b5b08",
    },
  });
  map.addLayer({
    id: "operations-track-target-motion",
    type: "line",
    source: TRACK_TARGET_SOURCE,
    filter: ["==", ["get", "kind"], "track-motion"],
    paint: {
      "line-color": "#8b5b08",
      "line-width": 3,
      "line-opacity": 0.84,
    },
  });
  map.addLayer({
    id: "operations-track-target-points",
    type: "circle",
    source: TRACK_TARGET_SOURCE,
    filter: ["==", ["get", "kind"], "track-target"],
    paint: {
      "circle-radius": 8,
      "circle-color": [
        "match", ["get", "lifecycleState"],
        "ACTIVE", "#cf7a2a",
        "TEMPORARILY_OCCLUDED", "#d79a27",
        "LOST", "#7c8790",
        "#6d736e",
      ],
      "circle-stroke-color": "#f4f0e5",
      "circle-stroke-width": 2.5,
    },
  });

  map.addLayer({
    id: "operations-response-bounds-fill",
    type: "fill",
    source: RESPONSE_SOURCE,
    filter: ["==", ["get", "kind"], "response-boundary"],
    paint: {
      "fill-color": "#27799f",
      "fill-opacity": 0.1,
      "fill-outline-color": "#18251d",
    },
  });
  map.addLayer({
    id: "operations-known-building-issues",
    type: "fill",
    source: RESPONSE_SOURCE,
    filter: ["==", ["get", "kind"], "known-building-issue"],
    paint: {
      "fill-color": ["match", ["get", "result"], "INTERSECTION", "#a7352d", "#d79a27"],
      "fill-opacity": 0.36,
      "fill-outline-color": "#6f261f",
    },
  });
  map.addLayer({
    id: "operations-response-route",
    type: "line",
    source: RESPONSE_SOURCE,
    filter: ["==", ["geometry-type"], "LineString"],
    paint: {
      "line-color": "#18251d",
      "line-width": 3,
      "line-dasharray": [2, 1.5],
      "line-opacity": 0.84,
    },
  });
  map.addLayer({
    id: "operations-response-waypoints",
    type: "circle",
    source: RESPONSE_SOURCE,
    filter: ["==", ["geometry-type"], "Point"],
    paint: {
      "circle-radius": 7,
      "circle-color": "#f4f0e5",
      "circle-stroke-color": "#18251d",
      "circle-stroke-width": 3,
    },
  });

  map.addLayer({
    id: "operations-incident-clusters",
    type: "circle",
    source: INCIDENT_SOURCE,
    filter: ["has", "point_count"],
    paint: {
      "circle-color": "#b84f28",
      "circle-radius": ["step", ["get", "point_count"], 17, 10, 21, 30, 25],
      "circle-stroke-color": "#f2eee3",
      "circle-stroke-width": 3,
      "circle-opacity": 0.94,
    },
  });
  map.addLayer({
    id: "operations-incident-selection",
    type: "circle",
    source: INCIDENT_SOURCE,
    filter: ["all", ["!", ["has", "point_count"]], ["==", ["get", "selected"], true]],
    paint: {
      "circle-radius": 13,
      "circle-color": "rgba(0,0,0,0)",
      "circle-stroke-color": "#18251d",
      "circle-stroke-width": 3,
    },
  });
  map.addLayer({
    id: "operations-incident-points",
    type: "circle",
    source: INCIDENT_SOURCE,
    filter: ["!", ["has", "point_count"]],
    paint: {
      "circle-radius": 8,
      "circle-color": [
        "match", ["get", "priority"],
        "CRITICAL", "#a7352d",
        "HIGH", "#b84f28",
        "MEDIUM", "#d79a27",
        "#4c7859",
      ],
      "circle-stroke-color": "#f4f0e5",
      "circle-stroke-width": 2,
    },
  });
  map.addLayer({
    id: "operations-aircraft-selection",
    type: "circle",
    source: AIRCRAFT_SOURCE,
    filter: ["==", ["get", "selected"], true],
    paint: {
      "circle-radius": 15,
      "circle-color": "rgba(0,0,0,0)",
      "circle-stroke-color": "#18251d",
      "circle-stroke-width": 2.5,
    },
  });
  map.addLayer({
    id: "operations-aircraft-points",
    type: "circle",
    source: AIRCRAFT_SOURCE,
    paint: {
      "circle-radius": 10,
      "circle-color": ["match", ["get", "state"], "live", "#27799f", "stale", "#7c8790", "#6d736e"],
      "circle-stroke-color": "#f4f0e5",
      "circle-stroke-width": 2.5,
    },
  });
  map.addLayer({
    id: "operations-draft-response-fill",
    type: "fill",
    source: DRAFT_SOURCE,
    filter: ["==", ["get", "kind"], "draft-response-boundary"],
    paint: {
      "fill-color": "#d79a27",
      "fill-opacity": 0.1,
      "fill-outline-color": "#8b5b08",
    },
  });
  map.addLayer({
    id: "operations-draft-response-line",
    type: "line",
    source: DRAFT_SOURCE,
    filter: ["==", ["get", "kind"], "draft-response-line"],
    paint: {
      "line-color": "#8b5b08",
      "line-width": 2.5,
      "line-dasharray": [1.5, 1.5],
    },
  });
  map.addLayer({
    id: "operations-draft-location",
    type: "circle",
    source: DRAFT_SOURCE,
    paint: {
      "circle-radius": 10,
      "circle-color": "rgba(0,0,0,0)",
      "circle-stroke-color": "#b84f28",
      "circle-stroke-width": 3,
      "circle-stroke-opacity": 0.95,
    },
  });
}

function updateMapSources(
  map: MapLibreMap,
  values: Pick<OperationsMapProps, "incidents" | "aircraft" | "selectedIncidentId" | "draftLocation" | "draftResponseGeometry" | "responsePlan" | "responseDroneId" | "aircraftTrail" | "trackGeolocations">,
) {
  const incidentFeatures: GeoJSON.Feature<GeoJSON.Point>[] = values.incidents
    .filter(hasIncidentLocation)
    .map((incident) => ({
      type: "Feature",
      properties: {
        id: incident.id,
        summary: incident.summary,
        priority: incident.priority,
        status: incident.status,
        selected: incident.id === values.selectedIncidentId,
      },
      geometry: { type: "Point", coordinates: [incident.longitude, incident.latitude] },
    }));
  const aircraftFeatures: GeoJSON.Feature<GeoJSON.Point>[] = values.aircraft
    .filter(hasAircraftLocation)
    .map((item) => ({
      type: "Feature",
      properties: {
        id: item.droneId,
        label: item.droneName || "Unnamed aircraft",
        heading: item.telemetry?.headingDeg ?? 0,
        selected: item.droneId === values.responseDroneId,
        state: item.connectionStatus === "connected" && item.telemetry?.status === "live"
          ? "live"
          : item.connectionStatus === "connected" || item.telemetry?.status === "stale" ? "stale" : "offline",
      },
      geometry: { type: "Point", coordinates: [item.telemetry.longitude, item.telemetry.latitude] },
    }));
  const draftFeatures: GeoJSON.Feature[] = values.draftLocation ? [{
    type: "Feature",
    properties: { kind: "draft-location" },
    geometry: { type: "Point", coordinates: [values.draftLocation.longitude, values.draftLocation.latitude] },
  }] : [];
  const draftGeometryCoordinates = values.draftResponseGeometry?.points
    .filter(validCoordinate)
    .map((point) => [point.longitude, point.latitude] as [number, number]) ?? [];
  draftFeatures.push(...draftGeometryCoordinates.map((coordinates, index) => ({
    type: "Feature" as const,
    properties: { kind: "draft-response-point", index },
    geometry: { type: "Point" as const, coordinates },
  })));
  if (values.draftResponseGeometry?.pattern === "BOUNDED_AREA_SCAN" && draftGeometryCoordinates.length >= 3) {
    draftFeatures.push({
      type: "Feature",
      properties: { kind: "draft-response-boundary" },
      geometry: { type: "Polygon", coordinates: [[...draftGeometryCoordinates, draftGeometryCoordinates[0]]] },
    });
  } else if (values.draftResponseGeometry?.pattern === "BOUNDED_ORBIT" && draftGeometryCoordinates[0] && values.draftResponseGeometry.radiusMeters) {
    draftFeatures.push({
      type: "Feature",
      properties: { kind: "draft-response-boundary" },
      geometry: { type: "Polygon", coordinates: [circleCoordinates(values.draftResponseGeometry.points[0], values.draftResponseGeometry.radiusMeters)] },
    });
  } else if (draftGeometryCoordinates.length >= 2) {
    draftFeatures.push({
      type: "Feature",
      properties: { kind: "draft-response-line" },
      geometry: { type: "LineString", coordinates: draftGeometryCoordinates },
    });
  }
  const responseAircraft = values.aircraft
    .filter(hasAircraftLocation)
    .find((item) => item.droneId === values.responseDroneId);
  const responseCoordinates: [number, number][] = [
    ...(responseAircraft ? [[responseAircraft.telemetry.longitude, responseAircraft.telemetry.latitude] as [number, number]] : []),
    ...(values.responsePlan?.generatedWaypoints.map((waypoint) => [waypoint.longitude, waypoint.latitude] as [number, number]) ?? []),
  ];
  const incidentResponse = values.responsePlan?.metadata.incidentResponse;
  const reviewedGeometry = incidentResponse?.reviewedGeometry;
  const responseBoundary = responseBoundaryFeature(reviewedGeometry);
  const buildingIssues = incidentResponse?.knownBuildingAssessment?.issues ?? [];
  const responseFeatures: GeoJSON.Feature[] = [
    ...(responseBoundary ? [responseBoundary] : []),
    ...buildingIssues.flatMap((issue) => issue.footprint.length >= 4 ? [{
      type: "Feature" as const,
      properties: { kind: "known-building-issue", featureId: issue.featureId, result: issue.result },
      geometry: {
        type: "Polygon" as const,
        coordinates: [[...issue.footprint.map((point) => [point.longitude, point.latitude] as [number, number]), [issue.footprint[0].longitude, issue.footprint[0].latitude] as [number, number]]],
      },
    }] : []),
    ...(responseCoordinates.length >= 2 ? [{
      type: "Feature" as const,
      properties: { kind: "response-route" },
      geometry: { type: "LineString" as const, coordinates: responseCoordinates },
    }] : []),
    ...(values.responsePlan?.generatedWaypoints.map((waypoint) => ({
      type: "Feature" as const,
      properties: { kind: "response-waypoint", sequence: waypoint.sequence },
      geometry: { type: "Point" as const, coordinates: [waypoint.longitude, waypoint.latitude] },
    })) ?? []),
  ];
  const trailCoordinates = (values.aircraftTrail ?? [])
    .filter((position) => Number.isFinite(position.latitude) && Number.isFinite(position.longitude))
    .map((position) => [position.longitude, position.latitude] as [number, number]);
  const trailFeatures: GeoJSON.Feature[] = trailCoordinates.length >= 2 ? [{
    type: "Feature",
    properties: { kind: "aircraft-trail" },
    geometry: { type: "LineString", coordinates: trailCoordinates },
  }] : [];
  const trackFeatures: GeoJSON.Feature[] = (values.trackGeolocations ?? [])
    .filter(hasTrackLocation)
    .flatMap((target) => {
      const coordinate: [number, number] = [target.geolocation.longitude, target.geolocation.latitude];
      const uncertaintyM = target.geolocation.horizontalUncertaintyM ?? 0;
      const properties = {
        id: target.geolocation.id,
        kind: "track-target",
        lifecycleState: target.lifecycleState,
        trackId: target.geolocation.trackId,
      };
      const features: GeoJSON.Feature[] = [{
        type: "Feature",
        properties,
        geometry: { type: "Point", coordinates: coordinate },
      }];
      if (uncertaintyM > 0) {
        features.unshift({
          type: "Feature",
          properties: { ...properties, kind: "track-uncertainty" },
          geometry: {
            type: "Polygon",
            coordinates: [circleCoordinates({ latitude: coordinate[1], longitude: coordinate[0] }, uncertaintyM)],
          },
        });
      }
      if (target.geolocation.targetSpeedMps != null
          && target.geolocation.targetDirectionDeg != null
          && target.geolocation.motionStatus === "FILTERED") {
        const vectorEnd = offsetCoordinateMeters(
          coordinate[1],
          coordinate[0],
          target.geolocation.targetDirectionDeg,
          Math.min(300, Math.max(12, target.geolocation.targetSpeedMps * 8)),
        );
        features.push({
          type: "Feature",
          properties: { ...properties, kind: "track-motion" },
          geometry: { type: "LineString", coordinates: [coordinate, [vectorEnd.longitude, vectorEnd.latitude]] },
        });
      }
      return features;
    });

  setData(map, INCIDENT_SOURCE, incidentFeatures);
  setData(map, AIRCRAFT_SOURCE, aircraftFeatures);
  setData(map, DRAFT_SOURCE, draftFeatures);
  setData(map, RESPONSE_SOURCE, responseFeatures);
  setData(map, TRAIL_SOURCE, trailFeatures);
  setData(map, TRACK_TARGET_SOURCE, trackFeatures);
}

function applyLayerVisibility(map: MapLibreMap, layers: OperationsMapLayerVisibility) {
  const groups: Array<[boolean, string[]]> = [
    [layers.incidents, ["operations-incident-clusters", "operations-incident-selection", "operations-incident-points"]],
    [layers.aircraft, ["operations-aircraft-selection", "operations-aircraft-points"]],
    [layers.responseRoute, ["operations-response-bounds-fill", "operations-known-building-issues", "operations-response-route", "operations-response-waypoints"]],
    [layers.aircraftTrail, ["operations-aircraft-trail"]],
    [layers.trackTargets, ["operations-track-target-uncertainty", "operations-track-target-motion", "operations-track-target-points"]],
  ];
  for (const [visible, layerIds] of groups) {
    for (const layerId of layerIds) {
      if (map.getLayer(layerId)) map.setLayoutProperty(layerId, "visibility", visible ? "visible" : "none");
    }
  }
}

function updateOperationLabels(
  map: MapLibreMap,
  markers: Map<string, maplibregl.Marker>,
  values: Pick<OperationsMapProps, "incidents" | "aircraft" | "selectedIncidentId" | "responseDroneId" | "layers">,
) {
  const zoom = map.getZoom();
  const layers = values.layers ?? defaultOperationsMapLayers;
  const labels: Array<{
    id: string;
    kind: "incident" | "aircraft";
    label: string;
    longitude: number;
    latitude: number;
    selected: boolean;
    priority?: string;
    rank: number;
  }> = [];

  if (layers.incidents) {
    for (const incident of values.incidents.filter(hasIncidentLocation)) {
      const selected = incident.id === values.selectedIncidentId;
      const priorityRank = incident.priority === "CRITICAL" ? 0 : incident.priority === "HIGH" ? 1 : incident.priority === "MEDIUM" ? 2 : 3;
      const visible = selected
        || (zoom >= 11.5 && incident.priority === "CRITICAL")
        || (zoom >= 12.75 && incident.priority === "HIGH")
        || zoom >= 14;
      if (!visible) continue;
      labels.push({
        id: `incident:${incident.id}`,
        kind: "incident",
        label: truncateMapLabel(incident.summary || incident.incidentType, 34),
        longitude: incident.longitude,
        latitude: incident.latitude,
        selected,
        priority: incident.priority,
        rank: selected ? -100 : priorityRank,
      });
    }
  }

  if (layers.aircraft) {
    for (const item of values.aircraft.filter(hasAircraftLocation)) {
      const selected = item.droneId === values.responseDroneId;
      const live = item.connectionStatus === "connected" && item.telemetry.status === "live";
      const visible = selected || (zoom >= 12.75 && live) || zoom >= 14.5;
      if (!visible) continue;
      labels.push({
        id: `aircraft:${item.droneId}`,
        kind: "aircraft",
        label: truncateMapLabel(item.droneName || "Unnamed aircraft", 28),
        longitude: item.telemetry.longitude,
        latitude: item.telemetry.latitude,
        selected,
        rank: selected ? -90 : live ? 5 : 10,
      });
    }
  }

  const limit = zoom >= 15 ? 48 : zoom >= 13 ? 24 : 10;
  labels.sort((left, right) => left.rank - right.rank || left.label.localeCompare(right.label));
  const visibleLabels = labels.slice(0, limit);
  const visibleIds = new Set(visibleLabels.map((label) => label.id));
  for (const [id, marker] of markers) {
    if (visibleIds.has(id)) continue;
    marker.remove();
    markers.delete(id);
  }
  for (const label of visibleLabels) {
    const className = [
      "operations-map-label",
      `operations-map-label--${label.kind}`,
      label.selected ? "operations-map-label--selected" : "",
      label.priority === "CRITICAL" ? "operations-map-label--critical" : "",
    ].filter(Boolean).join(" ");
    const existing = markers.get(label.id);
    if (existing) {
      const element = existing.getElement();
      element.className = className;
      element.textContent = label.label;
      existing.setLngLat([label.longitude, label.latitude]);
      continue;
    }
    const element = document.createElement("div");
    element.className = className;
    element.textContent = label.label;
    element.setAttribute("aria-hidden", "true");
    element.dataset.mapLabelId = label.id;
    markers.set(label.id, new maplibregl.Marker({ element, anchor: "left", offset: [13, 0] })
      .setLngLat([label.longitude, label.latitude])
      .addTo(map));
  }
}

function clearOperationLabels(markers: Map<string, maplibregl.Marker>) {
  for (const marker of markers.values()) marker.remove();
  markers.clear();
}

function showTrackTargetPopup(map: MapLibreMap, target: OperationalTrackGeolocation) {
  if (!hasTrackLocation(target)) return;
  const location = target.geolocation;
  const content = document.createElement("article");
  content.className = "track-target-popup";
  const title = document.createElement("strong");
  title.textContent = `${target.classLabel || "Tracked object"} · ${shortTrackIdentity(location.trackId)}`;
  content.append(title);
  const subtitle = document.createElement("span");
  subtitle.textContent = `${target.lifecycleState.replace(/_/g, " ")} · ${target.droneName}`;
  content.append(subtitle);
  const facts = document.createElement("dl");
  appendPopupFact(facts, "Observed", location.frameObservedAtUnixMs ? new Date(location.frameObservedAtUnixMs).toLocaleString() : "Unknown");
  appendPopupFact(facts, "Coordinate", `${location.latitude.toFixed(6)}, ${location.longitude.toFixed(6)}`);
  appendPopupFact(facts, "Uncertainty", location.horizontalUncertaintyM == null ? "Unknown" : `±${location.horizontalUncertaintyM.toFixed(1)} m`);
  appendPopupFact(facts, "Terrain", location.refinementStatus === "NOT_REQUESTED"
    ? "Initial horizontal plane"
    : `${location.refinementStatus.replace(/_/g, " ")} · ${location.terrainIterationCount} samples · ${location.terrainResidualM?.toFixed(1) ?? "?"} m residual`);
  appendPopupFact(facts, "Motion", location.targetSpeedMps == null
    ? location.motionStatus.replace(/_/g, " ")
    : `${location.targetSpeedMps.toFixed(1)} m/s · ${location.targetDirectionDeg?.toFixed(0) ?? "?"}° · ${location.motionStatus.replace(/_/g, " ")}`);
  appendPopupFact(facts, "Evidence", `${target.evidenceMarkerCount} markers · ${target.annotationCount} annotations`);
  appendPopupFact(facts, "Track", `${target.observationCount} observations · ${target.selectionStatus || "not selected"}`);
  content.append(facts);
  new maplibregl.Popup({ closeButton: true, closeOnClick: true, maxWidth: "22rem", offset: 12 })
    .setLngLat([location.longitude, location.latitude])
    .setDOMContent(content)
    .addTo(map);
}

function appendPopupFact(list: HTMLDListElement, label: string, value: string) {
  const term = document.createElement("dt");
  term.textContent = label;
  const description = document.createElement("dd");
  description.textContent = value;
  list.append(term, description);
}

function shortTrackIdentity(trackId: string) {
  const parts = trackId.split(":");
  const part = parts[parts.length - 1];
  return part ? `#${part}` : trackId;
}

function truncateMapLabel(value: string, maxLength: number) {
  const normalized = value.trim().replace(/\s+/g, " ");
  return normalized.length <= maxLength ? normalized : `${normalized.slice(0, maxLength - 1)}…`;
}

function setData(map: MapLibreMap, sourceId: string, features: GeoJSON.Feature[]) {
  (map.getSource(sourceId) as GeoJSONSource | undefined)?.setData({ type: "FeatureCollection", features });
}

function responseBoundaryFeature(reviewedGeometry?: Record<string, unknown>): GeoJSON.Feature<GeoJSON.Polygon> | undefined {
  if (!reviewedGeometry) return undefined;
  if (reviewedGeometry.responsePattern === "BOUNDED_AREA_SCAN") {
    const points = Array.isArray(reviewedGeometry.areaPolygon)
      ? reviewedGeometry.areaPolygon.flatMap((value) => {
          if (!value || typeof value !== "object") return [];
          const point = value as Record<string, unknown>;
          const latitude = Number(point.latitude);
          const longitude = Number(point.longitude);
          return validCoordinate({ latitude, longitude }) ? [{ latitude, longitude }] : [];
        })
      : [];
    if (points.length < 3) return undefined;
    const coordinates = points.map((point) => [point.longitude, point.latitude] as [number, number]);
    return {
      type: "Feature",
      properties: { kind: "response-boundary" },
      geometry: { type: "Polygon", coordinates: [[...coordinates, coordinates[0]]] },
    };
  }
  if (reviewedGeometry.responsePattern === "BOUNDED_ORBIT") {
    const center = {
      latitude: Number(reviewedGeometry.centerLatitude),
      longitude: Number(reviewedGeometry.centerLongitude),
    };
    const radius = Number(reviewedGeometry.radiusMeters);
    if (!validCoordinate(center) || !Number.isFinite(radius) || radius <= 0) return undefined;
    return {
      type: "Feature",
      properties: { kind: "response-boundary" },
      geometry: { type: "Polygon", coordinates: [circleCoordinates(center, radius)] },
    };
  }
  return undefined;
}

function circleCoordinates(center: DraftLocation, radiusMeters: number): [number, number][] {
  const earthRadius = 6_371_000;
  const angularDistance = radiusMeters / earthRadius;
  const latitude = center.latitude * Math.PI / 180;
  const longitude = center.longitude * Math.PI / 180;
  const coordinates: [number, number][] = [];
  for (let index = 0; index <= 64; index += 1) {
    const bearing = index / 64 * Math.PI * 2;
    const nextLatitude = Math.asin(
      Math.sin(latitude) * Math.cos(angularDistance)
      + Math.cos(latitude) * Math.sin(angularDistance) * Math.cos(bearing),
    );
    const nextLongitude = longitude + Math.atan2(
      Math.sin(bearing) * Math.sin(angularDistance) * Math.cos(latitude),
      Math.cos(angularDistance) - Math.sin(latitude) * Math.sin(nextLatitude),
    );
    coordinates.push([nextLongitude * 180 / Math.PI, nextLatitude * 180 / Math.PI]);
  }
  return coordinates;
}

function offsetCoordinateMeters(latitudeDeg: number, longitudeDeg: number, bearingDeg: number, distanceMeters: number) {
  const earthRadiusM = 6_371_000;
  const angularDistance = distanceMeters / earthRadiusM;
  const bearing = bearingDeg * Math.PI / 180;
  const latitude = latitudeDeg * Math.PI / 180;
  const longitude = longitudeDeg * Math.PI / 180;
  const nextLatitude = Math.asin(
    Math.sin(latitude) * Math.cos(angularDistance)
      + Math.cos(latitude) * Math.sin(angularDistance) * Math.cos(bearing),
  );
  const nextLongitude = longitude + Math.atan2(
    Math.sin(bearing) * Math.sin(angularDistance) * Math.cos(latitude),
    Math.cos(angularDistance) - Math.sin(latitude) * Math.sin(nextLatitude),
  );
  return { latitude: nextLatitude * 180 / Math.PI, longitude: nextLongitude * 180 / Math.PI };
}

function validCoordinate(point: DraftLocation) {
  return Number.isFinite(point.latitude)
    && Number.isFinite(point.longitude)
    && point.latitude >= -90
    && point.latitude <= 90
    && point.longitude >= -180
    && point.longitude <= 180;
}

function fitOperationalContext(
  map: MapLibreMap,
  incidents: IncidentSnapshot[],
  aircraft: FleetAircraft[],
  trackGeolocations: OperationalTrackGeolocation[] = [],
) {
  const coordinates: [number, number][] = [
    ...incidents.filter(hasIncidentLocation).map((incident) => [incident.longitude, incident.latitude] as [number, number]),
    ...aircraft.filter(hasAircraftLocation).map((item) => [item.telemetry.longitude, item.telemetry.latitude] as [number, number]),
    ...trackGeolocations.filter(hasTrackLocation).map((target) => [target.geolocation.longitude, target.geolocation.latitude] as [number, number]),
  ];
  if (coordinates.length === 0) {
    map.easeTo({ center: ATLAS_MAP_INITIAL_CENTER, zoom: 11, duration: 420 });
    return;
  }
  if (coordinates.length === 1) {
    map.easeTo({ center: coordinates[0], zoom: 14, duration: 420 });
    return;
  }
  const bounds = coordinates.reduce(
    (current, coordinate) => current.extend(coordinate),
    new maplibregl.LngLatBounds(coordinates[0], coordinates[0]),
  );
  map.fitBounds(bounds, { padding: 72, maxZoom: 15, duration: 520 });
}

function fitResponsePlan(map: MapLibreMap, plan: ResponsePlanGeometry, aircraft: FleetAircraft[], droneId?: string) {
  const selectedAircraft = aircraft
    .filter(hasAircraftLocation)
    .find((item) => item.droneId === droneId);
  const coordinates: [number, number][] = [
    ...(selectedAircraft ? [[selectedAircraft.telemetry.longitude, selectedAircraft.telemetry.latitude] as [number, number]] : []),
    ...plan.generatedWaypoints.map((waypoint) => [waypoint.longitude, waypoint.latitude] as [number, number]),
  ];
  if (coordinates.length === 0) return;
  if (coordinates.length === 1) {
    map.easeTo({ center: coordinates[0], zoom: 15, duration: 460 });
    return;
  }
  const bounds = coordinates.reduce(
    (current, coordinate) => current.extend(coordinate),
    new maplibregl.LngLatBounds(coordinates[0], coordinates[0]),
  );
  map.fitBounds(bounds, { padding: 88, maxZoom: 16, duration: 520 });
}

function hasIncidentLocation(incident: IncidentSnapshot): incident is IncidentSnapshot & { latitude: number; longitude: number } {
  return Number.isFinite(incident.latitude) && Number.isFinite(incident.longitude);
}

function hasAircraftLocation(aircraft: FleetAircraft): aircraft is FleetAircraft & {
  droneId: string;
  telemetry: NonNullable<FleetAircraft["telemetry"]> & { latitude: number; longitude: number };
} {
  return Boolean(aircraft.droneId)
    && Number.isFinite(aircraft.telemetry?.latitude)
    && Number.isFinite(aircraft.telemetry?.longitude);
}

function hasTrackLocation(target: OperationalTrackGeolocation): target is OperationalTrackGeolocation & {
  geolocation: OperationalTrackGeolocation["geolocation"] & { latitude: number; longitude: number };
} {
  return Number.isFinite(target.geolocation.latitude) && Number.isFinite(target.geolocation.longitude);
}

function emptyFeatureCollection(): GeoJSON.FeatureCollection {
  return { type: "FeatureCollection", features: [] };
}
