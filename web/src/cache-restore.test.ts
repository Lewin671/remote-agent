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
