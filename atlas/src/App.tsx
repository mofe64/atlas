import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { backendBaseUrl, getBackendHealth, type BackendHealth } from "./api/backend";
import "./App.css";

type RuntimeInfo = {
  appVersion: string;
  targetArch: string;
  targetOs: string;
};

type CheckState<T> =
  | { state: "checking" }
  | { state: "available"; value: T }
  | { state: "unavailable"; reason: string };

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function App() {
  const [runtime, setRuntime] = useState<CheckState<RuntimeInfo>>({ state: "checking" });
  const [backend, setBackend] = useState<CheckState<BackendHealth>>({ state: "checking" });

  useEffect(() => {
    const controller = new AbortController();

    invoke<RuntimeInfo>("runtime_info")
      .then((value) => setRuntime({ state: "available", value }))
      .catch((error) => setRuntime({ state: "unavailable", reason: errorMessage(error) }));

    getBackendHealth(controller.signal)
      .then((value) => setBackend({ state: "available", value }))
      .catch((error) => {
        if (!controller.signal.aborted) {
          setBackend({ state: "unavailable", reason: errorMessage(error) });
        }
      });

    return () => controller.abort();
  }, []);

  return (
    <main className="app-shell">
      <header className="masthead">
        <div className="wordmark" aria-label="Atlas">
          <span className="wordmark-mark" aria-hidden="true">A</span>
          <span>Atlas</span>
        </div>
        <div className="environment-label">Native foundation · local</div>
      </header>

      <section className="hero" aria-labelledby="page-title">
        <p className="eyebrow">System readiness</p>
        <h1 id="page-title">The new Atlas control surface starts here.</h1>
        <p className="hero-copy">
          This shell proves the two foundational connections: React to the native Rust host,
          and the installed app to the independent Atlas API.
        </p>
      </section>

      <section className="readiness" aria-label="Application readiness checks">
        <StatusRow
          index="01"
          label="Native host"
          detail={runtime.state === "available"
            ? `${runtime.value.targetOs} · ${runtime.value.targetArch} · v${runtime.value.appVersion}`
            : runtime.state === "unavailable"
              ? "Open with `npm run tauri dev` to load the Rust host."
              : "Reading the Tauri runtime…"}
          state={runtime.state}
        />
        <StatusRow
          index="02"
          label="Atlas API"
          detail={backend.state === "available"
            ? `${backend.value.service} answered at ${backendBaseUrl}`
            : backend.state === "unavailable"
              ? `Start atlas-backend on ${backendBaseUrl}.`
              : `Checking ${backendBaseUrl}…`}
          state={backend.state}
        />
        <StatusRow
          index="03"
          label="Operator workspace"
          detail="Ready for the first domain workflow."
          state="available"
        />
      </section>

      <footer className="system-note">
        <span>PX4 remains flight-control authority.</span>
        <span>Atlas owns orchestration, policy, and operator state.</span>
      </footer>
    </main>
  );
}

type StatusRowProps = {
  detail: string;
  index: string;
  label: string;
  state: "checking" | "available" | "unavailable";
};

function StatusRow({ detail, index, label, state }: StatusRowProps) {
  const stateLabel = state === "available" ? "Ready" : state === "checking" ? "Checking" : "Offline";

  return (
    <article className="status-row">
      <span className="status-index">{index}</span>
      <h2>{label}</h2>
      <p>{detail}</p>
      <span className={`status-badge status-badge--${state}`}>
        <span className="status-dot" aria-hidden="true" />
        {stateLabel}
      </span>
    </article>
  );
}

export default App;
