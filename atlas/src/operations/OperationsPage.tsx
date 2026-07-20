import { useEffect, useMemo, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { highestRelatedOperationalAlert, type OperationalAlertList } from "../alerts/OperationalAlerts";
import type {
  CreateIncidentInput,
  ArrivalFailurePolicy,
  FleetAircraft,
  FleetSnapshot,
  IncidentAssignment,
  IncidentDetail,
  IncidentPriority,
  IncidentResponseGeometry,
  IncidentResponseAircraftSuitability,
  IncidentResponsePattern,
  IncidentResponsePlanPreview,
  IncidentSnapshot,
  IncidentStatus,
  PrepareIncidentResponseInput,
  PreparedIncidentResponse,
  KnownBuildingAssessment,
  OperationalTrackGeolocation,
  UpdateIncidentInput,
} from "../operationsTypes";
import type { Mission, MissionAction, MissionPlan, MissionRun } from "../missions/missionTypes";
import { formatDistance, missionDistanceStatus } from "../missions/missionSafety";
import { LiveVideo } from "../video/LiveVideo";
import {
  defaultOperationsMapLayers,
  OperationsMap,
  type OperationsMapLayerVisibility,
} from "./OperationsMap";
import "./OperationsPage.css";

type OperationsPageProps = {
  nativeAvailable: boolean;
  fleet: FleetSnapshot;
  alerts: OperationalAlertList;
  onOpenAircraft: (droneId: string) => void;
  onConfirmResponse: (missionId: string, droneId: string) => void;
};

type ResponseLayout = "map" | "video" | "split";
type IncidentStatusFilter = "CURRENT" | "ALL" | IncidentStatus;
type SafetyCommandType = "hold" | "return_to_launch" | "land";

type TelemetryHistoryPage = {
  snapshots: Array<{
    telemetry: {
      receivedAtUnixMs: number;
      latitude?: number | null;
      longitude?: number | null;
    };
  }>;
};

type VehicleCommandReceipt = {
  id: string;
  droneId: string;
  commandType: string;
  status: string;
  deadlineAtUnixMs: number;
  resultCode: string;
  resultMessage: string;
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
  responsePattern: IncidentResponsePattern;
  geometryPoints: Array<{ latitude: number; longitude: number }>;
  altitudeMeters: number;
  speedMps: number;
  laneSpacingMeters: number;
  sweepAngleDegrees: number;
  orbitRadiusMeters: number;
  orbitLapsPerLevel: number;
  orbitDirection: "CLOCKWISE" | "COUNTERCLOCKWISE";
  orbitMaxVerticalRateMps: number;
  arrivalFailurePolicy: ArrivalFailurePolicy;
  incidentTargetAltitudeAmslMeters?: number;
  buildingHorizontalClearanceMeters: number;
  buildingVerticalClearanceMeters: number;
  knownBuildingOverrideReason: string;
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

const terminalMissionStates = new Set(["COMPLETED", "FAILED", "CANCELLED", "RTL"]);
const terminalVehicleCommandStates = new Set(["succeeded", "failed", "rejected", "timed_out", "cancelled"]);

export function OperationsPage({ nativeAvailable, fleet, alerts, onOpenAircraft, onConfirmResponse }: OperationsPageProps) {
  const [incidents, setIncidents] = useState<IncidentSnapshot[]>([]);
  const [runs, setRuns] = useState<MissionRun[]>([]);
  const [selectedIncidentId, setSelectedIncidentId] = useState<string>();
  const [detail, setDetail] = useState<IncidentDetail>();
  const [searchQuery, setSearchQuery] = useState("");
  const [priorityFilter, setPriorityFilter] = useState<"ALL" | IncidentPriority>("ALL");
  const [statusFilter, setStatusFilter] = useState<IncidentStatusFilter>("CURRENT");
  const [layout, setLayout] = useState<ResponseLayout>(() => storedResponseLayout());
  const [mapLayers, setMapLayers] = useState<OperationsMapLayerVisibility>(defaultOperationsMapLayers);
  const [livePlan, setLivePlan] = useState<MissionPlan>();
  const [aircraftTrail, setAircraftTrail] = useState<Array<{ latitude: number; longitude: number }>>([]);
  const [trackGeolocations, setTrackGeolocations] = useState<OperationalTrackGeolocation[]>([]);
  const [safetyCommand, setSafetyCommand] = useState<VehicleCommandReceipt>();
  const [safetyCommandPending, setSafetyCommandPending] = useState<SafetyCommandType>();
  const [safetyCommandResult, setSafetyCommandResult] = useState<string>();
  const [safetyCommandError, setSafetyCommandError] = useState<string>();
  const [creating, setCreating] = useState(false);
  const [responding, setResponding] = useState(false);
  const [responseDraft, setResponseDraft] = useState<ResponseDraft>();
  const [responseSuitability, setResponseSuitability] = useState<IncidentResponseAircraftSuitability[]>();
  const [responseSuitabilityError, setResponseSuitabilityError] = useState<string>();
  const [responsePreview, setResponsePreview] = useState<IncidentResponsePlanPreview>();
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
        const [nextIncidents, nextDetail, nextRuns, nextTrackGeolocations] = await Promise.all([
          invoke<IncidentSnapshot[]>("incident_list", { includeClosed: true, limit: 250 }),
          selectedIncidentId
            ? invoke<IncidentDetail>("incident_detail", { incidentId: selectedIncidentId })
            : Promise.resolve(undefined),
          invoke<MissionRun[]>("mission_run_history", { limit: 200 }),
          invoke<OperationalTrackGeolocation[]>("operational_track_geolocations", { limit: 250 }),
        ]);
        if (!active) return;
        setIncidents(nextIncidents);
        setRuns(nextRuns);
        setTrackGeolocations(nextTrackGeolocations);
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
  }, [creating, nativeAvailable, selectedIncidentId]);

  const selectedIncident = detail && detail.incident.id === selectedIncidentId ? detail.incident : undefined;
  const activeIncidents = incidents.filter((incident) => incident.status === "OPEN" || incident.status === "ACTIVE");
  const connectedAircraft = fleet.aircraft.filter((aircraft) => aircraft.connectionStatus === "connected");
  const positionedAircraft = fleet.aircraft.filter(hasAircraftLocation);
  const criticalIncidents = activeIncidents.filter((incident) => incident.priority === "CRITICAL");
  const unlocatedIncidents = activeIncidents.filter((incident) => !hasIncidentLocation(incident));
  const draftLocation = draft.latitude !== undefined && draft.longitude !== undefined
    ? { latitude: draft.latitude, longitude: draft.longitude }
    : undefined;
  const responseAircraft = useMemo(() => {
    const rank = new Map(responseSuitability?.map((candidate, index) => [candidate.droneId, index]) ?? []);
    return [...fleet.aircraft]
      .filter((aircraft): aircraft is FleetAircraft & { droneId: string } => Boolean(aircraft.droneId) && aircraft.vehicleStatus !== "archived")
      .sort((left, right) => (rank.get(left.droneId) ?? Number.MAX_SAFE_INTEGER) - (rank.get(right.droneId) ?? Number.MAX_SAFE_INTEGER));
  }, [fleet.aircraft, responseSuitability]);
  const responseGeometry = responseDraft ? {
    pattern: responseDraft.responsePattern,
    points: responseDraft.geometryPoints,
    radiusMeters: responseDraft.responsePattern === "BOUNDED_ORBIT" ? responseDraft.orbitRadiusMeters : undefined,
  } : undefined;
  const filteredIncidents = useMemo(
    () => incidents.filter((incident) => incidentMatchesFilters(incident, searchQuery, priorityFilter, statusFilter)),
    [incidents, priorityFilter, searchQuery, statusFilter],
  );
  const liveAssignment = useMemo(() => {
    if (!selectedIncident || detail?.incident.id !== selectedIncident.id) return undefined;
    const assignments = [...detail.assignments].sort((left, right) => right.assignedAtUnixMs - left.assignedAtUnixMs);
    return assignments.find((assignment) => {
      const run = runs.find((candidate) => candidate.id === assignment.missionRunId);
      return !assignment.endedAtUnixMs && run && !terminalMissionStates.has(run.status);
    }) ?? assignments.find((assignment) => !assignment.endedAtUnixMs);
  }, [detail, runs, selectedIncident]);
  const liveRun = runs.find((run) => run.id === liveAssignment?.missionRunId);
  const liveDroneId = responding ? responseDraft?.droneId : liveAssignment?.droneId;
  const liveAircraft = fleet.aircraft.find((aircraft) => aircraft.droneId === liveDroneId);
  const workspacePlan = preparedResponse?.plan ?? responsePreview ?? livePlan;
  const highestAlert = useMemo(
    () => highestRelatedOperationalAlert(alerts.alerts, { incidentId: selectedIncident?.id, droneId: liveDroneId, missionRunId: liveRun?.id }),
    [alerts.alerts, liveDroneId, liveRun?.id, selectedIncident?.id],
  );
  const recordingPlanned = workspacePlan?.actions.some((action) => action.actionType === "START_RECORDING") ?? false;
  const recordingMissionId = preparedResponse?.mission.id ?? liveAssignment?.missionId;
  const canIssueSafetyCommand = Boolean(
    nativeAvailable
      && liveDroneId
      && liveRun
      && !terminalMissionStates.has(liveRun.status)
      && liveAircraft?.connectionStatus === "connected"
      && liveAircraft.telemetry?.status === "live"
      && liveAircraft.telemetry.inAir === true
      && !safetyCommandPending,
  );

  const mapIncidents = useMemo(() => {
    if (!selectedIncident || filteredIncidents.some((incident) => incident.id === selectedIncident.id)) return filteredIncidents;
    return [selectedIncident, ...filteredIncidents];
  }, [filteredIncidents, selectedIncident]);

  useEffect(() => {
    window.localStorage.setItem("atlas.operations.responseLayout", layout);
  }, [layout]);

  useEffect(() => {
    if (!nativeAvailable || !responding || !selectedIncident || !responseDraft) {
      setResponseSuitability(undefined);
      setResponseSuitabilityError(undefined);
      return;
    }
    const incidentId = selectedIncident.id;
    const draft = responseDraft;
    const target = draft.geometryPoints[0] ?? (hasIncidentLocation(selectedIncident)
      ? { latitude: selectedIncident.latitude, longitude: selectedIncident.longitude }
      : undefined);
    if (!target || !validResponseCoordinate(target)) {
      setResponseSuitability(undefined);
      return;
    }
    let active = true;
    let reading = false;
    async function refreshSuitability() {
      if (reading) return;
      reading = true;
      try {
        const suitability = await invoke<IncidentResponseAircraftSuitability[]>("incident_response_aircraft_suitability", {
          incidentId,
          input: {
            responsePattern: draft.responsePattern,
            targetLatitude: target.latitude,
            targetLongitude: target.longitude,
            speedMps: draft.speedMps,
          },
        });
        if (!active) return;
        setResponseSuitability(suitability);
        setResponseSuitabilityError(undefined);
      } catch (reason) {
        if (active) {
          setResponseSuitability(undefined);
          setResponseSuitabilityError(messageFrom(reason));
        }
      } finally {
        reading = false;
      }
    }
    void refreshSuitability();
    const interval = window.setInterval(refreshSuitability, 2_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [nativeAvailable, responding, responseDraft?.geometryPoints, responseDraft?.responsePattern, responseDraft?.speedMps, selectedIncident]);

  useEffect(() => {
    if (!responding || !responseDraft || responseDraft.droneId || !responseSuitability) return;
    const recommended = responseSuitability.find((candidate) => candidate.recommended && candidate.available);
    if (!recommended) return;
    const aircraft = fleet.aircraft.find((candidate) => candidate.droneId === recommended.droneId);
    setResponseDraft((current) => current && !current.droneId ? {
      ...current,
      droneId: recommended.droneId,
      incidentTargetAltitudeAmslMeters: current.incidentTargetAltitudeAmslMeters
        ?? aircraft?.telemetry?.homePosition?.absoluteAltitudeM
        ?? undefined,
    } : current);
  }, [fleet.aircraft, responding, responseDraft, responseSuitability]);

  useEffect(() => {
    const missionId = liveAssignment?.missionId;
    if (!nativeAvailable || !missionId) {
      setLivePlan(undefined);
      return;
    }
    let active = true;
    setLivePlan(undefined);
    void invoke<MissionPlan>("mission_plan", { missionId })
      .then((plan) => { if (active) setLivePlan(plan); })
      .catch((reason) => { if (active) setLoadError(messageFrom(reason)); });
    return () => { active = false; };
  }, [liveAssignment?.missionId, nativeAvailable]);

  useEffect(() => {
    if (!nativeAvailable || !liveRun) {
      setAircraftTrail([]);
      return;
    }
    let active = true;
    setAircraftTrail([]);
    void invoke<TelemetryHistoryPage>("vehicle_telemetry_history", {
      droneId: liveRun.droneId,
      fromReceivedAtUnixMs: liveRun.startedAtUnixMs ?? liveRun.createdAtUnixMs,
      toReceivedAtUnixMs: liveRun.completedAtUnixMs ?? null,
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
      if (active) setLoadError(messageFrom(reason));
    });
    return () => { active = false; };
  }, [liveRun?.completedAtUnixMs, liveRun?.createdAtUnixMs, liveRun?.droneId, liveRun?.id, liveRun?.startedAtUnixMs, nativeAvailable]);

  useEffect(() => {
    const telemetry = liveAircraft?.telemetry;
    if (telemetry?.status !== "live") return;
    const position = validTrailPosition(telemetry);
    if (position.length === 0) return;
    setAircraftTrail((current) => appendTrailPosition(current, position[0]));
  }, [liveAircraft?.telemetry?.latitude, liveAircraft?.telemetry?.longitude, liveAircraft?.telemetry?.status]);

  useEffect(() => {
    if (!safetyCommand || terminalVehicleCommandStates.has(safetyCommand.status)) {
      if (safetyCommand) setSafetyCommandPending(undefined);
      return;
    }
    let active = true;
    const poll = window.setInterval(() => {
      void invoke<VehicleCommandReceipt>("vehicle_command_detail", { commandId: safetyCommand.id })
        .then((receipt) => {
          if (!active) return;
          setSafetyCommand(receipt);
          if (terminalVehicleCommandStates.has(receipt.status)) setSafetyCommandPending(undefined);
        })
        .catch((reason) => {
          if (active) setSafetyCommandError(messageFrom(reason));
        });
    }, 300);
    return () => {
      active = false;
      window.clearInterval(poll);
    };
  }, [safetyCommand?.id, safetyCommand?.status]);

  async function refreshAfterMutation(preferred: IncidentDetail) {
    setDetail(preferred);
    setSelectedIncidentId(preferred.incident.id);
    const next = await invoke<IncidentSnapshot[]>("incident_list", { includeClosed: true, limit: 250 });
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
      await refreshAfterMutation(updated);
    } catch (reason) {
      setActionError(messageFrom(reason));
    } finally {
      setPending(undefined);
    }
  }

  async function previewResponse() {
    if (!selectedIncident || !responseDraft) return;
    const validation = validateResponseDraft(responseDraft);
    if (validation) {
      setActionError(validation);
      return;
    }
    const suitability = responseSuitability?.find((candidate) => candidate.droneId === responseDraft.droneId);
    if (!suitability?.available) {
      setActionError(suitability
        ? `Selected aircraft is unavailable: ${suitability.blockers.map((reason) => reason.message).join(" · ")}`
        : "Wait for the authoritative aircraft suitability assessment before reviewing this response.");
      return;
    }
    setActionError(undefined);
    setPending("preview-response");
    try {
      const input = responseInput(selectedIncident, responseDraft);
      const preview = await invoke<IncidentResponsePlanPreview>("preview_incident_response", {
        incidentId: selectedIncident.id,
        input,
      });
      setResponsePreview(preview);
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
    const suitability = responseSuitability?.find((candidate) => candidate.droneId === responseDraft.droneId);
    if (!suitability?.available) {
      setActionError(suitability
        ? `Selected aircraft is unavailable: ${suitability.blockers.map((reason) => reason.message).join(" · ")}`
        : "Wait for the authoritative aircraft suitability assessment before preparing this response.");
      return;
    }
    if (!responsePreview) {
      setActionError("Assess the reviewed geometry before preparing its immutable response plan.");
      return;
    }
    if (responsePreview.knownBuildingAssessment.overrideRequired && !responseDraft.knownBuildingOverrideReason.trim()) {
      setActionError("Record an explicit operator override reason for the known-building warning before preparation.");
      return;
    }
    setActionError(undefined);
    setPending("prepare-response");
    try {
      const input = responseInput(selectedIncident, responseDraft);
      const prepared = await invoke<PreparedIncidentResponse>("prepare_incident_response", {
        incidentId: selectedIncident.id,
        input,
      });
      setPreparedResponse(prepared);
      setResponsePreview(undefined);
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

  async function abandonPreparedResponse() {
    if (!selectedIncident || !preparedResponse) return;
    const confirmed = window.confirm(
      `Release ${preparedResponse.assignment.droneName} and abandon this prepared response? The immutable mission and audit history will be retained.`,
    );
    if (!confirmed) return;
    setActionError(undefined);
    setPending("abandon-response");
    try {
      const updated = await invoke<IncidentDetail>("abandon_prepared_response", {
        incidentId: selectedIncident.id,
        assignmentId: preparedResponse.assignment.id,
        input: {
          expectedIncidentRevision: selectedIncident.revision,
          reason: "Operator released the prepared response before mission upload",
        },
      });
      await refreshAfterMutation(updated);
      setPreparedResponse(undefined);
      setResponding(false);
      setSelectingLocation(false);
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
      setPreparedResponse({ incident: selectedIncident, assignment, mission, plan });
      setResponseDraft(responseDraftFromPlan(plan, assignment.droneId, selectedIncident));
      setResponsePreview(undefined);
      setResponding(true);
      setSelectingLocation(false);
    } catch (reason) {
      setActionError(messageFrom(reason));
    } finally {
      setPending(undefined);
    }
  }

  async function requestSafetyCommand(commandType: SafetyCommandType) {
    if (!liveDroneId || !canIssueSafetyCommand) return;
    if (commandType !== "hold") {
      const confirmed = window.confirm(
        commandType === "return_to_launch"
          ? "Request Return to Launch for this aircraft? Atlas will retain the command acknowledgement as evidence."
          : "Request immediate Land for this aircraft? Use only when the reviewed landing area is safe.",
      );
      if (!confirmed) return;
    }
    setSafetyCommandPending(commandType);
    setSafetyCommandError(undefined);
    setSafetyCommandResult(undefined);
    if (commandType === "hold" || commandType === "return_to_launch") {
      try {
        const updated = await invoke<MissionRun>("control_mission_run", {
          missionRunId: liveRun!.id,
          operation: commandType === "hold" ? "pause" : "return_to_launch",
        });
        setRuns((current) => current.map((run) => run.id === updated.id ? updated : run));
        setSafetyCommandResult(commandType === "hold" ? "Hold acknowledged · run paused" : "RTL acknowledged · run ending");
      } catch (reason) {
        setSafetyCommandError(messageFrom(reason));
      } finally {
        setSafetyCommandPending(undefined);
      }
      return;
    }
    try {
      const receipt = await invoke<VehicleCommandReceipt>("request_vehicle_command", {
        droneId: liveDroneId,
        commandType,
        parametersJson: "{}",
        timeoutMs: 15_000,
      });
      setSafetyCommand(receipt);
      if (terminalVehicleCommandStates.has(receipt.status)) setSafetyCommandPending(undefined);
    } catch (reason) {
      setSafetyCommandError(messageFrom(reason));
      setSafetyCommandPending(undefined);
    }
  }

  function beginResponse() {
    if (!selectedIncident || !hasIncidentLocation(selectedIncident)) return;
    const defaultObservation = offsetCoordinate(selectedIncident, 0, 50);
    setResponseDraft({
      droneId: "",
      responsePattern: "OFFSET_OBSERVE",
      geometryPoints: [defaultObservation],
      altitudeMeters: 30,
      speedMps: 5,
      laneSpacingMeters: 25,
      sweepAngleDegrees: 0,
      orbitRadiusMeters: 40,
      orbitLapsPerLevel: 1,
      orbitDirection: "CLOCKWISE",
      orbitMaxVerticalRateMps: 1.5,
      arrivalFailurePolicy: "RETURN_TO_LAUNCH",
      incidentTargetAltitudeAmslMeters: undefined,
      buildingHorizontalClearanceMeters: 10,
      buildingVerticalClearanceMeters: 5,
      knownBuildingOverrideReason: "",
    });
    setResponsePreview(undefined);
    setResponseSuitability(undefined);
    setResponseSuitabilityError(undefined);
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

      {selectedIncident && (
        <section className={`response-context response-context--${selectedIncident.priority.toLowerCase()}`} aria-label="Selected live response">
          <div className="response-context__identity">
            <span className="response-context__priority"><i aria-hidden="true">{selectedIncident.priority === "CRITICAL" ? "▲" : "◆"}</i>{selectedIncident.priority}</span>
            <div>
              <small>Incident {shortId(selectedIncident.id)} · revision {selectedIncident.revision}</small>
              <strong>{selectedIncident.summary}</strong>
            </div>
          </div>
          <dl className="response-context__facts">
            <div><dt>Aircraft</dt><dd>{liveAircraft?.droneName || liveAssignment?.droneName || "Not assigned"}</dd></div>
            <div><dt>Run state</dt><dd>{liveResponseState(liveRun, workspacePlan?.metadata.incidentResponse?.responsePattern)}</dd></div>
            <div><dt>Run</dt><dd>{liveRun ? shortId(liveRun.id) : "Not started"}</dd></div>
            <div><dt>Alert</dt><dd>{highestAlert ? `${highestAlert.severity} · ${highestAlert.title}` : "No active safety alerts"}</dd></div>
          </dl>
          <div className="response-context__safety" aria-label="Immediate aircraft safety controls">
            <span>Flight safety</span>
            <div>
              <button type="button" disabled={!canIssueSafetyCommand || liveRun?.status !== "RUNNING"} onClick={() => void requestSafetyCommand("hold")}>Hold</button>
              <button type="button" disabled={!canIssueSafetyCommand || !liveRun || !["RUNNING", "PAUSED"].includes(liveRun.status)} onClick={() => void requestSafetyCommand("return_to_launch")}>RTL</button>
              <button type="button" className="response-context__land" disabled={!canIssueSafetyCommand} onClick={() => void requestSafetyCommand("land")}>Land</button>
            </div>
            <small>{safetyCommandPending
              ? `Sending ${displayEnum(safetyCommandPending)}…`
              : safetyCommandResult
                ? safetyCommandResult
                : safetyCommand && safetyCommand.droneId === liveDroneId
                ? `${displayEnum(safetyCommand.commandType)} · ${displayEnum(safetyCommand.status)}${safetyCommand.resultMessage ? ` · ${safetyCommand.resultMessage}` : ""}`
                : canIssueSafetyCommand
                  ? "Commands require PX4 acknowledgement."
                  : "Fresh in-air telemetry and a connected Agent are required."}</small>
            {safetyCommandError && <small className="response-context__command-error" role="alert">{safetyCommandError}</small>}
          </div>
        </section>
      )}

      <section className="operations-board" aria-label="Incident operations board">
        <aside className="incident-queue" aria-label="Incident queue">
          <header>
            <div>
              <p className="eyebrow">Dispatch queue</p>
              <h2>Incidents</h2>
            </div>
            {(searchQuery || priorityFilter !== "ALL" || statusFilter !== "CURRENT") && (
              <button type="button" className="operations-filters__clear" onClick={() => { setSearchQuery(""); setPriorityFilter("ALL"); setStatusFilter("CURRENT"); }}>Clear</button>
            )}
          </header>
          <div className="operations-filters" role="search" aria-label="Filter operations incidents">
            <label className="operations-filters__search">
              <span>Search</span>
              <input type="search" value={searchQuery} onChange={(event) => setSearchQuery(event.target.value)} placeholder="ID, type, place or summary" />
            </label>
            <label><span>Priority</span><select value={priorityFilter} onChange={(event) => setPriorityFilter(event.target.value as "ALL" | IncidentPriority)}>
              <option value="ALL">All priorities</option>
              <option value="CRITICAL">Critical</option>
              <option value="HIGH">High</option>
              <option value="MEDIUM">Medium</option>
              <option value="LOW">Low</option>
            </select></label>
            <label><span>Status</span><select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value as IncidentStatusFilter)}>
              <option value="CURRENT">Open + active</option>
              <option value="OPEN">Open</option>
              <option value="ACTIVE">Active</option>
              <option value="RESOLVED">Resolved</option>
              <option value="CANCELLED">Cancelled</option>
              <option value="ALL">All statuses</option>
            </select></label>
          </div>
          <div className="incident-queue__count">
            <span>{filteredIncidents.length} of {incidents.length} visible</span>
            <span>{positionedAircraft.length} aircraft mapped</span>
          </div>
          {filteredIncidents.length === 0 ? (
            <div className="incident-queue__empty">
              <strong>{incidents.length === 0 ? "No incidents in this queue" : "No incidents match"}</strong>
              <p>{incidents.length === 0 ? "Create a manual incident to establish operational context before planning a response." : "Adjust the search, priority, or status filters."}</p>
              {incidents.length === 0 && <button type="button" onClick={beginCreate} disabled={!nativeAvailable}>Create incident</button>}
            </div>
          ) : (
            <ol className="incident-queue__list">
              {filteredIncidents.map((incident) => (
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

        <div className="operations-board__live">
          <div className="response-workspace-toolbar">
            <div className="response-layout-switch" role="group" aria-label="Live response layout">
              {(["map", "video", "split"] as ResponseLayout[]).map((option) => (
                <button key={option} type="button" className={layout === option ? "response-layout-switch--active" : ""} aria-pressed={layout === option} onClick={() => setLayout(option)}>{displayEnum(option)}</button>
              ))}
            </div>
            <fieldset className="response-layer-controls" hidden={layout === "video"}>
              <legend>Map layers</legend>
              {(Object.keys(mapLayers) as Array<keyof OperationsMapLayerVisibility>).map((layer) => (
                <label key={layer}><input type="checkbox" checked={mapLayers[layer]} onChange={(event) => setMapLayers((current) => ({ ...current, [layer]: event.target.checked }))} /><span>{mapLayerLabel(layer)}</span></label>
              ))}
            </fieldset>
          </div>
          <div className={`response-live-surfaces response-live-surfaces--${layout}`} data-response-layout={layout}>
            <div className="response-live-panel response-live-panel--map" aria-label="Response map" aria-hidden={layout === "video"}>
              <span className="response-live-panel__label">Map · {aircraftTrail.length} trail points</span>
              <OperationsMap
                incidents={mapIncidents}
                aircraft={fleet.aircraft}
                selectedIncidentId={creating ? undefined : selectedIncidentId}
                draftLocation={creating ? draftLocation : undefined}
                draftResponseGeometry={responding && !preparedResponse ? responseGeometry : undefined}
                selectingLocation={(creating || (responding && !preparedResponse)) && selectingLocation}
                responsePlan={workspacePlan}
                responseDroneId={liveDroneId}
                aircraftTrail={aircraftTrail}
                trackGeolocations={trackGeolocations}
                layers={mapLayers}
                onIncidentSelect={selectIncident}
                onAircraftSelect={onOpenAircraft}
                onLocationSelect={(location) => {
                  if (creating) {
                    setDraft((current) => ({ ...current, latitude: location.latitude, longitude: location.longitude }));
                  } else if (responding) {
                    setResponseDraft((current) => current ? {
                      ...current,
                      geometryPoints: current.responsePattern === "BOUNDED_AREA_SCAN"
                        ? [...current.geometryPoints, location]
                        : [location],
                    } : current);
                    setResponsePreview(undefined);
                  }
                  setSelectingLocation(false);
                }}
              />
            </div>
            <div className="response-live-panel response-live-panel--video" aria-label="Response video" aria-hidden={layout === "map"}>
              <span className="response-live-panel__label">Video + perception · persistent subscription</span>
              <LiveVideo
                nativeAvailable={nativeAvailable}
                droneId={liveDroneId}
                aircraft={liveAircraft}
                highestAlert={highestAlert}
                recordingPlanned={recordingPlanned}
                recordingContext={{
                  incidentId: selectedIncident?.id ?? undefined,
                  missionId: recordingMissionId ?? undefined,
                  missionRunId: liveRun?.id,
                }}
                compact={layout === "map"}
              />
            </div>
          </div>
          <div className="response-workspace-status">
            <span>{workspacePlan ? `${workspacePlan.generatedWaypoints.length} route waypoint${workspacePlan.generatedWaypoints.length === 1 ? "" : "s"}` : "No response route selected"}</span>
            <span className={liveAircraft?.telemetry?.status === "live" ? "response-workspace-status__live" : "response-workspace-status__stale"}><i aria-hidden="true">{liveAircraft?.telemetry?.status === "live" ? "✓" : "!"}</i>{liveAircraft?.telemetry?.status === "live" ? "Live telemetry" : "Telemetry stale or unavailable"}</span>
            <span>{highestAlert ? `${highestAlert.severity}: ${highestAlert.recommendedAction}` : "No active response alert"}</span>
          </div>
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
              suitability={responseSuitability}
              suitabilityError={responseSuitabilityError}
              draft={responseDraft}
              prepared={preparedResponse}
              preview={responsePreview}
              pending={pending === "prepare-response"}
              previewing={pending === "preview-response"}
              abandoning={pending === "abandon-response"}
              selectingLocation={selectingLocation}
              error={actionError}
              onChange={(next) => {
                const assessmentInputsUnchanged = responseDraft
                  && responseAssessmentSignature(responseDraft) === responseAssessmentSignature(next);
                setResponseDraft(next);
                if (!assessmentInputsUnchanged) setResponsePreview(undefined);
              }}
              onSelectLocation={() => setSelectingLocation((current) => !current)}
              onPrepare={() => void prepareResponse()}
              onPreview={() => void previewResponse()}
              onBack={() => {
                setResponding(false);
                setPreparedResponse(undefined);
                setResponsePreview(undefined);
                setSelectingLocation(false);
                setActionError(undefined);
              }}
              onConfirm={() => {
                if (preparedResponse?.assignment.missionId) {
                  onConfirmResponse(preparedResponse.assignment.missionId, preparedResponse.assignment.droneId);
                }
              }}
              onAbandon={() => void abandonPreparedResponse()}
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
  suitability,
  suitabilityError,
  draft,
  prepared,
  preview,
  pending,
  previewing,
  abandoning,
  selectingLocation,
  error,
  onChange,
  onSelectLocation,
  onPrepare,
  onPreview,
  onBack,
  onConfirm,
  onAbandon,
}: {
  incident: IncidentSnapshot;
  aircraft: Array<FleetAircraft & { droneId: string }>;
  suitability?: IncidentResponseAircraftSuitability[];
  suitabilityError?: string;
  draft: ResponseDraft;
  prepared?: PreparedIncidentResponse;
  preview?: IncidentResponsePlanPreview;
  pending: boolean;
  previewing: boolean;
  abandoning: boolean;
  selectingLocation: boolean;
  error?: string;
  onChange: (draft: ResponseDraft) => void;
  onSelectLocation: () => void;
  onPrepare: () => void;
  onPreview: () => void;
  onBack: () => void;
  onConfirm: () => void;
  onAbandon: () => void;
}) {
  const selectedAircraft = aircraft.find((candidate) => candidate.droneId === draft.droneId);
  const selectedSuitability = suitability?.find((candidate) => candidate.droneId === draft.droneId);
  const unavailableAircraft = suitability?.filter((candidate) => !candidate.available) ?? [];
  const primaryPoint = draft.geometryPoints[0];
  const distance = missionDistanceStatus(
    primaryPoint ?? { latitude: incident.latitude ?? 0, longitude: incident.longitude ?? 0 },
    selectedAircraft,
  );
  const arrivalSeconds = distance.distanceMeters !== undefined && draft.speedMps > 0
    ? distance.distanceMeters / draft.speedMps
    : undefined;
  const waypoint = prepared?.plan.generatedWaypoints[0];
  const arrivalActions = prepared?.plan.actions.filter((action) => action.actionType === "HOLD_AT_ARRIVAL" || action.actionType === "POINT_GIMBAL_AT_INCIDENT" || action.actionType === "RESUME_AFTER_ARRIVAL") ?? [];
  const reviewedFailurePolicy = arrivalActions[0]?.params.failurePolicy as ArrivalFailurePolicy | undefined;
  const preparedEvidence = prepared?.plan.metadata.incidentResponse;
  const preparedPattern = preparedEvidence?.responsePattern ?? "OFFSET_OBSERVE";
  const preparedAssessment = preparedEvidence?.knownBuildingAssessment;

  if (prepared) {
    return (
      <div className="incident-response incident-response--prepared">
        <header>
          <div>
            <p className="eyebrow">Response prepared</p>
            <h2>Confirm {responsePatternLabel(preparedPattern)}</h2>
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
          <div><dt>Pattern</dt><dd>{responsePatternLabel(preparedPattern)}</dd></div>
          <div><dt>Geometry</dt><dd>{responseGeometrySummary(prepared.plan)}</dd></div>
          <div><dt>Route</dt><dd>{prepared.plan.generatedWaypoints.length} waypoints · {formatDistance(Number(prepared.plan.metadata.estimatedDistanceMeters ?? 0))}</dd></div>
          <div><dt>Altitude</dt><dd>{waypoint ? `${waypoint.altitudeMeters.toFixed(0)} m home-relative` : "—"}</dd></div>
          <div><dt>Departure</dt><dd>{distance.distanceMeters !== undefined ? `${formatDistance(distance.distanceMeters)} from ${distance.reference?.source ?? "aircraft"}` : "Revalidated before upload"}</dd></div>
          <div><dt>Incident evidence</dt><dd>Revision {prepared.incident.revision} · location {prepared.incident.locationRevision}</dd></div>
          <div><dt>Arrival</dt><dd>{preparedPattern === "HOLD_AT_STAGING" ? "Hold then wait for operator" : "Hold must be acknowledged"} · {Number(arrivalActions[0]?.params.maxAttempts ?? 3)} attempts</dd></div>
          <div><dt>Hold failure</dt><dd>{failurePolicyLabel(reviewedFailurePolicy)}</dd></div>
        </dl>

        {preparedAssessment && <KnownBuildingAssessmentPanel assessment={preparedAssessment} immutable />}

        <section className="response-arrival-review" aria-label="Reviewed arrival action chain">
          <header><span>Arrival authority</span><strong>{preparedPattern === "HOLD_AT_STAGING" ? "Staged after Hold; awaiting operator" : "Not on scene until Hold succeeds"}</strong></header>
          <ol>
            {arrivalActions.map((action) => (
              <li key={action.sequence}>
                <span>{String(action.sequence + 1).padStart(2, "0")}</span>
                <div>
                  <strong>{actionLabel(action.actionType)}</strong>
                  <small>{arrivalActionDescription(action)}</small>
                  <small>{formatActionTiming(action.params)} · {failurePolicyLabel(String(action.params.failurePolicy))}</small>
                </div>
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

        <button type="button" className="incident-response__confirm" onClick={onConfirm} disabled={!waypoint || abandoning}>
          Confirm & review deployment
        </button>
        <button type="button" className="incident-response__abandon" onClick={onAbandon} disabled={abandoning}>
          {abandoning ? "Releasing aircraft…" : "Abandon prepared response"}
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
          <h2>Review response geometry</h2>
        </div>
        <button type="button" className="incident-form__cancel" onClick={onBack}>Cancel</button>
      </header>
      <p className="incident-response__scope">Reviewed geometry · incident revision {incident.revision} · assessment and preparation do not upload or arm an aircraft</p>

      <fieldset className="response-pattern-picker">
        <legend>Response pattern</legend>
        {(["HOLD_AT_STAGING", "OFFSET_OBSERVE", "BOUNDED_AREA_SCAN", "BOUNDED_ORBIT"] as IncidentResponsePattern[]).map((pattern) => (
          <button
            key={pattern}
            type="button"
            className={draft.responsePattern === pattern ? "response-pattern-picker__option response-pattern-picker__option--active" : "response-pattern-picker__option"}
            aria-pressed={draft.responsePattern === pattern}
            onClick={() => onChange({
              ...draft,
              responsePattern: pattern,
              geometryPoints: pattern === "BOUNDED_AREA_SCAN"
                ? []
                : pattern === "BOUNDED_ORBIT" && hasIncidentLocation(incident)
                  ? [{ latitude: incident.latitude, longitude: incident.longitude }]
                  : draft.geometryPoints.slice(0, 1),
            })}
          >
            <strong>{responsePatternLabel(pattern)}</strong>
            <small>{responsePatternDescription(pattern)}</small>
          </button>
        ))}
      </fieldset>

      <label>Assigned aircraft
        <select value={draft.droneId} onChange={(event) => onChange({ ...draft, droneId: event.target.value })} required>
          <option value="">{suitability ? "Select dispatch-ready aircraft" : "Assessing aircraft suitability…"}</option>
          {aircraft.map((candidate) => (
            <option
              key={candidate.droneId}
              value={candidate.droneId}
              disabled={!suitability?.find((assessment) => assessment.droneId === candidate.droneId)?.available}
            >
              {aircraftSuitabilityOption(candidate, suitability?.find((assessment) => assessment.droneId === candidate.droneId))}
            </option>
          ))}
        </select>
      </label>
      {selectedAircraft && selectedSuitability && (
        <div className={selectedSuitability.available ? "response-aircraft-readout response-aircraft-readout--ready" : "response-aircraft-readout response-aircraft-readout--blocked"}>
          <span className={`map-status-dot map-status-dot--${selectedSuitability.available ? "ready" : "degraded"}`} />
          <strong>{selectedAircraft.droneName || selectedAircraft.droneId}{selectedSuitability.recommended ? " · Recommended" : ""}</strong>
          <small>{aircraftSuitabilitySummary(selectedSuitability)}</small>
          {selectedSuitability.considerations.map((reason) => <small key={reason.code}>{reason.message}</small>)}
        </div>
      )}
      {suitabilityError && <p className="incident-form__error" role="alert">Aircraft suitability unavailable: {suitabilityError}</p>}
      {unavailableAircraft.length > 0 && (
        <details className="response-aircraft-blockers">
          <summary>{unavailableAircraft.length} unavailable aircraft</summary>
          <ul>
            {unavailableAircraft.map((candidate) => (
              <li key={candidate.droneId}>
                <strong>{candidate.droneName || candidate.droneId}</strong>
                <span>{candidate.blockers.map((reason) => reason.message).join(" · ")}</span>
              </li>
            ))}
          </ul>
        </details>
      )}

      <fieldset className="incident-form__location response-staging-location">
        <legend>{draft.responsePattern === "HOLD_AT_STAGING" ? "Operator-reviewed staging point" : draft.responsePattern === "OFFSET_OBSERVE" ? "Operator-reviewed observation point" : draft.responsePattern === "BOUNDED_AREA_SCAN" ? "Operator-reviewed scan polygon" : "Operator-reviewed orbit centre"}</legend>
        <button type="button" className={selectingLocation ? "incident-form__map-select incident-form__map-select--active" : "incident-form__map-select"} onClick={onSelectLocation}>
          {selectingLocation
            ? draft.responsePattern === "BOUNDED_AREA_SCAN" ? "Click map to add polygon vertices" : "Click map to place coordinate"
            : draft.responsePattern === "BOUNDED_AREA_SCAN" ? "Add polygon vertices on map" : "Choose coordinate on map"}
        </button>
        {draft.responsePattern === "BOUNDED_AREA_SCAN" ? (
          <div className="response-geometry-sequence">
            <span>{draft.geometryPoints.length} polygon vertices</span>
            <div>
              <button type="button" onClick={() => onChange({ ...draft, geometryPoints: draft.geometryPoints.slice(0, -1) })} disabled={draft.geometryPoints.length === 0}>Undo vertex</button>
              <button type="button" onClick={() => onChange({ ...draft, geometryPoints: [] })} disabled={draft.geometryPoints.length === 0}>Clear polygon</button>
            </div>
            <small>Atlas shows and preserves the complete reviewed polygon; at least three vertices are required.</small>
          </div>
        ) : (
          <div className="incident-form__row">
            <label>Latitude
              <input type="number" min="-90" max="90" step="0.000001" value={primaryPoint?.latitude ?? ""} onChange={(event) => onChange({ ...draft, geometryPoints: [{ latitude: Number(event.target.value), longitude: primaryPoint?.longitude ?? 0 }] })} required />
            </label>
            <label>Longitude
              <input type="number" min="-180" max="180" step="0.000001" value={primaryPoint?.longitude ?? ""} onChange={(event) => onChange({ ...draft, geometryPoints: [{ latitude: primaryPoint?.latitude ?? 0, longitude: Number(event.target.value) }] })} required />
            </label>
          </div>
        )}
      </fieldset>

      <div className="incident-form__row response-flight-envelope">
        <label>Altitude · m
          <input type="number" min="2" max="120" step="1" value={draft.altitudeMeters} onChange={(event) => onChange({ ...draft, altitudeMeters: Number(event.target.value) })} required />
        </label>
        <label>Speed · m/s
          <input type="number" min="0.5" max="15" step="0.5" value={draft.speedMps} onChange={(event) => onChange({ ...draft, speedMps: Number(event.target.value) })} required />
        </label>
      </div>

      {draft.responsePattern === "BOUNDED_AREA_SCAN" && (
        <div className="incident-form__row response-flight-envelope">
          <label>Lane spacing · m
            <input type="number" min="1" max="500" step="1" value={draft.laneSpacingMeters} onChange={(event) => onChange({ ...draft, laneSpacingMeters: Number(event.target.value) })} required />
          </label>
          <label>Sweep angle · °
            <input type="number" min="-360" max="360" step="5" value={draft.sweepAngleDegrees} onChange={(event) => onChange({ ...draft, sweepAngleDegrees: Number(event.target.value) })} required />
          </label>
        </div>
      )}

      {draft.responsePattern === "BOUNDED_ORBIT" && (
        <fieldset className="response-orbit-envelope">
          <legend>Single-level orbit envelope</legend>
          <div className="incident-form__row">
            <label>Radius · m
              <input type="number" min="5" max="500" step="5" value={draft.orbitRadiusMeters} onChange={(event) => onChange({ ...draft, orbitRadiusMeters: Number(event.target.value) })} required />
            </label>
            <label>Laps
              <input type="number" min="1" max="10" step="1" value={draft.orbitLapsPerLevel} onChange={(event) => onChange({ ...draft, orbitLapsPerLevel: Number(event.target.value) })} required />
            </label>
          </div>
          <div className="incident-form__row">
            <label>Direction
              <select value={draft.orbitDirection} onChange={(event) => onChange({ ...draft, orbitDirection: event.target.value as ResponseDraft["orbitDirection"] })}>
                <option value="CLOCKWISE">Clockwise</option>
                <option value="COUNTERCLOCKWISE">Counter-clockwise</option>
              </select>
            </label>
            <label>Maximum vertical rate · m/s
              <input type="number" min="0.2" max="5" step="0.1" value={draft.orbitMaxVerticalRateMps} onChange={(event) => onChange({ ...draft, orbitMaxVerticalRateMps: Number(event.target.value) })} required />
            </label>
          </div>
          <p>Stepped-altitude levels remain disabled until the single-level orbit has live acceptance evidence. This plan records one explicit level and zero transitions.</p>
        </fieldset>
      )}

      <fieldset className="response-arrival-policy">
        <legend>Arrival authority</legend>
        <div className="response-arrival-policy__hold">
          <span>Required action</span>
          <strong>HOLD_AT_ARRIVAL</strong>
          <small>{draft.responsePattern === "HOLD_AT_STAGING"
            ? "Reaching staging is not enough. Atlas waits for PX4 Hold acknowledgement, pauses the run, and keeps the aircraft staged until an operator decides what happens next."
            : "Reaching the waypoint is not enough. Atlas waits for a PX4 Hold acknowledgement before recording on scene."}</small>
        </div>
        <label>After three failed action attempts
          <select value={draft.arrivalFailurePolicy} onChange={(event) => onChange({ ...draft, arrivalFailurePolicy: event.target.value as ArrivalFailurePolicy })}>
            <option value="RETURN_TO_LAUNCH">Request Return to launch</option>
            <option value="OPERATOR_INTERVENTION">Require operator intervention</option>
          </select>
          <small>{failurePolicyDescription(draft.arrivalFailurePolicy)}</small>
        </label>
        {(draft.responsePattern === "OFFSET_OBSERVE" || draft.responsePattern === "BOUNDED_ORBIT") && (
          <div className="response-arrival-policy__hold">
            <span>Reviewed payload action</span>
            <strong>POINT_GIMBAL_AT_INCIDENT</strong>
            <small>Atlas points at the incident throughout the observation/orbit and requests an acknowledged incident ROI after Hold. Exhaustion is recorded as a degraded optional action.</small>
          </div>
        )}
        {(draft.responsePattern === "OFFSET_OBSERVE" || draft.responsePattern === "BOUNDED_ORBIT") && (
          <label>Incident target altitude · m AMSL
            <input type="number" min="-500" max="9000" step="1" value={draft.incidentTargetAltitudeAmslMeters ?? ""} onChange={(event) => onChange({ ...draft, incidentTargetAltitudeAmslMeters: optionalNumber(event.target.value) })} required />
            <small>Geographic gimbal targeting requires a reviewed absolute target altitude.</small>
          </label>
        )}
      </fieldset>

      <fieldset className="response-building-settings">
        <legend>Known-building assessment</legend>
        <div className="incident-form__row">
          <label>Horizontal clearance · m
            <input type="number" min="0" max="100" step="1" value={draft.buildingHorizontalClearanceMeters} onChange={(event) => onChange({ ...draft, buildingHorizontalClearanceMeters: Number(event.target.value) })} required />
          </label>
          <label>Vertical clearance · m
            <input type="number" min="0" max="100" step="1" value={draft.buildingVerticalClearanceMeters} onChange={(event) => onChange({ ...draft, buildingVerticalClearanceMeters: Number(event.target.value) })} required />
          </label>
        </div>
        <p>Checks the configured OS dataset only. Atlas never labels this geometry “safe” or “obstacle-free”.</p>
      </fieldset>

      {preview && (
        <>
          <div className="response-preview-summary" role="status">
            <span>Generated route preview</span>
            <strong>{preview.generatedWaypoints.length} waypoints · {formatDistance(Number(preview.metadata.estimatedDistanceMeters ?? 0))}</strong>
            <small>{responseGeometrySummary(preview)} · revalidated during atomic preparation</small>
          </div>
          <KnownBuildingAssessmentPanel assessment={preview.knownBuildingAssessment} />
        </>
      )}

      {preview?.knownBuildingAssessment.overrideRequired && (
        <label className="response-building-override">Operator override reason
          <textarea
            rows={3}
            maxLength={500}
            value={draft.knownBuildingOverrideReason}
            onChange={(event) => onChange({ ...draft, knownBuildingOverrideReason: event.target.value })}
            placeholder="State why this supervised operation may proceed despite the identified limitation."
            required
          />
          <small>The reason is revalidated and recorded when the immutable plan is prepared.</small>
        </label>
      )}

      <div className={distance.ok ? "response-distance response-distance--ready" : "response-distance"}>
        <span>{distance.ok ? "Upload radius check" : "Deployment blocker"}</span>
        <strong>{distance.distanceMeters !== undefined ? formatDistance(distance.distanceMeters) : "Position unavailable"}</strong>
        <small>{arrivalSeconds !== undefined ? `Nominal travel ${formatDuration(arrivalSeconds)} at reviewed speed` : distance.message}</small>
      </div>
      {error && <p className="incident-form__error" role="alert">{error}</p>}
      <button type="button" className="incident-response__assess" onClick={onPreview} disabled={previewing || pending || !draft.droneId}>
        {previewing ? "Assessing route…" : preview ? "Re-assess geometry" : "Assess geometry"}
      </button>
      <button type="submit" className="incident-form__submit" disabled={pending || previewing || !preview || !draft.droneId || (preview.knownBuildingAssessment.overrideRequired && !draft.knownBuildingOverrideReason.trim())}>
        {pending ? "Preparing atomically…" : "Prepare immutable response"}
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
            <span>Offset · area · orbit</span>
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

function responseInput(incident: IncidentSnapshot, draft: ResponseDraft): PrepareIncidentResponseInput {
  return {
    expectedIncidentRevision: incident.revision,
    droneId: draft.droneId,
    geometry: responseGeometryInput(draft),
    arrivalFailurePolicy: draft.arrivalFailurePolicy,
    incidentTargetAltitudeAmslMeters: draft.responsePattern === "OFFSET_OBSERVE" || draft.responsePattern === "BOUNDED_ORBIT"
      ? draft.incidentTargetAltitudeAmslMeters
      : undefined,
    buildingHorizontalClearanceMeters: draft.buildingHorizontalClearanceMeters,
    buildingVerticalClearanceMeters: draft.buildingVerticalClearanceMeters,
    knownBuildingOverrideReason: draft.knownBuildingOverrideReason.trim() || undefined,
  };
}

function responseGeometryInput(draft: ResponseDraft): IncidentResponseGeometry {
  const primaryPoint = draft.geometryPoints[0] ?? { latitude: Number.NaN, longitude: Number.NaN };
  if (draft.responsePattern === "HOLD_AT_STAGING") {
    return {
      responsePattern: "HOLD_AT_STAGING",
      stagingLatitude: primaryPoint.latitude,
      stagingLongitude: primaryPoint.longitude,
      altitudeMeters: draft.altitudeMeters,
      speedMps: draft.speedMps,
    };
  }
  if (draft.responsePattern === "BOUNDED_AREA_SCAN") {
    return {
      responsePattern: "BOUNDED_AREA_SCAN",
      areaPolygon: draft.geometryPoints,
      altitudeMeters: draft.altitudeMeters,
      speedMps: draft.speedMps,
      laneSpacingMeters: draft.laneSpacingMeters,
      sweepAngleDegrees: draft.sweepAngleDegrees,
    };
  }
  if (draft.responsePattern === "BOUNDED_ORBIT") {
    return {
      responsePattern: "BOUNDED_ORBIT",
      centerLatitude: primaryPoint.latitude,
      centerLongitude: primaryPoint.longitude,
      radiusMeters: draft.orbitRadiusMeters,
      altitudeLevelsMeters: [draft.altitudeMeters],
      speedMps: draft.speedMps,
      lapsPerLevel: draft.orbitLapsPerLevel,
      direction: draft.orbitDirection,
      maxVerticalRateMps: draft.orbitMaxVerticalRateMps,
    };
  }
  return {
    responsePattern: "OFFSET_OBSERVE",
    observationLatitude: primaryPoint.latitude,
    observationLongitude: primaryPoint.longitude,
    altitudeMeters: draft.altitudeMeters,
    speedMps: draft.speedMps,
  };
}

function responseDraftFromPlan(plan: MissionPlan, droneId: string, incident: IncidentSnapshot): ResponseDraft {
  const evidence = plan.metadata.incidentResponse;
  const geometry = evidence?.reviewedGeometry ?? {};
  const rawPattern = geometry.responsePattern ?? evidence?.responsePattern;
  const responsePattern: IncidentResponsePattern = rawPattern === "HOLD_AT_STAGING" || rawPattern === "BOUNDED_AREA_SCAN" || rawPattern === "BOUNDED_ORBIT"
    ? rawPattern
    : "OFFSET_OBSERVE";
  const areaPolygon = Array.isArray(geometry.areaPolygon)
    ? geometry.areaPolygon.flatMap((value) => responseCoordinateFromUnknown(value) ?? [])
    : [];
  const geometryPoints = responsePattern === "BOUNDED_AREA_SCAN"
    ? areaPolygon
    : responsePattern === "BOUNDED_ORBIT"
      ? [{
          latitude: finiteNumber(geometry.centerLatitude, incident.latitude ?? 0),
          longitude: finiteNumber(geometry.centerLongitude, incident.longitude ?? 0),
        }]
      : responsePattern === "HOLD_AT_STAGING"
        ? [{
            latitude: finiteNumber(geometry.stagingLatitude, plan.generatedWaypoints[0]?.latitude ?? incident.latitude ?? 0),
            longitude: finiteNumber(geometry.stagingLongitude, plan.generatedWaypoints[0]?.longitude ?? incident.longitude ?? 0),
          }]
      : [{
          latitude: finiteNumber(geometry.observationLatitude, plan.generatedWaypoints[0]?.latitude ?? incident.latitude ?? 0),
          longitude: finiteNumber(geometry.observationLongitude, plan.generatedWaypoints[0]?.longitude ?? incident.longitude ?? 0),
        }];
  const altitudeLevels = Array.isArray(geometry.altitudeLevelsMeters) ? geometry.altitudeLevelsMeters : [];
  const hold = plan.actions.find((action) => action.actionType === "HOLD_AT_ARRIVAL");
  const gimbal = plan.actions.find((action) => action.actionType === "POINT_GIMBAL_AT_INCIDENT");
  const failurePolicy = hold?.params.failurePolicy === "OPERATOR_INTERVENTION" ? "OPERATOR_INTERVENTION" : "RETURN_TO_LAUNCH";
  return {
    droneId,
    responsePattern,
    geometryPoints,
    altitudeMeters: finiteNumber(
      responsePattern === "BOUNDED_ORBIT" ? altitudeLevels[0] : geometry.altitudeMeters,
      plan.generatedWaypoints[0]?.altitudeMeters ?? 30,
    ),
    speedMps: finiteNumber(geometry.speedMps, plan.generatedWaypoints[0]?.speedMps ?? 5),
    laneSpacingMeters: finiteNumber(geometry.laneSpacingMeters, plan.metadata.laneSpacingMeters ?? 25),
    sweepAngleDegrees: finiteNumber(geometry.sweepAngleDegrees, plan.metadata.sweepAngleDegrees ?? 0),
    orbitRadiusMeters: finiteNumber(geometry.radiusMeters, evidence?.orbit?.radiusMeters ?? 40),
    orbitLapsPerLevel: finiteNumber(geometry.lapsPerLevel, evidence?.orbit?.lapsPerLevel ?? 1),
    orbitDirection: geometry.direction === "COUNTERCLOCKWISE" ? "COUNTERCLOCKWISE" : "CLOCKWISE",
    orbitMaxVerticalRateMps: finiteNumber(geometry.maxVerticalRateMps, evidence?.orbit?.maxVerticalRateMps ?? 1.5),
    arrivalFailurePolicy: failurePolicy,
    incidentTargetAltitudeAmslMeters: finiteOptionalNumber(gimbal?.params.altitudeAmslMeters),
    buildingHorizontalClearanceMeters: evidence?.buildingHorizontalClearanceMeters ?? 10,
    buildingVerticalClearanceMeters: evidence?.buildingVerticalClearanceMeters ?? 5,
    knownBuildingOverrideReason: evidence?.knownBuildingOverrideReason ?? "",
  };
}

function responseAssessmentSignature(draft: ResponseDraft) {
  return JSON.stringify({ ...draft, knownBuildingOverrideReason: undefined });
}

function responsePatternLabel(pattern: string) {
  return pattern === "HOLD_AT_STAGING"
    ? "Hold at staging"
    : pattern === "BOUNDED_AREA_SCAN"
    ? "Bounded area scan"
    : pattern === "BOUNDED_ORBIT"
      ? "Bounded orbit"
      : "Offset observe";
}

function responsePatternDescription(pattern: IncidentResponsePattern) {
  return pattern === "HOLD_AT_STAGING"
    ? "Fly to a reviewed staging point, Hold, and await the next operator decision without targeting the incident."
    : pattern === "BOUNDED_AREA_SCAN"
    ? "Lawn-mower coverage inside a reviewed polygon."
    : pattern === "BOUNDED_ORBIT"
      ? "One reviewed radius, level, lap count and direction."
      : "Observe from an operator-placed offset, then Hold and point at the incident.";
}

function responseGeometrySummary(plan: Pick<MissionPlan, "generatedWaypoints" | "metadata">) {
  const geometry = plan.metadata.incidentResponse?.reviewedGeometry;
  if (!geometry) return `${plan.generatedWaypoints.length} reviewed waypoints`;
  if (geometry.responsePattern === "HOLD_AT_STAGING") {
    return `${finiteNumber(geometry.stagingLatitude, 0).toFixed(5)}, ${finiteNumber(geometry.stagingLongitude, 0).toFixed(5)} · hold only`;
  }
  if (geometry.responsePattern === "BOUNDED_AREA_SCAN") {
    const vertices = Array.isArray(geometry.areaPolygon) ? geometry.areaPolygon.length : 0;
    return `${vertices} vertices · ${finiteNumber(geometry.laneSpacingMeters, 0).toFixed(0)} m spacing · ${finiteNumber(geometry.sweepAngleDegrees, 0).toFixed(0)}° sweep`;
  }
  if (geometry.responsePattern === "BOUNDED_ORBIT") {
    const levels = Array.isArray(geometry.altitudeLevelsMeters) ? geometry.altitudeLevelsMeters.length : 0;
    return `${finiteNumber(geometry.radiusMeters, 0).toFixed(0)} m radius · ${levels} level · ${finiteNumber(geometry.lapsPerLevel, 0).toFixed(0)} lap · ${displayEnum(String(geometry.direction ?? "CLOCKWISE"))}`;
  }
  return `${finiteNumber(geometry.observationLatitude, 0).toFixed(5)}, ${finiteNumber(geometry.observationLongitude, 0).toFixed(5)} · operator reviewed`;
}

function KnownBuildingAssessmentPanel({ assessment, immutable = false }: { assessment: KnownBuildingAssessment; immutable?: boolean }) {
  const icon = assessment.status === "CLEAR_OF_CHECKED_VOLUMES" ? "✓" : assessment.status === "INTERSECTIONS" ? "×" : "!";
  const headline = assessment.status === "CLEAR_OF_CHECKED_VOLUMES"
    ? "Clear of checked known volumes"
    : assessment.status === "INTERSECTIONS"
      ? "Known building intersection"
      : assessment.status === "INCOMPLETE"
        ? "Assessment incomplete"
        : "Known-building data unavailable";
  return (
    <section className={`known-building-assessment known-building-assessment--${assessment.status.toLowerCase().replace(/_/g, "-")}`} aria-label="Known-building assessment">
      <header>
        <i aria-hidden="true">{icon}</i>
        <div>
          <span>{immutable ? "Locked assessment evidence" : "Geometry assessment"}</span>
          <strong>{headline}</strong>
        </div>
      </header>
      <p>{assessment.statement}</p>
      <dl>
        <div><dt>Route</dt><dd>{assessment.routeSegmentCount} segments</dd></div>
        <div><dt>Features</dt><dd>{assessment.checkedFeatureCount} checked</dd></div>
        <div><dt>Intersections</dt><dd>{assessment.intersectionCount}</dd></div>
        <div><dt>Unknown height</dt><dd>{assessment.unknownHeightCount}</dd></div>
        <div><dt>Clearance</dt><dd>{assessment.horizontalClearanceMeters.toFixed(0)} m H · {assessment.verticalClearanceMeters.toFixed(0)} m V</dd></div>
        <div><dt>Coverage</dt><dd>{assessment.coverageComplete ? "Route within dataset bounds" : "Incomplete or unavailable"}</dd></div>
      </dl>
      {assessment.provenance && (
        <div className="known-building-assessment__provenance">
          <span>Dataset provenance</span>
          <strong>{assessment.provenance.provider} · {assessment.provenance.product}</strong>
          <small>{assessment.provenance.datasetId} · schema {assessment.provenance.schemaVersion} · release {assessment.provenance.release}</small>
          <small>Retrieved {formatDateTime(assessment.provenance.retrievedAtUnixMs)}</small>
        </div>
      )}
      {assessment.issues.length > 0 && (
        <ol className="known-building-assessment__issues">
          {assessment.issues.slice(0, 8).map((issue) => (
            <li key={`${issue.featureId}:${issue.result}`}>
              <span aria-hidden="true">{issue.result === "INTERSECTION" ? "×" : "?"}</span>
              <div>
                <strong>{issue.featureId} · {displayEnum(issue.result)}</strong>
                <small>{issue.routeSegmentIndexes.length > 0 ? `Route segment${issue.routeSegmentIndexes.length === 1 ? "" : "s"} ${issue.routeSegmentIndexes.map((index) => index + 1).join(", ")}` : "Near the assessed route"} · {displayEnum(issue.heightSource)}</small>
                {issue.evidenceDate && <small>Height evidence {issue.evidenceDate}{issue.heightConfidence ? ` · confidence ${issue.heightConfidence}` : ""}</small>}
              </div>
            </li>
          ))}
          {assessment.issues.length > 8 && <li><span>+</span><div><strong>{assessment.issues.length - 8} additional issues retained in the plan</strong></div></li>}
        </ol>
      )}
      {assessment.overrideReason && <p className="known-building-assessment__override"><strong>Operator override recorded:</strong> {assessment.overrideReason}</p>}
      {assessment.limitations.length > 0 && (
        <div className="known-building-assessment__limitations">
          <strong>Limits of this check</strong>
          <ul>{assessment.limitations.map((limitation) => <li key={limitation}>{limitation}</li>)}</ul>
        </div>
      )}
      <small className="known-building-assessment__boundary">This assessment is not an obstacle-free or safe-route claim.</small>
    </section>
  );
}

function offsetCoordinate(incident: IncidentSnapshot & { latitude: number; longitude: number }, bearingDegrees: number, distanceMeters: number) {
  const earthRadiusMeters = 6_371_000;
  const angularDistance = distanceMeters / earthRadiusMeters;
  const bearing = bearingDegrees * Math.PI / 180;
  const latitude = incident.latitude * Math.PI / 180;
  const longitude = incident.longitude * Math.PI / 180;
  const nextLatitude = Math.asin(
    Math.sin(latitude) * Math.cos(angularDistance)
    + Math.cos(latitude) * Math.sin(angularDistance) * Math.cos(bearing),
  );
  const nextLongitude = longitude + Math.atan2(
    Math.sin(bearing) * Math.sin(angularDistance) * Math.cos(latitude),
    Math.cos(angularDistance) - Math.sin(latitude) * Math.sin(nextLatitude),
  );
  return { latitude: nextLatitude * 180 / Math.PI, longitude: nextLongitude * 180 / Math.PI };
}

function responseCoordinateFromUnknown(value: unknown) {
  if (!value || typeof value !== "object") return undefined;
  const coordinate = value as Record<string, unknown>;
  const point = { latitude: Number(coordinate.latitude), longitude: Number(coordinate.longitude) };
  return validResponseCoordinate(point) ? point : undefined;
}

function finiteNumber(value: unknown, fallback: number) {
  const number = Number(value);
  return Number.isFinite(number) ? number : fallback;
}

function finiteOptionalNumber(value: unknown) {
  const number = Number(value);
  return Number.isFinite(number) ? number : undefined;
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
  const requiredPointCount = draft.responsePattern === "BOUNDED_AREA_SCAN" ? 3 : 1;
  if (draft.geometryPoints.length < requiredPointCount || !draft.geometryPoints.every(validResponseCoordinate)) {
    return draft.responsePattern === "BOUNDED_AREA_SCAN"
      ? "Place at least three valid WGS84 vertices for the scan polygon."
      : "Place one valid WGS84 response coordinate.";
  }
  if (draft.responsePattern === "BOUNDED_AREA_SCAN") {
    const distinct = new Set(draft.geometryPoints.map((point) => `${point.latitude.toFixed(7)}:${point.longitude.toFixed(7)}`));
    if (distinct.size < 3) return "The scan polygon requires at least three distinct vertices.";
    if (!Number.isFinite(draft.laneSpacingMeters) || draft.laneSpacingMeters < 1 || draft.laneSpacingMeters > 500) return "Lane spacing must be between 1 and 500 metres.";
    if (!Number.isFinite(draft.sweepAngleDegrees) || draft.sweepAngleDegrees < -360 || draft.sweepAngleDegrees > 360) return "Sweep angle must be between -360 and 360 degrees.";
  }
  if (draft.responsePattern === "BOUNDED_ORBIT") {
    if (!Number.isFinite(draft.orbitRadiusMeters) || draft.orbitRadiusMeters < 5 || draft.orbitRadiusMeters > 500) return "Orbit radius must be between 5 and 500 metres.";
    if (!Number.isInteger(draft.orbitLapsPerLevel) || draft.orbitLapsPerLevel < 1 || draft.orbitLapsPerLevel > 10) return "Orbit laps must be a whole number between 1 and 10.";
    if (!Number.isFinite(draft.orbitMaxVerticalRateMps) || draft.orbitMaxVerticalRateMps < 0.2 || draft.orbitMaxVerticalRateMps > 5) return "Maximum vertical rate must be between 0.2 and 5 m/s.";
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
  if ((draft.responsePattern === "OFFSET_OBSERVE" || draft.responsePattern === "BOUNDED_ORBIT") && (!Number.isFinite(draft.incidentTargetAltitudeAmslMeters) || draft.incidentTargetAltitudeAmslMeters! < -500 || draft.incidentTargetAltitudeAmslMeters! > 9000)) {
    return "Incident target altitude must be between -500 and 9000 metres AMSL.";
  }
  if (!Number.isFinite(draft.buildingHorizontalClearanceMeters) || draft.buildingHorizontalClearanceMeters < 0 || draft.buildingHorizontalClearanceMeters > 100) return "Horizontal building clearance must be between 0 and 100 metres.";
  if (!Number.isFinite(draft.buildingVerticalClearanceMeters) || draft.buildingVerticalClearanceMeters < 0 || draft.buildingVerticalClearanceMeters > 100) return "Vertical building clearance must be between 0 and 100 metres.";
  if (draft.knownBuildingOverrideReason.length > 500) return "Known-building override reason must be 500 characters or fewer.";
  return undefined;
}

function validResponseCoordinate(point: { latitude: number; longitude: number }) {
  return Number.isFinite(point.latitude)
    && Number.isFinite(point.longitude)
    && point.latitude >= -90
    && point.latitude <= 90
    && point.longitude >= -180
    && point.longitude <= 180;
}

function aircraftSuitabilityOption(
  aircraft: FleetAircraft & { droneId: string },
  suitability?: IncidentResponseAircraftSuitability,
) {
  const name = aircraft.droneName || aircraft.droneId;
  if (!suitability) return `${name} · assessing`;
  if (!suitability.available) return `${name} · unavailable · ${suitability.blockers[0]?.message ?? "dispatch checks failed"}`;
  const facts = [
    suitability.recommended ? "Recommended" : "Available",
    suitability.estimatedArrivalSeconds != null ? `${formatDuration(suitability.estimatedArrivalSeconds)} ETA` : undefined,
    suitability.batteryPercent != null ? `${Math.round(suitability.batteryPercent)}% battery` : undefined,
  ].filter(Boolean);
  return `${name} · ${facts.join(" · ")}`;
}

function aircraftSuitabilitySummary(suitability: IncidentResponseAircraftSuitability) {
  if (!suitability.available) {
    return suitability.blockers.map((reason) => reason.message).join(" · ");
  }
  const facts = [
    suitability.distanceMeters != null ? formatDistance(suitability.distanceMeters) : undefined,
    suitability.estimatedArrivalSeconds != null ? `${formatDuration(suitability.estimatedArrivalSeconds)} estimated arrival` : undefined,
    suitability.batteryPercent != null ? `${Math.round(suitability.batteryPercent)}% battery` : undefined,
    suitability.telemetryAgeMs != null ? `${(suitability.telemetryAgeMs / 1_000).toFixed(1)} s telemetry age` : undefined,
  ].filter(Boolean);
  return facts.join(" · ") || "Dispatch-ready; upload and start checks still apply";
}

function formatDuration(seconds: number) {
  if (seconds < 60) return `${Math.max(1, Math.round(seconds))} sec`;
  return `${Math.ceil(seconds / 60)} min`;
}

function actionLabel(actionType: string) {
  return actionType === "HOLD_AT_ARRIVAL" ? "Hold at arrival" : actionType === "POINT_GIMBAL_AT_INCIDENT" ? "Point gimbal at incident" : actionType === "RESUME_AFTER_ARRIVAL" ? "Resume operational pattern" : displayEnum(actionType);
}

function arrivalActionDescription(action: MissionAction) {
  if (action.actionType === "HOLD_AT_ARRIVAL") return action.params.waitForOperatorDecision === true
    ? "Agent requests MAVSDK Hold, pauses the run, and waits for an explicit operator decision."
    : "Agent requests MAVSDK Hold and waits for PX4 acknowledgement.";
  if (action.actionType === "RESUME_AFTER_ARRIVAL") return "Agent resumes the remaining scan or orbit only after the arrival actions are durably acknowledged.";
  return `Target ${Number(action.params.latitude).toFixed(6)}, ${Number(action.params.longitude).toFixed(6)} · ${Number(action.params.altitudeAmslMeters).toFixed(0)} m AMSL`;
}

function failurePolicyLabel(policy?: string) {
  return policy === "RETURN_TO_LAUNCH"
    ? "Return to launch"
    : policy === "OPERATOR_INTERVENTION"
      ? "Operator intervention"
      : policy === "SKIP_OPTIONAL_AND_NOTIFY"
        ? "Skip optional action and notify"
        : "Explicit policy required";
}

function failurePolicyDescription(policy?: ArrivalFailurePolicy) {
  return policy === "RETURN_TO_LAUNCH"
    ? "If an arrival action still fails after retries, request RTL and wait for PX4 acknowledgement."
    : "If an arrival action still fails after retries, keep the run open and require an operator decision.";
}

function formatActionTiming(params: Record<string, unknown>) {
  const timeoutSeconds = Number(params.timeoutMs) / 1_000;
  const retrySeconds = Number(params.retryInitialDelayMs) / 1_000;
  const multiplier = Number(params.retryBackoffMultiplier);
  if (![timeoutSeconds, retrySeconds, multiplier].every(Number.isFinite)) return "Timing unavailable";
  return `${timeoutSeconds.toFixed(0)} s timeout · ${retrySeconds.toFixed(0)} s retry ×${multiplier.toFixed(1)}`;
}

function shortId(value: string) {
  return value ? value.slice(0, 8).toUpperCase() : "—";
}

function optionalNumber(value: string) {
  if (!value.trim()) return undefined;
  const number = Number(value);
  return Number.isFinite(number) ? number : undefined;
}

function storedResponseLayout(): ResponseLayout {
  try {
    const stored = window.localStorage.getItem("atlas.operations.responseLayout");
    return stored === "map" || stored === "video" || stored === "split" ? stored : "map";
  } catch {
    return "map";
  }
}

function incidentMatchesFilters(
  incident: IncidentSnapshot,
  query: string,
  priority: "ALL" | IncidentPriority,
  status: IncidentStatusFilter,
) {
  if (priority !== "ALL" && incident.priority !== priority) return false;
  if (status === "CURRENT") {
    if (incident.status !== "OPEN" && incident.status !== "ACTIVE") return false;
  } else if (status !== "ALL" && incident.status !== status) {
    return false;
  }
  const normalized = query.trim().toLowerCase();
  if (!normalized) return true;
  return [incident.id, incident.incidentType, incident.summary, incident.description, incident.address, incident.area]
    .some((value) => value.toLowerCase().includes(normalized));
}

function liveResponseState(run?: MissionRun, responsePattern?: string) {
  if (!run) return "Not started";
  const hold = run.actions.find((action) => action.actionType === "HOLD_AT_ARRIVAL");
  if (hold?.state === "SUCCEEDED") return responsePattern === "HOLD_AT_STAGING"
    ? "STAGED · AWAITING OPERATOR"
    : "ON SCENE · HOLD ACK";
  if (hold?.state === "RETRYING") return `${run.status} · HOLD RETRYING`;
  if (hold?.state === "FAILED" || hold?.state === "POLICY_APPLIED") return `${run.status} · HOLD UNRESOLVED`;
  return run.status;
}

function mapLayerLabel(layer: keyof OperationsMapLayerVisibility) {
  return layer === "responseRoute" ? "Route" : layer === "aircraftTrail" ? "Trail" : displayEnum(layer);
}

function validTrailPosition(telemetry: { latitude?: number | null; longitude?: number | null }) {
  if (telemetry.latitude == null || telemetry.longitude == null || !Number.isFinite(telemetry.latitude) || !Number.isFinite(telemetry.longitude)) return [];
  return [{ latitude: telemetry.latitude, longitude: telemetry.longitude }];
}

function mergeTrails(persisted: Array<{ latitude: number; longitude: number }>, live: Array<{ latitude: number; longitude: number }>) {
  const merged: Array<{ latitude: number; longitude: number }> = [];
  for (const position of [...persisted, ...live]) {
    const last = merged[merged.length - 1];
    if (!last || !sameTrailPosition(last, position)) merged.push(position);
  }
  return merged.slice(-500);
}

function appendTrailPosition(
  trail: Array<{ latitude: number; longitude: number }>,
  position: { latitude: number; longitude: number },
) {
  const last = trail[trail.length - 1];
  return last && sameTrailPosition(last, position) ? trail : [...trail.slice(-499), position];
}

function sameTrailPosition(
  left: { latitude: number; longitude: number },
  right: { latitude: number; longitude: number },
) {
  return Math.abs(left.latitude - right.latitude) < 0.0000005
    && Math.abs(left.longitude - right.longitude) < 0.0000005;
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
