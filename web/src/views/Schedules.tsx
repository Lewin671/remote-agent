import { useEffect, useRef, useState, type ReactNode } from "react";
import { Agent, ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import { Clock, Plus, ChevronRight, ChevronDown, ChevronUp } from "../icons";
import {
  agentName,
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
  groupRuns,
  groupTasks,
  lastResult,
  localInput,
  nextText,
  oneShot,
  reasonText,
  runResult,
  taskGroup,
  taskRoute,
  taskTone,
  zonedISO,
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
import { SessionLine } from "./SessionLine";

function TaskOffline({ remote, id }: { remote: Remote; id?: string }) {
  const machines = [...remote.machines.values()].filter(
    (m) => (!id || m.id === id) && remote.supportsSchedules(m.id),
  );
  const offline = machines.filter((m) => !m.online);
  return (
    <>
      {remote.conn === "online" && offline.length > 0 && (
        <Banner>
          {scheduleOffline}
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
}: {
  remote: Remote;
  machineId: string;
  task: ScheduleTask;
  go: Go;
  now?: number;
}) {
  const m = remote.machines.get(machineId);
  const multiple = remote.machines.size > 1;
  const target = task.spec.target;
  const location =
    target.type === "resume_session"
      ? remote.sessions.get(target.session ?? "")?.info ? `在「${titleOf(remote.sessions.get(target.session!)!)}」里` : "在对话里"
      : projectName(target.cwd ?? "");
  const status =
    remote.conn !== "online" || !m?.online ? scheduleOfflineRow : nextText(task, now);
  return (
    <SessionLine
      tone={taskTone(task, remote.conn === "online" && !!m?.online)}
      title={task.spec.title}
      right={status}
      preview={[
        timingText(task.spec.trigger),
        location,
        target.agent === "claude"
          ? "Claude Code"
          : target.type === "new_session"
            ? "Codex"
            : "",
        multiple ? machineName(m) : "",
      ]
        .filter(Boolean)
        .join(" · ")}
      meta={lastResult(task, now)}
      onClick={() => go(taskRoute(machineId, task.id))}
    />
  );
}
export function SchedulesPage({ remote, go, sidebar = false, selected }: { remote: Remote; go: Go; sidebar?: boolean; selected?: string }) {
  useRemote(remote);
  useEffect(() => {
    remote.setSchedulesOpen(true);
    return () => remote.setSchedulesOpen(false);
  }, [remote]);
  const [endedOpen, setEndedOpen] = useState(() => localStorage.getItem("schedule-ended-open") === "true");
  const now = useNow(60_000);
  const entries = remote.taskEntries();
  const groups = groupTasks(entries);
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
                </button> : <div className="glabel">{group}<span>{tasks.length}</span></div>}
                {(group !== "已结束" || endedOpen) && tasks.map((e) => (
                  <div className={selected === taskRoute(e.machineId, e.task.id) ? "schedule-selected" : ""} key={e.machineId + e.task.id}><ScheduleRow
                    key={e.machineId + e.task.id}
                    remote={remote}
                    go={go}
                    now={now}
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
  const [edit, setEdit] = useState<"" | "name" | "time" | "message" | "target">(
    "",
  );
  const [deleting, setDeleting] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [expanded, setExpanded] = useState(new Set<string>());
  const [allHistory, setAllHistory] = useState(false);
  const [editBase, setEditBase] = useState<ScheduleTask>();
  const m = remote.machines.get(machineId);
  const online = !!m?.online && remote.conn === "online";
  const perform = async (operation: string, spec?: ScheduleSpec) => {
    if (!task || busy) return;
    const navigate = fromHere(go);
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await remote.changeSchedule(machineId, id, {
        operation,
        taskId: id,
        revision: (spec ? editBase : task)?.revision ?? task.revision,
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
  const paused = ["paused", "failed", "uncertain"].includes(task.status);
  const ended = taskGroup(task) === "已结束";
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
      <span>
        {dateTime(r.started_at, now)} · {runResult(r)}
        {r.manual ? " · 手动" : ""}
        <small>{runSummary(r)}</small>
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
  const history = allHistory ? runs.map(r => ({ key: r.id, quiet: false, runs: [r] })) : groupRuns(task).slice(0, 5);
  return (
    <div className="page gray schedules-page">
      <NavBar
        title={task.spec.title}
        sub={online ? nextText(task, now) : scheduleOffline}
        onBack={onBack}
      />
      <ConnBar remote={remote} />
      <div className="body schedule-body">
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
          <Banner kind="warn">
            {missingTarget ? <>
              {missingTargetRun && `${dateTime(missingTargetRun.started_at, now)} 的运行失败，`}任务已停下：{reasonText(task.reason)}。这个任务只发到原来的对话，恢复后还会失败。
            </> : <>
              {reasonText(task.reason)}。
              {task.status === "uncertain"
                ? "继续可能重复执行上次的消息，请先打开运行记录核实。"
                : failedOnce
                  ? "可立即额外执行一次，或改时间重排。"
                  : "恢复或重新安排后从下一次自动时间开始，不重发上次消息。"}
              {task.reason === "agent_schedule_permissions_changed" &&
                "继续会由你接管任务，按任务中显示的权限运行。"}
            </>}
          </Banner>
        )}
        {attention && !missingTarget && <div className="pad schedule-recovery"><button className="btn pri wide" disabled={!online || busy || !!pending?.frozen || !!task.active_run || !!task.manual_request} onClick={recover}>{failedOnce ? "立即额外执行" : "恢复"}</button></div>}
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
        <section className="schedule-description">
          <div className="schedule-description-head"><span>让 agent 做什么</span><button disabled={!online || busy || !!task.active_run || !!pending?.frozen} onClick={() => { setEditBase(task); setEdit("message"); }}>编辑{online && !busy && !task.active_run && !pending?.frozen && <ChevronRight />}</button></div>
          <ScheduleText key={task.spec.message} text={task.spec.message} />
        </section>
        <div className="glabel">
          运行记录<span>{allRuns(task).length}</span>
        </div>
        <div className="grp">
          {!allRuns(task).length && <p className="hint">还没有运行</p>}
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
                    {dayText(g.runs[0].started_at, now)} · {g.runs.length}{" "}
                    次，都没有通知
                    <small>
                      最近一次 {clockTime(g.runs[0].started_at)}：
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
          {!allHistory && runs.length > 5 && <button className="row act schedule-more" onClick={() => setAllHistory(true)}>查看全部 {runs.length} 次</button>}
        </div>
        <p className="manage-hint">
          “通知了你”表示已请求通知，不保证送达。记录保留最近 100
          次，已开的对话仍可查看。
        </p>
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
          {scheduleLimits(task.spec.trigger, now) && <Field name="结束" value={scheduleLimits(task.spec.trigger, now)} />}
          <Field
            name={target.type === "resume_session" ? "发到" : "在哪运行"}
            value={
              target.type === "resume_session"
                ? (targetSession?.info ? titleOf(targetSession) : "对话")
                : [
                    projectName(target.cwd ?? ""),
                    target.agent === "claude" ? "Claude Code" : "Codex",
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
        <div className="grp schedule-actions">
          {!attention && !ended && (
            <button
              className="row act"
              disabled={
                !online || busy || !!task.active_run || !!task.manual_request || !!pending?.frozen
              }
              onClick={() => void perform("run")}
            >
              立即运行一次
            </button>
          )}
          {attention && missingTarget && <button className="row act" disabled={!online || busy || !!pending?.frozen} onClick={recover}>恢复</button>}
          {!ended && !attention && (
            <button
              className="row act"
              disabled={!online || busy || !!pending?.frozen}
              onClick={() => void perform(paused ? "resume" : "pause")}
            >
              {paused ? "恢复" : "暂停"}
            </button>
          )}
          <button
            className="row red"
            disabled={!online || busy || !!pending?.frozen}
            onClick={() => setDeleting(true)}
          >
            删除任务
          </button>
        </div>
        {!online && <p className="manage-hint">{machineName(m)} 连上后才能修改、运行或删除。</p>}
      </div>
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
      {deleting && (
        <Alert
          title="删除这个任务？"
          text="停止今后的运行，已发送的消息和已开出的对话保留。"
          ok="删除"
          danger
          onCancel={() => setDeleting(false)}
          onOk={() => {
            setDeleting(false);
            void perform("cancel");
          }}
        />
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
    agent: target.agent === "claude" ? Agent.CLAUDE : Agent.CODEX,
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
      agent: cfg.agent === Agent.CLAUDE ? "claude" : "codex",
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
  Map<string, { project: string; name: string; trigger: ScheduleTrigger }>
>();
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
      { project: string; name: string; trigger: ScheduleTrigger }
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
    pickProject(key);
  };
  const [choosing, setChoosing] = useState(false);
  // A request and its stable creation key survive route changes in Remote.
  const [message, setMessage] = useState(remote.drafts.get(draftKey) ?? "");
  const [trigger, pickTrigger] = useState<ScheduleTrigger>(draft.trigger);
  const setTrigger = (t: ScheduleTrigger) => {
    draft.trigger = t;
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
  session,
  onDone,
}: {
  remote: Remote;
  project: string;
  name: string;
  message: string;
  trigger: ScheduleTrigger;
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
      trigger,
      target: session
        ? { type: "resume_session", session: session.id }
        : {
            type: "new_session",
            cwd: project,
            agent: ns.cfg.agent === Agent.CLAUDE ? "claude" : "codex",
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
        Agent 觉得值得说的时候才通知你。
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
