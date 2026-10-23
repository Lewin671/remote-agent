import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { create } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import { EnvelopeSchema } from "./gen/remoteagent/v1/envelope_pb";
import { clearEventCache, eventCache } from "./eventcache";

const event = (seq: bigint) => ({ seq, envelope: create(EnvelopeSchema, { ciphertext: new Uint8Array([1, 2, 3]) }) });
async function usage(sid: string, bytes: number, usedAt: number) {
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const req = indexedDB.open("remote-agent-events", 2);
    req.onsuccess = () => resolve(req.result); req.onerror = () => reject(req.error);
  });
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction("sessions", "readwrite");
    tx.objectStore("sessions").put({ bytes, usedAt }, sid);
    tx.oncomplete = () => resolve(); tx.onerror = () => reject(tx.error);
  });
  db.close();
}
afterEach(async () => { await clearEventCache(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });
it("trims after append activity without a listing refresh, coalescing appends and serializing explicit trims", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const cache = eventCache()!;
  await cache.append("old", [event(1n)]);
  // Account for a large historical session without allocating a load-test payload.
  await usage("old", 256 * 1024 * 1024, 1);
  const trim = vi.spyOn(cache, "trim");
  await cache.append("new", [event(1n)]);
  await cache.append("new", [event(2n)]);
  expect(trim).not.toHaveBeenCalled();
  await vi.advanceTimersByTimeAsync(1000);
  expect(trim).toHaveBeenCalledTimes(1);
  await trim.mock.results[0].value;
  expect(await cache.read("old")).toEqual([]);
  expect((await cache.read("new")).map(e => e.seq)).toEqual([1n, 2n]);
  const first = cache.trim();
  expect(cache.trim()).toBe(first);
  await first;
});
it("never replays a later suffix of an evicted session as a contiguous prefix", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const cache = eventCache()!;
  await cache.append("s", [event(1n)]);
  await usage("s", 256 * 1024 * 1024 + 1, 1);
  const trim = vi.spyOn(cache, "trim");
  await vi.advanceTimersByTimeAsync(1000);
  expect(trim).toHaveBeenCalledTimes(1);
  await trim.mock.results[0].value;
  await cache.append("s", [event(2n)]);
  expect(await cache.read("s")).toEqual([]);
  await cache.drop("s");
  await cache.append("s", [event(1n), event(2n)]);
  expect((await cache.read("s")).map(e => e.seq)).toEqual([1n, 2n]);
});

it("reads bounded cursor batches and stops at a corrupt record or sequence gap", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
  const cache = eventCache()!;
  await cache.append("batch", Array.from({ length: 130 }, (_, i) => event(BigInt(i + 1))));
  const first = await cache.read("batch");
  expect(first).toHaveLength(128);
  expect((await cache.read("batch", 128n)).map(e => e.seq)).toEqual([129n, 130n]);
  expect(await cache.read("batch", 130n)).toEqual([]);
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const req = indexedDB.open("remote-agent-events", 2);
    req.onsuccess = () => resolve(req.result); req.onerror = () => reject(req.error);
  });
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction("events", "readwrite");
    tx.objectStore("events").put(new Uint8Array([255]), ["batch", 129]);
    tx.objectStore("events").delete(["batch", 4]);
    tx.oncomplete = () => resolve(); tx.onerror = () => reject(tx.error);
  });
  db.close();
  expect((await cache.read("batch")).map(e => e.seq)).toEqual([1n, 2n, 3n]);
  expect(await cache.read("batch", 128n)).toEqual([]);
});
