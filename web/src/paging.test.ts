// The listing of sessions a page at a time (architecture §8 会话列表分页).

import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { expect, it } from "vitest";
import { PayloadKind, seal, sealMessage } from "./crypto";
import { SessionSchema } from "./gen/remoteagent/v1/local_pb";
import { SessionStatus } from "./gen/remoteagent/v1/event_pb";
import { MachineMetaSchema } from "./gen/remoteagent/v1/remote_pb";
import type { RemoteSession, SessionCursor } from "./gen/remoteagent/v1/relay_pb";
import { projects } from "./model";
import { Remote, SESSION_PAGE } from "./remote";

async function setup() {
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  const raw = crypto.getRandomValues(new Uint8Array(32));
  const r = new (class extends Remote {
    constructor() {
      super({ server: "https://ra.example.com", root: key, rootRaw: new Uint8Array(32), devicePrivate: device.privateKey, deviceId: "d" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key });
    }
  })();
  // The server's side: sessions with the time they ended, 0 while live.
  const server = new Map<string, { machineId: string; endedMs: number }>();
  let machines = ["m1"];
  const calls: { after?: SessionCursor; limit?: number; sessionIds?: string[] }[] = [];
  let failNext = false;
  let onList: (() => void) | undefined; // runs while the next listing is in flight
  const remote = async (id: string): Promise<RemoteSession> => {
    const s = server.get(id)!;
    const info = create(SessionSchema, { id, cwd: "/p", title: id, status: s.endedMs ? SessionStatus.EXITED : SessionStatus.IDLE, endedAt: s.endedMs ? timestampFromMs(s.endedMs) : undefined });
    return {
      $typeName: "remoteagent.v1.RemoteSession",
      machineId: s.machineId,
      sessionId: id,
      key: await seal(key, { kind: PayloadKind.SESSION_KEY, machineId: s.machineId, sessionId: id }, raw),
      meta: await sealMessage(await crypto.subtle.importKey("raw", raw, "AES-GCM", false, ["encrypt"]), { kind: PayloadKind.SESSION_META, machineId: s.machineId, sessionId: id, seq: 1n }, SessionSchema, info),
      lastSeq: 0n,
      updatedMs: 1n,
      endedMs: BigInt(s.endedMs),
    };
  };
  const ordered = () => [...server].filter(([, s]) => s.endedMs).sort((a, b) => b[1].endedMs - a[1].endedMs || (a[0] < b[0] ? -1 : 1));
  (r as unknown as { api: object }).api = {
    listMachines: async () => ({ machines: machines.map((id) => ({ id, online: true, lastSeenMs: 0n })) }),
    listRemoteSessions: async (req: { after?: SessionCursor; limit?: number; sessionIds?: string[] }) => {
      calls.push(req);
      const hook = onList;
      onList = undefined;
      hook?.();
      if (failNext) {
        failNext = false;
        throw new Error("offline");
      }
      if (req.sessionIds?.length) return { sessions: await Promise.all(req.sessionIds.filter((id) => server.has(id)).map(remote)) };
      const after = req.after;
      const rest = ordered().filter(([id, s]) => !after || BigInt(s.endedMs) < after.endedMs || (BigInt(s.endedMs) === after.endedMs && id > after.sessionId));
      const page = rest.slice(0, req.limit);
      const last = page[page.length - 1];
      const live = after ? [] : [...server].filter(([, s]) => !s.endedMs);
      return {
        sessions: await Promise.all([...live, ...page].map(([id]) => remote(id))),
        next: rest.length > page.length ? { endedMs: BigInt(last[1].endedMs), sessionId: last[0] } : undefined,
      };
    },
  };
  r.conn = "online";
  return { r, server, calls, setMachines: (m: string[]) => (machines = m), fail: () => (failNext = true), before: (fn: () => void) => (onList = fn) };
}

const ended = (n: number) => `e${String(n).padStart(3, "0")}`;

it("fetches live sessions and the latest ended ones first, then pages on demand", async () => {
  const { r, server, calls } = await setup();
  server.set("live", { machineId: "m1", endedMs: 0 });
  for (let i = 1; i <= 120; i++) server.set(ended(i), { machineId: "m1", endedMs: 1000 + i });
  await r.refresh();
  expect(r.listed).toBe(true);
  expect(r.sessions.size).toBe(1 + SESSION_PAGE);
  expect(r.sessions.has("live")).toBe(true);
  expect(r.sessions.has(ended(120))).toBe(true);
  expect(r.sessions.has(ended(70))).toBe(false);
  expect(r.moreSessions).toBe(true);
  expect(r.sessions.get(ended(120))!.endedMs).toBe(1120n);

  // Two askers share one page.
  const [a, b] = await Promise.all([r.loadMoreSessions(), r.loadMoreSessions()]);
  expect([a, b]).toEqual([true, true]);
  expect(calls.filter((c) => c.after).length).toBe(1);
  expect(r.sessions.size).toBe(1 + 2 * SESSION_PAGE);
  expect(r.loadingSessions).toBe(false);

  await r.loadAllSessions();
  expect(r.sessions.size).toBe(121);
  expect(r.moreSessions).toBe(false);
  expect(await r.loadMoreSessions()).toBe(false);
});

it("asks for a session outside the pages by its ID, and notes one that does not exist", async () => {
  const { r, server, calls } = await setup();
  for (let i = 1; i <= 80; i++) server.set(ended(i), { machineId: "m1", endedMs: 1000 + i });
  await r.refresh();
  expect(r.sessions.has(ended(3))).toBe(false);
  await r.ensureSession(ended(3));
  expect(r.sessions.get(ended(3))?.info?.title).toBe(ended(3));
  await r.ensureSession("nope");
  expect(r.missing.has("nope")).toBe(true);
  // Neither is asked for twice.
  const n = calls.length;
  await r.ensureSession(ended(3));
  await r.ensureSession("nope");
  expect(calls.length).toBe(n);
});

it("keeps what belongs to later pages when the first page is fetched again", async () => {
  const { r, server, setMachines } = await setup();
  server.set("live", { machineId: "m1", endedMs: 0 });
  server.set("other", { machineId: "m2", endedMs: 0 });
  for (let i = 1; i <= 120; i++) server.set(ended(i), { machineId: i === 2 ? "m2" : "m1", endedMs: 1000 + i });
  setMachines(["m1", "m2"]);
  await r.refresh();
  await r.loadAllSessions();
  expect(r.sessions.size).toBe(122);

  // Meanwhile: a recent session and a live one are gone from the server,
  // as is a machine with a session far down the listing.
  server.delete(ended(119));
  server.delete("live");
  server.delete("other");
  server.delete(ended(2));
  setMachines(["m1"]);
  await r.refresh();
  expect(r.sessions.has(ended(119))).toBe(false); // within the first page, and not on it
  expect(r.sessions.has("live")).toBe(false); // live sessions are all on the first page
  expect(r.sessions.has(ended(2))).toBe(false); // its machine is gone
  expect(r.sessions.has(ended(1))).toBe(true); // belongs to a later page
  expect(r.sessions.has(ended(120))).toBe(true);
  expect(r.moreSessions).toBe(true);
});

it("does not ask again at once for a page that failed", async () => {
  const { r, server, calls, fail } = await setup();
  for (let i = 1; i <= 80; i++) server.set(ended(i), { machineId: "m1", endedMs: 1000 + i });
  await r.refresh();
  fail();
  expect(await r.loadMoreSessions()).toBe(false);
  const n = calls.length;
  expect(await r.loadMoreSessions()).toBe(false);
  expect(calls.length).toBe(n);
  expect(r.moreSessions).toBe(true);
  expect(r.loadingSessions).toBe(false);
});

it("knows a project from its machine when none of its sessions are held", () => {
  const meta = create(MachineMetaSchema, { projects: [{ path: "/old", ended: 12, latestCreatedMs: 5000n, latestEndedMs: 6000n }, { path: "/p", ended: 40, latestCreatedMs: 100n }] });
  const held = [
    { id: "a", machineId: "m2", updatedMs: 9000n, info: create(SessionSchema, { cwd: "/p", status: SessionStatus.IDLE }) },
    { id: "b", machineId: "m2", updatedMs: 8000n, info: create(SessionSchema, { cwd: "/p", status: SessionStatus.EXITED }) },
    { id: "c", machineId: "m2", updatedMs: 7000n, info: create(SessionSchema, { cwd: "/new", status: SessionStatus.EXITED }) },
  ];
  const list = projects(held, [{ id: "m1", online: true, lastSeenMs: 0n, meta }]);
  const by = Object.fromEntries(list.map((p) => [p.key, p]));
  expect(by["/old"]).toMatchObject({ ended: 12, hasLive: false, latestCreatedMs: 5000, updatedMs: 6000, machines: ["m1"] });
  expect(by["/old"].sessions).toHaveLength(0);
  // Counted machine by machine: m1 says 40, and one of m2, which says nothing, is held.
  expect(by["/p"]).toMatchObject({ ended: 41, hasLive: true, machines: ["m2", "m1"] });
  // No machine counts it yet: what is held is the least there can be.
  expect(by["/new"].ended).toBe(1);
});

it("fetches what comes before a session held past the pages", async () => {
  const { r, server } = await setup();
  for (let i = 1; i <= 120; i++) server.set(ended(i), { machineId: "m1", endedMs: 1000 + i });
  await r.refresh();
  // Fetched by its ID, it lies beyond the first page: sessions not here yet come before it.
  await r.ensureSession(ended(5));
  expect(r.pastPages(r.sessions.get(ended(5))!)).toBe(true);
  expect(r.pastPages(r.sessions.get(ended(100))!)).toBe(false);
  await r.loadAllSessions();
  expect(r.pastPages(r.sessions.get(ended(5))!)).toBe(false);
});

it("does not bring back the sessions of a machine removed while a page was on its way", async () => {
  const { r, server, before } = await setup();
  for (let i = 1; i <= 80; i++) server.set(ended(i), { machineId: "m1", endedMs: 1000 + i });
  await r.refresh();
  const removed = () => {
    const x = r as unknown as { removedPushes: Map<string, number>; sessions: Map<string, unknown> };
    x.removedPushes.set("m1", (x.removedPushes.get("m1") ?? 0) + 1);
    x.sessions.clear();
  };
  before(removed);
  await r.loadMoreSessions();
  expect(r.sessions.size).toBe(0);
  before(removed);
  await r.ensureSession(ended(1));
  expect(r.sessions.size).toBe(0);
});

it("does not ask again at once for a session whose lookup failed", async () => {
  const { r, server, calls, fail } = await setup();
  server.set(ended(1), { machineId: "m1", endedMs: 1001 });
  fail();
  await r.ensureSession(ended(1));
  const n = calls.length;
  await r.ensureSession(ended(1));
  expect(calls.length).toBe(n);
  expect(r.missing.has(ended(1))).toBe(false);
});

it("drops a page that was on its way when the listing started over", async () => {
  const { r, server, before, setMachines } = await setup();
  server.set(ended(200), { machineId: "m1", endedMs: 5000 });
  for (let i = 1; i <= 80; i++) server.set(ended(i), { machineId: "m2", endedMs: 1000 + i });
  setMachines(["m1", "m2"]);
  await r.refresh();
  // While the second page is in flight, m2 turns out to have been removed.
  let again: Promise<void> | undefined;
  before(() => {
    setMachines(["m1"]);
    for (let i = 1; i <= 80; i++) server.delete(ended(i));
    again = r.refresh();
  });
  await r.loadMoreSessions();
  await again;
  expect([...r.sessions.keys()]).toEqual([ended(200)]);
  expect(r.moreSessions).toBe(false);
});
