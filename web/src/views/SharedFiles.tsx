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
import { renderText } from "../core/text";
import {
  SharedFileCardStatus,
  SharedFileKind,
  SharedFileMenuAction,
  SharedFilePrimaryAction,
  SharedFileWebPreviewKind,
  type SessionSharedFileItem,
  type SharedFileMenuItemView,
} from "../gen/remoteagent/core/v1/session_view_pb";
import * as i18n from "../i18n";
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
import { errorText, type Remote } from "../remote";
import { saveBlobAsDownload, sharedFilesOf } from "../shared-files";
import { GlassButton, Sheet, useDialogFocus, useEscape, wideQuery } from "../ui";
import { ImageViewer } from "./Images";

export interface SharedFilePreviewTarget {
  file: SessionSharedFileItem;
  blob: Blob;
  kind: SharedFileWebPreviewKind;
  previewBlob: Blob;
}

export interface SharedFileViewContextValue {
  openFile: (file: SessionSharedFileItem, blob: Blob) => void;
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

function useLocalBlob(
  remote: Remote | undefined,
  sessionId: string,
  file: SessionSharedFileItem,
): Blob | undefined {
  const manager = remote ? sharedFilesOf(remote) : undefined;
  useEffect(() => {
    if (manager) void manager.hydrate(sessionId, file);
  }, [manager, sessionId, file.fileId, file.sizeBytes]);

  return useSyncExternalStore(
    (cb) => (manager ? manager.subscribe(cb) : () => undefined),
    () => manager?.blobOf(sessionId, file.fileId),
  );
}

function useLocalMediaThumb(
  remote: Remote | undefined,
  sessionId: string,
  file: SessionSharedFileItem,
  blob?: Blob,
): string | undefined {
  const [thumbUrl, setThumbUrl] = useState<string | undefined>(undefined);

  useEffect(() => {
    if (
      !blob ||
      file.status !== SharedFileCardStatus.DOWNLOADED ||
      file.webPreviewKind === SharedFileWebPreviewKind.UNSPECIFIED ||
      !file.webPreviewMime
    ) {
      setThumbUrl(undefined);
      return;
    }
    const previewBlob = new Blob([blob], { type: file.webPreviewMime });
    let alive = true;
    const urlsToRevoke: string[] = [];
    const reportDuration = (durationSec: number | undefined) => {
      if (remote && file.hasMediaDuration && durationSec !== undefined && Number.isFinite(durationSec) && durationSec > 0) {
        sharedFilesOf(remote).reportDuration(sessionId, file.fileId, durationSec * 1000);
      }
    };

    if (file.webPreviewKind === SharedFileWebPreviewKind.IMAGE) {
      const url = URL.createObjectURL(previewBlob);
      urlsToRevoke.push(url);
      setThumbUrl(url);
    } else if (file.webPreviewKind === SharedFileWebPreviewKind.VIDEO) {
      const videoUrl = URL.createObjectURL(previewBlob);
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
        reportDuration(durationSec);
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
                    const url = URL.createObjectURL(thumbBlob);
                    urlsToRevoke.push(url);
                    setThumbUrl(url);
                  } else {
                    setThumbUrl(undefined);
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
          if (alive) setThumbUrl(undefined);
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
    } else if (file.webPreviewKind === SharedFileWebPreviewKind.AUDIO) {
      setThumbUrl(undefined);
      const audioUrl = URL.createObjectURL(previewBlob);
      const audio = document.createElement("audio");
      audio.preload = "metadata";
      audio.onloadedmetadata = () => {
        const durationSec = Number.isFinite(audio.duration) && audio.duration > 0 ? audio.duration : undefined;
        URL.revokeObjectURL(audioUrl);
        if (alive) reportDuration(durationSec);
      };
      audio.onerror = () => URL.revokeObjectURL(audioUrl);
      audio.src = audioUrl;
    }

    return () => {
      alive = false;
      for (const u of urlsToRevoke) URL.revokeObjectURL(u);
    };
  }, [remote, sessionId, file.fileId, file.status, file.webPreviewKind, file.webPreviewMime, file.hasMediaDuration, blob]);

  return thumbUrl;
}

function tileAppearance(kind: SharedFileKind): { cls: string; icon: ReactElement } {
  switch (kind) {
    case SharedFileKind.VIDEO:
      return { cls: "v", icon: <VideoIcon /> };
    case SharedFileKind.IMAGE:
      return { cls: "p", icon: <ImageIcon /> };
    case SharedFileKind.AUDIO:
      return { cls: "d", icon: <MusicIcon /> };
    case SharedFileKind.ARCHIVE:
      return { cls: "z", icon: <FileArchiveIcon /> };
    case SharedFileKind.PDF:
    case SharedFileKind.DOC:
      return { cls: "d", icon: <FileTextIcon /> };
    default:
      return { cls: "d", icon: <FileIcon /> };
  }
}

function cardClass(status: SharedFileCardStatus): string {
  if (status === SharedFileCardStatus.FAILED) return "sf-fc sf-fail";
  if (status === SharedFileCardStatus.OFFLINE) return "sf-fc sf-off";
  if (status === SharedFileCardStatus.CLEANED) return "sf-fc sf-gone";
  return "sf-fc";
}

function CardVisuals({
  file,
  thumbUrl,
  caption,
}: {
  file: SessionSharedFileItem;
  thumbUrl?: string;
  caption: string;
}) {
  const nameHead = file.nameHead || file.displayName || file.name;
  const nameTail = file.nameTail;
  const { cls, icon } = tileAppearance(file.kind);
  const showThumb = file.status === SharedFileCardStatus.DOWNLOADED && !!thumbUrl;
  const isVideo = file.kind === SharedFileKind.VIDEO;
  const ringDash = `${Math.max(2, file.progress * 81.68).toFixed(1)} 82`;
  const primaryLabel = renderText(file.primaryLabel);

  return (
    <>
      {showThumb ? (
        <span className={"sf-tile thumb" + (isVideo ? " video" : "")}>
          <img src={thumbUrl} draggable={false} aria-hidden />
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
      {(file.status === SharedFileCardStatus.AVAILABLE || file.status === SharedFileCardStatus.OFFLINE) && (
        <span className="sf-act" aria-hidden>
          <ArrowDown />
        </span>
      )}
      {(file.status === SharedFileCardStatus.DOWNLOADING || file.status === SharedFileCardStatus.PAUSED) && (
        <span className="sf-ring" aria-hidden>
          <svg viewBox="0 0 30 30">
            <circle className="t" cx="15" cy="15" r="13" />
            <circle className="p" cx="15" cy="15" r="13" strokeDasharray={ringDash} />
          </svg>
          {file.status === SharedFileCardStatus.DOWNLOADING ? <i /> : <PlayIcon className="sf-ring-play" />}
        </span>
      )}
      {(file.status === SharedFileCardStatus.DOWNLOADED || file.status === SharedFileCardStatus.FAILED) && primaryLabel && (
        <span className="sf-pill">{primaryLabel}</span>
      )}
    </>
  );
}

interface MenuItemSpec {
  id: number;
  label: string;
  icon?: ReactElement;
  red: boolean;
  disabled: boolean;
  disabledReason?: string;
  group: number;
  onClick: () => void;
}

function menuIconFor(action: SharedFileMenuAction): ReactElement | undefined {
  switch (action) {
    case SharedFileMenuAction.DOWNLOAD:
    case SharedFileMenuAction.SAVE:
      return <DownloadIcon />;
    case SharedFileMenuAction.SHARE:
      return <ShareIcon />;
    case SharedFileMenuAction.DELETE_LOCAL:
      return <TrashIcon />;
    default:
      return undefined;
  }
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
  thumbUrl,
  sub,
  fullPath,
  sourceCaption,
  cardRect,
  items,
  onClose,
}: {
  file: SessionSharedFileItem;
  thumbUrl?: string;
  sub: string;
  fullPath: string;
  sourceCaption: string;
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
    "aria-label": i18n.t("shared_file.menu_a11y_label"),
  };

  return (
    <div className="sf-ctx-overlay" ref={rootRef} {...overlayDialogProps}>
      <div className="sf-blur" onClick={onClose} />
      <div className="sf-ctx" style={{ top, left }}>
        <div className={cardClass(file.status)} onClick={onClose}>
          <CardVisuals file={file} thumbUrl={thumbUrl} caption={sub} />
        </div>
        <div className="sf-menu" role="menu">
          <div className="sf-mh">
            <div>{fullPath}</div>
            {sourceCaption && <small>{sourceCaption}</small>}
            {file.subtitleIsError && <small className="sf-mh-err">{sub}</small>}
          </div>
          {items.map((item, idx) => {
            const { showGap, noSep } = menuSeparatorFlags(items, idx);
            return (
              <div key={item.id}>
                {showGap && <div className="sf-mgap" />}
                <button
                  type="button"
                  role="menuitem"
                  disabled={item.disabled}
                  title={item.disabledReason}
                  className={"sf-mi" + (item.red ? " red" : "") + (noSep ? " no-sep" : "")}
                  onClick={() => {
                    if (item.disabled) return;
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

function canSystemShareBlob(blob: Blob, displayName: string, previewMime: string): boolean {
  if (typeof navigator === "undefined" || typeof navigator.canShare !== "function") return false;
  try {
    const f = new File([blob], displayName || "file", {
      type: previewMime || "application/octet-stream",
    });
    return navigator.canShare({ files: [f] });
  } catch {
    return false;
  }
}

async function systemShareBlob(
  blob: Blob,
  displayName: string,
  previewMime: string,
  onToast: (msg: string) => void,
) {
  try {
    const f = new File([blob], displayName || "file", {
      type: previewMime || "application/octet-stream",
    });
    await navigator.share({ files: [f] });
  } catch (e) {
    if ((e as Error).name !== "AbortError") {
      onToast(i18n.t("shared_file.toast_share_failed", { reason: errorText(e) }));
    }
  }
}

/** A single agent-shared file card rendered from Core's SessionSharedFileItem. */
export function SharedFileCard({
  remote,
  sessionId,
  file,
}: {
  remote?: Remote;
  sessionId: string;
  file: SessionSharedFileItem;
}) {
  const blob = useLocalBlob(remote, sessionId, file);
  const thumbUrl = useLocalMediaThumb(remote, sessionId, file, blob);
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

  const displayName = file.displayName || file.name;
  const subtitle = renderText(file.subtitle);
  const fullPath = file.displayPath || displayName;
  const sourceCaption = renderText(file.sourceCaption);
  const a11yLabel = renderText(file.a11yLabel) || displayName;
  const canShare =
    file.status === SharedFileCardStatus.DOWNLOADED && blob
      ? canSystemShareBlob(blob, displayName, file.webPreviewMime)
      : false;

  const runMenuAction = (action: SharedFileMenuAction) => {
    if (!remote) return;
    const manager = sharedFilesOf(remote);
    switch (action) {
      case SharedFileMenuAction.DOWNLOAD:
      case SharedFileMenuAction.RESUME:
      case SharedFileMenuAction.RETRY:
        void manager.startDownload(sessionId, file, { autoOpenIfStaying: false });
        break;
      case SharedFileMenuAction.PAUSE:
        manager.pause(sessionId, file.fileId);
        break;
      case SharedFileMenuAction.CANCEL_DOWNLOAD:
        void manager.cancel(sessionId, file.fileId);
        break;
      case SharedFileMenuAction.SAVE:
        if (blob) saveBlobAsDownload(blob, displayName);
        break;
      case SharedFileMenuAction.SHARE:
        if (blob) void systemShareBlob(blob, displayName, file.webPreviewMime, onToast);
        break;
      case SharedFileMenuAction.DELETE_LOCAL:
        void manager.deleteLocal(sessionId, file.fileId);
        break;
      default:
        break;
    }
  };

  const menuItems: MenuItemSpec[] = [];
  let group = 0;
  for (const m of file.menuItems as readonly SharedFileMenuItemView[]) {
    if (
      m.action === SharedFileMenuAction.SAVE_TO_ALBUM ||
      m.action === SharedFileMenuAction.OPEN_WITH ||
      (m.action === SharedFileMenuAction.SHARE && !canShare) ||
      (m.action === SharedFileMenuAction.SAVE && !blob)
    ) {
      continue;
    }
    if (m.separated && menuItems.length > 0) group++;
    menuItems.push({
      id: m.action,
      label: renderText(m.label),
      icon: menuIconFor(m.action),
      red: m.destructive,
      disabled: !m.enabled,
      disabledReason: renderText(m.disabledReason) || undefined,
      group,
      onClick: () => runMenuAction(m.action),
    });
  }

  const interactive =
    file.primaryEnabled && file.primaryAction !== SharedFilePrimaryAction.UNSPECIFIED;

  const handlePrimaryAction = () => {
    if (suppressNextClick.current) {
      suppressNextClick.current = false;
      return;
    }
    if (!interactive || !remote) return;
    const manager = sharedFilesOf(remote);
    switch (file.primaryAction) {
      case SharedFilePrimaryAction.DOWNLOAD:
      case SharedFilePrimaryAction.RESUME:
      case SharedFilePrimaryAction.RETRY:
        void manager.startDownload(sessionId, file, { autoOpenIfStaying: true });
        break;
      case SharedFilePrimaryAction.PAUSE:
        manager.pause(sessionId, file.fileId);
        break;
      case SharedFilePrimaryAction.OPEN:
        if (blob) openFile(file, blob);
        break;
      default:
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
        aria-label={a11yLabel}
        className={cardClass(file.status)}
        onClick={handlePrimaryAction}
        onKeyDown={onKeyDown}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={clearLongPress}
        onPointerCancel={clearLongPress}
        onContextMenu={onContextMenu}
      >
        <CardVisuals file={file} thumbUrl={thumbUrl} caption={subtitle} />
      </div>

      {menuState && !wide && (
        <PhoneContextOverlay
          file={file}
          thumbUrl={thumbUrl}
          sub={subtitle}
          fullPath={fullPath}
          sourceCaption={sourceCaption}
          cardRect={menuState.rect}
          items={menuItems}
          onClose={() => setMenuState(undefined)}
        />
      )}

      {menuState && wide && (
        <Sheet anchor={cardRef} onClose={() => setMenuState(undefined)} className="menu">
          <div className="menu-info">
            <span className="mono sf-wide-path">{fullPath}</span>
            {sourceCaption && <span>{sourceCaption}</span>}
            {file.subtitleIsError && <span className="red">{subtitle}</span>}
          </div>
          {menuGroups.map((grp, g) => (
            <div className="list" role="menu" key={g}>
              {grp.map((item) => (
                <button
                  key={item.id}
                  type="button"
                  role="menuitem"
                  disabled={item.disabled}
                  title={item.disabledReason}
                  className={"mi" + (item.red ? " danger" : "")}
                  onClick={() => {
                    if (item.disabled) return;
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
}: {
  remote?: Remote;
  sessionId: string;
  files: readonly SessionSharedFileItem[];
}) {
  return (
    <div className="sf-files">
      {files.map((f) => (
        <SharedFileCard
          key={f.fileId}
          remote={remote}
          sessionId={sessionId}
          file={f}
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
  wide,
  target,
  onClose,
}: {
  remote: Remote;
  sessionId: string;
  agentName: string;
  wide: boolean;
  target: SharedFilePreviewTarget;
  onClose: () => void;
}) {
  const [mediaUrl, setMediaUrl] = useState("");
  const boxRef = useRef<HTMLDivElement>(null);
  useEscape(onClose);
  useDialogFocus(boxRef);

  const safeName = target.file.displayName || target.file.name;
  const pathText = target.file.displayPath || safeName;
  const sizeText = renderText(target.file.sizeText);

  useEffect(() => {
    if (target.kind === SharedFileWebPreviewKind.IMAGE) return;
    const url = URL.createObjectURL(target.previewBlob);
    setMediaUrl(url);
    return () => {
      URL.revokeObjectURL(url);
      setMediaUrl("");
    };
  }, [target]);

  if (target.kind === SharedFileWebPreviewKind.IMAGE) {
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
        sharedFile={{
          blob: target.previewBlob,
          name: safeName,
          path: pathText,
          sizeText,
        }}
      />
    );
  }

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
          <GlassButton label={i18n.t("shared_file.preview_close")} onClick={onClose}>
            <X />
          </GlassButton>
        </div>
        <div className="lb-mid sf-media-mid">
          {target.kind === SharedFileWebPreviewKind.VIDEO && mediaUrl && (
            <video className="sf-media-video" src={mediaUrl} controls autoPlay playsInline />
          )}
          {target.kind === SharedFileWebPreviewKind.AUDIO && mediaUrl && (
            <div className="sf-media-audio-wrap">
              <MusicIcon />
              <b>{safeName}</b>
              <audio className="sf-media-audio" src={mediaUrl} controls autoPlay />
            </div>
          )}
        </div>
        <div className="lb-foot">
          <span className="lb-info">
            {safeName} · <code>{pathText}</code> · {sizeText}
          </span>
          <button className="glass" onClick={() => saveBlobAsDownload(target.blob, safeName)}>
            {i18n.t("shared_file.menu_save_to_device")}
          </button>
          <button className="glass" onClick={onClose}>
            {i18n.t("shared_file.preview_close")}
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="viewer" ref={boxRef} {...dialogProps}>
      <div className="vbar">
        <GlassButton label={i18n.t("shared_file.preview_close")} onClick={onClose}>
          <X />
        </GlassButton>
        <b>{safeName}</b>
        <span />
      </div>
      <div className="vimg sf-media-mid">
        {target.kind === SharedFileWebPreviewKind.VIDEO && mediaUrl && (
          <video className="sf-media-video" src={mediaUrl} controls autoPlay playsInline />
        )}
        {target.kind === SharedFileWebPreviewKind.AUDIO && mediaUrl && (
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
        <div>{sizeText}</div>
      </div>
      <div className="vact">
        <button className="glass" onClick={() => saveBlobAsDownload(target.blob, safeName)}>
          {i18n.t("shared_file.menu_save_to_device")}
        </button>
        <button className="glass" onClick={onClose}>
          {i18n.t("shared_file.preview_close")}
        </button>
      </div>
    </div>
  );
}
