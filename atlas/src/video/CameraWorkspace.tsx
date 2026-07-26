import type { AircraftFollowSession } from "../follow/followTypes";
import { InspectionPayloadControl } from "../missions/MissionPayloadControl";
import type { FleetAircraft } from "../operationsTypes";
import { LiveVideo } from "./LiveVideo";
import "./CameraWorkspace.css";

type CameraWorkspaceProps = {
  nativeAvailable: boolean;
  aircraft?: FleetAircraft;
  activeFollow?: AircraftFollowSession;
  onOpenFollow: () => void;
  onStopFollow: (session: AircraftFollowSession) => void;
  onOpenCommand: () => void;
  stopPending: boolean;
};

export function CameraWorkspace({
  nativeAvailable,
  aircraft,
  activeFollow,
  onOpenFollow,
  onStopFollow,
  onOpenCommand,
  stopPending,
}: CameraWorkspaceProps) {
  const capabilities = aircraft?.agentCapabilities ?? [];
  const telemetry = aircraft?.telemetry;
  const gimbalReady = capabilities.includes("gimbal:detected");
  const aircraftFollowReady = capabilities.includes("aircraft_follow:standoff:v1");
  const positionReady = telemetry?.status === "live"
    && telemetry.health?.localPositionOk === true
    && telemetry.health.globalPositionOk === true;
  const flightReady = aircraft?.connectionStatus === "connected"
    && telemetry?.status === "live"
    && telemetry.armed === true
    && telemetry.inAir === true;
  const battery = telemetry?.batteryPercent;

  return (
    <section className="camera-workspace" aria-label="Camera and follow workspace">
      <section className="camera-feed-panel" aria-label="Live camera feed and detected objects">
        <header className="camera-panel-heading">
          <div>
            <p className="eyebrow">Live feed</p>
            <h2>{cameraName(capabilities)}</h2>
          </div>
          <span>{aircraft?.connectionStatus === "connected" ? "Aircraft link connected" : "Waiting for aircraft link"}</span>
        </header>
        <LiveVideo
          nativeAvailable={nativeAvailable}
          droneId={aircraft?.droneId ?? undefined}
          aircraft={aircraft}
        />
      </section>

      <aside className="camera-control-panel" aria-label="Camera and follow controls">
        <header className="camera-panel-heading">
          <div>
            <p className="eyebrow">Payload</p>
            <h2>Controls</h2>
          </div>
          <span>{gimbalReady ? "Gimbal detected" : "Gimbal unavailable"}</span>
        </header>

        <InspectionPayloadControl aircraft={aircraft} />

        <section className="camera-follow-modes" aria-labelledby="camera-follow-modes-title">
          <header>
            <div>
              <p className="eyebrow">Selected target</p>
              <h2 id="camera-follow-modes-title">Follow modes</h2>
            </div>
            <span>Camera or aircraft</span>
          </header>

          <article className="camera-mode-card">
            <div className="camera-mode-card__heading">
              <h3>Camera follow</h3>
              <span>Camera only</span>
            </div>
            <p>Keeps the selected track centred by commanding gimbal rates. The aircraft does not move.</p>
            <ul className="camera-gates">
              <CameraGate ready={gimbalReady} label="Gimbal control" detail={gimbalReady ? "Available from the connected Agent" : "No MAVLink gimbal detected"} />
              <CameraGate ready={undefined} label="Target selected" detail="Choose a confirmed target in Detected objects" />
              <CameraGate ready={nativeAvailable && aircraft?.connectionStatus === "connected"} label="Aircraft link" detail={aircraft?.connectionStatus === "connected" ? "Connected" : "A live aircraft link is required"} />
            </ul>
            <p className="camera-mode-card__action-note">Start or stop Camera follow beside the selected target in the feed.</p>
          </article>

          <article className={activeFollow ? "camera-mode-card camera-mode-card--active" : "camera-mode-card"}>
            <div className="camera-mode-card__heading">
              <h3>Aircraft follow</h3>
              <span>Flies the aircraft</span>
            </div>
            <p>Commands PX4 Offboard from a reviewed standoff point, within the limits you set.</p>
            <ul className="camera-gates">
              <CameraGate ready={aircraftFollowReady} label="Agent support" detail={aircraftFollowReady ? "Aircraft follow is supported" : "Connected Agent does not advertise Aircraft follow"} />
              <CameraGate ready={positionReady} label="Position fix" detail={positionReady ? "Local and global position are ready" : "Live local and global position are required"} />
              <CameraGate ready={flightReady} label="Aircraft ready in flight" detail={flightReady ? telemetry?.flightMode || "In air" : "Connected, armed, in-air telemetry required"} />
              <CameraGate ready={battery != null ? battery >= (activeFollow?.minimumBatteryPercent ?? 25) : false} label="Battery above reserve" detail={battery == null ? "Battery unavailable" : `${battery.toFixed(0)}% · reserve ${(activeFollow?.minimumBatteryPercent ?? 25).toFixed(0)}%`} />
            </ul>
            {activeFollow ? (
              <>
                <div className={`camera-follow-state camera-follow-state--${activeFollow.state.toLowerCase()}`}>
                  <span aria-hidden="true" />
                  <div>
                    <strong>{followStateLabel(activeFollow.state)}</strong>
                    <small>{activeFollow.standoffM.toFixed(0)} m standoff · target ±{activeFollow.target.horizontalUncertaintyM.toFixed(1)} m</small>
                  </div>
                </div>
                <button className="camera-danger-action" type="button" disabled={stopPending} onClick={() => onStopFollow(activeFollow)}>
                  {stopPending ? "Confirming PX4 Hold…" : "Stop following · Hold"}
                </button>
                <p className="camera-mode-card__action-note">Stopping commands PX4 Hold. It never triggers RTL or Land.</p>
              </>
            ) : (
              <button className="camera-primary-action" type="button" onClick={onOpenFollow}>
                Review limits and start
              </button>
            )}
          </article>
        </section>

        <section className="camera-safety-link" aria-label="Flight safety">
          <div>
            <strong>Flight safety</strong>
            <span>Hold, Return home, and Land stay in Command with their receipt and confirmation states.</span>
          </div>
          <button type="button" onClick={onOpenCommand}>Open Command</button>
        </section>
      </aside>
    </section>
  );
}

function CameraGate({ ready, label, detail }: { ready: boolean | undefined; label: string; detail: string }) {
  return (
    <li data-ready={ready === undefined ? "unknown" : String(ready)}>
      <span aria-hidden="true">{ready === undefined ? "•" : ready ? "✓" : "×"}</span>
      <div><strong>{label}</strong><small>{detail}</small></div>
    </li>
  );
}

function cameraName(capabilities: string[]) {
  const reported = capabilities.find((capability) => capability.startsWith("camera:model:"));
  return reported ? reported.slice("camera:model:".length).replace(/_/g, " ") : "Aircraft camera";
}

function followStateLabel(state: AircraftFollowSession["state"]) {
  if (state === "DEGRADED_HOLD") return "Follow stopped · holding";
  return state.toLowerCase().replace(/_/g, " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}
