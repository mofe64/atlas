import { useEffect, useMemo, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import type { EvidenceAsset, EvidenceAssetType } from "./evidenceTypes";
import "./EvidencePage.css";

type EvidencePageProps = { nativeAvailable: boolean };
type AssetTypeFilter = "ALL" | EvidenceAssetType;

export function EvidencePage({ nativeAvailable }: EvidencePageProps) {
  const [assets, setAssets] = useState<EvidenceAsset[]>([]);
  const [selectedId, setSelectedId] = useState<string>();
  const [assetType, setAssetType] = useState<AssetTypeFilter>("ALL");
  const [searchQuery, setSearchQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>();

  const selected = useMemo(() => assets.find((asset) => asset.id === selectedId), [assets, selectedId]);
  const visibleAssets = useMemo(() => {
    const query = searchQuery.trim().toLowerCase();
    if (!query) return assets;
    return assets.filter((asset) => [
      asset.id,
      asset.droneId,
      asset.droneName,
      asset.incidentId,
      asset.missionId,
      asset.trackId,
      asset.trackClassLabel,
      asset.sourceId,
    ].some((value) => value?.toLowerCase().includes(query)));
  }, [assets, searchQuery]);

  useEffect(() => {
    if (!nativeAvailable) {
      setLoading(false);
      return;
    }
    let active = true;
    async function refresh(silent = false) {
      if (!silent) setLoading(true);
      try {
        const nextAssets = await invoke<EvidenceAsset[]>("evidence_assets", {
          input: {
            assetType: assetType === "ALL" ? null : assetType,
            droneId: null,
            limit: 500,
          },
        });
        if (!active) return;
        setAssets(nextAssets);
        setError(undefined);
        setSelectedId((current) => current && nextAssets.some((asset) => asset.id === current) ? current : nextAssets[0]?.id);
      } catch (reason) {
        if (active) setError(messageFrom(reason));
      } finally {
        if (active) setLoading(false);
      }
    }
    void refresh();
    const interval = window.setInterval(() => void refresh(true), 3_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [assetType, nativeAvailable]);

  const readyCount = assets.filter((asset) => asset.status === "READY").length;
  const photoCount = assets.filter((asset) => asset.assetType === "STILL").length;
  const clipCount = assets.filter((asset) => asset.assetType === "EVENT_CLIP").length;

  return (
    <main className="evidence-workspace" id="main-content">
      <header className="evidence-heading">
        <div>
          <p className="eyebrow">Camera output</p>
          <h1>Captures</h1>
          <p>Open photos and clips captured from live camera operations.</p>
        </div>
        <div className="evidence-heading__facts" aria-label="Capture summary">
          <span><small>Available</small><strong>{readyCount}</strong></span>
          <span><small>Photos</small><strong>{photoCount}</strong></span>
          <span><small>Clips</small><strong>{clipCount}</strong></span>
        </div>
      </header>

      <section className="evidence-controls" aria-label="Capture filters">
        <label className="evidence-search">
          <span>Search captures</span>
          <input type="search" value={searchQuery} onChange={(event) => setSearchQuery(event.target.value)} placeholder="Aircraft, mission, incident, or target" />
        </label>
        <div className="evidence-filter-group" role="group" aria-label="Capture type">
          {(["ALL", "STILL", "EVENT_CLIP"] as AssetTypeFilter[]).map((value) => (
            <button key={value} type="button" className={assetType === value ? "is-active" : undefined} aria-pressed={assetType === value} onClick={() => setAssetType(value)}>
              {assetTypeLabel(value)}
            </button>
          ))}
        </div>
      </section>

      {error && <p className="evidence-error" role="alert">{error}</p>}
      {!nativeAvailable && <CaptureEmpty title="Captures unavailable" body="Reopen Atlas to reconnect local camera services." />}

      {nativeAvailable && (
        <div className="evidence-browser">
          <aside className="evidence-index" aria-label="Captures">
            <header><span>Recent captures</span><small>{loading ? "Refreshing…" : `${visibleAssets.length} shown`}</small></header>
            <div className="evidence-index__list">
              {visibleAssets.map((asset) => (
                <button key={asset.id} type="button" className={selectedId === asset.id ? "is-selected" : undefined} aria-pressed={selectedId === asset.id} onClick={() => setSelectedId(asset.id)}>
                  <EvidenceThumbnail asset={asset} />
                  <span className="evidence-index__copy">
                    <span><strong>{assetTypeLabel(asset.assetType)}</strong><em className={`status-${asset.status.toLowerCase()}`}>{humanize(asset.status)}</em></span>
                    <small>{captureProvenance(asset)}</small>
                    <small>{captureContext(asset)}</small>
                  </span>
                </button>
              ))}
              {!visibleAssets.length && !loading && (
                <CaptureEmpty title={assets.length ? "No matching captures" : "No captures yet"} body={assets.length ? "Change the search or capture type." : "Capture a photo from Camera, or mark a tracked event while recording to create a clip."} compact />
              )}
            </div>
          </aside>

          <section className="evidence-detail" aria-live="polite">
            {selected ? <CaptureDetail asset={selected} /> : <CaptureEmpty title="Select a capture" body="Its media and camera context will appear here." />}
          </section>
        </div>
      )}
    </main>
  );
}

function CaptureDetail({ asset }: { asset: EvidenceAsset }) {
  return (
    <article className="capture-detail">
      <AssetPreview asset={asset} />
      <header className="capture-detail__identity">
        <div>
          <p className="eyebrow">{assetTypeLabel(asset.assetType)} · {formatDateTime(asset.capturedAtUnixMs)}</p>
          <h2>{captureSubject(asset)}</h2>
          <p className="capture-detail__provenance">{captureProvenance(asset)}</p>
        </div>
        <span className={`evidence-state evidence-state--${asset.status.toLowerCase()}`}>{humanize(asset.status)}</span>
      </header>

      {asset.errorMessage && <p className="evidence-error">{asset.errorMessage}</p>}
      <dl className="capture-summary">
        <div><dt>Aircraft</dt><dd>{aircraftLabel(asset)}</dd></div>
        <div><dt>Captured</dt><dd>{formatDateTime(asset.capturedAtUnixMs)}</dd></div>
        <div><dt>Duration</dt><dd>{formatDuration(asset.sourceStartedAtUnixMs, asset.sourceEndedAtUnixMs)}</dd></div>
        <div><dt>Context</dt><dd>{captureContext(asset)}</dd></div>
        <div><dt>Media</dt><dd>{formatBytes(asset.byteLength)} · {asset.mimeType || "Processing"}</dd></div>
      </dl>

      <details className="capture-technical-details">
        <summary>Details</summary>
        <dl>
          <div><dt>Capture ID</dt><dd>{asset.id}</dd></div>
          <div><dt>Aircraft ID</dt><dd>{asset.droneId}</dd></div>
          <div><dt>Camera source</dt><dd>{asset.sourceId}</dd></div>
          <div><dt>Recording session ID</dt><dd>{detailValue(asset.recordingSessionId, "Standalone photo")}</dd></div>
          <div><dt>Incident ID</dt><dd>{detailValue(asset.incidentId)}</dd></div>
          <div><dt>Mission ID</dt><dd>{detailValue(asset.missionId)}</dd></div>
          <div><dt>Mission run ID</dt><dd>{detailValue(asset.missionRunId)}</dd></div>
          <div><dt>Selection ID</dt><dd>{detailValue(asset.selectionId)}</dd></div>
          <div><dt>Track ID</dt><dd>{detailValue(asset.trackId)}</dd></div>
          <div><dt>Track session ID</dt><dd>{detailValue(asset.trackSessionId)}</dd></div>
          <div><dt>Evidence marker ID</dt><dd>{detailValue(asset.evidenceMarkerAnnotationId)}</dd></div>
          <div><dt>Captured timestamp</dt><dd>{formatExactTime(asset.capturedAtUnixMs)}</dd></div>
          <div><dt>Source frame start</dt><dd>{formatExactTime(asset.sourceStartedAtUnixMs)}</dd></div>
          <div><dt>Source frame end</dt><dd>{formatExactTime(asset.sourceEndedAtUnixMs)}</dd></div>
          <div><dt>Requested clip start</dt><dd>{formatExactTime(asset.requestedStartAtUnixMs)}</dd></div>
          <div><dt>Requested clip end</dt><dd>{formatExactTime(asset.requestedEndAtUnixMs)}</dd></div>
          <div><dt>Media path</dt><dd>{detailValue(asset.relativePath, "Pending")}</dd></div>
          <div><dt>Thumbnail path</dt><dd>{detailValue(asset.thumbnailRelativePath, "Pending")}</dd></div>
          <div><dt>Media bytes</dt><dd>{asset.byteLength || "Pending"}</dd></div>
          <div><dt>Thumbnail bytes</dt><dd>{asset.thumbnailByteLength || "Pending"}</dd></div>
          <div><dt>SHA-256</dt><dd>{asset.sha256 || "Pending"}</dd></div>
          <div><dt>Thumbnail SHA-256</dt><dd>{asset.thumbnailSha256 || "Pending"}</dd></div>
          <div><dt>Created by</dt><dd>{asset.createdBy}</dd></div>
          <div><dt>Created timestamp</dt><dd>{formatExactTime(asset.createdAtUnixMs)}</dd></div>
          <div><dt>Updated timestamp</dt><dd>{formatExactTime(asset.updatedAtUnixMs)}</dd></div>
        </dl>
      </details>
    </article>
  );
}

function CaptureEmpty({ title, body, compact = false }: { title: string; body: string; compact?: boolean }) {
  return <div className={`evidence-empty${compact ? " evidence-empty--compact" : ""}`}><strong>{title}</strong><p>{body}</p></div>;
}

function EvidenceThumbnail({ asset }: { asset: EvidenceAsset }) {
  const url = useEvidenceMedia(asset, true, asset.thumbnailMimeType || "image/jpeg");
  if (url) return <img className="evidence-thumbnail" src={url} alt="" />;
  return <span className={`evidence-thumbnail evidence-thumbnail--${asset.status.toLowerCase()}`}>{asset.status === "PENDING" ? "QUEUED" : asset.status === "FAILED" ? "FAILED" : "MEDIA"}</span>;
}

function AssetPreview({ asset }: { asset: EvidenceAsset }) {
  const thumbnail = useEvidenceMedia(asset, true, asset.thumbnailMimeType || "image/jpeg");
  const [loadClip, setLoadClip] = useState(false);
  const original = useEvidenceMedia(asset, asset.assetType === "EVENT_CLIP" ? !loadClip : false, asset.mimeType || (asset.assetType === "STILL" ? "image/jpeg" : "video/mp4"));
  useEffect(() => setLoadClip(false), [asset.id]);
  if (asset.status === "PENDING") return <div className="evidence-preview evidence-preview--message"><span>Preparing capture</span><p>The media will appear after local processing completes.</p></div>;
  if (asset.status === "FAILED") return <div className="evidence-preview evidence-preview--message"><span>Capture failed</span><p>{asset.errorMessage || "Atlas could not create this capture."}</p></div>;
  if (asset.assetType === "STILL" && original) return <div className="evidence-preview"><img src={original} alt="Captured photo" /></div>;
  if (asset.assetType === "STILL") return <div className="evidence-preview evidence-preview--message"><span>Photo unavailable</span><p>The local media could not be loaded.</p></div>;
  if (loadClip && original) return <div className="evidence-preview"><video src={original} controls preload="metadata" /></div>;
  return (
    <div className="evidence-preview evidence-preview--clip" style={thumbnail ? { backgroundImage: `linear-gradient(90deg, rgba(26,37,31,.28), rgba(26,37,31,.05)), url(${thumbnail})` } : undefined}>
      <button type="button" disabled={!thumbnail} onClick={() => setLoadClip(true)}><span aria-hidden="true">▶</span> Load clip</button>
    </div>
  );
}

function useEvidenceMedia(asset: EvidenceAsset, thumbnail: boolean, mimeType: string) {
  const [url, setUrl] = useState<string>();
  useEffect(() => {
    if (asset.status !== "READY" || (thumbnail ? !asset.thumbnailRelativePath : !asset.relativePath)) {
      setUrl(undefined);
      return;
    }
    let active = true;
    let objectUrl: string | undefined;
    invoke<ArrayBuffer>("evidence_asset_content", { assetId: asset.id, thumbnail })
      .then((bytes) => {
        if (!active) return;
        objectUrl = URL.createObjectURL(new Blob([bytes], { type: mimeType }));
        setUrl(objectUrl);
      })
      .catch(() => { if (active) setUrl(undefined); });
    return () => {
      active = false;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [asset.id, asset.status, asset.thumbnailRelativePath, asset.relativePath, mimeType, thumbnail]);
  return url;
}

function messageFrom(reason: unknown) { return reason instanceof Error ? reason.message : String(reason); }
function assetTypeLabel(value: AssetTypeFilter | EvidenceAssetType) { return value === "ALL" ? "All captures" : value === "STILL" ? "Photo" : "Clip"; }
function humanize(value: string) { return value.toLowerCase().replace(/_/g, " ").replace(/(^|\s)\S/g, (character) => character.toUpperCase()); }
function compactId(value?: string) { return value ? value.length > 14 ? `${value.slice(0, 7)}…${value.slice(-5)}` : value : "—"; }
function captureContext(asset: EvidenceAsset) { return asset.incidentId ? `Incident ${compactId(asset.incidentId)}` : asset.missionId ? `Mission ${compactId(asset.missionId)}` : asset.trackId ? `Target ${compactId(asset.trackId)}` : `Aircraft ${compactId(asset.droneId)}`; }
function aircraftLabel(asset: EvidenceAsset) { return asset.droneName || `Aircraft ${compactId(asset.droneId)}`; }
function captureSubject(asset: EvidenceAsset) { return asset.trackId ? `${humanize(asset.trackClassLabel || "target")} track` : assetTypeLabel(asset.assetType); }
function captureProvenance(asset: EvidenceAsset) { return `${captureSubject(asset)} · ${aircraftLabel(asset)} · captured ${formatClock(asset.capturedAtUnixMs)}`; }
function formatDateTime(value?: number) { return value == null ? "—" : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" }).format(value); }
function formatClock(value: number) { return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" }).format(value); }
function formatExactTime(value?: number) { return value == null ? "Not linked" : new Date(value).toISOString(); }
function formatDuration(start?: number, end?: number) { return start == null || end == null ? "—" : end === start ? "Single frame" : `${((end - start) / 1_000).toFixed(1)} s`; }
function formatBytes(value: number) { if (!value) return "—"; const units = ["B", "KB", "MB", "GB"]; const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1); return `${(value / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`; }
function detailValue(value?: string, fallback = "Not linked") { return value || fallback; }
