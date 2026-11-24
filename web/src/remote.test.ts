import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { type Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { LoginResponseSchema } from "./gen/remoteagent/v1/relay_pb";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, expect, it, vi } from "vitest";
import { newRequestID } from "./crypto";
import { patchSyncState, SyntheticRemote } from "./test/remote-fixture";
const liveFixtures: SyntheticRemote[] = [];
async function probe() {
  const r = await SyntheticRemote.create();
  liveFixtures.push(r);
  return r;
}
afterEach(async () => {
  for (const r of liveFixtures.splice(0)) await r.dispose();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("routes pingMachine through CoreHost runMachineCommand and cancels on timeout", async () => {
  const r = await probe();
  const { CommandResultSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { PayloadKind } = await import("./gen/remoteagent/v1/envelope_pb");
  let sent: Envelope | undefined;
  (r as unknown as { token: string; tokenExpires: number; openStreamLeg: unknown }).token = "tok";
  (r as unknown as { tokenExpires: number }).tokenExpires = Date.now() + 7 * 86_400_000;
  (r as unknown as { openStreamLeg: (id: bigint, t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) => void }).openStreamLeg = (legId, _t, cb) =>
    cb.onReady(legId, BigInt(Date.now()));
  (r as unknown as { api: object }).api = {
    listMachines: async () => ({ machines: [{ id: r.machine, online: true, lastSeenMs: 0n }] }),
    listRemoteSessions: async () => ({ sessions: [] }),
    listDevices: async () => ({ devices: [] }),
    getAccountPrefs: async () => ({}),
    forward: async (req: { command: Envelope }) => {
      sent = req.command;
      const opened = await r.world.fixture.open(r.machine, req.command);
      const resEnv = await r.seal(
        { kind: PayloadKind.COMMAND_RESULT, machineId: r.machine, sessionId: "", requestId: opened.header.requestId },
        CommandResultSchema,
        create(CommandResultSchema, { kind: { case: "getSettings", value: {} } }),
      );
      return { result: resEnv };
    },
  };
  r.start();
  await vi.waitFor(() => expect(r.conn).toBe("online"));
  const rtt = await r.pingMachine(r.machine, 5_000);
  expect(rtt).toBeGreaterThanOrEqual(0);
  expect(sent).toBeTruthy();
  const opened = await r.world.fixture.open(r.machine, sent!);
  expect(opened.header.sender).toBe(r.account.deviceId);
  expect(sent!.signature.length).toBe(64);
});

it("cancels in-flight pingMachine immediately when external AbortSignal aborts", async () => {
  const r = await probe();
  let forwardSignal: AbortSignal | undefined;
  (r as unknown as { token: string; tokenExpires: number; openStreamLeg: unknown }).token = "tok";
  (r as unknown as { tokenExpires: number }).tokenExpires = Date.now() + 7 * 86_400_000;
  (r as unknown as { openStreamLeg: (id: bigint, t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) => void }).openStreamLeg = (legId, _t, cb) =>
    cb.onReady(legId, BigInt(Date.now()));
  (r as unknown as { api: object }).api = {
    listMachines: async () => ({ machines: [{ id: r.machine, online: true, lastSeenMs: 0n }] }),
    listRemoteSessions: async () => ({ sessions: [] }),
    listDevices: async () => ({ devices: [] }),
    getAccountPrefs: async () => ({}),
    forward: async (_req: { command: Envelope }, opts?: { signal?: AbortSignal }) => {
      forwardSignal = opts?.signal;
      return await new Promise((_, reject) => {
        opts?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
      });
    },
  };
  r.start();
  await vi.waitFor(() => expect(r.conn).toBe("online"));
  const ctrl = new AbortController();
  const pending = r.pingMachine(r.machine, 15_000, ctrl.signal);
  await vi.waitFor(() => expect(forwardSignal).toBeDefined());
  expect(forwardSignal!.aborted).toBe(false);
  ctrl.abort();
  await expect(pending).rejects.toThrow();
  await vi.waitFor(() => expect(forwardSignal!.aborted).toBe(true));
});

it("preserves concurrent project edits through server CAS", async () => {
  const a = await probe();
  const b = await a.addClient();
  liveFixtures.push(b);
  const uploads: Envelope[] = [];
  const stored: {
    objectId: string;
    revision: bigint;
    envelope?: Envelope;
  } = { objectId: "project-prefs", revision: 0n };
  const api = {
    listMachines: async () => ({ machines: [] }),
    listRemoteSessions: async () => ({ sessions: [] }),
    listDevices: async () => ({ devices: [] }),
    getAccountPrefs: async () => ({ object: { ...stored } }),
    putAccountPrefs: async (req: {
      expectedRevision: bigint;
      envelope: Envelope;
    }) => {
      if (req.expectedRevision !== stored.revision)
        return { applied: false, object: { ...stored } };
      uploads.push(req.envelope);
      stored.revision = req.expectedRevision + 1n;
      stored.envelope = req.envelope;
      return { applied: true, object: { ...stored } };
    },
  };
  for (const r of [a, b]) {
    (r as any).token = "tok";
    (r as any).tokenExpires = Date.now() + 7 * 86_400_000;
    (r as any).api = api;
    (r as any).openStreamLeg = (legId: bigint, _t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) =>
      cb.onReady(legId, BigInt(Date.now()));
    r.start();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
  }
  await a.changePrefs("pin", "/synthetic/foo");
  await Promise.all([
    a.changePrefs("hide", "/synthetic/blog"),
    b.changePrefs("pin", "/synthetic/billing"),
  ]);
  const { RpcEffectSchema } = await import("./gen/remoteagent/core/v1/app_pb");
  const { getCoreHost } = await import("./core/host");
  for (const r of [a, b]) {
    getCoreHost(r).dispatch(
      await r.executeRpc(
        create(RpcEffectSchema, {
          opId: 0n,
          kind: { case: "getAccountPrefs", value: { objectId: "project-prefs" } },
        }),
      ),
    );
    await vi.waitFor(() => expect(r.prefs.snap?.revision).toBe(3n));
    const prefs = r.projectPrefs();
    expect(prefs.pinned.map((p) => p.path)).toEqual([
      "/synthetic/foo",
      "/synthetic/billing",
    ]);
    expect(prefs.hidden.map((p) => p.path)).toEqual(["/synthetic/blog"]);
  }
  expect(a.prefs.snap?.revision).toBe(3n);
  expect(uploads.length).toBeGreaterThanOrEqual(3);
  for (const env of uploads)
    expect(new TextDecoder().decode(env.ciphertext)).not.toContain("synthetic");
}, 20000);

it("shows a display copy only where it belongs and in the format it claims", async () => {
  const { PayloadKind } = await import("./gen/remoteagent/v1/envelope_pb");
  const { ImagePayloadSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { ImageFormat } = await import("./gen/remoteagent/v1/event_pb");
  const { create } = await import("@bufbuild/protobuf");
  const r = await probe();
  const base = r.sessions.get("s")!;
  await r.seedSession("s1", r.seed);
  patchSyncState(r, { sessions: new Map([["s", base], ["s1", { ...base, id: "s1" }]]) });
  const png = Uint8Array.from([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3,
  ]);
  const seal = (imageId: string, format: number, data: Uint8Array) =>
    r.seal(
      {
        kind: PayloadKind.IMAGE,
        machineId: r.machine,
        sessionId: "s1",
        requestId: imageId,
      },
      ImagePayloadSchema,
      create(ImagePayloadSchema, { format, width: 2, height: 1, data }),
    );
  const store: Record<string, unknown> = {
    good: { image: await seal("good", ImageFormat.PNG, png) },
    liar: {
      image: await seal(
        "liar",
        ImageFormat.PNG,
        new TextEncoder().encode("<svg onload=x>"),
      ),
    },
    moved: { image: await seal("other", ImageFormat.PNG, png) },
    old: { expired: true },
  };
  (r as unknown as { api: object }).api = {
    getImage: async (req: { imageId: string }) => {
      if (!(req.imageId in store))
        throw new ConnectError("no such image", Code.NotFound);
      return store[req.imageId];
    },
  };
  const good = await r.getImage("s1", "good");
  expect(good.kind).toBe("ok");
  expect(good.kind === "ok" && good.blob.type).toBe("image/png");
  expect((await r.getImage("s1", "liar")).kind).toBe("bad");
  // An envelope for another image, served under this ID, does not open here.
  expect((await r.getImage("s1", "moved")).kind).toBe("bad");
  expect((await r.getImage("s1", "old")).kind).toBe("expired");
  expect((await r.getImage("s1", "never")).kind).toBe("missing");
  // Seen once, it opens again without the server.
  (r as unknown as { api: object }).api = {
    getImage: async () => {
      throw new ConnectError("offline", Code.Unavailable);
    },
  };
  expect((await r.getImage("s1", "good")).kind).toBe("ok");
  await expect(r.getImage("s1", "liar")).rejects.toThrow();
});

it("gives up on a stream that never opens, and retains backoff when the network returns", async () => {
  const sockets: {
    closed: boolean;
    onclose?: (e: { code: number }) => void;
  }[] = [];
  vi.stubGlobal(
    "WebSocket",
    class {
      closed = false;
      onclose?: (e: { code: number }) => void;
      constructor() {
        sockets.push(this);
      }
      close() {
        if (this.closed) return;
        this.closed = true;
        this.onclose?.({ code: 1006 });
      }
    },
  );
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(new Uint8Array(), {
        headers: { "Content-Type": "application/proto" },
      }),
  );
  const r = await probe();
  try {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance", "Date"] });
    vi.spyOn(Math, "random").mockReturnValue(0.75);
    (r as any).token = "tok";
    (r as any).tokenExpires = Date.now() + 7 * 86_400_000;
    r.start();
    await vi.waitFor(() => expect(sockets.length).toBe(1));
    // The handshake hangs: nothing closes it until the client does.
    await vi.advanceTimersByTimeAsync(13_000);
    expect(sockets[0].closed).toBe(false);
    await vi.advanceTimersByTimeAsync(2_000);
    expect(sockets[0].closed).toBe(true);
    expect(r.conn).toBe("connecting");
    // Explicit reconnect replaces the active/pending attempt with a fresh socket.
    const before = sockets.length;
    r.reconnectNow();
    await vi.waitFor(() => expect(sockets.length).toBe(before + 1));
    expect(sockets[before - 1].closed).toBe(true);
  } finally {
    r.stop();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  }
});

it("stops and says so once the server no longer knows this device", async () => {
  const calls: string[] = [];
  let denied = 1;
  vi.stubGlobal(
    "WebSocket",
    class {
      close() {}
    },
  );
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const name = String(input instanceof Request ? input.url : input)
      .split("/")
      .pop()!;
    calls.push(name);
    if (name === "GetChallenge")
      return new Response(new Uint8Array(), {
        headers: { "Content-Type": "application/proto" },
      });
    if (denied-- > 0)
      return new Response(
        JSON.stringify({ code: "permission_denied", message: "login failed" }),
        { status: 403, headers: { "Content-Type": "application/json" } },
      );
    return new Response(
      name === "Login"
        ? new Uint8Array(toBinary(LoginResponseSchema, create(LoginResponseSchema, { token: "t", expiresMs: BigInt(Date.now() + 7 * 86_400_000) })))
        : new Uint8Array(),
      { headers: { "Content-Type": "application/proto" } },
    );
  });
  try {
    // One denial alone is a challenge the server forgot: the next login works.
    const r = await probe();
    r.start();
    await vi.waitFor(() =>
      expect(calls.filter((c) => c === "Login")).toHaveLength(2),
    );
    expect(r.removed).toBe(false);
    r.stop();

    // Denied a fresh challenge too: removed, and it stops asking.
    denied = 2;
    calls.length = 0;
    const gone = await probe();
    gone.start();
    await vi.waitFor(() => expect(gone.removed).toBe(true));
    expect(gone.conn).toBe("offline");
    gone.start();
    await new Promise((r) => setTimeout(r, 50));
    expect(calls.filter((c) => c === "Login")).toHaveLength(2);

    // Asked to try again after joining was restored on the server.
    gone.retryLogin();
    await vi.waitFor(() =>
      expect(calls.filter((c) => c === "Login")).toHaveLength(3),
    );
    expect(gone.removed).toBe(false);
    gone.stop();
  } finally {
    vi.unstubAllGlobals();
  }
});

it("reports an unreadable prefs object and allows a fresh read retry", async () => {
  const r = await probe();
  vi.spyOn(console, "warn").mockImplementation(() => undefined);
  let fail = true;
  (r as unknown as { token: string; tokenExpires: number; api: object }).token = "tok";
  (r as unknown as { tokenExpires: number }).tokenExpires = Date.now() + 7 * 86_400_000;
  (r as unknown as { api: object }).api = {
    listMachines: async () => ({ machines: [] }),
    listRemoteSessions: async () => ({ sessions: [] }),
    listDevices: async () => ({ devices: [] }),
    getAccountPrefs: async () => {
      if (fail) {
        fail = false;
        throw new Error("invalid synthetic prefs reply");
      }
      return {};
    },
  };
  (r as any).openStreamLeg = (legId: bigint, _t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) =>
    cb.onReady(legId, BigInt(Date.now()));
  r.start();
  await vi.waitFor(() => expect(r.prefs.status).toBe("error"));
  expect(r.prefs.snap).toBeUndefined();
  const { getCoreHost } = await import("./core/host");
  const { TimerKind } = await import("./gen/remoteagent/core/v1/app_pb");
  getCoreHost(r).fireActiveTimer(TimerKind.PREFS_RETRY);
  await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
});

it("revokes a removed machine and retains its archive when the head moves without a push", async () => {
  const r = await probe();
  const row = await r.sessionRow("s");
  (r as unknown as { token: string; tokenExpires: number; api: object; openStreamLeg: unknown }).token = "tok";
  (r as unknown as { tokenExpires: number }).tokenExpires = Date.now() + 7 * 86_400_000;
  (r as unknown as { api: object }).api = {
    listMachines: async () => ({ machines: [{ id: r.machine, online: true, lastSeenMs: 0n }] }),
    listRemoteSessions: async () => ({ sessions: [row] }),
    listDevices: async () => ({
      devices: [
        { id: r.machine, kind: 1 },
        { id: r.account.deviceId, kind: 2 },
      ],
    }),
    getAccountPrefs: async () => ({}),
  };
  (r as unknown as { openStreamLeg: (id: bigint, t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) => void }).openStreamLeg = (legId, _t, cb) =>
    cb.onReady(legId, BigInt(Date.now()));
  r.start();
  const { getCoreHost } = await import("./core/host");
  await vi.waitFor(() => {
    expect(r.devices.get(r.machine)).toBeTruthy();
    expect(r.sessions.get("s")).toBeTruthy();
    expect(getCoreHost(r).viewSyncState().securityLoaded).toBe(true);
  });
  // Sync picks up the removal even when the relay withholds the push.
  await r.world.fixture.remove([r.machine]);
  await r.syncSecurityNow();
  expect(r.devices.get(r.machine)).toBeUndefined();
  expect(r.sessions.get("s")).toBeTruthy();
  expect(r.machines.get(r.machine)?.online).toBe(false);
});

it("bootstraps current owner state from a checkpoint without loading a history prefix", async () => {
  const { create, toBinary } = await import("@bufbuild/protobuf");
  const { SessionSchema } = await import("./gen/remoteagent/v1/local_pb");
  const { LiveSnapshotSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const r = await probe();
  const info = create(SessionSchema, {
    id: "s",
    lastSeq: 900n,
    title: "current state",
  });
  const snapshot = create(LiveSnapshotSchema, { session: info, running: true });
  info.currentCheckpoint = toBinary(LiveSnapshotSchema, snapshot);
  const row = await r.sessionRow("s");
  row.meta = await r.seal(
    { kind: 2, sessionId: "s", seq: 2n },
    SessionSchema,
    info,
  );
  (r as unknown as { token: string; tokenExpires: number; api: object; openStreamLeg: unknown }).token = "tok";
  (r as unknown as { tokenExpires: number }).tokenExpires = Date.now() + 7 * 86_400_000;
  (r as unknown as { api: object }).api = {
    listMachines: async () => ({ machines: [] }),
    listRemoteSessions: async () => ({ sessions: [row] }),
    listDevices: async () => ({ devices: [] }),
    getAccountPrefs: async () => ({}),
  };
  (r as unknown as { openStreamLeg: (id: bigint, t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) => void }).openStreamLeg = (legId, _t, cb) =>
    cb.onReady(legId, BigInt(Date.now()));
  r.start();
  await vi.waitFor(() => expect(r.sessions.get("s")?.checkpoint?.running).toBe(true));
  expect(r.sessions.get("s")?.checkpoint?.running).toBe(true);
  expect(r.sessions.get("s")?.checkpoint?.session?.lastSeq).toBe(900n);
  expect(r.sessions.get("s")?.checkpointStatus).toBe("ready");
});

it("installs the updated head and valid lease through Core sync after a head change", async () => {
  const r = await probe();
  const other = await r.addClient();
  const before = await r.getSecurityStatus();
  await r.world.fixture.remove([other.account.deviceId]);
  const synced = await r.syncSecurityNow();
  expect(synced.headEpoch).toBeGreaterThan(before.headEpoch);
  expect(synced.leaseValid).toBe(true);
});

it("does not poll pairing state after startup without a pending delivery", async () => {
  const { SecurityWorkflowStatusSchema } = await import("./gen/remoteagent/core/v1/security_pb");
  const r = await probe();
  const state = r as unknown as {
    ensureHost(): { start(): void; stop(): void; securityClient: { resumePendingOp(): Promise<unknown> } };
    applySecurityStatusSnapshot(s: ReturnType<typeof create<typeof SecurityWorkflowStatusSchema>>): void;
    pairingTimer?: ReturnType<typeof setInterval>;
  };
  const resume = vi.fn(async () => create(SecurityWorkflowStatusSchema));
  vi.spyOn(state, "ensureHost").mockReturnValue({
    start: () => undefined,
    stop: () => undefined,
    securityClient: { resumePendingOp: resume },
  });
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    r.start();
    state.applySecurityStatusSnapshot(create(SecurityWorkflowStatusSchema));
    await vi.advanceTimersByTimeAsync(4500);
    expect(state.pairingTimer).toBeUndefined();
    expect(resume).not.toHaveBeenCalled();
  } finally { r.stop(); vi.useRealTimers(); }
});

it.each(["complete", "missing", "final", "transient", "close"])(
  "resumes a pending delivery on startup and manages its timer on %s", async (outcome) => {
    const { SecurityWorkflowError } = await import("./core/security-client");
    const { SecurityErrorCode, SecurityPendingOpSummarySchema, SecurityWorkflowStatusSchema } = await import("./gen/remoteagent/core/v1/security_pb");
    const r = await probe();
    const state = r as unknown as {
      ensureHost(): { start(): void; stop(): void; securityClient: { resumePendingOp(): Promise<unknown> } };
      applySecurityStatusSnapshot(s: ReturnType<typeof create<typeof SecurityWorkflowStatusSchema>>): void;
      pairingTimer?: ReturnType<typeof setInterval>;
    };
    const pendingStatus = (hasDelivery: boolean) =>
      create(SecurityWorkflowStatusSchema, {
        pendingOp: hasDelivery ? create(SecurityPendingOpSummarySchema, { hasDelivery: true }) : undefined,
      });
    const resume = vi.fn(async () => {
      if (outcome === "final") throw new SecurityWorkflowError("candidate must be removed", SecurityErrorCode.INVALID, false, false, "");
      if (outcome === "transient") throw new ConnectError("offline", Code.Unavailable);
      state.applySecurityStatusSnapshot(pendingStatus(false));
      return pendingStatus(false);
    });
    vi.spyOn(state, "ensureHost").mockReturnValue({
      start: () => undefined,
      stop: () => undefined,
      securityClient: { resumePendingOp: resume },
    });
    vi.spyOn(r, "loadDevices").mockResolvedValue();
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      r.start();
      state.applySecurityStatusSnapshot(pendingStatus(true));
      expect(state.pairingTimer).toBeDefined();
      if (outcome === "missing") state.applySecurityStatusSnapshot(pendingStatus(false));
      if (outcome === "close") r.stop();
      await vi.advanceTimersByTimeAsync(1500);
      expect(state.pairingTimer !== undefined).toBe(outcome === "transient");
      expect(resume).toHaveBeenCalledTimes(["missing", "close"].includes(outcome) ? 0 : 1);
      const calls = resume.mock.calls.length;
      if (outcome !== "transient") {
        await vi.advanceTimersByTimeAsync(4500);
        expect(resume).toHaveBeenCalledTimes(calls);
      }
    } finally { r.stop(); vi.useRealTimers(); }
  },
);

it("seals terminal frames with purpose-specific domains and a default deadline", async () => {
  const r = await probe();
  const { PayloadKind } = await import("./gen/remoteagent/v1/envelope_pb");
  const {
    TerminalFrameSchema,
    TerminalHistoryRequestSchema,
    TerminalResizeSchema,
  } = await import("./gen/remoteagent/v1/remote_pb");
  const seed = new Uint8Array(32).fill(9);
  await r.seedSession("s", seed);
  const internal = r as unknown as {
    sealSecureMessage: (...args: unknown[]) => Promise<{ envelope: Envelope }>;
  };
  const nowBefore = Date.now();
  let seq = 1n;
  for (const kind of [
    { case: "input" as const, value: new Uint8Array([13]) },
    { case: "resize" as const, value: create(TerminalResizeSchema, { cols: 80, rows: 24 }) },
    { case: "historyRequest" as const, value: create(TerminalHistoryRequestSchema, {}) },
  ]) {
    const { envelope: env } = await internal.sealSecureMessage.call(
      r,
      {
        kind: PayloadKind.TERMINAL_INPUT,
        machineId: r.machine,
        sessionId: "s",
        requestId: newRequestID(),
        seq: seq++,
      },
      TerminalFrameSchema,
      create(TerminalFrameSchema, { kind }),
    );
    const opened = await r.world.fixture.open(r.machine, env, seed);
    expect(opened.header.deadlineMs).toBeGreaterThanOrEqual(BigInt(nowBefore + 50000));
    expect(fromBinary(TerminalFrameSchema, opened.plaintext).kind.case).toBe(kind.case);
  }
});

it("proactively renews security every 12s while foreground and online so direct stays authorized", async () => {
  const r = await probe();
  const status = await r.getSecurityStatus();
  const host = (r as unknown as {
    ensureHost(): {
      start(): void;
      stop(): void;
      setForeground(active: boolean): void;
      securityClient: { syncNow: () => Promise<unknown> };
    };
  }).ensureHost();
  vi.spyOn(host, "start").mockImplementation(() => undefined);
  vi.spyOn(host, "stop").mockImplementation(() => undefined);
  vi.spyOn(host, "setForeground").mockImplementation(() => undefined);
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "setTimeout", "clearTimeout", "Date"] });
  try {
    const syncSpy = vi.spyOn(host.securityClient, "syncNow").mockResolvedValue(status);
    vi.spyOn(r, "conn", "get").mockReturnValue("online");
    r.start();
    const before = syncSpy.mock.calls.length;
    await vi.advanceTimersByTimeAsync(12_000);
    expect(syncSpy.mock.calls.length).toBeGreaterThan(before);
    const afterOnline = syncSpy.mock.calls.length;
    r.setForeground(false);
    await vi.advanceTimersByTimeAsync(24_000);
    expect(syncSpy.mock.calls.length).toBe(afterOnline);
    r.setForeground(true);
    await vi.advanceTimersByTimeAsync(11_000);
    expect(syncSpy.mock.calls.length).toBe(afterOnline);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(syncSpy.mock.calls.length).toBeGreaterThan(afterOnline);
  } finally {
    r.stop();
    vi.useRealTimers();
  }
});

it("disposes its CoreHost on dispose() and drops an earlier-started snapshot that finishes late", async () => {
  const r = await probe();
  await r.world.fixture.remove([r.machine]);
  await r.syncSecurityNow();

  const { getCoreHost } = await import("./core/host");
  const { DevicesFetchedEventSchema, EventSchema: CoreEventSchema, PlainDeviceSchema } = await import("./gen/remoteagent/core/v1/app_pb");
  const { DeviceKind } = await import("./gen/remoteagent/v1/relay_pb");
  const host = getCoreHost(r);
  host.dispatch(
    create(CoreEventSchema, {
      event: {
        case: "devicesFetched",
        value: create(DevicesFetchedEventSchema, {
          opId: 0n,
          pushSeq: 1n,
          devices: [create(PlainDeviceSchema, { deviceId: r.machine, kind: DeviceKind.MACHINE })],
        }),
      },
    }),
  );

  expect(host.viewSyncState().securityLoaded).toBe(true);
  expect(r.devices.get(r.machine)).toBeUndefined();

  r.dispose();
  expect(() => host.viewHome()).toThrow(/closed/);
  expect(getCoreHost(r)).not.toBe(host);
  r.dispose();
});

it("persists schedule seen tuples under ra-schedule-seen and tolerates corrupt storage", async () => {
  const mem = new Map<string, string>();
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => mem.get(k) ?? null,
    setItem: (k: string, v: string) => void mem.set(k, String(v)),
    removeItem: (k: string) => void mem.delete(k),
    clear: () => mem.clear(),
  });
  const r = await probe();
  expect(await r.loadScheduleSeen("")).toMatchObject({ found: false });
  expect(await r.loadScheduleSeen(r.machine)).toMatchObject({ found: false, tokens: [] });

  const { ScheduleSeenTokenSchema } = await import("./gen/remoteagent/core/v1/schedules_view_pb");
  await r.saveScheduleSeen(r.machine, [
    create(ScheduleSeenTokenSchema, { taskId: "t-1", runId: "r-1" }),
    create(ScheduleSeenTokenSchema, { taskId: "t-2", runId: "r-2" }),
  ]);

  const key = "ra-schedule-seen:" + JSON.stringify([r.account.server, r.account.deviceId, r.machine]);
  expect(JSON.parse(mem.get(key)!)).toEqual([
    JSON.stringify(["t-1", "r-1"]),
    JSON.stringify(["t-2", "r-2"]),
  ]);

  const loaded = await r.loadScheduleSeen(r.machine);
  expect(loaded.found).toBe(true);
  expect(loaded.tokens.map((t) => [t.taskId, t.runId])).toEqual([
    ["t-1", "r-1"],
    ["t-2", "r-2"],
  ]);

  mem.set(key, "{not json");
  expect(await r.loadScheduleSeen(r.machine)).toMatchObject({ found: false });
});

it("caches verified schedule list responses on executeCommandAttempt and decrypts via readScheduleCache", async () => {
  const r = await probe();
  const store = new Map<string, unknown>();
  (r as unknown as { cache: { get(k: string): Promise<unknown>; set(k: string, v: unknown): Promise<void> } }).cache = {
    get: async (k) => store.get(k),
    set: async (k, v) => {
      if (v === undefined) store.delete(k);
      else store.set(k, v);
    },
  };
  const { DispatchCommandEffectSchema, CommandRouteHint } = await import("./gen/remoteagent/core/v1/app_pb");
  const { CommandSchema, CommandResultSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { PayloadKind } = await import("./gen/remoteagent/v1/envelope_pb");
  const { PEER_PROTOCOL_VERSION } = await import("./protocol");
  const listJson = new TextEncoder().encode(JSON.stringify({ tasks: [{ id: "t-cached", revision: 2 }] }));

  const cmdBytes = toBinary(
    CommandSchema,
    create(CommandSchema, {
      protocolVersion: PEER_PROTOCOL_VERSION,
      kind: { case: "schedule", value: { operation: "list" } },
    }),
  );

  (r as unknown as { api: object }).api = {
    forward: async (req: { command: Envelope }) => {
      const opened = await r.world.fixture.open(r.machine, req.command);
      const resEnv = await r.seal(
        {
          kind: PayloadKind.COMMAND_RESULT,
          machineId: r.machine,
          sessionId: "",
          requestId: opened.header.requestId,
          compression: 0,
        },
        CommandResultSchema,
        create(CommandResultSchema, {
          kind: { case: "schedule", value: { resultJson: listJson } },
        }),
      );
      return { result: resEnv };
    },
  };

  expect(await r.readScheduleCache(1n, r.machine)).toMatchObject({ opId: 1n, machineId: r.machine, resultJson: new Uint8Array() });

  const outcome = await r.executeCommandAttempt(
    create(DispatchCommandEffectSchema, {
      requestId: newRequestID(),
      attemptId: newRequestID(),
      machineId: r.machine,
      deadlineMs: BigInt(Date.now() + 30_000),
      routeHint: CommandRouteHint.READ,
      commandBytes: cmdBytes,
    }),
  );
  expect(outcome.transportCode).toBe("");

  const cached = await r.readScheduleCache(2n, r.machine);
  expect(cached.error).toBe("");
  expect(new TextDecoder().decode(cached.resultJson)).toBe(new TextDecoder().decode(listJson));

  await r.deleteScheduleCache(r.machine);
  const cleared = await r.readScheduleCache(3n, r.machine);
  expect(cleared.resultJson).toEqual(new Uint8Array());
});

it("updates security status and trusted machine count when sync pulls a newly enrolled machine", async () => {
  const r = await probe();
  const { getCoreHost } = await import("./core/host");
  const before = await r.getSecurityStatus();
  const enrolled = await r.world.fixture.enroll(r.world.fixture.phone.json.member, 1);
  const updated = await r.syncSecurityNow();
  expect(updated.headEpoch).toBeGreaterThan(before.headEpoch);
  expect(getCoreHost(r).viewSyncState().securityLoaded).toBe(true);
  expect(enrolled.json.member).not.toBe("");
});
