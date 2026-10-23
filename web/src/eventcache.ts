// Sealed events kept in this browser, so that opening a session again
// downloads only what is new. They are stored as the server sent them:
// still encrypted, and opened and checked like a fresh download. Next to
// them are the last listing of machines and sessions and the account's
// project preferences, sealed likewise, the login token, and the model
// lists and settings the machines last reported (decoded: they name
// models and defaults), so that the page shows what it knew and reaches
// the server without logging in again.

import { fromBinary, toBinary } from "@bufbuild/protobuf";
import { EnvelopeSchema, type Envelope } from "./gen/remoteagent/v1/envelope_pb";

export interface CachedEvent {
  seq: bigint;
  envelope: Envelope;
}

export interface EventCache {
  /** A session's events from seq 1, as far as they are contiguous. */
  read(sid: string): Promise<CachedEvent[]>;
  append(sid: string, events: CachedEvent[]): Promise<void>;
  drop(sid: string): Promise<void>;
  /** Forgets the least recently opened sessions until the rest fits the
   * budget. Which sessions the server still has is not known here: the
   * listing comes a page at a time. */
  trim(): Promise<void>;
  /** Small values by name: the last listings, the login token. */
  get(name: string): Promise<unknown>;
  set(name: string, value: unknown): Promise<void>;
}

const DB = "remote-agent-events";
const EVENTS = "events"; // [session ID, seq] -> envelope bytes
const SESSIONS = "sessions"; // session ID -> Usage
const VALUES = "values"; // name -> value
const BUDGET = 256 * 1024 * 1024;

interface Usage {
  bytes: number;
  usedAt: number;
}

let opening: Promise<IDBDatabase> | undefined;
let trimJob: Promise<void> | undefined;
let trimTimer: ReturnType<typeof setTimeout> | undefined;

function scheduleTrim() {
  if (trimTimer !== undefined) return;
  trimTimer = setTimeout(() => {
    trimTimer = undefined;
    // An append during a trim needs a pass after that trim's snapshot.
    void (async () => {
      await trimJob?.catch(() => undefined);
      await idbCache.trim();
    })().catch(() => undefined);
  }, 1000);
}

function db(): Promise<IDBDatabase> {
  return (opening ??= new Promise<IDBDatabase>((resolve, reject) => {
    const req = indexedDB.open(DB, 2);
    req.onupgradeneeded = () => {
      for (const name of [EVENTS, SESSIONS, VALUES]) if (!req.result.objectStoreNames.contains(name)) req.result.createObjectStore(name);
    };
    req.onsuccess = () => {
      // Deleting the database (forgetting the account) must not wait for
      // this connection.
      req.result.onversionchange = () => {
        req.result.close();
        opening = undefined;
      };
      resolve(req.result);
    };
    req.onerror = () => reject(req.error);
  }).catch((e) => {
    opening = undefined;
    throw e;
  }));
}

function done(tx: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onerror = tx.onabort = () => reject(tx.error);
  });
}

function result<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

const of = (sid: string) => IDBKeyRange.bound([sid, 0], [sid, Infinity]);

function forget(tx: IDBTransaction, sid: string) {
  tx.objectStore(EVENTS).delete(of(sid));
  tx.objectStore(SESSIONS).delete(sid);
}

const idbCache: EventCache = {
  async read(sid) {
    const tx = (await db()).transaction([EVENTS, SESSIONS], "readwrite");
    const keys = result(tx.objectStore(EVENTS).getAllKeys(of(sid)));
    const values = result(tx.objectStore(EVENTS).getAll(of(sid)) as IDBRequest<Uint8Array[]>);
    const usage = (await result(tx.objectStore(SESSIONS).get(sid) as IDBRequest<Usage | undefined>)) ?? { bytes: 0, usedAt: 0 };
    tx.objectStore(SESSIONS).put({ ...usage, usedAt: Date.now() }, sid);
    const [ks, vs] = [await keys, await values];
    await done(tx);
    const out: CachedEvent[] = [];
    for (let i = 0; i < ks.length; i++) {
      if ((ks[i] as [string, number])[1] !== i + 1) break;
      try {
        out.push({ seq: BigInt(i + 1), envelope: fromBinary(EnvelopeSchema, vs[i]) });
      } catch {
        break;
      }
    }
    return out;
  },
  async append(sid, events) {
    const rows = events.map((e) => [Number(e.seq), toBinary(EnvelopeSchema, e.envelope)] as const);
    const tx = (await db()).transaction([EVENTS, SESSIONS], "readwrite");
    for (const [seq, bytes] of rows) tx.objectStore(EVENTS).put(bytes, [sid, seq]);
    const usage = (await result(tx.objectStore(SESSIONS).get(sid) as IDBRequest<Usage | undefined>)) ?? { bytes: 0, usedAt: 0 };
    tx.objectStore(SESSIONS).put({ bytes: usage.bytes + rows.reduce((n, [, b]) => n + b.length, 0), usedAt: Date.now() }, sid);
    await done(tx);
    scheduleTrim();
  },
  async drop(sid) {
    const tx = (await db()).transaction([EVENTS, SESSIONS], "readwrite");
    forget(tx, sid);
    await done(tx);
  },
  trim() {
    if (trimJob) return trimJob;
    trimJob = trimCache().finally(() => { trimJob = undefined; });
    return trimJob;
  },
  async get(name) {
    return result((await db()).transaction(VALUES).objectStore(VALUES).get(name));
  },
  async set(name, value) {
    const tx = (await db()).transaction(VALUES, "readwrite");
    if (value === undefined) tx.objectStore(VALUES).delete(name);
    else tx.objectStore(VALUES).put(value, name);
    await done(tx);
  },
};

async function trimCache() {
  const tx = (await db()).transaction([EVENTS, SESSIONS], "readwrite");
  const ids = result(tx.objectStore(SESSIONS).getAllKeys() as IDBRequest<string[]>);
  const usages = await result(tx.objectStore(SESSIONS).getAll() as IDBRequest<Usage[]>);
  const kept = (await ids).map((sid, i) => ({ sid, ...usages[i] }));
  kept.sort((a, b) => a.usedAt - b.usedAt);
  let total = kept.reduce((n, s) => n + s.bytes, 0);
  for (const s of kept) {
    if (total <= BUDGET) break;
    forget(tx, s.sid);
    total -= s.bytes;
  }
  await done(tx);
}

/** The cache in this browser's IndexedDB; none where there is no IndexedDB. */
export function eventCache(): EventCache | undefined {
  return typeof indexedDB === "undefined" ? undefined : idbCache;
}

/** Deletes every cached event, with the account they belong to. */
export async function clearEventCache(): Promise<void> {
  clearTimeout(trimTimer);
  trimTimer = undefined;
  await trimJob?.catch(() => undefined);
  if (typeof indexedDB === "undefined") return Promise.resolve();
  return new Promise((resolve) => {
    const req = indexedDB.deleteDatabase(DB);
    req.onsuccess = req.onerror = req.onblocked = () => resolve();
  });
}
