import * as i18n from "./i18n";
import type { SharedFile } from "./gen/remoteagent/v1/event_pb";
import { hasQuotaExceeded, OpfsSharedFileStore, type SharedFileStore } from "./shared-file-store";

export const DEFAULT_SHARED_FILE_CHUNK_BYTES = 786_432;
/** Files strictly larger than 50 MB are saved to disk on completion without local OPFS/memory caching. */
export const LOCAL_CACHE_MAX_BYTES = 50 * 1024 * 1024;
export const AUTO_RESUME_TIMEOUT_MS = 30_000;
export const ETA_DELAY_MS = 3_000;

export type SharedFileKind = "video" | "image" | "audio" | "archive" | "pdf" | "file";
export type SharedFilePreviewKind = "image" | "video" | "audio";
export type SharedFileStatus =
  | "available"
  | "downloading"
  | "paused"
  | "downloaded"
  | "failed"
  | "offline"
  | "cleaned";

export type SharedFileFailKind = "checksum" | "disconnected" | "storage" | "other";

export interface SharedFileTransferState {
  status: "available" | "downloading" | "paused" | "downloaded" | "failed";
  downloadedBytes: number;
  startedAtMs?: number;
  etaSeconds?: number;
  failKind?: SharedFileFailKind;
  failReason?: string;
  blob?: Blob;
  waitingReconnect?: boolean;
  /** Stored only while actively hydrating from OPFS/store. */
  hydrated?: boolean;
}

export interface SharedFileDerivedState {
  status: SharedFileStatus;
  downloadedBytes: number;
  totalBytes: number;
  progress: number;
  etaSeconds?: number;
  failKind?: SharedFileFailKind;
  failReason?: string;
  blob?: Blob;
  waitingReconnect: boolean;
}

export interface SharedFileChunkResult {
  data: Uint8Array;
  totalBytes?: bigint;
}

export interface SharedFileEnvironment {
  getChunk(sessionId: string, fileId: string, offset: bigint): Promise<SharedFileChunkResult>;
  isConnected(): boolean;
  isMachineOnline(sessionId: string): boolean;
  isSessionEnded(sessionId: string): boolean;
  sharedFileChunkBytes(sessionId: string): number;
  store?: SharedFileStore;
  now?: () => number;
  onSaveLargeFile?: (blob: Blob, safeName: string, file: SharedFile) => void;
  onAutoOpen?: (sessionId: string, file: SharedFile, blob: Blob) => void;
}

const IMAGE_PREVIEW_MIME: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
};

const VIDEO_PREVIEW_MIME: Record<string, string> = {
  mp4: "video/mp4",
  webm: "video/webm",
  mov: "video/quicktime",
  m4v: "video/mp4",
};

const AUDIO_PREVIEW_MIME: Record<string, string> = {
  mp3: "audio/mpeg",
  m4a: "audio/mp4",
  wav: "audio/wav",
  ogg: "audio/ogg",
};

const EXTRA_VIDEO_EXTS = new Set(["mkv", "avi", "flv", "wmv"]);
const EXTRA_AUDIO_EXTS = new Set(["aac", "flac", "opus"]);
const ARCHIVE_EXTS = new Set(["zip", "tar", "gz", "tgz", "bz2", "xz", "7z", "rar", "zst"]);

/** Removes path separators, ASCII controls, and Unicode bidirectional controls from a filename. */
export function sanitizeFileName(raw: string): string {
  const cleaned = raw
    .replace(/[/\\\u0000-\u001f\u007f\u200e\u200f\u061c\u202a-\u202e\u2066-\u2069]/g, "")
    .trim();
  if (!cleaned || cleaned === "." || cleaned === "..") return "file";
  return cleaned;
}

export function extensionOf(name: string): string {
  const safe = sanitizeFileName(name);
  const dot = safe.lastIndexOf(".");
  if (dot <= 0 || dot === safe.length - 1) return "";
  return safe.slice(dot + 1).toLowerCase();
}

export function classifySharedFile(name: string): {
  kind: SharedFileKind;
  previewKind?: SharedFilePreviewKind;
  previewMime?: string;
} {
  const ext = extensionOf(name);
  if (ext in VIDEO_PREVIEW_MIME) {
    return { kind: "video", previewKind: "video", previewMime: VIDEO_PREVIEW_MIME[ext] };
  }
  if (EXTRA_VIDEO_EXTS.has(ext)) {
    return { kind: "video" };
  }
  if (ext in IMAGE_PREVIEW_MIME) {
    return { kind: "image", previewKind: "image", previewMime: IMAGE_PREVIEW_MIME[ext] };
  }
  if (ext in AUDIO_PREVIEW_MIME) {
    return { kind: "audio", previewKind: "audio", previewMime: AUDIO_PREVIEW_MIME[ext] };
  }
  if (EXTRA_AUDIO_EXTS.has(ext)) {
    return { kind: "audio" };
  }
  if (ARCHIVE_EXTS.has(ext)) {
    return { kind: "archive" };
  }
  if (ext === "pdf") {
    return { kind: "pdf" };
  }
  return { kind: "file" };
}

export function kindName(kind: SharedFileKind): string {
  switch (kind) {
    case "video":
      return i18n.t("shared_file.type_video");
    case "image":
      return i18n.t("shared_file.type_image");
    case "audio":
      return i18n.t("shared_file.type_audio");
    case "archive":
      return i18n.t("shared_file.type_archive");
    case "pdf":
      return i18n.t("shared_file.type_pdf");
    case "file":
      return i18n.t("shared_file.type_other");
  }
}

/** Splits a sanitized filename into [head, tail] so middle ellipsis preserves the extension. */
export function splitFileName(raw: string): [string, string] {
  const name = sanitizeFileName(raw);
  const dot = name.lastIndexOf(".");
  if (dot <= 0 || dot === name.length - 1 || name.length - dot > 10) {
    return [name, ""];
  }
  const stem = name.slice(0, dot);
  const ext = name.slice(dot);
  const tailKeep = stem.length > 6 ? 2 : 0;
  if (tailKeep === 0) return [stem, ext];
  return [stem.slice(0, stem.length - tailKeep), stem.slice(stem.length - tailKeep) + ext];
}

export function formatMediaDuration(totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds < 0) return "0:00";
  const secs = Math.floor(totalSeconds);
  const h = Math.floor(secs / 3600);
  const m = Math.floor((secs % 3600) / 60);
  const s = secs % 60;
  if (h > 0) {
    return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
  }
  return `${m}:${String(s).padStart(2, "0")}`;
}

/**
 * Downloaded bytes written without a unit, in the unit of the total
 * ("8.1" for "8.1 / 24.3 MB"), same rounding as the file-size label.
 */
export function fileSizeDone(done: number | bigint, total: number | bigint): string {
  const d = Number(done);
  const t = Number(total);
  if (t < 1024) return `${d}`;
  if (t < 1024 * 1024) return (d / 1024).toFixed(1);
  return (d / 1024 / 1024).toFixed(1);
}

export function formatEta(seconds: number): string {
  const s = Math.max(1, Math.round(seconds));
  if (s < 60) return i18n.t("shared_file.eta_seconds", { count: s });
  if (s < 3600) return i18n.t("shared_file.eta_minutes", { count: Math.ceil(s / 60) });
  return i18n.t("shared_file.eta_hours", { count: Math.ceil(s / 3600) });
}

export function failureNotice(failKind?: SharedFileFailKind, reason?: string): string {
  switch (failKind) {
    case "checksum":
      return i18n.t("shared_file.subtitle_failed", {
        reason: i18n.t("shared_file.reason_checksum_mismatch"),
      });
    case "disconnected":
      return i18n.t("shared_file.subtitle_failed", {
        reason: i18n.t("shared_file.reason_disconnected"),
      });
    case "storage":
      return i18n.t("shared_file.subtitle_failed", {
        reason: i18n.t("shared_file.reason_browser_storage_full"),
      });
    default:
      return i18n.t("shared_file.subtitle_failed", {
        reason: reason?.trim() || i18n.t("shared_file.reason_unknown"),
      });
  }
}

/** Triggers a generic browser download save with forced application/octet-stream MIME type. */
export function saveBlobAsDownload(blob: Blob, rawFileName: string) {
  const safeName = sanitizeFileName(rawFileName);
  const safeBlob =
    blob.type === "application/octet-stream"
      ? blob
      : new Blob([blob], { type: "application/octet-stream" });
  const url = URL.createObjectURL(safeBlob);
  const a = document.createElement("a");
  a.href = url;
  a.download = safeName;
  a.style.display = "none";
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** Wraps a locally cached Blob in a client-assigned preview MIME if and only if extension is whitelisted. */
export function previewBlobForFile(blob: Blob, rawFileName: string): { kind: SharedFilePreviewKind; blob: Blob } | undefined {
  const info = classifySharedFile(rawFileName);
  if (!info.previewKind || !info.previewMime) return undefined;
  return {
    kind: info.previewKind,
    blob: new Blob([blob], { type: info.previewMime }),
  };
}

function bytesToHex(bytes: Uint8Array): string {
  let out = "";
  for (const b of bytes) out += b.toString(16).padStart(2, "0");
  return out;
}

function equalBytes(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

async function sha256(blob: Blob): Promise<Uint8Array> {
  const buf = await blob.arrayBuffer();
  const digest = await crypto.subtle.digest("SHA-256", buf);
  return new Uint8Array(digest);
}

function isLinkDrop(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const code = (err as { code?: string }).code;
  return code === "unavailable" || code === "unknown";
}

function rawFailureDetail(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  return String(err || i18n.t("shared_file.reason_unknown"));
}

interface InternalEntry extends SharedFileTransferState {
  key: string;
  sessionId: string;
  fileId: string;
  runToken: number;
  pausedByUser: boolean;
  canceledByUser: boolean;
  /** Set when the user clicks the card in session view; cleared if user navigates away. */
  autoOpenSessionId?: string;
  reconnectResolve?: () => void;
  hydrating?: Promise<void>;
}

/** Coordinates chunked shared-file downloads, resume/cancel, OPFS/memory persistence,
 * auto-resume on transient disconnects, and duplicate fileId state sharing. */
export class SharedFileManager {
  private readonly env: SharedFileEnvironment;
  private readonly store: SharedFileStore;
  private readonly entries = new Map<string, InternalEntry>();
  private readonly listeners = new Set<() => void>();
  private currentSessionId: string | undefined;
  private autoOpenHandler?: (sessionId: string, file: SharedFile, blob: Blob) => void;
  private nextToken = 1;

  constructor(env: SharedFileEnvironment) {
    this.env = env;
    this.store = env.store ?? new OpfsSharedFileStore();
  }

  private now(): number {
    return (this.env.now ?? Date.now)();
  }

  private key(sessionId: string, fileId: string): string {
    return `${sessionId}\0${fileId}`;
  }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  private emit() {
    for (const listener of this.listeners) listener();
  }

  private entryFor(sessionId: string, fileId: string): InternalEntry {
    const k = this.key(sessionId, fileId);
    let e = this.entries.get(k);
    if (!e) {
      e = {
        key: k,
        sessionId,
        fileId,
        status: "available",
        downloadedBytes: 0,
        waitingReconnect: false,
        runToken: 0,
        pausedByUser: false,
        canceledByUser: false,
      };
      this.entries.set(k, e);
    }
    return e;
  }

  /** Registers a UI callback to open completed files when the user stayed in the session. */
  setAutoOpenHandler(fn?: (sessionId: string, file: SharedFile, blob: Blob) => void) {
    this.autoOpenHandler = fn;
  }

  /** Tracks which session the user currently has open; leaving cancels pending auto-open. */
  setCurrentSession(sessionId: string | undefined) {
    this.currentSessionId = sessionId;
    for (const e of this.entries.values()) {
      if (e.autoOpenSessionId && e.autoOpenSessionId !== sessionId) {
        e.autoOpenSessionId = undefined;
      }
    }
  }

  /** Notifies active waits when link/machine online state changes. */
  onConnectivityChange() {
    for (const e of this.entries.values()) {
      if (
        e.reconnectResolve &&
        this.env.isConnected() &&
        this.env.isMachineOnline(e.sessionId)
      ) {
        const resolve = e.reconnectResolve;
        e.reconnectResolve = undefined;
        resolve();
      }
    }
  }

  /** Seeds a completed local blob directly (used by demo mode and tests). */
  seedDownloaded(sessionId: string, fileId: string, blob: Blob) {
    this.seedState(sessionId, fileId, {
      status: "downloaded",
      downloadedBytes: blob.size,
      blob,
    });
  }

  /** Seeds an arbitrary transfer state for demo showcases and component tests. */
  seedState(
    sessionId: string,
    fileId: string,
    patch: Partial<
      Pick<
        SharedFileTransferState,
        "status" | "downloadedBytes" | "etaSeconds" | "failKind" | "failReason" | "blob" | "waitingReconnect"
      >
    >,
  ) {
    const e = this.entryFor(sessionId, fileId);
    e.runToken++;
    e.pausedByUser = patch.status === "paused";
    e.canceledByUser = false;
    e.status = patch.status ?? "available";
    e.downloadedBytes = patch.downloadedBytes ?? (patch.blob ? patch.blob.size : 0);
    e.etaSeconds = patch.etaSeconds;
    e.failKind = patch.failKind;
    e.failReason = patch.failReason;
    e.blob = patch.blob;
    e.waitingReconnect = patch.waitingReconnect ?? false;
    e.hydrated = true;
    this.emit();
  }

  /** Hydrates saved OPFS/store blob and partial download metadata if present. */
  hydrate(sessionId: string, file: SharedFile): Promise<void> {
    const e = this.entryFor(sessionId, file.fileId);
    if (e.hydrated) return Promise.resolve();
    if (e.hydrating) return e.hydrating;
    const p = (async () => {
      const completed = await this.store.getCompletedBlob(sessionId, file.fileId);
      if (completed && BigInt(completed.size) === file.sizeBytes) {
        e.status = "downloaded";
        e.downloadedBytes = completed.size;
        e.blob = completed;
        e.hydrated = true;
        this.emit();
        return;
      }
      if (e.status === "available") {
        const meta = await this.store.getPartialMeta(sessionId, file.fileId);
        const chunkBytes = this.chunkSizeFor(sessionId);
        if (
          meta &&
          meta.sizeBytes === file.sizeBytes.toString() &&
          meta.chunkBytes === chunkBytes &&
          meta.sha256Hex === bytesToHex(file.sha256) &&
          meta.nextOffset > 0 &&
          meta.nextOffset <= Number(file.sizeBytes)
        ) {
          e.status = "paused";
          e.downloadedBytes = meta.nextOffset;
          this.emit();
        }
      }
      e.hydrated = true;
    })().finally(() => {
      e.hydrating = undefined;
    });
    e.hydrating = p;
    return p;
  }

  stateOf(sessionId: string, file: SharedFile): SharedFileDerivedState {
    const e = this.entries.get(this.key(sessionId, file.fileId));
    const totalBytes = Number(file.sizeBytes);
    const ended = this.env.isSessionEnded(sessionId);
    const online = this.env.isConnected() && this.env.isMachineOnline(sessionId);

    if (e?.status === "downloaded" && e.blob) {
      return {
        status: "downloaded",
        downloadedBytes: totalBytes,
        totalBytes,
        progress: 1,
        blob: e.blob,
        waitingReconnect: false,
      };
    }
    if (ended) {
      return {
        status: "cleaned",
        downloadedBytes: 0,
        totalBytes,
        progress: 0,
        waitingReconnect: false,
      };
    }
    if (e?.status === "downloading") {
      const done = Math.min(e.downloadedBytes, totalBytes);
      return {
        status: "downloading",
        downloadedBytes: done,
        totalBytes,
        progress: totalBytes > 0 ? Math.min(1, done / totalBytes) : 0,
        etaSeconds: e.etaSeconds,
        waitingReconnect: !!e.waitingReconnect,
      };
    }
    if (!online) {
      // Unfinished files render as offline when the computer cannot be reached.
      return {
        status: "offline",
        downloadedBytes: e?.downloadedBytes ?? 0,
        totalBytes,
        progress: totalBytes > 0 ? Math.min(1, (e?.downloadedBytes ?? 0) / totalBytes) : 0,
        waitingReconnect: false,
      };
    }
    if (e?.status === "paused") {
      const done = Math.min(e.downloadedBytes, totalBytes);
      return {
        status: "paused",
        downloadedBytes: done,
        totalBytes,
        progress: totalBytes > 0 ? Math.min(1, done / totalBytes) : 0,
        waitingReconnect: false,
      };
    }
    if (e?.status === "failed") {
      const done = Math.min(e.downloadedBytes, totalBytes);
      return {
        status: "failed",
        downloadedBytes: done,
        totalBytes,
        progress: totalBytes > 0 ? Math.min(1, done / totalBytes) : 0,
        failKind: e.failKind,
        failReason: e.failReason,
        waitingReconnect: false,
      };
    }
    return {
      status: "available",
      downloadedBytes: 0,
      totalBytes,
      progress: 0,
      waitingReconnect: false,
    };
  }

  private chunkSizeFor(sessionId: string): number {
    const v = this.env.sharedFileChunkBytes(sessionId);
    return v > 0 ? v : DEFAULT_SHARED_FILE_CHUNK_BYTES;
  }

  async startDownload(
    sessionId: string,
    file: SharedFile,
    opts?: { autoOpenIfStaying?: boolean },
  ): Promise<void> {
    const derived = this.stateOf(sessionId, file);
    if (
      derived.status === "downloaded" ||
      derived.status === "downloading" ||
      derived.status === "cleaned" ||
      derived.status === "offline"
    ) {
      return;
    }

    const e = this.entryFor(sessionId, file.fileId);
    const token = this.nextToken++;
    e.runToken = token;
    e.pausedByUser = false;
    e.canceledByUser = false;
    e.waitingReconnect = false;
    e.failKind = undefined;
    e.failReason = undefined;
    e.etaSeconds = undefined;
    e.startedAtMs = this.now();
    e.autoOpenSessionId = opts?.autoOpenIfStaying ? sessionId : undefined;
    e.status = "downloading";
    this.emit();

    const chunkBytes = this.chunkSizeFor(sessionId);
    const totalBytes = Number(file.sizeBytes);
    const shaHex = bytesToHex(file.sha256);

    let offset = 0;
    let attemptBaselineBytes = 0;
    let zeroByteDone = false;

    const existingMeta = await this.store.getPartialMeta(sessionId, file.fileId);
    if (
      existingMeta &&
      existingMeta.sizeBytes === file.sizeBytes.toString() &&
      existingMeta.chunkBytes === chunkBytes &&
      existingMeta.sha256Hex === shaHex &&
      existingMeta.nextOffset >= 0 &&
      existingMeta.nextOffset <= totalBytes &&
      !(totalBytes === 0 && existingMeta.nextOffset === 0)
    ) {
      offset = existingMeta.nextOffset;
      e.downloadedBytes = offset;
      attemptBaselineBytes = offset;
      this.emit();
    } else if (existingMeta) {
      await this.store.deletePartial(sessionId, file.fileId);
      e.downloadedBytes = 0;
    }

    let autoResumeUsed = false;

    while ((totalBytes === 0 && !zeroByteDone) || offset < totalBytes) {
      if (e.runToken !== token || e.pausedByUser || e.canceledByUser || this.env.isSessionEnded(sessionId)) {
        return;
      }

      let chunkRes: SharedFileChunkResult;
      try {
        chunkRes = await this.env.getChunk(sessionId, file.fileId, BigInt(offset));
      } catch (err) {
        if (e.runToken !== token || e.pausedByUser || e.canceledByUser) return;
        if (isLinkDrop(err) && !autoResumeUsed) {
          autoResumeUsed = true;
          const recovered = await this.waitForReconnect(e, token);
          if (!recovered) {
            if (e.runToken === token && !e.pausedByUser && !e.canceledByUser) {
              e.status = "failed";
              e.failKind = "disconnected";
              e.failReason = undefined;
              e.waitingReconnect = false;
              this.emit();
            }
            return;
          }
          continue;
        }
        const drop = isLinkDrop(err);
        e.status = "failed";
        e.failKind = drop ? "disconnected" : "other";
        e.failReason = drop ? undefined : rawFailureDetail(err);
        e.waitingReconnect = false;
        this.emit();
        return;
      }

      if (e.runToken !== token || e.pausedByUser || e.canceledByUser) return;

      if (
        chunkRes.data.length !== chunkBytes ||
        chunkRes.totalBytes === undefined ||
        chunkRes.totalBytes !== file.sizeBytes
      ) {
        await this.failChecksum(e);
        return;
      }

      const validLen = totalBytes === 0 ? 0 : Math.min(chunkBytes, totalBytes - offset);
      const validSlice = chunkRes.data.subarray(0, validLen);
      const nextOffset = offset + validLen;

      try {
        await this.store.writePartialChunk(sessionId, file.fileId, offset, validSlice, {
          nextOffset,
          sizeBytes: file.sizeBytes.toString(),
          chunkBytes,
          sha256Hex: shaHex,
        });
      } catch (err) {
        if (e.runToken !== token) return;
        const full = hasQuotaExceeded(err);
        e.status = "failed";
        e.failKind = full ? "storage" : "other";
        e.failReason = full ? undefined : rawFailureDetail(err);
        e.waitingReconnect = false;
        this.emit();
        return;
      }

      if (e.runToken !== token) return;
      if (e.canceledByUser) {
        await this.store.deletePartial(sessionId, file.fileId);
        return;
      }

      offset = nextOffset;
      zeroByteDone = true;
      e.downloadedBytes = offset;
      e.waitingReconnect = false;

      const elapsedMs = this.now() - (e.startedAtMs ?? this.now());
      const gained = offset - attemptBaselineBytes;
      if (elapsedMs >= ETA_DELAY_MS && gained > 0 && offset < totalBytes) {
        const bytesPerSec = (gained * 1000) / elapsedMs;
        e.etaSeconds = Math.max(1, (totalBytes - offset) / bytesPerSec);
      } else {
        e.etaSeconds = undefined;
      }
      this.emit();

      if (e.pausedByUser) {
        e.status = "paused";
        e.etaSeconds = undefined;
        this.emit();
        return;
      }
    }

    // All chunks received: verify SHA-256 over the exact unpadded byte stream.
    const partialBlob = await this.store.readPartialBlob(sessionId, file.fileId);
    if (!partialBlob) {
      await this.failChecksum(e);
      return;
    }

    if (file.sha256.length === 32) {
      const actualDigest = await sha256(partialBlob);
      if (!equalBytes(actualDigest, file.sha256)) {
        await this.failChecksum(e);
        return;
      }
    }

    if (e.runToken !== token || e.canceledByUser) return;

    if (totalBytes > LOCAL_CACHE_MAX_BYTES) {
      // > 50 MB: trigger browser download immediately and do not retain a local copy.
      await this.store.deletePartial(sessionId, file.fileId);
      e.status = "available";
      e.downloadedBytes = 0;
      e.etaSeconds = undefined;
      e.blob = undefined;
      e.waitingReconnect = false;
      e.autoOpenSessionId = undefined;
      this.emit();
      const safeName = sanitizeFileName(file.name);
      if (this.env.onSaveLargeFile) {
        this.env.onSaveLargeFile(partialBlob, safeName, file);
      } else {
        saveBlobAsDownload(partialBlob, safeName);
      }
      return;
    }

    let finalBlob: Blob;
    try {
      finalBlob = await this.store.commitCompleted(sessionId, file.fileId, totalBytes);
    } catch (err) {
      const full = hasQuotaExceeded(err);
      e.status = "failed";
      e.failKind = full ? "storage" : "other";
      e.failReason = full ? undefined : rawFailureDetail(err);
      e.waitingReconnect = false;
      this.emit();
      return;
    }

    if (e.runToken !== token || e.canceledByUser) {
      await this.store.deleteCompleted(sessionId, file.fileId);
      return;
    }

    e.status = "downloaded";
    e.downloadedBytes = totalBytes;
    e.etaSeconds = undefined;
    e.blob = finalBlob;
    e.hydrated = true;
    e.waitingReconnect = false;
    const shouldAutoOpen =
      e.autoOpenSessionId === sessionId && this.currentSessionId === sessionId;
    e.autoOpenSessionId = undefined;
    this.emit();

    if (shouldAutoOpen) {
      this.env.onAutoOpen?.(sessionId, file, finalBlob);
      this.autoOpenHandler?.(sessionId, file, finalBlob);
    }
  }

  private async failChecksum(e: InternalEntry) {
    await this.store.deletePartial(e.sessionId, e.fileId);
    e.status = "failed";
    e.downloadedBytes = 0;
    e.etaSeconds = undefined;
    e.failKind = "checksum";
    e.failReason = undefined;
    e.waitingReconnect = false;
    e.autoOpenSessionId = undefined;
    this.emit();
  }

  private waitForReconnect(e: InternalEntry, token: number): Promise<boolean> {
    e.waitingReconnect = true;
    this.emit();
    return new Promise<boolean>((resolve) => {
      if (e.runToken !== token || e.pausedByUser || e.canceledByUser || this.env.isSessionEnded(e.sessionId)) {
        resolve(false);
        return;
      }
      if (this.env.isConnected() && this.env.isMachineOnline(e.sessionId)) {
        // Already online (e.g., transient request error): allow immediate single auto-resume.
        resolve(true);
        return;
      }
      const timer = setTimeout(() => {
        if (e.reconnectResolve) {
          e.reconnectResolve = undefined;
          resolve(false);
        }
      }, AUTO_RESUME_TIMEOUT_MS);
      e.reconnectResolve = () => {
        clearTimeout(timer);
        resolve(
          e.runToken === token &&
            !e.pausedByUser &&
            !e.canceledByUser &&
            !this.env.isSessionEnded(e.sessionId),
        );
      };
    });
  }

  pause(sessionId: string, fileId: string) {
    const e = this.entries.get(this.key(sessionId, fileId));
    if (!e || e.status !== "downloading") return;
    e.pausedByUser = true;
    e.runToken++;
    e.etaSeconds = undefined;
    e.waitingReconnect = false;
    e.status = "paused";
    e.autoOpenSessionId = undefined;
    if (e.reconnectResolve) {
      const r = e.reconnectResolve;
      e.reconnectResolve = undefined;
      r();
    }
    this.emit();
  }

  async cancel(sessionId: string, fileId: string): Promise<void> {
    const e = this.entries.get(this.key(sessionId, fileId));
    if (!e) {
      await this.store.deletePartial(sessionId, fileId);
      return;
    }
    e.canceledByUser = true;
    e.runToken++;
    e.status = "available";
    e.downloadedBytes = 0;
    e.etaSeconds = undefined;
    e.failKind = undefined;
    e.failReason = undefined;
    e.waitingReconnect = false;
    e.autoOpenSessionId = undefined;
    if (e.reconnectResolve) {
      const r = e.reconnectResolve;
      e.reconnectResolve = undefined;
      r();
    }
    await this.store.deletePartial(sessionId, fileId);
    this.emit();
  }

  async deleteLocal(sessionId: string, fileId: string): Promise<void> {
    const e = this.entries.get(this.key(sessionId, fileId));
    if (e) {
      e.runToken++;
      e.status = "available";
      e.downloadedBytes = 0;
      e.blob = undefined;
      e.failKind = undefined;
      e.failReason = undefined;
      e.etaSeconds = undefined;
      e.waitingReconnect = false;
      e.autoOpenSessionId = undefined;
      e.hydrated = true;
    }
    await this.store.deleteCompleted(sessionId, fileId);
    await this.store.deletePartial(sessionId, fileId);
    this.emit();
  }

  async dropSession(sessionId: string): Promise<void> {
    const prefix = sessionId + "\0";
    for (const [k, e] of [...this.entries.entries()]) {
      if (!k.startsWith(prefix)) continue;
      e.canceledByUser = true;
      e.runToken++;
      if (e.reconnectResolve) {
        const r = e.reconnectResolve;
        e.reconnectResolve = undefined;
        r();
      }
      this.entries.delete(k);
    }
    await this.store.dropSession(sessionId);
    this.emit();
  }

  async clearAll(): Promise<void> {
    for (const e of this.entries.values()) {
      e.canceledByUser = true;
      e.runToken++;
      if (e.reconnectResolve) {
        const r = e.reconnectResolve;
        e.reconnectResolve = undefined;
        r();
      }
    }
    this.entries.clear();
    await this.store.clearAll();
    this.emit();
  }
}
