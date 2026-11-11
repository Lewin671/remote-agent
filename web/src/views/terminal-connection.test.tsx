// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import type { Remote } from "../remote";
import { TerminalView } from "./Terminal";

const terminal = vi.hoisted(() => ({ writes: vi.fn(), resize: vi.fn() }));
vi.mock("@xterm/xterm", () => ({ Terminal: class {
  cols = 80; rows = 24; element?: HTMLElement;
  constructor(readonly options: { fontSize: number }) {}
  open(el: HTMLElement) { this.element = el; }
  attachCustomWheelEventHandler() {}
  onData() { return { dispose() {} }; }
  resize(cols: number, rows: number) { this.cols = cols; this.rows = rows; terminal.resize(cols, rows); }
  write(data: Uint8Array, done?: () => void) { terminal.writes(data); done?.(); }
  reset() {} dispose() {} focus() {} blur() {}
} }));
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement;
afterEach(async () => {
  if (root) await act(async () => root!.unmount()); root = undefined;
  host?.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers();
});
async function mount() {
  let hidden = false, width = 600;
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(() => width);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(480);
  const observers: (() => void)[] = [];
  vi.stubGlobal("ResizeObserver", class {
    constructor(callback: () => void) { observers.push(callback); }
    observe() {} disconnect() {}
  });
  terminal.writes.mockClear(); terminal.resize.mockClear();
  const remote = DemoRemote.create(new URLSearchParams()); remote.start();
  let frame!: Parameters<Remote["openTerminal"]>[1];
  const close = vi.fn(), send = vi.fn();
  const open = vi.spyOn(remote, "openTerminal").mockImplementation(async (_sid, receive) => {
    frame = receive; return { close, send } as never;
  });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<TerminalView remote={remote} session={remote.sessions.get("s-run")!} running />));
  await act(async () => { await frame({ kind: { case: "snapshot", value: { cols: 80, rows: 24, data: new Uint8Array([65]) } } } as never); });
  send.mockClear(); vi.useFakeTimers();
  return { remote, open, close, send, get frame() { return frame; }, hide: (value: boolean) => { hidden = value; }, resize: (value: number) => { width = value; observers.forEach(f => f()); } };
}
it("keeps the terminal through hiding and closes it with the main connection", async () => {
  const { remote, open, close, frame, hide } = await mount();
  await act(async () => { hide(true); document.dispatchEvent(new Event("visibilitychange")); });
  expect(close).not.toHaveBeenCalled(); expect(open).toHaveBeenCalledTimes(1);
  await act(async () => { await frame({ kind: { case: "output", value: new Uint8Array([66]) } } as never); });
  expect(terminal.writes).toHaveBeenLastCalledWith(new Uint8Array([66]));
  await act(async () => { remote.conn = "offline"; remote.touch(); });
  expect(close).toHaveBeenCalledTimes(1);
  await act(async () => { remote.conn = "online"; remote.touch(); });
  expect(open).toHaveBeenCalledTimes(2);
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
  await act(async () => { state.remote.conn = "offline"; state.remote.touch(); });
  await act(async () => { state.remote.conn = "online"; state.remote.touch(); });
  await act(async () => { await state.frame({ kind: { case: "snapshot", value: { cols: 80, rows: 24, data: new Uint8Array([65]) } } } as never); });
  expect(state.send).not.toHaveBeenCalled();
  await act(async () => { state.hide(false); document.dispatchEvent(new Event("visibilitychange")); });
  expect(state.send).toHaveBeenCalledTimes(1);
});

it("keeps the first screen out of sight until it is fitted to the pane, or for a moment at most", async () => {
  const vm = vi;
  vm.spyOn(document, "hidden", "get").mockReturnValue(false);
  vm.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
  vm.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(480);
  vm.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  const remote = DemoRemote.create(new URLSearchParams()); remote.start();
  let frame!: Parameters<Remote["openTerminal"]>[1];
  vi.spyOn(remote, "openTerminal").mockImplementation(async (_sid, receive) => { frame = receive; return { close: vi.fn(), send: vi.fn() } as never; });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<TerminalView remote={remote} session={remote.sessions.get("s-run")!} running />));
  const pane = () => host.querySelector(".tpan")!;
  expect(pane().classList.contains("unfit")).toBe(true);
  vi.useFakeTimers();
  await act(async () => { await frame({ kind: { case: "snapshot", value: { cols: 80, rows: 24, data: new Uint8Array([65]) } } } as never); });
  expect(pane().classList.contains("unfit")).toBe(true);
  await act(async () => vi.advanceTimersByTimeAsync(899));
  expect(pane().classList.contains("unfit")).toBe(true);
  await act(async () => vi.advanceTimersByTimeAsync(2));
  expect(pane().classList.contains("unfit")).toBe(false);
});

it("shows the first screen as soon as the fitted one is in", async () => {
  const vm = vi;
  vm.spyOn(document, "hidden", "get").mockReturnValue(false);
  vm.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
  vm.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(480);
  vm.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  const remote = DemoRemote.create(new URLSearchParams()); remote.start();
  let frame!: Parameters<Remote["openTerminal"]>[1];
  vi.spyOn(remote, "openTerminal").mockImplementation(async (_sid, receive) => { frame = receive; return { close: vi.fn(), send: vi.fn() } as never; });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<TerminalView remote={remote} session={remote.sessions.get("s-run")!} running />));
  const pane = () => host.querySelector(".tpan")!;
  vi.useFakeTimers();
  const snapshot = (cols: number) => act(async () => { await frame({ kind: { case: "snapshot", value: { cols, rows: 24, data: new Uint8Array([65]) } } } as never); });
  await snapshot(80);
  expect(pane().classList.contains("unfit")).toBe(true);
  // The fit was asked for; the computer's answer is a screen of the new size.
  await snapshot(56);
  expect(pane().classList.contains("unfit")).toBe(false);
});
