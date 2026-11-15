import { afterEach, expect, test, vi } from "vitest";
import { SyntheticRemote } from "../test/remote-fixture";
import type { FrameStream } from "./path";
import { resetProcessBudgetForTest } from "./recovery";
import { TerminalReconnectController } from "./terminal-recovery";

afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); resetProcessBudgetForTest(); });

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

test.each(["network change", "socket drop"])("%s schedules recovery without calling independent terminal listeners", async kind => {
  const remote = await SyntheticRemote.create();
  const runtime = remote as unknown as { closed: boolean; generation: number; retryTimer?: ReturnType<typeof setTimeout>; openStream(g: number): void; connect(): Promise<void> };
  const socket: FrameStream = { ready: true, close: vi.fn(), send: vi.fn() };
  const reconnect = vi.spyOn(runtime, "connect").mockResolvedValue();
  vi.spyOn(remote.deviceLink, "openPathStream").mockReturnValue(socket);
  const notified = vi.fn();
  try {
    vi.useFakeTimers(); runtime.closed = false; remote.conn = "online";
    runtime.openStream(runtime.generation);
    remote.deviceLink.openStream(notified, vi.fn());
    if (kind === "network change") {
      remote.networkChanged("route-b");
      await Promise.resolve();
    } else {
      socket.onEnd?.();
    }
    expect(notified).not.toHaveBeenCalled();
    if (kind === "network change") {
      expect(reconnect).toHaveBeenCalledOnce();
    } else {
      expect(runtime.retryTimer).toBeDefined();
      await vi.advanceTimersByTimeAsync(1501);
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

test("close callback throwing does not break recovery scheduling", async () => {
  const remote = await SyntheticRemote.create();
  const runtime = remote as unknown as { closed: boolean; generation: number; retryTimer?: ReturnType<typeof setTimeout>; openStream(g: number): void; connect(): Promise<void> };
  const failure = Error("injected socket close");
  const socket: FrameStream = { ready: true, close() { throw failure; }, send: vi.fn() };
  const reconnect = vi.spyOn(runtime, "connect").mockResolvedValue();
  vi.spyOn(remote.deviceLink, "openPathStream").mockReturnValue(socket);
  try {
    vi.useFakeTimers();
    runtime.closed = false;
    remote.conn = "online";
    runtime.openStream(runtime.generation);
    expect(() => socket.onEnd?.()).toThrow(failure);
    expect(remote.conn).toBe("offline");
    expect(runtime.retryTimer).toBeDefined();
    await vi.advanceTimersByTimeAsync(1501);
    expect(reconnect).toHaveBeenCalledOnce();
  } finally { vi.useRealTimers(); await remote.dispose(); }
});

test("terminal controller reopens after opening failure when main goes online and backs off with global budget", async () => {
  vi.useFakeTimers();
  vi.spyOn(Math, "random").mockReturnValue(0.5);
  let online = true;
  const opens = vi.fn();
  const ctrl = new TerminalReconnectController(() => online, opens);
  try {
    // Opening failure stays closed while online stays true.
    ctrl.onClosed(false);
    expect(opens).not.toHaveBeenCalled();
    // Main transitions offline -> online: reopens immediately using first token.
    online = false;
    online = true;
    ctrl.onMainOnline();
    expect(opens).toHaveBeenCalledTimes(1);
    // Repeated failure before 5s stability enters exponential full-jitter backoff (0.5 * 1000ms = 500ms).
    ctrl.onClosed(true);
    expect(opens).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(499);
    expect(opens).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(opens).toHaveBeenCalledTimes(2);
    // Second consecutive failure doubles window (0.5 * 2000ms = 1000ms).
    ctrl.onClosed(true);
    await vi.advanceTimersByTimeAsync(999);
    expect(opens).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(opens).toHaveBeenCalledTimes(3);
    // Remaining global tokens in processBucket (capacity 4, after 3 used + refills):
    // 5 simultaneous terminals bursting initial reopen are bounded by global 4/2s bucket.
    resetProcessBudgetForTest();
    const burstOpens = vi.fn();
    const peers = Array.from({ length: 5 }, () => new TerminalReconnectController(() => true, burstOpens));
    try {
      for (const p of peers) p.onClosed(true);
      expect(burstOpens).toHaveBeenCalledTimes(4);
      await vi.advanceTimersByTimeAsync(500);
      expect(burstOpens).toHaveBeenCalledTimes(5);
    } finally {
      peers.forEach(p => p.dispose());
    }
  } finally {
    ctrl.dispose();
  }
});
