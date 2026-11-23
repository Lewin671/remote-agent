import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CoreHost } from "./core/host";
import { DemoRemote } from "./dev/demo";
import {
  EventSchema as WireEventSchema,
  SessionStatus,
  SharedFileSchema,
  ToolKind,
} from "./gen/remoteagent/v1/event_pb";
import { Agent, ApprovalMode } from "./gen/remoteagent/v1/event_pb";
import { SessionSchema } from "./gen/remoteagent/v1/local_pb";
import { MachineMetaSchema } from "./gen/remoteagent/v1/remote_pb";
import type { Event } from "./gen/remoteagent/core/v1/app_pb";
import { SharedFileCardStatus } from "./gen/remoteagent/core/v1/session_view_pb";
import {
  SharedFileDeleteLocalEffectSchema,
  SharedFileDeletePartialEffectSchema,
  SharedFileDeleteSessionEffectSchema,
  SharedFileInspectDiskEffectSchema,
  SharedFileIOErrorKind,
  SharedFileScanLocalEffectSchema,
  SharedFileVerifyAndCommitEffectSchema,
  SharedFileWriteChunkEffectSchema,
} from "./gen/remoteagent/core/v1/shared_files_pb";
import {
  bytesToHex,
  decodeSegment,
  MemorySharedFileStore,
  OpfsSharedFileStore,
  parsePartialMeta,
  safeSegment,
  SHARED_FILE_META_VERSION,
  StorageQuotaError,
  type DiskInspectResult,
  type ScannedSharedFileEntry,
  type SharedFileStore,
  type WriteChunkParams,
} from "./shared-file-store";
import { SharedFileExecutor, WEB_LOCAL_CACHE_MAX_BYTES } from "./shared-files";

async function sha256Of(data: Uint8Array): Promise<Uint8Array> {
  const buf = new ArrayBuffer(data.byteLength);
  new Uint8Array(buf).set(data);
  const digest = await crypto.subtle.digest("SHA-256", buf);
  return new Uint8Array(digest);
}

describe("SharedFileExecutor FIFO queue and platform I/O", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("serializes all 8 storage Effects strictly on one global FIFO queue across keys", async () => {
    const order: string[] = [];
    let active = 0;
    let maxConcurrent = 0;
    const gate = async (label: string, ms: number) => {
      active++;
      maxConcurrent = Math.max(maxConcurrent, active);
      order.push(`start:${label}`);
      await new Promise((r) => setTimeout(r, ms));
      order.push(`end:${label}`);
      active--;
    };

    const backing = new MemorySharedFileStore();
    const store: SharedFileStore = {
      async scanLocal(): Promise<ScannedSharedFileEntry[]> {
        await gate("scan", 20);
        return backing.scanLocal();
      },
      async inspectDisk(): Promise<DiskInspectResult> {
        await gate("inspect", 15);
        return backing.inspectDisk();
      },
      async writeChunk(params: WriteChunkParams): Promise<void> {
        await gate(`write:${params.sessionId}/${params.fileId}`, 10);
        return backing.writeChunk(params);
      },
      readPartialBlob: (s, f) => backing.readPartialBlob(s, f),
      async commitCompleted(s, f, bytes): Promise<Blob> {
        await gate(`commit:${s}/${f}`, 10);
        return backing.commitCompleted(s, f, bytes);
      },
      getCompletedBlob: (s, f) => backing.getCompletedBlob(s, f),
      async deletePartial(s, f): Promise<void> {
        await gate(`delPartial:${s}/${f}`, 5);
        return backing.deletePartial(s, f);
      },
      async deleteLocal(s, f): Promise<void> {
        await gate(`delLocal:${s}/${f}`, 5);
        return backing.deleteLocal(s, f);
      },
      async dropSession(s): Promise<void> {
        await gate(`delSession:${s}`, 5);
        return backing.dropSession(s);
      },
      async clearAll(): Promise<void> {
        await gate("clearAll", 5);
        return backing.clearAll();
      },
    };

    const events: Event["event"][] = [];
    const exec = new SharedFileExecutor({ store, onEvent: (ev) => events.push(ev) });
    const data = new Uint8Array([1, 2, 3, 4]);
    const digest = await sha256Of(data);

    exec.handleScanLocal(create(SharedFileScanLocalEffectSchema, { opId: 1n }));
    exec.handleInspectDisk(
      create(SharedFileInspectDiskEffectSchema, { opId: 2n, sessionId: "s1", fileId: "f1", requiredBytes: 4n }),
    );
    exec.handleWriteChunk(
      create(SharedFileWriteChunkEffectSchema, {
        opId: 3n,
        sessionId: "s1",
        fileId: "f1",
        displayName: "a.bin",
        offset: 0n,
        data,
        totalSizeBytes: 4n,
        chunkBytes: 16,
        committedOffset: 4n,
        expectedSha256: digest,
        resetPartial: true,
      }),
    );
    exec.handleVerifyAndCommit(
      create(SharedFileVerifyAndCommitEffectSchema, {
        opId: 4n,
        sessionId: "s1",
        fileId: "f1",
        displayName: "a.bin",
        totalSizeBytes: 4n,
        expectedSha256: digest,
        deliverOnly: false,
      }),
    );
    exec.handleDeletePartial(create(SharedFileDeletePartialEffectSchema, { sessionId: "s2", fileId: "f2" }));
    exec.handleDeleteLocal(create(SharedFileDeleteLocalEffectSchema, { sessionId: "s1", fileId: "f1" }));
    exec.handleDeleteSession(create(SharedFileDeleteSessionEffectSchema, { sessionId: "s2" }));
    exec.handleClearAll();

    await exec.whenIdle();

    expect(maxConcurrent).toBe(1);
    expect(order).toEqual([
      "start:scan",
      "end:scan",
      "start:inspect",
      "end:inspect",
      "start:write:s1/f1",
      "end:write:s1/f1",
      "start:commit:s1/f1",
      "end:commit:s1/f1",
      "start:delPartial:s2/f2",
      "end:delPartial:s2/f2",
      "start:delLocal:s1/f1",
      "end:delLocal:s1/f1",
      "start:delSession:s2",
      "end:delSession:s2",
      "start:clearAll",
      "end:clearAll",
    ]);
    expect(events.map((e) => e.case)).toEqual([
      "sharedFileLocalScanned",
      "sharedFileDiskInspected",
      "sharedFileWriteChunkResult",
      "sharedFileVerifyAndCommitResult",
    ]);
  });

  it("writes v2 metadata, truncates on reset/write, rejects legacy .meta.json, and maps quota errors", async () => {
    const store = new MemorySharedFileStore({ maxBytes: 12 });
    const sha = await sha256Of(new Uint8Array([1, 2, 3, 4]));
    const shaHex = bytesToHex(sha);

    // Reject legacy v1 .meta.json format.
    expect(
      parsePartialMeta(JSON.stringify({ nextOffset: 4, sizeBytes: "8", chunkBytes: 4, sha256Hex: shaHex })),
    ).toBeUndefined();
    expect(decodeSegment(safeSegment("sess/中文-1"))).toBe("sess/中文-1");

    // Seed legacy partial -> scanLocal emits malformed entry (committedOffset = 0) so Core emits DeletePartial.
    store.seedPartialRaw(
      "s-legacy",
      "f-old",
      JSON.stringify({ nextOffset: 4, sizeBytes: "8", chunkBytes: 4, sha256Hex: shaHex }),
      new Uint8Array([9, 9, 9, 9]),
    );
    const scannedLegacy = await store.scanLocal();
    expect(scannedLegacy).toHaveLength(1);
    expect(scannedLegacy[0]).toMatchObject({
      sessionId: "s-legacy",
      fileId: "f-old",
      completed: false,
      committedOffset: 0n,
      expectedSizeBytes: 0n,
    });
    await store.deletePartial("s-legacy", "f-old");

    // Write chunk 0 (8 bytes), then overwrite with resetPartial=true (4 bytes) -> truncated to 4 bytes.
    await store.writeChunk({
      sessionId: "s1",
      fileId: "f1",
      displayName: "demo.bin",
      offset: 0n,
      data: new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8]),
      totalSizeBytes: 8n,
      chunkBytes: 8,
      committedOffset: 8n,
      expectedSha256: sha,
      resetPartial: true,
    });
    await store.writeChunk({
      sessionId: "s1",
      fileId: "f1",
      displayName: "demo.bin",
      offset: 0n,
      data: new Uint8Array([1, 2, 3, 4]),
      totalSizeBytes: 4n,
      chunkBytes: 4,
      committedOffset: 4n,
      expectedSha256: sha,
      resetPartial: true,
    });
    const partial = await store.readPartialBlob("s1", "f1");
    expect(partial?.size).toBe(4);

    // Quota exhaustion on writeChunk maps strictly to SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL.
    const events: Event["event"][] = [];
    const exec = new SharedFileExecutor({ store, onEvent: (ev) => events.push(ev) });
    exec.handleWriteChunk(
      create(SharedFileWriteChunkEffectSchema, {
        opId: 10n,
        sessionId: "s1",
        fileId: "f-big",
        displayName: "big.bin",
        offset: 0n,
        data: new Uint8Array(20),
        totalSizeBytes: 20n,
        chunkBytes: 20,
        committedOffset: 20n,
        expectedSha256: sha,
        resetPartial: true,
      }),
    );
    await exec.whenIdle();
    const res = events[0];
    expect(res.case).toBe("sharedFileWriteChunkResult");
    if (res.case === "sharedFileWriteChunkResult") {
      expect(res.value.ok).toBe(false);
      expect(res.value.errorKind).toBe(SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL);
    }

    // Commit quota failure preserves .part for retry.
    const tightStore = new MemorySharedFileStore({ maxBytes: 5 });
    await tightStore.writeChunk({
      sessionId: "s1",
      fileId: "f-tight",
      displayName: "tight.bin",
      offset: 0n,
      data: new Uint8Array([1, 2, 3, 4]),
      totalSizeBytes: 4n,
      chunkBytes: 4,
      committedOffset: 4n,
      expectedSha256: sha,
      resetPartial: true,
    });
    const commitEvents: Event["event"][] = [];
    const exec2 = new SharedFileExecutor({ store: tightStore, onEvent: (ev) => commitEvents.push(ev) });
    exec2.handleVerifyAndCommit(
      create(SharedFileVerifyAndCommitEffectSchema, {
        opId: 11n,
        sessionId: "s1",
        fileId: "f-tight",
        displayName: "tight.bin",
        totalSizeBytes: 4n,
        expectedSha256: sha,
        deliverOnly: false,
      }),
    );
    await exec2.whenIdle();
    const cres = commitEvents[0];
    expect(cres.case).toBe("sharedFileVerifyAndCommitResult");
    if (cres.case === "sharedFileVerifyAndCommitResult") {
      expect(cres.value.ok).toBe(false);
      expect(cres.value.errorKind).toBe(SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL);
    }
    expect((await tightStore.readPartialBlob("s1", "f-tight"))?.size).toBe(4);
  });

  it("handles 0-byte verify, deliver_only direct save without caching, checksum mismatch, and LocalMissing", async () => {
    const store = new MemorySharedFileStore();
    const events: Event["event"][] = [];
    const delivered: Array<{ name: string; size: number; type: string }> = [];
    const exec = new SharedFileExecutor({
      store,
      onEvent: (ev) => events.push(ev),
      onDeliverOnly: (blob, displayName) => delivered.push({ name: displayName, size: blob.size, type: blob.type }),
    });

    // 0-byte file verification and commit.
    const emptySha = await sha256Of(new Uint8Array(0));
    exec.handleWriteChunk(
      create(SharedFileWriteChunkEffectSchema, {
        opId: 1n,
        sessionId: "s1",
        fileId: "empty",
        displayName: "empty.txt",
        offset: 0n,
        data: new Uint8Array(0),
        totalSizeBytes: 0n,
        chunkBytes: 16,
        committedOffset: 0n,
        expectedSha256: emptySha,
        resetPartial: true,
      }),
    );
    exec.handleVerifyAndCommit(
      create(SharedFileVerifyAndCommitEffectSchema, {
        opId: 2n,
        sessionId: "s1",
        fileId: "empty",
        displayName: "empty.txt",
        totalSizeBytes: 0n,
        expectedSha256: emptySha,
        deliverOnly: false,
      }),
    );
    await exec.whenIdle();
    expect(events[1].case === "sharedFileVerifyAndCommitResult" && events[1].value.ok).toBe(true);
    expect((await store.getCompletedBlob("s1", "empty"))?.size).toBe(0);

    // deliver_only verifies, delivers as application/octet-stream, deletes .part, and does not cache .bin.
    const payload = new Uint8Array([10, 20, 30, 40, 50]);
    const payloadSha = await sha256Of(payload);
    exec.handleWriteChunk(
      create(SharedFileWriteChunkEffectSchema, {
        opId: 3n,
        sessionId: "s1",
        fileId: "big",
        displayName: "archive.tar",
        offset: 0n,
        data: payload,
        totalSizeBytes: 5n,
        chunkBytes: 16,
        committedOffset: 5n,
        expectedSha256: payloadSha,
        resetPartial: true,
      }),
    );
    exec.handleVerifyAndCommit(
      create(SharedFileVerifyAndCommitEffectSchema, {
        opId: 4n,
        sessionId: "s1",
        fileId: "big",
        displayName: "archive.tar",
        totalSizeBytes: 5n,
        expectedSha256: payloadSha,
        deliverOnly: true,
      }),
    );
    await exec.whenIdle();
    expect(delivered).toEqual([{ name: "archive.tar", size: 5, type: "application/octet-stream" }]);
    expect(await store.getCompletedBlob("s1", "big")).toBeUndefined();
    expect(await store.readPartialBlob("s1", "big")).toBeUndefined();

    // Checksum mismatch deletes .part and reports CHECKSUM_MISMATCH.
    const badSha = new Uint8Array(32).fill(0xff);
    exec.handleWriteChunk(
      create(SharedFileWriteChunkEffectSchema, {
        opId: 5n,
        sessionId: "s1",
        fileId: "bad",
        displayName: "bad.bin",
        offset: 0n,
        data: payload,
        totalSizeBytes: 5n,
        chunkBytes: 16,
        committedOffset: 5n,
        expectedSha256: badSha,
        resetPartial: true,
      }),
    );
    exec.handleVerifyAndCommit(
      create(SharedFileVerifyAndCommitEffectSchema, {
        opId: 6n,
        sessionId: "s1",
        fileId: "bad",
        displayName: "bad.bin",
        totalSizeBytes: 5n,
        expectedSha256: badSha,
        deliverOnly: false,
      }),
    );
    await exec.whenIdle();
    const badRes = events.at(-1)!;
    expect(badRes.case).toBe("sharedFileVerifyAndCommitResult");
    if (badRes.case === "sharedFileVerifyAndCommitResult") {
      expect(badRes.value.ok).toBe(false);
      expect(badRes.value.errorKind).toBe(SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_CHECKSUM_MISMATCH);
    }
    expect(await store.readPartialBlob("s1", "bad")).toBeUndefined();

    // Missing completed copy triggers SharedFileLocalMissingEvent.
    const missing = await exec.ensureBlob("s1", "evicted", { reportMissing: true });
    expect(missing).toBeUndefined();
    const missEv = events.at(-1)!;
    expect(missEv.case).toBe("sharedFileLocalMissing");
    if (missEv.case === "sharedFileLocalMissing") {
      expect(missEv.value).toMatchObject({ sessionId: "s1", fileId: "evicted" });
    }
  });

  it("OpfsSharedFileStore falls back cleanly to memory when OPFS is unavailable and uses storage.estimate()", async () => {
    vi.stubGlobal("navigator", {
      storage: {
        estimate: async () => ({ quota: 200_000_000, usage: 50_000_000 }),
        getDirectory: async () => {
          throw new DOMException("denied", "SecurityError");
        },
      },
    });
    const store = new OpfsSharedFileStore();
    expect(await store.inspectDisk()).toEqual({ known: true, availableBytes: 150_000_000n });

    // Writing falls back cleanly to in-memory storage when getDirectory() throws.
    const sha = await sha256Of(new Uint8Array([7, 8, 9]));
    await store.writeChunk({
      sessionId: "s-fb",
      fileId: "f-fb",
      displayName: "fb.bin",
      offset: 0n,
      data: new Uint8Array([7, 8, 9]),
      totalSizeBytes: 3n,
      chunkBytes: 16,
      committedOffset: 3n,
      expectedSha256: sha,
      resetPartial: true,
    });
    expect(await store.scanLocal()).toHaveLength(1);
    const bytes = await (await store.readPartialBlob("s-fb", "f-fb"))!.arrayBuffer();
    const committed = await store.commitCompleted("s-fb", "f-fb", bytes);
    expect(committed.size).toBe(3);
    expect((await store.getCompletedBlob("s-fb", "f-fb"))?.size).toBe(3);
    // Once OPFS is disabled, inspectDisk falls back cleanly to known=false.
    expect(await store.inspectDisk()).toEqual({ known: false, availableBytes: 0n });
    expect(new StorageQuotaError().name).toBe("StorageQuotaError");
  });
});

describe("CoreHost real Core WASM shared-file smoke test", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("runs scan, chunked download, pause/resume, auto-open, and > 50 MiB deliver_only through real Core", async () => {
    const chunkBytes = 786_432;
    const smallBytes = new Uint8Array(chunkBytes + 1024);
    for (let i = 0; i < smallBytes.length; i++) smallBytes[i] = i % 251;
    const smallSha = await sha256Of(smallBytes);

    const largeSize = Number(WEB_LOCAL_CACHE_MAX_BYTES) + 1;
    const largeBytes = new Uint8Array(largeSize);
    for (let i = 0; i < largeSize; i++) largeBytes[i] = i % 251;
    const largeSha = await sha256Of(largeBytes);

    const demo = DemoRemote.create(new URLSearchParams(), async () => {});
    const mac = (demo as unknown as { demoMachines: Map<string, { meta?: { capabilities?: { sharedFiles?: boolean; sharedFileChunkBytes?: number } } }> }).demoMachines.get("mac")!;
    mac.meta ??= create(MachineMetaSchema, { hostname: "MacBook", capabilities: {} });
    mac.meta.capabilities ??= create(MachineMetaSchema, { capabilities: {} }).capabilities!;
    mac.meta.capabilities.sharedFiles = true;
    mac.meta.capabilities.sharedFileChunkBytes = chunkBytes;

    const demoSessions = (demo as unknown as { demoSessions: Map<string, unknown>; evs: Map<string, unknown[]> });
    demoSessions.demoSessions.set("s-smoke", {
      id: "s-smoke",
      machineId: "mac",
      info: create(SessionSchema, {
        id: "s-smoke",
        title: "smoke",
        cwd: "/tmp",
        agent: Agent.CLAUDE,
        status: SessionStatus.IDLE,
        mode: ApprovalMode.ASK,
      }),
      metaRev: 1n,
      lastSeq: 1n,
      updatedMs: BigInt(Date.now()),
      endedMs: 0n,
    });
    demoSessions.evs.set("s-smoke", [
      create(WireEventSchema, {
        seq: 1n,
        sessionId: "s-smoke",
        kind: {
          case: "toolCall",
          value: {
            callId: "c1",
            kind: ToolKind.OTHER,
            title: "send_file",
            status: "completed",
            exitCode: 0,
            sharedFiles: [
              create(SharedFileSchema, { fileId: "f-small", name: "small.bin", sizeBytes: BigInt(smallBytes.length), sha256: smallSha, path: "/tmp/small.bin" }),
              create(SharedFileSchema, { fileId: "f-large", name: "large.bin", sizeBytes: BigInt(largeSize), sha256: largeSha, path: "/tmp/large.bin" }),
              create(SharedFileSchema, { fileId: "f-resumed", name: "resumed.bin", sizeBytes: BigInt(smallBytes.length), sha256: smallSha, path: "/tmp/resumed.bin" }),
            ],
          },
        },
      }),
    ]);

    // Seed a valid v2 partial + a legacy corrupt partial before startup scan.
    demo.sharedFileStore.seedPartialRaw(
      "s-smoke",
      "f-resumed",
      JSON.stringify({
        v: SHARED_FILE_META_VERSION,
        sessionId: "s-smoke",
        fileId: "f-resumed",
        displayName: "resumed.bin",
        totalSizeBytes: String(smallBytes.length),
        chunkBytes,
        committedOffset: String(chunkBytes),
        expectedSha256: bytesToHex(smallSha),
      }),
      smallBytes.subarray(0, chunkBytes),
    );
    demo.sharedFileStore.seedPartialRaw(
      "s-smoke",
      "f-legacy",
      JSON.stringify({ nextOffset: 10, sizeBytes: "20", chunkBytes: 10, sha256Hex: bytesToHex(smallSha) }),
      new Uint8Array(10),
    );

    const fetchedOffsets: Array<{ fileId: string; offset: bigint }> = [];
    vi.spyOn(demo, "getSharedFileChunk").mockImplementation(async (_sid, fileId, offset) => {
      fetchedOffsets.push({ fileId, offset });
      const src = fileId === "f-large" ? largeBytes : smallBytes;
      const block = new Uint8Array(chunkBytes);
      const off = Number(offset);
      const slice = src.subarray(off, Math.min(src.length, off + chunkBytes));
      block.set(slice, 0);
      return { data: block, totalBytes: BigInt(src.length) };
    });

    const delivered: Array<{ name: string; size: number }> = [];
    const opened: Array<{ sid: string; fileId: string; size: number }> = [];
    const host = new CoreHost(demo, {
      sharedFileStore: demo.sharedFileStore,
      onDeliverLargeSharedFile: (blob, displayName) => delivered.push({ name: displayName, size: blob.size }),
    });
    host.sharedFiles.setAutoOpenHandler((sid, fileId, blob) => opened.push({ sid, fileId, size: blob.size }));
    try {
      host.start();
      host.watchSession("s-smoke");
      host.sessionOpen("s-smoke", false, false);
      host.sharedFileSetVisibleSession("s-smoke");

      const getCard = (fid: string) => {
        const vm = host.viewSession("s-smoke");
        for (const it of vm.timeline?.items ?? []) {
          if (it.kind.case === "sharedFiles") {
            const f = it.kind.value.files.find((x) => x.fileId === fid);
            if (f) return f;
          }
        }
        return undefined;
      };

      // Startup scan restores f-resumed as PAUSED and deletes legacy f-legacy.
      await vi.waitFor(() => {
        expect(getCard("f-resumed")?.status).toBe(SharedFileCardStatus.PAUSED);
      });
      await host.sharedFiles.whenIdle();
      expect(await demo.sharedFileStore.readPartialBlob("s-smoke", "f-legacy")).toBeUndefined();

      // Resume f-resumed: should only fetch offset=chunkBytes, verify, commit, and auto-open.
      host.sharedFileStartDownload("s-smoke", "f-resumed", true);
      await vi.waitFor(() => {
        expect(getCard("f-resumed")?.status).toBe(SharedFileCardStatus.DOWNLOADED);
      });
      expect(fetchedOffsets).toEqual([{ fileId: "f-resumed", offset: BigInt(chunkBytes) }]);
      expect(opened).toEqual([{ sid: "s-smoke", fileId: "f-resumed", size: smallBytes.length }]);

      // Leaving session revokes auto-open for f-small.
      host.sharedFileSetVisibleSession("");
      host.sharedFileStartDownload("s-smoke", "f-small", true);
      await vi.waitFor(() => {
        expect(getCard("f-small")?.status).toBe(SharedFileCardStatus.DOWNLOADED);
      });
      expect(opened).toHaveLength(1);

      // > 50 MiB file: Core sets deliver_only = true -> triggers browser save, returns to AVAILABLE, not cached.
      host.sharedFileSetVisibleSession("s-smoke");
      host.sharedFileStartDownload("s-smoke", "f-large", true);
      await vi.waitFor(
        () => {
          expect(delivered).toHaveLength(1);
          expect(getCard("f-large")?.status).toBe(SharedFileCardStatus.AVAILABLE);
        },
        { timeout: 10_000 },
      );
      expect(delivered).toEqual([{ name: "large.bin", size: largeSize }]);
      expect(await demo.sharedFileStore.getCompletedBlob("s-smoke", "f-large")).toBeUndefined();
      expect(await demo.sharedFileStore.readPartialBlob("s-smoke", "f-large")).toBeUndefined();
      expect(opened).toHaveLength(1);
    } finally {
      host.dispose();
      demo.dispose();
    }
  });
});
