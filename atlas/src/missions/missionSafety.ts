import type { FleetAircraft } from "../operationsTypes";

// Keep this value aligned with MAX_FIRST_WAYPOINT_DISTANCE_METERS in Native.
export const MAX_FIRST_WAYPOINT_DISTANCE_METERS = 5_000;

export type MissionPositionReference = {
  latitude: number;
  longitude: number;
  source: "aircraft" | "home";
  droneId: string;
  droneName: string;
};

export type MissionDistanceStatus = {
  ok: boolean;
  message?: string;
  distanceMeters?: number;
  reference?: MissionPositionReference;
};

export function planningPositionReference(aircraft: FleetAircraft | undefined): MissionPositionReference | undefined {
  if (!aircraft?.droneId || aircraft.connectionStatus !== "connected") return undefined;
  const telemetry = aircraft.telemetry;
  const droneName = aircraft.droneName || aircraft.droneId;
  const current = coordinates(telemetry?.latitude, telemetry?.longitude);
  if (current) {
    return {
      ...current,
      source: "aircraft",
      droneId: aircraft.droneId,
      droneName,
    };
  }
  return homePositionReference(aircraft);
}

export function homePositionReference(aircraft: FleetAircraft | undefined): MissionPositionReference | undefined {
  if (!aircraft?.droneId || aircraft.connectionStatus !== "connected") return undefined;
  const telemetry = aircraft.telemetry;
  const home = telemetry?.homePosition;
  const homeCoordinates = coordinates(home?.latitude, home?.longitude);
  if (telemetry?.homePositionSet !== true || !homeCoordinates) return undefined;
  return {
    ...homeCoordinates,
    source: "home",
    droneId: aircraft.droneId,
    droneName: aircraft.droneName || aircraft.droneId,
  };
}

export function uploadPositionReference(aircraft: FleetAircraft | undefined): MissionPositionReference | undefined {
  if (!aircraft?.droneId || aircraft.connectionStatus !== "connected") return undefined;
  const telemetry = aircraft.telemetry;
  const droneName = aircraft.droneName || aircraft.droneId;
  const home = homePositionReference(aircraft);
  if (home) return home;
  const current = coordinates(telemetry?.latitude, telemetry?.longitude);
  if (current) {
    return {
      ...current,
      source: "aircraft",
      droneId: aircraft.droneId,
      droneName,
    };
  }
  return undefined;
}

export function missionDistanceStatus(firstWaypoint: { latitude: number; longitude: number } | undefined, aircraft: FleetAircraft | undefined): MissionDistanceStatus {
  if (!firstWaypoint) return { ok: false, message: "Upload blocked: this plan has no first waypoint." };
  const reference = uploadPositionReference(aircraft);
  if (!reference) {
    const name = aircraft?.droneName || aircraft?.droneId || "the selected aircraft";
    return { ok: false, message: `Upload blocked: waiting for ${name} to report a valid home or current position.` };
  }
  const distanceMeters = distanceBetween(
    reference.latitude,
    reference.longitude,
    firstWaypoint.latitude,
    firstWaypoint.longitude,
  );
  if (distanceMeters > MAX_FIRST_WAYPOINT_DISTANCE_METERS) {
    return {
      ok: false,
      distanceMeters,
      reference,
      message: `Upload blocked: the first waypoint is ${formatDistance(distanceMeters)} from ${reference.droneName} ${reference.source} position. Move it within 5 km or select the intended aircraft.`,
    };
  }
  return { ok: true, distanceMeters, reference };
}

export function distanceBetween(latitude: number, longitude: number, targetLatitude: number, targetLongitude: number) {
  const earthRadiusMeters = 6_371_000;
  const latitudeRadians = latitude * Math.PI / 180;
  const targetLatitudeRadians = targetLatitude * Math.PI / 180;
  const latitudeDelta = (targetLatitude - latitude) * Math.PI / 180;
  const longitudeDelta = (targetLongitude - longitude) * Math.PI / 180;
  const haversine = Math.sin(latitudeDelta / 2) ** 2
    + Math.cos(latitudeRadians) * Math.cos(targetLatitudeRadians) * Math.sin(longitudeDelta / 2) ** 2;
  return earthRadiusMeters * 2 * Math.atan2(Math.sqrt(haversine), Math.sqrt(1 - haversine));
}

export function formatDistance(distanceMeters: number) {
  return distanceMeters >= 1_000 ? `${(distanceMeters / 1_000).toFixed(1)} km` : `${Math.round(distanceMeters)} m`;
}

function coordinates(latitude: number | null | undefined, longitude: number | null | undefined) {
  if (latitude != null
    && longitude != null
    && Number.isFinite(latitude)
    && Number.isFinite(longitude)
    && latitude >= -90
    && latitude <= 90
    && longitude >= -180
    && longitude <= 180) {
    return { latitude, longitude };
  }
  return undefined;
}
