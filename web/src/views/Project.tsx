// A project on a phone: its live sessions, the recently ended ones, and
// the box to start a new session in it. A wide window has no such page:
// the sidebar lists the project and the home starts a session in it.

import { useEffect, useRef, useState } from "react";
import { compareLive, compareEnded, exited, machineName, projectName, shortPath } from "../model";
import type { Remote } from "../remote";
import { Avatar, NavBar, useNow, useRemote } from "../ui";
import { colsOf, EndedRow, SessionRow } from "./Home";
import { ConfigSheet, ConfigSummary, NewBox, useNewSession, type Go } from "./NewSession";
import { RemovedNotice } from "./Projects";
import { isHidden } from "../prefs";
import { ChevronRight, Ellipsis } from "../icons";

const PAGE = 20;

export function ProjectPage({ remote, pkey, go, onBack }: { remote: Remote; pkey: string; go: Go; onBack: () => void }) {
  useRemote(remote);
  useNow(30_000);
  const ns = useNewSession(remote, pkey, go);
  const p = ns.p;
  const name = p?.name ?? projectName(pkey);
  const live = p?.sessions.filter((s) => !exited(s)).sort(compareLive) ?? [];
  const ended = p?.sessions.filter(exited).sort(compareEnded) ?? [];
  const machines = (p?.machines.length ? p.machines : [ns.cfg.machineId]).filter(Boolean).map((id) => machineName(remote.machines.get(id)));
  const [shownEnded, setShownEnded] = useState(3);
  // Ended sessions come a page at a time; the project's machines say how
  // many there are until every page is here.
  const totalEnded = remote.moreSessions ? Math.max(p?.ended ?? 0, ended.length) : ended.length;
  useEffect(() => {
    if (shownEnded > ended.length && ended.length < totalEnded) void remote.loadMoreSessions();
  });
  const cols = colsOf(remote, p?.sessions ?? [], false);
  const [scrolled, setScrolled] = useState(false);
  const [typing, setTyping] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(() => remote.demoFlag("project-settings"));
  const endedRef = useRef<HTMLDivElement>(null);
  const [focus] = useState(() => remote.focusProject === pkey);
  useEffect(() => {
    if (remote.focusProject === pkey) remote.focusProject = "";
  }, [remote, pkey]);
  const head = (
    <div className="phead">
      <Avatar name={name} size="lg" />
      <div>
        <div className="pname" title={name}>{name}</div>
        <div className="muted small ppath">{shortPath(pkey)}{machines.length > 0 && ` · ${machines.join(" · ")}`}</div>
        <div className="small pmore">
          <span className="muted">{live.length} 个进行中 · </span>
          <button
            className="link"
            onClick={() => {
              setShownEnded(Math.max(ended.length, PAGE));
              setTimeout(() => endedRef.current?.scrollIntoView({ behavior: "smooth" }));
            }}
          >
            已结束 {totalEnded}<ChevronRight />
          </button>
        </div>
        <ConfigSummary summary={ns.summary} chip onClick={() => setSettingsOpen(true)} />
      </div>
    </div>
  );
  // A removed project is still there to see; new sessions wait until it
  // is restored.
  const hidden = isHidden(remote.projectPrefs(), pkey);
  const lists = (
    <>
      <RemovedNotice remote={remote} pkey={pkey} />
      {live.length > 0 && (
        <div className="label">
          进行中 <span>{live.length}</span>
        </div>
      )}
      {live.map((s, i) => (
        <SessionRow key={s.id} remote={remote} s={s} go={go} cols={cols} last={i === live.length - 1} />
      ))}
      {ended.length > 0 && (
        <div className="label" ref={endedRef}>
          最近结束
        </div>
      )}
      {ended.slice(0, shownEnded).map((s) => (
        <EndedRow key={s.id} s={s} go={go} />
      ))}
      {totalEnded > Math.min(shownEnded, ended.length) && (
        <button className="todo-more" onClick={() => setShownEnded((n) => Math.min(n, ended.length) + PAGE)}>
          {shownEnded > ended.length ? "正在加载更早的会话…" : <>全部 {totalEnded} 个已结束的会话<ChevronRight /></>}
        </button>
      )}
      {!p && !hidden && <p className="empty">项目在第一个会话开始后才会出现在列表里。</p>}
    </>
  );
  return (
    <div className="page">
      {/* With the keyboard up the project's head folds into the bar. */}
      <NavBar onBack={onBack} title={scrolled || typing ? name : undefined} sub={scrolled || typing ? <ConfigSummary summary={ns.summary} onClick={() => setSettingsOpen(true)} /> : undefined} right={<button className="more" onClick={() => setSettingsOpen(true)} aria-label="项目设置"><Ellipsis /></button>} />
      <div className="body" onScroll={(e) => setScrolled(e.currentTarget.scrollTop > 64)}>
        {!typing && <div className="pad">{head}</div>}
        {lists}
      </div>
      {!hidden && <NewBox ns={ns} name={name} autoFocus={focus} onFocus={setTyping} />}
      {settingsOpen && <ConfigSheet ns={ns} onClose={() => setSettingsOpen(false)} />}
    </div>
  );
}
