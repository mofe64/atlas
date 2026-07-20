export type EvidenceAssetStatus = "PENDING" | "READY" | "FAILED" | "TRASHED" | "PURGING" | "PURGED";
export type EvidenceAssetType = "STILL" | "EVENT_CLIP";
export type EvidenceReviewState = "UNREVIEWED" | "RELEVANT" | "NOT_RELEVANT" | "NEEDS_FOLLOW_UP";
export type EvidenceRetentionClass = "STANDARD" | "EXTENDED" | "LEGAL_HOLD";

export type EvidenceAssetAnnotation = {
  id: string;
  annotationType: "NOTE" | "TAG";
  body: string;
  createdBy: string;
  createdAtUnixMs: number;
};

export type EvidenceAssetEvent = {
  id: string;
  sequence: number;
  eventType: string;
  actor: string;
  message: string;
  details: Record<string, unknown>;
  occurredAtUnixMs: number;
};

export type EvidenceAsset = {
  id: string;
  assetType: EvidenceAssetType;
  status: EvidenceAssetStatus;
  reviewState: EvidenceReviewState;
  retentionClass: EvidenceRetentionClass;
  sourceId: string;
  droneId: string;
  incidentId?: string;
  missionId?: string;
  missionRunId?: string;
  recordingSessionId?: string;
  selectionId?: string;
  trackSessionId?: string;
  trackId?: string;
  evidenceMarkerAnnotationId?: string;
  capturedAtUnixMs: number;
  sourceStartedAtUnixMs?: number;
  sourceEndedAtUnixMs?: number;
  requestedStartAtUnixMs?: number;
  requestedEndAtUnixMs?: number;
  relativePath: string;
  thumbnailRelativePath: string;
  mimeType: string;
  thumbnailMimeType: string;
  byteLength: number;
  sha256: string;
  thumbnailByteLength: number;
  thumbnailSha256: string;
  createdBy: string;
  createdAtUnixMs: number;
  updatedAtUnixMs: number;
  retainUntilUnixMs?: number;
  trashedAtUnixMs?: number;
  purgeAfterUnixMs?: number;
  deleteReason: string;
  purgedAtUnixMs?: number;
  errorMessage: string;
  annotations: EvidenceAssetAnnotation[];
  events: EvidenceAssetEvent[];
};

export type EvidenceRetentionPolicy = {
  enabled: boolean;
  defaultRetentionDays: number;
  extendedRetentionDays: number;
  trashGraceDays: number;
  updatedBy: string;
  updatedAtUnixMs: number;
};
