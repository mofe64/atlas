export type ConnectionStatus = "connected" | "stale" | "disconnected";
export type StatusTone = "positive" | "warning" | "neutral" | "critical";
export type NativeState = "starting" | "available" | "unavailable";
export type Nullable<T> = T | null | undefined;

export type FleetTelemetry = {
  status: "live" | "stale";
  receivedAtUnixMs: number;
  batteryPercent?: number | null;
  relativeAltitudeM?: number | null;
  absoluteAltitudeM?: number | null;
  flightMode?: string | null;
  armed?: boolean | null;
  inAir?: boolean | null;
  landedState?: string | null;
  latitude?: number | null;
  longitude?: number | null;
  headingDeg?: number | null;
  groundSpeedMps?: number | null;
  gpsFix?: string | null;
  satellitesVisible?: number | null;
  homePositionSet?: boolean | null;
  homePosition?: {
    latitude?: number | null;
    longitude?: number | null;
    absoluteAltitudeM?: number | null;
    relativeAltitudeM?: number | null;
  } | null;
  health?: {
    localPositionOk: boolean;
    globalPositionOk: boolean;
    homePositionOk: boolean;
    armable: boolean;
  } | null;
  batteries?: Array<{
    function: string;
    voltageV?: number | null;
  }> | null;
};

export type FleetStatusEvent = {
  severity: string;
  message: string;
  receivedAtUnixMs: number;
};

export type FleetAircraft = {
  connectionStatus: ConnectionStatus;
  droneId?: string | null;
  droneName?: string | null;
  vehicleType?: string | null;
  vehicleStatus?: string | null;
  agentCapabilities?: string[];
  remoteAddress?: string | null;
  lastHeartbeatAtUnixMs?: number | null;
  telemetry?: FleetTelemetry | null;
  statusEvents: FleetStatusEvent[];
};

export type FleetSnapshot = {
  generatedAtUnixMs: number;
  aircraft: FleetAircraft[];
};
