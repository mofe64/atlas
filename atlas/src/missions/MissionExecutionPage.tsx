import { useEffect, useMemo, useState } from "react";
import type { CSSProperties } from "react";
import { invoke } from "@tauri-apps/api/core";
import type { FleetAircraft, FleetSnapshot } from "../operationsTypes";
import { OperationalMissionMap, type PayloadTarget, type TrackedAircraft, type TrackedHome } from "./OperationalMissionMap";
import { MissionPayloadControl } from "./MissionPayloadControl";
import { LiveVideo } from "../video/LiveVideo";
import { formatDistance, missionDistanceStatus } from "./missionSafety";
import type { Mission, MissionPlan, MissionRun } from "./missionTypes";
import "./MissionPage.css";
import "./MissionExecutionPage.css";

type MissionExecutionPageProps = {
  nativeAvailable: boolean;
  missionId: string;
  onBack: () => void;
};

const terminalStates = new Set(["COMPLETED", "FAILED", "CANCELLED", "RTL"]);

type TelemetryHistoryPage = {
  snapshots: Array<{
    telemetry: {
      receivedAtUnixMs: number;
      latitude?: number | null;
      longitude?: number | null;
    };
  }>;
};

export function MissionExecutionPage({ nativeAvailable, missionId, onBack }: MissionExecutionPageProps) {
  const [mission, setMission] = useState<Mission>();
  const [plan, setPlan] = useState<MissionPlan>();
  const [fleet, setFleet] = useState<FleetSnapshot>();
  const [runs, setRuns] = useState<MissionRun[]>([]);
  const [selectedDroneId, setSelectedDroneId] = useState("");
  const [selectedRunId, setSelectedRunId] = useState<string>();
  const [pendingOperation, setPendingOperation] = useState<string>();
  const [error, setError] = useState<string>();
  const [followAircraft, setFollowAircraft] = useState(false);
  const [aircraftTrail, setAircraftTrail] = useState<Array<{ latitude: number; longitude: number }>>([]);
  const [payloadTarget, setPayloadTarget] = useState<PayloadTarget>();
  const [selectingPayloadTarget, setSelectingPayloadTarget] = useState(false);
  const [liveSurface, setLiveSurface] = useState<"map" | "camera">("map");

  useEffect(() => {
    if (!nativeAvailable) return;
    let active = true;
    let reading = false;
    async function readState(includeDefinition = false) {
      if (reading) return;
      reading = true;
      try {
        const [nextFleet, nextRuns] = await Promise.all([
          invoke<FleetSnapshot>("fleet_snapshot"),
          invoke<MissionRun[]>("mission_run_history", { limit: 100 }),
        ]);
        let nextMission: Mission | undefined;
        let nextPlan: MissionPlan | undefined;
        if (includeDefinition) {
          [nextMission, nextPlan] = await Promise.all([
            invoke<Mission>("mission_detail", { missionId }),
            invoke<MissionPlan>("mission_plan", { missionId }),
          ]);
        }
        if (!active) return;
        setFleet(nextFleet);
        setRuns(nextRuns);
        if (nextMission) setMission(nextMission);
        if (nextPlan) setPlan(nextPlan);
        const missionRuns = nextRuns.filter((run) => run.missionId === missionId);
        const preferredRun = missionRuns.find((run) => !terminalStates.has(run.status)) ?? missionRuns[0];
        const connected = nextFleet.aircraft.find((candidate) => candidate.connectionStatus === "connected" && candidate.droneId)?.droneId;
        setSelectedRunId((current) => current && nextRuns.some((run) => run.id === current) ? current : preferredRun?.id);
        setSelectedDroneId((current) => current || preferredRun?.droneId || connected || "");
      } catch (reason) {
        if (active) setError(messageFrom(reason));
      } finally {
        reading = false;
      }
    }
    void readState(true);
    const interval = window.setInterval(() => void readState(false), 1_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [missionId, nativeAvailable]);

  const missionRuns = useMemo(() => runs.filter((run) => run.missionId === missionId), [missionId, runs]);
  const selectedRun = runs.find((run) => run.id === selectedRunId) ?? missionRuns[0];
  const activeRun = missionRuns.find((run) => !terminalStates.has(run.status));
  const targetDroneId = selectedRun?.droneId || selectedDroneId;
  const targetAircraft = fleet?.aircraft.find((candidate) => candidate.droneId === targetDroneId);
  const connectedAircraft = fleet?.aircraft.filter((candidate) => candidate.connectionStatus === "connected" && candidate.droneId) ?? [];
  const targetActiveRun = runs.find((run) => run.droneId === selectedDroneId && !terminalStates.has(run.status));
  const deploymentAircraft = fleet?.aircraft.find((candidate) => candidate.droneId === selectedDroneId);
  const uploadDistance = selectedDroneId ? missionDistanceStatus(plan?.generatedWaypoints[0], deploymentAircraft) : undefined;
  const trackedAircraft = trackedPosition(targetAircraft);
  const trackedHome = trackedHomePosition(targetAircraft);
  const preflight = preflightState(targetAircraft);
  const canUpload = Boolean(nativeAvailable && mission?.generatedPlanId && plan && selectedDroneId && uploadDistance?.ok && !activeRun && !targetActiveRun && !pendingOperation);

  useEffect(() => {
    if (!nativeAvailable || !selectedRun) {
      setAircraftTrail([]);
      return;
    }
    setAircraftTrail([]);
    let active = true;
    const from = selectedRun.startedAtUnixMs ?? selectedRun.createdAtUnixMs;
    void invoke<TelemetryHistoryPage>("vehicle_telemetry_history", {
      droneId: selectedRun.droneId,
      fromReceivedAtUnixMs: from,
      toReceivedAtUnixMs: selectedRun.completedAtUnixMs ?? null,
      before: null,
      limit: 500,
    }).then((page) => {
      if (!active) return;
      const persisted = page.snapshots
        .slice()
        .reverse()
        .flatMap((snapshot) => validTrailPosition(snapshot.telemetry));
      setAircraftTrail((current) => mergeTrails(persisted, current));
    }).catch((reason) => {
      if (active) setError(messageFrom(reason));
    });
    return () => { active = false; };
  }, [nativeAvailable, selectedRun?.completedAtUnixMs, selectedRun?.createdAtUnixMs, selectedRun?.droneId, selectedRun?.id, selectedRun?.startedAtUnixMs]);

  useEffect(() => {
    if (!trackedAircraft || trackedAircraft.telemetryStatus !== "live") return;
    setAircraftTrail((current) => {
      const last = current[current.length - 1];
      if (last && Math.abs(last.latitude - trackedAircraft.latitude) < 0.0000005 && Math.abs(last.longitude - trackedAircraft.longitude) < 0.0000005) return current;
      return [...current.slice(-499), { latitude: trackedAircraft.latitude, longitude: trackedAircraft.longitude }];
    });
  }, [trackedAircraft?.latitude, trackedAircraft?.longitude, trackedAircraft?.telemetryStatus]);

  useEffect(() => {
    setPayloadTarget(undefined);
    setSelectingPayloadTarget(false);
  }, [selectedRun?.id]);

  async function refreshRuns(preferredRunId?: string) {
    const nextRuns = await invoke<MissionRun[]>("mission_run_history", { limit: 100 });
    setRuns(nextRuns);
    if (preferredRunId) setSelectedRunId(preferredRunId);
    return nextRuns;
  }

  async function upload() {
    if (!mission || !selectedDroneId) return;
    setPendingOperation("upload");
    setError(undefined);
    try {
      const run = await invoke<MissionRun>("upload_mission", { missionId: mission.id, droneId: selectedDroneId });
      setSelectedRunId(run.id);
      await refreshRuns(run.id);
      if (run.status === "FAILED") setError(run.errorMessage || "Mission upload could not be delivered.");
    } catch (reason) {
      setError(messageFrom(reason));
    } finally {
      setPendingOperation(undefined);
    }
  }

  async function control(operation: "start" | "pause" | "resume" | "cancel" | "return_to_launch") {
    if (!selectedRun) return;
    setPendingOperation(operation);
    setError(undefined);
    try {
      const run = await invoke<MissionRun>("control_mission_run", { missionRunId: selectedRun.id, operation });
      setSelectedRunId(run.id);
      await refreshRuns(run.id);
      if (run.errorCode) setError(run.errorMessage || `Mission ${operation.replace(/_/g, " ")} failed.`);
    } catch (reason) {
      setError(messageFrom(reason));
    } finally {
      setPendingOperation(undefined);
    }
  }

  if (!mission || !plan) {
    return (
      <main className="execution-workspace execution-workspace--loading" id="main-content">
        <button type="button" className="execution-back" onClick={onBack}>← Mission planner</button>
        <p>{nativeAvailable ? error || "Loading mission execution…" : "Atlas Native must be running to execute missions."}</p>
      </main>
    );
  }

  const progress = selectedRun?.status === "UPLOADING" ? selectedRun.uploadProgressPercent : selectedRun?.progressPercent ?? 0;
  const progressStyle = { "--mission-progress": `${Math.max(0, Math.min(100, progress))}%` } as CSSProperties;
  const warnings = translationWarnings(selectedRun);
  const latestEvent = selectedRun?.events[selectedRun.events.length - 1];

  return (
    <main className="execution-workspace" id="main-content">
      <header className="execution-header">
        <div>
          <button type="button" className="execution-back" onClick={onBack}>← Mission planner</button>
          <p className="eyebrow">Live mission execution</p>
          <h1>{mission.name}</h1>
          <p>{mission.templateType.replace(/_/g, " ")} · {plan.patternType.replace(/_/g, " ")} · {plan.generatedWaypoints.length} waypoints</p>
        </div>
        <span className={selectedRun ? `run-state run-state--${selectedRun.status.toLowerCase()}` : "run-state"}>{selectedRun?.status ?? "NOT UPLOADED"}</span>
      </header>

      <section className="execution-live-grid" aria-label="Live mission operation">
        <div className="execution-map-column">
          {liveSurface === "map" ? (
            <OperationalMissionMap
              mode="track"
              templateType={mission.templateType}
              planWaypoints={plan.generatedWaypoints}
              currentWaypoint={selectedRun?.currentWaypoint}
              aircraft={trackedAircraft}
              home={trackedHome}
              aircraftTrail={aircraftTrail}
              followAircraft={followAircraft}
              payloadTarget={payloadTarget}
              selectingPayloadTarget={selectingPayloadTarget}
              onPayloadTargetSelect={(target) => {
                setPayloadTarget(target);
                setSelectingPayloadTarget(false);
              }}
            />
          ) : (
            <LiveVideo nativeAvailable={nativeAvailable} droneId={targetDroneId || undefined} />
          )}
          <div className="execution-map-toolbar">
            <div className="execution-surface-switch" role="group" aria-label="Live operation view">
              <button type="button" className={liveSurface === "map" ? "execution-surface-switch--active" : ""} onClick={() => setLiveSurface("map")}>Mission map</button>
              <button type="button" className={liveSurface === "camera" ? "execution-surface-switch--active" : ""} onClick={() => setLiveSurface("camera")} disabled={!targetDroneId}>Camera & perception</button>
            </div>
            {liveSurface === "map" ? (
              <>
                <button type="button" className={followAircraft ? "follow-control follow-control--active" : "follow-control"} onClick={() => setFollowAircraft((current) => !current)} disabled={!trackedAircraft}>
                  {followAircraft ? "Following aircraft" : "Follow aircraft"}
                </button>
                <span>{trackedAircraft ? `${trackedAircraft.latitude.toFixed(6)}, ${trackedAircraft.longitude.toFixed(6)}` : "Waiting for aircraft position"}</span>
              </>
            ) : <span>{targetAircraft?.droneName || targetDroneId || "Select an aircraft"}</span>}
          </div>
          <div className="execution-telemetry-strip" aria-label="Live aircraft telemetry">
            <Metric label="Waypoint" value={selectedRun ? waypointLabel(selectedRun) : `— / ${plan.generatedWaypoints.length}`} />
            <Metric label="Altitude" value={formatMetric(targetAircraft?.telemetry?.relativeAltitudeM, "m")} />
            <Metric label="Ground speed" value={formatMetric(targetAircraft?.telemetry?.groundSpeedMps, "m/s")} />
            <Metric label="Heading" value={formatMetric(targetAircraft?.telemetry?.headingDeg, "°", 0)} />
            <Metric label="Flight mode" value={targetAircraft?.telemetry?.flightMode || "—"} />
            <Metric label="Aircraft" value={targetAircraft?.telemetry?.armed ? "ARMED" : "DISARMED"} tone={targetAircraft?.telemetry?.armed ? "warning" : undefined} />
          </div>
        </div>

        <aside className="execution-command-column" aria-label="Mission command and status">
          {!selectedRun && (
            <section className="execution-card">
              <div className="execution-card__title"><span>01</span><strong>Deploy plan</strong></div>
              <label className="execution-target">Target aircraft
                <select value={selectedDroneId} onChange={(event) => setSelectedDroneId(event.target.value)}>
                  <option value="">Select connected aircraft</option>
                  {connectedAircraft.map((candidate) => <option key={candidate.droneId!} value={candidate.droneId!}>{candidate.droneName || candidate.droneId}</option>)}
                </select>
              </label>
              {uploadDistance && !uploadDistance.ok && <div className="mission-distance-warning" role="alert"><strong>Mission distance check</strong><span>{uploadDistance.message}</span></div>}
              {uploadDistance?.ok && uploadDistance.distanceMeters !== undefined && <p className="mission-distance-ready">First waypoint · {formatDistance(uploadDistance.distanceMeters)} from {uploadDistance.reference?.source} position</p>}
              <button type="button" className="execution-primary-action" disabled={!canUpload} onClick={() => void upload()}>{pendingOperation === "upload" ? "Uploading to PX4…" : "Upload mission to aircraft"}</button>
              {connectedAircraft.length === 0 && <p className="execution-blocker">No connected Atlas Agent is available.</p>}
              {targetActiveRun && <p className="execution-blocker">This aircraft already has an unfinished run for “{targetActiveRun.missionName}”.</p>}
            </section>
          )}

          {selectedRun && (
            <>
              <section className="execution-card execution-card--run">
                <div className="execution-card__title"><span>01</span><strong>Run control</strong></div>
                <div className="execution-run-identity"><strong>{selectedRun.droneName}</strong><small>Run {shortId(selectedRun.id)} · plan {shortId(selectedRun.missionPlanId)}</small></div>
                <div className="mission-progress" style={progressStyle}>
                  <div className="mission-progress__track"><span /></div>
                  <div><strong>{Math.round(progress)}%</strong><span>{progressLabel(selectedRun)}</span></div>
                </div>
                {latestEvent && <p className="execution-latest-event"><span>{latestEvent.eventType.replace(/_/g, " ")}</span>{latestEvent.message || latestEvent.state}</p>}
                <div className="execution-run-controls">
                  {selectedRun.status === "READY" && <button type="button" className="execution-primary-action" disabled={Boolean(pendingOperation) || !preflight.ready} onClick={() => void control("start")}>Arm & start mission</button>}
                  {selectedRun.status === "RUNNING" && <button type="button" className="execution-primary-action" disabled={Boolean(pendingOperation)} onClick={() => void control("pause")}>Pause to hold</button>}
                  {selectedRun.status === "PAUSED" && <button type="button" className="execution-primary-action" disabled={Boolean(pendingOperation)} onClick={() => void control("resume")}>Resume mission</button>}
                  {["READY", "RUNNING", "PAUSED"].includes(selectedRun.status) && <button type="button" className="execution-secondary-action" disabled={Boolean(pendingOperation)} onClick={() => void control("cancel")}>Cancel mission</button>}
                  {["RUNNING", "PAUSED"].includes(selectedRun.status) && <button type="button" className="execution-critical-action" disabled={Boolean(pendingOperation)} onClick={() => void control("return_to_launch")}>End & RTL</button>}
                </div>
                {pendingOperation && <p className="execution-pending">Sending {pendingOperation.replace(/_/g, " ")}…</p>}
                {selectedRun.status === "READY" && <p className="execution-command-note">Atlas checks PX4 readiness, arms the aircraft, then starts the uploaded mission. A failed start commands HOLD.</p>}
                {selectedRun.status === "CANCELLED" && <p className="execution-command-note">Mission cleared. The aircraft remains in HOLD.</p>}
                {selectedRun.status === "RTL" && <p className="execution-command-note">PX4 accepted RTL. Monitor until landed and disarmed.</p>}
              </section>

              <MissionPayloadControl
                key={selectedRun.id}
                run={selectedRun}
                plan={plan}
                aircraft={targetAircraft}
                payloadTarget={payloadTarget}
                selectingPayloadTarget={selectingPayloadTarget}
                onSelectingPayloadTargetChange={setSelectingPayloadTarget}
                onClearPayloadTarget={() => setPayloadTarget(undefined)}
              />
              <section className="execution-card">
                <div className="execution-card__title"><span>03</span><strong>Preflight readiness</strong></div>
                <ul className="preflight-list">
                  {preflight.checks.map((check) => <li key={check.label} className={check.ok ? "preflight-check preflight-check--ready" : "preflight-check"}><span>{check.ok ? "✓" : "!"}</span><div><strong>{check.label}</strong><small>{check.detail}</small></div></li>)}
                </ul>
              </section>
            </>
          )}

          {(error || selectedRun?.errorCode) && <div className="mission-run-error" role="alert"><strong>{selectedRun?.errorCode || "MISSION OPERATION FAILED"}</strong><p>{error || selectedRun?.errorMessage}</p></div>}
          {warnings.map((warning) => <p className="mission-run-warning" key={warning}>{warning}</p>)}
        </aside>
      </section>

      <section className="execution-report-grid">
        <section className="mission-event-report" aria-label="Mission event report">
          <header><strong>Run event report</strong><span>{selectedRun?.events.length ?? 0} events</span></header>
          {selectedRun?.events.length ? (
            <ol>{[...selectedRun.events].reverse().map((event) => <li key={event.id}><span className={`event-mark event-mark--${event.state.toLowerCase()}`} /><div><strong>{event.eventType.replace(/_/g, " ")}</strong><p>{event.message || event.state}</p></div><time dateTime={new Date(event.occurredAtUnixMs).toISOString()}>{formatClock(event.occurredAtUnixMs)}</time></li>)}</ol>
          ) : <p className="event-report-empty">Upload this mission to begin its durable event report.</p>}
        </section>

        <section className="mission-run-history" aria-labelledby="mission-run-history-title">
          <header><div><h2 id="mission-run-history-title">Execution history</h2><span>Every upload creates a separate run</span></div><strong>{missionRuns.length}</strong></header>
          {missionRuns.length ? <div className="execution-history-list">{missionRuns.map((run) => <button key={run.id} type="button" className={selectedRun?.id === run.id ? "execution-history-row execution-history-row--selected" : "execution-history-row"} onClick={() => { setSelectedRunId(run.id); setSelectedDroneId(run.droneId); }}><span><strong>{run.droneName}</strong><small>{formatDate(run.createdAtUnixMs)} · {shortId(run.id)}</small></span><span><i className={`run-dot run-dot--${run.status.toLowerCase()}`} />{run.status}</span><span>{Math.round(run.progressPercent)}%</span></button>)}</div> : <p className="mission-history-empty">No execution run exists for this mission.</p>}
        </section>
      </section>
    </main>
  );
}

function Metric({ label, value, tone }: { label: string; value: string; tone?: "warning" }) {
  return <div className={tone ? `execution-metric execution-metric--${tone}` : "execution-metric"}><span>{label}</span><strong>{value}</strong></div>;
}

function trackedPosition(aircraft?: FleetAircraft): TrackedAircraft | undefined {
  const telemetry = aircraft?.telemetry;
  if (telemetry?.latitude == null || telemetry.longitude == null) return undefined;
  return { latitude: telemetry.latitude, longitude: telemetry.longitude, headingDegrees: telemetry.headingDeg, label: aircraft?.droneName, telemetryStatus: telemetry.status };
}

function trackedHomePosition(aircraft?: FleetAircraft): TrackedHome | undefined {
  const telemetry = aircraft?.telemetry;
  const home = telemetry?.homePosition;
  if (telemetry?.homePositionSet !== true || home?.latitude == null || home.longitude == null) return undefined;
  if (!Number.isFinite(home.latitude) || !Number.isFinite(home.longitude)) return undefined;
  return { latitude: home.latitude, longitude: home.longitude, absoluteAltitudeMeters: home.absoluteAltitudeM ?? undefined, label: aircraft?.droneName };
}

function validTrailPosition(telemetry: { latitude?: number | null; longitude?: number | null }) {
  if (telemetry.latitude == null || telemetry.longitude == null || !Number.isFinite(telemetry.latitude) || !Number.isFinite(telemetry.longitude)) return [];
  return [{ latitude: telemetry.latitude, longitude: telemetry.longitude }];
}

function mergeTrails(persisted: Array<{ latitude: number; longitude: number }>, live: Array<{ latitude: number; longitude: number }>) {
  const merged: Array<{ latitude: number; longitude: number }> = [];
  for (const position of [...persisted, ...live]) {
    const last = merged[merged.length - 1];
    if (!last || Math.abs(last.latitude - position.latitude) >= 0.0000005 || Math.abs(last.longitude - position.longitude) >= 0.0000005) merged.push(position);
  }
  return merged.slice(-500);
}

function preflightState(aircraft?: FleetAircraft) {
  const telemetry = aircraft?.telemetry;
  const health = telemetry?.health;
  const checks = [
    { label: "Aircraft link", ok: aircraft?.connectionStatus === "connected", detail: aircraft?.connectionStatus === "connected" ? "Agent connected" : "Waiting for connection" },
    { label: "Live telemetry", ok: telemetry?.status === "live", detail: telemetry?.status === "live" ? "Position and health are current" : "Waiting for current telemetry" },
    { label: "PX4 arming", ok: telemetry?.armed === true || health?.armable === true, detail: telemetry?.armed ? "Aircraft already armed" : health?.armable ? "PX4 reports armable" : "PX4 is not armable" },
    { label: "Global position", ok: health?.globalPositionOk === true, detail: health?.globalPositionOk ? "Global estimate ready" : "Waiting for global estimate" },
    { label: "Home position", ok: health?.homePositionOk === true && telemetry?.homePositionSet === true, detail: health?.homePositionOk && telemetry?.homePositionSet ? "Home position set" : "Waiting for home position" },
    { label: "Battery", ok: telemetry?.batteryPercent == null || telemetry.batteryPercent >= 15, detail: telemetry?.batteryPercent == null ? "No battery value reported" : `${Math.round(telemetry.batteryPercent)}% remaining` },
  ];
  return { checks, ready: checks.every((check) => check.ok) };
}

function messageFrom(reason: unknown) { return reason instanceof Error ? reason.message : String(reason); }
function shortId(value: string) { return value.slice(0, 8).toUpperCase(); }

function progressLabel(run: MissionRun) {
  if (run.status === "UPLOADING") return "Flight-controller upload";
  if (run.status === "READY") return "Uploaded · awaiting arm & start";
  if (run.status === "PAUSED") return "Paused · aircraft holding";
  if (run.status === "COMPLETED") return "Mission complete";
  if (run.status === "RTL") return "Mission ended · returning";
  if (run.status === "FAILED") return "Execution failed";
  return "Mission execution";
}

function waypointLabel(run: MissionRun) {
  if (run.currentWaypoint === undefined) return `— / ${run.totalWaypoints}`;
  if (run.status === "COMPLETED") return `${run.totalWaypoints} / ${run.totalWaypoints}`;
  return `${Math.min(run.currentWaypoint + 1, run.totalWaypoints)} / ${run.totalWaypoints}`;
}

function formatMetric(value: number | null | undefined, unit: string, fractionDigits = 1) { return value == null ? "—" : `${value.toFixed(fractionDigits)} ${unit}`; }
function formatClock(value: number) { return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(value); }
function formatDate(value: number) { return new Intl.DateTimeFormat(undefined, { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" }).format(value); }

function translationWarnings(run?: MissionRun) {
  if (!run) return [];
  const event = [...run.events].reverse().find((candidate) => candidate.eventType === "uploaded" && candidate.evidenceJson);
  if (!event?.evidenceJson) return [];
  try {
    const evidence = JSON.parse(event.evidenceJson) as { translationWarnings?: unknown };
    return Array.isArray(evidence.translationWarnings) ? evidence.translationWarnings.filter((value): value is string => typeof value === "string") : [];
  } catch {
    return [];
  }
}
