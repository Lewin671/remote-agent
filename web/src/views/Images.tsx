// Pictures in a session (client-v2 会话里的图片): previews under messages
// and tool rows, the card of a generated image, and the viewer that
// fetches the display copy on demand. Decrypted bytes are shown as blob:
// URLs, each revoked as soon as nothing shows it.

import { useRowExpanded } from "./RowState";
import { create } from "@bufbuild/protobuf";
import { ImageFormat, ImageRefSchema } from "../gen/remoteagent/v1/event_pb";
import type { Tray, TrayItem } from "../attach";
import { imageProblem } from "../image-problem";
import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import type { ImageRef } from "../gen/remoteagent/v1/event_pb";
import { ImageSync } from "../gen/remoteagent/v1/event_pb";
import { hhmm } from "../model";
import {
  aspectOf,
  baseName,
  extOf,
  matchesFormat,
  mimeOf,
  sizeText,
  type SessionImage,
} from "../imageview";
import { errorText, type ImageResult, type Remote } from "../remote";
import { Exp, GlassButton, useDialogFocus, useEscape, useRemote } from "../ui";
import { ChevronLeft, ChevronRight, X } from "../icons";

/** Opens the viewer at a picture; provided by the session page. */
export const OpenImage = createContext<(ref: ImageRef) => void>(
  () => undefined,
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
  height = 96,
  number,
  badge,
  onOpen,
  className = "",
}: {
  image: ImageRef;
  height?: number;
  number?: number;
  badge?: string;
  onOpen?: () => void;
  className?: string;
}) {
  const url = usePreview(r);
  const aspect = Math.min(3, Math.max(0.4, aspectOf(r)));
  // height 0: as wide as the chat, at the image's proportions.
  const style: CSSProperties = height
    ? { height, width: Math.round(height * aspect) }
    : { aspectRatio: String(aspectOf(r)) };
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
        aria-label="图片没有同步"
      >
        图片没有同步
        {r.path && <code>{baseName(r.path)}</code>}
      </button>
    );
  return (
    <button
      className={"pic " + className}
      style={style}
      onClick={onOpen}
      aria-label={r.path ? `图片 ${baseName(r.path)}` : "图片"}
    >
      {url && <img src={url} alt="" draggable={false} />}
      {number ? <span className="n">{number}</span> : null}
      {(badge ?? (r.sync === ImageSync.PREVIEW_ONLY ? "只有预览" : "")) && (
        <span className="badge">{badge ?? "只有预览"}</span>
      )}
    </button>
  );
}

/** A row of previews: at most four, the rest folded into +N. */
export function ImageStrip({
  refs,
  numbered,
  className = "",
  height,
}: {
  refs: ImageRef[];
  numbered?: boolean;
  className?: string;
  height?: number;
}) {
  const open = useContext(OpenImage);
  if (!refs.length) return null;
  const shown = refs.slice(0, 4);
  const more = refs.length - shown.length;
  return (
    <span className={"pics " + className}>
      {shown.map((r, i) => (
        <span key={i} className="picwrap">
          <Pic
            image={r}
            height={height}
            number={numbered && r.cliIndex ? r.cliIndex : undefined}
            onOpen={() => open(r)}
          />
          {more > 0 && i === shown.length - 1 && (
            <button
              className="more"
              onClick={() => open(r)}
              aria-label={`还有 ${more} 张`}
            >
              +{more}
            </button>
          )}
        </span>
      ))}
    </span>
  );
}

/** An image the agent made: full width, with its rewritten prompt. */
export function GenCard({
  refs,
  prompt,
}: {
  refs: ImageRef[];
  prompt: string;
}) {
  const open = useContext(OpenImage);
  const [shown, setShown] = useRowExpanded("image-prompt");
  return (
    <>
      {refs.map((r, i) => (
        <div key={i} className="gen-card">
          <Pic image={r} className="lg" height={0} onOpen={() => open(r)} />
          <div className="meta">
            <span>生成的图片{sizeText(r) && ` · ${sizeText(r)}`}</span>
            {prompt && i === 0 && (
              <button
                className="link"
                onClick={() => setShown(!shown)}
                aria-expanded={shown}
              >
                提示词 <Exp up={shown} />
              </button>
            )}
          </div>
          {shown && i === 0 && <div className="gen-prompt">{prompt}</div>}
        </div>
      ))}
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
  remote: Remote,
  sid: string,
  img: SessionImage | undefined,
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
  useEffect(() => {
    if (local) return;
    if (!img) return;
    const r = img.ref;
    if (r.sync === ImageSync.NAME_ONLY) return setState({ kind: "name-only" });
    if (r.sync !== ImageSync.SYNCED || !r.imageId)
      return setState({ kind: "preview-only" });
    const cached = remote.cachedImage(sid, r.imageId);
    if (cached) return setState(cached);
    if (remote.conn !== "online") return setState({ kind: "offline" });
    let alive = true;
    setState({ kind: "loading" });
    remote.getImage(sid, r.imageId).then(
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
  }, [remote, sid, local ? undefined : img, attempt, remote.conn === "online", local?.prepared, local?.state]); // eslint-disable-line react-hooks/exhaustive-deps
  return localLoad ?? state;
}

function sourceText(img: SessionImage, agent: string): string {
  if (img.kind === "generated") return "生成的图片";
  if (img.kind === "tool") return `${agent} 查看的图`;
  return "用户发送或粘贴的图";
}

/** What to say over the picture while its display copy is not shown. */
function loadNote(l: Load): string {
  switch (l.kind) {
    case "loading":
      return "";
    case "offline":
      return "连上后加载";
    case "error":
      return "没能下载 · 点按重试";
    case "expired":
      return "大图已过期，只剩预览";
    case "missing":
      return "电脑没有上传大图，只有预览";
    case "bad":
      return l.why ?? "图片校验失败，没有显示";
    case "preview-only":
      return "只有预览，原图在电脑上";
    case "name-only":
      return "图片没有同步";
    case "ok":
      return "";
  }
  return "";
}

/** The viewer: the session's pictures one at a time. The preview shows
 * enlarged at once and gives way to the display copy when it arrives. */
export function ImageViewer({
  remote,
  sid,
  images,
  start,
  agent,
  title,
  wide,
  onClose,
  showPath = (p) => p,
  draft,
}: {
  remote: Remote;
  sid: string;
  images: SessionImage[];
  start: number;
  agent: string;
  title: string;
  wide: boolean;
  onClose: () => void;
  // Paths as the chat writes them: relative to the session's folder.
  showPath?: (path: string) => string;
  draft?: Tray;
}) {
  const [i, setI] = useState(start);
  const [attempt, setAttempt] = useState(0);
  const [note, setNote] = useState("");
  const index = Math.min(i, images.length - 1);
  const img = images[index];
  const local = draft?.images()[index];
  const problem = local?.state === "failed" ? imageProblem(local.error ?? "") : undefined;
  const load = useDisplay(remote, sid, img, attempt, local);
  const full = useObjectUrl(load.kind === "ok" ? load.blob : undefined);
  const preview = usePreview(img?.ref ?? NO_REF);
  const go = (d: number) => {
    setNote("");
    setI((x) => Math.max(0, Math.min(images.length - 1, x + d)));
  };
  useEscape(onClose);
  const box = useRef<HTMLDivElement>(null);
  useDialogFocus(box);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "ArrowLeft") go(-1);
      else if (e.key === "ArrowRight") go(1);
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  });
  // Swipes: sideways to page, down to close. Two fingers zoom.
  const touch = useRef<{ x: number; y: number; d?: number } | undefined>(
    undefined,
  );
  const [zoom, setZoom] = useState(1);
  useEffect(() => setZoom(1), [i]);
  if (!img) return null;
  const name = img.ref.path
    ? baseName(img.ref.path)
    : `image-${i + 1}.${extOf(load.kind === "ok" ? load.format : img.ref.format)}`;
  const blob = load.kind === "ok" ? load.blob : undefined;
  const save = () => {
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
        setNote("没能分享：" + errorText(e));
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
      setNote("已复制");
    } catch (e) {
      setNote("没能复制：" + errorText(e));
    }
  };
  const status = local ? "" : loadNote(load);
  const retryable = load.kind === "error";
  const draftActions = local && draft && <>
    {problem?.retryable && <button className="glass" onClick={() => draft.retry(local.key)}>重试</button>}
    <button className="glass" onClick={() => { draft.remove(local.key); if (!draft.images().length) onClose(); }}>移除</button>
  </>;
  const picture = (
    <div
      className="vimg"
      onPointerDown={(e) => (touch.current = { x: e.clientX, y: e.clientY })}
      onPointerUp={(e) => {
        const t = touch.current;
        touch.current = undefined;
        if (!t || zoom > 1) return;
        const dx = e.clientX - t.x;
        const dy = e.clientY - t.y;
        if (Math.abs(dx) > 60 && Math.abs(dx) > Math.abs(dy))
          go(dx < 0 ? 1 : -1);
        else if (!wide && dy > 90) onClose();
      }}
      onTouchMove={(e) => {
        if (e.touches.length !== 2) return;
        const d = Math.hypot(
          e.touches[0].clientX - e.touches[1].clientX,
          e.touches[0].clientY - e.touches[1].clientY,
        );
        const t = touch.current ?? { x: 0, y: 0 };
        if (t.d) setZoom((z) => Math.max(1, Math.min(4, (z * d) / t.d!)));
        touch.current = { ...t, d };
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
      {load.kind === "loading" && !problem && <span className="ring" aria-label="加载中" />}
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
  const info = local ? <>
    <b>图片 {index + 1}，共 {images.length} 张</b>
    {local.file instanceof File && <div>{local.file.name}</div>}
    <div>{problem?.message ?? (local.state === "preparing" ? "正在处理" : local.state === "uploading" ? "正在传到电脑" : "待发送的图片")}</div>
  </> : (
    <>
      {sourceText(img, agent)}
      {img.ref.path && (
        <>
          {" · "}
          <code>{showPath(img.ref.path)}</code>
        </>
      )}
      {" · "}
      {[sizeText(img.ref), title, img.time ? hhmm(img.time) : ""]
        .filter(Boolean)
        .join(" · ")}
    </>
  );
  // Dark in both themes, its buttons dark glass (design-system §2, §4).
  if (wide)
    return (
      <div
        className="lightbox"
        role="dialog"
        aria-modal="true"
        aria-label="查看图片"
        ref={box}
      >
        <div className="lb-top">
          <span>
            {index + 1} / {images.length} · {draft ? "待发送的图片" : sourceText(img, agent)}
          </span>
          <GlassButton label="关闭" onClick={onClose}>
            <X />
          </GlassButton>
        </div>
        <div className="lb-mid">
          <GlassButton label="上一张" onClick={() => go(-1)} disabled={i === 0}>
            <ChevronLeft />
          </GlassButton>
          <div className="lb-img">{picture}</div>
          <GlassButton label="下一张" onClick={() => go(1)} disabled={i >= images.length - 1}>
            <ChevronRight />
          </GlassButton>
        </div>
        <div className="lb-foot">
          <span className="lb-info">{info}</span>
          {note && <span className="lb-note">{note}</span>}
          {draft ? draftActions : <>
          <button className="glass" onClick={save} disabled={!full}>
            保存
          </button>
          <button
            className="glass"
            onClick={() => void copy()}
            disabled={!blob || typeof ClipboardItem === "undefined"}
          >
            复制
          </button>
          <button className="glass" onClick={onClose}>回到消息</button>
          </>}
        </div>
      </div>
    );
  return (
    <div
      className="viewer"
      role="dialog"
      aria-modal="true"
      aria-label="查看图片"
      ref={box}
    >
      <div className="vbar">
        <GlassButton label="关闭" onClick={onClose}>
          <X />
        </GlassButton>
        <b>
          {index + 1} / {images.length}
        </b>
        <span />
      </div>
      {picture}
      <div className="vinfo">
        {local ? info : <>
        <b>{sourceText(img, agent)}</b>
        {img.ref.path && <code>{showPath(img.ref.path)}</code>}
        <div>
          {[sizeText(img.ref), title, img.time ? hhmm(img.time) : ""]
            .filter(Boolean)
            .join(" · ")}
        </div>
        {img.prompt && <div className="vprompt">{img.prompt}</div>}
        </>}
        {note && <div>{note}</div>}
      </div>
      <div className="vact">
        {draft ? draftActions : <>
        <button className="glass" onClick={save} disabled={!full}>
          保存
        </button>
        {canShare && <button className="glass" onClick={() => void share()}>分享</button>}
        <button className="glass" onClick={onClose}>回到消息</button>
        </>}
      </div>
    </div>
  );
}

/** Draft and message pictures use the same zooming, paging and dialog. Only
 * their source/available actions differ; drafts never fetch a server image. */
export function DraftImageViewer({ remote, tray, start, onClose }: { remote: Remote; tray: Tray; start: number; onClose: () => void }) {
  const images: SessionImage[] = tray.images().map((item) => ({
    ref: create(ImageRefSchema, { imageId: item.key, path: item.file instanceof File ? item.file.name : "", width: item.prepared?.width, height: item.prepared?.height }),
    seq: 0n, time: 0, kind: "user",
  }));
  return <ImageViewer remote={remote} sid="" images={images} start={start} agent="" title="待发送的图片" wide={matchMedia("(min-width: 900px)").matches} onClose={onClose} draft={tray} />;
}

async function toPng(blob: Blob): Promise<Blob> {
  const bmp = await createImageBitmap(blob);
  const c = document.createElement("canvas");
  c.width = bmp.width;
  c.height = bmp.height;
  c.getContext("2d")!.drawImage(bmp, 0, 0);
  bmp.close();
  return new Promise((res, rej) =>
    c.toBlob((b) => (b ? res(b) : rej(new Error("转换失败"))), "image/png"),
  );
}

/** This device's copies of images on their way: shown until the CLI
 * records the message with its own previews. */
export function LocalThumbs({ blobs }: { blobs?: Blob[] }) {
  const [urls, setUrls] = useState<string[]>([]);
  useEffect(() => {
    const us = (blobs ?? []).map((b) => URL.createObjectURL(b));
    setUrls(us);
    return () => us.forEach((u) => URL.revokeObjectURL(u));
  }, [blobs]);
  if (!urls.length) return null;
  return (
    <span className="pics">
      {urls.map((u) => (
        <span key={u} className="pic local">
          <img src={u} alt="" />
        </span>
      ))}
    </span>
  );
}
