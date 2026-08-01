import { lazy, Suspense, useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { FleetPage } from "./fleet/FleetPage";
import type { AircraftFollowSession } from "./follow/followTypes";
import { HistoryPage } from "./history/HistoryPage";
import type { MissionRun } from "./missions/missionTypes";
import type { ConnectionStatus, FleetSnapshot, NativeState, Nullable, StatusTone } from "./operationsTypes";
import { CameraWorkspace } from "./video/CameraWorkspace";
import {
  emptyOperationalAlerts,
  OperationalAlertButton,
  OperationalAlertCenter,
  type OperationalAlertList,
} from "./alerts/OperationalAlerts";
import "./AppShell.css";
import "./tokens.css";
import "./AppFrame.css";

const CommandPage = lazy(() => import("./command/CommandPage").then((module) => ({ default: module.CommandPage })));
const MissionPage = lazy(() => import("./missions/MissionPage").then((module) => ({ default: module.MissionPage })));
const MissionHistoryPage = lazy(() => import("./missions/MissionHistoryPage").then((module) => ({ default: module.MissionHistoryPage })));
const MissionExecutionPage = lazy(() => import("./missions/MissionExecutionPage").then((module) => ({ default: module.MissionExecutionPage })));
const OperationsPage = lazy(() => import("./operations/OperationsPage").then((module) => ({ default: module.OperationsPage })));
const EvidencePage = lazy(() => import("./evidence/EvidencePage").then((module) => ({ default: module.EvidencePage })));
const FollowPage = lazy(() => import("./follow/FollowPage").then((module) => ({ default: module.FollowPage })));

type WorkspaceView = "command" | "operations" | "fleet" | "aircraft" | "missions" | "mission-history" | "mission-execution" | "evidence";
type AircraftSection = "overview" | "live" | "follow" | "missions" | "history" | "settings";
type DisplayMode = "desk" | "field";

type GroundStationSnapshot = {
  listenAddress: string;
  connectionStatus: ConnectionStatus;
  droneId?: string | null;
  droneName?: string | null;
  vehicleType?: string | null;
  vehicleStatus?: string | null;
  agentId?: string | null;
  agentVersion?: string | null;
  agentCapabilities: string[];
  bindingId?: string | null;
  communicationLinkId?: string | null;
  sessionId?: string | null;
  remoteAddress?: string | null;
  connectedAtUnixMs?: number | null;
  lastHeartbeatAtUnixMs?: number | null;
  telemetry?: AircraftTelemetry | null;
  statusEvents: StatusEvent[];
};

type BatteryTelemetry = {
  id: number;
  function: string;
  remainingPercent?: number | null;
  voltageV?: number | null;
  currentA?: number | null;
  temperatureC?: number | null;
  consumedAh?: number | null;
  timeRemainingS?: number | null;
};

type VehicleHealth = {
  gyrometerCalibrationOk: boolean;
  accelerometerCalibrationOk: boolean;
  magnetometerCalibrationOk: boolean;
  localPositionOk: boolean;
  globalPositionOk: boolean;
  homePositionOk: boolean;
  armable: boolean;
};

type RcStatus = {
  available: boolean;
  wasAvailableOnce: boolean;
  signalStrengthPercent?: number | null;
};

type HomePosition = {
  latitude?: number | null;
  longitude?: number | null;
  absoluteAltitudeM?: number | null;
  relativeAltitudeM?: number | null;
};

type GpsQuality = {
  hdop?: number | null;
  vdop?: number | null;
  horizontalUncertaintyM?: number | null;
  verticalUncertaintyM?: number | null;
  velocityUncertaintyMps?: number | null;
  courseOverGroundDeg?: number | null;
};

type StatusEvent = {
  id: string;
  source: string;
  severity: string;
  message: string;
  observedAtUnixMs: number;
  receivedAtUnixMs: number;
};

type AircraftTelemetry = {
  status: "live" | "stale";
  source: string;
  observedAtUnixMs: number;
  receivedAtUnixMs: number;
  batteryPercent?: number | null;
  relativeAltitudeM?: number | null;
  flightMode?: string | null;
  armed?: boolean | null;
  inAir?: boolean | null;
  latitude?: number | null;
  longitude?: number | null;
  headingDeg?: number | null;
  groundSpeedMps?: number | null;
  gpsFix?: string | null;
  satellitesVisible?: number | null;
  homePositionSet?: boolean | null;
  batteries: BatteryTelemetry[];
  health?: VehicleHealth | null;
  absoluteAltitudeM?: number | null;
  terrainAltitudeM?: number | null;
  bottomClearanceM?: number | null;
  velocityNorthMps?: number | null;
  velocityEastMps?: number | null;
  velocityDownMps?: number | null;
  climbRateMps?: number | null;
  landedState?: string | null;
  rcStatus?: RcStatus | null;
  homePosition?: HomePosition | null;
  gpsQuality?: GpsQuality | null;
};

type OperatorView = {
  title: string;
  statusLabel: string;
  guidance: string;
  stateDetail: string;
  tone: StatusTone;
};

const emptySnapshot: GroundStationSnapshot = {
  listenAddress: "192.168.144.50:7443",
  connectionStatus: "disconnected",
  statusEvents: [],
  agentCapabilities: [],
};

function App() {
  const [snapshot, setSnapshot] = useState<GroundStationSnapshot>(emptySnapshot);
  const [fleet, setFleet] = useState<FleetSnapshot>({ generatedAtUnixMs: 0, aircraft: [] });
  const [nativeState, setNativeState] = useState<NativeState>("starting");
  const [workspaceView, setWorkspaceView] = useState<WorkspaceView>("command");
  const [selectedDroneId, setSelectedDroneId] = useState<string>();
  const [selectedMissionId, setSelectedMissionId] = useState<string>();
  const [missionDraftId, setMissionDraftId] = useState<string>();
  const [missionOrigin, setMissionOrigin] = useState<"missions" | "mission-history" | "operations">("missions");
  const [aircraftSection, setAircraftSection] = useState<AircraftSection>("overview");
  const [showArchived, setShowArchived] = useState(false);
  const [alerts, setAlerts] = useState<OperationalAlertList>(emptyOperationalAlerts);
  const [alertsOpen, setAlertsOpen] = useState(false);
  const [alertError, setAlertError] = useState<string>();
  const [pendingAlertId, setPendingAlertId] = useState<string>();
  const [operationalMissionRuns, setOperationalMissionRuns] = useState<MissionRun[]>([]);
  const [followSessions, setFollowSessions] = useState<AircraftFollowSession[]>([]);
  const [authorityPending, setAuthorityPending] = useState(false);
  const [authorityActionError, setAuthorityActionError] = useState<string>();
  const [authorityRefreshError, setAuthorityRefreshError] = useState<string>();
  const [authorityUpdatedAtUnixMs, setAuthorityUpdatedAtUnixMs] = useState<number>();
  const [displayMode, setDisplayMode] = useState<DisplayMode>(initialDisplayMode);

  useEffect(() => {
    let active = true;

    async function refresh() {
      try {
        const [nextFleet, nextSnapshot] = await Promise.all([
          invoke<FleetSnapshot>("fleet_snapshot", { includeArchived: showArchived }),
          selectedDroneId
            ? invoke<GroundStationSnapshot>("vehicle_operations_snapshot", { droneId: selectedDroneId })
            : invoke<GroundStationSnapshot>("ground_station_snapshot"),
        ]);
        if (active) {
          setFleet(nextFleet);
          setSnapshot(nextSnapshot);
          setNativeState("available");
        }
      } catch {
        if (active) setNativeState("unavailable");
      }
    }

    void refresh();
    const interval = window.setInterval(refresh, 1000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [selectedDroneId, showArchived]);

  useEffect(() => {
    if (nativeState !== "available") return;
    let active = true;
    async function refreshAlerts() {
      try {
        const next = await invoke<OperationalAlertList>("operational_alerts", {
          includeHistory: true,
          limit: 100,
        });
        if (active) {
          setAlerts(next);
          setAlertError(undefined);
        }
      } catch (reason) {
        if (active) setAlertError(reason instanceof Error ? reason.message : String(reason));
      }
    }
    void refreshAlerts();
    const interval = window.setInterval(refreshAlerts, 2_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [nativeState]);

  useEffect(() => {
    document.documentElement.dataset.mode = displayMode;
    try {
      window.localStorage.setItem("atlas.displayMode", displayMode);
    } catch {
      // The selected mode still applies for this session when storage is unavailable.
    }
  }, [displayMode]);

  useEffect(() => {
    if (nativeState !== "available") {
      setOperationalMissionRuns([]);
      setFollowSessions([]);
      setAuthorityRefreshError(undefined);
      setAuthorityUpdatedAtUnixMs(undefined);
      return;
    }
    let active = true;
    let reading = false;
    async function refreshOperationalAuthority() {
      if (reading) return;
      reading = true;
      try {
        const [nextMissionRuns, nextFollowSessions] = await Promise.all([
          invoke<MissionRun[]>("mission_run_history", { limit: 100 }),
          invoke<AircraftFollowSession[]>("aircraft_follow_sessions", {
            includeEnded: false,
            limit: 50,
          }),
        ]);
        if (active) {
          setOperationalMissionRuns(nextMissionRuns);
          setFollowSessions(nextFollowSessions);
          setAuthorityRefreshError(undefined);
          setAuthorityUpdatedAtUnixMs(Date.now());
        }
      } catch (reason) {
        if (active) {
          setAuthorityRefreshError(reason instanceof Error ? reason.message : String(reason));
        }
      } finally {
        reading = false;
      }
    }
    void refreshOperationalAuthority();
    const interval = window.setInterval(refreshOperationalAuthority, 2_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [nativeState]);

  async function acknowledgeAlert(alertId: string) {
    if (pendingAlertId) return;
    setPendingAlertId(alertId);
    setAlertError(undefined);
    try {
      await invoke("acknowledge_operational_alert", { alertId });
      const next = await invoke<OperationalAlertList>("operational_alerts", {
        includeHistory: true,
        limit: 100,
      });
      setAlerts(next);
    } catch (reason) {
      setAlertError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setPendingAlertId(undefined);
    }
  }

  async function stopActiveFollow(session: AircraftFollowSession) {
    if (authorityPending) return;
    setAuthorityPending(true);
    setAuthorityActionError(undefined);
    try {
      const updated = await invoke<AircraftFollowSession>("end_aircraft_follow_session", {
        input: {
          sessionId: session.id,
          reason: session.state === "DEGRADED_HOLD"
            ? "Operator ended the held follow session from the persistent control"
            : "Operator requested immediate Stop Follow and PX4 Hold from the persistent control",
          actor: "operator",
        },
      });
      setFollowSessions((current) => current.map(
        (candidate) => candidate.id === updated.id ? updated : candidate,
      ));
    } catch (reason) {
      setAuthorityActionError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setAuthorityPending(false);
    }
  }

  const heartbeat = formatRelativeTime(snapshot.lastHeartbeatAtUnixMs);
  const view = operatorView(snapshot, nativeState, heartbeat);
  const operationalAircraft = fleet.aircraft.filter((aircraft) => aircraft.vehicleStatus !== "archived");
  const visibleAircraft = showArchived
    ? fleet.aircraft.filter((aircraft) => aircraft.vehicleStatus === "archived")
    : operationalAircraft;
  const hasAircraft = Boolean(snapshot.droneId || snapshot.droneName);
  const sessionState = nativeState !== "available"
    ? nativeState === "starting" ? "Checking" : "Unknown"
    : snapshot.sessionId
    ? snapshot.connectionStatus === "disconnected" ? "Closed" : "Active"
    : "None";
  const agentValue = nativeState !== "available"
    ? nativeState === "starting" ? "Checking" : "Unknown"
    : snapshot.agentVersion ? `Version ${snapshot.agentVersion}` : "Not detected";
  const agentDetail = nativeState !== "available"
    ? "Waiting for local services"
    : snapshot.agentId ? compactIdentifier(snapshot.agentId) : "Waiting for agent identity";
  const heartbeatValue = nativeState === "available" ? heartbeat : sessionState;
  const heartbeatStatusDetail = nativeState === "available"
    ? heartbeatDetail(snapshot.connectionStatus, snapshot.lastHeartbeatAtUnixMs)
    : "Live state is not available";
  const groundLinkDetail = nativeState === "available"
    ? snapshot.remoteAddress || "No remote endpoint"
    : "Live state is not available";
  const sessionDetail = nativeState === "available"
    ? snapshot.sessionId ? compactIdentifier(snapshot.sessionId) : "No active session"
    : "Live state is not available";
  const selectedAircraft = fleet.aircraft.find((aircraft) => aircraft.droneId === selectedDroneId);
  const activeFollowSessions = followSessions
    .filter((session) => session.state !== "ENDED")
    .sort((left, right) => right.updatedAtUnixMs - left.updatedAtUnixMs);
  const activeMissionRuns = operationalMissionRuns
    .filter((run) => !run.completedAtUnixMs && ["RUNNING", "PAUSED", "ROUTE_COMPLETE", "RTL"].includes(run.status))
    .sort((left, right) => right.updatedAtUnixMs - left.updatedAtUnixMs);
  const primaryFollow = activeFollowSessions[0];
  const primaryMission = activeMissionRuns[0];
  const primaryMissionIsHeldOnScene = primaryMission?.status === "ROUTE_COMPLETE";
  const selectedFollow = activeFollowSessions.find((session) => session.droneId === selectedDroneId);
  const selectedMissionRun = activeMissionRuns.find((run) => run.droneId === selectedDroneId);
  const activeAuthorityCount = activeFollowSessions.length + activeMissionRuns.length;
  const additionalAuthorityCopy = activeAuthorityCount > 1
    ? ` · ${activeAuthorityCount - 1} more under control`
    : "";
  const criticalAlert = alerts.alerts
    .filter((alert) => alert.severity === "CRITICAL" && (alert.state === "ACTIVE" || alert.state === "ACKNOWLEDGED"))
    .sort((left, right) => right.lastSeenAtUnixMs - left.lastSeenAtUnixMs)[0];

  return (
    <div className="operations-shell" data-mode={displayMode}>
      <header className="operations-header">
        <BrandMark />
        <nav className="workspace-nav" aria-label="Atlas workspace">
          <button
            type="button"
            className={workspaceView === "command" ? "workspace-nav__active" : undefined}
            aria-current={workspaceView === "command" ? "page" : undefined}
            onClick={() => {
              setSelectedDroneId(undefined);
              setWorkspaceView("command");
            }}
          >
            Command
          </button>
          <button
            type="button"
            className={workspaceView === "operations" || (workspaceView === "mission-execution" && missionOrigin === "operations") ? "workspace-nav__active" : undefined}
            aria-current={workspaceView === "operations" || (workspaceView === "mission-execution" && missionOrigin === "operations") ? "page" : undefined}
            onClick={() => {
              setSelectedDroneId(undefined);
              setWorkspaceView("operations");
            }}
          >
            Dispatch
          </button>
          <button
            type="button"
            className={["fleet", "aircraft"].includes(workspaceView) ? "workspace-nav__active" : undefined}
            aria-current={["fleet", "aircraft"].includes(workspaceView) ? "page" : undefined}
            onClick={() => setWorkspaceView("fleet")}
          >
            Aircraft
          </button>
          <button
            type="button"
            className={workspaceView === "missions" || workspaceView === "mission-history" || (workspaceView === "mission-execution" && missionOrigin !== "operations") ? "workspace-nav__active" : undefined}
            aria-current={workspaceView === "missions" || workspaceView === "mission-history" || (workspaceView === "mission-execution" && missionOrigin !== "operations") ? "page" : undefined}
            onClick={() => {
              setMissionDraftId(undefined);
              setWorkspaceView("missions");
            }}
          >
            Plan
          </button>
          <button
            type="button"
            className={workspaceView === "evidence" ? "workspace-nav__active" : undefined}
            aria-current={workspaceView === "evidence" ? "page" : undefined}
            onClick={() => {
              setSelectedDroneId(undefined);
              setWorkspaceView("evidence");
            }}
          >
            Captures
          </button>
        </nav>
        <div className="operations-header__spacer" />
        <div className="header-operational-state">
          {(primaryFollow || primaryMission) && (
            <div className="persistent-authority" role="status" aria-live="polite">
              <button
                type="button"
                className="persistent-authority__summary"
                aria-label={`Open Command with ${
                  primaryFollow
                    ? aircraftName(fleet.aircraft, primaryFollow.droneId)
                    : primaryMission?.droneName || "aircraft"
                } selected`}
                onClick={() => {
                  setSelectedDroneId(primaryFollow?.droneId ?? primaryMission?.droneId);
                  setWorkspaceView("command");
                }}
              >
                <span className="persistent-authority__dot" aria-hidden="true" />
                <span>
                  <strong>
                    {primaryFollow
                      ? `${aircraftName(fleet.aircraft, primaryFollow.droneId)} is following a target`
                      : primaryMissionIsHeldOnScene
                        ? `${primaryMission?.droneName || "Aircraft"} is holding after completing the route for ${primaryMission?.missionName || "a mission"}`
                        : `${primaryMission?.droneName || "Aircraft"} is flying ${primaryMission?.missionName || "a mission"}`}
                  </strong>
                  <small>
                    {primaryFollow
                      ? `${
                          primaryFollow.state === "DEGRADED_HOLD"
                            ? "Follow stopped · holding"
                            : "Offboard · Atlas is flying it"
                        }${additionalAuthorityCopy}`
                      : primaryMissionIsHeldOnScene
                        ? `Route complete · Land or RTL required${additionalAuthorityCopy}`
                        : `${displayEnum(primaryMission?.status)}${additionalAuthorityCopy}`}
                  </small>
                </span>
              </button>
              <button
                type="button"
                disabled={authorityPending}
                className={primaryFollow ? "persistent-authority__stop" : undefined}
                onClick={() => {
                  if (primaryFollow) {
                    void stopActiveFollow(primaryFollow);
                  } else if (primaryMission) {
                    setSelectedMissionId(primaryMission.missionId);
                    setSelectedDroneId(primaryMission.droneId);
                    setMissionOrigin("missions");
                    setWorkspaceView("mission-execution");
                  }
                }}
              >
                {primaryFollow ? authorityPending ? "Stopping…" : "Stop" : "Open mission"}
              </button>
            </div>
          )}
          {authorityRefreshError && (
            <div className="authority-freshness" title={authorityRefreshError}>
              <span aria-hidden="true">!</span>
              <span>
                <strong>Mission and follow status stale</strong>
                <small>{authorityFreshnessCopy(authorityUpdatedAtUnixMs)}</small>
              </span>
            </div>
          )}
          {authorityActionError && (
            <div className="authority-action-error" role="alert">
              <strong>Control action failed.</strong>
              <span>{authorityActionError}</span>
            </div>
          )}
          <OperationalAlertButton
            alerts={alerts}
            expanded={alertsOpen}
            onClick={() => setAlertsOpen((open) => !open)}
          />
          <div className="display-mode-toggle" role="group" aria-label="Display mode">
            <button type="button" aria-pressed={displayMode === "desk"} onClick={() => setDisplayMode("desk")}>Desk</button>
            <button type="button" aria-pressed={displayMode === "field"} onClick={() => setDisplayMode("field")}>Field</button>
          </div>
        </div>
      </header>

      {criticalAlert && (
        <div className="critical-ribbon" role="alert">
          <span>Needs you</span>
          <p>
            <strong>{criticalAlert.title}</strong>
            {" "}
            {criticalAlert.recommendedAction}
          </p>
          <button type="button" onClick={() => setAlertsOpen(true)}>Review alert</button>
        </div>
      )}

      {alertsOpen && (
        <OperationalAlertCenter
          alerts={alerts}
          pendingAlertId={pendingAlertId}
          error={alertError}
          onAcknowledge={(alertId) => void acknowledgeAlert(alertId)}
          onClose={() => setAlertsOpen(false)}
        />
      )}

      {workspaceView === "command" ? (
        <Suspense fallback={<main className="workspace-loading" id="main-content"><p>Opening Command…</p></main>}>
          <CommandPage
            nativeAvailable={nativeState === "available"}
            fleet={{ ...fleet, aircraft: operationalAircraft }}
            alerts={alerts}
            missionRuns={operationalMissionRuns}
            followSessions={followSessions}
            preferredDroneId={selectedDroneId}
            onOpenAircraft={(droneId) => {
              setSelectedDroneId(droneId);
              setAircraftSection("overview");
              setWorkspaceView("aircraft");
            }}
            onOpenDispatch={() => setWorkspaceView("operations")}
            onOpenFollow={() => {
              setSelectedDroneId(primaryFollow?.droneId ?? operationalAircraft[0]?.droneId);
              setAircraftSection("follow");
              setWorkspaceView("aircraft");
            }}
            onOpenMission={(missionId, droneId) => {
              setSelectedMissionId(missionId);
              setSelectedDroneId(droneId);
              setMissionOrigin("missions");
              setWorkspaceView("mission-execution");
            }}
          />
        </Suspense>
      ) : workspaceView === "operations" ? (
        <Suspense fallback={<main className="workspace-loading" id="main-content"><p>Loading operational map…</p></main>}>
          <OperationsPage
            nativeAvailable={nativeState === "available"}
            fleet={{ ...fleet, aircraft: operationalAircraft }}
            alerts={alerts}
            onOpenAircraft={(droneId) => {
              setSelectedDroneId(droneId);
              setAircraftSection("overview");
              setWorkspaceView("aircraft");
            }}
            onConfirmResponse={(missionId, droneId) => {
              setSelectedMissionId(missionId);
              setSelectedDroneId(droneId);
              setMissionOrigin("operations");
              setWorkspaceView("mission-execution");
            }}
          />
        </Suspense>
      ) : workspaceView === "fleet" ? (
        <FleetPage
          aircraft={visibleAircraft}
          generatedAtUnixMs={fleet.generatedAtUnixMs}
          nativeState={nativeState}
          listenAddress={snapshot.listenAddress}
          showArchived={showArchived}
          onShowArchivedChange={setShowArchived}
          onOpenAircraft={(droneId) => {
            setSelectedDroneId(droneId);
            setAircraftSection(
              visibleAircraft.find((aircraft) => aircraft.droneId === droneId)?.vehicleStatus === "archived"
                ? "settings"
                : "overview",
            );
            setWorkspaceView("aircraft");
          }}
          onOpenHistory={(droneId) => {
            setSelectedDroneId(droneId);
            setAircraftSection("history");
            setWorkspaceView("aircraft");
          }}
        />
      ) : workspaceView === "missions" ? (
        <Suspense fallback={<main className="workspace-loading" id="main-content"><p>Loading mission map…</p></main>}>
          <MissionPage
            nativeAvailable={nativeState === "available"}
            fleetAircraft={operationalAircraft}
            preferredDroneId={selectedDroneId}
            initialMissionId={missionDraftId}
            onInitialMissionLoaded={() => setMissionDraftId(undefined)}
            onMissionReady={(missionId, droneId) => {
              setMissionDraftId(undefined);
              setSelectedMissionId(missionId);
              if (droneId) setSelectedDroneId(droneId);
              setMissionOrigin("missions");
              setWorkspaceView("mission-execution");
            }}
            onOpenHistory={() => setWorkspaceView("mission-history")}
          />
        </Suspense>
      ) : workspaceView === "mission-history" ? (
        <Suspense fallback={<main className="workspace-loading" id="main-content"><p>Loading mission history…</p></main>}>
          <MissionHistoryPage
            nativeAvailable={nativeState === "available"}
            onBack={() => {
              setMissionDraftId(undefined);
              setWorkspaceView("missions");
            }}
            onEditMission={(missionId) => {
              setMissionDraftId(missionId);
              setWorkspaceView("missions");
            }}
            onOpenMission={(missionId, droneId) => {
              setSelectedMissionId(missionId);
              if (droneId) setSelectedDroneId(droneId);
              setMissionOrigin("mission-history");
              setWorkspaceView("mission-execution");
            }}
          />
        </Suspense>
      ) : workspaceView === "mission-execution" && selectedMissionId ? (
        <Suspense fallback={<main className="workspace-loading" id="main-content"><p>Loading mission execution…</p></main>}>
          <MissionExecutionPage
            nativeAvailable={nativeState === "available"}
            missionId={selectedMissionId}
            preferredDroneId={selectedDroneId}
            lockedDroneId={missionOrigin === "operations" ? selectedDroneId : undefined}
            backLabel={missionOrigin === "operations" ? "Dispatch" : missionOrigin === "mission-history" ? "Mission history" : "Plan"}
            alerts={alerts}
            onBack={() => setWorkspaceView(missionOrigin)}
          />
        </Suspense>
      ) : workspaceView === "evidence" ? (
        <Suspense fallback={<main className="workspace-loading" id="main-content"><p>Opening captures…</p></main>}>
          <EvidencePage nativeAvailable={nativeState === "available"} />
        </Suspense>
      ) : (
      <main className="operations-main" id="main-content">
        <section className="aircraft-camera-context" aria-label="Selected aircraft">
          <label>
            <span>Aircraft</span>
            <select value={selectedDroneId ?? ""} onChange={(event) => setSelectedDroneId(event.target.value || undefined)}>
              {!operationalAircraft.length && <option value="">No operational aircraft</option>}
              {operationalAircraft.map((aircraft) => (
                <option key={aircraft.droneId ?? aircraft.droneName ?? "unknown"} value={aircraft.droneId ?? ""}>
                  {aircraft.droneName || "Unnamed aircraft"} — {aircraftContextState(aircraft)}
                </option>
              ))}
            </select>
          </label>
          <p>{aircraftSection === "live" || aircraftSection === "follow" ? cameraContextSummary(selectedAircraft) : `${view.title} · ${view.statusLabel}`}</p>
          <button type="button" onClick={() => setWorkspaceView("command")}>← Command</button>
        </section>

        <nav className="aircraft-section-nav" aria-label={`${view.title} workspace`}>
          {(["overview", "live", "follow", "missions", "history", "settings"] as AircraftSection[]).map((section) => (
            <button key={section} type="button" className={aircraftSection === section ? "aircraft-section-nav__active" : undefined} aria-current={aircraftSection === section ? "page" : undefined} onClick={() => setAircraftSection(section)}>
              {section === "live" ? "Camera" : section === "follow" ? "Aircraft follow" : displayEnum(section)}
            </button>
          ))}
        </nav>

        <div className={`aircraft-section-content aircraft-section-content--${aircraftSection}`}>
        {aircraftSection === "overview" && <>
        <section className="status-grid" aria-label="Live aircraft status">
          <StatusItem
            label="Ground link"
            value={view.statusLabel}
            detail={groundLinkDetail}
            tone={view.tone}
          />
          <StatusItem
            label="Onboard agent"
            value={agentValue}
            detail={agentDetail}
            tone={nativeState === "available" && snapshot.agentId ? "positive" : "neutral"}
          />
          <StatusItem
            label="Last heartbeat"
            value={heartbeatValue}
            detail={heartbeatStatusDetail}
            tone={nativeState === "available"
              ? heartbeatTone(snapshot.connectionStatus, snapshot.lastHeartbeatAtUnixMs)
              : "neutral"}
          />
          <StatusItem
            label="Session"
            value={sessionState}
            detail={sessionDetail}
            tone={sessionState === "Active" ? view.tone : "neutral"}
          />
        </section>

        {hasAircraft && nativeState === "available" && (
          <>
            <TelemetryPanel telemetry={snapshot.telemetry} authority={aircraftAuthoritySummary(selectedFollow, selectedMissionRun)} />
            <StatusEventFeed events={snapshot.statusEvents} />
          </>
        )}

        {!hasAircraft && nativeState === "available" && <ConnectionGuide />}
        {nativeState === "unavailable" && <RecoveryNotice />}

        <ConnectionDetails snapshot={snapshot} />
        </>}

        {aircraftSection === "live" && (
          <CameraWorkspace
            nativeAvailable={nativeState === "available"}
            aircraft={selectedAircraft}
            activeFollow={selectedFollow}
            stopPending={authorityPending}
            onOpenFollow={() => setAircraftSection("follow")}
            onStopFollow={(session) => void stopActiveFollow(session)}
            onOpenCommand={() => setWorkspaceView("command")}
          />
        )}

        {aircraftSection === "follow" && (
          <Suspense fallback={<div className="workspace-loading"><p>Opening supervised follow…</p></div>}>
            <FollowPage
              nativeAvailable={nativeState === "available"}
              fleet={{ ...fleet, aircraft: operationalAircraft }}
            />
          </Suspense>
        )}

        {aircraftSection === "missions" && snapshot.droneId && (
          <AircraftMissionRuns
            droneId={snapshot.droneId}
            nativeAvailable={nativeState === "available"}
            onOpenMission={(missionId) => {
              setSelectedMissionId(missionId);
              setWorkspaceView("mission-execution");
            }}
            onPlanMission={() => setWorkspaceView("missions")}
          />
        )}

        {aircraftSection === "settings" && (
          <AircraftSettings
            snapshot={snapshot}
            onLifecycleChanged={(operations) => setSnapshot((current) => ({ ...current, ...operations }))}
          />
        )}
        {aircraftSection === "history" && (
          <HistoryPage
            droneId={selectedDroneId}
            droneName={snapshot.droneId === selectedDroneId ? snapshot.droneName : undefined}
            nativeAvailable={nativeState === "available"}
            onOpenDroneHistory={setSelectedDroneId}
            onBackToOverview={() => setAircraftSection("overview")}
          />
        )}
        </div>
      </main>
      )}

      <footer className="operations-footer" aria-hidden="true" />
    </div>
  );
}

function AircraftMissionRuns({
  droneId,
  nativeAvailable,
  onOpenMission,
  onPlanMission,
}: {
  droneId: string;
  nativeAvailable: boolean;
  onOpenMission: (missionId: string) => void;
  onPlanMission: () => void;
}) {
  const [runs, setRuns] = useState<MissionRun[]>([]);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (!nativeAvailable) return;
    let active = true;
    async function refresh() {
      try {
        const next = await invoke<MissionRun[]>("mission_run_history", { droneId, limit: 30 });
        if (active) {
          setRuns(next);
          setError(undefined);
        }
      } catch (reason) {
        if (active) setError(reason instanceof Error ? reason.message : String(reason));
      }
    }
    void refresh();
    const interval = window.setInterval(refresh, 2_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [droneId, nativeAvailable]);

  const activeRun = runs.find((run) => !run.completedAtUnixMs && !["COMPLETED", "FAILED", "CANCELLED"].includes(run.status));
  const previousRuns = runs.filter((run) => run !== activeRun);

  return (
    <section className="aircraft-missions" aria-labelledby="aircraft-missions-title">
      <header>
        <div>
          <p className="eyebrow">Aircraft assignment</p>
          <h2 id="aircraft-missions-title">Missions</h2>
        </div>
        <button type="button" onClick={onPlanMission}>Plan a mission</button>
      </header>
      {error && <p className="aircraft-workspace-error" role="alert">{error}</p>}
      {activeRun ? (
        <article className="aircraft-active-run">
          <div><span>Current assignment</span><strong>{activeRun.missionName}</strong><small>{displayEnum(activeRun.status)} · {Math.round(activeRun.progressPercent)}% complete</small></div>
          <button type="button" onClick={() => onOpenMission(activeRun.missionId)}>Open execution</button>
        </article>
      ) : (
        <p className="aircraft-workspace-empty">No mission is currently assigned. Inspection controls remain aircraft-owned until a mission begins.</p>
      )}
      <div className="aircraft-run-history">
        <header><strong>Previous runs</strong><span>{previousRuns.length}</span></header>
        {previousRuns.length > 0 ? previousRuns.map((run) => (
          <button key={run.id} type="button" onClick={() => onOpenMission(run.missionId)}>
            <span><strong>{run.missionName}</strong><small>{formatDateTime(run.createdAtUnixMs)}</small></span>
            <span>{displayEnum(run.status)} · {Math.round(run.progressPercent)}%</span>
          </button>
        )) : <p>No previous mission runs are stored for this aircraft.</p>}
      </div>
    </section>
  );
}

function AircraftSettings({
  snapshot,
  onLifecycleChanged,
}: {
  snapshot: GroundStationSnapshot;
  onLifecycleChanged: (snapshot: Partial<GroundStationSnapshot>) => void;
}) {
  const [confirmingArchive, setConfirmingArchive] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [archiveReason, setArchiveReason] = useState("");
  const archived = snapshot.vehicleStatus === "archived";
  const connected = snapshot.connectionStatus === "connected";

  async function changeLifecycle(action: "archive" | "restore") {
    if (!snapshot.droneId || busy) return;
    setBusy(true);
    setError(undefined);
    setNotice(undefined);
    try {
      const operations = await invoke<Partial<GroundStationSnapshot>>(
        action === "archive" ? "archive_drone" : "restore_drone",
        action === "archive"
          ? { droneId: snapshot.droneId, reason: archiveReason.trim() }
          : { droneId: snapshot.droneId },
      );
      onLifecycleChanged(operations);
      setConfirmingArchive(false);
      setArchiveReason("");
      setNotice(action === "archive"
        ? "Aircraft archived. Missions, telemetry, events, and command history remain available."
        : "Aircraft restored. It will remain disconnected until Atlas Agent registers again.");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="aircraft-settings" aria-labelledby="aircraft-settings-title">
      <header>
        <p className="eyebrow">Aircraft lifecycle</p>
        <h2 id="aircraft-settings-title">Settings</h2>
        <p>Identity and lifecycle actions affect this aircraft record, not its retained missions, telemetry, events, or command history.</p>
      </header>
      <dl>
        <div><dt>Name</dt><dd>{snapshot.droneName || "Not reported"}</dd></div>
        <div><dt>Aircraft ID</dt><dd>{snapshot.droneId || "Not reported"}</dd></div>
        <div><dt>Vehicle type</dt><dd>{displayEnum(snapshot.vehicleType)}</dd></div>
        <div><dt>Lifecycle</dt><dd>{displayEnum(snapshot.vehicleStatus)}</dd></div>
        <div><dt>Binding</dt><dd>{snapshot.bindingId || "No binding"}</dd></div>
      </dl>
      <section className="aircraft-lifecycle-action">
        <div>
          <strong>{archived ? "Restore aircraft" : "Archive aircraft"}</strong>
          <p>{archived
            ? "Restoring makes this identity eligible to reconnect. It does not fabricate a link or binding."
            : "Archiving is available only after the communication link is disconnected. Operational evidence is retained."}</p>
        </div>
        {archived ? (
          <button type="button" disabled={busy} onClick={() => void changeLifecycle("restore")}>
            {busy ? "Restoring…" : "Restore aircraft"}
          </button>
        ) : confirmingArchive ? (
          <div className="aircraft-lifecycle-confirmation">
            <strong>Archive {snapshot.droneName || "this aircraft"}</strong>
            <ul className="aircraft-lifecycle-effects">
              <li data-effect="kept">Flights, telemetry, messages, and evidence stay searchable.</li>
              <li data-effect="kept">You can restore this aircraft at any time.</li>
              <li data-effect="changed">It disappears from Command, Dispatch, and Plan.</li>
              <li data-effect="changed">It cannot reconnect until you restore it.</li>
            </ul>
            <label>
              Why are you archiving it?
              <textarea
                value={archiveReason}
                maxLength={500}
                placeholder="For example: returned to the supplier for repair"
                onChange={(event) => setArchiveReason(event.target.value)}
              />
            </label>
            <div>
              <button type="button" disabled={busy} onClick={() => {
                setConfirmingArchive(false);
                setArchiveReason("");
              }}>Keep aircraft</button>
              <button type="button" className="aircraft-lifecycle-danger" disabled={busy || !archiveReason.trim()} onClick={() => void changeLifecycle("archive")}>
                {busy ? "Archiving…" : "Archive aircraft"}
              </button>
            </div>
          </div>
        ) : (
          <button type="button" disabled={connected || busy || !snapshot.droneId} onClick={() => {
            setArchiveReason("");
            setConfirmingArchive(true);
          }}>
            {connected ? "Disconnect before archiving" : "Archive aircraft"}
          </button>
        )}
      </section>
      {error && <p className="aircraft-workspace-error" role="alert">{error}</p>}
      {notice && <p className="aircraft-workspace-notice" role="status">{notice}</p>}
    </section>
  );
}

function formatDateTime(value: number) {
  return new Intl.DateTimeFormat(undefined, { day: "2-digit", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" }).format(value);
}

function TelemetryPanel({ telemetry, authority }: { telemetry?: AircraftTelemetry | null; authority: string }) {
  if (!telemetry) {
    return (
      <section className="telemetry-section telemetry-section--empty" aria-labelledby="telemetry-title">
        <div>
          <p className="eyebrow">Flight data</p>
          <h2 id="telemetry-title">Waiting for telemetry</h2>
        </div>
        <p>The onboard agent is connected, but MAVSDK has not reported flight data yet.</p>
      </section>
    );
  }

  const freshnessTone: StatusTone = telemetry.status === "live" ? "positive" : "warning";
  const primaryBattery = selectPrimaryBattery(telemetry.batteries);
  const primaryMetrics = [
    ["Flight state", flightState(telemetry.armed, telemetry.inAir, telemetry.landedState)],
    ["Authority", authority],
    ["Flight mode", displayEnum(telemetry.flightMode)],
    ["Battery", formatMeasurement(primaryBattery?.remainingPercent ?? telemetry.batteryPercent, 0, "%")],
    ["Relative altitude", formatMeasurement(telemetry.relativeAltitudeM, 1, " m")],
    ["Ground speed", formatMeasurement(telemetry.groundSpeedMps, 1, " m/s")],
    ["GPS", gpsStatus(telemetry.gpsFix, telemetry.satellitesVisible)],
    ["Link", rcStatus(telemetry.rcStatus)],
  ];
  const diagnosticMetrics = [
    ["Heading", formatMeasurement(telemetry.headingDeg, 0, "°")],
    ["Absolute altitude", formatMeasurement(telemetry.absoluteAltitudeM, 1, " m")],
    ["Terrain altitude", formatMeasurement(telemetry.terrainAltitudeM, 1, " m")],
    ["Bottom clearance", formatMeasurement(telemetry.bottomClearanceM, 1, " m")],
    ["Climb rate", formatSignedMeasurement(telemetry.climbRateMps, 1, " m/s")],
    ["GPS precision", gpsPrecision(telemetry.gpsQuality)],
    ["NED velocity", nedVelocity(telemetry)],
    ["Position", position(telemetry.latitude, telemetry.longitude)],
    ["Home", homeStatus(telemetry.homePositionSet, telemetry.homePosition)],
  ];

  return (
    <section className="telemetry-section" aria-labelledby="telemetry-title">
      <header className="telemetry-header">
        <div>
          <p className="eyebrow">Flight data</p>
          <h2 id="telemetry-title">Current telemetry</h2>
        </div>
        <div className={`telemetry-freshness telemetry-freshness--${freshnessTone}`}>
          <span className="state-dot" aria-hidden="true" />
          <span>
            <strong>{telemetry.status === "live" ? "Live" : "Stale"}</strong>
            <small>Received {formatRelativeTime(telemetry.receivedAtUnixMs).toLowerCase()}</small>
          </span>
        </div>
      </header>
      <div className="telemetry-grid">
        {primaryMetrics.map(([label, value]) => (
          <article key={label}>
            <p>{label}</p>
            <strong>{value}</strong>
          </article>
        ))}
      </div>
      <div className="telemetry-support telemetry-support--exceptions">
        <PreflightHealth health={telemetry.health} />
      </div>
      <details className="telemetry-diagnostics">
        <summary><span>Diagnostics</span><small>Precision, terrain, position, velocity, and electrical data</small></summary>
        <div className="telemetry-grid telemetry-grid--diagnostics">
          {diagnosticMetrics.map(([label, value]) => (
            <article key={label}>
              <p>{label}</p>
              <strong>{value}</strong>
            </article>
          ))}
        </div>
        <BatterySummary batteries={telemetry.batteries} />
      </details>
    </section>
  );
}

function PreflightHealth({ health }: { health?: VehicleHealth | null }) {
  const checks = health ? [
    ["Armable", health.armable],
    ["Gyroscope", health.gyrometerCalibrationOk],
    ["Accelerometer", health.accelerometerCalibrationOk],
    ["Magnetometer", health.magnetometerCalibrationOk],
    ["Local position", health.localPositionOk],
    ["Global position", health.globalPositionOk],
    ["Home position", health.homePositionOk],
  ] as const : [];
  const failedChecks = checks.filter(([, ready]) => !ready);

  return (
    <section className="telemetry-support-group" aria-labelledby="preflight-health-title">
      <div className="support-heading">
        <h3 id="preflight-health-title">Preflight health</h3>
        {health && <span>{health.armable ? "Ready to arm" : "Attention required"}</span>}
      </div>
      {health ? (
        <>
          {failedChecks.length > 0 ? (
            <HealthCheckList checks={failedChecks} className="health-list--exceptions" />
          ) : (
            <p className="preflight-clear"><span aria-hidden="true">✓</span> No failed preflight checks</p>
          )}
          <details className="preflight-details">
            <summary>{failedChecks.length > 0 ? `Show all ${checks.length} checks` : `All ${checks.length} checks ready`}</summary>
            <HealthCheckList checks={checks} />
          </details>
        </>
      ) : (
        <p className="support-empty">Waiting for MAVSDK health checks.</p>
      )}
    </section>
  );
}

function HealthCheckList({ checks, className = "" }: {
  checks: readonly (readonly [string, boolean])[];
  className?: string;
}) {
  return (
    <ul className={`health-list ${className}`.trim()}>
      {checks.map(([label, ready]) => (
        <li key={label} className={ready ? "health-check--ready" : "health-check--attention"}>
          <span className="health-marker" aria-hidden="true">{ready ? "✓" : "!"}</span>
          <span>{label}</span>
          <strong>{ready ? "Ready" : "Check"}</strong>
        </li>
      ))}
    </ul>
  );
}

function BatterySummary({ batteries }: { batteries: BatteryTelemetry[] }) {
  return (
    <section className="telemetry-support-group" aria-labelledby="battery-summary-title">
      <div className="support-heading">
        <h3 id="battery-summary-title">Power systems</h3>
        {batteries.length > 0 && <span>{batteries.length} {batteries.length === 1 ? "battery" : "batteries"}</span>}
      </div>
      {batteries.length > 0 ? (
        <ul className="battery-list">
          {batteries.map((battery) => (
            <li key={`${battery.id}-${battery.function}`}>
              <div>
                <strong>{batteryLabel(battery)}</strong>
                <span>{formatRemainingTime(battery.timeRemainingS)}</span>
              </div>
              <dl>
                <div><dt>Charge</dt><dd>{formatMeasurement(battery.remainingPercent, 0, "%")}</dd></div>
                <div><dt>Voltage</dt><dd>{formatMeasurement(battery.voltageV, 1, " V")}</dd></div>
                <div><dt>Current</dt><dd>{formatMeasurement(battery.currentA, 1, " A")}</dd></div>
                <div><dt>Temperature</dt><dd>{formatMeasurement(battery.temperatureC, 0, "°C")}</dd></div>
              </dl>
            </li>
          ))}
        </ul>
      ) : (
        <p className="support-empty">Waiting for detailed battery data.</p>
      )}
    </section>
  );
}

function StatusEventFeed({ events }: { events: StatusEvent[] }) {
  return (
    <section className="event-feed" aria-labelledby="event-feed-title">
      <header>
        <div>
          <p className="eyebrow">Aircraft events</p>
          <h2 id="event-feed-title">PX4 messages</h2>
        </div>
        <span>{events.length > 0 ? `${events.length} recent` : "No messages"}</span>
      </header>
      {events.length > 0 ? (
        <ol>
          {events.slice(0, 8).map((event) => (
            <li key={event.id} className={`event-item event-item--${eventTone(event.severity)}`}>
              <span className="event-severity">{displayEnum(event.severity)}</span>
              <p>{event.message}</p>
              <time dateTime={new Date(event.observedAtUnixMs).toISOString()}>
                {formatRelativeTime(event.receivedAtUnixMs)}
              </time>
            </li>
          ))}
        </ol>
      ) : (
        <p className="event-feed-empty">PX4 status and failsafe messages will appear here when reported.</p>
      )}
    </section>
  );
}

function StatusItem({
  label,
  value,
  detail,
  tone,
}: {
  label: string;
  value: string;
  detail: string;
  tone: StatusTone;
}) {
  return (
    <article className={`status-item status-item--${tone}`}>
      <p>{label}</p>
      <strong>{value}</strong>
      <span>{detail}</span>
    </article>
  );
}

function ConnectionGuide() {
  const steps = [
    ["Power aircraft systems", "Start the flight controller and onboard computer."],
    ["Confirm the HM30 link", "Keep both endpoints on the same local network."],
    ["Start Atlas Agent", "The aircraft appears automatically after it connects."],
  ];

  return (
    <section className="connection-guide" aria-labelledby="connection-guide-title">
      <div className="guide-heading">
        <p className="eyebrow">First connection</p>
        <h2 id="connection-guide-title">Connect an aircraft</h2>
      </div>
      <ol>
        {steps.map(([title, detail], index) => (
          <li key={title}>
            <span>{String(index + 1).padStart(2, "0")}</span>
            <div>
              <strong>{title}</strong>
              <p>{detail}</p>
            </div>
          </li>
        ))}
      </ol>
    </section>
  );
}

function RecoveryNotice() {
  return (
    <section className="recovery-notice" role="alert" aria-labelledby="recovery-title">
      <p className="eyebrow">Action required</p>
      <h2 id="recovery-title">Ground station services did not start</h2>
      <p>
        Close and reopen Atlas. If the problem continues, do not begin vehicle
        operations and review the application log.
      </p>
    </section>
  );
}

function ConnectionDetails({ snapshot }: { snapshot: GroundStationSnapshot }) {
  const details = [
    ["Listener", snapshot.listenAddress],
    ["Remote endpoint", snapshot.remoteAddress || "—"],
    ["Aircraft ID", snapshot.droneId || "—"],
    ["Agent ID", snapshot.agentId || "—"],
    ["Binding ID", snapshot.bindingId || "—"],
    ["Communication link ID", snapshot.communicationLinkId || "—"],
    ["Session ID", snapshot.sessionId || "—"],
  ];

  return (
    <details className="connection-details">
      <summary>Connection details</summary>
      <dl>
        {details.map(([label, value]) => (
          <div key={label}>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
    </details>
  );
}

function BrandMark() {
  return (
    <div className="wordmark" aria-label="Atlas Ground Station">
      <span className="wordmark-mark" aria-hidden="true">A</span>
      <span>
        <strong>Atlas</strong>
        <small>Ground Station</small>
      </span>
    </div>
  );
}

function operatorView(
  snapshot: GroundStationSnapshot,
  nativeState: NativeState,
  heartbeat: string,
): OperatorView {
  if (nativeState === "starting") {
    return {
      title: "Starting ground station",
      statusLabel: "Starting",
      guidance: "Preparing local aircraft services and connection state.",
      stateDetail: "Checking local services.",
      tone: "neutral",
    };
  }

  if (nativeState === "unavailable") {
    return {
      title: "Ground station unavailable",
      statusLabel: "Unavailable",
      guidance: "Reopen Atlas to restore the local services required for vehicle operations.",
      stateDetail: "Local services are not responding.",
      tone: "critical",
    };
  }

  if (snapshot.vehicleStatus === "archived") {
    return {
      title: snapshot.droneName || "Archived aircraft",
      statusLabel: "Archived",
      guidance: "This aircraft is outside operational fleet views and cannot reconnect until it is restored in Settings.",
      stateDetail: "Operational history is retained locally.",
      tone: "neutral",
    };
  }

  if (snapshot.connectionStatus === "connected") {
    return {
      title: snapshot.droneName || "Aircraft connected",
      statusLabel: "Connected",
      guidance: "The onboard agent is responding over the local link.",
      stateDetail: `Heartbeat ${heartbeat.toLowerCase()}.`,
      tone: "positive",
    };
  }

  if (snapshot.connectionStatus === "stale") {
    return {
      title: snapshot.droneName || "Aircraft link degraded",
      statusLabel: "Degraded",
      guidance: "Heartbeat updates have stopped. Check the HM30 link and onboard computer.",
      stateDetail: `Last heartbeat ${heartbeat.toLowerCase()}.`,
      tone: "warning",
    };
  }

  if (snapshot.droneId || snapshot.droneName) {
    return {
      title: snapshot.droneName || "Aircraft offline",
      statusLabel: "Offline",
      guidance: "Atlas will restore the session when the onboard agent reconnects.",
      stateDetail: snapshot.lastHeartbeatAtUnixMs
        ? `Last heartbeat ${heartbeat.toLowerCase()}.`
        : "No heartbeat has been recorded.",
      tone: "neutral",
    };
  }

  return {
    title: "No aircraft connected",
    statusLabel: "Waiting",
    guidance: "Power the onboard computer and confirm the HM30 network link. Atlas Agent connects automatically.",
    stateDetail: `Listening at ${snapshot.listenAddress}.`,
    tone: "neutral",
  };
}

function compactIdentifier(value: string) {
  return value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value;
}

function aircraftName(aircraft: FleetSnapshot["aircraft"], droneId: string) {
  const match = aircraft.find((candidate) => candidate.droneId === droneId);
  return match?.droneName || droneId.slice(0, 8).toUpperCase();
}

function aircraftContextState(aircraft: FleetSnapshot["aircraft"][number]) {
  if (aircraft.connectionStatus !== "connected") return displayEnum(aircraft.connectionStatus);
  if (aircraft.telemetry?.inAir) return displayEnum(aircraft.telemetry.flightMode || "in air");
  return "On the ground";
}

function cameraContextSummary(aircraft?: FleetSnapshot["aircraft"][number]) {
  if (!aircraft) return "Select an aircraft to open its camera and follow controls.";
  const connection = displayEnum(aircraft.connectionStatus);
  const flight = aircraft.telemetry?.inAir ? "in air" : aircraft.telemetry?.inAir === false ? "on the ground" : "flight state unknown";
  const battery = aircraft.telemetry?.batteryPercent == null ? "battery unavailable" : `${aircraft.telemetry.batteryPercent.toFixed(0)}% battery`;
  return `${connection} · ${flight} · ${battery}`;
}

function initialDisplayMode(): DisplayMode {
  try {
    return window.localStorage.getItem("atlas.displayMode") === "field" ? "field" : "desk";
  } catch {
    return "desk";
  }
}

function authorityFreshnessCopy(lastUpdatedAtUnixMs: number | undefined) {
  if (!lastUpdatedAtUnixMs) return "No current refresh · retrying";
  const ageSeconds = Math.max(0, Math.round((Date.now() - lastUpdatedAtUnixMs) / 1000));
  return `Last updated ${ageSeconds} s ago · retrying`;
}

function formatRelativeTime(timestamp: Nullable<number>) {
  if (!timestamp) return "Not received";
  const ageSeconds = Math.max(0, Math.round((Date.now() - timestamp) / 1000));
  if (ageSeconds < 2) return "Now";
  if (ageSeconds < 60) return `${ageSeconds} seconds ago`;
  const ageMinutes = Math.floor(ageSeconds / 60);
  if (ageMinutes < 60) return `${ageMinutes} ${ageMinutes === 1 ? "minute" : "minutes"} ago`;
  const ageHours = Math.floor(ageMinutes / 60);
  return `${ageHours} ${ageHours === 1 ? "hour" : "hours"} ago`;
}

function heartbeatDetail(status: ConnectionStatus, timestamp: Nullable<number>) {
  if (!timestamp) return "Waiting for first update";
  if (status === "stale") return "Updates interrupted";
  if (status === "disconnected") return "Session closed";
  return "Updates every 5 seconds";
}

function heartbeatTone(status: ConnectionStatus, timestamp: Nullable<number>): StatusTone {
  if (!timestamp || status === "disconnected") return "neutral";
  return status === "stale" ? "warning" : "positive";
}

function flightState(armed: Nullable<boolean>, inAir: Nullable<boolean>, landedState: Nullable<string>) {
  if (armed == null && inAir == null && !landedState) return "Not reported";
  if (inAir) return armed === false ? "In air · disarmed" : "In air · armed";
  if (landedState && landedState !== "UNKNOWN") {
    const state = displayEnum(landedState);
    return armed ? `${state} · armed` : `${state} · disarmed`;
  }
  return armed ? "On ground · armed" : "On ground · disarmed";
}

function displayEnum(value: Nullable<string>) {
  if (!value) return "Not reported";
  return value.toLowerCase().replace(/_/g, " ");
}

function formatMeasurement(value: Nullable<number>, digits: number, suffix: string) {
  return value == null ? "Not reported" : `${value.toFixed(digits)}${suffix}`;
}

function formatSignedMeasurement(value: Nullable<number>, digits: number, suffix: string) {
  if (value == null) return "Not reported";
  const prefix = value > 0 ? "+" : "";
  return `${prefix}${value.toFixed(digits)}${suffix}`;
}

function selectPrimaryBattery(batteries: BatteryTelemetry[]) {
  return batteries.find((battery) => battery.function === "ALL" || battery.function === "PROPULSION")
    ?? batteries[0];
}

function aircraftAuthoritySummary(follow?: AircraftFollowSession, mission?: MissionRun) {
  if (follow) return follow.state === "DEGRADED_HOLD" ? "PX4 hold · follow stopped" : "Aircraft follow";
  if (mission?.status === "ROUTE_COMPLETE" || mission?.status === "PAUSED") return "Mission · held";
  if (mission?.status === "RTL") return "Mission · return home";
  if (mission) return "Mission execution";
  return "Manual / PX4";
}

function batteryLabel(battery: BatteryTelemetry) {
  const role = displayEnum(battery.function);
  return battery.function === "UNKNOWN" ? `Battery ${battery.id}` : `${role} battery ${battery.id}`;
}

function formatRemainingTime(seconds: Nullable<number>) {
  if (seconds == null) return "Time remaining not reported";
  if (seconds < 60) return `${Math.round(seconds)} seconds remaining`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} ${minutes === 1 ? "minute" : "minutes"} remaining`;
  const hours = Math.floor(minutes / 60);
  const remainder = minutes % 60;
  return `${hours} h ${remainder} min remaining`;
}

function rcStatus(status: Nullable<RcStatus>) {
  if (!status) return "Not reported";
  if (!status.available) return status.wasAvailableOnce ? "Signal lost" : "Not detected";
  return status.signalStrengthPercent == null
    ? "Available"
    : `Available · ${status.signalStrengthPercent.toFixed(0)}%`;
}

function gpsPrecision(quality: Nullable<GpsQuality>) {
  if (!quality) return "Not reported";
  const values: string[] = [];
  if (quality.hdop != null) values.push(`HDOP ${quality.hdop.toFixed(1)}`);
  if (quality.horizontalUncertaintyM != null) {
    values.push(`±${quality.horizontalUncertaintyM.toFixed(1)} m`);
  }
  return values.length > 0 ? values.join(" · ") : "Not reported";
}

function nedVelocity(telemetry: AircraftTelemetry) {
  const values = [
    telemetry.velocityNorthMps,
    telemetry.velocityEastMps,
    telemetry.velocityDownMps,
  ];
  if (values.every((value) => value == null)) return "Not reported";
  return values.map((value) => value == null ? "—" : value.toFixed(1)).join(" / ") + " m/s";
}

function homeStatus(isSet: Nullable<boolean>, home: Nullable<HomePosition>) {
  if (isSet === false) return "Not set";
  if (home?.latitude != null && home.longitude != null) {
    return `${home.latitude.toFixed(4)}, ${home.longitude.toFixed(4)}`;
  }
  return isSet ? "Set" : "Not reported";
}

function eventTone(severity: string): StatusTone {
  switch (severity.toUpperCase()) {
    case "EMERGENCY":
    case "ALERT":
    case "CRITICAL":
    case "ERROR":
      return "critical";
    case "WARNING":
      return "warning";
    default:
      return "neutral";
  }
}

function gpsStatus(fix: Nullable<string>, satellites: Nullable<number>) {
  if (!fix && satellites == null) return "Not reported";
  const fixLabel = displayEnum(fix);
  return satellites == null ? fixLabel : `${fixLabel} · ${satellites} satellites`;
}

function position(latitude: Nullable<number>, longitude: Nullable<number>) {
  if (latitude == null || longitude == null) return "Not reported";
  return `${latitude.toFixed(5)}, ${longitude.toFixed(5)}`;
}

export default App;
