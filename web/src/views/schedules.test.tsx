// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import {
  ScheduleCreate,
  ScheduleDetail,
  ScheduleStrip,
  SchedulesPage,
  TimingSheet,
} from "./Schedules";
import { SessionPage } from "./Session";
import { task, run } from "../test/schedule-fixture";
import { clockTime, scheduleOffline, scheduleEmpty } from "../schedules";
import { ApprovalMode, Agent } from "../gen/remoteagent/v1/event_pb";
import type { ScheduleTask } from "../schedules";

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement;
const remotes: DemoRemote[] = [];
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  host?.remove();
  for (const r of remotes.splice(0)) r.stop();
  vi.restoreAllMocks();
  localStorage.clear();
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
  const b = button(text);
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
  expect(host.textContent!.indexOf("需要你看一下")).toBeLessThan(
    host.textContent!.indexOf("进行中"),
  );
  expect(host.querySelectorAll(".sess")).toHaveLength(2);
  await click(/失败任务/);
  expect(go).toHaveBeenCalledWith("tasks/mac/bad");
  await act(async () => {
    r.machines.get("mac")!.online = false;
    r.touch();
  });
  expect(host.textContent).toContain(scheduleOffline);
  expect(host.querySelectorAll(".sess.off")).toHaveLength(2);
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
  expect(button("立即运行一次")?.disabled).toBe(true);
  expect(button("删除任务")?.disabled).toBe(true);
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
  await click("删除");
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
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(2);
  await click(/2 次，都没有通知/);
  expect(host.querySelectorAll(".schedule-run")).toHaveLength(4);
  await click(new RegExp(clockTime(runs[0].started_at) + ".*没有通知"));
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
  expect(host.textContent).toContain("中继上的会话数已满");
  expect([...host.querySelectorAll(".pri")].map((b) => b.textContent)).toEqual([
    "恢复",
  ]);
  await click("恢复");
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
  await click("每周");
  await click("周五");
  const select = host.querySelector("select")!;
  await act(async () => {
    select.value = "count";
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await input(host.querySelector('input[type="number"]')!, "6");
  await click("完成");
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
  expect(host.textContent).not.toContain("机器和模型");
  await input(host.querySelector("textarea")!, "检查新回复，有结果通知我");
  await click("创建");
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
  await click("定时发送…");
  expect(
    host.querySelector(".schedule-session-overlay")?.textContent,
  ).toContain("定时发到这个对话");
});

it("preserves message and timing drafts when a viewport layout remounts the form", async () => {
  const r = remote([]);
  const props = { remote: r, go: () => {}, onBack: () => {} };
  await mount(<ScheduleCreate {...props} />);
  await input(host.querySelector("textarea")!, "保留这条消息");
  await click(/^什么时候/);
  await click("每隔");
  await input(host.querySelector('input[type="number"]')!, "15");
  await click("完成");
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
  await click("重新安排");
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
  await click("保存");
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
  expect(button("创建")?.disabled, host.textContent ?? "").toBe(false);
  await click("创建");
  if (host.querySelector('[role="alertdialog"]')) await click("完全放行");
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
  await click("每隔");
  await input(host.querySelector('input[type="number"]')!, "15");
  await click("完成");
  expect(save).toHaveBeenCalledWith({
    type: "interval",
    interval_seconds: 900,
    delay_seconds: 900,
  });
});

it("writes the conversation context in the schedule list's second line", async () => {
  const t = task();
  t.spec.target = { type: "resume_session", session: "s-idle" };
  const r = remote([t]);
  await mount(<SchedulesPage remote={r} go={() => {}} />);
  const title = r.sessions.get("s-idle")!.info!.title;
  expect(host.querySelector(".sess .pv")?.textContent).toContain(
    `在「${title}」里`,
  );
});
it("creates with an optional name above the message and preserves that name in the draft", async () => {
  const r = remote([]);
  const session = r.sessions.get("s-idle")!;
  const write = vi.spyOn(r, "changeSchedule").mockResolvedValue(task());
  const props = { remote: r, session, go: () => {}, onBack: () => {} };
  await mount(<ScheduleCreate {...props} />);
  const name = host.querySelector<HTMLInputElement>(
    'input[aria-label="任务名"]',
  )!;
  expect(name.placeholder).toBe("留空就用消息的第一句");
  expect(
    [...host.querySelectorAll(".schedule-body > label, .schedule-body label")]
      .slice(0, 2)
      .map((el) => el.textContent),
  ).toEqual(["任务名", "让 agent 做什么"]);
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
  await click("创建");
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
  await click("保存");
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
  await click("保存");
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
  ).toEqual(["任务名", "什么时候", "在哪运行", "权限", "消息"]);
  expect(button(/^什么时候/)?.textContent).toBe("什么时候每天 09:49");
  const zone = local === "UTC" ? "America/Los_Angeles" : "UTC";
  await act(async () => {
    t.spec.trigger.timezone = zone;
    r.touch();
  });
  expect(button(/^什么时候/)?.textContent).toBe(`什么时候每天 09:49 · ${zone}`);
  await click(/^什么时候/);
  expect(
    host.querySelector<HTMLInputElement>('input[aria-label="时区"]')?.value,
  ).toBe(zone);
  await input(
    host.querySelector<HTMLInputElement>('input[aria-label="时区"]')!,
    local,
  );
  await click("完成");
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
  await click("保存");
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
  await click("保存");
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
  await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
  expect(host.textContent).toContain("共 5 次，或到 ");
  expect(host.textContent).toContain("你创建");
  expect(host.textContent).not.toContain("接管");
  expect(button("立即运行一次")?.disabled).toBe(true);
  for (const label of [/^什么时候/,/^消息/,/^在哪运行/]) expect(button(label)?.disabled).toBe(true);
  expect(button(/^任务名/)?.disabled).toBe(false);
  await act(async()=>{r.machines.get("mac")!.online=false;r.touch();});
  expect(host.querySelector(".navbar")?.textContent).toContain(scheduleOffline);
  expect(host.querySelector(".navbar")?.textContent).not.toContain("运行中");
});
it("uses shared empty copy and resolves a missing conversation before naming it", async () => {
  const r=remote([]);
  await mount(<SchedulesPage remote={r} go={()=>{}} />);
  expect(host.textContent).toContain(scheduleEmpty);
  const t=task();t.spec.target={type:"resume_session",session:"not-loaded"};
  r.schedules.get("mac")!.tasks=[t];
  await act(async()=>r.touch());
  expect(host.querySelector(".sess .pv")?.textContent).toContain("在对话里");
  expect(host.querySelector(".sess .pv")?.textContent).toContain(t.spec.trigger.timezone);
  r.sessions.set("not-loaded",{id:"not-loaded",machineId:"mac",key:{} as CryptoKey,metaRev:0n,lastSeq:0n,updatedMs:0n,endedMs:0n});
  await act(async()=>r.touch());
  expect(host.querySelector(".sess .pv")?.textContent).toContain("在对话里");
});
it("keeps the original interval phase and both unchanged end limits", async () => {
  const trigger={type:"interval" as const,interval_seconds:900,at:"2026-10-01T08:00:00Z",delay_seconds:29,max_runs:5,until:"2027-10-30T08:00:29Z"};
  const save=vi.fn();await mount(<TimingSheet trigger={trigger} onSave={save} onClose={()=>{}} />);
  await click("完成");expect(save).toHaveBeenCalledWith(trigger);
  await input(host.querySelectorAll<HTMLInputElement>('input[type="number"]')[1],"6");
  await click("完成");expect(save.mock.calls.at(-1)![0]).toMatchObject({at:trigger.at,delay_seconds:29,max_runs:6,until:trigger.until});
});
it("submits the editor's snapshot revision and spec even after remote changes", async () => {
  const old=task();const r=remote([old]);const send=vi.spyOn(r,"changeSchedule").mockResolvedValue(old);
  await mount(<ScheduleDetail remote={r} machineId="mac" id={old.id} go={()=>{}} onBack={()=>{}} />);
  await click(/^消息/);
  await act(async()=>{r.schedules.get("mac")!.tasks=[task({revision:9,spec:{...old.spec,title:"别人改名"}})];r.touch();});
  await input(host.querySelector("textarea")!,"我的修改");await click("保存");
  expect(send.mock.calls[0][2]).toMatchObject({revision:1,spec:{...old.spec,message:"我的修改"}});
});
it("keeps plan permission visible and disables scheduled creation", async () => {
  const r=remote([]);r.sessions.clear();
  for(const m of r.machines.values())for(const agent of m.meta?.agents??[])agent.defaultMode=ApprovalMode.PLAN;
  for(const project of r.prefs.snap?.prefs.settings??[]) for(const agent of project.agents) agent.mode=ApprovalMode.PLAN;
  const send=vi.spyOn(r,"changeSchedule").mockResolvedValue(task());
  await mount(<ScheduleCreate remote={r} go={()=>{}} onBack={()=>{}} />);
  await input(host.querySelector("textarea")!,"检查更新");
  expect(button("创建")?.disabled).toBe(true);
  expect(host.textContent).toContain("计划模式不能创建执行任务");expect(send).not.toHaveBeenCalled();
});

it("derives an empty quiet summary and pending-only run guard from the task",async()=>{
  const t=task({runs:[run("q1",{notified:false,final_summary:""}),run("q2",{notified:false,final_summary:""})],manual_request:{request_id:"manual"}});
  const r=remote([t]);await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
  expect(host.textContent).toContain("：没有通知");expect(button("立即运行一次")?.disabled).toBe(true);
});

it("uses the shared new-task explanation in the actual creation page", async () => {
  const r = remote([]);
  await mount(<ScheduleCreate remote={r} go={() => {}} onBack={() => {}} />);
  expect(host.querySelector(".manage-hint")?.textContent).toBe("每次运行新开一个对话，归在这个任务下面，不进会话列表。Agent 觉得值得说的时候才通知你。");
});

it("defaults dual limits to count and preserves the deadline when only the zone changes",async()=>{
 const onSave=vi.fn();const trigger={type:"calendar" as const,time:"08:00",timezone:"America/Los_Angeles",max_runs:5,until:"2027-10-30T08:00:00Z"};
 await mount(<TimingSheet trigger={trigger} onSave={onSave} onClose={()=>{}} />);
 expect((host.querySelector('select[aria-label="结束"]') as HTMLSelectElement)?.value || (host.querySelector("select") as HTMLSelectElement)?.value).toBe("count");
 await input(host.querySelector('input[placeholder="Asia/Shanghai"]')!,"Asia/Shanghai");await click("完成");
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
 await click(/^模型和权限/);expect(host.textContent).toContain("任务设置");expect(button("设为常用")).toBeUndefined();expect(button("移除项目…")).toBeUndefined();await click("完成");
 await click(/^项目/);expect(button("另一个电脑")).toBeUndefined();expect(button("MacBook")?.disabled).toBe(true);await click("取消");
 await click("Codex");await click("保存");
 expect(send.mock.calls[0][2].spec?.target).toMatchObject({agent:"codex",model:"",mode:"auto"});
});

it("shows the shared destination placeholder until target metadata is loaded",async()=>{
 const t=task();t.spec.target={type:"resume_session",session:"missing"};const r=remote([t]);
 r.sessions.set("missing",{id:"missing",machineId:"mac",updatedMs:0n,key:{} as CryptoKey,metaRev:0n,lastSeq:0n,endedMs:0n});
 await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
 expect(button("发到对话")).toBeTruthy();
});

it("closes a stale editor when the daemon reports a revision conflict",async()=>{
 const t=task();const r=remote([t]);vi.spyOn(r,"changeSchedule").mockRejectedValue(new Error("任务刚被修改，已更新为最新内容"));
 await mount(<ScheduleDetail remote={r} machineId="mac" id={t.id} go={()=>{}} onBack={()=>{}} />);
 await click(/^消息/);await input(host.querySelector("textarea")!,"新消息");await click("保存");
 expect(host.querySelector('[role="dialog"]')).toBeNull();expect(host.textContent).toContain("任务刚被修改，已更新为最新内容");
});

it.each(["2027-10-24T15:00:00Z","2027-10-24T15:00:00+08:00"])("uses the local IANA zone when converting timestamp %s to a calendar",async(at)=>{
 const save=vi.fn();await mount(<TimingSheet trigger={{type:"at",at}} onSave={save} onClose={()=>{}} />);
 await click("每天");await click("完成");
 expect(save.mock.calls[0][0].timezone).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
});

it("requires a concrete permission when neither project nor CLI has a known default",async()=>{
 const r=remote([]);r.sessions.clear();
 for(const machine of r.machines.values()) for(const agent of machine.meta?.agents??[]) {agent.defaultMode=ApprovalMode.UNSPECIFIED;agent.defaultModeSource=0;}
 for(const setting of r.prefs.snap?.prefs.settings??[]) for(const agent of setting.agents) agent.mode=ApprovalMode.UNSPECIFIED;
 await mount(<ScheduleCreate remote={r} go={()=>{}} onBack={()=>{}} />);await input(host.querySelector("textarea")!,"检查");
 expect(button("创建")?.disabled).toBe(true);expect(host.textContent).toContain("请选择明确的权限，再创建定时任务。");
});

it.each([31,62,123])("preserves an unchanged %i-second interval exactly", async seconds => {
  const trigger={type:"interval" as const,interval_seconds:seconds,delay_seconds:29,at:"2026-10-01T08:00:00Z"};
  const save=vi.fn();await mount(<TimingSheet trigger={trigger} onSave={save} onClose={()=>{}} />);
  await click("完成");expect(save).toHaveBeenLastCalledWith(trigger);
  await input(host.querySelector<HTMLInputElement>('input[type="number"]')!,String(seconds/60+1));
  await click("完成");expect(save).toHaveBeenLastCalledWith({type:"interval",interval_seconds:seconds+60,delay_seconds:seconds+60});
  await input(host.querySelector<HTMLInputElement>('input[type="number"]')!,"0.511");
  save.mockClear();await click("完成");expect(save).not.toHaveBeenCalled();expect(host.textContent).toContain("间隔至少为 1 秒");
});
it("converts cutoff display across zones while retaining the absolute instant", async () => {
  const trigger={type:"calendar" as const,time:"08:00",timezone:"Asia/Shanghai",until:"2027-10-30T00:15:29Z"};
  const save=vi.fn();await mount(<TimingSheet trigger={trigger} onSave={save} onClose={()=>{}} />);
  const cutoff=()=>host.querySelector<HTMLInputElement>('input[type="datetime-local"]')!;
  const zone=host.querySelector<HTMLInputElement>('input[aria-label="时区"]')!;
  expect(cutoff().value).toBe("2027-10-30T08:15");
  await input(zone,"America/Los_Angeles");expect(cutoff().value).toBe("2027-10-29T17:15");
  await click("完成");expect(save.mock.calls.at(-1)![0].until).toBe(trigger.until);
  await input(zone,"Asia/Tokyo");expect(cutoff().value).toBe("2027-10-30T09:15");
  await input(cutoff(),"2027-11-01T09:30");
  await input(zone,"UTC");expect(cutoff().value).toBe("2027-11-01T00:30");
  await click("完成");expect(save.mock.calls.at(-1)![0].until).toBe("2027-11-01T00:30:00.000Z");
});
