import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactElement,
} from "react";
import * as i18n from "../i18n";
import type { SharedFile } from "../gen/remoteagent/v1/event_pb";
import {
  ArrowDown,
  DownloadIcon,
  FileArchiveIcon,
  FileIcon,
  FileTextIcon,
  ImageIcon,
  MusicIcon,
  PlayIcon,
  ShareIcon,
  TrashIcon,
  VideoIcon,
  X,
} from "../icons";
import { shortPath } from "../model";
import { errorText, type Remote } from "../remote";
import {
  classifySharedFile,
  failureNotice,
  formatEta,
  formatMediaDuration,
  kindName,
  previewBlobForFile,
  sanitizeFileName,
  saveBlobAsDownload,
  splitFileName,
  type SharedFileDerivedState,
  type SharedFileKind,
  type SharedFilePreviewKind,
} from "../shared-files";
import { GlassButton, Sheet, useDialogFocus, useEscape, wideQuery } from "../ui";
import { fileSize } from "./Files";
import { ImageViewer } from "./Images";

export interface SharedFilePreviewTarget {
  file: SharedFile;
  blob: Blob;
  kind: SharedFilePreviewKind;
  previewBlob: Blob;
  path: string;
}

export interface SharedFileViewContextValue {
  openFile: (file: SharedFile, blob: Blob) => void;
  onToast: (msg: string) => void;
}

export const SharedFileViewContext = createContext<SharedFileViewContextValue>({
  openFile: () => undefined,
  onToast: () => undefined,
});

function useWide(): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const m = wideQuery();
      m.addEventListener("change", onChange);
      return () => m.removeEventListener("change", onChange);
    },
    () => wideQuery().matches,
  );
}

function useSharedFileState(remote: Remote, sessionId: string, file: SharedFile): SharedFileDerivedState {
  useEffect(() => {
    void remote.sharedFiles.hydrate(sessionId, file);
  }, [remote, sessionId, file]);

  // Subscribe only to the shared-file store and read latest derived snapshot.
  const tick = useSyncExternalStore(
    (cb) => remote.sharedFiles.subscribe(cb),
    () => {
      const st = remote.sharedFiles.stateOf(sessionId, file);
      return `${st.status}:${st.downloadedBytes}:${st.etaSeconds ?? ""}:${st.failKind ?? ""}:${st.failReason ?? ""}:${st.waitingReconnect ? 1 : 0}`;
    },
  );
  void tick;
  return remote.sharedFiles.stateOf(sessionId, file);
}

interface MediaMeta {
  thumbUrl?: string;
  width?: number;
  height?: number;
  durationSec?: number;
}

function useLocalMediaMeta(file: SharedFile, blob?: Blob): MediaMeta {
  const [meta, setMeta] = useState<MediaMeta>({});

  useEffect(() => {
    if (!blob) {
      setMeta({});
      return;
    }
    const preview = previewBlobForFile(blob, file.name);
    if (!preview) {
      setMeta({});
      return;
    }
    let alive = true;
    const urlsToRevoke: string[] = [];

    if (preview.kind === "image") {
      const url = URL.createObjectURL(preview.blob);
      urlsToRevoke.push(url);
      setMeta({ thumbUrl: url });
      const img = new Image();
      img.onload = () => {
        if (alive && img.naturalWidth > 0 && img.naturalHeight > 0) {
          setMeta({ thumbUrl: url, width: img.naturalWidth, height: img.naturalHeight });
        }
      };
      img.src = url;
    } else if (preview.kind === "video") {
      const videoUrl = URL.createObjectURL(preview.blob);
      const video = document.createElement("video");
      video.preload = "auto";
      video.muted = true;
      video.playsInline = true;
      let captured = false;
      let fallbackTimer: ReturnType<typeof setTimeout> | undefined;
      const captureFrame = () => {
        if (captured) return;
        captured = true;
        clearTimeout(fallbackTimer);
        const durationSec = Number.isFinite(video.duration) && video.duration > 0 ? video.duration : undefined;
        let drew = false;
        try {
          if (video.videoWidth > 0 && video.videoHeight > 0) {
            const canvas = document.createElement("canvas");
            canvas.width = 88;
            canvas.height = 88;
            const ctx = canvas.getContext("2d");
            if (ctx) {
              const scale = Math.max(88 / video.videoWidth, 88 / video.videoHeight);
              const w = video.videoWidth * scale;
              const h = video.videoHeight * scale;
              ctx.drawImage(video, (88 - w) / 2, (88 - h) / 2, w, h);
              drew = true;
              canvas.toBlob(
                (thumbBlob) => {
                  URL.revokeObjectURL(videoUrl);
                  if (!alive) return;
                  if (thumbBlob) {
                    const thumbUrl = URL.createObjectURL(thumbBlob);
                    urlsToRevoke.push(thumbUrl);
                    setMeta({ thumbUrl, durationSec });
                  } else {
                    setMeta({ durationSec });
                  }
                },
                "image/jpeg",
                0.85,
              );
            }
          }
        } catch {
          drew = false;
        }
        if (!drew) {
          URL.revokeObjectURL(videoUrl);
          if (alive) setMeta({ durationSec });
        }
      };
      video.onloadeddata = () => {
        if (!alive) return;
        const seekTo = Number.isFinite(video.duration) && video.duration > 0 ? Math.min(0.25, video.duration / 2) : 0;
        if (seekTo > 0) {
          video.onseeked = captureFrame;
          video.currentTime = seekTo;
          fallbackTimer = setTimeout(captureFrame, 450);
        } else {
          captureFrame();
        }
      };
      video.onerror = () => {
        clearTimeout(fallbackTimer);
        URL.revokeObjectURL(videoUrl);
      };
      video.src = videoUrl;
    } else if (preview.kind === "audio") {
      const audioUrl = URL.createObjectURL(preview.blob);
      const audio = document.createElement("audio");
      audio.preload = "metadata";
      audio.onloadedmetadata = () => {
        const durationSec = Number.isFinite(audio.duration) && audio.duration > 0 ? audio.duration : undefined;
        URL.revokeObjectURL(audioUrl);
        if (alive) setMeta({ durationSec });
      };
      audio.onerror = () => URL.revokeObjectURL(audioUrl);
      audio.src = audioUrl;
    }

    return () => {
      alive = false;
      for (const u of urlsToRevoke) URL.revokeObjectURL(u);
    };
  }, [blob, file.name]);

  return meta;
}

function formatLimit(bytes: bigint): string {
  const n = Number(bytes);
  if (n > 0 && n % (1024 * 1024) === 0) return `${n / (1024 * 1024)} MB`;
  return fileSize(bytes);
}

function tileAppearance(kind: SharedFileKind): { cls: string; icon: ReactElement } {
  switch (kind) {
    case "video":
      return { cls: "v", icon: <VideoIcon /> };
    case "image":
      return { cls: "p", icon: <ImageIcon /> };
    case "audio":
      return { cls: "d", icon: <MusicIcon /> };
    case "archive":
      return { cls: "z", icon: <FileArchiveIcon /> };
    case "pdf":
      return { cls: "d", icon: <FileTextIcon /> };
    case "file":
      return { cls: "d", icon: <FileIcon /> };
  }
}

function displayHomePath(rawPath: string, fallbackName: string, home?: string): string {
  const target = rawPath || sanitizeFileName(fallbackName);
  if (home && (target === home || target.startsWith(home + "/"))) {
    return "~" + target.slice(home.length);
  }
  return shortPath(target);
}

function subtitleFor(
  kind: SharedFileKind,
  file: SharedFile,
  state: SharedFileDerivedState,
  meta: MediaMeta,
  maxBytes: bigint,
): string {
  const totalText = fileSize(file.sizeBytes);
  switch (state.status) {
    case "available":
      return i18n.t("shared_file.type_and_size", { kind: kindName(kind), size: totalText });
    case "downloading": {
      const doneText = fileSize(state.downloadedBytes);
      if (state.etaSeconds !== undefined) {
        return i18n.t("shared_file.downloading_eta", {
          done: doneText,
          total: totalText,
          eta: formatEta(state.etaSeconds),
        });
      }
      return i18n.t("shared_file.downloading_no_eta", { done: doneText, total: totalText });
    }
    case "paused":
      return i18n.t("shared_file.paused", {
        done: fileSize(state.downloadedBytes),
        total: totalText,
      });
    case "downloaded":
      if ((kind === "video" || kind === "audio") && meta.durationSec !== undefined) {
        return i18n.t("shared_file.video_and_size", {
          duration: formatMediaDuration(meta.durationSec),
          size: totalText,
        });
      }
      if (kind === "image" && meta.width && meta.height) {
        return i18n.t("shared_file.image_meta", {
          width: meta.width,
          height: meta.height,
          size: totalText,
        });
      }
      return i18n.t("shared_file.type_and_size", { kind: kindName(kind), size: totalText });
    case "failed":
      return failureNotice(state.failKind, state.failReason);
    case "offline":
      return i18n.t("shared_file.machine_offline");
    case "cleaned":
      return i18n.t("shared_file.cleaned");
    case "too_large":
      return i18n.t("shared_file.too_large", { limit: formatLimit(maxBytes) });
  }
}

function a11yLabelFor(file: SharedFile, state: SharedFileDerivedState, maxBytes: bigint): string {
  const safe = sanitizeFileName(file.name);
  let detail: string;
  switch (state.status) {
    case "available":
      detail = i18n.t("shared_file.a11y_available");
      break;
    case "downloading":
      detail = i18n.t("shared_file.a11y_downloading", {
        percent: Math.round(state.progress * 100),
      });
      break;
    case "paused":
      detail = i18n.t("shared_file.a11y_paused");
      break;
    case "downloaded":
      detail = i18n.t("shared_file.a11y_downloaded");
      break;
    case "failed":
      detail = i18n.t("shared_file.a11y_failed");
      break;
    case "offline":
      detail = i18n.t("shared_file.a11y_offline");
      break;
    case "cleaned":
      detail = i18n.t("shared_file.a11y_cleaned");
      break;
    case "too_large":
      detail = i18n.t("shared_file.a11y_too_large", { limit: formatLimit(maxBytes) });
      break;
  }
  return i18n.t("shared_file.a11y_card", { name: safe, status: detail });
}

function cardClass(state: SharedFileDerivedState): string {
  if (state.status === "failed") return "sf-fc sf-fail";
  if (state.status === "offline" || state.status === "too_large") return "sf-fc sf-off";
  if (state.status === "cleaned") return "sf-fc sf-gone";
  return "sf-fc";
}

function CardVisuals({
  file,
  state,
  meta,
  kind,
  caption,
}: {
  file: SharedFile;
  state: SharedFileDerivedState;
  meta: MediaMeta;
  kind: SharedFileKind;
  caption: string;
}) {
  const [nameHead, nameTail] = splitFileName(file.name);
  const { cls, icon } = tileAppearance(kind);
  const showThumb = state.status === "downloaded" && !!meta.thumbUrl;
  const isVideo = kind === "video";
  const ringDash = `${Math.max(2, state.progress * 81.68).toFixed(1)} 82`;

  return (
    <>
      {showThumb ? (
        <span className={"sf-tile thumb" + (isVideo ? " video" : "")}>
          <img src={meta.thumbUrl} draggable={false} aria-hidden />
          {isVideo && <PlayIcon />}
        </span>
      ) : (
        <span className={"sf-tile " + cls}>{icon}</span>
      )}
      <div className="sf-fi">
        <div className="sf-fn">
          <span>{nameHead}</span>
          {nameTail && <span>{nameTail}</span>}
        </div>
        <div className="sf-fs">{caption}</div>
      </div>
      {state.status === "available" && (
        <span className="sf-act" aria-hidden>
          <ArrowDown />
        </span>
      )}
      {state.status === "offline" && (
        <span className="sf-act" aria-hidden>
          <ArrowDown />
        </span>
      )}
      {(state.status === "downloading" || state.status === "paused") && (
        <span className="sf-ring" aria-hidden>
          <svg viewBox="0 0 30 30">
            <circle className="t" cx="15" cy="15" r="13" />
            <circle className="p" cx="15" cy="15" r="13" strokeDasharray={ringDash} />
          </svg>
          {state.status === "downloading" ? <i /> : <PlayIcon className="sf-ring-play" />}
        </span>
      )}
      {state.status === "downloaded" && (
        <span className="sf-pill">{i18n.t("shared_file.open")}</span>
      )}
      {state.status === "failed" && (
        <span className="sf-pill">{i18n.t("shared_file.retry")}</span>
      )}
    </>
  );
}

interface MenuItemSpec {
  id: string;
  label: string;
  icon?: ReactElement;
  red?: boolean;
  group: number;
  onClick: () => void;
}

function overlayCoordinates(cardRect: DOMRect, itemCount: number): { top: number; left: number } {
  const estHeight = 140 + itemCount * 44;
  const maxTop = Math.max(16, window.innerHeight - estHeight - 16);
  const top = Math.min(Math.max(16, cardRect.top), maxTop);
  const left = Math.min(Math.max(16, cardRect.left), Math.max(16, window.innerWidth - cardRect.width - 16));
  return { top, left };
}

function menuSeparatorFlags(items: MenuItemSpec[], idx: number): { showGap: boolean; noSep: boolean } {
  const item = items[idx];
  const prevGroup = idx ? items[idx - 1].group : item.group;
  const nextGroup = idx + 1 === items.length ? item.group : items[idx + 1].group;
  return {
    showGap: Boolean(idx) && item.group !== prevGroup,
    noSep: idx === items.length - 1 || nextGroup !== item.group,
  };
}

function PhoneContextOverlay({
  file,
  state,
  meta,
  kind,
  sub,
  fullPath,
  machineName,
  cardRect,
  items,
  onClose,
}: {
  file: SharedFile;
  state: SharedFileDerivedState;
  meta: MediaMeta;
  kind: SharedFileKind;
  sub: string;
  fullPath: string;
  machineName: string;
  cardRect: DOMRect;
  items: MenuItemSpec[];
  onClose: () => void;
}) {
  useEscape(onClose);
  const rootRef = useRef<HTMLDivElement>(null);
  useDialogFocus(rootRef);
  const { top, left } = overlayCoordinates(cardRect, items.length);
  const overlayDialogProps = {
    role: "dialog" as const,
    "aria-modal": true,
    "aria-label": i18n.t("shared_file.context_menu_label"),
  };

  return (
    <div className="sf-ctx-overlay" ref={rootRef} {...overlayDialogProps}>
      <div className="sf-blur" onClick={onClose} />
      <div className="sf-ctx" style={{ top, left }}>
        <div className={cardClass(state)} onClick={onClose}>
          <CardVisuals file={file} state={state} meta={meta} kind={kind} caption={sub} />
        </div>
        <div className="sf-menu" role="menu">
          <div className="sf-mh">
            <div>{fullPath}</div>
            <small>{i18n.t("shared_file.from_machine", { name: machineName })}</small>
            {state.status === "failed" && (
              <small className="sf-mh-err">{sub}</small>
            )}
          </div>
          {items.map((item, idx) => {
            const { showGap, noSep } = menuSeparatorFlags(items, idx);
            return (
              <div key={item.id}>
                {showGap && <div className="sf-mgap" />}
                <button
                  type="button"
                  role="menuitem"
                  className={"sf-mi" + (item.red ? " red" : "") + (noSep ? " no-sep" : "")}
                  onClick={() => {
                    onClose();
                    item.onClick();
                  }}
                >
                  <span>{item.label}</span>
                  {item.icon}
                </button>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

function canSystemShareBlob(blob: Blob, name: string): boolean {
  if (typeof navigator === "undefined" || typeof navigator.canShare !== "function") return false;
  try {
    const f = new File([blob], sanitizeFileName(name), {
      type: previewBlobForFile(blob, name)?.blob.type || "application/octet-stream",
    });
    return navigator.canShare({ files: [f] });
  } catch {
    return false;
  }
}

async function systemShareBlob(blob: Blob, name: string, onToast: (msg: string) => void) {
  try {
    const f = new File([blob], sanitizeFileName(name), {
      type: previewBlobForFile(blob, name)?.blob.type || "application/octet-stream",
    });
    await navigator.share({ files: [f] });
  } catch (e) {
    if ((e as Error).name !== "AbortError") {
      onToast(i18n.t("shared_file.share_failed", { reason: errorText(e) }));
    }
  }
}

/** A single agent-shared file card matching draft.html section ①. */
export function SharedFileCard({
  remote,
  sessionId,
  file,
  machineName,
  machineHome,
  maxBytes,
}: {
  remote: Remote;
  sessionId: string;
  file: SharedFile;
  machineName: string;
  machineHome?: string;
  maxBytes: bigint;
}) {
  const state = useSharedFileState(remote, sessionId, file);
  const { kind } = classifySharedFile(file.name);
  const meta = useLocalMediaMeta(file, state.blob);
  const { openFile, onToast } = useContext(SharedFileViewContext);
  const wide = useWide();

  const suppressNextClick = useRef(false);
  const cardRef = useRef(null as HTMLDivElement | null);
  const longPressTimer = useRef(undefined as ReturnType<typeof setTimeout> | undefined);
  const pressOrigin = useRef(undefined as { x: number; y: number } | undefined);
  const [menuState, setMenuState] = useState(undefined as { rect: DOMRect } | undefined);

  useEffect(() => {
    return () => clearTimeout(longPressTimer.current);
  }, []);

  const subtitle = subtitleFor(kind, file, state, meta, maxBytes);
  const fullPath = displayHomePath(file.path, file.name, machineHome);
  const canShare = state.status === "downloaded" && state.blob ? canSystemShareBlob(state.blob, file.name) : false;

  const menuItems: MenuItemSpec[] = [];
  if (state.status === "downloaded" && state.blob) {
    if (canShare) {
      menuItems.push({
        id: "share",
        label: i18n.t("shared_file.share"),
        icon: <ShareIcon />,
        group: 0,
        onClick: () => void systemShareBlob(state.blob!, file.name, onToast),
      });
    }
    menuItems.push({
      id: "save",
      label: i18n.t("shared_file.save_to_device"),
      icon: <DownloadIcon />,
      group: 0,
      onClick: () => saveBlobAsDownload(state.blob!, file.name),
    });
    menuItems.push({
      id: "delete",
      label: i18n.t("shared_file.delete_local"),
      icon: <TrashIcon />,
      red: true,
      group: 1,
      onClick: () => void remote.sharedFiles.deleteLocal(sessionId, file.fileId),
    });
  } else if (state.status === "downloading") {
    menuItems.push(
      {
        id: "pause",
        label: i18n.t("shared_file.pause"),
        group: 0,
        onClick: () => remote.sharedFiles.pause(sessionId, file.fileId),
      },
      {
        id: "cancel",
        label: i18n.t("shared_file.cancel_download"),
        group: 0,
        onClick: () => void remote.sharedFiles.cancel(sessionId, file.fileId),
      },
    );
  } else if (state.status === "paused") {
    menuItems.push(
      {
        id: "resume",
        label: i18n.t("shared_file.resume"),
        group: 0,
        onClick: () =>
          void remote.sharedFiles.startDownload(sessionId, file, { autoOpenIfStaying: false }),
      },
      {
        id: "cancel",
        label: i18n.t("shared_file.cancel_download"),
        group: 0,
        onClick: () => void remote.sharedFiles.cancel(sessionId, file.fileId),
      },
    );
  } else if (state.status === "failed") {
    menuItems.push({
      id: "retry",
      label: i18n.t("shared_file.retry"),
      group: 0,
      onClick: () =>
        void remote.sharedFiles.startDownload(sessionId, file, { autoOpenIfStaying: false }),
    });
    if (state.downloadedBytes > 0) {
      menuItems.push({
        id: "cancel",
        label: i18n.t("shared_file.cancel_download"),
        group: 0,
        onClick: () => void remote.sharedFiles.cancel(sessionId, file.fileId),
      });
    }
  } else if (state.status === "available") {
    menuItems.push({
      id: "download",
      label: i18n.t("shared_file.download"),
      icon: <DownloadIcon />,
      group: 0,
      onClick: () =>
        void remote.sharedFiles.startDownload(sessionId, file, { autoOpenIfStaying: false }),
    });
  }

  const interactive =
    state.status !== "offline" && state.status !== "cleaned" && state.status !== "too_large";

  const handlePrimaryAction = () => {
    if (suppressNextClick.current) {
      suppressNextClick.current = false;
      return;
    }
    switch (state.status) {
      case "available":
      case "paused":
      case "failed":
        void remote.sharedFiles.startDownload(sessionId, file, { autoOpenIfStaying: true });
        break;
      case "downloading":
        remote.sharedFiles.pause(sessionId, file.fileId);
        break;
      case "downloaded":
        if (state.blob) openFile(file, state.blob);
        break;
      case "offline":
      case "cleaned":
      case "too_large":
        break;
    }
  };

  const openMenuAtCard = () => {
    const rect = cardRef.current?.getBoundingClientRect();
    if (rect) setMenuState({ rect });
  };

  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.pointerType === "mouse" && e.button !== 0) return;
    suppressNextClick.current = false;
    pressOrigin.current = { x: e.clientX, y: e.clientY };
    clearTimeout(longPressTimer.current);
    longPressTimer.current = setTimeout(() => {
      suppressNextClick.current = true;
      openMenuAtCard();
    }, 480);
  };

  const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    const origin = pressOrigin.current;
    if (!origin) return;
    if (Math.hypot(e.clientX - origin.x, e.clientY - origin.y) > 8) {
      clearTimeout(longPressTimer.current);
      pressOrigin.current = undefined;
    }
  };

  const clearLongPress = () => {
    clearTimeout(longPressTimer.current);
    pressOrigin.current = undefined;
  };

  const onContextMenu = (e: ReactMouseEvent<HTMLDivElement>) => {
    e.preventDefault();
    clearLongPress();
    openMenuAtCard();
  };

  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      handlePrimaryAction();
    } else if (e.key === "ContextMenu" || (e.shiftKey && e.key === "F10")) {
      e.preventDefault();
      openMenuAtCard();
    }
  };

  const menuGroups: MenuItemSpec[][] = [];
  for (const item of menuItems) {
    if (!menuGroups.length || menuGroups[menuGroups.length - 1][0].group !== item.group) {
      menuGroups.push([item]);
    } else {
      menuGroups[menuGroups.length - 1].push(item);
    }
  }

  return (
    <>
      <div
        ref={cardRef}
        role="button"
        tabIndex={0}
        aria-disabled={interactive ? undefined : true}
        aria-label={a11yLabelFor(file, state, maxBytes)}
        className={cardClass(state)}
        onClick={handlePrimaryAction}
        onKeyDown={onKeyDown}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={clearLongPress}
        onPointerCancel={clearLongPress}
        onContextMenu={onContextMenu}
      >
        <CardVisuals file={file} state={state} meta={meta} kind={kind} caption={subtitle} />
      </div>

      {menuState && !wide && (
        <PhoneContextOverlay
          file={file}
          state={state}
          meta={meta}
          kind={kind}
          sub={subtitle}
          fullPath={fullPath}
          machineName={machineName}
          cardRect={menuState.rect}
          items={menuItems}
          onClose={() => setMenuState(undefined)}
        />
      )}

      {menuState && wide && (
        <Sheet anchor={cardRef} onClose={() => setMenuState(undefined)} className="menu">
          <div className="menu-info">
            <span className="mono sf-wide-path">{fullPath}</span>
            <span>{i18n.t("shared_file.from_machine", { name: machineName })}</span>
            {state.status === "failed" && <span className="red">{subtitle}</span>}
          </div>
          {menuGroups.map((grp, g) => (
            <div className="list" role="menu" key={g}>
              {grp.map((item) => (
                <button
                  key={item.id}
                  type="button"
                  role="menuitem"
                  className={"mi" + (item.red ? " danger" : "")}
                  onClick={() => {
                    setMenuState(undefined);
                    item.onClick();
                  }}
                >
                  <div className="mt">{item.label}</div>
                </button>
              ))}
            </div>
          ))}
        </Sheet>
      )}
    </>
  );
}

/** Renders the stack of shared-file cards in place of the send_file tool call row. */
export function SharedFileCards({
  remote,
  sessionId,
  files,
  machineName,
  machineHome,
  maxBytes,
}: {
  remote: Remote;
  sessionId: string;
  files: SharedFile[];
  machineName: string;
  machineHome?: string;
  maxBytes: bigint;
}) {
  return (
    <div className="sf-files">
      {files.map((f) => (
        <SharedFileCard
          key={f.fileId}
          remote={remote}
          sessionId={sessionId}
          file={f}
          machineName={machineName}
          machineHome={machineHome}
          maxBytes={maxBytes}
        />
      ))}
    </div>
  );
}

/** Session-scoped overlay hosting image viewer or audio/video player for opened shared files. */
export function SharedFilePreviewOverlay({
  remote,
  sessionId,
  agentName,
  machineHome,
  wide,
  target,
  onClose,
}: {
  remote: Remote;
  sessionId: string;
  agentName: string;
  machineHome?: string;
  wide: boolean;
  target: SharedFilePreviewTarget;
  onClose: () => void;
}) {
  const [mediaUrl, setMediaUrl] = useState("");
  const boxRef = useRef<HTMLDivElement>(null);
  useEscape(onClose);
  useDialogFocus(boxRef);

  useEffect(() => {
    if (target.kind === "image") return;
    const url = URL.createObjectURL(target.previewBlob);
    setMediaUrl(url);
    return () => {
      URL.revokeObjectURL(url);
      setMediaUrl("");
    };
  }, [target]);

  if (target.kind === "image") {
    return (
      <ImageViewer
        remote={remote}
        sid={sessionId}
        images={[]}
        start={0}
        agent={agentName}
        title=""
        wide={wide}
        onClose={onClose}
        showPath={(p) => displayHomePath(p, target.file.name, machineHome)}
        sharedFile={{
          blob: target.previewBlob,
          name: sanitizeFileName(target.file.name),
          path: target.path,
          sizeText: fileSize(target.file.sizeBytes),
        }}
      />
    );
  }

  const safeName = sanitizeFileName(target.file.name);
  const pathText = displayHomePath(target.path, target.file.name, machineHome);
  const dialogProps = {
    role: "dialog" as const,
    "aria-modal": true,
    "aria-label": safeName,
  };

  if (wide) {
    return (
      <div className="lightbox" ref={boxRef} {...dialogProps}>
        <div className="lb-top">
          <span>{safeName}</span>
          <GlassButton label={i18n.t("shared_file.close_preview")} onClick={onClose}>
            <X />
          </GlassButton>
        </div>
        <div className="lb-mid sf-media-mid">
          {target.kind === "video" && mediaUrl && (
            <video className="sf-media-video" src={mediaUrl} controls autoPlay playsInline />
          )}
          {target.kind === "audio" && mediaUrl && (
            <div className="sf-media-audio-wrap">
              <MusicIcon />
              <b>{safeName}</b>
              <audio className="sf-media-audio" src={mediaUrl} controls autoPlay />
            </div>
          )}
        </div>
        <div className="lb-foot">
          <span className="lb-info">
            {safeName} · <code>{pathText}</code> · {fileSize(target.file.sizeBytes)}
          </span>
          <button className="glass" onClick={() => saveBlobAsDownload(target.blob, target.file.name)}>
            {i18n.t("shared_file.save_to_device")}
          </button>
          <button className="glass" onClick={onClose}>
            {i18n.t("shared_file.close_preview")}
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="viewer" ref={boxRef} {...dialogProps}>
      <div className="vbar">
        <GlassButton label={i18n.t("shared_file.close_preview")} onClick={onClose}>
          <X />
        </GlassButton>
        <b>{safeName}</b>
        <span />
      </div>
      <div className="vimg sf-media-mid">
        {target.kind === "video" && mediaUrl && (
          <video className="sf-media-video" src={mediaUrl} controls autoPlay playsInline />
        )}
        {target.kind === "audio" && mediaUrl && (
          <div className="sf-media-audio-wrap">
            <MusicIcon />
            <b>{safeName}</b>
            <audio className="sf-media-audio" src={mediaUrl} controls autoPlay />
          </div>
        )}
      </div>
      <div className="vinfo">
        <b>{safeName}</b>
        <code>{pathText}</code>
        <div>{fileSize(target.file.sizeBytes)}</div>
      </div>
      <div className="vact">
        <button className="glass" onClick={() => saveBlobAsDownload(target.blob, target.file.name)}>
          {i18n.t("shared_file.save_to_device")}
        </button>
        <button className="glass" onClick={onClose}>
          {i18n.t("shared_file.close_preview")}
        </button>
      </div>
    </div>
  );
}
