import { invoke } from "@tauri-apps/api/core";
import { sampleTerrainElevation, terrainSource, terrainSourceVersion } from "../missions/terrain";
import type { OperationalTrackGeolocation } from "../operationsTypes";
import type { TrackGeolocation } from "./followTypes";

const terminalCommandStates = new Set(["succeeded", "failed", "rejected", "timed_out", "cancelled"]);
const terrainLookupTimeoutMs = 3_000;
const refinementMaximumIterations = 5;
const refinementConvergenceM = 0.75;

type CommandReceipt = {
  id: string;
  status: string;
  resultCode: string;
  resultMessage: string;
  deadlineAtUnixMs: number;
};

export async function acquireTrackGeolocation(
  operational: OperationalTrackGeolocation,
  gimbalId: number,
): Promise<TrackGeolocation> {
  const previous = operational.geolocation;
  const receipt = await dispatchVehicleCommand(previous.droneId, "geolocate_selected_track", {
    selectionId: previous.selectionId,
    sourceId: previous.sourceId,
    trackSessionId: previous.trackSessionId,
    trackId: previous.trackId,
    gimbalId,
    aimPoint: previous.aimPoint,
    groundAltitudeAmslMeters: previous.groundAltitudeAmslM,
    groundAltitudeUncertaintyMeters: previous.groundAltitudeUncertaintyM,
    groundAltitudeSource: previous.groundAltitudeSource,
    groundAltitudeSourceVersion: previous.groundAltitudeSourceVersion,
    groundAltitudeResolvedAtUnixMs: previous.groundAltitudeResolvedAtUnixMs,
    assumedAimPointHeightMeters: previous.assumedAimPointHeightM,
    assumedAimPointHeightUncertaintyMeters: previous.assumedAimPointHeightUncertaintyM,
    requestedBy: "operator-follow-supervision",
  });
  const geolocations = await invoke<TrackGeolocation[]>("perception_track_geolocations", {
    trackSessionId: previous.trackSessionId,
    trackId: previous.trackId,
    limit: 12,
  });
  const initial = geolocations.find((candidate) => candidate.commandId === receipt.id);
  if (!initial || initial.status !== "SUCCEEDED") {
    throw new Error("The Agent response did not produce a durable world-space target coordinate.");
  }
  return refineTrackGeolocation(initial);
}

async function dispatchVehicleCommand(
  droneId: string,
  commandType: string,
  parameters: Record<string, unknown>,
) {
  const initial = await invoke<CommandReceipt>("request_vehicle_command", {
    droneId,
    commandType,
    parametersJson: JSON.stringify(parameters),
    timeoutMs: 15_000,
  });
  let current = initial;
  while (!terminalCommandStates.has(current.status) && Date.now() <= initial.deadlineAtUnixMs + 1_500) {
    await wait(200);
    current = await invoke<CommandReceipt>("vehicle_command_detail", { commandId: initial.id });
  }
  if (current.status !== "succeeded") {
    throw new Error(current.resultMessage || current.resultCode || `${commandType.replace(/_/g, " ")} ${current.status}`);
  }
  return current;
}

async function refineTrackGeolocation(initial: TrackGeolocation): Promise<TrackGeolocation> {
  if (!finiteCoordinate(initial.latitude, -90, 90) || !finiteCoordinate(initial.longitude, -180, 180)) {
    throw new Error("The initial geolocation has no valid coordinate to sample.");
  }
  const evidence = asRecord(initial.evidence);
  const estimate = asRecord(evidence?.estimate);
  const origin = asRecord(estimate?.origin);
  const direction = asRecord(estimate?.worldDirectionNed);
  const ray = {
    originLatitude: requiredFinite(origin?.latitudeDeg, "origin latitude"),
    originLongitude: requiredFinite(origin?.longitudeDeg, "origin longitude"),
    originAltitudeM: requiredFinite(origin?.altitudeMeters, "origin altitude"),
    north: requiredFinite(direction?.x, "ray north component"),
    east: requiredFinite(direction?.y, "ray east component"),
    down: requiredFinite(direction?.z, "ray down component"),
  };
  const norm = Math.hypot(ray.north, ray.east, ray.down);
  if (norm < 0.9 || norm > 1.1 || ray.down <= 0) {
    throw new Error("Agent evidence does not contain a valid downward world-space observation ray.");
  }

  const source = terrainSource();
  const samples: Array<{ latitude: number; longitude: number; altitudeAmslM: number }> = [];
  let coordinate = { latitude: initial.latitude, longitude: initial.longitude };
  for (let iteration = 0; iteration < refinementMaximumIterations; iteration += 1) {
    const altitudeAmslM = await promiseWithTimeout(
      sampleTerrainElevation(coordinate.latitude, coordinate.longitude, source),
      terrainLookupTimeoutMs,
      `target terrain lookup ${iteration + 1} timed out`,
    );
    samples.push({ ...coordinate, altitudeAmslM });
    const next = terrainRayIntersection(ray, altitudeAmslM + initial.assumedAimPointHeightM);
    const residual = horizontalDistanceMeters(coordinate, next);
    coordinate = next;
    if (residual <= refinementConvergenceM) break;
  }
  const refined = await invoke<TrackGeolocation>("refine_perception_track_geolocation", {
    input: {
      geolocationId: initial.id,
      terrainSource: `Automatic target-area DEM · ${source.displayName}`.slice(0, 240),
      terrainSourceVersion: `target-ray-iterative-v1:${terrainSourceVersion(source)}`.slice(0, 240),
      terrainVerticalUncertaintyM: source.verticalUncertaintyMeters,
      convergenceThresholdM: refinementConvergenceM,
      samples,
    },
  });
  if (refined.refinementStatus !== "CONVERGED") {
    throw new Error("Target-area terrain intersection did not converge inside the accepted residual.");
  }
  return refined;
}

function terrainRayIntersection(
  ray: { originLatitude: number; originLongitude: number; originAltitudeM: number; north: number; east: number; down: number },
  altitudeM: number,
) {
  const verticalDrop = ray.originAltitudeM - altitudeM;
  if (!Number.isFinite(verticalDrop) || verticalDrop <= 0) {
    throw new Error("Target terrain is not below the observation origin.");
  }
  const slantRangeM = verticalDrop / ray.down;
  const groundRangeM = slantRangeM * Math.hypot(ray.north, ray.east);
  if (!Number.isFinite(slantRangeM) || slantRangeM <= 0 || groundRangeM > 3_000) {
    throw new Error("Target terrain intersection exceeds the bounded 3 km ground range.");
  }
  return offsetWgs84(
    ray.originLatitude,
    ray.originLongitude,
    altitudeM,
    slantRangeM * ray.north,
    slantRangeM * ray.east,
  );
}

function offsetWgs84(latitudeDeg: number, longitudeDeg: number, altitudeM: number, northM: number, eastM: number) {
  const semiMajorM = 6_378_137;
  const eccentricitySquared = 6.69437999014e-3;
  const latitude = latitudeDeg * Math.PI / 180;
  const sinLatitude = Math.sin(latitude);
  const denominator = Math.sqrt(1 - eccentricitySquared * sinLatitude * sinLatitude);
  const primeVerticalRadius = semiMajorM / denominator;
  const meridianRadius = semiMajorM * (1 - eccentricitySquared) / denominator ** 3;
  const nextLatitude = latitude + northM / (meridianRadius + altitudeM);
  const longitudeScale = (primeVerticalRadius + altitudeM) * Math.cos(latitude);
  if (Math.abs(longitudeScale) < 1e-6) throw new Error("Terrain intersection is undefined at the pole.");
  return {
    latitude: nextLatitude * 180 / Math.PI,
    longitude: (longitudeDeg * Math.PI / 180 + eastM / longitudeScale) * 180 / Math.PI,
  };
}

function horizontalDistanceMeters(
  left: { latitude: number; longitude: number },
  right: { latitude: number; longitude: number },
) {
  const earthRadiusM = 6_378_137;
  const meanLatitude = (left.latitude + right.latitude) * 0.5 * Math.PI / 180;
  const northM = (right.latitude - left.latitude) * Math.PI / 180 * earthRadiusM;
  const eastM = (right.longitude - left.longitude) * Math.PI / 180 * earthRadiusM * Math.cos(meanLatitude);
  return Math.hypot(northM, eastM);
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

function requiredFinite(value: unknown, label: string) {
  const number = Number(value);
  if (!Number.isFinite(number)) throw new Error(`Agent evidence is missing ${label}.`);
  return number;
}

function finiteCoordinate(value: number | null | undefined, minimum: number, maximum: number): value is number {
  return value != null && Number.isFinite(value) && value >= minimum && value <= maximum;
}

function promiseWithTimeout<T>(promise: Promise<T>, timeoutMs: number, message: string) {
  return new Promise<T>((resolve, reject) => {
    const timer = window.setTimeout(() => reject(new Error(message)), timeoutMs);
    promise.then(
      (value) => { window.clearTimeout(timer); resolve(value); },
      (reason) => { window.clearTimeout(timer); reject(reason); },
    );
  });
}

function wait(milliseconds: number) {
  return new Promise((resolve) => window.setTimeout(resolve, milliseconds));
}
