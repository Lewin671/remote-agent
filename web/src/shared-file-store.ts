/** Storage backend for locally cached shared files (<= 50 MiB) and partial chunks.
 * Prefers Origin Private File System (OPFS) and falls back cleanly to in-memory storage
 * when OPFS or writable file streams are unavailable. */

export const SHARED_FILE_META_VERSION = 2;

export interface SharedFilePartialMeta {
  v: 2;
  sessionId: string;
  fileId: string;
  displayName: string;
  totalSizeBytes: string;
  chunkBytes: number;
  committedOffset: string;
  expectedSha256: string;
}

export interface ScannedSharedFileEntry {
  sessionId: string;
  fileId: string;
  completed: boolean;
  bytesOnDisk: bigint;
  expectedSizeBytes: bigint;
  chunkBytes: number;
  committedOffset: bigint;
  sha256: Uint8Array;
  hasDuration?: boolean;
  durationMs?: bigint;
}

export interface DiskInspectResult {
  known: boolean;
  availableBytes: bigint;
}

export interface WriteChunkParams {
  sessionId: string;
  fileId: string;
  displayName: string;
  offset: bigint;
  data: Uint8Array;
  totalSizeBytes: bigint;
  chunkBytes: number;
  committedOffset: bigint;
  expectedSha256: Uint8Array;
  resetPartial: boolean;
}

export interface SharedFileStore {
  scanLocal(): Promise<ScannedSharedFileEntry[]>;
  inspectDisk(): Promise<DiskInspectResult>;
  writeChunk(params: WriteChunkParams): Promise<void>;
  readPartialBlob(sessionId: string, fileId: string): Promise<Blob | undefined>;
  commitPartial(sessionId: string, fileId: string, totalSizeBytes: number): Promise<Blob>;
  getCompletedBlob(sessionId: string, fileId: string): Promise<Blob | undefined>;
  deletePartial(sessionId: string, fileId: string): Promise<void>;
  deleteLocal(sessionId: string, fileId: string): Promise<void>;
  dropSession(sessionId: string): Promise<void>;
  clearAll(): Promise<void>;
}

export class StorageQuotaError extends Error {
  constructor() {
    super();
    this.name = "StorageQuotaError";
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === "object" && !Array.isArray(v);
}

export function hasQuotaExceeded(err: unknown): boolean {
  if (err instanceof StorageQuotaError) return true;
  if (!isRecord(err)) return false;
  const e = err as { name?: string; code?: number };
  return e.name === "QuotaExceededError" || e.name === "NS_ERROR_DOM_QUOTA_REACHED" || e.code === 22 || e.code === 1014;
}

export function bytesToHex(bytes: Uint8Array): string {
  let out = "";
  for (const b of bytes) out += b.toString(16).padStart(2, "0");
  return out;
}

export function hexToBytes(hex: string): Uint8Array {
  if (hex.length % 2 !== 0) return new Uint8Array();
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return out;
}

export function safeSegment(id: string): string {
  return bytesToHex(new TextEncoder().encode(id)) || "00";
}

export function decodeSegment(seg: string): string | undefined {
  if (!seg || seg.length % 2 !== 0 || !/^[0-9a-f]+$/i.test(seg)) return undefined;
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(hexToBytes(seg)) || undefined;
  } catch {
    return undefined;
  }
}

/** Strictly parses v2 partial metadata; legacy or malformed JSON returns undefined. */
export function parsePartialMeta(raw: string): SharedFilePartialMeta | undefined {
  let parsed: unknown;
  try { parsed = JSON.parse(raw); } catch { return undefined; }
  if (!isRecord(parsed)) return undefined;
  if (
    parsed.v !== SHARED_FILE_META_VERSION ||
    typeof parsed.sessionId !== "string" || !parsed.sessionId ||
    typeof parsed.fileId !== "string" || !parsed.fileId ||
    typeof parsed.displayName !== "string" ||
    typeof parsed.totalSizeBytes !== "string" || !/^\d+$/.test(parsed.totalSizeBytes) ||
    typeof parsed.chunkBytes !== "number" || !Number.isInteger(parsed.chunkBytes) || parsed.chunkBytes <= 0 ||
    typeof parsed.committedOffset !== "string" || !/^\d+$/.test(parsed.committedOffset) ||
    typeof parsed.expectedSha256 !== "string" || !/^[0-9a-f]{64}$/.test(parsed.expectedSha256)
  ) return undefined;
  return {
    v: 2,
    sessionId: parsed.sessionId,
    fileId: parsed.fileId,
    displayName: parsed.displayName,
    totalSizeBytes: parsed.totalSizeBytes,
    chunkBytes: parsed.chunkBytes,
    committedOffset: parsed.committedOffset,
    expectedSha256: parsed.expectedSha256,
  };
}

function makeCompletedEntry(sessionId: string, fileId: string, bytesOnDisk: bigint): ScannedSharedFileEntry {
  return { sessionId, fileId, completed: true, bytesOnDisk, expectedSizeBytes: 0n, chunkBytes: 0, committedOffset: 0n, sha256: new Uint8Array() };
}

function makePartialEntry(sessionId: string, fileId: string, bytesOnDisk: bigint, rawMeta?: string): ScannedSharedFileEntry {
  const meta = rawMeta !== undefined ? parsePartialMeta(rawMeta) : undefined;
  if (!meta || meta.sessionId !== sessionId || meta.fileId !== fileId) {
    return { sessionId, fileId, completed: false, bytesOnDisk, expectedSizeBytes: 0n, chunkBytes: 0, committedOffset: 0n, sha256: new Uint8Array() };
  }
  return {
    sessionId,
    fileId,
    completed: false,
    bytesOnDisk,
    expectedSizeBytes: BigInt(meta.totalSizeBytes),
    chunkBytes: meta.chunkBytes,
    committedOffset: BigInt(meta.committedOffset),
    sha256: hexToBytes(meta.expectedSha256),
  };
}

function serializePartialMeta(p: WriteChunkParams): string {
  const meta: SharedFilePartialMeta = {
    v: SHARED_FILE_META_VERSION,
    sessionId: p.sessionId,
    fileId: p.fileId,
    displayName: p.displayName,
    totalSizeBytes: p.totalSizeBytes.toString(),
    chunkBytes: p.chunkBytes,
    committedOffset: p.committedOffset.toString(),
    expectedSha256: bytesToHex(p.expectedSha256),
  };
  return JSON.stringify(meta);
}

function rethrowQuota(err: unknown): never {
  if (hasQuotaExceeded(err)) throw new StorageQuotaError();
  throw err;
}

function isNotFound(err: unknown): boolean {
  return isRecord(err) && err.name === "NotFoundError";
}

function chunksToBlobParts(chunks: Map<number, Uint8Array>, totalBytes: number): BlobPart[] {
  if (totalBytes <= 0) return [];
  const parts: BlobPart[] = [];
  let cursor = 0;
  for (const [offset, data] of [...chunks.entries()].sort((a, b) => a[0] - b[0])) {
    if (offset >= totalBytes) break;
    if (offset > cursor) parts.push(new Uint8Array(offset - cursor));
    const startInChunk = Math.max(0, cursor - offset);
    const endInChunk = Math.min(data.length, totalBytes - offset);
    if (endInChunk > startInChunk) {
      parts.push(data.subarray(startInChunk, endInChunk) as Uint8Array<ArrayBuffer>);
      cursor = offset + endInChunk;
    }
  }
  if (cursor < totalBytes) parts.push(new Uint8Array(totalBytes - cursor));
  return parts;
}

/** Pure in-memory store used as OPFS fallback (and directly in demo/tests). */
export class MemorySharedFileStore implements SharedFileStore {
  private readonly partials = new Map<string, { sessionId: string; fileId: string; rawMeta: string; chunks: Map<number, Uint8Array>; length: number }>();
  private readonly completed = new Map<string, { sessionId: string; fileId: string; blob: Blob }>();
  private readonly maxBytes?: number;
  private usedBytes = 0;

  constructor(opts?: { maxBytes?: number }) {
    this.maxBytes = opts?.maxBytes;
  }

  private key(sessionId: string, fileId: string): string {
    return `${safeSegment(sessionId)}:${safeSegment(fileId)}`;
  }

  private ensureSpace(delta: number) {
    if (this.maxBytes !== undefined && delta > 0 && this.usedBytes + delta > this.maxBytes) {
      throw new StorageQuotaError();
    }
  }

  async scanLocal(): Promise<ScannedSharedFileEntry[]> {
    const out: ScannedSharedFileEntry[] = [];
    const seen = new Set<string>();
    for (const [k, rec] of this.completed.entries()) {
      seen.add(k);
      out.push(makeCompletedEntry(rec.sessionId, rec.fileId, BigInt(rec.blob.size)));
    }
    for (const [k, rec] of this.partials.entries()) {
      if (!seen.has(k)) out.push(makePartialEntry(rec.sessionId, rec.fileId, BigInt(rec.length), rec.rawMeta));
    }
    return out;
  }

  async inspectDisk(): Promise<DiskInspectResult> {
    if (this.maxBytes === undefined) return { known: false, availableBytes: 0n };
    return { known: true, availableBytes: BigInt(Math.max(0, this.maxBytes - this.usedBytes)) };
  }

  async writeChunk(params: WriteChunkParams): Promise<void> {
    const k = this.key(params.sessionId, params.fileId);
    const off = Number(params.offset);
    const newLen = off + params.data.byteLength;
    const prev = this.partials.get(k);
    const nextChunks = new Map<number, Uint8Array>();
    if (!params.resetPartial && prev) {
      for (const [cOff, cData] of prev.chunks.entries()) {
        if (cOff >= newLen || cOff === off) continue;
        const end = Math.min(cOff + cData.byteLength, newLen);
        if (end > cOff) nextChunks.set(cOff, end < cOff + cData.byteLength ? cData.slice(0, end - cOff) : cData);
      }
    }
    if (params.data.byteLength > 0) nextChunks.set(off, params.data.slice());
    const delta = newLen - (prev?.length ?? 0);
    this.ensureSpace(delta);
    this.partials.set(k, {
      sessionId: params.sessionId,
      fileId: params.fileId,
      rawMeta: serializePartialMeta(params),
      chunks: nextChunks,
      length: newLen,
    });
    this.usedBytes = Math.max(0, this.usedBytes + delta);
  }

  async readPartialBlob(sessionId: string, fileId: string): Promise<Blob | undefined> {
    const rec = this.partials.get(this.key(sessionId, fileId));
    if (!rec) return undefined;
    return new Blob(chunksToBlobParts(rec.chunks, rec.length), { type: "application/octet-stream" });
  }

  async commitPartial(sessionId: string, fileId: string, totalSizeBytes: number): Promise<Blob> {
    const k = this.key(sessionId, fileId);
    const prevPartial = this.partials.get(k);
    const prevDone = this.completed.get(k);
    const delta = totalSizeBytes - (prevPartial?.length ?? 0) - (prevDone?.blob.size ?? 0);
    this.ensureSpace(delta);
    const parts = totalSizeBytes === 0 ? [] : chunksToBlobParts(prevPartial?.chunks ?? new Map(), totalSizeBytes);
    const blob = new Blob(parts, { type: "application/octet-stream" });
    if (prevPartial) {
      this.usedBytes = Math.max(0, this.usedBytes - prevPartial.length);
      this.partials.delete(k);
    }
    if (prevDone) this.usedBytes = Math.max(0, this.usedBytes - prevDone.blob.size);
    this.completed.set(k, { sessionId, fileId, blob });
    this.usedBytes += blob.size;
    return blob;
  }

  async getCompletedBlob(sessionId: string, fileId: string): Promise<Blob | undefined> {
    return this.completed.get(this.key(sessionId, fileId))?.blob;
  }

  async deletePartial(sessionId: string, fileId: string): Promise<void> {
    const k = this.key(sessionId, fileId);
    const rec = this.partials.get(k);
    if (rec) {
      this.usedBytes = Math.max(0, this.usedBytes - rec.length);
      this.partials.delete(k);
    }
  }

  async deleteLocal(sessionId: string, fileId: string): Promise<void> {
    await this.deletePartial(sessionId, fileId);
    const k = this.key(sessionId, fileId);
    const rec = this.completed.get(k);
    if (rec) {
      this.usedBytes = Math.max(0, this.usedBytes - rec.blob.size);
      this.completed.delete(k);
    }
  }

  async dropSession(sessionId: string): Promise<void> {
    const prefix = safeSegment(sessionId) + ":";
    for (const [k, rec] of [...this.partials.entries()]) {
      if (k.startsWith(prefix)) {
        this.usedBytes = Math.max(0, this.usedBytes - rec.length);
        this.partials.delete(k);
      }
    }
    for (const [k, rec] of [...this.completed.entries()]) {
      if (k.startsWith(prefix)) {
        this.usedBytes = Math.max(0, this.usedBytes - rec.blob.size);
        this.completed.delete(k);
      }
    }
  }

  async clearAll(): Promise<void> {
    this.partials.clear();
    this.completed.clear();
    this.usedBytes = 0;
  }

  seedCompleted(sessionId: string, fileId: string, blob: Blob) {
    const k = this.key(sessionId, fileId);
    const prev = this.completed.get(k);
    if (prev) this.usedBytes = Math.max(0, this.usedBytes - prev.blob.size);
    this.completed.set(k, { sessionId, fileId, blob });
    this.usedBytes += blob.size;
  }

  seedPartialRaw(sessionId: string, fileId: string, rawMeta: string, bytes: Uint8Array) {
    const k = this.key(sessionId, fileId);
    const prev = this.partials.get(k);
    if (prev) this.usedBytes = Math.max(0, this.usedBytes - prev.length);
    const chunks = new Map<number, Uint8Array>();
    if (bytes.byteLength > 0) chunks.set(0, bytes.slice());
    this.partials.set(k, { sessionId, fileId, rawMeta, chunks, length: bytes.byteLength });
    this.usedBytes += bytes.byteLength;
  }
}

const ROOT_DIR = "remote-agent-shared-files";

type IterableDirectoryHandle = FileSystemDirectoryHandle & {
  entries(): AsyncIterableIterator<[string, FileSystemHandle]>;
};

async function removeEntryQuiet(dir: FileSystemDirectoryHandle, name: string, recursive = false) {
  try { await dir.removeEntry(name, { recursive }); } catch { /* absent */ }
}

async function writeOpfs(
  fh: FileSystemFileHandle,
  keepExistingData: boolean,
  fn: (w: FileSystemWritableFileStream) => Promise<void>,
): Promise<void> {
  const w = await fh.createWritable({ keepExistingData });
  try {
    await fn(w);
    await w.close();
  } catch (e) {
    try { await w.abort(); } catch { /* ignore */ }
    throw e;
  }
}

/** Uses OPFS when available and falls back cleanly to memory storage if unsupported. */
export class OpfsSharedFileStore implements SharedFileStore {
  private readonly fallback = new MemorySharedFileStore();
  private opfsDisabled = false;

  private async opfsRoot(create: boolean): Promise<FileSystemDirectoryHandle | undefined> {
    if (this.opfsDisabled) return undefined;
    if (typeof navigator === "undefined" || typeof navigator.storage?.getDirectory !== "function") {
      this.opfsDisabled = true;
      return undefined;
    }
    try {
      const root = await navigator.storage.getDirectory();
      return await root.getDirectoryHandle(ROOT_DIR, { create });
    } catch (e) {
      if (!create && isNotFound(e)) return undefined;
      if (hasQuotaExceeded(e)) throw new StorageQuotaError();
      this.opfsDisabled = true;
      return undefined;
    }
  }

  private async sessionDir(sessionId: string, create: boolean): Promise<FileSystemDirectoryHandle | undefined> {
    const root = await this.opfsRoot(create);
    if (!root) return undefined;
    try {
      return await root.getDirectoryHandle(safeSegment(sessionId), { create });
    } catch (e) {
      if (!create && isNotFound(e)) return undefined;
      if (hasQuotaExceeded(e)) throw new StorageQuotaError();
      return undefined;
    }
  }

  private names(fileId: string) {
    const seg = safeSegment(fileId);
    return { part: `${seg}.part`, meta: `${seg}.meta.json`, bin: `${seg}.bin` };
  }

  async scanLocal(): Promise<ScannedSharedFileEntry[]> {
    const entries: ScannedSharedFileEntry[] = [];
    const seen = new Set<string>();
    const root = await this.opfsRoot(false);
    if (root && typeof (root as IterableDirectoryHandle).entries === "function") {
      for await (const [dirName, handle] of (root as IterableDirectoryHandle).entries()) {
        if (handle.kind !== "directory") continue;
        const sessionId = decodeSegment(dirName);
        if (!sessionId) continue;
        const bySeg = new Map<string, { bin?: FileSystemFileHandle; part?: FileSystemFileHandle; meta?: FileSystemFileHandle }>();
        try {
          for await (const [name, fHandle] of (handle as IterableDirectoryHandle).entries()) {
            if (fHandle.kind !== "file") continue;
            const fh = fHandle as FileSystemFileHandle;
            const slot = (seg: string) => {
              let s = bySeg.get(seg);
              if (!s) { s = {}; bySeg.set(seg, s); }
              return s;
            };
            if (name.endsWith(".bin")) slot(name.slice(0, -4)).bin = fh;
            else if (name.endsWith(".part")) slot(name.slice(0, -5)).part = fh;
            else if (name.endsWith(".meta.json")) slot(name.slice(0, -10)).meta = fh;
          }
        } catch { continue; }

        for (const [fileSeg, files] of bySeg.entries()) {
          const fileId = decodeSegment(fileSeg);
          if (!fileId) continue;
          seen.add(`${sessionId}\0${fileId}`);
          if (files.bin) {
            try {
              entries.push(makeCompletedEntry(sessionId, fileId, BigInt((await files.bin.getFile()).size)));
              continue;
            } catch { /* fall through */ }
          }
          const bytesOnDisk = files.part ? BigInt(await files.part.getFile().then((f) => f.size, () => 0)) : 0n;
          const rawMeta = files.meta ? await files.meta.getFile().then((f) => f.text(), () => undefined) : undefined;
          entries.push(makePartialEntry(sessionId, fileId, bytesOnDisk, rawMeta));
        }
      }
    }
    for (const fb of await this.fallback.scanLocal()) {
      if (!seen.has(`${fb.sessionId}\0${fb.fileId}`)) entries.push(fb);
    }
    return entries;
  }

  async inspectDisk(): Promise<DiskInspectResult> {
    if (this.opfsDisabled) return this.fallback.inspectDisk();
    if (typeof navigator !== "undefined" && typeof navigator.storage?.estimate === "function") {
      try {
        const { quota, usage } = await navigator.storage.estimate();
        if (typeof quota === "number" && Number.isFinite(quota) && typeof usage === "number" && Number.isFinite(usage)) {
          return { known: true, availableBytes: BigInt(Math.max(0, Math.floor(quota - usage))) };
        }
      } catch { /* fall through to unknown */ }
    }
    return { known: false, availableBytes: 0n };
  }

  async writeChunk(params: WriteChunkParams): Promise<void> {
    const dir = await this.sessionDir(params.sessionId, true);
    if (!dir) return this.fallback.writeChunk(params);
    const { part, meta } = this.names(params.fileId);
    const off = Number(params.offset);
    try {
      const partHandle = await dir.getFileHandle(part, { create: true });
      if (typeof partHandle.createWritable !== "function") {
        this.opfsDisabled = true;
        return this.fallback.writeChunk(params);
      }
      await writeOpfs(partHandle, !params.resetPartial, async (w) => {
        if (params.data.byteLength > 0) await w.write({ type: "write", position: off, data: params.data.slice() });
        await w.truncate(off + params.data.byteLength);
      });
      const metaHandle = await dir.getFileHandle(meta, { create: true });
      await writeOpfs(metaHandle, false, (w) => w.write(serializePartialMeta(params)));
    } catch (e) {
      rethrowQuota(e);
    }
  }

  async readPartialBlob(sessionId: string, fileId: string): Promise<Blob | undefined> {
    const dir = await this.sessionDir(sessionId, false);
    if (!dir) return this.fallback.readPartialBlob(sessionId, fileId);
    try { return await (await dir.getFileHandle(this.names(fileId).part)).getFile(); }
    catch (e) {
      if (isNotFound(e)) return this.fallback.readPartialBlob(sessionId, fileId);
      rethrowQuota(e);
    }
  }

  async commitPartial(sessionId: string, fileId: string, totalSizeBytes: number): Promise<Blob> {
    const dir = await this.sessionDir(sessionId, true);
    if (!dir) return this.fallback.commitPartial(sessionId, fileId, totalSizeBytes);
    const { part, meta, bin } = this.names(fileId);
    try {
      if (totalSizeBytes === 0) {
        const binHandle = await dir.getFileHandle(bin, { create: true });
        if (typeof binHandle.createWritable !== "function") {
          this.opfsDisabled = true;
          return this.fallback.commitPartial(sessionId, fileId, totalSizeBytes);
        }
        await writeOpfs(binHandle, false, async () => undefined);
        await removeEntryQuiet(dir, part);
        await removeEntryQuiet(dir, meta);
        return await binHandle.getFile();
      }

      let partHandle: FileSystemFileHandle;
      try {
        partHandle = await dir.getFileHandle(part);
      } catch (e) {
        if (isNotFound(e)) return this.fallback.commitPartial(sessionId, fileId, totalSizeBytes);
        throw e;
      }

      const partFile = await partHandle.getFile();
      if (partFile.size > totalSizeBytes && typeof partHandle.createWritable === "function") {
        await writeOpfs(partHandle, true, async (w) => {
          await w.truncate(totalSizeBytes);
        });
      }

      // Prefer zero-copy OPFS atomic rename (.part -> .bin) when supported.
      const movable = partHandle as FileSystemFileHandle & { move?: (newName: string) => Promise<void> };
      if (typeof movable.move === "function") {
        await removeEntryQuiet(dir, bin);
        await movable.move(bin);
        await removeEntryQuiet(dir, meta);
        return await (await dir.getFileHandle(bin)).getFile();
      }

      // Fallback when OPFS move() is unavailable: stream-copy in 1 MiB slices without loading whole file into RAM.
      const binHandle = await dir.getFileHandle(bin, { create: true });
      if (typeof binHandle.createWritable !== "function") {
        this.opfsDisabled = true;
        return this.fallback.commitPartial(sessionId, fileId, totalSizeBytes);
      }
      const srcFile = await partHandle.getFile();
      const COPY_SLICE = 1024 * 1024;
      await writeOpfs(binHandle, false, async (w) => {
        for (let off = 0; off < totalSizeBytes; off += COPY_SLICE) {
          const end = Math.min(totalSizeBytes, off + COPY_SLICE);
          const buf = await srcFile.slice(off, end).arrayBuffer();
          if (buf.byteLength > 0) await w.write(new Uint8Array(buf));
        }
      });
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
    try { return await (await dir.getFileHandle(this.names(fileId).bin)).getFile(); }
    catch { return this.fallback.getCompletedBlob(sessionId, fileId); }
  }

  async deletePartial(sessionId: string, fileId: string): Promise<void> {
    await this.fallback.deletePartial(sessionId, fileId);
    const dir = await this.sessionDir(sessionId, false);
    if (!dir) return;
    const { part, meta } = this.names(fileId);
    await removeEntryQuiet(dir, part);
    await removeEntryQuiet(dir, meta);
  }

  async deleteLocal(sessionId: string, fileId: string): Promise<void> {
    await this.fallback.deleteLocal(sessionId, fileId);
    const dir = await this.sessionDir(sessionId, false);
    if (!dir) return;
    const { bin, part, meta } = this.names(fileId);
    await removeEntryQuiet(dir, bin);
    await removeEntryQuiet(dir, part);
    await removeEntryQuiet(dir, meta);
  }

  async dropSession(sessionId: string): Promise<void> {
    await this.fallback.dropSession(sessionId);
    const root = await this.opfsRoot(false);
    if (root) await removeEntryQuiet(root, safeSegment(sessionId), true);
  }

  async clearAll(): Promise<void> {
    await this.fallback.clearAll();
    if (typeof navigator === "undefined" || typeof navigator.storage?.getDirectory !== "function") return;
    try { await removeEntryQuiet(await navigator.storage.getDirectory(), ROOT_DIR, true); } catch { /* ignore */ }
  }
}

export const sharedFileStore = new OpfsSharedFileStore();
