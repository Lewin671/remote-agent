import { create } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
import { PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import type { CachedEvent } from "./eventcache";
import { SyntheticRemote } from "./test/remote-fixture";

let remote: SyntheticRemote | undefined;
afterEach(async () => { await remote?.dispose(); remote = undefined; });
it("notifies after each verified cache batch and leaves scroll restoration pending until catch-up finishes", async () => {
  const r = remote = await SyntheticRemote.create();
  const all: CachedEvent[] = [];
  for (let seq = 1n; seq <= 3n; seq++) all.push({ seq,
    envelope: await r.seal({ kind: PayloadKind.EVENT, sessionId: "s", seq }, EventSchema,
      create(EventSchema, { sessionId: "s", seq, kind: { case: "agentMessage", value: { text: "m" + seq } } })),
  });
  let release!: () => void, entered!: () => void;
  const barrier = new Promise<void>(resolve => { release = resolve; });
  const waiting = new Promise<void>(resolve => { entered = resolve; });
  const read = vi.fn(async (_sid: string, after = 0n) => {
    if (after === 1n) { entered(); await barrier; }
    return all.filter(e => e.seq > after).slice(0, 1);
  });
  (r as any).cache = { read, drop: vi.fn(), append: vi.fn(async () => {}), set: vi.fn(async () => {}) };
  r.sessions.get("s")!.lastSeq = 3n;
  const getEvents = vi.fn(async () => ({ events: [], more: false }));
  (r as any).api = { getEvents };
  const progress: number[] = [];
  let finished!: () => void;
  const loaded = new Promise<void>(resolve => { finished = resolve; });
  const off = r.subscribe(() => {
    progress.push(r.events("s").length);
    if (r.loaded("s")) finished();
  });
  r.events("s");
  const pausedBetweenBatches = await Promise.race([waiting.then(() => true), loaded.then(() => false)]);
  try {
    expect(pausedBetweenBatches).toBe(true);
    expect(progress).toContain(1);
    expect(r.history("s").loaded).toBe(false);
    expect(getEvents).not.toHaveBeenCalled();
  } finally { release(); }
  await loaded;
  off();
  expect(progress).toContain(2);
  expect(progress).toContain(3);
  expect(read.mock.calls.map(([, after]) => after ?? 0n)).toEqual([0n, 1n, 2n, 3n]);
  expect(getEvents).toHaveBeenCalledWith(expect.objectContaining({ afterSeq: 3n }));
  expect(r.events("s").map(e => e.seq)).toEqual([1n, 2n, 3n]);
}, 20000);

it("draws the whole cached copy before the server has answered", async () => {
  const r = remote = await SyntheticRemote.create();
  const all: CachedEvent[] = [];
  for (let seq = 1n; seq <= 3n; seq++) all.push({ seq,
    envelope: await r.seal({ kind: PayloadKind.EVENT, sessionId: "s", seq }, EventSchema,
      create(EventSchema, { sessionId: "s", seq, kind: { case: "agentMessage", value: { text: "m" + seq } } })),
  });
  (r as any).cache = { read: vi.fn(async (_sid: string, after = 0n) => all.filter(e => e.seq > after).slice(0, 2)), drop: vi.fn(), append: vi.fn(async () => {}), set: vi.fn(async () => {}) };
  r.sessions.get("s")!.lastSeq = 3n;
  let release!: () => void, asked!: () => void;
  const barrier = new Promise<void>(resolve => { release = resolve; });
  const answered = new Promise<void>(resolve => { asked = resolve; });
  (r as any).api = { getEvents: vi.fn(async () => { asked(); await barrier; return { events: [], more: false }; }) };
  r.events("s");
  await answered;
  // The network request is out; the cached copy is all in and can be drawn.
  expect(r.events("s").map(e => e.seq)).toEqual([1n, 2n, 3n]);
  expect(r.history("s")).toMatchObject({ loaded: false, ready: true });
  const loaded = new Promise<void>(resolve => { const off = r.subscribe(() => { if (r.loaded("s")) { off(); resolve(); } }); });
  release();
  await loaded;
  expect(r.history("s")).toMatchObject({ loaded: true, ready: true });
}, 20000);

it("does not draw the cached copy when the latest page is to replace it", async () => {
  const r = remote = await SyntheticRemote.create();
  const rows: CachedEvent[] = [];
  for (let seq = 1n; seq <= 2n; seq++) rows.push({ seq,
    envelope: await r.seal({ kind: PayloadKind.EVENT, sessionId: "s", seq }, EventSchema,
      create(EventSchema, { sessionId: "s", seq, kind: { case: "agentMessage", value: { text: "m" + seq } } })),
  });
  (r as any).cache = { read: vi.fn(async (_sid: string, after = 0n) => rows.filter(e => e.seq > after)), drop: vi.fn(), append: vi.fn(async () => {}), set: vi.fn(async () => {}) };
  // The server is far ahead of the cached copy: its latest page is shown instead.
  r.sessions.get("s")!.lastSeq = 5000n;
  let release!: () => void, asked!: () => void;
  const barrier = new Promise<void>(resolve => { release = resolve; });
  const answered = new Promise<void>(resolve => { asked = resolve; });
  (r as any).api = { getEvents: vi.fn(async () => { asked(); await barrier; return { events: [], more: false }; }) };
  r.events("s");
  await answered;
  expect(r.history("s").ready).toBe(false);
  release();
}, 20000);

it("shows the verified cached tail before restoring the prefix, without downloading it again", async () => {
  const { CACHE_PAGE_SIZE } = await import("./eventcache");
  const r = remote = await SyntheticRemote.create();
  const count = CACHE_PAGE_SIZE + 2;
  const all: CachedEvent[] = [];
  for (let seq = 1n; seq <= BigInt(count); seq++) all.push({ seq,
    envelope: await r.seal({ kind: PayloadKind.EVENT, sessionId: "s", seq }, EventSchema,
      create(EventSchema, { sessionId: "s", seq, kind: { case: "agentMessage", value: { text: "message " + seq } } })),
  });
  let release!: () => void, entered!: () => void;
  const barrier = new Promise<void>(resolve => { release = resolve; });
  const waiting = new Promise<void>(resolve => { entered = resolve; });
  const read = vi.fn(async (_sid: string, after = 0n) => {
    if (!after) { entered(); await barrier; }
    return all.filter(e => e.seq > after).slice(0, CACHE_PAGE_SIZE);
  });
  (r as any).cache = { read, drop: vi.fn(), append: vi.fn(async () => {}), set: vi.fn(async () => {}) };
  r.sessions.get("s")!.lastSeq = BigInt(count);
  const getEvents = vi.fn(async () => ({ events: [], more: false }));
  (r as any).api = { getEvents };
  const loaded = new Promise<void>(resolve => { const off = r.subscribe(() => { if (r.loaded("s")) { off(); resolve(); } }); });
  r.events("s");
  await waiting;
  try {
    expect(r.history("s")).toMatchObject({ loaded: false, ready: true, backfilling: true });
    expect(r.events("s").at(-1)?.kind).toMatchObject({ case: "agentMessage", value: { text: `message ${count}` } });
    expect(r.events("s")[0].seq).toBe(3n);
    expect(getEvents).not.toHaveBeenCalled();
  } finally { release(); }
  await loaded;
  expect(r.events("s").map(e => e.seq)).toEqual(all.map(e => e.seq));
  expect(getEvents).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ afterSeq: BigInt(count) }));
}, 20000);
