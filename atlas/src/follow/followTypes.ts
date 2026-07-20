import type { OperationalTrackGeolocation } from "../operationsTypes";

export type TrackGeolocation = Omit<OperationalTrackGeolocation["geolocation"], "status"> & {
  status: "REQUESTED" | "SUCCEEDED" | "REJECTED";
};

export type AircraftFollowState =
  | "REQUESTED"
  | "VALIDATING"
  | "ACQUIRING"
  | "FOLLOWING"
  | "DEGRADED_HOLD"
  | "ENDED";

export type AircraftFollowTarget = {
  geolocationId: string;
  droneId: string;
  selectionId: string;
  sourceId: string;
  trackSessionId: string;
  trackId: string;
  observedAtUnixMs: number;
  latitude: number;
  longitude: number;
  altitudeAmslM: number;
  velocityNorthMps: number;
  velocityEastMps: number;
  horizontalUncertaintyM: number;
  velocityUncertaintyMps: number;
  trackConfidence: number;
  lifecycleState: string;
  motionStatus: string;
};

export type AircraftFollowEvent = {
  id: string;
  sequence: number;
  eventType: string;
  state: AircraftFollowState;
  source: string;
  operationId: string;
  reasonCode: string;
  message: string;
  evidence: unknown;
  occurredAtUnixMs: number;
};

export type AircraftFollowSession = {
  id: string;
  droneId: string;
  selectionId: string;
  trackSessionId: string;
  trackId: string;
  sourceId: string;
  state: AircraftFollowState;
  requestedBy: string;
  reviewedBy: string;
  operatorReviewNote: string;
  requestedAtUnixMs: number;
  authorizedAtUnixMs?: number | null;
  startedAtUnixMs?: number | null;
  endedAtUnixMs?: number | null;
  standoffM: number;
  altitudeRelativeM: number;
  minimumAltitudeRelativeM: number;
  maximumAltitudeRelativeM: number;
  maximumGroundSpeedMps: number;
  maximumAccelerationMps2: number;
  maximumDurationMs: number;
  boundaryCenterLatitude: number;
  boundaryCenterLongitude: number;
  boundaryRadiusM: number;
  minimumBatteryPercent: number;
  minimumTrackConfidence: number;
  maximumGeolocationUncertaintyM: number;
  maximumVelocityUncertaintyMps: number;
  latestGeolocationId: string;
  latestTargetObservedAtUnixMs: number;
  operatorLeaseExpiresAtUnixMs?: number | null;
  lastAgentUpdateAtUnixMs?: number | null;
  validationReference: string;
  boresightReference: string;
  boresightErrorBoundDeg: number;
  exitReasonCode: string;
  exitReason: string;
  createdAtUnixMs: number;
  updatedAtUnixMs: number;
  target: AircraftFollowTarget;
  events: AircraftFollowEvent[];
};

export type FollowEnvelopeDraft = {
  standoffM: number;
  altitudeRelativeM: number;
  minimumAltitudeRelativeM: number;
  maximumAltitudeRelativeM: number;
  maximumGroundSpeedMps: number;
  maximumAccelerationMps2: number;
  maximumDurationSeconds: number;
  boundaryCenterLatitude: number;
  boundaryCenterLongitude: number;
  boundaryRadiusM: number;
  minimumBatteryPercent: number;
  minimumTrackConfidence: number;
  maximumGeolocationUncertaintyM: number;
  maximumVelocityUncertaintyMps: number;
  operatorReviewNote: string;
};
