export type DroneStatus = "registered" | "online" | "stale" | "offline";
export type TelemetryState = "unknown" | "fresh" | "stale" | "lost" | "conflicted";
export type CommandChannelState = "connected" | "disconnected";
export type CommunicationLinkStatus =
  | "UNKNOWN"
  | "CONNECTED"
  | "DEGRADED"
  | "STALE"
  | "LOST"
  | "DISABLED"
  | "CONFLICTED";
export type CommunicationLinkType = "VEHICLE_AGENT_GRPC" | "GROUND_UNIT_DATA_LINK" | "UNKNOWN";
export type CommunicationLinkRole = "TELEMETRY" | "COMMAND" | "VIDEO" | "GIMBAL_CONTROL";
export type TelemetryFeedSourceType =
  | "AGENT_DIRECT"
  | "LOCAL_GROUND"
  | "EXTERNAL_OBSERVER"
  | "SIMULATOR"
  | "UNKNOWN";
export type TelemetryFeedStatus =
  | "UNKNOWN"
  | "ACTIVE"
  | "DEGRADED"
  | "STALE"
  | "LOST"
  | "ENDED"
  | "CONFLICTED";

export type Telemetry = {
  state: TelemetryState;
  activeTelemetryFeedId?: string;
  sourceCommunicationLinkId?: string;
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
  groundSpeedMPS: number;
  gpsFix: string;
  satellitesVisible: number;
  homePositionSet: boolean;
  source: string;
};

export type CommandChannel = {
  state: CommandChannelState;
  connectedAt?: string;
  lastDisconnectedAt?: string;
};

export type CommunicationSummary = {
  commandLinkStatus: CommunicationLinkStatus;
  activeCommandLinkId?: string;
  activeTelemetryLinkId?: string;
  activeLinkCount: number;
  degradedLinkCount: number;
  lostLinkCount: number;
};

export type CommunicationLink = {
  id: string;
  droneId: string;
  vehicleAgentId?: string;
  droneVehicleAgentConnectionId?: string;
  linkType: CommunicationLinkType;
  roles: CommunicationLinkRole[];
  status: CommunicationLinkStatus;
  transport: string;
  endpointDescription: string;
  commandEligible: boolean;
  latencyMs?: number;
  packetLossEstimate?: number;
  rxBytesPerSec?: number;
  txBytesPerSec?: number;
  lastSeenAt?: string;
  createdAt: string;
  endedAt?: string;
  endedReason?: string;
};

export type TelemetryFieldsAvailable = {
  position: boolean;
  altitude: boolean;
  heading: boolean;
  attitude: boolean;
  velocity: boolean;
  battery: boolean;
  armed: boolean;
  flightMode: boolean;
  gpsHealth: boolean;
  homePosition: boolean;
  missionProgress: boolean;
  systemHealth: boolean;
};

export type TelemetryFeed = {
  id: string;
  droneId: string;
  sourceType: TelemetryFeedSourceType;
  sourceId: string;
  communicationLinkId?: string;
  status: TelemetryFeedStatus;
  priority: number;
  freshness: TelemetryState;
  lastTelemetryAt?: string;
  lastSequence?: number;
  messageRateHz?: number;
  fieldsAvailable: TelemetryFieldsAvailable;
  startedAt: string;
  endedAt?: string;
  lastError?: string;
};

export type CommandType = "arm" | "takeoff" | "return_to_launch" | "land";
export type CommandAction = "arm" | "takeoff" | "return-to-launch" | "land";
export type CommandState =
  | "requested"
  | "authorized"
  | "rejected_by_policy"
  | "sent_to_vehicle_agent"
  | "vehicle_agent_received"
  | "sent_to_vehicle"
  | "vehicle_acked"
  | "vehicle_rejected"
  | "telemetry_confirmed"
  | "acked_but_not_observed"
  | "timed_out"
  | "failed";

export type MissionExecutionState =
  | "unknown"
  | "created"
  | "upload_requested"
  | "uploading"
  | "upload_failed"
  | "uploaded_to_vehicle"
  | "start_requested"
  | "active"
  | "hold"
  | "paused_or_hold"
  | "rtl_requested"
  | "completed"
  | "aborted"
  | "failed";

export type CommandRequest = {
  id: string;
  droneId: string;
  vehicleAgentId: string;
  type: CommandType;
  state: CommandState;
  requestedBy: string;
  requestedAt: string;
  updatedAt: string;
  lastSentAt?: string;
  leaseUntil?: string;
  vehicleAckedAt?: string;
  deliveryAttempt: number;
  idempotencyKey?: string;
  ackCorrelationId?: string;
  rawAckCode?: string;
  policyReason?: string;
  resultMessage?: string;
  telemetryState: TelemetryState;
  vehicleAgentStatus: DroneStatus;
};

export type RawMavlinkCommandAck = {
  type?: string;
  observedAt?: string;
  sourceSystemId?: number;
  sourceComponentId?: number;
  command?: number;
  result?: number;
  progress?: number;
  resultParam2?: number;
  targetSystem?: number;
  targetComponent?: number;
  resultLabel?: string;
  matchStatus?: string;
};

export type CommandEventEvidence = {
  rawMavlinkCommandAck?: RawMavlinkCommandAck;
  [key: string]: unknown;
};

export type CommandEvent = {
  id: string;
  vehicleActionId: string;
  droneId: string;
  vehicleAgentId: string;
  type: string;
  state: CommandState;
  message: string;
  source: string;
  rawAckCode?: string;
  evidence?: CommandEventEvidence;
  telemetrySnapshotId?: string;
  createdAt: string;
};

export type MavlinkObserverComponent = {
  systemId: number;
  componentId: number;
  firstSeenAt?: string;
  lastSeenAt?: string;
  packetCount: number;
};

export type MavlinkObserverDiagnostics = {
  connected?: boolean;
  packetsSeen?: number;
  lastPacketAt?: string;
  lastHeartbeatAt?: string;
  lastCommandAckAt?: string;
  lastCommandAckCommand?: number;
  lastCommandAckResult?: number;
  componentCount?: number;
  components?: MavlinkObserverComponent[];
};

export type BackendChannelHealth = {
  state?: "connecting" | "connected" | "disconnected" | string;
  reconnectCount?: number;
  connectedAt?: string;
  lastDisconnectedAt?: string;
  lastSuccessfulSendAt?: string;
  lastHeartbeatSentAt?: string;
  lastError?: string;
  backendAddress?: string;
  weakLink?: boolean;
  weakLinkReason?: string;
};

export type MissionExecution = {
  id: string;
  missionId: string;
  missionVersionId: string;
  droneId: string;
  vehicleAgentId: string;
  requestedBy: string;
  uploadRequestedBy?: string;
  startRequestedBy?: string;
  state: MissionExecutionState;
  createdAt: string;
  updatedAt: string;
  lastSentAt?: string;
  leaseUntil?: string;
  uploadRequestedAt?: string;
  uploadedAt?: string;
  startRequestedAt?: string;
  startedAt?: string;
  completedAt?: string;
  holdAt?: string;
  failedAt?: string;
  currentMissionItem?: number;
  totalMissionItems?: number;
  progressUpdatedAt?: string;
  deliveryAttempt: number;
  resultMessage?: string;
};

export type MissionExecutionEvent = {
  id: string;
  executionId: string;
  missionId: string;
  missionVersionId?: string;
  droneId: string;
  vehicleAgentId: string;
  type: string;
  state: MissionExecutionState;
  message: string;
  currentMissionItem?: number;
  totalMissionItems?: number;
  source: string;
  createdAt: string;
};

export type MissionCompletionAction = "hold" | "return_to_launch" | "land";
export type MissionValidationStatus = "not_validated" | "validated" | "rejected";

export type MissionWaypoint = {
  sequence: number;
  latitude: number;
  longitude: number;
  relativeAltitudeM: number;
  speedMPS?: number;
  loiterTimeS?: number;
};

export type MissionValidationError = {
  field: string;
  message: string;
};

export type Mission = {
  id: string;
  droneId: string;
  currentVersionId?: string;
  name: string;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  completionAction: MissionCompletionAction;
  validationStatus: MissionValidationStatus;
  validationErrors?: MissionValidationError[];
  waypoints: MissionWaypoint[];
};

export type MissionDetail = {
  mission: Mission;
  executions: MissionExecution[];
};

export type MissionStreamEventType = "mission_snapshot" | "mission_updated";

export type MissionStreamEvent = {
  type: MissionStreamEventType;
  detail: MissionDetail;
};

export type CreateMissionWaypointInput = {
  latitude: number;
  longitude: number;
  relativeAltitudeM: number;
  speedMPS?: number;
  loiterTimeS?: number;
};

export type CreateMissionInput = {
  name: string;
  completionAction: MissionCompletionAction;
  waypoints: CreateMissionWaypointInput[];
};

export type Drone = {
  id: string;
  name: string;
  vehicleAgentId: string;
  status: DroneStatus;
  lastSeenAt: string;
  lastHeartbeatAt?: string;
  telemetry?: Telemetry;
  commandChannel: CommandChannel;
  communication: CommunicationSummary;
  mavlinkObserver?: MavlinkObserverDiagnostics;
  backendChannel?: BackendChannelHealth;
  vehicleActions?: CommandRequest[];
  commands?: CommandRequest[];
  missionExecution?: MissionExecution;
};

export type GimbalControlInput = {
  pitchRateDegS: number;
  yawRateDegS: number;
};

export async function fetchDrones(signal?: AbortSignal): Promise<Drone[]> {
  const response = await fetch("/api/drones", { signal });

  if (!response.ok) {
    throw new Error(`Backend returned ${response.status}`);
  }

  return response.json() as Promise<Drone[]>;
}

export async function sendGimbalControl(
  droneId: string,
  input: GimbalControlInput,
): Promise<void> {
  const response = await fetch(`/api/drones/${encodeURIComponent(droneId)}/gimbal/control`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });

  if (!response.ok) {
    throw new Error(await errorMessage(response));
  }
}

export async function requestDroneCommand(
  droneId: string,
  action: CommandAction,
): Promise<CommandRequest> {
  const response = await fetch(
    `/api/drones/${encodeURIComponent(droneId)}/actions/${action}`,
    {
      method: "POST",
      headers: {
        "X-Atlas-Operator-ID": "atlas-ui-development",
        "Idempotency-Key": createIdempotencyKey(droneId, action),
      },
    },
  );

  const body = (await response.json()) as CommandRequest | { error?: string };

  if (!response.ok) {
    if ("state" in body) {
      return body;
    }

    throw new Error("error" in body && body.error ? body.error : `Backend returned ${response.status}`);
  }

  return body as CommandRequest;
}

function createIdempotencyKey(droneId: string, action: CommandAction) {
  const random =
    globalThis.crypto?.randomUUID?.() ??
    `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  return `atlas-ui:${droneId}:${action}:${random}`;
}

export async function fetchDroneCommunicationLinks(droneId: string): Promise<CommunicationLink[]> {
  const response = await fetch(`/api/drones/${encodeURIComponent(droneId)}/communication-links`);

  if (!response.ok) {
    throw new Error(await errorMessage(response));
  }

  return response.json() as Promise<CommunicationLink[]>;
}

export async function fetchDroneTelemetryFeeds(droneId: string): Promise<TelemetryFeed[]> {
  const response = await fetch(`/api/drones/${encodeURIComponent(droneId)}/telemetry-feeds`);

  if (!response.ok) {
    throw new Error(await errorMessage(response));
  }

  return response.json() as Promise<TelemetryFeed[]>;
}

export async function fetchCommandEvents(
  droneId: string,
  commandId: string,
  signal?: AbortSignal,
): Promise<CommandEvent[]> {
  const response = await fetch(
    `/api/drones/${encodeURIComponent(droneId)}/actions/${encodeURIComponent(commandId)}/events`,
    { signal },
  );

  if (!response.ok) {
    throw new Error(await errorMessage(response));
  }

  return response.json() as Promise<CommandEvent[]>;
}

export async function fetchDroneMissions(droneId: string): Promise<Mission[]> {
  const response = await fetch(`/api/drones/${encodeURIComponent(droneId)}/missions`);

  if (!response.ok) {
    throw new Error(await errorMessage(response));
  }

  return response.json() as Promise<Mission[]>;
}

export async function fetchMission(missionId: string): Promise<MissionDetail> {
  const response = await fetch(`/api/missions/${encodeURIComponent(missionId)}`);

  if (!response.ok) {
    throw new Error(await errorMessage(response));
  }

  return response.json() as Promise<MissionDetail>;
}

export async function fetchMissionExecutions(missionId: string): Promise<MissionExecution[]> {
  const response = await fetch(`/api/missions/${encodeURIComponent(missionId)}/executions`);

  if (!response.ok) {
    throw new Error(await errorMessage(response));
  }

  return response.json() as Promise<MissionExecution[]>;
}

export async function fetchMissionExecutionEvents(
  missionId: string,
): Promise<MissionExecutionEvent[]> {
  const response = await fetch(`/api/missions/${encodeURIComponent(missionId)}/events`);

  if (!response.ok) {
    throw new Error(await errorMessage(response));
  }

  return response.json() as Promise<MissionExecutionEvent[]>;
}

export async function createDroneMission(
  droneId: string,
  input: CreateMissionInput,
): Promise<Mission> {
  const response = await fetch(`/api/drones/${encodeURIComponent(droneId)}/missions`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Atlas-Operator-ID": "atlas-ui-development",
    },
    body: JSON.stringify(input),
  });

  const body = (await response.json()) as Mission | { error?: string };

  if (!response.ok && !("validationStatus" in body)) {
    throw new Error("error" in body && body.error ? body.error : `Backend returned ${response.status}`);
  }

  return body as Mission;
}

export async function requestMissionUpload(missionId: string): Promise<MissionExecution> {
  return requestMissionAction(missionId, "upload");
}

export async function requestMissionStart(missionId: string): Promise<MissionExecution> {
  return requestMissionAction(missionId, "start");
}

export async function requestMissionAbort(missionId: string): Promise<MissionExecution> {
  return requestMissionAction(missionId, "abort");
}

export type DroneStreamHandlers = {
  onDrones: (drones: Drone[]) => void;
  onOpen?: () => void;
  onClose?: () => void;
  onError?: (message: string) => void;
};

export type MissionStreamHandlers = {
  onMission: (detail: MissionDetail) => void;
  onOpen?: () => void;
  onClose?: () => void;
  onError?: (message: string) => void;
};

export function subscribeDrones(handlers: DroneStreamHandlers): () => void {
  const socket = new WebSocket(droneStreamURL());

  socket.onopen = () => {
    handlers.onOpen?.();
  };

  socket.onmessage = (event) => {
    try {
      handlers.onDrones(JSON.parse(event.data) as Drone[]);
    } catch {
      handlers.onError?.("Received an invalid fleet stream message");
    }
  };

  socket.onerror = () => {
    handlers.onError?.("Fleet stream connection failed");
  };

  socket.onclose = () => {
    handlers.onClose?.();
  };

  return () => {
    socket.close();
  };
}

export function subscribeMission(missionId: string, handlers: MissionStreamHandlers): () => void {
  const socket = new WebSocket(missionStreamURL(missionId));

  socket.onopen = () => {
    handlers.onOpen?.();
  };

  socket.onmessage = (event) => {
    try {
      handlers.onMission(missionDetailFromStreamMessage(JSON.parse(event.data)));
    } catch {
      handlers.onError?.("Received an invalid mission stream message");
    }
  };

  socket.onerror = () => {
    handlers.onError?.("Mission stream connection failed");
  };

  socket.onclose = () => {
    handlers.onClose?.();
  };

  return () => {
    socket.close();
  };
}

function droneStreamURL(): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}/api/drones/stream`;
}

function missionStreamURL(missionId: string): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}/api/missions/${encodeURIComponent(missionId)}/stream`;
}

function missionDetailFromStreamMessage(message: MissionDetail | MissionStreamEvent): MissionDetail {
  if ("detail" in message) {
    return message.detail;
  }

  return message;
}

async function requestMissionAction(
  missionId: string,
  action: "upload" | "start" | "abort",
): Promise<MissionExecution> {
  const response = await fetch(`/api/missions/${encodeURIComponent(missionId)}/${action}`, {
    method: "POST",
    headers: {
      "X-Atlas-Operator-ID": "atlas-ui-development",
    },
  });

  const body = (await response.json()) as MissionExecution | { error?: string };

  if (!response.ok) {
    throw new Error("error" in body && body.error ? body.error : `Backend returned ${response.status}`);
  }

  return body as MissionExecution;
}

async function errorMessage(response: Response) {
  try {
    const body = (await response.json()) as { error?: string };
    return body.error ?? `Backend returned ${response.status}`;
  } catch {
    return `Backend returned ${response.status}`;
  }
}
