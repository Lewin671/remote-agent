import { createPortal } from "react-dom";
import {
  memo,
  useEffect,
  useRef,
  useState,
  type ClipboardEvent,
  type DragEvent,
  type ReactNode,
  type RefObject,
} from "react";
import type { AttachmentExecutor } from "../attachment-executor";
import type { DraftCandidateEntry } from "../core/host";
import { renderText } from "../core/text";
import {
  AttachmentItemKind,
  AttachmentItemState,
  type AttachmentDraftItemView,
  type AttachmentDraftView,
} from "../gen/remoteagent/core/v1/attachments_pb";
import * as i18n from "../i18n";
import { Check, FileIcon, ImageIcon, Plus, X } from "../icons";
import { useGrow } from "../motion";
import { Banner } from "../ui";
import { keepComposerFocus, useComposerMenuDismiss } from "./ComposerPicker";
import { FileChip } from "./Files";
import { DraftImageViewer, useLocalAttachmentBlob } from "./Images";

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

export function filesToCandidates(files: readonly File[]): DraftCandidateEntry[] {
  return files.map((f) => ({
    file: f,
    name: f.name,
    mimeType: f.type,
    sizeBytes: f.size,
  }));
}

function useLocalBlobUrl(attachments: AttachmentExecutor | undefined, ref: string | undefined): string | undefined {
  const blob = useLocalAttachmentBlob(attachments, ref);
  const [shown, setShown] = useState<{ blob: Blob; url: string }>();
  useEffect(() => {
    if (!blob) {
      setShown(undefined);
      return;
    }
    const u = URL.createObjectURL(blob);
    setShown({ blob, url: u });
    return () => URL.revokeObjectURL(u);
  }, [blob]);
  return shown && shown.blob === blob ? shown.url : undefined;
}

/** ＋ opens the library at once; when the machine offers file uploads it
 * opens a small menu above it instead: 照片和视频, 文件. */
export function AttachButton({
  draft,
  supportsFiles,
  blocked = "",
  onPickFiles,
  onBlocked,
  className = "cm-plus",
  label = <Plus />,
}: {
  draft?: AttachmentDraftView;
  supportsFiles?: boolean;
  blocked?: string;
  onPickFiles: (entries: DraftCandidateEntry[], forceFile: boolean) => void;
  onBlocked: (why: string) => void;
  className?: string;
  label?: ReactNode;
}) {
  const input = useRef<HTMLInputElement>(null);
  const files = useRef<HTMLInputElement>(null);
  const wrap = useRef<HTMLSpanElement>(null);
  const [open, setOpen] = useState(false);
  useComposerMenuDismiss(open, wrap, () => setOpen(false));

  const fileSupported = draft ? draft.galleryAllowsVideo : !!supportsFiles;
  const restoring = !!draft?.restoring;
  const accept = draft?.galleryAcceptMime || (fileSupported ? "image/*,video/*" : "image/*");
  const fileMetadataNotice = renderText(draft?.fileMetadataNotice);
  const imagesFull = !!draft && !fileSupported && !draft.canPickGallery && !draft.canPickImages;

  const pick = (forFiles: boolean) => {
    setOpen(false);
    if (forFiles) {
      if (draft && !draft.canPickFiles) {
        onBlocked(renderText(draft.filesPickBlockedReason) || blocked);
        return;
      }
      if (!draft && blocked) {
        onBlocked(blocked);
        return;
      }
      files.current?.click();
      return;
    }
    if (draft && !draft.canPickGallery) {
      onBlocked(renderText(draft.galleryBlockedReason) || renderText(draft.imagesPickBlockedReason) || blocked);
      return;
    }
    if (!draft && blocked) {
      onBlocked(blocked);
      return;
    }
    input.current?.click();
  };

  return (
    <span className="attach-wrap" ref={wrap}>
      <button
        type="button"
        disabled={restoring}
        className={className + (blocked || imagesFull ? " off" : "")}
        aria-label={fileSupported ? i18n.t("new_session.ios_attach") : i18n.t("image.add")}
        aria-expanded={fileSupported ? open : undefined}
        onPointerDown={keepComposerFocus}
        onMouseDown={keepComposerFocus}
        onClick={() => (blocked ? onBlocked(blocked) : fileSupported ? setOpen(!open) : pick(false))}
      >
        {label}
      </button>
      {open && (
        <div className="attach-menu glass" role="menu">
          <button type="button" role="menuitem" onClick={() => pick(false)}>
            <ImageIcon />
            {i18n.t("attachment.photos_videos")}
          </button>
          <button type="button" role="menuitem" onClick={() => pick(true)}>
            <FileIcon />
            {i18n.t("attachment.file")}
          </button>
          {fileMetadataNotice && <p>{fileMetadataNotice}</p>}
        </div>
      )}
      <FilePickers onPickFiles={onPickFiles} accept={accept} media={input} files={files} />
    </span>
  );
}

/** The hidden inputs the button opens. React sets an input's type and name
 * again each time it draws it, which the browser sees as a change of
 * the page: they are drawn apart from the button, which draws with every
 * keystroke in the message, and only when something they depend on changes. */
const FilePickers = memo(function FilePickers({
  onPickFiles,
  accept,
  media,
  files,
}: {
  onPickFiles: (entries: DraftCandidateEntry[], forceFile: boolean) => void;
  accept: string;
  media: RefObject<HTMLInputElement | null>;
  files: RefObject<HTMLInputElement | null>;
}) {
  return (
    <>
      <input
        ref={media}
        type="file"
        accept={accept}
        multiple
        hidden
        onChange={(e) => {
          const picked = attachmentFiles(e.currentTarget.files);
          if (picked.length) onPickFiles(filesToCandidates(picked), false);
          e.currentTarget.value = "";
        }}
      />
      <input
        ref={files}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          const picked = attachmentFiles(e.currentTarget.files);
          if (picked.length) onPickFiles(filesToCandidates(picked), true);
          e.currentTarget.value = "";
        }}
      />
    </>
  );
});

function Thumb({
  item,
  count,
  attachments,
  onOpen,
  onRemove,
}: {
  item: AttachmentDraftItemView;
  count: number;
  attachments?: AttachmentExecutor;
  onOpen: () => void;
  onRemove: () => void;
}) {
  const url = useLocalBlobUrl(attachments, item.thumbRef || item.preparedRef || item.sourceRef);
  const busy =
    item.state === AttachmentItemState.PREPARING ||
    item.state === AttachmentItemState.UPLOADING ||
    item.state === AttachmentItemState.WAITING_RECONNECT;
  const failed = item.state === AttachmentItemState.FAILED;
  const paused = item.state === AttachmentItemState.PAUSED;
  const statusText = renderText(item.statusText);
  const stateLabel = failed ? i18n.t("attachment.failure", { reason: renderText(item.problem?.message) || statusText }) : statusText;
  const index = item.imageIndex >= 0 ? item.imageIndex + 1 : 1;
  return (
    <span className="draft-thumb">
      <button
        type="button"
        className="pic tthumb draft-open"
        onClick={onOpen}
        aria-label={i18n.t("image.position_state", { index, count, state: stateLabel })}
      >
        {url && <img src={url} alt="" />}
        {busy && item.progress > 0 && item.progress < 1 ? (
          <span className="pct">
            <b style={{ width: `${Math.round(item.progress * 100)}%` }} />
          </span>
        ) : (
          busy && <span className="ring spin" aria-label={statusText || i18n.t("attachment.uploading")} />
        )}
        {paused && <span className="badge" aria-hidden="true">{statusText || i18n.t("attachment.upload_paused")}</span>}
        {failed && <span className="badge" aria-hidden="true">{i18n.t("attachment.failed")}</span>}
      </button>
      {item.canRemove && (
        <button
          type="button"
          className="x"
          onClick={onRemove}
          aria-label={renderText(item.removeA11y) || i18n.t("image.remove_index", { index })}
        >
          <span className="draft-remove-mark">
            <X />
          </span>
        </button>
      )}
    </span>
  );
}

/** A chosen video in the tray: its first frame with Core's probed duration in a
 * corner, or, when the frame cannot be read, the file icon and its name. */
function VideoThumb({
  item,
  attachments,
  onRemove,
  onRetry,
  onPause,
  onResume,
}: {
  item: AttachmentDraftItemView;
  attachments?: AttachmentExecutor;
  onRemove: () => void;
  onRetry: () => void;
  onPause?: () => void;
  onResume?: () => void;
}) {
  const url = useLocalBlobUrl(attachments, item.sourceRef);
  const [unreadable, setUnreadable] = useState(false);
  useEffect(() => {
    setUnreadable(false);
  }, [url]);
  const busy =
    item.state === AttachmentItemState.PREPARING ||
    item.state === AttachmentItemState.UPLOADING ||
    item.state === AttachmentItemState.WAITING_RECONNECT;
  const failed = item.state === AttachmentItemState.FAILED;
  const label = item.displayName;
  const errText = renderText(item.fileErrorText) || renderText(item.statusText);
  return (
    <span className={"pic tthumb video" + (unreadable ? " plain" : "")} title={errText || label}>
      {url && !unreadable && (
        <video
          src={url + "#t=0.1"}
          muted
          playsInline
          preload="metadata"
          aria-hidden
          onError={() => setUnreadable(true)}
        />
      )}
      {(unreadable || !url) && (
        <span className="vname">
          <FileIcon />
          <span>{label}</span>
        </span>
      )}
      {!unreadable && item.durationText && <span className="dur">{item.durationText}</span>}
      {busy && item.progress > 0 && item.progress < 1 ? (
        <span className="pct">
          <b style={{ width: `${Math.round(item.progress * 100)}%` }} />
        </span>
      ) : (
        busy && <span className="ring spin" aria-label={renderText(item.statusText) || i18n.t("attachment.uploading")} />
      )}
      {item.canPause && onPause && (
        <button type="button" className="badge" onClick={onPause}>
          {i18n.t("shared_file.action_pause")}
        </button>
      )}
      {item.canResume && onResume && (
        <button type="button" className="badge" onClick={onResume}>
          {i18n.t("schedule.resume")}
        </button>
      )}
      {failed && item.canRetry && (
        <button type="button" className="badge retry" onClick={onRetry} title={errText}>
          {i18n.t("common.retry")}
        </button>
      )}
      {item.canRemove && (
        <button
          type="button"
          className="x"
          onClick={onRemove}
          aria-label={renderText(item.removeA11y) || i18n.t("attachment.remove_video", { name: label }).trim()}
        >
          <X />
        </button>
      )}
    </span>
  );
}

export interface TrayViewProps {
  draft?: AttachmentDraftView;
  attachments?: AttachmentExecutor;
  onRemove?: (itemId: string) => void;
  onRetry?: (itemId: string) => void;
  onPause?: (itemId: string) => void;
  onResume?: (itemId: string) => void;
  onSetOriginal?: (original: boolean) => void;
  onOpenViewer?: (index: number) => void;
  onNavigateViewer?: (delta: number) => void;
  onCloseViewer?: () => void;
  note?: ReactNode;
}

const noopStr = (_id: string) => undefined;
const noopBool = (_v: boolean) => undefined;
const noopNum = (_n: number) => undefined;
const noop = () => undefined;

/** The chosen attachments above the text, rendered purely from Core's AttachmentDraftView. */
export function TrayView(props: TrayViewProps) {
  const { draft, note } = props;
  const noticeText = renderText(draft?.notice);
  const storageError = renderText(draft?.storageError);
  const failureSummary = renderText(draft?.failureSummary);
  if (!draft || (draft.items.length === 0 && !noticeText && !storageError && !failureSummary)) {
    return <>{note}</>;
  }
  return <Attachments {...props} draft={draft} />;
}

/** The tray itself: it opens to its height as the first picture comes and
 * closes up as the last goes, the input moving with it. */
function Attachments({
  draft,
  attachments,
  onRemove = noopStr,
  onRetry = noopStr,
  onPause,
  onResume,
  onSetOriginal = noopBool,
  onOpenViewer = noopNum,
  onNavigateViewer = noopNum,
  onCloseViewer = noop,
  note,
}: TrayViewProps & { draft: AttachmentDraftView }) {
  const ref = useRef<HTMLDivElement>(null);
  useGrow(ref);
  const images = draft.images;
  // Videos from the library sit among the images, in the order chosen;
  // other files are chips.
  const thumbs = draft.items.filter((i) => i.kind === AttachmentItemKind.IMAGE || i.isVideo);
  const files = draft.files.filter((i) => !i.isVideo);
  const failureSummary = renderText(draft.failureSummary);
  const storageError = renderText(draft.storageError);
  const noticeText = renderText(draft.notice);
  const originalHint = renderText(draft.originalHint);
  const fileMetadataNotice = renderText(draft.fileMetadataNotice);
  const originalTotalSizeText = renderText(draft.originalTotalSizeText);
  const originalDisabledReason = renderText(draft.originalToggleDisabledReason);

  return (
    <div className="attachments" ref={ref}>
      {thumbs.length > 0 && (
        <div className="att-tray">
          {thumbs.map((i) =>
            i.kind === AttachmentItemKind.FILE ? (
              <VideoThumb
                key={i.itemId}
                item={i}
                attachments={attachments}
                onRemove={() => onRemove(i.itemId)}
                onRetry={() => onRetry(i.itemId)}
                onPause={onPause ? () => onPause(i.itemId) : undefined}
                onResume={onResume ? () => onResume(i.itemId) : undefined}
              />
            ) : (
              <Thumb
                key={i.itemId}
                item={i}
                count={images.length}
                attachments={attachments}
                onOpen={() => onOpenViewer(i.imageIndex >= 0 ? i.imageIndex : 0)}
                onRemove={() => onRemove(i.itemId)}
              />
            ),
          )}
        </div>
      )}
      {images.length > 0 && (
        <div className="att-meta">
          <button
            type="button"
            className={"orig" + (draft.original ? " on" : "")}
            role="switch"
            aria-checked={draft.original}
            disabled={!draft.canToggleOriginal}
            title={originalDisabledReason || undefined}
            onClick={() => onSetOriginal(!draft.original)}
          >
            <i>{draft.original && <Check />}</i>
            {i18n.t("image.original")}
            {originalTotalSizeText}
          </button>
          <span>{renderText(draft.imageCountText)}</span>
        </div>
      )}
      {draft.original && images.length > 0 && originalHint && <div className="att-hint">{originalHint}</div>}
      {files.length > 0 && (
        <div className="file-chips">
          {files.map((i) => (
            <FileChip
              key={i.itemId}
              file={{
                name: i.displayName,
                displayName: i.displayName,
                nameHead: i.nameHead,
                nameTail: i.nameTail,
                sizeText: renderText(i.sizeText),
              }}
              state={i.state}
              progress={i.progress}
              statusText={renderText(i.statusText)}
              error={renderText(i.fileErrorText)}
              removeA11y={renderText(i.removeA11y)}
              canPause={i.canPause}
              canResume={i.canResume}
              canRetry={i.canRetry}
              canRemove={i.canRemove}
              onPause={onPause ? () => onPause(i.itemId) : undefined}
              onResume={onResume ? () => onResume(i.itemId) : undefined}
              onRetry={() => onRetry(i.itemId)}
              onRemove={() => onRemove(i.itemId)}
            />
          ))}
        </div>
      )}
      {draft.files.length > 0 && fileMetadataNotice && <div className="att-hint">{fileMetadataNotice}</div>}
      {storageError && <Banner kind="err">{storageError}</Banner>}
      {failureSummary && <Banner kind="err">{failureSummary}</Banner>}
      {noticeText && <Banner>{noticeText}</Banner>}
      {note}
      {draft.imageViewer?.open &&
        createPortal(
          <DraftImageViewer
            viewer={draft.imageViewer}
            attachments={attachments}
            onNavigate={onNavigateViewer}
            onRetry={onRetry}
            onRemove={onRemove}
            onClose={onCloseViewer}
          />,
          document.body,
        )}
    </div>
  );
}

/** Drop and paste on a card: candidates are routed and validated in Core via DraftAddItemsAction. */
export function useDropPaste(
  onDropEntries: (entries: DraftCandidateEntry[], forceFile: boolean) => void,
  blocked: string,
  onBlocked: (why: string) => void,
) {
  const [over, setOver] = useState(false);
  const takeEntries = (entries: DraftCandidateEntry[]) => {
    if (!entries.length) return false;
    if (blocked) {
      onBlocked(blocked);
      return true;
    }
    onDropEntries(entries, false);
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
        const entries: DraftCandidateEntry[] = items.length
          ? items
              .filter((item) => item.kind === "file")
              .map((item) => {
                const isDirectory = !!item.webkitGetAsEntry?.()?.isDirectory;
                const file = isDirectory ? undefined : (item.getAsFile() ?? undefined);
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
