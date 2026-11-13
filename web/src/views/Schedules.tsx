import { rich } from "../i18n/rich";
import * as l10n from "../i18n";
import { ScheduleRevisionError } from "../remote";
import { useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
import { ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import { agentById, agentInfo } from "../agents";
import { Clock, ChevronRight, ChevronDown, Ellipsis, Info, LoaderCircle, X } from "../icons";
import {
  agentName,
  agentOf,
  defaultModeOf,
  machineName,
  modeOf,
  projectName,
  projects,
  titleOf,
  type NewConfig,
} from "../model";
import { errorText, type Remote, type SessionView } from "../remote";
import { scheduleDrafts } from "../schedule-drafts";
import {
  Switch,
  Alert,
  fromHere,
  Banner,
  ConnBar,
  Exp,
  NavBar,
  Segmented,
  Sheet,
  useNow,
  useRemote,
  useEscape,
  useDialogFocus,
  useOverlayExit,
} from "../ui";
import {
  allRuns,
  scheduleOffline, scheduleOfflineRow, scheduleEmpty, scheduleIntervalHint, scheduleLimits, validScheduleMessage, scheduleMessageLimit,
  clockTime,
  dateTime,
  dayText,
  frequency,
  timingText,
  taskName,
  clipTaskName,
  runSummary,
  runTitle,
  groupRuns,
  groupTasks,
  lastResult,
  latestNotification,
  notificationTime,
  localInput,
  nextText,
  oneShot,
  reasonText,
  runResult,
  taskGroup, weekdayLabel,
  taskRoute,
  scheduleTime, stoppedTime, finalRunText,
  zonedISO,
  type ScheduleDetailVisit,
  type ScheduleRun,
  type ScheduleSpec,
  type ScheduleTask,
  type ScheduleTrigger,
  type ScheduleTarget,
} from "../schedules";
import {
  ConfigSheet,
  NewFlow,
  NewProjectSheet,
  useNewSession,
  useMachineInfo,
  type Go,
} from "./NewSession";
import { ScheduleText, ScheduleTextarea, ScheduleMessageEditor } from "./ScheduleText";
import { Markdown } from "../markdown";

function TaskOffline({ remote, id }: { remote: Remote; id?: string }) {
  const machines = [...remote.machines.values()].filter(
    (m) => (!id || m.id === id) && remote.supportsSchedules(m.id),
  );
  const offline = machines.filter((m) => !remote.onlineOf(m));
  return (
    <>
      {remote.conn === "online" && offline.length > 0 && (
        <Banner>
          {offline.map((m) => machineName(m)).join("、")} {scheduleOffline()}
        </Banner>
      )}
    </>
  );
}
/** One task in a list (design-system §6 定时列表). layout "card": a task
 * that stopped is a gray card with an orange dot, its reason in orange and
 * 去处理 ›; one with a new result a card with a blue dot, up to four lines
 * of it and 看全文 ›. Otherwise one row: the title, the time, two lines of
 * the latest result and the third line; in the wide sidebar the row keeps
 * the dot at its head. */
export function ScheduleRow({
  remote,
  machineId,
  task,
  go,
  now = Date.now(),
  selected = false,
  layout = "row",
  gut = false,
}: {
  remote: Remote;
  machineId: string;
  task: ScheduleTask;
  go: Go;
  now?: number;
  selected?: boolean;
  layout?: "card" | "row";
  /** A row with the state's dot at its head, as the wide sidebar draws them. */
  gut?: boolean;
}) {
  const m = remote.machines.get(machineId);
  const entries = remote.taskEntries();
  const target = task.spec.target;
  const targetSession = remote.sessions.get(target.session ?? "");
  const location = (t: ScheduleTask) => t.spec.target.type === "new_session" ? t.spec.target.cwd ?? "" : remote.sessions.get(t.spec.target.session ?? "")?.info?.cwd ?? "";
  const agent = (t: ScheduleTask) => t.spec.target.type === "resume_session"
    ? remote.sessions.get(t.spec.target.session ?? "")?.info ? agentName(agentOf(remote.sessions.get(t.spec.target.session!))) : ""
    : t.spec.target.agent ? agentById(t.spec.target.agent).name : agentInfo().name;
  const multiple = (value: (t: ScheduleTask) => string) => new Set(entries.map(e => value(e.task)).filter(Boolean)).size > 1;
  const attention = taskGroup(task) === "attention";
  const fresh = remote.scheduleHasNew(machineId, task);
  const notification = latestNotification(task);
  const at = attention ? stoppedTime(task) : notificationTime(notification);
  // Nothing is said of the connection before the lists are in; the last state heard stands.
  const offline = !remote.awaiting && (remote.conn !== "online" || !(m && remote.onlineOf(m)));
  const running = task.active_run && ["dispatching", "queued", "running"].includes(task.active_run.status);
  const [menu, setMenu] = useState(false);
  const mark = () => { if (fresh) remote.markScheduleRead(machineId, task); else { remote.markScheduleUnread(machineId, task, selected); if (selected) go("tasks"); } };
  const card = layout === "card" && (attention || fresh);
  const dot = (attention || fresh) && <i className={"mark schedule-unread-dot " + (attention ? "wait attention-dot" : "unread")} role="img" aria-label={attention ? l10n.t("schedule.needs_your_attention") : l10n.t("home.ios_new_results")} />;
  const row1 = <span className="row1">{card && dot}<b>{task.spec.title}</b><span>{at ? scheduleTime(at, now) : ""}</span></span>;
  const pv = <span className="pv">{attention ? <><strong>{task.status === "failed" && oneShot(task) ? l10n.t("schedule.could_not_run") : l10n.t("schedule.stopped")}</strong> · {reasonText(task.reason, true)}</> : notification ? runSummary(notification) || runResult(notification) : running ? <span className="none">{l10n.t("schedule.no_results_yet")}</span> : allRuns(task).length ? lastResult(task, now) : task.next_run_at ? l10n.t("schedule.no_runs_next", { time: dateTime(task.next_run_at, now) }) : l10n.t("schedule.has_not_run_yet")}</span>;
  const mc = <span className="mc">{running && <><LoaderCircle className="spin" />{l10n.t("schedule.running_schedules")}</>}{[
    timingText(task.spec.trigger), multiple(location) ? projectName(location(task)) : "", multiple(agent) ? agent(task) : "",
    new Set(entries.map(e => e.machineId)).size > 1 ? machineName(m) : "",
    target.type === "resume_session" ? targetSession?.info ? l10n.t("schedule.in", { value: String(titleOf(targetSession)) }) : l10n.t("schedule.in_conversation") : "",
    task.status === "paused" ? l10n.t("queue.paused") : "", offline ? scheduleOfflineRow() : "",
  ].filter(Boolean).join(" · ")}</span>;
  const flags = (fresh ? " unread" : "") + (attention ? " attention" : "") + (task.status === "paused" ? " paused" : "") + (offline ? " off" : "") + (selected ? " sel" : "");
  const wrap = useRef<HTMLDivElement>(null);
  const props = {
    onContextMenu: (e: React.MouseEvent) => { if (matchMedia("(min-width: 900px)").matches) { e.preventDefault(); setMenu(true); } },
    onClick: () => go(taskRoute(machineId, task.id)),
  };
  return <div className={"schedule-row-wrap" + (selected ? " schedule-selected" : "")} ref={wrap}>
    {card ? (
      <button className={"rc schedule-line" + (attention ? " warn" : "") + flags} {...props}>
        {row1}
        {pv}
        <span className="f">{mc}<span className="go">{attention ? l10n.t("schedule.take_action") : l10n.t("schedule.read_full_text")}<ChevronRight /></span></span>
      </button>
    ) : (
      <button className={"sess schedule-line" + flags} {...props}>
        {gut && <span className="gut">{dot}</span>}
        <span className="sm">{row1}{pv}{mc}</span>
      </button>
    )}
    {notification && <button className="schedule-read-action" aria-label={fresh ? l10n.t("schedule.mark_as_read") : l10n.t("schedule.mark_as_unread")} onClick={mark}>{fresh ? l10n.t("schedule.mark_as_read") : l10n.t("schedule.mark_as_unread")}</button>}
    {menu && <ScheduleMenu remote={remote} machineId={machineId} task={task} contextMenu anchor={wrap} onClose={() => setMenu(false)} onUnread={selected ? () => go("tasks") : undefined} onDeleted={selected ? () => go("tasks") : undefined} />}
  </div>;
}

/** A task's ⋯: its name and 「项目 · CLI · 谁创建 · 日期」 at the top, then
 * 立即运行一次, 标为未读 and 暂停 / 恢复, and in a group of its own the red
 * 删除任务…. Opened from a wide row's context menu, 标为已读 or 标为未读
 * comes first. */
function ScheduleMenu({ remote, machineId, task, onClose, onUnread, onDeleted, onRecover, contextMenu = false, anchor }: { remote: Remote; machineId: string; task: ScheduleTask; onClose: () => void; onUnread?: () => void; onDeleted?: () => void; onRecover?: () => void; contextMenu?: boolean; anchor?: RefObject<HTMLElement | null> }) {
  const [busy, setBusy] = useState(false), [error, setError] = useState(""), [deleting, setDeleting] = useState(false);
  const [rearming, setRearming] = useState<ScheduleTask>();
  const online = remote.conn === "online" && remote.machines.get(machineId)?.online;
  const frozen = remote.scheduleAttempts.get(machineId + "/" + task.id)?.frozen;
  const ended = taskGroup(task) === "ended";
  const fresh = remote.scheduleHasNew(machineId, task);
  const paused = taskGroup(task) !== "active";
  const targetSession = remote.sessions.get(task.spec.target.session ?? "");
  const menuProject = task.spec.target.cwd || targetSession?.info?.cwd;
  const menuAgent = task.spec.target.type === "resume_session" ? targetSession?.info ? agentName(agentOf(targetSession)) : "" : (task.spec.target.agent ? agentById(task.spec.target.agent).name : agentInfo().name);
  const perform = async (operation: string, spec = task.spec, revision = task.revision) => {
    const after = fromHere(() => onDeleted?.());
    setBusy(true); setError("");
    try {
      await remote.changeSchedule(machineId, task.id, { operation, taskId: task.id, revision, ...(operation === "update" ? { spec } : {}) });
      onClose();
      if (operation === "cancel") after();
    } catch (e) { setRearming(undefined); setError(errorText(e)); } finally { setBusy(false); }
  };
  if (rearming) return <TimingSheet trigger={rearming.spec.trigger} onClose={() => setRearming(undefined)} onSave={trigger => void perform("update", { ...rearming.spec, trigger }, rearming.revision)} />;
  const readAction = latestNotification(task) && <button className="mi" role="menuitem" onClick={() => {
      if (contextMenu && fresh) remote.markScheduleRead(machineId, task);
      else { remote.markScheduleUnread(machineId, task, !!onUnread); onUnread?.(); }
      onClose();
    }}><span className="mt">{contextMenu && fresh ? l10n.t("schedule.mark_as_read") : l10n.t("schedule.mark_as_unread")}</span></button>;
  return <Sheet onClose={() => !busy && onClose()} className="menu schedule-menu" anchor={anchor}>
    <div className="menu-info"><b>{task.spec.title}</b>{[menuProject ? projectName(menuProject) : "", menuAgent, task.source === "agent" ? l10n.t("schedule.created_by_the_agent_in_a_conversation") : l10n.t("schedule.created_by_you"), dayText(task.created_at)].filter(Boolean).join(" · ")}</div>
    <div className="list" role="menu">
      {contextMenu && readAction}
      <button className="mi" role="menuitem" disabled={!online || busy || !!frozen || !!task.active_run || !!task.manual_request || ended} onClick={() => void perform("run")}><span className="mt">{l10n.t("schedule.run_once_now")}</span></button>
      {!contextMenu && readAction}
      {!ended && <button className="mi" role="menuitem" disabled={!online || busy || !!frozen} onClick={() => { if (paused && onRecover) onRecover(); else if (task.status === "cancelled" && oneShot(task)) setRearming(task); else void perform(paused ? task.status === "cancelled" ? "update" : task.status === "failed" && oneShot(task) ? "run" : "resume" : "pause"); }}><span className="mt">{paused ? l10n.t("schedule.resume") : l10n.t("schedule.pause")}</span></button>}
    </div>
    <div className="list" role="menu">
      <button className="mi danger" role="menuitem" disabled={!online || busy || !!frozen} onClick={() => setDeleting(true)}><span className="mt">{l10n.t("schedule.delete_task")}</span></button>
    </div>
    {error && <Banner kind="err">{error}</Banner>}
    {deleting && <Alert title={l10n.t("schedule.delete_task_schedules")} text={l10n.t("schedule.stops_automatic_runs_existing_conversations_remain")} ok={l10n.t("schedule.delete")} danger onCancel={() => setDeleting(false)} onOk={() => { setDeleting(false); void perform("cancel"); }} />}
  </Sheet>;
}

/** 定时 (design-system §6 定时列表): a sentence for a title (「N 个任务停下了」,
 * 「N 个新结果」, 「没有新结果」, 「还没有定时任务」) and the other kind
 * counted under it; the tasks that stopped as orange cards, those with
 * new results as blue ones under 新结果, the rest one a row under 其他任务,
 * and the ended ones folded. The wide sidebar lists the same groups as
 * rows. */
export function SchedulesPage({ remote, go, sidebar = false, selected }: { remote: Remote; go: Go; sidebar?: boolean; selected?: string }) {
  useRemote(remote);
  const [endedOpen, setEndedOpen] = useState(() => localStorage.getItem("schedule-ended-open") === "true");
  const now = useNow(60_000);
  const entries = remote.taskEntries();
  const hasNew = (e: { machineId: string; task: ScheduleTask }) => remote.scheduleHasNew(e.machineId, e.task);
  const groups = groupTasks(entries, hasNew);
  const supported = [...remote.machines.keys()].filter((id) =>
    remote.supportsSchedules(id),
  );
  const loading = [...remote.schedules.values()].some((s) => s.loading);
  const errors = supported.filter((id) => remote.schedules.get(id)?.error);
  const stopped = groups.get("attention") ?? [];
  const recent = groups.get("recent") ?? [];
  const fresh = recent.filter(hasNew);
  const others = recent.filter((e) => !hasNew(e));
  const ended = groups.get("ended") ?? [];
  const visible = stopped.length + recent.length + ended.length;
  const title = !visible ? (loading ? l10n.t("schedule.reading_tasks") : l10n.t("schedule.no_scheduled_tasks_yet"))
    : stopped.length ? rich(l10n.t("schedule.stopped_count", { count: stopped.length, total: "{total}" }), { total: <em>{stopped.length}</em> })
    : fresh.length ? l10n.t("schedule.new_results", { count: fresh.length }) : l10n.t("schedule.no_new_results");
  const sub = stopped.length > 0 && fresh.length > 0 ? l10n.t("schedule.more_new_results", { count: fresh.length }) : "";
  const rows = (list: typeof entries, layout: "card" | "row") => list.map((e) => (
    <ScheduleRow
      key={e.machineId + e.task.id}
      remote={remote}
      go={go}
      now={now}
      layout={sidebar ? "row" : layout}
      gut={sidebar}
      selected={selected === taskRoute(e.machineId, e.task.id)}
      {...e}
    />
  ));
  const list = (
    <>
      <TaskOffline remote={remote} />
      {errors.map((id) => (
        <Banner
          key={id}
          kind="err"
          action={l10n.t("common.retry")}
          onAction={() => void remote.refreshSchedules(id)}
        >
          {l10n.t("schedule.list_failed", { name: machineName(remote.machines.get(id)), reason: remote.schedules.get(id)?.error ?? "" })}
        </Banner>
      ))}
      {supported.length === 0 && (
        <Banner>{l10n.t("schedule.update_remote_agent_on_the_computer_to_use_scheduled")}</Banner>
      )}
      {!visible && !loading && <p className="home-note">{scheduleEmpty()}</p>}
      {stopped.length > 0 && <section className="schedule-group attention-group">{rows(stopped, "card")}</section>}
      {fresh.length > 0 && (
        <section className="schedule-group">
          <div className="sec">
            {l10n.t("schedule.new_results_schedules")}{fresh.length > 1 && <button className="sec-act" onClick={() => fresh.forEach((e) => remote.markScheduleRead(e.machineId, e.task))}>{l10n.t("schedule.mark_all_as_read")}</button>}
          </div>
          {rows(fresh, "card")}
        </section>
      )}
      {others.length > 0 && (
        <section className="schedule-group">
          <div className="sec">{l10n.t("schedule.other_tasks")}</div>
          {rows(others, "row")}
        </section>
      )}
      {ended.length > 0 && (
        <section className="schedule-group">
          <button className="tail schedule-ended" aria-expanded={endedOpen} onClick={() => setEndedOpen((old) => { localStorage.setItem("schedule-ended-open", String(!old)); return !old; })}>
            {l10n.t("schedule.ended")}{ended.length}
            <Exp up={endedOpen} />
          </button>
          {endedOpen && rows(ended, "row")}
        </section>
      )}
    </>
  );
  if (sidebar)
    return (
      <>
        <h1 className="whl">{title}</h1>
        {sub && <div className="wsub">{sub}</div>}
        <div className="scroll schedule-sidebar">{list}</div>
      </>
    );
  return (
    <div className="page schedules-page">
      <div className="body under-bar">
        <div className="tbar">
          <span className="tsp" />
        </div>
        <ConnBar remote={remote} />
        <h1 className="hl">{title}</h1>
        {sub && <div className="hl-sub">{sub}</div>}
        <div className="schedule-list">{list}</div>
      </div>
    </div>
  );
}
function Field({
  name,
  value,
  onClick,
  disabled,
}: {
  name: string;
  value: ReactNode;
  onClick?: () => void;
  disabled?: boolean;
}) {
  return onClick ? (
    <button
      className="row schedule-field"
      disabled={disabled}
      onClick={onClick}
    >
      <span>{name}</span>
      <span className="v">{value}</span>
      {!disabled && <ChevronRight className="chev" />}
    </button>
  ) : (
    <div className="row schedule-field">
      <span>{name}</span>
      <span className="v">{value}</span>
    </div>
  );
}
export function TimingSheet({
  trigger,
  onSave,
  onClose,
}: {
  trigger: ScheduleTrigger;
  onSave: (t: ScheduleTrigger) => void;
  onClose: () => void;
}) {
  const initialZone =
    trigger.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [zone, setZone] = useState(initialZone);
  const [kind, setKind] = useState(
    trigger.type === "calendar"
      ? trigger.weekdays?.length && trigger.weekdays.join() !== "1,2,3,4,5"
        ? "weekly"
        : "daily"
      : trigger.type === "after_turn"
        ? "after"
        : trigger.type,
  );
  const [at, setAt] = useState(
    localInput(
      trigger.at ?? new Date(Date.now() + 300_000).toISOString(),
      initialZone,
    ),
  );
  const [time, setTime] = useState(trigger.time ?? "08:00");
  const [workdays, setWorkdays] = useState(
    trigger.weekdays?.join() === "1,2,3,4,5",
  );
  const [days, setDays] = useState(
    trigger.weekdays?.length ? trigger.weekdays : [1],
  );
  const [minutes, setMinutes] = useState(
    String((trigger.interval_seconds ?? 3600) / 60),
  );
  const [end, setEnd] = useState(
    trigger.max_runs ? "count" : trigger.until ? "date" : "none",
  );
  const [count, setCount] = useState(String(trigger.max_runs || 10));
  const [until, setUntil] = useState(
    localInput(
      trigger.until ?? new Date(Date.now() + 86400_000).toISOString(),
      initialZone,
    ),
  );
  const [untilInstant, setUntilInstant] = useState(trigger.until ?? new Date(Date.now() + 86400_000).toISOString());
  const [untilEdited, setUntilEdited] = useState(false);
  const changeZone = (nextZone: string) => {
    setZone(nextZone);
    if (untilInstant) {
      try { setUntil(localInput(untilInstant, nextZone)); } catch { /* Keep partial IANA input editable. */ }
    }
  };
  const [error, setError] = useState("");
  const save = () => {
    try {
      // Validate IANA names even when a fixed interval doesn't use wall time.
      new Intl.DateTimeFormat("zh-CN", { timeZone: zone }).format();
      let next: ScheduleTrigger;
      if (kind === "at") next = { type: "at", at: zonedISO(at, zone) };
      else if (kind === "daily" || kind === "weekly") {
        if (kind === "weekly" && !days.length)
          throw new Error(l10n.t("schedule.select_at_least_one_day"));
        if (!/^\d\d:\d\d$/.test(time)) throw new Error(l10n.t("schedule.select_a_time"));
        next = {
          type: "calendar",
          time,
          timezone: zone,
          weekdays:
            kind === "weekly"
              ? [...days].sort()
              : workdays
                ? [1, 2, 3, 4, 5]
                : [],
        };
      } else if (kind === "after") next = { ...trigger };
      else {
        const rawSeconds = Number(minutes) * 60;
        const rounded = Math.round(rawSeconds);
        const seconds = trigger.type === "interval" && minutes === String(trigger.interval_seconds! / 60)
          ? trigger.interval_seconds!
          : Math.abs(rawSeconds - rounded) <= Number.EPSILON * Math.max(1, Math.abs(rawSeconds)) * 2 ? rounded : rawSeconds;
        if (!Number.isSafeInteger(seconds) || seconds < 1)
          throw new Error(l10n.t("schedule.the_interval_must_be_at_least_1_second"));
        next = {
          type: "interval",
          interval_seconds: seconds,
          delay_seconds: seconds,
        };
        // Preserve the phase on a non-timing edit of an existing interval.
        if (trigger.type === "interval" && seconds === trigger.interval_seconds)
          next = { ...trigger };
      }
      const initialEnd = trigger.max_runs ? "count" : trigger.until ? "date" : "none";
      const endChanged = end !== initialEnd || (end === "count" && count !== String(trigger.max_runs || 10)) || (end === "date" && untilEdited);
      if (end !== initialEnd || kind === "at") { delete next.max_runs; delete next.until; }
      else { next.max_runs = trigger.max_runs; next.until = trigger.until; }
      if (kind !== "at" && end === "count") {
        if (!Number.isSafeInteger(Number(count)) || Number(count) < 1)
          throw new Error(l10n.t("schedule.the_run_count_must_be_a_positive_integer"));
        next.max_runs = Number(count);
      }
      // The tracked instant survives zone changes; re-resolving the displayed wall time can shift it in a DST overlap.
      if (kind !== "at" && end === "date" && endChanged) next.until = untilInstant || zonedISO(until, zone);
      if (
        (next.at && Date.parse(next.at) <= Date.now() && kind === "at") ||
        (next.until && Date.parse(next.until) <= Date.now())
      )
        throw new Error(l10n.t("schedule.select_a_future_time"));
      onSave(next);
    } catch (e) {
      setError(errorText(e));
    }
  };
  return (
    <Sheet onClose={onClose} className="timing-sheet" title={l10n.t("schedule.when")} action={{ label: l10n.t("common.done"), onClick: save, disabled: kind === "weekly" && !days.length }}>
      <Segmented
        className="full"
        label={l10n.t("schedule.frequency")}
        value={kind}
        onChange={setKind}
        options={[
          ["at", l10n.t("schedule.once_schedules")],
          ["daily", l10n.t("schedule.daily")],
          ["weekly", l10n.t("schedule.weekly_on")],
          ["interval", l10n.t("schedule.every")],
          ...(kind === "after" ? [["after", l10n.t("schedule.after_turn_ends")]] : []),
        ].map(([value, label]) => ({ value, label }))}
      />
      <div className="schedule-inputs">
        {kind === "at" && (
          <label>
            {l10n.t("schedule.date_and_time")}<input
              type="datetime-local"
              value={at}
              onChange={(e) => setAt(e.target.value)}
            />
          </label>
        )}
        {(kind === "daily" || kind === "weekly") && (
          <label>
            {l10n.t("schedule.time")}<input
              type="time"
              value={time}
              onChange={(e) => setTime(e.target.value)}
            />
          </label>
        )}
        {kind === "daily" && (
          <label className="check-field">
            {l10n.t("schedule.weekdays_only")}<input
              type="checkbox"
              checked={workdays}
              onChange={(e) => setWorkdays(e.target.checked)}
            />
          </label>
        )}
        {kind === "weekly" && (
          <div className="weekdays">
            {[1, 2, 3, 4, 5, 6, 7].map((d) => (
              <button
                key={d}
                aria-pressed={days.includes(d)}
                className={days.includes(d) ? "on" : ""}
                onClick={() =>
                  setDays((ds) =>
                    ds.includes(d) ? ds.filter((x) => x !== d) : [...ds, d],
                  )
                }
              >
                {weekdayLabel(d % 7)}
              </button>
            ))}
          </div>
        )}
        {kind === "interval" && (
          <>
            <label>
              {l10n.t("schedule.interval_minutes")}<input
                type="number"
                min="0.0166666667"
                step="any"
                value={minutes}
                onChange={(e) => setMinutes(e.target.value)}
              />
            </label>
            <p className="hint">{scheduleIntervalHint()}</p>
          </>
        )}
        {kind === "after" && (
          <p>{l10n.t("schedule.after_turn_replace", { frequency: frequency(trigger) })}</p>
        )}
        {kind !== "at" && (
          <>
            <label>
              {l10n.t("session.end")}<select value={end} onChange={(e) => setEnd(e.target.value)}>
                <option value="none">{l10n.t("schedule.never")}</option>
                <option value="count">{l10n.t("schedule.after_a_run_count")}</option>
                <option value="date">{l10n.t("schedule.end_date")}</option>
              </select>
            </label>
            {end === "count" && (
              <label>
                {l10n.t("schedule.count")}<input
                  type="number"
                  min="1"
                  value={count}
                  onChange={(e) => setCount(e.target.value)}
                />
              </label>
            )}
            {end === "date" && (
              <label>
                {l10n.t("schedule.end_time")}<input
                  type="datetime-local"
                  value={until}
                  onChange={(e) => {
                    const value = e.target.value;
                    setUntil(value); setUntilEdited(true);
                    try { setUntilInstant(zonedISO(value, zone)); } catch { setUntilInstant(""); }
                  }}
                />
              </label>
            )}
          </>
        )}
        {kind !== "interval" && <label>
          {l10n.t("schedule.time_zone")}<input
            aria-label={l10n.t("schedule.time_zone")}
            value={zone}
            onChange={(e) => changeZone(e.target.value)}
            placeholder="Asia/Shanghai"
          />
        </label>}
      </div>
      {error && <Banner kind="err">{error}</Banner>}
      <div className="hint">
        {l10n.t("schedule.tasks_do_not_run_while_the_computer_sleeps_or")}</div>
    </Sheet>
  );
}

/** Reads through the ordinary verified GetEvents history consumer. */
function ScheduleResult({ remote, run, unread, now, open }: { remote: Remote; run: ScheduleRun; unread: boolean; now: number; open?: () => void }) {
  useRemote(remote);
  const [expanded, setExpanded] = useState(false), [long, setLong] = useState(false);
  const body = useRef<HTMLDivElement>(null);
  const sid = run.session_id;
  useEffect(() => {
    if (!sid) return;
    let cancelled = false, release: (() => void) | undefined;
    void remote.ensureSession(sid).then(() => {
      if (!cancelled) release = remote.retainHistory(sid);
    }).catch(() => undefined);
    return () => { cancelled = true; release?.(); };
  }, [remote, sid]);
  const history = sid ? remote.history(sid) : undefined;
  const full = sid && history?.loaded && !history.error && !history.broken && !history.backfilling ? finalRunText(remote.events(sid), run.id) : "";
  const text = full || run.final_summary || (run.reason ? reasonText(run.reason) : "");
  const title = runTitle(run);
  useEffect(() => {
    const el = body.current!;
    const measure = () => {
      const children = [...el.children] as HTMLElement[];
      children.forEach(child => { child.hidden = false; child.classList.remove("schedule-first-block-clipped"); });
      if (expanded || matchMedia("(min-width: 900px)").matches) { setLong(false); return; }
      const limit = (parseFloat(getComputedStyle(el).lineHeight) || 24) * 8;
      el.style.setProperty("--schedule-result-limit", `${limit}px`);
      let height = 0, cut = false;
      for (const [index, child] of children.entries()) {
        height += child.getBoundingClientRect().height + (parseFloat(getComputedStyle(child).marginTop) || 0);
        if (height > limit) cut = true;
        child.hidden = cut && index > 0;
        if (cut && index === 0) child.classList.add("schedule-first-block-clipped");
      }
      setLong(cut);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [text, expanded]);
  return <section className="schedule-result">
    <div className="schedule-result-head">{unread && <i className="mark unread schedule-unread-dot" role="img" aria-label={l10n.t("schedule.unread_on_entry")} />}<span>{dateTime(notificationTime(run), now)}</span>{run.session_id && open && <button onClick={open}>{l10n.t("schedule.conversation")}<ChevronRight /></button>}</div>
    {title && <h4>{title}</h4>}
    <div className="ai" ref={body}><Markdown text={text} /></div>
    {long && !expanded && <button className="schedule-expand" onClick={() => setExpanded(true)}>{l10n.t("markdown.expand")}<ChevronDown /></button>}
  </section>;
}

export function ScheduleDetail({
  remote,
  machineId,
  id,
  go,
  onBack,
}: {
  remote: Remote;
  machineId: string;
  id: string;
  go: Go;
  onBack: () => void;
}) {
  useRemote(remote);
  const now = useNow(60_000);
  const state = remote.schedules.get(machineId);
  const task = state?.tasks.find((t) => t.id === id);
  const visit = useMemo<ScheduleDetailVisit>(() => ({}), [machineId, id]);
  const live = remote.hasLiveSchedules(machineId);
  useEffect(() => {
    if (!live) return;
    remote.setScheduleDetail(machineId, id, visit);
    return () => { remote.setScheduleDetail(); };
  }, [remote, machineId, id, visit, live]);
  const [menu, setMenu] = useState(false);
  const moreRef = useRef<HTMLButtonElement>(null);
  const [snapshot, setSnapshot] = useState<{ task: string; runId?: string }>();
  const latest = task && latestNotification(task);
  useEffect(() => {
    if (task && live && snapshot?.task !== machineId + "/" + id) setSnapshot({ task: machineId + "/" + id, runId: latest?.id });
    // Entered with no result there is nothing to hold: the first one that comes is shown as it is.
    else if (snapshot?.task === machineId + "/" + id && snapshot.runId === undefined && latest) setSnapshot({ task: snapshot.task, runId: latest.id });
  }, [task, latest, snapshot, machineId, id, live]);
  const captured = snapshot?.task === machineId + "/" + id;
  const resultId = captured && snapshot.runId !== undefined ? snapshot.runId : latest?.id;
  const result = task && allRuns(task).find(run => run.id === resultId);
  const [edit, setEdit] = useState<"" | "name" | "time" | "message" | "target">(
    "",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [expanded, setExpanded] = useState(new Set<string>());
  const [allHistory, setAllHistory] = useState(false);
  const [editBase, setEditBase] = useState<ScheduleTask>();
  const m = remote.machines.get(machineId);
  const online = !!m?.online && remote.conn === "online";
  const perform = async (operation: string, spec?: ScheduleSpec, base?: ScheduleTask) => {
    if (!task || busy) return;
    const navigate = fromHere(go);
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await remote.changeSchedule(machineId, id, {
        operation,
        taskId: id,
        revision: (spec ? base ?? editBase : task)?.revision ?? task.revision,
        spec,
      });
      setEdit("");
      if (operation === "cancel") navigate("tasks");
      else if (operation === "run")
        setNotice(l10n.t("schedule.requested_a_run_now_the_automatic_schedule_is_unchanged"));
    } catch (e) {
      setError(errorText(e));
      if (e instanceof ScheduleRevisionError) {
        setEdit("");
        setEditBase(undefined);
      }
    } finally {
      setBusy(false);
    }
  };
  if (!task || !remote.supportsSchedules(machineId))
    return (
      <div className="page">
        <NavBar title={l10n.t("queue.android_scheduled")} onBack={onBack} />
        <div className="center muted">
          {state?.loading
            ? l10n.t("schedule.reading_tasks")
            : state?.error ||
              l10n.t("schedule.this_task_could_not_be_found_if_the_computer")}
        </div>
      </div>
    );
  const attention = taskGroup(task) === "attention";
  const systemCancelled = task.status === "cancelled" && attention;
  const failedOnce = task.status === "failed" && oneShot(task);
  const target = task.spec.target;
  const targetSession = remote.sessions.get(target.session ?? "");
  const permission =
    target.type === "new_session"
      ? (
          { ask: l10n.t("modes.ask"), auto: l10n.t("modes.auto"), full: l10n.t("modes.full_access") } as Record<
            string,
            string
          >
        )[target.mode ?? ""] || l10n.t("new_session.follow_cli")
      : targetSession
        ? (modeOf(targetSession)?.text ?? l10n.t("schedule.verify_current_permissions_in_the_conversation"))
        : l10n.t("schedule.verify_current_permissions_in_the_conversation");
  const openRun = (r: ScheduleRun) => {
    void remote
      .openScheduleRun(machineId, r)
      .catch((e) => setError(errorText(e)));
  };
  const row = (r: ScheduleRun) => (
    <button
      key={r.id}
      className="row schedule-run"
      disabled={!r.session_id}
      onClick={() => openRun(r)}
    >
      <span className="schedule-lead">{captured && visit.unread?.has(r.id) && <i className="mark unread schedule-unread-dot" role="img" aria-label={l10n.t("schedule.unread_on_entry")} />}</span><span className="schedule-run-text">
        <time>{dateTime(notificationTime(r), now)}</time>
        <small>{runSummary(r) || runResult(r)}</small>
      </span>
      {r.session_id && <ChevronRight className="chev" />}
    </button>
  );
  const pending = remote.scheduleAttempts.get(machineId + "/" + id);
  const missingTarget = target.type === "resume_session" && ["execution_session_missing", "execution_session_exited"].includes(task.reason ?? "");
  const recover = () => {
    if (systemCancelled && oneShot(task)) { setEditBase(task); setEdit("time"); }
    else void perform(systemCancelled ? "update" : failedOnce ? "run" : "resume", systemCancelled ? task.spec : undefined);
  };
  const runs = allRuns(task);
  const missingTargetRun = missingTarget ? runs.find(r => r.status === "failed" && r.reason === task.reason) ?? runs.find(r => r.status === "failed") : undefined;
  const earlier = runs.filter(r => r.id !== result?.id && r.id !== task.active_run?.id);
  const earlierTask = { ...task, runs: earlier, active_run: undefined };
  const history = allHistory ? earlier.map(r => ({ key: r.id, quiet: false, runs: [r] })) : groupRuns(earlierTask).slice(0, 5);
  return (
    <div className="page schedules-page schedule-detail">
      <NavBar
        title={task.spec.title}
        sub={attention ? l10n.t("schedule.stopped") : target.type === "resume_session" ? l10n.t("schedule.send_to", { value: String(timingText(task.spec.trigger)), detail: String(targetSession?.info ? titleOf(targetSession) : l10n.t("schedule.conversation")) }) : `${timingText(task.spec.trigger)} · ${taskGroup(task) === "active" ? task.next_run_at ? l10n.t("schedule.next", { value: dateTime(task.next_run_at, now) }) : l10n.t("schedule.next_run_pending") : nextText(task, now)}`}
        right={<button ref={moreRef} className="more" aria-label={l10n.t("common.ios_more")} onClick={() => setMenu(true)}><Ellipsis /></button>}
        onBack={onBack}
      />
      <ConnBar remote={remote} />
      <div className="body schedule-body schedule-detail-body">
        <div className="schedule-detail-content">
        <TaskOffline remote={remote} id={machineId} />
        {state?.error && (
          <Banner
            kind="err"
            action={l10n.t("common.retry")}
            onAction={() => void remote.refreshSchedules(machineId)}
          >
            {state.error}
          </Banner>
        )}
        {attention && (
          <div className="schedule-attention"><Banner kind="warn"><Info className="lead" />
            {missingTarget ? <>
              {missingTargetRun ? l10n.t("schedule.missing_target_run", { time: dateTime(missingTargetRun.started_at, now), reason: reasonText(task.reason) }) : l10n.t("schedule.missing_target", { reason: reasonText(task.reason) })}
            </> : <>
              {task.status === "uncertain" ? l10n.t("schedule.attention_uncertain", { reason: reasonText(task.reason) }) : failedOnce ? l10n.t("schedule.attention_once", { reason: reasonText(task.reason) }) : l10n.t("schedule.attention_resume", { reason: reasonText(task.reason) })}
              {task.reason === "agent_schedule_permissions_changed" && l10n.t("schedule.continuing_takes_over_the_task_and_runs_it_with")}
            </>}
          </Banner></div>
        )}
        {attention && <div className="schedule-recovery"><button className="btn pri" disabled={!online || busy || !!pending?.frozen || !!task.active_run || !!task.manual_request} onClick={recover}>{failedOnce ? l10n.t("schedule.run_once_now") : l10n.t("schedule.resume")}</button></div>}
        {error && <Banner kind="err">{error}</Banner>}
        {notice && <Banner>{notice}</Banner>}
        {pending?.frozen && (
          <Banner
            kind="warn"
            action={pending.expired ? l10n.t("schedule.verified_abandon_request") : l10n.t("schedule.retry_same_request")}
            onAction={() => {
              if (pending.expired) {
                pending.abandon();
                setError("");
                remote.touch();
              } else
                void perform(pending.payload!.operation, pending.payload?.spec);
            }}
          >
            {l10n.t("schedule.the_previous_operation_s_result_is_unconfirmed_verify_the")}</Banner>
        )}
        {latest?.id !== result?.id && <button className="schedule-more" onClick={() => setSnapshot({ task: machineId + "/" + id, runId: latest?.id })}>{l10n.t("home.ios_new_results")}</button>}
        {task.active_run && <div className="schedule-result schedule-running"><div className="schedule-result-head"><LoaderCircle className="spin" /><span>{l10n.t("schedule.running_since", { time: clockTime(task.active_run.started_at) })}</span>{task.active_run.session_id && <button onClick={() => openRun(task.active_run!)}>{l10n.t("schedule.conversation")}<ChevronRight /></button>}</div></div>}
        {result && <ScheduleResult key={result.id} remote={remote} run={result} unread={captured && !!visit.unread?.has(result.id)} now={now} open={target.type === "resume_session" && !!task.active_run ? undefined : () => openRun(result)} />}
        {!runs.length && <p className="gf">{l10n.t("schedule.has_not_run_yet")}</p>}
        {earlier.length > 0 && <section className="schedule-history">
        <div className="label">
          {l10n.t("schedule.earlier")}<span>{l10n.t("schedule.runs_count", { count: earlier.length })}</span>
        </div>
        <div className="grp">
          {history.map((g) =>
            g.quiet && g.runs.length > 1 ? (
              <div key={g.key}>
                <button
                  className="row schedule-run"
                  onClick={() =>
                    setExpanded((old) => {
                      const n = new Set(old);
                      if (n.has(g.key)) n.delete(g.key);
                      else n.add(g.key);
                      return n;
                    })
                  }
                >
                  <span className="schedule-lead" />
                  <span className="schedule-run-text">
                    <span className="quiet">
                      {l10n.t("schedule.quiet_runs", { day: dayText(notificationTime(g.runs[0]), now), count: g.runs.length })}</span>
                    <small>
                      {l10n.t("schedule.latest_summary", { time: clockTime(notificationTime(g.runs[0])), summary: g.runs[0].final_summary || l10n.t("schedule.no_notification") })}
                    </small>
                  </span>
                  <Exp up={expanded.has(g.key)} />
                </button>
                {expanded.has(g.key) && g.runs.map(row)}
              </div>
            ) : (
              g.runs.map(row)
            ),
          )}
          {!allHistory && earlier.length > 5 && <button className="row act schedule-more" onClick={() => setAllHistory(true)}>{l10n.t("schedule.view_all_runs", { count: earlier.length })}</button>}
        </div>
        </section>}
        <div className="label">{l10n.t("schedule.what_should_the_agent_do")}<button className="link" disabled={!online || busy || !!task.active_run || !!pending?.frozen} onClick={() => { setEditBase(task); setEdit("message"); }}>{l10n.t("tools.android_edit")}</button></div>
        <section className="schedule-description">
          <ScheduleText key={task.spec.message} text={task.spec.message} lines={3} />
        </section>
        <div className="label">{l10n.t("common.settings")}</div>
        <div className="grp">
          <Field
            name={l10n.t("schedule.when")}
            value={timingText(task.spec.trigger)}
            disabled={!online || busy || !!task.active_run || !!pending?.frozen}
            onClick={() => {
              setEditBase(task);
              setEdit("time");
            }}
          />
          <Field name={l10n.t("schedule.notify_on_completion")} value={<Switch label={l10n.t("schedule.notify_on_completion")} on={task.spec.notify !== "agent_only"} disabled={!online || busy || !!pending?.frozen} onChange={on => void perform("update", { ...task.spec, notify: on ? "finish" : "agent_only" }, task)} />} />
          {scheduleLimits(task.spec.trigger, now) && <Field name={l10n.t("session.end")} value={scheduleLimits(task.spec.trigger, now)} />}
          <Field
            name={target.type === "resume_session" ? l10n.t("schedule.send_to_schedules") : l10n.t("schedule.run_in")}
            value={
              target.type === "resume_session"
                ? (targetSession?.info ? titleOf(targetSession) : l10n.t("schedule.conversation"))
                : [
                    projectName(target.cwd ?? ""),
                    target.agent ? agentById(target.agent).name : agentInfo().name,
                    remote.machines.size > 1 ? machineName(m) : "",
                  ]
                    .filter(Boolean)
                    .join(" · ")
            }
            disabled={
              !online || (target.type === "resume_session" ? missingTarget :
              (busy || !!task.active_run || !!pending?.frozen))
            }
            onClick={
              target.type === "resume_session"
                ? () => go("s/" + target.session)
                : () => {
                    setEditBase(task);
                    setEdit("target");
                  }
            }
          />
          <Field
            name={l10n.t("new_session.permissions")}
            value={
              <span className={(target.type === "new_session" ? target.mode === "full" : !!targetSession && modeOf(targetSession)?.mode === ApprovalMode.FULL_ACCESS) ? "red" : ""}>
                {permission}
              </span>
            }
            disabled={!online || busy || !!task.active_run || !!pending?.frozen}
            onClick={
              target.type === "new_session"
                ? () => {
                    setEditBase(task);
                    setEdit("target");
                  }
                : undefined
            }
          />
          <Field
            name={l10n.t("schedule.task_name")}
            value={<span className="task-title">{task.spec.title}</span>}
            disabled={!online || busy || !!pending?.frozen}
            onClick={() => {
              setEditBase(task);
              setEdit("name");
            }}
          />
        </div>
        <p className="foot">
          {target.type === "new_session"
            ? l10n.t("schedule.each_run_opens_a_new_conversation_grouped_under_this")
            : l10n.t("schedule.messages_go_to_the_current_conversation_and_wait_for")}{" "}
          {task.source === "agent" ? l10n.t("schedule.created_by_the_agent_in_a_conversation") : l10n.t("schedule.created_by_you")} ·{" "}
          {dateTime(task.created_at, now)}
        </p>
        {!online && <p className="foot">{machineName(m)}{l10n.t("schedule.connect_to_edit_run_or_delete")}</p>}
        </div>
      </div>
      {menu && <ScheduleMenu remote={remote} machineId={machineId} task={task} onClose={() => setMenu(false)} onUnread={() => go("tasks")} onDeleted={() => go("tasks")} onRecover={() => { setMenu(false); recover(); }} anchor={moreRef} />}
      {edit === "target" && editBase && (
        <TaskTargetEditor
          remote={remote}
          machineId={machineId}
          task={editBase}
          busy={busy}
          error={error}
          onClose={() => !busy && setEdit("")}
          onSave={(target) =>
            void perform("update", { ...editBase.spec, target })
          }
        />
      )}
      {edit === "time" && editBase && (
        <TimingSheet
          trigger={editBase.spec.trigger}
          onClose={() => setEdit("")}
          onSave={(trigger) =>
            void perform("update", { ...editBase.spec, trigger })
          }
        />
      )}
      {edit === "name" && editBase && (
        <TaskNameDialog
          current={editBase.spec.title}
          message={editBase.spec.message}
          busy={busy}
          error={error}
          onClose={() => !busy && setEdit("")}
          onSave={(title) =>
            void perform("update", { ...editBase.spec, title })
          }
        />
      )}
      {edit === "message" && editBase && (
        <ScheduleMessageEditor initial={editBase.spec.message} busy={busy} disabled={!online || !!pending?.frozen} error={error} onClose={() => !busy && setEdit("")} onSave={message => void perform("update", { ...editBase.spec, message })} />
      )}

    </div>
  );
}

/** Same form, focus/select and save/cancel interaction as the session rename dialog. */
function TaskNameDialog({
  current,
  message,
  busy,
  error,
  onSave,
  onClose,
}: {
  current: string;
  message: string;
  busy: boolean;
  error: string;
  onSave: (name: string) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState(current);
  const input = useRef<HTMLInputElement>(null);
  useEscape(onClose);
  const root = useRef<HTMLDivElement>(null);
  useOverlayExit(root);
  useDialogFocus(root);
  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, []);
  return (
    <div className="overlay" ref={root}>
      <div className="dim" onClick={onClose} />
      <form
        className="dialog rename"
        role="dialog"
        aria-modal="true"
        aria-label={l10n.t("schedule.rename_task")}
        onSubmit={(e) => {
          e.preventDefault();
          if (!busy) {
            const resolved = taskName(name, message);
            if (name === current || resolved === current) onClose();
            else onSave(resolved);
          }
        }}
      >
        <h5>{l10n.t("schedule.rename_task")}</h5>
        <input
          ref={input}
          className="field"
          aria-label={l10n.t("schedule.task_name")}
          placeholder={l10n.t("schedule.leave_blank_to_use_the_message_s_first_sentence")}
          enterKeyHint="done"
          value={name}
          onChange={(e) => setName(clipTaskName(e.target.value))}
        />
        {error ? (
          <p className="red">{l10n.t("settings.save_error", { reason: error })}</p>
        ) : (
          <p>{l10n.t("schedule.saving_blank_uses_the_message_s_first_sentence")}</p>
        )}
        <div className="btns">
          <button
            type="button"
            className="btn"
            disabled={busy}
            onClick={onClose}
          >
            {l10n.t("common.cancel")}</button>
          <button type="submit" className="btn pri" disabled={busy}>
            {l10n.t("common.save")}</button>
        </div>
      </form>
    </div>
  );
}

function TaskTargetEditor({
  remote,
  machineId,
  task,
  busy,
  error,
  onSave,
  onClose,
}: {
  remote: Remote;
  machineId: string;
  task: ScheduleTask;
  busy: boolean;
  error: string;
  onSave: (target: ScheduleTarget) => void;
  onClose: () => void;
}) {
  const target = task.spec.target;
  const [cfg, setCfg] = useState<NewConfig>({
    machineId,
    cwd: target.cwd ?? "",
    agent: (target.agent ? agentById(target.agent) : agentInfo()).agent,
    model: target.model ?? "",
    effort: target.effort ?? "",
    mode:
      (
        {
          ask: ApprovalMode.ASK,
          auto: ApprovalMode.AUTO,
          full: ApprovalMode.FULL_ACCESS,
        } as Record<string, ApprovalMode>
      )[target.mode ?? ""] ?? ApprovalMode.UNSPECIFIED,
  });
  const base = useNewSession(remote, cfg.cwd, () => undefined, machineId);
  const info = useMachineInfo(remote, machineId, cfg.agent);
  const [config, setConfig] = useState(false);
  const [project, setProject] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const effectiveMode =
    cfg.mode || defaultModeOf(remote.machines.get(machineId), cfg.agent).mode;
  const save = () =>
    onSave({
      type: "new_session",
      cwd: cfg.cwd,
      agent: agentInfo(cfg.agent).id,
      model: cfg.model,
      effort: cfg.effort,
      mode: (
        {
          [ApprovalMode.ASK]: "ask",
          [ApprovalMode.AUTO]: "auto",
          [ApprovalMode.FULL_ACCESS]: "full",
        } as Record<number, string>
      )[effectiveMode],
    });
  const ns = {
    ...base,
    cfg,
    machine: remote.machines.get(machineId),
    info,
    agents: [cfg.agent],
    configFor: () => cfg,
    setCfg,
    unreadFor: () => false,
    setWatch: () => {},
  };
  return (
    <Sheet
      onClose={onClose}
      title={l10n.t("schedule.run_in")}
      action={{
        label: l10n.t("common.save"),
        disabled: busy || !effectiveMode || effectiveMode === ApprovalMode.PLAN,
        onClick: () => (effectiveMode === ApprovalMode.FULL_ACCESS ? setConfirm(true) : save()),
      }}
    >
      <div className="grp">
        <Field
          name={l10n.t("schedule.project")}
          value={projectName(cfg.cwd)}
          onClick={() => setProject(true)}
        />
        <Field
          name="CLI"
          value={
            base.agents.length > 1 ? (
              <Segmented
                label="CLI"
                value={String(cfg.agent)}
                onChange={(v) => {
                  const agent = Number(v) as typeof cfg.agent;
                  setCfg({ ...base.configFor(agent), machineId, cwd: cfg.cwd });
                }}
                options={base.agents.map((agent) => ({ value: String(agent), label: agentName(agent) }))}
              />
            ) : (
              agentName(cfg.agent)
            )
          }
        />
        <Field
          name={l10n.t("schedule.model_and_permissions")}
          value={[
            cfg.model || l10n.t("new_session.default_model"),
            effectiveMode
              ? (
                  {
                    [ApprovalMode.ASK]: l10n.t("modes.ask"),
                    [ApprovalMode.AUTO]: l10n.t("modes.auto"),
                    [ApprovalMode.PLAN]: l10n.t("modes.plan"),
                    [ApprovalMode.FULL_ACCESS]: l10n.t("modes.full_access"),
                  } as Record<number, string>
                )[effectiveMode]
              : l10n.t("schedule.select_permissions"),
          ].join(" · ")}
          onClick={() => setConfig(true)}
        />
      </div>
      <p className="hint">
        {l10n.t("schedule.only_changes_this_task_it_still_runs_on_the")}</p>
      {error && <Banner kind="err">{error}</Banner>}
      {config && (
        <ConfigSheet
          ns={ns}
          scheduled
          lockMachine
          onClose={() => setConfig(false)}
        />
      )}
      {project && (
        <NewProjectSheet
          remote={remote}
          fixedMachine={machineId}
          onClose={() => setProject(false)}
          onDone={(_, cwd) => {
            setCfg((c) => ({ ...c, cwd }));
            setProject(false);
          }}
        />
      )}
      {confirm && (
        <Alert
          title={l10n.t("schedule.run_with_full_access")}
          text={l10n.t("schedule.no_approval_will_be_required_for_future_runs_existing")}
          ok={l10n.t("modes.full_access")}
          danger
          onCancel={() => setConfirm(false)}
          onOk={() => {
            setConfirm(false);
            save();
          }}
        />
      )}
    </Sheet>
  );
}

export function ScheduleCreate({
  remote,
  go,
  onBack,
  session,
}: {
  remote: Remote;
  go: Go;
  onBack: () => void;
  session?: SessionView;
}) {
  useRemote(remote);
  const creationRoute = useRef(location.hash).current;
  const available = projects(
    remote.sessions.values(),
    remote.machines.values(),
    remote.projectPrefs().settings,
  ).filter((p) => !remote.projectPrefs().hidden.some((h) => h.path === p.key));
  const draftKey = "schedule:" + (session?.id ?? "new");
  const drafts =
    scheduleDrafts.get(remote) ??
    new Map<
      string,
      { project: string; name: string; trigger: ScheduleTrigger; notifyFinish?: boolean; dirty?: boolean }
    >();
  scheduleDrafts.set(remote, drafts);
  const [draft] = useState(() => {
    const saved = drafts.get(draftKey) ?? {
      project: available[0]?.key ?? "",
      name: "",
      trigger: {
        type: "calendar" as const,
        time: "08:00",
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      },
    };
    drafts.set(draftKey, saved);
    return saved;
  });
  const [name, pickName] = useState(draft.name);
  const setName = (value: string) => {
    const clipped = clipTaskName(value);
    draft.name = clipped;
    pickName(clipped);
  };
  const [project, pickProject] = useState(draft.project);
  const setProject = (key: string) => {
    draft.project = key;
    draft.dirty = true;
    pickProject(key);
  };
  const [choosing, setChoosing] = useState(false);
  // A request and its stable creation key survive route changes in Remote.
  const [message, setMessage] = useState(remote.drafts.get(draftKey) ?? "");
  const [notifyFinish, pickNotifyFinish] = useState(draft.notifyFinish ?? true);
  const setNotifyFinish = (on: boolean) => { draft.notifyFinish = on; draft.dirty = true; pickNotifyFinish(on); };
  const [trigger, pickTrigger] = useState<ScheduleTrigger>(draft.trigger);
  const setTrigger = (t: ScheduleTrigger) => {
    draft.trigger = t;
    draft.dirty = true;
    pickTrigger(t);
  };
  const [timing, setTiming] = useState(false);
  const setText = (text: string) => {
    setMessage(text);
    remote.drafts.set(draftKey, text);
  };
  const fields = (
    <>
      <label className="field big schedule-prompt">
        <small>{session ? l10n.t("composer.message") : l10n.t("schedule.what_should_the_agent_do")}</small>
        <ScheduleTextarea
          aria-label={l10n.t("schedule.what_should_the_agent_do")}
          placeholder={l10n.t("schedule.describe_the_scheduled_work_and_ask_the_agent_to")}
          value={message}
          onChange={(e) => setText(e.target.value)}
        />
      </label>
      {new TextEncoder().encode(message).length > scheduleMessageLimit && <p className="gf red">{l10n.t("schedule.too_long_maximum_32_kb")}</p>}
      <label className="field schedule-name" data-name-hint={message.trim() ? l10n.t("schedule.if_blank_uses", { value: String(taskName("", message)) }) : l10n.t("schedule.leave_blank_to_use_the_message_s_first_sentence")}>
        <small>{l10n.t("schedule.task_name")}</small>
        <input
          aria-label={l10n.t("schedule.task_name")}
          placeholder={message.trim() ? l10n.t("schedule.if_blank_uses", { value: String(taskName("", message)) }) : l10n.t("schedule.leave_blank_to_use_the_message_s_first_sentence")}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </label>
      <div className="grp">
        {!session && <Field name={l10n.t("schedule.notify_on_completion")} value={<Switch label={l10n.t("schedule.notify_on_completion")} on={notifyFinish} onChange={setNotifyFinish} />} />}
        <Field
          name={l10n.t("schedule.when")}
          value={timingText(trigger)}
          onClick={() => setTiming(true)}
        />
        {session && <Field name={l10n.t("schedule.notify_on_completion")} value={<Switch label={l10n.t("schedule.notify_on_completion")} on={notifyFinish} onChange={setNotifyFinish} />} />}
      </div>
    </>
  );
  const layers = (
    <>
      {choosing && (
        <NewFlow
          remote={remote}
          go={go}
          onClose={() => setChoosing(false)}
          onPicked={(key) => {
            setProject(key);
            setChoosing(false);
          }}
        />
      )}
      {timing && (
        <TimingSheet
          trigger={trigger}
          onSave={(t) => {
            setTrigger(t);
            setTiming(false);
          }}
          onClose={() => setTiming(false)}
        />
      )}
    </>
  );
  return (
    <ScheduleCreateConfig
      key={session?.id ?? project}
      remote={remote}
      project={project}
      name={name}
      message={message}
      trigger={trigger}
      notifyFinish={notifyFinish}
      session={session}
      onPickProject={() => setChoosing(true)}
      onDone={(machineId, task) => {
        remote.drafts.delete(draftKey);
        drafts.delete(draftKey);
        if (location.hash === creationRoute)
          go(session ? "s/" + session.id : taskRoute(machineId, task.id));
      }}
    >
      {(c) =>
        session ? (
          // From a conversation's ⋯: a sheet of the message, its name and
          // when, the ink 创建 under them; the rest follows the conversation.
          <div className="schedule-session-form">
            <div className="sheet-head">
              <span className="side-act" />
              <b>{l10n.t("schedule.schedule_messages_to_this_conversation")}</b>
              <button className="side-act" onClick={onBack}>{l10n.t("common.cancel")}</button>
            </div>
            <ConnBar remote={remote} />
            <div className="schedule-body">
              {fields}
              {c.notes}
              <div className="schedule-create-foot">{c.button}</div>
            </div>
            {layers}
            {c.layers}
          </div>
        ) : (
          // From 新定时: the whole-page sheet, close at the top left and the
          // ink 创建 at the top right.
          <div className="page rise schedules-page schedule-create">
            <header className="nt-hd">
              <button className="fbtn" aria-label={l10n.t("common.close")} onClick={onBack}>
                <X />
              </button>
              <b>{l10n.t("home.new_schedule")}</b>
              {c.button}
            </header>
            <ConnBar remote={remote} />
            <div className="body schedule-body">
              {fields}
              {c.rows}
              {c.notes}
            </div>
            {layers}
            {c.layers}
          </div>
        )
      }
    </ScheduleCreateConfig>
  );
}
type CreateParts = { rows: ReactNode; notes: ReactNode; button: ReactNode; layers: ReactNode };
function ScheduleCreateConfig({
  remote,
  project,
  name,
  message,
  trigger,
  notifyFinish,
  session,
  onPickProject,
  onDone,
  children,
}: {
  remote: Remote;
  project: string;
  name: string;
  message: string;
  trigger: ScheduleTrigger;
  notifyFinish: boolean;
  session?: SessionView;
  onPickProject: () => void;
  onDone: (machineId: string, t: ScheduleTask) => void;
  children: (parts: CreateParts) => ReactNode;
}) {
  const ns = useNewSession(remote, project, () => undefined);
  const [config, setConfig] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [full, setFull] = useState(false);
  const requestId = useRef(crypto.randomUUID());
  const machineId = session?.machineId ?? ns.cfg.machineId;
  const supported = remote.supportsSchedules(machineId);
  const online =
    remote.conn === "online" && !!remote.machines.get(machineId)?.online;
  const pendingKey = "create:" + (session?.id ?? project);
  const pending = remote.scheduleAttempts.get(machineId + "/" + pendingKey);
  const mode = ns.cfg.mode || defaultModeOf(ns.machine, ns.cfg.agent).mode;
  const createTask = async () => {
    if (!validScheduleMessage(message) || busy) return;
    if (!session && (!mode || mode === ApprovalMode.PLAN)) {
      setError(l10n.t("schedule.choose_ask_auto_or_full_access_scheduled_tasks_cannot"));
      return;
    }
    setBusy(true);
    setError("");
    const spec: ScheduleSpec = {
      title: taskName(name, message),
      message: message.trim(),
      notify: notifyFinish ? "finish" : "agent_only",
      trigger,
      target: session
        ? { type: "resume_session", session: session.id }
        : {
            type: "new_session",
            cwd: project,
            agent: agentInfo(ns.cfg.agent).id,
            model: ns.cfg.model,
            effort: ns.cfg.effort,
            mode: (
              {
                [ApprovalMode.ASK]: "ask",
                [ApprovalMode.AUTO]: "auto",
                [ApprovalMode.FULL_ACCESS]: "full",
              } as Record<number, string>
            )[mode],
          },
    };
    try {
      const task = await remote.changeSchedule(machineId, pendingKey, {
        operation: "create",
        spec,
        requestId: requestId.current,
        sessionId: session?.id,
      });
      onDone(machineId, task);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const disabled =
    !supported ||
    !online ||
    !validScheduleMessage(message) ||
    busy ||
    (!session && (!project || ns.reading || !!ns.issue || !mode || mode === ApprovalMode.PLAN));
  const rows = !session && (
    <div className="grp">
      <Field
        name={l10n.t("schedule.project")}
        value={project ? projectName(project) : l10n.t("projects.choose")}
        onClick={onPickProject}
      />
      <Field
        name="CLI"
        value={
          ns.agents.length > 1 ? (
            <Segmented
              label="CLI"
              value={String(ns.cfg.agent)}
              onChange={(v) => !pending?.frozen && ns.setAgent(Number(v) as typeof ns.cfg.agent)}
              options={ns.agents.map((a) => ({ value: String(a), label: agentName(a), disabled: !!pending?.frozen }))}
            />
          ) : (
            agentName(ns.cfg.agent)
          )
        }
      />
      <Field
        name={l10n.t("new_session.permissions")}
        value={<span className={mode === ApprovalMode.FULL_ACCESS ? "red" : ""}>{ns.modeText}</span>}
        disabled={!!pending?.frozen}
        onClick={() => setConfig(true)}
      />
      <Field
        name={remote.machines.size > 1 ? l10n.t("schedule.computer_and_model") : l10n.t("models.title")}
        value={
          remote.machines.size > 1
            ? [ns.parts.machine, ns.parts.model].join(" · ")
            : ns.parts.model
        }
        disabled={!!pending?.frozen}
        onClick={() => setConfig(true)}
      />
    </div>
  );
  const notes = (
    <>
      {!supported && (
        <Banner>{l10n.t("schedule.update_remote_agent_on_this_computer_to_use_scheduled")}</Banner>
      )}
      {!online && <Banner>{l10n.t("schedule.the_computer_is_offline_connect_to_create_tasks")}</Banner>}
      {error && <Banner kind="err">{error}</Banner>}
      {pending?.frozen && (
        <Banner
          kind="warn"
          action={pending.expired ? l10n.t("schedule.verified_abandon_request") : undefined}
          onAction={
            pending.expired
              ? () => {
                  pending.abandon();
                  requestId.current = crypto.randomUUID();
                  remote.touch();
                }
              : undefined
          }
        >
          {l10n.t("schedule.the_previous_creation_result_is_unconfirmed_retrying_uses_the")}</Banner>
      )}
      {!session && ns.issue && <Banner kind="warn">{ns.issue}</Banner>}
      {!session && mode === ApprovalMode.PLAN && <Banner kind="warn">{l10n.t("schedule.plan_mode_cannot_create_execution_tasks_choose_other_permissions")}</Banner>}
      {!session && !mode && <p className="gf">{l10n.t("schedule.select_explicit_permissions_before_creating_a_scheduled_task")}</p>}
      <p className="gf">
        {session
          ? l10n.t("schedule.messages_will_go_to_the_current_conversation_and_wait")
          : l10n.t("schedule.each_run_opens_a_new_conversation_grouped_under_this_schedules")}
        {l10n.t("schedule.notify_on_completion_by_default_when_off_only_the")}</p>
    </>
  );
  const button = (
    <button
      className={"btn pri" + (session ? "" : " sm")}
      disabled={disabled || pending?.expired}
      onClick={() => {
        if (!session && mode === ApprovalMode.FULL_ACCESS && !pending?.frozen) setFull(true);
        else void createTask();
      }}
    >
      {busy ? l10n.t("schedule.creating") : pending?.frozen ? l10n.t("schedule.retry_creation") : l10n.t("schedule.create")}
    </button>
  );
  const layers = (
    <>
      {config && (
        <ConfigSheet ns={ns} scheduled onClose={() => setConfig(false)} />
      )}
      {full && (
        <Alert
          title={l10n.t("schedule.run_with_full_access")}
          text={l10n.t("schedule.no_approval_is_required_for_any_operation_whenever_this")}
          ok={l10n.t("modes.full_access")}
          danger
          onCancel={() => setFull(false)}
          onOk={() => {
            setFull(false);
            void createTask();
          }}
        />
      )}
    </>
  );
  return <>{children({ rows, notes, button, layers })}</>;
}

export function ScheduleStrip({
  remote,
  session,
  go,
}: {
  remote: Remote;
  session: SessionView;
  go: Go;
}) {
  useRemote(remote);
  const now = useNow(60_000);
  const tasks = remote.sessionTasks(session.id);
  const [choosing, setChoosing] = useState(false);
  if (!tasks.length) return null;
  const nearest = [...tasks].sort((a, b) =>
    (a.task.next_run_at ?? "9999").localeCompare(b.task.next_run_at ?? "9999"),
  )[0];
  return (
    <>
      <button
        className="schedule-strip glass"
        onClick={() =>
          tasks.length === 1
            ? go(taskRoute(nearest.machineId, nearest.task.id))
            : setChoosing(true)
        }
      >
        <Clock />
        <span>
          {tasks.length > 1
            ? l10n.t("schedule.strip_count", { count: tasks.length, state: remote.machines.get(session.machineId)?.online && remote.conn === "online" ? nextText(nearest.task, now) : scheduleOffline() })
            : l10n.t("schedule.strip_single", { frequency: frequency(nearest.task.spec.trigger), state: remote.machines.get(session.machineId)?.online && remote.conn === "online" ? nextText(nearest.task, now) : scheduleOffline() })}
        </span>
        <span className="act">
          {l10n.t("schedule.manage")}<ChevronRight />
        </span>
      </button>
      {choosing && (
        <Sheet onClose={() => setChoosing(false)} title={l10n.t("schedule.this_conversation_s_scheduled_tasks")}>
          <div className="list schedule-choices">
            {tasks.map((e) => (
              <ScheduleRow key={e.task.id} remote={remote} go={go} gut {...e} />
            ))}
          </div>
        </Sheet>
      )}
    </>
  );
}
