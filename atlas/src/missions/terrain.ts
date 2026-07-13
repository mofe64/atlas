import type { MissionPlan, MissionSettings, MissionWaypoint } from "./missionTypes";

const EARTH_RADIUS_METERS = 6_371_000;
const DEFAULT_TILE_TEMPLATE = "https://s3.amazonaws.com/elevation-tiles-prod/terrarium/{z}/{x}/{y}.png";
const DEFAULT_ZOOM = 12;
const TILE_SIZE = 256;
const MAX_MERCATOR_LATITUDE = 85.05112878;

export type TerrainEncoding = "terrarium" | "mapbox";

export type TerrainSource = {
  datasetId: string;
  displayName: string;
  tileTemplate: string;
  encoding: TerrainEncoding;
  zoom: number;
  tileSize: number;
  attribution: string;
};

export type TerrainSample = {
  latitude: number;
  longitude: number;
  elevationMeters: number;
};

export type TerrainProfileStation = {
  latitude: number;
  longitude: number;
  sourceWaypointSequence?: number;
  samples: TerrainSample[];
};

export type ApplyTerrainProfileInput = {
  basePlanId: string;
  source: {
    datasetId: string;
    displayName: string;
    encoding: TerrainEncoding;
    zoom: number;
    tileSize: number;
    sampledAtUnixMs: number;
  };
  home: TerrainSample;
  stations: TerrainProfileStation[];
};

type DecodedTile = {
  width: number;
  height: number;
  pixels: Uint8ClampedArray;
};

const tileCache = new Map<string, Promise<DecodedTile>>();

export function terrainSource(): TerrainSource {
  const encoding = import.meta.env.VITE_ATLAS_TERRAIN_ENCODING === "mapbox" ? "mapbox" : "terrarium";
  const configuredZoom = Number(import.meta.env.VITE_ATLAS_TERRAIN_ZOOM);
  return {
    datasetId: import.meta.env.VITE_ATLAS_TERRAIN_DATASET_ID || "mapzen-aws-terrain-tiles",
    displayName: import.meta.env.VITE_ATLAS_TERRAIN_NAME || "Mapzen Terrain Tiles on AWS",
    tileTemplate: import.meta.env.VITE_ATLAS_TERRAIN_TILE_URL || DEFAULT_TILE_TEMPLATE,
    encoding,
    zoom: Number.isInteger(configuredZoom) && configuredZoom >= 0 && configuredZoom <= 22 ? configuredZoom : DEFAULT_ZOOM,
    tileSize: TILE_SIZE,
    attribution: 'Terrain: <a href="https://registry.opendata.aws/terrain-tiles/" target="_blank">Mapzen / AWS Open Data</a>',
  };
}

export async function sampleTerrainElevation(
  latitude: number,
  longitude: number,
  source = terrainSource(),
): Promise<number> {
  validateCoordinate(latitude, longitude);
  const position = tilePosition(latitude, longitude, source.zoom, source.tileSize);
  const tile = await loadTile(source, position.tileX, position.tileY);
  const pixelX = Math.min(tile.width - 1, Math.max(0, Math.floor(position.pixelX * tile.width / source.tileSize)));
  const pixelY = Math.min(tile.height - 1, Math.max(0, Math.floor(position.pixelY * tile.height / source.tileSize)));
  const offset = (pixelY * tile.width + pixelX) * 4;
  return roundElevation(decodeElevation(tile.pixels[offset], tile.pixels[offset + 1], tile.pixels[offset + 2], source.encoding));
}

export async function buildTerrainProfileInput(
  plan: MissionPlan,
  home: { latitude: number; longitude: number },
  settings: MissionSettings,
  onProgress?: (completed: number, total: number) => void,
): Promise<ApplyTerrainProfileInput> {
  if (plan.generatedWaypoints.length === 0) throw new Error("The base plan has no waypoints to profile.");
  const source = terrainSource();
  const stations = interpolateStations(plan.generatedWaypoints, settings.terrainSampleSpacingMeters);
  if (stations.length > 5_000) {
    throw new Error(`Terrain profile requires ${stations.length} stations; increase terrain sample spacing to keep the plan at or below 5,000 stations.`);
  }
  const total = stations.length + 1;
  let completed = 0;
  const homeElevation = await sampleTerrainElevation(home.latitude, home.longitude, source);
  onProgress?.(++completed, total);

  const profiledStations: TerrainProfileStation[] = [];
  for (let index = 0; index < stations.length; index += 1) {
    const station = stations[index];
    const bearing = stationBearing(stations, index);
    const halfWidth = settings.terrainCorridorWidthMeters / 2;
    const sampleCoordinates = halfWidth > 0.5
      ? [-halfWidth, 0, halfWidth].map((offset) => offsetCoordinate(station.latitude, station.longitude, bearing + 90, offset))
      : [{ latitude: station.latitude, longitude: station.longitude }];
    const samples = await Promise.all(sampleCoordinates.map(async (coordinate) => ({
      ...coordinate,
      elevationMeters: await sampleTerrainElevation(coordinate.latitude, coordinate.longitude, source),
    })));
    profiledStations.push({ ...station, samples });
    onProgress?.(++completed, total);
  }

  return {
    basePlanId: plan.id,
    source: {
      datasetId: source.datasetId,
      displayName: source.displayName,
      encoding: source.encoding,
      zoom: source.zoom,
      tileSize: source.tileSize,
      sampledAtUnixMs: Date.now(),
    },
    home: { latitude: home.latitude, longitude: home.longitude, elevationMeters: homeElevation },
    stations: profiledStations,
  };
}

export function interpolateStations(waypoints: MissionWaypoint[], spacingMeters: number): Array<{
  latitude: number;
  longitude: number;
  sourceWaypointSequence?: number;
}> {
  if (waypoints.length === 0) return [];
  if (!Number.isFinite(spacingMeters) || spacingMeters <= 0) throw new Error("Terrain sample spacing must be greater than zero.");
  const stations: Array<{ latitude: number; longitude: number; sourceWaypointSequence?: number }> = [{
    latitude: waypoints[0].latitude,
    longitude: waypoints[0].longitude,
    sourceWaypointSequence: waypoints[0].sequence,
  }];
  for (let index = 1; index < waypoints.length; index += 1) {
    const start = waypoints[index - 1];
    const end = waypoints[index];
    const divisions = Math.max(1, Math.ceil(distanceMeters(start, end) / spacingMeters));
    for (let step = 1; step <= divisions; step += 1) {
      const fraction = step / divisions;
      stations.push({
        latitude: start.latitude + (end.latitude - start.latitude) * fraction,
        longitude: start.longitude + (end.longitude - start.longitude) * fraction,
        sourceWaypointSequence: step === divisions ? end.sequence : undefined,
      });
    }
  }
  return stations;
}

export function distanceMeters(
  a: { latitude: number; longitude: number },
  b: { latitude: number; longitude: number },
) {
  const lat1 = toRadians(a.latitude);
  const lat2 = toRadians(b.latitude);
  const deltaLatitude = lat2 - lat1;
  const deltaLongitude = toRadians(b.longitude - a.longitude);
  const value = Math.sin(deltaLatitude / 2) ** 2
    + Math.cos(lat1) * Math.cos(lat2) * Math.sin(deltaLongitude / 2) ** 2;
  return 2 * EARTH_RADIUS_METERS * Math.asin(Math.sqrt(value));
}

function stationBearing(stations: Array<{ latitude: number; longitude: number }>, index: number) {
  if (stations.length < 2) return 0;
  if (index === 0) return bearingDegrees(stations[0], stations[1]);
  if (index === stations.length - 1) return bearingDegrees(stations[index - 1], stations[index]);
  return bearingDegrees(stations[index - 1], stations[index + 1]);
}

function bearingDegrees(a: { latitude: number; longitude: number }, b: { latitude: number; longitude: number }) {
  const latitude1 = toRadians(a.latitude);
  const latitude2 = toRadians(b.latitude);
  const longitudeDelta = toRadians(b.longitude - a.longitude);
  const y = Math.sin(longitudeDelta) * Math.cos(latitude2);
  const x = Math.cos(latitude1) * Math.sin(latitude2)
    - Math.sin(latitude1) * Math.cos(latitude2) * Math.cos(longitudeDelta);
  return (Math.atan2(y, x) * 180 / Math.PI + 360) % 360;
}

function offsetCoordinate(latitude: number, longitude: number, bearing: number, distance: number) {
  const angularDistance = distance / EARTH_RADIUS_METERS;
  const bearingRadians = toRadians(bearing);
  const latitudeRadians = toRadians(latitude);
  const longitudeRadians = toRadians(longitude);
  const nextLatitude = Math.asin(
    Math.sin(latitudeRadians) * Math.cos(angularDistance)
    + Math.cos(latitudeRadians) * Math.sin(angularDistance) * Math.cos(bearingRadians),
  );
  const nextLongitude = longitudeRadians + Math.atan2(
    Math.sin(bearingRadians) * Math.sin(angularDistance) * Math.cos(latitudeRadians),
    Math.cos(angularDistance) - Math.sin(latitudeRadians) * Math.sin(nextLatitude),
  );
  return { latitude: nextLatitude * 180 / Math.PI, longitude: nextLongitude * 180 / Math.PI };
}

async function loadTile(source: TerrainSource, x: number, y: number): Promise<DecodedTile> {
  const key = `${source.tileTemplate}|${source.zoom}|${x}|${y}`;
  let pending = tileCache.get(key);
  if (!pending) {
    pending = fetchAndDecodeTile(source, x, y).catch((error) => {
      tileCache.delete(key);
      throw error;
    });
    tileCache.set(key, pending);
  }
  return pending;
}

async function fetchAndDecodeTile(source: TerrainSource, x: number, y: number): Promise<DecodedTile> {
  const url = source.tileTemplate
    .replace("{z}", String(source.zoom))
    .replace("{x}", String(x))
    .replace("{y}", String(y));
  let response: Response;
  try {
    response = await fetch(url);
  } catch (reason) {
    throw new Error(`Terrain tile request failed. Check network access and VITE_ATLAS_TERRAIN_TILE_URL. ${messageFrom(reason)}`);
  }
  if (!response.ok) throw new Error(`Terrain tile request returned HTTP ${response.status} for z${source.zoom}/${x}/${y}.`);
  const decodedImage = await decodeImage(await response.blob());
  const canvas = document.createElement("canvas");
  canvas.width = decodedImage.width;
  canvas.height = decodedImage.height;
  const context = canvas.getContext("2d", { willReadFrequently: true });
  if (!context) {
    decodedImage.release();
    throw new Error("Atlas could not create a canvas to decode the terrain tile.");
  }
  context.drawImage(decodedImage.source, 0, 0);
  decodedImage.release();
  const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data;
  return { width: canvas.width, height: canvas.height, pixels };
}

async function decodeImage(blob: Blob): Promise<{
  source: CanvasImageSource;
  width: number;
  height: number;
  release: () => void;
}> {
  if (typeof createImageBitmap === "function") {
    const bitmap = await createImageBitmap(blob);
    return { source: bitmap, width: bitmap.width, height: bitmap.height, release: () => bitmap.close() };
  }
  const url = URL.createObjectURL(blob);
  const image = new Image();
  image.decoding = "async";
  try {
    await new Promise<void>((resolve, reject) => {
      image.addEventListener("load", () => resolve(), { once: true });
      image.addEventListener("error", () => reject(new Error("Atlas could not decode the terrain tile image.")), { once: true });
      image.src = url;
    });
    return { source: image, width: image.naturalWidth, height: image.naturalHeight, release: () => URL.revokeObjectURL(url) };
  } catch (reason) {
    URL.revokeObjectURL(url);
    throw reason;
  }
}

function tilePosition(latitude: number, longitude: number, zoom: number, tileSize: number) {
  const clampedLatitude = Math.max(-MAX_MERCATOR_LATITUDE, Math.min(MAX_MERCATOR_LATITUDE, latitude));
  const scale = 2 ** zoom;
  const worldX = (longitude + 180) / 360 * scale;
  const latitudeRadians = toRadians(clampedLatitude);
  const worldY = (1 - Math.asinh(Math.tan(latitudeRadians)) / Math.PI) / 2 * scale;
  const tileX = Math.min(scale - 1, Math.max(0, Math.floor(worldX)));
  const tileY = Math.min(scale - 1, Math.max(0, Math.floor(worldY)));
  return {
    tileX,
    tileY,
    pixelX: (worldX - tileX) * tileSize,
    pixelY: (worldY - tileY) * tileSize,
  };
}

function decodeElevation(red: number, green: number, blue: number, encoding: TerrainEncoding) {
  if (encoding === "mapbox") return -10_000 + (red * 256 * 256 + green * 256 + blue) * 0.1;
  return red * 256 + green + blue / 256 - 32_768;
}

function validateCoordinate(latitude: number, longitude: number) {
  if (!Number.isFinite(latitude) || latitude < -90 || latitude > 90 || !Number.isFinite(longitude) || longitude < -180 || longitude > 180) {
    throw new Error("Terrain sampling requires a valid latitude and longitude.");
  }
}

function roundElevation(value: number) {
  return Math.round(value * 10) / 10;
}

function toRadians(value: number) {
  return value * Math.PI / 180;
}

function messageFrom(reason: unknown) {
  return reason instanceof Error ? reason.message : String(reason);
}
