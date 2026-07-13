import { useEffect, useRef, useState } from "react";
import type { MutableRefObject } from "react";
import maplibregl, {
  type GeoJSONSource,
  type LngLatBoundsLike,
  type Map as MapLibreMap,
  type MapMouseEvent,
  type StyleSpecification,
} from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import type { MissionPoint, MissionTemplateType, MissionWaypoint } from "./missionTypes";
import { sampleTerrainElevation, terrainSource, type TerrainSource } from "./terrain";

const DEFAULT_TILE_URL = "https://tile.openstreetmap.org/{z}/{x}/{y}.png";
const INITIAL_CENTER: [number, number] = [-0.1278, 51.5074];

type MapStatus = "loading" | "ready" | "degraded";

export type TrackedAircraft = {
  latitude: number;
  longitude: number;
  headingDegrees?: number | null;
  label?: string | null;
  telemetryStatus?: "live" | "stale";
};

export type TrackedHome = {
  latitude: number;
  longitude: number;
  absoluteAltitudeMeters?: number;
  label?: string | null;
};

export type PayloadTarget = {
  latitude: number;
  longitude: number;
  terrainElevationMeters?: number;
  terrainSource?: string;
};

type OperationalMissionMapProps = {
  templateType: MissionTemplateType;
  points?: MissionPoint[];
  planWaypoints: MissionWaypoint[];
  mode?: "edit" | "track";
  aircraft?: TrackedAircraft;
  home?: TrackedHome;
  aircraftTrail?: Array<{ latitude: number; longitude: number }>;
  currentWaypoint?: number;
  followAircraft?: boolean;
  planningFocus?: { latitude: number; longitude: number };
  payloadTarget?: PayloadTarget;
  selectingPayloadTarget?: boolean;
  onPayloadTargetSelect?: (target: PayloadTarget) => void;
  onAddPoint?: (latitude: number, longitude: number) => void;
  onMovePoint?: (id: string, latitude: number, longitude: number) => void;
};

export function OperationalMissionMap({
  templateType,
  points = [],
  planWaypoints,
  mode = "edit",
  aircraft,
  home,
  aircraftTrail = [],
  currentWaypoint,
  followAircraft = false,
  planningFocus,
  payloadTarget,
  selectingPayloadTarget = false,
  onPayloadTargetSelect,
  onAddPoint,
  onMovePoint,
}: OperationalMissionMapProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MapLibreMap | null>(null);
  const markersRef = useRef<maplibregl.Marker[]>([]);
  const aircraftMarkerRef = useRef<maplibregl.Marker | null>(null);
  const homeMarkerRef = useRef<maplibregl.Marker | null>(null);
  const payloadTargetMarkerRef = useRef<maplibregl.Marker | null>(null);
  const appliedPlanningFocusRef = useRef<string | undefined>(undefined);
  const valuesRef = useRef({
    templateType, points, planWaypoints, mode, aircraft, home, aircraftTrail, currentWaypoint, followAircraft, planningFocus, payloadTarget, selectingPayloadTarget, onPayloadTargetSelect, onAddPoint, onMovePoint,
  });
  const [status, setStatus] = useState<MapStatus>("loading");
  const [cursor, setCursor] = useState<[number, number]>();
  const [cursorElevation, setCursorElevation] = useState<number>();

  valuesRef.current = {
    templateType, points, planWaypoints, mode, aircraft, home, aircraftTrail, currentWaypoint, followAircraft, planningFocus, payloadTarget, selectingPayloadTarget, onPayloadTargetSelect, onAddPoint, onMovePoint,
  };

  useEffect(() => {
    if (!containerRef.current || mapRef.current) return;
    const tileURL = import.meta.env.VITE_ATLAS_MAP_TILE_URL || DEFAULT_TILE_URL;
    const demSource = terrainSource();
    const map = new maplibregl.Map({
      container: containerRef.current,
      style: rasterStyle(tileURL, demSource),
      center: planningFocus ? [planningFocus.longitude, planningFocus.latitude] : INITIAL_CENTER,
      zoom: planningFocus ? 15 : 12,
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
      const current = valuesRef.current;
      addMissionSourcesAndLayers(map);
      setStatus("ready");
      updateSources(map, current.points, current.planWaypoints, current.templateType, current.currentWaypoint, current.aircraftTrail);
      syncDraftMarkers(map, markersRef.current, current.mode, current.points, current.onMovePoint);
      syncAircraftMarker(map, aircraftMarkerRef, current.aircraft);
      syncHomeMarker(map, homeMarkerRef, current.home);
      syncPayloadTargetMarker(map, payloadTargetMarkerRef, current.payloadTarget);
      fitCoordinates(map, current.points, current.planWaypoints, current.aircraft, current.home, current.planningFocus);
      if (current.mode === "edit" && current.points.length === 0 && current.planWaypoints.length === 0 && current.planningFocus) {
        appliedPlanningFocusRef.current = focusKey(current.planningFocus);
      }
    });
    map.on("error", (event) => {
      if (String(event.error?.message ?? event.error).toLowerCase().includes("tile")) setStatus("degraded");
    });
    map.on("mousemove", (event: MapMouseEvent) => {
      setCursor([event.lngLat.lng, event.lngLat.lat]);
      const elevation = map.queryTerrainElevation(event.lngLat);
      setCursorElevation(elevation == null ? undefined : elevation);
    });
    map.on("click", (event: MapMouseEvent) => {
      const current = valuesRef.current;
      if (current.mode === "edit") current.onAddPoint?.(event.lngLat.lat, event.lngLat.lng);
      if (current.mode === "track" && current.selectingPayloadTarget) {
        const target = { latitude: event.lngLat.lat, longitude: event.lngLat.lng };
        const renderedElevation = map.queryTerrainElevation(event.lngLat);
        if (renderedElevation != null) {
          current.onPayloadTargetSelect?.({
            ...target,
            terrainElevationMeters: Math.round(renderedElevation * 10) / 10,
            terrainSource: demSource.displayName,
          });
          return;
        }
        void sampleTerrainElevation(target.latitude, target.longitude, demSource)
          .then((terrainElevationMeters) => current.onPayloadTargetSelect?.({
            ...target,
            terrainElevationMeters,
            terrainSource: demSource.displayName,
          }))
          .catch(() => current.onPayloadTargetSelect?.(target));
      }
    });

    return () => {
      markersRef.current.forEach((marker) => marker.remove());
      markersRef.current = [];
      aircraftMarkerRef.current?.remove();
      aircraftMarkerRef.current = null;
      homeMarkerRef.current?.remove();
      homeMarkerRef.current = null;
      payloadTargetMarkerRef.current?.remove();
      payloadTargetMarkerRef.current = null;
      map.remove();
      mapRef.current = null;
    };
  }, []);

  useEffect(() => {
    const map = mapRef.current;
    if (!map?.getSource("mission-plan")) return;
    updateSources(map, points, planWaypoints, templateType, currentWaypoint, aircraftTrail);
    syncDraftMarkers(map, markersRef.current, mode, points, onMovePoint);
  }, [aircraftTrail, currentWaypoint, mode, onMovePoint, planWaypoints, points, templateType]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;
    syncAircraftMarker(map, aircraftMarkerRef, aircraft);
    syncHomeMarker(map, homeMarkerRef, home);
    if (followAircraft && aircraft) {
      map.easeTo({ center: [aircraft.longitude, aircraft.latitude], duration: 650 });
    }
  }, [aircraft?.headingDegrees, aircraft?.label, aircraft?.latitude, aircraft?.longitude, aircraft?.telemetryStatus, followAircraft, home?.label, home?.latitude, home?.longitude]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;
    syncPayloadTargetMarker(map, payloadTargetMarkerRef, payloadTarget);
    map.getCanvas().classList.toggle("operational-map__canvas--targeting", selectingPayloadTarget);
  }, [payloadTarget?.latitude, payloadTarget?.longitude, selectingPayloadTarget]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map || mode !== "edit" || points.length > 0 || planWaypoints.length > 0 || !planningFocus) return;
    const key = focusKey(planningFocus);
    if (appliedPlanningFocusRef.current === key) return;
    appliedPlanningFocusRef.current = key;
    if (home) {
      fitCoordinates(map, points, planWaypoints, aircraft, home, planningFocus);
    } else {
      map.easeTo({ center: [planningFocus.longitude, planningFocus.latitude], zoom: 15, duration: 650 });
    }
  }, [aircraft, home, mode, planningFocus, planWaypoints, points]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map || planWaypoints.length === 0) return;
    fitCoordinates(map, points, planWaypoints, aircraft, home, planningFocus);
  }, [planWaypoints]);

  function fitMission() {
    const map = mapRef.current;
    if (map) fitCoordinates(map, points, planWaypoints, aircraft, home, planningFocus);
  }

  return (
    <section className={`operational-map operational-map--${mode}`} aria-label={mode === "track" ? "Live mission tracking map" : "Operational mission map"}>
      <div ref={containerRef} className="operational-map__canvas" />
      <div className="operational-map__status" role="status">
        <span className={`map-status-dot map-status-dot--${status}`} />
        {status === "loading" ? "Loading map + terrain" : status === "degraded" ? "Map data degraded" : mode === "track" ? "Live map + terrain" : "Map + terrain online"}
      </div>
      <div className="operational-map__instruction">
        <strong>{mode === "track" ? selectingPayloadTarget ? "Select gimbal look target" : trackingTitle(currentWaypoint, planWaypoints.length) : drawingVerb(templateType)}</strong>
        <span>{mode === "track" ? selectingPayloadTarget ? "Click a ground location; Atlas will resolve its DEM elevation for ROI." : trackingInstruction(aircraft) : drawingInstruction(templateType, points.length)}</span>
      </div>
      <button className="operational-map__fit" type="button" onClick={fitMission} disabled={points.length + planWaypoints.length === 0 && !aircraft && !home}>
        Fit {mode === "track" ? "route" : "mission"}
      </button>
      {cursor && (
        <output className="operational-map__coordinates">
          {cursor[1].toFixed(6)}, {cursor[0].toFixed(6)}{cursorElevation === undefined ? "" : ` · ${cursorElevation.toFixed(0)} m AMSL`}
        </output>
      )}
    </section>
  );
}

function rasterStyle(tileURL: string, dem: TerrainSource): StyleSpecification {
  return {
    version: 8,
    sources: {
      "atlas-basemap": {
        type: "raster",
        tiles: [tileURL],
        tileSize: 256,
        maxzoom: 19,
        attribution: '© <a href="https://www.openstreetmap.org/copyright" target="_blank">OpenStreetMap contributors</a>',
      },
      "atlas-terrain": {
        type: "raster-dem",
        tiles: [dem.tileTemplate],
        tileSize: dem.tileSize,
        maxzoom: dem.zoom,
        encoding: dem.encoding,
        attribution: dem.attribution,
      },
    },
    terrain: { source: "atlas-terrain", exaggeration: 1 },
    layers: [
      { id: "atlas-basemap", type: "raster", source: "atlas-basemap" },
      {
        id: "atlas-terrain-hillshade",
        type: "hillshade",
        source: "atlas-terrain",
        paint: {
          "hillshade-shadow-color": "#28352b",
          "hillshade-highlight-color": "#f4f0e5",
          "hillshade-accent-color": "#6e7e68",
          "hillshade-exaggeration": 0.22,
        },
      },
    ],
  };
}

function addMissionSourcesAndLayers(map: MapLibreMap) {
  map.addSource("mission-draft", { type: "geojson", data: emptyFeatureCollection() });
  map.addSource("mission-plan", { type: "geojson", data: emptyFeatureCollection() });
  map.addSource("mission-progress", { type: "geojson", data: emptyFeatureCollection() });
  map.addSource("aircraft-trail", { type: "geojson", data: emptyFeatureCollection() });
  map.addLayer({ id: "mission-draft-fill", type: "fill", source: "mission-draft", filter: ["==", ["geometry-type"], "Polygon"], paint: { "fill-color": "#3f7655", "fill-opacity": 0.16 } });
  map.addLayer({ id: "mission-draft-line-casing", type: "line", source: "mission-draft", filter: ["in", ["geometry-type"], ["literal", ["LineString", "Polygon"]]], paint: { "line-color": "#eff3eb", "line-width": 6, "line-opacity": 0.9 } });
  map.addLayer({ id: "mission-draft-line", type: "line", source: "mission-draft", filter: ["in", ["geometry-type"], ["literal", ["LineString", "Polygon"]]], paint: { "line-color": "#264d36", "line-width": 3, "line-dasharray": [2, 1.2] } });
  map.addLayer({ id: "mission-plan-line-casing", type: "line", source: "mission-plan", paint: { "line-color": "#eef0e8", "line-width": 7 } });
  map.addLayer({ id: "mission-plan-line", type: "line", source: "mission-plan", paint: { "line-color": "#b84f28", "line-width": 3.5 } });
  map.addLayer({ id: "mission-plan-points", type: "circle", source: "mission-plan", filter: ["==", ["geometry-type"], "Point"], paint: { "circle-radius": 4, "circle-color": "#f3eee2", "circle-stroke-color": "#b84f28", "circle-stroke-width": 2 } });
  map.addLayer({ id: "mission-progress-line", type: "line", source: "mission-progress", filter: ["==", ["get", "kind"], "completed"], paint: { "line-color": "#2f7d57", "line-width": 5 } });
  map.addLayer({ id: "mission-current-leg", type: "line", source: "mission-progress", filter: ["==", ["get", "kind"], "active-leg"], paint: { "line-color": "#e7a62b", "line-width": 5, "line-dasharray": [1.5, 1] } });
  map.addLayer({ id: "mission-current-point", type: "circle", source: "mission-progress", filter: ["==", ["get", "kind"], "current"], paint: { "circle-radius": 8, "circle-color": "#e7a62b", "circle-stroke-color": "#18251d", "circle-stroke-width": 2 } });
  map.addLayer({ id: "aircraft-trail-line", type: "line", source: "aircraft-trail", paint: { "line-color": "#27799f", "line-width": 3, "line-opacity": 0.8, "line-dasharray": [1, 1.25] } });
}

function updateSources(map: MapLibreMap, points: MissionPoint[], plan: MissionWaypoint[], templateType: MissionTemplateType, currentWaypoint?: number, trail: Array<{ latitude: number; longitude: number }> = []) {
  const draftCoordinates = points.map((point) => [point.longitude, point.latitude]);
  const draftFeatures: GeoJSON.Feature[] = [];
  if (templateType === "AREA_SCAN" && draftCoordinates.length >= 3) {
    draftFeatures.push({ type: "Feature", properties: { kind: "draft-shape" }, geometry: { type: "Polygon", coordinates: [[...draftCoordinates, draftCoordinates[0]]] } });
  } else if (draftCoordinates.length >= 2) {
    draftFeatures.push({ type: "Feature", properties: { kind: "draft-shape" }, geometry: { type: "LineString", coordinates: draftCoordinates } });
  }
  setSource(map, "mission-draft", draftFeatures);

  const planCoordinates = plan.map((point) => [point.longitude, point.latitude]);
  const planFeatures: GeoJSON.Feature[] = plan.map((point) => ({ type: "Feature", properties: { sequence: point.sequence }, geometry: { type: "Point", coordinates: [point.longitude, point.latitude] } }));
  if (planCoordinates.length >= 2) planFeatures.unshift({ type: "Feature", properties: { kind: "generated-plan" }, geometry: { type: "LineString", coordinates: planCoordinates } });
  setSource(map, "mission-plan", planFeatures);

  const progressFeatures: GeoJSON.Feature[] = [];
  if (currentWaypoint !== undefined && planCoordinates.length > 0) {
    const currentIndex = Math.max(0, Math.min(currentWaypoint, planCoordinates.length - 1));
    const completed = planCoordinates.slice(0, currentIndex + 1);
    if (completed.length >= 2) progressFeatures.push({ type: "Feature", properties: { kind: "completed" }, geometry: { type: "LineString", coordinates: completed } });
    if (currentIndex > 0 && currentIndex < planCoordinates.length) progressFeatures.push({ type: "Feature", properties: { kind: "active-leg" }, geometry: { type: "LineString", coordinates: [planCoordinates[currentIndex - 1], planCoordinates[currentIndex]] } });
    if (currentWaypoint < planCoordinates.length) progressFeatures.push({ type: "Feature", properties: { kind: "current" }, geometry: { type: "Point", coordinates: planCoordinates[currentIndex] } });
  }
  setSource(map, "mission-progress", progressFeatures);

  const trailCoordinates = trail.map((position) => [position.longitude, position.latitude]);
  setSource(map, "aircraft-trail", trailCoordinates.length >= 2 ? [{ type: "Feature", properties: { kind: "aircraft-trail" }, geometry: { type: "LineString", coordinates: trailCoordinates } }] : []);
}

function setSource(map: MapLibreMap, id: string, features: GeoJSON.Feature[]) {
  (map.getSource(id) as GeoJSONSource | undefined)?.setData({ type: "FeatureCollection", features });
}

function syncDraftMarkers(map: MapLibreMap, currentMarkers: maplibregl.Marker[], mode: "edit" | "track", points: MissionPoint[], onMovePoint?: (id: string, latitude: number, longitude: number) => void) {
  currentMarkers.forEach((marker) => marker.remove());
  currentMarkers.splice(0, currentMarkers.length);
  if (mode !== "edit") return;
  for (const [index, point] of points.entries()) {
    const element = document.createElement("button");
    element.type = "button";
    element.className = "mission-map-marker";
    element.textContent = String(index + 1);
    element.setAttribute("aria-label", `Mission vertex ${index + 1}; drag to reposition`);
    element.addEventListener("click", (event) => event.stopPropagation());
    element.addEventListener("pointerdown", (event) => event.stopPropagation());
    const marker = new maplibregl.Marker({ element, draggable: true, anchor: "center" }).setLngLat([point.longitude, point.latitude]).addTo(map);
    marker.on("dragend", () => {
      const position = marker.getLngLat();
      onMovePoint?.(point.id, position.lat, position.lng);
    });
    currentMarkers.push(marker);
  }
}

function syncAircraftMarker(map: MapLibreMap, markerRef: MutableRefObject<maplibregl.Marker | null>, aircraft?: TrackedAircraft) {
  if (!aircraft) {
    markerRef.current?.remove();
    markerRef.current = null;
    return;
  }
  if (!markerRef.current) {
    const element = document.createElement("div");
    element.className = "mission-aircraft-marker";
    element.innerHTML = '<span aria-hidden="true">▲</span>';
    markerRef.current = new maplibregl.Marker({ element, anchor: "center", subpixelPositioning: true }).setLngLat([aircraft.longitude, aircraft.latitude]).addTo(map);
  }
  const element = markerRef.current.getElement();
  element.classList.toggle("mission-aircraft-marker--stale", aircraft.telemetryStatus === "stale");
  element.setAttribute("aria-label", `${aircraft.label || "Aircraft"} live position`);
  element.style.setProperty("--aircraft-heading", `${aircraft.headingDegrees ?? 0}deg`);
  markerRef.current.setLngLat([aircraft.longitude, aircraft.latitude]);
}

function syncHomeMarker(map: MapLibreMap, markerRef: MutableRefObject<maplibregl.Marker | null>, home?: TrackedHome) {
  if (!home) {
    markerRef.current?.remove();
    markerRef.current = null;
    return;
  }
  if (!markerRef.current) {
    const element = document.createElement("div");
    element.className = "mission-home-marker";
    element.innerHTML = '<svg aria-hidden="true" viewBox="0 0 24 24"><path d="M3.5 10.6 12 3.8l8.5 6.8v9.1h-6v-5.5h-5v5.5h-6z"/></svg>';
    markerRef.current = new maplibregl.Marker({ element, anchor: "center", subpixelPositioning: true }).setLngLat([home.longitude, home.latitude]).addTo(map);
  }
  markerRef.current.getElement().setAttribute("aria-label", `${home.label || "Aircraft"} home position`);
  markerRef.current.setLngLat([home.longitude, home.latitude]);
}

function syncPayloadTargetMarker(map: MapLibreMap, markerRef: MutableRefObject<maplibregl.Marker | null>, target?: PayloadTarget) {
  if (!target) {
    markerRef.current?.remove();
    markerRef.current = null;
    return;
  }
  if (!markerRef.current) {
    const element = document.createElement("div");
    element.className = "mission-payload-target-marker";
    element.innerHTML = '<span aria-hidden="true"></span>';
    element.setAttribute("aria-label", "Manual gimbal look target");
    markerRef.current = new maplibregl.Marker({ element, anchor: "center", subpixelPositioning: true })
      .setLngLat([target.longitude, target.latitude])
      .addTo(map);
  }
  markerRef.current.setLngLat([target.longitude, target.latitude]);
}

function fitCoordinates(
  map: MapLibreMap,
  points: MissionPoint[],
  plan: MissionWaypoint[],
  aircraft?: TrackedAircraft,
  home?: TrackedHome,
  planningFocus?: { latitude: number; longitude: number },
) {
  const coordinates = [
    ...points.map((point) => [point.longitude, point.latitude] as [number, number]),
    ...plan.map((point) => [point.longitude, point.latitude] as [number, number]),
    ...(aircraft ? [[aircraft.longitude, aircraft.latitude] as [number, number]] : []),
    ...(home ? [[home.longitude, home.latitude] as [number, number]] : []),
    ...(planningFocus ? [[planningFocus.longitude, planningFocus.latitude] as [number, number]] : []),
  ].filter((coordinate, index, all) => all.findIndex((candidate) => candidate[0] === coordinate[0] && candidate[1] === coordinate[1]) === index);
  if (coordinates.length === 0) return;
  if (coordinates.length === 1) {
    map.easeTo({ center: coordinates[0], zoom: 16, duration: 500 });
    return;
  }
  const bounds = coordinates.reduce((current, coordinate) => current.extend(coordinate), new maplibregl.LngLatBounds(coordinates[0], coordinates[0]));
  map.fitBounds(bounds as LngLatBoundsLike, { padding: 70, maxZoom: 17, duration: 500 });
}

function emptyFeatureCollection(): GeoJSON.FeatureCollection {
  return { type: "FeatureCollection", features: [] };
}

function drawingVerb(templateType: MissionTemplateType) {
  if (templateType === "AREA_SCAN") return "Draw coverage boundary";
  if (templateType === "ROUTE_SCAN") return "Draw route centreline";
  return "Place flight waypoints";
}

function drawingInstruction(templateType: MissionTemplateType, pointCount: number) {
  const minimum = templateType === "AREA_SCAN" ? 3 : templateType === "ROUTE_SCAN" ? 2 : 1;
  if (pointCount < minimum) return `Click the map to add ${minimum - pointCount} more ${minimum - pointCount === 1 ? "point" : "points"}.`;
  return "Click to extend; drag numbered vertices to refine the geometry.";
}

function trackingTitle(currentWaypoint: number | undefined, total: number) {
  if (currentWaypoint === undefined) return `${total} planned waypoints`;
  return `Waypoint ${Math.min(currentWaypoint + 1, total)} of ${total}`;
}

function trackingInstruction(aircraft?: TrackedAircraft) {
  if (!aircraft) return "Waiting for a live aircraft position.";
  if (aircraft.telemetryStatus === "stale") return "Aircraft position is stale; use the event report before commanding flight.";
  return "Aircraft position updates from live vehicle telemetry.";
}

function focusKey(position: { latitude: number; longitude: number }) {
  return `${position.latitude.toFixed(5)}:${position.longitude.toFixed(5)}`;
}
