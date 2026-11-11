// Starting sessions. There is one way: type in a project's new-session
// box and send. Project settings persist in the sealed account prefs.

import { AgentMark } from "../agent-mark";
import { useKeyboardDismiss } from "../keyboard";
import { useEffect, useRef, useState, type ReactNode, type ComponentProps } from "react";
import { ApprovalMode, type Agent } from "../gen/remoteagent/v1/event_pb";
import type { DaemonSettings, ListDirectoriesResponse, ListModelsResponse } from "../gen/remoteagent/v1/local_pb";
import { AGENTS, agentInfo, defaultsFor } from "../agents";
import {
  agentName,
  agentOf,
  exited,
  ago,
  findModel,
  STANDARD_WINDOW,
  allAutoUnavailable,
  autoUnavailable,
  modelLabel,
  inheritConfig,
  machineAgents,
  autoIssueKind,
  newConfigIssue,
  switchMachine,
  machineName,
  modeName,
  defaultModeOf,
  modeLabel,
  projectName,
  pathCrumbs,
  projects,
  shortPath,
  type MachineLike,
  type NewConfig,
} from "../model";
import { Attempt } from "../attempt";
import type { CreateSessionRequest } from "../gen/remoteagent/v1/local_pb";
import { errorText, type MachineView, type Remote } from "../remote";

type CreateReq = Omit<CreateSessionRequest, "$typeName" | "args" | "env">;
type StartPayload = { machineId: string; req: CreateReq };

/** A project's start. It belongs to the account, not to the page that
 * sent it (Android's `Starts`, iOS's `NewSessionDraft`): the page may be
 * left and opened again while the request is on its way or its outcome
 * unknown, and the wide home shows one project after another in one box. */
class Start {
  readonly attempt = new Attempt<StartPayload>();
  images: { key: string; prepared?: import("../attach").Prepared }[] = [];
  /** Its request is on its way. Nothing else is started in the project
   * meanwhile, whichever page is open. */
  running = false;
  /** What was chosen on the page the pending request was sent from. A page
   * opened later goes on from there. */
  page?: { local?: Local; picked?: Picked; latched?: Latched };
  /** What the requests that started a session were sent with. It went with
   * them, on every page that held it; what a page chose meanwhile, for the
   * session after, is another choice and stays. */
  readonly spent = new WeakSet<Local | Picked | Latched>();
  /** How many sessions it has started. */
  count = 0;
}
const starts = new WeakMap<Remote, Map<string, Start>>();
/** One start per project: an unknown start in one project must never be
 * what another project's box sends. */
function startOf(remote: Remote, key: string): Start {
  let all = starts.get(remote);
  if (!all) starts.set(remote, (all = new Map()));
  let start = all.get(key);
  if (!start) all.set(key, (start = new Start()));
  return start;
}
import { anchorOf, Avatar, Banner, PathText, Segmented, StateMark, fromHere, sendKeys, Sheet, usePointerFine } from "../ui";
import { isHidden } from "../prefs";
import { addProject, pinAction, prefsBlocked, ProjectItem, ProjectList, RemoveProjectAlert } from "./Projects";
import { ComposerPicker, keepComposerFocus, type ComposerAnchor } from "./ComposerPicker";
import { useAutoGrow } from "./Composer";
import { AttachButton, attachBlocked, maxImages, TrayView, useDropPaste, useTray } from "./Attach";
import { ArrowUp, Check, ChevronDown, ChevronLeft, ChevronRight, X } from "../icons";

/** Navigate to a route; with replace the current history entry is swapped, so back skips it. */
export type Go = (route: string, replace?: boolean) => void;

/** The machine's defaults for new sessions of a CLI. */
function defaultsOf(st: DaemonSettings | undefined, agent: Agent): { model: string; effort: string; mode: ApprovalMode } {
  return defaultsFor(st, agent);
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
 * model and effort, and the mode (a scheduled task's). */
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
  const window = !agentInfo(cfg.agent).standardContextOption ? 0n : cfg.standardContext || fixedWindow ? STANDARD_WINDOW : max || 1_000_000n;
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
  return AGENTS.map((a) => a.agent).filter((a) => have.includes(a));
}

type AgentSet = { agent: Agent; model: string; effort: string; mode: ApprovalMode; standardContext: boolean };

function setOf(c: NewConfig): AgentSet {
  return { agent: c.agent, model: c.model, effort: c.effort, mode: c.mode, standardContext: agentInfo(c.agent).standardContextOption && !!c.standardContext };
}

/** The mode a start with cfg will run in: the one chosen (a scheduled task's), else the CLI's
 * effective default the machine reports, or that it follows the CLI's
 * own settings when nobody could read them. */
function modeTextOf(cfg: NewConfig, m: MachineLike | undefined): string {
  const dm = defaultModeOf(m, cfg.agent);
  return cfg.mode ? modeName(cfg.mode) : dm.known && dm.mode ? modeName(dm.mode) : "跟随 CLI 设置";
}

/** What the user chose on this page and the stored settings do not say
 * (yet): another machine, edited sets. once: the machine is for this
 * start only and nothing of it is saved. */
type Local = { key: string; machineId?: string; once: boolean; sets: Partial<Record<Agent, AgentSet>> };
/** The CLI picked next to the box. */
type Picked = { key: string; agent: Agent };
/** The launch settings a draft was begun with. */
type Latched = { key: string; machineId: string; agent: Agent; by: Partial<Record<Agent, NewConfig>> };

/** The configuration and the create call of a new session in project key.
 * The project's settings name the machine and keep one set of model and
 * effort per CLI; which CLI a start uses is picked next to the box, the
 * project remembering the last one used. A new task has no approval mode:
 * what is stored or was inherited is not read, none is stored, and the
 * request says none, so the machine's default applies. A scheduled task
 * that shares this hook holds its own mode in this page only. */
export function useNewSession(remote: Remote, key: string, go: Go, fixedMachine?: string) {
  // Recomputed on every render: which session is latest changes with any
  // session's activity, not only when sessions come or go.
  const p = projects([...remote.sessions.values()].filter(s => !fixedMachine || s.machineId === fixedMachine), remote.machines.values(), remote.projectPrefs().settings.filter(s => !fixedMachine || s.machineId === fixedMachine)).find((x) => x.key === key);
  const saved = remote.projectPrefs().settings.find((s) => s.path === key && (!fixedMachine || s.machineId === fixedMachine));
  const start = startOf(remote, key);
  const current = start.attempt;
  const draftKey = "new:" + key;
  const text = remote.drafts.get(draftKey) ?? current.payload?.req.prompt ?? "";
  const setText = (value: string) => {
    remote.drafts.set(draftKey, value);
    remote.touch();
  };
  // What a page chose is for one draft, of this project: once a session
  // has started with it, it is no longer held.
  const own = <T extends Local | Picked | Latched>(x: T | undefined) => (x?.key === key && !start.spent.has(x) ? x : undefined);
  // A request that is on its way, or whose outcome is unknown, was sent
  // with choices of its own. A page that has none goes on from them.
  const sentWith = !fixedMachine && current.payload ? start.page : undefined;
  const [localState, setLocal] = useState<Local>();
  const local = own(localState) ?? sentWith?.local;
  // The CLI picked next to the box, for this draft.
  const [pickedState, setPicked] = useState<Picked>();
  const pick = own(pickedState) ?? sentWith?.picked;
  const picked = pick?.agent;
  // Once a draft is begun its launch settings hold: later activity (a
  // newer session in the project, a machine coming or going) does not
  // change where or how the draft will start. Only the user's own choice
  // does.
  const [latched, setLatched] = useState<Latched>();
  const held = own(latched) ?? sentWith?.latched;
  // They are this page's own from then on: they stay when that start ends
  // without a session, or is given up.
  const adopt = () => {
    if (!sentWith) return;
    if (!own(localState) && sentWith.local) setLocal(sentWith.local);
    if (!own(pickedState) && sentWith.picked) setPicked(sentWith.picked);
    if (!own(latched) && sentWith.latched) setLatched(sentWith.latched);
  };
  useEffect(adopt);
  // While that request is pending and the user has chosen nothing else,
  // the page shows the request's own configuration and nothing derived
  // again: sessions and machines come and go meanwhile, and a retry is
  // that request, on its machine, with its images.
  const pinned = sentWith && local === sentWith.local && picked === sentWith.picked?.agent ? current.payload : undefined;
  const inherit = (a?: Agent) => inheritConfig(p, remote.machines, key, a);
  const first = inherit();
  // Where it starts without a choice made on this page.
  // A project none of whose sessions are held starts on a machine that
  // has it, as its summary says, rather than on any machine.
  const known = p?.machines.find((id) => remote.machines.get(id)?.online) ?? p?.machines[0];
  const baseMachine = fixedMachine || remote.draftMachines.get(key) || held?.machineId || saved?.machineId || first?.machineId || freshConfig(remote, key, known).machineId;
  const machineId = fixedMachine ?? pinned?.machineId ?? local?.machineId ?? baseMachine;
  const machine = remote.machines.get(machineId);
  const agents = agentsOf(machine);
  const remembered = saved?.agent ? agentInfo(saved.agent).agent : undefined;
  const wanted = picked ?? held?.agent ?? remembered ?? first?.agent ?? agents[0];
  // A CLI the machine lacks is not started: the one it has is, and the
  // box says so. What the project remembers stays.
  const agent = pinned ? pinned.req.agent : agents.includes(wanted) ? wanted : agents[0];
  const missing = agent !== wanted && machine?.meta ? wanted : undefined;
  // What a CLI starts with, where no set is stored for it, is how the
  // project's latest session on it ran. Sessions come a page at a time:
  // while the project has ended sessions not fetched and none of this CLI
  // is held, that session may be among them. Pages are fetched until it
  // is here or the project's are all here, and no session starts before,
  // so that it is not started with a machine's defaults.
  const heldEnded = p?.sessions.filter(exited).length ?? 0;
  /** Whether what CLI a would inherit is still to be fetched. */
  const unreadFor = (a: Agent) =>
    !!p && remote.sessionsOpen && p.ended > heldEnded && !local?.sets[a] && !saved?.agents.some((x) => x.agent === a) &&
    // One held past the pages fetched (kept from before a reconnect, or fetched by its ID) may not be the latest.
    !p.sessions.some((s) => agentOf(s) === a && !remote.pastPages(s));
  const unread = unreadFor(agent);
  // The CLI whose set the settings sheet shows is read likewise: changing
  // one of its settings stores the whole set.
  const [watch, setWatch] = useState<Agent>();
  useEffect(() => {
    if (unread || (watch !== undefined && unreadFor(watch))) void remote.loadMoreSessions();
  });
  /** What a session of CLI a starts with: the set chosen on this page,
   * the stored one, else the project's latest session on that CLI. base:
   * as the project has it, leaving out this page's choices. */
  const configFor = (a: Agent, base = false): NewConfig => {
    const at = { machineId: base ? baseMachine : machineId, cwd: key, agent: a };
    if (!base && pinned?.req.agent === a) return { ...at, model: pinned.req.model, effort: pinned.req.effort, mode: pinned.req.mode, standardContext: pinned.req.standardContext };
    if (remote.draftMachines.get(key) === at.machineId && at.machineId !== (saved?.machineId || first?.machineId))
      return { ...at, model: "", effort: "", mode: ApprovalMode.UNSPECIFIED };
    const heldConfig = !base && held?.machineId === at.machineId && held?.by[a];
    if (heldConfig && !local?.sets[a] && !local?.machineId) return { ...heldConfig, ...at };
    const set = (!base && local?.sets[a]) || saved?.agents.find((x) => x.agent === a);
    // A stored set's mode is not read: a new task starts in the machine's default mode.
    if (set) return { ...at, model: set.model, effort: set.effort, mode: !base && local?.sets[a] ? set.mode : ApprovalMode.UNSPECIFIED, standardContext: set.standardContext };
    // Another machine has its own models and defaults.
    if (!base && local?.machineId) return { ...at, model: "", effort: "", mode: ApprovalMode.UNSPECIFIED };
    // A draft begun before this CLI's sessions were fetched held nothing for it.
    const h = held?.by[a] ?? inherit(a);
    return { ...at, model: h?.model ?? "", effort: h?.effort ?? "", mode: ApprovalMode.UNSPECIFIED, from: h?.from };
  };
  const configured = configFor(agent);
  const info = useMachineInfo(remote, configured.machineId, configured.agent);
  const model = findModel(info.models?.models, configured.model || defaultsOf(info.settings, agent).model || info.models?.defaultModel || "");
  const unavailable = !pinned && !!info.models && ((!!configured.model && !model) || (!!configured.effort && !!model && !model.efforts.includes(configured.effort)));
  const cfg = unavailable ? { ...configured, model: "", effort: "" } : configured;
  const configFallback = unavailable ? "所选模型或推理强度在这台机器上不可用，这次用机器的默认值" : "";
  // The request may have been sent from another page: this one is told
  // when it ends through the Remote, like the page that sent it.
  const busy = start.running;
  const [error, setError] = useState("");
  const [configError, setConfigError] = useState("");
  // The project's settings do not keep an approval mode: a scheduled task's choice lives in this page only.
  const save = (settings: { machineId?: string; agent?: Agent; sets?: AgentSet[] }, keepExisting = false) =>
    remote.changePrefs("settings", key, { id: crypto.randomUUID(), kind: "settings" as const, path: key, at: Date.now(), settings: settings.sets ? { ...settings, sets: settings.sets.map((x) => ({ ...x, mode: ApprovalMode.UNSPECIFIED })) } : settings, keepExisting }).then((ok) => setConfigError(ok ? "" : "项目设置没有保存，请重试"));
  // Settings not stored yet begin with where and on which CLI it starts now.
  const whole = () => (saved ? {} : { machineId: baseMachine, agent });
  /** Changes one CLI's set, or the machine: config is configFor of a CLI
   * with the change. Another machine clears every CLI's set, its models
   * and defaults being its own. once: only for this start, nothing saved. */
  const setCfg = (config: NewConfig, once = false) => {
    if (config.machineId !== machineId) {
      remote.draftMachines.delete(key);
      const sets = Object.fromEntries(AGENTS.map((e) => [e.agent, setOf({ ...config, agent: e.agent })])) as Record<Agent, AgentSet>;
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
  // A failure shown for one project is not the next one's.
  useEffect(() => { setError(""); setConfigError(""); }, [key]);

  // What a start would send. While one's outcome is unknown, only what the
  // user changed counts: the message as sent (trimmed), and the settings
  // only if they chose others, cfg being that request's own until then.
  const payloadFor = (prompt: string): StartPayload => ({
    machineId: cfg.machineId,
    req: {
      cwd: cfg.cwd,
      prompt: prompt.trim(),
      model: cfg.model,
      effort: cfg.effort,
      // Unspecified: the machine resolves its own default. Only a request
      // already sent keeps the mode it went with.
      mode: cfg.mode,
      agent: cfg.agent,
      standardContext: agentInfo(cfg.agent).standardContextOption && !!cfg.standardContext,
      // The images are part of what is started: other images, another start.
      imageIds: tray.imageIds(),
      fileIds: tray.fileIds(),
    },
  });
  /** Whether sending prompt now would be a different start from the one
   * whose outcome is unknown. */
  const changes = (prompt: string): boolean => {
    const pending = current.payload;
    if (!pending) return false;
    // Renewing an upload changes only its transport ID, never the unknown request.
    const wanted = payloadFor(prompt);
    wanted.req.imageIds = pending.req.imageIds;
    wanted.req.fileIds = pending.req.fileIds;
    return JSON.stringify(wanted) !== JSON.stringify(pending) || start.images.length !== tray.items.length ||
      start.images.some((im, i) => im.key !== tray.items[i].key || im.prepared !== tray.items[i].prepared);
  };
  // Images for the first message go to the chosen machine; a new machine
  // gets them again (the tray keeps them prepared).
  const tray = useTray(remote, "new:" + key, cfg.machineId, maxImages(machine));
  const [, redraw] = useState(0);
  const create = async (prompt: string, terminal = false) => {
    // One request at a time, whichever page sent the one on its way.
    if (start.running) return false;
    // A different start gives up the unknown one; the same start, however
    // it was typed, is retried as the same request.
    if (changes(prompt)) abandon();
    // While an earlier attempt's outcome is unknown, only it is retried.
    const retry = current.frozen;
    const sent = current.next(payloadFor(prompt));
    if (!retry) { start.page = { local, picked: pick, latched: held }; start.images = tray.items.map((i) => ({ key: i.key, prepared: i.prepared })); }
    const { payload, slot } = sent;
    const open = fromHere(go);
    start.running = true;
    remote.touch();
    setError("");
    try {
      const res = await remote.machineCommand(payload.machineId, { case: "createSession", value: payload.req }, slot);
      // Only this request's own answer settles it.
      current.settle(undefined, sent);
      if (res.kind.case !== "createSession" || !res.kind.value.session) throw new Error("意外的回复");
      const sid = res.kind.value.session.id;
      if (payload.req.prompt) remote.prompts.set(sid, payload.req.prompt);
      remote.created.add(sid);
      if (!terminal) {
        tray.clear();
        // A different message typed while this request was pending belongs
        // to the next session, even if its page has since been left.
        if (remote.drafts.get(draftKey)?.trim() === payload.req.prompt) remote.drafts.delete(draftKey);
      }
      if (terminal) remote.termFirst.add(sid);
      remote.draftMachines.delete(key);
      // The first session establishes the project's settings, and each
      // CLI's first one its set; later starts only move the remembered
      // CLI. A machine picked for one start is not what gets stored.
      const used = payload.req.agent;
      const asStored = configFor(used, true);
      if (!saved) void save({ machineId: asStored.machineId, agent: missing ? undefined : used, sets: [setOf(local?.once ? asStored : cfg)] }, true);
      else {
        const patch: { agent?: Agent; sets?: AgentSet[] } = {};
        if (!missing && remembered !== used) patch.agent = used;
        if (!saved.agents.some((x) => x.agent === used)) patch.sets = [setOf(local?.once ? asStored : cfg)];
        if (patch.agent !== undefined || patch.sets) void save(patch, true);
      }
      // The draft went with it, on every page that shows this project.
      for (const x of Object.values(start.page ?? {})) if (x) start.spent.add(x);
      start.count++;
      // The session takes the new-task page's place in the history: back goes to where the page was opened from.
      open("s/" + sid, true);
      return true;
    } catch (e) {
      current.settle(e, sent);
      setError(errorText(e));
      return false;
    } finally {
      if (!current.frozen) start.images = [];
      start.running = false;
      remote.touch();
      redraw((n) => n + 1);
    }
  };
  // A project removed from the list (here or on another client) keeps its
  // draft, but a new session waits until it is restored.
  const blocked = isHidden(remote.projectPrefs(), key) ? "这个项目已从列表移除，先重新添加" : remote.listed && !machine?.online ? `${machineName(machine)} 不在线` : remote.conn !== "online" ? "和服务器断线了" : unread ? "正在读取这个项目上次的设置…" : (configured.model || configured.effort) && info.loading ? "正在读取模型列表…" : "";
  // Giving up an unknown start: the user checked, or changed what to send.
  const abandon = () => {
    adopt();
    current.abandon();
    start.images = [];
    setError("");
    redraw((n) => n + 1);
  };
  /** Holds the launch settings as they are now, once a draft begins. */
  const hold = () => setLatched({ key, machineId, agent: wanted, by: Object.fromEntries(AGENTS.map((e) => [e.agent, unreadFor(e.agent) ? undefined : configFor(e.agent)])) as Record<Agent, NewConfig | undefined> });
  // A draft begun while its CLI's settings were still being fetched is
  // held as soon as they are here.
  // The draft that began so: the one for the project's next session.
  const draft = `${start.count}:${key}`;
  const [begun, setBegun] = useState<string>();
  const latch = () => {
    if (held) return;
    if (unread) setBegun(draft);
    else hold();
  };
  useEffect(() => {
    if (begun === draft && !unread && !held) hold();
    // What the draft's CLI inherits was left open when the draft began:
    // once known, it is held like the rest. A pending request names its
    // own CLI's settings, so this is only ever another CLI's, picked since.
    else if (held && held.by[agent] === undefined && !unread && inherit(agent)) setLatched({ ...held, by: { ...held.by, [agent]: inherit(agent) } });
  });
  const dm = defaultModeOf(machine, cfg.agent);
  const modeText = modeTextOf(cfg, machine);
  const parts = describeParts(cfg, machine, info);
  // The machine is named only where there is more than one to mean.
  // The model only once one is known: a CLI with none chosen yet says nothing of it.
  // The model's placeholder is written too, so the capsule is never empty.
  const summary: Summary = { agent: agentName(cfg.agent), machine: remote.machines.size > 1 ? machineName(machine) : "", model: parts.label };
  return {
    remote,
    go,
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
    configFallback,
    latch,
    unreadFor,
    setWatch,
    // The CLI's last settings are still being fetched: no session starts before.
    reading: unread && remote.conn === "online",
    issue: newConfigIssue(cfg.agent, parts.slug, cfg.mode || dm.mode, info.models?.models),
    // The same, told apart for a new task, whose mode is the machine's default.
    issueKind: autoIssueKind(cfg.agent, parts.slug, cfg.mode || dm.mode, info.models?.models),
    parts,
    tray,
    attachWhy: attachBlocked(remote, machine),
    machine,
    info,
    summary,
    modeText,
    create,
    busy,
    error: error || (current.frozen && !busy ? "上次的启动结果还没有确认" : ""),
    text,
    setText,
    key,
    configError,
    blocked,
  };
}

export type NewSession = ReturnType<typeof useNewSession>;

/** 新任务 (design-system §6 新任务): a sentence of where and with which
 * CLI, 「在 [项目] 用 [CLI]」, the capsules opening what they pick (the CLI
 * one only where the machine has more than one); the task under it; and
 * a row of ＋ and the summary of the settings, which opens the project's
 * settings. On a phone it is the whole-page sheet, its close at the top
 * left and send at the top right; on a wide window it is the main area,
 * the summary and send under the task. */
export function NewBox({
  ns,
  name,
  autoFocus,
  variant = "sheet",
  fixed,
  onPickProject,
  onClose,
  notice,
  settings = false,
  onDraft,
}: {
  ns: NewSession;
  name: string;
  autoFocus?: boolean;
  variant?: "sheet" | "start";
  /** The project is set by the scope: the sentence names it as words. */
  fixed?: boolean;
  onPickProject?: () => void;
  onClose?: () => void;
  /** A banner of the project's own, above the sentence. */
  notice?: ReactNode;
  /** Opened with the project's settings showing. */
  settings?: boolean;
  onDraft?: () => void;
}) {
  const { text, setText } = ns;
  const [config, setConfig] = useState<"" | "main" | Pick>(settings ? "main" : "");
  const [choosing, setChoosing] = useState(false);
  const afterKeyboard = useKeyboardDismiss();
  const cliRef = useRef<HTMLButtonElement>(null);
  const composerRef = useRef<HTMLDivElement>(null);
  const boundaryRef = useRef<HTMLDivElement>(null);
  const phone = variant === "sheet";
  const target = ns.key + "/" + ns.cfg.machineId + "/" + ns.cfg.agent;
  const previousTarget = useRef(target);
  useEffect(() => {
    if (!phone || previousTarget.current === target) return;
    previousTarget.current = target;
    setChoosing(false);
    setConfig("");
  }, [target, phone]);
  const toggleModels = () => {
    if (phone) setChoosing(false);
    setConfig((c) => phone && (c === "main" || c === "model") ? "" : "main");
  };
  const input = useRef<HTMLTextAreaElement>(null);
  useAutoGrow(input, text, 240);
  useEffect(() => {
    if (autoFocus) input.current?.focus();
  }, [autoFocus]);
  const cannot = ns.busy || !!ns.blocked || !!ns.issue;
  const submit = async () => {
    const prompt = text;
    // An unknown start that expired is not sent again, nor replaced by the
    // same start under a new request. Chosen images must all be on the
    // machine first, and there is no session without a first message: the
    // send button is disabled for these, but 重试 comes here too.
    if (cannot || (ns.expired && !ns.changes(prompt))) return;
    ns.tray.refreshExpired();
    if (ns.tray.items.length ? !ns.tray.ready() : !prompt.trim()) return;
    // Retrying an unknown start sends it as it was.
    await ns.create(prompt.trim());
  };
  const keys = sendKeys(usePointerFine());
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
          {moved && tray.busy() && <Banner>已换到 {moved}，附件正在重新上传</Banner>}
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
      className={"ask" + (text ? " typed" : "")}
      rows={1}
      value={text}
      placeholder="要做什么？"
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
  // Before the lists are in, no computer is known to be off.
  const offline = ns.remote.listed && !ns.machine?.online;
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
        <Banner kind={text ? "warn" : ""} action="换机器" onAction={() => setConfig("machine")}>
          {machineName(ns.machine)} {text ? "已离线，任务还没开始。草稿已保留，换一台在线的机器吧。" : "不在线"}
        </Banner>
      )}
      {ns.reading && !offline && <Banner>正在读取这个项目上次的设置…</Banner>}
      {ns.missing !== undefined && !offline && (
        <Banner action="换机器" onAction={() => setConfig("machine")}>
          {machineName(ns.machine)} 没有 {agentName(ns.missing)}，这次用 {agentName(ns.cfg.agent)}
        </Banner>
      )}
      {ns.configFallback && <Banner>{ns.configFallback}</Banner>}
      {ns.issueKind === "model" && (
        <div style={{ display: "contents" }} data-composer-picker={phone ? "model" : undefined} onPointerDown={phone ? keepComposerFocus : undefined} onMouseDown={phone ? keepComposerFocus : undefined}>
          <Banner kind="warn" action="换模型" onAction={() => { setChoosing(false); setConfig("model"); }}>
            这个模型没有自动模式，而 {machineName(ns.machine)} 的新项目默认审批模式是自动。换一个模型，或到「设置 › 这台电脑」改默认
          </Banner>
        </div>
      )}
      {ns.issueKind === "account" && (
        <Banner kind="warn" action="去改默认" onAction={() => ns.go("m/" + ns.cfg.machineId)}>
          当前账户不支持自动模式，而 {machineName(ns.machine)} 的新项目默认审批模式是自动。到「设置 › 这台电脑」改默认后再开始
        </Banner>
      )}
      {ns.configError && <Banner kind="warn" action="重试保存" onAction={() => ns.setCfg(ns.cfg)}>{ns.configError}</Banner>}
    </>
  );
  const banners = (notice || error || notes) && (
    <div className="nt-banners">
      {notice}
      {error}
      {notes}
    </div>
  );
  const send = (
    <button className={"snd" + (disabled ? " off" : "")} disabled={disabled} onClick={() => void submit()} aria-label="开始新会话">
      <ArrowUp />
    </button>
  );
  // The draft stays when the CLI is switched.
  const many = ns.agents.length > 1;
  const cli = many && (
    <button ref={cliRef} className="tok cli" disabled={ns.busy} aria-haspopup="dialog" aria-label={`CLI：${agentName(ns.cfg.agent)}`} aria-expanded={choosing}
      data-composer-picker={phone ? "cli" : undefined}
      onPointerDown={phone ? keepComposerFocus : undefined} onMouseDown={phone ? keepComposerFocus : undefined}
      onClick={() => { if (phone) setConfig(""); setChoosing((v) => phone ? !v : true); }}>
      <AgentMark agent={ns.cfg.agent} named />
      <span>{agentName(ns.cfg.agent)}</span>
      <ChevronDown />
    </button>
  );
  const mach = ns.summary.machine && (
    <button className="tok m" disabled={ns.busy} aria-haspopup="dialog" aria-label={`机器：${ns.summary.machine}`} onClick={() => afterKeyboard(() => setConfig("machine"))}>
      <span>{ns.summary.machine}</span>
      <ChevronDown />
    </button>
  );
  const proj =
    fixed || !onPickProject ? (
      <b>{name}</b>
    ) : (
      <button className="tok p" onClick={() => afterKeyboard(onPickProject)} aria-label={`项目：${name}`}>
        <Avatar name={name} size="sm" />
        <span>{name}</span>
        <ChevronDown />
      </button>
    );
  const sentence = (
    <div className="sent">
      {/* Where it runs reads as one place: the machine, then the project on it. */}
      {ns.summary.machine && (
        <span className="g">
          在
          {mach}
        </span>
      )}
      <span className="g">
        {ns.summary.machine ? "的" : "在"}
        {proj}
      </span>
      {many && (
        <span className="g">
          用
          {cli}
        </span>
      )}
    </div>
  );
  const busyNote = ns.busy && <div className="nt-note">正在 {machineName(ns.machine)} 上启动…</div>;
  const summary = <ConfigSummary summary={ns.summary} bare={many} onClick={toggleModels}
    data-composer-picker={phone ? "model" : undefined} aria-expanded={config === "main" || config === "model"}
    onPointerDown={phone ? keepComposerFocus : undefined} onMouseDown={phone ? keepComposerFocus : undefined} />;
  const attach = <AttachButton tray={tray} blocked={ns.attachWhy} onBlocked={setToast} />;
  const body =
    variant === "sheet" ? (
      <div ref={boundaryRef} className={"ntask" + (drop.over ? " drop" : "")} {...drop.props} role="group" aria-label={`${name} · 新会话`}>
        <header className="nt-hd">
          <button className="fbtn" aria-label="关闭" onClick={onClose}>
            <X />
          </button>
          {/* On a phone the title says where: the project, and under it the machine. */}
          <div className="nt-where">
            {proj}
            {mach}
          </div>
          <span className="nt-pad" />
        </header>
        <div className="nt-body">
          {banners}
          {busyNote}
        </div>
        {/* The same box as a session's and the wide page's, above the keyboard. */}
        <div className="pbox" ref={composerRef}>
          {field}
          {trayView}
          <div className="acc">
            {attach}
            {cli}
            {summary}
            <span className="grow" />
            {send}
          </div>
        </div>
      </div>
    ) : (
      <div className={"wstart" + (drop.over ? " drop" : "")} {...drop.props} role="group" aria-label={`${name} · 新会话`}>
        {banners}
        {sentence}
        {/* In a wide window the task is written in a box of its own: on the bare page nothing showed where to type. */}
        <div className="wbox">
          {field}
          {trayView}
          <div className="acc">
            {attach}
            {summary}
            {keys && <span className="kh">{keys} 开始</span>}
            {send}
          </div>
        </div>
        {busyNote}
      </div>
    );
  const cliChoices = (
    <div className="list" role="menu">
      {ns.agents.map((a) => (
        <button
          key={a}
          className="mi"
          role="menuitemradio"
          aria-checked={a === ns.cfg.agent}
          onClick={() => {
            setChoosing(false);
            if (a !== ns.cfg.agent) ns.setAgent(a);
          }}
        >
          <span className="mt"><AgentMark agent={a} named />{agentName(a)}</span>
          {a === ns.cfg.agent && <span className="ck"><Check /></span>}
        </button>
      ))}
    </div>
  );
  return (
    <>
      {body}
      {choosing && (phone ? (
        <ComposerPicker anchor={cliRef} boundary={boundaryRef} onClose={() => setChoosing(false)}>{cliChoices}</ComposerPicker>
      ) : (
        <Sheet onClose={() => setChoosing(false)} title="用哪个 CLI" anchor={cliRef}>{cliChoices}</Sheet>
      ))}
      {config && <ConfigSheet ns={ns} start={config === "main" ? "" : (config as Pick)} onClose={() => setConfig("")}
        composer={phone && config !== "machine" ? { anchor: composerRef, boundary: boundaryRef } : undefined} />}
    </>
  );
}

/** The pages of the settings; 审批模式 is a scheduled task's only. */
type Pick = "" | "machine" | "model" | "mode";

/** 项目设置: the machine (where the account has more than one), and the
 * model of the CLI picked next to the box; a new task has no approval mode
 * of its own. Another CLI's set is reached by picking that CLI there. With
 * one machine the model is all there is, so its page opens at once.
 * scheduled: a scheduled task's settings, which keep the project block,
 * the 权限 row and the project's actions. */
export function ConfigSheet({ ns, onClose, start: asked = "", scheduled = false, lockMachine = false, composer }: { composer?: ComposerAnchor; ns: NewSession; onClose: () => void; start?: Pick; scheduled?: boolean; lockMachine?: boolean }) {
  const taskEditing = scheduled && lockMachine;
  const { remote, p, machine } = ns;
  // A new task picks its machine in the sentence, next to the project: its settings are the model's page and nothing else.
  const lone = !scheduled;
  const start = asked || (lone ? "model" : "");
  const [pick, setPick] = useState<Pick>(start);
  const [removing, setRemoving] = useState(false);
  const [allModels, setAllModels] = useState(false);
  // Opened right at one setting: done with it is done with the sheet.
  const back2 = () => (start ? onClose() : setPick(""));
  // The CLI was picked next to the box: the sheet is that CLI's settings and no other's.
  const agent = ns.cfg.agent;
  const cfg = ns.configFor(agent);
  const info = ns.info;
  const parts = describeParts(cfg, machine, info);
  // How this CLI last ran in the project may be on a page of sessions not
  // fetched yet. Until it is here its set is not shown as if known, nor
  // changed: a change stores the whole set.
  const open = ns.unreadFor(agent);
  const { setWatch } = ns;
  useEffect(() => {
    setWatch(agent);
    return () => setWatch(undefined);
  }, [agent, setWatch]);
  const set = (patch: Partial<NewConfig>) => {
    if (!open) ns.setCfg({ ...cfg, ...patch });
  };
  const def = defaultsOf(info.settings, cfg.agent);
  useEffect(() => {
    if (pick === "model") info.retry();
  }, [pick]); // Refresh the native CLI list each time the model page opens.
  // Machines this project has used; a new project can use any.
  const choices: MachineView[] = (p && machine?.online ? p.machines : [...remote.machines.keys()]).map((id) => remote.machines.get(id)).filter((m): m is MachineView => !!m);
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
            <StateMark kind={m.online ? "online" : "off"} />
            <div className="mt">
              {m.id === cfg.machineId || !m.online ? machineName(m) : <b>改用 {machineName(m)}</b>}
              {!m.online ? (
                <div className="md">{m.lastSeenMs ? ago(m.lastSeenMs) + "在线，" : ""}现在不能新建</div>
              ) : (
                <div className="md">
                  {`已装 ${machineAgents(m).map(agentName).join("、")}`}
                  {m.id !== cfg.machineId && !machineAgents(m).includes(ns.cfg.agent) && (
                    `；这台机器没有 ${agentName(ns.cfg.agent)}，会改用 ${agentName(switchMachine(cfg, m).agent)}`
                  )}
                </div>
              )}
            </div>
            {m.id === cfg.machineId && <span className="ck"><Check /></span>}
          </button>
        ))}
      </div>
    );
  else if (pick === "model") {
    const chosen = findModel(info.models?.models, cfg.model || def.model || info.models?.defaultModel || "");
    // A long list shows three and the picked model, as the session sheet
    // does, so 推理强度 below it stays in sight.
    const list = info.models?.models ?? [];
    const picked = cfg.model ? findModel(list, cfg.model) : undefined;
    const few = allModels || list.length <= 5;
    const shown = few ? list : [...list.slice(0, 3), ...(picked && !list.slice(0, 3).includes(picked) ? [picked] : [])];
    body = (
      <>
        {info.error && (
          <Banner kind="warn" action="重试" onAction={info.retry}>
            没能读取模型列表，会用 {agentName(cfg.agent)} 自己的默认模型
          </Banner>
        )}
        {!info.models && !info.error && <div className="hint top">读取 {machineName(machine)} 的设置…</div>}
        <div className="list">
          <button className="mi" onClick={() => set({ model: "", effort: "" })}>
            <div className="mt">
              默认模型<div className="md">用 {machineName(machine)} 的新项目默认设置</div>
            </div>
            {!cfg.model && <span className="ck"><Check /></span>}
          </button>
          {shown.map((m) => (
            <button key={m.slug} className="mi" onClick={() => set({ model: m.slug, effort: "" })}>
              <div className="mt">
                {m.displayName || m.slug}
                {m.efforts.length === 1 && <div className="md">推理强度固定为 {m.efforts[0]}</div>}
              </div>
              {m === picked && <span className="ck"><Check /></span>}
            </button>
          ))}
          {!few && (
            <button className="mi link-row" onClick={() => setAllModels(true)}>
              <span className="mt">全部模型</span>
              <ChevronRight className="chev" />
            </button>
          )}
        </div>
        {chosen && chosen.efforts.length > 0 && (
          <>
            <div className="fl">推理强度</div>
            <Segmented
              className="full wrap"
              label="推理强度"
              value={cfg.effort || (cfg.model ? chosen.defaultEffort : def.effort || chosen.defaultEffort)}
              onChange={(e) => set({ effort: e })}
              options={chosen.efforts.map((e) => ({ value: e, label: e }))}
            />
          </>
        )}
        {agentInfo(cfg.agent).standardContextOption && !scheduled && (
          <>
            <div className="fl">上下文窗口</div>
            {parts.fixedWindow ? (
              <div className="hint top">标准 200k · 这个模型只有标准窗口</div>
            ) : (
              <Segmented
                className="full"
                label="上下文窗口"
                value={cfg.standardContext ? "std" : "1m"}
                onChange={(v) => set({ standardContext: v === "std" })}
                options={[
                  { value: "1m", label: "1M" },
                  { value: "std", label: "标准 200k" },
                ]}
              />
            )}
          </>
        )}
      </>
    );
  } else if (pick === "mode") {
    // A model without the auto mode (Claude Code's Haiku 4.5, or all
    // Antigravity models) cannot start with it; the CLI's own list says
    // which those are.
    const chosen = cfg.model || def.model || info.models?.defaultModel || "";
    const allNoAuto = agentInfo(cfg.agent).autoMayBeUnavailable && allAutoUnavailable(info.models?.models);
    const noAuto = allNoAuto || (agentInfo(cfg.agent).autoMayBeUnavailable && autoUnavailable(info.models?.models, chosen));
    body = (
      <div className="list">
        {[
          { mode: ApprovalMode.UNSPECIFIED, name: "默认", desc: `跟随 ${agentName(cfg.agent)} 自己的配置` },
          { mode: ApprovalMode.ASK, name: "询问", desc: "需要审批时由你确认，超时拒绝" },
          noAuto
            ? { mode: ApprovalMode.AUTO, name: "自动", desc: allNoAuto ? "当前账户不支持自动模式" : "当前模型没有自动模式", off: true }
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
              {m.name}
              <div className="md">{m.desc}</div>
            </div>
            {m.mode === cfg.mode && <span className="ck"><Check /></span>}
          </button>
        ))}
      </div>
    );
  }
  const titles: Record<Pick, string> = { "": taskEditing ? "任务设置" : "项目设置", machine: "机器", model: "模型", mode: taskEditing ? "权限" : "审批模式" };
  const many = ns.agents.length > 1;
  if (open && pick && pick !== "machine") body = <div className="hint top">正在读取这个项目上次的设置…</div>;
  const content = (
    <>
      {body ?? (
        <>
          {scheduled && (
            <div className="list">
              <div className="mi pid">
                <Avatar name={projectName(cfg.cwd)} size="sm" />
                <div className="mt">
                  {projectName(cfg.cwd)}
                  <div className="md mono"><PathText path={shortPath(cfg.cwd)} /></div>
                </div>
              </div>
            </div>
          )}
          {scheduled && (
            <>
              <div className="fl">新会话</div>
              <div className="list">
                <button className="mi" disabled={lockMachine} onClick={() => setPick("machine")}>
                  <span className="mt">机器</span>
                  <span className="mv">
                    <StateMark kind={machine?.online || !remote.listed ? "online" : "off"} />
                    {machineName(machine)}
                  </span>
                  <ChevronRight className="chev" />
                </button>
              </div>
            </>
          )}
          {many && <div className="fl">{agentName(agent)}</div>}
          {open ? (
            <div className="hint top">正在读取这个项目上次的设置…</div>
          ) : (
            <div className="list">
              <button className="mi" onClick={() => setPick("model")}>
                <span className="mt">模型</span>
                <span className="mv">{info.loading ? `读取 ${machineName(machine)} 的设置…` : parts.model}</span>
                <ChevronRight className="chev" />
              </button>
              {scheduled && (
                <button className="mi" onClick={() => setPick("mode")}>
                  <span className="mt">{taskEditing ? "权限" : "审批"}</span>
                  <span className={"mv" + (modeTextOf(cfg, machine) === modeName(ApprovalMode.FULL_ACCESS) ? " red" : "")}>{modeTextOf(cfg, machine)}</span>
                  <ChevronRight className="chev" />
                </button>
              )}
            </div>
          )}
          {info.error && (
            <Banner kind="warn" action="重试" onAction={info.retry}>
              没能读取模型列表，会用 {agentName(cfg.agent)} 自己的默认模型
            </Banner>
          )}
          {scheduled && <div className="hint">{taskEditing ? "只修改这个任务；已开出的对话保留原来的设置。" : <>{many ? "每个 CLI 各存一套，新建时用哪个在句子里选。" : ""}只影响之后新建的会话，进行中的不变；在你的设备之间同步。</>}</div>}
          {scheduled && !taskEditing && (
            <div className="list">
              <button className="mi link-row" disabled={!!prefsBlocked(remote)} onClick={pinAction(remote, cfg.cwd).run}>
                <span className="mt">{pinAction(remote, cfg.cwd).label}</span>
              </button>
              <button className="mi danger" disabled={!!prefsBlocked(remote)} onClick={() => setRemoving(true)}>
                <span className="mt">移除项目…</span>
              </button>
            </div>
          )}
          {scheduled && !taskEditing && removing && (
            <RemoveProjectAlert
              remote={remote}
              path={cfg.cwd}
              onCancel={() => setRemoving(false)}
              onDone={() => {
                onClose();
                ns.go("");
              }}
            />
          )}
        </>
      )}
    </>
  );
  return composer ? (
    <ComposerPicker {...composer} title="模型" onClose={onClose}>{content}</ComposerPicker>
  ) : (
    <Sheet onClose={onClose} title={titles[pick]} action={{ label: "完成", onClick: onClose }} back={pick && !start ? back2 : undefined} anchor={anchorOf(!scheduled && asked === "machine" ? ".wstart .sent .tok.m" : ".wstart .acc .project-summary")} className="with-head">{content}</Sheet>
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
  // The list fades into the sheet at an edge that more of it lies past (design-system §4 材质).
  const listRef = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState({ up: false, down: false });
  const measure = () => {
    const el = listRef.current;
    if (!el) return;
    const up = el.scrollTop > 0;
    const down = el.scrollTop + el.clientHeight < el.scrollHeight - 1;
    setEdges((e) => (e.up === up && e.down === down ? e : { up, down }));
  };
  useEffect(measure, [dir, loading]);
  const parts = dir ? pathCrumbs(dir.path) : [];
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
      <div className={"list dirs" + (edges.up ? " fade-top" : "") + (edges.down ? " fade-bottom" : "")} ref={listRef} onScroll={measure}>
        {dir?.parent && (
          <button className="mi" disabled={!!loading} onClick={() => void open(dir.parent)}>
            <ChevronLeft className="lead" />
            <div className="mt">上一级</div>
          </button>
        )}
        {dir?.dirs.map((d) => (
          <button key={d} className="mi" disabled={!!loading} onClick={() => void open(dir.path.replace(/\/$/, "") + "/" + d)}>
            <div className="mt">{d}</div>
            <ChevronRight className="chev" />
          </button>
        ))}
        {dir && dir.dirs.length === 0 && <div className="mi note">没有子目录</div>}
        {!dir && !error && <div className="mi note">读取中…</div>}
      </div>
      <div className="btns sheet-foot">
        <button className="btn pri" disabled={!dir || !!loading} onClick={() => dir && onPick(dir.path)}>
          {label(name)}
        </button>
      </div>
    </>
  );
}

/** 选项目: the project list, each row ending in ›, 新项目… at the end. */
export function ProjectPicker({ remote, onPick, onNew, onClose, title = "选项目" }: { remote: Remote; onPick: (key: string) => void; onNew: () => void; onClose: () => void; title?: string }) {
  return (
    <Sheet onClose={onClose} title={title} action={{ label: "取消", onClick: onClose }} anchor={anchorOf(".wstart .sent .tok.p")}>
      <ProjectList remote={remote} onNew={onNew} item={(p) => <ProjectItem key={p.key} remote={remote} p={p} onClick={() => onPick(p.key)} end={<ChevronRight className="chev" />} />} />
    </Sheet>
  );
}

/** A new project: a machine, then a folder on it. */
export function NewProjectSheet({ remote, onBack, onDone, onClose, fixedMachine }: { remote: Remote; onBack?: () => void; onDone: (machineId: string, path: string) => void; onClose: () => void; fixedMachine?: string }) {
  const machines = [...remote.machines.values()].filter(m => !fixedMachine || m.id === fixedMachine).sort((a, b) => Number(b.online) - Number(a.online) || machineName(a).localeCompare(machineName(b)));
  const [machineId, setMachineId] = useState(machines.find((m) => m.online)?.id ?? "");
  const m = remote.machines.get(machineId);
  return (
    <Sheet onClose={onClose} title={fixedMachine ? "项目 · 选目录" : "新项目 · 选目录"} back={onBack}>
      {remote.machines.size > 1 && <Segmented
        className="full machines"
        label="机器"
        value={machineId || undefined}
        onChange={setMachineId}
        options={machines.map((x) => ({
          value: x.id,
          disabled: !x.online || !!fixedMachine,
          label: (
            <>
              <StateMark kind={x.online ? "online" : "off"} />
              {machineName(x)}
            </>
          ),
        }))}
      />}
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
  const listed = projects(remote.sessions.values(), remote.machines.values(), remote.projectPrefs().settings);
  const pick = listed.length > 0 && !start;
  const [step, setStep] = useState<"project" | "new">(pick ? "project" : "new");
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
        onBack={pick ? () => setStep("project") : undefined}
        onClose={onClose}
        onDone={(machineId, path) => {
          addProject(remote, machineId, path);
          open(path);
        }}
      />
    );
  return <ProjectPicker remote={remote} onPick={open} onNew={() => setStep("new")} onClose={onClose} />;
}

/** What the new-session settings say: the machine is named in the
 * sentence (where the account has more than one), the CLI and the model in
 * the summary. */
export type Summary = { agent: string; machine: string; model: string };

/** The settings of a new session in one capsule, opening the project's
 * settings (design-system §6 新任务): the model; the effort and the window
 * are in the settings. A new task has no approval mode to show, and its
 * machine is in the sentence. The CLI is written at the start only when
 * nothing else names it (bare: the sentence already does). */
export function ConfigSummary({ summary, bare, ...button }: { summary: { agent: string; model: string }; bare?: boolean } & ComponentProps<"button">) {
  const { agent, model } = summary;
  const head = bare ? "" : agent;
  const sep = " · ";
  const line = [head, model].filter(Boolean).join(sep);
  return (
    <button className="tok project-summary" aria-label={`项目设置：${line}`} {...button}>
      {head && <span className="fx">{head}</span>}
      {/* The model brings the dot before it: a part cut short never leaves one hanging at its end. */}
      {model && <span className="mdl">{(head ? sep : "") + model}</span>}
      <ChevronRight />
    </button>
  );
}
