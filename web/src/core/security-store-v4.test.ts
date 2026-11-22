// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SecurityStoreV4, readV4RecordForNotify } from "./security-store-v4";
import {
  SecurityKeyDiscardEffectSchema,
  SecurityKeyKind,
  SecurityStorageEffectSchema,
  SecurityStorageOp,
  SecurityStorageWriteSchema,
  SecurityStoreBucket,
} from "../gen/remoteagent/core/v1/security_pb";
import { rawV4Buckets, storedSecurity } from "../test/stored-security-fixture";

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("SecurityStoreV4", () => {
  it("handles READ_BOOTSTRAP, WRITE_CAS conflicts, atomic BATCH_COMMIT, and DELETE_CAS", async () => {
    const idb = new IDBFactory();
    const store = new SecurityStoreV4(() => idb);

    const boot = await store.executeStorage(
      create(SecurityStorageEffectSchema, { opId: 1n, op: SecurityStorageOp.READ_BOOTSTRAP }),
    );
    expect(boot.error).toBe("");
    expect(boot.entries).toHaveLength(0);
    expect(boot.legacyCleared).toBe(false);

    const w1 = await store.executeStorage(
      create(SecurityStorageEffectSchema, {
        opId: 2n,
        op: SecurityStorageOp.WRITE_CAS,
        bucket: SecurityStoreBucket.RECORD,
        key: "record",
        data: new Uint8Array([1, 2, 3]),
        expectedRevision: "",
        nextRevision: "rev-1",
      }),
    );
    expect(w1.conflict).toBe(false);
    expect(w1.revision).toBe("rev-1");

    // Stale expectedRevision conflicts without overwriting.
    const conflict = await store.executeStorage(
      create(SecurityStorageEffectSchema, {
        opId: 3n,
        op: SecurityStorageOp.WRITE_CAS,
        bucket: SecurityStoreBucket.RECORD,
        key: "record",
        data: new Uint8Array([9, 9]),
        expectedRevision: "stale-rev",
        nextRevision: "rev-stale",
      }),
    );
    expect(conflict.conflict).toBe(true);
    const read1 = await store.executeStorage(
      create(SecurityStorageEffectSchema, {
        opId: 4n,
        op: SecurityStorageOp.READ,
        bucket: SecurityStoreBucket.RECORD,
        key: "record",
      }),
    );
    expect(read1.found).toBe(true);
    expect(read1.data).toEqual(new Uint8Array([1, 2, 3]));
    expect(read1.revision).toBe("rev-1");

    // BATCH_COMMIT atomically updates record, writes notify bucket, and deletes OPERATION on valid CAS.
    const pendingWrite = await store.executeStorage(
      create(SecurityStorageEffectSchema, {
        opId: 5n,
        op: SecurityStorageOp.WRITE_CAS,
        bucket: SecurityStoreBucket.OPERATION,
        key: "current",
        data: new Uint8Array([7]),
        expectedRevision: "",
        nextRevision: "op-rev-1",
      }),
    );
    expect(pendingWrite.conflict).toBe(false);

    const batch = await store.executeStorage(
      create(SecurityStorageEffectSchema, {
        opId: 6n,
        op: SecurityStorageOp.BATCH_COMMIT,
        batch: [
          create(SecurityStorageWriteSchema, {
            bucket: SecurityStoreBucket.RECORD,
            key: "record",
            data: new Uint8Array([4, 5, 6]),
            expectedRevision: "rev-1",
            nextRevision: "rev-2",
          }),
          create(SecurityStorageWriteSchema, {
            bucket: SecurityStoreBucket.NOTIFY_RECORD,
            key: "notify",
            data: new Uint8Array([8, 8]),
            nextRevision: "ntf-rev-1",
          }),
          create(SecurityStorageWriteSchema, {
            bucket: SecurityStoreBucket.OPERATION,
            key: "current",
            expectedRevision: "op-rev-1",
            remove: true,
          }),
        ],
      }),
    );
    expect(batch.conflict).toBe(false);
    expect(batch.error).toBe("");

    const afterBatch = await store.executeStorage(
      create(SecurityStorageEffectSchema, { opId: 7n, op: SecurityStorageOp.READ_BOOTSTRAP }),
    );
    expect(afterBatch.entries.find((e) => e.bucket === SecurityStoreBucket.RECORD)?.data).toEqual(
      new Uint8Array([4, 5, 6]),
    );
    expect(afterBatch.entries.find((e) => e.bucket === SecurityStoreBucket.NOTIFY_RECORD)?.data).toEqual(
      new Uint8Array([8, 8]),
    );
    expect(afterBatch.entries.find((e) => e.bucket === SecurityStoreBucket.OPERATION)).toBeUndefined();

    const delBad = await store.executeStorage(
      create(SecurityStorageEffectSchema, {
        opId: 8n,
        op: SecurityStorageOp.DELETE_CAS,
        bucket: SecurityStoreBucket.NOTIFY_RECORD,
        key: "notify",
        expectedRevision: "wrong",
      }),
    );
    expect(delBad.conflict).toBe(true);
  });

  it("generates non-extractable keys, signs, agrees, promotes candidate keys, and discards slots", async () => {
    const idb = new IDBFactory();
    const store = new SecurityStoreV4(() => idb);

    const edPub = new Uint8Array(await store.generateKey(SecurityKeyKind.CANDIDATE_ED, "pair-1", true));
    const xPub = new Uint8Array(await store.generateKey(SecurityKeyKind.CANDIDATE_X, "pair-1", true));
    expect(edPub).toHaveLength(32);
    expect(xPub).toHaveLength(32);

    const batch = await store.executeStorage(
      create(SecurityStorageEffectSchema, {
        opId: 1n,
        op: SecurityStorageOp.BATCH_COMMIT,
        promoteEdSlot: "pair-1",
        promoteXSlot: "pair-1",
      }),
    );
    expect(batch.error).toBe("");
    expect(batch.hasDeviceEd).toBe(true);
    expect(batch.hasDeviceX).toBe(true);
    expect(batch.deviceEdPub).toEqual(edPub);
    expect(batch.deviceXPub).toEqual(xPub);

    const msg = new TextEncoder().encode("ra-test-sign");
    const sig = new Uint8Array(await store.signKey(SecurityKeyKind.DEVICE_ED, "", msg));
    expect(sig).toHaveLength(64);
    const verKey = await crypto.subtle.importKey("raw", edPub, "Ed25519", false, ["verify"]);
    expect(await crypto.subtle.verify("Ed25519", verKey, sig, msg)).toBe(true);

    // Ephemeral X25519 agreement against peer X25519 public key.
    const peerPair = (await crypto.subtle.generateKey("X25519", true, ["deriveBits"])) as CryptoKeyPair;
    const peerRaw = new Uint8Array(await crypto.subtle.exportKey("raw", peerPair.publicKey));
    const ephPub = new Uint8Array(await store.generateKey(SecurityKeyKind.EPHEMERAL_X, "eph-1", false));
    const sharedA = await store.agreeKey(SecurityKeyKind.EPHEMERAL_X, "eph-1", peerRaw);
    const ephImported = await crypto.subtle.importKey("raw", ephPub, "X25519", false, []);
    const sharedB = new Uint8Array(
      await crypto.subtle.deriveBits({ name: "X25519", public: ephImported }, peerPair.privateKey, 256),
    );
    expect(sharedA).toEqual(sharedB);

    await store.discardKey(
      create(SecurityKeyDiscardEffectSchema, { keyKind: SecurityKeyKind.CANDIDATE_ED, slot: "pair-1" }),
    );
    await expect(store.signKey(SecurityKeyKind.CANDIDATE_ED, "pair-1", msg)).rejects.toThrow();
  });

  it("detects and wipes legacy v3/v2 databases and kv account while keeping ra-server", async () => {
    const idb = new IDBFactory();
    vi.stubGlobal("indexedDB", idb);
    localStorage.setItem("ra-server", "https://ra.example.com");

    await new Promise<void>((resolve, reject) => {
      const req = idb.open("remote-agent-security-v3", 1);
      req.onupgradeneeded = () => req.result.createObjectStore("security");
      req.onerror = () => reject(req.error);
      req.onsuccess = () => {
        const db = req.result;
        const tx = db.transaction("security", "readwrite");
        tx.objectStore("security").put({ stale: true }, "current");
        tx.oncomplete = () => {
          db.close();
          resolve();
        };
      };
    });
    await new Promise<void>((resolve, reject) => {
      const req = idb.open("remote-agent", 1);
      req.onupgradeneeded = () => req.result.createObjectStore("kv");
      req.onerror = () => reject(req.error);
      req.onsuccess = () => {
        const db = req.result;
        const tx = db.transaction("kv", "readwrite");
        tx.objectStore("kv").put({ server: "https://ra.example.com", deviceId: "old" }, "account");
        tx.oncomplete = () => {
          db.close();
          resolve();
        };
      };
    });

    const store = new SecurityStoreV4(() => idb);
    const boot1 = await store.executeStorage(
      create(SecurityStorageEffectSchema, { opId: 1n, op: SecurityStorageOp.READ_BOOTSTRAP }),
    );
    expect(boot1.legacyCleared).toBe(true);
    expect(localStorage.getItem("ra-server")).toBe("https://ra.example.com");

    const names = (await idb.databases()).map((d) => d.name);
    expect(names).not.toContain("remote-agent-security-v3");
    const legacyAccount = await new Promise<unknown>((resolve) => {
      const req = idb.open("remote-agent", 1);
      req.onsuccess = () => {
        const db = req.result;
        const tx = db.transaction("kv", "readonly");
        const g = tx.objectStore("kv").get("account");
        tx.oncomplete = () => {
          db.close();
          resolve(g.result);
        };
      };
    });
    expect(legacyAccount).toBeUndefined();

    store.dismissUpgradeNotice();
    const boot2 = await store.executeStorage(
      create(SecurityStorageEffectSchema, { opId: 2n, op: SecurityStorageOp.READ_BOOTSTRAP }),
    );
    expect(boot2.legacyCleared).toBe(false);
  });

  it("exposes persisted v4 record and non-extractable X key for Service Worker notifications", async () => {
    const f = await storedSecurity();
    const snap = await readV4RecordForNotify();
    expect(snap).toBeDefined();
    expect(snap!.recordBlob).toEqual(f.recordBlob);
    expect(snap!.xPrivate.extractable).toBe(false);

    await rawV4Buckets("readwrite", (s) => s.delete(`${SecurityStoreBucket.RECORD}:record`));
    expect(await readV4RecordForNotify()).toBeUndefined();
  });

  it("preserves ephemeral candidate keys across aborted BATCH_COMMIT so retry promotes cleanly", async () => {
    const idb = new IDBFactory();
    let abortFirstBatch = true;
    const wrappedOpen = (...args: Parameters<IDBFactory["open"]>) => {
      const req = idb.open(...args);
      return new Proxy(req, {
        get(target, prop, receiver) {
          if (prop === "result") {
            const db = target.result;
            return new Proxy(db, {
              get(dbTarget, dbProp) {
                if (dbProp === "transaction") {
                  return (...txArgs: Parameters<IDBDatabase["transaction"]>) => {
                    const tx = dbTarget.transaction(...txArgs);
                    const stores = Array.isArray(txArgs[0]) ? txArgs[0] : [txArgs[0]];
                    if (abortFirstBatch && txArgs[1] === "readwrite" && stores.includes("keys") && stores.includes("buckets")) {
                      const origObjectStore = tx.objectStore.bind(tx);
                      return new Proxy(tx, {
                        get(txTarget, txProp) {
                          if (txProp === "objectStore") {
                            return (name: string) => {
                              const os = origObjectStore(name);
                              if (name !== "buckets") return os;
                              return new Proxy(os, {
                                get(osTarget, osProp) {
                                  if (osProp === "put") {
                                    return (...putArgs: Parameters<IDBObjectStore["put"]>) => {
                                      if (abortFirstBatch) {
                                        abortFirstBatch = false;
                                        txTarget.abort();
                                      }
                                      return osTarget.put(...putArgs);
                                    };
                                  }
                                  const v = Reflect.get(osTarget, osProp);
                                  return typeof v === "function" ? v.bind(osTarget) : v;
                                },
                              });
                            };
                          }
                          const tv = Reflect.get(txTarget, txProp);
                          return typeof tv === "function" ? tv.bind(txTarget) : tv;
                        },
                      });
                    }
                    return tx;
                  };
                }
                const dv = Reflect.get(dbTarget, dbProp);
                return typeof dv === "function" ? dv.bind(dbTarget) : dv;
              },
            });
          }
          const rv = Reflect.get(target, prop, receiver);
          return typeof rv === "function" ? rv.bind(target) : rv;
        },
      });
    };
    const fakeIdb = new Proxy(idb, {
      get(target, prop) {
        if (prop === "open") return wrappedOpen;
        const v = Reflect.get(target, prop);
        return typeof v === "function" ? v.bind(target) : v;
      },
    }) as IDBFactory;

    const store = new SecurityStoreV4(() => fakeIdb);
    const edPub = new Uint8Array(await store.generateKey(SecurityKeyKind.CANDIDATE_ED, "slot-retry", false));
    const xPub = new Uint8Array(await store.generateKey(SecurityKeyKind.CANDIDATE_X, "slot-retry", false));

    const batchEffect = create(SecurityStorageEffectSchema, {
      opId: 1n,
      op: SecurityStorageOp.BATCH_COMMIT,
      promoteEdSlot: "slot-retry",
      promoteXSlot: "slot-retry",
      batch: [
        {
          bucket: SecurityStoreBucket.RECORD,
          key: "record",
          nextRevision: "rec-1",
          data: new TextEncoder().encode("rec-v1"),
        },
      ],
    });

    const first = await store.executeStorage(batchEffect);
    expect(first.error).not.toBe("");

    // Ephemeral candidate slots must survive the aborted transaction so retry can promote them.
    const probe = new TextEncoder().encode("probe");
    await expect(store.signKey(SecurityKeyKind.CANDIDATE_ED, "slot-retry", probe)).resolves.toBeInstanceOf(Uint8Array);

    const retried = await store.executeStorage(batchEffect);
    expect(retried.error).toBe("");
    expect(retried.hasDeviceEd).toBe(true);
    expect(retried.deviceEdPub).toEqual(edPub);
    expect(retried.deviceXPub).toEqual(xPub);
    await expect(store.signKey(SecurityKeyKind.CANDIDATE_ED, "slot-retry", probe)).rejects.toThrow();
  });
});
