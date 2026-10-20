import { authorityName } from "./DeviceManagement";
// The home page: a timeline of sessions within a scope (one project, or
// all of them), its wide-screen form as the sidebar, and the list rows
// they share (client-v27).

import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import {
  agentName,
  agentOf,
  compareAttention,
  compareLive,
  compareProjects,
  compareEnded,
  dayGroup,
  endedMs,
  endedTime,
  askTitle,
  derive,
  exited,
  machineName,
  minutesLeft,
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
import {
  ALL,
  resolveScope,
  savedScope,
  saveScope,
  subscribeScope,
} from "../scope";
import {
  Avatar,
  Banner,
  Dot,
  Sheet,
  Skeleton,
  useEscape,
  useNow,
  useRemote,
} from "../ui";
import { deadlineOf, useDerived } from "./actions";
import { approvalDisplay } from "../approval-display";
import { ApprovalActions, approvalText, approvalTitle, CardCommand } from "./Dock";
import { NewBox, NewFlow, useNewSession, type Go } from "./NewSession";
import { ConnCheck, connWord } from "./ConnCheck";
import {
  PrefsBanner,
  ProjectItem,
  ProjectList,
  RemovedNotice,
  sessionCounts,
  useProjectGroups,
} from "./Projects";
import { applyUpdate, useUpdate } from "../update";
import {
  Check,
  ChevronDown,
  ChevronRight,
  CloudOff,
  LoaderCircle,
  Search,
  Settings,
  SquarePen,
  WifiOff,
  X,
} from "../icons";

export type { Go };

/** Which of a row's attributes differ within the list it is in. A row
 * names only those: what every row shares is noise. */
export type Cols = { project?: boolean; agent?: boolean; machine?: boolean };

/** The third line of a row: 分叉, then what its list does not share. */
function metaOf(remote: Remote, s: SessionView, cols: Cols): string {
  return [
    s.info?.forkedFrom ? "分叉" : "",
    cols.project ? projectName(projectKey(s)) : "",
    cols.agent ? agentName(agentOf(s)) : "",
    cols.machine ? machineName(remote.machines.get(s.machineId)) : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

/** The columns of a list of sessions: the CLI when they used more than
 * one, the machine when the account has more than one. */
export function colsOf(
  remote: Remote,
  sessions: SessionView[],
  project: boolean,
): Cols {
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

/** Sessions waiting for the user, oldest wait first. */
export function todoList(remote: Remote): SessionView[] {
  return [...remote.sessions.values()]
    .filter((s) => needsAttention(s, remote.machines.get(s.machineId)))
    .sort((a, b) => compareAttention(a, b, remote.machines));
}

/** Live sessions: not ended, and not already under 需要处理. */
function liveList(remote: Remote): SessionView[] {
  return [...remote.sessions.values()]
    .filter(
      (s) => !exited(s) && !needsAttention(s, remote.machines.get(s.machineId)),
    )
    .sort((a, b) => compareLive(a, b));
}

/** A session waiting for the user. Command approvals are handled right
 * on the card; questions, plans and popups link into the session. */
function TodoCard({
  remote,
  s,
  go,
  cols,
}: {
  remote: Remote;
  s: SessionView;
  go: Go;
  cols: Cols;
}) {
  const now = useNow(1000);
  const d = useDerived(remote, s.id);
  const machine = remote.machines.get(s.machineId);
  const st = stateOf(s, machine);
  const agent = agentOf(s);
  const a = st.kind === "approval" ? [...d.approvals.values()][0] : undefined;
  const q = st.kind === "question" ? [...d.questions.values()][0] : undefined;
  const where = metaOf(remote, s, cols);
  const open = () => go("s/" + s.id);
  const kind = a
    ? approvalTitle(a)
    : st.kind === "question"
      ? askTitle(agent)
      : st.text;
  const deadline = a ? deadlineOf(a) : 0;
  const link =
    {
      approval: "查看会话",
      question: "去回答",
      plan: "去批准",
      popup: "去处理",
    }[st.kind as string] ?? "查看会话";
  return (
    <div className="todo">
      <div className="k">
        <span>
          <i className="pending-dot" /> {kind}
        </span>
        <span>
          {deadline
            ? minutesLeft(deadline, now) + (deadline > now ? "，超时拒绝" : "")
            : when(s.updatedMs, now)}
        </span>
      </div>
      <div className="t">
        <span>{titleOf(s)}</span>
        <button className="link" onClick={open}>
          {link}
          <ChevronRight />
        </button>
      </div>
      <div className="m">
        {[
          where,
          q?.questions[0]?.text,
          st.kind === "popup" && s.info?.statusDetail,
        ]
          .filter(Boolean)
          .join(" · ")}
      </div>
      {a && (
        <>
          <CardCommand text={approvalText(a)} />
          {a.description && (
            <div className="desc">{approvalDisplay(a.description)}</div>
          )}
          {/* Keyed by approval: the next one must not inherit this one's locked buttons. */}
          <ApprovalActions
            key={a.approvalId}
            remote={remote}
            sid={s.id}
            a={a}
            agent={agent}
          />
        </>
      )}
    </div>
  );
}

/** A live session in a list: the dot, the state word and what it is
 * doing, then whatever its list does not share. */
export function SessionRow({
  remote,
  s,
  go,
  selected,
  cols = {},
  last,
}: {
  remote: Remote;
  s: SessionView;
  go: Go;
  selected?: boolean;
  cols?: Cols;
  last?: boolean;
}) {
  const machine = remote.machines.get(s.machineId);
  const st = shownStateOf(s, machine, remote.unlinked);
  // Waiting sessions preview what they wait for, which is in the events.
  const waiting = st.tone === "wait";
  useEffect(
    () => (waiting ? remote.retainHistory(s.id) : undefined),
    [remote, s.id, waiting],
  );
  const pv = previewOf(s, st, waiting ? pendingOf(remote, s.id) : undefined);
  const ended = st.kind === "ended";
  const meta = metaOf(remote, s, cols);
  return (
    <button
      className={`sess ${st.tone}${selected ? " sel" : ""}${last ? " last" : ""}`}
      onClick={() => go("s/" + s.id)}
    >
      <Dot tone={st.tone} />
      <div className="sm">
        <div className="row1">
          <b>{titleOf(s)}</b>
          <span>{when(ended ? BigInt(endedMs(s)) : s.updatedMs)}</span>
        </div>
        <div className="pv">
          <span className={"stw " + st.tone}>{st.text}</span>
          {pv && pv !== st.text && ` · ${pv}`}
        </div>
        {meta && <div className="mc">{meta}</div>}
      </div>
    </button>
  );
}

/** An ended session: one line, under the date group that says when. */
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
    <button
      className={"old" + (selected ? " sel" : "")}
      onClick={() => go("s/" + s.id)}
    >
      <span>{titleOf(s)}</span>
      <span>
        {project && projectName(projectKey(s)) + " · "}
        {endedTime(endedMs(s))}
      </span>
    </button>
  );
}

function pendingOf(remote: Remote, sid: string) {
  // The few waiting sessions have their events loaded anyway, for the cards.
  const d = remote.currentDerived(sid, derive(remote.events(sid)));
  return {
    approval: [...d.approvals.values()][0],
    question: [...d.questions.values()][0],
  };
}

const PAGE = 30;

/** The sessions of a scope: 需要处理 for the whole account, then the
 * scope's live sessions and its ended ones by date. With more than eight
 * sessions it can be filtered by title. The session on screen stays
 * listed and highlighted even when it is outside the scope. */
function Timeline({
  remote,
  go,
  scope,
  selected,
  wide,
}: {
  remote: Remote;
  go: Go;
  scope: string;
  selected?: string;
  wide?: boolean;
}) {
  const now = useNow(30_000);
  const [query, setQuery] = useState("");
  const [allTodo, setAllTodo] = useState(false);
  const [allLive, setAllLive] = useState(false);
  const [shown, setShown] = useState(PAGE);
  const input = useRef<HTMLInputElement>(null);
  const prefs = remote.projectPrefs();
  const q = query.trim().toLocaleLowerCase();
  const match = (s: SessionView) =>
    !q || titleOf(s).toLocaleLowerCase().includes(q);
  const within = (s: SessionView) => scope === ALL || projectKey(s) === scope;
  const inScope = [...remote.sessions.values()].filter(within);
  const todo = todoList(remote).filter(match);
  const live = liveList(remote).filter(within).filter(match);
  // A removed project's ended sessions are not listed under all projects.
  const everEnded = inScope
    .filter(
      (s) => exited(s) && (scope !== ALL || !isHidden(prefs, projectKey(s))),
    )
    .sort(compareEnded);
  const ended = everEnded.filter(match);
  const cols = colsOf(remote, inScope, scope === ALL);
  // Ended sessions come a page at a time: how many the scope has in all
  // is what its machines count, until every page is here.
  const groups = useProjectGroups(remote);
  const counted = [...groups.pinned, ...groups.others]
    .filter((p) => scope === ALL || p.key === scope)
    .reduce((n, p) => n + p.ended, 0);
  const totalEnded = remote.sessionsOpen
    ? Math.max(counted, everEnded.length)
    : everEnded.length;
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
  if (
    sel &&
    !q &&
    !todo.includes(sel) &&
    !live.includes(sel) &&
    !ended.slice(0, shown).includes(sel)
  )
    live.unshift(sel);
  // A phone's home keeps them short; the wide sidebar lists them all and
  // scrolls as one column.
  const cut = <T,>(list: T[], max: number, all: boolean) =>
    all || wide || q || (sel && list.indexOf(sel as T) >= max)
      ? list
      : list.slice(0, max);
  const todoShown = cut(todo, 3, allTodo);
  const liveShown = cut(live, 5, allLive);
  const endedShown = ended.slice(0, shown);
  // ⌘/Ctrl+K reaches the filter from anywhere on a wide window.
  useEffect(() => {
    if (!wide) return;
    const onKey = (e: KeyboardEvent) => {
      if (
        (e.metaKey || e.ctrlKey) &&
        e.key.toLowerCase() === "k" &&
        input.current
      ) {
        e.preventDefault();
        input.current.focus();
      }
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [wide]);
  const total = live.length + ended.length + todo.length;
  let group = "";
  return (
    <>
      {(liveList(remote).filter(within).length + totalEnded > 8 || query) && (
        <label className={"find" + (query ? " typed" : "")}>
          <Search />
          <input
            ref={input}
            aria-label="按标题筛选"
            placeholder="按标题筛选"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => e.key === "Escape" && setQuery("")}
          />
          {query ? (
            <button
              className="x"
              aria-label="清空筛选"
              onClick={() => setQuery("")}
            >
              <X />
            </button>
          ) : (
            wide && (
              <kbd>{/Mac|iP/.test(navigator.platform) ? "⌘K" : "Ctrl K"}</kbd>
            )
          )}
        </label>
      )}
      {todo.length > 0 && (
        <div className="label">
          需要处理 <span>{todo.length}</span>
        </div>
      )}
      {todoShown.map((s) => {
        const c = {
          ...cols,
          project: scope === ALL || projectKey(s) !== scope,
        };
        return wide ? (
          <SessionRow
            key={s.id}
            remote={remote}
            s={s}
            go={go}
            cols={c}
            selected={s.id === selected}
          />
        ) : (
          <TodoCard key={s.id} remote={remote} s={s} go={go} cols={c} />
        );
      })}
      {todo.length > todoShown.length && (
        <button className="todo-more" onClick={() => setAllTodo(true)}>
          还有 {todo.length - todoShown.length} 项待处理
          <ChevronRight />
        </button>
      )}
      {live.length > 0 && (
        <div className="label">
          进行中 <span>{live.length}</span>
        </div>
      )}
      {liveShown.map((s, i) => (
        <SessionRow
          key={s.id}
          remote={remote}
          s={s}
          go={go}
          cols={{ ...cols, project: scope === ALL || projectKey(s) !== scope }}
          selected={s.id === selected}
          last={i === liveShown.length - 1}
        />
      ))}
      {live.length > liveShown.length && (
        <button className="todo-more" onClick={() => setAllLive(true)}>
          还有 {live.length - liveShown.length} 个进行中
          <ChevronRight />
        </button>
      )}
      {endedShown.map((s) => {
        const g = dayGroup(endedMs(s), now);
        const head = g !== group && <div className="label">{g}</div>;
        group = g;
        return (
          <div key={s.id}>
            {head}
            <EndedRow
              s={s}
              go={go}
              project={scope === ALL}
              selected={s.id === selected}
            />
          </div>
        );
      })}
      {q ? (
        ended.length > endedShown.length ? (
          <More onMore={() => setShown((n) => n + PAGE)}>
            {total} 个会话的标题里有「{query.trim()}」，继续加载
          </More>
        ) : (
          <div className="tail">
            {remote.sessionsOpen
              ? "正在加载更早的会话…"
              : total
                ? `${total} 个会话的标题里有「${query.trim()}」`
                : `没有标题里有「${query.trim()}」的会话`}
          </div>
        )
      ) : totalEnded > endedShown.length ? (
        // Never further than one page past what is held: the rest waits for the server.
        <More
          onMore={() =>
            setShown((n) => Math.min(n + PAGE, everEnded.length + PAGE))
          }
        >
          共 {totalEnded} 个，滚到底加载更早的
        </More>
      ) : totalEnded > PAGE ? (
        <div className="tail">共 {totalEnded} 个</div>
      ) : (
        total === 0 &&
        scope !== ALL && (
          <div className="empty-state">
            <div className="et">这个项目还没有会话</div>
            <div className="ed">点右上角的新建按钮开始一个</div>
          </div>
        )
      )}
    </>
  );
}

/** The end of a page of ended sessions: reaching it, or clicking it,
 * shows the next page. */
function More({
  onMore,
  children,
}: {
  onMore: () => void;
  children: React.ReactNode;
}) {
  const el = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!el.current || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver(
      (entries) => entries.some((e) => e.isIntersecting) && onMore(),
    );
    io.observe(el.current);
    return () => io.disconnect();
  });
  return (
    <button ref={el} className="tail" onClick={onMore}>
      {children}
    </button>
  );
}

/** The title of the home and of the sidebar: the scope, which opens the
 * menu to pick another. With no project yet it is just 会话. */
function ScopeTitle({
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
  if (!list.length)
    return wide ? <b className="scope plain">会话</b> : <h3>会话</h3>;
  const name = scope === ALL ? "全部项目" : projectName(scope);
  return (
    <>
      <button
        className={"scope" + (open === "menu" ? " open" : "")}
        aria-haspopup="dialog"
        aria-expanded={open === "menu"}
        aria-label={`范围：${name}`}
        title={scope === ALL ? undefined : shortPath(scope)}
        onClick={() => setOpen(open ? "" : "menu")}
      >
        {scope === ALL ? (
          <div className="av sm all">全</div>
        ) : (
          <Avatar name={name} size="sm" />
        )}
        <b>{name}</b>
        <ChevronDown className="dn" />
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
      {open === "new" && (
        <NewFlow
          remote={remote}
          go={go}
          start="new"
          onClose={() => setOpen("")}
        />
      )}
    </>
  );
}

/** The scopes to pick from: all projects when there is more than one,
 * then the projects in the order of the lists, each with its path and
 * how many sessions are live. A sheet on a phone, a menu on a wide page. */
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
          <button
            className="pi"
            role="menuitemradio"
            aria-checked={scope === ALL}
            onClick={() => onPick(ALL)}
          >
            <div className="av sm all">全</div>
            <div>
              <b>全部项目</b>
              <small>{counts || `${count} 个项目`}</small>
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
        管理项目
        <ChevronRight />
      </button>
    </ProjectList>
  );
  if (!wide)
    return (
      <Sheet onClose={onClose}>
        <div className="ns-head">
          <b>看哪个项目</b>
          <button className="link" onClick={onClose}>
            取消
          </button>
        </div>
        {body}
      </Sheet>
    );
  return <Popover onClose={onClose}>{body}</Popover>;
}

function Popover({
  onClose,
  children,
}: {
  onClose: () => void;
  children: React.ReactNode;
}) {
  useEscape(onClose);
  return (
    <>
      <div className="pop-back" onClick={onClose} />
      <div className="pop">{children}</div>
    </>
  );
}

/** What an account with no sessions shows, by what machines it has. */
function Empty({
  remote,
  onStart,
}: {
  remote: Remote;
  onStart: () => void;
}) {
  const machines = [...remote.machines.values()];
  const on = machines.find((m) => m.online);
  return (
    <div className="empty-state">
      <div className="et">还没有会话</div>
      {on ? (
        <>
          <div className="ed">{machineName(on)} 在线，可以直接开始</div>
          <button className="btn pri" onClick={onStart}>
            开始第一个会话
          </button>
        </>
      ) : machines.length ? (
        <div className="ed">
          没有在线的电脑。在电脑上运行 <code>remote-agent daemon</code>
        </div>
      ) : (
        <div className="ed">
          {remote.isAuthority ? <>电脑运行 CLI，客户端操作会话。<button className="btn pri" onClick={() => { location.hash = "/settings/add-device"; }}>添加设备</button></> : <>还没有电脑。在管理端（{authorityName(remote)}）上打开「设置 › 添加设备」，把电脑加进来。</>}
        </div>
      )}
    </div>
  );
}

/** A newer build is downloaded: offered until the page is reloaded. */
/** The phone's states are from the future: remote control is paused. */
function ClockWarning({ remote }: { remote: Remote }) {
  useRemote(remote);
  if (!remote.clockWarning) return null;
  return (
    <div className="pad">
      <Banner kind="warn">{remote.clockWarning}</Banner>
    </div>
  );
}

function UpdateBanner() {
  if (useUpdate() !== "ready") return null;
  return (
    <div className="pad">
      <Banner action="重新加载" onAction={applyUpdate}>
        新版本已下载
      </Banner>
    </div>
  );
}

/** Listing sessions failed while connected: say so, offer a retry. */
function ListError({ remote }: { remote: Remote }) {
  if (!remote.listError) return null;
  return (
    <div className="pad listerr">
      <Banner kind="err" action="重试" onAction={() => remote.retryList()}>
        没能读取会话列表：{remote.listError}
      </Banner>
    </div>
  );
}

function HomeConn({ remote }: { remote: Remote }) {
  const link = remote.link;
  if (link === "online") return null;
  if (link === "no-network")
    return (
      <span className="conn">
        <WifiOff /> 本机网络不通
      </span>
    );
  if (link === "waiting")
    return (
      <span className="conn">
        <LoaderCircle /> {remote.everOnline ? "正在重连…" : "连接中…"}
      </span>
    );
  const kept = remote.listed || remote.sessions.size > 0;
  return (
    <span className={"conn" + (kept ? "" : " err")}>
      {kept ? <CloudOff /> : <X />} 连不上服务器
    </span>
  );
}

export function Home({ remote, go }: { remote: Remote; go: Go }) {
  useRemote(remote);
  const [picking, setPicking] = useState(() => remote.demoFlag("pick"));
  const { scope } = useScope(remote);
  const none = remote.sessions.size === 0;
  // Within a project the new session is its own; under all projects one is picked first.
  const start = () => {
    if (scope === ALL) return setPicking(true);
    remote.focusProject = scope;
    go("p/" + encodeURIComponent(scope));
  };
  return (
    <div className="body">
      <div className="large">
        <ScopeTitle remote={remote} go={go} />
        <HomeConn remote={remote} />
        <button className="hit" onClick={start} aria-label="新会话">
          <SquarePen className="compose-icon" />
        </button>
      </div>
      <UpdateBanner />
      <ClockWarning remote={remote} />

      <ListError remote={remote} />
      {!remote.listed && none ? (
        !remote.listError && (
          <div className="pad">
            <Skeleton />
          </div>
        )
      ) : none ? (
        <Empty remote={remote} onStart={() => setPicking(true)} />
      ) : (
        <>
          <div className="pad">
            <PrefsBanner remote={remote} quiet />
          </div>
          <Timeline key={scope} remote={remote} go={go} scope={scope} />
        </>
      )}
      {picking && (
        <NewFlow remote={remote} go={go} onClose={() => setPicking(false)} />
      )}
    </div>
  );
}

/** The wide sidebar: the scope, and its timeline. `project` is the
 * project the main area is opened on, if it is. */
export function Sidebar({
  remote,
  go,
  selected,
  project,
}: {
  remote: Remote;
  go: Go;
  selected?: string;
  project?: string;
}) {
  useRemote(remote);
  const { scope } = useScope(remote);
  const conn = connWord(remote);
  const [checking, setChecking] = useState(false);
  return (
    <aside className="side">
      <div className="sh">
        {/* The main area follows the scope unless it shows something else. */}
        <ScopeTitle
          remote={remote}
          go={go}
          wide
          onPicked={() => project !== undefined && go("")}
        />
        <button
          className="nb"
          aria-label="新会话"
          title="新会话"
          onClick={() => {
            remote.focusProject = "*";
            go("");
          }}
        >
          <SquarePen className="compose-icon" />
        </button>
      </div>
      <div className="scroll">
        <UpdateBanner />
        <ClockWarning remote={remote} />

        <ListError remote={remote} />
        {!remote.listed && !remote.listError && remote.sessions.size === 0 && (
          <div className="pad">
            <Skeleton />
          </div>
        )}
        <Timeline
          key={scope}
          remote={remote}
          go={go}
          scope={scope}
          selected={selected}
          wide
        />
      </div>
      <div className="foot">
        <button onClick={() => go("settings")}>
          <Settings /> 设置
        </button>
        <button
          className="link-conn"
          onClick={() => setChecking(true)}
          aria-label={`${conn.text}，连接检测`}
          title="连接检测"
        >
          <Dot tone={conn.tone} /> {conn.text}
        </button>
      </div>
      {checking && (
        <ConnCheck remote={remote} onClose={() => setChecking(false)} />
      )}
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
  const [picking, setPicking] = useState<"" | "pick" | "new">(() =>
    remote.demoFlag("pick") ? "pick" : "",
  );
  const pkey = fixed ?? key ?? list[0]?.key ?? "";
  const ns = useNewSession(remote, pkey, go);
  const focus =
    remote.focusProject === "*" ||
    (project !== undefined && remote.focusProject === project);
  if (focus) remote.focusProject = "";
  const name = projectName(pkey);
  return (
    <div className="body">
      <div className="start">
        {!remote.listed && !pkey ? (
          remote.listError ? (
            <ListError remote={remote} />
          ) : (
            <Skeleton />
          )
        ) : !pkey ? (
          <Empty remote={remote} onStart={() => setPicking("new")} />
        ) : (
          <>
            <RemovedNotice remote={remote} pkey={pkey} />
            <NewBox
              ns={ns}
              name={name}
              autoFocus={focus}
              variant="start"
              // With no project picked, the box follows project ordering;
              // once a draft begins it stays on that one, whatever other
              // projects do meanwhile.
              onDraft={() => setKey((k) => k ?? pkey)}
              lead={
                fixed === undefined && (
                  <button className="chip" onClick={() => setPicking("pick")}>
                    <Avatar name={name} size="xs" />
                    {name}
                    <ChevronDown />
                  </button>
                )
              }
            />
          </>
        )}
      </div>
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
