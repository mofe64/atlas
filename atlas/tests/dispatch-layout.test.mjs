import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const stylesheet = await readFile(
  new URL("../src/operations/OperationsPage.css", import.meta.url),
  "utf8",
);
const operationsPage = await readFile(
  new URL("../src/operations/OperationsPage.tsx", import.meta.url),
  "utf8",
);
const operationsMap = await readFile(
  new URL("../src/operations/OperationsMap.tsx", import.meta.url),
  "utf8",
);
const operationsMapStyles = await readFile(
  new URL("../src/operations/OperationsMap.css", import.meta.url),
  "utf8",
);
const liveVideoStyles = await readFile(
  new URL("../src/video/LiveVideo.css", import.meta.url),
  "utf8",
);
const liveVideo = await readFile(
  new URL("../src/video/LiveVideo.tsx", import.meta.url),
  "utf8",
);
const app = await readFile(
  new URL("../src/App.tsx", import.meta.url),
  "utf8",
);
const missionExecutionStyles = await readFile(
  new URL("../src/missions/MissionExecutionPage.css", import.meta.url),
  "utf8",
);
const missionExecutionPage = await readFile(
  new URL("../src/missions/MissionExecutionPage.tsx", import.meta.url),
  "utf8",
);
const missionPage = await readFile(
  new URL("../src/missions/MissionPage.tsx", import.meta.url),
  "utf8",
);
const missionHistoryPage = await readFile(
  new URL("../src/missions/MissionHistoryPage.tsx", import.meta.url),
  "utf8",
);
const operationalMissionMap = await readFile(
  new URL("../src/missions/OperationalMissionMap.tsx", import.meta.url),
  "utf8",
);

test("Dispatch keeps the board viewport-constrained", () => {
  assert.doesNotMatch(stylesheet, /min-height:\s*max\(42rem/);
  assert.match(
    stylesheet,
    /\.operations-board\s*\{[^}]*flex:\s*1 1 auto;[^}]*min-height:\s*0;[^}]*overflow:\s*hidden;/s,
  );
});

test("Dispatch declares independent rail and video scroll owners", () => {
  assert.match(stylesheet, /\.incident-queue\s*\{[^}]*overflow-y:\s*auto;/s);
  assert.match(stylesheet, /\.incident-detail\s*\{[^}]*overflow-y:\s*auto;/s);
  assert.match(stylesheet, /\.response-live-panel--video\s*\{[^}]*overflow-y:\s*auto;/s);
});

test("Response planning starts at the incident location and accepts map precision", () => {
  assert.match(
    operationsPage,
    /geometryPoints:\s*\[\{\s*latitude:\s*selectedIncident\.latitude,\s*longitude:\s*selectedIncident\.longitude,/s,
  );
  assert.doesNotMatch(operationsPage, /step="0\.000001"/);
  assert.match(operationsPage, /min="-90"\s+max="90"\s+step="any"/);
  assert.match(operationsPage, /min="-180"\s+max="180"\s+step="any"/);
});

test("Building evidence is progressive disclosure and operator copy avoids implementation jargon", () => {
  assert.match(operationsPage, /<details[\s\S]*className=\{`known-building-assessment/);
  assert.match(operationsPage, /open=\{assessment\.status === "INTERSECTIONS"\}/);
  assert.doesNotMatch(operationsPage, /\b(?:immutable|atomically)\b/i);
});

test("Dispatch maintains left-rail insets and resets detail scroll between workflows", () => {
  assert.match(stylesheet, /--incident-rail-gutter:\s*clamp\(/);
  assert.match(stylesheet, /\.incident-detail__empty\s*\{[^}]*padding-inline:\s*clamp\(/s);
  assert.doesNotMatch(stylesheet, /\.operations-metric:first-child\s*\{[^}]*padding-left:\s*0/);
  assert.match(operationsPage, /incidentDetailRef\.current\.scrollTop\s*=\s*0/);
});

test("The shared operations map owns and imports its styling", () => {
  assert.match(operationsMap, /import "\.\/OperationsMap\.css";/);
  assert.match(operationsMapStyles, /\.operations-map\s*\{[^}]*position:\s*relative;[^}]*min-height:\s*36rem;/s);
  assert.match(operationsMapStyles, /\.operations-map__canvas\s*\{[^}]*position:\s*absolute;[^}]*inset:\s*0;/s);
});

test("Dispatch retains its latest observation source without granting ended-run authority", () => {
  assert.match(operationsPage, /const observationAssignment = liveAssignment \?\? incidentAssignments\?\.\[0\];/);
  assert.match(operationsPage, /const workspaceDroneId = creating \? undefined : observationDroneId;/);
  assert.match(operationsPage, /droneId=\{workspaceDroneId\}/);
  assert.match(operationsPage, /const canIssueSafetyCommand = Boolean\([\s\S]*&& liveDroneId[\s\S]*&& liveRun/);
});

test("Video controls respond to their panel and altitude accepts telemetry precision", () => {
  assert.match(liveVideoStyles, /container-type:\s*inline-size/);
  assert.match(liveVideoStyles, /@container \(max-width:\s*54rem\)/);
  assert.match(liveVideoStyles, /@container \(max-width:\s*42rem\)/);
  assert.match(
    operationsPage,
    /Incident target altitude · m AMSL\s*<input[^>]*step="any"/,
  );
});

test("Terminal incidents do not retain the live response banner", () => {
  assert.match(
    operationsPage,
    /const showLiveResponseContext = Boolean\(\s*selectedIncident && \(selectedIncident\.status === "OPEN" \|\| selectedIncident\.status === "ACTIVE"\),\s*\);/s,
  );
  assert.match(operationsPage, /\{showLiveResponseContext && selectedIncident && \(/);
});

test("Completed response assignments retain a route back to mission history", () => {
  assert.doesNotMatch(operationsPage, /assignment\.missionId && !assignment\.endedAtUnixMs/);
  assert.match(operationsPage, /assignment\.endedAtUnixMs\s*\?\s*"View mission"/);
});

test("Route completion and RTL retain mission authority until aircraft recovery", () => {
  assert.match(app, /\["RUNNING", "PAUSED", "ROUTE_COMPLETE", "RTL"\]\.includes\(run\.status\)/);
  assert.doesNotMatch(app, /const heldMissionRuns = operationalMissionRuns/);
  assert.match(app, /primaryMission\?\.status === "ROUTE_COMPLETE"/);
  assert.match(app, /Route complete · Land or RTL required/);
  assert.match(app, /:\s*"Open mission"\}/);
});

test("Mission execution headers use the shared responsive inset", () => {
  assert.match(missionExecutionStyles, /--execution-inline-gutter:\s*clamp\(/);
  assert.match(
    missionExecutionStyles,
    /\.execution-header\s*\{[^}]*padding-right:\s*var\(--execution-inline-gutter\);[^}]*padding-left:\s*var\(--execution-inline-gutter\);/s,
  );
});

test("A new response draft does not inherit the previous assignment route or trail", () => {
  assert.match(
    operationsPage,
    /const workspacePlan = creating\s*\?\s*undefined\s*:\s*responding\s*\?\s*preparedResponse\?\.plan \?\? responsePreview\s*:\s*livePlan;/s,
  );
  assert.match(operationsPage, /const workspaceTrail = creating \|\| responding \? \[\] : aircraftTrail;/);
  assert.match(operationsPage, /aircraftTrail=\{workspaceTrail\}/);
  assert.match(operationsPage, /Map · \{workspaceTrail\.length\} trail points/);
});

test("Dispatch opens unselected and creation clears historical response context", () => {
  assert.doesNotMatch(operationsPage, /setSelectedIncidentId\(nextIncidents\[0\]\.id\)/);
  assert.match(
    operationsPage,
    /function beginCreate\(\) \{\s*setSelectedIncidentId\(undefined\);\s*setDetail\(undefined\);/s,
  );
  assert.match(operationsPage, /setLivePlan\(undefined\);[\s\S]*setAircraftTrail\(\[\]\);/);
});

test("Planning uses live position once without turning the authoring map into follow mode", () => {
  assert.match(operationalMissionMap, /const planningFocusAppliedRef = useRef\(false\);/);
  assert.match(operationalMissionMap, /if \(planningFocusAppliedRef\.current\) return;/);
  assert.doesNotMatch(operationalMissionMap, /focusKey\(planningFocus\)/);
  assert.match(missionPage, /distance checks keep updating without moving the planning map/);
});

test("Mission execution owns a bounded scrollable run-control rail", () => {
  assert.match(missionExecutionStyles, /\.execution-live-grid\s*\{[^}]*height:\s*clamp\([^}]*overflow:\s*hidden;/s);
  assert.match(missionExecutionStyles, /\.execution-command-column\s*\{[^}]*min-height:\s*0;[^}]*overflow-y:\s*auto;/s);
});

test("Mission execution defaults to a balanced view and omits design commentary", () => {
  assert.match(missionExecutionPage, /atlas\.execution\.responseLayout\.v2/);
  assert.match(missionExecutionPage, /\?\s*stored\s*:\s*"split"/);
  assert.doesNotMatch(missionExecutionPage, /Safety actions remain available in every response layout/);
});

test("Mission camera evidence actions remain available in compact layouts", () => {
  assert.match(liveVideo, /\{compact && \(\s*<div className=\{`live-video__compact-evidence/s);
  assert.match(liveVideo, /Capture still/);
  assert.match(liveVideo, /Start recording/);
  assert.match(liveVideoStyles, /\.live-video__compact-evidence-actions/);
});

test("Live video discloses specialist controls by operator task", () => {
  assert.match(liveVideo, /type VideoTool = "targets" \| "counting" \| "diagnostics";/);
  assert.match(liveVideo, /aria-label="Camera tools"/);
  assert.match(liveVideo, />Targets<\/button>/);
  assert.match(liveVideo, />Counting tools<\/button>/);
  assert.match(liveVideo, />Diagnostics<\/button>/);
  assert.match(liveVideo, /activeTool === "counting" && \(\s*<div className="track-operations__counts">/s);
  assert.match(liveVideo, /activeTool === "targets" && \(\s*<div className=\{`track-selection/s);
  assert.match(liveVideo, /activeTool === "diagnostics" && \(\s*<section className="live-video__diagnostics"/s);
  assert.match(liveVideoStyles, /\.live-video__tool-switch/);
  assert.match(liveVideoStyles, /\.live-video__diagnostics/);
});

test("Evidence actions stay persistent while idle evidence facts collapse", () => {
  assert.match(liveVideo, /const showRecordingDetails = recordingActive[\s\S]*recording\?\.diskState === "STOP";/);
  assert.match(liveVideo, /showRecordingDetails && <div className="evidence-recorder__facts">/);
  assert.match(liveVideo, /showRecordingDetails \? "" : " evidence-recorder--summary"/);
  assert.match(liveVideoStyles, /\.evidence-recorder--summary/);
});

test("Dispatch is map-first until an aircraft establishes video context", () => {
  assert.match(operationsPage, /const videoAvailable = Boolean\(workspaceDroneId\);/);
  assert.match(operationsPage, /if \(!videoAvailable && layout !== "map"\) setLayout\("map"\);/);
  assert.match(operationsPage, /disabled=\{option !== "map" && !videoAvailable\}/);
  assert.match(operationsPage, /Assign an aircraft to open response video\./);
  assert.match(operationsPage, /\{videoAvailable \? \(\s*<LiveVideo/s);
  assert.match(operationsPage, /Video opens after aircraft assignment/);
});

test("Dispatch map layers use a compact disclosure menu", () => {
  assert.match(operationsPage, /<details className="response-layer-menu"/);
  assert.match(operationsPage, /<summary>Layers <strong>\{enabledLayerCount\}<\/strong><\/summary>/);
  assert.match(operationsPage, /<fieldset className="response-layer-controls">/);
  assert.match(stylesheet, /\.response-layer-menu summary/);
  assert.match(stylesheet, /\.response-layer-controls\s*\{[^}]*position:\s*absolute;/s);
});

test("Plan has one reset path and progressively discloses specialist settings", () => {
  assert.equal((missionPage.match(/>New mission<\/button>/g) ?? []).length, 1);
  assert.equal((missionPage.match(/>Mission history<\/button>/g) ?? []).length, 1);
  assert.doesNotMatch(missionPage, /Create new mission|Start new|Active missions/);
  assert.match(missionPage, /<summary>Customize mission <span>Camera, gimbal, detection, and completion<\/span><\/summary>/);
  assert.match(missionPage, /<details className="mission-advanced-settings">[\s\S]*<ViewSettings[\s\S]*Detection and completion[\s\S]*<\/details>/);
  assert.ok(missionPage.indexOf("<TemplateSettings") < missionPage.indexOf('className="mission-identity-fields"'));
  assert.doesNotMatch(missionPage, /Local mission library/);
  assert.match(missionHistoryPage, /invoke<Mission\[\]>\("mission_list"\)/);
  assert.match(missionHistoryPage, /invoke<MissionRun\[\]>\("mission_run_history"/);
  assert.match(missionHistoryPage, /Saved mission plans/);
  assert.match(missionHistoryPage, /Past mission runs/);
  assert.match(missionHistoryPage, /onEditMission\(mission\.id\).*Edit plan/s);
  assert.match(missionPage, /invoke<Mission>\("mission_detail", \{ missionId: initialMissionId \}\)/);
});
