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
      <small>{fileSize(file.sizeBytes)}{busy && ` · ${state === "preparing" ? "准备中" : `上传 ${Math.round(progress * 100)}%`}`}{state === "failed" && " · 上传失败"}</small>
      {busy && <progress max={1} value={progress} aria-label="文件上传进度" />}
    </span>
    {state === "failed" && onRetry && <button onClick={onRetry}>重试</button>}
    {onRemove && <button onClick={onRemove} aria-label={`移除文件 ${file.name}`}><X /></button>}
  </span>;
}
export function FileChips({ files }: { files?: Pick<SentFile, "name" | "sizeBytes" | "fileId">[] }) {
  return files?.length ? <div className="file-chips">{files.map((f) => <FileChip key={f.fileId} file={f} />)}</div> : null;
}

/** Hide only complete, attested path lines; leave all other text verbatim. */
export function withoutFileLines(text: string, files: Pick<SentFile, "path">[]): string {
  const paths = new Set(files.map((f) => f.path).filter(Boolean));
  const lines = text.split("\n");
  const hidden = new Set<number>();
  lines.forEach((line, i) => {
    if (paths.has(line) || line.startsWith("@") && paths.has(line.slice(1))) {
      hidden.add(i);
      if (i > 0 && lines[i - 1] === "") hidden.add(i - 1);
    }
  });
  return lines.filter((_, i) => !hidden.has(i)).join("\n");
}
