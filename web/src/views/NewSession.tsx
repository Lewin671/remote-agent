// Starting sessions. There is one way: type in a project's new-session
// box and send. Project settings persist in the sealed account prefs.

import { useEffect, useRef, useState, type ReactNode } from "react";
import { Agent, ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import type { DaemonSettings, ListDirectoriesResponse, ListModelsResponse } from "../gen/remoteagent/v1/local_pb";
import {
  agentName,
  ago,
  findModel,
  modelLabel,
  inheritConfig,
  machineAgents,
  newConfigIssue,
  switchAgent,
  switchMachine,
  machineName,
  modeName,
  defaultModeOf,
  modeLabel,
  refusedForFullAccess,
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

export type Go = (route: string) => void;

/** The machine's defaults for new sessions of a CLI. */
function defaultsOf(st: DaemonSettings | undefined, agent: Agent): { model: string; effort: string; mode: ApprovalMode } {
  if (!st) return { model: "", effort: "", mode: ApprovalMode.UNSPECIFIED };
  if (agent === Agent.CLAUDE) return { model: st.claude?.model ?? "", effort: st.claude?.effort ?? "", mode: st.claude?.mode ?? ApprovalMode.UNSPECIFIED };
  return { model: st.defaultModel, effort: st.defaultEffort, mode: st.defaultMode };
}

/** A machine's model list and settings, for showing what defaults mean. */
export function useMachineInfo(remote: Remote, machineId: string, agent: Agent) {
  const [models, setModels] = useState<ListModelsResponse>();
  const [settings, setSettings] = useState<DaemonSettings>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const online = remote.machines.get(machineId)?.online ?? false;
  useEffect(() => {
    let alive = true;
    setModels(undefined);
    setSettings(undefined);
    setError("");
    if (!machineId || !online) return;
    setLoading(true);
    Promise.allSettled([remote.listModels(machineId, agent), remote.settings(machineId)]).then(([m, s]) => {
      if (!alive) return;
      setLoading(false);
      if (m.status === "fulfilled") setModels(m.value);
      else setError(errorText(m.reason));
      if (s.status === "fulfilled") setSettings(s.value);
    });
    return () => {
      alive = false;
    };
  }, [remote, machineId, agent, online, attempt]);
  return { models, settings, error, loading, retry: () => setAttempt((a) => a + 1) };
}

/** What the configuration says, part by part: the machine, the CLI, the
 * model and effort, and the mode. */
export function describeParts(cfg: NewConfig, m: MachineLike | undefined, info: ReturnType<typeof useMachineInfo>) {
  const def = defaultsOf(info.settings, cfg.agent);
  const slug = cfg.model || def.model || info.models?.defaultModel || "";
  const model = findModel(info.models?.models, slug);
  const effort = cfg.effort || (cfg.model ? model?.defaultEffort : def.effort || model?.defaultEffort) || "";
  const modelText = slug ? `${modelLabel(info.models?.models, slug, cfg.agent)}${effort ? " · " + effort : ""}` : info.loading ? "读取设置…" : "默认模型";
  return { machine: machineName(m), agent: agentName(cfg.agent), model: modelText, slug, mode: modeLabel(cfg.mode, defaultModeOf(m, cfg.agent), cfg.agent) };
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

/** The configuration and the create call of a new session in project key. */
export function useNewSession(remote: Remote, key: string, go: Go) {
  // Recomputed on every render: which session is latest changes with any
  // session's activity, not only when sessions come or go.
  const p = projects(remote.sessions.values()).find((x) => x.key === key);
  const inherited = inheritConfig(p, remote.machines, key);
  const [selection, setSelection] = useState<{ key: string; config: NewConfig }>();
  const [oneTimeKey, setOneTimeKey] = useState("");
  const oneTime = oneTimeKey === key;
  const saved = remote.projectPrefs().settings.find((s) => s.path === key);
  const override = selection?.key === key ? selection.config : saved ? { machineId: saved.machineId, cwd: key, agent: saved.agent, model: saved.model, effort: saved.effort, mode: saved.mode } : undefined;
  // Once a draft is begun its launch settings hold: later activity (a
  // newer session in the project, a machine coming or going) does not
  // change where or how the draft will start. Only the user's own choice
  // does.
  const [latched, setLatched] = useState<{ key: string; config: NewConfig }>();
  const held = latched?.key === key ? latched.config : undefined;
  const cfg = override ? { ...override, from: inherited?.from } : (held ?? inherited ?? freshConfig(remote, key, remote.draftMachines.get(key)));
  const machine = remote.machines.get(cfg.machineId);
  const info = useMachineInfo(remote, cfg.machineId, cfg.agent);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [configError, setConfigError] = useState("");
  const setCfg = (config: NewConfig, once = false) => {
    setSelection({ key, config });
    if (once) setOneTimeKey(key);
    if (once || oneTime) return;
    const op = { id: crypto.randomUUID(), kind: "settings" as const, path: key, at: Date.now(), settings: { machineId: config.machineId, agent: config.agent, model: config.model, effort: config.effort, mode: config.mode } };
    void remote.changePrefs("settings", key, op).then((ok) => setConfigError(ok ? "" : "项目设置没有保存，请重试"));
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
  const payloadFor = (prompt: string, mode?: ApprovalMode, confirmFullAccess = false): StartPayload => {
    const pending = current.payload;
    const c = pending && !override ? { machineId: pending.machineId, ...pending.req } : { machineId: cfg.machineId, cwd: cfg.cwd, model: cfg.model, effort: cfg.effort, mode: mode ?? cfg.mode, agent: cfg.agent };
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
        imageIds: tray.imageIds(),
        // Confirmation is not a setting the user changes: a retry of the
        // pending start keeps it, override or not.
        confirmFullAccess: pending ? pending.req.confirmFullAccess : confirmFullAccess,
      },
    };
  };
  /** Whether sending prompt now would be a different start from the one
   * whose outcome is unknown. */
  const changes = (prompt: string, mode?: ApprovalMode, confirm = false): boolean => {
    const pending = current.payload;
    return !!pending && JSON.stringify(payloadFor(prompt, mode, confirm)) !== JSON.stringify(pending);
  };
  // The daemon refused the last start: the folder's default is full access.
  const [refused, setRefused] = useState(false);
  // Images for the first message go to the chosen machine; a new machine
  // gets them again (the tray keeps them prepared).
  const tray = useTray(remote, "new:" + key, cfg.machineId, maxImages(machine));
  const create = async (prompt: string, mode?: ApprovalMode, terminal = false, confirmFullAccess = false) => {
    // A different start gives up the unknown one; the same start, however
    // it was typed, is retried as the same request.
    if (changes(prompt, mode, confirmFullAccess)) abandon();
    // While an earlier attempt's outcome is unknown, only it is retried.
    const { payload, slot } = current.next(payloadFor(prompt, mode, confirmFullAccess));
    setBusy(true);
    setError("");
    setRefused(false);
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
      // The first session establishes the project's settings. Later starts
      // leave a saved choice alone, including a one-time offline fallback.
      if (!saved) {
        const c = oneTime ? inherited ?? cfg : cfg;
        void remote.changePrefs("settings", key, { id: crypto.randomUUID(), kind: "settings", path: key, at: Date.now(), settings: { machineId: c.machineId, agent: c.agent, model: c.model, effort: c.effort, mode: c.mode } });
      }
      setSelection(undefined);
      setOneTimeKey("");
      setLatched(undefined);
      go("s/" + sid);
      return true;
    } catch (e) {
      current.settle(e);
      // Refused before anything ran: ask, then send a new request.
      if (refusedForFullAccess(e)) {
        current.abandon();
        setRefused(true);
        return false;
      }
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
    if (!override && !held) setLatched({ key, config: cfg });
  };
  // The mode the start will run in: the one chosen, else the CLI's
  // effective default the machine reports. Full access, or a default
  // nobody knows, is confirmed first.
  const dm = defaultModeOf(machine, cfg.agent);
  const modeText = cfg.mode ? modeName(cfg.mode) : dm.known && dm.mode ? modeName(dm.mode) : "审批：CLI 配置（未读到）";
  return {
    remote,
    frozen: current.frozen,
    expired: current.expired,
    abandon,
    changes,
    p,
    cfg,
    setCfg,
    latch,
    refused,
    clearRefused: () => setRefused(false),
    issue: newConfigIssue(cfg.agent, describeParts(cfg, machine, info).slug, cfg.mode || dm.mode),
    parts: describeParts(cfg, machine, info),
    tray,
    attachWhy: attachBlocked(remote, machine),
    machine,
    info,
    line: [agentName(cfg.agent), modeText, machineName(machine), describeParts(cfg, machine, info).model].join(" · "),
    modeText,
    create,
    busy,
    error,
    configError,
    blocked,
  };
}

export type NewSession = ReturnType<typeof useNewSession>;

/** The first message starts a session. The project summary opens its
 * settings; the composer's ＋ only adds images. */
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
  const submit = async (force = false) => {
    const prompt = text;
    // An unknown start that expired is not sent again, nor replaced by the
    // same start under a new request. Chosen images must all be on the
    // machine first.
    if (cannot || (ns.expired && !ns.changes(prompt))) return;
    if (ns.tray.items.length && !ns.tray.ready()) return;
    // Retrying an unknown start sends it as it was.
    // Older daemons still require the compatibility flag for full access.
    const dm = defaultModeOf(ns.machine, ns.cfg.agent);
    const confirm = force || ns.cfg.mode === ApprovalMode.FULL_ACCESS || (!ns.cfg.mode && dm.mode === ApprovalMode.FULL_ACCESS);
    const ok = await ns.create(prompt.trim(), undefined, false, confirm);
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
      ↑
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
  let body;
  if (variant === "start")
    body = (
      <>
        <h3 className="wide-start-title">在 {lead} 里开始一个任务</h3>
        {error}
        {notes}
        {card}
        <button className={"project-summary" + (ns.line.includes("完全放行") ? " full" : "")} onClick={() => setConfig("main")}>{ns.line}</button>
        {busyNote}
      </>
    );
  else
    body = (
      <>
        {error}
        {notes}
        {card}
        {busyNote}
      </>
    );
  return (
    <>
      {body}
      {ns.refused && <Banner kind="warn" action="重试" onAction={() => { ns.clearRefused(); void submit(true); }}>电脑需要确认完全放行</Banner>}
      {config && <ConfigSheet ns={ns} start={config === "main" ? "" : (config as Pick)} onClose={() => setConfig("")} />}
    </>
  );
}

/** The CLI for the new session: those the machine has; the draft stays. */
export function CliSheet({ ns, onClose, onMachine }: { ns: NewSession; onClose: () => void; onMachine: () => void }) {
  const { remote, cfg, machine } = ns;
  const have = machineAgents(machine);
  const all = [Agent.CODEX, Agent.CLAUDE];
  // Another online machine of the project that has a CLI this one lacks.
  const missing = all.filter((a) => !have.includes(a));
  const elsewhere = missing.flatMap((a) =>
    [...remote.machines.values()].filter((m) => m.online && m.id !== cfg.machineId && machineAgents(m).includes(a)).slice(0, 1).map((m) => ({ a, m })),
  );
  return (
    <Sheet onClose={onClose}>
      <h4>新会话使用的 CLI</h4>
      <div className="list">
        {have.map((a) => {
          const v = machine?.meta?.agents.find((x) => x.agent === a);
          return (
            <button key={a} className="mi" onClick={() => (ns.setCfg(switchAgent(cfg, a)), onClose())}>
              <div className="mt">
                <b>{agentName(a)}</b>
                <div className="md">
                  {machineName(machine)} 上已安装{v?.version ? ` · ${v.version}` : ""}
                </div>
              </div>
              {a === cfg.agent && <span className="ck">✓</span>}
            </button>
          );
        })}
      </div>
      <div className="hint">切换后采用该 CLI 的默认模型和有效模式，草稿保留。{have.length === 1 ? `${machineName(machine)} 上只有 ${agentName(have[0])}。` : ""}</div>
      {missing.map((a) => {
        const other = elsewhere.find((x) => x.a === a)?.m;
        return (
          <Banner key={a} action={other ? "换机器" : undefined} onAction={onMachine}>
            {machineName(machine)} 没有 {agentName(a)}
            {other ? `，${machineName(other)} 上有` : ""}
          </Banner>
        );
      })}
    </Sheet>
  );
}

type Pick = "" | "machine" | "agent" | "model" | "mode";

/** 这次的设置: machine, CLI, folder, model and mode for this session. */
export function ConfigSheet({ ns, onClose, start = "" }: { ns: NewSession; onClose: () => void; start?: Pick }) {
  const [pick, setPick] = useState<Pick>(start);
  // Opened right at one setting: done with it is done with the sheet.
  const back2 = () => (start ? onClose() : setPick(""));
  const { remote, cfg, p, machine, info } = ns;
  const set = (patch: Partial<NewConfig>) => ns.setCfg({ ...cfg, ...patch });
  const def = defaultsOf(info.settings, cfg.agent);
  // Machines this project has used; a new project can use any.
  const choices: MachineView[] = (p && machine?.online ? p.machines : [...remote.machines.keys()]).map((id) => remote.machines.get(id)).filter((m): m is MachineView => !!m);
  const back = (
    <button className="back2" onClick={back2} aria-label="返回">
      ‹
    </button>
  );
  // The CLI: those the machine has, with where to find the others.
  if (pick === "agent") return <CliSheet ns={ns} onClose={back2} onMachine={() => setPick("machine")} />;
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
                    {machineAgents(m).includes(cfg.agent) ? `有 ${agentName(cfg.agent)}` : `只有 ${machineAgents(m).map(agentName).join("、")}`}；模型和模式会按这台机器重新选择
                  </div>
                )
              )}
            </div>
            {m.id === cfg.machineId && <span className="ck">✓</span>}
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
            {!cfg.model && <span className="ck">✓</span>}
          </button>
          {info.models?.models.map((m) => (
            <button key={m.slug} className="mi" onClick={() => set({ model: m.slug, effort: "" })}>
              <div className="mt">{m.displayName || m.slug}</div>
              {cfg.model && m === findModel(info.models?.models, cfg.model) && <span className="ck">✓</span>}
            </button>
          ))}
        </div>
        {chosen && chosen.efforts.length > 0 && (
          <>
            <div className="fl">推理强度</div>
            <div className="seg wrap">
              {chosen.efforts.map((e) => (
                <button key={e} className={e === (cfg.effort || (cfg.model ? chosen.defaultEffort : def.effort || chosen.defaultEffort)) ? "on" : ""} onClick={() => set({ model: chosen.slug, effort: e })}>
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
            {m.mode === cfg.mode && <span className="ck">✓</span>}
          </button>
        ))}
      </div>
    );
  }
  const titles: Record<Pick, string> = { "": "项目设置", machine: "机器", agent: "CLI", model: "模型", mode: "审批" };
  const modelLine = ns.parts.model;
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
            <button className="sr" onClick={() => setPick("agent")}>
              <span className="lbl">CLI</span>
              <span className="v">
                <em>{agentName(cfg.agent)}</em>
              </span>
            </button>
            <button className="sr" onClick={() => setPick("machine")}>
              <span className="lbl">机器</span>
              <span className="v">
                <Dot tone={machine?.online ? "idle" : "off"} />
                <em>{machineName(machine)}</em>
              </span>
            </button>
            <button className="sr" onClick={() => setPick("model")}>
              <span className="lbl">模型</span>
              <span className={"v" + (info.loading ? " muted" : "")}>
                <em>{info.loading ? `读取 ${machineName(machine)} 的设置…` : modelLine}</em>
              </span>
            </button>
            <button className="sr" onClick={() => setPick("mode")}>
              <span className="lbl">审批</span>
              <span className="v">
                <em>{ns.modeText}</em>
              </span>
            </button>
          </div>
          {info.error && (
            <Banner kind="warn" action="重试" onAction={info.retry}>
              没能读取模型列表，会用 {agentName(cfg.agent)} 自己的默认模型
            </Banner>
          )}
          <div className="hint">只影响之后新建的会话，进行中的不变；在你的设备之间同步。</div>
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
            <div className="mt">‹ 上一级</div>
          </button>
        )}
        {dir?.dirs.map((d) => (
          <button key={d} className="mi" disabled={!!loading} onClick={() => void open(dir.path.replace(/\/$/, "") + "/" + d)}>
            <div className="mt">{d}</div>
            <span className="muted">›</span>
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
      <span className="chev">›</span>
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
          ＋ 新项目…<small>选机器和目录</small>
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
            ‹
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
export function NewFlow({ remote, go, onClose, onPicked }: { remote: Remote; go: Go; onClose: () => void; onPicked?: (key: string) => void }) {
  const [step, setStep] = useState<"project" | "new">(remote.sessions.size ? "project" : "new");
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
        onBack={remote.sessions.size ? () => setStep("project") : undefined}
        onClose={onClose}
        onDone={(machineId, path) => {
          remote.draftMachines.set(path, machineId);
          open(path);
        }}
      />
    );
  return <ProjectPicker remote={remote} onPick={open} onNew={() => setStep("new")} onClose={onClose} title={onPicked ? "选项目" : undefined} />;
}
