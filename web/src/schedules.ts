// JSON contract owned by internal/schedule. Only encrypted machine commands
// carry these definitions; the relay never receives task content in plaintext.
import type { Tone, Item } from "./model";
import { isSkeleton } from "./skeleton";
import {
  InputStatus,
  SessionStatus,
  TurnOutcome,
  type Event,
} from "./gen/remoteagent/v1/event_pb";

export interface ScheduleTrigger {
  type: "at" | "calendar" | "interval" | "after_turn";
  at?: string;
  time?: string;
  weekdays?: number[];
  timezone?: string;
  until?: string;
  interval_seconds?: number;
  delay_seconds?: number;
  repeat?: boolean;
  max_runs?: number;
}
export interface ScheduleTarget {
  type: "new_session" | "resume_session";
  session?: string;
  cwd?: string;
  agent?: string;
  model?: string;
  effort?: string;
  mode?: string;
}
export interface ScheduleSpec {
  title: string;
  message: string;
  trigger: ScheduleTrigger;
  target: ScheduleTarget;
  late_seconds?: number;
}
export interface ScheduleRun {
  id: string;
  session_id?: string;
  scheduled_at: string;
  started_at: string;
  ended_at?: string;
  start_seq: number;
  after_seq: number;
  turn_id?: string;
  status: string;
  reason?: string;
  notified: boolean;
  notification_text?: string;
  notification_count?: number;
  final_summary?: string;
  manual?: boolean;
}
export interface ScheduleTask {
  id: string;
  revision: number;
  spec: ScheduleSpec;
  status: string;
  reason?: string;
  created_at: string;
  updated_at: string;
  next_run_at?: string;
  source: string;
  source_session?: string;
  scoped_mode?: string;
  dispatches: number;
  runs?: ScheduleRun[];
  active_run?: ScheduleRun;
  manual_request?: { request_id: string };
}
export type TaskEntry = { machineId: string; task: ScheduleTask };
export const scheduleGroups = [
  "需要你看一下",
  "进行中",
  "已暂停",
  "已结束",
] as const;
export type ScheduleGroup = (typeof scheduleGroups)[number];
export function taskGroup(t: ScheduleTask): ScheduleGroup {
  if (t.status === "cancelled" && t.reason && t.reason !== "user_cancelled")
    return "需要你看一下";
  if (["completed", "cancelled"].includes(t.status)) return "已结束";
  if (
    ["failed", "uncertain"].includes(t.status) ||
    (t.status === "paused" && t.reason !== "user_paused")
  )
    return "需要你看一下";
  return t.status === "paused" ? "已暂停" : "进行中";
}
export function taskVisible(t: ScheduleTask): boolean {
  return (
    t.status !== "cancelled" || (!!t.reason && t.reason !== "user_cancelled")
  );
}
export function groupTasks(
  entries: TaskEntry[],
): Map<ScheduleGroup, TaskEntry[]> {
  return new Map(
    scheduleGroups.map((g) => [
      g,
      entries
        .filter((e) => taskVisible(e.task) && taskGroup(e.task) === g)
        .sort(
          (a, b) =>
            (g === "进行中"
              ? (a.task.next_run_at ?? "9999").localeCompare(
                  b.task.next_run_at ?? "9999",
                )
              : b.task.updated_at.localeCompare(a.task.updated_at)) ||
            a.machineId.localeCompare(b.machineId) ||
            a.task.id.localeCompare(b.task.id),
        ),
    ]),
  );
}
export function taskTone(t: ScheduleTask, online: boolean): Tone {
  if (!online) return "off";
  const g = taskGroup(t);
  return g === "需要你看一下"
    ? "wait"
    : g === "进行中"
      ? t.active_run
        ? "run"
        : "idle"
      : "off";
}
export function clockTime(at: string | number): string {
  return new Date(at).toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}
export function dayKey(at: string | number): string {
  const d = new Date(at);
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}
export function dayText(at: string | number, now = Date.now()): string {
  if (dayKey(at) === dayKey(now)) return "今天";
  const previous = new Date(now);
  previous.setDate(previous.getDate() - 1);
  if (dayKey(at) === dayKey(previous.getTime())) return "昨天";
  const d = new Date(at);
  return `${d.getFullYear() === new Date(now).getFullYear() ? "" : d.getFullYear() + "年"}${d.getMonth() + 1}月${d.getDate()}日`;
}
export function dateTime(at: string, now = Date.now()): string {
  return `${dayText(at, now)} ${clockTime(at)}`;
}
export function frequency(tr: ScheduleTrigger): string {
  switch (tr.type) {
    case "at":
      return tr.at ? dateTime(tr.at) + "，一次" : "一次";
    case "calendar": {
      const days = tr.weekdays ?? [];
      const names = ["", "一", "二", "三", "四", "五", "六", "日"];
      const daily = !days.length || days.length === 7;
      const workdays = days.join() === "1,2,3,4,5";
      return `${daily ? "每天" : workdays ? "工作日" : "每周" + days.map((d) => names[d]).join("、")} ${tr.time}`;
    }
    case "interval": {
      const s = tr.interval_seconds ?? 0;
      return s === 3600
        ? "每小时"
        : s % 86400 === 0
          ? `每 ${s / 86400} 天`
          : s % 3600 === 0
            ? `每 ${s / 3600} 小时`
            : s % 60 === 0
              ? `每 ${s / 60} 分钟`
              : `每 ${s} 秒`;
    }
    case "after_turn":
      return `${tr.repeat ? "每轮" : "当前轮"}结束 ${tr.delay_seconds ?? 0} 秒后`;
  }
}
/** The same explicit zone label on every client; local calendar zones stay implicit. */
export function timingText(
  tr: ScheduleTrigger,
  localZone = Intl.DateTimeFormat().resolvedOptions().timeZone,
): string {
  const canonical = (zone: string) => {
    try {
      return new Intl.DateTimeFormat("zh-CN", {
        timeZone: zone,
      }).resolvedOptions().timeZone;
    } catch {
      return zone;
    }
  };
  return (
    frequency(tr) +
    (tr.type === "calendar" &&
    tr.timezone &&
    canonical(tr.timezone) !== canonical(localZone)
      ? " · " + tr.timezone
      : "")
  );
}
export function clipTaskName(name: string): string {
  return [...name].slice(0, 60).join("");
}
/** Resolve the optional name at save time; editing a message later preserves it. */
export function taskName(name: string, message: string): string {
  const first = message.trim().split(/\r?\n|[。！？!?]|\.(?=\s|$)/u)[0];
  return clipTaskName(
    (name.trim() || first || message.trim()).replace(/\s+/g, " ").trim(),
  );
}
export function nextText(t: ScheduleTask, now = Date.now()): string {
  const g = taskGroup(t);
  if (g !== "进行中") return g === "需要你看一下" ? "已停下" : g;
  if (t.active_run)
    return (
      (
        {
          dispatching: "正在启动",
          queued: "等待当前轮结束",
          running: "正在运行",
        } as Record<string, string>
      )[t.active_run.status] ?? "正在运行"
    );
  if (!t.next_run_at) return "等待本轮结束";
  return `下次 ${dayKey(t.next_run_at) === dayKey(now) ? clockTime(t.next_run_at) : dateTime(t.next_run_at, now)}`;
}
const reasons: Record<string, string> = {
  relay_session_limit: "中继上的会话数已满",
  agent_schedule_permissions_changed: "创建任务时的权限已改变，任务已暂停",
  agent_schedule_interval_below_minimum: "Agent 创建的任务间隔过短，任务已暂停",
  execution_failed: "运行失败，任务已停下",
  execution_interrupted: "运行被中断，任务已停下",
  execution_session_exited: "运行中的对话已结束",
  execution_session_missing: "找不到运行的对话",
  message_cancelled: "定时消息在送达前被取消，任务已停下",
  message_delivery_failed: "消息没有送达 CLI",
  could_not_queue_message: "消息没能加入待发队列",
  execution_input_paused: "定时消息已暂停，请先在对话里恢复或取消这条消息",
  restart_dispatching: "电脑重启前的发送结果无法确认",
  catch_up_window_expired: "已超过补跑时间，本次跳过",
  user_paused: "已暂停",
  user_cancelled: "已删除",
};
export function reasonText(reason?: string): string {
  return reason
    ? (reasons[reason] ?? `电脑返回的原因：${reason}`)
    : "运行结果无法确认";
}
export function allRuns(t: ScheduleTask): ScheduleRun[] {
  const byId = new Map((t.runs ?? []).map((r) => [r.id, r]));
  if (t.active_run) byId.set(t.active_run.id, t.active_run);
  return [...byId.values()].sort(
    (a, b) =>
      b.started_at.localeCompare(a.started_at) || b.id.localeCompare(a.id),
  );
}
const stoppedRunLabels: Record<string, string> = {
  failed: "失败",
  uncertain: "无法确认",
  interrupted: "已中断",
  skipped: "跳过",
  cancelled: "已取消",
};
export function runResult(r: ScheduleRun): string {
  if (stoppedRunLabels[r.status]) return stoppedRunLabels[r.status];
  if (r.notified) return "通知了你";
  return (
    (
      {
        completed: "没有通知",
        dispatching: "正在启动",
        queued: "等待当前轮结束",
        running: "正在运行",
      } as Record<string, string>
    )[r.status] ?? "等待结果"
  );
}
export function runSummary(r: ScheduleRun): string {
  if (stoppedRunLabels[r.status]) return reasonText(r.reason);
  return (
    r.notification_text ||
    r.final_summary ||
    (r.reason ? reasonText(r.reason) : "")
  );
}
export function lastResult(t: ScheduleTask, now = Date.now()): string {
  const runs = allRuns(t);
  const latest = runs[0];
  if (!latest) return "还没有运行";
  const today = runs.filter((r) => dayKey(r.started_at) === dayKey(now));
  if (
    today.length > 1 &&
    today.every((r) => r.status === "completed" && !r.notified)
  )
    return `今天 ${today.length} 次都没有通知`;
  const detail =
    latest.notified || stoppedRunLabels[latest.status]
      ? runSummary(latest)
      : "";
  return `${dateTime(latest.started_at, now)} ${runResult(latest)}${detail ? "：" + detail : ""}`;
}
export type RunGroup = {
  key: string;
  day: string;
  runs: ScheduleRun[];
  quiet: boolean;
};
export function groupRuns(t: ScheduleTask): RunGroup[] {
  const groups: RunGroup[] = [];
  const days = new Map<string, RunGroup>();
  for (const r of allRuns(t)) {
    const day = dayKey(r.started_at);
    const quiet = r.status === "completed" && !r.notified;
    if (quiet) {
      const existing = days.get(day);
      if (existing) {
        existing.runs.push(r);
        continue;
      }
    }
    const g = { key: r.id, day, runs: [r], quiet };
    groups.push(g);
    if (quiet) days.set(day, g);
  }
  return groups;
}
export function oneShot(t: ScheduleTask): boolean {
  return (
    t.spec.trigger.type === "at" ||
    (t.spec.trigger.type === "after_turn" && !t.spec.trigger.repeat)
  );
}
export function taskRoute(machineId: string, id: string): string {
  return `tasks/${encodeURIComponent(machineId)}/${encodeURIComponent(id)}`;
}

// Fold only complete, consecutive scheduled rounds. A human message, approval,
// failed/unfinished turn, notification or unknown event is never hidden.
export type QuietSpan = {
  key: string;
  start: bigint;
  end: bigint;
  times: string[];
};
export function quietSpans(events: Event[]): QuietSpan[] {
  const spans: QuietSpan[] = [];
  const identity = (task: string, run: string) => task + "/" + run;
  // Scheduled context can survive a completed turn and notify during a later
  // autonomous turn. Classify the whole recorded run before hiding any of it.
  const notifiedRuns = new Set<string>();
  for (const e of events) {
    const k = e.kind;
    if (k.case === "scheduledNotification")
      notifiedRuns.add(identity(k.value.taskId, k.value.runId));
    if (k.case === "turnEnded" && k.value.scheduledNotified)
      notifiedRuns.add(
        identity(k.value.scheduledTaskId, k.value.scheduledRunId),
      );
  }
  let start: Event | undefined;
  let run = "";
  let task = "";
  let notified = false;
  let safe = true;
  let boundary = true;
  let lastDay = "";
  let tailMayContinue = false;
  for (const e of events) {
    const k = e.kind;
    const oversized = isSkeleton(e);
    if (k.case === "userMessage") {
      tailMayContinue = false;
      start =
        e.origin?.scheduledRunId && e.origin.scheduledTaskId ? e : undefined;
      run = e.origin?.scheduledRunId ?? "";
      task = e.origin?.scheduledTaskId ?? "";
      notified = false;
      safe = !oversized && !!e.time;
      if (!start) boundary = true;
    } else if (start && k.case === "scheduledNotification") notified = true;
    else if (
      start &&
      (oversized ||
        !k.case ||
        [
          "approvalRequested",
          "questionAsked",
          "planProposed",
          "sessionState",
          "sessionForked",
        ].includes(k.case) ||
        (k.case === "inputState" && (k.value.paused || [InputStatus.FAILED, InputStatus.UNCONFIRMED, InputStatus.CANCELED, InputStatus.NEEDS_TERMINAL, InputStatus.HELD_BY_CLI].includes(k.value.status))))
    )
      safe = false;
    // Visible follow-up content is a fence. Otherwise widening a span to the
    // next quiet round could swallow an intervening reply, failure or notice.
    if (
      !start &&
      (oversized ||
        !k.case ||
        [
          "scheduledNotification",
          "agentMessage",
          "reasoning",
          "toolCall",
          "toolStarted",
          "approvalRequested",
          "questionAsked",
          "planProposed",
          "settingsChanged",
          "imageGenerated",
          "sessionForked",
        ].includes(k.case) ||
        (k.case === "inputState" &&
          ![
            InputStatus.QUEUED,
            InputStatus.SUBMITTED,
            InputStatus.HELD_BY_CLI,
          ].includes(k.value.status)) ||
        (k.case === "sessionState" && k.value.status === SessionStatus.EXITED))
    ) {
      boundary = true;
      // A grouped tool row can straddle an invisible short turn end. Unfold
      // the tail as well, so that such a group cannot hide follow-up calls.
      if (tailMayContinue) {
        spans.pop();
        tailMayContinue = false;
      }
    }
    if (k.case !== "turnEnded") continue;
    if (
      start &&
      safe &&
      !notified &&
      !notifiedRuns.has(identity(task, run)) &&
      !k.value.scheduledNotified &&
      k.value.scheduledTaskId === task &&
      k.value.scheduledRunId === run &&
      k.value.outcome === TurnOutcome.COMPLETED
    ) {
      const at = Number(start.time!.seconds) * 1000;
      const time = clockTime(at);
      const day = dayKey(at);
      const last = spans.at(-1);
      if (!boundary && last && day === lastDay) {
        last.end = e.seq;
        last.times.push(time);
      } else
        spans.push({
          key: "quiet-" + start.seq,
          start: start.seq,
          end: e.seq,
          times: [time],
        });
      lastDay = day;
      boundary = false;
      tailMayContinue = true;
    } else {
      boundary = true;
      tailMayContinue = false;
    }
    start = undefined;
  }
  return spans;
}

export type ScheduledItem =
  Item | { kind: "quiet"; key: string; times: string[]; rows: Item[] };
export function foldScheduled(
  items: Item[],
  events: Event[],
  expanded: Set<string>,
  revealSeq?: bigint,
): ScheduledItem[] {
  const spans = quietSpans(events).filter(
    (s) =>
      s.times.length > 1 &&
      !expanded.has(s.key) &&
      !(revealSeq !== undefined && s.start <= revealSeq && revealSeq <= s.end),
  );
  const out: ScheduledItem[] = [];
  let spanIndex = 0;
  for (const item of items) {
    const seq =
      item.kind === "event"
        ? item.event.seq
        : BigInt(item.key.replace(/^[a-z]+/, "") || 0);
    while (spans[spanIndex] && seq > spans[spanIndex].end) spanIndex++;
    const span = spans[spanIndex];
    if (!span || seq < span.start) {
      out.push(item);
      continue;
    }
    let group = out.at(-1);
    if (group?.kind !== "quiet" || group.key !== span.key) {
      group = { kind: "quiet", key: span.key, times: span.times, rows: [] };
      out.push(group);
    }
    group.rows.push(item);
  }
  return out;
}

/** Convert a wall-clock input in an IANA zone, rejecting nonexistent DST times. */
export function zonedISO(local: string, timezone: string): string {
  const target = Date.parse(local + "Z");
  if (!Number.isFinite(target)) throw new Error("请选择有效的日期和时间");
  const format = new Intl.DateTimeFormat("sv-SE", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  });
  let value = target;
  for (let i = 0; i < 4; i++) {
    const shown = Date.parse(format.format(value).replace(" ", "T") + "Z");
    if (shown === target) return new Date(value).toISOString();
    value += target - shown;
  }
  throw new Error("这个时区在该时刻发生夏令时跳转，请换一个时间");
}
export function localInput(at: string, timezone: string): string {
  return new Intl.DateTimeFormat("sv-SE", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  })
    .format(new Date(at))
    .replace(" ", "T");
}
