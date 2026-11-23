import * as i18n from "../i18n";
import { t } from "../i18n";
// Pictures in a session (client-v2 会话里的图片): previews under messages
// and tool rows, the card of a generated image, and the viewer that
// fetches the display copy on demand. Viewer indexing, paging, source
// labels, paths, and empty state come from Core's SessionImageViewerView.

import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import type { Tray, TrayItem } from "../attach";
import { renderText } from "../core/text";
import type {
  SessionImageViewerView,
  SessionViewerImageView,
} from "../gen/remoteagent/core/v1/session_view_pb";
import type { ImageRef } from "../gen/remoteagent/v1/event_pb";
import { ImageFormat, ImageSync } from "../gen/remoteagent/v1/event_pb";
import { ChevronLeft, ChevronRight, X } from "../icons";
import { matchesFormat, mimeOf } from "../imageview";
import { hhmm } from "../model";
import { errorText, type ImageResult, type Remote } from "../remote";
import { saveBlobAsDownload } from "../shared-files";
import { Exp, GlassButton, useDialogFocus, useEscape, useRemote } from "../ui";
import { useRowExpanded } from "./RowState";

export interface ImageAnchor {
  seq: bigint;
  imageIndex: number;
}

export function viewerImageKey(seq: bigint, imageIndex: number): string {
  return `${seq}:${imageIndex}`;
}

/** Opens the session image viewer anchored to an event image; provided by SessionScreenView. */
export const OpenImage = createContext<(anchor: ImageAnchor) => void>(
  () => undefined,
);

/** Map of (seq, eventImageIndex) -> Core SessionViewerImageView for chat thumbnails and GenCard. */
export const ViewerImagesMeta = createContext<ReadonlyMap<string, SessionViewerImageView>>(
  new Map(),
);

/** A blob: URL for bytes, made while mounted and revoked after. Made in
 * an effect, not during render, so no URL outlives what shows it. */
export function useBlobUrl(
  bytes: Uint8Array | undefined,
  type: string,
): string | undefined {
  const [url, setUrl] = useState<string>();
  useEffect(() => {
    if (!bytes || !bytes.length || !type) return setUrl(undefined);
    const u = URL.createObjectURL(
      new Blob([bytes as Uint8Array<ArrayBuffer>], { type }),
    );
    setUrl(u);
    return () => URL.revokeObjectURL(u);
  }, [bytes, type]);
  return url;
}

/** The URL of a blob while mounted. */
function useObjectUrl(blob: Blob | undefined): string | undefined {
  const [shown, setShown] = useState<{ blob: Blob; url: string }>();
  useEffect(() => {
    if (!blob) return setShown(undefined);
    const u = URL.createObjectURL(blob);
    setShown({ blob, url: u });
    return () => URL.revokeObjectURL(u);
  }, [blob]);
  return shown?.blob === blob ? shown?.url : undefined;
}

/** A ref's embedded preview, if it is what it claims to be. */
function usePreview(ref: ImageRef): string | undefined {
  const ok =
    ref.preview.length > 0 && matchesFormat(ref.preview, ref.previewFormat);
  return useBlobUrl(ok ? ref.preview : undefined, mimeOf(ref.previewFormat));
}

/** One preview, laid out at its own proportions before it loads. */
export function Pic({
  image: r,
  meta,
  height = 96,
  number,
  badge,
  onOpen,
  className = "",
}: {
  image: ImageRef;
  meta?: SessionViewerImageView;
  height?: number;
  number?: number;
  badge?: string;
  onOpen?: () => void;
  className?: string;
}) {
  const url = usePreview(r);
  const aspect = height
    ? (meta?.clampedAspectRatio || meta?.aspectRatio || 4 / 3)
    : (meta?.aspectRatio || 4 / 3);
  const style: CSSProperties = height
    ? previewGeometry(aspect, height)
    : { aspectRatio: String(aspect) };
  const name = meta?.displayName || (r.path ? r.path.split(/[\\/]/).pop() || r.path : "");
  if (r.sync === ImageSync.NAME_ONLY || (!url && r.sync !== ImageSync.SYNCED))
    return (
      <button
        className={"pic miss " + className}
        style={
          height
            ? { ...style, width: Math.max(style.width as number, 120) }
            : style
        }
        onClick={onOpen}
        aria-label={i18n.t("image.unsynced")}
      >
        {i18n.t("image.unsynced")}{name && <code>{name}</code>}
      </button>
    );
  return (
    <button
      className={"pic " + className}
      style={style}
      onClick={onOpen}
      aria-label={name ? i18n.t("image.named", { name }) : i18n.t("image.title")}
    >
      {url && <img src={url} alt="" draggable={false} />}
      {number ? <span className="n">{number}</span> : null}
      {(badge ?? (r.sync === ImageSync.PREVIEW_ONLY ? i18n.t("image.preview_only") : "")) && (
        <span className="badge">{badge ?? i18n.t("image.preview_only")}</span>
      )}
    </button>
  );
}

/** A row of previews: at most four, the rest folded into +N. */
export function ImageStrip({
  refs,
  seq = 0n,
  anchors,
  numbered,
  className = "",
  height,
}: {
  refs: ImageRef[];
  seq?: bigint;
  anchors?: readonly ImageAnchor[];
  numbered?: boolean;
  className?: string;
  height?: number;
}) {
  const open = useContext(OpenImage);
  const metaMap = useContext(ViewerImagesMeta);
  if (!refs.length) return null;
  const shown = refs.slice(0, 4);
  const more = refs.length - shown.length;
  return (
    <span className={"pics " + className}>
      {shown.map((r, i) => {
        const anchor = anchors?.[i] ?? { seq, imageIndex: i };
        const meta = metaMap.get(viewerImageKey(anchor.seq, anchor.imageIndex));
        return (
          <span key={i} className="picwrap">
            <Pic
              image={r}
              meta={meta}
              height={height}
              number={numbered && r.cliIndex ? r.cliIndex : undefined}
              onOpen={() => open(anchor)}
            />
            {more > 0 && i === shown.length - 1 && (
              <button
                className="more"
                onClick={() => open(anchor)}
                aria-label={i18n.t("image.more", { count: more })}
              >
                +{more}
              </button>
            )}
          </span>
        );
      })}
    </span>
  );
}

/** An image the agent made: full width, with its rewritten prompt. */
export function GenCard({
  refs,
  seq = 0n,
  prompt,
}: {
  refs: ImageRef[];
  seq?: bigint;
  prompt: string;
}) {
  const open = useContext(OpenImage);
  const metaMap = useContext(ViewerImagesMeta);
  const [shown, setShown] = useRowExpanded("image-prompt");
  return (
    <>
      {refs.map((r, i) => {
        const anchor = { seq, imageIndex: i };
        const meta = metaMap.get(viewerImageKey(seq, i));
        const label = renderText(meta?.sourceLabel) || i18n.t("image.generated");
        const size = meta?.sizeText ?? "";
        return (
          <div key={i} className="gen-card">
            <Pic image={r} meta={meta} className="lg" height={0} onOpen={() => open(anchor)} />
            <div className="meta">
              <span>{label}{size && ` · ${size}`}</span>
              {prompt && i === 0 && (
                <button
                  className="link"
                  onClick={() => setShown(!shown)}
                  aria-expanded={shown}
                >
                  {i18n.t("image.prompt")}<Exp up={shown} />
                </button>
              )}
            </div>
            {shown && i === 0 && <div className="gen-prompt">{prompt}</div>}
          </div>
        );
      })}
    </>
  );
}

const NO_REF = {
  preview: new Uint8Array(),
  previewFormat: 0,
} as unknown as ImageRef;

type Load =
  | {
      kind:
        | "idle"
        | "loading"
        | "offline"
        | "error"
        | "preview-only"
        | "name-only";
      error?: string;
    }
  | ImageResult;

/** Fetches the display copy of the picture shown, once per picture. */
function useDisplay(
  remote: Remote | undefined,
  sid: string,
  img: SessionViewerImageView | undefined,
  attempt: number,
  local?: TrayItem,
): Load {
  useRemote(remote);
  const [state, setState] = useState<Load>({ kind: "idle" });
  const localLoad = useMemo<Load | undefined>(() => {
    if (!local) return;
    const p = local.prepared;
    if (!p || p.format === "file") return { kind: local.state === "failed" ? "error" : "loading" };
    const format = { jpeg: ImageFormat.JPEG, png: ImageFormat.PNG, webp: ImageFormat.WEBP, gif: ImageFormat.GIF }[p.format];
    return { kind: "ok", blob: new Blob([p.bytes as Uint8Array<ArrayBuffer>], { type: mimeOf(format) }), width: p.width, height: p.height, format };
  }, [local?.key, local?.prepared, local?.state]);
  const imageId = img?.ref?.imageId ?? "";
  const sync = img?.ref?.sync ?? ImageSync.UNSPECIFIED;
  const hasDisplayCopy = !!img?.hasDisplayCopy;
  useEffect(() => {
    if (local) return;
    if (!img?.ref) return;
    if (sync === ImageSync.NAME_ONLY) return setState({ kind: "name-only" });
    if (!hasDisplayCopy || !imageId) return setState({ kind: "preview-only" });
    if (!remote) return setState({ kind: "idle" });
    const cached = remote.cachedImage(sid, imageId);
    if (cached) return setState(cached);
    if (remote.conn !== "online") return setState({ kind: "offline" });
    let alive = true;
    setState({ kind: "loading" });
    remote.getImage(sid, imageId).then(
      (res) => alive && setState(res),
      (e) =>
        alive &&
        setState({
          kind: remote.conn === "online" ? "error" : "offline",
          error: errorText(e),
        }),
    );
    return () => {
      alive = false;
    };
    // Offline, it waits for the connection and then fetches by itself.
  }, [remote, sid, local ? undefined : imageId, sync, hasDisplayCopy, attempt, remote?.conn === "online"]); // eslint-disable-line react-hooks/exhaustive-deps
  return localLoad ?? state;
}

/** What to say over the picture while its display copy is not shown. */
function loadNote(l: Load): string {
  switch (l.kind) {
    case "loading":
      return "";
    case "offline":
      return i18n.t("image.load_connected");
    case "error":
      return i18n.t("image.download_retry");
    case "expired":
      return i18n.t("image.original_expired");
    case "missing":
      return i18n.t("image.original_not_uploaded");
    case "bad":
      return l.why ?? i18n.t("image.verification_failed");
    case "preview-only":
      return i18n.t("image.original_on_computer");
    case "name-only":
      return i18n.t("image.unsynced");
    case "ok":
      return "";
  }
  return "";
}

/** The viewer: Core-driven session pictures (via SessionImageViewerView),
 * a downloaded shared-file image, or local draft pictures. */
export function ImageViewer({
  remote,
  sid = "",
  viewer,
  previewRef,
  title = "",
  wide,
  onNavigate,
  onClose,
  draft,
  draftStart = 0,
  sharedFile,
}: {
  remote?: Remote;
  sid?: string;
  viewer?: SessionImageViewerView;
  previewRef?: ImageRef;
  title?: string;
  wide: boolean;
  onNavigate?: (delta: number) => void;
  onClose: () => void;
  draft?: Tray;
  draftStart?: number;
  sharedFile?: { blob: Blob; name: string; path: string; sizeText: string };
}) {
  const [draftIdx, setDraftIdx] = useState(draftStart);
  const [attempt, setAttempt] = useState(0);
  const [note, setNote] = useState("");
  const [zoom, setZoom] = useState(1);

  const draftImages = draft?.images() ?? [];
  const draftIndex = draftImages.length > 0 ? Math.min(draftIdx, draftImages.length - 1) : 0;
  const local = draft ? draftImages[draftIndex] : undefined;
  const problem = local?.state === "failed" ? local.problem : undefined;

  const img = !sharedFile && !draft ? viewer?.currentImage : undefined;
  const hasPrev = draft ? draftIndex > 0 : !!viewer?.hasPrev;
  const hasNext = draft ? draftIndex < draftImages.length - 1 : !!viewer?.hasNext;
  const positionText = draft
    ? `${draftIndex + 1} / ${draftImages.length}`
    : renderText(viewer?.positionText);
  const activeKey = sharedFile
    ? `sf:${sharedFile.name}`
    : draft
      ? `draft:${draftIndex}`
      : `session:${viewer?.currentIndex ?? 0}`;

  useEffect(() => {
    setZoom(1);
    setNote("");
  }, [activeKey]);

  const fetched = useDisplay(remote, sid, img, attempt, local);
  const load: Load = sharedFile
    ? { kind: "ok", blob: sharedFile.blob, width: 0, height: 0, format: ImageFormat.UNSPECIFIED }
    : fetched;
  const full = useObjectUrl(load.kind === "ok" ? load.blob : undefined);
  const preview = usePreview(previewRef ?? img?.ref ?? NO_REF);

  const step = (d: number) => {
    if (d < 0 && !hasPrev) return;
    if (d > 0 && !hasNext) return;
    setNote("");
    if (draft) {
      setDraftIdx((x) => Math.max(0, Math.min(draftImages.length - 1, x + d)));
    } else {
      onNavigate?.(d);
    }
  };

  useEscape(onClose);
  const box = useRef<HTMLDivElement>(null);
  useDialogFocus(box);
  useEffect(() => {
    if (sharedFile) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "ArrowLeft") step(-1);
      else if (e.key === "ArrowRight") step(1);
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  });

  // Swipes: sideways to page (dispatched on pointerup/settle), down to close. Two fingers zoom.
  const touch = useRef<{ x: number; y: number; d?: number } | undefined>(undefined);

  if (!sharedFile && !draft && (!viewer || viewer.totalCount === 0 || !img)) {
    const emptyText = renderText(viewer?.emptyText) || i18n.t("image.viewer_empty");
    return (
      <div
        className={wide ? "lightbox" : "viewer"}
        role="dialog"
        aria-modal="true"
        aria-label={i18n.t("image.view")}
        ref={box}
      >
        <div className={wide ? "lb-top" : "vbar"}>
          {!wide && (
            <GlassButton label={i18n.t("common.close")} onClick={onClose}>
              <X />
            </GlassButton>
          )}
          <span>{emptyText}</span>
          {wide ? (
            <GlassButton label={i18n.t("common.close")} onClick={onClose}>
              <X />
            </GlassButton>
          ) : (
            <span />
          )}
        </div>
        <div className="vimg">
          <div className="vnote">{emptyText}</div>
        </div>
      </div>
    );
  }
  if (draft && !local) return null;

  const sourceLabel = draft
    ? i18n.t("image.draft")
    : renderText(img?.sourceLabel);
  const name = sharedFile
    ? sharedFile.name
    : draft
      ? (local?.file instanceof File ? local.file.name : `image-${draftIndex + 1}.jpg`)
      : img?.displayName || `image-${(viewer?.currentIndex ?? 0) + 1}.${img?.fileExtension || "jpg"}`;
  const blob = load.kind === "ok" ? load.blob : undefined;
  const save = () => {
    // Agent-sent files are always saved as application/octet-stream, never with the preview MIME.
    if (sharedFile) return saveBlobAsDownload(sharedFile.blob, sharedFile.name);
    if (!full) return;
    const a = document.createElement("a");
    a.href = full;
    a.download = name;
    a.click();
  };
  const file = blob ? new File([blob], name, { type: blob.type }) : undefined;
  const canShare =
    !!file &&
    typeof navigator.canShare === "function" &&
    navigator.canShare({ files: [file] });
  const share = async () => {
    if (!file) return;
    try {
      await navigator.share({ files: [file] });
    } catch (e) {
      if ((e as Error).name !== "AbortError")
        setNote(i18n.t("image.share_failed", { reason: errorText(e) }));
    }
  };
  const copy = async () => {
    if (!blob) return;
    try {
      // Clipboards take PNG; other formats are converted first.
      const png = blob.type === "image/png" ? blob : await toPng(blob);
      await navigator.clipboard.write([
        new ClipboardItem({ "image/png": png }),
      ]);
      setNote(t("common.copied"));
    } catch (e) {
      setNote(i18n.t("image.copy_failed", { reason: errorText(e) }));
    }
  };
  const status = local ? "" : loadNote(load);
  const retryable = load.kind === "error";
  const draftActions = local && draft && <>
    {problem?.retryable && <button className="glass" onClick={() => draft.retry(local.key)}>{t("common.retry")}</button>}
    <button className="glass" onClick={() => { draft.remove(local.key); if (!draft.images().length) onClose(); }}>{t("common.remove")}</button>
  </>;
  const picture = (
    <div
      className="vimg"
      onPointerDown={(e) => (touch.current = { x: e.clientX, y: e.clientY })}
      onPointerUp={(e) => {
        const pt = touch.current;
        touch.current = undefined;
        if (!pt || zoom > 1) return;
        const dx = e.clientX - pt.x;
        const dy = e.clientY - pt.y;
        if (!sharedFile && Math.abs(dx) > 60 && Math.abs(dx) > Math.abs(dy))
          step(dx < 0 ? 1 : -1);
        else if (!wide && dy > 90) onClose();
      }}
      onTouchMove={(e) => {
        if (e.touches.length !== 2) return;
        const d = Math.hypot(
          e.touches[0].clientX - e.touches[1].clientX,
          e.touches[0].clientY - e.touches[1].clientY,
        );
        const pt = touch.current ?? { x: 0, y: 0 };
        if (pt.d) setZoom((z) => Math.max(1, Math.min(4, (z * d) / pt.d!)));
        touch.current = { ...pt, d };
      }}
      onDoubleClick={() => setZoom((z) => (z > 1 ? 1 : 2))}
    >
      {(full || preview) && (
        <img
          src={full ?? preview}
          alt=""
          style={{ transform: `scale(${zoom})` }}
          draggable={false}
          className={full ? "" : "soft"}
        />
      )}
      {load.kind === "loading" && !problem && <span className="ring" aria-label={i18n.t("common.loading")} />}
      {status && (
        <button
          className="vnote"
          onClick={retryable ? () => setAttempt((a) => a + 1) : undefined}
          disabled={!retryable}
        >
          {status}
        </button>
      )}
    </div>
  );
  const tsMs = img ? Number(img.timestampMs) : 0;
  const metaTail = [img?.sizeText ?? "", title, tsMs ? hhmm(tsMs) : ""].filter(Boolean).join(" · ");
  const info = (() => {
    if (sharedFile) {
      return (
        <span>
          {sharedFile.name}
          {sharedFile.path && (
            <>
              {" · "}
              <code>{sharedFile.path}</code>
            </>
          )}
          {" · "}
          {sharedFile.sizeText}
        </span>
      );
    }
    if (local) {
      return (
        <>
          <b>{i18n.t("image.position", { index: draftIndex + 1, count: draftImages.length })}</b>
          {local.file instanceof File && <div>{local.file.name}</div>}
          <div>{(problem && renderText(problem.message)) || (local.state === "preparing" ? i18n.t("image.preparing") : local.state === "uploading" ? i18n.t("image.uploading") : i18n.t("image.draft"))}</div>
        </>
      );
    }
    return (
      <>
        {sourceLabel}
        {img?.displayPath && (
          <>
            {" · "}
            <code>{img.displayPath}</code>
          </>
        )}
        {metaTail && (
          <>
            {" · "}
            {metaTail}
          </>
        )}
      </>
    );
  })();
  // Dark in both themes, its buttons dark glass (design-system §2, §4).
  if (wide)
    return (
      <div
        className="lightbox"
        role="dialog"
        aria-modal="true"
        aria-label={i18n.t("image.view")}
        ref={box}
      >
        <div className="lb-top">
          <span>
            {sharedFile ? sharedFile.name : `${positionText} · ${sourceLabel}`}
          </span>
          <GlassButton label={i18n.t("common.close")} onClick={onClose}>
            <X />
          </GlassButton>
        </div>
        <div className="lb-mid">
          {!sharedFile && (
            <GlassButton label={i18n.t("image.previous")} onClick={() => step(-1)} disabled={!hasPrev}>
              <ChevronLeft />
            </GlassButton>
          )}
          <div className="lb-img">{picture}</div>
          {!sharedFile && (
            <GlassButton label={i18n.t("image.next")} onClick={() => step(1)} disabled={!hasNext}>
              <ChevronRight />
            </GlassButton>
          )}
        </div>
        <div className="lb-foot">
          <span className="lb-info">{info}</span>
          {note && <span className="lb-note">{note}</span>}
          {draft ? draftActions : <>
            <button className="glass" onClick={save} disabled={!full}>
              {t("common.save")}
            </button>
            <button
              className="glass"
              onClick={() => void copy()}
              disabled={!blob || typeof ClipboardItem === "undefined"}
            >
              {t("common.copy")}
            </button>
            <button className="glass" onClick={onClose}>{i18n.t("image.back_message")}</button>
          </>}
        </div>
      </div>
    );
  return (
    <div
      className="viewer"
      role="dialog"
      aria-modal="true"
      aria-label={i18n.t("image.view")}
      ref={box}
    >
      <div className="vbar">
        <GlassButton label={i18n.t("common.close")} onClick={onClose}>
          <X />
        </GlassButton>
        <b>
          {sharedFile ? sharedFile.name : positionText}
        </b>
        <span />
      </div>
      {picture}
      <div className="vinfo">
        {sharedFile && (
          <div>
            <b>{sharedFile.name}</b>
            {sharedFile.path && <code>{sharedFile.path}</code>}
            <div>{sharedFile.sizeText}</div>
          </div>
        )}
        {!sharedFile && local && info}
        {!sharedFile && !local && img && (
          <div>
            <b>{sourceLabel}</b>
            {img.displayPath && <code>{img.displayPath}</code>}
            {metaTail && <div>{metaTail}</div>}
            {img.revisedPrompt && <div className="vprompt">{img.revisedPrompt}</div>}
          </div>
        )}
        {note && <div>{note}</div>}
      </div>
      <div className="vact">
        {draft ? draftActions : <>
          <button className="glass" onClick={save} disabled={!full}>
            {t("common.save")}
          </button>
          {canShare && <button className="glass" onClick={() => void share()}>{i18n.t("image.share")}</button>}
          <button className="glass" onClick={onClose}>{i18n.t("image.back_message")}</button>
        </>}
      </div>
    </div>
  );
}

/** Draft and message pictures use the same zooming, paging and dialog. Only
 * their source/available actions differ; drafts never fetch a server image. */
export function DraftImageViewer({ remote, tray, start, onClose }: { remote: Remote; tray: Tray; start: number; onClose: () => void }) {
  return (
    <ImageViewer
      remote={remote}
      title={i18n.t("image.draft")}
      wide={matchMedia("(min-width: 900px)").matches}
      onClose={onClose}
      draft={tray}
      draftStart={start}
    />
  );
}

async function toPng(blob: Blob): Promise<Blob> {
  const bmp = await createImageBitmap(blob);
  const c = document.createElement("canvas");
  c.width = bmp.width;
  c.height = bmp.height;
  c.getContext("2d")!.drawImage(bmp, 0, 0);
  bmp.close();
  return new Promise((res, rej) =>
    c.toBlob((b) => (b ? res(b) : rej(new Error(i18n.t("image.convert_failed")))), "image/png"),
  );
}

/** This device's copies of images on their way: shown until the CLI
 * records the message with its own previews. */
export function previewGeometry(aspect: number, height = 96): { width: number; height: number } {
  return { width: Math.round(height * Math.min(3, Math.max(0.4, aspect))), height };
}

export function LocalThumbs({ blobs, sizes }: { blobs?: Blob[]; sizes?: { width: number; height: number }[] }) {
  const [urls, setUrls] = useState<string[]>([]);
  useEffect(() => {
    const us = (blobs ?? []).map((b) => URL.createObjectURL(b));
    setUrls(us);
    return () => us.forEach((u) => URL.revokeObjectURL(u));
  }, [blobs]);
  if (!urls.length) return null;
  return (
    <span className="pics">
      {urls.slice(0, 4).map((u, i) => (
        <span key={u} className="picwrap">
          <span className="pic local" style={previewGeometry(sizes?.[i]?.height ? sizes[i].width / sizes[i].height : 4 / 3)}>
            <img src={u} alt={i18n.t("image.numbered", { index: i + 1 })} />
            <span className="n">{i + 1}</span>
          </span>
          {i === 3 && urls.length > 4 && <span className="more" aria-label={i18n.t("image.more", { count: urls.length - 4 })}>+{urls.length - 4}</span>}
        </span>
      ))}
    </span>
  );
}
