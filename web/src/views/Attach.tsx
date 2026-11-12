import * as i18n from "../i18n";
// Choosing images for a message: the ＋ button (the system's picker:
// library, camera, files), dropping or pasting on wide screens, and the
// tray of chosen images above the text with each one's upload. A computer
// that takes files also gets videos from the library: they go as files
// (session-files.md 从相册选视频), drawn in the tray among the images.

import { memo, useEffect, useRef, useState, type ClipboardEvent, type DragEvent, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { keepComposerFocus, useComposerMenuDismiss } from "./ComposerPicker";
import { Tray, type TrayItem } from "../attach";
import { imageFailureLine, imageProblem, originalImageHint } from "../image-problem";
import { DraftImageViewer } from "./Images";
import { clock, machineName } from "../model";
import type { MachineView, Remote } from "../remote";
import { Banner } from "../ui";
import { useGrow } from "../motion";
import { FileChip } from "./Files";
import { Check, FileIcon, ImageIcon, Plus, X } from "../icons";

/** The tray of a draft (a session's input, or a project's new-session
 * card), kept on the Remote so it survives leaving the page. */
export function useTray(remote: Remote, key: string, machineId: string, max: number): Tray {
  let t = remote.trays.get(key);
  if (!t) {
    t = new Tray(remote.prepareImage, (m, p, progress, started) => remote.uploadImage(m, p, progress, started), () => { remote.persistDraft(key); remote.touch(); }, max);
    t.machineId = machineId;
    remote.trays.set(key, t);
    void remote.restoreDraft(key, t).catch(() => undefined);
  }
  t.max = max;
  const caps = remote.machines.get(machineId)?.meta?.capabilities;
  t.fileSupported = !!caps?.fileUpload;
  t.maxFiles = caps?.maxFilesPerMessage || 6;
  t.maxFileBytes = Number(caps?.maxFileUploadBytes || 100n * 1024n * 1024n);
  const tray = t;
  useEffect(() => { void remote.restoreDraft(key, tray).catch(() => undefined); }, [remote, key, tray]);
  useEffect(() => {
    tray.retarget(machineId);
  }, [tray, machineId]);
  const deadlines = tray.items.map((i) => i.expiresMs ?? 0).join(",");
  const online = !attachBlocked(remote, remote.machines.get(machineId));
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

/** Why images cannot be added now, or "". */
export function attachBlocked(remote: Remote, m: MachineView | undefined): string {
  const name = machineName(m);
  if (!m?.online) return i18n.t("attachment.computer_offline", { name });
  if (remote.conn !== "online") return i18n.t("attachment.disconnected");
  if (!m.meta) return i18n.t("attachment.computer_unread", { name });
  if (!m.meta.capabilities?.imageUpload) return i18n.t("attachment.path_spaces", { name });
  return "";
}

export function maxImages(m: MachineView | undefined): number {
  return m?.meta?.capabilities?.maxImagesPerMessage || 6;
}

/** Image files in a drop or a paste. */
export function attachmentFiles(list: FileList | DataTransferItemList | null | undefined): File[] {
  if (!list) return [];
  const out: File[] = [];
  for (const x of Array.from(list as ArrayLike<File | DataTransferItem>)) {
    const f = x instanceof File ? x : (x as DataTransferItem).kind === "file" ? (x as DataTransferItem).getAsFile() : null;
    if (f) out.push(f);
  }
  return out;
}

/** Whether a chosen file is a video, which goes as a file and shows in
 * the tray beside the images. */
export function isVideo(f: Blob): boolean {
  return f.type.startsWith("video/");
}

/** What the library picker offers: images, and videos too when the
 * computer takes files. */
export function mediaAccept(fileSupported: boolean): string {
  return fileSupported ? "image/*,video/*" : "image/*";
}

/** Files chosen from the library into the tray: images as images, the
 * rest (videos) as files, which checks their size and count as it takes
 * them, and says so when the computer cannot take files. */
export function addMedia(tray: Tray, files: File[]) {
  const images = files.filter((f) => f.type.startsWith("image/"));
  const other = files.filter((f) => !f.type.startsWith("image/"));
  if (images.length) tray.add(images);
  if (other.length) tray.addFiles(other);
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
    if (blocked) return onBlocked(blocked);
    const filesFull = tray.files().length >= tray.maxFiles;
    // The library is full only when neither another image nor a video fits.
    if (file ? filesFull : tray.full() && (!tray.fileSupported || filesFull)) return onBlocked(file ? i18n.t("attachment.max_files", { count: tray.maxFiles }) : i18n.t("attachment.max_images", { count: tray.max }));
    (file ? files : input).current?.click();
  };
  return <span className="attach-wrap" ref={wrap}>
    <button disabled={tray.restoring} className={className + (blocked || !tray.fileSupported && tray.full() ? " off" : "")} aria-label={tray.fileSupported ? i18n.t("new_session.ios_attach") : i18n.t("image.add")} aria-expanded={tray.fileSupported ? open : undefined}
      onPointerDown={keepComposerFocus} onMouseDown={keepComposerFocus} onClick={() => blocked ? onBlocked(blocked) : tray.fileSupported ? setOpen(!open) : pick(false)}>{label}</button>
    {open && (
      <div className="attach-menu glass" role="menu">
        <button role="menuitem" onClick={() => pick(false)}><ImageIcon />{i18n.t("attachment.photos_videos")}</button>
        <button role="menuitem" onClick={() => pick(true)}><FileIcon />{i18n.t("attachment.file")}</button>
        <p>{i18n.t("attachment.metadata")}</p>
      </div>
    )}
    <FilePickers tray={tray} fileSupported={tray.fileSupported} media={input} files={files} />
  </span>;
}

/** The hidden inputs the button opens. React sets an input's type and name
 * again each time it draws it, which the browser sees as a change of
 * the page: they are drawn apart from the button, which draws with every
 * keystroke in the message, and only when something they depend on changes. */
const FilePickers = memo(function FilePickers({ tray, fileSupported, media, files }: { tray: Tray; fileSupported: boolean; media: RefObject<HTMLInputElement | null>; files: RefObject<HTMLInputElement | null> }) {
  return <>
    <input ref={media} type="file" accept={mediaAccept(fileSupported)} multiple hidden onChange={(e) => { addMedia(tray, attachmentFiles(e.currentTarget.files)); e.currentTarget.value = ""; }} />
    <input ref={files} type="file" multiple hidden onChange={(e) => { tray.addFiles(attachmentFiles(e.currentTarget.files)); e.currentTarget.value = ""; }} />
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
  const state = item.state === "failed" ? i18n.t("attachment.failure", { reason: imageProblem(item.error ?? "").message })
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
  return (
    <span className={"pic tthumb video" + (length === "unknown" ? " plain" : "")} title={item.name}>
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
          <span>{item.name}</span>
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
      <button className="x" onClick={onRemove} aria-label={i18n.t("attachment.remove_video", { name: item.name ?? "" }).trim()}>
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
  const thumbs = tray.items.filter((i) => i.kind !== "file" || isVideo(i.file));
  const files = tray.files().filter((i) => !isVideo(i.file));
  const failed = images.filter((i) => i.state === "failed");
  const failedFiles = tray.files().filter((i) => i.state === "failed");
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
      {tray.original && images.length > 0 && <div className="att-hint">{originalImageHint()}</div>}
      {files.length > 0 && <div className="file-chips">{files.map((i) => <FileChip key={i.key} file={{ name: i.name!, sizeBytes: BigInt(i.bytes ?? 0) }} state={i.state} progress={i.progress} error={i.error} onRetry={() => tray.retry(i.key)} onRemove={() => tray.remove(i.key)} />)}</div>}
      {tray.files().length > 0 && <div className="att-hint">{i18n.t("attachment.metadata")}</div>}
      {failedFiles.length > 0 && <Banner kind="err">{i18n.t("attachment.failed_send_hint", { reason: failedFiles[0].error ?? "" })}</Banner>}
      {failed.length > 0 && (
        <Banner kind="err">
          {imageFailureLine(failed.map(i => i.error ?? ""))}
        </Banner>
      )}
      {tray.note && <Banner>{tray.note}</Banner>}
      {note}
      {viewing !== undefined && remote && createPortal(<DraftImageViewer remote={remote} tray={tray} start={viewing} onClose={() => setViewing(undefined)} />, document.body)}
    </div>
  );
}

/** Drop and paste on a card: images join the tray. */
export function useDropPaste(tray: Tray, blocked: string, onBlocked: (why: string) => void) {
  const [over, setOver] = useState(false);
  const take = (files: File[]) => {
    if (!files.length) return false;
    if (blocked) onBlocked(blocked);
    else {
      const images = files.filter((f) => f.type.startsWith("image/"));
      const other = files.filter((f) => !f.type.startsWith("image/"));
      if (images.length) tray.add(images);
      if (other.length) tray.addFiles(other);
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
        const folders = items.filter((item) => item.kind === "file" && item.webkitGetAsEntry?.()?.isDirectory);
        if (folders.length) {
          e.preventDefault();
          onBlocked(i18n.t("attachment.zip_folder"));
          const files = items.filter((item) => !folders.includes(item));
          take(attachmentFiles(files as unknown as DataTransferItemList));
        } else if (take(attachmentFiles(e.dataTransfer.files))) e.preventDefault();
      },
      onPaste: (e: ClipboardEvent) => {
        if (take(attachmentFiles(e.clipboardData.items))) e.preventDefault();
      },
    },
  };
}
