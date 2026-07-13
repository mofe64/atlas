import { useMemo, useState, useEffect } from "react";
import { invoke } from "@tauri-apps/api/core";
import type { FleetAircraft } from "../operationsTypes";
import { OperationalMissionMap } from "./OperationalMissionMap";
import { buildTerrainProfileInput } from "./terrain";
import { formatDistance as formatSafetyDistance, homePositionReference, missionDistanceStatus, planningPositionReference } from "./missionSafety";
import {
  defaultSettings,
  fallbackTemplates,
  newPoint,
  type CameraMode,
  type GimbalYawMode,
  type Mission,
  type MissionPlan,
  type MissionPoint,
  type MissionSettings,
  type MissionTemplate,
  type MissionTemplateType,
} from "./missionTypes";
import "./MissionPage.css";

export function MissionPage({ nativeAvailable, fleetAircraft, preferredDroneId, onMissionReady }: {
  nativeAvailable: boolean;
  fleetAircraft: FleetAircraft[];
  preferredDroneId?: string;
  onMissionReady: (missionId: string) => void;
}) {
  const [templates, setTemplates] = useState<MissionTemplate[]>(fallbackTemplates);
  const [missions, setMissions] = useState<Mission[]>([]);
  const [templateType, setTemplateType] = useState<MissionTemplateType>("WAYPOINT");
  const [name, setName] = useState("New waypoint mission");
  const [description, setDescription] = useState("");
  const [settings, setSettings] = useState<MissionSettings>({ ...defaultSettings.WAYPOINT });
  const [points, setPoints] = useState<MissionPoint[]>([]);
  const [plan, setPlan] = useState<MissionPlan>();
  const [editingMissionId, setEditingMissionId] = useState<string>();
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [busyLabel, setBusyLabel] = useState("Generating plan…");

  useEffect(() => {
    if (!nativeAvailable) return;
    void Promise.all([
      invoke<MissionTemplate[]>("mission_templates"),
      invoke<Mission[]>("mission_list"),
    ]).then(([nextTemplates, nextMissions]) => {
      setTemplates(nextTemplates);
      setMissions(nextMissions);
    }).catch((reason) => setError(String(reason)));
  }, [nativeAvailable]);

  const selectedTemplate = useMemo(
    () => templates.find((template) => template.templateType === templateType) ?? fallbackTemplates[0],
    [templateType, templates],
  );
  const minimumPoints = templateType === "AREA_SCAN" ? 3 : templateType === "ROUTE_SCAN" ? 2 : 1;
  const geometryReady = points.length >= minimumPoints;
  const planningAircraft = useMemo(
    () => fleetAircraft.find((aircraft) => aircraft.droneId === preferredDroneId && aircraft.connectionStatus === "connected")
      ?? fleetAircraft.find((aircraft) => aircraft.connectionStatus === "connected" && aircraft.droneId),
    [fleetAircraft, preferredDroneId],
  );
  const planningReference = planningPositionReference(planningAircraft);
  const planningHome = homePositionReference(planningAircraft);
  const draftDistance = points[0] && planningAircraft ? missionDistanceStatus(points[0], planningAircraft) : undefined;

  function selectTemplate(next: MissionTemplateType) {
    setTemplateType(next);
    setName(next === "WAYPOINT" ? "New waypoint mission" : next === "AREA_SCAN" ? "New area scan" : "New route scan");
    setSettings({ ...defaultSettings[next] });
    setPoints([]);
    setPlan(undefined);
    setEditingMissionId(undefined);
    setError(undefined);
  }

  function addPoint(latitude: number, longitude: number) {
    setPoints((current) => [...current, newPoint(latitude, longitude)]);
    setPlan(undefined);
  }

  function updatePoint(id: string, patch: Partial<MissionPoint>) {
    setPoints((current) => current.map((point) => point.id === id ? { ...point, ...patch } : point));
    setPlan(undefined);
  }

  function changeSettings(patch: Partial<MissionSettings>) {
    setSettings((current) => ({ ...current, ...patch }));
    setPlan(undefined);
  }

  function removePoint(id: string) {
    setPoints((current) => current.filter((point) => point.id !== id));
    setPlan(undefined);
  }

  function movePoint(id: string, offset: -1 | 1) {
    setPoints((current) => {
      const index = current.findIndex((point) => point.id === id);
      const destination = index + offset;
      if (index < 0 || destination < 0 || destination >= current.length) return current;
      const next = [...current];
      [next[index], next[destination]] = [next[destination], next[index]];
      return next;
    });
    setPlan(undefined);
  }

  async function createAndPlan() {
    setError(undefined);
    const validationError = validateDraft(name, templateType, points, settings, planningHome);
    if (validationError) {
      setError(validationError);
      return;
    }
    setBusy(true);
    setBusyLabel("Generating route geometry…");
    try {
      const input = {
          templateType,
          name: name.trim(),
          description: description.trim(),
          selectedPattern: selectedTemplate.defaultPattern,
          params: missionParams(templateType, points, settings),
      };
      const mission = editingMissionId
        ? await invoke<Mission>("update_mission", { missionId: editingMissionId, input })
        : await invoke<Mission>("create_mission", { input });
      const basePlan = await invoke<MissionPlan>("generate_mission_plan", { missionId: mission.id });
      let generated = basePlan;
      if (settings.altitudeMode === "TERRAIN_CLEARANCE") {
        if (!planningHome) throw new Error("Terrain-aware planning requires a connected aircraft home position.");
        setBusyLabel("Loading DEM terrain…");
        const terrainProfile = await buildTerrainProfileInput(basePlan, planningHome, settings, (completed, total) => {
          if (completed === total || completed % 5 === 0) setBusyLabel(`Sampling terrain · ${completed} / ${total}`);
        });
        setBusyLabel("Validating altitude profile…");
        generated = await invoke<MissionPlan>("apply_mission_terrain_profile", { missionId: mission.id, input: terrainProfile });
      }
      setEditingMissionId(mission.id);
      setPlan(generated);
      setMissions(await invoke<Mission[]>("mission_list"));
      onMissionReady(mission.id);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setBusy(false);
    }
  }

  async function loadMission(mission: Mission) {
    setError(undefined);
    try {
      const nextType = mission.templateType;
      setTemplateType(nextType);
      setName(mission.name);
      setDescription(mission.description);
      setEditingMissionId(mission.id);
      setSettings(settingsFromMission(nextType, mission.params));
      setPoints(pointsFromMission(nextType, mission.params));
      setPlan(await invoke<MissionPlan>("mission_plan", { missionId: mission.id }));
      window.scrollTo({ top: 0, behavior: "smooth" });
    } catch (reason) {
      setError(String(reason));
    }
  }

  return (
    <main className="mission-workspace" id="main-content">
      <header className="mission-heading">
        <div>
          <p className="eyebrow">Mission operations</p>
          <h1>Plan the flight</h1>
          <p>Draw the mission geometry and verify the generated path. Saving a valid plan opens a separate live execution workspace.</p>
        </div>
        <div className="mission-safety-note">
          <strong>Operational boundary</strong>
          <span>Planning never commands an aircraft. Upload, preflight checks, arm, start, and tracking happen in the execution workspace.</span>
        </div>
      </header>

      <section className="mission-builder" aria-label="Mission builder">
        <aside className="mission-control-rail">
          <header className="control-rail-heading">
            <div><span>01</span><strong>Mission definition</strong></div>
            <small>{selectedTemplate.defaultPattern.replace(/_/g, " ")}</small>
          </header>

          <fieldset className="template-picker">
            <legend>Template</legend>
            {templates.map((template) => (
              <button
                key={template.templateType}
                type="button"
                className={templateType === template.templateType ? "template-option template-option--active" : "template-option"}
                onClick={() => selectTemplate(template.templateType)}
                aria-pressed={templateType === template.templateType}
              >
                <strong>{template.name}</strong>
                <span>{template.description}</span>
              </button>
            ))}
          </fieldset>

          <div className="mission-identity-fields">
            <label>Mission name<input value={name} maxLength={120} onChange={(event) => setName(event.target.value)} /></label>
            <label>Description <span>optional</span><input value={description} onChange={(event) => setDescription(event.target.value)} /></label>
          </div>
          {editingMissionId && (
            <div className="editing-context">
              <span>Editing saved definition</span>
              <button type="button" onClick={() => selectTemplate(templateType)}>Start new</button>
            </div>
          )}

          <TemplateSettings templateType={templateType} settings={settings} onChange={changeSettings} />

          <ViewSettings templateType={templateType} settings={settings} onChange={changeSettings} />

          <details className="mission-advanced-settings">
            <summary>Detection and completion</summary>
            <div className="settings-grid settings-grid--single">
              <label>Detection classes<input value={settings.detectionClasses} placeholder="person, vehicle, smoke" onChange={(event) => changeSettings({ detectionClasses: event.target.value })} /></label>
              <label className="toggle-field"><input type="checkbox" checked={settings.recordVideo} onChange={(event) => changeSettings({ recordVideo: event.target.checked })} /><span>Record video during scan</span></label>
              <label className="toggle-field"><input type="checkbox" checked={settings.returnToLaunch} onChange={(event) => changeSettings({ returnToLaunch: event.target.checked })} /><span>Return to launch on completion</span></label>
            </div>
          </details>

          <div className="geometry-summary">
            <span>Geometry</span>
            <strong className={geometryReady ? "geometry-ready" : undefined}>{points.length} / {minimumPoints} minimum points</strong>
          </div>
          <div className={planningReference ? "mission-location-context mission-location-context--live" : "mission-location-context"}>
            <span>{planningReference ? "Planning reference" : "Map reference"}</span>
            <strong>{planningReference ? planningReference.droneName : "No connected aircraft position"}</strong>
            <small>{planningReference
              ? `Centred on ${planningReference.source === "aircraft" ? "live aircraft position" : "reported home position"}.${planningHome ? " Home is marked on the map." : ""}`
              : "Using the default map location until an aircraft reports its position."}</small>
          </div>
          {draftDistance && !draftDistance.ok && (
            <div className="mission-distance-warning" role="alert">
              <strong>Route is too far from the aircraft</strong>
              <span>{draftDistance.message}</span>
            </div>
          )}
          {draftDistance?.ok && draftDistance.distanceMeters !== undefined && (
            <p className="mission-distance-ready">First waypoint · {formatSafetyDistance(draftDistance.distanceMeters)} from {draftDistance.reference?.source} position</p>
          )}
          {error && <p className="mission-error" role="alert">{error}</p>}
          <button className="plan-button" type="button" disabled={!nativeAvailable || busy || !geometryReady} onClick={() => void createAndPlan()}>
            {busy ? busyLabel : editingMissionId ? "Update & generate new plan" : "Save & generate plan"}
          </button>
          {!nativeAvailable && <p className="native-boundary-note">Open this workspace in Atlas Native to persist and generate plans.</p>}
        </aside>

        <div className="mission-map-stage">
          <header className="map-stage-heading">
            <div><span>02</span><strong>{geometryTitle(templateType)}</strong></div>
            <div className="map-stage-actions">
              <button type="button" disabled={points.length === 0} onClick={() => { setPoints((current) => current.slice(0, -1)); setPlan(undefined); }}>Undo point</button>
              <button type="button" disabled={points.length === 0} onClick={() => { setPoints([]); setPlan(undefined); }}>Clear</button>
            </div>
          </header>
          <OperationalMissionMap
            templateType={templateType}
            points={points}
            planWaypoints={plan?.generatedWaypoints ?? []}
            planningFocus={planningReference}
            home={planningHome}
            onAddPoint={addPoint}
            onMovePoint={(id, latitude, longitude) => updatePoint(id, { latitude, longitude })}
          />
          <CoordinateEditor
            templateType={templateType}
            points={points}
            settings={settings}
            onUpdate={updatePoint}
            onRemove={removePoint}
            onMove={movePoint}
          />
          <PlanConsole plan={plan} />
        </div>
      </section>

      <section className="mission-library" aria-labelledby="mission-library-title">
        <header>
          <div><p className="eyebrow">Local mission library</p><h2 id="mission-library-title">Saved definitions</h2></div>
          <span>{missions.length} total</span>
        </header>
        {missions.length === 0 ? (
          <p className="mission-library-empty">No missions have been saved in this local ground station.</p>
        ) : (
          <div className="mission-table" role="list">
            {missions.map((mission) => (
              <div key={mission.id} className="mission-table__row" role="listitem">
                <button className="mission-table__open" type="button" onClick={() => onMissionReady(mission.id)} disabled={!mission.generatedPlanId}>
                  <span><strong>{mission.name}</strong><small>{mission.templateType.replace(/_/g, " ")} · {mission.selectedPattern.replace(/_/g, " ")}</small></span>
                  <span className="mission-table__status">{mission.generatedPlanId ? "Open execution" : mission.status}</span>
                </button>
                <button className="mission-table__edit" type="button" onClick={() => void loadMission(mission)}>Edit</button>
              </div>
            ))}
          </div>
        )}
      </section>
    </main>
  );
}

function TemplateSettings({ templateType, settings, onChange }: {
  templateType: MissionTemplateType;
  settings: MissionSettings;
  onChange: (patch: Partial<MissionSettings>) => void;
}) {
  if (templateType === "WAYPOINT") {
    return (
      <section className="flight-settings" aria-labelledby="flight-settings-title">
        <h2 id="flight-settings-title">Flight defaults</h2>
        <AltitudeReferenceSettings settings={settings} onChange={onChange} />
        <div className="settings-grid">
          <NumberField label={settings.altitudeMode === "TERRAIN_CLEARANCE" ? "Ground clearance" : "Altitude"} unit="m" value={settings.defaultAltitudeMeters} min={2} max={120} onChange={(value) => onChange({ defaultAltitudeMeters: value })} />
          <NumberField label="Speed" unit="m/s" value={settings.defaultSpeedMps} min={0.5} max={15} step={0.5} onChange={(value) => onChange({ defaultSpeedMps: value })} />
          {settings.altitudeMode === "HOME_RELATIVE" && <NumberField label="Takeoff" unit="m" value={settings.takeoffAltitudeMeters} min={2} max={120} onChange={(value) => onChange({ takeoffAltitudeMeters: value })} />}
        </div>
        {settings.altitudeMode === "TERRAIN_CLEARANCE" && <TerrainProfileSettings settings={settings} onChange={onChange} />}
      </section>
    );
  }
  if (templateType === "AREA_SCAN") {
    return (
      <section className="flight-settings" aria-labelledby="flight-settings-title">
        <h2 id="flight-settings-title">Coverage settings</h2>
        <AltitudeReferenceSettings settings={settings} onChange={onChange} />
        <div className="settings-grid">
          <NumberField label={settings.altitudeMode === "TERRAIN_CLEARANCE" ? "Ground clearance" : "Altitude"} unit="m" value={settings.altitudeMeters} min={2} max={120} onChange={(value) => onChange({ altitudeMeters: value })} />
          <NumberField label="Speed" unit="m/s" value={settings.speedMps} min={0.5} max={15} step={0.5} onChange={(value) => onChange({ speedMps: value })} />
          <NumberField label="Lane spacing" unit="m" value={settings.laneSpacingMeters} min={1} max={500} onChange={(value) => onChange({ laneSpacingMeters: value })} />
          <NumberField label="Overlap" unit="%" value={settings.overlapPercent} min={0} max={89} onChange={(value) => onChange({ overlapPercent: value })} />
          <NumberField label="Sweep angle" unit="°" value={settings.sweepAngleDegrees} min={-180} max={180} onChange={(value) => onChange({ sweepAngleDegrees: value })} />
        </div>
        {settings.altitudeMode === "TERRAIN_CLEARANCE" && <TerrainProfileSettings settings={settings} onChange={onChange} />}
      </section>
    );
  }
  return (
    <section className="flight-settings" aria-labelledby="flight-settings-title">
      <h2 id="flight-settings-title">Corridor settings</h2>
      <AltitudeReferenceSettings settings={settings} onChange={onChange} />
      <div className="settings-grid">
        <NumberField label={settings.altitudeMode === "TERRAIN_CLEARANCE" ? "Ground clearance" : "Altitude"} unit="m" value={settings.altitudeMeters} min={2} max={120} onChange={(value) => onChange({ altitudeMeters: value })} />
        <NumberField label="Speed" unit="m/s" value={settings.speedMps} min={0.5} max={15} step={0.5} onChange={(value) => onChange({ speedMps: value })} />
        <NumberField label="Sample spacing" unit="m" value={settings.sampleSpacingMeters} min={1} max={10000} onChange={(value) => onChange({ sampleSpacingMeters: value })} />
        <NumberField label="Corridor width" unit="m" value={settings.corridorWidthMeters} min={1} max={5000} onChange={(value) => onChange({ corridorWidthMeters: value })} />
      </div>
      {settings.altitudeMode === "TERRAIN_CLEARANCE" && <TerrainProfileSettings settings={settings} onChange={onChange} />}
    </section>
  );
}

function AltitudeReferenceSettings({ settings, onChange }: {
  settings: MissionSettings;
  onChange: (patch: Partial<MissionSettings>) => void;
}) {
  return (
    <label className="altitude-reference">Altitude reference
      <select value={settings.altitudeMode} onChange={(event) => onChange({ altitudeMode: event.target.value as MissionSettings["altitudeMode"] })}>
        <option value="HOME_RELATIVE">Fixed above home</option>
        <option value="TERRAIN_CLEARANCE">Terrain-aware clearance</option>
      </select>
      <small>{settings.altitudeMode === "TERRAIN_CLEARANCE" ? "Atlas precomputes a DEM altitude profile before upload; this is not live terrain following." : "Every waypoint uses a fixed altitude relative to the aircraft home."}</small>
    </label>
  );
}

function TerrainProfileSettings({ settings, onChange }: {
  settings: MissionSettings;
  onChange: (patch: Partial<MissionSettings>) => void;
}) {
  return (
    <div className="terrain-profile-settings">
      <div className="terrain-profile-settings__heading"><strong>Terrain safety envelope</strong><span>Centre + corridor edges</span></div>
      <div className="settings-grid">
        <NumberField label="Safety margin" unit="m" value={settings.terrainSafetyMarginMeters} min={0} max={100} onChange={(value) => onChange({ terrainSafetyMarginMeters: value })} />
        <NumberField label="DEM spacing" unit="m" value={settings.terrainSampleSpacingMeters} min={5} max={200} onChange={(value) => onChange({ terrainSampleSpacingMeters: value })} />
        <NumberField label="Terrain corridor" unit="m" value={settings.terrainCorridorWidthMeters} min={0} max={5000} onChange={(value) => onChange({ terrainCorridorWidthMeters: value })} />
        <NumberField label="Relative ceiling" unit="m" value={settings.terrainMaxRelativeAltitudeMeters} min={20} max={1000} onChange={(value) => onChange({ terrainMaxRelativeAltitudeMeters: value })} />
        <NumberField label="Max climb" unit="m/s" value={settings.terrainMaxClimbRateMps} min={0.2} max={10} step={0.1} onChange={(value) => onChange({ terrainMaxClimbRateMps: value })} />
        <NumberField label="Max descent" unit="m/s" value={settings.terrainMaxDescentRateMps} min={0.2} max={10} step={0.1} onChange={(value) => onChange({ terrainMaxDescentRateMps: value })} />
      </div>
    </div>
  );
}

function ViewSettings({ templateType, settings, onChange }: {
  templateType: MissionTemplateType;
  settings: MissionSettings;
  onChange: (patch: Partial<MissionSettings>) => void;
}) {
  return (
    <section className="view-settings" aria-labelledby="view-settings-title">
      <div className="view-settings__heading">
        <h2 id="view-settings-title">Camera & gimbal behaviour</h2>
        <span>Template default · operator override</span>
      </div>
      <div className="settings-grid">
        <label>Camera mode
          <select value={settings.cameraMode} onChange={(event) => {
            const cameraMode = event.target.value as CameraMode;
            onChange({ cameraMode, ...viewPreset(templateType, cameraMode) });
          }}>
            <option value="FORWARD_OBLIQUE">Forward oblique</option>
            <option value="DOWNWARD_SCAN">Downward scan</option>
            <option value="DOWNWARD_OBLIQUE_SCAN">Downward oblique</option>
            <option value="LOOK_AT_POINT">Look at point</option>
            <option value="FIXED_ANGLE">Fixed angle</option>
          </select>
        </label>
        <NumberField label="Gimbal pitch" unit="°" value={settings.gimbalPitchDegrees} min={-90} max={30} onChange={(value) => onChange({ gimbalPitchDegrees: value })} />
        <NumberField label="Mission zoom" unit="%" value={settings.zoomPercent} min={0} max={100} step={5} onChange={(value) => onChange({ zoomPercent: value })} />
        <label>Yaw behaviour
          <select value={settings.gimbalYawMode} onChange={(event) => onChange({ gimbalYawMode: event.target.value as GimbalYawMode })}>
            <option value="FOLLOW_DRONE_HEADING">Follow drone heading</option>
            <option value="FOLLOW_LANE_DIRECTION">Follow lane direction</option>
            <option value="FOLLOW_ROUTE_BEARING">Follow route bearing</option>
            <option value="LOOK_AT_POINT">Look at point</option>
            <option value="FIXED_ANGLE">Fixed angle</option>
          </select>
        </label>
        {settings.gimbalYawMode === "FIXED_ANGLE" && (
          <NumberField label="Gimbal yaw" unit="°" value={settings.gimbalYawDegrees} min={-180} max={180} onChange={(value) => onChange({ gimbalYawDegrees: value })} />
        )}
      </div>
      {settings.gimbalYawMode === "LOOK_AT_POINT" && (
        <div className="settings-grid view-target-fields">
          <OptionalNumberField label="Target latitude" value={settings.gimbalTargetLatitude} min={-90} max={90} step={0.000001} onChange={(value) => onChange({ gimbalTargetLatitude: value })} />
          <OptionalNumberField label="Target longitude" value={settings.gimbalTargetLongitude} min={-180} max={180} step={0.000001} onChange={(value) => onChange({ gimbalTargetLongitude: value })} />
        </div>
      )}
      <label className="toggle-field view-stabilization"><input type="checkbox" checked={settings.gimbalStabilization} onChange={(event) => onChange({ gimbalStabilization: event.target.checked })} /><span>Stabilize gimbal through this mission</span></label>
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
  return <label>{label}<span className="number-input"><input type="number" value={value} min={min} max={max} step={step} onChange={(event) => onChange(Number(event.target.value))} /><small>{unit}</small></span></label>;
}

function OptionalNumberField({ label, value, min, max, step = 1, onChange }: {
  label: string;
  value?: number;
  min: number;
  max: number;
  step?: number;
  onChange: (value: number | undefined) => void;
}) {
  return <label>{label}<input type="number" value={value ?? ""} min={min} max={max} step={step} onChange={(event) => onChange(optionalNumber(event.target.value))} /></label>;
}

function CoordinateEditor({ templateType, points, settings, onUpdate, onRemove, onMove }: {
  templateType: MissionTemplateType;
  points: MissionPoint[];
  settings: MissionSettings;
  onUpdate: (id: string, patch: Partial<MissionPoint>) => void;
  onRemove: (id: string) => void;
  onMove: (id: string, offset: -1 | 1) => void;
}) {
  return (
    <section className="coordinate-editor" aria-labelledby="coordinate-editor-title">
      <header><div><span>03</span><strong id="coordinate-editor-title">{vertexLabel(templateType)} coordinates</strong></div><small>Drag on map or edit precisely</small></header>
      {points.length === 0 ? (
        <p className="coordinate-empty">Click the map to begin drawing.</p>
      ) : (
        <ol>
          {points.map((point, index) => (
            <li key={point.id}>
              <span className="coordinate-index">{String(index + 1).padStart(2, "0")}</span>
              <label>Latitude<input type="number" step="0.000001" value={point.latitude} onChange={(event) => onUpdate(point.id, { latitude: Number(event.target.value) })} /></label>
              <label>Longitude<input type="number" step="0.000001" value={point.longitude} onChange={(event) => onUpdate(point.id, { longitude: Number(event.target.value) })} /></label>
              {templateType === "WAYPOINT" && <>
                <label>{settings.altitudeMode === "TERRAIN_CLEARANCE" ? "Clearance" : "Altitude"}<input type="number" min={2} max={120} placeholder={String(settings.defaultAltitudeMeters)} value={point.altitudeMeters ?? ""} onChange={(event) => onUpdate(point.id, { altitudeMeters: optionalNumber(event.target.value) })} /></label>
                <label>Hold<input type="number" min={0} placeholder="0" value={point.holdSeconds ?? ""} onChange={(event) => onUpdate(point.id, { holdSeconds: optionalNumber(event.target.value) })} /></label>
              </>}
              <div className="coordinate-actions">
                <button type="button" aria-label={`Move point ${index + 1} earlier`} disabled={index === 0} onClick={() => onMove(point.id, -1)}>↑</button>
                <button type="button" aria-label={`Move point ${index + 1} later`} disabled={index === points.length - 1} onClick={() => onMove(point.id, 1)}>↓</button>
                <button type="button" aria-label={`Remove point ${index + 1}`} onClick={() => onRemove(point.id)}>Remove</button>
              </div>
              {templateType === "WAYPOINT" && (
                <WaypointViewOverride point={point} onUpdate={(patch) => onUpdate(point.id, patch)} />
              )}
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

function WaypointViewOverride({ point, onUpdate }: {
  point: MissionPoint;
  onUpdate: (patch: Partial<MissionPoint>) => void;
}) {
  const mode = point.viewModeOverride;
  return (
    <details className="waypoint-view-override">
      <summary>{mode ? `View override · ${mode.replace(/_/g, " ").toLowerCase()}` : "Add camera/gimbal action at waypoint"}</summary>
      <div className="waypoint-view-fields">
        <label>View mode
          <select value={mode ?? ""} onChange={(event) => {
            const next = event.target.value as CameraMode | "";
            if (!next) {
              onUpdate({ viewModeOverride: undefined, gimbalPitchDegrees: undefined, gimbalYawDegrees: undefined, gimbalTargetLatitude: undefined, gimbalTargetLongitude: undefined });
              return;
            }
            onUpdate({ viewModeOverride: next, ...waypointViewPreset(next) });
          }}>
            <option value="">Inherit mission behaviour</option>
            <option value="FORWARD_OBLIQUE">Forward oblique</option>
            <option value="DOWNWARD_SCAN">Downward scan</option>
            <option value="DOWNWARD_OBLIQUE_SCAN">Downward oblique</option>
            <option value="LOOK_AT_POINT">Look at point</option>
            <option value="FIXED_ANGLE">Fixed angle</option>
          </select>
        </label>
        {mode && <OptionalNumberField label="Pitch degrees" value={point.gimbalPitchDegrees} min={-90} max={30} onChange={(value) => onUpdate({ gimbalPitchDegrees: value })} />}
        {mode === "FIXED_ANGLE" && <OptionalNumberField label="Yaw degrees" value={point.gimbalYawDegrees} min={-180} max={180} onChange={(value) => onUpdate({ gimbalYawDegrees: value })} />}
        {mode === "LOOK_AT_POINT" && <>
          <OptionalNumberField label="Target latitude" value={point.gimbalTargetLatitude} min={-90} max={90} step={0.000001} onChange={(value) => onUpdate({ gimbalTargetLatitude: value })} />
          <OptionalNumberField label="Target longitude" value={point.gimbalTargetLongitude} min={-180} max={180} step={0.000001} onChange={(value) => onUpdate({ gimbalTargetLongitude: value })} />
        </>}
      </div>
    </details>
  );
}

function PlanConsole({ plan }: { plan?: MissionPlan }) {
  if (!plan) {
    return <section className="plan-console plan-console--empty"><div><span>04</span><strong>Generated plan</strong></div><p>The planned path will overlay in orange after native validation succeeds.</p></section>;
  }
  return (
    <section className="plan-console" aria-labelledby="plan-console-title">
      <header><div><span>04</span><strong id="plan-console-title">Generated plan</strong></div><span className="plan-status">{plan.status}</span></header>
      <div className="plan-metrics">
        <div><span>Waypoints</span><strong>{plan.generatedWaypoints.length}</strong></div>
        <div><span>Distance</span><strong>{formatDistance(plan.metadata.estimatedDistanceMeters)}</strong></div>
        <div><span>Pattern</span><strong>{plan.patternType.replace(/_/g, " ")}</strong></div>
        <div><span>Actions</span><strong>{plan.actions.length}</strong></div>
      </div>
      {plan.metadata.altitudeMode === "TERRAIN_CLEARANCE" && plan.metadata.terrainProfile && (
        <>
          <div className="terrain-plan-summary">
            <div><span>Terrain evidence</span><strong>{plan.metadata.terrainProfile.displayName ?? plan.metadata.terrainProfile.datasetId}</strong></div>
            <div><span>Profile</span><strong>{plan.metadata.terrainProfile.stationCount ?? 0} stations · {formatAltitudeRange(plan.metadata.terrainProfile.minimumRelativeAltitudeMeters, plan.metadata.terrainProfile.maximumRelativeAltitudeMeters)}</strong></div>
            <small>Altitude is relative to home; the DEM profile maintains the requested clearance and margin across the sampled corridor.</small>
          </div>
          <TerrainAltitudeProfile points={plan.metadata.terrainProfile.profilePoints ?? []} />
        </>
      )}
      {plan.validationWarnings.map((warning) => <p className="plan-warning" key={warning}>{warning}</p>)}
      <details><summary>Inspect action sequence</summary><ol>{plan.actions.map((action) => <li key={action.sequence}><span>{String(action.sequence + 1).padStart(2, "0")}</span><strong>{action.actionType.replace(/_/g, " ")}</strong></li>)}</ol></details>
    </section>
  );
}

function TerrainAltitudeProfile({ points }: {
  points: Array<{ sequence: number; groundRelativeAltitudeMeters: number; plannedRelativeAltitudeMeters: number }>;
}) {
  if (points.length < 2) return null;
  const chartPoints = decimateProfile(points, 300);
  const values = chartPoints.flatMap((point) => [point.groundRelativeAltitudeMeters, point.plannedRelativeAltitudeMeters]);
  const minimum = Math.min(0, ...values);
  const maximum = Math.max(...values);
  const range = Math.max(1, maximum - minimum);
  const x = (index: number) => 12 + index / (chartPoints.length - 1) * 576;
  const y = (value: number) => 128 - (value - minimum) / range * 110;
  const terrainLine = chartPoints.map((point, index) => `${x(index).toFixed(1)},${y(point.groundRelativeAltitudeMeters).toFixed(1)}`).join(" ");
  const flightLine = chartPoints.map((point, index) => `${x(index).toFixed(1)},${y(point.plannedRelativeAltitudeMeters).toFixed(1)}`).join(" ");
  return (
    <figure className="terrain-altitude-profile">
      <figcaption><strong>Terrain-aware altitude profile</strong><span>Relative to aircraft home</span></figcaption>
      <svg viewBox="0 0 600 146" role="img" aria-label={`Terrain and planned flight altitude from ${minimum.toFixed(0)} to ${maximum.toFixed(0)} metres relative to home`}>
        <line x1="12" x2="588" y1={y(0)} y2={y(0)} className="terrain-altitude-profile__home" />
        <polyline points={terrainLine} className="terrain-altitude-profile__terrain" />
        <polyline points={flightLine} className="terrain-altitude-profile__flight" />
      </svg>
      <div><span><i className="terrain-key terrain-key--ground" />Terrain</span><span><i className="terrain-key terrain-key--flight" />Planned aircraft</span><strong>{minimum.toFixed(0)}–{maximum.toFixed(0)} m</strong></div>
    </figure>
  );
}

function decimateProfile<T>(values: T[], maximum: number) {
  if (values.length <= maximum) return values;
  const result: T[] = [];
  for (let index = 0; index < maximum; index += 1) {
    result.push(values[Math.round(index / (maximum - 1) * (values.length - 1))]);
  }
  return result;
}

function missionParams(templateType: MissionTemplateType, points: MissionPoint[], settings: MissionSettings) {
  const altitudeProfile = {
    altitudeMode: settings.altitudeMode,
    terrain: settings.altitudeMode === "TERRAIN_CLEARANCE" ? {
      safetyMarginMeters: settings.terrainSafetyMarginMeters,
      sampleSpacingMeters: settings.terrainSampleSpacingMeters,
      corridorWidthMeters: settings.terrainCorridorWidthMeters,
      maxClimbRateMps: settings.terrainMaxClimbRateMps,
      maxDescentRateMps: settings.terrainMaxDescentRateMps,
      maxRelativeAltitudeMeters: settings.terrainMaxRelativeAltitudeMeters,
    } : undefined,
  };
  const common = {
    ...altitudeProfile,
    speedMps: settings.speedMps,
    cameraMode: settings.cameraMode,
    gimbal: missionGimbalParams(settings),
    zoomPercent: settings.zoomPercent,
    detectionClasses: detectionClasses(settings.detectionClasses),
    returnToLaunch: settings.returnToLaunch,
    recordVideo: settings.recordVideo,
  };
  if (templateType === "WAYPOINT") {
    return {
      ...altitudeProfile,
      defaultAltitudeMeters: settings.defaultAltitudeMeters,
      defaultSpeedMps: settings.defaultSpeedMps,
      takeoffAltitudeMeters: settings.takeoffAltitudeMeters,
      cameraMode: settings.cameraMode,
      gimbal: missionGimbalParams(settings),
      zoomPercent: settings.zoomPercent,
      detectionClasses: detectionClasses(settings.detectionClasses),
      returnToLaunch: settings.returnToLaunch,
      recordVideo: settings.recordVideo,
      waypoints: points.map((point) => ({
        latitude: point.latitude,
        longitude: point.longitude,
        altitudeMeters: point.altitudeMeters,
        speedMps: point.speedMps,
        headingDegrees: point.headingDegrees,
        holdSeconds: point.holdSeconds,
        ...waypointViewParams(point),
      })),
    };
  }
  if (templateType === "AREA_SCAN") {
    return {
      ...common,
      areaPolygon: points.map(({ latitude, longitude }) => ({ latitude, longitude })),
      altitudeMeters: settings.altitudeMeters,
      laneSpacingMeters: settings.laneSpacingMeters,
      overlapPercent: settings.overlapPercent,
      sweepAngleDegrees: settings.sweepAngleDegrees,
    };
  }
  return {
    ...common,
    route: points.map(({ latitude, longitude }) => ({ latitude, longitude })),
    altitudeMeters: settings.altitudeMeters,
    sampleSpacingMeters: settings.sampleSpacingMeters,
    corridorWidthMeters: settings.corridorWidthMeters,
  };
}

function missionGimbalParams(settings: MissionSettings) {
  return {
    pitchDegrees: settings.gimbalPitchDegrees,
    yawMode: settings.gimbalYawMode,
    yawDegrees: settings.gimbalYawMode === "FIXED_ANGLE" ? settings.gimbalYawDegrees : undefined,
    stabilization: settings.gimbalStabilization,
    target: settings.gimbalYawMode === "LOOK_AT_POINT" ? {
      latitude: settings.gimbalTargetLatitude,
      longitude: settings.gimbalTargetLongitude,
    } : undefined,
  };
}

function waypointViewParams(point: MissionPoint) {
  const mode = point.viewModeOverride;
  if (!mode) return {};
  const yawMode: GimbalYawMode = mode === "LOOK_AT_POINT"
    ? "LOOK_AT_POINT"
    : mode === "FIXED_ANGLE"
      ? "FIXED_ANGLE"
      : "FOLLOW_DRONE_HEADING";
  return {
    cameraMode: mode,
    gimbal: {
      pitchDegrees: point.gimbalPitchDegrees ?? waypointViewPreset(mode).gimbalPitchDegrees,
      yawMode,
      yawDegrees: yawMode === "FIXED_ANGLE" ? point.gimbalYawDegrees ?? 0 : undefined,
      stabilization: true,
      target: yawMode === "LOOK_AT_POINT" ? {
        latitude: point.gimbalTargetLatitude,
        longitude: point.gimbalTargetLongitude,
      } : undefined,
    },
  };
}

function pointsFromMission(templateType: MissionTemplateType, params: Record<string, unknown>): MissionPoint[] {
  const key = templateType === "WAYPOINT" ? "waypoints" : templateType === "AREA_SCAN" ? "areaPolygon" : "route";
  const values = Array.isArray(params[key]) ? params[key] as Array<Record<string, unknown>> : [];
  return values.flatMap((value) => {
    const latitude = Number(value.latitude);
    const longitude = Number(value.longitude);
    if (!Number.isFinite(latitude) || !Number.isFinite(longitude)) return [];
    const gimbal = recordValue(value.gimbal);
    const target = recordValue(gimbal?.target);
    return [{
      ...newPoint(latitude, longitude),
      altitudeMeters: finiteValue(value.altitudeMeters),
      speedMps: finiteValue(value.speedMps),
      headingDegrees: finiteValue(value.headingDegrees),
      holdSeconds: finiteValue(value.holdSeconds),
      viewModeOverride: cameraModeValue(value.cameraMode),
      gimbalPitchDegrees: finiteValue(gimbal?.pitchDegrees),
      gimbalYawDegrees: finiteValue(gimbal?.yawDegrees),
      gimbalTargetLatitude: finiteValue(target?.latitude),
      gimbalTargetLongitude: finiteValue(target?.longitude),
    }];
  });
}

function settingsFromMission(templateType: MissionTemplateType, params: Record<string, unknown>): MissionSettings {
  const defaults = defaultSettings[templateType];
  const gimbal = recordValue(params.gimbal);
  const target = recordValue(gimbal?.target);
  return {
    ...defaults,
    altitudeMode: params.altitudeMode === "TERRAIN_CLEARANCE" ? "TERRAIN_CLEARANCE" : "HOME_RELATIVE",
    altitudeMeters: finiteValue(params.altitudeMeters) ?? defaults.altitudeMeters,
    defaultAltitudeMeters: finiteValue(params.defaultAltitudeMeters) ?? defaults.defaultAltitudeMeters,
    speedMps: finiteValue(params.speedMps) ?? defaults.speedMps,
    defaultSpeedMps: finiteValue(params.defaultSpeedMps) ?? defaults.defaultSpeedMps,
    takeoffAltitudeMeters: finiteValue(params.takeoffAltitudeMeters) ?? defaults.takeoffAltitudeMeters,
    laneSpacingMeters: finiteValue(params.laneSpacingMeters) ?? defaults.laneSpacingMeters,
    overlapPercent: finiteValue(params.overlapPercent) ?? defaults.overlapPercent,
    sweepAngleDegrees: finiteValue(params.sweepAngleDegrees) ?? defaults.sweepAngleDegrees,
    corridorWidthMeters: finiteValue(params.corridorWidthMeters) ?? defaults.corridorWidthMeters,
    sampleSpacingMeters: finiteValue(params.sampleSpacingMeters) ?? defaults.sampleSpacingMeters,
    terrainSafetyMarginMeters: finiteValue(recordValue(params.terrain)?.safetyMarginMeters) ?? defaults.terrainSafetyMarginMeters,
    terrainSampleSpacingMeters: finiteValue(recordValue(params.terrain)?.sampleSpacingMeters) ?? defaults.terrainSampleSpacingMeters,
    terrainCorridorWidthMeters: finiteValue(recordValue(params.terrain)?.corridorWidthMeters) ?? defaults.terrainCorridorWidthMeters,
    terrainMaxClimbRateMps: finiteValue(recordValue(params.terrain)?.maxClimbRateMps) ?? defaults.terrainMaxClimbRateMps,
    terrainMaxDescentRateMps: finiteValue(recordValue(params.terrain)?.maxDescentRateMps) ?? defaults.terrainMaxDescentRateMps,
    terrainMaxRelativeAltitudeMeters: finiteValue(recordValue(params.terrain)?.maxRelativeAltitudeMeters) ?? defaults.terrainMaxRelativeAltitudeMeters,
    cameraMode: cameraModeValue(params.cameraMode) ?? defaults.cameraMode,
    gimbalPitchDegrees: finiteValue(gimbal?.pitchDegrees) ?? defaults.gimbalPitchDegrees,
    gimbalYawMode: gimbalYawModeValue(gimbal?.yawMode) ?? defaults.gimbalYawMode,
    gimbalYawDegrees: finiteValue(gimbal?.yawDegrees) ?? defaults.gimbalYawDegrees,
    gimbalTargetLatitude: finiteValue(target?.latitude),
    gimbalTargetLongitude: finiteValue(target?.longitude),
    gimbalStabilization: typeof gimbal?.stabilization === "boolean" ? gimbal.stabilization : defaults.gimbalStabilization,
    zoomPercent: finiteValue(params.zoomPercent) ?? defaults.zoomPercent,
    detectionClasses: Array.isArray(params.detectionClasses) ? params.detectionClasses.join(", ") : "",
    returnToLaunch: typeof params.returnToLaunch === "boolean" ? params.returnToLaunch : defaults.returnToLaunch,
    recordVideo: typeof params.recordVideo === "boolean" ? params.recordVideo : defaults.recordVideo,
  };
}

function validateDraft(name: string, templateType: MissionTemplateType, points: MissionPoint[], settings: MissionSettings, home?: { latitude: number; longitude: number }) {
  if (!name.trim()) return "Enter a mission name.";
  const minimum = templateType === "AREA_SCAN" ? 3 : templateType === "ROUTE_SCAN" ? 2 : 1;
  if (points.length < minimum) return `${geometryTitle(templateType)} requires at least ${minimum} ${minimum === 1 ? "point" : "points"}.`;
  if (points.some((point) => point.latitude < -90 || point.latitude > 90 || point.longitude < -180 || point.longitude > 180)) return "All coordinates must be valid latitude and longitude values.";
  const altitude = templateType === "WAYPOINT" ? settings.defaultAltitudeMeters : settings.altitudeMeters;
  const speed = templateType === "WAYPOINT" ? settings.defaultSpeedMps : settings.speedMps;
  if (altitude < 2 || altitude > 120) return "Altitude must be between 2 and 120 metres.";
  if (speed < 0.5 || speed > 15) return "Speed must be between 0.5 and 15 metres per second.";
  if (settings.altitudeMode === "TERRAIN_CLEARANCE") {
    if (!home) return "Terrain-aware planning requires a connected aircraft with a reported home position.";
    if (altitude + settings.terrainSafetyMarginMeters > 120) return "Ground clearance plus terrain safety margin must not exceed 120 metres.";
    if (settings.terrainSampleSpacingMeters < 5 || settings.terrainSampleSpacingMeters > 200) return "Terrain sample spacing must be between 5 and 200 metres.";
    if (settings.terrainCorridorWidthMeters < 0 || settings.terrainCorridorWidthMeters > 5000) return "Terrain corridor width must be between 0 and 5000 metres.";
    if (settings.terrainMaxClimbRateMps < 0.2 || settings.terrainMaxClimbRateMps > 10 || settings.terrainMaxDescentRateMps < 0.2 || settings.terrainMaxDescentRateMps > 10) return "Terrain climb and descent rates must be between 0.2 and 10 metres per second.";
    if (settings.terrainMaxRelativeAltitudeMeters < 20 || settings.terrainMaxRelativeAltitudeMeters > 1000) return "Terrain relative-altitude ceiling must be between 20 and 1000 metres.";
  }
  if (settings.gimbalPitchDegrees < -90 || settings.gimbalPitchDegrees > 30) return "Gimbal pitch must be between -90 and 30 degrees.";
  if (settings.zoomPercent < 0 || settings.zoomPercent > 100) return "Mission zoom must be between 0 and 100 percent.";
  if (settings.gimbalYawMode === "FIXED_ANGLE" && (settings.gimbalYawDegrees < -180 || settings.gimbalYawDegrees > 180)) return "Fixed gimbal yaw must be between -180 and 180 degrees.";
  if (settings.gimbalYawMode === "LOOK_AT_POINT" && !validTarget(settings.gimbalTargetLatitude, settings.gimbalTargetLongitude)) return "Mission LOOK_AT_POINT requires a valid target latitude and longitude.";
  for (const [index, point] of points.entries()) {
    if (point.viewModeOverride === "LOOK_AT_POINT" && !validTarget(point.gimbalTargetLatitude, point.gimbalTargetLongitude)) return `Waypoint ${index + 1} LOOK_AT_POINT requires a valid target latitude and longitude.`;
    if (point.gimbalPitchDegrees !== undefined && (point.gimbalPitchDegrees < -90 || point.gimbalPitchDegrees > 30)) return `Waypoint ${index + 1} gimbal pitch must be between -90 and 30 degrees.`;
  }
  return undefined;
}

function viewPreset(templateType: MissionTemplateType, mode: CameraMode): Partial<MissionSettings> {
  if (mode === "DOWNWARD_SCAN") return { gimbalPitchDegrees: -90, gimbalYawMode: templateType === "AREA_SCAN" ? "FOLLOW_LANE_DIRECTION" : templateType === "ROUTE_SCAN" ? "FOLLOW_ROUTE_BEARING" : "FOLLOW_DRONE_HEADING" };
  if (mode === "DOWNWARD_OBLIQUE_SCAN") return { gimbalPitchDegrees: -65, gimbalYawMode: templateType === "AREA_SCAN" ? "FOLLOW_LANE_DIRECTION" : templateType === "ROUTE_SCAN" ? "FOLLOW_ROUTE_BEARING" : "FOLLOW_DRONE_HEADING" };
  if (mode === "LOOK_AT_POINT") return { gimbalPitchDegrees: -45, gimbalYawMode: "LOOK_AT_POINT" };
  if (mode === "FIXED_ANGLE") return { gimbalPitchDegrees: -35, gimbalYawMode: "FIXED_ANGLE", gimbalYawDegrees: 0 };
  return { gimbalPitchDegrees: templateType === "ROUTE_SCAN" ? -40 : -35, gimbalYawMode: templateType === "ROUTE_SCAN" ? "FOLLOW_ROUTE_BEARING" : "FOLLOW_DRONE_HEADING" };
}

function waypointViewPreset(mode: CameraMode): Partial<MissionPoint> {
  if (mode === "DOWNWARD_SCAN") return { gimbalPitchDegrees: -90 };
  if (mode === "DOWNWARD_OBLIQUE_SCAN") return { gimbalPitchDegrees: -65 };
  if (mode === "LOOK_AT_POINT") return { gimbalPitchDegrees: -45 };
  if (mode === "FIXED_ANGLE") return { gimbalPitchDegrees: -35, gimbalYawDegrees: 0 };
  return { gimbalPitchDegrees: -35 };
}

function validTarget(latitude?: number, longitude?: number) {
  return latitude !== undefined && longitude !== undefined
    && latitude >= -90 && latitude <= 90
    && longitude >= -180 && longitude <= 180;
}

function geometryTitle(templateType: MissionTemplateType) {
  if (templateType === "AREA_SCAN") return "Coverage polygon";
  if (templateType === "ROUTE_SCAN") return "Route centreline";
  return "Waypoint path";
}

function vertexLabel(templateType: MissionTemplateType) {
  return templateType === "AREA_SCAN" ? "Polygon vertex" : templateType === "ROUTE_SCAN" ? "Route point" : "Waypoint";
}

function detectionClasses(value: string) {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function optionalNumber(value: string) {
  if (value.trim() === "") return undefined;
  const number = Number(value);
  return Number.isFinite(number) ? number : undefined;
}

function finiteValue(value: unknown) {
  const number = Number(value);
  return Number.isFinite(number) ? number : undefined;
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function cameraModeValue(value: unknown): CameraMode | undefined {
  return typeof value === "string" && ["FORWARD_OBLIQUE", "DOWNWARD_SCAN", "DOWNWARD_OBLIQUE_SCAN", "LOOK_AT_POINT", "FIXED_ANGLE"].includes(value)
    ? value as CameraMode
    : undefined;
}

function gimbalYawModeValue(value: unknown): GimbalYawMode | undefined {
  return typeof value === "string" && ["FOLLOW_DRONE_HEADING", "FOLLOW_LANE_DIRECTION", "FOLLOW_ROUTE_BEARING", "LOCKED_TO_ROUTE", "LOOK_AT_POINT", "FIXED_ANGLE"].includes(value)
    ? value as GimbalYawMode
    : undefined;
}

function formatDistance(value?: number) {
  if (value === undefined) return "—";
  return value >= 1000 ? `${(value / 1000).toFixed(1)} km` : `${value.toFixed(0)} m`;
}

function formatAltitudeRange(minimum?: number, maximum?: number) {
  if (minimum === undefined || maximum === undefined) return "—";
  return `${minimum.toFixed(0)}–${maximum.toFixed(0)} m`;
}
