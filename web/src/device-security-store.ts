import {
  MAX_CLOCK_SKEW,
  MAX_STATES,
  SecurityError,
  equal,
  openGrant,
  openPrevSecret,
  parseRecoveryStatement,
  parseState,
  stateHash,
  verifyChain,
  verifyTransition,
  type Pin,
  type RecoveryStatement,
  type SecurityState,
} from "./device-security";

type B = Uint8Array<ArrayBuffer>;
const DATABASE = "remote-agent-security-v3";
const STORE = "security";
const KEY = "current";
export interface ContentKey {
  epoch: bigint;
  secret: B;
}
export interface SecurityRecord {
  version: 3;
  pin: Pin;
  // Verified chain from the pinned genesis to the head.
  states: SecurityState[];
  proofs: (RecoveryStatement | undefined)[];
  stateHash: B;
  memberID: string;
  // Whether the head still names this device. A removed device keeps the
  // last secret and cached keys so its retained history stays readable.
  member: boolean;
  xPrivate: CryptoKey;
  // Raw 32-byte content secrets. A page that could decrypt content can
  // already read these; PrevSecret walking needs the bytes.
  secret: B;
  contentKeys: ContentKey[];
  counter: bigint;
  // Persisted time lower bound, ms.
  floor: bigint;
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
function validRecord(r: SecurityRecord | undefined): asserts r is SecurityRecord {
  if (
    !r ||
    r.version !== 3 ||
    !Array.isArray(r.states) ||
    r.states.length === 0 ||
    r.states.length > MAX_STATES ||
    !Array.isArray(r.proofs) ||
    r.proofs.length !== r.states.length ||
    !(r.stateHash instanceof Uint8Array) ||
    r.stateHash.length !== 32 ||
    typeof r.memberID !== "string" ||
    typeof r.member !== "boolean" ||
    !(r.secret instanceof Uint8Array) ||
    r.secret.length !== 32 ||
    !Array.isArray(r.contentKeys) ||
    typeof r.counter !== "bigint" ||
    r.counter < 0n ||
    r.counter > 0xffffffffffffffffn ||
    typeof r.floor !== "bigint" ||
    !(r.xPrivate instanceof CryptoKey) ||
    r.xPrivate.extractable ||
    r.xPrivate.algorithm.name !== "X25519"
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
    equal(a.domain, b.domain) &&
    equal(a.genesis, b.genesis)
  );
}
function keyCache(keys: ContentKey[]): Map<bigint, B> {
  return new Map(keys.map((k) => [k.epoch, k.secret]));
}
function remember(keys: ContentKey[], epoch: bigint, secret: B): void {
  if (!keys.some((k) => k.epoch === epoch))
    keys.push({ epoch, secret: new Uint8Array(secret) });
}
/** Opens the content key for epoch from the last state that named this
 * device. A missing PrevSecret is the recovery boundary: pre-recovery content
 * is unavailable. A removed device therefore keeps the key of its last
 * membership, not the head's. */
export async function contentSecretOf(
  r: Pick<SecurityRecord, "states" | "secret" | "contentKeys"> &
    Partial<Pick<SecurityRecord, "memberID" | "member">>,
  epoch: bigint,
): Promise<B> {
  const head = r.states[r.states.length - 1];
  const cached = keyCache(r.contentKeys).get(epoch);
  if (cached) return new Uint8Array(cached);
  const removed = r.member === false;
  let last: SecurityState | undefined = head;
  if (removed && r.memberID) {
    last = undefined;
    for (let i = r.states.length - 1; i >= 0; i--) {
      const s = r.states[i];
      if (s.core.members.some((m) => m.id === r.memberID)) {
        last = s;
        break;
      }
    }
  }
  if (!last) throw new SecurityError("unavailable");
  if (!removed && epoch === head.core.contentEpoch)
    return new Uint8Array(r.secret);
  if (epoch > head.core.contentEpoch) throw new SecurityError("stale");
  let key: B | undefined = r.secret;
  for (let i = r.states.indexOf(last); i >= 0; i--) {
    const s = r.states[i];
    if (s.core.contentEpoch === epoch) {
      if (!key) throw new SecurityError("unavailable");
      return new Uint8Array(key);
    }
    if (s.core.contentEpoch < epoch) throw new SecurityError("stale");
    if (i === 0) break;
    const prev = r.states[i - 1];
    if (prev.core.contentEpoch === s.core.contentEpoch) continue;
    if (s.prevSecret.length === 0 || !key)
      throw new SecurityError("unavailable");
    key = await openPrevSecret(key, s.prevSecret);
  }
  throw new SecurityError("unavailable");
}
/** The current key follows each lease renewal but changes only with the
 * content epoch; the counter resets only when that key changes. */
export async function extendRecord(
  r: SecurityRecord,
  states: SecurityState[],
  proofs: (RecoveryStatement | undefined)[],
): Promise<SecurityRecord> {
  if (!states.length) return r;
  const next: SecurityRecord = {
    ...r,
    states: [...r.states],
    proofs: [...r.proofs],
    contentKeys: r.contentKeys.map((k) => ({ ...k })),
    secret: new Uint8Array(r.secret),
  };
  for (let i = 0; i < states.length; i++) {
    const s = states[i];
    const head = next.states[next.states.length - 1];
    if (s.core.epoch <= head.core.epoch) {
      if (!equal(await stateHash(s), await stateHash(head)))
        throw new SecurityError("stale");
      continue;
    }
    if (
      s.core.epoch !== head.core.epoch + 1n ||
      next.states.length >= MAX_STATES
    )
      throw new SecurityError("stale");
    const proof = proofs[i];
    if (s.core.issuedAt > BigInt(Date.now()) + BigInt(MAX_CLOCK_SKEW))
      throw new SecurityError("clock");
    await verifyTransition(head, s, proof);
    const member = s.core.members.some((m) => m.id === next.memberID);
    if (member) {
      const secret = await openGrant(
        next.pin,
        s,
        next.memberID,
        next.xPrivate,
      );
      if (s.core.contentEpoch !== head.core.contentEpoch) {
        remember(next.contentKeys, head.core.contentEpoch, next.secret);
        if (!proof) {
          const old = await openPrevSecret(secret, s.prevSecret);
          if (!equal(old, next.secret))
            throw new SecurityError("untrusted");
        }
        next.secret = secret;
        next.counter = 0n;
      } else {
        if (!equal(secret, next.secret))
          throw new SecurityError("untrusted");
      }
    }
    next.states.push(s);
    next.proofs.push(proof);
  }
  const head = next.states[next.states.length - 1];
  next.member = head.core.members.some((m) => m.id === next.memberID);
  next.stateHash = await stateHash(head);
  next.floor = maxFloor(next.floor);
  return next;
}
function maxFloor(floor: bigint): bigint {
  const now = BigInt(Date.now());
  return now > floor ? now : floor;
}
/** Builds a new record from a pairing payload or the pinned chain. */
export async function buildRecord(
  pin: Pin,
  states: SecurityState[],
  proofs: (RecoveryStatement | undefined)[],
  memberID: string,
  xPrivate: CryptoKey,
): Promise<SecurityRecord> {
  await verifyChain(pin, states, proofs);
  const head = states[states.length - 1];
  if (!head.core.members.some((m) => m.id === memberID))
    throw new SecurityError("untrusted");
  const secret = await openGrant(pin, head, memberID, xPrivate);
  const r: SecurityRecord = {
    version: 3,
    pin,
    states: [...states],
    proofs: [...proofs],
    stateHash: await stateHash(head),
    memberID,
    member: true,
    xPrivate,
    secret,
    contentKeys: [],
    counter: 0n,
    floor: BigInt(Date.now()),
  };
  // Cache every content key still reachable from the head. A device that
  // joined after a recovery stops at the boundary.
  for (let i = states.length - 1; i >= 0; i--) {
    const epoch = states[i].core.contentEpoch;
    try {
      const key = await contentSecretOf(r, epoch);
      remember(r.contentKeys, epoch, key);
    } catch {
      break;
    }
  }
  remember(r.contentKeys, head.core.contentEpoch, r.secret);
  return r;
}
/** Explicitly called only after the user verified the SAS binding. No
 * root-only import, relay directory or stored account can call this. */
export async function initializeSecurity(
  pin: Pin,
  states: SecurityState[],
  proofs: (RecoveryStatement | undefined)[],
  memberID: string,
  xPrivate: CryptoKey,
  replace = false,
): Promise<void> {
  const record = await buildRecord(pin, states, proofs, memberID, xPrivate);
  validRecord(record);
  const db = await database(true);
  await transaction<void>(db, "readwrite", (store, done, fail) => {
    const get = store.get(KEY);
    get.onsuccess = () => {
      const old = get.result as SecurityRecord | undefined;
      if (old !== undefined) {
        // A re-pair may replace its own domain only; a corrupt or
        // different record must never be overwritten.
        if (
          !replace ||
          !publicPinEqual(old.pin, record.pin) ||
          old.memberID !== record.memberID
        ) {
          fail(new SecurityError("conflict"));
          return;
        }
        store.put(record, KEY);
        done();
        return;
      }
      store.add(record, KEY);
      done();
    };
  });
}
/** The verifier every stored record passes through on load. */
export async function verifyStoredSecurity(
  r: SecurityRecord,
): Promise<SecurityRecord> {
  validRecord(r);
  if (r.states.length !== r.proofs.length)
    throw new SecurityError("storage");
  // Every transition, authority signature and recovery proof, not only the
  // parent links and the head signature.
  await verifyChain(r.pin, r.states, r.proofs);
  const head = r.states[r.states.length - 1];
  if (!equal(await stateHash(head), r.stateHash))
    throw new SecurityError("storage");
  return r;
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
  return verifyStoredSecurity(r);
}
/** Atomic across tabs. The committed counter is consumed BEFORE any encryption. */
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
        const next = { ...r, counter: r.counter + 1n };
        store.put(next, KEY);
        done(next);
      } catch (err) {
        fail(err instanceof Error ? err : new SecurityError("storage"));
      }
    };
  });
}
export async function installSecurity(
  expected: SecurityRecord,
  states: SecurityState[],
  proofs: (RecoveryStatement | undefined)[],
): Promise<void> {
  const next = await extendRecord(expected, states, proofs);
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
        store.put(next, KEY);
        done();
      } catch (err) {
        fail(err instanceof Error ? err : new SecurityError("storage"));
      }
    };
  });
}
/** Persists the time lower bound; control checks use max(now, floor). */
export async function touchSecurity(now: bigint): Promise<void> {
  const db = await database(false);
  await transaction<void>(db, "readwrite", (store, done, fail) => {
    const get = store.get(KEY);
    get.onsuccess = () => {
      try {
        const r = get.result as SecurityRecord;
        validRecord(r);
        if (now > r.floor) store.put({ ...r, floor: now }, KEY);
        done();
      } catch (err) {
        fail(err instanceof Error ? err : new SecurityError("storage"));
      }
    };
  });
}
function deleteDatabase(name: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.deleteDatabase(name);
    req.onsuccess = () => resolve();
    req.onerror = req.onblocked = () => reject(new SecurityError("storage"));
  });
}
export async function forgetSecurity(): Promise<void> {
  await deleteDatabase(DATABASE);
  // Protocol 9's database is not migrated; drop its keys on an explicit
  // logout too, so an upgrade does not leave them behind.
  await deleteDatabase("remote-agent-security-v2");
}
/** The local pin, or undefined when this browser has no security record. */
export async function securityPin(): Promise<Pin | undefined> {
  try {
    return (await loadSecurity()).pin;
  } catch {
    return undefined;
  }
}
/** Missing state starts an explicit one-time SAS setup. Existing but corrupt
 * state must never be silently replaced or reconstructed from the root. */
export async function securitySetupStatus(): Promise<
  "missing" | "ready" | "invalid" | "clock"
> {
  const databases = await indexedDB.databases();
  if (!databases.some((d) => d.name === DATABASE)) return "missing";
  try {
    await loadSecurity();
    return "ready";
  } catch (e) {
    if (e instanceof SecurityError && e.code === "clock") return "clock";
    return "invalid";
  }
}
// Used by the pairing flow and tests to turn a proto SecurityState list
// into parsed states and recovery proofs.
export async function parseChain(
  raw: { state: Uint8Array; recovery: Uint8Array }[],
): Promise<{
  states: SecurityState[];
  proofs: (RecoveryStatement | undefined)[];
}> {
  if (!raw.length || raw.length > MAX_STATES)
    throw new SecurityError("invalid");
  const states: SecurityState[] = [];
  const proofs: (RecoveryStatement | undefined)[] = [];
  for (const s of raw) {
    states.push(await parseState(new Uint8Array(s.state)));
    proofs.push(
      s.recovery.length
        ? await parseRecoveryStatement(new Uint8Array(s.recovery))
        : undefined,
    );
  }
  return { states, proofs };
}
