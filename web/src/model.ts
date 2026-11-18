import * as l10n from "./i18n";
import * as i18n from "./i18n";
import { t } from "./i18n";
import { agentInfo, agentName, DEFAULT_AGENT, isKnownAgent } from "./agents";
import { timestampMs } from "@bufbuild/protobuf/wkt";
import {
  Agent,
  ApprovalMode,
  SessionStatus,
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

const compareProjects = (
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

export function exited(s: SessionLike): boolean {
  return s.info?.status === SessionStatus.EXITED;
}

function needsAttention(s: SessionLike): boolean {
  const i = s.info;
  return !!i && i.status !== SessionStatus.EXITED && (i.pendingApprovals > 0 || i.pendingQuestions > 0 || i.planOpen || i.status === SessionStatus.BLOCKED);
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

function tablePreview(text: string): string {
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
function when(ms: number | bigint, now = Date.now()): string {
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

export function hhmm(ms: number): string {
  const d = new Date(ms);
  return `${d.getHours()}:${String(d.getMinutes()).padStart(2, "0")}`;
}

export function clock(ms: number): string {
  const s = Math.floor(ms / 1000);
  const m = Math.floor(s / 60);
  const h = Math.floor(m / 60);
  return h > 0
    ? `${h}:${String(m % 60).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`
    : `${m}:${String(s % 60).padStart(2, "0")}`;
}

/** A future local time in Chinese, independent of the browser's locale.
 * Compare calendar dates so midnight, month/year changes and DST stay correct.
 * Pairing expiry uses only the zero-padded 24-hour clock. */
export function futureTime(ms: number | bigint, now = Date.now(), format: "date-time" | "time" = "date-time"): string {
  const time = Number(ms);
  const clockStr = hhmm(time).padStart(5, "0");
  if (format === "time") return clockStr;
  const date = new Date(time);
  const today = new Date(now);
  const tomorrow = new Date(now);
  tomorrow.setDate(tomorrow.getDate() + 1);
  const label = date.toDateString() === today.toDateString() ? l10n.t("time.today")
    : date.toDateString() === tomorrow.toDateString() ? l10n.t("time.tomorrow")
    : l10n.t("time.month_day", { month: date.getMonth() + 1, day: date.getDate() });
  return `${label} ${clockStr}`;
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

/** One whole path shortened by the same rules, spaces and all, as a file
 * edit's title is; unchanged if it is not under the session or home
 * folder. */
export function shortenPath(path: string, cwd: string, home = ""): string {
  const base = samePlace(cwd).replace(/\/+$/, "");
  const h = samePlace(home).replace(/\/+$/, "");
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

// --- modes and chips ---

function permissionModeName(pm: string): string {
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
function modelIs(id: string, slug: string): boolean {
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
