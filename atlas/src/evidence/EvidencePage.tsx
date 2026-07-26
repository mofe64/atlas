import { useEffect, useMemo, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import type {
  EvidenceAsset,
  EvidenceAssetType,
  EvidenceRetentionClass,
  EvidenceRetentionPolicy,
  EvidenceReviewState,
} from "./evidenceTypes";
import "./EvidencePage.css";

type EvidencePageProps = { nativeAvailable: boolean };
type AssetTypeFilter = "ALL" | EvidenceAssetType;
type ReviewFilter = "ALL" | EvidenceReviewState;

const reviewStates: EvidenceReviewState[] = ["UNREVIEWED", "RELEVANT", "NEEDS_FOLLOW_UP", "NOT_RELEVANT"];
const retentionClasses: EvidenceRetentionClass[] = ["STANDARD", "EXTENDED", "LEGAL_HOLD"];

export function EvidencePage({ nativeAvailable }: EvidencePageProps) {
  const [assets, setAssets] = useState<EvidenceAsset[]>([]);
  const [policy, setPolicy] = useState<EvidenceRetentionPolicy>();
  const [selectedId, setSelectedId] = useState<string>();
  const [assetType, setAssetType] = useState<AssetTypeFilter>("ALL");
  const [reviewFilter, setReviewFilter] = useState<ReviewFilter>("ALL");
  const [showTrash, setShowTrash] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [pendingAction, setPendingAction] = useState<string>();
  const [error, setError] = useState<string>();
  const [reviewNote, setReviewNote] = useState("");
  const [annotationType, setAnnotationType] = useState<"NOTE" | "TAG">("NOTE");
  const [annotationBody, setAnnotationBody] = useState("");
  const [trashReason, setTrashReason] = useState("");

  const selected = useMemo(() => assets.find((asset) => asset.id === selectedId), [assets, selectedId]);
  const visibleAssets = useMemo(() => {
    const query = searchQuery.trim().toLowerCase();
    if (!query) return assets;
    return assets.filter((asset) => [
      asset.id,
      asset.droneId,
      asset.incidentId,
      asset.missionId,
      asset.trackId,
      asset.deleteReason,
      ...asset.annotations.map((annotation) => annotation.body),
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
        const [nextAssets, nextPolicy] = await Promise.all([
          invoke<EvidenceAsset[]>("evidence_assets", {
            input: {
              assetType: assetType === "ALL" ? null : assetType,
              reviewState: reviewFilter === "ALL" ? null : reviewFilter,
              droneId: null,
              includeTrashed: showTrash,
              limit: 500,
            },
          }),
          invoke<EvidenceRetentionPolicy>("evidence_retention_policy"),
        ]);
        if (!active) return;
        setAssets(nextAssets);
        setPolicy(nextPolicy);
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
  }, [assetType, nativeAvailable, reviewFilter, showTrash]);

  function replaceAsset(asset: EvidenceAsset) {
    setAssets((current) => {
      const next = current.map((candidate) => candidate.id === asset.id ? asset : candidate);
      return next.some((candidate) => candidate.id === asset.id) ? next : [asset, ...next];
    });
    setSelectedId(asset.id);
  }

  async function mutate(label: string, operation: () => Promise<EvidenceAsset>) {
    if (pendingAction) return;
    setPendingAction(label);
    setError(undefined);
    try {
      replaceAsset(await operation());
    } catch (reason) {
      setError(messageFrom(reason));
    } finally {
      setPendingAction(undefined);
    }
  }

  async function review(reviewState: EvidenceReviewState) {
    if (!selected) return;
    await mutate("review", () => invoke<EvidenceAsset>("review_evidence_asset", {
      input: { assetId: selected.id, reviewState, note: reviewNote.trim(), actor: "operator" },
    }));
    setReviewNote("");
  }

  async function annotate() {
    if (!selected || !annotationBody.trim()) return;
    await mutate("annotate", () => invoke<EvidenceAsset>("annotate_evidence_asset", {
      input: { assetId: selected.id, annotationType, body: annotationBody.trim(), actor: "operator" },
    }));
    setAnnotationBody("");
  }

  async function changeRetention(retentionClass: EvidenceRetentionClass) {
    if (!selected) return;
    await mutate("retention", () => invoke<EvidenceAsset>("update_evidence_asset_retention", {
      input: { assetId: selected.id, retentionClass, actor: "operator" },
    }));
  }

  async function trash() {
    if (!selected || !trashReason.trim()) return;
    await mutate("trash", () => invoke<EvidenceAsset>("trash_evidence_asset", {
      input: { assetId: selected.id, reason: trashReason.trim(), actor: "operator" },
    }));
    setTrashReason("");
    setShowTrash(true);
  }

  async function restore() {
    if (!selected) return;
    await mutate("restore", () => invoke<EvidenceAsset>("restore_evidence_asset", {
      input: { assetId: selected.id, actor: "operator" },
    }));
  }

  async function savePolicy(next: EvidenceRetentionPolicy) {
    if (pendingAction) return;
    setPendingAction("policy");
    setError(undefined);
    try {
      const updated = await invoke<EvidenceRetentionPolicy>("update_evidence_retention_policy", {
        input: { ...next, actor: "operator" },
      });
      setPolicy(updated);
    } catch (reason) {
      setError(messageFrom(reason));
    } finally {
      setPendingAction(undefined);
    }
  }

  const readyCount = assets.filter((asset) => asset.status === "READY").length;
  const reviewQueue = assets.filter((asset) => asset.reviewState === "UNREVIEWED" && asset.status === "READY").length;
  const heldCount = assets.filter((asset) => asset.retentionClass === "LEGAL_HOLD").length;

  return (
    <main className="evidence-workspace" id="main-content">
      <header className="evidence-heading">
        <div>
          <p className="eyebrow">Local captures</p>
          <h1>Evidence</h1>
          <p>Review captures, record decisions, and manage local retention with source context intact.</p>
        </div>
        <div className="evidence-heading__facts" aria-label="Evidence summary">
          <span><small>Available</small><strong>{readyCount}</strong></span>
          <span><small>Review queue</small><strong>{reviewQueue}</strong></span>
          <span><small>Legal hold</small><strong>{heldCount}</strong></span>
        </div>
      </header>

      <section className="evidence-controls" aria-label="Evidence filters">
        <label className="evidence-search">
          <span>Search captures</span>
          <input type="search" value={searchQuery} onChange={(event) => setSearchQuery(event.target.value)} placeholder="Notes, tags, aircraft, incident" />
        </label>
        <div className="evidence-filter-group">
          {(["ALL", "STILL", "EVENT_CLIP"] as AssetTypeFilter[]).map((value) => (
            <button key={value} type="button" className={assetType === value ? "is-active" : undefined} onClick={() => setAssetType(value)}>
              {assetTypeLabel(value)}
            </button>
          ))}
        </div>
        <label>
          Review decision
          <select value={reviewFilter} onChange={(event) => setReviewFilter(event.target.value as ReviewFilter)}>
            <option value="ALL">All decisions</option>
            {reviewStates.map((state) => <option key={state} value={state}>{reviewStateLabel(state)}</option>)}
          </select>
        </label>
        <label className="evidence-trash-toggle">
          <input type="checkbox" checked={showTrash} onChange={(event) => setShowTrash(event.target.checked)} />
          Include deleted captures
        </label>
        {policy && <RetentionPolicy policy={policy} disabled={pendingAction != null} onSave={(next) => void savePolicy(next)} />}
      </section>

      {error && <p className="evidence-error" role="alert">{error}</p>}
      {!nativeAvailable && <p className="evidence-empty">Captures are unavailable while local services are offline. Reopen Atlas to continue.</p>}

      {nativeAvailable && (
        <div className="evidence-ledger">
          <aside className="evidence-index" aria-label="Captures">
            <header><span>Captures</span><small>{loading ? "Refreshing…" : `${visibleAssets.length} of ${assets.length}`}</small></header>
            <div className="evidence-index__list">
              {visibleAssets.map((asset) => (
                <button key={asset.id} type="button" className={selectedId === asset.id ? "is-selected" : undefined} onClick={() => setSelectedId(asset.id)}>
                  <EvidenceThumbnail asset={asset} />
                  <span className="evidence-index__copy">
                    <span><strong>{assetTypeLabel(asset.assetType)}</strong><em className={`status-${asset.status.toLowerCase()}`}>{humanize(asset.status)}</em></span>
                    <small>{formatDateTime(asset.capturedAtUnixMs)}</small>
                    <small>{asset.trackId ? `Track ${compactId(asset.trackId)}` : `Aircraft ${compactId(asset.droneId)}`}</small>
                    <span className="evidence-index__review">{reviewStateLabel(asset.reviewState)}</span>
                  </span>
                </button>
              ))}
              {!visibleAssets.length && !loading && (
                <div className="evidence-empty evidence-empty--index">
                  <strong>No matching evidence</strong>
                  <p>Capture a photo from Camera or mark a tracked event while source recording is running.</p>
                </div>
              )}
            </div>
          </aside>

          <section className="evidence-detail-grid" aria-live="polite">
            {selected ? (
              <>
              <section className="evidence-review evidence-review--source" aria-label="Selected capture source">
                <AssetPreview asset={selected} />
                <header className="evidence-review__identity">
                  <div>
                    <p className="eyebrow">{assetTypeLabel(selected.assetType)} · {formatDateTime(selected.capturedAtUnixMs)}</p>
                    <h2>{selected.trackId ? `Tracked ${compactId(selected.trackId)}` : `Aircraft ${compactId(selected.droneId)}`}</h2>
                  </div>
                  <span className={`evidence-state evidence-state--${selected.status.toLowerCase()}`}>{humanize(selected.status)}</span>
                </header>

                {selected.errorMessage && <p className="evidence-error">{selected.errorMessage}</p>}
                <dl className="evidence-provenance">
                  <div><dt>Aircraft</dt><dd>{selected.droneId}</dd></div>
                  <div><dt>Source</dt><dd>{selected.sourceId}</dd></div>
                  <div><dt>Observation</dt><dd>{formatDateTime(selected.sourceStartedAtUnixMs ?? selected.capturedAtUnixMs)}</dd></div>
                  <div><dt>Duration</dt><dd>{formatDuration(selected.sourceStartedAtUnixMs, selected.sourceEndedAtUnixMs)}</dd></div>
                  <div><dt>Recording</dt><dd>{selected.recordingSessionId ? compactId(selected.recordingSessionId) : "Standalone photo"}</dd></div>
                  <div><dt>Track record</dt><dd>{selected.trackSessionId ? `${compactId(selected.trackSessionId)} / ${compactId(selected.trackId)}` : "Not track-linked"}</dd></div>
                  <div><dt>Media</dt><dd>{formatBytes(selected.byteLength)} · local integrity recorded</dd></div>
                  <div><dt>Retention</dt><dd>{selected.retentionClass === "LEGAL_HOLD" ? "Legal hold · no expiry" : formatDateTime(selected.retainUntilUnixMs)}</dd></div>
                </dl>
              </section>

              <section className="evidence-review evidence-review--decisions" aria-label="Review and retention">
                {selected.status !== "TRASHED" && selected.status !== "PURGING" && selected.status !== "PURGED" && (
                  <div className="evidence-review__workbench">
                    <section>
                      <h3>Review decision</h3>
                      <textarea value={reviewNote} maxLength={2000} onChange={(event) => setReviewNote(event.target.value)} placeholder="Optional rationale saved with this decision" />
                      <div className="evidence-review-actions">
                        {reviewStates.map((state) => (
                          <button key={state} type="button" className={selected.reviewState === state ? "is-current" : undefined} disabled={pendingAction != null} onClick={() => void review(state)}>
                            {reviewStateLabel(state)}
                          </button>
                        ))}
                      </div>
                    </section>
                    <section>
                      <h3>Notes and tags</h3>
                      <div className="evidence-annotation-entry">
                        <select value={annotationType} onChange={(event) => setAnnotationType(event.target.value as "NOTE" | "TAG")}>
                          <option value="NOTE">Note</option>
                          <option value="TAG">Tag</option>
                        </select>
                        <input value={annotationBody} maxLength={2000} onChange={(event) => setAnnotationBody(event.target.value)} placeholder={annotationType === "TAG" ? "gate-entry" : "Observation about this media"} />
                        <button type="button" disabled={!annotationBody.trim() || pendingAction != null} onClick={() => void annotate()}>Add</button>
                      </div>
                      <div className="evidence-annotations">
                        {selected.annotations.map((annotation) => (
                          <p key={annotation.id}><span>{annotation.annotationType}</span>{annotation.body}<small>{annotation.createdBy} · {formatDateTime(annotation.createdAtUnixMs)}</small></p>
                        ))}
                        {!selected.annotations.length && <p className="evidence-muted">No capture notes or tags.</p>}
                      </div>
                    </section>
                  </div>
                )}

                <div className="evidence-custody">
                  <section>
                    <h3>Retention & deletion</h3>
                    {selected.status === "PURGING" ? (
                      <div className="evidence-trash-state">
                        <p>Deletion is in progress. The media can no longer be restored.</p>
                      </div>
                    ) : selected.status === "TRASHED" ? (
                      <div className="evidence-trash-state">
                        <p>Recoverable until <strong>{formatDateTime(selected.purgeAfterUnixMs)}</strong>. {selected.deleteReason}</p>
                        <button type="button" disabled={pendingAction != null} onClick={() => void restore()}>Restore capture</button>
                      </div>
                    ) : (
                      <>
                        <label>Retention
                          <select value={selected.retentionClass} disabled={pendingAction != null} onChange={(event) => void changeRetention(event.target.value as EvidenceRetentionClass)}>
                            {retentionClasses.map((value) => <option key={value} value={value}>{humanize(value)}</option>)}
                          </select>
                        </label>
                        <div className="evidence-trash-entry">
                          <input value={trashReason} maxLength={500} onChange={(event) => setTrashReason(event.target.value)} placeholder="Reason for recoverable deletion" />
                          <button type="button" disabled={selected.status !== "READY" || selected.retentionClass === "LEGAL_HOLD" || !trashReason.trim() || pendingAction != null} onClick={() => void trash()}>Delete (recoverable)</button>
                        </div>
                      </>
                    )}
                  </section>
                  <section>
                    <h3>Capture history</h3>
                    <ol className="evidence-timeline">
                      {[...selected.events].reverse().map((event) => (
                        <li key={event.id}><span>{event.eventType}</span><p>{event.message}</p><small>{event.actor} · {formatDateTime(event.occurredAtUnixMs)}</small></li>
                      ))}
                    </ol>
                  </section>
                </div>
              </section>
              </>
            ) : (
              <div className="evidence-empty evidence-empty--review"><strong>Select a capture</strong><p>Each capture keeps its media, source, decisions, and retention together.</p></div>
            )}
          </section>
        </div>
      )}
    </main>
  );
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
  if (asset.status === "PENDING") return <div className="evidence-preview evidence-preview--message"><span>Clip assembly queued</span><p>Atlas will publish this event only after checksum-verified recording segments cover the requested post-roll.</p></div>;
  if (asset.status === "FAILED") return <div className="evidence-preview evidence-preview--message"><span>Capture failed</span><p>{asset.errorMessage || "The failure remains in capture history."}</p></div>;
  if (asset.assetType === "STILL" && original) return <div className="evidence-preview"><img src={original} alt="Captured photo" /></div>;
  if (asset.assetType === "STILL") return <div className="evidence-preview evidence-preview--message"><span>Photo unavailable</span><p>The capture record is intact, but its media could not be loaded.</p></div>;
  if (asset.assetType === "EVENT_CLIP" && loadClip && original) return <div className="evidence-preview"><video src={original} controls preload="metadata" /></div>;
  return (
    <div className="evidence-preview evidence-preview--clip" style={thumbnail ? { backgroundImage: `linear-gradient(90deg, rgba(26,37,31,.28), rgba(26,37,31,.05)), url(${thumbnail})` } : undefined}>
      <button type="button" disabled={!thumbnail} onClick={() => setLoadClip(true)}><span aria-hidden="true">▶</span> Load clip</button>
    </div>
  );
}

function useEvidenceMedia(asset: EvidenceAsset, thumbnail: boolean, mimeType: string) {
  const [url, setUrl] = useState<string>();
  useEffect(() => {
    if (!matchesMediaState(asset.status) || (thumbnail ? !asset.thumbnailRelativePath : !asset.relativePath)) {
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

function RetentionPolicy({ policy, disabled, onSave }: { policy: EvidenceRetentionPolicy; disabled: boolean; onSave: (policy: EvidenceRetentionPolicy) => void }) {
  const [draft, setDraft] = useState(policy);
  useEffect(() => setDraft(policy), [policy]);
  return (
    <details className="retention-policy">
      <summary>Retention policy</summary>
      <div>
        <label><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /> Automatic retention</label>
        <label>Standard days<input type="number" min={1} max={3650} value={draft.defaultRetentionDays} onChange={(event) => setDraft({ ...draft, defaultRetentionDays: Number(event.target.value) })} /></label>
        <label>Extended days<input type="number" min={1} max={3650} value={draft.extendedRetentionDays} onChange={(event) => setDraft({ ...draft, extendedRetentionDays: Number(event.target.value) })} /></label>
        <label>Recovery window (days)<input type="number" min={1} max={365} value={draft.trashGraceDays} onChange={(event) => setDraft({ ...draft, trashGraceDays: Number(event.target.value) })} /></label>
        <button type="button" disabled={disabled} onClick={() => onSave(draft)}>Save policy</button>
      </div>
    </details>
  );
}

function matchesMediaState(status: EvidenceAsset["status"]) { return status === "READY" || status === "TRASHED"; }
function messageFrom(reason: unknown) { return reason instanceof Error ? reason.message : String(reason); }
function assetTypeLabel(value: AssetTypeFilter | EvidenceAssetType) {
  if (value === "ALL") return "Photos and clips";
  return value === "STILL" ? "Photo" : "Clip";
}
function reviewStateLabel(value: EvidenceReviewState) {
  const labels: Record<EvidenceReviewState, string> = {
    UNREVIEWED: "Undecided",
    RELEVANT: "Keep",
    NEEDS_FOLLOW_UP: "Needs follow-up",
    NOT_RELEVANT: "Not useful",
  };
  return labels[value];
}
function humanize(value: string) { return value.toLowerCase().replace(/_/g, " ").replace(/(^|\s)\S/g, (character) => character.toUpperCase()); }
function compactId(value?: string) { return value ? value.length > 14 ? `${value.slice(0, 7)}…${value.slice(-5)}` : value : "—"; }
function formatDateTime(value?: number) { return value == null ? "—" : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" }).format(value); }
function formatDuration(start?: number, end?: number) { return start == null || end == null ? "—" : end === start ? "Single frame" : `${((end - start) / 1_000).toFixed(1)} s`; }
function formatBytes(value: number) { if (!value) return "—"; const units = ["B", "KB", "MB", "GB"]; const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1); return `${(value / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`; }
