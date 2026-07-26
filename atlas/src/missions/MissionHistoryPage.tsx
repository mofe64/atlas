import { useEffect, useMemo, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import type { Mission, MissionRun } from "./missionTypes";
import "./MissionHistoryPage.css";

type MissionHistoryPageProps = {
  nativeAvailable: boolean;
  onBack: () => void;
  onEditMission: (missionId: string) => void;
  onOpenMission: (missionId: string, droneId?: string) => void;
};

const terminalStates = new Set(["COMPLETED", "FAILED", "CANCELLED", "RTL"]);

export function MissionHistoryPage({ nativeAvailable, onBack, onEditMission, onOpenMission }: MissionHistoryPageProps) {
  const [missions, setMissions] = useState<Mission[]>([]);
  const [runs, setRuns] = useState<MissionRun[]>([]);
  const [loading, setLoading] = useState(nativeAvailable);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (!nativeAvailable) {
      setLoading(false);
      return;
    }

    let active = true;
    void Promise.all([
      invoke<Mission[]>("mission_list"),
      invoke<MissionRun[]>("mission_run_history", { limit: 500 }),
    ]).then(([nextMissions, nextRuns]) => {
      if (!active) return;
      setMissions(nextMissions);
      setRuns(nextRuns);
      setError(undefined);
    }).catch((reason) => {
      if (active) setError(reason instanceof Error ? reason.message : String(reason));
    }).finally(() => {
      if (active) setLoading(false);
    });

    return () => {
      active = false;
    };
  }, [nativeAvailable]);

  const pastRuns = useMemo(
    () => runs
      .filter((run) => terminalStates.has(run.status))
      .sort((left, right) => right.updatedAtUnixMs - left.updatedAtUnixMs),
    [runs],
  );
  const savedMissions = useMemo(
    () => [...missions].sort((left, right) => right.updatedAtUnixMs - left.updatedAtUnixMs),
    [missions],
  );

  return (
    <main className="mission-history-workspace" id="main-content">
      <header className="mission-history-heading">
        <div>
          <button type="button" onClick={onBack}>← Plan</button>
          <p className="eyebrow">Mission records</p>
          <h1>Mission history</h1>
          <p>Review saved flight paths and completed, cancelled, or failed executions.</p>
        </div>
        <span>{savedMissions.length} plans · {pastRuns.length} past runs</span>
      </header>

      {error && <p className="mission-history-error" role="alert">{error}</p>}

      <div className="mission-history-sections">
        <section className="mission-history-section" aria-labelledby="saved-plans-title">
          <header>
            <div>
              <p className="eyebrow">Reusable definitions</p>
              <h2 id="saved-plans-title">Saved mission plans</h2>
            </div>
            <span>{savedMissions.length}</span>
          </header>
          {loading ? (
            <p className="mission-history-empty">Loading saved plans…</p>
          ) : savedMissions.length === 0 ? (
            <p className="mission-history-empty">No mission plans have been saved on this ground station.</p>
          ) : (
            <div className="mission-history-list" role="list">
              {savedMissions.map((mission) => (
                <article key={mission.id} className="mission-history-item" role="listitem">
                  <div>
                    <span>{mission.templateType.replace(/_/g, " ")}</span>
                    <strong>{mission.name}</strong>
                    <small>{mission.selectedPattern.replace(/_/g, " ")} · updated {formatDate(mission.updatedAtUnixMs)}</small>
                  </div>
                  <div className="mission-history-item__actions">
                    <button type="button" onClick={() => onEditMission(mission.id)}>Edit plan</button>
                    <button type="button" disabled={!mission.generatedPlanId} onClick={() => onOpenMission(mission.id)}>
                      {mission.generatedPlanId ? "Review plan" : "Draft only"}
                    </button>
                  </div>
                </article>
              ))}
            </div>
          )}
        </section>

        <section className="mission-history-section" aria-labelledby="past-runs-title">
          <header>
            <div>
              <p className="eyebrow">Execution record</p>
              <h2 id="past-runs-title">Past mission runs</h2>
            </div>
            <span>{pastRuns.length}</span>
          </header>
          {loading ? (
            <p className="mission-history-empty">Loading past runs…</p>
          ) : pastRuns.length === 0 ? (
            <p className="mission-history-empty">Completed, cancelled, and failed runs will appear here.</p>
          ) : (
            <div className="mission-history-list" role="list">
              {pastRuns.map((run) => (
                <article key={run.id} className="mission-history-item" role="listitem">
                  <div>
                    <span className={`mission-history-status mission-history-status--${run.status.toLowerCase()}`}>{run.status}</span>
                    <strong>{run.missionName}</strong>
                    <small>{run.droneName} · {Math.round(run.progressPercent)}% · {formatDate(run.updatedAtUnixMs)}</small>
                  </div>
                  <button type="button" onClick={() => onOpenMission(run.missionId, run.droneId)}>Review run</button>
                </article>
              ))}
            </div>
          )}
        </section>
      </div>
    </main>
  );
}

function formatDate(value: number) {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}
