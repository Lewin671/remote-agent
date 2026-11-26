// @vitest-environment happy-dom
import { act } from "react";
import { demoSessions } from "../test/remote-fixture";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { getCoreHost } from "../core/host";
import { TimerKind } from "../gen/remoteagent/core/v1/app_pb";
import { TermConfirmKind, type Remote } from "../remote";
import { TerminalView } from "./Terminal";

const terminal = vi.hoisted(() => ({ writes: vi.fn(), resize: vi.fn(), dataHandler: undefined as ((s: string) => void) | undefined }));
vi.mock("@xterm/xterm", () => ({ Terminal: class {
  cols = 80; rows = 24; element?: HTMLElement;
  constructor(readonly options: { fontSize: number }) {}
  open(el: HTMLElement) { this.element = el; }
  attachCustomWheelEventHandler() {}
  onData(cb: (s: string) => void) { terminal.dataHandler = cb; return { dispose() {} }; }
  resize(cols: number, rows: number) { this.cols = cols; this.rows = rows; terminal.resize(cols, rows); }
  write(data: Uint8Array, done?: () => void) { terminal.writes(data); done?.(); }
  reset() {} dispose() {} focus() {} blur() {}
} }));
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement;
const remotes: DemoRemote[] = [];
const track = (r: DemoRemote) => { remotes.push(r); return r; };
afterEach(async () => {
  if (root) await act(async () => root!.unmount()); root = undefined;
  host?.remove();
  for (const r of remotes.splice(0)) r.dispose();
  vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers();
});
function stubCoreEntropy(byte7: number) {
  const orig = crypto.getRandomValues.bind(crypto);
  return vi.spyOn(crypto, "getRandomValues").mockImplementation(<T extends ArrayBufferView<ArrayBuffer>>(arr: T): T => {
    orig(arr);
    if (arr instanceof Uint8Array && arr.length >= 8) {
      arr.fill(0);
      for (let i = 7; i < arr.length; i += 8) arr[i] = byte7;
    }
    return arr;
  });
}
function setMainOnline(remote: DemoRemote, online: boolean) {
  remote.setDemoMainOnline(online);
}
async function mount() {
  vi.useFakeTimers();
  let hidden = false, width = 600;
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(() => width);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(480);
  const observers: { callback: (entries: unknown[]) => void; elements: Element[] }[] = [];
  vi.stubGlobal("ResizeObserver", class {
    private observation: (typeof observers)[number];
    constructor(callback: (entries: unknown[]) => void) { this.observation = { callback, elements: [] }; observers.push(this.observation); }
    observe(el: Element) { this.observation.elements.push(el); } disconnect() {}
  });
  terminal.writes.mockClear(); terminal.resize.mockClear();
  const remote = track(DemoRemote.create(new URLSearchParams())); remote.start();
  let frame!: Parameters<Remote["openTerminal"]>[1];
  let end!: Parameters<Remote["openTerminal"]>[2];
  let onStateChange: Parameters<Remote["openTerminal"]>[4];
  const close = vi.fn(), send = vi.fn(), confirmRebuild = vi.fn();
  const open = vi.spyOn(remote, "openTerminal").mockImplementation(async (_sid, receive, onClose, _sig, onState) => {
    frame = receive; end = onClose; onStateChange = onState; return { close, send, confirmRebuild } as never;
  });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<TerminalView remote={remote} session={demoSessions(remote).get("s-run")!} running />));
  await act(async () => { await frame({ kind: { case: "snapshot", value: { cols: 80, rows: 24, data: new Uint8Array([65]) } } } as never); });
  send.mockClear();
  const layout = (value: number, paneHeight = 480, screenHeight = 480) => {
    width = value;
    for (const o of observers) o.callback(o.elements.map(el => {
      const height = el.classList.contains("term") ? 480 : el.classList.contains("tpan") ? paneHeight : screenHeight;
      return { target: el, contentRect: { width, height }, borderBoxSize: [{ inlineSize: width, blockSize: height }] };
    }));
  };
  return { remote, open, close, send, confirmRebuild, get frame() { return frame; }, get end() { return end; }, get onStateChange() { return onStateChange; }, hide: (value: boolean) => { hidden = value; }, resize: layout, layout };
}
it("keeps the healthy independent terminal through hiding and main reconnects", async () => {
  const { remote, open, close, frame, hide } = await mount();
  await act(async () => { hide(true); document.dispatchEvent(new Event("visibilitychange")); });
  expect(close).not.toHaveBeenCalled(); expect(open).toHaveBeenCalledTimes(1);
  await act(async () => { await frame({ kind: { case: "output", value: new Uint8Array([66]) } } as never); });
  expect(terminal.writes).toHaveBeenLastCalledWith(new Uint8Array([66]));
  await act(async () => { setMainOnline(remote, false); });
  expect(close).not.toHaveBeenCalled();
  await act(async () => { setMainOnline(remote, true); });
  expect(open).toHaveBeenCalledTimes(1);
  expect(terminal.writes).toHaveBeenLastCalledWith(new Uint8Array([66]));
});
it("does not resize tmux while hidden, including a refit already scheduled before hiding", async () => {
  const { send, hide, resize } = await mount();
  resize(700); hide(true);
  await act(async () => vi.advanceTimersByTimeAsync(200));
  expect(send).not.toHaveBeenCalled();
  resize(800); await act(async () => vi.advanceTimersByTimeAsync(200));
  expect(send).not.toHaveBeenCalled();
  await act(async () => { hide(false); document.dispatchEvent(new Event("visibilitychange")); });
  await act(async () => vi.advanceTimersByTimeAsync(200));
  expect(send).toHaveBeenCalledTimes(1);
  expect(send.mock.calls[0][0].kind.case).toBe("resize");
});

it("defers the first fit of a terminal reopened in the background until visible", async () => {
  const state = await mount();
  await act(async () => { state.hide(true); document.dispatchEvent(new Event("visibilitychange")); });
  await act(async () => {
    setMainOnline(state.remote, false);
    state.end("closed");
  });
  expect(state.open).toHaveBeenCalledTimes(1);
  await act(async () => { setMainOnline(state.remote, true); });
  expect(state.open).toHaveBeenCalledTimes(2);
  await act(async () => { await state.frame({ kind: { case: "snapshot", value: { cols: 80, rows: 24, data: new Uint8Array([65]) } } } as never); });
  expect(state.send).not.toHaveBeenCalled();
  await act(async () => { state.hide(false); document.dispatchEvent(new Event("visibilitychange")); });
  expect(state.send).toHaveBeenCalledTimes(1);
});

it("does not drop a queued reopen when offline arrives in the same turn as terminal close", async () => {
  stubCoreEntropy(0);
  const state = await mount();
  await act(async () => { state.hide(true); document.dispatchEvent(new Event("visibilitychange")); });
  await act(async () => {
    state.end("closed");
    setMainOnline(state.remote, false);
  });
  expect(state.open).toHaveBeenCalledTimes(2);
  await act(async () => { setMainOnline(state.remote, true); });
  await act(async () => { await state.frame({ kind: { case: "snapshot", value: { cols: 80, rows: 24, data: new Uint8Array([65]) } } } as never); });
  expect(state.send).not.toHaveBeenCalled();
  await act(async () => { state.hide(false); document.dispatchEvent(new Event("visibilitychange")); });
  expect(state.send).toHaveBeenCalledTimes(1);
});

it("keeps the first screen out of sight until it is fitted to the pane, or for a moment at most", async () => {
  const vm = vi;
  vm.useFakeTimers();
  vm.spyOn(document, "hidden", "get").mockReturnValue(false);
  vm.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
  vm.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(480);
  vm.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  const remote = track(DemoRemote.create(new URLSearchParams())); remote.start();
  let frame!: Parameters<Remote["openTerminal"]>[1];
  vi.spyOn(remote, "openTerminal").mockImplementation(async (_sid, receive) => { frame = receive; return { close: vi.fn(), send: vi.fn() } as never; });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<TerminalView remote={remote} session={demoSessions(remote).get("s-run")!} running />));
  const pane = () => host.querySelector(".tpan")!;
  expect(pane().classList.contains("unfit")).toBe(true);
  await act(async () => { await frame({ kind: { case: "snapshot", value: { cols: 80, rows: 24, data: new Uint8Array([65]) } } } as never); });
  expect(pane().classList.contains("unfit")).toBe(true);
  await act(async () => vi.advanceTimersByTimeAsync(899));
  expect(pane().classList.contains("unfit")).toBe(true);
  await act(async () => vi.advanceTimersByTimeAsync(2));
  expect(pane().classList.contains("unfit")).toBe(false);
});

it("shows the first screen as soon as the fitted one is in", async () => {
  const vm = vi;
  vm.useFakeTimers();
  vm.spyOn(document, "hidden", "get").mockReturnValue(false);
  vm.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
  vm.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(480);
  vm.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  const remote = track(DemoRemote.create(new URLSearchParams())); remote.start();
  let frame!: Parameters<Remote["openTerminal"]>[1];
  vi.spyOn(remote, "openTerminal").mockImplementation(async (_sid, receive) => { frame = receive; return { close: vi.fn(), send: vi.fn() } as never; });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<TerminalView remote={remote} session={demoSessions(remote).get("s-run")!} running />));
  const pane = () => host.querySelector(".tpan")!;
  const snapshot = (cols: number) => act(async () => { await frame({ kind: { case: "snapshot", value: { cols, rows: 24, data: new Uint8Array([65]) } } } as never); });
  await snapshot(80);
  expect(pane().classList.contains("unfit")).toBe(true);
  // The fit was asked for; the computer's answer is a screen of the new size.
  await snapshot(56);
  expect(pane().classList.contains("unfit")).toBe(false);
});

it("keeps the terminal prompt at the bottom when its pane shrinks, without resizing tmux for height", async () => {
  const state = await mount();
  const screen = host.querySelector<HTMLElement>(".tpan > div")!;
  await act(async () => state.layout(600, 300, 700));
  expect(screen.style.marginTop).toBe("-400px");
  await act(async () => state.layout(600, 800, 700));
  expect(screen.style.marginTop).toBe("0px");
  await act(async () => vi.advanceTimersByTimeAsync(200));
  expect(state.send).not.toHaveBeenCalled();
});

it("recovers a terminal leg that closes after the main path is already online", async () => {
  const vm = await mount();
  await act(async () => { setMainOnline(vm.remote, false); });
  await act(async () => { setMainOnline(vm.remote, true); });
  expect(vm.open).toHaveBeenCalledTimes(1);
  await act(async () => { vm.end("late loss"); });
  expect(vm.open).toHaveBeenCalledTimes(2);
  expect(terminal.writes).toHaveBeenCalledTimes(1);
  expect(vm.send.mock.calls.filter(([f]) => f.kind.case === "input")).toHaveLength(0);
});

it("reopens after an opening failure when main transitions from offline to online", async () => {
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(480);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  const remote = track(DemoRemote.create(new URLSearchParams())); remote.start();
  const open = vi.spyOn(remote, "openTerminal")
    .mockRejectedValueOnce(Error("initial opening failure"))
    .mockResolvedValueOnce({ close: vi.fn(), send: vi.fn() } as never);
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const session = demoSessions(remote).get("s-run")!;
  await act(async () => root!.render(<TerminalView remote={remote} session={session} running />));
  expect(open).toHaveBeenCalledTimes(1);
  // Transitions offline -> online should reopen even though the first attempt never opened.
  await act(async () => { setMainOnline(remote, false); });
  expect(open).toHaveBeenCalledTimes(1);
  await act(async () => { setMainOnline(remote, true); });
  expect(open).toHaveBeenCalledTimes(2);
});

it("does not open a newly selected session while offline, and opens it once online", async () => {
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(480);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  const remote = track(DemoRemote.create(new URLSearchParams()));
  await act(async () => { remote.start(); });
  setMainOnline(remote, false);
  const openedSids: string[] = [];
  vi.spyOn(remote, "openTerminal").mockImplementation(async (sid) => {
    openedSids.push(sid);
    return { close: vi.fn(), send: vi.fn() } as never;
  });
  const s1 = demoSessions(remote).get("s-run")!;
  const s2 = { ...s1, id: "s-next" };
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<TerminalView remote={remote} session={s1} running />));
  expect(openedSids).toEqual([]);
  // Switching session while offline must still not open in the background.
  await act(async () => root!.render(<TerminalView remote={remote} session={s2} running />));
  expect(openedSids).toEqual([]);
  // Once main becomes online, only the newly selected session opens.
  await act(async () => { setMainOnline(remote, true); });
  expect(openedSids).toEqual(["s-next"]);
});

it("applies backoff before re-stabilizing when reopened terminal keeps failing", async () => {
  // Deterministic 0.5 jitter unit (0x80 in MSB of uint64 -> (1<<52)/(1<<53) = 0.5 -> 500ms).
  stubCoreEntropy(0x80);
  const vm = await mount();
  expect(vm.open).toHaveBeenCalledTimes(1);
  await act(async () => { vm.end("loss 1"); });
  // First reopen after initial connect uses a token immediately.
  expect(vm.open).toHaveBeenCalledTimes(2);
  // Second failure before 5s stability must back off rather than spinning immediately.
  await act(async () => { vm.end("loss 2"); });
  expect(vm.open).toHaveBeenCalledTimes(2);
  await act(async () => { await vi.advanceTimersByTimeAsync(499); });
  expect(vm.open).toHaveBeenCalledTimes(2);
  await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(vm.open).toHaveBeenCalledTimes(3);
});

it("keeps terminal in connecting state during cold-start reachability unknown, opens when ready, and closes when grace expires offline", async () => {
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(480);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  const remote = track(DemoRemote.create(new URLSearchParams("conn=connecting")));
  await act(async () => { remote.start(); });
  const open = vi.spyOn(remote, "openTerminal").mockResolvedValue({ close: vi.fn(), send: vi.fn() } as never);
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const s1 = demoSessions(remote).get("s-run")!;
  await act(async () => root!.render(<TerminalView remote={remote} session={s1} running />));

  // Neutral connecting capsule, no "terminal closed" card while grace is active.
  expect(open).toHaveBeenCalledTimes(0);
  expect(host.querySelector(".tstate")).toBeNull();
  expect(host.querySelector(".tchip")?.textContent).not.toContain("断开");
  expect(host.querySelector(".tchip")?.textContent).not.toContain("Disconnected");

  // Grace expires while still offline -> transitions to closed card.
  await act(async () => { getCoreHost(remote).fireActiveTimer(TimerKind.LINK_GRACE, "cold-start"); });
  expect(open).toHaveBeenCalledTimes(0);
  expect(host.querySelector(".tstate")).not.toBeNull();

  // Cold-start connecting -> main comes online -> automatically opens terminal.
  act(() => root!.unmount());
  const remote2 = track(DemoRemote.create(new URLSearchParams("conn=connecting")));
  await act(async () => { remote2.start(); });
  const open2 = vi.spyOn(remote2, "openTerminal").mockResolvedValue({ close: vi.fn(), send: vi.fn() } as never);
  root = createRoot(host);
  await act(async () => root!.render(<TerminalView remote={remote2} session={demoSessions(remote2).get("s-run")!} running />));
  expect(open2).toHaveBeenCalledTimes(0);
  expect(host.querySelector(".tstate")).toBeNull();
  await act(async () => {
    remote2.setDemoMainOnline(true);
  });
  expect(open2).toHaveBeenCalledTimes(1);
});

it("clears open history reader and restores input when switching sessions on the same TerminalView", async () => {
  const vm = await mount();
  const s1 = demoSessions(vm.remote).get("s-run")!;
  const s2 = { ...s1, id: "s-next" };

  // Enter reading on s1 via wheel-up, accept a history page, and trigger live output while reading.
  const termEl = host.querySelector<HTMLElement>(".term")!;
  await act(async () => {
    termEl.dispatchEvent(new WheelEvent("wheel", { deltaY: -40, bubbles: true, cancelable: true }));
  });
  const reqCall = vm.send.mock.calls.find((c) => c[0]?.kind?.case === "historyRequest");
  expect(reqCall).toBeDefined();
  const requestId = reqCall![0].kind.value.requestId as string;
  await act(async () => {
    const { create, toBinary } = await import("@bufbuild/protobuf");
    const { getCoreHost } = await import("../core/host");
    const { TerminalHistoryEventKind } = await import("../gen/remoteagent/core/v1/terminal_history_pb");
    const { TerminalHistoryPageSchema } = await import("../gen/remoteagent/v1/remote_pb");
    const tid = (vm.open.mock.calls[0]?.[5] as string) || s1.id;
    getCoreHost(vm.remote).sendTerminalHistory(
      tid,
      TerminalHistoryEventKind.PAGE_RECEIVED,
      toBinary(
        TerminalHistoryPageSchema,
        create(TerminalHistoryPageSchema, { requestId, lines: ["old line"], nextCursor: "c:1", error: "" }),
      ) as Uint8Array<ArrayBuffer>,
    );
    await vm.frame({ kind: { case: "output", value: new Uint8Array([66]) } } as never);
  });
  expect(host.querySelector(".terminal-history")).not.toBeNull();
  expect(host.querySelector(".tpill")).not.toBeNull();

  // Switch session prop on the same mounted TerminalView instance.
  vm.send.mockClear();
  await act(async () => root!.render(<TerminalView remote={vm.remote} session={s2} running />));
  await act(async () => {
    await vm.frame({ kind: { case: "snapshot", value: { cols: 80, rows: 24, data: new Uint8Array([67]) } } } as never);
  });

  // Stale reader from s1 must be cleared and keyboard input on s2 must go through.
  expect(host.querySelector(".terminal-history")).toBeNull();
  expect(host.querySelector(".tpill")).toBeNull();
  await act(async () => { terminal.dataHandler?.("hi"); });
  expect(vm.send.mock.calls.some((c) => c[0]?.kind?.case === "input")).toBe(true);
});

it("renders Core-driven rebuild confirmation without auto-closing and dispatches confirmRebuild or close (M4)", async () => {
  const vm = await mount();
  expect(vm.open).toHaveBeenCalledTimes(1);

  // Core enters NeedsConfirmation -> shell MUST NOT auto-close terminal; renders rebuild & close buttons.
  await act(async () => {
    vm.onStateChange?.({ kind: "confirm", confirmKind: TermConfirmKind.Input });
  });
  expect(vm.close).not.toHaveBeenCalled();
  const card = host.querySelector(".tstate");
  expect(card).not.toBeNull();
  const rebuildBtn = card!.querySelector<HTMLButtonElement>("button.btn.pri");
  const closeBtn = card!.querySelector<HTMLButtonElement>("button.btn:not(.pri)");
  expect(rebuildBtn).not.toBeNull();
  expect(closeBtn).not.toBeNull();

  // Clicking primary rebuild button dispatches confirmRebuild to Core.
  await act(async () => { rebuildBtn!.click(); });
  expect(vm.confirmRebuild).toHaveBeenCalledTimes(1);
  expect(vm.close).not.toHaveBeenCalled();

  // Core confirms rebuild -> shell opens a fresh terminal stream.
  await act(async () => { vm.onStateChange?.({ kind: "rebuild" }); });
  expect(vm.open).toHaveBeenCalledTimes(2);
  await act(async () => {
    await vm.frame({ kind: { case: "snapshot", value: { cols: 80, rows: 24, data: new Uint8Array([65]) } } } as never);
  });

  // Choosing close in confirmation card closes the terminal instead of rebuilding.
  await act(async () => {
    vm.onStateChange?.({ kind: "confirm", confirmKind: TermConfirmKind.Restart });
  });
  const closeBtn2 = host.querySelector<HTMLButtonElement>(".tstate button.btn:not(.pri)")!;
  await act(async () => { closeBtn2.click(); });
  expect(vm.close).toHaveBeenCalledTimes(1);
});
