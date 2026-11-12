import { t } from "../i18n";
// Everything that lists or manages projects (client-v34): one row and one
// list for the scope menu, the project picker and 项目管理, and the two
// things done to a project there, pinning and removing. Removing only
// takes the project off the list: no session or history goes away, and no
// folder on a computer is moved or deleted; adding the folder again as a
// new project lists it again.

import { useState, type ReactNode } from "react";
import { ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import { AGENTS } from "../agents";
import { isHidden, isPinned, groupProjects } from "../prefs";
import { exited, needsAttention, projectName, projects, shortPath, type Project } from "../model";
import type { Remote, SessionView } from "../remote";
import { ActionSheet, Alert, Avatar, Banner, fromHere, NavBar, PathText, useNow, useRemote } from "../ui";
import type { Go } from "./NewSession";
import { NewProjectSheet } from "./NewSession";
import { saveScope } from "../scope";
import { Ellipsis, LoaderCircle, Plus, Search, X } from "../icons";

type P = Project<SessionView>;

/** The account's projects as the lists group them. */
export function useProjectGroups(remote: Remote) {
  const list = projects(remote.sessions.values(), remote.machines.values(), remote.projectPrefs().settings);
  return groupProjects(list, remote.projectPrefs());
}

/** Whether the list can be changed now, and why not. */
export function prefsBlocked(remote: Remote): string {
  if (remote.prefs.status === "loading") return t("projects.loading");
  if (remote.prefs.status === "error" && !remote.prefs.snap) return t("projects.load_failed");
  if (remote.conn !== "online") return t("projects.connect_to_edit");
  return "";
}

/** What the list's sync is doing: saving, failed, or changed elsewhere. */
export function PrefsBanner({ remote, quiet }: { remote: Remote; quiet?: boolean }) {
  const now = useNow(1000);
  const p = remote.prefs;
  if (p.failed)
    return (
      <Banner kind="err" action={t("common.retry")} onAction={() => remote.retryPrefs()}>
        {t("projects.save_failed", { reason: p.failed.error })}
      </Banner>
    );
  if (p.pending.size > 0) return <Banner><LoaderCircle /> {t("projects.syncing")}</Banner>;
  if (quiet) return null;
  if (p.status === "error" && !p.snap)
    return (
      <Banner kind="err" action={t("common.retry")} onAction={() => void remote.loadPrefs()}>
        {t("projects.load_failed")}
      </Banner>
    );
  if (p.changedAt && now - p.changedAt < 8000) return <Banner>{t("projects.changed_elsewhere")}</Banner>;
  return null;
}

/** How many of some sessions wait for the user and how many are running,
 * counted as the home's 需要处理 and 进行中 are: 「2 个待处理 · 3 个进行中」,
 * a part that is 0 left out, "" when both are. */
export function sessionCounts(remote: Remote, sessions: Iterable<SessionView>): string {
  let todo = 0;
  let live = 0;
  for (const s of sessions) {
    if (needsAttention(s, remote.presence.get(s.machineId))) todo++;
    else if (!s.info?.taskOwned && !exited(s)) live++;
  }
  return todo && live ? t("projects.pending_and_live", { count: todo, live }) : todo ? t("projects.pending_count", { count: todo }) : live ? t("projects.live_count", { count: live }) : "";
}

/** A project's second line, the same wherever it is listed: its folder,
 * then how many sessions wait and run, or that no computer of it is
 * online. */
export function projectSub(remote: Remote, p: P): ReactNode {
  const counts = sessionCounts(remote, p.sessions);
  const offline = p.machines.length > 0 && remote.listed && p.machines.every((id) => !remote.machines.get(id)?.online);
  return (
    <>
      <PathText path={shortPath(p.key)} />
      {counts ? ` · ${counts}` : offline ? ` · ${t("projects.computer_offline")}` : ""}
    </>
  );
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
      <button className="dots" onClick={onMore} disabled={busy} aria-label={t("projects.manage_named", { name: p.name })}>
        {busy ? <LoaderCircle /> : <Ellipsis />}
      </button>
    </div>
  );
}

/** The projects on the list, the same in every place that shows them, in
 * rounded groups: 常用 then 其他 (the titles only when there are both), a
 * search above more than eight, and 新项目… at the end. `first` goes in a
 * group above the projects while nothing is searched for; `children` in
 * the last group, after 新项目…. */
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
      {g.pinned.length + g.others.length > 8 && (
        <label className="find">
          <Search />
          <input aria-label={t("projects.search")} placeholder={t("projects.search")} value={query} onChange={(e) => setQuery(e.target.value)} onKeyDown={(e) => e.key === "Escape" && setQuery("")} />
          {query && (
            <button className="x" aria-label={t("common.clear_search")} onClick={() => setQuery("")}>
              <X />
            </button>
          )}
        </label>
      )}
      <div className="plist" role={first ? "menu" : undefined}>
        {!q && first && <div className="grp">{first}</div>}
        {titled && <div className="fl">{t("projects.pinned")}</div>}
        {pinned.length > 0 && <div className="grp">{pinned.map(item)}</div>}
        {titled && <div className="fl">{t("projects.others")}</div>}
        {others.length > 0 && <div className="grp">{others.map(item)}</div>}
        {q && pinned.length + others.length === 0 && <div className="hint">{t("projects.search_empty", { query: query.trim() })}</div>}
        <div className="grp">
          <button className="pi add" onClick={onNew}>
            <Plus /> {t("projects.new")}
          </button>
          {children}
        </div>
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
    const sets = AGENTS.map((e) => ({ agent: e.agent, model: "", effort: "", mode: ApprovalMode.UNSPECIFIED, standardContext: false }));
    void remote.changePrefs("settings", path, { id: crypto.randomUUID(), kind: "settings", path, at: Date.now(), settings: { machineId, sets } });
  }
}

/** What 设为常用 is called for the project at path now, and what it does. */
export function pinAction(remote: Remote, path: string): { label: string; run: () => void } {
  const pinned = isPinned(remote.projectPrefs(), path);
  return { label: pinned ? t("projects.unpin") : t("projects.pin"), run: () => void remote.changePrefs(pinned ? "unpin" : "pin", path) };
}

/** Asks before a project leaves the list: there is no undo, only adding
 * it again. */
export function RemoveProjectAlert({ remote, path, onDone, onCancel }: { remote: Remote; path: string; onDone?: () => void; onCancel: () => void }) {
  return (
    <Alert
      title={t("projects.remove_title", { name: projectName(path) })}
      text={t("projects.remove_explanation")}
      ok={t("common.remove")}
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

/** 项目管理: the list, with ⋯ on each project for 常用 and 移除; on a
 * wide window in the main area's middle column of about 640. */
export function ManagePage({ remote, go, onBack }: { remote: Remote; go: Go; onBack: () => void }) {
  useRemote(remote);
  const [menu, setMenu] = useState<P>();
  const [removing, setRemoving] = useState("");
  const [adding, setAdding] = useState(false);
  const blocked = prefsBlocked(remote);
  const pending = remote.prefs.pending;
  const pin = menu && pinAction(remote, menu.key);
  return (
    <div className="page set">
      <NavBar onBack={onBack} title={t("projects.manage")} />
      <div className="body">
        <div className="manage">
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
                label={t("projects.view_sessions", { name: p.name })}
                onClick={() => {
                  saveScope(p.key);
                  go("");
                }}
                onMore={() => setMenu(p)}
              />
            )}
          />
          <div className="gf">{t("projects.remove_note")}</div>
        </div>
      </div>
      {menu && pin && (
        <ActionSheet
          info={
            <>
              <b>{menu.name}</b>
              <span className="mono"><PathText path={shortPath(menu.key)} /></span>
            </>
          }
          actions={[
            { label: pin.label, onClick: pin.run, disabled: !!blocked, note: blocked || undefined },
            { label: t("projects.remove_action"), red: true, onClick: () => setRemoving(menu.key), disabled: !!blocked, group: 1 },
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
    </div>
  );
}

/** On the page of a project that is off the list (reached from one of its
 * sessions): it is still there to see, and one tap lists it again. */
export function RemovedNotice({ remote, pkey }: { remote: Remote; pkey: string }) {
  if (!isHidden(remote.projectPrefs(), pkey)) return null;
  const busy = remote.prefs.pending.has(pkey);
  return (
    <>
      {/* A rare state: a line with its action, not the page's main button. */}
      <Banner action={busy ? t("projects.adding") : t("projects.add_again")} onAction={prefsBlocked(remote) || busy ? undefined : () => void remote.changePrefs("unhide", pkey)}>
        {t("projects.removed")}
      </Banner>
      <PrefsBanner remote={remote} quiet />
    </>
  );
}
