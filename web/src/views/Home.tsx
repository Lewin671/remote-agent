// The home page (需要处理, 进行中, 项目), its wide-screen form with the
// sidebar, and the list rows they share.

import { useState } from "react";
import {
  agentName,
  agentOf,
  ago,
  askTitle,
  derive,
  exited,
  machineName,
  minutesLeft,
  needsAttention,
  previewOf,
  projectKey,
  projectName,
  projects,
  shortPath,
  stateOf,
  statePriority,
  titleOf,
  when,
  type Project,
} from "../model";
import type { Remote, SessionView } from "../remote";
import { Avatar, Banner, Dot, Skeleton, useNow, useRemote } from "../ui";
import { deadlineOf, useDerived } from "./actions";
import { ApprovalActions, approvalText, approvalTitle } from "./Dock";
import { NewBox, NewFlow, useNewSession, type Go } from "./NewSession";

export type { Go };

/** Sessions waiting for the user, oldest wait first. */
export function todoList(remote: Remote): SessionView[] {
  const rank = (s: SessionView) => statePriority.indexOf(stateOf(s, remote.machines.get(s.machineId)).kind);
  return [...remote.sessions.values()]
    .filter((s) => needsAttention(s, remote.machines.get(s.machineId)))
    .sort((a, b) => rank(a) - rank(b) || Number(a.updatedMs - b.updatedMs));
}

/** Live sessions: not ended, and not already under 需要处理. */
function liveList(remote: Remote): SessionView[] {
  return [...remote.sessions.values()]
    .filter((s) => !exited(s) && !needsAttention(s, remote.machines.get(s.machineId)))
    .sort((a, b) => Number(b.updatedMs - a.updatedMs));
}

/** A session waiting for the user. Command approvals are handled right
 * on the card; questions, plans and popups link into the session. */
function TodoCard({ remote, s, go, wide }: { remote: Remote; s: SessionView; go: Go; wide?: boolean }) {
  const now = useNow(1000);
  const d = useDerived(remote, s.id);
  const machine = remote.machines.get(s.machineId);
  const st = stateOf(s, machine);
  const agent = agentOf(s);
  const a = st.kind === "approval" ? [...d.approvals.values()][0] : undefined;
  const q = st.kind === "question" ? [...d.questions.values()][0] : undefined;
  const where = `${projectName(projectKey(s))} · ${machineName(machine)}`;
  const open = () => go("s/" + s.id);
  const kind = a ? approvalTitle(a) : st.kind === "question" ? askTitle(agent) : st.text;
  const deadline = a ? deadlineOf(a) : 0;
  const link = { approval: "查看会话 ›", question: "去回答 ›", plan: "去批准 ›", popup: "去处理 ›" }[st.kind as string] ?? "查看会话 ›";
  return (
    <div className="todo">
      <div className="k">
        <span>● {kind}</span>
        <span>{deadline ? minutesLeft(deadline, now) + (deadline > now ? "，超时拒绝" : "") : when(s.updatedMs, now)}</span>
      </div>
      <div className="t">
        <span>{titleOf(s)}</span>
        <button className="link" onClick={open}>
          {link}
        </button>
      </div>
      <div className="m">
        {where}
        {wide && a?.description && ` · ${a.description}`}
        {q?.questions[0] && ` · ${q.questions[0].text}`}
        {st.kind === "popup" && s.info?.statusDetail && ` · ${s.info.statusDetail}`}
      </div>
      {a && (
        <>
          {!wide && a.description && <div className="desc">{a.description}</div>}
          <pre>{approvalText(a)}</pre>
          {/* Keyed by approval: the next one must not inherit this one's locked buttons. */}
          <ApprovalActions key={a.approvalId} remote={remote} sid={s.id} a={a} agent={agent} wideRow={wide} />
        </>
      )}
    </div>
  );
}

function TodoSection({ remote, go, wide }: { remote: Remote; go: Go; wide?: boolean }) {
  const [all, setAll] = useState(false);
  const todo = todoList(remote);
  if (!todo.length) return null;
  const shown = all ? todo : todo.slice(0, 3);
  return (
    <>
      <div className="label">
        需要处理 <span>{todo.length}</span>
      </div>
      {shown.map((s) => (
        <TodoCard key={s.id} remote={remote} s={s} go={go} wide={wide} />
      ))}
      {todo.length > shown.length && (
        <button className="todo-more" onClick={() => setAll(true)}>
          还有 {todo.length - shown.length} 项待处理 ›
        </button>
      )}
    </>
  );
}

/** A session in a list: the dot, and the state word beside it. */
export function SessionRow({
  remote,
  s,
  go,
  selected,
  compact,
  showProject = true,
  last,
}: {
  remote: Remote;
  s: SessionView;
  go: Go;
  selected?: boolean;
  compact?: boolean;
  showProject?: boolean;
  last?: boolean;
}) {
  const machine = remote.machines.get(s.machineId);
  const st = stateOf(s, machine);
  // Waiting sessions preview what they wait for, which is in the events.
  const pending = st.tone === "wait" && !compact ? pendingOf(remote, s.id) : undefined;
  const ended = st.kind === "ended";
  return (
    <button className={`sess ${st.tone}${ended ? " ended" : ""}${selected ? " sel" : ""}${last ? " last" : ""}`} onClick={() => go("s/" + s.id)}>
      <Dot tone={st.tone} />
      <div className="sm">
        <div className="row1">
          <b>{titleOf(s)}</b>
          <span>{when(s.updatedMs)}</span>
        </div>
        {!compact && <div className="pv">{previewOf(s, st, pending)}</div>}
        <div className="mc">
          <span className={"stw " + st.tone}>{st.text}</span>
          {showProject && ` · ${projectName(projectKey(s))}`} · {machineName(machine)}
        </div>
      </div>
    </button>
  );
}

function pendingOf(remote: Remote, sid: string) {
  // The few waiting sessions have their events loaded anyway, for the cards.
  const d = derive(remote.events(sid));
  return { approval: [...d.approvals.values()][0], question: [...d.questions.values()][0] };
}

function ProjectRow({ p, go }: { p: Project<SessionView>; go: Go }) {
  const live = p.sessions.filter((s) => !exited(s)).length;
  return (
    <button className="proj" onClick={() => go("p/" + encodeURIComponent(p.key))}>
      <Avatar name={p.name} />
      <div className="pt">
        <div>
          <div className="pn">{p.name}</div>
          <div className="ps">
            {shortPath(p.key)} · {live ? `${live} 个进行中` : ago(p.updatedMs)}
          </div>
        </div>
        <span className="chev">›</span>
      </div>
    </button>
  );
}

/** What an account with no sessions shows, by what machines it has. */
function Empty({ remote, go, onStart }: { remote: Remote; go: Go; onStart: () => void }) {
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
        <>
          <div className="ed">还没有加电脑。在电脑上运行 remote-agent，再把它加进这个账户</div>
          <button className="btn pri" onClick={() => go("add")}>
            添加设备
          </button>
        </>
      )}
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
  if (remote.conn === "online") return null;
  const failed = !remote.everOnline && remote.failures >= 2;
  return <span className={"conn" + (failed ? " err" : "")}>{failed ? "✕ 连不上服务器" : remote.everOnline ? "◌ 连接断了，正在重连" : "◌ 连接中…"}</span>;
}

export function Home({ remote, go }: { remote: Remote; go: Go }) {
  useRemote(remote);
  useNow(30_000);
  const [picking, setPicking] = useState(false);
  const live = liveList(remote);
  const list = projects(remote.sessions.values());
  return (
    <div className="body">
      <div className="large">
        <h3>会话</h3>
        <HomeConn remote={remote} />
        <button className="hit" onClick={() => setPicking(true)} aria-label="新会话">
          <span className="newbtn">＋</span>
        </button>
      </div>
      <ListError remote={remote} />
      {!remote.listed && list.length === 0 ? (
        !remote.listError && (
          <div className="pad">
            <Skeleton />
          </div>
        )
      ) : list.length === 0 ? (
        <Empty remote={remote} go={go} onStart={() => setPicking(true)} />
      ) : (
        <>
          <TodoSection remote={remote} go={go} />
          {live.length > 0 && (
            <>
              <div className="label">
                进行中 <span>{live.length}</span>
              </div>
              {live.map((s, i) => (
                <SessionRow key={s.id} remote={remote} s={s} go={go} last={i === live.length - 1} />
              ))}
            </>
          )}
          <div className="label">项目</div>
          {list.map((p) => (
            <ProjectRow key={p.key} p={p} go={go} />
          ))}
        </>
      )}
      {picking && <NewFlow remote={remote} go={go} onClose={() => setPicking(false)} />}
    </div>
  );
}

/** The wide sidebar: 需要处理, 进行中, and every project; a project's arrow
 * opens its recently ended sessions. The session on screen stays listed
 * and highlighted even after its state changes. */
export function Sidebar({ remote, go, selected, project }: { remote: Remote; go: Go; selected?: string; project?: string }) {
  useRemote(remote);
  useNow(30_000);
  const [open, setOpen] = useState<Set<string>>(new Set());
  const todo = todoList(remote);
  const live = liveList(remote);
  const sel = selected ? remote.sessions.get(selected) : undefined;
  // Keep the open session in view where it would have been.
  if (sel && !todo.includes(sel) && !live.includes(sel)) live.unshift(sel);
  const list = projects(remote.sessions.values());
  const conn = remote.conn === "online" ? ["idle", "已连接"] : remote.everOnline ? ["wait", "重连中"] : ["off", remote.failures >= 2 ? "连不上" : "连接中"];
  return (
    <aside className="side">
      <div className="sh">
        <b>会话</b>
        <button
          className="nb"
          onClick={() => {
            remote.focusProject = "*";
            go("");
          }}
        >
          ＋ 新会话
        </button>
      </div>
      <div className="scroll">
        <ListError remote={remote} />
        {!remote.listed && !remote.listError && list.length === 0 && (
          <div className="pad">
            <Skeleton />
          </div>
        )}
        {todo.length > 0 && <div className="label">需要处理</div>}
        {todo.map((s) => (
          <SessionRow key={s.id} remote={remote} s={s} go={go} compact selected={s.id === selected} />
        ))}
        {live.length > 0 && <div className="label">进行中</div>}
        {live.map((s) => (
          <SessionRow key={s.id} remote={remote} s={s} go={go} compact selected={s.id === selected} />
        ))}
        {list.length > 0 && <div className="label">项目</div>}
        {list.map((p) => {
          const n = p.sessions.filter((s) => !exited(s)).length;
          const ended = p.sessions.filter(exited);
          const isOpen = open.has(p.key);
          return (
            <div key={p.key}>
              <div className={"ph" + (project === p.key ? " sel" : "")}>
                <button
                  className="arr"
                  aria-label={isOpen ? "收起" : "展开最近结束的会话"}
                  aria-expanded={isOpen}
                  onClick={() =>
                    setOpen((o) => {
                      const x = new Set(o);
                      if (x.has(p.key)) x.delete(p.key);
                      else x.add(p.key);
                      return x;
                    })
                  }
                >
                  {isOpen ? "▾" : "▸"}
                </button>
                <button className="pname" onClick={() => go("p/" + encodeURIComponent(p.key))} title={shortPath(p.key)}>
                  <Avatar name={p.name} size="sm" />
                  <span className="nm">{p.name}</span>
                  <span className={n ? "live" : ""}>{n ? `${n} 进行中` : ago(p.updatedMs)}</span>
                </button>
              </div>
              {isOpen && (
                <>
                  {ended.slice(0, 2).map((s) => (
                    <button key={s.id} className={"sub-sess" + (s.id === selected ? " sel" : "")} onClick={() => go("s/" + s.id)}>
                      <span>{titleOf(s)}</span>
                      <span>{ago(s.updatedMs)}</span>
                    </button>
                  ))}
                  <button className="sub-sess link" onClick={() => go("p/" + encodeURIComponent(p.key))}>
                    <span>{ended.length ? `全部 ${p.sessions.length} 个` : "没有结束的会话"}</span>
                    <span />
                  </button>
                </>
              )}
            </div>
          );
        })}
      </div>
      <div className="foot">
        <button onClick={() => go("settings")}>⚙︎ 设置</button>
        <span>
          <Dot tone={conn[0] as "idle"} /> {conn[1]}
        </span>
      </div>
    </aside>
  );
}

/** The wide home: a new-session box for the latest project, and what
 * waits for the user. */
export function WideHome({ remote, go }: { remote: Remote; go: Go }) {
  useRemote(remote);
  const list = projects(remote.sessions.values());
  const [key, setKey] = useState<string>();
  const [picking, setPicking] = useState<"" | "pick" | "new">("");
  const pkey = key ?? list[0]?.key ?? "";
  const ns = useNewSession(remote, pkey, go);
  const focus = remote.focusProject === "*";
  if (focus) remote.focusProject = "";
  const name = projectName(pkey);
  return (
    <div className="body">
      <div className="start">
        {!remote.listed && !pkey ? (
          remote.listError ? <ListError remote={remote} /> : <Skeleton />
        ) : !pkey ? (
          <Empty remote={remote} go={go} onStart={() => setPicking("new")} />
        ) : (
          <>
            <h3>要 {agentName(ns.cfg.agent)} 做什么？</h3>
            <NewBox
              ns={ns}
              name={name}
              autoFocus={focus}
              variant="start"
              lead={
                <button className="chip" onClick={() => setPicking("pick")}>
                  <Avatar name={name} size="xs" />
                  {name} ▾
                </button>
              }
            />
          </>
        )}
        <TodoSection remote={remote} go={go} wide />
      </div>
      {picking && (
        <NewFlow
          key={picking}
          remote={remote}
          go={go}
          onClose={() => setPicking("")}
          onPicked={(k) => {
            setKey(k);
          }}
        />
      )}
    </div>
  );
}
