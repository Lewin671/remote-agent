// A project: its live sessions, the recently ended ones, and the box to
// start a new session in it.

import { useEffect, useRef, useState } from "react";
import { compareLive, compareEnded, exited, machineName, projectName, shortPath } from "../model";
import type { Remote } from "../remote";
import { Avatar, NavBar, useNow, useRemote } from "../ui";
import { SessionRow } from "./Home";
import { NewBox, useNewSession, type Go } from "./NewSession";
import { RemovedNotice } from "./Projects";
import { isHidden } from "../prefs";

const PAGE = 20;

export function ProjectPage({ remote, pkey, go, onBack, wide }: { remote: Remote; pkey: string; go: Go; onBack: () => void; wide: boolean }) {
  useRemote(remote);
  useNow(30_000);
  const ns = useNewSession(remote, pkey, go);
  const p = ns.p;
  const name = p?.name ?? projectName(pkey);
  const live = p?.sessions.filter((s) => !exited(s)).sort(compareLive) ?? [];
  const ended = p?.sessions.filter(exited).sort(compareEnded) ?? [];
  const [shownEnded, setShownEnded] = useState(wide ? PAGE : 3);
  const [scrolled, setScrolled] = useState(false);
  const [typing, setTyping] = useState(false);
  const endedRef = useRef<HTMLDivElement>(null);
  const [focus] = useState(() => remote.focusProject === pkey);
  useEffect(() => {
    if (remote.focusProject === pkey) remote.focusProject = "";
  }, [remote, pkey]);
  const machines = p ? p.machines.map((id) => machineName(remote.machines.get(id))) : [machineName(remote.machines.get(ns.cfg.machineId))];
  const head = (
    <div className="phead">
      <Avatar name={name} size="lg" />
      <div>
        <div className="pname" title={name}>{name}</div>
        <div className="muted small">{[shortPath(pkey), ...machines].join(" · ")}</div>
        {!wide && ended.length > 0 && (
          <div className="small pmore">
            {live.length > 0 && <span className="muted">{live.length} 个进行中 · </span>}
            <button
              className="link"
              onClick={() => {
                setShownEnded(ended.length);
                setTimeout(() => endedRef.current?.scrollIntoView({ behavior: "smooth" }));
              }}
            >
              已结束 {ended.length} ›
            </button>
          </div>
        )}
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
        <SessionRow key={s.id} remote={remote} s={s} go={go} showProject={false} last={i === live.length - 1} />
      ))}
      {ended.length > 0 && (
        <div className="label" ref={endedRef}>
          {wide ? (
            <>
              已结束 <span>{ended.length}</span>
            </>
          ) : (
            "最近结束"
          )}
        </div>
      )}
      {ended.slice(0, shownEnded).map((s) => (
        <SessionRow key={s.id} remote={remote} s={s} go={go} showProject={false} />
      ))}
      {ended.length > shownEnded && (
        <button className="todo-more" onClick={() => setShownEnded((n) => n + PAGE)}>
          {wide ? "更多" : `全部 ${ended.length} 个已结束的会话`} ›
        </button>
      )}
      {!p && !hidden && <p className="empty">项目在第一个会话开始后才会出现在列表里。</p>}
    </>
  );
  if (wide)
    return (
      <div className="body">
        <div className="wpage">
          {head}
          {!hidden && <NewBox ns={ns} name={name} autoFocus={focus} variant="prompt" />}
          {lists}
        </div>
      </div>
    );
  return (
    <div className="page">
      {/* With the keyboard up the project's head folds into the bar. */}
      <NavBar onBack={onBack} title={scrolled || typing ? name : undefined} sub={scrolled || typing ? shortPath(pkey) : undefined} />
      <div className="body" onScroll={(e) => setScrolled(e.currentTarget.scrollTop > 64)}>
        {!typing && <div className="pad">{head}</div>}
        {lists}
      </div>
      {!hidden && <NewBox ns={ns} name={name} autoFocus={focus} onFocus={setTyping} />}
    </div>
  );
}
