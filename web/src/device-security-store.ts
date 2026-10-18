import {
  advance,
  equal,
  openGrant,
  parseState,
  SecurityError,
  stateBytes,
  stateHash,
  verifyState,
  type Pin,
  type SecurityState,
} from "./device-security";

type B = Uint8Array<ArrayBuffer>;
const DATABASE = "remote-agent-security-v2";
const STORE = "security";
const KEY = "current";
export interface SecurityRecord {
  version: 2;
  pin: Pin;
  state: B;
  stateHash: B;
  memberID: string;
  xPrivate: CryptoKey;
  // HKDF CryptoKey cannot be exported. Locked native clients use their own
  // platform storage; a notification worker never receives this record.
  secret: CryptoKey;
  counter: bigint;
}
function database(initialize: boolean): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DATABASE, 1);
    req.onupgradeneeded = () => {
      if (!initialize) {
        req.transaction?.abort();
        return;
      }
      req.result.createObjectStore(STORE);
    };
    req.onerror = () => reject(new SecurityError("storage"));
    req.onsuccess = () => {
      const db = req.result;
      db.onversionchange = () => db.close();
      if (!db.objectStoreNames.contains(STORE)) {
        db.close();
        reject(new SecurityError("storage"));
        return;
      }
      resolve(db);
    };
  });
}
function validRecord(
  r: SecurityRecord | undefined,
): asserts r is SecurityRecord {
  if (
    !r ||
    r.version !== 2 ||
    !(r.state instanceof Uint8Array) ||
    !(r.stateHash instanceof Uint8Array) ||
    r.stateHash.length !== 32 ||
    typeof r.counter !== "bigint" ||
    r.counter < 0n ||
    r.counter > 0xffffffffffffffffn ||
    !(r.xPrivate instanceof CryptoKey) ||
    r.xPrivate.extractable ||
    r.xPrivate.algorithm.name !== "X25519" ||
    !(r.secret instanceof CryptoKey) ||
    r.secret.extractable ||
    r.secret.algorithm.name !== "HKDF"
  )
    throw new SecurityError("storage");
}
function transaction<T>(
  db: IDBDatabase,
  mode: IDBTransactionMode,
  op: (
    store: IDBObjectStore,
    finish: (result: T) => void,
    fail: (err: Error) => void,
  ) => void,
): Promise<T> {
  return new Promise((resolve, reject) => {
    let result: T, failure: Error | undefined;
    const tx = db.transaction(STORE, mode, { durability: "strict" });
    const fail = (err: Error) => {
      failure = err;
      tx.abort();
    };
    tx.onabort = tx.onerror = () => {
      db.close();
      reject(failure ?? new SecurityError("storage"));
    };
    tx.oncomplete = () => {
      db.close();
      resolve(result);
    };
    try {
      op(
        tx.objectStore(STORE),
        (r) => {
          result = r;
        },
        fail,
      );
    } catch (err) {
      fail(err instanceof Error ? err : new SecurityError("storage"));
    }
  });
}
function publicPinEqual(a: Pin, b: Pin): boolean {
  return (
    a.account === b.account &&
    a.authorityID === b.authorityID &&
    equal(a.domain, b.domain) &&
    equal(a.genesis, b.genesis) &&
    equal(a.authorityEd, b.authorityEd) &&
    equal(a.authorityX, b.authorityX)
  );
}
/** Explicitly called only after the user verified the SAS binding. No root-only
 * import, relay directory, missing DB or stored account can call this implicitly. */
export async function initializeSecurity(
  pin: Pin,
  state: SecurityState,
  memberID: string,
  xPrivate: CryptoKey,
): Promise<void> {
  await verifyState(pin, state);
  const plain = await openGrant(pin, state, memberID, xPrivate);
  const secret = await crypto.subtle.importKey("raw", plain, "HKDF", false, [
    "deriveBits",
  ]);
  plain.fill(0);
  const record: SecurityRecord = {
    version: 2,
    pin,
    state: await stateBytes(state),
    stateHash: await stateHash(state),
    memberID,
    xPrivate,
    secret,
    counter: 0n,
  };
  validRecord(record);
  const db = await database(true);
  await transaction<void>(db, "readwrite", (store, done, fail) => {
    const get = store.get(KEY);
    get.onsuccess = () => {
      if (get.result !== undefined) {
        fail(new SecurityError("conflict"));
        return;
      }
      store.add(record, KEY);
      done();
    };
  });
}
export async function loadSecurity(): Promise<SecurityRecord> {
  const db = await database(false);
  const r = await transaction<SecurityRecord>(
    db,
    "readonly",
    (store, done, fail) => {
      const get = store.get(KEY);
      get.onsuccess = () => {
        try {
          validRecord(get.result);
          done(get.result);
        } catch {
          fail(new SecurityError("storage"));
        }
      };
    },
  );
  const state = await parseState(r.state);
  await verifyState(r.pin, state);
  if (
    !equal(await stateHash(state), r.stateHash) ||
    !state.core.members.some((m) => m.id === r.memberID)
  )
    throw new SecurityError("storage");
  return r;
}
/** Atomic across tabs. The committed counter is consumed BEFORE any encryption,
 * even when the network request is later canceled or the process crashes. */
export async function reserveCounter(
  expected: SecurityRecord,
): Promise<SecurityRecord> {
  const db = await database(false);
  return transaction<SecurityRecord>(db, "readwrite", (store, done, fail) => {
    const get = store.get(KEY);
    get.onsuccess = () => {
      try {
        const r = get.result as SecurityRecord;
        validRecord(r);
        if (
          !publicPinEqual(r.pin, expected.pin) ||
          r.memberID !== expected.memberID ||
          !equal(r.stateHash, expected.stateHash)
        ) {
          fail(new SecurityError("stale"));
          return;
        }
        if (r.counter === 0xffffffffffffffffn) {
          fail(new SecurityError("capacity"));
          return;
        }
        r.counter++;
        store.put(r, KEY);
        done(r);
      } catch (err) {
        fail(err instanceof Error ? err : new SecurityError("storage"));
      }
    };
  });
}
export async function installSecurity(
  expected: SecurityRecord,
  next: SecurityState,
): Promise<void> {
  const old = await parseState(expected.state);
  await advance(expected.pin, old, next);
  const plain = await openGrant(
    expected.pin,
    next,
    expected.memberID,
    expected.xPrivate,
  );
  const secret = await crypto.subtle.importKey("raw", plain, "HKDF", false, [
    "deriveBits",
  ]);
  plain.fill(0);
  const nextBytes = await stateBytes(next),
    nextHash = await stateHash(next);
  const db = await database(false);
  await transaction<void>(db, "readwrite", (store, done, fail) => {
    const get = store.get(KEY);
    get.onsuccess = () => {
      try {
        const r = get.result as SecurityRecord;
        validRecord(r);
        if (
          !publicPinEqual(r.pin, expected.pin) ||
          r.memberID !== expected.memberID ||
          !equal(r.stateHash, expected.stateHash)
        ) {
          fail(new SecurityError("stale"));
          return;
        }
        store.put(
          { state: r.state, key: r.secret },
          "epoch:" + old.core.epoch.toString(),
        );
        store.put(
          { ...r, state: nextBytes, stateHash: nextHash, secret, counter: 0n },
          KEY,
        );
        done();
      } catch (err) {
        fail(err instanceof Error ? err : new SecurityError("storage"));
      }
    };
  });
}
export function forgetSecurity(): Promise<void> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.deleteDatabase(DATABASE);
    req.onsuccess = () => resolve();
    req.onerror = req.onblocked = () => reject(new SecurityError("storage"));
  });
}

export interface StoredRemoval {
  request: import("./device-security-removal").DeleteRequest;
  phase: "unconfirmed" | "pending" | "complete";
  waiting: string[];
  error?: string;
  cleanup?: {
    completion: Uint8Array<ArrayBuffer>;
    request: Uint8Array<ArrayBuffer>;
    chain: Uint8Array<ArrayBuffer>[];
  };
  serverCleaned?: boolean;
}
export async function saveRemoval(op: StoredRemoval): Promise<void> {
  const db = await database(false);
  await transaction<void>(db, "readwrite", (store, done, fail) => {
    const get = store.get(KEY);
    get.onsuccess = () => {
      try {
        const r = get.result as SecurityRecord;
        validRecord(r);
        if (
          op.request.Account !== r.pin.account ||
          op.request.Requester !== r.memberID ||
          !equal(new Uint8Array(op.request.Domain), r.pin.domain)
        ) {
          fail(new SecurityError("untrusted"));
          return;
        }
        const pending = store.get("removals");
        pending.onsuccess = () => {
          const entries = (pending.result ?? {}) as Record<
            string,
            StoredRemoval
          >;
          entries[op.request.Operation] = op;
          const ids = Object.keys(entries);
          if (ids.length > 128) {
            const complete = ids.filter(
              (id) =>
                entries[id].phase === "complete" && entries[id].serverCleaned,
            );
            while (Object.keys(entries).length > 128 && complete.length)
              delete entries[complete.shift()!];
            if (Object.keys(entries).length > 128) {
              fail(new SecurityError("capacity"));
              return;
            }
          }
          store.put(entries, "removals");
          done();
        };
      } catch {
        fail(new SecurityError("storage"));
      }
    };
  });
}
export async function loadRemovals(): Promise<StoredRemoval[]> {
  const r = await loadSecurity();
  const db = await database(false);
  return transaction<StoredRemoval[]>(db, "readonly", (store, done, fail) => {
    const get = store.get("removals");
    get.onsuccess = () => {
      const entries = Object.values(get.result ?? {}) as StoredRemoval[];
      if (
        entries.length > 128 ||
        entries.some(
          (o) =>
            o.request.Account !== r.pin.account ||
            o.request.Requester !== r.memberID ||
            !equal(new Uint8Array(o.request.Domain), r.pin.domain),
        )
      ) {
        fail(new SecurityError("storage"));
        return;
      }
      done(entries);
    };
  });
}

export interface CachedEpoch {
  state: Uint8Array<ArrayBuffer>;
  key: CryptoKey;
}
export async function cacheSecurityEpoch(
  state: SecurityState,
  secret: Uint8Array<ArrayBuffer>,
): Promise<void> {
  const current = await loadSecurity();
  await verifyState(current.pin, state);
  const head = await parseState(current.state);
  if (state.core.epoch > head.core.epoch || secret.length !== 32)
    throw new SecurityError("untrusted");
  const key = await crypto.subtle.importKey("raw", secret, "HKDF", false, [
    "deriveBits",
  ]);
  const entry: CachedEpoch = { state: await stateBytes(state), key };
  const db = await database(false);
  await transaction<void>(db, "readwrite", (store, done, fail) => {
    const get = store.get(KEY);
    get.onsuccess = () => {
      try {
        const r = get.result as SecurityRecord;
        validRecord(r);
        if (
          !publicPinEqual(r.pin, current.pin) ||
          r.memberID !== current.memberID
        ) {
          fail(new SecurityError("stale"));
          return;
        }
        store.put(entry, "epoch:" + state.core.epoch.toString());
        done();
      } catch {
        fail(new SecurityError("storage"));
      }
    };
  });
}
export async function readSecurityEpoch(
  epoch: bigint,
): Promise<CachedEpoch | undefined> {
  const current = await loadSecurity();
  const head = await parseState(current.state);
  if (epoch < 1n || epoch > head.core.epoch)
    throw new SecurityError("untrusted");
  const db = await database(false);
  const entry = await transaction<CachedEpoch | undefined>(
    db,
    "readonly",
    (store, done) => {
      const get = store.get("epoch:" + epoch.toString());
      get.onsuccess = () => done(get.result);
    },
  );
  if (!entry) return undefined;
  const state = await parseState(entry.state);
  await verifyState(current.pin, state);
  if (
    state.core.epoch !== epoch ||
    !(entry.key instanceof CryptoKey) ||
    entry.key.extractable ||
    entry.key.algorithm.name !== "HKDF"
  )
    throw new SecurityError("storage");
  return entry;
}

/** Missing state starts an explicit one-time SAS setup. Existing but corrupt
 * state must never be silently replaced or reconstructed from the root. */
export async function securitySetupStatus(): Promise<
  "missing" | "ready" | "invalid"
> {
  const databases = await indexedDB.databases();
  if (!databases.some((d) => d.name === DATABASE)) return "missing";
  try {
    const record = await loadSecurity();
    await verifyState(record.pin, await parseState(record.state));
    return "ready";
  } catch {
    return "invalid";
  }
}
