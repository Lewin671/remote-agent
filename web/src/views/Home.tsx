import { t } from "../i18n";
// The home page: a timeline of sessions within a scope (one project, or
// all of them), headed by one sentence of how things stand; its wide form
// as the sidebar; the bottom row of tabs; and the list rows they share
// (design-system §6, client-v58 #home).

import { AgentMark } from "../agent-mark";
import { agentName } from "../agents";
import { Suspense, useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import {
  agentOf,
  askTitle,
  compareAttention,
  compareLive,
  compareProjects,
  compareEnded,
  dayGroup,
  endedMs,
  endedTime,
  exited,
  machineName,
  needsAttention,
  previewOf,
  projectKey,
  projectName,
  shortPath,
  shownStateOf,
  stateOf,
  titleOf,
  when,
} from "../model";
import { isHidden } from "../prefs";
import type { Remote, SessionView } from "../remote";
import { ALL, resolveScope, savedScope, saveScope, subscribeScope } from "../scope";
import { frequency, unreadLabel } from "../schedules";
import { approvalDisplay } from "../approval-display";
import { Avatar, Banner, GlassButton, Segmented, Sheet, Skeleton, StateMark, useDialogFocus, useEscape, useNow, useRemote } from "../ui";
import { ScheduleBadge } from "./ScheduleBadge";
import { SchedulesPage } from "./pages";
import { SessionLine } from "./SessionLine";
import { useFresh } from "../motion";
import { authorityName, RecoveryNotice } from "./authority-notice";
import { useDerived } from "./actions";
import { ApprovalCard } from "./ApprovalCard";
import { approvalTitle } from "./Dock";
import { NewBox, NewFlow, useNewSession, type Go } from "./NewSession";
import { ConnCheck, connWord } from "./ConnCheck";
import { PrefsBanner, ProjectItem, ProjectList, RemovedNotice, sessionCounts, useProjectGroups } from "./Projects";
import { applyUpdate, useUpdate } from "../update";
import { Check, ChevronDown, ChevronRight, Clock, MessageCircle, Plus, Search, Settings, SquarePen, X } from "../icons";

export type { Go };

function emphasizeCount(text: string, count: number) {
  const at = text.indexOf(String(count));
  return <>{text.slice(0, at)}<em>{count}</em>{text.slice(at + String(count).length)}</>;
}

function commandNote(text: string) {
  const command = "remote-agent daemon";
  const at = text.indexOf(command);
  return <>{text.slice(0, at)}<code>{command}</code>{text.slice(at + command.length)}</>;
}

/** Which of a row's attributes differ within the list it is in. A row
 * names only those: what every row shares is noise. */
export type Cols = { project?: boolean; agent?: boolean; machine?: boolean };

/** The third line of a row: 分叉, then what its list does not share. The
 * CLI is not among the words: its mark leads the line. */
function metaOf(remote: Remote, s: SessionView, cols: Cols): string {
  return [
    s.info?.forkedFrom ? t("home.forked") : "",
    cols.project ? projectName(projectKey(s)) : "",
    cols.machine ? machineName(remote.machines.get(s.machineId)) : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

/** The columns of a list of sessions: the CLI when they used more than
 * one, the machine when the account has more than one. */
export function colsOf(remote: Remote, sessions: SessionView[], project: boolean): Cols {
  return {
    project,
    agent: new Set(sessions.map(agentOf)).size > 1,
    machine: remote.machines.size > 1,
  };
}

/** The scope shown, and the listed projects it is picked from. */
function useScope(remote: Remote) {
  const groups = useProjectGroups(remote);
  const list = [...groups.pinned, ...groups.others];
  const saved = useSyncExternalStore(subscribeScope, savedScope);
  return {
    scope: resolveScope(
      saved,
      list.map((p) => p.key),
    ),
    list,
    groups,
  };
}

/** Sessions waiting for the user, the most urgent first: approvals,
 * questions, plans, popups, and within each the longest wait first. */
export function todoList(remote: Remote): SessionView[] {
  return [...remote.sessions.values()]
    .filter((s) => needsAttention(s, remote.presence.get(s.machineId)))
    .sort((a, b) => compareAttention(a, b, remote.presence));
}

/** Live sessions: not ended, and not already among what waits. */
function liveList(remote: Remote): SessionView[] {
  return [...remote.sessions.values()].filter((s) => !s.info?.taskOwned)
    .filter(
      (s) => !exited(s) && !needsAttention(s, remote.presence.get(s.machineId)),
    )
    .sort((a, b) => compareLive(a, b));
}

function pendingOf(remote: Remote, sid: string) {
  // The few waiting sessions have their events loaded anyway, for the rows.
  const d = remote.currentDerived(sid, remote.historicalDerived(sid));
  return {
    approval: [...d.approvals.values()][0],
    question: [...d.questions.values()][0],
  };
}

/** What waits, as one row of the gray list: the orange dot, the title,
 * then 「类型 · 摘要」 with the type in orange; it opens the session, where
 * the dock handles it. Another project's item names its project. */
function PendingRow({ remote, s, go, scope }: { remote: Remote; s: SessionView; go: Go; scope: string }) {
  useEffect(() => remote.retainHistory(s.id), [remote, s.id]);
  const st = stateOf(s, remote.presence.get(s.machineId));
  const p = pendingOf(remote, s.id);
  const kind =
    st.kind === "approval"
      ? p.approval
        ? approvalTitle(p.approval)
        : st.text
      : st.kind === "question"
        ? askTitle(agentOf(s))
        : st.text;
  const about =
    st.kind === "approval"
      ? p.approval?.command
        ? approvalDisplay(p.approval.command).replace(/\s+/g, " ")
        : ""
      : st.kind === "question"
        ? p.question?.questions[0]?.text ?? ""
        : st.kind === "popup"
          ? s.info?.statusDetail ?? ""
          : "";
  const other = scope !== ALL && projectKey(s) !== scope ? projectName(projectKey(s)) : "";
  const rest = [other, about].filter(Boolean).join(" · ");
  return (
    <button className="pr" onClick={() => go("s/" + s.id)}>
      <StateMark kind="wait" />
      <span className="nb">
        <b>{titleOf(s)}</b>
        <span>
          <i>{kind}</i>
          {rest && ` · ${rest}`}
        </span>
      </span>
      <ChevronRight className="chev" />
    </button>
  );
}

/** The most urgent item, when it is an approval: the dark card, handled
 * right here. Until the approval itself is read it is a row like the rest. */
function TopApproval({ remote, s, go, scope }: { remote: Remote; s: SessionView; go: Go; scope: string }) {
  const d = useDerived(remote, s.id);
  const a = [...d.approvals.values()][0];
  if (!a)
    return (
      <div className="pl">
        <PendingRow remote={remote} s={s} go={go} scope={scope} />
      </div>
    );
  // The computer is named only where the account has more than one (design-system §6 终端卡).
  const where = [projectName(projectKey(s)), remote.machines.size > 1 ? machineName(remote.machines.get(s.machineId)) : ""].filter(Boolean).join(" · ");
  // Keyed by approval: the next one must not inherit this one's locked buttons.
  return <ApprovalCard key={a.approvalId} remote={remote} sid={s.id} a={a} title={titleOf(s)} where={where} onOpen={() => go("s/" + s.id)} timeoutNote />;
}

/** A live session in a list: the state graphic, 「状态 · 内容」 with the
 * state word in ink2 like the rest, then whatever its list does not share. */
export function SessionRow({
  remote,
  s,
  go,
  selected,
  cols = {},
  last,
  enter,
}: {
  remote: Remote;
  s: SessionView;
  go: Go;
  selected?: boolean;
  cols?: Cols;
  last?: boolean;
  enter?: boolean;
}) {
  const st = shownStateOf(s, remote.presence.get(s.machineId), remote.unlinked && !remote.awaiting);
  // Waiting sessions preview what they wait for, which is in the events.
  const waiting = st.tone === "wait";
  useEffect(
    () => (waiting ? remote.retainHistory(s.id) : undefined),
    [remote, s.id, waiting],
  );
  if (st.kind === "ended") return <EndedRow s={s} go={go} selected={selected} project={cols.project} />;
  const pv = previewOf(s, st, waiting ? pendingOf(remote, s.id) : undefined);
  const meta = metaOf(remote, s, cols);
  const timers = remote.sessionTasks(s.id);
  const scheduleMeta = timers.length ? timers.length > 1 ? t("home.scheduled_tasks", { count: timers.length }) : t("home.scheduled_frequency", { frequency: frequency(timers[0].task.spec.trigger) }) : "";
  // A mark alone looks unfinished: with nothing else to say, the line names the CLI.
  const words = [meta, scheduleMeta].filter(Boolean).join(" · ");
  return <SessionLine tone={st.tone} title={titleOf(s)} right={when(s.updatedMs)}
    preview={st.text + (pv && pv !== st.text ? ` · ${pv}` : "")}
    meta={(cols.agent || words) && <>{cols.agent && <AgentMark agent={agentOf(s)} named={!words} />}{words || (cols.agent ? agentName(agentOf(s)) : "")}</>} selected={selected} last={last} enter={enter} onClick={() => go("s/" + s.id)} />;
}

/** An ended session: one line with no state graphic, under the date
 * group that says when; under all projects the time names the project. */
export function EndedRow({
  s,
  go,
  selected,
  project,
}: {
  s: SessionView;
  go: Go;
  selected?: boolean;
  project?: boolean;
}) {
  return (
    <button className={"old" + (selected ? " sel" : "")} onClick={() => go("s/" + s.id)}>
      <span className="sm">
        <span>{titleOf(s)}</span>
        <span>
          {project && projectName(projectKey(s)) + " · "}
          {endedTime(endedMs(s))}
        </span>
      </span>
    </button>
  );
}

const PAGE = 30;

/** The sessions of a scope: what waits for the whole account, then the
 * scope's live sessions and its ended ones by date. With more than eight
 * sessions it can be filtered by title. The session on screen stays
 * listed and highlighted even when it is outside the scope. `head` goes
 * above the list, after the filter on a wide window, where the filter
 * stays put and the rest scrolls. */
function Timeline({
  remote,
  go,
  scope,
  selected,
  wide,
  head,
}: {
  remote: Remote;
  go: Go;
  scope: string;
  selected?: string;
  wide?: boolean;
  head?: ReactNode;
}) {
  const now = useNow(30_000);
  const [query, setQuery] = useState("");
  const [allTodo, setAllTodo] = useState(false);
  const [allLive, setAllLive] = useState(false);
  const [shown, setShown] = useState(PAGE);
  const input = useRef<HTMLInputElement>(null);
  const prefs = remote.projectPrefs();
  const q = query.trim().toLocaleLowerCase();
  const match = (s: SessionView) => !q || titleOf(s).toLocaleLowerCase().includes(q);
  const within = (s: SessionView) => scope === ALL || projectKey(s) === scope;
  const inScope = [...remote.sessions.values()].filter((s) => !s.info?.taskOwned).filter(within);
  const todo = todoList(remote).filter(match);
  const live = liveList(remote).filter(within).filter(match);
  // A removed project's ended sessions are not listed under all projects.
  const everEnded = inScope
    .filter((s) => exited(s) && (scope !== ALL || !isHidden(prefs, projectKey(s))))
    .sort(compareEnded);
  const ended = everEnded.filter(match);
  const cols = colsOf(remote, inScope, scope === ALL);
  // Ended sessions come a page at a time: how many the scope has in all
  // is what its machines count, until every page is here.
  const groups = useProjectGroups(remote);
  const counted = [...groups.pinned, ...groups.others]
    .filter((p) => scope === ALL || p.key === scope)
    .reduce((n, p) => n + p.ended, 0);
  const totalEnded = remote.sessionsOpen ? Math.max(counted, everEnded.length) : everEnded.length;
  // Showing past the pages held fetches the next; a page may hold none of
  // this scope's sessions, and then the one after is fetched in turn.
  // Filtering has to see every title, so it fetches all that is left.
  useEffect(() => {
    if (q) void remote.loadAllSessions();
    // A session shown that lies past the pages fetched (kept from before
    // a reconnect, or fetched by its ID) may have others before it that
    // are not here yet.
    else if (
      (shown > everEnded.length && everEnded.length < totalEnded) ||
      everEnded.slice(0, shown).some((s) => remote.pastPages(s))
    )
      void remote.loadMoreSessions();
  });
  const sel = selected ? remote.sessions.get(selected) : undefined;
  // Keep the open session in view where it would have been.
  if (sel && !q && !todo.includes(sel) && !live.includes(sel) && !ended.slice(0, shown).includes(sel)) live.unshift(sel);
  // A phone's home keeps them short; the wide sidebar lists them all and
  // scrolls as one column.
  const cut = <T,>(list: T[], max: number, all: boolean) =>
    all || wide || q || (sel && list.indexOf(sel as T) >= max) ? list : list.slice(0, max);
  const liveShown = cut(live, 5, allLive);
  // A session that comes to the list fades in; the list as it is opened,
  // scoped or searched does not.
  const fresh = useFresh(liveShown.map((s) => s.id), true, { bulk: 2, anywhere: true });
  const endedShown = ended.slice(0, shown);
  // ⌘/Ctrl+K reaches the filter from anywhere on a wide window.
  useEffect(() => {
    if (!wide) return;
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k" && input.current) {
        e.preventDefault();
        input.current.focus();
      }
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [wide]);
  const total = live.length + ended.length + todo.length;
  // On a phone the most urgent approval is the dark card; the rest are
  // rows, three at most before 还有 N 件. The sidebar lists them as rows.
  const top = !wide && todo[0] && stateOf(todo[0], remote.presence.get(todo[0].machineId)).kind === "approval" ? todo[0] : undefined;
  const rows = top ? todo.slice(1) : todo;
  const rowsShown = cut(rows, 3, allTodo);
  let group = "";
  const find = (liveList(remote).filter(within).length + totalEnded > 8 || query) && (
    <label className={"find" + (query ? " typed" : "")}>
      <Search />
      <input
        ref={input}
        aria-label={t("home.filter_title")}
        placeholder={t("home.filter_title")}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && setQuery("")}
      />
      {query ? (
        <button className="x" aria-label={t("home.clear_filter")} onClick={() => setQuery("")}>
          <X />
        </button>
      ) : (
        wide && <kbd>{/Mac|iP/.test(navigator.platform) ? "⌘K" : "Ctrl K"}</kbd>
      )}
    </label>
  );
  const list = (
    <>
      {top && <TopApproval key={top.id} remote={remote} s={top} go={go} scope={scope} />}
      {wide
        ? todo.map((s) => (
            <SessionRow key={s.id} remote={remote} s={s} go={go} cols={{ ...cols, project: scope === ALL || projectKey(s) !== scope }} selected={s.id === selected} />
          ))
        : rows.length > 0 && (
            <div className="pl">
              {rowsShown.map((s) => (
                <PendingRow key={s.id} remote={remote} s={s} go={go} scope={scope} />
              ))}
              {rows.length > rowsShown.length && (
                <button className="pr all" onClick={() => setAllTodo(true)}>
                  {t("home.more_waiting", { count: rows.length - rowsShown.length })}
                </button>
              )}
            </div>
          )}
      {live.length > 0 && <div className="sec">{t("home.live")}</div>}
      {liveShown.map((s) => (
        <SessionRow
          key={s.id}
          remote={remote}
          s={s}
          go={go}
          cols={{ ...cols, project: scope === ALL || projectKey(s) !== scope }}
          selected={s.id === selected}
          enter={fresh(s.id)}
        />
      ))}
      {live.length > liveShown.length && (
        <button className="more-row" onClick={() => setAllLive(true)}>
          {t("home.more_live", { count: live.length - liveShown.length })}
          <ChevronRight />
        </button>
      )}
      {endedShown.map((s) => {
        const g = dayGroup(endedMs(s), now);
        const title = g !== group && <div className="sec d">{g}</div>;
        group = g;
        return (
          <div key={s.id}>
            {title}
            <EndedRow s={s} go={go} project={scope === ALL} selected={s.id === selected} />
          </div>
        );
      })}
      {q ? (
        ended.length > endedShown.length ? (
          <More onMore={() => setShown((n) => n + PAGE)}>
            {t("home.matches_loading", { count: total, query: query.trim() })}
          </More>
        ) : (
          <div className="tail">
            {remote.sessionsOpen
              ? t("home.loading_earlier")
              : total
                ? t("home.matches", { count: total, query: query.trim() })
                : t("home.no_matches", { query: query.trim() })}
          </div>
        )
      ) : totalEnded > endedShown.length ? (
        // Never further than one page past what is held: the rest waits for the server.
        <More onMore={() => setShown((n) => Math.min(n + PAGE, everEnded.length + PAGE))}>
          {t("home.total_load_earlier", { count: totalEnded })}
        </More>
      ) : (
        totalEnded > PAGE && <div className="tail">{t("home.total", { count: totalEnded })}</div>
      )}
    </>
  );
  if (wide)
    return (
      <>
        {find}
        <div className="scroll">
          {head}
          {list}
        </div>
      </>
    );
  return (
    <>
      {head}
      {find}
      {list}
    </>
  );
}

/** The end of a page of ended sessions: reaching it, or clicking it,
 * shows the next page. */
function More({ onMore, children }: { onMore: () => void; children: ReactNode }) {
  const el = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!el.current || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver((entries) => entries.some((e) => e.isIntersecting) && onMore());
    io.observe(el.current);
    return () => io.disconnect();
  });
  return (
    <button ref={el} className="tail" onClick={onMore}>
      {children}
    </button>
  );
}

/** The scope capsule: the project's avatar and name, or 全部项目, opening
 * the menu to pick another. None while there is no project at all. */
function ScopeCapsule({
  remote,
  go,
  wide,
  onPicked,
}: {
  remote: Remote;
  go: Go;
  wide?: boolean;
  onPicked?: () => void;
}) {
  const { scope, list, groups } = useScope(remote);
  const [open, setOpen] = useState<"" | "menu" | "new">("");
  if (!list.length) return null;
  const name = scope === ALL ? t("home.all_projects") : projectName(scope);
  return (
    <>
      <button
        className={(wide ? "scp" : "gcap glass scope") + (open === "menu" ? " open" : "")}
        aria-haspopup="dialog"
        aria-expanded={open === "menu"}
        aria-label={t("home.scope_label", { name })}
        title={scope === ALL ? undefined : shortPath(scope)}
        onClick={() => setOpen(open ? "" : "menu")}
      >
        <Avatar name={name} all={scope === ALL} />
        <b>{name}</b>
        <ChevronDown />
      </button>
      {open === "menu" && (
        <ScopeMenu
          remote={remote}
          scope={scope}
          count={groups.pinned.length + groups.others.length}
          wide={wide}
          onClose={() => setOpen("")}
          onPick={(key) => {
            saveScope(key);
            setOpen("");
            onPicked?.();
          }}
          onNew={() => setOpen("new")}
          onManage={() => {
            setOpen("");
            go("projects");
          }}
        />
      )}
      {open === "new" && <NewFlow remote={remote} go={go} start="new" onClose={() => setOpen("")} />}
    </>
  );
}

/** The scopes to pick from: all projects when there is more than one,
 * then the projects as every list of them draws them, the current one
 * with a blue ✓. A sheet on a phone, a menu by the capsule on a wide page. */
function ScopeMenu({
  remote,
  scope,
  count,
  wide,
  onClose,
  onPick,
  onNew,
  onManage,
}: {
  remote: Remote;
  scope: string;
  count: number;
  wide?: boolean;
  onClose: () => void;
  onPick: (key: string) => void;
  onNew: () => void;
  onManage: () => void;
}) {
  const counts = sessionCounts(remote, remote.sessions.values());
  const body = (
    <ProjectList
      remote={remote}
      onNew={onNew}
      first={
        count > 1 && (
          <button className="pi" role="menuitemradio" aria-checked={scope === ALL} onClick={() => onPick(ALL)}>
            <Avatar name={t("home.all_projects")} size="sm" all />
            <div>
              <b>{t("home.all_projects")}</b>
              <small>{counts || t("projects.count", { count })}</small>
            </div>
            <span className="ck">{scope === ALL && <Check />}</span>
          </button>
        )
      }
      item={(p) => (
        <ProjectItem
          key={p.key}
          remote={remote}
          p={p}
          checked={p.key === scope}
          onClick={() => onPick(p.key)}
          end={<span className="ck">{p.key === scope && <Check />}</span>}
        />
      )}
    >
      <button className="pi act" onClick={onManage}>
        {t("home.manage_projects")}
        <ChevronRight className="chev" />
      </button>
    </ProjectList>
  );
  if (!wide)
    return (
      <Sheet onClose={onClose} title={t("home.choose_scope")}>
        {body}
      </Sheet>
    );
  return <Popover onClose={onClose}>{body}</Popover>;
}

function Popover({ onClose, children }: { onClose: () => void; children: ReactNode }) {
  useEscape(onClose);
  const pop = useRef<HTMLDivElement>(null);
  useDialogFocus(pop);
  return (
    <>
      <div className="pop-back" onClick={onClose} />
      <div className="pop" role="dialog" ref={pop}>
        {children}
      </div>
    </>
  );
}

/** How things stand, in one sentence (design-system §6 大标题): what
 * waits, else what runs, else that nothing waits; with no session, that
 * there is none; out of reach, the stretch of the way that is down. The
 * line under it counts running and idle sessions apart. */
function useHeadline(remote: Remote, scope: string): { title: ReactNode; sub?: ReactNode; err?: boolean; path?: boolean } {
  const [details, setDetails] = useState(false);
  const { list } = useScope(remote);
  const link = remote.link;
  const kept = remote.listed || remote.sessions.size > 0;
  if (link === "no-network") return { title: t("connection.no_network"), sub: kept ? t("connection.previous_below") : t("connection.network_required") };
  if (link === "waiting" && remote.showWaiting) return { title: remote.everOnline ? t("connection.reconnecting") : t("connection.connecting"), sub: kept ? t("connection.previous_below") : undefined };
  if (link === "no-server") {
    const why = remote.connError || t("connection.no_response");
    // A browser only knows of no network at all: one that leads nowhere
    // looks the same as a server that is down.
    if (details)
      return {
        title: t("connection.no_server"),
        err: !kept,
        sub: (
          <>
            {t("connection.server_details", { reason: why })}
            <button onClick={() => remote.reconnectNow(true)}>{t("common.retry")}</button>
          </>
        ),
      };
    return {
      title: t("connection.no_server"),
      err: !kept,
      sub: (
        <>
          {kept ? t("connection.retrying_previous") : why}
          <button onClick={() => setDetails(true)}>{t("common.details")}</button>
        </>
      ),
    };
  }
  const waiting = todoList(remote).length;
  let running = 0;
  let idle = 0;
  for (const s of liveList(remote)) {
    if (scope !== ALL && projectKey(s) !== scope) continue;
    const tone = stateOf(s, remote.presence.get(s.machineId)).tone;
    if (tone === "run") running++;
    else if (tone === "idle") idle++;
  }
  if (waiting > 0)
    return {
      title: (
        <>
          {emphasizeCount(t("home.waiting_count", { count: waiting }), waiting)}
        </>
      ),
      sub: running && idle ? t("home.running_and_idle", { count: running, idle }) : running ? t("home.running_count", { count: running }) : idle ? t("home.idle_count", { count: idle }) : undefined,
    };
  if (running > 0) return { title: t("home.running_count", { count: running }), sub: idle ? t("home.other_idle", { count: idle }) : undefined };
  if (remote.sessions.size === 0) return { title: remote.listed ? t("home.no_sessions") : t("home.sessions_unavailable") };
  const project = scope !== ALL ? list.find((p) => p.key === scope) : undefined;
  if (project && project.sessions.every((s) => s.info?.taskOwned) && project.ended === 0) return { title: t("home.project_no_sessions"), sub: shortPath(scope), path: true };
  return { title: t("home.nothing_waiting"), sub: idle ? t("home.idle_count", { count: idle }) : undefined };
}

function Headline({ remote, scope, wide }: { remote: Remote; scope: string; wide?: boolean }) {
  const h = useHeadline(remote, scope);
  return (
    <>
      <h1 className={wide ? "whl" : "hl"}>{h.title}</h1>
      {h.sub && <div className={(wide ? "wsub" : "hl-sub") + (h.err ? " err" : "") + (h.path ? " path" : "")}>{h.sub}</div>}
    </>
  );
}

/** Under the title of an account with no sessions: what to do first, by
 * what machines it has. The bottom right's 新任务 is the screen's one ink
 * button; only with no computer yet is 添加设备 the thing to do. */
function Empty({ remote, onStart }: { remote: Remote; onStart?: () => void }) {
  const machines = [...remote.machines.values()];
  const on = machines.find((m) => m.online);
  return (
    <div className="home-note">
      {on ? (
        onStart ? (
          <>
            {t("home.online_start", { name: machineName(on) })}
            <div>
              <button className="link mid" onClick={onStart}>
                {t("home.start_first")}
              </button>
            </div>
          </>
        ) : (
          <>{t("home.online_new_task", { name: machineName(on) })}</>
        )
      ) : machines.length ? (
        <>
          {commandNote(t("home.no_online_computer", { command: "remote-agent daemon" }))}
        </>
      ) : remote.isAuthority ? (
        <>
          {t("home.computer_runs_cli")}
          <div>
            <button
              className="btn pri"
              onClick={() => {
                location.hash = "/settings/add-device";
              }}
            >
              {t("home.add_device")}
            </button>
          </div>
        </>
      ) : (
        <>{t("home.add_via_manager", { name: authorityName(remote) })}</>
      )}
    </div>
  );
}

/** The phone's states are from the future: remote control is paused. */
function ClockWarning({ remote }: { remote: Remote }) {
  useRemote(remote);
  if (!remote.clockWarning) return null;
  return <Banner kind="warn">{remote.clockWarning}</Banner>;
}

/** A newer build is downloaded: offered until the page is reloaded. */
function UpdateBanner() {
  if (useUpdate() !== "ready") return null;
  return (
    <Banner action={t("common.reload")} onAction={applyUpdate}>
      {t("home.update_ready")}
    </Banner>
  );
}

/** Listing sessions failed while connected: say so, offer a retry. */
function ListError({ remote }: { remote: Remote }) {
  if (!remote.listError) return null;
  return (
    <div className="listerr">
      <Banner kind="err" action={t("common.retry")} onAction={() => remote.retryList()}>
        {t("home.list_failed", { reason: remote.listError })}
      </Banner>
    </div>
  );
}

/** The banners under the title: a new version, the clock, a recovery
 * request, a failed listing, the project list's sync. */
function HomeBanners({ remote, go, prefs }: { remote: Remote; go: Go; prefs?: boolean }) {
  return (
    <div className="home-banners">
      <UpdateBanner />
      <ClockWarning remote={remote} />
      <RecoveryNotice remote={remote} go={go} />
      <ListError remote={remote} />
      {prefs && <PrefsBanner remote={remote} quiet />}
    </div>
  );
}

/** Whether a new session can be started from here now: not out of reach
 * of the server, and with a computer to run it on. */
export function canStart(remote: Remote): boolean {
  return !remote.unlinked && remote.machines.size > 0;
}

/** The bottom of the phone's sessions and tasks pages: the two tabs in a
 * glass capsule, each with its badge, and on the right the ink button
 * that says what it makes. No tab bar, no settings tab: the gear is on
 * the home's top right. */
export function BottomBar({
  remote,
  go,
  active,
  action,
}: {
  remote: Remote;
  go: Go;
  active: "sessions" | "tasks";
  action: { label: string; icon: ReactNode; onClick: () => void; disabled?: boolean };
}) {
  const todo = todoList(remote).length;
  return (
    <>
      <div className="edge-b" />
      <nav className="bbar">
        <div className="tabs glass">
          <button className={active === "sessions" ? "on" : ""} aria-current={active === "sessions" ? "page" : undefined} onClick={() => go("")}>
            <MessageCircle />
            {t("home.sessions_tab")}
            {todo > 0 && (
              <span className="count" aria-label={t("home.pending_label", { count: todo })}>
                {unreadLabel(todo)}
              </span>
            )}
          </button>
          <button className={active === "tasks" ? "on" : ""} aria-current={active === "tasks" ? "page" : undefined} onClick={() => go("tasks")}>
            <Clock />
            {t("home.schedules_tab")}
            <ScheduleBadge remote={remote} />
          </button>
        </div>
        <button className="fab" disabled={action.disabled} onClick={action.onClick}>
          {action.icon}
          {action.label}
        </button>
      </nav>
    </>
  );
}

export function Home({ remote, go }: { remote: Remote; go: Go }) {
  useRemote(remote);
  const [picking, setPicking] = useState(() => remote.demoFlag("pick"));
  const { scope } = useScope(remote);
  // Another scope is another list: it starts from its title, not from where the last one was scrolled to.
  const body = useRef<HTMLDivElement>(null);
  useEffect(() => { body.current?.scrollTo(0, 0); }, [scope]);
  const none = remote.sessions.size === 0;
  // Within a project the new session is its own; under all projects one is picked first.
  const start = () => {
    if (scope === ALL) return setPicking(true);
    remote.focusProject = scope;
    go("p/" + encodeURIComponent(scope));
  };
  return (
    <>
      <div className="body home under-bar" ref={body}>
        <div className="tbar">
          <ScopeCapsule remote={remote} go={go} />
          <span className="sp" />
          <GlassButton label={t("common.settings")} onClick={() => go("settings")}>
            <Settings />
          </GlassButton>
        </div>
        {!remote.listed && none && !remote.listError && remote.link === "online" ? (
          <div className="pad">
            <Skeleton />
          </div>
        ) : (
          <>
            <Headline remote={remote} scope={scope} />
            {none ? (
              <>
                <HomeBanners remote={remote} go={go} />
                {remote.listed && <Empty remote={remote} />}
              </>
            ) : (
              <Timeline key={scope} remote={remote} go={go} scope={scope} head={<HomeBanners remote={remote} go={go} prefs />} />
            )}
          </>
        )}
      </div>
      <BottomBar remote={remote} go={go} active="sessions" action={{ label: t("home.new_task"), icon: <SquarePen />, onClick: start, disabled: !canStart(remote) }} />
      {picking && <NewFlow remote={remote} go={go} onClose={() => setPicking(false)} />}
    </>
  );
}

/** The wide sidebar, drawn on the gray window: the button that makes
 * something new, then for sessions the scope, the sentence and the
 * timeline, for tasks their list; at the foot where the link stands, the
 * 会话 / 定时 switch and the gear. `project` is the project the main area
 * is opened on, if it is. */
export function Sidebar({
  remote,
  go,
  selected,
  project,
  active = "sessions",
  selectedTask,
}: {
  remote: Remote;
  go: Go;
  selected?: string;
  project?: string;
  active?: string;
  selectedTask?: string;
}) {
  useRemote(remote);
  const { scope } = useScope(remote);
  const conn = connWord(remote);
  const [checking, setChecking] = useState(false);
  const tasks = active === "tasks";
  const supported = [...remote.machines.keys()].some((id) => remote.supportsSchedules(id));
  return (
    <aside className="side">
      <div className="side-top">
        {/* The scope and the way to start something share the top row: one line of controls, then the headline. */}
        {!tasks && <ScopeCapsule remote={remote} go={go} wide onPicked={() => project !== undefined && go("")} />}
        {tasks ? (
          <button className="mini" disabled={!supported || remote.unlinked} onClick={() => go("tasks/new")}>
            <Plus />
            {t("home.new_schedule")}
          </button>
        ) : (
          <button
            className="mini"
            disabled={!canStart(remote)}
            onClick={() => {
              remote.focusProject = "*";
              go("");
              // The main area may already be the new task: it is told all the same.
              remote.touch();
            }}
          >
            <SquarePen />
            {t("home.new_task")}
          </button>
        )}
      </div>
      {tasks ? (
        <Suspense fallback={<><h1 className="whl">{"\u200b"}</h1><div className="scroll" /></>}><SchedulesPage remote={remote} go={go} sidebar selected={selectedTask} /></Suspense>
      ) : (
        <>
          {!remote.listed && !remote.listError && remote.sessions.size === 0 && remote.link === "online" ? (
            <div className="scroll pad">
              <Skeleton />
            </div>
          ) : (
            <>
              <Headline remote={remote} scope={scope} wide />
              <Timeline key={scope} remote={remote} go={go} scope={scope} selected={selected} wide head={<HomeBanners remote={remote} go={go} />} />
            </>
          )}
        </>
      )}
      <button className="conn" onClick={() => setChecking(true)} aria-label={conn.text ? t("home.connection_label", { state: conn.text }) : t("home.connection_check")} title={t("home.connection_check")}>
        {conn.text && (
          <>
            <StateMark kind={conn.tone} />
            {conn.text}
          </>
        )}
      </button>
      <div className="sfoot">
        <Segmented
          label={t("home.sessions_or_schedules")}
          value={tasks ? "tasks" : active === "sessions" ? "sessions" : undefined}
          onChange={(v) => go(v === "tasks" ? "tasks" : "")}
          options={[
            { value: "sessions", label: <><MessageCircle />{t("home.sessions_tab")}</> },
            { value: "tasks", label: <><Clock />{t("home.schedules_tab")}<ScheduleBadge remote={remote} /></> },
          ]}
        />
        <button className={"ib" + (active === "settings" ? " on" : "")} aria-label={t("common.settings")} title={t("common.settings")} onClick={() => go("settings")}>
          <Settings />
        </button>
      </div>
      {checking && <ConnCheck remote={remote} onClose={() => setChecking(false)} />}
    </aside>
  );
}

/** The wide home: a new-session box. Within a project's scope, or opened
 * on a project, the box is that project's; under all projects it names
 * the project and lets another be picked. */
export function WideHome({
  remote,
  go,
  project,
}: {
  remote: Remote;
  go: Go;
  project?: string;
}) {
  useRemote(remote);
  const { scope, list: listed } = useScope(remote);
  // Opening a listed project makes it the scope.
  const known = project !== undefined && listed.some((p) => p.key === project);
  useEffect(() => {
    if (known) saveScope(project);
  }, [known, project]);
  const fixed = project ?? (scope !== ALL ? scope : undefined);
  // The first project by liveness and creation time; a removed one is not offered.
  const list = [...listed].sort(compareProjects);
  const [key, setKey] = useState<string>();
  const [picking, setPicking] = useState<"" | "pick" | "new">(() => (remote.demoFlag("pick") ? "pick" : ""));
  const pkey = fixed ?? key ?? list[0]?.key ?? "";
  const ns = useNewSession(remote, pkey, go);
  // Taken once, and kept for the render that commits: a render may run twice.
  const asked = useRef(false);
  if (remote.focusProject === "*" || (project !== undefined && remote.focusProject === project)) {
    asked.current = true;
    remote.focusProject = "";
  }
  const focus = asked.current;
  useEffect(() => { asked.current = false; });
  // 新任务 with no project yet to start one in: it begins with choosing a directory.
  const first = focus && !pkey && remote.listed;
  useEffect(() => {
    if (first) setPicking("new");
  }, [first]);
  const name = projectName(pkey);
  return (
    <div className="body wnew">
      {!remote.listed && !pkey ? (
        <div className="wstart">{remote.listError ? <ListError remote={remote} /> : <Skeleton />}</div>
      ) : !pkey ? (
        <div className="wstart">
          <h3 className="et">{t("home.no_sessions")}</h3>
          <Empty remote={remote} onStart={() => setPicking("new")} />
        </div>
      ) : (
        <NewBox
          ns={ns}
          name={name}
          autoFocus={focus}
          variant="start"
          fixed={fixed !== undefined}
          onPickProject={() => setPicking("pick")}
          notice={<RemovedNotice remote={remote} pkey={pkey} />}
          // With no project picked, the box follows project ordering;
          // once a draft begins it stays on that one, whatever other
          // projects do meanwhile.
          onDraft={() => setKey((k) => k ?? pkey)}
        />
      )}
      {picking && (
        <NewFlow
          key={picking}
          remote={remote}
          go={go}
          start={picking === "new" ? "new" : undefined}
          onClose={() => setPicking("")}
          onPicked={(k) => {
            setKey(k);
          }}
        />
      )}
    </div>
  );
}
