import { fromBinary } from "@bufbuild/protobuf";
import { envelopeSigned } from "./crypto";
import { HeaderSchema, type Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { Code, ConnectError } from "@connectrpc/connect";
import { expect, it, vi } from "vitest";
import { CommandError, COMMAND_TTL_MS, newSlot, Remote, uncertain, type CommandKind, type RequestSlot } from "./remote";

it("reuses a request slot only when the outcome is unknown", () => {
  // Timed out or cut off: the command may have run; a retry must reuse
  // the slot so the daemon answers from its log.
  expect(uncertain(new CommandError("unknown", "连接超时"))).toBe(true);
  expect(uncertain(new CommandError("aborted", "outcome unknown"))).toBe(true);
  expect(uncertain(new ConnectError("reset", Code.Internal))).toBe(true);
  // A definite answer: the next attempt starts afresh.
  expect(uncertain(new CommandError("unavailable", "电脑不在线"))).toBe(false);
  expect(uncertain(new CommandError("failed_precondition", "cwd is not a directory"))).toBe(false);
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
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  class Probe extends Remote {
    constructor() {
      super({ server: "https://ra.example.com", root: key, rootRaw: new Uint8Array(32), devicePrivate: device.privateKey, deviceId: "d" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key });
    }
  }
  const r = new Probe();
  const replies = [new ConnectError("no reply from the machine in time", Code.DeadlineExceeded), new ConnectError("machine is offline", Code.Unavailable)];
  (r as unknown as { api: object }).api = {
    forward: async () => {
      throw replies.shift();
    },
  };
  // Between the two tries forward waits a second.
  const e = await r.machineCommand("m", { case: "listModels", value: {} }).catch((x) => x);
  expect(e).toBeInstanceOf(CommandError);
  expect((e as CommandError).code).toBe("unknown");
}, 10_000);

it("keeps a message of unknown outcome on its request when the machine is then offline", async () => {
  const { DemoRemote } = await import("./dev/demo");
  const r = DemoRemote.create(new URLSearchParams());
  const replies = [new CommandError("unknown", "连接超时"), new CommandError("unavailable", "电脑不在线，指令没有发出")];
  const seen: string[] = [];
  r.command = async (_s: string, _c: unknown, slot?: RequestSlot) => {
    seen.push(slot!.requestId);
    throw replies.shift() ?? new CommandError("unavailable", "电脑不在线，指令没有发出");
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
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  class Probe extends Remote {
    constructor() {
      super({ server: "https://ra.example.com", root: key, rootRaw: new Uint8Array(32), devicePrivate: device.privateKey, deviceId: "dev1" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key });
    }
  }
  const r = new Probe();
  let sent: Envelope | undefined;
  (r as unknown as { api: object }).api = {
    forward: async (req: { command: Envelope }) => {
      sent = req.command;
      throw new ConnectError("machine is offline", Code.Unavailable);
    },
  };
  await r.machineCommand("m", { case: "listModels", value: {} }).catch(() => undefined);
  expect(sent).toBeTruthy();
  expect(fromBinary(HeaderSchema, sent!.header).sender).toBe("dev1");
  expect(sent!.signature.length).toBe(64);
  expect(await crypto.subtle.verify("Ed25519", device.publicKey, sent!.signature as Uint8Array<ArrayBuffer>, envelopeSigned(sent!))).toBe(true);
});

it("sends its protocol version with a command, and sends none to a machine of another version", async () => {
  const { create } = await import("@bufbuild/protobuf");
  const { openMessage, PayloadKind } = await import("./crypto");
  const { CommandSchema, MachineMetaSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { PROTOCOL_VERSION } = await import("./protocol");
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  class Probe extends Remote {
    constructor() {
      super({ server: "https://ra.example.com", root: key, rootRaw: new Uint8Array(32), devicePrivate: device.privateKey, deviceId: "dev1" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key });
    }
  }
  const r = new Probe();
  const sent: Envelope[] = [];
  (r as unknown as { api: object }).api = {
    forward: async (req: { command: Envelope }) => {
      sent.push(req.command);
      throw new ConnectError("machine is offline", Code.Unavailable);
    },
  };
  const machine = (id: string, protocolVersion: number) =>
    r.machines.set(id, { id, online: true, lastSeenMs: 0n, metaRev: 1n, meta: create(MachineMetaSchema, { hostname: "MacBook.local", protocolVersion }) });
  const run = (id: string) => r.machineCommand(id, { case: "listModels", value: {} }).then(() => undefined, (e: unknown) => e);

  // The same version, and a machine whose metadata has not opened yet.
  machine("same", PROTOCOL_VERSION);
  await run("same");
  await run("unknown");
  expect(sent).toHaveLength(2);
  for (const env of sent) expect((await openMessage(key, env, { kind: PayloadKind.COMMAND }, CommandSchema)).msg.protocolVersion).toBe(PROTOCOL_VERSION);

  // An older machine, including one from before it reported a version.
  machine("older", PROTOCOL_VERSION - 1);
  const older = await run("older");
  expect(older).toBeInstanceOf(CommandError);
  expect((older as CommandError).message).toBe("MacBook 上的 remote-agent 版本较旧，请在电脑上更新后再试");
  // Never sent, so a later attempt starts afresh.
  expect(uncertain(older)).toBe(false);

  // A newer machine: it is this page that is behind.
  machine("newer", PROTOCOL_VERSION + 1);
  const newer = await run("newer");
  expect(newer).toBeInstanceOf(CommandError);
  expect((newer as CommandError).message).toBe("这个网页版本较旧，请刷新或更新后再试");
  expect(sent).toHaveLength(2);
});

it("replays a project pin on another client's sealed copy after a conflict", async () => {
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  const keys = { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key };
  class Probe extends Remote {
    constructor(id: string) {
      super({ server: "https://ra.example.com", root: key, rootRaw: new Uint8Array(32), devicePrivate: device.privateKey, deviceId: id }, keys);
    }
  }
  // One server object; two clients share the account key.
  let stored: { revision: bigint; envelope?: Envelope } = { revision: 0n };
  let before: (() => Promise<void>) | undefined;
  const api = {
    getAccountPrefs: async () => ({ object: { objectId: "project-prefs", revision: stored.revision, envelope: stored.envelope } }),
    putAccountPrefs: async (req: { expectedRevision: bigint; envelope: Envelope }) => {
      const b = before;
      before = undefined;
      await b?.();
      if (req.expectedRevision !== stored.revision) return { applied: false, object: { objectId: "project-prefs", ...stored } };
      stored = { revision: req.expectedRevision + 1n, envelope: req.envelope };
      return { applied: true, object: { objectId: "project-prefs", ...stored } };
    },
  };
  const a = new Probe("a");
  const b = new Probe("b");
  for (const r of [a, b]) {
    (r as unknown as { api: object }).api = api;
    r.conn = "online";
    await r.loadPrefs();
  }
  await a.changePrefs("pin", "/Users/me/Code/foo");
  await b.loadPrefs();
  // a hides blog from its copy while b pins billing in between.
  before = async () => {
    await b.changePrefs("pin", "/Users/me/Code/billing");
  };
  expect(await a.changePrefs("hide", "/Users/me/Code/blog")).toBe(true);
  await b.loadPrefs();
  for (const r of [a, b]) {
    const p = r.projectPrefs();
    expect(p.pinned.map((x) => x.path)).toEqual(["/Users/me/Code/foo", "/Users/me/Code/billing"]);
    expect(p.hidden.map((x) => x.path)).toEqual(["/Users/me/Code/blog"]);
  }
  expect(stored.revision).toBe(3n);
  // The server only ever held ciphertext.
  expect(new TextDecoder().decode(stored.envelope!.ciphertext)).not.toContain("billing");
});

it("shows a display copy only where it belongs and in the format it claims", async () => {
  const { sealMessage, PayloadKind } = await import("./crypto");
  const { ImagePayloadSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const { ImageFormat } = await import("./gen/remoteagent/v1/event_pb");
  const { create } = await import("@bufbuild/protobuf");
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  class Probe extends Remote {
    constructor() {
      super({ server: "https://ra.example.com", root: key, rootRaw: new Uint8Array(32), devicePrivate: device.privateKey, deviceId: "d" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key });
    }
  }
  const r = new Probe();
  r.sessions.set("s1", { id: "s1", machineId: "m1", key, metaRev: 0n, lastSeq: 0n, updatedMs: 0n, endedMs: 0n });
  const png = Uint8Array.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3]);
  const seal = (imageId: string, format: number, data: Uint8Array) =>
    sealMessage(key, { kind: PayloadKind.IMAGE, machineId: "m1", sessionId: "s1", requestId: imageId }, ImagePayloadSchema, create(ImagePayloadSchema, { format, width: 2, height: 1, data }));
  const store: Record<string, unknown> = {
    good: { image: await seal("good", ImageFormat.PNG, png) },
    liar: { image: await seal("liar", ImageFormat.PNG, new TextEncoder().encode("<svg onload=x>")) },
    moved: { image: await seal("other", ImageFormat.PNG, png) },
    old: { expired: true },
  };
  (r as unknown as { api: object }).api = {
    getImage: async (req: { imageId: string }) => {
      if (!(req.imageId in store)) throw new ConnectError("no such image", Code.NotFound);
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
  (r as unknown as { api: object }).api = { getImage: async () => { throw new ConnectError("offline", Code.Unavailable); } };
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
    const { CommandResultSchema } = await import("./gen/remoteagent/v1/remote_pb");
    const status = (extra = {}) => ({ status: { transferId: "t", state: 1, persistedBytes: persisted, totalBytes: BigInt(bytes.length), maxChunkBytes: 1_000_000, ...extra } });
    switch (cmd?.case) {
      case "beginImageUpload":
        calls.push(`begin ${cmd.value.totalBytes} ${cmd.value.sha256?.length}`);
        return create(CommandResultSchema, { kind: { case: "beginImageUpload", value: status() } });
      case "putImageChunk": {
        calls.push(`put ${cmd.value.offset} ${cmd.value.data?.length}`);
        expect(cmd.value.offset).toBe(persisted);
        expect([...cmd.value.data!.slice(0, 3)]).toEqual([...bytes.slice(Number(cmd.value.offset), Number(cmd.value.offset) + 3)]);
        persisted += BigInt(cmd.value.data!.length);
        // The reply to the second chunk is lost, though it was stored.
        if (persisted > 800_000n && lost) {
          lost = false;
          throw new CommandError("unknown", "连接超时");
        }
        return create(CommandResultSchema, { kind: { case: "putImageChunk", value: status() } });
      }
      case "getImageUploadStatus":
        calls.push("status");
        return create(CommandResultSchema, { kind: { case: "getImageUploadStatus", value: status() } });
      case "completeImageUpload":
        calls.push("complete");
        return create(CommandResultSchema, { kind: { case: "completeImageUpload", value: status({ state: 2, imageId: "img-1" }) } });
    }
    throw new Error("unexpected " + cmd?.case);
  };
  const progress: number[] = [];
  const id = await Remote.prototype.uploadImage.call(r, "mac", { bytes, format: "jpeg" }, (d: number) => progress.push(d));
  expect(id).toBe("img-1");
  // Chunks of at most 768 KiB, each starting where the machine is.
  expect(calls).toEqual([`begin 2000000 32`, "put 0 786432", "put 786432 786432", "status", "put 1572864 427136", "complete"]);
  expect(progress.at(-1)).toBe(2_000_000);
});

it("lets go of a sent message's local image copies once the CLI records it", async () => {
  const { DemoRemote } = await import("./dev/demo");
  const r = DemoRemote.create(new URLSearchParams());
  r.start();
  const blob = new Blob([new Uint8Array(10)]);
  const id = r.send("s-idle", "看图", ["img-1"], [blob]);
  const failedId = r.send("s-idle", "另一条", ["img-2"], [blob]);
  expect(r.outbox("s-idle").find((o) => o.sendId === id)?.thumbs?.length).toBe(1);
  r.recorded("s-idle", [id]);
  expect(r.outbox("s-idle").find((o) => o.sendId === id)?.thumbs).toBeUndefined();
  // Not yet recorded: kept for putting it back into the input.
  expect(r.outbox("s-idle").find((o) => o.sendId === failedId)?.thumbs?.length).toBe(1);
});

it("gives up on a stream that never opens, and reconnects at once when the network returns", async () => {
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  class Probe extends Remote {
    constructor() {
      super({ server: "https://ra.example.com", root: key, rootRaw: new Uint8Array(32), devicePrivate: device.privateKey, deviceId: "d" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key });
    }
  }
  const sockets: { closed: boolean; onclose?: (e: { code: number }) => void }[] = [];
  vi.stubGlobal("WebSocket", class {
    closed = false;
    onclose?: (e: { code: number }) => void;
    constructor() { sockets.push(this); }
    close() {
      if (this.closed) return;
      this.closed = true;
      this.onclose?.({ code: 1006 });
    }
  });
  vi.stubGlobal("fetch", async () => new Response(new Uint8Array(), { headers: { "Content-Type": "application/proto" } }));
  const r = new Probe();
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
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  class Probe extends Remote {
    constructor() {
      super({ server: "https://ra.example.com", root: key, rootRaw: new Uint8Array(32), devicePrivate: device.privateKey, deviceId: "d" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key });
    }
  }
  const calls: string[] = [];
  let denied = 1;
  vi.stubGlobal("WebSocket", class { close() {} });
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const name = String(input instanceof Request ? input.url : input).split("/").pop()!;
    calls.push(name);
    if (name === "GetChallenge") return new Response(new Uint8Array(), { headers: { "Content-Type": "application/proto" } });
    if (denied-- > 0) return new Response(JSON.stringify({ code: "permission_denied", message: "login failed" }), { status: 403, headers: { "Content-Type": "application/json" } });
    return new Response(new Uint8Array(), { headers: { "Content-Type": "application/proto" } });
  });
  try {
    // One denial alone is a challenge the server forgot: the next login works.
    const r = new Probe();
    r.start();
    await vi.waitFor(() => expect(calls.filter((c) => c === "Login")).toHaveLength(2));
    expect(r.removed).toBe(false);
    r.stop();

    // Denied a fresh challenge too: removed, and it stops asking.
    denied = 2;
    calls.length = 0;
    const gone = new Probe();
    gone.start();
    await vi.waitFor(() => expect(gone.removed).toBe(true));
    expect(gone.conn).toBe("offline");
    gone.start();
    await new Promise((r) => setTimeout(r, 50));
    expect(calls.filter((c) => c === "Login")).toHaveLength(2);

    // Asked to try again after joining was restored on the server.
    gone.retryLogin();
    await vi.waitFor(() => expect(calls.filter((c) => c === "Login")).toHaveLength(3));
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
  const cache = { read: async () => [], append: async () => undefined, drop: async () => undefined, keep: async () => undefined, get: async (k: string) => kept.get(k), set: async (k: string, v: unknown) => void kept.set(k, v) };
  const account = { server: "https://ra.example.com", root: {} as CryptoKey, rootRaw: new Uint8Array(32), devicePrivate: {} as CryptoKey, deviceId: "me" };
  class Kept extends DemoRemote {
    static open() {
      return new Kept(account, {} as never, cache);
    }
    warmed() {
      return (this as unknown as { warm(): Promise<void> }).warm();
    }
  }
  const first = Kept.open();
  (first as unknown as { seed(): void }).seed(); // the demo's machines, online
  expect(first.cachedModels("mac", Agent.CODEX)).toBeUndefined();
  const models = await first.listModels("mac", Agent.CODEX);
  await first.settings("mac");
  await vi.waitFor(() => expect(kept.has("info")).toBe(true));

  // The next visit knows them before any machine answers.
  const next = Kept.open();
  await next.warmed();
  expect(next.cachedModels("mac", Agent.CODEX)?.models.map((m) => m.displayName)).toEqual(models.models.map((m) => m.displayName));
  expect(next.cachedModels("mac", Agent.CLAUDE)).toBeUndefined();
  expect(next.cachedSettings("mac")).toEqual(first.cachedSettings("mac"));

  // Another account's or device's values are not taken.
  const other = new (class extends DemoRemote {
    constructor() {
      super({ ...account, deviceId: "someone-else" }, {} as never, cache);
    }
  })();
  await (other as unknown as { warm(): Promise<void> }).warm();
  expect(other.cachedModels("mac", Agent.CODEX)).toBeUndefined();
});
