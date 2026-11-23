import { memo, useEffect, useRef, useState, type ClipboardEvent, type DragEvent, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Tray, type CandidateEntry, type TrayItem } from "../attach";
import { getCoreHost } from "../core/host";
import { renderText } from "../core/text";
import * as i18n from "../i18n";
import { Check, FileIcon, ImageIcon, Plus, X } from "../icons";
import { clock } from "../model";
import { useGrow } from "../motion";
import type { Remote } from "../remote";
import { Banner } from "../ui";
import { keepComposerFocus, useComposerMenuDismiss } from "./ComposerPicker";
import { FileChip } from "./Files";
import { DraftImageViewer } from "./Images";

export interface TrayCapabilities {
  sessionId?: string;
  maxImages: number;
  maxFiles: number;
  maxFileBytes: bigint | number;
  supportsFiles: boolean;
  galleryAcceptMime?: string;
  originalImageHint?: string;
  fileMetadataNotice?: string;
  blocked: string;
  fileBlocked?: string;
}

/** The tray of a draft (a session's input, or a project's new-session
 * card), kept on the Remote so it survives leaving the page. */
export function useTray(remote: Remote, key: string, machineId: string, caps: TrayCapabilities): Tray {
  const max = caps.maxImages > 0 ? caps.maxImages : 6;
  let t = remote.trays.get(key);
  const evaluate = (req: Parameters<NonNullable<Tray["evaluate"]>>[0]) =>
    getCoreHost(remote).evalAttachments(req);
  if (!t) {
    t = new Tray(
      remote.prepareImage,
      (m, p, progress, started) => remote.uploadImage(m, p, progress, started),
      () => { remote.persistDraft(key); remote.touch(); },
      max,
      evaluate,
    );
    t.machineId = machineId;
    remote.trays.set(key, t);
    void remote.restoreDraft(key, t).catch(() => undefined);
  }
  t.evaluate = evaluate;
  t.sessionId = caps.sessionId ?? "";
  t.max = max;
  t.fileSupported = caps.supportsFiles;
  t.maxFiles = caps.maxFiles > 0 ? caps.maxFiles : 6;
  t.maxFileBytes = Number(caps.maxFileBytes) > 0 ? Number(caps.maxFileBytes) : 100 * 1024 * 1024;
  t.galleryAcceptMime = caps.galleryAcceptMime || (caps.supportsFiles ? "image/*,video/*" : "image/*");
  t.originalImageHint = caps.originalImageHint ?? "";
  t.fileMetadataNotice = caps.fileMetadataNotice ?? "";
  t.fileBlocked = caps.fileBlocked ?? "";
  const tray = t;
  useEffect(() => { void remote.restoreDraft(key, tray).catch(() => undefined); }, [remote, key, tray]);
  useEffect(() => {
    tray.retarget(machineId);
  }, [tray, machineId]);
  const deadlines = tray.items.map((i) => i.expiresMs ?? 0).join(",");
  const online = !caps.blocked;
  useEffect(() => {
    if (!online) return;
    const refresh = () => { tray.refreshExpired(); remote.touch(); };
    refresh();
    const next = Math.min(...tray.items.filter((i) => i.state === "done" && i.expiresMs! > Date.now()).map((i) => i.expiresMs!));
    const timer = Number.isFinite(next) ? setTimeout(refresh, Math.min(2147483647, Math.max(1, next - Date.now()))) : undefined;
    addEventListener("focus", refresh);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      clearTimeout(timer);
      removeEventListener("focus", refresh);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [tray, remote, online, deadlines, tray.items.length]);
  return tray;
}

/** Files in a drop, paste, or file input change event. */
export function attachmentFiles(list: FileList | DataTransferItemList | null | undefined): File[] {
  if (!list) return [];
  const out: File[] = [];
  for (const x of Array.from(list as ArrayLike<File | DataTransferItem>)) {
    const f = x instanceof File ? x : (x as DataTransferItem).kind === "file" ? (x as DataTransferItem).getAsFile() : null;
    if (f) out.push(f);
  }
  return out;
}

function filesToCandidates(files: readonly File[]): CandidateEntry[] {
  return files.map((f) => ({
    file: f,
    name: f.name,
    mimeType: f.type,
    sizeBytes: f.size,
  }));
}

/** ＋ opens the library at once; when the machine offers file uploads it
 * opens a small menu above it instead: 照片和视频, 文件. */
export function AttachButton({ tray, blocked, onBlocked, className = "cm-plus", label = <Plus /> }: {
  tray: Tray; blocked: string; onBlocked: (why: string) => void; className?: string; label?: ReactNode;
}) {
  const input = useRef<HTMLInputElement>(null);
  const files = useRef<HTMLInputElement>(null);
  const wrap = useRef<HTMLSpanElement>(null);
  const [open, setOpen] = useState(false);
  useComposerMenuDismiss(open, wrap, () => setOpen(false));
  const pick = (file: boolean) => {
    setOpen(false);
    const reason = tray.checkPickBlocked(file, blocked);
    if (reason) return onBlocked(reason);
    (file ? files : input).current?.click();
  };
  return <span className="attach-wrap" ref={wrap}>
    <button disabled={tray.restoring} className={className + (blocked || (!tray.fileSupported && tray.full()) ? " off" : "")} aria-label={tray.fileSupported ? i18n.t("new_session.ios_attach") : i18n.t("image.add")} aria-expanded={tray.fileSupported ? open : undefined}
      onPointerDown={keepComposerFocus} onMouseDown={keepComposerFocus} onClick={() => blocked ? onBlocked(blocked) : tray.fileSupported ? setOpen(!open) : pick(false)}>{label}</button>
    {open && (
      <div className="attach-menu glass" role="menu">
        <button role="menuitem" onClick={() => pick(false)}><ImageIcon />{i18n.t("attachment.photos_videos")}</button>
        <button role="menuitem" onClick={() => pick(true)}><FileIcon />{i18n.t("attachment.file")}</button>
        <p>{tray.fileMetadataNotice || i18n.t("attachment.metadata")}</p>
      </div>
    )}
    <FilePickers tray={tray} accept={tray.galleryAcceptMime} media={input} files={files} />
  </span>;
}

/** The hidden inputs the button opens. React sets an input's type and name
 * again each time it draws it, which the browser sees as a change of
 * the page: they are drawn apart from the button, which draws with every
 * keystroke in the message, and only when something they depend on changes. */
const FilePickers = memo(function FilePickers({ tray, accept, media, files }: { tray: Tray; accept: string; media: RefObject<HTMLInputElement | null>; files: RefObject<HTMLInputElement | null> }) {
  return <>
    <input ref={media} type="file" accept={accept} multiple hidden onChange={(e) => { tray.addCandidates(filesToCandidates(attachmentFiles(e.currentTarget.files)), false); e.currentTarget.value = ""; }} />
    <input ref={files} type="file" multiple hidden onChange={(e) => { tray.addCandidates(filesToCandidates(attachmentFiles(e.currentTarget.files)), true); e.currentTarget.value = ""; }} />
  </>;
});

function Thumb({ item, index, count, onOpen, onRemove }: { item: TrayItem; index: number; count: number; onOpen: () => void; onRemove: () => void }) {
  const [url, setUrl] = useState<string>();
  useEffect(() => {
    if (!item.thumb) return;
    const u = URL.createObjectURL(item.thumb);
    setUrl(u);
    return () => URL.revokeObjectURL(u);
  }, [item.thumb]);
  const busy = item.state === "preparing" || item.state === "uploading";
  const problemText = renderText(item.problem?.message);
  const state = item.state === "failed" ? i18n.t("attachment.failure", { reason: problemText })
    : item.state === "preparing" ? i18n.t("image.preparing") : item.state === "uploading" ? i18n.t("image.uploading") : i18n.t("image.uploaded");
  return (
    <span className="draft-thumb">
      <button className="pic tthumb draft-open" onClick={onOpen} aria-label={i18n.t("image.position_state", { index, count, state })}>
      {url && <img src={url} alt="" />}
      {busy && item.progress > 0 && item.progress < 1 ? (
        <span className="pct">
          <b style={{ width: `${Math.round(item.progress * 100)}%` }} />
        </span>
      ) : (
        busy && <span className="ring spin" aria-label={i18n.t("attachment.uploading")} />
      )}
      {item.state === "failed" && (
        <span className="badge" aria-hidden="true">{i18n.t("attachment.failed")}</span>
      )}
      </button>
      <button className="x" onClick={onRemove} aria-label={i18n.t("image.remove_index", { index })}>
        <span className="draft-remove-mark"><X /></span>
      </button>
    </span>
  );
}

/** A chosen video in the tray: its first frame with its length in a
 * corner, or, when neither can be read, the file icon and its name. */
function VideoThumb({ item, onRemove, onRetry }: { item: TrayItem; onRemove: () => void; onRetry: () => void }) {
  const [url, setUrl] = useState<string>();
  const [length, setLength] = useState<number | "unknown">();
  useEffect(() => {
    const u = URL.createObjectURL(item.file);
    setUrl(u);
    setLength(undefined);
    return () => URL.revokeObjectURL(u);
  }, [item.file]);
  const busy = item.state === "preparing" || item.state === "uploading";
  const read = typeof length === "number";
  const label = item.displayName || item.name || "";
  return (
    <span className={"pic tthumb video" + (length === "unknown" ? " plain" : "")} title={label}>
      {url && length !== "unknown" && (
        <video
          src={url + "#t=0.1"}
          muted
          playsInline
          preload="metadata"
          aria-hidden
          onLoadedMetadata={(e) => {
            const d = e.currentTarget.duration;
            setLength(Number.isFinite(d) ? d : "unknown");
          }}
          onError={() => setLength("unknown")}
        />
      )}
      {length === "unknown" && (
        <span className="vname">
          <FileIcon />
          <span>{label}</span>
        </span>
      )}
      {read && <span className="dur">{clock(length * 1000)}</span>}
      {busy && item.progress > 0 && item.progress < 1 ? (
        <span className="pct">
          <b style={{ width: `${Math.round(item.progress * 100)}%` }} />
        </span>
      ) : (
        busy && <span className="ring spin" aria-label={i18n.t("attachment.uploading")} />
      )}
      {item.state === "failed" && (
        <button className="badge retry" onClick={onRetry} title={item.error}>
          {i18n.t("common.retry")}</button>
      )}
      <button className="x" onClick={onRemove} aria-label={i18n.t("attachment.remove_video", { name: label }).trim()}>
        <X />
      </button>
    </span>
  );
}

function mb(n: number): string {
  return n < 1024 * 1024 ? `${Math.max(1, Math.round(n / 1024))} KB` : `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** The chosen images above the text: each with its upload, 原图 for this
 * message, and how many of how many. */
export function TrayView({ tray, note, remote }: { tray: Tray; note?: ReactNode; remote?: Remote }) {
  if (tray.items.length === 0 && !tray.note) return <>{note}</>;
  return <Attachments tray={tray} note={note} remote={remote} />;
}

/** The tray itself: it opens to its height as the first picture comes and
 * closes up as the last goes, the input moving with it. */
function Attachments({ tray, note, remote }: { tray: Tray; note?: ReactNode; remote?: Remote }) {
  const ref = useRef<HTMLDivElement>(null);
  const [viewing, setViewing] = useState<number>();
  useGrow(ref);
  const images = tray.images();
  // Videos from the library sit among the images, in the order chosen;
  // other files are chips.
  const thumbs = tray.items.filter((i) => i.kind !== "file" || i.isVideo);
  const files = tray.files().filter((i) => !i.isVideo);
  const failed = images.filter((i) => i.state === "failed");
  const failedFiles = tray.files().filter((i) => i.state === "failed");
  const failureSummary = renderText(tray.failureSummary);
  return (
    <div className="attachments" ref={ref}>
      {thumbs.length > 0 && (
        <div className="att-tray">
          {thumbs.map((i) =>
            i.kind === "file" ? (
              <VideoThumb key={i.key} item={i} onRemove={() => tray.remove(i.key)} onRetry={() => tray.retry(i.key)} />
            ) : (
              <Thumb key={i.key} item={i} index={images.indexOf(i) + 1} count={images.length} onOpen={() => setViewing(images.indexOf(i))} onRemove={() => tray.remove(i.key)} />
            ),
          )}
        </div>
      )}
      {images.length > 0 && (
        <>
          <div className="att-meta">
            <button
              className={"orig" + (tray.original ? " on" : "")}
              role="switch"
              aria-checked={tray.original}
              disabled={!tray.canSwitchOriginal()}
              title={tray.canSwitchOriginal() ? undefined : i18n.t("image.original_missing")}
              onClick={() => tray.setOriginal(!tray.original)}
            >
              <i>{tray.original && <Check />}</i>
              {i18n.t("image.original")}{tray.original && tray.totalBytes() > 0 ? i18n.t("image.total_size", { size: mb(tray.totalBytes()) }) : ""}
            </button>
            <span>
              {images.length} / {tray.max}
            </span>
          </div>
        </>
      )}
      {tray.original && images.length > 0 && <div className="att-hint">{tray.originalImageHint || i18n.t("image.original_hint")}</div>}
      {files.length > 0 && (
        <div className="file-chips">
          {files.map((i) => (
            <FileChip
              key={i.key}
              file={{ name: i.name!, displayName: i.displayName, sizeText: i.formattedSize }}
              state={i.state}
              progress={i.progress}
              error={i.error}
              onRetry={() => tray.retry(i.key)}
              onRemove={() => tray.remove(i.key)}
            />
          ))}
        </div>
      )}
      {tray.files().length > 0 && <div className="att-hint">{tray.fileMetadataNotice || i18n.t("attachment.metadata")}</div>}
      {failedFiles.length > 0 && <Banner kind="err">{i18n.t("attachment.failed_send_hint", { reason: failedFiles[0].error ?? "" })}</Banner>}
      {failed.length > 0 && failureSummary && (
        <Banner kind="err">
          {failureSummary}
        </Banner>
      )}
      {tray.note && <Banner>{tray.note}</Banner>}
      {note}
      {viewing !== undefined && remote && createPortal(<DraftImageViewer remote={remote} tray={tray} start={viewing} onClose={() => setViewing(undefined)} />, document.body)}
    </div>
  );
}

/** Drop and paste on a card: candidates are routed and validated synchronously via Core AttachmentEval. */
export function useDropPaste(tray: Tray, blocked: string, onBlocked: (why: string) => void) {
  const [over, setOver] = useState(false);
  const takeEntries = (entries: CandidateEntry[]) => {
    if (!entries.length) return false;
    if (blocked) {
      onBlocked(blocked);
      return true;
    }
    const hadDirectory = entries.some((e) => e.isDirectory);
    const notice = tray.addCandidates(entries, false);
    if (hadDirectory && notice) {
      tray.note = "";
      onBlocked(notice);
    }
    return true;
  };
  return {
    over,
    props: {
      onDragOver: (e: DragEvent) => {
        if ([...e.dataTransfer.types].includes("Files")) {
          e.preventDefault();
          setOver(true);
        }
      },
      onDragLeave: () => setOver(false),
      onDrop: (e: DragEvent) => {
        setOver(false);
        const items = Array.from(e.dataTransfer.items ?? []);
        const entries: CandidateEntry[] = items.length
          ? items
              .filter((item) => item.kind === "file")
              .map((item) => {
                const isDirectory = !!item.webkitGetAsEntry?.()?.isDirectory;
                const file = isDirectory ? undefined : item.getAsFile() ?? undefined;
                return {
                  file,
                  name: file?.name ?? "folder",
                  mimeType: file?.type ?? "",
                  sizeBytes: file?.size ?? 0,
                  isDirectory,
                };
              })
              .filter((entry) => entry.isDirectory || entry.file)
          : filesToCandidates(attachmentFiles(e.dataTransfer.files));
        if (takeEntries(entries)) e.preventDefault();
      },
      onPaste: (e: ClipboardEvent) => {
        if (takeEntries(filesToCandidates(attachmentFiles(e.clipboardData.items)))) e.preventDefault();
      },
    },
  };
}
