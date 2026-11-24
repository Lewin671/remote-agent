import { create } from "@bufbuild/protobuf";
import { clearEventCache } from "../eventcache";
import {
  SecurityEncapsulatedPairSchema,
  SecurityKeyKind,
  SecurityStorageEffectSchema,
  SecurityStorageEntrySchema,
  SecurityStorageOp,
  SecurityStorageResultEventSchema,
  SecurityStoreBucket,
  type SecurityEncapsulatedPair,
  type SecurityEncapsulateRecipient,
  type SecurityKeyDiscardEffect,
  type SecurityStorageEffect,
  type SecurityStorageResultEvent,
} from "../gen/remoteagent/core/v1/security_pb";

type B = Uint8Array<ArrayBuffer>;

export const V4_DB_NAME = "remote-agent-security-v4";
export const V4_STORE_BUCKETS = "buckets";
export const V4_STORE_KEYS = "keys";
export const V4_STORE_META = "meta";

const LEGACY_DBS = ["remote-agent-security-v3", "remote-agent-security-v2"] as const;
const LEGACY_ACCOUNT_DB = "remote-agent";
const LEGACY_ACCOUNT_STORE = "kv";
const UPGRADE_NOTICE_KEY = "ra-upgrade-repair-v4";
const LOGIN_TOKEN_KEY = "login-token";
const RECORD_ID = `${SecurityStoreBucket.RECORD}:record`;

export class CorruptLocalContent extends Error {}
export interface LoginToken {
  token: string;
  expiresMs: number;
}

interface StoredBucketRow {
  bucket: SecurityStoreBucket;
  key: string;
  revision: string;
  data: Uint8Array;
}

interface StoredKeyRow {
  privateKey: CryptoKey;
  publicKey: Uint8Array;
}

interface SealedLocalValue {
  binding: string;
  key: CryptoKey;
  iv: B;
  ciphertext: ArrayBuffer;
}

const toAB = (src: Uint8Array): B => new Uint8Array(src);
const bucketId = (bucket: SecurityStoreBucket, key: string) => `${bucket}:${key}`;
const keyId = (kind: SecurityKeyKind, slot: string) => `${kind}:${slot}`;

function parseBucketId(id: string): { bucket: SecurityStoreBucket; key: string } | undefined {
  const idx = id.indexOf(":");
  return idx <= 0 ? undefined : { bucket: Number(id.slice(0, idx)) as SecurityStoreBucket, key: id.slice(idx + 1) };
}

function keyAlgForKind(kind: SecurityKeyKind): "Ed25519" | "X25519" {
  if (kind === SecurityKeyKind.DEVICE_ED || kind === SecurityKeyKind.CANDIDATE_ED) return "Ed25519";
  if (kind === SecurityKeyKind.DEVICE_X || kind === SecurityKeyKind.CANDIDATE_X || kind === SecurityKeyKind.EPHEMERAL_X) return "X25519";
  throw new Error("unsupported key kind");
}

function openV4DB(idb: IDBFactory = indexedDB): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = idb.open(V4_DB_NAME, 1);
    req.onupgradeneeded = () => {
      for (const name of [V4_STORE_BUCKETS, V4_STORE_KEYS, V4_STORE_META]) {
        if (!req.result.objectStoreNames.contains(name)) req.result.createObjectStore(name);
      }
    };
    req.onerror = () => reject(req.error ?? new Error("indexeddb open failed"));
    req.onsuccess = () => {
      req.result.onversionchange = () => req.result.close();
      resolve(req.result);
    };
  });
}

async function withDB<T>(idb: IDBFactory, run: (db: IDBDatabase) => Promise<T>): Promise<T> {
  const db = await openV4DB(idb);
  try {
    return await run(db);
  } finally {
    db.close();
  }
}

function runTx<T>(
  db: IDBDatabase,
  stores: string | string[],
  mode: IDBTransactionMode,
  failMsg: string,
  setup: (tx: IDBTransaction) => () => T,
): Promise<T> {
  return new Promise((resolve, reject) => {
    const tx = mode === "readwrite" ? db.transaction(stores, mode, { durability: "strict" }) : db.transaction(stores, mode);
    const read = setup(tx);
    tx.oncomplete = () => resolve(read());
    tx.onabort = tx.onerror = () => reject(tx.error ?? new Error(failMsg));
  });
}

function tryGetLocalFlag(): boolean {
  try {
    return typeof localStorage !== "undefined" && localStorage.getItem(UPGRADE_NOTICE_KEY) === "1";
  } catch {
    return false;
  }
}

function trySetLocalFlag(on: boolean): void {
  try {
    if (typeof localStorage === "undefined") return;
    if (on) localStorage.setItem(UPGRADE_NOTICE_KEY, "1");
    else localStorage.removeItem(UPGRADE_NOTICE_KEY);
  } catch { /* ignore restricted storage contexts */ }
}

let logoutChannel: BroadcastChannel | undefined;

/** Stops every open copy of this browser account after an explicit logout. */
export function watchAccountLogout(onLogout: () => void): () => void {
  const channel = new BroadcastChannel("ra-account-logout");
  logoutChannel = channel;
  channel.onmessage = (e) => e.data === "logout" && onLogout();
  return () => {
    channel.close();
    if (logoutChannel === channel) logoutChannel = undefined;
  };
}

function broadcastLogout(): void {
  try {
    if (logoutChannel) {
      logoutChannel.postMessage("logout");
      return;
    }
    if (typeof BroadcastChannel !== "undefined") {
      const ch = new BroadcastChannel("ra-account-logout");
      ch.postMessage("logout");
      ch.close();
    }
  } catch { /* ignore */ }
}

const activeSharedFileClearers = new Set<() => Promise<void>>();
const activeAttachmentClearers = new Set<() => Promise<void>>();

/** Registers an active CoreHost's queued shared-file ClearAll handler for logout coordination. */
export function registerActiveSharedFileClearer(fn: () => Promise<void>): () => void {
  activeSharedFileClearers.add(fn);
  return () => { activeSharedFileClearers.delete(fn); };
}

/** Registers an active CoreHost's queued attachment/draft ClearAll handler for logout coordination. */
export function registerActiveAttachmentClearer(fn: () => Promise<void>): () => void {
  activeAttachmentClearers.add(fn);
  return () => { activeAttachmentClearers.delete(fn); };
}

async function clearAccountCaches(): Promise<void> {
  await clearEventCache().catch(() => undefined);
  const sharedClear =
    activeSharedFileClearers.size > 0
      ? Promise.all([...activeSharedFileClearers].map((fn) => fn().catch(() => undefined)))
      : import("../shared-file-store").then((m) => m.sharedFileStore.clearAll()).catch(() => undefined);
  const attClear =
    activeAttachmentClearers.size > 0
      ? Promise.all([...activeAttachmentClearers].map((fn) => fn().catch(() => undefined)))
      : import("../attachment-store").then((m) => m.clearDefaultAttachmentOpfs()).catch(() => undefined);
  await Promise.all([sharedClear, attClear]);
}

function isCasConflict(cur: StoredBucketRow | undefined, expectedRevision: string, createOnly: boolean, remove = false): boolean {
  if (remove) return expectedRevision !== "" && !!cur && cur.revision !== expectedRevision;
  return (createOnly && !!cur) || (expectedRevision !== "" && (!cur || cur.revision !== expectedRevision));
}

function readPubsFromTx(tx: IDBTransaction) {
  const kStore = tx.objectStore(V4_STORE_KEYS);
  const edReq = kStore.get(keyId(SecurityKeyKind.DEVICE_ED, "")) as IDBRequest<StoredKeyRow | undefined>;
  const xReq = kStore.get(keyId(SecurityKeyKind.DEVICE_X, "")) as IDBRequest<StoredKeyRow | undefined>;
  return () => {
    const edRow = edReq.result;
    const xRow = xReq.result;
    return {
      hasDeviceEd: !!edRow?.privateKey,
      deviceEdPub: edRow?.publicKey ? toAB(edRow.publicKey) : new Uint8Array(),
      hasDeviceX: !!xRow?.privateKey,
      deviceXPub: xRow?.publicKey ? toAB(xRow.publicKey) : new Uint8Array(),
    };
  };
}

/**
 * Pure dumb v4 IndexedDB + non-extractable WebCrypto store.
 * All security protocol rules, verification, and workflow state transitions live in Go clientcore.
 */
export class SecurityStoreV4 {
  private readonly ephemeralKeys = new Map<string, StoredKeyRow>();
  private legacyClearedFlag = tryGetLocalFlag();

  constructor(private readonly getIDB: () => IDBFactory = () => indexedDB) {}

  private idbOrUndefined(): IDBFactory | undefined {
    try { return this.getIDB(); } catch { return undefined; }
  }

  private async probeDatabaseExists(name: string, idb: IDBFactory): Promise<boolean> {
    return new Promise((resolve) => {
      try {
        const req = idb.open(name);
        let existed = true;
        req.onupgradeneeded = () => {
          existed = false;
          try { req.transaction?.abort(); } catch { /* ignore */ }
        };
        req.onerror = () => resolve(false);
        req.onsuccess = () => {
          req.result.close();
          resolve(existed);
        };
      } catch {
        resolve(false);
      }
    });
  }

  /** Detects legacy v3/v2 IndexedDB state or legacy kv account, wipes them while keeping ra-server, and records the repair notice flag. */
  async detectAndClearLegacySecurityV3(): Promise<boolean> {
    const idb = this.idbOrUndefined();
    if (!idb) return this.legacyClearedFlag;
    let listedNames: Set<string> | undefined;
    try {
      if (typeof idb.databases === "function") {
        listedNames = new Set((await idb.databases()).flatMap((d) => (d.name ? [d.name] : [])));
      }
    } catch { listedNames = undefined; }

    let foundLegacy = false;
    for (const legacyDb of LEGACY_DBS) {
      if (listedNames ? listedNames.has(legacyDb) : await this.probeDatabaseExists(legacyDb, idb)) {
        foundLegacy = true;
        await new Promise<void>((resolve) => {
          try {
            const req = idb.deleteDatabase(legacyDb);
            req.onsuccess = req.onerror = req.onblocked = () => resolve();
          } catch { resolve(); }
        });
      }
    }

    if (listedNames ? listedNames.has(LEGACY_ACCOUNT_DB) : await this.probeDatabaseExists(LEGACY_ACCOUNT_DB, idb)) {
      const cleared = await new Promise<boolean>((resolve) => {
        try {
          const req = idb.open(LEGACY_ACCOUNT_DB);
          req.onupgradeneeded = () => req.transaction?.abort();
          req.onerror = () => resolve(false);
          req.onsuccess = () => {
            const db = req.result;
            db.onversionchange = () => db.close();
            if (!db.objectStoreNames.contains(LEGACY_ACCOUNT_STORE)) {
              db.close();
              resolve(false);
              return;
            }
            let saw = false;
            const tx = db.transaction(LEGACY_ACCOUNT_STORE, "readwrite");
            const cur = tx.objectStore(LEGACY_ACCOUNT_STORE).openCursor();
            cur.onsuccess = () => {
              const e = cur.result;
              if (!e) return;
              const k = e.key;
              if (k === "account" || k === "registration" || (typeof k === "string" && k.startsWith("account-public/"))) {
                saw = true;
                e.delete();
              }
              e.continue();
            };
            tx.oncomplete = () => { db.close(); resolve(saw); };
            tx.onabort = tx.onerror = () => { db.close(); resolve(false); };
          };
        } catch { resolve(false); }
      });
      if (cleared) foundLegacy = true;
    }

    if (foundLegacy) {
      this.legacyClearedFlag = true;
      trySetLocalFlag(true);
      await clearAccountCaches();
    }
    return this.legacyClearedFlag || tryGetLocalFlag();
  }

  dismissUpgradeNotice(): void {
    this.legacyClearedFlag = false;
    trySetLocalFlag(false);
  }

  async generateKey(kind: SecurityKeyKind, slot: string, persist: boolean): Promise<Uint8Array> {
    const alg = keyAlgForKind(kind);
    const pair = (await crypto.subtle.generateKey({ name: alg }, false, alg === "Ed25519" ? ["sign", "verify"] : ["deriveBits"])) as CryptoKeyPair;
    const publicKey = new Uint8Array(await crypto.subtle.exportKey("raw", pair.publicKey));
    const row: StoredKeyRow = { privateKey: pair.privateKey, publicKey };
    if (!persist || kind === SecurityKeyKind.EPHEMERAL_X) {
      this.ephemeralKeys.set(keyId(kind, slot), row);
      return publicKey;
    }
    await withDB(this.getIDB(), (db) =>
      runTx(db, V4_STORE_KEYS, "readwrite", "key store write failed", (tx) => {
        tx.objectStore(V4_STORE_KEYS).put(row, keyId(kind, slot));
        return () => undefined;
      }),
    );
    return publicKey;
  }

  private async loadKey(kind: SecurityKeyKind, slot: string): Promise<StoredKeyRow | undefined> {
    const id = keyId(kind, slot);
    const mem = this.ephemeralKeys.get(id);
    if (mem || kind === SecurityKeyKind.EPHEMERAL_X) return mem;
    return withDB(this.getIDB(), (db) =>
      runTx(db, V4_STORE_KEYS, "readonly", "key store read failed", (tx) => {
        const req = tx.objectStore(V4_STORE_KEYS).get(id) as IDBRequest<StoredKeyRow | undefined>;
        return () => req.result;
      }),
    );
  }

  async signKey(kind: SecurityKeyKind, slot: string, message: Uint8Array): Promise<Uint8Array> {
    const row = await this.loadKey(kind, slot);
    if (!row?.privateKey) throw new Error("missing signing key");
    return new Uint8Array(await crypto.subtle.sign("Ed25519", row.privateKey, toAB(message)));
  }

  async agreeKey(kind: SecurityKeyKind, slot: string, peerPublic: Uint8Array): Promise<Uint8Array> {
    const row = await this.loadKey(kind, slot);
    if (!row?.privateKey || peerPublic.byteLength !== 32) throw new Error("missing agreement key or invalid peer public key");
    const peer = await crypto.subtle.importKey("raw", toAB(peerPublic), "X25519", false, []);
    return new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: peer }, row.privateKey, 256));
  }

  async encapsulate(recipients: SecurityEncapsulateRecipient[]): Promise<SecurityEncapsulatedPair[]> {
    return Promise.all(
      recipients.map(async (r) => {
        if (r.xPublic.byteLength !== 32) throw new Error("invalid recipient x25519 public key");
        const eph = (await crypto.subtle.generateKey({ name: "X25519" }, false, ["deriveBits"])) as CryptoKeyPair;
        const enc = new Uint8Array(await crypto.subtle.exportKey("raw", eph.publicKey));
        const peer = await crypto.subtle.importKey("raw", toAB(r.xPublic), "X25519", false, []);
        const dh = new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: peer }, eph.privateKey, 256));
        return create(SecurityEncapsulatedPairSchema, { memberId: r.memberId, enc, dh });
      }),
    );
  }

  async discardKey(eff: SecurityKeyDiscardEffect): Promise<void> {
    this.ephemeralKeys.delete(keyId(eff.keyKind, eff.slot));
    if (eff.keyKind === SecurityKeyKind.EPHEMERAL_X) return;
    await withDB(this.getIDB(), (db) =>
      runTx(db, V4_STORE_KEYS, "readwrite", "key discard failed", (tx) => {
        tx.objectStore(V4_STORE_KEYS).delete(keyId(eff.keyKind, eff.slot));
        return () => undefined;
      }),
    );
  }

  async executeStorage(eff: SecurityStorageEffect): Promise<SecurityStorageResultEvent> {
    if (eff.op === SecurityStorageOp.CLEAR_ALL) {
      this.ephemeralKeys.clear();
      this.dismissUpgradeNotice();
      await withDB(this.getIDB(), (db) =>
        runTx(db, [V4_STORE_BUCKETS, V4_STORE_KEYS, V4_STORE_META], "readwrite", "clear all failed", (tx) => {
          for (const s of [V4_STORE_BUCKETS, V4_STORE_KEYS, V4_STORE_META]) tx.objectStore(s).clear();
          return () => undefined;
        }),
      );
      await clearAccountCaches();
      broadcastLogout();
      return create(SecurityStorageResultEventSchema, { opId: eff.opId });
    }

    return withDB(this.getIDB(), async (db) => {
      if (eff.op === SecurityStorageOp.READ) {
        return runTx(db, [V4_STORE_BUCKETS, V4_STORE_KEYS], "readonly", "storage read failed", (tx) => {
          const bReq = tx.objectStore(V4_STORE_BUCKETS).get(bucketId(eff.bucket, eff.key)) as IDBRequest<StoredBucketRow | undefined>;
          const readPubs = readPubsFromTx(tx);
          return () => {
            const row = bReq.result;
            return create(SecurityStorageResultEventSchema, {
              opId: eff.opId,
              found: !!row,
              revision: row?.revision ?? "",
              data: row?.data ? toAB(row.data) : new Uint8Array(),
              ...readPubs(),
            });
          };
        });
      }

      if (eff.op === SecurityStorageOp.READ_BOOTSTRAP) {
        const res = await runTx(db, [V4_STORE_BUCKETS, V4_STORE_KEYS], "readonly", "bootstrap read failed", (tx) => {
          const bStore = tx.objectStore(V4_STORE_BUCKETS);
          const allReq = bStore.getAll() as IDBRequest<StoredBucketRow[]>;
          const keysReq = bStore.getAllKeys() as IDBRequest<string[]>;
          const readPubs = readPubsFromTx(tx);
          return () => {
            const entries: StoredBucketRow[] = [];
            const vals = allReq.result ?? [];
            const keys = keysReq.result ?? [];
            for (let i = 0; i < vals.length; i++) {
              const parsed = typeof keys[i] === "string" ? parseBucketId(keys[i]) : undefined;
              const bucket = vals[i].bucket || parsed?.bucket || SecurityStoreBucket.UNSPECIFIED;
              const key = vals[i].key || parsed?.key || "";
              if (eff.bucket !== SecurityStoreBucket.UNSPECIFIED && bucket !== eff.bucket) continue;
              entries.push({ bucket, key, revision: vals[i].revision, data: vals[i].data });
            }
            return { entries, pubs: readPubs() };
          };
        });
        const hasV4 = res.entries.some(
          (e) =>
            e.data.byteLength > 0 &&
            (e.bucket === SecurityStoreBucket.RECORD || e.bucket === SecurityStoreBucket.OPERATION || e.bucket === SecurityStoreBucket.REGISTRATION_INTENT),
        );
        const legacyCleared = !hasV4 ? await this.detectAndClearLegacySecurityV3() : (this.dismissUpgradeNotice(), false);
        return create(SecurityStorageResultEventSchema, {
          opId: eff.opId,
          entries: res.entries.map((e) => create(SecurityStorageEntrySchema, { bucket: e.bucket, key: e.key, revision: e.revision, data: toAB(e.data) })),
          legacyCleared,
          ...res.pubs,
        });
      }

      if (eff.op === SecurityStorageOp.WRITE_CAS || eff.op === SecurityStorageOp.DELETE_CAS) {
        return runTx(db, [V4_STORE_BUCKETS, V4_STORE_KEYS], "readwrite", "cas write failed", (tx) => {
          const store = tx.objectStore(V4_STORE_BUCKETS);
          const id = bucketId(eff.bucket, eff.key);
          const getReq = store.get(id) as IDBRequest<StoredBucketRow | undefined>;
          const readPubs = readPubsFromTx(tx);
          let conflict = false;
          let dismissNotice = false;
          getReq.onsuccess = () => {
            conflict = isCasConflict(getReq.result, eff.expectedRevision, eff.createOnly, eff.op === SecurityStorageOp.DELETE_CAS);
            if (conflict) return;
            if (eff.op === SecurityStorageOp.DELETE_CAS) {
              store.delete(id);
            } else {
              store.put({ bucket: eff.bucket, key: eff.key, revision: eff.nextRevision, data: toAB(eff.data) } satisfies StoredBucketRow, id);
              if (eff.bucket === SecurityStoreBucket.RECORD) dismissNotice = true;
            }
          };
          return () => {
            if (dismissNotice) this.dismissUpgradeNotice();
            return create(SecurityStorageResultEventSchema, {
              opId: eff.opId,
              conflict,
              revision: conflict || eff.op === SecurityStorageOp.DELETE_CAS ? "" : eff.nextRevision,
              ...readPubs(),
            });
          };
        });
      }

      if (eff.op === SecurityStorageOp.BATCH_COMMIT) {
        return new Promise<SecurityStorageResultEvent>((resolve, reject) => {
          const tx = db.transaction([V4_STORE_BUCKETS, V4_STORE_KEYS, V4_STORE_META], "readwrite", { durability: "strict" });
          const bStore = tx.objectStore(V4_STORE_BUCKETS);
          const kStore = tx.objectStore(V4_STORE_KEYS);
          const mStore = tx.objectStore(V4_STORE_META);
          let conflict = false;
          let outcomeErr = "";
          let onCommitted: (() => void) | undefined;
          let readPubs: ReturnType<typeof readPubsFromTx> | undefined;

          const curReqs = eff.batch.map((w) => bStore.get(bucketId(w.bucket, w.key)) as IDBRequest<StoredBucketRow | undefined>);
          const candEdId = eff.promoteEdSlot ? keyId(SecurityKeyKind.CANDIDATE_ED, eff.promoteEdSlot) : "";
          const candXId = eff.promoteXSlot ? keyId(SecurityKeyKind.CANDIDATE_X, eff.promoteXSlot) : "";
          const edCandReq = candEdId ? (kStore.get(candEdId) as IDBRequest<StoredKeyRow | undefined>) : undefined;
          const xCandReq = candXId ? (kStore.get(candXId) as IDBRequest<StoredKeyRow | undefined>) : undefined;
          let pendingReads = curReqs.length + (edCandReq ? 1 : 0) + (xCandReq ? 1 : 0);

          const applyCommit = () => {
            try {
              conflict = eff.batch.some((w, i) => isCasConflict(curReqs[i].result, w.expectedRevision, w.createOnly, w.remove));
              if (conflict) {
                readPubs = readPubsFromTx(tx);
                return;
              }
              const ephemeralDeletes: string[] = [];
              let recordCommitted = false;
              const promote = (slot: string, id: string, req: IDBRequest<StoredKeyRow | undefined> | undefined, target: SecurityKeyKind, label: string) => {
                if (!slot) return;
                const cand = req?.result ?? this.ephemeralKeys.get(id);
                if (!cand?.privateKey) throw new Error(`missing promote ${label} slot`);
                kStore.put(cand, keyId(target, ""));
                kStore.delete(id);
                ephemeralDeletes.push(id);
              };
              promote(eff.promoteEdSlot, candEdId, edCandReq, SecurityKeyKind.DEVICE_ED, "ed");
              promote(eff.promoteXSlot, candXId, xCandReq, SecurityKeyKind.DEVICE_X, "x");
              for (const w of eff.batch) {
                const id = bucketId(w.bucket, w.key);
                if (w.remove) bStore.delete(id);
                else {
                  bStore.put({ bucket: w.bucket, key: w.key, revision: w.nextRevision, data: toAB(w.data) } satisfies StoredBucketRow, id);
                  if (w.bucket === SecurityStoreBucket.RECORD) recordCommitted = true;
                }
              }
              for (const slot of eff.discardSlots) {
                for (const kind of [SecurityKeyKind.CANDIDATE_ED, SecurityKeyKind.CANDIDATE_X, SecurityKeyKind.EPHEMERAL_X]) {
                  ephemeralDeletes.push(keyId(kind, slot));
                  if (kind !== SecurityKeyKind.EPHEMERAL_X) kStore.delete(keyId(kind, slot));
                }
              }
              if (eff.clearLoginToken) mStore.delete(LOGIN_TOKEN_KEY);
              onCommitted = () => {
                for (const id of ephemeralDeletes) this.ephemeralKeys.delete(id);
                if (recordCommitted) this.dismissUpgradeNotice();
              };
              readPubs = readPubsFromTx(tx);
            } catch (e) {
              outcomeErr = e instanceof Error ? e.message : String(e);
              try { tx.abort(); } catch { /* ignore */ }
            }
          };

          const onReadDone = () => { if (--pendingReads === 0) applyCommit(); };
          if (pendingReads === 0) applyCommit();
          else {
            for (const req of curReqs) req.onsuccess = onReadDone;
            if (edCandReq) edCandReq.onsuccess = onReadDone;
            if (xCandReq) xCandReq.onsuccess = onReadDone;
          }
          tx.oncomplete = () => {
            onCommitted?.();
            resolve(create(SecurityStorageResultEventSchema, {
              opId: eff.opId,
              conflict,
              error: outcomeErr,
              ...(readPubs ? readPubs() : {}),
            }));
          };
          tx.onabort = tx.onerror = () => {
            if (outcomeErr) resolve(create(SecurityStorageResultEventSchema, { opId: eff.opId, error: outcomeErr }));
            else reject(tx.error ?? new Error("batch commit failed"));
          };
        });
      }

      return create(SecurityStorageResultEventSchema, { opId: eff.opId, error: "unsupported storage op" });
    });
  }
}

export const securityStoreV4 = new SecurityStoreV4();

async function sealLocalValue(binding: string, bytes: Uint8Array): Promise<SealedLocalValue> {
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const ciphertext = await crypto.subtle.encrypt({ name: "AES-GCM", iv, additionalData: new TextEncoder().encode(binding) }, key, toAB(bytes));
  return { binding, key, iv, ciphertext };
}

async function openLocalValue(stored: SealedLocalValue): Promise<Uint8Array> {
  if (!(stored.key instanceof CryptoKey) || stored.key.extractable) throw new Error("corrupt wrapped key");
  return new Uint8Array(await crypto.subtle.decrypt({ name: "AES-GCM", iv: stored.iv, additionalData: new TextEncoder().encode(stored.binding) }, stored.key, stored.ciphertext));
}

function readMetaIfHasRecord(db: IDBDatabase, metaKey: string, failMsg: string): Promise<SealedLocalValue | undefined> {
  return runTx(db, [V4_STORE_BUCKETS, V4_STORE_META], "readonly", failMsg, (tx) => {
    const bReq = tx.objectStore(V4_STORE_BUCKETS).get(RECORD_ID) as IDBRequest<StoredBucketRow | undefined>;
    const mReq = tx.objectStore(V4_STORE_META).get(metaKey) as IDBRequest<SealedLocalValue | undefined>;
    return () => (bReq.result?.data.byteLength ? mReq.result : undefined);
  });
}

function writeMetaIfHasRecord(failMsg: string, abortOnMissing: boolean, apply: (mStore: IDBObjectStore) => void): Promise<void> {
  return withDB(indexedDB, (db) =>
    runTx(db, [V4_STORE_BUCKETS, V4_STORE_META], "readwrite", failMsg, (tx) => {
      const bReq = tx.objectStore(V4_STORE_BUCKETS).get(RECORD_ID) as IDBRequest<StoredBucketRow | undefined>;
      bReq.onsuccess = () => {
        if (bReq.result?.data.byteLength) apply(tx.objectStore(V4_STORE_META));
        else if (abortOnMissing) tx.abort();
      };
      return () => undefined;
    }),
  );
}

export async function saveLoginToken(server: string, memberID: string, token: LoginToken): Promise<void> {
  const sealed = await sealLocalValue(JSON.stringify([server, memberID]), new TextEncoder().encode(JSON.stringify(token)));
  await writeMetaIfHasRecord("save login token failed", false, (mStore) => mStore.put(sealed, LOGIN_TOKEN_KEY));
}

export async function clearLoginToken(server: string, memberID: string): Promise<void> {
  const binding = JSON.stringify([server, memberID]);
  await withDB(indexedDB, (db) =>
    new Promise<void>((resolve) => {
      const tx = db.transaction(V4_STORE_META, "readwrite", { durability: "strict" });
      const mStore = tx.objectStore(V4_STORE_META);
      const getReq = mStore.get(LOGIN_TOKEN_KEY) as IDBRequest<SealedLocalValue | undefined>;
      getReq.onsuccess = () => { if (getReq.result?.binding === binding) mStore.delete(LOGIN_TOKEN_KEY); };
      tx.oncomplete = tx.onabort = tx.onerror = () => resolve();
    }),
  );
}

export async function loadLoginToken(server: string, memberID: string, now = Date.now()): Promise<LoginToken | undefined> {
  const binding = JSON.stringify([server, memberID]);
  const stored = await withDB(indexedDB, (db) => readMetaIfHasRecord(db, LOGIN_TOKEN_KEY, "load login token failed"));
  if (!stored || stored.binding !== binding) return undefined;
  try {
    const token = JSON.parse(new TextDecoder().decode(await openLocalValue(stored))) as LoginToken;
    if (typeof token.token !== "string" || !token.token || !Number.isFinite(token.expiresMs) || token.expiresMs < now + 86_400_000) {
      await clearLoginToken(server, memberID);
      return undefined;
    }
    return token;
  } catch {
    await clearLoginToken(server, memberID);
    return undefined;
  }
}

export async function saveLocalData(server: string, memberID: string, values: Map<string, Uint8Array | undefined>): Promise<void> {
  const owner = JSON.stringify([server, memberID]);
  const sealed = await Promise.all([...values].map(async ([n, b]) => [n, b === undefined ? undefined : await sealLocalValue(JSON.stringify([owner, n]), b)] as const));
  await writeMetaIfHasRecord("save local data failed", true, (mStore) => {
    for (const [n, v] of sealed) v === undefined ? mStore.delete("local-content:" + n) : mStore.put(v, "local-content:" + n);
  });
}

export async function removeLocalData(_server: string, _memberID: string, name: string): Promise<void> {
  await withDB(indexedDB, (db) =>
    runTx(db, V4_STORE_META, "readwrite", "remove local data failed", (tx) => {
      const mStore = tx.objectStore(V4_STORE_META);
      const key = "local-content:" + name;
      mStore.delete(key);
      const cur = mStore.openCursor(IDBKeyRange.bound(key + "/", key + "/\uffff"));
      cur.onsuccess = () => {
        if (cur.result) {
          cur.result.delete();
          cur.result.continue();
        }
      };
      return () => undefined;
    }),
  );
}

export async function loadLocalData(server: string, memberID: string, name: string): Promise<Uint8Array | undefined> {
  const binding = JSON.stringify([JSON.stringify([server, memberID]), name]);
  const stored = await withDB(indexedDB, (db) => readMetaIfHasRecord(db, "local-content:" + name, "load local data failed"));
  if (!stored) return undefined;
  if (stored.binding !== binding || !(stored.key instanceof CryptoKey) || stored.key.extractable) throw new CorruptLocalContent();
  try {
    return await openLocalValue(stored);
  } catch {
    throw new CorruptLocalContent();
  }
}

const DRAFT_V1_PREFIX = "draft-v1:";
const BLOB_KEY_META_KEY = `${DRAFT_V1_PREFIX}blob-key`;

interface StoredAttachmentBlobKey {
  binding: string;
  key: CryptoKey;
}

export async function loadAttachmentBlobKey(
  server: string,
  memberID: string,
  idb: IDBFactory = indexedDB,
): Promise<CryptoKey | undefined> {
  const owner = JSON.stringify([server, memberID]);
  const stored = await withDB(idb, (db) =>
    runTx(db, [V4_STORE_BUCKETS, V4_STORE_META], "readonly", "load attachment blob key failed", (tx) => {
      const bReq = tx.objectStore(V4_STORE_BUCKETS).get(RECORD_ID) as IDBRequest<StoredBucketRow | undefined>;
      const mReq = tx.objectStore(V4_STORE_META).get(BLOB_KEY_META_KEY) as IDBRequest<StoredAttachmentBlobKey | undefined>;
      return () => (bReq.result?.data.byteLength ? mReq.result : undefined);
    }),
  );
  if (!stored || stored.binding !== owner || !(stored.key instanceof CryptoKey) || stored.key.extractable) {
    return undefined;
  }
  return stored.key;
}

export async function getOrCreateAttachmentBlobKey(
  server: string,
  memberID: string,
  idb: IDBFactory = indexedDB,
): Promise<CryptoKey> {
  const existing = await loadAttachmentBlobKey(server, memberID, idb);
  if (existing) return existing;
  const owner = JSON.stringify([server, memberID]);
  const fresh = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  return withDB(idb, (db) =>
    runTx(db, [V4_STORE_BUCKETS, V4_STORE_META], "readwrite", "save attachment blob key failed", (tx) => {
      const bStore = tx.objectStore(V4_STORE_BUCKETS);
      const mStore = tx.objectStore(V4_STORE_META);
      const bReq = bStore.get(RECORD_ID) as IDBRequest<StoredBucketRow | undefined>;
      const mReq = mStore.get(BLOB_KEY_META_KEY) as IDBRequest<StoredAttachmentBlobKey | undefined>;
      let chosen = fresh;
      let pending = 2;
      const decide = () => {
        if (--pending !== 0) return;
        if (!bReq.result?.data.byteLength) {
          tx.abort();
          return;
        }
        const cur = mReq.result;
        if (cur && cur.binding === owner && cur.key instanceof CryptoKey && !cur.key.extractable) {
          chosen = cur.key;
          return;
        }
        mStore.put({ binding: owner, key: fresh } satisfies StoredAttachmentBlobKey, BLOB_KEY_META_KEY);
      };
      bReq.onsuccess = decide;
      mReq.onsuccess = decide;
      return () => chosen;
    }),
  );
}

export type DraftRecordBucketName = "draft" | "outbox";

export interface SealedDraftRecordEntry {
  bucket: DraftRecordBucketName;
  key: string;
  data: Uint8Array;
  corrupt: boolean;
}

function draftMetaKey(bucket: DraftRecordBucketName, key: string): string {
  return `${DRAFT_V1_PREFIX}${bucket}:${key}`;
}

function parseDraftMetaKey(rawKey: string): { bucket: DraftRecordBucketName; key: string } | undefined {
  if (!rawKey.startsWith(DRAFT_V1_PREFIX)) return undefined;
  const rest = rawKey.slice(DRAFT_V1_PREFIX.length);
  if (rest.startsWith("draft:")) return { bucket: "draft", key: rest.slice("draft:".length) };
  if (rest.startsWith("outbox:")) return { bucket: "outbox", key: rest.slice("outbox:".length) };
  return undefined;
}

export async function listDraftRecords(
  server: string,
  memberID: string,
  idb: IDBFactory = indexedDB,
): Promise<SealedDraftRecordEntry[]> {
  const owner = JSON.stringify([server, memberID]);
  const rawEntries = await withDB(idb, (db) =>
    runTx(db, [V4_STORE_BUCKETS, V4_STORE_META], "readonly", "list draft records failed", (tx) => {
      const bReq = tx.objectStore(V4_STORE_BUCKETS).get(RECORD_ID) as IDBRequest<StoredBucketRow | undefined>;
      const mStore = tx.objectStore(V4_STORE_META);
      const range = IDBKeyRange.bound(DRAFT_V1_PREFIX, DRAFT_V1_PREFIX + "\uffff");
      const items: Array<{ metaKey: string; value: SealedLocalValue }> = [];
      const cur = mStore.openCursor(range);
      cur.onsuccess = () => {
        const e = cur.result;
        if (!e) return;
        if (typeof e.key === "string") {
          items.push({ metaKey: e.key, value: e.value as SealedLocalValue });
        }
        e.continue();
      };
      return () => (bReq.result?.data.byteLength ? items : []);
    }),
  );
  const out: SealedDraftRecordEntry[] = [];
  for (const { metaKey, value } of rawEntries) {
    const parsed = parseDraftMetaKey(metaKey);
    if (!parsed || !parsed.key) continue;
    const expectedBinding = JSON.stringify([owner, metaKey]);
    if (!value || value.binding !== expectedBinding || !(value.key instanceof CryptoKey) || value.key.extractable) {
      out.push({ bucket: parsed.bucket, key: parsed.key, data: new Uint8Array(), corrupt: true });
      continue;
    }
    try {
      const data = await openLocalValue(value);
      out.push({ bucket: parsed.bucket, key: parsed.key, data, corrupt: false });
    } catch {
      out.push({ bucket: parsed.bucket, key: parsed.key, data: new Uint8Array(), corrupt: true });
    }
  }
  return out;
}

export async function readDraftRecord(
  server: string,
  memberID: string,
  bucket: DraftRecordBucketName,
  key: string,
  idb: IDBFactory = indexedDB,
): Promise<{ found: boolean; data?: Uint8Array; corrupt?: boolean }> {
  const metaKey = draftMetaKey(bucket, key);
  const expectedBinding = JSON.stringify([JSON.stringify([server, memberID]), metaKey]);
  const stored = await withDB(idb, (db) => readMetaIfHasRecord(db, metaKey, "read draft record failed"));
  if (!stored) return { found: false };
  if (stored.binding !== expectedBinding || !(stored.key instanceof CryptoKey) || stored.key.extractable) {
    return { found: false, corrupt: true };
  }
  try {
    return { found: true, data: await openLocalValue(stored) };
  } catch {
    return { found: false, corrupt: true };
  }
}

export async function writeDraftRecord(
  server: string,
  memberID: string,
  bucket: DraftRecordBucketName,
  key: string,
  data: Uint8Array,
  idb: IDBFactory = indexedDB,
): Promise<void> {
  const metaKey = draftMetaKey(bucket, key);
  const binding = JSON.stringify([JSON.stringify([server, memberID]), metaKey]);
  const sealed = await sealLocalValue(binding, data);
  await withDB(idb, (db) =>
    runTx(db, [V4_STORE_BUCKETS, V4_STORE_META], "readwrite", "write draft record failed", (tx) => {
      const bReq = tx.objectStore(V4_STORE_BUCKETS).get(RECORD_ID) as IDBRequest<StoredBucketRow | undefined>;
      bReq.onsuccess = () => {
        if (bReq.result?.data.byteLength) tx.objectStore(V4_STORE_META).put(sealed, metaKey);
        else tx.abort();
      };
      return () => undefined;
    }),
  );
}

export async function deleteDraftRecord(
  bucket: DraftRecordBucketName,
  key: string,
  idb: IDBFactory = indexedDB,
): Promise<void> {
  const metaKey = draftMetaKey(bucket, key);
  await withDB(idb, (db) =>
    runTx(db, V4_STORE_META, "readwrite", "delete draft record failed", (tx) => {
      tx.objectStore(V4_STORE_META).delete(metaKey);
      return () => undefined;
    }),
  );
}

export async function clearAllDraftRecords(idb: IDBFactory = indexedDB): Promise<void> {
  await withDB(idb, (db) =>
    runTx(db, V4_STORE_META, "readwrite", "clear draft records failed", (tx) => {
      const mStore = tx.objectStore(V4_STORE_META);
      const cur = mStore.openCursor(IDBKeyRange.bound(DRAFT_V1_PREFIX, DRAFT_V1_PREFIX + "\uffff"));
      cur.onsuccess = () => {
        const e = cur.result;
        if (!e) return;
        e.delete();
        e.continue();
      };
      return () => undefined;
    }),
  );
}

/** Read-only Service Worker helper: reads v4 RECORD and DEVICE_X without writes or network. */
export async function readV4RecordForNotify(): Promise<{ recordBlob: Uint8Array; revision: string; xPrivate: CryptoKey } | undefined> {
  if (typeof indexedDB === "undefined") return undefined;
  let hasV4 = true;
  try {
    if (typeof indexedDB.databases === "function") hasV4 = (await indexedDB.databases()).some((d) => d.name === V4_DB_NAME);
  } catch { hasV4 = true; }
  if (!hasV4) return undefined;
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(V4_DB_NAME, 1);
    req.onupgradeneeded = () => req.transaction?.abort();
    req.onerror = () => reject(req.error ?? new Error("notify v4 open failed"));
    req.onsuccess = () => {
      const db = req.result;
      db.onversionchange = () => db.close();
      if (!db.objectStoreNames.contains(V4_STORE_BUCKETS) || !db.objectStoreNames.contains(V4_STORE_KEYS)) {
        db.close();
        resolve(undefined);
        return;
      }
      const tx = db.transaction([V4_STORE_BUCKETS, V4_STORE_KEYS], "readonly");
      const rReq = tx.objectStore(V4_STORE_BUCKETS).get(RECORD_ID) as IDBRequest<StoredBucketRow | undefined>;
      const xReq = tx.objectStore(V4_STORE_KEYS).get(keyId(SecurityKeyKind.DEVICE_X, "")) as IDBRequest<StoredKeyRow | undefined>;
      tx.oncomplete = () => {
        db.close();
        const rec = rReq.result;
        const x = xReq.result;
        if (!rec?.data.byteLength || !x?.privateKey) {
          resolve(undefined);
          return;
        }
        resolve({ recordBlob: toAB(rec.data), revision: rec.revision, xPrivate: x.privateKey });
      };
      tx.onabort = tx.onerror = () => { db.close(); reject(tx.error ?? new Error("notify v4 read failed")); };
    };
  });
}

/** Explicit non-security account teardown helper (retains ra-server and ra-project-config/*). */
export async function clearAllSecurityV4(store: SecurityStoreV4 = securityStoreV4): Promise<void> {
  await store.executeStorage(create(SecurityStorageEffectSchema, { opId: 0n, op: SecurityStorageOp.CLEAR_ALL }));
}
