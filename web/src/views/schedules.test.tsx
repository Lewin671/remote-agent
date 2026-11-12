import { ScheduleRevisionError } from "../remote";
import { t as message } from "../i18n";
// @vitest-environment happy-dom
import { hasScheduleDrafts } from "../schedule-drafts";
import { create, toBinary } from "@bufbuild/protobuf";
import { EnvelopeSchema } from "../gen/remoteagent/v1/envelope_pb";
import { CommandResultSchema } from "../gen/remoteagent/v1/remote_pb";
import { act, StrictMode } from "react";
import { Sidebar } from "./Home";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import {
  ScheduleCreate,
  ScheduleDetail,
  ScheduleRow,
  ScheduleStrip,
  SchedulesPage,
  TimingSheet,
} from "./Schedules";
import { PendingBubbles } from "./Queue";
import { derive } from "../model";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { EventSchema, InputStatus, SendMode } from "../gen/remoteagent/v1/event_pb";
import { SessionPage } from "./Session";
import { task, run } from "../test/schedule-fixture";
import { dateTime, scheduleTime, scheduleOffline, scheduleEmpty } from "../schedules";
import { DefaultModeSource } from "../gen/remoteagent/v1/remote_pb";
import { ApprovalMode, Agent } from "../gen/remoteagent/v1/event_pb";
import type { ScheduleTask } from "../schedules";

const fsModule = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fsModule)) as { readFileSync(path: string, encoding: string): string };
const styles = readFileSync("src/styles.css", "utf8");

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement;
const remotes: DemoRemote[] = [];
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  host?.remove();
  for (const r of remotes.splice(0)) r.stop();
  vi.restoreAllMocks();
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
  localStorage.clear();
  delete document.documentElement.dataset.theme;
});
async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
}
function remote(tasks: ScheduleTask[] = [task()]) {
  const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
  remotes.push(r);
  r.start();
  r.conn = "online";
  vi.spyOn(r, "hasLiveSchedules").mockReturnValue(true);
  r.machines.get("mac")!.meta!.capabilities!.scheduledTasks = true;
  r.schedules.set("mac", { tasks, revision: 1n, loading: false, error: "" });
  return r;
}
function button(text: string | RegExp) {
  return [...host.querySelectorAll("button")].find((b) =>
    typeof text === "string"
      ? b.textContent === text
      : text.test(b.textContent ?? ""),
  );
}
async function click(text: string | RegExp) {
  const target = text === "删除任务" ? "删除任务…" : text;
  if (!button(target) && ["删除任务", message("schedule.delete_task"), message("schedule.run_once_now"), message("schedule.pause"), message("schedule.resume")].includes(String(text))) {
    const more = host.querySelector<HTMLButtonElement>('[aria-label="更多"]');
    if (more) await act(async () => more.click());
  }
  const b = button(target);
  expect(b).toBeTruthy();
  await act(async () => b!.click());
}
async function input(
  el: HTMLInputElement | HTMLTextAreaElement,
  value: string,
) {
  const setter = Object.getOwnPropertyDescriptor(
    el instanceof HTMLTextAreaElement
      ? HTMLTextAreaElement.prototype
      : HTMLInputElement.prototype,
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
    task({
      id: "bad",
      status: "failed",
      reason: "relay_session_limit",
      spec: { ...task().spec, title: "失败任务" },
    }),
    task({ id: "deleted", status: "cancelled" }),
  ]);
  const go = vi.fn();
  await mount(<SchedulesPage remote={r} go={go} />);
  // The sentence counts what stopped; its card comes before the other tasks.
  expect(host.querySelector(".hl")?.textContent).toBe("1 个任务停下了");
  expect(host.textContent!.indexOf("失败任务")).toBeLessThan(
    host.textContent!.indexOf(message("schedule.other_tasks")),
  );
  expect(host.querySelector(".rc.warn")?.textContent).toContain("失败任务");
  expect(host.querySelectorAll(".schedule-line")).toHaveLength(2);
  await click(/失败任务/);
  expect(go).toHaveBeenCalledWith("tasks/mac/bad");
  await act(async () => {
    r.machines.get("mac")!.online = false;
    r.touch();
  });
  expect(host.textContent).toContain(scheduleOffline());
  expect(host.querySelectorAll(".schedule-line.off")).toHaveLength(2);
  await act(async () =>
    root!.render(
      <ScheduleDetail
        remote={r}
        machineId="mac"
        id="task-1"
        go={go}
        onBack={() => {}}
      />,
    ),
  );
  await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="更多"]')!.click());
  expect(button(message("schedule.run_once_now"))?.disabled).toBe(true);
  expect(button(message("schedule.delete_task"))?.disabled).toBe(true);
  await act(async () => host.querySelector<HTMLDivElement>(".dim")?.click());
});
it("confirms deletion, preserves conversations and uses the displayed revision", async () => {
  const r = remote();
  const write = vi
    .spyOn(r, "changeSchedule")
    .mockResolvedValue(task({ status: "cancelled" }));
  const go = vi.fn();
  await mount(
    <ScheduleDetail
      remote={r}
      machineId="mac"
      id="task-1"
      go={go}
      onBack={() => {}}
    />,
  );
  await click("删除任务");
  expect(write).not.toHaveBeenCalled();
  expect(host.querySelector('[role="alertdialog"]')?.textContent).toContain(
    "已开出的对话保留",
  );
  await click(message("schedule.delete"));
  expect(write).toHaveBeenCalledWith(
    "mac",
    "task-1",
    expect.objectContaining({ operation: "cancel", revision: 1 }),
  );
  expect(go).toHaveBeenCalledWith("tasks");
});
it("folds quiet history by day and opens the exact selected execution", async () => {
  const runs = [
    run("one"),
    run("two", { started_at: "2026-10-24T11:00:00Z" }),
    run("notified", {
      notified: true,
      notification_text: "有新结果",
      started_at: "2026-10-23T11:00:00Z",
    }),
  ];
  const r = remote([task({ runs })]);
  const open = vi.spyOn(r, "openScheduleRun").mockResolvedValue();
  await mount(
    <ScheduleDetail
      remote={r}
      machineId="mac"
      id="task-1"
      go={() => {}}
      onBack={() => {}}
    />,
  );
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(1);
  await click(/2 次，都没有通知/);
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(3);
  await act(async () => host.querySelectorAll<HTMLButtonElement>(".schedule-run")[2].click());
  expect(open).toHaveBeenCalledWith("mac", runs[0]);
});
it("makes resume the only solid page action when a task needs attention", async () => {
  const r = remote([task({ status: "failed", reason: "relay_session_limit" })]);
  const write = vi.spyOn(r, "changeSchedule").mockResolvedValue(task());
  await mount(
    <ScheduleDetail
      remote={r}
      machineId="mac"
      id="task-1"
      go={() => {}}
      onBack={() => {}}
    />,
  );
  expect(host.textContent).toContain(message("schedule.the_relay_s_session_limit_has_been_reached"));
  expect([...host.querySelectorAll(".pri")].map((b) => b.textContent)).toEqual([
    message("schedule.resume"),
  ]);
  await click(message("schedule.resume"));
  expect(write).toHaveBeenCalledWith(
    "mac",
    "task-1",
    expect.objectContaining({ operation: "resume" }),
  );
});
it("edits calendar time, weekdays, limits and the IANA timezone", async () => {
  const onSave = vi.fn();
  await mount(
    <TimingSheet
      trigger={{ type: "calendar", time: "08:00", timezone: "Asia/Shanghai" }}
      onSave={onSave}
      onClose={() => {}}
    />,
  );
  await click(message("schedule.weekly_on"));
  await click("周五");
  const select = host.querySelector("select")!;
  await act(async () => {
    select.value = "count";
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await input(host.querySelector('input[type="number"]')!, "6");
  await click(message("common.done"));
  expect(onSave).toHaveBeenCalledWith({
    type: "calendar",
    time: "08:00",
    timezone: "Asia/Shanghai",
    weekdays: [1, 5],
    max_runs: 6,
  });
});
it("creates a current-conversation task with message, time and an optional name", async () => {
  const r = remote([]);
  const session = r.sessions.get("s-idle")!;
  const write = vi.spyOn(r, "changeSchedule").mockResolvedValue(task());
  await mount(
    <ScheduleCreate
      remote={r}
      session={session}
      go={() => {}}
      onBack={() => {}}
    />,
  );
  expect(host.textContent).not.toContain(message("schedule.computer_and_model"));
  await input(host.querySelector("textarea")!, "检查新回复，有结果通知我");
  await click(message("schedule.create"));
  expect(write).toHaveBeenCalledWith(
    "mac",
    "create:s-idle",
    expect.objectContaining({
      operation: "create",
      sessionId: "s-idle",
      requestId: expect.any(String),
      spec: expect.objectContaining({
        target: { type: "resume_session", session: "s-idle" },
      }),
    }),
  );
});
it("offers the scheduled-send menu and links the conversation strip to its task", async () => {
  const t = task();
  t.spec.target = { type: "resume_session", session: "s-idle" };
  const r = remote([t]);
  const go = vi.fn();
  const session = r.sessions.get("s-idle")!;
  await mount(<ScheduleStrip remote={r} session={session} go={go} />);
  await click(/定时 · 每天/);
  expect(go).toHaveBeenCalledWith("tasks/mac/task-1");
  await act(async () =>
    root!.render(
      <SessionPage
        remote={r}
        session={session}
        wide={false}
        onBack={() => {}}
      />,
    ),
  );
  await act(async () =>
    host.querySelector<HTMLButtonElement>(".navbar .more")!.click(),
  );
  await click(message("session.schedule_send"));
  expect(
    host.querySelector(".schedule-session-overlay")?.textContent,
  ).toContain(message("schedule.schedule_messages_to_this_conversation"));
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
  expect(host.querySelector("textarea")?.value).toBe("保留这条消息");
  expect(host.textContent).toContain("每 15 分钟");
});

it("keeps a delivery cancellation visible and re-arms the schedule explicitly", async () => {
  const r = remote([
    task({ status: "cancelled", reason: "message_cancelled" }),
  ]);
  expect(r.taskEntries()).toHaveLength(1);
  const write = vi.spyOn(r, "changeSchedule").mockResolvedValue(task());
  await mount(
    <ScheduleDetail
      remote={r}
      machineId="mac"
      id="task-1"
      go={() => {}}
      onBack={() => {}}
    />,
  );
  expect(host.textContent).toContain("送达前被取消");
  await click(message("schedule.resume"));
  expect(write).toHaveBeenCalledWith(
    "mac",
    "task-1",
    expect.objectContaining({
      operation: "update",
      revision: 1,
      spec: task().spec,
    }),
  );
});
it("edits a task's run settings without replacing the project's defaults", async () => {
  const r = remote();
  const saved = r.projectPrefs();
  const write = vi.spyOn(r, "changeSchedule").mockResolvedValue(task());
  await mount(
    <ScheduleDetail
      remote={r}
      machineId="mac"
      id="task-1"
      go={() => {}}
      onBack={() => {}}
    />,
  );
  await click(/^在哪运行/);
  expect(host.querySelector('[role="dialog"]')?.textContent).toContain(
    "只修改这个任务",
  );
  await click(message("common.save"));
  expect(write).toHaveBeenCalledWith(
    "mac",
    "task-1",
    expect.objectContaining({
      operation: "update",
      revision: 1,
      spec: expect.objectContaining({
        target: expect.objectContaining({
          cwd: "/example/project",
          mode: "auto",
          agent: "claude",
        }),
      }),
    }),
  );
  expect(r.projectPrefs()).toEqual(saved);
});

it("creates a new-conversation task without borrowing a source session", async () => {
  const r = remote([]);
  const sourceSession = r.sessions.get("s-idle")!;
  r.sessions.clear();
  r.sessions.set(sourceSession.id, sourceSession);
  for (const id of r.machines.keys()) if (id !== "mac") r.machines.delete(id);
  const write = vi.spyOn(r, "changeSchedule").mockResolvedValue(task());
  await mount(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />);
  await input(host.querySelector("textarea")!, "新建来源测试");
  expect(button(message("schedule.create"))?.disabled, host.textContent ?? "").toBe(false);
  await click(message("schedule.create"));
  if (host.querySelector('[role="alertdialog"]')) await click(message("modes.full_access"));
  expect(write).toHaveBeenCalled();
  const [machineId, , request] = write.mock.calls[0];
  expect(machineId).toBe("mac");
  expect(request.sessionId).toBeUndefined();
  expect(request.spec?.target.cwd).toBe(sourceSession.info?.cwd);
  expect(request.spec?.target.type).toBe("new_session");
});
it("fetches a task execution by ID and reveals the first event after its saved cursor", async () => {
  const r = remote();
  const fetch = vi.spyOn(r, "ensureSession").mockResolvedValue(undefined);
  const execution = run("missing", {
    session_id: "missing-session",
    start_seq: 42,
  });
  await r.openScheduleRun("mac", execution);
  expect(fetch).toHaveBeenCalledWith("missing-session");
  expect(r.scheduleFocus.get("missing-session")).toEqual({
    seq: 43n,
    runId: "missing",
  });
  expect(location.hash).toContain("s/missing-session");
});

it("starts a newly selected interval after the full interval", async () => {
  const save = vi.fn();
  await mount(
    <TimingSheet
      trigger={{ type: "calendar", time: "08:00", timezone: "Asia/Shanghai" }}
      onSave={save}
      onClose={() => {}}
    />,
  );
  await click(message("schedule.every"));
  await input(host.querySelector('input[type="number"]')!, "15");
  await click(message("common.done"));
  expect(save).toHaveBeenCalledWith({
    type: "interval",
    interval_seconds: 900,
    delay_seconds: 900,
  });
});

it("writes the conversation context in the schedule list's third line", async () => {
  const t = task();
  t.spec.target = { type: "resume_session", session: "s-idle" };
  const r = remote([t]);
  await mount(<SchedulesPage remote={r} go={() => {}} />);
  const title = r.sessions.get("s-idle")!.info!.title;
  expect(host.querySelector(".sess .mc")?.textContent).toContain(
    `在「${title}」里`,
  );
  expect(host.querySelector(".sess .mc")?.textContent).not.toContain("Codex");
});
it("creates with an optional name after the message and preserves that name in the draft", async () => {
  const r = remote([]);
  const session = r.sessions.get("s-idle")!;
  const write = vi.spyOn(r, "changeSchedule").mockResolvedValue(task());
  const props = { remote: r, session, go: () => {}, onBack: () => {} };
  await mount(<ScheduleCreate {...props} />);
  const name = host.querySelector<HTMLInputElement>(
    'input[aria-label="任务名"]',
  )!;
  expect(name.placeholder).toBe(message("schedule.leave_blank_to_use_the_message_s_first_sentence"));
  expect(
    [...host.querySelectorAll(".schedule-body > label, .schedule-body label")]
      .slice(0, 2)
      .map((el) => el.textContent),
  ).toEqual([message("composer.message"), message("schedule.task_name")]);
  await input(name, "每小时看 PR");
  await input(
    host.querySelector("textarea")!,
    "检查新回复。只在有变化时通知我。",
  );
  await act(async () => root!.render(null));
  await act(async () => root!.render(<ScheduleCreate {...props} />));
  expect(
    host.querySelector<HTMLInputElement>('input[aria-label="任务名"]')?.value,
  ).toBe("每小时看 PR");
  await click(message("schedule.create"));
  expect(write.mock.calls[0][2].spec?.title).toBe("每小时看 PR");
});
it("edits the task name with the existing rename interaction and keeps the other settings", async () => {
  const r = remote();
  const write = vi
    .spyOn(r, "changeSchedule")
    .mockImplementation(async (_machine, _key, request) => {
      const changed = task({ spec: request.spec!, revision: 2 });
      r.schedules.get("mac")!.tasks = [changed];
      r.touch();
      return changed;
    });
  await mount(
    <ScheduleDetail
      remote={r}
      machineId="mac"
      id="task-1"
      go={() => {}}
      onBack={() => {}}
    />,
  );
  await click(/^任务名/);
  const dialog = host.querySelector('[aria-label="重命名任务"]')!;
  const name = dialog.querySelector<HTMLInputElement>(
    'input[aria-label="任务名"]',
  )!;
  expect(document.activeElement).toBe(name);
  expect(name.selectionStart).toBe(0);
  expect(name.selectionEnd).toBe(name.value.length);
  await input(name, "项目巡检");
  await click(message("common.save"));
  expect(write).toHaveBeenCalledWith(
    "mac",
    "task-1",
    expect.objectContaining({
      revision: 1,
      operation: "update",
      spec: { ...task().spec, title: "项目巡检" },
    }),
  );
  expect(host.querySelector(".navbar .title b")?.textContent).toBe("项目巡检");
  await click(/^任务名/);
  await input(
    host.querySelector<HTMLInputElement>('[aria-label="重命名任务"] input')!,
    "",
  );
  await click(message("common.save"));
  expect(write.mock.calls[1][2].spec?.title).toBe(task().spec.message);
});
it("keeps timezone inside the timing sheet and appends only foreign zones to its value", async () => {
  const local = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const t = task({
    spec: {
      ...task().spec,
      trigger: { type: "calendar", time: "09:49", timezone: local },
    },
  });
  const r = remote([t]);
  const write = vi.spyOn(r, "changeSchedule").mockResolvedValue(task());
  await mount(
    <ScheduleDetail
      remote={r}
      machineId="mac"
      id="task-1"
      go={() => {}}
      onBack={() => {}}
    />,
  );
  expect(
    [...host.querySelectorAll(".grp > .schedule-field > span:first-child")].map(
      (el) => el.textContent,
    ),
  ).toEqual([message("schedule.when"), message("schedule.notify_on_completion"), message("schedule.run_in"), message("new_session.permissions"), message("schedule.task_name")]);
  expect(button(/^什么时候/)?.textContent).toBe("什么时候每天 09:49");
  const zone = local === "UTC" ? "America/Los_Angeles" : "UTC";
  await act(async () => {
    t.spec.trigger.timezone = zone;
    r.touch();
  });
  expect(button(/^什么时候/)?.textContent).toBe(`什么时候每天 09:49（${zone === "America/Los_Angeles" ? message("schedule.los_angeles_time") : zone}）`);
  await click(/^什么时候/);
  expect(
    host.querySelector<HTMLInputElement>('input[aria-label="时区"]')?.value,
  ).toBe(zone);
  await input(
    host.querySelector<HTMLInputElement>('input[aria-label="时区"]')!,
    local,
  );
  await click(message("common.done"));
  expect(write).toHaveBeenCalledWith(
    "mac",
    "task-1",
    expect.objectContaining({
      operation: "update",
      revision: 1,
      spec: expect.objectContaining({
        trigger: expect.objectContaining({
          type: "calendar",
          time: "09:49",
          timezone: local,
        }),
      }),
    }),
  );
});

it("uses the shared multiple-task strip wording", async () => {
  const first = task();
  first.spec.target = { type: "resume_session", session: "s-idle" };
  const second = task({ ...first, id: "task-2" });
  const r = remote([first, second]);
  await mount(
    <ScheduleStrip
      remote={r}
      session={r.sessions.get("s-idle")!}
      go={() => {}}
    />,
  );
  expect(host.querySelector(".schedule-strip")?.textContent).toContain(
    "2 个定时 · 下次",
  );
  expect(host.querySelector(".schedule-strip")?.textContent).not.toContain(
    "2 个定时，",
  );
});

it("closes an unchanged task name without sending an update", async () => {
  const r = remote();
  const write = vi.spyOn(r, "changeSchedule").mockResolvedValue(task());
  await mount(
    <ScheduleDetail
      remote={r}
      machineId="mac"
      id="task-1"
      go={() => {}}
      onBack={() => {}}
    />,
  );
  await click(/^任务名/);
  await click(message("common.save"));
  expect(write).not.toHaveBeenCalled();
  expect(host.querySelector('[aria-label="重命名任务"]')).toBeNull();
});

it.each<Partial<ScheduleTask>>([
  { status: "scheduled" },
  { status: "paused", reason: "user_paused" },
  { status: "completed" },
  { status: "cancelled" },
  { status: "failed", reason: "relay_session_limit" },
  { status: "uncertain", reason: "restart_dispatching" },
  { active_run: run("active", { status: "dispatching" }) },
  { active_run: run("active", { status: "queued" }) },
  { active_run: run("active", { status: "running" }) },
  { manual_request: { request_id: "manual" } },
])("allows a title-only update without replacing execution settings: %j", async (state) => {
  const original = task({ ...state, runs: [run("history")] });
  const r = remote([original]);
  const write = vi.spyOn(r, "changeSchedule").mockResolvedValue(original);
  await mount(
    <ScheduleDetail
      remote={r}
      machineId="mac"
      id="task-1"
      go={() => {}}
      onBack={() => {}}
    />,
  );
  expect(button(/^任务名/)?.disabled).toBe(false);
  await click(/^任务名/);
  await input(
    host.querySelector<HTMLInputElement>('[aria-label="重命名任务"] input')!,
    "新任务名",
  );
  await click(message("common.save"));
  expect(write).toHaveBeenCalledExactlyOnceWith("mac", "task-1", {
    operation: "update",
    taskId: "task-1",
    revision: original.revision,
    spec: { ...original.spec, title: "新任务名" },
  });
  await act(async () => {
    r.machines.get("mac")!.online = false;
    r.touch();
  });
  expect(button(/^任务名/)?.disabled).toBe(true);
});

it("derives final limits, creator, offline subtitle and running edit guards", async () => {
  const t = task({active_run: run("active", {status:"running"}), manual_request:{request_id:"manual"}});
  t.spec.trigger = {type:"interval",interval_seconds:3600,delay_seconds:3600,max_runs:5,until:"2027-10-30T08:00:00Z"};
  const r=remote([t]);
  const write=vi.spyOn(r,"changeSchedule").mockResolvedValue(t);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
  expect(host.textContent).toContain("共 5 次，或到 ");
  expect(host.textContent).toContain(message("schedule.created_by_you"));
  expect(host.textContent).not.toContain("接管");
  await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="更多"]')!.click());
  expect(button(message("schedule.run_once_now"))?.disabled).toBe(true);
  await act(async () => host.querySelector<HTMLDivElement>(".dim")?.click());
  for (const label of [/^什么时候/,/^编辑$/,/^在哪运行/]) expect(button(label)?.disabled).toBe(true);
  expect(button(/^任务名/)?.disabled).toBe(false);
  const notice = host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="完成后通知"]')!;
  expect(notice.disabled).toBe(false);
  await act(async()=>notice.click());
  expect(write.mock.calls[0][2].spec).toEqual({...t.spec,notify:"agent_only"});
  await act(async()=>{r.machines.get("mac")!.online=false;r.touch();});
  expect(host.textContent).toContain(scheduleOffline());
  expect(host.querySelector(".navbar")?.textContent).not.toContain(message("list_state.running"));
  expect(host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="完成后通知"]')!.disabled).toBe(true);
});
it("uses shared empty copy and resolves a missing conversation before naming it", async () => {
  // The zone label is shown only when the task differs from the viewer's zone.
  vi.stubEnv("TZ", "UTC");
  const r=remote([]);
  await mount(<SchedulesPage remote={r} go={()=>{}} />);
  expect(host.textContent).toContain(scheduleEmpty());
  const t=task();t.spec.target={type:"resume_session",session:"not-loaded"};
  r.schedules.get("mac")!.tasks=[t];
  await act(async()=>r.touch());
  expect(host.querySelector(".sess .mc")?.textContent).toContain(message("schedule.in_conversation"));
  expect(host.querySelector(".sess .mc")?.textContent).toContain(message("schedule.beijing_time"));
  r.sessions.set("not-loaded",{id:"not-loaded",machineId:"mac",metaRev:0n,lastSeq:0n,updatedMs:0n,endedMs:0n});
  await act(async()=>r.touch());
  expect(host.querySelector(".sess .mc")?.textContent).toContain(message("schedule.in_conversation"));
});
it("keeps the original interval phase and both unchanged end limits", async () => {
  const trigger={type:"interval" as const,interval_seconds:900,at:"2026-10-01T08:00:00Z",delay_seconds:29,max_runs:5,until:"2027-10-30T08:00:29Z"};
  const save=vi.fn();await mount(<TimingSheet trigger={trigger} onSave={save} onClose={()=>{}} />);
  await click(message("common.done"));expect(save).toHaveBeenCalledWith(trigger);
  await input(host.querySelectorAll<HTMLInputElement>('input[type="number"]')[1],"6");
  await click(message("common.done"));expect(save.mock.calls.at(-1)![0]).toMatchObject({at:trigger.at,delay_seconds:29,max_runs:6,until:trigger.until});
});
it("submits the editor's snapshot revision and spec even after remote changes", async () => {
  const old=task();const r=remote([old]);const send=vi.spyOn(r,"changeSchedule").mockResolvedValue(old);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={old.id} go={()=>{}} onBack={()=>{}} />);
  await click(/^编辑$/);
  await act(async()=>{r.schedules.get("mac")!.tasks=[task({revision:9,spec:{...old.spec,title:"别人改名"}})];r.touch();});
  await input(host.querySelector("textarea")!,"我的修改");await click(message("common.save"));
  expect(send.mock.calls[0][2]).toMatchObject({revision:1,spec:{...old.spec,message:"我的修改"}});
});
it("keeps plan permission visible and disables scheduled creation", async () => {
  const r=remote([]);r.sessions.clear();
  for(const m of r.machines.values())for(const agent of m.meta?.agents??[])agent.defaultMode=ApprovalMode.PLAN;
  for(const project of r.prefs.snap?.prefs.settings??[]) for(const agent of project.agents) agent.mode=ApprovalMode.PLAN;
  const send=vi.spyOn(r,"changeSchedule").mockResolvedValue(task());
  await mount(<ScheduleCreate remote={r} go={()=>{}} onBack={()=>{}} />);
  await input(host.querySelector("textarea")!,"检查更新");
  expect(button(message("schedule.create"))?.disabled).toBe(true);
  expect(host.textContent).toContain("计划模式不能创建执行任务");expect(send).not.toHaveBeenCalled();
});

it("derives an empty quiet summary and pending-only run guard from the task",async()=>{
  const t=task({runs:[run("q1",{notified:false,final_summary:""}),run("q2",{notified:false,final_summary:""})],manual_request:{request_id:"manual"}});
  const r=remote([t]);await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
  expect(host.textContent).toContain("：没有通知");await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="更多"]')!.click());
  expect(button(message("schedule.run_once_now"))?.disabled).toBe(true);
  await act(async () => host.querySelector<HTMLDivElement>(".dim")?.click());
});

it("uses the shared new-task explanation in the actual creation page", async () => {
  const r = remote([]);
  // The default project follows demo session times, some fixed to the time of
  // day; give every machine a known default permission so the page under test
  // is the same whichever project comes first.
  for (const m of r.machines.values()) if (m.meta) m.meta.agents = r.machines.get("mac")!.meta!.agents;
  await mount(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />);
  expect(host.querySelector(".schedule-body > .gf:last-child")?.textContent).toBe("每次运行新开一个对话，归在这个任务下面，不进会话列表。完成后默认通知；关闭后仅由 agent 主动通知。");
});

it("defaults dual limits to count and preserves the deadline when only the zone changes",async()=>{
 const onSave=vi.fn();const trigger={type:"calendar" as const,time:"08:00",timezone:"America/Los_Angeles",max_runs:5,until:"2027-10-30T08:00:00Z"};
 await mount(<TimingSheet trigger={trigger} onSave={onSave} onClose={()=>{}} />);
 expect((host.querySelector('select[aria-label="结束"]') as HTMLSelectElement)?.value || (host.querySelector("select") as HTMLSelectElement)?.value).toBe("count");
 await input(host.querySelector('input[placeholder="Asia/Shanghai"]')!,"Asia/Shanghai");await click(message("common.done"));
 expect(onSave.mock.calls[0][0]).toMatchObject({max_runs:5,until:trigger.until,timezone:"Asia/Shanghai"});
});

it("edits CLI choices and inherited settings only on the task computer",async()=>{
 const t=task();const r=remote([t]);r.sessions.clear();
 const m=r.machines.get("mac")!;const other=structuredClone(m);other.id="other";other.meta!.hostname="另一个电脑";other.meta!.agents=other.meta!.agents.filter(a=>a.agent===Agent.CLAUDE);r.machines.set("other",other);
 r.prefs.snap!.prefs.settings[0].path=t.spec.target.cwd!;r.prefs.snap!.prefs.settings[0].machineId="other";
 r.prefs.snap!.prefs.settings[0].agents.forEach(a=>{a.model="other-only-model";a.mode=ApprovalMode.FULL_ACCESS;});
 const send=vi.spyOn(r,"changeSchedule").mockResolvedValue(t);
 await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);await click(/^在哪运行/);
 expect(button("Codex")).toBeTruthy();
 await click(/^模型和权限/);expect(host.textContent).toContain(message("new_session.task_settings"));expect(button(message("projects.pin"))).toBeUndefined();expect(button(message("projects.remove_action"))).toBeUndefined();await click(message("common.done"));
 await click(/^项目/);expect(button("另一个电脑")).toBeUndefined();expect(button("MacBook")?.disabled).toBe(true);await act(async () => ([...host.querySelectorAll(".dim")].at(-1) as HTMLElement).click());
 await click("Codex");await click(message("common.save"));
 expect(send.mock.calls[0][2].spec?.target).toMatchObject({agent:"codex",model:"",mode:"auto"});
});

it("shows CLI as plain text on a single-CLI computer and a segmented control when multiple CLIs are installed", async () => {
  const r = remote([task()]);
  const idle = r.sessions.get("s-idle")!;
  r.sessions.clear();
  r.sessions.set("s-idle", idle);
  await mount(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />);
  expect(host.querySelector(".schedule-field .seg")).not.toBeNull();
  await act(async () => {
    r.machines.get("mac")!.meta!.agents = r.machines.get("mac")!.meta!.agents.filter((a) => a.agent === Agent.CODEX);
    r.touch();
  });
  expect(host.querySelector(".schedule-field .seg")).toBeNull();
  const cliField = [...host.querySelectorAll(".schedule-field")].find((el) => el.firstElementChild?.textContent === message("new_session.cli_picker_label"));
  expect(cliField?.querySelector(".v")?.textContent).toBe("Codex");
});

it("shows the shared destination placeholder until target metadata is loaded",async()=>{
 const t=task();t.spec.target={type:"resume_session",session:"missing"};const r=remote([t]);
 r.sessions.set("missing",{id:"missing",machineId:"mac",updatedMs:0n,metaRev:0n,lastSeq:0n,endedMs:0n});
 await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
 expect(button("发到对话")).toBeTruthy();
});

it("closes a stale editor when the daemon reports a revision conflict",async()=>{
 const t=task();const r=remote([t]);vi.spyOn(r,"changeSchedule").mockRejectedValue(new ScheduleRevisionError(message("errors.the_task_was_just_changed_updated_to_the_latest")));
 await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
 await click(/^编辑$/);await input(host.querySelector("textarea")!,"新消息");await click(message("common.save"));
 expect(host.querySelector('[role="dialog"]')).toBeNull();expect(host.textContent).toContain(message("errors.the_task_was_just_changed_updated_to_the_latest"));
});

it.each(["2027-10-24T15:00:00Z","2027-10-24T15:00:00+08:00"])("uses the local IANA zone when converting timestamp %s to a calendar",async(at)=>{
 const save=vi.fn();await mount(<TimingSheet trigger={{type:"at",at}} onSave={save} onClose={()=>{}} />);
 await click(message("schedule.daily"));await click(message("common.done"));
 expect(save.mock.calls[0][0].timezone).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
});

it("requires a concrete permission when neither project nor CLI has a known default",async()=>{
 const r=remote([]);r.sessions.clear();
 for(const machine of r.machines.values()) for(const agent of machine.meta?.agents??[]) {agent.defaultMode=ApprovalMode.UNSPECIFIED;agent.defaultModeSource=0;}
 for(const setting of r.prefs.snap?.prefs.settings??[]) for(const agent of setting.agents) agent.mode=ApprovalMode.UNSPECIFIED;
 await mount(<ScheduleCreate remote={r} go={()=>{}} onBack={()=>{}} />);await input(host.querySelector("textarea")!,"检查");
 expect(button(message("schedule.create"))?.disabled).toBe(true);expect(host.textContent).toContain(message("schedule.select_explicit_permissions_before_creating_a_scheduled_task"));
});

it.each([31,62,123])("preserves an unchanged %i-second interval exactly", async seconds => {
  const trigger={type:"interval" as const,interval_seconds:seconds,delay_seconds:29,at:"2026-10-01T08:00:00Z"};
  const save=vi.fn();await mount(<TimingSheet trigger={trigger} onSave={save} onClose={()=>{}} />);
  await click(message("common.done"));expect(save).toHaveBeenLastCalledWith(trigger);
  await input(host.querySelector<HTMLInputElement>('input[type="number"]')!,String(seconds/60+1));
  await click(message("common.done"));expect(save).toHaveBeenLastCalledWith({type:"interval",interval_seconds:seconds+60,delay_seconds:seconds+60});
  await input(host.querySelector<HTMLInputElement>('input[type="number"]')!,"0.511");
  save.mockClear();await click(message("common.done"));expect(save).not.toHaveBeenCalled();expect(host.textContent).toContain(message("schedule.the_interval_must_be_at_least_1_second"));
});
it("converts cutoff display across zones while retaining the absolute instant", async () => {
  const trigger={type:"calendar" as const,time:"08:00",timezone:"Asia/Shanghai",until:"2027-10-30T00:15:29Z"};
  const save=vi.fn();await mount(<TimingSheet trigger={trigger} onSave={save} onClose={()=>{}} />);
  const cutoff=()=>host.querySelector<HTMLInputElement>('input[type="datetime-local"]')!;
  const zone=host.querySelector<HTMLInputElement>('input[aria-label="时区"]')!;
  expect(cutoff().value).toBe("2027-10-30T08:15");
  await input(zone,"America/Los_Angeles");expect(cutoff().value).toBe("2027-10-29T17:15");
  await click(message("common.done"));expect(save.mock.calls.at(-1)![0].until).toBe(trigger.until);
  await input(zone,"Asia/Tokyo");expect(cutoff().value).toBe("2027-10-30T09:15");
  await input(cutoff(),"2027-11-01T09:30");
  await input(zone,"UTC");expect(cutoff().value).toBe("2027-11-01T00:30");
  await click(message("common.done"));expect(save.mock.calls.at(-1)![0].until).toBe("2027-11-01T00:30:00.000Z");
});

it("keeps an edited cutoff instant when a zone change lands it in a repeated wall time", async () => {
  const save=vi.fn();await mount(<TimingSheet trigger={{type:"calendar",time:"08:00",timezone:"UTC",until:"2027-10-30T00:00:00Z"}} onSave={save} onClose={()=>{}} />);
  const cutoff=()=>host.querySelector<HTMLInputElement>('input[type="datetime-local"]')!;
  await input(cutoff(),"2027-11-07T06:30");
  await input(host.querySelector<HTMLInputElement>('input[aria-label="时区"]')!,"America/New_York");
  expect(cutoff().value).toBe("2027-11-07T01:30");
  await click(message("common.done"));expect(save.mock.calls.at(-1)![0].until).toBe("2027-11-07T06:30:00.000Z");
});

it("creates with the new computer's displayed defaults after switching without editing",async()=>{
  const r=remote([]);const old=r.machines.get("mac")!, next=structuredClone(old);
  old.meta!.agents=old.meta!.agents.filter(a=>a.agent===Agent.CODEX);
  old.meta!.agents[0].defaultMode=ApprovalMode.FULL_ACCESS;old.meta!.agents[0].defaultModeSource=DefaultModeSource.CLI_CONFIG;
  next.id="new";next.meta!.hostname="新电脑";next.meta!.agents=[{...old.meta!.agents[0],agent:Agent.CLAUDE,defaultMode:ApprovalMode.ASK}];
  r.machines.clear();r.machines.set("mac",old);r.machines.set("new",next);
  const source=r.sessions.get("s-idle")!, path=source.info!.cwd;
  const another={...source,id:"new-session",machineId:"new",info:{...source.info!,id:"new-session",agent:Agent.CLAUDE}};
  r.sessions.clear();r.sessions.set(source.id,source);r.sessions.set(another.id,another);
  const stored=r.prefs.snap!.prefs.settings[0];stored.path=path;stored.machineId="mac";stored.agent=Agent.CODEX;
  stored.agents=[{...stored.agents[0],agent:Agent.CODEX,model:"gpt-6-astra",effort:"high",mode:ApprovalMode.FULL_ACCESS}];
  const send=vi.spyOn(r,"changeSchedule").mockResolvedValue(task());
  await mount(<ScheduleCreate remote={r} go={()=>{}} onBack={()=>{}} />);
  await input(host.querySelector("textarea")!,"检查回复");
  await click(/^机器和模型/);
  const machineRow=[...host.querySelectorAll<HTMLButtonElement>("button")].find(b=>b.querySelector(".mt")?.textContent===message("common.machine"));
  expect(machineRow,host.textContent??"").toBeTruthy();await act(async()=>machineRow!.click());
  expect(button(/改用 新电脑/),host.textContent??"").toBeTruthy();await click(/改用 新电脑/);
  await click(message("common.done"));
  expect(button(/^机器和模型/)?.textContent).toContain("新电脑");
  expect(button(/^权限/)?.textContent).toContain(message("modes.ask"));
  expect(button(/^机器和模型/)?.textContent).not.toContain("gpt-6-astra");
  await click(message("schedule.create"));expect(send).toHaveBeenCalledTimes(1);
  const [machine,,request]=send.mock.calls[0];expect(machine).toBe("new");
  expect(request.spec?.target).toMatchObject({agent:"claude",model:"",effort:"",mode:"ask",cwd:path});
});


it.each(["at", "cutoff"])("rejects a DST gap and saves the first repeated time for %s", async (field) => {
  vi.spyOn(Date, "now").mockReturnValue(Date.parse("2026-10-24T00:00:00Z"));
  const save = vi.fn();
  await mount(<TimingSheet trigger={field === "at" ? {type:"at", at:"2027-01-01T12:00:00Z"} : {type:"calendar", time:"08:00", timezone:"America/Los_Angeles", until:"2027-01-01T12:00:00Z"}} onSave={save} onClose={()=>{}} />);
  const zone = host.querySelector<HTMLInputElement>('input[aria-label="时区"]')!;
  await input(zone, "America/Los_Angeles");
  const local = host.querySelector<HTMLInputElement>('input[type="datetime-local"]')!;
  await input(local, "2027-03-14T02:30");
  await click(message("common.done"));
  expect(save).not.toHaveBeenCalled();
  expect(host.textContent).toContain(message("schedule.daylight_saving_time_skips_this_time_in_the_selected"));
  await input(zone, "Europe/Berlin");
  await input(local, "2027-10-31T02:30");
  await click(message("common.done"));
  expect(save.mock.calls.at(-1)![0][field === "at" ? "at" : "until"]).toBe("2027-10-31T00:30:00.000Z");
});

it("keeps unread on opening the list, reads only the detail, and keeps entry dots for this visit", async () => {
  const r = remote([]);
  let tasks: ScheduleTask[] = [];
  vi.spyOn(r, "machineCommand").mockImplementation(async () => create(CommandResultSchema, {
    kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify({ tasks })) } },
  }));
  await r.refreshSchedules("mac");
  tasks = [task({ runs: [run("new-notice", { notified: true })] }), task({ id: "other", runs: [run("other-notice", { notified: true })] })];
  await r.refreshSchedules("mac");
  expect(r.scheduleNewResults()).toBe(true);
  await mount(<SchedulesPage remote={r} go={() => {}} />);
  expect(r.scheduleNewResults()).toBe(true);
  expect(host.querySelectorAll(".schedule-line.unread")).toHaveLength(2);
  expect(host.querySelector(".hl")?.textContent).toBe("2 个新结果");
  tasks[0].runs!.push(run("while-list-open", { notified: true }));
  await act(async () => { await r.refreshSchedules("mac"); });
  expect(r.scheduleNewResults()).toBe(true);
  expect(host.querySelectorAll(".schedule-line.unread")).toHaveLength(2);
  await act(async () => root!.render(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />));
  expect(r.scheduleNewResults()).toBe(true);
  expect(host.querySelectorAll(".schedule-unread-dot")).toHaveLength(2);
  tasks[0].runs!.push(run("while-detail-open", { notified: true }));
  await act(async () => { await r.refreshSchedules("mac"); });
  expect(r.scheduleNewResults()).toBe(true);
  expect(host.querySelectorAll(".schedule-unread-dot")).toHaveLength(2);
  await act(async () => root!.render(<SchedulesPage remote={r} go={() => {}} />));
  tasks[0].runs!.push(run("after-leaving", { notified: true }));
  await act(async () => { await r.refreshSchedules("mac"); });
  r.conn = "offline";
  expect(r.scheduleNewResults()).toBe(true);
  await act(async () => root!.render(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />));
  expect(host.querySelectorAll(".schedule-unread-dot")).toHaveLength(1);
});

it("reads runs when their actual session page opens and as new runs arrive", async () => {
  const r = remote([]);
  let tasks: ScheduleTask[] = [];
  vi.spyOn(r, "machineCommand").mockImplementation(async () => create(CommandResultSchema, {
    kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify({ tasks })) } },
  }));
  await r.refreshSchedules("mac");
  const session = [...r.sessions.values()][0];
  tasks = [task({ runs: [run("a", { notified: true, session_id: session.id }), run("b", { notified: true, session_id: session.id }), run("c", { notified: true })] })];
  await r.refreshSchedules("mac");
  await mount(<SessionPage remote={r} session={session} wide={false} onBack={() => {}} />);
  expect(r.scheduleNewResults()).toBe(true);
  tasks[0].runs!.push(run("new", { notified: true, session_id: session.id }));
  await act(async () => { await r.refreshSchedules("mac"); });
  expect(r.scheduleNewResults()).toBe(true);
});

it("counts unread notifications in the wide sidebar's schedules entry too", async () => {
  const r = remote([]);
  let tasks: ScheduleTask[] = [];
  vi.spyOn(r, "machineCommand").mockImplementation(async () => create(CommandResultSchema, {
    kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify({ tasks })) } },
  }));
  await r.refreshSchedules("mac");
  tasks = [task({ runs: [run("new-notice", { notified: true })] })];
  await r.refreshSchedules("mac");
  await mount(<Sidebar remote={r} go={() => {}} />);
  expect(host.querySelector('.sfoot [aria-label="有新结果"]')).not.toBeNull();
});

it("leaves 新定时 to the bottom row and remembers the ended group on this device", async () => {
  const r = remote([task({id:"done",status:"completed"})]);
  const go = vi.fn();
  await mount(<SchedulesPage remote={r} go={go} />);
  // The page's head has no button of its own: the shell's ink 新定时 makes a task.
  expect(host.querySelector(".large button")).toBeNull();
  expect(host.querySelector(".task-new")).toBeNull();
  expect(host.querySelectorAll(".schedule-line")).toHaveLength(0);
  await click(/已结束/);
  expect(host.querySelectorAll(".schedule-line")).toHaveLength(1);
  expect(localStorage.getItem("schedule-ended-open")).toBe("true");
  await act(async () => root!.render(<SchedulesPage key="remount" remote={r} go={go} />));
  expect(host.querySelectorAll(".schedule-line")).toHaveLength(1);
});
it("uses short offline rows and hides editing arrows with the computer disconnected", async () => {
  const r = remote(); r.machines.get("mac")!.online = false;
  await mount(<SchedulesPage remote={r} go={()=>{}} />);
  expect(host.querySelector(".mc")?.textContent).toContain(message("list_state.unlinked"));
  expect(host.querySelector(".row1")?.textContent).not.toContain("上次");
  await act(async()=>root!.render(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={()=>{}} onBack={()=>{}} />));
  expect(host.querySelectorAll(".schedule-field .ic")).toHaveLength(0);
  expect(host.textContent).toContain("连上后才能修改、运行或删除。");
  expect(button(message("tools.android_edit"))?.disabled).toBe(true);
});
it("shows five recent runs before settings and expands all history on request", async () => {
  const r = remote([task({runs:Array.from({length:8}, (_, i)=>run(`r${i}`, {notified:true,started_at:new Date(1792828800000+i*3600000).toISOString()}))})]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={()=>{}} onBack={()=>{}} />);
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(5);
  expect(host.textContent!.indexOf(message("schedule.earlier"))).toBeLessThan(host.textContent!.indexOf(message("schedule.what_should_the_agent_do")));
  expect(host.textContent!.indexOf(message("schedule.earlier"))).toBeLessThan(host.textContent!.indexOf(message("common.settings")));
  await click("查看全部 7 次");
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(7);
});
it("opens a full message editor, guards cancel and validates UTF-8 bytes", async () => {
  const t = task(); t.spec.message = Array.from({length:9}, (_,i)=>`检查第 ${i} 项`).join("\n");
  const r = remote([t]); const write = vi.spyOn(r,"changeSchedule").mockResolvedValue(t);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
  await click(message("markdown.expand"));
  expect(host.querySelector(".schedule-text.expanded")).not.toBeNull();
  await click(message("markdown.collapse")); await click(message("tools.android_edit"));
  expect(host.querySelector(".schedule-editor")).not.toBeNull();
  await input(host.querySelector("textarea")!,"中".repeat(11000));
  expect(button(message("common.save"))?.disabled).toBe(true);
  expect(host.textContent).toContain(message("schedule.too_long_maximum_32_kb"));
  await input(host.querySelector("textarea")!,"   "); expect(button(message("common.save"))?.disabled).toBe(true);
  await input(host.querySelector("textarea")!,"第一行\n第二行");
  await click(message("common.cancel")); expect(host.querySelector('[role="alertdialog"]')?.textContent).toContain(message("schedule.discard_changes"));
  expect(write).not.toHaveBeenCalled(); await click(message("schedule.discard_changes_schedule_text"));
  expect(host.querySelector(".schedule-editor")).toBeNull();
  await click(message("tools.android_edit")); expect(host.querySelector("textarea")?.value).toBe(t.spec.message);
});
it("shows the generated task name as a live placeholder and rejects an oversized creation", async () => {
  const r = remote([]); const write = vi.spyOn(r,"changeSchedule").mockResolvedValue(task());
  await mount(<ScheduleCreate remote={r} session={r.sessions.get("s-idle")!} go={()=>{}} onBack={()=>{}} />);
  await input(host.querySelector("textarea")!,"检查项目更新：\n逐条列出");
  expect(host.querySelector<HTMLInputElement>('[aria-label="任务名"]')?.placeholder).toBe("留空会用：检查项目更新");
  await input(host.querySelector("textarea")!,"x".repeat(32769));
  expect(button(message("schedule.create"))?.disabled).toBe(true); expect(host.textContent).toContain(message("schedule.too_long_maximum_32_kb")); expect(write).not.toHaveBeenCalled();
});
it("disables weekly completion without selected days and hides interval timezone", async () => {
  const save = vi.fn(); await mount(<TimingSheet trigger={{type:"calendar",time:"09:30",weekdays:[1],timezone:"UTC",max_runs:4}} onSave={save} onClose={()=>{}} />);
  await click("周一"); expect(button(message("common.done"))?.disabled).toBe(true);
  await click(message("schedule.every")); expect(host.querySelector('[aria-label="时区"]')).toBeNull();
  await click(message("schedule.once_schedules")); await click(message("schedule.weekly_on")); expect(button(message("common.done"))?.disabled).toBe(true);
  await click("周五"); await click(message("common.done"));
  expect(save).toHaveBeenCalledWith(expect.objectContaining({time:"09:30",weekdays:[5],max_runs:4,timezone:"UTC"}));
});
it("keeps manual pauses banner-free and places missing-target recovery beneath the warning", async () => {
  const t = task({status:"paused",reason:"completed_while_paused"}); const r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
  expect(host.querySelector(".banner.warn")).toBeNull(); expect(host.querySelector(".pri")).toBeNull();
  await act(async()=>{ t.reason="execution_session_missing";t.spec.target={type:"resume_session",session:"missing"};r.touch(); });
  expect(host.querySelector(".pri")?.textContent).toBe(message("schedule.resume"));
});
it("replaces the wide session timeline with a task list and keeps navigation tabs", async () => {
  const r=remote(); await mount(<Sidebar remote={r} active="tasks" selectedTask="tasks/mac/task-1" go={()=>{}} />);
  expect(host.querySelector(".schedule-sidebar")).not.toBeNull(); expect(host.querySelector(".schedule-selected")).not.toBeNull();
  expect(host.querySelector(".side-top .mini")?.textContent).toBe(message("home.new_schedule"));
  expect(host.querySelector(".sfoot .seg")?.textContent).toContain(message("session.title"));
  expect(host.querySelector(".sfoot .seg .on")?.textContent).toContain(message("home.schedules_tab"));
});

it("renders scheduled queue metadata from the accepted event and keeps cancellation available", async () => {
  const r = remote(); const session = r.sessions.get("s-idle")!;
  const when = new Date(2026,9,30,15).getTime();
  const events = [create(EventSchema,{seq:1n,sessionId:session.id,time:timestampFromMs(when),origin:{scheduledTaskId:"task-1",scheduledRunId:"run-1"},kind:{case:"inputState",value:{requestId:"scheduled-input",text:"检查新回复",mode:SendMode.QUEUE,status:InputStatus.QUEUED}}})];
  vi.spyOn(r,"events").mockReturnValue(events);
  const command = vi.spyOn(r,"command").mockResolvedValue(create(CommandResultSchema));
  await mount(<PendingBubbles remote={r} session={session} d={derive(events,Agent.CLAUDE)} />);
  expect(host.querySelector(".pending-meta")?.textContent).toContain("定时任务 · 15:00 · 这一轮结束后发");
  await click(message("common.cancel"));
  expect(command).toHaveBeenCalledWith(session.id,expect.objectContaining({case:"cancelInput",value:{sessionId:session.id,requestId:"scheduled-input"}}));
});

it("keeps the complete quiet-day count in the preview and can show every execution", async () => {
  const r = remote([task({runs:Array.from({length:38},(_,i)=>run(`quiet-${i}`))})]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={()=>{}} onBack={()=>{}} />);
  expect(button(/38 次，都没有通知/)).toBeTruthy();
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(1);
  await click("查看全部 38 次");
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(38);
});
it("offers a return to the owning task from a wide run conversation", async () => {
  const r = remote();const session = r.sessions.get("s-idle")!;
  session.info!.taskOwned = true;session.info!.scheduledTaskId = "task-1";
  const back = vi.fn();
  await mount(<SessionPage remote={r} session={session} wide onBack={back} />);
  const control=host.querySelector<HTMLButtonElement>('.schedule-run-back');
  expect(control?.getAttribute("aria-label")).toBe(message("common.back"));
  await act(async()=>control!.click());expect(back).toHaveBeenCalledOnce();
});

it.each([0, 1, 2, 400])("explains missing-target failures using that run's local start date (%s days ago)", async daysAgo => {
  const now = new Date(2026, 9, 30, 16);
  vi.spyOn(Date, "now").mockReturnValue(now.getTime());
  const started = new Date(2026, 9, 30 - daysAgo, 15);
  const expectedDay = daysAgo === 0 ? "今天" : daysAgo === 1 ? "昨天" : daysAgo === 2 ? "10月28日" : "2025年9月25日";
  const failed = run("missing-target", {started_at: started.toISOString(), status:"failed", reason:"execution_session_missing", session_id:undefined});
  const t = task({status:"failed", reason:"execution_session_missing", runs:[
    run("later-completed", {started_at:now.toISOString()}),
    run("unrelated-failure", {started_at:new Date(now.getTime()-60_000).toISOString(), status:"failed", reason:"execution_failed"}),
    failed,
  ]});
  t.spec.target = {type:"resume_session",session:"missing"};
  const r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
  expect(host.querySelector(".banner.warn")?.textContent).toBe(`${expectedDay} 15:00 的运行失败，任务已停下：找不到运行的对话。这个任务只发到原来的对话，恢复后还会失败。`);
  expect(host.querySelector(".pri")?.textContent).toBe(message("schedule.resume"));
});
it("does not invent a failure time when a missing-target task has no recorded run", async () => {
  const t = task({status:"failed",reason:"execution_session_missing"});
  t.spec.target = {type:"resume_session",session:"missing"};
  await mount(<ScheduleDetail remote={remote([t])} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
  expect(host.querySelector(".banner.warn")?.textContent).toBe("任务已停下：找不到运行的对话。这个任务只发到原来的对话，恢复后还会失败。");
});
it("preserves other failure banner wording", async () => {
  const t = task({status:"failed",reason:"relay_session_limit"});
  await mount(<ScheduleDetail remote={remote([t])} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
  expect(host.querySelector(".banner.warn")?.textContent).toBe("中继上的会话数已满。恢复或重新安排后从下一次自动时间开始，不重发上次消息。");
});

it("defaults completion notifications on, submits opt-out, and edits the full snapshot", async () => {
  const r = remote([]);
  const write = vi.spyOn(r, "changeSchedule").mockResolvedValue(task());
  await mount(<ScheduleCreate remote={r} session={r.sessions.get("s-idle")!} go={()=>{}} onBack={()=>{}} />);
  const toggle = host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="完成后通知"]')!;
  expect(toggle.getAttribute("aria-checked")).toBe("true");
  await act(async () => toggle.click());
  await input(host.querySelector("textarea")!, "检查完成通知");
  await click(message("schedule.create"));
  expect(write.mock.calls[0][2].spec?.notify).toBe("agent_only");
  await act(async () => root!.unmount()); root = undefined; host.remove();
  const original = task({ spec: { ...task().spec, notify: "agent_only", late_seconds: 123 } });
  r.schedules.set("mac", { tasks: [original], revision: 1n, loading: false, error: "" });
  write.mockClear();
  await mount(<ScheduleDetail remote={r} machineId="mac" id={original.id} go={()=>{}} onBack={()=>{}} />);
  const edit = host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="完成后通知"]')!;
  expect(edit.getAttribute("aria-checked")).toBe("false");
  await act(async () => edit.click());
  expect(write.mock.calls[0][2].spec).toEqual({ ...original.spec, notify: "finish" });
});

it("shows the latest notified preview instead of a newer quiet result, caps unread, and retains offline metadata", async () => {
  const r = remote([]);
  let tasks: ScheduleTask[] = [];
  vi.spyOn(r, "machineCommand").mockImplementation(async () => create(CommandResultSchema, {
    kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify({ tasks })) } },
  }));
  await r.refreshSchedules("mac");
  const ended_at = new Date().toISOString();
  const longTitle = "检查发布记录并汇总需要注意的变化".repeat(4);
  const summary = "发布说明有值得关注的变化，请核实后再更新。".repeat(10);
  tasks = [task({
    spec: { ...task().spec, title: longTitle },
    runs: [
      ...Array.from({ length: 105 }, (_, i) => run(`n-${i}`, { notified: true, ended_at, notification_text: summary, final_summary: "最终摘要" })),
      run("quiet-newer", { started_at: new Date(Date.now() + 60_000).toISOString(), final_summary: "静默结果" }),
    ],
  })];
  await r.refreshSchedules("mac");
  r.machines.get("mac")!.online = false;
  await mount(<SchedulesPage remote={r} go={() => {}} />);
  // A new result is a card: its blue dot in its head, four lines of it, 看全文 ›.
  const row = host.querySelector(".rc.schedule-line")!;
  expect(row.classList.contains("unread")).toBe(true);
  expect(row.querySelector('.row1 [aria-label="有新结果"]')).not.toBeNull();
  expect(row.querySelector(".go")?.textContent).toBe(message("schedule.read_full_text"));
  expect(row.querySelector(".row1 b")?.textContent).toBe(longTitle);
  expect(row.querySelector(".row1 > span")?.textContent).toBe(scheduleTime(ended_at));
  expect(row.querySelector(".pv")?.textContent).toBe(summary);
  expect(row.querySelector(".schedule-unread")).toBeNull();
  expect(row.querySelector('[aria-label="有新结果"]')).not.toBeNull();
  expect(row.querySelector(".mc")?.textContent).toContain(message("list_state.unlinked"));
  expect(row.querySelector(".mc")?.textContent).not.toContain("Claude Code");
  expect(row.querySelector(".mc")?.textContent).not.toContain("下次");
  tasks[0].status = "failed";
  tasks[0].reason = "relay_session_limit";
  await act(async () => { r.schedules.get("mac")!.tasks = tasks; r.touch(); });
  expect(host.querySelector(".rc.warn .pv")?.textContent).toBe("已停下 · 中继上的会话数已满");
  expect(host.querySelector(".rc.warn .go")?.textContent).toBe(message("schedule.take_action"));
});

it("captures unread dots when a deep-linked detail receives its first task snapshot", async () => {
  const r = remote([]);
  let tasks: ScheduleTask[] = [];
  const command = vi.spyOn(r, "machineCommand").mockImplementation(async () => create(CommandResultSchema, {
    kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify({ tasks })) } },
  }));
  await r.refreshSchedules("mac");
  let finish!: (value: ReturnType<typeof create<typeof CommandResultSchema>>) => void;
  command.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  const loading = r.refreshSchedules("mac");
  await mount(<StrictMode><ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} /></StrictMode>);
  expect(host.querySelector(".schedule-unread-dot")).toBeNull();
  tasks = [task({ runs: [run("entry", { notified: true })] }), task({ id: "other", runs: [run("other", { notified: true })] })];
  await act(async () => {
    finish(create(CommandResultSchema, { kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify({ tasks })) } } }));
    await loading;
  });
  expect(host.querySelectorAll(".schedule-unread-dot")).toHaveLength(1);
  expect(r.scheduleNewResults()).toBe(true);
  tasks[0].runs!.push(run("during", { notified: true }));
  await act(async () => { await r.refreshSchedules("mac"); });
  expect(host.querySelectorAll(".schedule-unread-dot")).toHaveLength(1);
  expect(r.scheduleNewResults()).toBe(true);
});

it.each([
  [new Date(2026, 10, 1, 13, 59, 40), "13:59"],
  [new Date(2026, 10, 1, 13, 57), "13:57"],
  [new Date(2026, 10, 1, 12), "12:00"],
  [new Date(2026, 9, 31, 12), "昨天"],
  [new Date(2026, 9, 28, 12), "周三"],
])("uses the schedule calendar formatter for result time %s", async (at, expected) => {
  const r = remote([]);
  const t = task({ runs: [run("notice", { notified: true, ended_at: at.toISOString() })] });
  await mount(<ScheduleRow remote={r} task={t} machineId="mac" go={() => {}} now={new Date(2026, 10, 1, 14).getTime()} />);
  expect(host.querySelector(".row1 > span")?.textContent).toBe(expected);
});

it("connects session/detail read transitions to exact notification cleanup without making API errors visible", async () => {
  const getNotifications = vi.fn().mockImplementation(async ({ tag }: { tag: string }) => [{ tag, close: vi.fn() }]);
  // Simulated worker API only; this test does not control a browser.
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ getNotifications }) } });
  const r = remote([]);
  let tasks: ScheduleTask[] = [];
  vi.spyOn(r, "machineCommand").mockImplementation(async () => create(CommandResultSchema, {
    kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify({ tasks })) } },
  }));
  await r.refreshSchedules("mac");
  const session = r.sessions.get("s-idle")!;
  tasks = [task({ runs: [run("visible", { notified: true, session_id: session.id, notification_seq: 7 }), run("other", { notified: true, notification_seq: 8 })] })];
  await r.refreshSchedules("mac");
  expect(getNotifications).not.toHaveBeenCalled();
  await mount(<SessionPage remote={r} session={session} wide={false} onBack={() => {}} />);
  expect(getNotifications).toHaveBeenCalledExactlyOnceWith({ tag: `${session.id}/scheduled:7/NOTIFY_KIND_SCHEDULED` });
  expect(r.scheduleNewResults()).toBe(true);
  getNotifications.mockRejectedValueOnce(new Error("notification API failed"));
  await act(async () => root!.render(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />));
  expect(getNotifications).toHaveBeenLastCalledWith({ tag: "session-other/scheduled:8/NOTIFY_KIND_SCHEDULED" });
  expect(r.scheduleBadge()).toBe(0);
  expect(host.textContent).not.toContain("notification API failed");
});

it("renders verified run history as full Markdown and quietly falls back while loading or unavailable", async () => {
  const execution = run("markdown", { session_id: "s-idle", notified: true, notification_text: "检查完成", final_summary: "短摘要" });
  const r = remote([task({ runs: [execution] })]);
  const release = vi.fn();
  vi.spyOn(r, "ensureSession").mockResolvedValue();
  const retain = vi.spyOn(r, "retainHistory").mockReturnValue(release);
  const history = vi.spyOn(r, "history").mockReturnValue({ loaded: false, ready: false, backfilling: false, broken: false, error: "", unavailable: 0 });
  const full = "完整回复\n\n| 项目 | 结果 |\n| --- | --- |\n| 检查 | 通过 |\n\n- 第一项\n- 第二项\n\n```sh\necho ok\n```";
  vi.spyOn(r, "events").mockReturnValue([
    create(EventSchema, { origin: { scheduledRunId: execution.id }, kind: { case: "userMessage", value: { text: "检查" } } }),
    create(EventSchema, { kind: { case: "agentMessage", value: { text: full, phase: "final_answer" } } }),
  ]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id="task-1" go={() => {}} onBack={() => {}} />);
  expect(retain).toHaveBeenCalledWith("s-idle");
  expect(host.querySelector(".schedule-result .ai")?.textContent).toBe("短摘要");
  history.mockReturnValue({ loaded: true, ready: true, backfilling: false, broken: false, error: "", unavailable: 0 });
  await act(async () => r.touch());
  expect(host.querySelector(".schedule-result table")?.textContent).toContain("检查通过");
  expect(host.querySelectorAll(".schedule-result li")).toHaveLength(2);
  expect(host.querySelector(".schedule-result pre")?.textContent).toBe("echo ok");
  history.mockReturnValue({ loaded: true, ready: true, backfilling: false, broken: true, error: "private read failure", unavailable: 0 });
  await act(async () => r.touch());
  expect(host.querySelector(".schedule-result .ai")?.textContent).toBe("短摘要");
  expect(host.textContent).not.toContain("private read failure");
  await act(async () => root!.render(null));
  expect(release).toHaveBeenCalledOnce();
});

it("keeps the latest card unchanged until the new-result prompt is clicked", async () => {
  const old = run("old", { notified: true, ended_at: "2026-11-01T08:00:00Z", notification_source: "agent", notification_text: "原结果", session_id: undefined });
  const t = task({ runs: [old] }), r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  const next = run("new", { notified: true, ended_at: "2026-11-01T09:00:00Z", notification_source: "agent", notification_text: "后来结果", session_id: undefined });
  await act(async () => { t.runs = [old, next]; r.touch(); });
  expect(host.querySelector(".schedule-result h4")?.textContent).toBe("原结果");
  await click(message("home.ios_new_results"));
  expect(host.querySelector(".schedule-result h4")?.textContent).toBe("后来结果");
  expect(button(message("home.ios_new_results"))).toBeUndefined();
});

it("marks only the recent group read, retains the attention dot, and exposes keyboard reading actions", async () => {
  const r = remote([]);
  let tasks: ScheduleTask[] = [];
  vi.spyOn(r, "machineCommand").mockImplementation(async () => create(CommandResultSchema, {
    kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify({ tasks })) } },
  }));
  await r.refreshSchedules("mac");
  tasks = [task({ id: "attention", status: "failed", runs: [run("a", { notified: true })] }), task({ id: "recent", runs: [run("r", { notified: true })] }), task({ id: "recent-2", runs: [run("r2", { notified: true })] })];
  await r.refreshSchedules("mac");
  const go = vi.fn();
  await mount(<SchedulesPage remote={r} go={go} />);
  expect(host.querySelector('.attention .row1 [aria-label="需要你处理"]')).not.toBeNull();
  expect(host.querySelector(".hl-sub")?.textContent).toBe("另有 2 个新结果");
  expect(host.querySelectorAll('.attention .schedule-unread-dot')).toHaveLength(1);
  await click(message("schedule.mark_all_as_read"));
  expect(r.scheduleHasNew("mac", tasks[0])).toBe(true);
  expect(r.scheduleHasNew("mac", tasks[1])).toBe(false);
  expect(r.scheduleHasNew("mac", tasks[2])).toBe(false);
  expect(button(message("schedule.mark_all_as_read"))).toBeUndefined();
  const unread = host.querySelector<HTMLButtonElement>('.schedule-read-action[aria-label="标为未读"]')!;
  await act(async () => unread.click());
  expect(r.scheduleUnread("mac", tasks[1])).toEqual(new Set(["r"]));
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
  expect(host.querySelectorAll(".schedule-result .ai > [hidden]")).toHaveLength(0);
  expect(host.querySelector(".schedule-result .schedule-expand")).toBeNull();
  measure.mockRestore();
});

it("marks the selected wide row unread through its context menu and exits the detail", async () => {
  const t = task({ runs: [run("latest", { notified: true })] }), r = remote([t]), go = vi.fn();
  vi.spyOn(window, "matchMedia").mockReturnValue({ matches: true } as MediaQueryList);
  await mount(<SchedulesPage remote={r} go={go} sidebar selected="tasks/mac/task-1" />);
  await act(async () => host.querySelector(".schedule-line")!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true })));
  expect(host.querySelector(".schedule-menu")).not.toBeNull();
  await click(message("schedule.mark_as_unread"));
  expect(r.scheduleUnread("mac", t)).toEqual(new Set(["latest"]));
  expect(go).toHaveBeenCalledWith("tasks");
});

it("re-arms a cancelled one-shot from the wide row menu with a new time before submitting", async () => {
  const t = task({ status: "cancelled", reason: "message_cancelled", spec: { ...task().spec, trigger: { type: "at", at: new Date(Date.now() + 3600_000).toISOString() } } });
  const r = remote([t]), write = vi.spyOn(r, "changeSchedule").mockResolvedValue(t);
  vi.spyOn(window, "matchMedia").mockReturnValue({ matches: true } as MediaQueryList);
  await mount(<SchedulesPage remote={r} go={() => {}} sidebar />);
  await act(async () => host.querySelector(".schedule-line")!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true })));
  await click(message("schedule.resume"));
  expect(write).not.toHaveBeenCalled();
  expect(host.querySelector('[role="dialog"]')?.textContent).toContain(message("schedule.when"));
  await click(message("common.done"));
  expect(write).toHaveBeenCalledWith("mac", t.id, expect.objectContaining({ operation: "update", revision: t.revision, spec: expect.objectContaining({ trigger: expect.objectContaining({ type: "at" }) }) }));
});

it("updates a frozen run's summary and end time without replacing its identity", async () => {
  const first = run("finishing", { notified: true, session_id: undefined, ended_at: undefined, final_summary: "正在收尾" });
  const t = task({ runs: [first] }), r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  const completed = { ...first, ended_at: "2026-11-02T14:21:00Z", final_summary: "完成后补齐的摘要" };
  await act(async () => { r.schedules.get("mac")!.tasks = [{ ...t, runs: [completed] }]; r.touch(); });
  expect(host.querySelector(".schedule-result .ai")?.textContent).toBe("完成后补齐的摘要");
  expect(host.querySelector(".schedule-result-head")?.textContent).toContain(dateTime(completed.ended_at));
  expect(button(message("home.ios_new_results"))).toBeUndefined();
});

it("shows the first result at once when the task was entered with none, and holds the next ones", async () => {
  const t = task({ runs: [] }), r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  expect(host.textContent).toContain(message("schedule.has_not_run_yet"));
  const first = run("first", { notified: true, ended_at: "2026-11-01T09:00:00Z", notification_source: "agent", notification_text: "第一个结果", session_id: undefined });
  await act(async () => { t.runs = [first]; r.touch(); });
  expect(host.querySelector(".schedule-result h4")?.textContent).toBe("第一个结果");
  expect(button(message("home.ios_new_results"))).toBeUndefined();
  expect(host.textContent).not.toContain(message("schedule.has_not_run_yet"));
  const next = run("next", { notified: true, ended_at: "2026-11-01T10:00:00Z", notification_source: "agent", notification_text: "第二个结果", session_id: undefined });
  await act(async () => { t.runs = [first, next]; r.touch(); });
  expect(host.querySelector(".schedule-result h4")?.textContent).toBe("第一个结果");
  expect(button(message("home.ios_new_results"))).toBeTruthy();
});

it("follows cached results until the first live list, then freezes only the selected run ID", async () => {
  const cached = run("cached", { notified: true, session_id: undefined, notification_source: "agent", notification_text: "缓存结果", ended_at: "2026-11-01T08:00:00Z" });
  const t = task({ runs: [cached] }), r = remote([t]);
  vi.mocked(r.hasLiveSchedules).mockReturnValue(false);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  expect(host.querySelector(".schedule-result h4")?.textContent).toBe("缓存结果");
  const live = run("live", { notified: true, session_id: undefined, notification_source: "agent", notification_text: "在线结果", ended_at: "2026-11-01T09:00:00Z" });
  await act(async () => { vi.mocked(r.hasLiveSchedules).mockReturnValue(true); r.schedules.get("mac")!.tasks = [{ ...t, runs: [cached, live] }]; r.touch(); });
  expect(host.querySelector(".schedule-result h4")?.textContent).toBe("在线结果");
  expect(button(message("home.ios_new_results"))).toBeUndefined();
  const next = run("later", { notified: true, session_id: undefined, notification_source: "agent", notification_text: "之后的新结果", ended_at: "2026-11-01T10:00:00Z" });
  await act(async () => { r.schedules.get("mac")!.tasks = [{ ...t, runs: [cached, live, next] }]; r.touch(); });
  expect(host.querySelector(".schedule-result h4")?.textContent).toBe("在线结果");
  expect(button(message("home.ios_new_results"))).toBeTruthy();
});

it.each(["one large paragraph", "| A | B |\n| --- | --- |\n| one | two |"])("retains the first oversized Markdown block: %s", async text => {
  vi.spyOn(window, "matchMedia").mockReturnValue({ matches: false } as MediaQueryList);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(() => new DOMRect(0, 0, 300, 400));
  const t = task({ runs: [run("large", { notified: true, session_id: undefined, final_summary: text })] }), r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  const first = host.querySelector<HTMLElement>(".schedule-result .ai > :first-child")!;
  expect(first.hidden).toBe(false);
  expect(first.textContent).not.toBe("");
  expect(first.classList.contains("schedule-first-block-clipped")).toBe(true);
  await click(/^展开全文/);
  expect(first.hidden).toBe(false);
  expect(first.classList.contains("schedule-first-block-clipped")).toBe(false);
});

it("keeps the detail menu's specified order", async () => {
  const t = task({ runs: [run("result", { notified: true, session_id: undefined })] }), r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="更多"]')!.click());
  expect([...host.querySelectorAll(".schedule-menu .mi")].map(button => button.textContent)).toEqual([message("schedule.run_once_now"), message("schedule.mark_as_unread"), message("schedule.pause"), message("schedule.delete_task")]);
  expect(host.querySelector(".schedule-menu .mi.danger")?.textContent).toBe(message("schedule.delete_task"));
});

it("protects a name-only schedule draft after leaving the creation form", async () => {
  const r = remote([]);
  await mount(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />);
  const name = host.querySelector<HTMLInputElement>('input[aria-label="任务名"]')!;
  await input(name, "还没有提交的任务名");
  await act(async () => root!.render(<SchedulesPage remote={r} go={() => {}} />));
  expect(hasScheduleDrafts(r)).toBe(true);
  await act(async () => root!.render(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />));
  await input(host.querySelector<HTMLInputElement>('input[aria-label="任务名"]')!, "");
  expect(hasScheduleDrafts(r)).toBe(false);
});

it.each(["dispatching", "queued", "running"])("shows no-result copy on the first active run (%s) without duplicating the running status", async status => {
  const active = run("first", { status, ended_at: undefined, notified: false });
  const t = task({ active_run: active, runs: [] }), r = remote([t]);
  await mount(<ScheduleRow remote={r} machineId="mac" task={t} go={() => {}} />);
  expect(host.querySelector(".pv")?.textContent).toBe(message("schedule.no_results_yet"));
  expect(host.querySelector(".mc")?.textContent).toMatch(/^正在运行/);
  expect(host.querySelector(".mc .spin")).not.toBeNull();
  expect(host.querySelector(".row1 > span")?.textContent).toBe("");
});

it("keeps the last result's preview and time while running again, and the never-run copy before the first run", async () => {
  const old = run("old", { notified: true, notification_text: "上次的摘要", ended_at: "2026-11-01T08:02:00Z" });
  const t = task({ runs: [old], active_run: run("current", { status: "running", notified: false }) }), r = remote([t]);
  await mount(<ScheduleRow remote={r} machineId="mac" task={t} go={() => {}} />);
  expect(host.querySelector(".pv")?.textContent).toBe("上次的摘要");
  expect(host.querySelector(".row1 > span")?.textContent).toBe(scheduleTime(old.ended_at!));
  const never = task();
  await act(async () => root!.render(<ScheduleRow remote={r} machineId="mac" task={never} go={() => {}} />));
  expect(host.querySelector(".pv")?.textContent).toBe(`还没有运行 · 下次 ${dateTime(never.next_run_at!)}`);
  expect(host.querySelector(".pv")?.textContent).not.toContain(message("schedule.running"));
});

it.each([
  { source: "daemon" as const, text: "正文开头", summary: "正文开头", heading: false },
  { source: "agent" as const, text: "相同内容 ", summary: " 相同内容", heading: false },
  { source: "agent_and_daemon" as const, text: "独立结论", summary: "完整的正文", heading: true },
  { source: "agent" as const, text: "独立结论", summary: "完整的正文", heading: true },
  { source: "daemon" as const, text: "定时任务运行失败：电脑返回的原因", summary: "失败原因", heading: false },
])("renders result heading only when justified by notification source: $source / $heading", async ({ source, text, summary, heading }) => {
  const t = task({ runs: [run("result", { notified: true, session_id: undefined, notification_source: source, notification_text: text, final_summary: summary })] }), r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  expect(!!host.querySelector(".schedule-result h4")).toBe(heading);
  expect(host.querySelector(".schedule-result .ai")?.textContent).toBe(summary);
});

it.each(["never", "running", "single-result"])("omits the entire earlier group when empty (%s), along with the old notification footnote", async state => {
  const active = run("first", { status: "running", session_id: undefined, notified: false });
  const t = state === "never" ? task() : state === "running" ? task({ active_run: active }) : task({ runs: [run("only", { notified: true, session_id: undefined })] });
  const r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  expect(host.querySelector(".schedule-history")).toBeNull();
  expect([...host.querySelectorAll(".label")].some(label => label.textContent?.includes(message("schedule.earlier")))).toBe(false);
  expect(host.textContent).not.toMatch(/表示已请求通知|不保证送达|记录保留最近/);
});

it("shows the earlier group once a previous execution exists", async () => {
  const t = task({ runs: [run("latest", { notified: true, session_id: undefined, ended_at: "2026-11-01T09:00:00Z" }), run("older", { notified: true, session_id: undefined, ended_at: "2026-11-01T08:00:00Z" })] }), r = remote([t]);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  expect(host.querySelector(".schedule-history .label")?.textContent).toBe("更早1 次");
  expect(host.querySelectorAll(".schedule-history .schedule-run")).toHaveLength(1);
});

it.each(["light", "dark"])("aligns the attention banner, recovery button and cards within the same capped column (%s)", async theme => {
  const t = task({ status: "failed", reason: "relay_session_limit", runs: [run("failure", { notified: true, session_id: undefined, final_summary: "运行失败" })] }), r = remote([t]);
  document.documentElement.dataset.theme = theme;
  await mount(<div className="desk"><main className="dm"><ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} /></main></div>);
  const style = document.createElement("style"); style.textContent = styles; host.append(style);
  const banner = host.querySelector<HTMLElement>(".schedule-attention > .banner")!;
  const recovery = host.querySelector<HTMLElement>(".schedule-recovery")!;
  const card = host.querySelector<HTMLElement>(".schedule-result")!;
  const column = host.querySelector<HTMLElement>(".schedule-detail-content")!;
  expect(banner.parentElement?.parentElement).toBe(column);
  expect(recovery.parentElement).toBe(column);
  expect(card.parentElement).toBe(column);
  expect(banner.parentElement?.nextElementSibling).toBe(recovery);
  const alertStyle = getComputedStyle(banner), cardStyle = getComputedStyle(card), actionStyle = getComputedStyle(recovery);
  expect(getComputedStyle(column).maxWidth).toBe("760px");
  expect(getComputedStyle(banner.parentElement!).marginLeft).toBe(cardStyle.marginLeft);
  expect(getComputedStyle(banner.parentElement!).marginRight).toBe(cardStyle.marginRight);
  expect(actionStyle.paddingLeft).toBe(cardStyle.marginLeft);
  expect(actionStyle.paddingRight).toBe(cardStyle.marginRight);
  // The banner's text starts where the card's does; on a wide window a banner is 10 round, a card 14.
  expect(alertStyle.paddingLeft).toBe(cardStyle.paddingLeft);
  expect(alertStyle.borderRadius).toBe("10px");
  expect(cardStyle.borderRadius).toBe("14px");
  expect(alertStyle.backgroundColor).not.toBe(getComputedStyle(column.parentElement!).backgroundColor);
  expect(alertStyle.fontSize).toBe("13px");
  expect(alertStyle.color).toBe(getComputedStyle(document.documentElement).getPropertyValue("--orange-ink").trim());
  delete document.documentElement.dataset.theme;
});

it("does not put a running status in the preview when the run record is present before active_run", async () => {
  const t = task({ runs: [run("pending", { status: "running", notified: false, ended_at: undefined })] }), r = remote([t]);
  await mount(<ScheduleRow remote={r} machineId="mac" task={t} go={() => {}} />);
  expect(host.querySelector(".pv")?.textContent).toBe(message("schedule.no_results_yet"));
});

it.each(["offline", "list-error"])("shows the latest cached result without reading it or showing entry dots (%s)", async mode => {
  const t = task({ runs: [run("cached", { notified: true, session_id: "s-idle", notification_source: "agent", notification_text: "缓存结论", final_summary: "缓存正文" })] });
  const r = remote([]);
  vi.mocked(r.hasLiveSchedules).mockReturnValue(false);
  localStorage.setItem("ra-schedule-seen:" + JSON.stringify([r.account.server, r.account.deviceId, "mac"]), "[]");
  vi.spyOn(r as any, "openSecureMessage").mockResolvedValue({ msg: create(CommandResultSchema, { kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify({ tasks: [t] })) } } }) });
  await (r as any).restoreScheduleSnapshot("mac", { bytes: toBinary(EnvelopeSchema, create(EnvelopeSchema)), requestId: "cache" });
  vi.spyOn(r, "ensureSession").mockResolvedValue();
  vi.spyOn(r, "retainHistory").mockReturnValue(() => {});
  vi.spyOn(r, "history").mockReturnValue({ loaded: true, ready: true, backfilling: false, broken: false, error: "", unavailable: 0 });
  vi.spyOn(r, "events").mockReturnValue([
    create(EventSchema, { origin: { scheduledRunId: "cached" }, kind: { case: "userMessage", value: { text: "定时检查" } } }),
    create(EventSchema, { kind: { case: "agentMessage", value: { phase: "final_answer", text: "缓存的完整回复" } } }),
  ]);
  if (mode === "offline") { r.conn = "offline"; r.machines.get("mac")!.online = false; }
  else r.schedules.get("mac")!.error = "读取失败";
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={() => {}} onBack={() => {}} />);
  expect(host.querySelector(".schedule-result h4")?.textContent).toBe("缓存结论");
  expect(host.querySelector(".schedule-result .ai")?.textContent).toBe("缓存的完整回复");
  expect(host.querySelector(".schedule-unread-dot")).toBeNull();
  expect(host.querySelector(".schedule-history")).toBeNull();
  expect(r.scheduleUnread("mac", t)).toEqual(new Set(["cached"]));
  await act(async () => { r.setScheduleSession("s-idle"); r.touch(); });
  expect(r.scheduleUnread("mac", t)).toEqual(new Set(["cached"]));
});

it("uses the latest task at the instant a stale menu action is invoked", async () => {
  const old = run("old", { notified: true, session_id: undefined, ended_at: "2026-11-01T08:00:00Z" });
  const next = run("next", { notified: true, session_id: undefined, ended_at: "2026-11-01T09:00:00Z" });
  const before = task({ runs: [old] }), current = { ...before, runs: [old, next] };
  const r = remote([before]), go = vi.fn();
  r.markScheduleRead("mac", current);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={before.id} go={go} onBack={() => {}} />);
  await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="更多"]')!.click());
  const control = [...host.querySelectorAll<HTMLButtonElement>(".schedule-menu .mi")].find(button => button.textContent === message("schedule.mark_as_unread"))!;
  // The store has advanced, but this DOM action still closes over the old task.
  r.schedules.get("mac")!.tasks = [current];
  await act(async () => control.click());
  expect(r.scheduleUnread("mac", current)).toEqual(new Set(["next"]));
  expect(go).toHaveBeenCalledWith("tasks");
});
