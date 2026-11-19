// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { getCoreHost, type CoreHost } from "../core/host";
import { DemoRemote } from "../dev/demo";
import {
  CacheEventsLoadedEventSchema,
  PlainEventSchema,
} from "../gen/remoteagent/core/v1/app_pb";
import {
  ScheduleCacheLoadedEventSchema,
  ScheduleFormMode,
  ScheduleTimingEnd,
  ScheduleTimingKind,
} from "../gen/remoteagent/core/v1/schedules_view_pb";
import {
  SessionPendingTrayViewSchema,
  SessionTrayGroupKind,
  SessionTrayGroupViewSchema,
  SessionTrayItemViewSchema,
  SessionTrayRowActivity,
  SessionTrayTone,
} from "../gen/remoteagent/core/v1/session_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import { Agent, ApprovalMode, EventSchema, OriginSchema, SendMode } from "../gen/remoteagent/v1/event_pb";
import { CommandResultSchema, DefaultModeSource } from "../gen/remoteagent/v1/remote_pb";
import { t as message } from "../i18n";
import { CommandError } from "../remote";
import { patchSyncState } from "../test/remote-fixture";
import { run, task, type ScheduleTaskJson } from "../test/schedule-fixture";
import { Sidebar } from "./Home";
import { PendingTray } from "./Queue";
import {
  ScheduleCreate,
  ScheduleDetail,
  ScheduleStrip,
  SchedulesPage,
  TimingSheet,
} from "./Schedules";
import { SessionPage } from "./Session";

const fsModule = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fsModule)) as {
  readFileSync(path: string, encoding: string): string;
};
const styles = readFileSync("src/styles.css", "utf8");

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let host: HTMLDivElement;
const remotes: DemoRemote[] = [];

interface TestCtrl {
  core: CoreHost;
  byMachine: Map<string, ScheduleTaskJson[]>;
  calls: Array<{ machineId: string; kind: Parameters<DemoRemote["machineCommand"]>[1] }>;
}
const ctrls = new WeakMap<DemoRemote, TestCtrl>();

afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  host?.remove();
  for (const r of remotes.splice(0)) r.dispose();
  vi.restoreAllMocks();
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
  localStorage.clear();
  delete document.documentElement.dataset.theme;
});

async function settle() {
  await act(async () => {
    for (let i = 0; i < 8; i++) await Promise.resolve();
  });
}

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
  await settle();
}

function encodeScheduleResult(payload: unknown) {
  return create(CommandResultSchema, {
    kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify(payload)) } },
  });
}

function enableScheduleOnMac(r: DemoRemote) {
  const demoMac = (r as unknown as {
    demoMachines: Map<string, { meta?: { capabilities?: { scheduledTasks?: boolean }; scheduleRevision?: bigint } }>;
  }).demoMachines.get("mac");
  if (demoMac?.meta) {
    demoMac.meta.capabilities = {
      ...demoMac.meta.capabilities,
      scheduledTasks: true,
    } as typeof demoMac.meta.capabilities;
    demoMac.meta.scheduleRevision = 1n;
  }
  const machines = new Map(r.machines);
  const mac = machines.get("mac");
  if (mac?.meta) {
    mac.meta = {
      ...mac.meta,
      scheduleRevision: 1n,
      capabilities: { ...mac.meta.capabilities, scheduledTasks: true } as typeof mac.meta.capabilities,
    };
    machines.set("mac", { ...mac });
  }
  return machines;
}

function remote(
  tasks: ScheduleTaskJson[] = [task()],
  opts: { cached?: ScheduleTaskJson[]; listError?: boolean } = {},
) {
  const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
  remotes.push(r);
  const byMachine = new Map<string, ScheduleTaskJson[]>([["mac", tasks]]);
  const calls: TestCtrl["calls"] = [];
  vi.spyOn(r, "machineCommand").mockImplementation(async (machineId, kind) => {
    calls.push({ machineId, kind });
    const list = byMachine.get(machineId) ?? [];
    if (kind?.case !== "schedule" || !kind.value) return create(CommandResultSchema, {});
    const req = kind.value;
    if (req.operation === "list") {
      if (opts.listError) throw new CommandError("unavailable", "读取失败");
      return encodeScheduleResult({ tasks: list });
    }
    if (req.operation === "create") {
      const spec = JSON.parse(new TextDecoder().decode(req.specJson));
      const created = task({ id: `task-new-${list.length + 1}`, spec, status: "scheduled" });
      byMachine.set(machineId, [created, ...list]);
      return encodeScheduleResult(created);
    }
    const existing = list.find((t) => t.id === req.taskId) ?? task({ id: req.taskId });
    const updated: ScheduleTaskJson = {
      ...existing,
      revision: existing.revision + 1,
      ...(req.operation === "update"
        ? { spec: JSON.parse(new TextDecoder().decode(req.specJson)), status: "scheduled", reason: undefined }
        : req.operation === "pause"
          ? { status: "paused", reason: "user_paused" }
          : req.operation === "resume"
            ? { status: "scheduled", reason: undefined }
            : req.operation === "cancel"
              ? { status: "cancelled", reason: undefined }
              : req.operation === "run"
                ? { manual_request: { id: "manual" } }
                : {}),
    };
    byMachine.set(
      machineId,
      list.some((t) => t.id === updated.id) ? list.map((t) => (t.id === updated.id ? updated : t)) : [updated],
    );
    return encodeScheduleResult(updated);
  });
  vi.spyOn(r, "readScheduleCache").mockImplementation(async (opId, machineId) =>
    create(ScheduleCacheLoadedEventSchema, {
      opId,
      machineId,
      ...(opts.cached && machineId === "mac"
        ? { resultJson: new TextEncoder().encode(JSON.stringify({ tasks: opts.cached })) }
        : {}),
    }),
  );
  r.start();
  const machines = enableScheduleOnMac(r);
  patchSyncState(r, { conn: "online", listed: true, machines });
  const core = getCoreHost(r);
  ctrls.set(r, { core, byMachine, calls });
  return r;
}

async function setTasks(r: DemoRemote, tasks: ScheduleTaskJson[], machineId = "mac") {
  const ctrl = ctrls.get(r)!;
  ctrl.byMachine.set(machineId, tasks);
  ctrl.core.scheduleRefresh(machineId);
  await settle();
}

function taskHasUnread(core: CoreHost, taskId: string) {
  const vm = core.viewSchedules();
  const item = [...vm.attention, ...vm.fresh, ...vm.others, ...vm.ended].find((x) => x.taskId === taskId);
  return !!item?.hasUnread;
}

function scheduleCalls(r: DemoRemote, op?: string) {
  return ctrls
    .get(r)!
    .calls.filter(
      (c) => c.kind?.case === "schedule" && (!op || c.kind.value?.operation === op),
    ) as Array<{ machineId: string; kind: { case: "schedule"; value: { operation: string; taskId: string; sessionId: string; specJson: Uint8Array; expectedRevision: bigint } } }>;
}

function button(text: string | RegExp) {
  return [...host.querySelectorAll("button")].find((b) =>
    typeof text === "string" ? b.textContent === text : text.test(b.textContent ?? ""),
  );
}

async function click(text: string | RegExp) {
  const targets = [
    message("schedule.delete_task"),
    message("schedule.run_once_now"),
    message("schedule.pause"),
    message("schedule.resume"),
    message("schedule.mark_as_unread"),
    message("schedule.mark_as_read"),
  ];
  if (!button(text) && targets.includes(String(text))) {
    const more = host.querySelector<HTMLButtonElement>('[aria-label="更多"]');
    if (more) await act(async () => more.click());
  }
  const b = button(text);
  expect(b, `Missing button matching ${String(text)}`).toBeTruthy();
  await act(async () => b!.click());
  await settle();
}

async function input(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype,
    "value",
  )!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

it("renders attention first, combines machines and keeps cached tasks read-only offline", async () => {
  const r = remote([
    task(),
    task({ id: "bad", status: "failed", reason: "relay_session_limit", spec: { ...task().spec, title: "失败任务" } }),
    task({ id: "deleted", status: "cancelled" }),
  ]);
  const go = vi.fn();
  await mount(<SchedulesPage remote={r} go={go} />);
  expect(host.querySelector(".hl")?.textContent).toBe("1 个任务停下了");
  expect(host.textContent!.indexOf("失败任务")).toBeLessThan(host.textContent!.indexOf(message("schedule.other_tasks")));
  expect(host.querySelector(".rc.warn")?.textContent).toContain("失败任务");
  expect(host.querySelectorAll(".schedule-line")).toHaveLength(2);
  await click(/失败任务/);
  expect(go).toHaveBeenCalledWith("tasks/mac/bad");
  await act(async () => {
    patchSyncState(r, {
      machines: new Map([...r.machines].map(([id, m]) => [id, id === "mac" ? { ...m, online: false } : m])),
    });
  });
  await settle();
  expect(host.textContent).toContain("未连接，显示上次状态");
  expect(host.querySelectorAll(".schedule-line.off")).toHaveLength(2);
  await act(async () => root!.render(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={go} onBack={() => {}} />));
  await settle();
  await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="更多"]')!.click());
  expect(button(message("schedule.run_once_now"))?.disabled).toBe(true);
  expect(button(message("schedule.delete_task"))?.disabled).toBe(true);
  await act(async () => host.querySelector<HTMLDivElement>(".dim")?.click());
});

it("confirms deletion, preserves conversations and uses the displayed revision", async () => {
  const r = remote();
  const go = vi.fn();
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={go} onBack={() => {}} />);
  await click(message("schedule.delete_task"));
  expect(scheduleCalls(r, "cancel")).toHaveLength(0);
  expect(host.querySelector('[role="alertdialog"]')?.textContent).toContain(
    message("schedule.stops_automatic_runs_existing_conversations_remain"),
  );
  await click(message("schedule.delete"));
  const cancels = scheduleCalls(r, "cancel");
  expect(cancels).toHaveLength(1);
  expect(cancels[0].kind.value.expectedRevision).toBe(1n);
  expect(go).toHaveBeenCalledWith("tasks");
});

it("folds quiet history by day and opens the exact selected execution", async () => {
  const runs = [
    run("one"),
    run("two", { started_at: "2026-10-24T11:00:00Z" }),
    run("notified", { notified: true, notification_text: "有新结果", started_at: "2026-10-23T11:00:00Z" }),
  ];
  const r = remote([task({ runs })]);
  const go = vi.fn();
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={go} onBack={() => {}} />);
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(1);
  await click(/2 次，都没有通知/);
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(2);
  await act(async () => host.querySelectorAll<HTMLButtonElement>(".schedule-run")[1].click());
  await settle();
  expect(go).toHaveBeenCalledWith("s/session-one");
  expect(getCoreHost(r).getScheduleFocus("session-one")).toEqual({ runId: "one", seq: 3n });
});

it("makes resume the only solid page action when a task needs attention", async () => {
  const r = remote([task({ status: "failed", reason: "relay_session_limit" })]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  expect(host.textContent).toContain(message("schedule.the_relay_s_session_limit_has_been_reached"));
  expect([...host.querySelectorAll(".pri")].map((b) => b.textContent)).toEqual([message("schedule.resume")]);
  await click(message("schedule.resume"));
  expect(scheduleCalls(r, "resume")).toHaveLength(1);
});

it("edits calendar time, weekdays, limits and the IANA timezone", async () => {
  const r = remote([
    task({
      spec: {
        ...task().spec,
        trigger: { type: "calendar", time: "08:00", timezone: "Asia/Shanghai" },
      },
    }),
  ]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  await click(/^什么时候/);
  await click(message("schedule.weekly_on"));
  await click("周五");
  const select = host.querySelector("select")!;
  await act(async () => {
    select.value = String(ScheduleTimingEnd.COUNT);
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await input(host.querySelector('input[type="number"]')!, "6");
  await click(message("common.done"));
  const updates = scheduleCalls(r, "update");
  expect(updates).toHaveLength(1);
  const spec = JSON.parse(new TextDecoder().decode(updates[0].kind.value.specJson));
  expect(spec.trigger).toEqual({
    type: "calendar",
    time: "08:00",
    timezone: "Asia/Shanghai",
    weekdays: [1, 5],
    max_runs: 6,
  });
});

it("creates a current-conversation task with message, time and an optional name", async () => {
  const r = remote([]);
  await mount(<ScheduleCreate remote={r} sessionId="s-idle" go={() => {}} onBack={() => {}} />);
  expect(host.textContent).not.toContain(message("schedule.computer_and_model"));
  await input(host.querySelector("textarea")!, "检查新回复，有结果通知我");
  await click(message("schedule.create"));
  const creates = scheduleCalls(r, "create");
  expect(creates).toHaveLength(1);
  const spec = JSON.parse(new TextDecoder().decode(creates[0].kind.value.specJson));
  expect(spec.target).toEqual({ type: "resume_session", session: "s-idle" });
});

it("offers the scheduled-send menu and links the conversation strip to its task", async () => {
  const t = task();
  t.spec.target = { type: "resume_session", session: "s-idle" };
  const r = remote([t]);
  const go = vi.fn();
  await mount(<ScheduleStrip remote={r} sessionId="s-idle" go={go} />);
  await click(/定时 · 每天/);
  expect(go).toHaveBeenCalledWith("tasks/mac/task-1");
  await act(async () => root!.render(<SessionPage remote={r} sessionId="s-idle" wide={false} onBack={() => {}} />));
  await settle();
  await act(async () => host.querySelector<HTMLButtonElement>(".navbar .more")!.click());
  await click(message("session.schedule_send"));
  expect(host.querySelector(".schedule-session-overlay")?.textContent).toContain(
    message("schedule.schedule_messages_to_this_conversation"),
  );
});

it("preserves message and timing drafts when a viewport layout remounts the form", async () => {
  const r = remote([]);
  const props = { remote: r, go: () => {}, onBack: () => {} };
  await mount(<ScheduleCreate {...props} />);
  await input(host.querySelector("textarea")!, "保留这条消息");
  await click(/^什么时候/);
  await click(message("schedule.every"));
  await input(host.querySelector('input[type="number"]')!, "15");
  await click(message("common.done"));
  await act(async () => root!.render(<div />));
  await act(async () => root!.render(<ScheduleCreate {...props} />));
  await settle();
  expect(host.querySelector("textarea")?.value).toBe("保留这条消息");
  expect(host.textContent).toContain("每 15 小时");
});

it("keeps a delivery cancellation visible and re-arms the schedule explicitly", async () => {
  const r = remote([task({ status: "cancelled", reason: "message_cancelled" })]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  expect(host.textContent).toContain("送达前被取消");
  await click(message("schedule.resume"));
  const updates = scheduleCalls(r, "update");
  expect(updates).toHaveLength(1);
  expect(updates[0].kind.value.expectedRevision).toBe(1n);
});

it("edits a task's run settings without replacing the project's defaults", async () => {
  const r = remote();
  const saved = r.projectPrefs();
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  await click(/^在哪运行/);
  expect(host.querySelector('[role="dialog"]')?.textContent).toContain("只修改这个任务");
  await click(message("common.save"));
  const updates = scheduleCalls(r, "update");
  expect(updates).toHaveLength(1);
  const spec = JSON.parse(new TextDecoder().decode(updates[0].kind.value.specJson));
  expect(spec.target).toMatchObject({ cwd: "/example/project", mode: "auto", agent: "claude" });
  expect(r.projectPrefs()).toEqual(saved);
});

it("creates a new-conversation task without borrowing a source session", async () => {
  const r = remote([]);
  const sourceSession = r.sessions.get("s-idle")!;
  patchSyncState(r, {
    sessions: new Map([[sourceSession.id, sourceSession]]),
    machines: enableScheduleOnMac(r),
  });
  await mount(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />);
  await input(host.querySelector("textarea")!, "新建来源测试");
  expect(button(message("schedule.create"))?.disabled, host.textContent ?? "").toBe(false);
  await click(message("schedule.create"));
  if (host.querySelector('[role="alertdialog"]')) await click(message("modes.full_access"));
  const creates = scheduleCalls(r, "create");
  expect(creates).toHaveLength(1);
  expect(creates[0].kind.value.sessionId).toBe("");
  const spec = JSON.parse(new TextDecoder().decode(creates[0].kind.value.specJson));
  expect(spec.target.cwd).toBe(sourceSession.info?.cwd);
  expect(spec.target.type).toBe("new_session");
});

it("creates with an optional name after the message and preserves that name in the draft", async () => {
  const r = remote([]);
  const props = { remote: r, sessionId: "s-idle", go: () => {}, onBack: () => {} };
  await mount(<ScheduleCreate {...props} />);
  const name = host.querySelector<HTMLInputElement>('input[aria-label="任务名"]')!;
  expect(name.placeholder).toBe(message("schedule.leave_blank_to_use_the_message_s_first_sentence"));
  expect(
    [...host.querySelectorAll(".schedule-body > label, .schedule-body label")]
      .slice(0, 2)
      .map((el) => el.textContent),
  ).toEqual([message("composer.message"), message("schedule.task_name")]);
  await input(name, "每小时看 PR");
  await input(host.querySelector("textarea")!, "检查新回复。只在有变化时通知我。");
  await act(async () => root!.render(null));
  await act(async () => root!.render(<ScheduleCreate {...props} />));
  await settle();
  expect(host.querySelector<HTMLInputElement>('input[aria-label="任务名"]')?.value).toBe("每小时看 PR");
  await click(message("schedule.create"));
  const spec = JSON.parse(new TextDecoder().decode(scheduleCalls(r, "create")[0].kind.value.specJson));
  expect(spec.title).toBe("每小时看 PR");
});

it("edits the task name with selection and keeps the other settings", async () => {
  const r = remote();
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  await click(/^任务名/);
  const dialog = host.querySelector('[aria-label="重命名任务"]')!;
  const name = dialog.querySelector<HTMLInputElement>('input[aria-label="任务名"]')!;
  expect(document.activeElement).toBe(name);
  expect(name.selectionStart).toBe(0);
  expect(name.selectionEnd).toBe(name.value.length);
  await input(name, "项目巡检");
  await click(message("common.save"));
  const updates = scheduleCalls(r, "update");
  expect(updates).toHaveLength(1);
  expect(JSON.parse(new TextDecoder().decode(updates[0].kind.value.specJson))).toEqual({
    ...task().spec,
    title: "项目巡检",
  });
  expect(host.querySelector(".navbar .title b")?.textContent).toBe("项目巡检");
  await click(/^任务名/);
  await input(host.querySelector<HTMLInputElement>('[aria-label="重命名任务"] input')!, "");
  await click(message("common.save"));
  expect(JSON.parse(new TextDecoder().decode(scheduleCalls(r, "update")[1].kind.value.specJson)).title).toBe(
    task().spec.message,
  );
  // Closing unchanged name sends no update
  await click(/^任务名/);
  await click(message("common.save"));
  expect(scheduleCalls(r, "update")).toHaveLength(2);
  expect(host.querySelector('[aria-label="重命名任务"]')).toBeNull();
});

it("keeps timezone inside the timing sheet and appends only foreign zones to its value", async () => {
  const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Shanghai";
  const otherZone = localZone === "Asia/Tokyo" ? "UTC" : "Asia/Tokyo";
  const r = remote([
    task({
      spec: {
        ...task().spec,
        trigger: { type: "calendar", time: "08:00", timezone: otherZone },
      },
    }),
  ]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  const timingValue = () => button(/^什么时候/)?.querySelector(".v")?.textContent ?? "";
  expect(timingValue()).toContain(otherZone === "UTC" ? "UTC" : "东京时间");
  await click(/^什么时候/);
  await input(host.querySelector<HTMLInputElement>('input[aria-label="时区"]')!, localZone);
  await click(message("common.done"));
  expect(timingValue()).not.toContain(localZone);
});

it("uses the shared multiple-task strip wording", async () => {
  const r = remote([
    task({ id: "t1", spec: { ...task().spec, title: "第一个任务", target: { type: "resume_session", session: "s-idle" } } }),
    task({ id: "t2", spec: { ...task().spec, title: "第二个任务", target: { type: "resume_session", session: "s-idle" } } }),
  ]);
  const go = vi.fn();
  await mount(<ScheduleStrip remote={r} sessionId="s-idle" go={go} />);
  expect(host.textContent).toContain("2 个定时");
  await click(/2 个定时/);
  expect(host.querySelectorAll(".schedule-choices .schedule-line")).toHaveLength(2);
  await click(/第二个任务/);
  expect(go).toHaveBeenCalledWith("tasks/mac/t2");
});

it("keeps plan permission visible and disables scheduled creation", async () => {
  const r = remote([]);
  patchSyncState(r, { sessions: new Map() });
  const storedPrefs = (r as unknown as {
    stored: { prefs: { settings: Array<{ agents: Array<{ mode: ApprovalMode }> }> } };
  }).stored.prefs;
  const demoMachines = (r as unknown as {
    demoMachines: Map<string, { meta?: { agents: Array<{ defaultMode: ApprovalMode }> } }>;
  }).demoMachines;
  for (const m of demoMachines.values()) for (const agent of m.meta?.agents ?? []) agent.defaultMode = ApprovalMode.PLAN;
  for (const project of storedPrefs.settings) for (const agent of project.agents) agent.mode = ApprovalMode.PLAN;
  r.syncDemoToCore();
  await mount(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />);
  await input(host.querySelector("textarea")!, "检查更新");
  expect(button(message("schedule.create"))?.disabled).toBe(true);
  expect(host.textContent).toContain("计划模式不能创建执行任务");
  expect(scheduleCalls(r, "create")).toHaveLength(0);
});

it("uses the shared new-task explanation in the actual creation page", async () => {
  const r = remote([]);
  for (const m of r.machines.values()) if (m.meta) m.meta.agents = r.machines.get("mac")!.meta!.agents;
  await mount(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />);
  expect(host.querySelector(".schedule-body > .gf:last-child")?.textContent).toBe(
    "每次运行新开一个对话，归在这个任务下面，不进会话列表。 完成后默认通知；关闭后仅由 agent 主动通知。",
  );
});

it("shows CLI as plain text on a single-CLI computer and segmented when multiple CLIs are installed", async () => {
  const r = remote([task()]);
  const idle = r.sessions.get("s-idle")!;
  patchSyncState(r, { sessions: new Map([["s-idle", idle]]) });
  await mount(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />);
  expect(host.querySelector(".schedule-field .seg")).not.toBeNull();
  await act(async () => {
    const mac = r.machines.get("mac")!;
    patchSyncState(r, {
      machines: new Map([
        ...r.machines,
        [
          "mac",
          {
            ...mac,
            meta: {
              ...mac.meta!,
              agents: mac.meta!.agents.filter((a) => a.agent === Agent.CODEX),
            },
          },
        ],
      ]),
    });
  });
  await settle();
  expect(host.querySelector(".schedule-field .seg")).toBeNull();
  const cliField = [...host.querySelectorAll(".schedule-field")].find(
    (el) => el.firstElementChild?.textContent === message("new_session.cli_picker_label"),
  );
  expect(cliField?.querySelector(".v")?.textContent).toBe("Codex");
});

it("closes a stale editor when the daemon reports a revision conflict", async () => {
  const r = remote([task()]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  await click(/^编辑$/);
  const orig = vi.mocked(r.machineCommand).getMockImplementation()!;
  vi.spyOn(r, "machineCommand").mockImplementation(async (mid, kind, slot) => {
    if (kind?.case === "schedule" && kind.value?.operation === "update") {
      throw new CommandError(
        "conflict",
        message("errors.the_task_was_just_changed_updated_to_the_latest"),
        true,
        false,
        "conflict",
        "business",
      );
    }
    return orig(mid, kind, slot);
  });
  await input(host.querySelector("textarea")!, "新消息");
  await click(message("common.save"));
  expect(host.querySelector('[role="dialog"]')).toBeNull();
  expect(host.textContent).toContain(message("errors.the_task_was_just_changed_updated_to_the_latest"));
});

it.each([31, 62, 123])("preserves an unchanged %i-second interval and validates < 1s", async (seconds) => {
  const t = task({
    spec: {
      ...task().spec,
      trigger: {
        type: "interval",
        interval_seconds: seconds,
        delay_seconds: 29,
        at: "2026-10-01T08:00:00Z",
      },
    },
  });
  const r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  await click(/^什么时候/);
  await click(message("common.done"));
  const updates1 = scheduleCalls(r, "update");
  expect(updates1).toHaveLength(1);
  expect(JSON.parse(new TextDecoder().decode(updates1[0].kind.value.specJson)).trigger).toEqual(t.spec.trigger);

  await click(/^什么时候/);
  await input(host.querySelector<HTMLInputElement>('input[type="number"]')!, String(seconds + 60));
  await click(message("common.done"));
  const updates2 = scheduleCalls(r, "update");
  expect(JSON.parse(new TextDecoder().decode(updates2[1].kind.value.specJson)).trigger).toEqual({
    type: "interval",
    interval_seconds: seconds + 60,
    delay_seconds: seconds + 60,
  });

  await click(/^什么时候/);
  await input(host.querySelector<HTMLInputElement>('input[type="number"]')!, "0");
  await click(message("common.done"));
  expect(scheduleCalls(r, "update")).toHaveLength(2);
  expect(host.textContent).toContain(message("schedule.ios_enter_a_valid_interval"));
});

it("converts cutoff display across zones while retaining the absolute instant", async () => {
  const trigger = { type: "calendar" as const, time: "08:00", timezone: "Asia/Shanghai", until: "2027-10-30T00:15:29Z" };
  const r = remote([task({ spec: { ...task().spec, trigger } })]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  await click(/^什么时候/);
  const cutoff = () => host.querySelector<HTMLInputElement>('input[type="datetime-local"]')!;
  const zone = host.querySelector<HTMLInputElement>('input[aria-label="时区"]')!;
  expect(cutoff().value).toBe("2027-10-30T08:15");
  await input(zone, "America/Los_Angeles");
  expect(cutoff().value).toBe("2027-10-29T17:15");
  await click(message("common.done"));
  expect(JSON.parse(new TextDecoder().decode(scheduleCalls(r, "update")[0].kind.value.specJson)).trigger.until).toBe(
    trigger.until,
  );

  await click(/^什么时候/);
  const cutoff2 = () => host.querySelector<HTMLInputElement>('input[type="datetime-local"]')!;
  const zone2 = host.querySelector<HTMLInputElement>('input[aria-label="时区"]')!;
  await input(zone2, "Asia/Tokyo");
  expect(cutoff2().value).toBe("2027-10-30T09:15");
  await input(cutoff2(), "2027-11-01T09:30");
  await input(zone2, "UTC");
  expect(cutoff2().value).toBe("2027-11-01T00:30");
  await click(message("common.done"));
  expect(JSON.parse(new TextDecoder().decode(scheduleCalls(r, "update")[1].kind.value.specJson)).trigger.until).toBe(
    "2027-11-01T00:30:00Z",
  );
});

it("keeps an edited cutoff instant when a zone change lands it in a repeated wall time", async () => {
  const r = remote([
    task({
      spec: {
        ...task().spec,
        trigger: { type: "calendar", time: "08:00", timezone: "UTC", until: "2027-10-30T00:00:00Z" },
      },
    }),
  ]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  await click(/^什么时候/);
  const cutoff = () => host.querySelector<HTMLInputElement>('input[type="datetime-local"]')!;
  await input(cutoff(), "2027-11-07T06:30");
  await input(host.querySelector<HTMLInputElement>('input[aria-label="时区"]')!, "America/New_York");
  expect(cutoff().value).toBe("2027-11-07T01:30");
  await click(message("common.done"));
  expect(JSON.parse(new TextDecoder().decode(scheduleCalls(r, "update")[0].kind.value.specJson)).trigger.until).toBe(
    "2027-11-07T06:30:00Z",
  );
});

it.each(["at", "cutoff"])("rejects a DST gap and saves the first repeated time for %s", async (field) => {
  const trigger =
    field === "at"
      ? { type: "at" as const, at: "2027-01-01T12:00:00Z" }
      : { type: "calendar" as const, time: "08:00", timezone: "America/Los_Angeles", until: "2027-01-01T12:00:00Z" };
  const r = remote([task({ spec: { ...task().spec, trigger } })]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  await click(/^什么时候/);
  const zone = host.querySelector<HTMLInputElement>('input[aria-label="时区"]')!;
  await input(zone, "America/Los_Angeles");
  const local = host.querySelector<HTMLInputElement>('input[type="datetime-local"]')!;
  await input(local, "2027-03-14T02:30");
  await click(message("common.done"));
  expect(scheduleCalls(r, "update")).toHaveLength(0);
  expect(host.textContent).toContain(message("schedule.daylight_saving_time_skips_this_time_in_the_selected"));
  await input(zone, "Europe/Berlin");
  await input(local, "2027-10-31T02:30");
  await click(message("common.done"));
  const savedTrigger = JSON.parse(new TextDecoder().decode(scheduleCalls(r, "update")[0].kind.value.specJson)).trigger;
  expect(savedTrigger[field === "at" ? "at" : "until"]).toBe("2027-10-31T00:30:00Z");
});

it("creates with the new computer's displayed defaults after switching without editing", async () => {
  const r = remote([]);
  const old = { ...r.machines.get("mac")! };
  const next = structuredClone(old);
  old.meta = {
    ...old.meta!,
    agents: old.meta!.agents
      .filter((a) => a.agent === Agent.CODEX)
      .map((a) => ({ ...a, defaultMode: ApprovalMode.FULL_ACCESS, defaultModeSource: DefaultModeSource.CLI_CONFIG })),
  };
  next.id = "new";
  next.meta!.hostname = "新电脑";
  next.meta!.agents = [{ ...old.meta!.agents[0], agent: Agent.CLAUDE, defaultMode: ApprovalMode.ASK }];
  const source = r.sessions.get("s-idle")!;
  const path = source.info!.cwd;
  const another = {
    ...source,
    id: "new-session",
    machineId: "new",
    info: { ...source.info!, id: "new-session", agent: Agent.CLAUDE },
  };
  patchSyncState(r, {
    machines: new Map([
      ["mac", old],
      ["new", next],
    ]),
    sessions: new Map([
      [source.id, source],
      [another.id, another],
    ]),
  });
  const stored = (r as unknown as {
    stored: { prefs: { settings: Array<{ path: string; machineId: string; agent: Agent; agents: Array<Record<string, unknown>> }> } };
  }).stored.prefs.settings[0];
  stored.path = path;
  stored.machineId = "mac";
  stored.agent = Agent.CODEX;
  stored.agents = [{ ...stored.agents[0], agent: Agent.CODEX, model: "gpt-6-astra", effort: "high", mode: ApprovalMode.FULL_ACCESS }];
  r.syncDemoToCore();
  patchSyncState(r, {
    machines: new Map([
      ["mac", old],
      ["new", next],
    ]),
    sessions: new Map([
      [source.id, source],
      [another.id, another],
    ]),
  });
  await mount(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />);
  await input(host.querySelector("textarea")!, "检查回复");
  await click(/^电脑和模型/);
  const machineRow = [...host.querySelectorAll<HTMLButtonElement>("button")].find(
    (b) => b.querySelector(".mt")?.textContent === message("common.machine"),
  );
  expect(machineRow, host.textContent ?? "").toBeTruthy();
  await act(async () => machineRow!.click());
  expect(button(/新电脑/), host.textContent ?? "").toBeTruthy();
  await click(/新电脑/);
  await click(message("common.done"));
  expect(button(/^电脑和模型/)?.textContent).toContain("新电脑");
  expect(button(/^电脑和模型/)?.textContent).toContain(message("modes.ask"));
  expect(button(/^电脑和模型/)?.textContent).not.toContain("gpt-6-astra");
  await click(message("schedule.create"));
  const creates = scheduleCalls(r, "create");
  expect(creates).toHaveLength(1);
  expect(creates[0].machineId).toBe("new");
  const spec = JSON.parse(new TextDecoder().decode(creates[0].kind.value.specJson));
  expect(spec.target).toMatchObject({ agent: "claude", mode: "ask", cwd: path });
  expect(spec.target.model ?? "").toBe("");
  expect(spec.target.effort ?? "").toBe("");
});

it("keeps unread on opening the list, reads only the detail, and keeps entry dots for this visit", async () => {
  const r = remote([]);
  const core = getCoreHost(r);
  await setTasks(r, []);
  await setTasks(r, [
    task({ runs: [run("new-notice", { notified: true })] }),
    task({ id: "other", runs: [run("other-notice", { notified: true })] }),
  ]);
  expect(core.viewSchedules().hasNewResults).toBe(true);
  await mount(<SchedulesPage remote={r} go={() => {}} />);
  expect(core.viewSchedules().hasNewResults).toBe(true);
  expect(host.querySelectorAll(".schedule-line.unread")).toHaveLength(2);
  expect(host.querySelector(".hl")?.textContent).toBe("2 个新结果");
  await setTasks(r, [
    task({ runs: [run("new-notice", { notified: true }), run("while-list-open", { notified: true })] }),
    task({ id: "other", runs: [run("other-notice", { notified: true })] }),
  ]);
  expect(host.querySelectorAll(".schedule-line.unread")).toHaveLength(2);
  await act(async () => root!.render(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />));
  await settle();
  expect(core.viewSchedules().hasNewResults).toBe(true);
  expect(host.querySelectorAll(".schedule-unread-dot")).toHaveLength(2);
  await act(async () => root!.render(<SchedulesPage remote={r} go={() => {}} />));
  await settle();
  await setTasks(r, [
    task({
      runs: [
        run("new-notice", { notified: true }),
        run("while-list-open", { notified: true }),
        run("after-leaving", { notified: true }),
      ],
    }),
    task({ id: "other", runs: [run("other-notice", { notified: true })] }),
  ]);
  await act(async () => root!.render(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />));
  await settle();
  expect(host.querySelectorAll(".schedule-unread-dot")).toHaveLength(1);
});

it("connects session/detail read transitions to exact notification cleanup without making API errors visible", async () => {
  const getNotifications = vi.fn().mockImplementation(async ({ tag }: { tag: string }) => [{ tag, close: vi.fn() }]);
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ getNotifications }) } });
  const r = remote([]);
  const core = getCoreHost(r);
  await setTasks(r, []);
  const session = r.sessions.get("s-idle")!;
  await setTasks(r, [
    task({
      runs: [
        run("visible", { notified: true, session_id: session.id, notification_seq: 7 }),
        run("other", { notified: true, notification_seq: 8 }),
      ],
    }),
  ]);
  expect(getNotifications).not.toHaveBeenCalled();
  await mount(<SessionPage remote={r} sessionId={session.id} wide={false} onBack={() => {}} />);
  expect(getNotifications).toHaveBeenCalledExactlyOnceWith({ tag: `${session.id}/scheduled:7/NOTIFY_KIND_SCHEDULED` });
  expect(core.viewSchedules().hasNewResults).toBe(true);
  getNotifications.mockRejectedValueOnce(new Error("notification API failed"));
  await act(async () => root!.render(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />));
  await settle();
  expect(getNotifications).toHaveBeenLastCalledWith({ tag: "session-other/scheduled:8/NOTIFY_KIND_SCHEDULED" });
  expect(core.viewSchedules().hasNewResults).toBe(false);
  expect(core.viewSchedules().attentionCount).toBe(0);
  expect(host.textContent).not.toContain("notification API failed");
});

it("counts unread notifications in the wide sidebar's schedules entry too", async () => {
  const r = remote([]);
  await setTasks(r, []);
  await setTasks(r, [task({ runs: [run("new-notice", { notified: true })] })]);
  await mount(<Sidebar remote={r} go={() => {}} />);
  expect(host.querySelector('.sfoot [aria-label="有新结果"]')).not.toBeNull();
});

it("leaves new-schedule to the bottom row and remembers the ended group on this device", async () => {
  const r = remote([task({ id: "done", status: "completed" })]);
  await mount(<SchedulesPage remote={r} go={() => {}} />);
  expect(host.querySelector(".large button")).toBeNull();
  expect(host.querySelector(".task-new")).toBeNull();
  expect(host.querySelectorAll(".schedule-line")).toHaveLength(0);
  await click(/已结束/);
  expect(host.querySelectorAll(".schedule-line")).toHaveLength(1);
  expect(localStorage.getItem("schedule-ended-open")).toBe("true");
  await act(async () => root!.render(<SchedulesPage key="remount" remote={r} go={() => {}} />));
  await settle();
  expect(host.querySelectorAll(".schedule-line")).toHaveLength(1);
});

it("shows five recent runs before settings and expands all history on request", async () => {
  const r = remote([
    task({
      runs: Array.from({ length: 8 }, (_, i) =>
        run(`r${i}`, { notified: true, started_at: new Date(1792828800000 + i * 3600000).toISOString() }),
      ),
    }),
  ]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(5);
  expect(host.textContent!.indexOf(message("schedule.earlier"))).toBeLessThan(
    host.textContent!.indexOf(message("schedule.what_should_the_agent_do")),
  );
  expect(host.textContent!.indexOf(message("schedule.earlier"))).toBeLessThan(
    host.textContent!.indexOf(message("common.settings")),
  );
  await click("查看全部 7 次");
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(7);
});

it("opens a full message editor, guards cancel and validates UTF-8 bytes", async () => {
  const t = task();
  t.spec.message = Array.from({ length: 9 }, (_, i) => `检查第 ${i} 项`).join("\n");
  const r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  await click(message("markdown.expand"));
  expect(host.querySelector(".schedule-text.expanded")).not.toBeNull();
  await click(message("markdown.collapse"));
  await click(message("tools.android_edit"));
  expect(host.querySelector(".schedule-editor")).not.toBeNull();
  await input(host.querySelector("textarea")!, "中".repeat(11000));
  expect(button(message("common.save"))?.disabled).toBe(true);
  expect(host.textContent).toContain(message("schedule.too_long_maximum_32_kb"));
  await input(host.querySelector("textarea")!, "   ");
  expect(button(message("common.save"))?.disabled).toBe(true);
  await input(host.querySelector("textarea")!, "第一行\n第二行");
  await click(message("common.cancel"));
  expect(host.querySelector('[role="alertdialog"]')?.textContent).toContain(message("schedule.discard_changes"));
  expect(scheduleCalls(r, "update")).toHaveLength(0);
  await click(message("schedule.discard_changes_schedule_text"));
  expect(host.querySelector(".schedule-editor")).toBeNull();
  await click(message("tools.android_edit"));
  expect(host.querySelector("textarea")?.value).toBe(t.spec.message);
});

it("shows the generated task name as a live placeholder and rejects an oversized creation", async () => {
  const r = remote([]);
  await mount(<ScheduleCreate remote={r} sessionId="s-idle" go={() => {}} onBack={() => {}} />);
  await input(host.querySelector("textarea")!, "检查项目更新：\n逐条列出");
  expect(host.querySelector<HTMLInputElement>('[aria-label="任务名"]')?.placeholder).toBe("留空会用：检查项目更新");
  await input(host.querySelector("textarea")!, "x".repeat(32769));
  expect(button(message("schedule.create"))?.disabled).toBe(true);
  expect(host.textContent).toContain(message("schedule.too_long_maximum_32_kb"));
  expect(scheduleCalls(r, "create")).toHaveLength(0);
});

it("disables weekly completion without selected days and hides interval timezone", async () => {
  const r = remote([]);
  const core = getCoreHost(r);
  const formKey = core.scheduleFormOpen(ScheduleFormMode.CREATE_NEW, { machineId: "mac" });
  core.scheduleFormUpdateTiming({
    formKey,
    kind: ScheduleTimingKind.WEEKLY,
    weekdays: [1],
  });
  await mount(<TimingSheet host={core} formKey={formKey} onClose={() => {}} />);
  await click("周一");
  expect(button(message("common.done"))?.disabled).toBe(true);
  await click(message("schedule.every"));
  expect(host.querySelector('[aria-label="时区"]')).toBeNull();
  await click(message("schedule.once_schedules"));
  await click(message("schedule.weekly_on"));
  expect(button(message("common.done"))?.disabled).toBe(true);
  await click("周五");
  await click(message("common.done"));
  expect(core.viewScheduleForm(formKey).timing?.weekdays).toEqual([5]);
});

it("replaces the wide session timeline with a task list and keeps navigation tabs", async () => {
  const r = remote();
  await mount(<Sidebar remote={r} active="tasks" selectedTask="tasks/mac/task-1" go={() => {}} />);
  expect(host.querySelector(".schedule-sidebar")).not.toBeNull();
  expect(host.querySelector(".schedule-selected")).not.toBeNull();
  expect(host.querySelector(".side-top .mini")?.textContent).toBe(message("home.new_schedule"));
  expect(host.querySelector(".sfoot .seg")?.textContent).toContain(message("session.title"));
  expect(host.querySelector(".sfoot .seg .on")?.textContent).toContain(message("home.schedules_tab"));
});

it("renders scheduled source metadata from Core pending tray and keeps cancellation available", async () => {
  const r = remote();
  const session = r.sessions.get("s-idle")!;
  expect(session.id).toBeTruthy();
  const onCancelItem = vi.fn();
  const item = create(SessionTrayItemViewSchema, {
    requestId: "scheduled-input",
    text: "检查新回复",
    sourceLabel: create(TextSchema, { literal: "定时 15:00" }),
    group: SessionTrayGroupKind.WAIT_ANSWER,
    tone: SessionTrayTone.NEUTRAL,
    activity: SessionTrayRowActivity.IDLE,
    mode: SendMode.QUEUE,
    canCancel: true,
  });
  const tray = create(SessionPendingTrayViewSchema, {
    totalCount: 1,
    orderedItems: [item],
    groups: [
      create(SessionTrayGroupViewSchema, {
        kind: SessionTrayGroupKind.WAIT_ANSWER,
        tone: SessionTrayTone.NEUTRAL,
        headerText: create(TextSchema, { key: "queue.group_wait_answer" }),
        totalCount: 1,
        items: [item],
      }),
    ],
  });
  await mount(
    <PendingTray
      tray={tray}
      onCancelItem={onCancelItem}
      onIgnoreItem={vi.fn()}
      onPromoteItem={vi.fn()}
      onRestoreItem={vi.fn()}
      onRetryItem={vi.fn()}
      onResumeItem={vi.fn()}
      onCancelAllPaused={vi.fn()}
      onUndo={vi.fn()}
    />,
  );
  expect(host.querySelector(".ptray-gl")?.textContent).toContain(message("queue.group_wait_answer"));
  expect(host.querySelector(".ptray-t .src")?.textContent).toContain("定时 15:00");
  await act(async () => host.querySelector<HTMLButtonElement>(".ptray-acts button.x")!.click());
  expect(onCancelItem).toHaveBeenCalledTimes(1);
  expect(onCancelItem).toHaveBeenCalledWith("scheduled-input");
});

it("offers a return to the owning task from a wide run conversation", async () => {
  const r = remote();
  const demoS = (r as unknown as { demoSessions: Map<string, { info?: { taskOwned: boolean; scheduledTaskId: string } }> })
    .demoSessions.get("s-idle")!;
  demoS.info!.taskOwned = true;
  demoS.info!.scheduledTaskId = "task-1";
  r.syncDemoToCore();
  const back = vi.fn();
  await mount(<SessionPage remote={r} sessionId="s-idle" wide onBack={back} />);
  const control = host.querySelector<HTMLButtonElement>(".schedule-run-back");
  expect(control?.getAttribute("aria-label")).toBe(message("common.back"));
  await act(async () => control!.click());
  expect(back).toHaveBeenCalledOnce();
});

it("renders verified run history as full Markdown and quietly falls back while loading or unavailable", async () => {
  const execution = run("markdown", {
    session_id: "s-idle",
    notified: true,
    notification_text: "检查完成",
    final_summary: "短摘要",
  });
  const r = remote([task({ runs: [execution] })]);
  const full = "完整回复\n\n| 项目 | 结果 |\n| --- | --- |\n| 检查 | 通过 |\n\n- 第一项\n- 第二项\n\n```sh\necho ok\n```";
  const prev = (r as unknown as { evs: Map<string, ReturnType<typeof create<typeof EventSchema>>[]> }).evs.get("s-idle") ?? [];
  const baseSeq = BigInt(prev.length);
  const next = [
    ...prev,
    create(EventSchema, {
      seq: baseSeq + 1n,
      sessionId: "s-idle",
      origin: create(OriginSchema, { scheduledTaskId: "task-1", scheduledRunId: execution.id }),
      kind: { case: "userMessage", value: { text: "检查" } },
    }),
    create(EventSchema, {
      seq: baseSeq + 2n,
      sessionId: "s-idle",
      kind: { case: "agentMessage", value: { text: full, phase: "final_answer" } },
    }),
  ];
  (r as unknown as { evs: Map<string, typeof next> }).evs.set("s-idle", next);
  (r as unknown as { demoSessions: Map<string, { lastSeq: bigint }> }).demoSessions.get("s-idle")!.lastSeq = baseSeq + 2n;
  r.syncDemoToCore();
  let releaseEvents!: () => void;
  let getEventsSeen = false;
  const heldEvents = new Promise<void>((res) => {
    releaseEvents = res;
  });
  const origRpc = r.executeRpc.bind(r);
  vi.spyOn(r, "executeRpc").mockImplementation(async (eff) => {
    if (eff.kind.case === "getEvents" && eff.kind.value.sessionId === "s-idle") {
      getEventsSeen = true;
      await heldEvents;
    }
    return origRpc(eff);
  });
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  await vi.waitFor(() => expect(getEventsSeen).toBe(true));
  expect(host.querySelector(".schedule-result .ai")?.textContent).toBe("短摘要");
  await act(async () => {
    releaseEvents();
  });
  await vi.waitFor(() => expect(host.querySelector(".schedule-result table")?.textContent).toContain("检查通过"));
  expect(host.querySelectorAll(".schedule-result li")).toHaveLength(2);
  expect(host.querySelector(".schedule-result pre")?.textContent).toBe("echo ok");
});

it("keeps the latest card unchanged until the new-result prompt is clicked", async () => {
  const old = run("old", {
    notified: true,
    ended_at: "2026-11-01T08:00:00Z",
    notification_source: "agent",
    notification_text: "原结果",
    session_id: undefined,
  });
  const t = task({ runs: [old] });
  const r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  const next = run("new", {
    notified: true,
    ended_at: "2026-11-01T09:00:00Z",
    notification_source: "agent",
    notification_text: "后来结果",
    session_id: undefined,
  });
  await setTasks(r, [{ ...t, runs: [old, next] }]);
  expect(host.querySelector(".schedule-result h4")?.textContent).toBe("原结果");
  await click(message("home.ios_new_results"));
  expect(host.querySelector(".schedule-result h4")?.textContent).toBe("后来结果");
  expect(button(message("home.ios_new_results"))).toBeUndefined();
});

it("marks only the recent group read, retains the attention dot, and exposes keyboard reading actions", async () => {
  const r = remote([]);
  const core = getCoreHost(r);
  await setTasks(r, []);
  const t0 = task({ id: "attention", status: "failed", runs: [run("a", { notified: true })] });
  const t1 = task({ id: "recent", runs: [run("r", { notified: true })] });
  const t2 = task({ id: "recent-2", runs: [run("r2", { notified: true })] });
  await setTasks(r, [t0, t1, t2]);
  const go = vi.fn();
  await mount(<SchedulesPage remote={r} go={go} />);
  expect(host.querySelector('.attention .row1 [aria-label="需要你处理"]')).not.toBeNull();
  expect(host.querySelector(".hl-sub")?.textContent).toBe("另有 2 个新结果");
  expect(host.querySelectorAll(".attention .schedule-unread-dot")).toHaveLength(1);
  await click(message("schedule.mark_all_as_read"));
  const vmAfter = core.viewSchedules();
  expect(host.querySelector('.attention .row1 [aria-label="需要你处理"]')).not.toBeNull();
  expect([...vmAfter.fresh, ...vmAfter.others].filter((x) => x.hasUnread)).toHaveLength(0);
  expect(button(message("schedule.mark_all_as_read"))).toBeUndefined();
  const unread = host.querySelector<HTMLButtonElement>('.schedule-read-action[aria-label="标为未读"]')!;
  await act(async () => unread.click());
  await settle();
  expect(taskHasUnread(core, "recent")).toBe(true);
  expect(go).not.toHaveBeenCalled();
});

it("folds mobile result text at whole Markdown blocks and shows every block on expansion or wide screens", async () => {
  const media = vi.spyOn(window, "matchMedia").mockReturnValue({ matches: false } as MediaQueryList);
  const measure = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(() => new DOMRect(0, 0, 300, 80));
  const text = "第一段\n\n第二段\n\n第三段\n\n第四段";
  const t = task({ runs: [run("blocks", { notified: true, session_id: undefined, final_summary: text })] });
  const r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  expect(host.querySelectorAll(".schedule-result .ai > :not([hidden])")).toHaveLength(2);
  expect(host.querySelectorAll(".schedule-result .ai > [hidden]")).toHaveLength(2);
  await click(/^展开全文/);
  expect(host.querySelectorAll(".schedule-result .ai > [hidden]")).toHaveLength(0);
  expect(host.querySelectorAll(".schedule-result .ai > p")).toHaveLength(4);
  media.mockReturnValue({ matches: true } as MediaQueryList);
  await act(async () => root!.render(<ScheduleDetail key="wide" remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />));
  await settle();
  expect(host.querySelectorAll(".schedule-result .ai > [hidden]")).toHaveLength(0);
  expect(host.querySelector(".schedule-result .schedule-expand")).toBeNull();
  measure.mockRestore();
});

it.each(["one large paragraph", "| A | B |\n| --- | --- |\n| one | two |"])("retains the first oversized Markdown block: %s", async (text) => {
  vi.spyOn(window, "matchMedia").mockReturnValue({ matches: false } as MediaQueryList);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(() => new DOMRect(0, 0, 300, 400));
  const t = task({ runs: [run("large", { notified: true, session_id: undefined, final_summary: text })] });
  const r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  const first = host.querySelector<HTMLElement>(".schedule-result .ai > :first-child")!;
  expect(first.hidden).toBe(false);
  expect(first.textContent).not.toBe("");
  expect(first.classList.contains("schedule-first-block-clipped")).toBe(true);
  await click(/^展开全文/);
  expect(first.hidden).toBe(false);
  expect(first.classList.contains("schedule-first-block-clipped")).toBe(false);
});

it("marks the selected wide row unread through its context menu and exits the detail", async () => {
  const t = task({ runs: [run("latest", { notified: true })] });
  const r = remote([t]);
  const core = getCoreHost(r);
  const go = vi.fn();
  vi.spyOn(window, "matchMedia").mockReturnValue({ matches: true } as MediaQueryList);
  await mount(<SchedulesPage remote={r} go={go} sidebar selected="tasks/mac/task-1" />);
  await act(async () => host.querySelector(".schedule-line")!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true })));
  expect(host.querySelector(".schedule-menu")).not.toBeNull();
  await click(message("schedule.mark_as_unread"));
  expect(taskHasUnread(core, t.id)).toBe(true);
  expect(go).toHaveBeenCalledWith("tasks");
});

it("re-arms a cancelled one-shot from the wide row menu with a new time before submitting", async () => {
  const t = task({
    status: "cancelled",
    reason: "message_cancelled",
    spec: { ...task().spec, trigger: { type: "at", at: new Date(Date.now() + 3600_000).toISOString() } },
  });
  const r = remote([t]);
  vi.spyOn(window, "matchMedia").mockReturnValue({ matches: true } as MediaQueryList);
  await mount(<SchedulesPage remote={r} go={() => {}} sidebar />);
  await act(async () => host.querySelector(".schedule-line")!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true })));
  await click(message("schedule.resume"));
  expect(scheduleCalls(r, "update")).toHaveLength(0);
  expect(host.querySelector('[role="dialog"]')?.textContent).toContain(message("schedule.when"));
  await click(message("common.done"));
  const updates = scheduleCalls(r, "update");
  expect(updates).toHaveLength(1);
  expect(updates[0].kind.value.expectedRevision).toBe(BigInt(t.revision));
  const spec = JSON.parse(new TextDecoder().decode(updates[0].kind.value.specJson));
  expect(spec.trigger.type).toBe("at");
});

it("keeps the detail menu's specified order", async () => {
  const t = task({ runs: [run("result", { notified: true, session_id: undefined })] });
  const r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="更多"]')!.click());
  expect([...host.querySelectorAll(".schedule-menu .mi")].map((b) => b.textContent)).toEqual([
    message("schedule.run_once_now"),
    message("schedule.mark_as_unread"),
    message("schedule.pause"),
    message("schedule.delete_task"),
  ]);
  expect(host.querySelector(".schedule-menu .mi.danger")?.textContent).toBe(message("schedule.delete_task"));
});

it("protects a name-only schedule draft after leaving the creation form", async () => {
  const r = remote([]);
  const core = getCoreHost(r);
  await mount(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />);
  const name = host.querySelector<HTMLInputElement>('input[aria-label="任务名"]')!;
  await input(name, "还没有提交的任务名");
  await act(async () => root!.render(<SchedulesPage remote={r} go={() => {}} />));
  await settle();
  expect(core.hasUnsavedScheduleDrafts()).toBe(true);
  await act(async () => root!.render(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />));
  await settle();
  core.scheduleFormClose("create:new", true);
  expect(core.hasUnsavedScheduleDrafts()).toBe(false);
});

it.each(["light", "dark"])("aligns the attention banner, recovery button and cards within the same capped column (%s)", async (theme) => {
  const t = task({
    status: "failed",
    reason: "relay_session_limit",
    runs: [run("failure", { notified: true, session_id: undefined, final_summary: "运行失败" })],
  });
  const r = remote([t]);
  document.documentElement.dataset.theme = theme;
  await mount(
    <div className="desk">
      <main className="dm">
        <ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />
      </main>
    </div>,
  );
  const style = document.createElement("style");
  style.textContent = styles;
  host.append(style);
  const banner = host.querySelector<HTMLElement>(".schedule-attention > .banner")!;
  const recovery = host.querySelector<HTMLElement>(".schedule-recovery")!;
  const card = host.querySelector<HTMLElement>(".schedule-result")!;
  const column = host.querySelector<HTMLElement>(".schedule-detail-content")!;
  expect(banner.parentElement?.parentElement).toBe(column);
  expect(recovery.parentElement).toBe(column);
  expect(card.parentElement).toBe(column);
  expect(banner.parentElement?.nextElementSibling).toBe(recovery);
  const alertStyle = getComputedStyle(banner);
  const cardStyle = getComputedStyle(card);
  const actionStyle = getComputedStyle(recovery);
  expect(getComputedStyle(column).maxWidth).toBe("760px");
  expect(getComputedStyle(banner.parentElement!).marginLeft).toBe(cardStyle.marginLeft);
  expect(getComputedStyle(banner.parentElement!).marginRight).toBe(cardStyle.marginRight);
  expect(actionStyle.paddingLeft).toBe(cardStyle.marginLeft);
  expect(actionStyle.paddingRight).toBe(cardStyle.marginRight);
  expect(alertStyle.paddingLeft).toBe(cardStyle.paddingLeft);
  expect(alertStyle.borderRadius).toBe("10px");
  expect(cardStyle.borderRadius).toBe("14px");
  expect(alertStyle.backgroundColor).not.toBe(getComputedStyle(column.parentElement!).backgroundColor);
  expect(alertStyle.fontSize).toBe("13px");
  expect(alertStyle.color).toBe(getComputedStyle(document.documentElement).getPropertyValue("--orange-ink").trim());
  delete document.documentElement.dataset.theme;
});

it.each(["offline", "list-error"])("shows the latest cached result without reading it or showing entry dots (%s)", async (mode) => {
  const t = task({
    runs: [
      run("cached", {
        notified: true,
        session_id: "s-idle",
        notification_source: "agent",
        notification_text: "缓存结论",
        final_summary: "缓存正文",
      }),
    ],
  });
  const r = remote([], { cached: [t], listError: true });
  const core = getCoreHost(r);
  localStorage.setItem("ra-schedule-seen:" + JSON.stringify([r.account.server, r.account.deviceId, "mac"]), "[]");
  const prev = (r as unknown as { evs: Map<string, ReturnType<typeof create<typeof EventSchema>>[]> }).evs.get("s-idle") ?? [];
  const baseSeq = BigInt(prev.length);
  const evs = [
    ...prev,
    create(EventSchema, {
      seq: baseSeq + 1n,
      sessionId: "s-idle",
      origin: create(OriginSchema, { scheduledTaskId: "task-1", scheduledRunId: "cached" }),
      kind: { case: "userMessage", value: { text: "定时检查" } },
    }),
    create(EventSchema, {
      seq: baseSeq + 2n,
      sessionId: "s-idle",
      kind: { case: "agentMessage", value: { phase: "final_answer", text: "缓存的完整回复" } },
    }),
  ];
  vi.spyOn(r, "readEventsCache").mockImplementation(async (opId, sessionId) =>
    create(CacheEventsLoadedEventSchema, {
      opId,
      sessionId,
      events: evs.map((e) => create(PlainEventSchema, { seq: e.seq, event: e, verified: true })),
    }),
  );
  (r as unknown as { evs: Map<string, typeof evs> }).evs.set("s-idle", evs);
  (r as unknown as { demoSessions: Map<string, { lastSeq: bigint }> }).demoSessions.get("s-idle")!.lastSeq = baseSeq + 2n;
  r.syncDemoToCore();
  if (mode === "offline") {
    patchSyncState(r, {
      conn: "offline",
      machines: new Map([...r.machines].map(([id, m]) => [id, id === "mac" ? { ...m, online: false } : m])),
    });
  } else {
    core.scheduleRefresh("mac");
  }
  await settle();
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  expect(core.viewScheduleDetail("mac", t.id)).toBeTruthy();
  expect(host.querySelector(".schedule-result h4")?.textContent).toBe("缓存结论");
  expect(host.querySelector(".schedule-result .ai")?.textContent).toBe("缓存的完整回复");
  expect(host.querySelector(".schedule-unread-dot")).toBeNull();
  expect(core.viewScheduleDetail("mac", t.id).resultCard?.unreadOnEntry).toBe(false);
});

it("passes open menu actions directly to CoreHost even if snapshot advanced while menu was open", async () => {
  const old = run("old", { notified: true, session_id: undefined, ended_at: "2026-11-01T08:00:00Z" });
  const next = run("next", { notified: true, session_id: undefined, ended_at: "2026-11-01T09:00:00Z" });
  const before = task({ runs: [old] });
  const current = { ...before, runs: [old, next] };
  const r = remote([before]);
  const core = getCoreHost(r);
  const go = vi.fn();
  await mount(<ScheduleDetail remote={r} machineId="mac" id={before.id} go={go} onBack={() => {}} />);
  await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="更多"]')!.click());
  const control = [...host.querySelectorAll<HTMLButtonElement>(".schedule-menu .mi")].find(
    (b) => b.textContent === message("schedule.mark_as_unread"),
  )!;
  await setTasks(r, [current]);
  await act(async () => control.click());
  await settle();
  expect(taskHasUnread(core, before.id)).toBe(true);
  expect(go).toHaveBeenCalledWith("tasks");
});
