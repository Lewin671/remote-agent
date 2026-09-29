import { useState } from "react";
import { exited, machineName, needsAttention, plain, projectKey, projects, shortPath, statusOf, titleOf, when, type Project } from "../model";
import type { Remote, SessionView } from "../remote";
import { Avatar, Dot, useNow, useRemote } from "../ui";

export type Go = (route: string) => void;

/** Sessions that need the user: pending approvals, questions, popups. */
function TodoCards({ remote, go }: { remote: Remote; go: Go }) {
  const todo = [...remote.sessions.values()].filter(needsAttention).sort((a, b) => Number(b.updatedMs - a.updatedMs));
  if (todo.length === 0) return null;
  return (
    <>
      <div className="label">需要处理</div>
      <div className="hscroll">
        {todo.map((s) => (
          <button key={s.id} className="todo-card" onClick={() => go("s/" + s.id)}>
            <div className="k">
              <span>● {statusOf(s).text}</span>
              <span>{when(s.updatedMs)}</span>
            </div>
            <div className="t">{titleOf(s)}</div>
            <div className="m">
              {projectKey(s)} · {machineName(remote.machines.get(s.machineId))}
            </div>
          </button>
        ))}
      </div>
    </>
  );
}

function ProjectRow({ p, go }: { p: Project; go: Go }) {
  const live = p.sessions.filter((s) => !exited(s));
  const wait = live.filter((s) => statusOf(s).tone === "wait").length;
  const run = live.filter((s) => statusOf(s).tone === "run").length;
  const latest = p.sessions[0];
  const preview = plain(latest?.info?.preview || "") || (latest ? statusOf(latest).text : "");
  return (
    <button className="proj" onClick={() => go("p/" + encodeURIComponent(p.key))}>
      <Avatar name={p.name} />
      <div className="pmain">
        <div className="pt">
          <div className="pn">{p.name}</div>
          <div className="ps">
            {preview}
            {preview && " · "}
            {when(p.updatedMs)}
          </div>
        </div>
        <div className="badges">
          {wait > 0 && <span className="badge wait">{wait}</span>}
          {run > 0 && <span className="badge run">{run}</span>}
        </div>
      </div>
    </button>
  );
}

export function Home({ remote, go }: { remote: Remote; go: Go }) {
  useRemote(remote);
  useNow(30_000);
  const list = projects(remote);
  return (
    <div className="body">
      <div className="large">
        <h3>项目</h3>
        <ConnState remote={remote} />
      </div>
      <TodoCards remote={remote} go={go} />
      <div className="label">全部项目</div>
      {list.length === 0 && (
        <p className="empty">
          还没有会话。在电脑上运行 <code>remote-agent daemon</code>，再用 <code>remote-agent codex</code> 开一个会话。
        </p>
      )}
      {list.map((p) => (
        <ProjectRow key={p.key} p={p} go={go} />
      ))}
    </div>
  );
}

export function SessionRow({ s, remote, go, selected, compact }: { s: SessionView; remote: Remote; go: Go; selected?: boolean; compact?: boolean }) {
  const st = statusOf(s);
  const preview = st.tone === "wait" ? st.text : plain(s.info?.preview || "") || st.text;
  return (
    <button className={`sess ${st.tone}${selected ? " sel" : ""}`} onClick={() => go("s/" + s.id)}>
      <Dot tone={st.tone} />
      <div className="sm">
        <div className="top">
          <b>{titleOf(s)}</b>
          <span>{when(s.updatedMs)}</span>
        </div>
        {!compact && <div className="pv">{preview}</div>}
        <div className="mc">{machineName(remote.machines.get(s.machineId))}</div>
      </div>
    </button>
  );
}

export function ProjectPage({ remote, name, go, onBack }: { remote: Remote; name: string; go: Go; onBack: () => void }) {
  useRemote(remote);
  useNow(30_000);
  const [tab, setTab] = useState<"live" | "ended">("live");
  const p = projects(remote).find((x) => x.key === name);
  const live = p?.sessions.filter((s) => !exited(s)) ?? [];
  const ended = p?.sessions.filter(exited) ?? [];
  const shown = tab === "live" ? live : ended;
  return (
    <>
      <header className="navbar">
        <button className="back" onClick={onBack} aria-label="返回">
          ‹
        </button>
        <span className="title" />
        <span className="more" />
      </header>
      <div className="body">
        <div className="pad proj-head">
          <Avatar name={name} size="lg" />
          <div>
            <div className="proj-name">{name}</div>
            <div className="muted small">{[...(p?.paths.map(shortPath) ?? []), ...(p?.machines ?? [])].join(" · ")}</div>
          </div>
        </div>
        <div className="pad">
          <div className="seg">
            <button className={tab === "live" ? "on" : ""} onClick={() => setTab("live")}>
              进行中 {live.length}
            </button>
            <button className={tab === "ended" ? "on" : ""} onClick={() => setTab("ended")}>
              已结束 {ended.length}
            </button>
          </div>
        </div>
        {shown.length === 0 && <p className="empty">没有{tab === "live" ? "进行中" : "已结束"}的会话</p>}
        {shown.map((s) => (
          <SessionRow key={s.id} s={s} remote={remote} go={go} />
        ))}
      </div>
    </>
  );
}

/** The wide-screen sidebar: projects with their live sessions. */
export function Sidebar({ remote, go, selected }: { remote: Remote; go: Go; selected?: string }) {
  useRemote(remote);
  useNow(30_000);
  const list = projects(remote);
  return (
    <aside className="side">
      <div className="side-head">
        <b>项目</b>
        <ConnState remote={remote} />
      </div>
      {list.map((p) => {
        const live = p.sessions.filter((s) => !exited(s) || s.id === selected);
        return (
          <div key={p.key}>
            <button className="ph" onClick={() => go("p/" + encodeURIComponent(p.key))}>
              <Avatar name={p.name} size="sm" />
              {p.name}
              <span>{live.length}</span>
            </button>
            {live.map((s) => (
              <SessionRow key={s.id} s={s} remote={remote} go={go} selected={s.id === selected} compact />
            ))}
          </div>
        );
      })}
      <button className="side-foot" onClick={() => go("settings")}>
        ⚙︎ 设置
      </button>
    </aside>
  );
}

export function ConnState({ remote }: { remote: Remote }) {
  const text = { connecting: "连接中", online: "", offline: "未连接" }[remote.conn];
  if (!text) return null;
  return (
    <span className={"conn " + remote.conn} title={remote.error}>
      {text}
    </span>
  );
}

export function WideHome({ remote, go }: { remote: Remote; go: Go }) {
  useRemote(remote);
  return (
    <div className="body wide-home">
      <TodoCards remote={remote} go={go} />
      <p className="empty">从左边选一个会话</p>
    </div>
  );
}
