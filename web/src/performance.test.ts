import { afterEach, expect, it, vi } from "vitest";
import { Remote } from "./remote";
import { create } from "@bufbuild/protobuf";
import { CommandResultSchema } from "./gen/remoteagent/v1/remote_pb";

class Socket {
  static all: Socket[] = [];
  static OPEN = 1;
  readyState = 0;
  onopen?: () => void;
  onclose?: () => void;
  onerror?: () => void;
  onmessage?: (m: { data: ArrayBuffer }) => void;
  send = vi.fn();
  constructor(readonly url: string) { Socket.all.push(this); }
  open() { this.readyState = 1; this.onopen?.(); }
  close() { this.readyState = 3; queueMicrotask(() => this.onclose?.()); }
}
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); Socket.all = []; });
async function probe() {
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]) as CryptoKeyPair;
  class Probe extends Remote { constructor() { super({ server: "https://ra.example.com", root: key, devicePrivate: device.privateKey, deviceId: "d" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key }); } }
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
