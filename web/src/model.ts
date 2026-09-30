// View models derived from the decrypted state: projects, session states
// by priority, what is docked above the input, how a sent message is
// doing, and how the chat timeline groups events. Everything here is pure,
// so the rules in docs/design/client-v1.html can be tested directly.

import { timestampMs } from "@bufbuild/protobuf/wkt";
import type { ApprovalRequested, Event, PlanProposed, QuestionAsked, TokenUsage, ToolCall } from "./gen/remoteagent/v1/event_pb";
import { Agent, ApprovalDecision, ApprovalMode, InputStatus, SessionStatus, ToolKind, TurnOutcome } from "./gen/remoteagent/v1/event_pb";
import type { Session } from "./gen/remoteagent/v1/local_pb";
import type { MachineMeta } from "./gen/remoteagent/v1/remote_pb";

// The parts of Remote's views the rules need; kept structural so tests can
// build them without a Remote.
export interface MachineLike {
  id: string;
  online: boolean;
  lastSeenMs: bigint;
  meta?: MachineMeta;
}

export interface SessionLike {
  id: string;
  machineId: string;
  info?: Session;
  updatedMs: bigint;
}

// --- agents ---

/** The CLI a session runs; sessions from before Claude Code are Codex's. */
export function agentOf(s?: { info?: Session } | Session): Agent {
  const a = s && "info" in s ? s.info?.agent : (s as Session | undefined)?.agent;
  return a === Agent.CLAUDE ? Agent.CLAUDE : Agent.CODEX;
}

export function agentName(a: Agent): string {
  return a === Agent.CLAUDE ? "Claude Code" : "Codex";
}

/** The CLIs a machine can start. Daemons older than Claude Code support
 * list none and run only Codex. */
export function machineAgents(m?: MachineLike): Agent[] {
  const list = m?.meta?.agents.map((a) => (a.agent === Agent.CLAUDE ? Agent.CLAUDE : Agent.CODEX)) ?? [];
  const uniq = [...new Set(list)];
  return uniq.length ? uniq : [Agent.CODEX];
}

// --- projects ---

export interface Project<S extends SessionLike = SessionLike> {
  key: string;
  name: string;
  sessions: S[]; // most recent first
  updatedMs: number;
  machines: string[]; // machine IDs, most recent first
}

/** The project a session belongs to: its working directory's absolute
 * path, the same on every machine. */
export function projectKey(s: SessionLike): string {
  return s.info?.cwd || "";
}

/** A project's name: the last folder of its path. */
export function projectName(key: string): string {
  return key.split("/").filter(Boolean).pop() || "未知项目";
}

export function projects<S extends SessionLike>(sessions: Iterable<S>): Project<S>[] {
  const byKey = new Map<string, Project<S>>();
  const sorted = [...sessions].sort((a, b) => Number(b.updatedMs - a.updatedMs));
  for (const s of sorted) {
    const key = projectKey(s);
    let p = byKey.get(key);
    if (!p) {
      p = { key, name: projectName(key), sessions: [], updatedMs: 0, machines: [] };
      byKey.set(key, p);
    }
    p.sessions.push(s);
    p.updatedMs = Math.max(p.updatedMs, Number(s.updatedMs));
    if (!p.machines.includes(s.machineId)) p.machines.push(s.machineId);
  }
  return [...byKey.values()].sort((a, b) => b.updatedMs - a.updatedMs);
}

export function machineName(m?: MachineLike): string {
  return m?.meta?.hostname?.replace(/\.local$/, "") || (m ? m.id.slice(0, 8) : "未知电脑");
}

/** Avatar color class, stable per project. */
export function avatarClass(key: string): string {
  let h = 0;
  for (const c of key) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return "c" + ((h % 4) + 1);
}

// --- session state by priority ---

export type Tone = "run" | "wait" | "idle" | "off";

/** Session states from highest priority to lowest: lists, the navigation
 * bar and the bottom of a session all show only the highest one. */
export type StateKind = "ended" | "offline" | "approval" | "question" | "plan" | "popup" | "starting" | "running" | "idle";

export const statePriority: StateKind[] = ["ended", "offline", "approval", "question", "plan", "popup", "starting", "running", "idle"];

export interface State {
  kind: StateKind;
  tone: Tone;
  text: string;
}

const stateText: Record<StateKind, [Tone, string]> = {
  ended: ["off", "已结束"],
  offline: ["off", "电脑离线"],
  approval: ["wait", "等待审批"],
  question: ["wait", "等你回答"],
  plan: ["wait", "计划待批准"],
  popup: ["wait", "终端里有弹窗"],
  starting: ["run", "启动中"],
  running: ["run", "运行中"],
  idle: ["idle", "空闲"],
};

export function exited(s: SessionLike): boolean {
  return s.info?.status === SessionStatus.EXITED;
}

/** The single state to show for a session. machine undefined counts as
 * online: a machine not listed yet is not known to be off. */
export function stateOf(s: SessionLike, machine?: MachineLike): State {
  const i = s.info;
  let kind: StateKind;
  if (i?.status === SessionStatus.EXITED) kind = "ended";
  else if (machine && !machine.online) kind = "offline";
  else if (!i) kind = "idle";
  else if (i.pendingApprovals > 0) kind = "approval";
  else if (i.pendingQuestions > 0) kind = "question";
  else if (i.planOpen) kind = "plan";
  else if (i.status === SessionStatus.BLOCKED) kind = "popup";
  else if (i.status === SessionStatus.STARTING) kind = "starting";
  else if (i.status === SessionStatus.RUNNING) kind = "running";
  else kind = "idle";
  const [tone, text] = stateText[kind];
  return { kind, tone, text };
}

export function needsAttention(s: SessionLike, machine?: MachineLike): boolean {
  return stateOf(s, machine).tone === "wait";
}

/** The title rule: the session's title, else "folder · start time". */
export function titleOf(s: SessionLike): string {
  const t = plain(s.info?.title || "");
  if (t) return t;
  const start = s.info?.createdAt ? timestampMs(s.info.createdAt) : Number(s.updatedMs);
  return `${projectName(projectKey(s))}${start ? " · " + hhmm(start) : ""}`;
}

/** One-line preview for a list row, by state. */
export function previewOf(s: SessionLike, st: State, pending?: { approval?: ApprovalRequested; question?: QuestionAsked }): string {
  switch (st.kind) {
    case "approval":
      if (!pending?.approval) return st.text;
      return pending.approval.command ? "请求执行 " + oneLine(pending.approval.command) : "请求权限 · " + pending.approval.toolName;
    case "question":
      return pending?.question?.questions[0]?.text || st.text;
    case "plan":
      return "计划待批准";
    case "popup":
      return s.info?.statusDetail || st.text;
  }
  const p = plain(s.info?.preview || "");
  if (p) return p;
  return s.info?.title ? st.text : "还没发消息";
}

/** Text for one-line previews: Markdown marks removed whole, so a cut
 * never leaves half a link or code mark. */
export function plain(text: string): string {
  return oneLine(
    text
      .replace(/```[^\n]*\n?/g, "")
      .replace(/`([^`]*)`/g, "$1")
      .replace(/\*\*([^*]+)\*\*/g, "$1")
      .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
      .replace(/^\s{0,3}#{1,6}\s+/gm, "")
      .replace(/^\s*(?:[-*+]|\d+[.)])\s+/gm, ""),
  );
}

function oneLine(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

/** Short time, like the mockup: 刚刚, 3 分钟前, 9:12, 昨天, 9月27日. */
export function when(ms: number | bigint, now = Date.now()): string {
  const t = Number(ms);
  if (!t) return "";
  const d = now - t;
  if (d < 60_000) return "刚刚";
  if (d < 3_600_000) return `${Math.floor(d / 60_000)} 分钟前`;
  const then = new Date(t);
  if (then.toDateString() === new Date(now).toDateString()) return hhmm(t);
  if (then.toDateString() === new Date(now - 86_400_000).toDateString()) return "昨天";
  return `${then.getMonth() + 1}月${then.getDate()}日`;
}

/** How long ago, for projects and machines: 2 小时前 rather than 9:12. */
export function ago(ms: number | bigint, now = Date.now()): string {
  const t = Number(ms);
  if (!t) return "";
  const d = now - t;
  if (d >= 3_600_000 && d < 86_400_000 && new Date(t).toDateString() === new Date(now).toDateString()) return `${Math.floor(d / 3_600_000)} 小时前`;
  return when(t, now);
}

export function hhmm(ms: number): string {
  const d = new Date(ms);
  return `${d.getHours()}:${String(d.getMinutes()).padStart(2, "0")}`;
}

/** Minutes left before a deadline, as the dock writes it. */
export function minutesLeft(deadlineMs: number, now = Date.now()): string {
  const left = deadlineMs - now;
  if (left <= 0) return "已超时";
  if (left < 60_000) return `剩 ${Math.ceil(left / 1000)} 秒`;
  return `剩 ${Math.ceil(left / 60_000)} 分钟`;
}

export function duration(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} 秒`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} 分 ${s % 60} 秒`;
  return `${Math.floor(m / 60)} 小时 ${m % 60} 分`;
}

/** Home folders as ~. */
export function shortPath(p: string): string {
  return p.replace(/^\/Users\/[^/]+/, "~").replace(/^\/home\/[^/]+/, "~");
}

/** macOS reaches /tmp, /var and /etc through /private; either spelling
 * names the same folder. */
function samePlace(p: string): string {
  return p.replace(/^\/private(?=\/(?:tmp|var|etc)(?:\/|$))/, "");
}

/** Absolute paths in a tool title, shortened for display. Paths are
 * found as whole tokens: a quoted one is the whole quoted text, spaces and
 * all; an unquoted one runs to whitespace or a quote. A token must start
 * with / after the start, a space, =, :, (, [, { or a comma.
 * - under the session folder (cwd): relative to it, the folder itself "."
 * - else under the home folder: ~/…; without home, /Users/<name> and
 *   /home/<name> count as home
 * - anything else, or anything unsure, unchanged as written
 * A folder only matches when what follows it in the token is /, nothing,
 * or a :line suffix, so /Users/me/Code/foo[x]/a or "…/foo bar/a" is not
 * inside /Users/me/Code/foo. macOS's /private/tmp, /private/var and
 * /private/etc count as /tmp, /var and /etc. The expanded details keep the
 * original text. */
export function shortenPaths(text: string, cwd: string, home = ""): string {
  return text.replace(/(^|[\s=:([{,])(?:(["'])(\/[^"'\n]*)\2|(\/[^\s"'`]*))/g, (all, before: string, q: string | undefined, quoted?: string, bare?: string) => {
    const path = quoted ?? bare ?? "";
    const short = shortenPath(path, cwd, home);
    if (short === path) return all;
    return quoted !== undefined ? before + q + short + q : before + short;
  });
}

/** One whole path shortened by the same rules, spaces and all, as a file
 * edit's title is; unchanged if it is not under the session or home
 * folder. */
export function shortenPath(path: string, cwd: string, home = ""): string {
  const base = samePlace(cwd).replace(/\/+$/, "");
  const h = samePlace(home).replace(/\/+$/, "");
  // What follows a folder in the path: a subpath, nothing, or :line[:col].
  const under = (p: string, dir: string): string | undefined => {
    if (!dir || !p.startsWith(dir)) return undefined;
    const rest = p.slice(dir.length);
    return rest === "" || rest[0] === "/" || /^:\d+(:\d+)*$/.test(rest) ? rest : undefined;
  };
  const p = samePlace(path);
  const inCwd = under(p, base);
  if (inCwd !== undefined) return inCwd === "" || inCwd === "/" ? "." : inCwd[0] === "/" ? inCwd.slice(1) : "." + inCwd;
  const hm = h || /^\/(?:Users|home)\/[^/:]+/.exec(p)?.[0] || "";
  const inHome = under(p, hm);
  return inHome === undefined ? path : "~" + inHome;
}

/** A path that does not fit keeps its end: …/ai-bot/remote-agent. */
export function tailPath(p: string, max = 28): string {
  const s = shortPath(p);
  if (s.length <= max) return s;
  const parts = s.split("/");
  let out = parts.pop() || "";
  while (parts.length && out.length + parts[parts.length - 1].length + 1 <= max - 2) out = parts.pop() + "/" + out;
  return "…/" + out;
}

// --- modes and chips ---

/** The approval mode named from turn-context fields. */
/** Claude Code's permission mode as one of the four client modes; a mode
 * outside them, such as acceptEdits, keeps its own name (architecture §7:
 * shown as the CLI has it). */
export function permissionModeName(pm: string): string {
  return ({ default: "询问", auto: "自动", plan: "计划", bypassPermissions: "完全放行" } as Record<string, string>)[pm] ?? pm;
}

/** The approval mode named from turn-context fields: Claude Code's
 * permission mode, or Codex's approval fields. */
export function contextMode(reviewer: string, policy: string, sandbox: string, collaboration: string, permission = ""): string {
  if (permission) return permissionModeName(permission);
  if (!reviewer && !policy && !collaboration) return "";
  if (collaboration === "plan") return "计划";
  if (sandbox === "danger-full-access" && policy === "never") return "完全放行";
  if (reviewer === "auto_review") return "自动";
  return "询问";
}

const modeNames: Record<number, string> = {
  [ApprovalMode.ASK]: "询问",
  [ApprovalMode.AUTO]: "自动",
  [ApprovalMode.PLAN]: "计划",
  [ApprovalMode.FULL_ACCESS]: "完全放行",
};

export function modeName(m: ApprovalMode): string {
  return modeNames[m] ?? "";
}

export function modeByName(name: string): ApprovalMode {
  return (Number(Object.keys(modeNames).find((k) => modeNames[Number(k)] === name)) || ApprovalMode.UNSPECIFIED) as ApprovalMode;
}

const modeClass: Record<string, string> = { 计划: "m-plan", 完全放行: "m-danger", 自动: "m-auto", 询问: "" };

export interface ModeChip {
  cls: string;
  text: string;
  mode: ApprovalMode;
}

/** The approval mode, read from the latest turn context. */
export function modeOf(s: SessionLike): ModeChip | undefined {
  const i = s.info;
  if (!i) return undefined;
  // Session.mode is either CLI's mode as a client mode; UNSPECIFIED with a
  // permission mode is one of Claude Code's own, shown by its name. Older
  // daemons set neither: name it from the approval fields.
  const text = i.mode
    ? modeName(i.mode)
    : i.permissionMode
      ? permissionModeName(i.permissionMode)
      : contextMode(i.approvalsReviewer, i.approvalPolicy, i.sandbox, i.collaborationMode);
  return text ? { cls: modeClass[text] ?? "", text, mode: modeByName(text) } : undefined;
}

/** Whether a session's model lacks the auto mode (Claude Code's haiku). */
export function noAutoMode(s: SessionLike): boolean {
  return agentOf(s) === Agent.CLAUDE && /haiku/i.test(s.info?.model ?? "");
}

export function contextPercent(s: SessionLike): number | undefined {
  const i = s.info;
  if (!i || i.contextWindow <= 0n) return undefined;
  return Math.min(100, Number((i.contextTokens * 100n) / i.contextWindow));
}

/** The entry of a model list that a session's model is. Codex reports
 * the slug it was started with; Claude Code offers aliases (sonnet, opus)
 * but records the full ID (claude-sonnet-5-5), so an alias matches a full
 * ID that has it as one of its words. */
export function findModel<M extends { slug: string }>(models: M[] | undefined, id: string): M | undefined {
  if (!models || !id) return undefined;
  return models.find((m) => m.slug === id) ?? models.find((m) => modelIs(id, m.slug));
}

/** The name to show for a model: its display name from the machine's
 * list; the ID as is while the list has not come; and for a Codex model
 * missing from the list (the machine's default may be one Codex hides
 * from /model), the name Codex gives such slugs: gpt-6.1-sol is
 * GPT-6.1-Sol. */
export function modelLabel<M extends { slug: string; displayName: string }>(models: M[] | undefined, id: string, agent: Agent = Agent.CODEX): string {
  if (!id) return "";
  const m = findModel(models, id);
  if (m?.displayName) return m.displayName;
  if (!models || agent === Agent.CLAUDE) return id;
  return id
    .split("-")
    .map((w) => (/^gpt$/i.test(w) ? "GPT" : w.charAt(0).toUpperCase() + w.slice(1)))
    .join("-");
}

/** Whether the model a CLI reports (id) is the one chosen by slug or
 * alias. */
export function modelIs(id: string, slug: string): boolean {
  return !!id && !!slug && (id === slug || id.toLowerCase().split(/[-_.\s]/).includes(slug.toLowerCase()));
}

/** Context above this share turns the chip orange. */
export const CONTEXT_WARN = 80;

// --- new sessions: the configuration follows the project ---

export interface NewConfig {
  machineId: string;
  cwd: string;
  agent: Agent;
  model: string; // empty: the machine's default
  effort: string;
  mode: ApprovalMode; // UNSPECIFIED: the machine's default
  from?: SessionLike; // the session copied, if any
}

/** What a new session in project uses: the machine, CLI, model, effort
 * and mode of its latest session. Full access (needs confirming each
 * time) and plan (a passing state) fall back to the machine default. If
 * that machine is offline, another online machine the project used is
 * taken with its own defaults. */
export function inheritConfig(p: Project<SessionLike> | undefined, machines: Map<string, MachineLike>, cwd = p?.key ?? ""): NewConfig | undefined {
  const latest = p?.sessions[0];
  if (!p || !latest) return undefined;
  const agent = agentOf(latest);
  const online = (id: string) => machines.get(id)?.online ?? false;
  if (online(latest.machineId)) {
    const mode = modeOf(latest)?.mode ?? ApprovalMode.UNSPECIFIED;
    const keep = mode === ApprovalMode.ASK || mode === ApprovalMode.AUTO;
    return {
      machineId: latest.machineId,
      cwd,
      agent,
      model: latest.info?.model ?? "",
      effort: latest.info?.effort ?? "",
      mode: keep ? mode : ApprovalMode.UNSPECIFIED,
      from: latest,
    };
  }
  const other = p.machines.find(online);
  const machineId = other ?? latest.machineId;
  const m = machines.get(machineId);
  // That machine may not have the same CLI; take one it has.
  const agents = machineAgents(m);
  return { machineId, cwd, agent: agents.includes(agent) ? agent : agents[0], model: "", effort: "", mode: ApprovalMode.UNSPECIFIED, from: latest };
}

// --- events ---

export interface RunningTool {
  title: string;
  since: number;
  callId: string;
  // As the finished call will be; known at the start for Claude Code.
  kind: ToolKind;
  itemType: string;
}

export interface Derived {
  approvals: Map<string, ApprovalRequested>; // pending, in request order
  resolved: Map<string, { decision: ApprovalDecision; message: string }>;
  questions: Map<string, QuestionAsked>; // pending
  answered: Map<string, string[]>;
  inputs: Map<string, InputStatus>; // latest state per request ID, in first-seen order
  running: boolean;
  usage?: TokenUsage;
  plan?: PlanProposed; // the latest plan
  tools: RunningTool[]; // started and not completed, oldest first
  // User messages typed in the terminal rather than sent from a client.
  typedLocally: Set<bigint>;
  // Mode or model changes made in the terminal, by the seq of the context
  // that shows them.
  terminalChanges: Map<bigint, string>;
  // Context use before and after each compaction, by the event's seq.
  compactions: Map<bigint, { before?: number; after?: number }>;
}

function usagePercent(u?: TokenUsage): number | undefined {
  if (!u || u.contextWindow <= 0n) return undefined;
  return Math.min(100, Number((u.lastTotalTokens * 100n) / u.contextWindow));
}

/** Folds a session's events into what is pending and what happened. */
export function derive(events: Event[], agent: Agent = Agent.CODEX): Derived {
  const d: Derived = {
    approvals: new Map(),
    resolved: new Map(),
    questions: new Map(),
    answered: new Map(),
    inputs: new Map(),
    running: false,
    tools: [],
    typedLocally: new Set(),
    terminalChanges: new Map(),
    compactions: new Map(),
  };
  let mode = "";
  let model = "";
  let compacting: bigint | undefined;
  // Messages sent from clients are recorded as submitted before the CLI
  // records them; the rest were typed in the terminal.
  let submitted = 0;
  // A user message with no submitted state before it, which may still
  // come right after: Claude Code records the message first.
  let unclaimed: bigint | undefined;
  // The model and effort last switched to from here, until a context shows
  // a model.
  let expected: { model: string; effort: string } | undefined;
  const queuedAt = new Map<string, bigint>();
  // A plan was proposed since entering plan mode: leaving it is the plan's
  // answer, however given, not a mode switch made in the terminal.
  let planned = false;
  for (const e of events) {
    const k = e.kind;
    switch (k.case) {
      case "approvalRequested":
        d.approvals.set(k.value.approvalId, k.value);
        break;
      case "approvalResolved":
        d.approvals.delete(k.value.approvalId);
        d.resolved.set(k.value.approvalId, { decision: k.value.decision, message: k.value.message });
        break;
      case "questionAsked":
        if (!k.value.async) d.questions.set(k.value.questionId, k.value);
        break;
      case "questionAnswered":
        d.questions.delete(k.value.questionId);
        d.answered.set(
          k.value.questionId,
          k.value.answers.flatMap((a) => a.answers),
        );
        break;
      case "turnStarted":
        d.running = true;
        break;
      case "turnEnded":
        d.running = false;
        d.questions.clear();
        d.tools = [];
        break;
      case "inputState":
        d.inputs.set(k.value.requestId, k.value.status);
        if (k.value.status === InputStatus.QUEUED && !queuedAt.has(k.value.requestId)) queuedAt.set(k.value.requestId, e.seq);
        if (k.value.status === InputStatus.SUBMITTED) {
          // Codex marks a message submitted before recording it. Claude
          // Code usually records the message first and the state follows;
          // but a message queued into a running turn is confirmed by its
          // enqueue record, before the message. So the state claims only
          // a message recorded after its request was queued: one recorded
          // before that was typed at the computer.
          const since = queuedAt.get(k.value.requestId);
          if (agent === Agent.CLAUDE && unclaimed !== undefined && e.seq - unclaimed <= 3n && (since === undefined || unclaimed > since)) {
            d.typedLocally.delete(unclaimed);
            unclaimed = undefined;
          } else submitted++;
        }
        break;
      case "userMessage":
        if (submitted > 0) submitted--;
        // Codex sends this itself when a plan is approved.
        else if (k.value.text !== "Implement the plan.") {
          d.typedLocally.add(e.seq);
          unclaimed = e.seq;
        }
        break;
      case "toolStarted":
        d.tools.push({ title: k.value.title, since: eventTime(e), callId: k.value.callId, kind: k.value.kind, itemType: k.value.itemType });
        break;
      case "toolCall": {
        // Claude Code runs tools in parallel and names the call; Codex does
        // not, and its calls complete in the order they started.
        const id = k.value.callId;
        let i = id ? d.tools.findIndex((t) => t.callId === id) : -1;
        if (i < 0) i = d.tools.findIndex((t) => !t.callId);
        if (i >= 0) d.tools.splice(i, 1);
        break;
      }
      case "tokenUsage":
        d.usage = k.value;
        if (compacting !== undefined) {
          const c = d.compactions.get(compacting);
          if (c) c.after = usagePercent(k.value);
          compacting = undefined;
        }
        break;
      case "planProposed":
        d.plan = k.value;
        planned = true;
        break;
      case "settingsChanged":
        // Changed through remote-agent; recorded as such already.
        // The next context should show this model; the client names it by
        // slug or alias, the context by the CLI's own ID.
        if (k.value.model) expected = { model: k.value.model, effort: k.value.effort };
        if (k.value.mode) mode = modeName(k.value.mode);
        if (k.value.compacted) {
          d.compactions.set(e.seq, { before: usagePercent(d.usage) });
          compacting = e.seq;
        }
        break;
      case "turnContext": {
        const c = k.value;
        const m = contextMode(c.approvalsReviewer, c.approvalPolicy, c.sandbox, c.collaborationMode, c.permissionMode);
        const planning = c.collaborationMode === "plan" || c.permissionMode === "plan";
        const mm = `${c.model} ${c.effort}`;
        const changes: string[] = [];
        if (mode && m && m !== mode && !(mode === "计划" && planned)) changes.push(`模式切到了 ${m}`);
        if (m && m !== mode) planned = false;
        // Plan mode runs at its own effort; only a model change counts there.
        // A context without a model is Claude Code's mode, read off the
        // screen; it says nothing about the model.
        // After a switch from here, the next context is compared with what
        // was chosen; a different model means the terminal changed it since.
        const changed = expected ? !(modelIs(c.model, expected.model) && (!expected.effort || !c.effort || c.effort === expected.effort)) : mm !== model;
        if ((model || expected) && c.model && changed && !planning) changes.push(`模型切到了 ${c.model}${c.effort ? " · " + c.effort : ""}`);
        if (c.model && !planning) expected = undefined;
        if (changes.length) d.terminalChanges.set(e.seq, changes.join("，"));
        if (m) mode = m;
        if (!planning && c.model) model = mm;
        break;
      }
      case "sessionState":
        if (k.value.status === SessionStatus.EXITED) {
          d.running = false;
          d.approvals.clear();
          d.questions.clear();
          d.tools = [];
        }
        break;
    }
  }
  return d;
}

export function eventTime(e: Event): number {
  return e.time ? timestampMs(e.time) : 0;
}

export function decisionText(d: ApprovalDecision): string {
  switch (d) {
    case ApprovalDecision.ALLOW:
      return "已允许";
    case ApprovalDecision.DENY:
      return "已拒绝";
    case ApprovalDecision.TIMEOUT:
      return "超时，已拒绝";
    case ApprovalDecision.CANCELED:
      return "已取消";
    case ApprovalDecision.TERMINAL:
      return "已在终端处理";
  }
  return "";
}

/** Splits the "(Recommended)" marker off an option label. */
export function optionLabel(label: string): { text: string; recommended: boolean } {
  const m = label.match(/^(.*?)\s*\((Recommended|推荐)\)\s*$/i);
  return m ? { text: m[1], recommended: true } : { text: label, recommended: false };
}

// --- the dock above the input ---

export type DockItem =
  | { kind: "approval"; id: string; approval: ApprovalRequested }
  | { kind: "question"; id: string; question: QuestionAsked }
  | { kind: "plan"; id: string; plan?: PlanProposed }
  | { kind: "popup"; id: string; title: string };

/** Everything waiting for the user, in the state table's order. Only the
 * first is docked; the rest wait their turn. An ended or offline session
 * shows a status card instead. */
export function dockItems(s: SessionLike, d: Derived, machine?: MachineLike): DockItem[] {
  const st = stateOf(s, machine);
  if (st.kind === "ended" || st.kind === "offline") return [];
  const out: DockItem[] = [];
  for (const a of d.approvals.values()) out.push({ kind: "approval", id: "a:" + a.approvalId, approval: a });
  for (const q of d.questions.values()) out.push({ kind: "question", id: "q:" + q.questionId, question: q });
  if (s.info?.planOpen) out.push({ kind: "plan", id: "plan", plan: d.plan });
  if (s.info?.status === SessionStatus.BLOCKED) out.push({ kind: "popup", id: "popup", title: s.info.statusDetail });
  return out;
}

// --- messages sent from this client ---

/** A message sent from this page. sendId goes in SendRequest and makes
 * the daemon queue the text once; requestId and deadline seal the command,
 * and a retry reuses them so the daemon returns its first answer. */
export interface Outgoing {
  sendId: string;
  requestId: string;
  deadline: number;
  text: string;
  status: "sending" | "sent" | "failed";
  error?: string;
  // A try may have reached the computer: retries keep this request.
  unknown?: boolean;
}

export type Phase =
  | "sending" // on its way to the computer
  | "delivered" // the computer has it, nothing more heard yet
  | "queued" // waiting in the daemon's queue: shown as a queue chip
  | "failed" // not confirmed to have arrived: retry is safe
  | "unconfirmed" // typed into the terminal, not confirmed: no retry
  | "done" // the CLI recorded it: shown as a normal message
  | "canceled";

/** Where a message stands, from the send call and the daemon's events. */
export function phaseOf(o: Pick<Outgoing, "status">, input?: InputStatus): Phase {
  switch (input) {
    case InputStatus.SUBMITTED:
      return "done";
    case InputStatus.UNCONFIRMED:
      return "unconfirmed";
    case InputStatus.CANCELED:
      return "canceled";
    case InputStatus.QUEUED:
      return "queued";
  }
  if (o.status === "failed") return "failed";
  return o.status === "sending" ? "sending" : "delivered";
}

/** Why queued messages wait, in the mockup's words. */
export function queueReason(s: SessionLike, d: Derived): string {
  const i = s.info;
  if (i?.inputWait === "unrecognized") return "认不出输入框，已暂停";
  if (i?.inputWait === "draft") return "终端里有没发出的草稿，发完或清掉后再发";
  if (i?.status === SessionStatus.BLOCKED || i?.inputWait === "popup") return "弹窗处理完再发";
  if (d.questions.size > 0 || d.approvals.size > 0) return "答完再发";
  if (i?.planOpen) return "计划处理完再发";
  if (i?.status === SessionStatus.STARTING) return "启动后发";
  if (d.running) return "本轮结束后发";
  return "";
}

// --- the chat timeline ---

export type Item =
  // decision: an approval's result shown on the tool call it was for.
  | { kind: "event"; key: string; event: Event; decision?: ApprovalDecision }
  | { kind: "tools"; key: string; calls: ToolCall[] }
  | { kind: "turn"; key: string; ms: number };

/** Whether an event draws anything in the chat. */
function visible(e: Event, d: Derived): boolean {
  const k = e.kind;
  switch (k.case) {
    case "userMessage":
    case "agentMessage":
    case "reasoning":
    case "toolCall":
    case "planProposed":
      return true;
    case "turnEnded":
      return k.value.outcome === TurnOutcome.ABORTED || k.value.durationMs >= 1000n;
    case "approvalRequested":
      return d.resolved.has(k.value.approvalId);
    case "questionAsked":
      return d.answered.has(k.value.questionId);
    case "inputState":
      return k.value.status === InputStatus.CANCELED || k.value.status === InputStatus.UNCONFIRMED;
    case "settingsChanged":
      return k.value.compacted || !!k.value.model || !!modeName(k.value.mode);
    case "turnContext":
      return d.terminalChanges.has(e.seq);
    case "sessionState":
      return k.value.status === SessionStatus.EXITED;
  }
  return false;
}

function toolOk(t: ToolCall): boolean {
  return t.status !== "failed" && (t.exitCode === undefined || t.exitCode === 0);
}

/** The chat's rows: runs of two or more completed tool calls in a row
 * fold into one line; events that draw nothing are left out. */
export function timeline(events: Event[], d: Derived): Item[] {
  const out: Item[] = [];
  const merged = approvalsOnCalls(events, d);
  let run: { first: Event; calls: ToolCall[] } | undefined;
  const flush = () => {
    if (!run) return;
    if (run.calls.length > 1) out.push({ kind: "tools", key: "t" + run.first.seq, calls: run.calls });
    else out.push({ kind: "event", key: String(run.first.seq), event: run.first });
    run = undefined;
  };
  for (const e of events) {
    if (!visible(e, d)) continue;
    const k = e.kind;
    if (k.case === "approvalRequested" && merged.approvals.has(k.value.approvalId)) continue;
    const decision = merged.calls.get(e.seq);
    if (decision !== undefined) {
      // Shown on its own, so the decision is not folded away.
      flush();
      out.push({ kind: "event", key: String(e.seq), event: e, decision });
      continue;
    }
    if (k.case === "toolCall" && toolOk(k.value)) {
      if (!run) run = { first: e, calls: [] };
      run.calls.push(k.value);
      continue;
    }
    flush();
    if (k.case === "turnEnded" && k.value.outcome !== TurnOutcome.ABORTED) out.push({ kind: "turn", key: String(e.seq), ms: Number(k.value.durationMs) });
    else out.push({ kind: "event", key: String(e.seq), event: e });
  }
  flush();
  return out;
}

/** Resolved approvals to show on their tool calls: only where the call is
 * surely the one approved, its tool and input digest (the daemon's, on
 * both events, compared as strings) matching exactly one
 * resolved approval requested before it, and that approval matching no
 * other call. Parallel calls, repeated identical calls and events without
 * a digest (Codex, older daemons) keep the approval as a row of its own. */
export function approvalsOnCalls(events: Event[], d: Derived): { calls: Map<bigint, ApprovalDecision>; approvals: Set<string> } {
  const approvals = new Map<string, { id: string; seq: bigint }[]>();
  const calls = new Map<string, bigint[]>();
  for (const e of events) {
    const k = e.kind;
    if (k.case === "approvalRequested" && d.resolved.has(k.value.approvalId)) {
      // The daemon gives the request the digest it gives the call; without
      // one (Codex, older daemons) there is nothing to match.
      const digest = k.value.inputDigest;
      if (!digest) continue;
      const key = k.value.toolName + "\0" + digest;
      approvals.set(key, [...(approvals.get(key) ?? []), { id: k.value.approvalId, seq: e.seq }]);
    } else if (k.case === "toolCall" && k.value.inputDigest) {
      const key = k.value.itemType + "\0" + k.value.inputDigest;
      calls.set(key, [...(calls.get(key) ?? []), e.seq]);
    }
  }
  const out = { calls: new Map<bigint, ApprovalDecision>(), approvals: new Set<string>() };
  for (const [key, as] of approvals) {
    const cs = calls.get(key) ?? [];
    if (as.length !== 1 || cs.length !== 1 || cs[0] < as[0].seq) continue;
    out.calls.set(cs[0], d.resolved.get(as[0].id)!.decision);
    out.approvals.add(as[0].id);
  }
  return out;
}

/** "运行了 3 条命令" and the like, for a folded run of tool calls. */
export function toolsSummary(calls: ToolCall[]): string {
  const n = calls.length;
  if (calls.every((c) => c.kind === ToolKind.COMMAND)) return `运行了 ${n} 条命令`;
  if (calls.every((c) => c.kind === ToolKind.FILE_CHANGE)) return `编辑了 ${n} 个文件`;
  return `完成了 ${n} 个工具调用`;
}

/** Why a session ended, in plain words. */
export function exitReason(detail: string): string {
  if (detail === "stopped") return "会话是被手动结束的。";
  return "tmux 会话已经不在了。";
}

// --- wording that names the CLI ---

export function askTitle(a: Agent): string {
  return `${agentName(a)} 在问你`;
}
