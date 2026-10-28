// Choosing images for a message: the ＋ button (the system's picker:
// library, camera, files), dropping or pasting on wide screens, and the
// tray of chosen images above the text with each one's upload.

import { useEffect, useRef, useState, type ClipboardEvent, type DragEvent, type ReactNode } from "react";
import { Tray, type TrayItem } from "../attach";
import { machineName } from "../model";
import type { MachineView, Remote } from "../remote";
import { Banner } from "../ui";
import { FileChip } from "./Files";
import { Plus, X } from "../icons";

/** The tray of a draft (a session's input, or a project's new-session
 * card), kept on the Remote so it survives leaving the page. */
export function useTray(remote: Remote, key: string, machineId: string, max: number): Tray {
  let t = remote.trays.get(key);
  if (!t) {
    t = new Tray(remote.prepareImage, (m, p, progress, started) => remote.uploadImage(m, p, progress, started), () => remote.touch(), max);
    t.machineId = machineId;
    remote.trays.set(key, t);
  }
  t.max = max;
  const caps = remote.machines.get(machineId)?.meta?.capabilities;
  t.fileSupported = !!caps?.fileUpload;
  t.maxFiles = caps?.maxFilesPerMessage || 6;
  t.maxFileBytes = Number(caps?.maxFileUploadBytes || 100n * 1024n * 1024n);
  const tray = t;
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
  if (!m?.online) return `${name} 离线，连上后才能添加图片`;
  if (remote.conn !== "online") return "和服务器断线了，连上后才能添加图片";
  if (!m.meta) return `还没读到 ${name} 的信息，稍后才能添加图片`;
  if (!m.meta.capabilities?.imageUpload) return `${name} 上 remote-agent 的目录路径带空格，不能发图片`;
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

export function imageFiles(list: FileList | DataTransferItemList | null | undefined): File[] {
  return attachmentFiles(list).filter((f) => f.type.startsWith("image/"));
}

/** A quiet two-choice menu only when the machine offers file uploads. */
export function AttachButton({ tray, blocked, onBlocked, className = "cm-plus", label = <Plus /> }: {
  tray: Tray; blocked: string; onBlocked: (why: string) => void; className?: string; label?: ReactNode;
}) {
  const input = useRef<HTMLInputElement>(null);
  const files = useRef<HTMLInputElement>(null);
  const wrap = useRef<HTMLSpanElement>(null);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (!open) return;
    const close = (e: PointerEvent) => { if (!wrap.current?.contains(e.target as Node)) setOpen(false); };
    const escape = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false); };
    document.addEventListener("pointerdown", close); document.addEventListener("keydown", escape);
    return () => { document.removeEventListener("pointerdown", close); document.removeEventListener("keydown", escape); };
  }, [open]);
  const pick = (file: boolean) => {
    setOpen(false);
    if (blocked) return onBlocked(blocked);
    if (file ? tray.files().length >= tray.maxFiles : tray.full()) return onBlocked(file ? `一条消息最多 ${tray.maxFiles} 个文件` : `一条消息最多 ${tray.max} 张`);
    (file ? files : input).current?.click();
  };
  return <span className="attach-wrap" ref={wrap}>
    <button className={className + (blocked || !tray.fileSupported && tray.full() ? " off" : "")} aria-label={tray.fileSupported ? "添加附件" : "添加图片"} aria-expanded={tray.fileSupported ? open : undefined}
      onMouseDown={(e) => e.preventDefault()} onClick={() => blocked ? onBlocked(blocked) : tray.fileSupported ? setOpen(!open) : pick(false)}>{label}</button>
    {open && <div className="attach-menu" role="menu"><button role="menuitem" onClick={() => pick(false)}>图片</button><button role="menuitem" onClick={() => pick(true)}>文件</button></div>}
    <input ref={input} type="file" accept="image/*" multiple hidden onChange={(e) => { tray.add(imageFiles(e.currentTarget.files)); e.currentTarget.value = ""; }} />
    <input ref={files} type="file" multiple hidden onChange={(e) => { tray.addFiles(attachmentFiles(e.currentTarget.files)); e.currentTarget.value = ""; }} />
  </span>;
}

function Thumb({ item, onRemove, onRetry }: { item: TrayItem; onRemove: () => void; onRetry: () => void }) {
  const [url, setUrl] = useState<string>();
  useEffect(() => {
    if (!item.thumb) return;
    const u = URL.createObjectURL(item.thumb);
    setUrl(u);
    return () => URL.revokeObjectURL(u);
  }, [item.thumb]);
  const busy = item.state === "preparing" || item.state === "uploading";
  return (
    <span className="pic tthumb">
      {url && <img src={url} alt="" />}
      {busy && item.progress > 0 && item.progress < 1 ? (
        <span className="pct">
          <b style={{ width: `${Math.round(item.progress * 100)}%` }} />
        </span>
      ) : (
        busy && <span className="ring spin" aria-label="上传中" />
      )}
      {item.state === "failed" && (
        <button className="badge retry" onClick={onRetry} title={item.error}>
          重试
        </button>
      )}
      <button className="x" onClick={onRemove} aria-label="移除这张图">
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
export function TrayView({ tray, note }: { tray: Tray; note?: ReactNode }) {
  if (tray.items.length === 0 && !tray.note) return null;
  const images = tray.images();
  const files = tray.files();
  const failed = images.filter((i) => i.state === "failed");
  return (
    <div className="attachments">
      {images.length > 0 && (
        <>
          <div className="att-tray">
            {images.map((i) => (
              <Thumb key={i.key} item={i} onRemove={() => tray.remove(i.key)} onRetry={() => tray.retry(i.key)} />
            ))}
          </div>
          <div className="att-meta">
            <button
              className={"orig" + (tray.original ? " on" : "")}
              role="switch"
              aria-checked={tray.original}
              disabled={!tray.canSwitchOriginal()}
              title={tray.canSwitchOriginal() ? undefined : "放回的图片没有原始文件，不能切换原图"}
              onClick={() => tray.setOriginal(!tray.original)}
            >
              <i />
              原图{tray.original && tray.totalBytes() > 0 ? ` · 共 ${mb(tray.totalBytes())}` : ""}
            </button>
            <span>
              {images.length} / {tray.max}
            </span>
          </div>
        </>
      )}
      {tray.original && images.length > 0 && <div className="att-hint">原图保住画质；两个 CLI 仍会把过大的图缩到 2000px 左右。</div>}
      {files.length > 0 && <>
        <div className="att-tray file-chips">{files.map((i) => <FileChip key={i.key} file={{ name: i.name!, sizeBytes: BigInt(i.bytes ?? 0) }} state={i.state} progress={i.progress} error={i.error} onRetry={() => tray.retry(i.key)} onRemove={() => tray.remove(i.key)} />)}</div>
        <div className="att-hint">文件原样发送，包含其中的元数据。</div>
        {files.some((i) => i.state === "failed") && <Banner kind="err">{files.find((i) => i.state === "failed")?.error}。点「重试」或移除后才能发送</Banner>}
      </>}
      {failed.length > 0 && (
        <Banner kind="err">
          {failed.length} 张图没能传到电脑：{failed[0].error}。点「重试」或移除后才能发送
        </Banner>
      )}
      {tray.note && <Banner>{tray.note}</Banner>}
      {note}
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
        if (take(attachmentFiles(e.dataTransfer.files))) e.preventDefault();
      },
      onPaste: (e: ClipboardEvent) => {
        if (take(attachmentFiles(e.clipboardData.items))) e.preventDefault();
      },
    },
  };
}
