// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App";
import { DemoRemote } from "./dev/demo";
import * as remoteModule from "./remote";
import * as storage from "./storage";
import * as authoritySetup from "./authority-setup";
import { Home } from "./views/Home";

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let host: HTMLDivElement | undefined;
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined; host?.remove();
  vi.restoreAllMocks(); vi.useRealTimers();
});
async function mount(initiallyHidden = false) {
  let hidden = initiallyHidden;
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  vi.spyOn(storage, "loadAccount").mockResolvedValue(undefined);
  vi.spyOn(authoritySetup, "authorityStartup").mockResolvedValue({ kind: "none" } as never);
  vi.spyOn(remoteModule, "resumeRegistration").mockResolvedValue();
  const remote = DemoRemote.create(new URLSearchParams("role=member&empty=none"));
  vi.spyOn(remoteModule.Remote, "load").mockResolvedValue(remote);
  const start = vi.spyOn(remote, "start").mockImplementation(() => {});
  const stop = vi.spyOn(remote, "stop").mockImplementation(() => {});
  const reconnect = vi.spyOn(remote, "reconnectNow").mockImplementation(() => {});
  host = document.createElement("div"); document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<App />));
  expect(start).toHaveBeenCalledTimes(initiallyHidden ? 0 : 1);
  vi.useFakeTimers();
  const visibility = async (value: boolean) => {
    await act(async () => { hidden = value; document.dispatchEvent(new Event("visibilitychange")); });
  };
  return { remote, start, stop, reconnect, visibility, hide: (value: boolean) => { hidden = value; } };
}
it("keeps the connection through a short app switch and cancels the hidden deadline", async () => {
  const { start, stop, visibility } = await mount();
  await visibility(true);
  await vi.advanceTimersByTimeAsync(2_000); expect(stop).not.toHaveBeenCalled();
  await visibility(false); expect(start).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(60_000); expect(stop).not.toHaveBeenCalled();
});
it("stops after sixty hidden seconds without extending the deadline on duplicate events", async () => {
  const { stop, visibility } = await mount();
  await visibility(true); await vi.advanceTimersByTimeAsync(59_999);
  expect(stop).not.toHaveBeenCalled();
  await visibility(true); await vi.advanceTimersByTimeAsync(1);
  expect(stop).toHaveBeenCalledTimes(1);
});
it("stops synchronously on pagehide even within the hidden grace and resumes on pageshow", async () => {
  const { start, stop, visibility, hide } = await mount();
  await visibility(true);
  window.dispatchEvent(new PageTransitionEvent("pagehide", { persisted: true }));
  expect(stop).toHaveBeenCalledTimes(1);
  hide(false); window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
  expect(start).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(60_000); expect(stop).toHaveBeenCalledTimes(1);
});
it("stops synchronously for document freeze and starts again on document resume", async () => {
  const { start, stop, visibility, hide } = await mount();
  await visibility(true); document.dispatchEvent(new Event("freeze"));
  expect(stop).toHaveBeenCalledTimes(1);
  hide(false); document.dispatchEvent(new Event("resume"));
  expect(start).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(60_000); expect(stop).toHaveBeenCalledTimes(1);
});
it("checks the retained connection on all foreground events", async () => {
  const { start, stop } = await mount();
  window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
  document.dispatchEvent(new Event("resume"));
  expect(start).toHaveBeenCalledTimes(3); expect(stop).not.toHaveBeenCalled();
});
it("ignores network events after suspension and cancels timers and listeners on unmount", async () => {
  const { start, stop, reconnect, visibility } = await mount();
  await visibility(true); await vi.advanceTimersByTimeAsync(60_000);
  window.dispatchEvent(new Event("online")); expect(reconnect).not.toHaveBeenCalled();
  await visibility(false); expect(start).toHaveBeenCalledTimes(2);
  await visibility(true);
  await act(async () => root!.unmount()); root = undefined;
  const stopped = stop.mock.calls.length;
  window.dispatchEvent(new Event("pageshow")); document.dispatchEvent(new Event("resume"));
  document.dispatchEvent(new Event("freeze")); window.dispatchEvent(new Event("online"));
  await vi.advanceTimersByTimeAsync(60_000);
  expect(start).toHaveBeenCalledTimes(2); expect(stop).toHaveBeenCalledTimes(stopped);
  expect(reconnect).not.toHaveBeenCalled();
});
it("leaves an initially hidden page stopped until visible", async () => {
  const { start, visibility } = await mount(true);
  window.dispatchEvent(new Event("pageshow")); document.dispatchEvent(new Event("resume"));
  expect(start).not.toHaveBeenCalled();
  await visibility(false); expect(start).toHaveBeenCalledTimes(1);
});

it("uses the delayed waiting notice on the home page too", async () => {
  const { remote } = await mount();
  const waiting = vi.spyOn(remote, "showWaiting", "get").mockReturnValue(false);
  await act(async () => { remote.conn = "offline"; remote.everOnline = true; remote.listed = true; remote.touch(); });
  expect(host!.textContent).not.toMatch(/正在重连|重连中/);
  waiting.mockReturnValue(true);
  await act(async () => remote.touch());
  expect(host!.textContent).toMatch(/正在重连|重连中/);
});

it("uses the same waiting delay in the narrow home header", async () => {
  const { remote } = await mount();
  const waiting = vi.spyOn(remote, "showWaiting", "get").mockReturnValue(false);
  await act(async () => {
    remote.conn = "offline"; remote.everOnline = true; remote.listed = true;
    root!.render(<Home remote={remote} go={() => {}} />);
  });
  expect(host!.textContent).not.toContain("正在重连");
  waiting.mockReturnValue(true);
  await act(async () => remote.touch());
  expect(host!.textContent).toContain("正在重连");
});
