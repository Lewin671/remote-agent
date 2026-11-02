import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import { EventSchema, OriginKind, SessionStatus, type Event } from "./gen/remoteagent/v1/event_pb";
import { ScheduleSeen, runTitle, runSummary, finalRunText, groupTasks, notificationTime, scheduleTime, stoppedTime, timingText } from "./schedules";
import { run, task } from "./test/schedule-fixture";

function store() {
  const values = new Map<string, string>();
  return { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value); } };
}
it("moves completed tasks with new results into recent and back into the folded ended group when read", () => {
  const seen = new ScheduleSeen([], store()), read: string[] = [];
  seen.observe("m", []);
  const ended = task({ status: "completed", runs: [run("result", { notified: true })] });
  seen.observe("m", [ended]);
  const entries = [{ machineId: "m", task: ended }];
  const groups = () => groupTasks(entries, e => seen.hasNew(e.machineId, e.task));
  expect(groups().get("最近")).toHaveLength(1);
  expect(groups().get("已结束")).toHaveLength(0);
  seen.markRead("m", ended);
  expect(groups().get("最近")).toHaveLength(0);
  expect(groups().get("已结束")).toHaveLength(1);
  const cleanup = new ScheduleSeen([], store(), r => read.push(r.id));
  cleanup.observe("m", []); cleanup.observe("m", [ended]); cleanup.markRead("m", ended);
  expect(read).toEqual(["result"]);
});
it("marks only the latest result unread, persists it, and keeps quiet runs out of new results", () => {
  const storage = store(), seen = new ScheduleSeen([], storage);
  const t = task({ runs: [run("old", { notified: true, ended_at: "2026-10-30T08:00:00Z" }), run("latest", { notified: true, ended_at: "2026-11-01T08:00:00Z" }), run("quiet", { ended_at: "2026-11-02T08:00:00Z" })] });
  seen.observe("m", [t]); seen.markUnread("m", t);
  expect(seen.hasNew("m", t)).toBe(true);
  expect(seen.unread("m", t)).toEqual(new Set(["latest"]));
  const restored = new ScheduleSeen([], storage); restored.observe("m", [t]);
  expect(restored.unread("m", t)).toEqual(new Set(["latest"]));
  restored.markRead("m", t); expect(restored.hasNew("m", t)).toBe(false);
  restored.markUnread("m", task({ runs: [run("quiet")] }));
  expect(restored.hasNew("m", t)).toBe(false);
});
it("sorts attention by stop time, recent by result time then next time, and keeps deterministic ties", () => {
  const tasks = [
    task({ id: "no-result-late", next_run_at: "2026-11-02T09:00:00Z" }),
    task({ id: "no-result-early", next_run_at: "2026-11-02T08:00:00Z", status: "paused" }),
    task({ id: "newer", runs: [run("r", { notified: true, ended_at: "2026-11-01T09:00:00Z" })] }),
    task({ id: "older", runs: [run("r", { notified: true, ended_at: "2026-11-01T08:00:00Z" })] }),
    task({ id: "stopped-new", status: "failed", runs: [run("failure", { ended_at: "2026-11-01T10:00:00Z" })] }),
    task({ id: "stopped-old", status: "uncertain", updated_at: "2026-10-30T10:00:00Z" }),
  ];
  const groups = groupTasks(tasks.map(t => ({ machineId: "m", task: t })));
  expect(groups.get("需要你看一下")!.map(e => e.task.id)).toEqual(["stopped-new", "stopped-old"]);
  expect(groups.get("最近")!.map(e => e.task.id)).toEqual(["newer", "older", "no-result-early", "no-result-late"]);
  expect(stoppedTime(tasks[5])).toBe(tasks[5].updated_at);
  expect(notificationTime(run("r", { started_at: "", ended_at: "", scheduled_at: "scheduled" }))).toBe("scheduled");
});
it("formats schedule dates by local calendar days through DST, week and year boundaries", () => {
  const now = new Date(2026, 10, 1, 14).getTime();
  const at = (year: number, month: number, day: number, hour = 8) => new Date(year, month, day, hour).toISOString();
  expect([at(2026, 10, 1), at(2026, 9, 31), at(2026, 9, 30), at(2026, 9, 26), at(2026, 9, 25), at(2025, 11, 31)].map(t => scheduleTime(t, now)))
    .toEqual(["08:00", "昨天", "周五", "周一", "10月25日", "2025年12月31日"]);
  expect(scheduleTime(at(2025, 11, 31), new Date(2026, 0, 1, 9).getTime())).toBe("昨天");
  expect(scheduleTime(at(2025, 11, 30), new Date(2026, 0, 3, 9).getTime())).toBe("周二");
  expect(timingText({ type: "calendar", time: "08:00", timezone: "Asia/Shanghai" }, "America/Los_Angeles")).toBe("每天 08:00（北京时间）");
  expect(timingText({ type: "calendar", time: "08:00", timezone: "Europe/Oslo" }, "UTC")).toBe("每天 08:00（Oslo）");
});
function user(runId?: string, kind = OriginKind.SYSTEM): Event {
  return create(EventSchema, { origin: { kind, scheduledRunId: runId }, kind: { case: "userMessage", value: { text: "check" } } });
}
function answer(text: string, phase = "final_answer"): Event {
  return create(EventSchema, { kind: { case: "agentMessage", value: { text, phase } } });
}
it("extracts the last final reply including autonomous followups and preserves full Markdown", () => {
  const full = "# Report\n\n| Item | Result |\n| --- | --- |\n| Test | OK |\n\n- complete\n\n```sh\necho ok\n```";
  expect(finalRunText([answer("unrelated"), user("target"), answer("first"), answer("comment", "commentary"), answer(full)], "target")).toBe(full);
  expect(finalRunText([user("another"), answer("wrong")], "target")).toBe("");
  expect(finalRunText([user("target"), answer("comment", "commentary")], "target")).toBe("");
});
it.each([
  user(undefined, OriginKind.CLIENT), user(undefined, OriginKind.LOCAL), user("another"),
  create(EventSchema, { origin: { kind: OriginKind.LOCAL }, kind: { case: "sessionState", value: { detail: "resumed" } } }),
  create(EventSchema, { kind: { case: "sessionState", value: { status: SessionStatus.EXITED } } }),
])("does not take replies beyond an ownership boundary %#", boundary => {
  expect(finalRunText([user("target"), answer("correct"), boundary, answer("wrong")], "target")).toBe("correct");
});

it("falls back rather than crossing an unavailable history event", () => {
  expect(finalRunText([user("target"), answer("first"), create(EventSchema), answer("uncertain ownership")], "target")).toBe("");
});

it("only releases the detail tracker when marking that same task unread", () => {
  const seen = new ScheduleSeen([], store());
  const first = task({ id: "first", runs: [run("first", { notified: true })] });
  const second = task({ id: "second", runs: [run("second", { notified: true })] });
  seen.observe("m", [first, second]); seen.setDetail("m", first.id);
  seen.markUnread("m", second);
  first.runs!.push(run("during", { notified: true }));
  seen.observe("m", [first, second]);
  expect(seen.hasNew("m", first)).toBe(false);
  expect(seen.unread("m", second)).toEqual(new Set(["second"]));
  seen.markUnread("m", first);
  seen.observe("m", [first, second]);
  expect(seen.hasNew("m", first)).toBe(true);
});

it.each([
  { source: "agent" as const, text: " 独立的标题 ", summary: "正文", title: "独立的标题" },
  { source: "agent_and_daemon" as const, text: "标题", summary: "正文", title: "标题" },
  { source: "agent" as const, text: " 相同内容\n", summary: "相同内容 ", title: "" },
  { source: "agent_and_daemon" as const, text: "相同内容", summary: " 相同内容\n", title: "" },
  { source: "daemon" as const, text: "正文前 200 字", summary: "全文开头", title: "" },
  { source: "daemon" as const, text: "定时任务运行失败：运行失败", summary: "", title: "" },
  { source: "undeliverable" as const, text: "通知内容", summary: "摘要", title: "" },
  { source: undefined, text: "通知内容", summary: "摘要", title: "" },
  { source: "agent" as const, text: "  ", summary: "摘要", title: "" },
])("shows only distinct agent headings: $source / $text", ({ source, text, summary, title }) => {
  const value = run("result", { notified: true, notification_source: source, notification_text: text, final_summary: summary });
  expect(runTitle(value)).toBe(title);
  expect(runSummary(value)).toBe(text); // The list summary rule is unchanged.
});
