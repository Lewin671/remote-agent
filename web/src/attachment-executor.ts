import { create } from "@bufbuild/protobuf";
import type { AttachmentStore } from "./attachment-store";
import type { Event } from "./gen/remoteagent/core/v1/app_pb";
import {
  AttachmentHashFileResultEventSchema,
  AttachmentIOErrorKind,
  AttachmentItemKind,
  AttachmentPrepareImageResultEventSchema,
  AttachmentReadChunkResultEventSchema,
  DraftStorageEntrySchema,
  DraftStorageListedEventSchema,
  DraftStorageReadResultEventSchema,
  DraftStorageWriteResultEventSchema,
  type AttachmentHashFileEffect,
  type AttachmentPrepareImageEffect,
  type AttachmentReadChunkEffect,
  type AttachmentReleaseLocalEffect,
  type DraftStorageDeleteEffect,
  type DraftStorageListEffect,
  type DraftStorageReadEffect,
  type DraftStorageWriteEffect,
} from "./gen/remoteagent/core/v1/attachments_pb";
import { ImageFormat } from "./gen/remoteagent/v1/event_pb";
import { browserBackend, compressImage, ImageError, prepareOriginal } from "./images";
import { mimeOf } from "./imageview";
import { PartialTruncatedError, StreamingSha256 } from "./shared-file-sha256";
import { hasQuotaExceeded } from "./shared-file-store";

export interface PreparedImageOutput {
  bytes: Uint8Array;
  format: "jpeg" | "png" | "webp" | "gif";
  width: number;
  height: number;
}

export type ImagePrepareFn = (
  blob: Blob,
  opts: {
    original: boolean;
    maxOriginalBytes: number;
    maxDimensionPx: number;
    maxCompressedBytes: number;
  },
) => Promise<PreparedImageOutput>;

export interface AttachmentExecutorOptions {
  store: AttachmentStore;
  onEvent: (ev: Event["event"]) => void;
  /** Optional async gate awaited before storage operations (e.g. security bootstrap). */
  ensureStorageReady?: () => Promise<void>;
  /** Optional image prepare override (used by tests / demo). */
  prepareImage?: ImagePrepareFn;
  /** Optional video duration probe override (used by tests). */
  probeVideoDurationMs?: (blob: Blob) => Promise<number | undefined>;
}

const THUMB_MAX_EDGE = 320;
const THUMB_SKIP_BYTES = 64 * 1024;
const VIDEO_PROBE_TIMEOUT_MS = 3_000;

function formatToProto(fmt: PreparedImageOutput["format"]): ImageFormat {
  switch (fmt) {
    case "jpeg": return ImageFormat.JPEG;
    case "png": return ImageFormat.PNG;
    case "webp": return ImageFormat.WEBP;
    case "gif": return ImageFormat.GIF;
  }
}

function formatToExt(fmt: PreparedImageOutput["format"]): string {
  return fmt === "jpeg" ? "jpg" : fmt;
}

async function sha256Bytes(bytes: Uint8Array): Promise<Uint8Array<ArrayBuffer>> {
  if (typeof crypto !== "undefined" && crypto.subtle?.digest) {
    return new Uint8Array(await crypto.subtle.digest("SHA-256", bytes as Uint8Array<ArrayBuffer>));
  }
  const hasher = new StreamingSha256();
  hasher.update(bytes);
  return hasher.digest() as Uint8Array<ArrayBuffer>;
}

async function defaultPrepareImage(
  blob: Blob,
  opts: {
    original: boolean;
    maxOriginalBytes: number;
    maxDimensionPx: number;
    maxCompressedBytes: number;
  },
): Promise<PreparedImageOutput> {
  if (opts.original) {
    const p = await prepareOriginal(blob, opts.maxOriginalBytes);
    return { bytes: p.bytes, format: p.format, width: p.width, height: p.height };
  }
  const p = await compressImage(blob, browserBackend, {
    maxDimensionPx: opts.maxDimensionPx,
    maxCompressedBytes: opts.maxCompressedBytes,
  });
  return { bytes: p.bytes, format: p.format, width: p.width, height: p.height };
}

async function makeThumbnailBlob(prepared: PreparedImageOutput): Promise<{ blob: Blob; ext: string }> {
  const mime = mimeOf(formatToProto(prepared.format)) || "image/jpeg";
  const fullBlob = new Blob([prepared.bytes as Uint8Array<ArrayBuffer>], { type: mime });
  if (
    prepared.bytes.byteLength <= THUMB_SKIP_BYTES ||
    typeof createImageBitmap !== "function" ||
    (typeof OffscreenCanvas === "undefined" && typeof document === "undefined")
  ) {
    return { blob: fullBlob, ext: formatToExt(prepared.format) };
  }
  let bmp: ImageBitmap | undefined;
  try {
    bmp = await createImageBitmap(fullBlob);
    const maxDim = Math.max(bmp.width, bmp.height);
    if (maxDim <= THUMB_MAX_EDGE) {
      return { blob: fullBlob, ext: formatToExt(prepared.format) };
    }
    const scale = THUMB_MAX_EDGE / maxDim;
    const w = Math.max(1, Math.round(bmp.width * scale));
    const h = Math.max(1, Math.round(bmp.height * scale));
    if (typeof OffscreenCanvas !== "undefined") {
      const c = new OffscreenCanvas(w, h);
      const ctx = c.getContext("2d");
      if (ctx) {
        ctx.drawImage(bmp, 0, 0, w, h);
        const out = await c.convertToBlob({ type: "image/jpeg", quality: 0.78 });
        if (out && out.size > 0 && out.size < prepared.bytes.byteLength) {
          return { blob: out, ext: "jpg" };
        }
      }
    } else {
      const c = document.createElement("canvas");
      c.width = w;
      c.height = h;
      const ctx = c.getContext("2d");
      if (ctx) {
        ctx.drawImage(bmp, 0, 0, w, h);
        const out = await new Promise<Blob | null>((resolve) => c.toBlob(resolve, "image/jpeg", 0.78));
        if (out && out.size > 0 && out.size < prepared.bytes.byteLength) {
          return { blob: out, ext: "jpg" };
        }
      }
    }
  } catch {
    // Fall back to full prepared bytes as thumbnail.
  } finally {
    bmp?.close();
  }
  return { blob: fullBlob, ext: formatToExt(prepared.format) };
}

async function defaultProbeVideoDurationMs(blob: Blob): Promise<number | undefined> {
  if (typeof document === "undefined" || typeof URL === "undefined" || typeof URL.createObjectURL !== "function") {
    return undefined;
  }
  const url = URL.createObjectURL(blob);
  const video = document.createElement("video");
  video.preload = "metadata";
  video.muted = true;
  return new Promise<number | undefined>((resolve) => {
    let done = false;
    const finish = (ms?: number) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      video.onloadedmetadata = null;
      video.onerror = null;
      video.removeAttribute("src");
      try { video.load(); } catch { /* ignore */ }
      URL.revokeObjectURL(url);
      resolve(ms);
    };
    const timer = setTimeout(() => finish(undefined), VIDEO_PROBE_TIMEOUT_MS);
    video.onloadedmetadata = () => {
      const d = video.duration;
      finish(Number.isFinite(d) && d > 0 ? Math.round(d * 1000) : undefined);
    };
    video.onerror = () => finish(undefined);
    video.src = url;
  });
}

function errorDetailOf(err: unknown): string {
  if (typeof err === "string") return err;
  if (err instanceof Error && err.message) return err.message;
  return String(err ?? "");
}

function classifyStorageIOError(err: unknown): { errorKind: AttachmentIOErrorKind; errorDetail: string } {
  if (hasQuotaExceeded(err)) {
    return { errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL, errorDetail: "" };
  }
  return { errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER, errorDetail: errorDetailOf(err) };
}

function classifyPrepareError(err: unknown): {
  errorKind: AttachmentIOErrorKind;
  errorCode: string;
  errorDetail: string;
  isReadingSource: boolean;
} {
  if (hasQuotaExceeded(err)) {
    return { errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL, errorCode: "", errorDetail: "", isReadingSource: false };
  }
  if (err instanceof ImageError) {
    const errorKind =
      err.problem === "original_too_large"
        ? AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_ORIGINAL_TOO_LARGE
        : err.problem === "dimensions"
          ? AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIMENSIONS
          : AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DECODE;
    return { errorKind, errorCode: err.code, errorDetail: err.detail, isReadingSource: false };
  }
  return {
    errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER,
    errorCode: "",
    errorDetail: errorDetailOf(err),
    isReadingSource: false,
  };
}

/** Pure platform I/O executor for Core's 9 attachment/draft Effects (Contract §2).
 * All storage-class Effects execute strictly sequentially on a single global FIFO queue. */
export class AttachmentExecutor {
  readonly store: AttachmentStore;
  private readonly onEvent: (ev: Event["event"]) => void;
  private readonly ensureStorageReady?: () => Promise<void>;
  private readonly prepareImageFn: ImagePrepareFn;
  private readonly probeVideoDurationFn: (blob: Blob) => Promise<number | undefined>;
  private storageQueue: Promise<void> = Promise.resolve();
  private readonly activeIO = new Set<Promise<void>>();
  private readonly blobCache = new Map<string, Blob>();
  private readonly blobLoads = new Map<string, Promise<Blob | undefined>>();
  private readonly blobListeners = new Set<() => void>();

  constructor(opts: AttachmentExecutorOptions) {
    this.store = opts.store;
    this.onEvent = opts.onEvent;
    this.ensureStorageReady = opts.ensureStorageReady;
    this.prepareImageFn = opts.prepareImage ?? defaultPrepareImage;
    this.probeVideoDurationFn = opts.probeVideoDurationMs ?? defaultProbeVideoDurationMs;
  }

  private enqueueStorage(work: () => Promise<void>): Promise<void> {
    const run = async () => {
      if (this.ensureStorageReady) await this.ensureStorageReady().catch(() => undefined);
      await work();
    };
    const next = this.storageQueue.then(run, run);
    this.storageQueue = next.catch(() => undefined);
    return next;
  }

  private trackIO(work: Promise<void>) {
    this.activeIO.add(work);
    void work.finally(() => {
      this.activeIO.delete(work);
    });
  }

  /** Waits until all queued storage ops and in-flight I/O operations finish. */
  async whenIdle(): Promise<void> {
    for (;;) {
      const q = this.storageQueue;
      const io = [...this.activeIO];
      await q;
      if (io.length > 0) await Promise.all(io);
      if (this.storageQueue === q && this.activeIO.size === 0) break;
    }
  }

  subscribeBlobs(listener: () => void): () => void {
    this.blobListeners.add(listener);
    return () => this.blobListeners.delete(listener);
  }

  private emitBlobs() {
    for (const fn of this.blobListeners) fn();
  }

  cachedBlob(ref: string): Blob | undefined {
    if (!ref) return undefined;
    return this.blobCache.get(ref) ?? this.store.peekMemoryBlob(ref);
  }

  ensureBlob(ref: string): Promise<Blob | undefined> {
    if (!ref) return Promise.resolve(undefined);
    const cached = this.cachedBlob(ref);
    if (cached) return Promise.resolve(cached);
    const inflight = this.blobLoads.get(ref);
    if (inflight) return inflight;
    const job = this.store
      .getLocalBlob(ref)
      .then((blob) => {
        if (blob) {
          this.blobCache.set(ref, blob);
          this.emitBlobs();
        }
        return blob;
      })
      .catch(() => undefined)
      .finally(() => {
        this.blobLoads.delete(ref);
      });
    this.blobLoads.set(ref, job);
    return job;
  }

  /** Synchronously stages a user-picked/dropped/pasted Blob and returns its opaque source_ref. */
  stagePickedBlob(blob: Blob): string {
    const ref = this.store.stageBlob(blob, { mimeType: blob.type });
    this.blobCache.set(ref, blob);
    return ref;
  }

  // --- Global FIFO storage-class Effects (Contract §0.3 & §2.4-2.6) ---

  handleDraftStorageList(eff: DraftStorageListEffect) {
    void this.enqueueStorage(async () => {
      try {
        const { entries, localRefs } = await this.store.list();
        this.onEvent({
          case: "draftStorageListed",
          value: create(DraftStorageListedEventSchema, {
            opId: eff.opId,
            entries: entries.map((e) =>
              create(DraftStorageEntrySchema, {
                bucket: e.bucket,
                key: e.key,
                data: e.data as Uint8Array<ArrayBuffer>,
                corrupt: e.corrupt,
              }),
            ),
            localRefs,
          }),
        });
      } catch (err) {
        const io = classifyStorageIOError(err);
        this.onEvent({
          case: "draftStorageListed",
          value: create(DraftStorageListedEventSchema, {
            opId: eff.opId,
            errorKind: io.errorKind,
            errorDetail: io.errorDetail,
          }),
        });
      }
    });
  }

  handleDraftStorageRead(eff: DraftStorageReadEffect) {
    void this.enqueueStorage(async () => {
      try {
        const res = await this.store.read(eff.bucket, eff.key);
        if (res.corrupt) {
          this.onEvent({
            case: "draftStorageReadResult",
            value: create(DraftStorageReadResultEventSchema, {
              opId: eff.opId,
              bucket: eff.bucket,
              key: eff.key,
              found: false,
              errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE,
            }),
          });
          return;
        }
        this.onEvent({
          case: "draftStorageReadResult",
          value: create(DraftStorageReadResultEventSchema, {
            opId: eff.opId,
            bucket: eff.bucket,
            key: eff.key,
            found: res.found,
            data: (res.data ?? new Uint8Array()) as Uint8Array<ArrayBuffer>,
          }),
        });
      } catch (err) {
        const io = classifyStorageIOError(err);
        this.onEvent({
          case: "draftStorageReadResult",
          value: create(DraftStorageReadResultEventSchema, {
            opId: eff.opId,
            bucket: eff.bucket,
            key: eff.key,
            found: false,
            errorKind: io.errorKind,
            errorDetail: io.errorDetail,
          }),
        });
      }
    });
  }

  handleDraftStorageWrite(eff: DraftStorageWriteEffect) {
    void this.enqueueStorage(async () => {
      try {
        await this.store.write(eff.bucket, eff.key, eff.data, eff.referencedRefs);
        this.onEvent({
          case: "draftStorageWriteResult",
          value: create(DraftStorageWriteResultEventSchema, {
            opId: eff.opId,
            bucket: eff.bucket,
            key: eff.key,
            ok: true,
          }),
        });
      } catch (err) {
        const io = classifyStorageIOError(err);
        this.onEvent({
          case: "draftStorageWriteResult",
          value: create(DraftStorageWriteResultEventSchema, {
            opId: eff.opId,
            bucket: eff.bucket,
            key: eff.key,
            ok: false,
            errorKind: io.errorKind,
            errorDetail: io.errorDetail,
          }),
        });
      }
    });
  }

  handleDraftStorageDelete(eff: DraftStorageDeleteEffect) {
    void this.enqueueStorage(async () => {
      await this.store.delete(eff.bucket, eff.key).catch(() => undefined);
    });
  }

  handleDraftStorageClearAll() {
    void this.enqueueStorage(async () => {
      await this.store.clearAll().catch(() => undefined);
      if (this.blobCache.size > 0) {
        this.blobCache.clear();
        this.emitBlobs();
      }
    });
  }

  handleReleaseLocal(eff: AttachmentReleaseLocalEffect) {
    void this.enqueueStorage(async () => {
      if (!eff.refs.length) return;
      await this.store.deleteLocalRefs(eff.refs).catch(() => undefined);
      let removed = false;
      for (const ref of eff.refs) {
        if (this.blobCache.delete(ref)) removed = true;
      }
      if (removed) this.emitBlobs();
    });
  }

  // --- Prepare / Hash / ReadChunk Effects (Contract §2.1-2.3) ---

  handlePrepareImage(eff: AttachmentPrepareImageEffect) {
    const work = (async () => {
      const base = {
        opId: eff.opId,
        scope: eff.scope,
        outboxRequestId: eff.outboxRequestId,
        itemId: eff.itemId,
      };
      let preparedRef = "";
      let thumbRef = "";
      try {
        const source = await this.store.getLocalBlob(eff.sourceRef);
        if (!source) {
          this.onEvent({
            case: "attachmentPrepareImageResult",
            value: create(AttachmentPrepareImageResultEventSchema, {
              ...base,
              ok: false,
              errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
              isReadingSource: true,
            }),
          });
          return;
        }
        const prepared = await this.prepareImageFn(source, {
          original: eff.original,
          maxOriginalBytes: Number(eff.maxOriginalBytes),
          maxDimensionPx: eff.maxDimensionPx,
          maxCompressedBytes: Number(eff.maxCompressedBytes),
        });
        const protoFmt = formatToProto(prepared.format);
        const mime = mimeOf(protoFmt) || "image/jpeg";
        const sha256 = await sha256Bytes(prepared.bytes);
        const preparedBlob = new Blob([prepared.bytes as Uint8Array<ArrayBuffer>], { type: mime });
        preparedRef = await this.store.putLocalBlob(preparedBlob, {
          mimeType: mime,
          ext: formatToExt(prepared.format),
        });
        const thumb = await makeThumbnailBlob(prepared);
        thumbRef = await this.store.putLocalBlob(thumb.blob, {
          mimeType: thumb.blob.type || mime,
          ext: thumb.ext,
        });
        if (!this.store.hasPersistedPreparedImages) {
          this.blobCache.set(preparedRef, preparedBlob);
        }
        this.blobCache.set(thumbRef, thumb.blob);
        this.emitBlobs();
        this.onEvent({
          case: "attachmentPrepareImageResult",
          value: create(AttachmentPrepareImageResultEventSchema, {
            ...base,
            ok: true,
            preparedRef,
            thumbRef,
            format: protoFmt,
            width: prepared.width,
            height: prepared.height,
            sizeBytes: BigInt(prepared.bytes.byteLength),
            sha256,
          }),
        });
      } catch (err) {
        const cleanup = [preparedRef, thumbRef].filter(Boolean);
        if (cleanup.length > 0) {
          await this.store.deleteLocalRefs(cleanup).catch(() => undefined);
        }
        const c = classifyPrepareError(err);
        this.onEvent({
          case: "attachmentPrepareImageResult",
          value: create(AttachmentPrepareImageResultEventSchema, {
            ...base,
            ok: false,
            errorKind: c.errorKind,
            errorCode: c.errorCode,
            errorDetail: c.errorDetail,
            isReadingSource: c.isReadingSource,
          }),
        });
      }
    })();
    this.trackIO(work);
  }

  handleHashFile(eff: AttachmentHashFileEffect) {
    const work = (async () => {
      const base = {
        opId: eff.opId,
        scope: eff.scope,
        outboxRequestId: eff.outboxRequestId,
        itemId: eff.itemId,
      };
      try {
        const hashed = await this.store.hashLocalBlob(eff.sourceRef, Number(eff.maxBytes));
        if (hashed.kind === "missing") {
          this.onEvent({
            case: "attachmentHashFileResult",
            value: create(AttachmentHashFileResultEventSchema, {
              ...base,
              ok: false,
              errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
            }),
          });
          return;
        }
        if (hashed.kind === "too_large") {
          this.onEvent({
            case: "attachmentHashFileResult",
            value: create(AttachmentHashFileResultEventSchema, {
              ...base,
              ok: false,
              sizeBytes: BigInt(hashed.sizeBytes),
              errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_FILE_TOO_LARGE,
            }),
          });
          return;
        }
        if (hashed.kind === "changed") {
          this.onEvent({
            case: "attachmentHashFileResult",
            value: create(AttachmentHashFileResultEventSchema, {
              ...base,
              ok: false,
              errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
            }),
          });
          return;
        }
        let hasDuration = false;
        let durationMs = 0n;
        if (eff.isVideo) {
          const source = await this.store.getLocalBlob(eff.sourceRef);
          const ms = source ? await this.probeVideoDurationFn(source).catch(() => undefined) : undefined;
          if (ms !== undefined && Number.isFinite(ms) && ms > 0) {
            hasDuration = true;
            durationMs = BigInt(Math.round(ms));
          }
        }
        this.onEvent({
          case: "attachmentHashFileResult",
          value: create(AttachmentHashFileResultEventSchema, {
            ...base,
            ok: true,
            sizeBytes: BigInt(hashed.sizeBytes),
            sha256: hashed.sha256,
            hasDuration,
            durationMs,
          }),
        });
      } catch (err) {
        const errorKind =
          err instanceof PartialTruncatedError
            ? AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED
            : hasQuotaExceeded(err)
              ? AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL
              : AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER;
        this.onEvent({
          case: "attachmentHashFileResult",
          value: create(AttachmentHashFileResultEventSchema, {
            ...base,
            ok: false,
            errorKind,
            errorDetail: errorDetailOf(err),
          }),
        });
      }
    })();
    this.trackIO(work);
  }

  handleReadChunk(eff: AttachmentReadChunkEffect) {
    const work = (async () => {
      const base = {
        opId: eff.opId,
        scope: eff.scope,
        outboxRequestId: eff.outboxRequestId,
        itemId: eff.itemId,
        offset: eff.offset,
      };
      const ref = eff.kind === AttachmentItemKind.IMAGE ? eff.preparedRef : eff.sourceRef;
      if (!ref) {
        this.onEvent({
          case: "attachmentReadChunkResult",
          value: create(AttachmentReadChunkResultEventSchema, {
            ...base,
            ok: false,
            errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
          }),
        });
        return;
      }
      try {
        const res = await this.store.readLocalSlice(
          ref,
          Number(eff.offset),
          eff.length,
          Number(eff.expectedSizeBytes),
        );
        if (res.kind === "missing") {
          this.onEvent({
            case: "attachmentReadChunkResult",
            value: create(AttachmentReadChunkResultEventSchema, {
              ...base,
              ok: false,
              errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
            }),
          });
          return;
        }
        if (res.kind === "changed") {
          this.onEvent({
            case: "attachmentReadChunkResult",
            value: create(AttachmentReadChunkResultEventSchema, {
              ...base,
              ok: false,
              errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
            }),
          });
          return;
        }
        this.onEvent({
          case: "attachmentReadChunkResult",
          value: create(AttachmentReadChunkResultEventSchema, {
            ...base,
            ok: true,
            data: res.data as Uint8Array<ArrayBuffer>,
          }),
        });
      } catch (err) {
        this.onEvent({
          case: "attachmentReadChunkResult",
          value: create(AttachmentReadChunkResultEventSchema, {
            ...base,
            ok: false,
            errorKind: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER,
            errorDetail: errorDetailOf(err),
          }),
        });
      }
    })();
    this.trackIO(work);
  }
}
