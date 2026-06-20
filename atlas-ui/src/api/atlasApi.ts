export type DroneStatus = "registered" | "online" | "stale" | "offline";
export type TelemetryState = "unknown" | "fresh" | "stale" | "lost";

export type Telemetry = {
  state: TelemetryState;
  observedAt: string;
  receivedAt: string;
  batteryPercent: number;
  relativeAltitudeM: number;
  flightMode: string;
  armed: boolean;
  inAir: boolean;
  latitude: number;
  longitude: number;
  headingDeg: number;
  gpsFix: string;
  satellitesVisible: number;
  homePositionSet: boolean;
  source: string;
};

export type Drone = {
  id: string;
  name: string;
  agentId: string;
  status: DroneStatus;
  lastSeenAt: string;
  lastHeartbeatAt?: string;
  telemetry?: Telemetry;
};

export async function fetchDrones(signal?: AbortSignal): Promise<Drone[]> {
  const response = await fetch("/api/drones", { signal });

  if (!response.ok) {
    throw new Error(`Backend returned ${response.status}`);
  }

  return response.json() as Promise<Drone[]>;
}
