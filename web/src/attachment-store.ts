import {
  clearAllDraftRecords,
  deleteDraftRecord,
  listDraftRecords,
  readDraftRecord,
  writeDraftRecord,
  type DraftRecordBucketName,
} from "./core/security-store-v4";
import { newRequestID } from "./crypto";
import { DraftStorageBucket } from "./gen/remoteagent/core/v1/attachments_pb";
import { hasQuotaExceeded, StorageQuotaError } from "./shared-file-store";

export interface StoredDraftEntry {
  bucket: DraftStorageBucket;
  key: string;
  data: Uint8Array;
  corrupt: boolean;
}

export interface ReadDraftRecordResult {
  found: boolean;
  data?: Uint8Array;
  corrupt?: boolean;
}

export type ReadLocalSliceResult =
  | { kind: "ok"; data: Uint8Array }
  | { kind: "missing" }
  | { kind: "changed" };

export interface AttachmentStore {
  readonly hasPersistedPreparedImages: boolean;
  probe(): Promise<boolean>;
  /** Synchronously mints an opaque ref and holds the Blob in memory until persisted or released. */
  stageBlob(blob: Blob | Uint8Array, opts?: { mimeType?: string; ext?: string }): string;
  /** Stores a newly prepared/thumbnail blob (persisting to OPFS when available). */
  putLocalBlob(blob: Blob | Uint8Array, opts?: { mimeType?: string; ext?: string }): Promise<string>;
  /** Returns a lazy Blob/File handle for reading or UI preview without loading large files into RAM. */
  getLocalBlob(ref: string): Promise<Blob | undefined>;
  /** Synchronous lookup for blobs already staged or cached in memory. */
  peekMemoryBlob(ref: string): Blob | undefined;
  /** Reads exact [offset, offset+length) bytes after verifying existence and expectedSize. */
  readLocalSlice(ref: string, offset: number, length: number, expectedSize: number): Promise<ReadLocalSliceResult>;
  /** Deletes the given opaque refs from both memory and persistent storage. */
  deleteLocalRefs(refs: readonly string[]): Promise<void>;
  /** Enumerates all DRAFT/OUTBOX records plus all existing local attachment refs for orphan GC. */
  list(): Promise<{ entries: StoredDraftEntry[]; localRefs: string[] }>;
  read(bucket: DraftStorageBucket, key: string): Promise<ReadDraftRecordResult>;
  write(bucket: DraftStorageBucket, key: string, data: Uint8Array, referencedRefs?: readonly string[]): Promise<void>;
  delete(bucket: DraftStorageBucket, key: string): Promise<void>;
  clearAll(): Promise<void>;
}

const OPFS_ROOT_DIR = "remote-agent-attachments";
const OPFS_BLOBS_DIR = "blobs";
const STREAM_SLICE_BYTES = 1024 * 1024;

type IterableDirectoryHandle = FileSystemDirectoryHandle & {
  entries(): AsyncIterableIterator<[string, FileSystemHandle]>;
};

const EXT_TO_MIME: Record<string, string> = {
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  png: "image/png",
  webp: "image/webp",
  gif: "image/gif",
  mp4: "video/mp4",
  mov: "video/quicktime",
  webm: "video/webm",
  m4v: "video/x-m4v",
};

const MIME_TO_EXT: Record<string, string> = {
  "image/jpeg": "jpg",
  "image/png": "png",
  "image/webp": "webp",
  "image/gif": "gif",
  "video/mp4": "mp4",
  "video/quicktime": "mov",
  "video/webm": "webm",
  "video/x-m4v": "m4v",
};

function normalizeExt(opts?: { mimeType?: string; ext?: string }): string {
  const raw = (opts?.ext ?? "").toLowerCase().replace(/[^a-z0-9]/g, "");
  if (raw && raw.length <= 8) return raw;
  const mime = (opts?.mimeType ?? "").toLowerCase().split(";")[0].trim();
  return MIME_TO_EXT[mime] ?? "bin";
}

function mimeFromRef(ref: string): string {
  const dot = ref.lastIndexOf(".");
  if (dot < 0) return "";
  return EXT_TO_MIME[ref.slice(dot + 1).toLowerCase()] ?? "";
}

function mintRef(opts?: { mimeType?: string; ext?: string }): string {
  const ext = normalizeExt(opts);
  const id = newRequestID().replace(/[^a-zA-Z0-9_-]/g, "");
  return `att_${id}.${ext}`;
}

function toBlob(input: Blob | Uint8Array, mimeType?: string): Blob {
  if (input instanceof Blob) {
    if (mimeType && !input.type) return new Blob([input], { type: mimeType });
    return input;
  }
  return new Blob([input as Uint8Array<ArrayBuffer>], { type: mimeType || "application/octet-stream" });
}

function bucketToName(bucket: DraftStorageBucket): DraftRecordBucketName {
  return bucket === DraftStorageBucket.OUTBOX ? "outbox" : "draft";
}

function nameToBucket(name: DraftRecordBucketName): DraftStorageBucket {
  return name === "outbox" ? DraftStorageBucket.OUTBOX : DraftStorageBucket.DRAFT;
}

function isNotFound(err: unknown): boolean {
  return !!err && typeof err === "object" && (err as { name?: string }).name === "NotFoundError";
}

async function removeQuiet(dir: FileSystemDirectoryHandle, name: string, recursive = false): Promise<void> {
  try {
    await dir.removeEntry(name, { recursive });
  } catch {
    /* already absent */
  }
}

async function readSliceFromBlob(blob: Blob, offset: number, length: number, expectedSize: number): Promise<ReadLocalSliceResult> {
  if (blob.size !== expectedSize) return { kind: "changed" };
  if (length === 0) return { kind: "ok", data: new Uint8Array() };
  if (offset < 0 || length < 0 || offset + length > blob.size) return { kind: "changed" };
  const buf = await blob.slice(offset, offset + length).arrayBuffer();
  if (buf.byteLength !== length) return { kind: "changed" };
  return { kind: "ok", data: new Uint8Array(buf) };
}

/** Pure in-memory AttachmentStore used in DemoRemote, unit tests, and OPFS fallback. */
export class MemoryAttachmentStore implements AttachmentStore {
  private readonly records = new Map<string, { bucket: DraftStorageBucket; key: string; data: Uint8Array; corrupt: boolean }>();
  private readonly blobs = new Map<string, Blob>();
  private readonly maxBytes?: number;
  private usedBytes = 0;
  readonly hasPersistedPreparedImages: boolean;

  constructor(opts?: { maxBytes?: number; hasPersistedPreparedImages?: boolean }) {
    this.maxBytes = opts?.maxBytes;
    this.hasPersistedPreparedImages = opts?.hasPersistedPreparedImages ?? false;
  }

  private recKey(bucket: DraftStorageBucket, key: string): string {
    return `${bucket}:${key}`;
  }

  private ensureSpace(delta: number) {
    if (this.maxBytes !== undefined && delta > 0 && this.usedBytes + delta > this.maxBytes) {
      throw new StorageQuotaError();
    }
  }

  async probe(): Promise<boolean> {
    return this.hasPersistedPreparedImages;
  }

  stageBlob(blob: Blob | Uint8Array, opts?: { mimeType?: string; ext?: string }): string {
    const b = toBlob(blob, opts?.mimeType);
    const ref = mintRef({ mimeType: opts?.mimeType ?? b.type, ext: opts?.ext });
    this.blobs.set(ref, b);
    this.usedBytes += b.size;
    return ref;
  }

  async putLocalBlob(blob: Blob | Uint8Array, opts?: { mimeType?: string; ext?: string }): Promise<string> {
    const b = toBlob(blob, opts?.mimeType);
    this.ensureSpace(b.size);
    const ref = mintRef({ mimeType: opts?.mimeType ?? b.type, ext: opts?.ext });
    this.blobs.set(ref, b);
    this.usedBytes += b.size;
    return ref;
  }

  async getLocalBlob(ref: string): Promise<Blob | undefined> {
    return this.blobs.get(ref);
  }

  peekMemoryBlob(ref: string): Blob | undefined {
    return this.blobs.get(ref);
  }

  async readLocalSlice(ref: string, offset: number, length: number, expectedSize: number): Promise<ReadLocalSliceResult> {
    const blob = this.blobs.get(ref);
    if (!blob) return { kind: "missing" };
    return readSliceFromBlob(blob, offset, length, expectedSize);
  }

  async deleteLocalRefs(refs: readonly string[]): Promise<void> {
    for (const ref of refs) {
      const prev = this.blobs.get(ref);
      if (prev) {
        this.usedBytes = Math.max(0, this.usedBytes - prev.size);
        this.blobs.delete(ref);
      }
    }
  }

  async list(): Promise<{ entries: StoredDraftEntry[]; localRefs: string[] }> {
    const entries: StoredDraftEntry[] = [];
    for (const r of this.records.values()) {
      entries.push({ bucket: r.bucket, key: r.key, data: r.data.slice(), corrupt: r.corrupt });
    }
    return { entries, localRefs: [...this.blobs.keys()] };
  }

  async read(bucket: DraftStorageBucket, key: string): Promise<ReadDraftRecordResult> {
    const r = this.records.get(this.recKey(bucket, key));
    if (!r) return { found: false };
    if (r.corrupt) return { found: false, corrupt: true };
    return { found: true, data: r.data.slice() };
  }

  async write(bucket: DraftStorageBucket, key: string, data: Uint8Array): Promise<void> {
    const rk = this.recKey(bucket, key);
    const prev = this.records.get(rk);
    const delta = data.byteLength - (prev?.data.byteLength ?? 0);
    this.ensureSpace(delta);
    this.records.set(rk, { bucket, key, data: data.slice(), corrupt: false });
    this.usedBytes = Math.max(0, this.usedBytes + delta);
  }

  async delete(bucket: DraftStorageBucket, key: string): Promise<void> {
    const rk = this.recKey(bucket, key);
    const prev = this.records.get(rk);
    if (prev) {
      this.usedBytes = Math.max(0, this.usedBytes - prev.data.byteLength);
      this.records.delete(rk);
    }
  }

  async clearAll(): Promise<void> {
    this.records.clear();
    this.blobs.clear();
    this.usedBytes = 0;
  }

  /** Test helper: seeds a raw/corrupt record directly into the store. */
  seedRecord(bucket: DraftStorageBucket, key: string, data: Uint8Array, corrupt = false) {
    this.records.set(this.recKey(bucket, key), { bucket, key, data: data.slice(), corrupt });
  }

  /** Test helper: seeds a blob under an explicit ref name (e.g. orphan GC tests). */
  seedBlob(ref: string, blob: Blob) {
    this.blobs.set(ref, blob);
  }

  /** Test helper: replaces a blob under an existing ref (e.g. source-changed tests). */
  replaceBlob(ref: string, blob: Blob) {
    this.blobs.set(ref, blob);
  }
}

async function writeBlobToOpfs(dir: FileSystemDirectoryHandle, name: string, blob: Blob): Promise<void> {
  const tmpName = `${name}.tmp`;
  const targetHandle = await dir.getFileHandle(tmpName, { create: true });
  if (typeof targetHandle.createWritable !== "function") {
    await removeQuiet(dir, tmpName);
    throw new Error("OPFS createWritable unavailable");
  }
  const w = await targetHandle.createWritable({ keepExistingData: false });
  try {
    for (let off = 0; off < blob.size; off += STREAM_SLICE_BYTES) {
      const end = Math.min(blob.size, off + STREAM_SLICE_BYTES);
      const buf = await blob.slice(off, end).arrayBuffer();
      if (buf.byteLength > 0) await w.write(new Uint8Array(buf));
    }
    await w.close();
  } catch (err) {
    try {
      await w.abort();
    } catch {
      /* ignore */
    }
    await removeQuiet(dir, tmpName);
    throw err;
  }
  const movable = targetHandle as FileSystemFileHandle & { move?: (newName: string) => Promise<void> };
  if (typeof movable.move === "function") {
    await removeQuiet(dir, name);
    await movable.move(name);
    return;
  }
  // Fallback if OPFS move() is unavailable: write directly into final name.
  const finalHandle = await dir.getFileHandle(name, { create: true });
  const fw = await finalHandle.createWritable({ keepExistingData: false });
  try {
    for (let off = 0; off < blob.size; off += STREAM_SLICE_BYTES) {
      const end = Math.min(blob.size, off + STREAM_SLICE_BYTES);
      const buf = await blob.slice(off, end).arrayBuffer();
      if (buf.byteLength > 0) await fw.write(new Uint8Array(buf));
    }
    await fw.close();
  } catch (err) {
    try {
      await fw.abort();
    } catch {
      /* ignore */
    }
    await removeQuiet(dir, name);
    throw err;
  } finally {
    await removeQuiet(dir, tmpName);
  }
}

/** Persistent Web AttachmentStore:
 * - DRAFT/OUTBOX records are sealed with non-extractable AES-GCM-256 keys in IndexedDB (v4 security store).
 * - Local attachment blobs (source/prepared/thumb) are stored in OPFS (streamed in 1 MiB slices) when available,
 *   falling back cleanly to in-memory storage when OPFS or createWritable is unavailable. */
export class WebAttachmentStore implements AttachmentStore {
  private readonly stagedBlobs = new Map<string, Blob>();
  private readonly memoryBlobs = new Map<string, Blob>();
  private readonly fallbackRecords = new Map<string, Uint8Array>();
  private opfsAvailable: boolean | undefined;
  private probePromise?: Promise<boolean>;

  constructor(
    private readonly account?: { server: string; deviceId: string },
    private readonly idb?: IDBFactory,
  ) {}

  get hasPersistedPreparedImages(): boolean {
    if (this.opfsAvailable !== undefined) return this.opfsAvailable;
    return typeof navigator !== "undefined" && typeof navigator.storage?.getDirectory === "function";
  }

  private useIdb(): boolean {
    return !!this.account?.server && !!this.account?.deviceId && (this.idb !== undefined || typeof indexedDB !== "undefined");
  }

  private getIDB(): IDBFactory {
    return this.idb ?? indexedDB;
  }

  async probe(): Promise<boolean> {
    if (this.opfsAvailable !== undefined) return this.opfsAvailable;
    if (this.probePromise) return this.probePromise;
    this.probePromise = (async () => {
      if (typeof navigator === "undefined" || typeof navigator.storage?.getDirectory !== "function") {
        this.opfsAvailable = false;
        return false;
      }
      try {
        const root = await navigator.storage.getDirectory();
        const attDir = await root.getDirectoryHandle(OPFS_ROOT_DIR, { create: true });
        const blobsDir = await attDir.getDirectoryHandle(OPFS_BLOBS_DIR, { create: true });
        const testName = ".probe";
        const fh = await blobsDir.getFileHandle(testName, { create: true });
        const ok = typeof fh.createWritable === "function";
        await removeQuiet(blobsDir, testName);
        this.opfsAvailable = ok;
        return ok;
      } catch {
        this.opfsAvailable = false;
        return false;
      }
    })();
    return this.probePromise;
  }

  private async blobsDir(create: boolean): Promise<FileSystemDirectoryHandle | undefined> {
    if (this.opfsAvailable === false) return undefined;
    if (this.opfsAvailable === undefined) {
      const ok = await this.probe();
      if (!ok) return undefined;
    }
    try {
      const root = await navigator.storage.getDirectory();
      const attDir = await root.getDirectoryHandle(OPFS_ROOT_DIR, { create });
      return await attDir.getDirectoryHandle(OPFS_BLOBS_DIR, { create });
    } catch (err) {
      if (!create && isNotFound(err)) return undefined;
      if (hasQuotaExceeded(err)) throw new StorageQuotaError();
      this.opfsAvailable = false;
      return undefined;
    }
  }

  stageBlob(blob: Blob | Uint8Array, opts?: { mimeType?: string; ext?: string }): string {
    const b = toBlob(blob, opts?.mimeType);
    const ref = mintRef({ mimeType: opts?.mimeType ?? b.type, ext: opts?.ext });
    this.stagedBlobs.set(ref, b);
    return ref;
  }

  async putLocalBlob(blob: Blob | Uint8Array, opts?: { mimeType?: string; ext?: string }): Promise<string> {
    const b = toBlob(blob, opts?.mimeType);
    const ref = mintRef({ mimeType: opts?.mimeType ?? b.type, ext: opts?.ext });
    const dir = await this.blobsDir(true);
    if (!dir) {
      this.memoryBlobs.set(ref, b);
      return ref;
    }
    try {
      await writeBlobToOpfs(dir, ref, b);
      return ref;
    } catch (err) {
      if (hasQuotaExceeded(err)) throw new StorageQuotaError();
      // OPFS became unavailable at write time -> fall back cleanly to memory.
      this.opfsAvailable = false;
      this.memoryBlobs.set(ref, b);
      return ref;
    }
  }

  private async persistStagedRefs(refs: readonly string[]): Promise<void> {
    if (!refs.length) return;
    let dir: FileSystemDirectoryHandle | undefined;
    for (const ref of refs) {
      const staged = this.stagedBlobs.get(ref);
      if (!staged) continue;
      if (this.opfsAvailable === false) {
        this.memoryBlobs.set(ref, staged);
        this.stagedBlobs.delete(ref);
        continue;
      }
      if (!dir) dir = await this.blobsDir(true);
      if (!dir) {
        this.memoryBlobs.set(ref, staged);
        this.stagedBlobs.delete(ref);
        continue;
      }
      try {
        await writeBlobToOpfs(dir, ref, staged);
        this.stagedBlobs.delete(ref);
      } catch (err) {
        if (hasQuotaExceeded(err)) throw new StorageQuotaError();
        this.opfsAvailable = false;
        this.memoryBlobs.set(ref, staged);
        this.stagedBlobs.delete(ref);
      }
    }
  }

  peekMemoryBlob(ref: string): Blob | undefined {
    return this.stagedBlobs.get(ref) ?? this.memoryBlobs.get(ref);
  }

  async getLocalBlob(ref: string): Promise<Blob | undefined> {
    const mem = this.peekMemoryBlob(ref);
    if (mem) return mem;
    const dir = await this.blobsDir(false);
    if (!dir) return undefined;
    try {
      const file = await (await dir.getFileHandle(ref)).getFile();
      const mime = mimeFromRef(ref);
      return mime && !file.type ? new Blob([file], { type: mime }) : file;
    } catch (err) {
      if (isNotFound(err)) return undefined;
      throw err;
    }
  }

  async readLocalSlice(ref: string, offset: number, length: number, expectedSize: number): Promise<ReadLocalSliceResult> {
    const blob = await this.getLocalBlob(ref);
    if (!blob) return { kind: "missing" };
    return readSliceFromBlob(blob, offset, length, expectedSize);
  }

  async deleteLocalRefs(refs: readonly string[]): Promise<void> {
    if (!refs.length) return;
    for (const ref of refs) {
      this.stagedBlobs.delete(ref);
      this.memoryBlobs.delete(ref);
    }
    const dir = await this.blobsDir(false);
    if (!dir) return;
    for (const ref of refs) {
      await removeQuiet(dir, ref);
      await removeQuiet(dir, `${ref}.tmp`);
    }
  }

  async list(): Promise<{ entries: StoredDraftEntry[]; localRefs: string[] }> {
    const entries: StoredDraftEntry[] = [];
    if (this.useIdb()) {
      const recs = await listDraftRecords(this.account!.server, this.account!.deviceId, this.getIDB());
      for (const r of recs) {
        entries.push({ bucket: nameToBucket(r.bucket), key: r.key, data: r.data, corrupt: r.corrupt });
      }
    } else {
      for (const [rk, data] of this.fallbackRecords.entries()) {
        const idx = rk.indexOf(":");
        if (idx <= 0) continue;
        entries.push({
          bucket: Number(rk.slice(0, idx)) as DraftStorageBucket,
          key: rk.slice(idx + 1),
          data: data.slice(),
          corrupt: false,
        });
      }
    }

    const refSet = new Set<string>([...this.stagedBlobs.keys(), ...this.memoryBlobs.keys()]);
    const dir = await this.blobsDir(false);
    if (dir && typeof (dir as IterableDirectoryHandle).entries === "function") {
      for await (const [name, handle] of (dir as IterableDirectoryHandle).entries()) {
        if (handle.kind !== "file") continue;
        if (name === ".probe") continue;
        if (name.endsWith(".tmp")) {
          await removeQuiet(dir, name);
          continue;
        }
        refSet.add(name);
      }
    }
    return { entries, localRefs: [...refSet] };
  }

  async read(bucket: DraftStorageBucket, key: string): Promise<ReadDraftRecordResult> {
    if (this.useIdb()) {
      return readDraftRecord(this.account!.server, this.account!.deviceId, bucketToName(bucket), key, this.getIDB());
    }
    const data = this.fallbackRecords.get(`${bucket}:${key}`);
    return data ? { found: true, data: data.slice() } : { found: false };
  }

  async write(bucket: DraftStorageBucket, key: string, data: Uint8Array, referencedRefs: readonly string[] = []): Promise<void> {
    await this.persistStagedRefs(referencedRefs);
    if (this.useIdb()) {
      try {
        await writeDraftRecord(this.account!.server, this.account!.deviceId, bucketToName(bucket), key, data, this.getIDB());
      } catch (err) {
        if (hasQuotaExceeded(err)) throw new StorageQuotaError();
        throw err;
      }
      return;
    }
    this.fallbackRecords.set(`${bucket}:${key}`, data.slice());
  }

  async delete(bucket: DraftStorageBucket, key: string): Promise<void> {
    if (this.useIdb()) {
      await deleteDraftRecord(bucketToName(bucket), key, this.getIDB());
      return;
    }
    this.fallbackRecords.delete(`${bucket}:${key}`);
  }

  async clearAll(): Promise<void> {
    this.stagedBlobs.clear();
    this.memoryBlobs.clear();
    this.fallbackRecords.clear();
    if (this.useIdb()) {
      await clearAllDraftRecords(this.getIDB()).catch(() => undefined);
    }
    await clearDefaultAttachmentOpfs();
  }
}

/** Removes the OPFS attachment root directory (used by logout teardown when no CoreHost is active). */
export async function clearDefaultAttachmentOpfs(): Promise<void> {
  if (typeof navigator === "undefined" || typeof navigator.storage?.getDirectory !== "function") return;
  try {
    const root = await navigator.storage.getDirectory();
    await removeQuiet(root, OPFS_ROOT_DIR, true);
  } catch {
    /* ignore */
  }
}
