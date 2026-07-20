import { useEffect, useMemo, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import type { FleetAircraft, FleetSnapshot, OperationalTrackGeolocation } from "../operationsTypes";
import { OperationsMap } from "../operations/OperationsMap";
import "../operations/OperationsPage.css";
import { acquireTrackGeolocation } from "./trackGeolocation";
import type { AircraftFollowSession, FollowEnvelopeDraft, TrackGeolocation } from "./followTypes";
import "./FollowPage.css";

type FollowPageProps = {
  nativeAvailable: boolean;
  fleet: FleetSnapshot;
};

const activeStates = new Set(["REQUESTED", "VALIDATING", "ACQUIRING", "FOLLOWING"]);
const leaseDurationMs = 4_000;

export function FollowPage({ nativeAvailable, fleet }: FollowPageProps) {
  const [tracks, setTracks] = useState<OperationalTrackGeolocation[]>([]);
  const tracksRef = useRef(tracks);
  const fleetRef = useRef(fleet);
  const [sessions, setSessions] = useState<AircraftFollowSession[]>([]);
  const [selectedGeolocationId, setSelectedGeolocationId] = useState<string>();
  const [draft, setDraft] = useState<FollowEnvelopeDraft>();
  const [pending, setPending] = useState<"acquire" | "start" | "end">();
  const [message, setMessage] = useState<string>();
  const [error, setError] = useState<string>();
  const [supervisionError, setSupervisionError] = useState<string>();

  tracksRef.current = tracks;
  fleetRef.current = fleet;

  useEffect(() => {
    if (!nativeAvailable) return;
    let mounted = true;
    let reading = false;
    async function refresh() {
      if (reading) return;
      reading = true;
      try {
        const [nextTracks, nextSessions] = await Promise.all([
          invoke<OperationalTrackGeolocation[]>("operational_track_geolocations", { limit: 250 }),
          invoke<AircraftFollowSession[]>("aircraft_follow_sessions", { includeEnded: true, limit: 50 }),
        ]);
        if (!mounted) return;
        setTracks(nextTracks);
        setSessions(nextSessions);
        setSelectedGeolocationId((current) => current && nextTracks.some((track) => track.geolocation.id === current)
          ? current
          : preferredTrack(nextTracks)?.geolocation.id);
        setError(undefined);
      } catch (reason) {
        if (mounted) setError(messageFrom(reason));
      } finally {
        reading = false;
      }
    }
    void refresh();
    const interval = window.setInterval(refresh, 1_000);
    return () => {
      mounted = false;
      window.clearInterval(interval);
    };
  }, [nativeAvailable]);

  const selected = tracks.find((track) => track.geolocation.id === selectedGeolocationId);
  const selectedAircraft = fleet.aircraft.find((aircraft) => aircraft.droneId === selected?.geolocation.droneId);
  const activeSession = sessions.find((session) => session.state !== "ENDED");
  const latestSession = activeSession ?? sessions[0];

  useEffect(() => {
    if (!selected || activeSession) return;
    setDraft(defaultDraft(selected));
  }, [activeSession?.id, selected?.geolocation.id]);

  const readiness = useMemo(
    () => followReadiness(selected, selectedAircraft, draft, activeSession),
    [activeSession, draft, selected, selectedAircraft],
  );
  const commissioned = readiness.find((gate) => gate.id === "commissioning")?.ready === true;

  useEffect(() => {
    if (!activeSession || !activeStates.has(activeSession.state)) return;
    const session = activeSession;
    let mounted = true;
    let renewing = false;
    async function renew() {
      if (renewing) return;
      renewing = true;
      try {
        const track = tracksRef.current.find((candidate) => (
          candidate.geolocation.trackSessionId === session.trackSessionId
          && candidate.geolocation.trackId === session.trackId
          && candidate.geolocation.selectionId === session.selectionId
        ));
        if (!track) throw new Error("The exact authorized track is no longer present in the operational target ledger.");
        const aircraft = fleetRef.current.aircraft.find((candidate) => candidate.droneId === session.droneId);
        const refined = await acquireTrackGeolocation(track, gimbalId(aircraft));
        ensureRenewableTarget(refined);
        await invoke<AircraftFollowSession>("renew_aircraft_follow_session", {
          input: {
            sessionId: session.id,
            geolocationId: refined.id,
            leaseDurationMs,
            actor: "operator",
          },
        });
        if (mounted) setSupervisionError(undefined);
      } catch (reason) {
        if (mounted) {
          setSupervisionError(`${messageFrom(reason)} No further lease renewal was sent; watchdog Hold is authoritative.`);
        }
      } finally {
        renewing = false;
      }
    }
    void renew();
    const interval = window.setInterval(renew, 1_000);
    return () => {
      mounted = false;
      window.clearInterval(interval);
    };
  }, [activeSession?.id, activeSession?.state]);

  async function acquireWorldState() {
    if (!selected || pending) return;
    setPending("acquire");
    setError(undefined);
    setMessage(undefined);
    try {
      const refined = await acquireTrackGeolocation(selected, gimbalId(selectedAircraft));
      setMessage(refined.motionStatus === "FILTERED"
        ? `World state acquired · ${formatMeters(refined.horizontalUncertaintyM)} position · ${formatSpeed(refined.targetSpeedMps)}`
        : "Coordinate acquired. Acquire once more after target movement to establish filtered velocity.");
      setSelectedGeolocationId(refined.id);
    } catch (reason) {
      setError(messageFrom(reason));
    } finally {
      setPending(undefined);
    }
  }

  async function startFollow() {
    if (!selected || !draft || pending || readiness.some((gate) => !gate.ready)) return;
    setPending("start");
    setError(undefined);
    setMessage("Refreshing world-space target state before authorization…");
    try {
      const refined = await acquireTrackGeolocation(selected, gimbalId(selectedAircraft));
      ensureRenewableTarget(refined);
      const session = await invoke<AircraftFollowSession>("create_aircraft_follow_session", {
        input: {
          droneId: selected.geolocation.droneId,
          geolocationId: refined.id,
          requestedBy: "operator",
          reviewedBy: "operator",
          ...draft,
        },
        leaseDurationMs,
      });
      setSessions((current) => [session, ...current.filter((candidate) => candidate.id !== session.id)]);
      setMessage("Authorization delivered. The Agent must confirm ACQUIRING then FOLLOWING before movement.");
    } catch (reason) {
      setError(messageFrom(reason));
    } finally {
      setPending(undefined);
    }
  }

  async function endFollow() {
    if (!activeSession || pending) return;
    setPending("end");
    setError(undefined);
    try {
      const session = await invoke<AircraftFollowSession>("end_aircraft_follow_session", {
        input: {
          sessionId: activeSession.id,
          reason: activeSession.state === "DEGRADED_HOLD"
            ? "Operator ended the held follow session after reviewing its exit reason"
            : "Operator requested immediate Stop Follow and PX4 Hold",
          actor: "operator",
        },
      });
      setSessions((current) => current.map((candidate) => candidate.id === session.id ? session : candidate));
      setMessage("Stop Follow delivered. Awaiting Agent confirmation that Offboard ended in Hold.");
    } catch (reason) {
      setError(`${messageFrom(reason)} The short onboard lease remains the independent Hold path.`);
    } finally {
      setPending(undefined);
    }
  }

  return (
    <main className="follow-workspace" id="main-content">
      <header className="follow-heading">
        <div>
          <p className="eyebrow">Supervised navigation</p>
          <h1>Follow from standoff</h1>
          <p>Maintain a reviewed observation point from validated world-space target motion. Camera framing remains a separate gimbal authority.</p>
        </div>
        <div className={`follow-commissioning ${commissioned ? "follow-commissioning--verified" : "follow-commissioning--unverified"}`}>
          <span>Flight-control acceptance</span>
          <strong>{commissioned ? "VERIFIED" : "UNVERIFIED"}</strong>
          <small>{commissioned ? "Physical reference advertised by Agent" : "Aircraft translation is hard-blocked"}</small>
        </div>
      </header>

      <section className="follow-status-ribbon" aria-label="Follow authority status">
        <StatusDatum label="Authority" value={latestSession ? formatState(latestSession.state) : "Not requested"} />
        <StatusDatum label="Exact track" value={latestSession ? shortId(latestSession.trackId) : selected ? shortId(selected.geolocation.trackId) : "None"} />
        <StatusDatum label="Target age" value={formatAge(latestSession?.latestTargetObservedAtUnixMs ?? selected?.geolocation.frameObservedAtUnixMs)} />
        <StatusDatum label="Lease" value={leaseLabel(latestSession)} />
        <StatusDatum label="PX4 response" value={latestSession?.state === "FOLLOWING" ? "OFFBOARD" : latestSession?.state === "DEGRADED_HOLD" ? "HOLD" : "No translation"} />
      </section>

      {(error || supervisionError || message) && (
        <div className={`follow-notice ${error || supervisionError ? "follow-notice--error" : "follow-notice--info"}`} role={error || supervisionError ? "alert" : "status"}>
          {error ?? supervisionError ?? message}
        </div>
      )}

      <div className="follow-layout">
        <section className="follow-map-column" aria-label="Operational target map">
          <div className="follow-map-frame">
            <OperationsMap
              incidents={[]}
              aircraft={fleet.aircraft}
              trackGeolocations={tracks}
              draftResponseGeometry={draft ? {
                pattern: "BOUNDED_ORBIT",
                points: [{ latitude: draft.boundaryCenterLatitude, longitude: draft.boundaryCenterLongitude }],
                radiusMeters: draft.boundaryRadiusM,
              } : undefined}
              layers={{ incidents: false, aircraft: true, responseRoute: true, aircraftTrail: false, trackTargets: true }}
              onIncidentSelect={() => undefined}
            />
            <div className="follow-map-caption">
              <span>Reviewed boundary</span>
              <strong>{draft ? `${draft.boundaryRadiusM.toFixed(0)} m radius` : "Select a target"}</strong>
              <small>The Native and Agent watchdogs enforce the numeric boundary; the map is review context.</small>
            </div>
          </div>

          <div className="follow-track-ledger">
            <header>
              <div><p className="eyebrow">World targets</p><h2>Track ledger</h2></div>
              <button type="button" onClick={() => void acquireWorldState()} disabled={!selected || Boolean(pending) || Boolean(activeSession)}>
                {pending === "acquire" ? "Sampling terrain…" : "Acquire world state"}
              </button>
            </header>
            {tracks.length === 0 ? (
              <p className="follow-empty">No persisted target coordinates. Select and geolocate a confirmed track in Live video first.</p>
            ) : (
              <div className="follow-track-rows">
                {tracks.map((track) => {
                  const geolocation = track.geolocation;
                  const chosen = geolocation.id === selectedGeolocationId;
                  return (
                    <button
                      type="button"
                      key={geolocation.id}
                      className={chosen ? "follow-track-row follow-track-row--selected" : "follow-track-row"}
                      onClick={() => setSelectedGeolocationId(geolocation.id)}
                      disabled={Boolean(activeSession)}
                    >
                      <span><strong>{track.classLabel} · {shortId(geolocation.trackId)}</strong><small>{track.droneName}</small></span>
                      <span><b>{track.lifecycleState}</b><small>{track.selectionStatus || "NOT SELECTED"}</small></span>
                      <span><b>{formatMeters(geolocation.horizontalUncertaintyM)}</b><small>{geolocation.refinementStatus}</small></span>
                      <span><b>{formatSpeed(geolocation.targetSpeedMps)}</b><small>{geolocation.motionStatus.replace(/_/g, " ")}</small></span>
                      <span><b>{formatAge(geolocation.frameObservedAtUnixMs)}</b><small>{geolocation.rangeSource.replace(/_/g, " ")}</small></span>
                    </button>
                  );
                })}
              </div>
            )}
          </div>
        </section>

        <aside className="follow-control-column" aria-label="Follow authorization">
          {activeSession ? (
            <ActiveFollowPanel session={activeSession} pending={pending} onEnd={() => void endFollow()} />
          ) : (
            <>
              <section className="follow-readiness">
                <header><p className="eyebrow">Authorization gates</p><h2>Ready to request</h2></header>
                <ol>
                  {readiness.map((gate) => (
                    <li key={gate.id} className={gate.ready ? "follow-gate--ready" : "follow-gate--blocked"}>
                      <span aria-hidden="true">{gate.ready ? "✓" : "×"}</span>
                      <div><strong>{gate.label}</strong><small>{gate.detail}</small></div>
                    </li>
                  ))}
                </ol>
              </section>

              {draft && (
                <section className="follow-envelope">
                  <header><p className="eyebrow">Operator-reviewed envelope</p><h2>Observation limits</h2></header>
                  <div className="follow-field-grid">
                    <NumberField label="Standoff" unit="m" value={draft.standoffM} min={10} max={500} onChange={(standoffM) => setDraft({ ...draft, standoffM })} />
                    <NumberField label="Flight altitude" unit="m rel" value={draft.altitudeRelativeM} min={5} max={120} onChange={(altitudeRelativeM) => setDraft({ ...draft, altitudeRelativeM })} />
                    <NumberField label="Altitude floor" unit="m rel" value={draft.minimumAltitudeRelativeM} min={5} max={120} onChange={(minimumAltitudeRelativeM) => setDraft({ ...draft, minimumAltitudeRelativeM })} />
                    <NumberField label="Altitude ceiling" unit="m rel" value={draft.maximumAltitudeRelativeM} min={5} max={120} onChange={(maximumAltitudeRelativeM) => setDraft({ ...draft, maximumAltitudeRelativeM })} />
                    <NumberField label="Max groundspeed" unit="m/s" value={draft.maximumGroundSpeedMps} min={0.5} max={15} step={0.5} onChange={(maximumGroundSpeedMps) => setDraft({ ...draft, maximumGroundSpeedMps })} />
                    <NumberField label="Max acceleration" unit="m/s²" value={draft.maximumAccelerationMps2} min={0.1} max={5} step={0.1} onChange={(maximumAccelerationMps2) => setDraft({ ...draft, maximumAccelerationMps2 })} />
                    <NumberField label="Duration" unit="s" value={draft.maximumDurationSeconds} min={10} max={1800} onChange={(maximumDurationSeconds) => setDraft({ ...draft, maximumDurationSeconds })} />
                    <NumberField label="Boundary radius" unit="m" value={draft.boundaryRadiusM} min={25} max={5000} onChange={(boundaryRadiusM) => setDraft({ ...draft, boundaryRadiusM })} />
                    <NumberField label="Battery reserve" unit="%" value={draft.minimumBatteryPercent} min={15} max={100} onChange={(minimumBatteryPercent) => setDraft({ ...draft, minimumBatteryPercent })} />
                    <NumberField label="Track confidence" unit="min" value={draft.minimumTrackConfidence} min={0.5} max={1} step={0.05} onChange={(minimumTrackConfidence) => setDraft({ ...draft, minimumTrackConfidence })} />
                    <NumberField label="Position uncertainty" unit="m max" value={draft.maximumGeolocationUncertaintyM} min={1} max={100} onChange={(maximumGeolocationUncertaintyM) => setDraft({ ...draft, maximumGeolocationUncertaintyM })} />
                    <NumberField label="Velocity uncertainty" unit="m/s max" value={draft.maximumVelocityUncertaintyMps} min={0.1} max={25} step={0.1} onChange={(maximumVelocityUncertaintyMps) => setDraft({ ...draft, maximumVelocityUncertaintyMps })} />
                  </div>
                  <label className="follow-review-note">
                    <span>Review note</span>
                    <textarea value={draft.operatorReviewNote} maxLength={500} onChange={(event) => setDraft({ ...draft, operatorReviewNote: event.target.value })} />
                    <small>Record why this standoff, altitude, speed, duration, reserve, and geographic bound are acceptable now.</small>
                  </label>
                  <button
                    type="button"
                    className="follow-authorize"
                    disabled={Boolean(pending) || readiness.some((gate) => !gate.ready)}
                    onClick={() => void startFollow()}
                  >
                    {pending === "start" ? "Validating + acquiring…" : "Authorize Follow from standoff"}
                  </button>
                  <p className="follow-authority-note">Authorization starts a 4-second renewable operator lease. Closing this workspace or losing valid target updates stops renewal and causes Hold.</p>
                </section>
              )}
            </>
          )}

          {latestSession && latestSession.state === "ENDED" && <FollowHistory session={latestSession} />}
        </aside>
      </div>
    </main>
  );
}

function ActiveFollowPanel({ session, pending, onEnd }: {
  session: AircraftFollowSession;
  pending?: string;
  onEnd: () => void;
}) {
  const stages = ["REQUESTED", "VALIDATING", "ACQUIRING", "FOLLOWING"];
  return (
    <section className={`follow-active follow-active--${session.state.toLowerCase()}`}>
      <header><p className="eyebrow">Active authority</p><h2>{formatState(session.state)}</h2></header>
      <div className="follow-state-machine" aria-label={`Follow state ${session.state}`}>
        {stages.map((stage) => <span key={stage} className={stage === session.state || (session.state === "FOLLOWING" && stages.indexOf(stage) < 4) ? "follow-stage--reached" : undefined}>{stage}</span>)}
        {session.state === "DEGRADED_HOLD" && <span className="follow-stage--hold">DEGRADED HOLD</span>}
      </div>
      <dl>
        <div><dt>Target</dt><dd>{shortId(session.trackId)}</dd></div>
        <div><dt>Standoff</dt><dd>{session.standoffM.toFixed(0)} m</dd></div>
        <div><dt>Altitude band</dt><dd>{session.minimumAltitudeRelativeM.toFixed(0)}–{session.maximumAltitudeRelativeM.toFixed(0)} m rel</dd></div>
        <div><dt>Speed / accel</dt><dd>{session.maximumGroundSpeedMps.toFixed(1)} m/s · {session.maximumAccelerationMps2.toFixed(1)} m/s²</dd></div>
        <div><dt>Position uncertainty</dt><dd>{session.target.horizontalUncertaintyM.toFixed(1)} / {session.maximumGeolocationUncertaintyM.toFixed(1)} m</dd></div>
        <div><dt>Boresight</dt><dd>±{session.boresightErrorBoundDeg.toFixed(1)}° · {session.boresightReference}</dd></div>
        <div><dt>Validation</dt><dd>{session.validationReference}</dd></div>
      </dl>
      {session.state === "DEGRADED_HOLD" && (
        <div className="follow-exit-reason"><strong>{session.exitReasonCode.replace(/_/g, " ")}</strong><p>{session.exitReason}</p></div>
      )}
      <button type="button" className="follow-stop" disabled={Boolean(pending)} onClick={onEnd}>
        {pending === "end" ? "Confirming PX4 Hold…" : session.state === "DEGRADED_HOLD" ? "End held session" : "Stop Follow · Hold now"}
      </button>
      <p className="follow-authority-note">RC and PX4 failsafes remain independent. Stop Follow never implies RTL or Land.</p>
      <FollowHistory session={session} />
    </section>
  );
}

function FollowHistory({ session }: { session: AircraftFollowSession }) {
  return (
    <section className="follow-history">
      <header><p className="eyebrow">Durable trace</p><h2>Authority events</h2></header>
      <ol>
        {[...session.events].reverse().slice(0, 12).map((event) => (
          <li key={event.id}>
            <time>{formatTime(event.occurredAtUnixMs)}</time>
            <div><strong>{event.eventType.replace(/_/g, " ")}</strong><p>{event.message}</p></div>
            <span>{event.state.replace(/_/g, " ")}</span>
          </li>
        ))}
      </ol>
    </section>
  );
}

function NumberField({ label, unit, value, min, max, step = 1, onChange }: {
  label: string;
  unit: string;
  value: number;
  min: number;
  max: number;
  step?: number;
  onChange: (value: number) => void;
}) {
  return (
    <label className="follow-number-field">
      <span>{label}</span>
      <div><input type="number" value={value} min={min} max={max} step={step} onChange={(event) => onChange(Number(event.target.value))} /><small>{unit}</small></div>
    </label>
  );
}

function StatusDatum({ label, value }: { label: string; value: string }) {
  return <div><span>{label}</span><strong>{value}</strong></div>;
}

function preferredTrack(tracks: OperationalTrackGeolocation[]) {
  return tracks.find((track) => track.selectionStatus === "SELECTED" && track.lifecycleState === "ACTIVE") ?? tracks[0];
}

function defaultDraft(track: OperationalTrackGeolocation): FollowEnvelopeDraft {
  const latitude = track.geolocation.filteredLatitude ?? track.geolocation.latitude ?? 0;
  const longitude = track.geolocation.filteredLongitude ?? track.geolocation.longitude ?? 0;
  return {
    standoffM: 40,
    altitudeRelativeM: 30,
    minimumAltitudeRelativeM: 20,
    maximumAltitudeRelativeM: 45,
    maximumGroundSpeedMps: 8,
    maximumAccelerationMps2: 1.5,
    maximumDurationSeconds: 300,
    boundaryCenterLatitude: latitude,
    boundaryCenterLongitude: longitude,
    boundaryRadiusM: 500,
    minimumBatteryPercent: 30,
    minimumTrackConfidence: 0.7,
    maximumGeolocationUncertaintyM: 20,
    maximumVelocityUncertaintyMps: 5,
    operatorReviewNote: "Reviewed for current aircraft, target, weather, airspace, and operating area.",
  };
}

function followReadiness(
  track: OperationalTrackGeolocation | undefined,
  aircraft: FleetAircraft | undefined,
  draft: FollowEnvelopeDraft | undefined,
  activeSession: AircraftFollowSession | undefined,
) {
  const geolocation = track?.geolocation;
  const telemetry = aircraft?.telemetry;
  const capabilities = aircraft?.agentCapabilities ?? [];
  const gates = [
    {
      id: "commissioning",
      label: "Commissioned flight path",
      ready: capabilities.includes("aircraft_follow:standoff:v1:verified")
        && capabilities.includes("geolocation:boresight_alignment:verified")
        && capabilities.some((value) => value.startsWith("aircraft_follow:validation:") && value.length > "aircraft_follow:validation:".length),
      detail: capabilities.includes("aircraft_follow:standoff:v1:verified") ? "Agent advertises physical acceptance evidence" : "UNVERIFIED installation cannot enter Offboard",
    },
    {
      id: "track",
      label: "Exact active selection",
      ready: track?.selectionStatus === "SELECTED" && track.lifecycleState === "ACTIVE",
      detail: track ? `${track.lifecycleState} · ${track.selectionStatus || "not selected"}` : "Select and geolocate a confirmed track",
    },
    {
      id: "world",
      label: "World-space position + velocity",
      ready: geolocation?.refinementStatus === "CONVERGED" && geolocation.motionStatus === "FILTERED",
      detail: geolocation ? `${geolocation.refinementStatus} · ${geolocation.motionStatus.replace(/_/g, " ")}` : "No target coordinate",
    },
    {
      id: "quality",
      label: "Uncertainty inside envelope",
      ready: Boolean(draft && geolocation
        && (geolocation.horizontalUncertaintyM ?? Infinity) <= draft.maximumGeolocationUncertaintyM
        && (geolocation.targetVelocityUncertaintyMps ?? Infinity) <= draft.maximumVelocityUncertaintyMps),
      detail: geolocation ? `${formatMeters(geolocation.horizontalUncertaintyM)} position · ${formatSpeed(geolocation.targetVelocityUncertaintyMps)} velocity uncertainty` : "Unknown",
    },
    {
      id: "aircraft",
      label: "Aircraft ready in flight",
      ready: aircraft?.connectionStatus === "connected" && telemetry?.status === "live"
        && telemetry.armed === true && telemetry.inAir === true
        && telemetry.health?.localPositionOk === true && telemetry.health.globalPositionOk === true,
      detail: aircraft ? `${aircraft.connectionStatus} · ${telemetry?.flightMode ?? "mode unknown"}` : "No linked aircraft",
    },
    {
      id: "battery",
      label: "Battery above reviewed reserve",
      ready: Boolean(draft && telemetry?.batteryPercent != null && telemetry.batteryPercent >= draft.minimumBatteryPercent),
      detail: telemetry?.batteryPercent != null && draft ? `${telemetry.batteryPercent.toFixed(0)}% available · ${draft.minimumBatteryPercent.toFixed(0)}% reserve` : "Battery unavailable",
    },
    {
      id: "review",
      label: "Envelope review recorded",
      ready: Boolean(draft && draft.operatorReviewNote.trim().length >= 8 && validDraft(draft)),
      detail: !draft?.operatorReviewNote.trim()
        ? "Review note required"
        : validDraft(draft)
        ? "Standoff, altitude, speed, duration, reserve, and bounds reviewed"
        : "One or more reviewed limits is outside the supported safety range",
    },
    {
      id: "authority",
      label: "Navigation authority available",
      ready: !activeSession,
      detail: activeSession ? `Session ${shortId(activeSession.id)} is ${formatState(activeSession.state)}` : "No other follow session owns the aircraft",
    },
  ];
  return gates;
}

function validDraft(draft: FollowEnvelopeDraft) {
  const values = Object.values(draft).filter((value): value is number => typeof value === "number");
  return values.every(Number.isFinite)
    && draft.standoffM >= 10 && draft.standoffM <= 500
    && draft.altitudeRelativeM >= 5 && draft.altitudeRelativeM <= 120
    && draft.minimumAltitudeRelativeM >= 5 && draft.minimumAltitudeRelativeM <= draft.altitudeRelativeM
    && draft.maximumAltitudeRelativeM >= draft.altitudeRelativeM && draft.maximumAltitudeRelativeM <= 120
    && draft.maximumGroundSpeedMps >= 0.5 && draft.maximumGroundSpeedMps <= 15
    && draft.maximumAccelerationMps2 >= 0.1 && draft.maximumAccelerationMps2 <= 5
    && draft.maximumDurationSeconds >= 10 && draft.maximumDurationSeconds <= 1_800
    && draft.boundaryCenterLatitude >= -90 && draft.boundaryCenterLatitude <= 90
    && draft.boundaryCenterLongitude >= -180 && draft.boundaryCenterLongitude <= 180
    && draft.boundaryRadiusM >= 25 && draft.boundaryRadiusM <= 5_000
    && draft.minimumBatteryPercent >= 15 && draft.minimumBatteryPercent <= 100
    && draft.minimumTrackConfidence >= 0.5 && draft.minimumTrackConfidence <= 1
    && draft.maximumGeolocationUncertaintyM >= 1 && draft.maximumGeolocationUncertaintyM <= 100
    && draft.maximumVelocityUncertaintyMps >= 0.1 && draft.maximumVelocityUncertaintyMps <= 25;
}

function ensureRenewableTarget(geolocation: TrackGeolocation) {
  if (geolocation.status !== "SUCCEEDED" || geolocation.refinementStatus !== "CONVERGED") {
    throw new Error("Follow renewal requires a successful converged terrain intersection.");
  }
  if (geolocation.motionStatus !== "FILTERED"
    || geolocation.targetVelocityNorthMps == null
    || geolocation.targetVelocityEastMps == null
    || geolocation.targetVelocityUncertaintyMps == null) {
    throw new Error("Follow renewal requires filtered target velocity; acquire another world-state sample first.");
  }
}

function gimbalId(aircraft?: FleetAircraft) {
  const capability = aircraft?.agentCapabilities?.find((value) => /^gimbal:id:\d+$/.test(value));
  if (!capability) return 0;
  const parts = capability.split(":");
  return Number(parts[parts.length - 1]);
}

function leaseLabel(session?: AircraftFollowSession) {
  if (!session || session.state === "ENDED") return "None";
  if (session.state === "DEGRADED_HOLD") return "Expired · Hold";
  const remaining = Math.max(0, (session.operatorLeaseExpiresAtUnixMs ?? 0) - Date.now());
  return `${(remaining / 1_000).toFixed(1)} s supervised`;
}

function formatState(state: string) {
  return state.toLowerCase().replace(/_/g, " ").replace(/(^|\s)\w/g, (letter) => letter.toUpperCase());
}

function shortId(value: string) {
  const parts = value.split(":");
  const part = parts[parts.length - 1] || value;
  return part.length > 12 ? `${part.slice(0, 8)}…` : `#${part}`;
}

function formatAge(value?: number | null) {
  if (!value) return "Unknown";
  const age = Math.max(0, Date.now() - value);
  return age < 1_000 ? "Now" : `${(age / 1_000).toFixed(age < 10_000 ? 1 : 0)} s`;
}

function formatMeters(value?: number | null) {
  return value == null || !Number.isFinite(value) ? "Unknown" : `${value.toFixed(1)} m`;
}

function formatSpeed(value?: number | null) {
  return value == null || !Number.isFinite(value) ? "Unknown" : `${value.toFixed(1)} m/s`;
}

function formatTime(value: number) {
  return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(value);
}

function messageFrom(reason: unknown) {
  return reason instanceof Error ? reason.message : String(reason);
}
