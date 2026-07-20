export type MissionTemplateType = "WAYPOINT" | "AREA_SCAN" | "ROUTE_SCAN";
export type CameraMode = "FORWARD_OBLIQUE" | "DOWNWARD_SCAN" | "DOWNWARD_OBLIQUE_SCAN" | "LOOK_AT_POINT" | "FIXED_ANGLE";
export type GimbalYawMode = "FOLLOW_DRONE_HEADING" | "FOLLOW_LANE_DIRECTION" | "FOLLOW_ROUTE_BEARING" | "LOCKED_TO_ROUTE" | "LOOK_AT_POINT" | "FIXED_ANGLE";
export type AltitudeMode = "HOME_RELATIVE" | "TERRAIN_CLEARANCE";
export type MissionActionExecutionState = "REQUESTED" | "RUNNING" | "RETRYING" | "SUCCEEDED" | "FAILED" | "POLICY_APPLIED";

export type MissionTemplate = {
  id: string;
  name: string;
  templateType: MissionTemplateType;
  description: string;
  supportedPatterns: string[];
  defaultPattern: string;
  requiredParams: string[];
  optionalParams: string[];
  defaultParams: Record<string, unknown>;
  version: number;
};

export type MissionPoint = {
  id: string;
  latitude: number;
  longitude: number;
  altitudeMeters?: number;
  speedMps?: number;
  headingDegrees?: number;
  holdSeconds?: number;
  viewModeOverride?: CameraMode;
  gimbalPitchDegrees?: number;
  gimbalYawDegrees?: number;
  gimbalTargetLatitude?: number;
  gimbalTargetLongitude?: number;
};

export type Mission = {
  id: string;
  templateType: MissionTemplateType;
  name: string;
  description: string;
  status: string;
  params: Record<string, unknown>;
  selectedPattern: string;
  generatedPlanId?: string;
  updatedAtUnixMs: number;
};

export type MissionWaypoint = {
  sequence: number;
  latitude: number;
  longitude: number;
  altitudeMeters: number;
  speedMps?: number;
  headingDegrees?: number;
  holdSeconds?: number;
};

export type MissionAction = {
  sequence: number;
  actionType: string;
  params: Record<string, unknown>;
};

export type MissionPlan = {
  id: string;
  missionId: string;
  templateType: MissionTemplateType;
  patternType: string;
  status: string;
  generatedWaypoints: MissionWaypoint[];
  actions: MissionAction[];
  metadata: {
    waypointCount?: number;
    estimatedDistanceMeters?: number;
    laneSpacingMeters?: number;
    sweepAngleDegrees?: number;
    corridorWidthMeters?: number;
    sampleSpacingMeters?: number;
    altitudeMode?: AltitudeMode;
    basePlanId?: string;
    incidentResponse?: {
      incidentId: string;
      incidentRevision: number;
      locationRevision: number;
      incidentLatitude: number;
      incidentLongitude: number;
      responsePattern?: "HOLD_AT_STAGING" | "OFFSET_OBSERVE" | "BOUNDED_AREA_SCAN" | "BOUNDED_ORBIT";
      reviewedGeometry?: Record<string, unknown>;
      stagingLatitude?: number;
      stagingLongitude?: number;
      altitudeMeters?: number;
      speedMps?: number;
      arrivalFailurePolicy: "RETURN_TO_LAUNCH" | "OPERATOR_INTERVENTION";
      optionalActionFailurePolicy: "SKIP_OPTIONAL_AND_NOTIFY";
      pointGimbalAtIncident?: boolean;
      incidentTargetAltitudeAmslMeters?: number | null;
      buildingHorizontalClearanceMeters?: number;
      buildingVerticalClearanceMeters?: number;
      knownBuildingOverrideReason?: string | null;
      knownBuildingAssessment?: import("../operationsTypes").KnownBuildingAssessment;
      orbit?: {
        centerLatitude: number;
        centerLongitude: number;
        radiusMeters: number;
        altitudeReference: "HOME_RELATIVE";
        altitudeLevelsMeters: number[];
        lapsPerLevel: number;
        direction: "CLOCKWISE" | "COUNTERCLOCKWISE";
        transition: "NONE_SINGLE_LEVEL" | string;
        transitionCount: number;
        maxVerticalRateMps: number;
        pointsPerLap: number;
      };
      reviewedAtUnixMs: number;
    };
    terrainProfile?: {
      datasetId?: string;
      displayName?: string;
      homeElevationMeters?: number;
      stationCount?: number;
      sampleCount?: number;
      minimumTerrainElevationMeters?: number;
      maximumTerrainElevationMeters?: number;
      minimumRelativeAltitudeMeters?: number;
      maximumRelativeAltitudeMeters?: number;
      minimumClearanceMeters?: number;
      safetyMarginMeters?: number;
      sampleSpacingMeters?: number;
      corridorWidthMeters?: number;
      profilePoints?: Array<{
        sequence: number;
        groundRelativeAltitudeMeters: number;
        plannedRelativeAltitudeMeters: number;
      }>;
    };
  };
  validationWarnings: string[];
};

export type MissionRunStatus = "UPLOADING" | "READY" | "RUNNING" | "PAUSED" | "COMPLETED" | "FAILED" | "CANCELLED" | "RTL";

export type MissionRunEvent = {
  id: string;
  sequence: number;
  operationId?: string;
  eventType: string;
  state: MissionRunStatus;
  source: "atlas_native" | "atlas_agent" | string;
  occurredAtUnixMs: number;
  currentWaypoint?: number;
  totalWaypoints?: number;
  progressPercent?: number;
  errorCode: string;
  message: string;
  evidenceJson?: string;
};

export type MissionRun = {
  id: string;
  missionId: string;
  missionPlanId: string;
  missionName: string;
  templateType: MissionTemplateType;
  patternType: string;
  droneId: string;
  droneName: string;
  status: MissionRunStatus;
  currentWaypoint?: number;
  totalWaypoints: number;
  uploadProgressPercent: number;
  progressPercent: number;
  createdAtUnixMs: number;
  updatedAtUnixMs: number;
  uploadedAtUnixMs?: number;
  startedAtUnixMs?: number;
  pausedAtUnixMs?: number;
  completedAtUnixMs?: number;
  errorCode: string;
  errorMessage: string;
  actions: MissionActionExecution[];
  events: MissionRunEvent[];
};

export type MissionActionExecutionEvent = {
  id: string;
  sequence: number;
  state: MissionActionExecutionState;
  attempt: number;
  source: "atlas_native" | "atlas_agent" | string;
  occurredAtUnixMs: number;
  errorCode: string;
  message: string;
  evidenceJson?: string;
};

export type MissionActionExecution = {
  id: string;
  missionRunId: string;
  missionPlanId: string;
  actionSequence: number;
  actionType: "HOLD_AT_ARRIVAL" | "POINT_GIMBAL_AT_INCIDENT" | "RESUME_AFTER_ARRIVAL" | string;
  state: MissionActionExecutionState;
  attempt: number;
  maxAttempts: number;
  failurePolicy: "RETURN_TO_LAUNCH" | "OPERATOR_INTERVENTION" | "SKIP_OPTIONAL_AND_NOTIFY";
  timeoutMs: number;
  retryInitialDelayMs: number;
  retryBackoffMultiplier: number;
  attemptDeadlineAtUnixMs?: number;
  nextAttemptAtUnixMs?: number;
  requestedAtUnixMs: number;
  updatedAtUnixMs: number;
  startedAtUnixMs?: number;
  completedAtUnixMs?: number;
  errorCode: string;
  errorMessage: string;
  evidenceJson?: string;
  events: MissionActionExecutionEvent[];
};

export type MissionSettings = {
  altitudeMode: AltitudeMode;
  altitudeMeters: number;
  defaultAltitudeMeters: number;
  speedMps: number;
  defaultSpeedMps: number;
  takeoffAltitudeMeters: number;
  laneSpacingMeters: number;
  overlapPercent: number;
  sweepAngleDegrees: number;
  corridorWidthMeters: number;
  sampleSpacingMeters: number;
  terrainSafetyMarginMeters: number;
  terrainSampleSpacingMeters: number;
  terrainCorridorWidthMeters: number;
  terrainMaxClimbRateMps: number;
  terrainMaxDescentRateMps: number;
  terrainMaxRelativeAltitudeMeters: number;
  cameraMode: CameraMode;
  gimbalPitchDegrees: number;
  gimbalYawMode: GimbalYawMode;
  gimbalYawDegrees: number;
  gimbalTargetLatitude?: number;
  gimbalTargetLongitude?: number;
  gimbalStabilization: boolean;
  zoomPercent: number;
  detectionClasses: string;
  returnToLaunch: boolean;
  recordVideo: boolean;
};

export const fallbackTemplates: MissionTemplate[] = [
  {
    id: "waypoint-v1",
    name: "Waypoint mission",
    templateType: "WAYPOINT",
    description: "Fly an ordered set of operator-defined positions.",
    supportedPatterns: ["DIRECT_WAYPOINTS"],
    defaultPattern: "DIRECT_WAYPOINTS",
    requiredParams: ["waypoints"],
    optionalParams: ["altitudeMode", "terrain"],
    defaultParams: { cameraMode: "FORWARD_OBLIQUE", gimbal: { pitchDegrees: -35, yawMode: "FOLLOW_DRONE_HEADING", stabilization: true }, zoomPercent: 0 },
    version: 1,
  },
  {
    id: "area-scan-v1",
    name: "Area scan",
    templateType: "AREA_SCAN",
    description: "Generate alternating coverage lanes inside a polygon.",
    supportedPatterns: ["LAWN_MOWER"],
    defaultPattern: "LAWN_MOWER",
    requiredParams: ["areaPolygon", "altitudeMeters"],
    optionalParams: ["altitudeMode", "terrain"],
    defaultParams: { cameraMode: "DOWNWARD_SCAN", gimbal: { pitchDegrees: -90, yawMode: "FOLLOW_LANE_DIRECTION", stabilization: true }, zoomPercent: 0 },
    version: 1,
  },
  {
    id: "route-scan-v1",
    name: "Route scan",
    templateType: "ROUTE_SCAN",
    description: "Follow and sample a road, river, path, or corridor.",
    supportedPatterns: ["ROUTE_FOLLOW"],
    defaultPattern: "ROUTE_FOLLOW",
    requiredParams: ["route", "altitudeMeters"],
    optionalParams: ["altitudeMode", "terrain"],
    defaultParams: { cameraMode: "FORWARD_OBLIQUE", gimbal: { pitchDegrees: -40, yawMode: "FOLLOW_ROUTE_BEARING", stabilization: true }, zoomPercent: 0 },
    version: 1,
  },
];

export const defaultSettings: Record<MissionTemplateType, MissionSettings> = {
  WAYPOINT: {
    altitudeMode: "HOME_RELATIVE",
    altitudeMeters: 25,
    defaultAltitudeMeters: 25,
    speedMps: 4,
    defaultSpeedMps: 4,
    takeoffAltitudeMeters: 20,
    laneSpacingMeters: 25,
    overlapPercent: 30,
    sweepAngleDegrees: 0,
    corridorWidthMeters: 40,
    sampleSpacingMeters: 30,
    terrainSafetyMarginMeters: 10,
    terrainSampleSpacingMeters: 30,
    terrainCorridorWidthMeters: 30,
    terrainMaxClimbRateMps: 2,
    terrainMaxDescentRateMps: 1.5,
    terrainMaxRelativeAltitudeMeters: 500,
    cameraMode: "FORWARD_OBLIQUE",
    gimbalPitchDegrees: -35,
    gimbalYawMode: "FOLLOW_DRONE_HEADING",
    gimbalYawDegrees: 0,
    gimbalStabilization: true,
    zoomPercent: 0,
    detectionClasses: "",
    returnToLaunch: true,
    recordVideo: false,
  },
  AREA_SCAN: {
    altitudeMode: "HOME_RELATIVE",
    altitudeMeters: 35,
    defaultAltitudeMeters: 25,
    speedMps: 4,
    defaultSpeedMps: 4,
    takeoffAltitudeMeters: 20,
    laneSpacingMeters: 25,
    overlapPercent: 30,
    sweepAngleDegrees: 0,
    corridorWidthMeters: 40,
    sampleSpacingMeters: 30,
    terrainSafetyMarginMeters: 10,
    terrainSampleSpacingMeters: 30,
    terrainCorridorWidthMeters: 30,
    terrainMaxClimbRateMps: 2,
    terrainMaxDescentRateMps: 1.5,
    terrainMaxRelativeAltitudeMeters: 500,
    cameraMode: "DOWNWARD_SCAN",
    gimbalPitchDegrees: -90,
    gimbalYawMode: "FOLLOW_LANE_DIRECTION",
    gimbalYawDegrees: 0,
    gimbalStabilization: true,
    zoomPercent: 0,
    detectionClasses: "",
    returnToLaunch: true,
    recordVideo: false,
  },
  ROUTE_SCAN: {
    altitudeMode: "HOME_RELATIVE",
    altitudeMeters: 45,
    defaultAltitudeMeters: 25,
    speedMps: 5,
    defaultSpeedMps: 4,
    takeoffAltitudeMeters: 20,
    laneSpacingMeters: 25,
    overlapPercent: 30,
    sweepAngleDegrees: 0,
    corridorWidthMeters: 40,
    sampleSpacingMeters: 30,
    terrainSafetyMarginMeters: 10,
    terrainSampleSpacingMeters: 30,
    terrainCorridorWidthMeters: 40,
    terrainMaxClimbRateMps: 2,
    terrainMaxDescentRateMps: 1.5,
    terrainMaxRelativeAltitudeMeters: 500,
    cameraMode: "FORWARD_OBLIQUE",
    gimbalPitchDegrees: -40,
    gimbalYawMode: "FOLLOW_ROUTE_BEARING",
    gimbalYawDegrees: 0,
    gimbalStabilization: true,
    zoomPercent: 0,
    detectionClasses: "",
    returnToLaunch: true,
    recordVideo: false,
  },
};

export function newPoint(latitude: number, longitude: number): MissionPoint {
  return {
    id: globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`,
    latitude,
    longitude,
  };
}
