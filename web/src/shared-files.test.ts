import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import vectors from "../../testdata/shared-files/blocks.json";
import outcomes from "../../testdata/transport/outcomes.json";
import {
  SharedFileFailureKind,
  SharedFileTransferStatus,
  type SharedFileLocalStateUpdate,
} from "./gen/remoteagent/core/v1/session_view_pb";
import { SharedFileSchema, type SharedFile } from "./gen/remoteagent/v1/event_pb";
import { CommandError, uncertain, type Outcome } from "./remote";
import { MemorySharedFileStore } from "./shared-file-store";
import {
  LOCAL_CACHE_MAX_BYTES,
  PROGRESS_REPORT_INTERVAL_MS,
  SharedFileManager,
} from "./shared-files";

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
      const reported: SharedFileLocalStateUpdate[] = [];

      const mgr = new SharedFileManager({
        store,
        isConnected: () => true,
        isMachineOnline: () => true,
        isSessionEnded: () => false,
        sharedFileChunkBytes: () => blockBytes,
        onStateReport: (updates) => reported.push(...updates),
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
      const st = mgr.transferOf("s1", file.fileId)!;

      if (size > LOCAL_CACHE_MAX_BYTES) {
        // Large files (> 50 MB) are handed off for save and not kept locally.
        expect(st.status).toBe("available");
        expect(reported.at(-1)?.status).toBe(SharedFileTransferStatus.UNSPECIFIED);
        expect(autoSaved).toEqual([{ size, name: `${c.name}.bin` }]);
        expect(autoOpened).toEqual([]);
        expect(await store.getCompletedBlob("s1", file.fileId)).toBeUndefined();
        expect(await store.getPartialMeta("s1", file.fileId)).toBeUndefined();
      } else {
        expect(st.status).toBe("downloaded");
        expect(reported.at(-1)?.status).toBe(SharedFileTransferStatus.DOWNLOADED);
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

  it("rejects wrong block padding length or mismatched totalBytes and reports CHECKSUM_MISMATCH", async () => {
    const c = vectors.cases[3]; // exactly one block
    const store = new MemorySharedFileStore();
    const reported: SharedFileLocalStateUpdate[] = [];
    const mgr = new SharedFileManager({
      store,
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      sharedFileChunkBytes: () => blockBytes,
      onStateReport: (u) => reported.push(...u),
      getChunk: async () => ({
        data: new Uint8Array(blockBytes - 1),
        totalBytes: BigInt(c.sizeBytes),
      }),
    });

    const file = makeVectorFile(c);
    await mgr.startDownload("s1", file);
    const st = mgr.transferOf("s1", file.fileId)!;
    expect(st.status).toBe("failed");
    expect(st.failKind).toBe("checksum");
    expect(reported.at(-1)).toMatchObject({
      status: SharedFileTransferStatus.FAILED,
      failureKind: SharedFileFailureKind.CHECKSUM_MISMATCH,
    });
    expect(await store.getPartialMeta("s1", file.fileId)).toBeUndefined();

    // Missing totalBytes also fails integrity check.
    const mgr2 = new SharedFileManager({
      store: new MemorySharedFileStore(),
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      sharedFileChunkBytes: () => blockBytes,
      getChunk: async () => ({
        data: makeVectorBlock(Number(c.sizeBytes), 0, blockBytes),
        totalBytes: undefined,
      }),
    });
    await mgr2.startDownload("s1", file);
    expect(mgr2.transferOf("s1", file.fileId)?.failKind).toBe("checksum");
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
    expect(mgr.transferOf("s1", file.fileId)?.status).toBe("failed");
    expect(mgr.transferOf("s1", file.fileId)?.failKind).toBe("checksum");
    expect(await store.getPartialMeta("s1", file.fileId)).toBeUndefined();

    corruptFirstAttempt = false;
    offsets.length = 0;
    await mgr.startDownload("s1", file);
    expect(offsets).toEqual(c.offsets);
    expect(mgr.transferOf("s1", file.fileId)?.status).toBe("downloaded");
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
      sharedFileChunkBytes: () => blockBytes,
      getChunk: async (_sid, _fid, offset) => {
        fetched.push(offset.toString());
        nowMs += 3500;
        const blk = makeVectorBlock(size, Number(offset), blockBytes);
        return { data: blk, totalBytes: BigInt(size) };
      },
    });
    const unsub = mgr.subscribe(() => {
      if (pauseOnBlock0 && mgr.transferOf("s1", file.fileId)?.downloadedBytes === blockBytes) {
        pauseOnBlock0 = false;
        mgr.pause("s1", file.fileId);
      }
    });

    await mgr.startDownload("s1", file);
    expect(mgr.transferOf("s1", file.fileId)?.status).toBe("paused");
    expect(mgr.transferOf("s1", file.fileId)?.downloadedBytes).toBe(blockBytes);
    expect(fetched).toEqual(["0"]);

    // Resume only requests remaining offsets 786432 and 1572864.
    await mgr.startDownload("s1", file);
    expect(mgr.transferOf("s1", file.fileId)?.status).toBe("downloaded");
    expect(fetched).toEqual(c.offsets);

    // Delete local returns to available; start + pause + cancel wipes chunks.
    await mgr.deleteLocal("s1", file.fileId);
    expect(mgr.transferOf("s1", file.fileId)?.status).toBe("available");
    expect(await store.getCompletedBlob("s1", file.fileId)).toBeUndefined();

    fetched.length = 0;
    pauseOnBlock0 = true;
    await mgr.startDownload("s1", file);
    expect(mgr.transferOf("s1", file.fileId)?.status).toBe("paused");
    await mgr.cancel("s1", file.fileId);
    unsub();
    expect(mgr.transferOf("s1", file.fileId)?.status).toBe("available");
    expect(await store.getPartialMeta("s1", file.fileId)).toBeUndefined();
  });

  it("throttles chunk progress reports to 250ms or integer % change and sends metadataOnly/replaceAll/clearSessionId", async () => {
    let nowMs = 1000;
    const events: Array<{ updates: SharedFileLocalStateUpdate[]; replaceAll?: boolean; clearSessionId?: string }> = [];
    const chunkBytes = 16;
    const totalBytes = 16 * 300; // 300 chunks (~0.33% per chunk)
    const mgr = new SharedFileManager({
      store: new MemorySharedFileStore(),
      now: () => nowMs,
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      sharedFileChunkBytes: () => chunkBytes,
      onStateReport: (updates, opts) => events.push({ updates, ...opts }),
      getChunk: async () => {
        nowMs += 10; // < 250ms per chunk
        return { data: new Uint8Array(chunkBytes), totalBytes: BigInt(totalBytes) };
      },
    });
    const file = makeFile({ fileId: "throttled", name: "big.bin", sizeBytes: BigInt(totalBytes) });
    await mgr.startDownload("s1", file);
    const progressReports = events
      .flatMap((e) => e.updates)
      .filter((u) => u.status === SharedFileTransferStatus.DOWNLOADING);
    // 300 chunks at 10ms each should be throttled far below 300 reports (~100 integer % steps + initial).
    expect(progressReports.length).toBeLessThan(130);
    expect(progressReports.length).toBeGreaterThanOrEqual(90);
    expect(PROGRESS_REPORT_INTERVAL_MS).toBe(250);

    // Duration metadata_only update preserves status and sends metadataOnly = true.
    events.length = 0;
    mgr.reportDuration("s1", "throttled", 84_200);
    expect(events).toHaveLength(1);
    expect(events[0].updates[0]).toMatchObject({
      sessionId: "s1",
      fileId: "throttled",
      metadataOnly: true,
      hasDuration: true,
      durationMs: 84_200n,
    });

    // reportAll sends replaceAll = true with current state and duration.
    events.length = 0;
    mgr.reportAll();
    expect(events).toHaveLength(1);
    expect(events[0].replaceAll).toBe(true);
    expect(events[0].updates[0]).toMatchObject({
      status: SharedFileTransferStatus.DOWNLOADED,
      hasDuration: true,
      durationMs: 84_200n,
    });

    // dropSession sends clearSessionId; clearAll sends replaceAll = true with empty updates.
    events.length = 0;
    await mgr.dropSession("s1");
    expect(events[0]).toMatchObject({ clearSessionId: "s1", updates: [] });

    events.length = 0;
    await mgr.clearAll();
    expect(events[0]).toMatchObject({ replaceAll: true, updates: [] });
  });

  it.each(["unavailable", "unknown_outcome"])("auto-resumes once on %s within 30s, or fails preserving chunks after timeout", async (code) => {
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
      sharedFileChunkBytes: () => blockBytes,
      getChunk: async (_sid, _fid, offset) => {
        fetched.push(offset.toString());
        if (offset === BigInt(blockBytes) && dropSecondBlock) {
          online = false;
          throw new CommandError(code, "offline");
        }
        return { data: makeVectorBlock(size, Number(offset), blockBytes), totalBytes: BigInt(size) };
      },
    });

    const file = makeVectorFile(c);
    const p = mgr.startDownload("s1", file);
    await vi.advanceTimersByTimeAsync(100);
    expect(mgr.transferOf("s1", file.fileId)?.status).toBe("downloading");
    expect(mgr.transferOf("s1", file.fileId)?.downloadedBytes).toBe(blockBytes);

    mgr.pause("s1", file.fileId);
    expect(mgr.transferOf("s1", file.fileId)?.status).toBe("paused");
    expect(mgr.transferOf("s1", file.fileId)?.downloadedBytes).toBe(blockBytes);
    await p;
    const resumed = mgr.startDownload("s1", file);
    await vi.advanceTimersByTimeAsync(100);

    // Connection returns before 30s: resumes once and completes.
    dropSecondBlock = false;
    online = true;
    mgr.onConnectivityChange();
    await resumed;
    expect(mgr.transferOf("s1", file.fileId)?.status).toBe("downloaded");
    expect(fetched).toEqual(["0", "786432", "786432", "786432", "1572864"]);

    // Case 2: stays disconnected past 30s -> fails with disconnected & preserves block 0.
    const store2 = new MemorySharedFileStore();
    let online2 = true;
    const reported2: SharedFileLocalStateUpdate[] = [];
    const mgr2 = new SharedFileManager({
      store: store2,
      isConnected: () => online2,
      isMachineOnline: () => online2,
      isSessionEnded: () => false,
      sharedFileChunkBytes: () => blockBytes,
      onStateReport: (u) => reported2.push(...u),
      getChunk: async (_sid, _fid, offset) => {
        if (offset > 0n) {
          online2 = false;
          throw new CommandError(code, "lost link");
        }
        return { data: makeVectorBlock(size, 0, blockBytes), totalBytes: BigInt(size) };
      },
    });
    const p2 = mgr2.startDownload("s1", file);
    await vi.advanceTimersByTimeAsync(30_001);
    await p2;
    expect(mgr2.transferOf("s1", file.fileId)?.status).toBe("failed");
    expect(mgr2.transferOf("s1", file.fileId)?.failKind).toBe("disconnected");
    expect(reported2.at(-1)).toMatchObject({
      status: SharedFileTransferStatus.FAILED,
      failureKind: SharedFileFailureKind.DISCONNECTED,
      downloadedBytes: BigInt(blockBytes),
    });
    expect((await store2.getPartialMeta("s1", file.fileId))?.nextOffset).toBe(blockBytes);
  });

  it("reports BROWSER_STORAGE_FULL on quota exceeded, CLEANED_UP on daemon not_found, and OTHER with detail", async () => {
    const c = vectors.cases[3]; // 786432 bytes
    const fullStore = new MemorySharedFileStore({ maxBytes: 100 });
    const quotaReports: SharedFileLocalStateUpdate[] = [];
    const mgr = new SharedFileManager({
      store: fullStore,
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      sharedFileChunkBytes: () => blockBytes,
      onStateReport: (u) => quotaReports.push(...u),
      getChunk: async () => ({
        data: makeVectorBlock(Number(c.sizeBytes), 0, blockBytes),
        totalBytes: BigInt(c.sizeBytes),
      }),
    });
    const f = makeVectorFile(c);
    await mgr.startDownload("s1", f);
    expect(quotaReports.at(-1)).toMatchObject({
      status: SharedFileTransferStatus.FAILED,
      failureKind: SharedFileFailureKind.BROWSER_STORAGE_FULL,
    });

    const notFoundReports: SharedFileLocalStateUpdate[] = [];
    const mgrNotFound = new SharedFileManager({
      store: new MemorySharedFileStore(),
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      sharedFileChunkBytes: () => blockBytes,
      onStateReport: (u) => notFoundReports.push(...u),
      getChunk: async () => {
        throw new CommandError("not_found", "文件已从电脑删除");
      },
    });
    await mgrNotFound.startDownload("s1", f);
    expect(mgrNotFound.transferOf("s1", f.fileId)?.status).toBe("cleaned_up");
    expect(notFoundReports.at(-1)?.status).toBe(SharedFileTransferStatus.CLEANED_UP);

    const otherReports: SharedFileLocalStateUpdate[] = [];
    const mgrOther = new SharedFileManager({
      store: new MemorySharedFileStore(),
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      sharedFileChunkBytes: () => blockBytes,
      onStateReport: (u) => otherReports.push(...u),
      getChunk: async () => {
        throw new CommandError("too_large", "file is larger than the transfer limit");
      },
    });
    await mgrOther.startDownload("s1", f);
    expect(otherReports.at(-1)).toMatchObject({
      status: SharedFileTransferStatus.FAILED,
      failureKind: SharedFileFailureKind.OTHER,
      failureDetail: "file is larger than the transfer limit",
    });
  });

  it("suppresses auto-open if user left session and refuses downloads when offline or ended", async () => {
    const c = vectors.cases[2];
    const autoOpened: string[] = [];
    let online = false;
    let ended = false;
    let calls = 0;
    const mgr = new SharedFileManager({
      store: new MemorySharedFileStore(),
      isConnected: () => true,
      isMachineOnline: () => online,
      isSessionEnded: () => ended,
      sharedFileChunkBytes: () => blockBytes,
      onAutoOpen: (_sid, f) => autoOpened.push(f.fileId),
      getChunk: async () => {
        calls++;
        mgr.setCurrentSession("other-session");
        return {
          data: makeVectorBlock(Number(c.sizeBytes), 0, blockBytes),
          totalBytes: BigInt(c.sizeBytes),
        };
      },
    });

    const small = makeVectorFile(c);
    await mgr.startDownload("s1", small, { autoOpenIfStaying: true });
    expect(calls).toBe(0);

    online = true;
    mgr.setCurrentSession("s1");
    await mgr.startDownload("s1", small, { autoOpenIfStaying: true });
    expect(mgr.transferOf("s1", small.fileId)?.status).toBe("downloaded");
    expect(autoOpened).toEqual([]);

    ended = true;
    calls = 0;
    await mgr.startDownload("s1", makeFile({ fileId: "unfetched", name: "x.txt", sizeBytes: 12n }));
    expect(calls).toBe(0);
  });
});

// Outcome certainty and the existing read-only download retry policy are
// separate: preserve v0.72's single recovery attempt and saved offset.
describe("download outcome policy", () => {
  afterEach(() => vi.useRealTimers());
  it.each(outcomes.downloads)("download outcome $code signed=$signed", async (v) => {
    vi.useFakeTimers();
    const error = new CommandError(v.code, "fixture", v.signed, true, v.outcome as Outcome);
    expect(uncertain(error)).toBe(v.outcome === "unknown");
    let calls = 0;
    const mgr = new SharedFileManager({
      store: new MemorySharedFileStore(),
      isConnected: () => true,
      isMachineOnline: () => true,
      isSessionEnded: () => false,
      sharedFileChunkBytes: () => 16,
      getChunk: async (_sid, _id, offset) => {
        calls++;
        if (offset > 0n) throw error;
        return { data: new Uint8Array(16), totalBytes: 32n };
      },
    });
    const file = makeFile({ fileId: "policy", name: "file.bin", sizeBytes: 32n, sha256: new Uint8Array(32) });
    const run = mgr.startDownload("s", file);
    await vi.advanceTimersByTimeAsync(30_001);
    await run;
    expect(calls).toBe(v.webRetry ? 3 : 2);
    if (v.code === "not_found") {
      expect(mgr.transferOf("s", file.fileId)?.status).toBe("cleaned_up");
      expect(mgr.transferOf("s", file.fileId)?.downloadedBytes).toBe(0);
    } else {
      expect(mgr.transferOf("s", file.fileId)?.status).toBe("failed");
      expect(mgr.transferOf("s", file.fileId)?.failKind).toBe(v.webRetry ? "disconnected" : "other");
      expect(mgr.transferOf("s", file.fileId)?.downloadedBytes).toBe(16);
    }
  });
});
