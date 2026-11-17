// The home screen: scope, headline, what waits, what runs, what ended,
// sidebar, and wide main area — all rendered straight from HomeViewModel.

import { create } from "@bufbuild/protobuf";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { AgentMark } from "../agent-mark";
import {
  useCoreHost,
  useHomeView,
  useNewSessionView,
  useProjectView,
  type Go,
} from "../core/host";
import { renderText } from "../core/text";
import {
  EventSchema,
  HomeLoadMoreEndedActionSchema,
  HomeSelectSessionActionSchema,
  HomeSetFilterActionSchema,
  HomeSetScopeActionSchema,
  ProjectKeyActionSchema,
} from "../gen/remoteagent/core/v1/app_pb";
import {
  ConnState,
  HeadlineKind,
  HomeEmptyKind,
  SessionDisplayStatus,
  TailKind,
  type AttentionCardView,
  type AttentionRowView,
  type ConnectionStatusView,
  type EndedGroupView,
  type EndedRowView,
  type HomeEmptyView,
  type HomeHeadlineView,
  type HomeViewModel,
  type ScopeRowView,
  type SessionRowView,
  type TailRowView,
} from "../gen/remoteagent/core/v1/home_view_pb";
import { t } from "../i18n";
import {
  Check,
  ChevronDown,
  ChevronRight,
  Clock,
  MessageCircle,
  Plus,
  Search,
  Settings,
  X,
} from "../icons";
import { useFresh } from "../motion";
import { type Remote } from "../remote";
import {
  Avatar,
  Banner,
  ConnBar,
  GlassButton,
  PathText,
  Segmented,
  Sheet,
  Skeleton,
  StateMark,
  useRemote,
  type Mark,
} from "../ui";
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

function markOfSession(status: SessionDisplayStatus): Mark {
  switch (status) {
    case SessionDisplayStatus.STARTING:
    case SessionDisplayStatus.RUNNING:
      return "run";
    case SessionDisplayStatus.APPROVAL:
    case SessionDisplayStatus.QUESTION:
    case SessionDisplayStatus.PLAN:
    case SessionDisplayStatus.POPUP:
      return "wait";
    case SessionDisplayStatus.IDLE:
      return "idle";
    case SessionDisplayStatus.OFFLINE:
    case SessionDisplayStatus.ENDED:
    case SessionDisplayStatus.UNLINKED:
    default:
      return "off";
  }
}

function connMark(c?: ConnectionStatusView): Mark {
  if (!c) return "online";
  switch (c.state) {
    case ConnState.RECONNECTING:
    case ConnState.CONNECTING:
      return "run";
    case ConnState.OFFLINE:
    case ConnState.NO_NETWORK:
      return "off";
    case ConnState.CONNECTED:
    default:
      return "online";
  }
}

function highlightTitle(text: string, n: number): ReactNode {
  if (n <= 0) return text;
  const s = String(n);
  const idx = text.indexOf(s);
  if (idx < 0) return text;
  return (
    <>
      {text.slice(0, idx)}
      <em>{s}</em>
      {text.slice(idx + s.length)}
    </>
  );
}

function ClockWarning({ remote }: { remote: Remote }) {
  if (!remote.clockWarning) return null;
  return <Banner kind="warn">{remote.clockWarning}</Banner>;
}

function UpdateBanner() {
  const u = useUpdate();
  if (u !== "ready") return null;
  return (
    <Banner action={t("common.reload")} onAction={applyUpdate}>
      {t("home.update_ready")}
    </Banner>
  );
}

function ListError({ remote }: { remote: Remote }) {
  if (!remote.listError) return null;
  return (
    <Banner kind="err" action={t("common.retry")} onAction={() => remote.retryList()}>
      {remote.listError}
    </Banner>
  );
}

function suppressWaitingHeadline(remote?: Remote, vm?: HomeHeadlineView): boolean {
  if (!remote || !vm || remote.conn === "online" || remote.link !== "waiting" || remote.showWaiting) return false;
  return vm.title?.key === "connection.connecting" || vm.title?.key === "connection.reconnecting";
}

function Headline({
  vm,
  onDetails,
}: {
  vm?: HomeHeadlineView;
  onDetails?: () => void;
}) {
  if (!vm) return null;
  const titleText = renderText(vm.title);
  const subText = renderText(vm.sub);
  return (
    <>
      <h1 className="hl">{highlightTitle(titleText, vm.highlightCount)}</h1>
      {(subText || vm.detailsAction) && (
        <div
          className={
            "hl-sub" +
            (vm.subError ? " err" : "") +
            (vm.subPath ? " path" : "")
          }
        >
          {vm.subPath && subText ? <PathText path={subText} /> : subText}
          {vm.detailsAction && onDetails && (
            <button type="button" onClick={onDetails}>
              {t("common.details")}
            </button>
          )}
        </div>
      )}
    </>
  );
}

function HomeEmpty({
  empty,
  onAddDevice,
  onStartFirst,
  onRetry,
}: {
  empty?: HomeEmptyView;
  onAddDevice: () => void;
  onStartFirst: (machineId: string) => void;
  onRetry: () => void;
}) {
  if (!empty || empty.kind === HomeEmptyKind.UNSPECIFIED || empty.kind === HomeEmptyKind.NONE) {
    return null;
  }
  const msg = renderText(empty.message);
  return (
    <div className="home-note">
      {msg && <div>{msg}</div>}
      {empty.canAddDevice && (
        <button type="button" className="btn pri" onClick={onAddDevice}>
          {t("home.add_device")}
        </button>
      )}
      {empty.canStartFirst && (
        <button
          type="button"
          className="btn pri"
          onClick={() => onStartFirst(empty.readyMachineId)}
        >
          {t("home.start_first")}
        </button>
      )}
      {empty.canRetry && (
        <button type="button" className="btn pri" onClick={onRetry}>
          {t("common.retry")}
        </button>
      )}
    </div>
  );
}

function AttentionRow({
  row,
  onOpen,
}: {
  row: AttentionRowView;
  remote?: Remote;
  onOpen: () => void;
}) {
  const summary = renderText(row.summary);
  return (
    <button type="button" className="pr" onClick={onOpen}>
      <StateMark kind="wait" />
      <div className="nb">
        <b>{renderText(row.title)}</b>
        {summary && <span>{summary}</span>}
      </div>
      <ChevronRight className="chev" />
    </button>
  );
}

function TopApprovalLive({
  card,
  remote,
  onOpen,
}: {
  card: AttentionCardView;
  remote: Remote;
  onOpen: () => void;
}) {
  const derived = useDerived(remote, card.sessionId);
  const live = derived.approvals.get(card.approvalId) ?? [...derived.approvals.values()][0];
  if (live) {
    const title = renderText(card.title);
    const where = renderText(card.where);
    return (
      <ApprovalCard
        key={live.approvalId}
        remote={remote}
        sid={card.sessionId}
        a={live}
        title={title}
        where={where}
        onOpen={onOpen}
        timeoutNote
      />
    );
  }
  return <TopApprovalPreview card={card} onOpen={onOpen} />;
}

function TopApprovalPreview({
  card,
  onOpen,
}: {
  card: AttentionCardView;
  onOpen: () => void;
}) {
  const kindText = renderText(card.kindText);
  const timeText = renderText(card.time);
  const where = renderText(card.where);
  const alwaysLabel = renderText(card.alwaysLabel);
  return (
    <div className="tc">
      <div className="k">
        <i />
        {kindText}
        {timeText && <span className={card.hot ? "hot" : undefined}>{timeText}</span>}
      </div>
      <h4>
        <button type="button" onClick={onOpen}>
          <span>{renderText(card.title)}</span>
          <ChevronRight />
        </button>
      </h4>
      {where && <div className="m">{where}</div>}
      {card.body && <pre className="cmd clamp">{card.body}</pre>}
      {alwaysLabel && <div className="ml">{alwaysLabel}</div>}
      <div className="btns">
        <button type="button" className="btn pri" onClick={onOpen}>
          {t("common.details")}
        </button>
      </div>
    </div>
  );
}

function TopApproval({
  card,
  remote,
  onOpen,
}: {
  card: AttentionCardView;
  remote?: Remote;
  onOpen: () => void;
}) {
  if (remote) {
    return <TopApprovalLive card={card} remote={remote} onOpen={onOpen} />;
  }
  return <TopApprovalPreview card={card} onOpen={onOpen} />;
}

/** One session row in the running/waiting list, driven by SessionRowView. */
export function SessionRow({
  row,
  onSelect,
  last,
  fresh,
}: {
  row: SessionRowView;
  onSelect: () => void;
  last?: boolean;
  fresh?: boolean;
  remote?: Remote;
}) {
  const timeText = renderText(row.time);
  const second = renderText(row.secondLine) || renderText(row.preview);
  const meta = renderText(row.metaLine);
  return (
    <button
      type="button"
      className={
        "sess" +
        (last ? " last" : "") +
        (row.selected ? " sel" : "") +
        (fresh ? " fresh" : "")
      }
      onClick={onSelect}
    >
      <div className="gut">
        <StateMark kind={markOfSession(row.status)} />
      </div>
      <div className="sm">
        <div className="row1">
          <b>{renderText(row.title)}</b>
          {timeText && <span>{timeText}</span>}
        </div>
        {second && <div className="pv">{second}</div>}
        {(row.multipleAgents || meta) && (
          <div className="mc">
            {row.multipleAgents && <AgentMark agent={row.agent} />}
            {meta}
          </div>
        )}
      </div>
    </button>
  );
}

function EndedRow({
  row,
  onSelect,
}: {
  row: EndedRowView;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      className={"old" + (row.selected ? " sel" : "")}
      onClick={onSelect}
    >
      <div className="sm">
        <span>{renderText(row.title)}</span>
        <span>{renderText(row.right)}</span>
      </div>
    </button>
  );
}

function EndedGroups({
  groups,
  onOpenSession,
}: {
  groups: readonly EndedGroupView[];
  onOpenSession: (sessionId: string) => void;
}) {
  return (
    <>
      {groups.map((g) => (
        <div key={g.groupKey || g.groupKind}>
          <div className="sec d">{renderText(g.label)}</div>
          {g.items.map((row) => (
            <EndedRow
              key={row.sessionId}
              row={row}
              onSelect={() => onOpenSession(row.sessionId)}
            />
          ))}
        </div>
      ))}
    </>
  );
}

function TailRow({
  tail,
  hasMoreEnded,
  onLoadMore,
  onRetry,
}: {
  tail?: TailRowView;
  hasMoreEnded?: boolean;
  onLoadMore?: () => void;
  onRetry: () => void;
}) {
  if (!tail || tail.kind === TailKind.UNSPECIFIED) return null;
  const label = renderText(tail.text);
  if (!label) return null;
  if (
    onLoadMore &&
    (tail.kind === TailKind.MORE_ON_SERVER ||
      (tail.kind === TailKind.MORE_ENDED && hasMoreEnded))
  ) {
    return (
      <button type="button" className="tail" onClick={onLoadMore}>
        {label}
      </button>
    );
  }
  if (tail.retry) {
    return (
      <div className="tail">
        {label}{" "}
        <button type="button" onClick={onRetry}>
          {t("common.retry")}
        </button>
      </div>
    );
  }
  return <div className="tail">{label}</div>;
}

function ScopeMenuSheet({
  rows,
  anchor,
  onSelectScope,
  onManageProjects,
  onClose,
}: {
  rows: readonly ScopeRowView[];
  anchor?: React.RefObject<HTMLElement | null>;
  onSelectScope: (scopeKey: string) => void;
  onManageProjects: () => void;
  onClose: () => void;
}) {
  return (
    <Sheet
      onClose={onClose}
      anchor={anchor}
      className={anchor ? "with-head" : undefined}
      title={t("home.choose_scope")}
    >
      <div className="plist">
        <div className="grp">
          {rows.map((r) => {
            const name = renderText(r.name);
            const sub = renderText(r.subtitle);
            return (
              <button
                key={r.allProjects ? "__all__" : r.key}
                type="button"
                className="pi"
                onClick={() => {
                  onSelectScope(r.allProjects ? "" : r.key);
                  onClose();
                }}
              >
                <Avatar name={name} all={r.allProjects} />
                <div className="pn">
                  <b>{name}</b>
                  {sub && (
                    <small className={r.allOffline ? "off" : undefined}>
                      {sub}
                    </small>
                  )}
                </div>
                {r.checked && (
                  <span className="ck">
                    <Check />
                  </span>
                )}
              </button>
            );
          })}
        </div>
        <div className="grp">
          <button
            type="button"
            className="pi add"
            onClick={() => {
              onClose();
              onManageProjects();
            }}
          >
            {t("home.manage_projects")}
            <ChevronRight className="chev" />
          </button>
        </div>
      </div>
    </Sheet>
  );
}

function FilterBar({
  query,
  onChange,
}: {
  query: string;
  onChange: (q: string) => void;
}) {
  return (
    <div className="find">
      <Search />
      <input
        value={query}
        onChange={(e) => onChange(e.target.value)}
        placeholder={t("home.filter_title")}
        aria-label={t("home.filter_title")}
      />
      {query && (
        <button
          type="button"
          className="x"
          aria-label={t("home.clear_filter")}
          onClick={() => onChange("")}
        >
          <X />
        </button>
      )}
    </div>
  );
}

/** Pure phone Home screen rendered directly from HomeViewModel. */
export function HomeView({
  vm,
  remote,
  onSelectScope,
  onSetFilter,
  onOpenSession,
  onOpenSettings,
  onManageProjects,
  onAddDevice,
  onStart,
  onLoadMoreEnded,
  onRetryLoad,
  onOpenDetails,
  banners,
  connBar,
  bottomBar,
}: {
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
  const [scopeOpen, setScopeOpen] = useState(false);
  const [allTodo, setAllTodo] = useState(false);
  const [allLive, setAllLive] = useState(false);

  const scopeTitle = renderText(vm.scopeTitle) || t("home.all_projects");
  const isAllScope = !vm.scopeKey;

  const filtering = Boolean(vm.filterQuery);
  const card = vm.topAttentionCard;
  const showTopCard = Boolean(card && card.sessionId && !filtering);
  const restAttention = showTopCard
    ? vm.attention.filter((a) => a.sessionId !== card!.sessionId)
    : vm.attention;
  const shownAttention =
    allTodo || filtering ? restAttention : restAttention.slice(0, 3);
  const hiddenAttention = restAttention.length - shownAttention.length;

  const selectedRunIndex = vm.running.findIndex((r) => r.selected);
  const foldRunningAt = selectedRunIndex >= 5 ? selectedRunIndex + 1 : 5;
  const shownRunning =
    allLive || filtering
      ? vm.running
      : vm.running.slice(0, foldRunningAt);
  const hiddenRunning = vm.running.length - shownRunning.length;

  const fresh = useFresh(
    shownRunning.map((r) => r.sessionId),
    vm.headline?.kind !== HeadlineKind.UNSPECIFIED,
  );

  return (
    <div className="page">
      <div className="body under-bar">
        <div className="tbar">
          {vm.scopeSelectable ? (
            <button
              type="button"
              className="gcap glass scope"
              aria-label={t("home.scope_label", { name: scopeTitle })}
              onClick={() => setScopeOpen(true)}
            >
              <Avatar name={scopeTitle} size="xs" all={isAllScope} />
              <b>{scopeTitle}</b>
              <ChevronDown />
            </button>
          ) : (
            <div className="gcap glass scope">
              <Avatar name={scopeTitle} size="xs" all={isAllScope} />
              <b>{scopeTitle}</b>
            </div>
          )}
          <div className="sp" />
          <GlassButton label={t("common.settings")} onClick={onOpenSettings}>
            <Settings />
          </GlassButton>
        </div>

        {connBar}

        <Headline vm={suppressWaitingHeadline(remote, vm.headline) ? undefined : vm.headline} onDetails={onOpenDetails} />

        <div className="home-banners">{banners}</div>

        <HomeEmpty
          empty={vm.empty}
          onAddDevice={onAddDevice}
          onStartFirst={(machineId) => onStart(machineId)}
          onRetry={onRetryLoad}
        />

        {vm.canFilter && (
          <FilterBar query={vm.filterQuery} onChange={onSetFilter} />
        )}

        {showTopCard && card && (
          <TopApproval
            card={card}
            remote={remote}
            onOpen={() => onOpenSession(card.sessionId)}
          />
        )}

        {shownAttention.length > 0 && (
          <div className="pl">
            {shownAttention.map((row) => (
              <AttentionRow
                key={row.sessionId}
                row={row}
                remote={remote}
                onOpen={() => onOpenSession(row.sessionId)}
              />
            ))}
            {hiddenAttention > 0 && (
              <button
                type="button"
                className="pr all"
                onClick={() => setAllTodo(true)}
              >
                {t("home.more_waiting", { count: hiddenAttention })}
              </button>
            )}
          </div>
        )}

        {shownRunning.length > 0 && (
          <>
            <div className="sec">
              {t("home.live")}
              <span>{vm.running.length}</span>
            </div>
            {shownRunning.map((row, idx) => (
              <SessionRow
                key={row.sessionId}
                row={row}
                remote={remote}
                last={idx === shownRunning.length - 1 && hiddenRunning === 0}
                fresh={fresh(row.sessionId)}
                onSelect={() => onOpenSession(row.sessionId)}
              />
            ))}
            {hiddenRunning > 0 && (
              <button
                type="button"
                className="more-row"
                onClick={() => setAllLive(true)}
              >
                {t("home.more_live", { count: hiddenRunning })}
                <ChevronRight />
              </button>
            )}
          </>
        )}

        <EndedGroups
          groups={vm.endedGroups}
          onOpenSession={onOpenSession}
        />

        <TailRow
          tail={vm.endedTail}
          hasMoreEnded={vm.hasMoreEnded}
          onLoadMore={onLoadMoreEnded}
          onRetry={onRetryLoad}
        />

        {vm.filterSummary && !renderText(vm.endedTail?.text) && (
          <div className="tail">{renderText(vm.filterSummary)}</div>
        )}
      </div>

      <div className="edge-b" />
      {bottomBar}

      {scopeOpen && (
        <ScopeMenuSheet
          rows={vm.scopeMenu}
          onSelectScope={onSelectScope}
          onManageProjects={onManageProjects}
          onClose={() => setScopeOpen(false)}
        />
      )}
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
    if (
      !vm.filterQuery &&
      remote.moreSessions &&
      (vm.endedTail?.kind === TailKind.MORE_ENDED ||
        vm.endedTail?.kind === TailKind.MORE_ON_SERVER) &&
      heldEnded < endedLimit
    ) {
      void remote.loadMoreSessions();
    }
  }, [vm.filterQuery, vm.endedTail?.kind, remote, remote.moreSessions, heldEnded, endedLimit]);

  const startTask = (_machineId?: string) => {
    if (vm.scopeKey) {
      remote.focusProject = vm.scopeKey;
      go("p/" + encodeURIComponent(vm.scopeKey));
    } else {
      setPicking(true);
    }
  };

  return (
    <>
      <HomeView
        vm={vm}
        remote={remote}
        onSelectScope={(scopeKey) =>
          host.dispatch(
            create(EventSchema, {
              event: {
                case: "homeSetScope",
                value: create(HomeSetScopeActionSchema, { scopeKey }),
              },
            }),
          )
        }
        onSetFilter={(query) =>
          host.dispatch(
            create(EventSchema, {
              event: {
                case: "homeSetFilter",
                value: create(HomeSetFilterActionSchema, { query }),
              },
            }),
          )
        }
        onOpenSession={(sid) => go("s/" + encodeURIComponent(sid))}
        onOpenSettings={() => go("settings")}
        onManageProjects={() => go("projects")}
        onAddDevice={() => go("devices")}
        onStart={startTask}
        onLoadMoreEnded={() => {
          setEndedLimit((n) => n + 30);
          host.dispatch(
            create(EventSchema, {
              event: {
                case: "homeLoadMoreEnded",
                value: create(HomeLoadMoreEndedActionSchema, { step: 30 }),
              },
            }),
          );
        }}
        onRetryLoad={() => {
          host.dispatch(
            create(EventSchema, { event: { case: "homeRetryLoad", value: {} } }),
          );
          remote.retryList();
        }}
        onOpenDetails={() => setConnOpen(true)}
        connBar={<ConnBar remote={remote} />}
        banners={
          <>
            <UpdateBanner />
            <ClockWarning remote={remote} />
            <RecoveryNotice remote={remote} go={go} />
            <DiskReminders remote={remote} go={go} />
            <ListError remote={remote} />
            <PrefsBanner remote={remote} />
          </>
        }
        bottomBar={
          <BottomBar
            remote={remote}
            active="sessions"
            waiting={vm.waitingTotal}
            canStart={vm.canStart && vm.connection?.state === ConnState.CONNECTED}
            onStart={() => startTask()}
            go={go}
          />
        }
      />
      {picking && (
        <NewFlow
          remote={remote}
          go={go}
          onClose={() => setPicking(false)}
        />
      )}
      {connOpen && (
        <ConnCheck remote={remote} onClose={() => setConnOpen(false)} />
      )}
    </>
  );
}

/** Pure desktop Sidebar view driven by HomeViewModel. */
export function SidebarView({
  vm,
  remote,
  selected,
  tasks = false,
  scheduleSidebar,
  scheduleAttention = 0,
  scheduleNew = false,
  settingsActive,
  onSelectScope,
  onSetFilter,
  onSelectSession,
  onNewTask,
  onNewSchedule,
  onToggleTasks,
  onOpenSettings,
  onManageProjects,
  onOpenConnCheck,
  onLoadMoreEnded,
  onRetryLoad,
  banners,
}: {
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
  const [scopeOpen, setScopeOpen] = useState(false);
  const scopeBtn = useRef<HTMLButtonElement>(null);
  const scopeTitle = renderText(vm.scopeTitle) || t("home.all_projects");
  const isAllScope = !vm.scopeKey;

  const filtering = Boolean(vm.filterQuery);
  const connText = renderText(vm.connection?.badgeText);
  const fresh = useFresh(
    vm.running.map((r) => r.sessionId),
    vm.headline?.kind !== HeadlineKind.UNSPECIFIED,
  );

  // On desktop sidebar, show all waiting rows followed by running rows, unless Schedule view is active.
  return (
    <aside className="side">
      <div className="side-top">
        <button
          ref={scopeBtn}
          type="button"
          className={"scp" + (scopeOpen ? " open" : "")}
          disabled={!vm.scopeSelectable}
          aria-label={t("home.scope_label", { name: scopeTitle })}
          onClick={() => vm.scopeSelectable && setScopeOpen((o) => !o)}
        >
          <Avatar name={scopeTitle} size="xs" all={isAllScope} />
          <b>{scopeTitle}</b>
          <ChevronDown />
        </button>
        <div className="sp" />
        {tasks ? (
          <button
            type="button"
            className="mini"
            onClick={onNewSchedule ?? onNewTask}
          >
            {t("home.new_schedule")}
          </button>
        ) : (
          <button
            type="button"
            className="ib"
            aria-label={t("home.new_task")}
            disabled={!vm.canStart}
            onClick={onNewTask}
          >
            <Plus />
          </button>
        )}
      </div>

      {!tasks && vm.canFilter && (
        <FilterBar query={vm.filterQuery} onChange={onSetFilter} />
      )}

      {tasks && scheduleSidebar ? (
        scheduleSidebar
      ) : (
        <div className="scroll">
          <div className="home-banners">{banners}</div>

          {vm.noMatches && (
            <p className="empty">
              {renderText(vm.filterSummary) ||
                t("home.no_matches", { query: vm.filterQuery })}
            </p>
          )}

          {vm.attention.length > 0 && (
            <>
              <div className="sec">
                {t("home.pending_label", { count: vm.attention.length })}
              </div>
              {vm.attention.map((row) => (
                <button
                  key={row.sessionId}
                  type="button"
                  className={
                    "sess" + (selected === row.sessionId ? " sel" : "")
                  }
                  onClick={() => onSelectSession(row.sessionId)}
                >
                  <div className="gut">
                    <StateMark kind="wait" />
                  </div>
                  <div className="sm">
                    <div className="row1">
                      <b>{renderText(row.title)}</b>
                    </div>
                    {row.summary && (
                      <div className="pv">{renderText(row.summary)}</div>
                    )}
                  </div>
                </button>
              ))}
            </>
          )}

          {vm.running.length > 0 && (
            <>
              <div className="sec">{t("home.live")}</div>
              {vm.running.map((row, idx) => (
                <SessionRow
                  key={row.sessionId}
                  row={
                    selected && row.sessionId === selected
                      ? { ...row, selected: true }
                      : row
                  }
                  remote={remote}
                  last={idx === vm.running.length - 1}
                  fresh={fresh(row.sessionId)}
                  onSelect={() => onSelectSession(row.sessionId)}
                />
              ))}
            </>
          )}

          {!filtering &&
            vm.attention.length === 0 &&
            vm.running.length === 0 &&
            vm.endedGroups.length === 0 &&
            vm.empty?.message && (
              <div className="home-note">{renderText(vm.empty.message)}</div>
            )}

          <EndedGroups groups={vm.endedGroups} onOpenSession={onSelectSession} />

          <TailRow
            tail={vm.endedTail}
            onLoadMore={onLoadMoreEnded}
            onRetry={onRetryLoad}
          />
        </div>
      )}

      <button
        type="button"
        className="conn"
        aria-label={connText ? t("home.connection_label", { state: connText }) : t("home.connection_check")}
        title={t("home.connection_check")}
        onClick={onOpenConnCheck}
      >
        <StateMark kind={connMark(vm.connection)} />
        <span>{connText}</span>
      </button>

      <div className="sfoot">
        <Segmented
          label={t("home.sessions_or_schedules")}
          value={tasks ? "tasks" : "sessions"}
          onChange={(v) => onToggleTasks(v === "tasks")}
          options={[
            { value: "sessions", label: t("home.sessions_tab") },
            {
              value: "tasks",
              label: (
                <>
                  {t("home.schedules_tab")}
                  {scheduleAttention > 0 && (
                    <span
                      className="schedule-attention-badge"
                      aria-label={t("home.ios_badge_tasks", { count: scheduleAttention })}
                    >
                      {scheduleAttention > 99 ? "99+" : scheduleAttention}
                    </span>
                  )}
                  {scheduleAttention === 0 && scheduleNew && (
                    <span
                      className="schedule-new-badge"
                      aria-label={t("home.ios_new_results")}
                    />
                  )}
                </>
              ),
            },
          ]}
        />
        <button
          type="button"
          className={"ib" + (settingsActive ? " on" : "")}
          aria-label={t("common.settings")}
          onClick={onOpenSettings}
        >
          <Settings />
        </button>
      </div>

      {scopeOpen && (
        <ScopeMenuSheet
          rows={vm.scopeMenu}
          anchor={scopeBtn}
          onSelectScope={onSelectScope}
          onManageProjects={onManageProjects}
          onClose={() => setScopeOpen(false)}
        />
      )}
    </aside>
  );
}

/** Connected wide sidebar backed by CoreHost. */
export function Sidebar({
  remote,
  selected,
  project,
  active = "sessions",
  tasks,
  selectedTask,
  go,
  settingsActive,
}: {
  remote: Remote;
  selected?: string;
  project?: string;
  active?: "sessions" | "tasks" | "settings";
  tasks?: boolean;
  selectedTask?: string;
  go: Go;
  settingsActive?: boolean;
}) {
  useRemote(remote);
  const host = useCoreHost(remote, go);
  const [endedLimit, setEndedLimit] = useState(30);
  const vm = useHomeView(host, endedLimit);
  const [connOpen, setConnOpen] = useState(false);

  const showTasks = tasks ?? active === "tasks";
  const isSettings = settingsActive ?? active === "settings";

  useEffect(() => {
    host.dispatch(
      create(EventSchema, {
        event: {
          case: "homeSelectSession",
          value: create(HomeSelectSessionActionSchema, {
            sessionId: selected ?? "",
          }),
        },
      }),
    );
  }, [host, selected]);

  // Preserve test/reconnect behavior: don't render stale "已连接" while reconnecting during grace window.
  const connView =
    remote.conn !== "online" && !remote.showWaiting
      ? vm.connection
        ? { ...vm.connection, badgeText: undefined }
        : undefined
      : vm.connection;
  const viewVm = connView === vm.connection ? vm : { ...vm, connection: connView };

  const activeScope = project ?? vm.scopeKey;

  return (
    <>
      <SidebarView
        vm={viewVm}
        remote={remote}
        selected={selected}
        tasks={showTasks}
        scheduleSidebar={
          showTasks ? (
            <SchedulesPage remote={remote} go={go} sidebar selected={selectedTask} />
          ) : undefined
        }
        scheduleAttention={remote.scheduleBadge()}
        scheduleNew={remote.scheduleNewResults()}
        settingsActive={isSettings}
        onSelectScope={(scopeKey) => {
          host.dispatch(
            create(EventSchema, {
              event: {
                case: "homeSetScope",
                value: create(HomeSetScopeActionSchema, { scopeKey }),
              },
            }),
          );
          if (project !== undefined) go("");
        }}
        onSetFilter={(query) =>
          host.dispatch(
            create(EventSchema, {
              event: {
                case: "homeSetFilter",
                value: create(HomeSetFilterActionSchema, { query }),
              },
            }),
          )
        }
        onSelectSession={(sid) => go("s/" + encodeURIComponent(sid))}
        onNewTask={() => {
          if (activeScope) go("p/" + encodeURIComponent(activeScope));
          else go("");
        }}
        onNewSchedule={() => go("task-new")}
        onToggleTasks={(showTasks) => go(showTasks ? "tasks" : "")}
        onOpenSettings={() => go("settings")}
        onManageProjects={() => go("projects")}
        onOpenConnCheck={() => setConnOpen(true)}
        onLoadMoreEnded={() => {
          setEndedLimit((n) => n + 30);
          host.dispatch(
            create(EventSchema, {
              event: {
                case: "homeLoadMoreEnded",
                value: create(HomeLoadMoreEndedActionSchema, { step: 30 }),
              },
            }),
          );
        }}
        onRetryLoad={() => {
          host.dispatch(
            create(EventSchema, { event: { case: "homeRetryLoad", value: {} } }),
          );
          remote.retryList();
        }}
        banners={
          <>
            <UpdateBanner />
            <ClockWarning remote={remote} />
            <RecoveryNotice remote={remote} go={go} />
            <DiskReminders remote={remote} go={go} />
            <ListError remote={remote} />
            <PrefsBanner remote={remote} quiet />
          </>
        }
      />
      {connOpen && (
        <ConnCheck remote={remote} onClose={() => setConnOpen(false)} />
      )}
    </>
  );
}

/** Wide main area: start a new session directly (for all projects or a picked project). */
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
  const host = useCoreHost(remote, go);
  const homeVm = useHomeView(host, 1);
  const scopedProject = project || homeVm.scopeKey;
  const [draftKey, setDraftKey] = useState("");
  const projectKey = scopedProject || draftKey;
  const newVm = useNewSessionView(host, projectKey);
  const projectVm = useProjectView(host, projectKey, false);
  const [picking, setPicking] = useState(false);

  const hiddenNotice = projectKey ? projectVm.detail?.hiddenNotice : undefined;
  useEffect(() => {
    if (draftKey && (scopedProject || (!newVm.drafting && !hiddenNotice?.active))) {
      setDraftKey("");
    }
  }, [draftKey, scopedProject, newVm.drafting, hiddenNotice?.active]);

  const notice = hiddenNotice?.active ? (
    <RemovedNotice
      notice={hiddenNotice}
      onReadd={() =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "projectToggleHide",
              value: create(ProjectKeyActionSchema, { projectKey }),
            },
          }),
        )
      }
    />
  ) : undefined;

  if (!remote.listed) {
    return (
      <div className="page wnew">
        <div className="wstart">
          {remote.listError ? <ListError remote={remote} /> : <Skeleton />}
        </div>
      </div>
    );
  }

  if (!homeVm.canStart) {
    return (
      <div className="page wnew">
        <div className="wstart">
          <Headline vm={suppressWaitingHeadline(remote, homeVm.headline) ? undefined : homeVm.headline} />
          <div className="home-banners">
            <ListError remote={remote} />
            <PrefsBanner remote={remote} />
          </div>
          <HomeEmpty
            empty={homeVm.empty}
            onAddDevice={() => go("devices")}
            onStartFirst={() => {}}
            onRetry={() => {
              host.dispatch(
                create(EventSchema, { event: { case: "homeRetryLoad", value: {} } }),
              );
              remote.retryList();
            }}
          />
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
        notice={notice}
        onDraft={() => {
          if (!scopedProject && newVm.projectKey) {
            setDraftKey(newVm.projectKey);
          }
        }}
      />
      {picking && (
        <NewFlow
          remote={remote}
          go={go}
          onClose={() => setPicking(false)}
          onPicked={(key) => {
            setPicking(false);
            host.dispatch(
              create(EventSchema, {
                event: {
                  case: "homeSetScope",
                  value: create(HomeSetScopeActionSchema, { scopeKey: key }),
                },
              }),
            );
          }}
        />
      )}
    </div>
  );
}

export interface BottomBarAction {
  label: string;
  icon: ReactNode;
  onClick: () => void;
  disabled?: boolean;
}

function BottomBarConnected({
  remote,
  active,
  action,
  waiting,
  canStart,
  onStart,
  go,
}: {
  remote: Remote;
  active: "sessions" | "tasks";
  action?: BottomBarAction;
  waiting?: number;
  canStart?: boolean;
  onStart?: () => void;
  go: Go;
}) {
  const host = useCoreHost(remote, go);
  const vm = useHomeView(host, 1);
  return (
    <BottomBarInner
      remote={remote}
      active={active}
      action={action}
      waiting={waiting ?? vm.waitingTotal}
      canStart={canStart ?? (vm.canStart && vm.connection?.state === ConnState.CONNECTED)}
      onStart={onStart}
      go={go}
    />
  );
}

function BottomBarInner({
  remote,
  active,
  action,
  waiting,
  canStart,
  onStart,
  go,
}: {
  remote?: Remote;
  active: "sessions" | "tasks";
  action?: BottomBarAction;
  waiting: number;
  canStart: boolean;
  onStart?: () => void;
  go: Go;
}) {
  const scheduleAttention = remote?.scheduleBadge() ?? 0;
  const scheduleNew = remote?.scheduleNewResults() ?? false;
  const [picking, setPicking] = useState(false);

  const handleStart = () => {
    if (onStart) {
      onStart();
      return;
    }
    if (remote) setPicking(true);
  };

  return (
    <>
      <div className="bbar">
        <div className="tabs glass">
          <button
            type="button"
            className={active === "sessions" ? "on" : undefined}
            onClick={() => go("")}
          >
            <MessageCircle />
            {t("home.sessions_tab")}
            {waiting > 0 && (
              <span className="count">{waiting > 99 ? "99+" : waiting}</span>
            )}
          </button>
          <button
            type="button"
            className={active === "tasks" ? "on" : undefined}
            onClick={() => go("tasks")}
          >
            <Clock />
            {t("home.schedules_tab")}
            {scheduleAttention > 0 && (
              <span
                className="schedule-attention-badge"
                aria-label={t("home.ios_badge_tasks", { count: scheduleAttention })}
              >
                {scheduleAttention > 99 ? "99+" : scheduleAttention}
              </span>
            )}
            {scheduleAttention === 0 && scheduleNew && (
              <span
                className="schedule-new-badge"
                aria-label={t("home.ios_new_results")}
              />
            )}
          </button>
        </div>

        <button
          type="button"
          className="fab"
          disabled={action ? action.disabled : !canStart}
          onClick={action ? action.onClick : handleStart}
        >
          {action ? action.icon : <Plus />}
          {action ? action.label : t("home.new_task")}
        </button>
      </div>
      {picking && remote && (
        <NewFlow remote={remote} go={go} onClose={() => setPicking(false)} />
      )}
    </>
  );
}

/** Floating bottom tabs + primary pill on phone. */
export function BottomBar(props: {
  remote?: Remote;
  active: "sessions" | "tasks";
  action?: BottomBarAction;
  waiting?: number;
  canStart?: boolean;
  onStart?: () => void;
  go: Go;
}) {
  if (props.remote) {
    return <BottomBarConnected {...props} remote={props.remote} />;
  }
  return (
    <BottomBarInner
      active={props.active}
      action={props.action}
      waiting={props.waiting ?? 0}
      canStart={props.canStart ?? false}
      onStart={props.onStart}
      go={props.go}
    />
  );
}
