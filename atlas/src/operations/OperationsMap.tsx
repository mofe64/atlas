import { useEffect, useRef, useState } from "react";
import maplibregl, { type GeoJSONSource, type Map as MapLibreMap } from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import { atlasMapStyle, ATLAS_MAP_INITIAL_CENTER } from "../maps/atlasMapStyle";
import type { MissionPlan } from "../missions/missionTypes";
import type { FleetAircraft, IncidentSnapshot } from "../operationsTypes";

type DraftLocation = { latitude: number; longitude: number };

type OperationsMapProps = {
  incidents: IncidentSnapshot[];
  aircraft: FleetAircraft[];
  selectedIncidentId?: string;
  draftLocation?: DraftLocation;
  selectingLocation?: boolean;
  responsePlan?: MissionPlan;
  responseDroneId?: string;
  onIncidentSelect: (incidentId: string) => void;
  onAircraftSelect?: (droneId: string) => void;
  onLocationSelect?: (location: DraftLocation) => void;
};

const INCIDENT_SOURCE = "operations-incidents";
const AIRCRAFT_SOURCE = "operations-aircraft";
const DRAFT_SOURCE = "operations-draft-location";
const RESPONSE_SOURCE = "operations-response-plan";

export function OperationsMap({
  incidents,
  aircraft,
  selectedIncidentId,
  draftLocation,
  selectingLocation = false,
  responsePlan,
  responseDroneId,
  onIncidentSelect,
  onAircraftSelect,
  onLocationSelect,
}: OperationsMapProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MapLibreMap | null>(null);
  const initialFitRef = useRef(false);
  const valuesRef = useRef({
    incidents,
    aircraft,
    selectedIncidentId,
    draftLocation,
    selectingLocation,
    responsePlan,
    responseDroneId,
    onIncidentSelect,
    onAircraftSelect,
    onLocationSelect,
  });
  const [mapState, setMapState] = useState<"loading" | "ready" | "degraded">("loading");

  valuesRef.current = {
    incidents,
    aircraft,
    selectedIncidentId,
    draftLocation,
    selectingLocation,
    responsePlan,
    responseDroneId,
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
      fitOperationalContext(map, valuesRef.current.incidents, valuesRef.current.aircraft);
      initialFitRef.current =
        valuesRef.current.incidents.some(hasIncidentLocation) ||
        valuesRef.current.aircraft.some(hasAircraftLocation);
    });
    map.on("error", (event) => {
      if (String(event.error?.message ?? event.error).toLowerCase().includes("tile")) {
        setMapState("degraded");
      }
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
    map.on("click", (event) => {
      if (event.defaultPrevented || !valuesRef.current.selectingLocation) return;
      valuesRef.current.onLocationSelect?.({
        latitude: event.lngLat.lat,
        longitude: event.lngLat.lng,
      });
    });
    for (const layer of ["operations-incident-points", "operations-incident-clusters", "operations-aircraft-points"]) {
      map.on("mouseenter", layer, () => { map.getCanvas().style.cursor = "pointer"; });
      map.on("mouseleave", layer, () => {
        map.getCanvas().style.cursor = valuesRef.current.selectingLocation ? "crosshair" : "";
      });
    }

    const observer = new ResizeObserver(() => map.resize());
    observer.observe(containerRef.current);
    return () => {
      observer.disconnect();
      map.remove();
      mapRef.current = null;
    };
  }, []);

  useEffect(() => {
    const map = mapRef.current;
    if (!map?.getSource(INCIDENT_SOURCE)) return;
    updateMapSources(map, valuesRef.current);
    map.getCanvas().style.cursor = selectingLocation ? "crosshair" : "";
    if (!initialFitRef.current && (incidents.some(hasIncidentLocation) || aircraft.some(hasAircraftLocation))) {
      fitOperationalContext(map, incidents, aircraft);
      initialFitRef.current = true;
    }
  }, [aircraft, draftLocation, incidents, responseDroneId, responsePlan, selectedIncidentId, selectingLocation]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map || !responsePlan) return;
    fitResponsePlan(map, responsePlan, aircraft, responseDroneId);
  }, [aircraft, responseDroneId, responsePlan]);

  useEffect(() => {
    const selected = incidents.find((incident) => incident.id === selectedIncidentId);
    const map = mapRef.current;
    if (!map || !selected || !hasIncidentLocation(selected)) return;
    map.easeTo({ center: [selected.longitude, selected.latitude], zoom: Math.max(map.getZoom(), 14), duration: 460 });
  }, [incidents, selectedIncidentId]);

  function fitAll() {
    const map = mapRef.current;
    if (map) fitOperationalContext(map, incidents, aircraft);
  }

  return (
    <section className="operations-map" aria-label="Fleet and incident operations map">
      <div ref={containerRef} className="operations-map__canvas" />
      <div className="operations-map__status" role="status">
        <span className={`map-status-dot map-status-dot--${mapState}`} />
        {mapState === "loading" ? "Loading operational map" : mapState === "degraded" ? "Map data degraded" : "Operational map live"}
      </div>
      <div className="operations-map__legend" aria-label="Map legend">
        <span><i className="operations-map__legend-incident" /> Incident</span>
        <span><i className="operations-map__legend-aircraft" /> Aircraft</span>
      </div>
      {selectingLocation && (
        <p className="operations-map__instruction">Select the incident location on the map</p>
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
  values: Pick<OperationsMapProps, "incidents" | "aircraft" | "selectedIncidentId" | "draftLocation" | "responsePlan" | "responseDroneId">,
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
  const draftFeatures: GeoJSON.Feature<GeoJSON.Point>[] = values.draftLocation ? [{
    type: "Feature",
    properties: { kind: "draft-location" },
    geometry: { type: "Point", coordinates: [values.draftLocation.longitude, values.draftLocation.latitude] },
  }] : [];
  const responseAircraft = values.aircraft
    .filter(hasAircraftLocation)
    .find((item) => item.droneId === values.responseDroneId);
  const responseCoordinates: [number, number][] = [
    ...(responseAircraft ? [[responseAircraft.telemetry.longitude, responseAircraft.telemetry.latitude] as [number, number]] : []),
    ...(values.responsePlan?.generatedWaypoints.map((waypoint) => [waypoint.longitude, waypoint.latitude] as [number, number]) ?? []),
  ];
  const responseFeatures: GeoJSON.Feature[] = [
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

  setData(map, INCIDENT_SOURCE, incidentFeatures);
  setData(map, AIRCRAFT_SOURCE, aircraftFeatures);
  setData(map, DRAFT_SOURCE, draftFeatures);
  setData(map, RESPONSE_SOURCE, responseFeatures);
}

function setData(map: MapLibreMap, sourceId: string, features: GeoJSON.Feature[]) {
  (map.getSource(sourceId) as GeoJSONSource | undefined)?.setData({ type: "FeatureCollection", features });
}

function fitOperationalContext(map: MapLibreMap, incidents: IncidentSnapshot[], aircraft: FleetAircraft[]) {
  const coordinates: [number, number][] = [
    ...incidents.filter(hasIncidentLocation).map((incident) => [incident.longitude, incident.latitude] as [number, number]),
    ...aircraft.filter(hasAircraftLocation).map((item) => [item.telemetry.longitude, item.telemetry.latitude] as [number, number]),
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

function fitResponsePlan(map: MapLibreMap, plan: MissionPlan, aircraft: FleetAircraft[], droneId?: string) {
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

function emptyFeatureCollection(): GeoJSON.FeatureCollection {
  return { type: "FeatureCollection", features: [] };
}
