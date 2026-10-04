// Starting sessions. There is one way: type in a project's new-session
// box and send. Project settings persist in the sealed account prefs.

import { useEffect, useRef, useState, type ReactNode } from "react";
import { Agent, ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import type { DaemonSettings, ListDirectoriesResponse, ListModelsResponse } from "../gen/remoteagent/v1/local_pb";
import {
  agentName,
  ago,
  findModel,
  STANDARD_WINDOW,
  modelLabel,
  inheritConfig,
  machineAgents,
  newConfigIssue,
  switchMachine,
  machineName,
  modeName,
  defaultModeOf,
  modeLabel,
  projectName,
  projects,
  shortPath,
  type MachineLike,
  type NewConfig,
  type Project,
} from "../model";
import { Attempt } from "../attempt";
import type { CreateSessionRequest } from "../gen/remoteagent/v1/local_pb";
import { errorText, type MachineView, type Remote, type SessionView } from "../remote";

type CreateReq = Omit<CreateSessionRequest, "$typeName" | "args" | "env">;
type StartPayload = { machineId: string; req: CreateReq };
import { Avatar, Banner, Dot, Sheet } from "../ui";
import { groupProjects, isHidden } from "../prefs";
import { useAutoGrow } from "./Composer";
import { AttachButton, attachBlocked, maxImages, TrayView, useDropPaste, useTray } from "./Attach";
import { ArrowUp, Check, ChevronLeft, ChevronRight, Plus } from "../icons";

export type Go = (route: string) => void;

/** The machine's defaults for new sessions of a CLI. */
function defaultsOf(st: DaemonSettings | undefined, agent: Agent): { model: string; effort: string; mode: ApprovalMode } {
  if (!st) return { model: "", effort: "", mode: ApprovalMode.UNSPECIFIED };
  if (agent === Agent.CLAUDE) return { model: st.claude?.model ?? "", effort: st.claude?.effort ?? "", mode: st.claude?.mode ?? ApprovalMode.UNSPECIFIED };
  return { model: st.defaultModel, effort: st.defaultEffort, mode: st.defaultMode };
}

/** A machine's model list and settings, for showing what defaults mean.
 * What was read before shows at once and is read again behind it, so
 * coming back to a machine or a CLI does not blink through 读取设置…. */
export function useMachineInfo(remote: Remote, machineId: string, agent: Agent) {
  const key = machineId + "/" + agent;
  const [got, setGot] = useState<{ key: string; models?: ListModelsResponse; settings?: DaemonSettings; error: string; done: boolean }>();
  const [attempt, setAttempt] = useState(0);
  const online = remote.machines.get(machineId)?.online ?? false;
  useEffect(() => {
    let alive = true;
    setGot((g) => (g?.key === key ? { ...g, error: "", done: false } : g));
    if (!machineId || !online) return;
    Promise.allSettled([remote.listModels(machineId, agent, true), remote.settings(machineId)]).then(([m, s]) => {
      if (!alive) return;
      setGot({
        key,
        models: m.status === "fulfilled" ? m.value : undefined,
        settings: s.status === "fulfilled" ? s.value : undefined,
        error: m.status === "fulfilled" ? "" : errorText(m.reason),
        done: true,
      });
    });
    return () => {
      alive = false;
    };
  }, [remote, machineId, agent, online, attempt]);
  // Another machine's or CLI's answer is never shown for this one.
  const mine = got?.key === key ? got : undefined;
  const models = mine?.models ?? remote.cachedModels(machineId, agent);
  const settings = mine?.settings ?? remote.cachedSettings(machineId);
  const loading = !!machineId && online && !mine?.done && !models;
  return { models, settings, error: mine?.error ?? "", loading, retry: () => setAttempt((a) => a + 1) };
}

/** What the configuration says, part by part: the machine, the CLI, the
 * model and effort, and the mode. */
export function describeParts(cfg: NewConfig, m: MachineLike | undefined, info: ReturnType<typeof useMachineInfo>) {
  const def = defaultsOf(info.settings, cfg.agent);
  const slug = cfg.model || def.model || info.models?.defaultModel || "";
  const model = findModel(info.models?.models, slug);
  const effort = cfg.effort || (cfg.model ? model?.defaultEffort : def.effort || model?.defaultEffort) || "";
  const unset = info.loading ? "读取设置…" : "默认模型";
  const label = slug ? modelLabel(info.models?.models, slug, cfg.agent) : unset;
  const modelText = slug ? `${label}${effort ? " · " + effort : ""}` : unset;
  // Claude Code only: the window new sessions get, 1M unless the project
  // asks for the standard one or the model has no larger one.
  const max = model?.contextWindow ?? 0n;
  const fixedWindow = max > 0n && max <= STANDARD_WINDOW;
  const window = cfg.agent !== Agent.CLAUDE ? 0n : cfg.standardContext || fixedWindow ? STANDARD_WINDOW : max || 1_000_000n;
  return { machine: machineName(m), agent: agentName(cfg.agent), model: modelText, label, slug, window, fixedWindow, mode: modeLabel(cfg.mode, defaultModeOf(m, cfg.agent), cfg.agent) };
}

/** The configuration in one line: machine, CLI, model and effort, mode. */
export function describe(cfg: NewConfig, m: MachineLike | undefined, info: ReturnType<typeof useMachineInfo>): string {
  const p = describeParts(cfg, m, info);
  return [p.machine, p.agent, p.model, p.mode].join(" · ");
}

function freshConfig(remote: Remote, key: string, machineId?: string): NewConfig {
  const online = [...remote.machines.values()].filter((m) => m.online);
  const id = machineId ?? online[0]?.id ?? [...remote.machines.keys()][0] ?? "";
  return { machineId: id, cwd: key, agent: machineAgents(remote.machines.get(id))[0], model: "", effort: "", mode: ApprovalMode.UNSPECIFIED };
}

/** The CLIs a new session can use on a machine, Codex first. */
function agentsOf(m: MachineLike | undefined): Agent[] {
  const have = machineAgents(m);
  return [Agent.CODEX, Agent.CLAUDE].filter((a) => have.includes(a));
}

type AgentSet = { agent: Agent; model: string; effort: string; mode: ApprovalMode; standardContext: boolean };

function setOf(c: NewConfig): AgentSet {
  return { agent: c.agent, model: c.model, effort: c.effort, mode: c.mode, standardContext: c.agent === Agent.CLAUDE && !!c.standardContext };
}

/** The mode a start with cfg will run in: the one chosen, else the CLI's
 * effective default the machine reports. */
function modeTextOf(cfg: NewConfig, m: MachineLike | undefined): string {
  const dm = defaultModeOf(m, cfg.agent);
  return cfg.mode ? modeName(cfg.mode) : dm.known && dm.mode ? modeName(dm.mode) : "审批：CLI 配置（未读到）";
}

/** What the user chose on this page and the stored settings do not say
 * (yet): another machine, edited sets. once: the machine is for this
 * start only and nothing of it is saved. */
type Local = { key: string; machineId?: string; once: boolean; sets: Partial<Record<Agent, AgentSet>> };

/** The configuration and the create call of a new session in project key.
 * The project's settings name the machine and keep one set of model,
 * effort and mode per CLI; which CLI a start uses is picked next to the
 * box, the project remembering the last one used. */
export function useNewSession(remote: Remote, key: string, go: Go) {
  // Recomputed on every render: which session is latest changes with any
  // session's activity, not only when sessions come or go.
  const p = projects(remote.sessions.values()).find((x) => x.key === key);
  const saved = remote.projectPrefs().settings.find((s) => s.path === key);
  const [localState, setLocal] = useState<Local>();
  const local = localState?.key === key ? localState : undefined;
  // The CLI picked next to the box, for this draft.
  const [pickedState, setPicked] = useState<{ key: string; agent: Agent }>();
  const picked = pickedState?.key === key ? pickedState.agent : undefined;
  // Once a draft is begun its launch settings hold: later activity (a
  // newer session in the project, a machine coming or going) does not
  // change where or how the draft will start. Only the user's own choice
  // does.
  const [latched, setLatched] = useState<{ key: string; machineId: string; agent: Agent; by: Partial<Record<Agent, NewConfig>> }>();
  const held = latched?.key === key ? latched : undefined;
  const inherit = (a?: Agent) => inheritConfig(p, remote.machines, key, a);
  const first = inherit();
  // Where it starts without a choice made on this page.
  const baseMachine = saved?.machineId || held?.machineId || first?.machineId || freshConfig(remote, key, remote.draftMachines.get(key)).machineId;
  const machineId = local?.machineId ?? baseMachine;
  const machine = remote.machines.get(machineId);
  const agents = agentsOf(machine);
  const remembered = saved?.agent ? (saved.agent === Agent.CLAUDE ? Agent.CLAUDE : Agent.CODEX) : undefined;
  const wanted = picked ?? remembered ?? held?.agent ?? first?.agent ?? agents[0];
  // A CLI the machine lacks is not started: the one it has is, and the
  // box says so. What the project remembers stays.
  const agent = agents.includes(wanted) ? wanted : agents[0];
  const missing = agent !== wanted && machine?.meta ? wanted : undefined;
  /** What a session of CLI a starts with: the set chosen on this page,
   * the stored one, else the project's latest session on that CLI. base:
   * as the project has it, leaving out this page's choices. */
  const configFor = (a: Agent, base = false): NewConfig => {
    const at = { machineId: base ? baseMachine : machineId, cwd: key, agent: a };
    const set = (!base && local?.sets[a]) || saved?.agents.find((x) => x.agent === a);
    if (set) return { ...at, model: set.model, effort: set.effort, mode: set.mode, standardContext: set.standardContext };
    // Another machine has its own models and defaults.
    if (!base && local?.machineId) return { ...at, model: "", effort: "", mode: ApprovalMode.UNSPECIFIED };
    const h = held ? held.by[a] : inherit(a);
    return { ...at, model: h?.model ?? "", effort: h?.effort ?? "", mode: h?.mode ?? ApprovalMode.UNSPECIFIED, from: h?.from };
  };
  const cfg = configFor(agent);
  const info = useMachineInfo(remote, cfg.machineId, cfg.agent);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [configError, setConfigError] = useState("");
  const save = (settings: { machineId?: string; agent?: Agent; sets?: AgentSet[] }) =>
    remote.changePrefs("settings", key, { id: crypto.randomUUID(), kind: "settings" as const, path: key, at: Date.now(), settings }).then((ok) => setConfigError(ok ? "" : "项目设置没有保存，请重试"));
  // Settings not stored yet begin with where and on which CLI it starts now.
  const whole = () => (saved ? {} : { machineId: baseMachine, agent });
  /** Changes one CLI's set, or the machine: config is configFor of a CLI
   * with the change. Another machine clears every CLI's set, its models
   * and defaults being its own. once: only for this start, nothing saved. */
  const setCfg = (config: NewConfig, once = false) => {
    if (config.machineId !== machineId) {
      const sets = { [Agent.CODEX]: setOf({ ...config, agent: Agent.CODEX }), [Agent.CLAUDE]: setOf({ ...config, agent: Agent.CLAUDE }) };
      setLocal({ key, machineId: config.machineId, once, sets });
      if (!once) void save({ ...whole(), machineId: config.machineId, sets: Object.values(sets) });
      return;
    }
    const set = setOf(config);
    setLocal({ key, machineId: local?.machineId, once: !!local?.once, sets: { ...local?.sets, [config.agent]: set } });
    if (!local?.once) void save({ ...whole(), sets: [set] });
  };
  const setAgent = (a: Agent) => {
    latch();
    setPicked({ key, agent: a });
  };
  // One pending attempt per project: the wide home reuses this hook when
  // switching projects, and an unknown start in one project must never be
  // what another project's box sends.
  const attempts = useRef(new Map<string, Attempt<StartPayload>>());
  let attempt = attempts.current.get(key);
  if (!attempt) attempts.current.set(key, (attempt = new Attempt()));
  const current = attempt;
  // A failure shown for one project is not the next one's.
  useEffect(() => { setError(""); setConfigError(""); }, [key]);

  // What a start would send. While one's outcome is unknown, only what the
  // user changed counts: the message as sent (trimmed), and the settings
  // only if they chose others; the page's own view of the settings may
  // change as the started session's record arrives.
  const chose = !!saved || !!local || picked !== undefined;
  const payloadFor = (prompt: string, mode?: ApprovalMode): StartPayload => {
    const pending = current.payload;
    const c = pending && !chose ? { machineId: pending.machineId, ...pending.req } : { machineId: cfg.machineId, cwd: cfg.cwd, model: cfg.model, effort: cfg.effort, mode: mode ?? cfg.mode, agent: cfg.agent, standardContext: cfg.agent === Agent.CLAUDE && !!cfg.standardContext };
    // The images are part of what is started: other images, another start.
    return {
      machineId: c.machineId,
      req: {
        cwd: c.cwd,
        prompt: prompt.trim(),
        model: c.model,
        effort: c.effort,
        mode: c.mode,
        agent: c.agent,
        standardContext: c.standardContext,
        imageIds: tray.imageIds(),
      },
    };
  };
  /** Whether sending prompt now would be a different start from the one
   * whose outcome is unknown. */
  const changes = (prompt: string, mode?: ApprovalMode): boolean => {
    const pending = current.payload;
    return !!pending && JSON.stringify(payloadFor(prompt, mode)) !== JSON.stringify(pending);
  };
  // Images for the first message go to the chosen machine; a new machine
  // gets them again (the tray keeps them prepared).
  const tray = useTray(remote, "new:" + key, cfg.machineId, maxImages(machine));
  const create = async (prompt: string, mode?: ApprovalMode, terminal = false) => {
    // A different start gives up the unknown one; the same start, however
    // it was typed, is retried as the same request.
    if (changes(prompt, mode)) abandon();
    // While an earlier attempt's outcome is unknown, only it is retried.
    const { payload, slot } = current.next(payloadFor(prompt, mode));
    setBusy(true);
    setError("");
    try {
      const res = await remote.machineCommand(payload.machineId, { case: "createSession", value: payload.req }, slot);
      current.settle();
      if (res.kind.case !== "createSession" || !res.kind.value.session) throw new Error("意外的回复");
      const sid = res.kind.value.session.id;
      if (payload.req.prompt) remote.prompts.set(sid, payload.req.prompt);
      remote.created.add(sid);
      if (!terminal) tray.clear();
      if (terminal) remote.termFirst.add(sid);
      remote.draftMachines.delete(key);
      // The first session establishes the project's settings, and each
      // CLI's first one its set; later starts only move the remembered
      // CLI. A machine picked for one start is not what gets stored.
      const used = payload.req.agent;
      const asStored = configFor(used, true);
      if (!saved) void save({ machineId: asStored.machineId, agent: used, sets: [setOf(local?.once ? asStored : cfg)] });
      else {
        const patch: { agent?: Agent; sets?: AgentSet[] } = {};
        if (remembered !== used) patch.agent = used;
        if (!saved.agents.some((x) => x.agent === used)) patch.sets = [setOf(local?.once ? asStored : cfg)];
        if (patch.agent !== undefined || patch.sets) void save(patch);
      }
      setLocal(undefined);
      setPicked(undefined);
      setLatched(undefined);
      go("s/" + sid);
      return true;
    } catch (e) {
      current.settle(e);
      setError(errorText(e));
      return false;
    } finally {
      setBusy(false);
    }
  };
  // A project removed from the list (here or on another client) keeps its
  // draft, but a new session waits until it is restored.
  const blocked = isHidden(remote.projectPrefs(), key) ? "这个项目已从列表移除，先恢复" : !machine?.online ? `${machineName(machine)} 不在线` : remote.conn !== "online" ? "和服务器断线了" : "";
  // Giving up an unknown start: the user checked, or changed what to send.
  const abandon = () => {
    current.abandon();
    setError("");
  };
  /** Holds the launch settings as they are now, once a draft begins. */
  const latch = () => {
    if (!saved && !held) setLatched({ key, machineId: baseMachine, agent: first?.agent ?? agents[0], by: { [Agent.CODEX]: inherit(Agent.CODEX), [Agent.CLAUDE]: inherit(Agent.CLAUDE) } });
  };
  // Full access, or a default nobody knows, is confirmed first.
  const dm = defaultModeOf(machine, cfg.agent);
  const modeText = modeTextOf(cfg, machine);
  const parts = describeParts(cfg, machine, info);
  // The machine is named only where there is more than one to mean.
  const summary: Summary = { agent: agentName(cfg.agent), mode: modeText, machine: remote.machines.size > 1 ? machineName(machine) : "", model: parts.label };
  return {
    remote,
    frozen: current.frozen,
    expired: current.expired,
    abandon,
    changes,
    p,
    cfg,
    setCfg,
    configFor,
    agents,
    setAgent,
    missing,
    latch,
    issue: newConfigIssue(cfg.agent, parts.slug, cfg.mode || dm.mode),
    parts,
    tray,
    attachWhy: attachBlocked(remote, machine),
    machine,
    info,
    summary,
    modeText,
    create,
    busy,
    error,
    configError,
    blocked,
  };
}

export type NewSession = ReturnType<typeof useNewSession>;

/** The first message starts a session. Next to the box is the CLI it
 * starts on, where the machine has more than one; the project summary
 * opens its settings; the composer's ＋ only adds images. */
export function NewBox({
  ns,
  name,
  autoFocus,
  variant = "box",
  lead,
  onDraft,
  onFocus,
}: {
  ns: NewSession;
  name: string;
  autoFocus?: boolean;
  variant?: "box" | "prompt" | "start";
  lead?: ReactNode;
  onDraft?: () => void;
  // Whether the user is typing in it: a phone makes room for the keyboard.
  onFocus?: (typing: boolean) => void;
}) {
  const [text, setText] = useState("");
  const [config, setConfig] = useState<"" | "main" | Pick>("");
  const input = useRef<HTMLTextAreaElement>(null);
  useAutoGrow(input, text, 132);
  useEffect(() => {
    if (autoFocus) input.current?.focus();
  }, [autoFocus]);
  const cannot = ns.busy || !!ns.blocked || !!ns.issue;
  const submit = async () => {
    const prompt = text;
    // An unknown start that expired is not sent again, nor replaced by the
    // same start under a new request. Chosen images must all be on the
    // machine first.
    if (cannot || (ns.expired && !ns.changes(prompt))) return;
    if (ns.tray.items.length && !ns.tray.ready()) return;
    // Retrying an unknown start sends it as it was.
    const ok = await ns.create(prompt.trim());
    if (ok) setText("");
  };
  const status = ns.busy ? `正在 ${machineName(ns.machine)} 上启动…` : "";
  const placeholder = {
    box: "描述任务…",
    prompt: "描述任务…",
    start: "描述任务，⌘/Ctrl+Enter 开始",
  }[variant];
  const tray = ns.tray;
  tray.onAdd = () => {
    ns.latch();
    onDraft?.();
  };
  const images = tray.items.length;
  const disabled = cannot || (ns.expired && !ns.changes(text)) || (images ? !tray.ready() : !text.trim());
  const [toast, setToast] = useState("");
  const drop = useDropPaste(tray, ns.attachWhy, setToast);
  // The machine changed under chosen images: they are uploaded again.
  const lastMachine = useRef(ns.cfg.machineId);
  const [moved, setMoved] = useState("");
  useEffect(() => {
    if (lastMachine.current !== ns.cfg.machineId && images) setMoved(machineName(ns.machine));
    lastMachine.current = ns.cfg.machineId;
  }, [ns.cfg.machineId]); // eslint-disable-line react-hooks/exhaustive-deps
  const trayView = (
    <TrayView
      tray={tray}
      note={
        <>
          {moved && tray.busy() && <Banner>已换到 {moved}，图片正在重新上传</Banner>}
          {toast && (
            <Banner action="知道了" onAction={() => setToast("")}>
              {toast}
            </Banner>
          )}
        </>
      }
    />
  );
  const field = (
    <textarea
      ref={input}
      className={"draft" + (text ? " typed" : "")}
      rows={1}
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        // A draft begun holds its project and launch settings.
        if (!text && e.target.value) {
          ns.latch();
          onDraft?.();
        }
        setText(e.target.value);
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
          e.preventDefault();
          void submit();
        }
      }}
      onFocus={() => onFocus?.(true)}
      onBlur={() => onFocus?.(false)}
      aria-label="新会话的第一句话"
    />
  );
  const offline = !ns.machine?.online;
  const error =
    ns.error &&
    (ns.expired ? (
      <Banner kind="warn" action="放弃这次启动" onAction={ns.abandon}>
        不确定是否已经启动，请先看会话列表。这个请求已经过期，不会再发，也不会换成新的请求
      </Banner>
    ) : (
      <Banner kind={ns.frozen ? "warn" : "err"} action="重试" onAction={() => void submit()}>
        {ns.frozen ? `不确定是否已经启动：${ns.error}。重试会发送同一个请求，不会重复启动` : `启动失败：${ns.error}`}
      </Banner>
    ));
  const notes = (
    <>
      {offline && ns.machine && (
        <Banner kind="warn" action="换机器" onAction={() => setConfig("machine")}>
          {machineName(ns.machine)} {text ? "已离线，任务尚未开始。草稿和配置保留，不会自动换机器或 CLI" : "不在线"}
        </Banner>
      )}
      {ns.missing !== undefined && !offline && (
        <Banner action="换机器" onAction={() => setConfig("machine")}>
          {machineName(ns.machine)} 没有 {agentName(ns.missing)}，这次用 {agentName(ns.cfg.agent)}
        </Banner>
      )}
      {ns.issue && (
        <Banner kind="warn" action="改模式" onAction={() => setConfig("mode")}>
          {ns.issue}
        </Banner>
      )}
      {ns.configError && <Banner kind="warn" action="重试保存" onAction={() => ns.setCfg(ns.cfg)}>{ns.configError}</Banner>}

    </>
  );
  const go = (
    <button className={"go44" + (disabled ? " off" : "")} disabled={disabled} onClick={() => void submit()} aria-label="开始新会话">
      <ArrowUp />
    </button>
  );
  // One row, as in a session: ＋ adds images, the task starts a session.
  const card = (
    <div className={"compose-card new-compose" + (drop.over ? " drop" : "")} {...drop.props} role="group" aria-label={`${name} · 新会话`}>
      {trayView}
      <div className="compose-menu">
        <AttachButton tray={tray} blocked={ns.attachWhy} onBlocked={setToast} />
      </div>
      {field}
      {go}
    </div>
  );
  const busyNote = status && <div className="small muted startnote">{status}</div>;
  // The draft stays when the CLI is switched.
  const cli = ns.agents.length > 1 && (
    <div className="pick" role="group" aria-label="新会话使用的 CLI">
      {ns.agents.map((a) => (
        <button key={a} className={a === ns.cfg.agent ? "on" : ""} aria-pressed={a === ns.cfg.agent} disabled={ns.busy} onClick={() => ns.setAgent(a)}>
          {agentName(a)}
        </button>
      ))}
    </div>
  );
  let body;
  if (variant === "box")
    // A phone: the switch has its own row above the box and stays with it
    // when the keyboard is up; the summary is under the project's title.
    body = (
      <>
        {error}
        {notes}
        {cli && <div className="launch">{cli}</div>}
        {card}
        {busyNote}
      </>
    );
  else
    // Wide: the switch and the summary of the CLI picked, under the box.
    body = (
      <>
        {variant === "start" && <h3 className="wide-start-title">{lead ? <>在 {lead} 里开始一个任务</> : "开始一个任务"}</h3>}
        {error}
        {notes}
        {card}
        <div className="launch">
          {cli}
          <ConfigSummary summary={ns.summary} bare={!!cli} chip onClick={() => setConfig("main")} />
        </div>
        {busyNote}
      </>
    );
  return (
    <>
      {body}
      {config && <ConfigSheet ns={ns} start={config === "main" ? "" : (config as Pick)} onClose={() => setConfig("")} />}
    </>
  );
}

type Pick = "" | "machine" | "model" | "mode";

/** 项目设置: the machine, and each CLI's model and mode for new sessions.
 * The tabs show one CLI's set at a time, opening on the CLI picked next
 * to the box; looking at the other one picks nothing. */
export function ConfigSheet({ ns, onClose, start = "" }: { ns: NewSession; onClose: () => void; start?: Pick }) {
  const [pick, setPick] = useState<Pick>(start);
  // Opened right at one setting: done with it is done with the sheet.
  const back2 = () => (start ? onClose() : setPick(""));
  const { remote, p, machine } = ns;
  const [tab, setTab] = useState(ns.cfg.agent);
  const agent = ns.agents.includes(tab) ? tab : ns.cfg.agent;
  const cfg = ns.configFor(agent);
  // The box's CLI is already read; only the other tab asks the machine.
  const other = useMachineInfo(remote, agent === ns.cfg.agent ? "" : cfg.machineId, agent);
  const info = agent === ns.cfg.agent ? ns.info : other;
  const parts = describeParts(cfg, machine, info);
  const set = (patch: Partial<NewConfig>) => ns.setCfg({ ...cfg, ...patch });
  const def = defaultsOf(info.settings, cfg.agent);
  useEffect(() => {
    if (pick === "model") info.retry();
  }, [pick]); // Refresh the native CLI list each time the model page opens.
  // Machines this project has used; a new project can use any.
  const choices: MachineView[] = (p && machine?.online ? p.machines : [...remote.machines.keys()]).map((id) => remote.machines.get(id)).filter((m): m is MachineView => !!m);
  const back = (
    <button className="back2" onClick={back2} aria-label="返回">
      <ChevronLeft />
    </button>
  );
  let body;
  if (pick === "machine")
    body = (
      <div className="list">
        {choices.map((m) => (
          <button
            key={m.id}
            className={"mi" + (m.online ? "" : " dis")}
            disabled={!m.online}
            onClick={() => {
              ns.setCfg(switchMachine(cfg, m), !machine?.online);
              back2();
            }}
          >
            <Dot tone={m.online ? "idle" : "off"} />
            <div className="mt">
              {m.id === cfg.machineId || !m.online ? machineName(m) : <b>改用 {machineName(m)}</b>}
              {!m.online ? (
                <div className="md">{m.lastSeenMs ? ago(m.lastSeenMs) + "在线，" : ""}现在不能新建</div>
              ) : (
                m.id !== cfg.machineId && (
                  <div className="md">
                    {machineAgents(m).includes(ns.cfg.agent) ? `有 ${agentName(ns.cfg.agent)}` : `只有 ${agentsOf(m).map(agentName).join("、")}`}；模型和模式会按这台机器重新选择
                  </div>
                )
              )}
            </div>
            {m.id === cfg.machineId && <span className="ck"><Check /></span>}
          </button>
        ))}
      </div>
    );
  else if (pick === "model") {
    const chosen = findModel(info.models?.models, cfg.model || def.model || info.models?.defaultModel || "");
    body = (
      <>
        {info.error && (
          <Banner kind="warn" action="重试" onAction={info.retry}>
            没能读取模型列表，会用 {agentName(cfg.agent)} 自己的默认模型
          </Banner>
        )}
        {!info.models && !info.error && <div className="hint">读取 {machineName(machine)} 的设置…</div>}
        <div className="list">
          <button className="mi" onClick={() => set({ model: "", effort: "" })}>
            <div className="mt">
              默认模型<div className="md">用 {machineName(machine)} 的新项目默认设置</div>
            </div>
            {!cfg.model && <span className="ck"><Check /></span>}
          </button>
          {info.models?.models.map((m) => (
            <button key={m.slug} className="mi" onClick={() => set({ model: m.slug, effort: "" })}>
              <div className="mt">{m.displayName || m.slug}</div>
              {cfg.model && m === findModel(info.models?.models, cfg.model) && <span className="ck"><Check /></span>}
            </button>
          ))}
        </div>
        {chosen && chosen.efforts.length > 0 && (
          <>
            <div className="fl">推理强度</div>
            <div className="seg wrap">
              {chosen.efforts.map((e) => (
                <button key={e} className={e === (cfg.effort || (cfg.model ? chosen.defaultEffort : def.effort || chosen.defaultEffort)) ? "on" : ""} onClick={() => set({ effort: e })}>
                  {e}
                </button>
              ))}
            </div>
          </>
        )}
      </>
    );
  } else if (pick === "mode") {
    // Claude Code's haiku has no auto mode.
    const noAuto = cfg.agent === Agent.CLAUDE && /haiku/i.test(cfg.model || def.model);
    body = (
      <div className="list">
        {[
          { mode: ApprovalMode.UNSPECIFIED, name: "默认", desc: `跟随 ${agentName(cfg.agent)} 自己的配置` },
          { mode: ApprovalMode.ASK, name: "询问", desc: "需要审批时由你确认，超时拒绝" },
          noAuto
            ? { mode: ApprovalMode.AUTO, name: "自动", desc: "Haiku 没有自动模式", off: true }
            : { mode: ApprovalMode.AUTO, name: "自动", desc: `由 ${agentName(cfg.agent)} 自己判断，拿不准的在对话里问你` },
          { mode: ApprovalMode.FULL_ACCESS, name: "完全放行", desc: "所有操作都不审批", danger: true },
        ].map((m: { mode: ApprovalMode; name: string; desc: string; danger?: boolean; off?: boolean }) => (
          <button
            key={m.mode}
            className={"mi" + (m.danger ? " danger" : "")}
            disabled={m.off}
            onClick={() => {
              set({ mode: m.mode });
              back2();
            }}
          >
            <div className="mt">
              <b>{m.name}</b>
              <div className="md">{m.desc}</div>
            </div>
            {m.mode === cfg.mode && <span className="ck"><Check /></span>}
          </button>
        ))}
      </div>
    );
  }
  const titles: Record<Pick, string> = { "": "项目设置", machine: "机器", model: "模型", mode: "审批" };
  const modelLine = parts.model;
  const many = ns.agents.length > 1;
  return (
    <Sheet onClose={onClose}>
      <div className="ns-head">
        {pick && back}
        <b>{titles[pick]}</b>
        <button className="link" onClick={onClose}>
          完成
        </button>
      </div>
      {body ?? (
        <>
          <div className="ps-id"><Avatar name={projectName(cfg.cwd)} size="sm" /><div className="ps-text"><b>{projectName(cfg.cwd)}</b><small>{shortPath(cfg.cwd)}</small></div></div>
          <div className="setrows">
            <button className="sr" onClick={() => setPick("machine")}>
              <span className="lbl">机器</span>
              <span className="v">
                <Dot tone={machine?.online ? "idle" : "off"} />
                <em>{machineName(machine)}</em>
                <ChevronRight className="chev" />
              </span>
            </button>
          </div>
          {many && (
            <div className="seg cli-tabs" role="tablist" aria-label="按 CLI 的设置">
              {ns.agents.map((a) => (
                <button key={a} role="tab" aria-selected={a === agent} className={a === agent ? "on" : ""} onClick={() => setTab(a)}>
                  {agentName(a)}
                </button>
              ))}
            </div>
          )}
          <div className="setrows">
            <button className="sr" onClick={() => setPick("model")}>
              <span className="lbl">模型</span>
              <span className={"v" + (info.loading ? " muted" : "")}>
                <em>{info.loading ? `读取 ${machineName(machine)} 的设置…` : modelLine}</em>
                <ChevronRight className="chev" />
              </span>
            </button>
            {cfg.agent === Agent.CLAUDE && (
              <div className="sr">
                <span className="lbl">上下文</span>
                {parts.fixedWindow ? (
                  <span className="fixed">标准 200k · 这个模型只有标准窗口</span>
                ) : (
                  <div className="pick" role="group" aria-label="上下文窗口">
                    <button className={cfg.standardContext ? "" : "on"} onClick={() => set({ standardContext: false })}>1M</button>
                    <button className={cfg.standardContext ? "on" : ""} onClick={() => set({ standardContext: true })}>标准 200k</button>
                  </div>
                )}
              </div>
            )}
            <button className="sr" onClick={() => setPick("mode")}>
              <span className="lbl">审批</span>
              <span className="v">
                <em>{modeTextOf(cfg, machine)}</em>
                <ChevronRight className="chev" />
              </span>
            </button>
          </div>
          {info.error && (
            <Banner kind="warn" action="重试" onAction={info.retry}>
              没能读取模型列表，会用 {agentName(cfg.agent)} 自己的默认模型
            </Banner>
          )}
          <div className="hint">{many ? "两个 CLI 各存一套，新建时用哪个在输入框旁选。" : ""}只影响之后新建的会话，进行中的不变；在你的设备之间同步。</div>
          <div className="setrows">
            <button className="sr" onClick={() => void remote.changePrefs(ns.p && remote.projectPrefs().pinned.some((x) => x.path === cfg.cwd) ? "unpin" : "pin", cfg.cwd)}>{remote.projectPrefs().pinned.some((x) => x.path === cfg.cwd) ? "取消常用" : "设为常用"}</button>
            <button className="sr red" onClick={() => void remote.changePrefs("hide", cfg.cwd)}>从项目列表移除</button>
          </div>
        </>
      )}
    </Sheet>
  );
}

/** Folder browser on a machine: breadcrumb, parent, subfolders. */
export function DirBrowser({
  remote,
  machineId,
  start,
  head,
  label,
  onPick,
}: {
  remote: Remote;
  machineId: string;
  start: string;
  head?: ReactNode;
  label: (name: string) => string;
  onPick: (path: string) => void;
}) {
  const [dir, setDir] = useState<ListDirectoriesResponse>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState("");
  const open = async (path: string) => {
    setError("");
    setLoading(path);
    try {
      const r = await remote.machineCommand(machineId, { case: "listDirectories", value: { path } });
      if (r.kind.case === "listDirectories") setDir(r.kind.value);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading("");
    }
  };
  useEffect(() => {
    setDir(undefined);
    void open(start);
  }, [machineId]); // eslint-disable-line react-hooks/exhaustive-deps
  const parts = dir ? shortPath(dir.path).split("/").filter(Boolean) : [];
  const name = parts[parts.length - 1] || "/";
  return (
    <>
      {head}
      <div className="crumb">
        {dir ? (
          <>
            {parts.slice(0, -1).join(" / ")}
            {parts.length > 1 && " / "}
            <b>{name}</b>
          </>
        ) : (
          "…"
        )}
      </div>
      {error && (
        <Banner kind="err" action="重试" onAction={() => void open(loading || dir?.path || start)}>
          读不了这个目录：{error}
        </Banner>
      )}
      <div className="list dirs">
        {dir?.parent && (
          <button className="mi" disabled={!!loading} onClick={() => void open(dir.parent)}>
            <div className="mt"><ChevronLeft /> 上一级</div>
          </button>
        )}
        {dir?.dirs.map((d) => (
          <button key={d} className="mi" disabled={!!loading} onClick={() => void open(dir.path.replace(/\/$/, "") + "/" + d)}>
            <div className="mt">{d}</div>
            <ChevronRight className="chev" />
          </button>
        ))}
        {dir && dir.dirs.length === 0 && <div className="hint">没有子目录</div>}
        {!dir && !error && <div className="hint">读取中…</div>}
      </div>
      <div className="btns" style={{ marginTop: 10 }}>
        <button className="btn pri" disabled={!dir || !!loading} onClick={() => dir && onPick(dir.path)}>
          {label(name)}
        </button>
      </div>
    </>
  );
}

/** From the home page's ＋: pick a project first, or a new one. */
export function ProjectPicker({ remote, onPick, onNew, onClose, title = "新会话 · 选项目" }: { remote: Remote; onPick: (key: string) => void; onNew: () => void; onClose: () => void; title?: string }) {
  const [query, setQuery] = useState("");
  // Grouped as the lists are; removed projects are left out.
  const g = groupProjects(projects(remote.sessions.values()), remote.projectPrefs());
  const match = (p: Project<SessionView>) => (p.name + " " + p.key).toLocaleLowerCase().includes(query.toLocaleLowerCase());
  const row = (p: Project<SessionView>) => (
    <button key={p.key} className="pi" onClick={() => onPick(p.key)}>
      <Avatar name={p.name} size="sm" />
      <div>
        <b>{p.name}</b>
        <small>
          {shortPath(p.key)} ·{" "}
          {p.machines
            .map((id) => {
              const m = remote.machines.get(id);
              return machineName(m) + (m && !m.online ? "（不在线）" : "");
            })
            .join("、")}
        </small>
      </div>
      <ChevronRight className="chev" />
    </button>
  );
  return (
    <Sheet onClose={onClose}>
      <div className="ns-head">
        <b>{title}</b>
        <button className="link" onClick={onClose}>
          取消
        </button>
      </div>
      {g.pinned.length + g.others.length > 8 && <input className="field project-search" aria-label="搜索项目" placeholder="搜索项目" value={query} onChange={(e) => setQuery(e.target.value)} />}
      <div className="plist">
        {g.pinned.some(match) && <div className="fl">常用项目</div>}
        {g.pinned.filter(match).map(row)}
        {g.pinned.length > 0 && g.others.length > 0 && <div className="fl">其他项目</div>}
        {g.others.filter(match).map(row)}
        <button className="pi add" onClick={onNew}>
          <Plus /> 新项目…<small>选机器和目录</small>
        </button>
      </div>
    </Sheet>
  );
}

/** A new project: a machine, then a folder on it. */
export function NewProjectSheet({ remote, onBack, onDone, onClose }: { remote: Remote; onBack?: () => void; onDone: (machineId: string, path: string) => void; onClose: () => void }) {
  const machines = [...remote.machines.values()].sort((a, b) => Number(b.online) - Number(a.online) || machineName(a).localeCompare(machineName(b)));
  const [machineId, setMachineId] = useState(machines.find((m) => m.online)?.id ?? "");
  const m = remote.machines.get(machineId);
  return (
    <Sheet onClose={onClose}>
      <div className="ns-head">
        {onBack && (
          <button className="back2" onClick={onBack} aria-label="返回">
            <ChevronLeft />
          </button>
        )}
        <b>新项目 · 选目录</b>
        <button className="link" onClick={onClose}>
          取消
        </button>
      </div>
      <div className="recent">
        <small>机器</small>
        {machines.map((x) => (
          <button key={x.id} className={x.id === machineId ? "on" : ""} disabled={!x.online} onClick={() => setMachineId(x.id)}>
            <Dot tone={x.online ? "idle" : "off"} />
            {machineName(x)}
          </button>
        ))}
      </div>
      {!machineId ? (
        <div className="hint">没有在线的电脑。在电脑上运行 remote-agent daemon 后再来。</div>
      ) : (
        <DirBrowser key={machineId} remote={remote} machineId={machineId} start={m?.meta?.home ?? ""} label={(name) => `用 ${name} 这个目录`} onPick={(path) => onDone(machineId, path)} />
      )}
    </Sheet>
  );
}

/** The ＋ flow: pick a project, or make a new one; then its page with the
 * box focused. */
export function NewFlow({ remote, go, onClose, onPicked, start }: { remote: Remote; go: Go; onClose: () => void; onPicked?: (key: string) => void; start?: "new" }) {
  const [step, setStep] = useState<"project" | "new">(remote.sessions.size && !start ? "project" : "new");
  const open = (key: string) => {
    onClose();
    if (onPicked) return onPicked(key);
    remote.focusProject = key;
    go("p/" + encodeURIComponent(key));
  };
  if (step === "new")
    return (
      <NewProjectSheet
        remote={remote}
        onBack={remote.sessions.size && !start ? () => setStep("project") : undefined}
        onClose={onClose}
        onDone={(machineId, path) => {
          remote.draftMachines.set(path, machineId);
          open(path);
        }}
      />
    );
  return <ProjectPicker remote={remote} onPick={open} onNew={() => setStep("new")} onClose={onClose} title={onPicked ? "选项目" : undefined} />;
}

/** What the summary of the new-session settings says. */
export type Summary = { agent: string; mode: string; machine: string; model: string };

/** The new-session settings in one line, opening the project's settings:
 * the mode, the machine and the model; the effort and the window are in
 * the settings. Only 完全放行 is red. bare: next to the CLI switch, which already names
 * the CLI. The machine's name is what gives way when the line is long. */
export function ConfigSummary({ summary, bare, chip, onClick }: { summary: Summary; bare?: boolean; chip?: boolean; onClick: () => void }) {
  const { agent, mode, machine, model: rest } = summary;
  const full = mode === modeName(ApprovalMode.FULL_ACCESS);
  const sep = " · ";
  const line = [!bare && agent, mode, machine, rest].filter(Boolean).join(sep);
  return (
    <button className={"project-summary" + (chip ? " chip" : "")} onClick={onClick} aria-label={`新会话设置：${line}`}>
      <span>
        <span>
          {!bare && agent + sep}
          {full ? <span className="red">{mode}</span> : mode}
          {(machine || rest) && sep}
        </span>
        {machine && <span className="machine">{machine}</span>}
        {rest && <span>{(machine ? sep : "") + rest}</span>}
      </span>
      {chip && <ChevronRight className="chev" />}
    </button>
  );
}
