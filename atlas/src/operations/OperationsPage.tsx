import { useEffect, useMemo, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import type {
  CreateIncidentInput,
  ArrivalFailurePolicy,
  FleetAircraft,
  FleetSnapshot,
  IncidentAssignment,
  IncidentDetail,
  IncidentPriority,
  IncidentSnapshot,
  IncidentStatus,
  PrepareIncidentResponseInput,
  PreparedIncidentResponse,
  UpdateIncidentInput,
} from "../operationsTypes";
import type { Mission, MissionPlan } from "../missions/missionTypes";
import { formatDistance, missionDistanceStatus } from "../missions/missionSafety";
import { OperationsMap } from "./OperationsMap";
import "./OperationsPage.css";

type OperationsPageProps = {
  nativeAvailable: boolean;
  fleet: FleetSnapshot;
  onOpenAircraft: (droneId: string) => void;
  onConfirmResponse: (missionId: string, droneId: string) => void;
};

type IncidentDraft = {
  incidentType: string;
  priority: IncidentPriority;
  summary: string;
  description: string;
  latitude?: number;
  longitude?: number;
  address: string;
  area: string;
  occurredAt: string;
};

type ResponseDraft = {
  droneId: string;
  stagingLatitude: number;
  stagingLongitude: number;
  altitudeMeters: number;
  speedMps: number;
  arrivalFailurePolicy: ArrivalFailurePolicy;
  pointGimbalAtIncident: boolean;
  incidentTargetAltitudeAmslMeters?: number;
};

const emptyDraft: IncidentDraft = {
  incidentType: "",
  priority: "MEDIUM",
  summary: "",
  description: "",
  address: "",
  area: "",
  occurredAt: "",
};

export function OperationsPage({ nativeAvailable, fleet, onOpenAircraft, onConfirmResponse }: OperationsPageProps) {
  const [incidents, setIncidents] = useState<IncidentSnapshot[]>([]);
  const [selectedIncidentId, setSelectedIncidentId] = useState<string>();
  const [detail, setDetail] = useState<IncidentDetail>();
  const [includeClosed, setIncludeClosed] = useState(false);
  const [creating, setCreating] = useState(false);
  const [responding, setResponding] = useState(false);
  const [responseDraft, setResponseDraft] = useState<ResponseDraft>();
  const [preparedResponse, setPreparedResponse] = useState<PreparedIncidentResponse>();
  const [selectingLocation, setSelectingLocation] = useState(false);
  const [draft, setDraft] = useState<IncidentDraft>(emptyDraft);
  const [pending, setPending] = useState<string>();
  const [loadError, setLoadError] = useState<string>();
  const [actionError, setActionError] = useState<string>();

  useEffect(() => {
    if (!nativeAvailable) return;
    let active = true;
    let reading = false;
    async function refresh() {
      if (reading) return;
      reading = true;
      try {
        const [nextIncidents, nextDetail] = await Promise.all([
          invoke<IncidentSnapshot[]>("incident_list", { includeClosed, limit: 250 }),
          selectedIncidentId
            ? invoke<IncidentDetail>("incident_detail", { incidentId: selectedIncidentId })
            : Promise.resolve(undefined),
        ]);
        if (!active) return;
        setIncidents(nextIncidents);
        if (nextDetail) setDetail(nextDetail);
        setLoadError(undefined);
        if (!selectedIncidentId && !creating && nextIncidents[0]) {
          setSelectedIncidentId(nextIncidents[0].id);
        }
      } catch (reason) {
        if (active) setLoadError(messageFrom(reason));
      } finally {
        reading = false;
      }
    }
    void refresh();
    const interval = window.setInterval(refresh, 2_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [creating, includeClosed, nativeAvailable, selectedIncidentId]);

  const selectedIncident = detail && detail.incident.id === selectedIncidentId ? detail.incident : undefined;
  const activeIncidents = incidents.filter((incident) => incident.status === "OPEN" || incident.status === "ACTIVE");
  const connectedAircraft = fleet.aircraft.filter((aircraft) => aircraft.connectionStatus === "connected");
  const positionedAircraft = fleet.aircraft.filter(hasAircraftLocation);
  const criticalIncidents = activeIncidents.filter((incident) => incident.priority === "CRITICAL");
  const unlocatedIncidents = activeIncidents.filter((incident) => !hasIncidentLocation(incident));
  const draftLocation = draft.latitude !== undefined && draft.longitude !== undefined
    ? { latitude: draft.latitude, longitude: draft.longitude }
    : undefined;
  const responseAircraft = useMemo(
    () => [...fleet.aircraft]
      .filter((aircraft): aircraft is FleetAircraft & { droneId: string } => Boolean(aircraft.droneId) && aircraft.vehicleStatus === "active")
      .sort((left, right) => aircraftSuitabilityScore(right) - aircraftSuitabilityScore(left)),
    [fleet.aircraft],
  );
  const responseLocation = responseDraft
    ? { latitude: responseDraft.stagingLatitude, longitude: responseDraft.stagingLongitude }
    : undefined;

  const mapIncidents = useMemo(() => {
    if (!selectedIncident || incidents.some((incident) => incident.id === selectedIncident.id)) return incidents;
    return [selectedIncident, ...incidents];
  }, [incidents, selectedIncident]);

  async function refreshAfterMutation(preferred: IncidentDetail) {
    setDetail(preferred);
    setSelectedIncidentId(preferred.incident.id);
    const next = await invoke<IncidentSnapshot[]>("incident_list", { includeClosed, limit: 250 });
    setIncidents(next);
  }

  async function createIncident() {
    setActionError(undefined);
    const validation = validateDraft(draft);
    if (validation) {
      setActionError(validation);
      return;
    }
    setPending("create");
    try {
      const created = await invoke<IncidentDetail>("create_incident", { input: createInput(draft) });
      await refreshAfterMutation(created);
      setDraft(emptyDraft);
      setCreating(false);
      setSelectingLocation(false);
    } catch (reason) {
      setActionError(messageFrom(reason));
    } finally {
      setPending(undefined);
    }
  }

  async function changeStatus(status: IncidentStatus) {
    if (!selectedIncident) return;
    setActionError(undefined);
    setPending(`status:${status}`);
    try {
      const updated = await invoke<IncidentDetail>("update_incident", {
        incidentId: selectedIncident.id,
        input: updateInput(selectedIncident, status),
      });
      if (status === "RESOLVED" || status === "CANCELLED") setIncludeClosed(true);
      await refreshAfterMutation(updated);
    } catch (reason) {
      setActionError(messageFrom(reason));
    } finally {
      setPending(undefined);
    }
  }

  async function prepareResponse() {
    if (!selectedIncident || !responseDraft) return;
    const validation = validateResponseDraft(responseDraft);
    if (validation) {
      setActionError(validation);
      return;
    }
    setActionError(undefined);
    setPending("prepare-response");
    try {
      const input: PrepareIncidentResponseInput = {
        expectedIncidentRevision: selectedIncident.revision,
        droneId: responseDraft.droneId,
        stagingLatitude: responseDraft.stagingLatitude,
        stagingLongitude: responseDraft.stagingLongitude,
        altitudeMeters: responseDraft.altitudeMeters,
        speedMps: responseDraft.speedMps,
        arrivalFailurePolicy: responseDraft.arrivalFailurePolicy,
        pointGimbalAtIncident: responseDraft.pointGimbalAtIncident,
        incidentTargetAltitudeAmslMeters: responseDraft.incidentTargetAltitudeAmslMeters,
      };
      const prepared = await invoke<PreparedIncidentResponse>("prepare_incident_response", {
        incidentId: selectedIncident.id,
        input,
      });
      setPreparedResponse(prepared);
      setSelectingLocation(false);
      void invoke<IncidentDetail>("incident_detail", { incidentId: selectedIncident.id })
        .then(setDetail)
        .catch(() => undefined);
    } catch (reason) {
      setActionError(messageFrom(reason));
    } finally {
      setPending(undefined);
    }
  }

  async function reviewAssignment(assignment: IncidentAssignment) {
    if (!selectedIncident || !assignment.missionId) return;
    setActionError(undefined);
    setPending(`review:${assignment.id}`);
    try {
      const [mission, plan] = await Promise.all([
        invoke<Mission>("mission_detail", { missionId: assignment.missionId }),
        invoke<MissionPlan>("mission_plan", { missionId: assignment.missionId }),
      ]);
      const waypoint = plan.generatedWaypoints[0];
      setPreparedResponse({ incident: selectedIncident, assignment, mission, plan });
      setResponseDraft({
        droneId: assignment.droneId,
        stagingLatitude: waypoint?.latitude ?? selectedIncident.latitude ?? 0,
        stagingLongitude: waypoint?.longitude ?? selectedIncident.longitude ?? 0,
        altitudeMeters: waypoint?.altitudeMeters ?? 30,
        speedMps: waypoint?.speedMps ?? 5,
        arrivalFailurePolicy: plan.metadata.incidentResponse?.arrivalFailurePolicy ?? "RETURN_TO_LAUNCH",
        pointGimbalAtIncident: plan.metadata.incidentResponse?.pointGimbalAtIncident ?? false,
        incidentTargetAltitudeAmslMeters: plan.metadata.incidentResponse?.incidentTargetAltitudeAmslMeters ?? undefined,
      });
      setResponding(true);
      setSelectingLocation(false);
    } catch (reason) {
      setActionError(messageFrom(reason));
    } finally {
      setPending(undefined);
    }
  }

  function beginResponse() {
    if (!selectedIncident || !hasIncidentLocation(selectedIncident)) return;
    const preferredAircraft = responseAircraft[0];
    setResponseDraft({
      droneId: preferredAircraft?.droneId ?? "",
      stagingLatitude: selectedIncident.latitude,
      stagingLongitude: selectedIncident.longitude,
      altitudeMeters: 30,
      speedMps: 5,
      arrivalFailurePolicy: "RETURN_TO_LAUNCH",
      pointGimbalAtIncident: false,
      incidentTargetAltitudeAmslMeters: preferredAircraft?.telemetry?.homePosition?.absoluteAltitudeM ?? undefined,
    });
    setPreparedResponse(undefined);
    setCreating(false);
    setResponding(true);
    setSelectingLocation(false);
    setActionError(undefined);
  }

  function beginCreate() {
    setCreating(true);
    setResponding(false);
    setPreparedResponse(undefined);
    setSelectingLocation(true);
    setActionError(undefined);
  }

  function selectIncident(incidentId: string) {
    setCreating(false);
    setResponding(false);
    setPreparedResponse(undefined);
    setSelectingLocation(false);
    setActionError(undefined);
    setSelectedIncidentId(incidentId);
  }

  return (
    <main className="operations-workspace" id="main-content">
      <header className="operations-workspace__heading">
        <div>
          <p className="eyebrow">Live coordination</p>
          <h1>Operations</h1>
          <p>Manual incidents and fleet readiness share one local operational picture.</p>
        </div>
        <div className="operations-workspace__heading-actions">
          <span>Updated {formatRelativeTime(fleet.generatedAtUnixMs)}</span>
          <button type="button" onClick={beginCreate} disabled={!nativeAvailable || pending === "create"}>
            New incident
          </button>
        </div>
      </header>

      <section className="operations-summary" aria-label="Operations summary">
        <OperationsMetric label="Open incidents" value={activeIncidents.length} tone={activeIncidents.length ? "warning" : "neutral"} />
        <OperationsMetric label="Critical" value={criticalIncidents.length} tone={criticalIncidents.length ? "critical" : "neutral"} />
        <OperationsMetric label="Connected aircraft" value={connectedAircraft.length} tone={connectedAircraft.length ? "positive" : "neutral"} />
        <OperationsMetric label="Need location" value={unlocatedIncidents.length} tone={unlocatedIncidents.length ? "warning" : "neutral"} />
      </section>

      {!nativeAvailable && (
        <p className="operations-boundary" role="status">Incident records are available only inside Atlas Native.</p>
      )}
      {loadError && <p className="operations-error" role="alert">{loadError}</p>}

      <section className="operations-board" aria-label="Incident operations board">
        <aside className="incident-queue" aria-label="Incident queue">
          <header>
            <div>
              <p className="eyebrow">Dispatch queue</p>
              <h2>{includeClosed ? "All incidents" : "Open incidents"}</h2>
            </div>
            <label className="incident-queue__closed-toggle">
              <input type="checkbox" checked={includeClosed} onChange={(event) => setIncludeClosed(event.target.checked)} />
              <span>History</span>
            </label>
          </header>
          <div className="incident-queue__count">
            <span>{incidents.length} visible</span>
            <span>{positionedAircraft.length} aircraft mapped</span>
          </div>
          {incidents.length === 0 ? (
            <div className="incident-queue__empty">
              <strong>No incidents in this queue</strong>
              <p>Create a manual incident to establish operational context before planning a response.</p>
              <button type="button" onClick={beginCreate} disabled={!nativeAvailable}>Create incident</button>
            </div>
          ) : (
            <ol className="incident-queue__list">
              {incidents.map((incident) => (
                <li key={incident.id}>
                  <button
                    type="button"
                    className={`incident-row incident-row--${incident.priority.toLowerCase()}${selectedIncidentId === incident.id && !creating ? " incident-row--selected" : ""}`}
                    aria-pressed={selectedIncidentId === incident.id && !creating}
                    onClick={() => selectIncident(incident.id)}
                  >
                    <span className="incident-row__priority">{displayEnum(incident.priority)}</span>
                    <strong>{incident.summary}</strong>
                    <span className="incident-row__meta">
                      {incident.address || incident.area || coordinateLabel(incident) || "Location required"}
                    </span>
                    <span className="incident-row__footer">
                      <i>{displayEnum(incident.status)}</i>
                      <time dateTime={new Date(incident.receivedAtUnixMs).toISOString()}>{formatRelativeTime(incident.receivedAtUnixMs)}</time>
                    </span>
                  </button>
                </li>
              ))}
            </ol>
          )}
        </aside>

        <div className="operations-board__map">
          <OperationsMap
            incidents={mapIncidents}
            aircraft={fleet.aircraft}
            selectedIncidentId={creating ? undefined : selectedIncidentId}
            draftLocation={creating ? draftLocation : responding && !preparedResponse ? responseLocation : undefined}
            selectingLocation={(creating || (responding && !preparedResponse)) && selectingLocation}
            responsePlan={responding ? preparedResponse?.plan : undefined}
            responseDroneId={responding ? responseDraft?.droneId : undefined}
            onIncidentSelect={selectIncident}
            onAircraftSelect={onOpenAircraft}
            onLocationSelect={(location) => {
              if (creating) {
                setDraft((current) => ({ ...current, latitude: location.latitude, longitude: location.longitude }));
              } else if (responding) {
                setResponseDraft((current) => current ? {
                  ...current,
                  stagingLatitude: location.latitude,
                  stagingLongitude: location.longitude,
                } : current);
              }
              setSelectingLocation(false);
            }}
          />
        </div>

        <aside className="incident-detail" aria-label={creating ? "Create incident" : responding ? "Prepare incident response" : "Incident detail"}>
          {creating ? (
            <IncidentCreateForm
              draft={draft}
              pending={pending === "create"}
              selectingLocation={selectingLocation}
              error={actionError}
              onChange={setDraft}
              onSelectLocation={() => setSelectingLocation((current) => !current)}
              onCancel={() => {
                setCreating(false);
                setSelectingLocation(false);
                setActionError(undefined);
              }}
              onSubmit={() => void createIncident()}
            />
          ) : responding && selectedIncident && responseDraft ? (
            <IncidentResponsePanel
              incident={selectedIncident}
              aircraft={responseAircraft}
              draft={responseDraft}
              prepared={preparedResponse}
              pending={pending === "prepare-response"}
              selectingLocation={selectingLocation}
              error={actionError}
              onChange={setResponseDraft}
              onSelectLocation={() => setSelectingLocation((current) => !current)}
              onPrepare={() => void prepareResponse()}
              onBack={() => {
                setResponding(false);
                setPreparedResponse(undefined);
                setSelectingLocation(false);
                setActionError(undefined);
              }}
              onConfirm={() => {
                if (preparedResponse?.assignment.missionId) {
                  onConfirmResponse(preparedResponse.assignment.missionId, preparedResponse.assignment.droneId);
                }
              }}
            />
          ) : selectedIncident ? (
            <IncidentDetailPanel
              detail={detail}
              pending={pending}
              error={actionError}
              onRespond={beginResponse}
              onOpenAssignment={(assignment) => {
                if (assignment.status === "PREPARED" && !assignment.missionRunId) {
                  void reviewAssignment(assignment);
                } else if (assignment.missionId) {
                  onConfirmResponse(assignment.missionId, assignment.droneId);
                }
              }}
              onStatusChange={(status) => void changeStatus(status)}
            />
          ) : (
            <div className="incident-detail__empty">
              <p className="eyebrow">Incident detail</p>
              <h2>Select an incident</h2>
              <p>The operational record, location, revisions, and event history will appear here.</p>
            </div>
          )}
        </aside>
      </section>
    </main>
  );
}

function OperationsMetric({ label, value, tone }: { label: string; value: number; tone: "positive" | "warning" | "critical" | "neutral" }) {
  return (
    <article className={`operations-metric operations-metric--${tone}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </article>
  );
}

function IncidentCreateForm({
  draft,
  pending,
  selectingLocation,
  error,
  onChange,
  onSelectLocation,
  onCancel,
  onSubmit,
}: {
  draft: IncidentDraft;
  pending: boolean;
  selectingLocation: boolean;
  error?: string;
  onChange: (draft: IncidentDraft) => void;
  onSelectLocation: () => void;
  onCancel: () => void;
  onSubmit: () => void;
}) {
  function update<Key extends keyof IncidentDraft>(key: Key, value: IncidentDraft[Key]) {
    onChange({ ...draft, [key]: value });
  }

  return (
    <form className="incident-form" onSubmit={(event) => { event.preventDefault(); onSubmit(); }}>
      <header>
        <div>
          <p className="eyebrow">Manual intake</p>
          <h2>New incident</h2>
        </div>
        <button type="button" className="incident-form__cancel" onClick={onCancel}>Cancel</button>
      </header>
      <p className="incident-form__source">Source locked to Atlas Native · manual entry</p>

      <div className="incident-form__row">
        <label>
          Incident type
          <input value={draft.incidentType} maxLength={80} onChange={(event) => update("incidentType", event.target.value)} placeholder="Missing person" required />
        </label>
        <label>
          Priority
          <select value={draft.priority} onChange={(event) => update("priority", event.target.value as IncidentPriority)}>
            {(["LOW", "MEDIUM", "HIGH", "CRITICAL"] as IncidentPriority[]).map((priority) => <option key={priority}>{priority}</option>)}
          </select>
        </label>
      </div>
      <label>
        Summary
        <input value={draft.summary} maxLength={200} onChange={(event) => update("summary", event.target.value)} placeholder="Short operational description" required />
      </label>
      <label>
        Description
        <textarea value={draft.description} maxLength={4000} rows={3} onChange={(event) => update("description", event.target.value)} placeholder="Known context, caller information, or constraints" />
      </label>

      <fieldset className="incident-form__location">
        <legend>Location</legend>
        <button type="button" className={selectingLocation ? "incident-form__map-select incident-form__map-select--active" : "incident-form__map-select"} onClick={onSelectLocation}>
          {selectingLocation ? "Click map to place" : draft.latitude !== undefined ? "Move on map" : "Select on map"}
        </button>
        <div className="incident-form__row">
          <label>
            Latitude
            <input type="number" step="any" min={-90} max={90} value={draft.latitude ?? ""} onChange={(event) => update("latitude", optionalNumber(event.target.value))} placeholder="51.507400" />
          </label>
          <label>
            Longitude
            <input type="number" step="any" min={-180} max={180} value={draft.longitude ?? ""} onChange={(event) => update("longitude", optionalNumber(event.target.value))} placeholder="-0.127800" />
          </label>
        </div>
        <label>
          Address or landmark
          <input value={draft.address} maxLength={500} onChange={(event) => update("address", event.target.value)} placeholder="North trail entrance" />
        </label>
        <label>
          Operational area
          <input value={draft.area} maxLength={500} onChange={(event) => update("area", event.target.value)} placeholder="North sector" />
        </label>
      </fieldset>

      <label>
        Occurred at
        <input type="datetime-local" value={draft.occurredAt} onChange={(event) => update("occurredAt", event.target.value)} />
      </label>
      {error && <p className="incident-form__error" role="alert">{error}</p>}
      <button type="submit" className="incident-form__submit" disabled={pending}>{pending ? "Creating incident…" : "Create incident"}</button>
    </form>
  );
}

function IncidentResponsePanel({
  incident,
  aircraft,
  draft,
  prepared,
  pending,
  selectingLocation,
  error,
  onChange,
  onSelectLocation,
  onPrepare,
  onBack,
  onConfirm,
}: {
  incident: IncidentSnapshot;
  aircraft: Array<FleetAircraft & { droneId: string }>;
  draft: ResponseDraft;
  prepared?: PreparedIncidentResponse;
  pending: boolean;
  selectingLocation: boolean;
  error?: string;
  onChange: (draft: ResponseDraft) => void;
  onSelectLocation: () => void;
  onPrepare: () => void;
  onBack: () => void;
  onConfirm: () => void;
}) {
  const selectedAircraft = aircraft.find((candidate) => candidate.droneId === draft.droneId);
  const distance = missionDistanceStatus(
    { latitude: draft.stagingLatitude, longitude: draft.stagingLongitude },
    selectedAircraft,
  );
  const arrivalSeconds = distance.distanceMeters !== undefined && draft.speedMps > 0
    ? distance.distanceMeters / draft.speedMps
    : undefined;
  const waypoint = prepared?.plan.generatedWaypoints[0];
  const arrivalActions = prepared?.plan.actions.filter((action) => action.actionType === "HOLD_AT_ARRIVAL" || action.actionType === "POINT_GIMBAL_AT_INCIDENT") ?? [];
  const reviewedFailurePolicy = arrivalActions[0]?.params.failurePolicy as ArrivalFailurePolicy | undefined;

  if (prepared) {
    return (
      <div className="incident-response incident-response--prepared">
        <header>
          <div>
            <p className="eyebrow">Response prepared</p>
            <h2>Confirm immutable plan</h2>
          </div>
          <button type="button" className="incident-form__cancel" onClick={onBack}>Back</button>
        </header>

        <div className="response-plan-seal">
          <span>Plan locked</span>
          <strong>{shortId(prepared.plan.id)}</strong>
          <small>Assignment {shortId(prepared.assignment.id)} · no aircraft command sent</small>
        </div>

        <dl className="response-review-facts">
          <div><dt>Aircraft</dt><dd>{prepared.assignment.droneName}</dd></div>
          <div><dt>Staging</dt><dd>{waypoint ? `${waypoint.latitude.toFixed(6)}, ${waypoint.longitude.toFixed(6)}` : "No waypoint"}</dd></div>
          <div><dt>Altitude</dt><dd>{waypoint ? `${waypoint.altitudeMeters.toFixed(0)} m home-relative` : "—"}</dd></div>
          <div><dt>Speed</dt><dd>{waypoint?.speedMps !== undefined ? `${waypoint.speedMps.toFixed(1)} m/s` : "—"}</dd></div>
          <div><dt>Departure</dt><dd>{distance.distanceMeters !== undefined ? `${formatDistance(distance.distanceMeters)} from ${distance.reference?.source ?? "aircraft"}` : "Revalidated before upload"}</dd></div>
          <div><dt>Incident evidence</dt><dd>Revision {prepared.incident.revision} · location {prepared.incident.locationRevision}</dd></div>
          <div><dt>Arrival</dt><dd>Hold must be acknowledged · {Number(arrivalActions[0]?.params.maxAttempts ?? 3)} attempts</dd></div>
          <div><dt>Hold failure</dt><dd>{failurePolicyLabel(reviewedFailurePolicy)}</dd></div>
        </dl>

        <section className="response-arrival-review" aria-label="Reviewed arrival action chain">
          <header><span>Arrival authority</span><strong>Not on scene until Hold succeeds</strong></header>
          <ol>
            {arrivalActions.map((action) => (
              <li key={action.sequence}>
                <span>{String(action.sequence + 1).padStart(2, "0")}</span>
                <div><strong>{actionLabel(action.actionType)}</strong><small>{action.actionType === "HOLD_AT_ARRIVAL" ? "Agent requests MAVSDK Hold and waits for PX4 acknowledgement." : `Target ${Number(action.params.latitude).toFixed(6)}, ${Number(action.params.longitude).toFixed(6)} · ${Number(action.params.altitudeAmslMeters).toFixed(0)} m AMSL`}</small></div>
              </li>
            ))}
          </ol>
          <p>Failure policy · {failurePolicyDescription(reviewedFailurePolicy)}</p>
        </section>

        {prepared.plan.validationWarnings.length > 0 && (
          <div className="response-plan-warnings" role="status">
            <strong>Planner warnings</strong>
            {prepared.plan.validationWarnings.map((warning) => <p key={warning}>{warning}</p>)}
          </div>
        )}
        {!distance.ok && <p className="incident-form__error" role="alert">{distance.message}</p>}
        {error && <p className="incident-form__error" role="alert">{error}</p>}

        <ol className="response-handoff">
          <li className="response-handoff--complete"><span>01</span><div><strong>Prepared atomically</strong><small>Mission, plan, and assignment committed together.</small></div></li>
          <li><span>02</span><div><strong>Confirm deployment</strong><small>Open the existing upload and preflight workspace.</small></div></li>
          <li><span>03</span><div><strong>Upload & start</strong><small>Connectivity, distance, position, and run locks are rechecked.</small></div></li>
        </ol>

        <button type="button" className="incident-response__confirm" onClick={onConfirm} disabled={!waypoint}>
          Confirm & review deployment
        </button>
        <p className="incident-response__reservation">Returning keeps this prepared assignment reserved and recoverable from the incident record.</p>
      </div>
    );
  }

  return (
    <form className="incident-form incident-response" onSubmit={(event) => { event.preventDefault(); onPrepare(); }}>
      <header>
        <div>
          <p className="eyebrow">Safe response</p>
          <h2>Review staging plan</h2>
        </div>
        <button type="button" className="incident-form__cancel" onClick={onBack}>Cancel</button>
      </header>
      <p className="incident-response__scope">One waypoint · incident revision {incident.revision} · preparation does not upload or arm an aircraft</p>

      <label>Assigned aircraft
        <select value={draft.droneId} onChange={(event) => onChange({ ...draft, droneId: event.target.value })} required>
          <option value="">Select operational aircraft</option>
          {aircraft.map((candidate) => (
            <option key={candidate.droneId} value={candidate.droneId}>
              {candidate.droneName || candidate.droneId} · {displayEnum(candidate.connectionStatus)}{candidate.telemetry?.status ? ` / ${displayEnum(candidate.telemetry.status)}` : ""}
            </option>
          ))}
        </select>
      </label>
      {selectedAircraft && (
        <div className="response-aircraft-readout">
          <span className={`map-status-dot map-status-dot--${selectedAircraft.connectionStatus === "connected" ? "ready" : "degraded"}`} />
          <strong>{selectedAircraft.droneName || selectedAircraft.droneId}</strong>
          <small>{selectedAircraft.connectionStatus === "connected" ? "Available for final upload checks" : "Plan may be prepared; upload remains blocked until connected"}</small>
        </div>
      )}

      <fieldset className="incident-form__location response-staging-location">
        <legend>Operator-reviewed staging coordinate</legend>
        <button type="button" className={selectingLocation ? "incident-form__map-select incident-form__map-select--active" : "incident-form__map-select"} onClick={onSelectLocation}>
          {selectingLocation ? "Select staging point on map" : "Choose staging point on map"}
        </button>
        <div className="incident-form__row">
          <label>Latitude
            <input type="number" min="-90" max="90" step="0.000001" value={draft.stagingLatitude} onChange={(event) => onChange({ ...draft, stagingLatitude: Number(event.target.value) })} required />
          </label>
          <label>Longitude
            <input type="number" min="-180" max="180" step="0.000001" value={draft.stagingLongitude} onChange={(event) => onChange({ ...draft, stagingLongitude: Number(event.target.value) })} required />
          </label>
        </div>
      </fieldset>

      <div className="incident-form__row response-flight-envelope">
        <label>Altitude · m
          <input type="number" min="2" max="120" step="1" value={draft.altitudeMeters} onChange={(event) => onChange({ ...draft, altitudeMeters: Number(event.target.value) })} required />
        </label>
        <label>Speed · m/s
          <input type="number" min="0.5" max="15" step="0.5" value={draft.speedMps} onChange={(event) => onChange({ ...draft, speedMps: Number(event.target.value) })} required />
        </label>
      </div>

      <fieldset className="response-arrival-policy">
        <legend>Arrival authority</legend>
        <div className="response-arrival-policy__hold">
          <span>Required action</span>
          <strong>HOLD_AT_ARRIVAL</strong>
          <small>Reaching the waypoint is not enough. Atlas waits for a PX4 Hold acknowledgement before recording on scene.</small>
        </div>
        <label>After three failed action attempts
          <select value={draft.arrivalFailurePolicy} onChange={(event) => onChange({ ...draft, arrivalFailurePolicy: event.target.value as ArrivalFailurePolicy })}>
            <option value="RETURN_TO_LAUNCH">Request Return to launch</option>
            <option value="OPERATOR_INTERVENTION">Require operator intervention</option>
          </select>
          <small>{failurePolicyDescription(draft.arrivalFailurePolicy)}</small>
        </label>
        <label className="response-arrival-policy__optional">
          <input
            type="checkbox"
            checked={draft.pointGimbalAtIncident}
            onChange={(event) => onChange({
              ...draft,
              pointGimbalAtIncident: event.target.checked,
              incidentTargetAltitudeAmslMeters: event.target.checked
                ? draft.incidentTargetAltitudeAmslMeters ?? selectedAircraft?.telemetry?.homePosition?.absoluteAltitudeM ?? undefined
                : draft.incidentTargetAltitudeAmslMeters,
            })}
          />
          <span><strong>Point gimbal at incident after Hold</strong><small>Optional acknowledged ROI action using the incident coordinate.</small></span>
        </label>
        {draft.pointGimbalAtIncident && (
          <label>Incident target altitude · m AMSL
            <input type="number" min="-500" max="9000" step="1" value={draft.incidentTargetAltitudeAmslMeters ?? ""} onChange={(event) => onChange({ ...draft, incidentTargetAltitudeAmslMeters: optionalNumber(event.target.value) })} required />
            <small>Geographic gimbal targeting requires a reviewed absolute target altitude.</small>
          </label>
        )}
      </fieldset>

      <div className={distance.ok ? "response-distance response-distance--ready" : "response-distance"}>
        <span>{distance.ok ? "Upload radius check" : "Deployment blocker"}</span>
        <strong>{distance.distanceMeters !== undefined ? formatDistance(distance.distanceMeters) : "Position unavailable"}</strong>
        <small>{arrivalSeconds !== undefined ? `Nominal travel ${formatDuration(arrivalSeconds)} at reviewed speed` : distance.message}</small>
      </div>
      {error && <p className="incident-form__error" role="alert">{error}</p>}
      <button type="submit" className="incident-form__submit" disabled={pending || !draft.droneId}>
        {pending ? "Preparing atomically…" : "Prepare response plan"}
      </button>
    </form>
  );
}

function IncidentDetailPanel({
  detail,
  pending,
  error,
  onRespond,
  onOpenAssignment,
  onStatusChange,
}: {
  detail?: IncidentDetail;
  pending?: string;
  error?: string;
  onRespond: () => void;
  onOpenAssignment: (assignment: IncidentAssignment) => void;
  onStatusChange: (status: IncidentStatus) => void;
}) {
  if (!detail) return <div className="incident-detail__loading">Loading incident record…</div>;
  const incident = detail.incident;
  const primaryStatus: IncidentStatus = incident.status === "OPEN" ? "ACTIVE" : incident.status === "ACTIVE" ? "RESOLVED" : "OPEN";
  const primaryLabel = incident.status === "OPEN" ? "Mark active" : incident.status === "ACTIVE" ? "Resolve incident" : "Reopen incident";
  return (
    <div className="incident-record">
      <header>
        <div className="incident-record__flags">
          <span className={`incident-priority incident-priority--${incident.priority.toLowerCase()}`}>{displayEnum(incident.priority)}</span>
          <span>{displayEnum(incident.status)}</span>
        </div>
        <h2>{incident.summary}</h2>
        <p>{incident.incidentType}</p>
      </header>

      {(incident.status === "OPEN" || incident.status === "ACTIVE") && (
        <div className="incident-record__respond-block">
          <button type="button" className="incident-record__respond" onClick={onRespond} disabled={Boolean(pending) || !hasIncidentLocation(incident)}>
            <span>Rapid waypoint</span>
            <strong>Prepare response</strong>
          </button>
          {!hasIncidentLocation(incident) && <small>Add an incident location before preparing a response.</small>}
        </div>
      )}

      <div className="incident-record__actions">
        <button type="button" onClick={() => onStatusChange(primaryStatus)} disabled={Boolean(pending)}>{pending === `status:${primaryStatus}` ? "Updating…" : primaryLabel}</button>
        {(incident.status === "OPEN" || incident.status === "ACTIVE") && (
          <button type="button" className="incident-record__cancel" onClick={() => onStatusChange("CANCELLED")} disabled={Boolean(pending)}>Cancel incident</button>
        )}
      </div>
      {error && <p className="incident-form__error" role="alert">{error}</p>}

      {incident.description && <p className="incident-record__description">{incident.description}</p>}
      <dl className="incident-record__facts">
        <div><dt>Location</dt><dd>{incident.address || incident.area || coordinateLabel(incident) || "Not supplied"}</dd></div>
        <div><dt>Coordinates</dt><dd>{coordinateLabel(incident) || "Required before response planning"}</dd></div>
        <div><dt>Source</dt><dd>{displayEnum(incident.sourceType)} · {displayEnum(incident.sourceSystem)}</dd></div>
        <div><dt>Revision</dt><dd>{incident.revision} · location {incident.locationRevision}</dd></div>
        <div><dt>Received</dt><dd>{formatDateTime(incident.receivedAtUnixMs)}</dd></div>
        <div><dt>Occurred</dt><dd>{incident.occurredAtUnixMs ? formatDateTime(incident.occurredAtUnixMs) : "Not supplied"}</dd></div>
      </dl>

      {detail.assignments.length > 0 && (
        <section className="incident-assignments" aria-labelledby="incident-assignments-title">
          <header>
            <h3 id="incident-assignments-title">Response assignments</h3>
            <span>{detail.assignments.length}</span>
          </header>
          <ol>
            {detail.assignments.map((assignment) => (
              <li key={assignment.id}>
                <div>
                  <span>{displayEnum(assignment.status)}</span>
                  <strong>{assignment.droneName}</strong>
                  <small>{assignment.missionName || `Mission ${shortId(assignment.missionId || "")}`}</small>
                  {assignment.onSceneAtUnixMs && <small>Hold acknowledged on scene · {formatDateTime(assignment.onSceneAtUnixMs)}</small>}
                </div>
                {assignment.missionId && !assignment.endedAtUnixMs && (
                  <button type="button" onClick={() => onOpenAssignment(assignment)} disabled={pending === `review:${assignment.id}`}>
                    {pending === `review:${assignment.id}` ? "Loading…" : assignment.status === "PREPARED" && !assignment.missionRunId ? "Review plan" : "Open mission"}
                  </button>
                )}
              </li>
            ))}
          </ol>
        </section>
      )}

      <section className="incident-timeline" aria-labelledby="incident-timeline-title">
        <header>
          <h3 id="incident-timeline-title">Incident timeline</h3>
          <span>{detail.events.length} events</span>
        </header>
        <ol>
          {[...detail.events].reverse().map((event) => (
            <li key={event.id}>
              <span className="incident-timeline__marker" aria-hidden="true" />
              <div>
                <strong>{event.message}</strong>
                <span>{displayEnum(event.eventType)} · {displayEnum(event.source)}</span>
              </div>
              <time dateTime={new Date(event.occurredAtUnixMs).toISOString()}>{formatRelativeTime(event.occurredAtUnixMs)}</time>
            </li>
          ))}
        </ol>
      </section>
    </div>
  );
}

function createInput(draft: IncidentDraft): CreateIncidentInput {
  return {
    incidentType: draft.incidentType.trim(),
    priority: draft.priority,
    summary: draft.summary.trim(),
    description: draft.description.trim(),
    latitude: draft.latitude,
    longitude: draft.longitude,
    address: draft.address.trim(),
    area: draft.area.trim(),
    occurredAtUnixMs: draft.occurredAt ? new Date(draft.occurredAt).getTime() : undefined,
  };
}

function updateInput(incident: IncidentSnapshot, status: IncidentStatus): UpdateIncidentInput {
  return {
    expectedRevision: incident.revision,
    incidentType: incident.incidentType,
    priority: incident.priority,
    status,
    summary: incident.summary,
    description: incident.description,
    latitude: incident.latitude,
    longitude: incident.longitude,
    address: incident.address,
    area: incident.area,
    occurredAtUnixMs: incident.occurredAtUnixMs,
  };
}

function validateDraft(draft: IncidentDraft) {
  if (!draft.incidentType.trim()) return "Incident type is required.";
  if (!draft.summary.trim()) return "Summary is required.";
  if ((draft.latitude === undefined) !== (draft.longitude === undefined)) return "Latitude and longitude must be supplied together.";
  if (draft.occurredAt && !Number.isFinite(new Date(draft.occurredAt).getTime())) return "Occurred-at time is invalid.";
  return undefined;
}

function validateResponseDraft(draft: ResponseDraft) {
  if (!draft.droneId) return "Select an aircraft for this response.";
  if (!Number.isFinite(draft.stagingLatitude) || draft.stagingLatitude < -90 || draft.stagingLatitude > 90) {
    return "Staging latitude must be between -90 and 90.";
  }
  if (!Number.isFinite(draft.stagingLongitude) || draft.stagingLongitude < -180 || draft.stagingLongitude > 180) {
    return "Staging longitude must be between -180 and 180.";
  }
  if (!Number.isFinite(draft.altitudeMeters) || draft.altitudeMeters < 2 || draft.altitudeMeters > 120) {
    return "Altitude must be between 2 and 120 metres.";
  }
  if (!Number.isFinite(draft.speedMps) || draft.speedMps < 0.5 || draft.speedMps > 15) {
    return "Speed must be between 0.5 and 15 m/s.";
  }
  if (!(["RETURN_TO_LAUNCH", "OPERATOR_INTERVENTION"] as string[]).includes(draft.arrivalFailurePolicy)) {
    return "Choose an explicit arrival-action failure policy.";
  }
  if (draft.pointGimbalAtIncident && (!Number.isFinite(draft.incidentTargetAltitudeAmslMeters) || draft.incidentTargetAltitudeAmslMeters! < -500 || draft.incidentTargetAltitudeAmslMeters! > 9000)) {
    return "Incident target altitude must be between -500 and 9000 metres AMSL.";
  }
  return undefined;
}

function aircraftSuitabilityScore(aircraft: FleetAircraft) {
  let score = 0;
  if (aircraft.connectionStatus === "connected") score += 8;
  if (aircraft.telemetry?.status === "live") score += 4;
  if (hasAircraftLocation(aircraft)) score += 2;
  if (aircraft.telemetry?.health?.armable) score += 1;
  return score;
}

function formatDuration(seconds: number) {
  if (seconds < 60) return `${Math.max(1, Math.round(seconds))} sec`;
  return `${Math.ceil(seconds / 60)} min`;
}

function actionLabel(actionType: string) {
  return actionType === "HOLD_AT_ARRIVAL" ? "Hold at arrival" : actionType === "POINT_GIMBAL_AT_INCIDENT" ? "Point gimbal at incident" : displayEnum(actionType);
}

function failurePolicyLabel(policy?: ArrivalFailurePolicy) {
  return policy === "RETURN_TO_LAUNCH" ? "Return to launch" : policy === "OPERATOR_INTERVENTION" ? "Operator intervention" : "Explicit policy required";
}

function failurePolicyDescription(policy?: ArrivalFailurePolicy) {
  return policy === "RETURN_TO_LAUNCH"
    ? "If an arrival action still fails after retries, request RTL and wait for PX4 acknowledgement."
    : "If an arrival action still fails after retries, keep the run open and require an operator decision.";
}

function shortId(value: string) {
  return value ? value.slice(0, 8).toUpperCase() : "—";
}

function optionalNumber(value: string) {
  if (!value.trim()) return undefined;
  const number = Number(value);
  return Number.isFinite(number) ? number : undefined;
}

function coordinateLabel(incident: IncidentSnapshot) {
  if (!hasIncidentLocation(incident)) return undefined;
  return `${incident.latitude.toFixed(6)}, ${incident.longitude.toFixed(6)}`;
}

function hasIncidentLocation(incident: IncidentSnapshot): incident is IncidentSnapshot & { latitude: number; longitude: number } {
  return Number.isFinite(incident.latitude) && Number.isFinite(incident.longitude);
}

function hasAircraftLocation(aircraft: FleetAircraft) {
  return Number.isFinite(aircraft.telemetry?.latitude) && Number.isFinite(aircraft.telemetry?.longitude);
}

function displayEnum(value: string) {
  return value.toLowerCase().replace(/_/g, " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function formatRelativeTime(value?: number | null) {
  if (!value) return "not yet";
  const seconds = Math.max(0, Math.round((Date.now() - value) / 1000));
  if (seconds < 10) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

function formatDateTime(value: number) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(value);
}

function messageFrom(reason: unknown) {
  return reason instanceof Error ? reason.message : String(reason);
}
