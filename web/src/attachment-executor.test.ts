// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { IDBKeyRange } from "fake-indexeddb";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AttachmentExecutor } from "./attachment-executor";
import { MemoryAttachmentStore, WebAttachmentStore } from "./attachment-store";
import type { Event } from "./gen/remoteagent/core/v1/app_pb";
import {
  AttachmentHashFileEffectSchema,
  AttachmentIOErrorKind,
  AttachmentItemKind,
  AttachmentPrepareImageEffectSchema,
  AttachmentReadChunkEffectSchema,
  AttachmentReleaseLocalEffectSchema,
  DraftScopeKind,
  DraftScopeSchema,
  DraftStorageBucket,
  DraftStorageDeleteEffectSchema,
  DraftStorageListEffectSchema,
  DraftStorageReadEffectSchema,
  DraftStorageWriteEffectSchema,
} from "./gen/remoteagent/core/v1/attachments_pb";
import { ImageFormat } from "./gen/remoteagent/v1/event_pb";
import { ImageError } from "./images";
import { storedSecurity } from "./test/stored-security-fixture";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const SESSION_SCOPE = create(DraftScopeSchema, {
  kind: DraftScopeKind.SESSION,
  id: "s1",
});

describe("AttachmentExecutor FIFO queue and platform I/O", () => {
  it("executes all storage-class effects strictly in global FIFO order", async () => {
    const store = new MemoryAttachmentStore();
    const order: string[] = [];
    let releaseFirstWrite!: () => void;
    const origWrite = store.write.bind(store);
    const origRead = store.read.bind(store);
    const origDelete = store.delete.bind(store);
    const origRelease = store.deleteLocalRefs.bind(store);
    let firstWrite = true;
    store.write = async (...args) => {
      order.push("write:start:" + args[1]);
      if (firstWrite) {
        firstWrite = false;
        await new Promise<void>((r) => { releaseFirstWrite = r; });
      }
      await origWrite(...args);
      order.push("write:end:" + args[1]);
    };
    store.read = async (...args) => {
      order.push("read:" + args[1]);
      return origRead(...args);
    };
    store.delete = async (...args) => {
      order.push("delete:" + args[1]);
      return origDelete(...args);
    };
    store.deleteLocalRefs = async (refs) => {
      order.push("release:" + refs.join(","));
      return origRelease(refs);
    };

    const events: Event["event"][] = [];
    const exec = new AttachmentExecutor({ store, onEvent: (ev) => events.push(ev) });
    const ref = exec.stagePickedBlob(new Blob(["abc"], { type: "text/plain" }));

    exec.handleDraftStorageWrite(
      create(DraftStorageWriteEffectSchema, {
        opId: 1n,
        bucket: DraftStorageBucket.DRAFT,
        key: "session:s1",
        data: new Uint8Array([1, 2, 3]),
        referencedRefs: [ref],
      }),
    );
    exec.handleDraftStorageRead(
      create(DraftStorageReadEffectSchema, { opId: 2n, bucket: DraftStorageBucket.DRAFT, key: "session:s1" }),
    );
    exec.handleDraftStorageDelete(
      create(DraftStorageDeleteEffectSchema, { bucket: DraftStorageBucket.OUTBOX, key: "out:s1:r1" }),
    );
    exec.handleReleaseLocal(create(AttachmentReleaseLocalEffectSchema, { refs: [ref] }));

    await vi.waitFor(() => expect(order).toEqual(["write:start:session:s1"]));
    releaseFirstWrite();
    await exec.whenIdle();

    expect(order).toEqual([
      "write:start:session:s1",
      "write:end:session:s1",
      "read:session:s1",
      "delete:out:s1:r1",
      `release:${ref}`,
    ]);
    expect(events.map((e) => e.case)).toEqual(["draftStorageWriteResult", "draftStorageReadResult"]);
    const readEv = events[1];
    expect(readEv.case).toBe("draftStorageReadResult");
    if (readEv.case === "draftStorageReadResult") {
      expect(readEv.value.opId).toBe(2n);
      expect(readEv.value.found).toBe(true);
      expect(readEv.value.data).toEqual(new Uint8Array([1, 2, 3]));
    }
    expect(await store.getLocalBlob(ref)).toBeUndefined();
  });

  it("reports STORAGE_FULL, CORRUPT_STORE, and local_refs on storage effects", async () => {
    const store = new MemoryAttachmentStore({ maxBytes: 8 });
    store.seedRecord(DraftStorageBucket.DRAFT, "session:bad", new Uint8Array([9]), true);
    store.seedBlob("orphan-1.bin", new Blob(["x"]));
    const events: Event["event"][] = [];
    const exec = new AttachmentExecutor({ store, onEvent: (ev) => events.push(ev) });

    exec.handleDraftStorageList(create(DraftStorageListEffectSchema, { opId: 10n }));
    exec.handleDraftStorageRead(
      create(DraftStorageReadEffectSchema, { opId: 11n, bucket: DraftStorageBucket.DRAFT, key: "session:bad" }),
    );
    exec.handleDraftStorageWrite(
      create(DraftStorageWriteEffectSchema, {
        opId: 12n,
        bucket: DraftStorageBucket.DRAFT,
        key: "session:big",
        data: new Uint8Array(64),
      }),
    );
    await exec.whenIdle();

    expect(events[0].case).toBe("draftStorageListed");
    if (events[0].case === "draftStorageListed") {
      expect(events[0].value.opId).toBe(10n);
      expect(events[0].value.entries).toHaveLength(1);
      expect(events[0].value.entries[0].corrupt).toBe(true);
      expect(events[0].value.localRefs).toContain("orphan-1.bin");
    }
    expect(events[1].case).toBe("draftStorageReadResult");
    if (events[1].case === "draftStorageReadResult") {
      expect(events[1].value.opId).toBe(11n);
      expect(events[1].value.errorKind).toBe(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE);
    }
    expect(events[2].case).toBe("draftStorageWriteResult");
    if (events[2].case === "draftStorageWriteResult") {
      expect(events[2].value.opId).toBe(12n);
      expect(events[2].value.ok).toBe(false);
      expect(events[2].value.errorKind).toBe(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL);
    }
  });

  it("passes Core prepare limits without hardcoding and maps ImageError kinds", async () => {
    const store = new MemoryAttachmentStore();
    const calls: Array<{ original: boolean; maxOriginalBytes: number; maxDimensionPx: number; maxCompressedBytes: number }> = [];
    let failWith: Error | undefined;
    const events: Event["event"][] = [];
    const exec = new AttachmentExecutor({
      store,
      onEvent: (ev) => events.push(ev),
      prepareImage: async (_blob, opts) => {
        calls.push(opts);
        if (failWith) throw failWith;
        return { bytes: new Uint8Array([0x89, 0x50, 0x4e, 0x47]), format: "png", width: 800, height: 600 };
      },
    });

    const srcRef = exec.stagePickedBlob(new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }));
    exec.handlePrepareImage(
      create(AttachmentPrepareImageEffectSchema, {
        opId: 21n,
        scope: SESSION_SCOPE,
        itemId: "att-1",
        sourceRef: srcRef,
        original: false,
        maxOriginalBytes: 20_971_520n,
        maxDimensionPx: 1600,
        maxCompressedBytes: 512_000n,
      }),
    );
    await exec.whenIdle();

    expect(calls).toEqual([
      { original: false, maxOriginalBytes: 20_971_520, maxDimensionPx: 1600, maxCompressedBytes: 512_000 },
    ]);
    const okEv = events.shift()!;
    expect(okEv.case).toBe("attachmentPrepareImageResult");
    if (okEv.case === "attachmentPrepareImageResult") {
      expect(okEv.value.opId).toBe(21n);
      expect(okEv.value.ok).toBe(true);
      expect(okEv.value.format).toBe(ImageFormat.PNG);
      expect(okEv.value.width).toBe(800);
      expect(okEv.value.height).toBe(600);
      expect(okEv.value.sizeBytes).toBe(4n);
      expect(okEv.value.sha256.byteLength).toBe(32);
      expect(okEv.value.preparedRef).toBeTruthy();
      expect(okEv.value.thumbRef).toBeTruthy();
      expect(okEv.value.preparedRef).not.toBe(okEv.value.thumbRef);
    }

    // Missing source -> SOURCE_MISSING + isReadingSource=true
    exec.handlePrepareImage(
      create(AttachmentPrepareImageEffectSchema, {
        opId: 22n,
        scope: SESSION_SCOPE,
        itemId: "att-2",
        sourceRef: "absent.png",
      }),
    );
    // Dimensions & OriginalTooLarge errors
    failWith = new ImageError("too_large", "too big", undefined, "dimensions");
    exec.handlePrepareImage(
      create(AttachmentPrepareImageEffectSchema, {
        opId: 23n,
        scope: SESSION_SCOPE,
        itemId: "att-3",
        sourceRef: srcRef,
      }),
    );
    await exec.whenIdle();

    const missEv = events.find((e) => e.case === "attachmentPrepareImageResult" && e.value.opId === 22n);
    expect(missEv?.case === "attachmentPrepareImageResult" && missEv.value.errorKind).toBe(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING);
    expect(missEv?.case === "attachmentPrepareImageResult" && missEv.value.isReadingSource).toBe(true);

    const dimEv = events.find((e) => e.case === "attachmentPrepareImageResult" && e.value.opId === 23n);
    expect(dimEv?.case === "attachmentPrepareImageResult" && dimEv.value.errorKind).toBe(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIMENSIONS);
  });

  it("hashes files streamingly (including 0-byte files & video duration) and reads exact chunks", async () => {
    const store = new MemoryAttachmentStore();
    const events: Event["event"][] = [];
    const exec = new AttachmentExecutor({
      store,
      onEvent: (ev) => events.push(ev),
      probeVideoDurationMs: async () => 12_345,
    });

    const payload = new Uint8Array([10, 20, 30, 40, 50, 60]);
    const fileRef = exec.stagePickedBlob(new Blob([payload], { type: "video/mp4" }));
    const emptyRef = exec.stagePickedBlob(new Blob([], { type: "text/plain" }));

    exec.handleHashFile(
      create(AttachmentHashFileEffectSchema, {
        opId: 31n,
        scope: SESSION_SCOPE,
        itemId: "f1",
        sourceRef: fileRef,
        maxBytes: 100n,
        isVideo: true,
      }),
    );
    exec.handleHashFile(
      create(AttachmentHashFileEffectSchema, {
        opId: 32n,
        scope: SESSION_SCOPE,
        itemId: "f2",
        sourceRef: emptyRef,
        maxBytes: 100n,
        isVideo: false,
      }),
    );
    exec.handleHashFile(
      create(AttachmentHashFileEffectSchema, {
        opId: 33n,
        scope: SESSION_SCOPE,
        itemId: "f3",
        sourceRef: fileRef,
        maxBytes: 3n,
        isVideo: false,
      }),
    );
    await exec.whenIdle();

    const h1 = events.find((e) => e.case === "attachmentHashFileResult" && e.value.opId === 31n);
    expect(h1?.case === "attachmentHashFileResult" && h1.value.ok).toBe(true);
    expect(h1?.case === "attachmentHashFileResult" && h1.value.sizeBytes).toBe(6n);
    expect(h1?.case === "attachmentHashFileResult" && h1.value.sha256.byteLength).toBe(32);
    expect(h1?.case === "attachmentHashFileResult" && h1.value.hasDuration).toBe(true);
    expect(h1?.case === "attachmentHashFileResult" && h1.value.durationMs).toBe(12_345n);

    const h2 = events.find((e) => e.case === "attachmentHashFileResult" && e.value.opId === 32n);
    expect(h2?.case === "attachmentHashFileResult" && h2.value.ok).toBe(true);
    expect(h2?.case === "attachmentHashFileResult" && h2.value.sizeBytes).toBe(0n);

    const h3 = events.find((e) => e.case === "attachmentHashFileResult" && e.value.opId === 33n);
    expect(h3?.case === "attachmentHashFileResult" && h3.value.ok).toBe(false);
    expect(h3?.case === "attachmentHashFileResult" && h3.value.errorKind).toBe(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_FILE_TOO_LARGE);

    // Chunk read exact slice & SOURCE_CHANGED when underlying file shrinks
    exec.handleReadChunk(
      create(AttachmentReadChunkEffectSchema, {
        opId: 41n,
        scope: SESSION_SCOPE,
        itemId: "f1",
        kind: AttachmentItemKind.FILE,
        sourceRef: fileRef,
        offset: 2n,
        length: 3,
        expectedSizeBytes: 6n,
      }),
    );
    await exec.whenIdle();
    const c1 = events.find((e) => e.case === "attachmentReadChunkResult" && e.value.opId === 41n);
    expect(c1?.case === "attachmentReadChunkResult" && c1.value.ok).toBe(true);
    expect(c1?.case === "attachmentReadChunkResult" && c1.value.data).toEqual(new Uint8Array([30, 40, 50]));

    store.replaceBlob(fileRef, new Blob([new Uint8Array([1])]));
    exec.handleReadChunk(
      create(AttachmentReadChunkEffectSchema, {
        opId: 42n,
        scope: SESSION_SCOPE,
        itemId: "f1",
        kind: AttachmentItemKind.FILE,
        sourceRef: fileRef,
        offset: 0n,
        length: 6,
        expectedSizeBytes: 6n,
      }),
    );
    await exec.whenIdle();
    const c2 = events.find((e) => e.case === "attachmentReadChunkResult" && e.value.opId === 42n);
    expect(c2?.case === "attachmentReadChunkResult" && c2.value.ok).toBe(false);
    expect(c2?.case === "attachmentReadChunkResult" && c2.value.errorKind).toBe(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED);
  });

  it("WebAttachmentStore persists encrypted records in IDB and falls back to memory when OPFS is unavailable", async () => {
    const f = await storedSecurity();
    vi.stubGlobal("IDBKeyRange", IDBKeyRange);
    const idb = indexedDB as unknown as IDBFactory;
    const store = new WebAttachmentStore({ server: "https://relay.example.com", deviceId: f.memberID }, idb);
    const opfsOk = await store.probe();
    expect(opfsOk).toBe(false);
    expect(store.hasPersistedPreparedImages).toBe(false);

    const ref = store.stageBlob(new Blob(["hello"], { type: "text/plain" }));
    await store.write(DraftStorageBucket.DRAFT, "session:s1", new Uint8Array([7, 8, 9]), [ref]);
    await store.write(DraftStorageBucket.OUTBOX, "out:s1:r1", new Uint8Array([4, 5]), [ref]);

    // A fresh WebAttachmentStore instance reads back the persisted encrypted IDB records.
    const store2 = new WebAttachmentStore({ server: "https://relay.example.com", deviceId: f.memberID }, idb);
    const listed = await store2.list();
    expect(listed.entries.map((e) => [e.bucket, e.key, e.corrupt]).sort()).toEqual([
      [DraftStorageBucket.DRAFT, "session:s1", false],
      [DraftStorageBucket.OUTBOX, "out:s1:r1", false],
    ]);
    const readBack = await store2.read(DraftStorageBucket.DRAFT, "session:s1");
    expect(readBack).toEqual({ found: true, data: new Uint8Array([7, 8, 9]) });

    await store2.clearAll();
    expect((await store2.list()).entries).toHaveLength(0);
  });

  it("real-Core smoke: prepares, chunk-uploads, sends via Outbox, restores Draft & Outbox across reload, and GCs orphan blobs", async () => {
    const { DemoRemote } = await import("./dev/demo");
    const { getCoreHost, sessionDraftScope } = await import("./core/host");
    const { CommandResultSchema } = await import("./gen/remoteagent/v1/remote_pb");

    const sharedStore = new MemoryAttachmentStore();
    const orphanRef = sharedStore.stageBlob(new Blob(["orphan-bytes"]));
    await sharedStore.write(DraftStorageBucket.DRAFT, "session:orphan-seed", new Uint8Array([1]));
    await sharedStore.delete(DraftStorageBucket.DRAFT, "session:orphan-seed");

    const r1 = DemoRemote.create(new URLSearchParams(), async () => undefined, sharedStore);
    r1.prepareImageOverride = async () => ({
      bytes: new Uint8Array([0x89, 0x50, 0x4e, 0x47, 1, 2, 3, 4]),
      format: "png",
      width: 16,
      height: 12,
      thumbBytes: new Uint8Array([0x89, 0x50, 0x4e, 0x47]),
    });
    const uploadedCmds: string[] = [];
    const origMachine = r1.machineCommand.bind(r1);
    r1.machineCommand = async (m, cmd, slot) => {
      if (cmd?.case) uploadedCmds.push(cmd.case);
      return origMachine(m, cmd, slot);
    };
    let sendResolve!: () => void;
    const sendBlocked = new Promise<void>((res) => {
      sendResolve = res;
    });
    let sentImageIds: string[] = [];
    let sentFileIds: string[] = [];
    r1.command = async (_sid, cmd) => {
      if (cmd?.case === "send") {
        sentImageIds = [...(cmd.value.imageIds ?? [])];
        sentFileIds = [...(cmd.value.fileIds ?? [])];
        await sendBlocked;
      }
      return create(CommandResultSchema);
    };

    try {
      const host1 = getCoreHost(r1);
      await host1.warmLocalCache("");
      r1.start();
      // Enable file uploads on mac
      const dm = (r1 as unknown as { demoMachines: Map<string, { meta?: { capabilities?: { fileUpload: boolean } } }> }).demoMachines.get("mac")!;
      dm.meta!.capabilities!.fileUpload = true;
      r1.machines.get("mac")!.meta!.capabilities!.fileUpload = true;
      (r1 as unknown as { syncDemoToCore(): void }).syncDemoToCore();

      // Draft 1: session s-idle with image + file, then send (leaves Outbox in flight)
      const s1 = sessionDraftScope("s-idle");
      host1.draftOpen(s1, "mac");
      await host1.attachments.whenIdle();
      host1.draftSetText(s1, "send with image and file");
      const imgFile = new File(["img-src"], "pic.png", { type: "image/png" });
      const txtFile = new File(["hello-file"], "notes.txt", { type: "text/plain" });
      host1.stageAndAddDraftItems(
        s1,
        [
          { file: imgFile, name: imgFile.name, mimeType: imgFile.type, sizeBytes: imgFile.size },
          { file: txtFile, name: txtFile.name, mimeType: txtFile.type, sizeBytes: txtFile.size },
        ],
        false,
      );
      await vi.waitFor(() => {
        const d = host1.viewAttachmentDraft(s1).draft;
        expect(d?.ready).toBe(true);
        expect(d?.items).toHaveLength(2);
      });
      expect(uploadedCmds).toContain("beginImageUpload");
      expect(uploadedCmds).toContain("putImageChunk");
      expect(uploadedCmds).toContain("completeImageUpload");

      host1.draftSendSession("s-idle", 1);
      await vi.waitFor(() => expect(sentImageIds).toHaveLength(1));
      expect(sentFileIds).toHaveLength(1);

      // Draft 2: session s-run unsent draft persisted across reload
      const s2 = sessionDraftScope("s-run");
      host1.draftOpen(s2, "mac");
      await host1.attachments.whenIdle();
      host1.draftSetText(s2, "persisted draft text");
      const img2 = new File(["img-2"], "keep.png", { type: "image/png" });
      host1.stageAndAddDraftItems(
        s2,
        [{ file: img2, name: img2.name, mimeType: img2.type, sizeBytes: img2.size }],
        false,
      );
      await vi.waitFor(() => {
        const d = host1.viewAttachmentDraft(s2).draft;
        expect(d?.ready).toBe(true);
        expect(d?.items).toHaveLength(1);
      });
      await host1.attachments.whenIdle();

      // Reload into a brand-new CoreHost sharing the same AttachmentStore
      const r2 = DemoRemote.create(new URLSearchParams(), async () => undefined, sharedStore);
      r2.prepareImageOverride = r1.prepareImageOverride;
      try {
        const host2 = getCoreHost(r2);
        await host2.warmLocalCache("");
        r2.start();
        // Open draft immediately while startup scan is still in flight (must not duplicate text).
        host2.draftOpen(s2, "mac");
        await host2.attachments.whenIdle();

        // Orphan blob from before reload should have been GC'd by Core's startup scan
        expect(await sharedStore.getLocalBlob(orphanRef)).toBeUndefined();

        // Persisted draft for s-run is restored with text + 1 image (not duplicated!)
        const restoredDraft = host2.viewAttachmentDraft(s2).draft!;
        expect(restoredDraft.text).toBe("persisted draft text");
        expect(restoredDraft.items).toHaveLength(1);

        // Pending Outbox for s-idle is restored into pending tray
        const restoredTray = host2.viewSession("s-idle").composer?.pendingTray?.orderedItems ?? [];
        expect(restoredTray.some((it) => it.text === "send with image and file" && it.attachmentCount === 2)).toBe(true);
      } finally {
        r2.dispose();
      }
    } finally {
      sendResolve();
      r1.dispose();
    }
  });

  it("WebAttachmentStore encrypts OPFS blobs in 256 KiB AES-GCM chunks bound to ref, decrypts slices in O(1), and avoids pinning prepared blobs in RAM", async () => {
    const f = await storedSecurity();
    vi.stubGlobal("IDBKeyRange", IDBKeyRange);
    const idb = indexedDB as unknown as IDBFactory;

    // Minimal in-memory fake OPFS directory that tracks slice reads to prove O(1) range decryption.
    const rawFiles = new Map<string, Uint8Array>();
    const sliceReads: Array<[number, number]> = [];
    const makeFileHandle = (name: string) => ({
      kind: "file" as const,
      name,
      async createWritable() {
        const chunks: Uint8Array[] = [];
        return {
          async write(data: Uint8Array) {
            chunks.push(new Uint8Array(data));
          },
          async close() {
            const total = chunks.reduce((n, c) => n + c.byteLength, 0);
            const merged = new Uint8Array(total);
            let off = 0;
            for (const c of chunks) {
              merged.set(c, off);
              off += c.byteLength;
            }
            rawFiles.set(name, merged);
          },
          async abort() {
            chunks.length = 0;
          },
        };
      },
      async move(newName: string) {
        const prev = rawFiles.get(name);
        if (prev) {
          rawFiles.set(newName, prev);
          rawFiles.delete(name);
        }
      },
      async getFile() {
        const bytes = rawFiles.get(name);
        if (!bytes) {
          const err = new Error("missing");
          err.name = "NotFoundError";
          throw err;
        }
        const backing = new Blob([bytes as Uint8Array<ArrayBuffer>]);
        return {
          size: backing.size,
          type: "",
          slice(start?: number, end?: number) {
            const s = start ?? 0;
            const e = end ?? backing.size;
            sliceReads.push([s, e]);
            return backing.slice(s, e);
          },
          arrayBuffer: () => backing.arrayBuffer(),
        } as unknown as File;
      },
    });
    const blobsDir = {
      kind: "directory" as const,
      name: "blobs",
      async getFileHandle(name: string, opts?: { create?: boolean }) {
        if (!opts?.create && !rawFiles.has(name)) {
          const err = new Error("missing");
          err.name = "NotFoundError";
          throw err;
        }
        return makeFileHandle(name);
      },
      async removeEntry(name: string) {
        rawFiles.delete(name);
      },
      async *entries() {
        for (const name of rawFiles.keys()) {
          yield [name, { kind: "file" as const, name }] as [string, FileSystemHandle];
        }
      },
    };
    const rootDir = {
      kind: "directory" as const,
      name: "root",
      async getDirectoryHandle() {
        return {
          kind: "directory" as const,
          name: "remote-agent-attachments",
          async getDirectoryHandle() {
            return blobsDir;
          },
        };
      },
      async removeEntry() {
        rawFiles.clear();
      },
    } as unknown as FileSystemDirectoryHandle;

    const acct = { server: "https://relay.example.com", deviceId: f.memberID };
    const store1 = new WebAttachmentStore(acct, idb, async () => rootDir);
    expect(await store1.probe()).toBe(true);
    expect(store1.hasPersistedPreparedImages).toBe(true);

    // 600 KiB payload spanning 3 encrypted chunks (256 KiB + 256 KiB + 88 KiB) with recognizable marker.
    const plain = new Uint8Array(600 * 1024);
    for (let i = 0; i < plain.length; i++) plain[i] = (i * 31 + 7) & 0xff;
    const marker = new TextEncoder().encode("TOP-SECRET-PLAINTEXT-MARKER");
    plain.set(marker, 1024);

    const ref = await store1.putLocalBlob(plain, { mimeType: "application/octet-stream", ext: "bin" });
    const rawOnDisk = rawFiles.get(ref)!;
    expect(rawOnDisk).toBeDefined();
    // Header starts with "RAB1" and ciphertext MUST NOT contain plaintext marker!
    expect(new TextDecoder().decode(rawOnDisk.subarray(0, 4))).toBe("RAB1");
    expect(new TextDecoder("latin1").decode(rawOnDisk)).not.toContain("TOP-SECRET-PLAINTEXT-MARKER");

    // Reload into a fresh WebAttachmentStore sharing IDB + OPFS -> decrypts with persisted non-extractable key.
    const store2 = new WebAttachmentStore(acct, idb, async () => rootDir);
    expect(await store2.probe()).toBe(true);

    // O(1) range read crossing chunk 0 -> chunk 1 boundary must NOT read chunk 2!
    sliceReads.length = 0;
    const sliceRes = await store2.readLocalSlice(ref, 256 * 1024 - 16, 48, plain.length);
    expect(sliceRes.kind).toBe("ok");
    if (sliceRes.kind === "ok") {
      expect(sliceRes.data).toEqual(plain.subarray(256 * 1024 - 16, 256 * 1024 + 32));
    }
    // Header (16B) + chunk 0 + chunk 1 only (total bytes read < raw file size).
    const totalSliceBytes = sliceReads.reduce((acc, [s, e]) => acc + (e - s), 0);
    expect(totalSliceBytes).toBeLessThan(rawOnDisk.byteLength);

    // Streaming hash over encrypted OPFS matches exact SHA-256 of plaintext.
    const hashed = await store2.hashLocalBlob(ref, plain.length + 1);
    expect(hashed.kind).toBe("ok");
    if (hashed.kind === "ok") {
      expect(hashed.sizeBytes).toBe(plain.length);
      const expectedSha = new Uint8Array(await crypto.subtle.digest("SHA-256", plain));
      expect(hashed.sha256).toEqual(expectedSha);
    }

    // Renaming the encrypted file to a different ref breaks AAD ref binding -> rejected!
    rawFiles.set("att_renamed.bin", rawOnDisk.slice());
    expect((await store2.readLocalSlice("att_renamed.bin", 0, 16, plain.length)).kind).toBe("changed");
    expect(await store2.getLocalBlob("att_renamed.bin")).toBeUndefined();

    // Tampering a ciphertext byte breaks GCM auth -> rejected!
    const tampered = rawOnDisk.slice();
    tampered[40] ^= 0xff;
    rawFiles.set("att_tampered.bin", tampered);
    expect((await store2.readLocalSlice("att_tampered.bin", 0, 16, plain.length)).kind).toBe("changed");

    // L8: handlePrepareImage on OPFS-backed store caches thumbRef only, NOT full preparedBlob in RAM.
    const prepEvents: Event["event"][] = [];
    const exec = new AttachmentExecutor({
      store: store2,
      onEvent: (ev) => prepEvents.push(ev),
      prepareImage: async () => ({
        bytes: new Uint8Array([0x89, 0x50, 0x4e, 0x47, 9, 8, 7, 6]),
        format: "png",
        width: 32,
        height: 24,
      }),
    });
    const srcRef = exec.stagePickedBlob(new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }));
    exec.handlePrepareImage(
      create(AttachmentPrepareImageEffectSchema, {
        opId: 91n,
        scope: SESSION_SCOPE,
        itemId: "img-opfs",
        sourceRef: srcRef,
        original: false,
        maxOriginalBytes: 20_971_520n,
        maxDimensionPx: 2048,
        maxCompressedBytes: 921_600n,
      }),
    );
    await exec.whenIdle();
    const prepEv = prepEvents.find((e) => e.case === "attachmentPrepareImageResult" && e.value.opId === 91n);
    expect(prepEv?.case === "attachmentPrepareImageResult" && prepEv.value.ok).toBe(true);
    if (prepEv?.case === "attachmentPrepareImageResult") {
      expect(exec.cachedBlob(prepEv.value.thumbRef)).toBeDefined();
      expect(exec.cachedBlob(prepEv.value.preparedRef)).toBeUndefined();
      // Viewer can still lazily decrypt preparedRef on demand via ensureBlob!
      const loaded = await exec.ensureBlob(prepEv.value.preparedRef);
      expect(new Uint8Array(await loaded!.arrayBuffer())).toEqual(new Uint8Array([0x89, 0x50, 0x4e, 0x47, 9, 8, 7, 6]));
    }

    await store2.clearAll();
    expect(rawFiles.size).toBe(0);
  });
});
