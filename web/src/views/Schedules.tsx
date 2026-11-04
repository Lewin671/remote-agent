import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import { agentById, agentInfo } from "../agents";
import { Clock, Plus, ChevronRight, ChevronDown, ChevronUp, Ellipsis, LoaderCircle } from "../icons";
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
import {
  Switch,
  Alert,
  fromHere,
  Banner,
  ConnBar,
  NavBar,
  Sheet,
  useNow,
  useRemote,
  useEscape,
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
  taskGroup,
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
  const offline = machines.filter((m) => !m.online);
  return (
    <>
      {remote.conn === "online" && offline.length > 0 && (
        <Banner>
          {offline.map((m) => machineName(m)).join("、")} {scheduleOffline}
        </Banner>
      )}
    </>
  );
}
export function ScheduleRow({
  remote,
  machineId,
  task,
  go,
  now = Date.now(),
  selected = false,
}: {
  remote: Remote;
  machineId: string;
  task: ScheduleTask;
  go: Go;
  now?: number;
  selected?: boolean;
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
  const attention = taskGroup(task) === "需要你看一下";
  const fresh = remote.scheduleHasNew(machineId, task);
  const notification = latestNotification(task);
  const at = attention ? stoppedTime(task) : notificationTime(notification);
  const offline = remote.conn !== "online" || !m?.online;
  const running = task.active_run && ["dispatching", "queued", "running"].includes(task.active_run.status);
  const [menu, setMenu] = useState(false);
  const mark = () => { if (fresh) remote.markScheduleRead(machineId, task); else { remote.markScheduleUnread(machineId, task, selected); if (selected) go("tasks"); } };
  return <div className="schedule-row-wrap">
    <button className={"sess schedule-line" + (fresh ? " unread" : "") + (attention ? " attention" : "") + (task.status === "paused" ? " paused" : "") + (offline ? " off" : "")}
      onContextMenu={e => { if (matchMedia("(min-width: 900px)").matches) { e.preventDefault(); setMenu(true); } }}
      onClick={() => go(taskRoute(machineId, task.id))}>
      <span className="schedule-lead">{(attention || fresh) && <span className={"schedule-unread-dot" + (attention ? " attention-dot" : "")} role="img" aria-label={attention ? "需要你处理" : "有新结果"} />}</span>
      <span className="sm"><span className="row1"><b>{task.spec.title}</b><span>{at ? scheduleTime(at, now) : ""}</span></span>
        <span className="pv">{attention ? <><strong>{task.status === "failed" && oneShot(task) ? "没能运行" : "已停下"}</strong> · {reasonText(task.reason).replace(/，任务已停下$/, "")}</> : notification ? runSummary(notification) || runResult(notification) : running ? "还没有结果" : allRuns(task).length ? lastResult(task, now) : `还没有运行${task.next_run_at ? " · 下次 " + dateTime(task.next_run_at, now) : ""}`}</span>
        <span className="mc">{running && <><LoaderCircle className="spin" />正在运行 · </>}{[
          timingText(task.spec.trigger), multiple(location) ? projectName(location(task)) : "", multiple(agent) ? agent(task) : "",
          new Set(entries.map(e => e.machineId)).size > 1 ? machineName(m) : "",
          target.type === "resume_session" ? targetSession?.info ? `在「${titleOf(targetSession)}」里` : "在对话里" : "",
          task.status === "paused" ? "已暂停" : "", offline ? scheduleOfflineRow : "",
        ].filter(Boolean).join(" · ")}</span>
      </span>
    </button>
    {notification && <button className="schedule-read-action" aria-label={fresh ? "标为已读" : "标为未读"} onClick={mark}>{fresh ? "标为已读" : "标为未读"}</button>}
    {menu && <ScheduleMenu remote={remote} machineId={machineId} task={task} contextMenu onClose={() => setMenu(false)} onUnread={selected ? () => go("tasks") : undefined} onDeleted={selected ? () => go("tasks") : undefined} />}
  </div>;
}

function ScheduleMenu({ remote, machineId, task, onClose, onUnread, onDeleted, onRecover, contextMenu = false }: { remote: Remote; machineId: string; task: ScheduleTask; onClose: () => void; onUnread?: () => void; onDeleted?: () => void; onRecover?: () => void; contextMenu?: boolean }) {
  const [busy, setBusy] = useState(false), [error, setError] = useState(""), [deleting, setDeleting] = useState(false);
  const [rearming, setRearming] = useState<ScheduleTask>();
  const online = remote.conn === "online" && remote.machines.get(machineId)?.online;
  const frozen = remote.scheduleAttempts.get(machineId + "/" + task.id)?.frozen;
  const ended = taskGroup(task) === "已结束";
  const fresh = remote.scheduleHasNew(machineId, task);
  const paused = taskGroup(task) !== "进行中";
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
  const readAction = latestNotification(task) && <button className="row" onClick={() => {
      if (contextMenu && fresh) remote.markScheduleRead(machineId, task);
      else { remote.markScheduleUnread(machineId, task, !!onUnread); onUnread?.(); }
      onClose();
    }}>{contextMenu && fresh ? "标为已读" : "标为未读"}</button>;
  return <Sheet onClose={() => !busy && onClose()} className="schedule-menu">
    <div className="schedule-menu-title"><b>{task.spec.title}</b><small>{[menuProject ? projectName(menuProject) : "", menuAgent, task.source === "agent" ? "Agent 在对话里创建" : "你创建", dayText(task.created_at)].filter(Boolean).join(" · ")}</small></div>
    {contextMenu && readAction}
    <button className="row" disabled={!online || busy || !!frozen || !!task.active_run || !!task.manual_request || ended} onClick={() => void perform("run")}>立即运行一次</button>
    {!contextMenu && readAction}
    {!ended && <button className="row" disabled={!online || busy || !!frozen} onClick={() => { if (paused && onRecover) onRecover(); else if (task.status === "cancelled" && oneShot(task)) setRearming(task); else void perform(paused ? task.status === "cancelled" ? "update" : task.status === "failed" && oneShot(task) ? "run" : "resume" : "pause"); }}>{paused ? "恢复" : "暂停"}</button>}
    <button className="row red" disabled={!online || busy || !!frozen} onClick={() => setDeleting(true)}>删除任务…</button>
    {error && <Banner kind="err">{error}</Banner>}
    {deleting && <Alert title="删除任务？" text="不再自动运行，已开出的对话保留。" ok="删除" danger onCancel={() => setDeleting(false)} onOk={() => { setDeleting(false); void perform("cancel"); }} />}
  </Sheet>;
}

export function SchedulesPage({ remote, go, sidebar = false, selected }: { remote: Remote; go: Go; sidebar?: boolean; selected?: string }) {
  useRemote(remote);
  const [endedOpen, setEndedOpen] = useState(() => localStorage.getItem("schedule-ended-open") === "true");
  const now = useNow(60_000);
  const entries = remote.taskEntries();
  const groups = groupTasks(entries, e => remote.scheduleHasNew(e.machineId, e.task));
  const supported = [...remote.machines.keys()].filter((id) =>
    remote.supportsSchedules(id),
  );
  const loading = [...remote.schedules.values()].some((s) => s.loading);
  const errors = supported.filter((id) => remote.schedules.get(id)?.error);
  return (
    <div className={"page schedules-page" + (sidebar ? " schedule-sidebar" : "")}>
      <ConnBar remote={remote} />
      <div className="large">
        <h3>定时任务</h3>
        <button className="nb" aria-label="新任务" title="新任务" disabled={!supported.length} onClick={() => go("tasks/new")}><Plus /></button>
      </div>
      <div className="body schedule-body">
        <TaskOffline remote={remote} />
        {errors.map((id) => (
          <Banner
            key={id}
            kind="err"
            action="重试"
            onAction={() => void remote.refreshSchedules(id)}
          >
            {machineName(remote.machines.get(id))} 的任务没能读取：
            {remote.schedules.get(id)?.error}
          </Banner>
        ))}
        {supported.length === 0 && (
          <Banner>需要更新电脑上的 remote-agent 才能使用定时任务。</Banner>
        )}
        {!entries.length && (
          <div className="schedule-empty">
            <b>{loading ? "正在读取任务…" : "还没有定时任务"}</b>
            <p>
              {scheduleEmpty}
            </p>
          </div>
        )}
        {[...groups].map(
          ([group, tasks]) =>
            tasks.length > 0 && (
              <section key={group}>
                {group === "已结束" ? <button className="schedule-ended" aria-expanded={endedOpen} onClick={() => setEndedOpen(old => { localStorage.setItem("schedule-ended-open", String(!old)); return !old; })}>
                  <span>已结束</span><span>{tasks.length} {endedOpen ? <ChevronUp /> : <ChevronDown />}</span>
                </button> : <div className={"glabel" + (group === "需要你看一下" ? " schedule-attention-label" : "")}>{group}{group === "需要你看一下" ? <span>{tasks.length}</span> : tasks.some(e => remote.scheduleHasNew(e.machineId, e.task)) && <button className="schedule-mark-all" onClick={() => tasks.forEach(e => remote.markScheduleRead(e.machineId, e.task))}>全部标为已读</button>}</div>}
                {(group !== "已结束" || endedOpen) && tasks.map((e) => (
                  <div className={selected === taskRoute(e.machineId, e.task.id) ? "schedule-selected" : ""} key={e.machineId + e.task.id}><ScheduleRow
                    key={e.machineId + e.task.id}
                    remote={remote}
                    go={go}
                    now={now}
                    selected={selected === taskRoute(e.machineId, e.task.id)}
                    {...e}
                  /></div>
                ))}
              </section>
            ),
        )}

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
      <span className="v">
        {value}
        {!disabled && <ChevronRight />}
      </span>
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
          throw new Error("请至少选择一天");
        if (!/^\d\d:\d\d$/.test(time)) throw new Error("请选择时间");
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
          throw new Error("间隔至少为 1 秒");
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
          throw new Error("运行次数必须是正整数");
        next.max_runs = Number(count);
      }
      // The tracked instant survives zone changes; re-resolving the displayed wall time can shift it in a DST overlap.
      if (kind !== "at" && end === "date" && endChanged) next.until = untilInstant || zonedISO(until, zone);
      if (
        (next.at && Date.parse(next.at) <= Date.now() && kind === "at") ||
        (next.until && Date.parse(next.until) <= Date.now())
      )
        throw new Error("请选择未来的时间");
      onSave(next);
    } catch (e) {
      setError(errorText(e));
    }
  };
  return (
    <Sheet onClose={onClose} className="timing-sheet">
      <div className="ns-head">
        <b>什么时候</b>
        <button disabled={kind === "weekly" && !days.length} onClick={save}>完成</button>
      </div>
      <div className="seg">
        {[
          ["at", "一次"],
          ["daily", "每天"],
          ["weekly", "每周"],
          ["interval", "每隔"],
          ...(kind === "after" ? [["after", "轮结束后"]] : []),
        ].map(([id, label]) => (
          <button
            key={id}
            className={kind === id ? "on" : ""}
            onClick={() => setKind(id)}
          >
            {label}
          </button>
        ))}
      </div>
      <div className="schedule-inputs">
        {kind === "at" && (
          <label>
            日期和时间
            <input
              type="datetime-local"
              value={at}
              onChange={(e) => setAt(e.target.value)}
            />
          </label>
        )}
        {(kind === "daily" || kind === "weekly") && (
          <label>
            时间
            <input
              type="time"
              value={time}
              onChange={(e) => setTime(e.target.value)}
            />
          </label>
        )}
        {kind === "daily" && (
          <label className="check-field">
            只在工作日
            <input
              type="checkbox"
              checked={workdays}
              onChange={(e) => setWorkdays(e.target.checked)}
            />
          </label>
        )}
        {kind === "weekly" && (
          <div className="weekdays">
            {[1, 2, 3, 4, 5, 6, 7].map((d, i) => (
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
                周{["一", "二", "三", "四", "五", "六", "日"][i]}
              </button>
            ))}
          </div>
        )}
        {kind === "interval" && (
          <>
            <label>
              间隔（分钟）
              <input
                type="number"
                min="0.0166666667"
                step="any"
                value={minutes}
                onChange={(e) => setMinutes(e.target.value)}
              />
            </label>
            <p className="hint">{scheduleIntervalHint}</p>
          </>
        )}
        {kind === "after" && (
          <p>{frequency(trigger)}。选择上方其他频率可替换。</p>
        )}
        {kind !== "at" && (
          <>
            <label>
              结束
              <select value={end} onChange={(e) => setEnd(e.target.value)}>
                <option value="none">不结束</option>
                <option value="count">运行指定次数</option>
                <option value="date">截止日期</option>
              </select>
            </label>
            {end === "count" && (
              <label>
                次数
                <input
                  type="number"
                  min="1"
                  value={count}
                  onChange={(e) => setCount(e.target.value)}
                />
              </label>
            )}
            {end === "date" && (
              <label>
                截止时间
                <input
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
          时区
          <input
            aria-label="时区"
            value={zone}
            onChange={(e) => changeZone(e.target.value)}
            placeholder="Asia/Shanghai"
          />
        </label>}
      </div>
      {error && <Banner kind="err">{error}</Banner>}
      <div className="hint">
        电脑睡眠或关机时不会运行；醒来后默认在 24
        小时内补跑最近一次，再晚就跳过。立即运行一次不占自动计划的次数。
      </div>
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
  const full = sid && history?.loaded && !history.error && !history.broken && !history.backfilling && !remote.hasUnverifiedHistory(sid) ? finalRunText(remote.events(sid), run.id) : "";
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
    <div className="schedule-result-head"><span>{unread && <span className="schedule-unread-dot" role="img" aria-label="进入时未读" />}{dateTime(notificationTime(run), now)}</span>{run.session_id && open && <button onClick={open}>对话 <ChevronRight /></button>}</div>
    {title && <h4>{title}</h4>}
    <div className="ai" ref={body}><Markdown text={text} /></div>
    {long && !expanded && <button className="schedule-expand" onClick={() => setExpanded(true)}>展开全文 <ChevronDown /></button>}
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
  const [snapshot, setSnapshot] = useState<{ task: string; runId?: string }>();
  const latest = task && latestNotification(task);
  useEffect(() => {
    if (task && live && snapshot?.task !== machineId + "/" + id) setSnapshot({ task: machineId + "/" + id, runId: latest?.id });
  }, [task, latest, snapshot, machineId, id, live]);
  const captured = snapshot?.task === machineId + "/" + id;
  const resultId = captured ? snapshot.runId : latest?.id;
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
        setNotice("已请求立即运行一次，不改变自动计划。");
    } catch (e) {
      setError(errorText(e));
      if (errorText(e).includes("任务刚被修改")) {
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
        <NavBar title="定时任务" onBack={onBack} />
        <div className="center muted">
          {state?.loading
            ? "正在读取任务…"
            : state?.error ||
              "找不到这个任务。电脑离线且没有缓存时，请连上电脑后重试。"}
        </div>
      </div>
    );
  const attention = taskGroup(task) === "需要你看一下";
  const systemCancelled = task.status === "cancelled" && attention;
  const failedOnce = task.status === "failed" && oneShot(task);
  const target = task.spec.target;
  const targetSession = remote.sessions.get(target.session ?? "");
  const permission =
    target.type === "new_session"
      ? (
          { ask: "询问", auto: "自动", full: "完全放行" } as Record<
            string,
            string
          >
        )[target.mode ?? ""] || "跟随 CLI 设置"
      : targetSession
        ? (modeOf(targetSession)?.text ?? "请在对话里核实当前权限")
        : "请在对话里核实当前权限";
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
      <span className="schedule-lead">{captured && visit.unread?.has(r.id) && <span className="schedule-unread-dot" role="img" aria-label="进入时未读" />}</span><span className="schedule-run-text">
        <time>{dateTime(notificationTime(r), now)}</time>
        <small>{runSummary(r) || runResult(r)}</small>
      </span>
      {r.session_id && <ChevronRight />}
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
    <div className="page gray schedules-page">
      <NavBar
        title={task.spec.title}
        sub={attention ? "已停下" : target.type === "resume_session" ? `${timingText(task.spec.trigger)} · 发到「${targetSession?.info ? titleOf(targetSession) : "对话"}」` : `${timingText(task.spec.trigger)} · ${taskGroup(task) === "进行中" ? task.next_run_at ? "下次 " + dateTime(task.next_run_at, now) : "下次待定" : nextText(task, now)}`}
        right={<button className="more" aria-label="更多" onClick={() => setMenu(true)}><Ellipsis /></button>}
        onBack={onBack}
      />
      <ConnBar remote={remote} />
      <div className="body schedule-body schedule-detail-body">
        <div className="schedule-detail-content">
        <TaskOffline remote={remote} id={machineId} />
        {state?.error && (
          <Banner
            kind="err"
            action="重试"
            onAction={() => void remote.refreshSchedules(machineId)}
          >
            {state.error}
          </Banner>
        )}
        {attention && (
          <div className="schedule-attention"><Banner kind="warn">
            {missingTarget ? <>
              {missingTargetRun && `${dateTime(missingTargetRun.started_at, now)} 的运行失败，`}任务已停下：{reasonText(task.reason)}。这个任务只发到原来的对话，恢复后还会失败。
            </> : <>
              {reasonText(task.reason)}。
              {task.status === "uncertain"
                ? "继续可能重复执行上次的消息，请先打开运行记录核实。"
                : failedOnce
                  ? "可改时间重排。"
                  : "恢复或重新安排后从下一次自动时间开始，不重发上次消息。"}
              {task.reason === "agent_schedule_permissions_changed" &&
                "继续会由你接管任务，按任务中显示的权限运行。"}
            </>}
          </Banner></div>
        )}
        {attention && <div className="pad schedule-recovery"><button className="btn pri wide" disabled={!online || busy || !!pending?.frozen || !!task.active_run || !!task.manual_request} onClick={recover}>{failedOnce ? "立即运行一次" : "恢复"}</button></div>}
        {error && <Banner kind="err">{error}</Banner>}
        {notice && <Banner>{notice}</Banner>}
        {pending?.frozen && (
          <Banner
            kind="warn"
            action={pending.expired ? "已核实，放弃请求" : "重试同一请求"}
            onAction={() => {
              if (pending.expired) {
                pending.abandon();
                setError("");
                remote.touch();
              } else
                void perform(pending.payload!.operation, pending.payload?.spec);
            }}
          >
            上次操作结果还没有确认。请核对任务与记录，重试会使用同一个请求。
          </Banner>
        )}
        {latest?.id !== result?.id && <button className="schedule-more" onClick={() => setSnapshot({ task: machineId + "/" + id, runId: latest?.id })}>有新结果</button>}
        {task.active_run && <div className="schedule-result schedule-running"><LoaderCircle className="spin" />正在运行 · {clockTime(task.active_run.started_at)} 开始{task.active_run.session_id && <button onClick={() => openRun(task.active_run!)}>对话 <ChevronRight /></button>}</div>}
        {result && <ScheduleResult key={result.id} remote={remote} run={result} unread={captured && !!visit.unread?.has(result.id)} now={now} open={target.type === "resume_session" && !!task.active_run ? undefined : () => openRun(result)} />}
        {!runs.length && <p className="hint">还没有运行</p>}
        {earlier.length > 0 && <section className="schedule-history">
        <div className="glabel">
          更早<span>{earlier.length} 次</span>
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
                  <span>
                    {dayText(notificationTime(g.runs[0]), now)} · {g.runs.length}{" "}
                    次，都没有通知
                    <small>
                      最近一次 {clockTime(notificationTime(g.runs[0]))}：
                      {g.runs[0].final_summary || "没有通知"}
                    </small>
                  </span>
                  {expanded.has(g.key) ? <ChevronUp /> : <ChevronDown />}
                </button>
                {expanded.has(g.key) && g.runs.map(row)}
              </div>
            ) : (
              g.runs.map(row)
            ),
          )}
          {!allHistory && earlier.length > 5 && <button className="row act schedule-more" onClick={() => setAllHistory(true)}>查看全部 {earlier.length} 次</button>}
        </div>
        </section>}
        <section className="schedule-description">
          <div className="schedule-description-head"><span>让 agent 做什么</span><button disabled={!online || busy || !!task.active_run || !!pending?.frozen} onClick={() => { setEditBase(task); setEdit("message"); }}>编辑{online && !busy && !task.active_run && !pending?.frozen && <ChevronRight />}</button></div>
          <ScheduleText key={task.spec.message} text={task.spec.message} lines={3} />
        </section>
        <div className="glabel">设置</div>
        <div className="grp">
          <Field
            name="什么时候"
            value={timingText(task.spec.trigger)}
            disabled={!online || busy || !!task.active_run || !!pending?.frozen}
            onClick={() => {
              setEditBase(task);
              setEdit("time");
            }}
          />
          <Field name="完成后通知" value={<Switch label="完成后通知" on={task.spec.notify !== "agent_only"} disabled={!online || busy || !!pending?.frozen} onChange={on => void perform("update", { ...task.spec, notify: on ? "finish" : "agent_only" }, task)} />} />
          {scheduleLimits(task.spec.trigger, now) && <Field name="结束" value={scheduleLimits(task.spec.trigger, now)} />}
          <Field
            name={target.type === "resume_session" ? "发到" : "在哪运行"}
            value={
              target.type === "resume_session"
                ? (targetSession?.info ? titleOf(targetSession) : "对话")
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
            name="权限"
            value={
              <span className={permission === "完全放行" ? "red" : ""}>
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
            name="任务名"
            value={<span className="task-title">{task.spec.title}</span>}
            disabled={!online || busy || !!pending?.frozen}
            onClick={() => {
              setEditBase(task);
              setEdit("name");
            }}
          />
        </div>
        <p className="manage-hint">
          {target.type === "new_session"
            ? "每次新开一个对话，归在这个任务下面。"
            : "消息发到当前对话，等待当前轮结束。"}{" "}
          {task.source === "agent" ? "Agent 在对话里创建" : "你创建"} ·{" "}
          {dateTime(task.created_at, now)}
        </p>
        {!online && <p className="manage-hint">{machineName(m)} 连上后才能修改、运行或删除。</p>}
        </div>
      </div>
      {menu && <ScheduleMenu remote={remote} machineId={machineId} task={task} onClose={() => setMenu(false)} onUnread={() => go("tasks")} onDeleted={() => go("tasks")} onRecover={() => { setMenu(false); recover(); }} />}
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
  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, []);
  return (
    <div className="overlay">
      <div className="dim" onClick={onClose} />
      <form
        className="alert rename"
        role="dialog"
        aria-modal="true"
        aria-label="重命名任务"
        onSubmit={(e) => {
          e.preventDefault();
          if (!busy) {
            const resolved = taskName(name, message);
            if (name === current || resolved === current) onClose();
            else onSave(resolved);
          }
        }}
      >
        <h5>重命名任务</h5>
        <input
          ref={input}
          className="field"
          aria-label="任务名"
          placeholder="留空就用消息的第一句"
          enterKeyHint="done"
          value={name}
          onChange={(e) => setName(clipTaskName(e.target.value))}
        />
        {error ? (
          <p className="red">没能保存：{error}</p>
        ) : (
          <p>留空保存会用消息的第一句</p>
        )}
        <div className="btns">
          <button
            type="button"
            className="btn"
            disabled={busy}
            onClick={onClose}
          >
            取消
          </button>
          <button type="submit" className="btn pri" disabled={busy}>
            保存
          </button>
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
    <Sheet onClose={onClose}>
      <div className="ns-head">
        <b>在哪运行</b>
        <button
          disabled={
            busy || !effectiveMode || effectiveMode === ApprovalMode.PLAN
          }
          onClick={() =>
            effectiveMode === ApprovalMode.FULL_ACCESS
              ? setConfirm(true)
              : save()
          }
        >
          保存
        </button>
      </div>
      <div className="grp">
        <Field
          name="项目"
          value={projectName(cfg.cwd)}
          onClick={() => setProject(true)}
        />
        <Field
          name="CLI"
          value={
            base.agents.length > 1 ? (
              <span className="mini-seg">
                {base.agents.map((agent) => (
                  <button
                    key={agent}
                    className={agent === cfg.agent ? "on" : ""}
                    onClick={() =>
                      setCfg({
                        ...base.configFor(agent),
                        machineId,
                        cwd: cfg.cwd,
                      })
                    }
                  >
                    {agentName(agent)}
                  </button>
                ))}
              </span>
            ) : (
              agentName(cfg.agent)
            )
          }
        />
        <Field
          name="模型和权限"
          value={[
            cfg.model || "默认模型",
            effectiveMode
              ? (
                  {
                    [ApprovalMode.ASK]: "询问",
                    [ApprovalMode.AUTO]: "自动",
                    [ApprovalMode.PLAN]: "计划",
                    [ApprovalMode.FULL_ACCESS]: "完全放行",
                  } as Record<number, string>
                )[effectiveMode]
              : "请选择权限",
          ].join(" · ")}
          onClick={() => setConfig(true)}
        />
      </div>
      <p className="hint">
        只修改这个任务。它仍在原来的电脑运行；已开出的对话保留原来的设置。
      </p>
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
          title="以完全放行运行？"
          text="以后每次运行都不审批。已开出的对话不变。"
          ok="完全放行"
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

const scheduleDrafts = new WeakMap<
  Remote,
  Map<string, { project: string; name: string; trigger: ScheduleTrigger; notifyFinish?: boolean; dirty?: boolean }>
>();
export function hasScheduleDrafts(remote: Remote): boolean {
  return [...(scheduleDrafts.get(remote)?.values() ?? [])].some(draft => !!draft.name || !!draft.dirty);
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
  const content = (
    <>
      <label className="schedule-prompt">
        让 agent 做什么
        <ScheduleTextarea
          aria-label="让 agent 做什么"
          placeholder="描述要定时做的事，有结果时让 agent 通知你"
          value={message}
          onChange={(e) => setText(e.target.value)}
        />
      </label>
      <label className="schedule-name">
        任务名
        <input
          aria-label="任务名"
          placeholder={message.trim() ? `留空会用：${taskName("", message)}` : "留空就用消息的第一句"}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </label>
      <div className="grp">
        <Field
          name="什么时候"
          value={timingText(trigger)}
          onClick={() => setTiming(true)}
        />
        <Field name="完成后通知" value={<Switch label="完成后通知" on={notifyFinish} onChange={setNotifyFinish} />} />
        {!session && (
          <Field
            name="项目"
            value={project ? projectName(project) : "选项目"}
            onClick={() => setChoosing(true)}
          />
        )}
      </div>
    </>
  );
  return (
    <div className="page schedules-page">
      <NavBar title={session ? "定时发到这个对话" : "新任务"} onBack={onBack} />
      <ConnBar remote={remote} />
      <div className="body schedule-body">
        {content}
        {new TextEncoder().encode(message).length > scheduleMessageLimit && <p className="hint red">太长了，最多 32 KB</p>}
        <ScheduleCreateConfig
          key={session?.id ?? project}
          remote={remote}
          project={project}
          name={name}
          message={message}
          trigger={trigger}
          notifyFinish={notifyFinish}
          session={session}
          onDone={(machineId, task) => {
            remote.drafts.delete(draftKey);
            drafts.delete(draftKey);
            if (location.hash === creationRoute)
              go(session ? "s/" + session.id : taskRoute(machineId, task.id));
          }}
        />
      </div>
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
    </div>
  );
}
function ScheduleCreateConfig({
  remote,
  project,
  name,
  message,
  trigger,
  notifyFinish,
  session,
  onDone,
}: {
  remote: Remote;
  project: string;
  name: string;
  message: string;
  trigger: ScheduleTrigger;
  notifyFinish: boolean;
  session?: SessionView;
  onDone: (machineId: string, t: ScheduleTask) => void;
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
  const createTask = async () => {
    if (!validScheduleMessage(message) || busy) return;
    const mode = ns.cfg.mode || defaultModeOf(ns.machine, ns.cfg.agent).mode;
    if (!session && (!mode || mode === ApprovalMode.PLAN)) {
      setError("请选择询问、自动或完全放行，定时任务不能以计划模式新开对话。");
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
    (!session && (!project || ns.reading || !!ns.issue || !(ns.cfg.mode || defaultModeOf(ns.machine, ns.cfg.agent).mode) || (ns.cfg.mode || defaultModeOf(ns.machine, ns.cfg.agent).mode) === ApprovalMode.PLAN));
  return (
    <>
      {!session && (
        <div className="grp">
          <Field
            name="CLI"
            value={
              ns.agents.length > 1 ? (
                <span className="mini-seg">
                  {ns.agents.map((a) => (
                    <button
                      key={a}
                      className={ns.cfg.agent === a ? "on" : ""}
                      disabled={!!pending?.frozen}
                      onClick={() => ns.setAgent(a)}
                    >
                      {agentName(a)}
                    </button>
                  ))}
                </span>
              ) : (
                agentName(ns.cfg.agent)
              )
            }
          />
          <Field
            name="权限"
            value={
              <span
                className={
                  (ns.cfg.mode ||
                    defaultModeOf(ns.machine, ns.cfg.agent).mode) ===
                  ApprovalMode.FULL_ACCESS
                    ? "red"
                    : ""
                }
              >
                {ns.modeText}
              </span>
            }
            disabled={!!pending?.frozen}
            onClick={() => setConfig(true)}
          />
          <Field
            name={remote.machines.size > 1 ? "机器和模型" : "模型"}
            value={
              remote.machines.size > 1
                ? [ns.parts.machine, ns.parts.model].join(" · ")
                : ns.parts.model
            }
            disabled={!!pending?.frozen}
            onClick={() => setConfig(true)}
          />
        </div>
      )}
      {!supported && (
        <Banner>需要更新这台电脑上的 remote-agent 才能使用定时任务。</Banner>
      )}
      {!online && <Banner>电脑不在线，连上后才能创建任务。</Banner>}
      {error && <Banner kind="err">{error}</Banner>}
      {pending?.frozen && (
        <Banner
          kind="warn"
          action={pending.expired ? "已核实，放弃请求" : undefined}
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
          上次创建结果还没有确认。重试会使用原来的任务名、消息、时间和请求键，不会重复创建。
        </Banner>
      )}
      {!session && ns.issue && <Banner kind="warn">{ns.issue}</Banner>}
      {!session && (ns.cfg.mode || defaultModeOf(ns.machine, ns.cfg.agent).mode) === ApprovalMode.PLAN && <Banner kind="warn">计划模式不能创建执行任务，请先选择其他权限</Banner>}
      {!session && !(ns.cfg.mode || defaultModeOf(ns.machine, ns.cfg.agent).mode) && <p className="manage-hint">请选择明确的权限，再创建定时任务。</p>}
      <p className="manage-hint">
        {session
          ? "消息会发到当前对话，等待正在运行的一轮结束。"
          : "每次运行新开一个对话，归在这个任务下面，不进会话列表。"}
        完成后默认通知；关闭后仅由 agent 主动通知。
      </p>
      <div className="pad">
        <button
          className="btn pri wide"
          disabled={disabled || pending?.expired}
          onClick={() => {
            if (
              !session &&
              (ns.cfg.mode || defaultModeOf(ns.machine, ns.cfg.agent).mode) ===
                ApprovalMode.FULL_ACCESS &&
              !pending?.frozen
            )
              setFull(true);
            else void createTask();
          }}
        >
          {busy ? "正在创建…" : pending?.frozen ? "重试创建" : "创建"}
        </button>
      </div>
      {config && (
        <ConfigSheet ns={ns} scheduled onClose={() => setConfig(false)} />
      )}
      {full && (
        <Alert
          title="以完全放行运行？"
          text="任务每次运行时，所有操作都不审批。"
          ok="完全放行"
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
        className="schedule-strip"
        onClick={() =>
          tasks.length === 1
            ? go(taskRoute(nearest.machineId, nearest.task.id))
            : setChoosing(true)
        }
      >
        <Clock />
        <span>
          {tasks.length > 1
            ? `${tasks.length} 个定时 · `
            : `定时 · ${frequency(nearest.task.spec.trigger)}，`}
          {remote.machines.get(session.machineId)?.online &&
          remote.conn === "online"
            ? nextText(nearest.task, now)
            : scheduleOffline}
        </span>
        <span className="blue">
          管理 <ChevronRight />
        </span>
      </button>
      {choosing && (
        <Sheet onClose={() => setChoosing(false)}>
          <h4>这个对话的定时任务</h4>
          {tasks.map((e) => (
            <ScheduleRow key={e.task.id} remote={remote} go={go} {...e} />
          ))}
        </Sheet>
      )}
    </>
  );
}
