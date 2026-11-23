import { renderText } from "../core/text";
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
export function FileChip({ file, state, progress = 0, error, onRetry, onRemove }: {
  file: FileChipInfo;
  state?: "preparing" | "uploading" | "done" | "failed";
  progress?: number;
  error?: string;
  onRetry?: () => void;
  onRemove?: () => void;
}) {
  const label = file.displayName || file.name;
  const head = file.nameHead || label;
  const tail = file.nameTail ?? "";
  const size = file.sizeText ?? "";
  const busy = state === "preparing" || state === "uploading";
  return <span className={"file-chip" + (state === "failed" ? " failed" : "")} title={error || label}>
    <FileIcon />
    <span className="file-info">
      <span className="file-name" aria-label={label}><span>{head}</span><span>{tail && <bdi dir="ltr">{tail}</bdi>}</span></span>
      <small>{size}{busy && " · " + (state === "preparing" ? i18n.t("attachment.preparing") : i18n.t("attachment.upload_progress", { percent: Math.round(progress * 100) }))}{state === "failed" && " · " + i18n.t("attachment.upload_failed")}</small>
      {busy && <progress max={1} value={progress} aria-label={i18n.t("attachment.file_progress")} />}
    </span>
    {state === "failed" && onRetry && <button onClick={onRetry}>{i18n.t("common.retry")}</button>}
    {onRemove && <button onClick={onRemove} aria-label={i18n.t("attachment.remove_file", { name: label })}><X /></button>}
  </span>;
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
