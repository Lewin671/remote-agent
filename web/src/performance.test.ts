import { afterEach, expect, it, vi } from "vitest";
import { patchSyncState, SyntheticRemote } from "./test/remote-fixture";
import { getCoreHost } from "./core/host";
const fixtureRemotes: SyntheticRemote[] = [];
import { create } from "@bufbuild/protobuf";

class Socket {
  static all: Socket[] = [];
  static OPEN = 1;
  readyState = 0;
  onopen?: () => void;
  onclose?: (e?: { code: number }) => void;
  onerror?: () => void;
  onmessage?: (m: { data: ArrayBuffer }) => void;
  send = vi.fn();
  constructor(readonly url: string) {
    Socket.all.push(this);
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  close() {
    this.readyState = 3;
    queueMicrotask(() => this.onclose?.());
  }
  /** The server closes the stream with a status, as when it refuses the hello. */
  closedBy(code: number) {
    this.readyState = 3;
    this.onclose?.({ code });
  }
}
afterEach(async () => {
  for (const r of fixtureRemotes.splice(0)) await r.dispose();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  Socket.all = [];
});
async function probe() {
  const r = await SyntheticRemote.create();
  fixtureRemotes.push(r);
  return r;
}

it("does not open a stale main socket when stopped during login", async () => {
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  const status = await r.getSecurityStatus();
  vi.spyOn((r as unknown as { ensureHost(): { securityClient: { syncNow: () => Promise<unknown> } } }).ensureHost().securityClient, "syncNow").mockResolvedValue(status);
  let finish!: () => void;
  vi.spyOn(r as any, "login").mockImplementation(
    () =>
      new Promise<void>((resolve) => {
        finish = () => {
          (r as any).token = "tok";
          (r as any).tokenExpires = Date.now() + 7 * 86_400_000;
          resolve();
        };
      }),
  );
  const settle = async () => {
    for (let i = 0; i < 50; i++) await Promise.resolve();
  };
  r.start();
  await vi.waitFor(() => expect(finish).toBeTypeOf("function"));
  r.stop();
  finish();
  await settle();
  expect(Socket.all).toHaveLength(0);
  finish = undefined as never;
  r.start();
  await vi.waitFor(() => expect(finish).toBeTypeOf("function"));
  await vi.waitFor(() => expect((r as any).conn).toBe("connecting"));
  await settle();
  finish();
  await vi.waitFor(() => expect(Socket.all).toHaveLength(1));
  r.stop();
}, 15000);
it("stops pending terminal opens and bounds queued decryption", async () => {
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  const { getCoreHost } = await import("./core/host");
  vi.spyOn(getCoreHost(r), "runMachineCommand").mockResolvedValue(undefined);
  const closed = vi.fn();
  const frames = vi.fn();
  const opening = r.openTerminal("s", frames, closed);
  const socket = Socket.all[0];
  socket.open();
  const terminal = await opening;
  // All calls occur before promise jobs execute: these frames must be bounded and discarded.
  for (let i = 0; i < 513; i++)
    socket.onmessage?.({ data: new ArrayBuffer(0) });
  await Promise.resolve();
  await Promise.resolve();
  expect(closed).toHaveBeenCalledTimes(1);
  expect(frames).not.toHaveBeenCalled();
  expect(socket.readyState).toBe(3);
  terminal.close();
  r.stop();
});
it("closes a terminal socket even while its handshake is pending", async () => {
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  const opening = r.openTerminal("s", vi.fn(), vi.fn()).catch((e) => e);
  r.stop();
  expect(await opening).toBeInstanceOf(Error);
  expect(Socket.all[0].readyState).toBe(3);
});

it("serializes the main stream and drops a bounded backlog", async () => {
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  vi.spyOn(r as any, "login").mockImplementation(async () => {
    (r as any).token = "tok";
    (r as any).tokenExpires = Date.now() + 7 * 86_400_000;
  });
  let release!: () => void;
  const handler = vi.spyOn(r as any, "mapStreamFrame").mockImplementation(
    () =>
      new Promise<undefined>((resolve) => {
        release = () => resolve(undefined);
      }),
  );
  const { toBinary } = await import("@bufbuild/protobuf");
  const { ServerFrameSchema } = await import("./gen/remoteagent/v1/relay_pb");
  const frame = toBinary(
    ServerFrameSchema,
    create(ServerFrameSchema, { kind: { case: "heartbeat", value: {} } }),
  ).buffer as ArrayBuffer;
  r.start();
  await vi.waitFor(() => expect(Socket.all).toHaveLength(1));
  const ws = Socket.all[0];
  ws.open();
  ws.onmessage?.({ data: frame });
  await vi.waitFor(() => expect(handler).toHaveBeenCalledTimes(1));
  for (let i = 0; i < 1024; i++) ws.onmessage?.({ data: frame });
  expect(ws.readyState).toBe(3);
  release();
  await Promise.resolve();
  await Promise.resolve();
  expect(handler).toHaveBeenCalledTimes(1);
  r.stop();
});

it("counts terminal output until the renderer has consumed it", async () => {
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  const { getCoreHost } = await import("./core/host");
  const command = vi.spyOn(getCoreHost(r), "runMachineCommand").mockResolvedValue(undefined);
  let consumed!: () => void;
  const renderer = vi.fn(
    () =>
      new Promise<void>((resolve) => {
        consumed = resolve;
      }),
  );
  const closed = vi.fn();
  const opening = r.openTerminal("s", renderer, closed);
  const ws = Socket.all[0];
  ws.open();
  const conn = await opening;
  const { toBinary } = await import("@bufbuild/protobuf");
  const { EnvelopeSchema, PayloadKind } =
    await import("./gen/remoteagent/v1/envelope_pb");
  const { TerminalFrameSchema } =
    await import("./gen/remoteagent/v1/remote_pb");
  const streamId = (command.mock.calls as any)[0][2].value.streamId;
  const env = await r.seal(
    {
      kind: PayloadKind.TERMINAL_OUTPUT,
      machineId: r.machine,
      sessionId: "s",
      requestId: streamId,
      seq: 1n,
    },
    TerminalFrameSchema,
    create(TerminalFrameSchema, {
      kind: { case: "output", value: new Uint8Array([65]) },
    }),
  );
  ws.onmessage?.({ data: toBinary(EnvelopeSchema, env).buffer as ArrayBuffer });
  await vi.waitFor(() => expect(renderer).toHaveBeenCalledTimes(1));
  for (let i = 0; i < 512; i++) ws.onmessage?.({ data: new ArrayBuffer(0) });
  expect(ws.readyState).toBe(3);
  expect(closed).toHaveBeenCalledTimes(1);
  consumed();
  conn.close();
  r.stop();
});

async function sealedEvents(r: SyntheticRemote, n: number) {
  const { PayloadKind } = await import("./gen/remoteagent/v1/envelope_pb");
  const { EventSchema } = await import("./gen/remoteagent/v1/event_pb");
  const out = [];
  for (let seq = 1n; seq <= BigInt(n); seq++)
    out.push({
      seq,
      envelope: await (r as SyntheticRemote).seal(
        { kind: PayloadKind.EVENT, machineId: r.machine, sessionId: "s", seq },
        EventSchema,
        create(EventSchema, {
          seq,
          sessionId: "s",
          kind: { case: "agentMessage", value: { text: "m" + seq } },
        }),
      ),
    });
  return out;
}

it("opens a session from the cached history and downloads only what is new", async () => {
  const r = await probe();
  const all = await sealedEvents(r, 3);
  const row = await r.sessionRow("s");
  const cache = {
    read: vi.fn(async (_sid: string, after = 0n) => all.slice(0, 2).filter(e => e.seq > after)),
    append: vi.fn(async () => undefined),
    drop: vi.fn(async () => undefined),
    keep: vi.fn(async () => undefined),
    get: vi.fn(async () => undefined),
    set: vi.fn(async () => undefined),
  };
  (r as any).cache = cache;
  (r as any).token = "tok";
  (r as any).tokenExpires = Date.now() + 7 * 86_400_000;
  const getEvents = vi.fn(async ({ afterSeq }: { afterSeq: bigint }) => ({
    events: all.filter((e) => e.seq > afterSeq),
    more: false,
  }));
  (r as any).api = {
    listMachines: async () => ({ machines: [] }),
    listRemoteSessions: async () => ({ sessions: [{ ...row, lastSeq: 3n }] }),
    listDevices: async () => ({ devices: [] }),
    getAccountPrefs: async () => ({}),
    getEvents,
  };
  (r as any).openStreamLeg = (legId: bigint, _t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) =>
    cb.onReady(legId, BigInt(Date.now()));
  r.start();
  await vi.waitFor(() => expect(r.sessions.get("s")?.lastSeq).toBe(3n));
  const host = getCoreHost(r);
  host.watchSession("s");
  host.sessionOpen("s");
  try {
    await vi.waitFor(() => expect(host.viewSession("s").timeline?.loaded).toBe(true));
    expect(getEvents).toHaveBeenCalledTimes(1);
    expect(getEvents.mock.calls[0][0].afterSeq).toBe(2n);
    expect(host.viewSession("s").timeline?.items.map((e) => e.seq)).toEqual([1n, 2n, 3n]);
    // Only the downloaded event is written back.
    expect(cache.append).toHaveBeenCalledTimes(1);
    expect(
      (cache.append.mock.calls as any)[0][1].map((e: { seq: bigint }) => e.seq),
    ).toEqual([3n]);
  } finally {
    host.unwatchSession("s");
    r.stop();
  }
});

it("drops a cached history that is not this session's", async () => {
  const r = await probe();
  const all = await sealedEvents(r, 3);
  const row = await r.sessionRow("s");
  let serverLastSeq = 2n;
  const getEvents = vi.fn(async ({ afterSeq }: { afterSeq: bigint }) => ({
    events: all.filter((e) => e.seq > afterSeq && e.seq <= serverLastSeq),
    more: false,
  }));
  (r as any).token = "tok";
  (r as any).tokenExpires = Date.now() + 7 * 86_400_000;
  (r as any).api = {
    listMachines: async () => ({ machines: [] }),
    listRemoteSessions: async () => ({ sessions: [{ ...row, lastSeq: serverLastSeq }] }),
    listDevices: async () => ({ devices: [] }),
    getAccountPrefs: async () => ({}),
    getEvents,
  };
  (r as any).openStreamLeg = (legId: bigint, _t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) =>
    cb.onReady(legId, BigInt(Date.now()));
  // Longer than what the server has.
  const long = {
    read: vi.fn(async () => all),
    append: vi.fn(async () => undefined),
    drop: vi.fn(async () => undefined),
    keep: vi.fn(async () => undefined),
    get: vi.fn(async () => undefined),
    set: vi.fn(async () => undefined),
  };
  (r as any).cache = long;
  r.start();
  await vi.waitFor(() => expect(r.sessions.get("s")?.lastSeq).toBe(2n));
  const host1 = getCoreHost(r);
  host1.watchSession("s");
  host1.sessionOpen("s");
  await vi.waitFor(() => expect(host1.viewSession("s").timeline?.loaded).toBe(true));
  await vi.waitFor(() => expect(long.drop).toHaveBeenCalledWith("s"));
  expect(getEvents.mock.calls[0][0].afterSeq).toBe(0n);
  host1.unwatchSession("s");
  r.stop();

  // Does not open where it is: sealed for another seq.
  serverLastSeq = 3n;
  const r2 = await probe();
  const all2 = await sealedEvents(r2, 3);
  const getEvents2 = vi.fn(async ({ afterSeq }: { afterSeq: bigint }) => ({
    events: all2.filter((e) => e.seq > afterSeq),
    more: false,
  }));
  const moved = {
    ...long,
    read: vi.fn(async () => [{ seq: 1n, envelope: all2[1].envelope }]),
    drop: vi.fn(async () => undefined),
  };
  (r2 as any).token = "tok";
  (r2 as any).tokenExpires = Date.now() + 7 * 86_400_000;
  (r2 as any).api = {
    listMachines: async () => ({ machines: [] }),
    listRemoteSessions: async () => ({ sessions: [{ ...(await r2.sessionRow("s")), lastSeq: 3n }] }),
    listDevices: async () => ({ devices: [] }),
    getAccountPrefs: async () => ({}),
    getEvents: getEvents2,
  };
  (r2 as any).openStreamLeg = (legId: bigint, _t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) =>
    cb.onReady(legId, BigInt(Date.now()));
  (r2 as any).cache = moved;
  r2.start();
  await vi.waitFor(() => expect(r2.sessions.get("s")?.lastSeq).toBe(3n));
  const host2 = getCoreHost(r2);
  host2.watchSession("s");
  host2.sessionOpen("s");
  await vi.waitFor(() => expect(host2.viewSession("s").timeline?.loaded).toBe(true));
  await vi.waitFor(() => expect(moved.drop).toHaveBeenCalledWith("s"));
  expect(host2.viewSession("s").timeline?.retired).toBe(false);
  expect(host2.viewSession("s").timeline?.items.map((e) => e.seq)).toEqual([1n, 2n, 3n]);
  host2.unwatchSession("s");
  r2.stop();
});

it("starts from what the browser kept: the token, and the last listing", async () => {
  // Token reuse is independent of recovery jitter, which has its own tests.
  vi.spyOn(Math, "random").mockReturnValue(0.1);
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  patchSyncState(r, { sessions: new Map() });
  const { ListMachinesResponseSchema, ListRemoteSessionsResponseSchema } =
    await import("./gen/remoteagent/v1/relay_pb");
  const { toBinary } = await import("@bufbuild/protobuf");
  const keptRow = await r.sessionRow("kept", "from last time");
  const sessions = toBinary(
    ListRemoteSessionsResponseSchema,
    create(ListRemoteSessionsResponseSchema, {
      sessions: [{ ...keptRow, lastSeq: 4n }],
    }),
  );
  const mine = {
    deviceId: r.account.deviceId,
    server: "https://ra.example.com",
  };
  vi.spyOn(await import("./core/security-store-v4"), "loadLoginToken").mockImplementation(
    async (_server, member) => member === r.account.deviceId
      ? { token: "kept-token", expiresMs: Date.now() + 10 * 86_400_000 }
      : undefined,
  );
  const clearToken = vi.spyOn(await import("./core/security-store-v4"), "clearLoginToken").mockResolvedValue(undefined);
  const kept: Record<string, unknown> = {
    token: {
      ...mine,
      token: "kept-token",
      expiresMs: Date.now() + 10 * 86_400_000,
    },
    machines: {
      ...mine,
      bytes: toBinary(
        ListMachinesResponseSchema,
        create(ListMachinesResponseSchema, {
          machines: [
            {
              id: r.machine,
              online: true,
              meta: undefined,
            },
          ],
        }),
      ),
    },
    sessions: { ...mine, bytes: sessions },
  };
  (r as any).cache = {
    read: vi.fn(async () => []),
    append: vi.fn(),
    drop: vi.fn(),
    keep: vi.fn(async () => undefined),
    get: vi.fn(async (k: string) => kept[k]),
    set: vi.fn(async () => undefined),
  };
  const login = vi.spyOn(r as any, "login").mockResolvedValue(undefined);
  let notified = 0;
  r.subscribe(() => notified++);
  r.start();
  await vi.waitFor(() => {
    expect(Socket.all).toHaveLength(1);
    expect(r.sessions.get("kept")?.info?.title).toBe("from last time");
  });
  // Shown before the server has said anything, without logging in.
  expect(login).not.toHaveBeenCalled();
  expect(r.machines.get(r.machine)?.online).toBe(false);
  expect(r.listed).toBe(false);
  expect(r.authHeader()).toBe("Bearer kept-token");
  // A handshake the network dropped keeps the token.
  Socket.all[0].closedBy(1006);
  // Token reuse is independent of the backoff duration (covered separately).
  // The foreground request now honors the same bounded jitter as other hints.
  r.reconnectNow();
  await vi.waitFor(() => expect(Socket.all).toHaveLength(2));
  expect(r.authHeader()).toBe("Bearer kept-token");
  expect(login).not.toHaveBeenCalled();
  // The server turning the token away is followed by a login.
  Socket.all[1].closedBy(1008);
  expect(r.authHeader()).toBe("Bearer ");
  await (r as any).tokenWrites;
  expect(clearToken).toHaveBeenCalledOnce();
  r.stop();
  // Another account's leftovers are not used.
  const other = await probe();
  (other as any).cache = {
    ...(r as any).cache,
    get: vi.fn(async (k: string) => ({
      ...(kept[k] as object),
      deviceId: "someone else",
    })),
  };
  const otherLogin = vi
    .spyOn(other as any, "login")
    .mockResolvedValue(undefined);
  patchSyncState(other, { sessions: new Map() });
  other.start();
  await vi.waitFor(() => expect(otherLogin).toHaveBeenCalled());
  expect(other.sessions.size).toBe(0);
  other.stop();
});
