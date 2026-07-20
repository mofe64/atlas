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

export type IncidentResponsePattern = "HOLD_AT_STAGING" | "OFFSET_OBSERVE" | "BOUNDED_AREA_SCAN" | "BOUNDED_ORBIT";

export type ResponseCoordinate = { latitude: number; longitude: number };

export type IncidentResponseGeometry =
  | {
      responsePattern: "HOLD_AT_STAGING";
      stagingLatitude: number;
      stagingLongitude: number;
      altitudeMeters: number;
      speedMps: number;
    }
  | {
      responsePattern: "OFFSET_OBSERVE";
      observationLatitude: number;
      observationLongitude: number;
      altitudeMeters: number;
      speedMps: number;
    }
  | {
      responsePattern: "BOUNDED_AREA_SCAN";
      areaPolygon: ResponseCoordinate[];
      altitudeMeters: number;
      speedMps: number;
      laneSpacingMeters: number;
      sweepAngleDegrees: number;
    }
  | {
      responsePattern: "BOUNDED_ORBIT";
      centerLatitude: number;
      centerLongitude: number;
      radiusMeters: number;
      altitudeLevelsMeters: number[];
      speedMps: number;
      lapsPerLevel: number;
      direction: "CLOCKWISE" | "COUNTERCLOCKWISE";
      maxVerticalRateMps: number;
    };

export type AircraftSuitabilityReason = {
  code: string;
  message: string;
};

export type IncidentResponseAircraftSuitability = {
  droneId: string;
  droneName: string;
  available: boolean;
  recommended: boolean;
  connectionStatus: ConnectionStatus;
  batteryPercent?: number | null;
  telemetryAgeMs?: number | null;
  distanceMeters?: number | null;
  estimatedArrivalSeconds?: number | null;
  activeIncidentId?: string | null;
  unfinishedMissionRunId?: string | null;
  blockers: AircraftSuitabilityReason[];
  considerations: AircraftSuitabilityReason[];
};

export type PrepareIncidentResponseInput = {
  expectedIncidentRevision: number;
  droneId: string;
  geometry: IncidentResponseGeometry;
  arrivalFailurePolicy: ArrivalFailurePolicy;
  incidentTargetAltitudeAmslMeters?: number | null;
  buildingHorizontalClearanceMeters: number;
  buildingVerticalClearanceMeters: number;
  knownBuildingOverrideReason?: string | null;
};

export type KnownBuildingAssessment = {
  status: "CLEAR_OF_CHECKED_VOLUMES" | "INTERSECTIONS" | "INCOMPLETE" | "DATA_UNAVAILABLE" | string;
  statement: string;
  checkedFeatureCount: number;
  horizontalClearanceMeters: number;
  verticalClearanceMeters: number;
  homeAbsoluteAltitudeMeters?: number | null;
  routeStart?: {
    latitude: number;
    longitude: number;
    relativeAltitudeMeters: number;
  } | null;
  routeSegmentCount: number;
  intersectionCount: number;
  unknownHeightCount: number;
  coverageComplete: boolean;
  overrideRequired: boolean;
  overrideReason?: string | null;
  provenance?: {
    provider: string;
    product: string;
    datasetId: string;
    schemaVersion: string;
    release: string;
    retrievedAtUnixMs: number;
    coverageBbox: [number, number, number, number];
  } | null;
  issues: Array<{
    featureId: string;
    result: "INTERSECTION" | "HEIGHT_OR_DATUM_UNKNOWN" | string;
    routeSegmentIndexes: number[];
    routePointIndexes: number[];
    absoluteBaseMeters?: number | null;
    absoluteTopMeters?: number | null;
    relativeTopMeters?: number | null;
    heightSource: string;
    heightConfidence?: string | null;
    evidenceDate?: string | null;
    footprint: ResponseCoordinate[];
  }>;
  limitations: string[];
};

export type IncidentResponsePlanPreview = {
  templateType: Mission["templateType"];
  patternType: string;
  generatedWaypoints: MissionPlan["generatedWaypoints"];
  actions: MissionPlan["actions"];
  metadata: MissionPlan["metadata"];
  validationWarnings: string[];
  knownBuildingAssessment: KnownBuildingAssessment;
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

export type OperationalTrackGeolocation = {
  geolocation: {
    id: string;
    commandId: string;
    selectionId: string;
    droneId: string;
    trackSessionId: string;
    trackId: string;
    sourceId: string;
    status: "SUCCEEDED";
    requestedAtUnixMs: number;
    resolvedAtUnixMs?: number | null;
    aimPoint: "GROUND_CONTACT" | "TARGET_CENTER";
    assumedAimPointHeightM: number;
    assumedAimPointHeightUncertaintyM: number;
    groundAltitudeAmslM: number;
    groundAltitudeUncertaintyM: number;
    groundAltitudeSource: string;
    groundAltitudeSourceVersion: string;
    groundAltitudeResolvedAtUnixMs: number;
    latitude?: number | null;
    longitude?: number | null;
    altitudeAmslM?: number | null;
    horizontalUncertaintyM?: number | null;
    method: string;
    frameObservedAtUnixMs?: number | null;
    refinementStatus: "NOT_REQUESTED" | "CONVERGED" | "MAX_ITERATIONS";
    terrainSource: string;
    terrainSourceVersion: string;
    terrainIterationCount: number;
    terrainResidualM?: number | null;
    rangeSource: string;
    filteredLatitude?: number | null;
    filteredLongitude?: number | null;
    targetVelocityNorthMps?: number | null;
    targetVelocityEastMps?: number | null;
    targetSpeedMps?: number | null;
    targetDirectionDeg?: number | null;
    targetVelocityUncertaintyMps?: number | null;
    motionStatus: string;
    rejectionCode: string;
    rejectionReason: string;
    evidence?: unknown;
  };
  droneName: string;
  classLabel: string;
  lifecycleState: "TENTATIVE" | "ACTIVE" | "TEMPORARILY_OCCLUDED" | "LOST" | "CLOSED";
  observationCount: number;
  selectionStatus: string;
  annotationCount: number;
  evidenceMarkerCount: number;
};
