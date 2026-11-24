import {
  clearAllDraftRecords,
  deleteDraftRecord,
  getOrCreateAttachmentBlobKey,
  listDraftRecords,
  loadAttachmentBlobKey,
  purgeLegacyComposerLocalData,
  readDraftRecord,
  writeDraftRecord,
  type DraftRecordBucketName,
} from "./core/security-store-v4";
import { newRequestID } from "./crypto";
import { DraftStorageBucket } from "./gen/remoteagent/core/v1/attachments_pb";
import { hashBlobStreaming, StreamingSha256 } from "./shared-file-sha256";
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

export type HashLocalBlobResult =
  | { kind: "ok"; sizeBytes: number; sha256: Uint8Array<ArrayBuffer> }
  | { kind: "missing" }
  | { kind: "too_large"; sizeBytes: number }
  | { kind: "changed" };

export interface AttachmentStore {
  readonly hasPersistedPreparedImages: boolean;
  /** True when this tab owns persistent draft/outbox storage (Web Locks). */
  readonly storageOwned: boolean;
  /** Optional callback fired when OPFS degrades to memory mid-session (L7). */
  onOpfsDegraded?: () => void;
  /** Attempts to acquire exclusive tab ownership of persistent draft/outbox storage (M5). */
  acquireStorageLock?(): Promise<boolean>;
  /** Releases exclusive tab ownership lock (called on dispose/logout). */
  releaseStorageLock?(): void;
  probe(): Promise<boolean>;
  /** Synchronously mints an opaque ref and holds the Blob in memory until persisted or released. */
  stageBlob(blob: Blob | Uint8Array, opts?: { mimeType?: string; ext?: string }): string;
  /** Stores a newly prepared/thumbnail blob (persisting encrypted chunks to OPFS when available). */
  putLocalBlob(blob: Blob | Uint8Array, opts?: { mimeType?: string; ext?: string }): Promise<string>;
  /** Returns a decrypted Blob/File handle for UI preview or image preparation. */
  getLocalBlob(ref: string): Promise<Blob | undefined>;
  /** Synchronous lookup for blobs already staged or cached in memory. */
  peekMemoryBlob(ref: string): Blob | undefined;
  /** Streams SHA-256 over the local blob without loading large encrypted OPFS files into RAM. */
  hashLocalBlob(ref: string, maxBytes: number): Promise<HashLocalBlobResult>;
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
const OPFS_BLOB_MAGIC = new Uint8Array([0x52, 0x41, 0x42, 0x31]); // "RAB1"
const OPFS_BLOB_HEADER_BYTES = 16;
const OPFS_BLOB_CHUNK_BYTES = 256 * 1024;
const OPFS_GCM_IV_BYTES = 12;
const OPFS_GCM_TAG_BYTES = 16;
const OPFS_GCM_OVERHEAD_BYTES = OPFS_GCM_IV_BYTES + OPFS_GCM_TAG_BYTES;
const TEXT_ENCODER = new TextEncoder();

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
  readonly storageOwned = true;
  onOpfsDegraded?: () => void;

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

  async hashLocalBlob(ref: string, maxBytes: number): Promise<HashLocalBlobResult> {
    const blob = this.blobs.get(ref);
    if (!blob) return { kind: "missing" };
    if (maxBytes > 0 && blob.size > maxBytes) return { kind: "too_large", sizeBytes: blob.size };
    const sha256 = (await hashBlobStreaming(blob, blob.size)) as Uint8Array<ArrayBuffer>;
    return { kind: "ok", sizeBytes: blob.size, sha256 };
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

function opfsBlobChunkAad(owner: string, ref: string, totalSize: number, chunkIndex: number): Uint8Array<ArrayBuffer> {
  return TEXT_ENCODER.encode(JSON.stringify(["RAB1", owner, ref, totalSize, chunkIndex])) as Uint8Array<ArrayBuffer>;
}

function buildEncryptedOpfsHeader(totalSize: number, chunkSize: number): Uint8Array<ArrayBuffer> {
  const buf = new ArrayBuffer(OPFS_BLOB_HEADER_BYTES);
  const bytes = new Uint8Array(buf);
  bytes.set(OPFS_BLOB_MAGIC, 0);
  const view = new DataView(buf);
  view.setBigUint64(4, BigInt(totalSize), false);
  view.setUint32(12, chunkSize, false);
  return bytes;
}

async function readEncryptedOpfsHeader(file: Blob): Promise<{ totalSize: number; chunkSize: number } | undefined> {
  if (file.size < OPFS_BLOB_HEADER_BYTES) return undefined;
  const raw = new Uint8Array(await file.slice(0, OPFS_BLOB_HEADER_BYTES).arrayBuffer());
  if (raw.byteLength !== OPFS_BLOB_HEADER_BYTES) return undefined;
  for (let i = 0; i < OPFS_BLOB_MAGIC.length; i++) {
    if (raw[i] !== OPFS_BLOB_MAGIC[i]) return undefined;
  }
  const view = new DataView(raw.buffer, raw.byteOffset, raw.byteLength);
  const totalBig = view.getBigUint64(4, false);
  if (totalBig > BigInt(Number.MAX_SAFE_INTEGER)) return undefined;
  const totalSize = Number(totalBig);
  const chunkSize = view.getUint32(12, false);
  if (chunkSize <= 0) return undefined;
  const chunkCount = totalSize === 0 ? 0 : Math.ceil(totalSize / chunkSize);
  const expectedFileSize = OPFS_BLOB_HEADER_BYTES + totalSize + chunkCount * OPFS_GCM_OVERHEAD_BYTES;
  if (file.size !== expectedFileSize) return undefined;
  return { totalSize, chunkSize };
}

async function decryptOpfsChunk(
  file: Blob,
  ref: string,
  key: CryptoKey,
  owner: string,
  totalSize: number,
  chunkSize: number,
  chunkIndex: number,
): Promise<Uint8Array<ArrayBuffer>> {
  const totalChunks = Math.ceil(totalSize / chunkSize);
  const plainStart = chunkIndex * chunkSize;
  const plainLen = chunkIndex === totalChunks - 1 ? totalSize - plainStart : chunkSize;
  const fullFrameBytes = chunkSize + OPFS_GCM_OVERHEAD_BYTES;
  const frameStart = OPFS_BLOB_HEADER_BYTES + chunkIndex * fullFrameBytes;
  const frameLen = plainLen + OPFS_GCM_OVERHEAD_BYTES;
  const frame = new Uint8Array(await file.slice(frameStart, frameStart + frameLen).arrayBuffer());
  if (frame.byteLength !== frameLen) throw new Error("corrupt encrypted opfs chunk");
  const iv = frame.subarray(0, OPFS_GCM_IV_BYTES);
  const cipher = frame.subarray(OPFS_GCM_IV_BYTES);
  const plain = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv, additionalData: opfsBlobChunkAad(owner, ref, totalSize, chunkIndex) },
    key,
    cipher,
  );
  return new Uint8Array(plain);
}

async function writeEncryptedStreamToWritable(
  w: FileSystemWritableFileStream,
  blob: Blob,
  ref: string,
  key: CryptoKey,
  owner: string,
): Promise<void> {
  const totalSize = blob.size;
  await w.write(buildEncryptedOpfsHeader(totalSize, OPFS_BLOB_CHUNK_BYTES));
  let chunkIndex = 0;
  for (let off = 0; off < totalSize; off += OPFS_BLOB_CHUNK_BYTES) {
    const end = Math.min(totalSize, off + OPFS_BLOB_CHUNK_BYTES);
    const plain = await blob.slice(off, end).arrayBuffer();
    const iv = crypto.getRandomValues(new Uint8Array(OPFS_GCM_IV_BYTES));
    const cipher = new Uint8Array(
      await crypto.subtle.encrypt(
        { name: "AES-GCM", iv, additionalData: opfsBlobChunkAad(owner, ref, totalSize, chunkIndex) },
        key,
        plain,
      ),
    );
    const frame = new Uint8Array(OPFS_GCM_IV_BYTES + cipher.byteLength);
    frame.set(iv, 0);
    frame.set(cipher, OPFS_GCM_IV_BYTES);
    await w.write(frame);
    chunkIndex++;
  }
}

async function writeEncryptedBlobToOpfs(
  dir: FileSystemDirectoryHandle,
  name: string,
  blob: Blob,
  key: CryptoKey,
  owner: string,
): Promise<void> {
  const tmpName = `${name}.tmp`;
  const targetHandle = await dir.getFileHandle(tmpName, { create: true });
  if (typeof targetHandle.createWritable !== "function") {
    await removeQuiet(dir, tmpName);
    throw new Error("OPFS createWritable unavailable");
  }
  const w = await targetHandle.createWritable({ keepExistingData: false });
  try {
    await writeEncryptedStreamToWritable(w, blob, name, key, owner);
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
    await writeEncryptedStreamToWritable(fw, blob, name, key, owner);
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

async function readEncryptedOpfsSlice(
  file: Blob,
  ref: string,
  key: CryptoKey,
  owner: string,
  offset: number,
  length: number,
  expectedSize: number,
): Promise<ReadLocalSliceResult> {
  const hdr = await readEncryptedOpfsHeader(file);
  if (!hdr) return { kind: "changed" };
  if (hdr.totalSize !== expectedSize) return { kind: "changed" };
  if (length === 0) return { kind: "ok", data: new Uint8Array() };
  if (offset < 0 || length < 0 || offset + length > hdr.totalSize) return { kind: "changed" };
  const firstChunk = Math.floor(offset / hdr.chunkSize);
  const lastChunk = Math.floor((offset + length - 1) / hdr.chunkSize);
  const out = new Uint8Array(length);
  let outPos = 0;
  for (let idx = firstChunk; idx <= lastChunk; idx++) {
    const plain = await decryptOpfsChunk(file, ref, key, owner, hdr.totalSize, hdr.chunkSize, idx);
    const chunkStart = idx * hdr.chunkSize;
    const copyStart = Math.max(offset, chunkStart) - chunkStart;
    const copyEnd = Math.min(offset + length, chunkStart + plain.byteLength) - chunkStart;
    out.set(plain.subarray(copyStart, copyEnd), outPos);
    outPos += copyEnd - copyStart;
  }
  return { kind: "ok", data: out };
}

async function decryptOpfsBlobFull(file: Blob, ref: string, key: CryptoKey, owner: string): Promise<Blob> {
  const hdr = await readEncryptedOpfsHeader(file);
  if (!hdr) throw new Error("corrupt encrypted opfs header");
  const mime = mimeFromRef(ref) || "application/octet-stream";
  if (hdr.totalSize === 0) return new Blob([], { type: mime });
  const totalChunks = Math.ceil(hdr.totalSize / hdr.chunkSize);
  const parts: Uint8Array<ArrayBuffer>[] = [];
  for (let idx = 0; idx < totalChunks; idx++) {
    parts.push(await decryptOpfsChunk(file, ref, key, owner, hdr.totalSize, hdr.chunkSize, idx));
  }
  return new Blob(parts, { type: mime });
}

async function hashEncryptedOpfsBlob(
  file: Blob,
  ref: string,
  key: CryptoKey,
  owner: string,
  maxBytes: number,
): Promise<HashLocalBlobResult> {
  const hdr = await readEncryptedOpfsHeader(file);
  if (!hdr) return { kind: "changed" };
  if (maxBytes > 0 && hdr.totalSize > maxBytes) {
    return { kind: "too_large", sizeBytes: hdr.totalSize };
  }
  const hasher = new StreamingSha256();
  if (hdr.totalSize > 0) {
    const totalChunks = Math.ceil(hdr.totalSize / hdr.chunkSize);
    for (let idx = 0; idx < totalChunks; idx++) {
      const plain = await decryptOpfsChunk(file, ref, key, owner, hdr.totalSize, hdr.chunkSize, idx);
      hasher.update(plain);
    }
  }
  return { kind: "ok", sizeBytes: hdr.totalSize, sha256: hasher.digest() as Uint8Array<ArrayBuffer> };
}

/** Persistent Web AttachmentStore:
 * - DRAFT/OUTBOX records are sealed with non-extractable AES-GCM-256 keys in IndexedDB (v4 security store).
 * - Local attachment blobs (source/prepared/thumb) are stored as chunked AES-GCM-256 encrypted files ("RAB1",
 *   256 KiB chunks, AAD bound to owner+ref+totalSize+chunkIndex) in OPFS using a non-extractable key in v4 META,
 *   falling back cleanly to in-memory storage when OPFS or createWritable is unavailable. */
export class WebAttachmentStore implements AttachmentStore {
  private readonly stagedBlobs = new Map<string, Blob>();
  private readonly memoryBlobs = new Map<string, Blob>();
  private readonly fallbackRecords = new Map<string, Uint8Array>();
  private opfsAvailable: boolean | undefined;
  private probePromise?: Promise<boolean>;
  private blobKey?: CryptoKey;
  private _storageOwned = true;
  private lockAttempted = false;
  private lockPromise?: Promise<boolean>;
  private releaseLockFn?: () => void;
  onOpfsDegraded?: () => void;

  constructor(
    private readonly account?: { server: string; deviceId: string },
    private readonly idb?: IDBFactory,
    private readonly getOpfsRoot?: () => Promise<FileSystemDirectoryHandle>,
    private readonly lockManager?: Pick<LockManager, "request">,
  ) {}

  get storageOwned(): boolean {
    return this._storageOwned;
  }

  get hasPersistedPreparedImages(): boolean {
    if (!this._storageOwned || !this.useIdb()) return false;
    if (this.opfsAvailable !== undefined) return this.opfsAvailable;
    return this.getOpfsRoot !== undefined || (typeof navigator !== "undefined" && typeof navigator.storage?.getDirectory === "function");
  }

  private getLocks(): Pick<LockManager, "request"> | undefined {
    if (this.lockManager) return this.lockManager;
    if (typeof navigator !== "undefined" && typeof navigator.locks?.request === "function") {
      return navigator.locks;
    }
    return undefined;
  }

  /** Acquires exclusive multi-tab ownership via Web Locks (M5). Non-owner tabs stay memory-only. */
  async acquireStorageLock(): Promise<boolean> {
    if (this.lockAttempted) return this._storageOwned;
    if (this.lockPromise) return this.lockPromise;
    if (!this.useIdb()) {
      this.lockAttempted = true;
      this._storageOwned = true;
      return true;
    }
    const locks = this.getLocks();
    if (!locks) {
      this.lockAttempted = true;
      this._storageOwned = true;
      return true;
    }
    const lockName = `ra-draft-owner:${this.account!.server}:${this.account!.deviceId}`;
    this.lockPromise = new Promise<boolean>((resolve) => {
      let settled = false;
      const done = (owned: boolean) => {
        if (settled) return;
        settled = true;
        this.lockAttempted = true;
        this._storageOwned = owned;
        if (!owned) {
          this.opfsAvailable = false;
        }
        resolve(owned);
      };
      void Promise.resolve(
        locks.request(lockName, { ifAvailable: true }, (lock) => {
          if (!lock) {
            done(false);
            return;
          }
          return new Promise<void>((release) => {
            this.releaseLockFn = release;
            done(true);
          });
        }),
      ).catch(() => {
        done(true);
      });
    });
    return this.lockPromise;
  }

  releaseStorageLock(): void {
    const fn = this.releaseLockFn;
    this.releaseLockFn = undefined;
    fn?.();
  }

  private markOpfsDegraded(): void {
    const wasAvailable = this.opfsAvailable === true;
    this.opfsAvailable = false;
    if (wasAvailable) {
      this.onOpfsDegraded?.();
    }
  }

  private useIdb(): boolean {
    return !!this.account?.server && !!this.account?.deviceId && (this.idb !== undefined || typeof indexedDB !== "undefined");
  }

  private getIDB(): IDBFactory {
    return this.idb ?? indexedDB;
  }

  private ownerBinding(): string {
    return JSON.stringify([this.account?.server ?? "", this.account?.deviceId ?? ""]);
  }

  private async ensureBlobKey(): Promise<CryptoKey> {
    if (this.blobKey) return this.blobKey;
    const key = await getOrCreateAttachmentBlobKey(this.account!.server, this.account!.deviceId, this.getIDB());
    this.blobKey = key;
    return key;
  }

  private async loadExistingBlobKey(): Promise<CryptoKey | undefined> {
    if (this.blobKey) return this.blobKey;
    if (!this.useIdb()) return undefined;
    const key = await loadAttachmentBlobKey(this.account!.server, this.account!.deviceId, this.getIDB());
    if (key) this.blobKey = key;
    return key;
  }

  private async opfsRoot(): Promise<FileSystemDirectoryHandle | undefined> {
    if (this.getOpfsRoot) return this.getOpfsRoot();
    if (typeof navigator === "undefined" || typeof navigator.storage?.getDirectory !== "function") {
      return undefined;
    }
    return navigator.storage.getDirectory();
  }

  async probe(): Promise<boolean> {
    if (!this._storageOwned || !this.useIdb()) {
      this.opfsAvailable = false;
      return false;
    }
    if (this.opfsAvailable !== undefined) return this.opfsAvailable;
    if (this.probePromise) return this.probePromise;
    this.probePromise = (async () => {
      if (typeof crypto === "undefined" || !crypto.subtle) {
        this.opfsAvailable = false;
        return false;
      }
      try {
        const root = await this.opfsRoot();
        if (!root) {
          this.opfsAvailable = false;
          return false;
        }
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
    if (!this._storageOwned || !this.useIdb() || this.opfsAvailable === false) return undefined;
    if (this.opfsAvailable === undefined) {
      const ok = await this.probe();
      if (!ok) return undefined;
    }
    try {
      const root = await this.opfsRoot();
      if (!root) {
        this.markOpfsDegraded();
        return undefined;
      }
      const attDir = await root.getDirectoryHandle(OPFS_ROOT_DIR, { create });
      return await attDir.getDirectoryHandle(OPFS_BLOBS_DIR, { create });
    } catch (err) {
      if (!create && isNotFound(err)) return undefined;
      if (hasQuotaExceeded(err)) throw new StorageQuotaError();
      this.markOpfsDegraded();
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
      const key = await this.ensureBlobKey();
      await writeEncryptedBlobToOpfs(dir, ref, b, key, this.ownerBinding());
      return ref;
    } catch (err) {
      if (hasQuotaExceeded(err)) throw new StorageQuotaError();
      // OPFS became unavailable at write time -> fall back cleanly to memory and notify Core (L7).
      this.markOpfsDegraded();
      this.memoryBlobs.set(ref, b);
      return ref;
    }
  }

  private async persistStagedRefs(refs: readonly string[]): Promise<void> {
    if (!refs.length) return;
    let dir: FileSystemDirectoryHandle | undefined;
    let key: CryptoKey | undefined;
    const owner = this.ownerBinding();
    for (const ref of refs) {
      const staged = this.stagedBlobs.get(ref);
      if (!staged) continue;
      if (!this._storageOwned || this.opfsAvailable === false) {
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
        if (!key) key = await this.ensureBlobKey();
        await writeEncryptedBlobToOpfs(dir, ref, staged, key, owner);
        this.stagedBlobs.delete(ref);
      } catch (err) {
        if (hasQuotaExceeded(err)) throw new StorageQuotaError();
        this.markOpfsDegraded();
        this.memoryBlobs.set(ref, staged);
        this.stagedBlobs.delete(ref);
      }
    }
  }

  peekMemoryBlob(ref: string): Blob | undefined {
    return this.stagedBlobs.get(ref) ?? this.memoryBlobs.get(ref);
  }

  private async openOpfsFile(ref: string): Promise<File | undefined> {
    const dir = await this.blobsDir(false);
    if (!dir) return undefined;
    try {
      return await (await dir.getFileHandle(ref)).getFile();
    } catch (err) {
      if (isNotFound(err)) return undefined;
      throw err;
    }
  }

  async getLocalBlob(ref: string): Promise<Blob | undefined> {
    const mem = this.peekMemoryBlob(ref);
    if (mem) return mem;
    const file = await this.openOpfsFile(ref);
    if (!file) return undefined;
    const key = await this.loadExistingBlobKey();
    if (!key) return undefined;
    try {
      return await decryptOpfsBlobFull(file, ref, key, this.ownerBinding());
    } catch {
      return undefined;
    }
  }

  async hashLocalBlob(ref: string, maxBytes: number): Promise<HashLocalBlobResult> {
    const mem = this.peekMemoryBlob(ref);
    if (mem) {
      if (maxBytes > 0 && mem.size > maxBytes) return { kind: "too_large", sizeBytes: mem.size };
      const sha256 = (await hashBlobStreaming(mem, mem.size)) as Uint8Array<ArrayBuffer>;
      return { kind: "ok", sizeBytes: mem.size, sha256 };
    }
    const file = await this.openOpfsFile(ref);
    if (!file) return { kind: "missing" };
    const key = await this.loadExistingBlobKey();
    if (!key) return { kind: "missing" };
    try {
      return await hashEncryptedOpfsBlob(file, ref, key, this.ownerBinding(), maxBytes);
    } catch {
      return { kind: "changed" };
    }
  }

  async readLocalSlice(ref: string, offset: number, length: number, expectedSize: number): Promise<ReadLocalSliceResult> {
    const mem = this.peekMemoryBlob(ref);
    if (mem) return readSliceFromBlob(mem, offset, length, expectedSize);
    const file = await this.openOpfsFile(ref);
    if (!file) return { kind: "missing" };
    const key = await this.loadExistingBlobKey();
    if (!key) return { kind: "missing" };
    try {
      return await readEncryptedOpfsSlice(file, ref, key, this.ownerBinding(), offset, length, expectedSize);
    } catch {
      return { kind: "changed" };
    }
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
    if (!this._storageOwned) {
      return { entries: [], localRefs: [] };
    }
    const entries: StoredDraftEntry[] = [];
    if (this.useIdb()) {
      await purgeLegacyComposerLocalData(this.getIDB()).catch(() => undefined);
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
    if (!this._storageOwned) return { found: false };
    if (this.useIdb()) {
      return readDraftRecord(this.account!.server, this.account!.deviceId, bucketToName(bucket), key, this.getIDB());
    }
    const data = this.fallbackRecords.get(`${bucket}:${key}`);
    return data ? { found: true, data: data.slice() } : { found: false };
  }

  async write(bucket: DraftStorageBucket, key: string, data: Uint8Array, referencedRefs: readonly string[] = []): Promise<void> {
    await this.persistStagedRefs(referencedRefs);
    if (!this._storageOwned) return;
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
    if (!this._storageOwned) return;
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
    this.blobKey = undefined;
    if (this.useIdb()) {
      await clearAllDraftRecords(this.getIDB()).catch(() => undefined);
    }
    const root = await this.opfsRoot().catch(() => undefined);
    if (root) {
      await removeQuiet(root, OPFS_ROOT_DIR, true);
    } else {
      await clearDefaultAttachmentOpfs();
    }
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
