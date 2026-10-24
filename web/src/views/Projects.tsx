// Everything that lists or manages projects (client-v34): one row and one
// list for the scope menu, the project picker and 项目管理, and the two
// things done to a project there, pinning and removing. Removing only
// takes the project off the list: no session or history goes away, and no
// folder on a computer is moved or deleted; adding the folder again as a
// new project lists it again.

import { useState, type ReactNode } from "react";
import { Agent, ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import { isHidden, isPinned, groupProjects } from "../prefs";
import { exited, needsAttention, projectName, projects, shortPath, type Project } from "../model";
import type { Remote, SessionView } from "../remote";
import { ActionSheet, Alert, Avatar, Banner, fromHere, NavBar, useNow, useRemote } from "../ui";
import type { Go } from "./NewSession";
import { NewProjectSheet } from "./NewSession";
import { saveScope } from "../scope";
import { Ellipsis, LoaderCircle, Plus } from "../icons";

type P = Project<SessionView>;

/** The account's projects as the lists group them. */
export function useProjectGroups(remote: Remote) {
  const list = projects(remote.sessions.values(), remote.machines.values(), remote.projectPrefs().settings);
  return groupProjects(list, remote.projectPrefs());
}

/** Whether the list can be changed now, and why not. */
export function prefsBlocked(remote: Remote): string {
  if (remote.prefs.status === "loading") return "正在读取项目列表…";
  if (remote.prefs.status === "error" && !remote.prefs.snap) return "没能读取项目列表";
  if (remote.conn !== "online") return "连上服务器后才能修改";
  return "";
}

/** What the list's sync is doing: saving, failed, or changed elsewhere. */
export function PrefsBanner({ remote, quiet }: { remote: Remote; quiet?: boolean }) {
  const now = useNow(1000);
  const p = remote.prefs;
  if (p.failed)
    return (
      <Banner kind="err" action="重试" onAction={() => remote.retryPrefs()}>
        没能保存，列表保持原样：{p.failed.error}
      </Banner>
    );
  if (p.pending.size > 0) return <Banner><LoaderCircle /> 正在同步项目列表…</Banner>;
  if (quiet) return null;
  if (p.status === "error" && !p.snap)
    return (
      <Banner kind="err" action="重试" onAction={() => void remote.loadPrefs()}>
        没能读取项目列表
      </Banner>
    );
  if (p.changedAt && now - p.changedAt < 8000) return <Banner>项目列表已在另一客户端更新</Banner>;
  return null;
}

/** How many of some sessions wait for the user and how many are running,
 * counted as the home's 需要处理 and 进行中 are: 「2 个待处理 · 3 个进行中」,
 * a part that is 0 left out, "" when both are. */
export function sessionCounts(remote: Remote, sessions: Iterable<SessionView>): string {
  let todo = 0;
  let live = 0;
  for (const s of sessions) {
    if (s.info?.taskOwned) continue;
    if (needsAttention(s, remote.machines.get(s.machineId))) todo++;
    else if (!exited(s)) live++;
  }
  return [todo && `${todo} 个待处理`, live && `${live} 个进行中`].filter(Boolean).join(" · ");
}

/** A project's second line, the same wherever it is listed: its folder,
 * then how many sessions wait and run, or that no computer of it is
 * online. */
export function projectSub(remote: Remote, p: P): string {
  const counts = sessionCounts(remote, p.sessions);
  const offline = p.machines.length > 0 && p.machines.every((id) => !remote.machines.get(id)?.online);
  return shortPath(p.key) + (counts ? ` · ${counts}` : offline ? " · 电脑不在线" : "");
}

/** One project in a list. The whole row is one button, with `end` at its
 * right; with onMore the name is the button and ⋯ is a second one. */
export function ProjectItem({
  remote,
  p,
  onClick,
  end,
  onMore,
  busy,
  checked,
  label,
}: {
  remote: Remote;
  p: P;
  onClick: () => void;
  end?: ReactNode;
  onMore?: () => void;
  busy?: boolean;
  /** In a menu of scopes: whether this is the current one. */
  checked?: boolean;
  label?: string;
}) {
  const text = (
    <>
      <Avatar name={p.name} size="sm" />
      <div>
        <b>{p.name}</b>
        <small>{projectSub(remote, p)}</small>
      </div>
    </>
  );
  if (!onMore)
    return (
      <button className="pi" role={checked === undefined ? undefined : "menuitemradio"} aria-checked={checked} aria-label={label} onClick={onClick}>
        {text}
        {end}
      </button>
    );
  return (
    <div className={"pi split" + (busy ? " busy" : "")}>
      <button className="pm" onClick={onClick} aria-label={label}>
        {text}
      </button>
      <button className="dots" onClick={onMore} disabled={busy} aria-label={`管理 ${p.name}`}>
        {busy ? <LoaderCircle /> : <Ellipsis />}
      </button>
    </div>
  );
}

/** The projects on the list, the same in every place that shows them:
 * 常用 then 其他 (the titles only when there are both), a search above more
 * than eight, and 新项目… at the end. `first` goes above the projects while
 * nothing is searched for; `children` after 新项目…. */
export function ProjectList({ remote, item, first, onNew, children }: { remote: Remote; item: (p: P) => ReactNode; first?: ReactNode; onNew: () => void; children?: ReactNode }) {
  const [query, setQuery] = useState("");
  const g = useProjectGroups(remote);
  const q = query.trim().toLocaleLowerCase();
  const match = (p: P) => (p.name + " " + p.key).toLocaleLowerCase().includes(q);
  const pinned = g.pinned.filter(match);
  const others = g.others.filter(match);
  const titled = pinned.length > 0 && others.length > 0;
  return (
    <>
      {g.pinned.length + g.others.length > 8 && <input className="field project-search" aria-label="搜索项目" placeholder="搜索项目" value={query} onChange={(e) => setQuery(e.target.value)} />}
      <div className="plist" role={first ? "menu" : undefined}>
        {!q && first}
        {titled && <div className="fl">常用</div>}
        {pinned.map(item)}
        {titled && <div className="fl">其他</div>}
        {others.map(item)}
        {q && pinned.length + others.length === 0 && <div className="hint">没有名字或路径里有「{query.trim()}」的项目</div>}
        <button className="pi add" onClick={onNew}>
          <Plus /> 新项目…
        </button>
        {children}
      </div>
    </>
  );
}

/** Lists the folder at path on machineId as a project, before its first
 * session, here and on the account's other clients. A folder removed from
 * the list earlier is listed again. */
export function addProject(remote: Remote, machineId: string, path: string) {
  remote.draftMachines.set(path, machineId);
  const prefs = remote.projectPrefs();
  if (isHidden(prefs, path)) void remote.changePrefs("unhide", path);
  if (prefs.settings.find((s) => s.path === path)?.machineId !== machineId) {
    const sets = [Agent.CODEX, Agent.CLAUDE].map((agent) => ({ agent, model: "", effort: "", mode: ApprovalMode.UNSPECIFIED, standardContext: false }));
    void remote.changePrefs("settings", path, { id: crypto.randomUUID(), kind: "settings", path, at: Date.now(), settings: { machineId, sets } });
  }
}

/** What 设为常用 is called for the project at path now, and what it does. */
export function pinAction(remote: Remote, path: string): { label: string; run: () => void } {
  const pinned = isPinned(remote.projectPrefs(), path);
  return { label: pinned ? "取消常用" : "设为常用", run: () => void remote.changePrefs(pinned ? "unpin" : "pin", path) };
}

/** Asks before a project leaves the list: there is no undo, only adding
 * it again. */
export function RemoveProjectAlert({ remote, path, onDone, onCancel }: { remote: Remote; path: string; onDone?: () => void; onCancel: () => void }) {
  return (
    <Alert
      title={`移除 ${projectName(path)}？`}
      text="只从项目列表里移除，会话、历史和电脑上的目录都不动。以后要用，再用「新项目」添加这个目录。"
      ok="移除"
      danger
      onCancel={onCancel}
      onOk={() => {
        onCancel();
        const done = fromHere(() => onDone?.());
        void remote.changePrefs("hide", path).then((ok) => ok && done());
      }}
    />
  );
}

/** 项目管理: the list, with ⋯ on each project for 常用 and 移除. On a wide
 * window it is the main area's page; on a phone, a page of its own. */
export function ManagePage({ remote, go, onBack, wide }: { remote: Remote; go: Go; onBack: () => void; wide: boolean }) {
  useRemote(remote);
  const [menu, setMenu] = useState<P>();
  const [removing, setRemoving] = useState("");
  const [adding, setAdding] = useState(false);
  const blocked = prefsBlocked(remote);
  const pending = remote.prefs.pending;
  const pin = menu && pinAction(remote, menu.key);
  const body = (
    <>
      <PrefsBanner remote={remote} />
      <ProjectList
        remote={remote}
        onNew={() => setAdding(true)}
        item={(p) => (
          <ProjectItem
            key={p.key}
            remote={remote}
            p={p}
            busy={pending.has(p.key)}
            label={`看 ${p.name} 的会话`}
            onClick={() => {
              saveScope(p.key);
              go("");
            }}
            onMore={() => setMenu(p)}
          />
        )}
      />
      <div className="hint">移除只是不再列出这个项目，会话和电脑上的目录都不动。</div>
    </>
  );
  const layers = (
    <>
      {menu && pin && (
        <ActionSheet
          info={
            <>
              <b>{menu.name}</b>
              <span className="mono">{shortPath(menu.key)}</span>
            </>
          }
          actions={[
            { label: pin.label, onClick: pin.run, disabled: !!blocked, note: blocked || undefined },
            { label: "移除项目…", red: true, onClick: () => setRemoving(menu.key), disabled: !!blocked },
          ]}
          onClose={() => setMenu(undefined)}
        />
      )}
      {removing && <RemoveProjectAlert remote={remote} path={removing} onCancel={() => setRemoving("")} />}
      {adding && (
        <NewProjectSheet
          remote={remote}
          onClose={() => setAdding(false)}
          onDone={(machineId, path) => {
            addProject(remote, machineId, path);
            setAdding(false);
          }}
        />
      )}
    </>
  );
  if (wide)
    return (
      <div className="body">
        <div className="wpage manage">
          <h3 className="wtitle">项目管理</h3>
          {body}
        </div>
        {layers}
      </div>
    );
  return (
    <div className="page">
      <NavBar onBack={onBack} title={<span className="tname">项目管理</span>} />
      <div className="body">
        <div className="pad manage">{body}</div>
      </div>
      {layers}
    </div>
  );
}

/** On the page of a project that is off the list (reached from one of its
 * sessions): it is still there to see, and one tap lists it again. */
export function RemovedNotice({ remote, pkey }: { remote: Remote; pkey: string }) {
  if (!isHidden(remote.projectPrefs(), pkey)) return null;
  const busy = remote.prefs.pending.has(pkey);
  return (
    <div className="removed-note">
      {/* A rare state: a line with its action, not the page's main button. */}
      <Banner action={busy ? "正在添加…" : "重新添加"} onAction={prefsBlocked(remote) || busy ? undefined : () => void remote.changePrefs("unhide", pkey)}>
        这个项目已从列表移除
      </Banner>
      <PrefsBanner remote={remote} quiet />
    </div>
  );
}
