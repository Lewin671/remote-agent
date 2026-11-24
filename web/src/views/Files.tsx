import { renderText } from "../core/text";
import { AttachmentItemState } from "../gen/remoteagent/core/v1/attachments_pb";
import type { SessionFileRefView } from "../gen/remoteagent/core/v1/session_view_pb";
import * as i18n from "../i18n";
import { FileIcon, X } from "../icons";

export interface FileChipInfo {
  fileId?: string;
  name: string;
  displayName?: string;
  nameHead?: string;
  nameTail?: string;
  sizeText?: string;
}

/** One chip in drafts, pending messages and recorded user messages. */
export function FileChip({
  file,
  state,
  progress = 0,
  statusText,
  error,
  removeA11y,
  canPause,
  canResume,
  canRetry,
  canRemove = true,
  onPause,
  onResume,
  onRetry,
  onRemove,
}: {
  file: FileChipInfo;
  state?: AttachmentItemState;
  progress?: number;
  statusText?: string;
  error?: string;
  removeA11y?: string;
  canPause?: boolean;
  canResume?: boolean;
  canRetry?: boolean;
  canRemove?: boolean;
  onPause?: () => void;
  onResume?: () => void;
  onRetry?: () => void;
  onRemove?: () => void;
}) {
  const label = file.displayName || file.name;
  const head = file.nameHead || label;
  const tail = file.nameTail ?? "";
  const size = file.sizeText ?? "";
  const busy =
    state === AttachmentItemState.PREPARING ||
    state === AttachmentItemState.UPLOADING ||
    state === AttachmentItemState.WAITING_RECONNECT;
  const failed = state === AttachmentItemState.FAILED;
  const status = statusText || (failed ? i18n.t("attachment.upload_failed") : "");
  const meta = size && status ? `${size} · ${status}` : size || status;
  return (
    <span className={"file-chip" + (failed ? " failed" : "")} title={error || status || label}>
      <FileIcon />
      <span className="file-info">
        <span className="file-name" aria-label={label}>
          <span>{head}</span>
          <span>{tail && <bdi dir="ltr">{tail}</bdi>}</span>
        </span>
        {meta && <small>{meta}</small>}
        {busy && <progress max={1} value={progress} aria-label={i18n.t("attachment.file_progress")} />}
      </span>
      {canPause && onPause && <button type="button" onClick={onPause}>{i18n.t("shared_file.action_pause")}</button>}
      {canResume && onResume && <button type="button" onClick={onResume}>{i18n.t("schedule.resume")}</button>}
      {(canRetry ?? failed) && onRetry && <button type="button" onClick={onRetry}>{i18n.t("common.retry")}</button>}
      {canRemove && onRemove && (
        <button type="button" onClick={onRemove} aria-label={removeA11y || i18n.t("attachment.remove_file", { name: label })}>
          <X />
        </button>
      )}
    </span>
  );
}

export function FileChips({ files }: { files?: readonly SessionFileRefView[] }) {
  return files?.length ? (
    <div className="file-chips">
      {files.map((f, i) => (
        <FileChip
          key={f.path || f.name || String(i)}
          file={{
            name: f.name,
            displayName: f.displayName,
            nameHead: f.nameHead,
            nameTail: f.nameTail,
            sizeText: renderText(f.sizeText),
          }}
        />
      ))}
    </div>
  ) : null;
}
