import { useEffect, useMemo, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import type { FleetAircraft } from "../operationsTypes";
import type { PayloadTarget } from "./OperationalMissionMap";
import type { MissionPlan, MissionRun } from "./missionTypes";

type CommandReceipt = {
  id: string;
  status: string;
  resultCode: string;
  resultMessage: string;
  deadlineAtUnixMs: number;
};

type OverrideState = "automatic" | "acquiring" | "manual" | "restoring";

type MissionPayloadControlProps = {
  run: MissionRun;
  plan: MissionPlan;
  aircraft?: FleetAircraft;
  payloadTarget?: PayloadTarget;
  selectingPayloadTarget: boolean;
  onSelectingPayloadTargetChange: (selecting: boolean) => void;
  onClearPayloadTarget: () => void;
};

const terminalCommandStates = new Set(["succeeded", "failed", "rejected", "timed_out", "cancelled"]);
const leaseDurationMs = 7_000;

export function MissionPayloadControl({
  run,
  plan,
  aircraft,
  payloadTarget,
  selectingPayloadTarget,
  onSelectingPayloadTargetChange,
  onClearPayloadTarget,
}: MissionPayloadControlProps) {
  const capabilities = aircraft?.agentCapabilities ?? [];
  const gimbalDetected = capabilities.includes("gimbal:detected");
  const roiSupported = capabilities.includes("gimbal:roi");
  const zoomSupported = capabilities.includes("camera:zoom:range");
  const gimbalId = capabilityNumber(capabilities, /^gimbal:id:(\d+)$/);
  const cameraComponentId = capabilityNumber(capabilities, /^camera:component_id:(\d+)$/);
  const missionView = useMemo(() => currentMissionView(plan, run.currentWaypoint), [plan, run.currentWaypoint]);
  const sessionRef = useRef<string | undefined>(undefined);
  const [overrideState, setOverrideState] = useState<OverrideState>("automatic");
  const [pitch, setPitch] = useState(missionView.pitchDegrees);
  const [yaw, setYaw] = useState(missionView.yawDegrees);
  const [yawFrame, setYawFrame] = useState<"AIRCRAFT_RELATIVE" | "NORTH_LOCKED">("AIRCRAFT_RELATIVE");
  const [zoomPercent, setZoomPercent] = useState(missionView.zoomPercent);
  const [targetHeightAboveGround, setTargetHeightAboveGround] = useState(0);
  const [manualTargetAltitude, setManualTargetAltitude] = useState<number | undefined>(aircraft?.telemetry?.homePosition?.absoluteAltitudeM ?? undefined);
  const [pendingLabel, setPendingLabel] = useState<string>();
  const [result, setResult] = useState<string>();
  const [error, setError] = useState<string>();
  const activeRun = run.status === "RUNNING" || run.status === "PAUSED";
  const linkReady = aircraft?.connectionStatus === "connected";
  const manual = overrideState === "manual";
  const terrainElevation = payloadTarget?.terrainElevationMeters;
  const targetAltitude = terrainElevation === undefined ? manualTargetAltitude : terrainElevation + targetHeightAboveGround;

  useEffect(() => {
    if (overrideState !== "automatic") return;
    setPitch(missionView.pitchDegrees);
    setYaw(missionView.yawDegrees);
    setZoomPercent(missionView.zoomPercent);
  }, [missionView.pitchDegrees, missionView.yawDegrees, missionView.zoomPercent, overrideState]);

  useEffect(() => {
    if (manualTargetAltitude !== undefined) return;
    const homeAltitude = aircraft?.telemetry?.homePosition?.absoluteAltitudeM;
    if (homeAltitude != null) setManualTargetAltitude(homeAltitude);
  }, [aircraft?.telemetry?.homePosition?.absoluteAltitudeM, manualTargetAltitude]);

  useEffect(() => {
    setTargetHeightAboveGround(0);
  }, [payloadTarget?.latitude, payloadTarget?.longitude]);

  useEffect(() => {
    if (!manual || !sessionRef.current) return;
    const interval = window.setInterval(() => {
      const sessionID = sessionRef.current;
      if (!sessionID) return;
      void dispatchCommand("payload_control_renew", {
        ...commandIdentity(run.id, sessionID, gimbalId, cameraComponentId),
        leaseDurationMs,
      }, true).catch((reason) => {
        setError(`Manual-control lease could not be renewed. Mission view will restore automatically: ${messageFrom(reason)}`);
      });
    }, 3_000);
    return () => window.clearInterval(interval);
  }, [cameraComponentId, gimbalId, manual, run.id]);

  useEffect(() => {
    return () => {
      const sessionID = sessionRef.current;
      if (!sessionID || !aircraft?.droneId) return;
      void invoke("request_vehicle_command", {
        droneId: aircraft.droneId,
        commandType: "payload_control_end",
        parametersJson: JSON.stringify(commandIdentity(run.id, sessionID, gimbalId, cameraComponentId)),
        timeoutMs: 15_000,
      });
    };
  }, [aircraft?.droneId, cameraComponentId, gimbalId, run.id]);

  useEffect(() => {
    if (activeRun || !sessionRef.current) return;
    sessionRef.current = undefined;
    setOverrideState("automatic");
    onSelectingPayloadTargetChange(false);
    onClearPayloadTarget();
  }, [activeRun, onClearPayloadTarget, onSelectingPayloadTargetChange]);

  async function dispatchCommand(commandType: string, parameters: Record<string, unknown>, quiet = false) {
    if (!aircraft?.droneId) throw new Error("No aircraft is attached to this run.");
    if (!quiet) {
      setPendingLabel(commandLabel(commandType));
      setError(undefined);
    }
    try {
      const initial = await invoke<CommandReceipt>("request_vehicle_command", {
        droneId: aircraft.droneId,
        commandType,
        parametersJson: JSON.stringify(parameters),
        timeoutMs: 15_000,
      });
      const receipt = await awaitCommandResult(initial);
      if (receipt.status !== "succeeded") {
        throw new Error(receipt.resultMessage || receipt.resultCode || `Command ${receipt.status}`);
      }
      if (!quiet) setResult(receipt.resultMessage || receipt.resultCode || "Command acknowledged");
      return receipt;
    } finally {
      if (!quiet) setPendingLabel(undefined);
    }
  }

  async function beginManual() {
    if (!activeRun || !linkReady || !gimbalDetected) return;
    const sessionID = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
    sessionRef.current = sessionID;
    setOverrideState("acquiring");
    setError(undefined);
    try {
      await dispatchCommand("payload_control_begin", {
        ...commandIdentity(run.id, sessionID, gimbalId, cameraComponentId),
        leaseDurationMs,
      });
      setOverrideState("manual");
    } catch (reason) {
      sessionRef.current = undefined;
      setOverrideState("automatic");
      setError(messageFrom(reason));
    }
  }

  async function endManual() {
    const sessionID = sessionRef.current;
    if (!sessionID) return;
    setOverrideState("restoring");
    onSelectingPayloadTargetChange(false);
    try {
      await dispatchCommand("payload_control_end", commandIdentity(run.id, sessionID, gimbalId, cameraComponentId));
      sessionRef.current = undefined;
      setOverrideState("automatic");
      onClearPayloadTarget();
    } catch (reason) {
      // Stop renewing after an end request even when restoration fails. The
      // Agent clears the session before it restores mission intent; if the
      // command never arrived, its short lease provides the same fail-safe.
      sessionRef.current = undefined;
      setOverrideState("automatic");
      onClearPayloadTarget();
      setError(`Manual control ended, but Atlas could not confirm the mission view was restored: ${messageFrom(reason)}`);
    }
  }

  async function manualCommand(commandType: string, parameters: Record<string, unknown>) {
    const sessionID = sessionRef.current;
    if (!sessionID || !manual) return;
    try {
      await dispatchCommand(commandType, {
        ...commandIdentity(run.id, sessionID, gimbalId, cameraComponentId),
        ...parameters,
      });
    } catch (reason) {
      setError(messageFrom(reason));
    }
  }

  function rate(pitchRateDegreesPerSecond: number, yawRateDegreesPerSecond: number) {
    void manualCommand("gimbal_set_rates", { pitchRateDegreesPerSecond, yawRateDegreesPerSecond, yawFrame });
  }

  function stopRate() {
    rate(0, 0);
  }

  const baseStateLabel = !gimbalDetected
    ? "No gimbal detected"
    : !activeRun
      ? "Available during mission"
      : overrideState === "manual"
        ? "Manual control"
        : overrideState === "acquiring"
          ? "Taking control"
          : overrideState === "restoring"
            ? "Restoring mission view"
            : "Mission automatic";

  return (
    <section
      className={manual ? "execution-card payload-control payload-control--manual" : "execution-card payload-control"}
      aria-labelledby="payload-control-title"
      tabIndex={manual ? 0 : undefined}
      onKeyDown={(event) => {
        if (!manual || event.repeat) return;
        const controls: Record<string, [number, number]> = { ArrowUp: [15, 0], ArrowDown: [-15, 0], ArrowLeft: [0, -15], ArrowRight: [0, 15] };
        const value = controls[event.key];
        if (value) {
          event.preventDefault();
          rate(value[0], value[1]);
        }
      }}
      onKeyUp={(event) => {
        if (manual && event.key.startsWith("Arrow")) stopRate();
      }}
      onBlur={(event) => {
        if (manual && !event.currentTarget.contains(event.relatedTarget)) stopRate();
      }}
    >
      <div className="execution-card__title payload-control__title">
        <span>02</span>
        <strong id="payload-control-title">Camera & gimbal</strong>
        <i className={manual ? "payload-state payload-state--manual" : "payload-state"}>{baseStateLabel}</i>
      </div>

      <div className="payload-mission-view">
        <span>Mission view now</span>
        <strong>{displayMode(missionView.cameraMode)}</strong>
        <small>{missionView.pitchDegrees}° pitch · {displayMode(missionView.yawMode)}{zoomSupported ? ` · ${Math.round(missionView.zoomPercent)}% zoom` : ""}</small>
      </div>

      {!manual && overrideState === "automatic" && (
        <>
          <button type="button" className="execution-secondary-action payload-take-control" disabled={!activeRun || !linkReady || !gimbalDetected || Boolean(pendingLabel)} onClick={() => void beginManual()}>
            Take manual payload control
          </button>
          <p className="execution-command-note">{gimbalDetected ? "Flight continues on its mission route. Atlas restores the current mission view when manual control ends." : "The connected Agent has not discovered a MAVLink gimbal."}</p>
        </>
      )}

      {(overrideState === "acquiring" || overrideState === "restoring") && (
        <p className="payload-transition" role="status">{overrideState === "acquiring" ? "Claiming primary gimbal control…" : "Stopping manual movement and restoring mission intent…"}</p>
      )}

      {manual && (
        <div className="payload-manual-controls">
          <div className="payload-angle-fields">
            <label>Pitch<input type="number" min={-90} max={30} value={pitch} onChange={(event) => setPitch(Number(event.target.value))} /><small>degrees</small></label>
            <label>Gimbal yaw<input type="number" min={-180} max={180} value={yaw} onChange={(event) => setYaw(Number(event.target.value))} /><small>degrees</small></label>
            <label>Yaw reference<select value={yawFrame} onChange={(event) => setYawFrame(event.target.value as typeof yawFrame)}><option value="AIRCRAFT_RELATIVE">Aircraft nose</option><option value="NORTH_LOCKED">North locked</option></select></label>
            <div className="payload-angle-actions">
              <button type="button" disabled={Boolean(pendingLabel)} onClick={() => {
                onClearPayloadTarget();
                void manualCommand("gimbal_set_angles", { pitchDegrees: pitch, yawDegrees: yaw, yawFrame });
              }}>Set view</button>
              <button type="button" disabled={Boolean(pendingLabel)} onClick={() => {
                onClearPayloadTarget();
                void manualCommand("gimbal_center", {});
              }}>Centre</button>
            </div>
          </div>

          <div className="payload-rate-pad" aria-label="Gimbal rate control">
            <span>Rate pad<small>Arrow keys supported</small></span>
            <RateButton label="Tilt up" glyph="↑" onStart={() => rate(15, 0)} onStop={stopRate} />
            <RateButton label="Pan left" glyph="←" onStart={() => rate(0, -15)} onStop={stopRate} />
            <button type="button" aria-label="Stop gimbal" onClick={stopRate}>■</button>
            <RateButton label="Pan right" glyph="→" onStart={() => rate(0, 15)} onStop={stopRate} />
            <RateButton label="Tilt down" glyph="↓" onStart={() => rate(-15, 0)} onStop={stopRate} />
          </div>

          {roiSupported && (
            <div className="payload-roi">
              <div><strong>Look at map target</strong><span>Geographic ROI</span></div>
              <button type="button" className={selectingPayloadTarget ? "payload-map-select payload-map-select--active" : "payload-map-select"} onClick={() => onSelectingPayloadTargetChange(!selectingPayloadTarget)}>
                {selectingPayloadTarget ? "Cancel map selection" : payloadTarget ? "Change map target" : "Choose on map"}
              </button>
              {payloadTarget && (
                <>
                  <output>{payloadTarget.latitude.toFixed(6)}, {payloadTarget.longitude.toFixed(6)}</output>
                  {terrainElevation !== undefined ? (
                    <div className="payload-roi__terrain">
                      <span>DEM ground elevation<strong>{terrainElevation.toFixed(1)} m AMSL</strong></span>
                      <small>{payloadTarget.terrainSource ?? "Configured terrain source"}</small>
                    </div>
                  ) : (
                    <p className="payload-roi__terrain-warning">DEM elevation was unavailable at this point. Enter a verified target altitude.</p>
                  )}
                  {terrainElevation !== undefined ? (
                    <label>Target height above ground<input type="number" min={-20} max={500} step={1} value={targetHeightAboveGround} onChange={(event) => setTargetHeightAboveGround(Number(event.target.value))} /><small>ROI target · {(terrainElevation + targetHeightAboveGround).toFixed(1)} m AMSL</small></label>
                  ) : (
                    <label>Target altitude AMSL<input type="number" step={1} value={manualTargetAltitude ?? ""} onChange={(event) => setManualTargetAltitude(optionalNumber(event.target.value))} /><small>MAVLink geographic ROI requires an AMSL target altitude.</small></label>
                  )}
                  <button type="button" disabled={targetAltitude === undefined || Boolean(pendingLabel)} onClick={() => void manualCommand("gimbal_set_roi", { latitude: payloadTarget.latitude, longitude: payloadTarget.longitude, altitudeAmslMeters: targetAltitude })}>Look at target</button>
                </>
              )}
            </div>
          )}

          {zoomSupported && (
            <label className="payload-zoom">
              <span>Digital zoom<strong>{Math.round(zoomPercent)}%</strong></span>
              <input type="range" min={0} max={100} step={5} value={zoomPercent} onChange={(event) => setZoomPercent(Number(event.target.value))} />
              <button type="button" disabled={Boolean(pendingLabel)} onClick={() => void manualCommand("camera_set_zoom", { zoomPercent })}>Apply zoom</button>
            </label>
          )}

          <button type="button" className="execution-primary-action payload-return" disabled={Boolean(pendingLabel)} onClick={() => void endManual()}>Return to mission view</button>
        </div>
      )}

      {(pendingLabel || result || error) && <p className={error ? "payload-command-result payload-command-result--error" : "payload-command-result"} role="status">{error ?? pendingLabel ?? result}</p>}
    </section>
  );
}

function RateButton({ label, glyph, onStart, onStop }: { label: string; glyph: string; onStart: () => void; onStop: () => void }) {
  return <button type="button" aria-label={label} onPointerDown={(event) => { event.currentTarget.setPointerCapture(event.pointerId); onStart(); }} onPointerUp={onStop} onPointerCancel={onStop} onLostPointerCapture={onStop}>{glyph}</button>;
}

function commandIdentity(missionRunId: string, controlSessionId: string, gimbalId: number, cameraComponentId: number) {
  return { missionRunId, controlSessionId, gimbalId, cameraComponentId };
}

async function awaitCommandResult(initial: CommandReceipt) {
  let current = initial;
  while (!terminalCommandStates.has(current.status) && Date.now() <= initial.deadlineAtUnixMs + 1_500) {
    await new Promise((resolve) => window.setTimeout(resolve, 200));
    current = await invoke<CommandReceipt>("vehicle_command_detail", { commandId: initial.id });
  }
  if (!terminalCommandStates.has(current.status)) throw new Error("Command status did not reach a terminal state before its deadline.");
  return current;
}

function currentMissionView(plan: MissionPlan, currentWaypoint = 0) {
  const view = { cameraMode: "FORWARD_OBLIQUE", pitchDegrees: -35, yawDegrees: 0, yawMode: "FOLLOW_DRONE_HEADING", zoomPercent: 0 };
  for (const action of plan.actions) {
    const waypoint = finiteNumber(action.params.waypointSequence);
    if (waypoint !== undefined && waypoint !== currentWaypoint) continue;
    if (action.actionType === "SET_CAMERA_MODE" && typeof action.params.cameraMode === "string") view.cameraMode = action.params.cameraMode;
    if (action.actionType === "SET_GIMBAL_ORIENTATION") {
      view.pitchDegrees = finiteNumber(action.params.pitchDegrees) ?? view.pitchDegrees;
      view.yawDegrees = finiteNumber(action.params.yawDegrees) ?? view.yawDegrees;
      if (typeof action.params.yawMode === "string") view.yawMode = action.params.yawMode;
    }
    if (action.actionType === "SET_CAMERA_ZOOM") view.zoomPercent = finiteNumber(action.params.zoomPercent) ?? view.zoomPercent;
  }
  return view;
}

function capabilityNumber(capabilities: string[], pattern: RegExp) {
  for (const capability of capabilities) {
    const match = pattern.exec(capability);
    if (match) return Number(match[1]);
  }
  return 0;
}

function commandLabel(commandType: string) {
  if (commandType === "payload_control_begin") return "Taking manual payload control…";
  if (commandType === "payload_control_end") return "Restoring mission payload view…";
  if (commandType === "gimbal_set_roi") return "Sending geographic look target…";
  if (commandType === "camera_set_zoom") return "Setting camera zoom…";
  return "Sending payload command…";
}

function displayMode(value: string) {
  return value.replace(/_/g, " ").toLowerCase().replace(/(^|\s)\S/g, (letter) => letter.toUpperCase());
}

function finiteNumber(value: unknown) {
  const number = Number(value);
  return Number.isFinite(number) ? number : undefined;
}

function optionalNumber(value: string) {
  if (value.trim() === "") return undefined;
  const number = Number(value);
  return Number.isFinite(number) ? number : undefined;
}

function messageFrom(reason: unknown) {
  return reason instanceof Error ? reason.message : String(reason);
}
