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
  vi.spyOn(r.security, "sync").mockResolvedValue({} as never);
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
