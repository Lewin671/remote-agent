// Project management (client-v2 项目管理): pinning, removing a project from
// the list and restoring it. It changes only the list's entries: no
// session, history or reminder of a removed project goes away, and no
// folder on a computer is moved or deleted.

import { useEffect, useState } from "react";
import { isHidden, isPinned, groupProjects } from "../prefs";
import { projectName, projects, shortPath, type Project } from "../model";
import type { Remote, SessionView } from "../remote";
import { ActionSheet, Avatar, Banner, NavBar, useNow, useRemote } from "../ui";
import type { Go } from "./NewSession";

/** The account's projects as the lists group them. */
export function useProjectGroups(remote: Remote) {
  const list = projects(remote.sessions.values());
  return groupProjects(list, remote.projectPrefs());
}

/** Whether the list can be changed now, and why not. */
export function prefsBlocked(remote: Remote): string {
  if (remote.prefs.status === "unsupported") return "服务器版本较旧，还不能同步常用和已移除的项目";
  if (remote.prefs.status === "loading") return "正在读取项目列表的设置…";
  if (remote.prefs.status === "error" && !remote.prefs.snap) return "没能读取项目列表的设置";
  if (remote.conn !== "online") return "当前显示缓存 · 连上后才能管理";
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
  if (p.pending.size > 0) return <Banner>◌ 正在同步项目列表…</Banner>;
  if (quiet) return null;
  const blocked = prefsBlocked(remote);
  if (blocked)
    return (
      <Banner kind={p.status === "error" ? "err" : ""} action={p.status === "error" ? "重试" : undefined} onAction={() => void remote.loadPrefs()}>
        {blocked}
      </Banner>
    );
  if (p.changedAt && now - p.changedAt < 8000) return <Banner>项目列表已在另一客户端更新</Banner>;
  return null;
}

/** Actions on one project, with the removal undo. */
function useActions(remote: Remote) {
  const [menu, setMenu] = useState<Project<SessionView> | { key: string; name: string }>();
  const [undo, setUndo] = useState<{ key: string; name: string }>();
  useEffect(() => {
    if (!undo) return;
    const t = setTimeout(() => setUndo(undefined), 8000);
    return () => clearTimeout(t);
  }, [undo]);
  const blocked = !!prefsBlocked(remote);
  const remove = async (key: string) => {
    if (await remote.changePrefs("hide", key)) setUndo({ key, name: projectName(key) });
  };
  const sheet = menu && (
    <ActionSheet
      info={
        <>
          <b>{menu.name}</b>
          <br />
          <span className="mono">{shortPath(menu.key)}</span>
        </>
      }
      actions={
        blocked
          ? []
          : [
              isPinned(remote.projectPrefs(), menu.key)
                ? { label: "取消常用", onClick: () => void remote.changePrefs("unpin", menu.key) }
                : { label: "设为常用", onClick: () => void remote.changePrefs("pin", menu.key) },
              { label: "从项目列表移除", red: true, onClick: () => void remove(menu.key) },
            ]
      }
      onClose={() => setMenu(undefined)}
    />
  );
  const toast = undo && (
    <div className="toast plain">
      <span>已移除 {undo.name}</span>
      <button
        className="link"
        onClick={() => {
          const key = undo.key;
          setUndo(undefined);
          void remote.changePrefs("unhide", key);
        }}
      >
        撤销
      </button>
    </div>
  );
  return { open: setMenu, sheet, toast, undone: undo, blocked };
}

function ManageRow({ p, onMore, busy }: { p: { key: string; name: string }; onMore: () => void; busy: boolean }) {
  return (
    <div className={"manage-row" + (busy ? " busy" : "")}>
      <Avatar name={p.name} size="sm" />
      <div className="pt">
        <div className="pn">
          {p.name}
        </div>
        <div className="ps">{shortPath(p.key)}</div>
      </div>
      <button className="dots" onClick={onMore} aria-label={`管理 ${p.name}`}>
        {busy ? "◌" : "⋯"}
      </button>
    </div>
  );
}

/** 项目管理: pinned projects in their order, the others by liveness then latest session creation, and
 * the way to the removed ones. */
export function ManagePage({ remote, go, onBack, wide }: { remote: Remote; go: Go; onBack: () => void; wide: boolean }) {
  useRemote(remote);
  const g = useProjectGroups(remote);
  const a = useActions(remote);
  const hiddenCount = remote.projectPrefs().hidden.length;
  const pending = remote.prefs.pending;
  if (wide)
    return (
      <div className="body">
        <div className="wpage">
          <h3 className="wtitle">项目管理</h3>
          <p className="muted small">常用和已移除项目跨客户端同步；移除只隐藏入口，保留会话、历史和提醒。不移动或删除电脑上的目录。</p>
          <PrefsBanner remote={remote} />
          {[
            ["常用项目", g.pinned, true],
            ["其他项目", g.others, false],
          ].map(([label, list, pinned]) =>
            (list as Project<SessionView>[]).length ? (
              <div key={label as string}>
                <div className="label flush">{label as string}</div>
                <table className="manage-table">
                  <tbody>
                    {(list as Project<SessionView>[]).map((p) => (
                      <tr key={p.key}>
                        <td>
                          <b>{p.name}</b>
                          <span className="path">{shortPath(p.key)}</span>
                        </td>
                        <td>
                          <button className="link" disabled={a.blocked || pending.has(p.key)} onClick={() => void remote.changePrefs(pinned ? "unpin" : "pin", p.key)}>
                            {pending.has(p.key) ? "◌" : pinned ? "取消常用" : "设为常用"}
                          </button>
                          {" · "}
                          <button className="link" onClick={() => a.open(p)} aria-label={`管理 ${p.name}`}>
                            ⋯
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : null,
          )}
          <div className="wide-project-tools">
            <button className="chip linkchip" onClick={() => go("projects/removed")}>
              {hiddenCount ? `已移除 ${hiddenCount} 个 · 查看与恢复 ›` : "没有已移除的项目"}
            </button>
          </div>
          {a.toast}
        </div>
        {a.sheet}
      </div>
    );
  return (
    <div className="page">
      <NavBar onBack={onBack} title={<span className="tname">项目管理</span>} />
      <div className="body">
        <div className="manage-hint">只管理项目入口，不移动或删除电脑上的目录。</div>
        <div className="pad">
          <PrefsBanner remote={remote} />
        </div>
        {g.pinned.length > 0 && (
          <div className="label">
            常用 <span>{g.pinned.length}</span>
          </div>
        )}
        {g.pinned.map((p) => (
          <ManageRow key={p.key} p={p} busy={pending.has(p.key)} onMore={() => a.open(p)} />
        ))}
        <div className="label">
          其他项目 <span>{g.others.length}</span>
        </div>
        {g.others.map((p) => (
          <ManageRow key={p.key} p={p} busy={pending.has(p.key)} onMore={() => a.open(p)} />
        ))}
        {a.undone && <div className="manage-hint">{a.undone.name} 的会话和历史仍然保留。有新的待处理项时，主页仍会显示。</div>}
        <div className="label">
          <span>已移除 {hiddenCount} 个项目</span>
          <button className="link" onClick={() => go("projects/removed")}>
            查看 ›
          </button>
        </div>
        <div className="manage-hint">常用和已移除项目在账户内同步。断线时可查看缓存，修改暂停；不会影响会话和提醒。</div>
      </div>
      {a.toast}
      {a.sheet}
    </div>
  );
}

/** Projects removed from the list, each with 恢复. */
export function RemovedPage({ remote, onBack, wide }: { remote: Remote; onBack: () => void; wide: boolean }) {
  useRemote(remote);
  const hidden = remote.projectPrefs().hidden;
  const blocked = !!prefsBlocked(remote);
  const body = (
    <>
      <div className="manage-hint">恢复只让项目重新出现在列表里，不会启动或恢复会话。</div>
      <div className="pad">
        <PrefsBanner remote={remote} />
      </div>
      {hidden.length === 0 && <div className="manage-hint">没有已移除的项目</div>}
      {hidden.map((h) => (
        <div key={h.path} className="manage-row">
          <Avatar name={projectName(h.path)} size="sm" />
          <div className="pt">
            <div className="pn">{projectName(h.path)}</div>
            <div className="ps">{shortPath(h.path)}</div>
          </div>
          <button className="dots restore" disabled={blocked || remote.prefs.pending.has(h.path)} onClick={() => void remote.changePrefs("unhide", h.path)}>
            {remote.prefs.pending.has(h.path) ? "◌" : "恢复"}
          </button>
        </div>
      ))}
      <div className="manage-hint">已移除的项目不出现在项目列表和新建选择器中；进行中会话、待处理卡片和通知入口仍然可访问。</div>
    </>
  );
  if (wide)
    return (
      <div className="body">
        <div className="wpage">
          <h3 className="wtitle">已移除的项目</h3>
          {body}
        </div>
      </div>
    );
  return (
    <div className="page">
      <NavBar onBack={onBack} title={<span className="tname">已移除的项目</span>} />
      <div className="body">{body}</div>
    </div>
  );
}

/** On a removed project's own page: it is still there to see and use,
 * and restoring it is one tap. */
export function RemovedNotice({ remote, pkey }: { remote: Remote; pkey: string }) {
  if (!isHidden(remote.projectPrefs(), pkey)) return null;
  const busy = remote.prefs.pending.has(pkey);
  return (
    <div className="removed-note">
      {/* A rare state: a line with its action, not the page's main button. */}
      <Banner action={busy ? "正在恢复…" : "恢复"} onAction={prefsBlocked(remote) || busy ? undefined : () => void remote.changePrefs("unhide", pkey)}>
        这个项目已从列表移除
      </Banner>
      <PrefsBanner remote={remote} quiet />
    </div>
  );
}
