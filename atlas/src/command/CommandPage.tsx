import { useEffect, useMemo, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import type { OperationalAlertList } from "../alerts/OperationalAlerts";
import type { AircraftFollowSession } from "../follow/followTypes";
import type { MissionRun } from "../missions/missionTypes";
import {
  defaultOperationsMapLayers,
  OperationsMap,
} from "../operations/OperationsMap";
import type {
  FleetAircraft,
  FleetSnapshot,
  IncidentSnapshot,
  OperationalTrackGeolocation,
} from "../operationsTypes";
import {
  classifyVehicleCommandReceipt,
  pollVehicleCommand,
  type VehicleCommandOutcome,
  type VehicleCommandReceipt,
} from "./vehicleCommandPolling";
import "./CommandPage.css";

type CommandPageProps = {
  nativeAvailable: boolean;
  fleet: FleetSnapshot;
  alerts: OperationalAlertList;
  missionRuns: MissionRun[];
  followSessions: AircraftFollowSession[];
  preferredDroneId?: string;
  onOpenAircraft: (droneId: string) => void;
  onOpenDispatch: () => void;
  onOpenFollow: () => void;
  onOpenMission: (missionId: string, droneId: string) => void;
};

const activeMissionStates = new Set(["UPLOADING", "READY", "RUNNING", "PAUSED", "ROUTE_COMPLETE", "RTL"]);
const safetyCommandTimeoutMs = 15_000;

type AircraftAuthorityTone =
  | "authority"
  | "critical"
  | "nominal"
  | "caution"
  | "idle"
  | "offline";

type AircraftAuthority = {
  label: string;
  activity: string;
  detail: string;
  tone: AircraftAuthorityTone;
  progress?: number;
  progressLabel?: string;
};

const authorityToneClasses: Record<
  AircraftAuthorityTone,
  { aircraft: string; badge: string; detail: string }
> = {
  authority: {
    aircraft: "command-aircraft--authority",
    badge: "command-authority--authority",
    detail: "command-detail__state--authority",
  },
  critical: {
    aircraft: "command-aircraft--critical",
    badge: "command-authority--critical",
    detail: "command-detail__state--critical",
  },
  nominal: {
    aircraft: "command-aircraft--nominal",
    badge: "command-authority--nominal",
    detail: "command-detail__state--nominal",
  },
  caution: {
    aircraft: "command-aircraft--caution",
    badge: "command-authority--caution",
    detail: "command-detail__state--caution",
  },
  idle: {
    aircraft: "command-aircraft--idle",
    badge: "command-authority--idle",
    detail: "command-detail__state--idle",
  },
  offline: {
    aircraft: "command-aircraft--offline",
    badge: "command-authority--offline",
    detail: "command-detail__state--offline",
  },
};

type CommandNotice = {
  tone: "neutral" | "nominal" | "caution" | "critical";
  message: string;
  receipt?: VehicleCommandReceipt;
};

export function CommandPage({
  nativeAvailable,
  fleet,
  alerts,
  missionRuns,
  followSessions,
  preferredDroneId,
  onOpenAircraft,
  onOpenDispatch,
  onOpenFollow,
  onOpenMission,
}: CommandPageProps) {
  const [incidents, setIncidents] = useState<IncidentSnapshot[]>([]);
  const [trackGeolocations, setTrackGeolocations] = useState<OperationalTrackGeolocation[]>([]);
  const [selectedDroneId, setSelectedDroneId] = useState<string>();
  const [selectedIncidentId, setSelectedIncidentId] = useState<string>();
  const [loadError, setLoadError] = useState<string>();
  const [pendingCommand, setPendingCommand] = useState<string>();
  const [commandNotice, setCommandNotice] = useState<CommandNotice>();
  const [confirmingCommand, setConfirmingCommand] = useState<
    "return_to_launch" | "land"
  >();
  const appliedPreferredDroneId = useRef<string | undefined>(undefined);

  const operationalAircraft = useMemo(
    () => fleet.aircraft.filter((aircraft) => aircraft.vehicleStatus !== "archived"),
    [fleet.aircraft],
  );
  const liveFollowSessions = useMemo(
    () => followSessions.filter((session) => session.state !== "ENDED"),
    [followSessions],
  );
  const activeMissionRuns = useMemo(
    () => missionRuns.filter((run) => !run.completedAtUnixMs && activeMissionStates.has(run.status)),
    [missionRuns],
  );

  useEffect(() => {
    if (!nativeAvailable) {
      setIncidents([]);
      setTrackGeolocations([]);
      return;
    }
    let active = true;
    let reading = false;
    async function refreshOperationalPicture() {
      if (reading) return;
      reading = true;
      try {
        const [nextIncidents, nextTracks] = await Promise.all([
          invoke<IncidentSnapshot[]>("incident_list", { includeClosed: false, limit: 250 }),
          invoke<OperationalTrackGeolocation[]>("operational_track_geolocations", { limit: 250 }),
        ]);
        if (active) {
          setIncidents(nextIncidents);
          setTrackGeolocations(nextTracks);
          setLoadError(undefined);
        }
      } catch (reason) {
        if (active) setLoadError(messageFrom(reason));
      } finally {
        reading = false;
      }
    }
    void refreshOperationalPicture();
    const interval = window.setInterval(refreshOperationalPicture, 2_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [nativeAvailable]);

  useEffect(() => {
    if (
      preferredDroneId
      && preferredDroneId !== appliedPreferredDroneId.current
      && operationalAircraft.some((aircraft) => aircraft.droneId === preferredDroneId)
    ) {
      setSelectedDroneId(preferredDroneId);
      appliedPreferredDroneId.current = preferredDroneId;
    }
  }, [operationalAircraft, preferredDroneId]);

  useEffect(() => {
    if (
      selectedDroneId
      && operationalAircraft.some((aircraft) => aircraft.droneId === selectedDroneId)
    ) {
      return;
    }
    const authorityDroneId = liveFollowSessions[0]?.droneId
      ?? activeMissionRuns[0]?.droneId;
    const initial = operationalAircraft.find((aircraft) => aircraft.droneId === authorityDroneId)
      ?? operationalAircraft.find((aircraft) => aircraft.telemetry?.inAir)
      ?? operationalAircraft.find((aircraft) => aircraft.connectionStatus === "connected")
      ?? operationalAircraft[0];
    setSelectedDroneId(initial?.droneId ?? undefined);
  }, [activeMissionRuns, liveFollowSessions, operationalAircraft, selectedDroneId]);

  useEffect(() => {
    setConfirmingCommand(undefined);
    setCommandNotice(undefined);
  }, [selectedDroneId]);

  const selectedAircraft = operationalAircraft.find(
    (aircraft) => aircraft.droneId === selectedDroneId,
  );
  const selectedIncident = incidents.find((incident) => incident.id === selectedIncidentId);
  const connectedCount = operationalAircraft.filter(
    (aircraft) => aircraft.connectionStatus === "connected",
  ).length;
  const airborneCount = operationalAircraft.filter(
    (aircraft) => aircraft.telemetry?.inAir,
  ).length;

  async function requestSafetyCommand(commandType: "hold" | "return_to_launch" | "land") {
    if (!selectedAircraft?.droneId || pendingCommand) return;
    setConfirmingCommand(undefined);
    setPendingCommand(commandType);
    setCommandNotice({
      tone: "neutral",
      message: `Sending ${commandLabel(commandType).toLowerCase()}…`,
    });
    const requestedAtUnixMs = Date.now();
    let initialReceipt: VehicleCommandReceipt | undefined;
    try {
      initialReceipt = await invoke<VehicleCommandReceipt>("request_vehicle_command", {
        droneId: selectedAircraft.droneId,
        commandType,
        parametersJson: "{}",
        timeoutMs: safetyCommandTimeoutMs,
      });
      const outcome = await pollVehicleCommand(initialReceipt, {
        timeoutMs: safetyCommandTimeoutMs,
        requestedAtUnixMs,
        readReceipt: (commandId) => invoke<VehicleCommandReceipt>(
          "vehicle_command_detail",
          { commandId },
        ),
      });
      setCommandNotice(noticeForCommandOutcome(outcome, commandType));
    } catch (reason) {
      setCommandNotice(initialReceipt
        ? {
            tone: "caution",
            message: "Command status could not be refreshed — check the aircraft.",
            receipt: initialReceipt,
          }
        : {
            tone: "critical",
            message: messageFrom(reason),
          });
    } finally {
      setPendingCommand(undefined);
    }
  }

  async function refreshCommandReceipt() {
    if (!commandNotice?.receipt || pendingCommand) return;
    setPendingCommand(commandNotice.receipt.commandType);
    try {
      const receipt = await invoke<VehicleCommandReceipt>("vehicle_command_detail", {
        commandId: commandNotice.receipt.id,
      });
      setCommandNotice(noticeForCommandOutcome(
        classifyVehicleCommandReceipt(receipt),
        receipt.commandType,
      ));
    } catch {
      setCommandNotice({
        ...commandNotice,
        tone: "caution",
        message: "Command status is still unavailable — check the aircraft.",
      });
    } finally {
      setPendingCommand(undefined);
    }
  }

  return (
    <main className="command-workspace" id="main-content">
      <section className="command-panel command-fleet" aria-label="Fleet">
        <header className="command-panel__head">
          <h1>Fleet</h1>
          <span>{airborneCount} flying · {connectedCount} connected</span>
        </header>
        <div className="command-panel__scroll command-fleet__list">
          {operationalAircraft.length > 0 ? (
            operationalAircraft.map((aircraft) => {
              const authority = aircraftAuthority(
                aircraft,
                liveFollowSessions,
                activeMissionRuns,
              );
              return (
                <button
                  key={aircraft.droneId ?? aircraft.droneName ?? "unknown-aircraft"}
                  type="button"
                  className={`command-aircraft ${authorityToneClasses[authority.tone].aircraft}`}
                  aria-current={aircraft.droneId === selectedDroneId ? "true" : undefined}
                  onClick={() => setSelectedDroneId(aircraft.droneId ?? undefined)}
                >
                  <span className="command-aircraft__top">
                    <span className="command-aircraft__identity">
                      <strong>{aircraft.droneName || "Unnamed aircraft"}</strong>
                      <span>{shortAircraftId(aircraft.droneId)}</span>
                    </span>
                    <span className={`command-authority ${authorityToneClasses[authority.tone].badge}`}>
                      <i aria-hidden="true" />
                      {authority.label}
                    </span>
                  </span>
                  <span className="command-aircraft__activity">
                    <strong>{authority.activity}</strong>
                    <span>{authority.detail}</span>
                  </span>
                  <span className="command-aircraft__instruments">
                    <Instrument
                      label="Alt"
                      value={measurement(aircraft.telemetry?.relativeAltitudeM, 1)}
                      unit="m"
                    />
                    <Instrument
                      label="Gspd"
                      value={measurement(aircraft.telemetry?.groundSpeedMps, 1)}
                    />
                    <Instrument
                      label="Batt"
                      value={measurement(aircraft.telemetry?.batteryPercent, 0)}
                      unit="%"
                      tone={batteryTone(aircraft.telemetry?.batteryPercent)}
                    />
                    <Instrument
                      label="Telem"
                      value={telemetryAge(aircraft.telemetry?.receivedAtUnixMs)}
                      tone={aircraft.telemetry?.status === "stale" ? "critical" : "neutral"}
                    />
                  </span>
                  {authority.progress != null && (
                    <span className="command-aircraft__progress">
                      <span>
                        <i style={{ width: `${Math.max(0, Math.min(100, authority.progress))}%` }} />
                      </span>
                      <small>{authority.progressLabel}</small>
                      <small>{Math.round(authority.progress)}%</small>
                    </span>
                  )}
                </button>
              );
            })
          ) : (
            <div className="command-empty">
              <strong>No operational aircraft</strong>
              <p>
                {nativeAvailable
                  ? "Start Atlas Agent on an aircraft to add it to Command."
                  : "Local services are unavailable. Reopen Atlas before operating aircraft."}
              </p>
            </div>
          )}
        </div>
      </section>

      <section className="command-panel command-map" aria-label="Live map">
        <header className="command-panel__head">
          <h2>Live map</h2>
          <span>{incidents.length} incidents · {operationalAircraft.length} aircraft</span>
        </header>
        <div className="command-map__surface">
          <OperationsMap
            incidents={incidents}
            aircraft={operationalAircraft}
            selectedIncidentId={selectedIncidentId}
            trackGeolocations={trackGeolocations}
            layers={defaultOperationsMapLayers}
            onIncidentSelect={setSelectedIncidentId}
            onAircraftSelect={setSelectedDroneId}
          />
          {selectedIncident && (
            <div className="command-map__incident" role="status">
              <span>{humanize(selectedIncident.priority)}</span>
              <strong>{selectedIncident.summary}</strong>
              <button type="button" onClick={onOpenDispatch}>Open in Dispatch</button>
            </div>
          )}
          {loadError && <p className="command-map__error" role="alert">{loadError}</p>}
        </div>
      </section>

      <aside
        className={`command-panel command-detail${
          confirmingCommand ? " command-detail--confirming" : ""
        }`}
        aria-label="Selected aircraft"
      >
        <header className="command-panel__head">
          <h2>
            {selectedAircraft
              ? selectedAircraft.droneName || "Unnamed aircraft"
              : "Aircraft"}
          </h2>
          {selectedAircraft?.droneId && (
            <button type="button" onClick={() => onOpenAircraft(selectedAircraft.droneId!)}>
              Open aircraft →
            </button>
          )}
        </header>
        {selectedAircraft ? (
          <>
            <SelectedAircraftDetail
              aircraft={selectedAircraft}
              followSession={liveFollowSessions.find(
                (session) => session.droneId === selectedAircraft.droneId,
              )}
              missionRun={activeMissionRuns.find(
                (run) => run.droneId === selectedAircraft.droneId,
              )}
              alerts={alerts}
              onOpenFollow={onOpenFollow}
              onOpenMission={onOpenMission}
            />
            <section className="command-safety" aria-label="Flight safety">
              <header>
                <strong>Flight safety</strong>
                <span>{shortAircraftId(selectedAircraft.droneId)}</span>
              </header>
              <div>
                <button
                  type="button"
                  disabled={!canIssueSafetyCommand(selectedAircraft) || Boolean(pendingCommand)}
                  onClick={() => void requestSafetyCommand("hold")}
                >
                  Hold
                </button>
                <button
                  type="button"
                  disabled={!canIssueSafetyCommand(selectedAircraft) || Boolean(pendingCommand)}
                  aria-expanded={confirmingCommand === "return_to_launch"}
                  onClick={() => setConfirmingCommand("return_to_launch")}
                >
                  Return home
                </button>
                <button
                  type="button"
                  className="command-safety__land"
                  disabled={!canIssueSafetyCommand(selectedAircraft) || Boolean(pendingCommand)}
                  aria-expanded={confirmingCommand === "land"}
                  onClick={() => setConfirmingCommand("land")}
                >
                  Land
                </button>
              </div>
              {confirmingCommand && (
                <div
                  className={`command-safety__confirmation command-safety__confirmation--${
                    confirmingCommand === "land" ? "critical" : "caution"
                  }`}
                >
                  <strong>{commandConfirmationTitle(confirmingCommand, selectedAircraft)}</strong>
                  <p>{commandConfirmationConsequence(confirmingCommand)}</p>
                  <div>
                    <button
                      type="button"
                      onClick={() => setConfirmingCommand(undefined)}
                    >
                      {confirmingCommand === "land" ? "Keep flying" : "Keep current flight"}
                    </button>
                    <button
                      type="button"
                      className={confirmingCommand === "land" ? "command-safety__confirm-land" : undefined}
                      onClick={() => void requestSafetyCommand(confirmingCommand)}
                    >
                      {confirmingCommand === "land" ? "Confirm land" : "Confirm return home"}
                    </button>
                  </div>
                </div>
              )}
              <div
                className={`command-safety__reason${
                  commandNotice ? ` command-safety__reason--${commandNotice.tone}` : ""
                }`}
                role={commandNotice?.tone === "critical" ? "alert" : "status"}
              >
                <span>{commandNotice?.message || safetyAvailability(selectedAircraft)}</span>
                {commandNotice?.receipt && (
                  <small>
                    Receipt {shortIdentifier(commandNotice.receipt.id)}
                    {" · "}
                    {humanize(commandNotice.receipt.status)}
                  </small>
                )}
                {commandNotice?.tone === "caution" && commandNotice.receipt && (
                  <button
                    type="button"
                    disabled={Boolean(pendingCommand)}
                    onClick={() => void refreshCommandReceipt()}
                  >
                    {pendingCommand ? "Refreshing…" : "Refresh receipt"}
                  </button>
                )}
              </div>
            </section>
          </>
        ) : (
          <div className="command-empty command-empty--detail">
            <strong>Select an aircraft</strong>
            <p>Its current authority, instruments, messages, and safety controls will appear here.</p>
          </div>
        )}
      </aside>
    </main>
  );
}

function SelectedAircraftDetail({
  aircraft,
  followSession,
  missionRun,
  alerts,
  onOpenFollow,
  onOpenMission,
}: {
  aircraft: FleetAircraft;
  followSession?: AircraftFollowSession;
  missionRun?: MissionRun;
  alerts: OperationalAlertList;
  onOpenFollow: () => void;
  onOpenMission: (missionId: string, droneId: string) => void;
}) {
  const authority = aircraftAuthority(
    aircraft,
    followSession ? [followSession] : [],
    missionRun ? [missionRun] : [],
  );
  const relatedAlerts = alerts.alerts.filter(
    (alert) => alert.droneId === aircraft.droneId
      && (alert.state === "ACTIVE" || alert.state === "ACKNOWLEDGED"),
  );
  const messages = aircraft.statusEvents.slice(0, 5);

  return (
    <div className="command-detail__scroll">
      <div className={`command-detail__state ${authorityToneClasses[authority.tone].detail}`}>
        <i aria-hidden="true" />
        <span>
          <strong>{authority.activity}</strong>
          <small>{authority.detail}</small>
        </span>
        {followSession && (
          <button type="button" onClick={onOpenFollow}>Review follow</button>
        )}
        {!followSession && missionRun && (
          <button
            type="button"
            onClick={() => onOpenMission(missionRun.missionId, missionRun.droneId)}
          >
            Open mission
          </button>
        )}
      </div>

      <div className="command-detail__cluster">
        <Instrument
          label="Altitude (AGL)"
          value={measurement(aircraft.telemetry?.relativeAltitudeM, 1)}
          unit="m"
          large
        />
        <Instrument
          label="Ground spd"
          value={measurement(aircraft.telemetry?.groundSpeedMps, 1)}
          unit="m/s"
          large
        />
        <Instrument
          label="Battery"
          value={measurement(aircraft.telemetry?.batteryPercent, 0)}
          unit="%"
          tone={batteryTone(aircraft.telemetry?.batteryPercent)}
          large
        />
        <Instrument
          label="Heading"
          value={measurement(aircraft.telemetry?.headingDeg, 0)}
          unit="°"
          large
        />
      </div>

      <dl className="command-detail__rows">
        <div><dt>Control</dt><dd>{authority.label}</dd></div>
        <div><dt>Flight mode</dt><dd>{humanize(aircraft.telemetry?.flightMode || "Not reported")}</dd></div>
        <div><dt>Aircraft state</dt><dd>{flightState(aircraft)}</dd></div>
        <div><dt>GPS</dt><dd>{gpsStatus(aircraft)}</dd></div>
        <div><dt>Home</dt><dd>{aircraft.telemetry?.homePositionSet ? "Set" : "Not confirmed"}</dd></div>
        <div><dt>Contact</dt><dd>{contactStatus(aircraft)}</dd></div>
      </dl>

      {relatedAlerts.length > 0 && (
        <div className="command-detail__alerts" role="alert">
          {relatedAlerts.slice(0, 2).map((alert) => (
            <p key={alert.id}><strong>{alert.title}</strong>{alert.recommendedAction}</p>
          ))}
        </div>
      )}

      <ol className="command-detail__messages" aria-label="Recent aircraft messages">
        {messages.length > 0 ? messages.map((event, index) => (
          <li key={`${event.receivedAtUnixMs}-${index}`}>
            <time dateTime={new Date(event.receivedAtUnixMs).toISOString()}>
              {formatClock(event.receivedAtUnixMs)}
            </time>
            <p>{event.message}</p>
          </li>
        )) : (
          <li className="command-detail__messages-empty">No recent PX4 messages.</li>
        )}
      </ol>
    </div>
  );
}

function Instrument({
  label,
  value,
  unit,
  tone = "neutral",
  large = false,
}: {
  label: string;
  value: string;
  unit?: string;
  tone?: "neutral" | "nominal" | "caution" | "critical";
  large?: boolean;
}) {
  return (
    <span className={`command-instrument command-instrument--${tone}${large ? " command-instrument--large" : ""}`}>
      <span>{label}</span>
      <strong>{value}<small>{unit ? ` ${unit}` : ""}</small></strong>
    </span>
  );
}

function aircraftAuthority(
  aircraft: FleetAircraft,
  followSessions: AircraftFollowSession[],
  missionRuns: MissionRun[],
): AircraftAuthority {
  const follow = followSessions.find((session) => session.droneId === aircraft.droneId);
  if (follow) {
    if (follow.state === "DEGRADED_HOLD") {
      return {
        label: "PX4 Hold",
        activity: "Follow stopped · holding",
        detail: follow.exitReason || "Needs an operator decision",
        tone: "critical" as const,
      };
    }
    const elapsed = follow.startedAtUnixMs
      ? Date.now() - follow.startedAtUnixMs
      : 0;
    const progress = follow.maximumDurationMs > 0
      ? (elapsed / follow.maximumDurationMs) * 100
      : 0;
    return {
      label: "Following",
      activity: humanize(follow.state),
      detail: "Offboard · Atlas is flying it",
      tone: "authority" as const,
      progress,
      progressLabel: `${Math.round(follow.standoffM)} m standoff`,
    };
  }

  const run = missionRuns.find(
    (candidate) => candidate.droneId === aircraft.droneId && !candidate.completedAtUnixMs && activeMissionStates.has(candidate.status),
  );
  if (run) {
    return {
      label: run.status === "RUNNING" ? "Mission" : humanize(run.status),
      activity: run.missionName,
      detail: missionActivity(run),
      tone: run.status === "FAILED" ? "critical" as const : "nominal" as const,
      progress: run.status === "UPLOADING" ? run.uploadProgressPercent : run.progressPercent,
      progressLabel: waypointProgress(run),
    };
  }

  if (aircraft.connectionStatus !== "connected") {
    return {
      label: aircraft.connectionStatus === "stale" ? "Link stale" : "No signal",
      activity: aircraft.lastHeartbeatAtUnixMs
        ? `No heartbeat ${formatAge(aircraft.lastHeartbeatAtUnixMs)}`
        : "Waiting for first contact",
      detail: aircraft.telemetry?.inAir ? "Last seen in air" : "Last seen on ground",
      tone: "offline" as const,
    };
  }

  if (aircraft.telemetry?.inAir) {
    return {
      label: "PX4",
      activity: humanize(aircraft.telemetry.flightMode || "In flight"),
      detail: "PX4 has flight control",
      tone: "caution" as const,
    };
  }

  return {
    label: "Idle",
    activity: "On the ground",
    detail: aircraft.telemetry?.health?.armable ? "Ready to fly" : "Preflight checks required",
    tone: "idle" as const,
  };
}

function canIssueSafetyCommand(aircraft: FleetAircraft) {
  return aircraft.connectionStatus === "connected"
    && aircraft.telemetry?.status === "live"
    && aircraft.telemetry.inAir === true;
}

function safetyAvailability(aircraft: FleetAircraft) {
  if (aircraft.connectionStatus !== "connected") {
    return "Unavailable — reconnect this aircraft before sending a flight command.";
  }
  if (aircraft.telemetry?.status !== "live") {
    return "Unavailable — telemetry must be live before sending a flight command.";
  }
  if (!aircraft.telemetry.inAir) {
    return "Unavailable — flight safety controls are available only while the aircraft is in air.";
  }
  return "Ready — every command requires acknowledgement from Atlas Agent and PX4.";
}

function shortAircraftId(droneId: string | null | undefined) {
  return droneId ? `ID ${shortIdentifier(droneId)}` : "ID not reported";
}

function shortIdentifier(value: string) {
  return value.length > 8 ? `${value.slice(0, 8).toLowerCase()}…` : value.toLowerCase();
}

function missionActivity(run: MissionRun) {
  if (run.status === "RUNNING") return `Waypoint ${Math.max(1, run.currentWaypoint ?? 1)} of ${run.totalWaypoints}`;
  if (run.status === "PAUSED") return "Paused · holding position";
  if (run.status === "READY") return "Uploaded · waiting to start";
  return humanize(run.status);
}

function waypointProgress(run: MissionRun) {
  if (run.status === "UPLOADING") return "Uploading flight path";
  return `${run.currentWaypoint ?? 0} of ${run.totalWaypoints} waypoints`;
}

function flightState(aircraft: FleetAircraft) {
  if (aircraft.telemetry?.inAir) {
    return aircraft.telemetry.armed === false ? "In air · disarmed" : "Armed · in air";
  }
  return aircraft.telemetry?.armed ? "Armed · on ground" : "Disarmed · on ground";
}

function gpsStatus(aircraft: FleetAircraft) {
  const fix = humanize(aircraft.telemetry?.gpsFix || "Not reported");
  const satellites = aircraft.telemetry?.satellitesVisible;
  return satellites == null ? fix : `${fix} · ${satellites} satellites`;
}

function contactStatus(aircraft: FleetAircraft) {
  if (!aircraft.lastHeartbeatAtUnixMs) return "No heartbeat recorded";
  if (aircraft.connectionStatus === "stale") {
    return `Interrupted · last confirmed ${formatAge(aircraft.lastHeartbeatAtUnixMs)}`;
  }
  if (aircraft.connectionStatus === "disconnected") {
    return `No signal · last confirmed ${formatAge(aircraft.lastHeartbeatAtUnixMs)}`;
  }
  return `Good · confirmed ${formatAge(aircraft.lastHeartbeatAtUnixMs)}`;
}

function measurement(value: number | null | undefined, digits: number) {
  return value == null ? "—" : value.toFixed(digits);
}

function batteryTone(value: number | null | undefined) {
  if (value == null) return "neutral" as const;
  if (value <= 20) return "critical" as const;
  if (value <= 35) return "caution" as const;
  return "nominal" as const;
}

function telemetryAge(value: number | null | undefined) {
  if (!value) return "—";
  const seconds = Math.max(0, (Date.now() - value) / 1000);
  return seconds < 10 ? `${seconds.toFixed(1)}s` : `${Math.round(seconds)}s`;
}

function formatAge(value: number) {
  const seconds = Math.max(0, Math.round((Date.now() - value) / 1000));
  if (seconds < 60) return `${seconds} s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} min ago`;
  return `${Math.floor(minutes / 60)} h ago`;
}

function formatClock(value: number) {
  return new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(value);
}

function humanize(value: string) {
  return value
    .toLowerCase()
    .replace(/_/g, " ")
    .replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function commandLabel(commandType: string) {
  if (commandType === "return_to_launch") return "Return home";
  return humanize(commandType);
}

function commandConfirmationTitle(
  commandType: "return_to_launch" | "land",
  aircraft: FleetAircraft,
) {
  const name = aircraft.droneName || "this aircraft";
  return commandType === "land" ? `Land ${name}?` : `Return ${name} home?`;
}

function commandConfirmationConsequence(
  commandType: "return_to_launch" | "land",
) {
  if (commandType === "land") {
    return "Land commands PX4 to descend at its current position. Continue only when the landing area is clear.";
  }
  return "Return home commands PX4 RTL. The aircraft leaves its current task and flies to its configured home position.";
}

function messageFrom(reason: unknown) {
  return reason instanceof Error ? reason.message : String(reason);
}

function noticeForCommandOutcome(
  outcome: VehicleCommandOutcome,
  commandType: string,
): CommandNotice {
  if (outcome.kind === "success") {
    return {
      tone: "nominal",
      message: outcome.receipt.resultMessage
        || `${commandLabel(commandType)} acknowledged by PX4`,
      receipt: outcome.receipt,
    };
  }
  if (outcome.kind === "failure") {
    return {
      tone: "critical",
      message: outcome.receipt.resultMessage
        || outcome.receipt.resultCode
        || `${commandLabel(commandType)} ${humanize(outcome.receipt.status)}`,
      receipt: outcome.receipt,
    };
  }
  return {
    tone: "caution",
    message: "Still waiting for PX4 — check the aircraft.",
    receipt: outcome.receipt,
  };
}
