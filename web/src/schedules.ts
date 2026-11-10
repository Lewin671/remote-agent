// JSON contract owned by internal/schedule. Only encrypted machine commands
// carry these definitions; the relay never receives task content in plaintext.
import { derive, isBackground, toolOk, type Item } from "./model";
import { isSkeleton } from "./skeleton";
import {
  InputStatus,
  OriginKind,
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
  notify?: "finish" | "agent_only";
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
  notification_seq?: number;
  notification_source?: "agent" | "daemon" | "agent_and_daemon" | "undeliverable";
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
export const scheduleGroups = ["需要你看一下", "最近", "已结束"] as const;
export type ScheduleGroup = (typeof scheduleGroups)[number];
type TaskState = "需要你看一下" | "进行中" | "已暂停" | "已结束";
export function taskGroup(t: ScheduleTask): TaskState {
  if (t.status === "cancelled" && t.reason && t.reason !== "user_cancelled")
    return "需要你看一下";
  if (["completed", "cancelled"].includes(t.status)) return "已结束";
  if (
    ["failed", "uncertain"].includes(t.status) ||
    (t.status === "paused" && !!t.reason && !["user_paused", "completed_while_paused"].includes(t.reason))
  )
    return "需要你看一下";
  return t.status === "paused" ? "已暂停" : "进行中";
}
export function taskVisible(t: ScheduleTask): boolean {
  return (
    t.status !== "cancelled" || (!!t.reason && t.reason !== "user_cancelled")
  );
}
export function stoppedTime(task: ScheduleTask): string {
  return notificationTime(allRuns(task)[0]) || task.updated_at;
}
export function groupTasks(entries: TaskEntry[], hasNew: (entry: TaskEntry) => boolean = () => false): Map<ScheduleGroup, TaskEntry[]> {
  const group = (e: TaskEntry): ScheduleGroup => taskGroup(e.task) === "需要你看一下" ? "需要你看一下"
    : taskGroup(e.task) === "已结束" && !hasNew(e) ? "已结束" : "最近";
  return new Map(scheduleGroups.map(g => [g, entries.filter(e => taskVisible(e.task) && group(e) === g).sort((a, b) => {
    const time = (e: TaskEntry) => Date.parse(g === "需要你看一下" ? stoppedTime(e.task) : notificationTime(latestNotification(e.task))) || 0;
    return time(b) - time(a) || (g === "最近"
      ? (a.task.next_run_at ?? "9999").localeCompare(b.task.next_run_at ?? "9999")
      : b.task.updated_at.localeCompare(a.task.updated_at)) || a.machineId.localeCompare(b.machineId) || a.task.id.localeCompare(b.task.id);
  })]));
}
/** Calendar-day comparisons avoid DST's 23/25-hour days. Schedules only. */
export function scheduleTime(at: string, now = Date.now()): string {
  const d = new Date(at), today = new Date(now);
  const days = (Date.UTC(today.getFullYear(), today.getMonth(), today.getDate()) - Date.UTC(d.getFullYear(), d.getMonth(), d.getDate())) / 86400_000;
  if (!days) return clockTime(at);
  if (days === 1) return "昨天";
  if (days > 1 && days < 7) return "周" + "日一二三四五六"[d.getDay()];
  if (d.getFullYear() !== today.getFullYear()) return `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日`;
  return `${d.getMonth() + 1}月${d.getDate()}日`;
}
/** Same ownership fences as the daemon's scheduled follow-up watcher. */
export function finalRunText(events: Event[], runId: string): string {
  let found = false, text = "";
  for (const e of events) {
    const k = e.kind, origin = e.origin;
    if (!found) {
      if (k.case === "userMessage" && origin?.scheduledRunId === runId) found = true;
      continue;
    }
    if (!k.case || isSkeleton(e)) return "";
    const human = origin?.kind === OriginKind.CLIENT || origin?.kind === OriginKind.LOCAL;
    if ((human && (k.case === "userMessage" || (k.case === "sessionState" && k.value.detail === "resumed"))) ||
      (k.case === "userMessage" && origin?.scheduledRunId && origin.scheduledRunId !== runId) ||
      (k.case === "sessionState" && k.value.status === SessionStatus.EXITED)) break;
    if (k.case === "agentMessage" && k.value.phase === "final_answer" && !isSkeleton(e)) text = k.value.text;
  }
  return text;
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
  const tomorrow = new Date(now);
  tomorrow.setDate(tomorrow.getDate() + 1);
  if (dayKey(at) === dayKey(tomorrow.getTime())) return "明天";
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
function zoneName(zone: string): string {
  return ({ "Asia/Shanghai": "北京时间", "Asia/Hong_Kong": "香港时间", "Asia/Tokyo": "东京时间", "America/Los_Angeles": "洛杉矶时间", "America/New_York": "纽约时间", "Europe/London": "伦敦时间", "Asia/Singapore": "新加坡时间" } as Record<string, string>)[zone] ?? zone.split("/").at(-1)!.replaceAll("_", " ");
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
      ? "（" + zoneName(canonical(tr.timezone)) + "）"
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
    (name.trim() || first?.trim().replace(/[，,:：;；]+$/u, "") || message.trim()).replace(/\s+/g, " ").trim(),
  );
}
export function nextText(t: ScheduleTask, now = Date.now()): string {
  if (t.active_run && ["dispatching", "queued", "running"].includes(t.active_run.status)) return "运行中";
  const g = taskGroup(t);
  if (g !== "进行中") return g === "需要你看一下" ? "已停下" : g;
  return t.next_run_at ? `下次 ${dateTime(t.next_run_at, now)}` : "下次待定";
}
export const scheduleOfflineRow = "未连接";
export const scheduleMessageLimit = 32 * 1024;
export function validScheduleMessage(message: string): boolean {
  return !!message.trim() && new TextEncoder().encode(message).length <= scheduleMessageLimit;
}
export const scheduleOffline = "未连接，显示的是上次的状态";
export const scheduleEmpty = "在对话里跟 agent 说「每天 8 点帮我看看…」，它会替你建好；也可以自己新建一个。";
export const scheduleIntervalHint = "第一次会在一个完整间隔后运行，想现在执行可以创建后点“立即运行一次”。";
export function scheduleLimits(tr: ScheduleTrigger, now = Date.now()): string {
  return [tr.max_runs ? `共 ${tr.max_runs} 次` : "", tr.until ? `到 ${dateTime(tr.until, now)}` : ""].filter(Boolean).join("，或");
}

const reasons: Record<string, string> = {
  relay_session_limit: "中继上的会话数已满",
  agent_schedule_permissions_changed: "创建任务时的权限已改变，任务已停下",
  agent_schedule_interval_below_minimum: "Agent 创建的任务间隔过短，任务已停下",
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
  completed_while_paused: "已暂停：期间计划已到期",
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
// The run record has no notification timestamp. Use completion time when
// available, otherwise the start, then the scheduled time; the same on every client.
export function notificationTime(run?: ScheduleRun): string {
  return run ? run.ended_at || run.started_at || run.scheduled_at : "";
}
function notificationMillis(run?: ScheduleRun): number { return Date.parse(notificationTime(run)) || 0; }
export function latestNotification(task: ScheduleTask): ScheduleRun | undefined {
  return allRuns(task).filter(r => r.notified).sort((a, b) =>
    notificationMillis(b) - notificationMillis(a) || b.id.localeCompare(a.id),
  )[0];
}
export function unreadLabel(count: number): string { return count > 99 ? "99+" : String(count); }
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
/** Only an agent-authored notification can add a distinct result heading. */
export function runTitle(run: ScheduleRun): string {
  const text = run.notification_text?.trim() ?? "";
  return (run.notification_source === "agent" || run.notification_source === "agent_and_daemon") &&
    text !== (run.final_summary?.trim() ?? "") ? text : "";
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
  if (taskGroup(t) === "需要你看一下") return reasonText(t.reason);
  const runs = allRuns(t);
  const latest = runs[0];
  if (!latest) return "还没有运行";
  if (["dispatching", "queued", "running"].includes(latest.status)) return "还没有结果";
  const today = runs.filter((r) => dayKey(r.started_at) === dayKey(now));
  if (
    today.length > 1 &&
    today.every((r) => r.status === "completed" && !r.notified)
  )
    return `今天 ${today.length} 次，都没有通知`;
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
    const day = dayKey(notificationTime(r));
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
  const derived = derive(events);
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
  let toolsThrough = 0n;
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
      toolsThrough = 0n;
      if (!start) boundary = true;
    } else if (start && k.case === "scheduledNotification") notified = true;
    else if (
      start &&
      (oversized ||
        (k.case === "toolCall" && (!toolOk(k.value) || isBackground(k.value))) ||
        (k.case === "toolStarted" && typeof derived.toolEnds.get(e.seq) !== "bigint") ||
        (k.case === "turnContext" && derived.terminalChanges.has(e.seq)) ||
        !k.case ||
        [
          "approvalRequested",
          "questionAsked",
          "questionAnswered",
          "settingsChanged",
          "imageGenerated",
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
          "questionAnswered",
          "turnContext",
          "turnEnded",
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
    if (start && k.case === "toolStarted") {
      const end = derived.toolEnds.get(e.seq);
      if (typeof end === "bigint" && end > toolsThrough) toolsThrough = end;
    }
    if (k.case !== "turnEnded") continue;
    if (
      start &&
      safe &&
      toolsThrough <= e.seq &&
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
  const wall = (instant: number) => Date.parse(format.format(instant).replace(" ", "T") + "Z");
  // Sample both sides of a transition; iteration from UTC alone can choose the
  // second occurrence in zones east of UTC. Only exact round trips are valid.
  const candidates = [-86400_000, 0, 86400_000].map(delta => {
    const sample = target + delta;
    return target - (wall(sample) - sample);
  }).filter(instant => wall(instant) === target);
  if (candidates.length) return new Date(Math.min(...candidates)).toISOString();
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

export type ScheduleDetailVisit = { unread?: ReadonlySet<string> };

/** Only opaque run identities are persisted; task content stays in the encrypted cache. */
export class ScheduleSeen {
  private seen = new Map<string, Set<string>>();
  private loaded = new Map<string, ScheduleTask[]>();
  private live = new Set<string>();
  private protectedUnread = new Map<string, Set<string>>();
  private page?: string;
  private handoff = false;
  private detail?: { machine: string; task: string; visit: ScheduleDetailVisit };
  private dismissed = new Map<string, Set<string>>();
  private session?: string;
  private visible = true;
  constructor(private scope: string[], private storage: Pick<Storage, "getItem" | "setItem">, private onRead: (run: ScheduleRun) => void = () => {}) {}
  private key(machine: string) { return "ra-schedule-seen:" + JSON.stringify([...this.scope, machine]); }
  private save(machine: string, ids: Set<string>) {
    this.seen.set(machine, ids);
    try { this.storage.setItem(this.key(machine), JSON.stringify([...ids])); } catch { /* Keep working in memory when storage is unavailable. */ }
  }
  // Only a complete, verified list may establish a baseline or prune history.
  // Loading placeholders, partial pages and failed responses must not enter it.
  observe(machine: string, tasks: ScheduleTask[], options: { complete?: boolean; prune?: boolean; live?: boolean } = {}) {
    if (options.complete === false) return;
    const ids = new Set(tasks.filter(taskVisible).flatMap(t => allRuns(t).filter(r => r.notified).map(r => JSON.stringify([t.id, r.id]))));
    this.loaded.set(machine, tasks);
    if (options.live !== false) this.live.add(machine);
    if (!this.seen.has(machine)) {
      try {
        const stored: unknown = JSON.parse(this.storage.getItem(this.key(machine)) ?? "null");
        if (Array.isArray(stored) && stored.every(x => typeof x === "string")) this.seen.set(machine, new Set(stored));
      } catch { /* A missing or unreadable baseline starts with known notifications seen. */ }
      if (!this.seen.has(machine)) this.save(machine, ids);
    }
    if (options.prune !== false) {
      this.protectedUnread.set(machine, new Set([...(this.protectedUnread.get(machine) ?? [])].filter(id => ids.has(id))));
      const previous = this.seen.get(machine)!;
      const retained = new Set([...previous].filter(id => ids.has(id)));
      if (retained.size !== previous.size) this.save(machine, retained);
      const notifications = new Set(tasks.filter(taskVisible).flatMap(t => allRuns(t).filter(r => r.notified).map(r => this.dismissKey(t, r))));
      this.dismissed.set(machine, new Set([...(this.dismissed.get(machine) ?? [])].filter(id => notifications.has(id))));
    }
    this.markVisible(machine, tasks);
  }
  unread(machine: string, task: ScheduleTask): Set<string> {
    const seen = this.seen.get(machine);
    return new Set(allRuns(task).filter(r => r.notified && seen && !seen.has(JSON.stringify([task.id, r.id]))).map(r => r.id));
  }
  private viewing(): string | undefined {
    return this.detail ? taskRoute(this.detail.machine, this.detail.task) : this.session ? "s/" + encodeURIComponent(this.session) : undefined;
  }
  private moveViewing(before?: string, next?: string) {
    if (before === next) return;
    // Leaving the detail after the action is a handoff to the destination,
    // not another visit. Empty setters during React cleanup are intermediate.
    if (this.handoff) {
      if (next !== undefined) this.handoff = false;
    } else this.protectedUnread.clear();
  }
  setPage(page: string) {
    this.moveViewing(this.page ?? this.viewing(), page);
    this.page = page;
  }
  setDetail(machine?: string, task?: string, visit: ScheduleDetailVisit = {}): ScheduleDetailVisit | undefined {
    const before = this.viewing();
    this.detail = machine && task ? { machine, task, visit } : undefined;
    if (this.page === undefined) this.moveViewing(before, this.viewing());
    for (const [id, tasks] of this.loaded) this.markVisible(id, tasks);
    return this.detail?.visit;
  }
  // A hidden page is not being looked at: keep its notifications unread until it shows again.
  setVisible(visible: boolean) {
    const shown = visible && !this.visible;
    if (!visible) { this.protectedUnread.clear(); this.handoff = false; }
    this.visible = visible;
    const detail = this.detail;
    if (shown && detail?.visit.unread) {
      // Notifications that arrived while hidden were not seen during the visit, so they get dots too.
      const task = this.loaded.get(detail.machine)?.find(t => t.id === detail.task);
      if (task) detail.visit.unread = new Set([...detail.visit.unread, ...this.unread(detail.machine, task)]);
    }
    for (const [machine, tasks] of this.loaded) this.markVisible(machine, tasks);
  }
  setSession(session?: string) {
    const before = this.viewing();
    this.session = session;
    if (this.page === undefined) this.moveViewing(before, this.viewing());
    for (const [machine, tasks] of this.loaded) this.markVisible(machine, tasks);
  }
  private markVisible(machine: string, tasks: ScheduleTask[]) {
    if (!this.live.has(machine)) return;
    const detail = this.detail;
    if (detail?.machine === machine && detail.visit.unread === undefined) {
      const task = tasks.find(t => t.id === detail.task);
      // Capture exactly once, when this visit first has its task snapshot.
      if (task) detail.visit.unread = this.unread(machine, task);
    }
    if (!this.visible) return;
    const seen = new Set(this.seen.get(machine));
    const before = seen.size;
    const dismissed = this.dismissed.get(machine) ?? new Set<string>();
    this.dismissed.set(machine, dismissed);
    for (const t of tasks) for (const r of allRuns(t)) {
      if (r.notified && !this.protectedUnread.get(machine)?.has(JSON.stringify([t.id, r.id])) && ((this.detail?.machine === machine && this.detail.task === t.id) ||
        (this.session && r.session_id === this.session))) {
        seen.add(JSON.stringify([t.id, r.id]));
        const key = this.dismissKey(t, r);
        if (!dismissed.has(key)) {
          dismissed.add(key);
          try { this.onRead(r); } catch { /* Notification cleanup cannot affect reading. */ }
        }
      }
    }
    if (seen.size !== before) this.save(machine, seen);
  }
  private dismissKey(task: ScheduleTask, run: ScheduleRun) {
    return JSON.stringify([task.id, run.id, run.session_id, run.notification_seq]);
  }
  forget(machine: string) { this.loaded.delete(machine); this.dismissed.delete(machine); this.live.delete(machine); this.protectedUnread.delete(machine); }
  hasNew(machine: string, task: ScheduleTask): boolean { return this.unread(machine, task).size > 0; }
  markRead(machine: string, task: ScheduleTask) {
    const ids = new Set(this.seen.get(machine));
    const dismissed = this.dismissed.get(machine) ?? new Set<string>();
    this.dismissed.set(machine, dismissed);
    for (const r of allRuns(task).filter(r => r.notified)) {
      ids.add(JSON.stringify([task.id, r.id]));
      this.protectedUnread.get(machine)?.delete(JSON.stringify([task.id, r.id]));
      const key = this.dismissKey(task, r);
      if (!dismissed.has(key)) {
        dismissed.add(key);
        try { this.onRead(r); } catch { /* Notification cleanup cannot affect reading. */ }
      }
    }
    this.save(machine, ids);
  }
  markUnread(machine: string, task: ScheduleTask, leaving = false) {
    const latest = latestNotification(task);
    if (!latest) return;
    if (this.detail?.machine === machine && this.detail.task === task.id) {
      this.detail = undefined;
      this.handoff = true;
    }
    if (leaving) this.handoff = true;
    const identity = JSON.stringify([task.id, latest.id]);
    const protectedIds = this.protectedUnread.get(machine) ?? new Set<string>();
    protectedIds.add(identity);
    this.protectedUnread.set(machine, protectedIds);
    const ids = new Set(this.seen.get(machine));
    ids.delete(identity);
    this.save(machine, ids);
  }
  count(entries: TaskEntry[]) {
    return entries.filter(({ task }) => taskVisible(task) && taskGroup(task) === "需要你看一下").length;
  }
}
