import * as i18n from "../i18n";
import type { SentFile } from "../gen/remoteagent/v1/event_pb";
import { FileIcon, X } from "../icons";

export function fileSize(n: number | bigint): string {
  const bytes = Number(n);
  return bytes < 1024 ? `${bytes} B` : bytes < 1024 * 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

/** One chip in drafts, pending messages and recorded user messages. */
export function FileChip({ file, state, progress = 0, error, onRetry, onRemove }: {
  file: Pick<SentFile, "name" | "sizeBytes">;
  state?: "preparing" | "uploading" | "done" | "failed";
  progress?: number;
  error?: string;
  onRetry?: () => void;
  onRemove?: () => void;
}) {
  const chars = Array.from(file.name);
  const split = Math.ceil(chars.length / 2);
  const busy = state === "preparing" || state === "uploading";
  return <span className={"file-chip" + (state === "failed" ? " failed" : "")} title={error || file.name}>
    <FileIcon />
    <span className="file-info">
      <span className="file-name" aria-label={file.name}><span>{chars.slice(0, split).join("")}</span><span><bdi dir="ltr">{chars.slice(split).join("")}</bdi></span></span>
      <small>{fileSize(file.sizeBytes)}{busy && " · " + (state === "preparing" ? i18n.t("attachment.preparing") : i18n.t("attachment.upload_progress", { percent: Math.round(progress * 100) }))}{state === "failed" && " · " + i18n.t("attachment.upload_failed")}</small>
      {busy && <progress max={1} value={progress} aria-label={i18n.t("attachment.file_progress")} />}
    </span>
    {state === "failed" && onRetry && <button onClick={onRetry}>{i18n.t("common.retry")}</button>}
    {onRemove && <button onClick={onRemove} aria-label={i18n.t("attachment.remove_file", { name: file.name })}><X /></button>}
  </span>;
}
export function FileChips({ files }: { files?: Pick<SentFile, "name" | "sizeBytes" | "fileId">[] }) {
  return files?.length ? <div className="file-chips">{files.map((f) => <FileChip key={f.fileId} file={f} />)}</div> : null;
}
