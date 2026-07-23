import { OrbitControls } from "@react-three/drei";
import { Canvas } from "@react-three/fiber";
import { invoke } from "@tauri-apps/api/core";
import { BufferAttribute, BufferGeometry } from "three";
import { useEffect, useMemo, useRef, useState } from "react";
import type { FleetAircraft } from "../operationsTypes";
import { LiveVideo } from "../video/LiveVideo";
import { decodeSpatialFrame, maximumSpatialPoints, type SpatialCloudFrame, type SpatialCloudMetadata } from "./spatialFrame";
import "./IndoorPage.css";

type SpatialSnapshot = {
  status: "connected" | "stale" | "disconnected";
  sessionId: string;
  streamId: string;
  droneId: string;
  sourceId: string;
  maximumPoints: number;
  connectedAtUnixMs: number;
  lastReceivedAtUnixMs: number;
  latestCloud?: SpatialCloudMetadata;
};

type IndoorPageProps = {
  nativeAvailable: boolean;
  aircraft: FleetAircraft[];
  preferredDroneId?: string;
  onSelectAircraft?: (droneId: string) => void;
};

const leaseDurationMs = 12_000;

export function IndoorPage({ nativeAvailable, aircraft, preferredDroneId, onSelectAircraft }: IndoorPageProps) {
  const eligibleAircraft = useMemo(() => aircraft.filter((item) =>
    item.vehicleStatus !== "archived" && item.agentCapabilities?.includes("spatial:complete_cloud:v1"),
  ), [aircraft]);
  const [droneId, setDroneId] = useState(
    preferredDroneId && eligibleAircraft.some((item) => item.droneId === preferredDroneId)
      ? preferredDroneId
      : eligibleAircraft[0]?.droneId ?? "",
  );
  const [snapshot, setSnapshot] = useState<SpatialSnapshot>();
  const [frame, setFrame] = useState<SpatialCloudFrame>();
  const [error, setError] = useState<string>();
  const [cameraRevision, setCameraRevision] = useState(0);
  const latestSequence = useRef(0);
  const latestEpoch = useRef<string | undefined>(undefined);

  useEffect(() => {
    if (droneId && eligibleAircraft.some((item) => item.droneId === droneId)) return;
    const replacement = eligibleAircraft[0]?.droneId ?? "";
    setDroneId(replacement);
    onSelectAircraft?.(replacement);
  }, [droneId, eligibleAircraft, onSelectAircraft]);

  useEffect(() => {
    latestSequence.current = 0;
    latestEpoch.current = undefined;
    setSnapshot(undefined);
    setFrame(undefined);
    setError(undefined);
    if (!nativeAvailable || !droneId) return;

    let active = true;
    const subscriptionId = `indoor-${crypto.randomUUID()}`;
    async function renew() {
      try {
        await invoke("spatial_subscription_renew", {
          droneId,
          subscriptionId,
          leaseDurationMs,
        });
        if (active) setError(undefined);
      } catch (reason) {
        if (active) setError(message(reason));
      }
    }
    void invoke("spatial_subscription_start", {
      droneId,
      subscriptionId,
      leaseDurationMs,
    }).catch((reason) => active && setError(message(reason)));
    const leaseInterval = window.setInterval(renew, 5_000);

    return () => {
      active = false;
      window.clearInterval(leaseInterval);
      void invoke("spatial_subscription_stop", { droneId, subscriptionId }).catch(() => undefined);
    };
  }, [droneId, nativeAvailable]);

  useEffect(() => {
    if (!nativeAvailable || !droneId) return;
    let active = true;
    async function refreshSnapshot() {
      try {
        const next = await invoke<SpatialSnapshot | null>("spatial_snapshot", { droneId });
        if (active) setSnapshot(next ?? undefined);
      } catch (reason) {
        if (active) setError(message(reason));
      }
    }
    void refreshSnapshot();
    const interval = window.setInterval(refreshSnapshot, 1_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [droneId, nativeAvailable]);

  useEffect(() => {
    if (!nativeAvailable || !droneId) return;
    let active = true;
    let timer = 0;
    async function pullFrame() {
      try {
        const packet = await invoke<ArrayBuffer>("spatial_frame", {
          droneId,
          afterStreamEpoch: latestEpoch.current,
          afterSequence: latestSequence.current,
        });
        if (!active) return;
        const decoded = decodeSpatialFrame(packet);
        if (decoded) {
          latestSequence.current = decoded.metadata.sequence;
          latestEpoch.current = decoded.metadata.streamEpoch;
          setFrame(decoded);
          setError(undefined);
        }
      } catch (reason) {
        if (active) setError(message(reason));
      } finally {
        if (active) timer = window.setTimeout(pullFrame, 250);
      }
    }
    void pullFrame();
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [droneId, nativeAvailable]);

  const selectedAircraft = aircraft.find((item) => item.droneId === droneId);
  const status = !nativeAvailable ? "native-unavailable" : snapshot?.status ?? "waiting";
  const statusLabel = status === "connected" && frame ? "Live cloud" : label(status);
  const stale = snapshot?.status === "stale" || (frame && Date.now() - frame.metadata.receivedAtUnixMs > 3_000);

  function selectAircraft(nextDroneId: string) {
    setDroneId(nextDroneId);
    onSelectAircraft?.(nextDroneId);
  }

  return (
    <main className="indoor-page" id="main-content">
      <header className="indoor-page__header">
        <div>
          <p className="eyebrow">Local spatial awareness</p>
          <h1>Indoor explore</h1>
          <p>Inspect the complete current VIO-local map before indoor mission controls are introduced.</p>
        </div>
        <label className="indoor-page__aircraft">
          <span>Spatial aircraft</span>
          <select value={droneId} onChange={(event) => selectAircraft(event.target.value)} disabled={eligibleAircraft.length === 0}>
            {eligibleAircraft.length === 0 && <option value="">No spatial aircraft connected</option>}
            {eligibleAircraft.map((item) => (
              <option key={item.droneId} value={item.droneId ?? ""}>
                {item.droneName || item.droneId}
              </option>
            ))}
          </select>
        </label>
      </header>

      <section className="indoor-status" aria-label="Spatial stream status">
        <StatusMetric label="Stream" value={statusLabel} tone={stale || error ? "warning" : frame ? "positive" : "neutral"} />
        <StatusMetric label="Complete cloud" value={frame ? frame.metadata.pointCount.toLocaleString() : "No frame"} detail={`of ${maximumSpatialPoints.toLocaleString()} points`} />
        <StatusMetric label="Resolution" value={frame ? `${Math.round(frame.metadata.voxelSizeM * 100)} cm` : "—"} detail="voxel edge" />
        <StatusMetric label="Coordinate frame" value={frame?.metadata.frameId || "—"} detail={frame ? compact(frame.metadata.streamEpoch) : "Waiting for epoch"} />
        <StatusMetric label="Received" value={frame ? relativeTime(frame.metadata.receivedAtUnixMs) : "—"} detail={frame ? `snapshot ${frame.metadata.sequence}` : "No cloud received"} />
      </section>

      {error && <p className="indoor-page__error" role="alert">Spatial stream: {error}</p>}

      <section className="indoor-workspace" aria-label="Indoor spatial workspace">
        <article className="indoor-cloud">
          <header className="indoor-cloud__toolbar">
            <div>
              <span className={`indoor-cloud__state indoor-cloud__state--${stale ? "warning" : frame ? "live" : "waiting"}`} />
              <strong>{stale ? "Cloud stale" : frame ? "Complete map snapshot" : "Waiting for point cloud"}</strong>
            </div>
            <button type="button" onClick={() => setCameraRevision((value) => value + 1)}>Reset view</button>
          </header>
          <div className="indoor-cloud__viewport">
            {frame ? (
              <SpatialCanvas key={cameraRevision} frame={frame} />
            ) : (
              <div className="indoor-cloud__empty">
                <span aria-hidden="true" />
                <strong>{eligibleAircraft.length === 0 ? "No spatial-capable aircraft" : "Cloud not available yet"}</strong>
                <p>{eligibleAircraft.length === 0
                  ? "Connect an aircraft reporting the spatial complete-cloud capability."
                  : "Atlas has opened the view lease and will render the next complete map snapshot."}</p>
              </div>
            )}
            <div className="indoor-cloud__legend" aria-label="Map legend">
              <span><i className="indoor-cloud__legend-point" />Observed surface</span>
              <span><i className="indoor-cloud__legend-aircraft" />Aircraft pose</span>
              <span>Axes: X / Y ground · Z up</span>
            </div>
          </div>
        </article>

        <aside className="indoor-video" aria-label="Current aircraft video">
          <header>
            <div><p className="eyebrow">Visual cross-check</p><h2>Live camera</h2></div>
            <span>{selectedAircraft?.connectionStatus === "connected" ? "Linked" : "Unavailable"}</span>
          </header>
          <LiveVideo nativeAvailable={nativeAvailable} droneId={droneId || undefined} aircraft={selectedAircraft} compact />
        </aside>
      </section>
    </main>
  );
}

function SpatialCanvas({ frame }: { frame: SpatialCloudFrame }) {
  return (
    <Canvas
      camera={{ position: [5, -7, 4.5], fov: 48, near: 0.02, far: 250, up: [0, 0, 1] }}
      dpr={[1, 2]}
      frameloop="demand"
      gl={{ antialias: true, alpha: false, powerPreference: "high-performance" }}
    >
      <color attach="background" args={["#132019"]} />
      <fog attach="fog" args={["#132019", 18, 65]} />
      <ambientLight intensity={1.4} />
      <gridHelper args={[40, 80, "#496855", "#273e31"]} rotation={[Math.PI / 2, 0, 0]} />
      <axesHelper args={[1.25]} />
      <PointCloud positions={frame.positions} />
      {frame.metadata.pose && <AircraftPose pose={frame.metadata.pose} />}
      <OrbitControls makeDefault target={[0, 0, 0.5]} enableDamping dampingFactor={0.08} minDistance={0.5} maxDistance={80} />
    </Canvas>
  );
}

function PointCloud({ positions }: { positions: Float32Array }) {
  const geometry = useMemo(() => {
    const next = new BufferGeometry();
    next.setAttribute("position", new BufferAttribute(positions, 3));
    next.computeBoundingSphere();
    return next;
  }, [positions]);
  useEffect(() => () => geometry.dispose(), [geometry]);
  return (
    <points geometry={geometry} frustumCulled>
      <pointsMaterial color="#a8d6b2" size={0.035} sizeAttenuation transparent opacity={0.9} depthWrite={false} />
    </points>
  );
}

function AircraftPose({ pose }: { pose: NonNullable<SpatialCloudMetadata["pose"]> }) {
  return (
    <group position={[pose.x, pose.y, pose.z]} quaternion={[pose.qx, pose.qy, pose.qz, pose.qw]}>
      <mesh rotation={[Math.PI / 2, 0, 0]}>
        <coneGeometry args={[0.14, 0.38, 4]} />
        <meshStandardMaterial color="#ed9839" />
      </mesh>
      <mesh position={[0, 0, -0.05]}>
        <sphereGeometry args={[0.09, 12, 8]} />
        <meshStandardMaterial color="#f2d09b" />
      </mesh>
    </group>
  );
}

function StatusMetric({ label: metricLabel, value, detail, tone = "neutral" }: { label: string; value: string; detail?: string; tone?: "positive" | "warning" | "neutral" }) {
  return <div className={`indoor-status__metric indoor-status__metric--${tone}`}><small>{metricLabel}</small><strong>{value}</strong>{detail && <span>{detail}</span>}</div>;
}

function message(reason: unknown) { return reason instanceof Error ? reason.message : String(reason); }
function compact(value: string) { return value.length > 15 ? `${value.slice(0, 8)}…${value.slice(-5)}` : value; }
function label(value: string) { return value.split("-").map((part) => part[0]?.toUpperCase() + part.slice(1)).join(" "); }
function relativeTime(value: number) {
  const age = Math.max(0, Date.now() - value);
  if (age < 1_000) return "Now";
  if (age < 60_000) return `${Math.round(age / 1_000)}s ago`;
  return `${Math.round(age / 60_000)}m ago`;
}
