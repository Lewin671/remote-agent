import { afterEach, expect, test, vi } from "vitest";
import { SyntheticRemote } from "../test/remote-fixture";
import type { FrameStream } from "./path";

afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); });

test("throwing stream cleanup still notifies and drains the other streams", async () => {
  const remote = await SyntheticRemote.create();
  const failure = Error("injected stop");
  const notified = vi.fn(), stopped = vi.fn(), second = vi.fn();
  const first = remote.deviceLink.openStream(notified, () => { throw failure; });
  remote.deviceLink.openStream(second, stopped);
  try {
    expect(() => remote.deviceLink.closeStreams("closed")).toThrow(failure);
    first.end("again");
    expect(notified).toHaveBeenCalledExactlyOnceWith("closed");
    expect(stopped).toHaveBeenCalledOnce();
    expect(second).toHaveBeenCalledOnce();
    expect(() => remote.deviceLink.closeStreams("again")).not.toThrow();
  } finally { await remote.dispose(); }
});

test("stop completes state and all observers despite throwing physical and product closes", async () => {
  const remote = await SyntheticRemote.create();
  const failure = Error("injected socket close");
  const state = remote as unknown as { ws?: FrameStream; closed: boolean; graceTimer?: ReturnType<typeof setTimeout> };
  const unsubscribe: (() => void)[] = [];
  const closed = vi.fn(), observed = vi.fn();
  try {
    remote.conn = "online";
    state.ws = { ready: true, send: vi.fn(), close() { throw failure; } };
    state.graceTimer = setTimeout(() => {}, 60000);
    remote.deviceLink.openStream(() => { throw Error("injected listener"); }, vi.fn());
    remote.deviceLink.openStream(closed, vi.fn());
    unsubscribe.push(remote.deviceLink.subscribe(() => { throw Error("injected observer"); }));
    unsubscribe.push(remote.subscribe(observed));
    expect(() => remote.stop()).toThrow(failure);
    expect(remote.conn).toBe("offline");
    expect(state.ws).toBeUndefined();
    expect(state.graceTimer).toBeUndefined();
    expect(state.closed).toBe(true);
    expect(closed).toHaveBeenCalledOnce();
    expect(observed).toHaveBeenCalledOnce();
  } finally {
    unsubscribe.forEach(fn => fn());
    // A deliberately broken stop may have left streams behind. Do not let
    // their injected failures hide the assertion that detected that leak.
    try { remote.deviceLink.closeStreams("test cleanup"); } catch { /* injected */ }
    await remote.dispose();
  }
});

test.each(["network change", "socket drop"])("%s schedules recovery even when close callbacks throw", async kind => {
  const remote = await SyntheticRemote.create();
  const runtime = remote as unknown as { closed: boolean; generation: number; retryTimer?: ReturnType<typeof setTimeout>; openStream(g: number): void; connect(): Promise<void> };
  const socket: FrameStream = { ready: true, close: vi.fn(), send: vi.fn() };
  const reconnect = vi.spyOn(runtime, "connect").mockResolvedValue();
  vi.spyOn(remote.deviceLink, "openPathStream").mockReturnValue(socket);
  const failure = Error("injected product close");
  try {
    vi.useFakeTimers();
    runtime.closed = false;
    remote.conn = "online";
    runtime.openStream(runtime.generation);
    remote.deviceLink.openStream(() => { throw failure; }, vi.fn());
    if (kind === "network change") {
      expect(() => remote.reconnectNow(true)).toThrow(failure);
      expect(reconnect).toHaveBeenCalledOnce();
    } else {
      expect(() => socket.onEnd?.()).toThrow(failure);
      expect(remote.conn).toBe("offline");
      expect(runtime.retryTimer).toBeDefined();
      await vi.advanceTimersByTimeAsync(1251);
      expect(reconnect).toHaveBeenCalledOnce();
    }
  } finally { vi.useRealTimers(); await remote.dispose(); }
});

test("physical close failure settles a terminal opening and releases its logical stream", async () => {
  const remote = await SyntheticRemote.create();
  const failure = Error("injected terminal close");
  const socket: FrameStream = { ready: false, close() { throw failure; }, send: vi.fn() };
  const notify = vi.fn();
  vi.spyOn(remote.deviceLink, "openPathStream").mockReturnValue(socket);
  try {
    // Only the session lookup precedes opening; no fake daemon reply is needed.
    remote.sessions.set("terminal", { machineId: remote.machine } as never);
    let settled = false;
    const opening = remote.openTerminal("terminal", vi.fn(), notify).catch(error => { settled = true; return error; });
    expect(() => socket.onEnd?.()).toThrow(failure);
    await Promise.resolve();
    await Promise.resolve();
    expect(settled, "opening waiter must settle despite a close failure").toBe(true);
    expect(await opening).toBeInstanceOf(Error);
    expect(notify).toHaveBeenCalledOnce();
    expect(() => remote.deviceLink.closeStreams("again")).not.toThrow();
  } finally { await remote.dispose(); }
});


test("stop callbacks cannot register a new terminal on the stopped owner", async () => {
  const remote = await SyntheticRemote.create();
  const open = vi.spyOn(remote.deviceLink, "openPathStream").mockImplementation(() => { throw Error("unexpected new path"); });
  let rejected: Promise<unknown> | undefined;
  remote.sessions.set("terminal", { machineId: remote.machine } as never);
  remote.deviceLink.openStream(() => {
    rejected = remote.openTerminal("terminal", vi.fn(), vi.fn()).catch(error => error);
  }, vi.fn());
  try {
    remote.stop();
    expect(await rejected).toBeInstanceOf(Error);
    expect(open).not.toHaveBeenCalled();
  } finally { await remote.dispose(); }
});
