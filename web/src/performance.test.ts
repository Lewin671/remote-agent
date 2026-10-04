import { afterEach, expect, it, vi } from "vitest";
import { Remote } from "./remote";
import { create } from "@bufbuild/protobuf";
import { CommandResultSchema } from "./gen/remoteagent/v1/remote_pb";

class Socket {
  static all: Socket[] = [];
  static OPEN = 1;
  readyState = 0;
  onopen?: () => void;
  onclose?: (e?: { code: number }) => void;
  onerror?: () => void;
  onmessage?: (m: { data: ArrayBuffer }) => void;
  send = vi.fn();
  constructor(readonly url: string) { Socket.all.push(this); }
  open() { this.readyState = 1; this.onopen?.(); }
  close() { this.readyState = 3; queueMicrotask(() => this.onclose?.()); }
  /** The server closes the stream with a status, as when it refuses the hello. */
  closedBy(code: number) { this.readyState = 3; this.onclose?.({ code }); }
}
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); Socket.all = []; });
async function probe() {
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]) as CryptoKeyPair;
  class Probe extends Remote { constructor() { super({ server: "https://ra.example.com", root: key, rootRaw: new Uint8Array(32), devicePrivate: device.privateKey, deviceId: "d" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key }); } }
  const r = new Probe();
  r.sessions.set("s", { id: "s", machineId: "m", key, metaRev: 0n, lastSeq: 0n, updatedMs: 0n });
  return r;
}
it("does not open a stale main socket when stopped during login", async () => {
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  let finish!: () => void;
  vi.spyOn(r as any, "login").mockImplementation(() => new Promise<void>(resolve => { finish = resolve; }));
  r.start(); r.stop(); finish();
  await Promise.resolve(); await Promise.resolve();
  expect(Socket.all).toHaveLength(0);
  r.start(); finish(); await Promise.resolve(); await Promise.resolve();
  expect(Socket.all).toHaveLength(1);
  r.stop();
});
it("does not start catch-up after a pending refresh completes in background", async () => {
  const r = await probe();
  let finish!: () => void;
  vi.spyOn(r, "refresh").mockImplementation(() => new Promise<void>(resolve => { finish = resolve; }));
  const devices = vi.spyOn(r as any, "loadDevices").mockResolvedValue(undefined);
  const prefs = vi.spyOn(r as any, "loadPrefs").mockResolvedValue(undefined);
  const pending = (r as any).onFrame({ case: "ready", value: {} });
  r.stop(); finish(); await pending;
  expect(devices).not.toHaveBeenCalled(); expect(prefs).not.toHaveBeenCalled();
});
it("pauses history pagination without marking partial history complete", async () => {
  const r = await probe();
  let finish!: (response: object) => void;
  const fetch = vi.fn(() => new Promise<object>(resolve => { finish = resolve; }));
  (r as any).api = { getEvents: fetch };
  r.events("s"); r.stop(); finish({ events: [], more: true });
  await Promise.resolve(); await Promise.resolve();
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(r.history("s").loaded).toBe(false);
});
it("stops pending terminal opens and bounds queued decryption", async () => {
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  r.command = vi.fn(async () => create(CommandResultSchema));
  const closed = vi.fn(); const frames = vi.fn();
  const opening = r.openTerminal("s", frames, closed);
  const socket = Socket.all[0]; socket.open();
  const terminal = await opening;
  // All calls occur before promise jobs execute: these frames must be bounded and discarded.
  for (let i = 0; i < 513; i++) socket.onmessage?.({ data: new ArrayBuffer(0) });
  await Promise.resolve(); await Promise.resolve();
  expect(closed).toHaveBeenCalledTimes(1); expect(frames).not.toHaveBeenCalled();
  expect(socket.readyState).toBe(3);
  terminal.close(); r.stop();
});
it("closes a terminal socket even while its handshake is pending", async () => {
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  const opening = r.openTerminal("s", vi.fn(), vi.fn()).catch(e => e);
  r.stop();
  expect(await opening).toBeInstanceOf(Error);
  expect(Socket.all[0].readyState).toBe(3);
});

it("serializes the main stream and drops a bounded backlog", async () => {
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  vi.spyOn(r as any, "login").mockResolvedValue(undefined);
  let release!: () => void;
  const handler = vi.spyOn(r as any, "onFrame").mockImplementation(() => new Promise<void>((resolve) => { release = resolve; }));
  const { toBinary } = await import("@bufbuild/protobuf");
  const { ServerFrameSchema } = await import("./gen/remoteagent/v1/relay_pb");
  const frame = toBinary(ServerFrameSchema, create(ServerFrameSchema, { kind: { case: "ready", value: {} } })).buffer as ArrayBuffer;
  r.start();
  await vi.waitFor(() => expect(Socket.all).toHaveLength(1));
  const ws = Socket.all[0]; ws.open(); ws.onmessage?.({ data: frame });
  await vi.waitFor(() => expect(handler).toHaveBeenCalledTimes(1));
  for (let i = 0; i < 1024; i++) ws.onmessage?.({ data: frame });
  expect(ws.readyState).toBe(3);
  release();
  await Promise.resolve(); await Promise.resolve();
  expect(handler).toHaveBeenCalledTimes(1);
  r.stop();
});

it("evicts idle histories while preserving mounted and image-confirmation consumers", async () => {
  const r = await probe();
  (r as any).api = { getEvents: vi.fn(async () => ({ events: [], more: false })) };
  const keep = r.retainHistory("s");
  for (let i = 0; i < 12; i++) {
    const id = "idle" + i;
    r.sessions.set(id, { ...r.sessions.get("s")!, id });
    r.events(id);
  }
  await vi.waitFor(() => expect((r as any).buffers.size).toBe(9));
  expect((r as any).buffers.has("s")).toBe(true);
  keep(); keep(); // Releasing twice must not unpin another consumer.
  await vi.waitFor(() => expect((r as any).buffers.size).toBe(8));
  const release = r.retainHistory("s");
  await vi.waitFor(() => expect(r.loaded("s")).toBe(true));
  (r as any).outboxes.set("s", [{ sendId: "image", status: "sent", thumbs: [new Blob(["image"])] }]);
  release();
  const b = (r as any).buffers.get("s"); b.usedAt = 0;
  (r as any).trimHistory();
  expect((r as any).buffers.has("s")).toBe(true);
  r.recorded("s", ["image"]);
  (r as any).trimHistory();
  expect((r as any).buffers.has("s")).toBe(false);
  clearTimeout((r as any).historyTrim); r.stop();
});

it("does not put a late fetch into a reclaimed history", async () => {
  const r = await probe();
  let finish!: (r: object) => void;
  const api = vi.fn(() => new Promise<object>((resolve) => { finish = resolve; }));
  (r as any).api = { getEvents: api };
  r.events("s");
  const old = (r as any).buffers.get("s"); old.usedAt = 0;
  (r as any).trimHistory();
  finish({ events: [], more: false });
  await Promise.resolve(); await Promise.resolve();
  expect((r as any).buffers.has("s")).toBe(false);
  expect(old.loaded).toBe(false);
  clearTimeout((r as any).historyTrim); r.stop();
});

it("counts terminal output until the renderer has consumed it", async () => {
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  const command = vi.fn(async () => create(CommandResultSchema)); r.command = command;
  let consumed!: () => void;
  const renderer = vi.fn(() => new Promise<void>((resolve) => { consumed = resolve; }));
  const closed = vi.fn();
  const opening = r.openTerminal("s", renderer, closed);
  const ws = Socket.all[0]; ws.open(); const conn = await opening;
  const { sealMessage } = await import("./crypto");
  const { toBinary } = await import("@bufbuild/protobuf");
  const { EnvelopeSchema, PayloadKind } = await import("./gen/remoteagent/v1/envelope_pb");
  const { TerminalFrameSchema } = await import("./gen/remoteagent/v1/remote_pb");
  const streamId = (command.mock.calls as any)[0][1].value.streamId;
  const env = await sealMessage(r.sessions.get("s")!.key, { kind: PayloadKind.TERMINAL_OUTPUT, machineId: "m", sessionId: "s", requestId: streamId, seq: 1n }, TerminalFrameSchema, create(TerminalFrameSchema, { kind: { case: "output", value: new Uint8Array([65]) } }));
  ws.onmessage?.({ data: toBinary(EnvelopeSchema, env).buffer as ArrayBuffer });
  await vi.waitFor(() => expect(renderer).toHaveBeenCalledTimes(1));
  for (let i = 0; i < 512; i++) ws.onmessage?.({ data: new ArrayBuffer(0) });
  expect(ws.readyState).toBe(3);
  expect(closed).toHaveBeenCalledTimes(1);
  consumed(); conn.close(); r.stop();
});

it("reclaims failed and canceled image histories without deleting retry thumbnails", async () => {
  const r = await probe();
  (r as any).api = { getEvents: vi.fn(async () => ({ events: [], more: false })) };
  const release = r.retainHistory("s");
  await vi.waitFor(() => expect(r.loaded("s")).toBe(true));
  const thumb = new Blob(["retry image"]);
  (r as any).outboxes.set("s", [{ sendId: "failed", status: "failed", thumbs: [thumb] }]);
  (r as any).buffers.get("s").usedAt = 0;
  release();
  (r as any).buffers.get("s").usedAt = 0;
  (r as any).trimHistory();
  expect((r as any).buffers.has("s")).toBe(false);
  expect(r.outbox("s")[0].thumbs![0]).toBe(thumb);
  r.events("s");
  await vi.waitFor(() => expect(r.loaded("s")).toBe(true));
  const { InputStatus } = await import("./gen/remoteagent/v1/event_pb");
  (r as any).outboxes.set("s", [{ sendId: "canceled", status: "sent", thumbs: [thumb] }]);
  const b = (r as any).buffers.get("s"); b.usedAt = 0; b.inputStatuses = new Map([["canceled", InputStatus.CANCELED]]);
  (r as any).trimHistory();
  expect((r as any).buffers.has("s")).toBe(false);
  expect(r.outbox("s")[0].thumbs![0]).toBe(thumb);
  r.stop();
});

async function sealedEvents(r: Remote, n: number) {
  const { sealMessage } = await import("./crypto");
  const { PayloadKind } = await import("./gen/remoteagent/v1/envelope_pb");
  const { EventSchema } = await import("./gen/remoteagent/v1/event_pb");
  const out = [];
  for (let seq = 1n; seq <= BigInt(n); seq++)
    out.push({ seq, envelope: await sealMessage(r.sessions.get("s")!.key, { kind: PayloadKind.EVENT, machineId: "m", sessionId: "s", seq }, EventSchema, create(EventSchema, { seq, sessionId: "s", kind: { case: "agentMessage", value: { text: "m" + seq } } })) });
  return out;
}

it("opens a session from the cached history and downloads only what is new", async () => {
  const r = await probe();
  const all = await sealedEvents(r, 3);
  const cache = { read: vi.fn(async () => all.slice(0, 2)), append: vi.fn(async () => undefined), drop: vi.fn(async () => undefined), keep: vi.fn(async () => undefined), get: vi.fn(async () => undefined), set: vi.fn(async () => undefined) };
  (r as any).cache = cache;
  r.sessions.get("s")!.lastSeq = 3n;
  const getEvents = vi.fn(async ({ afterSeq }: { afterSeq: bigint }) => ({ events: all.filter((e) => e.seq > afterSeq), more: false }));
  (r as any).api = { getEvents };
  r.events("s");
  await vi.waitFor(() => expect(r.loaded("s")).toBe(true));
  expect(getEvents).toHaveBeenCalledTimes(1);
  expect(getEvents.mock.calls[0][0].afterSeq).toBe(2n);
  expect(r.events("s").map((e) => e.seq)).toEqual([1n, 2n, 3n]);
  // Only the downloaded event is written back.
  expect(cache.append).toHaveBeenCalledTimes(1);
  expect((cache.append.mock.calls as any)[0][1].map((e: { seq: bigint }) => e.seq)).toEqual([3n]);
  clearTimeout((r as any).historyTrim); r.stop();
});

it("drops a cached history that is not this session's", async () => {
  const r = await probe();
  const all = await sealedEvents(r, 3);
  const getEvents = vi.fn(async ({ afterSeq }: { afterSeq: bigint }) => ({ events: all.filter((e) => e.seq > afterSeq), more: false }));
  (r as any).api = { getEvents };
  // Longer than what the server has.
  r.sessions.get("s")!.lastSeq = 2n;
  const long = { read: vi.fn(async () => all), append: vi.fn(async () => undefined), drop: vi.fn(async () => undefined), keep: vi.fn(async () => undefined), get: vi.fn(async () => undefined), set: vi.fn(async () => undefined) };
  (r as any).cache = long;
  r.events("s");
  await vi.waitFor(() => expect(r.loaded("s")).toBe(true));
  expect(long.drop).toHaveBeenCalledWith("s");
  expect(getEvents.mock.calls[0][0].afterSeq).toBe(0n);
  // Does not open where it is: sealed for another seq.
  (r as any).buffers.delete("s");
  r.sessions.get("s")!.lastSeq = 3n;
  const moved = { ...long, read: vi.fn(async () => [{ seq: 1n, envelope: all[1].envelope }]), drop: vi.fn(async () => undefined) };
  (r as any).cache = moved;
  r.events("s");
  await vi.waitFor(() => expect(r.loaded("s")).toBe(true));
  expect(moved.drop).toHaveBeenCalledWith("s");
  expect(r.history("s").broken).toBe(false);
  expect(r.events("s").map((e) => e.seq)).toEqual([1n, 2n, 3n]);
  clearTimeout((r as any).historyTrim); r.stop();
});

it("starts from what the browser kept: the token, and the last listing", async () => {
  vi.stubGlobal("WebSocket", Socket);
  const r = await probe();
  r.sessions.clear();
  const { sealMessage, seal } = await import("./crypto");
  const { PayloadKind } = await import("./gen/remoteagent/v1/envelope_pb");
  const { SessionSchema } = await import("./gen/remoteagent/v1/local_pb");
  const { ListMachinesResponseSchema, ListRemoteSessionsResponseSchema } = await import("./gen/remoteagent/v1/relay_pb");
  const { toBinary } = await import("@bufbuild/protobuf");
  const key = (r as any).keys.wrap as CryptoKey;
  const raw = crypto.getRandomValues(new Uint8Array(32));
  const skey = await crypto.subtle.importKey("raw", raw, "AES-GCM", false, ["encrypt", "decrypt"]);
  const sessions = toBinary(ListRemoteSessionsResponseSchema, create(ListRemoteSessionsResponseSchema, { sessions: [{
    sessionId: "kept", machineId: "m", lastSeq: 4n,
    key: await seal(key, { kind: PayloadKind.SESSION_KEY, machineId: "m", sessionId: "kept" }, raw),
    meta: await sealMessage(skey, { kind: PayloadKind.SESSION_META, machineId: "m", sessionId: "kept", seq: 1n }, SessionSchema, create(SessionSchema, { id: "kept", title: "from last time" })),
  }] }));
  const mine = { deviceId: "d", server: "https://ra.example.com" };
  const kept: Record<string, unknown> = {
    token: { ...mine, token: "kept-token", expiresMs: Date.now() + 10 * 86_400_000 },
    machines: { ...mine, bytes: toBinary(ListMachinesResponseSchema, create(ListMachinesResponseSchema, { machines: [{ id: "m", online: true }] })) },
    sessions: { ...mine, bytes: sessions },
  };
  (r as any).cache = { read: vi.fn(async () => []), append: vi.fn(), drop: vi.fn(), keep: vi.fn(async () => undefined), get: vi.fn(async (k: string) => kept[k]), set: vi.fn(async () => undefined) };
  const login = vi.spyOn(r as any, "login").mockResolvedValue(undefined);
  let notified = 0;
  r.subscribe(() => notified++);
  r.start();
  await vi.waitFor(() => expect(Socket.all).toHaveLength(1));
  // Shown before the server has said anything, without logging in.
  expect(login).not.toHaveBeenCalled();
  expect(r.sessions.get("kept")?.info?.title).toBe("from last time");
  expect(r.machines.get("m")?.online).toBe(true);
  expect(r.listed).toBe(false);
  expect(r.authHeader()).toBe("Bearer kept-token");
  // A handshake the network dropped keeps the token.
  Socket.all[0].close();
  await vi.waitFor(() => expect(Socket.all).toHaveLength(2), { timeout: 3000 });
  expect(r.authHeader()).toBe("Bearer kept-token");
  expect(login).not.toHaveBeenCalled();
  // The server turning the token away is followed by a login.
  Socket.all[1].closedBy(1008);
  expect(r.authHeader()).toBe("Bearer ");
  r.stop();
  // Another account's leftovers are not used.
  const other = await probe();
  (other as any).cache = { ...(r as any).cache, get: vi.fn(async (k: string) => ({ ...(kept[k] as object), deviceId: "someone else" })) };
  const otherLogin = vi.spyOn(other as any, "login").mockResolvedValue(undefined);
  other.sessions.clear();
  other.start();
  await vi.waitFor(() => expect(otherLogin).toHaveBeenCalled());
  expect(other.sessions.size).toBe(0);
  other.stop();
});
