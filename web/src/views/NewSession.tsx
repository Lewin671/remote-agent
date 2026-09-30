// Starting sessions. There is one way: type in a project's new-session
// box and send. The configuration follows the project (its latest
// session's machine, CLI, model, effort and mode) and is written in one
// line under the box; 更改 opens the sheet to change it.

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
  projectName,
  projects,
  shortPath,
  tailPath,
  titleOf,
  type MachineLike,
  type NewConfig,
  type Project,
} from "../model";
import { Attempt } from "../attempt";
import { loadProjectConfig, saveProjectConfig, clearProjectConfig } from "../storage";
import type { CreateSessionRequest } from "../gen/remoteagent/v1/local_pb";
import { errorText, type MachineView, type Remote, type SessionView } from "../remote";

type CreateReq = Omit<CreateSessionRequest, "$typeName" | "args" | "env" | "imageIds">;
type StartPayload = { machineId: string; req: CreateReq };
import { FullAccessAlert } from "../sheets";
import { Avatar, Banner, Dot, Sheet } from "../ui";
import { groupProjects } from "../prefs";
import { useAutoGrow } from "./Composer";

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
  return { machine: machineName(m), agent: agentName(cfg.agent), model: modelText, slug, mode: modeName(cfg.mode || def.mode) || "默认审批" };
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
  const override = selection?.key === key ? selection.config : loadProjectConfig(remote.account, key);
  // Once a draft is begun its launch settings hold: later activity (a
  // newer session in the project, a machine coming or going) does not
  // change where or how the draft will start. Only the user's own choice
  // does.
  const [latched, setLatched] = useState<{ key: string; config: NewConfig }>();
  const held = latched?.key === key ? latched.config : undefined;
  const cfg = override ? { ...override, from: inherited?.from } : (held ?? inherited ?? freshConfig(remote, key, remote.draftMachines.get(key)));
  // Full access confirmed for this project, machine and CLI during this
  // visit, as when it was chosen in 这次的设置. Another CLI or machine
  // asks again: its default may be full access too.
  const [fullConfirmed, setFullConfirmed] = useState("");
  const confirmKey = `${key}|${cfg.machineId}|${cfg.agent}`;
  const machine = remote.machines.get(cfg.machineId);
  const info = useMachineInfo(remote, cfg.machineId, cfg.agent);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [configError, setConfigError] = useState("");
  const setCfg = (config: NewConfig) => {
    setSelection({ key, config });
    try {
      saveProjectConfig(remote.account, key, config);
      setConfigError("");
    } catch {
      setConfigError("设置未能保存在浏览器里，刷新后会恢复原设置");
    }
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
  const payloadFor = (prompt: string, mode?: ApprovalMode): StartPayload => {
    const pending = current.payload;
    const c = pending && !override ? { machineId: pending.machineId, ...pending.req } : { machineId: cfg.machineId, cwd: cfg.cwd, model: cfg.model, effort: cfg.effort, mode: mode ?? cfg.mode, agent: cfg.agent };
    return { machineId: c.machineId, req: { cwd: c.cwd, prompt: prompt.trim(), model: c.model, effort: c.effort, mode: c.mode, agent: c.agent } };
  };
  /** Whether sending prompt now would be a different start from the one
   * whose outcome is unknown. */
  const changes = (prompt: string, mode?: ApprovalMode): boolean => {
    const pending = current.payload;
    return !!pending && JSON.stringify(payloadFor(prompt, mode)) !== JSON.stringify(pending);
  };
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
      if (terminal) remote.termFirst.add(sid);
      remote.draftMachines.delete(key);
      // The new session now supplies the project's next launch settings.
      try { clearProjectConfig(remote.account, key); } catch { /* Keep the saved choice if storage is unavailable. */ }
      setSelection(undefined);
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
  const blocked = !machine?.online ? `${machineName(machine)} 不在线` : remote.conn !== "online" ? "和服务器断线了" : "";
  // Giving up an unknown start: the user checked, or changed what to send.
  const abandon = () => {
    current.abandon();
    setError("");
  };
  /** Holds the launch settings as they are now, once a draft begins. */
  const latch = () => {
    if (!override && !held) setLatched({ key, config: cfg });
  };
  // The mode the start will run in: the one chosen, else the machine's
  // default for this CLI; undefined while that default is not known.
  const def = defaultsOf(info.settings, cfg.agent);
  const effectiveMode = cfg.mode || (info.settings ? def.mode : undefined);
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
    effectiveMode,
    fullConfirmed: fullConfirmed === confirmKey,
    confirmFull: () => setFullConfirmed(confirmKey),
    issue: newConfigIssue(cfg.agent, describeParts(cfg, machine, info).slug, effectiveMode ?? ApprovalMode.UNSPECIFIED),
    parts: describeParts(cfg, machine, info),
    machine,
    info,
    line: describe(cfg, machine, info),
    create,
    busy,
    error,
    configError,
    blocked,
  };
}

export type NewSession = ReturnType<typeof useNewSession>;

/** The new-session box: at the bottom of a project page ("box"), at the
 * top of a wide project page ("prompt"), or the wide home ("start", with
 * the project chip leading). The CLI is picked right on it; the machine
 * and mode, and the model and effort, are written out; 更改 opens the
 * rest. */
export function NewBox({
  ns,
  name,
  autoFocus,
  variant = "box",
  lead,
  onDraft,
}: {
  ns: NewSession;
  name: string;
  autoFocus?: boolean;
  variant?: "box" | "prompt" | "start";
  lead?: ReactNode;
  onDraft?: () => void;
}) {
  const [text, setText] = useState("");
  const [askFull, setAskFull] = useState<"" | "send" | "terminal">("");
  const [config, setConfig] = useState<"" | "main" | Pick>("");
  const [cli, setCli] = useState(false);
  const input = useRef<HTMLTextAreaElement>(null);
  useAutoGrow(input, text, variant === "box" ? 132 : 240);
  useEffect(() => {
    if (autoFocus) input.current?.focus();
  }, [autoFocus]);
  // The machine's default mode is still being read: nothing to confirm yet.
  const modePending = !ns.cfg.mode && ns.effectiveMode === undefined && ns.info.loading;
  const [modeUnknown, setModeUnknown] = useState(false);
  const cannot = ns.busy || !!ns.blocked || modePending || !!ns.issue;
  const submit = async (confirmed = false, terminal = false) => {
    const prompt = terminal ? "" : text;
    // An unknown start that expired is not sent again, nor replaced by the
    // same start under a new request.
    if (cannot || (ns.expired && !ns.changes(prompt))) return;
    // Retrying an unknown start sends it as it was.
    const retry = ns.frozen && !ns.changes(prompt);
    if (!retry) {
      if (ns.effectiveMode === undefined) return setModeUnknown(true);
      // Full access, chosen or the machine's default, is confirmed for
      // each start; then it is sent as chosen, not left to the default.
      if (ns.effectiveMode === ApprovalMode.FULL_ACCESS && !ns.fullConfirmed && !confirmed) return setAskFull(terminal ? "terminal" : "send");
    }
    const ok = await ns.create(prompt.trim(), !retry && ns.effectiveMode === ApprovalMode.FULL_ACCESS ? ApprovalMode.FULL_ACCESS : undefined, terminal);
    if (ok && !terminal) setText("");
  };
  const agent = agentName(ns.cfg.agent);
  const p = ns.parts;
  const status = ns.busy ? `正在 ${machineName(ns.machine)} 上启动…` : "";
  const placeholder = {
    box: `在 ${name} 里开始一个任务…`,
    prompt: `在 ${name} 里开始新会话…`,
    start: "描述任务，⌘/Ctrl+Enter 开始",
  }[variant];
  const disabled = cannot || (ns.expired && !ns.changes(text)) || !text.trim();
  const field = (
    <textarea
      ref={input}
      className={variant === "box" ? "in" + (text ? " typed" : "") : "draft" + (text ? " typed" : "")}
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
      aria-label="新会话的第一句话"
    />
  );
  const cliPick = (
    <button className="cli-pick" onClick={() => setCli(true)} aria-label={`CLI：${agent}`}>
      {agent} ▾
    </button>
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
      {ns.configError && <Banner kind="warn">{ns.configError}</Banner>}
      {modeUnknown && ns.effectiveMode === undefined && (
        <Banner kind="warn" action="重试" onAction={() => (setModeUnknown(false), ns.info.retry())}>
          没能读取 {machineName(ns.machine)} 的默认审批模式，在「更改」里选一种再开始
        </Banner>
      )}
    </>
  );
  const onlyTerm = !text && (
    <button className="onlyterm" disabled={cannot} onClick={() => void submit(false, true)}>
      只打开终端 ›
    </button>
  );
  const go =
    variant === "box" ? (
      <button className="go" disabled={disabled} onClick={() => void submit()} aria-label="开始新会话">
        ↑
      </button>
    ) : (
      <button className={"go44" + (disabled ? " off" : "")} disabled={disabled} onClick={() => void submit()} aria-label="开始新会话">
        ↑
      </button>
    );
  let body;
  if (variant === "box")
    body = (
      <div className="newbox">
        {error}
        {notes}
        <div className="nh">
          <span>＋ 新会话</span>
          {cliPick}
        </div>
        <div className="row2">
          {field}
          {go}
        </div>
        <div className="cfg">
          <div className="cfg-main">
            <Dot tone={ns.machine?.online ? "idle" : "off"} />
            <span className={ns.blocked ? "warn-text" : ""}>{status || p.machine}</span>
            <span>· {p.mode}</span>
          </div>
          <div className="cfg-model">{p.model}</div>
          <button className="cfg-edit" onClick={() => setConfig("main")}>
            更改
          </button>
        </div>
        {onlyTerm}
      </div>
    );
  else
    body = (
      <>
        <div className="wide-project-tools">
          {lead}
          {cliPick}
          <span className="small muted cfgsum">
            <Dot tone={ns.machine?.online ? "idle" : "off"} /> {status || p.machine} · {p.mode}
          </span>
          {variant === "prompt" && (
            <button className="link small" onClick={() => setConfig("main")}>
              更改
            </button>
          )}
        </div>
        {error}
        {notes}
        <div className="compose-card newcard">
          {field}
          <div className="controls">
            <button className={"ctl mode" + (p.mode === "完全放行" ? " danger" : "")} onClick={() => setConfig("mode")}>
              {p.mode} ▾
            </button>
            <button className="ctl model" onClick={() => setConfig("model")}>
              <em>{p.model} ▾</em>
            </button>
            {go}
          </div>
        </div>
        {onlyTerm}
        {variant === "start" && (
          <div className="small muted startnote">
            {ns.cfg.from ? `沿用 ${projectName(ns.cfg.cwd)} 最近一次的 ${agent} 设置` : `用 ${p.machine} 的默认设置`} ·{" "}
            <button className="link small" onClick={() => setConfig("main")}>
              更改机器与目录
            </button>
          </div>
        )}
      </>
    );
  return (
    <>
      {body}
      {askFull && (
        <FullAccessAlert
          forNew
          onCancel={() => setAskFull("")}
          onOk={() => {
            const terminal = askFull === "terminal";
            setAskFull("");
            ns.confirmFull();
            void submit(true, terminal);
          }}
        />
      )}
      {config && <ConfigSheet ns={ns} start={config === "main" ? "" : (config as Pick)} onClose={() => setConfig("")} />}
      {cli && <CliSheet ns={ns} onClose={() => setCli(false)} onMachine={() => (setCli(false), setConfig("machine"))} />}
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

type Pick = "" | "machine" | "agent" | "dir" | "model" | "mode";

/** 这次的设置: machine, CLI, folder, model and mode for this session. */
export function ConfigSheet({ ns, onClose, start = "" }: { ns: NewSession; onClose: () => void; start?: Pick }) {
  const [pick, setPick] = useState<Pick>(start);
  // Opened right at one setting: done with it is done with the sheet.
  const back2 = () => (start ? onClose() : setPick(""));
  const [confirmFull, setConfirmFull] = useState(false);
  const { remote, cfg, p, machine, info } = ns;
  const set = (patch: Partial<NewConfig>) => ns.setCfg({ ...cfg, ...patch });
  const def = defaultsOf(info.settings, cfg.agent);
  // Machines this project has used; a new project can use any.
  const choices: MachineView[] = (p ? p.machines : [...remote.machines.keys()]).map((id) => remote.machines.get(id)).filter((m): m is MachineView => !!m);
  const back = (
    <button className="back2" onClick={back2} aria-label="返回">
      ‹
    </button>
  );
  if (confirmFull)
    return (
      <FullAccessAlert
        forNew
        onCancel={() => setConfirmFull(false)}
        onOk={() => {
          setConfirmFull(false);
          set({ mode: ApprovalMode.FULL_ACCESS });
          ns.confirmFull();
          back2();
        }}
      />
    );
  if (pick === "dir")
    return (
      <Sheet onClose={onClose}>
        <DirBrowser
          remote={remote}
          machineId={cfg.machineId}
          start={cfg.cwd}
          head={
            <div className="ns-head">
              {back}
              <b>选目录</b>
            </div>
          }
          label={(name) => `在 ${name} 里开始`}
          onPick={(path) => {
            set({ cwd: path });
            back2();
          }}
        />
      </Sheet>
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
              ns.setCfg(switchMachine(cfg, m));
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
  else if (pick === "agent")
    body = (
      <div className="list">
        {machineAgents(machine).map((a) => {
          const v = machine?.meta?.agents.find((x) => x.agent === a);
          return (
            <button key={a} className="mi" onClick={() => (ns.setCfg(switchAgent(cfg, a)), back2())}>
              <div className="mt">
                {agentName(a)}
                {v?.version && <small>{v.version}</small>}
              </div>
              {a === cfg.agent && <span className="ck">✓</span>}
            </button>
          );
        })}
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
              默认模型<div className="md">用 {machineName(machine)} 的新会话默认设置</div>
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
          { mode: ApprovalMode.UNSPECIFIED, name: "默认", desc: def.mode ? `${machineName(machine)} 的默认：${modeName(def.mode)}` : `跟随 ${agentName(cfg.agent)} 自己的配置` },
          { mode: ApprovalMode.ASK, name: "询问", desc: "需要审批时由你确认，超时拒绝" },
          noAuto
            ? { mode: ApprovalMode.AUTO, name: "自动", desc: "Haiku 没有自动模式", off: true }
            : { mode: ApprovalMode.AUTO, name: "自动", desc: `由 ${agentName(cfg.agent)} 自己判断，拿不准的在对话里问你` },
          { mode: ApprovalMode.FULL_ACCESS, name: "完全放行", desc: "所有操作都不审批，每次新建都要确认", danger: true },
        ].map((m: { mode: ApprovalMode; name: string; desc: string; danger?: boolean; off?: boolean }) => (
          <button
            key={m.mode}
            className={"mi" + (m.danger ? " danger" : "")}
            disabled={m.off}
            onClick={() => {
              if (m.mode === ApprovalMode.FULL_ACCESS && cfg.mode !== m.mode) return setConfirmFull(true);
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
  const titles: Record<Pick, string> = { "": "这次的设置", machine: "机器", agent: "CLI", dir: "目录", model: "模型", mode: "审批" };
  const modelLine = describe(cfg, machine, info).split(" · ").slice(-2, -1)[0];
  return (
    <Sheet onClose={onClose}>
      <div className="ns-head">
        {pick && back}
        <b>{titles[pick]}</b>
        {!pick && (
          <button className="link" onClick={onClose}>
            完成
          </button>
        )}
      </div>
      {body ?? (
        <>
          <div className="setrows">
            <button className="sr" onClick={() => setPick("machine")}>
              <span className="lbl">机器</span>
              <span className="v">
                <Dot tone={machine?.online ? "idle" : "off"} />
                <em>{machineName(machine)}</em>
              </span>
            </button>
            {machineAgents(machine).length > 1 && (
              <button className="sr" onClick={() => setPick("agent")}>
                <span className="lbl">CLI</span>
                <span className="v">
                  <em>{agentName(cfg.agent)}</em>
                </span>
              </button>
            )}
            <button className="sr" onClick={() => setPick("dir")} disabled={!machine?.online}>
              <span className="lbl">目录</span>
              <span className="v">
                <em>{tailPath(cfg.cwd, 26)}</em>
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
                <em>{modeName(cfg.mode || def.mode) || "默认"}</em>
              </span>
            </button>
          </div>
          {info.error && (
            <Banner kind="warn" action="重试" onAction={info.retry}>
              没能读取模型列表，会用 {agentName(cfg.agent)} 自己的默认模型
            </Banner>
          )}
          <div className="hint">
            {cfg.from
              ? `默认沿用 ${projectName(cfg.cwd)} 最近一个会话（${titleOf(cfg.from)}）。改了之后，这个项目下次也会沿用新的设置。机器只在这个项目用过的机器里选；目录可以选项目里的子目录`
              : `新项目用 ${machineName(machine)} 的默认设置。`}
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
  // Grouped as the lists are; removed projects are left out.
  const g = groupProjects(projects(remote.sessions.values()), remote.projectPrefs());
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
      <div className="plist">
        {g.pinned.length > 0 && <div className="fl">常用项目</div>}
        {g.pinned.map(row)}
        {g.pinned.length > 0 && g.others.length > 0 && <div className="fl">其他项目</div>}
        {g.others.map(row)}
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

