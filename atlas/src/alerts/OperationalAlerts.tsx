import "./OperationalAlerts.css";

export type OperationalAlertState = "ACTIVE" | "ACKNOWLEDGED" | "RESOLVED" | "EXPIRED";
export type OperationalAlertSeverity = "INFO" | "WARNING" | "CRITICAL";

export type OperationalAlert = {
  id: string;
  dedupeKey: string;
  alertType: string;
  severity: OperationalAlertSeverity;
  source: string;
  state: OperationalAlertState;
  droneId?: string | null;
  incidentId?: string | null;
  missionRunId?: string | null;
  title: string;
  recommendedAction: string;
  evidence: Record<string, unknown>;
  firstSeenAtUnixMs: number;
  lastSeenAtUnixMs: number;
  observationCount: number;
  acknowledgedAtUnixMs?: number | null;
  acknowledgedBy?: string | null;
  resolvedAtUnixMs?: number | null;
  resolutionReason?: string | null;
  expiredAtUnixMs?: number | null;
};

export type OperationalAlertList = {
  generatedAtUnixMs: number;
  activeCount: number;
  unacknowledgedCount: number;
  alerts: OperationalAlert[];
};

export const emptyOperationalAlerts: OperationalAlertList = {
  generatedAtUnixMs: 0,
  activeCount: 0,
  unacknowledgedCount: 0,
  alerts: [],
};

export function highestRelatedOperationalAlert(
  alerts: OperationalAlert[],
  associations: { incidentId?: string; droneId?: string; missionRunId?: string },
) {
  const severity = { INFO: 1, WARNING: 2, CRITICAL: 3 } as const;
  return alerts
    .filter((alert) => alert.state === "ACTIVE" || alert.state === "ACKNOWLEDGED")
    .filter((alert) => {
      const associated = Boolean(alert.incidentId || alert.droneId || alert.missionRunId);
      if (!associated) return true;
      return (associations.incidentId && alert.incidentId === associations.incidentId)
        || (associations.droneId && alert.droneId === associations.droneId)
        || (associations.missionRunId && alert.missionRunId === associations.missionRunId);
    })
    .sort((left, right) => severity[right.severity] - severity[left.severity]
      || Number(right.state === "ACTIVE") - Number(left.state === "ACTIVE")
      || right.lastSeenAtUnixMs - left.lastSeenAtUnixMs)[0];
}

export function OperationalAlertButton({
  alerts,
  expanded,
  onClick,
}: {
  alerts: OperationalAlertList;
  expanded: boolean;
  onClick: () => void;
}) {
  const tone = alerts.unacknowledgedCount > 0 ? "attention" : alerts.activeCount > 0 ? "acknowledged" : "clear";
  return (
    <button
      type="button"
      className={`operational-alert-button operational-alert-button--${tone}`}
      aria-expanded={expanded}
      aria-controls="operational-alert-center"
      onClick={onClick}
    >
      <span aria-hidden="true">{alerts.activeCount > 0 ? "!" : "✓"}</span>
      <span>
        <small>Alerts</small>
        <strong>{alerts.activeCount > 0 ? `${alerts.activeCount} active` : "Clear"}</strong>
      </span>
    </button>
  );
}

export function OperationalAlertCenter({
  alerts,
  pendingAlertId,
  error,
  onAcknowledge,
  onClose,
}: {
  alerts: OperationalAlertList;
  pendingAlertId?: string;
  error?: string;
  onAcknowledge: (alertId: string) => void;
  onClose: () => void;
}) {
  const current = alerts.alerts.filter((alert) => alert.state === "ACTIVE" || alert.state === "ACKNOWLEDGED");
  const history = alerts.alerts.filter((alert) => alert.state === "RESOLVED" || alert.state === "EXPIRED").slice(0, 12);
  return (
    <aside id="operational-alert-center" className="operational-alert-center" aria-labelledby="operational-alert-title">
      <header>
        <div>
          <p>Operator attention</p>
          <h2 id="operational-alert-title">Operational alerts</h2>
        </div>
        <button type="button" onClick={onClose} aria-label="Close operational alerts">Close</button>
      </header>
      <p className="operational-alert-center__meaning">
        Acknowledgement records that the operator has seen an alert. Only recovery of the underlying condition resolves it.
      </p>
      {error && <p className="operational-alert-center__error" role="alert">{error}</p>}

      <section aria-labelledby="active-alerts-title">
        <div className="operational-alert-center__section-title">
          <h3 id="active-alerts-title">Current conditions</h3>
          <span>{current.length}</span>
        </div>
        {current.length > 0 ? (
          <ol className="operational-alert-list">
            {current.map((alert) => (
              <li key={alert.id} className={`operational-alert operational-alert--${alert.severity.toLowerCase()}`}>
                <div className="operational-alert__heading">
                  <span>{alert.severity}</span>
                  <time dateTime={new Date(alert.lastSeenAtUnixMs).toISOString()}>{formatTime(alert.lastSeenAtUnixMs)}</time>
                </div>
                <strong>{alert.title}</strong>
                <p>{alert.recommendedAction}</p>
                <dl>
                  <div><dt>Source</dt><dd>{displayEnum(alert.source)}</dd></div>
                  {alert.droneId && <div><dt>Aircraft</dt><dd>{shortId(alert.droneId)}</dd></div>}
                  {alert.incidentId && <div><dt>Incident</dt><dd>{shortId(alert.incidentId)}</dd></div>}
                  {alert.missionRunId && <div><dt>Run</dt><dd>{shortId(alert.missionRunId)}</dd></div>}
                  <div><dt>Observed</dt><dd>{alert.observationCount}×</dd></div>
                </dl>
                {alert.state === "ACTIVE" ? (
                  <button type="button" disabled={pendingAlertId === alert.id} onClick={() => onAcknowledge(alert.id)}>
                    {pendingAlertId === alert.id ? "Recording…" : "Acknowledge as seen"}
                  </button>
                ) : (
                  <p className="operational-alert__acknowledged">Seen · condition remains active</p>
                )}
              </li>
            ))}
          </ol>
        ) : (
          <p className="operational-alert-center__empty">No active operational conditions.</p>
        )}
      </section>

      {history.length > 0 && (
        <section aria-labelledby="alert-history-title">
          <div className="operational-alert-center__section-title">
            <h3 id="alert-history-title">Recent history</h3>
            <span>{history.length}</span>
          </div>
          <ol className="operational-alert-history">
            {history.map((alert) => (
              <li key={alert.id}>
                <span>{alert.state}</span>
                <div><strong>{alert.title}</strong><small>{alert.resolutionReason || "Retained operational evidence"}</small></div>
                <time dateTime={new Date(alert.resolvedAtUnixMs || alert.lastSeenAtUnixMs).toISOString()}>{formatTime(alert.resolvedAtUnixMs || alert.lastSeenAtUnixMs)}</time>
              </li>
            ))}
          </ol>
        </section>
      )}
    </aside>
  );
}

function formatTime(value: number) {
  return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(value);
}

function displayEnum(value: string) {
  return value.toLowerCase().replace(/_/g, " ").replace(/\b\w/g, (letter: string) => letter.toUpperCase());
}

function shortId(value: string) {
  return value.slice(0, 8).toUpperCase();
}
