// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { t as message } from "./i18n";
import { getCoreHost } from "./core/host";
import { DemoRemote } from "./dev/demo";
import { patchSyncState } from "./test/remote-fixture";
import { Remote } from "./remote";
import * as securityClient from "./core/security-client";
import { SecurityBootstrapKind, SecurityWorkflowStatusSchema } from "./gen/remoteagent/core/v1/security_pb";
import { CommandResultSchema } from "./gen/remoteagent/v1/remote_pb";
import { run, task, type ScheduleTaskJson } from "./test/schedule-fixture";

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
let activeRemote: DemoRemote | undefined;
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  activeRemote?.stop();
  activeRemote = undefined;
  host?.remove();
  localStorage.clear();
  history.replaceState(null, "", "/");
  wide = false;
  vi.restoreAllMocks();
});

async function mount() {
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  vi.spyOn(securityClient, "createStandaloneSecurityClient").mockReturnValue({
    client: {
      bootstrap: vi.fn(async () => create(SecurityWorkflowStatusSchema, { bootstrap: SecurityBootstrapKind.READY, server: "https://ra.example.com", deviceId: "me", accountId: "acc", isMember: true })),
    } as unknown as securityClient.SecurityClient,
    dispose: vi.fn(),
  });
  const remote = DemoRemote.create(new URLSearchParams());
  activeRemote = remote;
  vi.spyOn(Remote, "load").mockResolvedValue(remote);
  const demoMachines = (remote as unknown as { demoMachines: Map<string, import("./test/remote-fixture").TestMachineView> }).demoMachines;
  const mac = demoMachines.get("mac")!;
  mac.meta!.capabilities!.scheduledTasks = true;
  let tasks: ScheduleTaskJson[] = [];
  let rev = 1n;
  mac.meta!.scheduleRevision = rev;
  const listCalls = vi.spyOn(remote, "machineCommand").mockImplementation(async (_mid, kind) => {
    if (kind?.case === "schedule" && kind.value?.operation === "list") {
      return create(CommandResultSchema, {
        kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify({ tasks })) } },
      });
    }
    return create(CommandResultSchema, {});
  });
  remote.start();
  patchSyncState(remote, { conn: "online", machines: new Map(demoMachines) });
  const coreHost = getCoreHost(remote);
  const refresh = async () => {
    const before = listCalls.mock.calls.filter(([, k]) => k?.case === "schedule" && k.value?.operation === "list").length;
    rev += 1n;
    coreHost.scheduleRefresh("mac");
    await vi.waitFor(() => {
      const after = listCalls.mock.calls.filter(([, k]) => k?.case === "schedule" && k.value?.operation === "list").length;
      expect(after).toBeGreaterThan(before);
    });
    await act(async () => { await Promise.resolve(); });
  };
  await refresh();
  tasks = [
    task({ id: "first", runs: [run("first", { notified: true, ended_at: "2026-11-01T09:00:00Z" })] }),
    task({ id: "second", runs: [run("second", { notified: true, ended_at: "2026-11-01T08:00:00Z" })] }),
  ];
  await refresh();
  host = document.createElement("div"); document.body.append(host);
  root = createRoot(host);
  history.replaceState(null, "", "/#/tasks");
  await act(async () => {
    root!.render(<App />);
    await import("./Workspace");
  });
  await vi.waitFor(() => expect(coreHost.viewSchedules().fresh.length).toBe(2));
  const taskHasUnread = (id: string) => {
    const vm = coreHost.viewSchedules();
    const row = [...vm.attention, ...vm.fresh, ...vm.others, ...vm.ended].find((x) => x.taskId === id);
    return !!row?.hasUnread;
  };
  return { remote, coreHost, tasks, refresh, taskHasUnread };
}

async function open(hash: string) {
  await act(async () => { location.hash = hash; window.dispatchEvent(new HashChangeEvent("hashchange")); });
  await act(async () => { await vi.dynamicImportSettled(); });
}

it("makes a task from the phone's bottom row", async () => {
  await mount();
  const make = [...host.querySelectorAll<HTMLButtonElement>(".bbar .fab")];
  expect(make.map((b) => b.textContent)).toEqual([message("home.new_schedule")]);
  expect(host.querySelector(".tabs .on")?.textContent).toContain(message("home.schedules_tab"));
  await act(async () => make[0].click());
  expect(location.hash).toBe("#/tasks/new");
});

it("keeps tab unread, shares the badge across widths, and reads only the selected wide detail", async () => {
  const { coreHost, taskHasUnread } = await mount();
  expect(coreHost.viewSchedules().hasNewResults).toBe(true);
  expect(host.querySelector('.tabs [aria-label="有新结果"]')).not.toBeNull();
  await act(async () => { wide = true; mediaListeners.forEach(listener => listener()); });
  // A wide window does not open a task by itself, so its notifications stay unread.
  expect(host.querySelector(".dm .schedule-unread-dot")).toBeNull();
  expect(host.querySelector(".side .schedule-selected")).toBeNull();
  expect(host.querySelector(".dm")?.textContent).toBe("选一个任务查看结果新定时");
  expect(host.querySelector(".dm textarea")).toBeNull();
  expect(coreHost.viewSchedules().hasNewResults).toBe(true);
  expect(host.querySelector('.sfoot [aria-label="有新结果"]')).not.toBeNull();
  await open("#/tasks/mac/first");
  expect(host.querySelector(".dm .schedules-page .navbar .title b")?.textContent).toBe(message("settings.check_for_updates"));
  expect(taskHasUnread("first")).toBe(false);
  expect(taskHasUnread("second")).toBe(true);
  expect(host.querySelector('.sfoot [aria-label="有新结果"]')).not.toBeNull();
  expect(host.querySelector(".dm .schedule-unread-dot")).not.toBeNull();
  await act(async () => { wide = false; mediaListeners.forEach(listener => listener()); });
  await open("#/tasks");
  expect(host.querySelector('.tabs [aria-label="有新结果"]')).not.toBeNull();
});

it("retains the selected wide detail when another task receives a newer notification", async () => {
  wide = true;
  const { tasks, refresh } = await mount();
  await open("#/tasks/mac/first");
  expect(host.querySelectorAll(".dm .schedule-unread-dot")).toHaveLength(1);
  tasks[1].runs!.push(run("newest", { notified: true, ended_at: "2026-11-01T10:00:00Z" }));
  await refresh();
  expect(host.querySelector(".side .schedule-selected .schedule-unread")).toBeNull();
  expect(host.querySelector('.side .sess [aria-label="有新结果"]')).not.toBeNull();
  expect(host.querySelectorAll(".dm .schedule-unread-dot")).toHaveLength(1);
});

it("leaves notifications unread while the open detail page is hidden", async () => {
  const { taskHasUnread, tasks, refresh } = await mount();
  await open("#/tasks/mac/first");
  const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(true);
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  tasks[0].runs!.push(run("while-hidden", { notified: true }));
  await refresh();
  expect(taskHasUnread("first")).toBe(true);
  hidden.mockReturnValue(false);
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  expect(taskHasUnread("first")).toBe(false);
  expect(host.querySelectorAll(".schedule-unread-dot")).toHaveLength(2);
});

it("keeps the actual mobile badge accessible when its visible label is capped", async () => {
  const { coreHost, tasks, refresh } = await mount();
  tasks[0].runs = Array.from({ length: 105 }, (_, i) => run(`notice-${i}`, { notified: true }));
  await refresh();
  expect(coreHost.viewSchedules().attentionCount).toBe(0);
  tasks.push(...Array.from({ length: 106 }, (_, i) => task({ id: `attention-${i}`, status: "failed" })));
  await refresh();
  expect(coreHost.viewSchedules().attentionCount).toBe(106);
  const badge = host.querySelector(".tabs .schedule-attention-badge")!;
  expect(badge.textContent).toBe("99+");
  expect(badge.getAttribute("aria-label")).toBe("106 件需要处理");
});

it("clears the wide selection when marking the open detail unread", async () => {
  wide = true;
  const { taskHasUnread } = await mount();
  await open("#/tasks/mac/first");
  await act(async () => host.querySelector<HTMLButtonElement>('.dm [aria-label="更多"]')!.click());
  const unread = [...host.querySelectorAll<HTMLButtonElement>(".schedule-menu button")].find(b => b.textContent === message("schedule.mark_as_unread"))!;
  await act(async () => unread.click());
  await vi.waitFor(() => expect(location.hash).toBe("#/tasks"));
  expect(host.querySelector(".side .schedule-selected")).toBeNull();
  expect(host.querySelector(".dm")?.textContent).toBe("选一个任务查看结果新定时");
  expect(taskHasUnread("first")).toBe(true);
});

it.each(["context", "keyboard"])("marking another task unread via %s preserves the current selection and reading tracker", async action => {
  wide = true;
  const { coreHost, tasks, refresh, taskHasUnread } = await mount();
  await open("#/tasks/mac/first");
  await act(async () => coreHost.scheduleMarkRead([{ machineId: "mac", taskId: "second" }], false));
  const other = host.querySelectorAll<HTMLElement>(".side .schedule-row-wrap")[1];
  if (action === "context") {
    await act(async () => other.querySelector(".schedule-line")!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true })));
    const buttons = host.querySelectorAll<HTMLButtonElement>(".schedule-menu .mi");
    expect(buttons[0].textContent).toBe(message("schedule.mark_as_unread"));
    expect(buttons[1].textContent).toBe(message("schedule.run_once_now"));
    await act(async () => buttons[0].click());
  } else await act(async () => other.querySelector<HTMLButtonElement>('.schedule-read-action[aria-label="标为未读"]')!.click());
  expect(location.hash).toBe("#/tasks/mac/first");
  expect(host.querySelector(".side .schedule-selected")).not.toBeNull();
  expect(taskHasUnread("second")).toBe(true);
  tasks[0].runs!.push(run("new-while-reading", { notified: true, ended_at: "2026-11-01T11:00:00Z" }));
  await refresh();
  expect(taskHasUnread("first")).toBe(false);
  expect(taskHasUnread("second")).toBe(true);
});

it("keeps a source-session result unread through the sidebar action and route handoff, then reads it on the next visit", async () => {
  wide = true;
  const { tasks, refresh, taskHasUnread } = await mount();
  const demoSessions = (activeRemote as unknown as { demoSessions: Map<string, import("./test/remote-fixture").TestSessionView> }).demoSessions;
  const source = { ...demoSessions.get("s-idle")!, info: { ...demoSessions.get("s-idle")!.info!, taskOwned: true, scheduledTaskId: tasks[0].id } };
  demoSessions.set("s-idle", source);
  patchSyncState(activeRemote!, { sessions: new Map(demoSessions) });
  tasks[0].spec.target = { type: "resume_session", session: source.id };
  tasks[0].runs![0].session_id = source.id;
  await refresh();
  await open("#/s/" + source.id);
  expect(taskHasUnread("first")).toBe(false);
  const mark = host.querySelector<HTMLButtonElement>('.side .schedule-selected .schedule-read-action[aria-label="标为未读"]')!;
  expect(mark).toBeTruthy();
  await act(async () => mark.click());
  await vi.waitFor(() => expect(location.hash).toBe("#/tasks"));
  // Another list response while on #/tasks must not reverse the unread state.
  await refresh();
  expect(taskHasUnread("first")).toBe(true);
  await open("#/s/" + source.id);
  expect(taskHasUnread("first")).toBe(false);
});
