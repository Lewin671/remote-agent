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
import { clockTime } from "../schedules";
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
  expect(host.textContent).toContain("上次的任务状态");
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
it("creates a current-conversation task with only message and time", async () => {
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

it("inherits the native source session used by a new task's selected configuration", async () => {
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
  expect(request.sessionId).toBeTruthy();
  const source = r.sessions.get(request.sessionId!)!;
  expect(source.machineId).toBe(machineId);
  expect(request.spec?.target.cwd).toBe(source.info?.cwd);
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
