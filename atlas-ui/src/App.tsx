import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type {
  GeoJSONSource,
  GeoJSONSourceSpecification,
  Map as MapLibreMap,
  Marker,
  StyleSpecification,
} from "maplibre-gl";
import {
  Link,
  Navigate,
  NavLink,
  Route as RouterRoute,
  Routes,
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import {
  Activity,
  CircleArrowDown,
  History,
  ListChecks,
  Loader2,
  LocateFixed,
  Map as MapIcon,
  MapPin,
  PlaneTakeoff,
  Play,
  Plus,
  Power,
  RadioTower,
  RefreshCw,
  Route,
  RotateCcw,
  ShieldCheck,
  Trash2,
  UploadCloud,
} from "lucide-react";
import {
  type CommandAction,
  type CommandState,
  type CreateMissionInput,
  type CreateMissionWaypointInput,
  type Drone,
  type Mission,
  type MissionCompletionAction,
  type MissionDetail,
  type MissionExecution,
  type MissionExecutionState,
  type CommandRequest,
  createDroneMission,
  fetchDroneMissions,
  fetchDrones,
  fetchMission,
  requestMissionAbort,
  requestDroneCommand,
  requestMissionStart,
  requestMissionUpload,
  subscribeDrones,
  subscribeMission,
} from "./api/atlasApi";

const flowItems = [
  { label: "Atlas UI", detail: "Operator workflow", icon: MapIcon },
  { label: "Atlas Backend", detail: "Policy and audit", icon: ShieldCheck },
  { label: "Atlas Agent", detail: "Onboard bridge", icon: RadioTower },
  { label: "PX4", detail: "Flight authority", icon: Activity },
];

const statusStyles = {
  registered: "bg-atlas-sky/20 text-atlas-ink",
  online: "bg-atlas-field/25 text-atlas-ink",
  stale: "bg-atlas-signal/20 text-atlas-ink",
  offline: "bg-atlas-ink/10 text-atlas-ink/70",
};

const statusDescriptions = {
  registered: "Registered, waiting for first heartbeat",
  online: "Heartbeat is fresh",
  stale: "Heartbeat is delayed",
  offline: "Heartbeat is too old",
};

const telemetryStyles = {
  unknown: "bg-atlas-ink/10 text-atlas-ink/70",
  fresh: "bg-atlas-field/25 text-atlas-ink",
  stale: "bg-atlas-signal/20 text-atlas-ink",
  lost: "bg-atlas-ink/10 text-atlas-ink/70",
};

const commandChannelStyles = {
  connected: "bg-atlas-field/25 text-atlas-ink",
  disconnected: "bg-atlas-ink/10 text-atlas-ink/70",
};

const commandStateStyles: Record<CommandState, string> = {
  requested: "bg-atlas-ink/10 text-atlas-ink/70",
  authorized: "bg-atlas-sky/20 text-atlas-ink",
  rejected_by_policy: "bg-atlas-signal/20 text-atlas-ink",
  sent_to_vehicle_agent: "bg-atlas-sky/20 text-atlas-ink",
  vehicle_agent_received: "bg-atlas-sky/20 text-atlas-ink",
  sent_to_vehicle: "bg-atlas-sky/20 text-atlas-ink",
  vehicle_acked: "bg-atlas-field/25 text-atlas-ink",
  vehicle_rejected: "bg-atlas-signal/20 text-atlas-ink",
  telemetry_confirmed: "bg-atlas-field/25 text-atlas-ink",
  timed_out: "bg-atlas-signal/20 text-atlas-ink",
  failed: "bg-atlas-signal/20 text-atlas-ink",
};

const missionStateStyles: Record<MissionExecutionState, string> = {
  unknown: "bg-atlas-ink/10 text-atlas-ink/70",
  created: "bg-atlas-ink/10 text-atlas-ink/70",
  upload_requested: "bg-atlas-sky/20 text-atlas-ink",
  uploading: "bg-atlas-sky/20 text-atlas-ink",
  upload_failed: "bg-atlas-signal/20 text-atlas-ink",
  uploaded_to_vehicle: "bg-atlas-field/20 text-atlas-ink",
  start_requested: "bg-atlas-sky/20 text-atlas-ink",
  active: "bg-atlas-sky/20 text-atlas-ink",
  hold: "bg-atlas-field/25 text-atlas-ink",
  paused_or_hold: "bg-atlas-field/25 text-atlas-ink",
  rtl_requested: "bg-atlas-sky/20 text-atlas-ink",
  completed: "bg-atlas-field/25 text-atlas-ink",
  aborted: "bg-atlas-signal/20 text-atlas-ink",
  failed: "bg-atlas-signal/20 text-atlas-ink",
};

const commandActions: Array<{
  action: CommandAction;
  label: string;
  Icon: typeof Power;
}> = [
  { action: "arm", label: "Arm", Icon: Power },
  { action: "takeoff", label: "Takeoff", Icon: PlaneTakeoff },
  { action: "return-to-launch", label: "RTL", Icon: RotateCcw },
  { action: "land", label: "Land", Icon: CircleArrowDown },
];

const lifecycleSteps: Array<{ state: CommandState; label: string; shortLabel: string }> = [
  { state: "authorized", label: "Authorized", shortLabel: "Auth" },
  { state: "sent_to_vehicle_agent", label: "Sent to vehicle agent", shortLabel: "Sent" },
  { state: "vehicle_agent_received", label: "Vehicle agent received", shortLabel: "V-Agent" },
  { state: "sent_to_vehicle", label: "Sent to vehicle", shortLabel: "PX4" },
  { state: "vehicle_acked", label: "Vehicle acknowledged", shortLabel: "Ack" },
  { state: "telemetry_confirmed", label: "Telemetry confirmed", shortLabel: "Confirm" },
];

const missionLifecycleSteps: Array<{
  state: MissionExecutionState;
  label: string;
  shortLabel: string;
}> = [
  { state: "upload_requested", label: "Upload requested", shortLabel: "Upload" },
  { state: "uploaded_to_vehicle", label: "Uploaded to vehicle", shortLabel: "Vehicle" },
  { state: "start_requested", label: "Start requested", shortLabel: "Start" },
  { state: "active", label: "Mission active", shortLabel: "Active" },
  { state: "completed", label: "Mission completed", shortLabel: "Done" },
  { state: "hold", label: "Holding", shortLabel: "Hold" },
];

type AtlasView = "fleet" | "missions";

type MissionDraftWaypoint = {
  latitude: string;
  longitude: string;
  relativeAltitudeM: string;
  speedMPS: string;
  loiterTimeS: string;
};

type MissionDraft = {
  name: string;
  completionAction: MissionCompletionAction;
  waypoints: MissionDraftWaypoint[];
};

type MappedMissionWaypoint = {
  index: number;
  latitude: number;
  longitude: number;
};

type MapPosition = {
  latitude: number;
  longitude: number;
  accuracyM?: number;
};

type MapLibreModule = typeof import("maplibre-gl");

const defaultMissionDraft: MissionDraft = {
  name: "Training mission",
  completionAction: "hold",
  waypoints: [],
};

const atlasMapStyle: StyleSpecification = {
  version: 8,
  sources: {
    openStreetMap: {
      type: "raster",
      tiles: ["https://tile.openstreetmap.org/{z}/{x}/{y}.png"],
      tileSize: 256,
      attribution: "OpenStreetMap contributors",
    },
  },
  layers: [
    {
      id: "open-street-map",
      type: "raster",
      source: "openStreetMap",
    },
  ],
};
const fallbackMissionCenter: [number, number] = [-0.1278, 51.5074];

export function App() {
  const [drones, setDrones] = useState<Drone[]>([]);
  const [commandsByDrone, setCommandsByDrone] = useState<Record<string, CommandRequest[]>>({});
  const [commandErrors, setCommandErrors] = useState<Record<string, string | null>>({});
  const [pendingCommands, setPendingCommands] = useState<Record<string, boolean>>({});
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [streamConnected, setStreamConnected] = useState(false);
  const location = useLocation();

  const applyDroneSnapshot = useCallback((nextDrones: Drone[]) => {
    setDrones(nextDrones);
    setCommandsByDrone((current) => {
      const next = { ...current };
      for (const drone of nextDrones) {
        next[drone.id] = mergeCommands(drone.commands ?? [], current[drone.id] ?? []);
      }
      return next;
    });
  }, []);

  useEffect(() => {
    let active = true;
    let streamOpen = false;
    let unsubscribeStream: (() => void) | null = null;
    let reconnectTimer: number | undefined;

    async function loadDrones() {
      try {
        const nextDrones = await fetchDrones();
        if (!active) {
          return;
        }

        applyDroneSnapshot(nextDrones);
        setError(null);
      } catch (err) {
        if (!active) {
          return;
        }

        setError(err instanceof Error ? err.message : "Failed to load drones");
      } finally {
        if (active) {
          setLoading(false);
        }
      }
    }

    function connectStream() {
      unsubscribeStream?.();
      unsubscribeStream = subscribeDrones({
        onOpen: () => {
          if (!active) {
            return;
          }

          streamOpen = true;
          setStreamConnected(true);
          setError(null);
        },
        onDrones: (nextDrones) => {
          if (!active) {
            return;
          }

          applyDroneSnapshot(nextDrones);
          setError(null);
          setLoading(false);
        },
        onError: (message) => {
          if (!active) {
            return;
          }

          setError(message);
        },
        onClose: () => {
          if (!active) {
            return;
          }

          streamOpen = false;
          setStreamConnected(false);
          reconnectTimer = window.setTimeout(connectStream, 2000);
        },
      });
    }

    void loadDrones();
    connectStream();

    const fallbackInterval = window.setInterval(() => {
      if (!streamOpen) {
        void loadDrones();
      }
    }, 5000);

    return () => {
      active = false;
      window.clearInterval(fallbackInterval);
      window.clearTimeout(reconnectTimer);
      unsubscribeStream?.();
    };
  }, [applyDroneSnapshot]);

  async function handleCommand(drone: Drone, action: CommandAction) {
    const pendingKey = `${drone.id}:${action}`;
    setPendingCommands((current) => ({ ...current, [pendingKey]: true }));
    setCommandErrors((current) => ({ ...current, [drone.id]: null }));

    try {
      const command = await requestDroneCommand(drone.id, action);
      setCommandsByDrone((current) => ({
        ...current,
        [drone.id]: mergeCommand(current[drone.id] ?? [], command),
      }));
    } catch (err) {
      setCommandErrors((current) => ({
        ...current,
        [drone.id]: err instanceof Error ? err.message : "Command request failed",
      }));
    } finally {
      setPendingCommands((current) => ({ ...current, [pendingKey]: false }));
    }
  }

  const onlineCount = useMemo(
    () => drones.filter((drone) => drone.status === "online").length,
    [drones],
  );
  const connectionLabel = error
    ? "backend unavailable"
    : `${onlineCount}/${drones.length} online`;
  const streamLabel = streamConnected ? "live stream" : "fallback polling";
  const activeView: AtlasView = isMissionPath(location.pathname) ? "missions" : "fleet";

  return (
    <main className="min-h-screen bg-atlas-mist text-atlas-ink">
      <section
        className={`mx-auto flex min-h-screen w-full flex-col px-6 py-6 sm:px-8 lg:px-10 ${
          activeView === "missions" ? "max-w-none" : "max-w-7xl"
        }`}
      >
        <header className="flex flex-wrap items-center justify-between gap-4 border-b border-atlas-ink/15 pb-5">
          <div>
            <p className="text-sm font-semibold uppercase tracking-[0.18em] text-atlas-signal">
              Atlas Operations
            </p>
            <h1 className="mt-2 text-3xl font-semibold sm:text-4xl">Fleet control starts here</h1>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <div className="flex items-center gap-1 border border-atlas-ink/10 bg-atlas-panel p-1">
              <NavLink
                to="/"
                end
                className={`inline-flex min-h-9 items-center gap-2 px-3 text-sm font-semibold transition ${
                  activeView === "fleet" ? "bg-atlas-ink text-atlas-panel" : "text-atlas-ink/70"
                }`}
              >
                <RadioTower aria-hidden="true" size={15} />
                Fleet
              </NavLink>
              <NavLink
                to="/missions"
                className={`inline-flex min-h-9 items-center gap-2 px-3 text-sm font-semibold transition ${
                  activeView === "missions" ? "bg-atlas-ink text-atlas-panel" : "text-atlas-ink/70"
                }`}
              >
                <Route aria-hidden="true" size={15} />
                Missions
              </NavLink>
            </div>
            <div className="hidden items-center gap-2 rounded-full bg-atlas-panel px-4 py-2 text-sm font-medium shadow-sm shadow-atlas-ink/5 sm:flex">
              <span
                className={`h-2.5 w-2.5 rounded-full ${error ? "bg-atlas-signal" : "bg-atlas-field"}`}
              />
              {connectionLabel}
            </div>
          </div>
        </header>

        <Routes>
          <RouterRoute
            path="/"
            element={
              <FleetWorkspace
                drones={drones}
                commandsByDrone={commandsByDrone}
                commandErrors={commandErrors}
                pendingCommands={pendingCommands}
                loading={loading}
                error={error}
                streamLabel={streamLabel}
                onCommand={(drone, action) => void handleCommand(drone, action)}
              />
            }
          />
          <RouterRoute path="/missions" element={<MissionIndexRoute drones={drones} />} />
          <RouterRoute path="/missions/new" element={<MissionRoute drones={drones} />} />
          <RouterRoute path="/missions/:missionId" element={<MissionRoute drones={drones} />} />
          <RouterRoute
            path="/drones/:droneId/missions/new"
            element={<MissionRoute drones={drones} />}
          />
          <RouterRoute
            path="/drones/:droneId/missions"
            element={<DroneMissionManagementRoute drones={drones} />}
          />
          <RouterRoute path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </section>
    </main>
  );
}

function FleetWorkspace({
  drones,
  commandsByDrone,
  commandErrors,
  pendingCommands,
  loading,
  error,
  streamLabel,
  onCommand,
}: {
  drones: Drone[];
  commandsByDrone: Record<string, CommandRequest[]>;
  commandErrors: Record<string, string | null>;
  pendingCommands: Record<string, boolean>;
  loading: boolean;
  error: string | null;
  streamLabel: string;
  onCommand: (drone: Drone, action: CommandAction) => void;
}) {
  return (
    <div className="grid flex-1 gap-8 py-10 lg:grid-cols-[1fr_0.9fr]">
      <section className="space-y-8">
        <div>
          <p className="max-w-2xl text-lg leading-8 text-atlas-ink/75">
            Atlas is proving the PX4 SITL control loop: vehicle state streams through the agent
            while approved commands move back down to PX4.
          </p>
        </div>

        <section className="bg-atlas-panel p-5 shadow-sm shadow-atlas-ink/5">
          <div className="flex items-center justify-between border-b border-atlas-ink/10 pb-4">
            <h2 className="text-xl font-semibold">Fleet</h2>
            <div className="flex items-center gap-2 text-sm text-atlas-ink/60">
              <RefreshCw aria-hidden="true" size={16} />
              {streamLabel}
            </div>
          </div>

          <div className="mt-5">
            {loading && <p className="text-sm text-atlas-ink/65">Loading fleet state...</p>}

            {error && (
              <p className="border-l-4 border-atlas-signal bg-atlas-signal/10 px-4 py-3 text-sm">
                Live stream unavailable. Showing the last known fleet state when available. {error}
              </p>
            )}

            {!loading && drones.length === 0 && !error && (
              <p className="text-sm text-atlas-ink/65">
                No agents have registered yet. Start `atlas-agent` to bring a drone online.
              </p>
            )}

            <div className="space-y-3">
              {drones.map((drone) => (
                <article key={drone.id} className="grid gap-4 border border-atlas-ink/10 p-4">
                  <div className="grid gap-4 sm:grid-cols-[1fr_auto]">
                    <div>
                      <div className="flex flex-wrap items-center gap-3">
                        <h3 className="text-lg font-semibold">{drone.name}</h3>
                        <span
                          className={`rounded-full px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] ${
                            statusStyles[drone.status]
                          }`}
                        >
                          {drone.status}
                        </span>
                        <span
                          className={`rounded-full px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] ${
                            telemetryStyles[drone.telemetry?.state ?? "unknown"]
                          }`}
                        >
                          telemetry {drone.telemetry?.state ?? "unknown"}
                        </span>
                        <span
                          className={`rounded-full px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] ${
                            commandChannelStyles[drone.commandChannel.state]
                          }`}
                        >
                          commands {drone.commandChannel.state}
                        </span>
                      </div>
                      <p className="mt-2 text-sm text-atlas-ink/65">
                        {drone.id} through {drone.vehicleAgentId}
                      </p>
                      <p className="mt-1 text-sm text-atlas-ink/65">
                        {statusDescriptions[drone.status]}
                      </p>
                      <div className="mt-4 flex flex-wrap gap-2">
                        <Link
                          to={`/drones/${encodeURIComponent(drone.id)}/missions`}
                          className="inline-flex min-h-9 items-center gap-2 border border-atlas-ink/15 bg-atlas-panel px-3 text-sm font-semibold transition hover:border-atlas-ink/35 hover:bg-atlas-mist"
                        >
                          <Route aria-hidden="true" size={15} />
                          Missions
                        </Link>
                        <Link
                          to={`/drones/${encodeURIComponent(drone.id)}/missions/new`}
                          className="inline-flex min-h-9 items-center gap-2 border border-atlas-ink/15 bg-atlas-mist px-3 text-sm font-semibold transition hover:border-atlas-ink/35 hover:bg-atlas-panel"
                        >
                          <Plus aria-hidden="true" size={15} />
                          Plan route
                        </Link>
                      </div>
                    </div>
                    <div className="text-left text-sm text-atlas-ink/65 sm:text-right">
                      <p>Last seen {formatTime(drone.lastSeenAt)}</p>
                      <p>Heartbeat {formatTime(drone.lastHeartbeatAt)}</p>
                      <p>Telemetry {formatTime(drone.telemetry?.receivedAt)}</p>
                      <p>Command channel {formatCommandChannelTime(drone)}</p>
                    </div>
                  </div>

                  <TelemetryGrid drone={drone} />
                  <MissionPanel drone={drone} />
                  <CommandPanel
                    drone={drone}
                    commands={commandsByDrone[drone.id] ?? []}
                    error={commandErrors[drone.id]}
                    pendingCommands={pendingCommands}
                    onCommand={(action) => onCommand(drone, action)}
                  />
                </article>
              ))}
            </div>
          </div>
        </section>
      </section>

      <section className="bg-atlas-panel p-5 shadow-sm shadow-atlas-ink/5">
        <div className="flex items-center justify-between border-b border-atlas-ink/10 pb-4">
          <h2 className="text-xl font-semibold">System boundary</h2>
          <span className="rounded-full bg-atlas-mist px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/60">
            Phase 2
          </span>
        </div>

        <div className="mt-5 space-y-3">
          {flowItems.map((item, index) => {
            const Icon = item.icon;
            return (
              <div key={item.label} className="flex items-center gap-4">
                <div className="flex h-12 w-12 shrink-0 items-center justify-center bg-atlas-ink text-atlas-panel">
                  <Icon aria-hidden="true" size={22} strokeWidth={1.8} />
                </div>
                <div className="min-w-0 flex-1">
                  <p className="font-semibold">{item.label}</p>
                  <p className="text-sm text-atlas-ink/65">{item.detail}</p>
                </div>
                {index < flowItems.length - 1 && (
                  <span className="hidden text-sm font-semibold text-atlas-signal sm:inline">
                    connects to
                  </span>
                )}
              </div>
            );
          })}
        </div>
      </section>
    </div>
  );
}

function MissionIndexRoute({ drones }: { drones: Drone[] }) {
  return (
    <div className="flex-1 py-8">
      <section className="bg-atlas-panel p-5 shadow-sm shadow-atlas-ink/5">
        <div className="flex flex-wrap items-start justify-between gap-4 border-b border-atlas-ink/10 pb-4">
          <div>
            <h2 className="text-xl font-semibold">Mission operations</h2>
            <p className="mt-1 max-w-2xl text-sm text-atlas-ink/65">
              Select a drone to review active mission execution, mission definitions, and past
              attempts.
            </p>
          </div>
          <span className="rounded-full bg-atlas-mist px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/60">
            Drone scoped
          </span>
        </div>

        <div className="mt-5 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {drones.length === 0 && (
            <p className="text-sm text-atlas-ink/65">
              No registered drones yet. Start an agent before managing missions.
            </p>
          )}
          {drones.map((drone) => (
            <Link
              key={drone.id}
              to={`/drones/${encodeURIComponent(drone.id)}/missions`}
              className="grid gap-3 border border-atlas-ink/10 p-4 transition hover:border-atlas-ink/30 hover:bg-atlas-mist/70"
            >
              <div className="flex items-center justify-between gap-3">
                <h3 className="truncate text-lg font-semibold">{drone.name}</h3>
                <span
                  className={`shrink-0 rounded-full px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] ${
                    statusStyles[drone.status]
                  }`}
                >
                  {drone.status}
                </span>
              </div>
              <p className="truncate text-sm text-atlas-ink/60">{drone.id}</p>
              <MissionExecutionSummary execution={drone.missionExecution} />
            </Link>
          ))}
        </div>
      </section>
    </div>
  );
}

function DroneMissionManagementRoute({ drones }: { drones: Drone[] }) {
  const { droneId } = useParams();

  if (!droneId) {
    return <Navigate to="/missions" replace />;
  }

  return <DroneMissionManagement drones={drones} droneId={droneId} />;
}

function DroneMissionManagement({ drones, droneId }: { drones: Drone[]; droneId: string }) {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const selectedMissionParam = searchParams.get("mission");
  const [missions, setMissions] = useState<Mission[]>([]);
  const [selectedMissionId, setSelectedMissionId] = useState("");
  const [missionDetail, setMissionDetail] = useState<MissionDetail | null>(null);
  const [loadingMissions, setLoadingMissions] = useState(false);
  const [missionError, setMissionError] = useState<string | null>(null);
  const [pendingAction, setPendingAction] = useState<string | null>(null);
  const [missionStreamConnected, setMissionStreamConnected] = useState(false);

  const drone = drones.find((item) => item.id === droneId);
  const activeMissionId = drone?.missionExecution?.missionId;

  const loadMissions = useCallback(async () => {
    setLoadingMissions(true);
    try {
      const nextMissions = await fetchDroneMissions(droneId);
      setMissions(nextMissions);
      setMissionError(null);
      setSelectedMissionId((current) => {
        if (current && nextMissions.some((mission) => mission.id === current)) {
          return current;
        }

        const preferredMissionId =
          selectedMissionParam && nextMissions.some((mission) => mission.id === selectedMissionParam)
            ? selectedMissionParam
            : activeMissionId && nextMissions.some((mission) => mission.id === activeMissionId)
              ? activeMissionId
              : nextMissions[0]?.id;

        return preferredMissionId ?? "";
      });
    } catch (err) {
      setMissionError(err instanceof Error ? err.message : "Failed to load missions");
    } finally {
      setLoadingMissions(false);
    }
  }, [activeMissionId, droneId, selectedMissionParam]);

  useEffect(() => {
    void loadMissions();
  }, [loadMissions]);

  useEffect(() => {
    if (!selectedMissionParam) {
      return;
    }

    setSelectedMissionId(selectedMissionParam);
  }, [selectedMissionParam]);

  useEffect(() => {
    if (!selectedMissionId) {
      setMissionDetail(null);
      setMissionStreamConnected(false);
      return;
    }

    let active = true;
    let unsubscribeStream: (() => void) | null = null;

    async function loadMissionDetail() {
      try {
        const detail = await fetchMission(selectedMissionId);
        if (!active) {
          return;
        }

        setMissionDetail(detail);
        setMissionError(null);
      } catch (err) {
        if (!active) {
          return;
        }

        setMissionError(err instanceof Error ? err.message : "Failed to load mission detail");
      }
    }

    void loadMissionDetail();
    unsubscribeStream = subscribeMission(selectedMissionId, {
      onOpen: () => {
        if (active) {
          setMissionStreamConnected(true);
        }
      },
      onMission: (detail) => {
        if (!active) {
          return;
        }

        setMissionDetail(detail);
        setMissionError(null);
      },
      onError: (message) => {
        if (active) {
          setMissionError(message);
        }
      },
      onClose: () => {
        if (active) {
          setMissionStreamConnected(false);
        }
      },
    });

    return () => {
      active = false;
      unsubscribeStream?.();
    };
  }, [selectedMissionId]);

  async function handleMissionAction(action: "upload" | "start" | "abort") {
    if (!selectedMissionId) {
      return;
    }

    setPendingAction(action);
    setMissionError(null);
    try {
      const execution =
        action === "upload"
          ? await requestMissionUpload(selectedMissionId)
          : action === "start"
            ? await requestMissionStart(selectedMissionId)
            : await requestMissionAbort(selectedMissionId);
      setMissionDetail((current) =>
        current
          ? {
              ...current,
              executions: mergeMissionExecutions([execution], current.executions),
            }
          : current,
      );
    } catch (err) {
      setMissionError(err instanceof Error ? err.message : `Mission ${action} failed`);
    } finally {
      setPendingAction(null);
    }
  }

  function selectMission(missionId: string) {
    setSelectedMissionId(missionId);
    navigate(`/drones/${encodeURIComponent(droneId)}/missions?mission=${encodeURIComponent(missionId)}`);
  }

  return (
    <div className="grid flex-1 gap-5 py-6 xl:grid-cols-[24rem_minmax(0,1fr)]">
      <aside className="min-w-0 border border-atlas-ink/10 bg-atlas-panel shadow-sm shadow-atlas-ink/5">
        <div className="border-b border-atlas-ink/10 p-5">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <p className="text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/45">
                Drone missions
              </p>
              <h2 className="mt-1 truncate text-xl font-semibold">{drone?.name ?? droneId}</h2>
              <p className="mt-1 truncate text-sm text-atlas-ink/60">{drone?.vehicleAgentId ?? droneId}</p>
            </div>
            {drone && (
              <span
                className={`shrink-0 rounded-full px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] ${
                  statusStyles[drone.status]
                }`}
              >
                {drone.status}
              </span>
            )}
          </div>

          <Link
            to={`/drones/${encodeURIComponent(droneId)}/missions/new`}
            className="mt-5 inline-flex min-h-10 w-full items-center justify-center gap-2 bg-atlas-ink px-4 text-sm font-semibold text-atlas-panel"
          >
            <Plus aria-hidden="true" size={16} />
            Create mission
          </Link>
        </div>

        <div className="p-5">
          <div className="flex items-center justify-between gap-3">
            <h3 className="text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/45">
              Mission definitions
            </h3>
            <button
              type="button"
              onClick={() => void loadMissions()}
              className="inline-flex min-h-8 items-center gap-2 border border-atlas-ink/15 px-2.5 text-xs font-semibold"
            >
              <RefreshCw aria-hidden="true" size={14} />
              Refresh
            </button>
          </div>

          <div className="mt-3 space-y-2">
            {loadingMissions && (
              <p className="text-sm text-atlas-ink/60">Loading mission definitions...</p>
            )}
            {!loadingMissions && missions.length === 0 && (
              <p className="border border-atlas-ink/10 p-3 text-sm text-atlas-ink/60">
                No missions submitted for this drone yet.
              </p>
            )}
            {missions.map((mission) => (
              <button
                key={mission.id}
                type="button"
                onClick={() => selectMission(mission.id)}
                className={`grid w-full gap-2 border p-3 text-left transition ${
                  selectedMissionId === mission.id
                    ? "border-atlas-ink bg-atlas-mist"
                    : "border-atlas-ink/10 hover:border-atlas-ink/30"
                }`}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate text-sm font-semibold">{mission.name}</span>
                  <span
                    className={`shrink-0 px-2 py-1 text-[10px] font-semibold uppercase tracking-[0.08em] ${missionValidationStyle(
                      mission.validationStatus,
                    )}`}
                  >
                    {mission.validationStatus.replace(/_/g, " ")}
                  </span>
                </div>
                <span className="text-xs text-atlas-ink/55">
                  {mission.waypoints.length} waypoint{mission.waypoints.length === 1 ? "" : "s"} ·{" "}
                  {mission.completionAction.replace(/_/g, " ")}
                </span>
              </button>
            ))}
          </div>
        </div>
      </aside>

      <section className="min-w-0 space-y-5">
        <div className="border border-atlas-ink/10 bg-atlas-panel p-5 shadow-sm shadow-atlas-ink/5">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-atlas-ink/10 pb-4">
            <div>
              <h2 className="text-xl font-semibold">Mission activity</h2>
              <p className="mt-1 text-sm text-atlas-ink/60">
                {missionStreamConnected ? "Live mission stream" : "Mission stream disconnected"}
              </p>
            </div>
            <MissionExecutionSummary execution={drone?.missionExecution} />
          </div>

          {missionError && (
            <p className="mt-4 border-l-4 border-atlas-signal bg-atlas-signal/10 px-4 py-3 text-sm">
              {missionError}
            </p>
          )}

          <div className="mt-5">
            <MissionDetailPanel
              detail={missionDetail}
              drone={drone}
              activeExecution={drone?.missionExecution}
              pendingAction={pendingAction}
              onUpload={() => void handleMissionAction("upload")}
              onStart={() => void handleMissionAction("start")}
              onAbort={() => void handleMissionAction("abort")}
            />
          </div>
        </div>
      </section>
    </div>
  );
}

function MissionRoute({ drones }: { drones: Drone[] }) {
  const { droneId, missionId } = useParams();

  return <MissionWorkspace drones={drones} droneId={droneId} missionId={missionId} />;
}

function MissionWorkspace({
  drones,
  droneId,
  missionId,
}: {
  drones: Drone[];
  droneId?: string;
  missionId?: string;
}) {
  const navigate = useNavigate();
  const [selectedDroneId, setSelectedDroneId] = useState(droneId ?? "");
  const [draft, setDraft] = useState<MissionDraft>(defaultMissionDraft);
  const [missionError, setMissionError] = useState<string | null>(null);
  const [pendingMissionAction, setPendingMissionAction] = useState<string | null>(null);

  useEffect(() => {
    if (droneId && droneId !== selectedDroneId) {
      setSelectedDroneId(droneId);
    }
  }, [droneId, selectedDroneId]);

  useEffect(() => {
    if (selectedDroneId || drones.length === 0 || missionId) {
      return;
    }

    setSelectedDroneId(drones[0].id);
  }, [drones, missionId, selectedDroneId]);

  useEffect(() => {
    if (!missionId) {
      return;
    }

    const currentMissionId = missionId;
    let active = true;

    async function loadMissionForEditing() {
      try {
        const detail = await fetchMission(currentMissionId);
        if (!active) {
          return;
        }

        setDraft(missionToDraft(detail.mission));
        setSelectedDroneId(detail.mission.droneId);
        setMissionError(null);
      } catch (err) {
        if (!active) {
          return;
        }

        setMissionError(err instanceof Error ? err.message : "Failed to load mission");
      }
    }

    void loadMissionForEditing();

    return () => {
      active = false;
    };
  }, [missionId]);

  async function handleCreateMission() {
    if (!selectedDroneId) {
      setMissionError("Select a drone before creating a mission");
      return;
    }

    const input = missionDraftToInput(draft);
    if (input.waypoints.length === 0) {
      setMissionError("Add at least one valid waypoint");
      return;
    }

    setPendingMissionAction("create");
    setMissionError(null);
    try {
      const mission = await createDroneMission(selectedDroneId, input);
      navigate(
        `/drones/${encodeURIComponent(selectedDroneId)}/missions?mission=${encodeURIComponent(
          mission.id,
        )}`,
      );
    } catch (err) {
      setMissionError(err instanceof Error ? err.message : "Mission create failed");
    } finally {
      setPendingMissionAction(null);
    }
  }

  const selectedDrone = drones.find((drone) => drone.id === selectedDroneId);

  return (
    <div className="grid min-h-[calc(100vh-7.5rem)] flex-1 gap-4 py-4 xl:grid-cols-[minmax(0,1fr)_25rem]">
      <section className="min-h-[34rem] overflow-hidden border border-atlas-ink/10 bg-atlas-panel shadow-sm shadow-atlas-ink/5">
        <MissionMapPlanner draft={draft} drone={selectedDrone} onChange={setDraft} />
      </section>

      <aside className="min-h-0 border border-atlas-ink/10 bg-atlas-panel shadow-sm shadow-atlas-ink/5">
        <div className="flex h-full max-h-[calc(100vh-8rem)] flex-col">
          <div className="border-b border-atlas-ink/10 p-5">
            <div className="flex items-center justify-between gap-3">
              <div>
                <h2 className="text-xl font-semibold">Mission planner</h2>
                <p className="mt-1 text-sm text-atlas-ink/60">
                  {selectedDrone ? `${selectedDrone.name} route plan` : "Select a drone"}
                </p>
              </div>
              <span className="rounded-full bg-atlas-mist px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/60">
                Phase 3
              </span>
            </div>

            <label className="mt-5 block text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/45">
              Drone
            </label>
            <select
              value={selectedDroneId}
              onChange={(event) => {
                const nextDroneId = event.target.value;
                setSelectedDroneId(nextDroneId);
                navigate(
                  nextDroneId ? `/drones/${encodeURIComponent(nextDroneId)}/missions` : "/missions",
                );
              }}
              className="mt-2 min-h-11 w-full border border-atlas-ink/15 bg-atlas-mist px-3 text-sm font-semibold text-atlas-ink"
            >
              {drones.length === 0 && <option value="">No registered drones</option>}
              {drones.map((drone) => (
                <option key={drone.id} value={drone.id}>
                  {drone.name} · {drone.id}
                </option>
              ))}
            </select>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto p-5">
            <MissionForm
              draft={draft}
              disabled={!selectedDroneId || pendingMissionAction === "create"}
              onChange={setDraft}
              onSubmit={() => void handleCreateMission()}
            />

            {missionError && (
              <p className="mt-4 border-l-4 border-atlas-signal bg-atlas-signal/10 px-4 py-3 text-sm">
                {missionError}
              </p>
            )}
          </div>
        </div>
      </aside>
    </div>
  );
}

function MissionDetailPanel({
  detail,
  drone,
  activeExecution,
  pendingAction,
  onUpload,
  onStart,
  onAbort,
}: {
  detail: MissionDetail | null;
  drone?: Drone;
  activeExecution?: MissionExecution;
  pendingAction: string | null;
  onUpload: () => void;
  onStart: () => void;
  onAbort: () => void;
}) {
  if (!detail) {
    return (
      <div className="border border-atlas-ink/10 p-4 text-sm text-atlas-ink/60">
        Select a mission definition to inspect validation, upload state, and execution history.
      </div>
    );
  }

  const latest = detail.executions[0];
  const currentExecution = activeExecution?.missionId === detail.mission.id ? activeExecution : latest;
  const selectedMissionLocked = missionExecutionLocksDefinition(currentExecution);
  const droneMissionLocked = missionExecutionLocksDefinition(activeExecution);
  const canEditRoute = !selectedMissionLocked;
  const canUpload = detail.mission.validationStatus === "validated" && !droneMissionLocked;
  const canStart = currentExecution?.state === "uploaded_to_vehicle" && !droneMissionLocked;
  const canAbort =
    currentExecution?.missionId === detail.mission.id && missionExecutionCanAbort(currentExecution);

  return (
    <div className="min-w-0 space-y-5">
      <div className="grid gap-4 border border-atlas-ink/10 p-4 lg:grid-cols-[1fr_auto]">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="truncate text-lg font-semibold">{detail.mission.name}</h3>
            <span
              className={`px-2.5 py-1 text-[11px] font-semibold uppercase tracking-[0.08em] ${missionValidationStyle(
                detail.mission.validationStatus,
              )}`}
            >
              {detail.mission.validationStatus.replace(/_/g, " ")}
            </span>
          </div>
          <p className="mt-1 text-sm text-atlas-ink/60">
            {detail.mission.id} · {detail.mission.completionAction.replace(/_/g, " ")}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          {canEditRoute ? (
            <Link
              to={`/missions/${encodeURIComponent(detail.mission.id)}`}
              className="inline-flex min-h-10 items-center gap-2 border border-atlas-ink/15 px-3 text-sm font-semibold"
            >
              <MapIcon aria-hidden="true" size={15} />
              Edit route
            </Link>
          ) : (
            <span
              className="inline-flex min-h-10 cursor-not-allowed items-center gap-2 border border-atlas-ink/15 px-3 text-sm font-semibold opacity-45"
            title="Route editing is locked while this mission is active"
            >
              <MapIcon aria-hidden="true" size={15} />
              Edit route
            </span>
          )}
          <button
            type="button"
            disabled={!canUpload || pendingAction === "upload"}
            onClick={onUpload}
            className="inline-flex min-h-10 items-center gap-2 border border-atlas-ink/15 px-3 text-sm font-semibold disabled:cursor-not-allowed disabled:opacity-45"
            title={
              droneMissionLocked
                ? "Upload is locked while this drone has an active mission"
                : !canUpload
                  ? "Mission must be validated before upload"
                  : "Upload mission"
            }
          >
            {pendingAction === "upload" ? (
              <Loader2 aria-hidden="true" className="animate-spin" size={15} />
            ) : (
              <UploadCloud aria-hidden="true" size={15} />
            )}
            Upload
          </button>
          <button
            type="button"
            disabled={!canStart || pendingAction === "start"}
            onClick={onStart}
            className="inline-flex min-h-10 items-center gap-2 bg-atlas-ink px-3 text-sm font-semibold text-atlas-panel disabled:cursor-not-allowed disabled:opacity-45"
            title={
              !canStart
                ? "Mission must be uploaded to vehicle before start"
                : "Arm, take off, and start mission"
            }
          >
            {pendingAction === "start" ? (
              <Loader2 aria-hidden="true" className="animate-spin" size={15} />
            ) : (
              <Play aria-hidden="true" size={15} />
            )}
            Start flight
          </button>
          <button
            type="button"
            disabled={!canAbort || pendingAction === "abort"}
            onClick={onAbort}
            className="inline-flex min-h-10 items-center gap-2 border border-atlas-signal/40 bg-atlas-signal/10 px-3 text-sm font-semibold text-atlas-ink disabled:cursor-not-allowed disabled:opacity-45"
            title={canAbort ? "Abort active mission and command RTL" : "Mission must be active before aborting to RTL"}
          >
            {pendingAction === "abort" ? (
              <Loader2 aria-hidden="true" className="animate-spin" size={15} />
            ) : (
              <RotateCcw aria-hidden="true" size={15} />
            )}
            Abort to RTL
          </button>
        </div>
      </div>

      {detail.mission.validationErrors && detail.mission.validationErrors.length > 0 && (
        <div className="border-l-4 border-atlas-signal bg-atlas-signal/10 px-4 py-3 text-sm">
          {detail.mission.validationErrors.map((error) => (
            <p key={`${error.field}:${error.message}`}>
              {error.field}: {error.message}
            </p>
          ))}
        </div>
      )}

      <LiveMissionMap mission={detail.mission} execution={currentExecution} drone={drone} />

      <div className="grid gap-5 xl:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]">
        <div>
          <h4 className="flex items-center gap-2 text-xs font-semibold uppercase tracking-[0.12em] text-atlas-ink/55">
            <Route aria-hidden="true" size={14} />
            Route
          </h4>
          <div className="mt-3 space-y-2">
            {detail.mission.waypoints.map((waypoint) => (
              <div
                key={waypoint.sequence}
                className="grid gap-2 border-l-2 border-atlas-ink/10 pl-3 text-sm sm:grid-cols-[auto_1fr]"
              >
                <span className="font-semibold">WP {waypoint.sequence}</span>
                <span className="text-atlas-ink/65">
                  {waypoint.latitude.toFixed(5)}, {waypoint.longitude.toFixed(5)} ·{" "}
                  {waypoint.relativeAltitudeM.toFixed(1)} m
                  {typeof waypoint.loiterTimeS === "number"
                    ? ` · hold ${waypoint.loiterTimeS}s`
                    : ""}
                </span>
              </div>
            ))}
          </div>
        </div>

        <div>
          <h4 className="flex items-center gap-2 text-xs font-semibold uppercase tracking-[0.12em] text-atlas-ink/55">
            <History aria-hidden="true" size={14} />
            Execution history
          </h4>
          <div className="mt-3 space-y-2">
            {detail.executions.length === 0 && (
              <p className="text-sm text-atlas-ink/60">No execution attempts yet.</p>
            )}
            {detail.executions.map((execution) => (
              <div key={execution.id} className="border border-atlas-ink/10 p-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <p className="truncate text-sm font-semibold">{execution.id}</p>
                  <span
                    className={`px-2.5 py-1 text-[11px] font-semibold uppercase tracking-[0.08em] ${
                      missionStateStyles[execution.state]
                    }`}
                  >
                    {missionStateLabel(execution.state)}
                  </span>
                </div>
                <p className="mt-1 text-sm text-atlas-ink/60">
                  {execution.resultMessage || "No result message yet"}
                </p>
                <p className="mt-1 text-xs text-atlas-ink/50">
                  Updated {formatTime(execution.progressUpdatedAt ?? execution.updatedAt)}
                </p>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

function LiveMissionMap({
  mission,
  execution,
  drone,
}: {
  mission: Mission;
  execution?: MissionExecution;
  drone?: Drone;
}) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<MapLibreMap | null>(null);
  const maplibreRef = useRef<MapLibreModule | null>(null);
  const waypointMarkersRef = useRef<Marker[]>([]);
  const droneMarkerRef = useRef<Marker | null>(null);
  const framedMissionRef = useRef("");

  const mappedWaypoints = useMemo(() => mappedMissionWaypointsFromMission(mission), [mission]);
  const dronePosition = droneTelemetryPosition(drone);
  const initialCenterRef = useRef(liveMissionMapCenter(mappedWaypoints, dronePosition));
  const [mapReady, setMapReady] = useState(false);
  const activeWaypointIndex = activeMissionWaypointIndex(execution, mappedWaypoints.length);
  const activeSegment = activeMissionSegment(mappedWaypoints, activeWaypointIndex, dronePosition);
  const activeBearingDeg = bearingToActiveWaypoint(
    dronePosition,
    mappedWaypoints,
    activeWaypointIndex,
  );
  const routeDistanceM = missionRouteDistanceM(mappedWaypoints);

  useEffect(() => {
    if (!containerRef.current || mapRef.current) {
      return;
    }

    let cancelled = false;

    void import("maplibre-gl").then((module) => {
      if (cancelled || !containerRef.current) {
        return;
      }

      const maplibre = module;
      maplibreRef.current = maplibre;
      const map = new maplibre.Map({
        container: containerRef.current,
        style: atlasMapStyle,
        center: initialCenterRef.current,
        zoom: 15,
        attributionControl: false,
      });
      mapRef.current = map;

      map.addControl(new maplibre.NavigationControl({ visualizePitch: false }), "top-right");
      map.addControl(new maplibre.AttributionControl({ compact: true }), "bottom-right");

      map.on("load", () => {
        map.addSource("live-mission-route", emptyMissionRouteSource());
        map.addSource("live-mission-active-segment", emptyMissionRouteSource());
        map.addLayer({
          id: "live-mission-route-casing",
          type: "line",
          source: "live-mission-route",
          paint: {
            "line-color": "#f8f7f1",
            "line-opacity": 0.92,
            "line-width": 8,
          },
        });
        map.addLayer({
          id: "live-mission-route-line",
          type: "line",
          source: "live-mission-route",
          paint: {
            "line-color": "#6f7f78",
            "line-opacity": 0.9,
            "line-width": 3,
          },
        });
        map.addLayer({
          id: "live-mission-active-segment-line",
          type: "line",
          source: "live-mission-active-segment",
          paint: {
            "line-color": "#cf5f38",
            "line-width": 5,
          },
        });
        setMapReady(true);
        requestAnimationFrame(() => map.resize());
      });
    });

    return () => {
      cancelled = true;
      waypointMarkersRef.current.forEach((marker) => marker.remove());
      waypointMarkersRef.current = [];
      droneMarkerRef.current?.remove();
      droneMarkerRef.current = null;
      mapRef.current?.remove();
      mapRef.current = null;
      maplibreRef.current = null;
      framedMissionRef.current = "";
      setMapReady(false);
    };
  }, []);

  useEffect(() => {
    if (!mapReady || !mapRef.current) {
      return;
    }

    const resizeMap = () => mapRef.current?.resize();
    resizeMap();
    window.addEventListener("resize", resizeMap);

    return () => {
      window.removeEventListener("resize", resizeMap);
    };
  }, [mapReady]);

  useEffect(() => {
    const maplibre = maplibreRef.current;
    if (!mapReady || !mapRef.current || !maplibre) {
      return;
    }

    updateMissionRouteSourceByID(mapRef.current, "live-mission-route", mappedWaypoints);
    updateLineSourceByID(
      mapRef.current,
      "live-mission-active-segment",
      activeSegmentFeatureCollection(activeSegment),
    );

    waypointMarkersRef.current.forEach((marker) => marker.remove());
    waypointMarkersRef.current = mappedWaypoints.map((waypoint) => {
      const element = document.createElement("div");
      element.className = liveWaypointMarkerClass(waypoint.index, activeWaypointIndex, execution);
      element.textContent = String(waypoint.index + 1);
      element.title = `Waypoint ${waypoint.index + 1}`;

      return new maplibre.Marker({ element })
        .setLngLat([waypoint.longitude, waypoint.latitude])
        .addTo(mapRef.current!);
    });

    if (framedMissionRef.current !== mission.id) {
      fitMissionRoute(mapRef.current, mappedWaypoints, dronePosition, null);
      framedMissionRef.current = mission.id;
    }
  }, [activeSegment, activeWaypointIndex, dronePosition, execution, mapReady, mappedWaypoints, mission.id]);

  useEffect(() => {
    const maplibre = maplibreRef.current;
    if (!mapReady || !mapRef.current || !maplibre) {
      return;
    }

    if (!dronePosition) {
      droneMarkerRef.current?.remove();
      droneMarkerRef.current = null;
      return;
    }

    if (!droneMarkerRef.current) {
      const element = document.createElement("div");
      element.className = "atlas-live-drone-marker";
      element.title = `${drone?.name ?? "Drone"} live position`;
      const heading = document.createElement("div");
      heading.className = "atlas-live-drone-marker-heading";
      element.appendChild(heading);
      droneMarkerRef.current = new maplibre.Marker({ element })
        .setLngLat([dronePosition.longitude, dronePosition.latitude])
        .addTo(mapRef.current);
    } else {
      droneMarkerRef.current.setLngLat([dronePosition.longitude, dronePosition.latitude]);
    }

    const headingElement = droneMarkerRef.current
      .getElement()
      .querySelector<HTMLDivElement>(".atlas-live-drone-marker-heading");
    if (headingElement && typeof drone?.telemetry?.headingDeg === "number") {
      headingElement.style.transform = `rotate(${drone.telemetry.headingDeg}deg)`;
    }
  }, [drone?.name, drone?.telemetry?.headingDeg, dronePosition, mapReady]);

  return (
    <section className="grid overflow-hidden border border-atlas-ink/10 lg:grid-cols-[minmax(0,1fr)_18rem]">
      <div className="min-h-[26rem] bg-atlas-mist/40">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-atlas-ink/10 bg-atlas-panel/95 p-4">
          <div className="min-w-0">
            <h4 className="flex items-center gap-2 text-xs font-semibold uppercase tracking-[0.12em] text-atlas-ink/55">
              <MapPin aria-hidden="true" size={14} />
              Live mission map
            </h4>
            <p className="mt-1 text-sm text-atlas-ink/60">
              {mappedWaypoints.length} waypoints · {formatDistance(routeDistanceM)}
            </p>
          </div>
          <button
            type="button"
            onClick={() => mapRef.current && fitMissionRoute(mapRef.current, mappedWaypoints, dronePosition, null)}
            className="inline-flex min-h-9 items-center gap-2 border border-atlas-ink/15 bg-atlas-panel px-3 text-sm font-semibold"
          >
            <MapIcon aria-hidden="true" size={15} />
            Frame
          </button>
        </div>
        <div className="relative h-[26rem] min-h-0">
          <div ref={containerRef} className="h-full w-full" />
          {!dronePosition && (
            <p className="pointer-events-none absolute left-4 top-4 max-w-sm border border-atlas-ink/10 bg-atlas-panel/95 px-3 py-2 text-xs font-medium text-atlas-ink/65 shadow-sm shadow-atlas-ink/10">
              Waiting for live drone position.
            </p>
          )}
        </div>
      </div>

      <LiveMissionTelemetryPanel
        execution={execution}
        drone={drone}
        activeWaypointIndex={activeWaypointIndex}
        activeBearingDeg={activeBearingDeg}
      />
    </section>
  );
}

function LiveMissionTelemetryPanel({
  execution,
  drone,
  activeWaypointIndex,
  activeBearingDeg,
}: {
  execution?: MissionExecution;
  drone?: Drone;
  activeWaypointIndex: number | null;
  activeBearingDeg: number | null;
}) {
  const telemetry = drone?.telemetry;

  return (
    <aside className="border-t border-atlas-ink/10 bg-atlas-panel p-4 lg:border-l lg:border-t-0">
      <div className="flex flex-wrap items-center gap-2">
        <span
          className={`px-2.5 py-1 text-[11px] font-semibold uppercase tracking-[0.08em] ${
            execution ? missionStateStyles[execution.state] : "bg-atlas-ink/10 text-atlas-ink/70"
          }`}
        >
          {execution ? missionStateLabel(execution.state) : "no execution"}
        </span>
        {telemetry && (
          <span className={`px-2.5 py-1 text-[11px] font-semibold uppercase tracking-[0.08em] ${telemetryStyles[telemetry.state]}`}>
            {telemetry.state}
          </span>
        )}
      </div>

      <div className="mt-4 grid grid-cols-2 gap-x-4 gap-y-3 lg:grid-cols-1">
        <LiveMissionMetric
          label="Waypoint"
          value={activeWaypointIndex === null ? "not active" : `WP ${activeWaypointIndex + 1}`}
        />
        <LiveMissionMetric
          label="Progress"
          value={
            execution && execution.currentMissionItem && execution.totalMissionItems
              ? `${execution.currentMissionItem}/${execution.totalMissionItems}`
              : "waiting"
          }
        />
        <LiveMissionMetric
          label="Altitude"
          value={telemetry ? `${telemetry.relativeAltitudeM.toFixed(1)} m` : "waiting"}
        />
        <LiveMissionMetric
          label="Ground speed"
          value={telemetry ? `${telemetry.groundSpeedMPS.toFixed(1)} m/s` : "waiting"}
        />
        <LiveMissionMetric label="Flight mode" value={telemetry?.flightMode ?? "waiting"} />
        <LiveMissionMetric
          label="Heading"
          value={telemetry ? `${telemetry.headingDeg.toFixed(0)} deg` : "waiting"}
        />
        <LiveMissionMetric
          label="Track"
          value={activeBearingDeg === null ? "waiting" : `${activeBearingDeg.toFixed(0)} deg`}
        />
      </div>
    </aside>
  );
}

function LiveMissionMetric({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 border-b border-atlas-ink/10 pb-2 last:border-b-0">
      <p className="text-[11px] font-semibold uppercase tracking-[0.12em] text-atlas-ink/45">
        {label}
      </p>
      <p className="mt-1 truncate text-sm font-semibold text-atlas-ink">{value}</p>
    </div>
  );
}

function MissionForm({
  draft,
  disabled,
  onChange,
  onSubmit,
}: {
  draft: MissionDraft;
  disabled: boolean;
  onChange: (draft: MissionDraft) => void;
  onSubmit: () => void;
}) {
  function updateWaypoint(index: number, field: keyof MissionDraftWaypoint, value: string) {
    onChange({
      ...draft,
      waypoints: draft.waypoints.map((waypoint, waypointIndex) =>
        waypointIndex === index ? { ...waypoint, [field]: value } : waypoint,
      ),
    });
  }

  function addWaypoint() {
    onChange({
      ...draft,
      waypoints: [
        ...draft.waypoints,
        { latitude: "", longitude: "", relativeAltitudeM: "12", speedMPS: "", loiterTimeS: "" },
      ],
    });
  }

  function removeWaypoint(index: number) {
    onChange({
      ...draft,
      waypoints: draft.waypoints.filter((_, waypointIndex) => waypointIndex !== index),
    });
  }

  return (
    <div className="mt-5 space-y-4">
      <label className="block">
        <span className="text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/45">
          Mission name
        </span>
        <input
          value={draft.name}
          onChange={(event) => onChange({ ...draft, name: event.target.value })}
          className="mt-2 min-h-11 w-full border border-atlas-ink/15 bg-atlas-mist px-3 text-sm font-semibold text-atlas-ink"
        />
      </label>

      <label className="block">
        <span className="text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/45">
          Completion
        </span>
        <select
          value={draft.completionAction}
          onChange={(event) =>
            onChange({ ...draft, completionAction: event.target.value as MissionCompletionAction })
          }
          className="mt-2 min-h-11 w-full border border-atlas-ink/15 bg-atlas-mist px-3 text-sm font-semibold text-atlas-ink"
        >
          <option value="hold">Hold at final point</option>
          <option value="return_to_launch">Return to launch</option>
          <option value="land">Land at final point</option>
        </select>
      </label>

      <div>
        <div className="flex items-center justify-between">
          <h3 className="text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/45">
            Waypoints
          </h3>
          <button
            type="button"
            onClick={addWaypoint}
            className="inline-flex min-h-9 items-center gap-2 border border-atlas-ink/15 px-3 text-sm font-semibold"
          >
            <Plus aria-hidden="true" size={15} />
            Add
          </button>
        </div>

        <div className="mt-3 space-y-3">
          {draft.waypoints.length === 0 && (
            <p className="border border-atlas-ink/10 p-3 text-sm text-atlas-ink/60">
              Click the map, use the drone marker, or use your browser location to add the first
              waypoint.
            </p>
          )}
          {draft.waypoints.map((waypoint, index) => (
            <div key={index} className="border border-atlas-ink/10 p-3">
              <div className="mb-3 flex items-center justify-between">
                <p className="text-sm font-semibold">Waypoint {index + 1}</p>
                <button
                  type="button"
                  onClick={() => removeWaypoint(index)}
                  disabled={draft.waypoints.length <= 1}
                  className="inline-flex h-8 w-8 items-center justify-center border border-atlas-ink/15 disabled:opacity-35"
                  title="Remove waypoint"
                >
                  <Trash2 aria-hidden="true" size={15} />
                </button>
              </div>
              <div className="grid gap-2 sm:grid-cols-2">
                <MissionInput label="Latitude" value={waypoint.latitude} onChange={(value) => updateWaypoint(index, "latitude", value)} />
                <MissionInput label="Longitude" value={waypoint.longitude} onChange={(value) => updateWaypoint(index, "longitude", value)} />
                <MissionInput label="Altitude m" value={waypoint.relativeAltitudeM} onChange={(value) => updateWaypoint(index, "relativeAltitudeM", value)} />
                <MissionInput label="Speed m/s" value={waypoint.speedMPS} onChange={(value) => updateWaypoint(index, "speedMPS", value)} />
                <MissionInput label="Loiter s" value={waypoint.loiterTimeS} onChange={(value) => updateWaypoint(index, "loiterTimeS", value)} />
              </div>
            </div>
          ))}
        </div>
      </div>

      <button
        type="button"
        disabled={disabled}
        onClick={onSubmit}
        className="inline-flex min-h-11 w-full items-center justify-center gap-2 bg-atlas-ink px-4 text-sm font-semibold text-atlas-panel disabled:cursor-not-allowed disabled:opacity-45"
      >
        {disabled ? <Loader2 aria-hidden="true" className="animate-spin" size={16} /> : <ListChecks aria-hidden="true" size={16} />}
        Submit mission
      </button>
    </div>
  );
}

function MissionMapPlanner({
  draft,
  drone,
  onChange,
}: {
  draft: MissionDraft;
  drone?: Drone;
  onChange: (draft: MissionDraft) => void;
}) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<MapLibreMap | null>(null);
  const maplibreRef = useRef<MapLibreModule | null>(null);
  const waypointMarkersRef = useRef<Marker[]>([]);
  const droneMarkerRef = useRef<Marker | null>(null);
  const operatorMarkerRef = useRef<Marker | null>(null);
  const draftRef = useRef(draft);
  const onChangeRef = useRef(onChange);
  const initialCenterRef = useRef(missionMapCenter(draft, drone));
  const [mapReady, setMapReady] = useState(false);
  const [selectedWaypointIndex, setSelectedWaypointIndex] = useState<number | null>(null);
  const [operatorPosition, setOperatorPosition] = useState<MapPosition | null>(null);
  const [operatorLocationStatus, setOperatorLocationStatus] = useState<
    "idle" | "requesting" | "available" | "unavailable"
  >("idle");

  const mappedWaypoints = useMemo(() => mappedMissionWaypoints(draft), [draft]);
  const dronePosition = droneTelemetryPosition(drone);
  const routeDistanceM = missionRouteDistanceM(mappedWaypoints);

  useEffect(() => {
    draftRef.current = draft;
    onChangeRef.current = onChange;
  }, [draft, onChange]);

  useEffect(() => {
    if (!containerRef.current || mapRef.current) {
      return;
    }

    let cancelled = false;

    void import("maplibre-gl").then((module) => {
      if (cancelled || !containerRef.current) {
        return;
      }

      const maplibre = module;
      maplibreRef.current = maplibre;
      const map = new maplibre.Map({
        container: containerRef.current,
        style: atlasMapStyle,
        center: initialCenterRef.current,
        zoom: 15,
        attributionControl: false,
      });
      mapRef.current = map;

      map.addControl(new maplibre.NavigationControl({ visualizePitch: false }), "top-right");
      map.addControl(new maplibre.AttributionControl({ compact: true }), "bottom-right");

      map.on("load", () => {
        map.addSource("mission-route", emptyMissionRouteSource());
        map.addLayer({
          id: "mission-route-casing",
          type: "line",
          source: "mission-route",
          paint: {
            "line-color": "#f8f7f1",
            "line-opacity": 0.9,
            "line-width": 7,
          },
        });
        map.addLayer({
          id: "mission-route-line",
          type: "line",
          source: "mission-route",
          paint: {
            "line-color": "#cf5f38",
            "line-width": 3,
          },
        });
        setMapReady(true);
        requestAnimationFrame(() => map.resize());
      });

      map.on("click", (event) => {
        const nextDraft = appendWaypointFromLngLat(
          draftRef.current,
          event.lngLat.lng,
          event.lngLat.lat,
        );
        onChangeRef.current(nextDraft);
        setSelectedWaypointIndex(nextDraft.waypoints.length - 1);
      });
    });

    return () => {
      cancelled = true;
      waypointMarkersRef.current.forEach((marker) => marker.remove());
      waypointMarkersRef.current = [];
      droneMarkerRef.current?.remove();
      droneMarkerRef.current = null;
      operatorMarkerRef.current?.remove();
      operatorMarkerRef.current = null;
      mapRef.current?.remove();
      mapRef.current = null;
      maplibreRef.current = null;
      setMapReady(false);
    };
  }, []);

  useEffect(() => {
    if (!mapReady || !mapRef.current) {
      return;
    }

    const resizeMap = () => mapRef.current?.resize();
    resizeMap();
    window.addEventListener("resize", resizeMap);

    return () => {
      window.removeEventListener("resize", resizeMap);
    };
  }, [mapReady]);

  useEffect(() => {
    if (mappedWaypoints.length > 0 || dronePosition || operatorLocationStatus !== "idle") {
      return;
    }

    if (!("geolocation" in navigator)) {
      setOperatorLocationStatus("unavailable");
      return;
    }

    setOperatorLocationStatus("requesting");
    navigator.geolocation.getCurrentPosition(
      (position) => {
        setOperatorPosition({
          latitude: position.coords.latitude,
          longitude: position.coords.longitude,
          accuracyM: position.coords.accuracy,
        });
        setOperatorLocationStatus("available");
      },
      () => {
        setOperatorLocationStatus("unavailable");
      },
      {
        enableHighAccuracy: true,
        maximumAge: 30000,
        timeout: 8000,
      },
    );
  }, [dronePosition, mappedWaypoints.length, operatorLocationStatus]);

  useEffect(() => {
    const maplibre = maplibreRef.current;
    if (!mapReady || !mapRef.current || !maplibre) {
      return;
    }

    updateMissionRouteSource(mapRef.current, mappedWaypoints);
    waypointMarkersRef.current.forEach((marker) => marker.remove());
    waypointMarkersRef.current = mappedWaypoints.map((waypoint) => {
      const element = document.createElement("button");
      element.type = "button";
      element.className =
        selectedWaypointIndex === waypoint.index
          ? "atlas-waypoint-marker atlas-waypoint-marker-selected"
          : "atlas-waypoint-marker";
      element.textContent = String(waypoint.index + 1);
      element.title = `Waypoint ${waypoint.index + 1}`;
      element.addEventListener("click", (event) => {
        event.stopPropagation();
        setSelectedWaypointIndex(waypoint.index);
      });

      const marker = new maplibre.Marker({ element, draggable: true })
        .setLngLat([waypoint.longitude, waypoint.latitude])
        .addTo(mapRef.current!);

      marker.on("dragstart", () => setSelectedWaypointIndex(waypoint.index));
      marker.on("dragend", () => {
        const position = marker.getLngLat();
        onChangeRef.current(
          replaceWaypointLocation(draftRef.current, waypoint.index, position.lng, position.lat),
        );
      });

      return marker;
    });
  }, [mapReady, mappedWaypoints, selectedWaypointIndex]);

  useEffect(() => {
    const maplibre = maplibreRef.current;
    if (!mapReady || !mapRef.current || !maplibre) {
      return;
    }

    droneMarkerRef.current?.remove();
    droneMarkerRef.current = null;

    if (!dronePosition) {
      return;
    }

    const element = document.createElement("div");
    element.className = "atlas-drone-marker";
    element.title = `${drone?.name ?? "Drone"} position`;
    droneMarkerRef.current = new maplibre.Marker({ element })
      .setLngLat([dronePosition.longitude, dronePosition.latitude])
      .addTo(mapRef.current);
  }, [drone?.name, dronePosition, mapReady]);

  useEffect(() => {
    const maplibre = maplibreRef.current;
    if (!mapReady || !mapRef.current || !maplibre) {
      return;
    }

    operatorMarkerRef.current?.remove();
    operatorMarkerRef.current = null;

    if (!operatorPosition) {
      return;
    }

    const element = document.createElement("div");
    element.className = "atlas-operator-marker";
    element.title = `Operator location${
      typeof operatorPosition.accuracyM === "number"
        ? `, accuracy ${operatorPosition.accuracyM.toFixed(0)} m`
        : ""
    }`;
    operatorMarkerRef.current = new maplibre.Marker({ element })
      .setLngLat([operatorPosition.longitude, operatorPosition.latitude])
      .addTo(mapRef.current);

    if (!dronePosition && mappedWaypoints.length === 0) {
      mapRef.current.flyTo({
        center: [operatorPosition.longitude, operatorPosition.latitude],
        zoom: 16,
      });
    }
  }, [dronePosition, mappedWaypoints.length, mapReady, operatorPosition]);

  function addDronePositionWaypoint() {
    if (!dronePosition) {
      return;
    }

    const nextDraft = appendWaypointFromLngLat(
      draftRef.current,
      dronePosition.longitude,
      dronePosition.latitude,
    );
    onChangeRef.current(nextDraft);
    setSelectedWaypointIndex(nextDraft.waypoints.length - 1);
    mapRef.current?.flyTo({ center: [dronePosition.longitude, dronePosition.latitude], zoom: 16 });
  }

  function addOperatorPositionWaypoint() {
    if (!operatorPosition) {
      return;
    }

    const nextDraft = appendWaypointFromLngLat(
      draftRef.current,
      operatorPosition.longitude,
      operatorPosition.latitude,
    );
    onChangeRef.current(nextDraft);
    setSelectedWaypointIndex(nextDraft.waypoints.length - 1);
    mapRef.current?.flyTo({
      center: [operatorPosition.longitude, operatorPosition.latitude],
      zoom: 16,
    });
  }

  function clearRoute() {
    onChangeRef.current({ ...draftRef.current, waypoints: [] });
    setSelectedWaypointIndex(null);
  }

  function frameRoute() {
    if (mapRef.current) {
      fitMissionRoute(mapRef.current, mappedWaypoints, dronePosition, operatorPosition);
    }
  }

  return (
    <div className="flex h-full min-h-[34rem] flex-col bg-atlas-mist/40">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-atlas-ink/10 bg-atlas-panel/95 p-4">
        <div className="min-w-0">
          <h3 className="flex items-center gap-2 text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/55">
            <MapPin aria-hidden="true" size={15} />
            Route map
          </h3>
          <p className="mt-1 text-sm text-atlas-ink/65">
            {mappedWaypoints.length} points · {formatDistance(routeDistanceM)}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={addDronePositionWaypoint}
            disabled={!dronePosition}
            className="inline-flex min-h-9 items-center gap-2 border border-atlas-ink/15 bg-atlas-panel px-3 text-sm font-semibold disabled:cursor-not-allowed disabled:opacity-40"
          >
            <LocateFixed aria-hidden="true" size={15} />
            Drone
          </button>
          <button
            type="button"
            onClick={addOperatorPositionWaypoint}
            disabled={!operatorPosition}
            className="inline-flex min-h-9 items-center gap-2 border border-atlas-ink/15 bg-atlas-panel px-3 text-sm font-semibold disabled:cursor-not-allowed disabled:opacity-40"
            title={
              operatorLocationStatus === "requesting"
                ? "Requesting browser location"
                : operatorLocationStatus === "unavailable"
                  ? "Browser location unavailable"
                  : "Use operator location"
            }
          >
            <LocateFixed aria-hidden="true" size={15} />
            Me
          </button>
          <button
            type="button"
            onClick={frameRoute}
            disabled={mappedWaypoints.length === 0 && !dronePosition && !operatorPosition}
            className="inline-flex min-h-9 items-center gap-2 border border-atlas-ink/15 bg-atlas-panel px-3 text-sm font-semibold disabled:cursor-not-allowed disabled:opacity-40"
          >
            <MapIcon aria-hidden="true" size={15} />
            Frame
          </button>
          <button
            type="button"
            onClick={clearRoute}
            disabled={draft.waypoints.length === 0}
            className="inline-flex min-h-9 items-center gap-2 border border-atlas-ink/15 bg-atlas-panel px-3 text-sm font-semibold disabled:cursor-not-allowed disabled:opacity-40"
          >
            <Trash2 aria-hidden="true" size={15} />
            Clear
          </button>
        </div>
      </div>

      <div className="relative min-h-0 flex-1 overflow-hidden bg-atlas-panel">
        <div ref={containerRef} className="h-full min-h-[32rem] w-full" />
        {mappedWaypoints.length === 0 && (
          <div className="pointer-events-none absolute left-4 top-4 max-w-sm border border-atlas-ink/10 bg-atlas-panel/95 px-4 py-3 text-sm text-atlas-ink/70 shadow-sm shadow-atlas-ink/10">
            Click the map to create the first waypoint.
          </div>
        )}
        <p className="pointer-events-none absolute bottom-4 left-4 max-w-sm border border-atlas-ink/10 bg-atlas-panel/95 px-3 py-2 text-xs font-medium text-atlas-ink/60 shadow-sm shadow-atlas-ink/10">
          {operatorLocationStatus === "requesting"
            ? "Requesting browser location. Click to append a waypoint."
            : operatorLocationStatus === "unavailable" && !dronePosition
              ? "Browser location unavailable. Click to append a waypoint."
              : "Click to append a waypoint. Drag numbered markers to adjust latitude and longitude."}
        </p>
      </div>
    </div>
  );
}

function MissionInput({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <label className="block">
      <span className="text-[11px] font-semibold uppercase tracking-[0.12em] text-atlas-ink/45">
        {label}
      </span>
      <input
        value={value}
        onChange={(event) => onChange(event.target.value)}
        inputMode="decimal"
        className="mt-1 min-h-10 w-full border border-atlas-ink/15 bg-atlas-mist px-2 text-sm font-semibold text-atlas-ink"
      />
    </label>
  );
}

function mappedMissionWaypoints(draft: MissionDraft): MappedMissionWaypoint[] {
  return draft.waypoints.flatMap((waypoint, index): MappedMissionWaypoint[] => {
    const latitude = Number.parseFloat(waypoint.latitude);
    const longitude = Number.parseFloat(waypoint.longitude);
    if (!Number.isFinite(latitude) || !Number.isFinite(longitude)) {
      return [];
    }

    return [{ index, latitude, longitude }];
  });
}

function mappedMissionWaypointsFromMission(mission: Mission): MappedMissionWaypoint[] {
  return mission.waypoints
    .slice()
    .sort((first, second) => first.sequence - second.sequence)
    .map((waypoint, index) => ({
      index,
      latitude: waypoint.latitude,
      longitude: waypoint.longitude,
    }))
    .filter(
      (waypoint) =>
        Number.isFinite(waypoint.latitude) && Number.isFinite(waypoint.longitude),
    );
}

function droneTelemetryPosition(drone?: Drone) {
  if (
    !drone?.telemetry ||
    !Number.isFinite(drone.telemetry.latitude) ||
    !Number.isFinite(drone.telemetry.longitude)
  ) {
    return null;
  }

  return {
    latitude: drone.telemetry.latitude,
    longitude: drone.telemetry.longitude,
  };
}

function missionMapCenter(draft: MissionDraft, drone?: Drone): [number, number] {
  const dronePosition = droneTelemetryPosition(drone);
  if (dronePosition) {
    return [dronePosition.longitude, dronePosition.latitude];
  }

  const firstWaypoint = mappedMissionWaypoints(draft)[0];
  if (firstWaypoint) {
    return [firstWaypoint.longitude, firstWaypoint.latitude];
  }

  return fallbackMissionCenter;
}

function liveMissionMapCenter(
  waypoints: MappedMissionWaypoint[],
  dronePosition: MapPosition | null,
): [number, number] {
  if (dronePosition) {
    return [dronePosition.longitude, dronePosition.latitude];
  }

  const firstWaypoint = waypoints[0];
  if (firstWaypoint) {
    return [firstWaypoint.longitude, firstWaypoint.latitude];
  }

  return fallbackMissionCenter;
}

function emptyMissionRouteSource(): GeoJSONSourceSpecification {
  return {
    type: "geojson",
    data: missionRouteFeatureCollection([]),
  };
}

function updateMissionRouteSource(map: MapLibreMap, waypoints: MappedMissionWaypoint[]) {
  const source = map.getSource("mission-route") as GeoJSONSource | undefined;
  source?.setData(missionRouteFeatureCollection(waypoints));
}

function updateMissionRouteSourceByID(
  map: MapLibreMap,
  sourceID: string,
  waypoints: MappedMissionWaypoint[],
) {
  const source = map.getSource(sourceID) as GeoJSONSource | undefined;
  source?.setData(missionRouteFeatureCollection(waypoints));
}

function updateLineSourceByID(
  map: MapLibreMap,
  sourceID: string,
  featureCollection: ReturnType<typeof activeSegmentFeatureCollection>,
) {
  const source = map.getSource(sourceID) as GeoJSONSource | undefined;
  source?.setData(featureCollection);
}

function missionRouteFeatureCollection(waypoints: MappedMissionWaypoint[]) {
  const coordinates = waypoints.map((waypoint) => [waypoint.longitude, waypoint.latitude]);

  return {
    type: "FeatureCollection" as const,
    features:
      coordinates.length > 1
        ? [
            {
              type: "Feature" as const,
              properties: {},
              geometry: {
                type: "LineString" as const,
                coordinates,
              },
            },
          ]
        : [],
  };
}

function activeSegmentFeatureCollection(coordinates: Array<[number, number]>) {
  return {
    type: "FeatureCollection" as const,
    features:
      coordinates.length > 1
        ? [
            {
              type: "Feature" as const,
              properties: {},
              geometry: {
                type: "LineString" as const,
                coordinates,
              },
            },
          ]
        : [],
  };
}

function activeMissionWaypointIndex(execution: MissionExecution | undefined, waypointCount: number) {
  if (!execution || waypointCount === 0) {
    return null;
  }

  if (execution.state === "completed" || execution.state === "hold") {
    return waypointCount - 1;
  }

  if (typeof execution.currentMissionItem !== "number" || execution.currentMissionItem <= 0) {
    return null;
  }

  return Math.max(0, Math.min(waypointCount - 1, execution.currentMissionItem - 1));
}

function activeMissionSegment(
  waypoints: MappedMissionWaypoint[],
  activeWaypointIndex: number | null,
  dronePosition: MapPosition | null,
) {
  if (activeWaypointIndex === null || activeWaypointIndex >= waypoints.length) {
    return [];
  }

  const activeWaypoint = waypoints[activeWaypointIndex];
  if (activeWaypointIndex === 0 && dronePosition) {
    return [
      [dronePosition.longitude, dronePosition.latitude] as [number, number],
      [activeWaypoint.longitude, activeWaypoint.latitude] as [number, number],
    ];
  }

  const previousWaypoint = waypoints[activeWaypointIndex - 1];
  if (!previousWaypoint) {
    return [];
  }

  return [
    [previousWaypoint.longitude, previousWaypoint.latitude] as [number, number],
    [activeWaypoint.longitude, activeWaypoint.latitude] as [number, number],
  ];
}

function bearingToActiveWaypoint(
  dronePosition: MapPosition | null,
  waypoints: MappedMissionWaypoint[],
  activeWaypointIndex: number | null,
) {
  if (!dronePosition || activeWaypointIndex === null) {
    return null;
  }

  const waypoint = waypoints[activeWaypointIndex];
  if (!waypoint) {
    return null;
  }

  return bearingDegrees(
    dronePosition.latitude,
    dronePosition.longitude,
    waypoint.latitude,
    waypoint.longitude,
  );
}

function bearingDegrees(
  fromLatitude: number,
  fromLongitude: number,
  toLatitude: number,
  toLongitude: number,
) {
  const fromLat = degreesToRadians(fromLatitude);
  const toLat = degreesToRadians(toLatitude);
  const deltaLon = degreesToRadians(toLongitude - fromLongitude);
  const y = Math.sin(deltaLon) * Math.cos(toLat);
  const x =
    Math.cos(fromLat) * Math.sin(toLat) -
    Math.sin(fromLat) * Math.cos(toLat) * Math.cos(deltaLon);
  const bearing = (Math.atan2(y, x) * 180) / Math.PI;
  return (bearing + 360) % 360;
}

function liveWaypointMarkerClass(
  waypointIndex: number,
  activeWaypointIndex: number | null,
  execution?: MissionExecution,
) {
  if (activeWaypointIndex !== null && waypointIndex === activeWaypointIndex) {
    return "atlas-waypoint-marker atlas-waypoint-marker-active";
  }

  if (
    activeWaypointIndex !== null &&
    (waypointIndex < activeWaypointIndex ||
      execution?.state === "completed" ||
      execution?.state === "hold")
  ) {
    return "atlas-waypoint-marker atlas-waypoint-marker-complete";
  }

  return "atlas-waypoint-marker atlas-waypoint-marker-readonly";
}

function appendWaypointFromLngLat(draft: MissionDraft, longitude: number, latitude: number) {
  const previous = draft.waypoints[draft.waypoints.length - 1];

  return {
    ...draft,
    waypoints: [
      ...draft.waypoints,
      {
        latitude: formatCoordinate(latitude),
        longitude: formatCoordinate(longitude),
        relativeAltitudeM: previous?.relativeAltitudeM || "12",
        speedMPS: previous?.speedMPS || "3",
        loiterTimeS: "",
      },
    ],
  };
}

function replaceWaypointLocation(
  draft: MissionDraft,
  waypointIndex: number,
  longitude: number,
  latitude: number,
) {
  return {
    ...draft,
    waypoints: draft.waypoints.map((waypoint, index) =>
      index === waypointIndex
        ? {
            ...waypoint,
            latitude: formatCoordinate(latitude),
            longitude: formatCoordinate(longitude),
          }
        : waypoint,
    ),
  };
}

function fitMissionRoute(
  map: MapLibreMap,
  waypoints: MappedMissionWaypoint[],
  dronePosition: MapPosition | null,
  operatorPosition?: MapPosition | null,
) {
  const coordinates = waypoints.map(
    (waypoint) => [waypoint.longitude, waypoint.latitude] as [number, number],
  );
  if (dronePosition) {
    coordinates.push([dronePosition.longitude, dronePosition.latitude]);
  }
  if (operatorPosition) {
    coordinates.push([operatorPosition.longitude, operatorPosition.latitude]);
  }

  if (coordinates.length === 0) {
    return;
  }

  if (coordinates.length === 1) {
    map.flyTo({ center: coordinates[0], zoom: 16 });
    return;
  }

  const lngValues = coordinates.map(([longitude]) => longitude);
  const latValues = coordinates.map(([, latitude]) => latitude);
  map.fitBounds(
    [
      [Math.min(...lngValues), Math.min(...latValues)],
      [Math.max(...lngValues), Math.max(...latValues)],
    ],
    { padding: 60, maxZoom: 17, duration: 400 },
  );
}

function missionRouteDistanceM(waypoints: MappedMissionWaypoint[]) {
  return waypoints.reduce((distance, waypoint, index) => {
    const previous = waypoints[index - 1];
    if (!previous) {
      return distance;
    }

    return distance + haversineDistanceM(previous, waypoint);
  }, 0);
}

function haversineDistanceM(from: MappedMissionWaypoint, to: MappedMissionWaypoint) {
  const earthRadiusM = 6371000;
  const fromLat = degreesToRadians(from.latitude);
  const toLat = degreesToRadians(to.latitude);
  const deltaLat = degreesToRadians(to.latitude - from.latitude);
  const deltaLon = degreesToRadians(to.longitude - from.longitude);
  const a =
    Math.sin(deltaLat / 2) * Math.sin(deltaLat / 2) +
    Math.cos(fromLat) * Math.cos(toLat) * Math.sin(deltaLon / 2) * Math.sin(deltaLon / 2);
  return 2 * earthRadiusM * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
}

function degreesToRadians(value: number) {
  return (value * Math.PI) / 180;
}

function formatCoordinate(value: number) {
  return value.toFixed(6);
}

function formatDistance(distanceM: number) {
  if (!Number.isFinite(distanceM) || distanceM <= 0) {
    return "0 m";
  }

  if (distanceM < 1000) {
    return `${distanceM.toFixed(0)} m`;
  }

  return `${(distanceM / 1000).toFixed(2)} km`;
}

function missionDraftToInput(draft: MissionDraft): CreateMissionInput {
  return {
    name: draft.name.trim(),
    completionAction: draft.completionAction,
    waypoints: draft.waypoints.flatMap((waypoint): CreateMissionWaypointInput[] => {
      const latitude = Number.parseFloat(waypoint.latitude);
      const longitude = Number.parseFloat(waypoint.longitude);
      const relativeAltitudeM = Number.parseFloat(waypoint.relativeAltitudeM);
      if (!Number.isFinite(latitude) || !Number.isFinite(longitude) || !Number.isFinite(relativeAltitudeM)) {
        return [];
      }

      return [
        {
          latitude,
          longitude,
          relativeAltitudeM,
          speedMPS: optionalNumber(waypoint.speedMPS),
          loiterTimeS: optionalNumber(waypoint.loiterTimeS),
        },
      ];
    }),
  };
}

function missionToDraft(mission: Mission): MissionDraft {
  return {
    name: mission.name,
    completionAction: mission.completionAction,
    waypoints: mission.waypoints
      .slice()
      .sort((first, second) => first.sequence - second.sequence)
      .map((waypoint) => ({
        latitude: formatCoordinate(waypoint.latitude),
        longitude: formatCoordinate(waypoint.longitude),
        relativeAltitudeM: formatNumberInput(waypoint.relativeAltitudeM),
        speedMPS:
          typeof waypoint.speedMPS === "number" ? formatNumberInput(waypoint.speedMPS) : "",
        loiterTimeS:
          typeof waypoint.loiterTimeS === "number" ? formatNumberInput(waypoint.loiterTimeS) : "",
      })),
  };
}

function formatNumberInput(value: number) {
  return Number.isInteger(value) ? String(value) : String(Number(value.toFixed(3)));
}

function optionalNumber(value: string) {
  if (value.trim() === "") {
    return undefined;
  }

  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function MissionPanel({ drone }: { drone: Drone }) {
  const execution = drone.missionExecution;
  const guide = missionStartGuide(drone);

  if (!execution) {
    return (
      <div className="border-t border-atlas-ink/10 pt-4 text-sm text-atlas-ink/65">
        No mission execution yet.
      </div>
    );
  }

  const progress = missionProgressPercent(execution);
  const hasProgress =
    typeof execution.currentMissionItem === "number" &&
    typeof execution.totalMissionItems === "number" &&
    execution.totalMissionItems > 0;

  return (
    <div className="grid gap-4 border-t border-atlas-ink/10 pt-4 lg:grid-cols-[18rem_minmax(0,1fr)] xl:grid-cols-[20rem_minmax(0,1fr)]">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <h4 className="text-xs font-semibold uppercase tracking-[0.12em] text-atlas-ink/55">
            Mission
          </h4>
          <span
            className={`max-w-full truncate whitespace-nowrap px-2.5 py-1 text-[11px] font-semibold uppercase tracking-[0.08em] ${
              missionStateStyles[execution.state]
            }`}
          >
            {missionStateLabel(execution.state)}
          </span>
        </div>

        <p className="mt-2 truncate text-sm font-semibold text-atlas-ink">
          {execution.missionId} · {execution.id}
        </p>
        <p className="mt-1 text-sm text-atlas-ink/60">
          Updated {formatTime(execution.progressUpdatedAt ?? execution.updatedAt)}
        </p>
        {guide && <p className="mt-3 text-sm font-medium text-atlas-signal">{guide}</p>}
      </div>

      <div className="min-w-0">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-sm font-semibold text-atlas-ink">
            {hasProgress
              ? `${execution.currentMissionItem}/${execution.totalMissionItems} mission items`
              : "Waiting for mission progress"}
          </p>
          {execution.resultMessage && (
            <p className="max-w-full truncate text-sm text-atlas-ink/60">
              {execution.resultMessage}
            </p>
          )}
        </div>

        <div className="mt-3 h-2 overflow-hidden bg-atlas-ink/10">
          <div
            className="h-full bg-atlas-field transition-transform duration-500"
            style={{ transform: `translateX(-${100 - progress}%)` }}
          />
        </div>

        <div className="mt-3 grid grid-cols-6 gap-1.5 sm:gap-2">
          {missionLifecycleSteps.map((step) => {
            const complete = isMissionLifecycleStepComplete(execution.state, step.state);
            return (
              <div
                key={step.state}
                title={step.label}
                className={`min-w-0 overflow-hidden whitespace-nowrap border px-1.5 py-2 text-center text-[11px] font-semibold leading-none sm:px-2 ${
                  complete
                    ? "border-atlas-field/50 bg-atlas-field/15 text-atlas-ink"
                    : "border-atlas-ink/10 text-atlas-ink/45"
                }`}
              >
                {step.shortLabel}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

function CommandPanel({
  drone,
  commands,
  error,
  pendingCommands,
  onCommand,
}: {
  drone: Drone;
  commands: CommandRequest[];
  error?: string | null;
  pendingCommands: Record<string, boolean>;
  onCommand: (action: CommandAction) => void;
}) {
  const canCommand = drone.status === "online" && drone.telemetry?.state === "fresh";
  const latest = commands[0];

  return (
    <div className="grid gap-5 border-t border-atlas-ink/10 pt-4 lg:grid-cols-[18rem_minmax(0,1fr)] xl:grid-cols-[20rem_minmax(0,1fr)]">
      <div>
        <div className="grid max-w-[22rem] grid-cols-2 gap-2">
          {commandActions.map(({ action, label, Icon }) => {
            const pending = pendingCommands[`${drone.id}:${action}`] ?? false;

            return (
              <button
                key={action}
                type="button"
                disabled={!canCommand || pending}
                onClick={() => onCommand(action)}
                className="inline-flex min-h-11 items-center justify-start gap-2 border border-atlas-ink/15 bg-atlas-mist px-4 text-sm font-semibold text-atlas-ink transition hover:border-atlas-ink/35 hover:bg-atlas-panel disabled:cursor-not-allowed disabled:opacity-45"
                title={!canCommand ? "Requires online agent and fresh telemetry" : label}
              >
                {pending ? (
                  <Loader2 aria-hidden="true" className="animate-spin" size={16} />
                ) : (
                  <Icon aria-hidden="true" size={16} />
                )}
                {label}
              </button>
            );
          })}
        </div>
        {error && <p className="mt-3 text-sm text-atlas-signal">{error}</p>}
      </div>

      <div className="min-w-0">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <h4 className="text-xs font-semibold uppercase tracking-[0.12em] text-atlas-ink/55">
            Command lifecycle
          </h4>
          {latest && (
            <span
              className={`max-w-full shrink-0 truncate whitespace-nowrap px-2.5 py-1 text-[11px] font-semibold uppercase tracking-[0.08em] ${
                commandStateStyles[latest.state]
              }`}
            >
              {commandStateLabel(latest.state)}
            </span>
          )}
        </div>

        {latest ? (
          <>
            <div className="mt-3 grid grid-cols-6 gap-1.5 sm:gap-2">
              {lifecycleSteps.map((step) => {
                const complete = isLifecycleStepComplete(latest.state, step.state);
                return (
                  <div
                    key={step.state}
                    title={step.label}
                    className={`min-w-0 overflow-hidden whitespace-nowrap border px-1.5 py-2 text-center text-[11px] font-semibold leading-none sm:px-2 ${
                      complete
                        ? "border-atlas-field/50 bg-atlas-field/15 text-atlas-ink"
                        : "border-atlas-ink/10 text-atlas-ink/45"
                    }`}
                  >
                    {step.shortLabel}
                  </div>
                );
              })}
            </div>
            <div className="mt-3 space-y-2">
              {commands.slice(0, 4).map((command) => (
                <div
                  key={command.id}
                  className="grid gap-2 border-l-2 border-atlas-ink/10 pl-3 text-sm sm:grid-cols-[1fr_auto]"
                >
                  <div className="min-w-0">
                    <p className="truncate font-semibold">
                      {commandTypeLabel(command.type)} · {command.id}
                    </p>
                    <p className="truncate text-atlas-ink/60">
                      {command.resultMessage || command.policyReason || commandStateLabel(command.state)}
                    </p>
                  </div>
                  <p className="text-atlas-ink/55 sm:text-right">
                    {formatTime(command.updatedAt)}
                  </p>
                </div>
              ))}
            </div>
          </>
        ) : (
          <p className="mt-3 text-sm text-atlas-ink/60">No commands requested.</p>
        )}
      </div>
    </div>
  );
}

function TelemetryGrid({ drone }: { drone: Drone }) {
  if (!drone.telemetry) {
    return (
      <div className="border-t border-atlas-ink/10 pt-4 text-sm text-atlas-ink/65">
        Waiting for first telemetry snapshot.
      </div>
    );
  }

  const telemetry = drone.telemetry;

  return (
    <div className="grid gap-3 border-t border-atlas-ink/10 pt-4 sm:grid-cols-2 lg:grid-cols-3">
      <Metric label="Battery" value={`${telemetry.batteryPercent.toFixed(1)}%`} />
      <Metric label="Altitude" value={`${telemetry.relativeAltitudeM.toFixed(1)} m`} />
      <Metric label="Mode" value={telemetry.flightMode} />
      <Metric label="Armed" value={telemetry.armed ? "yes" : "no"} />
      <Metric label="In air" value={telemetry.inAir ? "yes" : "no"} />
      <Metric label="GPS" value={`${telemetry.gpsFix} / ${telemetry.satellitesVisible} sats`} />
      <Metric label="Position" value={`${telemetry.latitude.toFixed(5)}, ${telemetry.longitude.toFixed(5)}`} />
      <Metric label="Heading" value={`${telemetry.headingDeg.toFixed(0)} deg`} />
      <Metric label="Source" value={telemetry.source} />
    </div>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <p className="text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/45">{label}</p>
      <p className="mt-1 truncate text-sm font-semibold text-atlas-ink">{value}</p>
    </div>
  );
}

function MissionExecutionSummary({ execution }: { execution?: MissionExecution }) {
  if (!execution) {
    return (
      <div className="inline-flex min-h-9 items-center gap-2 border border-atlas-ink/10 px-3 text-sm text-atlas-ink/60">
        <History aria-hidden="true" size={15} />
        No active mission
      </div>
    );
  }

  return (
    <div className="inline-flex max-w-full min-h-9 items-center gap-2 border border-atlas-ink/10 px-3 text-sm">
      <span
        className={`shrink-0 px-2 py-1 text-[10px] font-semibold uppercase tracking-[0.08em] ${
          missionStateStyles[execution.state]
        }`}
      >
        {missionStateLabel(execution.state)}
      </span>
      <span className="truncate text-atlas-ink/60">{execution.missionId}</span>
    </div>
  );
}

function missionValidationStyle(status: Mission["validationStatus"]) {
  switch (status) {
    case "validated":
      return "bg-atlas-field/25 text-atlas-ink";
    case "rejected":
      return "bg-atlas-signal/20 text-atlas-ink";
    case "not_validated":
      return "bg-atlas-ink/10 text-atlas-ink/70";
  }
}

function isMissionPath(pathname: string) {
  return (
    pathname === "/missions" ||
    pathname.startsWith("/missions/") ||
    /^\/drones\/[^/]+\/missions(\/.*)?$/.test(pathname)
  );
}

function formatTime(value?: string) {
  if (!value) {
    return "not received";
  }

  return new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(new Date(value));
}

function formatCommandChannelTime(drone: Drone) {
  if (drone.commandChannel.state === "connected") {
    return formatTime(drone.commandChannel.connectedAt);
  }

  return formatTime(drone.commandChannel.lastDisconnectedAt);
}

function mergeCommand(commands: CommandRequest[], command: CommandRequest) {
  return mergeCommands([command], commands);
}

function mergeCommands(primary: CommandRequest[], secondary: CommandRequest[]) {
  const byID = new Map<string, CommandRequest>();
  for (const command of secondary) {
    byID.set(command.id, command);
  }
  for (const command of primary) {
    byID.set(command.id, command);
  }

  const next = Array.from(byID.values());
  return next.sort((a, b) => Date.parse(b.requestedAt) - Date.parse(a.requestedAt)).slice(0, 8);
}

function mergeMissionExecutions(
  primary: MissionExecution[],
  secondary: MissionExecution[],
) {
  const byID = new Map<string, MissionExecution>();
  for (const execution of secondary) {
    byID.set(execution.id, execution);
  }
  for (const execution of primary) {
    byID.set(execution.id, execution);
  }

  return Array.from(byID.values()).sort(
    (first, second) => Date.parse(second.updatedAt) - Date.parse(first.updatedAt),
  );
}

function commandTypeLabel(type: CommandRequest["type"]) {
  switch (type) {
    case "arm":
      return "Arm";
    case "takeoff":
      return "Takeoff";
    case "return_to_launch":
      return "RTL";
    case "land":
      return "Land";
  }
}

function commandStateLabel(state: CommandState) {
  return state.replace(/_/g, " ");
}

function missionStateLabel(state: MissionExecutionState) {
  return state.replace(/_/g, " ");
}

function missionProgressPercent(execution: MissionExecution) {
  if (
    typeof execution.currentMissionItem === "number" &&
    typeof execution.totalMissionItems === "number" &&
    execution.totalMissionItems > 0
  ) {
    return Math.max(
      0,
      Math.min(100, (execution.currentMissionItem / execution.totalMissionItems) * 100),
    );
  }

  if (execution.state === "completed" || execution.state === "hold") {
    return 100;
  }

  return 0;
}

function missionExecutionLocksDefinition(execution?: MissionExecution) {
  switch (execution?.state) {
    case "upload_requested":
    case "uploading":
    case "start_requested":
    case "active":
    case "hold":
    case "paused_or_hold":
    case "rtl_requested":
      return true;
    default:
      return false;
  }
}

function missionExecutionCanAbort(execution?: MissionExecution) {
  switch (execution?.state) {
    case "start_requested":
    case "active":
    case "hold":
    case "paused_or_hold":
      return true;
    default:
      return false;
  }
}

function missionStartGuide(drone: Drone) {
  const execution = drone.missionExecution;
  if (!execution || execution.state !== "uploaded_to_vehicle") {
    return null;
  }

  if (!drone.telemetry?.armed || !drone.telemetry.inAir) {
    return "Start flight will arm, take off, and begin the uploaded mission.";
  }

  return null;
}

function isLifecycleStepComplete(current: CommandState, step: CommandState) {
  const order = lifecycleSteps.map((item) => item.state);
  const currentIndex = order.indexOf(normalizedLifecycleState(current));
  const stepIndex = order.indexOf(step);

  return currentIndex >= stepIndex && stepIndex >= 0;
}

function isMissionLifecycleStepComplete(
  current: MissionExecutionState,
  step: MissionExecutionState,
) {
  const order = missionLifecycleSteps.map((item) => item.state);
  const currentIndex = order.indexOf(normalizedMissionLifecycleState(current));
  const stepIndex = order.indexOf(step);

  return currentIndex >= stepIndex && stepIndex >= 0;
}

function normalizedLifecycleState(state: CommandState) {
  switch (state) {
    case "requested":
    case "authorized":
      return "authorized";
    case "vehicle_rejected":
    case "timed_out":
    case "failed":
      return "sent_to_vehicle";
    default:
      return state;
  }
}

function normalizedMissionLifecycleState(state: MissionExecutionState) {
  switch (state) {
    case "created":
    case "upload_requested":
    case "uploading":
    case "upload_failed":
      return "upload_requested";
    case "uploaded_to_vehicle":
      return "uploaded_to_vehicle";
    case "start_requested":
      return "start_requested";
    case "active":
    case "paused_or_hold":
    case "rtl_requested":
      return "active";
    case "completed":
    case "aborted":
    case "failed":
      return "completed";
    case "hold":
      return "hold";
    case "unknown":
      return "upload_requested";
  }
}
