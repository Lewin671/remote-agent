// The home screen: scope, headline, what waits, what runs, what ended,
// sidebar, and wide main area — all rendered straight from HomeViewModel.

import { useEffect, useRef, useState, type ReactNode } from "react";
import { AgentMark } from "../agent-mark";
import { useCoreHost, useHomeView, useNewSessionView, useProjectView, type Go } from "../core/host";
import { renderText } from "../core/text";
import {
  ConnState,
  HeadlineKind,
  HomeEmptyKind,
  SessionDisplayStatus,
  TailKind,
  type AttentionCardView,
  type ConnectionStatusView,
  type EndedGroupView,
  type HomeEmptyView,
  type HomeHeadlineView,
  type HomeViewModel,
  type ScopeRowView,
  type SessionRowView,
  type TailRowView,
} from "../gen/remoteagent/core/v1/home_view_pb";
import { t } from "../i18n";
import { Check, ChevronDown, ChevronRight, Clock, MessageCircle, Plus, Search, Settings, X } from "../icons";
import { useFresh } from "../motion";
import { type Remote } from "../remote";
import { Avatar, Banner, ConnBar, GlassButton, PathText, Segmented, Sheet, Skeleton, StateMark, useRemote, type Mark } from "../ui";
import { applyUpdate, useUpdate } from "../update";
import { ApprovalCard } from "./ApprovalCard";
import { ConnCheck } from "./ConnCheck";
import { DiskReminders } from "./DiskReminders";
import { NewBox, NewFlow } from "./NewSession";
import { PrefsBanner, RemovedNotice } from "./Projects";
import { SchedulesPage } from "./Schedules";
import { useDerived } from "./actions";
import { RecoveryNotice } from "./authority-notice";

export type { Go } from "../core/host";

const SESSION_MARKS: Partial<Record<SessionDisplayStatus, Mark>> = {
  [SessionDisplayStatus.STARTING]: "run",
  [SessionDisplayStatus.RUNNING]: "run",
  [SessionDisplayStatus.APPROVAL]: "wait",
  [SessionDisplayStatus.QUESTION]: "wait",
  [SessionDisplayStatus.PLAN]: "wait",
  [SessionDisplayStatus.POPUP]: "wait",
  [SessionDisplayStatus.IDLE]: "idle",
};

function connMark(c?: ConnectionStatusView): Mark {
  if (!c || c.state === ConnState.CONNECTED) return "online";
  return c.state === ConnState.RECONNECTING || c.state === ConnState.CONNECTING ? "run" : "off";
}

function highlightTitle(text: string, n: number): ReactNode {
  const s = n > 0 ? String(n) : "";
  const idx = s ? text.indexOf(s) : -1;
  if (idx < 0) return text;
  return <>{text.slice(0, idx)}<em>{s}</em>{text.slice(idx + s.length)}</>;
}

function UpdateBanner() {
  return useUpdate() === "ready" ? <Banner action={t("common.reload")} onAction={applyUpdate}>{t("home.update_ready")}</Banner> : null;
}

function ListError({ remote }: { remote: Remote }) {
  return remote.listError ? <Banner kind="err" action={t("common.retry")} onAction={() => remote.retryList()}>{remote.listError}</Banner> : null;
}

function HomeBanners({ remote, go, quiet }: { remote: Remote; go: Go; quiet?: boolean }) {
  return (
    <div className="home-banners">
      <UpdateBanner />
      {remote.clockWarning && <Banner kind="warn">{remote.clockWarning}</Banner>}
      <RecoveryNotice remote={remote} go={go} />
      <DiskReminders remote={remote} go={go} />
      <ListError remote={remote} />
      <PrefsBanner remote={remote} quiet={quiet} />
    </div>
  );
}

function suppressWaitingHeadline(remote?: Remote, vm?: HomeHeadlineView): boolean {
  if (!remote || !vm || remote.conn === "online" || remote.link !== "waiting" || remote.showWaiting) return false;
  return vm.title?.key === "connection.connecting" || vm.title?.key === "connection.reconnecting";
}

function Headline({ vm, onDetails }: { vm?: HomeHeadlineView; onDetails?: () => void }) {
  if (!vm) return null;
  const subText = renderText(vm.sub);
  return (
    <>
      <h1 className="hl">{highlightTitle(renderText(vm.title), vm.highlightCount)}</h1>
      {(subText || vm.detailsAction) && (
        <div className={"hl-sub" + (vm.subError ? " err" : "") + (vm.subPath ? " path" : "")}>
          {vm.subPath && subText ? <PathText path={subText} /> : subText}
          {vm.detailsAction && onDetails && <button type="button" onClick={onDetails}>{t("common.details")}</button>}
        </div>
      )}
    </>
  );
}

function HomeEmpty({ empty, onAddDevice, onStartFirst, onRetry }: { empty?: HomeEmptyView; onAddDevice: () => void; onStartFirst: (machineId: string) => void; onRetry: () => void }) {
  if (!empty || empty.kind === HomeEmptyKind.UNSPECIFIED || empty.kind === HomeEmptyKind.NONE) return null;
  const msg = renderText(empty.message);
  return (
    <div className="home-note">
      {msg && <div>{msg}</div>}
      {empty.canAddDevice && <button type="button" className="btn pri" onClick={onAddDevice}>{t("home.add_device")}</button>}
      {empty.canStartFirst && <button type="button" className="btn pri" onClick={() => onStartFirst(empty.readyMachineId)}>{t("home.start_first")}</button>}
      {empty.canRetry && <button type="button" className="btn pri" onClick={onRetry}>{t("common.retry")}</button>}
    </div>
  );
}

function TopApproval({ card, remote, onOpen }: { card: AttentionCardView; remote?: Remote; onOpen: () => void }) {
  if (remote) return <TopApprovalLive card={card} remote={remote} onOpen={onOpen} />;
  return <TopApprovalPreview card={card} onOpen={onOpen} />;
}

function TopApprovalLive({ card, remote, onOpen }: { card: AttentionCardView; remote: Remote; onOpen: () => void }) {
  const derived = useDerived(remote, card.sessionId);
  const live = derived.approvals.get(card.approvalId) ?? [...derived.approvals.values()][0];
  if (!live) return <TopApprovalPreview card={card} onOpen={onOpen} />;
  return <ApprovalCard key={live.approvalId} remote={remote} sid={card.sessionId} a={live} title={renderText(card.title)} where={renderText(card.where)} onOpen={onOpen} timeoutNote />;
}

function TopApprovalPreview({ card, onOpen }: { card: AttentionCardView; onOpen: () => void }) {
  const timeText = renderText(card.time);
  const where = renderText(card.where);
  const alwaysLabel = renderText(card.alwaysLabel);
  return (
    <div className="tc">
      <div className="k"><i />{renderText(card.kindText)}{timeText && <span className={card.hot ? "hot" : undefined}>{timeText}</span>}</div>
      <h4><button type="button" onClick={onOpen}><span>{renderText(card.title)}</span><ChevronRight /></button></h4>
      {where && <div className="m">{where}</div>}
      {card.body && <pre className="cmd clamp">{card.body}</pre>}
      {alwaysLabel && <div className="ml">{alwaysLabel}</div>}
      <div className="btns"><button type="button" className="btn pri" onClick={onOpen}>{t("common.details")}</button></div>
    </div>
  );
}

/** One session row in the running/waiting list, driven by SessionRowView. */
export function SessionRow({ row, onSelect, last, fresh }: { row: SessionRowView; onSelect: () => void; last?: boolean; fresh?: boolean; remote?: Remote }) {
  const timeText = renderText(row.time);
  const second = renderText(row.secondLine) || renderText(row.preview);
  const meta = renderText(row.metaLine);
  return (
    <button type="button" className={"sess" + (last ? " last" : "") + (row.selected ? " sel" : "") + (fresh ? " fresh" : "")} onClick={onSelect}>
      <div className="gut"><StateMark kind={SESSION_MARKS[row.status] ?? "off"} /></div>
      <div className="sm">
        <div className="row1"><b>{renderText(row.title)}</b>{timeText && <span>{timeText}</span>}</div>
        {second && <div className="pv">{second}</div>}
        {(row.multipleAgents || meta) && <div className="mc">{row.multipleAgents && <AgentMark agent={row.agent} />}{meta}</div>}
      </div>
    </button>
  );
}

function EndedGroups({ groups, onOpenSession }: { groups: readonly EndedGroupView[]; onOpenSession: (sessionId: string) => void }) {
  return (
    <>
      {groups.map((g) => (
        <div key={g.groupKey || g.groupKind}>
          <div className="sec d">{renderText(g.label)}</div>
          {g.items.map((row) => (
            <button key={row.sessionId} type="button" className={"old" + (row.selected ? " sel" : "")} onClick={() => onOpenSession(row.sessionId)}>
              <div className="sm"><span>{renderText(row.title)}</span><span>{renderText(row.right)}</span></div>
            </button>
          ))}
        </div>
      ))}
    </>
  );
}

function TailRow({ tail, hasMoreEnded, onLoadMore, onRetry }: { tail?: TailRowView; hasMoreEnded?: boolean; onLoadMore?: () => void; onRetry: () => void }) {
  if (!tail || tail.kind === TailKind.UNSPECIFIED) return null;
  const label = renderText(tail.text);
  if (!label) return null;
  if (onLoadMore && (tail.kind === TailKind.MORE_ON_SERVER || (tail.kind === TailKind.MORE_ENDED && hasMoreEnded))) {
    return <button type="button" className="tail" onClick={onLoadMore}>{label}</button>;
  }
  if (tail.retry) {
    return <div className="tail">{label} <button type="button" onClick={onRetry}>{t("common.retry")}</button></div>;
  }
  return <div className="tail">{label}</div>;
}

function ScopeMenuSheet({ rows, anchor, onSelectScope, onManageProjects, onClose }: { rows: readonly ScopeRowView[]; anchor?: React.RefObject<HTMLElement | null>; onSelectScope: (scopeKey: string) => void; onManageProjects: () => void; onClose: () => void }) {
  return (
    <Sheet onClose={onClose} anchor={anchor} className={anchor ? "with-head" : undefined} title={t("home.choose_scope")}>
      <div className="plist">
        <div className="grp">
          {rows.map((r) => {
            const name = renderText(r.name);
            const sub = renderText(r.subtitle);
            return (
              <button key={r.allProjects ? "__all__" : r.key} type="button" className="pi" onClick={() => { onSelectScope(r.allProjects ? "" : r.key); onClose(); }}>
                <Avatar name={name} all={r.allProjects} />
                <div className="pn"><b>{name}</b>{sub && <small className={r.allOffline ? "off" : undefined}>{sub}</small>}</div>
                {r.checked && <span className="ck"><Check /></span>}
              </button>
            );
          })}
        </div>
        <div className="grp">
          <button type="button" className="pi add" onClick={() => { onClose(); onManageProjects(); }}>{t("home.manage_projects")}<ChevronRight className="chev" /></button>
        </div>
      </div>
    </Sheet>
  );
}

function FilterBar({ query, onChange }: { query: string; onChange: (q: string) => void }) {
  return (
    <div className="find">
      <Search />
      <input value={query} onChange={(e) => onChange(e.target.value)} placeholder={t("home.filter_title")} aria-label={t("home.filter_title")} />
      {query && <button type="button" className="x" aria-label={t("home.clear_filter")} onClick={() => onChange("")}><X /></button>}
    </div>
  );
}

function ScheduleBadges({ attention, hasNew }: { attention: number; hasNew: boolean }) {
  if (attention > 0) return <span className="schedule-attention-badge" aria-label={t("home.ios_badge_tasks", { count: attention })}>{attention > 99 ? "99+" : attention}</span>;
  if (hasNew) return <span className="schedule-new-badge" aria-label={t("home.ios_new_results")} />;
  return null;
}

/** Pure phone Home screen rendered directly from HomeViewModel. */
export function HomeView(p: {
  vm: HomeViewModel;
  remote?: Remote;
  onSelectScope: (scopeKey: string) => void;
  onSetFilter: (query: string) => void;
  onOpenSession: (sessionId: string) => void;
  onOpenSettings: () => void;
  onManageProjects: () => void;
  onAddDevice: () => void;
  onStart: (machineId?: string) => void;
  onLoadMoreEnded: () => void;
  onRetryLoad: () => void;
  onOpenDetails: () => void;
  banners?: ReactNode;
  connBar?: ReactNode;
  bottomBar?: ReactNode;
}) {
  const { vm, remote } = p;
  const [scopeOpen, setScopeOpen] = useState(false);
  const [allTodo, setAllTodo] = useState(false);
  const [allLive, setAllLive] = useState(false);
  const scopeTitle = renderText(vm.scopeTitle) || t("home.all_projects");
  const isAllScope = !vm.scopeKey;
  const filtering = Boolean(vm.filterQuery);
  const card = vm.topAttentionCard;
  const showTopCard = Boolean(card && card.sessionId && !filtering);
  const restAttention = showTopCard ? vm.attention.filter((a) => a.sessionId !== card!.sessionId) : vm.attention;
  const shownAttention = allTodo || filtering ? restAttention : restAttention.slice(0, 3);
  const hiddenAttention = restAttention.length - shownAttention.length;
  const selectedRunIndex = vm.running.findIndex((r) => r.selected);
  const foldRunningAt = selectedRunIndex >= 5 ? selectedRunIndex + 1 : 5;
  const shownRunning = allLive || filtering ? vm.running : vm.running.slice(0, foldRunningAt);
  const hiddenRunning = vm.running.length - shownRunning.length;
  const fresh = useFresh(shownRunning.map((r) => r.sessionId), vm.headline?.kind !== HeadlineKind.UNSPECIFIED);

  return (
    <div className="page">
      <div className="body under-bar">
        <div className="tbar">
          {vm.scopeSelectable ? (
            <button type="button" className="gcap glass scope" aria-label={t("home.scope_label", { name: scopeTitle })} onClick={() => setScopeOpen(true)}>
              <Avatar name={scopeTitle} size="xs" all={isAllScope} /><b>{scopeTitle}</b><ChevronDown />
            </button>
          ) : (
            <div className="gcap glass scope"><Avatar name={scopeTitle} size="xs" all={isAllScope} /><b>{scopeTitle}</b></div>
          )}
          <div className="sp" />
          <GlassButton label={t("common.settings")} onClick={p.onOpenSettings}><Settings /></GlassButton>
        </div>
        {p.connBar}
        <Headline vm={suppressWaitingHeadline(remote, vm.headline) ? undefined : vm.headline} onDetails={p.onOpenDetails} />
        {p.banners}
        <HomeEmpty empty={vm.empty} onAddDevice={p.onAddDevice} onStartFirst={p.onStart} onRetry={p.onRetryLoad} />
        {vm.canFilter && <FilterBar query={vm.filterQuery} onChange={p.onSetFilter} />}
        {showTopCard && card && <TopApproval card={card} remote={remote} onOpen={() => p.onOpenSession(card.sessionId)} />}
        {shownAttention.length > 0 && (
          <div className="pl">
            {shownAttention.map((row) => {
              const summary = renderText(row.summary);
              return (
                <button key={row.sessionId} type="button" className="pr" onClick={() => p.onOpenSession(row.sessionId)}>
                  <StateMark kind="wait" /><div className="nb"><b>{renderText(row.title)}</b>{summary && <span>{summary}</span>}</div><ChevronRight className="chev" />
                </button>
              );
            })}
            {hiddenAttention > 0 && <button type="button" className="pr all" onClick={() => setAllTodo(true)}>{t("home.more_waiting", { count: hiddenAttention })}</button>}
          </div>
        )}
        {shownRunning.length > 0 && (
          <>
            <div className="sec">{t("home.live")}<span>{vm.running.length}</span></div>
            {shownRunning.map((row, idx) => (
              <SessionRow key={row.sessionId} row={row} remote={remote} last={idx === shownRunning.length - 1 && hiddenRunning === 0} fresh={fresh(row.sessionId)} onSelect={() => p.onOpenSession(row.sessionId)} />
            ))}
            {hiddenRunning > 0 && <button type="button" className="more-row" onClick={() => setAllLive(true)}>{t("home.more_live", { count: hiddenRunning })}<ChevronRight /></button>}
          </>
        )}
        <EndedGroups groups={vm.endedGroups} onOpenSession={p.onOpenSession} />
        <TailRow tail={vm.endedTail} hasMoreEnded={vm.hasMoreEnded} onLoadMore={p.onLoadMoreEnded} onRetry={p.onRetryLoad} />
        {vm.filterSummary && !renderText(vm.endedTail?.text) && <div className="tail">{renderText(vm.filterSummary)}</div>}
      </div>
      <div className="edge-b" />
      {p.bottomBar}
      {scopeOpen && <ScopeMenuSheet rows={vm.scopeMenu} onSelectScope={p.onSelectScope} onManageProjects={p.onManageProjects} onClose={() => setScopeOpen(false)} />}
    </div>
  );
}

/** Connected phone Home screen wired to CoreHost. */
export function Home({ remote, go }: { remote: Remote; go: Go }) {
  useRemote(remote);
  const host = useCoreHost(remote, go);
  const [endedLimit, setEndedLimit] = useState(30);
  const vm = useHomeView(host, endedLimit);
  const [connOpen, setConnOpen] = useState(false);
  const [picking, setPicking] = useState(false);

  const heldEnded = vm.endedGroups.reduce((n, g) => n + g.items.length, 0);
  useEffect(() => {
    if (!vm.filterQuery && remote.moreSessions && (vm.endedTail?.kind === TailKind.MORE_ENDED || vm.endedTail?.kind === TailKind.MORE_ON_SERVER) && heldEnded < endedLimit) {
      void remote.loadMoreSessions();
    }
  }, [vm.filterQuery, vm.endedTail?.kind, remote, remote.moreSessions, heldEnded, endedLimit]);

  const startTask = () => {
    if (vm.scopeKey) { remote.focusProject = vm.scopeKey; go("p/" + encodeURIComponent(vm.scopeKey)); }
    else setPicking(true);
  };

  return (
    <>
      <HomeView
        vm={vm}
        remote={remote}
        onSelectScope={host.homeSetScope}
        onSetFilter={host.homeSetFilter}
        onOpenSession={(sid) => go("s/" + encodeURIComponent(sid))}
        onOpenSettings={() => go("settings")}
        onManageProjects={() => go("projects")}
        onAddDevice={() => go("devices")}
        onStart={startTask}
        onLoadMoreEnded={() => { setEndedLimit((n) => n + 30); host.homeLoadMoreEnded(30); }}
        onRetryLoad={() => { host.homeRetryLoad(); remote.retryList(); }}
        onOpenDetails={() => setConnOpen(true)}
        connBar={<ConnBar remote={remote} />}
        banners={<HomeBanners remote={remote} go={go} />}
        bottomBar={<BottomBar remote={remote} active="sessions" waiting={vm.waitingTotal} canStart={vm.canStart && vm.connection?.state === ConnState.CONNECTED} onStart={startTask} go={go} />}
      />
      {picking && <NewFlow remote={remote} go={go} onClose={() => setPicking(false)} />}
      {connOpen && <ConnCheck remote={remote} onClose={() => setConnOpen(false)} />}
    </>
  );
}

/** Pure desktop Sidebar view driven by HomeViewModel. */
export function SidebarView(p: {
  vm: HomeViewModel;
  remote?: Remote;
  selected?: string;
  tasks?: boolean;
  scheduleSidebar?: ReactNode;
  scheduleAttention?: number;
  scheduleNew?: boolean;
  settingsActive?: boolean;
  onSelectScope: (scopeKey: string) => void;
  onSetFilter: (query: string) => void;
  onSelectSession: (sessionId: string) => void;
  onNewTask: () => void;
  onNewSchedule?: () => void;
  onToggleTasks: (showTasks: boolean) => void;
  onOpenSettings: () => void;
  onManageProjects: () => void;
  onOpenConnCheck: () => void;
  onLoadMoreEnded: () => void;
  onRetryLoad: () => void;
  banners?: ReactNode;
}) {
  const { vm, remote, selected, tasks = false } = p;
  const [scopeOpen, setScopeOpen] = useState(false);
  const scopeBtn = useRef<HTMLButtonElement>(null);
  const scopeTitle = renderText(vm.scopeTitle) || t("home.all_projects");
  const isAllScope = !vm.scopeKey;
  const connText = renderText(vm.connection?.badgeText);
  const fresh = useFresh(vm.running.map((r) => r.sessionId), vm.headline?.kind !== HeadlineKind.UNSPECIFIED);

  return (
    <aside className="side">
      <div className="side-top">
        <button ref={scopeBtn} type="button" className={"scp" + (scopeOpen ? " open" : "")} disabled={!vm.scopeSelectable} aria-label={t("home.scope_label", { name: scopeTitle })} onClick={() => vm.scopeSelectable && setScopeOpen((o) => !o)}>
          <Avatar name={scopeTitle} size="xs" all={isAllScope} /><b>{scopeTitle}</b><ChevronDown />
        </button>
        <div className="sp" />
        {tasks ? (
          <button type="button" className="mini" onClick={p.onNewSchedule ?? p.onNewTask}>{t("home.new_schedule")}</button>
        ) : (
          <button type="button" className="ib" aria-label={t("home.new_task")} disabled={!vm.canStart} onClick={p.onNewTask}><Plus /></button>
        )}
      </div>
      {!tasks && vm.canFilter && <FilterBar query={vm.filterQuery} onChange={p.onSetFilter} />}
      {tasks && p.scheduleSidebar ? p.scheduleSidebar : (
        <div className="scroll">
          {p.banners}
          {vm.noMatches && <p className="empty">{renderText(vm.filterSummary) || t("home.no_matches", { query: vm.filterQuery })}</p>}
          {vm.attention.length > 0 && (
            <>
              <div className="sec">{t("home.pending_label", { count: vm.attention.length })}</div>
              {vm.attention.map((row) => (
                <button key={row.sessionId} type="button" className={"sess" + (selected === row.sessionId ? " sel" : "")} onClick={() => p.onSelectSession(row.sessionId)}>
                  <div className="gut"><StateMark kind="wait" /></div>
                  <div className="sm"><div className="row1"><b>{renderText(row.title)}</b></div>{row.summary && <div className="pv">{renderText(row.summary)}</div>}</div>
                </button>
              ))}
            </>
          )}
          {vm.running.length > 0 && (
            <>
              <div className="sec">{t("home.live")}</div>
              {vm.running.map((row, idx) => (
                <SessionRow key={row.sessionId} row={selected && row.sessionId === selected ? { ...row, selected: true } : row} remote={remote} last={idx === vm.running.length - 1} fresh={fresh(row.sessionId)} onSelect={() => p.onSelectSession(row.sessionId)} />
              ))}
            </>
          )}
          {!vm.filterQuery && vm.attention.length === 0 && vm.running.length === 0 && vm.endedGroups.length === 0 && vm.empty?.message && <div className="home-note">{renderText(vm.empty.message)}</div>}
          <EndedGroups groups={vm.endedGroups} onOpenSession={p.onSelectSession} />
          <TailRow tail={vm.endedTail} onLoadMore={p.onLoadMoreEnded} onRetry={p.onRetryLoad} />
        </div>
      )}
      <button type="button" className="conn" aria-label={connText ? t("home.connection_label", { state: connText }) : t("home.connection_check")} title={t("home.connection_check")} onClick={p.onOpenConnCheck}>
        <StateMark kind={connMark(vm.connection)} /><span>{connText}</span>
      </button>
      <div className="sfoot">
        <Segmented label={t("home.sessions_or_schedules")} value={tasks ? "tasks" : "sessions"} onChange={(v) => p.onToggleTasks(v === "tasks")} options={[{ value: "sessions", label: t("home.sessions_tab") }, { value: "tasks", label: <>{t("home.schedules_tab")}<ScheduleBadges attention={p.scheduleAttention ?? 0} hasNew={!!p.scheduleNew} /></> }]} />
        <button type="button" className={"ib" + (p.settingsActive ? " on" : "")} aria-label={t("common.settings")} onClick={p.onOpenSettings}><Settings /></button>
      </div>
      {scopeOpen && <ScopeMenuSheet rows={vm.scopeMenu} anchor={scopeBtn} onSelectScope={p.onSelectScope} onManageProjects={p.onManageProjects} onClose={() => setScopeOpen(false)} />}
    </aside>
  );
}

/** Connected wide sidebar backed by CoreHost. */
export function Sidebar({ remote, selected, project, active = "sessions", tasks, selectedTask, go, settingsActive }: { remote: Remote; selected?: string; project?: string; active?: "sessions" | "tasks" | "settings"; tasks?: boolean; selectedTask?: string; go: Go; settingsActive?: boolean }) {
  useRemote(remote);
  const host = useCoreHost(remote, go);
  const [endedLimit, setEndedLimit] = useState(30);
  const vm = useHomeView(host, endedLimit);
  const [connOpen, setConnOpen] = useState(false);
  const showTasks = tasks ?? active === "tasks";

  useEffect(() => { host.homeSelectSession(selected ?? ""); }, [host, selected]);

  // Don't render stale "已连接" while reconnecting during grace window.
  const connView = remote.conn !== "online" && !remote.showWaiting ? (vm.connection ? { ...vm.connection, badgeText: undefined } : undefined) : vm.connection;
  const viewVm = connView === vm.connection ? vm : { ...vm, connection: connView };
  const activeScope = project ?? vm.scopeKey;

  return (
    <>
      <SidebarView
        vm={viewVm}
        remote={remote}
        selected={selected}
        tasks={showTasks}
        scheduleSidebar={showTasks ? <SchedulesPage remote={remote} go={go} sidebar selected={selectedTask} /> : undefined}
        scheduleAttention={remote.scheduleBadge()}
        scheduleNew={remote.scheduleNewResults()}
        settingsActive={settingsActive ?? active === "settings"}
        onSelectScope={(scopeKey) => { host.homeSetScope(scopeKey); if (project !== undefined) go(""); }}
        onSetFilter={host.homeSetFilter}
        onSelectSession={(sid) => go("s/" + encodeURIComponent(sid))}
        onNewTask={() => go(activeScope ? "p/" + encodeURIComponent(activeScope) : "")}
        onNewSchedule={() => go("task-new")}
        onToggleTasks={(on) => go(on ? "tasks" : "")}
        onOpenSettings={() => go("settings")}
        onManageProjects={() => go("projects")}
        onOpenConnCheck={() => setConnOpen(true)}
        onLoadMoreEnded={() => { setEndedLimit((n) => n + 30); host.homeLoadMoreEnded(30); }}
        onRetryLoad={() => { host.homeRetryLoad(); remote.retryList(); }}
        banners={<HomeBanners remote={remote} go={go} quiet />}
      />
      {connOpen && <ConnCheck remote={remote} onClose={() => setConnOpen(false)} />}
    </>
  );
}

/** Wide main area: start a new session directly (for all projects or a picked project). */
export function WideHome({ remote, go, project }: { remote: Remote; go: Go; project?: string }) {
  useRemote(remote);
  const host = useCoreHost(remote, go);
  const homeVm = useHomeView(host, 1);
  const scopedProject = project || homeVm.scopeKey;
  const [draftKey, setDraftKey] = useState("");
  const projectKey = scopedProject || draftKey;
  const newVm = useNewSessionView(host, projectKey);
  const hiddenNotice = useProjectView(host, projectKey, false).detail?.hiddenNotice;
  const [picking, setPicking] = useState(false);

  useEffect(() => {
    if (draftKey && (scopedProject || (!newVm.drafting && !hiddenNotice?.active))) setDraftKey("");
  }, [draftKey, scopedProject, newVm.drafting, hiddenNotice?.active]);

  if (!remote.listed) {
    return <div className="page wnew"><div className="wstart">{remote.listError ? <ListError remote={remote} /> : <Skeleton />}</div></div>;
  }
  if (!homeVm.canStart) {
    return (
      <div className="page wnew">
        <div className="wstart">
          <Headline vm={suppressWaitingHeadline(remote, homeVm.headline) ? undefined : homeVm.headline} />
          <div className="home-banners"><ListError remote={remote} /><PrefsBanner remote={remote} /></div>
          <HomeEmpty empty={homeVm.empty} onAddDevice={() => go("devices")} onStartFirst={() => {}} onRetry={() => { host.homeRetryLoad(); remote.retryList(); }} />
        </div>
      </div>
    );
  }
  return (
    <div className="page wnew">
      <NewBox
        remote={remote}
        projectKey={projectKey}
        go={go}
        variant="start"
        fixed={Boolean(scopedProject)}
        onPickProject={scopedProject ? undefined : () => setPicking(true)}
        notice={hiddenNotice?.active ? <RemovedNotice notice={hiddenNotice} onReadd={() => host.projectToggleHide(projectKey)} /> : undefined}
        onDraft={() => { if (!scopedProject && newVm.projectKey) setDraftKey(newVm.projectKey); }}
      />
      {picking && <NewFlow remote={remote} go={go} onClose={() => setPicking(false)} onPicked={(key) => { setPicking(false); host.homeSetScope(key); }} />}
    </div>
  );
}

export interface BottomBarAction { label: string; icon: ReactNode; onClick: () => void; disabled?: boolean; }

function BottomBarInner(p: { remote?: Remote; active: "sessions" | "tasks"; action?: BottomBarAction; waiting: number; canStart: boolean; onStart?: () => void; go: Go }) {
  const [picking, setPicking] = useState(false);
  return (
    <>
      <div className="bbar">
        <div className="tabs glass">
          <button type="button" className={p.active === "sessions" ? "on" : undefined} onClick={() => p.go("")}>
            <MessageCircle />{t("home.sessions_tab")}{p.waiting > 0 && <span className="count">{p.waiting > 99 ? "99+" : p.waiting}</span>}
          </button>
          <button type="button" className={p.active === "tasks" ? "on" : undefined} onClick={() => p.go("tasks")}>
            <Clock />{t("home.schedules_tab")}<ScheduleBadges attention={p.remote?.scheduleBadge() ?? 0} hasNew={p.remote?.scheduleNewResults() ?? false} />
          </button>
        </div>
        <button type="button" className="fab" disabled={p.action ? p.action.disabled : !p.canStart} onClick={p.action ? p.action.onClick : () => p.onStart ? p.onStart() : p.remote && setPicking(true)}>
          {p.action ? p.action.icon : <Plus />}{p.action ? p.action.label : t("home.new_task")}
        </button>
      </div>
      {picking && p.remote && <NewFlow remote={p.remote} go={p.go} onClose={() => setPicking(false)} />}
    </>
  );
}

function BottomBarConnected(p: { remote: Remote; active: "sessions" | "tasks"; action?: BottomBarAction; waiting?: number; canStart?: boolean; onStart?: () => void; go: Go }) {
  const vm = useHomeView(useCoreHost(p.remote, p.go), 1);
  return <BottomBarInner {...p} waiting={p.waiting ?? vm.waitingTotal} canStart={p.canStart ?? (vm.canStart && vm.connection?.state === ConnState.CONNECTED)} />;
}

/** Floating bottom tabs + primary pill on phone. */
export function BottomBar(props: { remote?: Remote; active: "sessions" | "tasks"; action?: BottomBarAction; waiting?: number; canStart?: boolean; onStart?: () => void; go: Go }) {
  return props.remote ? <BottomBarConnected {...props} remote={props.remote} /> : <BottomBarInner {...props} waiting={props.waiting ?? 0} canStart={props.canStart ?? false} />;
}
