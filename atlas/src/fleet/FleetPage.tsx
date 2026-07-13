import type { FleetAircraft, NativeState, Nullable, StatusTone } from "../operationsTypes";
import "./FleetPage.css";

type FleetPageProps = {
  aircraft: FleetAircraft[];
  generatedAtUnixMs?: number;
  nativeState: NativeState;
  listenAddress: string;
  onOpenAircraft: (droneId: string) => void;
  onOpenHistory: (droneId: string) => void;
};

export function FleetPage({
  aircraft,
  generatedAtUnixMs,
  nativeState,
  listenAddress,
  onOpenAircraft,
  onOpenHistory,
}: FleetPageProps) {
  const connected = aircraft.filter((item) => item.connectionStatus === "connected").length;
  const linkAlerts = aircraft.filter((item) => item.connectionStatus !== "connected").length;
  const airborne = aircraft.filter((item) => item.telemetry?.inAir).length;

  return (
    <main className="fleet-workspace" id="main-content">
      <header className="fleet-heading">
        <div>
          <p className="eyebrow">Local operations</p>
          <h1>Fleet</h1>
          <p>
            Drones registered with this ground station, ordered by most recent telemetry.
          </p>
        </div>
        <span className="fleet-updated">
          {generatedAtUnixMs ? `Updated ${formatRelativeTime(generatedAtUnixMs).toLowerCase()}` : "Checking local fleet"}
        </span>
      </header>

      <section className="fleet-summary" aria-label="Fleet summary">
        <FleetMetric label="Drones" value={aircraft.length} />
        <FleetMetric label="Connected" value={connected} tone={connected > 0 ? "positive" : "neutral"} />
        <FleetMetric label="Link alerts" value={linkAlerts} tone={linkAlerts > 0 ? "warning" : "neutral"} />
        <FleetMetric label="Airborne" value={airborne} tone={airborne > 0 ? "positive" : "neutral"} />
      </section>

      {nativeState === "unavailable" && (
        <section className="fleet-notice fleet-notice--critical" role="alert">
          <strong>Local services are unavailable</strong>
          <p>Reopen Atlas before beginning vehicle operations.</p>
        </section>
      )}

      {nativeState !== "unavailable" && aircraft.length === 0 && (
        <section className="fleet-empty" aria-labelledby="fleet-empty-title">
          <div>
            <p className="eyebrow">No registered drones</p>
            <h2 id="fleet-empty-title">Waiting for Atlas Agent</h2>
          </div>
          <div>
            <p>
              Power the onboard computer and confirm the HM30 network link. The agent will
              register the drone automatically when it reaches this ground station.
            </p>
            <dl>
              <div>
                <dt>Ground station listener</dt>
                <dd>{listenAddress}</dd>
              </div>
            </dl>
          </div>
        </section>
      )}

      {aircraft.length > 0 && (
        <section className="fleet-list" aria-labelledby="fleet-list-title">
          <header>
            <h2 id="fleet-list-title">Drones</h2>
            <span>{aircraft.length} registered locally</span>
          </header>
          <div className="fleet-list__columns" aria-hidden="true">
            <span>Drone</span>
            <span>Link</span>
            <span>Flight</span>
            <span>Battery</span>
            <span>Position</span>
            <span>Telemetry</span>
            <span />
          </div>
          <div className="fleet-list__rows">
            {aircraft.map((item) => (
              <FleetRow
                key={item.droneId}
                aircraft={item}
                onOpen={() => item.droneId && onOpenAircraft(item.droneId)}
                onOpenHistory={() => item.droneId && onOpenHistory(item.droneId)}
              />
            ))}
          </div>
        </section>
      )}
    </main>
  );
}

function FleetMetric({ label, value, tone = "neutral" }: { label: string; value: number; tone?: StatusTone }) {
  return (
    <article className={`fleet-metric fleet-metric--${tone}`}>
      <span>{label}</span>
      <strong>{value.toString().padStart(2, "0")}</strong>
    </article>
  );
}

function FleetRow({
  aircraft,
  onOpen,
  onOpenHistory,
}: {
  aircraft: FleetAircraft;
  onOpen: () => void;
  onOpenHistory: () => void;
}) {
  const telemetry = aircraft.telemetry;
  const tone = connectionTone(aircraft.connectionStatus);
  const connectionLabel = aircraft.connectionStatus === "stale" ? "Degraded" : capitalize(aircraft.connectionStatus);

  return (
    <article className={`fleet-row fleet-row--${tone}`}>
      <div className="fleet-aircraft-identity">
        <span className="state-dot" aria-hidden="true" />
        <div>
          <strong>{aircraft.droneName || "Unnamed drone"}</strong>
          <span>{droneIdentity(aircraft.vehicleType, aircraft.droneId)}</span>
        </div>
      </div>
      <FleetDatum
        label="Link"
        value={connectionLabel}
        detail={aircraft.lastHeartbeatAtUnixMs
          ? `Heartbeat ${formatRelativeTime(aircraft.lastHeartbeatAtUnixMs).toLowerCase()}`
          : "No heartbeat received"}
        tone={tone}
      />
      <FleetDatum
        label="Flight"
        value={telemetry ? flightState(telemetry.armed, telemetry.inAir, telemetry.landedState) : "No flight data"}
        detail={telemetry?.flightMode ? `Mode · ${displayEnum(telemetry.flightMode)}` : "Mode unavailable"}
      />
      <FleetDatum
        label="Battery"
        value={telemetry?.batteryPercent == null ? "—" : formatMeasurement(telemetry.batteryPercent, 0, "%")}
        detail={batteryDetail(telemetry)}
      />
      <FleetDatum
        label="Position"
        value={telemetry?.relativeAltitudeM == null
          ? "—"
          : formatMeasurement(telemetry.relativeAltitudeM, 1, " m AGL")}
        detail={gpsStatus(telemetry?.gpsFix, telemetry?.satellitesVisible)}
      />
      <FleetDatum
        label="Telemetry"
        value={telemetry ? capitalize(telemetry.status) : "Unavailable"}
        detail={telemetry?.receivedAtUnixMs
          ? `Last sample ${formatRelativeTime(telemetry.receivedAtUnixMs).toLowerCase()}`
          : "No samples received"}
        tone={telemetry?.status === "live" ? "positive" : telemetry ? "warning" : "neutral"}
      />
      <div className="fleet-row__actions">
        <button type="button" className="text-action" onClick={onOpenHistory}>History</button>
        <button type="button" className="primary-action" onClick={onOpen}>Open drone</button>
      </div>
    </article>
  );
}

function FleetDatum({
  label,
  value,
  detail,
  tone = "neutral",
}: {
  label: string;
  value: string;
  detail: string;
  tone?: StatusTone;
}) {
  return (
    <div className={`fleet-datum fleet-datum--${tone}`}>
      <span>{label}</span>
      <strong>{value}</strong>
      <small>{detail}</small>
    </div>
  );
}

function connectionTone(status: FleetAircraft["connectionStatus"]): StatusTone {
  if (status === "connected") return "positive";
  if (status === "stale") return "warning";
  return "neutral";
}

function flightState(armed: Nullable<boolean>, inAir: Nullable<boolean>, landedState: Nullable<string>) {
  if (inAir) return armed === false ? "In air · disarmed" : "In air · armed";
  if (landedState && landedState !== "UNKNOWN") {
    return `${displayEnum(landedState)}${armed ? " · armed" : " · disarmed"}`;
  }
  if (armed != null) return armed ? "On ground · armed" : "On ground · disarmed";
  return "Not reported";
}

function gpsStatus(fix: Nullable<string>, satellites: Nullable<number>) {
  if (!fix && satellites == null) return "No GPS data";
  const fixLabel = displayEnum(fix) || "GPS";
  return satellites == null ? fixLabel : `${fixLabel} · ${satellites} sat`;
}

function batteryDetail(telemetry?: FleetAircraft["telemetry"]) {
  const percent = telemetry?.batteryPercent;
  if (percent == null) return "No battery data";
  const primaryBattery = telemetry?.batteries?.find((battery) =>
    battery.function === "ALL" || battery.function === "PROPULSION"
  ) ?? telemetry?.batteries?.[0];
  if (primaryBattery?.voltageV != null) return `${primaryBattery.voltageV.toFixed(1)} V`;
  if (percent <= 20) return "Low charge";
  if (percent <= 35) return "Monitor charge";
  return "Charge available";
}

function formatMeasurement(value: Nullable<number>, digits: number, suffix: string) {
  return value == null ? "Not reported" : `${value.toFixed(digits)}${suffix}`;
}

function formatRelativeTime(timestamp: Nullable<number>) {
  if (!timestamp) return "Not received";
  const ageSeconds = Math.max(0, Math.round((Date.now() - timestamp) / 1000));
  if (ageSeconds < 2) return "Now";
  if (ageSeconds < 60) return `${ageSeconds}s ago`;
  const ageMinutes = Math.floor(ageSeconds / 60);
  if (ageMinutes < 60) return `${ageMinutes}m ago`;
  const ageHours = Math.floor(ageMinutes / 60);
  if (ageHours < 24) return `${ageHours}h ago`;
  return `${Math.floor(ageHours / 24)}d ago`;
}

function displayEnum(value: Nullable<string>) {
  if (!value || value === "unknown") return "";
  return value.toLowerCase().replace(/_/g, " ");
}

function capitalize(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1);
}

function compactIdentifier(value: string) {
  return value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value;
}

function droneIdentity(vehicleType: Nullable<string>, droneId: Nullable<string>) {
  const type = displayEnum(vehicleType);
  const id = droneId ? `ID ${compactIdentifier(droneId)}` : "ID unavailable";
  return type ? `${type} · ${id}` : id;
}
