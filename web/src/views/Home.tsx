// The home screen: scope, headline, what waits, what runs, what ended,
// sidebar, and wide main area — all rendered straight from HomeViewModel.

import { create } from "@bufbuild/protobuf";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { AgentMark } from "../agent-mark";
import { getCoreHost, useCoreHost, useHomeView, useNewSessionView, useProjectView, useSchedulesView, useSessionView, useSyncState, type Go } from "../core/host";
import type { SyncStateView } from "../gen/remoteagent/core/v1/sync_view_pb";
import { renderText } from "../core/text";
import { SessionCommandStatus, SessionDockKind } from "../gen/remoteagent/core/v1/session_view_pb";
import { CommandSchema, type Command } from "../gen/remoteagent/v1/remote_pb";
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
  type HomeQuestionCardView,
  type HomeViewModel,
  type ScopeRowView,
  type SessionRowView,
  type TailRowView,
} from "../gen/remoteagent/core/v1/home_view_pb";
import { t } from "../i18n";
import { Check, ChevronDown, ChevronRight, Clock, LoaderCircle, MessageCircle, Plus, Search, Settings, X } from "../icons";
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
  const host = useCoreHost(remote);
  const sync = useSyncState(host);
  const msg = renderText(sync.sessionListError);
  return msg ? <Banner kind="err" action={t("common.retry")} onAction={() => host.homeRetryLoad()}>{msg}</Banner> : null;
}

function HomeBanners({ remote, go, quiet }: { remote: Remote; go: Go; quiet?: boolean }) {
  const host = useCoreHost(remote);
  const sync = useSyncState(host);
  const clockWarning = sync.accountClockSkewed ? t("errors.this_device_s_clock_is_behind_the_account_state") : "";
  return (
    <div className="home-banners">
      <UpdateBanner />
      {clockWarning && <Banner kind="warn">{clockWarning}</Banner>}
      <RecoveryNotice remote={remote} go={go} />
      <DiskReminders remote={remote} go={go} />
      <ListError remote={remote} />
      <PrefsBanner remote={remote} quiet={quiet} />
    </div>
  );
}

function suppressWaitingHeadline(sync?: SyncStateView, vm?: HomeHeadlineView): boolean {
  if (!sync || !vm || !sync.displayGrace || sync.connection === ConnState.CONNECTED) return false;
  return vm.isConnecting;
}

function Headline({ vm, onDetails }: { vm?: HomeHeadlineView; onDetails?: () => void }) {
  const prevKind = useRef<HeadlineKind | undefined>(undefined);
  const [crossfade, setCrossfade] = useState(false);

  useEffect(() => {
    const kind = vm?.kind;
    if (prevKind.current === HeadlineKind.WAITING && kind !== undefined && kind !== HeadlineKind.WAITING) {
      setCrossfade(true);
      const timer = setTimeout(() => setCrossfade(false), 200);
      prevKind.current = kind;
      return () => clearTimeout(timer);
    }
    prevKind.current = kind;
    return undefined;
  }, [vm?.kind]);

  if (!vm) return null;
  const hlClass = crossfade ? "hl-wrap crossfade" : "hl-wrap";
  const subText = renderText(vm.sub);
  return (
    <div className={hlClass}>
      <h1 className="hl">{highlightTitle(renderText(vm.title), vm.highlightCount)}</h1>
      {(subText || vm.detailsAction) && (
        <div className={"hl-sub" + (vm.subError ? " err" : "") + (vm.subPath ? " path" : "")}>
          {vm.subPath && subText ? <PathText path={subText} /> : subText}
          {vm.detailsAction && onDetails && <button type="button" onClick={onDetails}>{t("common.details")}</button>}
        </div>
      )}
    </div>
  );
}

function HomeEmpty({ empty, onAddDevice, onStartFirst, onRetry }: { empty?: HomeEmptyView; onAddDevice: () => void; onStartFirst: (machineId: string) => void; onRetry: () => void }) {
  if (!empty || empty.kind === HomeEmptyKind.UNSPECIFIED || empty.kind === HomeEmptyKind.NONE || empty.kind === HomeEmptyKind.LOADING) return null;
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

function TopQuestionSlot({
  question,
  remote,
  onOpen,
  onOpenTerminal,
}: {
  question?: HomeQuestionCardView;
  remote?: Remote;
  onOpen: (sessionId: string) => void;
  onOpenTerminal: (sessionId: string) => void;
}) {
  const [held, setHeld] = useState(question as HomeQuestionCardView | undefined);
  const [phase, setPhase] = useState("idle" as "idle" | "sending" | "error" | "delivered" | "elsewhere" | "exiting");
  const [exitKind, setExitKind] = useState("delivered" as "delivered" | "elsewhere");
  const [picked, setPicked] = useState(undefined as { num: number; label: string } | undefined);
  const [showSpinner, setShowSpinner] = useState(false);
  const [errMsg, setErrMsg] = useState("");
  const spinTimer = useRef(undefined as ReturnType<typeof setTimeout> | undefined);

  useEffect(() => () => clearTimeout(spinTimer.current), []);

  // Track changes to active question (switch, external answer, or cleared after submit)
  useEffect(() => {
    if (question) {
      if (!held || held.sessionId !== question.sessionId || held.questionId !== question.questionId) {
        clearTimeout(spinTimer.current);
        setShowSpinner(false);
        setPicked(undefined);
        setErrMsg("");
        setPhase("idle");
        setHeld(question);
      } else {
        setHeld(question);
      }
      return;
    }
    if (!held) return;
    if (phase === "sending") {
      clearTimeout(spinTimer.current);
      setShowSpinner(false);
      setExitKind("delivered");
      setPhase("delivered");
    } else if (phase === "idle" || phase === "error") {
      clearTimeout(spinTimer.current);
      setShowSpinner(false);
      setExitKind("elsewhere");
      setPhase("elsewhere");
    }
  }, [question, held, phase]);

  // 900ms confirmation hold -> 160ms collapse fade
  useEffect(() => {
    if (phase !== "delivered" && phase !== "elsewhere") return;
    const t1 = setTimeout(() => setPhase("exiting"), 900);
    return () => clearTimeout(t1);
  }, [phase]);

  useEffect(() => {
    if (phase !== "exiting") return;
    const t2 = setTimeout(() => {
      setHeld(undefined);
      setPicked(undefined);
      setErrMsg("");
      setPhase("idle");
    }, 160);
    return () => clearTimeout(t2);
  }, [phase]);

  if (!held) return null;

  const finishDelivery = (label: string) => {
    clearTimeout(spinTimer.current);
    setShowSpinner(false);
    setPicked((p) => p ?? { num: 0, label });
    setExitKind("delivered");
    setPhase("delivered");
  };

  if (phase === "delivered" || phase === "elsewhere" || phase === "exiting") {
    const isElsewhere = phase === "elsewhere" || (phase === "exiting" && exitKind === "elsewhere");
    const label = isElsewhere
      ? t("home.question_answered_elsewhere")
      : t("home.question_answered_line", { answer: picked?.label || "" });
    return (
      <div className={"tc line" + (phase === "exiting" ? " q-exit" : "")} role="status">
        <div className="k">
          {!isElsewhere && <Check />}
          <span>{label}</span>
        </div>
      </div>
    );
  }

  return remote ? (
    <TopQuestionLive
      card={held}
      remote={remote}
      phase={phase}
      picked={picked}
      showSpinner={showSpinner}
      errMsg={errMsg}
      onStartSend={(num, label) => {
        clearTimeout(spinTimer.current);
        setErrMsg("");
        setPicked({ num, label });
        setPhase("sending");
        setShowSpinner(false);
        spinTimer.current = setTimeout(() => setShowSpinner(true), 150);
      }}
      onDelivered={finishDelivery}
      onError={(msg) => {
        clearTimeout(spinTimer.current);
        setShowSpinner(false);
        setErrMsg(msg);
        setPhase("error");
      }}
      onOpen={() => onOpen(held.sessionId)}
      onOpenTerminal={() => onOpenTerminal(held.sessionId)}
    />
  ) : (
    <TopQuestionCardView
      card={held}
      phase={phase}
      picked={picked}
      showSpinner={showSpinner}
      errMsg={errMsg}
      onPick={(opt) => {
        setPicked({ num: opt.optionNum, label: opt.label });
        setPhase("delivered");
      }}
      onRetry={() => setPhase("idle")}
      onOpen={() => onOpen(held.sessionId)}
      onOpenTerminal={() => onOpenTerminal(held.sessionId)}
    />
  );
}

function TopQuestionLive({
  card,
  remote,
  phase,
  picked,
  showSpinner,
  errMsg,
  onStartSend,
  onDelivered,
  onError,
  onOpen,
  onOpenTerminal,
}: {
  card: HomeQuestionCardView;
  remote: Remote;
  phase: "idle" | "sending" | "error";
  picked?: { num: number; label: string };
  showSpinner: boolean;
  errMsg: string;
  onStartSend: (num: number, label: string) => void;
  onDelivered: (label: string) => void;
  onError: (msg: string) => void;
  onOpen: () => void;
  onOpenTerminal: () => void;
}) {
  const host = useCoreHost(remote);
  const vm = useSessionView(host, card.sessionId);
  const dock = vm.dock;
  const cmd =
    dock?.hasItem && dock.kind === SessionDockKind.QUESTION && dock.itemId === card.questionId
      ? dock.command
      : undefined;
  const machineId = vm.header?.machineId || card.machineId || "";
  const slotKey = cmd?.slotKey || `question:${card.sessionId}:${card.questionId}#0`;
  const lastCmd = useRef<Command | undefined>(undefined);
  const wasBusy = useRef(false);

  const cmdBusy = !!cmd?.busy;
  const cmdError =
    !!cmd &&
    (cmd.status === SessionCommandStatus.FAILED || cmd.status === SessionCommandStatus.UNKNOWN || !!cmd.expired);
  const cmdErrText = renderText(cmd?.errorText) || renderText(cmd?.statusText);

  useEffect(() => {
    if (cmdBusy) {
      wasBusy.current = true;
      return;
    }
    if (phase === "sending" && wasBusy.current) {
      wasBusy.current = false;
      if (cmdError) {
        onError(cmdErrText || t("composer.android_unknown_reason"));
      } else {
        onDelivered(picked?.label || "");
      }
    }
  }, [cmdBusy, cmdError, cmdErrText, phase, picked?.label, onDelivered, onError]);

  const sendOption = (num: number, label: string) => {
    if (phase === "sending" || !card.canAnswer) return;
    wasBusy.current = false;
    onStartSend(num, label);
    const built = create(CommandSchema, {
      kind: {
        case: "answerQuestion",
        value: {
          sessionId: card.sessionId,
          questionId: card.questionId,
          option: num,
          questionIndex: 0,
          options: [],
          observedEventSeq: card.observedEventSeq,
        },
      },
    });
    lastCmd.current = built;
    host.sessionSubmitCommand(slotKey, machineId, card.sessionId, built);
  };

  return (
    <TopQuestionCardView
      card={card}
      phase={phase}
      picked={picked}
      showSpinner={showSpinner}
      errMsg={errMsg}
      onPick={(opt) => sendOption(opt.optionNum, opt.label)}
      onRetry={() => {
        if (picked) {
          onStartSend(picked.num, picked.label);
          const built =
            lastCmd.current ??
            create(CommandSchema, {
              kind: {
                case: "answerQuestion",
                value: {
                  sessionId: card.sessionId,
                  questionId: card.questionId,
                  option: picked.num,
                  questionIndex: 0,
                  options: [],
                  observedEventSeq: card.observedEventSeq,
                },
              },
            });
          host.sessionSubmitCommand(slotKey, machineId, card.sessionId, built);
        }
      }}
      onOpen={onOpen}
      onOpenTerminal={onOpenTerminal}
    />
  );
}

function TopQuestionCardView({
  card,
  phase,
  picked,
  showSpinner,
  errMsg,
  onPick,
  onRetry,
  onOpen,
  onOpenTerminal,
}: {
  card: HomeQuestionCardView;
  phase: "idle" | "sending" | "error";
  picked?: { num: number; label: string };
  showSpinner: boolean;
  errMsg: string;
  onPick: (opt: HomeQuestionCardView["options"][number]) => void;
  onRetry: () => void;
  onOpen: () => void;
  onOpenTerminal: () => void;
}) {
  const sending = phase === "sending";
  const rightMeta = !card.canAnswer
    ? renderText(card.offlineNote) || t("list_state.unlinked")
    : renderText(card.timeText);
  const title = renderText(card.title);
  const where = renderText(card.where);

  return (
    <div className="tc">
      <div className="k">
        <i />
        {renderText(card.badgeText) || t("dock.answer_title")}
        {rightMeta && <span>{rightMeta}</span>}
      </div>
      <h4>
        <button type="button" onClick={onOpen}>
          <span>{title}</span>
          <ChevronRight />
        </button>
      </h4>
      {where && <div className="m">{where}</div>}
      <div className="q">{card.questionText}</div>
      <div className="q-opts">
        {card.options.map((opt) => {
          const isPicked = picked?.num === opt.optionNum;
          const cls =
            "q-opt" +
            (isPicked ? " picking" : sending ? " dimmed" : "");
          return (
            <button
              key={opt.optionNum}
              type="button"
              className={cls}
              disabled={!card.canAnswer || sending}
              onClick={() => onPick(opt)}
            >
              <div>
                <b>
                  {opt.label}
                  {opt.recommended ? ` · ${t("dock.recommended")}` : ""}
                </b>
                {opt.description && <small>{opt.description}</small>}
              </div>
              {sending && isPicked && showSpinner && <LoaderCircle />}
            </button>
          );
        })}
      </div>
      {phase === "error" && (
        <div className="q-err" role="alert">
          <span>{t("home.question_send_failed", { reason: errMsg })}</span>
          <button type="button" onClick={onRetry}>{t("common.retry")}</button>
        </div>
      )}
      <div className="q-foot">
        <span>{t("dock.none_fit")}</span>
        <button type="button" onClick={onOpenTerminal}>
          {t("home.question_answer_in_terminal")}
          <ChevronRight />
        </button>
      </div>
    </div>
  );
}

function TopApproval({ card, remote, onOpen }: { card: AttentionCardView; remote?: Remote; onOpen: () => void }) {
  if (remote) return <TopApprovalLive card={card} remote={remote} onOpen={onOpen} />;
  return <TopApprovalPreview card={card} onOpen={onOpen} />;
}

function TopApprovalLive({ card, remote, onOpen }: { card: AttentionCardView; remote: Remote; onOpen: () => void }) {
  const host = useCoreHost(remote);
  const vm = useSessionView(host, card.sessionId);
  const lastCmd = useRef<Command | undefined>(undefined);
  const dock = vm.dock;
  const dockApproval =
    dock?.hasItem && dock.kind === SessionDockKind.APPROVAL && dock.detail.case === "approval" && dock.detail.value.approvalId === card.approvalId
      ? dock.detail.value
      : undefined;
  const command =
    dock?.hasItem && dock.kind === SessionDockKind.APPROVAL && dock.itemId === card.approvalId
      ? dock.command
      : undefined;
  const machineId = vm.header?.machineId ?? "";
  const slotKey = command?.slotKey || `approval:${card.sessionId}:${card.approvalId}`;
  const reason = remote.approvalReasons.get(card.approvalId) ?? "";

  const makeCmd = (allow: boolean, why: string, always: boolean): Command =>
    create(CommandSchema, {
      kind: {
        case: "resolveApproval",
        value: {
          sessionId: card.sessionId,
          approvalId: card.approvalId,
          allow,
          message: why,
          always,
          observedEventSeq: dock?.observedSeq ?? 0n,
        },
      },
    });

  return (
    <ApprovalCard
      kind={renderText(card.kindText)}
      body={dockApproval ? (dockApproval.body || dockApproval.commandPreview || dockApproval.toolName) : (card.body || card.toolName)}
      description={dockApproval?.description}
      truncated={dockApproval?.bodyTruncated ?? card.truncated}
      isSkeleton={dockApproval?.isSkeleton}
      canAlwaysAllow={dockApproval?.canAlwaysAllow ?? !!renderText(card.alwaysLabel)}
      alwaysLabel={renderText(dockApproval?.alwaysLabel) || renderText(card.alwaysLabel)}
      denyReasonPlaceholder={renderText(dockApproval?.denyReasonPlaceholder)}
      timeText={renderText(card.time)}
      hot={card.hot}
      locked={card.locked || (vm.header?.found ? !vm.header.capabilities?.canSend : false)}
      command={command}
      reason={reason}
      onReasonChange={(v) => {
        if (v) remote.approvalReasons.set(card.approvalId, v);
        else remote.approvalReasons.delete(card.approvalId);
        remote.touch();
      }}
      title={renderText(card.title)}
      where={renderText(card.where)}
      onOpen={onOpen}
      onResolve={(allow, why, always) => {
        const built = makeCmd(allow, why, always);
        lastCmd.current = built;
        host.sessionSubmitCommand(slotKey, machineId, card.sessionId, built);
      }}
      onRetry={() => {
        const built = lastCmd.current ?? makeCmd(true, "", false);
        host.sessionSubmitCommand(slotKey, machineId, card.sessionId, built);
      }}
      onAbandon={() => host.sessionAbandonCommand(slotKey, card.sessionId)}
    />
  );
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
export function SessionRow({ row, onSelect, last, fresh }: { row: SessionRowView; onSelect: () => void; last?: boolean; fresh?: boolean }) {
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
  onOpenSessionTerminal?: (sessionId: string) => void;
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
  const qCard = !card ? vm.topAttentionQuestion : undefined;
  const showTopCard = Boolean(card && card.sessionId && !filtering);
  const showTopQuestion = Boolean(qCard && qCard.sessionId && !filtering);
  const excludedTopId = showTopCard ? card!.sessionId : showTopQuestion ? qCard!.sessionId : "";
  const restAttention = excludedTopId ? vm.attention.filter((a) => a.sessionId !== excludedTopId) : vm.attention;
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
        <Headline vm={suppressWaitingHeadline(remote ? getCoreHost(remote).viewSyncState() : undefined, vm.headline) ? undefined : vm.headline} onDetails={p.onOpenDetails} />
        {p.banners}
        <HomeEmpty empty={vm.empty} onAddDevice={p.onAddDevice} onStartFirst={p.onStart} onRetry={p.onRetryLoad} />
        {vm.canFilter && <FilterBar query={vm.filterQuery} onChange={p.onSetFilter} />}
        {showTopCard && card && <TopApproval card={card} remote={remote} onOpen={() => p.onOpenSession(card.sessionId)} />}
        {!showTopCard && (
          <TopQuestionSlot
            question={!filtering ? qCard : undefined}
            remote={remote}
            onOpen={p.onOpenSession}
            onOpenTerminal={p.onOpenSessionTerminal ?? p.onOpenSession}
          />
        )}
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
              <SessionRow key={row.sessionId} row={row} last={idx === shownRunning.length - 1 && hiddenRunning === 0} fresh={fresh(row.sessionId)} onSelect={() => p.onOpenSession(row.sessionId)} />
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
  const host = useCoreHost(remote, go);
  const sync = useSyncState(host);
  const [endedLimit, setEndedLimit] = useState(30);
  const vm = useHomeView(host, endedLimit);
  const [connOpen, setConnOpen] = useState(false);
  const [picking, setPicking] = useState(false);

  const heldEnded = vm.endedGroups.reduce((n, g) => n + g.items.length, 0);
  useEffect(() => {
    if (!vm.filterQuery && sync.hasMoreSessions && (vm.endedTail?.kind === TailKind.MORE_ENDED || vm.endedTail?.kind === TailKind.MORE_ON_SERVER) && heldEnded < endedLimit) {
      void remote.loadMoreSessions();
    }
  }, [vm.filterQuery, vm.endedTail?.kind, remote, sync.hasMoreSessions, heldEnded, endedLimit]);

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
        onOpenSessionTerminal={(sid) => {
          remote.termFirst.add(sid);
          go("s/" + encodeURIComponent(sid));
        }}
        onOpenSettings={() => go("settings")}
        onManageProjects={() => go("projects")}
        onAddDevice={() => go("devices")}
        onStart={startTask}
        onLoadMoreEnded={() => { setEndedLimit((n) => n + 30); host.homeLoadMoreEnded(30); }}
        onRetryLoad={() => host.homeRetryLoad()}
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
  const { vm, selected, tasks = false } = p;
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
                <SessionRow key={row.sessionId} row={selected && row.sessionId === selected ? { ...row, selected: true } : row} last={idx === vm.running.length - 1} fresh={fresh(row.sessionId)} onSelect={() => p.onSelectSession(row.sessionId)} />
              ))}
            </>
          )}
          {!vm.filterQuery && vm.attention.length === 0 && vm.running.length === 0 && vm.endedGroups.length === 0 && vm.empty?.message && (
            <div className={vm.empty.kind === HomeEmptyKind.LOADING ? "home-note late" : "home-note"}>{renderText(vm.empty.message)}</div>
          )}
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
  const sync = useSyncState(host);
  const schedVm = useSchedulesView(host, 1);
  const [connOpen, setConnOpen] = useState(false);
  const showTasks = tasks ?? active === "tasks";

  useEffect(() => { host.homeSelectSession(selected ?? ""); }, [host, selected]);

  // Don't render stale "已连接" while reconnecting during grace window.
  const connView = sync.connection !== ConnState.CONNECTED && sync.displayGrace ? (vm.connection ? { ...vm.connection, badgeText: undefined } : undefined) : vm.connection;
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
        scheduleAttention={schedVm.attentionCount}
        scheduleNew={schedVm.hasNewResults}
        settingsActive={settingsActive ?? active === "settings"}
        onSelectScope={(scopeKey) => { host.homeSetScope(scopeKey); if (project !== undefined) go(""); }}
        onSetFilter={host.homeSetFilter}
        onSelectSession={(sid) => go("s/" + encodeURIComponent(sid))}
        onNewTask={() => go(activeScope ? "p/" + encodeURIComponent(activeScope) : "")}
        onNewSchedule={() => go("tasks/new")}
        onToggleTasks={(on) => go(on ? "tasks" : "")}
        onOpenSettings={() => go("settings")}
        onManageProjects={() => go("projects")}
        onOpenConnCheck={() => setConnOpen(true)}
        onLoadMoreEnded={() => { setEndedLimit((n) => n + 30); host.homeLoadMoreEnded(30); }}
        onRetryLoad={() => host.homeRetryLoad()}
        banners={<HomeBanners remote={remote} go={go} quiet />}
      />
      {connOpen && <ConnCheck remote={remote} onClose={() => setConnOpen(false)} />}
    </>
  );
}

/** Wide main area: start a new session directly (for all projects or a picked project). */
export function WideHome({ remote, go, project }: { remote: Remote; go: Go; project?: string }) {
  const host = useCoreHost(remote, go);
  const sync = useSyncState(host);
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

  if (!homeVm.canStart) {
    if (homeVm.empty?.kind === HomeEmptyKind.LOADING || homeVm.empty?.kind === HomeEmptyKind.NONE) {
      return <div className="page wnew"><div className="wstart">{renderText(sync.sessionListError) ? <ListError remote={remote} /> : <div data-late="1"><Skeleton /></div>}</div></div>;
    }
    return (
      <div className="page wnew">
        <div className="wstart">
          <Headline vm={suppressWaitingHeadline(sync, homeVm.headline) ? undefined : homeVm.headline} />
          <div className="home-banners"><ListError remote={remote} /><PrefsBanner remote={remote} /></div>
          <HomeEmpty empty={homeVm.empty} onAddDevice={() => go("devices")} onStartFirst={() => {}} onRetry={() => host.homeRetryLoad()} />
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

function BottomBarInner(p: { remote?: Remote; active: "sessions" | "tasks"; action?: BottomBarAction; waiting: number; canStart: boolean; scheduleAttention?: number; scheduleNew?: boolean; onStart?: () => void; go: Go }) {
  const [picking, setPicking] = useState(false);
  const prevWaiting = useRef(p.waiting);
  const [exitingBadge, setExitingBadge] = useState(undefined as number | undefined);

  useEffect(() => {
    if (prevWaiting.current > 0 && p.waiting === 0) {
      setExitingBadge(prevWaiting.current);
      const timer = setTimeout(() => setExitingBadge(undefined), 120);
      prevWaiting.current = p.waiting;
      return () => clearTimeout(timer);
    }
    if (p.waiting > 0) setExitingBadge(undefined);
    prevWaiting.current = p.waiting;
    return undefined;
  }, [p.waiting]);

  const badgeNum = p.waiting ? p.waiting : exitingBadge;

  return (
    <>
      <div className="bbar">
        <div className="tabs glass">
          <button type="button" className={p.active === "sessions" ? "on" : undefined} onClick={() => p.go("")}>
            <MessageCircle />{t("home.sessions_tab")}{badgeNum !== undefined && <span className={"count" + (p.waiting === 0 ? " exit" : "")}>{badgeNum > 99 ? "99+" : badgeNum}</span>}
          </button>
          <button type="button" className={p.active === "tasks" ? "on" : undefined} onClick={() => p.go("tasks")}>
            <Clock />{t("home.schedules_tab")}<ScheduleBadges attention={p.scheduleAttention ?? 0} hasNew={p.scheduleNew ?? false} />
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
  const host = useCoreHost(p.remote, p.go);
  const vm = useHomeView(host, 1);
  const schedVm = useSchedulesView(host, 1);
  return (
    <BottomBarInner
      {...p}
      waiting={p.waiting ?? vm.waitingTotal}
      canStart={p.canStart ?? (vm.canStart && vm.connection?.state === ConnState.CONNECTED)}
      scheduleAttention={schedVm.attentionCount}
      scheduleNew={schedVm.hasNewResults}
    />
  );
}

/** Floating bottom tabs + primary pill on phone. */
export function BottomBar(props: { remote?: Remote; active: "sessions" | "tasks"; action?: BottomBarAction; waiting?: number; canStart?: boolean; onStart?: () => void; go: Go }) {
  return props.remote ? <BottomBarConnected {...props} remote={props.remote} /> : <BottomBarInner {...props} waiting={props.waiting ?? 0} canStart={props.canStart ?? false} />;
}
