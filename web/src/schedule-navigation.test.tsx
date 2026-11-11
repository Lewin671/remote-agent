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

it("makes a task from the phone's bottom row", async () => {
  await mount();
  const make = [...host.querySelectorAll<HTMLButtonElement>(".bbar .fab")];
  expect(make.map((b) => b.textContent)).toEqual(["新定时"]);
  expect(host.querySelector(".tabs .on")?.textContent).toContain("定时");
  await act(async () => make[0].click());
  expect(location.hash).toBe("#/tasks/new");
});

it("keeps tab unread, shares the badge across widths, and reads only the selected wide detail", async () => {
  const { remote } = await mount();
  expect(remote.scheduleNewResults()).toBe(true);
  expect(host.querySelector('.tabs [aria-label="有新结果"]')).not.toBeNull();
  await act(async () => { wide = true; mediaListeners.forEach(listener => listener()); });
  // A wide window does not open a task by itself, so its notifications stay unread.
  expect(host.querySelector(".dm .schedule-unread-dot")).toBeNull();
  expect(host.querySelector(".side .schedule-selected")).toBeNull();
  expect(host.querySelector(".dm")?.textContent).toBe("选一个任务查看结果新定时");
  expect(host.querySelector(".dm textarea")).toBeNull();
  expect(remote.scheduleNewResults()).toBe(true);
  expect(host.querySelector('.sfoot [aria-label="有新结果"]')).not.toBeNull();
  await open("#/tasks/mac/first");
  expect(host.querySelector(".dm .schedules-page .navbar .title b")?.textContent).toBe("检查更新");
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[0]).size).toBe(0);
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[1]).size).toBe(1);
  expect(host.querySelector('.sfoot [aria-label="有新结果"]')).not.toBeNull();
  expect(host.querySelector(".dm .schedule-unread-dot")).not.toBeNull();
  await act(async () => { wide = false; mediaListeners.forEach(listener => listener()); });
  await open("#/tasks");
  expect(host.querySelector('.tabs [aria-label="有新结果"]')).not.toBeNull();
});

it("retains the selected wide detail when another task receives a newer notification", async () => {
  wide = true;
  const { remote, tasks, refresh } = await mount();
  await open("#/tasks/mac/first");
  expect(host.querySelectorAll(".dm .schedule-unread-dot")).toHaveLength(1);
  tasks[1].runs!.push(run("newest", { notified: true, ended_at: "2026-11-01T10:00:00Z" }));
  await act(async () => { await refresh(); });
  expect(remote.scheduleNewResults()).toBe(true);
  expect(host.querySelector(".side .schedule-selected .schedule-unread")).toBeNull();
  expect(host.querySelector('.side .sess [aria-label="有新结果"]')).not.toBeNull();
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
  expect(remote.scheduleBadge()).toBe(0);
  tasks.push(...Array.from({ length: 106 }, (_, i) => task({ id: `attention-${i}`, status: "failed" })));
  await act(async () => { await refresh(); });
  expect(remote.scheduleBadge()).toBe(106);
  const badge = host.querySelector(".tabs .schedule-attention-badge")!;
  expect(badge.textContent).toBe("99+");
  expect(badge.getAttribute("aria-label")).toBe("106 件需要处理");
});

it("clears the wide selection when marking the open detail unread", async () => {
  wide = true;
  const { remote } = await mount();
  await open("#/tasks/mac/first");
  await act(async () => host.querySelector<HTMLButtonElement>('.dm [aria-label="更多"]')!.click());
  const unread = [...host.querySelectorAll<HTMLButtonElement>(".schedule-menu button")].find(b => b.textContent === "标为未读")!;
  await act(async () => unread.click());
  await open(location.hash);
  expect(host.querySelector(".side .schedule-selected")).toBeNull();
  expect(host.querySelector(".dm")?.textContent).toBe("选一个任务查看结果新定时");
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[0])).toEqual(new Set(["first"]));
});

it.each(["context", "keyboard"])("marking another task unread via %s preserves the current selection and reading tracker", async action => {
  wide = true;
  const { remote, tasks, refresh } = await mount();
  await open("#/tasks/mac/first");
  await act(async () => remote.markScheduleRead("mac", tasks[1]));
  const other = host.querySelectorAll<HTMLElement>(".side .schedule-row-wrap")[1];
  if (action === "context") {
    await act(async () => other.querySelector(".schedule-line")!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true })));
    const buttons = host.querySelectorAll<HTMLButtonElement>(".schedule-menu .mi");
    expect(buttons[0].textContent).toBe("标为未读");
    expect(buttons[1].textContent).toBe("立即运行一次");
    await act(async () => buttons[0].click());
  } else await act(async () => other.querySelector<HTMLButtonElement>('.schedule-read-action[aria-label="标为未读"]')!.click());
  expect(location.hash).toBe("#/tasks/mac/first");
  expect(host.querySelector(".side .schedule-selected")).not.toBeNull();
  expect(remote.scheduleUnread("mac", tasks[1])).toEqual(new Set(["second"]));
  tasks[0].runs!.push(run("new-while-reading", { notified: true, ended_at: "2026-11-01T11:00:00Z" }));
  await act(async () => { await refresh(); });
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[0]).size).toBe(0);
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[1])).toEqual(new Set(["second"]));
});

it("keeps a source-session result unread through the sidebar action and route handoff, then reads it on the next visit", async () => {
  wide = true;
  const { remote, tasks, refresh } = await mount();
  const source = remote.sessions.get("s-idle")!;
  source.info!.taskOwned = true; source.info!.scheduledTaskId = tasks[0].id;
  tasks[0].spec.target = { type: "resume_session", session: source.id };
  tasks[0].runs![0].session_id = source.id;
  await act(async () => { await refresh(); });
  await open("#/s/" + source.id);
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[0]).size).toBe(0);
  const mark = host.querySelector<HTMLButtonElement>('.side .schedule-selected .schedule-read-action[aria-label="标为未读"]')!;
  expect(mark).toBeTruthy();
  await act(async () => mark.click());
  await open(location.hash);
  expect(location.hash).toBe("#/tasks");
  // A late session binding or another list response must not reverse the action.
  await act(async () => { remote.setScheduleSession(source.id); await refresh(); });
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[0])).toEqual(new Set(["first"]));
  await open("#/s/" + source.id);
  expect(remote.scheduleUnread("mac", remote.schedules.get("mac")!.tasks[0]).size).toBe(0);
});
