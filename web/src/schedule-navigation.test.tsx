// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "./dev/demo";
import { Remote } from "./remote";
import * as storage from "./storage";
import * as authoritySetup from "./authority-setup";
import * as remoteModule from "./remote";
import { CommandResultSchema } from "./gen/remoteagent/v1/remote_pb";
import { task, run } from "./test/schedule-fixture";
import type { ScheduleTask } from "./schedules";

// A simulated media query for component tests, with no browser or GUI.
let wide = false;
const mediaListeners = new Set<() => void>();
vi.stubGlobal("matchMedia", (query: string) => ({
  get matches() { return query === "(min-width: 900px)" && wide; },
  addEventListener: (_type: string, listener: () => void) => mediaListeners.add(listener),
  removeEventListener: (_type: string, listener: () => void) => mediaListeners.delete(listener),
}));
const { App } = await import("./App");
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let host: HTMLDivElement;
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  host?.remove();
  localStorage.clear();
  history.replaceState(null, "", "/");
  wide = false;
  vi.restoreAllMocks();
});

async function mount() {
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  vi.spyOn(storage, "loadAccount").mockResolvedValue(undefined);
  vi.spyOn(authoritySetup, "authorityStartup").mockResolvedValue({ kind: "none" } as never);
  vi.spyOn(remoteModule, "resumeRegistration").mockResolvedValue();
  const remote = DemoRemote.create(new URLSearchParams());
  vi.spyOn(Remote, "load").mockResolvedValue(remote);
  vi.spyOn(remote, "start").mockImplementation(() => {});
  vi.spyOn(remote, "stop").mockImplementation(() => {});
  vi.spyOn(remote, "syncSchedules").mockImplementation(() => {});
  remote.conn = "online";
  remote.machines.get("mac")!.meta!.capabilities!.scheduledTasks = true;
  let tasks: ScheduleTask[] = [];
  vi.spyOn(remote, "machineCommand").mockImplementation(async () => create(CommandResultSchema, {
    kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify({ tasks })) } },
  }));
  await remote.refreshSchedules("mac");
  tasks = [task({ id: "first", runs: [run("first", { notified: true, ended_at: "2026-11-01T09:00:00Z" })] }), task({ id: "second", runs: [run("second", { notified: true, ended_at: "2026-11-01T08:00:00Z" })] })];
  await remote.refreshSchedules("mac");
  host = document.createElement("div"); document.body.append(host);
  root = createRoot(host);
  history.replaceState(null, "", "/#/tasks");
  await act(async () => root!.render(<App />));
  return { remote, tasks, refresh: () => remote.refreshSchedules("mac") };
}

async function open(hash: string) {
  await act(async () => { location.hash = hash; window.dispatchEvent(new HashChangeEvent("hashchange")); });
}

it("keeps tab unread, shares the badge across widths, and reads only the selected wide detail", async () => {
  const { remote } = await mount();
  expect(remote.scheduleBadge()).toBe(2);
  expect(host.querySelector(".tabbar .schedule-unread")?.textContent).toBe("2");
  await act(async () => { wide = true; mediaListeners.forEach(listener => listener()); });
  // A wide window does not open a task by itself, so its notifications stay unread.
  expect(host.querySelector(".dm .schedule-unread-dot")).toBeNull();
  expect(host.querySelector(".side .schedule-selected")).toBeNull();
  expect(remote.scheduleBadge()).toBe(2);
  expect(host.querySelector(".side-tabs .schedule-unread")?.textContent).toBe("2");
  await open("#/tasks/mac/first");
  expect(host.querySelector(".dm .schedules-page .navbar .title b")?.textContent).toBe("检查更新");
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[0]).size).toBe(0);
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[1]).size).toBe(1);
  expect(host.querySelector(".side-tabs .schedule-unread")?.textContent).toBe("1");
  expect(host.querySelector(".dm .schedule-unread-dot")).not.toBeNull();
  await act(async () => { wide = false; mediaListeners.forEach(listener => listener()); });
  await open("#/tasks");
  expect(host.querySelector(".tabbar .schedule-unread")?.textContent).toBe("1");
});

it("retains the selected wide detail when another task receives a newer notification", async () => {
  wide = true;
  const { remote, tasks, refresh } = await mount();
  await open("#/tasks/mac/first");
  expect(host.querySelectorAll(".dm .schedule-unread-dot")).toHaveLength(1);
  tasks[1].runs!.push(run("newest", { notified: true, ended_at: "2026-11-01T10:00:00Z" }));
  await act(async () => { await refresh(); });
  expect(remote.scheduleBadge()).toBe(2);
  expect(host.querySelector(".side .schedule-selected .schedule-unread")).toBeNull();
  expect(host.querySelector(".side .sess .schedule-unread")?.textContent).toBe("2");
  expect(host.querySelectorAll(".dm .schedule-unread-dot")).toHaveLength(1);
});

it("leaves notifications unread while the open detail page is hidden", async () => {
  const { remote, tasks, refresh } = await mount();
  await open("#/tasks/mac/first");
  const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(true);
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  tasks[0].runs!.push(run("while-hidden", { notified: true }));
  await act(async () => { await refresh(); });
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[0])).toEqual(new Set(["while-hidden"]));
  hidden.mockReturnValue(false);
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[0]).size).toBe(0);
  expect(host.querySelectorAll(".schedule-unread-dot")).toHaveLength(2);
});

it("keeps the actual mobile badge accessible when its visible label is capped", async () => {
  const { remote, tasks, refresh } = await mount();
  tasks[0].runs = Array.from({ length: 105 }, (_, i) => run(`notice-${i}`, { notified: true }));
  await act(async () => { await refresh(); });
  expect(remote.scheduleBadge()).toBe(106);
  const badge = host.querySelector(".tabbar .schedule-unread")!;
  expect(badge.textContent).toBe("99+");
  expect(badge.getAttribute("aria-label")).toBe("106 条待查看");
});
