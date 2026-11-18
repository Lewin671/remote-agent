import { t as message } from "./i18n";
import { expect, it } from "vitest";
import shared from "../../testdata/schedules/clients.json";
import type { ScheduleTask } from "./schedules";
import {
  clockTime,
  dateTime,
  frequency,
  groupRuns,
  groupTasks,
  lastResult,
  nextText,
  reasonText,
  taskGroup,
  timingText,
  zonedISO,
  scheduleEmpty, scheduleOffline, scheduleOfflineRow, validScheduleMessage,
  taskName,
  runResult,
  runSummary,
} from "./schedules";
import { task, run } from "./test/schedule-fixture";

it("groups tasks after one failed execution and counts only attention", () => {
  const tasks = [
    task(),
    task({ id: "pause", status: "paused", reason: "user_paused" }),
    task({
      id: "failed",
      status: "failed",
      runs: [run("bad", { status: "failed" })],
      reason: "relay_session_limit",
    }),
    task({
      id: "permission",
      status: "paused",
      reason: "agent_schedule_permissions_changed",
    }),
    task({ id: "unknown", status: "uncertain" }),
    task({ id: "done", status: "completed" }),
    task({ id: "cancel", status: "cancelled" }),
  ];
  const groups = groupTasks(
    tasks.map((task) => ({ task, machineId: "computer" })),
  );
  expect(
    groups
      .get("attention")
      ?.map((e) => e.task.id)
      .sort(),
  ).toEqual(["failed", "permission", "unknown"]);
  expect(groups.get("recent")?.map((e) => e.task.id)).toEqual(["pause", "task-1"]);
  expect(groups.get("ended")?.map((e) => e.task.id)).toEqual(["done"]);
  expect(
    taskGroup(task({ status: "cancelled", reason: "message_cancelled" })),
  ).toBe("attention");
  expect(
    groupTasks([
      {
        machineId: "computer",
        task: task({ status: "cancelled", reason: "message_cancelled" }),
      },
    ]).get("attention"),
  ).toHaveLength(1);
  expect(reasonText("relay_session_limit")).toBe(message("schedule.the_relay_s_session_limit_has_been_reached"));
  expect(taskGroup(task({ status: "failed", runs: [run("1")] }))).toBe(
    "attention",
  );
});
it("aggregates today's quiet results without hiding notified or failed runs", () => {
  const now = Date.parse("2026-10-24T12:00:00Z");
  const runs = [
    run("1"),
    run("2", { started_at: "2026-10-24T11:00:00Z", manual: true }),
    run("old", { started_at: "2026-10-23T11:00:00Z", ended_at: "2026-10-23T11:01:00Z" }),
  ];
  const t = task({ runs, active_run: runs[1] });
  expect(lastResult(t, now)).toBe("今天 2 次，都没有通知");
  expect(groupRuns(t).map((g) => g.runs.map((r) => r.id))).toEqual([
    ["2", "1"],
    ["old"],
  ]);
  const alert = run("3", {
    notified: true,
    notification_text: "有新回复",
    started_at: "2026-10-24T11:30:00Z",
  });
  expect(lastResult(task({ runs: [...runs, alert] }), now)).toContain(
    "通知了你：有新回复",
  );
  expect(
    groupRuns(task({ runs: [...runs, alert] })).map((g) =>
      g.runs.map((r) => r.id),
    ),
  ).toEqual([["3"], ["2", "1"], ["old"]]);
  expect(
    lastResult(task({ runs: [run("bad", { status: "failed" })] }), now),
  ).toContain(message("attachment.failed"));
});
it("derives next time labels by local date, state and active run", () => {
  const now = Date.parse("2026-10-24T10:00:00Z");
  expect(nextText(task(), now)).toBe(
    `下次 今天 ${clockTime("2026-10-24T15:00:00Z")}`,
  );
  expect(nextText(task({ next_run_at: "2026-10-25T15:00:00Z" }), now)).toBe(
    `下次 ${dateTime("2026-10-25T15:00:00Z", now)}`,
  );
  expect(nextText(task({ status: "failed" }), now)).toBe(message("schedule.stopped"));
  expect(nextText(task({ status: "paused", reason: "user_paused" }), now)).toBe(
    message("queue.paused"),
  );
  expect(
    nextText(task({ active_run: run("live", { status: "queued" }) }), now),
  ).toBe(message("list_state.running"));
  expect(frequency({ type: "interval", interval_seconds: 3600 })).toBe(
    message("schedule.hourly"),
  );
  expect(
    frequency({ type: "calendar", weekdays: [1, 2, 3, 4, 5], time: "08:00" }),
  ).toBe("工作日 08:00");
});

it("converts local times in the selected zone and rejects DST gaps", () => {
  expect(zonedISO("2026-10-24T08:00", "Asia/Shanghai")).toBe(
    "2026-10-24T00:00:00.000Z",
  );
  expect(zonedISO("2026-10-24T08:00", "America/Los_Angeles")).toBe(
    "2026-10-24T15:00:00.000Z",
  );
  expect(() => zonedISO("2026-03-08T02:30", "America/Los_Angeles")).toThrow(
    "夏令时",
  );
  expect(() => zonedISO("2026-10-24T08:00", "bad/zone")).toThrow();
});

it("resolves repeated wall times to the first occurrence in western and eastern zones", () => {
  expect(zonedISO("2027-11-07T01:30", "America/Los_Angeles")).toBe("2027-11-07T08:30:00.000Z");
  expect(zonedISO("2027-10-31T02:30", "Europe/Berlin")).toBe("2027-10-31T00:30:00.000Z");
  expect(() => zonedISO("2027-03-14T02:30", "America/Los_Angeles")).toThrow("这个时区在该时刻发生夏令时跳转，请换一个时间");
});

it("resolves optional task names from the first sentence and keeps explicit names", () => {
  expect(taskName("", "  检查服务器   。后续消息  ")).toBe("检查服务器");
  expect(taskName("  早间巡检  ", "检查服务器。没事不用通知。")).toBe(
    "早间巡检",
  );
  expect(taskName("", "检查服务器。没事不用通知。")).toBe("检查服务器");
  expect(taskName(" ", "Check the service. Notify only on changes.")).toBe(
    "Check the service",
  );
  expect(taskName("", "Check v1.2 status! Next sentence")).toBe(
    "Check v1.2 status",
  );
  expect(taskName("", "第一行\n第二行")).toBe("第一行");
  expect(taskName("每日   检查", "msg")).toBe("每日 检查");
  expect([...taskName("😀".repeat(61), "msg")]).toHaveLength(60);
});
it("only appends a foreign calendar zone to the timing value", () => {
  const tr = {
    type: "calendar" as const,
    time: "09:49",
    timezone: "America/Los_Angeles",
  };
  expect(timingText(tr, "America/Los_Angeles")).toBe("每天 09:49");
  expect(timingText(tr, "Asia/Shanghai")).toBe(
    "每天 09:49（洛杉矶时间）",
  );
  expect(timingText({ ...tr, timezone: "Etc/UTC" }, "UTC")).toBe("每天 09:49");
  expect(timingText({ type: "interval", interval_seconds: 3600 }, "UTC")).toBe(
    message("schedule.hourly"),
  );
});
it("uses the agreed outcome wording and never replaces a failure reason with its final summary", () => {
  const failed = run("bad", {
    status: "failed",
    reason: "relay_session_limit",
    final_summary: "最后的摘要",
    notified: true,
    notification_text: "之前的通知",
  });
  expect(runResult(failed)).toBe(message("attachment.failed"));
  expect(runSummary(failed)).toBe(message("schedule.the_relay_s_session_limit_has_been_reached"));
  expect(lastResult(task({ runs: [failed] }))).toContain(
    "失败：中继上的会话数已满",
  );
  expect(
    runSummary(
      run("unknown-reason", {
        status: "failed",
        reason: undefined,
        final_summary: "无关摘要",
      }),
    ),
  ).toBe(message("schedule.execution_outcome_is_unconfirmed"));
  expect(runResult(run("skip", { status: "skipped" }))).toBe(message("schedule.skipped"));
  expect(runResult(run("quiet"))).toBe(message("schedule.no_notification"));
  expect(runResult(run("notify", { notified: true }))).toBe(message("schedule.notified_you"));
});

it("derives the final shared grouping, attention reason and state copy from tasks", () => {
  expect(scheduleEmpty()).toBe(shared.empty);expect(scheduleOffline()).toBe(shared.offline);
  const tasks = shared.tasks as ScheduleTask[];
  const groups = groupTasks(tasks.map(task => ({ machineId: shared.machine, task })));
  expect(Object.fromEntries([...groups].map(([g, tasks]) => [g, tasks.map(e => e.task.id)]))).toEqual({ "attention": shared.groups["需要你处理"], "recent": ["conversation", "quiet", "paused"], "ended": shared.groups["已结束"] });
  const attention = tasks.find(t => t.id === "attention")!;
  expect(lastResult(attention, Date.parse(shared.now))).toBe(message("schedule.task_creation_permissions_changed_the_task_stopped"));
  expect(runResult(attention.runs![0])).toBe(message("schedule.no_notification"));
  expect(reasonText("completed_while_paused")).toBe(message("schedule.paused_the_schedule_expired_in_the_meantime"));
  for (const status of ["dispatching", "queued", "running"])
    expect(nextText(task({active_run: run("live", {status})}))).toBe(message("list_state.running"));
  expect(nextText(task({next_run_at: undefined}))).toBe(message("schedule.next_run_pending"));
  expect(taskName("", "  检查更新  。第二句")).toBe("检查更新");
  const now = new Date(2026, 9, 24, 12).getTime();
  expect(dateTime(new Date(2026, 9, 25, 8).toISOString(), now)).toBe("明天 08:00");
  expect(nextText(task({next_run_at: new Date(2026, 9, 24, 15).toISOString()}), now)).toBe("下次 今天 15:00");
});
it("uses the shared v52 title and offline-row vectors and UTF-8 message limit", () => {
  expect(scheduleOfflineRow()).toBe(shared.offlineRow);
  for (const [message, expected] of shared.titleFromMessage) expect(taskName("", message)).toBe(expected);
  expect(validScheduleMessage(" ")).toBe(false);
  expect(validScheduleMessage("a".repeat(32768))).toBe(true);
  expect(validScheduleMessage("a".repeat(32769))).toBe(false);
  expect(validScheduleMessage("中".repeat(10923))).toBe(false);
});


it("sorts each group by latest notified run, then preserves the no-notification ordering", () => {
  const notified = (id: string, time: string) => run(id, { notified: true, ended_at: time });
  const tasks = [
    task({ id: "quiet-late", next_run_at: "2026-11-02T12:00:00Z", runs: [run("quiet", { ended_at: "2026-11-03T12:00:00Z" })] }),
    task({ id: "older", next_run_at: "2026-11-01T01:00:00Z", runs: [notified("older", "2026-11-01T12:00:00Z")] }),
    task({ id: "newer", next_run_at: "2026-11-03T01:00:00Z", runs: [notified("old", "2026-10-31T01:00:00Z"), notified("new", "2026-11-01T12:00:00.100Z")] }),
    task({ id: "quiet-early", next_run_at: "2026-11-02T01:00:00Z" }),
  ];
  expect(groupTasks(tasks.map(task => ({ machineId: "m", task }))).get("recent")!.map(e => e.task.id)).toEqual(["newer", "older", "quiet-early", "quiet-late"]);
  for (const status of ["failed", "paused", "completed"]) {
    const entries = tasks.map((t, i) => ({ machineId: "m", task: { ...t, status, updated_at: `2026-11-0${i + 1}T00:00:00Z` } }));
    expect([...groupTasks(entries).values()].flat().map(e => e.task.id)).toEqual(status === "failed" ? ["quiet-early", "quiet-late", "older", "newer"] : ["newer", "older", "quiet-early", "quiet-late"]);
  }
});
it("uses notification summaries even when a newer quiet run exists", async () => {
  const { latestNotification, notificationTime, unreadLabel } = await import("./schedules");
  const notice = run("notice", { notified: true, notification_text: "主动通知", final_summary: "最终摘要", ended_at: "2026-11-01T09:30:00Z" });
  const t = task({ runs: [notice, run("quiet", { started_at: "2026-11-02T01:00:00Z" })] });
  expect(latestNotification(t)?.id).toBe("notice");
  expect(runSummary(latestNotification(t)!)).toBe("主动通知");
  expect(latestNotification(task())).toBeUndefined();
  expect(notificationTime(run("active", { notified: true, ended_at: undefined }))).toBe("2026-10-24T10:00:00Z");
  expect([1, 99, 100, 1000].map(unreadLabel)).toEqual(["1", "99", "99+", "99+"]);
});
