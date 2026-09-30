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
  machineName,
  modeName,
  projectName,
  projects,
  shortPath,
  tailPath,
  titleOf,
  type MachineLike,
  type NewConfig,
} from "../model";
import { Attempt } from "../attempt";
import { loadProjectConfig, saveProjectConfig, clearProjectConfig } from "../storage";
import type { CreateSessionRequest } from "../gen/remoteagent/v1/local_pb";
import { errorText, type MachineView, type Remote } from "../remote";

type CreateReq = Omit<CreateSessionRequest, "$typeName" | "args" | "env">;
type StartPayload = { machineId: string; req: CreateReq };
import { FullAccessAlert } from "../sheets";
import { Avatar, Banner, Dot, Sheet } from "../ui";

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

/** What the configuration line says: machine, CLI, model and effort, mode. */
export function describe(cfg: NewConfig, m: MachineLike | undefined, info: ReturnType<typeof useMachineInfo>): string {
  const def = defaultsOf(info.settings, cfg.agent);
  const slug = cfg.model || def.model || info.models?.defaultModel || "";
  const model = findModel(info.models?.models, slug);
  const effort = cfg.effort || (cfg.model ? model?.defaultEffort : def.effort || model?.defaultEffort) || "";
  const modelText = slug ? `${modelLabel(info.models?.models, slug, cfg.agent)}${effort ? " " + effort : ""}` : info.loading ? "读取设置…" : "默认模型";
  const mode = modeName(cfg.mode || def.mode) || "默认审批";
  const parts = [machineName(m)];
  if (machineAgents(m).length > 1 || cfg.agent !== Agent.CODEX) parts.push(agentName(cfg.agent));
  parts.push(modelText, mode);
  return parts.join(" · ");
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
  // Full access confirmed for this project during this visit, as when it
  // was chosen in 这次的设置.
  const [fullConfirmed, setFullConfirmed] = useState("");
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
  const create = async (prompt: string, mode?: ApprovalMode) => {
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
    fullConfirmed: fullConfirmed === key,
    confirmFull: () => setFullConfirmed(key),
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
 * the project chip leading the configuration line). */
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
  const [askFull, setAskFull] = useState(false);
  const [config, setConfig] = useState(false);
  const input = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (autoFocus) input.current?.focus();
  }, [autoFocus]);
  // The machine's default mode is still being read: nothing to confirm yet.
  const modePending = !ns.cfg.mode && ns.effectiveMode === undefined && ns.info.loading;
  const submit = async (confirmed = false) => {
    // An unknown start that expired is not sent again, nor replaced by the
    // same start under a new request.
    if (ns.busy || ns.blocked || modePending || (ns.expired && !ns.changes(text))) return;
    // Retrying an unknown start sends it as it was.
    const retry = ns.frozen && !ns.changes(text);
    if (!retry) {
      if (ns.effectiveMode === undefined) return setModeUnknown(true);
      // Full access, chosen or the machine's default, is confirmed for
      // each start; then it is sent as chosen, not left to the default.
      if (ns.effectiveMode === ApprovalMode.FULL_ACCESS && !ns.fullConfirmed && !confirmed) return setAskFull(true);
    }
    await ns.create(text.trim(), !retry && ns.effectiveMode === ApprovalMode.FULL_ACCESS ? ApprovalMode.FULL_ACCESS : undefined);
  };
  const [modeUnknown, setModeUnknown] = useState(false);
  const agent = agentName(ns.cfg.agent);
  const status = ns.busy ? `正在 ${machineName(ns.machine)} 上启动…` : "";
  const placeholder = {
    box: `在 ${name} 里要 ${agent} 做什么？`,
    prompt: `在 ${name} 里开始新会话，⌘↵ 发送`,
    start: "描述任务，⌘↵ 开始；也可以不写",
  }[variant];
  const field = (
    <textarea
      ref={input}
      className={"in" + (text ? " typed" : "")}
      rows={variant === "box" ? 1 : 2}
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
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          void submit();
        }
      }}
      aria-label="新会话的第一句话"
    />
  );
  const go = (
    <button className="go" disabled={ns.busy || !!ns.blocked || modePending || (ns.expired && !ns.changes(text))} onClick={() => void submit()} aria-label="开始新会话">
      ↑
    </button>
  );
  const cfgLine = (
    <div className="cfg">
      {lead}
      {!lead && <Dot tone={ns.machine?.online ? "idle" : "off"} />}
      <em className={ns.blocked ? "warn-text" : ""}>{status || ns.blocked || ns.line}</em>
      <button className="link" onClick={() => setConfig(true)}>
        更改
      </button>
    </div>
  );
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
  return (
    <>
      {variant === "box" ? (
        <div className="newbox">
          {error}
          <div className="nh">＋ 新会话</div>
          <div className="row2">
            {field}
            {go}
          </div>
          {cfgLine}
        </div>
      ) : (
        <>
          <div className="prompt">
            {field}
            <div className="pf">
              {variant === "prompt" ? cfgLine : <span />}
              {go}
            </div>
          </div>
          {variant === "start" && <div className="opts">{cfgLine}</div>}
          {error}
        </>
      )}
      {ns.configError && <Banner kind="warn">{ns.configError}</Banner>}
      {modeUnknown && ns.effectiveMode === undefined && (
        <Banner kind="warn" action="重试" onAction={() => (setModeUnknown(false), ns.info.retry())}>
          没能读取 {machineName(ns.machine)} 的默认审批模式，在「更改」里选一种再开始
        </Banner>
      )}
      {askFull && (
        <FullAccessAlert
          forNew
          onCancel={() => setAskFull(false)}
          onOk={() => {
            setAskFull(false);
            ns.confirmFull();
            void submit(true);
          }}
        />
      )}
      {config && <ConfigSheet ns={ns} onClose={() => setConfig(false)} />}
    </>
  );
}

type Pick = "" | "machine" | "agent" | "dir" | "model" | "mode";

/** 这次的设置: machine, CLI, folder, model and mode for this session. */
export function ConfigSheet({ ns, onClose }: { ns: NewSession; onClose: () => void }) {
  const [pick, setPick] = useState<Pick>("");
  const [confirmFull, setConfirmFull] = useState(false);
  const { remote, cfg, p, machine, info } = ns;
  const set = (patch: Partial<NewConfig>) => ns.setCfg({ ...cfg, ...patch });
  const def = defaultsOf(info.settings, cfg.agent);
  // Machines this project has used; a new project can use any.
  const choices: MachineView[] = (p ? p.machines : [...remote.machines.keys()]).map((id) => remote.machines.get(id)).filter((m): m is MachineView => !!m);
  const back = (
    <button className="back2" onClick={() => setPick("")} aria-label="返回">
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
          setPick("");
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
            setPick("");
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
              const agents = machineAgents(m);
              ns.setCfg({ ...cfg, machineId: m.id, agent: agents.includes(cfg.agent) ? cfg.agent : agents[0], model: "", effort: "", mode: ApprovalMode.UNSPECIFIED });
              setPick("");
            }}
          >
            <Dot tone={m.online ? "idle" : "off"} />
            <div className="mt">
              {machineName(m)}
              {!m.online && <div className="md">{m.lastSeenMs ? ago(m.lastSeenMs) + "在线，" : ""}现在不能新建</div>}
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
            <button key={a} className="mi" onClick={() => (ns.setCfg({ ...cfg, agent: a, model: "", effort: "" }), setPick(""))}>
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
              setPick("");
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
  const list = projects(remote.sessions.values());
  return (
    <Sheet onClose={onClose}>
      <div className="ns-head">
        <b>{title}</b>
        <button className="link" onClick={onClose}>
          取消
        </button>
      </div>
      <div className="plist">
        {list.map((p) => (
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
        ))}
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

