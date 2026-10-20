import { create, fromBinary } from "@bufbuild/protobuf";
import { HeaderSchema, type Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, afterEach, expect, it, vi } from "vitest";
import { SyntheticRemote } from "./test/remote-fixture";
import { removeFixtureBuild } from "./test/security-fixture";
const liveFixtures: SyntheticRemote[] = [];
async function probe() {
  const r = await SyntheticRemote.create();
  liveFixtures.push(r);
  return r;
}
afterEach(async () => {
  for (const r of liveFixtures.splice(0)) await r.dispose();
  vi.restoreAllMocks();
});
afterAll(removeFixtureBuild);
import {
  CommandError,
  COMMAND_TTL_MS,
  newSlot,
  Remote,
  uncertain,
  type CommandKind,
  type RequestSlot,
} from "./remote";

it("reuses a request slot only when the outcome is unknown", () => {
  // Timed out or cut off: the command may have run; a retry must reuse
  // the slot so the daemon answers from its log.
  expect(uncertain(new CommandError("unknown", "连接超时"))).toBe(true);
  expect(uncertain(new CommandError("aborted", "outcome unknown"))).toBe(true);
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
  (r as unknown as { api: object }).api = {
    forward: async () => {
      throw replies.shift();
    },
  };
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const clockTimeout = globalThis.setTimeout;
  let scheduled!: () => void;
  const retryScheduled = new Promise<void>(resolve => { scheduled = resolve; });
  globalThis.setTimeout = ((...args: Parameters<typeof setTimeout>) => {
    const handle = clockTimeout(...args);
    if (args[1] === 1000) scheduled();
    return handle;
  }) as typeof setTimeout;
  try {
    const result = r
      .machineCommand(r.machine, { case: "listModels", value: {} })
      .catch((x) => x);
    // Observing the API call does not guarantee its rejection has reached
    // forward() and registered the timer yet. Advance only after registration.
    await retryScheduled;
    expect(replies).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1000);
    const e = await result;
    expect(e).toBeInstanceOf(CommandError);
    expect((e as CommandError).code).toBe("unknown");
    expect(replies).toHaveLength(0);
  } finally {
    globalThis.setTimeout = clockTimeout;
    vi.useRealTimers();
  }
}, 10_000);

it("keeps a message of unknown outcome on its request when the machine is then offline", async () => {
  const { DemoRemote } = await import("./dev/demo");
  const r = DemoRemote.create(new URLSearchParams());
  const replies = [
    new CommandError("unknown", "连接超时"),
    new CommandError("unavailable", "电脑不在线，指令没有发出"),
  ];
  const seen: string[] = [];
  r.command = async (_s: string, _c: unknown, slot?: RequestSlot) => {
    seen.push(slot!.requestId);
    throw (
      replies.shift() ??
      new CommandError("unavailable", "电脑不在线，指令没有发出")
    );
  };
  r.send("s-idle", "hi");
  await vi.waitFor(() => expect(r.outbox("s-idle")[0].status).toBe("failed"));
  const o = r.outbox("s-idle")[0];
  r.retrySend("s-idle", o.sendId);
  await vi.waitFor(() => expect(seen).toHaveLength(2));
  await vi.waitFor(() => expect(r.outbox("s-idle")[0].status).toBe("failed"));
  r.retrySend("s-idle", o.sendId);
  await vi.waitFor(() => expect(seen).toHaveLength(3));
  expect(new Set(seen).size).toBe(1);
  expect(r.outbox("s-idle")[0].unknown).toBe(true);
});

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
  const { PROTOCOL_VERSION } = await import("./protocol");
  const r = await probe();
  const sent: Envelope[] = [];
  (r as unknown as { api: object }).api = {
    forward: async (req: { command: Envelope }) => {
      sent.push(req.command);
      throw new ConnectError("machine is offline", Code.Unavailable);
    },
  };
  const machine = (id: string, protocolVersion: number) =>
    r.machines.set(id, {
      id,
      online: true,
      lastSeenMs: 0n,
      metaRev: 1n,
      meta: create(MachineMetaSchema, {
        hostname: "MacBook.local",
        protocolVersion,
      }),
    });
  const run = (id: string) =>
    r.machineCommand(id, { case: "listModels", value: {} }).then(
      () => undefined,
      (e: unknown) => e,
    );

  // The same version, and a machine whose metadata has not opened yet.
  machine(r.machine, PROTOCOL_VERSION);
  await run(r.machine);
  r.machines.delete(r.machine);
  await run(r.machine);
  expect(sent).toHaveLength(2);
  for (const env of sent)
    expect(
      fromBinary(
        CommandSchema,
        (await r.world.fixture.open(r.machine, env)).plaintext,
      ).protocolVersion,
    ).toBe(PROTOCOL_VERSION);

  // An older machine, including one from before it reported a version.
  machine("older", PROTOCOL_VERSION - 1);
  const older = await run("older");
  expect(older).toBeInstanceOf(CommandError);
  expect((older as CommandError).message).toBe(
    "MacBook 上的 remote-agent 版本较旧，请在电脑上更新后再试",
  );
  // Never sent, so a later attempt starts afresh.
  expect(uncertain(older)).toBe(false);

  // A newer machine: it is this page that is behind.
  machine("newer", PROTOCOL_VERSION + 1);
  const newer = await run("newer");
  expect(newer).toBeInstanceOf(CommandError);
  expect((newer as CommandError).message).toBe(
    "这个网页版本较旧，请刷新或更新后再试",
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
    (r as any).api = api;
    r.conn = "online";
    await r.loadPrefs();
  }
  await a.changePrefs("pin", "/synthetic/foo");
  await Promise.all([
    a.changePrefs("hide", "/synthetic/blog"),
    b.changePrefs("pin", "/synthetic/billing"),
  ]);
  for (const r of [a, b]) {
    await r.loadPrefs();
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
  const { PayloadKind } = await import("./crypto");
  const { ImagePayloadSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { ImageFormat } = await import("./gen/remoteagent/v1/event_pb");
  const { create } = await import("@bufbuild/protobuf");
  const r = await probe();
  r.sessions.set("s1", { ...r.sessions.get("s")!, id: "s1" });
  (r as any).sessionSeeds.set("s1", r.seed);
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
  const { sealMessage, importSessionKey } = await import("./crypto");
  const oldImage = await sealMessage(
    await importSessionKey(r.seed),
    {
      kind: PayloadKind.IMAGE,
      machineId: r.machine,
      sessionId: "s1",
      requestId: "legacy",
    },
    ImagePayloadSchema,
    create(ImagePayloadSchema, {
      format: ImageFormat.PNG,
      width: 2,
      height: 1,
      data: png,
    }),
  );
  const store: Record<string, unknown> = {
    legacy: { image: oldImage },
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
  const old = await r.getImage("s1", "legacy");
  expect(old.kind === "ok" && old.unverifiedLegacy).toBe(true);
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
  const r = DemoRemote.create(new URLSearchParams());
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
          throw new CommandError("unknown", "连接超时");
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

it("lets go of a sent message's local image copies once the CLI records it", async () => {
  const { DemoRemote } = await import("./dev/demo");
  const r = DemoRemote.create(new URLSearchParams());
  r.start();
  const blob = new Blob([new Uint8Array(10)]);
  const id = r.send("s-idle", "看图", ["img-1"], [blob]);
  const failedId = r.send("s-idle", "另一条", ["img-2"], [blob]);
  expect(r.outbox("s-idle").find((o) => o.sendId === id)?.thumbs?.length).toBe(
    1,
  );
  r.recorded("s-idle", [id]);
  expect(
    r.outbox("s-idle").find((o) => o.sendId === id)?.thumbs,
  ).toBeUndefined();
  // Not yet recorded: kept for putting it back into the input.
  expect(
    r.outbox("s-idle").find((o) => o.sendId === failedId)?.thumbs?.length,
  ).toBe(1);
});

it("gives up on a stream that never opens, and reconnects at once when the network returns", async () => {
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
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    r.start();
    await vi.waitFor(() => expect(sockets.length).toBe(1));
    // The handshake hangs: nothing closes it until the client does.
    vi.advanceTimersByTime(13_000);
    expect(sockets[0].closed).toBe(false);
    vi.advanceTimersByTime(2_000);
    expect(sockets[0].closed).toBe(true);
    expect(r.conn).toBe("offline");
    // Waiting out the backoff; the network coming back skips the wait.
    expect(sockets.length).toBe(1);
    r.reconnectNow();
    vi.useRealTimers();
    await vi.waitFor(() => expect(sockets.length).toBe(2));
    // While a connection is being made, asking again changes nothing.
    r.reconnectNow();
    await new Promise((done) => setTimeout(done, 50));
    expect(sockets.length).toBe(2);
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
    return new Response(new Uint8Array(), {
      headers: { "Content-Type": "application/proto" },
    });
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
      return new Kept(account, {} as never, cache);
    }
    warmed() {
      return (this as unknown as { warm(): Promise<void> }).warm();
    }
  }
  const identity = await (await probe()).security.localIdentity();
  const first = Kept.open();
  vi.spyOn(first.security, "localIdentity").mockResolvedValue(identity);
  vi.spyOn(first.security, "historicalMachines").mockResolvedValue(new Set());
  (first as unknown as { seed(): void }).seed(); // the demo's machines, online
  expect(first.cachedModels("mac", Agent.CODEX)).toBeUndefined();
  const models = await first.listModels("mac", Agent.CODEX);
  await first.settings("mac");
  await vi.waitFor(() => expect(kept.has("info")).toBe(true));

  // The next visit knows them before any machine answers.
  const next = Kept.open();
  vi.spyOn(next.security, "localIdentity").mockResolvedValue(identity);
  vi.spyOn(next.security, "historicalMachines").mockResolvedValue(new Set());
  await next.warmed();
  expect(
    next.cachedModels("mac", Agent.CODEX)?.models.map((m) => m.displayName),
  ).toEqual(models.models.map((m) => m.displayName));
  expect(next.cachedModels("mac", Agent.CLAUDE)).toBeUndefined();
  expect(next.cachedSettings("mac")).toEqual(first.cachedSettings("mac"));

  // Another account's or device's values are not taken.
  const other = new (class extends DemoRemote {
    constructor() {
      super({ ...account, deviceId: "someone-else" }, {} as never, cache);
    }
  })();
  vi.spyOn(other.security, "localIdentity").mockResolvedValue(identity);
  await (other as unknown as { warm(): Promise<void> }).warm();
  expect(other.cachedModels("mac", Agent.CODEX)).toBeUndefined();
});

it("keeps an unauthenticated command result unknown and retries its slot", async () => {
  const { Attempt } = await import("./attempt");
  const { sealMessage, PayloadKind } = await import("./crypto");
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
      const result = await sealMessage(
        r.legacy.command,
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
  const attempt = new Attempt<null>();
  for (let i = 0; i < 2; i++) {
    const { slot } = attempt.next(null);
    const error = await r
      .machineCommand(
        r.machine,
        { case: "createSession", value: { prompt: "task" } },
        slot,
      )
      .catch((e) => e);
    expect(uncertain(error)).toBe(true);
    attempt.settle(error);
    expect(attempt.frozen).toBe(true);
  }
  expect(ids[1]).toBe(ids[0]);
});

it("reports an unreadable prefs object and allows a fresh read retry", async () => {
  const r = await probe();
  vi.spyOn(console, "warn").mockImplementation(() => undefined);
  let fail = true;
  (r as unknown as { api: object }).api = {
    getAccountPrefs: async () => {
      if (fail) {
        fail = false;
        throw new Error("invalid synthetic prefs reply");
      }
      return {};
    },
  };
  await r.loadPrefs();
  expect(r.prefs.status).toBe("error");
  expect(r.prefs.error).not.toBe("");
  expect(r.prefs.snap).toBeUndefined();
  await r.loadPrefs();
  expect(r.prefs.status).toBe("ready");
  expect(r.prefs.error).toBe("");
});

it("removes recorded text and image outbox entries even before the RPC settles", async () => {
  const { DemoRemote } = await import("./dev/demo");
  const r = DemoRemote.create(new URLSearchParams());
  let finish!: () => void;
  r.command = () =>
    new Promise((resolve) => {
      finish = () => resolve({ kind: { case: undefined } } as any);
    });
  const id = r.send("s-idle", "text");
  r.recorded("s-idle", [id]);
  expect(r.outbox("s-idle")).toEqual([]);
  finish();
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(r.outbox("s-idle")).toEqual([]);
  expect(
    (r as unknown as { outboxes: Map<string, unknown> }).outboxes.has("s-idle"),
  ).toBe(false);
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
  await r.loadDevices(false);
  expect(r.devices.get(r.machine)).toBeTruthy();
  expect(r.sessions.get("s")).toBeTruthy();
  await r.world.fixture.remove([r.machine]);
  // The relay withholds every removal push; the 60 s head pull must be enough.
  (r as unknown as { conn: string }).conn = "online";
  await (r as unknown as { syncSecurity(): Promise<void> }).syncSecurity();
  expect(r.devices.get(r.machine)).toBeUndefined();
  expect(r.sessions.get("s")).toBeTruthy();
  expect(r.machines.get(r.machine)?.online).toBe(false);
  expect(
    (
      r as unknown as { trustedMembers: Map<string, unknown> }
    ).trustedMembers.has(r.machine),
  ).toBe(false);
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
  await (
    r as unknown as { putSession(row: unknown): Promise<void> }
  ).putSession(row);
  expect(r.sessions.get("s")?.checkpoint?.running).toBe(true);
  expect(r.sessions.get("s")?.checkpoint?.session?.lastSeq).toBe(900n);
  expect(r.sessions.get("s")?.checkpointStatus).toBe("ready");
  expect(
    (r as unknown as { buffers: Map<string, unknown> }).buffers.has("s"),
  ).toBe(false);
});

it("installs the lease forwarded by the Remote state-page adapter after a head change", async () => {
  const r = await probe();
  const other = await r.addClient();
  await r.world.fixture.remove([other.account.deviceId]);
  // Use Remote's constructor adapter, not the fixture's direct SDK fetcher.
  const security = (
    r as unknown as {
      security: import("./device-security-client").DeviceSecurityClient;
    }
  ).security;
  await security.sync();
  await expect(
    security.seal(
      {
        purpose: 5,
        kind: 5,
        compression: 0,
        session: "",
        seq: 0n,
        machine: r.machine,
        request: "adapter-request-123",
        deadline: BigInt(Date.now() + 60000),
      },
      new Uint8Array(),
    ),
  ).resolves.toBeDefined();
});

it("retries authorized actions with their original observation after completion", async () => {
  const r = await probe();
  const { LiveSnapshotSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { EventSchema, ApprovalRequestedSchema, QuestionAskedSchema } =
    await import("./gen/remoteagent/v1/event_pb");
  const internal = r as unknown as {
    liveSnapshots: Map<
      string,
      ReturnType<typeof create<typeof LiveSnapshotSchema>>
    >;
    refreshLiveSnapshot(sid: string): Promise<void>;
    security: { sync(): Promise<unknown> };
    api: object;
  };
  const sent: Envelope[] = [];
  internal.api = {
    forward: async (req: { command: Envelope }) => {
      sent.push(req.command);
      throw new ConnectError("unsigned refusal", Code.Unavailable);
    },
  };
  internal.refreshLiveSnapshot = async () => {};
  // A normal session row is enough; the live observation below comes from its daemon.

  for (const kind of [
    "resolveApproval",
    "answerQuestion",
    "answerPlan",
  ] as const) {
    internal.liveSnapshots.set(
      "s",
      create(LiveSnapshotSchema, {
        actions: [
          create(EventSchema, {
            seq: 7n,
            kind: {
              case: "approvalRequested",
              value: create(ApprovalRequestedSchema, { approvalId: "a" }),
            },
          }),
          create(EventSchema, {
            seq: 8n,
            kind: {
              case: "questionAsked",
              value: create(QuestionAskedSchema, { questionId: "q" }),
            },
          }),
        ],
        planEventSeq: 9n,
        planScreenHash: new Uint8Array(32).fill(3),
      }),
    );
    const cmd =
      kind === "resolveApproval"
        ? {
            case: kind,
            value: { sessionId: "s", approvalId: "a", allow: true },
          }
        : kind === "answerQuestion"
          ? {
              case: kind,
              value: { sessionId: "s", questionId: "q", answers: [] },
            }
          : { case: kind, value: { sessionId: "s", accept: true } };
    const slot = newSlot();
    await expect(r.command("s", cmd, slot)).rejects.toMatchObject({
      code: "unknown",
    });
    const first = await r.world.fixture.open(r.machine, sent.at(-1)!);
    internal.liveSnapshots.set("s", create(LiveSnapshotSchema));
    const joined = await r.addClient();
    liveFixtures.push(joined);
    await internal.security.sync();
    // Resealing uses the current security state and the original command, not this empty observation.
    await expect(r.command("s", cmd, slot)).rejects.toMatchObject({
      code: "unknown",
    });
    const retry = await r.world.fixture.open(r.machine, sent.at(-1)!);
    expect(retry.header.requestId).toBe(first.header.requestId);
    expect(retry.header.deadlineMs).toBe(first.header.deadlineMs);
    expect(retry.plaintext).toEqual(first.plaintext);
    expect(sent.at(-1)!.securityHeader).not.toEqual(
      sent.at(-2)!.securityHeader,
    );
    // An initial authorization still refuses a completed observation.
    await expect(r.command("s", cmd, newSlot())).rejects.toThrow(
      "当前请求已不可操作",
    );
  }
});
