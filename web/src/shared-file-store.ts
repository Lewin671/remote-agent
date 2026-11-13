/** Storage backend for locally cached shared files (<= 50 MB) and partial chunks.
 * Prefers Origin Private File System (OPFS) and falls back to an in-memory map
 * when OPFS or writable file streams are unavailable. */

export interface PartialMeta {
  nextOffset: number;
  sizeBytes: string;
  chunkBytes: number;
  sha256Hex: string;
}

export interface SharedFileStore {
  getPartialMeta(sessionId: string, fileId: string): Promise<PartialMeta | undefined>;
  writePartialChunk(
    sessionId: string,
    fileId: string,
    offset: number,
    bytes: Uint8Array,
    meta: PartialMeta,
  ): Promise<void>;
  readPartialBlob(sessionId: string, fileId: string): Promise<Blob | undefined>;
  commitCompleted(sessionId: string, fileId: string, totalBytes: number): Promise<Blob>;
  getCompletedBlob(sessionId: string, fileId: string): Promise<Blob | undefined>;
  deletePartial(sessionId: string, fileId: string): Promise<void>;
  deleteCompleted(sessionId: string, fileId: string): Promise<void>;
  dropSession(sessionId: string): Promise<void>;
  clearAll(): Promise<void>;
}

export class StorageQuotaError extends Error {
  constructor() {
    super();
    this.name = "StorageQuotaError";
  }
}

export function hasQuotaExceeded(err: unknown): boolean {
  if (err instanceof StorageQuotaError) return true;
  if (!err || typeof err !== "object") return false;
  const e = err as { name?: string; code?: number };
  return (
    e.name === "QuotaExceededError" ||
    e.name === "NS_ERROR_DOM_QUOTA_REACHED" ||
    e.code === 22 ||
    e.code === 1014
  );
}

/** Encodes arbitrary session/file IDs into path-safe single directory/file names. */
function safeSegment(id: string): string {
  const bytes = new TextEncoder().encode(id);
  let out = "";
  for (const b of bytes) out += b.toString(16).padStart(2, "0");
  return out || "00";
}

function rethrowQuota(err: unknown): never {
  if (hasQuotaExceeded(err)) throw new StorageQuotaError();
  throw err;
}

function concatChunks(chunks: Map<number, Uint8Array>, totalBytes: number): Uint8Array<ArrayBuffer> {
  const buf = new ArrayBuffer(totalBytes);
  const out = new Uint8Array(buf);
  const entries = [...chunks.entries()].sort((a, b) => a[0] - b[0]);
  for (const [offset, data] of entries) {
    if (offset >= totalBytes) continue;
    const slice = data.subarray(0, Math.min(data.length, totalBytes - offset));
    out.set(slice, offset);
  }
  return out;
}

/** Pure in-memory store used as fallback (and directly in unit tests). */
export class MemorySharedFileStore implements SharedFileStore {
  private readonly partials = new Map<string, { meta: PartialMeta; chunks: Map<number, Uint8Array> }>();
  private readonly completed = new Map<string, Blob>();
  private readonly maxBytes?: number;
  private usedBytes = 0;

  constructor(opts?: { maxBytes?: number }) {
    this.maxBytes = opts?.maxBytes;
  }

  private key(sessionId: string, fileId: string): string {
    return `${safeSegment(sessionId)}:${safeSegment(fileId)}`;
  }

  private ensureSpace(delta: number) {
    if (this.maxBytes !== undefined && this.usedBytes + delta > this.maxBytes) {
      throw new StorageQuotaError();
    }
  }

  async getPartialMeta(sessionId: string, fileId: string): Promise<PartialMeta | undefined> {
    return this.partials.get(this.key(sessionId, fileId))?.meta;
  }

  async writePartialChunk(
    sessionId: string,
    fileId: string,
    offset: number,
    bytes: Uint8Array,
    meta: PartialMeta,
  ): Promise<void> {
    const k = this.key(sessionId, fileId);
    let entry = this.partials.get(k);
    if (!entry) {
      entry = { meta, chunks: new Map() };
      this.partials.set(k, entry);
    }
    const prev = entry.chunks.get(offset)?.byteLength ?? 0;
    const delta = bytes.byteLength - prev;
    if (delta > 0) this.ensureSpace(delta);
    entry.chunks.set(offset, bytes.slice());
    entry.meta = meta;
    this.usedBytes += delta;
  }

  async readPartialBlob(sessionId: string, fileId: string): Promise<Blob | undefined> {
    const entry = this.partials.get(this.key(sessionId, fileId));
    if (!entry) return undefined;
    const total = Number(entry.meta.sizeBytes);
    const len = Math.min(entry.meta.nextOffset, total);
    const bytes = concatChunks(entry.chunks, len);
    return new Blob([bytes], { type: "application/octet-stream" });
  }

  async commitCompleted(sessionId: string, fileId: string, totalBytes: number): Promise<Blob> {
    const k = this.key(sessionId, fileId);
    const entry = this.partials.get(k);
    if (!entry) return new Blob([], { type: "application/octet-stream" });
    const bytes = concatChunks(entry.chunks, totalBytes);
    const blob = new Blob([bytes], { type: "application/octet-stream" });
    this.partials.delete(k);
    this.completed.set(k, blob);
    return blob;
  }

  async getCompletedBlob(sessionId: string, fileId: string): Promise<Blob | undefined> {
    return this.completed.get(this.key(sessionId, fileId));
  }

  async deletePartial(sessionId: string, fileId: string): Promise<void> {
    const k = this.key(sessionId, fileId);
    const entry = this.partials.get(k);
    if (entry) {
      for (const c of entry.chunks.values()) this.usedBytes -= c.byteLength;
      this.partials.delete(k);
    }
  }

  async deleteCompleted(sessionId: string, fileId: string): Promise<void> {
    const k = this.key(sessionId, fileId);
    const b = this.completed.get(k);
    if (b) {
      this.usedBytes -= b.size;
      this.completed.delete(k);
    }
  }

  async dropSession(sessionId: string): Promise<void> {
    const prefix = safeSegment(sessionId) + ":";
    for (const [k, entry] of [...this.partials.entries()]) {
      if (k.startsWith(prefix)) {
        for (const c of entry.chunks.values()) this.usedBytes -= c.byteLength;
        this.partials.delete(k);
      }
    }
    for (const [k, b] of [...this.completed.entries()]) {
      if (k.startsWith(prefix)) {
        this.usedBytes -= b.size;
        this.completed.delete(k);
      }
    }
  }

  async clearAll(): Promise<void> {
    this.partials.clear();
    this.completed.clear();
    this.usedBytes = 0;
  }

  /** Test helper to pre-populate a completed blob. */
  seedCompleted(sessionId: string, fileId: string, blob: Blob) {
    this.completed.set(this.key(sessionId, fileId), blob);
    this.usedBytes += blob.size;
  }
}

const ROOT_DIR = "remote-agent-shared-files";

async function opfsRoot(): Promise<FileSystemDirectoryHandle | undefined> {
  if (typeof navigator === "undefined" || !navigator.storage?.getDirectory) return undefined;
  try {
    const root = await navigator.storage.getDirectory();
    return await root.getDirectoryHandle(ROOT_DIR, { create: true });
  } catch {
    return undefined;
  }
}

async function removeEntryQuiet(dir: FileSystemDirectoryHandle, name: string, recursive = false) {
  try {
    await dir.removeEntry(name, { recursive });
  } catch {
    // Entry already absent or directory inaccessible.
  }
}

/** Uses OPFS when available and falls back cleanly to memory storage if unsupported. */
export class OpfsSharedFileStore implements SharedFileStore {
  private readonly fallback = new MemorySharedFileStore();
  private opfsDisabled = false;

  private async sessionDir(sessionId: string, create: boolean): Promise<FileSystemDirectoryHandle | undefined> {
    if (this.opfsDisabled) return undefined;
    const root = await opfsRoot();
    if (!root) {
      this.opfsDisabled = true;
      return undefined;
    }
    try {
      return await root.getDirectoryHandle(safeSegment(sessionId), { create });
    } catch {
      return undefined;
    }
  }

  private partNames(fileId: string) {
    const seg = safeSegment(fileId);
    return { part: `${seg}.part`, meta: `${seg}.meta.json`, bin: `${seg}.bin` };
  }

  async getPartialMeta(sessionId: string, fileId: string): Promise<PartialMeta | undefined> {
    const dir = await this.sessionDir(sessionId, false);
    if (!dir) return this.fallback.getPartialMeta(sessionId, fileId);
    try {
      const fh = await dir.getFileHandle(this.partNames(fileId).meta);
      const file = await fh.getFile();
      return JSON.parse(await file.text()) as PartialMeta;
    } catch {
      return this.fallback.getPartialMeta(sessionId, fileId);
    }
  }

  async writePartialChunk(
    sessionId: string,
    fileId: string,
    offset: number,
    bytes: Uint8Array,
    meta: PartialMeta,
  ): Promise<void> {
    const dir = await this.sessionDir(sessionId, true);
    if (!dir) return this.fallback.writePartialChunk(sessionId, fileId, offset, bytes, meta);
    const { part, meta: metaName } = this.partNames(fileId);
    try {
      const partHandle = await dir.getFileHandle(part, { create: true });
      if (typeof partHandle.createWritable !== "function") {
        this.opfsDisabled = true;
        return this.fallback.writePartialChunk(sessionId, fileId, offset, bytes, meta);
      }
      const writable = await partHandle.createWritable({ keepExistingData: offset > 0 });
      try {
        if (bytes.byteLength > 0) {
          await writable.write({ type: "write", position: offset, data: bytes.slice() });
        }
        await writable.close();
      } catch (e) {
        try {
          await writable.abort();
        } catch {
          // ignore
        }
        throw e;
      }
      const metaHandle = await dir.getFileHandle(metaName, { create: true });
      const metaWritable = await metaHandle.createWritable();
      try {
        await metaWritable.write(JSON.stringify(meta));
        await metaWritable.close();
      } catch (e) {
        try {
          await metaWritable.abort();
        } catch {
          // ignore
        }
        throw e;
      }
    } catch (e) {
      rethrowQuota(e);
    }
  }

  async readPartialBlob(sessionId: string, fileId: string): Promise<Blob | undefined> {
    const dir = await this.sessionDir(sessionId, false);
    if (!dir) return this.fallback.readPartialBlob(sessionId, fileId);
    try {
      const fh = await dir.getFileHandle(this.partNames(fileId).part);
      return await fh.getFile();
    } catch {
      return this.fallback.readPartialBlob(sessionId, fileId);
    }
  }

  async commitCompleted(sessionId: string, fileId: string, totalBytes: number): Promise<Blob> {
    const dir = await this.sessionDir(sessionId, true);
    if (!dir) return this.fallback.commitCompleted(sessionId, fileId, totalBytes);
    const { part, meta, bin } = this.partNames(fileId);
    try {
      const partHandle = await dir.getFileHandle(part);
      const partFile = await partHandle.getFile();
      const buf = await partFile.arrayBuffer();
      const exact = new Uint8Array(buf, 0, Math.min(totalBytes, buf.byteLength));
      const binHandle = await dir.getFileHandle(bin, { create: true });
      const writable = await binHandle.createWritable();
      try {
        if (exact.byteLength > 0) {
          await writable.write(exact.slice());
        }
        await writable.close();
      } catch (e) {
        try {
          await writable.abort();
        } catch {
          // ignore
        }
        throw e;
      }
      await removeEntryQuiet(dir, part);
      await removeEntryQuiet(dir, meta);
      return await binHandle.getFile();
    } catch (e) {
      rethrowQuota(e);
    }
  }

  async getCompletedBlob(sessionId: string, fileId: string): Promise<Blob | undefined> {
    const dir = await this.sessionDir(sessionId, false);
    if (!dir) return this.fallback.getCompletedBlob(sessionId, fileId);
    try {
      const fh = await dir.getFileHandle(this.partNames(fileId).bin);
      return await fh.getFile();
    } catch {
      return this.fallback.getCompletedBlob(sessionId, fileId);
    }
  }

  async deletePartial(sessionId: string, fileId: string): Promise<void> {
    await this.fallback.deletePartial(sessionId, fileId);
    const dir = await this.sessionDir(sessionId, false);
    if (!dir) return;
    const { part, meta } = this.partNames(fileId);
    await removeEntryQuiet(dir, part);
    await removeEntryQuiet(dir, meta);
  }

  async deleteCompleted(sessionId: string, fileId: string): Promise<void> {
    await this.fallback.deleteCompleted(sessionId, fileId);
    const dir = await this.sessionDir(sessionId, false);
    if (!dir) return;
    await removeEntryQuiet(dir, this.partNames(fileId).bin);
  }

  async dropSession(sessionId: string): Promise<void> {
    await this.fallback.dropSession(sessionId);
    const root = await opfsRoot();
    if (root) await removeEntryQuiet(root, safeSegment(sessionId), true);
  }

  async clearAll(): Promise<void> {
    await this.fallback.clearAll();
    if (typeof navigator === "undefined" || !navigator.storage?.getDirectory) return;
    try {
      const root = await navigator.storage.getDirectory();
      await removeEntryQuiet(root, ROOT_DIR, true);
    } catch {
      // ignore
    }
  }
}

export const sharedFileStore = new OpfsSharedFileStore();
