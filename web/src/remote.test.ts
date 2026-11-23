import { t as message } from "./i18n";
import { create, fromBinary, fromJson, toBinary } from "@bufbuild/protobuf";
import { HeaderSchema, type Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { LoginResponseSchema } from "./gen/remoteagent/v1/relay_pb";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, expect, it, vi } from "vitest";
import { patchSyncState, SyntheticRemote } from "./test/remote-fixture";
const liveFixtures: SyntheticRemote[] = [];
const demoFixtures: Remote[] = [];
async function probe() {
  const r = await SyntheticRemote.create();
  liveFixtures.push(r);
  return r;
}
function trackDemo<T extends Remote>(r: T): T {
  demoFixtures.push(r);
  return r;
}
afterEach(async () => {
  for (const r of liveFixtures.splice(0)) await r.dispose();
  for (const r of demoFixtures.splice(0)) r.dispose();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
import {
  CommandError,
  COMMAND_TTL_MS,
  newSlot,
  RequestSlot,
  Remote,
  uncertain,
  type CommandKind,
} from "./remote";

it("reuses a request slot only when the outcome is unknown", () => {
  // Timed out or cut off: the command may have run; a retry must reuse
  // the slot so the daemon answers from its log.
  expect(uncertain(new CommandError("unknown_outcome", "连接超时", false, false, "unknown"))).toBe(true);
  expect(uncertain(new CommandError("aborted", "outcome unknown", true))).toBe(true);
  expect(uncertain(new ConnectError("reset", Code.Internal))).toBe(true);
  // A definite answer: the next attempt starts afresh.
  expect(uncertain(new CommandError("unavailable", "电脑不在线"))).toBe(false);
  expect(
    uncertain(
      new CommandError("failed_precondition", "cwd is not a directory"),
    ),
  ).toBe(false);
  expect(uncertain(new Error("意外的回复"))).toBe(false);
});

it("gives each slot a fresh ID and a deadline a minute out", () => {
  const a = newSlot(1000);
  const b = newSlot(1000);
  expect(a.requestId).not.toBe(b.requestId);
  expect(a.requestId).toMatch(/^[0-9a-f]{32}$/);
  expect(a.deadline).toBe(1000 + COMMAND_TTL_MS);
});

it("reports a command unknown when a retry finds the machine gone", async () => {
  // A Remote whose server answers "no result from the machine" and then
  // "machine offline".
  const r = await probe();
  const replies = [
    new ConnectError(
      "no reply from the machine in time",
      Code.DeadlineExceeded,
    ),
    new ConnectError("machine is offline", Code.Unavailable),
  ];
  const forward = vi.fn(async () => {
    throw replies.shift();
  });
  (r as unknown as { api: object }).api = { forward };
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    const result = r
      .machineCommand(r.machine, { case: "listModels", value: {} })
      .catch((x) => x);
    await vi.waitFor(
      () => {
        expect(forward).toHaveBeenCalledTimes(1);
        expect(vi.getTimerCount()).toBeGreaterThan(0);
      },
      { interval: 5 },
    );
    expect(replies).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1000);
    const e = await result;
    expect(e).toBeInstanceOf(CommandError);
    expect((e as CommandError).code).toBe("unknown_outcome");
    expect((e as CommandError).message).toBe(message("errors.connection_dropped_midway"));
    expect(replies).toHaveLength(0);
  } finally {
    vi.useRealTimers();
  }
}, 10_000);

it("signs every command it forwards with the device key, naming itself the sender", async () => {
  const r = await probe();
  let sent: Envelope | undefined;
  (r as unknown as { api: object }).api = {
    forward: async (req: { command: Envelope }) => {
      sent = req.command;
      throw new ConnectError("machine is offline", Code.Unavailable);
    },
  };
  await r
    .machineCommand(r.machine, { case: "listModels", value: {} })
    .catch(() => undefined);
  expect(sent).toBeTruthy();
  const opened = await r.world.fixture.open(r.machine, sent!);
  expect(opened.header.sender).toBe(r.account.deviceId);
  expect(sent!.signature.length).toBe(64);
});

it("sends its protocol version with a command, and sends none to a machine of another version", async () => {
  const { create } = await import("@bufbuild/protobuf");
  const { CommandSchema, MachineMetaSchema } = await import(
    "./gen/remoteagent/v1/remote_pb"
  );
  const { PEER_PROTOCOL_VERSION } = await import("./protocol");
  const r = await probe();
  const sent: Envelope[] = [];
  (r as unknown as { api: object }).api = {
    forward: async (req: { command: Envelope }) => {
      sent.push(req.command);
      throw new ConnectError("machine is offline", Code.Unavailable);
    },
  };
  const machine = (id: string, protocolVersion: number) =>
    patchSyncState(r, {
      conn: "online",
      listed: true,
      machines: new Map([
        ...r.machines,
        [
          id,
          {
            id,
            online: true,
            lastSeenMs: 0n,
            metaRev: 1n,
            meta: create(MachineMetaSchema, {
              hostname: "MacBook.local",
              protocolVersion,
            }),
          },
        ],
      ]),
    });
  const run = (id: string) =>
    r.machineCommand(id, { case: "listModels", value: {} }).then(
      () => undefined,
      (e: unknown) => e,
    );

  // The same version, and a machine whose metadata has not opened yet.
  machine(r.machine, PEER_PROTOCOL_VERSION);
  await run(r.machine);
  const withoutMachine = new Map(r.machines);
  withoutMachine.delete(r.machine);
  patchSyncState(r, { machines: withoutMachine });
  await run(r.machine);
  expect(sent).toHaveLength(2);
  for (const env of sent)
    expect(
      fromBinary(
        CommandSchema,
        (await r.world.fixture.open(r.machine, env)).plaintext,
      ).protocolVersion,
    ).toBe(PEER_PROTOCOL_VERSION);

  // An older machine, including one from before it reported a version.
  machine("older", PEER_PROTOCOL_VERSION - 1);
  const older = await run("older");
  expect(older).toBeInstanceOf(CommandError);
  expect((older as CommandError).message).toBe(
    "MacBook 上的 remote-agent 版本较旧，请在电脑上更新后再试",
  );
  // Never sent, so a later attempt starts afresh.
  expect(uncertain(older)).toBe(false);

  // A newer machine: it is this page that is behind.
  machine("newer", PEER_PROTOCOL_VERSION + 1);
  const newer = await run("newer");
  expect(newer).toBeInstanceOf(CommandError);
  expect((newer as CommandError).message).toBe(
    message("errors.this_page_is_out_of_date_refresh_or_update"),
  );
  expect(sent).toHaveLength(2);
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

it("uploads an image in chunks and resumes from what the machine has after a lost reply", async () => {
  const { DemoRemote } = await import("./dev/demo");
  const r = trackDemo(DemoRemote.create(new URLSearchParams()));
  const bytes = new Uint8Array(2_000_000).map((_, i) => i % 251);
  const calls: string[] = [];
  let persisted = 0n;
  let lost = true;
  r.machineCommand = async (_m: string, cmd: CommandKind) => {
    const { create } = await import("@bufbuild/protobuf");
    const { CommandResultSchema } = await import(
      "./gen/remoteagent/v1/remote_pb"
    );
    const status = (extra = {}) => ({
      status: {
        transferId: "t",
        state: 1,
        persistedBytes: persisted,
        totalBytes: BigInt(bytes.length),
        maxChunkBytes: 1_000_000,
        ...extra,
      },
    });
    switch (cmd?.case) {
      case "beginImageUpload":
        calls.push(`begin ${cmd.value.totalBytes} ${cmd.value.sha256?.length}`);
        return create(CommandResultSchema, {
          kind: { case: "beginImageUpload", value: status() },
        });
      case "putImageChunk": {
        calls.push(`put ${cmd.value.offset} ${cmd.value.data?.length}`);
        expect(cmd.value.offset).toBe(persisted);
        expect([...cmd.value.data!.slice(0, 3)]).toEqual([
          ...bytes.slice(
            Number(cmd.value.offset),
            Number(cmd.value.offset) + 3,
          ),
        ]);
        persisted += BigInt(cmd.value.data!.length);
        // The reply to the second chunk is lost, though it was stored.
        if (persisted > 800_000n && lost) {
          lost = false;
          throw new CommandError("unknown_outcome", "连接超时", false, false, "unknown");
        }
        return create(CommandResultSchema, {
          kind: { case: "putImageChunk", value: status() },
        });
      }
      case "getImageUploadStatus":
        calls.push("status");
        return create(CommandResultSchema, {
          kind: { case: "getImageUploadStatus", value: status() },
        });
      case "completeImageUpload":
        calls.push("complete");
        return create(CommandResultSchema, {
          kind: {
            case: "completeImageUpload",
            value: status({
              state: 2,
              imageId: "img-1",
              expiresMs: 123456789n,
            }),
          },
        });
    }
    throw new Error("unexpected " + cmd?.case);
  };
  const progress: number[] = [];
  const id = await Remote.prototype.uploadImage.call(
    r,
    "mac",
    { bytes, format: "jpeg" },
    (d: number) => progress.push(d),
  );
  expect(id).toEqual({ imageId: "img-1", expiresMs: 123456789 });
  // Chunks of at most 768 KiB, each starting where the machine is.
  expect(calls).toEqual([
    `begin 2000000 32`,
    "put 0 786432",
    "put 786432 786432",
    "status",
    "put 1572864 427136",
    "complete",
  ]);
  expect(progress.at(-1)).toBe(2_000_000);
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

it("restores, for the next visit's first paint, the model lists and settings the machines last reported", async () => {
  const { DemoRemote } = await import("./dev/demo");
  const { Agent } = await import("./gen/remoteagent/v1/event_pb");
  const kept = new Map<string, unknown>();
  const cache = {
    read: async () => [],
    readNewest: async () => [],
    append: async () => undefined,
    drop: async () => undefined,
    trim: async () => undefined,
    get: async (k: string) => kept.get(k),
    set: async (k: string, v: unknown) => void kept.set(k, v),
  };
  const account = {
    server: "https://ra.example.com",
    root: {} as CryptoKey,
    rootRaw: new Uint8Array(32),
    devicePrivate: {} as CryptoKey,
    deviceId: "me",
  };
  class Kept extends DemoRemote {
    static open() {
      return new Kept(account, cache);
    }
    warmed() {
      return (this as unknown as { warmInfoOnce(): Promise<void> }).warmInfoOnce();
    }
  }
  const first = trackDemo(Kept.open());
  // Exercise real demo responses/cache writes without its two simulated
  // 500 ms network delays. This test asserts persistence, not latency.
  vi.spyOn(first as any, "pause").mockResolvedValue(undefined);
  (first as unknown as { seed(): void; syncDemoToCore(): void }).seed(); // the demo's machines, online
  (first as unknown as { syncDemoToCore(): void }).syncDemoToCore();
  expect(first.cachedModels("mac", Agent.CODEX)).toBeUndefined();
  const models = await first.listModels("mac", Agent.CODEX);
  await first.settings("mac");
  await vi.waitFor(() => expect(kept.has("info")).toBe(true));

  // The next visit knows them before any machine answers.
  const next = trackDemo(Kept.open());
  await next.warmed();
  expect(
    next.cachedModels("mac", Agent.CODEX)?.models.map((m) => m.displayName),
  ).toEqual(models.models.map((m) => m.displayName));
  expect(next.cachedModels("mac", Agent.CLAUDE)).toBeUndefined();
  expect(next.cachedSettings("mac")).toEqual(first.cachedSettings("mac"));

  // Another account's or device's values are not taken.
  const other = trackDemo(new (class extends DemoRemote {
    constructor() {
      super({ ...account, deviceId: "someone-else" }, cache);
    }
  })());
  await (other as unknown as { warmInfoOnce(): Promise<void> }).warmInfoOnce();
  expect(other.cachedModels("mac", Agent.CODEX)).toBeUndefined();
});

it("keeps an unauthenticated command result unknown and retries its slot", async () => {
  const { PayloadKind } = await import("./gen/remoteagent/v1/envelope_pb");
  const { create } = await import("@bufbuild/protobuf");
  const { CommandResultSchema } = await import(
    "./gen/remoteagent/v1/remote_pb"
  );
  const r = await probe();
  const ids: string[] = [];
  (r as unknown as { api: object }).api = {
    forward: async ({ command }: { command: Envelope }) => {
      const requestId = fromBinary(HeaderSchema, command.header).requestId;
      ids.push(requestId);
      const result = await r.seal(
        { kind: PayloadKind.COMMAND_RESULT, machineId: r.machine, requestId },
        CommandResultSchema,
        create(CommandResultSchema, {
          kind: {
            case: "createSession",
            value: { session: { id: "started" } },
          },
        }),
      );
      result.ciphertext[0] ^= 1;
      return { result };
    },
  };
  const slot = newSlot();
  for (let i = 0; i < 2; i++) {
    const error = await r
      .machineCommand(
        r.machine,
        { case: "createSession", value: { prompt: "task" } },
        slot,
      )
      .catch((e) => e);
    expect(uncertain(error)).toBe(true);
    expect(slot.evidence.unresolved).toBe(true);
  }
  expect(ids[1]).toBe(ids[0]);
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

it.each([Code.Unavailable, Code.PermissionDenied, Code.FailedPrecondition])(
  "keeps an executed command unknown after an outer relay error %s",
  async (code) => {
    const r = await probe();
    let executed = 0;
    (r as unknown as { api: object }).api = {
      forward: async () => {
        executed++;
        throw new ConnectError(
          "synthetic relay discarded the executor result",
          code,
        );
      },
    };
    const slot = newSlot();
    const error = await r
      .machineCommand(
        r.machine,
        { case: "createSession", value: { prompt: "synthetic intent" } },
        slot,
      )
      .catch((e) => e);
    expect(executed).toBe(1);
    expect(uncertain(error)).toBe(true);
  },
);

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

it("uploads files through shared RPCs with UNSPECIFIED format and separate IDs", async () => {
  const { CommandResultSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const calls: CommandKind[] = [];
  const bytes = new Uint8Array([0, 1, 255, 10]);
  const r = { machineCommand: async (_m: string, cmd: CommandKind) => {
    if (!cmd) throw new Error("missing command");
    calls.push(cmd);
    if (cmd.case === "beginImageUpload") {
      expect(cmd.value.fileName).toBe("报告.pdf"); expect(cmd.value.format).toBe(0); expect(cmd.value.totalBytes).toBe(4n);
      expect(cmd.value.sha256).toHaveLength(32);
    }
    if (cmd.case === "putImageChunk") expect(cmd.value.data).toEqual(bytes);
    const done = cmd.case === "completeImageUpload";
    return create(CommandResultSchema, { kind: { case: cmd.case as "beginImageUpload", value: { status: { persistedBytes: cmd.case === "beginImageUpload" ? 0n : 4n, totalBytes: 4n, maxChunkBytes: 786432, fileId: done ? "file-1" : "", expiresMs: 123456789n } } } });
  } };
  const result = await Remote.prototype.uploadImage.call(r as unknown as Remote, "mac", { bytes, format: "file", fileName: "报告.pdf" });
  expect(result).toEqual({ imageId: "", fileId: "file-1", expiresMs: 123456789 });
  expect(calls.map((c) => c?.case)).toEqual(["beginImageUpload", "putImageChunk", "completeImageUpload"]);
});

it("retries Complete on the same transfer while hashing after a lost reply, without reuploading bytes", async () => {
  const { CommandResultSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const calls: string[] = [];
  let transfer = ""; let complete = 0; let persisted = 0n;
  const r = { machineCommand: async (_m: string, cmd: CommandKind) => {
    if (!cmd?.case) throw new Error("missing command");
    calls.push(cmd.case);
    if (cmd.case === "beginImageUpload") transfer = cmd.value.transferId!;
    else expect(cmd.value).toMatchObject({ transferId: transfer });
    if (cmd.case === "putImageChunk") persisted = 4n;
    if (cmd.case === "completeImageUpload" && ++complete === 1) throw new CommandError("unknown_outcome", "lost completion reply", false, false, "unknown");
    const done = cmd.case === "completeImageUpload";
    return create(CommandResultSchema, { kind: { case: cmd.case as "beginImageUpload", value: { status: { transferId: transfer, state: done ? 2 : 1, persistedBytes: persisted, totalBytes: 4n, maxChunkBytes: 786432, fileId: done ? "ready" : "", expiresMs: 123456789n } } } });
  } };
  const result = await Remote.prototype.uploadImage.call(r as unknown as Remote, "mac", { bytes: new Uint8Array([0,1,2,3]), format: "file", fileName: "data.bin" });
  expect(result.fileId).toBe("ready");
  expect(calls).toEqual(["beginImageUpload", "putImageChunk", "completeImageUpload", "getImageUploadStatus", "completeImageUpload"]);
});

it.each(["send", "createSession"] as const)("rejects file-bearing %s when refreshed capability is false", async (kind) => {
  const r = await probe();
  const { MachineMetaSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { PEER_PROTOCOL_VERSION } = await import("./protocol");
  patchSyncState(r, {
    conn: "online",
    listed: true,
    machines: new Map([[r.machine, { id: r.machine, online: true, lastSeenMs: 0n, metaRev: 0n, meta: create(MachineMetaSchema, { protocolVersion: PEER_PROTOCOL_VERSION, capabilities: { fileUpload: false } }) }]]),
  });
  const forward = vi.fn(async () => { throw new ConnectError("reached transport", Code.InvalidArgument); });
  (r as unknown as { api: object }).api = { forward };
  const cmd = kind === "send" ? { case: "send" as const, value: { sessionId: "s", text: "text with file", fileIds: ["file"] } } : { case: "createSession" as const, value: { prompt: "text with file", fileIds: ["file"] } };
  const error = await (kind === "send" ? r.command("s", cmd) : r.machineCommand(r.machine, cmd)).catch(e => e);
  expect(error).toBeInstanceOf(CommandError);
  expect(error.code).toBe("failed_precondition");
  expect(error.message).toContain(message("session.update_computer"));
  expect(forward).not.toHaveBeenCalled();
});

it("checks the canonical file-bearing payload again on an authorized create retry", async () => {
  const r = await probe();
  const { MachineMetaSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { PEER_PROTOCOL_VERSION } = await import("./protocol");
  patchSyncState(r, {
    conn: "online",
    listed: true,
    machines: new Map([[r.machine, { id: r.machine, online: true, lastSeenMs: 0n, metaRev: 0n, meta: create(MachineMetaSchema, { protocolVersion: PEER_PROTOCOL_VERSION, capabilities: { fileUpload: true } }) }]]),
  });
  const forward = vi.fn(async () => { throw new ConnectError("no result", Code.InvalidArgument); });
  (r as unknown as { api: object }).api = { forward };
  const slot = newSlot();
  await r.machineCommand(r.machine, { case: "createSession", value: { prompt: "first", fileIds: ["file"] } }, slot).catch(() => undefined);
  expect(forward).toHaveBeenCalledOnce();
  r.machines.get(r.machine)!.meta!.capabilities!.fileUpload = false;
  // A caller cannot strip the files from an already authorized request.
  const error = await r.machineCommand(r.machine, { case: "createSession", value: { prompt: "changed" } }, slot).catch(e => e);
  expect(error.message).toContain(message("session.update_computer"));
  expect(forward).toHaveBeenCalledOnce();
});

it("rechecks file capability after asynchronous sealing before first dispatch", async () => {
  const r = await probe();
  const { MachineMetaSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { PEER_PROTOCOL_VERSION } = await import("./protocol");
  patchSyncState(r, {
    conn: "online",
    listed: true,
    machines: new Map([[r.machine, { id: r.machine, online: true, lastSeenMs: 0n, metaRev: 0n, meta: create(MachineMetaSchema, { protocolVersion: PEER_PROTOCOL_VERSION, capabilities: { fileUpload: true } }) }]]),
  });
  const seal = (r as any).sealSecureMessage.bind(r);
  vi.spyOn(r as any, "sealSecureMessage").mockImplementation(async (...args: unknown[]) => {
    const envelope = await seal(...args);
    r.machines.get(r.machine)!.meta!.capabilities!.fileUpload = false;
    return envelope;
  });
  const forward = vi.fn(async () => { throw new ConnectError("reached transport", Code.InvalidArgument); });
  (r as unknown as { api: object }).api = { forward };
  const error = await r.command("s", { case: "send", value: { text: "text", fileIds: ["file"] } }).catch(e => e);
  expect(error.message).toContain(message("session.update_computer"));
  expect(forward).not.toHaveBeenCalled();
});

it("blocks an automatic file retry after metadata refresh and keeps its outcome unknown", async () => {
  const r = await probe();
  const { MachineMetaSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { PEER_PROTOCOL_VERSION } = await import("./protocol");
  patchSyncState(r, {
    conn: "online",
    listed: true,
    machines: new Map([[r.machine, { id: r.machine, online: true, lastSeenMs: 0n, metaRev: 0n, meta: create(MachineMetaSchema, { protocolVersion: PEER_PROTOCOL_VERSION, capabilities: { fileUpload: true } }) }]]),
  });
  const forward = vi.fn(async () => { r.machines.get(r.machine)!.meta!.capabilities!.fileUpload = false; throw new ConnectError("no reply", Code.DeadlineExceeded); });
  (r as unknown as { api: object }).api = { forward };
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    const done = r.command("s", { case: "send", value: { text: "text", fileIds: ["file"] } }).catch(e => e);
    await vi.waitFor(
      () => {
        expect(forward).toHaveBeenCalledOnce();
        expect(vi.getTimerCount()).toBeGreaterThan(0);
      },
      { interval: 5 },
    );
    await vi.advanceTimersByTimeAsync(1000);
    const error = await done;
    expect(error.code).toBe("unknown_outcome");
    expect(error.message).toContain(message("session.update_computer"));
    expect(forward).toHaveBeenCalledOnce();
  } finally { vi.useRealTimers(); }
});

it.each(["send", "createSession"] as const)("allows text/image-only %s with file capability off", async (kind) => {
  const r = await probe();
  const { MachineMetaSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { PEER_PROTOCOL_VERSION } = await import("./protocol");
  patchSyncState(r, {
    conn: "online",
    listed: true,
    machines: new Map([[r.machine, { id: r.machine, online: true, lastSeenMs: 0n, metaRev: 0n, meta: create(MachineMetaSchema, { protocolVersion: PEER_PROTOCOL_VERSION, capabilities: { fileUpload: false } }) }]]),
  });
  const forward = vi.fn(async () => { throw new ConnectError("transport reached", Code.InvalidArgument); });
  (r as unknown as { api: object }).api = { forward };
  const cmd = kind === "send" ? { case: "send" as const, value: { text: "text", imageIds: ["image"] } } : { case: "createSession" as const, value: { prompt: "text", imageIds: ["image"] } };
  await (kind === "send" ? r.command("s", cmd) : r.machineCommand(r.machine, cmd)).catch(() => undefined);
  expect(forward).toHaveBeenCalledOnce();
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
    const { SecurityPendingOpSummarySchema, SecurityWorkflowStatusSchema } = await import("./gen/remoteagent/core/v1/security_pb");
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
      if (outcome === "final") throw new SecurityWorkflowError("candidate must be removed", "invalid", false, false, "");
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

it("retries an unsigned in-flight abort with the same request slot", async () => {
  const r = await probe();
  const ids: string[] = [];
  const forward = vi.fn(async ({ command }: { command: Envelope }) => {
    ids.push(fromBinary(HeaderSchema, command.header).requestId);
    throw new ConnectError("no final result", Code.Aborted);
  });
  (r as unknown as { api: object }).api = { forward };
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    const slot = newSlot();
    const result = r.machineCommand(r.machine, { case: "listModels", value: {} }, slot).catch(e => e);
    // Let crypto and authorization settle before advancing each retry timer.
    for (const delay of [1000, 2000, 3000]) {
      const target = ids.length + 1;
      await vi.waitFor(() => expect(ids).toHaveLength(target));
      await vi.waitFor(() => expect(vi.getTimerCount()).toBeGreaterThan(0));
      await vi.advanceTimersByTimeAsync(delay);
    }
    expect(uncertain(await result)).toBe(true);
    expect(ids).toEqual(Array(4).fill(slot.requestId));
  } finally { vi.useRealTimers(); }
});

it.each([999, 1000, 1001])("bounds abort retry against remaining deadline %i ms", async remaining => {
  const r = await probe();
  let now = Date.now();
  vi.spyOn(Date, "now").mockImplementation(() => now);
  const slot = new RequestSlot(newSlot().requestId, now + remaining, undefined, false);
  const forward = vi.fn(async () => { throw new ConnectError("no signed result", Code.Aborted); });
  (r as unknown as { api: object }).api = { forward };
  // Advance only this request's retry delay. Advancing the synthetic clock
  // for fixture/network timers can consume the 1 ms boundary under load.
  const realTimeout = globalThis.setTimeout.bind(globalThis);
  const timeout = vi.spyOn(globalThis, "setTimeout").mockImplementation(((fn: () => void, ms: number) => {
    if (ms !== 1000) return realTimeout(fn, ms);
    now += ms;
    queueMicrotask(fn);
    return 0;
  }) as typeof setTimeout);
  try {
    await expect(r.machineCommand(r.machine, { case: "listModels", value: {} }, slot)).rejects.toMatchObject({ code: "unknown_outcome" });
    expect(forward).toHaveBeenCalledTimes(remaining > 1000 ? 2 : 1);
    for (const [req] of forward.mock.calls as unknown as [{command: Envelope}][])
      expect(fromBinary(HeaderSchema, req.command.header).deadlineMs).toBe(BigInt(slot.deadline));
  } finally { timeout.mockRestore(); }
});

it.each(["canceled", "deadline_exceeded", "unknown", "generation_conflict"])("ends a verified read-only %s without retry", async code => {
  const r = await probe();
  const { CommandResultSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const slot = newSlot();
  const result = await r.seal({ kind: 6, requestId: slot.requestId }, CommandResultSchema, create(CommandResultSchema, { errorCode: code, errorMessage: "read stopped" }));
  const forward = vi.fn(async () => ({result}));
  (r as unknown as {api: object}).api = {forward};
  const error = await r.machineCommand(r.machine, {case: "listModels", value: {}}, slot).catch(e => e);
  expect(error).toMatchObject({code, fromDaemon: true, readOnly: true});
  expect(uncertain(error)).toBe(false);
  expect(forward).toHaveBeenCalledTimes(1);
});

it.each(["canceled", "deadline_exceeded", "aborted", "not_found", "permission_denied"])("shared-file %s is an authenticated final read failure", async code => {
 const r=await probe();
 const {CommandResultSchema}=await import("./gen/remoteagent/v1/remote_pb");
 // Bind the signed reply to the actual request generated by the download API.
 const forward=vi.fn(async (req:{command:Envelope})=>{
  const h=fromBinary(HeaderSchema,req.command.header);
  return {result:await r.seal({kind:6,sessionId:"s",requestId:h.requestId},CommandResultSchema,create(CommandResultSchema,{errorCode:code,errorMessage:"fixture"}))};
 });
 (r as unknown as {api:object}).api={forward};
 const error=await r.getSharedFileChunk("s","file",0n).catch(e=>e);
 expect(error).toMatchObject({code,fromDaemon:true,readOnly:true});
 expect(uncertain(error)).toBe(false);expect(forward).toHaveBeenCalledTimes(1);
});

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
        requestId: newSlot().requestId,
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

it("classifies readOnlyCommand consistently with shared transport fixtures", async () => {
  const { default: fixture } = await import("../../testdata/transport/outcomes.json");
  const { readOnlyCommand } = await import("./remote");
  const { CommandSchema } = await import("./gen/remoteagent/v1/remote_pb");
  expect(fixture.protocol).toBe(15);
  for (const v of fixture.commands) {
    const body: Record<string, string> = v.kind === "schedule" ? { operation: ("operation" in v ? v.operation : "") ?? "" } : {};
    const cmd = v.kind ? fromJson(CommandSchema, { [v.kind]: body }) : create(CommandSchema);
    const readOnly = readOnlyCommand(cmd.kind);
    expect(readOnly, `${v.kind}/${"operation" in v ? v.operation : ""}`).toBe(v.readOnly);
    for (const code of ["canceled", "deadline_exceeded", "aborted"]) {
      expect(uncertain(new CommandError(code, "fixture", true, readOnly, "executed"))).toBe(false);
    }
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
      requestId: newSlot().requestId,
      attemptId: newSlot().requestId,
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
