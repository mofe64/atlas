import { lazy, Suspense, useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { FleetPage } from "./fleet/FleetPage";
import { HistoryPage } from "./history/HistoryPage";
import { InspectionPayloadControl } from "./missions/MissionPayloadControl";
import type { MissionRun } from "./missions/missionTypes";
import type { ConnectionStatus, FleetSnapshot, NativeState, Nullable, StatusTone } from "./operationsTypes";
import { LiveVideo } from "./video/LiveVideo";
import "./App.css";

const MissionPage = lazy(() => import("./missions/MissionPage").then((module) => ({ default: module.MissionPage })));
const MissionExecutionPage = lazy(() => import("./missions/MissionExecutionPage").then((module) => ({ default: module.MissionExecutionPage })));

type WorkspaceView = "fleet" | "aircraft" | "missions" | "mission-execution" | "history";
type AircraftSection = "overview" | "live" | "missions" | "settings";

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
  const [workspaceView, setWorkspaceView] = useState<WorkspaceView>("fleet");
  const [selectedDroneId, setSelectedDroneId] = useState<string>();
  const [selectedMissionId, setSelectedMissionId] = useState<string>();
  const [aircraftSection, setAircraftSection] = useState<AircraftSection>("overview");
  const [showArchived, setShowArchived] = useState(false);

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

  const heartbeat = formatRelativeTime(snapshot.lastHeartbeatAtUnixMs);
  const view = operatorView(snapshot, nativeState, heartbeat);
  const operationalAircraft = fleet.aircraft.filter((aircraft) => aircraft.vehicleStatus !== "archived");
  const visibleAircraft = showArchived
    ? fleet.aircraft.filter((aircraft) => aircraft.vehicleStatus === "archived")
    : operationalAircraft;
  const fleetView = fleetSystemView({ ...fleet, aircraft: operationalAircraft }, nativeState);
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

  return (
    <div className="operations-shell">
      <header className="operations-header">
        <BrandMark />
        <nav className="workspace-nav" aria-label="Atlas workspace">
          <button
            type="button"
            className={workspaceView === "fleet" || workspaceView === "aircraft" ? "workspace-nav__active" : undefined}
            aria-current={workspaceView === "fleet" || workspaceView === "aircraft" ? "page" : undefined}
            onClick={() => setWorkspaceView("fleet")}
          >
            Fleet
          </button>
          <button
            type="button"
            className={workspaceView === "missions" || workspaceView === "mission-execution" ? "workspace-nav__active" : undefined}
            aria-current={workspaceView === "missions" || workspaceView === "mission-execution" ? "page" : undefined}
            onClick={() => {
              setWorkspaceView("missions");
            }}
          >
            Missions
          </button>
          <button
            type="button"
            className={workspaceView === "history" ? "workspace-nav__active" : undefined}
            aria-current={workspaceView === "history" ? "page" : undefined}
            onClick={() => {
              setSelectedDroneId(undefined);
              setWorkspaceView("history");
            }}
          >
            History
          </button>
        </nav>
        <div
          className={`system-state system-state--${fleetView.tone}`}
          role="status"
          aria-live="polite"
        >
          <span className="state-dot" aria-hidden="true" />
          <span>
            <small>Fleet status</small>
            <strong>{fleetView.statusLabel}</strong>
          </span>
        </div>
      </header>

      {workspaceView === "fleet" ? (
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
            setWorkspaceView("history");
          }}
        />
      ) : workspaceView === "missions" ? (
        <Suspense fallback={<main className="workspace-loading" id="main-content"><p>Loading mission map…</p></main>}>
          <MissionPage
            nativeAvailable={nativeState === "available"}
            fleetAircraft={operationalAircraft}
            preferredDroneId={selectedDroneId}
            onMissionReady={(missionId) => {
              setSelectedMissionId(missionId);
              setWorkspaceView("mission-execution");
            }}
          />
        </Suspense>
      ) : workspaceView === "mission-execution" && selectedMissionId ? (
        <Suspense fallback={<main className="workspace-loading" id="main-content"><p>Loading mission execution…</p></main>}>
          <MissionExecutionPage
            nativeAvailable={nativeState === "available"}
            missionId={selectedMissionId}
            onBack={() => setWorkspaceView("missions")}
          />
        </Suspense>
      ) : workspaceView === "history" ? (
        <HistoryPage
          droneId={selectedDroneId}
          droneName={snapshot.droneId === selectedDroneId ? snapshot.droneName : undefined}
          nativeAvailable={nativeState === "available"}
          onOpenDroneHistory={setSelectedDroneId}
          onBackToOverview={() => setSelectedDroneId(undefined)}
        />
      ) : (
      <main className="operations-main" id="main-content">
        <button type="button" className="back-to-fleet" onClick={() => setWorkspaceView("fleet")}>
          <span aria-hidden="true">←</span> Fleet
        </button>
        <section className="aircraft-overview aircraft-overview--workspace" aria-labelledby="aircraft-title">
          <div className="overview-copy">
            <p className="eyebrow">{aircraftSectionLabel(aircraftSection)}</p>
            <h1 id="aircraft-title">{view.title}</h1>
            <p className="overview-guidance">{aircraftSectionGuidance(aircraftSection, view.guidance)}</p>
          </div>

          <div className={`state-readout state-readout--${view.tone}`}>
            <span className="readout-label">Current state</span>
            <strong>{view.statusLabel}</strong>
            <p>{view.stateDetail}</p>
          </div>
        </section>

        <section className="aircraft-context-strip" aria-label="Selected aircraft context">
          <StatusItem label="Ground link" value={view.statusLabel} detail={groundLinkDetail} tone={view.tone} />
          <StatusItem label="Aircraft" value={flightState(snapshot.telemetry?.armed, snapshot.telemetry?.inAir, snapshot.telemetry?.landedState)} detail={displayEnum(snapshot.telemetry?.flightMode)} tone={snapshot.telemetry?.armed ? "warning" : "neutral"} />
          <StatusItem label="Lifecycle" value={displayEnum(snapshot.vehicleStatus)} detail={snapshot.droneId ? compactIdentifier(snapshot.droneId) : "No aircraft identity"} tone={snapshot.vehicleStatus === "active" ? "positive" : "neutral"} />
        </section>

        <nav className="aircraft-section-nav" aria-label={`${view.title} workspace`}>
          {(["overview", "live", "missions", "settings"] as AircraftSection[]).map((section) => (
            <button key={section} type="button" className={aircraftSection === section ? "aircraft-section-nav__active" : undefined} aria-current={aircraftSection === section ? "page" : undefined} onClick={() => setAircraftSection(section)}>
              {displayEnum(section)}
            </button>
          ))}
          <button type="button" onClick={() => setWorkspaceView("history")}>History</button>
        </nav>

        {aircraftSection === "overview" && <>
        <section className="status-grid" aria-label="Live drone status">
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
            <TelemetryPanel telemetry={snapshot.telemetry} />
            <StatusEventFeed events={snapshot.statusEvents} />
          </>
        )}

        {!hasAircraft && nativeState === "available" && <ConnectionGuide />}
        {nativeState === "unavailable" && <RecoveryNotice />}

        <ConnectionDetails snapshot={snapshot} />
        </>}

        {aircraftSection === "live" && (
          <section className="aircraft-live-workspace" aria-label="Live aircraft inspection">
            <div className="aircraft-live-video">
              <LiveVideo nativeAvailable={nativeState === "available"} droneId={snapshot.droneId ?? undefined} />
            </div>
            <aside className="aircraft-live-control" aria-label="Inspection payload controls">
              <InspectionPayloadControl aircraft={selectedAircraft} />
            </aside>
          </section>
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
      </main>
      )}

      <footer className="operations-footer">
        <span>Atlas Ground Station</span>
        <span>Local data · 7-day telemetry history</span>
      </footer>
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

  const activeRun = runs.find((run) => !["COMPLETED", "FAILED", "CANCELLED", "RTL"].includes(run.status));
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
          ? { droneId: snapshot.droneId, reason: "operator archived aircraft from settings" }
          : { droneId: snapshot.droneId },
      );
      onLifecycleChanged(operations);
      setConfirmingArchive(false);
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
        <div><dt>Drone ID</dt><dd>{snapshot.droneId || "Not reported"}</dd></div>
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
            <p>This removes the aircraft from operational fleet views and blocks reconnects until it is restored.</p>
            <div>
              <button type="button" disabled={busy} onClick={() => setConfirmingArchive(false)}>Cancel</button>
              <button type="button" className="aircraft-lifecycle-danger" disabled={busy} onClick={() => void changeLifecycle("archive")}>
                {busy ? "Archiving…" : "Confirm archive"}
              </button>
            </div>
          </div>
        ) : (
          <button type="button" disabled={connected || busy || !snapshot.droneId} onClick={() => setConfirmingArchive(true)}>
            {connected ? "Disconnect before archiving" : "Archive aircraft"}
          </button>
        )}
      </section>
      {error && <p className="aircraft-workspace-error" role="alert">{error}</p>}
      {notice && <p className="aircraft-workspace-notice" role="status">{notice}</p>}
    </section>
  );
}

function aircraftSectionLabel(section: AircraftSection) {
  if (section === "live") return "Live inspection";
  if (section === "missions") return "Aircraft missions";
  if (section === "settings") return "Aircraft settings";
  return "Drone overview";
}

function aircraftSectionGuidance(section: AircraftSection, overviewGuidance: string) {
  if (section === "live") return "Observe clean video and perception readiness first. Physical gimbal and zoom controls require an explicit, renewable inspection lease.";
  if (section === "missions") return "Review the current assignment and retained execution history for this aircraft.";
  if (section === "settings") return "Manage aircraft identity and service lifecycle without deleting operational history.";
  return overviewGuidance;
}

function formatDateTime(value: number) {
  return new Intl.DateTimeFormat(undefined, { day: "2-digit", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" }).format(value);
}

function TelemetryPanel({ telemetry }: { telemetry?: AircraftTelemetry | null }) {
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
  const metrics = [
    ["Flight state", flightState(telemetry.armed, telemetry.inAir, telemetry.landedState)],
    ["Flight mode", displayEnum(telemetry.flightMode)],
    ["Battery", batteryStatus(primaryBattery, telemetry.batteryPercent)],
    ["RC link", rcStatus(telemetry.rcStatus)],
    ["Relative altitude", formatMeasurement(telemetry.relativeAltitudeM, 1, " m")],
    ["Absolute altitude", formatMeasurement(telemetry.absoluteAltitudeM, 1, " m")],
    ["Terrain altitude", formatMeasurement(telemetry.terrainAltitudeM, 1, " m")],
    ["Bottom clearance", formatMeasurement(telemetry.bottomClearanceM, 1, " m")],
    ["Climb rate", formatSignedMeasurement(telemetry.climbRateMps, 1, " m/s")],
    ["Ground speed", formatMeasurement(telemetry.groundSpeedMps, 1, " m/s")],
    ["Heading", formatMeasurement(telemetry.headingDeg, 0, "°")],
    ["GPS", gpsStatus(telemetry.gpsFix, telemetry.satellitesVisible)],
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
        {metrics.map(([label, value]) => (
          <article key={label}>
            <p>{label}</p>
            <strong>{value}</strong>
          </article>
        ))}
      </div>
      <div className="telemetry-support">
        <PreflightHealth health={telemetry.health} />
        <BatterySummary batteries={telemetry.batteries} />
      </div>
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

  return (
    <section className="telemetry-support-group" aria-labelledby="preflight-health-title">
      <div className="support-heading">
        <h3 id="preflight-health-title">Preflight health</h3>
        {health && <span>{health.armable ? "Ready to arm" : "Attention required"}</span>}
      </div>
      {health ? (
        <ul className="health-list">
          {checks.map(([label, ready]) => (
            <li key={label} className={ready ? "health-check--ready" : "health-check--attention"}>
              <span className="health-marker" aria-hidden="true">{ready ? "✓" : "!"}</span>
              <span>{label}</span>
              <strong>{ready ? "Ready" : "Check"}</strong>
            </li>
          ))}
        </ul>
      ) : (
        <p className="support-empty">Waiting for MAVSDK health checks.</p>
      )}
    </section>
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
          <p className="eyebrow">Drone events</p>
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
    ["Power drone systems", "Start the flight controller and onboard computer."],
    ["Confirm the HM30 link", "Keep both endpoints on the same local network."],
    ["Start Atlas Agent", "The drone appears automatically after it connects."],
  ];

  return (
    <section className="connection-guide" aria-labelledby="connection-guide-title">
      <div className="guide-heading">
        <p className="eyebrow">First connection</p>
        <h2 id="connection-guide-title">Connect a drone</h2>
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
    ["Drone ID", snapshot.droneId || "—"],
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
      guidance: "Preparing local drone services and connection state.",
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
      title: snapshot.droneName || "Drone connected",
      statusLabel: "Connected",
      guidance: "The onboard agent is responding over the local link.",
      stateDetail: `Heartbeat ${heartbeat.toLowerCase()}.`,
      tone: "positive",
    };
  }

  if (snapshot.connectionStatus === "stale") {
    return {
      title: snapshot.droneName || "Drone link degraded",
      statusLabel: "Degraded",
      guidance: "Heartbeat updates have stopped. Check the HM30 link and onboard computer.",
      stateDetail: `Last heartbeat ${heartbeat.toLowerCase()}.`,
      tone: "warning",
    };
  }

  if (snapshot.droneId || snapshot.droneName) {
    return {
      title: snapshot.droneName || "Drone offline",
      statusLabel: "Offline",
      guidance: "Atlas will restore the session when the onboard agent reconnects.",
      stateDetail: snapshot.lastHeartbeatAtUnixMs
        ? `Last heartbeat ${heartbeat.toLowerCase()}.`
        : "No heartbeat has been recorded.",
      tone: "neutral",
    };
  }

  return {
    title: "No drone connected",
    statusLabel: "Waiting",
    guidance: "Power the onboard computer and confirm the HM30 network link. Atlas Agent connects automatically.",
    stateDetail: `Listening at ${snapshot.listenAddress}.`,
    tone: "neutral",
  };
}

function fleetSystemView(fleet: FleetSnapshot, nativeState: NativeState): Pick<OperatorView, "statusLabel" | "tone"> {
  if (nativeState === "starting") return { statusLabel: "Starting", tone: "neutral" };
  if (nativeState === "unavailable") return { statusLabel: "Unavailable", tone: "critical" };
  if (fleet.aircraft.length === 0) return { statusLabel: "Waiting for drones", tone: "neutral" };

  const connected = fleet.aircraft.filter((aircraft) => aircraft.connectionStatus === "connected").length;
  const degraded = fleet.aircraft.some((aircraft) => aircraft.connectionStatus === "stale");
  if (degraded) return { statusLabel: `${connected}/${fleet.aircraft.length} connected`, tone: "warning" };
  if (connected === fleet.aircraft.length) {
    return { statusLabel: `${connected}/${fleet.aircraft.length} connected`, tone: "positive" };
  }
  return { statusLabel: `${connected}/${fleet.aircraft.length} connected`, tone: "neutral" };
}

function compactIdentifier(value: string) {
  return value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value;
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

function batteryStatus(battery: Nullable<BatteryTelemetry>, fallback: Nullable<number>) {
  const charge = battery?.remainingPercent ?? fallback;
  if (!battery) return formatMeasurement(charge, 0, "%");
  const parts = [formatMeasurement(charge, 0, "%")];
  if (battery.voltageV != null) parts.push(`${battery.voltageV.toFixed(1)} V`);
  if (battery.currentA != null) parts.push(`${battery.currentA.toFixed(1)} A`);
  return parts.join(" · ");
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
