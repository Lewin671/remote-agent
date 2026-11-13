import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import vectors from "../../testdata/shared-files/blocks.json";
import { SharedFileSchema, type SharedFile } from "./gen/remoteagent/v1/event_pb";
import {
  classifySharedFile,
  failureNotice,
  formatEta,
  formatMediaDuration,
  LOCAL_CACHE_MAX_BYTES,
  previewBlobForFile,
  sanitizeFileName,
  SharedFileManager,
  splitFileName,
} from "./shared-files";
import { MemorySharedFileStore } from "./shared-file-store";
import { CommandError } from "./remote";

function hexToBytes(hex?: string): Uint8Array {
  if (!hex) return new Uint8Array();
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

async function computeSha256(sizeBytes: number): Promise<Uint8Array> {
  const bytes = new Uint8Array(sizeBytes);
  for (let i = 0; i < sizeBytes; i++) bytes[i] = deterministicFileByte(i);
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return new Uint8Array(digest);
}

function deterministicFileByte(index: number): number {
  return index % 251;
}

function makeVectorBlock(sizeBytes: number, offset: number, blockBytes: number): Uint8Array {
  const block = new Uint8Array(blockBytes);
  const end = Math.min(sizeBytes, offset + blockBytes);
  for (let i = offset; i < end; i++) {
    block[i - offset] = deterministicFileByte(i);
  }
  return block;
}

function makeFile(overrides: Partial<{ fileId: string; name: string; sizeBytes: bigint; sha256: Uint8Array; path: string }>): SharedFile {
  return create(SharedFileSchema, {
    fileId: overrides.fileId ?? "f-1",
    name: overrides.name ?? "demo.bin",
    sizeBytes: overrides.sizeBytes ?? 0n,
    sha256: overrides.sha256 ?? new Uint8Array(),
    path: overrides.path ?? "/Users/alice/demo.bin",
  });
}

describe("shared-files security & helpers", () => {
  it("sanitizes path separators, control chars and bidi spoofing controls", () => {
    expect(sanitizeFileName("../../etc/passwd")).toBe("....etcpasswd");
    expect(sanitizeFileName("a\\b\u0000\u001f\u007fc.txt")).toBe("abc.txt");
    expect(sanitizeFileName("report\u202Egpj.exe")).toBe("reportgpj.exe");
    expect(sanitizeFileName("\u2066\u200E..\u2069")).toBe("file");
    expect(sanitizeFileName("   ")).toBe("file");
  });

  it("enforces strict preview extension whitelists and assigns client MIME types", () => {
    const raw = new Blob(["<svg onload=alert(1)>"], { type: "image/svg+xml" });
    expect(previewBlobForFile(raw, "evil.svg")).toBeUndefined();
    expect(previewBlobForFile(raw, "page.html")).toBeUndefined();
    expect(previewBlobForFile(raw, "doc.pdf")).toBeUndefined();
    expect(previewBlobForFile(raw, "notes.txt")).toBeUndefined();

    const img = previewBlobForFile(raw, "PHOTO.JPG");
    expect(img).toEqual({ kind: "image", blob: expect.any(Blob) });
    expect(img?.blob.type).toBe("image/jpeg");

    const webp = previewBlobForFile(raw, "pic.webp");
    expect(webp?.kind).toBe("image");
    expect(webp?.blob.type).toBe("image/webp");

    const vid = previewBlobForFile(raw, "clip.mov");
    expect(vid?.kind).toBe("video");
    expect(vid?.blob.type).toBe("video/quicktime");

    const aud = previewBlobForFile(raw, "voice.m4a");
    expect(aud?.kind).toBe("audio");
    expect(aud?.blob.type).toBe("audio/mp4");
  });

  it("classifies file kinds, splits filenames preserving extension, and formats durations/ETA", () => {
    expect(classifySharedFile("daocheng-final.mp4").kind).toBe("video");
    expect(classifySharedFile("IMG_4821.jpg").kind).toBe("image");
    expect(classifySharedFile("memo.ogg").kind).toBe("audio");
    expect(classifySharedFile("assets-backup.tar.gz").kind).toBe("archive");
    expect(classifySharedFile("roadmap.pdf").kind).toBe("pdf");
    expect(classifySharedFile("trace.log").kind).toBe("file");

    expect(splitFileName("daocheng-final.mp4")).toEqual(["daocheng-fin", "al.mp4"]);
    expect(splitFileName("a.txt")).toEqual(["a", ".txt"]);
    expect(splitFileName("README")).toEqual(["README", ""]);

    expect(formatMediaDuration(84)).toBe("1:24");
    expect(formatMediaDuration(3661)).toBe("1:01:01");
    expect(formatEta(12)).toBe("12 秒");
    expect(formatEta(65)).toBe("2 分");
  });
});

describe("SharedFileManager against testdata/shared-files/blocks.json", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  const blockBytes = Number(vectors.blockBytes);

  for (const c of vectors.cases) {
    it(`downloads and verifies SHA-256 for vector case "${c.name}" (${c.sizeBytes} bytes)`, async () => {
      const size = Number(c.sizeBytes);
      const requestedOffsets: string[] = [];
      const store = new MemorySharedFileStore();
      const autoSaved: Array<{ size: number; name: string }> = [];
      const autoOpened: string[] = [];

      const mgr = new SharedFileManager({
        store,
        isConnected: () => true,
        isMachineOnline: () => true,
        isSessionEnded: () => false,
        maxSharedFileBytes: () => 200_000_000n,
        sharedFileChunkBytes: () => blockBytes,
        onSaveLargeFile: (blob, safeName) => {
          autoSaved.push({ size: blob.size, name: safeName });
        },
        onAutoOpen: (_sid, f) => autoOpened.push(f.fileId),
        getChunk: async (_sid, _fid, offset) => {
          requestedOffsets.push(offset.toString());
          return {
            data: makeVectorBlock(size, Number(offset), blockBytes),
            totalBytes: BigInt(size),
          };
        },
      });
      mgr.setCurrentSession("s1");

      const file = makeFile({
        fileId: `vec-${c.name}`,
        name: `${c.name}.bin`,
        sizeBytes: BigInt(c.sizeBytes),
        sha256: "sha256" in c && c.sha256 ? hexToBytes(c.sha256) : await computeSha256(size),
      });
      await mgr.startDownload("s1", file, { autoOpenIfStaying: true });

      expect(requestedOffsets).toEqual(c.offsets);
      const st = mgr.stateOf("s1", file);

      if (size > LOCAL_CACHE_MAX_BYTES) {
        // Large files (> 50 MB) are handed off for save and not kept locally.
        expect(st.status).toBe("available");
        expect(autoSaved).toEqual([{ size, name: `${c.name}.bin` }]);
        expect(autoOpened).toEqual([]);
        expect(await store.getCompletedBlob("s1", file.fileId)).toBeUndefined();
        expect(await store.getPartialMeta("s1", file.fileId)).toBeUndefined();
      } else {
        expect(st.status).toBe("downloaded");
        expect(st.totalBytes).toBe(size);
        expect(st.blob?.size).toBe(size);
        expect(autoOpened).toEqual([file.fileId]);
        const bytes = new Uint8Array(await st.blob!.arrayBuffer());
        expect(bytes.length).toBe(size);
        for (let i = 0; i < Math.min(size, 64); i++) {
          expect(bytes[i]).toBe(deterministicFileByte(i));
        }
        if (size > 0) {
          expect(bytes[size - 1]).toBe(deterministicFileByte(size - 1));
        }
      }
    });
  }

  function makeVectorFile(c: (typeof vectors.cases)[number]): SharedFile {
    return makeFile({
      fileId: `vec-${c.name}`,
      name: `${c.name}.bin`,
      sizeBytes: BigInt(c.sizeBytes),
      sha256: hexToBytes(c.sha256),
    });
  }

  it("rejects wrong block padding length or mismatched totalBytes and discards chunks", async () => {
    const c = vectors.cases[3]; // exactly one block
    const store = new MemorySharedFileStore();
    const mgr = new SharedFileManager({
      store,
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      maxSharedFileBytes: () => 104_857_600n,
      sharedFileChunkBytes: () => blockBytes,
      getChunk: async () => ({
        data: new Uint8Array(blockBytes - 1),
        totalBytes: BigInt(c.sizeBytes),
      }),
    });

    const file = makeVectorFile(c);
    await mgr.startDownload("s1", file);
    const st = mgr.stateOf("s1", file);
    expect(st.status).toBe("failed");
    expect(st.failKind).toBe("checksum");
    expect(failureNotice(st.failKind)).toBe("没下载下来，文件校验不一致");
    expect(await store.getPartialMeta("s1", file.fileId)).toBeUndefined();

    // Missing totalBytes also fails integrity check.
    const mgr2 = new SharedFileManager({
      store: new MemorySharedFileStore(),
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      maxSharedFileBytes: () => 104_857_600n,
      sharedFileChunkBytes: () => blockBytes,
      getChunk: async () => ({
        data: makeVectorBlock(Number(c.sizeBytes), 0, blockBytes),
        totalBytes: undefined,
      }),
    });
    await mgr2.startDownload("s1", file);
    expect(mgr2.stateOf("s1", file).failKind).toBe("checksum");
  });

  it("discards partial data on SHA-256 mismatch and restarts from offset 0 on retry", async () => {
    const c = vectors.cases[5]; // two blocks and a bit
    const size = Number(c.sizeBytes);
    const store = new MemorySharedFileStore();
    let corruptFirstAttempt = true;
    const offsets: string[] = [];

    const mgr = new SharedFileManager({
      store,
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      maxSharedFileBytes: () => 104_857_600n,
      sharedFileChunkBytes: () => blockBytes,
      getChunk: async (_sid, _fid, offset) => {
        offsets.push(offset.toString());
        const blk = makeVectorBlock(size, Number(offset), blockBytes);
        if (corruptFirstAttempt && offset === 0n) blk[0] ^= 0xff;
        return { data: blk, totalBytes: BigInt(size) };
      },
    });

    const file = makeVectorFile(c);
    await mgr.startDownload("s1", file);
    expect(mgr.stateOf("s1", file).status).toBe("failed");
    expect(mgr.stateOf("s1", file).failKind).toBe("checksum");
    expect(await store.getPartialMeta("s1", file.fileId)).toBeUndefined();

    corruptFirstAttempt = false;
    offsets.length = 0;
    await mgr.startDownload("s1", file);
    expect(offsets).toEqual(c.offsets);
    expect(mgr.stateOf("s1", file).status).toBe("downloaded");
  });

  it("pauses keeping downloaded chunks, resumes without refetching completed chunks, and cancels clearing chunks", async () => {
    const c = vectors.cases[5]; // 3 chunks: 0, 786432, 1572864
    const size = Number(c.sizeBytes);
    const store = new MemorySharedFileStore();
    const fetched: string[] = [];
    let nowMs = 0;
    let mgr!: SharedFileManager;
    const file = makeVectorFile(c);

    let pauseOnBlock0 = true;
    mgr = new SharedFileManager({
      store,
      now: () => nowMs,
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      maxSharedFileBytes: () => 104_857_600n,
      sharedFileChunkBytes: () => blockBytes,
      getChunk: async (_sid, _fid, offset) => {
        fetched.push(offset.toString());
        nowMs += 3500;
        const blk = makeVectorBlock(size, Number(offset), blockBytes);
        return { data: blk, totalBytes: BigInt(size) };
      },
    });
    const unsub = mgr.subscribe(() => {
      if (pauseOnBlock0 && mgr.stateOf("s1", file).downloadedBytes === blockBytes) {
        pauseOnBlock0 = false;
        mgr.pause("s1", file.fileId);
      }
    });

    await mgr.startDownload("s1", file);
    expect(mgr.stateOf("s1", file).status).toBe("paused");
    expect(mgr.stateOf("s1", file).downloadedBytes).toBe(blockBytes);
    expect(fetched).toEqual(["0"]);

    // Resume only requests remaining offsets 786432 and 1572864.
    await mgr.startDownload("s1", file);
    expect(mgr.stateOf("s1", file).status).toBe("downloaded");
    expect(fetched).toEqual(c.offsets);

    // Delete local returns to available; start + pause + cancel wipes chunks.
    await mgr.deleteLocal("s1", file.fileId);
    expect(mgr.stateOf("s1", file).status).toBe("available");
    expect(await store.getCompletedBlob("s1", file.fileId)).toBeUndefined();

    fetched.length = 0;
    pauseOnBlock0 = true;
    await mgr.startDownload("s1", file);
    expect(mgr.stateOf("s1", file).status).toBe("paused");
    await mgr.cancel("s1", file.fileId);
    unsub();
    expect(mgr.stateOf("s1", file).status).toBe("available");
    expect(await store.getPartialMeta("s1", file.fileId)).toBeUndefined();
  });

  it("auto-resumes once on disconnect within 30s, or fails preserving chunks after 30s timeout", async () => {
    vi.useFakeTimers();
    const c = vectors.cases[5];
    const size = Number(c.sizeBytes);
    const store = new MemorySharedFileStore();
    let online = true;
    let dropSecondBlock = true;
    const fetched: string[] = [];

    const mgr = new SharedFileManager({
      store,
      isConnected: () => online,
      isMachineOnline: () => online,
      isSessionEnded: () => false,
      maxSharedFileBytes: () => 104_857_600n,
      sharedFileChunkBytes: () => blockBytes,
      getChunk: async (_sid, _fid, offset) => {
        fetched.push(offset.toString());
        if (offset === BigInt(blockBytes) && dropSecondBlock) {
          online = false;
          throw new CommandError("unavailable", "offline");
        }
        return { data: makeVectorBlock(size, Number(offset), blockBytes), totalBytes: BigInt(size) };
      },
    });

    const file = makeVectorFile(c);
    const p = mgr.startDownload("s1", file);
    await vi.advanceTimersByTimeAsync(100);
    // Still waiting to reconnect in downloading state.
    expect(mgr.stateOf("s1", file).status).toBe("downloading");
    expect(mgr.stateOf("s1", file).downloadedBytes).toBe(blockBytes);

    // Connection returns before 30s: resumes once and completes.
    dropSecondBlock = false;
    online = true;
    mgr.onConnectivityChange();
    await p;
    expect(mgr.stateOf("s1", file).status).toBe("downloaded");
    expect(fetched).toEqual(["0", "786432", "786432", "1572864"]);

    // Case 2: stays disconnected past 30s -> fails with disconnected & preserves block 0.
    const store2 = new MemorySharedFileStore();
    let online2 = true;
    const mgr2 = new SharedFileManager({
      store: store2,
      isConnected: () => online2,
      isMachineOnline: () => online2,
      isSessionEnded: () => false,
      maxSharedFileBytes: () => 104_857_600n,
      sharedFileChunkBytes: () => blockBytes,
      getChunk: async (_sid, _fid, offset) => {
        if (offset > 0n) {
          online2 = false;
          throw new CommandError("unavailable", "lost link");
        }
        return { data: makeVectorBlock(size, 0, blockBytes), totalBytes: BigInt(size) };
      },
    });
    const p2 = mgr2.startDownload("s1", file);
    await vi.advanceTimersByTimeAsync(30_001);
    await p2;
    online2 = true;
    expect(mgr2.stateOf("s1", file).status).toBe("failed");
    expect(mgr2.stateOf("s1", file).failKind).toBe("disconnected");
    expect(failureNotice("disconnected")).toBe("没下载下来，连接断了");
    expect(mgr2.stateOf("s1", file).downloadedBytes).toBe(blockBytes);
    expect((await store2.getPartialMeta("s1", file.fileId))?.nextOffset).toBe(blockBytes);
  });

  it("maps storage quota exceeded and daemon error reasons verbatim", async () => {
    const c = vectors.cases[3]; // 786432 bytes
    const fullStore = new MemorySharedFileStore({ maxBytes: 100 });
    const mgr = new SharedFileManager({
      store: fullStore,
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      maxSharedFileBytes: () => 104_857_600n,
      sharedFileChunkBytes: () => blockBytes,
      getChunk: async () => ({
        data: makeVectorBlock(Number(c.sizeBytes), 0, blockBytes),
        totalBytes: BigInt(c.sizeBytes),
      }),
    });
    const f = makeVectorFile(c);
    await mgr.startDownload("s1", f);
    const st = mgr.stateOf("s1", f);
    expect(st.status).toBe("failed");
    expect(st.failKind).toBe("storage");
    expect(failureNotice(st.failKind)).toBe("没下载下来，浏览器空间不足");

    const mgrOther = new SharedFileManager({
      store: new MemorySharedFileStore(),
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      maxSharedFileBytes: () => 104_857_600n,
      sharedFileChunkBytes: () => blockBytes,
      getChunk: async () => {
        throw new CommandError("not_found", "文件已从电脑删除");
      },
    });
    await mgrOther.startDownload("s1", f);
    const stOther = mgrOther.stateOf("s1", f);
    expect(stOther.status).toBe("failed");
    expect(stOther.failKind).toBe("other");
    expect(failureNotice(stOther.failKind, stOther.failReason)).toBe("没下载下来，文件已从电脑删除");
  });

  it("shares state by fileId, suppresses auto-open if user left session, and honors offline/ended/too_large states", async () => {
    const c = vectors.cases[2];
    const autoOpened: string[] = [];
    let online = false;
    let ended = false;
    const mgr = new SharedFileManager({
      store: new MemorySharedFileStore(),
      isConnected: () => true,
      isMachineOnline: () => online,
      isSessionEnded: () => ended,
      maxSharedFileBytes: () => 1000n,
      sharedFileChunkBytes: () => blockBytes,
      onAutoOpen: (_sid, f) => autoOpened.push(f.fileId),
      getChunk: async () => ({
        data: makeVectorBlock(Number(c.sizeBytes), 0, blockBytes),
        totalBytes: BigInt(c.sizeBytes),
      }),
    });

    const tooBig = makeFile({ fileId: "big", name: "big.zip", sizeBytes: 1001n });
    expect(mgr.stateOf("s1", tooBig).status).toBe("too_large");

    const small = makeFile({ fileId: "ok", name: "note.txt", sizeBytes: 10n });
    expect(mgr.stateOf("s1", small).status).toBe("offline");

    online = true;
    expect(mgr.stateOf("s1", small).status).toBe("available");

    // Simulate download where user navigates away mid-flight.
    const small2 = makeVectorFile(c);
    const mgr2 = new SharedFileManager({
      store: new MemorySharedFileStore(),
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => ended,
      maxSharedFileBytes: () => 10_000_000n,
      sharedFileChunkBytes: () => blockBytes,
      onAutoOpen: (_sid, f) => autoOpened.push(f.fileId),
      getChunk: async () => {
        mgr2.setCurrentSession("other-session");
        return {
          data: makeVectorBlock(Number(c.sizeBytes), 0, blockBytes),
          totalBytes: BigInt(c.sizeBytes),
        };
      },
    });
    mgr2.setCurrentSession("s1");
    await mgr2.startDownload("s1", small2, { autoOpenIfStaying: true });
    expect(mgr2.stateOf("s1", small2).status).toBe("downloaded");
    expect(autoOpened).toEqual([]);

    // Ended session: downloaded stays downloaded, undownloaded becomes cleaned.
    ended = true;
    expect(mgr2.stateOf("s1", small2).status).toBe("downloaded");
    expect(mgr2.stateOf("s1", makeFile({ fileId: "unfetched", name: "x.txt", sizeBytes: 12n })).status).toBe("cleaned");
  });
});
