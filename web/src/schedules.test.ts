import { t as message } from "./i18n";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { expect, it } from "vitest";
import shared from "../../testdata/schedules/clients.json";
import type { ScheduleTask } from "./schedules";
import {
  EventSchema,
  TurnOutcome,
  ToolBackground,
  InputStatus,
  SessionStatus,
  type Event,
} from "./gen/remoteagent/v1/event_pb";
import { derive, timeline } from "./model";
import {
  clockTime,
  dateTime,
  foldScheduled,
  frequency,
  groupRuns,
  groupTasks,
  lastResult,
  nextText,
  quietSpans,
  reasonText,
  taskGroup,
  zonedISO,
  timingText,
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
function rounds(
  options: {
    notified?: boolean;
    failed?: boolean;
    human?: boolean;
    active?: boolean;
    approval?: boolean;
  } = {},
): Event[] {
  const events: Event[] = [];
  const add = (
    kind: MessageInitShape<typeof EventSchema>["kind"],
    origin?: { scheduledTaskId: string; scheduledRunId: string },
  ) =>
    events.push(
      create(EventSchema, {
        seq: BigInt(events.length + 1),
        sessionId: "s",
        time: timestampFromMs(
          Date.parse("2026-10-24T10:00:00Z") + events.length * 60_000,
        ),
        origin,
        kind,
      }),
    );
  for (let i = 0; i < 2; i++) {
    if (i && options.human)
      add({ case: "userMessage", value: { text: "人工消息" } });
    add(
      { case: "userMessage", value: { text: "定时检查" } },
      { scheduledTaskId: "task", scheduledRunId: "run" + i },
    );
    if (i && options.notified)
      add({
        case: "scheduledNotification",
        value: { taskId: "task", runId: "run1", text: "有结果" },
      });
    if (i && options.approval)
      add({ case: "approvalRequested", value: { approvalId: "a" } });
    add({ case: "agentMessage", value: { text: "没有新消息" } });
    if (i && options.active) continue;
    add({
      case: "turnEnded",
      value: {
        durationMs: 1000n,
        outcome:
          i && options.failed ? TurnOutcome.ABORTED : TurnOutcome.COMPLETED,
        scheduledTaskId: "task",
        scheduledRunId: "run" + i,
        scheduledNotified: !!(i && options.notified),
      },
    });
  }
  return events;
}
it("folds consecutive completed quiet rounds and reveals a selected run", () => {
  const events = rounds();
  const raw = timeline(events, derive(events));
  const folded = foldScheduled(raw, events, new Set());
  expect(folded).toHaveLength(1);
  expect(folded[0].kind).toBe("quiet");
  expect(quietSpans(events)[0].times).toHaveLength(2);
  expect(foldScheduled(raw, events, new Set(["quiet-1"]))).toEqual(raw);
  expect(foldScheduled(raw, events, new Set(), 4n)).toEqual(raw);
  for (const options of [
    { notified: true },
    { failed: true },
    { human: true },
    { active: true },
    { approval: true },
  ]) {
    const es = rounds(options);
    expect(
      foldScheduled(timeline(es, derive(es)), es, new Set()).some(
        (it) => it.kind === "quiet",
      ),
    ).toBe(false);
  }
  const notified = rounds({ notified: true });
  expect(
    timeline(notified, derive(notified)).some(
      (it) =>
        it.kind === "event" && it.event.kind.case === "scheduledNotification",
    ),
  ).toBe(true);
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

it("keeps late notifications and autonomous replies outside quiet-round folds", () => {
  const original = rounds();
  const notification = create(EventSchema, {
    time: timestampFromMs(Date.parse("2026-10-24T10:03:00Z")),
    kind: {
      case: "scheduledNotification",
      value: { taskId: "task", runId: "run0", text: "迟到的通知不能隐藏" },
    },
  });
  for (const inserted of [
    notification,
    create(EventSchema, {
      kind: { case: "agentMessage", value: { text: "后续自主回复不能隐藏" } },
    }),
  ]) {
    const events = [
      ...original.slice(0, 3),
      inserted,
      ...original.slice(3),
    ].map((e, i) => ({ ...e, seq: BigInt(i + 1) }));
    const raw = timeline(events, derive(events));
    const folded = foldScheduled(raw, events, new Set());
    expect(folded.some((item) => item.kind === "quiet")).toBe(false);
    expect(folded).toEqual(raw);
  }
  const late = [...original, notification].map((e, i) => ({
    ...e,
    seq: BigInt(i + 1),
  }));
  expect(
    foldScheduled(timeline(late, derive(late)), late, new Set()).some(
      (item) => item.kind === "quiet",
    ),
  ).toBe(false);
  const grouped = rounds().map((e) => {
    if (e.kind.case === "agentMessage")
      return create(EventSchema, {
        seq: e.seq,
        time: e.time,
        origin: e.origin,
        sessionId: e.sessionId,
        kind: {
          case: "toolCall",
          value: {
            callId: "before-" + e.seq,
            status: "completed",
            title: "check",
          },
        },
      });
    if (e.kind.case === "turnEnded") e.kind.value.durationMs = 0n;
    return e;
  });
  grouped.push(
    create(EventSchema, {
      seq: 7n,
      kind: {
        case: "toolCall",
        value: {
          callId: "follow-up",
          status: "completed",
          title: "follow-up must stay visible",
        },
      },
    }),
  );
  const groupedRows = timeline(grouped, derive(grouped));
  expect(groupedRows.some((item) => item.kind === "tools")).toBe(true);
  expect(foldScheduled(groupedRows, grouped, new Set())).toEqual(groupedRows);
});
it("does not fold across oversized-content markers or local date boundaries", async () => {
  const { skeletonNote } = await import("./skeleton");
  const oversized = rounds();
  if (oversized[1].kind.case === "agentMessage")
    oversized[1].kind.value.text += skeletonNote;
  expect(
    foldScheduled(
      timeline(oversized, derive(oversized)),
      oversized,
      new Set(),
    ).some((item) => item.kind === "quiet"),
  ).toBe(false);
  const dates = rounds();
  for (const e of dates.slice(3))
    e.time = timestampFromMs(Date.parse("2026-10-25T10:00:00Z"));
  expect(
    foldScheduled(timeline(dates, derive(dates)), dates, new Set()).some(
      (item) => item.kind === "quiet",
    ),
  ).toBe(false);
});

it("still folds quiet rounds across ordinary scheduled queue and idle metadata", () => {
  const events = rounds()
    .flatMap((e) => {
      if (e.kind.case === "userMessage")
        return [
          create(EventSchema, {
            origin: e.origin,
            kind: {
              case: "inputState",
              value: { requestId: "q" + e.seq, status: InputStatus.QUEUED },
            },
          }),
          e,
          create(EventSchema, {
            origin: e.origin,
            kind: {
              case: "inputState",
              value: { requestId: "q" + e.seq, status: InputStatus.SUBMITTED },
            },
          }),
        ];
      if (e.kind.case === "turnEnded")
        return [
          e,
          create(EventSchema, {
            kind: {
              case: "sessionState",
              value: { status: SessionStatus.IDLE },
            },
          }),
        ];
      return [e];
    })
    .map((e, i) => ({ ...e, seq: BigInt(i + 1) }));
  const folded = foldScheduled(
    timeline(events, derive(events)),
    events,
    new Set(),
  );
  expect(folded).toHaveLength(1);
  expect(folded[0].kind).toBe("quiet");
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
  expect(Object.fromEntries([...groups].map(([g, tasks]) => [g, tasks.map(e => e.task.id)]))).toEqual({ "attention": shared.groups["需要你看一下"], "recent": ["conversation", "quiet", "paused"], "ended": shared.groups["已结束"] });
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
it.each([InputStatus.FAILED, InputStatus.UNCONFIRMED, InputStatus.CANCELED, InputStatus.NEEDS_TERMINAL, InputStatus.HELD_BY_CLI])("keeps blocking scheduled input visible after its message: %s", status => {
  const events = rounds().flatMap(e => e.kind.case === "userMessage" ? [e, create(EventSchema, {kind:{case:"inputState",value:{status,requestId:"q"}}})] : [e]).map((e,i)=>({...e,seq:BigInt(i+1)}));
  expect(foldScheduled(timeline(events, derive(events)),events,new Set()).some(x=>x.kind==="quiet")).toBe(false);
});

it.each([
  {case:"settingsChanged",value:{model:"new-model"}},
  {case:"settingsChanged",value:{mode:2}},
  {case:"questionAnswered",value:{questionId:"q"}},
  {case:"turnContext",value:{model:"new-model"}},
  {case:"turnEnded",value:{durationMs:1000n}},
] as MessageInitShape<typeof EventSchema>["kind"][])("preserves visible inter-round events: $case", kind => {
  const events=rounds();
  events.splice(3,0,create(EventSchema,{seq:4n,kind}));
  events.forEach((event,i)=>event.seq=BigInt(i+1));
  const raw=timeline(events,derive(events));
  expect(foldScheduled(raw,events,new Set())).toEqual(raw);
  expect(quietSpans(events).every(span=>!(span.start<4n && span.end>4n))).toBe(true);
});
it.each([
  {case:"toolCall",value:{title:"failed command",status:"failed"}},
  {case:"toolCall",value:{title:"nonzero command",status:"completed",exitCode:7}},
  {case:"toolCall",value:{title:"background command",status:"completed",background:ToolBackground.STILL_RUNNING}},
  {case:"toolStarted",value:{title:"unfinished command",callId:"pending"}},
  {case:"settingsChanged",value:{model:"other-model"}},
  {case:"questionAnswered",value:{questionId:"q"}},
  {case:"imageGenerated",value:{revisedPrompt:"image"}},
] as MessageInitShape<typeof EventSchema>["kind"][])("keeps protected in-round content visible: $case",kind=>{
  const events=rounds();events[1]=create(EventSchema,{seq:events[1].seq,time:events[1].time,origin:events[1].origin,kind});
  const raw=timeline(events,derive(events));
  expect(foldScheduled(raw,events,new Set())).toEqual(raw);
  expect(quietSpans(events).some(s=>s.start===1n)).toBe(false);
});
it("still folds completed successful tool results",()=>{
  const events=rounds();events[1]=create(EventSchema,{seq:events[1].seq,time:events[1].time,origin:events[1].origin,kind:{case:"toolCall",value:{title:"ok",status:"completed",exitCode:0}}});
  expect(quietSpans(events)[0].times).toHaveLength(2);
});

it("preserves terminal setting changes inside a scheduled round",()=>{
  const events=rounds();events[1]=create(EventSchema,{seq:2n,kind:{case:"turnContext",value:{model:"new-model"}}});
  events.unshift(create(EventSchema,{seq:0n,kind:{case:"turnContext",value:{model:"old-model"}}}));
  expect(derive(events).terminalChanges.has(2n)).toBe(true);
  const rows=timeline(events,derive(events));expect(foldScheduled(rows,events,new Set())).toEqual(rows);
});
it("keeps a failed result painted back onto a tool started in an earlier round",()=>{
  const events=rounds();
  events[4]=create(EventSchema,{seq:5n,kind:{case:"toolStarted",value:{title:"late command",callId:"late"}}});
  events.push(
    create(EventSchema,{seq:7n,time:events[0].time,origin:{scheduledTaskId:"task",scheduledRunId:"late-run"},kind:{case:"userMessage",value:{text:"next check"}}}),
    create(EventSchema,{seq:8n,kind:{case:"toolCall",value:{title:"late command",callId:"late",status:"failed",exitCode:7}}}),
    create(EventSchema,{seq:9n,kind:{case:"turnEnded",value:{outcome:TurnOutcome.COMPLETED,scheduledTaskId:"task",scheduledRunId:"late-run"}}}),
  );
  expect(derive(events).toolEnds.get(5n)).toBe(8n);
  const rows=timeline(events,derive(events));expect(foldScheduled(rows,events,new Set())).toEqual(rows);
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
