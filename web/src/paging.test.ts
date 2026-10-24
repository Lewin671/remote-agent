// The listing of sessions a page at a time (architecture §8 会话列表分页).

import { create, toBinary } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { afterEach, afterAll, beforeAll, expect, it, vi } from "vitest";
import { SessionSchema } from "./gen/remoteagent/v1/local_pb";
import { SessionStatus } from "./gen/remoteagent/v1/event_pb";
import { MachineMetaSchema } from "./gen/remoteagent/v1/remote_pb";
import type {
  RemoteSession,
  SessionCursor,
} from "./gen/remoteagent/v1/relay_pb";
import { needsAttention, projects } from "./model";
import { todoList } from "./views/Home";
import { Remote, SESSION_PAGE } from "./remote";
import {
  GoSecurityFixture,
  webMember,
  MemorySecurityStorage,
} from "./test/security-fixture";

import type { Envelope } from "./gen/remoteagent/v1/envelope_pb";
// Keep real signed envelopes and independent clients, but exercise page
// boundaries with three rows instead of sealing hundreds of redundant rows.
const PAGE = 3;
const MANY = 2 * PAGE + 2;
const SOME = PAGE + 2;
let world: Awaited<ReturnType<typeof GoSecurityFixture.create>>;
let member: Awaited<ReturnType<typeof webMember>>;
let ids: Record<string, string>;
const raw = new Uint8Array(32).fill(7);
const remotes: Remote[] = [];
beforeAll(async () => {
  world = await GoSecurityFixture.create();
  const m1 = await world.fixture.enroll(world.machine, 1);
  const m2 = await world.fixture.enroll(world.machine, 1);
  ids = { m1: m1.json.member, m2: m2.json.member };
  member = await webMember(world);
});
afterEach(() => {
  for (const r of remotes.splice(0)) r.stop();
  vi.useRealTimers();
});
afterAll(async () => { await world?.fixture.close(); });

async function setup(pageSize: number | null = PAGE) {
  const f = world;
  // Retry windows and signed lease times are logical state, not a CPU budget.
  vi.useFakeTimers({ toFake: ["Date", "setTimeout", "clearTimeout"] });
  vi.setSystemTime(Number(member.record.floor));
  const r = new (class extends Remote {
    constructor() {
      super(member.account, f.legacyKeys, undefined,
        new MemorySecurityStorage(structuredClone(member.record)), undefined, pageSize ?? undefined);
    }
  })();
  remotes.push(r);
  const wrapped = new Map<string, Envelope>();
  const records = new Map<string, { stamp: string; record: RemoteSession }>();
  // The server's side: sessions with the time they ended, 0 while live.
  const server = new Map<string, { machineId: string; endedMs: number; taskOwned?: boolean; pendingApprovals?: number }>();
  let machines = ["m1"];
  // Paging tests need a machine to disappear without the phone: the
  // signed roster is replaced here only to exercise the listing logic.
  (r as unknown as { loadDevices(): Promise<void> }).loadDevices = async () => {
    const state = r as unknown as {
      trustedMembers: Map<string, unknown>;
      devicesStatus: string;
    };
    state.trustedMembers = new Map([
      [member.account.deviceId, { id: member.account.deviceId, kind: 2 }],
      ...machines.map(
        (alias) => [ids[alias], { id: ids[alias], kind: 1 }] as [string, unknown],
      ),
    ]);
    state.devicesStatus = "ready";
  };
  const calls: {
    after?: SessionCursor;
    limit?: number;
    sessionIds?: string[];
  }[] = [];
  let failNext = false;
  let onList: (() => void | Promise<void>) | undefined; // runs while the next listing is in flight
  const remote = async (id: string): Promise<RemoteSession> => {
    const entry = server.get(id)!;
    const machineId = ids[entry.machineId];
    const stamp = JSON.stringify(entry);
    const cached = records.get(id);
    if (cached?.stamp === stamp) return cached.record;
    const info = create(SessionSchema, {
      id,
      taskOwned: entry.taskOwned,
      pendingApprovals: entry.pendingApprovals,
      cwd: "/p",
      title: id,
      status: entry.endedMs ? SessionStatus.EXITED : SessionStatus.IDLE,
      endedAt: entry.endedMs ? timestampFromMs(entry.endedMs) : undefined,
    });
    let key = wrapped.get(id);
    if (!key) {
      key = await f.fixture.seal(
        machineId,
        { kind: 3, machineId, sessionId: id, seq: 1n },
        raw,
      );
      wrapped.set(id, key);
    }
    const meta = await f.fixture.seal(
      machineId,
      { kind: 2, machineId, sessionId: id, seq: 1n },
      toBinary(SessionSchema, info),
    );
    const record: RemoteSession = {
      $typeName: "remoteagent.v1.RemoteSession",
      machineId,
      sessionId: id,
      key,
      meta,
      lastSeq: 0n,
      updatedMs: 1n,
      endedMs: BigInt(entry.endedMs),
    };
    records.set(id, { stamp, record });
    return record;
  };
  const ordered = () =>
    [...server]
      .filter(([, s]) => s.endedMs)
      .sort((a, b) => b[1].endedMs - a[1].endedMs || (a[0] < b[0] ? -1 : 1));
  (r as unknown as { api: object }).api = {
    listMachines: async () => ({
      machines: machines.map((alias) => ({
        id: ids[alias],
        online: true,
        lastSeenMs: 0n,
      })),
    }),
    listDevices: async () => ({ devices: [] }),
    putDeviceMeta: async () => ({ applied: true }),
    listRemoteSessions: async (req: {
      after?: SessionCursor;
      limit?: number;
      sessionIds?: string[];
    }) => {
      calls.push(req);
      const hook = onList;
      onList = undefined;
      await hook?.();
      if (failNext) {
        failNext = false;
        throw new Error("offline");
      }
      if (req.sessionIds?.length)
        return {
          sessions: await Promise.all(
            req.sessionIds.filter((id) => server.has(id)).map(remote),
          ),
        };
      const after = req.after;
      const rest = ordered().filter(
        ([id, s]) =>
          !after ||
          BigInt(s.endedMs) < after.endedMs ||
          (BigInt(s.endedMs) === after.endedMs && id > after.sessionId),
      );
      const page = rest.slice(0, req.limit);
      const last = page[page.length - 1];
      const live = after ? [] : [...server].filter(([, s]) => !s.endedMs);
      return {
        sessions: await Promise.all(
          [...live, ...page].map(([id]) => remote(id)),
        ),
        next:
          rest.length > page.length
            ? { endedMs: BigInt(last[1].endedMs), sessionId: last[0] }
            : undefined,
      };
    },
  };
  r.conn = "online";
  return {
    r,
    server,
    calls,
    ids,
    raw,
    setMachines: async (next: string[]) => {
      machines = next;
    },
    fail: () => (failNext = true),
    before: (fn: () => void | Promise<void>) => (onList = fn),
  };
}

const ended = (n: number) => `e${String(n).padStart(3, "0")}`;

it("fetches live sessions and the latest ended ones first, then pages on demand", async () => {
  const { r, server, calls } = await setup();
  server.set("live", { machineId: "m1", endedMs: 0 });
  for (let i = 1; i <= MANY; i++)
    server.set(ended(i), { machineId: "m1", endedMs: 1000 + i });
  await r.refresh();
  expect(r.listed).toBe(true);
  expect(r.sessions.size).toBe(1 + PAGE);
  expect(r.sessions.has("live")).toBe(true);
  expect(r.sessions.has(ended(MANY))).toBe(true);
  expect(r.sessions.has(ended(MANY - PAGE))).toBe(false);
  expect(r.moreSessions).toBe(true);
  expect(r.sessions.get(ended(MANY))!.endedMs).toBe(BigInt(1000 + MANY));

  // Two askers share one page.
  const [a, b] = await Promise.all([
    r.loadMoreSessions(),
    r.loadMoreSessions(),
  ]);
  expect([a, b]).toEqual([true, true]);
  expect(calls.filter((c) => c.after).length).toBe(1);
  expect(r.sessions.size).toBe(1 + 2 * PAGE);
  expect(r.loadingSessions).toBe(false);

  await r.loadAllSessions();
  expect(r.sessions.size).toBe(1 + MANY);
  expect(r.moreSessions).toBe(false);
  expect(await r.loadMoreSessions()).toBe(false);
});

it("asks for a session outside the pages by its ID, and notes one that does not exist", async () => {
  const { r, server, calls } = await setup();
  for (let i = 1; i <= SOME; i++)
    server.set(ended(i), { machineId: "m1", endedMs: 1000 + i });
  await r.refresh();
  expect(r.sessions.has(ended(1))).toBe(false);
  await r.ensureSession(ended(1));
  expect(r.sessions.get(ended(1))?.info?.title).toBe(ended(1));
  await r.ensureSession("nope");
  expect(r.missing.has("nope")).toBe(true);
  // Neither is asked for twice.
  const n = calls.length;
  await r.ensureSession(ended(1));
  await r.ensureSession("nope");
  expect(calls.length).toBe(n);
});

it("keeps what belongs to later pages when the first page is fetched again", async () => {
  const { r, server, setMachines } = await setup();
  server.set("live", { machineId: "m1", endedMs: 0 });
  server.set("other", { machineId: "m2", endedMs: 0 });
  for (let i = 1; i <= MANY; i++)
    server.set(ended(i), {
      machineId: i === 2 ? "m2" : "m1",
      endedMs: 1000 + i,
    });
  await setMachines(["m1", "m2"]);
  await r.refresh();
  await r.loadAllSessions();
  expect(r.sessions.size).toBe(2 + MANY);

  // Meanwhile: a recent session and a live one are gone from the server,
  // as is a machine with a session far down the listing.
  server.delete(ended(MANY - 1));
  server.delete("live");
  server.delete("other");
  server.delete(ended(2));
  await setMachines(["m1"]);
  await r.refresh();
  expect(r.sessions.has(ended(MANY - 1))).toBe(false); // within the first page, and not on it
  expect(r.sessions.has("live")).toBe(false); // live sessions are all on the first page
  expect(r.sessions.has(ended(2))).toBe(false); // its machine is gone
  expect(r.sessions.has(ended(1))).toBe(true); // belongs to a later page
  expect(r.sessions.has(ended(MANY))).toBe(true);
  expect(r.moreSessions).toBe(true);
});

it("does not ask again at once for a page that failed", async () => {
  const { r, server, calls, fail } = await setup();
  for (let i = 1; i <= SOME; i++)
    server.set(ended(i), { machineId: "m1", endedMs: 1000 + i });
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
  const meta = create(MachineMetaSchema, {
    projects: [
      { path: "/old", ended: 12, latestCreatedMs: 5000n, latestEndedMs: 6000n },
      { path: "/p", ended: 40, latestCreatedMs: 100n },
    ],
  });
  const held = [
    {
      id: "a",
      machineId: "m2",
      updatedMs: 9000n,
      info: create(SessionSchema, { cwd: "/p", status: SessionStatus.IDLE }),
    },
    {
      id: "b",
      machineId: "m2",
      updatedMs: 8000n,
      info: create(SessionSchema, { cwd: "/p", status: SessionStatus.EXITED }),
    },
    {
      id: "c",
      machineId: "m2",
      updatedMs: 7000n,
      info: create(SessionSchema, {
        cwd: "/new",
        status: SessionStatus.EXITED,
      }),
    },
  ];
  const list = projects(held, [
    { id: "m1", online: true, lastSeenMs: 0n, meta },
  ]);
  const by = Object.fromEntries(list.map((p) => [p.key, p]));
  expect(by["/old"]).toMatchObject({
    ended: 12,
    hasLive: false,
    latestCreatedMs: 5000,
    updatedMs: 6000,
    machines: ["m1"],
  });
  expect(by["/old"].sessions).toHaveLength(0);
  // Counted machine by machine: m1 says 40, and one of m2, which says nothing, is held.
  expect(by["/p"]).toMatchObject({
    ended: 41,
    hasLive: true,
    machines: ["m2", "m1"],
  });
  // No machine counts it yet: what is held is the least there can be.
  expect(by["/new"].ended).toBe(1);
});

it("lists a project added but not started in yet, from its settings", () => {
  const held = [
    {
      id: "a",
      machineId: "m1",
      updatedMs: 9000n,
      info: create(SessionSchema, {
        cwd: "/p",
        status: SessionStatus.EXITED,
        createdAt: timestampFromMs(100),
      }),
    },
  ];
  const settings = [
    { path: "/added", machineId: "m1", updatedMs: 5000n },
    // One with sessions is as its sessions say.
    { path: "/p", machineId: "m1", updatedMs: 7000n },
    // The machine chosen for it is no longer the account's.
    { path: "/gone", machineId: "m9", updatedMs: 8000n },
  ];
  const list = projects(
    held,
    [{ id: "m1", online: true, lastSeenMs: 0n }],
    settings,
  );
  expect(list.map((p) => p.key)).toEqual(["/added", "/p"]);
  expect(list[0]).toMatchObject({
    name: "added",
    ended: 0,
    hasLive: false,
    latestCreatedMs: 5000,
    updatedMs: 5000,
    machines: ["m1"],
  });
  expect(list[0].sessions).toHaveLength(0);
  expect(list[1]).toMatchObject({ latestCreatedMs: 100, machines: ["m1"] });
});

it("fetches what comes before a session held past the pages", async () => {
  const { r, server } = await setup();
  for (let i = 1; i <= MANY; i++)
    server.set(ended(i), { machineId: "m1", endedMs: 1000 + i });
  await r.refresh();
  // Fetched by its ID, it lies beyond the first page: sessions not here yet come before it.
  await r.ensureSession(ended(5));
  expect(r.pastPages(r.sessions.get(ended(5))!)).toBe(true);
  expect(r.pastPages(r.sessions.get(ended(MANY - 1))!)).toBe(false);
  await r.loadAllSessions();
  expect(r.pastPages(r.sessions.get(ended(5))!)).toBe(false);
});

it("does not bring back the sessions of a machine removed while a page was on its way", async () => {
  const { r, server, before, ids } = await setup();
  for (let i = 1; i <= SOME; i++)
    server.set(ended(i), { machineId: "m1", endedMs: 1000 + i });
  await r.refresh();
  const removed = () => {
    const x = r as unknown as {
      removedPushes: Map<string, number>;
      sessions: Map<string, unknown>;
    };
    x.removedPushes.set(ids.m1, (x.removedPushes.get(ids.m1) ?? 0) + 1);
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
  for (let i = 1; i <= SOME; i++)
    server.set(ended(i), { machineId: "m2", endedMs: 1000 + i });
  await setMachines(["m1", "m2"]);
  await r.refresh();
  // While the second page is in flight, m2 turns out to have been removed.
  let again: Promise<void> | undefined;
  before(async () => {
    await setMachines(["m1"]);
    for (let i = 1; i <= SOME; i++) server.delete(ended(i));
    again = r.refresh();
  });
  await r.loadMoreSessions();
  await again;
  expect([...r.sessions.keys()]).toEqual([ended(200)]);
  expect(r.moreSessions).toBe(false);
});

it("gives up on a session whose record does not open, rather than asking for it forever", async () => {
  const { r, server, calls } = await setup();
  server.set(ended(1), { machineId: "m1", endedMs: 1001 });
  // The key was sealed for another place: it does not open here.
  const api = (
    r as unknown as {
      api: {
        listRemoteSessions(q: object): Promise<{ sessions: RemoteSession[] }>;
      };
    }
  ).api;
  const list = api.listRemoteSessions;
  api.listRemoteSessions = async (q) => {
    const res = await list(q);
    return {
      ...res,
      sessions: res.sessions.map((s) => ({
        ...s,
        sessionId: s.sessionId,
        key: { ...s.key!, nonce: new Uint8Array(12) },
      })),
    };
  };
  await r.ensureSession(ended(1));
  expect(r.sessions.has(ended(1))).toBe(false);
  expect(r.missing.has(ended(1))).toBe(true);
  const n = calls.length;
  await r.ensureSession(ended(1));
  expect(calls.length).toBe(n);
});

it("does not take a session that was being opened when its machine turned out to be gone", async () => {
  const { r, server, setMachines, raw } = await setup();
  server.set(ended(200), { machineId: "m1", endedMs: 5000 });
  for (let i = 1; i <= SOME; i++)
    server.set(ended(i), { machineId: "m2", endedMs: 1000 + i });
  await setMachines(["m1", "m2"]);
  await r.refresh();
  // The next page arrives. While the key of its first session is being
  // imported, a refresh finds m2 removed and is done before the key is in.
  const real = crypto.subtle.importKey.bind(crypto.subtle);
  let again: Promise<void> | undefined;
  let switched = false;
  const spy = vi
    .spyOn(crypto.subtle, "importKey")
    .mockImplementation(async (...args: Parameters<typeof real>) => {
      // Only the client's own import of a session key, not the fake server's sealing.
      if (
        !switched &&
        args[0] === "raw" &&
        args[1] instanceof Uint8Array &&
        args[1].length === raw.length &&
        args[1].every((b, i) => b === raw[i]) &&
        (args[4] as string[]).includes("decrypt")
      ) {
        switched = true;
        await setMachines(["m1"]);
        for (let i = 1; i <= SOME; i++) server.delete(ended(i));
        again = r.refresh();
        await again;
      }
      return real(...args);
    });
  try {
    await r.loadMoreSessions();
  } finally {
    spy.mockRestore();
  }
  expect(again).toBeDefined();
  expect([...r.sessions.keys()]).toEqual([ended(200)]);
});

it("keeps the default session listing page at 50 rows", async () => {
  const { r, calls } = await setup(null);
  await r.refresh();
  expect(SESSION_PAGE).toBe(50);
  expect(calls[0]?.limit).toBe(50);
});

it("cold listing retains task-owned approvals through refresh", async () => {
  const { r, server, calls } = await setup();
  server.set("approval", { machineId: "m1", endedMs: 0, taskOwned: true, pendingApprovals: 1 });
  server.set("quiet", { machineId: "m1", endedMs: 0, taskOwned: true });
  await r.refresh();
  await r.refresh();
  expect(calls.every((c) => !c.sessionIds?.length)).toBe(true);
  const approval = r.sessions.get("approval")!;
  expect(approval.info?.taskOwned).toBe(true);
  expect(needsAttention(approval, r.machines.get(approval.machineId))).toBe(true);
  expect(todoList(r).map((s) => s.id)).toEqual(["approval"]);
  expect(projects(r.sessions.values(), r.machines.values()).flatMap((p) => p.sessions.map((s) => s.id))).toEqual(["approval"]);
});
