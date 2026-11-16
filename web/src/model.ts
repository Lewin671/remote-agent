import * as l10n from "./i18n";
import * as i18n from "./i18n";
import { t } from "./i18n";
import { isSkeleton } from "./skeleton";
import { approvalDisplay } from "./approval-display";
import { agentInfo, agentName, DEFAULT_AGENT, isKnownAgent } from "./agents";
// View models derived from the decrypted state: projects, session states
// by priority, what is docked above the input, how a sent message is
// doing, and how the chat timeline groups events. Everything here is pure,
// so the rules in docs/design/client-v1.html can be tested directly.

import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { timestampMs } from "@bufbuild/protobuf/wkt";
import { getCore } from "./core/runtime";
import {
  EventBatchSchema,
  ProjectionStateSchema,
  type ProjectionState,
} from "./gen/remoteagent/core/v1/projection_pb";
import type {
  ApprovalRequested,
  Event,
  Origin,
  PlanProposed,
  QuestionAsked,
  SentFile,
  TokenUsage,
  ToolCall,
} from "./gen/remoteagent/v1/event_pb";
import {
  Agent,
  ApprovalDecision,
  ApprovalMode,
  InputStage,
  InputStatus,
  SendMode,
  SessionStatus,
  ToolBackground,
  ToolKind,
  TurnOutcome,
} from "./gen/remoteagent/v1/event_pb";
import type { ModelInfo, Session } from "./gen/remoteagent/v1/local_pb";
import {
  DefaultModeSource,
  type MachineMeta,
} from "./gen/remoteagent/v1/remote_pb";

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

// List positions use session lifecycle times, not relay upload times.
const byID = (a: SessionLike, b: SessionLike) =>
  a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
export const createdMs = (s: SessionLike) =>
  s.info?.createdAt ? timestampMs(s.info.createdAt) : 0;
export const endedMs = (s: SessionLike) =>
  s.info?.endedAt ? timestampMs(s.info.endedAt) : createdMs(s);
export const attentionMs = (s: SessionLike) =>
  s.info?.attentionSince ? timestampMs(s.info.attentionSince) : createdMs(s);
export const compareLive = (a: SessionLike, b: SessionLike) =>
  createdMs(b) - createdMs(a) || byID(a, b);
export const compareEnded = (a: SessionLike, b: SessionLike) =>
  endedMs(b) - endedMs(a) || byID(a, b);
export function compareAttention(
  a: SessionLike,
  b: SessionLike,
  machines: ReadonlyMap<string, MachineLike>,
): number {
  return (
    statePriority.indexOf(stateOf(a, machines.get(a.machineId)).kind) -
      statePriority.indexOf(stateOf(b, machines.get(b.machineId)).kind) ||
    attentionMs(a) - attentionMs(b) ||
    byID(a, b)
  );
}

// --- agents ---

export { agentName } from "./agents";

/** The CLI a session runs; Codex until the session's record says. */
export function agentOf(s?: { info?: Session } | Session): Agent {
  const a =
    s && "info" in s ? s.info?.agent : (s as Session | undefined)?.agent;
  return agentInfo(a).agent;
}

/** The CLIs a machine can start; Codex while its list is not known. */
export function machineAgents(m?: MachineLike): Agent[] {
  const list =
    m?.meta?.agents
      .map((a) => agentInfo(a.agent).agent)
      .filter((a) => isKnownAgent(a)) ?? [];
  const uniq = [...new Set(list)];
  return uniq.length ? uniq : [DEFAULT_AGENT];
}

// --- projects ---

export interface Project<S extends SessionLike = SessionLike> {
  key: string;
  name: string;
  sessions: S[]; // most recent first
  updatedMs: number;
  hasLive: boolean;
  latestCreatedMs: number; // stable list position, independent of output
  machines: string[]; // machine IDs, most recent first
  // How many of its sessions have ended, as its machines count them: the
  // sessions held here are only the pages fetched so far.
  ended: number;
}

/** The project a session belongs to: its working directory's absolute
 * path, the same on every machine. */
export function projectKey(s: SessionLike): string {
  return s.info?.cwd || "";
}

/** A project's name: the last folder of its path. */
export function projectName(key: string): string {
  return key.split("/").filter(Boolean).pop() || t("projects.unknown");
}

export const compareProjects = (
  a: Pick<Project, "key" | "hasLive" | "latestCreatedMs">,
  b: Pick<Project, "key" | "hasLive" | "latestCreatedMs">,
) =>
  Number(b.hasLive) - Number(a.hasLive) ||
  b.latestCreatedMs - a.latestCreatedMs ||
  (a.key < b.key ? -1 : a.key > b.key ? 1 : 0);

/** The account's projects: those of the sessions held, those the
 * machines report (MachineMeta.projects), and those added that have no
 * session yet. Sessions are listed a page at a time, so a project whose
 * sessions all ended long ago is known only from its machine; whether a
 * project has live sessions is still read from the sessions, which are
 * all held while they live. A project added with 新项目… is known only
 * from its settings (ProjectPrefs.settings) until its first session: it
 * is listed while the machine chosen for it is one of the account's, as
 * created when it was added. */
export function projects<S extends SessionLike>(
  sessions: Iterable<S>,
  machines: Iterable<MachineLike> = [],
  settings: Iterable<{
    path: string;
    machineId: string;
    updatedMs: bigint;
  }> = [],
): Project<S>[] {
  const byKey = new Map<string, Project<S>>();
  const project = (key: string) => {
    let p = byKey.get(key);
    if (!p) {
      p = {
        key,
        name: projectName(key),
        sessions: [],
        updatedMs: 0,
        hasLive: false,
        latestCreatedMs: 0,
        machines: [],
        ended: 0,
      };
      byKey.set(key, p);
    }
    return p;
  };
  const sorted = [...sessions].filter((s) => !s.info?.taskOwned || needsAttention(s)).sort(
    (a, b) => Number(b.updatedMs - a.updatedMs) || byID(a, b),
  );
  for (const s of sorted) {
    const p = project(projectKey(s));
    p.sessions.push(s);
    p.hasLive ||= !exited(s);
    p.latestCreatedMs = Math.max(p.latestCreatedMs, createdMs(s));
    p.updatedMs = Math.max(p.updatedMs, Number(s.updatedMs));
    if (!p.machines.includes(s.machineId)) p.machines.push(s.machineId);
  }
  // Ended sessions are counted machine by machine: each machine's count,
  // or what is held of its sessions where that is more (its metadata may
  // not have arrived, or may be behind).
  const counted = new Map<string, Map<string, number>>();
  const count = (key: string, machineId: string, n: number) => {
    const by = counted.get(key) ?? new Map<string, number>();
    counted.set(key, by);
    by.set(machineId, Math.max(by.get(machineId) ?? 0, n));
  };
  for (const p of byKey.values()) {
    const held = new Map<string, number>();
    for (const s of p.sessions)
      if (exited(s)) held.set(s.machineId, (held.get(s.machineId) ?? 0) + 1);
    for (const [machineId, n] of held) count(p.key, machineId, n);
  }
  const machineIds = new Set<string>();
  for (const m of machines) {
    machineIds.add(m.id);
    for (const x of m.meta?.projects ?? []) {
      const p = project(x.path);
      count(x.path, m.id, x.ended);
      p.latestCreatedMs = Math.max(
        p.latestCreatedMs,
        Number(x.latestCreatedMs),
      );
      p.updatedMs = Math.max(
        p.updatedMs,
        Number(x.latestCreatedMs),
        Number(x.latestEndedMs),
      );
      if (!p.machines.includes(m.id)) p.machines.push(m.id);
    }
  }
  for (const s of settings) {
    if (!s.path || byKey.has(s.path) || !machineIds.has(s.machineId)) continue;
    const p = project(s.path);
    p.machines.push(s.machineId);
    p.latestCreatedMs = p.updatedMs = Number(s.updatedMs);
  }
  for (const p of byKey.values())
    p.ended = [...(counted.get(p.key)?.values() ?? [])].reduce(
      (a, b) => a + b,
      0,
    );
  return [...byKey.values()].sort(compareProjects);
}

export function machineName(m?: MachineLike): string {
  return (
    m?.meta?.hostname?.replace(/\.local$/, "") ||
    (m ? m.id.slice(0, 8) : t("common.unknown_computer"))
  );
}

// --- session state by priority ---

export type Tone = "run" | "wait" | "idle" | "off";

/** Session states from highest priority to lowest: lists, the navigation
 * bar and the bottom of a session all show only the highest one. */
export type StateKind =
  | "ended"
  | "offline"
  | "unlinked"
  | "approval"
  | "question"
  | "plan"
  | "popup"
  | "starting"
  | "running"
  | "idle";

export const statePriority: StateKind[] = [
  "ended",
  "offline",
  "approval",
  "question",
  "plan",
  "popup",
  "starting",
  "running",
  "idle",
];

export interface State {
  kind: StateKind;
  tone: Tone;
  text: string;
}

const stateText: Record<StateKind, [Tone, () => string]> = {
  ended: ["off", () => t("list_state.ended")],
  offline: ["off", () => t("list_state.offline")],
  unlinked: ["off", () => t("list_state.unlinked")],
  approval: ["wait", () => t("list_state.approval")],
  question: ["wait", () => t("list_state.question")],
  plan: ["wait", () => t("list_state.plan")],
  popup: ["wait", () => t("list_state.popup")],
  starting: ["run", () => t("list_state.starting")],
  running: ["run", () => t("list_state.running")],
  idle: ["idle", () => t("list_state.idle")],
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
  return { kind, tone, text: text() };
}

/** The state to show for a session. Out of reach of the server, the last
 * one heard is not shown as if it still held (client-v30 「断线」); that a
 * session ended stays true. */
export function shownStateOf(
  s: SessionLike,
  machine: MachineLike | undefined,
  unlinked: boolean,
): State {
  const st = stateOf(s, machine);
  if (!unlinked || st.kind === "ended") return st;
  const [tone, text] = stateText.unlinked;
  return { kind: "unlinked", tone, text: text() };
}

/** Why a session cannot be forked now, or "" when it can: the CLIs fork a
 * conversation between turns, and there must be one (architecture §13).
 * running is what the session's events say, which may be ahead of its
 * summary. */
export function forkBlocked(
  s: SessionLike,
  machine?: MachineLike,
  running = false,
): string {
  switch (stateOf(s, machine).kind) {
    case "starting":
      return i18n.t("session.fork_after_start");
    case "running":
    case "approval":
    case "question":
      return i18n.t("session.fork_after_turn");
    case "plan":
    case "popup":
      return i18n.t("session.fork_after_pending");
  }
  if (running) return i18n.t("session.fork_after_turn");
  return s.info?.title || s.info?.preview ? "" : i18n.t("session.fork_after_message");
}

export function needsAttention(s: SessionLike, machine?: MachineLike): boolean {
  return stateOf(s, machine).tone === "wait";
}

/** The title rule: the session's title, else "folder · start time". */
export function titleOf(s: SessionLike): string {
  const t = plain(s.info?.title || "");
  if (t) return t;
  const start = s.info?.createdAt
    ? timestampMs(s.info.createdAt)
    : Number(s.updatedMs);
  return `${projectName(projectKey(s))}${start ? " · " + hhmm(start) : ""}`;
}

/** One-line preview for a list row, by state. */
export function previewOf(
  s: SessionLike,
  st: State,
  pending?: { approval?: ApprovalRequested; question?: QuestionAsked },
): string {
  switch (st.kind) {
    case "approval":
      if (!pending?.approval) return st.text;
      return pending.approval.command
        ? t("list_state.request_command", { command: oneLine(approvalDisplay(pending.approval.command)) })
        : t("list_state.request_permission", { tool: approvalDisplay(pending.approval.toolName) });
    case "question":
      return pending?.question?.questions[0]?.text || st.text;
    case "plan":
      return t("list_state.plan");
    case "popup":
      return popupTitle(s.info?.statusDetail ?? "") || st.text;
  }
  const p = plain(s.info?.preview || "");
  if (p) return p;
  return s.info?.title ? st.text : t("list_state.no_message");
}

/** Strip table delimiters before flattening Markdown into a preview. Rows
 * without outer pipes are tables only beside their separator/header. */
export function tablePreview(text: string): string {
  const lines = text.split("\n");
  const cells = (line: string) => line.trim().replace(/^\|/, "").replace(/(?<!\\)\|$/, "").split(/(?<!\\)\|/).map(c => c.trim());
  const separator = (line: string) => line.includes("|") && cells(line).every(c => /^:?-{3,}:?$/.test(c));
  let inTable = false;
  return lines.map((line, i) => {
    if (separator(line)) { inTable = true; return ""; }
    if (line.includes("|") && (/^\s*\||(?<!\\)\|\s*$/.test(line) || inTable || separator(lines[i + 1] ?? "")))
      return cells(line).filter(Boolean).map(c => c.replace(/\\\|/g, "|")).join(i18n.t("common.list_separator"));
    inTable = false;
    return line;
  }).join("\n");
}

/** Text for one-line previews: Markdown marks removed whole, so a cut
 * never leaves half a link or code mark. */
export function plain(text: string): string {
  return oneLine(
    tablePreview(text)
      // The CLIs' image placeholders: gone before the words, 图 N among them.
      .replace(/^(?:\s*\[Image #\d+\])+\s*/, "")
      .replace(/\[Image #(\d+)\]/g, (_, index: string) => i18n.t("image.placeholder", { index: Number(index) }))
      .replace(/\x60\x60\x60[^\n]*\n?/g, "")
      .replace(/\x60([^\x60]*)\x60/g, "$1")
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
  const time = Number(ms);
  if (!time) return "";
  const d = now - time;
  if (d < 60_000) return t("time.just_now");
  if (d < 3_600_000) return t("time.minutes_ago", { count: Math.floor(d / 60_000) });
  const then = new Date(time);
  if (then.toDateString() === new Date(now).toDateString()) return hhmm(time);
  if (then.toDateString() === new Date(now - 86_400_000).toDateString())
    return t("time.yesterday");
  return t("time.month_day", { month: then.getMonth() + 1, day: then.getDate() });
}

/** How long ago, for projects and machines: 2 小时前 rather than 9:12. */
export function ago(ms: number | bigint, now = Date.now()): string {
  const time = Number(ms);
  if (!time) return "";
  const d = now - time;
  if (
    d >= 3_600_000 &&
    d < 86_400_000 &&
    new Date(time).toDateString() === new Date(now).toDateString()
  )
    return t("time.hours_ago", { count: Math.floor(d / 3_600_000) });
  return when(time, now);
}

/** Whole calendar days from a time to now, by local midnight. */
function daysAgo(ms: number, now: number): number {
  const day = (t: number) => {
    const d = new Date(t);
    d.setHours(0, 0, 0, 0);
    return d.getTime();
  };
  return Math.round((day(now) - day(ms)) / 86_400_000);
}

/** The date group an ended session is listed under on the home timeline. */
export function dayGroup(ms: number, now = Date.now()): string {
  const n = daysAgo(ms, now);
  if (n <= 0) return t("time.today");
  if (n === 1) return t("time.yesterday");
  if (n < 7) return t("time.last_week");
  const d = new Date(ms);
  return d.getFullYear() === new Date(now).getFullYear()
    ? t("time.month_group", { month: d.getMonth() + 1 })
    : t("time.year_month_group", { year: d.getFullYear(), month: d.getMonth() + 1 });
}

/** When a session ended, in the terms of its date group: the time under
 * 今天 and 昨天, the weekday under 近 7 天, the date under a month. */
export function endedTime(ms: number, now = Date.now()): string {
  if (!ms) return "";
  const n = daysAgo(ms, now);
  if (n <= 1) return hhmm(ms);
  const d = new Date(ms);
  return n < 7
    ? t("time.weekday", { day: ["sun", "mon", "tue", "wed", "thu", "fri", "sat"][d.getDay()] })
    : t("time.month_day", { month: d.getMonth() + 1, day: d.getDate() });
}

export function hhmm(ms: number): string {
  const d = new Date(ms);
  return `${d.getHours()}:${String(d.getMinutes()).padStart(2, "0")}`;
}

/** A clock time, with the date when it is not today: 9:41, 9月27日 14:02. */
export function dayTime(ms: number, now = Date.now()): string {
  const d = new Date(ms);
  if (d.toDateString() === new Date(now).toDateString()) return hhmm(ms);
  return i18n.t("time.ios_date_time", { month: d.getMonth() + 1, day: d.getDate(), time: hhmm(ms) });
}

/** A future local time in Chinese, independent of the browser's locale.
 * Compare calendar dates so midnight, month/year changes and DST stay correct.
 * Pairing expiry uses only the zero-padded 24-hour clock. */
export function futureTime(ms: number | bigint, now = Date.now(), format: "date-time" | "time" = "date-time"): string {
  const time = Number(ms);
  const clock = hhmm(time).padStart(5, "0");
  if (format === "time") return clock;
  const date = new Date(time);
  const today = new Date(now);
  const tomorrow = new Date(now);
  tomorrow.setDate(tomorrow.getDate() + 1);
  const label = date.toDateString() === today.toDateString() ? l10n.t("time.today")
    : date.toDateString() === tomorrow.toDateString() ? l10n.t("time.tomorrow")
    : l10n.t("time.month_day", { month: date.getMonth() + 1, day: date.getDate() });
  return `${label} ${clock}`;
}

/** Minutes left before a deadline, as the dock writes it. */
export function minutesLeft(deadlineMs: number, now = Date.now()): string {
  const left = deadlineMs - now;
  if (left <= 0) return i18n.t("approval.expired");
  if (left < 60_000) return i18n.t("approval.seconds_left", { count: Math.ceil(left / 1000) });
  return i18n.t("home.ios_approval_remaining", { count: Math.ceil(left / 60_000) });
}

export function duration(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 60) return i18n.t("time.duration_seconds", { count: s });
  const m = Math.floor(s / 60);
  if (m < 60) return i18n.t("time.duration_minutes_seconds", { minutes: m, seconds: s % 60 });
  return i18n.t("time.duration_hours_minutes", { hours: Math.floor(m / 60), minutes: m % 60 });
}

/** Home folders as ~. */
export function shortPath(p: string): string {
  return p.replace(/^\/Users\/[^/]+/, "~").replace(/^\/home\/[^/]+/, "~");
}

/** A folder's path as the picker writes it, piece by piece: ~ / Code / foo.
 * A path outside the home folder keeps its leading slash on the first piece
 * (/srv / www), so it does not read as one below the home folder; the root
 * is "/". */
export function pathCrumbs(p: string): string[] {
  const parts = shortPath(p).split("/").filter(Boolean);
  if (parts[0] === "~") return parts;
  if (parts.length === 0) return ["/"];
  return ["/" + parts[0], ...parts.slice(1)];
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
  return text.replace(
    /(^|[\s=:([{,])(?:([\x22\x27])(\/[^\x22\x27\n]*)\2|(\/[^\s\x22\x27\x60]*))/g,
    (
      all,
      before: string,
      q: string | undefined,
      quoted?: string,
      bare?: string,
    ) => {
      const path = quoted ?? bare ?? "";
      const short = shortenPath(path, cwd, home);
      if (short === path) return all;
      return quoted !== undefined ? before + q + short + q : before + short;
    },
  );
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
    return rest === "" || rest[0] === "/" || /^:\d+(:\d+)*$/.test(rest)
      ? rest
      : undefined;
  };
  const p = samePlace(path);
  const inCwd = under(p, base);
  if (inCwd !== undefined)
    return inCwd === "" || inCwd === "/"
      ? "."
      : inCwd[0] === "/"
        ? inCwd.slice(1)
        : "." + inCwd;
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
  while (
    parts.length &&
    out.length + parts[parts.length - 1].length + 1 <= max - 2
  )
    out = parts.pop() + "/" + out;
  return "…/" + out;
}

// Popup details are adapter diagnostics or native CLI text, never UI copy.
// Keep the raw value for identity and trust hints; display existing localized copy.
export function popupTitle(detail: string): string {
  return detail ? t("list_state.popup") : "";
}

// --- modes and chips ---

/** The approval mode named from turn-context fields. */
/** Claude Code's permission mode as one of the four client modes; a mode
 * outside them, such as acceptEdits, keeps its own name (architecture §7:
 * shown as the CLI has it). */
export function permissionModeName(pm: string): string {
  return (
    (
      {
        default: i18n.t("modes.ask"),
        auto: i18n.t("modes.auto"),
        plan: i18n.t("modes.plan"),
        bypassPermissions: i18n.t("modes.full_access"),
        acceptEdits: "accept edits",
      } as Record<string, string>
    )[pm] ?? pm
  );
}

const modeNames: Record<number, () => string> = {
  [ApprovalMode.ASK]: () => i18n.t("modes.ask"),
  [ApprovalMode.AUTO]: () => i18n.t("modes.auto"),
  [ApprovalMode.PLAN]: () => i18n.t("modes.plan"),
  [ApprovalMode.FULL_ACCESS]: () => i18n.t("modes.full_access"),
};

export function modeName(m: ApprovalMode): string {
  return modeNames[m]?.() ?? "";
}

const permissionModes: Record<string, ApprovalMode> = {
  default: ApprovalMode.ASK,
  auto: ApprovalMode.AUTO,
  plan: ApprovalMode.PLAN,
  bypassPermissions: ApprovalMode.FULL_ACCESS,
};
const modePermissions: Record<number, string> = Object.fromEntries(Object.entries(permissionModes).map(([permission, mode]) => [mode, permission]));
const modeClass: Record<number, string> = {
  [ApprovalMode.PLAN]: "m-plan",
  [ApprovalMode.FULL_ACCESS]: "m-danger",
  [ApprovalMode.AUTO]: "m-auto",
  [ApprovalMode.ASK]: "",
};

export interface ModeChip {
  cls: string;
  text: string;
  mode: ApprovalMode;
}

/** The approval mode, read from the latest turn context. */
export function modeOf(s: SessionLike): ModeChip | undefined {
  const i = s.info;
  if (!i || i.status === SessionStatus.STARTING) return undefined;
  // Session.mode is either CLI's mode as a client mode; UNSPECIFIED with a
  // permission mode is one of Claude Code's own, shown by its name.
  const text = i.mode
    ? modeName(i.mode)
    : i.permissionMode
      ? permissionModeName(i.permissionMode)
      : "";
  const mode = i.mode || permissionModes[i.permissionMode] || ApprovalMode.UNSPECIFIED;
  return text
    ? { cls: modeClass[mode] ?? "", text, mode }
    : undefined;
}

/** Whether the daemon marks every reported model as lacking auto approval mode. */
export function allAutoUnavailable(models: ModelInfo[] | undefined): boolean {
  return !!models && models.length > 0 && models.every((m) => m.autoUnavailable);
}

/** Whether the daemon reports auto approval as unavailable for a model.
 * An unknown model or a list not read yet gives no answer. */
export function autoUnavailable(
  models: ModelInfo[] | undefined,
  model: string,
): boolean {
  return allAutoUnavailable(models) || !!findModel(models, model)?.autoUnavailable;
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
export function findModel<M extends { slug: string }>(
  models: M[] | undefined,
  id: string,
): M | undefined {
  if (!models || !id) return undefined;
  // A "[1m]" suffix names the 1M window on the command line; it is not part of the ID.
  id = id.replace(/\[1m\]$/, "");
  return (
    models.find((m) => m.slug === id) ?? models.find((m) => modelIs(id, m.slug))
  );
}

/** The standard context window, which a Claude Code session can be held to. */
export const STANDARD_WINDOW = 200_000n;

/** A model's context window as the pickers write it: 1M 上下文, 200k 上下文; empty when unknown. */
export function contextText(tokens: bigint | undefined): string {
  const n = Number(tokens ?? 0n);
  if (n <= 0) return "";
  return i18n.t("models.context_size", { size: n >= 1_000_000 ? `${+(n / 1_000_000).toFixed(1)}M` : `${Math.round(n / 1000)}k` });
}

/** The name to show for a model: its display name from the machine's
 * list; the ID as is while the list has not come; and for a Codex model
 * missing from the list (the machine's default may be one Codex hides
 * from /model), the name Codex gives such slugs: gpt-6.1-sol is
 * GPT-6.1-Sol. */
export function modelLabel<M extends { slug: string; displayName: string }>(
  models: M[] | undefined,
  id: string,
  agent: Agent = DEFAULT_AGENT,
): string {
  if (!id) return "";
  const m = findModel(models, id);
  if (m?.displayName) return m.displayName;
  if (!models) return id;
  return agentInfo(agent).modelLabel(id);
}

/** Whether the model a CLI reports (id) is the one chosen by slug or
 * alias. */
export function modelIs(id: string, slug: string): boolean {
  return (
    !!id &&
    !!slug &&
    (id === slug ||
      id
        .toLowerCase()
        .split(/[-_.\s]/)
        .includes(slug.toLowerCase()))
  );
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
  standardContext?: boolean; // Claude Code: the standard context window instead of 1M
  from?: SessionLike; // the session copied, if any
}

/** A project's latest session supplies its initial settings: the machine
 * and the CLI. Each CLI's model and effort come from the project's latest
 * session on that CLI (agent: which CLI to answer for; the latest
 * session's by default), the machine's defaults if it has none. The
 * approval mode is never inherited: a new task starts with the machine's
 * default. An offline machine stays selected until the user picks another
 * for this start. */
export function inheritConfig(
  p: Project<SessionLike> | undefined,
  machines: Map<string, MachineLike>,
  cwd = p?.key ?? "",
  agent?: Agent,
): NewConfig | undefined {
  // In the order of the listing, live sessions before ended ones: the
  // listing is fetched in that order, so the pages held always include
  // the session inherited from.
  const order = p
    ? [
        ...p.sessions.filter((s) => !exited(s)).sort(compareLive),
        ...p.sessions.filter(exited).sort(compareEnded),
      ]
    : [];
  const latest = order[0];
  if (!p || !latest) return undefined;
  const a = agent ?? agentOf(latest);
  const from = order.find((s) => agentOf(s) === a);
  void machines;
  return {
    machineId: latest.machineId,
    cwd,
    agent: a,
    model: from?.info?.model ?? "",
    effort: from?.info?.effort ?? "",
    mode: ApprovalMode.UNSPECIFIED,
    from,
  };
}

/** The new session's configuration on another machine: its CLI if the
 * machine has it, else one it has; the model and mode are that machine's
 * defaults again. */
export function switchMachine(cfg: NewConfig, m: MachineLike): NewConfig {
  const agents = machineAgents(m);
  return {
    ...cfg,
    machineId: m.id,
    agent: agents.includes(cfg.agent) ? cfg.agent : agents[0],
    model: "",
    effort: "",
    mode: ApprovalMode.UNSPECIFIED,
  };
}

/** What keeps a session from starting in AUTO with this CLI and model:
 * "account" when no model of the account has the auto mode, "model" when
 * only this one lacks it, "" when nothing does. */
export function autoIssueKind(
  agent: Agent,
  model: string,
  mode: ApprovalMode,
  models?: ModelInfo[],
): "" | "model" | "account" {
  if (
    !agentInfo(agent).autoMayBeUnavailable ||
    mode !== ApprovalMode.AUTO ||
    !autoUnavailable(models, model)
  )
    return "";
  return allAutoUnavailable(models) ? "account" : "model";
}

/** Why a session cannot start with this CLI, model and mode, or "". This
 * is the wording for a scheduled task, whose mode is chosen with it; a new
 * task has no mode of its own and words it in the new task page. */
export function newConfigIssue(
  agent: Agent,
  model: string,
  mode: ApprovalMode,
  models?: ModelInfo[],
): string {
  const kind = autoIssueKind(agent, model, mode, models);
  return kind === "account"
    ? i18n.t("modes.account_no_auto")
    : kind === "model"
      ? i18n.t("modes.model_no_auto")
      : "";
}

/** The mode a CLI gives a session started without one on a machine, as the
 * daemon reads it (AgentInfo.default_mode): its settings, else the CLI's
 * own configuration. mode UNSPECIFIED with known is a mode outside the four
 * (accept edits); not known is an unreadable config. */
export interface DefaultMode {
  mode: ApprovalMode;
  known: boolean;
  varies: boolean; // the folder's project settings may change it (Claude Code)
}

export function defaultModeOf(
  m: MachineLike | undefined,
  agent: Agent,
): DefaultMode {
  const want = agentInfo(agent).agent;
  const info = m?.meta?.agents.find(
    (a) => agentInfo(a.agent).agent === want,
  );
  const known =
    !!info &&
    (info.defaultModeSource === DefaultModeSource.DAEMON_SETTINGS ||
      info.defaultModeSource === DefaultModeSource.CLI_CONFIG);
  return {
    mode: known ? info!.defaultMode : ApprovalMode.UNSPECIFIED,
    known,
    varies: !!info?.defaultModeVaries,
  };
}

/** The mode a new session will start in, as the card writes it. */
export function modeLabel(
  chosen: ApprovalMode,
  d: DefaultMode,
  agent: Agent,
  includeDefault = true,
): string {
  if (chosen) return modeName(chosen);
  const base = !d.known
    ? i18n.t("new_session.follow_cli")
    : d.mode
      ? includeDefault ? i18n.t("modes.default_named", { mode: modeName(d.mode) }) : modeName(d.mode)
      : includeDefault ? i18n.t("modes.default_agent", { agent: agentName(agent) }) : i18n.t("modes.agent_own", { agent: agentName(agent) });
  return d.varies ? i18n.t("modes.default_varies", { mode: base }) : base;
}

// --- events ---

export interface RunningTool {
  title: string;
  since: number;
  callId: string;
  // As the finished call will be; known at the start for Claude Code.
  kind: ToolKind;
  itemType: string;
  input: string;
  inputDigest: string;
  seq: bigint; // the ToolStarted event's
}

export type TerminalChange = { kind: "mode"; permission: string } | { kind: "model"; model: string; effort: string };

export interface Derived {
  approvals: Map<string, ApprovalRequested>; // pending, in request order
  resolved: Map<string, { decision: ApprovalDecision; message: string }>;
  questions: Map<string, QuestionAsked>; // pending
  answered: Map<string, string[]>;
  // Who answered an approval or a question, as the daemon says.
  by: Map<string, Origin>;
  inputs: Map<string, InputStatus>; // latest state per request ID, in first-seen order
  inputSeqs: Map<string, bigint>; // last inputState event per request ID
  // What the computer accepted for each message, as its first QUEUED state
  // says: the text and images, and who sent it; so every client shows
  // the same queue.
  accepted: Map<
    string,
    {
      text: string;
      imageIds: string[];
      files?: SentFile[];
      origin?: Origin;
      mode: SendMode;
    }
  >;
  paused: Set<string>;
  stages: Map<string, InputStage>; // how far a queued message got
  inputReasons: Map<string, string>; // why a message failed
  // Messages the session ended before writing: canceled for that reason
  // ("exited"). Never sent; not waiting either.
  unsent: Set<string>;
  running: boolean;
  runningSeq: bigint; // last event that changed running
  scheduledClosed: boolean;
  idleClosed: boolean;
  usage?: TokenUsage;
  plan?: PlanProposed; // the latest plan
  tools: RunningTool[]; // started and not completed, oldest first
  // How each ToolStarted ended: its ToolCall's seq, or "unsettled".
  toolEnds: Map<bigint, bigint | "unsettled">;
  // Whether the turn an unsettled tool belonged to was interrupted.
  abortedTools: Set<bigint>;
  // User messages typed in the terminal rather than sent from a client.
  typedLocally: Set<bigint>;
  // Mode or model changes made in the terminal, by the seq of the context
  // that shows them.
  terminalChanges: Map<bigint, TerminalChange[]>;
  // Context use before and after each compaction, by the event's seq.
  compactions: Map<bigint, { before?: number; after?: number }>;
  // Unambiguous approval IDs associated with completed ToolCall event seqs.
  approvalCallIds: Map<bigint, string>;
}

type AcceptedEntry = {
  text: string;
  imageIds: string[];
  files?: SentFile[];
  origin?: Origin;
  mode: SendMode;
};

const deriveInputs = new WeakMap<Derived, { events: Event[]; agent: Agent }>();

/** Serializes an ordered slice of events as a core EventBatch. */
export function encodeEventBatch(events: readonly Event[]): Uint8Array {
  if (events.length === 0) return new Uint8Array(0);
  return toBinary(EventBatchSchema, create(EventBatchSchema, { events: [...events] }));
}

/** Looks up the raw event slice and agent passed to derive(), for stateless currentDerived calls. */
export function deriveSource(d: Derived): { events: Event[]; agent: Agent } | undefined {
  return deriveInputs.get(d);
}

/** Converts a Go ProjectionState protobuf message into the web view model. */
export function fromProjectionState(state: ProjectionState): Derived {
  const approvals = new Map<string, ApprovalRequested>();
  for (const p of state.approvals) {
    if (p.request) approvals.set(p.approvalId, p.request);
  }
  const resolved = new Map<string, { decision: ApprovalDecision; message: string }>();
  const by = new Map<string, Origin>();
  for (const [id, r] of Object.entries(state.resolved)) {
    resolved.set(id, { decision: r.decision, message: r.message });
    if (r.origin) by.set(id, r.origin);
  }
  const questions = new Map<string, QuestionAsked>();
  for (const q of state.questions) {
    if (q.request) questions.set(q.questionId, q.request);
  }
  const answered = new Map<string, string[]>();
  for (const [id, a] of Object.entries(state.answered)) {
    answered.set(id, a.answers);
    if (a.origin) by.set(id, a.origin);
  }
  const inputs = new Map<string, InputStatus>();
  const inputSeqs = new Map<string, bigint>();
  const accepted = new Map<string, AcceptedEntry>();
  const paused = new Set<string>();
  const stages = new Map<string, InputStage>();
  const inputReasons = new Map<string, string>();
  for (const inp of state.inputs) {
    inputs.set(inp.requestId, inp.status);
    inputSeqs.set(inp.requestId, inp.lastSeq);
    if (inp.accepted) {
      accepted.set(inp.requestId, {
        text: inp.accepted.text,
        imageIds: inp.accepted.imageIds,
        ...(inp.accepted.files.length ? { files: inp.accepted.files } : {}),
        ...(inp.accepted.origin ? { origin: inp.accepted.origin } : {}),
        mode: inp.detail?.mode ?? SendMode.UNSPECIFIED,
      });
    }
    if (inp.detail) {
      if (inp.detail.paused) paused.add(inp.requestId);
      const stage = inp.accepted?.stage || inp.detail.stage;
      if (stage) stages.set(inp.requestId, stage);
    }
    if (inp.failureReason !== undefined) {
      inputReasons.set(inp.requestId, inp.failureReason);
    }
  }
  const unsent = new Set<string>(state.unsent.map((u) => u.requestId));
  const tools: RunningTool[] = state.tools.map((t) => ({
    title: t.title,
    since: Number(t.sinceMs),
    callId: t.callId,
    kind: t.kind,
    itemType: t.itemType,
    input: t.input,
    inputDigest: t.inputDigest,
    seq: t.seq,
  }));
  const toolEnds = new Map<bigint, bigint | "unsettled">();
  for (const [seqStr, endSeq] of Object.entries(state.completedAt)) {
    toolEnds.set(BigInt(seqStr), endSeq);
  }
  for (const seq of state.unfinishedSeqs) {
    toolEnds.set(seq, "unsettled");
  }
  const noResult = new Set(state.noResultSeqs);
  const abortedTools = new Set<bigint>();
  for (const seq of state.unfinishedSeqs) {
    if (!noResult.has(seq)) abortedTools.add(seq);
  }
  const typedLocally = new Set(state.typedLocallySeqs);
  const terminalChanges = new Map<bigint, TerminalChange[]>();
  for (const [seqStr, list] of Object.entries(state.terminalChanges)) {
    const mapped: TerminalChange[] = [];
    for (const ch of list.changes) {
      if (ch.kind.case === "mode") {
        const m = ch.kind.value;
        mapped.push({
          kind: "mode",
          permission: m.mode ? (modePermissions[m.mode] ?? "") : m.rawMode,
        });
      } else if (ch.kind.case === "model") {
        mapped.push({
          kind: "model",
          model: ch.kind.value.model,
          effort: ch.kind.value.effort,
        });
      }
    }
    if (mapped.length) terminalChanges.set(BigInt(seqStr), mapped);
  }
  const compactions = new Map<bigint, { before?: number; after?: number }>();
  for (const [seqStr, c] of Object.entries(state.compactions)) {
    compactions.set(BigInt(seqStr), {
      ...(c.beforePercent !== undefined ? { before: c.beforePercent } : {}),
      ...(c.afterPercent !== undefined ? { after: c.afterPercent } : {}),
    });
  }
  const approvalCallIds = new Map<bigint, string>();
  for (const [seqStr, approvalId] of Object.entries(state.approvalCallIds)) {
    approvalCallIds.set(BigInt(seqStr), approvalId);
  }
  return {
    approvals,
    resolved,
    questions,
    answered,
    by,
    inputs,
    inputSeqs,
    accepted,
    paused,
    stages,
    inputReasons,
    unsent,
    running: state.running,
    runningSeq: state.runningSeq,
    scheduledClosed: state.scheduledClosed,
    idleClosed: state.idleClosed,
    usage: state.usage,
    plan: state.plan,
    tools,
    toolEnds,
    abortedTools,
    typedLocally,
    terminalChanges,
    compactions,
    approvalCallIds,
  };
}

/** Folds a session's events into what is pending and what happened via the Go WASM core. */
export function derive(events: Event[], agent: Agent = DEFAULT_AGENT): Derived {
  const bytes = getCore().reduce(agent, encodeEventBatch(events));
  const d = fromProjectionState(fromBinary(ProjectionStateSchema, bytes));
  deriveInputs.set(d, { events, agent });
  return d;
}

/** How long a tool has run, from the daemon's start time. While this
 * client is cut off from the server (since lostAt) the reading stops
 * there: it cannot tell whether the tool still runs. An unknown start is
 * not guessed. */
export function toolClock(
  since: number,
  now: number,
  lostAt: number | undefined,
): { ms: number; frozen: boolean } | undefined {
  if (!since) return undefined;
  const frozen = lostAt !== undefined;
  return {
    ms: Math.max(0, (frozen ? Math.min(now, lostAt) : now) - since),
    frozen,
  };
}

/** m:ss, as a frozen or full tool clock is written. */
export function clock(ms: number): string {
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** A running tool's short name: the CLI's tool name (Bash), or for
 * Codex, which gives none, 命令 or 工具. */
export function toolName(t: Pick<RunningTool, "itemType" | "kind">): string {
  return (
    t.itemType ||
    (t.kind === ToolKind.COMMAND || t.kind === ToolKind.UNSPECIFIED
      ? i18n.t("tools.command")
      : i18n.t("tools.tool"))
  );
}

export function eventTime(e: Event): number {
  return e.time ? timestampMs(e.time) : 0;
}

export function decisionText(d: ApprovalDecision): string {
  switch (d) {
    case ApprovalDecision.ALLOW:
      return i18n.t("approval.allowed");
    case ApprovalDecision.ALLOW_ALWAYS:
      return i18n.t("approval.always_allowed");
    case ApprovalDecision.DENY:
      return i18n.t("approval.denied");
    case ApprovalDecision.TIMEOUT:
      return i18n.t("approval.timed_out");
    case ApprovalDecision.CANCELED:
      return i18n.t("queue.canceled");
    case ApprovalDecision.TERMINAL:
      return i18n.t("approval.in_terminal");
  }
  return "";
}

export function alwaysText(message: string): string {
  const command = message.match(
    /commands that start with [\x60\x27“]?(.+?)[\x60\x27”]?(?:\s*$)/i,
  );
  if (command)
    return i18n.t("approval.always_prefix", { command: command[1].replace(/[\x60\x27”]$/, "") });
  const dir = message.match(
    /always allow access to (.+?)(?: from this project)?\s*$/i,
  );
  if (dir) return i18n.t("approval.always_directory", { path: dir[1] });
  if (/accept edits|automatically accept edits/i.test(message))
    return i18n.t("approval.auto_edits");
  return message;
}

/** Splits the "(Recommended)" marker off an option label. */
export function optionLabel(label: string): {
  text: string;
  recommended: boolean;
} {
  const m = label.match(/^(.*?)\s*\((Recommended|推荐)\)\s*$/i);
  return m
    ? { text: m[1], recommended: true }
    : { text: label, recommended: false };
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
export function dockItems(
  s: SessionLike,
  d: Derived,
  machine?: MachineLike,
): DockItem[] {
  const st = stateOf(s, machine);
  if (st.kind === "ended" || st.kind === "offline") return [];
  const out: DockItem[] = [];
  for (const a of d.approvals.values())
    out.push({ kind: "approval", id: "a:" + a.approvalId, approval: a });
  for (const q of d.questions.values())
    out.push({ kind: "question", id: "q:" + q.questionId, question: q });
  if (s.info?.planOpen && d.plan)
    out.push({ kind: "plan", id: "plan", plan: d.plan });
  if (s.info?.status === SessionStatus.BLOCKED)
    out.push({ kind: "popup", id: "popup", title: s.info.statusDetail });
  return out;
}

// --- messages sent from this client ---

/** A message sent from this page. sendId goes in SendRequest and makes
 * the daemon queue the text once; requestId and deadline seal the command,
 * and a retry reuses them so the daemon returns its first answer. */
export interface Outgoing {
  prepared?: import("./remote").PreparedPayload;
  sendId: string;
  requestId: string;
  deadline: number;
  text: string;
  mode?: SendMode;
  // Images uploaded to the machine for this message (their image IDs).
  imageIds?: string[];
  // This device's copies of them, shown until the CLI records the message.
  thumbs?: Blob[];
  imageSizes?: { width: number; height: number }[];
  attachments?: { original: boolean; files: Blob[] };
  files?: { fileId: string; name: string; sizeBytes: bigint; path: string; file?: Blob }[];
  missingImages?: boolean;
  status: "sending" | "sent" | "failed";
  error?: string;
  // A try may have reached the computer: retries keep this request.
  unknown?: boolean;
  // A verified missing result or identity conflict supplies its complete caption.
  outcomeNotice?: boolean;
}

export type Phase =
  | "sending" // on its way to the computer
  | "delivered" // the computer has it, nothing more heard yet
  | "queued" // waiting in the daemon's queue: shown as a queue chip
  | "failed" // not confirmed to have arrived: retry is safe
  | "unconfirmed" // typed into the terminal, not confirmed: no retry
  | "done" // the CLI recorded it: shown as a normal message
  | "canceled"
  | "notSent" // failed before Enter; the terminal is as it was
  | "needsTerminal"; // failed, and the terminal may hold part of it

/** Where a message stands, from the send call and the daemon's events. */
export function phaseOf(
  o: Pick<Outgoing, "status">,
  input?: InputStatus,
): Phase {
  switch (input) {
    case InputStatus.SUBMITTED:
      return "done";
    case InputStatus.UNCONFIRMED:
      return "unconfirmed";
    case InputStatus.CANCELED:
      return "canceled";
    case InputStatus.QUEUED:
    case InputStatus.HELD_BY_CLI:
      return "queued";
    case InputStatus.FAILED:
      return "notSent";
    case InputStatus.NEEDS_TERMINAL:
      return "needsTerminal";
  }
  if (o.status === "failed") return "failed";
  return o.status === "sending" ? "sending" : "delivered";
}

/** Why queued messages wait, in the mockup's words. */
export function queueReason(s: SessionLike, d: Derived): string {
  const i = s.info;
  if (i?.inputWait === "unrecognized") return i18n.t("queue.unrecognized");
  if (i?.inputWait === "draft") return i18n.t("queue.terminal_draft");
  if (i?.status === SessionStatus.BLOCKED || i?.inputWait === "popup")
    return i18n.t("queue.when_ready");
  if (d.questions.size > 0 || d.approvals.size > 0) return i18n.t("queue.when_ready");
  if (i?.planOpen) return i18n.t("queue.when_ready");
  if (i?.status === SessionStatus.STARTING) return i18n.t("queue.after_start");
  if (d.running) return i18n.t("queue.after_current_turn");
  return "";
}

/** Messages waiting in the daemon's queue, in the order it accepted
 * them: queued, and not left behind by a session that ended. skip is the
 * first message of a session started here, which the chat shows. */
export function queuedIds(d: Derived, skip?: string): string[] {
  return [...d.inputs.entries()]
    .filter(
      ([id, st]) =>
        (st === InputStatus.QUEUED || st === InputStatus.HELD_BY_CLI) &&
        !d.unsent.has(id) &&
        id !== skip,
    )
    .map(([id]) => id);
}

/** Whether a message's state says the session ended before writing it:
 * canceled by the daemon, not by anyone, and offered to be put back. */
export function endedUnsent(state: {
  status: InputStatus;
  reason: string;
}): boolean {
  return state.status === InputStatus.CANCELED && state.reason === "exited";
}

/** Why a message was not written, from the daemon's reason code. */
export function inputFailure(reason: string, agent: Agent): string {
  switch (reason) {
    case "image_not_recognized":
      return i18n.t("composer.images_unrecognized", { agent: agentName(agent) });
    case "input_changed":
      return i18n.t("composer.input_unconfirmed");
    case "timeout":
      return i18n.t("composer.ready_timeout");
    case "restarted":
      return i18n.t("composer.computer_restarted");
    case "write_failed":
      return i18n.t("composer.write_failed");
  }
  return i18n.t("composer.not_written");
}

// --- the chat timeline ---

export type Item =
  // decision: an approval's result shown on the tool call it was for.
  | {
      kind: "event";
      key: string;
      event: Event;
      decision?: ApprovalDecision;
      approvalId?: string;
    }
  | { kind: "tools"; key: string; calls: ToolCall[] }
  | { kind: "turn"; key: string; ms: number }
  // A tool that started and has no result yet, and one the turn ended
  // without a result for.
  | { kind: "running"; key: string; tool: RunningTool }
  | { kind: "unsettled"; key: string; tool: RunningTool; aborted: boolean }
  // A call that went on in the background: started, end unknown.
  | { kind: "background"; key: string; call: ToolCall };

/** Whether an event draws anything in the chat. */
function visible(e: Event, d: Derived): boolean {
  const k = e.kind;
  switch (k.case) {
    case "userMessage":
    case "agentMessage":
    case "reasoning":
    case "toolCall":
    case "planProposed":
    case "toolStarted":
    case "planAnswered":
    case "imageGenerated":
    case "sessionForked":
    case "scheduledNotification":
      return true;
    case "turnEnded":
      return (
        k.value.outcome === TurnOutcome.ABORTED || k.value.durationMs >= 1000n
      );
    case "approvalRequested":
      return d.resolved.has(k.value.approvalId);
    case "questionAsked":
      return k.value.async || d.answered.has(k.value.questionId);
    case "inputState":
      // A message never sent shows once, where it was queued.
      if (d.unsent.has(k.value.requestId))
        return k.value.status === InputStatus.QUEUED;
      // The CLI's record may come after the daemon stopped waiting for
      // it: the message was submitted after all.
      if (k.value.status === InputStatus.UNCONFIRMED)
        return d.inputs.get(k.value.requestId) === InputStatus.UNCONFIRMED;
      return [
        InputStatus.CANCELED,
        InputStatus.FAILED,
        InputStatus.NEEDS_TERMINAL,
      ].includes(k.value.status);
    case "settingsChanged":
      return k.value.compacted || !!k.value.model || Object.hasOwn(modePermissions, k.value.mode);
    case "turnContext":
      return d.terminalChanges.has(e.seq);
    case "sessionState":
      return k.value.status === SessionStatus.EXITED;
  }
  return false;
}

/** Whether a call went on in the background, as the daemon says. Its
 * event is then its start, not its end. */
export function isBackground(t: ToolCall): boolean {
  return t.background !== ToolBackground.UNSPECIFIED;
}

/** Why a call is in the background, and what that means. */
export function backgroundNote(b: ToolBackground): string {
  switch (b) {
    case ToolBackground.REQUESTED:
      return i18n.t("tools.background_agent");
    case ToolBackground.TIMED_OUT:
      return i18n.t("tools.background_timeout");
    case ToolBackground.BY_USER:
      return i18n.t("tools.background_terminal");
    case ToolBackground.STILL_RUNNING:
      return i18n.t("tools.background_turn");
  }
  return i18n.t("tools.background_returned");
}

export function toolOk(t: ToolCall): boolean {
  return (
    t.status !== "failed" && (t.exitCode === undefined || t.exitCode === 0)
  );
}

/** The chat's rows: runs of two or more completed tool calls in a row
 * fold into one line; events that draw nothing are left out. */
export function timeline(events: Event[], d: Derived, opts?: { sharedFiles?: boolean }): Item[] {
  const allowShared = opts?.sharedFiles ?? false;
  const out: Item[] = [];
  const mergedApprovals = new Set(d.approvalCallIds.values());
  let run: { first: Event; calls: ToolCall[] } | undefined;
  const flush = () => {
    if (!run) return;
    if (run.calls.length > 1)
      out.push({ kind: "tools", key: "t" + run.first.seq, calls: run.calls });
    else
      out.push({ kind: "event", key: String(run.first.seq), event: run.first });
    run = undefined;
  };
  const unsentShown = new Set<string>();
  const bySeq = new Map<bigint, Event>();
  // A call named by its start (Claude Code's parallel calls) is drawn
  // where it started, so its row turns from running into its result in
  // place; the call itself is then skipped where it completed.
  const placed = new Set<bigint>();
  for (const e of events) {
    if (e.kind.case === "toolCall") bySeq.set(e.seq, e);
    if (e.kind.case === "toolStarted" && e.kind.value.callId) {
      const end = d.toolEnds.get(e.seq);
      if (typeof end === "bigint") placed.add(end);
    }
  }
  const running = new Map(d.tools.map((t) => [t.seq, t]));
  const place = (e: Event) => {
    const k = e.kind;
    const approvalId = d.approvalCallIds.get(e.seq);
    const decision = approvalId !== undefined ? d.resolved.get(approvalId)?.decision : undefined;
    if (decision !== undefined) {
      // Shown on its own, so the decision is not folded away.
      flush();
      out.push({
        kind: "event",
        key: String(e.seq),
        event: e,
        decision,
        approvalId,
      });
      return;
    }
    if (k.case === "toolCall" && isBackground(k.value)) {
      flush();
      out.push({ kind: "background", key: "b" + e.seq, call: k.value });
      return;
    }
    if (k.case === "toolCall" && allowShared && toolOk(k.value) && k.value.sharedFiles.length > 0) {
      flush();
      out.push({ kind: "event", key: "e" + e.seq, event: e });
      return;
    }
    if (k.case === "toolCall" && toolOk(k.value)) {
      if (!run) run = { first: e, calls: [] };
      run.calls.push(k.value);
      return;
    }
    flush();
    if (k.case === "turnEnded" && k.value.outcome !== TurnOutcome.ABORTED)
      out.push({
        kind: "turn",
        key: String(e.seq),
        ms: Number(k.value.durationMs),
      });
    else out.push({ kind: "event", key: String(e.seq), event: e });
  };
  for (const e of events) {
    if (isSkeleton(e)) {
      flush();
      out.push({ kind: "event", key: String(e.seq), event: e });
      continue;
    }
    if (!visible(e, d)) continue;
    const k = e.kind;
    if (k.case === "toolStarted") {
      const end = d.toolEnds.get(e.seq);
      if (typeof end === "bigint") {
        if (k.value.callId && bySeq.has(end)) place(bySeq.get(end)!);
        continue;
      }
      const t = running.get(e.seq);
      if (!t && end !== "unsettled") continue;
      flush();
      const tool = t ?? {
        title: k.value.title,
        since: eventTime(e),
        callId: k.value.callId,
        kind: k.value.kind,
        itemType: k.value.itemType,
        input: k.value.input,
        inputDigest: k.value.inputDigest,
        seq: e.seq,
      };
      out.push(
        end === "unsettled"
          ? {
              kind: "unsettled",
              key: "u" + e.seq,
              tool,
              aborted: d.abortedTools.has(e.seq),
            }
          : { kind: "running", key: "r" + e.seq, tool },
      );
      continue;
    }
    if (k.case === "toolCall" && placed.has(e.seq)) continue;
    if (k.case === "inputState" && d.unsent.has(k.value.requestId)) {
      if (unsentShown.has(k.value.requestId)) continue;
      unsentShown.add(k.value.requestId);
    }
    if (
      k.case === "approvalRequested" &&
      mergedApprovals.has(k.value.approvalId)
    )
      continue;
    place(e);
  }
  flush();
  return out;
}

/** "运行了 3 条命令" and the like, for a folded run of tool calls. */
export function toolsSummary(calls: ToolCall[]): string {
  const n = calls.length;
  if (calls.every((c) => c.kind === ToolKind.COMMAND))
    return i18n.t("tools.completed_commands", { count: n });
  if (calls.every((c) => c.kind === ToolKind.FILE_CHANGE))
    return i18n.t("tools.completed_files", { count: n });
  return i18n.t("tools.completed_calls", { count: n });
}

/** The daemon's status detail when a client-created session's login shell
 * could not start the CLI (internal/daemon/shell.go); the timeout one adds
 * the second phrase. */
const loginShellDetail = "the login shell";
const loginShellTimeoutDetail = "did not start the CLI within";

/** Why a session ended, in plain words. */
export function exitReason(
  detail: string,
  scheduledClosed = false,
  idleClosed = false,
): string {
  if (detail === "stopped" && scheduledClosed) return i18n.t("session.scheduled_closed");
  if (detail === "stopped" && idleClosed) return i18n.t("session.idle_closed");
  if (detail === "stopped") return i18n.t("session.manually_closed");
  if (detail.startsWith(loginShellDetail)) {
    return detail.includes(loginShellTimeoutDetail) ? i18n.t("session.shell_timeout", { detail }) : i18n.t("session.shell_failed", { detail });
  }
  return i18n.t("session.tmux_missing");
}

// --- wording that names the CLI ---

export function askTitle(a: Agent): string {
  return t("list_state.agent_asking", { agent: agentName(a) });
}

export function inputTimingLabel(agent: Agent, mode: SendMode | undefined, held: boolean, starting?: string): string {
  if (!agentInfo(agent).steersDuringTurn) return starting ? t("queue.agent_ready", { agent: starting }) : t("queue.after_turn");
  if (held) return t("queue.insert");
  if (mode === SendMode.QUEUE) return t("queue.after_turn");
  return starting ? t("queue.agent_ready", { agent: starting }) : t("queue.insert");
}
