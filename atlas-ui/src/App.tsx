import { useEffect, useMemo, useState } from "react";
import { Activity, Map, RadioTower, RefreshCw, ShieldCheck } from "lucide-react";
import { type Drone, fetchDrones } from "./api/atlasApi";

const flowItems = [
  { label: "Atlas UI", detail: "Operator workflow", icon: Map },
  { label: "Atlas Backend", detail: "Policy and audit", icon: ShieldCheck },
  { label: "Atlas Agent", detail: "Onboard bridge", icon: RadioTower },
  { label: "PX4", detail: "Flight authority", icon: Activity },
];

const statusStyles = {
  registered: "bg-atlas-sky/20 text-atlas-ink",
  online: "bg-atlas-field/25 text-atlas-ink",
  stale: "bg-atlas-signal/20 text-atlas-ink",
  offline: "bg-atlas-ink/10 text-atlas-ink/70",
};

const statusDescriptions = {
  registered: "Registered, waiting for first heartbeat",
  online: "Heartbeat is fresh",
  stale: "Heartbeat is delayed",
  offline: "Heartbeat is too old",
};

const telemetryStyles = {
  unknown: "bg-atlas-ink/10 text-atlas-ink/70",
  fresh: "bg-atlas-field/25 text-atlas-ink",
  stale: "bg-atlas-signal/20 text-atlas-ink",
  lost: "bg-atlas-ink/10 text-atlas-ink/70",
};

export function App() {
  const [drones, setDrones] = useState<Drone[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let active = true;

    async function loadDrones() {
      try {
        const nextDrones = await fetchDrones();
        if (!active) {
          return;
        }

        setDrones(nextDrones);
        setError(null);
      } catch (err) {
        if (!active) {
          return;
        }

        setError(err instanceof Error ? err.message : "Failed to load drones");
      } finally {
        if (active) {
          setLoading(false);
        }
      }
    }

    void loadDrones();
    const interval = window.setInterval(loadDrones, 3000);

    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, []);

  const onlineCount = useMemo(
    () => drones.filter((drone) => drone.status === "online").length,
    [drones],
  );
  const connectionLabel = error ? "backend unavailable" : `${onlineCount}/${drones.length} online`;

  return (
    <main className="min-h-screen bg-atlas-mist text-atlas-ink">
      <section className="mx-auto flex min-h-screen w-full max-w-7xl flex-col px-6 py-6 sm:px-8 lg:px-10">
        <header className="flex items-center justify-between border-b border-atlas-ink/15 pb-5">
          <div>
            <p className="text-sm font-semibold uppercase tracking-[0.18em] text-atlas-signal">
              Atlas Operations
            </p>
            <h1 className="mt-2 text-3xl font-semibold sm:text-4xl">Fleet control starts here</h1>
          </div>
          <div className="hidden items-center gap-2 rounded-full bg-atlas-panel px-4 py-2 text-sm font-medium shadow-sm shadow-atlas-ink/5 sm:flex">
            <span
              className={`h-2.5 w-2.5 rounded-full ${error ? "bg-atlas-signal" : "bg-atlas-field"}`}
            />
            {connectionLabel}
          </div>
        </header>

        <div className="grid flex-1 gap-8 py-10 lg:grid-cols-[1fr_0.9fr]">
          <section className="space-y-8">
            <div>
              <p className="max-w-2xl text-lg leading-8 text-atlas-ink/75">
                Atlas is now proving its first operational loop: agent registration,
                backend heartbeat storage, and UI-visible drone status. PX4 integration comes after
                this identity path is reliable.
              </p>
            </div>

            <section className="bg-atlas-panel p-5 shadow-sm shadow-atlas-ink/5">
              <div className="flex items-center justify-between border-b border-atlas-ink/10 pb-4">
                <h2 className="text-xl font-semibold">Fleet</h2>
                <div className="flex items-center gap-2 text-sm text-atlas-ink/60">
                  <RefreshCw aria-hidden="true" size={16} />
                  3s refresh
                </div>
              </div>

              <div className="mt-5">
                {loading && <p className="text-sm text-atlas-ink/65">Loading fleet state...</p>}

                {error && (
                  <p className="border-l-4 border-atlas-signal bg-atlas-signal/10 px-4 py-3 text-sm">
                    Backend unavailable. Showing the last known fleet state when available. {error}
                  </p>
                )}

                {!loading && drones.length === 0 && !error && (
                  <p className="text-sm text-atlas-ink/65">
                    No agents have registered yet. Start `atlas-agent` to bring a drone online.
                  </p>
                )}

                <div className="space-y-3">
                  {drones.map((drone) => (
                    <article
                      key={drone.id}
                      className="grid gap-4 border border-atlas-ink/10 p-4"
                    >
                      <div className="grid gap-4 sm:grid-cols-[1fr_auto]">
                        <div>
                          <div className="flex flex-wrap items-center gap-3">
                            <h3 className="text-lg font-semibold">{drone.name}</h3>
                            <span
                              className={`rounded-full px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] ${
                                statusStyles[drone.status]
                              }`}
                            >
                              {drone.status}
                            </span>
                            <span
                              className={`rounded-full px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] ${
                                telemetryStyles[drone.telemetry?.state ?? "unknown"]
                              }`}
                            >
                              telemetry {drone.telemetry?.state ?? "unknown"}
                            </span>
                          </div>
                          <p className="mt-2 text-sm text-atlas-ink/65">
                            {drone.id} through {drone.agentId}
                          </p>
                          <p className="mt-1 text-sm text-atlas-ink/65">
                            {statusDescriptions[drone.status]}
                          </p>
                        </div>
                        <div className="text-left text-sm text-atlas-ink/65 sm:text-right">
                          <p>Last seen {formatTime(drone.lastSeenAt)}</p>
                          <p>Heartbeat {formatTime(drone.lastHeartbeatAt)}</p>
                          <p>Telemetry {formatTime(drone.telemetry?.receivedAt)}</p>
                        </div>
                      </div>

                      <TelemetryGrid drone={drone} />
                    </article>
                  ))}
                </div>
              </div>
            </section>
          </section>

          <section className="bg-atlas-panel p-5 shadow-sm shadow-atlas-ink/5">
            <div className="flex items-center justify-between border-b border-atlas-ink/10 pb-4">
              <h2 className="text-xl font-semibold">System boundary</h2>
              <span className="rounded-full bg-atlas-mist px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/60">
                Phase 0
              </span>
            </div>

            <div className="mt-5 space-y-3">
              {flowItems.map((item, index) => {
                const Icon = item.icon;
                return (
                  <div key={item.label} className="flex items-center gap-4">
                    <div className="flex h-12 w-12 shrink-0 items-center justify-center bg-atlas-ink text-atlas-panel">
                      <Icon aria-hidden="true" size={22} strokeWidth={1.8} />
                    </div>
                    <div className="min-w-0 flex-1">
                      <p className="font-semibold">{item.label}</p>
                      <p className="text-sm text-atlas-ink/65">{item.detail}</p>
                    </div>
                    {index < flowItems.length - 1 && (
                      <span className="hidden text-sm font-semibold text-atlas-signal sm:inline">
                        connects to
                      </span>
                    )}
                  </div>
                );
              })}
            </div>
          </section>
        </div>
      </section>
    </main>
  );
}

function TelemetryGrid({ drone }: { drone: Drone }) {
  if (!drone.telemetry) {
    return (
      <div className="border-t border-atlas-ink/10 pt-4 text-sm text-atlas-ink/65">
        Waiting for first telemetry snapshot.
      </div>
    );
  }

  const telemetry = drone.telemetry;

  return (
    <div className="grid gap-3 border-t border-atlas-ink/10 pt-4 sm:grid-cols-2 lg:grid-cols-3">
      <Metric label="Battery" value={`${telemetry.batteryPercent.toFixed(1)}%`} />
      <Metric label="Altitude" value={`${telemetry.relativeAltitudeM.toFixed(1)} m`} />
      <Metric label="Mode" value={telemetry.flightMode} />
      <Metric label="Armed" value={telemetry.armed ? "yes" : "no"} />
      <Metric label="In air" value={telemetry.inAir ? "yes" : "no"} />
      <Metric label="GPS" value={`${telemetry.gpsFix} / ${telemetry.satellitesVisible} sats`} />
      <Metric label="Position" value={`${telemetry.latitude.toFixed(5)}, ${telemetry.longitude.toFixed(5)}`} />
      <Metric label="Heading" value={`${telemetry.headingDeg.toFixed(0)} deg`} />
      <Metric label="Source" value={telemetry.source} />
    </div>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <p className="text-xs font-semibold uppercase tracking-[0.14em] text-atlas-ink/45">{label}</p>
      <p className="mt-1 truncate text-sm font-semibold text-atlas-ink">{value}</p>
    </div>
  );
}

function formatTime(value?: string) {
  if (!value) {
    return "not received";
  }

  return new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(new Date(value));
}
