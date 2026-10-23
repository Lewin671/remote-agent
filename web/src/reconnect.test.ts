import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { ConnBar } from "./ui";
import { afterEach, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { ServerFrameSchema } from "./gen/remoteagent/v1/relay_pb";
import { Remote } from "./remote";

// Closing a blackholed stream does not have to produce a close event.
class Socket {
  static all: Socket[] = [];
  onopen?: () => void;
  onclose?: (e: { code: number }) => void;
  onmessage?: (m: { data: ArrayBuffer }) => void;
  closed = false;
  send = vi.fn();
  constructor() { Socket.all.push(this); }
  close() { this.closed = true; }
  frame(kind: "ready" | "heartbeat") {
    const bytes = toBinary(ServerFrameSchema, create(ServerFrameSchema, { kind: { case: kind, value: {} } }));
    this.onmessage?.({ data: bytes.buffer as ArrayBuffer });
  }
}
const remotes: Remote[] = [];
afterEach(() => {
  for (const r of remotes.splice(0)) r.stop();
  vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); Socket.all = [];
});
async function probe(mockLogin = true) {
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]) as CryptoKeyPair;
  class Probe extends Remote {
    constructor() { super({ server: "https://ra.example.com", root: key, rootRaw: new Uint8Array(32), devicePrivate: device.privateKey, deviceId: "d" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key }); }
  }
  const r = new Probe(); remotes.push(r);
  vi.stubGlobal("WebSocket", Socket);
  if (mockLogin) vi.spyOn(r as any, "login").mockResolvedValue(undefined);
  vi.spyOn(r.security, "sync").mockResolvedValue({ member: true, states: [{ core: { authorityID: "authority" } }] } as never);
  vi.spyOn(r, "refresh").mockResolvedValue(undefined);
  vi.spyOn(r as any, "loadDevices").mockResolvedValue(undefined);
  vi.spyOn(r as any, "loadPrefs").mockResolvedValue(undefined);
  return r;
}
async function flush() { for (let i = 0; i < 15; i++) await Promise.resolve(); }

it("retries a handshake without waiting for onclose and ignores late frames", async () => {
  const r = await probe(); vi.useFakeTimers();
  r.start(); await flush();
  const old = Socket.all[0];
  await vi.advanceTimersByTimeAsync(15_000);
  expect(old.closed).toBe(true); expect(r.conn).toBe("offline");
  old.frame("ready"); await flush(); expect(r.conn).toBe("offline");
  r.reconnectNow(); await flush(); expect(Socket.all).toHaveLength(2);
  // Neither a late open nor a close from the old socket affects its replacement.
  old.onopen?.(); old.onclose?.({ code: 1008 });
  expect(old.send).not.toHaveBeenCalled();
  Socket.all[1].frame("ready"); await flush(); expect(r.conn).toBe("online");
});

it("names the stretch that is down: the server only after a moment, this device's network at once", async () => {
  const r = await probe(); vi.useFakeTimers();
  r.start(); await flush();
  expect(r.link).toBe("waiting");
  Socket.all[0].frame("ready"); await flush(); expect(r.link).toBe("online");
  // Just lost: it is being remade, no verdict yet.
  Socket.all[0].onclose?.({ code: 1006 }); await flush();
  expect(r.conn).toBe("offline"); expect(r.link).toBe("waiting");
  await vi.advanceTimersByTimeAsync(r.linkGraceMs);
  expect(r.link).toBe("no-server"); expect(r.unlinked).toBe(true);
  // The browser's word needs no waiting, and a network back starts over.
  r.setNetwork(false); expect(r.link).toBe("no-network");
  r.setNetwork(true); expect(r.link).toBe("waiting");
  r.reconnectNow(true); await flush();
  Socket.all[Socket.all.length - 1].frame("ready"); await flush();
  expect(r.link).toBe("online"); expect(r.unlinked).toBe(false);
});

it("replaces an apparently online socket when the network changes", async () => {
  const r = await probe(); vi.useFakeTimers();
  r.start(); await flush(); const old = Socket.all[0];
  old.frame("ready"); await flush(); expect(r.conn).toBe("online");
  r.reconnectNow(true); await flush();
  expect(old.closed).toBe(true); expect(Socket.all).toHaveLength(2);
  old.frame("ready"); await flush(); expect(r.conn).toBe("connecting");
  Socket.all[1].frame("ready"); await flush(); expect(r.conn).toBe("online");
});

it("detects silent loss while heartbeats keep an idle healthy stream online", async () => {
  const r = await probe(); vi.useFakeTimers();
  r.start(); await flush(); const ws = Socket.all[0];
  ws.frame("ready"); await flush();
  for (let i = 0; i < 5; i++) {
    await vi.advanceTimersByTimeAsync(20_000); ws.frame("heartbeat"); await flush();
    expect(ws.closed).toBe(false); expect(Socket.all).toHaveLength(1);
  }
  await vi.advanceTimersByTimeAsync(60_000);
  expect(ws.closed).toBe(true); expect(r.conn).toBe("offline");
  await vi.advanceTimersByTimeAsync(1_250); expect(Socket.all).toHaveLength(2);
});

it("observes heartbeats while catch-up is still processing the ready frame", async () => {
  const r = await probe(); vi.useFakeTimers();
  let finish!: () => void;
  vi.mocked(r.refresh).mockImplementation(() => new Promise(resolve => { finish = resolve; }));
  r.start(); await flush(); const ws = Socket.all[0]; ws.frame("ready"); await flush();
  for (let i = 0; i < 4; i++) {
    await vi.advanceTimersByTimeAsync(20_000); ws.frame("heartbeat"); await flush();
    expect(ws.closed).toBe(false);
  }
  finish(); await flush(); expect(r.conn).toBe("online");
});

it("bounds a blackholed login and retries it without marking the device removed", async () => {
  const r = await probe(false); vi.useFakeTimers();
  const requests: AbortSignal[] = [];
  vi.stubGlobal("fetch", (_input: unknown, init?: RequestInit) => new Promise((_resolve, reject) => {
    const signal = init!.signal!; requests.push(signal);
    signal.addEventListener("abort", () => reject(signal.reason), { once: true });
  }));
  r.start(); await flush(); expect(requests).toHaveLength(1);
  await vi.advanceTimersByTimeAsync(15_000);
  expect(requests[0].aborted).toBe(true); expect(r.conn).toBe("offline"); expect(r.removed).toBe(false);
  await vi.advanceTimersByTimeAsync(1_250); expect(requests).toHaveLength(2);
});

it("releases a blackholed history fetch so a new connection can catch up", async () => {
  const r = await probe(); vi.useFakeTimers();
  let count = 0;
  vi.stubGlobal("fetch", (_input: unknown, init?: RequestInit) => {
    if (++count > 1) return Promise.resolve(new Response(new Uint8Array(), { headers: { "Content-Type": "application/proto" } }));
    return new Promise((_resolve, reject) => init!.signal!.addEventListener("abort", () => reject(init!.signal!.reason), { once: true }));
  });
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  r.sessions.set("s", { id: "s", machineId: "m", key, metaRev: 0n, lastSeq: 0n, updatedMs: 0n, endedMs: 0n });
  r.start(); await flush(); Socket.all[0].frame("ready"); await flush();
  r.events("s"); await flush(); expect(count).toBe(1);
  Socket.all[0].onclose?.({ code: 1006 });
  r.reconnectNow(); await flush(); Socket.all[1].frame("ready"); await flush();
  await vi.advanceTimersByTimeAsync(45_000); await flush();
  expect(count).toBeGreaterThan(1); expect(r.history("s").loaded).toBe(true);
});


it("bounds terminal opening and cancels it without waiting for socket callbacks", async () => {
  const r = await probe(); vi.useFakeTimers();
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  r.sessions.set("s", { id: "s", machineId: "m", key, metaRev: 0n, lastSeq: 0n, updatedMs: 0n, endedMs: 0n });
  const close = vi.fn();
  const timedOut = r.openTerminal("s", vi.fn(), close).catch(e => e);
  await vi.advanceTimersByTimeAsync(15_000);
  expect((await timedOut).message).toContain("超时"); expect(Socket.all[0].closed).toBe(true);
  Socket.all[0].onopen?.(); expect(Socket.all[0].send).not.toHaveBeenCalled();
  const controller = new AbortController();
  const aborted = r.openTerminal("s", vi.fn(), close, controller.signal).catch(e => e);
  controller.abort(); expect((await aborted).message).toContain("取消");
  expect(Socket.all[1].closed).toBe(true);
  const stopped = r.openTerminal("s", vi.fn(), close).catch(e => e);
  r.stop(); expect(await stopped).toBeInstanceOf(Error); expect(Socket.all[2].closed).toBe(true);
  expect(close).toHaveBeenCalledTimes(3);
});

it("opens the live stream while the admin's lease renewal is still waiting", async () => {
  // Browsers (and Node 24) have Web Locks; renewal then runs, but it must not
  // hold the connection: a slow round trip or an unfinished operation would
  // otherwise keep the stream from ever starting.
  vi.stubGlobal("navigator", { ...globalThis.navigator, locks: { request: vi.fn() } });
  const r = await probe();
  vi.spyOn(r.security, "sync").mockResolvedValue({ member: true, states: [{ core: { authorityID: "d" } }] } as never);
  const renew = vi.spyOn(r.authority, "renewLeaseIfNeeded").mockReturnValue(new Promise(() => {}));
  r.start(); await flush();
  expect(renew).toHaveBeenCalledTimes(1);
  expect(Socket.all).toHaveLength(1);
  Socket.all[0].frame("ready"); await flush();
  expect(r.conn).toBe("online");
});

it("does not take the authority lock on a device that is not the admin", async () => {
  const request = vi.fn();
  vi.stubGlobal("navigator", { ...globalThis.navigator, locks: { request } });
  const r = await probe();
  const renew = vi.spyOn(r.authority, "renewLeaseIfNeeded");
  r.start(); await flush();
  Socket.all[0].frame("ready"); await flush();
  expect(r.conn).toBe("online");
  expect(renew).not.toHaveBeenCalled();
  expect(request).not.toHaveBeenCalled();
});

it("bounds a security sync that never answers and retries it", async () => {
  const r = await probe(); vi.useFakeTimers();
  const sync = vi.spyOn(r.security, "sync").mockReturnValue(new Promise(() => undefined));
  r.start(); await flush();
  expect(r.conn).toBe("connecting"); expect(sync).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(15_000);
  expect(r.conn).toBe("offline"); expect(r.connError).toBe("同步设备状态超时，正在重连");
  expect(r.failures).toBe(1); expect(Socket.all).toHaveLength(0);
  // Waiting out the backoff is not needed: a network back tries at once.
  sync.mockResolvedValue({ member: true, states: [{ core: { authorityID: "authority" } }] } as never);
  r.reconnectNow(); await flush();
  expect(Socket.all).toHaveLength(1);
  Socket.all[0].frame("ready"); await flush(); expect(r.conn).toBe("online");
});

it("does not stay connecting when local state never answers", async () => {
  const r = await probe(); vi.useFakeTimers();
  vi.spyOn(r as any, "warm").mockReturnValue(new Promise(() => undefined));
  r.start(); await flush();
  await vi.advanceTimersByTimeAsync(15_000);
  expect(r.conn).toBe("offline"); expect(r.connError).toBe("读取本机状态超时，正在重连");
  await vi.advanceTimersByTimeAsync(1_250 + 15_000);
  expect(r.failures).toBe(2); expect(Socket.all).toHaveLength(0);
});

it.each(["storage", "socket"])("retries a synchronous %s failure instead of leaving startup stuck", async (step) => {
  const r = await probe(); vi.useFakeTimers();
  if (step === "storage") {
    vi.spyOn(r as any, "warm").mockRejectedValueOnce(new Error("storage unavailable"));
  } else {
    vi.stubGlobal("WebSocket", class { constructor() { throw new DOMException("blocked", "SecurityError"); } });
  }
  // Await the attempt directly so an escaped rejection is a test failure,
  // rather than an unrelated unhandled rejection from start().
  await expect((r as any).connect()).resolves.toBeUndefined();
  expect(r.conn).toBe("offline"); expect(r.failures).toBe(1);
  expect(r.connError).toContain(step === "storage" ? "storage unavailable" : "blocked");
  vi.stubGlobal("WebSocket", Socket);
  await vi.advanceTimersByTimeAsync(1_250); await flush();
  expect(Socket.all).toHaveLength(1);
  Socket.all[0].frame("ready"); await flush(); expect(r.conn).toBe("online");
});

it("ignores a failed local read from an attempt replaced by a network change", async () => {
  const r = await probe(); vi.useFakeTimers();
  let reject!: (e: Error) => void;
  vi.spyOn(r as any, "warm").mockImplementationOnce(() => new Promise((_resolve, fail) => { reject = fail; }));
  const old = (r as any).connect(); await flush();
  r.reconnectNow(true); await flush();
  Socket.all[0].frame("ready"); await flush();
  reject(new Error("old read failed"));
  await expect(old).resolves.toBeUndefined();
  expect(r.conn).toBe("online"); expect(r.failures).toBe(0);
  await vi.advanceTimersByTimeAsync(1_250); expect(Socket.all).toHaveLength(1);
});

it("keeps a recently active stream when foregrounding, but replaces one silent for over thirty seconds", async () => {
  const r = await probe(); vi.useFakeTimers();
  r.start(); await flush(); const old = Socket.all[0];
  old.frame("ready"); await flush();
  await vi.advanceTimersByTimeAsync(20_000); old.frame("heartbeat"); await flush();
  await vi.advanceTimersByTimeAsync(30_000); r.start(); await flush();
  expect(Socket.all).toHaveLength(1); expect(old.closed).toBe(false);
  await vi.advanceTimersByTimeAsync(1); r.start(); await flush();
  expect(old.closed).toBe(true); expect(Socket.all).toHaveLength(2);
  old.frame("ready"); await flush(); expect(r.conn).toBe("connecting");
  Socket.all[1].frame("ready"); await flush(); expect(r.conn).toBe("online");
  r.start(); r.start(); await flush(); expect(Socket.all).toHaveLength(2);
});
it("cancels an old backoff when foreground startup has already replaced it", async () => {
  const r = await probe(); vi.useFakeTimers();
  r.start(); await flush(); Socket.all[0].frame("ready"); await flush();
  Socket.all[0].onclose?.({ code: 1006 });
  r.start(); await flush(); expect(Socket.all).toHaveLength(2);
  await vi.advanceTimersByTimeAsync(1_250); expect(Socket.all).toHaveLength(2);
});

it("delays only cached waiting notices, preserves offline warnings and the ten-second failure deadline", async () => {
  const r = await probe(); vi.useFakeTimers();
  const bar = () => renderToStaticMarkup(createElement(ConnBar, { remote: r }));
  r.start(); await flush(); expect(bar()).toContain("连接中");
  Socket.all[0].frame("ready"); await flush(); r.listed = true;
  Socket.all[0].onclose?.({ code: 1006 });
  expect(bar()).toBe("");
  await vi.advanceTimersByTimeAsync(1_499); expect(bar()).toBe("");
  await vi.advanceTimersByTimeAsync(1); expect(bar()).toContain("正在重连");
  await vi.advanceTimersByTimeAsync(8_500); expect(bar()).toContain("连不上服务器");
  r.setNetwork(false); expect(bar()).toContain("本机网络不通");
});
it("never flashes a waiting notice when cached content reconnects within one second", async () => {
  const r = await probe(); vi.useFakeTimers(); r.listed = true;
  const bar = () => renderToStaticMarkup(createElement(ConnBar, { remote: r }));
  r.start(); await flush(); Socket.all[0].frame("ready"); await flush();
  r.stop(); r.start(); await flush(); expect(bar()).toBe("");
  await vi.advanceTimersByTimeAsync(1_000); Socket.all[1].frame("ready"); await flush();
  expect(bar()).toBe(""); await vi.advanceTimersByTimeAsync(1_000); expect(bar()).toBe("");
});
