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
});
