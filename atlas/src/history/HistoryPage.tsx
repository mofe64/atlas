import { useEffect, useMemo, useState } from "react";
import type { CSSProperties } from "react";
import { invoke } from "@tauri-apps/api/core";
import type { Nullable } from "../operationsTypes";
import "./HistoryPage.css";

type HistoryRange = "1h" | "6h" | "24h" | "7d";

type TelemetryChartPoint = {
  receivedAtUnixMs: number;
  batteryPercent?: number | null;
  relativeAltitudeM?: number | null;
  bottomClearanceM?: number | null;
  groundSpeedMps?: number | null;
  climbRateMps?: number | null;
  rcSignalStrengthPercent?: number | null;
  gpsHdop?: number | null;
  flightMode?: string | null;
  armed?: boolean | null;
  inAir?: boolean | null;
};

type TelemetryChartSeries = {
  fromReceivedAtUnixMs: number;
  toReceivedAtUnixMs: number;
  bucketWidthMs: number;
  points: TelemetryChartPoint[];
};

type VehicleEvent = {
  id: string;
  source: string;
  origin: string;
  eventType: string;
  code?: string | null;
  detailsJson?: string | null;
  severity: string;
  message: string;
  observedAtUnixMs: number;
  receivedAtUnixMs: number;
};

type SeriesDefinition = {
  label: string;
  value: (point: TelemetryChartPoint) => Nullable<number>;
  color: string;
  unit: string;
  digits: number;
};

type HistoryPageProps = {
  droneId?: string | null;
  droneName?: string | null;
  nativeAvailable: boolean;
  onOpenDroneHistory: (droneId: string) => void;
  onBackToOverview: () => void;
};

type DroneHistorySummary = {
  droneId: string;
  droneName: string;
  vehicleType: string;
  snapshotCount: number;
  eventCount: number;
  firstSnapshotAtUnixMs?: number | null;
  lastSnapshotAtUnixMs?: number | null;
  lastEventAtUnixMs?: number | null;
  latestFlightMode?: string | null;
  latestBatteryPercent?: number | null;
  latestInAir?: boolean | null;
};

type HistoryOverviewSnapshot = {
  generatedAtUnixMs: number;
  retentionDays: number;
  drones: DroneHistorySummary[];
};

const ranges: Array<{ value: HistoryRange; label: string; durationMs: number }> = [
  { value: "1h", label: "1 hour", durationMs: 60 * 60 * 1_000 },
  { value: "6h", label: "6 hours", durationMs: 6 * 60 * 60 * 1_000 },
  { value: "24h", label: "24 hours", durationMs: 24 * 60 * 60 * 1_000 },
  { value: "7d", label: "7 days", durationMs: 7 * 24 * 60 * 60 * 1_000 },
];

export function HistoryPage({
  droneId,
  droneName,
  nativeAvailable,
  onOpenDroneHistory,
  onBackToOverview,
}: HistoryPageProps) {
  const [range, setRange] = useState<HistoryRange>("6h");
  const [series, setSeries] = useState<TelemetryChartSeries>();
  const [events, setEvents] = useState<VehicleEvent[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>();
  const [refreshKey, setRefreshKey] = useState(0);
  const [updatedAt, setUpdatedAt] = useState<number>();
  const [overview, setOverview] = useState<HistoryOverviewSnapshot>();
  const [overviewLoading, setOverviewLoading] = useState(false);
  const [overviewError, setOverviewError] = useState<string>();
  const [overviewRefreshKey, setOverviewRefreshKey] = useState(0);

  useEffect(() => {
    if (!nativeAvailable || droneId) return;
    let active = true;
    setOverviewLoading(true);
    setOverviewError(undefined);
    invoke<HistoryOverviewSnapshot>("history_overview")
      .then((nextOverview) => {
        if (active) setOverview(nextOverview);
      })
      .catch((reason: unknown) => {
        if (!active) return;
        setOverviewError(
          typeof reason === "string"
            ? reason
            : "Atlas could not read the local history overview.",
        );
      })
      .finally(() => {
        if (active) setOverviewLoading(false);
      });
    return () => {
      active = false;
    };
  }, [droneId, nativeAvailable, overviewRefreshKey]);

  useEffect(() => {
    if (!nativeAvailable || !droneId) {
      setSeries(undefined);
      setEvents([]);
      setLoading(false);
      return;
    }
    let active = true;
    const selectedRange = ranges.find((candidate) => candidate.value === range) ?? ranges[1];
    const toReceivedAtUnixMs = Date.now();
    const fromReceivedAtUnixMs = toReceivedAtUnixMs - selectedRange.durationMs;
    setLoading(true);
    setError(undefined);

    Promise.all([
      invoke<TelemetryChartSeries>("vehicle_telemetry_chart_series", {
        droneId,
        fromReceivedAtUnixMs,
        toReceivedAtUnixMs,
        maximumPoints: 720,
      }),
      invoke<VehicleEvent[]>("vehicle_event_history", {
        droneId,
        fromReceivedAtUnixMs,
        toReceivedAtUnixMs,
        limit: 200,
      }),
    ])
      .then(([nextSeries, nextEvents]) => {
        if (!active) return;
        setSeries(nextSeries);
        setEvents(nextEvents);
        setUpdatedAt(Date.now());
      })
      .catch((reason: unknown) => {
        if (!active) return;
        setError(
          typeof reason === "string"
            ? reason
            : "Atlas could not read the local flight history. Reopen the page and try again.",
        );
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
    };
  }, [droneId, nativeAvailable, range, refreshKey]);

  const summary = useMemo(() => summarize(series?.points ?? []), [series]);

  if (!nativeAvailable) {
    return (
      <main className="history-main history-main--empty" id="main-content">
        <p className="eyebrow">Flight record</p>
        <h1>History is unavailable</h1>
        <p>Atlas Native must be running to read telemetry stored on this computer.</p>
      </main>
    );
  }

  if (!droneId) {
    return (
      <HistoryOverview
        overview={overview}
        loading={overviewLoading}
        error={overviewError}
        onRefresh={() => setOverviewRefreshKey((current) => current + 1)}
        onOpenDrone={onOpenDroneHistory}
      />
    );
  }

  return (
    <main className="history-main" id="main-content">
      <button type="button" className="history-back" onClick={onBackToOverview}>
        <span aria-hidden="true">←</span> All drone history
      </button>
      <header className="history-hero">
        <div>
          <p className="eyebrow">Flight record</p>
          <h1>{droneName || "Drone history"}</h1>
          <p className="history-intro">Telemetry trends and drone events retained on this computer.</p>
        </div>
        <div className="history-retention">
          <span>Rolling retention</span>
          <strong>7 days</strong>
          <small>{updatedAt ? `Updated ${formatRelativeTime(updatedAt).toLowerCase()}` : "Local SQLite record"}</small>
        </div>
      </header>

      <section className="history-controls" aria-label="History time range">
        <div className="range-selector">
          {ranges.map((option) => (
            <button
              key={option.value}
              type="button"
              className={range === option.value ? "range-selector__active" : undefined}
              aria-pressed={range === option.value}
              onClick={() => setRange(option.value)}
            >
              {option.label}
            </button>
          ))}
        </div>
        <button
          type="button"
          className="history-refresh"
          disabled={loading}
          onClick={() => setRefreshKey((current) => current + 1)}
        >
          {loading ? "Reading history…" : "Refresh history"}
        </button>
      </section>

      {error && (
        <section className="history-error" role="alert">
          <strong>History could not be loaded</strong>
          <p>{error}</p>
          <button type="button" onClick={() => setRefreshKey((current) => current + 1)}>
            Retry history
          </button>
        </section>
      )}

      {loading && !series ? <HistorySkeleton /> : (
        <>
          <section className="history-summary" aria-label="Selected period summary">
            <HistoryMetric label="Chart points" value={String(series?.points.length ?? 0)} detail={bucketDetail(series)} />
            <HistoryMetric label="Peak altitude" value={formatMetric(summary.maximumAltitude, 1, " m")} detail="Relative to home" />
            <HistoryMetric label="Peak ground speed" value={formatMetric(summary.maximumSpeed, 1, " m/s")} detail="Highest sampled value" />
            <HistoryMetric label="Lowest battery" value={formatMetric(summary.minimumBattery, 0, "%")} detail="Primary battery" />
          </section>

          {(series?.points.length ?? 0) > 0 ? (
            <>
              <section className="history-chart-grid" aria-label="Telemetry charts">
                <LineChart
                  className="chart-panel--primary"
                  title="Altitude profile"
                  description="Relative altitude and fused bottom clearance"
                  points={series?.points ?? []}
                  series={[
                    { label: "Relative altitude", value: (point) => point.relativeAltitudeM, color: "var(--field)", unit: "m", digits: 1 },
                    { label: "Bottom clearance", value: (point) => point.bottomClearanceM, color: "var(--signal)", unit: "m", digits: 1 },
                  ]}
                />
                <LineChart
                  title="Power and control link"
                  description="Primary battery and RC signal strength"
                  points={series?.points ?? []}
                  fixedDomain={[0, 100]}
                  series={[
                    { label: "Battery", value: (point) => point.batteryPercent, color: "var(--field)", unit: "%", digits: 0 },
                    { label: "RC signal", value: (point) => point.rcSignalStrengthPercent, color: "var(--signal)", unit: "%", digits: 0 },
                  ]}
                />
                <LineChart
                  title="Drone motion"
                  description="Ground speed and vertical climb rate"
                  points={series?.points ?? []}
                  series={[
                    { label: "Ground speed", value: (point) => point.groundSpeedMps, color: "var(--field)", unit: "m/s", digits: 1 },
                    { label: "Climb rate", value: (point) => point.climbRateMps, color: "var(--signal)", unit: "m/s", digits: 1 },
                  ]}
                />
                <LineChart
                  title="Navigation precision"
                  description="GPS horizontal dilution; lower is better"
                  points={series?.points ?? []}
                  series={[
                    { label: "GPS HDOP", value: (point) => point.gpsHdop, color: "var(--field)", unit: "", digits: 1 },
                  ]}
                />
              </section>
              <FlightModeTrack points={series?.points ?? []} />
            </>
          ) : (
            <section className="history-no-data">
              <p className="eyebrow">Selected period</p>
              <h2>No telemetry snapshots</h2>
              <p>Choose a wider range or operate the drone to begin building its flight record.</p>
            </section>
          )}

          <EventTimeline events={events} />
        </>
      )}
    </main>
  );
}

function HistoryOverview({
  overview,
  loading,
  error,
  onRefresh,
  onOpenDrone,
}: {
  overview?: HistoryOverviewSnapshot;
  loading: boolean;
  error?: string;
  onRefresh: () => void;
  onOpenDrone: (droneId: string) => void;
}) {
  const drones = overview?.drones ?? [];
  const dronesWithHistory = drones.filter((drone) => drone.snapshotCount > 0).length;
  const snapshotCount = drones.reduce((total, drone) => total + drone.snapshotCount, 0);
  const eventCount = drones.reduce((total, drone) => total + drone.eventCount, 0);

  return (
    <main className="history-main history-overview" id="main-content">
      <header className="history-hero history-overview__hero">
        <div>
          <p className="eyebrow">Local flight records</p>
          <h1>History</h1>
          <p className="history-intro">
            Review retained telemetry and events across the fleet, then open a drone for charts and its flight timeline.
          </p>
        </div>
        <div className="history-retention">
          <span>Rolling retention</span>
          <strong>{overview?.retentionDays ?? 7} days</strong>
          <small>{overview?.generatedAtUnixMs
            ? `Updated ${formatRelativeTime(overview.generatedAtUnixMs).toLowerCase()}`
            : "Reading local SQLite"}</small>
        </div>
      </header>

      <section className="history-summary history-overview__summary" aria-label="History storage summary">
        <HistoryMetric label="Registered drones" value={String(drones.length).padStart(2, "0")} detail={`${dronesWithHistory} with telemetry`} />
        <HistoryMetric label="Telemetry samples" value={formatCount(snapshotCount)} detail="Within retention window" />
        <HistoryMetric label="Drone events" value={formatCount(eventCount)} detail="Status and derived events" />
        <HistoryMetric label="Retention" value={`${overview?.retentionDays ?? 7} days`} detail="Rolling local storage" />
      </section>

      <div className="history-overview__toolbar">
        <div>
          <h2>Drone records</h2>
          <span>{drones.length} registered locally</span>
        </div>
        <button type="button" disabled={loading} onClick={onRefresh}>
          {loading ? "Reading history…" : "Refresh overview"}
        </button>
      </div>

      {error && (
        <section className="history-error" role="alert">
          <strong>History overview could not be loaded</strong>
          <p>{error}</p>
          <button type="button" onClick={onRefresh}>Retry overview</button>
        </section>
      )}

      {loading && !overview ? <HistoryOverviewSkeleton /> : drones.length > 0 ? (
        <section className="history-records" aria-label="Drone history records">
          <div className="history-records__columns" aria-hidden="true">
            <span>Drone</span>
            <span>Latest record</span>
            <span>Latest state</span>
            <span>Stored records</span>
            <span />
          </div>
          {drones.map((drone) => (
            <article className="history-record" key={drone.droneId}>
              <div className="history-record__identity">
                <strong>{drone.droneName}</strong>
                <span>{displayEnum(drone.vehicleType)} · ID {compactIdentifier(drone.droneId)}</span>
              </div>
              <HistoryRecordDatum
                label="Latest record"
                value={drone.lastSnapshotAtUnixMs
                  ? formatRelativeTime(drone.lastSnapshotAtUnixMs)
                  : "No telemetry saved"}
                detail={recordSpan(drone.firstSnapshotAtUnixMs, drone.lastSnapshotAtUnixMs)}
              />
              <HistoryRecordDatum
                label="Latest state"
                value={drone.latestFlightMode ? displayEnum(drone.latestFlightMode) : "No flight state"}
                detail={latestStateDetail(drone)}
              />
              <HistoryRecordDatum
                label="Stored records"
                value={`${formatCount(drone.snapshotCount)} samples`}
                detail={`${formatCount(drone.eventCount)} events`}
              />
              <button type="button" onClick={() => onOpenDrone(drone.droneId)}>Open history</button>
            </article>
          ))}
        </section>
      ) : !error ? (
        <section className="history-overview__empty">
          <p className="eyebrow">No registered drones</p>
          <h2>History begins after registration</h2>
          <p>Connect Atlas Agent. Telemetry samples and drone events will be retained here automatically.</p>
        </section>
      ) : null}
    </main>
  );
}

function HistoryRecordDatum({ label, value, detail }: { label: string; value: string; detail: string }) {
  return (
    <div className="history-record__datum">
      <span>{label}</span>
      <strong>{value}</strong>
      <small>{detail}</small>
    </div>
  );
}

function HistoryOverviewSkeleton() {
  return (
    <div className="history-overview-skeleton" aria-label="Loading history overview">
      <span /><span /><span />
    </div>
  );
}

function HistoryMetric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return (
    <div>
      <p>{label}</p>
      <strong>{value}</strong>
      <span>{detail}</span>
    </div>
  );
}

function LineChart({
  title,
  description,
  points,
  series,
  fixedDomain,
  className,
}: {
  title: string;
  description: string;
  points: TelemetryChartPoint[];
  series: SeriesDefinition[];
  fixedDomain?: [number, number];
  className?: string;
}) {
  const [activeIndex, setActiveIndex] = useState(Math.max(0, points.length - 1));
  const width = 840;
  const height = 260;
  const left = 58;
  const right = 18;
  const top = 22;
  const bottom = 34;
  const plotWidth = width - left - right;
  const plotHeight = height - top - bottom;
  const values = series.flatMap((definition) => points.map(definition.value)).filter(isNumber);
  const hasValues = values.length > 0;
  const rawMinimum = fixedDomain?.[0] ?? Math.min(...values);
  const rawMaximum = fixedDomain?.[1] ?? Math.max(...values);
  const spread = Math.max(1, rawMaximum - rawMinimum);
  const minimum = fixedDomain?.[0] ?? rawMinimum - spread * 0.08;
  const maximum = fixedDomain?.[1] ?? rawMaximum + spread * 0.08;
  const firstTime = points[0]?.receivedAtUnixMs ?? 0;
  const lastTime = points[points.length - 1]?.receivedAtUnixMs ?? firstTime + 1;
  const timeSpan = Math.max(1, lastTime - firstTime);
  const safeActiveIndex = Math.min(activeIndex, Math.max(0, points.length - 1));
  const activePoint = points[safeActiveIndex];
  const x = (timestamp: number) => left + ((timestamp - firstTime) / timeSpan) * plotWidth;
  const y = (value: number) => top + (1 - (value - minimum) / (maximum - minimum)) * plotHeight;

  const inspectPointer = (clientX: number, element: SVGSVGElement) => {
    const bounds = element.getBoundingClientRect();
    const ratio = Math.min(1, Math.max(0, (clientX - bounds.left) / bounds.width));
    setActiveIndex(Math.round(ratio * Math.max(0, points.length - 1)));
  };

  return (
    <article className={`chart-panel ${className ?? ""}`}>
      <header>
        <div>
          <h2>{title}</h2>
          <p>{description}</p>
        </div>
        <time dateTime={activePoint ? new Date(activePoint.receivedAtUnixMs).toISOString() : undefined}>
          {activePoint ? formatChartTime(activePoint.receivedAtUnixMs) : "No samples"}
        </time>
      </header>
      {hasValues ? (
        <>
          <div className="chart-legend" aria-live="polite">
            {series.map((definition) => (
              <span key={definition.label} style={{ "--series-color": definition.color } as CSSProperties}>
                <i aria-hidden="true" />
                {definition.label}
                <strong>{formatSeriesValue(activePoint ? definition.value(activePoint) : undefined, definition)}</strong>
              </span>
            ))}
          </div>
          <svg
            className="line-chart"
            viewBox={`0 0 ${width} ${height}`}
            role="img"
            aria-label={`${title}. ${description}. Use the slider below to inspect values.`}
            onPointerMove={(event) => inspectPointer(event.clientX, event.currentTarget)}
          >
            {[0, 0.25, 0.5, 0.75, 1].map((position) => {
              const lineY = top + position * plotHeight;
              const label = maximum - position * (maximum - minimum);
              return (
                <g key={position}>
                  <line className="chart-gridline" x1={left} x2={width - right} y1={lineY} y2={lineY} />
                  <text className="chart-axis-label" x={left - 10} y={lineY + 4} textAnchor="end">
                    {formatAxisValue(label)}
                  </text>
                </g>
              );
            })}
            {series.map((definition) => (
              <path
                key={definition.label}
                d={linePath(points, definition.value, x, y)}
                fill="none"
                stroke={definition.color}
                strokeWidth="2.4"
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            ))}
            {activePoint && (
              <line
                className="chart-crosshair"
                x1={x(activePoint.receivedAtUnixMs)}
                x2={x(activePoint.receivedAtUnixMs)}
                y1={top}
                y2={height - bottom}
              />
            )}
            <text className="chart-axis-label" x={left} y={height - 8}>{formatChartTime(firstTime)}</text>
            <text className="chart-axis-label" x={width - right} y={height - 8} textAnchor="end">{formatChartTime(lastTime)}</text>
          </svg>
          <input
            className="chart-scrubber"
            type="range"
            min="0"
            max={Math.max(0, points.length - 1)}
            value={safeActiveIndex}
            aria-label={`Inspect ${title.toLowerCase()} values`}
            onChange={(event) => setActiveIndex(Number(event.target.value))}
          />
        </>
      ) : (
        <p className="chart-empty">This telemetry field was not reported during the selected period.</p>
      )}
    </article>
  );
}

function FlightModeTrack({ points }: { points: TelemetryChartPoint[] }) {
  const segments = modeSegments(points);
  return (
    <section className="mode-track" aria-labelledby="mode-track-title">
      <header>
        <div>
          <p className="eyebrow">State timeline</p>
          <h2 id="mode-track-title">Flight mode</h2>
        </div>
        <span>{segments.length} {segments.length === 1 ? "phase" : "phases"}</span>
      </header>
      {segments.length > 0 ? (
        <div className="mode-track__rail">
          {segments.map((segment, index) => (
            <div
              key={`${segment.mode}-${segment.start}-${index}`}
              className={`mode-track__segment mode-track__segment--${index % 3}`}
              style={{ left: `${segment.left}%`, width: `${segment.width}%` }}
              title={`${displayEnum(segment.mode)} · ${formatChartTime(segment.start)}–${formatChartTime(segment.end)}`}
            >
              <span>{displayEnum(segment.mode)}</span>
            </div>
          ))}
        </div>
      ) : <p className="chart-empty">Flight mode was not reported during the selected period.</p>}
    </section>
  );
}

function EventTimeline({ events }: { events: VehicleEvent[] }) {
  return (
    <section className="history-events" aria-labelledby="history-events-title">
      <header>
        <div>
          <p className="eyebrow">Drone timeline</p>
          <h2 id="history-events-title">Events</h2>
        </div>
        <span>{events.length} in selected period</span>
      </header>
      {events.length > 0 ? (
        <ol>
          {events.map((event) => (
            <li key={event.id} className={`history-event history-event--${eventTone(event.severity)}`}>
              <time dateTime={new Date(event.receivedAtUnixMs).toISOString()}>
                <strong>{formatEventTime(event.receivedAtUnixMs)}</strong>
                <span>{formatEventDate(event.receivedAtUnixMs)}</span>
              </time>
              <span className="history-event__marker" aria-hidden="true" />
              <div>
                <div className="history-event__meta">
                  <strong>{displayEnum(event.eventType)}</strong>
                  <span>{originLabel(event.origin)}</span>
                  <span>{displayEnum(event.severity)}</span>
                </div>
                <p>{event.message}</p>
                {eventDetails(event.detailsJson) && <small>{eventDetails(event.detailsJson)}</small>}
              </div>
            </li>
          ))}
        </ol>
      ) : (
        <div className="history-events__empty">
          <strong>No drone events</strong>
          <p>PX4 messages and meaningful telemetry transitions will appear here.</p>
        </div>
      )}
    </section>
  );
}

function HistorySkeleton() {
  return (
    <div className="history-skeleton" aria-label="Reading local flight history" role="status">
      <span />
      <span />
      <span />
    </div>
  );
}

function summarize(points: TelemetryChartPoint[]) {
  return {
    maximumAltitude: maximum(points.map((point) => point.relativeAltitudeM)),
    maximumSpeed: maximum(points.map((point) => point.groundSpeedMps)),
    minimumBattery: minimum(points.map((point) => point.batteryPercent)),
  };
}

function maximum(values: Array<Nullable<number>>) {
  const reported = values.filter(isNumber);
  return reported.length > 0 ? Math.max(...reported) : undefined;
}

function minimum(values: Array<Nullable<number>>) {
  const reported = values.filter(isNumber);
  return reported.length > 0 ? Math.min(...reported) : undefined;
}

function isNumber(value: Nullable<number>): value is number {
  return value != null && Number.isFinite(value);
}

function linePath(
  points: TelemetryChartPoint[],
  value: (point: TelemetryChartPoint) => Nullable<number>,
  x: (timestamp: number) => number,
  y: (value: number) => number,
) {
  let drawing = false;
  return points.map((point) => {
    const current = value(point);
    if (!isNumber(current)) {
      drawing = false;
      return "";
    }
    const command = drawing ? "L" : "M";
    drawing = true;
    return `${command}${x(point.receivedAtUnixMs).toFixed(2)},${y(current).toFixed(2)}`;
  }).join(" ");
}

function modeSegments(points: TelemetryChartPoint[]) {
  if (points.length === 0) return [];
  const start = points[0].receivedAtUnixMs;
  const end = points[points.length - 1]?.receivedAtUnixMs ?? start + 1;
  const span = Math.max(1, end - start);
  const segments: Array<{ mode: string; start: number; end: number; left: number; width: number }> = [];
  for (const point of points) {
    const mode = point.flightMode || "UNKNOWN";
    const previous = segments[segments.length - 1];
    if (!previous || previous.mode !== mode) {
      if (previous) previous.end = point.receivedAtUnixMs;
      segments.push({ mode, start: point.receivedAtUnixMs, end, left: 0, width: 0 });
    }
  }
  return segments.map((segment) => ({
    ...segment,
    left: ((segment.start - start) / span) * 100,
    width: Math.max(0.8, ((segment.end - segment.start) / span) * 100),
  }));
}

function eventDetails(value: Nullable<string>) {
  if (!value) return undefined;
  try {
    const details = JSON.parse(value) as { previous?: unknown; current?: unknown };
    if (details.previous !== undefined && details.current !== undefined) {
      return `${displayEnum(String(details.previous))} → ${displayEnum(String(details.current))}`;
    }
  } catch {
    return undefined;
  }
  return undefined;
}

function historyRangeLabel(series?: TelemetryChartSeries) {
  if (!series) return "No range";
  const start = new Date(series.fromReceivedAtUnixMs);
  const end = new Date(series.toReceivedAtUnixMs);
  const sameDay = start.toDateString() === end.toDateString();
  return sameDay
    ? `${formatEventDate(start.getTime())}, ${formatChartTime(start.getTime())}–${formatChartTime(end.getTime())}`
    : `${formatEventDate(start.getTime())} – ${formatEventDate(end.getTime())}`;
}

function bucketDetail(series?: TelemetryChartSeries) {
  if (!series) return "No samples in range";
  const seconds = Math.max(1, Math.round(series.bucketWidthMs / 1_000));
  return `${historyRangeLabel(series)} · ${seconds}s buckets`;
}

function formatMetric(value: Nullable<number>, digits: number, suffix: string) {
  return value == null ? "Not reported" : `${value.toFixed(digits)}${suffix}`;
}

function formatSeriesValue(value: Nullable<number>, series: SeriesDefinition) {
  return value == null ? "—" : `${value.toFixed(series.digits)}${series.unit}`;
}

function formatAxisValue(value: number) {
  const absolute = Math.abs(value);
  return absolute >= 100 ? value.toFixed(0) : absolute >= 10 ? value.toFixed(1) : value.toFixed(2);
}

function formatChartTime(timestamp: number) {
  return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" }).format(timestamp);
}

function formatEventTime(timestamp: number) {
  return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(timestamp);
}

function formatEventDate(timestamp: number) {
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(timestamp);
}

function formatRelativeTime(timestamp: number) {
  const seconds = Math.max(0, Math.round((Date.now() - timestamp) / 1_000));
  if (seconds < 2) return "Now";
  if (seconds < 60) return `${seconds} seconds ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} ${minutes === 1 ? "minute" : "minutes"} ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} ${hours === 1 ? "hour" : "hours"} ago`;
  const days = Math.floor(hours / 24);
  return `${days} ${days === 1 ? "day" : "days"} ago`;
}

function displayEnum(value: string) {
  return value.toLowerCase().replace(/_/g, " ");
}

function formatCount(value: number) {
  return new Intl.NumberFormat().format(value);
}

function compactIdentifier(value: string) {
  return value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value;
}

function recordSpan(first: Nullable<number>, last: Nullable<number>) {
  if (!first || !last) return "Nothing in the retention window";
  if (first === last) return "First retained sample";
  const hours = Math.max(1, Math.round((last - first) / (60 * 60 * 1_000)));
  if (hours < 24) return `${hours} ${hours === 1 ? "hour" : "hours"} recorded`;
  const days = Math.max(1, Math.round(hours / 24));
  return `${days} ${days === 1 ? "day" : "days"} recorded`;
}

function latestStateDetail(drone: DroneHistorySummary) {
  const state = drone.latestInAir == null
    ? undefined
    : drone.latestInAir ? "In air" : "On ground";
  const battery = drone.latestBatteryPercent == null
    ? undefined
    : `${drone.latestBatteryPercent.toFixed(0)}% battery`;
  return [state, battery].filter(Boolean).join(" · ") || "State unavailable";
}

function originLabel(origin: string) {
  if (origin === "atlas_native") return "Atlas Native";
  if (origin === "atlas_agent") return "Atlas Agent";
  if (origin === "px4") return "PX4";
  return displayEnum(origin);
}

function eventTone(severity: string) {
  if (["EMERGENCY", "ALERT", "CRITICAL", "ERROR"].includes(severity.toUpperCase())) return "critical";
  if (severity.toUpperCase() === "WARNING") return "warning";
  return "neutral";
}
