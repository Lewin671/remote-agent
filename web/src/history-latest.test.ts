import { afterAll, afterEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
import { PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import { SyntheticRemote } from "./test/remote-fixture";
import { removeFixtureBuild } from "./test/security-fixture";
const remotes: SyntheticRemote[] = [];
afterAll(removeFixtureBuild);
afterEach(async () => { for (const r of remotes.splice(0)) await r.dispose(); vi.restoreAllMocks(); });
async function probe(n: number) {
  const r = await SyntheticRemote.create();
  remotes.push(r);
  const rows = [];
  for (let seq = 1n; seq <= BigInt(n); seq++) rows.push({ seq, envelope: await r.seal(
    { kind: PayloadKind.EVENT, machineId: r.machine, sessionId: "s", seq }, EventSchema,
    create(EventSchema, { seq, sessionId: "s", kind: { case: "agentMessage", value: { text: "event " + seq } } }),
  ) });
  r.sessions.get("s")!.lastSeq = BigInt(n);
  return { r, rows };
}
function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
}

it("renders the latest page after one request, then joins the prefix with live events without duplicates", async () => {
  const { r, rows } = await probe(1003);
  // Metadata is one event behind; the live stream supplies the next event.
  r.sessions.get("s")!.lastSeq = 1002n;
  const gate = deferred();
  const getEvents = vi.fn(async ({ afterSeq, limit }: { afterSeq: bigint; limit: number }) => {
    if (afterSeq === 0n) await gate.promise;
    const rest = rows.slice(0, 1002).filter(e => e.seq > afterSeq);
    return { events: rest.slice(0, limit), more: rest.length > limit };
  });
  (r as any).api = { getEvents };
  r.events("s");
  await vi.waitFor(() => expect(getEvents).toHaveBeenCalledTimes(2), { timeout: 10_000 });
  expect(getEvents.mock.calls[0][0].afterSeq).toBe(502n);
  expect(r.events("s").map(e => e.seq)).toEqual(rows.slice(502, 1002).map(e => e.seq));
  expect(r.history("s")).toMatchObject({ loaded: false, backfilling: true });
  await (r as any).ingest(r.sessions.get("s"), rows.slice(1000), true, false, false);
  expect(r.events("s").at(-1)?.seq).toBe(1003n);
  expect(new Set(r.events("s").map(e => e.seq)).size).toBe(501);
  gate.resolve();
  await vi.waitFor(() => expect(r.loaded("s")).toBe(true), { timeout: 10_000 });
  expect(r.events("s").map(e => e.seq)).toEqual(rows.map(e => e.seq));
  expect(r.history("s").backfilling).toBe(false);
  // The initial tail response already proved completion at this snapshot;
  // backfill does not download the tail again or probe an empty page.
  expect(getEvents).toHaveBeenCalledTimes(3);
  expect((r as any).buffers.get("s").pending.size).toBe(0);
}, 30_000);

it("keeps a suffix separate from a gap and reports incomplete history rather than claiming completion", async () => {
  const { r, rows } = await probe(502);
  const getEvents = vi.fn(async ({ afterSeq, limit }: { afterSeq: bigint; limit: number }) => {
    const rest = rows.filter(e => e.seq > afterSeq && e.seq !== 2n);
    return { events: rest.slice(0, limit), more: rest.length > limit };
  });
  (r as any).api = { getEvents };
  r.events("s");
  await vi.waitFor(() => expect(r.history("s").error).not.toBe(""), { timeout: 10_000 });
  expect(r.loaded("s")).toBe(false);
  expect(r.events("s").map(e => e.seq)).toEqual(rows.slice(2).map(e => e.seq));
  expect((r as any).buffers.get("s").events.map((e: { seq: bigint }) => e.seq)).toEqual([1n]);
  // Repairing the gap joins the already verified suffix; duplicate rows are harmless.
  (r as any).api = { getEvents: async () => ({ events: rows, more: false }) };
  r.retryEvents("s");
  await vi.waitFor(() => expect(r.loaded("s")).toBe(true), { timeout: 10_000 });
  expect(r.events("s").map(e => e.seq)).toEqual(rows.map(e => e.seq));
}, 30_000);

for (const n of [0, 3, 500]) it(`opens ${n} events with one request from seq zero`, async () => {
  const { r, rows } = await probe(n);
  const getEvents = vi.fn(async () => ({ events: rows, more: false }));
  (r as any).api = { getEvents };
  r.events("s");
  await vi.waitFor(() => expect(r.loaded("s")).toBe(true), { timeout: 10_000 });
  expect(getEvents).toHaveBeenCalledExactlyOnceWith({ sessionId: "s", afterSeq: 0n, limit: 500 });
  expect(r.events("s").map(e => e.seq)).toEqual(rows.map(e => e.seq));
}, 30_000);

it("rejects a moved or corrupt tail event using the normal historical verifier", async () => {
  const { r, rows } = await probe(502);
  const getEvents = vi.fn(async ({ afterSeq }: { afterSeq: bigint }) => ({
    events: afterSeq ? [{ ...rows[2], seq: 4n }] : rows.slice(0, 2), more: false,
  }));
  (r as any).api = { getEvents };
  r.events("s");
  await vi.waitFor(() => expect(r.history("s").broken).toBe(true));
  expect(r.events("s").map(e => e.seq)).toEqual([1n, 2n]);
  expect(r.events("s").some(e => e.seq === 4n)).toBe(false);
}, 30_000);
