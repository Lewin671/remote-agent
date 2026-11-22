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
const DB_NAME = V4_DB_NAME;
const DB_VERSION = 1;
export const V4_STORE_BUCKETS = "buckets";
const STORE_BUCKETS = V4_STORE_BUCKETS;
export const V4_STORE_KEYS = "keys";
const STORE_KEYS = V4_STORE_KEYS;
export const V4_STORE_META = "meta";
const STORE_META = V4_STORE_META;

const LEGACY_DBS = ["remote-agent-security-v3", "remote-agent-security-v2"] as const;
const LEGACY_ACCOUNT_DB = "remote-agent";
const LEGACY_ACCOUNT_STORE = "kv";
const UPGRADE_NOTICE_KEY = "ra-upgrade-repair-v4";
const LOGIN_TOKEN_KEY = "login-token";

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

function toAB(src: Uint8Array): B {
  const out = new Uint8Array(src.byteLength);
  out.set(src);
  return out;
}

function bucketId(bucket: SecurityStoreBucket, key: string): string {
  return `${bucket}:${key}`;
}

function parseBucketId(id: string): { bucket: SecurityStoreBucket; key: string } | undefined {
  const idx = id.indexOf(":");
  if (idx <= 0) return undefined;
  const bucket = Number(id.slice(0, idx)) as SecurityStoreBucket;
  return { bucket, key: id.slice(idx + 1) };
}

function keyId(kind: SecurityKeyKind, slot: string): string {
  return `${kind}:${slot}`;
}

function isPersistentKeyKind(kind: SecurityKeyKind, persistFlag: boolean): boolean {
  if (!persistFlag || kind === SecurityKeyKind.EPHEMERAL_X) return false;
  return true;
}

function keyAlgForKind(kind: SecurityKeyKind): "Ed25519" | "X25519" {
  switch (kind) {
    case SecurityKeyKind.DEVICE_ED:
    case SecurityKeyKind.CANDIDATE_ED:
      return "Ed25519";
    case SecurityKeyKind.DEVICE_X:
    case SecurityKeyKind.CANDIDATE_X:
    case SecurityKeyKind.EPHEMERAL_X:
      return "X25519";
    default:
      throw new Error("unsupported key kind");
  }
}

function openV4DB(idb: IDBFactory = indexedDB): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = idb.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(STORE_BUCKETS)) db.createObjectStore(STORE_BUCKETS);
      if (!db.objectStoreNames.contains(STORE_KEYS)) db.createObjectStore(STORE_KEYS);
      if (!db.objectStoreNames.contains(STORE_META)) db.createObjectStore(STORE_META);
    };
    req.onerror = () => reject(req.error ?? new Error("indexeddb open failed"));
    req.onsuccess = () => {
      const db = req.result;
      db.onversionchange = () => db.close();
      resolve(db);
    };
  });
}

function deleteDatabase(name: string, idb: IDBFactory = indexedDB): Promise<void> {
  return new Promise((resolve) => {
    try {
      const req = idb.deleteDatabase(name);
      req.onsuccess = req.onerror = req.onblocked = () => resolve();
    } catch {
      resolve();
    }
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
  } catch {
    /* Ignore storage access errors in restricted workers/contexts. */
  }
}

let logoutChannel: BroadcastChannel | undefined;

/** Stops every open copy of this browser account after an explicit logout. */
export function watchAccountLogout(onLogout: () => void): () => void {
  const channel = new BroadcastChannel("ra-account-logout");
  logoutChannel = channel;
  channel.onmessage = (event) => {
    if (event.data === "logout") onLogout();
  };
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
  } catch {
    /* Ignore if BroadcastChannel is unsupported. */
  }
}

async function clearAccountCaches(): Promise<void> {
  await clearEventCache().catch(() => undefined);
  await import("../shared-file-store")
    .then((m) => m.sharedFileStore.clearAll())
    .catch(() => undefined);
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
    try {
      return this.getIDB();
    } catch {
      return undefined;
    }
  }

  private openDB(): Promise<IDBDatabase> {
    return openV4DB(this.getIDB());
  }

  private async probeDatabaseExists(name: string, idb: IDBFactory): Promise<boolean> {
    return new Promise((resolve) => {
      try {
        const req = idb.open(name);
        let existed = true;
        req.onupgradeneeded = () => {
          existed = false;
          try {
            req.transaction?.abort();
          } catch {
            /* ignore abort errors */
          }
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
        const list = await idb.databases();
        listedNames = new Set(list.flatMap((d) => (d.name ? [d.name] : [])));
      }
    } catch {
      listedNames = undefined;
    }

    let foundLegacy = false;
    for (const legacyDb of LEGACY_DBS) {
      const exists = listedNames ? listedNames.has(legacyDb) : await this.probeDatabaseExists(legacyDb, idb);
      if (exists) {
        foundLegacy = true;
        await deleteDatabase(legacyDb, idb);
      }
    }

    const hasAccountDb = listedNames ? listedNames.has(LEGACY_ACCOUNT_DB) : await this.probeDatabaseExists(LEGACY_ACCOUNT_DB, idb);
    if (hasAccountDb) {
      const hadAccountKeys = await new Promise<boolean>((resolve) => {
        try {
          const req = idb.open(LEGACY_ACCOUNT_DB);
          req.onupgradeneeded = () => {
            req.transaction?.abort();
          };
          req.onerror = () => resolve(false);
          req.onsuccess = () => {
            const db = req.result;
            db.onversionchange = () => db.close();
            if (!db.objectStoreNames.contains(LEGACY_ACCOUNT_STORE)) {
              db.close();
              resolve(false);
              return;
            }
            let cleared = false;
            const tx = db.transaction(LEGACY_ACCOUNT_STORE, "readwrite");
            const store = tx.objectStore(LEGACY_ACCOUNT_STORE);
            const cur = store.openCursor();
            cur.onsuccess = () => {
              const entry = cur.result;
              if (!entry) return;
              const k = entry.key;
              if (
                k === "account" ||
                k === "registration" ||
                (typeof k === "string" && k.startsWith("account-public/"))
              ) {
                cleared = true;
                entry.delete();
              }
              entry.continue();
            };
            tx.oncomplete = () => {
              db.close();
              resolve(cleared);
            };
            tx.onabort = tx.onerror = () => {
              db.close();
              resolve(false);
            };
          };
        } catch {
          resolve(false);
        }
      });
      if (hadAccountKeys) foundLegacy = true;
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

  private startReadIdentityPubs(tx: IDBTransaction): () => {
    hasDeviceEd: boolean;
    deviceEdPub: Uint8Array;
    hasDeviceX: boolean;
    deviceXPub: Uint8Array;
  } {
    const keysStore = tx.objectStore(STORE_KEYS);
    const edReq = keysStore.get(keyId(SecurityKeyKind.DEVICE_ED, "")) as IDBRequest<StoredKeyRow | undefined>;
    const xReq = keysStore.get(keyId(SecurityKeyKind.DEVICE_X, "")) as IDBRequest<StoredKeyRow | undefined>;
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

  async generateKey(kind: SecurityKeyKind, slot: string, persist: boolean): Promise<Uint8Array> {
    const alg = keyAlgForKind(kind);
    const pair = (await crypto.subtle.generateKey(
      { name: alg },
      false,
      alg === "Ed25519" ? ["sign", "verify"] : ["deriveBits"],
    )) as CryptoKeyPair;
    const publicKey = new Uint8Array(await crypto.subtle.exportKey("raw", pair.publicKey));
    const row: StoredKeyRow = { privateKey: pair.privateKey, publicKey };
    if (!isPersistentKeyKind(kind, persist)) {
      this.ephemeralKeys.set(keyId(kind, slot), row);
      return publicKey;
    }
    const db = await this.openDB();
    try {
      await new Promise<void>((resolve, reject) => {
        const tx = db.transaction(STORE_KEYS, "readwrite", { durability: "strict" });
        tx.objectStore(STORE_KEYS).put(row, keyId(kind, slot));
        tx.oncomplete = () => resolve();
        tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("key store write failed"));
      });
      return publicKey;
    } finally {
      db.close();
    }
  }

  private async loadKey(kind: SecurityKeyKind, slot: string): Promise<StoredKeyRow | undefined> {
    if (kind === SecurityKeyKind.EPHEMERAL_X) {
      return this.ephemeralKeys.get(keyId(kind, slot));
    }
    const mem = this.ephemeralKeys.get(keyId(kind, slot));
    if (mem) return mem;
    const db = await this.openDB();
    try {
      return await new Promise<StoredKeyRow | undefined>((resolve, reject) => {
        const tx = db.transaction(STORE_KEYS, "readonly");
        const req = tx.objectStore(STORE_KEYS).get(keyId(kind, slot)) as IDBRequest<StoredKeyRow | undefined>;
        tx.oncomplete = () => resolve(req.result);
        tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("key store read failed"));
      });
    } finally {
      db.close();
    }
  }

  async signKey(kind: SecurityKeyKind, slot: string, message: Uint8Array): Promise<Uint8Array> {
    const row = await this.loadKey(kind, slot);
    if (!row?.privateKey) throw new Error("missing signing key");
    return new Uint8Array(await crypto.subtle.sign("Ed25519", row.privateKey, toAB(message)));
  }

  async agreeKey(kind: SecurityKeyKind, slot: string, peerPublic: Uint8Array): Promise<Uint8Array> {
    const row = await this.loadKey(kind, slot);
    if (!row?.privateKey || peerPublic.byteLength !== 32) {
      throw new Error("missing agreement key or invalid peer public key");
    }
    const pub = await crypto.subtle.importKey("raw", toAB(peerPublic), "X25519", false, []);
    return new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: pub }, row.privateKey, 256));
  }

  async encapsulate(recipients: SecurityEncapsulateRecipient[]): Promise<SecurityEncapsulatedPair[]> {
    return Promise.all(
      recipients.map(async (r) => {
        if (r.xPublic.byteLength !== 32) {
          throw new Error("invalid recipient x25519 public key");
        }
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
    const db = await this.openDB();
    try {
      await new Promise<void>((resolve, reject) => {
        const tx = db.transaction(STORE_KEYS, "readwrite", { durability: "strict" });
        tx.objectStore(STORE_KEYS).delete(keyId(eff.keyKind, eff.slot));
        tx.oncomplete = () => resolve();
        tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("key discard failed"));
      });
    } finally {
      db.close();
    }
  }

  async executeStorage(eff: SecurityStorageEffect): Promise<SecurityStorageResultEvent> {
    if (eff.op === SecurityStorageOp.CLEAR_ALL) {
      this.ephemeralKeys.clear();
      this.dismissUpgradeNotice();
      const db = await this.openDB();
      try {
        await new Promise<void>((resolve, reject) => {
          const tx = db.transaction([STORE_BUCKETS, STORE_KEYS, STORE_META], "readwrite", { durability: "strict" });
          tx.objectStore(STORE_BUCKETS).clear();
          tx.objectStore(STORE_KEYS).clear();
          tx.objectStore(STORE_META).clear();
          tx.oncomplete = () => resolve();
          tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("clear all failed"));
        });
      } finally {
        db.close();
      }
      await clearAccountCaches();
      broadcastLogout();
      return create(SecurityStorageResultEventSchema, { opId: eff.opId });
    }

    const db = await this.openDB();
    try {
      if (eff.op === SecurityStorageOp.READ) {
        return await new Promise<SecurityStorageResultEvent>((resolve, reject) => {
          const tx = db.transaction([STORE_BUCKETS, STORE_KEYS], "readonly");
          const bReq = tx.objectStore(STORE_BUCKETS).get(bucketId(eff.bucket, eff.key)) as IDBRequest<StoredBucketRow | undefined>;
          const readPubs = this.startReadIdentityPubs(tx);
          tx.oncomplete = () => {
            const row = bReq.result;
            resolve(create(SecurityStorageResultEventSchema, {
              opId: eff.opId,
              found: !!row,
              revision: row?.revision ?? "",
              data: row?.data ? toAB(row.data) : new Uint8Array(),
              ...readPubs(),
            }));
          };
          tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("storage read failed"));
        });
      }

      if (eff.op === SecurityStorageOp.READ_BOOTSTRAP) {
        const res = await new Promise<{
          entries: StoredBucketRow[];
          pubs: { hasDeviceEd: boolean; deviceEdPub: Uint8Array; hasDeviceX: boolean; deviceXPub: Uint8Array };
        }>((resolve, reject) => {
          const tx = db.transaction([STORE_BUCKETS, STORE_KEYS], "readonly");
          const allReq = tx.objectStore(STORE_BUCKETS).getAll() as IDBRequest<StoredBucketRow[]>;
          const keysReq = tx.objectStore(STORE_BUCKETS).getAllKeys() as IDBRequest<string[]>;
          const readPubs = this.startReadIdentityPubs(tx);
          tx.oncomplete = () => {
            const rows: StoredBucketRow[] = [];
            const rawVals = allReq.result ?? [];
            const rawKeys = keysReq.result ?? [];
            for (let i = 0; i < rawVals.length; i++) {
              const row = rawVals[i];
              const parsedKey = typeof rawKeys[i] === "string" ? parseBucketId(rawKeys[i]) : undefined;
              const bucket = row.bucket || parsedKey?.bucket || SecurityStoreBucket.UNSPECIFIED;
              const key = row.key || parsedKey?.key || "";
              if (eff.bucket !== SecurityStoreBucket.UNSPECIFIED && bucket !== eff.bucket) continue;
              rows.push({ bucket, key, revision: row.revision, data: row.data });
            }
            resolve({ entries: rows, pubs: readPubs() });
          };
          tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("bootstrap read failed"));
        });

        const hasV4State = res.entries.some(
          (e) =>
            e.data.byteLength > 0 &&
            (e.bucket === SecurityStoreBucket.RECORD ||
              e.bucket === SecurityStoreBucket.OPERATION ||
              e.bucket === SecurityStoreBucket.REGISTRATION_INTENT),
        );
        const legacyCleared = !hasV4State
          ? await this.detectAndClearLegacySecurityV3()
          : (this.dismissUpgradeNotice(), false);

        return create(SecurityStorageResultEventSchema, {
          opId: eff.opId,
          entries: res.entries.map((e) =>
            create(SecurityStorageEntrySchema, {
              bucket: e.bucket,
              key: e.key,
              revision: e.revision,
              data: toAB(e.data),
            }),
          ),
          legacyCleared,
          ...res.pubs,
        });
      }

      if (eff.op === SecurityStorageOp.WRITE_CAS || eff.op === SecurityStorageOp.DELETE_CAS) {
        return await new Promise<SecurityStorageResultEvent>((resolve, reject) => {
          const tx = db.transaction([STORE_BUCKETS, STORE_KEYS], "readwrite", { durability: "strict" });
          const store = tx.objectStore(STORE_BUCKETS);
          const id = bucketId(eff.bucket, eff.key);
          const getReq = store.get(id) as IDBRequest<StoredBucketRow | undefined>;
          const readPubs = this.startReadIdentityPubs(tx);
          let conflict = false;
          let dismissNotice = false;
          getReq.onsuccess = () => {
            const cur = getReq.result;
            if (eff.op === SecurityStorageOp.WRITE_CAS) {
              if ((eff.createOnly && cur) || (eff.expectedRevision !== "" && (!cur || cur.revision !== eff.expectedRevision))) {
                conflict = true;
                return;
              }
              const row: StoredBucketRow = {
                bucket: eff.bucket,
                key: eff.key,
                revision: eff.nextRevision,
                data: toAB(eff.data),
              };
              store.put(row, id);
              if (eff.bucket === SecurityStoreBucket.RECORD) dismissNotice = true;
            } else {
              if (eff.expectedRevision !== "" && cur && cur.revision !== eff.expectedRevision) {
                conflict = true;
                return;
              }
              store.delete(id);
            }
          };
          tx.oncomplete = () => {
            if (dismissNotice) this.dismissUpgradeNotice();
            resolve(create(SecurityStorageResultEventSchema, {
              opId: eff.opId,
              conflict,
              revision: conflict || eff.op === SecurityStorageOp.DELETE_CAS ? "" : eff.nextRevision,
              ...readPubs(),
            }));
          };
          tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("cas write failed"));
        });
      }

      if (eff.op === SecurityStorageOp.BATCH_COMMIT) {
        return await new Promise<SecurityStorageResultEvent>((resolve, reject) => {
          const tx = db.transaction([STORE_BUCKETS, STORE_KEYS, STORE_META], "readwrite", { durability: "strict" });
          const bStore = tx.objectStore(STORE_BUCKETS);
          const kStore = tx.objectStore(STORE_KEYS);
          const mStore = tx.objectStore(STORE_META);

          let conflict = false;
          let outcomeErr = "";
          let onCommitted: (() => void) | undefined;
          let readPubs: (() => {
            hasDeviceEd: boolean;
            deviceEdPub: Uint8Array;
            hasDeviceX: boolean;
            deviceXPub: Uint8Array;
          }) | undefined;

          const curReqs = eff.batch.map((w) => bStore.get(bucketId(w.bucket, w.key)) as IDBRequest<StoredBucketRow | undefined>);
          const candEdId = eff.promoteEdSlot ? keyId(SecurityKeyKind.CANDIDATE_ED, eff.promoteEdSlot) : "";
          const candXId = eff.promoteXSlot ? keyId(SecurityKeyKind.CANDIDATE_X, eff.promoteXSlot) : "";
          const edCandReq = candEdId ? (kStore.get(candEdId) as IDBRequest<StoredKeyRow | undefined>) : undefined;
          const xCandReq = candXId ? (kStore.get(candXId) as IDBRequest<StoredKeyRow | undefined>) : undefined;

          let pendingReads = curReqs.length + (edCandReq ? 1 : 0) + (xCandReq ? 1 : 0);

          const applyCommit = () => {
            try {
              // Step 1: Validate all CAS expectations atomically before mutating anything.
              for (let i = 0; i < eff.batch.length; i++) {
                const w = eff.batch[i];
                const cur = curReqs[i].result;
                if (w.remove) {
                  if (w.expectedRevision !== "" && cur && cur.revision !== w.expectedRevision) {
                    conflict = true;
                    break;
                  }
                } else {
                  if ((w.createOnly && cur) || (w.expectedRevision !== "" && (!cur || cur.revision !== w.expectedRevision))) {
                    conflict = true;
                    break;
                  }
                }
              }
              if (conflict) {
                readPubs = this.startReadIdentityPubs(tx);
                return;
              }

              let ephemeralDeletes: string[] = [];
              let recordCommitted = false;

              // Step 2: Promote candidate slots -> device identity keys.
              if (eff.promoteEdSlot) {
                const cand = edCandReq?.result ?? this.ephemeralKeys.get(candEdId);
                if (!cand?.privateKey) {
                  outcomeErr = "missing promote ed slot";
                  tx.abort();
                  return;
                }
                kStore.put(cand, keyId(SecurityKeyKind.DEVICE_ED, ""));
                kStore.delete(candEdId);
                ephemeralDeletes.push(candEdId);
              }
              if (eff.promoteXSlot) {
                const cand = xCandReq?.result ?? this.ephemeralKeys.get(candXId);
                if (!cand?.privateKey) {
                  outcomeErr = "missing promote x slot";
                  tx.abort();
                  return;
                }
                kStore.put(cand, keyId(SecurityKeyKind.DEVICE_X, ""));
                kStore.delete(candXId);
                ephemeralDeletes.push(candXId);
              }

              // Step 3: Commit all bucket writes/removals.
              for (const w of eff.batch) {
                const id = bucketId(w.bucket, w.key);
                if (w.remove) {
                  bStore.delete(id);
                } else {
                  bStore.put({
                    bucket: w.bucket,
                    key: w.key,
                    revision: w.nextRevision,
                    data: toAB(w.data),
                  } satisfies StoredBucketRow, id);
                  if (w.bucket === SecurityStoreBucket.RECORD) recordCommitted = true;
                }
              }

              // Step 4: Discard remaining candidate/ephemeral slots and optionally clear login token.
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

              readPubs = this.startReadIdentityPubs(tx);
            } catch (e) {
              outcomeErr = e instanceof Error ? e.message : String(e);
              try {
                tx.abort();
              } catch {
                /* ignore */
              }
            }
          };

          const onReadDone = () => {
            if (--pendingReads === 0) applyCommit();
          };
          if (pendingReads === 0) {
            applyCommit();
          } else {
            for (const req of curReqs) req.onsuccess = onReadDone;
            if (edCandReq) edCandReq.onsuccess = onReadDone;
            if (xCandReq) xCandReq.onsuccess = onReadDone;
          }

          tx.oncomplete = () => {
            onCommitted?.();
            const pubs = readPubs
              ? readPubs()
              : { hasDeviceEd: false, deviceEdPub: new Uint8Array(), hasDeviceX: false, deviceXPub: new Uint8Array() };
            resolve(create(SecurityStorageResultEventSchema, {
              opId: eff.opId,
              conflict,
              error: outcomeErr,
              ...pubs,
            }));
          };
          tx.onabort = tx.onerror = () => {
            if (outcomeErr) {
              resolve(create(SecurityStorageResultEventSchema, { opId: eff.opId, error: outcomeErr }));
            } else {
              reject(tx.error ?? new Error("batch commit failed"));
            }
          };
        });
      }

      return create(SecurityStorageResultEventSchema, { opId: eff.opId, error: "unsupported storage op" });
    } finally {
      db.close();
    }
  }
}

export const securityStoreV4 = new SecurityStoreV4();

async function sealLocalValue(binding: string, bytes: Uint8Array): Promise<SealedLocalValue> {
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const ciphertext = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv, additionalData: new TextEncoder().encode(binding) },
    key,
    toAB(bytes),
  );
  return { binding, key, iv, ciphertext };
}

async function openLocalValue(stored: SealedLocalValue): Promise<Uint8Array> {
  if (!(stored.key instanceof CryptoKey) || stored.key.extractable) throw new Error("corrupt wrapped key");
  return new Uint8Array(
    await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: stored.iv, additionalData: new TextEncoder().encode(stored.binding) },
      stored.key,
      stored.ciphertext,
    ),
  );
}

export async function saveLoginToken(server: string, memberID: string, token: LoginToken): Promise<void> {
  const binding = JSON.stringify([server, memberID]);
  const sealed = await sealLocalValue(binding, new TextEncoder().encode(JSON.stringify(token)));
  const db = await openV4DB();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction([STORE_BUCKETS, STORE_META], "readwrite", { durability: "strict" });
      const bReq = tx.objectStore(STORE_BUCKETS).get(bucketId(SecurityStoreBucket.RECORD, "record")) as IDBRequest<StoredBucketRow | undefined>;
      bReq.onsuccess = () => {
        if (!bReq.result?.data.byteLength) return;
        tx.objectStore(STORE_META).put(sealed, LOGIN_TOKEN_KEY);
      };
      tx.oncomplete = () => resolve();
      tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("save login token failed"));
    });
  } finally {
    db.close();
  }
}

export async function clearLoginToken(server: string, memberID: string): Promise<void> {
  const binding = JSON.stringify([server, memberID]);
  const db = await openV4DB();
  try {
    await new Promise<void>((resolve) => {
      const tx = db.transaction(STORE_META, "readwrite", { durability: "strict" });
      const mStore = tx.objectStore(STORE_META);
      const getReq = mStore.get(LOGIN_TOKEN_KEY) as IDBRequest<SealedLocalValue | undefined>;
      getReq.onsuccess = () => {
        const cur = getReq.result;
        if (cur && cur.binding === binding) mStore.delete(LOGIN_TOKEN_KEY);
      };
      tx.oncomplete = tx.onabort = tx.onerror = () => resolve();
    });
  } finally {
    db.close();
  }
}

export async function loadLoginToken(server: string, memberID: string, now = Date.now()): Promise<LoginToken | undefined> {
  const binding = JSON.stringify([server, memberID]);
  const db = await openV4DB();
  try {
    const stored = await new Promise<SealedLocalValue | undefined>((resolve, reject) => {
      const tx = db.transaction([STORE_BUCKETS, STORE_META], "readonly");
      const bReq = tx.objectStore(STORE_BUCKETS).get(bucketId(SecurityStoreBucket.RECORD, "record")) as IDBRequest<StoredBucketRow | undefined>;
      const mReq = tx.objectStore(STORE_META).get(LOGIN_TOKEN_KEY) as IDBRequest<SealedLocalValue | undefined>;
      tx.oncomplete = () => resolve(bReq.result?.data.byteLength ? mReq.result : undefined);
      tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("load login token failed"));
    });
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
  } finally {
    db.close();
  }
}

export async function saveLocalData(server: string, memberID: string, values: Map<string, Uint8Array | undefined>): Promise<void> {
  const owner = JSON.stringify([server, memberID]);
  const sealed = await Promise.all(
    [...values].map(async ([name, bytes]) => {
      if (bytes === undefined) return [name, undefined] as const;
      return [name, await sealLocalValue(JSON.stringify([owner, name]), bytes)] as const;
    }),
  );
  const db = await openV4DB();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction([STORE_BUCKETS, STORE_META], "readwrite", { durability: "strict" });
      const bReq = tx.objectStore(STORE_BUCKETS).get(bucketId(SecurityStoreBucket.RECORD, "record")) as IDBRequest<StoredBucketRow | undefined>;
      bReq.onsuccess = () => {
        if (!bReq.result?.data.byteLength) {
          tx.abort();
          return;
        }
        const mStore = tx.objectStore(STORE_META);
        for (const [name, value] of sealed) {
          if (value === undefined) mStore.delete("local-content:" + name);
          else mStore.put(value, "local-content:" + name);
        }
      };
      tx.oncomplete = () => resolve();
      tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("save local data failed"));
    });
  } finally {
    db.close();
  }
}

export async function removeLocalData(_server: string, _memberID: string, name: string): Promise<void> {
  const db = await openV4DB();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(STORE_META, "readwrite", { durability: "strict" });
      const mStore = tx.objectStore(STORE_META);
      const key = "local-content:" + name;
      mStore.delete(key);
      const cursor = mStore.openCursor(IDBKeyRange.bound(key + "/", key + "/\uffff"));
      cursor.onsuccess = () => {
        if (cursor.result) {
          cursor.result.delete();
          cursor.result.continue();
        }
      };
      tx.oncomplete = () => resolve();
      tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("remove local data failed"));
    });
  } finally {
    db.close();
  }
}

export async function loadLocalData(server: string, memberID: string, name: string): Promise<Uint8Array | undefined> {
  const owner = JSON.stringify([server, memberID]);
  const db = await openV4DB();
  try {
    const stored = await new Promise<SealedLocalValue | undefined>((resolve, reject) => {
      const tx = db.transaction([STORE_BUCKETS, STORE_META], "readonly");
      const bReq = tx.objectStore(STORE_BUCKETS).get(bucketId(SecurityStoreBucket.RECORD, "record")) as IDBRequest<StoredBucketRow | undefined>;
      const mReq = tx.objectStore(STORE_META).get("local-content:" + name) as IDBRequest<SealedLocalValue | undefined>;
      tx.oncomplete = () => resolve(bReq.result?.data.byteLength ? mReq.result : undefined);
      tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("load local data failed"));
    });
    if (!stored) return undefined;
    const binding = JSON.stringify([owner, name]);
    if (stored.binding !== binding || !(stored.key instanceof CryptoKey) || stored.key.extractable) {
      throw new CorruptLocalContent();
    }
    try {
      return await openLocalValue(stored);
    } catch {
      throw new CorruptLocalContent();
    }
  } finally {
    db.close();
  }
}

/** Read-only Service Worker helper: reads v4 RECORD and DEVICE_X without writes or network. */
export async function readV4RecordForNotify(): Promise<
  { recordBlob: Uint8Array; revision: string; xPrivate: CryptoKey } | undefined
> {
  if (typeof indexedDB === "undefined") return undefined;
  let hasV4 = true;
  try {
    if (typeof indexedDB.databases === "function") {
      hasV4 = (await indexedDB.databases()).some((d) => d.name === DB_NAME);
    }
  } catch {
    hasV4 = true;
  }
  if (!hasV4) return undefined;
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = () => {
      req.transaction?.abort();
    };
    req.onerror = () => reject(req.error ?? new Error("notify v4 open failed"));
    req.onsuccess = () => {
      const db = req.result;
      db.onversionchange = () => db.close();
      if (!db.objectStoreNames.contains(STORE_BUCKETS) || !db.objectStoreNames.contains(STORE_KEYS)) {
        db.close();
        resolve(undefined);
        return;
      }
      const tx = db.transaction([STORE_BUCKETS, STORE_KEYS], "readonly");
      const rReq = tx.objectStore(STORE_BUCKETS).get(bucketId(SecurityStoreBucket.RECORD, "record")) as IDBRequest<StoredBucketRow | undefined>;
      const xReq = tx.objectStore(STORE_KEYS).get(keyId(SecurityKeyKind.DEVICE_X, "")) as IDBRequest<StoredKeyRow | undefined>;
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
      tx.onabort = tx.onerror = () => {
        db.close();
        reject(tx.error ?? new Error("notify v4 read failed"));
      };
    };
  });
}

/** Explicit non-security account teardown helper (retains ra-server and ra-project-config/*). */
export async function clearAllSecurityV4(store: SecurityStoreV4 = securityStoreV4): Promise<void> {
  await store.executeStorage(
    create(SecurityStorageEffectSchema, { opId: 0n, op: SecurityStorageOp.CLEAR_ALL }),
  );
}
