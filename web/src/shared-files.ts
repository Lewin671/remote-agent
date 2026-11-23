import { create } from "@bufbuild/protobuf";
import {
  SharedFileFailureKind,
  SharedFileLocalStateUpdateSchema,
  SharedFileTransferStatus,
  type SharedFileLocalStateUpdate,
} from "./gen/remoteagent/core/v1/session_view_pb";
import type { Remote } from "./remote";
import { hasQuotaExceeded, OpfsSharedFileStore, sharedFileStore, type SharedFileStore } from "./shared-file-store";

export const DEFAULT_SHARED_FILE_CHUNK_BYTES = 786_432;
/** Files strictly larger than 50 MB are saved to disk on completion without local OPFS/memory caching. */
export const LOCAL_CACHE_MAX_BYTES = 50 * 1024 * 1024;
export const AUTO_RESUME_TIMEOUT_MS = 30_000;
export const ETA_DELAY_MS = 3_000;
export const PROGRESS_REPORT_INTERVAL_MS = 250;

export type SharedFileFailKind = "checksum" | "disconnected" | "storage" | "other";

export interface SharedFileDescriptor {
  fileId: string;
  sizeBytes: bigint;
  sha256: Uint8Array;
  displayName?: string;
  name?: string;
}

export interface SharedFileTransferState {
  status: "available" | "downloading" | "paused" | "downloaded" | "failed" | "cleaned_up";
  downloadedBytes: number;
  startedAtMs?: number;
  etaSeconds?: number;
  durationMs?: number;
  failKind?: SharedFileFailKind;
  failReason?: string;
  blob?: Blob;
  waitingReconnect?: boolean;
  /** Stored only while actively hydrating from OPFS/store. */
  hydrated?: boolean;
}

export interface SharedFileChunkResult {
  data: Uint8Array;
  totalBytes?: bigint;
}

export type SharedFileStateReporter = (
  updates: SharedFileLocalStateUpdate[],
  opts?: { replaceAll?: boolean; clearSessionId?: string },
) => void;

export interface SharedFileEnvironment {
  getChunk(sessionId: string, fileId: string, offset: bigint): Promise<SharedFileChunkResult>;
  isConnected(sessionId: string): boolean;
  isMachineOnline(sessionId: string): boolean;
  isSessionEnded(sessionId: string): boolean;
  sharedFileChunkBytes(sessionId: string): number;
  store?: SharedFileStore;
  now?: () => number;
  onStateReport?: SharedFileStateReporter;
  onSaveLargeFile?: (blob: Blob, safeName: string, file: SharedFileDescriptor) => void;
  onAutoOpen?: (sessionId: string, file: SharedFileDescriptor, blob: Blob) => void;
}

/** Triggers a generic browser download save with forced application/octet-stream MIME type. */
export function saveBlobAsDownload(blob: Blob, safeFileName: string) {
  const safeName = safeFileName.trim() || "file";
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

function errorCodeOf(err: unknown): string | undefined {
  if (!err || typeof err !== "object") return undefined;
  const code = (err as { code?: unknown }).code;
  return typeof code === "string" ? code : undefined;
}

function isLinkDrop(err: unknown): boolean {
  const code = errorCodeOf(err);
  return code === "unavailable" || code === "unknown" || code === "unknown_outcome";
}

function rawFailureDetail(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  return typeof err === "string" ? err : "";
}

function toProtoUpdate(e: InternalEntry): SharedFileLocalStateUpdate {
  let status = SharedFileTransferStatus.UNSPECIFIED;
  switch (e.status) {
    case "downloading":
      status = SharedFileTransferStatus.DOWNLOADING;
      break;
    case "paused":
      status = SharedFileTransferStatus.PAUSED;
      break;
    case "downloaded":
      status = SharedFileTransferStatus.DOWNLOADED;
      break;
    case "failed":
      status = SharedFileTransferStatus.FAILED;
      break;
    case "cleaned_up":
      status = SharedFileTransferStatus.CLEANED_UP;
      break;
  }
  let failureKind = SharedFileFailureKind.UNSPECIFIED;
  if (e.status === "failed") {
    switch (e.failKind) {
      case "checksum":
        failureKind = SharedFileFailureKind.CHECKSUM_MISMATCH;
        break;
      case "disconnected":
        failureKind = SharedFileFailureKind.DISCONNECTED;
        break;
      case "storage":
        failureKind = SharedFileFailureKind.BROWSER_STORAGE_FULL;
        break;
      default:
        failureKind = SharedFileFailureKind.OTHER;
        break;
    }
  }
  return create(SharedFileLocalStateUpdateSchema, {
    sessionId: e.sessionId,
    fileId: e.fileId,
    status,
    downloadedBytes: BigInt(Math.max(0, Math.floor(e.downloadedBytes))),
    hasEta: e.etaSeconds !== undefined,
    etaSeconds: BigInt(e.etaSeconds !== undefined ? Math.max(1, Math.round(e.etaSeconds)) : 0),
    waitingReconnect: !!e.waitingReconnect,
    hasDuration: e.durationMs !== undefined,
    durationMs: BigInt(e.durationMs !== undefined ? Math.max(0, Math.round(e.durationMs)) : 0),
    failureKind,
    failureDetail: e.status === "failed" && e.failKind === "other" ? (e.failReason ?? "") : "",
    metadataOnly: false,
  });
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
  lastReportAtMs: number;
  lastReportPct: number;
  lastReportedHasEta: boolean;
}

/** Coordinates chunked shared-file downloads, resume/cancel, OPFS/memory persistence,
 * auto-resume on transient disconnects, and reporting local transfer state to Core. */
export class SharedFileManager {
  private readonly env: SharedFileEnvironment;
  private readonly store: SharedFileStore;
  private readonly entries = new Map<string, InternalEntry>();
  private readonly listeners = new Set<() => void>();
  private report?: SharedFileStateReporter;
  private currentSessionId: string | undefined;
  private autoOpenHandler?: (sessionId: string, file: SharedFileDescriptor, blob: Blob) => void;
  private nextToken = 1;

  constructor(env: SharedFileEnvironment) {
    this.env = env;
    this.store = env.store ?? new OpfsSharedFileStore();
    this.report = env.onStateReport;
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

  /** Attaches or detaches the Core reporter. On attach, pushes a full replace_all snapshot. */
  setReporter(reporter?: SharedFileStateReporter) {
    this.report = reporter;
    if (reporter) this.reportAll();
  }

  /** Reports all known local shared-file entries with replace_all = true. */
  reportAll() {
    if (!this.report) return;
    const updates: SharedFileLocalStateUpdate[] = [];
    for (const e of this.entries.values()) {
      if (e.status !== "available" || e.durationMs !== undefined) {
        updates.push(toProtoUpdate(e));
      }
    }
    this.report(updates, { replaceAll: true });
  }

  private reportEntry(e: InternalEntry, opts?: { throttle?: boolean; totalBytes?: number }) {
    if (!this.report) return;
    const now = this.now();
    if (opts?.throttle && e.status === "downloading") {
      const total = opts.totalBytes ?? 0;
      const pct = total > 0 ? Math.floor((e.downloadedBytes * 100) / total) : -1;
      const etaJustAppeared = e.etaSeconds !== undefined && !e.lastReportedHasEta;
      if (
        !etaJustAppeared &&
        now - e.lastReportAtMs < PROGRESS_REPORT_INTERVAL_MS &&
        pct === e.lastReportPct
      ) {
        return;
      }
      e.lastReportAtMs = now;
      e.lastReportPct = pct;
      e.lastReportedHasEta = e.etaSeconds !== undefined;
    } else {
      e.lastReportAtMs = now;
      e.lastReportPct = -1;
      e.lastReportedHasEta = e.etaSeconds !== undefined;
    }
    this.report([toProtoUpdate(e)]);
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
        lastReportAtMs: -Infinity,
        lastReportPct: -1,
        lastReportedHasEta: false,
      };
      this.entries.set(k, e);
    }
    return e;
  }

  /** Returns the locally cached Blob when downloaded in this browser. */
  blobOf(sessionId: string, fileId: string): Blob | undefined {
    const e = this.entries.get(this.key(sessionId, fileId));
    return e?.status === "downloaded" ? e.blob : undefined;
  }

  /** Returns the local transfer record for executor tests and internal guards. */
  transferOf(sessionId: string, fileId: string): SharedFileTransferState | undefined {
    return this.entries.get(this.key(sessionId, fileId));
  }

  /** Reports probed local video/audio duration (in ms) without altering transfer status. */
  reportDuration(sessionId: string, fileId: string, durationMs: number) {
    if (!Number.isFinite(durationMs) || durationMs <= 0) return;
    const ms = Math.round(durationMs);
    const e = this.entryFor(sessionId, fileId);
    if (e.durationMs === ms) return;
    e.durationMs = ms;
    this.report?.([
      create(SharedFileLocalStateUpdateSchema, {
        sessionId,
        fileId,
        metadataOnly: true,
        hasDuration: true,
        durationMs: BigInt(ms),
      }),
    ]);
  }

  /** Registers a UI callback to open completed files when the user stayed in the session. */
  setAutoOpenHandler(fn?: (sessionId: string, file: SharedFileDescriptor, blob: Blob) => void) {
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
        this.env.isConnected(e.sessionId) &&
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
        "status" | "downloadedBytes" | "etaSeconds" | "durationMs" | "failKind" | "failReason" | "blob" | "waitingReconnect"
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
    if (patch.durationMs !== undefined) e.durationMs = patch.durationMs;
    e.failKind = patch.failKind;
    e.failReason = patch.failReason;
    e.blob = patch.blob;
    e.waitingReconnect = patch.waitingReconnect ?? false;
    e.hydrated = true;
    this.reportEntry(e);
    this.emit();
  }

  /** Hydrates saved OPFS/store blob and partial download metadata if present. */
  hydrate(sessionId: string, file: SharedFileDescriptor): Promise<void> {
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
        this.reportEntry(e);
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
          this.reportEntry(e);
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

  private chunkSizeFor(sessionId: string): number {
    const v = this.env.sharedFileChunkBytes(sessionId);
    return v > 0 ? v : DEFAULT_SHARED_FILE_CHUNK_BYTES;
  }

  async startDownload(
    sessionId: string,
    file: SharedFileDescriptor,
    opts?: { autoOpenIfStaying?: boolean },
  ): Promise<void> {
    const existing = this.entries.get(this.key(sessionId, file.fileId));
    const online = this.env.isConnected(sessionId) && this.env.isMachineOnline(sessionId);
    if (
      existing?.status === "downloading" ||
      existing?.status === "cleaned_up" ||
      (existing?.status === "downloaded" && existing.blob) ||
      this.env.isSessionEnded(sessionId) ||
      (!online && (!existing || existing.status === "available"))
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
    this.reportEntry(e);
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
      this.reportEntry(e);
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
        if (errorCodeOf(err) === "not_found") {
          await this.store.deletePartial(sessionId, file.fileId);
          e.status = "cleaned_up";
          e.downloadedBytes = 0;
          e.etaSeconds = undefined;
          e.failKind = undefined;
          e.failReason = undefined;
          e.waitingReconnect = false;
          e.autoOpenSessionId = undefined;
          this.reportEntry(e);
          this.emit();
          return;
        }
        if (isLinkDrop(err) && !autoResumeUsed) {
          autoResumeUsed = true;
          const recovered = await this.waitForReconnect(e, token);
          if (!recovered) {
            if (e.runToken === token && !e.pausedByUser && !e.canceledByUser) {
              e.status = "failed";
              e.failKind = "disconnected";
              e.failReason = undefined;
              e.waitingReconnect = false;
              this.reportEntry(e);
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
        this.reportEntry(e);
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
        this.reportEntry(e);
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
      this.reportEntry(e, { throttle: true, totalBytes });
      this.emit();

      if (e.pausedByUser) {
        e.status = "paused";
        e.etaSeconds = undefined;
        this.reportEntry(e);
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
      this.reportEntry(e);
      this.emit();
      const safeName = file.displayName || file.name || "file";
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
      this.reportEntry(e);
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
    this.reportEntry(e);
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
    this.reportEntry(e);
    this.emit();
  }

  private waitForReconnect(e: InternalEntry, token: number): Promise<boolean> {
    e.waitingReconnect = true;
    this.reportEntry(e);
    this.emit();
    return new Promise<boolean>((resolve) => {
      if (e.runToken !== token || e.pausedByUser || e.canceledByUser || this.env.isSessionEnded(e.sessionId)) {
        resolve(false);
        return;
      }
      if (this.env.isConnected(e.sessionId) && this.env.isMachineOnline(e.sessionId)) {
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
    this.reportEntry(e);
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
    this.reportEntry(e);
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
      e.durationMs = undefined;
      e.waitingReconnect = false;
      e.autoOpenSessionId = undefined;
      e.hydrated = true;
    }
    await this.store.deleteCompleted(sessionId, fileId);
    await this.store.deletePartial(sessionId, fileId);
    if (e) this.reportEntry(e);
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
    this.report?.([], { clearSessionId: sessionId });
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
    this.report?.([], { replaceAll: true });
    this.emit();
  }
}

const sharedFileManagers = new WeakMap<Remote, SharedFileManager>();
const sharedFileReporters = new WeakMap<Remote, SharedFileStateReporter>();

/** Binds a CoreHost reporter to a Remote's SharedFileManager, syncing existing state immediately. */
export function bindSharedFiles(remote: Remote, reporter: SharedFileStateReporter): () => void {
  sharedFileReporters.set(remote, reporter);
  const existing = sharedFileManagers.get(remote);
  if (existing) existing.setReporter(reporter);
  return () => {
    if (sharedFileReporters.get(remote) === reporter) {
      sharedFileReporters.delete(remote);
      sharedFileManagers.get(remote)?.setReporter(undefined);
    }
  };
}

/** Lazily instantiates and caches the SharedFileManager for a Remote instance. */
export function sharedFilesOf(remote: Remote): SharedFileManager {
  let mgr = sharedFileManagers.get(remote);
  if (!mgr) {
    mgr = new SharedFileManager({
      ...remote.sharedFilesHost(),
      store: sharedFileStore,
      onStateReport: sharedFileReporters.get(remote),
    });
    remote.subscribe(() => mgr!.onConnectivityChange());
    sharedFileManagers.set(remote, mgr);
  }
  return mgr;
}
