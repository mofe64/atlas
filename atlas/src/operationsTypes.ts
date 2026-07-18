import type { Mission, MissionPlan } from "./missions/missionTypes";

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

export type IncidentPriority = "LOW" | "MEDIUM" | "HIGH" | "CRITICAL";
export type IncidentStatus = "OPEN" | "ACTIVE" | "RESOLVED" | "CANCELLED";

export type IncidentSnapshot = {
  id: string;
  sourceType: "MANUAL" | string;
  sourceSystem: "ATLAS_NATIVE" | string;
  externalId?: string | null;
  incidentType: string;
  priority: IncidentPriority;
  status: IncidentStatus;
  summary: string;
  description: string;
  latitude?: number | null;
  longitude?: number | null;
  address: string;
  area: string;
  occurredAtUnixMs?: number | null;
  receivedAtUnixMs: number;
  createdAtUnixMs: number;
  updatedAtUnixMs: number;
  revision: number;
  locationRevision: number;
  sourcePayload?: Record<string, unknown> | null;
};

export type IncidentEvent = {
  id: string;
  incidentId: string;
  sequence: number;
  eventType: string;
  state: IncidentStatus | string;
  source: string;
  message: string;
  details: Record<string, unknown>;
  occurredAtUnixMs: number;
  receivedAtUnixMs: number;
};

export type IncidentDetail = {
  incident: IncidentSnapshot;
  events: IncidentEvent[];
  assignments: IncidentAssignment[];
};

export type IncidentAssignment = {
  id: string;
  incidentId: string;
  droneId: string;
  droneName: string;
  missionId?: string | null;
  missionName?: string | null;
  generatedPlanId?: string | null;
  missionRunId?: string | null;
  operatorId?: string | null;
  status: string;
  assignedAtUnixMs: number;
  onSceneAtUnixMs?: number | null;
  endedAtUnixMs?: number | null;
};

export type ArrivalFailurePolicy = "RETURN_TO_LAUNCH" | "OPERATOR_INTERVENTION";

export type PrepareIncidentResponseInput = {
  expectedIncidentRevision: number;
  droneId: string;
  stagingLatitude: number;
  stagingLongitude: number;
  altitudeMeters: number;
  speedMps: number;
  arrivalFailurePolicy: ArrivalFailurePolicy;
  pointGimbalAtIncident: boolean;
  incidentTargetAltitudeAmslMeters?: number | null;
};

export type PreparedIncidentResponse = {
  incident: IncidentSnapshot;
  assignment: IncidentAssignment;
  mission: Mission;
  plan: MissionPlan;
};

export type CreateIncidentInput = {
  incidentType: string;
  priority: IncidentPriority;
  summary: string;
  description: string;
  latitude?: number | null;
  longitude?: number | null;
  address: string;
  area: string;
  occurredAtUnixMs?: number | null;
};

export type UpdateIncidentInput = CreateIncidentInput & {
  expectedRevision: number;
  status: IncidentStatus;
};
