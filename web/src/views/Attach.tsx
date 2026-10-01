// Choosing images for a message: the ＋ button (the system's picker:
// library, camera, files), dropping or pasting on wide screens, and the
// tray of chosen images above the text with each one's upload.

import { useEffect, useRef, useState, type ClipboardEvent, type DragEvent, type ReactNode } from "react";
import { Tray, type TrayItem } from "../attach";
import { machineName } from "../model";
import type { MachineView, Remote } from "../remote";
import { Banner } from "../ui";

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
  const tray = t;
  useEffect(() => {
    tray.retarget(machineId);
  }, [tray, machineId]);
  return tray;
}

/** Why images cannot be added now, or "". */
export function attachBlocked(remote: Remote, m: MachineView | undefined): string {
  const name = machineName(m);
  if (!m?.online) return `${name} 离线，连上后才能添加图片`;
  if (remote.conn !== "online") return "和服务器断线了，连上后才能添加图片";
  if (!m.meta?.capabilities?.imageUpload) return `${name} 上的 remote-agent 版本较旧，不能发图片；在电脑上更新后再试`;
  return "";
}

export function maxImages(m: MachineView | undefined): number {
  return m?.meta?.capabilities?.maxImagesPerMessage || 6;
}

/** Image files in a drop or a paste. */
export function imageFiles(list: FileList | DataTransferItemList | null | undefined): File[] {
  if (!list) return [];
  const out: File[] = [];
  for (const x of Array.from(list as ArrayLike<File | DataTransferItem>)) {
    const f = x instanceof File ? x : (x as DataTransferItem).kind === "file" ? (x as DataTransferItem).getAsFile() : null;
    if (f && f.type.startsWith("image/")) out.push(f);
  }
  return out;
}

/** 添加图片 in the input's ＋ menu: opens the system's image picker.
 * Unavailable, it says why. */
export function AttachButton({
  tray,
  blocked,
  onBlocked,
  className = "cm-plus",
  label = "＋",
}: {
  tray: Tray;
  blocked: string;
  onBlocked: (why: string) => void;
  className?: string;
  // The ＋ on the input's left; it opens the image picker itself.
  label?: string;
}) {
  const input = useRef<HTMLInputElement>(null);
  const off = !!blocked || tray.full();
  return (
    <>
      <button
        className={className + (off ? " off" : "")}
        aria-label="添加图片"
        onMouseDown={(e) => e.preventDefault()}
        onClick={() => (blocked ? onBlocked(blocked) : tray.full() ? onBlocked(`一条消息最多 ${tray.max} 张`) : input.current?.click())}
      >
        {label}
      </button>
      <input
        ref={input}
        type="file"
        accept="image/*"
        multiple
        hidden
        onChange={(e) => {
          tray.add(imageFiles(e.currentTarget.files));
          e.currentTarget.value = "";
        }}
      />
    </>
  );
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
        ✕
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
  const failed = tray.items.filter((i) => i.state === "failed");
  return (
    <>
      {tray.items.length > 0 && (
        <>
          <div className="att-tray">
            {tray.items.map((i) => (
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
              {tray.items.length} / {tray.max}
            </span>
          </div>
        </>
      )}
      {tray.original && tray.items.length > 0 && <div className="att-hint">原图保住画质；两个 CLI 仍会把过大的图缩到 2000px 左右。</div>}
      {failed.length > 0 && (
        <Banner kind="err">
          {failed.length} 张图没能传到电脑：{failed[0].error}。点「重试」或移除后才能发送
        </Banner>
      )}
      {tray.note && <Banner>{tray.note}</Banner>}
      {note}
    </>
  );
}

/** Drop and paste on a card: images join the tray. */
export function useDropPaste(tray: Tray, blocked: string, onBlocked: (why: string) => void) {
  const [over, setOver] = useState(false);
  const take = (files: File[]) => {
    if (!files.length) return false;
    if (blocked) onBlocked(blocked);
    else tray.add(files);
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
        if (take(imageFiles(e.dataTransfer.files))) e.preventDefault();
      },
      onPaste: (e: ClipboardEvent) => {
        if (take(imageFiles(e.clipboardData.items))) e.preventDefault();
      },
    },
  };
}
