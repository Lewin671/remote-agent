import { create } from "@bufbuild/protobuf";
import type { Event } from "./gen/remoteagent/core/v1/app_pb";
import {
  SharedFileDiskInspectedEventSchema,
  SharedFileIOErrorKind,
  SharedFileLocalEntrySchema,
  SharedFileLocalMissingEventSchema,
  SharedFileLocalScannedEventSchema,
  SharedFileVerifyAndCommitResultEventSchema,
  SharedFileWriteChunkResultEventSchema,
  type SharedFileCompletedEffect,
  type SharedFileDeleteLocalEffect,
  type SharedFileDeletePartialEffect,
  type SharedFileDeleteSessionEffect,
  type SharedFileInspectDiskEffect,
  type SharedFileScanLocalEffect,
  type SharedFileVerifyAndCommitEffect,
  type SharedFileWriteChunkEffect,
} from "./gen/remoteagent/core/v1/shared_files_pb";
import { hashBlobStreaming, PartialTruncatedError } from "./shared-file-sha256";
import { hasQuotaExceeded, sharedFileStore, type SharedFileStore } from "./shared-file-store";

/** Files strictly larger than 50 MiB are delivered directly via browser save without local caching. */
export const WEB_LOCAL_CACHE_MAX_BYTES = 52_428_800n;

/** Triggers a generic browser download save with forced application/octet-stream MIME type. */
export function saveBlobAsDownload(blob: Blob, safeFileName: string) {
  const safeName = safeFileName.trim() || "file";
  const safeBlob = blob.type === "application/octet-stream" ? blob : new Blob([blob], { type: "application/octet-stream" });
  const url = URL.createObjectURL(safeBlob);
  const a = document.createElement("a");
  a.href = url;
  a.download = safeName;
  a.style.display = "none";
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

function equalBytes(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

function classifyIOError(err: unknown): { errorKind: SharedFileIOErrorKind; errorDetail: string } {
  if (hasQuotaExceeded(err)) {
    return { errorKind: SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL, errorDetail: "" };
  }
  const detail = typeof err === "string" ? err : err instanceof Error && err.message ? err.message : String(err ?? "");
  return { errorKind: SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_OTHER, errorDetail: detail };
}

export interface SharedFileExecutorOptions {
  store?: SharedFileStore;
  onEvent: (ev: Event["event"]) => void;
  onDeliverOnly?: (blob: Blob, displayName: string) => void;
}

/** Pure platform I/O executor for Core's shared-file Effects (single global FIFO queue). */
export class SharedFileExecutor {
  readonly store: SharedFileStore;
  private readonly onEvent: (ev: Event["event"]) => void;
  private readonly onDeliverOnly: (blob: Blob, displayName: string) => void;
  private queue: Promise<void> = Promise.resolve();
  private readonly blobCache = new Map<string, Blob>();
  private readonly blobListeners = new Set<() => void>();
  private readonly pendingReads = new Map<string, Promise<Blob | undefined>>();
  private autoOpenHandler?: (eff: SharedFileCompletedEffect, blob: Blob) => void;

  constructor(opts: SharedFileExecutorOptions) {
    this.store = opts.store ?? sharedFileStore;
    this.onEvent = opts.onEvent;
    this.onDeliverOnly = opts.onDeliverOnly ?? saveBlobAsDownload;
  }

  private key(sessionId: string, fileId: string): string {
    return `${sessionId}\0${fileId}`;
  }

  private enqueue(work: () => Promise<void>): Promise<void> {
    const next = this.queue.then(work, work);
    this.queue = next.catch(() => undefined);
    return next;
  }

  whenIdle(): Promise<void> {
    return this.queue;
  }

  subscribeBlobs(listener: () => void): () => void {
    this.blobListeners.add(listener);
    return () => this.blobListeners.delete(listener);
  }

  private emitBlobs() {
    for (const listener of this.blobListeners) listener();
  }

  cachedBlob(sessionId: string, fileId: string): Blob | undefined {
    return this.blobCache.get(this.key(sessionId, fileId));
  }

  setAutoOpenHandler(fn?: (eff: SharedFileCompletedEffect, blob: Blob) => void) {
    this.autoOpenHandler = fn;
  }

  ensureBlob(sessionId: string, fileId: string, opts?: { reportMissing?: boolean }): Promise<Blob | undefined> {
    const k = this.key(sessionId, fileId);
    const cached = this.blobCache.get(k);
    if (cached) return Promise.resolve(cached);
    const inflight = this.pendingReads.get(k);
    if (inflight) return inflight;

    const job = (async () => {
      await this.queue;
      const again = this.blobCache.get(k);
      if (again) return again;
      const blob = await this.store.getCompletedBlob(sessionId, fileId).catch(() => undefined);
      if (blob) {
        this.blobCache.set(k, blob);
        this.emitBlobs();
        return blob;
      }
      if (opts?.reportMissing) {
        this.onEvent({ case: "sharedFileLocalMissing", value: create(SharedFileLocalMissingEventSchema, { sessionId, fileId }) });
      }
      return undefined;
    })().finally(() => { this.pendingReads.delete(k); });
    this.pendingReads.set(k, job);
    return job;
  }

  private emitWriteResult(opId: bigint, sessionId: string, fileId: string, ok: boolean, errorKind = SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_UNSPECIFIED, errorDetail = "") {
    this.onEvent({ case: "sharedFileWriteChunkResult", value: create(SharedFileWriteChunkResultEventSchema, { opId, sessionId, fileId, ok, errorKind, errorDetail }) });
  }

  private emitVerifyResult(opId: bigint, sessionId: string, fileId: string, ok: boolean, errorKind = SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_UNSPECIFIED, errorDetail = "") {
    this.onEvent({ case: "sharedFileVerifyAndCommitResult", value: create(SharedFileVerifyAndCommitResultEventSchema, { opId, sessionId, fileId, ok, errorKind, errorDetail }) });
  }

  handleScanLocal(eff: SharedFileScanLocalEffect) {
    void this.enqueue(async () => {
      let ev: Event["event"];
      try {
        const scanned = await this.store.scanLocal();
        ev = {
          case: "sharedFileLocalScanned",
          value: create(SharedFileLocalScannedEventSchema, {
            opId: eff.opId,
            entries: scanned.map((e) => create(SharedFileLocalEntrySchema, { ...e, hasDuration: !!e.hasDuration, durationMs: e.durationMs ?? 0n })),
          }),
        };
      } catch (err) {
        const detail = classifyIOError(err).errorDetail || (err instanceof Error && err.name ? err.name : "scan_local_failed");
        ev = {
          case: "sharedFileLocalScanned",
          value: create(SharedFileLocalScannedEventSchema, { opId: eff.opId, error: detail }),
        };
      }
      this.onEvent(ev);
    });
  }

  handleInspectDisk(eff: SharedFileInspectDiskEffect) {
    void this.enqueue(async () => {
      const res = await this.store.inspectDisk().catch(() => ({ known: false, availableBytes: 0n }));
      this.onEvent({
        case: "sharedFileDiskInspected",
        value: create(SharedFileDiskInspectedEventSchema, {
          opId: eff.opId,
          sessionId: eff.sessionId,
          fileId: eff.fileId,
          known: res.known,
          availableBytes: res.availableBytes,
        }),
      });
    });
  }

  handleWriteChunk(eff: SharedFileWriteChunkEffect) {
    void this.enqueue(async () => {
      let ok = false;
      let errorKind = SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_UNSPECIFIED;
      let errorDetail = "";
      try {
        await this.store.writeChunk(eff);
        ok = true;
      } catch (err) {
        const io = classifyIOError(err);
        errorKind = io.errorKind;
        errorDetail = io.errorDetail;
      }
      this.emitWriteResult(eff.opId, eff.sessionId, eff.fileId, ok, errorKind, errorDetail);
    });
  }

  handleVerifyAndCommit(eff: SharedFileVerifyAndCommitEffect) {
    void this.enqueue(async () => {
      let ok = false;
      let errorKind = SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_UNSPECIFIED;
      let errorDetail = "";
      const markChecksumFail = async () => {
        await this.store.deletePartial(eff.sessionId, eff.fileId).catch(() => undefined);
        ok = false;
        errorKind = SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_CHECKSUM_MISMATCH;
        errorDetail = "";
      };

      const totalSize = Number(eff.totalSizeBytes);
      if (eff.expectedSha256.length !== 32 || !Number.isFinite(totalSize) || totalSize < 0) {
        await markChecksumFail();
      } else {
        try {
          let partial: Blob | undefined;
          let digest: Uint8Array;
          if (totalSize === 0) {
            digest = await hashBlobStreaming(new Blob([]), 0);
          } else {
            partial = await this.store.readPartialBlob(eff.sessionId, eff.fileId);
            if (!partial || partial.size < totalSize) {
              await markChecksumFail();
              digest = new Uint8Array();
            } else {
              digest = await hashBlobStreaming(partial, totalSize);
            }
          }
          if (errorKind === SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_UNSPECIFIED) {
            if (!equalBytes(digest, eff.expectedSha256)) {
              await markChecksumFail();
            } else if (eff.deliverOnly) {
              const deliverBlob =
                totalSize === 0 || !partial
                  ? new Blob([], { type: "application/octet-stream" })
                  : partial.size === totalSize
                    ? partial
                    : partial.slice(0, totalSize, "application/octet-stream");
              this.onDeliverOnly(deliverBlob, eff.displayName);
              await this.store.deletePartial(eff.sessionId, eff.fileId).catch(() => undefined);
              ok = true;
            } else {
              const committed = await this.store.commitPartial(eff.sessionId, eff.fileId, totalSize);
              this.blobCache.set(this.key(eff.sessionId, eff.fileId), committed);
              this.emitBlobs();
              ok = true;
            }
          }
        } catch (err) {
          if (err instanceof PartialTruncatedError) {
            await markChecksumFail();
          } else {
            const io = classifyIOError(err);
            ok = false;
            errorKind = io.errorKind;
            errorDetail = io.errorDetail;
          }
        }
      }
      this.emitVerifyResult(eff.opId, eff.sessionId, eff.fileId, ok, errorKind, errorDetail);
    });
  }

  handleDeletePartial(eff: SharedFileDeletePartialEffect) {
    void this.enqueue(async () => { await this.store.deletePartial(eff.sessionId, eff.fileId).catch(() => undefined); });
  }

  handleDeleteLocal(eff: SharedFileDeleteLocalEffect) {
    void this.enqueue(async () => {
      await this.store.deleteLocal(eff.sessionId, eff.fileId).catch(() => undefined);
      if (this.blobCache.delete(this.key(eff.sessionId, eff.fileId))) this.emitBlobs();
    });
  }

  handleDeleteSession(eff: SharedFileDeleteSessionEffect) {
    void this.enqueue(async () => {
      await this.store.dropSession(eff.sessionId).catch(() => undefined);
      const prefix = eff.sessionId + "\0";
      let removed = false;
      for (const k of [...this.blobCache.keys()]) {
        if (k.startsWith(prefix)) { this.blobCache.delete(k); removed = true; }
      }
      if (removed) this.emitBlobs();
    });
  }

  handleClearAll() {
    void this.enqueue(async () => {
      await this.store.clearAll().catch(() => undefined);
      if (this.blobCache.size > 0) { this.blobCache.clear(); this.emitBlobs(); }
    });
  }

  handleCompleted(eff: SharedFileCompletedEffect) {
    if (eff.deliverOnly || !eff.autoOpen || !this.autoOpenHandler) return;
    const cached = this.blobCache.get(this.key(eff.sessionId, eff.fileId));
    if (cached) { this.autoOpenHandler(eff, cached); return; }
    void this.ensureBlob(eff.sessionId, eff.fileId, { reportMissing: true }).then((blob) => {
      if (blob) this.autoOpenHandler?.(eff, blob);
    });
  }
}
