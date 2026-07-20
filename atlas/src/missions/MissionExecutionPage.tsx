import { useEffect, useMemo, useState } from "react";
import type { CSSProperties } from "react";
import { invoke } from "@tauri-apps/api/core";
import { highestRelatedOperationalAlert, type OperationalAlertList } from "../alerts/OperationalAlerts";
import type { FleetAircraft, FleetSnapshot, IncidentDetail, IncidentSnapshot } from "../operationsTypes";
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
  preferredDroneId?: string;
  lockedDroneId?: string;
  backLabel?: string;
  alerts: OperationalAlertList;
  onBack: () => void;
};

type ResponseLayout = "map" | "video" | "split";

type VehicleCommandReceipt = {
  id: string;
  status: string;
  deadlineAtUnixMs: number;
  resultCode: string;
  resultMessage: string;
};

const terminalStates = new Set(["COMPLETED", "FAILED", "CANCELLED", "RTL"]);
const terminalCommandStates = new Set(["succeeded", "failed", "rejected", "timed_out", "cancelled"]);

type TelemetryHistoryPage = {
  snapshots: Array<{
    telemetry: {
      receivedAtUnixMs: number;
      latitude?: number | null;
      longitude?: number | null;
    };
  }>;
};

export function MissionExecutionPage({ nativeAvailable, missionId, preferredDroneId, lockedDroneId, backLabel = "Mission planner", alerts, onBack }: MissionExecutionPageProps) {
  const [mission, setMission] = useState<Mission>();
  const [plan, setPlan] = useState<MissionPlan>();
  const [fleet, setFleet] = useState<FleetSnapshot>();
  const [runs, setRuns] = useState<MissionRun[]>([]);
  const [selectedDroneId, setSelectedDroneId] = useState(preferredDroneId ?? "");
  const [selectedRunId, setSelectedRunId] = useState<string>();
  const [pendingOperation, setPendingOperation] = useState<string>();
  const [error, setError] = useState<string>();
  const [followAircraft, setFollowAircraft] = useState(false);
  const [aircraftTrail, setAircraftTrail] = useState<Array<{ latitude: number; longitude: number }>>([]);
  const [payloadTarget, setPayloadTarget] = useState<PayloadTarget>();
  const [selectingPayloadTarget, setSelectingPayloadTarget] = useState(false);
  const [liveSurface, setLiveSurface] = useState<ResponseLayout>(() => storedExecutionLayout());
  const [responseIncident, setResponseIncident] = useState<IncidentSnapshot>();
  const [safetyCommandResult, setSafetyCommandResult] = useState<string>();

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
        setSelectedDroneId((current) => current || preferredDroneId || preferredRun?.droneId || connected || "");
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
  }, [missionId, nativeAvailable, preferredDroneId]);

  useEffect(() => {
    window.localStorage.setItem("atlas.execution.responseLayout", liveSurface);
  }, [liveSurface]);

  useEffect(() => {
    const incidentId = plan?.metadata.incidentResponse?.incidentId;
    if (!nativeAvailable || !incidentId) {
      setResponseIncident(undefined);
      return;
    }
    let active = true;
    async function refreshIncident() {
      try {
        const detail = await invoke<IncidentDetail>("incident_detail", { incidentId });
        if (active) setResponseIncident(detail.incident);
      } catch (reason) {
        if (active) setError(messageFrom(reason));
      }
    }
    void refreshIncident();
    const interval = window.setInterval(refreshIncident, 2_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [nativeAvailable, plan?.metadata.incidentResponse?.incidentId]);

  const missionRuns = useMemo(() => runs.filter((run) => run.missionId === missionId), [missionId, runs]);
  const selectedRun = runs.find((run) => run.id === selectedRunId) ?? missionRuns[0];
  const activeRun = missionRuns.find((run) => !terminalStates.has(run.status));
  const targetDroneId = selectedRun?.droneId || selectedDroneId;
  const targetAircraft = fleet?.aircraft.find((candidate) => candidate.droneId === targetDroneId);
  const connectedAircraft = fleet?.aircraft.filter((candidate) => candidate.connectionStatus === "connected" && candidate.droneId) ?? [];
  const deploymentAircraftOptions = lockedDroneId
    ? fleet?.aircraft.filter((candidate) => candidate.droneId === lockedDroneId) ?? []
    : connectedAircraft;
  const targetActiveRun = runs.find((run) => run.droneId === selectedDroneId && !terminalStates.has(run.status));
  const deploymentAircraft = fleet?.aircraft.find((candidate) => candidate.droneId === selectedDroneId);
  const uploadDistance = selectedDroneId ? missionDistanceStatus(plan?.generatedWaypoints[0], deploymentAircraft) : undefined;
  const trackedAircraft = trackedPosition(targetAircraft);
  const trackedHome = trackedHomePosition(targetAircraft);
  const preflight = preflightState(targetAircraft);
  const canUpload = Boolean(nativeAvailable && mission?.generatedPlanId && plan && selectedDroneId && uploadDistance?.ok && !activeRun && !targetActiveRun && !pendingOperation);
  const responseIncidentId = plan?.metadata.incidentResponse?.incidentId;
  const highestAlert = useMemo(
    () => highestRelatedOperationalAlert(alerts.alerts, {
      incidentId: responseIncidentId,
      droneId: targetDroneId || undefined,
      missionRunId: selectedRun?.id,
    }),
    [alerts.alerts, responseIncidentId, selectedRun?.id, targetDroneId],
  );
  const recordingPlanned = plan?.actions.some((action) => action.actionType === "START_RECORDING") ?? false;

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

  async function landAircraft() {
    if (!targetDroneId || !selectedRun || terminalStates.has(selectedRun.status)) return;
    const confirmed = window.confirm("Request immediate Land for this aircraft? Use only when the landing area is safe.");
    if (!confirmed) return;
    setPendingOperation("land");
    setError(undefined);
    setSafetyCommandResult(undefined);
    try {
      const initial = await invoke<VehicleCommandReceipt>("request_vehicle_command", {
        droneId: targetDroneId,
        commandType: "land",
        parametersJson: "{}",
        timeoutMs: 15_000,
      });
      const receipt = await awaitVehicleCommand(initial);
      if (receipt.status !== "succeeded") {
        throw new Error(receipt.resultMessage || receipt.resultCode || `Land command ${receipt.status}`);
      }
      setSafetyCommandResult(receipt.resultMessage || receipt.resultCode || "Land acknowledged by PX4");
    } catch (reason) {
      setError(messageFrom(reason));
    } finally {
      setPendingOperation(undefined);
    }
  }

  if (!mission || !plan) {
    return (
      <main className="execution-workspace execution-workspace--loading" id="main-content">
        <button type="button" className="execution-back" onClick={onBack}>← {backLabel}</button>
        <p>{nativeAvailable ? error || "Loading mission execution…" : "Atlas Native must be running to execute missions."}</p>
      </main>
    );
  }

  const progress = selectedRun?.status === "UPLOADING" ? selectedRun.uploadProgressPercent : selectedRun?.progressPercent ?? 0;
  const progressStyle = { "--mission-progress": `${Math.max(0, Math.min(100, progress))}%` } as CSSProperties;
  const warnings = translationWarnings(selectedRun);
  const latestEvent = selectedRun?.events[selectedRun.events.length - 1];
  const arrivalHold = selectedRun?.actions.find((action) => action.actionType === "HOLD_AT_ARRIVAL");
  const onSceneAcknowledged = arrivalHold?.state === "SUCCEEDED";
  const displayedRunState = onSceneAcknowledged && selectedRun && !terminalStates.has(selectedRun.status) ? "ON SCENE" : selectedRun?.status ?? "NOT UPLOADED";

  return (
    <main className="execution-workspace" id="main-content">
      <header className="execution-header">
        <div>
          <button type="button" className="execution-back" onClick={onBack}>← {backLabel}</button>
          <p className="eyebrow">Live mission execution</p>
          <h1>{mission.name}</h1>
          <p>{mission.templateType.replace(/_/g, " ")} · {plan.patternType.replace(/_/g, " ")} · {plan.generatedWaypoints.length} waypoints</p>
        </div>
        <span className={selectedRun ? `run-state run-state--${selectedRun.status.toLowerCase()}` : "run-state"}>{displayedRunState}</span>
      </header>

      {responseIncidentId && (
        <section className={`execution-response-context execution-response-context--${(responseIncident?.priority ?? "MEDIUM").toLowerCase()}`} aria-label="Incident response identity and safety controls">
          <div className="execution-response-context__identity">
            <span aria-hidden="true">{responseIncident?.priority === "CRITICAL" ? "▲" : "◆"}</span>
            <div>
              <small>{responseIncident?.priority ?? "INCIDENT RESPONSE"} · INCIDENT {shortId(responseIncidentId)}</small>
              <strong>{responseIncident?.summary || mission.name}</strong>
            </div>
          </div>
          <dl>
            <div><dt>Run state</dt><dd>{displayedRunState}</dd></div>
            <div><dt>Plan revision</dt><dd>{plan.metadata.incidentResponse?.incidentRevision ?? "—"}</dd></div>
            <div><dt>Current revision</dt><dd>{responseIncident?.revision ?? "Loading"}</dd></div>
            <div><dt>Safety alert</dt><dd>{highestAlert ? `${highestAlert.severity} · ${highestAlert.title}` : "No active response alert"}</dd></div>
          </dl>
          <div className="execution-response-context__controls">
            <button type="button" disabled={selectedRun?.status !== "RUNNING" || Boolean(pendingOperation)} onClick={() => void control("pause")}>Hold</button>
            <button type="button" disabled={!selectedRun || !["RUNNING", "PAUSED"].includes(selectedRun.status) || Boolean(pendingOperation)} onClick={() => void control("return_to_launch")}>RTL</button>
            <button type="button" className="execution-response-context__land" disabled={!selectedRun || terminalStates.has(selectedRun.status) || targetAircraft?.connectionStatus !== "connected" || targetAircraft.telemetry?.status !== "live" || targetAircraft.telemetry.inAir !== true || Boolean(pendingOperation)} onClick={() => void landAircraft()}>Land</button>
            <small>{safetyCommandResult || "Safety actions remain available in every response layout."}</small>
          </div>
        </section>
      )}

      <section className="execution-live-grid" aria-label="Live mission operation">
        <div className="execution-map-column">
          <div className={`execution-live-surfaces execution-live-surfaces--${liveSurface}`} data-execution-layout={liveSurface}>
            <div className="execution-live-panel execution-live-panel--map">
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
            </div>
            <div className="execution-live-panel execution-live-panel--video">
              <LiveVideo
                nativeAvailable={nativeAvailable}
                droneId={targetDroneId || undefined}
                aircraft={targetAircraft}
                highestAlert={highestAlert}
                recordingPlanned={recordingPlanned}
                recordingContext={{
                  incidentId: responseIncidentId,
                  missionId,
                  missionRunId: selectedRun?.id,
                }}
                compact={liveSurface === "map"}
              />
            </div>
          </div>
          <div className="execution-map-toolbar">
            <div className="execution-surface-switch" role="group" aria-label="Live operation view">
              {(["map", "video", "split"] as ResponseLayout[]).map((option) => (
                <button key={option} type="button" className={liveSurface === option ? "execution-surface-switch--active" : ""} aria-pressed={liveSurface === option} onClick={() => setLiveSurface(option)}>{option === "map" ? "Mission map" : option === "video" ? "Video + perception" : "Split"}</button>
              ))}
            </div>
            <button type="button" className={followAircraft ? "follow-control follow-control--active" : "follow-control"} onClick={() => setFollowAircraft((current) => !current)} disabled={!trackedAircraft}>
              {followAircraft ? "Following aircraft" : "Follow aircraft"}
            </button>
            <span>{trackedAircraft ? `${trackedAircraft.latitude.toFixed(6)}, ${trackedAircraft.longitude.toFixed(6)}` : targetAircraft?.droneName || targetDroneId || "Waiting for aircraft position"}</span>
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
                <select value={selectedDroneId} onChange={(event) => setSelectedDroneId(event.target.value)} disabled={Boolean(lockedDroneId)}>
                  <option value="">Select connected aircraft</option>
                  {deploymentAircraftOptions.map((candidate) => <option key={candidate.droneId!} value={candidate.droneId!}>{candidate.droneName || candidate.droneId}</option>)}
                </select>
              </label>
              {lockedDroneId && <p className="execution-command-note">Aircraft selection is locked to the operator-reviewed incident assignment.</p>}
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

              {selectedRun.actions.length > 0 && <ArrivalActionRuntime run={selectedRun} />}

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

function ArrivalActionRuntime({ run }: { run: MissionRun }) {
  const hold = run.actions.find((action) => action.actionType === "HOLD_AT_ARRIVAL");
  const onScene = hold?.state === "SUCCEEDED";
  const failed = run.actions.some((action) => action.state === "FAILED" || action.state === "POLICY_APPLIED");
  return (
    <section className={onScene ? "execution-card arrival-runtime arrival-runtime--on-scene" : failed ? "execution-card arrival-runtime arrival-runtime--failed" : "execution-card arrival-runtime"}>
      <div className="execution-card__title"><span>02</span><strong>Arrival acknowledgement</strong></div>
      <header className="arrival-runtime__status">
        <span>{onScene ? "Hold acknowledged" : failed ? "Arrival action unresolved" : "Not yet on scene"}</span>
        <strong>{onScene ? "ON SCENE" : hold ? displayActionState(hold.state) : "WAITING"}</strong>
      </header>
      <ol className="arrival-runtime__actions">
        {run.actions.map((action) => (
          <li key={action.id} className={`arrival-runtime__action arrival-runtime__action--${action.state.toLowerCase()}`}>
            <span aria-hidden="true">{action.state === "SUCCEEDED" ? "✓" : action.state === "FAILED" || action.state === "POLICY_APPLIED" ? "!" : String(action.actionSequence + 1).padStart(2, "0")}</span>
            <div>
              <strong>{actionLabel(action.actionType)}</strong>
              <small>{actionStateDetail(action.state, action.attempt, action.maxAttempts)}</small>
              <small>{formatActionRuntimePolicy(action)}</small>
              {action.errorMessage && <small className="arrival-runtime__error">{action.errorMessage}</small>}
            </div>
          </li>
        ))}
      </ol>
      <p><span>Reviewed failure policy</span><strong>{failurePolicyLabel(hold?.failurePolicy)}</strong></p>
      <small className="arrival-runtime__boundary">Waypoint progress cannot set ON SCENE. Only the durable Hold acknowledgement can.</small>
    </section>
  );
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

function storedExecutionLayout(): ResponseLayout {
  try {
    const stored = window.localStorage.getItem("atlas.execution.responseLayout");
    return stored === "map" || stored === "video" || stored === "split" ? stored : "map";
  } catch {
    return "map";
  }
}

async function awaitVehicleCommand(initial: VehicleCommandReceipt) {
  let current = initial;
  while (!terminalCommandStates.has(current.status) && Date.now() <= initial.deadlineAtUnixMs + 1_500) {
    await new Promise((resolve) => window.setTimeout(resolve, 200));
    current = await invoke<VehicleCommandReceipt>("vehicle_command_detail", { commandId: initial.id });
  }
  if (!terminalCommandStates.has(current.status)) throw new Error("Land command did not reach a terminal state before its deadline.");
  return current;
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

function actionLabel(actionType: string) {
  return actionType === "HOLD_AT_ARRIVAL" ? "Hold at arrival" : actionType === "POINT_GIMBAL_AT_INCIDENT" ? "Point gimbal at incident" : actionType === "RESUME_AFTER_ARRIVAL" ? "Resume operational pattern" : actionType.replace(/_/g, " ");
}

function displayActionState(state: string) {
  return state.replace(/_/g, " ");
}

function actionStateDetail(state: string, attempt: number, maximum: number) {
  if (state === "REQUESTED") return "Requested from immutable plan · waiting for final waypoint";
  if (state === "RUNNING") return `Running attempt ${attempt} of ${maximum}`;
  if (state === "RETRYING") return `Retrying after attempt ${attempt} of ${maximum}`;
  if (state === "SUCCEEDED") return `Acknowledged on attempt ${attempt}`;
  if (state === "FAILED") return `Failed after attempt ${attempt} of ${maximum}`;
  return "Reviewed failure policy applied";
}

function failurePolicyLabel(policy?: string) {
  return policy === "RETURN_TO_LAUNCH"
    ? "Return to launch"
    : policy === "OPERATOR_INTERVENTION"
      ? "Operator intervention"
      : policy === "SKIP_OPTIONAL_AND_NOTIFY"
        ? "Skip optional action and notify"
        : "—";
}

function formatActionRuntimePolicy(action: MissionRun["actions"][number]) {
  const timing = `${Math.round(action.timeoutMs / 1_000)} s timeout · ${Math.round(action.retryInitialDelayMs / 1_000)} s retry ×${action.retryBackoffMultiplier.toFixed(1)}`;
  const deadline = action.state === "RUNNING" && action.attemptDeadlineAtUnixMs
    ? ` · deadline ${formatDate(action.attemptDeadlineAtUnixMs)}`
    : action.state === "RETRYING" && action.nextAttemptAtUnixMs
      ? ` · next ${formatDate(action.nextAttemptAtUnixMs)}`
      : "";
  return `${timing} · ${failurePolicyLabel(action.failurePolicy)}${deadline}`;
}
