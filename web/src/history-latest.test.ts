import { afterEach, beforeAll, expect, it, vi } from "vitest";
import { importSessionKey } from "./crypto";
import { Remote } from "./remote";
import { securityFixture, storedEvent, storedSecurity } from "./test/stored-security-fixture";

// Three events exercise the same page-boundary branches as production's 500,
// including two prefix pages and a tail/live overlap, without 3,000 signatures.
const PAGE = 3;
const SID = "synthetic-session";
let fixture: Awaited<ReturnType<typeof securityFixture>>;
let rows: { seq: bigint; envelope: Awaited<ReturnType<typeof storedEvent>>["envelope"] }[];
let seed: Uint8Array<ArrayBuffer>;
beforeAll(async () => {
  fixture = await securityFixture();
  rows = [];
  for (let seq = 1n; seq <= BigInt(2 * PAGE + 3); seq++) {
    const event = await storedEvent(fixture, seq);
    seed = event.seed;
    rows.push({ seq, envelope: event.envelope });
  }
});

class HistoryRemote extends Remote {
  constructor(pageSize?: number) {
    super({ server: "https://relay.example.com", deviceId: fixture.memberID,
      devicePrivate: fixture.ed }, undefined, undefined, undefined, pageSize);
  }
}
let remote: HistoryRemote | undefined;
afterEach(() => { remote?.stop(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });
async function probe(n: number, pageSize: number | null = PAGE) {
  await storedSecurity();
  const r = remote = new HistoryRemote(pageSize ?? undefined);
  const machineId = fixture.states.at(-1)!.core.members.find(m => m.kind === 1)!.id;
  r.sessions.set(SID, { id: SID, machineId, key: await importSessionKey(seed),
    metaRev: 0n, lastSeq: BigInt(n), updatedMs: 0n, endedMs: 0n });
  (r as any).sessionSeeds.set(SID, seed);
  return r;
}
function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
}
// Wait for actual state notifications, without polling or a latency assertion.
function until(r: Remote, ready: () => boolean) {
  if (ready()) return Promise.resolve();
  return new Promise<void>(resolve => {
    const off = r.subscribe(() => { if (ready()) { off(); resolve(); } });
  });
}

it("renders the latest page after one request, then joins the prefix with live events without duplicates", async () => {
  const count = 2 * PAGE + 2;
  const r = await probe(count);
  const gate = deferred();
  const prefixRequested = deferred();
  const getEvents = vi.fn(async ({ afterSeq, limit }: { afterSeq: bigint; limit: number }) => {
    if (afterSeq === 0n) { prefixRequested.resolve(); await gate.promise; }
    const rest = rows.slice(0, count).filter(e => e.seq > afterSeq);
    return { events: rest.slice(0, limit), more: rest.length > limit };
  });
  (r as any).api = { getEvents };
  r.events(SID);
  await prefixRequested.promise;
  expect(getEvents.mock.calls[0][0]).toEqual({ sessionId: SID, afterSeq: BigInt(count - PAGE), limit: PAGE });
  expect(r.events(SID).map(e => e.seq)).toEqual(rows.slice(count - PAGE, count).map(e => e.seq));
  expect(r.history(SID)).toMatchObject({ loaded: false, backfilling: true });
  // Overlap two downloaded events and append one real signed live event.
  await (r as any).ingest(r.sessions.get(SID), rows.slice(count - 2), true, false, false);
  expect(r.events(SID).at(-1)?.seq).toBe(BigInt(count + 1));
  expect(new Set(r.events(SID).map(e => e.seq)).size).toBe(PAGE + 1);
  const completed = until(r, () => r.loaded(SID));
  gate.resolve();
  await completed;
  expect(r.events(SID).map(e => e.seq)).toEqual(rows.map(e => e.seq));
  expect(r.history(SID).backfilling).toBe(false);
  expect(getEvents).toHaveBeenCalledTimes(3);
  expect((r as any).buffers.get(SID).pending.size).toBe(0);
});

it("keeps a suffix separate from a gap and reports incomplete history rather than claiming completion", async () => {
  const count = PAGE + 2;
  const r = await probe(count);
  const all = rows.slice(0, count);
  const getEvents = vi.fn(async ({ afterSeq, limit }: { afterSeq: bigint; limit: number }) => {
    const rest = all.filter(e => e.seq > afterSeq && e.seq !== 2n);
    return { events: rest.slice(0, limit), more: rest.length > limit };
  });
  (r as any).api = { getEvents };
  r.events(SID);
  await until(r, () => !!r.history(SID).error);
  expect(r.loaded(SID)).toBe(false);
  expect(r.events(SID).map(e => e.seq)).toEqual(all.slice(2).map(e => e.seq));
  expect((r as any).buffers.get(SID).events.map((e: { seq: bigint }) => e.seq)).toEqual([1n]);
  (r as any).api = { getEvents: async () => ({ events: all, more: false }) };
  r.retryEvents(SID);
  await until(r, () => r.loaded(SID));
  expect(r.events(SID).map(e => e.seq)).toEqual(all.map(e => e.seq));
});

for (const n of [0, PAGE - 1, PAGE]) it(`opens ${n} events with one request from seq zero`, async () => {
  const r = await probe(n);
  const all = rows.slice(0, n);
  const getEvents = vi.fn(async () => ({ events: all, more: false }));
  (r as any).api = { getEvents };
  r.events(SID);
  await until(r, () => r.loaded(SID));
  expect(getEvents).toHaveBeenCalledExactlyOnceWith({ sessionId: SID, afterSeq: 0n, limit: PAGE });
  expect(r.events(SID).map(e => e.seq)).toEqual(all.map(e => e.seq));
});

it("keeps the production default at 500 events per request", async () => {
  const r = await probe(0, null);
  const getEvents = vi.fn(async () => ({ events: [], more: false }));
  (r as any).api = { getEvents };
  r.events(SID);
  await until(r, () => r.loaded(SID));
  expect(getEvents).toHaveBeenCalledExactlyOnceWith({ sessionId: SID, afterSeq: 0n, limit: 500 });
});

it("rejects a moved or corrupt tail event using the normal historical verifier", async () => {
  const r = await probe(PAGE + 2);
  (r as any).api = { getEvents: async ({ afterSeq }: { afterSeq: bigint }) => ({
    events: afterSeq ? [{ ...rows[2], seq: 4n }] : rows.slice(0, 2), more: false,
  }) };
  r.events(SID);
  await until(r, () => r.history(SID).broken);
  // Let the valid-prefix fetch finish as well as the broken-tail notification.
  await until(r, () => r.loaded(SID));
  expect(r.events(SID).map(e => e.seq)).toEqual([1n, 2n]);
  expect(r.events(SID).some(e => e.seq === 4n)).toBe(false);
});
